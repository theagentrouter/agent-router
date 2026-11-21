// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestAnthropicAssistantThinkingPreservation(t *testing.T) {
	for _, tc := range []struct {
		name, blocks, wantReasoning string
		wantBlocks                  int
	}{
		{"empty thinking omitted", `[{"type":"thinking","thinking":"","signature":""}]`, "", 0},
		{"signature only preserved", `[{"type":"thinking","thinking":"","signature":"sig"}]`, "", 1},
		{"unsigned text preserved", `[{"type":"thinking","thinking":"thought","signature":""}]`, "thought", 1},
		{"adjacent blocks do not invent separators", `[{"type":"thinking","thinking":"first ","signature":"a"},{"type":"thinking","thinking":"second","signature":"b"}]`, "first second", 2},
		{"redacted block preserved", `[{"type":"redacted_thinking","data":"opaque"}]`, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var msg anthropic.MessageParam
			require.NoError(t, json.Unmarshal([]byte(`{"role":"assistant","content":`+tc.blocks+`}`), &msg))
			got := appendAnthropicAssistantMessage(nil, msg)
			require.Len(t, got, 1)
			a := got[0].OfAssistant
			require.NotNil(t, a)
			require.Equal(t, tc.wantReasoning, a.ReasoningContent)
			require.Len(t, a.ThinkingBlocks, tc.wantBlocks)
			if tc.wantBlocks > 0 {
				var want []openai.ThinkingBlock
				require.NoError(t, json.Unmarshal([]byte(tc.blocks), &want))
				require.Equal(t, want, a.ThinkingBlocks)
			}
		})
	}
}
