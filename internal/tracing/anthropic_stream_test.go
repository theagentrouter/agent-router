// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/otelgenai"
)

func TestMessageSpanMetadataStream(t *testing.T) {
	for _, withStart := range []bool{false, true} {
		chunks := []*anthropic.MessagesStreamChunk{nil}
		usage := &anthropic.Usage{InputTokens: 12, CacheReadInputTokens: 3, CacheCreationInputTokens: 2}
		if withStart {
			chunks = append(chunks, &anthropic.MessagesStreamChunk{MessageStart: (*anthropic.MessagesStreamChunkMessageStart)(
				&anthropic.MessagesResponse{ID: "msg_1", Model: "test-model", Usage: usage},
			)})
		}
		chunks = append(chunks,
			&anthropic.MessagesStreamChunk{ContentBlockStart: &anthropic.MessagesStreamChunkContentBlockStart{ContentBlock: anthropic.MessagesContentBlock{Text: &anthropic.TextBlock{Text: "private"}}}},
			&anthropic.MessagesStreamChunk{ContentBlockDelta: &anthropic.MessagesStreamChunkContentBlockDelta{Delta: anthropic.ContentBlockDelta{Text: " output"}}},
			&anthropic.MessagesStreamChunk{MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{Usage: anthropic.Usage{OutputTokens: 4}}},
			&anthropic.MessagesStreamChunk{MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{Usage: anthropic.Usage{OutputTokens: 9}, Delta: anthropic.MessagesStreamChunkMessageDeltaDelta{StopReason: "end_turn"}}},
		)
		recorder := otelgenai.NewMessageRecorder(&otelgenai.Config{})
		actual := testotel.RecordWithSpan(t, func(underlying trace.Span) bool {
			s := &messageSpan{span: underlying, recorder: recorder}
			for _, chunk := range chunks {
				s.RecordResponseChunk(chunk)
			}
			require.Empty(t, s.chunks, "metadata-only recording must not retain response chunks")
			require.Zero(t, usage.OutputTokens, "input usage must remain unchanged")
			s.EndSpan()
			require.Nil(t, s.stream)
			return true
		})
		expected := testotel.RecordWithSpan(t, func(underlying trace.Span) bool {
			recorder.RecordResponseChunks(underlying, chunks)
			return false
		})
		require.Equal(t, expected.Attributes, actual.Attributes)
		require.Equal(t, codes.Ok, actual.Status.Code)
	}
}

func TestMessageSpanMetadataStreamError(t *testing.T) {
	actual := testotel.RecordWithSpan(t, func(underlying trace.Span) bool {
		s := &messageSpan{span: underlying, recorder: otelgenai.NewMessageRecorder(&otelgenai.Config{})}
		s.RecordResponseChunk(&anthropic.MessagesStreamChunk{MessageDelta: &anthropic.MessagesStreamChunkMessageDelta{Usage: anthropic.Usage{OutputTokens: 4}}})
		s.EndSpanOnError(502, []byte("upstream failed"))
		require.Nil(t, s.stream)
		return true
	})
	require.Equal(t, codes.Error, actual.Status.Code)
	require.Contains(t, actual.Attributes, attribute.Int(otelgenai.UsageOutputTokens, 4))
}

func TestMessageSpanContentCaptureRetainsBatchPath(t *testing.T) {
	s := &messageSpan{recorder: otelgenai.NewMessageRecorder(&otelgenai.Config{CaptureMessageContent: true})}
	s.RecordResponseChunk(&anthropic.MessagesStreamChunk{})
	require.Len(t, s.chunks, 1)
	require.Nil(t, s.stream)
}
