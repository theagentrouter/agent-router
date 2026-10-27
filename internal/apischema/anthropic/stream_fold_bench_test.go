// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// BenchmarkMessagesResponseFromStream isolates folding allocation from event
// decoding and span export. Each text fragment owns its backing storage.
func BenchmarkMessagesResponseFromStream(b *testing.B) {
	const deltaBytes = 128
	for _, count := range []int{128, 1024, 8192} {
		b.Run(fmt.Sprintf("chunks=%d", count), func(b *testing.B) {
			chunks := []*MessagesStreamChunk{{ContentBlockStart: &MessagesStreamChunkContentBlockStart{
				ContentBlock: MessagesContentBlock{Text: &TextBlock{}},
			}}}
			for range count {
				chunks = append(chunks, &MessagesStreamChunk{ContentBlockDelta: &MessagesStreamChunkContentBlockDelta{
					Delta: ContentBlockDelta{Text: strings.Repeat("x", deltaBytes)},
				}})
			}
			b.ReportAllocs()
			b.SetBytes(int64(count * deltaBytes))
			for b.Loop() {
				response := MessagesResponseFromStream(chunks)
				runtime.KeepAlive(response)
			}
		})
	}
}
