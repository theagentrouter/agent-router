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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	modernProtocolVersion = "2026-07-28"
	modernMethodHeader    = "Mcp-Method"
	modernVersionHeader   = "Mcp-Protocol-Version"
	modernNameHeader      = "Mcp-Name"
)

// modernClient is a stateless MCP client that speaks the 2026-07-28 protocol.
// It sends each request as a standalone POST with modern headers.
type modernClient struct {
	endpoint   string
	httpClient *http.Client
	reqIDSeq   atomic.Int64
}

func newModernClient(endpoint string) *modernClient {
	return &modernClient{
		endpoint:   endpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// modernResult wraps a raw JSON-RPC result for deserialization by callers.
type modernResult struct {
	raw json.RawMessage
}

func (r *modernResult) unmarshal(dst any) error {
	return json.Unmarshal(r.raw, dst)
}

// callMethod sends a modern JSON-RPC request and returns the raw result.
func (c *modernClient) callMethod(t *testing.T, method string, name string, params any) *modernResult {
	t.Helper()
	id := c.reqIDSeq.Add(1)

	var paramsRaw json.RawMessage
	if params != nil {
		var err error
		paramsRaw, err = json.Marshal(params)
		require.NoError(t, err)
	} else {
		paramsRaw = json.RawMessage(`{}`)
	}

	// Inject _meta with protocol version and client capabilities.
	paramsRaw = injectModernMeta(paramsRaw)

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  paramsRaw,
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(encoded))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(modernVersionHeader, modernProtocolVersion)
	req.Header.Set(modernMethodHeader, method)
	if name != "" {
		req.Header.Set(modernNameHeader, name)
	}

	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "unexpected status for %s: %s", method, string(respBody))

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(respBody, &envelope))
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error for %s: code=%d msg=%s", method, envelope.Error.Code, envelope.Error.Message)
	}
	return &modernResult{raw: envelope.Result}
}

// injectModernMeta injects _meta with protocolVersion and clientCapabilities into params.
func injectModernMeta(params json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		obj = map[string]json.RawMessage{}
	}
	meta := fmt.Sprintf(`{
		"io.modelcontextprotocol/protocolVersion": "%s",
		"io.modelcontextprotocol/clientInfo": {"name":"modern-test-client","version":"1.0.0"},
		"io.modelcontextprotocol/clientCapabilities": {}
	}`, modernProtocolVersion)
	obj["_meta"] = json.RawMessage(meta)
	result, _ := json.Marshal(obj)
	return result
}

// --- Convenience wrappers ---

func (c *modernClient) serverDiscover(t *testing.T) map[string]any {
	t.Helper()
	res := c.callMethod(t, "server/discover", "", nil)
	var out map[string]any
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernToolsListResult struct {
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
}

func (c *modernClient) listTools(t *testing.T) modernToolsListResult {
	t.Helper()
	res := c.callMethod(t, "tools/list", "", nil)
	var out modernToolsListResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernCallToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (c *modernClient) callTool(t *testing.T, name string, args any) modernCallToolResult {
	t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	res := c.callMethod(t, "tools/call", name, params)
	var out modernCallToolResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernResourcesListResult struct {
	Resources []struct {
		Name     string `json:"name"`
		URI      string `json:"uri"`
		MIMEType string `json:"mimeType"`
	} `json:"resources"`
}

func (c *modernClient) listResources(t *testing.T) modernResourcesListResult {
	t.Helper()
	res := c.callMethod(t, "resources/list", "", nil)
	var out modernResourcesListResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernReadResourceResult struct {
	Contents []struct {
		URI      string `json:"uri"`
		MIMEType string `json:"mimeType"`
		Blob     string `json:"blob"`
	} `json:"contents"`
}

func (c *modernClient) readResource(t *testing.T, uri string) modernReadResourceResult {
	t.Helper()
	res := c.callMethod(t, "resources/read", "", map[string]any{"uri": uri})
	var out modernReadResourceResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernResourceTemplatesListResult struct {
	ResourceTemplates []struct {
		Name        string `json:"name"`
		URITemplate string `json:"uriTemplate"`
		Description string `json:"description"`
	} `json:"resourceTemplates"`
}

func (c *modernClient) listResourceTemplates(t *testing.T) modernResourceTemplatesListResult {
	t.Helper()
	res := c.callMethod(t, "resources/templates/list", "", nil)
	var out modernResourceTemplatesListResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernPromptsListResult struct {
	Prompts []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"prompts"`
}

func (c *modernClient) listPrompts(t *testing.T) modernPromptsListResult {
	t.Helper()
	res := c.callMethod(t, "prompts/list", "", nil)
	var out modernPromptsListResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernGetPromptResult struct {
	Description string `json:"description"`
	Messages    []struct {
		Role    string `json:"role"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

func (c *modernClient) getPrompt(t *testing.T, name string, args map[string]string) modernGetPromptResult {
	t.Helper()
	res := c.callMethod(t, "prompts/get", name, map[string]any{"name": name, "arguments": args})
	var out modernGetPromptResult
	require.NoError(t, res.unmarshal(&out))
	return out
}

type modernCompleteResult struct {
	Completion struct {
		Values []string `json:"values"`
	} `json:"completion"`
}

func (c *modernClient) complete(t *testing.T, params map[string]any) modernCompleteResult {
	t.Helper()
	res := c.callMethod(t, "completion/complete", "", params)
	var out modernCompleteResult
	require.NoError(t, res.unmarshal(&out))
	return out
}
