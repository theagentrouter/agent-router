// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package typesafe

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"k8s.io/utils/ptr"

	typesafeschema "github.com/envoyproxy/ai-gateway/internal/apischema/typesafe"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func newRequest() (*typesafeschema.SystemOneRequest, []byte) {
	req := &typesafeschema.SystemOneRequest{
		Model: "jev-latest",
		State: json.RawMessage(`{"subject":"Refund for order #4471"}`),
		Questions: map[string]typesafeschema.SystemOneQuestion{
			"team": {
				Type:         "choice",
				Instructions: json.RawMessage(`"Which team?"`),
				Criteria:     json.RawMessage(`{"billing":null,"shipping":null}`),
			},
		},
	}
	body, _ := json.Marshal(req)
	return req, body
}

func newResponse() *typesafeschema.SystemOneResponse {
	return &typesafeschema.SystemOneResponse{
		Model: "jev-1.13.0",
		Answers: map[string]json.RawMessage{
			"team": json.RawMessage(`{"type":"choice","choice":"billing","probabilities":{"billing":0.91,"shipping":0.09},"confidence":0.88}`),
		},
		Usage: &typesafeschema.SystemOneUsage{InputTokens: ptr.To(312), OutputTokens: ptr.To(48)},
	}
}

func TestSystemOneRecorder_StartParams(t *testing.T) {
	req, body := newRequest()
	recorder := NewSystemOneRecorderFromEnv()

	spanName, opts := recorder.StartParams(req, body)
	actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

	require.Equal(t, "SystemOne", actualSpan.Name)
	require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
}

func TestSystemOneRecorder_RecordRequest(t *testing.T) {
	req, body := newRequest()
	recorder := NewSystemOneRecorder(&openinference.TraceConfig{})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, req, body)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemTypeSafe),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMModelName, "jev-latest"),
		attribute.String(openinference.InputValue, string(body)),
		attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestSystemOneRecorder_RecordRequest_HideInputs(t *testing.T) {
	req, body := newRequest()
	recorder := NewSystemOneRecorder(&openinference.TraceConfig{HideInputs: true})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordRequest(span, req, body)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemTypeSafe),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMModelName, "jev-latest"),
		attribute.String(openinference.InputValue, openinference.RedactedValue),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestSystemOneRecorder_RecordResponse(t *testing.T) {
	resp := newResponse()
	recorder := NewSystemOneRecorder(&openinference.TraceConfig{})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, resp)
		return false
	})

	respBody, err := json.Marshal(resp)
	require.NoError(t, err)
	expected := []attribute.KeyValue{
		attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
		attribute.String(openinference.LLMModelName, "jev-1.13.0"),
		attribute.Int(openinference.LLMTokenCountPrompt, 312),
		attribute.Int(openinference.LLMTokenCountCompletion, 48),
		attribute.Int(openinference.LLMTokenCountTotal, 360),
		attribute.String(openinference.OutputValue, string(respBody)),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
	require.Equal(t, codes.Ok, actualSpan.Status.Code)
}

func TestSystemOneRecorder_RecordResponse_HideOutputs(t *testing.T) {
	resp := newResponse()
	recorder := NewSystemOneRecorder(&openinference.TraceConfig{HideOutputs: true})

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, resp)
		return false
	})

	expected := []attribute.KeyValue{
		attribute.String(openinference.LLMModelName, "jev-1.13.0"),
		attribute.Int(openinference.LLMTokenCountPrompt, 312),
		attribute.Int(openinference.LLMTokenCountCompletion, 48),
		attribute.Int(openinference.LLMTokenCountTotal, 360),
		attribute.String(openinference.OutputValue, openinference.RedactedValue),
	}
	openinference.RequireAttributesEqual(t, expected, actualSpan.Attributes)
}

func TestSystemOneRecorder_RecordResponseOnError(t *testing.T) {
	recorder := NewSystemOneRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseOnError(span, 429, []byte(`{"error":{"message":"rate limited"}}`))
		return false
	})

	require.Equal(t, codes.Error, actualSpan.Status.Code)
	require.Contains(t, actualSpan.Status.Description, "Error code: 429")
	require.Len(t, actualSpan.Events, 1)
	require.Equal(t, "exception", actualSpan.Events[0].Name)
}
