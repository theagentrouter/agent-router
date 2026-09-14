// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func newFanoutSubsetTestServer() (*httptest.Server, *perBackendCallCount) {
	callCount := &perBackendCallCount{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := r.Header.Get(internalapi.MCPBackendHeader)
		if backend == "" {
			http.Error(w, "missing backend header", http.StatusBadRequest)
			return
		}
		if callCount.inc(backend)%2 == 1 {
			w.Header().Set(sessionIDHeader, fmt.Sprintf("test-session-%s", backend))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(validInitializeResponse))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	return server, callCount
}

func mustCompileBackendSelector(t *testing.T, sel *filterapi.MCPRouteAuthorization) *compiledAuthorization {
	t.Helper()
	compiled, err := compileAuthorization(sel)
	require.NoError(t, err)
	return compiled
}

func bearerTokenWithClaims(claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	return signed
}

func TestNewSession_BackendSelector(t *testing.T) {
	tests := []struct {
		name                string
		backendSelector     *filterapi.MCPRouteAuthorization
		setHeaders          func(h http.Header)
		wantCalls           map[string]int
		wantSessionBackends []filterapi.MCPBackendName
		wantErr             string
		wantErrIs           error
	}{
		{
			name:                "no selector configured initializes all route backends",
			backendSelector:     nil,
			wantCalls:           map[string]int{"backend1": 2, "backend2": 2},
			wantSessionBackends: []filterapi.MCPBackendName{"backend1", "backend2"},
		},
		{
			name: "JWT claims rule selects a single backend (no shim required)",
			backendSelector: &filterapi.MCPRouteAuthorization{
				DefaultAction: filterapi.AuthorizationActionDeny,
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: filterapi.AuthorizationActionAllow,
						CEL:    ptr.To(`request.mcp.backend in request.auth.jwt.claims.mcp_backends`),
					},
				},
			},
			setHeaders: func(h http.Header) {
				token := bearerTokenWithClaims(jwt.MapClaims{"mcp_backends": []string{"backend2"}})
				h.Set("Authorization", "Bearer "+token)
			},
			wantCalls:           map[string]int{"backend1": 0, "backend2": 2},
			wantSessionBackends: []filterapi.MCPBackendName{"backend2"},
		},
		{
			name: "header-based rule selects a single backend (shim-backed)",
			backendSelector: &filterapi.MCPRouteAuthorization{
				DefaultAction: filterapi.AuthorizationActionDeny,
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: filterapi.AuthorizationActionAllow,
						CEL:    ptr.To(`("," + request.headers["x-ai-eg-mcp-backend-subset"] + ",").contains("," + request.mcp.backend + ",")`),
					},
				},
			},
			setHeaders: func(h http.Header) {
				h.Set(internalapi.MCPBackendSubsetHeader, "backend1")
			},
			wantCalls:           map[string]int{"backend1": 2, "backend2": 0},
			wantSessionBackends: []filterapi.MCPBackendName{"backend1"},
		},
		{
			name: "no rule matches and default is deny fails without initializing",
			backendSelector: &filterapi.MCPRouteAuthorization{
				DefaultAction: filterapi.AuthorizationActionDeny,
			},
			wantCalls: map[string]int{"backend1": 0, "backend2": 0},
			wantErr:   "backendSelector matches no route backends for route test-route",
			wantErrIs: errNoMatchingBackendSelector,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, callCount := newFanoutSubsetTestServer()
			defer server.Close()

			proxy := newTestMCPProxy()
			proxy.backendListenerAddr = server.URL
			proxy.requestHeaders = http.Header{}
			if tc.setHeaders != nil {
				tc.setHeaders(proxy.requestHeaders)
			}
			proxy.routes["test-route"].backendSelector = mustCompileBackendSelector(t, tc.backendSelector)

			s, err := proxy.newSession(t.Context(), &mcp.InitializeParams{}, "test-route", "", nil, time.Now())
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				if tc.wantErrIs != nil {
					require.ErrorIs(t, err, tc.wantErrIs)
				}
				require.Nil(t, s)
			} else {
				require.NoError(t, err)
				require.NotNil(t, s)
				require.Len(t, s.perBackendSessions, len(tc.wantSessionBackends))
				for _, backend := range tc.wantSessionBackends {
					require.Contains(t, s.perBackendSessions, backend)
				}
			}
			for backend, want := range tc.wantCalls {
				require.Equal(t, want, callCount.get(backend))
			}
		})
	}
}
