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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// Test_DynamicMetadataRateLimit checks that a per-tenant LLM token budget carried in dynamic
// metadata gates traffic, instead of the static BackendTrafficPolicy budget. One shared policy
// serves every tenant, with the tenant's own number arriving per request, so no per-tenant routes or
// policies are needed.
//
// Two halves have to meet, and only one of them is ours. ext_authz returns the budget and the policy
// reads it straight from the ext_authz metadata namespace, with no AI Gateway involvement. The
// tokens each call consumes come from the LLM response, which only AI Gateway can supply
// (globalLLMRequestCosts). This test exists to keep that split honest: if someone later routes the
// budget through AI Gateway, this stops being the thing it claims to test.
//
// Request-time cost is 0, so a call is admitted whenever the budget is not already spent and charged
// afterwards. A budget of N against a cost of C therefore allows N/C + 1 calls. The first cases use
// only the total budget with a 1/1/2 reply:
//
//	premium   6 tokens/HOUR -> 4 calls    basic  2 tokens/HOUR -> 2 calls
//	unknown   no metadata, static 4 -> 3  suspended 0 -> 1 call, then denied
//
// The last case sets all three budgets at once. The differing per-tier budgets are what prove the
// number came from metadata; equal budgets would only prove the per-tenant bucketing that EG's
// Distinct selector already gives us.
func Test_DynamicMetadataRateLimit(t *testing.T) {
	// limit.fromMetadata (envoyproxy/gateway#9216) only exists on Envoy Gateway main. Older releases
	// prune it, so nothing gets rate limited.
	if !e2elib.EnvoyGatewaySupportsLimitFromMetadata() {
		t.Skipf("needs Envoy Gateway %s, have %s", e2elib.EnvoyGatewayLatestVersion, e2elib.EnvoyGatewayVersion())
	}

	// Apply Redis manifest (shared with the other rate limit tests). The Envoy
	// Gateway global rate limit service (envoy-ratelimit) is backed by Redis.
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/token_ratelimit/redis.yaml"))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), "../../examples/token_ratelimit/redis.yaml")
	})

	const manifest = "testdata/dynamic_metadata_ratelimit.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-dynamic-ratelimit"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	// Wait for the redis pod to be ready so that the rate limit can be performed
	// correctly. Until the redis pod is ready, envoy-ratelimit will be in
	// CrashLoopBackOff, so restart it to get a clean state up faster.
	require.NoError(t, e2elib.KubectlRestartDeployment(t.Context(), e2elib.EnvoyGatewayNamespace, "envoy-ratelimit"))
	e2elib.RequireWaitForPodReady(t, e2elib.EnvoyGatewayNamespace, "app.kubernetes.io/component=ratelimit")
	e2elib.RequireWaitForPodReady(t, "redis-system", "app=redis")

	// The auth server supplies the budget, and requests fail closed if it isn't up.
	e2elib.RequireWaitForPodReady(t, "default", "app=ext-auth-server-dynamic-ratelimit")

	// One port forwarder for the whole test, because recreating it per request is slow and flaky.
	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	const modelName = "dynamic-ratelimit-model"
	// doRequest returns the transport error rather than failing the test, so the polling below can
	// ride out the connection resets that happen while Envoy is picking up a config change. usage is
	// what the testupstream reports the call consumed, which is what each budget is charged.
	doRequest := func(t *testing.T, tenantID, tier string, usage tokenUsage) (int, string, error) {
		t.Helper()
		requestBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`, modelName)
		fakeResponseBody := fmt.Sprintf(
			`{"choices":[{"message":{"content":"This is a test.","role":"assistant"}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
			usage.input, usage.output, usage.total)

		req, err := http.NewRequest(http.MethodPut, fwd.Address()+"/v1/chat/completions", strings.NewReader(requestBody))
		require.NoError(t, err)
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(fakeResponseBody)))
		req.Header.Set(testupstreamlib.ExpectedPathHeaderKey, base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
		// x-tenant-id keys the rate-limit budget and x-tenant-tier tells the auth server which budgets
		// to emit. Keeping them separate lets each case use a fresh, unused budget.
		req.Header.Set("x-tenant-id", tenantID)
		req.Header.Set("x-tenant-tier", tier)
		req.Header.Set("Host", "openai.com")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, "", err
		}
		return resp.StatusCode, string(body), nil
	}
	makeRequest := func(t *testing.T, tenantID, tier string, usage tokenUsage, expStatus int) {
		t.Helper()
		status, body, err := doRequest(t, tenantID, tier, usage)
		require.NoError(t, err)
		require.Equal(t, expStatus, status, "unexpected status code, body: %s", body)
	}

	// Fresh x-tenant-id per case, because rate-limit budgets persist in Redis across the run.
	baseID := int(time.Now().UnixNano())
	tenantID := func(offset int) string { return strconv.Itoa(baseID + offset) }

	// countAllowed reports how many requests a brand new tenant on tier gets through before it is
	// rate limited. Every call uses an unused x-tenant-id so no budget carries over, and it stops
	// after maxRequests so a limit that never trips can't spin forever.
	probeTenant := 100 // Well clear of the offsets the cases below use.
	countAllowed := func(t *testing.T, tier string, usage tokenUsage, maxRequests int) (int, error) {
		t.Helper()
		probeTenant++
		id := tenantID(probeTenant)
		for n := range maxRequests {
			status, _, err := doRequest(t, id, tier, usage)
			if err != nil {
				return 0, err
			}
			if status == http.StatusTooManyRequests {
				return n, nil
			}
		}
		return maxRequests, nil
	}

	// Readiness gate. The rate limit config reaches Envoy through xDS and envoy-ratelimit through its
	// own config load, so the first tenant to be throttled correctly tells us the rest of the cases
	// can assert exact counts without racing the gateway coming up.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		got, err := countAllowed(t, "basic", evenUsage, 4)
		if err == nil && got == 2 {
			break
		}
		if time.Now().After(deadline) {
			require.NoError(t, err, "rate limiting never became effective")
			t.Fatalf("a %q tenant was allowed %d requests, want 2", "basic", got)
		}
		time.Sleep(3 * time.Second)
	}

	t.Run("premium tier gets its 6 token budget from metadata", func(t *testing.T) {
		id := tenantID(0)
		for range 4 {
			makeRequest(t, id, "premium", evenUsage, http.StatusOK)
		}
		makeRequest(t, id, "premium", evenUsage, http.StatusTooManyRequests)
	})

	// A smaller budget than premium, on the same policy and the same static fallback, so the
	// difference can only have come from the metadata. That is the whole point.
	t.Run("basic tier gets its smaller 2 token budget from metadata", func(t *testing.T) {
		id := tenantID(1)
		makeRequest(t, id, "basic", evenUsage, http.StatusOK)
		makeRequest(t, id, "basic", evenUsage, http.StatusOK)
		makeRequest(t, id, "basic", evenUsage, http.StatusTooManyRequests)
	})

	// The auth server emits nothing for an unrecognized tier, so this falls back to the static
	// budget rather than being denied or going unlimited.
	t.Run("unknown tier falls back to the static budget", func(t *testing.T) {
		id := tenantID(2)
		for range 3 {
			makeRequest(t, id, "unknown", evenUsage, http.StatusOK)
		}
		makeRequest(t, id, "unknown", evenUsage, http.StatusTooManyRequests)
	})

	// A 0 budget still admits one call, because request-time cost is 0 and the tokens are charged
	// from the response. It denies from the second call on. That differs from request-count limiting,
	// where 0 denies immediately.
	t.Run("a 0 budget denies from the second call", func(t *testing.T) {
		id := tenantID(3)
		makeRequest(t, id, "suspended", evenUsage, http.StatusOK)
		makeRequest(t, id, "suspended", evenUsage, http.StatusTooManyRequests)
	})

	// Separate input, output and total budgets in flight at once, which is the shape a real tenant
	// plan takes. All three are emitted on every request and each drives its own policy rule, so a
	// tenant stops at whichever budget it exhausts first.
	//
	// The reply is deliberately lopsided (3 in, 1 out, 4 total) so the three budgets produce different
	// call counts. With an even reply the input and output budgets would be indistinguishable, and a
	// mix-up between the two keys would pass unnoticed.
	t.Run("separate input, output and total budgets", func(t *testing.T) {
		for i, tc := range []struct {
			// tier names which budget is the small one. The auth server leaves the other two
			// generous, so the named one is the only thing that can stop the tenant.
			tier string
			// want is the small budget divided by what this reply charges it, plus the one call
			// that is admitted before anything is charged.
			want int
		}{
			{tier: "tri-input", want: 3},  // 6 tokens / 3 per call
			{tier: "tri-output", want: 4}, // 3 tokens / 1 per call
			{tier: "tri-total", want: 4},  // 12 tokens / 4 per call
		} {
			t.Run(tc.tier, func(t *testing.T) {
				id := tenantID(10 + i)
				for range tc.want {
					makeRequest(t, id, tc.tier, lopsidedUsage, http.StatusOK)
				}
				makeRequest(t, id, tc.tier, lopsidedUsage, http.StatusTooManyRequests)
			})
		}
	})
}

// tokenUsage is what the testupstream reports a call consumed. Each budget is charged its own field.
type tokenUsage struct{ input, output, total int }

var (
	// evenUsage charges the total budget 2 per call and is what the single-budget cases use.
	evenUsage = tokenUsage{input: 1, output: 1, total: 2}
	// lopsidedUsage charges each budget a different amount, so the triplet case can tell them apart.
	lopsidedUsage = tokenUsage{input: 3, output: 1, total: 4}
)
