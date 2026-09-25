// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplane

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// failIf5xx because 5xx errors are likely a sign of a broken ExtProc or Envoy.
func failIf5xx(t *testing.T, resp *http.Response, was5xx *bool) {
	if resp.StatusCode >= 500 && resp.StatusCode < 600 {
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		*was5xx = true
		t.Fatalf("received %d response with body: %s", resp.StatusCode, string(body))
	}
}

// TestWithTestUpstream tests the end-to-end flow of the external processor with Envoy and the test upstream.
//
// This does not require any environment variables to be set as it relies on the test upstream.
func TestWithTestUpstream(t *testing.T) {
	now := time.Unix(int64(time.Now().Second()), 0).UTC()

	// Substitute any dynamically generated UUIDs in the response body with a placeholder
	// example generated UUID 703482f8-2e5b-4dcc-a872-d74bd66c386.
	m := regexp.MustCompile("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
	createdReg := regexp.MustCompile(`"created":\d+`)

	config := &filterapi.Config{
		Version: version.Parse(),
		// Dataplane Envoy does not set per-route xDS route_name metadata; use gateway defaults so costs still emit.
		GlobalLLMRequestCosts: []filterapi.GlobalLLMRequestCost{
			{MetadataKey: "used_token", Type: filterapi.LLMRequestCostTypeInputToken},
		},
		Backends: []filterapi.Backend{
			alwaysFailingBackend,
			testUpstreamOpenAIBackend,
			testUpstreamModelNameOverride,
			testUpstreamAAWSBackend,
			testUpstreamDynMdCredBackend,
			testUpstreamAWSDynMdCredBackend,
			testUpstreamAzureBackend,
			testUpstreamGCPVertexAIBackend,
			testUpstreamGCPAnthropicAIBackend,
			testUpstreamAWSAnthropicBackend,
			testUpstreamBodyMutationBackend,
			testUpstreamBodyMutationAnthropicBackend,
			{
				Name: "testupstream-openai-5xx", Schema: openAISchema, HeaderMutation: &filterapi.HTTPHeaderMutation{
					Set: []filterapi.HTTPHeader{{Name: testupstreamlib.ResponseStatusKey, Value: "500"}},
				},
				ModelNameOverride: "bad-model",
			},
			{
				Name:   "testupstream-anthropic",
				Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAnthropic}, Auth: &filterapi.BackendAuth{
					AnthropicAPIKey: &filterapi.AnthropicAPIKeyAuth{Key: "anthropic-api-key"},
				},
			},
		},
		Models: []filterapi.Model{
			{Name: "some-model1", OwnedBy: "Envoy AI Gateway", CreatedAt: now},
			{Name: "some-model2", OwnedBy: "Envoy AI Gateway", CreatedAt: now},
			{Name: "some-model3", OwnedBy: "Envoy AI Gateway", CreatedAt: now},
		},
	}

	configBytes, err := yaml.Marshal(config)
	require.NoError(t, err)
	env := startTestEnvironment(t, string(configBytes), true, false)

	listenerPort := env.EnvoyListenerPort()

	expectedModels := openai.ModelList{
		Object: "list",
		Data: []openai.Model{
			{ID: "some-model1", Object: "model", OwnedBy: "Envoy AI Gateway", Created: openai.JSONUNIXTime(now)},
			{ID: "some-model2", Object: "model", OwnedBy: "Envoy AI Gateway", Created: openai.JSONUNIXTime(now)},
			{ID: "some-model3", Object: "model", OwnedBy: "Envoy AI Gateway", Created: openai.JSONUNIXTime(now)},
		},
	}

	was5xx := false
	for _, tc := range []struct {
		// name is the name of the test case.
		name,
		// backend is the backend to send the request to. Either "openai" or "aws-bedrock" (matching the headers in the config).
		backend,
		// path is the path to send the request to.
		path,
		// method is the HTTP method to use.
		method,
		// requestBody is the request requestBody.
		requestBody,
		// responseBody is the response body to return from the test upstream.
		responseBody,
		// responseType is either empty, "sse" or "aws-event-stream" as implemented by the test upstream.
		responseType,
		// responseStatus is the HTTP status code of the response returned by the test upstream.
		responseStatus,
		// responseHeaders are the headers sent in the HTTP response
		// The value is a base64 encoded string of comma separated key-value pairs.
		// E.g. "key1:value1,key2:value2".
		responseHeaders,
		// expRawQuery is the expected raw query to be sent to the test upstream.
		expRawQuery string
		// expPath is the expected path to be sent to the test upstream.
		expPath string
		// expHost is the expected host to be sent to the test upstream.
		expHost string
		// expRequestHeaders are the expected request headers to be sent to the test upstream.
		// The value is a base64 encoded string of comma separated key-value pairs.
		// E.g. "key1:value1,key2:value2".
		expRequestHeaders map[string]string
		// requestHeaders are extra headers set on the client request to the gateway, e.g. to simulate a
		// downstream client spoofing an internal header.
		requestHeaders map[string]string
		// nonExpectedRequestHeaders are header names that must NOT be present on the request the test
		// upstream receives (the test upstream returns 400 if any are present).
		nonExpectedRequestHeaders []string
		// expRequestBody is the expected body to be sent to the test upstream.
		// This can be used to test the request body translation.
		expRequestBody string
		// expStatus is the expected status code from the gateway.
		expStatus int
		// expResponseHeaders are the expected headers from the gateway.
		expResponseHeaders map[string]string
		// expResponseBody is the expected body from the gateway to the client.
		// If this is empty, the responseBody from the test upstream is expected to be returned as-is.
		expResponseBody string
		// expResponseBodyFunc is a function to check the response body. This can be used instead of the expResponseBody field.
		expResponseBodyFunc func(require.TestingT, []byte)
	}{
		{
			name:            "openai - /v1/images/generations",
			backend:         "openai",
			path:            "/v1/images/generations",
			method:          http.MethodPost,
			requestBody:     `{"model":"gpt-image-1-mini","prompt":"a cat wearing sunglasses","size":"1024x1024","quality":"low"}`,
			expPath:         "/v1/images/generations",
			responseBody:    `{"created":1736890000,"data":[{"url":"https://example.com/image1.png"}],"model":"gpt-image-1-mini","usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"created":1736890000,"data":[{"url":"https://example.com/image1.png"}],"model":"gpt-image-1-mini","usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		},
		{
			name:            "openai - /v1/images/generations - non json upstream error mapped to OpenAI",
			backend:         "openai",
			path:            "/v1/images/generations",
			method:          http.MethodPost,
			requestBody:     `{"model":"dall-e-3","prompt":"a scenic beach"}`,
			expPath:         "/v1/images/generations",
			responseHeaders: "content-type:text/plain",
			responseStatus:  strconv.Itoa(http.StatusServiceUnavailable),
			responseBody:    `backend timeout`,
			expStatus:       http.StatusServiceUnavailable,
			expResponseBody: `{"error":{"type":"OpenAIBackendError","message":"backend timeout","code":"503"},"type":"error"}`,
		},
		{
			name:            "unknown path",
			path:            "/unknown",
			requestBody:     `{"prompt": "hello"}`,
			expStatus:       http.StatusNotFound,
			expResponseBody: `unsupported path: /unknown`,
		},
		{
			name:        "openai - /v1/chat/completions - malformed JSON request",
			backend:     "openai",
			path:        "/v1/chat/completions",
			method:      http.MethodPost,
			requestBody: `{"model": "something", "messages": [invalid json`,
			expStatus:   http.StatusBadRequest,
			expResponseBodyFunc: func(t require.TestingT, body []byte) {
				bodyStr := string(body)
				require.Contains(t, bodyStr, `"type":"error"`)
				require.Contains(t, bodyStr, `"type":"BadRequest"`)
				require.Contains(t, bodyStr, `"code":"400"`)
				require.Contains(t, bodyStr, `malformed request: failed to parse JSON for /v1/chat/completions:`)
			},
		},
		{
			name:            "gcp-anthropicai - /v1/chat/completions - missing max_tokens",
			backend:         "gcp-anthropicai",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"claude-3-sonnet","messages":[{"role":"user","content":"Hello"}]}`,
			expRequestBody:  `{"max_tokens":0,"messages":[{"content":[{"text":"Hello","type":"text"}],"role":"user"}],"anthropic_version":"vertex-2023-10-16"}`,
			expPath:         "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:rawPredict",
			responseStatus:  strconv.Itoa(http.StatusBadRequest),
			responseBody:    `{"error":{"type":"invalid_request_error","message":"max_tokens: Value must be greater than or equal to 1"}}`,
			expStatus:       http.StatusBadRequest,
			expResponseBody: `{"type":"error","error":{"type":"invalid_request_error","code":"400","message":"max_tokens: Value must be greater than or equal to 1"}}`,
		},
		{
			name:            "aws-anthropic - /v1/chat/completions - missing max_tokens",
			backend:         "aws-anthropic",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"anthropic.claude-3-sonnet-20240229-v1:0","messages":[{"role":"user","content":"Hello"}]}`,
			expRequestBody:  `{"max_tokens":0,"messages":[{"content":[{"text":"Hello","type":"text"}],"role":"user"}],"anthropic_version":"bedrock-2023-05-31"}`,
			expPath:         "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke",
			responseHeaders: "x-amzn-errortype:invalid_request_error",
			responseStatus:  strconv.Itoa(http.StatusBadRequest),
			responseBody:    `{"message":"max_tokens: Value must be greater than or equal to 1"}`,
			expStatus:       http.StatusBadRequest,
			expResponseBody: `{"type":"error","error":{"type":"invalid_request_error","code":"400","message":"max_tokens: Value must be greater than or equal to 1"}}`,
		},
		{
			name:            "aws system role - /v1/chat/completions",
			backend:         "aws-bedrock",
			path:            "/v1/chat/completions",
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:         "/model/something/converse",
			responseBody:    `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			expRequestBody:  `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expStatus:       http.StatusOK,
			responseHeaders: "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"response","role":"assistant"}}],"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","created":123,"model":"something","object":"chat.completion","usage":{"completion_tokens":20,"prompt_tokens":10,"total_tokens":30}}`,
		},
		{
			// A downstream client spoofs the internal AWS signing-host header. It must be stripped before
			// egress so it never reaches the upstream and cannot influence SigV4 signing.
			name:                      "aws-bedrock - spoofed signing-host header is stripped before upstream",
			backend:                   "aws-bedrock",
			path:                      "/v1/chat/completions",
			method:                    http.MethodPost,
			requestBody:               `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:                   "/model/something/converse",
			responseBody:              `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			expRequestBody:            `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expStatus:                 http.StatusOK,
			responseHeaders:           "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody:           `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"response","role":"assistant"}}],"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","created":123,"model":"something","object":"chat.completion","usage":{"completion_tokens":20,"prompt_tokens":10,"total_tokens":30}}`,
			requestHeaders:            map[string]string{"x-ai-eg-upstream-host": "attacker.example.com"},
			nonExpectedRequestHeaders: []string{"x-ai-eg-upstream-host"},
		},
		{
			// A trusted filter injects a per-request credential. The upstream must see a signature
			// carrying that session token and none of the credential headers. Only Envoy can prove
			// the second half: dropping them from the extproc's local map leaves them on the wire.
			name:            "aws-bedrock - per-request credential signs and is stripped before upstream",
			backend:         "aws-bedrock",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:         "/model/something/converse",
			responseBody:    `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			expRequestBody:  `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expStatus:       http.StatusOK,
			responseHeaders: "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"response","role":"assistant"}}],"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","created":123,"model":"something","object":"chat.completion","usage":{"completion_tokens":20,"prompt_tokens":10,"total_tokens":30}}`,
			requestHeaders: map[string]string{
				"x-aigw-aws-access-key-id":     "ASIAPERREQUEST",
				"x-aigw-aws-secret-access-key": "per-request-secret",
				"x-aigw-aws-session-token":     fakeAWSPerRequestSessionToken,
			},
			// The static fallback has no session token, so this can only be the per-request one.
			expRequestHeaders:         map[string]string{"X-Amz-Security-Token": fakeAWSPerRequestSessionToken},
			nonExpectedRequestHeaders: awsCredentialOverrideHeaders,
		},
		{
			// A downstream filter turns x-test-dynmd-api-key into dynamic metadata, which must
			// reach the upstream. Only a real Envoy proves the forwarding_namespaces link.
			name:                      "openai - per-request credential from dynamic metadata",
			backend:                   "dynmd-cred",
			path:                      "/v1/chat/completions",
			method:                    http.MethodPost,
			requestBody:               `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:                   "/v1/chat/completions",
			responseBody:              `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:                 http.StatusOK,
			expResponseBody:           `{"choices":[{"message":{"content":"This is a test."}}]}`,
			requestHeaders:            map[string]string{"x-test-dynmd-api-key": "metadata-sourced-key"},
			expRequestHeaders:         map[string]string{"Authorization": "Bearer metadata-sourced-key"},
			nonExpectedRequestHeaders: []string{"x-test-dynmd-api-key"},
		},
		{
			// Without the header the namespace is empty, so the static credential is used.
			name:              "openai - absent dynamic metadata falls back to the configured credential",
			backend:           "dynmd-cred",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:           "/v1/chat/completions",
			responseBody:      `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expRequestHeaders: map[string]string{"Authorization": "Bearer dummy-configured-key"},
		},
		{
			// The credential arrives as one struct value. The static fallback has no session
			// token, so X-Amz-Security-Token can only come from the forwarded metadata.
			name:              "aws-bedrock - struct credential from dynamic metadata signs the request",
			backend:           "aws-dynmd-cred",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:           "/model/something/converse",
			responseBody:      `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			expRequestBody:    `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expStatus:         http.StatusOK,
			responseHeaders:   "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody:   `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"response","role":"assistant"}}],"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","created":123,"model":"something","object":"chat.completion","usage":{"completion_tokens":20,"prompt_tokens":10,"total_tokens":30}}`,
			expRequestHeaders: map[string]string{"X-Amz-Security-Token": fakeAWSMetadataSessionToken},
		},
		{
			// Falls back to the configured credential file, which has no session token, so no
			// X-Amz-Security-Token reaches the upstream.
			name:                      "aws-bedrock - no per-request credential falls back to the configured one",
			backend:                   "aws-bedrock",
			path:                      "/v1/chat/completions",
			method:                    http.MethodPost,
			requestBody:               `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:                   "/model/something/converse",
			responseBody:              `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			expRequestBody:            `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expStatus:                 http.StatusOK,
			responseHeaders:           "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody:           `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"response","role":"assistant"}}],"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","created":123,"model":"something","object":"chat.completion","usage":{"completion_tokens":20,"prompt_tokens":10,"total_tokens":30}}`,
			nonExpectedRequestHeaders: []string{"X-Amz-Security-Token"},
		},
		{
			name:            "openai - /v1/chat/completions",
			backend:         "openai",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"choices":[{"message":{"content":"This is a test."}}]}`,
		},
		{
			name:            "openai - /v1/chat/completions - gzip",
			backend:         "openai",
			responseType:    "gzip",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"choices":[{"message":{"content":"This is a test."}}]}`,
		},
		{
			name:            "openai - /v1/chat/completions - tool call results",
			backend:         "openai",
			path:            "/v1/chat/completions",
			expPath:         "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     toolCallResultsRequestBody,
			expRequestBody:  toolCallResultsRequestBody,
			responseBody:    `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expResponseBody: `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:       http.StatusOK,
		},
		{
			name:            "aws-bedrock - /v1/chat/completions - tool call results",
			backend:         "aws-bedrock",
			path:            "/v1/chat/completions",
			expPath:         "/model/gpt-4-0613/converse",
			method:          http.MethodPost,
			requestBody:     toolCallResultsRequestBody,
			expRequestBody:  `{"inferenceConfig":{"maxTokens":1024},"messages":[{"content":[{"text":"List the files in the /tmp directory"}],"role":"user"},{"content":[{"toolUse":{"name":"list_files","input":{"path":"/tmp"},"toolUseId":"call_abc123"}}],"role":"assistant"},{"content":[{"toolResult":{"content":[{"text":"[\"foo.txt\", \"bar.log\", \"data.csv\"]"}],"status":null,"toolUseId":"call_abc123"}}],"role":"user"}]}`,
			responseBody:    `{"output":{"message":{"content":[{"text":"response"},{"text":"from"},{"text":"assistant"}],"role":"assistant"}},"stopReason":null,"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			responseHeaders: "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"response","role":"assistant"}}],"created":123, "id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","model":"gpt-4-0613","object":"chat.completion","usage":{"completion_tokens":20,"prompt_tokens":10,"total_tokens":30}}`,
			expStatus:       http.StatusOK,
		},
		{
			name:            "gcp-anthropic - /v1/chat/completions - tool call results",
			backend:         "gcp-anthropicai",
			path:            "/v1/chat/completions",
			expPath:         "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/gpt-4-0613:rawPredict",
			method:          http.MethodPost,
			requestBody:     toolCallResultsRequestBody,
			expRequestBody:  `{"max_tokens":1024,"messages":[{"content":[{"text":"List the files in the /tmp directory","type":"text"}],"role":"user"},{"content":[{"id":"call_abc123","input":{"path":"/tmp"},"name":"list_files","type":"tool_use"}],"role":"assistant"},{"content":[{"tool_use_id":"call_abc123","is_error":false,"content":[{"text":"[\"foo.txt\", \"bar.log\", \"data.csv\"]","type":"text"}],"type":"tool_result"}],"role":"user"}],"anthropic_version":"vertex-2023-10-16"}`,
			responseBody:    `{"id":"msg_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from Anthropic!"}],"usage":{"input_tokens":10,"output_tokens":25,"cache_read_input_tokens":10}}`,
			expResponseBody: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hello from Anthropic!","role":"assistant"}}],"created":123, "id":"msg_123","model":"gpt-4-0613","object":"chat.completion","usage":{"completion_tokens":25,"completion_tokens_details":{},"prompt_tokens":20,"total_tokens":45,"prompt_tokens_details":{"cached_tokens":10}}}`,
			expStatus:       http.StatusOK,
		},
		{
			name:            "azure-openai - /v1/chat/completions",
			backend:         "azure-openai",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:         "/openai/deployments/something/chat/completions",
			responseBody:    `{"model":"gpt-4o-2024-08-01","choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"model":"gpt-4o-2024-08-01","choices":[{"message":{"content":"This is a test."}}]}`,
		},
		{
			name:              "gcp-vertexai - /v1/chat/completions",
			backend:           "gcp-vertexai",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"gemini-1.5-pro","messages":[{"role":"system","content":"You are a helpful assistant."}]}`,
			expRequestBody:    `{"contents":null,"tools":null,"generationConfig":{},"systemInstruction":{"parts":[{"text":"You are a helpful assistant."}]}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/gemini-1.5-pro:generateContent",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"candidates":[{"content":{"parts":[{"text":"This is a test response from Gemini."}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":10,"totalTokenCount":35,"cachedContentTokenCount":10,"thoughtsTokenCount":10}}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"This is a test response from Gemini.","role":"assistant"}}],"model":"gemini-1.5-pro","object":"chat.completion","usage":{"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":10},"prompt_tokens":15,"prompt_tokens_details":{"cached_tokens":10},"total_tokens":35}}`,
		},
		{
			name:              "gcp-vertexai - /v1/chat/completions",
			backend:           "gcp-vertexai",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"gemini-1.5-pro","messages":[{"role":"system","content":"You are a helpful assistant."}]}`,
			expRequestBody:    `{"contents":null,"tools":null,"generationConfig":{},"systemInstruction":{"parts":[{"text":"You are a helpful assistant."}]}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/gemini-1.5-pro:generateContent",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"candidates":[{"content":{"parts":[{"text":"This is a test response from Gemini."}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":10,"totalTokenCount":25}}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"This is a test response from Gemini.","role":"assistant"}}],"model":"gemini-1.5-pro","object":"chat.completion","usage":{"completion_tokens":10,"completion_tokens_details":{},"prompt_tokens":15,"total_tokens":25,"prompt_tokens_details":{}}}`,
		},
		{
			name:              "gcp-vertexai - /v1/chat/completions - tool use",
			backend:           "gcp-vertexai",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"gemini-1.5-pro","messages":[{"role":"user","content":"tell me the delivery date for order 123"}],"tools":[{"type":"function","function":{"name":"get_delivery_date","description":"Get the delivery date for a customer's order. Call this whenever you need to know the delivery date, for example when a customer asks 'Where is my package'","parameters":{"type":"object","properties":{"order_id":{"type":"string","description":"The customer's order ID."}},"required":["order_id"]}}}]}`,
			expRequestBody:    `{"contents":[{"parts":[{"text":"tell me the delivery date for order 123"}],"role":"user"}],"tools":[{"functionDeclarations":[{"description":"Get the delivery date for a customer's order. Call this whenever you need to know the delivery date, for example when a customer asks 'Where is my package'","name":"get_delivery_date","parameters":{"properties":{"order_id":{"description":"The customer's order ID.","type":"string"}},"required":["order_id"],"type":"object"}}]}],"generationConfig":{}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/gemini-1.5-pro:generateContent",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_delivery_date","args":{"order_id":"123"}}}]},"finishReason":"STOP","avgLogprobs":0.000001220789272338152}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":11,"totalTokenCount":61,"trafficType":"ON_DEMAND","promptTokensDetails":[{"modality":"TEXT","tokenCount":50}],"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":11}]},"modelVersion":"gemini-2.0-flash-001","createTime":"2025-07-11T22:15:44.956335Z","responseId":"EI5xaK-vOtqJm22IPmuCR14AI"}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"choices":[{"finish_reason":"tool_calls","index":0,"message":{"role":"assistant","tool_calls":[{"id":"703482f8-2e5b-4dcc-a872-d74bd66c3866","function":{"arguments":"{\"order_id\":\"123\"}","name":"get_delivery_date"},"type":"function"}]}}],"created":123, "id":"EI5xaK-vOtqJm22IPmuCR14AI","model":"gemini-2.0-flash-001","object":"chat.completion","usage":{"completion_tokens":11,"completion_tokens_details":{},"prompt_tokens":50,"total_tokens":61,"prompt_tokens_details":{}}}`,
		},
		{
			name:              "gcp-anthropicai - /v1/chat/completions",
			backend:           "gcp-anthropicai",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"claude-3-sonnet","max_completion_tokens":1024, "messages":[{"role":"system","content":"You are an Anthropic assistant."},{"role":"user","content":"Hello!"}]}`,
			expRequestBody:    `{"max_tokens":1024,"messages":[{"content":[{"text":"Hello!","type":"text"}],"role":"user"}],"system":[{"text":"You are an Anthropic assistant.","type":"text"}],"anthropic_version":"vertex-2023-10-16"}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:rawPredict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"id":"msg_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from Anthropic!"}],"usage":{"input_tokens":10,"output_tokens":25}}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hello from Anthropic!","role":"assistant"}}],"created":123, "id":"msg_123","model":"claude-3-sonnet","object":"chat.completion","usage":{"completion_tokens":25,"completion_tokens_details":{},"prompt_tokens":10,"total_tokens":35,"prompt_tokens_details":{}}}`,
		},
		{
			name:              "gcp-anthropicai - /v1/chat/completions - with cache",
			backend:           "gcp-anthropicai",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			requestBody:       `{"model":"claude-3-sonnet","max_completion_tokens":1024, "messages":[{"role":"user","content":[{"type":"text", "text":"Hello!", "cache_control": {"type": "ephemeral"}}]}]}`,
			expRequestBody:    `{"max_tokens":1024,"messages":[{"content":[{"text":"Hello!","cache_control":{"type":"ephemeral"},"type":"text"}],"role":"user"}],"anthropic_version":"vertex-2023-10-16"}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:rawPredict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"id":"msg_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from cached Anthropic!"}],"usage":{"input_tokens":10,"output_tokens":25, "cache_read_input_tokens": 8}}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hello from cached Anthropic!","role":"assistant"}}], "created":123, "id":"msg_123", "model":"claude-3-sonnet","object":"chat.completion","usage":{"completion_tokens":25,"completion_tokens_details":{},"prompt_tokens":18,"total_tokens":43,"prompt_tokens_details":{"cached_tokens":8}}}`,
		},
		{
			name:            "modelname-override - /v1/chat/completions",
			backend:         "modelname-override",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"requested-model","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expRequestBody:  `{"model":"override-model","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"choices":[{"message":{"content":"This is a test."}}]}`,
		},
		{
			name:            "modelname-override-and-fallback - /v1/chat/completions",
			backend:         "modelname-override-and-fallback",
			path:            "/v1/chat/completions?need=model",
			method:          http.MethodPost,
			requestBody:     `{"model":"requested-model-is-very-long-yes","messages":[{"role":"system","content":"You are a chatbot."}]}`,
			expStatus:       http.StatusOK,
			responseBody:    `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expResponseBody: `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expResponseHeaders: map[string]string{
				"X-Model": "requested-model-is-very-long-yes",
			},
		},
		{
			name:            "aws - /v1/chat/completions - streaming with tool use",
			backend:         "aws-bedrock",
			path:            "/v1/chat/completions",
			responseType:    "aws-event-stream",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true}`,
			expRequestBody:  `{"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expPath:         "/model/something/converse-stream",
			responseHeaders: "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			responseBody: `{"role":"assistant"}
{"contentBlockIndex": 0, "delta":{"text":"Don"}}
{"contentBlockIndex": 0, "delta":{"text":"'t worry,  I'm here to help. It"}}
{"contentBlockIndex": 0, "delta":{"text":" seems like you're testing my ability to respond appropriately"}}
{"contentBlockIndex": 0}
{"start":{"toolUse":{"name":"cosine","toolUseId":"tooluse_QklrEHKjRu6Oc4BQUfy7ZQ"}}}
{"contentBlockIndex": 1, "delta":{"toolUse": {"input": "{\"x\": \"17\"}"}}}
{"contentBlockIndex": 1}
{"start":{"toolUse":{"name":"sine","toolUseId":"tooluse_stream2"}}}
{"contentBlockIndex": 2, "delta":{"toolUse": {"input": "{\"x\": \"17\"}"}}}
{"contentBlockIndex": 2}
{"stopReason":"tool_use"}
{"usage":{"inputTokens":41, "outputTokens":36, "totalTokens":77}}
`,
			expStatus: http.StatusOK,
			expResponseBody: `data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"","role":"assistant"}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"Don","role":"assistant"}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"'t worry,  I'm here to help. It","role":"assistant"}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":" seems like you're testing my ability to respond appropriately","role":"assistant"}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tooluse_QklrEHKjRu6Oc4BQUfy7ZQ","function":{"arguments":"","name":"cosine"},"type":"function"}]}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":null,"function":{"arguments":"{\"x\": \"17\"}","name":""},"type":"function"}]}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":1,"id":"tooluse_stream2","function":{"arguments":"","name":"sine"},"type":"function"}]}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":1,"id":null,"function":{"arguments":"{\"x\": \"17\"}","name":""},"type":"function"}]}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"","role":"assistant"},"finish_reason":"tool_calls"}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[],"created":123,"model":"something","object":"chat.completion.chunk","usage":{"prompt_tokens":41,"completion_tokens":36,"total_tokens":77}}

data: [DONE]
`,
		},
		{
			name:         "aws-bedrock - /v1/chat/completions - streaming with thinking config",
			backend:      "aws-bedrock",
			path:         "/v1/chat/completions",
			responseType: "aws-event-stream",
			method:       http.MethodPost,
			requestBody: `{
		"model":"something",
		"messages":[{"role":"system","content":"You are a chatbot."}],
		"stream": true,
		"thinking": {"type": "enabled", "budget_tokens": 4096}
	}`,
			expRequestBody: `{"additionalModelRequestFields":{"thinking":{"budget_tokens":4096,"type":"enabled"}},"inferenceConfig":{},"messages":[],"system":[{"text":"You are a chatbot."}]}`,
			expPath:        "/model/something/converse-stream",
			responseBody: `{"role":"assistant"}
	{"delta":{"reasoningContent":{"text":"First, I'll start by acknowledging the user..."}}}
	{"delta":{"text":"Hello!"}}
	{"stopReason":"end_turn"}`,
			expStatus:       http.StatusOK,
			responseHeaders: "x-amzn-requestid:2bc5b090-a26c-4007-9467-ce5adc4ffa1d",
			expResponseBody: `data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"","role":"assistant"}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"First, I'll start by acknowledging the user..."}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"Hello!","role":"assistant"}}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: {"id":"2bc5b090-a26c-4007-9467-ce5adc4ffa1d","choices":[{"index":0,"delta":{"content":"","role":"assistant"},"finish_reason":"stop"}],"created":123,"model":"something","object":"chat.completion.chunk"}

data: [DONE]
`,
		},
		{
			name:         "openai - /v1/chat/completions - streaming",
			backend:      "openai",
			path:         "/v1/chat/completions",
			responseType: "sse",
			method:       http.MethodPost,
			requestBody:  `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true}`,
			expPath:      "/v1/chat/completions",
			responseBody: `
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
[DONE]
`,
			expStatus: http.StatusOK,
			expResponseBody: `data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]

`,
		},
		{
			name:              "gcp-vertexai - /v1/chat/completions - streaming",
			backend:           "gcp-vertexai",
			path:              "/v1/chat/completions",
			responseType:      "sse",
			method:            http.MethodPost,
			requestBody:       `{"model":"gemini-1.5-pro","messages":[{"role":"system","content":"You are a helpful assistant."}], "stream": true}`,
			expRequestBody:    `{"contents":null,"tools":null,"generationConfig":{},"systemInstruction":{"parts":[{"text":"You are a helpful assistant."}]}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/gemini-1.5-pro:streamGenerateContent",
			expRawQuery:       "alt=sse",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody: `{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}
{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":"! How"}],"role":"model"}}]}
{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":" can I"}],"role":"model"}}]}
{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":" help"}],"role":"model"}}]}
{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":" you"}],"role":"model"}}]}
{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":" today"}],"role":"model"}}]}
{"responseId":"msg_123","createTime":"2024-11-15T09:00:00Z","candidates":[{"content":{"parts":[{"text":"?"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":7,"totalTokenCount":17}}`,
			expStatus: http.StatusOK,
			expResponseBody: `data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":"Hello","role":"assistant"}}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":"! How","role":"assistant"}}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":" can I","role":"assistant"}}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":" help","role":"assistant"}}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":" you","role":"assistant"}}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":" today","role":"assistant"}}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":"?","role":"assistant"},"finish_reason":"stop"}],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[],"created":123,"model":"gemini-1.5-pro","object":"chat.completion.chunk","usage":{"prompt_tokens":10,"completion_tokens":7,"total_tokens":17,"completion_tokens_details":{},"prompt_tokens_details":{}}}

data: [DONE]
`,
		},
		{
			name:              "gcp-anthropicai - /v1/chat/completions - streaming",
			backend:           "gcp-anthropicai",
			path:              "/v1/chat/completions",
			method:            http.MethodPost,
			responseType:      "sse",
			requestBody:       `{"model":"claude-3-sonnet","max_completion_tokens":1024, "messages":[{"role":"user","content":"Why is the sky blue?"}], "stream": true}`,
			expRequestBody:    `{"max_tokens":1024,"messages":[{"content":[{"text":"Why is the sky blue?","type":"text"}],"role":"user"}],"stream":true,"anthropic_version":"vertex-2023-10-16"}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:streamRawPredict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_123", "usage": {"input_tokens": 15, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 10, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "The sky appears blue"}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text":" due to Rayleigh scattering."}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 15, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 10, "output_tokens": 12}}

event: message_stop
data: {"type": "message_stop"}
`,
			expStatus: http.StatusOK,
			expResponseBody: `data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":"The sky appears blue","role":"assistant"}}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"content":" due to Rayleigh scattering."}}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk","usage":{"prompt_tokens":25,"completion_tokens":12,"total_tokens":37,"completion_tokens_details":{},"prompt_tokens_details":{"cached_tokens":10}}}

data: [DONE]

`,
		},
		{
			name:         "gcp-anthropicai - /v1/chat/completions - streaming tool use",
			backend:      "gcp-anthropicai",
			path:         "/v1/chat/completions",
			method:       http.MethodPost,
			responseType: "sse",
			requestBody: `{
		"model": "claude-3-sonnet",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "What is the weather in Boston?"}],
		"stream": true,
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get the current weather in a given location",
				"parameters": {
					"type": "object",
					"properties": {
						"location": {"type": "string", "description": "The city and state, e.g. San Francisco, CA"}
					},
					"required": ["location"]
				}
			}
		}]
	}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:streamRawPredict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_123", "usage": {"input_tokens": 50, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "tool_use", "id": "toolu_abc123", "name": "get_weather", "input": {}}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": "{\"location\":\"Bosto"}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": "n, MA\"}"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"input_tokens": 50, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 20}}

event: message_stop
data: {"type": "message_stop"}`,
			expStatus: http.StatusOK,
			expResponseBody: `data: {"id":"msg_123","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"toolu_abc123","function":{"arguments":"","name":"get_weather"},"type":"function"}]}}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":null,"function":{"arguments":"{\"location\":\"Bosto","name":""}}]}}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":null,"function":{"arguments":"n, MA\"}","name":""}}]}}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk"}

data: {"id":"msg_123","choices":[],"created":123,"model":"claude-3-sonnet","object":"chat.completion.chunk","usage":{"prompt_tokens":50,"completion_tokens":20,"total_tokens":70,"completion_tokens_details":{},"prompt_tokens_details":{}}}

data: [DONE]

`,
		},
		{
			name:            "openai - /v1/chat/completions - error response",
			backend:         "openai",
			path:            "/v1/chat/completions",
			responseType:    "",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true}`,
			expPath:         "/v1/chat/completions",
			responseStatus:  "400",
			expStatus:       http.StatusBadRequest,
			responseBody:    `{"error": {"message": "missing required field", "type": "BadRequestError", "code": "400"}}`,
			expResponseBody: `{"error": {"message": "missing required field", "type": "BadRequestError", "code": "400"}}`,
		},
		{
			name:            "aws-bedrock - /v1/chat/completions - error response",
			backend:         "aws-bedrock",
			path:            "/v1/chat/completions",
			responseType:    "",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true}`,
			expPath:         "/model/something/converse-stream",
			responseStatus:  "429",
			expStatus:       http.StatusTooManyRequests,
			responseHeaders: "x-amzn-errortype:ThrottledException",
			responseBody:    `{"message": "aws bedrock rate limit exceeded"}`,
			expResponseBody: `{"type":"error","error":{"type":"ThrottledException","code":"429","message":"aws bedrock rate limit exceeded"}}`,
		},
		{
			name:            "gcp-vertexai - /v1/chat/completions - error response",
			backend:         "gcp-vertexai",
			path:            "/v1/chat/completions",
			responseType:    "",
			method:          http.MethodPost,
			requestBody:     `{"model":"gemini-1.5-pro","messages":[{"role":"system","content":"You are a helpful assistant."}]}`,
			expPath:         "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/gemini-1.5-pro:generateContent",
			responseStatus:  "400",
			expStatus:       http.StatusBadRequest,
			responseBody:    `{"error":{"code":400,"message":"Invalid request: missing required field","status":"INVALID_ARGUMENT"}}`,
			expResponseBody: `{"type":"error","error":{"type":"INVALID_ARGUMENT","code":"400","message":"Invalid request: missing required field"}}`,
		},
		{
			name:            "gcp-anthropicai - /v1/chat/completions - error response",
			backend:         "gcp-anthropicai",
			path:            "/v1/chat/completions",
			responseType:    "",
			method:          http.MethodPost,
			requestBody:     `{"model":"claude-3-sonnet", "max_completion_tokens":1024, "messages":[{"role":"system","content":"You are a helpful assistant."}]}`,
			expPath:         "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:rawPredict",
			responseStatus:  "400",
			expStatus:       http.StatusBadRequest,
			responseBody:    `{"error":{"type":"invalid_request_error","code":400,"message":"Invalid request: missing required field","status":"INVALID_ARGUMENT"}}`,
			expResponseBody: `{"type":"error","error":{"type":"invalid_request_error","code":"400","message":"Invalid request: missing required field"}}`,
		},
		{
			name:            "openai - /v1/embeddings",
			backend:         "openai",
			path:            "/v1/embeddings",
			method:          http.MethodPost,
			requestBody:     `{"model":"text-embedding-ada-002","input":"The food was delicious and the waiter..."}`,
			expPath:         "/v1/embeddings",
			responseBody:    `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
		},
		{
			name:            "azure-openai - /v1/embeddings",
			backend:         "azure-openai",
			path:            "/v1/embeddings",
			method:          http.MethodPost,
			requestBody:     `{"model":"text-embedding-ada-002","input":"The food was delicious and the waiter..."}`,
			expPath:         "/openai/deployments/text-embedding-ada-002/embeddings",
			responseBody:    `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
		},
		{
			name:            "openai - /v1/embeddings - gzip",
			backend:         "openai",
			responseType:    "gzip",
			path:            "/v1/embeddings",
			method:          http.MethodPost,
			requestBody:     `{"model":"text-embedding-ada-002","input":"The food was delicious and the waiter..."}`,
			expPath:         "/v1/embeddings",
			responseBody:    `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
		},
		{
			name:            "azure-openai - /v1/embeddings - gzip",
			backend:         "azure-openai",
			responseType:    "gzip",
			path:            "/v1/embeddings",
			method:          http.MethodPost,
			requestBody:     `{"model":"text-embedding-ada-002","input":"The food was delicious and the waiter..."}`,
			expPath:         "/openai/deployments/text-embedding-ada-002/embeddings",
			responseBody:    `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"object":"list","data":[{"object":"embedding","embedding":[0.0023064255,-0.009327292,-0.0028842222],"index":0}],"model":"text-embedding-ada-002","usage":{"prompt_tokens":8,"total_tokens":8}}`,
		},
		{
			name:            "openai - /v1/embeddings - error response",
			backend:         "openai",
			path:            "/v1/embeddings",
			responseType:    "",
			method:          http.MethodPost,
			requestBody:     `{"model":"text-embedding-ada-002","input":""}`,
			expPath:         "/v1/embeddings",
			responseStatus:  "400",
			expStatus:       http.StatusBadRequest,
			responseBody:    `{"error": {"message": "input cannot be empty", "type": "BadRequestError", "code": "400"}}`,
			expResponseBody: `{"error": {"message": "input cannot be empty", "type": "BadRequestError", "code": "400"}}`,
		},
		{
			name:            "azure-openai - /v1/embeddings - error response",
			backend:         "azure-openai",
			path:            "/v1/embeddings",
			responseType:    "",
			method:          http.MethodPost,
			requestBody:     `{"model":"text-embedding-ada-002","input":""}`,
			expPath:         "/openai/deployments/text-embedding-ada-002/embeddings",
			responseStatus:  "400",
			expStatus:       http.StatusBadRequest,
			responseBody:    `{"error": {"message": "input cannot be empty", "type": "BadRequestError", "code": "400"}}`,
			expResponseBody: `{"error": {"message": "input cannot be empty", "type": "BadRequestError", "code": "400"}}`,
		},
		{
			name:              "gcp-vertexai - /v1/embeddings - simple string",
			backend:           "gcp-vertexai",
			path:              "/v1/embeddings",
			method:            http.MethodPost,
			requestBody:       `{"model":"text-embedding-004","input":"How do I reset my password?"}`,
			expRequestBody:    `{"instances":[{"content":"How do I reset my password?"}],"parameters":{}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/text-embedding-004:predict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"predictions":[{"embeddings":{"values":[0.5,1.0,1.5],"statistics":{"token_count":5,"truncated":false}}}]}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"data":[{"embedding":[0.5,1,1.5],"index":0,"object":"embedding"}],"model":"text-embedding-004","object":"list","usage":{"prompt_tokens":5,"total_tokens":5}}`,
		},
		{
			name:              "gcp-vertexai - /v1/embeddings - with task_type and title",
			backend:           "gcp-vertexai",
			path:              "/v1/embeddings",
			method:            http.MethodPost,
			requestBody:       `{"model":"text-embedding-004","input":{"content":"Machine learning fundamentals","task_type":"RETRIEVAL_DOCUMENT","title":"ML Guide"}}`,
			expRequestBody:    `{"instances":[{"content":"Machine learning fundamentals","task_type":"RETRIEVAL_DOCUMENT","title":"ML Guide"}],"parameters":{}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/text-embedding-004:predict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"predictions":[{"embeddings":{"values":[0.25,0.5,0.75],"statistics":{"token_count":7,"truncated":false}}}]}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"data":[{"embedding":[0.25,0.5,0.75],"index":0,"object":"embedding"}],"model":"text-embedding-004","object":"list","usage":{"prompt_tokens":7,"total_tokens":7}}`,
		},
		{
			name:              "gcp-vertexai - /v1/embeddings - array of strings",
			backend:           "gcp-vertexai",
			path:              "/v1/embeddings",
			method:            http.MethodPost,
			requestBody:       `{"model":"text-embedding-004","input":["First text","Second text","Third text"]}`,
			expRequestBody:    `{"instances":[{"content":"First text"},{"content":"Second text"},{"content":"Third text"}],"parameters":{}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/text-embedding-004:predict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"predictions":[{"embeddings":{"values":[0.5,1.0],"statistics":{"token_count":2,"truncated":false}}},{"embeddings":{"values":[1.5,2.0],"statistics":{"token_count":2,"truncated":false}}},{"embeddings":{"values":[2.5,3.0],"statistics":{"token_count":2,"truncated":false}}}]}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"data":[{"embedding":[0.5,1],"index":0,"object":"embedding"},{"embedding":[1.5,2],"index":1,"object":"embedding"},{"embedding":[2.5,3],"index":2,"object":"embedding"}],"model":"text-embedding-004","object":"list","usage":{"prompt_tokens":6,"total_tokens":6}}`,
		},
		{
			name:              "gcp-vertexai - /v1/embeddings - array of objects",
			backend:           "gcp-vertexai",
			path:              "/v1/embeddings",
			method:            http.MethodPost,
			requestBody:       `{"model":"text-embedding-004","input":[{"content":"Query about cats","task_type":"RETRIEVAL_QUERY"},{"content":"Document about dogs","task_type":"RETRIEVAL_DOCUMENT","title":"Dog Info"}]}`,
			expRequestBody:    `{"instances":[{"content":"Query about cats","task_type":"RETRIEVAL_QUERY"},{"content":"Document about dogs","task_type":"RETRIEVAL_DOCUMENT","title":"Dog Info"}],"parameters":{}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/text-embedding-004:predict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"predictions":[{"embeddings":{"values":[3.5,4.0],"statistics":{"token_count":4,"truncated":false}}},{"embeddings":{"values":[4.5,5.0],"statistics":{"token_count":5,"truncated":false}}}]}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"data":[{"embedding":[3.5,4],"index":0,"object":"embedding"},{"embedding":[4.5,5],"index":1,"object":"embedding"}],"model":"text-embedding-004","object":"list","usage":{"prompt_tokens":9,"total_tokens":9}}`,
		},
		{
			name:              "gcp-vertexai - /v1/embeddings - with dimensions",
			backend:           "gcp-vertexai",
			path:              "/v1/embeddings",
			method:            http.MethodPost,
			requestBody:       `{"model":"text-embedding-004","input":"Text for dimension testing","dimensions":256}`,
			expRequestBody:    `{"instances":[{"content":"Text for dimension testing"}],"parameters":{"outputDimensionality":256}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/text-embedding-004:predict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"predictions":[{"embeddings":{"values":[7.5,8.0,8.5],"statistics":{"token_count":4,"truncated":false}}}]}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"data":[{"embedding":[7.5,8,8.5],"index":0,"object":"embedding"}],"model":"text-embedding-004","object":"list","usage":{"prompt_tokens":4,"total_tokens":4}}`,
		},
		{
			name:              "gcp-vertexai - /v1/embeddings - error response",
			backend:           "gcp-vertexai",
			path:              "/v1/embeddings",
			method:            http.MethodPost,
			requestBody:       `{"model":"text-embedding-004","input":""}`,
			expRequestBody:    `{"instances":[{"content":""}],"parameters":{}}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/google/models/text-embedding-004:predict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    "400",
			expStatus:         http.StatusBadRequest,
			responseBody:      `{"error":{"code":400,"message":"Invalid embedding request","status":"INVALID_ARGUMENT"}}`,
			expResponseBody:   `{"type":"error","error":{"code":"400","message":"Invalid embedding request","type":"INVALID_ARGUMENT"}}`,
		},
		{
			name:                "openai - /v1/models",
			backend:             "openai",
			path:                "/v1/models",
			method:              http.MethodGet,
			expStatus:           http.StatusOK,
			expResponseBodyFunc: checkModels(expectedModels),
		},
		{
			name:    "openai - /v1/chat/completions - assistant text content",
			backend: "openai",
			path:    "/v1/chat/completions",
			method:  http.MethodPost,
			requestBody: `
{
       "model": "whatever",
       "messages": [
               {"role": "user", "content": [{"type": "text", "text": "hi sir"}]},
               {"role": "assistant","content": [{"type": "text", "text": "Hello! How can I assist you today?"}]},
               {"role": "user", "content": [{"type": "text", "text": "what are you?"}]}
       ]
}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"choices":[{"message":{"content":"This is a test."}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"choices":[{"message":{"content":"This is a test."}}]}`,
		},
		{
			name:              "gcp-anthropicai - /anthropic/v1/messages",
			backend:           "gcp-anthropicai",
			path:              "/anthropic/v1/messages",
			method:            http.MethodPost,
			requestBody:       `{"model":"claude-3-sonnet","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello, just a simple test message."}]}],"stream":false}`,
			expRequestBody:    `{"max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello, just a simple test message."}]}],"stream":false,"anthropic_version":"vertex-2023-10-16"}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:rawPredict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody:      `{"id":"msg_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from native Anthropic API!"}],"usage":{"input_tokens":8,"output_tokens":15}}`,
			expStatus:         http.StatusOK,
			expResponseBody:   `{"id":"msg_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from native Anthropic API!"}],"usage":{"input_tokens":8,"output_tokens":15}}`,
		},
		{
			name:              "gcp-anthropicai - /anthropic/v1/messages - streaming",
			backend:           "gcp-anthropicai",
			path:              "/anthropic/v1/messages",
			method:            http.MethodPost,
			responseType:      "sse",
			requestBody:       `{"model":"claude-3-sonnet","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Tell me a short joke"}]}],"stream":true}`,
			expRequestBody:    `{"max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Tell me a short joke"}]}],"stream":true,"anthropic_version":"vertex-2023-10-16"}`,
			expPath:           "/v1/projects/gcp-project-name/locations/gcp-region/publishers/anthropic/models/claude-3-sonnet:streamRawPredict",
			expRequestHeaders: map[string]string{"Authorization": "Bearer " + fakeGCPAuthToken},
			responseStatus:    strconv.Itoa(http.StatusOK),
			responseBody: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_789", "usage": {"input_tokens": 8, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Why don't scientists trust atoms? Because they make up everything!"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 8, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 15}}

event: message_stop
data: {"type": "message_stop"}

`,
			expStatus: http.StatusOK,
			expResponseBody: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_789", "usage": {"input_tokens": 8, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Why don't scientists trust atoms? Because they make up everything!"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 8, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 15}}

event: message_stop
data: {"type": "message_stop"}

`,
		},
		{
			name:              "anthropic - /anthropic/v1/messages",
			backend:           "anthropic",
			path:              "/anthropic/v1/messages",
			method:            http.MethodPost,
			expRequestHeaders: map[string]string{"x-api-key": "anthropic-api-key"},
			requestBody: `{
    "model": "foo",
    "max_tokens": 1000,
    "messages": [
      {
        "role": "user",
        "content": "say hi"
      }
    ]
  }`,
			expPath:      "/v1/messages",
			responseBody: `{"model":"foo","id":"msg_01J5gW6Sffiem6avXSAooZZw","type":"message","role":"assistant","content":[{"type":"text","text":"Hi! 👋 How can I help you today?"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":16,"service_tier":"standard"}}`,
			expStatus:    http.StatusOK,
		},
		{
			name:              "anthropic - /anthropic/v1/messages - streaming",
			backend:           "anthropic",
			path:              "/anthropic/v1/messages",
			method:            http.MethodPost,
			expRequestHeaders: map[string]string{"x-api-key": "anthropic-api-key"},
			responseType:      "sse",
			requestBody: `{
    "model": "foo",
    "max_tokens": 1000,
    "messages": [
      {
        "role": "user",
        "content": "say hi"
      }
    ], "stream": true
  }`,
			expPath: "/v1/messages",
			responseBody: `
event: message_start
data: {"type":"message_start","message":{"model":"foo","id":"msg_01BfvfMsg2gBzwsk6PZRLtDg","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":1,"service_tier":"standard"}}    }

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}      }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}           }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"! 👋 How"}      }

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" can I help you today?"}   }

event: content_block_stop
data: {"type":"content_block_stop","index":0             }

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":16}               }

event: message_stop
data: {"type":"message_stop"       }
`,
			expStatus: http.StatusOK,
		},
		{
			name:              "anthropic - /anthropic/v1/messages - streaming with gzip content-encoding",
			backend:           "anthropic",
			path:              "/anthropic/v1/messages",
			method:            http.MethodPost,
			expRequestHeaders: map[string]string{"x-api-key": "anthropic-api-key"},
			responseType:      "sse-gzip",
			requestBody: `{
    "model": "foo",
    "max_tokens": 1000,
    "messages": [
      {
        "role": "user",
        "content": "say hi"
      }
    ], "stream": true
  }`,
			expPath: "/v1/messages",
			responseBody: `
event: message_start
data: {"type":"message_start","message":{"model":"foo","id":"msg_gzip_test","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"! How can I help?"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}
`,
			expStatus: http.StatusOK,
		},
		{
			name:            "aws-anthropic - /anthropic/v1/messages",
			backend:         "aws-anthropic",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"anthropic.claude-3-sonnet-20240229-v1:0","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello from AWS!"}]}],"stream":false}`,
			expRequestBody:  `{"max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello from AWS!"}]}],"anthropic_version":"bedrock-2023-05-31"}`,
			expPath:         "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke",
			responseStatus:  strconv.Itoa(http.StatusOK),
			responseBody:    `{"id":"msg_aws_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from AWS Anthropic!"}],"usage":{"input_tokens":10,"output_tokens":20}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"msg_aws_123","type":"message","role":"assistant","stop_reason": "end_turn", "content":[{"type":"text","text":"Hello from AWS Anthropic!"}],"usage":{"input_tokens":10,"output_tokens":20}}`,
		},
		{
			name:           "aws-anthropic - /anthropic/v1/messages - streaming",
			backend:        "aws-anthropic",
			path:           "/anthropic/v1/messages",
			method:         http.MethodPost,
			responseType:   "aws-event-stream",
			requestBody:    `{"model":"anthropic.claude-3-haiku-20240307-v1:0","max_tokens":150,"messages":[{"role":"user","content":[{"type":"text","text":"Tell me a joke"}]}],"stream":true}`,
			expRequestBody: `{"max_tokens":150,"messages":[{"role":"user","content":[{"type":"text","text":"Tell me a joke"}]}],"anthropic_version":"bedrock-2023-05-31"}`,
			expPath:        "/model/anthropic.claude-3-haiku-20240307-v1:0/invoke-with-response-stream",
			responseStatus: strconv.Itoa(http.StatusOK),
			responseBody: `{"bytes":"eyJ0eXBlIjoibWVzc2FnZV9zdGFydCIsIm1lc3NhZ2UiOnsibW9kZWwiOiJjbGF1ZGUtc29ubmV0LTQtNS0yMDI1MDkyOSIsImlkIjoibXNnX2JkcmtfMDEyR0JQenBjb01DTFAxYjJwY0pzU0hrIiwidHlwZSI6Im1lc3NhZ2UiLCJyb2xlIjoiYXNzaXN0YW50IiwiY29udGVudCI6W10sInN0b3BfcmVhc29uIjpudWxsLCJzdG9wX3NlcXVlbmNlIjpudWxsLCJ1c2FnZSI6eyJpbnB1dF90b2tlbnMiOjEwLCJjYWNoZV9jcmVhdGlvbl9pbnB1dF90b2tlbnMiOjAsImNhY2hlX3JlYWRfaW5wdXRfdG9rZW5zIjowLCJjYWNoZV9jcmVhdGlvbiI6eyJlcGhlbWVyYWxfNW1faW5wdXRfdG9rZW5zIjowLCJlcGhlbWVyYWxfMWhfaW5wdXRfdG9rZW5zIjowfSwib3V0cHV0X3Rva2VucyI6MX19fQ==","p":"abcdefghijklmnopqr"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19zdGFydCIsImluZGV4IjowLCJjb250ZW50X2Jsb2NrIjp7InR5cGUiOiJ0ZXh0IiwidGV4dCI6IiJ9fQ==","p":"abcdefghijklmnopqrstuvwxy"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19kZWx0YSIsImluZGV4IjowLCJkZWx0YSI6eyJ0eXBlIjoidGV4dF9kZWx0YSIsInRleHQiOiJIaSJ9fQ==","p":"abcdefghijklmnopqrstuvwxyzABCDEF"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19kZWx0YSIsImluZGV4IjowLCJkZWx0YSI6eyJ0eXBlIjoidGV4dF9kZWx0YSIsInRleHQiOiIhIn19","p":"abcdefghijklmnopqrstuvwxyzAB"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19kZWx0YSIsImluZGV4IjowLCJkZWx0YSI6eyJ0eXBlIjoidGV4dF9kZWx0YSIsInRleHQiOiIgIn19","p":"abcdefghijklmnopqrstuvwxyzABCDEFGHIJK"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19kZWx0YSIsImluZGV4IjowLCJkZWx0YSI6eyJ0eXBlIjoidGV4dF9kZWx0YSIsInRleHQiOiLwn5GLIEhvdyJ9fQ==","p":"abcdefghijklmnopqrstuvwxyzABCDEFG"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19kZWx0YSIsImluZGV4IjowLCJkZWx0YSI6eyJ0eXBlIjoidGV4dF9kZWx0YSIsInRleHQiOiIgYXJlIHlvdSBkb2luZyB0b2RheT8ifX0=","p":"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ01234"}
{"bytes":"eyJ0eXBlIjoiY29udGVudF9ibG9ja19zdG9wIiwiaW5kZXgiOjB9","p":"abcdefghijklmnopqrstuvwxyz"}
{"bytes":"eyJ0eXBlIjoibWVzc2FnZV9kZWx0YSIsImRlbHRhIjp7InN0b3BfcmVhc29uIjoiZW5kX3R1cm4iLCJzdG9wX3NlcXVlbmNlIjpudWxsfSwidXNhZ2UiOnsiaW5wdXRfdG9rZW5zIjoxMCwiY2FjaGVfY3JlYXRpb25faW5wdXRfdG9rZW5zIjowLCJjYWNoZV9yZWFkX2lucHV0X3Rva2VucyI6MCwib3V0cHV0X3Rva2VucyI6MTV9fQ==","p":"abcdefghijklmnopqrstu"}
{"bytes":"eyJ0eXBlIjoibWVzc2FnZV9zdG9wIiwiYW1hem9uLWJlZHJvY2staW52b2NhdGlvbk1ldHJpY3MiOnsiaW5wdXRUb2tlbkNvdW50IjoxMCwib3V0cHV0VG9rZW5Db3VudCI6MTUsImludm9jYXRpb25MYXRlbmN5IjoxNzk4LCJmaXJzdEJ5dGVMYXRlbmN5IjoxNTA3fX0=","p":"ab"}
`,
			expStatus: http.StatusOK,
			expResponseBody: `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_bdrk_012GBPzpcoMCLP1b2pcJsSHk","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"👋 How"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" are you doing today?"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":15}}

event: message_stop
data: {"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":10,"outputTokenCount":15,"invocationLatency":1798,"firstByteLatency":1507}}

`,
		},
		{
			name:            "aws-anthropic - /anthropic/v1/messages - error response",
			backend:         "aws-anthropic",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"anthropic.claude-3-sonnet-20240229-v1:0","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Test error"}]}]}`,
			expPath:         "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke",
			responseStatus:  strconv.Itoa(http.StatusBadRequest),
			responseBody:    `{"type":"error","error":{"type":"validation_error","message":"Invalid request format"}}`,
			expStatus:       http.StatusBadRequest,
			expResponseBody: `{"type":"error","error":{"type":"validation_error","message":"Invalid request format"}}`,
		},
		{
			name:            "aws-bedrock - /anthropic/v1/messages",
			backend:         "aws-bedrock",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"anthropic.claude-3-sonnet-20240229-v1:0","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello from AWS Bedrock messages!"}]}]}`,
			expRequestBody:  `{"inferenceConfig":{"maxTokens":100},"messages":[{"content":[{"text":"Hello from AWS Bedrock messages!"}],"role":"user"}]}`,
			expPath:         "/model/anthropic.claude-3-sonnet-20240229-v1:0/converse",
			responseStatus:  strconv.Itoa(http.StatusOK),
			responseHeaders: "x-amzn-requestid:bedrock-msg-123",
			responseBody:    `{"output":{"message":{"content":[{"text":"Hello from AWS Bedrock!"}],"role":"assistant"}},"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"bedrock-msg-123","type":"message","role":"assistant","content":[{"type":"text","text":"Hello from AWS Bedrock!"}],"model":"anthropic.claude-3-sonnet-20240229-v1:0","stop_reason":"end_turn","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":10,"output_tokens":20}}`,
		},
		{
			name:            "body-mutation - /v1/chat/completions - OpenAI backend with route-level body mutations",
			backend:         "body-mutation",
			path:            "/v1/chat/completions",
			method:          http.MethodPost,
			requestBody:     `{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}],"temperature":0.9,"stream_options":{"include_usage":true}}`,
			expPath:         "/v1/chat/completions",
			expRequestBody:  `{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}],"temperature":0.5,"max_tokens":150,"custom_field":"route-level-value"}`,
			responseBody:    `{"choices":[{"message":{"content":"Hello! How can I help you?"}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"choices":[{"message":{"content":"Hello! How can I help you?"}}]}`,
		},
		{
			name:            "body-mutation-anthropic - /anthropic/v1/messages - Anthropic backend with route-level body mutations",
			backend:         "body-mutation-anthropic",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"claude-3-sonnet","max_tokens":50,"messages":[{"role":"user","content":"Hello"}],"temperature":0.1}`,
			expPath:         "/v1/messages",
			expRequestBody:  `{"model":"claude-3-sonnet","max_tokens":200,"messages":[{"role":"user","content":"Hello"}],"temperature":0.7}`,
			responseBody:    `{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"Hello! How can I assist you?"}],"usage":{"input_tokens":5,"output_tokens":10}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"Hello! How can I assist you?"}],"usage":{"input_tokens":5,"output_tokens":10}}`,
		},
		{
			name:            "openai - /v1/responses",
			backend:         "openai",
			path:            "/v1/responses",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","input": "Say this is a test."}`,
			expPath:         "/v1/responses",
			responseBody:    `{"id":"resp_67cc","object":"response","created_at":1741476542,"status":"completed","model":"something","output":[{"type":"message","id":"msg_67c","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test."}]}],"parallel_tool_calls":true,"store":true,"temperature":1,"text":{"format":{"type":"text"}},"tool_choice":"auto","top_p":1,"truncation":"disabled","usage":{"input_tokens":16,"input_tokens_details":{"cached_tokens":5},"output_tokens":6,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":22}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"resp_67cc","object":"response","created_at":1741476542,"status":"completed","model":"something","output":[{"type":"message","id":"msg_67c","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test."}]}],"parallel_tool_calls":true,"store":true,"temperature":1,"text":{"format":{"type":"text"}},"tool_choice":"auto","top_p":1,"truncation":"disabled","usage":{"input_tokens":16,"input_tokens_details":{"cached_tokens":5},"output_tokens":6,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":22}}`,
		},
		{
			name:            "azure-openai - /v1/responses",
			backend:         "azure-openai",
			path:            "/v1/responses",
			method:          http.MethodPost,
			requestBody:     `{"model":"something","input": "Say this is a test."}`,
			expPath:         "/openai/responses",
			responseBody:    `{"id":"resp_67cc","object":"response","created_at":1741476542,"status":"completed","model":"something","output":[{"type":"message","id":"msg_67c","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test."}]}],"parallel_tool_calls":true,"store":true,"temperature":1,"text":{"format":{"type":"text"}},"tool_choice":"auto","top_p":1,"truncation":"disabled","usage":{"input_tokens":16,"input_tokens_details":{"cached_tokens":5},"output_tokens":6,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":22}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"resp_67cc","object":"response","created_at":1741476542,"status":"completed","model":"something","output":[{"type":"message","id":"msg_67c","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test."}]}],"parallel_tool_calls":true,"store":true,"temperature":1,"text":{"format":{"type":"text"}},"tool_choice":"auto","top_p":1,"truncation":"disabled","usage":{"input_tokens":16,"input_tokens_details":{"cached_tokens":5},"output_tokens":6,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":22}}`,
		},
		{
			name:           "openai - /v1/responses - stream",
			backend:        "openai",
			path:           "/v1/responses",
			method:         http.MethodPost,
			responseType:   "sse",
			requestBody:    `{"model":"something","input": "Say this is a test.", "stream": true, "instructions":"You are a helpful assistant."}`,
			expPath:        "/v1/responses",
			expStatus:      http.StatusOK,
			responseStatus: strconv.Itoa(http.StatusOK),
			responseBody: `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_67c","object":"response","created_at":1741290958,"status":"in_progress","error":null,"incomplete_details":null,"instructions":"You are a helpful assistant.","max_output_tokens":null,"model":"gpt-4.1-2025-04-14","output":[],"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":null,"summary":null},"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled","usage":null,"user":null,"metadata":{}}}

event: response.in_progress
data: {"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_67c","object":"response","created_at":1741290958,"status":"in_progress","error":null,"incomplete_details":null,"instructions":"You are a helpful assistant.","max_output_tokens":null,"model":"gpt-4.1-2025-04-14","output":[],"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":null,"summary":null},"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled","usage":null,"user":null,"metadata":{}}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_67c","type":"message","status":"in_progress","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":3,"item_id":"msg_67c","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_67c","output_index":0,"content_index":0,"delta":"This"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_67c","output_index":0,"content_index":0,"delta":"is"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":6,"item_id":"msg_67c","output_index":0,"content_index":0,"delta":"a test."}

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":7,"item_id":"msg_67c","output_index":0,"content_index":0,"text":"This is a test."}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":8,"item_id":"msg_67c","output_index":0,"content_index":0,"part":{"type":"output_text","text":"This is a test.","annotations":[]}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":9,"output_index":0,"item":{"id":"msg_67c","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test.","annotations":[]}]}}

event: response.completed
data: {"type":"response.completed","sequence_number":10,"response":{"id":"resp_67c","object":"response","created_at":1741290958,"status":"completed","error":null,"incomplete_details":null,"instructions":"You are a helpful assistant.","max_output_tokens":null,"model":"gpt-4.1-2025-04-14","output":[{"id":"msg_67c","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test.","annotations":[]}]}],"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":null,"summary":null},"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled","usage":{"input_tokens":37,"output_tokens":11,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":48},"user":null,"metadata":{}}}
`,
			expResponseBody: `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_67c","object":"response","created_at":1741290958,"status":"in_progress","error":null,"incomplete_details":null,"instructions":"You are a helpful assistant.","max_output_tokens":null,"model":"gpt-4.1-2025-04-14","output":[],"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":null,"summary":null},"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled","usage":null,"user":null,"metadata":{}}}

event: response.in_progress
data: {"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_67c","object":"response","created_at":1741290958,"status":"in_progress","error":null,"incomplete_details":null,"instructions":"You are a helpful assistant.","max_output_tokens":null,"model":"gpt-4.1-2025-04-14","output":[],"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":null,"summary":null},"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled","usage":null,"user":null,"metadata":{}}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_67c","type":"message","status":"in_progress","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":3,"item_id":"msg_67c","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_67c","output_index":0,"content_index":0,"delta":"This"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_67c","output_index":0,"content_index":0,"delta":"is"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":6,"item_id":"msg_67c","output_index":0,"content_index":0,"delta":"a test."}

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":7,"item_id":"msg_67c","output_index":0,"content_index":0,"text":"This is a test."}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":8,"item_id":"msg_67c","output_index":0,"content_index":0,"part":{"type":"output_text","text":"This is a test.","annotations":[]}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":9,"output_index":0,"item":{"id":"msg_67c","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test.","annotations":[]}]}}

event: response.completed
data: {"type":"response.completed","sequence_number":10,"response":{"id":"resp_67c","object":"response","created_at":1741290958,"status":"completed","error":null,"incomplete_details":null,"instructions":"You are a helpful assistant.","max_output_tokens":null,"model":"gpt-4.1-2025-04-14","output":[{"id":"msg_67c","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"This is a test.","annotations":[]}]}],"parallel_tool_calls":true,"previous_response_id":null,"reasoning":{"effort":null,"summary":null},"store":true,"temperature":1.0,"text":{"format":{"type":"text"}},"tool_choice":"auto","tools":[],"top_p":1.0,"truncation":"disabled","usage":{"input_tokens":37,"output_tokens":11,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":48},"user":null,"metadata":{}}}
`,
		},
		{
			name:            "anthropic-openai - /anthropic/v1/messages - OpenAI Backend with Anthropic messages endpoint",
			backend:         "openai",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model": "foo","max_tokens": 1000, "messages": [{"role": "user", "content": "say hi"}]}`,
			expRequestBody:  `{"messages":[{"content":"say hi","role":"user"}],"model":"foo","max_completion_tokens":1000}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"choices":[{"message":{"content":"hi, this is a test."}}]}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"","type":"message","role":"assistant","content":[{"type":"text","text":"hi, this is a test."}],"model":"foo","stop_reason":"end_turn","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":0,"output_tokens":0}}`,
		},
		{
			name:            "anthropic-openai - /anthropic/v1/messages - non-streaming with system prompt",
			backend:         "openai",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"claude-test","max_tokens":100,"system":"You are a helpful assistant.","messages":[{"role":"user","content":"Hello"}]}`,
			expRequestBody:  `{"messages":[{"content":"You are a helpful assistant.","role":"system"},{"content":"Hello","role":"user"}],"model":"claude-test","max_completion_tokens":100}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"id":"chatcmpl-sys","model":"gpt-4o","choices":[{"message":{"content":"Hello! How can I help you today?","role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":10}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"chatcmpl-sys","type":"message","role":"assistant","content":[{"type":"text","text":"Hello! How can I help you today?"}],"model":"gpt-4o","stop_reason":"end_turn","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":25,"output_tokens":10}}`,
		},
		{
			name:            "anthropic-openai - /anthropic/v1/messages - non-streaming tool call",
			backend:         "openai",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"claude-test","max_tokens":200,"messages":[{"role":"user","content":"What's the weather in Paris?"}],"tools":[{"type":"custom","name":"get_weather","description":"Get weather info","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}]}`,
			expRequestBody:  `{"messages":[{"content":"What's the weather in Paris?","role":"user"}],"model":"claude-test","max_completion_tokens":200,"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather info","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}]}`,
			expPath:         "/v1/chat/completions",
			responseBody:    `{"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":50,"completion_tokens":15}}`,
			expStatus:       http.StatusOK,
			expResponseBody: `{"id":"chatcmpl-tool","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_abc","name":"get_weather","input":{"location":"Paris"}}],"model":"gpt-4o","stop_reason":"tool_use","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":50,"output_tokens":15}}`,
		},
		{
			name:           "anthropic-openai - /anthropic/v1/messages - streaming text response",
			backend:        "openai",
			path:           "/anthropic/v1/messages",
			method:         http.MethodPost,
			responseType:   "sse",
			requestBody:    `{"model":"claude-test","max_tokens":100,"messages":[{"role":"user","content":"Say hi"}],"stream":true}`,
			expRequestBody: `{"messages":[{"content":"Say hi","role":"user"}],"model":"claude-test","max_completion_tokens":100,"stream":true,"stream_options":{"include_usage":true}}`,
			expPath:        "/v1/chat/completions",
			responseBody: `{"id":"chatcmpl-stream","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}],"usage":null}
{"id":"chatcmpl-stream","model":"gpt-4o","choices":[{"index":0,"delta":{"content":" there!"},"finish_reason":null}],"usage":null}
{"id":"chatcmpl-stream","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}
{"id":"chatcmpl-stream","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":3}}
[DONE]`,
			expStatus: http.StatusOK,
			expResponseBody: `event: message_start
data: {"type":"message_start","message":{"id":"chatcmpl-stream","type":"message","role":"assistant","content":[],"model":"gpt-4o","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there!"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}`,
		},
		{
			name:           "anthropic-openai - /anthropic/v1/messages - streaming tool call",
			backend:        "openai",
			path:           "/anthropic/v1/messages",
			method:         http.MethodPost,
			responseType:   "sse",
			requestBody:    `{"model":"claude-test","max_tokens":100,"messages":[{"role":"user","content":"Get the weather in Paris"}],"stream":true,"tools":[{"type":"custom","name":"get_weather","description":"Get weather info","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}]}`,
			expRequestBody: `{"messages":[{"content":"Get the weather in Paris","role":"user"}],"model":"claude-test","max_completion_tokens":100,"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather info","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}]}`,
			expPath:        "/v1/chat/completions",
			responseBody: `{"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}],"usage":null}
{"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":"}}]},"finish_reason":null}],"usage":null}
{"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":null}],"usage":null}
{"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":null}
{"id":"chatcmpl-tool","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":15}}
[DONE]`,
			expStatus: http.StatusOK,
			expResponseBody: `event: message_start
data: {"type":"message_start","message":{"id":"chatcmpl-tool","type":"message","role":"assistant","content":[],"model":"gpt-4o","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_abc","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":50,"output_tokens":15}}

event: message_stop
data: {"type":"message_stop"}`,
		},
		{
			name:            "anthropic-openai - /anthropic/v1/messages - OpenAI JSON error translated to Anthropic error",
			backend:         "openai",
			path:            "/anthropic/v1/messages",
			method:          http.MethodPost,
			requestBody:     `{"model":"claude-test","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`,
			expPath:         "/v1/chat/completions",
			expRequestBody:  `{"messages":[{"content":"Hello","role":"user"}],"model":"claude-test","max_completion_tokens":100}`,
			responseBody:    `{"error":{"type":"invalid_request_error","message":"Model not found","code":"model_not_found"}}`,
			responseStatus:  "400",
			expStatus:       http.StatusBadRequest,
			expResponseBody: `{"error":{"message":"Model not found","type":"invalid_request_error"},"request_id":"","type":"error"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listenerAddress := fmt.Sprintf("http://localhost:%d", listenerPort)
			req, err := http.NewRequestWithContext(t.Context(), tc.method, listenerAddress+tc.path, strings.NewReader(tc.requestBody))
			require.NoError(t, err)
			req.Header.Set("x-test-backend", tc.backend)
			req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.responseBody)))
			req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.expPath)))
			req.Header.Set(testupstreamlib.ResponseStatusKey, tc.responseStatus)

			var expRequestHeaders []string
			for k, v := range tc.expRequestHeaders {
				expRequestHeaders = append(expRequestHeaders, fmt.Sprintf("%s:%s", k, v))
			}
			if len(expRequestHeaders) > 0 {
				req.Header.Set(
					testupstreamlib.ExpectedHeadersKey,
					base64.StdEncoding.EncodeToString(
						[]byte(strings.Join(expRequestHeaders, ","))),
				)
			}

			if tc.expRawQuery != "" {
				req.Header.Set(testupstreamlib.ExpectedRawQueryHeaderKey, tc.expRawQuery)
			}
			if tc.expHost != "" {
				req.Header.Set(testupstreamlib.ExpectedHostKey, tc.expHost)
			}
			if tc.responseType != "" {
				req.Header.Set(testupstreamlib.ResponseTypeKey, tc.responseType)
			}
			if tc.responseHeaders != "" {
				req.Header.Set(testupstreamlib.ResponseHeadersKey, base64.StdEncoding.EncodeToString([]byte(tc.responseHeaders)))
			}
			if tc.expRequestBody != "" {
				req.Header.Set(testupstreamlib.ExpectedRequestBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.expRequestBody)))
			}
			for k, v := range tc.requestHeaders {
				req.Header.Set(k, v)
			}
			if len(tc.nonExpectedRequestHeaders) > 0 {
				req.Header.Set(
					testupstreamlib.NonExpectedRequestHeadersKey,
					base64.StdEncoding.EncodeToString([]byte(strings.Join(tc.nonExpectedRequestHeaders, ","))),
				)
			}

			var lastErr error
			var lastStatusCode int
			var lastBody []byte
			var lastHeaders http.Header
			require.Eventually(t, func() bool {
				var resp *http.Response
				resp, lastErr = http.DefaultClient.Do(req)
				if lastErr != nil {
					return false
				}
				defer func() { _ = resp.Body.Close() }()

				// Only fail-fast on unexpected 5xx. Some test cases intentionally expect 5xx.
				if tc.expStatus < http.StatusInternalServerError {
					failIf5xx(t, resp, &was5xx)
				}

				lastBody, lastErr = io.ReadAll(resp.Body)
				if lastErr != nil {
					return false
				}
				t.Logf("Response status: %d", resp.StatusCode)
				t.Logf("Response headers: %+v", resp.Header)
				t.Logf("Response body: %s", string(lastBody))

				lastStatusCode = resp.StatusCode
				lastHeaders = resp.Header
				return resp.StatusCode == tc.expStatus
			}, eventuallyTimeout, eventuallyInterval,
				"Test failed - Last error: %v, Last status code: %d (expected: %d), Last body: %s",
				lastErr, lastStatusCode, tc.expStatus, lastBody)

			// Now perform assertions on the body outside the Eventually loop
			switch {
			case tc.expResponseBodyFunc != nil:
				tc.expResponseBodyFunc(t, lastBody)
			case tc.responseType != "" || tc.expStatus == http.StatusNotFound:
				expResponseBody := cmp.Or(tc.expResponseBody, tc.responseBody)

				bodyStr := m.ReplaceAllString(string(lastBody), "<UUID4-replaced>")
				bodyStr = createdReg.ReplaceAllString(bodyStr, `"created":123`)

				expectedResponseBody := m.ReplaceAllString(expResponseBody, "<UUID4-replaced>")
				expectedResponseBody = createdReg.ReplaceAllString(expectedResponseBody, `"created":123`)

				// Use plain-text comparison for streaming or 4xx error responses.
				require.Equal(t, strings.TrimSpace(expectedResponseBody), strings.TrimSpace(bodyStr), "Response body mismatch")
			default:
				expResponseBody := cmp.Or(tc.expResponseBody, tc.responseBody)
				// Use JSON comparison for regular responses.
				bodyStr := m.ReplaceAllString(string(lastBody), "<UUID4-replaced>")
				bodyStr = createdReg.ReplaceAllString(bodyStr, `"created":123`)

				expectedResponseBody := m.ReplaceAllString(expResponseBody, "<UUID4-replaced>")
				expectedResponseBody = createdReg.ReplaceAllString(expectedResponseBody, `"created":123`)

				require.JSONEq(t, expectedResponseBody, bodyStr, "Response body mismatch")
			}

			for k, v := range tc.expResponseHeaders {
				require.Equal(t, v, lastHeaders.Get(k), "Header %s mismatch", k)
			}
		})
	}

	t.Run("stream non blocking", func(t *testing.T) {
		if was5xx {
			return // rather than also failing subsequent tests, which confuses root cause.
		}

		// This receives a stream of 20 event messages. The testuptream server sleeps 200 ms between each message.
		// Therefore, if envoy fails to process the response in a streaming manner, the test will fail taking more than 4 seconds.
		listenerAddress := fmt.Sprintf("http://localhost:%d", listenerPort)
		client := openaigo.NewClient(
			option.WithBaseURL(listenerAddress+"/v1/"),
			option.WithHeader("x-test-backend", "openai"),
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
			Model: "something",
		})
		defer func() {
			_ = stream.Close()
		}()

		asserted := false
		for stream.Next() {
			chunk := stream.Current()
			if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.Content == "" {
				continue
			}
			t.Logf("%v: %v", time.Now(), chunk.Choices[0].Delta.Content)
			// Check each event is received less than a second after the previous one.
			require.Less(t, time.Since(start), 3*time.Second)
			start = time.Now()
			asserted = true
		}
		require.True(t, asserted)
		require.NoError(t, stream.Err())
	})
}

func checkModels(want openai.ModelList) func(t require.TestingT, body []byte) {
	return func(t require.TestingT, body []byte) {
		var models openai.ModelList
		require.NoError(t, json.Unmarshal(body, &models))
		require.Len(t, models.Data, len(want.Data))
		require.Equal(t, want, models)
	}
}

const (
	toolCallResultsRequestBody = `{
  "model": "gpt-4-0613",
  "max_completion_tokens":1024,
  "messages": [
    {"role": "user","content": "List the files in the /tmp directory"},
    {
      "role": "assistant",
      "tool_calls": [
        {
          "id": "call_abc123",
          "type": "function",
          "function": {
            "name": "list_files",
            "arguments": "{ \"path\": \"/tmp\" }"
          }
        }
      ]
    },
    {
      "role": "tool",
      "tool_call_id": "call_abc123",
      "content": "[\"foo.txt\", \"bar.log\", \"data.csv\"]"
    }
  ]
}`
)

// TestStreamingUsageInclusionWithCosts tests that streaming responses include usage
// when LLM request costs are configured (both global and route-scoped).
func TestStreamingUsageInclusionWithCosts(t *testing.T) {
	// Test cases that verify usage is forced to be included when costs are configured
	type testCase struct {
		name            string
		backend         string
		requestBody     string
		expRequestBody  string
		responseBody    string
		expResponseBody string
	}

	testCases := []testCase{
		{
			name:           "streaming - forced to include usage",
			backend:        "openai",
			requestBody:    `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true, "stream_options": {"include_usage": false}}`,
			expRequestBody: `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true, "stream_options": {"include_usage": true}}`,
			responseBody: `
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
[DONE]
`,
			expResponseBody: `data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]

`,
		},
		{
			name:           "streaming - forced to include usage without stream_options",
			backend:        "openai",
			requestBody:    `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true}`,
			expRequestBody: `{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true,"stream_options":{"include_usage":true}}`,
			responseBody: `
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
[DONE]
`,
			expResponseBody: `data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]

`,
		},
		{
			name:           "streaming - model override forced to include usage",
			backend:        "modelname-override",
			requestBody:    `{"model":"requested-model","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true, "stream_options": {"include_usage": false}}`,
			expRequestBody: `{"model":"override-model","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true, "stream_options": {"include_usage": true}}`,
			responseBody: `
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
[DONE]
`,
			expResponseBody: `data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]

`,
		},
		{
			name:           "streaming - model override without stream_options forced to include usage",
			backend:        "modelname-override",
			requestBody:    `{"model":"requested-model","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true}`,
			expRequestBody: `{"model":"override-model","messages":[{"role":"system","content":"You are a chatbot."}], "stream": true,"stream_options":{"include_usage":true}}`,
			responseBody: `
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
[DONE]
`,
			expResponseBody: `data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-foo","object":"chat.completion.chunk","created":1731618222,"model":"gpt-4o-mini-2024-07-18","system_fingerprint":"fp_0ba0d124f1","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":12,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}

data: [DONE]

`,
		},
	}

	// Test with both global and route-scoped costs
	costTypes := []struct {
		name        string
		globalCosts []filterapi.GlobalLLMRequestCost
		routeCosts  []filterapi.LLMRequestCost
	}{
		{
			name: "global costs",
			globalCosts: []filterapi.GlobalLLMRequestCost{
				{MetadataKey: "used_token", Type: filterapi.LLMRequestCostTypeInputToken},
			},
		},
		{
			name: "route-scoped costs",
			routeCosts: []filterapi.LLMRequestCost{
				{MetadataKey: "used_token", Type: filterapi.LLMRequestCostTypeInputToken, RouteName: "test-ns/openai-route"},
				{MetadataKey: "used_token", Type: filterapi.LLMRequestCostTypeInputToken, RouteName: "test-ns/modelname-override-route"},
			},
		},
	}

	for _, costType := range costTypes {
		t.Run(costType.name, func(t *testing.T) {
			config := &filterapi.Config{
				Version:               version.Parse(),
				GlobalLLMRequestCosts: costType.globalCosts,
				LLMRequestCosts:       costType.routeCosts,
				Backends: []filterapi.Backend{
					testUpstreamOpenAIBackend,
					testUpstreamModelNameOverride,
				},
			}

			configBytes, err := yaml.Marshal(config)
			require.NoError(t, err)
			env := startTestEnvironment(t, string(configBytes), true, false)
			listenerPort := env.EnvoyListenerPort()

			for _, tc := range testCases {
				t.Run(tc.name, func(t *testing.T) {
					listenerAddress := fmt.Sprintf("http://localhost:%d", listenerPort)
					req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, listenerAddress+"/v1/chat/completions", strings.NewReader(tc.requestBody))
					require.NoError(t, err)
					req.Header.Set("x-test-backend", tc.backend)
					req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.responseBody)))
					req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
					req.Header.Set(testupstreamlib.ResponseTypeKey, "sse")

					if tc.expRequestBody != "" {
						req.Header.Set(testupstreamlib.ExpectedRequestBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(tc.expRequestBody)))
					}

					resp, err := http.DefaultClient.Do(req)
					require.NoError(t, err)
					defer resp.Body.Close()

					require.Equal(t, http.StatusOK, resp.StatusCode)

					actualBody, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.Equal(t, tc.expResponseBody, string(actualBody))
				})
			}
		})
	}
}

// TestLocalReplyIsNotReprocessedAsUpstreamResponse tests that an error the gateway answers itself
// reaches the client intact and is counted once, since Envoy sends such a local reply back through
// the response path.
func TestLocalReplyIsNotReprocessedAsUpstreamResponse(t *testing.T) {
	config := &filterapi.Config{
		Version: version.Parse(),
		Backends: []filterapi.Backend{
			testUpstreamAWSAnthropicBackend,
			testUpstreamOpenAIRequiringPerRequestCredential,
			alwaysFailingBackend, // a fallback endpoint of the openai cluster.
		},
	}
	configBytes, err := yaml.Marshal(config)
	require.NoError(t, err)
	env := startTestEnvironment(t, string(configBytes), true, false)

	for _, tc := range []struct {
		name       string
		backend    string
		body       string
		expStatus  int
		expMessage string
	}{
		{
			// Cross-schema: the error body would be re-translated on the way back.
			name:       "translation rejects the request body",
			backend:    "aws-anthropic",
			body:       `{"model":"anthropic.claude-3-sonnet-20240229-v1:0","temperature":2.0,"messages":[{"role":"user","content":"hi"}]}`,
			expStatus:  http.StatusUnprocessableEntity,
			expMessage: "temperature 2.00 is not supported by Anthropic",
		},
		{
			// "something" matches no x-ai-eg-model route, so x-test-backend selects the route.
			name:       "the per-request credential is missing",
			backend:    "openai",
			body:       `{"model":"something","messages":[{"role":"user","content":"hi"}]}`,
			expStatus:  http.StatusUnauthorized,
			expMessage: "missing upstream credential",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const requests = 2
			for range requests {
				req, rerr := http.NewRequest(http.MethodPost,
					fmt.Sprintf("http://localhost:%d/v1/chat/completions", env.EnvoyListenerPort()),
					strings.NewReader(tc.body))
				require.NoError(t, rerr)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("x-test-backend", tc.backend)

				resp, rerr := http.DefaultClient.Do(req)
				require.NoError(t, rerr)
				respBody, rerr := io.ReadAll(resp.Body)
				require.NoError(t, rerr)
				_ = resp.Body.Close()

				require.Equal(t, tc.expStatus, resp.StatusCode)
				require.Contains(t, string(respBody), tc.expMessage)
			}

			// One completion per request, not one per response-path pass.
			require.Equal(t, requests, requestCompletionCount(t, env.ExtProcAdminPort(), tc.backend))
		})
	}
}

// requestCompletionCount returns how many request completions extproc recorded for the backend,
// read from its Prometheus endpoint.
func requestCompletionCount(t *testing.T, adminPort int, backend string) int {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", adminPort))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// The provider name is what keeps the two backends' series apart.
	provider := map[string]string{"aws-anthropic": "aws.anthropic", "openai": "openai"}[backend]
	require.NotEmpty(t, provider, "unmapped backend %q", backend)

	total := 0
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "gen_ai_server_request_duration_seconds_count") {
			continue
		}
		if !strings.Contains(line, `gen_ai_provider_name="`+provider+`"`) {
			continue
		}
		fields := strings.Fields(line)
		count, err := strconv.Atoi(fields[len(fields)-1])
		require.NoError(t, err, "parsing %q", line)
		total += count
	}
	return total
}
