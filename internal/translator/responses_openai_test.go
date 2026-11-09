// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestResponsesOpenAIToChatCompletion_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name, request, expected, override string
	}{
		{"string", `{"input":"Hello"}`, `{"messages":[{"role":"user","content":"Hello"}]}`, ""},
		{"conversation", `{"input":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"},{"role":"user","content":"How are you?"}]}`, `{"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"},{"role":"user","content":"How are you?"}]}`, ""},
		{"instructions", `{"instructions":"Be helpful","input":"Hello"}`, `{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"Hello"}]}`, ""},
		{"tools", `{"tools":[{"type":"function","name":"weather","description":"Get weather","strict":false,"parameters":{"type":"object"}}]}`, `{"tools":[{"type":"function","function":{"name":"weather","description":"Get weather","parameters":{"type":"object"}}}]}`, ""},
		{"model override", `{"input":"Hello"}`, `{"model":"backend-model"}`, "backend-model"},
		{"stream", `{"stream":true}`, `{"stream":true,"stream_options":{"include_usage":true}}`, ""},
		{"sampling and limits", `{"temperature":0.7,"top_p":0.9,"max_output_tokens":1024}`, `{"temperature":0.7,"top_p":0.9,"max_completion_tokens":1024}`, ""},
		{"image", `{"input":[{"role":"user","content":[{"type":"input_text","text":"Describe this"},{"type":"input_image","image_url":"https://example.com/image.jpg","detail":"high"}]}]}`, `{"messages":[{"role":"user","content":[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"https://example.com/image.jpg","detail":"high"}}]}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ResponseRequest
			require.NoError(t, json.Unmarshal([]byte(tc.request), &req))
			req.Model = "gpt-4o"
			translator := NewResponsesOpenAIToChatCompletionTranslator("v1", tc.override)
			headers, body, err := translator.RequestBody(nil, &req, false)
			require.NoError(t, err)
			requirePathHeader(t, headers, "/v1/chat/completions")
			requireJSONFields(t, tc.expected, body)
			var chatReq openai.ChatCompletionRequest
			require.NoError(t, json.Unmarshal(body, &chatReq))
			model := req.Model
			if tc.override != "" {
				model = tc.override
			}
			require.Equal(t, model, chatReq.Model)
		})
	}
}

func TestResponsesOpenAIToChatCompletion_ResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name, model, finish, message string
	}{
		{"text", "gpt-4o-2024-11-20", "stop", `{"role":"assistant","content":"Hello"}`},
		{"tool", "gpt-4o-2024-11-20", "tool_calls", `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Berlin\"}"}}]}`},
		{"model fallback", "", "stop", `{"role":"assistant","content":"Hello"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "gpt-4o"}, false)
			require.NoError(t, err)
			body := fmt.Sprintf(`{"id":"chat_1","model":%q,"choices":[{"index":0,"finish_reason":%q,"message":%s}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, tc.model, tc.finish, tc.message)
			_, translated, usage, model, err := translator.ResponseBody(nil, strings.NewReader(body), true, nil)
			require.NoError(t, err)
			if tc.model == "" {
				tc.model = "gpt-4o"
			}
			require.Equal(t, tc.model, model)
			var resp openai.Response
			require.NoError(t, json.Unmarshal(translated, &resp))
			require.Equal(t, "chat_1", resp.ID)
			require.Equal(t, "response", resp.Object)
			require.Equal(t, "completed", resp.Status)
			require.Equal(t, model, resp.Model)
			require.Len(t, resp.Output, 1)
			if tc.finish == "tool_calls" {
				tool := resp.Output[0].OfFunctionCall
				require.NotNil(t, tool)
				require.Equal(t, "function_call", tool.Type)
				require.Equal(t, "call_1", tool.ID)
				require.Equal(t, "call_1", tool.CallID)
				require.Equal(t, "weather", tool.Name)
				require.Equal(t, "completed", tool.Status)
				require.JSONEq(t, `{"city":"Berlin"}`, tool.Arguments)
			} else {
				message := resp.Output[0].OfOutputMessage
				require.NotNil(t, message)
				require.Equal(t, "msg_chat_1", message.ID)
				require.Equal(t, "assistant", message.Role)
				require.Equal(t, "completed", message.Status)
				require.Len(t, message.Content.OfContentArray, 1)
				require.Equal(t, "Hello", message.Content.OfContentArray[0].OfOutputText.Text)
			}
			require.Equal(t, int64(10), resp.Usage.InputTokens)
			require.Equal(t, int64(5), resp.Usage.OutputTokens)
			require.Equal(t, int64(15), resp.Usage.TotalTokens)
			input, ok := usage.InputTokens()
			require.True(t, ok)
			require.Equal(t, uint32(10), input)
			output, ok := usage.OutputTokens()
			require.True(t, ok)
			require.Equal(t, uint32(5), output)
			total, ok := usage.TotalTokens()
			require.True(t, ok)
			require.Equal(t, uint32(15), total)
		})
	}
}

func TestResponsesOpenAIToChatCompletion_Stream(t *testing.T) {
	for _, tool := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool=%t", tool), func(t *testing.T) {
			translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "gpt-4o", Stream: true}, false)
			require.NoError(t, err)
			stream := "data: {\"id\":\"chat_1\",\"model\":\"backend-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}\n\n"
			expectedEvents := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}
			if tool {
				stream = `data: {"id":"chat_1","model":"backend-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}` + "\n\n" +
					`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Berlin\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
				expectedEvents = []string{"response.created", "response.in_progress", "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed"}
			}
			stream += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n\n"
			_, body, usage, model, err := translator.ResponseBody(nil, strings.NewReader(stream), true, nil)
			require.NoError(t, err)
			require.Equal(t, "backend-model", model)
			events := parseSSEEventsFromBytes(body)
			require.Len(t, events, len(expectedEvents))
			for i, event := range events {
				require.Equal(t, expectedEvents[i], event.eventType)
				requireJSONFields(t, fmt.Sprintf(`{"type":%q,"sequence_number":%d}`, event.eventType, i+1), []byte(event.data))
			}
			var completed openai.ResponseCompletedEvent
			require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &completed))
			require.Equal(t, "completed", completed.Response.Status)
			require.Equal(t, model, completed.Response.Model)
			require.Len(t, completed.Response.Output, 1)
			if tool {
				require.JSONEq(t, `{"type":"response.function_call_arguments.done","item_id":"call_1","name":"weather","arguments":"{\"city\":\"Berlin\"}","output_index":0,"sequence_number":6}`, events[5].data)
				require.Equal(t, "call_1", completed.Response.Output[0].OfFunctionCall.CallID)
				require.JSONEq(t, `{"city":"Berlin"}`, completed.Response.Output[0].OfFunctionCall.Arguments)
			} else {
				require.Equal(t, "Hello world", completed.Response.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
			}
			require.Equal(t, int64(10), completed.Response.Usage.InputTokens)
			require.Equal(t, int64(5), completed.Response.Usage.OutputTokens)
			input, ok := usage.InputTokens()
			require.True(t, ok)
			require.Equal(t, uint32(10), input)
			output, ok := usage.OutputTokens()
			require.True(t, ok)
			require.Equal(t, uint32(5), output)
		})
	}
}

func TestResponsesOpenAIToChatCompletion_HeadersAndErrors(t *testing.T) {
	translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")
	headers, err := translator.ResponseHeaders(map[string]string{"content-type": "application/json", ":status": "200"})
	require.NoError(t, err)
	require.Nil(t, headers)
	headers, body, err := translator.ResponseError(map[string]string{"content-type": "application/json", ":status": "400"}, strings.NewReader(`{"error":{"message":"Invalid model"}}`))
	require.NoError(t, err)
	require.Nil(t, headers)
	require.Nil(t, body)
	headers, body, err = translator.ResponseError(map[string]string{"content-type": "text/plain", ":status": "503"}, strings.NewReader("service unavailable"))
	require.NoError(t, err)
	require.NotNil(t, headers)
	var resp openai.Error
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, "error", resp.Type)
	require.Equal(t, "service unavailable", resp.Error.Message)
}
