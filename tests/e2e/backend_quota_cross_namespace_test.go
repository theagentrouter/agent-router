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

// Test_Examples_BackendQuotaCrossNamespace covers the minimum cross-namespace
// QuotaPolicy cases:
//  1. Without a ReferenceGrant the policy is NotAccepted and quota is not enforced.
//  2. With a ReferenceGrant the policy is Accepted and per-model quota is enforced.
func Test_Examples_BackendQuotaCrossNamespace(t *testing.T) {
	const (
		redisManifest        = "../../examples/token_ratelimit/redis.yaml"
		rateLimitManifest    = "testdata/ai_gateway_ratelimit.yaml"
		baseManifest         = "testdata/backend_quota_cross_namespace.yaml"
		policyNamespace      = "quota-policy-ns"
		backendNamespace     = "quota-backend-ns"
		quotaPolicyName      = "cross-ns-quota-policy"
		quotaPolicyGrantName = "allow-quotapolicy-to-aiservicebackend"
		routeName            = "cross-ns-quota-route"
		modelName            = "cross-ns-quota-model"
		gatewaySelector      = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-quota-cross-ns"
	)

	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), redisManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), redisManifest)
	})
	e2elib.RequireWaitForPodReady(t, "redis-system", "app=redis")

	// Shared AI Gateway rate limit service (also used by same-namespace QuotaPolicy e2e).
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), rateLimitManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), rateLimitManifest)
	})
	// Restart so RLS reconnects after Redis is ready (same pattern as token ratelimit e2e).
	require.NoError(t, e2elib.KubectlRestartDeployment(t.Context(), e2elib.EnvoyGatewayNamespace, "envoy-ai-gateway-ratelimit"))
	require.NoError(t, e2elib.Kubectl(t.Context(), "rollout", "status",
		"deployment/envoy-ai-gateway-ratelimit", "-n", e2elib.EnvoyGatewayNamespace, "--timeout=2m").Run())

	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), baseManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), baseManifest)
	})

	e2elib.RequireWaitForGatewayPodReady(t, gatewaySelector)
	e2elib.RequireWaitForPodReady(t, backendNamespace, "app=cross-ns-quota-testupstream")

	doRequest := func(totalTokens int) (int, string) {
		t.Helper()
		fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, gatewaySelector, e2elib.EnvoyGatewayDefaultServicePort)
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

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	waitForQuotaPolicy := func(expectedType, expectedReason string) {
		t.Helper()
		require.Eventually(t, func() bool {
			condType, reason := getQuotaPolicyCondition(t, policyNamespace, quotaPolicyName)
			if condType != expectedType || reason != expectedReason {
				t.Logf("QuotaPolicy status type=%q reason=%q", condType, reason)
				return false
			}
			return true
		}, 60*time.Second, 500*time.Millisecond,
			"expected QuotaPolicy %s/%s", expectedType, expectedReason)
	}

	waitForQuotaHash := func(expectedPresent bool) {
		t.Helper()
		require.Eventually(t, func() bool {
			hashPresent := getHTTPRouteQuotaHash(t, policyNamespace, routeName) != ""
			return hashPresent == expectedPresent
		}, 30*time.Second, 500*time.Millisecond,
			"expected quota-policy-hash present=%t", expectedPresent)
	}

	assertQuotaNotEnforced := func() {
		t.Helper()
		flushQuotaKeys(t)
		status, body := doRequest(20)
		require.Equal(t, http.StatusOK, status, "body: %s", body)
		status, body = doRequest(20)
		require.Equal(t, http.StatusOK, status, "body: %s", body)
	}

	assertQuotaEnforced := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			flushQuotaKeys(t)
			status, body := doRequest(20)
			if status != http.StatusOK {
				t.Logf("waiting for quota charging path: status=%d body=%s", status, body)
				return false
			}
			usage, ok := peekQuotaUsage(t, modelName)
			if !ok || usage != 20 {
				t.Logf("waiting for redis quota usage=20, got ok=%v usage=%d", ok, usage)
				return false
			}
			return true
		}, 90*time.Second, 2*time.Second, "quota was not charged")

		status, body := doRequest(5)
		require.Equal(t, http.StatusTooManyRequests, status, "body: %s", body)
		requireQuotaUsage(t, modelName, 20)
	}

	deleteQuotaPolicyGrant := func() {
		t.Helper()
		require.NoError(t, e2elib.Kubectl(t.Context(), "delete", "referencegrant",
			quotaPolicyGrantName, "-n", backendNamespace, "--ignore-not-found").Run())
	}

	t.Run("without QuotaPolicy ReferenceGrant", func(_ *testing.T) {
		deleteQuotaPolicyGrant()
		waitForQuotaPolicy("NotAccepted", "RefNotPermitted")
		waitForQuotaHash(false)
		assertQuotaNotEnforced()
	})

	t.Run("grant restores enforcement", func(_ *testing.T) {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), baseManifest))
		waitForQuotaPolicy("Accepted", "ReconciliationSucceeded")
		waitForQuotaHash(true)
		assertQuotaEnforced()
	})

	t.Run("deleting policy removes enforcement", func(_ *testing.T) {
		require.NoError(t, e2elib.Kubectl(t.Context(), "delete", "quotapolicy",
			quotaPolicyName, "-n", policyNamespace).Run())
		require.Eventually(t, func() bool {
			return !quotaPolicyExists(t, policyNamespace, quotaPolicyName)
		}, 30*time.Second, 500*time.Millisecond, "QuotaPolicy was not deleted")
		waitForQuotaHash(false)
		assertQuotaNotEnforced()
	})

	t.Run("recreating policy restores enforcement", func(_ *testing.T) {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), baseManifest))
		waitForQuotaPolicy("Accepted", "ReconciliationSucceeded")
		waitForQuotaHash(true)
		assertQuotaEnforced()
	})
}

// getQuotaPolicyCondition returns the type and reason of the first status condition
// on the named QuotaPolicy.
func getQuotaPolicyCondition(t *testing.T, namespace, name string) (condType, reason string) {
	t.Helper()
	typeCmd := exec.CommandContext(t.Context(), "kubectl", "get", "quotapolicy", name,
		"-n", namespace,
		"-o", "jsonpath={.status.conditions[0].type}")
	typeOut, err := typeCmd.Output()
	require.NoError(t, err, "failed to get QuotaPolicy %s/%s condition type", namespace, name)

	reasonCmd := exec.CommandContext(t.Context(), "kubectl", "get", "quotapolicy", name,
		"-n", namespace,
		"-o", "jsonpath={.status.conditions[0].reason}")
	reasonOut, err := reasonCmd.Output()
	require.NoError(t, err, "failed to get QuotaPolicy %s/%s condition reason", namespace, name)

	return strings.TrimSpace(string(typeOut)), strings.TrimSpace(string(reasonOut))
}

func quotaPolicyExists(t *testing.T, namespace, name string) bool {
	t.Helper()
	return exec.CommandContext(t.Context(), "kubectl", "get", "quotapolicy", name,
		"-n", namespace).Run() == nil
}

// peekQuotaUsage is a non-fatal Redis quota counter read for use inside Eventually polls.
func peekQuotaUsage(t *testing.T, modelName string) (int, bool) {
	t.Helper()
	pattern := fmt.Sprintf("ai-gateway-quota_backend_name_*_model_name_override_%s_*", modelName)
	cmd := exec.CommandContext(t.Context(), "kubectl", "exec", "-n", "redis-system",
		"deploy/redis", "--", "redis-cli", "KEYS", pattern)
	keysOut, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	keys := strings.TrimSpace(string(keysOut))
	if keys == "" {
		return 0, false
	}
	key := strings.TrimSpace(strings.Split(keys, "\n")[0])
	getCmd := exec.CommandContext(t.Context(), "kubectl", "exec", "-n", "redis-system",
		"deploy/redis", "--", "redis-cli", "GET", key)
	valOut, err := getCmd.Output()
	if err != nil {
		return 0, false
	}
	val := strings.TrimSpace(string(valOut))
	if val == "" {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	return n, true
}
