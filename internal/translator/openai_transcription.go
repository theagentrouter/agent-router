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
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewTranscriptionOpenAIToOpenAITranslator implements [OpenAIAudioTranscriptionTranslator]
// for OpenAI to OpenAI translation for audio transcription.
func NewTranscriptionOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIAudioTranscriptionTranslator {
	return &openAIToOpenAITranslatorV1Transcription{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "audio", "transcriptions"),
	}
}

// openAIToOpenAITranslatorV1Transcription is a passthrough translator for OpenAI's
// /v1/audio/transcriptions endpoint.
//
// Streaming notes (gpt-4o-transcribe and gpt-4o-mini-transcribe with stream=true):
//   - Envoy delivers the SSE body across multiple ResponseBody invocations once the upstream
//     response sets Content-Type: text/event-stream. The response Content-Type is the authoritative
//     dispatch signal — the request's stream flag is intentionally not consulted here so we mirror
//     OpenAI's own behavior of silently ignoring stream=true for whisper-1 (which always returns JSON).
type openAIToOpenAITranslatorV1Transcription struct {
	modelNameOverride internalapi.ModelNameOverride
	path              string
	requestModel      internalapi.RequestModel
	contentType       string
	sseBuffer         []byte
}

// RequestBody implements [OpenAIAudioTranscriptionTranslator.RequestBody].
func (o *openAIToOpenAITranslatorV1Transcription) RequestBody(original []byte, req *openai.TranscriptionRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	o.requestModel = req.Model

	if o.modelNameOverride != "" && o.contentType != "" {
		var newContentType string
		var rewriteErr error
		newBody, newContentType, rewriteErr = rewriteMultipartModel(original, o.contentType, o.modelNameOverride)
		if rewriteErr != nil {
			return nil, nil, fmt.Errorf("failed to rewrite multipart model: %w", rewriteErr)
		}
		newHeaders = append(newHeaders,
			internalapi.Header{contentTypeHeaderName, newContentType},
			internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))},
		)
		o.requestModel = o.modelNameOverride
	}

	newHeaders = append(newHeaders, internalapi.Header{pathHeaderName, o.path})

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)

	if len(newBody) > 0 && o.modelNameOverride == "" {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [OpenAIAudioTranscriptionTranslator.ResponseHeaders].
func (o *openAIToOpenAITranslatorV1Transcription) ResponseHeaders(_ map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [OpenAIAudioTranscriptionTranslator.ResponseBody].
func (o *openAIToOpenAITranslatorV1Transcription) ResponseBody(respHeaders map[string]string, body io.Reader, _ bool, span tracingapi.TranscriptionSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	responseModel = o.requestModel

	if strings.Contains(respHeaders[contentTypeHeaderName], eventStreamContentType) {
		buf, readErr := io.ReadAll(body)
		if readErr != nil {
			return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to read SSE stream: %w", readErr)
		}
		o.sseBuffer = append(o.sseBuffer, buf...)
		o.recordTranscriptionStreamChunks(span)
		return
	}

	if span != nil {
		data, readErr := io.ReadAll(body)
		if readErr == nil {
			var resp openai.TranscriptionResponse
			if jsonErr := json.Unmarshal(data, &resp); jsonErr == nil {
				span.RecordResponse(&resp)
			} else {
				span.RecordResponse(&openai.TranscriptionResponse{
					Text: string(data),
				})
			}
		}
	}
	return
}

// recordTranscriptionStreamChunks scans o.sseBuffer for complete SSE `data:` lines and forwards
// each parsed event to the span chunk recorder. Mirrors the canonical pattern used by other
// streaming translators (see openai_openai.go:extractUsageFromBufferEvent).
//
// Tolerance rules — match OpenAI's pass-through expectations:
//   - Lines that don't start with "data: " (SSE event/comment lines, blanks) are skipped.
//   - "data: [DONE]" is skipped if it ever appears (forward-compat with chat-style streams).
//   - JSON unmarshal failures on a single line are silently skipped — one bad event must not
//     derail parsing of subsequent ones.
//   - Events with unknown `type` values are still forwarded to the chunk recorder verbatim.
func (o *openAIToOpenAITranslatorV1Transcription) recordTranscriptionStreamChunks(span tracingapi.TranscriptionSpan) {
	for {
		i := bytes.IndexByte(o.sseBuffer, '\n')
		if i == -1 {
			return
		}
		line := o.sseBuffer[:i]
		o.sseBuffer = o.sseBuffer[i+1:]
		// Some servers terminate SSE lines with \r\n; strip the trailing CR before JSON decode.
		line = bytes.TrimRight(line, "\r")
		payload, ok := cutSSEDataPrefix(line)
		if !ok {
			continue
		}
		if len(payload) == 0 || bytes.Equal(payload, sseDoneMessage) {
			continue
		}
		event := &openai.TranscriptionStreamEvent{}
		if err := json.Unmarshal(payload, event); err != nil {
			continue
		}
		if span != nil {
			span.RecordResponseChunk(event)
		}
	}
}

// ResponseError implements [OpenAIAudioTranscriptionTranslator.ResponseError].
func (o *openAIToOpenAITranslatorV1Transcription) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}

// SetContentType sets the content-type from the original request for multipart parsing during model rewrite.
func (o *openAIToOpenAITranslatorV1Transcription) SetContentType(ct string) {
	o.contentType = ct
}
