// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package requestheaderattrs

import (
	"fmt"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// DefaultSessionIDHeaderMapping is the default header-to-attribute mapping for session tracking.
const DefaultSessionIDHeaderMapping = "agent-session-id:session.id"

// ResolveAll resolves span, metrics, and log request header attributes using a shared base mapping.
// - Metrics never default.
// - Span and log default to DefaultSessionIDHeaderMapping when unset.
// - An explicitly empty span/log value clears the default (base still applies).
func ResolveAll(base, span, metrics, log *string) (map[string]string, map[string]string, map[string]string, error) {
	var err error
	var baseAttrs, spanAttrs, metricsAttrs, logAttrs map[string]string
	if baseAttrs, err = parseOptional(base, ""); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid request header attributes: %w", err)
	}
	if spanAttrs, err = parseOptional(span, DefaultSessionIDHeaderMapping); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid span request header attributes: %w", err)
	}
	if metricsAttrs, err = parseOptional(metrics, ""); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid metrics request header attributes: %w", err)
	}
	if logAttrs, err = parseOptional(log, DefaultSessionIDHeaderMapping); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid log request header attributes: %w", err)
	}
	return internalapi.MergeRequestHeaderAttributeMappings(baseAttrs, spanAttrs),
		internalapi.MergeRequestHeaderAttributeMappings(baseAttrs, metricsAttrs),
		internalapi.MergeRequestHeaderAttributeMappings(baseAttrs, logAttrs), nil
}

// ResolveLog resolves access-log header attributes using a shared base mapping.
// - Defaults to DefaultSessionIDHeaderMapping when unset.
// - Explicit empty clears the default (base still applies).
func ResolveLog(base, log *string) (map[string]string, error) {
	baseAttrs, err := parseOptional(base, "")
	if err != nil {
		return nil, fmt.Errorf("invalid request header attributes: %w", err)
	}
	logAttrs, err := parseOptional(log, DefaultSessionIDHeaderMapping)
	if err != nil {
		return nil, fmt.Errorf("invalid log request header attributes: %w", err)
	}
	return internalapi.MergeRequestHeaderAttributeMappings(baseAttrs, logAttrs), nil
}

// parseOptional returns nil for unset/empty and parses the mapping otherwise.
func parseOptional(v *string, def string) (map[string]string, error) {
	m := def
	if v != nil {
		m = *v
	}
	if m == "" {
		return nil, nil
	}
	attrs, err := internalapi.ParseRequestHeaderAttributeMapping(m)
	if err != nil {
		return nil, fmt.Errorf("%s", m)
	}
	for name := range attrs {
		if strings.EqualFold(name, internalapi.GoogleAPIKeyHeader) {
			delete(attrs, name)
		}
	}
	return attrs, nil
}
