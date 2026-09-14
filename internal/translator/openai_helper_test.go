// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// sseEvent holds parsed Anthropic SSE event data.
type sseEvent struct {
	eventType string
	data      string
}

// parseSSEEventsFromBytes parses raw Anthropic SSE output into individual events.
func parseSSEEventsFromBytes(output []byte) []sseEvent {
	var events []sseEvent
	for block := range bytes.SplitSeq(output, []byte("\n\n")) {
		block = bytes.TrimSpace(block)
		if len(block) == 0 {
			continue
		}
		var e sseEvent
		for line := range bytes.SplitSeq(block, []byte("\n")) {
			if after, ok := bytes.CutPrefix(line, []byte("event: ")); ok {
				e.eventType = string(after)
			} else if after, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
				e.data = string(after)
			}
		}
		if e.eventType != "" || e.data != "" {
			events = append(events, e)
		}
	}
	return events
}

// accumulateAnthropicMessage runs emitted Anthropic SSE bytes through anthropic-sdk-go's
// Message.Accumulate, the same logic a real Anthropic client uses to build usage from a stream.
func accumulateAnthropicMessage(t *testing.T, sse []byte) anthropicsdk.Message {
	t.Helper()
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(bytes.NewReader(sse)),
	}
	stream := ssestream.NewStream[anthropicsdk.MessageStreamEventUnion](ssestream.NewDecoder(resp), nil)
	var msg anthropicsdk.Message
	for stream.Next() {
		require.NoError(t, msg.Accumulate(stream.Current()))
	}
	require.NoError(t, stream.Err())
	return msg
}

func TestBuildOpenAIChatCompletionRequest(t *testing.T) {
	t.Run("basic model and message", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3-haiku",
			MaxTokens: 100,
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hello"}},
			},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		assert.Equal(t, "claude-3-haiku", req.Model)
		require.NotNil(t, req.MaxCompletionTokens)
		assert.Equal(t, int64(100), *req.MaxCompletionTokens)
		require.Len(t, req.Messages, 1)
		require.NotNil(t, req.Messages[0].OfUser)
		assert.Equal(t, openai.ChatMessageRoleUser, req.Messages[0].OfUser.Role)
		assert.Equal(t, "Hello", req.Messages[0].OfUser.Content.Value)
	})

	t.Run("model name override", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:    "claude-3-haiku",
			Messages: []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		req := buildOpenAIChatCompletionRequest(body, "gpt-4o")
		assert.Equal(t, "gpt-4o", req.Model)
	})

	t.Run("system prompt prepended as first message", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:  "claude-3",
			System: &anthropic.SystemPrompt{Text: "You are a helpful assistant."},
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}},
			},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.Len(t, req.Messages, 2)
		require.NotNil(t, req.Messages[0].OfSystem)
		assert.Equal(t, openai.ChatMessageRoleSystem, req.Messages[0].OfSystem.Role)
		assert.Equal(t, "You are a helpful assistant.", req.Messages[0].OfSystem.Content.Value)
		require.NotNil(t, req.Messages[1].OfUser)
	})

	t.Run("empty system prompt not prepended", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:  "claude-3",
			System: &anthropic.SystemPrompt{},
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}},
			},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.Len(t, req.Messages, 1)
		assert.Nil(t, req.Messages[0].OfSystem)
	})

	t.Run("multi-turn conversation", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model: "claude-3",
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}},
				{Role: anthropic.MessageRoleAssistant, Content: anthropic.MessageContent{Text: "Hello!"}},
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Bye"}},
			},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.Len(t, req.Messages, 3)
		assert.NotNil(t, req.Messages[0].OfUser)
		assert.NotNil(t, req.Messages[1].OfAssistant)
		assert.Equal(t, openai.ChatMessageRoleAssistant, req.Messages[1].OfAssistant.Role)
		assert.Equal(t, "Hello!", req.Messages[1].OfAssistant.Content.Value)
		assert.NotNil(t, req.Messages[2].OfUser)
	})

	t.Run("streaming sets stream_options", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:    "claude-3",
			Stream:   true,
			Messages: []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		assert.True(t, req.Stream)
		require.NotNil(t, req.StreamOptions)
		assert.True(t, req.StreamOptions.IncludeUsage)
	})

	t.Run("non-streaming has no stream_options", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:    "claude-3",
			Messages: []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		assert.False(t, req.Stream)
		assert.Nil(t, req.StreamOptions)
	})

	t.Run("temperature and topP passthrough", func(t *testing.T) {
		temp := 0.7
		topP := 0.95
		body := &anthropic.MessagesRequest{
			Model:       "claude-3",
			Temperature: &temp,
			TopP:        &topP,
			Messages:    []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.NotNil(t, req.Temperature)
		assert.Equal(t, &temp, req.Temperature)
		require.NotNil(t, req.TopP)
		assert.Equal(t, &topP, req.TopP)
	})

	t.Run("tools conversion", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model: "claude-3",
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Get weather"}},
			},
			Tools: []anthropic.ToolUnion{
				{Tool: &anthropic.Tool{
					Name:        "get_weather",
					Description: "Retrieve current weather information",
					InputSchema: json.RawMessage(`{"type":"object","required":["location"]}`),
				}},
			},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.Len(t, req.Tools, 1)
		assert.Equal(t, openai.ToolTypeFunction, req.Tools[0].Type)
		require.NotNil(t, req.Tools[0].Function)
		assert.Equal(t, "get_weather", req.Tools[0].Function.Name)
		assert.Equal(t, "Retrieve current weather information", req.Tools[0].Function.Description)
	})

	t.Run("no tools means tool choice not set even if body has it", func(t *testing.T) {
		tc := anthropic.ToolChoice{Auto: &anthropic.ToolChoiceAuto{Type: "auto"}}
		body := &anthropic.MessagesRequest{
			Model:      "claude-3",
			Messages:   []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			ToolChoice: &tc,
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		assert.Nil(t, req.Tools)
		assert.Nil(t, req.ToolChoice)
	})

	t.Run("tools with auto tool_choice", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:      "claude-3",
			Messages:   []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			Tools:      []anthropic.ToolUnion{{Tool: &anthropic.Tool{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}}},
			ToolChoice: ptr.To(anthropic.ToolChoice{Auto: &anthropic.ToolChoiceAuto{Type: "auto"}}),
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.NotNil(t, req.Tools)
		require.NotNil(t, req.ToolChoice)
		assert.Equal(t, string(openai.ToolChoiceTypeAuto), req.ToolChoice.Value)
	})
}

func TestAnthropicSystemPromptToText(t *testing.T) {
	tests := []struct {
		name     string
		system   *anthropic.SystemPrompt
		expected string
	}{
		{
			name:     "plain text",
			system:   &anthropic.SystemPrompt{Text: "You are helpful."},
			expected: "You are helpful.",
		},
		{
			name: "array of text blocks concatenated",
			system: &anthropic.SystemPrompt{
				Texts: []anthropic.TextBlockParam{
					{Text: "You are "},
					{Text: "very helpful."},
				},
			},
			expected: "You are very helpful.",
		},
		{
			name:     "empty system prompt",
			system:   &anthropic.SystemPrompt{},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, anthropicSystemPromptToText(tt.system))
		})
	}
}

func TestAnthropicContentToText(t *testing.T) {
	tests := []struct {
		name     string
		content  anthropic.MessageContent
		expected string
	}{
		{
			name:     "plain text field",
			content:  anthropic.MessageContent{Text: "Hello!"},
			expected: "Hello!",
		},
		{
			name: "array of text blocks",
			content: anthropic.MessageContent{
				Array: []anthropic.ContentBlockParam{
					{Text: &anthropic.TextBlockParam{Text: "Hello "}},
					{Text: &anthropic.TextBlockParam{Text: "world"}},
				},
			},
			expected: "Hello world",
		},
		{
			name: "array with nil text block is skipped",
			content: anthropic.MessageContent{
				Array: []anthropic.ContentBlockParam{
					{Text: nil},
					{Text: &anthropic.TextBlockParam{Text: "world"}},
				},
			},
			expected: "world",
		},
		{
			name:     "empty content",
			content:  anthropic.MessageContent{},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, anthropicContentToText(tt.content))
		})
	}
}

func TestAnthropicToolsToOpenAI(t *testing.T) {
	t.Run("nil tools returns nil", func(t *testing.T) {
		assert.Nil(t, anthropicToolsToOpenAI(nil))
	})

	t.Run("empty tools returns nil", func(t *testing.T) {
		assert.Nil(t, anthropicToolsToOpenAI([]anthropic.ToolUnion{}))
	})

	t.Run("single tool converted correctly", func(t *testing.T) {
		tools := []anthropic.ToolUnion{
			{Tool: &anthropic.Tool{
				Name:        "search",
				Description: "Search the web",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
			}},
		}
		result := anthropicToolsToOpenAI(tools)
		require.Len(t, result, 1)
		assert.Equal(t, openai.ToolTypeFunction, result[0].Type)
		require.NotNil(t, result[0].Function)
		assert.Equal(t, "search", result[0].Function.Name)
		assert.Equal(t, "Search the web", result[0].Function.Description)
	})

	t.Run("multiple tools all converted", func(t *testing.T) {
		tools := []anthropic.ToolUnion{
			{Tool: &anthropic.Tool{Name: "tool1", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			{Tool: &anthropic.Tool{Name: "tool2", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		}
		result := anthropicToolsToOpenAI(tools)
		require.Len(t, result, 2)
		assert.Equal(t, "tool1", result[0].Function.Name)
		assert.Equal(t, "tool2", result[1].Function.Name)
	})

	t.Run("schema reaches the backend as sent", func(t *testing.T) {
		const inputSchema = `{"type":"object","properties":{},"additionalProperties":false,"$defs":{"unit":{"enum":["c","f"]}}}`
		var tool anthropic.ToolUnion
		require.NoError(t, json.Unmarshal([]byte(`{"name":"get_time","input_schema":`+inputSchema+`}`), &tool))

		result := anthropicToolsToOpenAI([]anthropic.ToolUnion{tool})
		require.Len(t, result, 1)
		parameters, err := json.Marshal(result[0].Function.Parameters)
		require.NoError(t, err)
		require.JSONEq(t, inputSchema, string(parameters))
	})
}

func TestAnthropicToolChoiceToOpenAI(t *testing.T) {
	tests := []struct {
		name      string
		tc        anthropic.ToolChoice
		hasTools  bool
		expectNil bool
		expectVal any
	}{
		{
			name:      "zero value tool choice returns nil",
			tc:        anthropic.ToolChoice{},
			hasTools:  true,
			expectNil: true,
		},
		{
			name:      "no tools returns nil even with valid choice",
			tc:        anthropic.ToolChoice{Auto: &anthropic.ToolChoiceAuto{Type: "auto"}},
			hasTools:  false,
			expectNil: true,
		},
		{
			name:      "auto maps to auto",
			tc:        anthropic.ToolChoice{Auto: &anthropic.ToolChoiceAuto{Type: "auto"}},
			hasTools:  true,
			expectVal: string(openai.ToolChoiceTypeAuto),
		},
		{
			name:      "none maps to none",
			tc:        anthropic.ToolChoice{None: &anthropic.ToolChoiceNone{Type: "none"}},
			hasTools:  true,
			expectVal: string(openai.ToolChoiceTypeNone),
		},
		{
			name:      "any maps to required",
			tc:        anthropic.ToolChoice{Any: &anthropic.ToolChoiceAny{Type: "any"}},
			hasTools:  true,
			expectVal: string(openai.ToolChoiceTypeRequired),
		},
		{
			name:     "tool type with name maps to named tool choice",
			tc:       anthropic.ToolChoice{Tool: &anthropic.ToolChoiceTool{Type: "tool", Name: "search"}},
			hasTools: true,
			expectVal: openai.ChatCompletionNamedToolChoice{
				Type:     openai.ToolTypeFunction,
				Function: openai.ChatCompletionNamedToolChoiceFunction{Name: "search"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := anthropicToolChoiceToOpenAI(tt.tc, tt.hasTools)
			if tt.expectNil {
				assert.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			assert.Equal(t, tt.expectVal, result.Value)
		})
	}
}

func TestOpenAIResponseToAnthropic(t *testing.T) {
	t.Run("text content response", func(t *testing.T) {
		content := "Hello from the model!"
		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-123",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Content: &content,
						Role:    "assistant",
					},
				},
			},
			Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 20},
		}
		result := openAIResponseToAnthropic(resp, "gpt-4o")
		assert.Equal(t, "chatcmpl-123", result.ID)
		assert.Equal(t, "gpt-4o", result.Model)
		require.Len(t, result.Content, 1)
		require.NotNil(t, result.Content[0].Text)
		assert.Equal(t, "Hello from the model!", result.Content[0].Text.Text)
		require.NotNil(t, result.StopReason)
		assert.Equal(t, anthropic.StopReasonEndTurn, *result.StopReason)
		require.NotNil(t, result.Usage)
		assert.Equal(t, float64(10), result.Usage.InputTokens)
		assert.Equal(t, float64(20), result.Usage.OutputTokens)
	})

	t.Run("empty string content not added to blocks", func(t *testing.T) {
		empty := ""
		resp := &openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message:      openai.ChatCompletionResponseChoiceMessage{Content: &empty},
				},
			},
		}
		result := openAIResponseToAnthropic(resp, "test-model")
		assert.Nil(t, result.Content)
	})

	t.Run("nil content not added to blocks", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message:      openai.ChatCompletionResponseChoiceMessage{Content: nil},
				},
			},
		}
		result := openAIResponseToAnthropic(resp, "test-model")
		assert.Nil(t, result.Content)
	})

	t.Run("tool call response", func(t *testing.T) {
		callID := "call-abc"
		resp := &openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
					Message: openai.ChatCompletionResponseChoiceMessage{
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID: &callID,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_weather",
									Arguments: `{"location":"NYC"}`,
								},
							},
						},
					},
				},
			},
		}
		result := openAIResponseToAnthropic(resp, "test-model")
		require.Len(t, result.Content, 1)
		require.NotNil(t, result.Content[0].Tool)
		assert.Equal(t, "call-abc", result.Content[0].Tool.ID)
		assert.Equal(t, "get_weather", result.Content[0].Tool.Name)
		assert.Equal(t, map[string]any{"location": "NYC"}, result.Content[0].Tool.Input)
		require.NotNil(t, result.StopReason)
		assert.Equal(t, anthropic.StopReasonToolUse, *result.StopReason)
	})

	t.Run("malformed tool call arguments becomes empty map", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
					Message: openai.ChatCompletionResponseChoiceMessage{
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "broken_tool",
									Arguments: `{invalid json`,
								},
							},
						},
					},
				},
			},
		}
		result := openAIResponseToAnthropic(resp, "test-model")
		require.Len(t, result.Content, 1)
		require.NotNil(t, result.Content[0].Tool)
		assert.Equal(t, map[string]any{}, result.Content[0].Tool.Input)
	})

	t.Run("missing tool call ID becomes empty string", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
					Message: openai.ChatCompletionResponseChoiceMessage{
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID: nil,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "my_tool",
									Arguments: `{}`,
								},
							},
						},
					},
				},
			},
		}
		result := openAIResponseToAnthropic(resp, "test-model")
		require.Len(t, result.Content, 1)
		assert.Empty(t, result.Content[0].Tool.ID)
	})

	t.Run("no choices produces no content and no stop reason", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-empty",
			Model: "gpt-4o",
			Usage: openai.Usage{PromptTokens: 5},
		}
		result := openAIResponseToAnthropic(resp, "gpt-4o")
		assert.Nil(t, result.Content)
		assert.Nil(t, result.StopReason)
		assert.Equal(t, float64(5), result.Usage.InputTokens)
	})
}

func TestOpenAIFinishReasonToAnthropic(t *testing.T) {
	tests := []struct {
		reason   openai.ChatCompletionChoicesFinishReason
		expected anthropic.StopReason
	}{
		{openai.ChatCompletionChoicesFinishReasonStop, anthropic.StopReasonEndTurn},
		{openai.ChatCompletionChoicesFinishReasonLength, anthropic.StopReasonMaxTokens},
		{openai.ChatCompletionChoicesFinishReasonToolCalls, anthropic.StopReasonToolUse},
		{openai.ChatCompletionChoicesFinishReasonContentFilter, anthropic.StopReasonRefusal},
		{"function_call", anthropic.StopReasonEndTurn},
		{"", anthropic.StopReasonEndTurn},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			assert.Equal(t, tt.expected, openAIFinishReasonToAnthropic(tt.reason))
		})
	}
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_TextStreaming(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	// OpenAI SSE input: text streaming with usage chunk and [DONE].
	// Chunk 1: first delta with text → emits message_start, content_block_start, content_block_delta
	// Chunk 2: finish_reason → stores stop reason
	// Chunk 3: usage-only → emits content_block_stop, message_delta, message_stop
	// [DONE]: skipped
	input := "data: {\"id\":\"chatcmpl-abc\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"}}],\"model\":\"gpt-4o\"}\n\n" +
		"data: {\"id\":\"chatcmpl-abc\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-abc\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-abc\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)
	// 7 events: message_start, content_block_start, content_block_delta x2, content_block_stop, message_delta, message_stop
	require.Len(t, events, 7)

	assert.Equal(t, "message_start", events[0].eventType)
	require.JSONEq(t, `{
		"type":"message_start",
		"message":{
			"id":"chatcmpl-abc",
			"type":"message",
			"role":"assistant",
			"content":[],
			"model":"gpt-4o",
			"stop_reason":null,
			"stop_sequence":null,
			"usage":{"input_tokens":0,"output_tokens":0}
		}
	}`, events[0].data)

	assert.Equal(t, "content_block_start", events[1].eventType)
	require.JSONEq(t, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, events[1].data)

	assert.Equal(t, "content_block_delta", events[2].eventType)
	require.JSONEq(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`, events[2].data)

	assert.Equal(t, "content_block_delta", events[3].eventType)
	require.JSONEq(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`, events[3].data)

	assert.Equal(t, "content_block_stop", events[4].eventType)
	require.JSONEq(t, `{"type":"content_block_stop","index":0}`, events[4].data)

	assert.Equal(t, "message_delta", events[5].eventType)
	require.JSONEq(t, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":5}}`, events[5].data)

	assert.Equal(t, "message_stop", events[6].eventType)
	require.JSONEq(t, `{"type":"message_stop"}`, events[6].data)
}

// TestOpenAIStreamToAnthropicState_ProcessBuffer_CachedTokens is a regression test guarding
// against double-counting OpenAI's cached_tokens/cache_creation_input_tokens (a breakdown
// within prompt_tokens) as if they were additive, Anthropic-native cache usage fields.
func TestOpenAIStreamToAnthropicState_ProcessBuffer_CachedTokens(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	const (
		promptTokens        = 190_000
		cachedTokens        = 185_000
		cacheCreationTokens = 3_000
		completionTokens    = 800
	)

	input := fmt.Sprintf(
		"data: {\"id\":\"chatcmpl-cache\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}],\"model\":\"gpt-4o\"}\n\n"+
			"data: {\"id\":\"chatcmpl-cache\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"id\":\"chatcmpl-cache\",\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":%d,\"cache_creation_input_tokens\":%d}}}\n\n"+
			"data: [DONE]\n\n",
		promptTokens, completionTokens, cachedTokens, cacheCreationTokens,
	)

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	msg := accumulateAnthropicMessage(t, out)

	totalReportedInputTokens := msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens
	assert.Equal(t, int64(promptTokens), totalReportedInputTokens,
		"total input tokens must equal the real prompt_tokens, not be inflated by double-counting the cached portion")
	assert.Equal(t, int64(completionTokens), msg.Usage.OutputTokens)

	total, ok := state.tokenUsage.InputTokens()
	require.True(t, ok)
	assert.LessOrEqual(t, total, uint32(promptTokens),
		"internal TokenUsage must not inflate the input token count beyond the real prompt size")
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_ToolCallStreaming(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	// Chunk 1: new tool call start → emits message_start, content_block_start (tool_use)
	// Chunk 2: tool argument delta → emits content_block_delta (input_json_delta)
	// Chunk 3: finish_reason=tool_calls → stores stop reason
	// Chunk 4: usage → emits content_block_stop, message_delta (stop_reason=tool_use), message_stop
	input := "data: {\"id\":\"chatcmpl-def\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-xyz\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"},\"type\":\"function\"}]}}],\"model\":\"gpt-4o\"}\n\n" +
		"data: {\"id\":\"chatcmpl-def\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":null,\"function\":{\"name\":\"\",\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}}]}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-def\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-def\",\"choices\":[],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":10}}\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)
	// 6 events: message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop
	require.Len(t, events, 6)

	assert.Equal(t, "message_start", events[0].eventType)

	assert.Equal(t, "content_block_start", events[1].eventType)
	require.JSONEq(t, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-xyz","name":"get_weather","input":{}}}`, events[1].data)

	assert.Equal(t, "content_block_delta", events[2].eventType)
	require.JSONEq(t, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"NYC\"}"}}`, events[2].data)

	assert.Equal(t, "content_block_stop", events[3].eventType)
	require.JSONEq(t, `{"type":"content_block_stop","index":0}`, events[3].data)

	assert.Equal(t, "message_delta", events[4].eventType)
	require.JSONEq(t, `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":15,"output_tokens":10}}`, events[4].data)

	assert.Equal(t, "message_stop", events[5].eventType)
	require.JSONEq(t, `{"type":"message_stop"}`, events[5].data)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_EndOfStreamClosing(t *testing.T) {
	// Verify endOfStream triggers closing events when no usage chunk is present.
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "test-model",
	}

	input := "data: {\"id\":\"chatcmpl-eos\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)
	require.GreaterOrEqual(t, len(events), 4)

	// Last event must be message_stop.
	assert.Equal(t, "message_stop", events[len(events)-1].eventType)

	// Find message_delta and verify stop_reason defaults to end_turn.
	var msgDeltaData string
	for _, e := range events {
		if e.eventType == "message_delta" {
			msgDeltaData = e.data
			break
		}
	}
	require.NotEmpty(t, msgDeltaData)
	require.JSONEq(t, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`, msgDeltaData)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_EmptyInput(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "test-model",
	}

	var out []byte
	err := state.processBuffer(&out, false)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_SkipsDoneMarker(t *testing.T) {
	// Ensure [DONE] marker does not cause errors or spurious events.
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "test-model",
	}

	input := "data: [DONE]\n\n"
	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, false)
	require.NoError(t, err)
	// No events should be emitted for just [DONE].
	assert.Empty(t, out)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_MalformedChunkSkipped(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "test-model",
	}

	// A malformed JSON chunk should be silently skipped, no error.
	input := "data: {not valid json}\n\n"
	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, false)
	require.NoError(t, err)
}

func TestOpenAIStreamToAnthropicState_handleToolCallDelta_OpenBlock(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		hasOpenBlock: true,
		blockIndex:   0,
	}
	toolID := "test_id"
	toolCall := &openai.ChatCompletionChunkChoiceDeltaToolCall{
		Index: 5,
		ID:    &toolID,
		Function: openai.ChatCompletionMessageToolCallFunctionParam{
			Name:      "test",
			Arguments: "test_args",
		},
	}

	var out []byte
	err := state.handleToolCallDelta(toolCall, &out)
	require.NoError(t, err)
	require.Equal(t, 1, state.blockIndex)
}

func TestOpenAIStreamToAnthropicState_handleChunk_ZeroLen(t *testing.T) {
	state := &openAIStreamToAnthropicState{}

	chunk := &openai.ChatCompletionResponseChunk{
		Choices: []openai.ChatCompletionResponseChunkChoice{},
		Usage:   nil,
	}
	var out []byte
	err := state.handleChunk(chunk, &out)
	require.NoError(t, err)
}

func TestAppendAnthropicAssistantMessage_ThinkingPlusText(t *testing.T) {
	// thinking + text → structured content array with both blocks
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleAssistant,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "I should write a simple example.", Signature: "sig_abc123"}},
				{Text: &anthropic.TextBlockParam{Type: "text", Text: "Sure! Here is the code."}},
			},
		},
	}
	msgs := appendAnthropicAssistantMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfAssistant)

	assistantMsg := msgs[0].OfAssistant
	// Content should be a structured array, not a plain string.
	contentArray, ok := assistantMsg.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent)
	require.True(t, ok, "expected structured content array, got %T", assistantMsg.Content.Value)
	require.Len(t, contentArray, 2)

	// First block: thinking
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeThinking, contentArray[0].Type)
	require.NotNil(t, contentArray[0].Text)
	assert.Equal(t, "I should write a simple example.", *contentArray[0].Text)
	require.NotNil(t, contentArray[0].Signature)
	assert.Equal(t, "sig_abc123", *contentArray[0].Signature)

	// Second block: text
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeText, contentArray[1].Type)
	require.NotNil(t, contentArray[1].Text)
	assert.Equal(t, "Sure! Here is the code.", *contentArray[1].Text)
}

func TestAppendAnthropicAssistantMessage_ThinkingOnly(t *testing.T) {
	// thinking-only block → content array with just thinking, no text
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleAssistant,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "Just thinking...", Signature: "sig_xyz"}},
			},
		},
	}
	msgs := appendAnthropicAssistantMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfAssistant)

	assistantMsg := msgs[0].OfAssistant
	contentArray, ok := assistantMsg.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent)
	require.True(t, ok, "expected structured content array, got %T", assistantMsg.Content.Value)
	require.Len(t, contentArray, 1)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeThinking, contentArray[0].Type)
	assert.Equal(t, "Just thinking...", *contentArray[0].Text)
}

func TestAppendAnthropicAssistantMessage_ThinkingPlusToolUse(t *testing.T) {
	// thinking + tool_use → content array with thinking + tool_calls
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleAssistant,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "I need to call the calculator.", Signature: "sig_tool"}},
				{ToolUse: &anthropic.ToolUseBlockParam{Type: "tool_use", ID: "call_001", Name: "calculator", Input: map[string]any{"expression": "2+2"}}},
			},
		},
	}
	msgs := appendAnthropicAssistantMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfAssistant)

	assistantMsg := msgs[0].OfAssistant
	// Should have thinking in content array
	contentArray, ok := assistantMsg.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent)
	require.True(t, ok, "expected structured content array, got %T", assistantMsg.Content.Value)
	require.Len(t, contentArray, 1)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeThinking, contentArray[0].Type)

	// Should have tool calls
	require.Len(t, assistantMsg.ToolCalls, 1)
	assert.Equal(t, "calculator", assistantMsg.ToolCalls[0].Function.Name)
}

func TestAppendAnthropicAssistantMessage_MultipleThinkingBlocks(t *testing.T) {
	// multiple thinking blocks → all preserved in order
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleAssistant,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "First thought.", Signature: "s1"}},
				{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "Second thought.", Signature: "s2"}},
				{Text: &anthropic.TextBlockParam{Type: "text", Text: "Done."}},
			},
		},
	}
	msgs := appendAnthropicAssistantMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfAssistant)

	contentArray, ok := msgs[0].OfAssistant.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent)
	require.True(t, ok)
	require.Len(t, contentArray, 3)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeThinking, contentArray[0].Type)
	assert.Equal(t, "First thought.", *contentArray[0].Text)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeThinking, contentArray[1].Type)
	assert.Equal(t, "Second thought.", *contentArray[1].Text)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeText, contentArray[2].Type)
	assert.Equal(t, "Done.", *contentArray[2].Text)
}

func TestAppendAnthropicAssistantMessage_RedactedThinking(t *testing.T) {
	// redacted_thinking block → content with type "redacted_thinking"
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleAssistant,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "Thinking...", Signature: "sig_think"}},
				{RedactedThinking: &anthropic.RedactedThinkingBlockParam{Type: "redacted_thinking", Data: "BASE64_OPAQUE_DATA"}},
				{Text: &anthropic.TextBlockParam{Type: "text", Text: "Hi!"}},
			},
		},
	}
	msgs := appendAnthropicAssistantMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfAssistant)

	contentArray, ok := msgs[0].OfAssistant.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent)
	require.True(t, ok)
	require.Len(t, contentArray, 3)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeThinking, contentArray[0].Type)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking, contentArray[1].Type)
	require.NotNil(t, contentArray[1].RedactedContent)
	assert.Equal(t, openai.ChatCompletionAssistantMessageParamContentTypeText, contentArray[2].Type)
}

func TestAppendAnthropicAssistantMessage_NoThinkingUnchanged(t *testing.T) {
	// text-only messages still use plain string content (backward compatibility)
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleAssistant,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Text: &anthropic.TextBlockParam{Type: "text", Text: "Hello!"}},
			},
		},
	}
	msgs := appendAnthropicAssistantMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfAssistant)

	// Should still be a plain string, not a structured array
	strVal, ok := msgs[0].OfAssistant.Content.Value.(string)
	require.True(t, ok, "expected string content for text-only message, got %T", msgs[0].OfAssistant.Content.Value)
	assert.Equal(t, "Hello!", strVal)
}

func TestBuildOpenAIChatCompletionRequest_ThinkingConfig(t *testing.T) {
	t.Run("thinking enabled with budget", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 8192,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Think hard"}}},
			Thinking:  &anthropic.Thinking{Enabled: &anthropic.ThinkingEnabled{Type: "enabled", BudgetTokens: 4096}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.NotNil(t, req.Thinking)
		require.NotNil(t, req.Thinking.OfEnabled)
		assert.Equal(t, "enabled", req.Thinking.OfEnabled.Type)
		assert.Equal(t, int64(4096), req.Thinking.OfEnabled.BudgetTokens)
	})

	t.Run("thinking disabled", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			Thinking:  &anthropic.Thinking{Disabled: &anthropic.ThinkingDisabled{Type: "disabled"}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.NotNil(t, req.Thinking)
		require.NotNil(t, req.Thinking.OfDisabled)
		assert.Equal(t, "disabled", req.Thinking.OfDisabled.Type)
	})

	t.Run("thinking adaptive", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			Thinking:  &anthropic.Thinking{Adaptive: &anthropic.ThinkingAdaptive{Type: "adaptive"}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		require.NotNil(t, req.Thinking)
		require.NotNil(t, req.Thinking.OfAdaptive)
		assert.Equal(t, "adaptive", req.Thinking.OfAdaptive.Type)
	})

	t.Run("no thinking config", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		req := buildOpenAIChatCompletionRequest(body, "")
		assert.Nil(t, req.Thinking)
	})
}

func TestAppendAnthropicUserMessage_Base64Image(t *testing.T) {
	// base64 image → image_url with data URI
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleUser,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Text: &anthropic.TextBlockParam{Type: "text", Text: "Describe this image"}},
				{Image: &anthropic.ImageBlockParam{
					Type: "image",
					Source: anthropic.ImageSource{
						Base64: &anthropic.Base64ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "iVBORw0KGgo="},
					},
				}},
			},
		},
	}
	msgs := appendAnthropicUserMessage(nil, msg)
	// Should have 1 user message (no tool results)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfUser)

	// Content should be a structured array with text + image_url parts
	parts, ok := msgs[0].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
	require.True(t, ok, "expected structured content parts, got %T", msgs[0].OfUser.Content.Value)
	require.Len(t, parts, 2)

	// First part: text
	require.NotNil(t, parts[0].OfText)
	assert.Equal(t, "Describe this image", parts[0].OfText.Text)

	// Second part: image_url
	require.NotNil(t, parts[1].OfImageURL)
	assert.Equal(t, "data:image/jpeg;base64,iVBORw0KGgo=", parts[1].OfImageURL.ImageURL.URL)
}

func TestAppendAnthropicUserMessage_URLImage(t *testing.T) {
	// URL image → image_url with URL passthrough
	msg := anthropic.MessageParam{
		Role: anthropic.MessageRoleUser,
		Content: anthropic.MessageContent{
			Array: []anthropic.ContentBlockParam{
				{Text: &anthropic.TextBlockParam{Type: "text", Text: "What is this?"}},
				{Image: &anthropic.ImageBlockParam{
					Type: "image",
					Source: anthropic.ImageSource{
						URL: &anthropic.URLImageSource{Type: "url", URL: "https://example.com/cat.png"},
					},
				}},
			},
		},
	}
	msgs := appendAnthropicUserMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfUser)

	parts, ok := msgs[0].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
	require.True(t, ok)
	require.Len(t, parts, 2)
	require.NotNil(t, parts[1].OfImageURL)
	assert.Equal(t, "https://example.com/cat.png", parts[1].OfImageURL.ImageURL.URL)
}

func TestAppendAnthropicUserMessage_TextOnly(t *testing.T) {
	// text-only user message still uses plain string content (backward compat)
	msg := anthropic.MessageParam{
		Role:    anthropic.MessageRoleUser,
		Content: anthropic.MessageContent{Text: "Hello"},
	}
	msgs := appendAnthropicUserMessage(nil, msg)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].OfUser)
	assert.Equal(t, "Hello", msgs[0].OfUser.Content.Value)
}

func TestOpenAIResponseToAnthropic_ThinkingBlocks(t *testing.T) {
	content := "Here is the answer."
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-think",
		Model: "gpt-4o",
		Choices: []openai.ChatCompletionResponseChoice{
			{
				FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				Message: openai.ChatCompletionResponseChoiceMessage{
					Content: &content,
					Role:    "assistant",
					ThinkingBlocks: []openai.ThinkingBlock{
						{Type: "thinking", Thinking: "Let me reason about this...", Signature: "sig_123"},
					},
				},
			},
		},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 20},
	}
	result := openAIResponseToAnthropic(resp, "gpt-4o")

	// Should have 2 content blocks: thinking first, then text
	require.Len(t, result.Content, 2)
	require.NotNil(t, result.Content[0].Thinking)
	assert.Equal(t, "thinking", result.Content[0].Thinking.Type)
	assert.Equal(t, "Let me reason about this...", result.Content[0].Thinking.Thinking)
	assert.Equal(t, "sig_123", result.Content[0].Thinking.Signature)

	require.NotNil(t, result.Content[1].Text)
	assert.Equal(t, "Here is the answer.", result.Content[1].Text.Text)
}

func TestOpenAIResponseToAnthropic_RedactedThinkingBlocks(t *testing.T) {
	content := "Answer."
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionResponseChoice{
			{
				FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				Message: openai.ChatCompletionResponseChoiceMessage{
					Content: &content,
					Role:    "assistant",
					ThinkingBlocks: []openai.ThinkingBlock{
						{Type: "redacted_thinking", Data: "REDACTED_DATA"},
					},
				},
			},
		},
	}
	result := openAIResponseToAnthropic(resp, "test-model")

	// Should have redacted_thinking block before text
	require.Len(t, result.Content, 2)
	require.NotNil(t, result.Content[0].RedactedThinking)
	assert.Equal(t, "redacted_thinking", result.Content[0].RedactedThinking.Type)
	assert.Equal(t, "REDACTED_DATA", result.Content[0].RedactedThinking.Data)

	require.NotNil(t, result.Content[1].Text)
	assert.Equal(t, "Answer.", result.Content[1].Text.Text)
}

func TestOpenAIResponseToAnthropic_ThinkingBeforeText(t *testing.T) {
	// Verify thinking blocks appear before text in the content array
	content := "Result text"
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionResponseChoice{
			{
				Message: openai.ChatCompletionResponseChoiceMessage{
					Content: &content,
					ThinkingBlocks: []openai.ThinkingBlock{
						{Type: "thinking", Thinking: "step 1", Signature: "s1"},
						{Type: "thinking", Thinking: "step 2", Signature: "s2"},
					},
				},
			},
		},
	}
	result := openAIResponseToAnthropic(resp, "model")

	require.Len(t, result.Content, 3)
	// First two should be thinking blocks
	require.NotNil(t, result.Content[0].Thinking)
	require.NotNil(t, result.Content[1].Thinking)
	// Last should be text
	require.NotNil(t, result.Content[2].Text)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_ThinkingStreaming(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	// Chunk 1: reasoning content delta → emits message_start, thinking content_block_start, thinking content_block_delta
	// Chunk 2: finish_reason → stores stop reason
	// Chunk 3: usage-only → emits content_block_stop, message_delta, message_stop
	input := `data: {"id":"chatcmpl-think","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":{"text":"Let me think..."}}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-think","choices":[{"index":0,"delta":{"reasoning_content":{"text":" More thinking."}},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-think","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-think","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)
	// Should have: message_start, content_block_start (thinking), content_block_delta x2 (thinking_delta),
	// content_block_stop, message_delta, message_stop
	require.GreaterOrEqual(t, len(events), 6)

	assert.Equal(t, "message_start", events[0].eventType)
	assert.Equal(t, "content_block_start", events[1].eventType)
	assert.Contains(t, events[1].data, `"thinking"`)
	assert.Equal(t, "content_block_delta", events[2].eventType)
	assert.Contains(t, events[2].data, `"thinking_delta"`)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_ThinkingThenText(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	// Chunk 1: reasoning content → thinking block
	// Chunk 2: text content → should close thinking block, open text block
	// Chunk 3: usage
	input := `data: {"id":"chatcmpl-tt","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":{"text":"thinking..."}}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-tt","choices":[{"index":0,"delta":{"content":"Hello world"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-tt","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-tt","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)

	// Find block types in content_block_start events
	var blockStartTypes []string
	for _, e := range events {
		if e.eventType == "content_block_start" {
			if assert.Contains(t, e.data, "content_block") {
				if assert.Contains(t, e.data, `"type"`) {
					// Extract the content block type
					if bytes.Contains([]byte(e.data), []byte(`"thinking"`)) {
						blockStartTypes = append(blockStartTypes, "thinking")
					} else if bytes.Contains([]byte(e.data), []byte(`"text"`)) {
						blockStartTypes = append(blockStartTypes, "text")
					}
				}
			}
		}
	}
	// Should have two content blocks: thinking first, then text
	require.Equal(t, []string{"thinking", "text"}, blockStartTypes)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_ThinkingThenToolCall(t *testing.T) {
	// Verify that a thinking block is properly closed when a tool call arrives,
	// and that hasThinkingBlock is reset so subsequent text doesn't corrupt state.
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	input := `data: {"id":"chatcmpl-ttc","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":{"text":"thinking..."}}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-ttc","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"get_weather","arguments":""},"type":"function"}]}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-ttc","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":null,"function":{"name":"","arguments":"{\"city\":\"NYC\"}"}}]}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-ttc","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-ttc","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)

	// Should have: message_start, thinking content_block_start, thinking_delta,
	// thinking content_block_stop, tool content_block_start, tool input_json_delta,
	// tool content_block_stop, message_delta, message_stop
	var blockStartTypes []string
	var blockStopCount int
	for _, e := range events {
		if e.eventType == "content_block_start" {
			if bytes.Contains([]byte(e.data), []byte(`"thinking"`)) {
				blockStartTypes = append(blockStartTypes, "thinking")
			} else if bytes.Contains([]byte(e.data), []byte(`"tool_use"`)) {
				blockStartTypes = append(blockStartTypes, "tool_use")
			}
		}
		if e.eventType == "content_block_stop" {
			blockStopCount++
		}
	}
	assert.Equal(t, []string{"thinking", "tool_use"}, blockStartTypes)
	assert.Equal(t, 2, blockStopCount, "should have exactly 2 content_block_stop events (thinking + tool)")
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_TextThenReasoning(t *testing.T) {
	// Edge case: text arrives before reasoning (unlikely but should not corrupt state).
	// The text block must be properly closed before the thinking block opens.
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	input := `data: {"id":"chatcmpl-tr","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-tr","choices":[{"index":0,"delta":{"reasoning_content":{"text":"thinking after text"}}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-tr","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-tr","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)

	// Verify we get two separate content blocks: text first, then thinking.
	var blockStartTypes []string
	var blockStopCount int
	for _, e := range events {
		if e.eventType == "content_block_start" {
			if bytes.Contains([]byte(e.data), []byte(`"text"`)) {
				blockStartTypes = append(blockStartTypes, "text")
			} else if bytes.Contains([]byte(e.data), []byte(`"thinking"`)) {
				blockStartTypes = append(blockStartTypes, "thinking")
			}
		}
		if e.eventType == "content_block_stop" {
			blockStopCount++
		}
	}
	assert.Equal(t, []string{"text", "thinking"}, blockStartTypes)
	assert.Equal(t, 2, blockStopCount, "both text and thinking blocks should be properly closed")
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_SignatureOnlyChunk(t *testing.T) {
	// Verify that a signature-only chunk (no text) properly opens a thinking block first.
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	input := `data: {"id":"chatcmpl-so","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":{"signature":"sig_only"}}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-so","choices":[{"index":0,"delta":{"content":"Answer"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-so","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-so","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)

	// Should have a thinking content_block_start before the signature_delta
	var blockStartTypes []string
	for _, e := range events {
		if e.eventType == "content_block_start" {
			if bytes.Contains([]byte(e.data), []byte(`"thinking"`)) {
				blockStartTypes = append(blockStartTypes, "thinking")
			} else if bytes.Contains([]byte(e.data), []byte(`"text"`)) {
				blockStartTypes = append(blockStartTypes, "text")
			}
		}
	}
	assert.Equal(t, []string{"thinking", "text"}, blockStartTypes)
}

func TestOpenAIResponseToAnthropic_ReasoningContentString(t *testing.T) {
	// Some models (Qwen, DeepSeek) return reasoning_content as a plain string.
	content := "The answer is 4."
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionResponseChoice{
			{
				FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				Message: openai.ChatCompletionResponseChoiceMessage{
					Content: &content,
					Role:    "assistant",
					ReasoningContent: &openai.ReasoningContentUnion{
						Value: "Let me think step by step...",
					},
				},
			},
		},
	}
	result := openAIResponseToAnthropic(resp, "test-model")

	require.Len(t, result.Content, 2)
	require.NotNil(t, result.Content[0].Thinking)
	assert.Equal(t, "Let me think step by step...", result.Content[0].Thinking.Thinking)
	require.NotNil(t, result.Content[1].Text)
	assert.Equal(t, "The answer is 4.", result.Content[1].Text.Text)
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_SignatureDelta(t *testing.T) {
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	// Chunk 1: reasoning text → starts thinking block
	// Chunk 2: reasoning signature → emits signature_delta
	// Chunk 3: text content → closes thinking, opens text
	// Chunk 4: usage
	input := `data: {"id":"chatcmpl-sig","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":{"text":"deep thought"}}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-sig","choices":[{"index":0,"delta":{"reasoning_content":{"signature":"sig_abc"}}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-sig","choices":[{"index":0,"delta":{"content":"Answer"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-sig","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-sig","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)

	// Should contain a signature_delta event
	var hasSignatureDelta bool
	for _, e := range events {
		if e.eventType == "content_block_delta" && bytes.Contains([]byte(e.data), []byte(`"signature_delta"`)) {
			hasSignatureDelta = true
			assert.Contains(t, e.data, `"sig_abc"`)
		}
	}
	assert.True(t, hasSignatureDelta, "expected a signature_delta event")
}

func TestOpenAIStreamToAnthropicState_ProcessBuffer_ThinkingBlocks(t *testing.T) {
	// Signatures now ride on delta.thinking_blocks (reasoning_content is a plain
	// string), so a chunk carrying thinking_blocks must round-trip both the thinking
	// text and its signature to the Anthropic protocol.
	state := &openAIStreamToAnthropicState{
		activeTools:  make(map[int64]*streamToolCall),
		requestModel: "claude-3",
	}

	input := `data: {"id":"chatcmpl-tb","choices":[{"index":0,"delta":{"role":"assistant","thinking_blocks":[{"type":"thinking","thinking":"deep thought","signature":"sig_tb"}]}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-tb","choices":[{"index":0,"delta":{"content":"Answer"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-tb","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-tb","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	state.buffer.WriteString(input)

	var out []byte
	err := state.processBuffer(&out, true)
	require.NoError(t, err)

	events := parseSSEEventsFromBytes(out)

	var hasThinkingBlock, hasThinkingDelta, hasSignatureDelta bool
	for _, e := range events {
		switch {
		case e.eventType == "content_block_start" && bytes.Contains([]byte(e.data), []byte(`"thinking"`)):
			hasThinkingBlock = true
		case e.eventType == "content_block_delta" && bytes.Contains([]byte(e.data), []byte(`"thinking_delta"`)):
			hasThinkingDelta = true
			assert.Contains(t, e.data, `"deep thought"`)
		case e.eventType == "content_block_delta" && bytes.Contains([]byte(e.data), []byte(`"signature_delta"`)):
			hasSignatureDelta = true
			assert.Contains(t, e.data, `"sig_tb"`)
		}
	}
	assert.True(t, hasThinkingBlock, "expected a thinking content_block_start")
	assert.True(t, hasThinkingDelta, "expected a thinking_delta event")
	assert.True(t, hasSignatureDelta, "expected a signature_delta event")
}
