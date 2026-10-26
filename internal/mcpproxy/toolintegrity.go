// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json" //nolint: depguard // byte-stable hashing; sonic does not guarantee stable field order.
	"maps"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// compiledToolIntegrity is the runtime-compiled form of [filterapi.MCPToolIntegrity] for a
// single backend: expected digests keyed by tool name, normalized to lowercase once at
// config load, and what to do when a tool's observed digest doesn't match.
type compiledToolIntegrity struct {
	digests    map[string]string
	onMismatch filterapi.ToolIntegrityAction
}

// compileToolIntegrity compiles ti into its runtime form. Returns nil if ti is unset or
// declares no digests, so callers can treat a nil result as "verification not configured
// for this backend" without a separate nil check on the source config.
func compileToolIntegrity(ti *filterapi.MCPToolIntegrity) *compiledToolIntegrity {
	if ti == nil || len(ti.Digests) == 0 {
		return nil
	}
	onMismatch := ti.OnMismatch
	if onMismatch == "" {
		onMismatch = filterapi.ToolIntegrityActionDrop
	}
	digests := make(map[string]string, len(ti.Digests))
	for name, digest := range ti.Digests {
		digests[name] = strings.ToLower(strings.TrimSpace(digest))
	}
	return &compiledToolIntegrity{digests: digests, onMismatch: onMismatch}
}

// same reports whether two compiledToolIntegrity values are semantically equivalent.
func (c *compiledToolIntegrity) same(other *compiledToolIntegrity) bool {
	if c == nil || other == nil {
		return c == other
	}
	return c.onMismatch == other.onMismatch && maps.Equal(c.digests, other.digests)
}

// toolDigestPayload is the canonical, deterministically-ordered representation of an MCP
// tool definition that a configured digest is computed over: the full set of fields a
// caller (or the LLM behind it) uses to decide whether and how to invoke the tool.
// Envelope/metadata fields (e.g. "_meta") are intentionally excluded since they can vary
// across otherwise-identical responses without changing what the tool actually does.
type toolDigestPayload struct {
	Name         string               `json:"name"`
	Description  string               `json:"description,omitempty"`
	InputSchema  any                  `json:"inputSchema,omitempty"`
	OutputSchema any                  `json:"outputSchema,omitempty"`
	Annotations  *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

// toolDigest computes the lowercase-hex-encoded SHA-256 digest of tool's canonical
// definition.
//
// This deliberately uses encoding/json rather than the sonic-backed internal/json package
// used elsewhere in this proxy: InputSchema/OutputSchema are typically map[string]any
// decoded from the backend's JSON response, and sonic's fast path does not guarantee
// sorted map keys the way encoding/json does. A digest that isn't stable across
// equivalent-but-differently-ordered JSON would make this feature useless.
func toolDigest(tool *mcp.Tool) (string, error) {
	payload := toolDigestPayload{
		Name:         tool.Name,
		Description:  tool.Description,
		InputSchema:  tool.InputSchema,
		OutputSchema: tool.OutputSchema,
		Annotations:  tool.Annotations,
	}
	data, err := stdjson.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// verifyToolIntegrity checks tool against c's configured expected digest, if any.
//
// ok reports whether the tool passes verification: true when integrity isn't configured
// for this backend, or the tool has no configured expected digest (verification is opt-in
// per tool -- tools not listed in Digests pass through unverified), or the observed digest
// matches. mismatched distinguishes a real mismatch from a digest computation error, so
// callers can log the two cases differently.
func verifyToolIntegrity(c *compiledToolIntegrity, tool *mcp.Tool) (ok, mismatched bool, err error) {
	if c == nil {
		return true, false, nil
	}
	expected, configured := c.digests[tool.Name]
	if !configured {
		return true, false, nil
	}
	got, err := toolDigest(tool)
	if err != nil {
		return false, false, err
	}
	if got != expected {
		return false, true, nil
	}
	return true, false, nil
}

// toolIntegrityMismatch reports the name of the first tool in tools whose digest doesn't
// match its configured expected digest under c, and whether any such tool was found. It is
// used for [filterapi.ToolIntegrityActionDeny] handling, which cares only about whether the
// backend's response contains a digest-covered mismatch, not which of possibly several
// tools mismatched.
func toolIntegrityMismatch(c *compiledToolIntegrity, tools []*mcp.Tool) (name string, found bool) {
	for _, tool := range tools {
		expected, configured := c.digests[tool.Name]
		if !configured {
			continue
		}
		got, err := toolDigest(tool)
		if err != nil || got != expected {
			return tool.Name, true
		}
	}
	return "", false
}
