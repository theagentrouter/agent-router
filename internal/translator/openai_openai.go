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

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewChatCompletionOpenAIToOpenAITranslator implements [Factory] for OpenAI to OpenAI translation.
func NewChatCompletionOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIChatCompletionTranslator {
	return &openAIToOpenAITranslatorV1ChatCompletion{modelNameOverride: modelNameOverride, path: path.Join("/", prefix, "chat/completions")}
}

// NewChatCompletionOpenAIToAWSOpenAITranslator implements [Factory] for OpenAI to AWS OpenAI-compatible translation.
// The request body is always returned so that AWS SigV4 authentication signs the body Envoy sends upstream.
func NewChatCompletionOpenAIToAWSOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIChatCompletionTranslator {
	return &openAIToOpenAITranslatorV1ChatCompletion{
		modelNameOverride:        modelNameOverride,
		path:                     path.Join("/", prefix, "chat/completions"),
		forceRequestBodyMutation: true,
	}
}

// openAIToOpenAITranslatorV1ChatCompletion is a passthrough translator for OpenAI Chat Completions API.
// May apply model overrides but otherwise preserves the OpenAI format:
// https://platform.openai.com/docs/api-reference/chat/create
type openAIToOpenAITranslatorV1ChatCompletion struct {
	modelNameOverride internalapi.ModelNameOverride
	// AWS authentication signs bodyMutation.GetBody(), so AWS-compatible
	// requests must emit the original body even when this translator does not
	// otherwise mutate it. This ensures Envoy sends the exact bytes that were signed.
	forceRequestBodyMutation bool
	// requestModel serves as fallback for non-compliant OpenAI backends that
	// don't return model in responses, ensuring metrics/tracing always have a model.
	requestModel internalapi.RequestModel
	// streamingResponseModel stores the actual model from streaming responses
	streamingResponseModel internalapi.ResponseModel
	stream                 bool
	buffered               []byte
	// The path of the chat completions endpoint to be used for the request. It is prefixed with the OpenAI path prefix.
	path string
	// Redaction configuration for debug logging
	debugLogEnabled bool
	enableRedaction bool
	logger          *slog.Logger
}

// RequestBody implements [OpenAIChatCompletionTranslator.RequestBody].
func (o *openAIToOpenAITranslatorV1ChatCompletion) RequestBody(original []byte, req *openai.ChatCompletionRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if req.Stream {
		o.stream = true
	}
	// Store the request model to use as fallback for response model
	o.requestModel = req.Model
	if o.modelNameOverride != "" {
		// If modelName is set we override the model to be used for the request.
		newBody, err = sjson.SetBytesOptions(original, "model", o.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
		// Make everything coherent.
		o.requestModel = o.modelNameOverride
	}

	// Always set the path header to the chat completions endpoint so that the request is routed correctly.
	newHeaders = []internalapi.Header{{pathHeaderName, o.path}}

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation || o.forceRequestBodyMutation, newBody, original)

	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseError implements [OpenAIChatCompletionTranslator.ResponseError]
// For OpenAI based backend we return the OpenAI error type as is.
// If connection fails the error body is translated to OpenAI error type for events such as HTTP 503 or 504.
func (o *openAIToOpenAITranslatorV1ChatCompletion) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}

// convertErrorOpenAIToOpenAIError implements ResponseError conversion logic for OpenAI to OpenAI translation.
func convertErrorOpenAIToOpenAIError(respHeaders map[string]string, body io.Reader) (newHeaders []internalapi.Header, newBody []byte, err error) {
	if !strings.Contains(respHeaders[contentTypeHeaderName], jsonContentType) {
		var openaiError openai.Error
		buf, err := io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		statusCode := respHeaders[statusHeaderName]
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    openAIBackendError,
				Message: string(buf),
				Code:    &statusCode,
			},
		}
		newBody, err = json.Marshal(openaiError)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
		}
		newHeaders = append(newHeaders,
			internalapi.Header{contentTypeHeaderName, jsonContentType},
			internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))},
		)
	}
	return
}

// ResponseHeaders implements [OpenAIChatCompletionTranslator.ResponseHeaders].
func (o *openAIToOpenAITranslatorV1ChatCompletion) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [OpenAIChatCompletionTranslator.ResponseBody].
// OpenAI supports model virtualization through automatic routing and resolution,
// so we return the actual model from the response body which may differ from the requested model
// (e.g., request "gpt-4o" → response "gpt-4o-2024-08-06").
func (o *openAIToOpenAITranslatorV1ChatCompletion) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.ChatCompletionSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	if o.stream {
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, tokenUsage, o.requestModel, fmt.Errorf("failed to read body: %w", err)
		}
		o.buffered = append(o.buffered, buf...)
		tokenUsage = o.extractUsageFromBufferEvent(span)
		// Use stored streaming response model, fallback to request model for non-compliant backends
		responseModel = cmp.Or(o.streamingResponseModel, o.requestModel)
		return
	}
	resp := &openai.ChatCompletionResponse{}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}
	// A JSON `null` body decodes into a nil *resp without an error (the decode
	// target is a **ChatCompletionResponse), which some upstreams return with a
	// 200 status. Treat it as an empty response so we report zero usage and fall
	// back to the request model, mirroring the streaming path, instead of
	// dereferencing nil below.
	if resp == nil {
		resp = &openai.ChatCompletionResponse{}
	}

	// Redact and log response when enabled
	if o.debugLogEnabled && o.enableRedaction && o.logger != nil {
		redactedResp := o.RedactBody(resp)
		if jsonBody, marshalErr := json.Marshal(redactedResp); marshalErr == nil {
			o.logger.Debug("response body processing", slog.Any("response", string(jsonBody)))
		}
	}

	tokenUsage.SetInputTokens(uint32(resp.Usage.PromptTokens))      //nolint:gosec
	tokenUsage.SetOutputTokens(uint32(resp.Usage.CompletionTokens)) //nolint:gosec
	tokenUsage.SetTotalTokens(uint32(resp.Usage.TotalTokens))       //nolint:gosec
	if resp.Usage.PromptTokensDetails != nil {
		tokenUsage.SetCachedInputTokens(uint32(resp.Usage.PromptTokensDetails.CachedTokens))               //nolint:gosec
		tokenUsage.SetCacheCreationInputTokens(uint32(resp.Usage.PromptTokensDetails.CacheCreationTokens)) //nolint:gosec
	}
	if resp.Usage.CompletionTokensDetails != nil {
		tokenUsage.SetReasoningTokens(uint32(resp.Usage.CompletionTokensDetails.ReasoningTokens)) //nolint:gosec
	}
	// Fallback to request model for test or non-compliant OpenAI backends
	responseModel = cmp.Or(resp.Model, o.requestModel)
	if span != nil {
		span.RecordResponse(resp)
	}
	return
}

// extractUsageFromBufferEvent extracts the token usage from the buffered event.
// It scans complete lines and returns the latest usage found in this batch.
func (o *openAIToOpenAITranslatorV1ChatCompletion) extractUsageFromBufferEvent(span tracingapi.ChatCompletionSpan) (tokenUsage metrics.TokenUsage) {
	for {
		i := bytes.IndexByte(o.buffered, '\n')
		if i == -1 {
			return
		}
		line := o.buffered[:i]
		o.buffered = o.buffered[i+1:]
		data, ok := cutSSEDataPrefix(line)
		if !ok {
			continue
		}
		event := &openai.ChatCompletionResponseChunk{}
		if err := json.Unmarshal(data, event); err != nil {
			continue
		}
		if span != nil {
			span.RecordResponseChunk(event)
		}
		if event.Model != "" {
			// Store the response model for future batches
			o.streamingResponseModel = event.Model
		}
		if usage := event.Usage; usage != nil {
			tokenUsage.SetInputTokens(uint32(usage.PromptTokens))      //nolint:gosec
			tokenUsage.SetOutputTokens(uint32(usage.CompletionTokens)) //nolint:gosec
			tokenUsage.SetTotalTokens(uint32(usage.TotalTokens))       //nolint:gosec
			if usage.PromptTokensDetails != nil {
				tokenUsage.SetCachedInputTokens(uint32(usage.PromptTokensDetails.CachedTokens))               //nolint:gosec
				tokenUsage.SetCacheCreationInputTokens(uint32(usage.PromptTokensDetails.CacheCreationTokens)) //nolint:gosec
			}
			if usage.CompletionTokensDetails != nil {
				tokenUsage.SetReasoningTokens(uint32(usage.CompletionTokensDetails.ReasoningTokens)) //nolint:gosec
			}
			// Do not mark buffering done; keep scanning to return the latest usage in this batch.
		}
	}
}

// SetRedactionConfig implements [ResponseRedactor.SetRedactionConfig].
func (o *openAIToOpenAITranslatorV1ChatCompletion) SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger) {
	o.debugLogEnabled = debugLogEnabled
	o.enableRedaction = enableRedaction
	o.logger = logger
}

// RedactBody implements [ResponseRedactor.RedactBody].
// Creates a redacted copy of the response for safe logging without modifying the original.
func (o *openAIToOpenAITranslatorV1ChatCompletion) RedactBody(resp *openai.ChatCompletionResponse) *openai.ChatCompletionResponse {
	if resp == nil {
		return nil
	}

	// Create a shallow copy of the response
	redacted := *resp

	// Redact choices (contains AI-generated content)
	if len(resp.Choices) > 0 {
		redacted.Choices = make([]openai.ChatCompletionResponseChoice, len(resp.Choices))
		for i := range resp.Choices {
			redactedChoice := resp.Choices[i]
			redactedChoice.Message = redactResponseMessage(&resp.Choices[i].Message)
			redacted.Choices[i] = redactedChoice
		}
	}

	return &redacted
}

// redactResponseMessage redacts sensitive content from a chat completion response message.
func redactResponseMessage(msg *openai.ChatCompletionResponseChoiceMessage) openai.ChatCompletionResponseChoiceMessage {
	redactedMsg := *msg

	// Redact message content (AI-generated text)
	if msg.Content != nil {
		redactedContent := redaction.RedactString(*msg.Content)
		redactedMsg.Content = &redactedContent
	}

	// Redact tool call arguments (may contain data derived from user messages).
	// Function name is kept — it is the tool API name, not user data.
	if len(msg.ToolCalls) > 0 {
		redactedMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			redactedToolCall := tc
			redactedToolCall.Function.Arguments = redaction.RedactString(tc.Function.Arguments)
			redactedMsg.ToolCalls[i] = redactedToolCall
		}
	}

	// Redact audio data if present
	if msg.Audio != nil {
		redactedAudio := *msg.Audio
		redactedAudio.Data = redaction.RedactString(msg.Audio.Data)
		redactedAudio.Transcript = redaction.RedactString(msg.Audio.Transcript)
		redactedMsg.Audio = &redactedAudio
	}

	// Redact reasoning content if present
	if msg.ReasoningContent != nil {
		redactedMsg.ReasoningContent = redactReasoningContent(msg.ReasoningContent)
	}

	return redactedMsg
}

// redactReasoningContent redacts sensitive content from reasoning content union.
func redactReasoningContent(rc *openai.ReasoningContentUnion) *openai.ReasoningContentUnion {
	if rc == nil {
		return nil
	}

	switch reasoningContent := rc.Value.(type) {
	// Handle string type (e.g., from qwen model)
	case string:
		return &openai.ReasoningContentUnion{
			Value: redaction.RedactString(reasoningContent),
		}
	// Handle ReasoningContent type (e.g., from AWS Bedrock)
	case *openai.ReasoningContent:
		if reasoningContent.ReasoningContent != nil {
			if reasoningText := reasoningContent.ReasoningContent.ReasoningText; reasoningText != nil {
				return &openai.ReasoningContentUnion{
					Value: &openai.ReasoningContent{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							ReasoningText: &awsbedrock.ReasoningTextBlock{
								Text:      redaction.RedactString(reasoningText.Text),
								Signature: reasoningText.Signature,
							},
						},
					},
				}
			}
		}
	}

	return rc
}
