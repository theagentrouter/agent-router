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

	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestRecordResponseError(t *testing.T) {
	const body = `{"error":{"message":"the prompt was: my secret"}}`

	tests := []struct {
		name                string
		captureContent      bool
		statusCode          int
		body                string
		expectedErrorType   string
		expectedDescription string
	}{
		{
			name:                "capture off omits body",
			statusCode:          400,
			body:                body,
			expectedErrorType:   "400",
			expectedDescription: "Error code: 400",
		},
		{
			name:                "capture on includes body",
			captureContent:      true,
			statusCode:          400,
			body:                body,
			expectedErrorType:   "400",
			expectedDescription: "Error code: 400 - " + body,
		},
		{
			name:                "capture on with empty body omits separator",
			captureContent:      true,
			statusCode:          429,
			body:                "",
			expectedErrorType:   "429",
			expectedDescription: "Error code: 429",
		},
		{
			name:                "unmapped status reports itself",
			statusCode:          418,
			expectedErrorType:   "418",
			expectedDescription: "Error code: 418",
		},
		{
			name:                "non-http status falls back",
			statusCode:          0,
			expectedErrorType:   "_OTHER",
			expectedDescription: "Error code: 0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{CaptureMessageContent: tc.captureContent}

			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				RecordResponseError(span, cfg, tc.statusCode, tc.body)
				return false
			})

			testotel.RequireAttributesEqual(t, []attribute.KeyValue{
				attribute.String(ErrorType, tc.expectedErrorType),
			}, span.Attributes)

			require.Equal(t, codes.Error, span.Status.Code)
			require.Equal(t, tc.expectedDescription, span.Status.Description)

			// GenAI records errors via error.type, not an exception event.
			require.Empty(t, span.Events)
		})
	}
}

// TestRecordResponseError_doesNotLeakBody is the regression guard for the
// redaction hole: with capture off, no part of the body may reach the span.
func TestRecordResponseError_doesNotLeakBody(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		RecordResponseError(span, NewConfig(), 400, `{"error":"`+secret+`"}`)
		return false
	})

	require.NotContains(t, span.Status.Description, secret)
	for _, attr := range span.Attributes {
		require.NotContains(t, attr.Value.AsString(), secret, "attribute %s", attr.Key)
	}
}
