// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// TestResponseModel_OpenAIStreaming tests that OpenAI streaming returns the actual model version
// OpenAI supports automatic routing where generic models may resolve to specific versions
func TestResponseModel_OpenAIStreaming(t *testing.T) {
	translator := NewChatCompletionOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1ChatCompletion)

	// Initialize as streaming
	req := &openai.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: true,
	}
	_, _, err := translator.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.True(t, translator.stream)

	// Simulate SSE stream chunks with model in response
	sseChunks := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-2024-11-20", responseModel) // Returns actual versioned model
	require.Equal(t, tokenUsageFrom(10, -1, -1, 5, 15, -1), tokenUsage)
}

// TestResponseModel_EmptyFallback tests the fallback to request model when response model is empty
// This is a safeguard for test or non-compliant OpenAI backends that don't fill in the model field
func TestResponseModel_EmptyFallback(t *testing.T) {
	t.Run("non-streaming", func(t *testing.T) {
		translator := NewChatCompletionOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1ChatCompletion)

		// Set request model
		req := &openai.ChatCompletionRequest{
			Model:  "gpt-4o",
			Stream: false,
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.False(t, translator.stream)

		// Response without model field (empty model)
		responseJSON := `{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1234567890,
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello world"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(responseJSON)), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", responseModel) // Falls back to request model
		require.Equal(t, tokenUsageFrom(10, -1, -1, 5, 15, -1), tokenUsage)
	})

	t.Run("streaming", func(t *testing.T) {
		translator := NewChatCompletionOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1ChatCompletion)

		// Set request model
		req := &openai.ChatCompletionRequest{
			Model:  "gpt-4o-mini",
			Stream: true,
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.True(t, translator.stream)

		// SSE chunks without model field
		sseChunks := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-mini", responseModel) // Falls back to request model
		require.Equal(t, tokenUsageFrom(10, -1, -1, 5, 15, -1), tokenUsage)
	})

	t.Run("with model override", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{
			modelNameOverride: "gpt-4o-2024-11-20",
			path:              "/v1/chat/completions",
		}

		// Set request model (will be overridden)
		req := &openai.ChatCompletionRequest{
			Model:  "gpt-4o",
			Stream: false,
		}
		original := []byte(`{"model":"gpt-4o"}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", translator.requestModel) // Override is stored

		// Response without model field
		responseJSON := `{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1234567890,
			"model": "",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello world"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(responseJSON)), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel) // Falls back to overridden model
		require.Equal(t, tokenUsageFrom(10, -1, -1, 5, 15, -1), tokenUsage)
	})
}

func TestOpenAIToOpenAITranslatorV1ChatCompletionRequestBody(t *testing.T) {
	t.Run("valid body", func(t *testing.T) {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
				originalReq := &openai.ChatCompletionRequest{Model: "foo-bar-ai", Stream: stream}

				o := NewChatCompletionOpenAIToOpenAITranslator("foo/v1", "").(*openAIToOpenAITranslatorV1ChatCompletion)
				hm, bm, err := o.RequestBody(nil, originalReq, false)
				require.Nil(t, bm)
				require.NoError(t, err)
				require.Equal(t, stream, o.stream)
				require.NotNil(t, hm)
				require.Len(t, hm, 1)
				require.Equal(t, pathHeaderName, hm[0].Key())
				require.Equal(t, "/foo/v1/chat/completions", hm[0].Value())
			})
		}
	})
	t.Run("model name override", func(t *testing.T) {
		for _, forcedMutation := range []bool{true, false} {
			originalReq := &openai.ChatCompletionRequest{Model: "gpt-4o-mini", Stream: false}
			rawReq, err := json.Marshal(originalReq)
			require.NoError(t, err)
			modelName := "gpt-4o-mini-2024-07-18" // Example model name override.
			o := &openAIToOpenAITranslatorV1ChatCompletion{modelNameOverride: modelName, path: "/v1/chat/completions"}
			hm, body, err := o.RequestBody(rawReq, originalReq, forcedMutation)
			require.NoError(t, err)
			require.NotNil(t, body)
			var newReq openai.ChatCompletionRequest
			err = json.Unmarshal(body, &newReq)
			require.NoError(t, err)
			require.Equal(t, modelName, newReq.Model)
			require.NotNil(t, hm)
			require.Len(t, hm, 2)
			require.Equal(t, pathHeaderName, hm[0].Key())
			require.Equal(t, o.path, hm[0].Value())
			require.Equal(t, contentLengthHeaderName, hm[1].Key())
			require.Equal(t, strconv.Itoa(len(body)), hm[1].Value())
		}
	})
	t.Run("forced mutation", func(t *testing.T) {
		originalReq := &openai.ChatCompletionRequest{Model: "foo-bar-ai", Stream: true}
		original := []byte("whatever")
		o := NewChatCompletionOpenAIToOpenAITranslator("foo/v1", "").(*openAIToOpenAITranslatorV1ChatCompletion)
		hm, body, err := o.RequestBody(original, originalReq, true)
		require.NoError(t, err)
		require.True(t, o.stream)
		require.NotNil(t, body)
		require.NotNil(t, hm)
		require.Len(t, hm, 2)
		require.Equal(t, pathHeaderName, hm[0].Key())
		require.Equal(t, o.path, hm[0].Value())
		require.Equal(t, contentLengthHeaderName, hm[1].Key())
		require.Equal(t, strconv.Itoa(len(body)), hm[1].Value())
	})
}

func TestOpenAIToOpenAITranslator_ResponseError(t *testing.T) {
	tests := []struct {
		name            string
		responseHeaders map[string]string
		input           io.Reader
		contentType     string
		output          openai.Error
	}{
		{
			name:        "test unhealthy upstream",
			contentType: "text/plain",
			responseHeaders: map[string]string{
				":status":      "503",
				"content-type": "text/plain",
			},
			input: bytes.NewBuffer([]byte("service not available")),
			output: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    openAIBackendError,
					Code:    ptr.To("503"),
					Message: "service not available",
				},
			},
		},
		{
			name: "test OpenAI missing required field error",
			responseHeaders: map[string]string{
				":status":      "400",
				"content-type": "application/json",
			},
			contentType: "application/json",
			input:       bytes.NewBuffer([]byte(`{"error": {"message": "missing required field", "type": "BadRequestError", "code": "400"}}`)),
			output: openai.Error{
				Error: openai.ErrorType{
					Type:    "BadRequestError",
					Code:    ptr.To("400"),
					Message: "missing required field",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &openAIToOpenAITranslatorV1ChatCompletion{}
			hm, newBody, err := o.ResponseError(tt.responseHeaders, tt.input)
			require.NoError(t, err)
			if tt.contentType == jsonContentType {
				require.Nil(t, hm)
				require.Nil(t, newBody)
				var openAIError openai.Error
				require.NoError(t, json.Unmarshal(tt.input.(*bytes.Buffer).Bytes(), &openAIError))
				if !cmp.Equal(openAIError, tt.output) {
					t.Errorf("ConvertOpenAIErrorResp(), diff(got, expected) = %s\n", cmp.Diff(openAIError, tt.output))
				}
				return
			}

			require.NotNil(t, hm)
			require.Len(t, hm, 2)
			require.Equal(t, contentTypeHeaderName, hm[0].Key())
			require.Equal(t, jsonContentType, hm[0].Value()) //nolint:testifylint
			require.Equal(t, contentLengthHeaderName, hm[1].Key())
			require.Equal(t, strconv.Itoa(len(newBody)), hm[1].Value())

			var openAIError openai.Error
			require.NoError(t, json.Unmarshal(newBody, &openAIError))
			if !cmp.Equal(openAIError, tt.output) {
				t.Errorf("ConvertOpenAIErrorResp(), diff(got, expected) = %s\n", cmp.Diff(openAIError, tt.output))
			}
		})
	}
}

func TestOpenAIToOpenAITranslatorV1ChatCompletionResponseBody(t *testing.T) {
	t.Run("streaming", func(t *testing.T) {
		// This is the real event stream from OpenAI.
		wholeBody := []byte(`
data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":"This"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" is"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" a"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":"!"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" How"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" can"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" I"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" assist"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" you"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":" today"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"content":"?"},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]

`)

		o := &openAIToOpenAITranslatorV1ChatCompletion{stream: true}
		for i := range wholeBody {
			hm, bm, tokenUsage, _, err := o.ResponseBody(nil, bytes.NewReader(wholeBody[i:i+1]), false, nil)
			require.NoError(t, err)
			require.Nil(t, hm)
			require.Nil(t, bm)
			if outputTokens, ok := tokenUsage.OutputTokens(); ok && outputTokens > 0 {
				require.Equal(t, uint32(12), outputTokens)
			}
		}
	})

	t.Run("streaming read error", func(t *testing.T) {
		o := &openAIToOpenAITranslatorV1ChatCompletion{stream: true}
		pr, pw := io.Pipe()
		// Close the writer immediately with an error so reads fail.
		_ = pw.CloseWithError(fmt.Errorf("error reading body"))
		_, _, _, _, err := o.ResponseBody(nil, pr, false, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read body")
	})

	t.Run("non-streaming", func(t *testing.T) {
		t.Run("invalid body", func(t *testing.T) {
			o := &openAIToOpenAITranslatorV1ChatCompletion{}
			_, _, _, _, err := o.ResponseBody(nil, bytes.NewBuffer([]byte("invalid")), false, nil)
			require.Error(t, err)
		})
		t.Run("null body", func(t *testing.T) {
			// A literal JSON `null` decodes to a nil response without an error;
			// it must not panic and should report zero usage.
			s := &testotel.MockSpan{}
			o := &openAIToOpenAITranslatorV1ChatCompletion{requestModel: "gpt-4o-mini"}
			_, _, usedToken, responseModel, err := o.ResponseBody(nil, bytes.NewBuffer([]byte("null")), false, s)
			require.NoError(t, err)
			require.Equal(t, tokenUsageFrom(0, -1, -1, 0, 0, -1), usedToken)
			require.Equal(t, "gpt-4o-mini", responseModel)
		})
		t.Run("valid body", func(t *testing.T) {
			s := &testotel.MockSpan{}
			var resp openai.ChatCompletionResponse
			resp.Usage.TotalTokens = 42
			body, err := json.Marshal(resp)
			require.NoError(t, err)
			o := &openAIToOpenAITranslatorV1ChatCompletion{}
			_, _, usedToken, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, s)
			require.NoError(t, err)
			require.Equal(t, tokenUsageFrom(0, -1, -1, 0, 42, -1), usedToken)
			require.Equal(t, &resp, s.Resp)
		})
		t.Run("valid body with different response model", func(t *testing.T) {
			s := &testotel.MockSpan{}
			var resp openai.ChatCompletionResponse
			resp.Model = "gpt-4o-mini-2024-07-18"
			resp.Usage.PromptTokens = 10
			resp.Usage.CompletionTokens = 20
			resp.Usage.TotalTokens = 30
			body, err := json.Marshal(resp)
			require.NoError(t, err)
			o := &openAIToOpenAITranslatorV1ChatCompletion{}
			_, _, usedToken, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, s)
			require.NoError(t, err)
			require.Equal(t, tokenUsageFrom(10, -1, -1, 20, 30, -1), usedToken)
			require.Equal(t, &resp, s.Resp)
		})
	})
	t.Run("valid body with reasoning tokens", func(t *testing.T) {
		s := &testotel.MockSpan{}
		var resp openai.ChatCompletionResponse
		resp.Usage.PromptTokens = 10
		resp.Usage.CompletionTokens = 20
		resp.Usage.TotalTokens = 30
		resp.Usage.CompletionTokensDetails = &openai.CompletionTokensDetails{
			ReasoningTokens: 8,
		}
		body, err := json.Marshal(resp)
		require.NoError(t, err)
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		_, _, usedToken, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, s)
		require.NoError(t, err)
		require.Equal(t, tokenUsageFrom(10, -1, -1, 20, 30, 8), usedToken)
		require.Equal(t, &resp, s.Resp)
	})
	t.Run("response reasoning content", func(t *testing.T) {
		t.Run("valid body", func(t *testing.T) {
			s := &testotel.MockSpan{}
			var resp openai.ChatCompletionResponse
			resp.Usage.TotalTokens = 42
			resp.Choices = []openai.ChatCompletionResponseChoice{
				{
					Message: openai.ChatCompletionResponseChoiceMessage{
						Content: ptr.To("plain content"),
						ReasoningContent: &openai.ReasoningContentUnion{
							Value: "reasoning content",
						},
					},
				},
			}
			body, err := json.Marshal(resp)
			require.NoError(t, err)
			o := &openAIToOpenAITranslatorV1ChatCompletion{}
			_, _, usedToken, _, err := o.ResponseBody(nil, bytes.NewBuffer(body), false, s)
			require.NoError(t, err)
			require.Equal(t, tokenUsageFrom(0, -1, -1, 0, 42, -1), usedToken)
			require.Equal(t, &resp, s.Resp)
		})
	})
}

func TestExtractUsageFromBufferEvent(t *testing.T) {
	t.Run("valid usage data", func(t *testing.T) {
		s := &testotel.MockSpan{}
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		o.buffered = []byte("data: {\"usage\": {\"total_tokens\": 42}}\n")
		usedToken := o.extractUsageFromBufferEvent(s)
		require.Equal(t, tokenUsageFrom(0, -1, -1, 0, 42, -1), usedToken)
		require.Empty(t, o.buffered)
		require.Len(t, s.RespChunks, 1)
	})

	t.Run("valid usage data after invalid", func(t *testing.T) {
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		o.buffered = []byte("data: invalid\ndata: {\"usage\": {\"total_tokens\": 42}}\n")
		usedToken := o.extractUsageFromBufferEvent(nil)
		require.Equal(t, tokenUsageFrom(0, -1, -1, 0, 42, -1), usedToken)
		require.Empty(t, o.buffered)
	})

	t.Run("no usage data and then become valid", func(t *testing.T) {
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		o.buffered = []byte("data: {}\n\ndata: ")
		usedToken := o.extractUsageFromBufferEvent(nil)
		require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usedToken)
		require.GreaterOrEqual(t, len(o.buffered), 1)

		o.buffered = append(o.buffered, []byte("{\"usage\": {\"total_tokens\": 42}}\n")...)
		usedToken = o.extractUsageFromBufferEvent(nil)
		require.Equal(t, tokenUsageFrom(0, -1, -1, 0, 42, -1), usedToken)
		require.Empty(t, o.buffered)
	})

	t.Run("valid usage data with cached tokens", func(t *testing.T) {
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		o.buffered = []byte("data: {\"usage\": {\"prompt_tokens\": 5, \"completion_tokens\": 3, \"total_tokens\": 8, \"prompt_tokens_details\": {\"cached_tokens\": 2, \"cache_write_tokens\": 1}}}\n")
		usedToken := o.extractUsageFromBufferEvent(nil)
		require.Equal(t, tokenUsageFrom(5, 2, 1, 3, 8, -1), usedToken)
		require.Empty(t, o.buffered)
	})

	t.Run("valid usage data with reasoning tokens", func(t *testing.T) {
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		o.buffered = []byte("data: {\"usage\": {\"prompt_tokens\": 10, \"completion_tokens\": 20, \"total_tokens\": 30, \"completion_tokens_details\": {\"reasoning_tokens\": 8}}}\n")
		usedToken := o.extractUsageFromBufferEvent(nil)
		require.Equal(t, tokenUsageFrom(10, -1, -1, 20, 30, 8), usedToken)
		require.Empty(t, o.buffered)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		o := &openAIToOpenAITranslatorV1ChatCompletion{}
		o.buffered = []byte("data: invalid\n")
		usedToken := o.extractUsageFromBufferEvent(nil)
		require.Equal(t, tokenUsageFrom(-1, -1, -1, -1, -1, -1), usedToken)
		require.Empty(t, o.buffered)
	})
}

// TestResponseModel_OpenAI tests that OpenAI returns the actual model version in response
func TestResponseModel_OpenAI(t *testing.T) {
	translator := NewChatCompletionOpenAIToOpenAITranslator("v1", "")

	// Create a response like OpenAI would return - with the actual model version
	var resp openai.ChatCompletionResponse
	resp.Model = "gpt-4o-2024-08-06" // OpenAI returns actual version, not the alias
	resp.Usage.TotalTokens = 15
	resp.Usage.PromptTokens = 10
	resp.Usage.CompletionTokens = 5

	body, err := json.Marshal(resp)
	require.NoError(t, err)

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewBuffer(body), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-2024-08-06", responseModel)
	require.Equal(t, tokenUsageFrom(10, -1, -1, 5, 15, -1), tokenUsage)
}

// TestResponseModel_OpenAIEmbeddings tests OpenAI embeddings (not virtualized but has response field)
func TestResponseModel_OpenAIEmbeddings(t *testing.T) {
	translator := NewEmbeddingOpenAIToOpenAITranslator("v1", "")

	// OpenAI embeddings response includes model field even though no virtualization
	var resp openai.EmbeddingResponse
	resp.Model = "text-embedding-ada-002" // Returns exactly what was requested
	resp.Usage.PromptTokens = 10
	resp.Usage.TotalTokens = 10

	body, err := json.Marshal(resp)
	require.NoError(t, err)

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), true, nil)
	require.NoError(t, err)
	require.Equal(t, "text-embedding-ada-002", responseModel) // Uses response field as authoritative
	require.Equal(t, tokenUsageFrom(10, -1, -1, -1, 10, -1), tokenUsage)
}

func TestRedactBody(t *testing.T) {
	t.Run("redacts message content", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{}

		originalContent := "This is sensitive AI-generated content"
		resp := &openai.ChatCompletionResponse{
			ID:      "chatcmpl-123",
			Model:   "gpt-4o",
			Object:  "chat.completion",
			Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    "assistant",
						Content: &originalContent,
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}

		redacted := translator.RedactBody(resp)

		// Verify original is not modified
		require.Equal(t, "This is sensitive AI-generated content", *resp.Choices[0].Message.Content)

		// Verify redacted copy has redacted content
		require.NotNil(t, redacted.Choices[0].Message.Content)
		require.Contains(t, *redacted.Choices[0].Message.Content, "[REDACTED LENGTH=")
		require.Contains(t, *redacted.Choices[0].Message.Content, "HASH=")
		require.NotContains(t, *redacted.Choices[0].Message.Content, "sensitive")

		// Verify non-sensitive fields are preserved
		require.Equal(t, "chatcmpl-123", redacted.ID)
		require.Equal(t, "gpt-4o", redacted.Model)
		require.Equal(t, time.Time(resp.Created).Unix(), time.Time(redacted.Created).Unix())
		require.Equal(t, 10, redacted.Usage.PromptTokens)
		require.Equal(t, 5, redacted.Usage.CompletionTokens)
	})

	t.Run("redacts tool calls", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{}

		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-456",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: "assistant",
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("call_123"),
								Type: "function",
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_weather",
									Arguments: `{"location": "San Francisco", "unit": "celsius"}`,
								},
							},
						},
					},
				},
			},
		}

		redacted := translator.RedactBody(resp)

		// Verify original is not modified
		require.Equal(t, "get_weather", resp.Choices[0].Message.ToolCalls[0].Function.Name)
		require.Contains(t, resp.Choices[0].Message.ToolCalls[0].Function.Arguments, "San Francisco")

		// Verify redacted copy: name kept, arguments redacted
		require.Len(t, redacted.Choices[0].Message.ToolCalls, 1)
		require.Equal(t, "get_weather", redacted.Choices[0].Message.ToolCalls[0].Function.Name)
		require.Contains(t, redacted.Choices[0].Message.ToolCalls[0].Function.Arguments, "[REDACTED")
		require.NotContains(t, redacted.Choices[0].Message.ToolCalls[0].Function.Arguments, "San Francisco")
	})

	t.Run("redacts audio data", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{}

		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-789",
			Model: "gpt-4o-audio",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: "assistant",
						Audio: &openai.ChatCompletionResponseChoiceMessageAudio{
							ID:         "audio_123",
							Data:       "base64encodedaudiodata",
							Transcript: "This is the audio transcript",
							ExpiresAt:  1234567890,
						},
					},
				},
			},
		}

		redacted := translator.RedactBody(resp)

		// Verify original is not modified
		require.Equal(t, "base64encodedaudiodata", resp.Choices[0].Message.Audio.Data)
		require.Equal(t, "This is the audio transcript", resp.Choices[0].Message.Audio.Transcript)

		// Verify redacted copy has redacted audio
		require.NotNil(t, redacted.Choices[0].Message.Audio)
		require.Contains(t, redacted.Choices[0].Message.Audio.Data, "[REDACTED")
		require.Contains(t, redacted.Choices[0].Message.Audio.Transcript, "[REDACTED")
		require.NotContains(t, redacted.Choices[0].Message.Audio.Data, "base64encodedaudiodata")
		require.NotContains(t, redacted.Choices[0].Message.Audio.Transcript, "audio transcript")
	})

	t.Run("handles nil response", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{}

		redacted := translator.RedactBody(nil)

		require.Nil(t, redacted)
	})

	t.Run("handles empty choices", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{}

		resp := &openai.ChatCompletionResponse{
			ID:      "chatcmpl-empty",
			Model:   "gpt-4o",
			Choices: []openai.ChatCompletionResponseChoice{},
		}

		redacted := translator.RedactBody(resp)

		require.NotNil(t, redacted)
		require.Empty(t, redacted.Choices)
	})

	t.Run("does not modify original response", func(t *testing.T) {
		translator := &openAIToOpenAITranslatorV1ChatCompletion{}

		originalContent := "Original content"
		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-999",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    "assistant",
						Content: &originalContent,
					},
				},
			},
		}

		// Create a copy of the original for comparison
		originalContentCopy := *resp.Choices[0].Message.Content

		// Redact the response
		_ = translator.RedactBody(resp)

		// Verify original is completely unchanged
		require.Equal(t, originalContentCopy, *resp.Choices[0].Message.Content)
		require.NotContains(t, *resp.Choices[0].Message.Content, "[REDACTED")
	})
}

// The space after "data:" is optional per the SSE specification, and some
// OpenAI-compatible backends omit it. The stream must still yield usage.
func TestOpenAIToOpenAI_ResponseBody_streaming_noSpaceAfterColon(t *testing.T) {
	translator := &openAIToOpenAITranslatorV1ChatCompletion{stream: true}

	const response = `data:{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}

data:{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":16,"total_tokens":25}}

data:[DONE]

`

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, strings.NewReader(response), true, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o", responseModel)

	input, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(9), input)
	output, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(16), output)
	total, ok := tokenUsage.TotalTokens()
	require.True(t, ok)
	require.Equal(t, uint32(25), total)
}
