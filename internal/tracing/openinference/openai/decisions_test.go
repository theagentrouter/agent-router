// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	openaischema "github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestDecisionsRecorder_RecordRequest(t *testing.T) {
	req := &openaischema.DecisionRequest{Model: "gpt-6-luna"}

	tests := []struct {
		name     string
		body     string
		config   *openinference.TraceConfig
		expected []attribute.KeyValue
	}{
		{
			name:   "records native request",
			body:   `{"model":"gpt-6-luna","input":"sensitive evidence","questions":[]}`,
			config: &openinference.TraceConfig{},
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMModelName, "gpt-6-luna"),
				attribute.String(openinference.InputValue, `{"model":"gpt-6-luna","input":"sensitive evidence","questions":[]}`),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
			},
		},
		{
			name:   "redacts input",
			body:   `{"model":"gpt-6-luna","input":"sensitive evidence","questions":[]}`,
			config: &openinference.TraceConfig{HideInputs: true},
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMModelName, "gpt-6-luna"),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
			},
		},
		{
			name:   "redacts inline input images",
			body:   `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_text","text":"classify this"},{"type":"input_image","image_url":"data:image/png;base64,c2Vuc2l0aXZl"}]}],"questions":[]}`,
			config: &openinference.TraceConfig{HideInputImages: true},
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMModelName, "gpt-6-luna"),
				attribute.String(openinference.InputValue, `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_text","text":"classify this"},{"type":"input_image","image_url":"__REDACTED__"}]}],"questions":[]}`),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewDecisionsRecorder(tc.config)
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, req, []byte(tc.body))
				return false
			})
			openinference.RequireAttributesEqual(t, tc.expected, span.Attributes)
		})
	}
}

func TestDecisionsRecorder_RecordResponse(t *testing.T) {
	resp := &openaischema.DecisionResponse{
		Answers: []openaischema.DecisionAnswer{{Type: "choice", Name: "route", Choice: json.RawMessage(`"billing"`)}},
		Model:   "gpt-6-luna-2026-09-01",
		Usage: &openaischema.ResponseUsage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
			InputTokensDetails: openaischema.ResponseUsageInputTokensDetails{
				CachedTokens:     80,
				CacheWriteTokens: 20,
			},
			OutputTokensDetails: openaischema.ResponseUsageOutputTokensDetails{ReasoningTokens: 30},
		},
	}
	responseJSON := `{"answers":[{"type":"choice","name":"route","choice":"billing"}],"model":"gpt-6-luna-2026-09-01","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":20,"cache_creation_input_tokens":20},"output_tokens":50,"output_tokens_details":{"reasoning_tokens":30},"total_tokens":150}}`
	metadataAttrs := []attribute.KeyValue{
		attribute.String(openinference.LLMModelName, "gpt-6-luna-2026-09-01"),
		attribute.Int(openinference.LLMTokenCountPrompt, 100),
		attribute.Int(openinference.LLMTokenCountCompletion, 50),
		attribute.Int(openinference.LLMTokenCountTotal, 150),
		attribute.Int(openinference.LLMTokenCountPromptCacheHit, 80),
		attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 20),
		attribute.Int(openinference.LLMTokenCountCompletionReasoning, 30),
	}

	for _, tc := range []struct {
		name     string
		config   *openinference.TraceConfig
		expected string
		mimeType bool
	}{
		{name: "records response", config: &openinference.TraceConfig{}, expected: responseJSON, mimeType: true},
		{name: "redacts response", config: &openinference.TraceConfig{HideOutputs: true}, expected: openinference.RedactedValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewDecisionsRecorder(tc.config)
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordResponse(span, resp)
				return false
			})
			expected := append([]attribute.KeyValue{}, metadataAttrs...)
			if tc.mimeType {
				expected = append(expected, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
			}
			expected = append(expected, attribute.String(openinference.OutputValue, tc.expected))
			openinference.RequireAttributesEqual(t, expected, span.Attributes)
			require.Equal(t, codes.Ok, span.Status.Code)
		})
	}
}
