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

// ResponsesRecorder implements recorders for OpenInference responses spans.
type ResponsesRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewResponsesRecorderFromEnv creates an tracingapi.ResponsesRecorder
// from environment variables using the OpenInference configuration specification.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewResponsesRecorderFromEnv() tracingapi.ResponsesRecorder {
	return NewResponsesRecorder(nil)
}

// NewResponsesRecorder creates a tracingapi.ResponsesRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewResponsesRecorder(config *openinference.TraceConfig) tracingapi.ResponsesRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &ResponsesRecorder{traceConfig: config}
}

// responsesStartOpts sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var responsesStartOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.ResponsesRecorder.
func (r *ResponsesRecorder) StartParams(*openai.ResponseRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Responses", responsesStartOpts
}

// RecordRequest implements the same method as defined in tracingapi.ResponsesRecorder.
func (r *ResponsesRecorder) RecordRequest(span trace.Span, req *openai.ResponseRequest, body []byte) {
	span.SetAttributes(buildResponsesRequestAttributes(req, body, r.traceConfig)...)
}

// RecordResponseChunks implements the same method as defined in tracingapi.ResponsesRecorder.
func (r *ResponsesRecorder) RecordResponseChunks(span trace.Span, chunks []*openai.ResponseStreamEventUnion) {
	if len(chunks) > 0 {
		span.AddEvent("First Token Stream Event")
	}
	for _, chunk := range chunks {
		if chunk != nil {
			// response.completed event contains the full response so we don't need to accumulate chunks.
			if chunk.GetEventType() == "response.completed" {
				respComplEvent := chunk.OfResponseCompleted
				span.AddEvent("Response Completed Event")
				r.RecordResponse(span, &respComplEvent.Response)
			}
		}
	}
}

// RecordResponseOnError implements the same method as defined in tracingapi.ResponsesRecorder.
func (r *ResponsesRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, r.traceConfig, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.ResponsesRecorder.
func (r *ResponsesRecorder) RecordResponse(span trace.Span, resp *openai.Response) {
	// Add response attributes.
	attrs := buildResponsesResponseAttributes(resp, r.traceConfig)

	bodyString := openinference.RedactedValue
	if !r.traceConfig.HideOutputs {
		marshaled, err := json.Marshal(resp)
		if err == nil {
			bodyString = string(marshaled)
		}
	}
	attrs = append(attrs, attribute.String(openinference.OutputValue, bodyString))
	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}
