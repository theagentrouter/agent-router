// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestResponsesInputTokensOpenAIToOpenAI_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name              string
		prefix            string
		original          []byte
		body              openai.ResponseRequest
		forceBodyMutation bool
		modelNameOverride string
		expPath           string
	}{
		{
			name:     "no mutation",
			original: []byte(`{"model":"gpt-4.1","input":"hello"}`),
			body: openai.ResponseRequest{
				Model: "gpt-4.1",
			},
			expPath: "/responses/input_tokens",
		},
		{
			name:     "with prefix",
			prefix:   "/openai",
			original: []byte(`{"model":"gpt-4.1","input":"hello"}`),
			body: openai.ResponseRequest{
				Model: "gpt-4.1",
			},
			expPath: "/openai/responses/input_tokens",
		},
		{
			name:     "model override",
			original: []byte(`{"model":"gpt-4.1","input":"hello"}`),
			body: openai.ResponseRequest{
				Model: "gpt-4.1",
			},
			modelNameOverride: "gpt-4.1-2025-04-14",
			expPath:           "/responses/input_tokens",
		},
		{
			name:     "force body mutation",
			original: []byte(`{"model":"gpt-4.1","input":"hello"}`),
			body: openai.ResponseRequest{
				Model: "gpt-4.1",
			},
			forceBodyMutation: true,
			expPath:           "/responses/input_tokens",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewResponsesInputTokensOpenAIToOpenAITranslator(tc.prefix, tc.modelNameOverride)
			require.NotNil(t, tr)

			headerMutation, bodyMutation, err := tr.RequestBody(tc.original, &tc.body, tc.forceBodyMutation)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)

			pathHeader := headerMutation[0]
			require.Equal(t, pathHeaderName, pathHeader.Key())
			assert.Equal(t, tc.expPath, pathHeader.Value())

			if tc.modelNameOverride != "" {
				require.NotNil(t, bodyMutation)
				var parsed map[string]any
				require.NoError(t, json.Unmarshal(bodyMutation, &parsed))
				assert.Equal(t, tc.modelNameOverride, parsed["model"])
			}

			if tc.forceBodyMutation && tc.modelNameOverride == "" {
				require.Equal(t, tc.original, bodyMutation)
			}
		})
	}
}

func TestResponsesInputTokensOpenAIToOpenAI_ResponseBody(t *testing.T) {
	tr := NewResponsesInputTokensOpenAIToOpenAITranslator("", "")
	require.NotNil(t, tr)

	respBody := `{"input_tokens": 100}`
	_, _, tokenUsage, _, err := tr.ResponseBody(nil, strings.NewReader(respBody), false, nil)
	require.NoError(t, err)

	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(100), inputTokens)
}
