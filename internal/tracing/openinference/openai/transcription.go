// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// TranscriptionRecorder implements recorders for OpenInference audio transcription spans.
type TranscriptionRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewTranscriptionRecorderFromEnv creates a tracingapi.TranscriptionRecorder
// from environment variables using the OpenInference configuration specification.
func NewTranscriptionRecorderFromEnv() tracingapi.TranscriptionRecorder {
	return NewTranscriptionRecorder(nil)
}

// NewTranscriptionRecorder creates a tracingapi.TranscriptionRecorder with the
// given config using the OpenInference configuration specification.
func NewTranscriptionRecorder(config *openinference.TraceConfig) tracingapi.TranscriptionRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &TranscriptionRecorder{traceConfig: config}
}

var transcriptionStartOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.TranscriptionRecorder.
func (r *TranscriptionRecorder) StartParams(*openai.TranscriptionRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Transcription", transcriptionStartOpts
}

// RecordRequest implements the same method as defined in tracingapi.TranscriptionRecorder.
func (r *TranscriptionRecorder) RecordRequest(span trace.Span, req *openai.TranscriptionRequest, _ []byte) {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
	}

	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, req.Model))
	}

	if r.traceConfig.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		inputValue := openinference.RedactedValue
		if b, err := json.Marshal(struct {
			Model          string `json:"model"`
			FileName       string `json:"file_name"`
			FileSize       int64  `json:"file_size"`
			Language       string `json:"language"`
			ResponseFormat string `json:"response_format"`
		}{
			Model:          req.Model,
			FileName:       req.FileName,
			FileSize:       req.FileSize,
			Language:       req.Language,
			ResponseFormat: req.ResponseFormat,
		}); err == nil {
			inputValue = string(b)
		}
		attrs = append(attrs,
			attribute.String(openinference.InputValue, inputValue),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))
	}

	span.SetAttributes(attrs...)
}

// RecordResponse implements the same method as defined in tracingapi.TranscriptionRecorder.
func (r *TranscriptionRecorder) RecordResponse(span trace.Span, resp *openai.TranscriptionResponse) {
	var attrs []attribute.KeyValue

	if !r.traceConfig.HideOutputs && resp != nil {
		attrs = append(attrs,
			attribute.String(openinference.OutputValue, resp.Text),
			attribute.String(openinference.OutputMimeType, "text/plain"),
		)
		if resp.Duration > 0 {
			attrs = append(attrs, attribute.Float64(openinference.OutputAudioDuration, resp.Duration))
		}
		if resp.Language != "" {
			attrs = append(attrs, attribute.String(openinference.OutputLanguage, resp.Language))
		}
	}

	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}

// RecordResponseChunks implements the same method as defined in tracingapi.TranscriptionRecorder.
func (r *TranscriptionRecorder) RecordResponseChunks(span trace.Span, chunks []*openai.TranscriptionStreamEvent) {
	if len(chunks) == 0 || r.traceConfig.HideOutputs {
		return
	}

	var sb strings.Builder
	var doneTextSet bool
	for _, c := range chunks {
		if c == nil {
			continue
		}
		switch c.Type {
		case openai.TranscriptionStreamEventTypeDelta:
			if !doneTextSet {
				sb.WriteString(c.Delta)
			}
		case openai.TranscriptionStreamEventTypeDone:
			if c.Text != "" {
				sb.Reset()
				sb.WriteString(c.Text)
				doneTextSet = true
			}
		}
	}

	if sb.Len() == 0 {
		return
	}
	span.SetAttributes(
		attribute.String(openinference.OutputValue, sb.String()),
		attribute.String(openinference.OutputMimeType, "text/plain"),
	)
}

// RecordResponseOnError implements the same method as defined in tracingapi.TranscriptionRecorder.
func (r *TranscriptionRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, r.traceConfig, statusCode, string(body))
}
