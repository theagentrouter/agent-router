// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// echoArgs mirrors the shape of a trivial echo tool's arguments, used by the fake canary
// backend below.
type echoArgs struct {
	Text string `json:"text,omitempty"`
}

// newCanaryTestBackend starts a real, minimal go-sdk MCP server (not the repo's
// tests/internal/testmcp package, which internal/mcpproxy cannot import due to Go's
// internal-package visibility rules) exposing one "echo" tool whose response text is
// supplied by respond, called fresh on every invocation so a test can flip behavior
// mid-run (e.g. simulate a canary going from healthy to failing).
func newCanaryTestBackend(t *testing.T, respond func() (text string, isError bool)) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "canary-test-backend", Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echoes text"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ echoArgs) (*mcp.CallToolResult, any, error) {
			text, isError := respond()
			return &mcp.CallToolResult{
				IsError: isError,
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
			}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newTestCanaryProber(t *testing.T, backendAddr string) *canaryProber {
	t.Helper()
	l := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return newCanaryProber(l, nil, func() string { return backendAddr })
}

func TestCanaryRouteState_ReferenceCounting(t *testing.T) {
	rs := newCanaryRouteState()

	t.Run("healthy by default", func(t *testing.T) {
		require.True(t, rs.isToolHealthy("backend1", "echo"))
		require.True(t, rs.isBackendDenyHealthy("backend1"))
	})

	t.Run("a single failing Drop check makes only that tool unhealthy", func(t *testing.T) {
		rs.setHealth("backend1", "echo", filterapi.CanaryActionDrop, false)
		require.False(t, rs.isToolHealthy("backend1", "echo"))
		require.True(t, rs.isBackendDenyHealthy("backend1"), "Drop must not affect backend-level Deny health")
		require.True(t, rs.isToolHealthy("backend1", "other-tool"))

		rs.setHealth("backend1", "echo", filterapi.CanaryActionDrop, true)
		require.True(t, rs.isToolHealthy("backend1", "echo"))
	})

	t.Run("a failing Deny check makes both the tool and the backend unhealthy", func(t *testing.T) {
		rs.setHealth("backend2", "sum", filterapi.CanaryActionDeny, false)
		require.False(t, rs.isToolHealthy("backend2", "sum"))
		require.False(t, rs.isBackendDenyHealthy("backend2"))

		rs.setHealth("backend2", "sum", filterapi.CanaryActionDeny, true)
		require.True(t, rs.isToolHealthy("backend2", "sum"))
		require.True(t, rs.isBackendDenyHealthy("backend2"))
	})

	t.Run("two checks covering the same tool: healthy only once both recover", func(t *testing.T) {
		rs.setHealth("backend3", "echo", filterapi.CanaryActionDrop, false)
		rs.setHealth("backend3", "echo", filterapi.CanaryActionDrop, false)
		require.False(t, rs.isToolHealthy("backend3", "echo"))

		rs.setHealth("backend3", "echo", filterapi.CanaryActionDrop, true)
		require.False(t, rs.isToolHealthy("backend3", "echo"), "one of two failing checks recovering must not mark the tool healthy")

		rs.setHealth("backend3", "echo", filterapi.CanaryActionDrop, true)
		require.True(t, rs.isToolHealthy("backend3", "echo"))
	})
}

func TestCanaryProbeOnce(t *testing.T) {
	t.Run("matching Contains passes", func(t *testing.T) {
		backend := newCanaryTestBackend(t, func() (string, bool) { return "probe-ok-12345", false })
		p := newTestCanaryProber(t, backend.URL)
		check := &filterapi.MCPCanaryCheck{Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Contains: "probe-ok"}}
		healthy, detail := p.probeOnce(t.Context(), "route", "backend1", check)
		require.True(t, healthy, "detail: %s", detail)
	})

	t.Run("matching Equals passes", func(t *testing.T) {
		backend := newCanaryTestBackend(t, func() (string, bool) { return "exact", false })
		p := newTestCanaryProber(t, backend.URL)
		check := &filterapi.MCPCanaryCheck{Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Equals: "exact"}}
		healthy, detail := p.probeOnce(t.Context(), "route", "backend1", check)
		require.True(t, healthy, "detail: %s", detail)
	})

	t.Run("mismatched content fails", func(t *testing.T) {
		backend := newCanaryTestBackend(t, func() (string, bool) { return "something else entirely", false })
		p := newTestCanaryProber(t, backend.URL)
		check := &filterapi.MCPCanaryCheck{Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Contains: "probe-ok"}}
		healthy, detail := p.probeOnce(t.Context(), "route", "backend1", check)
		require.False(t, healthy)
		require.NotEmpty(t, detail)
	})

	t.Run("tool IsError fails regardless of Expect", func(t *testing.T) {
		backend := newCanaryTestBackend(t, func() (string, bool) { return "probe-ok", true })
		p := newTestCanaryProber(t, backend.URL)
		check := &filterapi.MCPCanaryCheck{Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Contains: "probe-ok"}}
		healthy, detail := p.probeOnce(t.Context(), "route", "backend1", check)
		require.False(t, healthy)
		require.NotEmpty(t, detail)
	})

	t.Run("connect failure fails", func(t *testing.T) {
		p := newTestCanaryProber(t, "http://127.0.0.1:1") // nothing listening.
		check := &filterapi.MCPCanaryCheck{Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Contains: "x"}}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		healthy, detail := p.probeOnce(ctx, "route", "backend1", check)
		require.False(t, healthy)
		require.NotEmpty(t, detail)
	})
}

func TestCanaryProber_ReconcileStartsAndStopsProbes(t *testing.T) {
	var calls atomic.Int64
	backend := newCanaryTestBackend(t, func() (string, bool) {
		calls.Add(1)
		return "probe-ok", false
	})
	p := newTestCanaryProber(t, backend.URL)

	check := filterapi.MCPCanaryCheck{
		Tool:     "echo",
		Expect:   filterapi.MCPCanaryExpectation{Contains: "probe-ok"},
		Interval: 10 * time.Millisecond,
	}
	cfgWithCheck := &mcpProxyConfig{routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
		"route1": {backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
			"backend1": {Name: "backend1", CanaryChecks: []filterapi.MCPCanaryCheck{check}},
		}},
	}}

	p.reconcile(cfgWithCheck)
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, time.Second, 5*time.Millisecond, "expected the probe to run repeatedly")

	// Reconciling with the identical config must not restart the probe (no observable
	// effect to assert on directly here beyond "it keeps running normally", covered by the
	// continued call growth below).
	p.reconcile(cfgWithCheck)
	require.Eventually(t, func() bool { return calls.Load() >= 4 }, time.Second, 5*time.Millisecond)

	// Removing the check must stop the probe: call count should stop growing.
	emptyCfg := &mcpProxyConfig{routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
		"route1": {backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
			"backend1": {Name: "backend1"},
		}},
	}}
	p.reconcile(emptyCfg)
	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, after, calls.Load(), "probe must stop after its check is removed from config")
}

func TestCanaryProber_FailureGatesRoutingAndToolsList(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	backend := newCanaryTestBackend(t, func() (string, bool) {
		if healthy.Load() {
			return "probe-ok", false
		}
		return "unexpected drift", false
	})
	p := newTestCanaryProber(t, backend.URL)

	dropCheck := filterapi.MCPCanaryCheck{
		Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Contains: "probe-ok"},
		Interval: 10 * time.Millisecond, OnFailure: filterapi.CanaryActionDrop,
	}
	denyCheck := filterapi.MCPCanaryCheck{
		Tool: "echo", Expect: filterapi.MCPCanaryExpectation{Contains: "probe-ok"},
		Interval: 10 * time.Millisecond, OnFailure: filterapi.CanaryActionDeny,
	}
	cfg := &mcpProxyConfig{routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
		"drop-route": {backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
			"backend1": {Name: "backend1", CanaryChecks: []filterapi.MCPCanaryCheck{dropCheck}},
		}},
		"deny-route": {backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
			"backend1": {Name: "backend1", CanaryChecks: []filterapi.MCPCanaryCheck{denyCheck}},
		}},
	}}
	p.reconcile(cfg)

	require.Eventually(t, func() bool {
		rs := p.routeState("drop-route")
		return rs != nil && rs.isToolHealthy("backend1", "echo")
	}, time.Second, 5*time.Millisecond, "should start healthy")

	healthy.Store(false)
	require.Eventually(t, func() bool {
		dropRS := p.routeState("drop-route")
		denyRS := p.routeState("deny-route")
		return dropRS != nil && !dropRS.isToolHealthy("backend1", "echo") &&
			denyRS != nil && !denyRS.isBackendDenyHealthy("backend1")
	}, time.Second, 5*time.Millisecond, "should become unhealthy once the backend starts drifting")

	// Drop-configured failures must not gate backend selection (that's Deny's job).
	dropRS := p.routeState("drop-route")
	require.True(t, dropRS.isBackendDenyHealthy("backend1"), "Drop failures must not exclude the backend from new sessions")

	healthy.Store(true)
	require.Eventually(t, func() bool {
		dropRS := p.routeState("drop-route")
		denyRS := p.routeState("deny-route")
		return dropRS.isToolHealthy("backend1", "echo") && denyRS.isBackendDenyHealthy("backend1")
	}, time.Second, 5*time.Millisecond, "should recover once the backend stops drifting")
}

func TestMergeToolsList_CanaryFiltering(t *testing.T) {
	proxy := newTestMCPProxy()
	proxy.routes["test-route"].toolSelectors = nil
	proxy.canaryProber = newCanaryProber(proxy.l, nil, func() string { return "" })
	rs := proxy.canaryProber.routeStateLocked("test-route")
	rs.setHealth("backend1", "unhealthy-tool", filterapi.CanaryActionDrop, false)
	session := &session{route: "test-route"}

	responses := []broadCastResponse[mcp.ListToolsResult]{
		{backendName: "backend1", res: mcp.ListToolsResult{Tools: []*mcp.Tool{
			{Name: "unhealthy-tool"},
			{Name: "healthy-tool"},
		}}},
	}

	result := proxy.mergeToolsList(session, responses)
	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	require.NotContains(t, names, "backend1__unhealthy-tool")
	require.Contains(t, names, "backend1__healthy-tool")
}

func TestSelectAuthorizedBackends_CanaryDenyExcludesBackend(t *testing.T) {
	proxy := newTestMCPProxy()
	proxy.canaryProber = newCanaryProber(proxy.l, nil, func() string { return "" })
	rs := proxy.canaryProber.routeStateLocked("test-route")
	rs.setHealth("backend1", "some-tool", filterapi.CanaryActionDeny, false)

	route := proxy.routes["test-route"]
	selected, err := proxy.selectAuthorizedBackends("test-route", route)
	require.NoError(t, err)
	_, hasBackend1 := selected["backend1"]
	require.False(t, hasBackend1, "backend1 must be excluded while it has a failing Deny check")
	_, hasBackend2 := selected["backend2"]
	require.True(t, hasBackend2, "backend2 must be unaffected")
}
