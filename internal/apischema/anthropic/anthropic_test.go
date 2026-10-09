// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestMessageContent_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    MessageContent
		wantErr bool
	}{
		{
			name:    "string content",
			jsonStr: `"Hello, world!"`,
			want:    MessageContent{Text: "Hello, world!"},
			wantErr: false,
		},
		{
			name:    "array content",
			jsonStr: `[{"type": "text", "text": "Hello, "}, {"type": "text", "text": "world!"}]`,
			want: MessageContent{Array: []ContentBlockParam{
				{Text: &TextBlockParam{Text: "Hello, ", Type: "text"}},
				{Text: &TextBlockParam{Text: "world!", Type: "text"}},
			}},
			wantErr: false,
		},
		{
			name:    "invalid content",
			jsonStr: `12345`,
			want:    MessageContent{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MessageContent
			err := mc.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, mc)
		})
	}
}

func TestMessageContent_MessagesStreamChunk(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		exp     MessagesStreamChunk
		wantErr bool
	}{
		{
			name:    "message_start",
			jsonStr: `{"type":"message_start","message":{"id":"msg_014p7gG3wDgGV9EUtLvnow3U","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","stop_sequence":null,"usage":{"input_tokens":472,"output_tokens":2},"content":[],"stop_reason":null}}`,
			exp: MessagesStreamChunk{
				Type: "message_start",
				MessageStart: &MessagesStreamChunkMessageStart{
					ID:           "msg_014p7gG3wDgGV9EUtLvnow3U",
					Type:         "message",
					Role:         "assistant",
					Model:        "claude-sonnet-4-5-20250929",
					StopSequence: nil,
					Usage: &Usage{
						InputTokens:  472,
						OutputTokens: 2,
					},
					Content:    []MessagesContentBlock{},
					StopReason: nil,
				},
			},
			wantErr: false,
		},
		{
			name: "content_block_start",
			exp: MessagesStreamChunk{
				Type: "content_block_start",
				ContentBlockStart: &MessagesStreamChunkContentBlockStart{
					Type:  "content_block_start",
					Index: 0,
					ContentBlock: MessagesContentBlock{
						Text: &TextBlock{
							Type: "text",
							Text: "",
						},
					},
				},
			},
			jsonStr: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			name: "content_block_delta",
			exp: MessagesStreamChunk{
				Type: "content_block_delta",
				ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Type:  "content_block_delta",
					Index: 0,
					Delta: ContentBlockDelta{
						Type: "text_delta",
						Text: "Okay",
					},
				},
			},
			jsonStr: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Okay"}}`,
		},
		{
			name: "content_block_delta input_json_delta",
			exp: MessagesStreamChunk{
				Type: "content_block_delta",
				ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Type:  "content_block_delta",
					Index: 1,
					Delta: ContentBlockDelta{
						Type:        "input_json_delta",
						PartialJSON: "{\"query",
					},
				},
			},
			jsonStr: `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query"}}`,
		},
		{
			name: "content_block_stop",
			exp: MessagesStreamChunk{
				Type: "content_block_stop",
				ContentBlockStop: &MessagesStreamChunkContentBlockStop{
					Type:  "content_block_stop",
					Index: 1,
				},
			},
			jsonStr: `{"type":"content_block_stop","index":1}`,
		},
		{
			name: "content_block_delta thinking_delta",
			exp: MessagesStreamChunk{
				Type: "content_block_delta",
				ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Type:  "content_block_delta",
					Index: 0,
					Delta: ContentBlockDelta{
						Type:     "thinking_delta",
						Thinking: "Let me solve this step by step",
					},
				},
			},
			jsonStr: `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me solve this step by step"}}`,
		},
		{
			name: "content_block_delta signature_delta",
			exp: MessagesStreamChunk{
				Type: "content_block_delta",
				ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Type:  "content_block_delta",
					Index: 0,
					Delta: ContentBlockDelta{
						Type:      "signature_delta",
						Signature: "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds...",
					},
				},
			},
			jsonStr: `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds..."}}`,
		},
		{
			name: "message_delta",
			exp: MessagesStreamChunk{
				Type: "message_delta",
				MessageDelta: &MessagesStreamChunkMessageDelta{
					Type: "message_delta",
					Delta: MessagesStreamChunkMessageDeltaDelta{
						StopReason:   "tool_use",
						StopSequence: "",
					},
					Usage: Usage{
						OutputTokens: 89,
					},
				},
			},
			jsonStr: `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}`,
		},
		{
			name: "message_stop",
			exp: MessagesStreamChunk{
				Type: "message_stop",
				MessageStop: &MessagesStreamChunkMessageStop{
					Type: "message_stop",
				},
			},
			jsonStr: ` {"type":"message_stop"}`,
		},
		{
			name:    "invalid event",
			jsonStr: `abcdes`,
			wantErr: true,
		},
		{
			name:    "type field does not exist",
			jsonStr: `{"foo":"bar"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mse MessagesStreamChunk
			err := mse.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.exp, mse)
		})
	}
}

func TestMessageContent_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		mc      MessageContent
		want    string
		wantErr bool
	}{
		{
			name: "string content",
			mc:   MessageContent{Text: "Hello, world!"},
			want: `"Hello, world!"`,
		},
		{
			name: "array content",
			mc: MessageContent{Array: []ContentBlockParam{
				{Text: &TextBlockParam{Text: "Hello, ", Type: "text"}},
				{Text: &TextBlockParam{Text: "world!", Type: "text"}},
			}},
			want: `[{"text":"Hello, ","type":"text"},{"text":"world!","type":"text"}]`,
		},
		{
			name:    "empty content",
			mc:      MessageContent{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.mc.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestContentBlockParam_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ContentBlockParam
		wantErr bool
	}{
		{
			name:    "text block",
			jsonStr: `{"type": "text", "text": "Hello"}`,
			want:    ContentBlockParam{Text: &TextBlockParam{Text: "Hello", Type: "text"}},
		},
		{
			name:    "image block",
			jsonStr: `{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "abc123"}}`,
			want: ContentBlockParam{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{Base64: &Base64ImageSource{Type: "base64", MediaType: "image/png", Data: "abc123"}},
			}},
		},
		{
			name:    "document block",
			jsonStr: `{"type": "document", "source": {"type": "text", "data": "hello", "media_type": "text/plain"}, "context": "some context", "title": "doc title"}`,
			want: ContentBlockParam{Document: &DocumentBlockParam{
				Type:    "document",
				Source:  DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "hello"}},
				Context: "some context",
				Title:   "doc title",
			}},
		},
		{
			name:    "search result block",
			jsonStr: `{"type": "search_result", "source": "https://example.com", "title": "Example", "content": [{"type": "text", "text": "result text"}]}`,
			want: ContentBlockParam{SearchResult: &SearchResultBlockParam{
				Type:    "search_result",
				Source:  "https://example.com",
				Title:   "Example",
				Content: []TextBlockParam{{Type: "text", Text: "result text"}},
			}},
		},
		{
			name:    "thinking block",
			jsonStr: `{"type": "thinking", "thinking": "Let me think...", "signature": "sig123"}`,
			want: ContentBlockParam{Thinking: &ThinkingBlockParam{
				Type:      "thinking",
				Thinking:  "Let me think...",
				Signature: "sig123",
			}},
		},
		{
			name:    "redacted thinking block",
			jsonStr: `{"type": "redacted_thinking", "data": "redacted_data_here"}`,
			want: ContentBlockParam{RedactedThinking: &RedactedThinkingBlockParam{
				Type: "redacted_thinking",
				Data: "redacted_data_here",
			}},
		},
		{
			name:    "tool use block",
			jsonStr: `{"type": "tool_use", "id": "tu_123", "name": "my_tool", "input": {"query": "test"}}`,
			want: ContentBlockParam{ToolUse: &ToolUseBlockParam{
				Type:  "tool_use",
				ID:    "tu_123",
				Name:  "my_tool",
				Input: map[string]any{"query": "test"},
			}},
		},
		{
			name:    "tool result block",
			jsonStr: `{"type": "tool_result", "tool_use_id": "tu_123", "content": "result text", "is_error": false}`,
			want: ContentBlockParam{ToolResult: &ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "tu_123",
				Content:   &ToolResultContent{Text: "result text"},
			}},
		},
		{
			name:    "server tool use block",
			jsonStr: `{"type": "server_tool_use", "id": "stu_123", "name": "web_search", "input": {"query": "test"}}`,
			want: ContentBlockParam{ServerToolUse: &ServerToolUseBlockParam{
				Type:  "server_tool_use",
				ID:    "stu_123",
				Name:  "web_search",
				Input: map[string]any{"query": "test"},
			}},
		},
		{
			name:    "web search tool result block",
			jsonStr: `{"type": "web_search_tool_result", "tool_use_id": "stu_123", "content": [{"type": "web_search_result", "title": "Result", "url": "https://example.com", "encrypted_content": "enc123"}]}`,
			want: ContentBlockParam{WebSearchToolResult: &WebSearchToolResultBlockParam{
				Type:      "web_search_tool_result",
				ToolUseID: "stu_123",
				Content: WebSearchToolResultContent{
					Results: []WebSearchResult{
						{Type: "web_search_result", Title: "Result", URL: "https://example.com", EncryptedContent: "enc123"},
					},
				},
			}},
		},
		{
			name:    "missing type",
			jsonStr: `{"text": "Hello"}`,
			wantErr: true,
		},
		{
			name:    "unknown type",
			jsonStr: `{"type": "unknown", "text": "Hello"}`,
			want:    ContentBlockParam{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cbp ContentBlockParam
			err := cbp.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cbp)
		})
	}
}

func TestContentBlockParam_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		cbp     ContentBlockParam
		want    string
		wantErr bool
	}{
		{
			name: "text block",
			cbp:  ContentBlockParam{Text: &TextBlockParam{Text: "Hello", Type: "text"}},
			want: `{"text":"Hello","type":"text"}`,
		},
		{
			name: "image block",
			cbp: ContentBlockParam{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{Base64: &Base64ImageSource{Type: "base64", MediaType: "image/png", Data: "abc123"}},
			}},
			want: `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc123"}}`,
		},
		{
			name: "document block",
			cbp: ContentBlockParam{Document: &DocumentBlockParam{
				Type:    "document",
				Source:  DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "hello"}},
				Context: "some context",
				Title:   "doc title",
			}},
			want: `{"type":"document","source":{"type":"text","data":"hello","media_type":"text/plain"},"context":"some context","title":"doc title"}`,
		},
		{
			name: "search result block",
			cbp: ContentBlockParam{SearchResult: &SearchResultBlockParam{
				Type:    "search_result",
				Source:  "https://example.com",
				Title:   "Example",
				Content: []TextBlockParam{{Type: "text", Text: "result text"}},
			}},
			want: `{"type":"search_result","content":[{"type":"text","text":"result text"}],"source":"https://example.com","title":"Example"}`,
		},
		{
			name: "thinking block",
			cbp: ContentBlockParam{Thinking: &ThinkingBlockParam{
				Type:      "thinking",
				Thinking:  "Let me think...",
				Signature: "sig123",
			}},
			want: `{"type":"thinking","thinking":"Let me think...","signature":"sig123"}`,
		},
		{
			name: "redacted thinking block",
			cbp: ContentBlockParam{RedactedThinking: &RedactedThinkingBlockParam{
				Type: "redacted_thinking",
				Data: "redacted_data_here",
			}},
			want: `{"type":"redacted_thinking","data":"redacted_data_here"}`,
		},
		{
			name: "tool use block",
			cbp: ContentBlockParam{ToolUse: &ToolUseBlockParam{
				Type:  "tool_use",
				ID:    "tu_123",
				Name:  "my_tool",
				Input: map[string]any{"query": "test"},
			}},
			want: `{"type":"tool_use","id":"tu_123","name":"my_tool","input":{"query":"test"}}`,
		},
		{
			name: "tool result block",
			cbp: ContentBlockParam{ToolResult: &ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "tu_123",
				Content:   &ToolResultContent{Text: "result text"},
			}},
			want: `{"type":"tool_result","tool_use_id":"tu_123","content":"result text"}`,
		},
		{
			name: "server tool use block",
			cbp: ContentBlockParam{ServerToolUse: &ServerToolUseBlockParam{
				Type:  "server_tool_use",
				ID:    "stu_123",
				Name:  "web_search",
				Input: map[string]any{"query": "test"},
			}},
			want: `{"type":"server_tool_use","id":"stu_123","name":"web_search","input":{"query":"test"}}`,
		},
		{
			name: "web search tool result block",
			cbp: ContentBlockParam{WebSearchToolResult: &WebSearchToolResultBlockParam{
				Type:      "web_search_tool_result",
				ToolUseID: "stu_123",
				Content: WebSearchToolResultContent{
					Results: []WebSearchResult{
						{Type: "web_search_result", Title: "Example", URL: "https://example.com", EncryptedContent: "enc123"},
					},
				},
			}},
			want: `{"type":"web_search_tool_result","tool_use_id":"stu_123","content":[{"type":"web_search_result","title":"Example","url":"https://example.com","encrypted_content":"enc123"}]}`,
		},
		{
			name:    "empty block",
			cbp:     ContentBlockParam{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cbp.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestSystemPrompt_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    SystemPrompt
		wantErr bool
	}{
		{
			name:    "string prompt",
			jsonStr: `"You are a helpful assistant."`,
			want:    SystemPrompt{Text: "You are a helpful assistant."},
		},
		{
			name:    "array prompt",
			jsonStr: `[{"type": "text", "text": "You are a helpful assistant."}]`,
			want: SystemPrompt{Texts: []TextBlockParam{
				{Text: "You are a helpful assistant.", Type: "text"},
			}},
		},
		{
			name:    "invalid prompt",
			jsonStr: `12345`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sp SystemPrompt
			err := sp.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, sp)
		})
	}
}

func TestSystemPrompt_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		sp      SystemPrompt
		want    string
		wantErr bool
	}{
		{
			name: "string prompt",
			sp:   SystemPrompt{Text: "You are a helpful assistant."},
			want: `"You are a helpful assistant."`,
		},
		{
			name: "array prompt",
			sp: SystemPrompt{Texts: []TextBlockParam{
				{Text: "You are a helpful assistant.", Type: "text"},
			}},
			want: `[{"text":"You are a helpful assistant.","type":"text"}]`,
		},
		{
			name:    "empty prompt",
			sp:      SystemPrompt{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.sp.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestMessagesContentBlock_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    MessagesContentBlock
		wantErr bool
	}{
		{
			name:    "text block",
			jsonStr: `{"type": "text", "text": "Hello"}`,
			want:    MessagesContentBlock{Text: &TextBlock{Text: "Hello", Type: "text"}},
		},
		{
			name:    "missing type",
			jsonStr: `{"text": "Hello"}`,
			wantErr: true,
		},
		{
			name:    "unknown type",
			jsonStr: `{"type": "unknown"}`,
			want:    MessagesContentBlock{},
		},
		{
			name:    "tool use block",
			jsonStr: `{"type": "tool_use", "name": "my_tool", "input": {"query": "What is the weather today?"}}`,
			want: MessagesContentBlock{Tool: &ToolUseBlock{
				Type: "tool_use",
				Name: "my_tool",
				Input: map[string]any{
					"query": "What is the weather today?",
				},
			}},
		},
		{
			name:    "thinking block",
			jsonStr: `{"type": "thinking", "thinking": "Let me think about that."}`,
			want: MessagesContentBlock{Thinking: &ThinkingBlock{
				Type:     "thinking",
				Thinking: "Let me think about that.",
			}},
		},
		{
			name:    "redacted thinking block",
			jsonStr: `{"type": "redacted_thinking", "data": "redacted_data"}`,
			want: MessagesContentBlock{RedactedThinking: &RedactedThinkingBlock{
				Type: "redacted_thinking",
				Data: "redacted_data",
			}},
		},
		{
			name:    "server tool use block",
			jsonStr: `{"type": "server_tool_use", "id": "stu_1", "name": "web_search", "input": {"query": "test"}}`,
			want: MessagesContentBlock{ServerToolUse: &ServerToolUseBlock{
				Type:  "server_tool_use",
				ID:    "stu_1",
				Name:  "web_search",
				Input: map[string]any{"query": "test"},
			}},
		},
		{
			name:    "web search tool result block",
			jsonStr: `{"type": "web_search_tool_result", "tool_use_id": "stu_1", "content": [{"type": "web_search_result", "title": "Result", "url": "https://example.com", "encrypted_content": "enc456"}]}`,
			want: MessagesContentBlock{WebSearchToolResult: &WebSearchToolResultBlock{
				Type:      "web_search_tool_result",
				ToolUseID: "stu_1",
				Content: WebSearchToolResultContent{
					Results: []WebSearchResult{
						{Type: "web_search_result", Title: "Result", URL: "https://example.com", EncryptedContent: "enc456"},
					},
				},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mcb MessagesContentBlock
			err := mcb.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, mcb)

			marshaled, err := mcb.MarshalJSON()
			// This is mostly for coverage. marshaling is not currently used in the main code.
			if err == nil {
				var unmarshaled MessagesContentBlock
				err = unmarshaled.UnmarshalJSON(marshaled)
				require.NoError(t, err)
				require.Equal(t, mcb, unmarshaled)
			}
		})
	}
}

func TestMessagesContentBlock_MarshalJSON(t *testing.T) {
	t.Run("empty block returns error", func(t *testing.T) {
		mcb := MessagesContentBlock{}
		_, err := mcb.MarshalJSON()
		require.Error(t, err)
	})
}

func TestToolUnion_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ToolUnion
		wantErr bool
	}{
		{
			name:    "custom tool",
			jsonStr: `{"type":"custom","name":"my_tool","input_schema":{"type":"object"}}`,
			want: ToolUnion{Tool: &Tool{
				Type: "custom", Name: "my_tool",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		{
			name:    "bash tool",
			jsonStr: `{"type":"bash_20250124","name":"bash"}`,
			want:    ToolUnion{BashTool: &BashTool{Type: "bash_20250124", Name: "bash"}},
		},
		{
			name:    "text editor tool 20250124",
			jsonStr: `{"type":"text_editor_20250124","name":"str_replace_editor"}`,
			want:    ToolUnion{TextEditorTool20250124: &TextEditorTool20250124{Type: "text_editor_20250124", Name: "str_replace_editor"}},
		},
		{
			name:    "text editor tool 20250429",
			jsonStr: `{"type":"text_editor_20250429","name":"str_replace_based_edit_tool"}`,
			want:    ToolUnion{TextEditorTool20250429: &TextEditorTool20250429{Type: "text_editor_20250429", Name: "str_replace_based_edit_tool"}},
		},
		{
			name:    "text editor tool 20250728",
			jsonStr: `{"type":"text_editor_20250728","name":"str_replace_based_edit_tool"}`,
			want:    ToolUnion{TextEditorTool20250728: &TextEditorTool20250728{Type: "text_editor_20250728", Name: "str_replace_based_edit_tool"}},
		},
		{
			name:    "web search tool",
			jsonStr: `{"type":"web_search_20250305","name":"web_search"}`,
			want:    ToolUnion{WebSearchTool: &WebSearchTool{Type: "web_search_20250305", Name: "web_search"}},
		},
		{
			name:    "missing type defaults to custom",
			jsonStr: `{"name":"my_tool","description":"A tool","input_schema":{"type":"object"}}`,
			want: ToolUnion{Tool: &Tool{
				Name: "my_tool", Description: "A tool",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		{
			name:    "empty type defaults to custom",
			jsonStr: `{"type":"","name":"my_tool","input_schema":{"type":"object"}}`,
			want: ToolUnion{Tool: &Tool{
				Type: "", Name: "my_tool",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"future_tool","name":"x"}`,
			want:    ToolUnion{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tu ToolUnion
			err := tu.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, tu)
		})
	}
}

func TestToolUnion_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		tu      ToolUnion
		want    string
		wantErr bool
	}{
		{
			name: "custom tool",
			tu:   ToolUnion{Tool: &Tool{Type: "custom", Name: "t", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			want: `{"type":"custom","name":"t","input_schema":{"type":"object"}}`,
		},
		{
			name: "bash tool",
			tu:   ToolUnion{BashTool: &BashTool{Type: "bash_20250124", Name: "bash"}},
			want: `{"type":"bash_20250124","name":"bash"}`,
		},
		{
			name: "text editor 20250124",
			tu:   ToolUnion{TextEditorTool20250124: &TextEditorTool20250124{Type: "text_editor_20250124", Name: "str_replace_editor"}},
			want: `{"type":"text_editor_20250124","name":"str_replace_editor"}`,
		},
		{
			name: "text editor 20250429",
			tu:   ToolUnion{TextEditorTool20250429: &TextEditorTool20250429{Type: "text_editor_20250429", Name: "n"}},
			want: `{"type":"text_editor_20250429","name":"n"}`,
		},
		{
			name: "text editor 20250728",
			tu:   ToolUnion{TextEditorTool20250728: &TextEditorTool20250728{Type: "text_editor_20250728", Name: "n"}},
			want: `{"type":"text_editor_20250728","name":"n"}`,
		},
		{
			name: "web search tool",
			tu:   ToolUnion{WebSearchTool: &WebSearchTool{Type: "web_search_20250305", Name: "web_search"}},
			want: `{"type":"web_search_20250305","name":"web_search"}`,
		},
		{
			name:    "empty tool union",
			tu:      ToolUnion{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.tu.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestToolChoice_UnmarshalJSON(t *testing.T) {
	boolTrue := true
	tests := []struct {
		name    string
		jsonStr string
		want    ToolChoice
		wantErr bool
	}{
		{
			name:    "auto",
			jsonStr: `{"type":"auto","disable_parallel_tool_use":true}`,
			want:    ToolChoice{Auto: &ToolChoiceAuto{Type: "auto", DisableParallelToolUse: &boolTrue}},
		},
		{
			name:    "any",
			jsonStr: `{"type":"any"}`,
			want:    ToolChoice{Any: &ToolChoiceAny{Type: "any"}},
		},
		{
			name:    "tool",
			jsonStr: `{"type":"tool","name":"my_tool"}`,
			want:    ToolChoice{Tool: &ToolChoiceTool{Type: "tool", Name: "my_tool"}},
		},
		{
			name:    "none",
			jsonStr: `{"type":"none"}`,
			want:    ToolChoice{None: &ToolChoiceNone{Type: "none"}},
		},
		{
			name:    "missing type",
			jsonStr: `{"name":"x"}`,
			wantErr: true,
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"future"}`,
			want:    ToolChoice{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tc ToolChoice
			err := tc.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, tc)
		})
	}
}

func TestToolChoice_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		tc      ToolChoice
		want    string
		wantErr bool
	}{
		{
			name: "auto",
			tc:   ToolChoice{Auto: &ToolChoiceAuto{Type: "auto"}},
			want: `{"type":"auto"}`,
		},
		{
			name: "any",
			tc:   ToolChoice{Any: &ToolChoiceAny{Type: "any"}},
			want: `{"type":"any"}`,
		},
		{
			name: "tool",
			tc:   ToolChoice{Tool: &ToolChoiceTool{Type: "tool", Name: "my_tool"}},
			want: `{"type":"tool","name":"my_tool"}`,
		},
		{
			name: "none",
			tc:   ToolChoice{None: &ToolChoiceNone{Type: "none"}},
			want: `{"type":"none"}`,
		},
		{
			name:    "empty tool choice",
			tc:      ToolChoice{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.tc.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestThinking_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    Thinking
		wantErr bool
	}{
		{
			name:    "enabled",
			jsonStr: `{"type":"enabled","budget_tokens":2048}`,
			want:    Thinking{Enabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 2048}},
		},
		{
			name:    "disabled",
			jsonStr: `{"type":"disabled"}`,
			want:    Thinking{Disabled: &ThinkingDisabled{Type: "disabled"}},
		},
		{
			name:    "adaptive",
			jsonStr: `{"type":"adaptive"}`,
			want:    Thinking{Adaptive: &ThinkingAdaptive{Type: "adaptive"}},
		},
		{
			name:    "enabled with display",
			jsonStr: `{"type":"enabled","budget_tokens":2048,"display":"omitted"}`,
			want:    Thinking{Enabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 2048, Display: "omitted"}},
		},
		{
			name:    "adaptive with display",
			jsonStr: `{"type":"adaptive","display":"summarized"}`,
			want:    Thinking{Adaptive: &ThinkingAdaptive{Type: "adaptive", Display: "summarized"}},
		},
		{
			name:    "missing type",
			jsonStr: `{"budget_tokens":1024}`,
			wantErr: true,
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"future"}`,
			want:    Thinking{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var th Thinking
			err := th.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, th)
		})
	}
}

func TestThinking_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		th      Thinking
		want    string
		wantErr bool
	}{
		{
			name: "enabled",
			th:   Thinking{Enabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 2048}},
			want: `{"type":"enabled","budget_tokens":2048}`,
		},
		{
			name: "disabled",
			th:   Thinking{Disabled: &ThinkingDisabled{Type: "disabled"}},
			want: `{"type":"disabled"}`,
		},
		{
			name: "adaptive",
			th:   Thinking{Adaptive: &ThinkingAdaptive{Type: "adaptive"}},
			want: `{"type":"adaptive"}`,
		},
		{
			name: "enabled with display",
			th:   Thinking{Enabled: &ThinkingEnabled{Type: "enabled", BudgetTokens: 2048, Display: "omitted"}},
			want: `{"type":"enabled","budget_tokens":2048,"display":"omitted"}`,
		},
		{
			name: "adaptive with display",
			th:   Thinking{Adaptive: &ThinkingAdaptive{Type: "adaptive", Display: "summarized"}},
			want: `{"type":"adaptive","display":"summarized"}`,
		},
		{
			name:    "empty thinking",
			th:      Thinking{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.th.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestContentBlockParam_UnmarshalJSON_ErrorPaths(t *testing.T) {
	// Each case has a valid "type" but invalid JSON for that type's struct,
	// triggering the unmarshal error path for each content block variant.
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "text invalid", jsonStr: `{"type":"text","text":123}`},
		{name: "image invalid", jsonStr: `{"type":"image","source":null,"cache_control":}`},
		{name: "document invalid", jsonStr: `{"type":"document","source":null,"context":123}`},
		{name: "search_result invalid", jsonStr: `{"type":"search_result","content":"not_array"}`},
		{name: "thinking invalid", jsonStr: `{"type":"thinking","thinking":123}`},
		{name: "redacted_thinking invalid", jsonStr: `{"type":"redacted_thinking","data":123}`},
		{name: "tool_use invalid", jsonStr: `{"type":"tool_use","input":"not_object"}`},
		{name: "tool_result invalid", jsonStr: `{"type":"tool_result","is_error":"not_bool"}`},
		{name: "server_tool_use invalid", jsonStr: `{"type":"server_tool_use","input":"not_object"}`},
		{name: "web_search_tool_result invalid", jsonStr: `{"type":"web_search_tool_result","tool_use_id":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cbp ContentBlockParam
			err := cbp.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestMessagesContentBlock_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "tool_use invalid", jsonStr: `{"type":"tool_use","input":"bad"}`},
		{name: "thinking invalid", jsonStr: `{"type":"thinking","thinking":123}`},
		{name: "redacted_thinking invalid", jsonStr: `{"type":"redacted_thinking","data":123}`},
		{name: "server_tool_use invalid", jsonStr: `{"type":"server_tool_use","input":"bad"}`},
		{name: "web_search_tool_result invalid", jsonStr: `{"type":"web_search_tool_result","tool_use_id":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mcb MessagesContentBlock
			err := mcb.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestToolUnion_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "custom invalid", jsonStr: `{"type":"custom","name":123}`},
		{name: "bash invalid", jsonStr: `{"type":"bash_20250124","name":123}`},
		{name: "text_editor_20250124 invalid", jsonStr: `{"type":"text_editor_20250124","name":123}`},
		{name: "text_editor_20250429 invalid", jsonStr: `{"type":"text_editor_20250429","name":123}`},
		{name: "text_editor_20250728 invalid", jsonStr: `{"type":"text_editor_20250728","name":123}`},
		{name: "web_search invalid", jsonStr: `{"type":"web_search_20250305","name":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tu ToolUnion
			err := tu.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestToolChoice_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "auto invalid", jsonStr: `{"type":"auto","disable_parallel_tool_use":"bad"}`},
		{name: "any invalid", jsonStr: `{"type":"any","disable_parallel_tool_use":"bad"}`},
		{name: "tool invalid", jsonStr: `{"type":"tool","name":123}`},
		{name: "none invalid", jsonStr: `{"type":"none","type":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tc ToolChoice
			err := tc.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestThinking_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "enabled invalid", jsonStr: `{"type":"enabled","budget_tokens":"bad"}`},
		{name: "disabled invalid", jsonStr: `{"type":"disabled","type":123}`},
		{name: "adaptive invalid", jsonStr: `{"type":"adaptive","type":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var th Thinking
			err := th.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestMessagesStreamChunk_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "message_delta invalid", jsonStr: `{"type":"message_delta","usage":"bad"}`},
		{name: "message_stop invalid", jsonStr: `{"type":"message_stop","type":123}`},
		{name: "content_block_start invalid", jsonStr: `{"type":"content_block_start","content_block":"bad"}`},
		{name: "content_block_delta invalid", jsonStr: `{"type":"content_block_delta","delta":"bad"}`},
		{name: "content_block_stop invalid", jsonStr: `{"type":"content_block_stop","index":"bad"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msc MessagesStreamChunk
			err := msc.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestCacheControl_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    CacheControl
		wantErr bool
	}{
		{
			name:    "ephemeral no TTL",
			jsonStr: `{"type":"ephemeral"}`,
			want:    CacheControl{Ephemeral: &CacheControlEphemeral{Type: "ephemeral"}},
		},
		{
			name:    "ephemeral with 5m TTL",
			jsonStr: `{"type":"ephemeral","ttl":"5m"}`,
			want: CacheControl{Ephemeral: &CacheControlEphemeral{
				Type: "ephemeral",
				TTL:  strPtr("5m"),
			}},
		},
		{
			name:    "ephemeral with 1h TTL",
			jsonStr: `{"type":"ephemeral","ttl":"1h"}`,
			want: CacheControl{Ephemeral: &CacheControlEphemeral{
				Type: "ephemeral",
				TTL:  strPtr("1h"),
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"persistent"}`,
			want:    CacheControl{},
		},
		{
			name:    "missing type",
			jsonStr: `{"ttl":"5m"}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			jsonStr: `{bad}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cc CacheControl
			err := cc.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cc)
		})
	}
}

func TestCacheControl_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		cc      CacheControl
		want    string
		wantErr bool
	}{
		{
			name: "ephemeral no TTL",
			cc:   CacheControl{Ephemeral: &CacheControlEphemeral{Type: "ephemeral"}},
			want: `{"type":"ephemeral"}`,
		},
		{
			name: "ephemeral with TTL",
			cc:   CacheControl{Ephemeral: &CacheControlEphemeral{Type: "ephemeral", TTL: strPtr("1h")}},
			want: `{"type":"ephemeral","ttl":"1h"}`,
		},
		{
			name:    "empty cache control",
			cc:      CacheControl{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cc.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestImageSource_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ImageSource
		wantErr bool
	}{
		{
			name:    "base64 jpeg",
			jsonStr: `{"type":"base64","media_type":"image/jpeg","data":"abc123"}`,
			want: ImageSource{Base64: &Base64ImageSource{
				Type: "base64", MediaType: "image/jpeg", Data: "abc123",
			}},
		},
		{
			name:    "base64 png",
			jsonStr: `{"type":"base64","media_type":"image/png","data":"xyz789"}`,
			want: ImageSource{Base64: &Base64ImageSource{
				Type: "base64", MediaType: "image/png", Data: "xyz789",
			}},
		},
		{
			name:    "url source",
			jsonStr: `{"type":"url","url":"https://example.com/image.png"}`,
			want: ImageSource{URL: &URLImageSource{
				Type: "url", URL: "https://example.com/image.png",
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"file","file_id":"file_123"}`,
			want:    ImageSource{},
		},
		{
			name:    "missing type",
			jsonStr: `{"media_type":"image/png","data":"abc"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var is ImageSource
			err := is.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, is)
		})
	}
}

func TestImageSource_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		is      ImageSource
		want    string
		wantErr bool
	}{
		{
			name: "base64",
			is:   ImageSource{Base64: &Base64ImageSource{Type: "base64", MediaType: "image/gif", Data: "gif_data"}},
			want: `{"type":"base64","media_type":"image/gif","data":"gif_data"}`,
		},
		{
			name: "url",
			is:   ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/img.webp"}},
			want: `{"type":"url","url":"https://example.com/img.webp"}`,
		},
		{
			name:    "empty image source",
			is:      ImageSource{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.is.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestImageSource_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "base64 invalid", jsonStr: `{"type":"base64","data":123}`},
		{name: "url invalid", jsonStr: `{"type":"url","url":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var is ImageSource
			err := is.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestDocumentSource_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    DocumentSource
		wantErr bool
	}{
		{
			name:    "base64 PDF",
			jsonStr: `{"type":"base64","media_type":"application/pdf","data":"pdf_data"}`,
			want: DocumentSource{Base64PDF: &Base64PDFSource{
				Type: "base64", MediaType: "application/pdf", Data: "pdf_data",
			}},
		},
		{
			name:    "plain text",
			jsonStr: `{"type":"text","media_type":"text/plain","data":"hello world"}`,
			want: DocumentSource{PlainText: &PlainTextSource{
				Type: "text", MediaType: "text/plain", Data: "hello world",
			}},
		},
		{
			name:    "URL PDF",
			jsonStr: `{"type":"url","url":"https://example.com/doc.pdf"}`,
			want: DocumentSource{URL: &URLPDFSource{
				Type: "url", URL: "https://example.com/doc.pdf",
			}},
		},
		{
			name:    "content block - string content",
			jsonStr: `{"type":"content","content":"some text content"}`,
			want: DocumentSource{ContentBlock: &ContentBlockSource{
				Type:    "content",
				Content: ContentBlockSourceContent{Text: "some text content"},
			}},
		},
		{
			name:    "content block - array content",
			jsonStr: `{"type":"content","content":[{"type":"text","text":"part one"}]}`,
			want: DocumentSource{ContentBlock: &ContentBlockSource{
				Type: "content",
				Content: ContentBlockSourceContent{
					Array: []ContentBlockSourceItem{
						{Text: &TextBlockParam{Type: "text", Text: "part one"}},
					},
				},
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"file","file_id":"f_123"}`,
			want:    DocumentSource{},
		},
		{
			name:    "missing type",
			jsonStr: `{"data":"pdf_data"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ds DocumentSource
			err := ds.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, ds)
		})
	}
}

func TestDocumentSource_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		ds      DocumentSource
		want    string
		wantErr bool
	}{
		{
			name: "base64 PDF",
			ds:   DocumentSource{Base64PDF: &Base64PDFSource{Type: "base64", MediaType: "application/pdf", Data: "pdf_data"}},
			want: `{"type":"base64","media_type":"application/pdf","data":"pdf_data"}`,
		},
		{
			name: "plain text",
			ds:   DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "hello"}},
			want: `{"type":"text","media_type":"text/plain","data":"hello"}`,
		},
		{
			name: "URL PDF",
			ds:   DocumentSource{URL: &URLPDFSource{Type: "url", URL: "https://example.com/doc.pdf"}},
			want: `{"type":"url","url":"https://example.com/doc.pdf"}`,
		},
		{
			name: "content block with string",
			ds: DocumentSource{ContentBlock: &ContentBlockSource{
				Type:    "content",
				Content: ContentBlockSourceContent{Text: "text content"},
			}},
			want: `{"type":"content","content":"text content"}`,
		},
		{
			name: "content block with array",
			ds: DocumentSource{ContentBlock: &ContentBlockSource{
				Type: "content",
				Content: ContentBlockSourceContent{
					Array: []ContentBlockSourceItem{
						{Text: &TextBlockParam{Type: "text", Text: "hello"}},
					},
				},
			}},
			want: `{"type":"content","content":[{"text":"hello","type":"text"}]}`,
		},
		{
			name:    "empty document source",
			ds:      DocumentSource{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.ds.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestDocumentSource_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "base64 invalid", jsonStr: `{"type":"base64","data":123}`},
		{name: "text invalid", jsonStr: `{"type":"text","data":123}`},
		{name: "url invalid", jsonStr: `{"type":"url","url":123}`},
		{name: "content invalid", jsonStr: `{"type":"content","content":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ds DocumentSource
			err := ds.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestContentBlockSourceContent_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ContentBlockSourceContent
		wantErr bool
	}{
		{
			name:    "string content",
			jsonStr: `"hello world"`,
			want:    ContentBlockSourceContent{Text: "hello world"},
		},
		{
			name:    "array with text block",
			jsonStr: `[{"type":"text","text":"block one"}]`,
			want: ContentBlockSourceContent{
				Array: []ContentBlockSourceItem{
					{Text: &TextBlockParam{Type: "text", Text: "block one"}},
				},
			},
		},
		{
			name:    "array with image block",
			jsonStr: `[{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}]`,
			want: ContentBlockSourceContent{
				Array: []ContentBlockSourceItem{
					{Image: &ImageBlockParam{
						Type:   "image",
						Source: ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/img.png"}},
					}},
				},
			},
		},
		{
			name:    "invalid content",
			jsonStr: `12345`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c ContentBlockSourceContent
			err := c.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, c)
		})
	}
}

func TestContentBlockSourceContent_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		c       ContentBlockSourceContent
		want    string
		wantErr bool
	}{
		{
			name: "string content",
			c:    ContentBlockSourceContent{Text: "hello world"},
			want: `"hello world"`,
		},
		{
			name: "array content",
			c: ContentBlockSourceContent{
				Array: []ContentBlockSourceItem{
					{Text: &TextBlockParam{Type: "text", Text: "item"}},
				},
			},
			want: `[{"text":"item","type":"text"}]`,
		},
		{
			name:    "empty content",
			c:       ContentBlockSourceContent{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.c.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestContentBlockSourceItem_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ContentBlockSourceItem
		wantErr bool
	}{
		{
			name:    "text item",
			jsonStr: `{"type":"text","text":"hello"}`,
			want:    ContentBlockSourceItem{Text: &TextBlockParam{Type: "text", Text: "hello"}},
		},
		{
			name:    "image item",
			jsonStr: `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}`,
			want: ContentBlockSourceItem{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{Base64: &Base64ImageSource{Type: "base64", MediaType: "image/png", Data: "abc"}},
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"document","source":{"type":"url","url":"https://example.com/doc.pdf"}}`,
			want:    ContentBlockSourceItem{},
		},
		{
			name:    "missing type",
			jsonStr: `{"text":"hello"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item ContentBlockSourceItem
			err := item.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, item)
		})
	}
}

func TestContentBlockSourceItem_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		item    ContentBlockSourceItem
		want    string
		wantErr bool
	}{
		{
			name: "text item",
			item: ContentBlockSourceItem{Text: &TextBlockParam{Type: "text", Text: "hello"}},
			want: `{"text":"hello","type":"text"}`,
		},
		{
			name: "image item",
			item: ContentBlockSourceItem{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/img.png"}},
			}},
			want: `{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}`,
		},
		{
			name:    "empty item",
			item:    ContentBlockSourceItem{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.item.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestContentBlockSourceItem_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "text invalid", jsonStr: `{"type":"text","text":123}`},
		{name: "image invalid", jsonStr: `{"type":"image","source":{"type":"url","url":123}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item ContentBlockSourceItem
			err := item.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestTextCitation_UnmarshalJSON(t *testing.T) {
	docTitle := "My Document"
	tests := []struct {
		name    string
		jsonStr string
		want    TextCitation
		wantErr bool
	}{
		{
			name:    "char_location",
			jsonStr: `{"type":"char_location","cited_text":"exact quote","document_index":0,"document_title":"My Document","start_char_index":10,"end_char_index":21}`,
			want: TextCitation{CharLocation: &CitationCharLocation{
				Type: "char_location", CitedText: "exact quote", DocumentIndex: 0,
				DocumentTitle: &docTitle, StartCharIndex: 10, EndCharIndex: 21,
			}},
		},
		{
			name:    "char_location no title",
			jsonStr: `{"type":"char_location","cited_text":"quote","document_index":1,"start_char_index":5,"end_char_index":10}`,
			want: TextCitation{CharLocation: &CitationCharLocation{
				Type: "char_location", CitedText: "quote", DocumentIndex: 1,
				StartCharIndex: 5, EndCharIndex: 10,
			}},
		},
		{
			name:    "page_location",
			jsonStr: `{"type":"page_location","cited_text":"page text","document_index":2,"document_title":"My Document","start_page_number":3,"end_page_number":5}`,
			want: TextCitation{PageLocation: &CitationPageLocation{
				Type: "page_location", CitedText: "page text", DocumentIndex: 2,
				DocumentTitle: &docTitle, StartPageNumber: 3, EndPageNumber: 5,
			}},
		},
		{
			name:    "content_block_location",
			jsonStr: `{"type":"content_block_location","cited_text":"block text","document_index":0,"document_title":"My Document","start_block_index":1,"end_block_index":3}`,
			want: TextCitation{ContentBlockLocation: &CitationContentBlockLocation{
				Type: "content_block_location", CitedText: "block text", DocumentIndex: 0,
				DocumentTitle: &docTitle, StartBlockIndex: 1, EndBlockIndex: 3,
			}},
		},
		{
			name:    "web_search_result_location",
			jsonStr: `{"type":"web_search_result_location","cited_text":"web quote","encrypted_index":"enc_abc","title":"Example Page","url":"https://example.com"}`,
			want: TextCitation{WebSearchResultLocation: &CitationWebSearchResultLocation{
				Type: "web_search_result_location", CitedText: "web quote",
				EncryptedIndex: "enc_abc", Title: "Example Page", URL: "https://example.com",
			}},
		},
		{
			name:    "search_result_location",
			jsonStr: `{"type":"search_result_location","cited_text":"search text","title":"Result Title","source":"https://source.com","start_block_index":0,"end_block_index":2,"search_result_index":1}`,
			want: TextCitation{SearchResultLocation: &CitationSearchResultLocation{
				Type: "search_result_location", CitedText: "search text", Title: "Result Title",
				Source: "https://source.com", StartBlockIndex: 0, EndBlockIndex: 2, SearchResultIndex: 1,
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"future_citation","data":"x"}`,
			want:    TextCitation{},
		},
		{
			name:    "missing type",
			jsonStr: `{"cited_text":"quote"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c TextCitation
			err := c.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, c)
		})
	}
}

func TestTextCitation_MarshalJSON(t *testing.T) {
	docTitle := "My Document"
	tests := []struct {
		name    string
		c       TextCitation
		want    string
		wantErr bool
	}{
		{
			name: "char_location",
			c: TextCitation{CharLocation: &CitationCharLocation{
				Type: "char_location", CitedText: "quote", DocumentIndex: 0,
				DocumentTitle: &docTitle, StartCharIndex: 5, EndCharIndex: 10,
			}},
			want: `{"type":"char_location","cited_text":"quote","document_index":0,"document_title":"My Document","start_char_index":5,"end_char_index":10}`,
		},
		{
			name: "page_location",
			c: TextCitation{PageLocation: &CitationPageLocation{
				Type: "page_location", CitedText: "page text", DocumentIndex: 1,
				StartPageNumber: 2, EndPageNumber: 4,
			}},
			want: `{"type":"page_location","cited_text":"page text","document_index":1,"start_page_number":2,"end_page_number":4}`,
		},
		{
			name: "content_block_location",
			c: TextCitation{ContentBlockLocation: &CitationContentBlockLocation{
				Type: "content_block_location", CitedText: "block", DocumentIndex: 0,
				StartBlockIndex: 0, EndBlockIndex: 1,
			}},
			want: `{"type":"content_block_location","cited_text":"block","document_index":0,"start_block_index":0,"end_block_index":1}`,
		},
		{
			name: "web_search_result_location",
			c: TextCitation{WebSearchResultLocation: &CitationWebSearchResultLocation{
				Type: "web_search_result_location", CitedText: "web text",
				EncryptedIndex: "enc_xyz", URL: "https://example.com",
			}},
			want: `{"type":"web_search_result_location","cited_text":"web text","encrypted_index":"enc_xyz","url":"https://example.com"}`,
		},
		{
			name: "search_result_location",
			c: TextCitation{SearchResultLocation: &CitationSearchResultLocation{
				Type: "search_result_location", CitedText: "search text",
				Source: "https://source.com", StartBlockIndex: 0, EndBlockIndex: 1, SearchResultIndex: 2,
			}},
			want: `{"type":"search_result_location","cited_text":"search text","source":"https://source.com","start_block_index":0,"end_block_index":1,"search_result_index":2}`,
		},
		{
			name:    "empty citation",
			c:       TextCitation{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.c.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestTextCitation_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "char_location invalid", jsonStr: `{"type":"char_location","document_index":"bad"}`},
		{name: "page_location invalid", jsonStr: `{"type":"page_location","document_index":"bad"}`},
		{name: "content_block_location invalid", jsonStr: `{"type":"content_block_location","document_index":"bad"}`},
		{name: "web_search_result_location invalid", jsonStr: `{"type":"web_search_result_location","cited_text":123}`},
		{name: "search_result_location invalid", jsonStr: `{"type":"search_result_location","cited_text":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c TextCitation
			err := c.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestWebSearchToolResultContent_UnmarshalJSON(t *testing.T) {
	pageAge := "2 days ago"
	tests := []struct {
		name    string
		jsonStr string
		want    WebSearchToolResultContent
		wantErr bool
	}{
		{
			name:    "array of results",
			jsonStr: `[{"type":"web_search_result","title":"Example","url":"https://example.com","encrypted_content":"enc123"}]`,
			want: WebSearchToolResultContent{
				Results: []WebSearchResult{
					{Type: "web_search_result", Title: "Example", URL: "https://example.com", EncryptedContent: "enc123"},
				},
			},
		},
		{
			name:    "result with page age",
			jsonStr: `[{"type":"web_search_result","title":"Old Page","url":"https://old.com","encrypted_content":"enc456","page_age":"2 days ago"}]`,
			want: WebSearchToolResultContent{
				Results: []WebSearchResult{
					{Type: "web_search_result", Title: "Old Page", URL: "https://old.com", EncryptedContent: "enc456", PageAge: &pageAge},
				},
			},
		},
		{
			name:    "multiple results",
			jsonStr: `[{"type":"web_search_result","title":"A","url":"https://a.com","encrypted_content":"enc_a"},{"type":"web_search_result","title":"B","url":"https://b.com","encrypted_content":"enc_b"}]`,
			want: WebSearchToolResultContent{
				Results: []WebSearchResult{
					{Type: "web_search_result", Title: "A", URL: "https://a.com", EncryptedContent: "enc_a"},
					{Type: "web_search_result", Title: "B", URL: "https://b.com", EncryptedContent: "enc_b"},
				},
			},
		},
		{
			name:    "error result",
			jsonStr: `{"type":"web_search_tool_result_error","error_code":"unavailable"}`,
			want: WebSearchToolResultContent{
				Error: &WebSearchToolResultError{Type: "web_search_tool_result_error", ErrorCode: "unavailable"},
			},
		},
		{
			name:    "max_uses_exceeded error",
			jsonStr: `{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}`,
			want: WebSearchToolResultContent{
				Error: &WebSearchToolResultError{Type: "web_search_tool_result_error", ErrorCode: "max_uses_exceeded"},
			},
		},
		{
			name:    "empty array",
			jsonStr: `[]`,
			want:    WebSearchToolResultContent{Results: []WebSearchResult{}},
		},
		{
			name:    "invalid content - plain string",
			jsonStr: `"some string"`,
			wantErr: true,
		},
		{
			name:    "invalid content - number",
			jsonStr: `42`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w WebSearchToolResultContent
			err := w.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, w)
		})
	}
}

func TestWebSearchToolResultContent_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		w       WebSearchToolResultContent
		want    string
		wantErr bool
	}{
		{
			name: "results",
			w: WebSearchToolResultContent{
				Results: []WebSearchResult{
					{Type: "web_search_result", Title: "Example", URL: "https://example.com", EncryptedContent: "enc123"},
				},
			},
			want: `[{"type":"web_search_result","title":"Example","url":"https://example.com","encrypted_content":"enc123"}]`,
		},
		{
			name: "error",
			w: WebSearchToolResultContent{
				Error: &WebSearchToolResultError{Type: "web_search_tool_result_error", ErrorCode: "query_too_long"},
			},
			want: `{"type":"web_search_tool_result_error","error_code":"query_too_long"}`,
		},
		{
			name: "empty results array",
			w:    WebSearchToolResultContent{Results: []WebSearchResult{}},
			want: `[]`,
		},
		{
			name:    "empty content",
			w:       WebSearchToolResultContent{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.w.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestCacheControl_InTextBlockParam(t *testing.T) {
	ttl1h := "1h"
	jsonStr := `{"type":"text","text":"Hello","cache_control":{"type":"ephemeral","ttl":"1h"},"citations":[{"type":"char_location","cited_text":"quote","document_index":0,"start_char_index":5,"end_char_index":10}]}`
	var param TextBlockParam
	err := json.Unmarshal([]byte(jsonStr), &param)
	require.NoError(t, err)
	require.Equal(t, TextBlockParam{
		Type: "text",
		Text: "Hello",
		CacheControl: &CacheControl{Ephemeral: &CacheControlEphemeral{
			Type: "ephemeral",
			TTL:  &ttl1h,
		}},
		Citations: []TextCitation{
			{CharLocation: &CitationCharLocation{
				Type: "char_location", CitedText: "quote", DocumentIndex: 0,
				StartCharIndex: 5, EndCharIndex: 10,
			}},
		},
	}, param)

	// Round-trip marshal.
	data, err := json.Marshal(param)
	require.NoError(t, err)
	require.JSONEq(t, jsonStr, string(data))
}

func TestCacheControl_InDocumentBlockParam(t *testing.T) {
	enabled := true
	jsonStr := `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"doc content"},"cache_control":{"type":"ephemeral"},"citations":{"enabled":true},"title":"My Doc","context":"some context"}`
	var param DocumentBlockParam
	err := json.Unmarshal([]byte(jsonStr), &param)
	require.NoError(t, err)
	require.Equal(t, DocumentBlockParam{
		Type:         "document",
		Source:       DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "doc content"}},
		CacheControl: &CacheControl{Ephemeral: &CacheControlEphemeral{Type: "ephemeral"}},
		Citations:    &CitationsConfigParam{Enabled: &enabled},
		Title:        "My Doc",
		Context:      "some context",
	}, param)

	// Round-trip marshal.
	data, err := json.Marshal(param)
	require.NoError(t, err)
	require.JSONEq(t, jsonStr, string(data))
}

func TestTextBlock_WithCitations(t *testing.T) {
	docTitle := "Source Doc"
	jsonStr := `{"type":"text","text":"Response with citation","citations":[{"type":"char_location","cited_text":"cited","document_index":0,"document_title":"Source Doc","start_char_index":0,"end_char_index":5},{"type":"web_search_result_location","cited_text":"web cited","encrypted_index":"enc_123","url":"https://example.com"}]}`
	var block TextBlock
	err := json.Unmarshal([]byte(jsonStr), &block)
	require.NoError(t, err)
	require.Equal(t, TextBlock{
		Type: "text",
		Text: "Response with citation",
		Citations: []TextCitation{
			{CharLocation: &CitationCharLocation{
				Type: "char_location", CitedText: "cited", DocumentIndex: 0,
				DocumentTitle: &docTitle, StartCharIndex: 0, EndCharIndex: 5,
			}},
			{WebSearchResultLocation: &CitationWebSearchResultLocation{
				Type: "web_search_result_location", CitedText: "web cited",
				EncryptedIndex: "enc_123", URL: "https://example.com",
			}},
		},
	}, block)
}

func TestImageBlockParam_WithCacheControl(t *testing.T) {
	jsonStr := `{"type":"image","source":{"type":"url","url":"https://example.com/img.png"},"cache_control":{"type":"ephemeral"}}`
	var param ImageBlockParam
	err := json.Unmarshal([]byte(jsonStr), &param)
	require.NoError(t, err)
	require.Equal(t, ImageBlockParam{
		Type:         "image",
		Source:       ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/img.png"}},
		CacheControl: &CacheControl{Ephemeral: &CacheControlEphemeral{Type: "ephemeral"}},
	}, param)
}

func TestWebSearchToolResultBlockParam_WithError(t *testing.T) {
	jsonStr := `{"type":"web_search_tool_result","tool_use_id":"ws_123","content":{"type":"web_search_tool_result_error","error_code":"too_many_requests"}}`
	var param WebSearchToolResultBlockParam
	err := json.Unmarshal([]byte(jsonStr), &param)
	require.NoError(t, err)
	require.Equal(t, WebSearchToolResultBlockParam{
		Type:      "web_search_tool_result",
		ToolUseID: "ws_123",
		Content: WebSearchToolResultContent{
			Error: &WebSearchToolResultError{
				Type:      "web_search_tool_result_error",
				ErrorCode: "too_many_requests",
			},
		},
	}, param)
}

func TestDocumentBlockSource_ContentBlockSource(t *testing.T) {
	jsonStr := `{"type":"content","content":[{"type":"text","text":"text part"},{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"webp_data"}}]}`
	var src ContentBlockSource
	err := json.Unmarshal([]byte(jsonStr), &src)
	require.NoError(t, err)
	require.Equal(t, ContentBlockSource{
		Type: "content",
		Content: ContentBlockSourceContent{
			Array: []ContentBlockSourceItem{
				{Text: &TextBlockParam{Type: "text", Text: "text part"}},
				{Image: &ImageBlockParam{
					Type:   "image",
					Source: ImageSource{Base64: &Base64ImageSource{Type: "base64", MediaType: "image/webp", Data: "webp_data"}},
				}},
			},
		},
	}, src)
}

func TestToolResultContent_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ToolResultContent
		wantErr bool
	}{
		{
			name:    "string content",
			jsonStr: `"result text"`,
			want:    ToolResultContent{Text: "result text"},
		},
		{
			name:    "array with text block",
			jsonStr: `[{"type":"text","text":"result text"}]`,
			want: ToolResultContent{Array: []ToolResultContentItem{
				{Text: &TextBlockParam{Type: "text", Text: "result text"}},
			}},
		},
		{
			name:    "array with image block",
			jsonStr: `[{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}]`,
			want: ToolResultContent{Array: []ToolResultContentItem{
				{Image: &ImageBlockParam{
					Type:   "image",
					Source: ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/img.png"}},
				}},
			}},
		},
		{
			name:    "array with document block",
			jsonStr: `[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"doc content"}}]`,
			want: ToolResultContent{Array: []ToolResultContentItem{
				{Document: &DocumentBlockParam{
					Type:   "document",
					Source: DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "doc content"}},
				}},
			}},
		},
		{
			name:    "array with search result block",
			jsonStr: `[{"type":"search_result","source":"https://example.com","title":"Result","content":[{"type":"text","text":"snippet"}]}]`,
			want: ToolResultContent{Array: []ToolResultContentItem{
				{SearchResult: &SearchResultBlockParam{
					Type:    "search_result",
					Source:  "https://example.com",
					Title:   "Result",
					Content: []TextBlockParam{{Type: "text", Text: "snippet"}},
				}},
			}},
		},
		{
			name:    "invalid content",
			jsonStr: `12345`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c ToolResultContent
			err := c.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, c)
		})
	}
}

func TestToolResultContent_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		c       ToolResultContent
		want    string
		wantErr bool
	}{
		{
			name: "string content",
			c:    ToolResultContent{Text: "result text"},
			want: `"result text"`,
		},
		{
			name: "array content",
			c: ToolResultContent{Array: []ToolResultContentItem{
				{Text: &TextBlockParam{Type: "text", Text: "result text"}},
			}},
			want: `[{"text":"result text","type":"text"}]`,
		},
		{
			name:    "empty content",
			c:       ToolResultContent{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.c.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestToolResultContentItem_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    ToolResultContentItem
		wantErr bool
	}{
		{
			name:    "text item",
			jsonStr: `{"type":"text","text":"hello"}`,
			want:    ToolResultContentItem{Text: &TextBlockParam{Type: "text", Text: "hello"}},
		},
		{
			name:    "image item",
			jsonStr: `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}`,
			want: ToolResultContentItem{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{Base64: &Base64ImageSource{Type: "base64", MediaType: "image/png", Data: "abc"}},
			}},
		},
		{
			name:    "search result item",
			jsonStr: `{"type":"search_result","source":"https://example.com","title":"Result","content":[{"type":"text","text":"snippet"}]}`,
			want: ToolResultContentItem{SearchResult: &SearchResultBlockParam{
				Type:    "search_result",
				Source:  "https://example.com",
				Title:   "Result",
				Content: []TextBlockParam{{Type: "text", Text: "snippet"}},
			}},
		},
		{
			name:    "document item",
			jsonStr: `{"type":"document","source":{"type":"text","media_type":"text/plain","data":"doc"}}`,
			want: ToolResultContentItem{Document: &DocumentBlockParam{
				Type:   "document",
				Source: DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "doc"}},
			}},
		},
		{
			name:    "unknown type ignored",
			jsonStr: `{"type":"thinking","thinking":"Let me think"}`,
			want:    ToolResultContentItem{},
		},
		{
			name:    "missing type",
			jsonStr: `{"text":"hello"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item ToolResultContentItem
			err := item.UnmarshalJSON([]byte(tt.jsonStr))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, item)
		})
	}
}

func TestToolResultContentItem_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		item    ToolResultContentItem
		want    string
		wantErr bool
	}{
		{
			name: "text item",
			item: ToolResultContentItem{Text: &TextBlockParam{Type: "text", Text: "hello"}},
			want: `{"text":"hello","type":"text"}`,
		},
		{
			name: "image item",
			item: ToolResultContentItem{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/img.png"}},
			}},
			want: `{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}`,
		},
		{
			name: "search result item",
			item: ToolResultContentItem{SearchResult: &SearchResultBlockParam{
				Type:    "search_result",
				Source:  "https://example.com",
				Title:   "Result",
				Content: []TextBlockParam{{Type: "text", Text: "snippet"}},
			}},
			want: `{"type":"search_result","content":[{"text":"snippet","type":"text"}],"source":"https://example.com","title":"Result"}`,
		},
		{
			name: "document item",
			item: ToolResultContentItem{Document: &DocumentBlockParam{
				Type:   "document",
				Source: DocumentSource{PlainText: &PlainTextSource{Type: "text", MediaType: "text/plain", Data: "doc"}},
			}},
			want: `{"type":"document","source":{"type":"text","data":"doc","media_type":"text/plain"}}`,
		},
		{
			name:    "empty item",
			item:    ToolResultContentItem{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.item.MarshalJSON()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestToolResultContentItem_UnmarshalJSON_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{name: "text invalid", jsonStr: `{"type":"text","text":123}`},
		{name: "image invalid", jsonStr: `{"type":"image","source":{"type":"url","url":123}}`},
		{name: "search_result invalid", jsonStr: `{"type":"search_result","content":"not_array"}`},
		{name: "document invalid", jsonStr: `{"type":"document","source":null,"context":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item ToolResultContentItem
			err := item.UnmarshalJSON([]byte(tt.jsonStr))
			require.Error(t, err)
		})
	}
}

func TestToolResultContent_InToolResultBlockParam(t *testing.T) {
	jsonStr := `{"type":"tool_result","tool_use_id":"tu_123","content":[{"type":"text","text":"42"},{"type":"image","source":{"type":"url","url":"https://example.com/chart.png"}}]}`
	var param ToolResultBlockParam
	err := json.Unmarshal([]byte(jsonStr), &param)
	require.NoError(t, err)
	require.Equal(t, ToolResultBlockParam{
		Type:      "tool_result",
		ToolUseID: "tu_123",
		Content: &ToolResultContent{Array: []ToolResultContentItem{
			{Text: &TextBlockParam{Type: "text", Text: "42"}},
			{Image: &ImageBlockParam{
				Type:   "image",
				Source: ImageSource{URL: &URLImageSource{Type: "url", URL: "https://example.com/chart.png"}},
			}},
		}},
	}, param)

	// Round-trip marshal.
	data, err := json.Marshal(param)
	require.NoError(t, err)
	require.JSONEq(t, jsonStr, string(data))
}

// strPtr is a helper to create a pointer to a string literal.
func strPtr(s string) *string {
	return &s
}
