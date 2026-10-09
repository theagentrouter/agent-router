// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewAnthropicToAnthropicTranslator creates a passthrough translator for Anthropic.
// The prefix defaults to "v1" via schemaToFilterAPI, producing "/v1/messages".
// AWS and GCP Anthropic wrappers pass empty prefix as they override the request path entirely.
func NewAnthropicToAnthropicTranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) AnthropicMessagesTranslator {
	return &anthropicToAnthropicTranslator{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "messages"),
	}
}

type anthropicToAnthropicTranslator struct {
	modelNameOverride      internalapi.ModelNameOverride
	path                   string
	requestModel           internalapi.RequestModel
	stream                 bool
	buffered               []byte
	streamingResponseModel internalapi.ResponseModel
	streamingTokenUsage    metrics.TokenUsage
	// Redaction configuration for debug logging
	debugLogEnabled bool
	enableRedaction bool
	logger          *slog.Logger
}

// RequestBody implements [AnthropicMessagesTranslator.RequestBody].
func (a *anthropicToAnthropicTranslator) RequestBody(original []byte, body *anthropic.MessagesRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	a.stream = body.Stream
	// Store the request model to use as fallback for response model
	a.requestModel = body.Model
	if a.modelNameOverride != "" {
		// If modelName is set we override the model to be used for the request.
		newBody, err = sjson.SetBytesOptions(original, "model", a.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
		// Make everything coherent.
		a.requestModel = a.modelNameOverride
	}

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)

	newHeaders = []internalapi.Header{{pathHeaderName, a.path}}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [AnthropicMessagesTranslator.ResponseHeaders].
func (a *anthropicToAnthropicTranslator) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [AnthropicMessagesTranslator.ResponseBody].
func (a *anthropicToAnthropicTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.MessageSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	if a.stream {
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, tokenUsage, a.requestModel, fmt.Errorf("failed to read body: %w", err)
		}

		a.buffered = append(a.buffered, buf...)
		a.extractUsageFromBufferEvent(span)
		// Use stored streaming response model, fallback to request model for non-compliant backends
		responseModel = cmp.Or(a.streamingResponseModel, a.requestModel)
		return nil, nil, a.streamingTokenUsage, responseModel, nil
	}

	// Parse the Anthropic response to extract token usage.
	anthropicResp := &anthropic.MessagesResponse{}
	if err := json.NewDecoder(body).Decode(anthropicResp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	// Redact and log response when enabled
	if a.debugLogEnabled && a.enableRedaction && a.logger != nil {
		redactedResp := a.RedactAnthropicBody(anthropicResp)
		if jsonBody, marshalErr := json.Marshal(redactedResp); marshalErr == nil {
			a.logger.Debug("response body processing", slog.Any("response", string(jsonBody)))
		}
	}

	usage := anthropicResp.Usage
	tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(
		int64(usage.InputTokens),
		int64(usage.OutputTokens),
		ptr.To(int64(usage.CacheReadInputTokens)),
		ptr.To(int64(usage.CacheCreationInputTokens)),
	)
	if span != nil {
		span.RecordResponse(anthropicResp)
	}
	responseModel = cmp.Or(anthropicResp.Model, a.requestModel)
	return nil, nil, tokenUsage, responseModel, nil
}

// extractUsageFromBufferEvent extracts the token usage from the buffered event.
// It scans complete lines and accumulates usage from all events in this batch.
func (a *anthropicToAnthropicTranslator) extractUsageFromBufferEvent(s tracingapi.MessageSpan) {
	for {
		i := bytes.IndexByte(a.buffered, '\n')
		if i == -1 {
			// Recalculate total tokens before returning
			a.updateTotalTokens()
			return
		}
		line := a.buffered[:i]
		a.buffered = a.buffered[i+1:]
		data, ok := cutSSEDataPrefix(line)
		if !ok {
			continue
		}
		eventUnion := &anthropic.MessagesStreamChunk{}
		if err := json.Unmarshal(data, eventUnion); err != nil {
			continue
		}
		if s != nil {
			s.RecordResponseChunk(eventUnion)
		}
		a.reflectStreamingEvent(eventUnion)
	}
}

func (a *anthropicToAnthropicTranslator) reflectStreamingEvent(eventUnion *anthropic.MessagesStreamChunk) {
	switch {
	case eventUnion.MessageStart != nil:
		message := eventUnion.MessageStart
		// Store the response model for future batches
		if message.Model != "" {
			a.streamingResponseModel = message.Model
		}
		// Extract usage from message_start event - this sets the baseline input tokens
		if u := message.Usage; u != nil {
			messageStartUsage := metrics.ExtractTokenUsageFromExplicitCaching(
				int64(u.InputTokens),
				int64(u.OutputTokens),
				ptr.To(int64(u.CacheReadInputTokens)),
				ptr.To(int64(u.CacheCreationInputTokens)),
			)
			// Override with message_start usage (contains input tokens and initial state)
			a.streamingTokenUsage.Override(messageStartUsage)
		}
	case eventUnion.MessageDelta != nil:
		u := eventUnion.MessageDelta.Usage
		// message_delta carries the final counts. Standard Anthropic only reports output_tokens
		// here, but some Anthropic-compatible backends report the final input/cache counts on
		// message_delta instead of message_start. See https://github.com/envoyproxy/ai-gateway/issues/2290.
		//
		// output_tokens is always the final value on message_delta, so take it unconditionally.
		if u.OutputTokens >= 0 {
			a.streamingTokenUsage.SetOutputTokens(uint32(u.OutputTokens)) //nolint:gosec
		}
		// Merge the input/cache counts per field rather than replacing the whole usage snapshot:
		// a delta may report only the fields that apply (the rest arrive as zero), so overwriting
		// every field would clobber values already set on message_start. We can only treat a field
		// as "present" when it is non-zero, since the upstream usage fields are not pointers.
		if u.InputTokens > 0 || u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
			// The unified input_tokens is the sum of raw input + cache-read + cache-creation, so
			// recover the latest known value of each component and overwrite only the ones present
			// on this delta before recomputing the sum.
			cacheRead, _ := a.streamingTokenUsage.CachedInputTokens()
			cacheCreation, _ := a.streamingTokenUsage.CacheCreationInputTokens()
			unifiedInput, _ := a.streamingTokenUsage.InputTokens()
			var rawInput uint32
			if unifiedInput >= cacheRead+cacheCreation {
				rawInput = unifiedInput - cacheRead - cacheCreation
			}

			if u.InputTokens > 0 {
				rawInput = uint32(u.InputTokens) //nolint:gosec
			}
			if u.CacheReadInputTokens > 0 {
				cacheRead = uint32(u.CacheReadInputTokens) //nolint:gosec
			}
			if u.CacheCreationInputTokens > 0 {
				cacheCreation = uint32(u.CacheCreationInputTokens) //nolint:gosec
			}

			a.streamingTokenUsage.SetCachedInputTokens(cacheRead)
			a.streamingTokenUsage.SetCacheCreationInputTokens(cacheCreation)
			a.streamingTokenUsage.SetInputTokens(rawInput + cacheRead + cacheCreation)
		}
	}
}

// updateTotalTokens recalculates and sets the total token count
func (a *anthropicToAnthropicTranslator) updateTotalTokens() {
	inputTokens, inputSet := a.streamingTokenUsage.InputTokens()
	outputTokens, outputSet := a.streamingTokenUsage.OutputTokens()

	// Initialize missing values to 0 if we have any token data
	if outputSet && !inputSet {
		a.streamingTokenUsage.SetInputTokens(0)
		inputTokens = 0
		inputSet = true
	}

	// Set cached tokens to 0 if not set but we have other token data
	if outputSet {
		if _, cachedSet := a.streamingTokenUsage.CachedInputTokens(); !cachedSet {
			a.streamingTokenUsage.SetCachedInputTokens(0)
		}
		if _, cachedSet := a.streamingTokenUsage.CacheCreationInputTokens(); !cachedSet {
			a.streamingTokenUsage.SetCacheCreationInputTokens(0)
		}
	}

	if inputSet && outputSet {
		a.streamingTokenUsage.SetTotalTokens(inputTokens + outputTokens)
	}
}

// ResponseError implements [AnthropicMessagesTranslator] for Anthropic to AWS Bedrock Anthropic translation.
func (a *anthropicToAnthropicTranslator) ResponseError(respHeaders map[string]string, r io.Reader) (
	newHeaders []internalapi.Header,
	mutatedBody []byte,
	err error,
) {
	statusCode := respHeaders[statusHeaderName]
	if !strings.Contains(respHeaders[contentTypeHeaderName], jsonContentType) {
		buf, err := io.ReadAll(r)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		var typ string
		switch statusCode {
		case "400":
			typ = "invalid_request_error"
		case "401":
			typ = "authentication_error"
		case "403":
			typ = "permission_error"
		case "404":
			typ = "not_found_error"
		case "413":
			typ = "request_too_large"
		case "429":
			typ = "rate_limit_error"
		case "500":
			typ = "internal_server_error"
		case "503":
			typ = "service_unavailable_error"
		case "529":
			typ = "overloaded_error"
		default:
			typ = "internal_server_error"
		}
		anthropicError := anthropic.ErrorResponse{
			Type:  "error", // Always "error" at the top level.
			Error: anthropic.ErrorResponseMessage{Type: typ, Message: string(buf)},
		}
		mutatedBody, err = json.Marshal(anthropicError)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
		}
		newHeaders = append(newHeaders,
			internalapi.Header{contentTypeHeaderName, jsonContentType},
			internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(mutatedBody))},
		)
	}
	return
}

// SetRedactionConfig implements [AnthropicResponseRedactor.SetRedactionConfig].
func (a *anthropicToAnthropicTranslator) SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger) {
	a.debugLogEnabled = debugLogEnabled
	a.enableRedaction = enableRedaction
	a.logger = logger
}

// RedactAnthropicBody implements [AnthropicResponseRedactor.RedactAnthropicBody].
// Creates a redacted copy of the Anthropic response for safe logging without modifying the original.
func (a *anthropicToAnthropicTranslator) RedactAnthropicBody(resp *anthropic.MessagesResponse) *anthropic.MessagesResponse {
	if resp == nil {
		return nil
	}

	// Create a shallow copy of the response
	redacted := *resp

	// Redact content blocks (contains AI-generated content)
	if len(resp.Content) > 0 {
		redacted.Content = make([]anthropic.MessagesContentBlock, len(resp.Content))
		for i := range resp.Content {
			redacted.Content[i] = redactAnthropicContent(&resp.Content[i])
		}
	}

	return &redacted
}

// redactAnthropicContent redacts sensitive content from an Anthropic content block.
func redactAnthropicContent(content *anthropic.MessagesContentBlock) anthropic.MessagesContentBlock {
	redactedContent := *content

	// Redact text content
	if content.Text != nil {
		textCopy := *content.Text
		textCopy.Text = redaction.RedactString(content.Text.Text)
		redactedContent.Text = &textCopy
	}

	// Redact thinking content
	if content.Thinking != nil {
		thinkingCopy := *content.Thinking
		thinkingCopy.Thinking = redaction.RedactString(content.Thinking.Thinking)
		redactedContent.Thinking = &thinkingCopy
	}

	// Redact tool use input (may contain sensitive data)
	if content.Tool != nil {
		toolCopy := *content.Tool
		// For tool use, we redact by replacing the input with a placeholder
		toolCopy.Input = map[string]any{
			"redacted": redaction.RedactString(fmt.Sprintf("%v", content.Tool.Input)),
		}
		redactedContent.Tool = &toolCopy
	}

	// Note: tool_use_id and function names are metadata, not sensitive content

	return redactedContent
}
