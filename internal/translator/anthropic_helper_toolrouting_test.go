// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// sse formats one Anthropic SSE event block.
func sse(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// feedEvents pushes raw SSE events through the stream parser one at a
// time -- as they arrive on the wire, so ordering is what the parser
// actually sees -- and returns every emitted OpenAI chunk.
func feedEvents(t *testing.T, events []string) []openai.ChatCompletionResponseChunk {
	t.Helper()
	p := newAnthropicStreamParser("claude-test")
	var raw []byte
	for _, ev := range events {
		_, newBody, _, _, err := p.Process(bytes.NewReader([]byte(ev)), false, nil)
		require.NoError(t, err)
		raw = append(raw, newBody...)
	}
	_, final, _, _, err := p.Process(bytes.NewReader(nil), true, nil)
	require.NoError(t, err)
	raw = append(raw, final...)

	var chunks []openai.ChatCompletionResponseChunk
	for line := range strings.SplitSeq(string(raw), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" || data == "" {
			continue
		}
		var chunk openai.ChatCompletionResponseChunk
		require.NoError(t, json.Unmarshal([]byte(data), &chunk), "chunk: %s", data)
		chunks = append(chunks, chunk)
	}
	return chunks
}

// argsByToolIndex accumulates streamed tool-call arguments per OpenAI
// tool_calls index, the way an OpenAI client would.
func argsByToolIndex(chunks []openai.ChatCompletionResponseChunk) map[int64]string {
	acc := map[int64]string{}
	for i := range chunks {
		for _, choice := range chunks[i].Choices {
			if choice.Delta == nil {
				continue
			}
			for _, tc := range choice.Delta.ToolCalls {
				acc[tc.Index] += tc.Function.Arguments
			}
		}
	}
	return acc
}

const toolRoutingMessageStart = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-test","usage":{"input_tokens":5,"output_tokens":0}}}`

// Step 2 of Anthropic's accumulation contract appends a delta to the
// block it names, so two open tool blocks accumulate separately however
// their deltas are ordered. Routing by "last tool started" instead
// appends both argument streams to whichever started last.
func TestStreamToolRoutingInterleavedDeltas(t *testing.T) {
	chunks := feedEvents(t, []string{
		sse("message_start", toolRoutingMessageStart),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_A","name":"get_weather"}}`),
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_B","name":"get_time"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Oslo\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	})

	args := argsByToolIndex(chunks)
	require.JSONEq(t, `{"city":"Oslo"}`, args[0], "tool A arguments corrupted")
	require.JSONEq(t, `{"tz":"UTC"}`, args[1], "tool B arguments corrupted")
}

// Step 3 of the contract closes the block the stop names. Keyed by
// "last tool started", the stop for an earlier tool deletes the state
// of the one still streaming, and its next input_json_delta fails the
// whole response with "received input_json_delta for unknown tool".
func TestStreamToolRoutingStopEvictsOtherTool(t *testing.T) {
	p := newAnthropicStreamParser("claude-test")
	events := []string{
		sse("message_start", toolRoutingMessageStart),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_A","name":"get_weather"}}`),
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_B","name":"get_time"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Oslo\"}"}}`),
		// A finishes first, while B is still open.
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sse("message_stop", `{"type":"message_stop"}`),
	}
	for _, ev := range events {
		_, _, _, _, err := p.Process(bytes.NewReader([]byte(ev)), false, nil)
		require.NoError(t, err, "event: %s", ev)
	}
}

// The OpenAI tool_calls index is dense across tool calls, while the
// Anthropic block index counts every block. Keying state by block index
// must not leak block indexes into the OpenAI stream.
func TestStreamToolRoutingOpenAIIndexesStayDense(t *testing.T) {
	chunks := feedEvents(t, []string{
		sse("message_start", toolRoutingMessageStart),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Checking."}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_A","name":"get_weather"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Oslo\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sse("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":2}`),
		sse("content_block_start", `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_B","name":"get_time"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":3}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	})

	args := argsByToolIndex(chunks)
	require.Len(t, args, 2)
	// OpenAI tool_calls indexes stay dense (0, 1) even though the Anthropic
	// block indexes were 1 and 3.
	require.JSONEq(t, `{"city":"Oslo"}`, args[0])
	require.JSONEq(t, `{"tz":"UTC"}`, args[1])
}

// A tool that takes no arguments never receives an input_json_delta; the
// accumulated OpenAI arguments must still be valid JSON.
func TestStreamZeroArgumentToolEmitsEmptyObject(t *testing.T) {
	chunks := feedEvents(t, []string{
		sse("message_start", toolRoutingMessageStart),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_A","name":"get_current_time","input":{}}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	})

	args := argsByToolIndex(chunks)
	require.JSONEq(t, "{}", args[0], "zero-argument tool must accumulate to valid JSON")
}
