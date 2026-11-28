// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package json

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarshalDeterministic(t *testing.T) {
	value := map[string]any{"z": []any{map[string]any{"b": 2, "a": 1}}, "a": "<value>"}
	for range 32 {
		encoded, err := MarshalDeterministic(value)
		require.NoError(t, err)
		require.Equal(t, `{"a":"<value>","z":[{"a":1,"b":2}]}`, string(encoded))
	}
	_, err := MarshalDeterministic(map[string]any{"unsupported": make(chan int)})
	require.Error(t, err)
}
