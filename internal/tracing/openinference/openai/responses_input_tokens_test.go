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
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

var (
	basicInputTokensReq     = &openai.ResponseRequest{Model: openai.ModelGPT5Nano, Input: openai.ResponseNewParamsInputUnion{OfString: ptr("hello")}}
	basicInputTokensReqBody = mustJSON(basicInputTokensReq)
)

func TestResponsesInputTokensRecorder_StartParams(t *testing.T) {
	recorder := NewResponsesInputTokensRecorderFromEnv()

	spanName, opts := recorder.StartParams(basicInputTokensReq, basicInputTokensReqBody)
	actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

	require.Equal(t, "ResponsesInputTokens", actualSpan.Name)
	require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
}

func TestResponsesInputTokensRecorder_RecordRequest(t *testing.T) {
	tests := []struct {
		name          string
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:   "basic request",
			config: &openinference.TraceConfig{},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicInputTokensReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
			},
		},
		{
			name:   "hide inputs",
			config: &openinference.TraceConfig{HideInputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesInputTokensRecorder(tt.config)

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, basicInputTokensReq, basicInputTokensReqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
			require.Empty(t, actualSpan.Events)
			require.Equal(t, trace.Status{Code: codes.Unset, Description: ""}, actualSpan.Status)
		})
	}
}

func TestResponsesInputTokensRecorder_RecordResponse(t *testing.T) {
	recorder := NewResponsesInputTokensRecorderFromEnv()

	resp := &openai.ResponsesInputTokensResponse{InputTokens: 42}

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, resp)
		return false
	})

	openinference.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.Int(openinference.LLMTokenCountPrompt, 42),
	}, actualSpan.Attributes)
	require.Equal(t, trace.Status{Code: codes.Ok, Description: ""}, actualSpan.Status)
}

func TestResponsesInputTokensRecorder_RecordResponseOnError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		errorBody      []byte
		expectedStatus trace.Status
		expectedEvents int
	}{
		{
			name:       "400 bad request",
			statusCode: 400,
			errorBody:  []byte(`{"error":{"message":"Invalid request","type":"invalid_request_error"}}`),
			expectedStatus: trace.Status{
				Code:        codes.Error,
				Description: "Error code: 400 - {\"error\":{\"message\":\"Invalid request\",\"type\":\"invalid_request_error\"}}",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewResponsesInputTokensRecorderFromEnv()

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

func TestResponsesInputTokensRecorder_RecordResponseChunks(t *testing.T) {
	recorder := NewResponsesInputTokensRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseChunks(span, []*struct{}{})
		return false
	})

	openinference.RequireEventsEqual(t, []trace.Event{}, actualSpan.Events)
}

func TestResponsesInputTokensRecorder_NewFromEnv(t *testing.T) {
	recorder := NewResponsesInputTokensRecorderFromEnv()
	require.NotNil(t, recorder)

	_, ok := recorder.(*ResponsesInputTokensRecorder)
	require.True(t, ok)
}

func TestResponsesInputTokensRecorder_NilConfig(t *testing.T) {
	recorder := NewResponsesInputTokensRecorder(nil)
	require.NotNil(t, recorder)

	_, ok := recorder.(*ResponsesInputTokensRecorder)
	require.True(t, ok)
}
