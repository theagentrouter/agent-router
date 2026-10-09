// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	openaiSchema "github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// ResponsesInputTokensRecorder implements recorders for OpenInference /v1/responses/input_tokens spans.
type ResponsesInputTokensRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewResponsesInputTokensRecorderFromEnv creates a tracingapi.ResponsesInputTokensRecorder
// from environment variables using the OpenInference configuration specification.
func NewResponsesInputTokensRecorderFromEnv() tracingapi.ResponsesInputTokensRecorder {
	return NewResponsesInputTokensRecorder(nil)
}

// NewResponsesInputTokensRecorder creates a tracingapi.ResponsesInputTokensRecorder with the
// given config using the OpenInference configuration specification.
func NewResponsesInputTokensRecorder(config *openinference.TraceConfig) tracingapi.ResponsesInputTokensRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &ResponsesInputTokensRecorder{traceConfig: config}
}

// StartParams implements the same method as defined in tracingapi.ResponsesInputTokensRecorder.
func (r *ResponsesInputTokensRecorder) StartParams(*openaiSchema.ResponseRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "ResponsesInputTokens", startOpts
}

// RecordRequest implements the same method as defined in tracingapi.ResponsesInputTokensRecorder.
func (r *ResponsesInputTokensRecorder) RecordRequest(span trace.Span, req *openaiSchema.ResponseRequest, body []byte) {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
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

// RecordResponse implements the same method as defined in tracingapi.ResponsesInputTokensRecorder.
func (r *ResponsesInputTokensRecorder) RecordResponse(span trace.Span, resp *openaiSchema.ResponsesInputTokensResponse) {
	span.SetAttributes(
		attribute.Int(openinference.LLMTokenCountPrompt, int(resp.InputTokens)),
	)
	span.SetStatus(codes.Ok, "")
}

// RecordResponseOnError implements the same method as defined in tracingapi.ResponsesInputTokensRecorder.
func (r *ResponsesInputTokensRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponseChunks implements SpanRecorder.RecordResponseChunks as a no-op (responses/input_tokens doesn't stream).
func (r *ResponsesInputTokensRecorder) RecordResponseChunks(trace.Span, []*struct{}) {}
