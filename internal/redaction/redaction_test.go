// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package redaction

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactString(t *testing.T) {
	require.Empty(t, RedactString(""))
	out := RedactString("secret")
	require.Contains(t, out, "[REDACTED LENGTH=6 HASH=")
	// Deterministic for identical input.
	require.Equal(t, out, RedactString("secret"))
}

func TestRedactJSONTree(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		require.Equal(t, RedactString("hello"), RedactJSONTree("hello"))
		require.Empty(t, RedactJSONTree(""))
	})

	t.Run("non-string leaves unchanged", func(t *testing.T) {
		require.Equal(t, 42, RedactJSONTree(42))
		require.Equal(t, 3.14, RedactJSONTree(3.14))
		require.Equal(t, true, RedactJSONTree(true))
		require.Nil(t, RedactJSONTree(nil))
	})

	t.Run("slice of any", func(t *testing.T) {
		in := []any{"secret1", 7, []any{"nested", true}}
		out := RedactJSONTree(in)
		require.Equal(t, []any{RedactString("secret1"), 7, []any{RedactString("nested"), true}}, out)
		// Input not mutated.
		require.Equal(t, "secret1", in[0])
	})

	t.Run("map of any", func(t *testing.T) {
		in := map[string]any{"prompt": "secret", "n": 1, "nested": map[string]any{"text": "shh"}}
		out := RedactJSONTree(in)
		require.Equal(t, RedactString("secret"), out.(map[string]any)["prompt"])
		require.Equal(t, 1, out.(map[string]any)["n"])
		require.Equal(t, RedactString("shh"), out.(map[string]any)["nested"].(map[string]any)["text"])
		// Keys preserved.
		_, ok := out.(map[string]any)["prompt"]
		require.True(t, ok)
		// Input not mutated.
		require.Equal(t, "secret", in["prompt"])
	})

	t.Run("no string leaves", func(t *testing.T) {
		in := []any{1, true, nil, 2.5}
		require.Equal(t, in, RedactJSONTree(in))
	})

	t.Run("type discriminator preserved", func(t *testing.T) {
		// "type" values are JSON-union discriminators, not user data — they must be
		// preserved so the redacted tree still round-trips into typed unions.
		in := map[string]any{
			"type":    "message",
			"role":    "user",
			"content": "secret",
		}
		out := RedactJSONTree(in).(map[string]any)
		require.Equal(t, "message", out["type"], "type discriminator preserved")
		require.Equal(t, RedactString("user"), out["role"], "non-type string redacted")
		require.Equal(t, RedactString("secret"), out["content"])
	})
}
