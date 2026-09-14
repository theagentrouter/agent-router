// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testotel

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// RequireAttributesEqual compensates for Go not having a reliable JSON field
// marshaling order.
func RequireAttributesEqual(t *testing.T, expected, actual []attribute.KeyValue) {
	expectedMap := make(map[attribute.Key]attribute.Value, len(expected))
	for _, attr := range expected {
		if _, exists := expectedMap[attr.Key]; exists {
			t.Fatalf("duplicate key in expected attributes: %s", attr.Key)
		}
		expectedMap[attr.Key] = attr.Value
	}

	require.Len(t, actual, len(expectedMap), "number of attributes differ")

	for _, attr := range actual {
		expVal, found := expectedMap[attr.Key]
		require.True(t, found, "unexpected attribute key in actual: %s", attr.Key)

		valStr := expVal.AsString()
		if len(valStr) > 0 && (valStr[0] == '{' || valStr[0] == '[') {
			// Try to parse as JSON, but if it fails, fall back to string comparison.
			var expectedJSON any
			if err := json.Unmarshal([]byte(valStr), &expectedJSON); err == nil {
				require.JSONEq(t, valStr, attr.Value.AsString(), "attribute %s does not match expected JSON", attr.Key)
			} else {
				// Not valid JSON, do string comparison.
				require.Equal(t, expVal, attr.Value, "attribute %s values do not match", attr.Key)
			}
		} else {
			require.Equal(t, expVal, attr.Value, "attribute %s values do not match", attr.Key)
		}
	}
}
