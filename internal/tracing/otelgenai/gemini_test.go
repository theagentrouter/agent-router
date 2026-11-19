// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestGeminiGenerateContentRecorder(t *testing.T) {
	recorder := NewGeminiGenerateContentRecorder(NewConfig())
	request := &gcp.GeminiGenerateContentRequest{Model: "alias"}
	name, opts := recorder.StartParams(request, nil)
	started := testotel.RecordNewSpan(t, name, opts...)
	require.Equal(t, "generate_content alias", started.Name)
	require.Equal(t, oteltrace.SpanKindClient, started.SpanKind)

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, request, nil)
		recorder.RecordResponseChunks(span, []*gcp.GeminiGenerateContentResponse{
			{ModelVersion: "first"},
			{ResponseID: "response-id", ModelVersion: "served", UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount: 10, ToolUsePromptTokenCount: 2, CandidatesTokenCount: 5, ThoughtsTokenCount: 3, CachedContentTokenCount: 4,
			}},
		})
		return false
	})
	require.Equal(t, codes.Ok, span.Status.Code)
	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(OperationName, "generate_content"),
		attribute.String(RequestModel, "alias"),
		attribute.String(ResponseID, "response-id"),
		attribute.String(ResponseModel, "served"),
		attribute.Int(UsageInputTokens, 12),
		attribute.Int(UsageOutputTokens, 8),
		attribute.Int(UsageCacheReadInputTokens, 4),
		attribute.Int(UsageReasoningOutputTokens, 3),
	}, span.Attributes)
}
