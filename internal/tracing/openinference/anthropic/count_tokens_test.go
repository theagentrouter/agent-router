// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

var (
	countTokensReq = &anthropic.CountTokensRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.MessageParam{
			{
				Role:    anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{Text: "Hello, how are you?"},
			},
		},
	}
	countTokensReqBody, _ = json.Marshal(countTokensReq)

	countTokensResp = &anthropic.CountTokensResponse{
		InputTokens: 13,
	}
)

func TestCountTokensRecorder_StartParams(t *testing.T) {
	recorder := NewCountTokensRecorderFromEnv()
	spanName, opts := recorder.StartParams(countTokensReq, countTokensReqBody)
	actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

	require.Equal(t, "CountTokens", actualSpan.Name)
	require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
}

func TestCountTokensRecorder_RecordRequest(t *testing.T) {
	t.Run("basic request", func(t *testing.T) {
		recorder := NewCountTokensRecorderFromEnv()

		actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			recorder.RecordRequest(span, countTokensReq, countTokensReqBody)
			return false
		})

		expectedAttrs := []attribute.KeyValue{
			attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
			attribute.String(openinference.LLMSystem, openinference.LLMSystemAnthropic),
			attribute.String(openinference.LLMModelName, "claude-sonnet-4-20250514"),
			attribute.String(openinference.InputValue, string(countTokensReqBody)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		}
		openinference.RequireAttributesEqual(t, expectedAttrs, actualSpan.Attributes)
	})

	t.Run("hide inputs", func(t *testing.T) {
		recorder := NewCountTokensRecorder(&openinference.TraceConfig{HideInputs: true})

		actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			recorder.RecordRequest(span, countTokensReq, countTokensReqBody)
			return false
		})

		expectedAttrs := []attribute.KeyValue{
			attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
			attribute.String(openinference.LLMSystem, openinference.LLMSystemAnthropic),
			attribute.String(openinference.LLMModelName, "claude-sonnet-4-20250514"),
		}
		openinference.RequireAttributesEqual(t, expectedAttrs, actualSpan.Attributes)
	})
}

func TestCountTokensRecorder_RecordResponse(t *testing.T) {
	recorder := NewCountTokensRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponse(span, countTokensResp)
		return false
	})

	expectedAttrs := []attribute.KeyValue{
		attribute.Int(openinference.LLMTokenCountPrompt, 13),
	}
	openinference.RequireAttributesEqual(t, expectedAttrs, actualSpan.Attributes)
	require.Equal(t, trace.Status{Code: codes.Ok, Description: ""}, actualSpan.Status)
}

func TestCountTokensRecorder_RecordResponseOnError(t *testing.T) {
	recorder := NewCountTokensRecorderFromEnv()

	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		recorder.RecordResponseOnError(span, 400, []byte(`{"error":"bad request"}`))
		return false
	})

	require.Equal(t, trace.Status{Code: codes.Error, Description: "Error code: 400 - {\"error\":\"bad request\"}"}, actualSpan.Status)
}
