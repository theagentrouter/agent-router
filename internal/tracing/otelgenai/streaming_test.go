// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	oteltrace "go.opentelemetry.io/otel/trace"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// TestChatStreamingMatchesUnary is the property that matters for streaming: a
// streamed response and the equivalent unary one must produce the same span.
// Backends cannot tell the two apart, so any divergence is a bug.
func TestChatStreamingMatchesUnary(t *testing.T) {
	cfg := &Config{CaptureMessageContent: true}
	r := NewChatCompletionRecorder(cfg)

	unary := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-1",
		Model: "gpt-5-nano",
		Usage: openai.Usage{
			PromptTokens:            10,
			CompletionTokens:        4,
			PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 8},
			CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: 2},
		},
		Choices: []openai.ChatCompletionResponseChoice{{
			FinishReason: "stop",
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: ptr("hello world"),
			},
		}},
	}

	finish := openai.ChatCompletionChoicesFinishReason("stop")
	streamed := []*openai.ChatCompletionResponseChunk{
		{ID: "chatcmpl-1", Model: "gpt-5-nano", Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{Role: openai.ChatMessageRoleAssistant, Content: ptr("hello ")},
		}}},
		{ID: "chatcmpl-1", Model: "gpt-5-nano", Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{Content: ptr("world")},
		}}},
		{ID: "chatcmpl-1", Model: "gpt-5-nano", Choices: []openai.ChatCompletionResponseChunkChoice{{
			FinishReason: finish,
		}}},
		{ID: "chatcmpl-1", Model: "gpt-5-nano", Usage: &openai.Usage{
			PromptTokens:            10,
			CompletionTokens:        4,
			PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: 8},
			CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: 2},
		}},
	}

	unarySpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, unary)
		return false
	})
	streamSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponseChunks(span, streamed)
		return false
	})

	testotel.RequireAttributesEqual(t, unarySpan.Attributes, streamSpan.Attributes)
}

func TestChatCompletionChunkMessages_toolCallFragments(t *testing.T) {
	// Tool arguments arrive split across chunks and must be concatenated.
	chunks := []*openai.ChatCompletionResponseChunk{
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
					Index:    0,
					ID:       ptr("call_1"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_weather", Arguments: `{"ci`},
				}},
			},
		}}},
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
					Index:    0,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{Arguments: `ty":"Berlin"}`},
				}},
			},
		}}},
	}

	attrs := messagesAttr(OutputMessages, chatCompletionChunkMessages(chunks))
	require.Len(t, attrs, 1)
	require.JSONEq(t,
		`[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"get_weather",`+
			`"arguments":"{\"city\":\"Berlin\"}"}]}]`,
		attrs[0].Value.AsString())
}

// TestChatCompletionChunkMessages_finishReason pins that the folded message
// carries the finish reason, which arrives on its own trailing chunk rather
// than alongside the content it terminates.
func TestChatCompletionChunkMessages_finishReason(t *testing.T) {
	chunks := []*openai.ChatCompletionResponseChunk{
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				Role:    openai.ChatMessageRoleAssistant,
				Content: ptr("hi there"),
			},
		}}},
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta:        &openai.ChatCompletionResponseChunkChoiceDelta{},
			FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
		}}},
	}

	attrs := messagesAttr(OutputMessages, chatCompletionChunkMessages(chunks))
	require.Len(t, attrs, 1)
	require.JSONEq(t,
		`[{"role":"assistant","parts":[{"type":"text","content":"hi there"}],"finish_reason":"stop"}]`,
		attrs[0].Value.AsString())
}

// TestChatCompletionChunkMessages_toolCallFragmentsOutOfOrder pins that
// fragments are attributed by the index the provider sends, not by their
// position within a chunk. Every chunk after the first carries the calls in a
// position that disagrees with its index, so positional keying concatenates
// each fragment onto the wrong call and fails this test.
func TestChatCompletionChunkMessages_toolCallFragmentsOutOfOrder(t *testing.T) {
	chunks := []*openai.ChatCompletionResponseChunk{
		// Both calls open, in index order.
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
					{
						Index:    0,
						ID:       ptr("call_weather"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_weather", Arguments: `{"city":`},
					},
					{
						Index:    1,
						ID:       ptr("call_time"),
						Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_time", Arguments: `{"tz":`},
					},
				},
			},
		}}},
		// Only index 1 continues, but it sits at position 0.
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
					Index:    1,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{Arguments: `"UTC"}`},
				}},
			},
		}}},
		// Both continue, but reordered relative to their indices.
		{Choices: []openai.ChatCompletionResponseChunkChoice{{
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
					{
						Index:    1,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{Arguments: ``},
					},
					{
						Index:    0,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{Arguments: `"Berlin"}`},
					},
				},
			},
		}}},
	}

	attrs := messagesAttr(OutputMessages, chatCompletionChunkMessages(chunks))
	require.Len(t, attrs, 1)
	require.JSONEq(t,
		`[{"role":"assistant","parts":[`+
			`{"type":"tool_call","id":"call_weather","name":"get_weather","arguments":"{\"city\":\"Berlin\"}"},`+
			`{"type":"tool_call","id":"call_time","name":"get_time","arguments":"{\"tz\":\"UTC\"}"}]}]`,
		attrs[0].Value.AsString())
}

func TestChatCompletionChunkMessages_multipleChoices(t *testing.T) {
	chunks := []*openai.ChatCompletionResponseChunk{
		{Choices: []openai.ChatCompletionResponseChunkChoice{
			{Index: 1, Delta: &openai.ChatCompletionResponseChunkChoiceDelta{Content: ptr("second")}},
			{Index: 0, Delta: &openai.ChatCompletionResponseChunkChoiceDelta{Content: ptr("first")}},
		}},
	}

	msgs := chatCompletionChunkMessages(chunks)
	require.Len(t, msgs, 2)
	// Order follows first appearance, so choices stay distinguishable.
	require.Equal(t, "second", msgs[0].Parts[0].Content)
	require.Equal(t, "first", msgs[1].Parts[0].Content)
}

// TestAnthropicStreamingMatchesUnary pins the same property for Anthropic,
// which reaches it by folding chunks back into a MessagesResponse.
func TestAnthropicStreamingMatchesUnary(t *testing.T) {
	stop := anthropicschema.StopReason("end_turn")
	unary := &anthropicschema.MessagesResponse{
		ID:         "msg_1",
		Model:      "claude-sonnet-5",
		Role:       "assistant",
		StopReason: &stop,
		Usage:      &anthropicschema.Usage{InputTokens: 10, OutputTokens: 4},
		Content: []anthropicschema.MessagesContentBlock{
			{Text: &anthropicschema.TextBlock{Text: "hello world"}},
		},
	}

	chunks := []*anthropicschema.MessagesStreamChunk{
		{MessageStart: (*anthropicschema.MessagesStreamChunkMessageStart)(&anthropicschema.MessagesResponse{
			ID:    "msg_1",
			Model: "claude-sonnet-5",
			Role:  "assistant",
			Usage: &anthropicschema.Usage{InputTokens: 10},
		})},
		{ContentBlockStart: &anthropicschema.MessagesStreamChunkContentBlockStart{
			Index:        0,
			ContentBlock: anthropicschema.MessagesContentBlock{Text: &anthropicschema.TextBlock{Text: ""}},
		}},
		{ContentBlockDelta: &anthropicschema.MessagesStreamChunkContentBlockDelta{
			Index: 0,
			Delta: anthropicschema.ContentBlockDelta{Text: "hello "},
		}},
		{ContentBlockDelta: &anthropicschema.MessagesStreamChunkContentBlockDelta{
			Index: 0,
			Delta: anthropicschema.ContentBlockDelta{Text: "world"},
		}},
		{MessageDelta: &anthropicschema.MessagesStreamChunkMessageDelta{
			Delta: anthropicschema.MessagesStreamChunkMessageDeltaDelta{StopReason: stop},
			Usage: anthropicschema.Usage{OutputTokens: 4},
		}},
	}

	r := NewMessageRecorder(&Config{CaptureMessageContent: true})

	unarySpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, unary)
		return false
	})
	streamSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponseChunks(span, chunks)
		return false
	})

	testotel.RequireAttributesEqual(t, unarySpan.Attributes, streamSpan.Attributes)
}

// TestChatCompletionChunkAttrs_emptyModel pins that an empty model in a chunk
// never reaches the span. Azure OpenAI opens a stream with a content-filter
// chunk carrying model:"", so taking the model from the first chunk records an
// empty gen_ai.response.model.
func TestChatCompletionChunkAttrs_emptyModel(t *testing.T) {
	responseModel := func(chunks []*openai.ChatCompletionResponseChunk) (string, bool) {
		for _, attr := range chatCompletionChunkAttrs(chunks) {
			if string(attr.Key) == ResponseModel {
				return attr.Value.AsString(), true
			}
		}
		return "", false
	}

	t.Run("content-filter prologue does not win", func(t *testing.T) {
		model, ok := responseModel([]*openai.ChatCompletionResponseChunk{
			{ID: "chatcmpl-1"},
			{ID: "chatcmpl-1", Model: "gpt-5-nano"},
		})
		require.True(t, ok)
		require.Equal(t, "gpt-5-nano", model)
	})

	t.Run("trailing empty model does not clear it", func(t *testing.T) {
		model, ok := responseModel([]*openai.ChatCompletionResponseChunk{
			{ID: "chatcmpl-1", Model: "gpt-5-nano"},
			{ID: "chatcmpl-1", Usage: &openai.Usage{PromptTokens: 1}},
		})
		require.True(t, ok)
		require.Equal(t, "gpt-5-nano", model)
	})

	t.Run("no model anywhere omits the attribute", func(t *testing.T) {
		_, ok := responseModel([]*openai.ChatCompletionResponseChunk{{ID: "chatcmpl-1"}})
		require.False(t, ok)
	})
}

func TestChunkRecording_boundaries(t *testing.T) {
	cfg := &Config{CaptureMessageContent: true}

	t.Run("chat", func(t *testing.T) {
		r := NewChatCompletionRecorder(cfg)
		for _, chunks := range [][]*openai.ChatCompletionResponseChunk{
			nil, {}, {nil}, {{}},
		} {
			require.NotPanics(t, func() {
				testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
					r.RecordResponseChunks(span, chunks)
					return false
				})
			})
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		r := NewMessageRecorder(cfg)
		for _, chunks := range [][]*anthropicschema.MessagesStreamChunk{
			nil, {}, {{}},
		} {
			require.NotPanics(t, func() {
				testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
					r.RecordResponseChunks(span, chunks)
					return false
				})
			})
		}
	})
}
