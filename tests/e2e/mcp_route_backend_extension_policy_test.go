// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// TestMCPRouteBackendExtensionPolicy verifies that MCPBackendExtensionPolicy extensions run on the requests the MCP
// proxy sends to each backend, with the policy naming a backend taking precedence over the MCPRoute-wide one.
//
// Each backend rejects requests that lack the x-ext value its effective policy's dynamic module filter sets (the test
// module in tests/internal/testdynamicmodule), so a successful tool call through a backend proves its extension ran
// on that hop.
func TestMCPRouteBackendExtensionPolicy(t *testing.T) {
	const manifest = "testdata/mcp_route_backend_extension_policy.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway-ext"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	t.Run("extensions run on each backend", func(t *testing.T) {
		client := mcp.NewClient(&mcp.Implementation{Name: "demo-http-client", Version: "0.1.0"}, nil)
		expectedTools := append(testMCPServerAllToolNames("mcp-ext-a__"), testMCPServerAllToolNames("mcp-ext-b__")...)
		testMCPRouteTools(t.Context(), t, client, fwd.Address(), "/mcp", expectedTools, http.DefaultClient, true, true)

		// testMCPRouteTools calls the tools of one backend only: call the echo tool of each.
		sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: fmt.Sprintf("%s/mcp", fwd.Address())}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = sess.Close() })
		for _, backend := range []string{"mcp-ext-a", "mcp-ext-b"} {
			res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      backend + "__" + testmcp.ToolEcho.Tool.Name,
				Arguments: testmcp.ToolEchoArgs{Text: "hello " + backend},
			})
			require.NoError(t, err, backend)
			require.False(t, res.IsError, backend)
		}
	})

	t.Run("status", func(t *testing.T) {
		for policy, expected := range map[string]string{
			"mcp-ext-wide":    "MCPRoute mcp-route-ext: applied to backends: mcp-ext-b; overridden for: mcp-ext-a (by mcp-ext-section)",
			"mcp-ext-section": "MCPRoute mcp-route-ext: applied to backends: mcp-ext-a",
		} {
			require.Eventually(t, func() bool {
				out, err := exec.CommandContext(t.Context(), "kubectl", "get", "mcpbackendextensionpolicy", // #nosec G204
					policy, "-n", "default", "-o", "json").Output()
				if err != nil {
					t.Logf("failed to get policy %s: %v", policy, err)
					return false
				}
				var p aigv1a1.MCPBackendExtensionPolicy
				if err := json.Unmarshal(out, &p); err != nil {
					t.Logf("failed to unmarshal policy %s: %v", policy, err)
					return false
				}
				if len(p.Status.Conditions) != 1 {
					return false
				}
				cond := p.Status.Conditions[0]
				t.Logf("policy %s: %s: %s", policy, cond.Type, cond.Message)
				return cond.Type == aigv1a1.ConditionTypeAccepted && cond.Message == expected
			}, 30*time.Second, 200*time.Millisecond, "policy %s", policy)
		}
	})
}
