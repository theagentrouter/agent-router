// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	stdjson "encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestPolicyTransportForEndpoints(t *testing.T) {
	require.Equal(t, policyTransportResponses, policyTransportFor[endpointspec.ResponsesEndpointSpec](filterapi.APISchemaOpenAI))
	require.Equal(t, policyTransportAnthropic, policyTransportFor[endpointspec.MessagesEndpointSpec](filterapi.APISchemaAnthropic))
	require.Equal(t, policyTransportChat, policyTransportFor[endpointspec.MessagesEndpointSpec](filterapi.APISchemaOpenAI))
	require.Equal(t, policyTransportChatBatch, policyTransportFor[endpointspec.ChatCompletionsBatchEndpointSpec](filterapi.APISchemaOpenAI))
	require.Equal(t, policyTransportNone, policyTransportFor[endpointspec.PassthroughEndpointSpec](filterapi.APISchemaCohere))
	// Operations without a prompt slot keep failing closed under a bound policy.
	require.Equal(t, policyTransportByField, policyTransportFor[endpointspec.CompletionsEndpointSpec](filterapi.APISchemaOpenAI))
	require.Equal(t, policyTransportByField, policyTransportFor[endpointspec.ChatCompletionsEndpointSpec](filterapi.APISchemaOpenAI))
}

func TestInferencePolicyResponsesWithoutInstructionsAddsThem(t *testing.T) {
	body := []byte(`{"model":"m","input":"Who are you?"}`)
	_, _, err := normalizeInferencePolicy(testInferencePolicyHeader(t, "append"), body)
	require.Error(t, err, "field-based detection cannot place the prompt")

	got, changed, err := normalizeInferencePolicyFor(policyTransportResponses, testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request map[string]any
	require.NoError(t, stdjson.Unmarshal(got, &request))
	require.Contains(t, request["instructions"], "served by Viettel AI")
	require.Equal(t, "Who are you?", request["input"])
}

func TestInferencePolicyAnthropicWithoutSystemSetsTopLevelSystem(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"Hi"}]}`)
	got, changed, err := normalizeInferencePolicyFor(policyTransportAnthropic, testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request map[string]any
	require.NoError(t, stdjson.Unmarshal(got, &request))
	require.Contains(t, request["system"], "served by Viettel AI")
	messages := request["messages"].([]any)
	require.Len(t, messages, 1, "Anthropic messages must not receive a system-role message")
	require.Equal(t, "user", messages[0].(map[string]any)["role"])
}

func TestInferencePolicyChatBatchAppliesToEveryConversation(t *testing.T) {
	body := []byte(`{"model":"m","messages":[[{"role":"user","content":"a"}],[{"role":"system","content":"client"},{"role":"user","content":"b"}]]}`)
	got, changed, err := normalizeInferencePolicyFor(policyTransportChatBatch, testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.True(t, changed)
	var request map[string]any
	require.NoError(t, stdjson.Unmarshal(got, &request))
	conversations := request["messages"].([]any)
	require.Len(t, conversations, 2)
	first := conversations[0].([]any)
	second := conversations[1].([]any)
	require.Len(t, first, 2)
	require.Len(t, second, 3)
	for _, conversation := range [][]any{first, second} {
		head := conversation[0].(map[string]any)
		require.Equal(t, "system", head["role"])
		require.Contains(t, head["content"], "served by Viettel AI")
	}

	_, _, err = normalizeInferencePolicyFor(policyTransportChatBatch, testInferencePolicyHeader(t, "append"), []byte(`{"messages":[{"role":"user","content":"x"}]}`))
	require.ErrorContains(t, err, "must be an array of messages")
}

func TestInferencePolicyPassthroughIsNoop(t *testing.T) {
	body := []byte(`{"model":"m","query":"q","documents":["a"]}`)
	got, changed, err := normalizeInferencePolicyFor(policyTransportNone, testInferencePolicyHeader(t, "append"), body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, got)
}

func TestProcessorForPathResolvesEndpointScopedOperations(t *testing.T) {
	s, err := NewServer(slog.Default(), false)
	require.NoError(t, err)
	s.config = &filterapi.RuntimeConfig{}
	registered := map[string]*mockProcessor{}
	for _, p := range []string{"/v1/chat/completions", "/v1/chat/completions/batch", "/v1/rerank", "/v1/score", "/tokenize", "/detokenize", "/v1/messages", "/anthropic/v1/messages"} {
		proc := &mockProcessor{}
		registered[p] = proc
		s.Register(p, func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
			return proc, nil
		})
	}
	for requestPath, want := range map[string]string{
		"/ep-1/v1/chat/completions/batch": "/v1/chat/completions/batch",
		"/ep-1/v1/chat/completions":       "/v1/chat/completions",
		"/ep-1/v1/rerank":                 "/v1/rerank",
		"/ep-1/v1/score?x=1":              "/v1/score",
		"/ep-1/detokenize":                "/detokenize",
		"/ep-1/tokenize":                  "/tokenize",
		"/v1/messages":                    "/v1/messages",
	} {
		got, err := s.processorForPath(map[string]string{":path": requestPath}, false, slog.Default())
		require.NoError(t, err, requestPath)
		require.Same(t, registered[want], got, requestPath)
	}
	_, err = s.processorForPath(map[string]string{":path": "/ep-1/metrics"}, false, slog.Default())
	require.ErrorIs(t, err, errNoProcessor)
}
