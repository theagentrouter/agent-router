// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type (
	// ProxyConfig holds the main MCP proxy configuration.
	// This implements [filterapi.ConfigReceiver] to gets the up-to-date configuration.
	ProxyConfig struct {
		*mcpProxyConfig
		toolChangeSignaler         changeSignaler // signals tool changes to active sessions.
		l                          *slog.Logger
		sessionCrypto              SessionCrypto
		tracer                     tracingapi.MCPTracer
		client                     http.Client
		logRequestHeaderAttributes map[string]string
		maxRequestBodySize         int64 // maximum allowed POST body size in bytes
	}

	mcpProxyConfig struct {
		backendListenerAddr string
		routes              map[filterapi.MCPRouteName]*mcpProxyConfigRoute // route name -> backends of that route.
	}

	mcpProxyConfigRoute struct {
		backends        map[filterapi.MCPBackendName]filterapi.MCPBackend
		toolSelectors   map[filterapi.MCPBackendName]*toolSelector
		promptSelectors map[filterapi.MCPBackendName]*toolSelector
		authorization   *compiledAuthorization
		// backendSelector reuses the same compiledAuthorization machinery as authorization
		// above, but is evaluated once per candidate backend in newSession() instead of
		// per JSON-RPC method call.
		backendSelector *compiledAuthorization
		forwardHeaders  []string
		// routePrefixMode is the route-level fallback when a backend has no per-backend PrefixMode set.
		routePrefixMode filterapi.PrefixMode

		// neverModeToolIndex maps a bare tool name to the backend that owns it, for every backend
		// whose effective PrefixMode is Never. It is computed once here, at config load, from each
		// such backend's declared toolSelector.include (admission-time validation requires this to
		// be set and unique across backends for Never-mode backends — see validatePerBackendPrefixMode
		// in internal/controller/mcp_route.go). Because it lives on the route config rather than on
		// a per-client session, tool-call routing can resolve bare names directly from here without
		// depending on session state that doesn't survive across the separate HTTP requests a real
		// MCP session is made of.
		neverModeToolIndex map[string]string

		// neverModePromptIndex is the prompt equivalent of neverModeToolIndex, populated only for
		// backends that opt in by declaring promptSelector.include. Never-mode backends that don't
		// declare a promptSelector keep their prompts prefixed (see mergePromptsList) since there is
		// no admission-validated, unique set of names to expose bare for them.
		neverModePromptIndex map[string]string
	}

	// toolSelector filters tools using include and exclude patterns with exact matches or regular expressions.
	// Exclude rules take precedence over include rules (deny-wins).
	toolSelector struct {
		include        map[string]struct{}
		includeRegexps []*regexp.Regexp
		exclude        map[string]struct{}
		excludeRegexps []*regexp.Regexp
	}

	// changeSignaler is an interface for signaling configuration changes to multiple
	// watchers.
	changeSignaler interface {
		// Watch returns a channel that is closed then the configuration changes.
		// The channel should be obtained by calling this method every time when used in a loop,
		// because it will be closed and recreated after each signal is sent.
		Watch() <-chan struct{}
		// Signal all watchers that the configuration has changed.
		Signal()
	}

	multiWatcherSignaler struct {
		mu     sync.Mutex
		notify chan struct{}
	}
)

func (m *mcpProxyConfig) sameTools(other *mcpProxyConfig) bool {
	if m == nil || other == nil {
		return m == other
	}
	return maps.EqualFunc(m.routes, other.routes, func(a, b *mcpProxyConfigRoute) bool {
		return a.sameTools(b)
	})
}

// effectivePrefixMode returns the effective PrefixMode for the given backend.
// Per-backend PrefixMode takes precedence; falls back to route-level PrefixMode.
// An empty string (zero value) for either field is treated as Always (the default).
func (m *mcpProxyConfigRoute) effectivePrefixMode(backendName filterapi.MCPBackendName) filterapi.PrefixMode {
	if backend, ok := m.backends[backendName]; ok && backend.PrefixMode != "" {
		return backend.PrefixMode
	}
	return m.routePrefixMode
}

func (m *mcpProxyConfigRoute) sameTools(other *mcpProxyConfigRoute) bool {
	if m == nil || other == nil {
		return m == other
	}
	if !equalKeys(m.backends, other.backends) {
		return false
	}
	if !m.authorization.same(other.authorization) {
		return false
	}
	// neverModeToolIndex/neverModePromptIndex catch PrefixMode and include-list changes that
	// don't touch toolSelectors/promptSelectors directly, so reload correctly signals
	// notifications/tools(prompts)/list_changed for those too.
	if !maps.Equal(m.neverModeToolIndex, other.neverModeToolIndex) {
		return false
	}
	if !maps.Equal(m.neverModePromptIndex, other.neverModePromptIndex) {
		return false
	}
	if !maps.EqualFunc(m.promptSelectors, other.promptSelectors, func(a, b *toolSelector) bool {
		return a.sameTools(b)
	}) {
		return false
	}
	return maps.EqualFunc(m.toolSelectors, other.toolSelectors, func(a, b *toolSelector) bool {
		return a.sameTools(b)
	})
}

var sortRegexpAsString = func(a, b *regexp.Regexp) int { return strings.Compare(a.String(), b.String()) }

func equalKeys[K comparable, V any](m1, m2 map[K]V) bool {
	return maps.EqualFunc(m1, m2, func(_, _ V) bool { return true })
}

// buildSelector compiles a toolSelector from raw include/exclude patterns shared by
// MCPToolSelector and MCPPromptSelector. includeKind/excludeKind are used only to make compile
// errors legible (e.g. "include", "prompt exclude").
func buildSelector(include, includeRegex, exclude, excludeRegex []string, includeKind, excludeKind string, backendName filterapi.MCPBackendName, routeName filterapi.MCPRouteName) (*toolSelector, error) {
	ts := &toolSelector{
		include: make(map[string]struct{}, len(include)),
		exclude: make(map[string]struct{}, len(exclude)),
	}
	for _, name := range include {
		ts.include[name] = struct{}{}
	}
	includeRegexps, err := compileRegexps(includeRegex, includeKind, backendName, routeName)
	if err != nil {
		return nil, err
	}
	ts.includeRegexps = includeRegexps
	for _, name := range exclude {
		ts.exclude[name] = struct{}{}
	}
	excludeRegexps, err := compileRegexps(excludeRegex, excludeKind, backendName, routeName)
	if err != nil {
		return nil, err
	}
	ts.excludeRegexps = excludeRegexps
	return ts, nil
}

func compileRegexps(exprs []string, kind string, backendName filterapi.MCPBackendName, routeName filterapi.MCPRouteName) ([]*regexp.Regexp, error) {
	var regexps []*regexp.Regexp
	for _, expr := range exprs {
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("failed to compile %s regex %q for backend %q in route %q: %w", kind, expr, backendName, routeName, err)
		}
		regexps = append(regexps, re)
	}
	return regexps, nil
}

func (t *toolSelector) sameTools(other *toolSelector) bool {
	if t == nil || other == nil {
		return t == other
	}
	if !equalKeys(t.include, other.include) {
		return false
	}
	if !equalKeys(t.exclude, other.exclude) {
		return false
	}
	tIncludeRegexps := slices.Clone(t.includeRegexps)
	otherIncludeRegexps := slices.Clone(other.includeRegexps)
	slices.SortFunc(tIncludeRegexps, sortRegexpAsString)
	slices.SortFunc(otherIncludeRegexps, sortRegexpAsString)
	if !slices.EqualFunc(tIncludeRegexps, otherIncludeRegexps,
		func(a, b *regexp.Regexp) bool {
			return a.String() == b.String()
		}) {
		return false
	}
	tExcludeRegexps := slices.Clone(t.excludeRegexps)
	otherExcludeRegexps := slices.Clone(other.excludeRegexps)
	slices.SortFunc(tExcludeRegexps, sortRegexpAsString)
	slices.SortFunc(otherExcludeRegexps, sortRegexpAsString)
	return slices.EqualFunc(tExcludeRegexps, otherExcludeRegexps,
		func(a, b *regexp.Regexp) bool {
			return a.String() == b.String()
		})
}

func (t *toolSelector) allows(tool string) bool {
	// Check exclude filters first (deny-wins).
	if len(t.exclude) > 0 {
		if _, ok := t.exclude[tool]; ok {
			return false
		}
	}
	if len(t.excludeRegexps) > 0 {
		for _, re := range t.excludeRegexps {
			if re.MatchString(tool) {
				return false
			}
		}
	}

	// Check include filters - if no filter, allow all; if filter exists, allow only matches.
	if len(t.include) > 0 {
		_, ok := t.include[tool]
		return ok
	}
	if len(t.includeRegexps) > 0 {
		for _, re := range t.includeRegexps {
			if re.MatchString(tool) {
				return true
			}
		}
		return false
	}
	// No include filters, allow all (that passed exclude checks).
	return true
}

// LoadConfig implements [extproc.ConfigReceiver.LoadConfig] which will be called
// when the configuration is updated on the file system.
func (p *ProxyConfig) LoadConfig(_ context.Context, config *filterapi.Config) error {
	newConfig := &mcpProxyConfig{}
	mcpConfig := config.MCPConfig
	if config.MCPConfig == nil {
		return nil
	}

	// Talk to the backend MCP listener on the local Envoy instance.
	newConfig.backendListenerAddr = mcpConfig.BackendListenerAddr

	// Build a map of routes to backends.
	// Each route has its own set of backends. For a given downstream request,
	// the MCP proxy initializes sessions only with the backends tied to that route.
	newConfig.routes = make(map[filterapi.MCPRouteName]*mcpProxyConfigRoute, len(mcpConfig.Routes))

	for _, route := range mcpConfig.Routes {
		compiledAuth, err := compileAuthorization(route.Authorization)
		if err != nil {
			return fmt.Errorf("failed to compile authorization rules for route %s: %w", route.Name, err)
		}
		compiledBackendSel, err := compileAuthorization(route.BackendSelector)
		if err != nil {
			return fmt.Errorf("failed to compile backend selector rules for route %s: %w", route.Name, err)
		}

		r := &mcpProxyConfigRoute{
			backends:        make(map[filterapi.MCPBackendName]filterapi.MCPBackend, len(route.Backends)),
			toolSelectors:   make(map[filterapi.MCPBackendName]*toolSelector, len(route.Backends)),
			promptSelectors: make(map[filterapi.MCPBackendName]*toolSelector, len(route.Backends)),
			authorization:   compiledAuth,
			backendSelector: compiledBackendSel,
			forwardHeaders:  route.ForwardHeaders,
			routePrefixMode: route.PrefixMode,
		}
		for _, backend := range route.Backends {
			r.backends[backend.Name] = backend
			if s := backend.ToolSelector; s != nil {
				ts, err := buildSelector(s.Include, s.IncludeRegex, s.Exclude, s.ExcludeRegex, "include", "exclude", backend.Name, route.Name)
				if err != nil {
					return err
				}
				r.toolSelectors[backend.Name] = ts
			}
			if s := backend.PromptSelector; s != nil {
				ps, err := buildSelector(s.Include, s.IncludeRegex, s.Exclude, s.ExcludeRegex, "prompt include", "prompt exclude", backend.Name, route.Name)
				if err != nil {
					return err
				}
				r.promptSelectors[backend.Name] = ps
			}
		}

		// Build the static Never-mode indexes now that backends/routePrefixMode are populated,
		// since effectivePrefixMode needs both. Collisions are already rejected at admission time
		// (validatePerBackendPrefixMode), so a collision here indicates the running config diverged
		// from what was validated; log and keep the first claim rather than failing config load.
		r.neverModeToolIndex = make(map[string]string)
		r.neverModePromptIndex = make(map[string]string)
		for _, backend := range route.Backends {
			if r.effectivePrefixMode(backend.Name) != filterapi.PrefixModeNever {
				continue
			}
			if ts := r.toolSelectors[backend.Name]; ts != nil {
				for name := range ts.include {
					if !ts.allows(name) {
						continue
					}
					if existing, collision := r.neverModeToolIndex[name]; collision {
						p.l.Warn("BUG: prefixMode=Never tool name collision survived admission validation, keeping first claim",
							slog.String("tool", name), slog.String("backend1", existing), slog.String("backend2", backend.Name))
						continue
					}
					r.neverModeToolIndex[name] = backend.Name
				}
			}
			if ps := r.promptSelectors[backend.Name]; ps != nil {
				for name := range ps.include {
					if !ps.allows(name) {
						continue
					}
					if existing, collision := r.neverModePromptIndex[name]; collision {
						p.l.Warn("BUG: prefixMode=Never prompt name collision survived admission validation, keeping first claim",
							slog.String("prompt", name), slog.String("backend1", existing), slog.String("backend2", backend.Name))
						continue
					}
					r.neverModePromptIndex[name] = backend.Name
				}
			}
		}

		newConfig.routes[route.Name] = r
	}

	toolsChanged := !p.sameTools(newConfig)
	p.mcpProxyConfig = newConfig // This is racy, but we don't care.
	if toolsChanged {
		p.toolChangeSignaler.Signal()
	}

	return nil
}

// newMultiWatcherSignaler creates a new multi-watcher signaler.
func newMultiWatcherSignaler() *multiWatcherSignaler {
	return &multiWatcherSignaler{
		notify: make(chan struct{}),
	}
}

// Watch returns a channel that is closed then the configuration changes.
// The channel should be obtained by calling this method every time when used in a loop,
// because it will be closed and recreated after each signal is sent.
func (m *multiWatcherSignaler) Watch() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.notify
}

// Signal notifies all watchers of a configuration change.
func (m *multiWatcherSignaler) Signal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	close(m.notify)                // Wake everyone
	m.notify = make(chan struct{}) // Create a new channel for future updates
}
