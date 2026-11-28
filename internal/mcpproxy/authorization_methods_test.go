// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// recordingMCPBackend is a fake backend listener that answers every JSON-RPC request with a
// method-appropriate result and records which (backend, method) pairs reached it.
type recordingMCPBackend struct {
	mu    sync.Mutex
	calls []string // "<backend> <method>"
}

func (b *recordingMCPBackend) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		msg, err := jsonrpc.DecodeMessage(body)
		require.NoError(t, err)
		req, ok := msg.(*jsonrpc.Request)
		require.True(t, ok)
		backend := r.Header.Get(internalapi.MCPBackendHeader)

		b.mu.Lock()
		b.calls = append(b.calls, backend+" "+req.Method)
		b.mu.Unlock()

		if !req.ID.IsValid() { // Notification.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "tools/list":
			result = mcp.ListToolsResult{Tools: []*mcp.Tool{
				{Name: "allowed_tool", InputSchema: map[string]any{"type": "object"}},
				{Name: "other_tool", InputSchema: map[string]any{"type": "object"}},
			}}
		case "tools/call":
			result = mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}
		case "resources/read":
			result = mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "file:///secret", Text: "SECRET-RESOURCE"}}}
		case "resources/list":
			result = mcp.ListResourcesResult{Resources: []*mcp.Resource{{Name: "secret", URI: "file:///secret"}}}
		case "resources/templates/list":
			result = mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{{Name: "tpl", URITemplate: "file:///{x}"}}}
		case "prompts/get":
			result = mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "SECRET-PROMPT"}}}}
		case "prompts/list":
			result = mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "secret_prompt"}}}
		case "completion/complete":
			result = mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{"SECRET-COMPLETION"}}}
		default:
			result = struct{}{}
		}
		encodedResult, err := json.Marshal(result)
		require.NoError(t, err)
		resp, err := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: req.ID, Result: encodedResult})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	})
}

func (b *recordingMCPBackend) reset() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	calls := b.calls
	b.calls = nil
	return calls
}

// newAuthorizationTestProxy returns a proxy whose "test-route" (backends backend1 and backend2)
// carries the given authorization policy, a fake backend listener, and a session ID for a
// session on both backends.
func newAuthorizationTestProxy(t *testing.T, auth *filterapi.MCPRouteAuthorization) (*mcpRequestContext, *recordingMCPBackend, string) {
	t.Helper()
	backend := &recordingMCPBackend{}
	server := httptest.NewServer(backend.handler(t))
	t.Cleanup(server.Close)

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	route := proxy.routes["test-route"]
	route.toolSelectors = nil
	route.authorization = mustCompileAuthorization(t, auth)

	sid := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	sessionID := secureID(t, proxy, fmt.Sprintf("test-route@@backend1:%s,backend2:%s", sid("s1"), sid("s2")))
	return proxy, backend, sessionID
}

// doMCPPost sends a JSON-RPC request (or a notification when id is empty) through servePOST,
// with a fresh request context as the production mux does for every HTTP request.
func doMCPPost(t *testing.T, base *mcpRequestContext, sessionID, method string, params any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	encodedParams, err := json.Marshal(params)
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("1")
	require.NoError(t, err)
	req := &jsonrpc.Request{ID: id, Method: method, Params: encodedParams}
	if method == "notifications/roots/list_changed" {
		req.ID = jsonrpc.ID{}
	}
	body, err := jsonrpc.EncodeMessage(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	httpReq.Header.Set(sessionIDHeader, sessionID)
	httpReq.Header.Set(internalapi.MCPRouteHeader, "test-route")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	proxy := &mcpRequestContext{ProxyConfig: base.ProxyConfig, metrics: base.metrics, requestHeaders: httpReq.Header}
	proxy.servePOST(rr, httpReq)
	return rr
}

// allMethodRequests lists one request per MCP method that is forwarded to backends, addressed
// to backend1 where the method targets a single backend.
var allMethodRequests = []struct {
	method string
	params any
}{
	{"resources/read", &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend1")}},
	{"resources/subscribe", &mcp.SubscribeParams{URI: downstreamResourceURI("file:///secret", "backend1")}},
	{"resources/unsubscribe", &mcp.UnsubscribeParams{URI: downstreamResourceURI("file:///secret", "backend1")}},
	{"prompts/get", &mcp.GetPromptParams{Name: "backend1__secret_prompt"}},
	{"completion/complete", &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/prompt", Name: "backend1__secret_prompt"}, Argument: mcp.CompleteParamsArgument{Name: "a", Value: "b"}}},
	{"completion/complete", &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/resource", URI: downstreamResourceURI("file:///{x}", "backend1")}, Argument: mcp.CompleteParamsArgument{Name: "x", Value: "y"}}},
	{"resources/list", &mcp.ListResourcesParams{}},
	{"resources/templates/list", &mcp.ListResourceTemplatesParams{}},
	{"prompts/list", &mcp.ListPromptsParams{}},
	{"logging/setLevel", &mcp.SetLoggingLevelParams{Level: "debug"}},
	{"notifications/roots/list_changed", &mcp.RootsListChangedParams{}},
}

// TestAuthorization_DefaultDenyAppliesToNonToolMethods verifies that a route
// with defaultAction Deny and a single tools/call allow rule must not forward any other MCP method
// to its backends.
func TestAuthorization_DefaultDenyAppliesToNonToolMethods(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		Rules: []filterapi.MCPRouteAuthorizationRule{{
			Action: filterapi.AuthorizationActionAllow,
			Target: &filterapi.MCPAuthorizationTarget{Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "allowed_tool"}}},
		}},
	})

	t.Run("allowlisted tool is allowed", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "tools/call", &mcp.CallToolParams{Name: "backend1__allowed_tool"}, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, []string{"backend1 tools/call"}, backend.reset())
	})
	t.Run("tools/list shows only the allowlisted tool", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "tools/list", &mcp.ListToolsParams{}, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.ElementsMatch(t, []string{"backend1 tools/list", "backend2 tools/list"}, backend.reset())
		require.Contains(t, rr.Body.String(), "backend1__allowed_tool")
		require.NotContains(t, rr.Body.String(), "other_tool")
	})
	t.Run("other tool is denied", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "tools/call", &mcp.CallToolParams{Name: "backend1__other_tool"}, nil)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Empty(t, backend.reset())
	})

	for _, tc := range allMethodRequests {
		t.Run(tc.method, func(t *testing.T) {
			rr := doMCPPost(t, proxy, sessionID, tc.method, tc.params, nil)
			require.Empty(t, backend.reset(), "denied method must not reach any backend")
			body := rr.Body.String()
			require.NotContains(t, body, "SECRET")
			switch tc.method {
			case "resources/list", "resources/templates/list", "prompts/list", "logging/setLevel":
				// Fan-out methods return an aggregate with the denied backends left out.
				require.Equal(t, http.StatusOK, rr.Code, body)
			case "notifications/roots/list_changed":
				require.Equal(t, http.StatusAccepted, rr.Code, body)
			default:
				require.Equal(t, http.StatusForbidden, rr.Code, body)
				require.Contains(t, body, "access denied")
			}
		})
	}
}

// TestAuthorization_NoAuthorizationForwardsAllMethods pins that routes without an authorization
// policy keep forwarding every method.
func TestAuthorization_NoAuthorizationForwardsAllMethods(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, nil)
	for _, tc := range allMethodRequests {
		t.Run(tc.method, func(t *testing.T) {
			rr := doMCPPost(t, proxy, sessionID, tc.method, tc.params, nil)
			require.Less(t, rr.Code, 300, rr.Body.String())
			require.NotEmpty(t, backend.reset())
		})
	}
}

// TestAuthorization_MethodRules verifies that rules keyed on request.mcp.method /
// request.mcp.backend / request.mcp.params can grant access to non-tool methods per backend.
func TestAuthorization_MethodRules(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		Rules: []filterapi.MCPRouteAuthorizationRule{
			{
				Action: filterapi.AuthorizationActionDeny,
				CEL:    ptr.To(`request.mcp.method == "resources/read" && request.mcp.params.uri.endsWith("/forbidden")`),
			},
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method.startsWith("resources/") && request.mcp.backend == "backend1"`),
			},
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method == "prompts/get" && request.headers["x-tenant"] == "acme"`),
			},
		},
	})

	t.Run("resources/read allowed on backend1", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "resources/read", &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend1")}, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), "SECRET-RESOURCE")
		require.Equal(t, []string{"backend1 resources/read"}, backend.reset())
	})
	t.Run("resources/read denied by params rule", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "resources/read", &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///forbidden", "backend1")}, nil)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Empty(t, backend.reset())
	})
	t.Run("resources/read denied on backend2", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "resources/read", &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend2")}, nil)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Empty(t, backend.reset())
	})
	t.Run("resources/list only fans out to backend1", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "resources/list", &mcp.ListResourcesParams{}, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), "backend1")
		require.NotContains(t, rr.Body.String(), "backend2")
		require.Equal(t, []string{"backend1 resources/list"}, backend.reset())
	})
	t.Run("prompts/get allowed by header", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "prompts/get", &mcp.GetPromptParams{Name: "backend2__secret_prompt"}, map[string]string{"x-tenant": "acme"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, []string{"backend2 prompts/get"}, backend.reset())
	})
	t.Run("prompts/get denied without header", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "prompts/get", &mcp.GetPromptParams{Name: "backend2__secret_prompt"}, nil)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Empty(t, backend.reset())
	})
	t.Run("prompts/list stays denied", func(t *testing.T) {
		rr := doMCPPost(t, proxy, sessionID, "prompts/list", &mcp.ListPromptsParams{}, map[string]string{"x-tenant": "acme"})
		require.Equal(t, http.StatusOK, rr.Code)
		require.NotContains(t, rr.Body.String(), "secret_prompt")
		require.Empty(t, backend.reset())
	})
}

// TestAuthorization_NonToolMethodInsufficientScope verifies the insufficient_scope challenge is
// returned for non-tool methods, as it is for tools/call.
func TestAuthorization_NonToolMethodInsufficientScope(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		VerifiedJWT:   true,
		Rules: []filterapi.MCPRouteAuthorizationRule{{
			Action: filterapi.AuthorizationActionAllow,
			Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"read"}}},
		}},
	})
	token := func(scopes ...string) string {
		return verifiedJWTClaims(jwt.MapClaims{"scope": scopes})
	}
	params := &mcp.GetPromptParams{Name: "backend1__secret_prompt"}

	rr := doMCPPost(t, proxy, sessionID, "prompts/get", params, map[string]string{internalapi.MCPJWTClaimsHeader: token("other")})
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Header().Get("WWW-Authenticate"), `scope="read"`)
	require.Empty(t, backend.reset())

	rr = doMCPPost(t, proxy, sessionID, "prompts/get", params, map[string]string{internalapi.MCPJWTClaimsHeader: token("read")})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"backend1 prompts/get"}, backend.reset())
}

// TestAuthorization_ModernListMethods verifies the modern (stateless) list fan-out applies the
// same per-backend authorization as the legacy path.
func TestAuthorization_ModernListMethods(t *testing.T) {
	respFn := func(backend, method string) any {
		switch method {
		case "resources/list":
			return mcp.ListResourcesResult{Resources: []*mcp.Resource{{Name: "r", URI: "file:///" + backend}}}
		case "resources/templates/list":
			return mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{{Name: "t", URITemplate: "file:///" + backend + "/{x}"}}}
		default:
			return mcp.ListPromptsResult{Prompts: []*mcp.Prompt{{Name: "p"}}}
		}
	}
	type handlerFn func(*mcpRequestContext, context.Context, http.ResponseWriter, *http.Request, *jsonrpc.Request, filterapi.MCPRouteName) (handlerResult, error)
	handlers := map[string]handlerFn{
		"resources/list": func(m *mcpRequestContext, ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName) (handlerResult, error) {
			return m.handleModernResourcesList(ctx, w, r, req, route, nil)
		},
		"resources/templates/list": func(m *mcpRequestContext, ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName) (handlerResult, error) {
			return m.handleModernResourceTemplatesList(ctx, w, r, req, route, nil)
		},
		"prompts/list": func(m *mcpRequestContext, ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName) (handlerResult, error) {
			return m.handleModernPromptsList(ctx, w, r, req, route, nil)
		},
	}

	for method, handle := range handlers {
		t.Run(method, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				cel      string
				expected map[string]int
			}{
				{name: "default deny", cel: `false`, expected: map[string]int{}},
				{name: "backend1 only", cel: `request.mcp.method == "` + method + `" && request.mcp.backend == "backend1"`, expected: map[string]int{"backend1": 1}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					calls := &perBackendCallCount{}
					server := httptest.NewServer(modernBackendHandler(t, calls, nil, respFn))
					t.Cleanup(server.Close)
					proxy := newTestMCPProxy()
					proxy.backendListenerAddr = server.URL
					proxy.routes["test-route"].authorization = mustCompileAuthorization(t, &filterapi.MCPRouteAuthorization{
						DefaultAction: filterapi.AuthorizationActionDeny,
						Rules:         []filterapi.MCPRouteAuthorizationRule{{Action: filterapi.AuthorizationActionAllow, CEL: ptr.To(tc.cel)}},
					})
					rr := httptest.NewRecorder()
					_, err := handle(proxy, t.Context(), rr, newModernRequest(method), modernReq(t, method, nil), "test-route")
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
					require.NotContains(t, rr.Body.String(), "backend2")
					calls.mu.Lock()
					defer calls.mu.Unlock()
					require.Len(t, calls.count, len(tc.expected))
					for b, n := range tc.expected {
						require.Equal(t, n, calls.count[b])
					}
				})
			}
		})
	}
}

// TestAuthorization_UntargetedCELOnToolOnlyFields pins the upgrade caveat documented for
// non-tool methods: an untargeted rule whose CEL reads tool-only params fails to evaluate on other
// methods, and evaluation errors deny the request. Guarding the expression on the method avoids it.
func TestAuthorization_UntargetedCELOnToolOnlyFields(t *testing.T) {
	const argsExpr = `request.mcp.params.arguments.text == "hi"`
	allowAll := filterapi.MCPRouteAuthorizationRule{Action: filterapi.AuthorizationActionAllow, CEL: ptr.To(`true`)}

	t.Run("unguarded expression denies non-tool methods", func(t *testing.T) {
		proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
			DefaultAction: filterapi.AuthorizationActionDeny,
			Rules: []filterapi.MCPRouteAuthorizationRule{
				{Action: filterapi.AuthorizationActionDeny, CEL: ptr.To(argsExpr)},
				allowAll,
			},
		})
		rr := doMCPPost(t, proxy, sessionID, "prompts/get", &mcp.GetPromptParams{Name: "backend1__secret_prompt"}, nil)
		require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rr = doMCPPost(t, proxy, sessionID, "prompts/list", &mcp.ListPromptsParams{}, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.NotContains(t, rr.Body.String(), "secret_prompt")
		require.Empty(t, backend.reset())

		// tools/call still evaluates the expression normally.
		rr = doMCPPost(t, proxy, sessionID, "tools/call", &mcp.CallToolParams{Name: "backend1__t", Arguments: map[string]any{"text": "other"}}, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, []string{"backend1 tools/call"}, backend.reset())
	})

	t.Run("method-guarded expression allows non-tool methods", func(t *testing.T) {
		proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
			DefaultAction: filterapi.AuthorizationActionDeny,
			Rules: []filterapi.MCPRouteAuthorizationRule{
				{Action: filterapi.AuthorizationActionDeny, CEL: ptr.To(`request.mcp.method == "tools/call" && ` + argsExpr)},
				allowAll,
			},
		})
		rr := doMCPPost(t, proxy, sessionID, "prompts/get", &mcp.GetPromptParams{Name: "backend1__secret_prompt"}, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, []string{"backend1 prompts/get"}, backend.reset())

		rr = doMCPPost(t, proxy, sessionID, "tools/call", &mcp.CallToolParams{Name: "backend1__t", Arguments: map[string]any{"text": "hi"}}, nil)
		require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		require.Empty(t, backend.reset())
	})
}

// TestAuthorization_FanOutPartialAllow verifies that logging/setLevel and
// notifications/roots/list_changed only reach the backends the policy allows.
func TestAuthorization_FanOutPartialAllow(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		Rules: []filterapi.MCPRouteAuthorizationRule{{
			Action: filterapi.AuthorizationActionAllow,
			CEL:    ptr.To(`request.mcp.backend == "backend2" && request.mcp.method in ["logging/setLevel", "notifications/roots/list_changed"]`),
		}},
	})

	rr := doMCPPost(t, proxy, sessionID, "logging/setLevel", &mcp.SetLoggingLevelParams{Level: "debug"}, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"backend2 logging/setLevel"}, backend.reset())

	rr = doMCPPost(t, proxy, sessionID, "notifications/roots/list_changed", &mcp.RootsListChangedParams{}, nil)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	require.Equal(t, []string{"backend2 notifications/roots/list_changed"}, backend.reset())
}

// TestAuthorization_CompletionSeesClientFacingParams verifies that completion/complete is
// authorized against the params as the client sent them (backend-prefixed names and URIs), not
// the unprefixed upstream names forwarded to the backend. ref.name and ref.uri are omitted when
// empty, so each rule is guarded on ref.type to avoid CEL "no such key" errors, which deny.
func TestAuthorization_CompletionSeesClientFacingParams(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		Rules: []filterapi.MCPRouteAuthorizationRule{
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method == "completion/complete" && request.mcp.params.ref.type == "ref/prompt" && request.mcp.params.ref.name == "backend1__allowed_prompt"`),
			},
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method == "completion/complete" && request.mcp.params.ref.type == "ref/resource" && request.mcp.params.ref.uri == "` + downstreamResourceURI("file:///{x}", "backend1") + `"`),
			},
		},
	})
	complete := func(ref *mcp.CompleteReference) *httptest.ResponseRecorder {
		return doMCPPost(t, proxy, sessionID, "completion/complete", &mcp.CompleteParams{Ref: ref, Argument: mcp.CompleteParamsArgument{Name: "a", Value: "b"}}, nil)
	}

	rr := complete(&mcp.CompleteReference{Type: "ref/prompt", Name: "backend1__allowed_prompt"})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"backend1 completion/complete"}, backend.reset())

	// The unprefixed upstream name must not be what authorization sees.
	rr = complete(&mcp.CompleteReference{Type: "ref/prompt", Name: "backend1__other_prompt"})
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	require.Empty(t, backend.reset())

	rr = complete(&mcp.CompleteReference{Type: "ref/resource", URI: downstreamResourceURI("file:///{x}", "backend1")})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"backend1 completion/complete"}, backend.reset())
}

// TestAuthorization_CompletionMissingRef verifies that completion/complete without a ref is
// rejected as invalid params instead of dereferencing a nil ref.
func TestAuthorization_CompletionMissingRef(t *testing.T) {
	for _, auth := range []*filterapi.MCPRouteAuthorization{nil, {DefaultAction: filterapi.AuthorizationActionAllow}} {
		proxy, backend, sessionID := newAuthorizationTestProxy(t, auth)
		rr := doMCPPost(t, proxy, sessionID, "completion/complete", map[string]any{"argument": map[string]any{"name": "a", "value": "b"}}, nil)
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), "missing ref")
		require.Empty(t, backend.reset())
	}
}

// TestAuthorization_ModernSingleBackendMethods verifies that the modern (stateless) single-backend
// handlers evaluate the route's authorization policy, against the client-facing params, before
// forwarding.
func TestAuthorization_ModernSingleBackendMethods(t *testing.T) {
	respFn := func(_, method string) any {
		switch method {
		case "resources/read":
			return mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "file:///secret", Text: "SECRET-RESOURCE"}}}
		case "prompts/get":
			return mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "SECRET-PROMPT"}}}}
		default:
			return mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{"SECRET-COMPLETION"}}}
		}
	}
	type handlerFn func(*mcpRequestContext, *httptest.ResponseRecorder, *http.Request, *jsonrpc.Request) (handlerResult, error)
	read := func(m *mcpRequestContext, w *httptest.ResponseRecorder, r *http.Request, req *jsonrpc.Request) (handlerResult, error) {
		return m.handleModernResourcesRead(r.Context(), w, r, req, "test-route", nil)
	}
	get := func(m *mcpRequestContext, w *httptest.ResponseRecorder, r *http.Request, req *jsonrpc.Request) (handlerResult, error) {
		return m.handleModernPromptsGet(r.Context(), w, r, req, "test-route", nil)
	}
	complete := func(m *mcpRequestContext, w *httptest.ResponseRecorder, r *http.Request, req *jsonrpc.Request) (handlerResult, error) {
		return m.handleModernComplete(r.Context(), w, r, req, "test-route", nil)
	}
	completeArg := mcp.CompleteParamsArgument{Name: "a", Value: "b"}
	// Only backend1 is allowed. The completion rules match the client-facing (prefixed) ref.
	auth := &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		Rules: []filterapi.MCPRouteAuthorizationRule{
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method in ["resources/read", "prompts/get"] && request.mcp.backend == "backend1"`),
			},
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method == "completion/complete" && request.mcp.params.ref.type == "ref/prompt" && request.mcp.params.ref.name == "backend1__p"`),
			},
			{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method == "completion/complete" && request.mcp.params.ref.type == "ref/resource" && request.mcp.params.ref.uri.startsWith("backend1+")`),
			},
		},
	}

	for _, tc := range []struct {
		name    string
		method  string
		handle  handlerFn
		allowed any
		denied  any
	}{
		{
			name: "resources/read", method: "resources/read", handle: read,
			allowed: &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend1")},
			denied:  &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend2")},
		},
		{
			name: "prompts/get", method: "prompts/get", handle: get,
			allowed: &mcp.GetPromptParams{Name: "backend1__p"},
			denied:  &mcp.GetPromptParams{Name: "backend2__p"},
		},
		{
			name: "completion/complete ref/prompt", method: "completion/complete", handle: complete,
			allowed: &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/prompt", Name: "backend1__p"}, Argument: completeArg},
			denied:  &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/prompt", Name: "backend1__other"}, Argument: completeArg},
		},
		{
			name: "completion/complete ref/resource", method: "completion/complete", handle: complete,
			allowed: &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/resource", URI: downstreamResourceURI("file:///{x}", "backend1")}, Argument: completeArg},
			denied:  &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/resource", URI: downstreamResourceURI("file:///{x}", "backend2")}, Argument: completeArg},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &perBackendCallCount{}
			server := httptest.NewServer(modernBackendHandler(t, calls, nil, respFn))
			t.Cleanup(server.Close)
			proxy := newTestMCPProxy()
			proxy.backendListenerAddr = server.URL
			proxy.routes["test-route"].authorization = mustCompileAuthorization(t, auth)
			call := func(params any) *httptest.ResponseRecorder {
				encoded, err := json.Marshal(params)
				require.NoError(t, err)
				rr := httptest.NewRecorder()
				_, _ = tc.handle(proxy, rr, newModernRequest(tc.method), modernReq(t, tc.method, encoded))
				return rr
			}

			rr := call(tc.denied)
			require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
			require.Contains(t, rr.Body.String(), "access denied")
			require.NotContains(t, rr.Body.String(), "SECRET")
			calls.mu.Lock()
			require.Empty(t, calls.count, "denied request must not reach any backend")
			calls.mu.Unlock()

			rr = call(tc.allowed)
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			require.Contains(t, rr.Body.String(), "SECRET")
			calls.mu.Lock()
			require.Equal(t, map[string]int{"backend1": 1}, calls.count)
			calls.mu.Unlock()
		})
	}
}

// TestAuthorization_ModernSubscriptionsListen verifies that subscriptions/listen only opens streams
// to backends the route's authorization policy allows, and rejects resource subscriptions on denied
// backends.
func TestAuthorization_ModernSubscriptionsListen(t *testing.T) {
	newProxy := func(t *testing.T) (*mcpRequestContext, *perBackendCallCount) {
		calls := &perBackendCallCount{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.inc(r.Header.Get(internalapi.MCPBackendHeader))
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			ack := `{"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"notifications":{"toolsListChanged":true}}}`
			_, _ = w.Write([]byte("event: message\ndata: " + ack + "\n\n"))
		}))
		t.Cleanup(server.Close)
		proxy := newTestMCPProxy()
		proxy.backendListenerAddr = server.URL
		proxy.routes["test-route"].authorization = mustCompileAuthorization(t, &filterapi.MCPRouteAuthorization{
			DefaultAction: filterapi.AuthorizationActionDeny,
			Rules: []filterapi.MCPRouteAuthorizationRule{{
				Action: filterapi.AuthorizationActionAllow,
				CEL:    ptr.To(`request.mcp.method == "subscriptions/listen" && request.mcp.backend == "backend1"`),
			}},
		})
		return proxy, calls
	}
	listen := func(t *testing.T, proxy *mcpRequestContext, notifications map[string]any) *concurrentRecorder {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		params, err := json.Marshal(map[string]any{"notifications": notifications})
		require.NoError(t, err)
		rr := &concurrentRecorder{ResponseRecorder: httptest.NewRecorder()}
		_, _ = proxy.handleSubscriptionsListen(ctx, rr, newModernRequest("subscriptions/listen").WithContext(ctx), modernReq(t, "subscriptions/listen", params), "test-route", nil)
		return rr
	}

	t.Run("list_changed only opens allowed backends", func(t *testing.T) {
		proxy, calls := newProxy(t)
		rr := listen(t, proxy, map[string]any{"toolsListChanged": true})
		require.Contains(t, rr.bodyString(), "notifications/subscriptions/acknowledged")
		calls.mu.Lock()
		defer calls.mu.Unlock()
		require.Equal(t, map[string]int{"backend1": 1}, calls.count)
	})

	t.Run("resource subscription on denied backend is rejected", func(t *testing.T) {
		proxy, calls := newProxy(t)
		rr := listen(t, proxy, map[string]any{
			"resourceSubscriptions": []string{downstreamResourceURI("file:///secret", "backend2")},
		})
		require.Equal(t, http.StatusForbidden, rr.Code, rr.bodyString())
		require.Contains(t, rr.bodyString(), "access denied")
		calls.mu.Lock()
		defer calls.mu.Unlock()
		require.Empty(t, calls.count)
	})
}

// TestAuthorization_ScopeChallengeOnlyForMatchingRules verifies that the insufficient_scope
// challenge only advertises the scopes of a rule whose CEL condition matches the request, not
// those of a rule written for another method that would still deny after the scope is acquired.
func TestAuthorization_ScopeChallengeOnlyForMatchingRules(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		VerifiedJWT:   true,
		Rules: []filterapi.MCPRouteAuthorizationRule{
			{
				Action: filterapi.AuthorizationActionAllow,
				Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"prompts"}}},
				CEL:    ptr.To(`request.mcp.method == "prompts/get"`),
			},
			{
				Action: filterapi.AuthorizationActionAllow,
				Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"resources"}}},
				CEL:    ptr.To(`request.mcp.method == "resources/read"`),
			},
		},
	})
	headers := map[string]string{internalapi.MCPJWTClaimsHeader: verifiedJWTClaims(jwt.MapClaims{"scope": "other"})}

	rr := doMCPPost(t, proxy, sessionID, "resources/read", &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend1")}, headers)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Header().Get("WWW-Authenticate"), `scope="resources"`)
	require.Empty(t, backend.reset())

	rr = doMCPPost(t, proxy, sessionID, "prompts/get", &mcp.GetPromptParams{Name: "backend1__secret_prompt"}, headers)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Header().Get("WWW-Authenticate"), `scope="prompts"`)

	// No rule matches completion/complete at all, so there is no scope to advertise.
	rr = doMCPPost(t, proxy, sessionID, "completion/complete", &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/prompt", Name: "backend1__secret_prompt"}, Argument: mcp.CompleteParamsArgument{Name: "a", Value: "b"}}, headers)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Empty(t, rr.Header().Get("WWW-Authenticate"))
	require.Empty(t, backend.reset())
}

// TestAuthorization_FilteredListsArePrivate verifies that aggregated list results are marked as
// privately cacheable when the route's authorization policy can filter them per caller, even if
// every backend reported a public cache scope.
func TestAuthorization_FilteredListsArePrivate(t *testing.T) {
	public := mcp.Cacheable{TTLMs: 60000, CacheScope: "public"}
	for _, tc := range []struct {
		name          string
		authorization *compiledAuthorization
		expected      string
	}{
		{name: "no authorization", expected: "public"},
		{name: "authorization", authorization: mustCompileAuthorization(t, &filterapi.MCPRouteAuthorization{DefaultAction: filterapi.AuthorizationActionAllow}), expected: "private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTestMCPProxy()
			proxy.routes["test-route"].toolSelectors = nil
			proxy.routes["test-route"].authorization = tc.authorization
			s := &session{route: "test-route"}

			tools := proxy.mergeToolsList(s, []broadCastResponse[mcp.ListToolsResult]{{backendName: "backend1", res: mcp.ListToolsResult{Cacheable: public}}})
			require.Equal(t, tc.expected, tools.CacheScope)
			resources := proxy.mergeResourceList(s, []broadCastResponse[mcp.ListResourcesResult]{{backendName: "backend1", res: mcp.ListResourcesResult{Cacheable: public}}})
			require.Equal(t, tc.expected, resources.CacheScope)
			templates := proxy.mergeResourcesTemplateList(s, []broadCastResponse[mcp.ListResourceTemplatesResult]{{backendName: "backend1", res: mcp.ListResourceTemplatesResult{Cacheable: public}}})
			require.Equal(t, tc.expected, templates.CacheScope)
			prompts := proxy.mergePromptsList(s, []broadCastResponse[mcp.ListPromptsResult]{{backendName: "backend1", res: mcp.ListPromptsResult{Cacheable: public}}})
			require.Equal(t, tc.expected, prompts.CacheScope)
		})
	}
}

// TestAuthorization_ModernSubscriptionsListenScopeChallenge verifies that a resource subscription
// denied for missing scopes carries the insufficient_scope challenge, like resources/read.
func TestAuthorization_ModernSubscriptionsListenScopeChallenge(t *testing.T) {
	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = "http://unused"
	proxy.routes["test-route"].authorization = mustCompileAuthorization(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		VerifiedJWT:   true,
		Rules: []filterapi.MCPRouteAuthorizationRule{{
			Action: filterapi.AuthorizationActionAllow,
			Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"subscribe"}}},
			CEL:    ptr.To(`request.mcp.method == "subscriptions/listen"`),
		}},
	})
	params, err := json.Marshal(map[string]any{"notifications": map[string]any{
		"resourceSubscriptions": []string{downstreamResourceURI("file:///secret", "backend1")},
	}})
	require.NoError(t, err)
	r := newModernRequest("subscriptions/listen")
	r.Header.Set(internalapi.MCPJWTClaimsHeader, verifiedJWTClaims(jwt.MapClaims{"scope": "other"}))
	rr := httptest.NewRecorder()

	_, err = proxy.handleSubscriptionsListen(t.Context(), rr, r, modernReq(t, "subscriptions/listen", params), "test-route", nil)
	require.ErrorIs(t, err, errAuthorizationDenied)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Header().Get("WWW-Authenticate"), `scope="subscribe"`)
}

// TestAuthorization_ScopeChallengeWithScopeDependentCEL verifies that a rule whose CEL also checks
// request.auth.jwt.scopes still yields a challenge for its missing scopes: the CEL is evaluated as
// if the caller already held them.
func TestAuthorization_ScopeChallengeWithScopeDependentCEL(t *testing.T) {
	proxy, backend, sessionID := newAuthorizationTestProxy(t, &filterapi.MCPRouteAuthorization{
		DefaultAction: filterapi.AuthorizationActionDeny,
		VerifiedJWT:   true,
		Rules: []filterapi.MCPRouteAuthorizationRule{{
			Action: filterapi.AuthorizationActionAllow,
			Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"read"}}},
			CEL:    ptr.To(`request.mcp.method == "resources/read" && "read" in request.auth.jwt.scopes`),
		}},
	})
	params := &mcp.ReadResourceParams{URI: downstreamResourceURI("file:///secret", "backend1")}

	rr := doMCPPost(t, proxy, sessionID, "resources/read", params, map[string]string{internalapi.MCPJWTClaimsHeader: verifiedJWTClaims(jwt.MapClaims{"scope": "other"})})
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Header().Get("WWW-Authenticate"), `scope="read"`)
	require.Empty(t, backend.reset())

	rr = doMCPPost(t, proxy, sessionID, "resources/read", params, map[string]string{internalapi.MCPJWTClaimsHeader: verifiedJWTClaims(jwt.MapClaims{"scope": "read"})})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"backend1 resources/read"}, backend.reset())
}

// TestAuthorization_ResourceReadIsPrivate verifies that resources/read results are marked private
// on routes with an authorization policy, even when the backend reports a public cache scope, on
// both the legacy and the modern path.
func TestAuthorization_ResourceReadIsPrivate(t *testing.T) {
	const publicResult = `{"contents":[{"uri":"file:///x","text":"t"}],"ttlMs":60000,"cacheScope":"public"}`
	authz := mustCompileAuthorization(t, &filterapi.MCPRouteAuthorization{DefaultAction: filterapi.AuthorizationActionAllow})
	cacheScope := func(t *testing.T, raw []byte) string {
		var out struct {
			CacheScope string `json:"cacheScope"`
		}
		require.NoError(t, json.Unmarshal(raw, &out))
		return out.CacheScope
	}

	for _, tc := range []struct {
		name          string
		authorization *compiledAuthorization
		expected      string
	}{
		{name: "no authorization", expected: "public"},
		{name: "authorization", authorization: authz, expected: "private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("legacy", func(t *testing.T) {
				proxy := newTestMCPProxy()
				proxy.routes["test-route"].authorization = tc.authorization
				msg := &jsonrpc.Response{Result: []byte(publicResult)}
				require.NoError(t, proxy.maybeResponseModify(t.Context(), &session{route: "test-route"}, &jsonrpc.Request{Method: "resources/read"}, msg, "backend1"))
				require.Equal(t, tc.expected, cacheScope(t, msg.Result))
			})
			t.Run("modern", func(t *testing.T) {
				server := httptest.NewServer(modernBackendHandler(t, nil, nil, func(string, string) any { return json.RawMessage(publicResult) }))
				t.Cleanup(server.Close)
				proxy := newTestMCPProxy()
				proxy.backendListenerAddr = server.URL
				proxy.routes["test-route"].authorization = tc.authorization
				params, err := json.Marshal(&mcp.ReadResourceParams{URI: downstreamResourceURI("file:///x", "backend1")})
				require.NoError(t, err)
				rr := httptest.NewRecorder()
				_, err = proxy.handleModernResourcesRead(t.Context(), rr, newModernRequest("resources/read"), modernReq(t, "resources/read", params), "test-route", nil)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
				require.Equal(t, tc.expected, cacheScope(t, decodeResultRaw(t, rr)))
			})
		})
	}
}

func decodeResultRaw(t *testing.T, rr *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &envelope))
	return envelope.Result
}
