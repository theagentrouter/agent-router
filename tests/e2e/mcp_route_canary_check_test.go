// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestMCPRouteCanaryCheck verifies synthetic canary health checks (a follow-up to
// #2736/toolIntegrity): a backend's tool is actually invoked on an interval and compared
// against an expected result, to catch a backend's real behavior changing even when its
// declared MCP interface (name/description/schema) stays identical -- something no
// interface-level digest check can ever see.
//
// The check here is deliberately configured to fail from the start (a fixed "this text will
// never appear" expectation against the real, deterministic "echo" tool) rather than trying
// to make a live backend's behavior actually drift mid-test: the interesting behavior to
// prove is what the gateway does once a check is failing, not how to provoke a failure.
func TestMCPRouteCanaryCheck(t *testing.T) {
	const manifest = "testdata/mcp_route_canary_check.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway-canary-check"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	t.Run("onFailure=Drop: only the checked tool is dropped once the check starts failing", func(t *testing.T) {
		client := mcp.NewClient(&mcp.Implementation{Name: "canary-e2e-drop-client", Version: "0.1.0"}, nil)
		require.Eventually(t, func() bool {
			sess := requireConnectMCP(t.Context(), t, client, fwd.Address()+"/mcp-canary-drop", nil)
			defer func() { _ = sess.Close() }()

			tools, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
			if err != nil {
				return false
			}
			names := make([]string, len(tools.Tools))
			for i, tool := range tools.Tools {
				names[i] = tool.Name
			}
			echoDropped := !slices.Contains(names, "mcp-backend-canary-check__echo")
			countdownPresent := slices.Contains(names, "mcp-backend-canary-check__countdown")
			return echoDropped && countdownPresent
		}, 20*time.Second, 200*time.Millisecond, "expected the failing check's tool to be dropped while an unrelated tool stays available")
	})

	t.Run("onFailure=Deny: the whole backend is excluded once the check starts failing", func(t *testing.T) {
		client := mcp.NewClient(&mcp.Implementation{Name: "canary-e2e-deny-client", Version: "0.1.0"}, nil)
		require.Eventually(t, func() bool {
			sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: fwd.Address() + "/mcp-canary-deny"}, nil)
			if err == nil {
				_ = sess.Close()
			}
			// This route has exactly one backend, so once its Deny check is failing,
			// selectAuthorizedBackends has nothing left to select and session
			// establishment itself is refused (403) -- a stronger guarantee than merely
			// omitting the backend's tools from an otherwise-successful session.
			return err != nil
		}, 20*time.Second, 200*time.Millisecond, "expected session establishment to start failing once the backend's only Deny check fails")
	})
}

