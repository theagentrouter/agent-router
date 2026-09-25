// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// TestMCPPrefixMode tests per-backend prefixMode behaviour:
//   - Never-only: tools exposed as bare names, tools/call routes correctly.
//   - Mixed: Never and Always backends coexist on one route.
//   - Collision: two Never backends with the same tool name → NotAccepted.
func TestMCPPrefixMode(t *testing.T) {
	// Apply the base mcp_route.yaml first: it creates the mcp-gateway Gateway and the
	// mcp-backend Deployment that this test's routes and Services reuse. Applying it here
	// (rather than relying on TestMCP having run) makes this test independent of test
	// execution order — Go runs test files alphabetically, so this file runs before
	// mcp_route_test.go and cannot assume that gateway already exists.
	const baseManifest = "testdata/mcp_route.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), baseManifest))

	const manifest = "testdata/mcp_route_prefix_mode.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	client := mcp.NewClient(&mcp.Implementation{Name: "prefix-mode-test", Version: "0.1.0"}, nil)

	t.Run("never mode: bare tool names, call routes correctly", func(t *testing.T) {
		sess := requireMCPConnect(t, client, fwd.Address(), "/mcp/prefix-mode-never")
		t.Cleanup(func() { _ = sess.Close() })

		tools, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)

		names := toolNames(tools.Tools)
		// Bare names — no backend prefix.
		assert.Contains(t, names, testmcp.ToolEcho.Tool.Name, "echo tool should be bare (no prefix)")
		assert.Contains(t, names, testmcp.ToolSum.Tool.Name, "sum tool should be bare (no prefix)")
		for _, n := range names {
			assert.NotContains(t, n, "__", "no tool should be prefixed in Never mode")
		}

		// tools/call must route to the correct backend via the bare name.
		const hello = "hello prefix-mode"
		res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      testmcp.ToolEcho.Tool.Name,
			Arguments: testmcp.ToolEchoArgs{Text: hello},
		})
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.Len(t, res.Content, 1)
		txt, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, hello, txt.Text)
	})

	t.Run("never mode: bare prompt name, get routes correctly on a fresh connection", func(t *testing.T) {
		// Use a brand-new session (separate from the tools/list call above) to
		// verify that bare-name resolution for Never mode does not depend on
		// having previously seen a prompts/list response on *this* session —
		// it must be resolvable from route-level static config alone.
		sess := requireMCPConnect(t, client, fwd.Address(), "/mcp/prefix-mode-never")
		t.Cleanup(func() { _ = sess.Close() })

		prompts, err := sess.ListPrompts(t.Context(), &mcp.ListPromptsParams{})
		require.NoError(t, err)
		names := promptNames(prompts.Prompts)
		assert.Contains(t, names, testmcp.CodeReviewPrompt.Name, "code_review prompt should be bare (no prefix)")
		for _, n := range names {
			assert.NotContains(t, n, "__", "no prompt should be prefixed in Never mode")
		}

		// prompts/get on a fresh session must still resolve the bare name via
		// the route's static neverModePromptIndex, without relying on any
		// prior prompts/list call on this session.
		fresh := requireMCPConnect(t, client, fwd.Address(), "/mcp/prefix-mode-never")
		t.Cleanup(func() { _ = fresh.Close() })
		res, err := fresh.GetPrompt(t.Context(), &mcp.GetPromptParams{
			Name:      testmcp.CodeReviewPrompt.Name,
			Arguments: map[string]string{"Code": "print('hi')"},
		})
		require.NoError(t, err)
		require.Len(t, res.Messages, 1)
	})

	t.Run("mixed mode: Never and Always backends coexist", func(t *testing.T) {
		sess := requireMCPConnect(t, client, fwd.Address(), "/mcp/prefix-mode-mixed")
		t.Cleanup(func() { _ = sess.Close() })

		tools, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)

		names := toolNames(tools.Tools)
		// backend-a (Never) → bare names.
		assert.Contains(t, names, testmcp.ToolEcho.Tool.Name)
		assert.Contains(t, names, testmcp.ToolSum.Tool.Name)
		// backend-b (Always) → prefixed names.
		assert.Contains(t, names, "mcp-backend-b__"+testmcp.ToolEcho.Tool.Name)
		assert.Contains(t, names, "mcp-backend-b__"+testmcp.ToolSum.Tool.Name)

		// Calling the bare-name tool routes to backend-a.
		res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      testmcp.ToolSum.Tool.Name,
			Arguments: testmcp.ToolSumArgs{A: 20, B: 22},
		})
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.Len(t, res.Content, 1)
		txt, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "42", txt.Text)

		// Calling the prefixed tool routes to backend-b.
		res, err = sess.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "mcp-backend-b__" + testmcp.ToolSum.Tool.Name,
			Arguments: testmcp.ToolSumArgs{A: 20, B: 22},
		})
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.Len(t, res.Content, 1)
		txt, ok = res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "42", txt.Text)

		// backend-a (Never, promptSelector.include: [code_review]) → bare prompt name.
		// backend-b (Always) → prefixed prompt name.
		prompts, err := sess.ListPrompts(t.Context(), &mcp.ListPromptsParams{})
		require.NoError(t, err)
		pnames := promptNames(prompts.Prompts)
		assert.Contains(t, pnames, testmcp.CodeReviewPrompt.Name)
		assert.Contains(t, pnames, "mcp-backend-b__"+testmcp.CodeReviewPrompt.Name)
	})

	t.Run("collision: two Never backends with same tool → NotAccepted", func(t *testing.T) {
		// The collision route should be marked NotAccepted by the controller.
		var notAcceptedMsg string
		require.Eventually(t, func() bool {
			var route aigv1b1.MCPRoute
			out, err := exec.CommandContext(t.Context(), "kubectl", "get", "mcproute", // #nosec G204
				"mcp-prefix-mode-collision", "-n", "default", "-o", "json").Output()
			if err != nil {
				t.Logf("failed to get mcproute: %v", err)
				return false
			}
			if err := json.Unmarshal(out, &route); err != nil {
				t.Logf("failed to unmarshal mcproute: %v", err)
				return false
			}
			for _, cond := range route.Status.Conditions {
				if cond.Type == aigv1b1.ConditionTypeNotAccepted {
					notAcceptedMsg = cond.Message
					return true
				}
			}
			return false
		}, 30*time.Second, 200*time.Millisecond, "collision route should be NotAccepted")

		// Verify error message mentions the duplicate tool.
		assert.Contains(t, notAcceptedMsg, "echo", "error should name the duplicate tool")
		assert.Contains(t, notAcceptedMsg, "mcp-backend-a", "error should name first backend")
		assert.Contains(t, notAcceptedMsg, "mcp-backend-b", "error should name second backend")
	})
}

func requireMCPConnect(t *testing.T, c *mcp.Client, addr, path string) *mcp.ClientSession {
	t.Helper()
	var sess *mcp.ClientSession
	require.Eventually(t, func() bool {
		var err error
		sess, err = c.Connect(
			t.Context(),
			&mcp.StreamableClientTransport{
				Endpoint: fmt.Sprintf("%s%s", addr, path),
			}, nil)
		if err != nil {
			t.Logf("connect %s: %v", path, err)
			return false
		}
		return true
	}, 30*time.Second, 100*time.Millisecond)
	return sess
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}

func promptNames(prompts []*mcp.Prompt) []string {
	names := make([]string, len(prompts))
	for i, p := range prompts {
		names[i] = p.Name
	}
	return names
}
