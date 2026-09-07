// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	"github.com/envoyproxy/ai-gateway/internal/tracing"
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

func TestServeGET_MissingSessionID(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rr := httptest.NewRecorder()

	proxy.serveGET(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "missing session ID")
}

func TestServeGET_InvalidSessionID(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set(sessionIDHeader, "invalid-session-id")
	rr := httptest.NewRecorder()

	proxy.serveGET(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid session ID")
}

func TestServeGET_OK(t *testing.T) {
	proxy := newTestMCPProxy()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	sessionID := secureID(t, proxy, "@@backend1:dGVzdC1zZXNzaW9u") // "test-session" base64 encoded.
	req.Header.Set(sessionIDHeader, sessionID)
	rr := httptest.NewRecorder()

	proxy.serveGET(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)
}

func TestServerDELETE_MissingSessionID(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	rr := httptest.NewRecorder()

	proxy.serverDELETE(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "missing session ID")
}

func TestServerDELETE_InvalidSessionID(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set(sessionIDHeader, "invalid-session-id")
	rr := httptest.NewRecorder()

	proxy.serverDELETE(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid session ID")
}

func TestServeDELETE_OK(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	sessionID := secureID(t, proxy, "@@backend1:dGVzdC1zZXNzaW9u") // "test-session" base64 encoded.
	req.Header.Set(sessionIDHeader, sessionID)
	rr := httptest.NewRecorder()
	proxy.serverDELETE(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

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

func TestServePOST_InvalidSessionID(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"test-tool"},"id":"1"}`))
	req.Header.Set(sessionIDHeader, "invalid-session-id")
	rr := httptest.NewRecorder()
	proxy.servePOST(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid session ID")
}

func TestServePOST_MissingSessionID(t *testing.T) {
	proxy := newTestMCPProxy()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"test-tool"},"id":"1"}`))
	rr := httptest.NewRecorder()
	proxy.servePOST(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "missing session ID")
}

func TestServePOST_InitializeRequest(t *testing.T) {
	// Create a test server to simulate the mcp backend listener.
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasSessionID := r.Header.Get(sessionIDHeader) != ""
		backend := r.Header.Get(internalapi.MCPBackendHeader)

		// simulate an initialize failure for the second backend.
		if backend == "backend2" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("simulated backend error"))
			return
		}

		if !hasSessionID { // Call to initialize.
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(validInitializeResponse))
		} else { // Call to notifications/initialized.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(testServer.Close)

	mr := sdkmetric.NewManualReader()
	proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
	proxy.backendListenerAddr = testServer.URL

	// Create initialize request.
	id, err := jsonrpc.MakeID("test-1")
	require.NoError(t, err)
	initReq := &jsonrpc.Request{
		Method: "initialize",
		ID:     id,
		Params: []byte(`{
    "protocolVersion": "2024-11-05",
    "capabilities": {
      "roots": {
        "listChanged": true
      },
      "sampling": {},
      "elicitation": {}
    },
    "clientInfo": {
      "name": "ExampleClient",
      "title": "Example Client Display Name",
      "version": "1.0.0"
    }
  }`),
	}
	body, err := jsonrpc.EncodeMessage(initReq)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// This header is used to establish MCP session with the backends associated with the route.
	// It is set by the frontend listeners based on the selected route.
	req.Header.Set(internalapi.MCPRouteHeader, "test-route")
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, req)

	if rr.Code != http.StatusOK {
		t.Logf("Response body: %s", rr.Body.String())
	}
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	require.NotEmpty(t, rr.Header().Get(sessionIDHeader))

	decrypted, err := proxy.sessionCrypto.Decrypt(rr.Header().Get(sessionIDHeader))
	require.NoError(t, err)
	perBackendSessions, _, err := clientToGatewaySessionID(decrypted).backendSessionIDs()
	require.NoError(t, err)
	require.ElementsMatch(t, []filterapi.MCPBackendName{"backend1"}, slices.Collect(maps.Keys(perBackendSessions)))

	// backend1 is the only backend that successfully initialized.
	count, sum := testotel.GetHistogramValues(t, mr, "mcp.initialization.duration", attribute.NewSet(
		attribute.String("mcp.backend", "backend1"),
	))
	require.Equal(t, 1, int(count)) // nolint: gosec
	require.Greater(t, sum, 0.0)

	capaCount := testotel.GetCounterValue(t, mr, "mcp.capabilities.negotiated", attribute.NewSet(
		attribute.String("mcp.backend", "backend1"),
		attribute.String("capability.type", "tools"),
		attribute.String("capability.side", "server")))
	require.Equal(t, 1, int(capaCount))

	capaCount = testotel.GetCounterValue(t, mr, "mcp.capabilities.negotiated", attribute.NewSet(
		attribute.String("capability.type", "roots"),
		attribute.String("capability.side", "client")))
	require.Equal(t, 1, int(capaCount))
}

// TestServePOST_InitializeRequest_BackendSelectorDenied verifies that a backendSelector denying
// every route backend is treated as an authorization decision (403), not a system failure (500).
func TestServePOST_InitializeRequest_BackendSelectorDenied(t *testing.T) {
	proxy := newTestMCPProxy()
	proxy.routes["test-route"].backendSelector = mustCompileBackendSelector(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
	})

	id, err := jsonrpc.MakeID("test-1")
	require.NoError(t, err)
	initReq := &jsonrpc.Request{Method: "initialize", ID: id, Params: []byte(`{"protocolVersion": "2024-11-05"}`)}
	body, err := jsonrpc.EncodeMessage(initReq)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalapi.MCPRouteHeader, "test-route")
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
}

// TestServePOST_JSONRPCRequest tests various jsonrpc.Request body, not jsonrpc.Response.
func TestServePOST_JSONRPCRequest(t *testing.T) {
	tests := []struct {
		name             string
		method           string
		upstreamResponse string
		// expected HTTP status code from the proxy.
		expStatusCode int
		// expected body substring on non-200 status code, i.e. when expStatusCode != 200.
		expBodyOnNonOKStatus string
		params               any
		validate             func(*testing.T, []byte)
	}{
		{
			method:               "unknown-method",
			params:               &mcp.ListToolsParams{},
			expStatusCode:        400,
			expBodyOnNonOKStatus: `unsupported method: unknown-method`,
		},
		{
			method:        "notifications/cancelled",
			params:        &mcp.ListToolsParams{},
			expStatusCode: 202,
		},
		{
			name:          "initialize invalid param",
			method:        "initialize",
			params:        "invalid-param",
			expStatusCode: 400,
		},
		{
			name:          "initialize without route header",
			method:        "initialize",
			params:        &mcp.InitializeParams{},
			expStatusCode: 500,
		},
		{
			method:           "tools/list",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"tools":[{"name":"my-tool"},{"name":"test-tool"}]}}`,
			params:           &mcp.ListToolsParams{},
			expStatusCode:    200,
			validate: func(t *testing.T, raw []byte) {
				var result mcp.ListToolsResult
				require.NoError(t, json.Unmarshal(raw, &result))
				require.Len(t, result.Tools, 1)
				require.Equal(t, "backend1__test-tool", result.Tools[0].Name)
			},
		},
		{
			method:           "notifications/roots/list_changed",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{}}`,
			params:           &mcp.RootsListChangedParams{},
			expStatusCode:    202,
		},
		{
			name:          "notifications/roots/list_changed invalid param",
			method:        "notifications/roots/list_changed",
			params:        "invalid-param",
			expStatusCode: 400,
		},
		{
			method:           "prompts/list",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"prompts":[{"name":"my-prompt"}]}}`,
			params:           &mcp.ListPromptsParams{},
			expStatusCode:    200,
			validate: func(t *testing.T, raw []byte) {
				var result mcp.ListPromptsResult
				require.NoError(t, json.Unmarshal(raw, &result))
				require.Len(t, result.Prompts, 1)
				require.Equal(t, "backend1__my-prompt", result.Prompts[0].Name)
			},
		},
		{
			name:          "prompts/list invalid param type",
			method:        "prompts/list",
			params:        "invalid",
			expStatusCode: 400,
		},
		{
			method:           "resources/list",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"resources":[{"name":"my-resource"}]}}`,
			params:           &mcp.ListResourcesParams{},
			expStatusCode:    200,
			validate: func(t *testing.T, raw []byte) {
				var result mcp.ListResourcesResult
				require.NoError(t, json.Unmarshal(raw, &result))
				require.Len(t, result.Resources, 1)
				require.Equal(t, "backend1__my-resource", result.Resources[0].Name)
			},
		},
		{
			method:           "resources/templates/list",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"resourceTemplates":[{"name":"my-template"}]}}`,
			params:           &mcp.ListResourceTemplatesParams{},
			expStatusCode:    200,
			validate: func(t *testing.T, raw []byte) {
				var result mcp.ListResourceTemplatesResult
				require.NoError(t, json.Unmarshal(raw, &result))
				require.Len(t, result.ResourceTemplates, 1)
				require.Equal(t, "backend1__my-template", result.ResourceTemplates[0].Name)
			},
		},
		{
			method:           "completion/complete",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"completion": {"values":["completed text"]}}}`,
			expStatusCode:    200,
			params: &mcp.CompleteParams{
				Ref: &mcp.CompleteReference{Name: "backend1__my-completion", Type: "ref/prompt"},
			},
			validate: func(t *testing.T, raw []byte) {
				var result mcp.CompleteResult
				require.NoError(t, json.Unmarshal(raw, &result))
				fmt.Printf("Completion result: %+v\n", result)
				require.Len(t, result.Completion.Values, 1)
				require.Equal(t, "completed text", result.Completion.Values[0])
			},
		},
		{
			name:          "completion/complete invalid param type",
			method:        "completion/complete",
			expStatusCode: 400,
			// Invalid mismatched type.
			params:               "aaaaaaaaaaaa",
			expBodyOnNonOKStatus: `invalid params`,
		},
		{
			method:           "notifications/progress",
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"completion": {"values":["completed text"]}}}`,
			expStatusCode:    200,
			params:           &mcp.ProgressNotificationParams{ProgressToken: "1234__i__backend1"},
		},
		{
			name:                 "notifications/progress invalid param",
			method:               "notifications/progress",
			expStatusCode:        400,
			params:               "aaaaaaaa",
			expBodyOnNonOKStatus: `invalid params`,
		},
		{
			name:                 "notifications/progress invalid token",
			method:               "notifications/progress",
			expStatusCode:        400,
			params:               &mcp.ProgressNotificationParams{ProgressToken: "invalid-token"},
			expBodyOnNonOKStatus: `invalid progressToken invalid-token`,
		},
		{
			method:        "notifications/initialized",
			expStatusCode: 202,
			params:        &mcp.InitializeParams{},
		},
		{
			method:        "logging/setLevel",
			expStatusCode: 200,
			params:        &mcp.SetLoggingLevelParams{Level: "debug"},
		},
		{
			name:                 "logging/setLevel invalid param",
			method:               "logging/setLevel",
			expStatusCode:        400,
			params:               "invalid-param",
			expBodyOnNonOKStatus: `invalid set logging level params`,
		},
		{
			method:           "resources/subscribe",
			expStatusCode:    200,
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"subscriptionId":"sub-1234"}}`,
			params:           &mcp.SubscribeParams{URI: "backend1+file://my-resource"},
		},
		{
			name:                 "resources/subscribe invalid param",
			method:               "resources/subscribe",
			expStatusCode:        400,
			params:               &mcp.SubscribeParams{URI: "file://my-resource"},
			expBodyOnNonOKStatus: `invalid resource URI: file://my-resource`,
		},
		{
			method:           "resources/unsubscribe",
			expStatusCode:    200,
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"subscriptionId":"sub-1234"}}`,
			params:           &mcp.UnsubscribeParams{URI: "backend1+file://my-resource"},
		},
		{
			name:                 "resources/unsubscribe invalid param",
			method:               "resources/unsubscribe",
			expStatusCode:        400,
			params:               &mcp.UnsubscribeParams{URI: "file://my-resource"},
			expBodyOnNonOKStatus: `invalid resource URI: file://my-resource`,
		},
		{
			method:           "resources/read",
			params:           &mcp.ReadResourceParams{URI: "backend1+file://my-resource"},
			upstreamResponse: `{"jsonrpc":"2.0","id":"1","result":{"contents":[{"uri":"file://my-resource"}]}}`,
			expStatusCode:    200,
			validate: func(t *testing.T, raw []byte) {
				var result mcp.ReadResourceResult
				require.NoError(t, json.Unmarshal(raw, &result))
				require.Len(t, result.Contents, 1)
				require.Equal(t, "backend1+file://my-resource", result.Contents[0].URI)
			},
		},
		{
			name:                 "resources/read invalid param",
			method:               "resources/read",
			expStatusCode:        400,
			params:               &mcp.ReadResourceParams{URI: "file://my-resource"},
			expBodyOnNonOKStatus: `invalid resource URI: file://my-resource`,
		},
	}

	for _, tt := range tests {
		t.Run(cmp.Or(tt.name, tt.method), func(t *testing.T) {
			// Mock backend server.
			backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.upstreamResponse))
			}))
			t.Cleanup(backendServer.Close)

			proxy := newTestMCPProxy()
			proxy.backendListenerAddr = backendServer.URL

			// Create a session with the test tool route.
			sessionID := secureID(t, proxy, "test-route@@backend1:dGVzdC1zZXNzaW9u") // "test-session" base64 encoded.

			// Create tools/call request.
			id, err := jsonrpc.MakeID("1")
			require.NoError(t, err)
			paramsData, err := json.Marshal(tt.params)
			require.NoError(t, err)
			toolReq := &jsonrpc.Request{
				Method: tt.method,
				ID:     id,
				Params: paramsData,
			}
			body, err := jsonrpc.EncodeMessage(toolReq)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(sessionIDHeader, sessionID)
			rr := httptest.NewRecorder()

			proxy.servePOST(rr, req)
			require.NoError(t, err)
			require.Equal(t, tt.expStatusCode, rr.Code, rr.Body.String())
			if tt.expStatusCode != 200 {
				require.Contains(t, rr.Body.String(), tt.expBodyOnNonOKStatus)
				return
			}

			var resp *jsonrpc.Response
			if rr.Header().Get("content-type") != "text/event-stream" {
				// Regular JSON response.
				var msg jsonrpc.Message
				msg, err = jsonrpc.DecodeMessage(rr.Body.Bytes())
				require.NoError(t, err)
				var ok bool
				resp, ok = msg.(*jsonrpc.Response)
				require.True(t, ok)
			} else {
				p := newSSEEventParser(rr.Body, "backend1")
				var event *sseEvent
				event, err = p.next()
				require.NoError(t, err)
				require.Len(t, event.messages, 1)
				var ok bool
				resp, ok = event.messages[0].(*jsonrpc.Response)
				require.True(t, ok)
			}
			if tt.validate != nil {
				tt.validate(t, resp.Result)
			}
		})
	}
}

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

func TestServePOST_ToolsCallRequest(t *testing.T) {
	tests := []struct {
		name        string
		route       string
		tool        string
		wantBackend string
		wantStatus  int
	}{
		{name: "backend1", route: "test-route", tool: "backend1__test-tool", wantBackend: "backend1", wantStatus: http.StatusOK},
		{name: "backend2", route: "test-route", tool: "backend2__test-tool", wantBackend: "backend2", wantStatus: http.StatusOK},
		{name: "backend3", route: "test-route-another", tool: "backend3__test-tool", wantBackend: "backend3", wantStatus: http.StatusOK},
		{name: "test-route-another", tool: "unknown__test-tool", wantBackend: "unknown", wantStatus: http.StatusNotFound},
		{name: "backend1-not-whitelisted", route: "test-route", tool: "backend1__custom-tool", wantBackend: "backend1", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock backend server.
			backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Validate that all metadata headers are set.
				require.Equal(t, tt.wantBackend, r.Header.Get(internalapi.MCPBackendHeader))
				require.Equal(t, "tools/call", r.Header.Get(internalapi.MCPMetadataHeaderMethod))
				require.Equal(t, tt.tool, r.Header.Get(internalapi.MCPMetadataHeaderRequestID))
				// The headers a tools/call populates. Resource-scoped headers are asserted in the
				// resources/read and resources/subscribe tests instead, since a tools/call leaves them unset.
				for _, h := range []string{
					internalapi.MCPBackendHeader,
					internalapi.MCPRouteHeader,
					sessionIDHeader,
					internalapi.MCPMetadataHeaderMethod,
					internalapi.MCPMetadataHeaderRequestID,
					internalapi.MCPMetadataHeaderToolName,
				} {
					require.NotEmpty(t, r.Header.Get(h))
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":"success"}`))
			}))
			t.Cleanup(backendServer.Close)

			mr := sdkmetric.NewManualReader()
			proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
			proxy.backendListenerAddr = backendServer.URL

			// Create a session with the test tool route.
			sessionID := secureID(t, proxy, tt.route+"@@"+tt.wantBackend+":dGVzdC1zZXNzaW9u") // "test-session" base64 encoded.

			// Create tools/call request.
			id, err := jsonrpc.MakeID(tt.tool)
			require.NoError(t, err)
			params := &mcp.CallToolParams{Name: tt.tool}
			paramsData, err := json.Marshal(params)
			require.NoError(t, err)
			toolReq := &jsonrpc.Request{
				Method: "tools/call",
				ID:     id,
				Params: paramsData,
			}
			body, err := jsonrpc.EncodeMessage(toolReq)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			// This header is used to establish MCP session with the backends associated with the route.
			// It is set by the frontend listeners based on the selected route.
			req.Header.Set(internalapi.MCPRouteHeader, "test-route")
			req.Header.Set(sessionIDHeader, sessionID)
			rr := httptest.NewRecorder()

			proxy.servePOST(rr, req)

			require.Equal(t, tt.wantStatus, rr.Code)

			var countAttrs, durationAttrs attribute.Set
			if tt.wantStatus == http.StatusOK {
				countAttrs = attribute.NewSet(
					attribute.String("mcp.backend", tt.wantBackend),
					attribute.String("mcp.method.name", "tools/call"),
					attribute.String("status", "success"),
				)
				durationAttrs = attribute.NewSet(
					attribute.String("mcp.backend", tt.wantBackend),
				)
			} else {
				countAttrs = attribute.NewSet(
					attribute.String("mcp.backend", tt.wantBackend),
					attribute.String("mcp.method.name", "tools/call"),
					attribute.String("status", "error"),
				)
				durationAttrs = attribute.NewSet(
					attribute.String("mcp.backend", tt.wantBackend),
					attribute.String("error.type", string(metrics.MCPErrorInvalidParam)),
				)
			}

			methodCount := testotel.GetCounterValue(t, mr, "mcp.method.count", countAttrs)
			require.Equal(t, 1, int(methodCount))

			count, sum := testotel.GetHistogramValues(t, mr, "mcp.request.duration", durationAttrs)
			require.Equal(t, 1, int(count)) // nolint: gosec
			require.Greater(t, sum, 0.0)
		})
	}
}

func TestServePOST_UnsupportedMethod(t *testing.T) {
	mr := sdkmetric.NewManualReader()
	proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
	t.Cleanup(func() {
		if err := mr.Shutdown(t.Context()); err != nil {
			t.Logf("failed to shutdown manual reader: %v", err)
		}
	})

	// Create request with unsupported method.
	id, err := jsonrpc.MakeID("test-1")
	require.NoError(t, err)
	req := &jsonrpc.Request{
		Method: "unsupported/method",
		ID:     id,
	}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set(sessionIDHeader, secureID(t, proxy, "test-route@@backend1:dGVzdC1zZXNzaW9u")) // "test-session" base64 encoded.
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, httpReq)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "unsupported method")

	methodCount := testotel.GetCounterValue(t, mr, "mcp.method.count", attribute.NewSet(
		attribute.String("mcp.method.name", "unsupported/method"),
		attribute.String("status", "error")))
	require.Equal(t, 1, int(methodCount))

	count, sum := testotel.GetHistogramValues(t, mr, "mcp.request.duration", attribute.NewSet(
		attribute.String("error.type", "unsupported_method")))
	require.Equal(t, 1, int(count)) // nolint: gosec
	require.Greater(t, sum, 0.0)
}

func TestHandleToolCallRequest_UnknownBackend(t *testing.T) {
	proxy := newTestMCPProxy()
	s := &session{
		reqCtx:             proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{},
	}

	params := &mcp.CallToolParams{Name: "unknown-backend__unknown-tool"}
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()

	_, err := proxy.handleToolCallRequest(t.Context(), s, rr, &jsonrpc.Request{}, params, nil, httpReq)
	require.Error(t, err)

	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "unknown backend unknown-backend")
}

// TestHandleToolCallRequest_NoSession covers a backend that is configured on the route
// (getBackendForRoute succeeds) but has no session in this particular session (e.g.
// excluded by backendSelector). This should be 403 instead of 400 or other error codes.
func TestHandleToolCallRequest_NoSession(t *testing.T) {
	proxy := newTestMCPProxy()
	s := &session{
		reqCtx:             proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
		route:              "test-route",
	}

	params := &mcp.CallToolParams{Name: "backend2__some-tool"}
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()

	_, err := proxy.handleToolCallRequest(t.Context(), s, rr, &jsonrpc.Request{}, params, nil, httpReq)
	require.ErrorIs(t, err, errSessionNotFound)

	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "no MCP session found for backend backend2")
}

func TestHandleToolCallRequest_BackendError(t *testing.T) {
	// Mock backend server that returns error.
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("backend error"))
	}))
	t.Cleanup(backendServer.Close)

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL
	s := &session{
		reqCtx: proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {
				sessionID: "test-session",
			},
		},
		route: "test-route",
	}

	params := &mcp.CallToolParams{Name: "backend1__test-tool"}
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()

	_, err := proxy.handleToolCallRequest(t.Context(), s, rr, &jsonrpc.Request{}, params, nil, httpReq)
	require.Error(t, err)

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, rr.Body.String(), "call to backend1 failed with status code 500, body=backend error")
}

func TestHandleToolCallRequest_InvalidToolName(t *testing.T) {
	// Mock backend server that returns a JSON-RPC error saying the tool doesn't exist
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request to extract the ID
		body, _ := io.ReadAll(r.Body)
		req, _ := jsonrpc.DecodeMessage(body)
		reqMsg := req.(*jsonrpc.Request)

		// Return a JSON-RPC error response indicating tool not found
		resp := &jsonrpc.Response{
			ID: reqMsg.ID,
			Error: &jsonrpc.Error{
				Code:    jsonrpc.CodeMethodNotFound,
				Message: "unknown tool \"unknown_tool\"",
			},
		}
		respBody, _ := jsonrpc.EncodeMessage(resp)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(backendServer.Close)

	reqCtx := newTestMCPProxy()
	reqCtx.backendListenerAddr = backendServer.URL

	// Add "unknown_tool" to the allowed list for backend1
	// This simulates a tool being in the config but not actually on the MCP server
	reqCtx.routes["test-route"].toolSelectors["backend1"].include["unknown_tool"] = struct{}{}

	s := &session{
		reqCtx: reqCtx,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {
				sessionID: "test-session",
			},
		},
		route: "test-route",
	}

	// Use a tool that is in the allowed list (unknown_tool is in backend1's selector)
	// but doesn't actually exist on the MCP server
	params := &mcp.CallToolParams{Name: "backend1__unknown_tool"}
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()

	id := mustJSONRPCRequestID()
	req := &jsonrpc.Request{ID: id, Method: "tools/call"}

	_, err := reqCtx.handleToolCallRequest(t.Context(), s, rr, req, params, nil, httpReq)
	// JSON-RPC errors are application-level errors that should be returned for proper metrics tracking,
	// but they're not treated as span exceptions since the protocol worked correctly.
	require.Error(t, err)

	// Verify it's a JSON-RPC error
	var jsonrpcErr *jsonrpc.Error
	require.ErrorAs(t, err, &jsonrpcErr)
	require.Contains(t, err.Error(), "unknown tool")

	// Response should be written with the JSON-RPC error
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "unknown tool")
}

func TestHandleToolCallRequest_ToolResultWithIsError(t *testing.T) {
	// Mock backend server that returns a successful JSON-RPC response, but the tool result has isError: true.
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request to extract the ID
		body, _ := io.ReadAll(r.Body)
		req, _ := jsonrpc.DecodeMessage(body)
		reqMsg := req.(*jsonrpc.Request)

		// Create a CallToolResult with IsError: true
		toolResult := mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{Text: "missing required parameter: owner"},
			},
		}
		resultJSON, _ := json.Marshal(toolResult)

		// Return a successful JSON-RPC response with the error tool result
		resp := &jsonrpc.Response{
			ID:     reqMsg.ID,
			Result: resultJSON,
		}
		respBody, _ := jsonrpc.EncodeMessage(resp)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(backendServer.Close)

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL
	s := &session{
		reqCtx: proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {
				sessionID: "test-session",
			},
		},
		route: "test-route",
	}

	params := &mcp.CallToolParams{Name: "backend1__test-tool"}
	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()

	id := mustJSONRPCRequestID()
	req := &jsonrpc.Request{ID: id, Method: "tools/call"}

	_, err := proxy.handleToolCallRequest(t.Context(), s, rr, req, params, nil, httpReq)
	// isError: true means the tool executed successfully but returned an error result.
	// An error is returned for proper metrics tracking, but it's treated as an application-level
	// error (not a span exception) since the protocol worked correctly and the LLM needs to see these errors.
	require.Error(t, err)

	// Verify it's a structured errToolCall with the expected details
	var toolErr *errToolCall
	require.ErrorAs(t, err, &toolErr)
	require.Equal(t, "test-tool", toolErr.toolName)
	require.Equal(t, "backend1", toolErr.backend)
	require.Contains(t, err.Error(), "missing required parameter: owner")

	// The response should be written to the client (HTTP 200)
	require.Equal(t, http.StatusOK, rr.Code)

	// Verify the response contains the error message
	require.Contains(t, rr.Body.String(), "missing required parameter: owner")
}

func TestProxyResponseBody_JSONResponse(t *testing.T) {
	proxy := newTestMCPProxy()

	id := mustJSONRPCRequestID()
	resp := &jsonrpc.Response{ID: id, Result: []byte(`{"test": "data"}`)}
	body, err := jsonrpc.EncodeMessage(resp)
	require.NoError(t, err)

	httpResp := &http.Response{
		Header:     http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		StatusCode: http.StatusOK,
	}

	rr := httptest.NewRecorder()

	proxy.proxyResponseBody(t.Context(), nil, rr, httpResp, &jsonrpc.Request{ID: id}, filterapi.MCPBackend{Name: "mybackend"}, nil) //nolint:errcheck

	require.Contains(t, rr.Body.String(), "test")
	require.Contains(t, rr.Body.String(), "data")
	// Verify that the response ID matches the request ID.
	require.Contains(t, rr.Body.String(), id.Raw())
}

func TestProxyResponseBody_JSONResponseWithBOM(t *testing.T) {
	proxy := newTestMCPProxy()

	id := mustJSONRPCRequestID()
	resp := &jsonrpc.Response{ID: id, Result: []byte(`{"test": "bom"}`)}
	body, err := jsonrpc.EncodeMessage(resp)
	require.NoError(t, err)

	bomBody := append([]byte{0xEF, 0xBB, 0xBF}, body...)
	httpResp := &http.Response{
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(bomBody)),
		StatusCode: http.StatusOK,
	}

	rr := httptest.NewRecorder()

	proxy.proxyResponseBody(t.Context(), nil, rr, httpResp, &jsonrpc.Request{ID: id}, filterapi.MCPBackend{Name: "mybackend"}, nil) //nolint:errcheck

	require.Contains(t, rr.Body.String(), "bom")
	require.Contains(t, rr.Body.String(), id.Raw())
}

func TestProxyResponseBody_SSEResponse(t *testing.T) {
	proxy := newTestMCPProxy()

	// Create SSE response.
	id := mustJSONRPCRequestID()
	res := &jsonrpc.Response{ID: id}
	msg, err := jsonrpc.EncodeMessage(res)
	require.NoError(t, err)

	invalidSeverToClientReq := &jsonrpc.Request{Method: "roots/list", ID: id, Params: []byte(`{"invalid": "json"}`)}
	invalidReqBody, err := jsonrpc.EncodeMessage(invalidSeverToClientReq)
	require.NoError(t, err)

	sseBody := fmt.Sprintf(`
event: test
data: %s

event: test
data: %s

`, invalidReqBody, msg)

	httpResp := &http.Response{
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
		StatusCode: http.StatusOK,
	}

	rr := httptest.NewRecorder()
	sessionID := secureID(t, proxy, "@@backend1:"+base64.StdEncoding.EncodeToString([]byte("test-session")))
	eventID := secureID(t, proxy, "@@backend1:"+base64.StdEncoding.EncodeToString([]byte("_1")))
	s, err := proxy.sessionFromID(secureClientToGatewaySessionID(sessionID), secureClientToGatewayEventID(eventID))
	require.NoError(t, err)

	proxy.proxyResponseBody(t.Context(), s, rr, httpResp, &jsonrpc.Request{Method: "test", ID: id}, filterapi.MCPBackend{Name: "mybackend"}, nil) //nolint:errcheck

	require.Contains(t, rr.Body.String(), "event: test")
	require.Contains(t, rr.Body.String(), "data:")

	// Verify that the response ID matches the request ID.
	require.Contains(t, rr.Body.String(), id.Raw())
}

func TestOnError(t *testing.T) {
	rr := httptest.NewRecorder()
	onErrorResponse(rr, http.StatusBadRequest, "test error")

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))
	require.Equal(t, "test error", rr.Body.String())
}

func TestRecordResponse(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		msg := jsonrpc.Response{Result: []byte(`{"test": "data"}`)}
		proxy := newTestMCPProxy()
		proxy.recordResponse(t.Context(), &msg)
	})
	t.Run("request", func(t *testing.T) {
		for _, tc := range []struct {
			method string
		}{
			{method: "notifications/prompts/list_changed"},
			{method: "notifications/resources/list_changed"},
			{method: "notifications/resources/updated"},
			{method: "notifications/progress"},
			{method: "roots/list"},
			{method: "notifications/message"},
			{method: "sampling/createMessage"},
			{method: "elicitation/create"},
			{method: "notifications/tools/list_changed"},
		} {
			msg := jsonrpc.Request{Method: tc.method}
			proxy := newTestMCPProxy()
			proxy.recordResponse(t.Context(), &msg)
		}
	})

	t.Run("unsupported method", func(t *testing.T) {
		proxy := newTestMCPProxy()

		id, err := jsonrpc.MakeID("test")
		require.NoError(t, err)
		req := &jsonrpc.Request{Method: "unsupported/server/method", ID: id}
		proxy.recordResponse(t.Context(), req)
	})
	t.Run("unsupported message type", func(t *testing.T) {
		proxy := newTestMCPProxy()
		proxy.recordResponse(t.Context(), nil)
	})
}

func TestServePOST_NotificationsInitialized(t *testing.T) {
	proxy := newTestMCPProxy()

	// Create notifications/initialized request.
	req := &jsonrpc.Request{
		Method: "notifications/initialized",
		Params: []byte(`{}`),
	}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set(sessionIDHeader, secureID(t, proxy, "test-route@@backend1:dGVzdC1zZXNzaW9u")) // "test-session" base64 encoded.
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, httpReq)

	require.Equal(t, http.StatusAccepted, rr.Code)
}

func TestServePOST_PromptsGet(t *testing.T) {
	tracer := &fakeTracer{}
	proxy := newTestMCPProxyWithTracer(tracer)

	// Create a valid session.
	sessionID := secureID(t, proxy, "@@default-backend:"+base64.StdEncoding.EncodeToString([]byte("test-session")))

	// Create prompts/get request.
	params := &mcp.GetPromptParams{Name: "somebackend__test-prompt"}
	paramsData, err := json.Marshal(params)
	require.NoError(t, err)
	req := &jsonrpc.Request{
		Method: "prompts/get",
		Params: paramsData,
	}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set(sessionIDHeader, sessionID)
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, httpReq)

	// Should try to route but fail since no backend server.
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "unknown backend somebackend")

	// EndSpanOnError called.
	require.NotNil(t, tracer.span)
}

// TestHandlePromptGetRequest_NoSession covers a backend that is configured on the route
// (getBackendForRoute succeeds) but has no session here, e.g. excluded by backendSelector.
// This should be 403 instead of 400 or other error codes.
func TestHandlePromptGetRequest_NoSession(t *testing.T) {
	proxy := newTestMCPProxy()
	rr := httptest.NewRecorder()
	s := &session{
		reqCtx:             proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
		route:              "test-route",
	}
	_, err := proxy.handlePromptGetRequest(t.Context(), s, rr, &jsonrpc.Request{}, &mcp.GetPromptParams{Name: "backend2__test-prompt"})
	require.ErrorIs(t, err, errSessionNotFound)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "no MCP session found for backend backend2")
}

// TestServePOST_RecordsClientSession pins that the span carries the
// client-facing session, i.e. the one the MCP client sees in the session header,
// rather than a gateway-to-backend session. Broadcast methods route to several
// backends, each with its own upstream session, so recording a per-backend one
// on the span would be last-writer-wins.
func TestServePOST_RecordsClientSession(t *testing.T) {
	tracer := &fakeTracer{}
	proxy := newTestMCPProxyWithTracer(tracer)

	sessionID := secureID(t, proxy, "@@default-backend:"+base64.StdEncoding.EncodeToString([]byte("test-session")))

	params := &mcp.GetPromptParams{Name: "somebackend__test-prompt"}
	paramsData, err := json.Marshal(params)
	require.NoError(t, err)
	req := &jsonrpc.Request{Method: "prompts/get", Params: paramsData}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set(sessionIDHeader, sessionID)
	proxy.servePOST(httptest.NewRecorder(), httpReq)

	require.NotNil(t, tracer.span)
	require.Equal(t, sessionID, tracer.span.clientSessionID)
}

func TestServePOST_InvalidToolCallParams(t *testing.T) {
	tracer := &fakeTracer{}
	proxy := newTestMCPProxyWithTracer(tracer)

	// Create tools/call request where the params is an array instead of object.
	// This should fail when trying to unmarshal into CallToolParams struct.
	req := &jsonrpc.Request{
		Method: "tools/call",
		Params: []byte(`["invalid", "array", "params"]`), // array instead of object.
	}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	// Need to provide a session ID for tools/call requests.
	sessionID := secureID(t, proxy, "@@backend1:"+base64.StdEncoding.EncodeToString([]byte("test-session")))
	httpReq.Header.Set(sessionIDHeader, sessionID)
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, httpReq)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid params")

	require.Nil(t, tracer.span)
}

func TestServePOST_InvalidPromptsGetParams(t *testing.T) {
	proxy := newTestMCPProxy()

	// Create prompts/get request where the params is an array instead of object.
	// This should fail when trying to unmarshal into GetPromptParams struct.
	req := &jsonrpc.Request{
		Method: "prompts/get",
		Params: []byte(`["invalid", "array", "params"]`), // array instead of object.
	}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set(sessionIDHeader, secureID(t, proxy, "test-route@@backend1:dGVzdC1zZXNzaW9u")) // "test-session" base64 encoded.
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, httpReq)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid params")
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

func Test_maybeResponseModify(t *testing.T) {
	ctx := t.Context()
	m := newTestMCPProxy()
	backend := filterapi.MCPBackendName("backend1")

	t.Run("rewrites _meta.ui.resourceUri", func(t *testing.T) {
		raw, err := json.Marshal(&mcp.CallToolResult{
			Meta: mcp.Meta{"ui": map[string]any{"resourceUri": "ui://prefab/renderer.html"}},
		})
		require.NoError(t, err)
		msg := &jsonrpc.Response{Result: raw}
		require.NoError(t, m.maybeResponseModify(ctx, &jsonrpc.Request{Method: "tools/call"}, msg, backend))
		var got mcp.CallToolResult
		require.NoError(t, json.Unmarshal(msg.Result, &got))
		require.Equal(t, "ui://backend1/prefab/renderer.html", got.Meta["ui"].(map[string]any)["resourceUri"])
	})

	t.Run("rewrites ResourceLink in Content", func(t *testing.T) {
		raw, err := json.Marshal(&mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ResourceLink{URI: "ui://prefab/link.html"}},
		})
		require.NoError(t, err)
		msg := &jsonrpc.Response{Result: raw}
		require.NoError(t, m.maybeResponseModify(ctx, &jsonrpc.Request{Method: "tools/call"}, msg, backend))
		var got mcp.CallToolResult
		require.NoError(t, json.Unmarshal(msg.Result, &got))
		require.Equal(t, "ui://backend1/prefab/link.html", got.Content[0].(*mcp.ResourceLink).URI)
	})

	t.Run("no resource URIs leaves result bytes unchanged", func(t *testing.T) {
		raw, err := json.Marshal(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hi"}}})
		require.NoError(t, err)
		before := append([]byte(nil), raw...)
		msg := &jsonrpc.Response{Result: raw}
		require.NoError(t, m.maybeResponseModify(ctx, &jsonrpc.Request{Method: "tools/call"}, msg, backend))
		require.Equal(t, before, []byte(msg.Result))
	})

	t.Run("non-standard result shape passes through", func(t *testing.T) {
		raw := []byte(`{"content":"not-an-array"}`)
		msg := &jsonrpc.Response{Result: raw}
		require.NoError(t, m.maybeResponseModify(ctx, &jsonrpc.Request{Method: "tools/call"}, msg, backend))
		require.Equal(t, raw, []byte(msg.Result))
	})

	t.Run("resources/read rewrites ui Contents URIs keeping the scheme", func(t *testing.T) {
		raw, err := json.Marshal(&mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: "ui://prefab/renderer.html"}},
		})
		require.NoError(t, err)
		msg := &jsonrpc.Response{Result: raw}
		require.NoError(t, m.maybeResponseModify(ctx, &jsonrpc.Request{Method: "resources/read"}, msg, backend))
		var got mcp.ReadResourceResult
		require.NoError(t, json.Unmarshal(msg.Result, &got))
		require.Equal(t, "ui://backend1/prefab/renderer.html", got.Contents[0].URI)
	})
}

func TestExtractSubject(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "unsigned token with principal",
			token: "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJtY3AifQ.",
			want:  "mcp",
		},
		{
			name:  "signed token with principal",
			token: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJtY3AifQ.3FpuVHQFtGubZnErnKK6RULYffuZmtgmS3g8D8z8ykM",
			want:  "mcp",
		},
		{
			name:  "invalid token",
			token: "invalid-token",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/mcp", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+tt.token)

			require.Equal(t, tt.want, extractSubject(req))
		})
	}

	t.Run("no auth header", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/mcp", nil)
		require.NoError(t, err)
		require.Empty(t, extractSubject(req))
	})

	t.Run("invalid auth header", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/mcp", nil)
		req.Header.Set("Authorization", "basic foobar")
		require.NoError(t, err)
		require.Empty(t, extractSubject(req))
	})

	t.Run("bearer with no token", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/mcp", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "bearer")
		require.Empty(t, extractSubject(req))
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

func TestMCPProxy_handleCompletionComplete(t *testing.T) {
	reqID, _ := jsonrpc.MakeID("id")

	proxy := newTestMCPProxy()
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		reqRaw, err := jsonrpc.DecodeMessage(body)
		require.NoError(t, err)
		req, ok := reqRaw.(*jsonrpc.Request)
		require.True(t, ok)

		// Verify method and params.
		require.Equal(t, "completion/complete", req.Method)
		var params mcp.CompleteParams
		require.NoError(t, json.Unmarshal(req.Params, &params))
		require.NotNil(t, params.Ref)
		if params.Ref.Name != "" {
			require.Equal(t, "my-prompt", params.Ref.Name)
		} else {
			require.Equal(t, "file://my-uri", params.Ref.URI)
		}
		// Respond with a valid completion response.
		resp := &jsonrpc.Response{ID: reqID}
		resp.Result, _ = json.Marshal(&mcp.CompleteResult{})
		respBody, err := jsonrpc.EncodeMessage(resp)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(testServer.Close)

	proxy.backendListenerAddr = testServer.URL

	for _, tc := range []struct {
		param  *mcp.CompleteParams
		errMsg string
	}{
		{
			param: &mcp.CompleteParams{Ref: &mcp.CompleteReference{
				Type: "ref/prompt",
				Name: "backend1__my-prompt",
			}},
		},
		{
			param: &mcp.CompleteParams{
				Ref: &mcp.CompleteReference{
					Type: "ref/resource",
					URI:  "backend1+file://my-uri",
				},
			},
		},
	} {
		rr := httptest.NewRecorder()
		_, err := proxy.handleCompletionComplete(t.Context(), &session{
			reqCtx: proxy,
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"backend1": {sessionID: "test-session"},
			},
			route: "test-route",
		}, rr, &jsonrpc.Request{ID: reqID, Method: "completion/complete"}, &mcp.CompleteParams{Ref: tc.param.Ref}, nil)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, rr.Code)
		require.JSONEq(t, `{"jsonrpc":"2.0","id":"id","result":{"completion":{"values":null}}}`, rr.Body.String())
	}
}

// TestMCPProxy_handleCompletionComplete_NoSession covers a backend that is configured on
// the route (getBackendForRoute succeeds) but has no session in this particular session
// (e.g. excluded by backendSelector). Passing a non-nil span reproduces the case that used
// to panic on a nil *compositeSessionEntry before the session was ever checked for nil.
func TestMCPProxy_handleCompletionComplete_NoSession(t *testing.T) {
	reqID, _ := jsonrpc.MakeID("id")
	proxy := newTestMCPProxy()

	rr := httptest.NewRecorder()
	span := &fakeSpan{}
	var err error
	require.NotPanics(t, func() {
		_, err = proxy.handleCompletionComplete(t.Context(), &session{
			reqCtx: proxy,
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"backend1": {sessionID: "test-session"},
			},
			route: "test-route",
		}, rr, &jsonrpc.Request{ID: reqID, Method: "completion/complete"}, &mcp.CompleteParams{
			Ref: &mcp.CompleteReference{Type: "ref/prompt", Name: "backend2__my-prompt"},
		}, span)
	})
	require.ErrorIs(t, err, errSessionNotFound)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "no MCP session found for backend backend2")
	require.Empty(t, span.backends, "must not record a route-to-backend span for a backend with no session")
}

func TestMCPProxy_handlePing(t *testing.T) {
	reqID, _ := jsonrpc.MakeID("id")

	proxy := newTestMCPProxy()
	rr := httptest.NewRecorder()
	err := proxy.handlePing(t.Context(), rr, &jsonrpc.Request{ID: reqID, Method: "ping"})
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, rr.Code)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":"id","result":{}}`, rr.Body.String())
}

func TestMCPPRoxy_handleSetLoggingLevel(t *testing.T) {
	t.Run("backend with logging capability", func(t *testing.T) {
		var callCount atomic.Int32
		testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"id","result":{}}`))
		}))
		t.Cleanup(testServer.Close)

		reqID, _ := jsonrpc.MakeID("id")

		proxy := newTestMCPProxy()
		proxy.backendListenerAddr = testServer.URL
		rr := httptest.NewRecorder()
		s := &session{
			reqCtx: proxy,
			route:  "test-route",
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"backend1": {
					sessionID:    "test-session",
					capabilities: &mcp.ServerCapabilities{Logging: &mcp.LoggingCapabilities{}},
				},
			},
		}
		err := proxy.handleSetLoggingLevel(t.Context(), s, rr, &jsonrpc.Request{ID: reqID, Method: "logging/setLevel"}, &mcp.SetLoggingLevelParams{}, nil)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), `data: {"jsonrpc":"2.0","id":"id","result":{}}`)
		require.Equal(t, int32(1), callCount.Load(), "backend with logging capability should be called")
	})

	t.Run("backend without logging capability is not called", func(t *testing.T) {
		testServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("backend without logging capability should not be called")
		}))
		t.Cleanup(testServer.Close)

		reqID, _ := jsonrpc.MakeID("id")

		proxy := newTestMCPProxy()
		proxy.backendListenerAddr = testServer.URL
		rr := httptest.NewRecorder()
		s := &session{
			reqCtx: proxy,
			route:  "test-route",
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"backend1": {
					sessionID: "test-session-1",
					capabilities: &mcp.ServerCapabilities{
						Tools: &mcp.ToolCapabilities{ListChanged: true},
					},
				},
			},
		}
		err := proxy.handleSetLoggingLevel(t.Context(), s, rr, &jsonrpc.Request{ID: reqID, Method: "logging/setLevel"}, &mcp.SetLoggingLevelParams{}, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rr.Code)
	})
}

func TestMCPPRoxy_handleResourceReadRequest(t *testing.T) {
	t.Run("invalid resource name", func(t *testing.T) {
		proxy := newTestMCPProxy()
		rr := httptest.NewRecorder()
		_, err := proxy.handleResourceReadRequest(t.Context(), nil, rr,
			&jsonrpc.Request{Method: "resources/subscribe"}, &mcp.ReadResourceParams{
				URI: "invalid-form",
			},
		)
		require.ErrorContains(t, err, "invalid resource URI: invalid-form")
	})

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "backend1", r.Header.Get(internalapi.MCPBackendHeader))
		// The URI has already been rewritten from the client-facing composite form to the upstream one
		// by this point, so that is what the metadata header carries. Same as mcp_tool_name, which
		// records the tool name after the backend prefix is stripped.
		require.Equal(t, "file://foo-resource", r.Header.Get(internalapi.MCPMetadataHeaderResourceURI))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), `"uri":"file://foo-resource"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"id","result":{}}`))
	}))

	t.Cleanup(testServer.Close)

	reqID, _ := jsonrpc.MakeID("id")
	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = testServer.URL
	rr := httptest.NewRecorder()
	s := &session{
		reqCtx:             proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
		route:              "test-route",
	}
	_, err := proxy.handleResourceReadRequest(t.Context(), s, rr, &jsonrpc.Request{ID: reqID, Method: "resources/read"}, &mcp.ReadResourceParams{
		URI: downstreamResourceURI("file://foo-resource", "backend1"),
	})
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, rr.Code)
	var response struct {
		Result struct {
			Contents []*mcp.ResourceContents `json:"contents"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
	require.Empty(t, response.Result.Contents)

	t.Run("no session for known backend", func(t *testing.T) {
		// backend2 is configured on test-route (getBackendForRoute succeeds) but has no
		// session here, e.g. excluded by backendSelector. This should be 403 instead of
		// 400 or other error codes.
		rr := httptest.NewRecorder()
		s := &session{
			reqCtx:             proxy,
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
			route:              "test-route",
		}
		_, err := proxy.handleResourceReadRequest(t.Context(), s, rr, &jsonrpc.Request{ID: reqID, Method: "resources/read"}, &mcp.ReadResourceParams{
			URI: downstreamResourceURI("file://foo-resource", "backend2"),
		})
		require.ErrorIs(t, err, errSessionNotFound)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Contains(t, rr.Body.String(), "no MCP session found for backend backend2")
	})
}

func TestMCPProxy_maybeUpdateProgressTokenMetadata(t *testing.T) {
	proxy := newTestMCPProxy()
	metadata := mcp.Meta{}
	require.False(t, proxy.maybeUpdateProgressTokenMetadata(t.Context(), metadata, "backend"))
	metadata[progressTokenMetadataKey] = struct{}{}
	require.False(t, proxy.maybeUpdateProgressTokenMetadata(t.Context(), metadata, "backend"))
	metadata[progressTokenMetadataKey] = nil
	require.False(t, proxy.maybeUpdateProgressTokenMetadata(t.Context(), metadata, "backend"))

	metadata[progressTokenMetadataKey] = "abcd"
	require.True(t, proxy.maybeUpdateProgressTokenMetadata(t.Context(), metadata, "backend"))
	// Base64 encoded "abcd" is "YWJjZA==".
	require.Equal(t, "YWJjZA==__s__backend", metadata[progressTokenMetadataKey])

	metadata[progressTokenMetadataKey] = 1.1
	require.True(t, proxy.maybeUpdateProgressTokenMetadata(t.Context(), metadata, "backend"))
	require.Equal(t, "9a9999999999f13f__f__backend", metadata[progressTokenMetadataKey])

	metadata[progressTokenMetadataKey] = int64(1)
	require.True(t, proxy.maybeUpdateProgressTokenMetadata(t.Context(), metadata, "backend"))
	require.Equal(t, "1__i__backend", metadata[progressTokenMetadataKey])
}

func TestMCPProxy_handleClientToServerNotificationsProgress(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		inputProgressToken                any
		expResponseBody, expUpstreamToken string
	}{
		{
			name:               "invalid type",
			inputProgressToken: struct{}{},
			expResponseBody:    `invalid progressToken type struct {}`,
		},
		{
			name:               "invalid format",
			inputProgressToken: "@@@@@@@@@@@@@@",
			expResponseBody:    `invalid progressToken @@@@@@@@@@@@@@`,
		},
		{
			name:               "string type",
			inputProgressToken: "YWJjZA==__s__backend1", // base64 encoded "abcd".
			expUpstreamToken:   `"progressToken":"abcd"`,
		},
		{
			name:               "float64 type",
			inputProgressToken: "9a9999999999f13f__f__backend1",
			expUpstreamToken:   `"progressToken":1.1`,
		},
		{
			name:               "int64 type",
			inputProgressToken: "12345__i__backend1",
			expUpstreamToken:   `"progressToken":12345`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTestMCPProxy()
			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Contains(t, string(body), tc.expUpstreamToken)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"id","result":{}}`))
			}))
			t.Cleanup(testServer.Close)
			proxy.backendListenerAddr = testServer.URL

			rr := httptest.NewRecorder()
			s := &session{
				reqCtx:             proxy,
				perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
				route:              "test-route",
			}
			params := &mcp.ProgressNotificationParams{ProgressToken: tc.inputProgressToken}
			_, err := proxy.handleClientToServerNotificationsProgress(t.Context(), s, rr,
				&jsonrpc.Request{Method: "notifications/progress"}, params, nil)
			if rr.Code != http.StatusOK {
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
				t.Logf("Response body: %s", rr.Body.String())
				require.Contains(t, rr.Body.String(), tc.expResponseBody)
				return
			}
			require.NoError(t, err)
			require.Contains(t, rr.Body.String(), tc.expResponseBody)
		})
	}
}

// TestMCPProxy_handleClientToServerNotificationsProgress_NoSession covers a backend that is
// configured on the route (getBackendForRoute succeeds) but has no session in this particular
// session (e.g. excluded by backendSelector). Passing a non-nil span reproduces the case that
// used to panic on a nil *compositeSessionEntry before the session was ever checked for nil.
func TestMCPProxy_handleClientToServerNotificationsProgress_NoSession(t *testing.T) {
	proxy := newTestMCPProxy()
	rr := httptest.NewRecorder()
	s := &session{
		reqCtx:             proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
		route:              "test-route",
	}
	params := &mcp.ProgressNotificationParams{ProgressToken: "YWJjZA==__s__backend2"}
	span := &fakeSpan{}
	var err error
	require.NotPanics(t, func() {
		_, err = proxy.handleClientToServerNotificationsProgress(t.Context(), s, rr,
			&jsonrpc.Request{Method: "notifications/progress"}, params, span)
	})
	require.ErrorIs(t, err, errSessionNotFound)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "no MCP session found for backend backend2")
	require.Empty(t, span.backends, "must not record a route-to-backend span for a backend with no session")
}

func TestMCPProxy_maybeServerToClientRequestModify(t *testing.T) {
	strID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)
	f64ID, err := jsonrpc.MakeID(float64(1))
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		msg    *jsonrpc.Request
		expErr string
		verify func(t *testing.T, modified *jsonrpc.Request)
	}{
		{
			name:   "not server-to-client request",
			msg:    &jsonrpc.Request{Method: "ping"},
			verify: func(t *testing.T, modified *jsonrpc.Request) { require.Equal(t, "ping", modified.Method) },
		},
		{
			name:   "roots/list invalid param",
			msg:    &jsonrpc.Request{Method: "roots/list", Params: []byte(`fewfwaf`)},
			expErr: `failed to unmarshal roots/list params:`,
		},
		{
			name:   "roots/list no id",
			msg:    &jsonrpc.Request{Method: "roots/list", Params: []byte(`{"_meta": {"progressToken": 1345}}`)},
			expErr: `missing id in the server->client request`,
		},
		{
			name: "roots/list",
			msg:  &jsonrpc.Request{ID: strID, Method: "roots/list", Params: []byte(`{"_meta": {"progressToken": 1345}}`)},
			verify: func(t *testing.T, modified *jsonrpc.Request) {
				params := &mcp.ListRootsParams{}
				require.NoError(t, json.Unmarshal(modified.Params, params))
				// Check the progress token is updated.
				require.Equal(t, "0000000000049540__f__backend", params.Meta[progressTokenMetadataKey])
				// Then check the ID: aWQ= is the base64 encoded "id".
				require.Equal(t, "aWQ=__s__backend", modified.ID.Raw().(string))
			},
		},
		{
			name: "sampling/createMessage",
			msg:  &jsonrpc.Request{ID: f64ID, Method: "sampling/createMessage", Params: []byte(`{"_meta": {"progressToken": "pt"}}`)},
			verify: func(t *testing.T, modified *jsonrpc.Request) {
				params := &mcp.CreateMessageParams{}
				require.NoError(t, json.Unmarshal(modified.Params, params))
				// Check the progress token is updated: cHQ= is the base64 encoded "pt".
				require.Equal(t, "cHQ=__s__backend", params.Meta[progressTokenMetadataKey])
				// Then check the ID: 1 is encoded as 1__i__backend because of the roundtrip issue of the jsonrpc library in MCP SDK.
				// https://github.com/modelcontextprotocol/go-sdk/blob/5d64d61974982512270b554afd45d053c6dc2fb7/internal/jsonrpc2/messages.go#L32
				require.Equal(t, "1__i__backend", modified.ID.Raw().(string))
			},
		},
		{
			name: "elicitation/create",
			msg:  &jsonrpc.Request{ID: f64ID, Method: "elicitation/create", Params: []byte(`{"_meta": {"progressToken": "pt"}}`)},
			verify: func(t *testing.T, modified *jsonrpc.Request) {
				params := &mcp.CreateMessageParams{}
				require.NoError(t, json.Unmarshal(modified.Params, params))
				// Check the progress token is updated: cHQ= is the base64 encoded "pt".
				require.Equal(t, "cHQ=__s__backend", params.Meta[progressTokenMetadataKey])
				// Then check the ID: 1 is encoded as 1__i__backend because of the roundtrip issue of the jsonrpc library in MCP SDK.
				// https://github.com/modelcontextprotocol/go-sdk/blob/5d64d61974982512270b554afd45d053c6dc2fb7/internal/jsonrpc2/messages.go#L32
				require.Equal(t, "1__i__backend", modified.ID.Raw().(string))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTestMCPProxy()
			err := proxy.maybeServerToClientRequestModify(t.Context(), tc.msg, "backend")
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
			} else {
				require.NoError(t, err)
				tc.verify(t, tc.msg)
			}
		})
	}
}

func TestMCPProxy_handleClientToServerResponse(t *testing.T) {
	t.Run("invalid IDs", func(t *testing.T) {
		proxy := newTestMCPProxy()
		rr := httptest.NewRecorder()
		_, err := proxy.handleClientToServerResponse(t.Context(), nil, rr, &jsonrpc.Response{})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid response ID type: <nil>")

		invalidID, err := jsonrpc.MakeID("invalidformatid")
		require.NoError(t, err)
		rr = httptest.NewRecorder()
		_, err = proxy.handleClientToServerResponse(t.Context(), nil, rr, &jsonrpc.Response{ID: invalidID})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid response ID format: invalidformatid")

		invalidID2, err := jsonrpc.MakeID("__foo__")
		require.NoError(t, err)
		rr = httptest.NewRecorder()
		_, err = proxy.handleClientToServerResponse(t.Context(), nil, rr, &jsonrpc.Response{ID: invalidID2})
		require.ErrorContains(t, err, `invalid response ID type identifier: foo`)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), `invalid response ID type identifier`)

		invalidFloatID, err := jsonrpc.MakeID("00__f__backend1")
		require.NoError(t, err)
		rr = httptest.NewRecorder()
		require.NotPanics(t, func() {
			_, err = proxy.handleClientToServerResponse(t.Context(), nil, rr, &jsonrpc.Response{ID: invalidFloatID})
		})
		require.ErrorContains(t, err, "float64 ID requires 8 bytes, got 1")
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid response ID format")
	})

	unknownBackendID, err := jsonrpc.MakeID("aWQ=__s__unknownbackend") // aWQK is the base64 encoded "id".
	require.NoError(t, err)
	excludedBackendID, err := jsonrpc.MakeID("aWQ=__s__backend2") // backend2 is configured on test-route but has no session here.
	require.NoError(t, err)
	intID, err := jsonrpc.MakeID("1__i__backend1")
	require.NoError(t, err)
	strID, err := jsonrpc.MakeID("aWQ=__s__backend1") // aWQK is the base64 encoded "id".
	require.NoError(t, err)
	f64ID, err := jsonrpc.MakeID("9a9999999999f13f__f__backend1")
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		msg     *jsonrpc.Response
		expErr  string
		expCode int
		verify  func(t *testing.T, modified *jsonrpc.Response)
	}{
		{
			// Backend not configured on the route at all: unknown backend, 404.
			name:    "unknown backend",
			msg:     &jsonrpc.Response{ID: unknownBackendID},
			expErr:  `unknown backend unknownbackend`,
			expCode: http.StatusNotFound,
		},
		{
			// Backend configured on the route but excluded by backendSelector (no session):
			// authorization decision, 403.
			name:    "no session for known backend",
			msg:     &jsonrpc.Response{ID: excludedBackendID},
			expErr:  `no MCP session found for backend backend2`,
			expCode: http.StatusForbidden,
		},
		{
			name: "str id",
			msg:  &jsonrpc.Response{ID: strID},
			verify: func(t *testing.T, modified *jsonrpc.Response) {
				// Check the ID is decoded properly.
				require.Equal(t, "id", modified.ID.Raw().(string))
			},
		},
		{
			name: "int id",
			msg:  &jsonrpc.Response{ID: intID},
			verify: func(t *testing.T, modified *jsonrpc.Response) {
				// Check the ID is decoded properly.
				require.Equal(t, int64(1), modified.ID.Raw().(int64))
			},
		},
		{
			name: "float id",
			msg:  &jsonrpc.Response{ID: f64ID},
			verify: func(t *testing.T, modified *jsonrpc.Response) {
				// MCP SDK ignores the fraction part and converts to int64 during the roundtrip.
				// https://github.com/modelcontextprotocol/go-sdk/blob/5d64d61974982512270b554afd45d053c6dc2fb7/internal/jsonrpc2/messages.go#L32
				require.Equal(t, int64(1), modified.ID.Raw().(int64))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				// Parse the body as the jsonrpc message.
				reqRaw, err := jsonrpc.DecodeMessage(body)
				require.NoError(t, err)
				req, ok := reqRaw.(*jsonrpc.Response)
				require.True(t, ok)
				tc.verify(t, req)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"id","result":{}}`))
			}))
			t.Cleanup(testServer.Close)
			proxy := newTestMCPProxy()
			proxy.backendListenerAddr = testServer.URL

			rr := httptest.NewRecorder()
			_, err := proxy.handleClientToServerResponse(t.Context(), &session{
				reqCtx:             proxy,
				perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "test-session"}},
				route:              "test-route",
			}, rr, tc.msg)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				require.Equal(t, tc.expCode, rr.Code)
				require.Contains(t, rr.Body.String(), tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rr.Code)
			require.Contains(t, rr.Body.String(), `{"jsonrpc":"2.0","id":"id","result":{}}`)
		})
	}
}

func TestMCPServer_handleNotificationsRootsListChanged(t *testing.T) {
	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)

	proxy := newTestMCPProxy()
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(testServer.Close)

	proxy.backendListenerAddr = testServer.URL
	proxy.routes = map[filterapi.MCPRouteName]*mcpProxyConfigRoute{
		"some-route": {
			backends: map[filterapi.MCPBackendName]filterapi.MCPBackend{
				"test-backend": {Name: "test-backend"},
			},
		},
	}

	req := &jsonrpc.Request{ID: reqID, Method: "notifications/roots/list_changed", Params: emptyJSONRPCMessage}
	rr := httptest.NewRecorder()
	err = proxy.handleNotificationsRootsListChanged(t.Context(), &session{
		reqCtx:             proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"test-backend": {sessionID: ""}},
	}, rr, req, nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, rr.Code)
}

func TestMCPServer_handleResourcesSubscriptionRequest(t *testing.T) {
	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)

	for _, tc := range []struct {
		p    any
		name string
	}{
		{p: &mcp.SubscribeParams{URI: "backend1+file://foo"}, name: "resources/subscribe"},
		{p: &mcp.UnsubscribeParams{URI: "backend1+file://bar"}, name: "resources/unsubscribe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTestMCPProxy()
			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body []byte
				body, err = io.ReadAll(r.Body)
				require.NoError(t, err)
				var decoded jsonrpc.Message
				decoded, err = jsonrpc.DecodeMessage(body)
				require.NoError(t, err)
				req, ok := decoded.(*jsonrpc.Request)
				require.True(t, ok)
				require.Equal(t, tc.name, req.Method)
				switch tc.p.(type) {
				case *mcp.SubscribeParams:
					var params mcp.SubscribeParams
					require.NoError(t, json.Unmarshal(req.Params, &params))
					require.Equal(t, "file://foo", params.URI)
					require.Equal(t, "file://foo", r.Header.Get(internalapi.MCPMetadataHeaderResourceURI))
				case *mcp.UnsubscribeParams:
					var params mcp.UnsubscribeParams
					require.NoError(t, json.Unmarshal(req.Params, &params))
					require.Equal(t, "file://bar", params.URI)
					require.Equal(t, "file://bar", r.Header.Get(internalapi.MCPMetadataHeaderResourceURI))
				default:
					t.Fatalf("unexpected params type: %T", tc.p)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"id","result":{}}`))
			}))
			t.Cleanup(testServer.Close)

			proxy.backendListenerAddr = testServer.URL

			req := &jsonrpc.Request{ID: reqID, Method: tc.name, Params: emptyJSONRPCMessage}
			rr := httptest.NewRecorder()
			s := &session{
				reqCtx:             proxy,
				perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "a"}},
				route:              "test-route",
			}
			switch pp := tc.p.(type) {
			case *mcp.SubscribeParams:
				_, err = proxy.handleResourcesSubscribeRequest(t.Context(), s, rr, req, pp, nil)
			case *mcp.UnsubscribeParams:
				_, err = proxy.handleResourcesUnsubscribeRequest(t.Context(), s, rr, req, pp, nil)
			}
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rr.Code)
		})
	}

	t.Run("no session for known backend", func(t *testing.T) {
		// backend2 is configured on test-route (getBackendForRoute succeeds) but has no
		// session here, e.g. excluded by backendSelector. This should be 403 instead of
		// 400 or other error codes.
		proxy := newTestMCPProxy()
		rr := httptest.NewRecorder()
		s := &session{
			reqCtx:             proxy,
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"backend1": {sessionID: "a"}},
			route:              "test-route",
		}
		_, err := proxy.handleResourcesSubscribeRequest(t.Context(), s, rr,
			&jsonrpc.Request{ID: reqID, Method: "resources/subscribe"}, &mcp.SubscribeParams{URI: "backend2+file://foo"}, nil)
		require.ErrorIs(t, err, errSessionNotFound)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Contains(t, rr.Body.String(), "no MCP session found for backend backend2")
	})
}

func Test_sendToAllBackendsAndAggregateResponsesImpl(t *testing.T) {
	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)
	proxy := newTestMCPProxy()
	s := &session{perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"a": {sessionID: "session-a"}}}

	type testData struct {
		Value string `json:"value"`
	}
	events := make(chan *backendEvent)
	go func() {
		for _, msg := range []jsonrpc.Message{
			&jsonrpc.Response{ID: reqID, Result: []byte(`{"value": "foo"}`)},
			&jsonrpc.Request{Method: "notifications/roots/list_changed"},
			// Empty result should be ignored.
			&jsonrpc.Response{ID: reqID},
			&jsonrpc.Response{ID: reqID, Result: []byte(`{"value": "bar"}`)},
			// Invalid result should be logged and ignored, not blocking the response.
			&jsonrpc.Response{ID: reqID, Result: []byte(`invalidddddddddddddddddd`)},
			// Error should be logged and ignored, not blocking the response.
			&jsonrpc.Response{ID: reqID, Error: errors.New("some error")},
		} {
			events <- &backendEvent{sseEvent: &sseEvent{backend: "a", messages: []jsonrpc.Message{msg}}}
		}
		close(events)
	}()

	rr := httptest.NewRecorder()
	var testParams *mcp.ListToolsParams
	err = sendToAllBackendsAndAggregateResponsesImpl(t.Context(), events, proxy, rr, s, &jsonrpc.Request{ID: reqID, Method: "test"},
		testParams,
		func(_ *session, res []broadCastResponse[testData]) testData {
			var combined testData
			for _, r := range res {
				combined.Value += r.res.Value
			}
			return combined
		},
		nil,
	)
	require.ErrorIs(t, err, errBackendResponseError)
	require.Equal(t, http.StatusOK, rr.Code)
	// The response is the SSE stream containing the aggregated result.
	require.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
	require.Contains(t, rr.Body.String(), `{"jsonrpc":"2.0","id":"id","result":{"value":"foobar"}}`)
}

func Test_sendToAllBackendsAndAggregateResponsesImpl_RecordsSpan(t *testing.T) {
	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)
	proxy := newTestMCPProxy()
	s := &session{perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{"a": {sessionID: "session-a"}}}

	type testData struct {
		Value string `json:"value"`
	}
	events := make(chan *backendEvent)
	go func() {
		events <- &backendEvent{sseEvent: &sseEvent{backend: "a", messages: []jsonrpc.Message{
			&jsonrpc.Response{ID: reqID, Result: []byte(`{"value": "foo"}`)},
		}}}
		close(events)
	}()

	span := &fakeSpan{}
	rr := httptest.NewRecorder()
	var testParams *mcp.ListToolsParams
	err = sendToAllBackendsAndAggregateResponsesImpl(t.Context(), events, proxy, rr, s, &jsonrpc.Request{ID: reqID, Method: "tools/list"},
		testParams,
		func(_ *session, res []broadCastResponse[testData]) testData {
			var combined testData
			for _, r := range res {
				combined.Value += r.res.Value
			}
			return combined
		},
		span,
	)
	require.NoError(t, err)
	// The aggregation is bracketed with begin/end events for the span timeline.
	require.Equal(t, []string{"tools/list aggregation begin", "tools/list aggregation end"}, span.events)
	// The merged result is handed to the span exactly once.
	require.Len(t, span.listResults, 1)
	require.Equal(t, testData{Value: "foo"}, span.listResults[0])
}

func Test_recordToolCallResult(t *testing.T) {
	result := []byte(`{"content":[]}`)

	t.Run("records for tools/call with result", func(t *testing.T) {
		span := &fakeSpan{}
		recordToolCallResult(span, &jsonrpc.Request{Method: "tools/call"}, &jsonrpc.Response{Result: result})
		require.Equal(t, result, span.toolCallResult)
	})

	t.Run("skips non tools/call method", func(t *testing.T) {
		span := &fakeSpan{}
		recordToolCallResult(span, &jsonrpc.Request{Method: "prompts/get"}, &jsonrpc.Response{Result: result})
		require.Nil(t, span.toolCallResult)
	})

	t.Run("skips nil result", func(t *testing.T) {
		span := &fakeSpan{}
		recordToolCallResult(span, &jsonrpc.Request{Method: "tools/call"}, &jsonrpc.Response{})
		require.Nil(t, span.toolCallResult)
	})

	t.Run("no panic on nil span or nil request", func(t *testing.T) {
		require.NotPanics(t, func() {
			recordToolCallResult(nil, &jsonrpc.Request{Method: "tools/call"}, &jsonrpc.Response{Result: result})
			recordToolCallResult(&fakeSpan{}, nil, &jsonrpc.Response{Result: result})
		})
	})
}

func Test_parseParamsAndMaybeStartSpan(t *testing.T) {
	params := &mcp.GetPromptParams{Name: "somebackend__test-prompt"}
	paramsData, err := json.Marshal(params)
	require.NoError(t, err)
	req := &jsonrpc.Request{
		Method: "prompts/get",
		Params: paramsData,
	}
	p := &mcp.GetPromptParams{}
	m := newTestMCPProxy()
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	trace, err := tracing.NewTracingFromEnv(t.Context(), t.Output(), nil)
	require.NoError(t, err)
	m.tracer = trace.MCPTracer()
	s, err := parseParamsAndMaybeStartSpan(t.Context(), m, req, p, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
	// Make sure that traceparent is not empty, that's span started.
	require.NotEmpty(t, p.GetMeta()["traceparent"])
}

func Test_parseParamsAndMaybeStartSpan_NilParam(t *testing.T) {
	req := &jsonrpc.Request{
		Method: "prompts/get",
	}
	p := &mcp.GetPromptParams{}
	m := newTestMCPProxy()
	s, err := parseParamsAndMaybeStartSpan(t.Context(), m, req, p, nil)
	require.NoError(t, err)
	require.Nil(t, s)
}

func Test_errorType(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected metrics.MCPErrorType
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "jsonrpc invalid params error",
			err:      &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params"},
			expected: metrics.MCPErrorInvalidParam,
		},
		{
			name:     "jsonrpc method not found error",
			err:      &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"},
			expected: metrics.MCPErrorUnsupportedMethod,
		},
		{
			name:     "jsonrpc invalid request error",
			err:      &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "invalid request"},
			expected: metrics.MCPErrorInvalidJSONRPC,
		},
		{
			name:     "jsonrpc parse error",
			err:      &jsonrpc.Error{Code: jsonrpc.CodeParseError, Message: "parse error"},
			expected: metrics.MCPErrorInvalidJSONRPC,
		},
		{
			name:     "jsonrpc internal error",
			err:      &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal error"},
			expected: metrics.MCPErrorInternal,
		},
		{
			name:     "jsonrpc unknown error code",
			err:      &jsonrpc.Error{Code: -32000, Message: "custom error"},
			expected: metrics.MCPErrorInternal,
		},
		{
			name:     "backend not found error",
			err:      errBackendNotFound,
			expected: metrics.MCPErrorInvalidParam,
		},
		{
			name:     "session not found error",
			err:      errSessionNotFound,
			expected: metrics.MCPErrorInvalidParam,
		},
		{
			name:     "invalid tool name error",
			err:      errInvalidToolName,
			expected: metrics.MCPErrorInvalidParam,
		},
		{
			name:     "wrapped backend not found error",
			err:      fmt.Errorf("failed to call backend: %w", errBackendNotFound),
			expected: metrics.MCPErrorInvalidParam,
		},
		{
			name:     "generic error",
			err:      errors.New("some generic error"),
			expected: metrics.MCPErrorInternal,
		},
		{
			name: "joined errors with non-internal error",
			err: errors.Join(
				errors.New("error 1"),
				errBackendNotFound,
				errors.New("error 3"),
			),
			expected: metrics.MCPErrorInvalidParam,
		},
		{
			name: "joined errors all internal",
			err: errors.Join(
				errors.New("error 1"),
				errors.New("error 2"),
			),
			expected: metrics.MCPErrorInternal,
		},
		{
			name: "joined errors with jsonrpc error",
			err: errors.Join(
				errors.New("error 1"),
				&jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"},
			),
			expected: metrics.MCPErrorUnsupportedMethod,
		},
		{
			name:     "tool call error",
			err:      &errToolCall{toolName: "test-tool", backend: "backend1", err: errors.New("tool failed")},
			expected: metrics.MCPErrorInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := errorType(tt.err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func Test_checkToolCallError(t *testing.T) {
	tests := []struct {
		name        string
		req         *jsonrpc.Request
		msg         *jsonrpc.Response
		backendName string
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil request",
			req:         nil,
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true}`)},
			backendName: "backend1",
			wantErr:     false,
		},
		{
			name:        "not a tools/call request",
			req:         &jsonrpc.Request{Method: "prompts/list"},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true}`)},
			backendName: "backend1",
			wantErr:     false,
		},
		{
			name:        "nil result",
			req:         &jsonrpc.Request{Method: "tools/call"},
			msg:         &jsonrpc.Response{Result: nil},
			backendName: "backend1",
			wantErr:     false,
		},
		{
			name:        "isError is false",
			req:         &jsonrpc.Request{Method: "tools/call"},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": false, "content": []}`)},
			backendName: "backend1",
			wantErr:     false,
		},
		{
			name:        "isError true with no content",
			req:         &jsonrpc.Request{Method: "tools/call", Params: []byte(`{"name": "test-tool"}`)},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true, "content": []}`)},
			backendName: "backend1",
			wantErr:     true,
			errContains: "tool returned isError=true",
		},
		{
			name:        "isError true with text content",
			req:         &jsonrpc.Request{Method: "tools/call", Params: []byte(`{"name": "test-tool"}`)},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true, "content": [{"type": "text", "text": "missing required parameter: owner"}]}`)},
			backendName: "backend1",
			wantErr:     true,
			errContains: "missing required parameter: owner",
		},
		{
			name:        "isError true with multiple text contents",
			req:         &jsonrpc.Request{Method: "tools/call", Params: []byte(`{"name": "test-tool"}`)},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true, "content": [{"type": "text", "text": "error 1"}, {"type": "text", "text": "error 2"}]}`)},
			backendName: "backend1",
			wantErr:     true,
			errContains: "error 1; error 2",
		},
		{
			name:        "isError true with mixed content types",
			req:         &jsonrpc.Request{Method: "tools/call", Params: []byte(`{"name": "test-tool"}`)},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true, "content": [{"type": "text", "text": "Error occurred"}, {"type": "text", "text": "Additional info"}]}`)},
			backendName: "backend1",
			wantErr:     true,
			errContains: "Error occurred; Additional info",
		},
		{
			name:        "isError true without tool name in params",
			req:         &jsonrpc.Request{Method: "tools/call", Params: []byte(`{}`)},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true, "content": [{"type": "text", "text": "tool error"}]}`)},
			backendName: "backend1",
			wantErr:     true,
			errContains: "tool error",
		},
		{
			name:        "isError true with invalid params",
			req:         &jsonrpc.Request{Method: "tools/call", Params: []byte(`invalid json`)},
			msg:         &jsonrpc.Response{Result: []byte(`{"isError": true, "content": [{"type": "text", "text": "tool error"}]}`)},
			backendName: "backend1",
			wantErr:     true,
			errContains: "tool error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolErr := checkToolCallError(tt.req, tt.msg, tt.backendName)
			if tt.wantErr {
				require.NotNil(t, toolErr)
				require.Contains(t, toolErr.Error(), tt.errContains)
				require.Equal(t, tt.backendName, toolErr.backend)
			} else {
				require.Nil(t, toolErr)
			}
		})
	}
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
func TestServePOST_InitializeRequest_ForwardsExtensions(t *testing.T) {
	const initializeWithExtensions = `{
"jsonrpc": "2.0",
"id": 1,
"result": {
"protocolVersion": "2025-06-18",
"capabilities": {
"tools": {"listChanged": true},
"extensions": {"io.modelcontextprotocol/ui": {}}
},
"serverInfo": {"name": "ui-backend", "version": "1.0.0"}
}
}`

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionIDHeader) == "" {
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(initializeWithExtensions))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(testServer.Close)

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = testServer.URL

	id, err := jsonrpc.MakeID("test-1")
	require.NoError(t, err)
	initReq := &jsonrpc.Request{Method: "initialize", ID: id, Params: []byte(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}`)}
	body, err := jsonrpc.EncodeMessage(initReq)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalapi.MCPRouteHeader, "test-route")
	rr := httptest.NewRecorder()

	proxy.servePOST(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Result struct {
			Capabilities struct {
				Extensions map[string]any `json:"extensions"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Contains(t, resp.Result.Capabilities.Extensions, "io.modelcontextprotocol/ui")
}
