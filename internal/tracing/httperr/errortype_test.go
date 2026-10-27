// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package httperr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenInferenceErrorType(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   string
	}{
		{statusCode: 400, expected: "BadRequestError"},
		{statusCode: 401, expected: "AuthenticationError"},
		{statusCode: 403, expected: "PermissionDeniedError"},
		{statusCode: 404, expected: "NotFoundError"},
		{statusCode: 429, expected: "RateLimitError"},
		{statusCode: 500, expected: "InternalServerError"},
		{statusCode: 502, expected: "InternalServerError"},
		{statusCode: 503, expected: "InternalServerError"},
		// Unmapped statuses, including ones adjacent to mapped entries.
		{statusCode: 402, expected: FallbackOpenInference},
		{statusCode: 428, expected: FallbackOpenInference},
		{statusCode: 430, expected: FallbackOpenInference},
		{statusCode: 501, expected: FallbackOpenInference},
		{statusCode: 504, expected: FallbackOpenInference},
		{statusCode: 200, expected: FallbackOpenInference},
		{statusCode: 0, expected: FallbackOpenInference},
		{statusCode: -1, expected: FallbackOpenInference},
	}

	for _, tc := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			require.Equal(t, tc.expected, OpenInferenceErrorType(tc.statusCode),
				"status %d", tc.statusCode)
		})
	}
}

func TestGenAIErrorType(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   string
	}{
		{statusCode: 400, expected: "400"},
		{statusCode: 401, expected: "401"},
		{statusCode: 403, expected: "403"},
		{statusCode: 404, expected: "404"},
		{statusCode: 429, expected: "429"},
		// 502 and 503 collapse onto 500 to match the OpenInference grouping.
		{statusCode: 500, expected: "500"},
		{statusCode: 502, expected: "500"},
		{statusCode: 503, expected: "500"},
		// Unmapped 4xx/5xx report their own status rather than inventing a name.
		{statusCode: 402, expected: "402"},
		{statusCode: 418, expected: "418"},
		{statusCode: 501, expected: "501"},
		{statusCode: 599, expected: "599"},
		// Boundaries of the 4xx-5xx window.
		{statusCode: 399, expected: FallbackGenAI},
		{statusCode: 600, expected: FallbackGenAI},
		// Non-error statuses stay low-cardinality.
		{statusCode: 200, expected: FallbackGenAI},
		{statusCode: 0, expected: FallbackGenAI},
		{statusCode: -1, expected: FallbackGenAI},
	}

	for _, tc := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			require.Equal(t, tc.expected, GenAIErrorType(tc.statusCode),
				"status %d", tc.statusCode)
		})
	}
}

// TestGenAIErrorType_boundedCardinality pins that no status can produce an
// unbounded set of error.type values, which would blow up backend cardinality.
func TestGenAIErrorType_boundedCardinality(t *testing.T) {
	seen := make(map[string]struct{})
	for status := -100; status <= 700; status++ {
		seen[GenAIErrorType(status)] = struct{}{}
	}
	// 4xx and 5xx each contribute at most 100 values, minus the 502/503 that
	// collapse onto 500, plus the fallback.
	require.LessOrEqual(t, len(seen), 200)
	require.Contains(t, seen, FallbackGenAI)
}
