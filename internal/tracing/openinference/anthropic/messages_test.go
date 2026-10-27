// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestConvertSSEToResponse(t *testing.T) {
	tests := []struct {
		name   string
		chunks []*anthropic.MessagesStreamChunk
		want   *anthropic.MessagesResponse
	}{
		{
			name: "basic text stream",
			chunks: []*anthropic.MessagesStreamChunk{
				{
					MessageStart: &anthropic.MessagesStreamChunkMessageStart{
						ID:    "msg_123",
						Model: "claude-3",
						Role:  "assistant",
						Usage: &anthropic.Usage{InputTokens: 10},
					},
				},
				{
					ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
						Index: 0,
						ContentBlock: anthropic.MessagesContentBlock{
							Text: &anthropic.TextBlock{Type: "text", Text: ""},
						},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "text_delta", Text: "Hello"},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "text_delta", Text: " World"},
					},
				},
				{
					ContentBlockStop: &anthropic.MessagesStreamChunkContentBlockStop{Index: 0},
				},
				{
					MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{
						Usage: anthropic.Usage{OutputTokens: 5},
						Delta: anthropic.MessagesStreamChunkMessageDeltaDelta{StopReason: "end_turn"},
					},
				},
				{MessageStop: &anthropic.MessagesStreamChunkMessageStop{}},
			},
			want: &anthropic.MessagesResponse{
				ID:    "msg_123",
				Model: "claude-3",
				Role:  "assistant",
				Usage: &anthropic.Usage{InputTokens: 10, OutputTokens: 5},
				Content: []anthropic.MessagesContentBlock{
					{Text: &anthropic.TextBlock{Type: "text", Text: "Hello World"}},
				},
				StopReason:   func() *anthropic.StopReason { s := anthropic.StopReason("end_turn"); return &s }(),
				StopSequence: func() *string { s := ""; return &s }(),
			},
		},
		{
			name: "tool use stream",
			chunks: []*anthropic.MessagesStreamChunk{
				{
					MessageStart: &anthropic.MessagesStreamChunkMessageStart{
						ID:    "msg_tool",
						Model: "claude-3",
						Role:  "assistant",
						Usage: &anthropic.Usage{InputTokens: 20},
					},
				},
				{
					ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
						Index: 0,
						ContentBlock: anthropic.MessagesContentBlock{
							Tool: &anthropic.ToolUseBlock{Type: "tool_use", ID: "tool_1", Name: "get_weather", Input: map[string]any{}},
						},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "input_json_delta", PartialJSON: `{"loc`},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "input_json_delta", PartialJSON: `ation": "NYC"}`},
					},
				},
				{
					ContentBlockStop: &anthropic.MessagesStreamChunkContentBlockStop{Index: 0},
				},
				{
					MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{
						Usage: anthropic.Usage{OutputTokens: 10},
						Delta: anthropic.MessagesStreamChunkMessageDeltaDelta{StopReason: "tool_use"},
					},
				},
			},
			want: &anthropic.MessagesResponse{
				ID:    "msg_tool",
				Model: "claude-3",
				Role:  "assistant",
				Usage: &anthropic.Usage{InputTokens: 20, OutputTokens: 10},
				Content: []anthropic.MessagesContentBlock{
					{Tool: &anthropic.ToolUseBlock{
						Type: "tool_use", ID: "tool_1", Name: "get_weather",
						Input: map[string]any{"location": "NYC"},
					}},
				},
				StopReason:   func() *anthropic.StopReason { s := anthropic.StopReason("tool_use"); return &s }(),
				StopSequence: func() *string { s := ""; return &s }(),
			},
		},
		{
			name: "thinking stream",
			chunks: []*anthropic.MessagesStreamChunk{
				{
					MessageStart: &anthropic.MessagesStreamChunkMessageStart{
						ID:    "msg_think",
						Model: "claude-3-5-sonnet",
						Role:  "assistant",
						Usage: &anthropic.Usage{InputTokens: 30},
					},
				},
				{
					ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
						Index: 0,
						ContentBlock: anthropic.MessagesContentBlock{
							Thinking: &anthropic.ThinkingBlock{Type: "thinking", Thinking: ""},
						},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "thinking_delta", Thinking: "Let me "},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "thinking_delta", Thinking: "think."},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 0,
						Delta: anthropic.ContentBlockDelta{Type: "signature_delta", Signature: "sig123"},
					},
				},
				{
					ContentBlockStop: &anthropic.MessagesStreamChunkContentBlockStop{Index: 0},
				},
				{
					ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
						Index: 1,
						ContentBlock: anthropic.MessagesContentBlock{
							Text: &anthropic.TextBlock{Type: "text", Text: ""},
						},
					},
				},
				{
					ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
						Index: 1,
						Delta: anthropic.ContentBlockDelta{Type: "text_delta", Text: "Answer"},
					},
				},
				{
					MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{
						Usage: anthropic.Usage{OutputTokens: 20},
						Delta: anthropic.MessagesStreamChunkMessageDeltaDelta{StopReason: "end_turn"},
					},
				},
			},
			want: &anthropic.MessagesResponse{
				ID:    "msg_think",
				Model: "claude-3-5-sonnet",
				Role:  "assistant",
				Usage: &anthropic.Usage{InputTokens: 30, OutputTokens: 20},
				Content: []anthropic.MessagesContentBlock{
					{Thinking: &anthropic.ThinkingBlock{Type: "thinking", Thinking: "Let me think.", Signature: "sig123"}},
					{Text: &anthropic.TextBlock{Type: "text", Text: "Answer"}},
				},
				StopReason:   func() *anthropic.StopReason { s := anthropic.StopReason("end_turn"); return &s }(),
				StopSequence: func() *string { s := ""; return &s }(),
			},
		},
		{
			name: "negative content block index is ignored",
			chunks: []*anthropic.MessagesStreamChunk{
				{
					MessageStart: &anthropic.MessagesStreamChunkMessageStart{
						ID:    "msg_neg",
						Model: "claude-3",
						Role:  "assistant",
						Usage: &anthropic.Usage{InputTokens: 5},
					},
				},
				{
					ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
						Index: -1,
						ContentBlock: anthropic.MessagesContentBlock{
							Text: &anthropic.TextBlock{Type: "text", Text: "bad"},
						},
					},
				},
			},
			want: &anthropic.MessagesResponse{
				ID:      "msg_neg",
				Model:   "claude-3",
				Role:    "assistant",
				Usage:   &anthropic.Usage{InputTokens: 5},
				Content: []anthropic.MessagesContentBlock{},
			},
		},
		{
			name: "content block index at cap is ignored",
			chunks: []*anthropic.MessagesStreamChunk{
				{
					MessageStart: &anthropic.MessagesStreamChunkMessageStart{
						ID:    "msg_cap",
						Model: "claude-3",
						Role:  "assistant",
						Usage: &anthropic.Usage{InputTokens: 5},
					},
				},
				{
					ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
						Index: 1000, // == maxContentBlocks, should be rejected
						ContentBlock: anthropic.MessagesContentBlock{
							Text: &anthropic.TextBlock{Type: "text", Text: "bad"},
						},
					},
				},
			},
			want: &anthropic.MessagesResponse{
				ID:      "msg_cap",
				Model:   "claude-3",
				Role:    "assistant",
				Usage:   &anthropic.Usage{InputTokens: 5},
				Content: []anthropic.MessagesContentBlock{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertSSEToResponse(tt.chunks)
			require.Equal(t, tt.want, got)
		})
	}
}

var (
	basicReq = &anthropic.MessagesRequest{
		MaxTokens: 100,
		Model:     "claude-3-opus-20240229",
		Messages: []anthropic.MessageParam{
			{
				Role:    anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{Text: "Hello!"},
			},
			{
				Role: anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{
					Array: []anthropic.ContentBlockParam{
						{Text: &anthropic.TextBlockParam{Type: "text", Text: "World"}},
					},
				},
			},
		},
	}
	basicReqBody, _ = json.Marshal(basicReq)

	basicResp = &anthropic.MessagesResponse{
		ID:    "msg_123",
		Model: "claude-3-opus-20240229",
		Role:  "assistant",
		Content: []anthropic.MessagesContentBlock{
			{Text: &anthropic.TextBlock{Type: "text", Text: "Hi there!"}},
			{Tool: &anthropic.ToolUseBlock{Type: "tool_use", ID: "tool_1", Name: "get_time", Input: map[string]any{"timezone": "UTC"}}},
		},
		Usage: &anthropic.Usage{
			InputTokens:  10,
			OutputTokens: 5,
		},
	}
)

func TestMessageRecorder_StartParams(t *testing.T) {
	recorder := NewMessageRecorderFromEnv()
	spanName, opts := recorder.StartParams(basicReq, basicReqBody)
	actualSpan := testotel.RecordNewSpan(t, spanName, opts...)

	require.Equal(t, "Message", actualSpan.Name)
	require.Equal(t, oteltrace.SpanKindInternal, actualSpan.SpanKind)
}

func TestMessageRecorder_RecordRequest(t *testing.T) {
	tests := []struct {
		name          string
		req           *anthropic.MessagesRequest
		reqBody       []byte
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:    "basic request",
			req:     basicReq,
			reqBody: basicReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemAnthropic),
				attribute.String(openinference.LLMModelName, "claude-3-opus-20240229"),
				attribute.String(openinference.InputValue, string(basicReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"claude-3-opus-20240229","max_tokens":100}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello!"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), "World"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewMessageRecorderFromEnv()

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordRequest(span, tt.req, tt.reqBody)
				return false
			})

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, actualSpan.Attributes)
		})
	}
}

func TestMessageRecorder_RecordResponse(t *testing.T) {
	tests := []struct {
		name          string
		resp          *anthropic.MessagesResponse
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic response",
			resp: basicResp,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "claude-3-opus-20240229"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageContent), "Hi there!"),
				attribute.String(openinference.OutputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallID), "tool_1"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), "get_time"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), `{"timezone":"UTC"}`),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, 0),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 0),
				attribute.Int(openinference.LLMTokenCountCompletion, 5),
				attribute.Int(openinference.LLMTokenCountTotal, 15),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := NewMessageRecorderFromEnv()

			actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				recorder.RecordResponse(span, tt.resp)
				return false
			})

			// Check OutputValue separately as it is a JSON string
			var outputValue string
			for _, attr := range actualSpan.Attributes {
				if attr.Key == openinference.OutputValue {
					outputValue = attr.Value.AsString()
					break
				}
			}
			require.NotEmpty(t, outputValue)
			require.JSONEq(t, func() string { b, _ := json.Marshal(tt.resp); return string(b) }(), outputValue)

			// Filter out OutputValue for easier comparison
			var otherAttrs []attribute.KeyValue
			for _, attr := range actualSpan.Attributes {
				if attr.Key != openinference.OutputValue {
					otherAttrs = append(otherAttrs, attr)
				}
			}

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, otherAttrs)
			require.Equal(t, trace.Status{Code: codes.Ok, Description: ""}, actualSpan.Status)
		})
	}
}

func TestMessageRecorder_RecordResponse_NoUsage(t *testing.T) {
	// A response without usage, for example one folded from a stream that never carried usage
	// or translated from a Bedrock response without usage, must not panic.
	resp := &anthropic.MessagesResponse{
		ID:      "msg_no_usage",
		Model:   "claude-3-opus-20240229",
		Role:    "assistant",
		Content: []anthropic.MessagesContentBlock{{Text: &anthropic.TextBlock{Type: "text", Text: "Hi there!"}}},
	}
	recorder := NewMessageRecorderFromEnv()

	var attrs []attribute.KeyValue
	require.NotPanics(t, func() {
		attrs = testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			recorder.RecordResponse(span, resp)
			return false
		}).Attributes
	})
	for _, attr := range attrs {
		require.NotEqual(t, attribute.Key(openinference.LLMTokenCountPrompt), attr.Key)
	}
}
