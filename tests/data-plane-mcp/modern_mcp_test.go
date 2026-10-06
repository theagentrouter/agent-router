// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplanemcp

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// TestModernMCP exercises the modern (2026-07-28) stateless MCP protocol path
// through the gateway with modern backend servers.
func TestModernMCP(t *testing.T) {
	env := requireNewModernMCPEnv(t, 1200*time.Second, defaultMCPPath)
	cli := env.modernCli

	t.Run("ServerDiscover", func(t *testing.T) {
		result := cli.serverDiscover(t)
		versions, ok := result["supportedVersions"].([]any)
		require.True(t, ok, "supportedVersions must be an array")
		require.NotEmpty(t, versions)
		caps, ok := result["capabilities"].(map[string]any)
		require.True(t, ok, "capabilities must be a map")
		require.NotNil(t, caps["tools"])
	})

	t.Run("ListTools", func(t *testing.T) {
		result := cli.listTools(t)
		var names []string
		for _, tool := range result.Tools {
			names = append(names, tool.Name)
		}
		// The gateway prefixes tool names with the backend name.
		require.Contains(t, names, "dumb-mcp-backend__"+testmcp.ToolDumbEcho.Tool.Name)
		require.Contains(t, names, defaultMCPBackendResourcePrefix+testmcp.ToolEcho.Tool.Name)
		require.Contains(t, names, defaultMCPBackendResourcePrefix+testmcp.ToolSum.Tool.Name)
		require.Contains(t, names, defaultMCPBackendResourcePrefix+testmcp.ToolError.Tool.Name)
	})

	t.Run("ToolCall/Echo", func(t *testing.T) {
		const helloText = "hello Modern MCP 👋"
		result := cli.callTool(t, defaultMCPBackendResourcePrefix+testmcp.ToolEcho.Tool.Name, testmcp.ToolEchoArgs{Text: helloText})
		require.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		require.Equal(t, helloText, result.Content[0].Text)
	})

	t.Run("ToolCall/Sum", func(t *testing.T) {
		result := cli.callTool(t, defaultMCPBackendResourcePrefix+testmcp.ToolSum.Tool.Name, testmcp.ToolSumArgs{A: 41, B: 1})
		require.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		require.Equal(t, "42", result.Content[0].Text)
	})

	t.Run("ToolCall/DumbEcho", func(t *testing.T) {
		const helloText = "hello Modern MCP 👋"
		result := cli.callTool(t, "dumb-mcp-backend__"+testmcp.ToolDumbEcho.Tool.Name, testmcp.ToolEchoArgs{Text: helloText})
		require.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		require.Equal(t, "dumb echo: "+helloText, result.Content[0].Text)
	})

	t.Run("ToolCall/Error", func(t *testing.T) {
		const errMsg = "tool error"
		result := cli.callTool(t, defaultMCPBackendResourcePrefix+testmcp.ToolError.Tool.Name, testmcp.ToolErrorArgs{Error: errMsg})
		require.True(t, result.IsError)
		require.Len(t, result.Content, 1)
		require.Equal(t, errMsg, result.Content[0].Text)
	})

	t.Run("ListResources", func(t *testing.T) {
		result := cli.listResources(t)
		require.Len(t, result.Resources, 2)
		urisByName := map[string]string{}
		for _, r := range result.Resources {
			urisByName[r.Name] = r.URI
		}
		require.Equal(t, defaultMCPBackendResourceURIPrefix+testmcp.DummyResource.URI,
			urisByName[defaultMCPBackendResourcePrefix+testmcp.DummyResource.Name])
		require.Equal(t, defaultMCPBackendUIRendererURI,
			urisByName[defaultMCPBackendResourcePrefix+testmcp.UIRendererResource.Name])
	})

	t.Run("ReadResource", func(t *testing.T) {
		result := cli.readResource(t, defaultMCPBackendResourceURIPrefix+"file:///dummy.txt")
		require.Len(t, result.Contents, 1)
		require.Equal(t, defaultMCPBackendResourceURIPrefix+testmcp.DummyResource.URI, result.Contents[0].URI)
		require.Equal(t, testmcp.DummyResource.MIMEType, result.Contents[0].MIMEType)
		require.Equal(t, "dummy", result.Contents[0].Blob)
	})

	t.Run("ReadResourceNotFound", func(t *testing.T) {
		// Gateway currently surfaces backend JSON-RPC errors as plain-text HTTP 500
		// ("call to <backend> failed: backend error: ..."), so accept either a
		// JSON-RPC error body or that plain-text gateway error.
		id := cli.reqIDSeq.Add(1)
		params := map[string]any{"uri": defaultMCPBackendResourceURIPrefix + "file:///notfound.txt"}
		paramsRaw, err := json.Marshal(params)
		require.NoError(t, err)
		paramsRaw = injectModernMeta(paramsRaw)
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "resources/read",
			"params":  json.RawMessage(paramsRaw),
		}
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, cli.endpoint, bytes.NewReader(encoded))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set(modernVersionHeader, modernProtocolVersion)
		req.Header.Set(modernMethodHeader, "resources/read")
		resp, err := cli.httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		msg := string(respBody)
		require.True(t,
			strings.Contains(msg, "Resource not found") || strings.Contains(msg, "call to") && strings.Contains(msg, "failed"),
			"expected not-found error, got: %s", msg)
	})

	t.Run("ListResourceTemplates", func(t *testing.T) {
		result := cli.listResourceTemplates(t)
		require.Len(t, result.ResourceTemplates, 1)
		require.Equal(t, defaultMCPBackendResourcePrefix+testmcp.DummyResourceTemplate.Name, result.ResourceTemplates[0].Name)
		require.Equal(t, defaultMCPBackendResourceURIPrefix+testmcp.DummyResourceTemplate.URITemplate, result.ResourceTemplates[0].URITemplate)
		require.Equal(t, testmcp.DummyResourceTemplate.Description, result.ResourceTemplates[0].Description)
	})

	t.Run("ListPrompts", func(t *testing.T) {
		result := cli.listPrompts(t)
		require.Len(t, result.Prompts, 1)
		require.Equal(t, defaultMCPBackendResourcePrefix+testmcp.CodeReviewPrompt.Name, result.Prompts[0].Name)
		require.Equal(t, testmcp.CodeReviewPrompt.Description, result.Prompts[0].Description)
	})

	t.Run("GetPrompt", func(t *testing.T) {
		result := cli.getPrompt(t, defaultMCPBackendResourcePrefix+"code_review", map[string]string{"Code": "1+1"})
		require.Equal(t, "Code review prompt", result.Description)
		require.Len(t, result.Messages, 1)
		require.Equal(t, "user", result.Messages[0].Role)
		require.Contains(t, result.Messages[0].Content.Text, "Please review the following code: 1+1")
	})

	t.Run("Complete/Prompt", func(t *testing.T) {
		result := cli.complete(t, map[string]any{
			"argument": map[string]any{"name": "language", "value": "py"},
			"ref":      map[string]any{"type": "ref/prompt", "name": defaultMCPBackendResourcePrefix + "code_review"},
		})
		require.Equal(t, []string{"python", "pytorch", "pyside"}, result.Completion.Values)
	})

	t.Run("Complete/Resource", func(t *testing.T) {
		result := cli.complete(t, map[string]any{
			"argument": map[string]any{"name": "id", "value": "23"},
			"ref":      map[string]any{"type": "ref/resource", "uri": defaultMCPBackendResourceURIPrefix + "file://results-{id}.txt"},
		})
		require.Equal(t, []string{"file://results-23.txt"}, result.Completion.Values)
	})

	t.Run("Metrics/MethodCount", func(t *testing.T) {
		// Verify metrics are being recorded for modern requests.
		require.Eventually(t, func() bool {
			val, err := getCounterMetricByNameLabels(env.extProcMetricsURL, retrieveMetricsTime, "mcp_method_count_total", map[string]string{
				"mcp_method_name": "tools/call",
			})
			if err != nil {
				t.Log("metric not yet available:", err)
				return false
			}
			return val > 0
		}, retrieveMetricsTime, retrieveMetricsTick, "mcp_method_count_total for tools/call should be > 0")
	})

	t.Run("Metrics/ServerDiscover", func(t *testing.T) {
		require.Eventually(t, func() bool {
			val, err := getCounterMetricByNameLabels(env.extProcMetricsURL, retrieveMetricsTime, "mcp_method_count_total", map[string]string{
				"mcp_method_name": "server/discover",
			})
			if err != nil {
				t.Log("metric not yet available:", err)
				return false
			}
			return val > 0
		}, retrieveMetricsTime, retrieveMetricsTick, "mcp_method_count_total for server/discover should be > 0")
	})

	t.Run("Tracing/ToolCallSpan", func(t *testing.T) {
		// Drain spans left by earlier subtests so we observe only this call.
		drainSpans(env.collector)
		cli.callTool(t, defaultMCPBackendResourcePrefix+testmcp.ToolEcho.Tool.Name, testmcp.ToolEchoArgs{Text: "span test"})
		span := takeSpanNamed(t, env.collector, "CallTool")
		require.NotNil(t, span, "expected a span from the tool call")

		// Verify span has the expected tool name attribute.
		found := false
		for _, attr := range span.Attributes {
			if attr.Key == "mcp.tool.name" {
				require.Equal(t, defaultMCPBackendResourcePrefix+testmcp.ToolEcho.Tool.Name, attr.Value.GetStringValue())
				found = true
			}
		}
		require.True(t, found, "mcp.tool.name attribute not found on span")

		// Default (legacy) tracing vocabulary hardcodes mcp.protocol.version to
		// 2025-06-18 for dashboard compatibility; modern requests still use it
		// until AI_GATEWAY_TRACING_SEMCONV selects the OTel vocabulary.
		for _, attr := range span.Attributes {
			if attr.Key == "mcp.protocol.version" {
				require.Equal(t, "2025-06-18", attr.Value.GetStringValue())
			}
		}
	})
}

// TestModernMCP_RejectedLegacyMethods verifies the gateway rejects legacy-only
// methods (initialize, ping, logging/setLevel) on the modern path.
func TestModernMCP_RejectedLegacyMethods(t *testing.T) {
	env := requireNewModernMCPEnv(t, 60*time.Second, defaultMCPPath)
	cli := env.modernCli

	for _, method := range []string{"initialize", "ping", "logging/setLevel"} {
		t.Run(fmt.Sprintf("rejected/%s", method), func(t *testing.T) {
			// The gateway should return an HTTP error (not 200) for legacy methods on modern path.
			id := cli.reqIDSeq.Add(1)
			paramsRaw := injectModernMeta([]byte(`{}`))
			body := map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"method":  method,
				"params":  paramsRaw,
			}
			encoded, err := jsonMarshal(body)
			require.NoError(t, err)

			req, err := newModernHTTPRequest(cli.endpoint, method, encoded)
			require.NoError(t, err)
			resp, err := cli.httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Modern path rejects legacy methods with non-200 status.
			require.NotEqual(t, 200, resp.StatusCode, "expected rejection for %s on modern path", method)
		})
	}
}

// jsonMarshal is a local alias to avoid import cycle issues.
var jsonMarshal = jsonMarshalImpl

func jsonMarshalImpl(v any) ([]byte, error) {
	return json.Marshal(v)
}

func newModernHTTPRequest(endpoint, method string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(modernVersionHeader, modernProtocolVersion)
	req.Header.Set(modernMethodHeader, method)
	return req, nil
}
