// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestInteractionsGCPVertexAITranslator_RequestBody(t *testing.T) {
	const agentBody = `{"agent":"antigravity","input":"hi","store":false}`
	for _, tc := range []struct {
		name     string
		override string
		raw      string
		req      gcp.InteractionsRequest
		force    bool
		wantBody string // "" means the body is forwarded unchanged.
	}{
		{
			name: "no override",
			raw:  agentBody,
			req:  gcp.InteractionsRequest{Agent: "antigravity"},
		},
		{
			name:     "agent override",
			override: "antigravity-preview-09-2026",
			raw:      agentBody,
			req:      gcp.InteractionsRequest{Agent: "antigravity"},
			wantBody: `{"agent":"antigravity-preview-09-2026","input":"hi","store":false}`,
		},
		{
			name:     "model override",
			override: "gemini-4-hydrogen",
			raw:      `{"model":"hydrogen","agent":"a","input":"hi"}`,
			req:      gcp.InteractionsRequest{Model: "hydrogen", Agent: "a"},
			wantBody: `{"model":"gemini-4-hydrogen","agent":"a","input":"hi"}`,
		},
		{
			name:     "forced body mutation sends the original body",
			raw:      agentBody,
			req:      gcp.InteractionsRequest{Agent: "antigravity"},
			force:    true,
			wantBody: agentBody,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewInteractionsGCPVertexAITranslator(tc.override)
			headers, body, err := tr.RequestBody([]byte(tc.raw), &tc.req, tc.force)
			require.NoError(t, err)
			require.Equal(t, internalapi.Header{":path", "interactions"}, headers[0])
			if tc.wantBody == "" {
				require.Nil(t, body)
				require.Len(t, headers, 1)
				return
			}
			require.JSONEq(t, tc.wantBody, string(body))
			require.Equal(t, []internalapi.Header{{":path", "interactions"}, {"content-length", strconv.Itoa(len(body))}}, headers)
		})
	}
}

func TestInteractionsGCPVertexAITranslator_ResponseBody(t *testing.T) {
	t.Run("non-streaming", func(t *testing.T) {
		tr := NewInteractionsGCPVertexAITranslator("")
		_, _, err := tr.RequestBody([]byte(`{"agent":"a"}`), &gcp.InteractionsRequest{Agent: "a"}, false)
		require.NoError(t, err)
		body := `{"id":"v1_x","status":"completed","agent":"antigravity-preview-09-2026","usage":{"total_input_tokens":100,"total_output_tokens":30,"total_thought_tokens":5}}`
		headers, newBody, usage, model, err := tr.ResponseBody(nil, strings.NewReader(body), true, nil)
		require.NoError(t, err)
		require.Nil(t, headers)
		require.Nil(t, newBody, "the body is forwarded unchanged")
		require.Equal(t, "antigravity-preview-09-2026", model)
		in, _ := usage.InputTokens()
		require.Equal(t, uint32(100), in)
		out, _ := usage.OutputTokens()
		require.Equal(t, uint32(35), out)
	})

	t.Run("response model falls back to the request", func(t *testing.T) {
		tr := NewInteractionsGCPVertexAITranslator("gemini-4-hydrogen")
		_, _, err := tr.RequestBody([]byte(`{"model":"hydrogen"}`), &gcp.InteractionsRequest{Model: "hydrogen"}, false)
		require.NoError(t, err)
		_, _, usage, model, err := tr.ResponseBody(nil, strings.NewReader(`{"id":"v1_x","status":"completed"}`), true, nil)
		require.NoError(t, err)
		require.Equal(t, "gemini-4-hydrogen", model)
		require.True(t, usage.IsZero())
	})

	t.Run("streaming", func(t *testing.T) {
		tr := NewInteractionsGCPVertexAITranslator("")
		_, _, err := tr.RequestBody([]byte(`{"agent":"a","stream":true}`), &gcp.InteractionsRequest{Agent: "a", Stream: true}, false)
		require.NoError(t, err)

		created := "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_s\",\"status\":\"in_progress\",\"agent\":\"antigravity-preview-09-2026\"}}\n\n"
		step := "data: {\"event_type\":\"step.stop\",\"index\":0,\"usage\":{\"total_input_tokens\":40,\"total_output_tokens\":4},\"step_usage\":{\"total_input_tokens\":1}}\r\n\r\n"
		delta := "data: {\"event_type\":\"step.delta\",\"index\":1,\"delta\":{\"type\":\"text\",\"text\":\"Hello\"}}\n\n"
		completed := "data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"v1_s\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":50,\"total_output_tokens\":10}}}"
		stream := created + step + delta + completed

		var last struct {
			in, out uint32
			model   string
		}
		cuts := []int{7, len(created) + 10, len(created) + len(step), len(stream)}
		prev := 0
		for i, cut := range cuts {
			headers, newBody, usage, model, err := tr.ResponseBody(nil, bytes.NewReader([]byte(stream[prev:cut])), i == len(cuts)-1, nil)
			require.NoError(t, err)
			require.Nil(t, headers)
			require.Nil(t, newBody, "chunks are forwarded unchanged")
			last.in, _ = usage.InputTokens()
			last.out, _ = usage.OutputTokens()
			last.model = model
			if i == 2 {
				// The step.stop event is complete: its cumulative usage is recorded, not step_usage.
				require.Equal(t, uint32(40), last.in)
			}
			prev = cut
		}
		// The final event has no trailing separator and is read at the end of the stream.
		require.Equal(t, uint32(50), last.in)
		require.Equal(t, uint32(10), last.out)
		require.Equal(t, "antigravity-preview-09-2026", last.model)
	})
}

func TestInteractionsGCPVertexAITranslator_PassThrough(t *testing.T) {
	tr := NewInteractionsGCPVertexAITranslator("")
	headers, err := tr.ResponseHeaders(map[string]string{":status": "200"})
	require.NoError(t, err)
	require.Nil(t, headers)
	headers, body, err := tr.ResponseError(map[string]string{":status": "400"}, strings.NewReader(`{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`))
	require.NoError(t, err)
	require.Nil(t, headers)
	require.Nil(t, body, "errors keep the Google error format")
}

func TestNextSSEEventEnd(t *testing.T) {
	for _, tc := range []struct {
		buf        string
		end, seLen int
	}{
		{"data: a", -1, 0},
		{"data: a\n\ndata: b\r\n\r\n", 7, 2},
		{"data: a\r\n\r\ndata: b\n\n", 7, 4},
	} {
		end, sepLen := nextSSEEventEnd([]byte(tc.buf))
		require.Equal(t, tc.end, end, tc.buf)
		require.Equal(t, tc.seLen, sepLen, tc.buf)
	}
}

func TestInteractionsUsage(t *testing.T) {
	t.Run("all fields", func(t *testing.T) {
		u, ok := interactionsUsageOf(`{"total_input_tokens":100,"total_tool_use_tokens":20,"total_output_tokens":30,"total_thought_tokens":7,"total_cached_tokens":40,"total_tokens":157}`)
		require.True(t, ok)
		in, set := u.InputTokens()
		require.True(t, set)
		require.Equal(t, uint32(120), in)
		out, set := u.OutputTokens()
		require.True(t, set)
		require.Equal(t, uint32(37), out)
		reasoning, set := u.ReasoningTokens()
		require.True(t, set)
		require.Equal(t, uint32(7), reasoning)
		cached, set := u.CachedInputTokens()
		require.True(t, set)
		require.Equal(t, uint32(40), cached)
		total, set := u.TotalTokens()
		require.True(t, set)
		require.Equal(t, uint32(157), total)
	})

	t.Run("partial fields leave the rest unset", func(t *testing.T) {
		u, ok := interactionsUsageOf(`{"total_input_tokens":5,"total_output_tokens":3}`)
		require.True(t, ok)
		in, _ := u.InputTokens()
		require.Equal(t, uint32(5), in)
		out, _ := u.OutputTokens()
		require.Equal(t, uint32(3), out)
		_, set := u.ReasoningTokens()
		require.False(t, set)
		_, set = u.CachedInputTokens()
		require.False(t, set)
		_, set = u.TotalTokens()
		require.False(t, set)
	})

	for name, raw := range map[string]string{
		"missing":          ``,
		"not an object":    `"usage"`,
		"no token counts":  `{"input_tokens_by_modality":[]}`,
		"negative count":   `{"total_input_tokens":-1}`,
		"non-number count": `{"total_input_tokens":"10"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := interactionsUsageOf(raw)
			require.False(t, ok)
		})
	}
}

// interactionsUsageOf parses raw and runs InteractionsUsage on it.
func interactionsUsageOf(raw string) (metrics.TokenUsage, bool) {
	usage := gjson.Parse(raw)
	return InteractionsUsage(&usage)
}
