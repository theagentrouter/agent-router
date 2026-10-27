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

// NewSpeechOpenAIToOpenAITranslator implements [OpenAISpeechTranslator] for OpenAI to OpenAI translation for speech.
func NewSpeechOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAISpeechTranslator {
	return &openAIToOpenAITranslatorV1Speech{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "audio", "speech"),
	}
}

// openAIToOpenAITranslatorV1Speech is a passthrough translator for OpenAI Speech API.
// May apply model overrides but otherwise preserves the OpenAI format:
// https://platform.openai.com/docs/api-reference/audio/createSpeech
type openAIToOpenAITranslatorV1Speech struct {
	modelNameOverride internalapi.ModelNameOverride
	// The path of the speech endpoint to be used for the request. It is prefixed with the OpenAI path prefix.
	path string
	// stream indicates whether the request is for SSE streaming.
	stream bool
	// requestModel stores the model from the request to use in the response.
	requestModel internalapi.RequestModel
	// buffered stores incomplete SSE events across streamed response body chunks.
	buffered []byte
}

// RequestBody implements [OpenAISpeechTranslator.RequestBody].
func (o *openAIToOpenAITranslatorV1Speech) RequestBody(original []byte, req *openai.SpeechRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Store the request model to use as response model
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
	o.stream = req.StreamFormat != nil && *req.StreamFormat == openai.StreamFormatSSE

	// Always set the path header to the speech endpoint so that the request is routed correctly.
	newHeaders = []internalapi.Header{{pathHeaderName, o.path}}

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)

	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [OpenAISpeechTranslator.ResponseHeaders].
func (o *openAIToOpenAITranslatorV1Speech) ResponseHeaders(_ map[string]string) (newHeaders []internalapi.Header, err error) {
	// For OpenAI to OpenAI translation, we don't need to mutate the response headers.
	// The content-type will be set by the backend (audio/mpeg, audio/wav, etc. for binary, or text/event-stream for SSE).
	return nil, nil
}

// ResponseBody implements [OpenAISpeechTranslator.ResponseBody].
// Speech responses are either binary audio or SSE streaming chunks.
func (o *openAIToOpenAITranslatorV1Speech) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.SpeechSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	if o.stream {
		// Handle SSE streaming response
		return o.handleStreamingResponse(body, span)
	}

	// Handle binary audio response - just pass through the audio bytes
	return o.handleBinaryResponse(body, span)
}

// handleStreamingResponse handles SSE streaming responses from the Speech API.
func (o *openAIToOpenAITranslatorV1Speech) handleStreamingResponse(body io.Reader, span tracingapi.SpeechSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	// Buffer the incoming SSE data
	chunks, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read SSE stream: %w", err)
	}

	// Record SSE chunks to span if tracing is enabled
	if span != nil {
		o.buffered = append(o.buffered, chunks...)
		o.recordSSEChunksToSpan(span)
	}

	// Use request model as response model (speech synthesis doesn't return a model field)
	responseModel = o.requestModel
	// Audio synthesis doesn't have token-based usage metrics
	return
}

// handleBinaryResponse handles binary audio responses from the Speech API.
func (o *openAIToOpenAITranslatorV1Speech) handleBinaryResponse(body io.Reader, span tracingapi.SpeechSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	// Read the binary audio data
	audioData, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to read audio data: %w", err)
	}

	// Record binary response to span if tracing is enabled
	if span != nil {
		span.RecordResponse(&audioData)
	}

	// Use request model as response model
	responseModel = o.requestModel
	// Audio synthesis doesn't have token-based usage metrics
	return
}

// recordSSEChunksToSpan records SSE streaming chunks to the tracing span.
func (o *openAIToOpenAITranslatorV1Speech) recordSSEChunksToSpan(span tracingapi.SpeechSpan) {
	for {
		event, remaining, ok := nextSSEEvent(o.buffered)
		if !ok {
			return
		}
		o.buffered = remaining
		for line := range bytes.SplitSeq(event, []byte("\n")) {
			data, ok := cutSSEDataPrefix(line)
			if !ok {
				continue
			}
			if len(data) == 0 || bytes.Equal(data, sseDoneMessage) {
				continue
			}

			var chunk openai.SpeechStreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				continue // skip invalid JSON
			}

			// Record streaming chunk to span
			span.RecordResponseChunk(&chunk)
		}
	}
}

// ResponseError implements [OpenAISpeechTranslator.ResponseError].
func (o *openAIToOpenAITranslatorV1Speech) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}
