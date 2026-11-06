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

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// Test_Examples_BackendQuotaFailover exercises quota-aware failover across multiple
// backends in a single AIGatewayRoute. It depends on two features shipped by the
// newer RLS and Envoy builds:
//
//   - The rate limit service advertises, per request, the set of (backend, model)
//     pairs that still have quota ("passedBackends" dynamic metadata), and only
//     returns OVER_LIMIT when EVERY quota group for the route is exhausted.
//   - Envoy's envoy.load_balancing_policies.host_allowlist load balancing policy
//     restricts host selection to that advertised set, so traffic skips backends
//     whose quota is exhausted and falls over to a backend that still has quota.
//
// The fixture route has three backends at ascending priorities:
//
//	backend-failover-a  priority 0  quota 10 tokens/h (model quota-failover-a)
//	backend-failover-b  priority 1  quota 10 tokens/h (model quota-failover-b)
//	backend-failover-c  priority 2  NO quota          (model quota-failover-c)
func Test_Examples_BackendQuotaFailover(t *testing.T) {
	// Quota failover depends on the envoy.load_balancing_policies.host_allowlist LB policy
	// (envoyproxy/envoy#47805). This test's EnvoyProxy pins an Envoy image that includes it,
	// but that bleeding-edge data plane is only safe to pair with the latest Envoy Gateway
	// control plane. Skip on older EG versions (e.g. v1.8.5, v1.9.2) to avoid control-plane /
	// data-plane version skew.
	if e2elib.EnvoyGatewayVersion() != e2elib.EnvoyGatewayLatestVersion {
		t.Skipf("quota failover needs the latest Envoy Gateway (%s), have %s",
			e2elib.EnvoyGatewayLatestVersion, e2elib.EnvoyGatewayVersion())
	}

	const (
		redisManifest     = "../../examples/token_ratelimit/redis.yaml"
		ratelimitManifest = "testdata/ai_gateway_ratelimit.yaml"
		manifest          = "testdata/backend_quota_failover.yaml"
		routeModel        = "quota-failover-model"
		modelA            = "quota-failover-a"
		modelB            = "quota-failover-b"
		egSelector        = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-quota-failover"
	)

	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), redisManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), redisManifest)
	})
	e2elib.RequireWaitForPodReady(t, "redis-system", "app=redis")

	// Shared AI Gateway rate limit service (also used by the other QuotaPolicy e2e tests).
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), ratelimitManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), ratelimitManifest)
	})
	// Restart so RLS reconnects after Redis is ready (same pattern as the other quota e2e tests).
	require.NoError(t, e2elib.KubectlRestartDeployment(t.Context(), e2elib.EnvoyGatewayNamespace, "envoy-ai-gateway-ratelimit"))
	require.NoError(t, e2elib.Kubectl(t.Context(), "rollout", "status",
		"deployment/envoy-ai-gateway-ratelimit", "-n", e2elib.EnvoyGatewayNamespace, "--timeout=2m").Run())

	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	e2elib.RequireWaitForGatewayPodReady(t, egSelector)
	e2elib.RequireWaitForPodReady(t, "default", "app=backend-failover-a")
	e2elib.RequireWaitForPodReady(t, "default", "app=backend-failover-b")
	e2elib.RequireWaitForPodReady(t, "default", "app=backend-failover-c")

	flushQuotaKeys(t)

	// doRequest sends one chat completion with the given total_tokens in the fake
	// response and returns the HTTP status and the responding backend id (from the
	// testupstream-id response header). It returns transport errors so callers can
	// ride out connection resets while Envoy picks up a config change.
	doRequest := func(totalTokens int) (int, string, error) {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
		defer fwd.Kill()

		requestBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`, routeModel)
		fakeResponseBody := fmt.Sprintf(
			`{"choices":[{"message":{"content":"This is a test.","role":"assistant"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":%d}}`,
			totalTokens,
		)
		req, err := http.NewRequest(http.MethodPut, fwd.Address()+"/v1/chat/completions", strings.NewReader(requestBody))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(fakeResponseBody)))
		req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
		req.Header.Set("Host", "openai.com")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, "", err
		}
		backendID := resp.Header.Get("testupstream-id")
		t.Logf("failover request total_tokens=%d status=%d backend=%q body=%s",
			totalTokens, resp.StatusCode, backendID, strings.TrimSpace(string(body)))
		return resp.StatusCode, backendID, nil
	}

	// requireBackendEventually polls until a 200 response is served by wantBackend.
	// This rides out the asynchronous Redis quota update (charged on stream-done) and
	// the xDS/host_allowlist propagation of the exhausted-backend state.
	requireBackendEventually := func(wantBackend string, totalTokens int) {
		t.Helper()
		require.Eventually(t, func() bool {
			status, backend, err := doRequest(totalTokens)
			if err != nil {
				t.Logf("request error while waiting for backend %q: %v", wantBackend, err)
				return false
			}
			return status == http.StatusOK && backend == wantBackend
		}, 90*time.Second, 2*time.Second, "expected a 200 served by %q", wantBackend)
	}

	// 1. The first request must land on the priority-0 backend (backend-a), which
	//    still has quota.
	t.Run("primary backend serves while it has quota", func(t *testing.T) {
		status, backend, err := doRequest(20)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "backend-failover-a", backend,
			"the priority-0 backend must serve while it still has quota")
		// backend-a charged 20 tokens (> its 10 limit), so it is now exhausted.
		requireQuotaUsage(t, modelA, 20)
	})

	// 2. With backend-a exhausted, RLS still returns OK (backend-b's quota group is
	//    not over) and advertises passedBackends=[backend-b]; host_allowlist excludes
	//    backend-a, so traffic fails over to the priority-1 backend (backend-b).
	t.Run("fails over to the next quota backend when the primary is exhausted", func(t *testing.T) {
		requireBackendEventually("backend-failover-b", 20)
		// backend-b is now charged 20 tokens (> its 10 limit), so it is exhausted too.
		requireQuotaUsage(t, modelB, 20)
	})

	// 3. With BOTH quota backends exhausted, every quota group is over, so RLS returns
	//    OVER_LIMIT and the rate-limit filter returns 429 BEFORE host selection. The
	//    non-quota backend-c is never reached: there is no failover to a non-quota
	//    backend once all quota-backed targets are exhausted.
	t.Run("returns 429 when all quota backends are exhausted (no failover to non-quota backend)", func(t *testing.T) {
		require.Eventually(t, func() bool {
			status, backend, err := doRequest(20)
			if err != nil {
				t.Logf("request error while waiting for 429: %v", err)
				return false
			}
			if backend == "backend-failover-c" {
				t.Fatalf("traffic reached the non-quota backend-c, but quota exhaustion should short-circuit with 429")
			}
			return status == http.StatusTooManyRequests
		}, 90*time.Second, 2*time.Second,
			"expected 429 once both quota backends are exhausted")

		// The quota counters must not have advanced past their exhausted values: a
		// rejected request is never completed, so it charges nothing.
		requireQuotaUsage(t, modelA, 20)
		requireQuotaUsage(t, modelB, 20)
	})
}
