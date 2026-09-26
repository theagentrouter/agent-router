// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json" //nolint: depguard // byte-stable hashing; sonic does not guarantee stable field order.
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// compileResourceIntegrity normalizes ri's digests (lowercased, trimmed) once at config
// load, mirroring compileToolIntegrity. Returns nil if ri is unset or declares no digests,
// so callers can treat a nil result as "verification not configured for this backend".
func compileResourceIntegrity(ri *filterapi.MCPResourceIntegrity) map[string]string {
	if ri == nil || len(ri.Digests) == 0 {
		return nil
	}
	digests := make(map[string]string, len(ri.Digests))
	for uri, digest := range ri.Digests {
		digests[uri] = strings.ToLower(strings.TrimSpace(digest))
	}
	return digests
}

// resourceDigestPayload is the canonical, deterministically-ordered representation of an
// MCP resource that a configured digest is computed over: URI, MIME type, and the actual
// content (Text or Blob). Unlike a tool's digest, this is a *complete* integrity check --
// an MCP resource's entire "behavior" is its content, with no separate handler logic hidden
// behind it the way a tool's implementation is hidden behind its declared schema.
type resourceDigestPayload struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}

// resourceDigest computes the lowercase-hex-encoded SHA-256 digest of rc's canonical
// content. See toolDigest's doc comment for why this uses encoding/json rather than the
// sonic-backed internal/json package used elsewhere in this proxy: the same byte-stability
// requirement applies here, even though ResourceContents holds no nested maps today.
func resourceDigest(rc *mcp.ResourceContents) (string, error) {
	payload := resourceDigestPayload{
		URI:      rc.URI,
		MIMEType: rc.MIMEType,
		Text:     rc.Text,
		Blob:     rc.Blob,
	}
	data, err := stdjson.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// verifyResourceIntegrity checks rc's content against digests' configured expected digest
// for rc.URI, if any. ok reports whether rc passes: true when digests is nil, or rc.URI has
// no configured expected digest (verification is opt-in per resource), or the content
// matches. mismatched distinguishes a real mismatch from a digest computation error.
func verifyResourceIntegrity(digests map[string]string, rc *mcp.ResourceContents) (ok, mismatched bool, err error) {
	if digests == nil {
		return true, false, nil
	}
	expected, configured := digests[rc.URI]
	if !configured {
		return true, false, nil
	}
	got, err := resourceDigest(rc)
	if err != nil {
		return false, false, err
	}
	if got != expected {
		return false, true, nil
	}
	return true, false, nil
}
