// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

const (
	// defaultCanaryInterval mirrors the controller's default (see
	// internal/controller/gateway.go's defaultCanaryCheckInterval) for checks constructed
	// directly (e.g. in tests) without going through that translation.
	defaultCanaryInterval = time.Minute
	// canaryProbeTimeout caps a single probe call, independent of the configured interval,
	// so a hanging backend can't leak an ever-growing number of in-flight probes.
	canaryProbeTimeout = 10 * time.Second
)

// canaryToolKey identifies one backend's tool within a route, for health tracking.
type canaryToolKey struct {
	backend filterapi.MCPBackendName
	tool    string
}

// canaryProbeKey identifies one configured canary check across reloads: a backend's
// CanaryChecks is a plain list, so a check's position in that list (not its content, which
// can change) is its stable identity for reconciliation purposes.
type canaryProbeKey struct {
	route   filterapi.MCPRouteName
	backend filterapi.MCPBackendName
	index   int
}

// canaryRouteState tracks canary health for one route. It is long-lived across LoadConfig
// reloads (unlike mcpProxyConfigRoute, which is rebuilt from scratch every reload), since a
// check's failure state must not reset just because unrelated config changed elsewhere.
//
// Health is reference-counted rather than a flat set, because more than one check can cover
// the same tool (or the same backend, for the Deny count): a tool/backend only becomes
// healthy again once every check currently failing against it recovers.
type canaryRouteState struct {
	mu                 sync.Mutex
	unhealthyToolCount map[canaryToolKey]int
	unhealthyDenyCount map[filterapi.MCPBackendName]int
}

func newCanaryRouteState() *canaryRouteState {
	return &canaryRouteState{
		unhealthyToolCount: map[canaryToolKey]int{},
		unhealthyDenyCount: map[filterapi.MCPBackendName]int{},
	}
}

// setHealth records a transition for one check's contribution to shared health state.
// Callers must only call this on an actual state change (healthy -> unhealthy or back), not
// on every probe tick, since it's reference-counted.
func (rs *canaryRouteState) setHealth(backend filterapi.MCPBackendName, tool string, onFailure filterapi.CanaryAction, healthy bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	key := canaryToolKey{backend: backend, tool: tool}
	if healthy {
		decrementOrDelete(rs.unhealthyToolCount, key)
	} else {
		rs.unhealthyToolCount[key]++
	}
	if onFailure == filterapi.CanaryActionDeny {
		if healthy {
			decrementOrDelete(rs.unhealthyDenyCount, backend)
		} else {
			rs.unhealthyDenyCount[backend]++
		}
	}
}

func decrementOrDelete[K comparable](m map[K]int, key K) {
	if m[key] <= 1 {
		delete(m, key)
	} else {
		m[key]--
	}
}

// isToolHealthy reports whether every currently-running check covering backend/tool is
// passing. A tool no check covers is always healthy.
func (rs *canaryRouteState) isToolHealthy(backend filterapi.MCPBackendName, tool string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.unhealthyToolCount[canaryToolKey{backend: backend, tool: tool}] == 0
}

// isBackendDenyHealthy reports whether every currently-running Deny-configured check
// against backend is passing. A backend no Deny check covers is always healthy.
func (rs *canaryRouteState) isBackendDenyHealthy(backend filterapi.MCPBackendName) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.unhealthyDenyCount[backend] == 0
}

// canaryHeaderInjector adds the backend/route routing headers the backend-only Envoy
// listener uses to select a target backend. A canary probe has no downstream client request
// to derive these from (unlike addMCPHeaders' normal callers), so it sets them directly.
type canaryHeaderInjector struct {
	base        http.RoundTripper
	routeName   filterapi.MCPRouteName
	backendName filterapi.MCPBackendName
}

func (c *canaryHeaderInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(internalapi.MCPBackendHeader, c.backendName)
	req.Header.Set(internalapi.MCPRouteHeader, c.routeName)
	base := c.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// runningCanaryProbe is the handle reconcile keeps for a check's background goroutine.
type runningCanaryProbe struct {
	check  *filterapi.MCPCanaryCheck
	cancel context.CancelFunc
}

// canaryProber runs the background goroutines for every configured canary check and owns
// the per-route health state they report into. Unlike mcpProxyConfig, a canaryProber
// instance is created once and lives for the lifetime of the process; LoadConfig calls
// reconcile on every reload instead of replacing it.
type canaryProber struct {
	l                     *slog.Logger
	backendListenerAddrFn func() string
	httpTransport         http.RoundTripper

	mu     sync.Mutex
	probes map[canaryProbeKey]*runningCanaryProbe
	routes map[filterapi.MCPRouteName]*canaryRouteState
}

// newCanaryProber creates a canaryProber. backendListenerAddrFn is a function rather than a
// fixed string so the prober always dispatches to the current backend listener address even
// if it were ever to change across a reload (it does not today, but this keeps the prober
// from needing to know that).
func newCanaryProber(l *slog.Logger, httpTransport http.RoundTripper, backendListenerAddrFn func() string) *canaryProber {
	return &canaryProber{
		l:                     l,
		backendListenerAddrFn: backendListenerAddrFn,
		httpTransport:         httpTransport,
		probes:                map[canaryProbeKey]*runningCanaryProbe{},
		routes:                map[filterapi.MCPRouteName]*canaryRouteState{},
	}
}

// routeState returns the health state for routeName, or nil if that route has no canary
// checks configured (the common case, kept alloc-free) or the receiver itself is nil (a
// ProxyConfig built directly rather than via NewMCPProxy, as most tests do, since canary
// checks are entirely opt-in and such tests never populate the prober).
func (p *canaryProber) routeState(routeName filterapi.MCPRouteName) *canaryRouteState {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.routes[routeName]
}

func (p *canaryProber) routeStateLocked(routeName filterapi.MCPRouteName) *canaryRouteState {
	rs, ok := p.routes[routeName]
	if !ok {
		rs = newCanaryRouteState()
		p.routes[routeName] = rs
	}
	return rs
}

// reconcile starts, restarts, and stops canary probe goroutines so the running set exactly
// matches what newConfig declares. It is called from LoadConfig on every reload.
func (p *canaryProber) reconcile(newConfig *mcpProxyConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	desired := map[canaryProbeKey]*filterapi.MCPCanaryCheck{}
	for routeName, route := range newConfig.routes {
		for backendName, backend := range route.backends {
			for i := range backend.CanaryChecks {
				// Index into the slice rather than copying the range value: MCPCanaryCheck
				// is large enough (a map plus several strings) that gocritic's hugeParam
				// check flags copying it by value, and there's no reason to here.
				desired[canaryProbeKey{route: routeName, backend: backendName, index: i}] = &backend.CanaryChecks[i]
			}
		}
	}

	for key, running := range p.probes {
		if newCheck, ok := desired[key]; ok && canaryCheckEqual(running.check, newCheck) {
			continue // unchanged, leave it running.
		}
		running.cancel()
		delete(p.probes, key)
	}

	for key, check := range desired {
		if _, ok := p.probes[key]; ok {
			continue // already running with this exact definition.
		}
		rs := p.routeStateLocked(key.route)
		ctx, cancel := context.WithCancel(context.Background())
		p.probes[key] = &runningCanaryProbe{check: check, cancel: cancel}
		go p.runLoop(ctx, key, check, rs)
	}

	// Drop health state for routes no longer configured at all, so a route that is deleted
	// and later recreated under the same name doesn't inherit stale failure counts.
	for routeName := range p.routes {
		if _, ok := newConfig.routes[routeName]; !ok {
			delete(p.routes, routeName)
		}
	}
}

// canaryCheckEqual reports whether two MCPCanaryCheck values are equal for the purpose of
// deciding whether a running probe needs to be restarted. This runs only on config reload
// (not per-request), so reflect.DeepEqual's cost is not a concern.
func canaryCheckEqual(a, b *filterapi.MCPCanaryCheck) bool {
	return reflect.DeepEqual(a, b)
}

// runLoop runs check repeatedly (once immediately, then on check.Interval) until ctx is
// cancelled, reporting only state transitions into rs so canaryRouteState's reference counts
// stay correct regardless of how many checks cover the same tool/backend.
func (p *canaryProber) runLoop(ctx context.Context, key canaryProbeKey, check *filterapi.MCPCanaryCheck, rs *canaryRouteState) {
	interval := check.Interval
	if interval <= 0 {
		interval = defaultCanaryInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	healthy := true // assume healthy until proven otherwise, avoiding a false-positive gap before the first probe completes.
	defer func() {
		if !healthy {
			// Deregister this check's contribution so a cancelled/replaced check doesn't
			// leave a permanent phantom failure behind.
			rs.setHealth(key.backend, check.Tool, check.OnFailure, true)
		}
	}()

	probe := func() {
		probeCtx, cancel := context.WithTimeout(ctx, canaryProbeTimeout)
		ok, detail := p.probeOnce(probeCtx, key.route, key.backend, check)
		cancel()
		if ok == healthy {
			return
		}
		healthy = ok
		rs.setHealth(key.backend, check.Tool, check.OnFailure, healthy)
		if healthy {
			p.l.Info("canary check recovered", slog.String("route", key.route), slog.String("backend", key.backend), slog.String("tool", check.Tool))
		} else {
			p.l.Warn("canary check failing", slog.String("route", key.route), slog.String("backend", key.backend), slog.String("tool", check.Tool),
				slog.String("on_failure", string(check.OnFailure)), slog.String("detail", detail))
		}
	}

	probe()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

// probeOnce performs a single, real tools/call against backend's tool, dispatched directly
// via the backend-only listener (bypassing per-caller authorization and tool selectors,
// since this checks the backend itself rather than any caller's access to it -- the same
// listener initializeSession/invokeJSONRPCRequest use for real request dispatch). It
// deliberately uses the real mcp.Client rather than hand-rolled JSON-RPC/SSE parsing, so a
// canary probe is byte-for-byte the same protocol interaction a real downstream client would
// have.
func (p *canaryProber) probeOnce(ctx context.Context, routeName filterapi.MCPRouteName, backendName filterapi.MCPBackendName, check *filterapi.MCPCanaryCheck) (healthy bool, detail string) {
	httpClient := &http.Client{
		Transport: &canaryHeaderInjector{base: p.httpTransport, routeName: routeName, backendName: backendName},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ai-gateway-canary-probe", Version: "0.1.0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   p.backendListenerAddrFn(),
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		return false, fmt.Sprintf("connect failed: %v", err)
	}
	defer func() { _ = sess.Close() }()

	result, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: check.Tool, Arguments: check.Arguments})
	if err != nil {
		return false, fmt.Sprintf("tools/call failed: %v", err)
	}
	if result.IsError {
		return false, "tool result has IsError=true"
	}
	var text string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if check.Expect.Equals != "" && text != check.Expect.Equals {
		return false, fmt.Sprintf("expected result to equal %q, got %q", check.Expect.Equals, text)
	}
	if check.Expect.Contains != "" && !strings.Contains(text, check.Expect.Contains) {
		return false, fmt.Sprintf("expected result to contain %q, got %q", check.Expect.Contains, text)
	}
	return true, ""
}
