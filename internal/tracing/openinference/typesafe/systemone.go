// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package typesafe provides OpenInference semantic conventions hooks for
// TypeSafe System One instrumentation used by the ExtProc router filter.
package typesafe

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	typesafeschema "github.com/envoyproxy/ai-gateway/internal/apischema/typesafe"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// SystemOneRecorder implements recorders for TypeSafe System One spans.
type SystemOneRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
	traceConfig *openinference.TraceConfig
}

// NewSystemOneRecorderFromEnv creates a tracingapi.SystemOneRecorder from environment variables
// using the OpenInference configuration specification.
func NewSystemOneRecorderFromEnv() tracingapi.SystemOneRecorder {
	return NewSystemOneRecorder(nil)
}

// NewSystemOneRecorder creates a tracingapi.SystemOneRecorder with the given config using
// the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
func NewSystemOneRecorder(config *openinference.TraceConfig) tracingapi.SystemOneRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &SystemOneRecorder{traceConfig: config}
}

// startOpts sets trace.SpanKindInternal as that's the span kind used in OpenInference.
var startOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.SystemOneRecorder.
func (r *SystemOneRecorder) StartParams(*typesafeschema.SystemOneRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "SystemOne", startOpts
}

// RecordRequest implements the same method as defined in tracingapi.SystemOneRecorder.
func (r *SystemOneRecorder) RecordRequest(span trace.Span, req *typesafeschema.SystemOneRequest, body []byte) {
	span.SetAttributes(buildRequestAttributes(req, body, r.traceConfig)...)
}

// RecordResponseOnError implements the same method as defined in tracingapi.SystemOneRecorder.
func (r *SystemOneRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.SystemOneRecorder.
func (r *SystemOneRecorder) RecordResponse(span trace.Span, resp *typesafeschema.SystemOneResponse) {
	attrs := buildResponseAttributes(resp, r.traceConfig)

	bodyString := openinference.RedactedValue
	if !r.traceConfig.HideOutputs {
		if marshaled, err := json.Marshal(resp); err == nil {
			bodyString = string(marshaled)
		}
	}
	attrs = append(attrs, attribute.String(openinference.OutputValue, bodyString))

	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}

// buildRequestAttributes builds OpenInference attributes from the System One request.
// The state and questions are user content, so they are only recorded through
// input.value when inputs are not hidden.
func buildRequestAttributes(req *typesafeschema.SystemOneRequest, body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemTypeSafe),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
	}
	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, req.Model))
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

// buildResponseAttributes builds OpenInference attributes from the System One response.
func buildResponseAttributes(resp *typesafeschema.SystemOneResponse, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
	}
	if resp.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, resp.Model))
	}
	// Token counts are metadata and are included even when outputs are hidden.
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
	return attrs
}
