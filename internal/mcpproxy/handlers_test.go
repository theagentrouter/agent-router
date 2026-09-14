// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	tracingapi "github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func newTestMCPProxy() *mcpRequestContext {
	return newTestMCPProxyWithTracer(noopTracer)
}

func newTestMCPProxyWithTracer(t tracingapi.MCPTracer) *mcpRequestContext {
	// reduce the iterations for faster tests. The default is a good tradeoff between security
	// and performance, but for the tests we can use fewer iterations to make tests faster.
	sessionCrypto := NewPBKDF2AesGcmSessionCrypto("test", 100)

	return &mcpRequestContext{
		metrics: stubMetrics{},
		ProxyConfig: &ProxyConfig{
			sessionCrypto:      sessionCrypto,
			toolChangeSignaler: newMultiWatcherSignaler(),
			mcpProxyConfig: &mcpProxyConfig{
				backendListenerAddr: "http://test-backend",
				routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
					"test-route": {
						toolSelectors: map[filterapi.MCPBackendName]*toolSelector{
							"backend1": {include: map[string]struct{}{"test-tool": {}}},
						},
						backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
							"backend1": {Name: "backend1"},
							"backend2": {Name: "backend2"},
						},
					},
					"test-route-another": {
						backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
							"backend3": {Name: "backend3"},
						},
					},
				},
			},
			tracer: t,
			l:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})),
		},
	}
}

func newTestMCPProxyWithOTEL(mr *sdkmetric.ManualReader, tracer tracingapi.MCPTracer) *mcpRequestContext {
	mcpProxy := newTestMCPProxyWithTracer(tracer)
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr)).Meter("test")
	mcpProxy.metrics = metrics.NewMCP(meter, nil)
	return mcpProxy
}

// TestServePOST_InitializeRequest_BackendSelectorDenied verifies that a backendSelector denying
// every route backend is treated as an authorization decision (403), not a system failure (500).
func TestMergeToolsList_AuthorizationFiltering(t *testing.T) {
	makeToken := func(scopes ...string) string {
		claims := jwt.MapClaims{}
		if len(scopes) > 0 {
			claims["scope"] = scopes
		}
		token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		signed, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
		return signed
	}

	auth := &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		Rules: []filterapi.MCPRouteAuthorizationRule{
			{
				Action: filterapi.AuthorizationActionAllow,
				Source: &filterapi.MCPAuthorizationSource{
					JWT: filterapi.JWTSource{Scopes: []string{"tools:read"}},
				},
				Target: &filterapi.MCPAuthorizationTarget{
					Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "allowed-tool"}},
				},
			},
		},
	}
	compiled, err := compileAuthorization(auth)
	require.NoError(t, err)

	responses := []broadCastResponse[mcp.ListToolsResult]{
		{
			backendName: "backend1",
			res:         mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "allowed-tool"}, {Name: "restricted-tool"}}},
		},
	}
	session := &session{route: "test-route"}

	tests := []struct {
		name      string
		token     string
		wantTools []string
	}{
		{
			name:      "caller with required scope sees allowed tool only",
			token:     makeToken("tools:read"),
			wantTools: []string{"backend1__allowed-tool"},
		},
		{
			name:      "caller without required scope sees no tools",
			token:     makeToken("other:scope"),
			wantTools: []string{},
		},
		{
			name:      "caller with no token sees no tools",
			token:     "",
			wantTools: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := newTestMCPProxy()
			proxy.routes["test-route"].authorization = compiled
			// Clear static toolSelectors so only authorization rules govern visibility.
			proxy.routes["test-route"].toolSelectors = nil
			if tt.token != "" {
				proxy.requestHeaders = http.Header{"Authorization": []string{"Bearer " + tt.token}}
			} else {
				proxy.requestHeaders = http.Header{}
			}

			result := proxy.mergeToolsList(session, responses)

			got := make([]string, len(result.Tools))
			for i, tool := range result.Tools {
				got[i] = tool.Name
			}
			require.Equal(t, tt.wantTools, got)
		})
	}
}

func TestMergeToolsList_MetaResourceURIRewrite(t *testing.T) {
	responses := []broadCastResponse[mcp.ListToolsResult]{
		{
			backendName: "backend1",
			res: mcp.ListToolsResult{Tools: []*mcp.Tool{
				{
					Name: "ui-tool",
					Meta: mcp.Meta{"ui": map[string]any{"resourceUri": "ui://prefab/tool/renderer.html"}},
				},
				{Name: "plain-tool"},
			}},
		},
	}
	proxy := newTestMCPProxy()
	proxy.routes["test-route"].toolSelectors = nil
	proxy.requestHeaders = http.Header{}
	session := &session{route: "test-route"}

	result := proxy.mergeToolsList(session, responses)
	require.Len(t, result.Tools, 2)
	byName := map[string]*mcp.Tool{}
	for _, tool := range result.Tools {
		byName[tool.Name] = tool
	}

	uiTool := byName["backend1__ui-tool"]
	require.NotNil(t, uiTool)
	require.Equal(t, "ui://backend1/prefab/tool/renderer.html", uiTool.Meta["ui"].(map[string]any)["resourceUri"])

	plainTool := byName["backend1__plain-tool"]
	require.NotNil(t, plainTool)
	require.Nil(t, plainTool.Meta)
}

func TestMergeToolsList_PreservesExplicitFalseToolHints(t *testing.T) {
	responses := []broadCastResponse[mcp.ListToolsResult]{
		{
			backendName: "backend1",
			res: mcp.ListToolsResult{Tools: []*mcp.Tool{
				{
					Name:        "write-tool",
					Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
				},
			}},
		},
	}
	proxy := newTestMCPProxy()
	proxy.routes["test-route"].toolSelectors = nil
	proxy.requestHeaders = http.Header{}

	result := proxy.mergeToolsList(&session{route: "test-route"}, responses)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"readOnlyHint":false`)
	require.Contains(t, string(encoded), `"idempotentHint":false`)
}

func TestOnError(t *testing.T) {
	rr := httptest.NewRecorder()
	onErrorResponse(rr, http.StatusBadRequest, "test error")

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))
	require.Equal(t, "test error", rr.Body.String())
}

func Test_downstreamName(t *testing.T) {
	require.Equal(t, "learn-microsoft__resource",
		downstreamResourceName("resource", "learn-microsoft"))
}

func Test_upstreamResourceName(t *testing.T) {
	cases := []struct {
		input           string
		expectedTool    string
		expectedBackend string
		expectedErr     string
	}{
		{
			input:           "learn-microsoft__microsoft_docs_search",
			expectedBackend: "learn-microsoft",
			expectedTool:    "microsoft_docs_search",
		},
		{
			input:       "namewithoutsep",
			expectedErr: "invalid resource name: namewithoutsep",
		},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			backend, tool, err := upstreamResourceName(tc.input)
			if tc.expectedErr != "" {
				require.ErrorContains(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedTool, tool)
				require.Equal(t, tc.expectedBackend, backend)
			}
		})
	}
}

func Test_downstreamResourceURI(t *testing.T) {
	t.Run("downstream resource", func(t *testing.T) {
		downstream := downstreamResourceURI("file:///tmp/file.txt", "local")
		require.Equal(t, "local+file:///tmp/file.txt", downstream)
		// Verify it is a valid URI
		parsed, err := url.Parse(downstream)
		require.NoError(t, err)
		require.Equal(t, "local+file", parsed.Scheme)
		require.Equal(t, "/tmp/file.txt", parsed.Path)
	})

	t.Run("downstream resource template", func(t *testing.T) {
		downstream := downstreamResourceURI("file:///tmp/{file}", "local")
		require.Equal(t, "local+file:///tmp/{file}", downstream)
		// Verify it is a valid URI
		parsed, err := url.Parse(downstream)
		require.NoError(t, err)
		require.Equal(t, "local+file", parsed.Scheme)
		require.Equal(t, "/tmp/{file}", parsed.Path)
	})

	t.Run("downstream ui resource keeps the ui scheme", func(t *testing.T) {
		downstream := downstreamResourceURI("ui://prefab/tool/renderer.html", "local")
		require.Equal(t, "ui://local/prefab/tool/renderer.html", downstream)
		parsed, err := url.Parse(downstream)
		require.NoError(t, err)
		require.Equal(t, "ui", parsed.Scheme)
		require.Equal(t, "local", parsed.Host)
		require.Equal(t, "/prefab/tool/renderer.html", parsed.Path)
	})
}

func Test_upstreamResourceURI(t *testing.T) {
	cases := []struct {
		input           string
		expectedURI     string
		expectedBackend string
		expectedErr     string
	}{
		{
			input:           "local+file:///tmp/file.txt",
			expectedBackend: "local",
			expectedURI:     "file:///tmp/file.txt",
		},
		{
			input:           "local+file+test:///tmp/file.txt",
			expectedBackend: "local",
			expectedURI:     "file+test:///tmp/file.txt",
		},
		{
			input:           "local+file:///tmp/{file}", // Verify we can decode resource templates
			expectedBackend: "local",
			expectedURI:     "file:///tmp/{file}",
		},
		{
			input:       "file:///tmp/file.txt",
			expectedErr: "invalid resource URI: file:///tmp/file.txt",
		},
		{
			input:           "ui://local/prefab/tool/renderer.html",
			expectedBackend: "local",
			expectedURI:     "ui://prefab/tool/renderer.html",
		},
		{
			input:           "ui://local/renderer.html",
			expectedBackend: "local",
			expectedURI:     "ui://renderer.html",
		},
		{
			// The first path segment coinciding with the backend name decodes unambiguously.
			input:           "ui://local/local/renderer.html",
			expectedBackend: "local",
			expectedURI:     "ui://local/renderer.html",
		},
		{
			// A bare ui:// URI that was never namespaced.
			input:       "ui://renderer.html",
			expectedErr: "invalid resource URI: ui://renderer.html",
		},
		{
			input:       "ui://",
			expectedErr: "invalid resource URI: ui://",
		},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			backend, uri, err := upstreamResourceURI(tc.input)
			if tc.expectedErr != "" {
				require.ErrorContains(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedURI, uri)
				require.Equal(t, tc.expectedBackend, backend)
			}
		})
	}

	t.Run("round trip", func(t *testing.T) {
		for _, uri := range []string{
			"file:///tmp/file.txt",
			"file:///tmp/{file}",
			"ui://prefab/tool/renderer.html",
			"ui://renderer.html",
			"ui://local/renderer.html", // First segment equal to the backend name must survive.
		} {
			backend, upstream, err := upstreamResourceURI(downstreamResourceURI(uri, "local"))
			require.NoError(t, err)
			require.Equal(t, "local", backend)
			require.Equal(t, uri, upstream)
		}
	})
}

func Test_rewriteMetaResourceURIs(t *testing.T) {
	cases := []struct {
		name    string
		meta    mcp.Meta
		want    mcp.Meta
		changed bool
	}{
		{
			name:    "rewrites _meta.ui.resourceUri",
			meta:    mcp.Meta{"ui": map[string]any{"resourceUri": "ui://prefab/tool/renderer.html"}},
			want:    mcp.Meta{"ui": map[string]any{"resourceUri": "ui://backend1/prefab/tool/renderer.html"}},
			changed: true,
		},
		{
			name:    "rewrites flat _meta[ui/resourceUri]",
			meta:    mcp.Meta{"ui/resourceUri": "ui://prefab/tool/renderer.html"},
			want:    mcp.Meta{"ui/resourceUri": "ui://backend1/prefab/tool/renderer.html"},
			changed: true,
		},
		{
			name: "rewrites both conventions when present",
			meta: mcp.Meta{
				"ui":             map[string]any{"resourceUri": "ui://prefab/tool/renderer.html"},
				"ui/resourceUri": "ui://prefab/tool/renderer.html",
			},
			want: mcp.Meta{
				"ui":             map[string]any{"resourceUri": "ui://backend1/prefab/tool/renderer.html"},
				"ui/resourceUri": "ui://backend1/prefab/tool/renderer.html",
			},
			changed: true,
		},
		{
			name:    "nil meta",
			meta:    nil,
			want:    nil,
			changed: false,
		},
		{
			name:    "empty meta",
			meta:    mcp.Meta{},
			want:    mcp.Meta{},
			changed: false,
		},
		{
			name:    "ui present but resourceUri missing",
			meta:    mcp.Meta{"ui": map[string]any{"other": "val"}},
			want:    mcp.Meta{"ui": map[string]any{"other": "val"}},
			changed: false,
		},
		{
			name:    "resourceUri is not a string",
			meta:    mcp.Meta{"ui": map[string]any{"resourceUri": 42}},
			want:    mcp.Meta{"ui": map[string]any{"resourceUri": 42}},
			changed: false,
		},
		{
			name:    "ui value is not a map",
			meta:    mcp.Meta{"ui": "not-a-map"},
			want:    mcp.Meta{"ui": "not-a-map"},
			changed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := rewriteMetaResourceURIs(tc.meta, "backend1")
			require.Equal(t, tc.changed, changed)
			require.Equal(t, tc.want, tc.meta)
		})
	}
}

func Test_rewriteToolResultURIs(t *testing.T) {
	backend := filterapi.MCPBackendName("backend1")

	t.Run("rewrites ResourceLink URI", func(t *testing.T) {
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ResourceLink{URI: "ui://prefab/link.html"}},
		}
		require.True(t, rewriteToolResultURIs(result, backend))
		require.Equal(t, "ui://backend1/prefab/link.html", result.Content[0].(*mcp.ResourceLink).URI)
	})

	t.Run("rewrites EmbeddedResource URI", func(t *testing.T) {
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "ui://prefab/embed.html"}}},
		}
		require.True(t, rewriteToolResultURIs(result, backend))
		require.Equal(t, "ui://backend1/prefab/embed.html", result.Content[0].(*mcp.EmbeddedResource).Resource.URI)
	})

	t.Run("nil EmbeddedResource.Resource", func(t *testing.T) {
		result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: nil}}}
		require.False(t, rewriteToolResultURIs(result, backend))
	})

	t.Run("rewrites _meta.ui.resourceUri", func(t *testing.T) {
		result := &mcp.CallToolResult{
			Meta: mcp.Meta{"ui": map[string]any{"resourceUri": "ui://meta/renderer.html"}},
		}
		require.True(t, rewriteToolResultURIs(result, backend))
		require.Equal(t, "ui://backend1/meta/renderer.html", result.Meta["ui"].(map[string]any)["resourceUri"])
	})

	t.Run("non-ui URIs are namespaced with the scheme prefix form", func(t *testing.T) {
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ResourceLink{URI: "file:///tmp/file.txt"}},
		}
		require.True(t, rewriteToolResultURIs(result, backend))
		require.Equal(t, "backend1+file:///tmp/file.txt", result.Content[0].(*mcp.ResourceLink).URI)
	})

	t.Run("no resource URIs", func(t *testing.T) {
		result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hi"}}}
		require.False(t, rewriteToolResultURIs(result, backend))
	})
}

func TestExtractForwardHeaders(t *testing.T) {
	// Test that extractForwardHeaders correctly reads headers from the request.
	tests := []struct {
		name           string
		requestHeaders map[string]string
		forwardHeaders []string
		wantHeaders    map[string]string
	}{
		{
			name: "extract configured headers",
			requestHeaders: map[string]string{
				"X-User-Id":    "user123",
				"X-User-Email": "user@example.com",
			},
			forwardHeaders: []string{"X-User-Id", "X-User-Email"},
			wantHeaders: map[string]string{
				"X-User-Id":    "user123",
				"X-User-Email": "user@example.com",
			},
		},
		{
			name: "extract nested claim header",
			requestHeaders: map[string]string{
				"X-User-Roles": `["admin","user"]`,
			},
			forwardHeaders: []string{"X-User-Roles"},
			wantHeaders: map[string]string{
				"X-User-Roles": `["admin","user"]`,
			},
		},
		{
			name:           "missing header returns nil",
			requestHeaders: map[string]string{
				// X-Missing is not set
			},
			forwardHeaders: []string{"X-Missing"},
			wantHeaders:    nil,
		},
		{
			name: "mixed existing and missing headers",
			requestHeaders: map[string]string{
				"X-User-Id": "user123",
				// X-Missing is not set
			},
			forwardHeaders: []string{"X-User-Id", "X-Missing"},
			wantHeaders: map[string]string{
				"X-User-Id": "user123",
			},
		},
		{
			name:           "empty forward headers",
			requestHeaders: map[string]string{"X-User-Id": "user123"},
			forwardHeaders: []string{},
			wantHeaders:    nil,
		},
		{
			name:           "no headers on request",
			requestHeaders: map[string]string{},
			forwardHeaders: []string{"X-User-Id"},
			wantHeaders:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			// Set headers (simulating what Envoy's JWT filter does)
			for header, value := range tt.requestHeaders {
				headers.Set(header, value)
			}

			result := extractForwardHeaders(headers, tt.forwardHeaders)
			require.Equal(t, tt.wantHeaders, result)
		})
	}
}

func TestExtractPerBackendForwardHeaders(t *testing.T) {
	tests := []struct {
		name           string
		requestHeaders map[string]string
		mappings       []filterapi.MCPHeaderForward
		wantHeaders    map[string]string
	}{
		{
			name: "basic forwarding without rename",
			requestHeaders: map[string]string{
				"X-Api-Key": "secret123",
				"X-User-Id": "user456",
			},
			mappings: []filterapi.MCPHeaderForward{
				{Name: "X-Api-Key"},
				{Name: "X-User-Id"},
			},
			wantHeaders: map[string]string{
				"X-Api-Key": "secret123",
				"X-User-Id": "user456",
			},
		},
		{
			name: "header renaming via BackendHeader",
			requestHeaders: map[string]string{
				"Authorization": "Bearer tok123",
			},
			mappings: []filterapi.MCPHeaderForward{
				{Name: "Authorization", BackendHeader: "X-Original-Auth"},
			},
			wantHeaders: map[string]string{
				"X-Original-Auth": "Bearer tok123",
			},
		},
		{
			name: "mixed rename and passthrough",
			requestHeaders: map[string]string{
				"Authorization":   "Bearer tok",
				"X-Jira-Token":    "jira-secret",
				"X-Not-Forwarded": "should-not-appear",
			},
			mappings: []filterapi.MCPHeaderForward{
				{Name: "Authorization", BackendHeader: "X-Backend-Auth"},
				{Name: "X-Jira-Token"},
			},
			wantHeaders: map[string]string{
				"X-Backend-Auth": "Bearer tok",
				"X-Jira-Token":   "jira-secret",
			},
		},
		{
			name:           "missing header returns nil",
			requestHeaders: map[string]string{},
			mappings: []filterapi.MCPHeaderForward{
				{Name: "X-Missing"},
			},
			wantHeaders: nil,
		},
		{
			name: "mixed existing and missing headers",
			requestHeaders: map[string]string{
				"X-Present": "val",
			},
			mappings: []filterapi.MCPHeaderForward{
				{Name: "X-Present"},
				{Name: "X-Missing"},
			},
			wantHeaders: map[string]string{
				"X-Present": "val",
			},
		},
		{
			name:           "empty mappings returns nil",
			requestHeaders: map[string]string{"X-Foo": "bar"},
			mappings:       []filterapi.MCPHeaderForward{},
			wantHeaders:    nil,
		},
		{
			name:           "nil mappings returns nil",
			requestHeaders: map[string]string{"X-Foo": "bar"},
			mappings:       nil,
			wantHeaders:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			for header, value := range tt.requestHeaders {
				headers.Set(header, value)
			}

			result := extractPerBackendForwardHeaders(headers, tt.mappings)
			require.Equal(t, tt.wantHeaders, result)
		})
	}
}

func secureID(t *testing.T, proxy *mcpRequestContext, sessionID string) string {
	secure, err := proxy.sessionCrypto.Encrypt(sessionID)
	require.NoError(t, err)
	return secure
}

func TestAddMCPHeaders_MetadataValueGuard(t *testing.T) {
	longURI := "file://" + strings.Repeat("a", maxMCPMetadataHeaderValueLen)

	tests := []struct {
		name    string
		id      string
		uri     string
		wantID  string
		wantURI string
	}{
		{
			name:    "plain values are set",
			id:      "req-1",
			uri:     "file://config.yaml",
			wantID:  "req-1",
			wantURI: "file://config.yaml",
		},
		{
			name:    "non-ascii is allowed, http.Transport accepts it",
			id:      "req-2",
			uri:     "file://café/résumé.txt",
			wantID:  "req-2",
			wantURI: "file://café/résumé.txt",
		},
		{
			name:    "control characters are dropped, not forwarded",
			id:      "req-3",
			uri:     "file://a\nb",
			wantID:  "req-3",
			wantURI: "",
		},
		{
			name:    "a control character in the JSON-RPC ID drops only that header",
			id:      "req\r\n4",
			uri:     "file://config.yaml",
			wantID:  "",
			wantURI: "file://config.yaml",
		},
		{
			name:    "oversized values are dropped rather than truncated",
			id:      "req-5",
			uri:     longURI,
			wantID:  "req-5",
			wantURI: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "http://backend", nil)
			require.NoError(t, err)
			id, err := jsonrpc.MakeID(tt.id)
			require.NoError(t, err)
			addMCPHeaders(req,
				&jsonrpc.Request{ID: id, Method: "resources/read"},
				&mcp.ReadResourceParams{URI: tt.uri},
				"route1", "backend1")

			require.Equal(t, tt.wantID, req.Header.Get(internalapi.MCPMetadataHeaderRequestID))
			require.Equal(t, tt.wantURI, req.Header.Get(internalapi.MCPMetadataHeaderResourceURI))
			// Whatever was dropped, the request must still be sendable: the headers exist only for
			// access logging, so they must never be what makes a proxied call fail.
			require.NoError(t, req.Header.Write(io.Discard))
			for _, vs := range req.Header {
				for _, v := range vs {
					require.NotContains(t, v, "\n")
					require.NotContains(t, v, "\r")
				}
			}
		})
	}
}

// TestServePOST_InitializeRequest_ForwardsExtensions asserts a backend's extensions capability
// reaches the client. Without it a backend that advertises an extension, e.g. MCP Apps
// (io.modelcontextprotocol/ui), appears to support none once it is behind the proxy, so the client
// never negotiates it and ignores the ui metadata the backend puts on its tools.

func TestServePOST_InvalidJSONRPC(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{invalid json}"))
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid JSON-RPC message")
}

func TestServePOST_OversizedBody(t *testing.T) {
	proxy := newTestMCPProxy()
	proxy.maxRequestBodySize = 16 // tiny limit to exercise the guard without allocating a large buffer.

	body := strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{}}`) // > 16 bytes
	req := httptest.NewRequest(http.MethodPost, "/mcp", body)
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	require.Contains(t, rr.Body.String(), "request body too large")
}

func TestServePOST_EarlyReturnErrorMetrics(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*mcpRequestContext) *http.Request
		wantStatus     int
		wantErrType    metrics.MCPErrorType
		wantMethodName string // empty when the method is not known yet
	}{
		{
			name: "invalid session ID",
			setup: func(*mcpRequestContext) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","id":"1"}`))
				req.Header.Set(sessionIDHeader, "invalid-session-id")
				return req
			},
			wantStatus:  http.StatusBadRequest,
			wantErrType: metrics.MCPErrorInvalidSessionID,
			// Session validation runs before requestMethod is assigned.
		},
		{
			name: "missing session ID",
			setup: func(*mcpRequestContext) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"test-tool"},"id":"1"}`))
			},
			wantStatus:     http.StatusBadRequest,
			wantErrType:    metrics.MCPErrorInvalidSessionID,
			wantMethodName: "tools/call",
		},
		{
			name: "oversized body",
			setup: func(proxy *mcpRequestContext) *http.Request {
				proxy.maxRequestBodySize = 16
				return httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{}}`))
			},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantErrType: metrics.MCPErrorInternal,
		},
		{
			name: "body read error",
			setup: func(*mcpRequestContext) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/mcp", errReader{err: errors.New("read failed")})
			},
			wantStatus:  http.StatusBadRequest,
			wantErrType: metrics.MCPErrorInternal,
		},
		{
			name: "invalid JSON-RPC",
			setup: func(*mcpRequestContext) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{invalid json}"))
			},
			wantStatus:  http.StatusBadRequest,
			wantErrType: metrics.MCPErrorInvalidJSONRPC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr := sdkmetric.NewManualReader()
			proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
			t.Cleanup(func() {
				if err := mr.Shutdown(t.Context()); err != nil {
					t.Logf("failed to shutdown manual reader: %v", err)
				}
			})

			req := tt.setup(proxy)
			rr := httptest.NewRecorder()
			proxy.servePOST(rr, req)

			require.Equal(t, tt.wantStatus, rr.Code)

			count, sum := testotel.GetHistogramValues(t, mr, "mcp.request.duration", attribute.NewSet(
				attribute.String("error.type", string(tt.wantErrType))))
			require.Equal(t, 1, int(count)) // nolint: gosec
			require.Greater(t, sum, 0.0)

			if tt.wantMethodName != "" {
				methodCount := testotel.GetCounterValue(t, mr, "mcp.method.count", attribute.NewSet(
					attribute.String("mcp.method.name", tt.wantMethodName),
					attribute.String("status", string(metrics.MCPStatusError))))
				require.Equal(t, 1, int(methodCount))
			}
		})
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// TestMergeToolsList_PerBackendPrefixMode verifies that per-backend PrefixMode works:
// Never-mode backends expose bare names (resolved from the static, config-level
// neverModeToolIndex that LoadConfig would have computed from toolSelector.include);
// Always-mode backends keep the prefix.
func TestMergeToolsList_PerBackendPrefixMode(t *testing.T) {
	tests := []struct {
		name               string
		b1Mode             filterapi.PrefixMode
		b2Mode             filterapi.PrefixMode
		neverModeToolIndex map[string]string
		wantTools          []string
	}{
		{
			name:      "both Always — both prefixed",
			b1Mode:    filterapi.PrefixModeAlways,
			b2Mode:    filterapi.PrefixModeAlways,
			wantTools: []string{"backend1__search", "backend2__list"},
		},
		{
			name:               "both Never — both bare",
			b1Mode:             filterapi.PrefixModeNever,
			b2Mode:             filterapi.PrefixModeNever,
			neverModeToolIndex: map[string]string{"search": "backend1", "list": "backend2"},
			wantTools:          []string{"search", "list"},
		},
		{
			name:               "mixed — backend1 Never bare, backend2 Always prefixed",
			b1Mode:             filterapi.PrefixModeNever,
			b2Mode:             filterapi.PrefixModeAlways,
			neverModeToolIndex: map[string]string{"search": "backend1"},
			wantTools:          []string{"search", "backend2__list"},
		},
		{
			name:   "cross-mode collision — Always backend's prefixed name dropped",
			b1Mode: filterapi.PrefixModeNever,
			b2Mode: filterapi.PrefixModeAlways,
			// backend2's tool, once prefixed, collides with a name backend1 (Never mode)
			// declared ownership of. This can only happen if backend2's actual tool name is
			// "backend2__search"; it must be dropped rather than shown twice under one name.
			neverModeToolIndex: map[string]string{"search": "backend1", "backend2__search": "backend1"},
			wantTools:          []string{"search"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &mcpRequestContext{
				metrics: stubMetrics{},
				ProxyConfig: &ProxyConfig{
					mcpProxyConfig: &mcpProxyConfig{
						routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
							"test-route": {
								backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
									"backend1": {Name: "backend1", PrefixMode: tt.b1Mode},
									"backend2": {Name: "backend2", PrefixMode: tt.b2Mode},
								},
								neverModeToolIndex: tt.neverModeToolIndex,
							},
						},
					},
					l: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})),
				},
				requestHeaders: http.Header{},
			}

			var responses []broadCastResponse[mcp.ListToolsResult]
			if tt.name == "cross-mode collision — Always backend's prefixed name dropped" {
				responses = []broadCastResponse[mcp.ListToolsResult]{
					{backendName: "backend1", res: mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "search"}}}},
					{backendName: "backend2", res: mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "search"}}}},
				}
			} else {
				responses = []broadCastResponse[mcp.ListToolsResult]{
					{backendName: "backend1", res: mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "search"}}}},
					{backendName: "backend2", res: mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "list"}}}},
				}
			}

			s := &session{route: "test-route"}
			result := proxy.mergeToolsList(s, responses)

			got := make([]string, len(result.Tools))
			for i, tool := range result.Tools {
				got[i] = tool.Name
			}
			require.ElementsMatch(t, tt.wantTools, got)
		})
	}
}

// TestMergePromptsList_PerBackendPrefixMode verifies prompt naming under PrefixMode: a
// Never-mode backend only gets bare prompt names for names it opted into via
// promptSelector.include (route.neverModePromptIndex); without that declaration its prompts
// stay prefixed even under Never mode, and a bare name colliding with an Always-mode backend's
// prefixed name is dropped.
func TestMergePromptsList_PerBackendPrefixMode(t *testing.T) {
	tests := []struct {
		name                 string
		b1Mode               filterapi.PrefixMode
		b2Mode               filterapi.PrefixMode
		neverModePromptIndex map[string]string
		responses            []broadCastResponse[mcp.ListPromptsResult]
		wantPrompts          []string
	}{
		{
			name:   "both Always — both prefixed",
			b1Mode: filterapi.PrefixModeAlways,
			b2Mode: filterapi.PrefixModeAlways,
			responses: []broadCastResponse[mcp.ListPromptsResult]{
				{backendName: "backend1", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "greeting"}}}},
				{backendName: "backend2", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "farewell"}}}},
			},
			wantPrompts: []string{"backend1__greeting", "backend2__farewell"},
		},
		{
			name:                 "Never mode with declared promptSelector — bare",
			b1Mode:               filterapi.PrefixModeNever,
			b2Mode:               filterapi.PrefixModeAlways,
			neverModePromptIndex: map[string]string{"greeting": "backend1"},
			responses: []broadCastResponse[mcp.ListPromptsResult]{
				{backendName: "backend1", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "greeting"}}}},
				{backendName: "backend2", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "farewell"}}}},
			},
			wantPrompts: []string{"greeting", "backend2__farewell"},
		},
		{
			name:   "Never mode without promptSelector — stays prefixed",
			b1Mode: filterapi.PrefixModeNever,
			b2Mode: filterapi.PrefixModeAlways,
			// No neverModePromptIndex entry for backend1: it didn't opt in.
			responses: []broadCastResponse[mcp.ListPromptsResult]{
				{backendName: "backend1", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "greeting"}}}},
			},
			wantPrompts: []string{"backend1__greeting"},
		},
		{
			name:   "cross-mode collision — Always backend's prefixed name dropped",
			b1Mode: filterapi.PrefixModeNever,
			b2Mode: filterapi.PrefixModeAlways,
			neverModePromptIndex: map[string]string{
				"greeting":           "backend1",
				"backend2__greeting": "backend1",
			},
			responses: []broadCastResponse[mcp.ListPromptsResult]{
				{backendName: "backend1", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "greeting"}}}},
				{backendName: "backend2", res: mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "greeting"}}}},
			},
			wantPrompts: []string{"greeting"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &mcpRequestContext{
				metrics: stubMetrics{},
				ProxyConfig: &ProxyConfig{
					mcpProxyConfig: &mcpProxyConfig{
						routes: map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
							"test-route": {
								backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
									"backend1": {Name: "backend1", PrefixMode: tt.b1Mode},
									"backend2": {Name: "backend2", PrefixMode: tt.b2Mode},
								},
								neverModePromptIndex: tt.neverModePromptIndex,
							},
						},
					},
					l: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})),
				},
			}

			result := proxy.mergePromptsList(&session{route: "test-route"}, tt.responses)
			got := make([]string, len(result.Prompts))
			for i, p := range result.Prompts {
				got[i] = p.Name
			}
			require.ElementsMatch(t, tt.wantPrompts, got)
		})
	}
}
