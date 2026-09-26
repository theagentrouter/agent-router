// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopenai

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

func TestExtractModel(t *testing.T) {
	tests := []struct {
		name     string
		request  any
		expected string
	}{
		{
			name:     "chat completion request",
			request:  &openai.ChatCompletionRequest{Model: "gpt-4"},
			expected: "gpt-4",
		},
		{
			name:     "completion request",
			request:  &openai.CompletionRequest{Model: "gpt-3.5-turbo"},
			expected: "gpt-3.5-turbo",
		},
		{
			name:     "embeddings request",
			request:  &openai.EmbeddingRequest{Model: "text-embedding-ada-002"},
			expected: "text-embedding-ada-002",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractModel(tc.request)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestExtractModelFromBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "chat completion body",
			body:     `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`,
			expected: "gpt-4",
		},
		{
			name:     "completion body",
			body:     `{"model":"gpt-3.5-turbo","prompt":"test"}`,
			expected: "gpt-3.5-turbo",
		},
		{
			name:     "embeddings body",
			body:     `{"model":"text-embedding-ada-002","input":"test"}`,
			expected: "text-embedding-ada-002",
		},
		{
			name:     "missing model field",
			body:     `{"messages":[]}`,
			expected: "",
		},
		{
			name:     "invalid JSON",
			body:     `{invalid json}`,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractModelFromBody(tc.body)
			require.Equal(t, tc.expected, result)
		})
	}
}
