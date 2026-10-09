// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// envUserIDHeader names the HTTP request header whose value is stamped onto
// spans as the OpenInference `user.id` attribute (e.g. "x-ai-user-id"), so
// trace backends that understand OpenInference (e.g. Arize Phoenix) can
// filter/group traces by end user natively. Unset/empty = feature off,
// upstream behavior exactly.
//
// Unlike requestHeaderAttributes (whose one deployed mapping,
// x-client-id:consumer.id, is stamped from a header the GATEWAY injects
// post-auth and a client cannot spoof), the user-id header is supplied by
// the CONSUMER application on behalf of its logged-in end user. That makes
// it untrusted input headed for a trace store, and it is treated
// accordingly — see boundedUserIDValue:
//
//   - Trust boundary: the value is stamped ONLY when the request also
//     carries the authenticated consumer identity (the header that
//     requestHeaderAttributes maps to `consumer.id`). That header is set by
//     the gateway's API-key auth filter after a key validates, so its
//     presence is in-band evidence the request passed consumer auth. No
//     consumer.id mapping configured, or header absent = fail closed, no
//     user.id attribute. The value is attribution METADATA only — it must
//     never feed authorization decisions.
//   - Bounding: the value is sanitized to printable ASCII and capped in
//     length before it reaches the span, so a hostile consumer cannot smuggle
//     control bytes or megabyte blobs into the trace store. The bound covers
//     ONLY this user.id lane: values flowing through requestHeaderAttributes
//     mappings (upstream's default agent-session-id:session.id included) are
//     stamped verbatim and unbounded — pre-existing upstream behavior this
//     patch does not change.
//
// The MCP tracer is deliberately NOT extended: this mapping is merged only
// into the request tracers' headerAttributes (see NewTracingFromEnv), never
// into the MCP tracer's, whose meta-first lookup would bypass both the
// sanitizer and the consumer-auth gate.
const envUserIDHeader = "AI_GATEWAY_TRACING_USER_ID_HEADER"

// otelAttrUserID is the OpenInference span attribute for the end user's
// identity. https://github.com/Arize-ai/openinference — Phoenix keys its
// user filtering/grouping on exactly this attribute.
const otelAttrUserID = "user.id"

// otelAttrConsumerID is the span attribute the deployment maps its
// gateway-injected consumer-identity header to (requestHeaderAttributes,
// e.g. "x-client-id:consumer.id"). boundedUserIDValue keys its
// authenticated-consumer gate on this attribute NAME so the gate composes
// with whatever header the deployment designates, without hard-coding the
// header itself here.
const otelAttrConsumerID = "consumer.id"

// maxUserIDValueLen caps the stamped user.id value. Real ids are short
// (Mongo ObjectIds are 24 chars, Auth0 `sub` claims and emails well under
// 100); the cap only exists to bound hostile input.
const maxUserIDValueLen = 256

// withUserIDHeaderFromEnv returns headerAttributeMapping with the
// envUserIDHeader mapping (header -> user.id) merged in, or the input
// unchanged when the env is unset. The input map is never mutated — the
// caller passes the ORIGINAL map to the MCP tracer (see envUserIDHeader's
// doc for why MCP is excluded).
//
// Collision fail-safe: if the env names a header that headerAttributeMapping
// already maps (case-insensitively) to some attribute — e.g.
// AI_GATEWAY_TRACING_USER_ID_HEADER=x-client-id colliding with the deployed
// x-client-id:consumer.id mapping — the EXISTING mapping wins: the input is
// returned unchanged (user.id stamping stays off) and an explicit error
// naming both mappings is written to errOut. Silently overriding would let
// one misconfigured env var replace consumer.id with a consumer-supplied
// value on every span, destroying consumer attribution.
func withUserIDHeaderFromEnv(headerAttributeMapping map[string]string, errOut io.Writer) map[string]string {
	header := strings.ToLower(strings.TrimSpace(os.Getenv(envUserIDHeader)))
	if header == "" {
		return headerAttributeMapping
	}
	for existingHeader, attrName := range headerAttributeMapping {
		if strings.EqualFold(existingHeader, header) {
			fmt.Fprintf(errOut,
				"AI Gateway tracing: ignoring %s=%q: header %q is already mapped to span attribute %q by the configured header-attribute mapping; keeping %q -> %q, refusing the %q -> %q override, and DISABLING user.id stamping (point the env at a header not present in the mapping)\n",
				envUserIDHeader, header, existingHeader, attrName, existingHeader, attrName, header, otelAttrUserID)
			return headerAttributeMapping
		}
	}
	merged := make(map[string]string, len(headerAttributeMapping)+1)
	for k, v := range headerAttributeMapping {
		merged[k] = v
	}
	merged[header] = otelAttrUserID
	return merged
}

// boundedUserIDValue applies the user.id trust boundary and bounding to a
// raw header value. It returns the value to stamp and true, or "" and false
// when the attribute must not be stamped:
//
//   - false unless some header that headerAttributes maps to consumer.id is
//     present (non-empty) in the request — i.e. unless the gateway's auth
//     filter marked the request as authenticated consumer traffic. Fail
//     closed: no consumer.id mapping configured means user.id never stamps.
//   - the value is reduced to printable ASCII (bytes 0x20..0x7E; anything
//     else, including UTF-8 multibyte sequences and control bytes, becomes
//     '_') and truncated to maxUserIDValueLen bytes.
//   - false when the sanitized value contains nothing but underscores and
//     spaces. That drops values with no printable content (all replacement
//     damage) AND raw values made only of literal underscores/spaces —
//     printable, but indistinguishable from damage and worthless as an id.
func boundedUserIDValue(headerAttributes map[string]string, headers map[string]string, raw string) (string, bool) {
	authenticated := false
	for headerName, attrName := range headerAttributes {
		if attrName == otelAttrConsumerID && headers[headerName] != "" {
			authenticated = true
			break
		}
	}
	if !authenticated {
		return "", false
	}
	sanitized := sanitizeUserIDValue(raw)
	if strings.Trim(sanitized, "_ ") == "" {
		return "", false
	}
	return sanitized, true
}

// sanitizeUserIDValue maps every byte outside printable ASCII to '_'
// (replacement, not deletion, so hostile bytes stay visible as damage
// instead of silently splicing the remainder together) and truncates to
// maxUserIDValueLen bytes.
func sanitizeUserIDValue(raw string) string {
	if len(raw) > maxUserIDValueLen {
		raw = raw[:maxUserIDValueLen]
	}
	b := []byte(raw)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '_'
		}
	}
	return string(b)
}
