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
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func requirePathHeader(t *testing.T, headers []internalapi.Header, expectedPath string) {
	t.Helper()
	var found bool
	for _, h := range headers {
		if h.Key() == pathHeaderName {
			found = true
			require.Equal(t, expectedPath, h.Value())
		}
	}
	require.True(t, found, "expected :path header to be present")
}

func TestResponsesRequestToChatCompletionRequest_Controls(t *testing.T) {
	const parameters = `{"type":"object","properties":{"location":{"type":"object","properties":{"city":{"type":"string"}}},"days":{"type":"array","items":{"type":"object","properties":{"date":{"type":"string"}}}}}}`
	const strictParameters = `{"type":"object","additionalProperties":false,"required":["days","location"],"properties":{"location":{"type":"object","additionalProperties":false,"required":["city"],"properties":{"city":{"type":"string"}}},"days":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["date"],"properties":{"date":{"type":"string"}}}}}}`
	const incompatibleParameters = `{"type":"object","allOf":[{"properties":{"city":{"type":"string"}}}]}`
	for _, tc := range []struct {
		name, request, expected string
	}{
		{"json schema", `{"text":{"format":{"type":"json_schema","name":"answer","description":"An answer","strict":true,"schema":{"type":"object"}}}}`, `{"response_format":{"type":"json_schema","json_schema":{"name":"answer","description":"An answer","strict":true,"schema":{"type":"object"}}}}`},
		{"json object", `{"text":{"format":{"type":"json_object"}}}`, `{"response_format":{"type":"json_object"}}`},
		{"text", `{"text":{"format":{"type":"text"}}}`, `{"response_format":{"type":"text"}}`},
		{"required", `{"tool_choice":"required"}`, `{"tool_choice":"required"}`},
		{"none", `{"tool_choice":"none"}`, `{"tool_choice":"none"}`},
		{"auto", `{"tool_choice":"auto"}`, `{"tool_choice":"auto"}`},
		{"named function", `{"tool_choice":{"type":"function","name":"weather"}}`, `{"tool_choice":{"type":"function","function":{"name":"weather"}}}`},
		{"no parallel calls", `{"parallel_tool_calls":false}`, `{"parallel_tool_calls":false}`},
		{"parallel calls", `{"parallel_tool_calls":true}`, `{"parallel_tool_calls":true}`},
		{"strict true", `{"tools":[{"type":"function","name":"weather","strict":true}]}`, `{"tools":[{"type":"function","function":{"name":"weather","strict":true,"parameters":null}}]}`},
		{"strict false", `{"tools":[{"type":"function","name":"weather","strict":false}]}`, `{"tools":[{"type":"function","function":{"name":"weather","parameters":null}}]}`},
		{"default no parameters", `{"tools":[{"type":"function","name":"weather"}]}`, `{"tools":[{"type":"function","function":{"name":"weather","strict":true,"parameters":{"type":"object","required":[],"additionalProperties":false}}}]}`},
		{"default strict", `{"tools":[{"type":"function","name":"weather","parameters":` + parameters + `}]}`, `{"tools":[{"type":"function","function":{"name":"weather","strict":true,"parameters":` + strictParameters + `}}]}`},
		{"incompatible default strict", `{"tools":[{"type":"function","name":"weather","parameters":` + incompatibleParameters + `}]}`, `{"tools":[{"type":"function","function":{"name":"weather","parameters":` + incompatibleParameters + `}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ResponseRequest
			require.NoError(t, json.Unmarshal([]byte(tc.request), &req))
			original, err := json.Marshal(req.Tools)
			require.NoError(t, err)
			_, chatReq := responsesOpenAIRequestBody(t, "", &req)
			unchanged, err := json.Marshal(req.Tools)
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(unchanged))
			body, err := json.Marshal(chatReq)
			require.NoError(t, err)
			var actual, expected map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &actual))
			require.NoError(t, json.Unmarshal([]byte(tc.expected), &expected))
			for key, value := range expected {
				require.JSONEq(t, string(value), string(actual[key]))
			}
		})
	}
}

func TestResponsesRequestToChatCompletionRequest_ToolOutputs(t *testing.T) {
	for _, tc := range []struct {
		name, output, expected string
		invalid                bool
	}{
		{"string", `"sunny"`, "sunny", false},
		{"text array", `[{"type":"input_text","text":"sunny"},{"type":"input_text","text":"20 degrees"}]`, "sunny\n\n20 degrees", false},
		{"empty array", `[]`, "", false},
		{"image", `[{"type":"input_image","image_url":"https://example.com/image.png"}]`, "", true},
		{"file", `[{"type":"input_file","file_id":"file_1"}]`, "", true},
		{"mixed", `[{"type":"input_text","text":"sunny"},{"type":"input_image","image_url":"https://example.com/image.png"}]`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ResponseRequest
			require.NoError(t, json.Unmarshal([]byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":`+tc.output+`}]}`), &req))
			translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")
			_, body, err := translator.RequestBody(nil, &req, false)
			if tc.invalid {
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				return
			}
			require.NoError(t, err)
			var chatReq openai.ChatCompletionRequest
			require.NoError(t, json.Unmarshal(body, &chatReq))
			require.Equal(t, tc.expected, chatReq.Messages[0].OfTool.Content.Value)
			require.Equal(t, "call_1", chatReq.Messages[0].OfTool.ToolCallID)
		})
	}
	t.Run("unsupported tool choice", func(t *testing.T) {
		var req openai.ResponseRequest
		require.NoError(t, json.Unmarshal([]byte(`{"tool_choice":{"type":"file_search"}}`), &req))
		_, _, err := NewResponsesOpenAIToChatCompletionTranslator("v1", "").RequestBody(nil, &req, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
}

func TestResponsesViaChatCompletion_Buffering(t *testing.T) {
	for _, provider := range []string{"openai", "gemini", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			inner := NewChatCompletionOpenAIToOpenAITranslator("v1", "")
			stream := "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"stop\"}]}\n\n"
			switch provider {
			case "gemini":
				inner = NewChatCompletionOpenAIToGCPVertexAITranslator("")
				stream = `{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}` + "\n"
			case "anthropic":
				inner = NewChatCompletionOpenAIToGCPAnthropicTranslator("v1", "")
				stream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			translator := NewResponsesViaChatCompletionTranslator(inner)
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: true}, false)
			require.NoError(t, err)
			var output []byte
			for i := range len(stream) {
				_, body, _, _, err := translator.ResponseBody(nil, strings.NewReader(stream[i:i+1]), i == len(stream)-1, nil)
				require.NoError(t, err)
				require.NotNil(t, body, "buffered bytes must not pass through")
				output = append(output, body...)
			}
			events := parseSSEEventsFromBytes(output)
			require.NotEmpty(t, events)
			require.Equal(t, "response.created", events[0].eventType)
			require.Equal(t, "response.completed", events[len(events)-1].eventType)
			var terminal openai.ResponseCompletedEvent
			require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &terminal))
			require.Equal(t, "Hello", terminal.Response.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
		})
	}
}

func TestResponsesViaChatCompletion_Incomplete(t *testing.T) {
	for _, reason := range []openai.ChatCompletionChoicesFinishReason{openai.ChatCompletionChoicesFinishReasonLength, openai.ChatCompletionChoicesFinishReasonContentFilter} {
		t.Run(string(reason), func(t *testing.T) {
			response := chatCompletionToResponse(&openai.ChatCompletionResponse{Choices: []openai.ChatCompletionResponseChoice{{
				FinishReason: reason,
				Message:      openai.ChatCompletionResponseChoiceMessage{Content: ptr.To("partial")},
			}}}, "model")
			require.Equal(t, "incomplete", response.Status)
			expectedReason := "max_output_tokens"
			if reason == openai.ChatCompletionChoicesFinishReasonContentFilter {
				expectedReason = "content_filter"
			}
			require.Equal(t, expectedReason, response.IncompleteDetails.Reason)
			require.Equal(t, "incomplete", response.Output[0].OfOutputMessage.Status)
			for _, tool := range []bool{false, true} {
				for _, usage := range []bool{false, true} {
					t.Run(fmt.Sprintf("tool=%t/usage=%t", tool, usage), func(t *testing.T) {
						translator := NewResponsesOpenAIToChatCompletionTranslator("v1", "")
						_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: true}, false)
						require.NoError(t, err)
						delta := `{"content":"partial"}`
						if tool {
							delta = `{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"weather","arguments":"{"}}]}`
						}
						stream := fmt.Sprintf("data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", delta, reason)
						if usage {
							stream += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"
						}
						_, body, _, _, err := translator.ResponseBody(nil, strings.NewReader(stream), true, nil)
						require.NoError(t, err)
						require.NotContains(t, string(body), "response.completed")
						events := parseSSEEventsFromBytes(body)
						require.Equal(t, "response.incomplete", events[len(events)-1].eventType)
						var terminal openai.ResponseIncompleteEvent
						require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &terminal))
						require.Equal(t, "response.incomplete", terminal.Type)
						require.Equal(t, "incomplete", terminal.Response.Status)
						require.Equal(t, expectedReason, terminal.Response.IncompleteDetails.Reason)
						var done openai.ResponseOutputItemDoneEvent
						require.NoError(t, json.Unmarshal([]byte(events[len(events)-2].data), &done))
						require.Equal(t, "response.output_item.done", done.Type)
						if tool {
							require.Equal(t, "incomplete", done.Item.OfFunctionCall.Status)
							require.Equal(t, "incomplete", terminal.Response.Output[0].OfFunctionCall.Status)
						} else {
							require.Equal(t, "incomplete", done.Item.OfOutputMessage.Status)
							require.Equal(t, "incomplete", terminal.Response.Output[0].OfOutputMessage.Status)
						}
					})
				}
			}
		})
	}
}

func TestResponsesRequestToChatCompletionRequest_ConversationWithTools(t *testing.T) {
	req := &openai.ResponseRequest{
		Model: "requested-model",
		Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
			{OfOutputMessage: &openai.ResponseOutputMessage{
				Role:    "assistant",
				Content: openai.ResponseOutputMessageContentUnion{OfString: ptr.To("I'll check the weather")},
			}},
			{OfFunctionCall: &openai.ResponseFunctionToolCall{
				CallID: "call_1", Name: "weather", Arguments: `{"city":"Berlin"}`, Type: "function_call",
			}},
			{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_1", Type: "function_call_output",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: ptr.To("sunny")},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role: "user",
				Content: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "What about this photo?"}},
					{OfInputImage: &openai.ResponseInputImageParam{ImageURL: "https://example.com/photo.png", Detail: "high"}},
				},
			}},
			{OfFunctionCall: &openai.ResponseFunctionToolCall{
				CallID: "call_2", Name: "describe", Arguments: `{}`, Type: "function_call",
			}},
		}},
	}

	chatReq, err := responsesRequestToChatCompletionRequest(req, "backend-model")
	require.NoError(t, err)
	require.Equal(t, "backend-model", chatReq.Model)
	require.Len(t, chatReq.Messages, 4)
	require.Equal(t, "I'll check the weather", chatReq.Messages[0].OfAssistant.Content.Value)
	require.Len(t, chatReq.Messages[0].OfAssistant.ToolCalls, 1)
	require.Equal(t, "call_1", *chatReq.Messages[0].OfAssistant.ToolCalls[0].ID)
	require.JSONEq(t, `{"city":"Berlin"}`, chatReq.Messages[0].OfAssistant.ToolCalls[0].Function.Arguments)
	require.Equal(t, "call_1", chatReq.Messages[1].OfTool.ToolCallID)
	require.Equal(t, "sunny", chatReq.Messages[1].OfTool.Content.Value)
	parts, ok := chatReq.Messages[2].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
	require.True(t, ok)
	require.Len(t, parts, 2)
	require.Equal(t, "What about this photo?", parts[0].OfText.Text)
	require.Equal(t, "https://example.com/photo.png", parts[1].OfImageURL.ImageURL.URL)
	require.Equal(t, openai.ChatCompletionContentPartImageImageURLDetail("high"), parts[1].OfImageURL.ImageURL.Detail)
	require.Len(t, chatReq.Messages[3].OfAssistant.ToolCalls, 1)
	require.Equal(t, "call_2", *chatReq.Messages[3].OfAssistant.ToolCalls[0].ID)
}

func TestResponsesRequestToChatCompletionRequest_MessageRoles(t *testing.T) {
	req := &openai.ResponseRequest{
		Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
			{OfMessage: &openai.EasyInputMessageParam{
				Role: openai.ChatMessageRoleSystem,
				Content: openai.EasyInputMessageContentUnionParam{OfInputItemContentList: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "Follow the rules"}},
					{OfInputText: &openai.ResponseInputTextParam{Text: "Keep it short"}},
				}},
			}},
			{OfMessage: &openai.EasyInputMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: openai.EasyInputMessageContentUnionParam{OfString: ptr.To("Use plain language")},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				Content: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "First answer"}},
					{OfInputText: &openai.ResponseInputTextParam{Text: "Second answer"}},
				},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: []openai.ResponseInputContentUnionParam{{OfInputText: &openai.ResponseInputTextParam{Text: "Be precise"}}},
			}},
			{OfOutputMessage: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
					{OfOutputText: &openai.ResponseOutputTextParam{Text: "I can't help with that"}},
					{OfRefusal: &openai.ResponseOutputRefusalParam{Refusal: "Not allowed"}},
				}},
			}},
		}},
	}

	chatReq, err := responsesRequestToChatCompletionRequest(req, "backend-model")
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 5)
	require.Equal(t, "Follow the rules\n\nKeep it short", chatReq.Messages[0].OfSystem.Content.Value)
	require.Equal(t, "Use plain language", chatReq.Messages[1].OfDeveloper.Content.Value)
	require.Equal(t, "First answer\n\nSecond answer", chatReq.Messages[2].OfAssistant.Content.Value)
	require.Equal(t, "Be precise", chatReq.Messages[3].OfDeveloper.Content.Value)
	require.Equal(t, "I can't help with that\n\nNot allowed", chatReq.Messages[4].OfAssistant.Content.Value)
}
