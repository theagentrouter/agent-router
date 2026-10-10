// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strconv"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewCompletionOpenAIToOpenAITranslator implements [Factory] for OpenAI to OpenAI translation for completions.
func NewCompletionOpenAIToOpenAITranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAICompletionTranslator {
	return &openAIToOpenAITranslatorV1Completion{modelNameOverride: modelNameOverride, path: path.Join("/", apiVersion, "completions")}
}

// openAIToOpenAITranslatorV1Completion is a passthrough translator for OpenAI Completions API.
// May apply model overrides but otherwise preserves the OpenAI format:
// https://platform.openai.com/docs/api-reference/completions/create
type openAIToOpenAITranslatorV1Completion struct {
	modelNameOverride internalapi.ModelNameOverride
	// The path of the completions endpoint to be used for the request. It is prefixed with the OpenAI path prefix.
	path string
	// stream indicates whether the request is for streaming.
	stream bool
	// buffered accumulates SSE chunks for streaming responses.
	buffered []byte
	// streamingResponseModel stores the actual model from streaming responses.
	streamingResponseModel internalapi.ResponseModel
}

// RequestBody implements [OpenAICompletionTranslator.RequestBody].
func (o *openAIToOpenAITranslatorV1Completion) RequestBody(original []byte, req *openai.CompletionRequest, onRetry bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Track if this is a streaming request.
	o.stream = req.Stream

	if o.modelNameOverride != "" {
		// If modelName is set we override the model to be used for the request.
		newBody, err = sjson.SetBytesOptions(original, "model", o.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
	}

	// Always set the path header to the completions endpoint so that the request is routed correctly.
	if onRetry && len(newBody) == 0 {
		newBody = original
	}
	newHeaders = []internalapi.Header{{pathHeaderName, o.path}}

	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [OpenAICompletionTranslator.ResponseHeaders].
func (o *openAIToOpenAITranslatorV1Completion) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [OpenAICompletionTranslator.ResponseBody].
// OpenAI completions support model virtualization through automatic routing and resolution,
// so we return the actual model from the response body which may differ from the requested model.
func (o *openAIToOpenAITranslatorV1Completion) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.CompletionSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	// For streaming, just pass through and extract metadata from SSE chunks
	if o.stream {
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, tokenUsage, "", fmt.Errorf("failed to read body: %w", err)
		}
		o.buffered = append(o.buffered, buf...)
		tokenUsage = o.extractUsageFromBufferEvent(span)
		responseModel = o.streamingResponseModel
		// Pass through the SSE data as-is
		return nil, nil, tokenUsage, responseModel, nil
	}

	// Handle non-streaming response
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read body: %w", err)
	}

	var resp openai.CompletionResponse
	if decodeErr := json.Unmarshal(bodyBytes, &resp); decodeErr != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal body: %w", decodeErr)
	}

	// Extract the response model.
	responseModel = resp.Model

	// Extract token usage if available.
	if resp.Usage != nil {
		// Safely convert int to uint32 with bounds checking
		if resp.Usage.PromptTokens >= 0 {
			tokenUsage.SetInputTokens(uint32(resp.Usage.PromptTokens)) // #nosec G115
		}
		if resp.Usage.CompletionTokens >= 0 {
			tokenUsage.SetOutputTokens(uint32(resp.Usage.CompletionTokens)) // #nosec G115
		}
		if resp.Usage.TotalTokens >= 0 {
			tokenUsage.SetTotalTokens(uint32(resp.Usage.TotalTokens)) // #nosec G115
		}
		if resp.Usage.PromptTokensDetails != nil {
			if resp.Usage.PromptTokensDetails.CachedTokens >= 0 {
				tokenUsage.SetCachedInputTokens(uint32(resp.Usage.PromptTokensDetails.CachedTokens)) //nolint:gosec
			}
			if cacheWriteTokens := resp.Usage.PromptTokensDetails.CacheWriteTokensValue(); cacheWriteTokens >= 0 {
				tokenUsage.SetCacheCreationInputTokens(uint32(cacheWriteTokens)) //nolint:gosec
			}
		}
		if resp.Usage.CompletionTokensDetails != nil {
			if resp.Usage.CompletionTokensDetails.ReasoningTokens >= 0 {
				tokenUsage.SetReasoningTokens(uint32(resp.Usage.CompletionTokensDetails.ReasoningTokens)) //nolint:gosec
			}
		}
	}

	// Record non-streaming response to span if tracing is enabled.
	if span != nil {
		span.RecordResponse(&resp)
	}

	// Pass through the original body without re-encoding to preserve formatting
	return
}

// extractUsageFromBufferEvent extracts the token usage and model from the buffered SSE events.
// It scans complete lines and returns the latest usage found in this batch.
// It also records each parsed chunk to the tracing span if provided.
func (o *openAIToOpenAITranslatorV1Completion) extractUsageFromBufferEvent(span tracingapi.CompletionSpan) (tokenUsage metrics.TokenUsage) {
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
		// Skip the [DONE] marker
		if bytes.Equal(data, sseDoneMessage) {
			continue
		}
		// The OpenAPI spec says "Note: both the streamed and non-streamed response objects
		// share the same shape (unlike the chat endpoint)." So we use CompletionResponse
		// for streaming chunks as well.
		event := &openai.CompletionResponse{}
		if err := json.Unmarshal(data, event); err != nil {
			continue
		}
		if event.Model != "" {
			// Store the response model for future batches
			o.streamingResponseModel = event.Model
		}

		// Record streaming chunk to span if tracing is enabled.
		if span != nil {
			span.RecordResponseChunk(event)
		}

		if usage := event.Usage; usage != nil {
			tokenUsage.SetInputTokens(uint32(usage.PromptTokens))      //nolint:gosec
			tokenUsage.SetOutputTokens(uint32(usage.CompletionTokens)) //nolint:gosec
			tokenUsage.SetTotalTokens(uint32(usage.TotalTokens))       //nolint:gosec
			if usage.PromptTokensDetails != nil {
				tokenUsage.SetCachedInputTokens(uint32(usage.PromptTokensDetails.CachedTokens))                   //nolint:gosec
				tokenUsage.SetCacheCreationInputTokens(uint32(usage.PromptTokensDetails.CacheWriteTokensValue())) //nolint:gosec
			}
			if usage.CompletionTokensDetails != nil {
				tokenUsage.SetReasoningTokens(uint32(usage.CompletionTokensDetails.ReasoningTokens)) //nolint:gosec
			}
			// Do not mark buffering done; keep scanning to return the latest usage in this batch.
		}
	}
}

// ResponseError implements [OpenAICompletionTranslator.ResponseError].
func (o *openAIToOpenAITranslatorV1Completion) ResponseError(map[string]string, io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	return
}
