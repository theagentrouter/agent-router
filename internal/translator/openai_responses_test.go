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

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
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
