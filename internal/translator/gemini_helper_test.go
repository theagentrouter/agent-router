// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	openaigo "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOpenAIMessagesToGeminiContents(t *testing.T) {
	tests := []struct {
		name                      string
		messages                  []openai.ChatCompletionMessageParamUnion
		expectedErrorMsg          string
		expectedContents          []genai.Content
		expectedSystemInstruction *genai.Content
	}{
		{
			name: "happy-path",
			messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
						Role:    openai.ChatMessageRoleDeveloper,
						Content: openai.ContentUnion{Value: "This is a developer message"},
					},
				},
				{
					OfSystem: &openai.ChatCompletionSystemMessageParam{
						Role:    openai.ChatMessageRoleSystem,
						Content: openai.ContentUnion{Value: "This is a system message"},
					},
				},
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "This is a user message"},
					},
				},
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role:    openai.ChatMessageRoleAssistant,
						Audio:   openai.ChatCompletionAssistantMessageParamAudio{},
						Content: openai.StringOrAssistantRoleContentUnion{Value: "This is a assistant message"},
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID: ptr.To("tool_call_1"),
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "example_tool",
									Arguments: "{\"param1\":\"value1\"}",
								},
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
							},
						},
					},
				},
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						ToolCallID: "tool_call_1",
						Content:    openai.ContentUnion{Value: "This is a message from the example_tool"},
					},
				},
			},
			expectedContents: []genai.Content{
				{
					Parts: []*genai.Part{
						{Text: "This is a user message"},
					},
					Role: genai.RoleUser,
				},
				{
					Role: genai.RoleModel,
					Parts: []*genai.Part{
						{
							FunctionCall: &genai.FunctionCall{
								Name: "example_tool",
								Args: map[string]any{
									"param1": "value1",
								},
							},
							ThoughtSignature: dummyThoughtSignature,
						},
						{Text: "This is a assistant message"},
					},
				},
				{
					Role: genai.RoleUser,
					Parts: []*genai.Part{
						{
							FunctionResponse: &genai.FunctionResponse{
								Name: "example_tool",
								Response: map[string]any{
									"output": "This is a message from the example_tool",
								},
							},
						},
					},
				},
			},
			expectedSystemInstruction: &genai.Content{
				Parts: []*genai.Part{
					{Text: "This is a developer message"},
					{Text: "This is a system message"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contents, systemInstruction, err := openAIMessagesToGeminiContents(tc.messages, "gemini-3-pro")

			if tc.expectedErrorMsg != "" || err != nil {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
			} else {
				if d := cmp.Diff(tc.expectedContents, contents); d != "" {
					t.Errorf("Gemini Contents mismatch (-want +got):\n%s", d)
				}
				if d := cmp.Diff(tc.expectedSystemInstruction, systemInstruction); d != "" {
					t.Errorf("SystemInstruction mismatch (-want +got):\n%s", d)
				}
			}
		})
	}
}

// TestAssistantMsgToGeminiParts tests the assistantMsgToGeminiParts function.
func TestAssistantMsgToGeminiParts(t *testing.T) {
	tests := []struct {
		name              string
		msg               openai.ChatCompletionAssistantMessageParam
		expectedParts     []*genai.Part
		expectedToolCalls map[string]string
		expectedErrorMsg  string
	}{
		{
			name: "empty text content",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: "",
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts:     nil,
			expectedToolCalls: map[string]string{},
		},
		{
			name: "invalid content type",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: 10, // Invalid type.
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts:     nil,
			expectedToolCalls: map[string]string{},
			expectedErrorMsg:  "message 'content' must be a string or an array",
		},
		{
			name: "simple text content",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: "Hello, I'm an AI assistant",
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts: []*genai.Part{
				genai.NewPartFromText("Hello, I'm an AI assistant"),
			},
			expectedToolCalls: map[string]string{},
		},
		// Currently noting is returned for refusal messages.
		{
			name: "text content with refusal message",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type:    openai.ChatCompletionAssistantMessageParamContentTypeRefusal,
							Refusal: ptr.To("Response was refused"),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts:     nil,
			expectedToolCalls: map[string]string{},
		},
		{
			name: "content with an array of texts",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
							Text: ptr.To("Hello, I'm an AI assistant"),
						},
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
							Text: ptr.To("How can I assist you today?"),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts: []*genai.Part{
				genai.NewPartFromText("Hello, I'm an AI assistant"),
				genai.NewPartFromText("How can I assist you today?"),
			},
			expectedToolCalls: map[string]string{},
		},
		{
			name: "tool calls without content",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: "",
				},
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_123"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"New York","unit":"celsius"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Args: map[string]any{"location": "New York", "unit": "celsius"},
						Name: "get_weather",
					},
					ThoughtSignature: dummyThoughtSignature,
				},
			},
			expectedToolCalls: map[string]string{
				"call_123": "get_weather",
			},
		},
		{
			name: "multiple tool calls with content",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: "I'll help you with that",
				},
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_789"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"New York","unit":"celsius"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
					{
						ID: ptr.To("call_abc"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_time",
							Arguments: `{"timezone":"EST"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				func() *genai.Part {
					p := genai.NewPartFromFunctionCall("get_weather", map[string]any{
						"location": "New York",
						"unit":     "celsius",
					})
					p.ThoughtSignature = dummyThoughtSignature
					return p
				}(),
				genai.NewPartFromFunctionCall("get_time", map[string]any{
					"timezone": "EST",
				}),
				genai.NewPartFromText("I'll help you with that"),
			},
			expectedToolCalls: map[string]string{
				"call_789": "get_weather",
				"call_abc": "get_time",
			},
		},
		{
			name: "invalid tool call arguments",
			msg: openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_def"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"New York"`, // Invalid JSON.
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedErrorMsg: "function arguments must be valid JSON",
		},
		{
			name: "nil content",
			msg: openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts:     nil,
			expectedToolCalls: map[string]string{},
		},
		{
			name: "thinking content with valid text",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text: ptr.To("Let me think step by step..."),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts: []*genai.Part{
				{
					Text:    "Let me think step by step...",
					Thought: true,
				},
			},
			expectedToolCalls: map[string]string{},
		},
		{
			name: "thinking content with nil text",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text: nil,
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts:     nil,
			expectedToolCalls: map[string]string{},
		},
		{
			name: "thinking content with empty text",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text: ptr.To(""),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts:     nil,
			expectedToolCalls: map[string]string{},
		},
		{
			name: "mixed content with thinking and regular text",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text: ptr.To("First, I need to analyze this problem..."),
						},
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
							Text: ptr.To("Based on my analysis, here's the answer."),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts: []*genai.Part{
				{
					Text:    "First, I need to analyze this problem...",
					Thought: true,
				},
				genai.NewPartFromText("Based on my analysis, here's the answer."),
			},
			expectedToolCalls: map[string]string{},
		},
		{
			name: "thinking content mixed with tool calls",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text: ptr.To("I need to call a function to get the weather"),
						},
						{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
							Text: ptr.To("Let me get the weather for you"),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				func() *genai.Part {
					p := genai.NewPartFromFunctionCall("get_weather", map[string]any{
						"location": "San Francisco",
					})
					p.ThoughtSignature = dummyThoughtSignature
					return p
				}(),
				{
					Text:    "I need to call a function to get the weather",
					Thought: true,
				},
				genai.NewPartFromText("Let me get the weather for you"),
			},
			expectedToolCalls: map[string]string{
				"call_weather": "get_weather",
			},
		},
		{
			name: "thinking content with signature (no tool calls)",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text:      ptr.To("Let me think step by step..."),
							Signature: ptr.To("dGVzdHNpZ25hdHVyZQ=="), // "testsignature" in base64
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedParts: []*genai.Part{
				{
					Text:             "Let me think step by step...",
					Thought:          true,
					ThoughtSignature: []byte("testsignature"),
				},
			},
			expectedToolCalls: map[string]string{},
		},
		{
			name: "thinking content with signature and tool calls - signature on first tool call",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text:      ptr.To("I need to call a function to get the weather"),
							Signature: ptr.To("dGVzdHNpZ25hdHVyZQ=="), // "testsignature" in base64
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
					{
						ID: ptr.To("call_time"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_time",
							Arguments: `{"timezone":"PST"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_weather",
						Args: map[string]any{"location": "San Francisco"},
					},
					ThoughtSignature: []byte("testsignature"),
				},
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_time",
						Args: map[string]any{"timezone": "PST"},
					},
				},
				{
					Text:    "I need to call a function to get the weather",
					Thought: true,
				},
			},
			expectedToolCalls: map[string]string{
				"call_weather": "get_weather",
				"call_time":    "get_time",
			},
		},
		{
			name: "thinking_blocks signature with tool calls - signature on first tool call",
			msg: openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				ThinkingBlocks: []openai.ThinkingBlock{
					{Type: "thinking", Signature: "dGVzdHNpZ25hdHVyZQ=="}, // "testsignature" in base64
				},
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
					{
						ID: ptr.To("call_time"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_time",
							Arguments: `{"timezone":"PST"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_weather",
						Args: map[string]any{"location": "San Francisco"},
					},
					ThoughtSignature: []byte("testsignature"),
				},
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_time",
						Args: map[string]any{"timezone": "PST"},
					},
				},
			},
			expectedToolCalls: map[string]string{
				"call_weather": "get_weather",
				"call_time":    "get_time",
			},
		},
		{
			name: "tool calls with no signature anywhere fall back to dummy signature on first call only",
			msg: openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
					{
						ID: ptr.To("call_time"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_time",
							Arguments: `{"timezone":"PST"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_weather",
						Args: map[string]any{"location": "San Francisco"},
					},
					ThoughtSignature: dummyThoughtSignature,
				},
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_time",
						Args: map[string]any{"timezone": "PST"},
					},
				},
			},
			expectedToolCalls: map[string]string{
				"call_weather": "get_weather",
				"call_time":    "get_time",
			},
		},
		{
			name: "content thinking-part signature wins over differing thinking_blocks signature",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text:      ptr.To("I need to call a function to get the weather"),
							Signature: ptr.To("dGVzdHNpZ25hdHVyZQ=="), // "testsignature" in base64
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
				ThinkingBlocks: []openai.ThinkingBlock{
					{Type: "thinking", Signature: "b3RoZXJzaWduYXR1cmU="}, // "othersignature" in base64
				},
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_weather",
						Args: map[string]any{"location": "San Francisco"},
					},
					ThoughtSignature: []byte("testsignature"),
				},
				{
					Text:    "I need to call a function to get the weather",
					Thought: true,
				},
			},
			expectedToolCalls: map[string]string{
				"call_weather": "get_weather",
			},
		},
		{
			name: "invalid base64 in thinking_blocks signature",
			msg: openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				ThinkingBlocks: []openai.ThinkingBlock{
					{Type: "thinking", Signature: "not-valid-base64!!!"},
				},
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{
					{
						ID: ptr.To("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
						Type: openai.ChatCompletionMessageToolCallTypeFunction,
					},
				},
			},
			expectedErrorMsg: "failed to decode thought signature",
		},
		{
			name: "thinking content with invalid base64 signature",
			msg: openai.ChatCompletionAssistantMessageParam{
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{
							Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
							Text:      ptr.To("Let me think..."),
							Signature: ptr.To("not-valid-base64!!!"),
						},
					},
				},
				Role: openai.ChatMessageRoleAssistant,
			},
			expectedErrorMsg: "failed to decode thought signature",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts, toolCalls, err := assistantMsgToGeminiParts(&tc.msg)

			if tc.expectedErrorMsg != "" || err != nil {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
			} else {
				require.NoError(t, err)
				if d := cmp.Diff(tc.expectedParts, parts); d != "" {
					t.Errorf("Parts mismatch (-want +got):\n%s", d)
				}
				if d := cmp.Diff(tc.expectedToolCalls, toolCalls); d != "" {
					t.Errorf("Tools mismatch (-want +got):\n%s", d)
				}
			}
		})
	}
}

func TestDeveloperMsgToGeminiParts(t *testing.T) {
	tests := []struct {
		name             string
		msg              openai.ChatCompletionDeveloperMessageParam
		expectedParts    []*genai.Part
		expectedErrorMsg string
	}{
		{
			name: "string content",
			msg: openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ContentUnion{
					Value: "This is a system message",
				},
				Role: openai.ChatMessageRoleSystem,
			},
			expectedParts: []*genai.Part{
				{Text: "This is a system message"},
			},
		},
		{
			name: "content as string array",
			msg: openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ContentUnion{
					Value: []openai.ChatCompletionContentPartTextParam{
						{Text: "This is a system message"},
						{Text: "It can be multiline"},
					},
				},
				Role: openai.ChatMessageRoleSystem,
			},
			expectedParts: []*genai.Part{
				{Text: "This is a system message"},
				{Text: "It can be multiline"},
			},
		},
		{
			name: "invalid content type",
			msg: openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ContentUnion{
					Value: 10, // Invalid type.
				},
				Role: openai.ChatMessageRoleSystem,
			},
			expectedParts: []*genai.Part{
				{Text: "This is a system message"},
			},
			expectedErrorMsg: "message 'content' must be a string or an array",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content, err := developerMsgToGeminiParts(tc.msg)

			if tc.expectedErrorMsg != "" || err != nil {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
			} else {
				require.NoError(t, err)
				if d := cmp.Diff(tc.expectedParts, content); d != "" {
					t.Errorf("Content mismatch (-want +got):\n%s", d)
				}
			}
		})
	}
}

func TestToolMsgToGeminiParts(t *testing.T) {
	tests := []struct {
		name             string
		msg              openai.ChatCompletionToolMessageParam
		knownToolCalls   map[string]string
		expectedPart     *genai.Part
		expectedErrorMsg string
	}{
		{
			name: "Tool message with invalid content",
			msg: openai.ChatCompletionToolMessageParam{
				Content: openai.ContentUnion{
					Value: 10, // Invalid type.
				},
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "tool_123",
			},
			knownToolCalls:   map[string]string{"tool_123": "get_weather"},
			expectedErrorMsg: "message 'content' must be a string or an array",
		},
		{
			name: "Tool message with string content",
			msg: openai.ChatCompletionToolMessageParam{
				Content: openai.ContentUnion{
					Value: "This is a tool message",
				},
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "tool_123",
			},
			knownToolCalls: map[string]string{"tool_123": "get_weather"},
			expectedPart: &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					Name:     "get_weather",
					Response: map[string]any{"output": "This is a tool message"},
				},
			},
		},
		{
			name: "Tool message with string array content",
			msg: openai.ChatCompletionToolMessageParam{
				Content: openai.ContentUnion{
					Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: string(openai.ChatCompletionContentPartTextTypeText),
							Text: "This is a tool message. ",
						},
						{
							Type: string(openai.ChatCompletionContentPartTextTypeText),
							Text: "And this is another part",
						},
					},
				},
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "tool_123",
			},
			knownToolCalls: map[string]string{"tool_123": "get_weather"},
			expectedPart: &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					Name:     "get_weather",
					Response: map[string]any{"output": "This is a tool message. And this is another part"},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := toolMsgToGeminiParts(tc.msg, tc.knownToolCalls)

			if tc.expectedErrorMsg != "" || err != nil {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
			} else {
				require.NoError(t, err)
				if d := cmp.Diff(tc.expectedPart, parts); d != "" {
					t.Errorf("Parts mismatch (-want +got):\n%s", d)
				}
			}
		})
	}
}

// TestUserMsgToGeminiParts tests the gcpPartsFromUserMsgToGeminiParts function with different inputs.
func TestUserMsgToGeminiParts(t *testing.T) {
	tests := []struct {
		name           string
		msg            openai.ChatCompletionUserMessageParam
		expectedParts  []*genai.Part
		expectedErrMsg string
	}{
		{
			name: "simple string content",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: "Hello, how are you?",
				},
			},
			expectedParts: []*genai.Part{
				{Text: "Hello, how are you?"},
			},
		},
		{
			name: "empty string content",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: "",
				},
			},
			expectedParts: nil,
		},
		{
			name: "array with multiple text contents",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfText: &openai.ChatCompletionContentPartTextParam{
								Type: string(openai.ChatCompletionContentPartTextTypeText),
								Text: "First message",
							},
						},
						{
							OfText: &openai.ChatCompletionContentPartTextParam{
								Type: string(openai.ChatCompletionContentPartTextTypeText),
								Text: "Second message",
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{Text: "First message"},
				{Text: "Second message"},
			},
		},
		{
			name: "image content with URL",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "https://example.com/image.jpg",
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{FileData: &genai.FileData{FileURI: "https://example.com/image.jpg", MIMEType: "image/jpeg"}},
			},
		},
		{
			name: "empty image URL",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "",
								},
							},
						},
					},
				},
			},
			expectedParts: nil,
		},
		{
			name: "invalid image URL",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: ":%invalid-url%:",
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "invalid image URL",
		},
		{
			name: "mixed content - text and image",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfText: &openai.ChatCompletionContentPartTextParam{
								Type: string(openai.ChatCompletionContentPartTextTypeText),
								Text: "Check this image:",
							},
						},
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "https://example.com/image.jpg",
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{Text: "Check this image:"},
				{FileData: &genai.FileData{FileURI: "https://example.com/image.jpg", MIMEType: "image/jpeg"}},
			},
		},
		{
			name: "data URI image content",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwD3+iiigD//2Q==",
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "image/jpeg",
					},
				},
			},
		},
		{
			name: "invalid data URI format",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "data:invalid-format",
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "invalid image data URI",
		},
		{
			name: "audio content WAV - inline base64",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
								Type: "input_audio",
								InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
									Data:   base64.StdEncoding.EncodeToString([]byte("fake-wav-data")),
									Format: openai.ChatCompletionContentPartInputAudioInputAudioFormatWAV,
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "audio/wav",
					},
				},
			},
		},
		{
			name: "audio content MP3 - inline base64",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
								Type: "input_audio",
								InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
									Data:   base64.StdEncoding.EncodeToString([]byte("fake-mp3-data")),
									Format: openai.ChatCompletionContentPartInputAudioInputAudioFormatMP3,
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "audio/mp3",
					},
				},
			},
		},
		{
			name: "audio content - invalid base64 data",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
								Type: "input_audio",
								InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
									Data:   "!!!not-valid-base64!!!",
									Format: openai.ChatCompletionContentPartInputAudioInputAudioFormatWAV,
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "invalid audio data encoding",
		},
		{
			name: "audio content - unsupported format",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
								Type: "input_audio",
								InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
									Data:   base64.StdEncoding.EncodeToString([]byte("fake-audio")),
									Format: "flac",
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "unsupported audio format",
		},
		{
			name: "file content - PDF data URI",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfFile: &openai.ChatCompletionContentPartFileParam{
								Type: "file",
								File: openai.ChatCompletionContentPartFileFileParam{
									FileData: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("fake-pdf")),
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "application/pdf",
					},
				},
			},
		},
		{
			name: "file content - video/mp4 data URI",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfFile: &openai.ChatCompletionContentPartFileParam{
								Type: "file",
								File: openai.ChatCompletionContentPartFileFileParam{
									FileData: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("fake-video")),
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "video/mp4",
					},
				},
			},
		},
		{
			name: "file content - FileID not supported",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfFile: &openai.ChatCompletionContentPartFileParam{
								Type: "file",
								File: openai.ChatCompletionContentPartFileFileParam{
									FileID: "file-abc123",
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "file_id is not supported yet",
		},
		{
			name: "file content - empty file_data",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfFile: &openai.ChatCompletionContentPartFileParam{
								Type: "file",
								File: openai.ChatCompletionContentPartFileFileParam{},
							},
						},
					},
				},
			},
			expectedErrMsg: "file content must provide file_data",
		},
		{
			name: "file content - invalid data URI",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfFile: &openai.ChatCompletionContentPartFileParam{
								Type: "file",
								File: openai.ChatCompletionContentPartFileFileParam{
									FileData: "not-a-data-uri",
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "invalid file data URI",
		},
		{
			name: "mixed content - text, audio, and file",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfText: &openai.ChatCompletionContentPartTextParam{
								Type: string(openai.ChatCompletionContentPartTextTypeText),
								Text: "Analyze these:",
							},
						},
						{
							OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
								Type: "input_audio",
								InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
									Data:   base64.StdEncoding.EncodeToString([]byte("fake-wav")),
									Format: openai.ChatCompletionContentPartInputAudioInputAudioFormatWAV,
								},
							},
						},
						{
							OfFile: &openai.ChatCompletionContentPartFileParam{
								Type: "file",
								File: openai.ChatCompletionContentPartFileFileParam{
									FileData: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("fake-pdf")),
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{Text: "Analyze these:"},
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "audio/wav",
					},
				},
				{
					InlineData: &genai.Blob{
						Data:     []byte("This field is ignored during testcase comparison"),
						MIMEType: "application/pdf",
					},
				},
			},
		},
		{
			name: "unsupported content type",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: 42, // not a string or array.
				},
			},
			expectedErrMsg: "message 'content' must be a string or an array",
		},
		{
			name: "image with low detail",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL:    "https://example.com/image.jpg",
									Detail: openai.ChatCompletionContentPartImageImageURLDetailLow,
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FileData: &genai.FileData{
						FileURI:  "https://example.com/image.jpg",
						MIMEType: "image/jpeg",
					},
					MediaResolution: &genai.PartMediaResolution{
						Level: genai.PartMediaResolutionLevelMediaResolutionLow,
					},
				},
			},
		},
		{
			name: "image with medium detail",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL:    "https://example.com/image.jpg",
									Detail: openai.ChatCompletionContentPartImageImageURLDetailMedium,
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FileData: &genai.FileData{
						FileURI:  "https://example.com/image.jpg",
						MIMEType: "image/jpeg",
					},
					MediaResolution: &genai.PartMediaResolution{
						Level: genai.PartMediaResolutionLevelMediaResolutionMedium,
					},
				},
			},
		},
		{
			name: "image with high detail",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL:    "https://example.com/image.jpg",
									Detail: openai.ChatCompletionContentPartImageImageURLDetailHigh,
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FileData: &genai.FileData{
						FileURI:  "https://example.com/image.jpg",
						MIMEType: "image/jpeg",
					},
					MediaResolution: &genai.PartMediaResolution{
						Level: genai.PartMediaResolutionLevelMediaResolutionHigh,
					},
				},
			},
		},
		{
			name: "image with auto detail",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL:    "https://example.com/image.jpg",
									Detail: openai.ChatCompletionContentPartImageImageURLDetailAuto,
								},
							},
						},
					},
				},
			},
			expectedParts: []*genai.Part{
				{
					FileData: &genai.FileData{
						FileURI:  "https://example.com/image.jpg",
						MIMEType: "image/jpeg",
					},
					MediaResolution: &genai.PartMediaResolution{
						Level: genai.PartMediaResolutionLevelMediaResolutionUnspecified,
					},
				},
			},
		},
		{
			name: "image with invalid detail",
			msg: openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{
							OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: openai.ChatCompletionContentPartImageTypeImageURL,
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL:    "https://example.com/image.jpg",
									Detail: openai.ChatCompletionContentPartImageImageURLDetail("invalid"),
								},
							},
						},
					},
				},
			},
			expectedErrMsg: "invalid detail",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := userMsgToGeminiParts(tc.msg, "gemini-3-pro")

			if tc.expectedErrMsg != "" || err != nil {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				if d := cmp.Diff(tc.expectedParts, parts, cmpopts.IgnoreFields(genai.Blob{}, "Data")); d != "" {
					t.Errorf("Parts mismatch (-want +got):\n%s", d)
				}
			}
		})
	}
}

func TestOpenAIReqToGeminiGenerationConfig(t *testing.T) {
	tests := []struct {
		name                     string
		input                    *openai.ChatCompletionRequest
		expectedGenerationConfig *genai.GenerationConfig
		expectedResponseMode     geminiResponseMode
		expectedErrMsg           string
		requestModel             internalapi.RequestModel
	}{
		{
			name: "all fields set",
			input: &openai.ChatCompletionRequest{
				Temperature:      ptr.To(0.7),
				TopP:             ptr.To(0.9),
				Seed:             ptr.To(42),
				TopLogProbs:      ptr.To(3),
				LogProbs:         ptr.To(true),
				N:                ptr.To(2),
				MaxTokens:        ptr.To(int64(256)),
				PresencePenalty:  ptr.To(float32(1.1)),
				FrequencyPenalty: ptr.To(float32(0.5)),
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"stop1", "stop2"},
				},
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				Temperature:      ptr.To(float32(0.7)),
				TopP:             ptr.To(float32(0.9)),
				Seed:             ptr.To(int32(42)),
				Logprobs:         ptr.To(int32(3)),
				ResponseLogprobs: true,
				CandidateCount:   2,
				MaxOutputTokens:  256,
				PresencePenalty:  ptr.To(float32(1.1)),
				FrequencyPenalty: ptr.To(float32(0.5)),
				StopSequences:    []string{"stop1", "stop2"},
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name:                     "minimal fields",
			input:                    &openai.ChatCompletionRequest{},
			expectedGenerationConfig: &genai.GenerationConfig{},
			expectedResponseMode:     responseModeNone,
		},
		{
			name: "only MaxTokens set",
			input: &openai.ChatCompletionRequest{
				MaxTokens: ptr.To(int64(100)),
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				MaxOutputTokens: 100,
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "only MaxCompletionTokens set",
			input: &openai.ChatCompletionRequest{
				MaxCompletionTokens: ptr.To(int64(200)),
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				MaxOutputTokens: 200,
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "both MaxCompletionTokens and MaxTokens set - MaxCompletionTokens takes precedence",
			input: &openai.ChatCompletionRequest{
				MaxCompletionTokens: ptr.To(int64(300)),
				MaxTokens:           ptr.To(int64(100)),
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				MaxOutputTokens: 300,
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "neither MaxCompletionTokens nor MaxTokens set",
			input: &openai.ChatCompletionRequest{
				Temperature: ptr.To(0.5),
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				Temperature: ptr.To(float32(0.5)),
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "stop sequences",
			input: &openai.ChatCompletionRequest{
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfString: openaigo.Opt[string]("stop1"),
				},
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				StopSequences: []string{"stop1"},
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "text",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfText: &openai.ChatCompletionResponseFormatTextParam{
						Type: openai.ChatCompletionResponseFormatTypeText,
					},
				},
			},
			expectedGenerationConfig: &genai.GenerationConfig{ResponseMIMEType: "text/plain"},
			expectedResponseMode:     responseModeText,
			requestModel:             "gemini-2.5-flash",
		},
		{
			name: "json object",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONObject: &openai.ChatCompletionResponseFormatJSONObjectParam{
						Type: openai.ChatCompletionResponseFormatTypeJSONObject,
					},
				},
			},
			expectedGenerationConfig: &genai.GenerationConfig{ResponseMIMEType: "application/json"},
			expectedResponseMode:     responseModeJSON,
			requestModel:             "gemini-2.5-flash",
		},
		{
			name: "json schema (map)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{"type": "string"}`),
						},
					},
				},
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType:   "application/json",
				ResponseJsonSchema: map[string]any{"type": "string"},
			},
			expectedResponseMode: responseModeJSON,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "json schema (string)",
			input: &openai.ChatCompletionRequest{
				Model: "gemini-2.5",
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{"type":"string"}`),
						},
					},
				},
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType:   "application/json",
				ResponseJsonSchema: map[string]any{"type": "string"},
			},
			expectedResponseMode: responseModeJSON,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "json schema (invalid string)",
			input: &openai.ChatCompletionRequest{
				Model: "gemini-2.5",
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{"type":`), // invalid JSON.
						},
					},
				},
			},
			expectedErrMsg: "invalid json schema",
			requestModel:   "gemini-2.5-flash",
		},
		{
			name: "guided choice",
			input: &openai.ChatCompletionRequest{
				GuidedChoice: []string{"Positive", "Negative"},
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "text/x.enum",
				ResponseSchema:   &genai.Schema{Type: "STRING", Enum: []string{"Positive", "Negative"}},
			},
			expectedResponseMode: responseModeEnum,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "guided regex",
			input: &openai.ChatCompletionRequest{
				GuidedRegex: "\\w+@\\w+\\.com\\n",
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema:   &genai.Schema{Type: "STRING", Pattern: "\\w+@\\w+\\.com\\n"},
			},
			expectedResponseMode: responseModeRegex,
			requestModel:         "gemini-2.5-flash",
		},
		{
			name: "guided json",
			input: &openai.ChatCompletionRequest{
				GuidedJSON: json.RawMessage(`{"type": "string"}`),
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType:   "application/json",
				ResponseJsonSchema: json.RawMessage(`{"type": "string"}`),
			},
			expectedResponseMode: responseModeJSON,
		},
		{
			name: "multiple format specifiers - ResponseFormat and GuidedChoice",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfText: &openai.ChatCompletionResponseFormatTextParam{
						Type: openai.ChatCompletionResponseFormatTypeText,
					},
				},
				GuidedChoice: []string{"A", "B"},
			},
			expectedErrMsg: "only one of responseFormat, guidedChoice, guidedRegex, guidedJSON can be specified",
			requestModel:   "gemini-2.5-flash",
		},
		{
			name: "reasoning effort low",
			input: &openai.ChatCompletionRequest{
				ReasoningEffort: openai.ReasoningEffortLow,
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ThinkingConfig: &genai.ThinkingConfig{
					ThinkingLevel: genai.ThinkingLevelLow,
				},
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-3-pro",
		},
		{
			name: "reasoning effort medium",
			input: &openai.ChatCompletionRequest{
				ReasoningEffort: openai.ReasoningEffortMedium,
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ThinkingConfig: &genai.ThinkingConfig{
					ThinkingLevel: genai.ThinkingLevelHigh,
				},
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-3-pro",
		},
		{
			name: "high reasoning effort maps to ThinkingLevelHigh",
			input: &openai.ChatCompletionRequest{
				ReasoningEffort: openai.ReasoningEffortHigh,
			},
			expectedGenerationConfig: &genai.GenerationConfig{
				ThinkingConfig: &genai.ThinkingConfig{
					ThinkingLevel: genai.ThinkingLevelHigh,
				},
			},
			expectedResponseMode: responseModeNone,
			requestModel:         "gemini-3-flash",
		},
		{
			name: "reasoning effort unsupported value",
			input: &openai.ChatCompletionRequest{
				ReasoningEffort: "invalid_value",
			},
			expectedErrMsg: "unsupported reasoning effort level",
			requestModel:   "gemini-3-pro",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, responseMode, err := openAIReqToGeminiGenerationConfig(tc.input, tc.requestModel)
			if tc.expectedErrMsg != "" {
				require.ErrorContains(t, err, tc.expectedErrMsg)
			} else {
				require.NoError(t, err)

				if diff := cmp.Diff(tc.expectedGenerationConfig, got, cmpopts.IgnoreUnexported(genai.GenerationConfig{})); diff != "" {
					t.Errorf("GenerationConfig mismatch (-want +got):\n%s", diff)
				}

				if responseMode != tc.expectedResponseMode {
					t.Errorf("geminiResponseMode mismatch: got %v, want %v", responseMode, tc.expectedResponseMode)
				}
			}
		})
	}
}

func TestOpenAIToolsToGeminiTools(t *testing.T) {
	funcParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "integer"},
			"b": map[string]any{"type": "integer"},
		},
		"required": []any{"a", "b"},
	}
	tests := []struct {
		name                          string
		openaiTools                   []openai.Tool
		parametersJSONSchemaAvailable bool
		expected                      []genai.Tool
		expectedError                 string
	}{
		{
			name:                          "empty tools with parametersJSONSchemaAvailable=false",
			openaiTools:                   nil,
			parametersJSONSchemaAvailable: false,
			expected:                      nil,
		},
		{
			name:                          "empty tools with parametersJSONSchemaAvailable=true",
			openaiTools:                   nil,
			parametersJSONSchemaAvailable: true,
			expected:                      nil,
		},
		{
			name: "single function tool with parameters - parametersJSONSchemaAvailable=false",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "add",
						Description: "Add two numbers",
						Parameters:  funcParams,
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "add",
							Description: "Add two numbers",
							Parameters: &genai.Schema{
								Type: "object",
								Properties: map[string]*genai.Schema{
									"a": {Type: "integer"},
									"b": {Type: "integer"},
								},
								Required: []string{"a", "b"},
							},
						},
					},
				},
			},
		},
		{
			name: "single function tool with parameters - parametersJSONSchemaAvailable=true",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "add",
						Description: "Add two numbers",
						Parameters:  funcParams,
					},
				},
			},
			parametersJSONSchemaAvailable: true,
			expected: []genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:                 "add",
							Description:          "Add two numbers",
							ParametersJsonSchema: funcParams,
						},
					},
				},
			},
		},
		{
			name: "multiple function tools with nil/empty parameters - parametersJSONSchemaAvailable=false",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "foo",
						Description: "Foo function",
						Parameters:  map[string]any{}, // empty parameters
					},
				},
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "bar",
						Description: "Bar function",
						Parameters:  nil, // nil parameters
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "foo",
							Description: "Foo function",
							Parameters:  nil,
						},
						{
							Name:        "bar",
							Description: "Bar function",
							Parameters:  nil,
						},
					},
				},
			},
		},
		{
			name: "multiple function tools with nil/empty parameters - parametersJSONSchemaAvailable=true",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "foo",
						Description: "Foo function",
					},
				},
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "bar",
						Description: "Bar function",
					},
				},
			},
			parametersJSONSchemaAvailable: true,
			expected: []genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:               "foo",
							Description:        "Foo function",
							ResponseJsonSchema: nil,
						},
						{
							Name:               "bar",
							Description:        "Bar function",
							ResponseJsonSchema: nil,
						},
					},
				},
			},
		},
		{
			name: "tool with invalid parameters schema - parametersJSONSchemaAvailable=false",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "bad",
						Description: "Bad function",
						Parameters:  "invalid-json",
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expectedError:                 "tool bad parameters must be a JSON object",
		},
		{
			name: "tool with invalid parameters schema - parametersJSONSchemaAvailable=true",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "bad",
						Description: "Bad function",
						Parameters:  "invalid-json",
					},
				},
			},
			parametersJSONSchemaAvailable: true,
			expected: []genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Description: "Bad function",
							Name:        "bad",
							// ai-gateway does not validate schema, upstream will be expected to validate the bad json param
							ParametersJsonSchema: "invalid-json",
						},
					},
				},
			},
		},
		{
			name: "complex nested schema - parametersJSONSchemaAvailable=false",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "complex_tool",
						Description: "Complex tool with nested parameters",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"user": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name": map[string]any{"type": "string"},
										"age":  map[string]any{"type": "integer"},
									},
									"required": []any{"name"},
								},
								"items": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"id":   map[string]any{"type": "integer"},
											"name": map[string]any{"type": "string"},
										},
									},
								},
							},
							"required": []any{"user"},
						},
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "complex_tool",
							Description: "Complex tool with nested parameters",
							Parameters: &genai.Schema{
								Type: "object",
								Properties: map[string]*genai.Schema{
									"user": {
										Type: "object",
										Properties: map[string]*genai.Schema{
											"name": {Type: "string"},
											"age":  {Type: "integer"},
										},
										Required: []string{"name"},
									},
									"items": {
										Type: "array",
										Items: &genai.Schema{
											Type: "object",
											Properties: map[string]*genai.Schema{
												"id":   {Type: "integer"},
												"name": {Type: "string"},
											},
										},
									},
								},
								Required: []string{"user"},
							},
						},
					},
				},
			},
		},
		{
			name: "non-function tool is ignored - both modes",
			openaiTools: []openai.Tool{
				{
					Type: "retrieval",
				},
			},
			parametersJSONSchemaAvailable: false,
			expectedError:                 "unsupported tool type: retrieval",
		},
		{
			name: "mixed valid and invalid tools - parametersJSONSchemaAvailable=false",
			openaiTools: []openai.Tool{
				{
					Type: "retrieval", // Should be ignored
				},
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "valid_tool",
						Description: "Valid function tool",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"param": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expectedError:                 "unsupported tool type: retrieval",
		},
		{
			name: "tool with nil function - should not panic",
			openaiTools: []openai.Tool{
				{
					Type:     openai.ToolTypeFunction,
					Function: nil,
				},
			},
			parametersJSONSchemaAvailable: false,
			expected:                      nil,
		},
		{
			name: "enterprise search tool only",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeEnterpriseWebSearch,
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					EnterpriseWebSearch: &genai.EnterpriseWebSearch{},
				},
			},
		},
		{
			name: "mixed function and enterprise search tools",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "get_weather",
						Description: "Get current weather",
						Parameters:  funcParams,
					},
				},
				{
					Type: openai.ToolTypeEnterpriseWebSearch,
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					EnterpriseWebSearch: &genai.EnterpriseWebSearch{},
				},
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "get_weather",
							Description: "Get current weather",
							Parameters: &genai.Schema{
								Type: "object",
								Properties: map[string]*genai.Schema{
									"a": {Type: "integer"},
									"b": {Type: "integer"},
								},
								Required: []string{"a", "b"},
							},
						},
					},
				},
			},
		},
		{
			name: "google search tool only - no config",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeGoogleSearch,
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					GoogleSearch: &genai.GoogleSearch{},
				},
			},
		},
		{
			name: "google search tool with exclude domains",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeGoogleSearch,
					GoogleSearch: &openai.GCPGoogleSearchConfig{
						ExcludeDomains: []string{"example.com", "test.com"},
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					GoogleSearch: &genai.GoogleSearch{
						ExcludeDomains: []string{"example.com", "test.com"},
					},
				},
			},
		},
		{
			name: "google search tool with all options",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeGoogleSearch,
					GoogleSearch: &openai.GCPGoogleSearchConfig{
						ExcludeDomains:     []string{"spam.com"},
						BlockingConfidence: "BLOCK_MEDIUM_AND_ABOVE",
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					GoogleSearch: &genai.GoogleSearch{
						ExcludeDomains:     []string{"spam.com"},
						BlockingConfidence: genai.PhishBlockThresholdBlockMediumAndAbove,
					},
				},
			},
		},
		{
			name: "google search tool with time range filter",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeGoogleSearch,
					GoogleSearch: &openai.GCPGoogleSearchConfig{
						TimeRangeFilter: &openai.GCPTimeRangeFilter{
							StartTime: "2024-01-01T00:00:00Z",
							EndTime:   "2024-12-31T23:59:59Z",
						},
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					GoogleSearch: &genai.GoogleSearch{
						TimeRangeFilter: &genai.Interval{
							StartTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							EndTime:   time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
						},
					},
				},
			},
		},
		{
			name: "mixed function and google search tools",
			openaiTools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "search_products",
						Description: "Search for products",
						Parameters:  funcParams,
					},
				},
				{
					Type: openai.ToolTypeGoogleSearch,
					GoogleSearch: &openai.GCPGoogleSearchConfig{
						ExcludeDomains: []string{"competitor.com"},
					},
				},
			},
			parametersJSONSchemaAvailable: false,
			expected: []genai.Tool{
				{
					GoogleSearch: &genai.GoogleSearch{
						ExcludeDomains: []string{"competitor.com"},
					},
				},
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "search_products",
							Description: "Search for products",
							Parameters: &genai.Schema{
								Type: "object",
								Properties: map[string]*genai.Schema{
									"a": {Type: "integer"},
									"b": {Type: "integer"},
								},
								Required: []string{"a", "b"},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := openAIToolsToGeminiTools(tc.openaiTools, tc.parametersJSONSchemaAvailable)
			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
			} else {
				require.NoError(t, err)
				if d := cmp.Diff(tc.expected, result, cmpopts.IgnoreUnexported(genai.Schema{})); d != "" {
					t.Errorf("Result mismatch (-want +got):\n%s", d)
				}
			}
		})
	}
}

func TestOpenAIToolChoiceToGeminiToolConfig(t *testing.T) {
	tests := []struct {
		name      string
		input     *openai.ChatCompletionToolChoiceUnion
		expected  *genai.ToolConfig
		expectErr string
	}{
		{
			name:     "string auto",
			input:    &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
			expected: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}},
		},
		{
			name:     "string none",
			input:    &openai.ChatCompletionToolChoiceUnion{Value: "none"},
			expected: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}},
		},
		{
			name:     "string required",
			input:    &openai.ChatCompletionToolChoiceUnion{Value: "required"},
			expected: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAny}},
		},
		{
			name: "ToolChoice struct",
			input: &openai.ChatCompletionToolChoiceUnion{
				Value: openai.ChatCompletionNamedToolChoice{
					Type:     openai.ToolTypeFunction,
					Function: openai.ChatCompletionNamedToolChoiceFunction{Name: "myfunc"},
				},
			},
			expected: &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode:                 genai.FunctionCallingConfigModeAny,
					AllowedFunctionNames: []string{"myfunc"},
				},
				RetrievalConfig: nil,
			},
		},
		{
			name:      "unsupported type",
			input:     &openai.ChatCompletionToolChoiceUnion{Value: 123},
			expectErr: "tool_choice type not supported",
		},
		{
			name:      "unsupported string value",
			input:     &openai.ChatCompletionToolChoiceUnion{Value: "invalid"},
			expectErr: "unsupported tool_choice value 'invalid'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := openAIToolChoiceToGeminiToolConfig(tc.input)
			if tc.expectErr != "" {
				require.ErrorContains(t, err, tc.expectErr)
			} else {
				require.NoError(t, err)
				if diff := cmp.Diff(tc.expected, got, cmpopts.IgnoreUnexported(genai.ToolConfig{}, genai.FunctionCallingConfig{})); diff != "" {
					t.Errorf("ToolConfig mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestGeminiLogprobsToOpenAILogprobs(t *testing.T) {
	tests := []struct {
		name     string
		input    genai.LogprobsResult
		expected openai.ChatCompletionChoicesLogprobs
	}{
		{
			name:     "empty logprobs result",
			input:    genai.LogprobsResult{},
			expected: openai.ChatCompletionChoicesLogprobs{},
		},
		{
			name: "single chosen candidate without top candidates",
			input: genai.LogprobsResult{
				ChosenCandidates: []*genai.LogprobsResultCandidate{
					{
						Token:          "hello",
						LogProbability: -0.5,
					},
				},
			},
			expected: openai.ChatCompletionChoicesLogprobs{
				Content: []openai.ChatCompletionTokenLogprob{
					{
						Token:       "hello",
						Logprob:     -0.5,
						TopLogprobs: nil,
					},
				},
			},
		},
		{
			name: "multiple chosen candidates with top candidates",
			input: genai.LogprobsResult{
				ChosenCandidates: []*genai.LogprobsResultCandidate{
					{
						Token:          "hello",
						LogProbability: -0.5,
					},
					{
						Token:          "world",
						LogProbability: -0.3,
					},
				},
				TopCandidates: []*genai.LogprobsResultTopCandidates{
					{
						Candidates: []*genai.LogprobsResultCandidate{
							{Token: "hello", LogProbability: -0.5},
							{Token: "hi", LogProbability: -1.2},
							{Token: "hey", LogProbability: -1.5},
						},
					},
					{
						Candidates: []*genai.LogprobsResultCandidate{
							{Token: "world", LogProbability: -0.3},
							{Token: "earth", LogProbability: -1.1},
						},
					},
				},
			},
			expected: openai.ChatCompletionChoicesLogprobs{
				Content: []openai.ChatCompletionTokenLogprob{
					{
						Token:   "hello",
						Logprob: -0.5,
						TopLogprobs: []openai.ChatCompletionTokenLogprobTopLogprob{
							{Token: "hello", Logprob: -0.5},
							{Token: "hi", Logprob: -1.2},
							{Token: "hey", Logprob: -1.5},
						},
					},
					{
						Token:   "world",
						Logprob: -0.3,
						TopLogprobs: []openai.ChatCompletionTokenLogprobTopLogprob{
							{Token: "world", Logprob: -0.3},
							{Token: "earth", Logprob: -1.1},
						},
					},
				},
			},
		},
		{
			name: "chosen candidates with nil top candidates entry",
			input: genai.LogprobsResult{
				ChosenCandidates: []*genai.LogprobsResultCandidate{
					{
						Token:          "test",
						LogProbability: -0.8,
					},
				},
				TopCandidates: []*genai.LogprobsResultTopCandidates{
					nil,
				},
			},
			expected: openai.ChatCompletionChoicesLogprobs{
				Content: []openai.ChatCompletionTokenLogprob{
					{
						Token:       "test",
						Logprob:     -0.8,
						TopLogprobs: nil,
					},
				},
			},
		},
		{
			name: "chosen candidates with empty top candidates",
			input: genai.LogprobsResult{
				ChosenCandidates: []*genai.LogprobsResultCandidate{
					{
						Token:          "empty",
						LogProbability: -0.2,
					},
				},
				TopCandidates: []*genai.LogprobsResultTopCandidates{
					{
						Candidates: []*genai.LogprobsResultCandidate{},
					},
				},
			},
			expected: openai.ChatCompletionChoicesLogprobs{
				Content: []openai.ChatCompletionTokenLogprob{
					{
						Token:       "empty",
						Logprob:     -0.2,
						TopLogprobs: nil,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := geminiLogprobsToOpenAILogprobs(tt.input)
			// Use cmp.Equal with EquateApprox for float comparison due to float32->float64 conversion.
			if !cmp.Equal(tt.expected, result, cmpopts.EquateApprox(0, 0.0001)) {
				t.Errorf("geminiLogprobsToOpenAILogprobs() diff:\n%s", cmp.Diff(tt.expected, result, cmpopts.EquateApprox(0, 0.0001)))
			}
		})
	}
}

func TestExtractToolCallsFromGeminiParts(t *testing.T) {
	toolCalls := []openai.ChatCompletionMessageToolCallParam{}
	tests := []struct {
		name     string
		input    []*genai.Part
		expected []openai.ChatCompletionMessageToolCallParam
		wantErr  bool
	}{
		{
			name:     "nil parts",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty parts",
			input:    []*genai.Part{},
			expected: nil,
		},
		{
			name: "parts without function calls",
			input: []*genai.Part{
				{Text: "some text"},
				nil,
				{Text: "more text"},
			},
			expected: nil,
		},
		{
			name: "single function call",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_weather",
						Args: map[string]any{
							"location": "San Francisco",
							"unit":     "celsius",
						},
					},
				},
			},
			expected: []openai.ChatCompletionMessageToolCallParam{
				{
					ID:   ptr.To("0"),
					Type: openai.ChatCompletionMessageToolCallTypeFunction,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "get_weather",
						Arguments: `{"location":"San Francisco","unit":"celsius"}`,
					},
				},
			},
		},
		{
			name: "multiple function calls",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "function1",
						Args: map[string]any{"param1": "value1"},
					},
				},
				{Text: "some text between"},
				{
					FunctionCall: &genai.FunctionCall{
						Name: "function2",
						Args: map[string]any{"param2": float64(42)},
					},
				},
			},
			expected: []openai.ChatCompletionMessageToolCallParam{
				{
					ID:   ptr.To("0"),
					Type: openai.ChatCompletionMessageToolCallTypeFunction,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "function1",
						Arguments: `{"param1":"value1"}`,
					},
				},
				{
					ID:   ptr.To("1"),
					Type: openai.ChatCompletionMessageToolCallTypeFunction,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "function2",
						Arguments: `{"param2":42}`,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, _, err := extractToolCallsFromGeminiParts(toolCalls, tt.input, json.MarshalForDeterministicTesting)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Normalize IDs since they're generated.
			for i := range calls {
				calls[i].ID = ptr.To(fmt.Sprintf("%d", i))
			}

			require.Equal(t, tt.expected, calls)
		})
	}
}

func TestGeminiFinishReasonToOpenAI(t *testing.T) {
	tests := []struct {
		name      string
		input     genai.FinishReason
		toolCalls []openai.ChatCompletionMessageToolCallParam
		expected  openai.ChatCompletionChoicesFinishReason
	}{
		{
			name:      "stop reason",
			input:     genai.FinishReasonStop,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonStop,
		},
		{
			name:  "tool calls reason",
			input: genai.FinishReasonStop,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{
				{
					ID: ptr.To("tool_call_1"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "example_tool",
						Arguments: "{\"param1\":\"value1\"}",
					},
					Type: openai.ChatCompletionMessageToolCallTypeFunction,
				},
			},
			expected: openai.ChatCompletionChoicesFinishReasonToolCalls,
		},
		{
			name:      "max tokens reason",
			input:     genai.FinishReasonMaxTokens,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonLength,
		},
		{
			name:      "empty reason for streaming",
			input:     "",
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  "",
		},
		{
			name:      "safety reason",
			input:     genai.FinishReasonSafety,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
		{
			name:      "blocklist reason",
			input:     genai.FinishReasonBlocklist,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
		{
			name:      "prohibited content reason",
			input:     genai.FinishReasonProhibitedContent,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
		{
			name:      "spii reason",
			input:     genai.FinishReasonSPII,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
		{
			name:      "image safety reason",
			input:     genai.FinishReasonImageSafety,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
		{
			name:      "image prohibited content reason",
			input:     genai.FinishReasonImageProhibitedContent,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
		{
			name:      "recitation reason",
			input:     genai.FinishReasonRecitation,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonRecitation,
		},
		{
			name:      "image recitation reason",
			input:     genai.FinishReasonImageRecitation,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonRecitation,
		},
		{
			name:      "malformed function call reason",
			input:     genai.FinishReasonMalformedFunctionCall,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonMalformedFunctionCall,
		},
		{
			name:      "unexpected tool call reason",
			input:     genai.FinishReasonUnexpectedToolCall,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonUnexpectedToolCall,
		},
		{
			name:      "language reason",
			input:     genai.FinishReasonLanguage,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonLanguage,
		},
		{
			name:      "no image reason",
			input:     genai.FinishReasonNoImage,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonNoImage,
		},
		{
			name:      "other reason",
			input:     genai.FinishReasonOther,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonError,
		},
		{
			name:      "image other reason",
			input:     genai.FinishReasonImageOther,
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonError,
		},
		{
			name:      "unknown reason",
			input:     genai.FinishReason("unknown_reason"),
			toolCalls: []openai.ChatCompletionMessageToolCallParam{},
			expected:  openai.ChatCompletionChoicesFinishReasonError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := geminiFinishReasonToOpenAI(tt.input, tt.toolCalls)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractTextAndThoughtSummaryFromGeminiParts(t *testing.T) {
	tests := []struct {
		name                   string
		parts                  []*genai.Part
		responseMode           geminiResponseMode
		expectedThoughtSummary string
		expectedText           string
		expectedSignature      string
	}{
		{
			name:                   "nil parts",
			parts:                  nil,
			responseMode:           responseModeNone,
			expectedThoughtSummary: "",
			expectedText:           "",
		},
		{
			name:                   "empty parts",
			parts:                  []*genai.Part{},
			responseMode:           responseModeNone,
			expectedThoughtSummary: "",
			expectedText:           "",
		},
		{
			name: "multiple text parts without regex mode",
			parts: []*genai.Part{
				{Text: "Hello, "},
				{Text: "world!"},
			},
			responseMode:           responseModeJSON,
			expectedThoughtSummary: "",
			expectedText:           "Hello, world!",
		},
		{
			name: "regex mode with mixed quoted and unquoted text",
			parts: []*genai.Part{
				{Text: `"positive"`},
				{Text: `unquoted`},
				{Text: `"negative"`},
			},
			responseMode:           responseModeRegex,
			expectedThoughtSummary: "",
			expectedText:           "positiveunquotednegative",
		},
		{
			name: "regex mode with only double-quoted first and last words",
			parts: []*genai.Part{
				{Text: "\"\"ERROR\" Unable to connect to database \"DatabaseModule\"\""},
			},
			responseMode:           responseModeRegex,
			expectedThoughtSummary: "",
			expectedText:           "\"ERROR\" Unable to connect to database \"DatabaseModule\"",
		},
		{
			name: "non-regex mode with double-quoted text (should not remove quotes)",
			parts: []*genai.Part{
				{Text: `"positive"`},
			},
			responseMode:           responseModeJSON,
			expectedThoughtSummary: "",
			expectedText:           `"positive"`,
		},
		{
			name: "regex mode with text containing internal quotes",
			parts: []*genai.Part{
				{Text: `"He said \"hello\" to me"`},
			},
			responseMode:           responseModeRegex,
			expectedThoughtSummary: "",
			expectedText:           `He said \"hello\" to me`,
		},
		{
			name: "enum mode trims surrounding whitespace",
			parts: []*genai.Part{
				{Text: " negative"},
			},
			responseMode:           responseModeEnum,
			expectedThoughtSummary: "",
			expectedText:           "negative",
		},
		{
			name: "non-enum mode preserves surrounding whitespace",
			parts: []*genai.Part{
				{Text: " negative"},
			},
			responseMode:           responseModeJSON,
			expectedThoughtSummary: "",
			expectedText:           " negative",
		},
		{
			name: "test thought summary",
			parts: []*genai.Part{
				{Text: "Let me think step by step", Thought: true},
				{Text: "Here is the conclusion"},
			},
			expectedThoughtSummary: "Let me think step by step",
			expectedText:           "Here is the conclusion",
		},
		{
			name: "thought then visible text with ThoughtSignature on text part",
			parts: []*genai.Part{
				{Text: "internal reasoning", Thought: true},
				{Text: "answer", Thought: false, ThoughtSignature: []byte{0xab, 0xcd}},
			},
			responseMode:           responseModeNone,
			expectedThoughtSummary: "internal reasoning",
			expectedText:           "answer",
			expectedSignature:      base64.StdEncoding.EncodeToString([]byte{0xab, 0xcd}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			thoughtSummary, text, signature := extractTextAndThoughtSummaryFromGeminiParts(tc.parts, tc.responseMode)
			if thoughtSummary != tc.expectedThoughtSummary {
				t.Errorf("thought summary result of extractTextAndThoughtSummaryFromGeminiParts() = %q, want %q", thoughtSummary, tc.expectedThoughtSummary)
			}
			if text != tc.expectedText {
				t.Errorf("text result of extractTextAndThoughtSummaryFromGeminiParts() = %q, want %q", text, tc.expectedText)
			}
			if signature != tc.expectedSignature {
				t.Errorf("signature result of extractTextAndThoughtSummaryFromGeminiParts() = %q, want %q", signature, tc.expectedSignature)
			}
		})
	}
}

func TestGeminiCandidatesToOpenAIChoices(t *testing.T) {
	toolSigB64 := base64.StdEncoding.EncodeToString([]byte("tool-sig"))
	thoughtThenTool := []*genai.Part{
		{Text: "planning step", Thought: true},
		{
			FunctionCall: &genai.FunctionCall{
				Name: "get_weather",
				Args: map[string]any{"city": "NYC"},
			},
			ThoughtSignature: []byte("tool-sig"),
		},
	}

	tests := []struct {
		name         string
		candidates   []*genai.Candidate
		responseMode geminiResponseMode
		want         []openai.ChatCompletionResponseChoice
		wantErr      string
	}{
		{
			name: "plain text assistant message",
			candidates: []*genai.Candidate{
				{
					Content:      &genai.Content{Parts: []*genai.Part{{Text: "hello"}}},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    openai.ChatMessageRoleAssistant,
						Content: ptr.To("hello"),
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				},
			},
		},
		{
			name: "no thought text but signature on text part still creates thinking_block",
			candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "just an answer", ThoughtSignature: []byte("sig-only")},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: openai.ChatMessageRoleAssistant,
						ThinkingBlocks: []openai.ThinkingBlock{
							{Type: "thinking", Signature: base64.StdEncoding.EncodeToString([]byte("sig-only"))},
						},
						Content: ptr.To("just an answer"),
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				},
			},
		},
		{
			name: "thought summary without signature sets reasoning_content only",
			candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "Let me reason", Thought: true},
							{Text: "final answer"},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:             openai.ChatMessageRoleAssistant,
						ReasoningContent: &openai.ReasoningContentUnion{Value: "Let me reason"},
						Content:          ptr.To("final answer"),
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				},
			},
		},
		{
			name: "tool call with thought signature only yields signature thinking block",
			candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{
								FunctionCall: &genai.FunctionCall{
									Name: "fn",
									Args: map[string]any{"k": 1},
								},
								ThoughtSignature: []byte("tool-sig"),
							},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: openai.ChatMessageRoleAssistant,
						ThinkingBlocks: []openai.ThinkingBlock{
							{Type: "thinking", Signature: toolSigB64},
						},
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("id-0"),
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "fn",
									Arguments: `{"k":1}`,
								},
							},
						},
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
				},
			},
		},
		{
			name: "thought with text-part signature sets reasoning_content and thinking_blocks",
			candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "internal reasoning", Thought: true},
							{Text: "visible answer", ThoughtSignature: []byte("text-sig")},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:             openai.ChatMessageRoleAssistant,
						ReasoningContent: &openai.ReasoningContentUnion{Value: "internal reasoning"},
						ThinkingBlocks: []openai.ThinkingBlock{
							{Type: "thinking", Thinking: "internal reasoning", Signature: base64.StdEncoding.EncodeToString([]byte("text-sig"))},
						},
						Content: ptr.To("visible answer"),
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				},
			},
		},
		{
			name: "thought plus tool creates thinking block from tool signature only",
			candidates: []*genai.Candidate{
				{
					Content:      &genai.Content{Parts: thoughtThenTool},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:             openai.ChatMessageRoleAssistant,
						ReasoningContent: &openai.ReasoningContentUnion{Value: "planning step"},
						ThinkingBlocks: []openai.ThinkingBlock{
							{Type: "thinking", Signature: toolSigB64},
						},
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("id-0"),
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_weather",
									Arguments: `{"city":"NYC"}`,
								},
							},
						},
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
				},
			},
		},
		{
			name: "thought plus text-sig plus tool-sig merges tool sig into existing block",
			candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "deep reasoning", Thought: true},
							{Text: "visible", ThoughtSignature: []byte("text-sig")},
							{FunctionCall: &genai.FunctionCall{Name: "fn", Args: map[string]any{"a": 1}}, ThoughtSignature: []byte("tool-sig")},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:             openai.ChatMessageRoleAssistant,
						ReasoningContent: &openai.ReasoningContentUnion{Value: "deep reasoning"},
						ThinkingBlocks: []openai.ThinkingBlock{
							{Type: "thinking", Thinking: "deep reasoning", Signature: toolSigB64},
						},
						Content: ptr.To("visible"),
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("id-0"),
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name: "fn", Arguments: `{"a":1}`,
								},
							},
						},
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
				},
			},
		},
		{
			name: "no thought but text-sig plus tool-sig merges tool sig into existing block",
			candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "answer", ThoughtSignature: []byte("text-sig")},
							{FunctionCall: &genai.FunctionCall{Name: "fn", Args: map[string]any{"b": 2}}, ThoughtSignature: []byte("tool-sig")},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			responseMode: responseModeNone,
			want: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: openai.ChatMessageRoleAssistant,
						ThinkingBlocks: []openai.ThinkingBlock{
							{Type: "thinking", Signature: toolSigB64},
						},
						Content: ptr.To("answer"),
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("id-0"),
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name: "fn", Arguments: `{"b":2}`,
								},
							},
						},
					},
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := geminiCandidatesToOpenAIChoices(tc.candidates, tc.responseMode)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Tool call IDs are random UUIDs; normalize for comparison.
			for i := range got {
				for j := range got[i].Message.ToolCalls {
					got[i].Message.ToolCalls[j].ID = ptr.To(fmt.Sprintf("id-%d", j))
				}
			}

			if d := cmp.Diff(tc.want, got); d != "" {
				t.Errorf("geminiCandidatesToOpenAIChoices() mismatch (-want +got):\n%s", d)
			}
		})
	}
}

func TestOpenAIReqToGeminiGenerationConfigWithJsonSchemaToGemini(t *testing.T) {
	tests := []struct {
		name                     string
		input                    *openai.ChatCompletionRequest
		requestModel             internalapi.RequestModel
		expectedGenerationConfig *genai.GenerationConfig
		expectedResponseMode     geminiResponseMode
		expectedErrMsg           string
	}{
		{
			name: "json schema for older gemini model (uses jsonSchemaToGemini)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"name": {"type": "string"},
									"age": {"type": "number"}
								},
								"required": ["name", "age"]
							}`),
						},
					},
				},
			},
			requestModel: "gemini-2.0-flash", // older model that doesn't support native JSON schema
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"name": {Type: "string"},
						"age":  {Type: "number"},
					},
					Required: []string{"name", "age"},
				},
			},
			expectedResponseMode: responseModeJSON,
		},
		{
			name: "complex json schema with nested objects (uses jsonSchemaToGemini)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"user": {
										"type": "object",
										"properties": {
											"name": {"type": "string"},
											"contact": {
												"type": "object",
												"properties": {
													"email": {"type": "string"},
													"phone": {"type": "string"}
												}
											}
										}
									},
									"items": {
										"type": "array",
										"items": {
											"type": "object",
											"properties": {
												"id": {"type": "number"},
												"description": {"type": "string"}
											}
										}
									}
								},
								"required": ["user", "items"]
							}`),
						},
					},
				},
			},
			requestModel: "gemini-2.0-flash", // older model
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"user": {
							Type: "object",
							Properties: map[string]*genai.Schema{
								"name": {Type: "string"},
								"contact": {
									Type: "object",
									Properties: map[string]*genai.Schema{
										"email": {Type: "string"},
										"phone": {Type: "string"},
									},
								},
							},
						},
						"items": {
							Type: "array",
							Items: &genai.Schema{
								Type: "object",
								Properties: map[string]*genai.Schema{
									"id":          {Type: "number"},
									"description": {Type: "string"},
								},
							},
						},
					},
					Required: []string{"user", "items"},
				},
			},
			expectedResponseMode: responseModeJSON,
		},
		{
			name: "json schema with anyOf (uses jsonSchemaToGemini)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"value": {
										"anyOf": [
											{"type": "string"},
											{"type": "number"}
										]
									}
								}
							}`),
						},
					},
				},
			},
			requestModel: "gemini-2.0-flash",
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"value": {
							AnyOf: []*genai.Schema{
								{Type: "string"},
								{Type: "number"},
							},
						},
					},
				},
			},
			expectedResponseMode: responseModeJSON,
		},
		{
			name: "json schema with nullable type (uses jsonSchemaToGemini)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"optional_field": {
										"anyOf": [
											{"type": "string"},
											{"type": "null"}
										]
									}
								}
							}`),
						},
					},
				},
			},
			requestModel: "gemini-2.0-flash",
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"optional_field": {
							AnyOf: []*genai.Schema{
								{Type: "string"},
							},
							Nullable: ptr.To(true),
						},
					},
				},
			},
			expectedResponseMode: responseModeJSON,
		},
		{
			name: "json schema with $ref (uses jsonSchemaToGemini)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"user": {"$ref": "#/$defs/User"}
								},
								"$defs": {
									"User": {
										"type": "object",
										"properties": {
											"name": {"type": "string"},
											"age": {"type": "number"}
										}
									}
								}
							}`),
						},
					},
				},
			},
			requestModel: "gemini-2.0-flash",
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"user": {
							Type: "object",
							Properties: map[string]*genai.Schema{
								"name": {Type: "string"},
								"age":  {Type: "number"},
							},
						},
					},
				},
			},
			expectedResponseMode: responseModeJSON,
		},
		{
			name: "invalid json schema causes jsonSchemaToGemini error",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"invalid_ref": {"$ref": "#/nonexistent/path"}
								}
							}`),
						},
					},
				},
			},
			requestModel:   "gemini-2.0-flash",
			expectedErrMsg: "invalid JSON schema",
		},
		{
			name: "complex nested schema with arrays and refs (uses jsonSchemaToGemini)",
			input: &openai.ChatCompletionRequest{
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Schema: json.RawMessage(`{
								"type": "object",
								"properties": {
									"steps": {
										"type": "array",
										"items": {"$ref": "#/$defs/Step"}
									},
									"final_answer": {"type": "string"}
								},
								"required": ["steps", "final_answer"],
								"$defs": {
									"Step": {
										"type": "object",
										"properties": {
											"explanation": {"type": "string"},
											"output": {"type": "string"}
										},
										"required": ["explanation", "output"]
									}
								}
							}`),
						},
					},
				},
			},
			requestModel: "gemini-2.0-flash",
			expectedGenerationConfig: &genai.GenerationConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"steps": {
							Type: "array",
							Items: &genai.Schema{
								Type: "object",
								Properties: map[string]*genai.Schema{
									"explanation": {Type: "string"},
									"output":      {Type: "string"},
								},
								Required: []string{"explanation", "output"},
							},
						},
						"final_answer": {Type: "string"},
					},
					Required: []string{"steps", "final_answer"},
				},
			},
			expectedResponseMode: responseModeJSON,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, responseMode, err := openAIReqToGeminiGenerationConfig(tc.input, tc.requestModel)
			if tc.expectedErrMsg != "" {
				require.ErrorContains(t, err, tc.expectedErrMsg)
			} else {
				require.NoError(t, err)

				if diff := cmp.Diff(tc.expectedGenerationConfig, got, cmpopts.IgnoreUnexported(genai.GenerationConfig{}, genai.Schema{})); diff != "" {
					t.Errorf("GenerationConfig mismatch (-want +got):\n%s", diff)
				}

				if responseMode != tc.expectedResponseMode {
					t.Errorf("geminiResponseMode mismatch: got %v, want %v", responseMode, tc.expectedResponseMode)
				}
			}
		})
	}
}

func TestMapDetailMediaResolution(t *testing.T) {
	tests := []struct {
		name               string
		detail             openai.ChatCompletionContentPartImageImageURLDetail
		expectedResolution genai.PartMediaResolutionLevel
		expectedErrorMsg   string
	}{
		{
			name:               "low detail",
			detail:             openai.ChatCompletionContentPartImageImageURLDetailLow,
			expectedResolution: genai.PartMediaResolutionLevelMediaResolutionLow,
		},
		{
			name:               "medium detail",
			detail:             openai.ChatCompletionContentPartImageImageURLDetailMedium,
			expectedResolution: genai.PartMediaResolutionLevelMediaResolutionMedium,
		},
		{
			name:               "high detail",
			detail:             openai.ChatCompletionContentPartImageImageURLDetailHigh,
			expectedResolution: genai.PartMediaResolutionLevelMediaResolutionHigh,
		},
		{
			name:               "auto detail - returns unspecified for default behavior",
			detail:             openai.ChatCompletionContentPartImageImageURLDetailAuto,
			expectedResolution: genai.PartMediaResolutionLevelMediaResolutionUnspecified,
		},
		{
			name:             "empty string detail",
			detail:           openai.ChatCompletionContentPartImageImageURLDetail(""),
			expectedErrorMsg: "unsupported detail level",
		},
		{
			name:             "unknown detail value",
			detail:           openai.ChatCompletionContentPartImageImageURLDetail("unknown"),
			expectedErrorMsg: "unsupported detail level",
		},
		{
			name:             "invalid detail value",
			detail:           openai.ChatCompletionContentPartImageImageURLDetail("invalid_value"),
			expectedErrorMsg: "unsupported detail level",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolution, err := mapDetailMediaResolution(tc.detail)

			if tc.expectedErrorMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
				assert.Equal(t, genai.PartMediaResolutionLevel(""), resolution)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedResolution, resolution)
			}
		})
	}
}

func TestMapReasoningEffortToThinkingLevel(t *testing.T) {
	tests := []struct {
		name             string
		reasoningEffort  openai.ReasoningEffort
		model            internalapi.RequestModel
		expectedThinking genai.ThinkingLevel
		expectedErrorMsg string
	}{
		{
			name:             "none effort on Flash maps to ThinkingLevelMinimal",
			reasoningEffort:  "none",
			model:            "gemini-3-flash",
			expectedThinking: genai.ThinkingLevelMinimal,
		},
		{
			name:             "none effort on Pro returns error",
			reasoningEffort:  "none",
			model:            "gemini-3-pro",
			expectedErrorMsg: "reasoning effort 'none' is only supported for Gemini Flash models",
		},
		{
			name:             "low effort maps to ThinkingLevelLow",
			reasoningEffort:  openai.ReasoningEffortLow,
			model:            "gemini-3-flash",
			expectedThinking: genai.ThinkingLevelLow,
		},
		{
			name:             "medium effort on Flash maps to ThinkingLevelMedium",
			reasoningEffort:  openai.ReasoningEffortMedium,
			model:            "gemini-3-flash",
			expectedThinking: genai.ThinkingLevelMedium,
		},
		{
			name:             "medium effort on Pro maps to ThinkingLevelHigh",
			reasoningEffort:  openai.ReasoningEffortMedium,
			model:            "gemini-3-pro",
			expectedThinking: genai.ThinkingLevelHigh,
		},
		{
			name:             "high effort maps to ThinkingLevelHigh",
			reasoningEffort:  openai.ReasoningEffortHigh,
			model:            "gemini-3-flash",
			expectedThinking: genai.ThinkingLevelHigh,
		},
		{
			name:             "empty effort - not supported",
			reasoningEffort:  "",
			model:            "gemini-3-flash",
			expectedErrorMsg: "unsupported reasoning effort level",
		},
		{
			name:             "unknown effort - not supported",
			reasoningEffort:  "unknown",
			model:            "gemini-3-flash",
			expectedErrorMsg: "unsupported reasoning effort level",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			thinking, err := mapReasoningEffortToThinkingLevel(tc.reasoningEffort, tc.model)

			if tc.expectedErrorMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
				assert.Equal(t, genai.ThinkingLevel(""), thinking)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedThinking, thinking)
			}
		})
	}
}
