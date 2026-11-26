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
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestCountTokensToAnthropic_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name              string
		original          []byte
		body              anthropicschema.CountTokensRequest
		forceBodyMutation bool
		modelNameOverride string
		prefix            string
		originalPath      string
		expNewBody        []byte
		expPath           string
	}{
		{
			name:     "no mutation",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			prefix:     "v1",
			expNewBody: nil,
			expPath:    "/v1/messages/count_tokens",
		},
		{
			name:     "model override",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			modelNameOverride: "claude-sonnet-4-20250514",
			prefix:            "v1",
			expPath:           "/v1/messages/count_tokens",
		},
		{
			name:     "force body mutation",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			forceBodyMutation: true,
			prefix:            "v1",
			expPath:           "/v1/messages/count_tokens",
		},
		{
			name:     "empty prefix",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			prefix:  "",
			expPath: "/messages/count_tokens",
		},
		{
			name:     "custom prefix",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			prefix:  "gateway/v1",
			expPath: "/gateway/v1/messages/count_tokens",
		},
		{
			name:     "query string preserved",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			prefix:       "v1",
			originalPath: "/anthropic/v1/messages/count_tokens?beta=true",
			expPath:      "/v1/messages/count_tokens?beta=true",
		},
		{
			name:     "query string preserved with custom prefix and model override",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			modelNameOverride: "claude-sonnet-4-20250514",
			prefix:            "gateway/v1",
			originalPath:      "/anthropic/v1/messages/count_tokens?beta=true&foo=bar",
			expPath:           "/gateway/v1/messages/count_tokens?beta=true&foo=bar",
		},
		{
			name:     "original path without query",
			original: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`),
			body: anthropicschema.CountTokensRequest{
				Model: "claude-opus-4-6",
			},
			prefix:       "v1",
			originalPath: "/anthropic/v1/messages/count_tokens",
			expPath:      "/v1/messages/count_tokens",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewCountTokensToAnthropicTranslator(tc.prefix, tc.modelNameOverride)
			require.NotNil(t, translator)
			if tc.originalPath != "" {
				translator.(RequestHeadersSetter).SetRequestHeaders(map[string]string{
					internalapi.OriginalPathHeader: tc.originalPath,
				})
			}

			headerMutation, bodyMutation, err := translator.RequestBody(tc.original, &tc.body, tc.forceBodyMutation)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)

			pathHeader := headerMutation[0]
			require.Equal(t, pathHeaderName, pathHeader.Key())
			assert.Equal(t, tc.expPath, pathHeader.Value())

			if tc.modelNameOverride != "" {
				require.NotNil(t, bodyMutation)
				// Verify the model was overridden in the body.
				var parsed map[string]any
				require.NoError(t, json.Unmarshal(bodyMutation, &parsed))
				assert.Equal(t, tc.modelNameOverride, parsed["model"])
			}

			if tc.forceBodyMutation && tc.modelNameOverride == "" {
				// When force body mutation is set but no model override, the original body is returned.
				require.Equal(t, tc.original, bodyMutation)
			}
		})
	}
}

func TestCountTokensToAnthropic_ResponseBody(t *testing.T) {
	translator := NewCountTokensToAnthropicTranslator("v1", "")
	require.NotNil(t, translator)

	respBody := `{"input_tokens": 42}`
	_, _, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(respBody), false, nil)
	require.NoError(t, err)

	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(42), inputTokens)
}

func TestCountTokensToAnthropic_ResponseError(t *testing.T) {
	translator := NewCountTokensToAnthropicTranslator("v1", "")
	require.NotNil(t, translator)

	// Passthrough — no error translation.
	hdrs, body, err := translator.ResponseError(nil, nil)
	require.NoError(t, err)
	require.Nil(t, hdrs)
	require.Nil(t, body)
}
