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

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestCountTokensToGCPAnthropic_RequestBody(t *testing.T) {
	// GCP Vertex AI uses "count-tokens" as the model in the path, regardless of the actual model.
	expPath := "publishers/anthropic/models/count-tokens:rawPredict"

	tests := []struct {
		name     string
		override string
		model    string
		expModel string
	}{
		{
			name:     "no override keeps original model in body",
			model:    "claude-opus-4-6",
			expModel: "claude-opus-4-6",
		},
		{
			name:     "with override replaces model in body",
			override: "claude-sonnet-4-20250514",
			model:    "claude-opus-4-6",
			expModel: "claude-sonnet-4-20250514",
		},
		{
			name:     "strips @default version tag from override",
			override: "claude-sonnet-4-6@default",
			model:    "claude-opus-4-6",
			expModel: "claude-sonnet-4-6",
		},
		{
			name:     "keeps explicit @version tag in override",
			override: "claude-haiku-4-5@20251001",
			model:    "claude-opus-4-6",
			expModel: "claude-haiku-4-5@20251001",
		},
		{
			name:     "strips @latest version tag from override",
			override: "claude-sonnet-4-6@latest",
			model:    "claude-opus-4-6",
			expModel: "claude-sonnet-4-6",
		},
		{
			name:     "strips @default version tag from request model",
			model:    "claude-sonnet-4-6@default",
			expModel: "claude-sonnet-4-6",
		},
		{
			name:     "strips @latest version tag from request model",
			model:    "claude-sonnet-4-6@latest",
			expModel: "claude-sonnet-4-6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewCountTokensToGCPAnthropicTranslator("vertex-2023-10-16", tt.override)
			require.NotNil(t, translator)

			raw := []byte(`{"model":"` + tt.model + `","messages":[{"role":"user","content":"hello"}]}`)
			req := &anthropicschema.CountTokensRequest{Model: tt.model}

			headerMutation, bodyMutation, err := translator.RequestBody(raw, req, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)
			require.NotNil(t, bodyMutation)

			// Path always uses "count-tokens" as the model.
			pathHeader := headerMutation[0]
			require.Equal(t, pathHeaderName, pathHeader.Key())
			assert.Equal(t, expPath, pathHeader.Value())

			// Verify body has anthropic_version and model is preserved (or overridden).
			var parsed map[string]any
			require.NoError(t, json.Unmarshal(bodyMutation, &parsed))
			assert.Equal(t, "vertex-2023-10-16", parsed["anthropic_version"])
			assert.Equal(t, tt.expModel, parsed["model"])
		})
	}
}

func TestCountTokensToGCPAnthropic_RequestBody_MissingVersion(t *testing.T) {
	translator := NewCountTokensToGCPAnthropicTranslator("", "")
	require.NotNil(t, translator)

	raw := []byte(`{"model":"claude-opus-4-6","messages":[]}`)
	req := &anthropicschema.CountTokensRequest{Model: "claude-opus-4-6"}

	_, _, err := translator.RequestBody(raw, req, false)
	require.ErrorContains(t, err, "anthropic_version is required")
}

func TestCountTokensToGCPAnthropic_ResponseBody(t *testing.T) {
	translator := NewCountTokensToGCPAnthropicTranslator("vertex-2023-10-16", "")
	require.NotNil(t, translator)

	respBody := `{"input_tokens": 123}`
	_, _, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(respBody), false, nil)
	require.NoError(t, err)

	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(123), inputTokens)
}

func TestCountTokensToGCPAnthropic_ResponseError(t *testing.T) {
	translator := NewCountTokensToGCPAnthropicTranslator("vertex-2023-10-16", "")
	require.NotNil(t, translator)

	hdrs, body, err := translator.ResponseError(nil, nil)
	require.NoError(t, err)
	require.Nil(t, hdrs)
	require.Nil(t, body)
}
