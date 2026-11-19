// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package google

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestGenerateContentRecorder(t *testing.T) {
	recorder := NewGenerateContentRecorder()
	request := &gcp.GeminiGenerateContentRequest{Model: "alias"}
	name, opts := recorder.StartParams(request, nil)
	started := testotel.RecordNewSpan(t, name, opts...)
	require.Equal(t, "GenerateContent", started.Name)
	require.Equal(t, oteltrace.SpanKindInternal, started.SpanKind)

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, request, nil)
		recorder.RecordResponseChunks(span, []*gcp.GeminiGenerateContentResponse{
			{ModelVersion: "first"},
			{ModelVersion: "served", UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount: 10, ToolUsePromptTokenCount: 2, CandidatesTokenCount: 5, ThoughtsTokenCount: 3,
			}},
		})
		return false
	})
	require.Equal(t, codes.Ok, span.Status.Code)
	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, "google"),
		attribute.String(openinference.LLMModelName, "served"),
		attribute.Int(openinference.LLMTokenCountPrompt, 12),
		attribute.Int(openinference.LLMTokenCountCompletion, 8),
		attribute.Int(openinference.LLMTokenCountTotal, 20),
	}, span.Attributes)

	failed := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseOnError(span, 503, []byte("provider error payload"))
		return false
	})
	require.Equal(t, codes.Error, failed.Status.Code)
	require.Equal(t, "Error code: 503", failed.Status.Description)
	require.Len(t, failed.Events, 1)
	require.Equal(t, "exception", failed.Events[0].Name)
}
