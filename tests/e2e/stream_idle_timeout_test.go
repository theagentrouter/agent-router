// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// Test_Examples_StreamIdleTimeout tests the example in the examples/stream_idle_timeout directory.
//
// It verifies the two behaviors documented in the example's README:
//
//  1. Fallback: a streaming request to the "silent" rule is retried to the healthy testupstream once the per-try idle timeout fires,
//     returning a 200 within a few seconds of the 5s timeout.
//
//  2. Stream cut: a streaming request to the "semi-silent" rule is cut by the per-try idle timeout mid-stream.
//     Because response headers have already been flushed to the client, Envoy cannot synthesize a 504 and instead
//     resets the stream, which the client observes as a truncated body read.
func Test_Examples_StreamIdleTimeout(t *testing.T) {
	// The example puts the backends at priorities 0 and 1, which are set through Envoy Gateway's backendPriority
	// HTTPRouteFilter that older Envoy Gateway releases lack. There, the backendRefs end up in separate clusters
	// and do not fail over. See e2elib.EnvoyGatewaySupportsBackendPriority.
	if !e2elib.EnvoyGatewaySupportsBackendPriority() {
		t.Skipf("needs Envoy Gateway %s or later for backendRef priorities, have %s",
			"v1.10.0", e2elib.EnvoyGatewayVersion())
	}
	const backendsManifest = "../../examples/stream_idle_timeout/backends.yaml"
	const baseManifest = "../../examples/stream_idle_timeout/base.yaml"
	manifests := []string{backendsManifest, baseManifest}
	for _, manifest := range manifests {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	}
	t.Cleanup(func() {
		// Delete the base first so the gateway stops routing to the backends before they vanish.
		_ = e2elib.KubectlDeleteManifest(context.Background(), baseManifest)
		_ = e2elib.KubectlDeleteManifest(context.Background(), backendsManifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=stream-idle-timeout"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	// Wait for the stalling and healthy backend pods to be ready so requests aren't routed
	// before the upstreams are accepting connections.
	e2elib.RequireWaitForPodReady(t, "default", "app=stream-idle-timeout-silent")
	e2elib.RequireWaitForPodReady(t, "default", "app=stream-idle-timeout-semi-silent")
	e2elib.RequireWaitForPodReady(t, "default", "app=stream-idle-timeout-healthy")

	const idleTimeout = 5 * time.Second

	const streamingBody = `data: {"choices":[{"delta":{"content":"hi"},"index":0}]}

data: [DONE]

`

	t.Run("fallover to healthy backend on first-byte idle", func(t *testing.T) {
		// The "silent" rule routes to a priority-0 backend that accepts the TCP connection but
		// never sends a byte, so the per-try idle timer fires at ~5s. With the BackendTrafficPolicy
		// retrying on `reset`, Envoy falls over to the priority-1 healthy testupstream, which
		// returns the streaming body below. The client should see a 200 after roughly 5s.
		require.Eventually(t, func() bool {
			fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
			defer fwd.Kill()

			req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(
				`{"messages":[{"role":"user","content":"hi"}],"model":"stream-idle-timeout-silent-demo","stream":true}`))
			if err != nil {
				t.Logf("failed to build request: %v", err)
				return false
			}
			// Have the healthy testupstream return a valid SSE stream when the request reaches it.
			// The testupstream base64-decodes the response body header.
			req.Header.Set("x-response-type", "sse")
			req.Header.Set("x-response-body", base64.StdEncoding.EncodeToString([]byte(streamingBody)))

			start := time.Now()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Logf("request failed: %v", err)
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			elapsed := time.Since(start)
			if err != nil {
				t.Logf("failed to read response body after %s: %v", elapsed, err)
				return false
			}

			if resp.StatusCode != http.StatusOK {
				t.Logf("unexpected status %d after %s, body: %s", resp.StatusCode, elapsed, body)
				return false
			}

			// The fallover must have waited for the idle timeout on the silent backend before
			// retrying, so the response should take at least that long.
			if elapsed < idleTimeout {
				t.Logf("response arrived in %s, faster than the %s idle timeout; fallover did not occur", elapsed, idleTimeout)
				return false
			}

			// The fallover reached the healthy testupstream, which returns the SSE stream
			// we injected via the x-response-body header.
			if !strings.Contains(string(body), "hi") {
				t.Logf("response body does not contain streamed content: %s", body)
				return false
			}

			t.Logf("fallover succeeded in %s; body=%d bytes", elapsed, len(body))
			return true
		}, 30*time.Second, 2*time.Second, "streaming request did not fall over to the healthy backend")
	})

	t.Run("stream cut when idle fires mid-stream", func(t *testing.T) {
		// The "semi-silent" rule routes to a priority-0 backend that sends a partial SSE response
		// and then holds the connection open without sending another byte. Once response bytes
		// have already reached the downstream, the per-try idle timeout cuts the stream: Envoy
		// cannot synthesize a 504 (headers are already flushed), so it resets the stream.
		require.Eventually(t, func() bool {
			fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
			defer fwd.Kill()

			req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(
				`{"messages":[{"role":"user","content":"hi"}],"model":"stream-idle-timeout-semi-silent-demo","stream":true}`))
			if err != nil {
				t.Logf("failed to build request: %v", err)
				return false
			}
			// No response headers: the semi-silent backend serves its own partial raw SSE response.

			start := time.Now()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				// The reset can surface as a transport error before headers are read
				t.Logf("request failed after %s: %v", time.Since(start), err)
				return false
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			elapsed := time.Since(start)

			// The upstream sent headers before stalling, so Envoy forwards them downstream.
			// The status is whatever the semi-silent backend returned (200), not a 504, as Envoy
			// can only inject a 504 when no response headers have been sent yet.
			if resp.StatusCode != http.StatusOK {
				t.Logf("unexpected status %d after %s (expected 200 with a truncated body)", resp.StatusCode, elapsed)
				return false
			}

			if elapsed < idleTimeout {
				t.Logf("request returned in %s, faster than the %s idle timeout", elapsed, idleTimeout)
				return false
			}

			// The stream was cut mid-flight: the body read returns an error (unexpected EOF /
			// connection reset) rather than completing cleanly.
			if readErr == nil {
				t.Logf("body read completed cleanly (%d bytes) after %s; stream was not cut by the idle timeout", len(body), elapsed)
				return false
			}

			// We should have received the partial SSE payload before the stall.
			if !strings.Contains(string(body), "data: hello") {
				t.Logf("response body does not contain the partial SSE payload: %q (read err: %v)", body, readErr)
				return false
			}

			t.Logf("mid-stream idle timeout cut the stream after %s; partial body %d bytes, read err: %v",
				elapsed, len(body), readErr)
			return true
		}, 30*time.Second, 2*time.Second, "streaming request was not cut by the mid-stream idle timeout")
	})
}
