// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestAnthropicToAWSAnthropicTranslator_RequestBody_ModelNameOverride(t *testing.T) {
	tests := []struct {
		name           string
		override       string
		inputModel     string
		expectedModel  string
		expectedInPath string
	}{
		{
			name:           "no override uses original model",
			override:       "",
			inputModel:     "anthropic.claude-3-haiku-20240307-v1:0",
			expectedModel:  "anthropic.claude-3-haiku-20240307-v1:0",
			expectedInPath: "anthropic.claude-3-haiku-20240307-v1:0",
		},
		{
			name:           "override replaces model in body and path",
			override:       "anthropic.claude-3-sonnet-20240229-v1:0",
			inputModel:     "anthropic.claude-3-haiku-20240307-v1:0",
			expectedModel:  "anthropic.claude-3-sonnet-20240229-v1:0",
			expectedInPath: "anthropic.claude-3-sonnet-20240229-v1:0",
		},
		{
			name:           "override with empty input model",
			override:       "anthropic.claude-3-opus-20240229-v1:0",
			inputModel:     "",
			expectedModel:  "anthropic.claude-3-opus-20240229-v1:0",
			expectedInPath: "anthropic.claude-3-opus-20240229-v1:0",
		},
		{
			name:           "model with ARN format",
			override:       "",
			inputModel:     "arn:aws:bedrock:eu-central-1:000000000:application-inference-profile/aaaaaaaaa",
			expectedModel:  "arn:aws:bedrock:eu-central-1:000000000:application-inference-profile/aaaaaaaaa",
			expectedInPath: "arn:aws:bedrock:eu-central-1:000000000:application-inference-profile%2Faaaaaaaaa",
		},
		{
			name:           "global model ID",
			override:       "",
			inputModel:     "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			expectedModel:  "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			expectedInPath: "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", tt.override)

			// Create the request using map structure.
			originalReq := &anthropicschema.MessagesRequest{
				Model: tt.inputModel,
				Messages: []anthropicschema.MessageParam{
					{
						Role:    anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{Text: "Hello"},
					},
				},
			}

			rawBody, err := json.Marshal(originalReq)
			require.NoError(t, err)

			headerMutation, bodyMutation, err := translator.RequestBody(rawBody, originalReq, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)
			require.NotNil(t, bodyMutation)

			// Check path header contains expected model (URL encoded).
			// Use the last element as it takes precedence when multiple headers are set.
			pathHeader := headerMutation[len(headerMutation)-2]
			require.Equal(t, pathHeaderName, pathHeader.Key())
			expectedPath := "/model/" + tt.expectedInPath + "/invoke"
			assert.Equal(t, expectedPath, pathHeader.Value())

			// Check that model field is removed from body (since it's in the path).
			var modifiedReq map[string]any
			err = json.Unmarshal(bodyMutation, &modifiedReq)
			require.NoError(t, err)
			_, hasModel := modifiedReq["model"]
			assert.False(t, hasModel, "model field should be removed from request body")

			// Verify anthropic_version field is added (required by AWS Bedrock).
			version, hasVersion := modifiedReq["anthropic_version"]
			assert.True(t, hasVersion, "anthropic_version should be added for AWS Bedrock")
			assert.Equal(t, "bedrock-2023-05-31", version, "anthropic_version should match the configured version")
		})
	}
}

func TestAnthropicToAWSAnthropicTranslator_RequestBody_StreamingPaths(t *testing.T) {
	tests := []struct {
		name               string
		stream             any
		expectedPathSuffix string
	}{
		{
			name:               "non-streaming uses /invoke",
			stream:             false,
			expectedPathSuffix: "/invoke",
		},
		{
			name:               "streaming uses /invoke-with-response-stream",
			stream:             true,
			expectedPathSuffix: "/invoke-with-response-stream",
		},
		{
			name:               "missing stream defaults to /invoke",
			stream:             nil,
			expectedPathSuffix: "/invoke",
		},
		{
			name:               "non-boolean stream defaults to /invoke",
			stream:             "true",
			expectedPathSuffix: "/invoke",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", "")

			parsedReq := &anthropicschema.MessagesRequest{
				Model: "anthropic.claude-3-sonnet-20240229-v1:0",
				Messages: []anthropicschema.MessageParam{
					{
						Role: anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{
							Array: []anthropicschema.ContentBlockParam{
								{Text: &anthropicschema.TextBlockParam{Text: "Hello"}},
							},
						},
					},
				},
			}
			if tt.stream != nil {
				if streamVal, ok := tt.stream.(bool); ok {
					parsedReq.Stream = streamVal
				}
			}

			rawBody, err := json.Marshal(parsedReq)
			require.NoError(t, err)

			headerMutation, _, err := translator.RequestBody(rawBody, parsedReq, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)

			// Check path contains expected suffix.
			// Use the last element as it takes precedence when multiple headers are set.
			pathHeader := headerMutation[len(headerMutation)-2]
			expectedPath := "/model/anthropic.claude-3-sonnet-20240229-v1:0" + tt.expectedPathSuffix
			assert.Equal(t, expectedPath, pathHeader.Value())
		})
	}
}

func TestAnthropicToAWSAnthropicTranslator_URLEncoding(t *testing.T) {
	tests := []struct {
		name         string
		modelID      string
		expectedPath string
	}{
		{
			name:         "simple model ID with colon",
			modelID:      "anthropic.claude-3-sonnet-20240229-v1:0",
			expectedPath: "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke",
		},
		{
			name:         "full ARN with multiple special characters",
			modelID:      "arn:aws:bedrock:us-east-1:123456789012:foundation-model/anthropic.claude-3-sonnet-20240229-v1:0",
			expectedPath: "/model/arn:aws:bedrock:us-east-1:123456789012:foundation-model%2Fanthropic.claude-3-sonnet-20240229-v1:0/invoke",
		},
		{
			name:         "global model prefix",
			modelID:      "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			expectedPath: "/model/global.anthropic.claude-sonnet-4-5-20250929-v1:0/invoke",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", "")

			originalReq := &anthropicschema.MessagesRequest{
				Model: tt.modelID,
				Messages: []anthropicschema.MessageParam{
					{
						Role: anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{
							Array: []anthropicschema.ContentBlockParam{
								{Text: &anthropicschema.TextBlockParam{Text: "Hello"}},
							},
						},
					},
				},
			}

			rawBody, err := json.Marshal(originalReq)
			require.NoError(t, err)

			headerMutation, _, err := translator.RequestBody(rawBody, originalReq, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)

			// Use the last element as it takes precedence when multiple headers are set.
			pathHeader := headerMutation[len(headerMutation)-2]
			assert.Equal(t, pathHeaderName, pathHeader.Key())
			assert.Equal(t, tt.expectedPath, pathHeader.Value())
		})
	}
}

func TestAnthropicToAWSAnthropicTranslator_RequestBody_AnthropicBetaHeader(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected []string
	}{
		{
			name:     "single beta header",
			headers:  map[string]string{"anthropic-beta": "context-1m-2025-08-07"},
			expected: []string{"context-1m-2025-08-07"},
		},
		{
			name:     "multiple beta header values",
			headers:  map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			expected: []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"},
		},
		{
			name:     "trims whitespace",
			headers:  map[string]string{"anthropic-beta": " interleaved-thinking-2025-05-14 , context-1m-2025-08-07 "},
			expected: []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"},
		},
		{
			name:     "all five confirmed Bedrock flags",
			headers:  map[string]string{"anthropic-beta": "context-1m-2025-08-07,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14,token-efficient-tools-2025-02-19,tool-search-tool-2025-10-19"},
			expected: []string{"context-1m-2025-08-07", "interleaved-thinking-2025-05-14", "fine-grained-tool-streaming-2025-05-14", "token-efficient-tools-2025-02-19", "tool-search-tool-2025-10-19"},
		},
		{
			name:     "unsupported flags are dropped",
			headers:  map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,prompt-caching-2024-07-31,tool-search-tool-2025-10-19"},
			expected: []string{"interleaved-thinking-2025-05-14", "tool-search-tool-2025-10-19"},
		},
		{
			name:     "Anthropic API umbrella flag is rewritten to the Bedrock tool search flag",
			headers:  map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20"},
			expected: []string{"tool-search-tool-2025-10-19"},
		},
		{
			name:     "umbrella flag deduplicated with the explicitly sent Bedrock flag",
			headers:  map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,tool-search-tool-2025-10-19"},
			expected: []string{"tool-search-tool-2025-10-19"},
		},
		{
			name:     "umbrella flag combined with other supported flags",
			headers:  map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,advanced-tool-use-2025-11-20"},
			expected: []string{"interleaved-thinking-2025-05-14", "tool-search-tool-2025-10-19"},
		},
		{
			name:     "umbrella flag with unsupported siblings only forwards the mapped flag",
			headers:  map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,code-execution-2025-08-25,files-api-2025-04-14"},
			expected: []string{"tool-search-tool-2025-10-19"},
		},
		{
			name:    "only unsupported flags results in no anthropic_beta",
			headers: map[string]string{"anthropic-beta": "prompt-caching-2024-07-31,memory-2025-08-18"},
		},
		{
			name:    "no beta header",
			headers: map[string]string{},
		},
		{
			name:    "empty beta header",
			headers: map[string]string{"anthropic-beta": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", "")
			headerSetter, ok := translator.(RequestHeadersSetter)
			require.True(t, ok)
			headerSetter.SetRequestHeaders(tt.headers)

			originalReq := &anthropicschema.MessagesRequest{
				Model: "anthropic.claude-3-sonnet-20240229-v1:0",
				Messages: []anthropicschema.MessageParam{
					{
						Role: anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{
							Array: []anthropicschema.ContentBlockParam{
								{Text: &anthropicschema.TextBlockParam{Text: "Hello"}},
							},
						},
					},
				},
			}

			rawBody, err := json.Marshal(originalReq)
			require.NoError(t, err)

			_, bodyMutation, err := translator.RequestBody(rawBody, originalReq, false)
			require.NoError(t, err)

			var modifiedReq map[string]any
			err = json.Unmarshal(bodyMutation, &modifiedReq)
			require.NoError(t, err)

			betaRaw, hasBeta := modifiedReq["anthropic_beta"]
			if len(tt.expected) == 0 {
				require.False(t, hasBeta)
				return
			}

			require.True(t, hasBeta)
			betaValues, ok := betaRaw.([]any)
			require.True(t, ok)
			require.Len(t, betaValues, len(tt.expected))
			for i, expected := range tt.expected {
				require.Equal(t, expected, betaValues[i])
			}
		})
	}
}

func TestAnthropicToAWSAnthropicTranslator_RequestBody_HeaderValueFilter(t *testing.T) {
	tests := []struct {
		name             string
		requestHeaders   map[string]string
		filterHeader     string
		filterMode       string
		filterValues     []string
		wantBody         []string
		wantBetaHeader   string
		wantHeaderChange bool
	}{
		// NOTE: SetRequestHeaders already applies awsBedrockSupportedAnthropicBetas (and the
		// Anthropic-API-name aliases) before any of this runs, so these cases deliberately use
		// values that survive that allowlist. Using a non-allowlisted value here would pass
		// vacuously — the value would already be gone before the operator filter saw it.
		{
			name:           "no filter configured forwards all allowlisted betas unchanged",
			requestHeaders: map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			wantBody:       []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"},
		},
		{
			// Bedrock reads anthropic_beta from the body, so the filtered set has to land there —
			// filtering only the header would leave the rejected value in the request.
			name:             "denylist drops the configured value from body and header",
			requestHeaders:   map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			filterHeader:     "anthropic-beta",
			filterMode:       "Denylist",
			filterValues:     []string{"context-1m-2025-08-07"},
			wantBody:         []string{"interleaved-thinking-2025-05-14"},
			wantBetaHeader:   "interleaved-thinking-2025-05-14",
			wantHeaderChange: true,
		},
		{
			name:             "allowlist keeps only the sanctioned value",
			requestHeaders:   map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			filterHeader:     "anthropic-beta",
			filterMode:       "Allowlist",
			filterValues:     []string{"interleaved-thinking-2025-05-14"},
			wantBody:         []string{"interleaved-thinking-2025-05-14"},
			wantBetaHeader:   "interleaved-thinking-2025-05-14",
			wantHeaderChange: true,
		},
		{
			// The setter is called for every configured filter, so a filter on an unrelated header
			// must not reach the body's anthropic_beta field.
			name:           "filter on a different header is ignored",
			requestHeaders: map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			filterHeader:   "x-some-other-header",
			filterMode:     "Denylist",
			filterValues:   []string{"context-1m-2025-08-07"},
			wantBody:       []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"},
		},
		{
			name:             "filter header name matching is case-insensitive",
			requestHeaders:   map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			filterHeader:     "Anthropic-Beta",
			filterMode:       "Denylist",
			filterValues:     []string{"context-1m-2025-08-07"},
			wantBody:         []string{"interleaved-thinking-2025-05-14"},
			wantBetaHeader:   "interleaved-thinking-2025-05-14",
			wantHeaderChange: true,
		},
		{
			// The built-in allowlist runs first, so a value it strips never reaches the operator
			// filter: denying an already-stripped value is a no-op, not a second drop.
			name:           "value already stripped by the built-in allowlist never reaches the filter",
			requestHeaders: map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13"},
			filterHeader:   "anthropic-beta",
			filterMode:     "Denylist",
			filterValues:   []string{"thinking-token-count-2026-05-13"},
			wantBody:       []string{"interleaved-thinking-2025-05-14"},
		},
		{
			// advanced-tool-use-2025-11-20 is aliased to tool-search-tool-2025-10-19 before the
			// filter runs, so an operator filter has to name the Bedrock-side value.
			name:             "filter matches the post-alias Bedrock value",
			requestHeaders:   map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,context-1m-2025-08-07"},
			filterHeader:     "anthropic-beta",
			filterMode:       "Denylist",
			filterValues:     []string{"tool-search-tool-2025-10-19"},
			wantBody:         []string{"context-1m-2025-08-07"},
			wantBetaHeader:   "context-1m-2025-08-07",
			wantHeaderChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", "")
			translator.(RequestHeadersSetter).SetRequestHeaders(tt.requestHeaders)
			if tt.filterHeader != "" {
				translator.(HeaderValueFilterSetter).SetHeaderValueFilter(tt.filterHeader, tt.filterMode, tt.filterValues)
			}

			originalReq := &anthropicschema.MessagesRequest{
				Model: "anthropic.claude-3-sonnet-20240229-v1:0",
				Messages: []anthropicschema.MessageParam{
					{
						Role: anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{
							Array: []anthropicschema.ContentBlockParam{
								{Text: &anthropicschema.TextBlockParam{Text: "Hello"}},
							},
						},
					},
				},
			}
			rawBody, err := json.Marshal(originalReq)
			require.NoError(t, err)

			headerMutation, bodyMutation, err := translator.RequestBody(rawBody, originalReq, false)
			require.NoError(t, err)

			var modifiedReq map[string]any
			require.NoError(t, json.Unmarshal(bodyMutation, &modifiedReq))
			betaValues, ok := modifiedReq["anthropic_beta"].([]any)
			require.True(t, ok)
			require.Len(t, betaValues, len(tt.wantBody))
			for i, expected := range tt.wantBody {
				require.Equal(t, expected, betaValues[i])
			}

			var betaHeader *internalapi.Header
			for i := range headerMutation {
				if headerMutation[i].Key() == anthropicBetaHeaderName {
					betaHeader = &headerMutation[i]
				}
			}
			if tt.wantHeaderChange {
				require.NotNil(t, betaHeader, "expected anthropic-beta header to be overwritten")
				assert.Equal(t, tt.wantBetaHeader, betaHeader.Value())
			} else {
				require.Nil(t, betaHeader, "anthropic-beta header should not be overwritten")
			}
		})
	}
}

func TestAnthropicToAWSAnthropicTranslator_ResponseBody(t *testing.T) {
	t.Run("non-streaming response", func(t *testing.T) {
		// This is mostly for the coverage as it's the same as AnthropicToAnthropicTranslator.ResponseBody.
		translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", "")
		_, _, _, _, err := translator.ResponseBody(nil, strings.NewReader(``), false, nil)
		require.ErrorIs(t, err, io.EOF)
	})

	// Base64 encoded AWS Bedrock Anthropic event stream chunks extracted from a real streaming response.
	awsBase64Chunks := []string{
		"AAACiAAAAEtIch0pCzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaWJXVnpjMkZuWlY5emRHRnlkQ0lzSW0xbGMzTmhaMlVpT25zaWJXOWtaV3dpT2lKamJHRjFaR1V0YzI5dWJtVjBMVFF0TlMweU1ESTFNRGt5T1NJc0ltbGtJam9pYlhOblgySmtjbXRmTURFeVIwSlFlbkJqYjAxRFRGQXhZakp3WTBwelUwaHJJaXdpZEhsd1pTSTZJbTFsYzNOaFoyVWlMQ0p5YjJ4bElqb2lZWE56YVhOMFlXNTBJaXdpWTI5dWRHVnVkQ0k2VzEwc0luTjBiM0JmY21WaGMyOXVJanB1ZFd4c0xDSnpkRzl3WDNObGNYVmxibU5sSWpwdWRXeHNMQ0oxYzJGblpTSTZleUpwYm5CMWRGOTBiMnRsYm5NaU9qRXdMQ0pqWVdOb1pWOWpjbVZoZEdsdmJsOXBibkIxZEY5MGIydGxibk1pT2pBc0ltTmhZMmhsWDNKbFlXUmZhVzV3ZFhSZmRHOXJaVzV6SWpvd0xDSmpZV05vWlY5amNtVmhkR2x2YmlJNmV5SmxjR2hsYldWeVlXeGZOVzFmYVc1d2RYUmZkRzlyWlc1eklqb3dMQ0psY0dobGJXVnlZV3hmTVdoZmFXNXdkWFJmZEc5clpXNXpJam93ZlN3aWIzVjBjSFYwWDNSdmEyVnVjeUk2TVgxOWZRPT0iLCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyIn1MDhroAAAA9wAAAEt+OMs8CzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaVkyOXVkR1Z1ZEY5aWJHOWphMTl6ZEdGeWRDSXNJbWx1WkdWNElqb3dMQ0pqYjI1MFpXNTBYMkpzYjJOcklqcDdJblI1Y0dVaU9pSjBaWGgwSWl3aWRHVjRkQ0k2SWlKOWZRPT0iLCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eSJ9dAbelAAAAP4AAABLcyipTQs6ZXZlbnQtdHlwZQcABWNodW5rDTpjb250ZW50LXR5cGUHABBhcHBsaWNhdGlvbi9qc29uDTptZXNzYWdlLXR5cGUHAAVldmVudHsiYnl0ZXMiOiJleUowZVhCbElqb2lZMjl1ZEdWdWRGOWliRzlqYTE5a1pXeDBZU0lzSW1sdVpHVjRJam93TENKa1pXeDBZU0k2ZXlKMGVYQmxJam9pZEdWNGRGOWtaV3gwWVNJc0luUmxlSFFpT2lKSWFTSjlmUT09IiwicCI6ImFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6QUJDREVGIn2TCmMkAAAA9gAAAEtDWOKMCzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaVkyOXVkR1Z1ZEY5aWJHOWphMTlrWld4MFlTSXNJbWx1WkdWNElqb3dMQ0prWld4MFlTSTZleUowZVhCbElqb2lkR1Y0ZEY5a1pXeDBZU0lzSW5SbGVIUWlPaUloSW4xOSIsInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCIn3Koa0d",
		"AAAA/wAAAEtOSID9CzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaVkyOXVkR1Z1ZEY5aWJHOWphMTlrWld4MFlTSXNJbWx1WkdWNElqb3dMQ0prWld4MFlTSTZleUowZVhCbElqb2lkR1Y0ZEY5a1pXeDBZU0lzSW5SbGVIUWlPaUlnSW4xOSIsInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkdISUpLIn1jPsG/",
		"AAABBwAAAEv9UEjECzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaVkyOXVkR1Z1ZEY5aWJHOWphMTlrWld4MFlTSXNJbWx1WkdWNElqb3dMQ0prWld4MFlTSTZleUowZVhCbElqb2lkR1Y0ZEY5a1pXeDBZU0lzSW5SbGVIUWlPaUx3bjVHTElFaHZkeUo5ZlE9PSIsInAiOiJhYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ekFCQ0RFRkcifVMy/k4=",
		"AAABLwAAAEsM4SwBCzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaVkyOXVkR1Z1ZEY5aWJHOWphMTlrWld4MFlTSXNJbWx1WkdWNElqb3dMQ0prWld4MFlTSTZleUowZVhCbElqb2lkR1Y0ZEY5a1pXeDBZU0lzSW5SbGVIUWlPaUlnWVhKbElIbHZkU0JrYjJsdVp5QjBiMlJoZVQ4aWZYMD0iLCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWjAxMjM0In0vsjpT",
		"AAAAvAAAAEtRG6JkCzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaVkyOXVkR1Z1ZEY5aWJHOWphMTl6ZEc5d0lpd2lhVzVrWlhnaU9qQjkiLCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoifQSu+6I=",
		"AAABFwAAAEudsN9GCzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaWJXVnpjMkZuWlY5a1pXeDBZU0lzSW1SbGJIUmhJanA3SW5OMGIzQmZjbVZoYzI5dUlqb2laVzVrWDNSMWNtNGlMQ0p6ZEc5d1gzTmxjWFZsYm1ObElqcHVkV3hzZlN3aWRYTmhaMlVpT25zaWIzVjBjSFYwWDNSdmEyVnVjeUk2TVRWOWZRPT0iLCJwIjoiYWJjZGVmZ2hpamtsbW5vcHFyc3R1In1ijQxK",
		"AAABPAAAAEsrocFTCzpldmVudC10eXBlBwAFY2h1bmsNOmNvbnRlbnQtdHlwZQcAEGFwcGxpY2F0aW9uL2pzb24NOm1lc3NhZ2UtdHlwZQcABWV2ZW50eyJieXRlcyI6ImV5SjBlWEJsSWpvaWJXVnpjMkZuWlY5emRHOXdJaXdpWVcxaGVtOXVMV0psWkhKdlkyc3RhVzUyYjJOaGRHbHZiazFsZEhKcFkzTWlPbnNpYVc1d2RYUlViMnRsYmtOdmRXNTBJam94TUN3aWIzVjBjSFYwVkc5clpXNURiM1Z1ZENJNk1UVXNJbWx1ZG05allYUnBiMjVNWVhSbGJtTjVJam94TnprNExDSm1hWEp6ZEVKNWRHVk1ZWFJsYm1ONUlqb3hOVEEzZlgwPSIsInAiOiJhYiJ9OOM6wQ==",
	}

	var chunkBytes []byte
	for _, b64Chunk := range awsBase64Chunks {
		chunk, err := base64.StdEncoding.DecodeString(b64Chunk)
		require.NoError(t, err)
		chunkBytes = append(chunkBytes, chunk...)
	}

	translator := NewAnthropicToAWSAnthropicTranslator("bedrock-2023-05-31", "")
	translator.(*anthropicToAWSAnthropicTranslator).stream = true
	var results []byte
	var tokenUsage metrics.TokenUsage
	for i := range chunkBytes {
		var hm []internalapi.Header
		var newBody []byte
		var err error
		hm, newBody, tokenUsage, _, err = translator.ResponseBody(nil, bytes.NewBuffer([]byte{chunkBytes[i]}), i == len(chunkBytes)-1, nil)
		require.NoError(t, err)
		require.Nil(t, hm)
		if len(newBody) > 0 {
			results = append(results, newBody...)
		}
	}
	inputToken, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	outputToken, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(10), inputToken)
	assert.Equal(t, uint32(15), outputToken)
	require.Equal(t, `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_bdrk_012GBPzpcoMCLP1b2pcJsSHk","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"👋 How"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" are you doing today?"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":15}}

event: message_stop
data: {"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":10,"outputTokenCount":15,"invocationLatency":1798,"firstByteLatency":1507}}

`, string(results))
}
