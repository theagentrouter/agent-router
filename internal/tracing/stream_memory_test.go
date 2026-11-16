// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/tracing/otelgenai"
)

// BenchmarkMessageSpanStream measures allocation across an entire sampled
// stream, including folding at EndSpan. B/op is total allocation, not peak heap.
// Each delta owns its text, as independently decoded provider events would.
func BenchmarkMessageSpanStream(b *testing.B) {
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	b.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("stream-memory-benchmark")
	const deltaBytes = 128
	payload := strings.Repeat("x", deltaBytes)
	for _, capture := range []bool{false, true} {
		for _, count := range []int{128, 1024, 8192} {
			b.Run(fmt.Sprintf("capture=%t/chunks=%d", capture, count), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(count * deltaBytes))
				for b.Loop() {
					_, underlying := tracer.Start(context.Background(), "messages")
					s := &messageSpan{
						span:     underlying,
						recorder: otelgenai.NewMessageRecorder(&otelgenai.Config{CaptureMessageContent: capture}),
					}
					s.RecordResponseChunk(&anthropic.MessagesStreamChunk{
						ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{
							ContentBlock: anthropic.MessagesContentBlock{Text: &anthropic.TextBlock{}},
						},
					})
					for range count {
						s.RecordResponseChunk(&anthropic.MessagesStreamChunk{
							ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{
								Delta: anthropic.ContentBlockDelta{Text: strings.Clone(payload)},
							},
						})
					}
					s.RecordResponseChunk(&anthropic.MessagesStreamChunk{
						MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{Usage: anthropic.Usage{OutputTokens: float64(count)}},
					})
					s.EndSpan()
				}
			})
		}
	}
}
