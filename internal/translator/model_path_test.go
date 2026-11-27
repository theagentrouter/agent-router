// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func requestPath(t *testing.T, hs []internalapi.Header) string {
	t.Helper()
	for _, h := range hs {
		if h.Key() == pathHeaderName {
			return h.Value()
		}
	}
	t.Fatal("no path header")
	return ""
}

func TestModelPathSegment(t *testing.T) {
	for _, m := range []string{"", ".", ".."} {
		_, err := modelPathSegment(m)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody, "model %q", m)
	}
	for in, want := range map[string]string{
		"gpt-4o":                   "gpt-4o",
		"anthropic.claude-v2:0":    "anthropic.claude-v2:0",
		"claude-3-5-sonnet@2024":   "claude-3-5-sonnet@2024",
		"a/../b?x=1#":              "a%2F..%2Fb%3Fx=1%23",
		"arn:aws:bedrock:us:1:x/y": "arn:aws:bedrock:us:1:x%2Fy",
		"...":                      "...",
	} {
		got, err := modelPathSegment(in)
		require.NoError(t, err)
		require.Equal(t, want, got, "model %q", in)
	}
}

func TestBuildGCPModelPathSuffix_EscapesModel(t *testing.T) {
	p, err := buildGCPModelPathSuffix(gcpModelPublisherGoogle, "gemini-2.5-pro", gcpMethodGenerateContent)
	require.NoError(t, err)
	require.Equal(t, "publishers/google/models/gemini-2.5-pro:generateContent", p)

	p, err = buildGCPModelPathSuffix(gcpModelPublisherGoogle, "gemini-2.5-pro", gcpMethodStreamGenerateContent, "alt=sse")
	require.NoError(t, err)
	require.Equal(t, "publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse", p)

	p, err = buildGCPModelPathSuffix(gcpModelPublisherAnthropic, "claude-sonnet-4@20250514", gcpMethodRawPredict)
	require.NoError(t, err)
	require.Equal(t, "publishers/anthropic/models/claude-sonnet-4@20250514:rawPredict", p)

	p, err = buildGCPModelPathSuffix(gcpModelPublisherGoogle, "gemini-2.5-pro:countTokens?x=1#", gcpMethodGenerateContent)
	require.NoError(t, err)
	require.Equal(t, "publishers/google/models/gemini-2.5-pro%3AcountTokens%3Fx=1%23:generateContent", p)

	_, err = buildGCPModelPathSuffix(gcpModelPublisherGoogle, "..", gcpMethodGenerateContent)
	require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
}

// TestModelIsOneUpstreamPathSegment checks that the client-supplied model never changes
// the structure (segments, query, fragment) of the upstream path.
func TestModelIsOneUpstreamPathSegment(t *testing.T) {
	user := []openai.ChatCompletionMessageParamUnion{{OfUser: &openai.ChatCompletionUserMessageParam{
		Content: openai.StringOrUserRoleContentUnion{Value: "hi"}, Role: openai.ChatMessageRoleUser,
	}}}

	t.Run("azure chat", func(t *testing.T) {
		tr := NewChatCompletionOpenAIToAzureOpenAITranslator("2025-01-01-preview", "")
		hs, _, err := tr.RequestBody([]byte(`{}`), &openai.ChatCompletionRequest{Model: "gpt-4o/../other?api-version=2020-01-01#"}, false)
		require.NoError(t, err)
		require.Equal(t, "/openai/deployments/gpt-4o%2F..%2Fother%3Fapi-version=2020-01-01%23/chat/completions?api-version=2025-01-01-preview", requestPath(t, hs))

		hs, _, err = tr.RequestBody([]byte(`{}`), &openai.ChatCompletionRequest{Model: "gpt-4o"}, false)
		require.NoError(t, err)
		require.Equal(t, "/openai/deployments/gpt-4o/chat/completions?api-version=2025-01-01-preview", requestPath(t, hs))

		_, _, err = tr.RequestBody([]byte(`{}`), &openai.ChatCompletionRequest{Model: ".."}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
	t.Run("azure chat override is used", func(t *testing.T) {
		tr := NewChatCompletionOpenAIToAzureOpenAITranslator("v", "my-deployment")
		hs, _, err := tr.RequestBody([]byte(`{}`), &openai.ChatCompletionRequest{Model: "../x?y"}, false)
		require.NoError(t, err)
		require.Equal(t, "/openai/deployments/my-deployment/chat/completions?api-version=v", requestPath(t, hs))
	})
	t.Run("azure embeddings", func(t *testing.T) {
		tr := NewEmbeddingOpenAIToAzureOpenAITranslator("v", "")
		hs, _, err := tr.RequestBody([]byte(`{}`), &openai.EmbeddingRequest{Model: "victim/chat/completions?x="}, false)
		require.NoError(t, err)
		require.Equal(t, "/openai/deployments/victim%2Fchat%2Fcompletions%3Fx=/embeddings?api-version=v", requestPath(t, hs))

		_, _, err = tr.RequestBody([]byte(`{}`), &openai.EmbeddingRequest{Model: "."}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
	t.Run("vertex gemini chat", func(t *testing.T) {
		tr := NewChatCompletionOpenAIToGCPVertexAITranslator("")
		hs, _, err := tr.RequestBody(nil, &openai.ChatCompletionRequest{Model: "gemini-2.5-pro:countTokens?x=1#", Messages: user}, false)
		require.NoError(t, err)
		require.Equal(t, "publishers/google/models/gemini-2.5-pro%3AcountTokens%3Fx=1%23:generateContent", requestPath(t, hs))

		_, _, err = tr.RequestBody(nil, &openai.ChatCompletionRequest{Model: "..", Messages: user}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
	t.Run("vertex anthropic messages", func(t *testing.T) {
		tr := NewAnthropicToGCPAnthropicTranslator("vertex-2023-10-16", "")
		model := "x/../../../endpoints/1234:predict?"
		body := []byte(`{"model":"` + model + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
		hs, _, err := tr.RequestBody(body, &anthropic.MessagesRequest{Model: model, MaxTokens: 1}, false)
		require.NoError(t, err)
		require.Equal(t, "publishers/anthropic/models/x%2F..%2F..%2F..%2Fendpoints%2F1234%3Apredict%3F:rawPredict", requestPath(t, hs))
	})
	t.Run("bedrock rejects dot segments", func(t *testing.T) {
		tr := NewChatCompletionOpenAIToAWSBedrockTranslator("")
		_, _, err := tr.RequestBody(nil, &openai.ChatCompletionRequest{Model: "..", Messages: user}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)

		hs, _, err := tr.RequestBody(nil, &openai.ChatCompletionRequest{Model: "anthropic.claude-v2:0", Messages: user}, false)
		require.NoError(t, err)
		require.Equal(t, "/model/anthropic.claude-v2:0/converse", requestPath(t, hs))
	})
	t.Run("bedrock embeddings rejects dot segments", func(t *testing.T) {
		tr := NewEmbeddingOpenAIToAWSBedrockTranslator("")
		_, _, err := tr.RequestBody([]byte(`{}`), &openai.EmbeddingRequest{Model: ".."}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
}
