// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package google

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type generateContentRecorder struct{}

// NewGenerateContentRecorder records metadata for native Gemini generation.
func NewGenerateContentRecorder() tracingapi.GeminiGenerateContentRecorder {
	return generateContentRecorder{}
}

func (generateContentRecorder) StartParams(*gcp.GeminiGenerateContentRequest, []byte) (string, []trace.SpanStartOption) {
	return "GenerateContent", []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}
}

func (generateContentRecorder) RecordRequest(s trace.Span, req *gcp.GeminiGenerateContentRequest, _ []byte) {
	s.SetAttributes(
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, "google"),
		attribute.String(openinference.LLMModelName, req.Model),
	)
}

func (generateContentRecorder) RecordResponse(s trace.Span, resp *gcp.GeminiGenerateContentResponse) {
	if resp.ModelVersion != "" {
		s.SetAttributes(attribute.String(openinference.LLMModelName, resp.ModelVersion))
	}
	if usage := resp.UsageMetadata; usage != nil {
		input, output, total := gcp.GeminiUsageCounts(usage)
		s.SetAttributes(
			attribute.Int(openinference.LLMTokenCountPrompt, input),
			attribute.Int(openinference.LLMTokenCountCompletion, output),
			attribute.Int(openinference.LLMTokenCountTotal, total),
		)
	}
	s.SetStatus(codes.Ok, "")
}

func (r generateContentRecorder) RecordResponseChunks(s trace.Span, chunks []*gcp.GeminiGenerateContentResponse) {
	if len(chunks) != 0 {
		r.RecordResponse(s, chunks[len(chunks)-1])
	}
}

func (generateContentRecorder) RecordResponseOnError(s trace.Span, statusCode int, _ []byte) {
	openinference.RecordResponseError(s, statusCode, "")
}
