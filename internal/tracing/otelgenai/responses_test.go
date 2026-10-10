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

func TestResponsesConversationID(t *testing.T) {
	tests := []struct {
		name     string
		req      *openai.ResponseRequest
		expected string
	}{
		{name: "absent", req: &openai.ResponseRequest{}, expected: ""},
		{
			name: "string form",
			req: &openai.ResponseRequest{Conversation: openai.ResponseNewParamsConversationUnion{
				OfString: ptr("conv_123"),
			}},
			expected: "conv_123",
		},
		{
			name: "object form",
			req: &openai.ResponseRequest{Conversation: openai.ResponseNewParamsConversationUnion{
				OfConversationObject: &openai.ResponseConversationParam{ID: "conv_456"},
			}},
			expected: "conv_456",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, responsesConversationID(tc.req))
		})
	}
}

// TestResponsesRecorder_conversationIDNotGated pins that the conversation id is
// recorded even with content capture off: it is an identifier, not content.
func TestResponsesRecorder_conversationIDNotGated(t *testing.T) {
	r := NewResponsesRecorder(NewConfig())

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, &openai.ResponseRequest{
			Model:        "gpt-5-nano",
			Conversation: openai.ResponseNewParamsConversationUnion{OfString: ptr("conv_123")},
		}, nil)
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(OperationName, "chat"),
		attribute.String(RequestModel, "gpt-5-nano"),
		attribute.String(ConversationID, "conv_123"),
	}, span.Attributes)
}

func TestResponsesResponseAttrs(t *testing.T) {
	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		span.SetAttributes(responsesResponseAttrs(&openai.Response{
			ID:    "resp_123",
			Model: "gpt-5-nano",
			Usage: &openai.ResponseUsage{
				InputTokens:         100,
				OutputTokens:        50,
				InputTokensDetails:  openai.ResponseUsageInputTokensDetails{CachedTokens: 80},
				OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{ReasoningTokens: 30},
			},
		})...)
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(ResponseID, "resp_123"),
		attribute.String(ResponseModel, "gpt-5-nano"),
		attribute.Int(UsageInputTokens, 100),
		attribute.Int(UsageOutputTokens, 50),
		attribute.Int(UsageCacheReadInputTokens, 80),
		attribute.Int(UsageReasoningOutputTokens, 30),
	}, span.Attributes)
}

// TestResponsesRequestAttrs_readFromRequest pins that parameters come from the
// request, whose fields are pointers, rather than the response, where zero and
// unset are indistinguishable.
func TestResponsesRequestAttrs_readFromRequest(t *testing.T) {
	require.Empty(t, responsesRequestAttrs(&openai.ResponseRequest{}))

	require.Equal(t, []attribute.KeyValue{
		attribute.Float64(RequestTemperature, 0),
	}, responsesRequestAttrs(&openai.ResponseRequest{Temperature: ptr(0.0)}))
}

func TestResponsesOutputMessages(t *testing.T) {
	tests := []struct {
		name     string
		resp     *openai.Response
		expected string
	}{
		{name: "no output", resp: &openai.Response{}, expected: ""},
		{
			name: "text output",
			resp: &openai.Response{Output: []openai.ResponseOutputItemUnion{{
				OfOutputMessage: &openai.ResponseOutputMessage{
					Role: "assistant",
					Content: openai.ResponseOutputMessageContentUnion{
						OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{{
							OfOutputText: &openai.ResponseOutputTextParam{Text: "hi there"},
						}},
					},
				},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"hi there"}]}]`,
		},
		{
			name: "function call",
			resp: &openai.Response{Output: []openai.ResponseOutputItemUnion{{
				OfFunctionCall: &openai.ResponseFunctionToolCall{
					CallID:    "call_1",
					Name:      "get_weather",
					Arguments: `{"city":"Berlin"}`,
				},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1",` +
				`"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}]}]`,
		},
		{
			// A generated image is recorded by type only: the payload is
			// base64 bytes and the conventions define no attribute for them.
			name: "image generation recorded by type only",
			resp: &openai.Response{Output: []openai.ResponseOutputItemUnion{{
				OfImageGenerationCall: &openai.ResponseOutputItemImageGenerationCall{},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"image"}]}]`,
		},
		{
			// The content union also has a bare string form, which older
			// providers return in place of the content array.
			name: "string content",
			resp: &openai.Response{Output: []openai.ResponseOutputItemUnion{{
				OfOutputMessage: &openai.ResponseOutputMessage{
					Role:    "assistant",
					Content: openai.ResponseOutputMessageContentUnion{OfString: ptr("hi there")},
				},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"text","content":"hi there"}]}]`,
		},
		{
			name: "reasoning recorded by type only",
			resp: &openai.Response{Output: []openai.ResponseOutputItemUnion{{
				OfReasoning: &openai.ResponseReasoningItem{},
			}}},
			expected: `[{"role":"assistant","parts":[{"type":"reasoning"}]}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(OutputMessages, responsesOutputMessages(tc.resp))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

// TestResponsesSystemInstructions pins that the instructions field maps to its
// own attribute rather than being folded into the conversation, because this
// API models it separately.
func TestResponsesSystemInstructions(t *testing.T) {
	tests := []struct {
		name         string
		instructions string
		expected     string
	}{
		{name: "absent", instructions: "", expected: ""},
		{name: "present", instructions: "be brief", expected: `[{"type":"text","content":"be brief"}]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := partsAttr(SystemInstructions, responsesSystemInstructions(&openai.ResponseRequest{
				Instructions: tc.instructions,
			}))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}
