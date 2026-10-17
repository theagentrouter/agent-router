// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func Test_NewResponsesOpenAIToOpenAITranslator(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		expectPath string
	}{
		{
			name:       "v1",
			apiVersion: "v1",
			expectPath: "/v1/responses",
		},
		{
			name:       "custom path",
			apiVersion: "custom/v1",
			expectPath: "/custom/v1/responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewResponsesOpenAIToOpenAITranslator(tt.apiVersion, "").(*openAIToOpenAITranslatorV1Responses)
			require.NotNil(t, translator)
			require.Equal(t, tt.expectPath, translator.path)
		})
	}
}

func TestResponsesOpenAIToOpenAITranslator_RequestBody(t *testing.T) {
	t.Run("basic request without override", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi"}`)

		headers, body, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", translator.requestModel)
		require.False(t, translator.stream)
		require.Len(t, headers, 1)
		require.Equal(t, pathHeaderName, headers[0].Key())
		require.Equal(t, "/v1/responses", headers[0].Value())
		// Body should be nil when no mutation needed
		require.Nil(t, body)
	})

	t.Run("streaming request", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","stream":true,"input":"Hi"}`)

		headers, body, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.True(t, translator.stream)
		require.Len(t, headers, 1)
		// Body should be nil when no mutation needed
		require.Nil(t, body)
	})

	t.Run("model name override", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "gpt-4-turbo").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi"}`)

		headers, body, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4-turbo", translator.requestModel)

		// Verify the model was overridden in the body
		var result map[string]interface{}
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)
		require.Equal(t, "gpt-4-turbo", result["model"])

		// Verify content-length header is set
		require.Len(t, headers, 2)
		require.Equal(t, contentLengthHeaderName, headers[1].Key())
		require.Equal(t, strconv.Itoa(len(body)), headers[1].Value())
	})

	t.Run("forced mutation without override", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o", "input":"Hi"}`)

		headers, body, err := translator.RequestBody(original, req, true)
		require.NoError(t, err)

		// When forced mutation is true but no override, body should still be returned
		require.NotNil(t, body)
		require.Len(t, headers, 2)
		require.Equal(t, contentLengthHeaderName, headers[1].Key())
	})

	t.Run("empty original with forced mutation", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "override-model").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}

		_, body, err := translator.RequestBody([]byte{}, req, true)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.NotEmpty(t, body)

		// Verify the model override is in the body
		var result map[string]interface{}
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)
		require.Equal(t, "override-model", result["model"])
	})
}

func TestResponsesOpenAIToOpenAITranslator_ResponseHeaders(t *testing.T) {
	translator := NewResponsesOpenAIToOpenAITranslator("v1", "")

	headers, err := translator.ResponseHeaders(map[string]string{
		"content-type": "application/json",
		":status":      "200",
	})

	require.NoError(t, err)
	require.Nil(t, headers)
}

func TestResponsesOpenAIToOpenAITranslator_ResponseBody(t *testing.T) {
	t.Run("non-streaming response", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi"}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		// Create a valid response
		respJSON := []byte(`{
  			"id": "resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b",
  			"object": "response",
  			"created_at": 1741476542,
  			"status": "completed",
  			"model": "gpt-4o-2024-11-20",
  			"output": [
    			{
      				"type": "message",
     				"id": "msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b",
      				"status": "completed",
      				"role": "assistant",
      				"content": [
        				{
          					"type": "output_text",
          					"text": "Hello, how can I help?"
        				}
      				]
    			}
  			],
  			"usage": {
    			"input_tokens": 10,
				"input_tokens_details": {
					"cached_tokens": 2,
					"cache_write_tokens": 3
    			},
    			"output_tokens": 5,
    			"output_tokens_details": {
      				"reasoning_tokens": 0
    			},
    			"total_tokens": 15
  			}
		}`)
		var resp openai.Response
		err = json.Unmarshal(respJSON, &resp)
		require.NoError(t, err)

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(respJSON), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), totalTokens)

		cachedTokens, ok := tokenUsage.CachedInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(2), cachedTokens)

		cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(3), cacheCreationTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), reasoningTokens)
	})

	t.Run("non-streaming response with reasoning tokens", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "o1",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"o1","input":"Hi"}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		respJSON := []byte(`{
			"id": "resp_123",
			"object": "response",
			"created_at": 1741476542,
			"status": "completed",
			"model": "o1-2024-12-17",
			"output": [
				{
					"type": "message",
					"id": "msg_123",
					"status": "completed",
					"role": "assistant",
					"content": [
						{
							"type": "output_text",
							"text": "Hello!"
						}
					]
				}
			],
			"usage": {
				"input_tokens": 10,
				"input_tokens_details": {
					"cached_tokens": 0
				},
				"output_tokens": 25,
				"output_tokens_details": {
					"reasoning_tokens": 15
				},
				"total_tokens": 35
			}
		}`)

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(respJSON), false, nil)
		require.NoError(t, err)
		require.Equal(t, "o1-2024-12-17", responseModel)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), reasoningTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(25), outputTokens)
	})

	t.Run("non-streaming response with fallback model", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi"}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		// Response without model field
		respJSON := []byte(`{
  			"id": "resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b",
  			"object": "response",
  			"created_at": 1741476542,
  			"status": "completed",
  			"model": "",
  			"output": [
    			{
      				"type": "message",
     				"id": "msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b",
      				"status": "completed",
      				"role": "assistant",
      				"content": [
        				{
          					"type": "output_text",
          					"text": "Hello, how can I help?"
        				}
      				]
    			}
  			],
  			"usage": {
    			"input_tokens": 10,
    			"input_tokens_details": {
      				"cached_tokens": 2
    			},
    			"output_tokens": 5,
    			"output_tokens_details": {
      				"reasoning_tokens": 0
    			},
    			"total_tokens": 15
  			}
		}`)
		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(respJSON), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", responseModel) // Falls back to request model

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)
	})

	t.Run("streaming response", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi","stream":true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.True(t, translator.stream)

		// Simulate SSE stream with response events
		sseChunks := `data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"in_progress","role":"assistant","content":[]}}

data: {"type":"response.content_part.added","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

data: {"type":"response.output_text.delta","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"delta":"Hello"}

data: {"type":"response.output_text.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"text":"Hello, how can I help?"}

data: {"type":"response.content_part.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello, how can I help?","annotations":[]}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?","annotations":[]}]}}

data: {"type":"response.completed","response":{"id":"resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b","object":"response","created_at":1741476542,"status":"completed","model":"","output":[{"type":"message","id":"msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`
		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), totalTokens)

		cachedTokens, ok := tokenUsage.CachedInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(2), cachedTokens)

		cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(3), cacheCreationTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), reasoningTokens)
	})

	t.Run("streaming response with reasoning tokens", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "o1",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"o1","input":"Hi","stream":true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.True(t, translator.stream)

		sseChunks := `data: {"type":"response.created","response":{"model":"o1-2024-12-17"}}

data: {"type":"response.completed","response":{"id":"resp_123","object":"response","created_at":1741476542,"status":"completed","model":"o1-2024-12-17","output":[{"type":"message","id":"msg_123","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":25,"output_tokens_details":{"reasoning_tokens":15},"total_tokens":35}}}

data: [DONE]

`
		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "o1-2024-12-17", responseModel)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), reasoningTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(25), outputTokens)
	})

	t.Run("streaming response with fallback model", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o-mini",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o-mini","input":"Hi","stream": true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		// Streaming response without model in events
		sseChunks := `data: {"type":"response.created","response":{"model":""}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"in_progress","role":"assistant","content":[]}}

data: {"type":"response.content_part.added","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

data: {"type":"response.output_text.delta","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"delta":"Hello"}

data: {"type":"response.output_text.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"text":"Hello, how can I help?"}

data: {"type":"response.content_part.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello, how can I help?","annotations":[]}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?","annotations":[]}]}}

data: {"type":"response.completed","response":{"id":"resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b","object":"response","created_at":1741476542,"status":"completed","model":"","output":[{"type":"message","id":"msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`
		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-mini", responseModel) // Falls back to request model

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)
	})
}

func TestResponses_HandleStreamingResponse(t *testing.T) {
	t.Run("valid streaming events", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi","stream":true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		sseChunks := `data: {"type":"response.created","response":{"id":"resp_67c9fdcecf488190bdd9a0409de3a1ec07b8b0ad4e5eb654","object":"response","created_at":1741487325,"status":"in_progress","model":"gpt-4o-2024-11-20","output":[],"parallel_tool_calls":true,"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled"},"sequence_number": 1}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"in_progress","role":"assistant","content":[]}}

data: {"type":"response.content_part.added","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

data: {"type":"response.output_text.delta","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"delta":"Hello"}

data: {"type":"response.output_text.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"text":"Hello, how can I help?"}

data: {"type":"response.content_part.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello, how can I help?","annotations":[]}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?","annotations":[]}]}}

data: {"type":"response.completed","response":{"id":"resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b","object":"response","created_at":1741476542,"status":"completed","model":"","output":[{"type":"message","id":"msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`
		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		inputTokens, _ := tokenUsage.InputTokens()
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, _ := tokenUsage.OutputTokens()
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, _ := tokenUsage.TotalTokens()
		require.Equal(t, uint32(15), totalTokens)

		cachedTokens, _ := tokenUsage.CachedInputTokens()
		require.Equal(t, uint32(2), cachedTokens)

		cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), cacheCreationTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), reasoningTokens)
	})

	t.Run("streaming read error", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi","stream":true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		// Create a reader that fails
		failReader := &failingReader{}

		_, _, _, _, err = translator.ResponseBody(nil, failReader, true, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read body")
	})

	t.Run("response completed split across response body calls", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi","stream":true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		firstChunk := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.completed","response":{"id":"resp_123","object":"response","created_at":1741476542,"status":"completed","model":"gpt-4o-2024-11-20","output":[],"usage":{"input_tokens":10,`)
		secondChunk := []byte(`"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`)

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(firstChunk), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)
		_, ok := tokenUsage.InputTokens()
		require.False(t, ok)

		_, _, tokenUsage, responseModel, err = translator.ResponseBody(nil, bytes.NewReader(secondChunk), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), totalTokens)

		cachedTokens, ok := tokenUsage.CachedInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(2), cachedTokens)

		cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), cacheCreationTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), reasoningTokens)
	})

	t.Run("CRLF-delimited events without a space after data colon", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		_, _, err := translator.RequestBody([]byte(`{"model":"gpt-4o","input":"Hi","stream":true}`), req, false)
		require.NoError(t, err)

		firstChunk := []byte("data:{\"type\":\"response.created\",\"response\":{\"model\":\"gpt-4o-2024-11-20\"}}\r\n\r\n")
		secondChunk := []byte("data:{\"type\":\"response.completed\",\"response\":{\"id\":\"resp_123\",\"object\":\"response\",\"created_at\":1741476542,\"status\":\"completed\",\"model\":\"gpt-4o-2024-11-20\",\"output\":[],\"usage\":{\"input_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens\":5,\"output_tokens_details\":{\"reasoning_tokens\":0},\"total_tokens\":15}}}\r\n\r\n")

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(firstChunk), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)
		_, ok := tokenUsage.InputTokens()
		require.False(t, ok)

		_, _, tokenUsage, responseModel, err = translator.ResponseBody(nil, bytes.NewReader(secondChunk), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)
	})

	t.Run("complete event followed by next response body call", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi","stream":true}`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		firstChunk := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

`)
		secondChunk := []byte(`data: {"type":"response.completed","response":{"id":"resp_123","object":"response","created_at":1741476542,"status":"completed","model":"gpt-4o-2024-11-20","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`)

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(firstChunk), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)
		require.Empty(t, translator.buffered)
		_, ok := tokenUsage.InputTokens()
		require.False(t, ok)

		_, _, tokenUsage, responseModel, err = translator.ResponseBody(nil, bytes.NewReader(secondChunk), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)
		require.Empty(t, translator.buffered)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), totalTokens)
	})
}

func TestResponses_HandleNonStreamingResponse(t *testing.T) {
	t.Run("complete response", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		original := []byte(`{"model":"gpt-4o","input":"Hi"`)
		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)

		respJSON := []byte(`{
  			"id": "resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b",
  			"object": "response",
  			"created_at": 1741476542,
  			"status": "completed",
  			"model": "gpt-4o-2024-11-20",
  			"output": [
    			{
      				"type": "message",
     				"id": "msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b",
      				"status": "completed",
      				"role": "assistant",
      				"content": [
        				{
          					"type": "output_text",
          					"text": "Hello, how can I help?"
        				}
      				]
    			}
  			],
  			"usage": {
    			"input_tokens": 10,
    			"input_tokens_details": {
      				"cached_tokens": 2
    			},
    			"output_tokens": 5,
    			"output_tokens_details": {
      				"reasoning_tokens": 0
    			},
    			"total_tokens": 15
  			}
		}`)

		_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(respJSON), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		inputTokens, _ := tokenUsage.InputTokens()
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, _ := tokenUsage.OutputTokens()
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, _ := tokenUsage.TotalTokens()
		require.Equal(t, uint32(15), totalTokens)

		cachedTokens, _ := tokenUsage.CachedInputTokens()
		require.Equal(t, uint32(2), cachedTokens)

		cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), cacheCreationTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), reasoningTokens)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		invalidBody := bytes.NewReader([]byte(`{invalid json`))

		_, _, _, _, err = translator.ResponseBody(nil, invalidBody, false, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to unmarshal body")
	})
}

func TestResponses_ExtractUsageFromBufferEvent(t *testing.T) {
	t.Run("valid usage data", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		chunks := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"in_progress","role":"assistant","content":[]}}

data: {"type":"response.content_part.added","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

data: {"type":"response.output_text.delta","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"delta":"Hello"}

data: {"type":"response.output_text.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"text":"Hello, how can I help?"}

data: {"type":"response.content_part.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello, how can I help?","annotations":[]}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?","annotations":[]}]}}

data: {"type":"response.completed","response":{"id":"resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b","object":"response","created_at":1741476542,"status":"completed","model":"","output":[{"type":"message","id":"msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(15), totalTokens)

		cachedTokens, ok := tokenUsage.CachedInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(2), cachedTokens)

		cacheCreationTokens, ok := tokenUsage.CacheCreationInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), cacheCreationTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(0), reasoningTokens)
	})

	t.Run("model extraction", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		chunks := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"in_progress","role":"assistant","content":[]}}

data: {"type":"response.content_part.added","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

data: {"type":"response.output_text.delta","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"delta":"Hello"}

data: {"type":"response.output_text.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"text":"Hello, how can I help?"}

data: {"type":"response.content_part.done","item_id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello, how can I help?","annotations":[]}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?","annotations":[]}]}}

data: {"type":"response.completed","response":{"id":"resp_67ccd2bed1ec8190b14f964abc0542670bb6a6b452d3795b","object":"response","created_at":1741476542,"status":"completed","model":"","output":[{"type":"message","id":"msg_67ccd2bf17f0819081ff3bb2cf6508e60bb6a6b452d3795b","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, how can I help?"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

data: [DONE]

`)

		translator.buffered = chunks
		translator.extractUsageFromBufferEvent(nil)
		require.Equal(t, "gpt-4o-2024-11-20", translator.streamingResponseModel)
	})

	t.Run("invalid JSON skipped", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		chunks := []byte(`data: {invalid json}

data: {"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(3), outputTokens)
	})

	t.Run("no usage data", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		chunks := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o"}}

data: [DONE]

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		_, inputSet := tokenUsage.InputTokens()
		_, outputSet := tokenUsage.OutputTokens()
		_, totalSet := tokenUsage.TotalTokens()
		_, cachedSet := tokenUsage.CachedInputTokens()
		_, cacheCreationSet := tokenUsage.CacheCreationInputTokens()

		require.False(t, totalSet)
		require.False(t, cachedSet)
		require.False(t, cacheCreationSet)
		require.False(t, inputSet)
		require.False(t, outputSet)
	})

	t.Run("response.incomplete carries usage", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		chunks := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.incomplete","response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-4o-2024-11-20","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":3},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":4},"total_tokens":19}}}

data: [DONE]

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(12), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(7), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(19), totalTokens)

		cachedTokens, ok := tokenUsage.CachedInputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(3), cachedTokens)

		reasoningTokens, ok := tokenUsage.ReasoningTokens()
		require.True(t, ok)
		require.Equal(t, uint32(4), reasoningTokens)
	})

	t.Run("response.failed carries usage when present", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		chunks := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","model":"gpt-4o-2024-11-20","error":{"code":"server_error","message":"boom"},"usage":{"input_tokens":8,"input_tokens_details":{"cached_tokens":0},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":10}}}

data: [DONE]

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(8), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(2), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), totalTokens)
	})

	t.Run("response.failed with nil usage does not panic", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		// Pre-generation failure: no usage object in the response.
		chunks := []byte(`data: {"type":"response.created","response":{"model":"gpt-4o-2024-11-20"}}

data: {"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","model":"gpt-4o-2024-11-20","error":{"code":"invalid_request_error","message":"bad input"}}}

data: [DONE]

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		_, inputSet := tokenUsage.InputTokens()
		_, outputSet := tokenUsage.OutputTokens()
		_, totalSet := tokenUsage.TotalTokens()
		require.False(t, inputSet)
		require.False(t, outputSet)
		require.False(t, totalSet)
	})

	t.Run("response.completed with nil usage does not panic", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Responses)

		// Non-compliant backend: response.completed without usage.
		chunks := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-4o-2024-11-20"}}

data: [DONE]

`)

		translator.buffered = chunks
		tokenUsage := translator.extractUsageFromBufferEvent(nil)

		_, inputSet := tokenUsage.InputTokens()
		_, outputSet := tokenUsage.OutputTokens()
		require.False(t, inputSet)
		require.False(t, outputSet)
	})
}

func TestResponsesOpenAIToOpenAITranslator_ResponseError(t *testing.T) {
	tests := []struct {
		name            string
		responseHeaders map[string]string
		input           io.Reader
		expectHeaders   int
	}{
		{
			name: "non-JSON error response",
			responseHeaders: map[string]string{
				":status":      "503",
				"content-type": "text/plain",
			},
			input:         bytes.NewBuffer([]byte("service unavailable")),
			expectHeaders: 2,
		},
		{
			name: "JSON error response",
			responseHeaders: map[string]string{
				":status":      "400",
				"content-type": "application/json",
			},
			input:         bytes.NewBuffer([]byte(`{"error":{"message":"bad request"}}`)),
			expectHeaders: 0,
		},
		{
			name: "missing content-type header",
			responseHeaders: map[string]string{
				":status": "500",
			},
			input:         bytes.NewBuffer([]byte("internal error")),
			expectHeaders: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewResponsesOpenAIToOpenAITranslator("v1", "")

			headers, body, err := translator.ResponseError(tt.responseHeaders, tt.input)
			require.NoError(t, err)

			if tt.expectHeaders > 0 {
				require.Len(t, headers, tt.expectHeaders)
				// Check that error was converted to OpenAI format
				var errResp openai.Error
				err = json.Unmarshal(body, &errResp)
				require.NoError(t, err)
				require.Equal(t, "error", errResp.Type)
				require.Equal(t, openAIBackendError, errResp.Error.Type)
				require.Equal(t, tt.responseHeaders[":status"], *errResp.Error.Code)
			} else {
				// JSON response or missing content-type should pass through unchanged
				require.Nil(t, body)
			}
		})
	}
}

func TestResponsesOpenAIToOpenAITranslatorWithModelOverride(t *testing.T) {
	t.Run("request body with override", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "gpt-4-turbo").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
		}
		original := []byte(`{"model":"gpt-4o"}`)

		_, _, err := translator.RequestBody(original, req, false)
		require.NoError(t, err)
		require.Equal(t, "gpt-4-turbo", translator.requestModel)
	})

	t.Run("response uses override as fallback", func(t *testing.T) {
		translator := NewResponsesOpenAIToOpenAITranslator("v1", "gpt-4-turbo").(*openAIToOpenAITranslatorV1Responses)

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: false,
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		// Response without model
		resp := openai.Response{
			Model: "",
			Usage: &openai.ResponseUsage{
				InputTokens:  10,
				OutputTokens: 5,
				TotalTokens:  15,
			},
			Text: openai.ResponseTextConfig{Format: openai.ResponseFormatTextConfigUnionParam{OfText: &openai.ResponseFormatTextParam{Type: "text"}}},
		}

		body, err := json.Marshal(resp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4-turbo", responseModel)
	})
}

// Helper types for testing

type failingReader struct{}

func (f *failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

type stubChatCompletionResponseBodyCall struct {
	headers []internalapi.Header
	body    []byte
	usage   metrics.TokenUsage
	model   internalapi.ResponseModel
	err     error
}

type stubChatCompletionTranslator struct {
	requestHeaders     []internalapi.Header
	requestBody        []byte
	requestErr         error
	responseHeaders    []internalapi.Header
	responseHeadersErr error
	responseBodyCalls  []stubChatCompletionResponseBodyCall
	responseBodyIndex  int
	responseErrHeaders []internalapi.Header
	responseErrBody    []byte
	responseErr        error
}

func (s *stubChatCompletionTranslator) RequestBody([]byte, *openai.ChatCompletionRequest, bool) ([]internalapi.Header, []byte, error) {
	return s.requestHeaders, s.requestBody, s.requestErr
}

func (s *stubChatCompletionTranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return s.responseHeaders, s.responseHeadersErr
}

func (s *stubChatCompletionTranslator) ResponseBody(map[string]string, io.Reader, bool, tracingapi.ChatCompletionSpan) ([]internalapi.Header, []byte, metrics.TokenUsage, internalapi.ResponseModel, error) {
	call := s.responseBodyCalls[s.responseBodyIndex]
	s.responseBodyIndex++
	return call.headers, call.body, call.usage, call.model, call.err
}

func (s *stubChatCompletionTranslator) ResponseError(map[string]string, io.Reader) ([]internalapi.Header, []byte, error) {
	return s.responseErrHeaders, s.responseErrBody, s.responseErr
}

func TestResponsesHelper_BoolToInt64(t *testing.T) {
	require.Equal(t, int64(1), boolToInt64(true))
	require.Equal(t, int64(0), boolToInt64(false))
}

func TestResponsesHelper_WithContentLengthHeader(t *testing.T) {
	t.Run("replaces existing content length", func(t *testing.T) {
		headers := withContentLengthHeader([]internalapi.Header{{contentLengthHeaderName, "7"}}, 42)
		require.Len(t, headers, 1)
		require.Equal(t, contentLengthHeaderName, headers[0].Key())
		require.Equal(t, "42", headers[0].Value())
	})

	t.Run("appends content length when missing", func(t *testing.T) {
		headers := withContentLengthHeader([]internalapi.Header{{contentTypeHeaderName, jsonContentType}}, 42)
		require.Len(t, headers, 2)
		require.Equal(t, contentLengthHeaderName, headers[1].Key())
		require.Equal(t, "42", headers[1].Value())
	})
}

func TestResponsesHelper_ConvertEasyInputContent(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		content := convertEasyInputContent(openai.EasyInputMessageContentUnionParam{
			OfString: ptr.To("hello"),
		})
		require.Equal(t, "hello", content.Value)
	})

	t.Run("content parts", func(t *testing.T) {
		content := convertEasyInputContent(openai.EasyInputMessageContentUnionParam{
			OfInputItemContentList: []openai.ResponseInputContentUnionParam{
				{OfInputText: &openai.ResponseInputTextParam{Type: "input_text", Text: "hello"}},
				{OfInputImage: &openai.ResponseInputImageParam{Type: "input_image", ImageURL: "https://example.com/cat.png", Detail: "high"}},
			},
		})

		parts, ok := content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
		require.True(t, ok)
		require.Len(t, parts, 2)
		require.Equal(t, "hello", parts[0].OfText.Text)
		require.Equal(t, "https://example.com/cat.png", parts[1].OfImageURL.ImageURL.URL)
		require.Equal(t, openai.ChatCompletionContentPartImageImageURLDetail("high"), parts[1].OfImageURL.ImageURL.Detail)
	})

	t.Run("empty", func(t *testing.T) {
		content := convertEasyInputContent(openai.EasyInputMessageContentUnionParam{})
		require.Empty(t, content.Value)
	})
}

func TestResponsesHelper_ExtractInputContentTextAndConvertInputItemMessage(t *testing.T) {
	content := []openai.ResponseInputContentUnionParam{
		{OfInputText: &openai.ResponseInputTextParam{Type: "input_text", Text: "hello"}},
		{OfInputImage: &openai.ResponseInputImageParam{Type: "input_image", ImageURL: "https://example.com/cat.png", Detail: "low"}},
		{OfInputText: &openai.ResponseInputTextParam{Type: "input_text", Text: "world"}},
	}

	require.Equal(t, "hello\n\nworld", extractInputContentText(content))

	tests := []struct {
		name  string
		role  string
		check func(t *testing.T, msg openai.ChatCompletionMessageParamUnion)
	}{
		{
			name: "assistant",
			role: openai.ChatMessageRoleAssistant,
			check: func(t *testing.T, msg openai.ChatCompletionMessageParamUnion) {
				require.NotNil(t, msg.OfAssistant)
				require.Equal(t, "hello\n\nworld", msg.OfAssistant.Content.Value)
			},
		},
		{
			name: "system",
			role: openai.ChatMessageRoleSystem,
			check: func(t *testing.T, msg openai.ChatCompletionMessageParamUnion) {
				require.NotNil(t, msg.OfSystem)
				require.Equal(t, "hello\n\nworld", msg.OfSystem.Content.Value)
			},
		},
		{
			name: "developer",
			role: openai.ChatMessageRoleDeveloper,
			check: func(t *testing.T, msg openai.ChatCompletionMessageParamUnion) {
				require.NotNil(t, msg.OfDeveloper)
				require.Equal(t, "hello\n\nworld", msg.OfDeveloper.Content.Value)
			},
		},
		{
			name: "default user",
			role: "custom",
			check: func(t *testing.T, msg openai.ChatCompletionMessageParamUnion) {
				require.NotNil(t, msg.OfUser)
				require.Equal(t, "hello\n\nworld", msg.OfUser.Content.Value)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := convertInputItemMessage(&openai.ResponseInputItemMessageParam{Role: tt.role, Content: content})
			tt.check(t, msg)
		})
	}
}

func TestResponsesViaChatCompletionTranslator_RequestBody_AWSBedrock(t *testing.T) {
	translator := NewResponsesViaChatCompletionTranslator(NewChatCompletionOpenAIToAWSBedrockTranslator(""))

	req := &openai.ResponseRequest{
		Model: "anthropic.claude-3-sonnet-20240229-v1:0",
		Input: openai.ResponseNewParamsInputUnion{
			OfString: ptr.To("Hello from Bedrock responses!"),
		},
	}

	headers, body, err := translator.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.Len(t, headers, 2)
	require.Equal(t, pathHeaderName, headers[0].Key())
	require.Equal(t, "/model/anthropic.claude-3-sonnet-20240229-v1:0/converse", headers[0].Value())
	require.JSONEq(t,
		`{"inferenceConfig":{},"messages":[{"content":[{"text":"Hello from Bedrock responses!"}],"role":"user"}]}`,
		string(body),
	)
}

func TestResponsesViaChatCompletionTranslator_ResponseBody_AWSBedrock_NonStreaming(t *testing.T) {
	translator := NewResponsesViaChatCompletionTranslator(NewChatCompletionOpenAIToAWSBedrockTranslator(""))

	req := &openai.ResponseRequest{
		Model: "anthropic.claude-3-sonnet-20240229-v1:0",
		Input: openai.ResponseNewParamsInputUnion{
			OfString: ptr.To("Hello from Bedrock responses!"),
		},
	}

	_, _, err := translator.RequestBody(nil, req, false)
	require.NoError(t, err)

	_, err = translator.ResponseHeaders(map[string]string{"x-amzn-requestid": "bedrock-resp-123"})
	require.NoError(t, err)

	headers, body, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewBufferString(`{
		"output": {
			"message": {
				"content": [
					{"text": "Hello from Bedrock!"},
					{"toolUse": {"name": "get_weather", "toolUseId": "call_123", "input": {"city": "Berlin"}}}
				],
				"role": "assistant"
			}
		},
		"stopReason": "tool_use",
		"usage": {"inputTokens": 10, "outputTokens": 20, "totalTokens": 30}
	}`), false, nil)
	require.NoError(t, err)
	require.Equal(t, req.Model, responseModel)
	require.Len(t, headers, 1)
	require.Equal(t, contentLengthHeaderName, headers[0].Key())
	require.Equal(t, strconv.Itoa(len(body)), headers[0].Value())

	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)

	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(20), outputTokens)

	totalTokens, ok := tokenUsage.TotalTokens()
	require.True(t, ok)
	require.Equal(t, uint32(30), totalTokens)

	var resp openai.Response
	err = json.Unmarshal(body, &resp)
	require.NoError(t, err)
	require.Equal(t, "response", resp.Object)
	require.Equal(t, "completed", resp.Status)
	require.Equal(t, req.Model, resp.Model)
	require.Equal(t, "bedrock-resp-123", resp.ID)
	require.NotNil(t, resp.Usage)
	require.Equal(t, int64(10), resp.Usage.InputTokens)
	require.Equal(t, int64(20), resp.Usage.OutputTokens)
	require.Equal(t, int64(30), resp.Usage.TotalTokens)
	require.Len(t, resp.Output, 2)
	require.NotNil(t, resp.Output[0].OfFunctionCall)
	require.Equal(t, "call_123", resp.Output[0].OfFunctionCall.CallID)
	require.Equal(t, "get_weather", resp.Output[0].OfFunctionCall.Name)
	require.JSONEq(t, `{"city":"Berlin"}`, resp.Output[0].OfFunctionCall.Arguments)
	require.NotNil(t, resp.Output[1].OfOutputMessage)
	require.Equal(t, "assistant", resp.Output[1].OfOutputMessage.Role)
	require.Len(t, resp.Output[1].OfOutputMessage.Content.OfContentArray, 1)
	require.Equal(t, "Hello from Bedrock!", resp.Output[1].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
}

func TestResponsesViaChatCompletionTranslator_ResponseBody_DefensiveChecks(t *testing.T) {
	translator := NewResponsesViaChatCompletionTranslator(&stubChatCompletionTranslator{
		responseBodyCalls: []stubChatCompletionResponseBodyCall{{}},
	})

	_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{
		Model: "anthropic.claude-3-sonnet-20240229-v1:0",
		Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("hello")},
	}, false)
	require.NoError(t, err)

	_, _, _, responseModel, err := translator.ResponseBody(nil, nil, false, nil)
	require.ErrorContains(t, err, "body is nil")
	require.Equal(t, internalapi.ResponseModel("anthropic.claude-3-sonnet-20240229-v1:0"), responseModel)

	_, _, _, responseModel, err = translator.ResponseBody(nil, bytes.NewReader(nil), false, nil)
	require.ErrorContains(t, err, "empty response body")
	require.Equal(t, internalapi.ResponseModel("anthropic.claude-3-sonnet-20240229-v1:0"), responseModel)
}

func TestResponsesViaChatCompletionTranslator_ResponseBody_Streaming(t *testing.T) {
	createdAt := openai.JSONUNIXTime(time.Unix(1741476542, 0))
	firstChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-bedrock-stream-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index: 0,
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{Content: ptr.To("Hello")},
		}},
	})
	require.NoError(t, err)

	finishChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-bedrock-stream-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index:        0,
			FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
		}},
	})
	require.NoError(t, err)

	usageChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-bedrock-stream-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Usage: &openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	})
	require.NoError(t, err)

	firstLine := append([]byte("data: "), firstChunk...)
	finishLine := append([]byte("data: "), finishChunk...)
	usageLine := append([]byte("data: "), usageChunk...)

	inner := &stubChatCompletionTranslator{
		responseBodyCalls: []stubChatCompletionResponseBodyCall{
			{
				headers: []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}},
				body:    firstLine,
			},
			{
				headers: []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}},
				body:    bytes.Join([][]byte{[]byte(""), finishLine, usageLine, []byte("data: [DONE]"), []byte("")}, []byte("\n")),
				usage: func() metrics.TokenUsage {
					var usage metrics.TokenUsage
					usage.SetInputTokens(10)
					usage.SetOutputTokens(20)
					usage.SetTotalTokens(30)
					return usage
				}(),
			},
		},
	}
	translator := NewResponsesViaChatCompletionTranslator(inner)

	_, _, err = translator.RequestBody(nil, &openai.ResponseRequest{
		Model:  "anthropic.claude-3-sonnet-20240229-v1:0",
		Stream: true,
		Input:  openai.ResponseNewParamsInputUnion{OfString: ptr.To("hello")},
	}, false)
	require.NoError(t, err)

	headers, out, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte("ignored")), false, nil)
	require.NoError(t, err)
	require.Len(t, headers, 1)
	require.Equal(t, contentTypeHeaderName, headers[0].Key())
	require.Empty(t, out)
	require.Equal(t, internalapi.ResponseModel("anthropic.claude-3-sonnet-20240229-v1:0"), responseModel)
	_, ok := tokenUsage.InputTokens()
	require.False(t, ok)

	headers, out, tokenUsage, responseModel, err = translator.ResponseBody(nil, bytes.NewReader([]byte("ignored")), true, nil)
	require.NoError(t, err)
	require.Len(t, headers, 1)
	require.Equal(t, contentTypeHeaderName, headers[0].Key())
	require.Equal(t, internalapi.ResponseModel("anthropic.claude-3-sonnet-20240229-v1:0"), responseModel)

	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)

	events := parseSSEEventsFromBytes(out)
	require.Equal(t, []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, responsesEventTypes(events))
	var completed openai.ResponseCompletedEvent
	err = json.Unmarshal([]byte(events[len(events)-1].data), &completed)
	require.NoError(t, err)
	require.Equal(t, "Hello", completed.Response.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
}

func TestResponsesViaChatCompletionTranslator_ResponseError(t *testing.T) {
	inner := &stubChatCompletionTranslator{
		responseErrHeaders: []internalapi.Header{{contentTypeHeaderName, jsonContentType}},
		responseErrBody:    []byte(`{"type":"error"}`),
	}
	translator := NewResponsesViaChatCompletionTranslator(inner)

	headers, body, err := translator.ResponseError(map[string]string{statusHeaderName: "500"}, bytes.NewReader([]byte("upstream error")))
	require.NoError(t, err)
	require.Len(t, headers, 1)
	require.Equal(t, contentTypeHeaderName, headers[0].Key())
	require.JSONEq(t, `{"type":"error"}`, string(body))
}

func TestChatCompletionStreamToResponsesConverter_ProcessLine(t *testing.T) {
	converter := &chatCompletionStreamToResponsesConverter{requestModel: "anthropic.claude-3-sonnet-20240229-v1:0"}
	createdAt := openai.JSONUNIXTime(time.Unix(1741476542, 0))

	firstChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-bedrock-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index: 0,
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{Content: ptr.To("Hello")},
		}},
	})
	require.NoError(t, err)

	finishChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-bedrock-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index:        0,
			FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
		}},
	})
	require.NoError(t, err)

	usageChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-bedrock-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Usage: &openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
		Choices: []openai.ChatCompletionResponseChunkChoice{},
	})
	require.NoError(t, err)

	var output []byte
	var usage *openai.Usage
	for _, line := range [][]byte{firstChunk, finishChunk, usageChunk} {
		converted, chunkUsage := converter.processLine(append([]byte("data: "), line...))
		output = append(output, converted...)
		if chunkUsage != nil {
			usage = chunkUsage
		}
	}

	require.NotNil(t, usage)
	require.Equal(t, 10, usage.PromptTokens)
	require.Equal(t, 20, usage.CompletionTokens)
	require.Equal(t, 30, usage.TotalTokens)

	events := parseSSEEventsFromBytes(output)
	require.Len(t, events, 9)
	require.Equal(t, []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, responsesEventTypes(events))

	var completed openai.ResponseCompletedEvent
	err = json.Unmarshal([]byte(events[len(events)-1].data), &completed)
	require.NoError(t, err)
	require.Equal(t, "response.completed", completed.Type)
	require.Equal(t, "completed", completed.Response.Status)
	require.Equal(t, "anthropic.claude-3-sonnet-20240229-v1:0", completed.Response.Model)
	require.Equal(t, "chatcmpl-bedrock-123", completed.Response.ID)
	require.NotNil(t, completed.Response.Usage)
	require.Equal(t, int64(10), completed.Response.Usage.InputTokens)
	require.Equal(t, int64(20), completed.Response.Usage.OutputTokens)
	require.Equal(t, int64(30), completed.Response.Usage.TotalTokens)
	require.Len(t, completed.Response.Output, 1)
	require.NotNil(t, completed.Response.Output[0].OfOutputMessage)
	require.Len(t, completed.Response.Output[0].OfOutputMessage.Content.OfContentArray, 1)
	require.Equal(t, "Hello", completed.Response.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
}

func TestChatCompletionStreamToResponsesConverter_ProcessLine_ToolCallsAndIgnoredLines(t *testing.T) {
	converter := &chatCompletionStreamToResponsesConverter{requestModel: "anthropic.claude-3-sonnet-20240229-v1:0"}
	createdAt := openai.JSONUNIXTime(time.Unix(1741476542, 0))

	ignored, usage := converter.processLine([]byte("event: response.created"))
	require.Nil(t, ignored)
	require.Nil(t, usage)

	ignored, usage = converter.processLine([]byte("data: {invalid json}"))
	require.Nil(t, ignored)
	require.Nil(t, usage)

	startChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-tool-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index: 0,
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
				ID: ptr.To("call_123"),
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name: "get_weather",
				},
				Type:  openai.ChatCompletionMessageToolCallTypeFunction,
				Index: 0,
			}}},
		}},
	})
	require.NoError(t, err)

	argsChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-tool-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index: 0,
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Arguments: `{"city":"Berlin"}`,
				},
				Type:  openai.ChatCompletionMessageToolCallTypeFunction,
				Index: 0,
			}}},
		}},
	})
	require.NoError(t, err)

	finishChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-tool-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Choices: []openai.ChatCompletionResponseChunkChoice{{
			Index:        0,
			FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
		}},
	})
	require.NoError(t, err)

	usageChunk, err := json.Marshal(openai.ChatCompletionResponseChunk{
		ID:      "chatcmpl-tool-123",
		Created: createdAt,
		Model:   "anthropic.claude-3-sonnet-20240229-v1:0",
		Usage: &openai.Usage{
			PromptTokens:     3,
			CompletionTokens: 4,
			TotalTokens:      7,
		},
	})
	require.NoError(t, err)

	var output []byte
	for _, line := range [][]byte{startChunk, argsChunk, finishChunk, usageChunk} {
		converted, _ := converter.processLine(append([]byte("data: "), line...))
		output = append(output, converted...)
	}

	ignored, usage = converter.processLine([]byte("data: [DONE]"))
	require.Nil(t, ignored)
	require.Nil(t, usage)

	events := parseSSEEventsFromBytes(output)
	require.Equal(t, []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	}, responsesEventTypes(events))

	var completed openai.ResponseCompletedEvent
	err = json.Unmarshal([]byte(events[len(events)-1].data), &completed)
	require.NoError(t, err)
	require.Len(t, completed.Response.Output, 1)
	require.NotNil(t, completed.Response.Output[0].OfFunctionCall)
	require.Equal(t, "call_123", completed.Response.Output[0].OfFunctionCall.CallID)
	require.Equal(t, "get_weather", completed.Response.Output[0].OfFunctionCall.Name)
	require.JSONEq(t, `{"city":"Berlin"}`, completed.Response.Output[0].OfFunctionCall.Arguments)
}

func TestResponsesRequestToChatCompletionRequest_InputItemsAndTools(t *testing.T) {
	req := &openai.ResponseRequest{
		Model:        "gpt-4o",
		Instructions: "Follow the instructions",
		Stream:       true,
		Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
			{OfMessage: &openai.EasyInputMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				Content: openai.EasyInputMessageContentUnionParam{OfInputItemContentList: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Type: "input_text", Text: "Previous answer"}},
					{OfInputText: &openai.ResponseInputTextParam{Type: "input_text", Text: "More context"}},
				}},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: []openai.ResponseInputContentUnionParam{{
					OfInputText: &openai.ResponseInputTextParam{Type: "input_text", Text: "What is the weather?"},
				}},
			}},
			{OfOutputMessage: &openai.ResponseOutputMessage{
				ID:     "msg_prev",
				Role:   "assistant",
				Type:   "message",
				Status: "completed",
				Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
					{OfOutputText: &openai.ResponseOutputTextParam{Type: "output_text", Text: "Tool result received"}},
					{OfRefusal: &openai.ResponseOutputRefusalParam{Type: "refusal", Refusal: "But I need more input"}},
				}},
			}},
			{OfFunctionCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_1",
				Name:      "get_weather",
				Arguments: `{"city":"Berlin"}`,
				Type:      "function_call",
			}},
			{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_1",
				Type:   "function_call_output",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr.To(`{"temperature":"72F"}`),
				},
			}},
		}},
		Tools: []openai.ResponseToolUnion{{
			OfFunction: &openai.FunctionToolParam{
				Type:        "function",
				Name:        "get_weather",
				Description: "Get the weather",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		}},
	}

	chatReq := responsesRequestToChatCompletionRequest(req, req.Model)
	require.Equal(t, req.Model, chatReq.Model)
	require.True(t, chatReq.Stream)
	require.NotNil(t, chatReq.StreamOptions)
	require.True(t, chatReq.StreamOptions.IncludeUsage)
	require.Len(t, chatReq.Messages, 5)
	require.NotNil(t, chatReq.Messages[0].OfSystem)
	require.Equal(t, "Follow the instructions", chatReq.Messages[0].OfSystem.Content.Value)
	require.NotNil(t, chatReq.Messages[1].OfAssistant)
	require.Equal(t, "Previous answer\n\nMore context", chatReq.Messages[1].OfAssistant.Content.Value)
	require.NotNil(t, chatReq.Messages[2].OfUser)
	userContent, ok := chatReq.Messages[2].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
	require.True(t, ok)
	require.Len(t, userContent, 1)
	require.Equal(t, "What is the weather?", userContent[0].OfText.Text)
	require.NotNil(t, chatReq.Messages[3].OfAssistant)
	require.Equal(t, "Tool result received\n\nBut I need more input", chatReq.Messages[3].OfAssistant.Content.Value)
	require.Len(t, chatReq.Messages[3].OfAssistant.ToolCalls, 1)
	require.Equal(t, "call_1", ptr.Deref(chatReq.Messages[3].OfAssistant.ToolCalls[0].ID, ""))
	require.Equal(t, "get_weather", chatReq.Messages[3].OfAssistant.ToolCalls[0].Function.Name)
	require.JSONEq(t, `{"city":"Berlin"}`, chatReq.Messages[3].OfAssistant.ToolCalls[0].Function.Arguments)
	require.NotNil(t, chatReq.Messages[4].OfTool)
	require.Equal(t, "call_1", chatReq.Messages[4].OfTool.ToolCallID)
	require.JSONEq(t, `{"temperature":"72F"}`, chatReq.Messages[4].OfTool.Content.Value.(string))
	require.Len(t, chatReq.Tools, 1)
	require.Equal(t, openai.ToolTypeFunction, chatReq.Tools[0].Type)
	require.NotNil(t, chatReq.Tools[0].Function)
	require.Equal(t, "get_weather", chatReq.Tools[0].Function.Name)
}

func TestResponsesChatCompletionToResponse_NonStreaming(t *testing.T) {
	resp := chatCompletionToResponse(&openai.ChatCompletionResponse{
		ID:      "chatcmpl-tool-123",
		Object:  "chat.completion",
		Created: openai.JSONUNIXTime(time.Unix(1741476542, 0)),
		Usage: openai.Usage{
			PromptTokens:     11,
			CompletionTokens: 7,
			TotalTokens:      18,
		},
		Choices: []openai.ChatCompletionResponseChoice{{
			Index:        0,
			FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:    "assistant",
				Content: ptr.To("I will call a tool."),
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
					ID: ptr.To("call_123"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "get_weather",
						Arguments: `{"city":"Berlin"}`,
					},
					Type: openai.ChatCompletionMessageToolCallTypeFunction,
				}},
			},
		}},
	}, "gpt-4o-mini")

	require.Equal(t, "chatcmpl-tool-123", resp.ID)
	require.Equal(t, "response", resp.Object)
	require.Equal(t, "gpt-4o-mini", resp.Model)
	require.Equal(t, "completed", resp.Status)
	require.NotNil(t, resp.Usage)
	require.Equal(t, int64(11), resp.Usage.InputTokens)
	require.Equal(t, int64(7), resp.Usage.OutputTokens)
	require.Equal(t, int64(18), resp.Usage.TotalTokens)
	require.Len(t, resp.Output, 2)
	require.NotNil(t, resp.Output[0].OfFunctionCall)
	require.Equal(t, "call_123", resp.Output[0].OfFunctionCall.ID)
	require.Equal(t, "call_123", resp.Output[0].OfFunctionCall.CallID)
	require.Equal(t, "get_weather", resp.Output[0].OfFunctionCall.Name)
	require.JSONEq(t, `{"city":"Berlin"}`, resp.Output[0].OfFunctionCall.Arguments)
	require.NotNil(t, resp.Output[1].OfOutputMessage)
	require.Equal(t, "assistant", resp.Output[1].OfOutputMessage.Role)
	require.Len(t, resp.Output[1].OfOutputMessage.Content.OfContentArray, 1)
	require.Equal(t, "I will call a tool.", resp.Output[1].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
}

func TestResponsesChatCompletionToResponse_NilResponse(t *testing.T) {
	require.Nil(t, chatCompletionToResponse(nil, "gpt-4o-mini"))
}

func TestChatCompletionStreamToResponsesConverter_BuildFinalResponse_SortsToolCalls(t *testing.T) {
	converter := &chatCompletionStreamToResponsesConverter{
		responseID:   "resp_123",
		requestModel: "gpt-4o-mini",
		toolCalls: map[int64]*streamingToolCallState{
			2: {id: "call_2", name: "second"},
			1: {id: "call_1", name: "first"},
		},
	}
	converter.toolCalls[2].arguments.WriteString(`{"order":2}`)
	converter.toolCalls[1].arguments.WriteString(`{"order":1}`)

	resp := converter.buildFinalResponse("completed", nil)

	require.Len(t, resp.Output, 2)
	require.Equal(t, "call_1", resp.Output[0].OfFunctionCall.CallID)
	require.Equal(t, "first", resp.Output[0].OfFunctionCall.Name)
	require.JSONEq(t, `{"order":1}`, resp.Output[0].OfFunctionCall.Arguments)
	require.Equal(t, "call_2", resp.Output[1].OfFunctionCall.CallID)
	require.Equal(t, "second", resp.Output[1].OfFunctionCall.Name)
	require.JSONEq(t, `{"order":2}`, resp.Output[1].OfFunctionCall.Arguments)
}

func responsesEventTypes(events []sseEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.eventType)
	}
	return types
}
