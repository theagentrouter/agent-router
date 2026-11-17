// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
package openinference

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestRecordResponseError(t *testing.T) {
	tests := []struct {
		name                string
		statusCode          int
		body                string
		expectedEvents      []trace.Event
		expectedDescription string
	}{
		{
			name:       "400 bad request",
			statusCode: 400,
			body:       `{"error": {"message": "Invalid request"}}`,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "BadRequestError"),
						attribute.String("exception.message", `Error code: 400 - {"error": {"message": "Invalid request"}}`),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: `Error code: 400 - {"error": {"message": "Invalid request"}}`,
		},
		{
			name:       "401 unauthorized",
			statusCode: 401,
			body:       `{"error": {"message": "Unauthorized"}}`,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "AuthenticationError"),
						attribute.String("exception.message", `Error code: 401 - {"error": {"message": "Unauthorized"}}`),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: `Error code: 401 - {"error": {"message": "Unauthorized"}}`,
		},
		{
			name:       "403 forbidden",
			statusCode: 403,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "PermissionDeniedError"),
						attribute.String("exception.message", "Error code: 403"),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: "Error code: 403",
		},
		{
			name:       "404 not found",
			statusCode: 404,
			body:       `{"error": {"message": "Model not found"}}`,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "NotFoundError"),
						attribute.String("exception.message", `Error code: 404 - {"error": {"message": "Model not found"}}`),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: `Error code: 404 - {"error": {"message": "Model not found"}}`,
		},
		{
			name:       "429 rate limit",
			statusCode: 429,
			body:       `{"error": {"message": "Rate limit exceeded"}}`,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "RateLimitError"),
						attribute.String("exception.message", `Error code: 429 - {"error": {"message": "Rate limit exceeded"}}`),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: `Error code: 429 - {"error": {"message": "Rate limit exceeded"}}`,
		},
		{
			name:       "500 internal server error",
			statusCode: 500,
			body:       `{"error": {"message": "Internal error"}}`,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "InternalServerError"),
						attribute.String("exception.message", `Error code: 500 - {"error": {"message": "Internal error"}}`),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: `Error code: 500 - {"error": {"message": "Internal error"}}`,
		},
		{
			name:       "unknown error code",
			statusCode: 599,
			body:       `{"error": {"message": "Unknown error"}}`,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "Error"),
						attribute.String("exception.message", `Error code: 599 - {"error": {"message": "Unknown error"}}`),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: `Error code: 599 - {"error": {"message": "Unknown error"}}`,
		},
		{
			name:       "error without body",
			statusCode: 500,
			expectedEvents: []trace.Event{
				{
					Name: "exception",
					Attributes: []attribute.KeyValue{
						attribute.String("exception.type", "InternalServerError"),
						attribute.String("exception.message", "Error code: 500"),
					},
					Time: time.Time{},
				},
			},
			expectedDescription: "Error code: 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				RecordResponseError(span, NewTraceConfig(), tt.statusCode, tt.body)
				return false // Recording of error shouldn't end the span.
			})
			RequireEventsEqual(t, tt.expectedEvents, actualSpan.Events)
			require.Equal(t, codes.Error, actualSpan.Status.Code)
			require.Equal(t, tt.expectedDescription, actualSpan.Status.Description)
		})
	}
}

// TestRecordResponseError_HidesBodyWhenContentIsHidden verifies that the error
// description drops the upstream body whenever TraceConfig hides inputs or
// outputs, since provider error bodies routinely echo the request. This mirrors
// the GenAI tracer's equivalent rule.
func TestRecordResponseError_HidesBodyWhenContentIsHidden(t *testing.T) {
	tests := []struct {
		name   string
		config *TraceConfig
	}{
		{name: "hide inputs", config: &TraceConfig{HideInputs: true}},
		{name: "hide outputs", config: &TraceConfig{HideOutputs: true}},
		{name: "hide inputs and outputs", config: &TraceConfig{HideInputs: true, HideOutputs: true}},
		{name: "nil config fails closed", config: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				RecordResponseError(span, tt.config, 400, `{"error": {"message": "Invalid request"}}`)
				return false
			})
			require.Equal(t, codes.Error, actualSpan.Status.Code)
			require.Len(t, actualSpan.Events, 1)
			require.Equal(t, "exception", actualSpan.Events[0].Name)
			const expectedDescription = "Error code: 400"
			require.Equal(t, expectedDescription, actualSpan.Status.Description)
			require.Equal(t, expectedDescription, actualSpan.Events[0].Attributes[1].Value.AsString())
		})
	}
}

// TestRecordResponseError_DoesNotLeakBody is the regression guard for the
// redaction hole: with content capture hidden, no part of the body may reach
// the span.
func TestRecordResponseError_DoesNotLeakBody(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		RecordResponseError(span, &TraceConfig{HideInputs: true, HideOutputs: true}, 400, `{"error":"`+secret+`"}`)
		return false
	})

	require.Equal(t, "Error code: 400", span.Status.Description)
	for _, attr := range span.Attributes {
		require.NotContains(t, attr.Value.AsString(), secret, "attribute %s", attr.Key)
	}
	for _, event := range span.Events {
		for _, attr := range event.Attributes {
			require.NotContains(t, attr.Value.AsString(), secret, "event attribute %s", attr.Key)
		}
	}
}
