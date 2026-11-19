// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi

import (
	"sort"
	"strings"
)

// CanonicalHeaderScopeKey returns a stable key for exact-match scope header pairs.
// Header names are lowercased per HTTP semantics. An empty map yields an empty string (no header scope).
func CanonicalHeaderScopeKey(pairs map[string]string) string {
	if len(pairs) == 0 {
		return ""
	}
	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+pairs[name])
	}
	return strings.Join(parts, ",")
}

// ScopeHeaderNamesFromKeys extracts the header names referenced in configured scope keys.
func ScopeHeaderNamesFromKeys(modelsByHeaderScope map[string][]Model) map[string]struct{} {
	names := make(map[string]struct{})
	for key := range modelsByHeaderScope {
		for _, part := range strings.Split(key, ",") {
			if idx := strings.IndexByte(part, '='); idx > 0 {
				names[part[:idx]] = struct{}{}
			}
		}
	}
	return names
}

// RequestHeaderScopeKey builds the canonical scope key from request headers, using only the header names
// present in the configured scope keys.
func RequestHeaderScopeKey(modelsByHeaderScope map[string][]Model, requestHeaders map[string]string) string {
	if len(modelsByHeaderScope) == 0 {
		return ""
	}
	names := ScopeHeaderNamesFromKeys(modelsByHeaderScope)
	pairs := make(map[string]string, len(names))
	for name := range names {
		if value := headerValueCaseInsensitive(requestHeaders, name); value != "" {
			pairs[name] = value
		}
	}
	return CanonicalHeaderScopeKey(pairs)
}

func headerValueCaseInsensitive(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
