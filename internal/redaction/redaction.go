// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package redaction provides utilities for redacting sensitive information
// from requests and responses for safe debug logging.
package redaction

import (
	"crypto/sha256"
	"fmt"
)

// ComputeContentHash computes a cryptographic hash for content uniqueness tracking.
// This hash is used for debugging purposes and unique content detection, particularly for:
// - Tracking cache hits/misses by correlating identical content across requests
// - Identifying duplicate or similar requests without exposing actual content
// - Debugging issues by matching redacted logs to specific content patterns
// - Unique detection of content for deduplication and analysis
//
// We use SHA256 for:
// - Strong collision resistance for reliable unique detection
// - Cryptographic properties suitable for content identification
// - Standard hash function widely used for content addressing
//
// Returns a 16-character hex string (first 64 bits of SHA256 hash) for compact representation
// while maintaining strong collision resistance.
func ComputeContentHash(s string) string {
	hash := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", hash)[:16]
}

// RedactString replaces sensitive string content with a placeholder containing length and hash.
// The hash allows correlating logs with specific content for debugging without exposing
// the actual sensitive data.
//
// Format: [REDACTED LENGTH=n HASH=xxxxxxxx]
//
// Example: "secret API key 12345" becomes "[REDACTED LENGTH=19 HASH=a3f5e8c2]"
func RedactString(s string) string {
	if s == "" {
		return ""
	}
	hash := ComputeContentHash(s)
	return fmt.Sprintf("[REDACTED LENGTH=%d HASH=%s]", len(s), hash)
}

// RedactJSONTree redacts every string leaf in a JSON-compatible value, returning a new value.
//
// It walks the standard JSON tree shapes produced by encoding/json's default unmarshaler into
// `any`: string, []any, and map[string]any, replacing each string leaf with the [REDACTED …]
// placeholder from [RedactString]. Numbers, bools, and nil are kept as-is.
//
// The value of a "type" key is preserved (not redacted): in the request schemas redacted here,
// "type" is a JSON-union discriminator (e.g. "message", "text", "image", "tool_use"), not user
// data. Redacting it would make the redacted value fail to round-trip back into the typed union
// during redaction, and discriminator values carry no sensitive content.
//
// It is intended for endpoint request fields whose values are untyped unions populated by custom
// unmarshalers (e.g. prompt/input content), so that no sensitive nested string is missed when
// redacting for debug logs. Non-sensitive structural strings other than "type" (role values,
// task_type, …) may be redacted — this over-redaction is acceptable and safe for debug logging,
// and it means the redactor never has to track which nested fields are user data.
//
// RedactJSONTree does not mutate its input; it returns a new tree (slices/maps are copied as it
// descends).
func RedactJSONTree(v any) any {
	switch val := v.(type) {
	case string:
		return RedactString(val)
	case []any:
		out := make([]any, len(val))
		for i, e := range val {
			out[i] = RedactJSONTree(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, e := range val {
			// Keys are structural (field/type names), not user data — keep them.
			// A "type" key holds a JSON-union discriminator value, not user data — keep it.
			if k == "type" {
				out[k] = e
				continue
			}
			out[k] = RedactJSONTree(e)
		}
		return out
	default:
		// Numbers, bools, nil, and any other concrete type are left unchanged.
		return v
	}
}
