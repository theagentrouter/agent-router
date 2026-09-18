// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"fmt"
	"testing"

	"github.com/openai/openai-go/v3"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	// fibPrompt represents a common code completion scenario for LoRA-tuned
	// code models like CodeLlama or Starcoder that are frequently deployed via
	// vLLM/llama.cpp
	fibPrompt = `def fib(n):
    if n <= 1:
        return n
    else:
        return fib(n-1) + fib(n-2)`
	// fibPromptPartial mimics mid-edit autocomplete.
	fibPromptPartial = `def fib(n):
    if n <= 1:
        return n
    else:`
	// harmonicPrompt is a code completion example that strconv.Unquote cannot handle.
	harmonicPrompt = `def h(n):
    if n<=1:
        return 1
    else:
        return h(n-1) + 1 / n`
	// embeddingPrompt represents a common embeddings request.
	embeddingPrompt = "How do I reset my password?"
)

var (
	// fibPromptTokens is fibPrompt in cl100k_base tokens.
	fibPromptTokens = []int64{755, 16178, 1471, 997, 262, 422, 308, 2717, 220, 16, 512, 286, 471, 308, 198, 262, 775, 512, 286, 471, 16178, 1471, 12, 16, 8, 489, 16178, 1471, 12, 17, 8}
	// fibPromptPartialTokens is fibPromptPartial in cl100k_base tokens.
	fibPromptPartialTokens = []int64{755, 16178, 1471, 997, 262, 422, 308, 2717, 220, 16, 512, 286, 471, 308, 198, 262, 775, 25}
	// embeddingTokens represents "How do I reset my password?" in cl100k_base tokens
	// From testopenai.CassetteEmbeddingsTokens
	embeddingTokens = []int64{4438, 656, 358, 7738, 856, 3636, 30}
	// embeddingMixedBatch represents a batch of embeddings with different languages
	// From testopenai.CassetteEmbeddingsMixedBatch
	embeddingMixedBatch = []string{
		"Hello 世界! 🌍",    // Mixed scripts and emoji
		"Здравствуй мир", // Cyrillic
		"مرحبا بالعالم",  // Arabic
		"你好世界",           // Chinese
		"नमस्ते दुनिया",  // Devanagari/Hindi
	}

	promptUnionBenchmarkCases = []struct {
		name     string
		data     []byte
		expected any
	}{
		{
			name:     "string", // From testopenai.CassetteCompletionBasic
			data:     []byte(`"def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)"`),
			expected: fibPrompt,
		},
		{
			name:     "string with escaped newline",
			data:     []byte(`"def h(n):\n    if n<=1:\n        return 1\n    else:\n        return h(n-1) + 1 \/ n"`),
			expected: harmonicPrompt,
		},
		{
			name:     "[]int64", // From testopenai.CassetteCompletionToken
			data:     []byte(`[755,16178,1471,997,262,422,308,2717,220,16,512,286,471,308,198,262,775,512,286,471,16178,1471,12,16,8,489,16178,1471,12,17,8]`),
			expected: fibPromptTokens,
		},
		{
			name: "[]string", // From testopenai.CassetteCompletionTextBatch
			data: []byte(`[
  "def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)",
  "def fib(n):\n    if n <= 1:\n        return n\n    else:"
]`),
			expected: []string{fibPrompt, fibPromptPartial},
		},
		{
			name: "[][]int64", // From testopenai.CassetteCompletionTokenBatch
			data: []byte(`[
  [755,16178,1471,997,262,422,308,2717,220,16,512,286,471,308,198,262,775,512,286,471,16178,1471,12,16,8,489,16178,1471,12,17,8],
  [755,16178,1471,997,262,422,308,2717,220,16,512,286,471,308,198,262,775,25]
]`),
			expected: [][]int64{fibPromptTokens, fibPromptPartialTokens},
		},
	}

	contentUnionBenchmarkCases = []struct {
		name     string
		data     []byte
		expected any
	}{
		{
			name:     "string",
			data:     []byte(`"def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)"`),
			expected: fibPrompt,
		},
		{
			name:     "string with escaped newline",
			data:     []byte(`"def h(n):\n    if n<=1:\n        return 1\n    else:\n        return h(n-1) + 1 \/ n"`),
			expected: harmonicPrompt,
		},
		{
			name: "[]ChatCompletionContentPartTextParam", // forged practice
			data: []byte(`[
	{
	  "text": "You are a helpful developer",
	  "type": "text"
	},
	{
	  "text": "You are an unhelpful developer",
	  "type": "text"
	}
]`),
			expected: []ChatCompletionContentPartTextParam{
				{
					Type: string(ChatCompletionContentPartTextTypeText),
					Text: "You are a helpful developer",
				},
				{
					Type: string(ChatCompletionContentPartTextTypeText),
					Text: "You are an unhelpful developer",
				},
			},
		},
	}

	embeddingRequestInputBenchmarkCases = []struct {
		name     string
		data     []byte
		expected any
	}{
		{
			name:     "string", // From testopenai.CassetteEmbeddingsBasic
			data:     []byte(`"How do I reset my password?"`),
			expected: embeddingPrompt,
		},
		{
			name:     "string with escaped", // String with escaped characters
			data:     []byte(`"The quick brown fox jumps over the \"lazy\" dog"`),
			expected: `The quick brown fox jumps over the "lazy" dog`,
		},
		{
			name:     "[]int64", // From testopenai.CassetteEmbeddingsTokens
			data:     []byte(`[4438,656,358,7738,856,3636,30]`),
			expected: embeddingTokens,
		},
		{
			name: "[]string", // From testopenai.CassetteEmbeddingsMixedBatch
			data: []byte(`[
  "Hello 世界! 🌍",
  "Здравствуй мир",
  "مرحبا بالعالم",
  "你好世界",
  "नमस्ते दुनिया"
]`),
			expected: embeddingMixedBatch,
		},
	}
)

func BenchmarkUnmarshalPromptUnion(b *testing.B) {
	for _, tc := range promptUnionBenchmarkCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.data)))

			// Validate once that it works correctly
			var p PromptUnion
			err := p.UnmarshalJSON(tc.data)
			if err != nil {
				b.Fatalf("failed to unmarshal %s: %v", tc.name, err)
			}
			val := p.Value

			// Validate type
			switch tc.name {
			case "string", "string with escaped newline":
				if _, ok := val.(string); !ok {
					b.Fatalf("expected string type for %s", tc.name)
				}
			case "[]string":
				if _, ok := val.([]string); !ok {
					b.Fatalf("expected []string type for %s", tc.name)
				}
			case "[]int64":
				if _, ok := val.([]int64); !ok {
					b.Fatalf("expected []int type for %s", tc.name)
				}
			case "[][]int64":
				if _, ok := val.([][]int64); !ok {
					b.Fatalf("expected [][]int type for %s", tc.name)
				}
			}
			b.Run("current", func(b *testing.B) {
				for b.Loop() {
					var p PromptUnion
					_ = p.UnmarshalJSON(tc.data)
				}
			})
			b.Run("naive", func(b *testing.B) {
				for b.Loop() {
					_, _ = unmarshalJSONPromptUnionNaive(tc.data)
				}
			})
			b.Run("openai-go", func(b *testing.B) {
				for b.Loop() {
					var u openai.CompletionNewParamsPromptUnion
					_ = u.UnmarshalJSON(tc.data)
				}
			})
		})
	}
}

func unmarshalJSONPromptUnionNaive(data []byte) (any, error) {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s, nil
	}

	// Try to determine what kind of array it is
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("prompt must be string, []string, []int64, or [][]int64")
	}

	if len(raw) == 0 {
		return []string{}, nil
	}

	// Check if first element is a number
	var num int
	if err := json.Unmarshal(raw[0], &num); err == nil {
		// It's []int64
		var ints []int64
		if err := json.Unmarshal(data, &ints); err == nil {
			return ints, nil
		}
	}

	// Check if first element is an array
	var arr []int
	if err := json.Unmarshal(raw[0], &arr); err == nil {
		// It's [][]int64
		var intArrs [][]int64
		if err := json.Unmarshal(data, &intArrs); err == nil {
			return intArrs, nil
		}
	}

	// Default to []string
	var strs []string
	if err := json.Unmarshal(data, &strs); err == nil {
		return strs, nil
	}

	return nil, fmt.Errorf("prompt must be string, []string, []int64, or [][]int64")
}

func BenchmarkUnmarshalContentUnion(b *testing.B) {
	for _, tc := range contentUnionBenchmarkCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.data)))

			// Validate once that it works correctly
			var p ContentUnion
			err := p.UnmarshalJSON(tc.data)
			if err != nil {
				b.Fatalf("failed to unmarshal %s: %v", tc.name, err)
			}
			val := p.Value

			// Validate type
			switch tc.name {
			case "string", "string with escaped newline":
				if _, ok := val.(string); !ok {
					b.Fatalf("expected string type for %s", tc.name)
				}
			case "[]ChatCompletionContentPartTextParam":
				if _, ok := val.([]ChatCompletionContentPartTextParam); !ok {
					b.Fatalf("expected []ChatCompletionContentPartTextParam type for %s", tc.name)
				}
			}
			b.Run("current", func(b *testing.B) {
				for b.Loop() {
					var p ContentUnion
					_ = p.UnmarshalJSON(tc.data)
				}
			})
			b.Run("naive", func(b *testing.B) {
				for b.Loop() {
					_, _ = unmarshalJSONContentUnionNaive(tc.data)
				}
			})
			b.Run("openai-go", func(b *testing.B) {
				for b.Loop() {
					var u openai.ChatCompletionSystemMessageParamContentUnion
					_ = u.UnmarshalJSON(tc.data)
				}
			})
		})
	}
}

func unmarshalJSONContentUnionNaive(data []byte) (any, error) {
	var str string
	err := json.Unmarshal(data, &str)
	if err == nil {
		return str, nil
	}

	// Try to unmarshal as array of ChatCompletionContentPartTextParam.
	var arr []ChatCompletionContentPartTextParam
	err = json.Unmarshal(data, &arr)
	if err == nil {
		return arr, nil
	}

	return nil, fmt.Errorf("cannot unmarshal JSON data as string or array of ChatCompletionContentPartTextParam")
}

func BenchmarkUnmarshalEmbeddingRequestInput(b *testing.B) {
	for _, tc := range embeddingRequestInputBenchmarkCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.data)))

			// Validate once that it works correctly
			var e EmbeddingRequestInput
			err := e.UnmarshalJSON(tc.data)
			if err != nil {
				b.Fatalf("failed to unmarshal %s: %v", tc.name, err)
			}
			val := e.Value

			// Validate type
			switch tc.name {
			case "string", "string with escaped", "large string":
				if _, ok := val.(string); !ok {
					b.Fatalf("expected string type for %s", tc.name)
				}
			case "[]string":
				if _, ok := val.([]string); !ok {
					b.Fatalf("expected []string type for %s", tc.name)
				}
			case "[]int64":
				if _, ok := val.([]int64); !ok {
					b.Fatalf("expected []int64 type for %s", tc.name)
				}
			}

			b.Run("current", func(b *testing.B) {
				for b.Loop() {
					var e EmbeddingRequestInput
					_ = e.UnmarshalJSON(tc.data)
				}
			})
			b.Run("naive", func(b *testing.B) {
				for b.Loop() {
					_, _ = unmarshalJSONEmbeddingRequestInputNaive(tc.data)
				}
			})
			b.Run("openai-go", func(b *testing.B) {
				for b.Loop() {
					var u openai.EmbeddingNewParamsInputUnion
					_ = u.UnmarshalJSON(tc.data)
				}
			})
		})
	}
}

func unmarshalJSONEmbeddingRequestInputNaive(data []byte) (any, error) {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s, nil
	}

	// Try to determine what kind of array it is
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("input must be string, []string, or []int64")
	}

	if len(raw) == 0 {
		return []string{}, nil
	}

	// Check if first element is a number
	var num int64
	if err := json.Unmarshal(raw[0], &num); err == nil {
		// It's []int64
		var ints []int64
		if err := json.Unmarshal(data, &ints); err == nil {
			return ints, nil
		}
	}

	// Default to []string
	var strs []string
	if err := json.Unmarshal(data, &strs); err == nil {
		return strs, nil
	}

	return nil, fmt.Errorf("input must be string, []string, or []int64")
}
