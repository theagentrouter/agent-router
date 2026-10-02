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
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// Test_Examples_BackendQuotaRateLimit tests the backend-level quota rate limiting
// using the QuotaPolicy CRD. This verifies that the upstream rate limit filter
// enforces per-model token quotas on requests to AIServiceBackends.
func Test_Examples_BackendQuotaRateLimit(t *testing.T) {
	// Apply Redis manifest (shared with token rate limit tests).
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/token_ratelimit/redis.yaml"))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), "../../examples/token_ratelimit/redis.yaml")
	})

	// Wait for the redis pod to be ready so that the rate limit service can connect.
	e2elib.RequireWaitForPodReady(t, "redis-system", "app=redis")

	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "testdata/backend_quota_ratelimit.yaml"))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), "testdata/backend_quota_ratelimit.yaml")
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-quota-ratelimit"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	// Wait for the AI Gateway rate limit service to be ready.
	e2elib.RequireWaitForPodReady(t, e2elib.EnvoyGatewayNamespace, "app=envoy-ai-gateway-ratelimit")

	// Flush any existing quota keys in Redis to start with a clean state.
	flushQuotaKeys(t)

	// makeRequest sends a chat completion request via the test upstream with the
	// specified total_tokens in the fake response and asserts the expected status code.
	makeRequest := func(modelName string, totalTokens int, expectedStatus int, headers ...http.Header) {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
		defer fwd.Kill()

		requestBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`, modelName)
		fakeResponseBody := fmt.Sprintf(
			`{"choices":[{"message":{"content":"This is a test.","role":"assistant"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":%d}}`,
			totalTokens,
		)

		req, err := http.NewRequest(http.MethodPut, fwd.Address()+"/v1/chat/completions", strings.NewReader(requestBody))
		require.NoError(t, err)
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(fakeResponseBody)))
		req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
		req.Header.Set("Host", "openai.com")
		for _, h := range headers {
			for k, vals := range h {
				for _, v := range vals {
					req.Header.Set(k, v)
				}
			}
		}

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, expectedStatus, resp.StatusCode, "unexpected status code, body: %s", string(body))
	}

	// Test per-model quota enforcement by verifying the quota counter in Redis.
	// The QuotaPolicy sets a quota of 10 total tokens per hour for "quota-test-model".
	t.Run("per-model quota", func(t *testing.T) {
		makeRequest("quota-test-model", 20, http.StatusOK)
		requireQuotaUsage(t, "quota-test-model", 21)
		makeRequest("quota-test-model", 5, http.StatusTooManyRequests)
		requireQuotaUsage(t, "quota-test-model", 22)
	})

	// The AI Gateway controller stamps a hash of the applicable QuotaPolicies onto the generated HTTPRoute
	// (the "aigateway.envoyproxy.io/quota-policy-hash" annotation). A QuotaPolicy change must re-stamp a
	// different hash, which is what forces Envoy Gateway to re-translate and re-run the extension
	// server's PostTranslateModify hook. The generated HTTPRoute shares the AIGatewayRoute's
	// name/namespace ("quota-test-model"/"default").
	t.Run("quota policy update is applied live", func(t *testing.T) {
		const routeName, routeNamespace = "quota-test-model", "default"

		// The annotation must be present initially because a QuotaPolicy targets this route's backend.
		var initialHash string
		require.Eventually(t, func() bool {
			initialHash = getHTTPRouteQuotaHash(t, routeNamespace, routeName)
			return initialHash != ""
		}, 30*time.Second, 500*time.Millisecond, "expected quota-policy-hash annotation to be set")

		// Update the QuotaPolicy limit live (no controller/Envoy restart).
		require.NoError(t, e2elib.Kubectl(t.Context(), "patch", "quotapolicy",
			"envoy-ai-gateway-quota-ratelimit-policy", "-n", routeNamespace,
			"--type=merge",
			"-p", `{"spec":{"perModelQuotas":[{"modelName":"quota-test-model","quota":{"mode":"Shared","defaultBucket":{"limit":5,"duration":"1h"}}}]}}`,
		).Run())

		// The controller must re-stamp a different hash, proving the HTTPRoute was actually updated
		// (and thus Envoy Gateway re-translated) in response to the QuotaPolicy change.
		require.Eventually(t, func() bool {
			return getHTTPRouteQuotaHash(t, routeNamespace, routeName) != initialHash
		}, 30*time.Second, 500*time.Millisecond,
			"quota-policy-hash annotation did not change after updating the QuotaPolicy")
	})

	// Deleting the QuotaPolicy must re-stamp the generated HTTPRoute so the quota-policy-hash annotation
	// is removed, proving the controller notified the AIGatewayRoute on delete (via the finalizer) and
	// Envoy Gateway re-translated without the deleted policy. This runs last because it removes the only
	// QuotaPolicy targeting the backend.
	t.Run("quota policy delete is applied live", func(t *testing.T) {
		const routeName, routeNamespace = "quota-test-model", "default"

		// Precondition: the annotation is present because a QuotaPolicy currently targets this route's backend.
		require.Eventually(t, func() bool {
			return getHTTPRouteQuotaHash(t, routeNamespace, routeName) != ""
		}, 30*time.Second, 500*time.Millisecond, "expected quota-policy-hash annotation to be set before delete")

		// Delete the QuotaPolicy live (no controller/Envoy restart).
		require.NoError(t, e2elib.Kubectl(t.Context(), "delete", "quotapolicy",
			"envoy-ai-gateway-quota-ratelimit-policy", "-n", routeNamespace,
		).Run())

		// With no QuotaPolicy targeting the backend, the controller must remove the annotation, proving the
		// HTTPRoute was re-stamped (and thus Envoy Gateway re-translated) in response to the delete.
		require.Eventually(t, func() bool {
			return getHTTPRouteQuotaHash(t, routeNamespace, routeName) == ""
		}, 30*time.Second, 500*time.Millisecond,
			"quota-policy-hash annotation was not removed after deleting the QuotaPolicy")
	})
}

// getHTTPRouteQuotaHash returns the value of the "aigateway.envoyproxy.io/quota-policy-hash"
// annotation stamped by the AI Gateway controller on the generated HTTPRoute. It returns "" when the
// annotation is absent.
func getHTTPRouteQuotaHash(t *testing.T, namespace, name string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "kubectl", "get", "httproute", name,
		"-n", namespace,
		"-o", `jsonpath={.metadata.annotations.aigateway\.envoyproxy\.io/quota-policy-hash}`)
	out, err := cmd.Output()
	require.NoError(t, err, "failed to get httproute %s/%s", namespace, name)
	return strings.TrimSpace(string(out))
}

// redisExec runs a redis-cli command on the Redis pod and returns the output.
func redisExec(t *testing.T, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{
		"exec", "-n", "redis-system",
		"deploy/redis", "--",
		"redis-cli",
	}, args...)
	cmd := exec.CommandContext(t.Context(), "kubectl", cmdArgs...)
	out, err := cmd.Output()
	require.NoError(t, err, "redis-cli %v failed", args)
	return strings.TrimSpace(string(out))
}

// flushQuotaKeys deletes all quota-related keys from Redis.
func flushQuotaKeys(t *testing.T) {
	t.Helper()
	keys := redisExec(t, "KEYS", "ai-gateway-quota_*")
	if keys == "" {
		return
	}
	for _, key := range strings.Split(keys, "\n") {
		key = strings.TrimSpace(key)
		if key != "" {
			redisExec(t, "DEL", key)
		}
	}
}

// getQuotaUsage retrieves the current quota counter value from Redis for the given model.
// The key pattern is: ai-gateway-quota_backend_name_{backend}_model_name_override_{model}_{timestamp}
func getQuotaUsage(t *testing.T, modelName string) (int, bool) {
	t.Helper()
	pattern := fmt.Sprintf("ai-gateway-quota_backend_name_*_model_name_override_%s_*", modelName)
	keys := redisExec(t, "KEYS", pattern)
	if keys == "" {
		return 0, false
	}
	// Use the first matching key (there should be exactly one per model per time window).
	key := strings.Split(keys, "\n")[0]
	key = strings.TrimSpace(key)
	val := redisExec(t, "GET", key)
	if val == "" {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	require.NoError(t, err, "failed to parse quota counter value: %q", val)
	return n, true
}

// requireQuotaUsage polls Redis until the quota counter for the given model reaches
// the expected value. The stream-done rate limit entry updates Redis asynchronously
// after the response, so polling is necessary.
func requireQuotaUsage(t *testing.T, modelName string, expected int) {
	t.Helper()
	require.Eventually(t, func() bool {
		usage, ok := getQuotaUsage(t, modelName)
		return ok && usage == expected
	}, 30*time.Second, 500*time.Millisecond,
		"quota counter for model %q did not reach expected value %d", modelName, expected)
}
