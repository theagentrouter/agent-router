// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func anthropicUserMessage(text string) anthropicschema.MessageParam {
	return anthropicschema.MessageParam{
		Role:    "user",
		Content: anthropicschema.MessageContent{Text: text},
	}
}

func TestAnthropicRequestAttrs(t *testing.T) {
	tests := []struct {
		name     string
		req      *anthropicschema.MessagesRequest
		expected []attribute.KeyValue
	}{
		{name: "nothing set", req: &anthropicschema.MessagesRequest{}, expected: nil},
		{
			name: "parameters",
			req: &anthropicschema.MessagesRequest{
				Temperature:   ptr(0.4),
				TopP:          ptr(0.8),
				TopK:          ptr(20),
				MaxTokens:     1024,
				StopSequences: []string{"STOP"},
			},
			expected: []attribute.KeyValue{
				attribute.Float64(RequestTemperature, 0.4),
				attribute.Float64(RequestTopP, 0.8),
				attribute.Int(RequestTopK, 20),
				attribute.Int64(RequestMaxTokens, 1024),
				attribute.StringSlice(RequestStopSequences, []string{"STOP"}),
			},
		},
		{
			// MaxTokens is required by this API and non-pointer, so zero means
			// unset rather than a deliberate limit of zero.
			name:     "zero max tokens is omitted",
			req:      &anthropicschema.MessagesRequest{MaxTokens: 0},
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, anthropicRequestAttrs(tc.req))
		})
	}
}

func TestAnthropicResponseAttrs(t *testing.T) {
	stop := anthropicschema.StopReason("end_turn")

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		span.SetAttributes(anthropicResponseAttrs(&anthropicschema.MessagesResponse{
			ID:         "msg_123",
			Model:      "claude-sonnet-5",
			StopReason: &stop,
			Usage: &anthropicschema.Usage{
				InputTokens:              100,
				OutputTokens:             50,
				CacheReadInputTokens:     80,
				CacheCreationInputTokens: 20,
			},
		})...)
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(ResponseID, "msg_123"),
		attribute.String(ResponseModel, "claude-sonnet-5"),
		attribute.Int(UsageInputTokens, 100),
		attribute.Int(UsageOutputTokens, 50),
		attribute.Int(UsageCacheReadInputTokens, 80),
		attribute.Int(UsageCacheCreationInputTokens, 20),
		attribute.StringSlice(ResponseFinishReasons, []string{"end_turn"}),
	}, span.Attributes)
}

func TestAnthropicInputMessages(t *testing.T) {
	tests := []struct {
		name     string
		req      *anthropicschema.MessagesRequest
		expected string
	}{
		{name: "no messages", req: &anthropicschema.MessagesRequest{}, expected: ""},
		{
			name: "string content",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{anthropicUserMessage("hello")},
			},
			expected: `[{"role":"user","parts":[{"type":"text","content":"hello"}]}]`,
		},
		{
			name: "tool use block",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{{
					Role: "assistant",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{{
							ToolUse: &anthropicschema.ToolUseBlockParam{
								ID:    "toolu_1",
								Name:  "get_weather",
								Input: map[string]any{"city": "Berlin"},
							},
						}},
					},
				}},
			},
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"toolu_1",` +
				`"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}]}]`,
		},
		{
			// A tool result is recorded by the call it answers. The payload is
			// the tool's output, which the conventions place on the tool span.
			name: "tool result block",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{{
					Role: "user",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{{
							ToolResult: &anthropicschema.ToolResultBlockParam{ToolUseID: "toolu_1"},
						}},
					},
				}},
			},
			expected: `[{"role":"user","parts":[{"type":"tool_call_response","id":"toolu_1"}]}]`,
		},
		{
			// Images are recorded by type only: the bytes are large and the
			// conventions define no attribute for them.
			name: "image block records type without content",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{{
					Role: "user",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{{
							Image: &anthropicschema.ImageBlockParam{Type: "image"},
						}},
					},
				}},
			},
			expected: `[{"role":"user","parts":[{"type":"image"}]}]`,
		},
		{
			name: "text block in array form",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{{
					Role: "user",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{{
							Text: &anthropicschema.TextBlockParam{Text: "hello"},
						}},
					},
				}},
			},
			expected: `[{"role":"user","parts":[{"type":"text","content":"hello"}]}]`,
		},
		{
			// An empty tool input yields no arguments rather than "{}", so a
			// consumer can tell "no arguments recorded" from "empty object".
			name: "tool use without input omits arguments",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{{
					Role: "assistant",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{{
							ToolUse: &anthropicschema.ToolUseBlockParam{ID: "toolu_1", Name: "ping"},
						}},
					},
				}},
			},
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"toolu_1","name":"ping"}]}]`,
		},
		{
			// Reasoning content is recorded by type only: it is model-internal
			// and often large.
			name: "thinking block records type without content",
			req: &anthropicschema.MessagesRequest{
				Messages: []anthropicschema.MessageParam{{
					Role: "assistant",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{{
							Thinking: &anthropicschema.ThinkingBlockParam{Thinking: "secret reasoning"},
						}},
					},
				}},
			},
			expected: `[{"role":"assistant","parts":[{"type":"reasoning"}]}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(InputMessages, anthropicInputMessages(tc.req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

// TestAnthropicSystemInstructions pins that the system prompt maps to its own
// attribute rather than being folded into the conversation, because this API
// models it as a separate field.
func TestAnthropicSystemInstructions(t *testing.T) {
	tests := []struct {
		name     string
		req      *anthropicschema.MessagesRequest
		expected string
	}{
		{name: "absent", req: &anthropicschema.MessagesRequest{}, expected: ""},
		{
			name:     "string form",
			req:      &anthropicschema.MessagesRequest{System: &anthropicschema.SystemPrompt{Text: "be brief"}},
			expected: `[{"type":"text","content":"be brief"}]`,
		},
		{
			name: "block form",
			req: &anthropicschema.MessagesRequest{System: &anthropicschema.SystemPrompt{
				Texts: []anthropicschema.TextBlockParam{{Text: "be brief"}, {Text: "be kind"}},
			}},
			expected: `[{"type":"text","content":"be brief"},{"type":"text","content":"be kind"}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := partsAttr(SystemInstructions, anthropicSystemInstructions(tc.req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestAnthropicOutputMessages(t *testing.T) {
	stop := anthropicschema.StopReason("end_turn")

	msgs := anthropicOutputMessages(&anthropicschema.MessagesResponse{
		Role:       "assistant",
		StopReason: &stop,
		Content: []anthropicschema.MessagesContentBlock{
			{Text: &anthropicschema.TextBlock{Text: "hi there"}},
		},
	})

	attrs := messagesAttr(OutputMessages, msgs)
	require.Len(t, attrs, 1)
	require.JSONEq(t,
		`[{"role":"assistant","parts":[{"type":"text","content":"hi there"}],"finish_reason":"end_turn"}]`,
		attrs[0].Value.AsString())
}

// TestAnthropicOutputMessages_blocks pins the response block arms, which are a
// different union from the request's.
func TestAnthropicOutputMessages_blocks(t *testing.T) {
	tests := []struct {
		name     string
		content  []anthropicschema.MessagesContentBlock
		expected string
	}{
		{
			name: "tool use block",
			content: []anthropicschema.MessagesContentBlock{{
				Tool: &anthropicschema.ToolUseBlock{
					ID:    "toolu_1",
					Name:  "get_weather",
					Input: map[string]any{"city": "Berlin"},
				},
			}},
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"toolu_1",` +
				`"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}]}]`,
		},
		{
			name:     "thinking block records type without content",
			content:  []anthropicschema.MessagesContentBlock{{Thinking: &anthropicschema.ThinkingBlock{Thinking: "secret reasoning"}}},
			expected: `[{"role":"assistant","parts":[{"type":"reasoning"}]}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(OutputMessages, anthropicOutputMessages(&anthropicschema.MessagesResponse{
				Role:    "assistant",
				Content: tc.content,
			}))
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestAnthropicOutputMessages_empty(t *testing.T) {
	require.Empty(t, anthropicOutputMessages(&anthropicschema.MessagesResponse{Role: "assistant"}))
}

// TestMessageRecorder_contentCapture pins the redaction boundary for Anthropic,
// covering both the conversation and the separately modelled system prompt.
func TestMessageRecorder_contentCapture(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"
	req := &anthropicschema.MessagesRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 100,
		System:    &anthropicschema.SystemPrompt{Text: secret},
		Messages:  []anthropicschema.MessageParam{anthropicUserMessage(secret)},
	}

	for _, capture := range []bool{false, true} {
		t.Run(t.Name(), func(t *testing.T) {
			r := NewMessageRecorder(&Config{CaptureMessageContent: capture})

			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, req, nil)
				return false
			})

			require.Equal(t, capture, hasAttr(span.Attributes, InputMessages))
			require.Equal(t, capture, hasAttr(span.Attributes, SystemInstructions))

			if !capture {
				for _, attr := range span.Attributes {
					require.NotContains(t, attr.Value.AsString(), secret, "attribute %s", attr.Key)
				}
			}
		})
	}
}

// TestMessageRecorder_operationIsChat pins that Anthropic messages report the
// registry value chat, not the "messages" value metrics uses.
func TestMessageRecorder_operationIsChat(t *testing.T) {
	r := NewMessageRecorder(NewConfig())
	name, _ := r.StartParams(&anthropicschema.MessagesRequest{Model: "claude-sonnet-5"}, nil)
	require.Equal(t, "chat claude-sonnet-5", name)
}
