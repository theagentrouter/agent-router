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
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestChatToolDefinitions(t *testing.T) {
	tests := []struct {
		name     string
		req      *openai.ChatCompletionRequest
		expected string
	}{
		{name: "no tools", req: &openai.ChatCompletionRequest{}, expected: ""},
		{
			name: "function tool",
			req: &openai.ChatCompletionRequest{Tools: []openai.Tool{{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        "get_weather",
					Description: "Look up the weather",
					Parameters:  map[string]any{"type": "object"},
				},
			}}},
			expected: `[{"type":"function","name":"get_weather","description":"Look up the weather",` +
				`"parameters":{"type":"object"}}]`,
		},
		{
			// Provider-native tools have no function block; the type alone is
			// still worth recording.
			name: "provider tool without a function block",
			req: &openai.ChatCompletionRequest{Tools: []openai.Tool{{
				Type: openai.ToolType("google_search"),
			}}},
			expected: `[{"type":"google_search","name":""}]`,
		},
		{
			// A tool with neither a type nor a name describes nothing, so it
			// is dropped rather than emitted as an empty object.
			name:     "tool with neither type nor name is dropped",
			req:      &openai.ChatCompletionRequest{Tools: []openai.Tool{{}}},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := toolDefinitionsAttr(chatToolDefinitions(tc.req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestAnthropicToolDefinitions(t *testing.T) {
	defs := anthropicToolDefinitions(&anthropicschema.MessagesRequest{
		Tools: []anthropicschema.ToolUnion{
			{Tool: &anthropicschema.Tool{
				Type:        "custom",
				Name:        "get_weather",
				Description: "Look up the weather",
			}},
			{BashTool: &anthropicschema.BashTool{Type: "bash_20250124", Name: "bash"}},
		},
	})

	require.Len(t, defs, 2)
	require.Equal(t, "get_weather", defs[0].Name)
	require.Equal(t, "Look up the weather", defs[0].Description)
	require.Equal(t, "bash", defs[1].Name)
	require.Equal(t, "bash_20250124", defs[1].Type)
}

// TestAnthropicToolDefinitions_variants pins every arm of the tool union. The
// provider-native tools are fixed capabilities, so they are recorded by type
// and name; only a custom tool carries a schema.
func TestAnthropicToolDefinitions_variants(t *testing.T) {
	defs := anthropicToolDefinitions(&anthropicschema.MessagesRequest{
		Tools: []anthropicschema.ToolUnion{
			{WebSearchTool: &anthropicschema.WebSearchTool{Type: "web_search_20250305", Name: "web_search"}},
			{TextEditorTool20250124: &anthropicschema.TextEditorTool20250124{Type: "text_editor_20250124", Name: "str_replace_editor"}},
			{TextEditorTool20250429: &anthropicschema.TextEditorTool20250429{Type: "text_editor_20250429", Name: "str_replace_based_edit_tool"}},
			{TextEditorTool20250728: &anthropicschema.TextEditorTool20250728{Type: "text_editor_20250728", Name: "str_replace_based_edit_tool"}},
			// An unset union must not contribute a blank definition.
			{},
		},
	})

	require.Equal(t, []toolDefinition{
		{Type: "web_search_20250305", Name: "web_search"},
		{Type: "text_editor_20250124", Name: "str_replace_editor"},
		{Type: "text_editor_20250429", Name: "str_replace_based_edit_tool"},
		{Type: "text_editor_20250728", Name: "str_replace_based_edit_tool"},
	}, defs)
}

// TestAnthropicToolDefinitions_schema pins how the input schema is carried. An
// absent schema must not emit "parameters":null, which a consumer reads as "the
// tool takes no arguments" rather than "the schema was not declared".
func TestAnthropicToolDefinitions_schema(t *testing.T) {
	tests := []struct {
		name     string
		schema   json.RawMessage
		expected string
	}{
		{
			name:     "absent schema omits parameters",
			expected: `[{"type":"custom","name":"get_weather"}]`,
		},
		{
			name:     "schema is carried as-is",
			schema:   json.RawMessage(`{"type":"object","additionalProperties":false}`),
			expected: `[{"type":"custom","name":"get_weather","parameters":{"type":"object","additionalProperties":false}}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := toolDefinitionsAttr(anthropicToolDefinitions(&anthropicschema.MessagesRequest{
				Tools: []anthropicschema.ToolUnion{
					{Tool: &anthropicschema.Tool{Type: "custom", Name: "get_weather", InputSchema: tc.schema}},
				},
			}))
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

// TestToolDefinitions_gatedByCapture pins that tool schemas are treated as
// content: they are authored by the caller and can carry proprietary detail.
func TestToolDefinitions_gatedByCapture(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model: "gpt-5-nano",
		Tools: []openai.Tool{{
			Type:     openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{Name: "internal_tool"},
		}},
	}

	for _, capture := range []bool{false, true} {
		t.Run(t.Name(), func(t *testing.T) {
			r := NewChatCompletionRecorder(&Config{CaptureMessageContent: capture})
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, req, nil)
				return false
			})
			require.Equal(t, capture, hasAttr(span.Attributes, ToolDefinitions))
		})
	}
}

func TestCompletionMessages(t *testing.T) {
	tests := []struct {
		name     string
		prompt   any
		expected string
	}{
		{name: "empty", prompt: "", expected: ""},
		{
			name:     "single string becomes a user message",
			prompt:   "once upon a time",
			expected: `[{"role":"user","parts":[{"type":"text","content":"once upon a time"}]}]`,
		},
		{
			name:   "string list becomes one message each",
			prompt: []string{"a", "b"},
			expected: `[{"role":"user","parts":[{"type":"text","content":"a"}]},` +
				`{"role":"user","parts":[{"type":"text","content":"b"}]}]`,
		},
		{
			// Pre-tokenized prompts are described by length rather than decoded,
			// because the gateway has no tokenizer for the target model.
			name:     "token array reports its length",
			prompt:   []int64{1, 2, 3},
			expected: `[{"role":"user","parts":[{"type":"text","content":"<3 tokens>"}]}]`,
		},
		{
			// A batch of pre-tokenized prompts becomes one message each, so the
			// batch size stays visible.
			name:   "nested token arrays report each length",
			prompt: [][]int64{{1, 2, 3}, {4, 5}},
			expected: `[{"role":"user","parts":[{"type":"text","content":"<3 tokens>"}]},` +
				`{"role":"user","parts":[{"type":"text","content":"<2 tokens>"}]}]`,
		},
		{name: "unsupported type", prompt: 42, expected: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &openai.CompletionRequest{Prompt: openai.PromptUnion{Value: tc.prompt}}
			attrs := messagesAttr(InputMessages, completionInputMessages(req))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestCompletionOutputMessages(t *testing.T) {
	attrs := messagesAttr(OutputMessages, completionOutputMessages(&openai.CompletionResponse{
		Choices: []openai.CompletionChoice{{Text: "the end", FinishReason: "stop"}},
	}))
	require.Len(t, attrs, 1)
	require.JSONEq(t,
		`[{"role":"assistant","parts":[{"type":"text","content":"the end"}],"finish_reason":"stop"}]`,
		attrs[0].Value.AsString())
}
