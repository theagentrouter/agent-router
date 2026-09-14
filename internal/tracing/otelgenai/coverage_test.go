// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// This file records, per endpoint, which optional recorder hooks are wired.
//
// Without it the gaps are only visible by reading endpoints.go closely, which
// is how they stay unnoticed. Wiring or dropping a hook should fail here,
// forcing the table to be updated deliberately rather than coverage drifting
// silently.
//
// "not applicable" is distinct from "not implemented": several endpoints have
// no messages or sampling parameters to record, and the conventions define no
// attributes for them beyond the core set. The comment on each entry says which.

type coverage struct {
	requestAttrs       bool
	responseAttrs      bool
	chunkAttrs         bool
	inputMessages      bool
	outputMessages     bool
	systemInstructions bool
	toolDefinitions    bool
	conversationID     bool
	chunkMessages      bool
	foldChunks         bool
}

func TestEndpointCoverage(t *testing.T) {
	cfg := NewConfig()

	tests := []struct {
		name     string
		actual   coverage
		expected coverage
	}{
		{
			// System messages occupy a position in the conversation, so they are
			// not split out. This API has no conversation id.
			name:   "chatCompletion",
			actual: coverageOf(t, NewChatCompletionRecorder(cfg)),
			expected: coverage{
				requestAttrs: true, responseAttrs: true, chunkAttrs: true,
				inputMessages: true, outputMessages: true, chunkMessages: true,
				toolDefinitions: true,
			},
		},
		{
			// Complete. Chunks fold into the response so streaming reuses the
			// unary path. This API has no conversation id.
			name:   "message",
			actual: coverageOf(t, NewMessageRecorder(cfg)),
			expected: coverage{
				requestAttrs: true, responseAttrs: true, foldChunks: true,
				inputMessages: true, outputMessages: true,
				systemInstructions: true, toolDefinitions: true,
			},
		},
		{
			// Prompts map to user messages. This API has no tools or conversation id.
			name:   "completion",
			actual: coverageOf(t, NewCompletionRecorder(cfg)),
			expected: coverage{
				requestAttrs: true, responseAttrs: true, foldChunks: true,
				inputMessages: true, outputMessages: true,
			},
		},
		{
			// The conventions define no attribute for embedding input text or vectors.
			name:   "embeddings",
			actual: coverageOf(t, NewEmbeddingsRecorder(cfg)),
			expected: coverage{
				requestAttrs: true, responseAttrs: true,
			},
		},
		{
			// TODO: input messages and streaming chunks are not mapped yet.
			name:   "responses",
			actual: coverageOf(t, NewResponsesRecorder(cfg)),
			expected: coverage{
				requestAttrs: true, responseAttrs: true,
				outputMessages: true, systemInstructions: true, conversationID: true,
			},
		},
		{
			// The conventions define no image generation attributes beyond the core set.
			name:     "imageGeneration",
			actual:   coverageOf(t, NewImageGenerationRecorder(cfg)),
			expected: coverage{},
		},
		{
			// The conventions define no speech attributes beyond the core set.
			name:     "speech",
			actual:   coverageOf(t, NewSpeechRecorder(cfg)),
			expected: coverage{},
		},
		{
			// The conventions define no transcription attributes beyond the core set.
			name:     "transcription",
			actual:   coverageOf(t, NewTranscriptionRecorder(cfg)),
			expected: coverage{},
		},
		{
			// The conventions define no translation attributes beyond the core set.
			name:     "translation",
			actual:   coverageOf(t, NewTranslationRecorder(cfg)),
			expected: coverage{},
		},
		{
			// The conventions define no reranker attributes; rerank is already a
			// custom operation.
			name:     "rerank",
			actual:   coverageOf(t, NewRerankRecorder(cfg)),
			expected: coverage{},
		},
		{
			// TODO: reuses the chat and completion mapping once those land.
			name:     "tokenize",
			actual:   coverageOf(t, NewTokenizeRecorder(cfg)),
			expected: coverage{},
		},
		{
			// The response is a token count, so only the usage attribute applies.
			// The request is Responses-shaped, but nothing is inferred from it:
			// no inference runs, so sampling parameters and output messages have
			// no meaning here.
			name:     "responses input tokens",
			actual:   coverageOf(t, NewResponsesInputTokensRecorder(cfg)),
			expected: coverage{responseAttrs: true},
		},
		{
			// The request carries a full conversation, so it records the same
			// input content the messages endpoint does. No inference runs, so
			// sampling parameters and output messages have no meaning, and the
			// response is a token count.
			name:   "count tokens",
			actual: coverageOf(t, NewCountTokensRecorder(cfg)),
			expected: coverage{
				responseAttrs: true, inputMessages: true,
				systemInstructions: true, toolDefinitions: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.actual,
				"coverage changed for %s; update this table deliberately", tc.name)
		})
	}
}

// hooks reports which optional hooks this recorder was constructed with.
func (r *recorder[ReqT, RespT, ChunkT]) hooks() coverage {
	return coverage{
		requestAttrs:       r.requestAttrs != nil,
		responseAttrs:      r.responseAttrs != nil,
		chunkAttrs:         r.chunkAttrs != nil,
		inputMessages:      r.inputMessages != nil,
		outputMessages:     r.outputMessages != nil,
		systemInstructions: r.systemInstructions != nil,
		toolDefinitions:    r.toolDefinitions != nil,
		conversationID:     r.conversationID != nil,
		chunkMessages:      r.chunkMessages != nil,
		foldChunks:         r.foldChunks != nil,
	}
}

// coverageOf inspects which hooks a constructor wired. It relies on the shared
// generic recorder, so a constructor returning some other type fails loudly.
func coverageOf(t *testing.T, r any) coverage {
	t.Helper()
	h, ok := r.(interface{ hooks() coverage })
	require.True(t, ok, "recorder %T does not expose hooks()", r)
	return h.hooks()
}
