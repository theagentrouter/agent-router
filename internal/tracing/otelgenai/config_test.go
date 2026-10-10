// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

// TestNewConfig_defaultsToNoContent pins the opt-in default. The GenAI
// conventions require content capture to be off unless explicitly enabled, and
// regressing this would silently start exporting prompts.
func TestNewConfig_defaultsToNoContent(t *testing.T) {
	require.False(t, NewConfig().CaptureMessageContent)
	require.False(t, NewConfig().CapturesMessages())
}

func TestNewConfigFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		set      bool
		value    string
		expected bool
	}{
		{name: "unset does not capture", set: false, expected: false},
		{name: "empty does not capture", set: true, value: "", expected: false},
		{name: "true captures", set: true, value: "true", expected: true},
		{name: "1 captures", set: true, value: "1", expected: true},
		{name: "TRUE captures", set: true, value: "TRUE", expected: true},
		{name: "false does not capture", set: true, value: "false", expected: false},
		{name: "0 does not capture", set: true, value: "0", expected: false},
		// A typo must fail closed: content stays hidden rather than being
		// exported because someone wrote "yes" instead of "true".
		{name: "typo does not capture", set: true, value: "yes", expected: false},
		{name: "garbage does not capture", set: true, value: "!!", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The "unset" case must not inherit the variable from the
			// developer's shell.
			internaltesting.ClearTestEnv(t)
			if tc.set {
				t.Setenv(EnvCaptureMessageContent, tc.value)
			}
			cfg := NewConfigFromEnv()
			require.Equal(t, tc.expected, cfg.CaptureMessageContent)
			require.Equal(t, tc.expected, cfg.CapturesMessages())
		})
	}
}
