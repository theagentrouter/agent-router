// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// SpeechRecorder implements recorders for OpenInference speech synthesis spans.
type SpeechRecorder struct {
	// Embedding NoopChunkRecorder since speech chunks are streamed audio data
	tracingapi.NoopChunkRecorder[openai.SpeechStreamChunk]
	traceConfig *openinference.TraceConfig
}

// NewSpeechRecorderFromEnv creates a tracingapi.SpeechRecorder
// from environment variables using the OpenInference configuration specification.
func NewSpeechRecorderFromEnv() tracingapi.SpeechRecorder {
	return NewSpeechRecorder(nil)
}

// NewSpeechRecorder creates a tracingapi.SpeechRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
func NewSpeechRecorder(config *openinference.TraceConfig) tracingapi.SpeechRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &SpeechRecorder{traceConfig: config}
}

// speechStartOpts sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var speechStartOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.SpeechRecorder.
func (r *SpeechRecorder) StartParams(*openai.SpeechRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "AudioSpeech", speechStartOpts
}

// RecordRequest implements the same method as defined in tracingapi.SpeechRecorder.
func (r *SpeechRecorder) RecordRequest(span trace.Span, req *openai.SpeechRequest, body []byte) {
	span.SetAttributes(buildSpeechRequestAttributes(req, string(body), r.traceConfig)...)
}

// RecordResponse implements the same method as defined in tracingapi.SpeechRecorder.
// For speech synthesis, the response is binary audio data.
func (r *SpeechRecorder) RecordResponse(span trace.Span, resp *[]byte) {
	// Set output attributes.
	var attrs []attribute.KeyValue

	// For binary audio, we record the size rather than the content
	if !r.traceConfig.HideOutputs && resp != nil {
		attrs = append(attrs,
			attribute.String(openinference.OutputMimeType, "audio/mpeg"), // Default, could be wav, opus, etc.
			attribute.Int("output.audio_bytes", len(*resp)),
		)
	}

	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}

// RecordResponseOnError implements the same method as defined in tracingapi.SpeechRecorder.
func (r *SpeechRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// buildSpeechRequestAttributes builds OpenInference attributes from the speech request.
func buildSpeechRequestAttributes(req *openai.SpeechRequest, body string, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
	}

	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, req.Model))
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		// For speech, we want to record the text input and other parameters
		inputJSON, err := json.Marshal(map[string]any{
			"input":           req.Input,
			"voice":           req.Voice,
			"response_format": req.ResponseFormat,
			"speed":           req.Speed,
			"instructions":    req.Instructions,
		})
		if err == nil {
			attrs = append(attrs,
				attribute.String(openinference.InputValue, string(inputJSON)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
			)
		}
	}

	if !config.HideLLMInvocationParameters {
		attrs = append(attrs, attribute.String(openinference.LLMInvocationParameters, body))
	}

	return attrs
}
