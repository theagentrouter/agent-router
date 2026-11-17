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

// Test_QuotaPolicyDynamicMetadata verifies that QuotaPolicy consumes the
// ext_authz limit struct through the AI Gateway quota filter and falls back to
// its static limit when the configured metadata is missing or removed.
func Test_QuotaPolicyDynamicMetadata(t *testing.T) {
	if !e2elib.EnvoyGatewaySupportsLimitFromMetadata() {
		t.Skipf("needs Envoy Gateway %s, have %s", e2elib.EnvoyGatewayLatestVersion, e2elib.EnvoyGatewayVersion())
	}

	const (
		baseManifest      = "testdata/dynamic_metadata_ratelimit.yaml"
		rateLimitManifest = "testdata/ai_gateway_ratelimit.yaml"
		quotaManifest     = "testdata/quota_dynamic_metadata_policy.yaml"
		routeNamespace    = "default"
		routeName         = "envoy-ai-gateway-dynamic-ratelimit"
		policyName        = "envoy-ai-gateway-dynamic-quota"
	)
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/token_ratelimit/redis.yaml"))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), "../../examples/token_ratelimit/redis.yaml")
	})
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), rateLimitManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), rateLimitManifest)
	})
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), baseManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), baseManifest)
	})
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), quotaManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), quotaManifest)
	})

	const selector = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-dynamic-ratelimit"
	e2elib.RequireWaitForGatewayPodReady(t, selector)
	e2elib.RequireWaitForPodReady(t, "default", "app=ext-auth-server-dynamic-ratelimit")
	e2elib.RequireWaitForPodReady(t, e2elib.EnvoyGatewayNamespace, "app=envoy-ai-gateway-ratelimit")
	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, selector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	request := func(tenant, tier string) (int, string, error) {
		body := `{"messages":[{"role":"user","content":"Say this is a test"}],"model":"dynamic-ratelimit-model"}`
		usage := `{"choices":[{"message":{"content":"This is a test.","role":"assistant"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
		req, err := http.NewRequest(http.MethodPut, fwd.Address()+"/v1/chat/completions", strings.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(usage)))
		req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
		req.Header.Set("x-tenant-id", tenant)
		req.Header.Set("x-tenant-tier", tier)
		req.Header.Set("Host", "openai.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, "", err
		}
		t.Logf("quota dynamic request tenant=%q tier=%q status=%d body=%s",
			tenant, tier, resp.StatusCode, strings.TrimSpace(string(responseBody)))
		return resp.StatusCode, strings.TrimSpace(string(responseBody)), nil
	}

	deadline := time.Now().Add(3 * time.Minute)
	for {
		status, responseBody, err := request("quota-readiness", "unknown")
		if err == nil && status == http.StatusTooManyRequests {
			break
		}
		if time.Now().After(deadline) {
			require.NoError(t, err)
			t.Fatalf("quota rate limiting did not become effective; last status %d", status)
		}
		t.Logf("waiting for quota enforcement: status=%d err=%v body=%s", status, err, responseBody)
		time.Sleep(3 * time.Second)
	}

	waitForQuotaHashChange := func(previous string) {
		require.Eventually(t, func() bool {
			return getHTTPRouteQuotaHash(t, routeNamespace, routeName) != "" &&
				getHTTPRouteQuotaHash(t, routeNamespace, routeName) != previous
		}, 60*time.Second, 500*time.Millisecond,
			"QuotaPolicy hash did not change after updating %s", policyName)
	}

	currentHash := ""
	require.Eventually(t, func() bool {
		currentHash = getHTTPRouteQuotaHash(t, routeNamespace, routeName)
		return currentHash != ""
	}, 60*time.Second, 500*time.Millisecond, "expected initial QuotaPolicy hash")

	id := fmt.Sprintf("quota-%d", time.Now().UnixNano())
	assertExhaustion := func(tenantSuffix, tier string, allowedHits int) {
		tenantID := id + "-" + tenantSuffix
		for i := 0; i < allowedHits; i++ {
			status, _, err := request(tenantID, tier)
			require.NoError(t, err, "request %d failed for tenant %s", i+1, tenantID)
			require.Equal(t, http.StatusOK, status, "expected OK on request %d for tenant %s", i+1, tenantID)
		}
		status, _, err := request(tenantID, tier)
		require.NoError(t, err, "exhaustion request failed for tenant %s", tenantID)
		require.Equal(t, http.StatusTooManyRequests, status, "expected 429 on exhaustion for tenant %s", tenantID)
	}

	assertExhaustion("premium-initial", "premium", 4)

	// Change only the metadata key. The ext_authz server does not emit this key,
	// so the static limit must be used even though fromMetadata is still present.
	require.NoError(t, e2elib.Kubectl(t.Context(), "patch", "quotapolicy", policyName,
		"-n", routeNamespace, "--type=merge", "-p",
		`{"spec":{"perModelQuotas":[{"modelName":"dynamic-ratelimit-model","quota":{"mode":"Shared","bucketRules":[{"clientSelectors":[{"headers":[{"name":"x-tenant-id","type":"Distinct"}]}],"quota":{"limit":1,"duration":"1h","fromMetadata":{"namespace":"envoy.filters.http.ext_authz","key":"missing_limit"}}}]}}]}}`).Run())
	waitForQuotaHashChange(currentHash)
	currentHash = getHTTPRouteQuotaHash(t, routeNamespace, routeName)

	status, _, err := request(id+"-missing-metadata", "premium")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	status, _, err = request(id+"-missing-metadata", "premium")
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, status)

	// Remove fromMetadata entirely and verify that the same static fallback
	// remains active.
	require.NoError(t, e2elib.Kubectl(t.Context(), "patch", "quotapolicy", policyName,
		"-n", routeNamespace, "--type=json", "-p",
		`[{"op":"remove","path":"/spec/perModelQuotas/0/quota/bucketRules/0/quota/fromMetadata"}]`).Run())
	waitForQuotaHashChange(currentHash)
	currentHash = getHTTPRouteQuotaHash(t, routeNamespace, routeName)

	status, _, err = request(id+"-no-metadata", "premium")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	status, _, err = request(id+"-no-metadata", "premium")
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, status)

	// Restore fromMetadata and verify that a newly emitted premium limit is
	// active again after the route is retranslated.
	require.NoError(t, e2elib.Kubectl(t.Context(), "patch", "quotapolicy", policyName,
		"-n", routeNamespace, "--type=merge", "-p",
		`{"spec":{"perModelQuotas":[{"modelName":"dynamic-ratelimit-model","quota":{"mode":"Shared","bucketRules":[{"clientSelectors":[{"headers":[{"name":"x-tenant-id","type":"Distinct"}]}],"quota":{"limit":1,"duration":"1h","fromMetadata":{"namespace":"envoy.filters.http.ext_authz","key":"total_limit"}}}]}}]}}`).Run())
	waitForQuotaHashChange(currentHash)

	assertExhaustion("premium-restored", "premium", 4)
}
