// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// modernMeta builds a params object carrying the per-request _meta block that a
// well-formed modern request must include. Fields left empty are omitted so
// individual tests can construct precisely the malformed shapes they want to
// exercise.
func modernMeta(protocolVersion string, capabilities []byte) []byte {
	meta := map[string]any{}
	if protocolVersion != "" {
		meta[metaProtocolVersion] = protocolVersion
	}
	if capabilities != nil {
		// Wrap in RawMessage so the bytes are embedded as JSON rather than base64-encoded.
		meta[metaClientCapabilities] = json.RawMessage(capabilities)
	}
	params := map[string]any{"_meta": meta}
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return raw
}

// newRequestMsg builds a JSON-RPC request. A nil id produces a notification
// (invalid ID, so expectsResponse is false); a non-nil id produces a call.
func newRequestMsg(t *testing.T, method string, id any, params []byte) *jsonrpc.Request {
	t.Helper()
	req := &jsonrpc.Request{Method: method, Params: params}
	if id != nil {
		jid, err := jsonrpc.MakeID(id)
		require.NoError(t, err)
		req.ID = jid
	}
	return req
}

// newHTTPRequest builds an *http.Request with the given method and headers.
func newHTTPRequest(t *testing.T, method string, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "/mcp", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestProtocolError_Error(t *testing.T) {
	err := &protocolError{Code: errCodeInvalidParams, Message: "bad params"}
	require.Equal(t, "mcp protocol error -32602: bad params", err.Error())
}

func TestJSONPresent(t *testing.T) {
	require.False(t, jsonPresent(nil))
	require.False(t, jsonPresent([]byte(``)))
	require.False(t, jsonPresent([]byte(`null`)))
	require.True(t, jsonPresent([]byte(`{}`)))
	require.True(t, jsonPresent([]byte(`{"tools":{}}`)))
	require.True(t, jsonPresent([]byte(`"x"`)))
	require.True(t, jsonPresent([]byte(`0`)))
}

// TestDetectClientEra_NonPOST covers the resolution rule that anything other
// than POST is legacy, since the GET/DELETE endpoints were removed by the
// modern spec.
func TestDetectClientEra_NonPOST(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			r := newHTTPRequest(t, method, map[string]string{
				// Even a modern version header must not flip a non-POST to modern.
				mcpProtocolVersionHeader: protocolVersion20260728,
			})
			got := detectClientEra(r, nil)
			require.Nil(t, got.err)
			require.Equal(t, eraLegacy, got.era)
		})
	}
}

func TestDetectClientEra_DeclaredVersion(t *testing.T) {
	t.Run("modern version routes to modern validation", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, map[string]string{
			mcpProtocolVersionHeader: protocolVersion20260728,
			mcpMethodHeader:          "tools/call",
		})
		msg := newRequestMsg(t, "tools/call", "id", modernMeta(protocolVersion20260728, []byte(`{"tools":{}}`)))
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraModern, got.era)
		require.Equal(t, protocolVersion20260728, got.version)
	})

	for _, v := range []string{"2025-11-25", "2025-06-18"} {
		t.Run("legacy version "+v+" routes to legacy validation", func(t *testing.T) {
			r := newHTTPRequest(t, http.MethodPost, map[string]string{
				mcpProtocolVersionHeader: v,
				sessionIDHeader:          "sess",
			})
			msg := newRequestMsg(t, "tools/call", "id", nil)
			got := detectClientEra(r, msg)
			require.Nil(t, got.err)
			require.Equal(t, eraLegacy, got.era)
			require.Equal(t, v, got.version)
		})
	}

	t.Run("unknown version is treated as legacy", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, map[string]string{
			mcpProtocolVersionHeader: "1999-01-01",
		})
		msg := newRequestMsg(t, "tools/call", "id", nil)
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraLegacy, got.era)
		require.Equal(t, "1999-01-01", got.version)
	})
}

// TestDetectClientEra_NoVersionHeader covers the simplified resolution rule:
// a missing Mcp-Protocol-Version is treated as legacy (map zero-value), matching
// the Streamable HTTP MAY that servers supporting pre-2025-06-18 clients may
// treat an omitted header as 2025-03-26.
func TestDetectClientEra_NoVersionHeader(t *testing.T) {
	t.Run("modern-only method without version header is rejected as method not found", func(t *testing.T) {
		for method := range modernOnlyMethods {
			r := newHTTPRequest(t, http.MethodPost, nil)
			msg := newRequestMsg(t, method, "id", nil)
			got := detectClientEra(r, msg)
			require.NotNil(t, got.err, "method %q should be rejected", method)
			// Missing version → legacy path → modern-only methods are unknown.
			require.Equal(t, errCodeMethodNotFound, got.err.Code)
			require.Equal(t, http.StatusOK, got.err.HTTPStatus)
		}
	})

	t.Run("session ID implies legacy", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, map[string]string{sessionIDHeader: "sess"})
		msg := newRequestMsg(t, "tools/call", "id", nil)
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraLegacy, got.era)
		require.Empty(t, got.version)
	})

	t.Run("legacy-only method without session implies legacy", func(t *testing.T) {
		for method := range legacyOnlyMethods {
			r := newHTTPRequest(t, http.MethodPost, nil)
			msg := newRequestMsg(t, method, "id", nil)
			got := detectClientEra(r, msg)
			require.Nil(t, got.err, "method %q should be legacy, got %+v", method, got.err)
			require.Equal(t, eraLegacy, got.era)
		}
	})

	t.Run("Mcp-Method without version header is treated as legacy", func(t *testing.T) {
		// Mcp-Method alone is not discriminating: only the version header value
		// selects the modern path. Without it, the request falls through to legacy.
		r := newHTTPRequest(t, http.MethodPost, map[string]string{mcpMethodHeader: "tools/call"})
		msg := newRequestMsg(t, "tools/call", "id", nil)
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraLegacy, got.era)
	})

	t.Run("Mcp-Method with session ID is still legacy", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, map[string]string{
			mcpMethodHeader: "tools/call",
			sessionIDHeader: "sess",
		})
		msg := newRequestMsg(t, "tools/call", "id", nil)
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraLegacy, got.era)
	})

	t.Run("plain request with nothing declared defaults to legacy", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, nil)
		msg := newRequestMsg(t, "tools/list", "id", nil)
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraLegacy, got.era)
	})
}

func TestValidateLegacyRequest(t *testing.T) {
	t.Run("modern-only method is rejected even on legacy", func(t *testing.T) {
		for method := range modernOnlyMethods {
			got := validateLegacyRequest(&requestDetails{isRequest: true, method: method})
			require.NotNil(t, got.err, "method %q", method)
			require.Equal(t, errCodeMethodNotFound, got.err.Code)
			// Legacy carries JSON-RPC failures inside a 200 body.
			require.Equal(t, http.StatusOK, got.err.HTTPStatus)
		}
	})

	t.Run("ordinary legacy request passes and preserves version", func(t *testing.T) {
		got := validateLegacyRequest(&requestDetails{
			isRequest:     true,
			method:        "tools/list",
			headerVersion: protocolVersion20250618,
		})
		require.Nil(t, got.err)
		require.Equal(t, eraLegacy, got.era)
		require.Equal(t, protocolVersion20250618, got.version)
	})
}

func TestValidateModernRequest(t *testing.T) {
	caps := []byte(`{"tools":{}}`)

	t.Run("JSON-RPC response is rejected", func(t *testing.T) {
		// isRequestOrNotification=false means the body is a response, not a request/notification.
		// The modern stateless POST path carries no server-initiated requests, so a
		// response has nothing to answer and must be rejected.
		got := validateModernRequest(&requestDetails{
			headerVersion: protocolVersion20260728,
			isRequest:     false,
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeInvalidRequest, got.err.Code)
		require.Equal(t, http.StatusBadRequest, got.err.HTTPStatus)
		require.Contains(t, got.err.Message, "responses are not valid")
	})

	t.Run("well-formed call passes", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta(protocolVersion20260728, caps),
		})
		require.Nil(t, got.err)
		require.Equal(t, eraModern, got.era)
		require.Equal(t, protocolVersion20260728, got.version)
	})

	t.Run("missing Mcp-Method header", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta(protocolVersion20260728, caps),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeHeaderMismatch, got.err.Code)
		require.Equal(t, http.StatusBadRequest, got.err.HTTPStatus)
		require.Contains(t, got.err.Message, mcpMethodHeader)
	})

	t.Run("Mcp-Method header disagrees with body", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/list",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta(protocolVersion20260728, caps),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeHeaderMismatch, got.err.Code)
		require.Contains(t, got.err.Message, "does not match body value")
	})

	t.Run("session ID is not allowed under modern", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			sessionID:       "sess",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta(protocolVersion20260728, caps),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeHeaderMismatch, got.err.Code)
		require.Contains(t, got.err.Message, sessionIDHeader)
	})

	t.Run("legacy-only method is not found under modern", func(t *testing.T) {
		for method := range legacyOnlyMethods {
			got := validateModernRequest(&requestDetails{
				headerVersion:   protocolVersion20260728,
				headerMethod:    method,
				method:          method,
				isRequest:       true,
				expectsResponse: true,
				params:          modernMeta(protocolVersion20260728, caps),
			})
			require.NotNil(t, got.err, "method %q", method)
			require.Equal(t, errCodeMethodNotFound, got.err.Code)
			require.Equal(t, http.StatusNotFound, got.err.HTTPStatus)
		}
	})

	t.Run("undecodable _meta is invalid params", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          []byte(`{"_meta":123}`),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeInvalidParams, got.err.Code)
		require.Equal(t, http.StatusBadRequest, got.err.HTTPStatus)
	})

	t.Run("missing protocolVersion in _meta", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta("", caps),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeInvalidParams, got.err.Code)
		require.Contains(t, got.err.Message, metaProtocolVersion)
	})

	t.Run("protocolVersion in _meta disagrees with header", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta("2025-11-25", caps),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeHeaderMismatch, got.err.Code)
		require.Contains(t, got.err.Message, metaProtocolVersion)
	})

	t.Run("call without client capabilities is rejected", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          modernMeta(protocolVersion20260728, nil),
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeMissingRequiredCapability, got.err.Code)
		require.Equal(t, map[string]any{"requiredCapabilities": []string{metaClientCapabilities}}, got.err.Data)
		require.Contains(t, got.err.Message, metaClientCapabilities)
	})

	t.Run("explicit null client capabilities is rejected", func(t *testing.T) {
		params := []byte(`{"_meta":{"` + metaProtocolVersion + `":"` + protocolVersion20260728 + `","` + metaClientCapabilities + `":null}}`)
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "tools/call",
			method:          "tools/call",
			isRequest:       true,
			expectsResponse: true,
			params:          params,
		})
		require.NotNil(t, got.err)
		require.Equal(t, errCodeMissingRequiredCapability, got.err.Code)
		require.Equal(t, map[string]any{"requiredCapabilities": []string{metaClientCapabilities}}, got.err.Data)
		require.Contains(t, got.err.Message, metaClientCapabilities)
	})

	t.Run("notification does not require client capabilities", func(t *testing.T) {
		got := validateModernRequest(&requestDetails{
			headerVersion:   protocolVersion20260728,
			headerMethod:    "notifications/progress",
			method:          "notifications/progress",
			isRequest:       true,
			expectsResponse: false,
			params:          modernMeta(protocolVersion20260728, nil),
		})
		require.Nil(t, got.err)
		require.Equal(t, eraModern, got.era)
	})
}

// TestDetectClientEra_ModernEndToEnd exercises the full detectClientEra path
// for a representative modern call, asserting header and body are validated
// together.
func TestDetectClientEra_ModernEndToEnd(t *testing.T) {
	caps, err := json.Marshal(mcp.ClientCapabilities{})
	require.NoError(t, err)

	t.Run("valid modern server/discover call", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, map[string]string{
			mcpProtocolVersionHeader: protocolVersion20260728,
			mcpMethodHeader:          "server/discover",
		})
		msg := newRequestMsg(t, "server/discover", "id", modernMeta(protocolVersion20260728, caps))
		got := detectClientEra(r, msg)
		require.Nil(t, got.err)
		require.Equal(t, eraModern, got.era)
	})

	t.Run("modern header but legacy-only method is rejected", func(t *testing.T) {
		r := newHTTPRequest(t, http.MethodPost, map[string]string{
			mcpProtocolVersionHeader: protocolVersion20260728,
			mcpMethodHeader:          "initialize",
		})
		msg := newRequestMsg(t, "initialize", "id", modernMeta(protocolVersion20260728, caps))
		got := detectClientEra(r, msg)
		require.NotNil(t, got.err)
		require.Equal(t, errCodeMethodNotFound, got.err.Code)
		require.Equal(t, http.StatusNotFound, got.err.HTTPStatus)
	})
}

// TestMethodTablesDisjoint guards against a method being simultaneously legacy-
// only and modern-only, which would make era detection self-contradictory.
func TestMethodTablesDisjoint(t *testing.T) {
	for m := range legacyOnlyMethods {
		_, both := modernOnlyMethods[m]
		require.False(t, both, "method %q cannot be both legacy-only and modern-only", m)
	}
}
