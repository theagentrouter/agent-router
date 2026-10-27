// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// convertSSEToJSON converts a complete SSE stream to a single JSON-encoded
// openai.ChatCompletionResponse. This will not serialize zero values including
// fields whose values are zero or empty, or nested objects where all fields
// have zero values.
//
// TODO: This can be refactored in "streaming" in stateful way without asking for all chunks at once.
// That would reduce a slice allocation for events.
// TODO Or, even better, we can make the chunk version of buildResponseAttributes which accepts a single
// openai.ChatCompletionResponseChunk one at a time, and then we won't need to accumulate all chunks
// in memory.
func convertSSEToJSON(chunks []*openai.ChatCompletionResponseChunk) *openai.ChatCompletionResponse {
	var (
		firstChunk   *openai.ChatCompletionResponseChunk
		content      strings.Builder
		usage        *openai.Usage
		annotations  []openai.Annotation
		role         string
		obfuscation  string
		finishReason openai.ChatCompletionChoicesFinishReason
		model        string
	)

	for _, chunk := range chunks {
		if firstChunk == nil {
			firstChunk = chunk
		}

		// Capture the model from the first chunk that has one: some
		// upstreams (e.g. Azure OpenAI's leading content-filter chunk)
		// send their first chunk with an empty model. Unlike the
		// translator's streamingResponseModel (last-non-empty-wins
		// across the whole stream), this keeps the first non-empty
		// value so a later, possibly-anomalous chunk can't silently
		// override it.
		if model == "" && chunk.Model != "" {
			model = chunk.Model
		}

		// Accumulate content, role, and annotations from delta (assuming single choice at index 0).
		if len(chunk.Choices) > 0 {
			if chunk.Choices[0].Delta != nil {
				if chunk.Choices[0].Delta.Content != nil {
					content.WriteString(*chunk.Choices[0].Delta.Content)
				}
				if chunk.Choices[0].Delta.Role != "" {
					role = chunk.Choices[0].Delta.Role
				}
				if as := chunk.Choices[0].Delta.Annotations; as != nil && len(*as) > 0 {
					annotations = append(annotations, *as...)
				}
			}
			// Capture finish_reason from any chunk that has it.
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}
		}

		// Capture usage from the last chunk that has it.
		if chunk.Usage != nil {
			usage = chunk.Usage
		}

		// Capture obfuscation from the last chunk that has it.
		if chunk.Obfuscation != "" {
			obfuscation = chunk.Obfuscation
		}
	}

	// If no valid first chunk found, return a minimal response.
	if firstChunk == nil {
		// Default to "stop" if no finish reason was captured.
		if finishReason == "" {
			finishReason = openai.ChatCompletionChoicesFinishReasonStop
		}
		return &openai.ChatCompletionResponse{
			ID:      "",
			Object:  "chat.completion.chunk",
			Created: openai.JSONUNIXTime{},
			Model:   "",
			Choices: []openai.ChatCompletionResponseChoice{{
				Index:        0,
				FinishReason: finishReason,
				Message: openai.ChatCompletionResponseChoiceMessage{
					Role: role,
				},
			}},
		}
	}

	// Build the response as a chunk with accumulated content.
	contentStr := content.String()

	// Default to "stop" if no finish reason was captured.
	if finishReason == "" {
		finishReason = openai.ChatCompletionChoicesFinishReasonStop
	}

	var annotationsPtr *[]openai.Annotation
	if len(annotations) > 0 {
		annotationsPtr = &annotations
	}

	// Create a ChatCompletionResponse with all accumulated content.
	response := &openai.ChatCompletionResponse{
		ID:                firstChunk.ID,
		Object:            "chat.completion.chunk", // Keep chunk object type for streaming.
		Created:           firstChunk.Created,
		Model:             model,
		ServiceTier:       firstChunk.ServiceTier,
		SystemFingerprint: firstChunk.SystemFingerprint,
		Obfuscation:       obfuscation,
		Choices: []openai.ChatCompletionResponseChoice{{
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:        role,
				Content:     &contentStr,
				Annotations: annotationsPtr,
			},
			Index:        0,
			FinishReason: finishReason,
		}},
	}

	if usage != nil {
		response.Usage = *usage
	}

	return response
}
