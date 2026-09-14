// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// CountTokensRecorder implements recorders for OpenInference count tokens spans.
type CountTokensRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewCountTokensRecorderFromEnv creates a tracingapi.CountTokensRecorder
// from environment variables using the OpenInference configuration specification.
func NewCountTokensRecorderFromEnv() tracingapi.CountTokensRecorder {
	return NewCountTokensRecorder(nil)
}

// NewCountTokensRecorder creates a tracingapi.CountTokensRecorder with the
// given config using the OpenInference configuration specification.
func NewCountTokensRecorder(config *openinference.TraceConfig) tracingapi.CountTokensRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &CountTokensRecorder{traceConfig: config}
}

// StartParams implements the same method as defined in tracingapi.CountTokensRecorder.
func (r *CountTokensRecorder) StartParams(*anthropic.CountTokensRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "CountTokens", startOpts
}

// RecordRequest implements the same method as defined in tracingapi.CountTokensRecorder.
func (r *CountTokensRecorder) RecordRequest(span trace.Span, req *anthropic.CountTokensRequest, body []byte) {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemAnthropic),
		attribute.String(openinference.LLMModelName, req.Model),
	}
	if !r.traceConfig.HideInputs {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, string(body)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}
	span.SetAttributes(attrs...)
}

// RecordResponse implements the same method as defined in tracingapi.CountTokensRecorder.
func (r *CountTokensRecorder) RecordResponse(span trace.Span, resp *anthropic.CountTokensResponse) {
	span.SetAttributes(
		attribute.Int(openinference.LLMTokenCountPrompt, int(resp.InputTokens)),
	)
	span.SetStatus(codes.Ok, "")
}

// RecordResponseOnError implements the same method as defined in tracingapi.CountTokensRecorder.
func (r *CountTokensRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponseChunks implements SpanRecorder.RecordResponseChunks as a no-op (count_tokens doesn't stream).
func (r *CountTokensRecorder) RecordResponseChunks(trace.Span, []*struct{}) {}
