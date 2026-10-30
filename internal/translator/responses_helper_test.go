// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
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

	chatReq := responsesRequestToChatCompletionRequest(req, "backend-model")
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

	chatReq := responsesRequestToChatCompletionRequest(req, "backend-model")
	require.Len(t, chatReq.Messages, 5)
	require.Equal(t, "Follow the rules\n\nKeep it short", chatReq.Messages[0].OfSystem.Content.Value)
	require.Equal(t, "Use plain language", chatReq.Messages[1].OfDeveloper.Content.Value)
	require.Equal(t, "First answer\n\nSecond answer", chatReq.Messages[2].OfAssistant.Content.Value)
	require.Equal(t, "Be precise", chatReq.Messages[3].OfDeveloper.Content.Value)
	require.Equal(t, "I can't help with that\n\nNot allowed", chatReq.Messages[4].OfAssistant.Content.Value)
}
