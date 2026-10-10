// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func responsesOpenAIRequestBody(t *testing.T, override string, req *openai.ResponseRequest) ([]internalapi.Header, openai.ChatCompletionRequest) {
	t.Helper()
	translator := NewResponsesOpenAIToChatCompletionTranslator("v1", override)
	headers, body, err := translator.RequestBody(nil, req, false)
	require.NoError(t, err)
	require.NotNil(t, body)
	var result openai.ChatCompletionRequest
	require.NoError(t, json.Unmarshal(body, &result))
	return headers, result
}

func TestResponsesOpenAIToChatCompletion_RequestBody(t *testing.T) {
	t.Run("simple string input", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		headers, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.Equal(t, "gpt-4o", chatReq.Model)
		require.Len(t, chatReq.Messages, 1)
		require.NotNil(t, chatReq.Messages[0].OfUser)
		require.Equal(t, openai.ChatMessageRoleUser, chatReq.Messages[0].OfUser.Role)
		require.Equal(t, "Hello", chatReq.Messages[0].OfUser.Content.Value)
		requirePathHeader(t, headers, "/v1/chat/completions")
	})

	t.Run("input items with user and assistant messages", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{
				OfInputItemList: []openai.ResponseInputItemUnionParam{
					{OfMessage: &openai.EasyInputMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.EasyInputMessageContentUnionParam{OfString: ptr.To("Hi there")}}},
					{OfMessage: &openai.EasyInputMessageParam{Role: openai.ChatMessageRoleAssistant, Content: openai.EasyInputMessageContentUnionParam{OfString: ptr.To("Hello!")}}},
					{OfMessage: &openai.EasyInputMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.EasyInputMessageContentUnionParam{OfString: ptr.To("How are you?")}}},
				},
			},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.Len(t, chatReq.Messages, 3)
		require.NotNil(t, chatReq.Messages[0].OfUser)
		require.Equal(t, "Hi there", chatReq.Messages[0].OfUser.Content.Value)
		require.NotNil(t, chatReq.Messages[1].OfAssistant)
		require.Equal(t, "Hello!", chatReq.Messages[1].OfAssistant.Content.Value)
		require.NotNil(t, chatReq.Messages[2].OfUser)
		require.Equal(t, "How are you?", chatReq.Messages[2].OfUser.Content.Value)
	})

	t.Run("instructions become system message", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model:        "gpt-4o",
			Instructions: "You are a helpful assistant",
			Input:        openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.GreaterOrEqual(t, len(chatReq.Messages), 2)
		var foundSystem bool
		for _, msg := range chatReq.Messages {
			if msg.OfSystem != nil {
				foundSystem = true
				require.Equal(t, "You are a helpful assistant", msg.OfSystem.Content.Value)
			}
			if msg.OfDeveloper != nil {
				foundSystem = true
				require.Equal(t, "You are a helpful assistant", msg.OfDeveloper.Content.Value)
			}
		}
		require.True(t, foundSystem, "instructions should be translated to a system or developer message")
	})

	t.Run("tools conversion", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Get the weather")},
			Tools: []openai.ResponseToolUnion{
				{OfFunction: &openai.FunctionToolParam{
					Name: "get_weather", Description: "Get weather for a location",
					Parameters: map[string]any{"type": "object", "properties": map[string]any{"location": map[string]any{"type": "string", "description": "City name"}}, "required": []any{"location"}},
					Type:       "function",
				}},
			},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.Len(t, chatReq.Tools, 1)
		require.Equal(t, openai.ToolTypeFunction, chatReq.Tools[0].Type)
		require.NotNil(t, chatReq.Tools[0].Function)
		require.Equal(t, "get_weather", chatReq.Tools[0].Function.Name)
		require.Equal(t, "Get weather for a location", chatReq.Tools[0].Function.Description)
	})

	t.Run("model override", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "gpt-4-turbo", req)
		require.Equal(t, "gpt-4-turbo", chatReq.Model)
	})

	t.Run("streaming sets stream and stream_options", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o", Stream: true,
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.True(t, chatReq.Stream)
		require.NotNil(t, chatReq.StreamOptions)
		require.True(t, chatReq.StreamOptions.IncludeUsage)
	})

	t.Run("path header set correctly", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		headers, _ := responsesOpenAIRequestBody(t, "", req)
		requirePathHeader(t, headers, "/v1/chat/completions")
	})

	t.Run("temperature and top_p pass through", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o", Temperature: ptr.To(0.7), TopP: ptr.To(0.9),
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.NotNil(t, chatReq.Temperature)
		require.InDelta(t, 0.7, *chatReq.Temperature, 0.001)
		require.NotNil(t, chatReq.TopP)
		require.InDelta(t, 0.9, *chatReq.TopP, 0.001)
	})

	t.Run("max_output_tokens maps to max_completion_tokens", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o", MaxOutputTokens: ptr.To(int64(1024)),
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.NotNil(t, chatReq.MaxCompletionTokens)
		require.Equal(t, int64(1024), *chatReq.MaxCompletionTokens)
	})

	t.Run("input with image content", func(t *testing.T) {
		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{
				OfInputItemList: []openai.ResponseInputItemUnionParam{
					{OfMessage: &openai.EasyInputMessageParam{
						Role: openai.ChatMessageRoleUser,
						Content: openai.EasyInputMessageContentUnionParam{
							OfInputItemContentList: []openai.ResponseInputContentUnionParam{
								{OfInputText: &openai.ResponseInputTextParam{Text: "What is in this image?", Type: "input_text"}},
								{OfInputImage: &openai.ResponseInputImageParam{ImageURL: "https://example.com/image.jpg", Type: "input_image", Detail: "auto"}},
							},
						},
					}},
				},
			},
		}
		_, chatReq := responsesOpenAIRequestBody(t, "", req)

		require.Len(t, chatReq.Messages, 1)
		require.NotNil(t, chatReq.Messages[0].OfUser)
	})
}

func TestResponsesOpenAIToChatCompletion_ResponseBody(t *testing.T) {
	t.Run("non-streaming text response", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hello"),
			},
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		chatCompletionResp := openai.ChatCompletionResponse{
			ID:     "chatcmpl-abc123",
			Model:  "gpt-4o-2024-11-20",
			Object: "chat.completion",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index:        0,
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    openai.ChatMessageRoleAssistant,
						Content: ptr.To("Hello! How can I help you?"),
					},
				},
			},
			Usage: openai.Usage{
				PromptTokens:     10,
				CompletionTokens: 8,
				TotalTokens:      18,
			},
		}

		body, err := json.Marshal(chatCompletionResp)
		require.NoError(t, err)

		_, newBody, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		var resp openai.Response
		require.NoError(t, json.Unmarshal(newBody, &resp))
		require.Equal(t, "response", resp.Object)
		require.Equal(t, "completed", resp.Status)
		require.Equal(t, "gpt-4o-2024-11-20", resp.Model)
		require.Equal(t, "chatcmpl-abc123", resp.ID)
		require.Len(t, resp.Output, 1)
		require.NotNil(t, resp.Output[0].OfOutputMessage)
		require.Equal(t, "assistant", resp.Output[0].OfOutputMessage.Role)
		require.Equal(t, "completed", resp.Output[0].OfOutputMessage.Status)
		require.NotNil(t, resp.Output[0].OfOutputMessage.Content.OfContentArray)
		require.Len(t, resp.Output[0].OfOutputMessage.Content.OfContentArray, 1)
		require.NotNil(t, resp.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText)
		require.Equal(t, "Hello! How can I help you?", resp.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
		require.NotNil(t, resp.Usage)
		require.Equal(t, int64(10), resp.Usage.InputTokens)
		require.Equal(t, int64(8), resp.Usage.OutputTokens)
		require.Equal(t, int64(18), resp.Usage.TotalTokens)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(8), outputTokens)

		totalTokens, ok := tokenUsage.TotalTokens()
		require.True(t, ok)
		require.Equal(t, uint32(18), totalTokens)
	})

	t.Run("non-streaming tool call response", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Get the weather"),
			},
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		chatCompletionResp := openai.ChatCompletionResponse{
			ID:     "chatcmpl-tool123",
			Model:  "gpt-4o-2024-11-20",
			Object: "chat.completion",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index:        0,
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: openai.ChatMessageRoleAssistant,
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("call_xyz"),
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_weather",
									Arguments: `{"location":"NYC"}`,
								},
							},
						},
					},
				},
			},
			Usage: openai.Usage{
				PromptTokens:     15,
				CompletionTokens: 12,
				TotalTokens:      27,
			},
		}

		body, err := json.Marshal(chatCompletionResp)
		require.NoError(t, err)

		_, mutatedBody, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)
		require.NotNil(t, mutatedBody)

		var resp openai.Response
		require.NoError(t, json.Unmarshal(mutatedBody, &resp))
		require.Equal(t, "response", resp.Object)
		require.Equal(t, "completed", resp.Status)
		require.Len(t, resp.Output, 1)
		require.NotNil(t, resp.Output[0].OfFunctionCall)
		require.Equal(t, "call_xyz", resp.Output[0].OfFunctionCall.CallID)
		require.Equal(t, "get_weather", resp.Output[0].OfFunctionCall.Name)
		require.JSONEq(t, `{"location":"NYC"}`, resp.Output[0].OfFunctionCall.Arguments)
		require.Equal(t, "function_call", resp.Output[0].OfFunctionCall.Type)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(12), outputTokens)
	})

	t.Run("model fallback", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

		req := &openai.ResponseRequest{
			Model: "gpt-4o",
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hello"),
			},
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		chatCompletionResp := openai.ChatCompletionResponse{
			ID:     "chatcmpl-fallback",
			Model:  "",
			Object: "chat.completion",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index:        0,
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    openai.ChatMessageRoleAssistant,
						Content: ptr.To("Hi"),
					},
				},
			},
			Usage: openai.Usage{
				PromptTokens:     5,
				CompletionTokens: 1,
				TotalTokens:      6,
			},
		}

		body, err := json.Marshal(chatCompletionResp)
		require.NoError(t, err)

		_, newBody, _, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), false, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", responseModel)

		var resp openai.Response
		require.NoError(t, json.Unmarshal(newBody, &resp))
		require.Equal(t, "gpt-4o", resp.Model)
	})

	t.Run("streaming response", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

		req := &openai.ResponseRequest{
			Model:  "gpt-4o",
			Stream: true,
			Input: openai.ResponseNewParamsInputUnion{
				OfString: ptr.To("Hi"),
			},
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		sseChunks := `data: {"id":"chatcmpl-abc","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}],"model":"gpt-4o-2024-11-20","object":"chat.completion.chunk"}

data: {"id":"chatcmpl-abc","choices":[{"index":0,"delta":{"content":" world"}}],"model":"gpt-4o-2024-11-20","object":"chat.completion.chunk"}

data: {"id":"chatcmpl-abc","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"model":"gpt-4o-2024-11-20","object":"chat.completion.chunk"}

data: {"id":"chatcmpl-abc","choices":[],"model":"gpt-4o-2024-11-20","object":"chat.completion.chunk","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`
		_, newBody, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(sseChunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-2024-11-20", responseModel)

		outputStr := string(newBody)
		require.Contains(t, outputStr, "event: response.created")
		require.Contains(t, outputStr, "event: response.output_text.delta")
		require.Contains(t, outputStr, "event: response.output_item.done")
		require.Contains(t, outputStr, `"object":"response"`)

		inputTokens, ok := tokenUsage.InputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(10), inputTokens)

		outputTokens, ok := tokenUsage.OutputTokens()
		require.True(t, ok)
		require.Equal(t, uint32(5), outputTokens)
	})

	t.Run("streaming tool call", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")
		_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{
			Model: "gpt-4o", Stream: true,
			Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Get the weather")},
		}, false)
		require.NoError(t, err)

		chunks := `data: {"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-weather","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}` + "\n\n" +
			`data: {"id":"chatcmpl-tool","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Berlin\"}"}}]}}]}` + "\n\n" +
			`data: {"id":"chatcmpl-tool","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
			`data: {"id":"chatcmpl-tool","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18}}` + "\n\n"

		_, body, _, model, err := translator.ResponseBody(nil, bytes.NewReader([]byte(chunks)), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", model)

		events := parseSSEEventsFromBytes(body)
		require.Len(t, events, 8)
		require.Equal(t, "response.created", events[0].eventType)
		require.Equal(t, "response.in_progress", events[1].eventType)
		require.Equal(t, "response.output_item.added", events[2].eventType)
		require.Equal(t, "response.function_call_arguments.delta", events[3].eventType)
		require.Equal(t, "response.function_call_arguments.delta", events[4].eventType)
		require.JSONEq(t, `{"type":"response.function_call_arguments.done","item_id":"call-weather","name":"get_weather","arguments":"{\"city\":\"Berlin\"}","output_index":0,"sequence_number":6}`, events[5].data)
		require.Equal(t, "response.output_item.done", events[6].eventType)
		require.Equal(t, "response.completed", events[7].eventType)

		var completed openai.ResponseCompletedEvent
		require.NoError(t, json.Unmarshal([]byte(events[7].data), &completed))
		require.Equal(t, "completed", completed.Response.Status)
		require.Len(t, completed.Response.Output, 1)
		require.Equal(t, "call-weather", completed.Response.Output[0].OfFunctionCall.CallID)
		require.JSONEq(t, `{"city":"Berlin"}`, completed.Response.Output[0].OfFunctionCall.Arguments)
		require.Equal(t, int64(12), completed.Response.Usage.InputTokens)
		require.Equal(t, int64(6), completed.Response.Usage.OutputTokens)
	})
}

func TestResponsesOpenAIToChatCompletion_ResponseError(t *testing.T) {
	t.Run("JSON error passthrough", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

		respHeaders := map[string]string{
			":status":      "400",
			"content-type": "application/json",
		}
		errorBody := []byte(`{"error":{"message":"Invalid model","type":"invalid_request_error","code":"model_not_found"}}`)

		headers, body, err := translator.ResponseError(respHeaders, bytes.NewReader(errorBody))
		require.NoError(t, err)

		if body != nil {
			var errResp openai.Error
			err = json.Unmarshal(body, &errResp)
			require.NoError(t, err)
			require.Equal(t, "error", errResp.Type)
		} else {
			require.Nil(t, headers)
		}
	})

	t.Run("non-JSON error wrapped in error format", func(t *testing.T) {
		translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

		respHeaders := map[string]string{
			":status":      "503",
			"content-type": "text/plain",
		}
		errorBody := []byte("service unavailable")

		headers, body, err := translator.ResponseError(respHeaders, bytes.NewReader(errorBody))
		require.NoError(t, err)
		require.NotNil(t, body)
		require.NotNil(t, headers)

		var errResp openai.Error
		err = json.Unmarshal(body, &errResp)
		require.NoError(t, err)
		require.Equal(t, "error", errResp.Type)
		require.Equal(t, "service unavailable", errResp.Error.Message)
	})
}

func TestResponsesOpenAIToChatCompletion_ResponseHeaders(t *testing.T) {
	translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")

	headers, err := translator.ResponseHeaders(map[string]string{
		"content-type": "application/json",
		":status":      "200",
	})
	require.NoError(t, err)
	require.Nil(t, headers)
}
