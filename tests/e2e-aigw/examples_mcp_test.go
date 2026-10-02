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
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

var (
	examplesDir = path.Join(internaltesting.FindProjectRoot(), "examples", "mcp")

	// kiwiToolPrefix is used to dynamically discover Kiwi tools instead of
	// hardcoding names that may change upstream without notice.
	kiwiToolPrefix = "kiwi__"
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

		var kiwiTools []string
		for _, tool := range resp.Tools {
			if strings.HasPrefix(tool.Name, kiwiToolPrefix) {
				kiwiTools = append(kiwiTools, tool.Name)
			}
		}
		// We intentionally assert only that expected tools are present rather than an
		// exact set: kiwi (and github) are third-party MCP servers that add or rename
		// tools upstream without notice, and pinning the exact set caused recurring CI
		// churn (see the revert of #2305).
		require.NotEmpty(t, kiwiTools, "expected at least one tool with prefix %q", kiwiToolPrefix)
		t.Logf("discovered kiwi tools: %v", kiwiTools)
	})

	t.Run("tool calls", func(t *testing.T) {
		// Discover a kiwi flight search tool dynamically since Kiwi may rename tools upstream.
		listResp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)
		kiwiFlightTool := findKiwiFlightTool(listResp.Tools)

		tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("02/01/2006")
		dayAfter := time.Now().UTC().AddDate(0, 0, 2).Format("02/01/2006")

		type callToolTest struct {
			toolName string
			params   map[string]any
			// expectResults asserts the tool returned a non-empty result set. Kiwi
			// returns isError=false with zero itineraries for e.g. past dates, so
			// this is what makes searching a future (tomorrow) date meaningful.
			expectResults bool
		}
		var tests []callToolTest
		if kiwiFlightTool != "" {
			t.Logf("discovered kiwi flight tool: %s", kiwiFlightTool)
			tests = append(tests, callToolTest{
				toolName:      kiwiFlightTool,
				expectResults: true,
				params: map[string]any{
					"flyFrom":                "LAX",
					"flyTo":                  "HND",
					"departureDate":          tomorrow,
					"departureDateFlexRange": 1,
					"returnDate":             dayAfter,
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
			})
		} else {
			t.Log("no kiwi flight tool found, skipping kiwi tool call test")
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

				if tc.expectResults {
					require.NotEmpty(t, resp.Content)
					text, ok := resp.Content[0].(*mcp.TextContent)
					require.True(t, ok, "expected text content, got %T", resp.Content[0])
					var result struct {
						ResultsCount int `json:"resultsCount"`
					}
					require.NoError(t, json.Unmarshal([]byte(text.Text), &result))
					require.NotZero(t, result.ResultsCount, "expected non-empty results: %s", text.Text)
				}
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

		var kiwiTools []string
		for _, tool := range resp.Tools {
			if strings.HasPrefix(tool.Name, kiwiToolPrefix) {
				kiwiTools = append(kiwiTools, tool.Name)
			}
		}
		require.NotEmpty(t, kiwiTools, "expected at least one tool with prefix %q", kiwiToolPrefix)
		t.Logf("discovered kiwi tools via oauth: %v", kiwiTools)
	})
}

// findKiwiFlightTool returns the first Kiwi flight-search tool, or "" if none is
// present. Kiwi may rename tools upstream, so we match by prefix + substring
// rather than an exact name.
func findKiwiFlightTool(tools []*mcp.Tool) string {
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, kiwiToolPrefix) && strings.Contains(tool.Name, "flight") {
			return tool.Name
		}
	}
	return ""
}
