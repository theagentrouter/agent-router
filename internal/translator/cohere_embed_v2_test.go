// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"

	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestCohereToCohereTranslatorV2Embed_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name              string
		modelNameOverride string
		onRetry           bool
		expPath           string
		expBodyContains   string
	}{
		{
			name:            "valid_body",
			expPath:         "/v2/embed",
			expBodyContains: "",
		},
		{
			name:              "model_name_override",
			modelNameOverride: "embed-v4.0",
			expPath:           "/v2/embed",
			expBodyContains:   `"model":"embed-v4.0"`,
		},
		{
			name:    "on_retry_no_change",
			onRetry: true,
			expPath: "/v2/embed",
		},
		{
			name:              "model_name_override_with_retry",
			modelNameOverride: "embed-v4.0",
			onRetry:           true,
			expPath:           "/v2/embed",
			expBodyContains:   `"model":"embed-v4.0"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbedCohereToCohereTranslator("v2", tc.modelNameOverride)
			originalBody := `{"model":"embed-v4.0","input_type":"classification","texts":["text1","text2"]}`
			var req cohereschema.EmbedV2Request
			require.NoError(t, json.Unmarshal([]byte(originalBody), &req))

			headerMutation, bodyMutation, err := translator.RequestBody([]byte(originalBody), &req, tc.onRetry)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)
			require.GreaterOrEqual(t, len(headerMutation), 1)
			require.Equal(t, pathHeaderName, headerMutation[0].Key())
			require.Equal(t, tc.expPath, headerMutation[0].Value())

			switch {
			case tc.expBodyContains != "":
				require.NotNil(t, bodyMutation)
				require.Contains(t, string(bodyMutation), tc.expBodyContains)
				// Verify content-length header is set.
				require.Len(t, headerMutation, 2)
				require.Equal(t, contentLengthHeaderName, headerMutation[1].Key())
			case bodyMutation != nil:
				// If there's a body mutation (like on retry), content-length header should be set.
				require.Len(t, headerMutation, 2)
				require.Equal(t, contentLengthHeaderName, headerMutation[1].Key())
			default:
				// No body mutation, only path header.
				require.Len(t, headerMutation, 1)
			}
		})
	}
}

func TestCohereToCohereTranslatorV2Embed_RequestBody_InvalidJSONCreatesBodyWithOverride(t *testing.T) {
	translator := NewEmbedCohereToCohereTranslator("v2", "override-model")
	// Provide invalid JSON; sjson with Optimistic mode can still produce a body with the override.
	originalBody := []byte("not-json")
	var req cohereschema.EmbedV2Request
	headerMutation, bodyMutation, err := translator.RequestBody(originalBody, &req, false)
	require.NoError(t, err)
	require.NotNil(t, headerMutation)
	require.NotNil(t, bodyMutation)
	// Body should contain the override model
	require.Contains(t, string(bodyMutation), `"model":"override-model"`)
	// Verify content-length header is set alongside :path
	require.GreaterOrEqual(t, len(headerMutation), 2)
	require.Equal(t, pathHeaderName, headerMutation[0].Key())
	require.Equal(t, "/v2/embed", headerMutation[0].Value())
	require.Equal(t, contentLengthHeaderName, headerMutation[1].Key())
}

func TestCohereToCohereTranslatorV2Embed_RequestBody_SetModelNameError(t *testing.T) {
	orig := sjsonOptions
	sjsonOptions = &sjson.Options{Optimistic: false, ReplaceInPlace: false}
	t.Cleanup(func() { sjsonOptions = orig })

	translator := NewEmbedCohereToCohereTranslator("v2", "override-model")
	// Use an array root to make setting an object key fail with Optimistic=false.
	originalBody := []byte("[]")
	var req cohereschema.EmbedV2Request

	headerMutation, bodyMutation, err := translator.RequestBody(originalBody, &req, false)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to set model name")
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
}

func TestCohereToCohereTranslatorV2Embed_ResponseHeaders(t *testing.T) {
	translator := NewEmbedCohereToCohereTranslator("v2", "")
	headerMutation, err := translator.ResponseHeaders(map[string]string{})
	require.NoError(t, err)
	require.Nil(t, headerMutation)
}

func TestCohereToCohereTranslatorV2Embed_ResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name           string
		responseBody   string
		expectedInput  int32
		expectedOutput int32
		expectedTotal  int32
		expError       bool
	}{
		{
			name: "valid_response_input_only",
			responseBody: `{
"embeddings": [{"float": [[0.1, 0.2, 0.3, 0.4]]}],
"id": "em-123",
"meta": {"tokens": {"input_tokens": 25}}
}`,
			expectedInput:  25,
			expectedOutput: -1,
			expectedTotal:  25,
		},
		{
			name: "valid_response_with_output_tokens",
			responseBody: `{
"embeddings": [{"float": [[0.1, 0.2, 0.3, 0.4]]}],
"id": "em-123",
"meta": {"tokens": {"input_tokens": 25, "output_tokens": 0}}
}`,
			expectedInput:  25,
			expectedOutput: 0,
			expectedTotal:  25,
		},
		{
			name:         "invalid_json",
			responseBody: `invalid json`,
			expError:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewEmbedCohereToCohereTranslator("v2", "")
			translator.(*cohereToCohereTranslatorV2Embed).requestModel = "embed-v4.0"
			headerMutation, bodyMutation, tokenUsage, responseModel, err := translator.ResponseBody(
				map[string]string{contentTypeHeaderName: jsonContentType},
				strings.NewReader(tc.responseBody),
				true,
				nil,
			)

			if tc.expError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			expected := tokenUsageFrom(tc.expectedInput, -1, -1, tc.expectedOutput, tc.expectedTotal, -1)
			require.Equal(t, expected, tokenUsage)
			require.Equal(t, "embed-v4.0", responseModel)
			require.Nil(t, headerMutation)
			require.Nil(t, bodyMutation)
		})
	}
}

func TestCohereToCohereTranslatorV2Embed_ResponseError(t *testing.T) {
	translator := NewEmbedCohereToCohereTranslator("v2", "")

	t.Run("non_json_error", func(t *testing.T) {
		respHeaders := map[string]string{
			statusHeaderName:      "503",
			contentTypeHeaderName: "text/plain",
		}
		errorBody := "Service Unavailable"

		headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, strings.NewReader(errorBody))
		require.NoError(t, err)
		require.NotNil(t, headerMutation)
		require.NotNil(t, bodyMutation)

		var cohereErr cohereschema.EmbedV2Error
		require.NoError(t, json.Unmarshal(bodyMutation, &cohereErr))
		require.NotNil(t, cohereErr.Message)
		require.Equal(t, errorBody, *cohereErr.Message)
	})

	t.Run("json_error_passthrough", func(t *testing.T) {
		respHeaders := map[string]string{
			statusHeaderName:      "400",
			contentTypeHeaderName: jsonContentType,
		}
		errorBody := `{"error": {"message": "Invalid request"}}`

		headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, strings.NewReader(errorBody))
		require.NoError(t, err)
		require.Nil(t, headerMutation)
		require.Nil(t, bodyMutation)
	})

	t.Run("read_error", func(t *testing.T) {
		respHeaders := map[string]string{
			statusHeaderName:      "500",
			contentTypeHeaderName: "text/plain",
		}
		headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, alwaysErrReader{})
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to read error body")
		require.Nil(t, headerMutation)
		require.Nil(t, bodyMutation)
	})
}

type mockEmbedSpanTranslator struct{ recordCalled bool }

func (m *mockEmbedSpanTranslator) EndSpan()                   {}
func (m *mockEmbedSpanTranslator) EndSpanOnError(int, []byte) {}
func (m *mockEmbedSpanTranslator) RecordResponse(_ *cohereschema.EmbedV2Response) {
	m.recordCalled = true
}
func (m *mockEmbedSpanTranslator) RecordResponseChunk(*struct{}) {}

func TestCohereToCohereTranslatorV2Embed_ResponseBody_RecordsResponseInSpan(t *testing.T) {
	mspan := &mockEmbedSpanTranslator{}
	tr := NewEmbedCohereToCohereTranslator("v2", "")
	tr.(*cohereToCohereTranslatorV2Embed).requestModel = "embed-v4.0"

	body := `{"embeddings":[[0.1, 0.2, 0.3]],"id":"em-456"}`
	_, _, _, _, err := tr.ResponseBody(
		map[string]string{contentTypeHeaderName: jsonContentType},
		strings.NewReader(body),
		true,
		mspan,
	)
	require.NoError(t, err)
	require.True(t, mspan.recordCalled)
}
