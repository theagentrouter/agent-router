// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package cohere

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestRerankRecorder_StartParams(t *testing.T) {
	req := &cohereschema.RerankV2Request{
		Model:     "rerank-english-v3",
		Query:     "reset password",
		TopN:      new(2),
		Documents: []string{"d1", "d2"},
	}
	reqBody, _ := json.Marshal(req)

	recorder := NewRerankRecorderFromEnv()

	spanName, opts := recorder.StartParams(req, reqBody)
	actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

	require.Equal(t, "Rerank", actualSpan.Name)
	require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
}

func TestRerankRecorder_RecordRequest(t *testing.T) {
	req := &cohereschema.RerankV2Request{
		Model:     "rerank-english-v3",
		Query:     "reset password",
		TopN:      new(2),
		Documents: []string{"d1", "d2"},
	}
	reqBody, _ := json.Marshal(req)

	recorder := NewRerankRecorder(&openinference.TraceConfig{})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, req, reqBody)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemCohere),
		attribute.String(openinference.SpanKind, openinference.SpanKindReranker),
		attribute.String(openinference.RerankerModelName, "rerank-english-v3"),
		attribute.Int(openinference.RerankerTopK, 2),
		attribute.String(openinference.RerankerQuery, "reset password"),
		attribute.String(openinference.InputValue, string(reqBody)),
		attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		attribute.String(openinference.RerankerInputDocumentAttribute(0, openinference.DocumentContent), "d1"),
		attribute.String(openinference.RerankerInputDocumentAttribute(1, openinference.DocumentContent), "d2"),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestRerankRecorder_RecordRequest_HideInputs(t *testing.T) {
	req := &cohereschema.RerankV2Request{
		Model:     "rerank-english-v3",
		Query:     "reset password",
		TopN:      new(2),
		Documents: []string{"d1", "d2"},
	}
	reqBody, _ := json.Marshal(req)

	recorder := NewRerankRecorder(&openinference.TraceConfig{HideInputs: true})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, req, reqBody)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemCohere),
		attribute.String(openinference.SpanKind, openinference.SpanKindReranker),
		attribute.String(openinference.RerankerModelName, "rerank-english-v3"),
		attribute.Int(openinference.RerankerTopK, 2),
		attribute.String(openinference.RerankerQuery, "reset password"),
		attribute.String(openinference.InputValue, openinference.RedactedValue),
		// No InputMimeType and no document content attributes when inputs are hidden
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestRerankRecorder_RecordResponse(t *testing.T) {
	resp := &cohereschema.RerankV2Response{
		Results: []*cohereschema.RerankV2Result{{Index: 1, RelevanceScore: 0.9}},
		Meta: &cohereschema.RerankV2Meta{
			Tokens: &cohereschema.RerankV2Tokens{
				InputTokens:  fptr(25),
				OutputTokens: fptr(0),
			},
		},
	}

	recorder := NewRerankRecorder(&openinference.TraceConfig{})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, resp)
		return false
	})

	respJSON, _ := json.Marshal(resp)
	expected := []attribute.KeyValue{
		attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
		attribute.Float64(openinference.RerankerOutputDocumentAttribute(0, openinference.DocumentScore), 0.9),
		attribute.Int(openinference.LLMTokenCountPrompt, 25),
		attribute.Int(openinference.LLMTokenCountTotal, 25),
		attribute.String(openinference.OutputValue, string(respJSON)),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestRerankRecorder_RecordResponse_HideOutputs(t *testing.T) {
	resp := &cohereschema.RerankV2Response{
		Results: []*cohereschema.RerankV2Result{
			{Index: 0, RelevanceScore: 0.42},
			{Index: 1, RelevanceScore: 0.84},
		},
		Meta: &cohereschema.RerankV2Meta{
			Tokens: &cohereschema.RerankV2Tokens{
				InputTokens:  fptr(3),
				OutputTokens: fptr(2),
			},
		},
	}

	recorder := NewRerankRecorder(&openinference.TraceConfig{HideOutputs: true})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, resp)
		return false
	})

	expected := []attribute.KeyValue{
		// No OutputMimeType and no per-document scores when outputs are hidden
		attribute.Int(openinference.LLMTokenCountPrompt, 3),
		attribute.Int(openinference.LLMTokenCountTotal, 5),
		attribute.String(openinference.OutputValue, openinference.RedactedValue),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
	require.Equal(t, trace.Status{Code: codes.Ok, Description: ""}, actualSpan.Status)
}

func TestRerankRecorder_RecordResponseOnError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		errorBody      []byte
		expectedStatus trace.Status
		expectedEvents int
	}{
		{
			name:       "404 not found error",
			statusCode: 404,
			errorBody:  []byte(`{"error":{"message":"Not found"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 404 - {\"error\":{\"message\":\"Not found\"}}",
			},
			expectedEvents: 1,
		},
		{
			name:       "500 internal server error",
			statusCode: 500,
			errorBody:  []byte(`{"error":{"message":"Internal server error"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 500 - {\"error\":{\"message\":\"Internal server error\"}}",
			},
			expectedEvents: 1,
		},
		{
			name:       "400 bad request",
			statusCode: 400,
			errorBody:  []byte(`{"error":{"message":"Invalid input"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 400 - {\"error\":{\"message\":\"Invalid input\"}}",
			},
			expectedEvents: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewRerankRecorderFromEnv()

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponseOnError(span, tt.statusCode, tt.errorBody)
				return false
			})

			require.Equal(t, tt.expectedStatus, actualSpan.Status)
			require.Len(t, actualSpan.Events, tt.expectedEvents)
			if tt.expectedEvents > 0 {
				require.Equal(t, "exception", actualSpan.Events[0].Name)
			}
		})
	}
}

//go:fix inline

func fptr(v int) *float64 {
	f := float64(v)
	return &f
}
