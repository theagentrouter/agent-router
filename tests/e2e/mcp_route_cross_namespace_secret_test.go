// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestMCPRouteCrossNamespaceSecret verifies that a backend's apiKey.secretRef in another namespace
// follows its ReferenceGrant end to end: denied without the grant, serving with the referenced Secret's
// value once the grant exists, back to the denied state (generated objects gone, no session) when the
// grant is deleted, and serving again after the grant returns. A backend with an inline key on the same
// route keeps working throughout.
func TestMCPRouteCrossNamespaceSecret(t *testing.T) {
	const (
		manifest      = "testdata/mcp_route_cross_namespace_secret.yaml"
		grantManifest = "testdata/mcp_route_cross_namespace_secret_grant.yaml"
		routeNS       = "mcp-cross-ns-route"
		credsNS       = "mcp-cross-ns-creds"
		singlePath    = "/mcp/cross-ns-single"
		mixedPath     = "/mcp/cross-ns-mixed"
	)
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.Kubectl(context.Background(), "delete", "-f", grantManifest, "--ignore-not-found").Run()
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	// The namespace label keeps the port forwarder away from the other MCP tests' Gateways.
	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway-cross-ns-secret,gateway.envoyproxy.io/owning-gateway-namespace=" + routeNS
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)
	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()
	client := mcp.NewClient(&mcp.Implementation{Name: "demo-http-client", Version: "0.1.0"}, nil)

	routes := []string{"mcp-cross-ns-single", "mcp-cross-ns-mixed"}
	grantedTools := testMCPServerAllToolNames("mcp-backend-granted__")
	localTools := testMCPServerAllToolNames("mcp-backend-local__")
	mixedTools := slices.Concat(grantedTools, localTools)
	// The objects the controller generates for the granted backend of the single route.
	generated := [][2]string{
		{"httproute", "ai-eg-mcp-br-mcp-cross-ns-single-mcp-backend-granted"},
		{"httproutefilter", "ai-eg-mcp-brf-mcp-cross-ns-single-mcp-backend-granted"},
		{"secret", "ai-eg-mcp-cred-mcp-cross-ns-single-mcp-backend-granted"},
	}
	requireDenied := func(t *testing.T) {
		t.Helper()
		for _, route := range routes {
			requireMCPRouteCondition(t, routeNS, route, aigv1b1.ConditionTypeNotAccepted, "is not permitted")
		}
		for _, obj := range generated {
			requireKubeObjectAbsent(t, routeNS, obj[0], obj[1])
		}
		requireMCPConnectFails(t, client, fwd.Address()+singlePath)
		requireMCPToolsEventually(t, client, fwd.Address()+mixedPath, localTools)
	}
	requireGranted := func(t *testing.T) {
		t.Helper()
		for _, route := range routes {
			requireMCPRouteCondition(t, routeNS, route, aigv1b1.ConditionTypeAccepted, "")
		}
		for _, obj := range generated {
			require.NoError(t, e2elib.Kubectl(t.Context(), "get", obj[0], obj[1], "-n", routeNS).Run())
		}
		requireMCPToolsEventually(t, client, fwd.Address()+mixedPath, mixedTools)
		testMCPRouteTools(t.Context(), t, client, fwd.Address(), singlePath, grantedTools, nil, true, true)
		testMCPRouteTools(t.Context(), t, client, fwd.Address(), mixedPath, mixedTools, nil, true, true)
	}

	t.Run("denied without a grant", func(t *testing.T) {
		requireDenied(t)
	})
	t.Run("granted", func(t *testing.T) {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), grantManifest))
		requireGranted(t)
	})
	t.Run("revoked", func(t *testing.T) {
		require.NoError(t, e2elib.Kubectl(t.Context(), "delete", "-f", grantManifest).Run())
		// The controller's finalizer releases the grant once the routes were woken up.
		requireKubeObjectAbsent(t, credsNS, "referencegrant", "allow-mcp-cross-ns-route")
		requireDenied(t)
	})
	t.Run("granted again", func(t *testing.T) {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), grantManifest))
		requireGranted(t)
	})
}

// requireMCPRouteCondition waits until the MCPRoute carries a condition of the given type whose message
// contains messageSubstring.
func requireMCPRouteCondition(t *testing.T, namespace, name, condType, messageSubstring string) {
	t.Helper()
	require.Eventually(t, func() bool {
		cmd := e2elib.Kubectl(t.Context(), "get", "mcproute", name, "-n", namespace, "-o", "json")
		cmd.Stdout = nil
		out, err := cmd.Output()
		if err != nil {
			t.Logf("failed to get mcproute %s/%s: %v", namespace, name, err)
			return false
		}
		var route aigv1b1.MCPRoute
		if err := json.Unmarshal(out, &route); err != nil {
			t.Logf("failed to unmarshal mcproute %s/%s: %v", namespace, name, err)
			return false
		}
		for _, cond := range route.Status.Conditions {
			if cond.Type == condType && strings.Contains(cond.Message, messageSubstring) {
				return true
			}
		}
		t.Logf("conditions of mcproute %s/%s: %+v", namespace, name, route.Status.Conditions)
		return false
	}, 60*time.Second, 500*time.Millisecond, "mcproute %s/%s should be %s", namespace, name, condType)
}

// requireKubeObjectAbsent waits until the object does not exist.
func requireKubeObjectAbsent(t *testing.T, namespace, kind, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		cmd := e2elib.Kubectl(t.Context(), "get", kind, name, "-n", namespace, "--ignore-not-found", "-o", "name")
		cmd.Stdout = nil
		out, err := cmd.Output()
		if err != nil {
			t.Logf("failed to get %s %s/%s: %v", kind, namespace, name, err)
			return false
		}
		return strings.TrimSpace(string(out)) == ""
	}, 60*time.Second, 500*time.Millisecond, "%s %s/%s should be gone", kind, namespace, name)
}

// requireMCPConnectFails waits until no MCP session can be created at endpoint because the gateway has
// no backend to serve it. A session that still opens while the data plane converges is closed and the
// check repeats.
func requireMCPConnectFails(t *testing.T, client *mcp.Client, endpoint string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
		if err != nil {
			t.Logf("connect to %s failed: %v", endpoint, err)
			return strings.Contains(err.Error(), "Internal Server Error")
		}
		_ = sess.Close()
		t.Logf("a session was still created at %s", endpoint)
		return false
	}, 60*time.Second, 1*time.Second, "no session must be created at %s", endpoint)
}

// requireMCPToolsEventually waits until a new session at endpoint lists exactly expectedTools.
func requireMCPToolsEventually(t *testing.T, client *mcp.Client, endpoint string, expectedTools []string) {
	t.Helper()
	expected := slices.Clone(expectedTools)
	slices.Sort(expected)
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
		if err != nil {
			t.Logf("failed to connect to %s: %v", endpoint, err)
			return false
		}
		defer sess.Close()
		tools, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			t.Logf("failed to list tools at %s: %v", endpoint, err)
			return false
		}
		names := toolNames(tools.Tools)
		slices.Sort(names)
		if !slices.Equal(names, expected) {
			t.Logf("tools at %s: %v", endpoint, names)
			return false
		}
		return true
	}, 60*time.Second, 1*time.Second, "tools at %s should be %v", endpoint, expectedTools)
}
