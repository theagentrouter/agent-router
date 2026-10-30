// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package fal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"k8s.io/utils/ptr"

	falschema "github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

var requestBody = []byte(`{"prompt":"a cat in a hat","image_size":"square_hd"}`)

func newResponse() *falschema.Response {
	return &falschema.Response{
		Model: "openrouter/router",
		Body:  []byte(`{"output":"hi","usage":{"prompt_tokens":40,"completion_tokens":227,"total_tokens":267}}`),
		Usage: &falschema.Usage{InputTokens: ptr.To(40), OutputTokens: ptr.To(227)},
	}
}

func TestRecorder_StartParams(t *testing.T) {
	recorder := NewRecorderFromEnv()

	spanName, opts := recorder.StartParams(&falschema.Request{}, requestBody)
	actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

	require.Equal(t, "Fal", actualSpan.Name)
	require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
}

func TestRecorder_RecordRequest(t *testing.T) {
	recorder := NewRecorder(&openinference.TraceConfig{})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, &falschema.Request{}, requestBody)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemFal),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.InputValue, string(requestBody)),
		attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestRecorder_RecordRequest_HideInputs(t *testing.T) {
	recorder := NewRecorder(&openinference.TraceConfig{HideInputs: true})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, &falschema.Request{}, requestBody)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemFal),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.InputValue, openinference.RedactedValue),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestRecorder_RecordResponse(t *testing.T) {
	tests := []struct {
		name     string
		config   *openinference.TraceConfig
		resp     *falschema.Response
		expected []attribute.KeyValue
	}{
		{
			name:   "body and usage",
			config: &openinference.TraceConfig{},
			resp:   newResponse(),
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "openrouter/router"),
				attribute.Int(openinference.LLMTokenCountPrompt, 40),
				attribute.Int(openinference.LLMTokenCountCompletion, 227),
				attribute.Int(openinference.LLMTokenCountTotal, 267),
				attribute.String(openinference.OutputValue, string(newResponse().Body)),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
			},
		},
		{
			name:   "hide outputs keeps usage",
			config: &openinference.TraceConfig{HideOutputs: true},
			resp:   newResponse(),
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "openrouter/router"),
				attribute.Int(openinference.LLMTokenCountPrompt, 40),
				attribute.Int(openinference.LLMTokenCountCompletion, 227),
				attribute.Int(openinference.LLMTokenCountTotal, 267),
				attribute.String(openinference.OutputValue, openinference.RedactedValue),
			},
		},
		{
			name:   "media result without usage",
			config: &openinference.TraceConfig{},
			resp:   &falschema.Response{Model: "fal-ai/flux/dev", Body: []byte(`{"images":[{"url":"https://v3.fal.media/x.png"}]}`)},
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "fal-ai/flux/dev"),
				attribute.String(openinference.OutputValue, `{"images":[{"url":"https://v3.fal.media/x.png"}]}`),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
			},
		},
		{
			name:   "streamed response has no body",
			config: &openinference.TraceConfig{},
			resp:   &falschema.Response{Model: "openrouter/router", Usage: &falschema.Usage{InputTokens: ptr.To(40)}},
			expected: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "openrouter/router"),
				attribute.Int(openinference.LLMTokenCountPrompt, 40),
				attribute.Int(openinference.LLMTokenCountTotal, 40),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponse(span, tt.resp)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expected, actualSpan.Attributes)
			require.Equal(t, codes.Ok, actualSpan.Status.Code)
		})
	}
}

func TestRecorder_RecordResponseOnError(t *testing.T) {
	recorder := NewRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseOnError(span, 422, []byte(`{"detail":[{"msg":"field required"}]}`))
		return false
	})

	require.Equal(t, codes.Error, actualSpan.Status.Code)
	require.Contains(t, actualSpan.Status.Description, "Error code: 422")
	require.Len(t, actualSpan.Events, 1)
	require.Equal(t, "exception", actualSpan.Events[0].Name)
}
