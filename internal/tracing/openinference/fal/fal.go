// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package fal provides OpenInference semantic conventions hooks for
// fal run and queue instrumentation used by the ExtProc router filter.
package fal

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	falschema "github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// Recorder implements recorders for fal run and queue spans.
type Recorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
	traceConfig *openinference.TraceConfig
}

// NewRecorderFromEnv creates a tracingapi.FalRecorder from environment variables
// using the OpenInference configuration specification.
func NewRecorderFromEnv() tracingapi.FalRecorder {
	return NewRecorder(nil)
}

// NewRecorder creates a tracingapi.FalRecorder with the given config using
// the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
func NewRecorder(config *openinference.TraceConfig) tracingapi.FalRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &Recorder{traceConfig: config}
}

// startOpts sets trace.SpanKindInternal as that's the span kind used in OpenInference.
var startOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.FalRecorder.
func (r *Recorder) StartParams(*falschema.Request, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Fal", startOpts
}

// RecordRequest implements the same method as defined in tracingapi.FalRecorder.
func (r *Recorder) RecordRequest(span trace.Span, _ *falschema.Request, body []byte) {
	span.SetAttributes(buildRequestAttributes(body, r.traceConfig)...)
}

// RecordResponseOnError implements the same method as defined in tracingapi.FalRecorder.
func (r *Recorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.FalRecorder.
func (r *Recorder) RecordResponse(span trace.Span, resp *falschema.Response) {
	span.SetAttributes(buildResponseAttributes(resp, r.traceConfig)...)
	span.SetStatus(codes.Ok, "")
}

// buildRequestAttributes builds OpenInference attributes from the fal request.
// The model is in the path, not the body, so it is recorded from the response.
func buildRequestAttributes(body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemFal),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
	}
	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, string(body)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}
	return attrs
}

// buildResponseAttributes builds OpenInference attributes from the fal response.
func buildResponseAttributes(resp *falschema.Response, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if resp.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, resp.Model))
	}
	if resp.Usage != nil {
		var total int
		if resp.Usage.InputTokens != nil {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, *resp.Usage.InputTokens))
			total += *resp.Usage.InputTokens
		}
		if resp.Usage.OutputTokens != nil {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletion, *resp.Usage.OutputTokens))
			total += *resp.Usage.OutputTokens
		}
		if total > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, total))
		}
	}
	switch {
	case config.HideOutputs:
		attrs = append(attrs, attribute.String(openinference.OutputValue, openinference.RedactedValue))
	case resp.Body != nil:
		attrs = append(attrs,
			attribute.String(openinference.OutputValue, string(resp.Body)),
			attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
		)
	}
	return attrs
}
