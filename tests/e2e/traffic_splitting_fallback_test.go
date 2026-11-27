// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// TestTrafficSplittingFallback tests the end-to-end functionality of traffic splitting and fallback.
func TestTrafficSplittingFallback(t *testing.T) {
	// The backendRef priorities in the manifest are set through Envoy Gateway's backendPriority
	// HTTPRouteFilter, which older Envoy Gateway releases lack. There, the backendRefs end up in separate
	// clusters and do not fail over. See e2elib.EnvoyGatewaySupportsBackendPriority.
	if !e2elib.EnvoyGatewaySupportsBackendPriority() {
		t.Skipf("needs Envoy Gateway %s or later for backendRef priorities, have %s",
			"v1.10.0", e2elib.EnvoyGatewayVersion())
	}
	const manifest = "testdata/traffic_splitting_fallback.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=traffic-splitting-fallback"

	// Wait for the gateway to be ready.
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	t.Run("traffic-distribution", func(t *testing.T) {
		// Test that traffic splitting configuration is working by making multiple requests
		// and verifying they are distributed between backends A and B.
		const requestCount = 50
		backendAResponses := 0
		backendBResponses := 0

		for range requestCount {
			fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
			defer fwd.Kill()

			req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(
				`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"model-a"}`))
			require.NoError(t, err)
			req.Header.Set(internalapi.ModelNameHeaderKeyDefault, "model-a")

			// Use a generic response that will be overridden by the testupstream server.
			req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(`{"choices":[{"message":{"content":"test"}}]}`)))

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, 200, resp.StatusCode)

			// Check response headers for backend identification.
			backendID := resp.Header.Get("testupstream-id")
			switch backendID {
			case "backend-a":
				backendAResponses++
			case "backend-b":
				backendBResponses++
			default:
				t.Logf("unexpected backend ID: %s", backendID)
			}
		}

		// Validate that both backends received traffic (with reasonable tolerance for 50/50 split).
		require.Positive(t, backendAResponses, "Backend A should receive some traffic")
		require.Positive(t, backendBResponses, "Backend B should receive some traffic")
		require.Equal(t, requestCount, backendAResponses+backendBResponses, "All requests should be handled")

		// Check that distribution is roughly 50/50 (within 20% tolerance).
		backendARatio := float64(backendAResponses) / float64(requestCount)
		backendBRatio := float64(backendBResponses) / float64(requestCount)

		// Validate that both backends receive traffic within 50/50 distribution (±20% tolerance).
		require.InDelta(t, 0.5, backendARatio, 0.2, "Backend A should receive approximately 50% of traffic")
		require.InDelta(t, 0.5, backendBRatio, 0.2, "Backend B should receive approximately 50% of traffic")

		t.Logf("Traffic distribution: Backend A=%d (%.1f%%), Backend B=%d (%.1f%%)",
			backendAResponses, backendARatio*100, backendBResponses, backendBRatio*100)
	})

	t.Run("fallback-backend-c", func(t *testing.T) {
		// Test that when backend-a and backend-b return 5xx errors,
		// traffic falls back to backend-c with model-c override.
		require.Eventually(t, func() bool {
			fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
			defer fwd.Kill()

			// Make multiple requests and count responses from each backend.
			backendCounts := make(map[string]int)
			numRequests := 20

			for range numRequests {
				req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(
					`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"model-a"}`))
				require.NoError(t, err)
				req.Header.Set(internalapi.ModelNameHeaderKeyDefault, "model-a")
				// Set all backends to return 500 errors.
				req.Header.Set(testupstreamlib.ResponseStatusKey, "500")
				req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(`{"choices":[{"message":{"content":"Fallback response from backend C"}}]}`)))

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Logf("Request failed: %v", err)
					continue
				}
				defer resp.Body.Close()

				// Count which backend responded.
				backendID := resp.Header.Get("testupstream-id")
				backendCounts[backendID]++

			}

			// Log the distribution.
			t.Logf("Backend distribution: %v", backendCounts)

			// Verify that we don't get any responses from backend-a or backend-b during fallback.
			require.Equal(t, 0, backendCounts["backend-a"], "Backend A should not receive any traffic during fallback")
			require.Equal(t, 0, backendCounts["backend-b"], "Backend B should not receive any traffic during fallback")

			// Verify that all requests resulted in responses from backend-c.
			require.Equal(t, numRequests, backendCounts["backend-c"], "All traffic should go to backend-c during fallback")

			return true
		}, 30*time.Second, 1*time.Second)
	})
}
