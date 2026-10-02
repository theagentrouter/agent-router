// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestCountTokensToAWSAnthropic_RequestBody(t *testing.T) {
	tests := []struct {
		name     string
		override string
		model    string
		expPath  string
	}{
		{
			name:    "no override uses original model",
			model:   "anthropic.claude-3-5-sonnet-20241022-v2:0",
			expPath: "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/count-tokens",
		},
		{
			name:     "with override",
			override: "anthropic.claude-3-haiku-20240307-v1:0",
			model:    "claude-opus-4-6",
			expPath:  "/model/anthropic.claude-3-haiku-20240307-v1:0/count-tokens",
		},
		{
			name:     "strips CRIS us. prefix from override",
			override: "us.anthropic.claude-sonnet-4-6",
			model:    "claude-opus-4-6",
			expPath:  "/model/anthropic.claude-sonnet-4-6/count-tokens",
		},
		{
			name:     "strips CRIS eu. prefix from override",
			override: "eu.anthropic.claude-sonnet-4-6",
			model:    "claude-opus-4-6",
			expPath:  "/model/anthropic.claude-sonnet-4-6/count-tokens",
		},
		{
			name:     "strips CRIS apac. prefix from override",
			override: "apac.anthropic.claude-sonnet-4-6",
			model:    "claude-opus-4-6",
			expPath:  "/model/anthropic.claude-sonnet-4-6/count-tokens",
		},
		{
			name:     "strips CRIS us-gov. prefix from override",
			override: "us-gov.anthropic.claude-sonnet-4-6",
			model:    "claude-opus-4-6",
			expPath:  "/model/anthropic.claude-sonnet-4-6/count-tokens",
		},
		{
			name:    "strips CRIS apac. prefix from request model",
			model:   "apac.anthropic.claude-sonnet-4-6",
			expPath: "/model/anthropic.claude-sonnet-4-6/count-tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewCountTokensToAWSAnthropicTranslator("bedrock-2023-05-31", tt.override)
			require.NotNil(t, translator)

			raw := []byte(`{"model":"` + tt.model + `","messages":[{"role":"user","content":"hello"}]}`)
			req := &anthropicschema.CountTokensRequest{Model: tt.model}

			headerMutation, bodyMutation, err := translator.RequestBody(raw, req, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)
			require.NotNil(t, bodyMutation)

			// Check path header uses /count-tokens.
			pathHeader := headerMutation[0]
			require.Equal(t, pathHeaderName, pathHeader.Key())
			assert.Equal(t, tt.expPath, pathHeader.Value())

			// Verify body is wrapped in Bedrock CountTokens format.
			var parsed map[string]any
			require.NoError(t, json.Unmarshal(bodyMutation, &parsed))

			// Should have input.invokeModel.body structure.
			input, ok := parsed["input"].(map[string]any)
			require.True(t, ok, "expected input field")
			invokeModel, ok := input["invokeModel"].(map[string]any)
			require.True(t, ok, "expected invokeModel field")
			bodyB64, ok := invokeModel["body"].(string)
			require.True(t, ok, "expected body field as string")

			// Decode the base64 body and verify it has anthropic_version but no model.
			decoded, err := base64.StdEncoding.DecodeString(bodyB64)
			require.NoError(t, err)
			var innerBody map[string]any
			require.NoError(t, json.Unmarshal(decoded, &innerBody))
			assert.Equal(t, "bedrock-2023-05-31", innerBody["anthropic_version"])
			assert.NotContains(t, innerBody, "model")
		})
	}
}

func TestCountTokensToAWSAnthropic_ResponseBody(t *testing.T) {
	translator := NewCountTokensToAWSAnthropicTranslator("bedrock-2023-05-31", "")
	require.NotNil(t, translator)

	// Bedrock returns camelCase {"inputTokens": N}.
	respBody := `{"inputTokens": 99}`
	hdrs, body, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(respBody), false, nil)
	require.NoError(t, err)

	// Check token usage metrics.
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(99), inputTokens)

	// Check response body is converted to Anthropic format.
	require.NotNil(t, body)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, float64(99), parsed["input_tokens"])

	// Check content-length header is set.
	require.NotNil(t, hdrs)
	assert.Equal(t, contentLengthHeaderName, hdrs[0].Key())
}

func TestCountTokensToAWSAnthropic_ResponseError(t *testing.T) {
	translator := NewCountTokensToAWSAnthropicTranslator("bedrock-2023-05-31", "")
	require.NotNil(t, translator)

	t.Run("AWS structured error response", func(t *testing.T) {
		// Real-world repro: Bedrock's CountTokens API rejects some Anthropic models.
		respHeaders := map[string]string{
			statusHeaderName:       "400",
			contentTypeHeaderName:  "application/json",
			awsErrorTypeHeaderName: "ValidationException",
		}
		errorBody := `{"message": "The provided model doesn't support counting tokens"}`

		headers, body, err := translator.ResponseError(respHeaders, strings.NewReader(errorBody))
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Len(t, headers, 2)
		assert.Equal(t, contentTypeHeaderName, headers[0].Key())
		assert.Equal(t, jsonContentType, headers[0].Value()) //nolint:testifylint // comparing header value, not JSON
		assert.Equal(t, contentLengthHeaderName, headers[1].Key())
		assert.Equal(t, strconv.Itoa(len(body)), headers[1].Value())

		var errResp anthropicschema.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Equal(t, "error", errResp.Type)
		assert.Equal(t, "invalid_request_error", errResp.Error.Type)
		assert.Equal(t, "The provided model doesn't support counting tokens", errResp.Error.Message)
	})

	t.Run("non-JSON error body", func(t *testing.T) {
		headers, body, err := translator.ResponseError(
			map[string]string{
				statusHeaderName:      "503",
				contentTypeHeaderName: "text/plain",
			},
			strings.NewReader("Service Unavailable"),
		)
		require.NoError(t, err)
		require.NotNil(t, headers)
		require.Len(t, headers, 2)
		assert.Equal(t, contentLengthHeaderName, headers[1].Key())
		assert.Equal(t, strconv.Itoa(len(body)), headers[1].Value())

		var errResp anthropicschema.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		assert.Equal(t, "error", errResp.Type)
		assert.Equal(t, "service_unavailable_error", errResp.Error.Type)
		assert.Equal(t, "Service Unavailable", errResp.Error.Message)
	})

	t.Run("malformed JSON error body", func(t *testing.T) {
		_, _, err := translator.ResponseError(
			map[string]string{
				statusHeaderName:      "400",
				contentTypeHeaderName: "application/json",
			},
			strings.NewReader(`{"message": not valid json`),
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal error body")
	})

	t.Run("status code mappings", func(t *testing.T) {
		tests := []struct {
			status       string
			expectedType string
		}{
			{"400", "invalid_request_error"},
			{"401", "authentication_error"},
			{"403", "permission_error"},
			{"404", "not_found_error"},
			{"429", "rate_limit_error"},
			{"500", "internal_server_error"},
			{"503", "service_unavailable_error"},
			{"502", "internal_server_error"}, // default
		}
		for _, tt := range tests {
			t.Run(tt.status, func(t *testing.T) {
				_, body, err := translator.ResponseError(
					map[string]string{
						statusHeaderName:      tt.status,
						contentTypeHeaderName: "text/plain",
					},
					strings.NewReader("error"),
				)
				require.NoError(t, err)
				var errResp anthropicschema.ErrorResponse
				require.NoError(t, json.Unmarshal(body, &errResp))
				assert.Equal(t, tt.expectedType, errResp.Error.Type)
			})
		}
	})

	t.Run("read error", func(t *testing.T) {
		_, _, err := translator.ResponseError(
			map[string]string{
				statusHeaderName:      "500",
				contentTypeHeaderName: "text/plain",
			},
			&errorReader{},
		)
		require.Error(t, err)
	})
}
