// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestDecisionsOpenAIToOpenAITranslator_RequestBody(t *testing.T) {
	t.Run("passthrough", func(t *testing.T) {
		tr := NewDecisionsOpenAIToOpenAITranslator("v1", "").(*openAIToOpenAITranslatorV1Decisions)
		original := []byte(`{"model":"gpt-6-luna","input":"hello","questions":[]}`)
		headers, body, err := tr.RequestBody(original, &openai.DecisionRequest{Model: "gpt-6-luna"}, false)
		require.NoError(t, err)
		require.Equal(t, []string{":path", "/v1/decisions"}, []string{headers[0].Key(), headers[0].Value()})
		require.Nil(t, body)
		require.Equal(t, "gpt-6-luna", tr.requestModel)
	})

	t.Run("custom prefix and model override", func(t *testing.T) {
		tr := NewDecisionsOpenAIToOpenAITranslator("custom/v1", "provider-luna").(*openAIToOpenAITranslatorV1Decisions)
		original := []byte(`{"model":"gpt-6-luna","input":"hello","questions":[]}`)
		headers, body, err := tr.RequestBody(original, &openai.DecisionRequest{Model: "gpt-6-luna"}, false)
		require.NoError(t, err)
		require.Equal(t, "/custom/v1/decisions", headers[0].Value())
		require.Equal(t, contentLengthHeaderName, headers[1].Key())
		require.Equal(t, strconv.Itoa(len(body)), headers[1].Value())
		require.JSONEq(t, `{"model":"provider-luna","input":"hello","questions":[]}`, string(body))
		require.Equal(t, "provider-luna", tr.requestModel)
	})

	t.Run("forced body mutation", func(t *testing.T) {
		tr := NewDecisionsOpenAIToOpenAITranslator("v1", "")
		original := []byte(`{"model":"gpt-6-luna","input":"hello","questions":[]}`)
		headers, body, err := tr.RequestBody(original, &openai.DecisionRequest{Model: "gpt-6-luna"}, true)
		require.NoError(t, err)
		require.Equal(t, original, body)
		require.Equal(t, strconv.Itoa(len(body)), headers[1].Value())
	})
}

func TestDecisionsOpenAIToOpenAITranslator_ResponseBody(t *testing.T) {
	tr := NewDecisionsOpenAIToOpenAITranslator("v1", "provider-luna").(*openAIToOpenAITranslatorV1Decisions)
	_, _, err := tr.RequestBody([]byte(`{"model":"gpt-6-luna"}`), &openai.DecisionRequest{Model: "gpt-6-luna"}, false)
	require.NoError(t, err)

	response := []byte(`{
		"model": "gpt-6-luna-2026-09-01",
		"answers": [
			{"type":"predicate","name":"relevant","probability":0},
			{"type":"choice","name":"approved","choice":true,"probabilities":[{"value":true,"probability":1}],"confidence":1},
			{"type":"score","name":"severity","score":1.1,"probabilities":[{"value":1,"label":"medium","probability":0.9}],"confidence":0.8},
			{"type":"refusal","name":"unsafe"}
		],
		"usage": {
			"input_tokens": 20,
			"input_tokens_details": {"cache_write_tokens": 3, "cached_tokens": 4},
			"output_tokens": 5,
			"output_tokens_details": {"reasoning_tokens": 2},
			"total_tokens": 25
		}
	}`)

	_, body, usage, responseModel, err := tr.ResponseBody(nil, bytes.NewReader(response), true, nil)
	require.NoError(t, err)
	require.Nil(t, body)
	require.Equal(t, "gpt-6-luna-2026-09-01", responseModel)
	inputTokens, hasInput := usage.InputTokens()
	require.True(t, hasInput)
	require.Equal(t, uint32(20), inputTokens)
	outputTokens, hasOutput := usage.OutputTokens()
	require.True(t, hasOutput)
	require.Equal(t, uint32(5), outputTokens)
	totalTokens, hasTotal := usage.TotalTokens()
	require.True(t, hasTotal)
	require.Equal(t, uint32(25), totalTokens)
	cachedTokens, hasCached := usage.CachedInputTokens()
	require.True(t, hasCached)
	require.Equal(t, uint32(4), cachedTokens)
	cacheWriteTokens, hasCacheWrite := usage.CacheCreationInputTokens()
	require.True(t, hasCacheWrite)
	require.Equal(t, uint32(3), cacheWriteTokens)
	reasoningTokens, hasReasoning := usage.ReasoningTokens()
	require.True(t, hasReasoning)
	require.Equal(t, uint32(2), reasoningTokens)

	var decoded openai.DecisionResponse
	require.NoError(t, json.Unmarshal(response, &decoded))
	require.Len(t, decoded.Answers, 4)
	require.NotNil(t, decoded.Answers[0].Probability)
	require.Zero(t, *decoded.Answers[0].Probability)
	require.JSONEq(t, `true`, string(decoded.Answers[1].Choice))
	require.JSONEq(t, `1`, string(decoded.Answers[2].Probabilities[0].Value))
}

func TestDecisionsOpenAIToOpenAITranslator_ResponseBody_FallsBackToRequestModelWithoutUsage(t *testing.T) {
	tr := NewDecisionsOpenAIToOpenAITranslator("v1", "provider-luna").(*openAIToOpenAITranslatorV1Decisions)
	_, _, err := tr.RequestBody([]byte(`{"model":"gpt-6-luna"}`), &openai.DecisionRequest{Model: "gpt-6-luna"}, false)
	require.NoError(t, err)

	_, _, usage, responseModel, err := tr.ResponseBody(nil, bytes.NewReader([]byte(`{"answers":[]}`)), true, nil)
	require.NoError(t, err)
	require.Equal(t, "provider-luna", responseModel)
	_, hasInput := usage.InputTokens()
	require.False(t, hasInput)
}

func TestDecisionsOpenAIToOpenAITranslator_ResponseError(t *testing.T) {
	tr := NewDecisionsOpenAIToOpenAITranslator("v1", "")
	body := []byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`)
	headers, mutated, err := tr.ResponseError(map[string]string{"content-type": "application/json", ":status": "400"}, bytes.NewReader(body))
	require.NoError(t, err)
	require.Nil(t, headers)
	require.Nil(t, mutated)
}
