// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package mcpproxy

import (
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// Protocol version constants.
const (
	protocolVersion20260728 = "2026-07-28"

	// Modern MCP headers (2026-07-28 spec).
	// These mirror the unexported constants from the go-sdk.
	mcpMethodHeader          = "Mcp-Method"
	mcpNameHeader            = "Mcp-Name"
	mcpProtocolVersionHeader = "Mcp-Protocol-Version"
	mcpParamHeaderPrefix     = "Mcp-Param-"

	// _meta key constants for per-request metadata.
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaLogLevel           = "io.modelcontextprotocol/logLevel"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
	metaSubscriptionID     = "io.modelcontextprotocol/subscriptionId"
)

// MCP JSON-RPC error codes from the SDK (re-exported for local use).
const (
	errCodeParseError                = -32700
	errCodeInvalidRequest            = -32600
	errCodeMethodNotFound            = -32601
	errCodeInvalidParams             = -32602
	errCodeHeaderMismatch            = mcp.CodeHeaderMismatch                    // -32020
	errCodeMissingRequiredCapability = mcp.CodeMissingRequiredClientCapabilities // -32021
)

// legacyOnlyMethods were removed by the 2026-07-28 spec (SEP-2575). Seeing one
// on a modern request means the client is mixing eras.
//
// ping is removed in both directions: servers can no longer originate requests,
// and any ordinary RPC already proves liveness.
var legacyOnlyMethods = map[string]struct{}{
	"initialize":                       {},
	"notifications/initialized":        {},
	"logging/setLevel":                 {}, // replaced by the per-request logLevel _meta field
	"resources/subscribe":              {}, // replaced by subscriptions/listen
	"resources/unsubscribe":            {},
	"roots/list":                       {}, // replaced by the MRTR ListRootsRequest (SEP-2322)
	"notifications/roots/list_changed": {},
	"ping":                             {},
}

// modernOnlyMethods were introduced by the 2026-07-28 spec and do not exist in
// any legacy version. They are valid under the modern era and rejected under
// legacy; server/discover in particular is mandatory for modern servers.
var modernOnlyMethods = map[string]struct{}{
	"server/discover":      {},
	"subscriptions/listen": {},
}

// era represents whether a client/backend speaks legacy or modern MCP protocol.
type era int

const (
	eraLegacy era = iota
	eraModern
)

// protocolError is a JSON-RPC error paired with the HTTP status the era in
// question mandates for it. The modern spec maps protocol failures onto real
// status codes (400 for version and header problems, 404 for unknown methods);
// legacy Streamable HTTP carries the same JSON-RPC codes inside a 200 body.
type protocolError struct {
	Code       int
	Message    string
	Data       any
	HTTPStatus int
}

func (e *protocolError) Error() string {
	return fmt.Sprintf("mcp protocol error %d: %s", e.Code, e.Message)
}

// eraDetection is the outcome of classifying a single request.
type eraDetection struct {
	// era is the interaction model to use for this request. Meaningful only
	// when err is nil.
	era era
	// version is the negotiated protocol version, empty when no version was
	// declared and the request fell back to legacy handling. Downstream code
	// treats the empty value as 2025-03-26 per the 2025-06-18 spec.
	version string
	// err, when non-nil, means the request could not be classified and must be
	// rejected with this error instead of forwarded.
	err *protocolError
}

// requestDetails is everything the era validators need, gathered once so the
// header and body are each read from a single place.
type requestDetails struct {
	// headerVersion is the declared Mcp-Protocol-Version, empty if absent.
	headerVersion string
	// headerMethod is the mirrored Mcp-Method, empty if absent.
	headerMethod string
	// sessionID is the legacy Mcp-Session-Id, empty if absent.
	sessionID string
	// method is the JSON-RPC method from the body, empty for responses.
	method string
	// params is the raw params object from the body.
	params json.RawMessage
	// isRequest is true when the body is a JSON-RPC request or
	// notification (both carry a method), and false when it is a response.
	isRequest bool
	// expectsResponse is true for requests with a valid id (calls), false for notifications.
	expectsResponse bool
}

// requestMetaEnvelope pulls _meta out of a params object without decoding the
// rest of it.
type requestMetaEnvelope struct {
	Meta json.RawMessage `json:"_meta"`
}

// modernRequestMeta holds extracted _meta fields from a modern request's params.
//
// clientInfo is optional as of modelcontextprotocol#3002: clients SHOULD send it
// unless configured otherwise, so its absence is not an error.
type modernRequestMeta struct {
	ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         json.RawMessage `json:"io.modelcontextprotocol/clientInfo"`
	ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
	LogLevel           string          `json:"io.modelcontextprotocol/logLevel,omitempty"`
}

// getRequestDetails gathers the header and body fields the era validators need
// into a single value, reading each source once. The body may be a
// jsonrpc.Request (a request or notification, both carrying a method) or a
// jsonrpc.Response; only the former populates the method, params, isRequest, and
// expectsResponse fields. For a response those fields remain zero-valued.
func getRequestDetails(r *http.Request, msg jsonrpc.Message) requestDetails {
	reqDetails := requestDetails{
		headerVersion: r.Header.Get(mcpProtocolVersionHeader),
		headerMethod:  r.Header.Get(mcpMethodHeader),
		sessionID:     r.Header.Get(sessionIDHeader),
	}
	if req, ok := msg.(*jsonrpc.Request); ok && req != nil {
		reqDetails.method = req.Method
		reqDetails.params = json.RawMessage(req.Params)
		reqDetails.isRequest = true
		reqDetails.expectsResponse = req.ID.IsValid()
	}
	return reqDetails
}

// parseModernMeta decodes the per-request _meta block. A params object that is
// absent, null, or missing _meta yields a zero-valued struct so callers can
// report precisely which required field is missing.
func parseModernMeta(params json.RawMessage) (modernRequestMeta, error) {
	var meta modernRequestMeta
	if len(params) == 0 {
		return meta, nil
	}
	var envelope requestMetaEnvelope
	if err := json.Unmarshal(params, &envelope); err != nil {
		return meta, err
	}
	if len(envelope.Meta) == 0 {
		return meta, nil
	}
	if err := json.Unmarshal(envelope.Meta, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

// jsonPresent reports whether a raw field carries a value, treating an explicit
// null as absent.
func jsonPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// detectClientEra determines whether an incoming request is from a legacy or
// modern client, and validates the request against the era it declares.
func detectClientEra(r *http.Request, msg jsonrpc.Message) eraDetection {
	if r.Method != http.MethodPost {
		return eraDetection{era: eraLegacy}
	}

	// Every POST request to the MCP endpoint MUST include an MCP-Protocol-Version header.
	// Ref: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#protocol-version-header
	reqDetails := getRequestDetails(r, msg)
	if reqDetails.headerVersion == protocolVersion20260728 {
		return validateModernRequest(&reqDetails)
	}
	return validateLegacyRequest(&reqDetails)
}

// validateLegacyRequest enforces the invariants of a pre-2026-07-28 request.
//
// Legacy has far less to check than modern: capabilities were negotiated once
// at initialize rather than per request, and the mirrored headers are not
// covered by a server-side validation requirement, so they are never treated as
// authoritative. Errors ride a 200 response body, which is how legacy
// Streamable HTTP carries JSON-RPC failures.
func validateLegacyRequest(requestDetails *requestDetails) eraDetection {
	if requestDetails.isRequest {
		// this rejects modern methods even on legacy requests, so we can return early
		if _, modernOnly := modernOnlyMethods[requestDetails.method]; modernOnly {
			return eraDetection{err: &protocolError{
				Code:       errCodeMethodNotFound,
				Message:    fmt.Sprintf("Method not found: %q", requestDetails.method),
				HTTPStatus: http.StatusOK,
			}}
		}
	}
	return eraDetection{
		era:     eraLegacy,
		version: requestDetails.headerVersion,
	}
}

// validateModernRequest enforces the invariants a 2026-07-28 request must
// satisfy before the gateway will treat its mirrored headers as trustworthy.
func validateModernRequest(requestDetails *requestDetails) eraDetection {
	// On the modern POST path the client only ever sends requests and
	// notifications, never responses. A client-to-server JSON-RPC response would
	// only exist if the server had sent the client a request; but on this path
	// the server only sends notifications (e.g. notifications/tools/list_changed),
	// never requests. So there is nothing for the client to respond to, and a
	// response body reaching here means something is wrong.
	if !requestDetails.isRequest {
		return eraDetection{err: &protocolError{
			Code:       errCodeInvalidRequest,
			Message:    "JSON-RPC responses are not valid on the modern POST path",
			HTTPStatus: http.StatusBadRequest,
		}}
	}
	// SEP-2243: Mcp-Method is required on every request and notification, and a
	// mirrored header that disagrees with the body lets an intermediary route
	// on one operation while the server executes another. Both a missing header
	// and a mismatched one are validation failures. Reject before anything
	// downstream reads either source.
	if requestDetails.headerMethod == "" {
		return eraDetection{err: &protocolError{
			Code:       errCodeHeaderMismatch,
			Message:    fmt.Sprintf("Header mismatch: %s is required", mcpMethodHeader),
			HTTPStatus: http.StatusBadRequest,
		}}
	}
	if requestDetails.headerMethod != requestDetails.method {
		return eraDetection{err: &protocolError{
			Code:       errCodeHeaderMismatch,
			Message:    fmt.Sprintf("Header mismatch: %s header value %q does not match body value %q", mcpMethodHeader, requestDetails.headerMethod, requestDetails.method),
			HTTPStatus: http.StatusBadRequest,
		}}
	}
	if _, legacyOnly := legacyOnlyMethods[requestDetails.method]; legacyOnly {
		return eraDetection{err: &protocolError{
			Code:       errCodeMethodNotFound,
			Message:    fmt.Sprintf("Method not found: %q", requestDetails.method),
			HTTPStatus: http.StatusNotFound,
		}}
	}

	if requestDetails.sessionID != "" {
		// Sessions were removed alongside the initialization handshake. A
		// session ID here means the client is straddling two eras, and honoring
		// it would reintroduce the sticky-routing requirement the spec removed.
		return eraDetection{err: &protocolError{
			Code:       errCodeHeaderMismatch,
			Message:    fmt.Sprintf("%s is not valid in protocol version %s", sessionIDHeader, requestDetails.headerVersion),
			HTTPStatus: http.StatusBadRequest,
		}}
	}

	meta, err := parseModernMeta(requestDetails.params)
	if err != nil {
		return eraDetection{err: &protocolError{
			Code:       errCodeInvalidParams,
			Message:    "Invalid params: _meta could not be decoded",
			HTTPStatus: http.StatusBadRequest,
		}}
	}

	if meta.ProtocolVersion == "" {
		return eraDetection{err: &protocolError{
			Code:       errCodeInvalidParams,
			Message:    fmt.Sprintf("Invalid params: %q is required", metaProtocolVersion),
			HTTPStatus: http.StatusBadRequest,
		}}
	}
	if meta.ProtocolVersion != requestDetails.headerVersion {
		return eraDetection{err: &protocolError{
			Code:       errCodeHeaderMismatch,
			Message:    fmt.Sprintf("Header mismatch: %s header value %q does not match %q value %q", mcpProtocolVersionHeader, requestDetails.headerVersion, metaProtocolVersion, meta.ProtocolVersion),
			HTTPStatus: http.StatusBadRequest,
		}}
	}

	// Calls must declare clientCapabilities: they tell the server what it may
	// send back on the response stream. Notifications need no reply, so the
	// field is optional. A missing block is not the same as {}: capabilities
	// are per-request and must not be reused from an earlier call.
	if requestDetails.expectsResponse && !jsonPresent(meta.ClientCapabilities) {
		return eraDetection{err: &protocolError{
			Code:       errCodeMissingRequiredCapability,
			Message:    fmt.Sprintf("Missing required client capability: %q", metaClientCapabilities),
			Data:       map[string]any{"requiredCapabilities": []string{metaClientCapabilities}},
			HTTPStatus: http.StatusBadRequest,
		}}
	}

	return eraDetection{
		era:     eraModern,
		version: requestDetails.headerVersion,
	}
}
