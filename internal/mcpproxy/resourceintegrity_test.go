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

func TestCompileResourceIntegrity(t *testing.T) {
	t.Run("nil config compiles to nil", func(t *testing.T) {
		require.Nil(t, compileResourceIntegrity(nil))
	})

	t.Run("empty digests compiles to nil", func(t *testing.T) {
		require.Nil(t, compileResourceIntegrity(&filterapi.MCPResourceIntegrity{}))
	})

	t.Run("lowercases and trims digests", func(t *testing.T) {
		got := compileResourceIntegrity(&filterapi.MCPResourceIntegrity{
			Digests: map[string]string{"skill://readme": "  ABCDEF  "},
		})
		require.Equal(t, "abcdef", got["skill://readme"])
	})
}

func TestResourceDigest_DeterministicAndSensitive(t *testing.T) {
	base := &mcp.ResourceContents{URI: "skill://readme", MIMEType: "text/markdown", Text: "# Skill\ndo the thing"}

	d1, err := resourceDigest(base)
	require.NoError(t, err)
	d2, err := resourceDigest(base)
	require.NoError(t, err)
	require.Equal(t, d1, d2, "digest must be deterministic for the same content")
	require.Len(t, d1, 64, "sha256 hex digest must be 64 characters")

	t.Run("changing the text changes the digest", func(t *testing.T) {
		changed := &mcp.ResourceContents{URI: base.URI, MIMEType: base.MIMEType, Text: "# Skill\ndo a different thing"}
		got, err := resourceDigest(changed)
		require.NoError(t, err)
		require.NotEqual(t, d1, got)
	})

	t.Run("changing the MIME type changes the digest", func(t *testing.T) {
		changed := &mcp.ResourceContents{URI: base.URI, MIMEType: "text/plain", Text: base.Text}
		got, err := resourceDigest(changed)
		require.NoError(t, err)
		require.NotEqual(t, d1, got)
	})

	t.Run("blob content is covered too", func(t *testing.T) {
		a, err := resourceDigest(&mcp.ResourceContents{URI: "skill://bin", Blob: []byte{1, 2, 3}})
		require.NoError(t, err)
		b, err := resourceDigest(&mcp.ResourceContents{URI: "skill://bin", Blob: []byte{1, 2, 4}})
		require.NoError(t, err)
		require.NotEqual(t, a, b)
	})
}

func TestVerifyResourceIntegrity(t *testing.T) {
	rc := &mcp.ResourceContents{URI: "skill://readme", Text: "do the thing"}
	digest, err := resourceDigest(rc)
	require.NoError(t, err)

	t.Run("nil digests always passes", func(t *testing.T) {
		ok, mismatched, err := verifyResourceIntegrity(nil, rc)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, mismatched)
	})

	t.Run("resource with no configured digest passes unverified", func(t *testing.T) {
		ok, mismatched, err := verifyResourceIntegrity(map[string]string{"skill://other": digest}, rc)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, mismatched)
	})

	t.Run("matching digest passes", func(t *testing.T) {
		ok, mismatched, err := verifyResourceIntegrity(map[string]string{"skill://readme": digest}, rc)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, mismatched)
	})

	t.Run("mismatching digest fails as a mismatch, not an error", func(t *testing.T) {
		ok, mismatched, err := verifyResourceIntegrity(map[string]string{"skill://readme": strings.Repeat("0", 64)}, rc)
		require.NoError(t, err)
		require.False(t, ok)
		require.True(t, mismatched)
	})
}
