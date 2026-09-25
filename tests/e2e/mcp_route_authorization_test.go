// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// bearerTokenTransport injects a bearer token into outgoing requests.
type bearerTokenTransport struct {
	token   string
	headers map[string]string
	base    http.RoundTripper
}

// RoundTrip implements [http.RoundTripper.RoundTrip].
func (t *bearerTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return base.RoundTrip(req)
}

func TestMCPRouteAuthorization(t *testing.T) {
	const manifest = "testdata/mcp_route_authorization.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway-authorization"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	client := mcp.NewClient(&mcp.Implementation{Name: "demo-http-client", Version: "0.1.0"}, nil)

	t.Run("allow rules with matching scopes and claims", func(t *testing.T) {
		token := makeSignedJWTWithClaims(t, jwt.MapClaims{
			"tenant": "acme",
			"org": map[string]any{
				"departments": []any{"engineering", "operations"},
			},
		}, "sum")
		authHTTPClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &bearerTokenTransport{
				token: token,
			},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		requireSumToolResult(ctx, t, sess, 41, 1, "42")
	})

	t.Run("access denied with matching scopes and mismatching claims", func(t *testing.T) {
		token := makeSignedJWTWithClaims(t, jwt.MapClaims{
			"tenant": "acme",
			"org": map[string]any{
				"departments": []any{"hr"},
			},
		}, "sum")
		authHTTPClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &bearerTokenTransport{
				token: token,
			},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		_, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "mcp-backend-authorization__" + testmcp.ToolSum.Tool.Name,
			Arguments: testmcp.ToolSumArgs{A: 41, B: 1},
		})
		require.Error(t, err)
		errMsg := strings.ToLower(err.Error())
		require.Contains(t, errMsg, "forbidden", "unexpected error: %v", err)
	})

	t.Run("matching scopes, arguments, and headers", func(t *testing.T) {
		token := makeSignedJWT(t, "echo")
		authHTTPClient := &http.Client{
			Timeout:   10 * time.Second,
			Transport: &bearerTokenTransport{token: token, headers: map[string]string{"x-tenant-id": "t-123"}},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		const hello = "Hello, world!" // Should match the CEL expression on params + header
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "mcp-backend-authorization__" + testmcp.ToolEcho.Tool.Name,
			Arguments: testmcp.ToolEchoArgs{Text: hello},
		})
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.Len(t, res.Content, 1)
		txt, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		require.Equal(t, hello, txt.Text)
	})

	t.Run("matching scopes and mismatched arguments or headers", func(t *testing.T) {
		token := makeSignedJWT(t, "echo")
		authHTTPClient := &http.Client{
			Timeout:   10 * time.Second,
			Transport: &bearerTokenTransport{token: token, headers: map[string]string{"x-tenant-id": "t-123"}},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		const hello = "hello, world!" // Should fail CEL due to lowercase h
		_, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "mcp-backend-authorization__" + testmcp.ToolEcho.Tool.Name,
			Arguments: testmcp.ToolEchoArgs{Text: hello},
		})
		require.Error(t, err)
		errMsg := strings.ToLower(err.Error())
		require.Contains(t, errMsg, "forbidden", "unexpected error: %v", err)
	})

	t.Run("missing scopes fall back to deny", func(t *testing.T) {
		// Only includes the sum scope, so the echo tool should be denied.
		token := makeSignedJWT(t, "sum")
		authHTTPClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &bearerTokenTransport{
				token: token,
			},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		_, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "mcp-backend-authorization__" + testmcp.ToolEcho.Tool.Name,
			Arguments: testmcp.ToolEchoArgs{Text: "hello"},
		})
		require.Error(t, err)
		errMsg := strings.ToLower(err.Error())
		require.Contains(t, errMsg, "forbidden", "unexpected error: %v", err)
	})

	t.Run("WWW-Authenticate on insufficient scope", func(t *testing.T) {
		token := makeSignedJWT(t, "sum") // only sum scope; echo requires echo
		authHTTPClient := &http.Client{
			Timeout:   10 * time.Second,
			Transport: &bearerTokenTransport{token: token},
		}

		routeHeader := "default/mcp-route-authorization-default-deny"

		// First, initialize a session to obtain a session ID header.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		// Now call a tool that requires a missing scope to trigger insufficient_scope.
		reqBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcp-backend-authorization__echo","arguments":{"text":"Hello, world!"}}}`)
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), bytes.NewReader(reqBody))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-tenant-id", "t-123")
		req.Header.Set("mcp-session-id", sess.ID())
		req.Header.Set("x-ai-eg-mcp-route", routeHeader)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		require.Contains(t, wwwAuth, `error="insufficient_scope"`)
		require.Contains(t, wwwAuth, `scope="echo"`) // expected missing scope
		require.Contains(t, wwwAuth, `resource_metadata="https://foo.bar.com/.well-known/oauth-protected-resource/mcp"`)
	})

	t.Run("empty source matches all sources", func(t *testing.T) {
		token := makeSignedJWT(t, "not-a-real-scope")
		authHTTPClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &bearerTokenTransport{
				token: token,
			},
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "mcp-backend-authorization__" + testmcp.ToolCountDown.Tool.Name,
			Arguments: testmcp.ToolCountDownArgs{From: 3, Interval: "10ms"},
		})
		require.NoError(t, err)
		if res.IsError {
			json, _ := res.Content[0].MarshalJSON()
			t.Logf("error content: %s", json)
		}
		require.False(t, res.IsError)
	})
	t.Run("empty target matches all targets", func(t *testing.T) {
		token := makeSignedJWT(t, "all-access")
		authHTTPClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &bearerTokenTransport{
				token: token,
			},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)

		sess := requireConnectMCP(ctx, t, client, fmt.Sprintf("%s/mcp-authorization", fwd.Address()), authHTTPClient)
		t.Cleanup(func() {
			_ = sess.Close()
		})

		requireSumToolResult(ctx, t, sess, 41, 1, "42")
	})
}

func requireConnectMCP(ctx context.Context, t *testing.T, client *mcp.Client, endpoint string, httpClient *http.Client) *mcp.ClientSession {
	var sess *mcp.ClientSession
	require.Eventually(t, func() bool {
		var err error
		sess, err = client.Connect(
			ctx,
			&mcp.StreamableClientTransport{
				Endpoint:   endpoint,
				HTTPClient: httpClient,
			}, nil)
		if err != nil {
			t.Logf("failed to connect to MCP server: %v", err)
			return false
		}
		return true
	}, 30*time.Second, 100*time.Millisecond, "failed to connect to MCP server")
	return sess
}

func requireSumToolResult(ctx context.Context, t *testing.T, sess *mcp.ClientSession, a, b float64, expected string) {
	t.Helper()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mcp-backend-authorization__" + testmcp.ToolSum.Tool.Name,
		Arguments: testmcp.ToolSumArgs{A: a, B: b},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	txt, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, expected, txt.Text)
}

func makeSignedJWT(t *testing.T, scopes ...string) string {
	t.Helper()
	return makeSignedJWTWithClaims(t, jwt.MapClaims{}, scopes...)
}

func makeSignedJWTWithClaims(t *testing.T, claims jwt.MapClaims, scopes ...string) string {
	t.Helper()

	claims["iss"] = "https://auth-server.example.com"
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
		claims["exp"] = time.Now().Add(30 * time.Minute).Unix()
	}

	key := jwkPrivateKey(t)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = jwkPrivateKeyID
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

func jwkPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	mustBigInt := func(val string) *big.Int {
		bytes, err := base64.RawURLEncoding.DecodeString(val)
		require.NoError(t, err)
		return new(big.Int).SetBytes(bytes)
	}

	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{
			N: mustBigInt(jwkN),
			E: 65537, // AQAB
		},
		D: mustBigInt(jwkD),
		Primes: []*big.Int{
			mustBigInt(jwkP),
			mustBigInt(jwkQ),
		},
	}
	key.Precompute()
	return key
}

const (
	jwkPrivateKeyID = "8b267675394d7786f98ae29d8fddf905"
	jwkN            = "lQiREO6mH0tV14KjhSz02zNaJ1SExS1QjlGoMSbG2-NUeURmvnwg-eY95sDCFpWuH3_ovFyRBGTi0e3j79mrQhM53PZQys-gr-rYGz8LeHp8G6H3XmeDxTvyemhB6uiN4EZkjOo6xT2ipmPEN3u315xPCR60Pwac2E0t4vZGxtU4LGYatIFOYtUvDdMPBLfGMKVapHBzbx9Ll4INEic1fNrAIUVtOn6i3sxzGHj4iGWsMrEUIXDOWEHzioXgPuRjJDRjhHRuEeA9i_Y-a9hY92q6P-dcPnCDLNF3349WDyw7jIMlU6TLM8lQ5ci_TS_0avovXPNECsuOObtT78LJJKLg58ghTnqrihwvSccVgW4M43Ntv7TOAgOsRl-NKY7QQJIbkxemvh14-gzwA6LijMvb0Tjrh6NynKfCIO0ASsMp3K3uks4cYhBALLJ1E41V-cYqdwg0c6Jam0Y4OXxNv_0FfmcsOk8iXdroNgWjBs3KaObMiMvNOKHNWZ4PsEll"
	jwkD            = "CCv3lFeZmUautsntgGxmIqzOqTBrtUoWTC9zCvrm1YDCDYIwJgq1Xi5_P2tbWRSs_wIq90UWGIkVnNAv-uNTDiTyu8hvxqca1vqIDfpnfRwuOO-pGi6P3Z07XvXfg2tr-Bu0ALwJK-6EwB3hUO-CNZrXBJd_56LLr9qPhQ3e9KEVWu3gUfxzGV06HsZvYOFYxysR7MlTswiiwvR5FgE7YBS4izp80kPGV3QbbYCYlBYLGp52DZ1bWyCGo5ZSpPAt4Az9wdDTzJoTtflLymg8kZ-idQqk2_re214xQgeCuVAHujjC4r3GqSzbQGUqXicd-rbRLenyB22Ul8wyHqY8WtcFrGmHojK8b-W3M9m0-xYkMXmWcllYQuQ0LMP9K8Tl0uMpKsyd0AePItaWa_ft3dAzoBiUZA15X2_Nbbc9WbkmjN0Et8E1RWlrL5fzppbvLUl4mlSKHsLnwgmLx2OROjEnQsfzjMGxV2KhMZXzdvbRPTkaDtq3YT70ZiRIyvRD"
	jwkP            = "yO5hho-83vQQ3t7HeVeinZClemDazWT5T7f2ZVMigcuyUNQjC69tyMzJ3I_UN5nUCwpKCw5wY8uCeT82o1j-OJC3irxWjAPHkkbsYTNxRnk8ShJ2UFdu5a7MEF82-QuRKciAv11cebEpk5ggf-jQrtTY2yQru0fW0WZB8hz19XywhFQ_mVMMahNHfycfXT2BMaV0wiBFKY8FXKqb5cErsCodcZ_STvqOTykWBaA4AWmJFRqd4i4enpf-MhgtkQK3"
	jwkQ            = "veD3yFnEOZegVIpIxPqIsj7zazjKRn-io1s3KJxkgaz5ND1o1JwbxiLuUNL9ufkj6cPOVCEHRkjQ2GabHnA0NYci4qRHBWdHhCD7aisS2D60xZAiAVmNZlEGLxRS7gFnyD8uneLILFFMalvJdIccCXzN3c8vPlC_9FlEzaEyDUmWzT_1zZES2GpaYeC73fNg7h-mJ6m-96Y6Wwvlx6YlCRCIPLU7l4kA-jca37T0IMNhobWmg8u4yqvVaqdDhojD"
)
