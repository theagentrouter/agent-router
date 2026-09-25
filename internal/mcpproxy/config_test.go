// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"io"
	"log/slog"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func Test_toolSelector_Allows(t *testing.T) {
	reBa := regexp.MustCompile("^ba.*")
	tests := []struct {
		name     string
		selector toolSelector
		tools    []string
		expected []bool
	}{
		{
			name:     "no rules allows all",
			selector: toolSelector{},
			tools:    []string{"foo", "bar"},
			expected: []bool{true, true},
		},
		{
			name:     "include specific tool",
			selector: toolSelector{include: map[string]struct{}{"foo": {}}},
			tools:    []string{"foo", "bar"},
			expected: []bool{true, false},
		},
		{
			name:     "include regexp",
			selector: toolSelector{includeRegexps: []*regexp.Regexp{reBa}},
			tools:    []string{"bar", "foo"},
			expected: []bool{true, false},
		},
		{
			name:     "exclude specific tool",
			selector: toolSelector{exclude: map[string]struct{}{"foo": {}}},
			tools:    []string{"foo", "bar"},
			expected: []bool{false, true},
		},
		{
			name:     "exclude regexp",
			selector: toolSelector{excludeRegexps: []*regexp.Regexp{reBa}},
			tools:    []string{"bar", "foo"},
			expected: []bool{false, true},
		},
		{
			name: "include + exclude where exclude wins",
			selector: toolSelector{
				include: map[string]struct{}{"foo": {}, "bar": {}},
				exclude: map[string]struct{}{"bar": {}},
			},
			tools:    []string{"foo", "bar", "baz"},
			expected: []bool{true, false, false},
		},
		{
			name: "include + excludeRegex where exclude wins",
			selector: toolSelector{
				include:        map[string]struct{}{"foo": {}, "bar": {}},
				excludeRegexps: []*regexp.Regexp{reBa},
			},
			tools:    []string{"foo", "bar", "baz"},
			expected: []bool{true, false, false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, tool := range tt.tools {
				actual := tt.selector.allows(tool)
				require.Equalf(t, tt.expected[i], actual, "tool: %s", tool)
			}
		})
	}
}

func TestLoadConfig_NilMCPConfig(t *testing.T) {
	proxy, _, err := NewMCPProxy(slog.Default(), stubMetrics{}, noopTracer, NewPBKDF2AesGcmSessionCrypto("test", 100), nil)
	require.NoError(t, err)

	config := &filterapi.Config{MCPConfig: nil}

	err = proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)
}

func TestLoadConfig_BasicConfiguration(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{Name: "backend1"},
						{
							Name: "backend2",
							ToolSelector: &filterapi.MCPToolSelector{
								Include:      []string{"tool1", "tool2"},
								IncludeRegex: []string{"^test.*"},
							},
						},
					},
				},
				{
					Name: "route2",
					Backends: []filterapi.MCPBackend{
						{Name: "backend3"},
						{Name: "backend4"},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)
	require.Equal(t, "http://localhost:8080", proxy.backendListenerAddr)
	require.Len(t, proxy.routes, 2)
	require.Contains(t, proxy.routes, filterapi.MCPRouteName("route1"))
	require.Contains(t, proxy.routes, filterapi.MCPRouteName("route2"))
	require.Len(t, proxy.routes["route1"].backends, 2)
	require.Len(t, proxy.routes["route2"].backends, 2)
	require.Contains(t, proxy.routes["route1"].backends, filterapi.MCPBackendName("backend1"))
	require.Contains(t, proxy.routes["route1"].backends, filterapi.MCPBackendName("backend2"))
	require.Contains(t, proxy.routes["route2"].backends, filterapi.MCPBackendName("backend3"))
	require.Contains(t, proxy.routes["route2"].backends, filterapi.MCPBackendName("backend4"))
	selector := proxy.routes["route1"].toolSelectors["backend2"]
	require.NotNil(t, selector)
	require.Contains(t, selector.include, "tool1")
	require.Contains(t, selector.include, "tool2")
	require.Len(t, selector.includeRegexps, 1)
	require.True(t, selector.includeRegexps[0].MatchString("test123"))
	require.False(t, selector.includeRegexps[0].MatchString("other"))
}

func TestLoadConfig_ToolsChangedNotification(t *testing.T) {
	toolChangeSignaler := newMultiWatcherSignaler()
	watcher := toolChangeSignaler.Watch()

	// Initialize proxy with initial configuration directly
	proxy := &ProxyConfig{
		mcpProxyConfig: &mcpProxyConfig{
			backendListenerAddr: "http://localhost:8080",
			routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
				"route1": {
					backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
						"backend1": {Name: "backend1"},
					},
					toolSelectors: map[filterapi.MCPBackendName]*toolSelector{},
				},
			},
		},
		toolChangeSignaler: toolChangeSignaler,
	}

	// Update with a different backend (tools changed)
	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{Name: "backend1"},
						{Name: "backend2"}, // Added backend
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	// Should receive tools changed notification
	select {
	case <-watcher:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected tools changed notification but didn't receive one")
	}
}

func TestLoadConfig_NoToolsChangedNotification(t *testing.T) {
	toolChangeSignaler := newMultiWatcherSignaler()
	watcher := toolChangeSignaler.Watch()

	// Initialize proxy with initial configuration directly
	proxy := &ProxyConfig{
		mcpProxyConfig: &mcpProxyConfig{
			backendListenerAddr: "http://localhost:8080",
			routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
				"route1": {
					backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
						"backend1": {Name: "backend1"},
					},
					toolSelectors: map[filterapi.MCPBackendName]*toolSelector{},
				},
			},
		},
		toolChangeSignaler: toolChangeSignaler,
	}

	// Update with same backends but different BackendListenerAddr (tools NOT changed)
	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:9090", // Different address
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{Name: "backend1"}, // Same backend
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	// Should NOT receive tools changed notification
	select {
	case <-watcher:
		t.Fatal("unexpected tools changed notification")
	case <-time.After(100 * time.Millisecond):
		// Expected - no notification
	}
}

func TestLoadConfig_ExcludeConfiguration(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								Include:      []string{"tool1", "tool2", "tool3"},
								Exclude:      []string{"tool3"},
								ExcludeRegex: []string{"^secret.*"},
							},
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)
	selector := proxy.routes["route1"].toolSelectors["backend1"]
	require.NotNil(t, selector)
	require.Contains(t, selector.include, "tool1")
	require.Contains(t, selector.include, "tool2")
	require.Contains(t, selector.include, "tool3")
	require.Contains(t, selector.exclude, "tool3")
	require.Len(t, selector.excludeRegexps, 1)
	require.True(t, selector.allows("tool1"))
	require.True(t, selector.allows("tool2"))
	require.False(t, selector.allows("tool3"))       // excluded by exact match
	require.False(t, selector.allows("secret_tool")) // excluded by regex
}

func TestLoadConfig_InvalidRegex(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								IncludeRegex: []string{"[invalid"}, // Invalid regex
							},
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to compile include regex")
}

func TestLoadConfig_InvalidBackendSelectorCEL(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{Name: "backend1"},
					},
					BackendSelector: &filterapi.MCPRouteAuthorization{
						Rules: []filterapi.MCPRouteAuthorizationRule{
							{CEL: ptr.To("request.")}, // Invalid CEL expression.
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to compile backend selector rules for route route1")
}

func TestLoadConfig_InvalidExcludeRegex(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								ExcludeRegex: []string{"[invalid"},
							},
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to compile exclude regex")
}

func TestLoadConfig_ToolSelectorChange(t *testing.T) {
	toolChangeSignaler := newMultiWatcherSignaler()
	watcher := toolChangeSignaler.Watch()

	// Initialize proxy with initial configuration directly
	proxy := &ProxyConfig{
		mcpProxyConfig: &mcpProxyConfig{
			backendListenerAddr: "http://localhost:8080",
			routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
				"route1": {
					backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
						"backend1": {Name: "backend1"},
					},
					toolSelectors: map[filterapi.MCPBackendName]*toolSelector{
						"backend1": {
							include: map[string]struct{}{"tool1": {}},
						},
					},
				},
			},
		},
		toolChangeSignaler: toolChangeSignaler,
	}

	// Update with different tool selector (tools changed)
	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								Include: []string{"tool1", "tool2"}, // Different tools
							},
						},
					},
				},
			},
		},
	}

	// Start watcher goroutines to make sure all of them are notified
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Go(func() {
			select {
			case <-watcher: // Expected
			case <-time.After(100 * time.Millisecond):
				t.Fatal("expected tools changed notification but didn't receive one")
			}
		})
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	wg.Wait()
}

func TestLoadConfig_ToolOrderDoesNotMatter(t *testing.T) {
	toolChangeSignaler := newMultiWatcherSignaler()
	watcher := toolChangeSignaler.Watch()

	// Initialize proxy with initial configuration directly
	proxy := &ProxyConfig{
		mcpProxyConfig: &mcpProxyConfig{
			backendListenerAddr: "http://localhost:8080/",
			routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
				"route1": {
					backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
						"backend1": {Name: "backend1"},
					},
					toolSelectors: map[filterapi.MCPBackendName]*toolSelector{
						"backend1": {
							include: map[string]struct{}{
								"tool-a": {},
								"tool-b": {},
								"tool-c": {},
							},
							includeRegexps: []*regexp.Regexp{
								regexp.MustCompile("^prefix.*"),
								regexp.MustCompile(".*suffix$"),
								regexp.MustCompile("^exact$"),
							},
						},
					},
				},
			},
		},
		toolChangeSignaler: toolChangeSignaler,
	}

	// Update with same tools and regexps but in different order
	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								Include:      []string{"tool-c", "tool-a", "tool-b"},        // Different order
								IncludeRegex: []string{"^exact$", ".*suffix$", "^prefix.*"}, // Different order
							},
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	// Should NOT receive tools changed notification since same tools, just different order
	select {
	case <-watcher:
		t.Fatal("unexpected tools changed notification when only order changed")
	case <-time.After(100 * time.Millisecond):
		// Expected - no notification
	}

	// Verify the tool selector still works correctly regardless of order
	route := proxy.routes["route1"]
	require.NotNil(t, route)
	selector := route.toolSelectors["backend1"]
	require.NotNil(t, selector)
	require.Contains(t, selector.include, "tool-a")
	require.Contains(t, selector.include, "tool-b")
	require.Contains(t, selector.include, "tool-c")
	require.Len(t, selector.includeRegexps, 3)
}

func Test_toolSelector_sameTools(t *testing.T) {
	reA := regexp.MustCompile("^a.*")
	reB := regexp.MustCompile("^b.*")

	tests := []struct {
		name     string
		a, b     *toolSelector
		expected bool
	}{
		{
			name:     "both nil",
			a:        nil,
			b:        nil,
			expected: true,
		},
		{
			name:     "one nil",
			a:        &toolSelector{},
			b:        nil,
			expected: false,
		},
		{
			name:     "same empty",
			a:        &toolSelector{},
			b:        &toolSelector{},
			expected: true,
		},
		{
			name:     "different exclude keys",
			a:        &toolSelector{exclude: map[string]struct{}{"foo": {}}},
			b:        &toolSelector{exclude: map[string]struct{}{"bar": {}}},
			expected: false,
		},
		{
			name:     "different include regexps",
			a:        &toolSelector{includeRegexps: []*regexp.Regexp{reA}},
			b:        &toolSelector{includeRegexps: []*regexp.Regexp{reB}},
			expected: false,
		},
		{
			name:     "same exclude regexps different order",
			a:        &toolSelector{excludeRegexps: []*regexp.Regexp{reA, reB}},
			b:        &toolSelector{excludeRegexps: []*regexp.Regexp{reB, reA}},
			expected: true,
		},
		{
			name:     "different exclude regexps",
			a:        &toolSelector{excludeRegexps: []*regexp.Regexp{reA}},
			b:        &toolSelector{excludeRegexps: []*regexp.Regexp{reB}},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.a.sameTools(tt.b))
		})
	}
}

func TestLoadConfig_AuthorizationChangeTriggersNotification(t *testing.T) {
	toolChangeSignaler := newMultiWatcherSignaler()
	watcher := toolChangeSignaler.Watch()

	proxy := &ProxyConfig{
		mcpProxyConfig: &mcpProxyConfig{
			backendListenerAddr: "http://localhost:8080",
			routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
				"route1": {
					backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
						"backend1": {Name: "backend1"},
					},
					authorization: &compiledAuthorization{
						DefaultAction: filterapi.AuthorizationActionDeny,
					},
				},
			},
		},
		toolChangeSignaler: toolChangeSignaler,
	}

	// Update with different authorization rules — should trigger tools/list notification.
	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name: "route1",
					Backends: []filterapi.MCPBackend{
						{Name: "backend1"},
					},
					Authorization: &filterapi.MCPRouteAuthorization{
						DefaultAction: filterapi.AuthorizationActionAllow, // Changed
					},
				},
			},
		},
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-watcher:
		case <-time.After(100 * time.Millisecond):
			t.Error("expected tools changed notification on authorization change but didn't receive one")
		}
	})

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)
	wg.Wait()
}

// TestLoadConfig_NeverModeToolIndex verifies that LoadConfig computes a static, route-level
// bare-tool-name → backend index for every effectively-Never-mode backend, from its declared
// toolSelector.include (minus excludes). This index is what tool-call routing consults instead
// of session-scoped state, so it must be correct and stable across reloads.
func TestLoadConfig_NeverModeToolIndex(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name:       "route1",
					PrefixMode: filterapi.PrefixModeNever, // route-level default.
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								Include: []string{"search", "fetch"},
								Exclude: []string{"fetch"}, // excluded, so should not appear in the index.
							},
						},
						{
							// Explicit per-backend override back to Always: should NOT appear in the index
							// even though it declares a toolSelector.
							Name:       "backend2",
							PrefixMode: filterapi.PrefixModeAlways,
							ToolSelector: &filterapi.MCPToolSelector{
								Include: []string{"list"},
							},
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	route := proxy.routes["route1"]
	require.NotNil(t, route)
	require.Equal(t, map[string]string{"search": "backend1"}, route.neverModeToolIndex)
	require.Empty(t, route.neverModePromptIndex)
}

// TestLoadConfig_NeverModePromptIndex verifies that PromptSelector.Include is opt-in: only
// backends that declare it get their prompts indexed for bare exposure under PrefixMode=Never.
func TestLoadConfig_NeverModePromptIndex(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name:       "route1",
					PrefixMode: filterapi.PrefixModeNever,
					Backends: []filterapi.MCPBackend{
						{
							Name: "backend1",
							ToolSelector: &filterapi.MCPToolSelector{
								Include: []string{"search"},
							},
							PromptSelector: &filterapi.MCPPromptSelector{
								Include: []string{"greeting", "farewell"},
								Exclude: []string{"farewell"},
							},
						},
						{
							// Never mode but no PromptSelector declared: opts out of bare prompt
							// naming, so this backend's prompts must not appear in the index.
							Name: "backend2",
							ToolSelector: &filterapi.MCPToolSelector{
								Include: []string{"list"},
							},
						},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	route := proxy.routes["route1"]
	require.NotNil(t, route)
	require.Equal(t, map[string]string{"greeting": "backend1"}, route.neverModePromptIndex)
}

// TestLoadConfig_NeverModeToolIndexCollision verifies the defensive, config-load-time
// collision handling: this should never happen because admission-time validation
// (validatePerBackendPrefixMode) already rejects overlapping declarations, but if it does, the
// first backend to claim a name should win rather than LoadConfig failing outright.
func TestLoadConfig_NeverModeToolIndexCollision(t *testing.T) {
	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: newMultiWatcherSignaler(),
		l:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	config := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes: []filterapi.MCPRoute{
				{
					Name:       "route1",
					PrefixMode: filterapi.PrefixModeNever,
					Backends: []filterapi.MCPBackend{
						{Name: "backend1", ToolSelector: &filterapi.MCPToolSelector{Include: []string{"search"}}},
						{Name: "backend2", ToolSelector: &filterapi.MCPToolSelector{Include: []string{"search"}}},
					},
				},
			},
		},
	}

	err := proxy.LoadConfig(t.Context(), config)
	require.NoError(t, err)

	route := proxy.routes["route1"]
	require.NotNil(t, route)
	require.Equal(t, map[string]string{"search": "backend1"}, route.neverModeToolIndex)
}

// TestLoadConfig_PrefixModeChangeTriggersNotification verifies that flipping a backend's
// effective PrefixMode (with its toolSelector.include left unchanged) is detected as a tools
// change, even though the backend set and toolSelectors content are otherwise identical.
func TestLoadConfig_PrefixModeChangeTriggersNotification(t *testing.T) {
	toolChangeSignaler := newMultiWatcherSignaler()
	watcher := toolChangeSignaler.Watch()

	proxy := &ProxyConfig{
		mcpProxyConfig:     &mcpProxyConfig{},
		toolChangeSignaler: toolChangeSignaler,
	}

	baseBackend := filterapi.MCPBackend{
		Name:         "backend1",
		ToolSelector: &filterapi.MCPToolSelector{Include: []string{"search"}},
	}
	initial := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes:              []filterapi.MCPRoute{{Name: "route1", Backends: []filterapi.MCPBackend{baseBackend}}},
		},
	}
	require.NoError(t, proxy.LoadConfig(t.Context(), initial))
	require.Empty(t, proxy.routes["route1"].neverModeToolIndex)

	// Only PrefixMode changes; the backend set and toolSelector.include are identical.
	changedBackend := baseBackend
	changedBackend.PrefixMode = filterapi.PrefixModeNever
	changed := &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://localhost:8080",
			Routes:              []filterapi.MCPRoute{{Name: "route1", Backends: []filterapi.MCPBackend{changedBackend}}},
		},
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-watcher:
		case <-time.After(100 * time.Millisecond):
			t.Error("expected tools changed notification on PrefixMode change but didn't receive one")
		}
	})

	require.NoError(t, proxy.LoadConfig(t.Context(), changed))
	wg.Wait()
	require.Equal(t, map[string]string{"search": "backend1"}, proxy.routes["route1"].neverModeToolIndex)
}
