// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessagesResponseFromStream_initialContentDoesNotMutateInput(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "delta", true: "replacement"}[replace], func(t *testing.T) {
			initial := &MessagesResponse{Content: []MessagesContentBlock{{Text: &TextBlock{Text: "initial"}}}}
			chunks := []*MessagesStreamChunk{{MessageStart: (*MessagesStreamChunkMessageStart)(initial)}}
			if replace {
				chunks = append(chunks, &MessagesStreamChunk{ContentBlockStart: &MessagesStreamChunkContentBlockStart{
					ContentBlock: MessagesContentBlock{Text: &TextBlock{Text: "replacement"}},
				}})
			}
			chunks = append(chunks, &MessagesStreamChunk{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{Delta: ContentBlockDelta{Text: " appended"}}})
			first := MessagesResponseFromStream(chunks)
			require.Equal(t, "initial", initial.Content[0].Text.Text)
			second := MessagesResponseFromStream(chunks)
			require.Equal(t, first, second)
			want := "initial appended"
			if replace {
				want = "replacement appended"
			}
			require.Equal(t, want, first.Content[0].Text.Text)
		})
	}
}
