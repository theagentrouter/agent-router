// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"cmp"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestAuthorizeRequest(t *testing.T) {
	makeTokenWithClaims := func(extraClaims jwt.MapClaims, scopes ...string) string {
		claims := jwt.MapClaims{}
		maps.Copy(claims, extraClaims)
		if len(scopes) > 0 {
			claims["scope"] = scopes
		}
		token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		signed, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
		return signed
	}

	makeToken := func(scopes ...string) string {
		return makeTokenWithClaims(jwt.MapClaims{}, scopes...)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy := &mcpRequestContext{ProxyConfig: &ProxyConfig{l: logger}}

	tests := []struct {
		name          string
		auth          *filterapi.MCPRouteAuthorization
		backend       string
		tool          string
		args          mcp.Params
		host          string
		headers       http.Header
		mcpMethod     string
		expectError   bool
		expectAllowed bool
		expectScopes  []string
	}{
		{
			name: "rule CEL matches all conditions",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						CEL:    new(`request.host.startsWith("api.") && request.mcp.backend == "backend1" && request.mcp.params.arguments.mode == "fast" && request.headers["x-tenant-id"] == "t-123"`),
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			args:          &mcp.CallToolParams{Arguments: map[string]any{"mode": "fast"}},
			host:          "api.example.com",
			headers:       http.Header{"X-Tenant-Id": []string{"t-123"}},
			expectAllowed: true,
		},
		{
			name: "rule CEL non match falls back to default deny",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						CEL:    new(`request.host.startsWith("api.") && request.mcp.backend == "backend1" && request.mcp.params.arguments.mode == "fast" && request.headers["x-tenant-id"] == "t-123"`),
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			args:          &mcp.CallToolParams{Name: "p1", Arguments: map[string]any{"mode": "fast"}},
			host:          "api.example.com",
			headers:       http.Header{"X-Tenant-Id": []string{"t-234"}},
			expectAllowed: false,
		},
		{
			name: "rule CEL returns non boolean treated as non match",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						CEL:    new(`10`),
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
		},
		{
			name: "invalid CEL denies",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						CEL: new(`invalid syntax here`),
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend: "backend1",
			tool:    "tool1",
			args: &mcp.CallToolParams{
				Name: "p1",
				Arguments: map[string]any{
					"mode": "other",
				},
			},
			expectError:   true,
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "rule with source target and CEL all match",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read", "write"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						CEL: new(`request.method == "POST" && request.mcp.backend == "backend1" && request.mcp.tool == "tool1" && request.headers["x-tenant-id"] == "t-123" && request.mcp.params.arguments["flag"] == true`),
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeToken("read", "write")}, "X-Tenant-Id": []string{"t-123"}},
			backend: "backend1",
			tool:    "tool1",
			args: &mcp.CallToolParams{
				Name: "p1",
				Arguments: map[string]any{
					"flag": true,
				},
			},
			expectAllowed: true,
		},
		{
			name: "source target match but CEL does not",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						CEL: new(`request.method == "GET"`),
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
		},
		{
			name: "CEL match but source target do not",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"write"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend2",
								Tool:    "tool2",
							}},
						},
						CEL: new(`request.method == "POST"`),
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("write")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
		},
		{
			name: "matching tool and scope",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read", "write"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("read", "write")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "numeric argument matches via CEL",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						CEL: new(`int(request.mcp.params.arguments["count"]) >= 40 && int(request.mcp.params.arguments["count"]) < 50`),
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend: "backend1",
			tool:    "tool1",
			args: &mcp.CallToolParams{
				Name: "p1",
				Arguments: map[string]any{
					"count": 42,
				},
			},
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "object argument can be matched via CEL safe navigation",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						CEL: new(`request.mcp.params.arguments["payload"] != null && request.mcp.params.arguments["payload"]["kind"] == "test" && request.mcp.params.arguments["payload"]["value"] == 123`),
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend: "backend1",
			tool:    "tool1",
			args: &mcp.CallToolParams{
				Name: "p1",
				Arguments: map[string]any{
					"payload": map[string]any{
						"kind":  "test",
						"value": 123,
					},
				},
			},
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "matching tool but insufficient scopes not allowed",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read", "write"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  []string{"read", "write"},
		},
		{
			name: "missing argument denies when required",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						CEL: new(`request.mcp.params.arguments["mode"] == "fast"`),
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend:       "backend1",
			tool:          "tool1",
			args:          &mcp.CallToolParams{Name: "p1", Arguments: map[string]any{}},
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "no matching rule falls back to default deny - tool mismatch",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("read", "write")}},
			backend:       "backend1",
			tool:          "other-tool",
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "no matching rule falls back to default deny - scope mismatch",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("foo", "bar")}},
			backend:       "backend1",
			tool:          "other-tool",
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "no bearer token not allowed when rules exist",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  []string{"read"},
		},
		{
			name: "invalid bearer token not allowed when rules exist",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer invalid.token.here"}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  []string{"read"},
		},
		{
			name: "selects smallest required scope set when multiple rules match",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"alpha", "beta", "gamma"}}},
						Target: &filterapi.MCPAuthorizationTarget{Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}}},
					},
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{JWT: filterapi.JWTSource{Scopes: []string{"alpha", "beta"}}},
						Target: &filterapi.MCPAuthorizationTarget{Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}}},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("alpha")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  []string{"alpha", "beta"},
		},
		{
			name: "allow requests with required scopes except those matching CEL deny rule - deny request",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Deny",
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "listFiles",
							}},
						},
						CEL: new(`request.mcp.params.arguments["folder"] == "restricted"`),
					},
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "listFiles",
							}},
						},
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend: "backend1",
			tool:    "listFiles",
			args: &mcp.CallToolParams{
				Name: "p1",
				Arguments: map[string]any{
					"folder": "restricted",
				},
			},
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "allow requests with required scopes except those matching CEL deny rule - allow request",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Deny",
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "listFiles",
							}},
						},
						CEL: new(`request.mcp.params.arguments["folder"] == "restricted"`),
					},
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "listFiles",
							}},
						},
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend: "backend1",
			tool:    "listFiles",
			args: &mcp.CallToolParams{
				Name: "p1",
				Arguments: map[string]any{
					"folder": "allowed",
				},
			},
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "no rules default deny",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "no rules default allow",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Allow",
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "empty rule default deny",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "empty rule default allow",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Allow",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Deny",
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "rule with no source allows all requests for matching tool",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{
								Backend: "backend1",
								Tool:    "tool1",
							}},
						},
						Action: "Allow",
					},
				},
			},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "rule with no target allows all requests with matching source",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Action: "Allow",
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeToken("read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "claims mismatch denies request",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"read"},
								Claims: []filterapi.JWTClaim{{
									Name:      "tenant",
									ValueType: filterapi.JWTClaimValueTypeString,
									Values:    []string{"acme", "globex"},
								}},
							},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"tenant": "other"}, "read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "claims match allows request - first value",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"read"},
								Claims: []filterapi.JWTClaim{{
									Name:      "tenant",
									ValueType: filterapi.JWTClaimValueTypeString,
									Values:    []string{"acme", "globex"},
								}},
							},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"tenant": "acme"}, "read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "claims match allows request - second value",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"read"},
								Claims: []filterapi.JWTClaim{{
									Name:      "tenant",
									ValueType: filterapi.JWTClaimValueTypeString,
									Values:    []string{"acme", "globex"},
								}},
							},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"tenant": "globex"}, "read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "opaque token denies with required scope challenge",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"read"},
							},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			// Opaque tokens are not JWTs; parsing fails so no scopes are extracted.
			headers:       http.Header{"Authorization": []string{"Bearer opaque-token"}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  []string{"read"},
		},
		{
			name: "scope mismatch denies request even if claims match",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"admin"},
								Claims: []filterapi.JWTClaim{{
									Name:      "tenant",
									ValueType: filterapi.JWTClaimValueTypeString,
									Values:    []string{"acme", "globex"},
								}},
							},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"tenant": "acme"}, "read")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  []string{"admin"},
		},
		{
			name: "scope and claims match allows request",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"admin"},
								Claims: []filterapi.JWTClaim{
									{
										Name:      "tenant",
										ValueType: filterapi.JWTClaimValueTypeString,
										Values:    []string{"acme", "globex"},
									},
								},
							},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"tenant": "acme"}, "admin")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
		},
		{
			name: "matching nested jwt claim string array value",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Claims: []filterapi.JWTClaim{{
									Name:      "org.departments",
									ValueType: filterapi.JWTClaimValueTypeStringArray,
									Values:    []string{"security", "hr"},
								}},
							},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{
				"org": map[string]any{"departments": []any{"engineering", "security"}},
			})}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "matching nested jwt claim string array value via CEL",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						CEL:    new(`request.auth.jwt.claims["org"]["departments"].exists(d, d == "security")`),
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{
				"org": map[string]any{"departments": []any{"engineering", "security"}},
			})}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "non-matching nested jwt claim string array value",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Claims: []filterapi.JWTClaim{{
									Name:      "org.departments",
									ValueType: filterapi.JWTClaimValueTypeStringArray,
									Values:    []string{"operations", "hr"},
								}},
							},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{
				"org": map[string]any{"departments": []any{"engineering", "security"}},
			})}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: false,
			expectScopes:  nil,
		},
		{
			name: "matching nested jwt claim string array single value",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Claims: []filterapi.JWTClaim{{
									Name:      "org.departments",
									ValueType: filterapi.JWTClaimValueTypeStringArray,
									Values:    []string{"operations", "hr", "engineering"},
								}},
							},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers: http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{
				"org": map[string]any{"departments": "engineering"},
			})}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
			expectScopes:  nil,
		},
		{
			name: "complex matching nested jwt claims",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{
								Scopes: []string{"admin"},
								Claims: []filterapi.JWTClaim{
									{
										Name:      "tenant",
										ValueType: filterapi.JWTClaimValueTypeString,
										Values:    []string{"acme", "globex"},
									},
									{
										Name:      "org.departments",
										ValueType: filterapi.JWTClaimValueTypeStringArray,
										Values:    []string{"operations", "engineering"},
									},
								},
							},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"tenant": "acme", "org": map[string]any{"departments": []any{"engineering", "hr"}}}, "admin")}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
		},
		{
			name: "scp claim used as scopes",
			auth: &filterapi.MCPRouteAuthorization{
				DefaultAction: "Deny",
				Rules: []filterapi.MCPRouteAuthorizationRule{
					{
						Action: "Allow",
						Source: &filterapi.MCPAuthorizationSource{
							JWT: filterapi.JWTSource{Scopes: []string{"read"}},
						},
						Target: &filterapi.MCPAuthorizationTarget{
							Tools: []filterapi.ToolCall{{Backend: "backend1", Tool: "tool1"}},
						},
					},
				},
			},
			headers:       http.Header{"Authorization": []string{"Bearer " + makeTokenWithClaims(jwt.MapClaims{"scp": "read"})}},
			backend:       "backend1",
			tool:          "tool1",
			expectAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := tt.headers
			if headers == nil {
				headers = http.Header{}
			}
			compiled, err := compileAuthorization(tt.auth)
			if (err != nil) != tt.expectError {
				t.Fatalf("expected error: %v, got: %v", tt.expectError, err)
			}
			if err != nil {
				return
			}
			allowed, requiredScopes := proxy.authorizeRequest(compiled, &authorizationRequest{
				Headers:    headers,
				HTTPMethod: cmp.Or(tt.mcpMethod, http.MethodPost),
				Host:       tt.host,
				HTTPPath:   "/mcp",
				MCPMethod:  cmp.Or(tt.mcpMethod, "tools/call"),
				Backend:    tt.backend,
				Tool:       tt.tool,
				Params:     tt.args,
			})
			if allowed != tt.expectAllowed {
				t.Fatalf("expected %v, got %v", tt.expectAllowed, allowed)
			}
			if !reflect.DeepEqual(requiredScopes, tt.expectScopes) {
				t.Fatalf("expected required scopes %v, got %v", tt.expectScopes, requiredScopes)
			}
		})
	}
}

func TestBuildInsufficientScopeHeader(t *testing.T) {
	const resourceMetadata = "https://api.example.com/.well-known/oauth-protected-resource/mcp"

	t.Run("with scopes and resource metadata", func(t *testing.T) {
		header := buildInsufficientScopeHeader([]string{"read", "write"}, resourceMetadata)
		expected := `Bearer error="insufficient_scope", scope="read write", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp", error_description="The token is missing required scopes"`
		if header != expected {
			t.Fatalf("expected %q, got %q", expected, header)
		}
	})
}

func TestCompileAuthorizationInvalidRuleCEL(t *testing.T) {
	_, err := compileAuthorization(&filterapi.MCPRouteAuthorization{
		Rules: []filterapi.MCPRouteAuthorizationRule{
			{
				CEL: new("request."),
			},
		},
	})
	if err == nil {
		t.Fatalf("expected compile error for invalid rule CEL expression")
	}
}
