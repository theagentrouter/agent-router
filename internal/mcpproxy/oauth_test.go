// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestIsProtectedResourceMetadataRequest(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/.well-known/oauth-protected-resource", true},
		{"/.well-known/oauth-protected-resource/mcp", true},
		{"/.well-known/oauth-protected-resource/tenant/mcp", true},
		// A path that merely starts with the same characters is a different resource.
		{"/.well-known/oauth-protected-resource-evil", false},
		{"/.well-known/oauth-authorization-server/mcp", false},
		{"/mcp/.well-known/oauth-protected-resource", false},
		{"/mcp", false},
		{"/", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			require.Equal(t, tc.want, isProtectedResourceMetadataRequest(tc.path))
		})
	}
}

func TestExternalScheme(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{"forwarded https", "https", "https"},
		{"forwarded http", "http", "http"},
		// Chained proxies append to the list; the first entry is closest to the client.
		{"chained proxies", "https, http", "https"},
		{"padded value", "  https  ", "https"},
		// Envoy always sets the header, so the fallback only matters for direct requests.
		{"absent", "", "http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/mcp", nil)
			if tc.header != "" {
				r.Header.Set("x-forwarded-proto", tc.header)
			}
			require.Equal(t, tc.want, externalScheme(r))
		})
	}
}

func TestExternalPath(t *testing.T) {
	t.Run("uses the routed request path", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/.well-known/oauth-protected-resource/mcp?a=b", nil)
		require.Equal(t, "/.well-known/oauth-protected-resource/mcp", externalPath(r))
	})

	// Nothing strips x-ai-eg-* off inbound requests, so a client must not be able to redirect
	// the proxy to a different document by claiming a different original path.
	t.Run("client supplied original path headers are ignored", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/mcp", nil)
		r.Header.Set(internalapi.OriginalPathHeader, "/.well-known/oauth-protected-resource/mcp")
		r.Header.Set(internalapi.EnvoyOriginalPathHeader, "/.well-known/oauth-protected-resource/mcp")
		require.Equal(t, "/mcp", externalPath(r))
		require.False(t, isProtectedResourceMetadataRequest(externalPath(r)))
	})
}

func TestResourceIdentifier(t *testing.T) {
	newRequest := func(host, proto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://placeholder/.well-known/oauth-protected-resource/mcp", nil)
		r.Host = host
		if proto != "" {
			r.Header.Set("x-forwarded-proto", proto)
		}
		return r
	}

	for _, tc := range []struct {
		name         string
		host         string
		proto        string
		resourcePath string
		oauth        *filterapi.MCPRouteOAuth
		want         string
	}{
		{
			name:         "https",
			host:         "api.example.com",
			proto:        "https",
			resourcePath: "/mcp",
			want:         "https://api.example.com/mcp",
		},
		{
			// A plain HTTP gateway must not advertise an https identifier: the client
			// compares it against the URL it actually used.
			name:         "plain http gateway",
			host:         "api.example.com",
			proto:        "http",
			resourcePath: "/mcp",
			want:         "http://api.example.com/mcp",
		},
		{
			// The port is part of the identifier, and is the case the control plane could
			// never have recovered from a Gateway listener hostname.
			name:         "non standard port",
			host:         "api.example.com:8443",
			proto:        "https",
			resourcePath: "/mcp",
			want:         "https://api.example.com:8443/mcp",
		},
		{
			name:         "nested path",
			host:         "api.example.com",
			proto:        "https",
			resourcePath: "/tenant/mcp",
			want:         "https://api.example.com/tenant/mcp",
		},
		{
			name:         "root path has no trailing slash",
			host:         "api.example.com",
			proto:        "https",
			resourcePath: "/",
			want:         "https://api.example.com",
		},
		{
			name:         "configured resource overrides the request",
			host:         "internal.svc.cluster.local:9856",
			proto:        "http",
			resourcePath: "/mcp",
			oauth:        &filterapi.MCPRouteOAuth{Resource: "https://api.example.com/mcp"},
			want:         "https://api.example.com/mcp",
		},
		{
			name:         "configured resource is trimmed",
			host:         "api.example.com",
			proto:        "https",
			resourcePath: "/mcp",
			oauth:        &filterapi.MCPRouteOAuth{Resource: "https://api.example.com/mcp/"},
			want:         "https://api.example.com/mcp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oauth := tc.oauth
			if oauth == nil {
				oauth = &filterapi.MCPRouteOAuth{}
			}
			require.Equal(t, tc.want, resourceIdentifier(newRequest(tc.host, tc.proto), oauth, tc.resourcePath))
		})
	}
}

func TestResourceMetadataURL(t *testing.T) {
	for _, tc := range []struct {
		name         string
		host         string
		proto        string
		resourcePath string
		oauth        *filterapi.MCPRouteOAuth
		want         string
	}{
		{
			name:         "path component is appended after the well-known path",
			host:         "api.example.com",
			proto:        "https",
			resourcePath: "/mcp",
			want:         "https://api.example.com/.well-known/oauth-protected-resource/mcp",
		},
		{
			name:         "port is preserved in the authority",
			host:         "api.example.com:8443",
			proto:        "https",
			resourcePath: "/tenant/mcp",
			want:         "https://api.example.com:8443/.well-known/oauth-protected-resource/tenant/mcp",
		},
		{
			name:         "root path",
			host:         "api.example.com",
			proto:        "http",
			resourcePath: "/",
			want:         "http://api.example.com/.well-known/oauth-protected-resource",
		},
		{
			name:         "configured resource with a path",
			host:         "internal:9856",
			proto:        "http",
			resourcePath: "/mcp",
			oauth:        &filterapi.MCPRouteOAuth{Resource: "https://api.example.com/mcp/v1"},
			want:         "https://api.example.com/.well-known/oauth-protected-resource/mcp/v1",
		},
		{
			name:         "configured resource without a path",
			host:         "internal:9856",
			proto:        "http",
			resourcePath: "/mcp",
			oauth:        &filterapi.MCPRouteOAuth{Resource: "https://api.example.com"},
			want:         "https://api.example.com/.well-known/oauth-protected-resource",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
			r.Host = tc.host
			r.Header.Set("x-forwarded-proto", tc.proto)
			oauth := tc.oauth
			if oauth == nil {
				oauth = &filterapi.MCPRouteOAuth{}
			}
			require.Equal(t, tc.want, resourceMetadataURL(r, oauth, tc.resourcePath))
		})
	}
}

// newOAuthTestProxy returns a mux serving a single route named routeName with the given OAuth
// configuration, exercising the same LoadConfig path the real filter config takes.
func newOAuthTestProxy(t *testing.T, routeName string, oauth *filterapi.MCPRouteOAuth) http.Handler {
	t.Helper()
	proxy, mux, err := NewMCPProxy(slog.Default(), stubMetrics{}, noopTracer, NewPBKDF2AesGcmSessionCrypto("test", 100), nil)
	require.NoError(t, err)
	require.NoError(t, proxy.LoadConfig(t.Context(), &filterapi.Config{
		MCPConfig: &filterapi.MCPConfig{
			BackendListenerAddr: "http://127.0.0.1:10088",
			Routes: []filterapi.MCPRoute{{
				Name:     routeName,
				Backends: []filterapi.MCPBackend{{Name: "backend1"}},
				OAuth:    oauth,
			}},
		},
	}))
	return mux
}

func TestServeOAuthProtectedResourceMetadata(t *testing.T) {
	const routeName = "default/my-route"

	get := func(h http.Handler, host, proto, path, route string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://placeholder"+path, nil)
		r.Host = host
		r.Header.Set("x-forwarded-proto", proto)
		if route != "" {
			r.Header.Set(internalapi.MCPRouteHeader, route)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	t.Run("derives the resource from the request", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{
			Issuer:          "https://auth.example.com",
			ResourceName:    "My MCP Tools",
			ScopesSupported: []string{"read", "write"},
		})

		w := get(h, "api.example.com:8443", "https", "/.well-known/oauth-protected-resource/mcp", routeName)
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "application/json", w.Header().Get("Content-Type"))
		require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))

		var doc map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		require.Equal(t, "https://api.example.com:8443/mcp", doc["resource"])
		require.Equal(t, []any{"https://auth.example.com"}, doc["authorization_servers"])
		require.Equal(t, []any{"header"}, doc["bearer_methods_supported"])
		require.Equal(t, "My MCP Tools", doc["resource_name"])
		require.Equal(t, []any{"read", "write"}, doc["scopes_supported"])
	})

	t.Run("the same config serves a different host correctly", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{Issuer: "https://auth.example.com"})

		for _, tc := range []struct{ host, proto, want string }{
			{"api.example.com", "https", "https://api.example.com/mcp"},
			{"localhost:1975", "http", "http://localhost:1975/mcp"},
			{"tenant-b.example.com", "https", "https://tenant-b.example.com/mcp"},
		} {
			w := get(h, tc.host, tc.proto, "/.well-known/oauth-protected-resource/mcp", routeName)
			require.Equal(t, http.StatusOK, w.Code)
			var doc map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
			require.Equal(t, tc.want, doc["resource"], "host %s", tc.host)
		}
	})

	t.Run("an explicitly configured resource still wins", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{
			Issuer:   "https://auth.example.com",
			Resource: "https://canonical.example.com/mcp",
		})
		w := get(h, "api.example.com", "https", "/.well-known/oauth-protected-resource/mcp", routeName)
		require.Equal(t, http.StatusOK, w.Code)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		require.Equal(t, "https://canonical.example.com/mcp", doc["resource"])
	})

	t.Run("optional fields are omitted when unset", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{Issuer: "https://auth.example.com"})
		w := get(h, "api.example.com", "https", "/.well-known/oauth-protected-resource/mcp", routeName)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		require.NotContains(t, doc, "resource_name")
		require.NotContains(t, doc, "scopes_supported")
		require.NotContains(t, doc, "resource_documentation")
		require.NotContains(t, doc, "resource_policy_uri")
		require.NotContains(t, doc, "resource_signing_alg_values_supported")
	})

	t.Run("all optional fields are propagated", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{
			Issuer:                            "https://auth.example.com",
			ResourceName:                      "name",
			ScopesSupported:                   []string{"read"},
			ResourceSigningAlgValuesSupported: []string{"RS256"},
			ResourceDocumentation:             "https://docs.example.com",
			ResourcePolicyURI:                 "https://policy.example.com",
		})
		w := get(h, "api.example.com", "https", "/.well-known/oauth-protected-resource/mcp", routeName)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		require.Equal(t, "name", doc["resource_name"])
		require.Equal(t, []any{"RS256"}, doc["resource_signing_alg_values_supported"])
		require.Equal(t, "https://docs.example.com", doc["resource_documentation"])
		require.Equal(t, "https://policy.example.com", doc["resource_policy_uri"])
	})

	t.Run("unknown route is not found", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{Issuer: "https://auth.example.com"})
		w := get(h, "api.example.com", "https", "/.well-known/oauth-protected-resource/mcp", "default/other-route")
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("route without OAuth is not found", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, nil)
		w := get(h, "api.example.com", "https", "/.well-known/oauth-protected-resource/mcp", routeName)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("CORS preflight", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{Issuer: "https://auth.example.com"})
		r := httptest.NewRequest(http.MethodOptions, "http://placeholder/.well-known/oauth-protected-resource/mcp", nil)
		r.Header.Set(internalapi.MCPRouteHeader, routeName)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, "GET, OPTIONS", w.Header().Get("Access-Control-Allow-Methods"))
	})

	t.Run("non GET methods are rejected", func(t *testing.T) {
		h := newOAuthTestProxy(t, routeName, &filterapi.MCPRouteOAuth{Issuer: "https://auth.example.com"})
		r := httptest.NewRequest(http.MethodDelete, "http://placeholder/.well-known/oauth-protected-resource/mcp", nil)
		r.Header.Set(internalapi.MCPRouteHeader, routeName)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}
