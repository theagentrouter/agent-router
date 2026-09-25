// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// MessagesResponseFromStream folds a complete stream of chunks back into the
// MessagesResponse the provider sent, so a streamed response can be recorded
// with the same logic as the equivalent unary one.
//
// TODO: This can be refactored in "streaming" in stateful way without asking for all chunks at once.
// That would reduce a slice allocation for events.
func MessagesResponseFromStream(chunks []*MessagesStreamChunk) *MessagesResponse {
	var response MessagesResponse
	toolInputs := make(map[int]string)
	textBuffers := make(map[int]*strings.Builder)
	thinkingBuffers := make(map[int]*strings.Builder)
	toolBuffers := make(map[int]*strings.Builder)

	for _, event := range chunks {
		if event == nil {
			continue
		}
		switch {
		case event.MessageStart != nil:
			clear(textBuffers)
			clear(thinkingBuffers)
			response = *(*MessagesResponse)(event.MessageStart)
			// Ensure Content is initialized if nil.
			if response.Content == nil {
				response.Content = []MessagesContentBlock{}
			}

		case event.MessageDelta != nil:
			delta := event.MessageDelta
			if response.Usage == nil {
				response.Usage = &delta.Usage
			} else {
				// Usage is cumulative for output tokens in message_delta.
				// Input tokens are usually in message_start.
				response.Usage.OutputTokens = delta.Usage.OutputTokens
			}
			response.StopReason = &delta.Delta.StopReason
			response.StopSequence = &delta.Delta.StopSequence

		case event.ContentBlockStart != nil:
			idx := event.ContentBlockStart.Index
			// Guard against negative or unreasonably large indices from a hostile upstream.
			const maxContentBlocks = 1000
			if idx < 0 || idx >= maxContentBlocks {
				continue
			}
			// Grow slice if needed.
			if idx >= len(response.Content) {
				newContent := make([]MessagesContentBlock, idx+1)
				copy(newContent, response.Content)
				response.Content = newContent
			}
			// Copy the block rather than aliasing the caller's: deltas below
			// append to it in place, which would otherwise mutate the input and
			// make a second fold of the same chunks produce doubled content.
			response.Content[idx] = copyContentBlock(event.ContentBlockStart.ContentBlock)
			delete(textBuffers, idx)
			delete(thinkingBuffers, idx)

		case event.ContentBlockDelta != nil:
			idx := event.ContentBlockDelta.Index
			if idx >= 0 && idx < len(response.Content) {
				block := &response.Content[idx]
				delta := event.ContentBlockDelta.Delta

				if block.Text != nil && delta.Text != "" {
					block.Text.Text = appendStreamText(textBuffers, idx, block.Text.Text, delta.Text)
				}
				if block.Tool != nil && delta.PartialJSON != "" {
					toolInputs[idx] = appendStreamText(toolBuffers, idx, toolInputs[idx], delta.PartialJSON)
				}
				if block.Thinking != nil {
					if delta.Thinking != "" {
						block.Thinking.Thinking = appendStreamText(thinkingBuffers, idx, block.Thinking.Thinking, delta.Thinking)
					}
					if delta.Signature != "" {
						block.Thinking.Signature = delta.Signature
					}
				}
			}

		case event.ContentBlockStop != nil:
			idx := event.ContentBlockStop.Index
			if jsonStr, ok := toolInputs[idx]; ok {
				if idx < len(response.Content) && response.Content[idx].Tool != nil {
					var input map[string]any
					if err := json.Unmarshal([]byte(jsonStr), &input); err == nil {
						response.Content[idx].Tool.Input = input
					}
				}
				delete(toolInputs, idx)
				delete(toolBuffers, idx)
			}

		case event.MessageStop != nil:
			// Nothing to do.
		}
	}
	return &response
}

// Keep one buffer per interleaved block so appending a delta does not copy the
// entire accumulated prefix. Builders are pointers because they cannot be copied.
func appendStreamText(buffers map[int]*strings.Builder, idx int, initial, delta string) string {
	buffer := buffers[idx]
	if buffer == nil {
		buffer = &strings.Builder{}
		buffer.WriteString(initial)
		buffers[idx] = buffer
	}
	buffer.WriteString(delta)
	return buffer.String()
}

// copyContentBlock deep-copies the fields the fold mutates, so folding does not
// modify the chunks it was given.
func copyContentBlock(block MessagesContentBlock) MessagesContentBlock {
	if block.Text != nil {
		text := *block.Text
		block.Text = &text
	}
	if block.Tool != nil {
		tool := *block.Tool
		block.Tool = &tool
	}
	if block.Thinking != nil {
		thinking := *block.Thinking
		block.Thinking = &thinking
	}
	return block
}
