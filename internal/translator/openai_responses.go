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
	"path"
	"strconv"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewResponsesOpenAIToOpenAITranslator implements [OpenAIResponsesTranslator] for OpenAI to OpenAI translation for responses.
func NewResponsesOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return &openAIToOpenAITranslatorV1Responses{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "responses"),
	}
}

// NewResponsesOpenAIToAWSOpenAITranslator implements [OpenAIResponsesTranslator] for OpenAI to AWS OpenAI-compatible translation.
// The request body is always returned so that AWS SigV4 authentication signs the body Envoy sends upstream.
func NewResponsesOpenAIToAWSOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return &openAIToOpenAITranslatorV1Responses{
		modelNameOverride:        modelNameOverride,
		path:                     path.Join("/", prefix, "responses"),
		forceRequestBodyMutation: true,
	}
}

// openAIToOpenAITranslatorV1Responses is a passthrough translator for OpenAI Responses API.
// May apply model overrides but otherwise preserves the OpenAI format:
// https://platform.openai.com/docs/api-reference/responses/create
type openAIToOpenAITranslatorV1Responses struct {
	modelNameOverride        internalapi.ModelNameOverride
	forceRequestBodyMutation bool
	// The path of the responses endpoint to be used for the request. It is prefixed with the OpenAI path prefix.
	path string
	// stream indicates whether the request is for streaming.
	stream bool
	// streamingResponseModel stores the actual model from streaming responses.
	streamingResponseModel internalapi.ResponseModel
	// requestModel serves as fallback for non-compliant OpenAI backends that
	// don't return model in responses, ensuring metrics/tracing always have a model.
	requestModel internalapi.RequestModel
	// buffered stores incomplete SSE bytes across ResponseBody calls.
	buffered []byte
}

// RequestBody implements [OpenAIResponsesTranslator.RequestBody].
func (o *openAIToOpenAITranslatorV1Responses) RequestBody(original []byte, req *openai.ResponseRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Store the request model to use as fallback for response model
	o.requestModel = req.Model
	if o.modelNameOverride != "" {
		// If modelNameOverride is set, we override the model to be used for the request.
		newBody, err = sjson.SetBytesOptions(original, "model", o.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model: %w", err)
		}
		o.requestModel = o.modelNameOverride
	}

	// Track if this is a streaming request.
	o.stream = req.Stream

	// Always set the path header to the responses endpoint so that the request is routed correctly.
	newHeaders = []internalapi.Header{{pathHeaderName, o.path}}

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation || o.forceRequestBodyMutation, newBody, original)

	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [OpenAIResponsesTranslator.ResponseHeaders].
func (o *openAIToOpenAITranslatorV1Responses) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	// For OpenAI to OpenAI translation, we don't need to mutate the response headers.
	return nil, nil
}

// ResponseBody implements [OpenAIResponsesTranslator.ResponseBody].
// OpenAI responses support model virtualization through automatic routing and resolution,
// so we return the actual model from the response body which may differ from the requested model.
func (o *openAIToOpenAITranslatorV1Responses) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	if o.stream {
		// Handle streaming response
		return o.handleStreamingResponse(body, span)
	}

	// Handle non-streaming response
	return o.handleNonStreamingResponse(body, span)
}

// handleStreamingResponse handles streaming responses from the Responses API.
func (o *openAIToOpenAITranslatorV1Responses) handleStreamingResponse(body io.Reader, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	chunks, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read body: %w", err)
	}
	o.buffered = append(o.buffered, chunks...)
	tokenUsage = o.extractUsageFromBufferEvent(span)
	// Use stored streaming response model, fallback to request model for non-compliant backends
	responseModel = cmp.Or(o.streamingResponseModel, o.requestModel)
	return
}

// handleNonStreamingResponse handles non-streaming responses from the Responses API.
func (o *openAIToOpenAITranslatorV1Responses) handleNonStreamingResponse(body io.Reader, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	var resp openai.Response
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	// Fallback to request model for test or non-compliant OpenAI backends
	responseModel = cmp.Or(resp.Model, o.requestModel)

	if resp.Usage != nil {
		tokenUsage.SetInputTokens(uint32(resp.Usage.InputTokens))                                         // #nosec G115
		tokenUsage.SetOutputTokens(uint32(resp.Usage.OutputTokens))                                       // #nosec G115
		tokenUsage.SetTotalTokens(uint32(resp.Usage.TotalTokens))                                         // #nosec G115
		tokenUsage.SetCachedInputTokens(uint32(resp.Usage.InputTokensDetails.CachedTokens))               // #nosec G115
		tokenUsage.SetCacheCreationInputTokens(uint32(resp.Usage.InputTokensDetails.CacheCreationTokens)) // #nosec G115
		tokenUsage.SetReasoningTokens(uint32(resp.Usage.OutputTokensDetails.ReasoningTokens))             // #nosec G115
	}

	// Record non-streaming response to span if tracing is enabled.
	if span != nil {
		span.RecordResponse(&resp)
	}
	return
}

// setTokenUsageFromResponse populates tokenUsage from a Response's Usage field,
// guarding against a nil Usage (e.g. response.failed before any tokens were generated).
func setTokenUsageFromResponse(tokenUsage *metrics.TokenUsage, resp *openai.Response) {
	if resp == nil || resp.Usage == nil {
		return
	}
	tokenUsage.SetInputTokens(uint32(resp.Usage.InputTokens))                           // #nosec G115
	tokenUsage.SetOutputTokens(uint32(resp.Usage.OutputTokens))                         // #nosec G115
	tokenUsage.SetTotalTokens(uint32(resp.Usage.TotalTokens))                           // #nosec G115
	tokenUsage.SetCachedInputTokens(uint32(resp.Usage.InputTokensDetails.CachedTokens)) // #nosec G115
	// Openai does not support cache creation response.
	tokenUsage.SetCacheCreationInputTokens(uint32(0))                                     // #nosec G115
	tokenUsage.SetReasoningTokens(uint32(resp.Usage.OutputTokensDetails.ReasoningTokens)) // #nosec G115
}

// extractUsageFromBufferEvent extracts the token usage and model from the buffered SSE events.
// It scans complete SSE events and returns the latest usage found in response.completed,
// response.incomplete or response.failed events.
func (o *openAIToOpenAITranslatorV1Responses) extractUsageFromBufferEvent(span tracingapi.ResponsesSpan) (tokenUsage metrics.TokenUsage) {
	for {
		// SSE event boundary is a blank line: "data: {json}\n\n".
		i := bytes.Index(o.buffered, []byte("\n\n"))
		if i == -1 {
			return tokenUsage
		}
		event := o.buffered[:i]
		o.buffered = o.buffered[i+2:]
		for line := range bytes.SplitSeq(event, []byte("\n")) {
			// Look for lines carrying the "data" field.
			data, ok := cutSSEDataPrefix(line)
			if !ok {
				continue
			}

			if len(data) == 0 || bytes.Equal(data, sseDoneMessage) {
				continue
			}

			var eventUnion openai.ResponseStreamEventUnion
			if err := json.Unmarshal(data, &eventUnion); err != nil {
				continue // skip invalid JSON
			}

			switch eventUnion.GetEventType() {
			case "response.created":
				// Extract model from the first streaming event.
				respCreatedEvent := eventUnion.OfResponseCreated
				if respCreatedEvent.Response.Model != "" {
					o.streamingResponseModel = respCreatedEvent.Response.Model
				}
			case "response.completed":
				// response.completed always carries a populated usage object.
				setTokenUsageFromResponse(&tokenUsage, &eventUnion.OfResponseCompleted.Response)
			case "response.incomplete":
				// response.incomplete (e.g. hit max_output_tokens or content filter)
				// reports tokens generated so far.
				setTokenUsageFromResponse(&tokenUsage, &eventUnion.OfResponseIncomplete.Response)
			case "response.failed":
				// response.failed may carry usage if the failure happened after
				// generation started; pre-generation failures leave it nil.
				setTokenUsageFromResponse(&tokenUsage, &eventUnion.OfResponseFailed.Response)
			}
			// Record streaming chunk to span if tracing is enabled.
			if span != nil {
				span.RecordResponseChunk(&eventUnion)
			}
		}
	}
}

// ResponseError implements [OpenAIResponsesTranslator.ResponseError].
func (o *openAIToOpenAITranslatorV1Responses) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}
