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

// CompletionRecorder implements recorders for OpenInference completions spans.
type CompletionRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewCompletionRecorderFromEnv creates an tracingapi.CompletionRecorder
// from environment variables using the OpenInference configuration specification.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#completions-api-legacy-text-completion
func NewCompletionRecorderFromEnv() tracingapi.CompletionRecorder {
	return NewCompletionRecorder(nil)
}

// NewCompletionRecorder creates a tracingapi.CompletionRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#completions-api-legacy-text-completion
func NewCompletionRecorder(config *openinference.TraceConfig) tracingapi.CompletionRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &CompletionRecorder{traceConfig: config}
}

// startOptsCompletion sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var startOptsCompletion = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.CompletionRecorder.
func (r *CompletionRecorder) StartParams(*openai.CompletionRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Completion", startOptsCompletion
}

// RecordRequest implements the same method as defined in tracingapi.CompletionRecorder.
func (r *CompletionRecorder) RecordRequest(span trace.Span, req *openai.CompletionRequest, body []byte) {
	span.SetAttributes(buildCompletionRequestAttributes(req, body, r.traceConfig)...)
}

// RecordResponseOnError implements the same method as defined in tracingapi.CompletionRecorder.
func (r *CompletionRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, r.traceConfig, statusCode, string(body))
}

// RecordResponseChunks implements the same method as defined in tracingapi.CompletionRecorder.
// For completions, streaming chunks are full CompletionResponse objects (not deltas like chat).
// This method aggregates chunks into a single response for recording.
func (r *CompletionRecorder) RecordResponseChunks(span trace.Span, chunks []*openai.CompletionResponse) {
	if len(chunks) == 0 {
		return
	}

	// Add "First Token Stream Event" timestamp event for streaming.
	span.AddEvent("First Token Stream Event")

	// Aggregate chunks into a single response.
	// Completion streaming chunks contain deltas (incremental text), similar to chat completions.
	aggregated := *chunks[0] // Start with first chunk's metadata

	// Concatenate text from chunks for each choice.
	// Completion streaming chunks contain deltas (incremental text), similar to chat completions.
	if len(aggregated.Choices) > 0 {
		// Build map of choice index to concatenated text and finish reason
		textByChoice := make(map[int]string)
		finishReasonByChoice := make(map[int]string)

		for _, chunk := range chunks {
			for _, choice := range chunk.Choices {
				idx := 0
				if choice.Index != nil {
					idx = *choice.Index
				}
				// Concatenate delta text
				textByChoice[idx] += choice.Text
				if choice.FinishReason != "" {
					finishReasonByChoice[idx] = choice.FinishReason
				}
			}
		}

		// Create new choices slice to avoid modifying the original chunks
		newChoices := make([]openai.CompletionChoice, len(aggregated.Choices))
		for i := range aggregated.Choices {
			idx := 0
			if aggregated.Choices[i].Index != nil {
				idx = *aggregated.Choices[i].Index
			}
			// Copy the choice and update with concatenated text
			newChoices[i] = aggregated.Choices[i]
			newChoices[i].Text = textByChoice[idx]
			if reason, ok := finishReasonByChoice[idx]; ok {
				newChoices[i].FinishReason = reason
			}
		}
		aggregated.Choices = newChoices
	}

	// Use the last chunk's metadata (ID, created, model) and usage if present
	lastChunk := chunks[len(chunks)-1]
	aggregated.ID = lastChunk.ID
	aggregated.Created = lastChunk.Created
	aggregated.Model = lastChunk.Model
	if lastChunk.Usage != nil {
		aggregated.Usage = lastChunk.Usage
	}

	// Record the aggregated response.
	r.RecordResponse(span, &aggregated)
}

// RecordResponse implements the same method as defined in tracingapi.CompletionRecorder.
func (r *CompletionRecorder) RecordResponse(span trace.Span, resp *openai.CompletionResponse) {
	// Add response attributes.
	attrs := buildCompletionResponseAttributes(resp, r.traceConfig)

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
