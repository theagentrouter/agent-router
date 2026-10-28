// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// An assistant tool call to a tool without parameters is sent back with empty
// arguments by clients that accumulate a stream in which the server sent none.
// The backends need an empty object (or no input), so empty arguments must not
// be rejected as invalid JSON.
func assistantMessageWithEmptyArguments() *openai.ChatCompletionAssistantMessageParam {
	return &openai.ChatCompletionAssistantMessageParam{
		Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
			ID:       ptr.To("call_1"),
			Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "now", Arguments: ""},
			Type:     openai.ChatCompletionMessageToolCallTypeFunction,
		}},
	}
}

func TestEmptyToolCallArgumentsAreAcceptedInRequests(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		msg, err := openAIMessageToAnthropicMessageRoleAssistant(assistantMessageWithEmptyArguments())
		require.NoError(t, err)
		require.Len(t, msg.Content, 1)
		require.NotNil(t, msg.Content[0].OfToolUse)
		require.Empty(t, msg.Content[0].OfToolUse.Input)
	})

	t.Run("bedrock", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleAssistant(assistantMessageWithEmptyArguments(), "assistant")
		require.NoError(t, err)
		require.NotNil(t, msg)
		var found bool
		for _, block := range msg.Content {
			if block.ToolUse != nil {
				found = true
				require.Empty(t, block.ToolUse.Input)
			}
		}
		require.True(t, found, "the tool use block must be kept")
	})

	t.Run("gemini", func(t *testing.T) {
		parts, _, err := assistantMsgToGeminiParts(assistantMessageWithEmptyArguments())
		require.NoError(t, err)
		require.NotEmpty(t, parts)
		var found bool
		for _, part := range parts {
			if part.FunctionCall != nil {
				found = true
				require.Empty(t, part.FunctionCall.Args)
			}
		}
		require.True(t, found, "the function call part must be kept")
	})

	t.Run("whitespace only", func(t *testing.T) {
		got, err := unmarshalToolCallArguments("  \n")
		require.NoError(t, err)
		require.Empty(t, got)
	})
}
