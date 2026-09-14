// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func textStreamChunks() []*MessagesStreamChunk {
	stop := StopReason("end_turn")
	return []*MessagesStreamChunk{
		{MessageStart: (*MessagesStreamChunkMessageStart)(&MessagesResponse{
			ID:    "msg_1",
			Model: "claude-sonnet-5",
			Role:  "assistant",
			Usage: &Usage{InputTokens: 10},
		})},
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{
			Index:        0,
			ContentBlock: MessagesContentBlock{Text: &TextBlock{Text: ""}},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{Text: "hello "},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{Text: "world"},
		}},
		{MessageDelta: &MessagesStreamChunkMessageDelta{
			Delta: MessagesStreamChunkMessageDeltaDelta{StopReason: stop},
			Usage: Usage{OutputTokens: 4},
		}},
	}
}

func TestMessagesResponseFromStream(t *testing.T) {
	resp := MessagesResponseFromStream(textStreamChunks())

	require.Equal(t, "msg_1", resp.ID)
	require.Equal(t, "claude-sonnet-5", resp.Model)
	require.NotNil(t, resp.Usage)
	require.Equal(t, float64(10), resp.Usage.InputTokens)
	require.Equal(t, float64(4), resp.Usage.OutputTokens)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "hello world", resp.Content[0].Text.Text)
}

// TestMessagesResponseFromStream_doesNotMutateInput pins that folding leaves the
// chunks untouched. The fold appends deltas into content blocks in place, so
// aliasing the caller's blocks made a second fold of the same chunks return
// doubled text.
func TestMessagesResponseFromStream_doesNotMutateInput(t *testing.T) {
	chunks := textStreamChunks()

	first := MessagesResponseFromStream(chunks)
	second := MessagesResponseFromStream(chunks)

	require.Equal(t, "hello world", first.Content[0].Text.Text)
	require.Equal(t, "hello world", second.Content[0].Text.Text,
		"folding the same chunks twice must produce the same response")

	// The originating chunk must still hold the empty block it started with.
	require.Empty(t, chunks[1].ContentBlockStart.ContentBlock.Text.Text)
}

func TestMessagesResponseFromStream_boundaries(t *testing.T) {
	tests := []struct {
		name   string
		chunks []*MessagesStreamChunk
	}{
		{name: "no chunks", chunks: nil},
		{name: "empty chunk", chunks: []*MessagesStreamChunk{{}}},
		{name: "nil chunk", chunks: []*MessagesStreamChunk{nil}},
		{name: "message stop alone", chunks: []*MessagesStreamChunk{{MessageStop: &MessagesStreamChunkMessageStop{}}}},
		{
			name: "content block stop for an unknown index is ignored",
			chunks: []*MessagesStreamChunk{{
				ContentBlockStop: &MessagesStreamChunkContentBlockStop{Index: 5},
			}},
		},
		{
			name: "negative index is ignored",
			chunks: []*MessagesStreamChunk{{
				ContentBlockStart: &MessagesStreamChunkContentBlockStart{Index: -1},
			}},
		},
		{
			name: "index beyond the cap is ignored",
			chunks: []*MessagesStreamChunk{{
				ContentBlockStart: &MessagesStreamChunkContentBlockStart{Index: 1000},
			}},
		},
		{
			name: "delta for an unknown index is ignored",
			chunks: []*MessagesStreamChunk{{
				ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Index: 5, Delta: ContentBlockDelta{Text: "orphan"},
				},
			}},
		},
		{
			name: "delta for a negative index is ignored",
			chunks: []*MessagesStreamChunk{{
				ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Index: -1, Delta: ContentBlockDelta{Text: "orphan"},
				},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				require.NotNil(t, MessagesResponseFromStream(tc.chunks))
			})
		})
	}
}

// TestMessagesResponseFromStream_indexCap pins the guard against a hostile
// upstream allocating an unbounded content slice.
func TestMessagesResponseFromStream_indexCap(t *testing.T) {
	for _, idx := range []int{998, 999, 1000, 1001} {
		resp := MessagesResponseFromStream([]*MessagesStreamChunk{{
			ContentBlockStart: &MessagesStreamChunkContentBlockStart{
				Index:        idx,
				ContentBlock: MessagesContentBlock{Text: &TextBlock{Text: "x"}},
			},
		}})
		if idx < 1000 {
			require.Len(t, resp.Content, idx+1, "index %d", idx)
		} else {
			require.Empty(t, resp.Content, "index %d must be rejected", idx)
		}
	}
}

// TestMessagesResponseFromStream_toolUse pins that tool arguments, which arrive
// as JSON fragments, are only parsed once the block closes. A fragment is not
// valid JSON on its own, so parsing eagerly would drop the arguments entirely.
func TestMessagesResponseFromStream_toolUse(t *testing.T) {
	resp := MessagesResponseFromStream([]*MessagesStreamChunk{
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{
			Index:        0,
			ContentBlock: MessagesContentBlock{Tool: &ToolUseBlock{ID: "toolu_1", Name: "get_weather"}},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{PartialJSON: `{"ci`},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{PartialJSON: `ty":"Berlin"}`},
		}},
		{ContentBlockStop: &MessagesStreamChunkContentBlockStop{Index: 0}},
		{MessageStop: &MessagesStreamChunkMessageStop{}},
	})

	require.Len(t, resp.Content, 1)
	require.Equal(t, map[string]any{"city": "Berlin"}, resp.Content[0].Tool.Input)
}

// TestMessagesResponseFromStream_toolUseUnparsable pins that a truncated
// argument stream leaves the input unset rather than half-populated.
func TestMessagesResponseFromStream_toolUseUnparsable(t *testing.T) {
	resp := MessagesResponseFromStream([]*MessagesStreamChunk{
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{
			Index:        0,
			ContentBlock: MessagesContentBlock{Tool: &ToolUseBlock{ID: "toolu_1", Name: "get_weather"}},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{PartialJSON: `{"ci`},
		}},
		{ContentBlockStop: &MessagesStreamChunkContentBlockStop{Index: 0}},
	})

	require.Len(t, resp.Content, 1)
	require.Nil(t, resp.Content[0].Tool.Input)
}

// TestMessagesResponseFromStream_thinking pins that reasoning text accumulates
// and that the signature, which arrives whole on its own delta, replaces rather
// than appends.
func TestMessagesResponseFromStream_thinking(t *testing.T) {
	resp := MessagesResponseFromStream([]*MessagesStreamChunk{
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{
			Index:        0,
			ContentBlock: MessagesContentBlock{Thinking: &ThinkingBlock{}},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{Thinking: "first "},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{Thinking: "second"},
		}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
			Index: 0, Delta: ContentBlockDelta{Signature: "sig-1"},
		}},
		{ContentBlockStop: &MessagesStreamChunkContentBlockStop{Index: 0}},
	})

	require.Len(t, resp.Content, 1)
	require.Equal(t, "first second", resp.Content[0].Thinking.Thinking)
	require.Equal(t, "sig-1", resp.Content[0].Thinking.Signature)
}

// TestMessagesResponseFromStream_usageWithoutMessageStart pins that a stream
// which never reported input tokens still records the output tokens it did
// report, rather than dropping usage because there was nothing to merge into.
func TestMessagesResponseFromStream_usageWithoutMessageStart(t *testing.T) {
	resp := MessagesResponseFromStream([]*MessagesStreamChunk{{
		MessageDelta: &MessagesStreamChunkMessageDelta{
			Delta: MessagesStreamChunkMessageDeltaDelta{StopReason: StopReason("end_turn")},
			Usage: Usage{OutputTokens: 7},
		},
	}})

	require.NotNil(t, resp.Usage)
	require.Equal(t, float64(7), resp.Usage.OutputTokens)
	require.Zero(t, resp.Usage.InputTokens)
}
