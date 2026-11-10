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

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func ptr[T any](v T) *T { return &v }

func userMessage(content string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Role:    openai.ChatMessageRoleUser,
			Content: openai.StringOrUserRoleContentUnion{Value: content},
		},
	}
}

func TestChatInputMessages(t *testing.T) {
	tests := []struct {
		name     string
		req      *openai.ChatCompletionRequest
		expected string
	}{
		{
			name:     "no messages",
			req:      &openai.ChatCompletionRequest{},
			expected: "",
		},
		{
			name:     "single user message",
			req:      &openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessageParamUnion{userMessage("hello")}},
			expected: `[{"role":"user","parts":[{"type":"text","content":"hello"}]}]`,
		},
		{
			name: "system then user preserves order",
			req: &openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessageParamUnion{
				{OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{Value: "be brief"},
				}},
				userMessage("hello"),
			}},
			expected: `[{"role":"system","parts":[{"type":"text","content":"be brief"}]},` +
				`{"role":"user","parts":[{"type":"text","content":"hello"}]}]`,
		},
		{
			name: "tool response carries the call id",
			req: &openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessageParamUnion{
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_1",
					Content:    openai.ContentUnion{Value: "72F"},
				}},
			}},
			expected: `[{"role":"tool","parts":[{"type":"tool_call_response","content":"72F","id":"call_1"}]}]`,
		},
		{
			// The call id still identifies which call this answers, so the part
			// is kept even when the tool returned nothing.
			name: "tool response without content keeps the call id",
			req: &openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessageParamUnion{
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_1",
					Content:    openai.ContentUnion{Value: ""},
				}},
			}},
			expected: `[{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1"}]}]`,
		},
		{
			name: "empty content yields empty parts",
			req: &openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessageParamUnion{
				userMessage(""),
			}},
			expected: `[{"role":"user","parts":null}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(InputMessages, chatInputMessages(tc.req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestChatOutputMessages(t *testing.T) {
	tests := []struct {
		name     string
		resp     *openai.ChatCompletionResponse
		expected string
	}{
		{
			name:     "no choices",
			resp:     &openai.ChatCompletionResponse{},
			expected: "",
		},
		{
			name: "text with finish reason",
			resp: &openai.ChatCompletionResponse{Choices: []openai.ChatCompletionResponseChoice{{
				FinishReason: "stop",
				Message: openai.ChatCompletionResponseChoiceMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: ptr("hi there"),
				},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"hi there"}],"finish_reason":"stop"}]`,
		},
		{
			name: "tool call",
			resp: &openai.ChatCompletionResponse{Choices: []openai.ChatCompletionResponseChoice{{
				FinishReason: "tool_calls",
				Message: openai.ChatCompletionResponseChoiceMessage{
					Role: openai.ChatMessageRoleAssistant,
					ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
						ID: ptr("call_1"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"city":"Berlin"}`,
						},
					}},
				},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"get_weather",` +
				`"arguments":"{\"city\":\"Berlin\"}"}],"finish_reason":"tool_calls"}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(OutputMessages, chatOutputMessages(tc.resp))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

// TestChatInputMessages_multiPartContent covers the content shapes that arrive
// as an ordered list of typed parts rather than a plain string. Non-text
// modalities must be recorded by type with no content, so the shape of the
// conversation stays visible without embedding payloads in a span attribute.
func TestChatInputMessages_multiPartContent(t *testing.T) {
	tests := []struct {
		name     string
		msg      openai.ChatCompletionMessageParamUnion
		expected string
	}{
		{
			name: "user text parts preserve order",
			msg: openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{OfText: &openai.ChatCompletionContentPartTextParam{Text: "first"}},
							{OfText: &openai.ChatCompletionContentPartTextParam{Text: "second"}},
						},
					},
				},
			},
			expected: `[{"role":"user","parts":[{"type":"text","content":"first"},` +
				`{"type":"text","content":"second"}]}]`,
		},
		{
			name: "image is recorded by type with no content",
			msg: openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{OfText: &openai.ChatCompletionContentPartTextParam{Text: "what is this?"}},
							{OfImageURL: &openai.ChatCompletionContentPartImageParam{
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "data:image/png;base64,SECRETPAYLOAD",
								},
							}},
						},
					},
				},
			},
			expected: `[{"role":"user","parts":[{"type":"text","content":"what is this?"},{"type":"image"}]}]`,
		},
		{
			name: "audio is recorded by type with no content",
			msg: openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{}},
						},
					},
				},
			},
			expected: `[{"role":"user","parts":[{"type":"audio"}]}]`,
		},
		{
			name: "unhandled user part type is dropped",
			msg: openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{OfFile: &openai.ChatCompletionContentPartFileParam{}},
						},
					},
				},
			},
			expected: `[{"role":"user","parts":[]}]`,
		},
		{
			name: "user content of an unexpected type yields no parts",
			msg: openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: 42},
				},
			},
			expected: `[{"role":"user","parts":null}]`,
		},
		{
			name: "system text parts preserve order",
			msg: openai.ChatCompletionMessageParamUnion{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role: openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{
						Value: []openai.ChatCompletionContentPartTextParam{
							{Text: "be brief"},
							{Text: "be kind"},
						},
					},
				},
			},
			expected: `[{"role":"system","parts":[{"type":"text","content":"be brief"},` +
				`{"type":"text","content":"be kind"}]}]`,
		},
		{
			name: "developer message maps to text parts",
			msg: openai.ChatCompletionMessageParamUnion{
				OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
					Role:    openai.ChatMessageRoleDeveloper,
					Content: openai.ContentUnion{Value: "internal guidance"},
				},
			},
			expected: `[{"role":"developer","parts":[{"type":"text","content":"internal guidance"}]}]`,
		},
		{
			name: "system content of an unexpected type yields no parts",
			msg: openai.ChatCompletionMessageParamUnion{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{Value: 42},
				},
			},
			expected: `[{"role":"system","parts":null}]`,
		},
		{
			name:     "message with no variant set is dropped",
			msg:      openai.ChatCompletionMessageParamUnion{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &openai.ChatCompletionRequest{
				Messages: []openai.ChatCompletionMessageParamUnion{tc.msg},
			}
			attrs := messagesAttr(InputMessages, chatInputMessages(req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

// TestChatInputMessages_assistantParts covers assistant messages appearing in
// the request, which is how prior turns of a conversation are replayed.
func TestChatInputMessages_assistantParts(t *testing.T) {
	tests := []struct {
		name     string
		msg      *openai.ChatCompletionAssistantMessageParam
		expected string
	}{
		{
			name: "string content",
			msg: &openai.ChatCompletionAssistantMessageParam{
				Role:    openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{Value: "earlier reply"},
			},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"earlier reply"}]}]`,
		},
		{
			name: "structured content parts",
			msg: &openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{Type: openai.ChatCompletionAssistantMessageParamContentTypeText, Text: ptr("part one")},
						{Type: openai.ChatCompletionAssistantMessageParamContentTypeText, Text: ptr("part two")},
					},
				},
			},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"part one"},` +
				`{"type":"text","content":"part two"}]}]`,
		},
		{
			name: "content part with no text is skipped",
			msg: &openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{Type: openai.ChatCompletionAssistantMessageParamContentTypeRefusal, Refusal: ptr("no")},
						{Type: openai.ChatCompletionAssistantMessageParamContentTypeText, Text: ptr("kept")},
					},
				},
			},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"kept"}]}]`,
		},
		{
			name: "tool calls follow content",
			msg: &openai.ChatCompletionAssistantMessageParam{
				Role:    openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{Value: "let me check"},
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
					ID: ptr("call_1"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "get_weather",
						Arguments: `{"city":"Berlin"}`,
					},
				}},
			},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"let me check"},` +
				`{"type":"tool_call","id":"call_1","name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}]}]`,
		},
		{
			name: "tool call with no id omits it",
			msg: &openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "get_weather",
						Arguments: `{}`,
					},
				}},
			},
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","name":"get_weather","arguments":"{}"}]}]`,
		},
		{
			name: "no content and no tool calls yields no parts",
			msg: &openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
			},
			expected: `[{"role":"assistant","parts":null}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &openai.ChatCompletionRequest{
				Messages: []openai.ChatCompletionMessageParamUnion{{OfAssistant: tc.msg}},
			}
			attrs := messagesAttr(InputMessages, chatInputMessages(req))
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

// TestChatCompletionRecorder_contentCapture is the redaction boundary: the same
// request must produce content only when the opt-in is set.
func TestChatCompletionRecorder_contentCapture(t *testing.T) {
	const secret = "SENSITIVE-PROMPT-TEXT"
	req := &openai.ChatCompletionRequest{
		Model:    "gpt-5-nano",
		Messages: []openai.ChatCompletionMessageParamUnion{userMessage(secret)},
	}
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionResponseChoice{{
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: ptr(secret),
			},
		}},
	}

	tests := []struct {
		name           string
		captureContent bool
	}{
		{name: "capture off", captureContent: false},
		{name: "capture on", captureContent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewChatCompletionRecorder(&Config{CaptureMessageContent: tc.captureContent})

			reqSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, req, nil)
				return false
			})
			respSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordResponse(span, resp)
				return false
			})

			require.Equal(t, tc.captureContent, hasAttr(reqSpan.Attributes, InputMessages))
			require.Equal(t, tc.captureContent, hasAttr(respSpan.Attributes, OutputMessages))

			if !tc.captureContent {
				for _, attr := range append(reqSpan.Attributes, respSpan.Attributes...) {
					require.NotContains(t, attr.Value.AsString(), secret, "attribute %s", attr.Key)
				}
			}
		})
	}
}

// TestChatCompletionRecorder_messageCountBoundaries exercises conversation
// lengths around the OTEL default attribute cap of 128. GenAI encodes all
// messages into one attribute, so none of these may truncate or drop.
func TestChatCompletionRecorder_messageCountBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 127, 128, 129, 500} {
		t.Run(t.Name(), func(t *testing.T) {
			msgs := make([]openai.ChatCompletionMessageParamUnion, 0, count)
			for i := 0; i < count; i++ {
				msgs = append(msgs, userMessage("m"))
			}

			r := NewChatCompletionRecorder(&Config{CaptureMessageContent: true})
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, &openai.ChatCompletionRequest{Model: "m", Messages: msgs}, nil)
				return false
			})

			// Regardless of conversation length, messages occupy exactly one
			// attribute, which is why this convention does not need the
			// attribute count limit lifted.
			require.LessOrEqual(t, len(span.Attributes), 3, "count=%d", count)
			require.Equal(t, count > 0, hasAttr(span.Attributes, InputMessages), "count=%d", count)
		})
	}
}

func hasAttr(attrs []attribute.KeyValue, key string) bool {
	for _, a := range attrs {
		if string(a.Key) == key {
			return true
		}
	}
	return false
}
