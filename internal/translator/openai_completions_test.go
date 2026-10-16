// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestOpenAIToOpenAITranslatorV1CompletionRequestBody(t *testing.T) {
	for _, tc := range []struct {
		name              string
		modelNameOverride internalapi.ModelNameOverride
		onRetry           bool
		expPath           string
		expBodyContains   string
	}{
		{
			name:            "valid_body",
			expPath:         "/v1/completions",
			expBodyContains: "",
		},
		{
			name:              "model_name_override",
			modelNameOverride: "custom-completion-model",
			expPath:           "/v1/completions",
			expBodyContains:   `"model":"custom-completion-model"`,
		},
		{
			name:    "on_retry",
			onRetry: true,
			expPath: "/v1/completions",
		},
		{
			name:              "model_name_override_with_retry",
			modelNameOverride: "custom-completion-model",
			onRetry:           true,
			expPath:           "/v1/completions",
			expBodyContains:   `"model":"custom-completion-model"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewCompletionOpenAIToOpenAITranslator("v1", tc.modelNameOverride)
			originalBody := `{"model":"gpt-3.5-turbo-instruct","prompt":"Say hello","max_tokens":10}`
			var req openai.CompletionRequest
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

func TestOpenAIToOpenAITranslatorV1CompletionRequestBodyStreaming(t *testing.T) {
	translator := NewCompletionOpenAIToOpenAITranslator("v1", "")
	originalBody := `{"model":"gpt-3.5-turbo-instruct","prompt":"Say hello","stream":true}`
	var req openai.CompletionRequest
	require.NoError(t, json.Unmarshal([]byte(originalBody), &req))

	headerMutation, bodyMutation, err := translator.RequestBody([]byte(originalBody), &req, false)
	require.NoError(t, err)
	require.NotNil(t, headerMutation)
	require.Nil(t, bodyMutation)

	// Verify the translator is now in streaming mode.
	impl := translator.(*openAIToOpenAITranslatorV1Completion)
	require.True(t, impl.stream)
}

func TestOpenAIToOpenAITranslatorV1CompletionResponseHeaders(t *testing.T) {
	translator := NewCompletionOpenAIToOpenAITranslator("v1", "")
	headerMutation, err := translator.ResponseHeaders(map[string]string{})
	require.NoError(t, err)
	require.Nil(t, headerMutation)
}

func TestOpenAIToOpenAITranslatorV1CompletionResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name           string
		responseBody   string
		responseStatus string
		expTokenUsage  metrics.TokenUsage
		expModel       string
		expError       bool
	}{
		{
			name: "valid_response",
			responseBody: `{
				"id": "cmpl-123",
				"object": "text_completion",
				"created": 1677649420,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [
					{
						"text": "Hello! How can I help you today?",
						"index": 0,
						"finish_reason": "stop"
					}
				],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 8,
					"total_tokens": 13
				}
			}`,
			expTokenUsage: tokenUsageFrom(5, -1, -1, 8, 13, -1),
			expModel:      "gpt-3.5-turbo-instruct",
		},
		{
			name: "valid_response_with_cached_tokens",
			responseBody: `{
				"id": "cmpl-123",
				"object": "text_completion",
				"created": 1677649420,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [
					{
						"text": "Hello! How can I help you today?",
						"index": 0,
						"finish_reason": "stop"
					}
				],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 8,
					"total_tokens": 13,
					"prompt_tokens_details": {
						"cached_tokens": 2,
						"cache_creation_input_tokens": 1
					}
				}
			}`,
			expTokenUsage: tokenUsageFrom(5, 2, 1, 8, 13, -1),
			expModel:      "gpt-3.5-turbo-instruct",
		},
		{
			name: "valid_response_with_reasoning_tokens",
			responseBody: `{
				"id": "cmpl-123",
				"object": "text_completion",
				"created": 1677649420,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [
					{
						"text": "Hello! How can I help you today?",
						"index": 0,
						"finish_reason": "stop"
					}
				],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 8,
					"total_tokens": 13,
					"completion_tokens_details": {
						"reasoning_tokens": 3
					}
				}
			}`,
			expTokenUsage: tokenUsageFrom(5, -1, -1, 8, 13, 3),
			expModel:      "gpt-3.5-turbo-instruct",
		},
		{
			name:          "invalid_json",
			responseBody:  `invalid json`,
			expError:      true,
			expTokenUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
		},
		{
			name: "response_without_usage",
			responseBody: `{
				"id": "cmpl-123",
				"object": "text_completion",
				"created": 1677649420,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [
					{
						"text": "Hello!",
						"index": 0,
						"finish_reason": "length"
					}
				]
			}`,
			expTokenUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
			expModel:      "gpt-3.5-turbo-instruct",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewCompletionOpenAIToOpenAITranslator("v1", "")
			respHeaders := map[string]string{
				"content-type": "application/json",
			}
			if tc.responseStatus != "" {
				respHeaders[statusHeaderName] = tc.responseStatus
			} else {
				respHeaders[statusHeaderName] = "200"
			}

			headerMutation, bodyMutation, tokenUsage, responseModel, err := translator.ResponseBody(
				respHeaders,
				strings.NewReader(tc.responseBody),
				true,
				nil,
			)

			if tc.expError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.expTokenUsage, tokenUsage)
			require.Equal(t, tc.expModel, responseModel)
			require.Nil(t, bodyMutation)
			require.Nil(t, headerMutation)
		})
	}
}

func TestOpenAIToOpenAITranslatorV1CompletionResponseBodyStreaming(t *testing.T) {
	translator := NewCompletionOpenAIToOpenAITranslator("v1", "")
	impl := translator.(*openAIToOpenAITranslatorV1Completion)
	impl.stream = true

	// Simulate receiving SSE data in chunks.
	chunk1 := `data: {"id":"cmpl-123","object":"text_completion","created":1677649420,"model":"gpt-3.5-turbo-instruct","choices":[{"text":"Hello","index":0,"logprobs":null,"finish_reason":null}]}

`
	chunk2 := `data: {"id":"cmpl-123","object":"text_completion","created":1677649420,"model":"gpt-3.5-turbo-instruct","choices":[{"text":" there!","index":0,"logprobs":null,"finish_reason":null}]}

`
	chunk3 := `data: {"id":"cmpl-123","object":"text_completion","created":1677649420,"model":"gpt-3.5-turbo-instruct","choices":[{"text":"","index":0,"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}

data: [DONE]

`

	respHeaders := map[string]string{
		"content-type": "text/event-stream",
	}

	// Process chunk1.
	headerMutation, bodyMutation, tokenUsage, responseModel, err := impl.ResponseBody(
		respHeaders,
		strings.NewReader(chunk1),
		false,
		nil,
	)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), tokenUsage)
	require.Equal(t, "gpt-3.5-turbo-instruct", responseModel)

	// Process chunk2.
	headerMutation, bodyMutation, tokenUsage, responseModel, err = impl.ResponseBody(
		respHeaders,
		strings.NewReader(chunk2),
		false,
		nil,
	)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), tokenUsage)
	require.Equal(t, "gpt-3.5-turbo-instruct", responseModel)

	// Process chunk3 with usage.
	headerMutation, bodyMutation, tokenUsage, responseModel, err = impl.ResponseBody(
		respHeaders,
		strings.NewReader(chunk3),
		true,
		nil,
	)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	require.Equal(t, tokenUsageFrom(5, -1, -1, 3, 8, -1), tokenUsage)
	require.Equal(t, "gpt-3.5-turbo-instruct", responseModel)
}

func TestOpenAIToOpenAITranslatorV1CompletionResponseBodyStreamingWithReasoningTokens(t *testing.T) {
	translator := NewCompletionOpenAIToOpenAITranslator("v1", "")
	impl := translator.(*openAIToOpenAITranslatorV1Completion)
	impl.stream = true

	chunk := `data: {"id":"cmpl-123","object":"text_completion","created":1677649420,"model":"gpt-3.5-turbo-instruct","choices":[{"text":"","index":0,"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":2}}}

data: [DONE]

`

	respHeaders := map[string]string{
		"content-type": "text/event-stream",
	}

	headerMutation, bodyMutation, tokenUsage, responseModel, err := impl.ResponseBody(
		respHeaders,
		strings.NewReader(chunk),
		true,
		nil,
	)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	require.Equal(t, tokenUsageFrom(5, -1, -1, 3, 8, 2), tokenUsage)
	require.Equal(t, "gpt-3.5-turbo-instruct", responseModel)
}

func TestOpenAIToOpenAITranslatorV1CompletionResponseError(t *testing.T) {
	translator := NewCompletionOpenAIToOpenAITranslator("v1", "")

	t.Run("json_error_passthrough", func(t *testing.T) {
		respHeaders := map[string]string{
			statusHeaderName:      "400",
			contentTypeHeaderName: jsonContentType,
		}
		errorBody := `{"error": {"message": "Invalid prompt", "type": "InvalidRequestError", "param": "prompt", "code": null}}`

		headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, strings.NewReader(errorBody))
		require.NoError(t, err)
		require.Nil(t, bodyMutation)
		require.Nil(t, headerMutation)
	})

	for _, tc := range []struct {
		name        string
		status      string
		contentType string
		body        string
	}{
		{"plain_text_error", "503", "text/plain", "Service Unavailable"},
		{"missing_content_type", "504", "", "upstream request timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			respHeaders := map[string]string{statusHeaderName: tc.status}
			if tc.contentType != "" {
				respHeaders[contentTypeHeaderName] = tc.contentType
			}
			headerMutation, bodyMutation, err := translator.ResponseError(respHeaders, strings.NewReader(tc.body))
			require.NoError(t, err)
			var response openai.Error
			require.NoError(t, json.Unmarshal(bodyMutation, &response))
			require.Equal(t, "error", response.Type)
			require.Equal(t, openAIBackendError, response.Error.Type)
			require.Equal(t, tc.body, response.Error.Message)
			require.Equal(t, &tc.status, response.Error.Code)
			require.Equal(t, []internalapi.Header{
				{contentTypeHeaderName, jsonContentType},
				{contentLengthHeaderName, strconv.Itoa(len(bodyMutation))},
			}, headerMutation)
		})
	}
}
