// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// TestWithTestUpstream tests the end-to-end functionality of the AI Gateway with the testupstream server.
func TestWithTestUpstream(t *testing.T) {
	const manifest = "testdata/testupstream.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=translation-testupstream"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	const dummyToken = "dummy-token"
	t.Run("/chat/completions", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			modelName string
			// expHost is the expected host that the request should be forwarded to the testupstream server.
			// Assertion will be performed in the testupstream server.
			expHost string
			// expTestUpstreamID is the expected testupstream ID that the request should be forwarded to.
			// This is used to differentiate between different testupstream instances.
			// Assertion will be performed in the testupstream server.
			expTestUpstreamID string
			// expPath is the expected path that the request should be forwarded to the testupstream server.
			// Assertion will be performed in the testupstream server.
			expPath string
			// fakeResponseBody is the body that the testupstream server will return when the request is made.
			fakeResponseBody string
			// expStatus is the expected HTTP status code for the test case.
			expStatus int
			// expResponseBody is the expected response body for the test case. This is optional and can be empty.
			expResponseBody string
			// nonexpectedHeaders are the headers that should NOT be present in the request to the testupstream server.
			nonexpectedHeaders []string
			// reqHeaders are the headers to be included in the request to the AI Gateway.
			reqHeaders map[string]string
		}{
			{
				name:              "openai",
				modelName:         "some-cool-model",
				expTestUpstreamID: "primary",
				expPath:           "/v1/chat/completions",
				expHost:           "testupstream.default.svc.cluster.local",
				fakeResponseBody:  `{"choices":[{"message":{"content":"This is a test."}}]}`,
				expStatus:         200,
			},
			{
				name:              "aws-bedrock",
				modelName:         "another-cool-model",
				expTestUpstreamID: "canary",
				expHost:           "testupstream-canary.default.svc.cluster.local",
				expPath:           "/model/another-cool-model/converse",
				fakeResponseBody:  `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
				expStatus:         200,
			},
			{
				name:            "openai",
				modelName:       "non-existent-model",
				expStatus:       404,
				expResponseBody: `No matching route found. It is likely because the model specified in your request is not configured in the Gateway.`,
			},
			{
				name:               "openai-header-mutation",
				modelName:          "some-cool-model",
				expTestUpstreamID:  "primary",
				expPath:            "/v1/chat/completions",
				expHost:            "testupstream.default.svc.cluster.local",
				fakeResponseBody:   `{"choices":[{"message":{"content":"This is a test."}}]}`,
				nonexpectedHeaders: []string{"x-remove-header"},
				reqHeaders:         map[string]string{"x-remove-header": "remove-me"},
				expStatus:          200,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				require.Eventually(t, func() bool {
					fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
					defer fwd.Kill()

					req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(
						`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`,
						tc.modelName)))
					require.NoError(t, err)
					req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.fakeResponseBody)))
					req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.expPath)))
					req.Header.Set(testupstreamlib.ExpectedHostKey, tc.expHost)
					req.Header.Set(testupstreamlib.ExpectedTestUpstreamIDKey, tc.expTestUpstreamID)
					for k, v := range tc.reqHeaders {
						req.Header.Set(k, v)
					}
					if tc.modelName == "some-cool-model" {
						req.Header.Set(testupstreamlib.ExpectedHeadersKey,
							base64.StdEncoding.EncodeToString([]byte("Authorization:Bearer "+dummyToken)))
					}

					if len(tc.nonexpectedHeaders) > 0 {
						req.Header.Set(testupstreamlib.NonExpectedRequestHeadersKey, base64.StdEncoding.EncodeToString([]byte(strings.Join(tc.nonexpectedHeaders, ","))))
					}

					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Logf("error: %v", err)
						return false
					}
					defer func() { _ = resp.Body.Close() }()
					body, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Logf("error reading response body: %v", err)
						return false
					}
					if resp.StatusCode != tc.expStatus {
						t.Logf("unexpected status code: %d (expected %d), body: %s", resp.StatusCode, tc.expStatus, body)
						return false
					}
					if tc.expResponseBody != "" && string(body) != tc.expResponseBody {
						t.Logf("unexpected response body: %s (expected %s)", body, tc.expResponseBody)
						return false
					}
					return true
				}, 10*time.Second, 1*time.Second)
			})
		}
	})

	t.Run("non-llm-route", func(t *testing.T) {
		// We should be able to make requests to /non-llm routes as well.
		//
		// If this route is intercepted by the AI Gateway ExtProc, which is unexpected, it would result in 404
		// since "/non-llm-route" is not a valid route at least for the OpenAI API.
		require.Eventually(t, func() bool {
			fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
			defer fwd.Kill()

			req, err := http.NewRequest(http.MethodGet, fwd.Address()+"/non-llm-route", strings.NewReader("somebody"))
			require.NoError(t, err)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Logf("error: %v", err)
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Logf("error reading response body: %v", err)
				return false
			}
			if resp.StatusCode != 200 {
				t.Logf("unexpected status code: %d (expected 200), body: %s", resp.StatusCode, body)
				return false
			}
			if string(body) != `{"message":"This is a non-LLM endpoint response"}` {
				t.Logf("unexpected response body: %s", body)
				return false
			}
			return true
		}, 10*time.Second, 1*time.Second)
	})

	// This is a regression test that ensures that stream=true requests are processed in a streaming manner.
	// https://github.com/envoyproxy/ai-gateway/pull/1026
	//
	// We have almost identical test in the tests/data-plane.
	t.Run("stream non blocking", func(t *testing.T) {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
		defer fwd.Kill()
		// This receives a stream of 20 event messages. The testuptream server sleeps 200 ms between each message.
		// Therefore, if envoy fails to process the response in a streaming manner, the test will fail taking more than 4 seconds.
		client := openaigo.NewClient(
			option.WithBaseURL(fwd.Address()+"/v1/"),
			option.WithHeader(testupstreamlib.ResponseTypeKey, "sse"),
			option.WithHeader(testupstreamlib.ResponseBodyHeaderKey,
				base64.StdEncoding.EncodeToString([]byte(
					`
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" This"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" is"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" a"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":"."},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" This"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" is"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" a"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":"."},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" This"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" is"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" a"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":"."},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" This"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" is"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" a"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":"."},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[],"usage":{"prompt_tokens":25,"completion_tokens":61,"total_tokens":86,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
[DONE]
`,
				))),
		)

		// NewStreaming below will block until the first event is received, so take the time before calling it.
		start := time.Now()
		stream := client.Chat.Completions.NewStreaming(t.Context(), openaigo.ChatCompletionNewParams{
			Messages: []openaigo.ChatCompletionMessageParamUnion{
				openaigo.UserMessage("Say this is a test"),
			},
			Model: "whatever-model",
		})

		defer func() {
			_ = stream.Close()
		}()

		asserted := false
		for stream.Next() {
			chunk := stream.Current()
			fmt.Println(chunk)
			if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.Content == "" {
				continue
			}
			t.Logf("%v: %v", time.Now(), chunk.Choices[0].Delta.Content)
			// Check each event is received less than a second after the previous one.
			require.Less(t, time.Since(start), time.Second)
			start = time.Now()
			asserted = true
		}
		require.NoError(t, stream.Err())
		require.True(t, asserted)
	})

	t.Run("secret update propagation", func(t *testing.T) {
		indexSecretName := controller.FilterConfigBundleIndexSecretName("translation-testupstream", "default")
		// Verify that the apiKey still exists in the filter config bundle with the existing value.
		internaltesting.RequireEventuallyNoError(t, func() error {
			config, err := extractFilterConfigFromBundle(t.Context(), indexSecretName)
			if err != nil {
				return err
			}
			if !strings.Contains(config, dummyToken) {
				return fmt.Errorf("filter config bundle does not contain %s", dummyToken)
			}
			return nil
		}, 10*time.Second, 1*time.Second, "initial filter config bundle not found")

		// Update the secret used by the BackendSecurityPolicy to have a new apiKey value.
		const updatedKey = "pikachu"
		secretUpdated := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: translation-testupstream-cool-model-backend-api-key
  namespace: default
type: Opaque
stringData:
  apiKey: "%s"`, updatedKey)
		require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), secretUpdated))

		// Verify that the new apiKey is propagated to the filter config bundle.
		internaltesting.RequireEventuallyNoError(t, func() error {
			config, err := extractFilterConfigFromBundle(t.Context(), indexSecretName)
			if err != nil {
				return err
			}
			if !strings.Contains(config, updatedKey) {
				return fmt.Errorf("filter config bundle does not contain %s", updatedKey)
			}
			return nil
		}, 20*time.Second, 1*time.Second, "updated secret not propagated to filter config bundle")
	})
}

// extractFilterConfigFromBundle reassembles the filter config from its Kubernetes Secrets.
func extractFilterConfigFromBundle(ctx context.Context, indexSecretName string) (string, error) {
	ctrl := e2elib.Kubectl(ctx, "get", "secrets", "-n", e2elib.EnvoyGatewayNamespace,
		indexSecretName, "-o", `jsonpath='{.data.index\.yaml}'`)
	ctrl.Stderr = nil
	ctrl.Stdout = nil
	output, err := ctrl.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get filter config bundle index: %w", err)
	}
	indexRaw, err := base64.StdEncoding.DecodeString(strings.Trim(string(output), "'"))
	if err != nil {
		return "", fmt.Errorf("failed to decode filter config bundle index: %w", err)
	}
	index, err := filterapi.UnmarshalConfigBundleIndex(indexRaw)
	if err != nil {
		return "", err
	}

	var raw []byte
	for _, part := range index.Parts {
		ctrl = e2elib.Kubectl(ctx, "get", "secrets", "-n", e2elib.EnvoyGatewayNamespace,
			part.Name, "-o", "jsonpath='{.data.chunk}'")
		ctrl.Stderr = nil
		ctrl.Stdout = nil
		output, err = ctrl.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get filter config bundle part %s: %w", part.Name, err)
		}
		partRaw, decodeErr := base64.StdEncoding.DecodeString(strings.Trim(string(output), "'"))
		if decodeErr != nil {
			return "", fmt.Errorf("failed to decode filter config bundle part %s: %w", part.Name, decodeErr)
		}
		raw = append(raw, partRaw...)
	}
	if got := filterapi.ConfigBundleChecksum(raw); got != index.Checksum {
		return "", fmt.Errorf("filter config bundle checksum mismatch: expected %s, got %s", index.Checksum, got)
	}
	return string(raw), nil
}

// TestPromptCacheTranslationMatrix proves OpenAI-shim cache_control markers survive
// translation onto the AWSAnthropic InvokeModel body for the known prompt shapes.
func TestPromptCacheTranslationMatrix(t *testing.T) {
	const manifest = "testdata/testupstream.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=translation-testupstream"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	const (
		expPath    = "/model/anthropic.claude-cache-matrix/invoke"
		expHost    = "testupstream.default.svc.cluster.local"
		upstreamID = "primary"
	)
	fakeResponseBody := `{"id":"msg_cache","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":1}}`

	for _, tc := range []struct {
		name           string
		requestBody    string
		expRequestBody string
	}{
		{
			name: "plain_only",
			requestBody: `{
  "model":"anthropic.claude-cache-matrix",
  "max_tokens":16,
  "messages":[
    {"role":"user","content":[{"type":"text","text":"plain prefix","cache_control":{"type":"ephemeral"}}]}
  ]
}`,
			expRequestBody: `{"max_tokens":16,"messages":[{"content":[{"text":"plain prefix","cache_control":{"type":"ephemeral"},"type":"text"}],"role":"user"}],"anthropic_version":"bedrock-2023-05-31"}`,
		},
		{
			name: "tool_defs_only",
			requestBody: `{
  "model":"anthropic.claude-cache-matrix",
  "max_tokens":16,
  "tools":[{
    "type":"function",
    "function":{
      "name":"search_groups",
      "description":"Search feedback groups",
      "parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]},
      "cache_control":{"type":"ephemeral"}
    }
  }],
  "messages":[{"role":"user","content":"use tools"}]
}`,
			expRequestBody: `{"max_tokens":16,"messages":[{"content":[{"text":"use tools","type":"text"}],"role":"user"}],"tools":[{"input_schema":{"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"},"name":"search_groups","description":"Search feedback groups","cache_control":{"type":"ephemeral"}}],"anthropic_version":"bedrock-2023-05-31"}`,
		},
		{
			name: "tool_messages_bp_on_result",
			requestBody: `{
  "model":"anthropic.claude-cache-matrix",
  "max_tokens":16,
  "tools":[{
    "type":"function",
    "function":{
      "name":"search_groups",
      "description":"Search feedback groups",
      "parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}
    }
  }],
  "messages":[
    {"role":"user","content":"search notifications"},
    {"role":"assistant","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"search_groups","arguments":"{\"query\":\"notifications\"}"}}]},
    {"role":"tool","tool_call_id":"toolu_1","content":"group results","cache_control":{"type":"ephemeral"}}
  ]
}`,
			expRequestBody: `{"max_tokens":16,"messages":[{"content":[{"text":"search notifications","type":"text"}],"role":"user"},{"content":[{"id":"toolu_1","input":{"query":"notifications"},"name":"search_groups","type":"tool_use"}],"role":"assistant"},{"content":[{"tool_use_id":"toolu_1","is_error":false,"cache_control":{"type":"ephemeral"},"content":[{"text":"group results","type":"text"}],"type":"tool_result"}],"role":"user"}],"tools":[{"input_schema":{"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"},"name":"search_groups","description":"Search feedback groups"}],"anthropic_version":"bedrock-2023-05-31"}`,
		},
		{
			name: "tool_messages_bp_on_tool_use",
			requestBody: `{
  "model":"anthropic.claude-cache-matrix",
  "max_tokens":16,
  "tools":[{
    "type":"function",
    "function":{
      "name":"search_groups",
      "description":"Search feedback groups",
      "parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}
    }
  }],
  "messages":[
    {"role":"user","content":"search notifications"},
    {"role":"assistant","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"search_groups","arguments":"{\"query\":\"notifications\"}"},"cache_control":{"type":"ephemeral"}}]},
    {"role":"tool","tool_call_id":"toolu_1","content":"group results"}
  ]
}`,
			expRequestBody: `{"max_tokens":16,"messages":[{"content":[{"text":"search notifications","type":"text"}],"role":"user"},{"content":[{"id":"toolu_1","input":{"query":"notifications"},"name":"search_groups","cache_control":{"type":"ephemeral"},"type":"tool_use"}],"role":"assistant"},{"content":[{"tool_use_id":"toolu_1","is_error":false,"content":[{"text":"group results","type":"text"}],"type":"tool_result"}],"role":"user"}],"tools":[{"input_schema":{"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"},"name":"search_groups","description":"Search feedback groups"}],"anthropic_version":"bedrock-2023-05-31"}`,
		},
		{
			name: "tool_messages_bp_on_plain",
			requestBody: `{
  "model":"anthropic.claude-cache-matrix",
  "max_tokens":16,
  "tools":[{
    "type":"function",
    "function":{
      "name":"search_groups",
      "description":"Search feedback groups",
      "parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}
    }
  }],
  "messages":[
    {"role":"user","content":"search notifications"},
    {"role":"assistant","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"search_groups","arguments":"{\"query\":\"notifications\"}"}}]},
    {"role":"tool","tool_call_id":"toolu_1","content":"group results"},
    {"role":"user","content":[{"type":"text","text":"summarize","cache_control":{"type":"ephemeral"}}]}
  ]
}`,
			expRequestBody: `{"max_tokens":16,"messages":[{"content":[{"text":"search notifications","type":"text"}],"role":"user"},{"content":[{"id":"toolu_1","input":{"query":"notifications"},"name":"search_groups","type":"tool_use"}],"role":"assistant"},{"content":[{"tool_use_id":"toolu_1","is_error":false,"content":[{"text":"group results","type":"text"}],"type":"tool_result"}],"role":"user"},{"content":[{"text":"summarize","cache_control":{"type":"ephemeral"},"type":"text"}],"role":"user"}],"tools":[{"input_schema":{"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"},"name":"search_groups","description":"Search feedback groups"}],"anthropic_version":"bedrock-2023-05-31"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Eventually(t, func() bool {
				fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
				defer fwd.Kill()

				req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(tc.requestBody))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(fakeResponseBody)))
				req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte(expPath)))
				req.Header.Set(testupstreamlib.ExpectedHostKey, expHost)
				req.Header.Set(testupstreamlib.ExpectedTestUpstreamIDKey, upstreamID)
				req.Header.Set(testupstreamlib.ExpectedRequestBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.expRequestBody)))

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Logf("error: %v", err)
					return false
				}
				defer func() { _ = resp.Body.Close() }()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Logf("error reading response body: %v", err)
					return false
				}
				if resp.StatusCode != http.StatusOK {
					t.Logf("unexpected status code: %d, body: %s", resp.StatusCode, body)
					return false
				}
				return true
			}, 20*time.Second, 1*time.Second)
		})
	}
}

// TestAnthropicBetaHeaderTranslationMatrix proves anthropic-beta header flags survive
// translation onto the AWSAnthropic InvokeModel body's anthropic_beta field, including
// the rewrite of Anthropic-API-only flag names to their Bedrock equivalents.
//
// The matrix is derived from the "bedrock" column of litellm's beta header mapping
// (https://github.com/BerriAI/litellm/blob/main/litellm/anthropic_beta_headers_config.json):
//   - advanced-tool-use-2025-11-20 -> tool-search-tool-2025-10-19 (Anthropic API umbrella
//     flag for Tool Search; Bedrock exposes the same tool_search_tool_*_20251119 tool types
//     under the older-dated flag). This is the flag Claude Code sends.
//   - tool-search-tool-2025-10-19 -> tool-search-tool-2025-10-19 (native Bedrock spelling).
//   - code-execution-2025-08-25, files-api-2025-04-14 -> null (unsupported, must be dropped;
//     Bedrock rejects the whole request with a 400 on unrecognized flags).
func TestAnthropicBetaHeaderTranslationMatrix(t *testing.T) {
	const manifest = "testdata/testupstream.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=translation-testupstream"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	const (
		expPath    = "/model/anthropic.claude-cache-matrix/invoke"
		expHost    = "testupstream.default.svc.cluster.local"
		upstreamID = "primary"
	)
	fakeResponseBody := `{"id":"msg_beta","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":1}}`

	// The request carries a tool_search_tool_regex_20251119 tool definition, the real
	// Claude Code request shape: the tool type string is identical on the Anthropic API
	// and Bedrock, only the enabling beta flag name differs.
	const requestBody = `{"model":"anthropic.claude-cache-matrix","max_tokens":16,"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}],"messages":[{"role":"user","content":"hi"}]}`
	const translatedBodyPrefix = `{"max_tokens":16,"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}],"messages":[{"role":"user","content":"hi"}],"anthropic_version":"bedrock-2023-05-31"`

	for _, tc := range []struct {
		name string
		// betaHeader is the anthropic-beta header value sent by the client.
		betaHeader string
		// expRequestBody is the exact AWSAnthropic InvokeModel body the testupstream
		// server must receive (asserted byte-for-byte by the testupstream server).
		expRequestBody string
	}{
		{
			name:           "anthropic_api_umbrella_flag_rewritten",
			betaHeader:     "advanced-tool-use-2025-11-20",
			expRequestBody: translatedBodyPrefix + `,"anthropic_beta":["tool-search-tool-2025-10-19"]}`,
		},
		{
			name:           "native_bedrock_flag_passthrough",
			betaHeader:     "tool-search-tool-2025-10-19",
			expRequestBody: translatedBodyPrefix + `,"anthropic_beta":["tool-search-tool-2025-10-19"]}`,
		},
		{
			name:           "umbrella_and_native_flag_deduplicated",
			betaHeader:     "advanced-tool-use-2025-11-20,tool-search-tool-2025-10-19",
			expRequestBody: translatedBodyPrefix + `,"anthropic_beta":["tool-search-tool-2025-10-19"]}`,
		},
		{
			name:           "umbrella_flag_with_other_supported_flags",
			betaHeader:     "advanced-tool-use-2025-11-20,interleaved-thinking-2025-05-14",
			expRequestBody: translatedBodyPrefix + `,"anthropic_beta":["tool-search-tool-2025-10-19","interleaved-thinking-2025-05-14"]}`,
		},
		{
			name:           "unsupported_siblings_dropped",
			betaHeader:     "advanced-tool-use-2025-11-20,code-execution-2025-08-25,files-api-2025-04-14",
			expRequestBody: translatedBodyPrefix + `,"anthropic_beta":["tool-search-tool-2025-10-19"]}`,
		},
		{
			name:           "only_unsupported_flags_no_anthropic_beta",
			betaHeader:     "code-execution-2025-08-25",
			expRequestBody: translatedBodyPrefix + `}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Eventually(t, func() bool {
				fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
				defer fwd.Kill()

				req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/anthropic/v1/messages", strings.NewReader(requestBody))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("anthropic-beta", tc.betaHeader)
				req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(fakeResponseBody)))
				req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte(expPath)))
				req.Header.Set(testupstreamlib.ExpectedHostKey, expHost)
				req.Header.Set(testupstreamlib.ExpectedTestUpstreamIDKey, upstreamID)
				req.Header.Set(testupstreamlib.ExpectedRequestBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.expRequestBody)))

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Logf("error: %v", err)
					return false
				}
				defer func() { _ = resp.Body.Close() }()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Logf("error reading response body: %v", err)
					return false
				}
				if resp.StatusCode != http.StatusOK {
					t.Logf("unexpected status code: %d, body: %s", resp.StatusCode, body)
					return false
				}
				return true
			}, 20*time.Second, 1*time.Second)
		})
	}
}
