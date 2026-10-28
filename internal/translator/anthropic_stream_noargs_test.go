// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// streamedToolArguments feeds Anthropic SSE event blocks to the parser and
// returns the arguments a client would concatenate for each tool call index.
func streamedToolArguments(t *testing.T, events ...string) map[int64]string {
	t.Helper()
	p := newAnthropicStreamParser("claude-sonnet-5-5")
	args := map[int64]string{}
	for _, event := range events {
		chunk, err := p.parseAndHandleEvent([]byte(event))
		require.NoError(t, err)
		if chunk == nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta == nil {
				continue
			}
			for _, call := range choice.Delta.ToolCalls {
				args[call.Index] += call.Function.Arguments
			}
		}
	}
	return args
}

func TestAnthropicStreamToolWithoutInputGetsEmptyObject(t *testing.T) {
	start := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	toolStart := func(index string) string {
		return `event: content_block_start
data: {"type":"content_block_start","index":` + index + `,"content_block":{"type":"tool_use","id":"toolu_` + index + `","name":"now","input":{}}}`
	}
	stop := func(index string) string {
		return `event: content_block_stop
data: {"type":"content_block_stop","index":` + index + `}`
	}
	emptyDelta := func(index string) string {
		return `event: content_block_delta
data: {"type":"content_block_delta","index":` + index + `,"delta":{"type":"input_json_delta","partial_json":""}}`
	}

	t.Run("no delta", func(t *testing.T) {
		args := streamedToolArguments(t, start, toolStart("0"), stop("0"))
		require.JSONEq(t, `{}`, args[0])
	})

	t.Run("empty delta", func(t *testing.T) {
		args := streamedToolArguments(t, start, toolStart("0"), emptyDelta("0"), stop("0"))
		require.JSONEq(t, `{}`, args[0])
	})

	t.Run("arguments are not padded", func(t *testing.T) {
		args := streamedToolArguments(t, start,
			toolStart("0"),
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
			stop("0"),
			toolStart("1"), emptyDelta("1"), stop("1"),
		)
		require.JSONEq(t, `{"a":1}`, args[0])
		require.JSONEq(t, `{}`, args[1])
	})
}
