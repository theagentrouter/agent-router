// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// parseSSEToChunks converts raw SSE data to a slice of ChatCompletionResponseChunk objects.
func parseSSEToChunks(t *testing.T, sseData string) []*openai.ChatCompletionResponseChunk {
	var chunks []*openai.ChatCompletionResponseChunk

	lines := strings.Split(sseData, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		var chunk openai.ChatCompletionResponseChunk
		err := json.Unmarshal([]byte(jsonData), &chunk)
		require.NoError(t, err)
		chunks = append(chunks, &chunk)
	}

	return chunks
}

func TestConvertSSEToJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "empty input",
		},
		{
			name: "response with escaped content",
			input: `data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"content":"Line 1\\nLine 2\\t\\\"quoted\\\""}}]}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}

data: [DONE]
`,
			expected: `{"id":"test","choices":[{"finish_reason":"stop","index":0,"message":{"content":"Line 1\\nLine 2\\t\\\"quoted\\\""}}],"created":123,"model":"test","object":"chat.completion.chunk","usage":{"completion_tokens":2,"prompt_tokens":1,"total_tokens":3}}`,
		},
		{
			name: "minimal response without optional fields",
			input: `data: {"id":"123","object":"chat.completion.chunk","created":456,"model":"gpt-4.1-nano","choices":[{"delta":{"content":"Hi"}}]}

data: [DONE]
`,
			expected: `{"id":"123","choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hi"}}],"created":456,"model":"gpt-4.1-nano","object":"chat.completion.chunk"}`,
		},
		{
			name: "response with usage details",
			input: `data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"content":"Hi"}}]}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]
`,
			expected: `{"id":"test","choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hi"}}],"created":123,"model":"test","object":"chat.completion.chunk","usage":{"completion_tokens":1,"prompt_tokens":5,"total_tokens":6,"completion_tokens_details":{},"prompt_tokens_details":{}}}`,
		},
		{
			name: "response with unicode escapes",
			input: `data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"content":"Hello \\u0048\\u0065\\u006c\\u006c\\u006f"}}]}

data: [DONE]
`,
			expected: `{"id":"test","choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hello \\u0048\\u0065\\u006c\\u006c\\u006f"}}],"created":123,"model":"test","object":"chat.completion.chunk"}`,
		},
		{
			name: "response with annotations",
			input: `data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"role":"assistant","content":"Check out"}}]}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"content":" httpbin.org"}}]}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"annotations":[{"type":"url_citation","url_citation":{"end_index":21,"start_index":10,"title":"httpbin.org","url":"https://httpbin.org"}}]},"finish_reason":"stop"}]}

data: [DONE]
`,
			expected: `{"id":"test","choices":[{"finish_reason":"stop","index":0,"message":{"content":"Check out httpbin.org","role":"assistant","annotations":[{"type":"url_citation","url_citation":{"end_index":21,"start_index":10,"url":"https://httpbin.org","title":"httpbin.org"}}]}}],"created":123,"model":"test","object":"chat.completion.chunk"}`,
		},
		{
			name: "response with obfuscation field preserves last value",
			input: `data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"role":"assistant","content":"Hello"}}],"obfuscation":"abc123"}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"content":" world"}}],"obfuscation":"def456"}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{},"finish_reason":"stop"}],"obfuscation":"XQdNwPP14L3TH0"}

data: [DONE]
`,
			expected: `{"id":"test","choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hello world","role":"assistant"}}],"created":123,"model":"test","object":"chat.completion.chunk","obfuscation":"XQdNwPP14L3TH0"}`,
		},
		{
			name: "response with finish_reason length",
			input: `data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[{"delta":{},"finish_reason":"length"}]}

data: {"id":"test","object":"chat.completion.chunk","created":123,"model":"test","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":100,"total_tokens":105}}

data: [DONE]
`,
			expected: `{"id":"test","choices":[{"finish_reason":"length","index":0,"message":{"content":"","role":"assistant"}}],"created":123,"model":"test","object":"chat.completion.chunk","usage":{"completion_tokens":100,"prompt_tokens":5,"total_tokens":105}}`,
		},
		{
			// Azure OpenAI / AI Foundry sends a leading content-filter
			// prologue chunk with an empty model; the model must come from
			// the first chunk that actually carries one.
			name: "model from first non-empty chunk (Azure content-filter prologue)",
			input: `data: {"id":"","object":"","created":0,"model":"","choices":[]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":123,"model":"gpt-5.4-nano-2026-03-17","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":123,"model":"gpt-5.4-nano-2026-03-17","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}

data: [DONE]
`,
			expected: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hi","role":"assistant"}}],"created":0,"model":"gpt-5.4-nano-2026-03-17","object":"chat.completion.chunk","usage":{"completion_tokens":1,"prompt_tokens":5,"total_tokens":6}}`,
		},
		{
			// A later chunk reporting a different model (e.g. a routed
			// fallback) must not override the first non-empty value.
			name: "model retains first non-empty value even if a later chunk differs",
			input: `data: {"id":"","object":"","created":0,"model":"","choices":[]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":123,"model":"gpt-first","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":123,"model":"gpt-second-should-not-win","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}

data: [DONE]
`,
			expected: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hi","role":"assistant"}}],"created":0,"model":"gpt-first","object":"chat.completion.chunk","usage":{"completion_tokens":1,"prompt_tokens":5,"total_tokens":6}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertSSEToJSON(parseSSEToChunks(t, tt.input))

			if tt.expected != "" {
				resultJSON, err := json.Marshal(result)
				require.NoError(t, err)

				var expectedObj, resultObj interface{}
				err = json.Unmarshal([]byte(tt.expected), &expectedObj)
				require.NoError(t, err)
				err = json.Unmarshal(resultJSON, &resultObj)
				require.NoError(t, err)

				require.Equal(t, expectedObj, resultObj)
			} else {
				require.NotNil(t, result)
			}
		})
	}
}
