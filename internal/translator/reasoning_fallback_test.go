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

func TestOpenAIResponseReasoningFallback(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"absent", ``, `[{"type":"thinking","thinking":"fallback"}]`},
		{"null", `"reasoning_content":null,`, `[{"type":"thinking","thinking":"fallback"}]`},
		{"empty string", `"reasoning_content":"",`, `[{"type":"thinking","thinking":"fallback"}]`},
		{"empty object", `"reasoning_content":{},`, `[{"type":"thinking","thinking":"fallback"}]`},
		{"empty structured content", `"reasoning_content":{"reasoningContent":{}},`, `[{"type":"thinking","thinking":"fallback"}]`},
		{"empty reasoning text", `"reasoning_content":{"reasoningContent":{"reasoningText":{"text":"","signature":""}}},`, `[{"type":"thinking","thinking":"fallback"}]`},
		{"plain content wins", `"reasoning_content":"preferred",`, `[{"type":"thinking","thinking":"preferred"}]`},
		{"signed content wins", `"reasoning_content":{"reasoningContent":{"reasoningText":{"text":"preferred","signature":"sig"}}},`, `[{"type":"thinking","thinking":"preferred","signature":"sig"}]`},
		{"signature only preserved", `"reasoning_content":{"reasoningContent":{"reasoningText":{"text":"","signature":"sig"}}},`, `[{"type":"thinking","thinking":"","signature":"sig"}]`},
		{"redacted content preserved", `"reasoning_content":{"reasoningContent":{"redactedContent":"c2VjcmV0"}},`, `[{"type":"redacted_thinking","data":"secret"}]`},
		{"structured blocks win", `"thinking_blocks":[{"type":"thinking","thinking":"structured","signature":"sig"}],"reasoning_content":"preferred",`, `[{"type":"thinking","thinking":"structured","signature":"sig"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp openai.ChatCompletionResponse
			require.NoError(t, json.Unmarshal([]byte(`{"choices":[{"message":{"role":"assistant",`+tc.fields+`"reasoning":"fallback","content":"answer"}}]}`), &resp))
			result := openAIResponseToAnthropic(&resp, "model")
			require.Len(t, result.Content, 2)
			require.NotNil(t, result.Content[1].Text)
			require.Equal(t, "answer", result.Content[1].Text.Text)
			got, err := json.Marshal(result.Content[:1])
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(got))
		})
	}
}

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

func TestRedactBothReasoningFields(t *testing.T) {
	var msg openai.ChatCompletionResponseChoiceMessage
	input := `{"role":"assistant","reasoning_content":"private preferred thought","reasoning":"private fallback thought"}`
	require.NoError(t, json.Unmarshal([]byte(input), &msg))
	got := redactResponseMessage(&msg)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private preferred thought")
	require.NotContains(t, string(encoded), "private fallback thought")
	require.NotNil(t, got.ReasoningContent)
	require.NotNil(t, got.Reasoning)
	require.Contains(t, *got.Reasoning, "[REDACTED")
	original, err := json.Marshal(msg)
	require.NoError(t, err)
	require.JSONEq(t, input, string(original))
}
