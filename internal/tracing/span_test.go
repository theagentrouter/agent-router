// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func TestChatCompletionSpan_RecordResponseChunk(t *testing.T) {
	chunks := []*openai.ChatCompletionResponseChunk{{}, {}}
	s := &chatCompletionSpan{}
	_ = testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s.span = span
		s.RecordResponseChunk(chunks[0])
		s.RecordResponseChunk(chunks[1])
		return false // Recording of chunks shouldn't end the span.
	})
	require.Len(t, s.chunks, 2)
	require.Equal(t, chunks, s.chunks)
}

func TestChatCompletionSpan_RecordResponse(t *testing.T) {
	resp := &openai.ChatCompletionResponse{ID: "chatcmpl-abc123"}
	respBytes, err := json.Marshal(resp)
	require.NoError(t, err)
	s := &chatCompletionSpan{recorder: testChatCompletionRecorder{}}
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s.span = span
		s.RecordResponse(resp)
		return false // Recording of chunks shouldn't end the span.
	})
	require.Empty(t, s.chunks)
	require.Equal(t, []attribute.KeyValue{
		attribute.Int("statusCode", 200),
		attribute.Int("respBodyLen", len(respBytes)),
	}, actualSpan.Attributes)
}

// TestChatCompletionSpan_RecordBackend pins that the backend only reaches the
// span when the convention's recorder asks for it. The backend is resolved for
// every request, so a recorder whose convention defines no backend attributes
// must not be required to implement the interface, and must not be called.
func TestChatCompletionSpan_RecordBackend(t *testing.T) {
	backend := tracingapi.Backend{Schema: "OpenAI", Name: "some-backend"}

	t.Run("recorder that records backends", func(t *testing.T) {
		s := &chatCompletionSpan{recorder: testBackendChatCompletionRecorder{}}
		actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			s.span = span
			s.RecordBackend(backend)
			return false // Recording the backend must not end the span.
		})
		require.Equal(t, []attribute.KeyValue{
			attribute.String("backendSchema", "OpenAI"),
			attribute.String("backendName", "some-backend"),
		}, actualSpan.Attributes)
	})

	t.Run("recorder that does not", func(t *testing.T) {
		s := &chatCompletionSpan{recorder: testChatCompletionRecorder{}}
		actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
			s.span = span
			s.RecordBackend(backend)
			return false
		})
		require.Empty(t, actualSpan.Attributes)
	})
}

// testBackendChatCompletionRecorder is a testChatCompletionRecorder whose
// convention also records the resolved backend.
type testBackendChatCompletionRecorder struct {
	testChatCompletionRecorder
}

func (testBackendChatCompletionRecorder) RecordBackend(span oteltrace.Span, backend tracingapi.Backend) {
	span.SetAttributes(
		attribute.String("backendSchema", backend.Schema),
		attribute.String("backendName", backend.Name),
	)
}

func TestChatCompletionSpan_EndSpanOnError(t *testing.T) {
	msg := "why did you do that?"
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s := &chatCompletionSpan{span: span, recorder: testChatCompletionRecorder{}}
		s.EndSpanOnError(500, []byte(msg))
		return true // EndSpan ends the underlying span.
	})

	require.Equal(t, []attribute.KeyValue{
		attribute.Int("statusCode", 500),
		attribute.String("errorBody", msg),
	}, actualSpan.Attributes)
}

func TestChatCompletionSpan_EndSpan(t *testing.T) {
	s := &chatCompletionSpan{recorder: testChatCompletionRecorder{}, chunks: []*openai.ChatCompletionResponseChunk{{}, {}}}
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s.span = span
		s.EndSpan()
		return true // EndSpan ends the underlying span.
	})

	require.Equal(t, []attribute.KeyValue{
		attribute.Int("eventCount", 2),
	}, actualSpan.Attributes)
}

func TestEmbeddingsSpan_EndSpanOnError(t *testing.T) {
	msg := "embeddings error occurred"
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s := &embeddingsSpan{span: span, recorder: testEmbeddingsRecorder{}}
		s.EndSpanOnError(500, []byte(msg))
		return true // EndSpanOnError ends the underlying span.
	})

	require.Equal(t, []attribute.KeyValue{
		attribute.Int("statusCode", 500),
		attribute.String("errorBody", msg),
	}, actualSpan.Attributes)
}

func TestCompletionSpan_RecordResponseChunk(t *testing.T) {
	chunks := []*openai.CompletionResponse{{}, {}}
	s := &completionSpan{}
	_ = testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s.span = span
		s.RecordResponseChunk(chunks[0])
		s.RecordResponseChunk(chunks[1])
		return false // Recording of chunks shouldn't end the span.
	})
	require.Len(t, s.chunks, 2)
	require.Equal(t, chunks, s.chunks)
}

func TestCompletionSpan_RecordResponse(t *testing.T) {
	resp := &openai.CompletionResponse{ID: "cmpl-abc123"}
	respBytes, err := json.Marshal(resp)
	require.NoError(t, err)
	s := &completionSpan{recorder: testCompletionRecorder{}}
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s.span = span
		s.RecordResponse(resp)
		return false // Recording of response shouldn't end the span.
	})
	require.Empty(t, s.chunks)
	require.Equal(t, []attribute.KeyValue{
		attribute.Int("statusCode", 200),
		attribute.Int("respBodyLen", len(respBytes)),
	}, actualSpan.Attributes)
}

func TestCompletionSpan_EndSpanOnError(t *testing.T) {
	msg := "completion error occurred"
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s := &completionSpan{span: span, recorder: testCompletionRecorder{}}
		s.EndSpanOnError(500, []byte(msg))
		return true // EndSpanOnError ends the underlying span.
	})

	require.Equal(t, []attribute.KeyValue{
		attribute.Int("statusCode", 500),
		attribute.String("errorBody", msg),
	}, actualSpan.Attributes)
}

func TestCompletionSpan_EndSpan(t *testing.T) {
	s := &completionSpan{recorder: testCompletionRecorder{}, chunks: []*openai.CompletionResponse{{}, {}}}
	actualSpan := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		s.span = span
		s.EndSpan()
		return true // EndSpan ends the underlying span.
	})

	require.Equal(t, []attribute.KeyValue{
		attribute.Int("eventCount", 2),
	}, actualSpan.Attributes)
}
