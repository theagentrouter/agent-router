// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

var (
	_ tracingapi.MCPSpan   = (*fakeSpan)(nil)
	_ tracingapi.MCPTracer = (*fakeTracer)(nil)
)

type fakeSpan struct {
	backends        []string
	clientSessionID string
	events          []string
	listResults     []any
	toolCallResult  []byte
	errType         string
	err             error
}

func (f *fakeSpan) RecordRouteToBackend(backend string, _ string, _ bool) {
	f.backends = append(f.backends, backend)
}

func (f *fakeSpan) RecordClientSession(sessionID string) {
	f.clientSessionID = sessionID
}

func (f *fakeSpan) AddEvent(name string) {
	f.events = append(f.events, name)
}

func (f *fakeSpan) RecordListResult(result any) {
	f.listResults = append(f.listResults, result)
}

func (f *fakeSpan) RecordToolCallResult(resultJSON []byte) {
	f.toolCallResult = resultJSON
}

func (f *fakeSpan) EndSpan() {}

func (f *fakeSpan) EndSpanOnError(errType string, err error) {
	f.errType = errType
	f.err = err
}

type fakeTracer struct {
	span *fakeSpan
}

func (f *fakeTracer) StartSpanAndInjectMeta(context.Context, *jsonrpc.Request, mcp.Params, http.Header) tracingapi.MCPSpan {
	if f.span == nil {
		f.span = &fakeSpan{}
	}
	return f.span
}

var noopTracer = tracingapi.NoopMCPTracer{}

func TestGetMaxRequestBodySize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		envValue string
		want     int64
	}{
		{name: "default when env var not set", envValue: "", want: defaultMaxRequestBodySize},
		{name: "custom value from env var", envValue: "1048576", want: 1048576},
		{name: "default when env var is not a number", envValue: "not-a-number", want: defaultMaxRequestBodySize},
		{name: "default when env var is zero", envValue: "0", want: defaultMaxRequestBodySize},
		{name: "default when env var is negative", envValue: "-1", want: defaultMaxRequestBodySize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_PROXY_MAX_REQUEST_BODY_SIZE", tc.envValue)
			require.Equal(t, tc.want, getMaxRequestBodySize())
		})
	}
}

func TestNewMCPProxy(t *testing.T) {
	l := slog.Default()
	proxy, mux, err := NewMCPProxy(l, stubMetrics{}, noopTracer, NewPBKDF2AesGcmSessionCrypto("test", 100), nil)

	require.NoError(t, err)
	require.NotNil(t, proxy)
	require.NotNil(t, mux)
}

func TestMCPProxy_HTTPMethods(t *testing.T) {
	l := slog.Default()
	_, mux, err := NewMCPProxy(l, stubMetrics{}, noopTracer, NewPBKDF2AesGcmSessionCrypto("test", 100), nil)
	require.NoError(t, err)

	// Test unsupported method.
	req := httptest.NewRequest(http.MethodPatch, "/mcp", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	require.Contains(t, rr.Body.String(), "method not allowed")
}

func Test_applyLogHeaderMappings(t *testing.T) {
	logAttrs := map[string]string{"agent-session-id": "session.id"}
	proxyCfg := &ProxyConfig{logRequestHeaderAttributes: logAttrs}

	t.Run("meta only", func(t *testing.T) {
		reqCtx := &mcpRequestContext{ProxyConfig: proxyCfg}
		req, err := http.NewRequest(http.MethodPost, "http://example", nil)
		require.NoError(t, err)

		id, err := jsonrpc.MakeID("1")
		require.NoError(t, err)
		msg := &jsonrpc.Request{
			ID:     id,
			Method: "tools/call",
			Params: []byte(`{"_meta":{"agent-session-id":"meta-session"}}`),
		}

		reqCtx.applyLogHeaderMappings(req, msg)
		require.Equal(t, "meta-session", req.Header.Get("agent-session-id"))
	})

	t.Run("header fallback", func(t *testing.T) {
		reqCtx := &mcpRequestContext{
			ProxyConfig:    proxyCfg,
			requestHeaders: http.Header{"Agent-Session-Id": []string{"header-session"}},
		}
		req, err := http.NewRequest(http.MethodPost, "http://example", nil)
		require.NoError(t, err)

		id, err := jsonrpc.MakeID("2")
		require.NoError(t, err)
		msg := &jsonrpc.Request{
			ID:     id,
			Method: "tools/call",
			Params: []byte(`{"_meta":{"other":"x"}}`),
		}

		reqCtx.applyLogHeaderMappings(req, msg)
		require.Equal(t, "header-session", req.Header.Get("agent-session-id"))
	})
}

func Test_originalPathForRequest(t *testing.T) {
	t.Run("request uri preferred", func(t *testing.T) {
		req := &http.Request{RequestURI: "/mcp?x=1"}
		require.Equal(t, "/mcp?x=1", originalPathForRequest(req))
	})

	t.Run("url request uri", func(t *testing.T) {
		req := &http.Request{URL: mustParseURL(t, "http://example/mcp?x=1")}
		require.Equal(t, "/mcp?x=1", originalPathForRequest(req))
	})

	t.Run("url path only", func(t *testing.T) {
		req := &http.Request{URL: mustParseURL(t, "http://example/mcp")}
		require.Equal(t, "/mcp", originalPathForRequest(req))
	})
}

func Test_applyOriginalPathHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example/mcp", nil)
	require.NoError(t, err)

	reqCtx := &mcpRequestContext{originalPath: "/mcp?x=1"}
	reqCtx.applyOriginalPathHeaders(req)
	require.Equal(t, "/mcp?x=1", req.Header.Get(internalapi.OriginalPathHeader))
	require.Equal(t, "/mcp?x=1", req.Header.Get(internalapi.EnvoyOriginalPathHeader))

	t.Run("does not override", func(t *testing.T) {
		req2, err := http.NewRequest(http.MethodGet, "http://example/mcp", nil)
		require.NoError(t, err)
		req2.Header.Set(internalapi.OriginalPathHeader, "/already")
		req2.Header.Set(internalapi.EnvoyOriginalPathHeader, "/envoy-already")
		reqCtx.applyOriginalPathHeaders(req2)
		require.Equal(t, "/already", req2.Header.Get(internalapi.OriginalPathHeader))
		require.Equal(t, "/envoy-already", req2.Header.Get(internalapi.EnvoyOriginalPathHeader))
	})
}

func Test_extractMetaFromJSONRPCMessage(t *testing.T) {
	t.Run("non request", func(t *testing.T) {
		id, err := jsonrpc.MakeID("1")
		require.NoError(t, err)
		resp := &jsonrpc.Response{ID: id, Result: []byte(`{}`)}
		require.Nil(t, extractMetaFromJSONRPCMessage(resp))
	})

	t.Run("no meta", func(t *testing.T) {
		id, err := jsonrpc.MakeID("1")
		require.NoError(t, err)
		req := &jsonrpc.Request{ID: id, Method: "tools/call", Params: []byte(`{"x":1}`)}
		require.Nil(t, extractMetaFromJSONRPCMessage(req))
	})

	t.Run("meta present", func(t *testing.T) {
		id, err := jsonrpc.MakeID("1")
		require.NoError(t, err)
		req := &jsonrpc.Request{ID: id, Method: "tools/call", Params: []byte(`{"_meta":{"agent-session-id":"s1"}}`)}
		meta := extractMetaFromJSONRPCMessage(req)
		require.Equal(t, "s1", meta["agent-session-id"])
	})
}

const (
	validInitializeResponse = `{
"jsonrpc": "2.0",
"id": 1,
"result": {
"protocolVersion": "2025-06-18",
"capabilities": {
"logging": {},
"prompts": {
"listChanged": true
},
"resources": {
"subscribe": true,
"listChanged": true
},
"tools": {
"listChanged": true
}
},
"serverInfo": {
"name": "ExampleServer",
"title": "Example Server Display Name",
"version": "1.0.0"
},
"instructions": "Optional instructions for the client"
}
}`
)

type perBackendCallCount struct {
	mu    sync.Mutex
	count map[string]int
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func (p *perBackendCallCount) inc(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.count == nil {
		p.count = make(map[string]int)
	}
	p.count[key]++
	return p.count[key]
}

func (p *perBackendCallCount) get(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count[key]
}

func TestNewSession_Success(t *testing.T) {
	// Mock backend server that responds to initialization.
	var callCount perBackendCallCount
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := r.Header.Get(internalapi.MCPBackendHeader)
		if callCount.inc(backend)%2 == 1 {
			// Initialize requests.
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(validInitializeResponse))
		} else {
			// notifications/initialized requests.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	s, err := proxy.newSession(t.Context(), &mcp.InitializeParams{}, "test-route", "", nil, time.Now())

	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotEmpty(t, s.clientGatewaySessionID())
}

func TestNewSession_NoBackend(t *testing.T) {
	proxy := newTestMCPProxy()

	s, err := proxy.newSession(t.Context(), &mcp.InitializeParams{}, "test-route", "", nil, time.Now())
	require.ErrorContains(t, err, `failed to create MCP session to any backend`)
	require.Nil(t, s)
}

func TestNewSession_SSE(t *testing.T) {
	// Mock backend server that responds to initialization.
	var callCount perBackendCallCount
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := r.Header.Get(internalapi.MCPBackendHeader)
		if callCount.inc(backend)%2 == 1 {
			// Odd calls: initialize requests.
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`event: message
id: Z4WAGVSUUFAJCOUNHNPZWRHCEU_0
data: {"jsonrpc":"2.0","id":"ff3964c5-4c79-4567-96e2-29e905754e58","result":{"capabilities":{"logging":{},"tools":{"listChanged":true}},"protocolVersion":"2025-06-18","serverInfo":{"name":"dumb-echo-server","version":"0.1.0"}}}

`))
		} else {
			// Even calls: notifications/initialized requests.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	s, err := proxy.newSession(t.Context(), &mcp.InitializeParams{}, "test-route", "", nil, time.Now())

	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotEmpty(t, s.clientGatewaySessionID())
}

func TestSessionFromID_ValidID(t *testing.T) {
	proxy := newTestMCPProxy()

	// Create a valid session ID.
	sessionID := secureID(t, proxy, "@@backend1:"+base64.StdEncoding.EncodeToString([]byte("test-session")))
	eventID := secureID(t, proxy, "@@backend1:"+base64.StdEncoding.EncodeToString([]byte("_1")))
	session, err := proxy.sessionFromID(secureClientToGatewaySessionID(sessionID), secureClientToGatewayEventID(eventID))

	require.NoError(t, err)
	require.NotNil(t, session)
	require.Equal(t, secureClientToGatewaySessionID(sessionID), session.clientGatewaySessionID())
}

func TestSessionFromID_InvalidID(t *testing.T) {
	proxy := newTestMCPProxy()

	// Create an invalid session ID.
	sessionID := secureID(t, proxy, "invalid-session-id")
	s, err := proxy.sessionFromID(secureClientToGatewaySessionID(sessionID), "")

	require.Error(t, err)
	require.Nil(t, s)
}

func TestInitializeSession_Success(t *testing.T) {
	// Mock backend server.
	var callCount perBackendCallCount
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := r.Header.Get(internalapi.MCPBackendHeader)
		if callCount.inc(backend) == 1 {
			// First call: initialize.
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(validInitializeResponse))
		} else {
			// Second call: notifications/initialized.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	res, err := proxy.initializeSession(t.Context(), "route1", filterapi.MCPBackend{Name: "test-backend"}, &mcp.InitializeParams{}, time.Now())

	require.NoError(t, err)
	require.Equal(t, gatewayToMCPServerSessionID("test-session-123"), res.sessionID)
	require.Equal(t, 2, callCount.get("test-backend"))
}

func TestInitializeSession_InitializeFailure(t *testing.T) {
	// Mock backend server that fails initialization.
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("initialization failed"))
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	sessionID, err := proxy.initializeSession(t.Context(), "route1", filterapi.MCPBackend{Name: "test-backend"}, &mcp.InitializeParams{}, time.Now())

	require.Error(t, err)
	require.Empty(t, sessionID)
	require.Contains(t, err.Error(), "failed with status code")
}

func TestInitializeSession_SSEEndsBeforeResponse(t *testing.T) {
	// Backend returns a 200 text/event-stream whose initialize response contains
	// only non-response events (a keep-alive) and then closes before ever sending
	// the JSON-RPC response. This must produce a clear error rather than the
	// misleading "MCP message is not a response: <nil>".
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(sessionIDHeader, "test-session-123")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`id: keepalive_0000
data:

`))
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	sessionID, err := proxy.initializeSession(t.Context(), "route1", filterapi.MCPBackend{Name: "test-backend"}, &mcp.InitializeParams{}, time.Now())

	require.Error(t, err)
	require.Empty(t, sessionID)
	require.Contains(t, err.Error(), "ended before a JSON-RPC response was received")
	require.NotContains(t, err.Error(), "is not a response")
}

func TestInitializeSession_NotificationsInitializedFailure(t *testing.T) {
	// Mock backend server.
	var callCount perBackendCallCount
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := r.Header.Get(internalapi.MCPBackendHeader)
		if callCount.inc(backend) == 1 {
			// First call: initialize - success.
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(validInitializeResponse))
		} else {
			// Second call: notifications/initialized - failure.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("notifications/initialized failed"))
		}
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	sessionID, err := proxy.initializeSession(t.Context(), "route1", filterapi.MCPBackend{Name: "test-backend"}, &mcp.InitializeParams{}, time.Now())

	require.Error(t, err)
	require.Empty(t, sessionID)
	require.Contains(t, err.Error(), "notifications/initialized request failed")
}

func TestInvokeJSONRPCRequest_Success(t *testing.T) {
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/", r.URL.Path)
		require.Equal(t, "test-backend", r.Header.Get("x-ai-eg-mcp-backend"))
		require.Equal(t, "test-session", r.Header.Get(sessionIDHeader))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": "success"}`))
	}))
	defer backendServer.Close()

	m := newTestMCPProxy()
	m.backendListenerAddr = backendServer.URL
	resp, err := m.invokeJSONRPCRequest(t.Context(), "route1", filterapi.MCPBackend{Name: "test-backend"}, &compositeSessionEntry{
		sessionID: "test-session",
	}, &jsonrpc.Request{}, nil)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

func TestInvokeJSONRPCRequest_NoSessionID(t *testing.T) {
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/", r.URL.Path)
		require.Equal(t, "test-backend", r.Header.Get("x-ai-eg-mcp-backend"))
		require.Empty(t, r.Header.Get(sessionIDHeader))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": "success"}`))
	}))
	defer backendServer.Close()

	m := newTestMCPProxy()
	m.backendListenerAddr = backendServer.URL
	resp, err := m.invokeJSONRPCRequest(t.Context(), "route1", filterapi.MCPBackend{Name: "test-backend"}, &compositeSessionEntry{
		sessionID: "",
	}, &jsonrpc.Request{}, nil)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

// Issue #2219: when a backend's initialize SSE response starts with a non-response
// event (an empty/keep-alive data line) before the real JSON-RPC response, session
// creation must still succeed rather than failing with "MCP message is not a response".
func TestNewSession_SSEWithLeadingKeepAlive(t *testing.T) {
	var callCount perBackendCallCount
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := r.Header.Get(internalapi.MCPBackendHeader)
		if callCount.inc(backend)%2 == 1 {
			// Odd calls: initialize requests. Emit a leading empty keep-alive
			// event, then the real response event.
			w.Header().Set(sessionIDHeader, "test-session-123")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`id: keepalive_0000
data:

event: message
id: msg_0001
data: {"jsonrpc":"2.0","id":"ff3964c5-4c79-4567-96e2-29e905754e58","result":{"capabilities":{"logging":{},"tools":{"listChanged":true}},"protocolVersion":"2025-06-18","serverInfo":{"name":"dumb-echo-server","version":"0.1.0"}}}

`))
		} else {
			// Even calls: notifications/initialized requests.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer backendServer.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = backendServer.URL

	s, err := proxy.newSession(t.Context(), &mcp.InitializeParams{}, "test-route", "", nil, time.Now())

	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotEmpty(t, s.clientGatewaySessionID())
}
