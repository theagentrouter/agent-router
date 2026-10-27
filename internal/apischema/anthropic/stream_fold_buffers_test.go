// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessagesResponseFromStream_interleavedBuffers(t *testing.T) {
	chunks := []*MessagesStreamChunk{
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{Index: 0, ContentBlock: MessagesContentBlock{Text: &TextBlock{Text: "prefix "}}}},
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{Index: 1, ContentBlock: MessagesContentBlock{Thinking: &ThinkingBlock{Thinking: "reason "}}}},
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{Index: 2, ContentBlock: MessagesContentBlock{Tool: &ToolUseBlock{}}}},
	}
	for _, part := range []string{"a", "b", "c"} {
		chunks = append(chunks,
			&MessagesStreamChunk{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{Index: 0, Delta: ContentBlockDelta{Text: part}}},
			&MessagesStreamChunk{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{Index: 1, Delta: ContentBlockDelta{Thinking: part, Signature: "sig"}}},
		)
	}
	for _, part := range []string{`{"value":`, `"abc"`, `}`} {
		chunks = append(chunks, &MessagesStreamChunk{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{Index: 2, Delta: ContentBlockDelta{PartialJSON: part}}})
	}
	chunks = append(chunks, &MessagesStreamChunk{ContentBlockStop: &MessagesStreamChunkContentBlockStop{Index: 2}})
	for range 2 {
		response := MessagesResponseFromStream(chunks)
		require.Equal(t, "prefix abc", response.Content[0].Text.Text)
		require.Equal(t, "reason abc", response.Content[1].Thinking.Thinking)
		require.Equal(t, "sig", response.Content[1].Thinking.Signature)
		require.Equal(t, map[string]any{"value": "abc"}, response.Content[2].Tool.Input)
	}
	require.Equal(t, "prefix ", chunks[0].ContentBlockStart.ContentBlock.Text.Text)
	require.Equal(t, "reason ", chunks[1].ContentBlockStart.ContentBlock.Thinking.Thinking)
}

func TestMessagesResponseFromStream_replacedBlockBuffer(t *testing.T) {
	response := MessagesResponseFromStream([]*MessagesStreamChunk{
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{ContentBlock: MessagesContentBlock{Text: &TextBlock{Text: "old"}}}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{Delta: ContentBlockDelta{Text: " discarded"}}},
		{ContentBlockStart: &MessagesStreamChunkContentBlockStart{ContentBlock: MessagesContentBlock{Text: &TextBlock{Text: "new"}}}},
		{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{Delta: ContentBlockDelta{Text: " kept"}}},
	})
	require.Equal(t, "new kept", response.Content[0].Text.Text)
}
