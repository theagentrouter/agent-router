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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// tenantIDAttribute is the attribute used for team ID in otel span and metrics.
// This is passed via a helm value to the AI Gateway deployment.
const tenantIDAttribute = "tenant.id"

// tenantIDMetricsLabel is the Prometheus label the tenantIDAttribute becomes when
// exported as a metric.
const tenantIDMetricsLabel = "tenant_id"

func Test_Examples_TokenRateLimit(t *testing.T) {
	const manifestDir = "../../examples/token_ratelimit"
	// Apply specific manifest files, not the entire directory (to avoid applying Helm values files)
	manifests := []string{
		manifestDir + "/redis.yaml",
		manifestDir + "/token_ratelimit.yaml",
	}
	for _, manifest := range manifests {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	}
	t.Cleanup(func() {
		for _, manifest := range manifests {
			_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
		}
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-token-ratelimit"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	// Wait for the redis pod to be ready so that the rate limit can be performed correctly.
	// Until the redis pod is ready, envoy-ratelimit deployment will be in CrashLoopBackOff, so restart it so
	// we can have it up and running faster in a clean state.
	require.NoError(t, e2elib.KubectlRestartDeployment(t.Context(), e2elib.EnvoyGatewayNamespace, "envoy-ratelimit"))
	e2elib.RequireWaitForPodReady(t, e2elib.EnvoyGatewayNamespace, "app.kubernetes.io/component=ratelimit")
	e2elib.RequireWaitForPodReady(t, "redis-system", "app=redis")

	const modelName = "rate-limit-funky-model"
	makeRequest := func(userID string, input, output, total int, cachedInput *int, expStatus int) {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
		defer fwd.Kill()

		requestBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`, modelName)
		const fakeResponseBodyTemplate = `{"choices":[{"message":{"content":"This is a test.","role":"assistant"}}],"stopReason":null,"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`
		const fakeResponseBodyTemplateWithCachedInput = `{"choices":[{"message":{"content":"This is a test.","role":"assistant"}}],"stopReason":null,"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}}`

		var fakeResponseBody string
		if cachedInput != nil {
			fakeResponseBody = fmt.Sprintf(fakeResponseBodyTemplateWithCachedInput, input, output, total, *cachedInput)
		} else {
			fakeResponseBody = fmt.Sprintf(fakeResponseBodyTemplate, input, output, total)
		}

		req, err := http.NewRequest(http.MethodPut, fwd.Address()+"/v1/chat/completions", strings.NewReader(requestBody))
		require.NoError(t, err)
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(fakeResponseBody)))
		req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
		req.Header.Set("x-tenant-id", userID)
		req.Header.Set("Host", "openai.com")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		if resp.StatusCode == http.StatusOK {
			var oaiBody openai.ChatCompletion
			require.NoError(t, json.Unmarshal(body, &oaiBody))
			// Sanity check the fake response is correctly parsed.
			require.Equal(t, "This is a test.", oaiBody.Choices[0].Message.Content)
			require.Equal(t, int64(input), oaiBody.Usage.PromptTokens)
			require.Equal(t, int64(output), oaiBody.Usage.CompletionTokens)
			require.Equal(t, int64(total), oaiBody.Usage.TotalTokens)
			if cachedInput != nil {
				require.NotNil(t, oaiBody.Usage.PromptTokensDetails)
				require.Equal(t, int64(*cachedInput), oaiBody.Usage.PromptTokensDetails.CachedTokens)
			}
		}
		require.Equal(t, expStatus, resp.StatusCode)
	}

	// Test the input token limit.
	baseID := int(time.Now().UnixNano()) // To avoid collision with previous runs.
	userID := strconv.Itoa(baseID)
	// This input number exceeds the limit.
	makeRequest(userID, 10000, 0, 0, nil, 200)
	// Any request with the same user ID should be rejected.
	makeRequest(userID, 0, 0, 0, nil, 429)

	// Test the output token limit.
	userID = strconv.Itoa(baseID + 1)
	// This output number exceeds the input limit, but should still be allowed.
	makeRequest(userID, 0, 20, 0, nil, 200)
	// This output number exceeds the output limit.
	makeRequest(userID, 0, 10000, 0, nil, 200)
	// Any request with the same user ID should be rejected.
	makeRequest(userID, 0, 0, 0, nil, 429)

	// Test the total token limit.
	userID = strconv.Itoa(baseID + 2)
	// This total number exceeds the input limit, but should still be allowed.
	makeRequest(userID, 0, 0, 20, nil, 200)
	// This total number exceeds the output limit, but should still be allowed.
	makeRequest(userID, 0, 0, 200, nil, 200)
	// This total number exceeds the total limit.
	makeRequest(userID, 0, 0, 1000000, nil, 200)
	// Any request with the same user ID should be rejected.
	makeRequest(userID, 0, 0, 0, nil, 429)

	// Test the cached input token limit.
	userID = strconv.Itoa(baseID + 3)
	// This cached input number exceeds the input limit, but should still be allowed.
	makeRequest(userID, 0, 0, 0, new(20), 200)
	// This output number exceeds the output limit.
	makeRequest(userID, 0, 0, 0, new(10000), 200)
	// Any request with the same user ID should be rejected.
	makeRequest(userID, 0, 0, 0, nil, 429)

	// Test the CEL token limit.
	userID = strconv.Itoa(baseID + 4)
	// When the input number is 3, the CEL expression returns 100000000 which exceeds the limit.
	makeRequest(userID, 3, 0, 0, nil, 200)
	// Any request with the same user ID should be rejected.
	makeRequest(userID, 0, 0, 0, nil, 429)

	require.Eventually(t, func() bool {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, "monitoring", "app=prometheus", 9090)
		defer fwd.Kill()
		// notice all labels are snake_case in Prometheus even though the otel inputs are dotted.
		query := fmt.Sprintf(
			`sum(gen_ai_client_token_usage_sum{gateway_envoyproxy_io_owning_gateway_name = "envoy-ai-gateway-token-ratelimit"}) by (gen_ai_request_model, gen_ai_token_type, %s)`,
			tenantIDMetricsLabel)
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/query?query=%s", fwd.Address(), url.QueryEscape(query)), nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("Failed to query Prometheus: %v", err)
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		t.Logf("Response: status=%d, body=%s", resp.StatusCode, string(body))
		if resp.StatusCode != http.StatusOK {
			t.Logf("Failed to query Prometheus: status=%s", resp.Status)
			return false
		}
		type prometheusResponse struct {
			Status string `json:"status"`
			Data   struct {
				ResultType string `json:"resultType"`
				Result     []struct {
					Metric map[string]string `json:"metric"`
					Value  []any             `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		var pr prometheusResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		require.Equal(t, "vector", pr.Data.ResultType)
		var actualTypes []string
		for _, result := range pr.Data.Result {
			require.Equal(t, modelName, result.Metric["gen_ai_request_model"])
			typ := result.Metric["gen_ai_token_type"]
			actualTypes = append(actualTypes, typ)
			// Look up based on the label, not the attribute name it was derived from!
			uID, ok := result.Metric[tenantIDMetricsLabel]
			require.True(t, ok, tenantIDMetricsLabel+" should be present in the metric")
			t.Logf("Type: %s, Value: %v, User ID: %s", typ, result.Value, uID)
		}
		// Metrics should include input, cached_input and output (total is a response
		// attribute, but not a metric).
		//
		// Note: We don't use require.Contains as that will crash the
		// eventually loop when there are no metrics, yet.
		if !slices.Contains(actualTypes, "input") || !slices.Contains(actualTypes, "cached_input") || !slices.Contains(actualTypes, "output") {
			t.Logf("waiting until there are both input and output tokens: %v", actualTypes)
			return false
		}
		return true
	}, 2*time.Minute, 1*time.Second)
}
