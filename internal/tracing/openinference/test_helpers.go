// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// RequireAttributesEqual re-exports testotel.RequireAttributesEqual, which is
// shared with the other semantic convention packages. It stays here so the
// existing call sites in this package's tests keep working unchanged.
func RequireAttributesEqual(t *testing.T, expected, actual []attribute.KeyValue) {
	testotel.RequireAttributesEqual(t, expected, actual)
}

var errorCodePattern = regexp.MustCompile(`^Error code: \d+ - `)

// RequireEventsEqual compensates for Go not having a reliable JSON field
// marshaling order.
func RequireEventsEqual(t *testing.T, expected, actual []trace.Event) {
	require.Len(t, actual, len(expected), "number of events differ")

	for i := range expected {
		require.Equal(t, expected[i].Name, actual[i].Name)
		require.Equal(t, expected[i].Time, actual[i].Time)
		require.Len(t, actual[i].Attributes, len(expected[i].Attributes))

		for j := range expected[i].Attributes {
			require.Equal(t, expected[i].Attributes[j].Key, actual[i].Attributes[j].Key)

			expVal := expected[i].Attributes[j].Value.AsString()
			actVal := actual[i].Attributes[j].Value.AsString()

			// Special case: exception.message with pattern "Error code: XXX - {json}".
			if expected[i].Attributes[j].Key == "exception.message" && errorCodePattern.MatchString(expVal) {
				expMatch := errorCodePattern.FindString(expVal)
				require.Equal(t, expMatch, actVal[:len(expMatch)])
				require.JSONEq(t, expVal[len(expMatch):], actVal[len(expMatch):])
			} else {
				require.Equal(t, expVal, actVal)
			}
		}
	}
}
