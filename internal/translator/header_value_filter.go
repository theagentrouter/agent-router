// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import "strings"

// Filter modes accepted by [HeaderValueFilterSetter]. These mirror
// aigv1b1.HTTPHeaderValueFilterMode without importing the Kubernetes API types.
const (
	headerValueFilterModeDenylist  = "Denylist"
	headerValueFilterModeAllowlist = "Allowlist"
)

// parseCommaSeparatedHeader splits a comma-separated request header into a slice of trimmed,
// non-empty values. It returns nil when the header is absent or empty.
func parseCommaSeparatedHeader(headers map[string]string, name string) []string {
	raw := headers[name]
	if raw == "" {
		return nil
	}
	var values []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	return values
}

// filterHeaderValues applies a denylist/allowlist filter to the values of a multi-valued header.
// It returns the filtered list and whether any value was removed. An empty values or list, or a
// mode other than "Denylist"/"Allowlist", leaves the input unchanged.
func filterHeaderValues(values []string, mode string, list []string) (filtered []string, changed bool) {
	if len(values) == 0 || len(list) == 0 {
		return values, false
	}
	var deny bool
	switch mode {
	case headerValueFilterModeDenylist:
		deny = true
	case headerValueFilterModeAllowlist:
		deny = false
	default:
		return values, false
	}
	set := make(map[string]struct{}, len(list))
	for _, v := range list {
		set[v] = struct{}{}
	}
	filtered = make([]string, 0, len(values))
	for _, v := range values {
		_, listed := set[v]
		keep := listed
		if deny {
			keep = !listed
		}
		if keep {
			filtered = append(filtered, v)
		}
	}
	return filtered, len(filtered) != len(values)
}
