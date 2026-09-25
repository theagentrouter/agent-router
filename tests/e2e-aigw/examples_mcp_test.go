// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2emcp

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

var (
	examplesDir = path.Join(internaltesting.FindProjectRoot(), "examples", "mcp")

	// Adjust these as services update, as they can be added, removed or renamed

	allNonGithubTools = []string{
		// TODO(nacx): Context7 started giving errors due to its certificate:
		// time=2026-02-20T12:14:12.555+01:00 level=ERROR msg="failed to create MCP session" component=mcp-proxy backend=context7
		// error="MCP initialize request failed with status code 503 and body=upstream connect error or disconnect/reset before headers.
		// reset reason: remote connection failure, transport failure reason: TLS_error:|268435563:SSL routines:OPENSSL_internal:BAD_ECC_CERT:TLS_error_end"
		//
		// Until those are resolved or figure out, we're just adding kiwi to verify that we can connect to a public MCP server and call a tool.
		// context7 can be enabled back when the certificate issue is sorted out.
		//
		// "context7__query-docs",
		// "context7__resolve-library-id",
		"kiwi__feedback-to-devs",
		"kiwi__search-flight",
	}
)

func TestMCP_standalone(t *testing.T) {
	ght := os.Getenv("TEST_GITHUB_ACCESS_TOKEN")
	githubConfigured := ght != ""
	if githubConfigured {
		t.Setenv("GITHUB_ACCESS_TOKEN", ght)
	}

	exampleYaml := path.Join(examplesDir, "mcp_example.yaml")
	startAIGWCLI(t, aigwBin, nil, "run", "--debug", exampleYaml)

	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", 1975)
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "public-mcp-client", Version: "0.1.0"}, &mcp.ClientOptions{})
	session, err := mcpClient.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: url,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	t.Run("tools/list", func(t *testing.T) {
		resp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)

		var actualNames []string
		for _, tool := range resp.Tools {
			actualNames = append(actualNames, tool.Name)
		}
		sort.Strings(actualNames)

		// GitHub tool listing is flaky, so only check non-GitHub tools here.
		for _, toolName := range allNonGithubTools {
			require.Contains(t, actualNames, toolName)
		}
	})

	t.Run("tool calls", func(t *testing.T) {
		type callToolTest struct {
			toolName string
			params   map[string]any
		}
		tests := []callToolTest{
			// {
			// 	toolName: "context7__resolve-library-id",
			// 	params: map[string]any{
			// 		"libraryName": "envoyproxy/ai-gateway",
			// 		"query":       "how can I route to an LLM bakend",
			// 	},
			// },
			// {
			// 	toolName: "context7__query-docs",
			// 	params: map[string]any{
			// 		"libraryId": "/envoyproxy/ai-gateway",
			// 		"query":     "how can I route to an LLM bakend",
			// 	},
			// },
			{
				toolName: "kiwi__search-flight",
				params: map[string]any{
					"flyFrom":                "LAX",
					"flyTo":                  "HND",
					"departureDate":          "01/12/2026",
					"departureDateFlexRange": 1,
					"returnDate":             "02/12/2026",
					"returnDateFlexRange":    1,
					"passengers": map[string]any{
						"adults":   1,
						"children": 0,
						"infants":  0,
					},
					"cabinClass": "M",
					"sort":       "date",
					"curr":       "USD",
					"locale":     "en",
				},
			},
		}
		if githubConfigured {
			tests = append(tests, callToolTest{
				toolName: "github__pull_request_read",
				params: map[string]any{
					"owner":      "envoyproxy",
					"repo":       "ai-gateway",
					"method":     "get",
					"pullNumber": 1,
				},
			})
		}
		for _, tc := range tests {
			t.Run(tc.toolName, func(t *testing.T) {
				t.Parallel()
				resp, err := session.CallTool(t.Context(), &mcp.CallToolParams{
					Name:      tc.toolName,
					Arguments: tc.params,
				})
				require.NoError(t, err)
				require.False(t, resp.IsError, "[[response]]\n%v", resp)
			})
		}
	})
}

// authTransport is an http.RoundTripper that adds Authorization header to requests.
type authTransport struct {
	token string
	base  http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

func TestMCP_standalone_oauth(t *testing.T) {
	startAIGWCLI(t, aigwBin, nil, "run", "--debug", path.Join(examplesDir, "mcp_oauth_example.yaml"))

	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", 1975)

	t.Run("fail to connect to MCP server without token", func(t *testing.T) {
		mcpClient := mcp.NewClient(&mcp.Implementation{Name: "public-mcp-client", Version: "0.1.0"}, &mcp.ClientOptions{})
		session, err := mcpClient.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint: url,
		}, nil)
		t.Cleanup(func() {
			if session != nil {
				_ = session.Close()
			}
		})
		// Should fail to connect due to missing authentication.
		require.Error(t, err)
		t.Logf("got expected error when connecting without token: %v", err)
	})

	t.Run("connect to MCP server with token", func(t *testing.T) {
		validToken := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCIsImtpZCI6ImI1MjBiM2MyYzRiZDc1YTEwZTljZWJjOTU3NjkzM2RjIn0.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMiwiaXNzIjoiaHR0cHM6Ly9hdXRoLXNlcnZlci5leGFtcGxlLmNvbSJ9.Ri7Dglgpp_BJV-T6pCwvp6aj6JE-vd0sk_6teVp5SkayZalI1FwM3xNcCAhuKd5AswynXC0tTvqBHdo3G7l3P-__KSua0YcwzOe3VRh0cVKaV0NDC8hVivDOf9GET_YT5IyxT1HQDzc9M9s77nStTSva_u4QDHr_jjlulVVisy77aQIkbG4GP_K4OJYr3fnZVOOTOPgA55-xm-VPRn2MlkT1Y9z6pFvyeZTl-xj2OY4E-d5EMETRUWFQMUs5AQpzTZtqnzfrdmRZ2haSjkwLej7iZ2uirXMaFnc0qMCVYhROHRKMny6akP6u77cZ9QdFwatltL0sQceMCqAXZbFyLBlKXI3uEOLOFCnnUMWnVqsQjLO4vODnw2ih4TeTArEi2maLadGk5zZTCsw3wKEdLdO89dtfVXeWvyd3xYDgDoPNhyJDGl5gU5dU4hUg5_4uCb8Sg-pW4xYF64K_Oa0iNS3z1-zYtyZ1B4Ftu2pbxmsHLkxp3SxoOzueFft8m0q3" //nolint:gosec // Test JWT token

		// Create HTTP client with Authorization header.
		authHTTPClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &authTransport{
				token: validToken,
				base:  http.DefaultTransport,
			},
		}
		// Create an MCP client and connect to the server over Streamable HTTP.
		mcpClient := mcp.NewClient(&mcp.Implementation{Name: "public-mcp-client", Version: "0.1.0"}, &mcp.ClientOptions{})
		session, err := mcpClient.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint: url,
			// Use HTTP client that adds Authorization header.
			HTTPClient: authHTTPClient,
		}, nil)

		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })

		// List tools to verify authenticated connection works.
		resp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)

		var actualNames []string
		for _, tool := range resp.Tools {
			actualNames = append(actualNames, tool.Name)
		}
		sort.Strings(actualNames)

		require.Equal(t, allNonGithubTools, actualNames)
	})
}
