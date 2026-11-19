// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// Gemini omits args for a function call to a tool without parameters, so the
// tool call reaches OpenAI clients with an empty object, not the JSON null a nil
// map marshals to.
func TestFunctionCallWithoutArgsHasEmptyObjectArguments(t *testing.T) {
	parts := []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "now"}}}

	t.Run("non-stream", func(t *testing.T) {
		calls, _, err := extractToolCallsFromGeminiParts([]openai.ChatCompletionMessageToolCallParam{}, parts, json.Marshal)
		require.NoError(t, err)
		require.Len(t, calls, 1)
		require.JSONEq(t, `{}`, calls[0].Function.Arguments)
	})

	t.Run("stream", func(t *testing.T) {
		o := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)
		calls, _, err := o.extractToolCallsFromGeminiPartsStream([]openai.ChatCompletionChunkChoiceDeltaToolCall{}, parts, json.Marshal)
		require.NoError(t, err)
		require.Len(t, calls, 1)
		require.JSONEq(t, `{}`, calls[0].Function.Arguments)
	})
}
