// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package openai provides OpenInference semantic conventions hooks for
// OpenAI instrumentation used by the ExtProc router filter.
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

// ChatCompletionRecorder implements recorders for OpenInference chat completion spans.
type ChatCompletionRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewChatCompletionRecorderFromEnv creates an tracingapi.ChatCompletionRecorder
// from environment variables using the OpenInference configuration specification.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewChatCompletionRecorderFromEnv() tracingapi.ChatCompletionRecorder {
	return NewChatCompletionRecorder(nil)
}

// NewChatCompletionRecorder creates a tracingapi.ChatCompletionRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewChatCompletionRecorder(config *openinference.TraceConfig) tracingapi.ChatCompletionRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &ChatCompletionRecorder{traceConfig: config}
}

// startOpts sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var startOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.ChatCompletionRecorder.
func (r *ChatCompletionRecorder) StartParams(*openai.ChatCompletionRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "ChatCompletion", startOpts
}

// RecordRequest implements the same method as defined in tracingapi.ChatCompletionRecorder.
func (r *ChatCompletionRecorder) RecordRequest(span trace.Span, chatReq *openai.ChatCompletionRequest, body []byte) {
	span.SetAttributes(buildRequestAttributes(chatReq, string(body), r.traceConfig)...)
}

// RecordResponseChunks implements the same method as defined in tracingapi.ChatCompletionRecorder.
func (r *ChatCompletionRecorder) RecordResponseChunks(span trace.Span, chunks []*openai.ChatCompletionResponseChunk) {
	if len(chunks) > 0 {
		span.AddEvent("First Token Stream Event")
	}
	converted := convertSSEToJSON(chunks)
	r.RecordResponse(span, converted)
}

// RecordResponseOnError implements the same method as defined in tracingapi.ChatCompletionRecorder.
func (r *ChatCompletionRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, r.traceConfig, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.ChatCompletionRecorder.
func (r *ChatCompletionRecorder) RecordResponse(span trace.Span, resp *openai.ChatCompletionResponse) {
	// Set output attributes.
	var attrs []attribute.KeyValue
	attrs = buildResponseAttributes(resp, r.traceConfig)

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
