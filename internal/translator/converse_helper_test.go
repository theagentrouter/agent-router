// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOpenAIToolsToBedrockToolConfig(t *testing.T) {
	weatherTool := openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "get_current_weather",
			Description: "Get the current weather in a given location",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	tests := []struct {
		name       string
		tools      []openai.Tool
		toolChoice *openai.ChatCompletionToolChoiceUnion
		model      string
		expected   *awsbedrock.ToolConfiguration
		expErr     string
	}{
		{
			name:  "no tools, no tool choice",
			tools: nil,
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{},
			},
		},
		{
			name:  "tool without description",
			tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "noop"}}},
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("noop"),
							Description: nil,
							InputSchema: &awsbedrock.ToolInputSchema{},
						},
					},
				},
			},
		},
		{
			name:  "tool_choice auto",
			tools: []openai.Tool{weatherTool},
			toolChoice: &openai.ChatCompletionToolChoiceUnion{
				Value: "auto",
			},
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("get_current_weather"),
							Description: ptr.To("Get the current weather in a given location"),
							InputSchema: &awsbedrock.ToolInputSchema{JSON: map[string]any{"type": "object"}},
						},
					},
				},
				ToolChoice: &awsbedrock.ToolChoice{Auto: &awsbedrock.AutoToolChoice{}},
			},
		},
		{
			name:  "tool_choice required",
			tools: []openai.Tool{weatherTool},
			toolChoice: &openai.ChatCompletionToolChoiceUnion{
				Value: "required",
			},
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("get_current_weather"),
							Description: ptr.To("Get the current weather in a given location"),
							InputSchema: &awsbedrock.ToolInputSchema{JSON: map[string]any{"type": "object"}},
						},
					},
				},
				ToolChoice: &awsbedrock.ToolChoice{Any: &awsbedrock.AnyToolChoice{}},
			},
		},
		{
			name:  "named tool_choice string is ignored for non-anthropic models",
			tools: []openai.Tool{weatherTool},
			toolChoice: &openai.ChatCompletionToolChoiceUnion{
				Value: "get_current_weather",
			},
			model: "amazon.nova-pro-v1:0",
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("get_current_weather"),
							Description: ptr.To("Get the current weather in a given location"),
							InputSchema: &awsbedrock.ToolInputSchema{JSON: map[string]any{"type": "object"}},
						},
					},
				},
				ToolChoice: nil,
			},
		},
		{
			name:  "named tool_choice string applies for anthropic claude models",
			tools: []openai.Tool{weatherTool},
			toolChoice: &openai.ChatCompletionToolChoiceUnion{
				Value: "get_current_weather",
			},
			model: "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("get_current_weather"),
							Description: ptr.To("Get the current weather in a given location"),
							InputSchema: &awsbedrock.ToolInputSchema{JSON: map[string]any{"type": "object"}},
						},
					},
				},
				ToolChoice: &awsbedrock.ToolChoice{Tool: &awsbedrock.SpecificToolChoice{Name: ptr.To("get_current_weather")}},
			},
		},
		{
			name:  "named tool_choice object",
			tools: []openai.Tool{weatherTool},
			toolChoice: &openai.ChatCompletionToolChoiceUnion{
				Value: openai.ChatCompletionNamedToolChoice{
					Type: openai.ToolTypeFunction,
					Function: openai.ChatCompletionNamedToolChoiceFunction{
						Name: "get_current_weather",
					},
				},
			},
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("get_current_weather"),
							Description: ptr.To("Get the current weather in a given location"),
							InputSchema: &awsbedrock.ToolInputSchema{JSON: map[string]any{"type": "object"}},
						},
					},
				},
				ToolChoice: &awsbedrock.ToolChoice{Tool: &awsbedrock.SpecificToolChoice{Name: ptr.To("get_current_weather")}},
			},
		},
		{
			name: "cache control on a tool appends a separate cache point element",
			tools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        "get_current_weather",
						Description: "Get the current weather in a given location",
						Parameters:  map[string]any{"type": "object"},
						AnthropicContentFields: &openai.AnthropicContentFields{
							CacheControl: anthropic.CacheControlEphemeralParam{
								Type: constant.ValueOf[constant.Ephemeral](),
							},
						},
					},
				},
			},
			expected: &awsbedrock.ToolConfiguration{
				Tools: []*awsbedrock.Tool{
					{
						ToolSpec: &awsbedrock.ToolSpecification{
							Name:        ptr.To("get_current_weather"),
							Description: ptr.To("Get the current weather in a given location"),
							InputSchema: &awsbedrock.ToolInputSchema{JSON: map[string]any{"type": "object"}},
						},
					},
					{CachePoint: &awsbedrock.CachePointBlock{Type: "default"}},
				},
			},
		},
		{
			name:  "unsupported tool_choice type returns an error",
			tools: []openai.Tool{weatherTool},
			toolChoice: &openai.ChatCompletionToolChoiceUnion{
				Value: 123,
			},
			expErr: "tool_choice type not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openAIToolsToBedrockToolConfig(tt.tools, tt.toolChoice, tt.model)
			if tt.expErr != "" {
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				require.ErrorContains(t, err, tt.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
			// Bedrock rejects a tools[] element that sets more than one of the union's members,
			// and it rejects an explicit "toolSpec":null alongside a cache point.
			for i, tool := range got.Tools {
				require.False(t, tool.ToolSpec != nil && tool.CachePoint != nil, "tools[%d] sets both toolSpec and cachePoint", i)
				raw, err := json.Marshal(tool)
				require.NoError(t, err)
				if tool.ToolSpec == nil {
					require.NotContains(t, string(raw), "toolSpec", "tools[%d] marshals a toolSpec key without a spec", i)
				}
			}
		})
	}
}

func TestOpenAIMessageToBedrockMessageRoleUser(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleUser(&openai.ChatCompletionUserMessageParam{
			Content: openai.StringOrUserRoleContentUnion{Value: "hello"},
		}, openai.ChatMessageRoleUser)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role:    openai.ChatMessageRoleUser,
			Content: []*awsbedrock.ContentBlock{{Text: ptr.To("hello")}},
		}, msg)
	})

	t.Run("text part with cache point", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleUser(&openai.ChatCompletionUserMessageParam{
			Content: openai.StringOrUserRoleContentUnion{
				Value: []openai.ChatCompletionContentPartUserUnionParam{
					{OfText: &openai.ChatCompletionContentPartTextParam{Text: "hello"}},
				},
			},
		}, openai.ChatMessageRoleUser)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role:    openai.ChatMessageRoleUser,
			Content: []*awsbedrock.ContentBlock{{Text: ptr.To("hello")}},
		}, msg)
	})

	imageFormats := []struct {
		mime   string
		format string
	}{
		{"image/png", "png"},
		{"image/jpeg", "jpeg"},
		{"image/gif", "gif"},
		{"image/webp", "webp"},
	}
	for _, f := range imageFormats {
		t.Run("image part "+f.format, func(t *testing.T) {
			msg, err := openAIMessageToBedrockMessageRoleUser(&openai.ChatCompletionUserMessageParam{
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "data:" + f.mime + ";base64,dGVzdA==",
							},
						}},
					},
				},
			}, openai.ChatMessageRoleUser)
			require.NoError(t, err)
			require.Equal(t, &awsbedrock.Message{
				Role: openai.ChatMessageRoleUser,
				Content: []*awsbedrock.ContentBlock{{
					Image: &awsbedrock.ImageBlock{
						Format: f.format,
						Source: awsbedrock.ImageSource{Bytes: []byte("test")},
					},
				}},
			}, msg)
		})
	}

	t.Run("unsupported image format returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleUser(&openai.ChatCompletionUserMessageParam{
			Content: openai.StringOrUserRoleContentUnion{
				Value: []openai.ChatCompletionContentPartUserUnionParam{
					{OfImageURL: &openai.ChatCompletionContentPartImageParam{
						ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
							URL: "data:image/bmp;base64,dGVzdA==",
						},
					}},
				},
			},
		}, openai.ChatMessageRoleUser)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "unsupported image format")
	})

	t.Run("invalid data URI returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleUser(&openai.ChatCompletionUserMessageParam{
			Content: openai.StringOrUserRoleContentUnion{
				Value: []openai.ChatCompletionContentPartUserUnionParam{
					{OfImageURL: &openai.ChatCompletionContentPartImageParam{
						ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
							URL: "not-a-data-uri",
						},
					}},
				},
			},
		}, openai.ChatMessageRoleUser)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "invalid image data URI")
	})

	t.Run("unexpected content type returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleUser(&openai.ChatCompletionUserMessageParam{
			Content: openai.StringOrUserRoleContentUnion{Value: 123},
		}, openai.ChatMessageRoleUser)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "unexpected content type for user message")
	})
}

func TestUnmarshalToolCallArguments(t *testing.T) {
	t.Run("valid arguments", func(t *testing.T) {
		got, err := unmarshalToolCallArguments(`{"location":"Queens, NY"}`)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"location": "Queens, NY"}, got)
	})

	t.Run("invalid arguments returns an error", func(t *testing.T) {
		_, err := unmarshalToolCallArguments(`not-json`)
		require.ErrorContains(t, err, "failed to unmarshal tool call arguments")
	})
}

func TestOpenAIMessageToBedrockMessageRoleAssistant(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			Content: openai.StringOrAssistantRoleContentUnion{Value: "hi there"},
		}, openai.ChatMessageRoleAssistant)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role:    openai.ChatMessageRoleAssistant,
			Content: []*awsbedrock.ContentBlock{{Text: ptr.To("hi there")}},
		}, msg)
	})

	t.Run("thinking and refusal parts", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			Content: openai.StringOrAssistantRoleContentUnion{
				Value: []openai.ChatCompletionAssistantMessageParamContent{
					{
						Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
						Text:      ptr.To("reasoning..."),
						Signature: ptr.To("sig"),
					},
					{
						Type:    openai.ChatCompletionAssistantMessageParamContentTypeRefusal,
						Refusal: ptr.To("I can't help with that"),
					},
				},
			},
		}, openai.ChatMessageRoleAssistant)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role: openai.ChatMessageRoleAssistant,
			Content: []*awsbedrock.ContentBlock{
				{ReasoningContent: &awsbedrock.ReasoningContentBlock{
					ReasoningText: &awsbedrock.ReasoningTextBlock{Text: "reasoning...", Signature: "sig"},
				}},
				{Text: ptr.To("I can't help with that")},
			},
		}, msg)
	})

	t.Run("redacted thinking with bytes content", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			Content: openai.StringOrAssistantRoleContentUnion{
				Value: []openai.ChatCompletionAssistantMessageParamContent{
					{
						Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
						RedactedContent: &openai.RedactedContentUnion{Value: []byte("secret")},
					},
				},
			},
		}, openai.ChatMessageRoleAssistant)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role: openai.ChatMessageRoleAssistant,
			Content: []*awsbedrock.ContentBlock{
				{ReasoningContent: &awsbedrock.ReasoningContentBlock{RedactedContent: []byte("secret")}},
			},
		}, msg)
	})

	t.Run("redacted thinking with string content returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			Content: openai.StringOrAssistantRoleContentUnion{
				Value: []openai.ChatCompletionAssistantMessageParamContent{
					{
						Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
						RedactedContent: &openai.RedactedContentUnion{Value: "not-bytes"},
					},
				},
			},
		}, openai.ChatMessageRoleAssistant)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "redacted_content must be a binary/bytes value")
	})

	t.Run("tool calls are appended to content", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			ToolCalls: []openai.ChatCompletionMessageToolCallParam{
				{
					ID: ptr.To("call_1"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "get_current_weather",
						Arguments: `{"location":"Queens, NY"}`,
					},
				},
			},
		}, openai.ChatMessageRoleAssistant)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role: openai.ChatMessageRoleAssistant,
			Content: []*awsbedrock.ContentBlock{
				{ToolUse: &awsbedrock.ToolUseBlock{
					Name:      "get_current_weather",
					ToolUseID: "call_1",
					Input:     map[string]any{"location": "Queens, NY"},
				}},
			},
		}, msg)
	})

	t.Run("tool call missing id returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			ToolCalls: []openai.ChatCompletionMessageToolCallParam{
				{Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_current_weather", Arguments: "{}"}},
			},
		}, openai.ChatMessageRoleAssistant)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "missing required field 'id'")
	})

	t.Run("tool call with invalid arguments returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleAssistant(&openai.ChatCompletionAssistantMessageParam{
			ToolCalls: []openai.ChatCompletionMessageToolCallParam{
				{ID: ptr.To("call_1"), Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_current_weather", Arguments: "not-json"}},
			},
		}, openai.ChatMessageRoleAssistant)
		require.ErrorContains(t, err, "failed to unmarshal tool call arguments")
	})
}

func TestOpenAIMessageToBedrockMessageRoleSystem(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		var bedrockSystem []*awsbedrock.SystemContentBlock
		err := openAIMessageToBedrockMessageRoleSystem(&openai.ChatCompletionSystemMessageParam{
			Content: openai.ContentUnion{Value: "be helpful"},
		}, &bedrockSystem)
		require.NoError(t, err)
		require.Equal(t, []*awsbedrock.SystemContentBlock{{Text: ptr.To("be helpful")}}, bedrockSystem)
	})

	t.Run("content parts", func(t *testing.T) {
		var bedrockSystem []*awsbedrock.SystemContentBlock
		err := openAIMessageToBedrockMessageRoleSystem(&openai.ChatCompletionSystemMessageParam{
			Content: openai.ContentUnion{
				Value: []openai.ChatCompletionContentPartTextParam{{Text: "part1"}, {Text: "part2"}},
			},
		}, &bedrockSystem)
		require.NoError(t, err)
		require.Equal(t, []*awsbedrock.SystemContentBlock{{Text: ptr.To("part1")}, {Text: ptr.To("part2")}}, bedrockSystem)
	})

	t.Run("unexpected content type returns an error", func(t *testing.T) {
		var bedrockSystem []*awsbedrock.SystemContentBlock
		err := openAIMessageToBedrockMessageRoleSystem(&openai.ChatCompletionSystemMessageParam{
			Content: openai.ContentUnion{Value: 123},
		}, &bedrockSystem)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "unexpected content type for system message")
	})
}

func TestOpenAIMessageToBedrockMessageRoleTool(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleTool(&openai.ChatCompletionToolMessageParam{
			Content:    openai.ContentUnion{Value: "70F and clear skies"},
			ToolCallID: "call_1",
		}, openai.ChatMessageRoleTool)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role: openai.ChatMessageRoleTool,
			Content: []*awsbedrock.ContentBlock{{
				ToolResult: &awsbedrock.ToolResultBlock{
					Content:   []*awsbedrock.ToolResultContentBlock{{Text: ptr.To("70F and clear skies")}},
					ToolUseID: ptr.To("call_1"),
				},
			}},
		}, msg)
	})

	t.Run("content parts", func(t *testing.T) {
		msg, err := openAIMessageToBedrockMessageRoleTool(&openai.ChatCompletionToolMessageParam{
			Content:    openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{{Text: "part1"}}},
			ToolCallID: "call_1",
		}, openai.ChatMessageRoleTool)
		require.NoError(t, err)
		require.Equal(t, &awsbedrock.Message{
			Role: openai.ChatMessageRoleTool,
			Content: []*awsbedrock.ContentBlock{{
				ToolResult: &awsbedrock.ToolResultBlock{
					Content:   []*awsbedrock.ToolResultContentBlock{{Text: ptr.To("part1")}},
					ToolUseID: ptr.To("call_1"),
				},
			}},
		}, msg)
	})

	t.Run("unexpected content type returns an error", func(t *testing.T) {
		_, err := openAIMessageToBedrockMessageRoleTool(&openai.ChatCompletionToolMessageParam{
			Content:    openai.ContentUnion{Value: 123},
			ToolCallID: "call_1",
		}, openai.ChatMessageRoleTool)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.ErrorContains(t, err, "message 'content' must be a string or an array")
	})
}
