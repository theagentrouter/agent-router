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

func TestResponsesInputMessages(t *testing.T) {
	items := func(items ...openai.ResponseInputItemUnionParam) *openai.ResponseRequest {
		return &openai.ResponseRequest{Input: openai.ResponseNewParamsInputUnion{OfInputItemList: items}}
	}
	tests := []struct {
		name     string
		req      *openai.ResponseRequest
		expected string
	}{
		{name: "no input", req: &openai.ResponseRequest{}, expected: ""},
		{
			name:     "string input is a user message",
			req:      &openai.ResponseRequest{Input: openai.ResponseNewParamsInputUnion{OfString: ptr("hi")}},
			expected: `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]`,
		},
		{
			name:     "empty string input records nothing",
			req:      &openai.ResponseRequest{Input: openai.ResponseNewParamsInputUnion{OfString: ptr("")}},
			expected: "",
		},
		{
			name: "message with string content",
			req: items(openai.ResponseInputItemUnionParam{OfMessage: &openai.EasyInputMessageParam{
				Role:    "user",
				Content: openai.EasyInputMessageContentUnionParam{OfString: ptr("hi")},
			}}),
			expected: `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]`,
		},
		{
			name: "message with empty content records nothing",
			req: items(openai.ResponseInputItemUnionParam{OfMessage: &openai.EasyInputMessageParam{
				Role:    "user",
				Content: openai.EasyInputMessageContentUnionParam{OfString: ptr("")},
			}}),
			expected: "",
		},
		{
			// Images are recorded by type only, as on the chat path.
			name: "message with content list",
			req: items(openai.ResponseInputItemUnionParam{OfMessage: &openai.EasyInputMessageParam{
				Role: "user",
				Content: openai.EasyInputMessageContentUnionParam{OfInputItemContentList: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "look"}},
					{OfInputImage: &openai.ResponseInputImageParam{}},
				}},
			}}),
			expected: `[{"role":"user","parts":[{"type":"text","content":"look"},{"type":"image"}]}]`,
		},
		{
			name: "message without a role defaults to user",
			req: items(openai.ResponseInputItemUnionParam{OfMessage: &openai.EasyInputMessageParam{
				Content: openai.EasyInputMessageContentUnionParam{OfString: ptr("hi")},
			}}),
			expected: `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]`,
		},
		{
			// System messages occupy a position in the conversation, as on the
			// chat path, rather than being split into system instructions.
			name: "system message stays in the conversation",
			req: items(openai.ResponseInputItemUnionParam{OfMessage: &openai.EasyInputMessageParam{
				Role:    "system",
				Content: openai.EasyInputMessageContentUnionParam{OfString: ptr("be brief")},
			}}),
			expected: `[{"role":"system","parts":[{"type":"text","content":"be brief"}]}]`,
		},
		{
			name: "empty replayed output message records nothing",
			req: items(openai.ResponseInputItemUnionParam{OfOutputMessage: &openai.ResponseOutputMessage{
				Role: "assistant",
			}}),
			expected: "",
		},
		{
			name: "input message keeps its role",
			req: items(openai.ResponseInputItemUnionParam{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role:    "developer",
				Content: []openai.ResponseInputContentUnionParam{{OfInputText: &openai.ResponseInputTextParam{Text: "be brief"}}},
			}}),
			expected: `[{"role":"developer","parts":[{"type":"text","content":"be brief"}]}]`,
		},
		{
			// A prior turn is replayed as consecutive output items and folds
			// into one assistant message, as it was recorded on the response side.
			name: "replayed turn folds into one assistant message",
			req: items(
				openai.ResponseInputItemUnionParam{OfReasoning: &openai.ResponseReasoningItem{}},
				openai.ResponseInputItemUnionParam{OfOutputMessage: &openai.ResponseOutputMessage{
					Role: "assistant",
					Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{{
						OfOutputText: &openai.ResponseOutputTextParam{Text: "earlier"},
					}}},
				}},
				openai.ResponseInputItemUnionParam{OfFunctionCall: &openai.ResponseFunctionToolCall{
					CallID: "call_1", Name: "get_weather", Arguments: `{}`,
				}},
			),
			expected: `[{"role":"assistant","parts":[{"type":"reasoning"},{"type":"text","content":"earlier"},` +
				`{"type":"tool_call","id":"call_1","name":"get_weather","arguments":"{}"}]}]`,
		},
		{
			name: "tool output closes the assistant turn",
			req: items(
				openai.ResponseInputItemUnionParam{OfFunctionCall: &openai.ResponseFunctionToolCall{CallID: "call_1", Name: "f", Arguments: `{}`}},
				openai.ResponseInputItemUnionParam{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
					CallID: "call_1", Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: ptr("ok")},
				}},
				openai.ResponseInputItemUnionParam{OfOutputMessage: &openai.ResponseOutputMessage{
					Content: openai.ResponseOutputMessageContentUnion{OfString: ptr("done")},
				}},
			),
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"f","arguments":"{}"}]},` +
				`{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1","content":"ok"}]},` +
				`{"role":"assistant","parts":[{"type":"text","content":"done"}]}]`,
		},
		{
			name:     "replayed image generation recorded by type only",
			req:      items(openai.ResponseInputItemUnionParam{OfImageGenerationCall: &openai.ResponseInputItemImageGenerationCallParam{}}),
			expected: `[{"role":"assistant","parts":[{"type":"image"}]}]`,
		},
		{
			name: "function call",
			req: items(openai.ResponseInputItemUnionParam{OfFunctionCall: &openai.ResponseFunctionToolCall{
				CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Berlin"}`,
			}}),
			expected: `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1",` +
				`"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}]}]`,
		},
		{
			name: "function call output",
			req: items(openai.ResponseInputItemUnionParam{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_1",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: ptr("72F")},
			}}),
			expected: `[{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1","content":"72F"}]}]`,
		},
		{
			// Mirrors the chat path, which records the first text block of a tool result.
			name: "function call output array records its first text",
			req: items(openai.ResponseInputItemUnionParam{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_1",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfResponseFunctionCallOutputItemArray: []openai.ResponseInputItemFunctionCallOutputItemUnionParam{
						{OfInputText: &openai.ResponseInputTextContentParam{Text: "72F"}},
						{OfInputImage: &openai.ResponseInputImageContentParam{}},
						{OfInputText: &openai.ResponseInputTextContentParam{Text: "sunny"}},
					},
				},
			}}),
			expected: `[{"role":"tool","parts":[{"type":"tool_call_response","id":"call_1","content":"72F"}]}]`,
		},
		{
			name:     "reasoning recorded by type only",
			req:      items(openai.ResponseInputItemUnionParam{OfReasoning: &openai.ResponseReasoningItem{}}),
			expected: `[{"role":"assistant","parts":[{"type":"reasoning"}]}]`,
		},
		{
			name:     "unmapped items are skipped",
			req:      items(openai.ResponseInputItemUnionParam{OfWebSearchCall: &openai.ResponseFunctionWebSearch{}}),
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(InputMessages, responsesInputMessages(tc.req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestResponsesFoldChunks(t *testing.T) {
	created := &openai.ResponseStreamEventUnion{OfResponseCreated: &openai.ResponseCreatedEvent{
		Response: openai.Response{ID: "resp_1", Model: "gpt-5"},
	}}
	completed := &openai.ResponseStreamEventUnion{OfResponseCompleted: &openai.ResponseCompletedEvent{
		Response: openai.Response{ID: "resp_1", Model: "gpt-5", Usage: &openai.ResponseUsage{InputTokens: 3, OutputTokens: 2}},
	}}
	incomplete := &openai.ResponseStreamEventUnion{OfResponseIncomplete: &openai.ResponseIncompleteEvent{
		Response: openai.Response{ID: "resp_1", Usage: &openai.ResponseUsage{OutputTokens: 9}},
	}}
	failed := &openai.ResponseStreamEventUnion{OfResponseFailed: &openai.ResponseFailedEvent{
		Response: openai.Response{ID: "resp_1"},
	}}

	tests := []struct {
		name     string
		chunks   []*openai.ResponseStreamEventUnion
		expected *openai.Response
	}{
		{name: "completed wins over created", chunks: []*openai.ResponseStreamEventUnion{created, completed}, expected: &completed.OfResponseCompleted.Response},
		{name: "incomplete carries the response", chunks: []*openai.ResponseStreamEventUnion{created, incomplete}, expected: &incomplete.OfResponseIncomplete.Response},
		{name: "failed carries the response", chunks: []*openai.ResponseStreamEventUnion{created, failed}, expected: &failed.OfResponseFailed.Response},
		{name: "last terminal event wins", chunks: []*openai.ResponseStreamEventUnion{completed, incomplete}, expected: &incomplete.OfResponseIncomplete.Response},
		{name: "no terminal event yields an empty response", chunks: []*openai.ResponseStreamEventUnion{created}, expected: &openai.Response{}},
		{name: "nil chunks are skipped", chunks: []*openai.ResponseStreamEventUnion{nil, completed, nil}, expected: &completed.OfResponseCompleted.Response},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, responsesFoldChunks(tc.chunks))
		})
	}
}

// TestResponsesInputMessages_replayMatchesOutput pins that a prior response
// replayed on the next request is recorded exactly as it was on the response
// side, so a conversation reads the same across turns.
func TestResponsesInputMessages_replayMatchesOutput(t *testing.T) {
	resp := &openai.Response{Output: []openai.ResponseOutputItemUnion{
		{OfReasoning: &openai.ResponseReasoningItem{}},
		{OfOutputMessage: &openai.ResponseOutputMessage{
			Role: "assistant",
			Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{{
				OfOutputText: &openai.ResponseOutputTextParam{Text: "checking"},
			}}},
		}},
		{OfFunctionCall: &openai.ResponseFunctionToolCall{CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Berlin"}`}},
		{OfImageGenerationCall: &openai.ResponseOutputItemImageGenerationCall{}},
	}}
	replay := &openai.ResponseRequest{Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
		{OfReasoning: resp.Output[0].OfReasoning},
		{OfOutputMessage: resp.Output[1].OfOutputMessage},
		{OfFunctionCall: resp.Output[2].OfFunctionCall},
		{OfImageGenerationCall: &openai.ResponseInputItemImageGenerationCallParam{}},
	}}}

	want := messagesAttr(OutputMessages, responsesOutputMessages(resp))
	got := messagesAttr(InputMessages, responsesInputMessages(replay))
	require.Len(t, want, 1)
	require.Len(t, got, 1)
	require.JSONEq(t, want[0].Value.AsString(), got[0].Value.AsString())
}
