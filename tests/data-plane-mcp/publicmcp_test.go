// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplanemcp

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/dataplaneenv"
)

func TestPublicMCPServers(t *testing.T) {
	mcpConfig := &filterapi.MCPConfig{
		BackendListenerAddr: "http://127.0.0.1:9999",
		Routes: []filterapi.MCPRoute{
			{
				Name: "test-route",
				Backends: []filterapi.MCPBackend{
					// TODO(nacx): Context7 started giving errors due to its certificate:
					// time=2026-02-20T12:14:12.555+01:00 level=ERROR msg="failed to create MCP session" component=mcp-proxy backend=context7
					// error="MCP initialize request failed with status code 503 and body=upstream connect error or disconnect/reset before headers.
					// reset reason: remote connection failure, transport failure reason: TLS_error:|268435563:SSL routines:OPENSSL_internal:BAD_ECC_CERT:TLS_error_end"
					//
					// Until those are resolved or figure out, we're just adding kiwi to verify that we can connect to a public MCP server and call a tool.
					// context7 can be enabled back when the certificate issue is sorted out.
					//
					// {Name: "context7"},
					{Name: "kiwi"},
				},
			},
		},
	}

	githubConfigured := false
	if githubAccessToken := os.Getenv("TEST_GITHUB_ACCESS_TOKEN"); githubAccessToken != "" {
		envoyConfig = strings.ReplaceAll(envoyConfig, "GITHUB_ACCESS_TOKEN_PLACEHOLDER", githubAccessToken)
		mcpConfig.Routes[0].Backends = append(mcpConfig.Routes[0].Backends,
			filterapi.MCPBackend{
				Name: "github",
				ToolSelector: &filterapi.MCPToolSelector{
					IncludeRegex: []string{".*pull_requests?.*", ".*issues?.*"},
				},
			},
		)
		githubConfigured = true
	}

	config, err := json.Marshal(filterapi.Config{MCPConfig: mcpConfig, Version: version.Parse()})
	require.NoError(t, err)

	env := dataplaneenv.StartTestEnvironment(t,
		func(_ testing.TB, _ io.Writer, _ map[string]int) {}, map[string]int{"backend_listener": 9999},
		string(config), nil, envoyConfig, true, true, 120*time.Second,
	)

	url := fmt.Sprintf("http://localhost:%d%s", env.EnvoyListenerPort(), defaultMCPPath)
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "public-mcp-client", Version: "0.1.0"}, &mcp.ClientOptions{})
	session, err := mcpClient.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: url,
	}, nil)
	require.NoError(t, err)
	// Intentionally not using t.Cleanup to close the session so that we can check to see if it closes cleanly.
	// If we do this in t.Cleanup, it will happen after the Envoy is terminating, and we won't see any valid "closure" error.
	defer func() { _ = session.Close() }()

	t.Run("tools/list", func(t *testing.T) {
		resp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)
		t.Logf("tools/list response: %+v", resp)
		var names []string
		var kiwiTools []string
		for _, tool := range resp.Tools {
			names = append(names, tool.Name)
			if strings.HasPrefix(tool.Name, "kiwi__") {
				kiwiTools = append(kiwiTools, tool.Name)
			}
		}
		// We intentionally assert only that expected tools are present rather than an
		// exact set: kiwi (and github) are third-party MCP servers that add or rename
		// tools upstream without notice, and pinning the exact set caused recurring CI
		// churn (see the revert of #2305).
		require.NotEmpty(t, kiwiTools, "expected at least one tool with prefix %q", "kiwi__")
		t.Logf("discovered kiwi tools: %v", kiwiTools)

		if githubConfigured {
			githubExps := []string{
				"github__issue_read",
				"github__pull_request_read",
				"github__list_issues",
				"github__list_pull_requests",
				"github__search_issues",
				"github__search_pull_requests",
			}
			for _, exp := range githubExps {
				require.Contains(t, names, exp, "expected tool not found: %s", exp)
			}
		}
	})

	t.Run("tool calls", func(t *testing.T) {
		// Discover a kiwi flight search tool dynamically since Kiwi may rename tools upstream.
		listResp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)
		kiwiFlightTool := findKiwiFlightTool(listResp.Tools)

		type callToolTest struct {
			toolName string
			params   map[string]any
			// expectResults asserts the tool returned a non-empty result set. Kiwi
			// returns isError=false with zero itineraries for e.g. past dates, so
			// this is what makes searching a future (tomorrow) date meaningful.
			expectResults bool
		}
		tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("02/01/2006")
		dayAfter := time.Now().UTC().AddDate(0, 0, 2).Format("02/01/2006")

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
				require.False(t, resp.IsError)

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

// findKiwiFlightTool returns the first Kiwi flight-search tool, or "" if none is
// present. Kiwi may rename tools upstream, so we match by prefix + substring
// rather than an exact name.
func findKiwiFlightTool(tools []*mcp.Tool) string {
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "kiwi__") && strings.Contains(tool.Name, "flight") {
			return tool.Name
		}
	}
	return ""
}
