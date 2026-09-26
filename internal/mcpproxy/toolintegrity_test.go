// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestCompileToolIntegrity(t *testing.T) {
	t.Run("nil config compiles to nil", func(t *testing.T) {
		require.Nil(t, compileToolIntegrity(nil))
	})

	t.Run("empty digests compiles to nil", func(t *testing.T) {
		require.Nil(t, compileToolIntegrity(&filterapi.MCPToolIntegrity{}))
	})

	t.Run("defaults OnMismatch to Drop and lowercases digests", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{
			Digests: map[string]string{"tool-a": "  ABCDEF  "},
		})
		require.NotNil(t, c)
		require.Equal(t, filterapi.ToolIntegrityActionDrop, c.onMismatch)
		require.Equal(t, "abcdef", c.digests["tool-a"])
	})

	t.Run("preserves explicit OnMismatch", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{
			Digests:    map[string]string{"tool-a": "abc"},
			OnMismatch: filterapi.ToolIntegrityActionDeny,
		})
		require.Equal(t, filterapi.ToolIntegrityActionDeny, c.onMismatch)
	})
}

func TestCompiledToolIntegrity_Same(t *testing.T) {
	a := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"t": "abc"}})
	b := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"t": "abc"}})
	c := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"t": "def"}})

	require.True(t, a.same(b))
	require.False(t, a.same(c))
	require.True(t, (*compiledToolIntegrity)(nil).same(nil))
	require.False(t, a.same(nil))
	require.False(t, (*compiledToolIntegrity)(nil).same(a))
}

func TestToolDigest_DeterministicAndSensitive(t *testing.T) {
	base := &mcp.Tool{
		Name:        "search",
		Description: "search things",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
	}

	d1, err := toolDigest(base)
	require.NoError(t, err)
	d2, err := toolDigest(base)
	require.NoError(t, err)
	require.Equal(t, d1, d2, "digest must be deterministic for the same tool")
	require.Len(t, d1, 64, "sha256 hex digest must be 64 characters")

	t.Run("differently-ordered but equal input schema keys produce the same digest", func(t *testing.T) {
		reordered := &mcp.Tool{
			Name:        "search",
			Description: "search things",
			InputSchema: map[string]any{"properties": map[string]any{"q": map[string]any{"type": "string"}}, "type": "object"},
		}
		got, err := toolDigest(reordered)
		require.NoError(t, err)
		require.Equal(t, d1, got, "digest must not depend on Go map iteration order")
	})

	t.Run("changing the description changes the digest", func(t *testing.T) {
		changed := &mcp.Tool{Name: "search", Description: "different description", InputSchema: base.InputSchema}
		got, err := toolDigest(changed)
		require.NoError(t, err)
		require.NotEqual(t, d1, got)
	})

	t.Run("changing the input schema changes the digest", func(t *testing.T) {
		changed := &mcp.Tool{Name: "search", Description: "search things", InputSchema: map[string]any{"type": "string"}}
		got, err := toolDigest(changed)
		require.NoError(t, err)
		require.NotEqual(t, d1, got)
	})

	t.Run("changing annotations changes the digest", func(t *testing.T) {
		readOnly := true
		changed := &mcp.Tool{
			Name: "search", Description: "search things", InputSchema: base.InputSchema,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		}
		got, err := toolDigest(changed)
		require.NoError(t, err)
		require.NotEqual(t, d1, got)
	})
}

func TestVerifyToolIntegrity(t *testing.T) {
	tool := &mcp.Tool{Name: "search", Description: "search things"}
	digest, err := toolDigest(tool)
	require.NoError(t, err)

	t.Run("nil config always passes", func(t *testing.T) {
		ok, mismatched, err := verifyToolIntegrity(nil, tool)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, mismatched)
	})

	t.Run("tool with no configured digest passes unverified", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"other-tool": digest}})
		ok, mismatched, err := verifyToolIntegrity(c, tool)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, mismatched)
	})

	t.Run("matching digest passes", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"search": digest}})
		ok, mismatched, err := verifyToolIntegrity(c, tool)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, mismatched)
	})

	t.Run("mismatching digest fails as a mismatch, not an error", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"search": strings.Repeat("0", 64)}})
		ok, mismatched, err := verifyToolIntegrity(c, tool)
		require.NoError(t, err)
		require.False(t, ok)
		require.True(t, mismatched)
	})
}

func TestToolIntegrityMismatch(t *testing.T) {
	toolA := &mcp.Tool{Name: "a"}
	toolB := &mcp.Tool{Name: "b"}
	digestA, err := toolDigest(toolA)
	require.NoError(t, err)

	t.Run("no mismatch when all digest-covered tools match", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"a": digestA}})
		_, found := toolIntegrityMismatch(c, []*mcp.Tool{toolA, toolB})
		require.False(t, found, "tool b has no configured digest so must not count as a mismatch")
	})

	t.Run("reports mismatch when a digest-covered tool doesn't match", func(t *testing.T) {
		c := compileToolIntegrity(&filterapi.MCPToolIntegrity{Digests: map[string]string{"a": "deadbeef"}})
		name, found := toolIntegrityMismatch(c, []*mcp.Tool{toolA, toolB})
		require.True(t, found)
		require.Equal(t, "a", name)
	})
}
