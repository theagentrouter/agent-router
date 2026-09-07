// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	// https://github.com/modelcontextprotocol/go-sdk/blob/392f719bd1956e7601cf85f7a9b24c7010cffb4c/mcp/streamable.go#L31-L32

	sessionIDHeader         = "mcp-session-id"
	protocolVersionHeader   = "mcp-protocol-version"
	protocolVersion20250618 = "2025-06-18"

	lastEventIDHeader = "Last-Event-Id"
)

// backendEvent wraps an sseEvent with request timing context for metrics.
type backendEvent struct {
	*sseEvent
	startAt time.Time
}

// session implements [Session].
type session struct {
	id                 secureClientToGatewaySessionID
	route              string
	reqCtx             *mcpRequestContext
	mu                 sync.RWMutex
	perBackendSessions map[filterapi.MCPBackendName]*compositeSessionEntry
	// extraHeaders contains header values extracted from the current HTTP request to be forwarded to ALL backends.
	// These are derived from the route's configured forward headers (e.g., OAuth claimToHeaders) and the current request's headers.
	// Note: extraHeaders is NOT encoded in the session ID. It is re-extracted from each incoming request.
	extraHeaders map[string]string
	// perBackendExtraHeaders contains per-backend header values extracted from the current HTTP request.
	// Key is the backend name; value is a map of destination header name -> value.
	// These are derived from each MCPRouteBackendRef's forwardHeaders config.
	// Note: perBackendExtraHeaders is NOT encoded in the session ID. It is re-extracted from each incoming request.
	perBackendExtraHeaders map[filterapi.MCPBackendName]map[string]string
}

// Close implements [io.Closer.Close].
func (s *session) Close() error {
	for backendName, sess := range s.perBackendSessions {
		sessionID := sess.sessionID
		if sessionID == "" {
			// Stateless backend, nothing to do.
			continue
		}
		req, err := http.NewRequest(http.MethodDelete, s.reqCtx.backendListenerAddr, nil)
		if err != nil {
			s.reqCtx.l.Error("failed to create DELETE request to MCP server to close session",
				slog.String("backend", backendName),
				slog.String("session_id", string(sessionID)),
				slog.String("error", err.Error()),
			)
			continue
		}
		addMCPHeaders(req, nil, nil, s.route, backendName)
		s.reqCtx.applyOriginalPathHeaders(req)
		req.Header.Set(sessionIDHeader, sessionID.String())
		resp, err := s.reqCtx.client.Do(req)
		if err != nil {
			s.reqCtx.l.Error("failed to send DELETE request to MCP server to close session",
				slog.String("backend", backendName),
				slog.String("session_id", string(sessionID)),
				slog.String("error", err.Error()),
			)
			continue
		}
		ensureHTTPConnectionReused(resp)
		if status := resp.StatusCode; (status < 200 || status >= 300) &&
			// E.g., learn-microsoft returns 405 Method Not Allowed for DELETE requests even though the session ID exists.
			status != http.StatusMethodNotAllowed &&
			// Some stateless backends may return 404 Not Found if they don't track sessions.
			status != http.StatusNotFound {
			s.reqCtx.l.Error("failed to close MCP session",
				slog.String("backend", backendName),
				slog.String("session_id", string(sessionID)),
				slog.Int("status_code", resp.StatusCode),
			)
		}
	}
	return nil
}

// clientGatewaySessionID returns the ID of this session in MCP in the client<>Gateway direction.
func (s *session) clientGatewaySessionID() secureClientToGatewaySessionID {
	return s.id
}

func (s *session) getCompositeSessionEntry(backend filterapi.MCPBackendName) *compositeSessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.perBackendSessions[backend]
	if !ok {
		s.reqCtx.l.Warn("attempt to get session for unknown backend in session",
			slog.String("backend", backend))
		return nil
	}
	return entry
}

func (s *session) setLastEventID(backend filterapi.MCPBackendName, lastEventID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.perBackendSessions[backend]
	if !ok {
		s.reqCtx.l.Warn("attempt to set last event ID for unknown backend in session",
			slog.String("backend", backend))
		return
	}
	entry.lastEventID = lastEventID
}

func (s *session) lastEventID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var b strings.Builder
	for _, entry := range s.perBackendSessions {
		_, _ = b.WriteString(entry.backendName)
		_, _ = b.WriteString(":")
		_, _ = b.WriteString(base64.StdEncoding.EncodeToString([]byte(entry.lastEventID)))
		_, _ = b.WriteString(",")
	}
	lastEventID := b.String()[:b.Len()-1] // string the trailing ','.
	if s.reqCtx != nil && s.reqCtx.sessionCrypto != nil {
		encrypted, err := s.reqCtx.sessionCrypto.Encrypt(lastEventID)
		if err != nil {
			s.reqCtx.l.Error("failed to encrypt last event ID", slog.String("error", err.Error()))
			return ""
		}
		return encrypted
	}

	return lastEventID
}

var (
	envoyAIGatewayServerToClientPingRequestIDPrefix         = "aigw-server-to-client-ping"
	envoyAIGatewayServerToClientToolsChangedRequestIDPrefix = "aigw-server-to-client-tools-changed"
	pingParam, _                                            = json.Marshal(&mcpsdk.PingParams{})
	toolsChangedParam, _                                    = json.Marshal(&mcpsdk.ToolListChangedParams{})
)

func newHeartBeatPingMessage() *jsonrpc.Request {
	// Assign a unique ID to each ping request so that the client can respond to each ping.
	id, _ := jsonrpc.MakeID(envoyAIGatewayServerToClientPingRequestIDPrefix + uuid.NewString())
	return &jsonrpc.Request{
		ID:     id,
		Method: "ping",
		Params: pingParam,
	}
}

func newToolListChangedMessage() *jsonrpc.Request {
	id, _ := jsonrpc.MakeID(envoyAIGatewayServerToClientToolsChangedRequestIDPrefix + uuid.NewString())
	return &jsonrpc.Request{
		ID:     id,
		Method: "notifications/tools/list_changed",
		Params: toolsChangedParam,
	}
}

// streamNotifications streams notifications from all backends in this session to the given writer.
func (s *session) streamNotifications(ctx context.Context, w http.ResponseWriter, toolChangeSignaler changeSignaler) error {
	backendMsgs := s.sendToAllBackends(ctx, http.MethodGet, nil, nil, nil)

	// Create a ticker for periodic heartbeat events to avoid HTTP timeouts.
	// This also helps unblock Goose at startup - it looks like Goose is waiting for the first SSE event before proceeding.
	//
	// TODO: no idea exactly why this is necessary. Goose shouldn't block on the first event.

	var (
		heartbeats      <-chan time.Time
		heartbeatTicker *time.Ticker
	)
	if heartbeatInterval > 0 {
		heartbeatTicker = time.NewTicker(heartbeatInterval)
		defer heartbeatTicker.Stop()
		heartbeats = heartbeatTicker.C
	} else {
		heartbeats = make(chan time.Time) // never ticks
	}

	// Eagerly send an initial heartbeat event to unblock Goose
	heartBeatEvent := &sseEvent{event: "message", messages: []jsonrpc.Message{newHeartBeatPingMessage()}}
	heartBeatEvent.writeAndMaybeFlush(w)

	for {
		select {
		// events received from the upstream MCP backends
		case event, ok := <-backendMsgs:
			if !ok {
				// All backend notification streams have ended (e.g. backends returned 405
				// or closed their SSE connections). Nil out the channel to disable this
				// select case, but keep the SSE connection alive for heartbeats and
				// gateway-level tool change notifications.
				backendMsgs = nil
				continue
			}

			prev := event.id
			s.setLastEventID(event.backend, event.id)
			event.id = s.lastEventID()
			if s.reqCtx.l.Enabled(ctx, slog.LevelDebug) {
				s.reqCtx.l.Debug("Changed event ID", slog.String("backend", event.backend),
					slog.String("prev_event_id", prev),
					slog.String("event_id", event.id))
			}
			for _, _msg := range event.messages {
				// Maybe the server->client request made during the notification handling needs to be modified.
				if msg, ok := _msg.(*jsonrpc.Request); ok {
					if err := s.reqCtx.maybeServerToClientRequestModify(ctx, msg, event.backend); err != nil {
						s.reqCtx.l.Error("failed to modify server->client request", slog.String("error", err.Error()))
						continue
					}
				}
				s.reqCtx.recordResponse(ctx, _msg)
			}
			event.writeAndMaybeFlush(w)
			// Reset the heartbeat ticker so that the next heartbeat will be sent after the full interval.
			// This avoids sending heartbeats too frequently when there are events.
			if heartbeatTicker != nil {
				heartbeatTicker.Reset(heartbeatInterval)
			}
		// toolChangeSignaler will trigger when the tools configured in the MCP routes change.
		// This is not related to upstream MCP server changes, but to the tools configured in the gateway.
		case <-toolChangeSignaler.Watch():
			toolChangeEvent := &sseEvent{event: "message", messages: []jsonrpc.Message{newToolListChangedMessage()}}
			toolChangeEvent.writeAndMaybeFlush(w)
			// Reset the heartbeat ticker so that the next heartbeat will be sent after the full interval.
			// This avoids sending heartbeats too frequently when there are events.
			if heartbeatTicker != nil {
				heartbeatTicker.Reset(heartbeatInterval)
			}
		case <-heartbeats:
			heartBeatEvent := &sseEvent{event: "message", messages: []jsonrpc.Message{newHeartBeatPingMessage()}}
			heartBeatEvent.writeAndMaybeFlush(w)
		case <-ctx.Done():
			// Context cancelled, stop streaming.
			return ctx.Err()
		}
	}
}

// heartbeatInterval is computed at startup to avoid the locks in os.Getenv() to be called on the request path.
var heartbeatInterval = getHeartbeatInterval(1 * time.Minute)

// getHeartbeatInterval returns the heartbeat interval configured via the MCP_HEARTBEAT_INTERVAL environment variable.
// If the environment variable is not set or invalid, it returns the default value of 1 minute.
// This value is intentionally hidden under an environment variable as it is unclear if it is generally useful.
func getHeartbeatInterval(def time.Duration) time.Duration {
	hbi, err := time.ParseDuration(os.Getenv("MCP_PROXY_HEARTBEAT_INTERVAL"))
	if err != nil {
		return def
	}
	return hbi
}

// sendToAllBackends sends an HTTP request to all backends in this session and returns a channel that streams
// the response events from all backends.
func (s *session) sendToAllBackends(ctx context.Context, httpMethod string, request *jsonrpc.Request, params mcpsdk.Params, span tracingapi.MCPSpan) <-chan *backendEvent {
	return s.sendToBackendsFiltered(ctx, httpMethod, request, params, span, nil)
}

// sendToBackendsFiltered sends an HTTP request to backends in this session that pass the given filter,
// and returns a channel that streams the response events from those backends.
// If filter is nil, all backends are included.
func (s *session) sendToBackendsFiltered(ctx context.Context, httpMethod string, request *jsonrpc.Request, params mcpsdk.Params, span tracingapi.MCPSpan, filter func(*compositeSessionEntry) bool) <-chan *backendEvent {
	var (
		logger      = s.reqCtx.l
		backendMsgs = make(chan *backendEvent, 200)
		wg          sync.WaitGroup
	)

	for backendName, cse := range s.perBackendSessions {
		if filter != nil && !filter(cse) {
			continue
		}
		wg.Add(1)
		sessionID := cse.sessionID
		go func() {
			defer wg.Done()
			backend, err := s.reqCtx.getBackendForRoute(s.route, backendName)
			if err != nil {
				logger.Error("failed to get backend for route",
					slog.String("backend", backendName),
					slog.String("session_id", string(sessionID)),
					slog.String("error", err.Error()),
				)
				return
			}
			err = s.sendRequestPerBackend(ctx, backendMsgs, s.route, backend, cse, httpMethod, request, params)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				logger.Error("failed to collect messages from MCP backend",
					slog.String("backend", backendName),
					slog.String("session_id", string(sessionID)),
					slog.String("error", err.Error()),
				)
			}
			if span != nil {
				span.RecordRouteToBackend(backendName, string(sessionID), false)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(backendMsgs)
	}()

	return backendMsgs
}

// sendRequestPerBackend sends an HTTP request to the given backend and streams the response events to eventChan.
func (s *session) sendRequestPerBackend(ctx context.Context, eventChan chan<- *backendEvent, routeName filterapi.MCPRouteName, backend filterapi.MCPBackend, cse *compositeSessionEntry,
	httpMethod string, request *jsonrpc.Request, params mcpsdk.Params,
) error {
	var body io.Reader
	if request != nil {
		encodedReq, err := jsonrpc.EncodeMessage(request)
		if err != nil {
			return fmt.Errorf("failed to encode request: %w", err)
		}
		body = bytes.NewReader(encodedReq)
	}

	req, err := http.NewRequestWithContext(ctx, httpMethod, s.reqCtx.backendListenerAddr, body)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %w", err)
	}
	sessionID := cse.sessionID.String()
	addMCPHeaders(req, request, params, routeName, backend.Name)
	s.reqCtx.applyLogHeaderMappings(req, request)
	s.reqCtx.applyOriginalPathHeaders(req)
	req.Header.Set(protocolVersionHeader, protocolVersion20250618)
	req.Header.Set(sessionIDHeader, cse.sessionID.String())
	if httpMethod != http.MethodGet {
		req.Header.Set("Content-type", "application/json")
	}
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("Accept-Encoding", "gzip, br")

	// Forward route-level headers (e.g., OAuth claimToHeaders) to the backend.
	for header, value := range s.extraHeaders {
		req.Header.Del(header)
		req.Header.Set(header, value)
	}
	// Forward per-backend headers (from MCPRouteBackendRef.forwardHeaders) with optional renaming.
	if perBackend, ok := s.perBackendExtraHeaders[backend.Name]; ok {
		for header, value := range perBackend {
			req.Header.Del(header)
			req.Header.Set(header, value)
		}
	}

	if lastEventID := cse.lastEventID; lastEventID != "" {
		req.Header.Set(lastEventIDHeader, lastEventID)
	}
	if s.reqCtx.l.Enabled(ctx, slog.LevelDebug) {
		args := []any{
			slog.String("backend", backend.Name),
			slog.String("session_id", sessionID),
			slog.String("http_method", httpMethod),
			slog.String("url", req.URL.String()),
		}
		if request != nil {
			args = append(args,
				slog.String("mcp_request_method", request.Method),
				slog.Any("mcp_request_id", request.ID),
				slog.Any("mcp_request_params", request.Params),
			)
		}

		if lastEventID := cse.lastEventID; lastEventID != "" {
			args = append(args, slog.String("last_event_id", lastEventID))
		}
		s.reqCtx.l.Debug("sending MCP request", args...)
	}
	startAt := time.Now()
	httpResp, err := s.reqCtx.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("failed to send GET request: %w", err)
	}

	defer func() {
		_ = httpResp.Body.Close()
	}()

	// Decompress the response body based on Content-Encoding.
	// We explicitly set Accept-Encoding: gzip, br so Go's http.Transport will not
	// auto-decompress, and we handle it here for both gzip and Brotli.
	var bodyReader io.Reader = httpResp.Body
	switch httpResp.Header.Get("Content-Encoding") {
	case "gzip":
		gr, gzErr := gzip.NewReader(httpResp.Body)
		if gzErr != nil {
			return fmt.Errorf("failed to create gzip decompressor: %w", gzErr)
		}
		defer gr.Close()
		bodyReader = gr
	case "br":
		bodyReader = brotli.NewReader(httpResp.Body)
	}

	switch httpResp.StatusCode {
	case http.StatusNoContent, http.StatusMethodNotAllowed, http.StatusAccepted:
		// No notifications.
		return nil
	case http.StatusOK:
	default:
		body, _ := io.ReadAll(bodyReader)
		return fmt.Errorf("MCP GET request failed with status code %d, body=%s", httpResp.StatusCode, string(body))
	}

	if isJSONContentType(httpResp.Header.Get("Content-Type")) {
		// Try to decode as a single JSON-RPC message first.
		var respBody []byte
		respBody, err = io.ReadAll(bodyReader)
		if err != nil {
			return fmt.Errorf("failed to read MCP response body: %w", err)
		}
		msg, ok := tryDecodeJSONRPCMessage(respBody)
		if ok {
			eventChan <- &backendEvent{
				sseEvent: &sseEvent{
					backend:  backend.Name,
					event:    "message",
					id:       "", // No event ID in this case.
					messages: []jsonrpc.Message{msg},
				},
				startAt: startAt,
			}
			return nil
		}
		// Body claimed application/json but isn't valid JSON-RPC (e.g. some backends
		// send SSE data despite the content type). Fall through to the SSE parser
		// using the already-read body bytes.
		bodyReader = bytes.NewReader(respBody)
	}

	// io.Copy won't flush until the end, which doesn't happen for streaming responses.
	// So we need to read the body in chunks and flush after each chunk.
	parser := newSSEEventParser(bodyReader, backend.Name)
	for {
		var event *sseEvent
		event, err = parser.next()
		// TODO: handle reconnect. We need to re-arrange the event ID so that it will also contain the backend name and the original session ID.
		// 	Since event ID can be arbitrary string, we can shove each backend's last even ID into the event ID just like the session ID.
		//
		// If it happens on the upstream, we can simply just reconnect here using the last-even-id by parsing the
		// events we have seen so far. This is because the downstream is still connected.
		//
		// In any case, the reconnect support here must be in line with proxyResponseBody's reconnect logic when that happens.
		if event != nil {
			eventChan <- &backendEvent{sseEvent: event, startAt: startAt}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
				// Unexpected EOF can happen when the server closes the connection, e.g. Envoy receives signal
				// or the upstream closes the connection. Either way, the error is not recoverable or not worth
				// the logging.
				errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			_ = httpResp.Body.Close()
			return fmt.Errorf("failed to read MCP GET response body: %w", err)
		}
	}
	return nil
}

type (
	// clientToGatewaySessionID is the ID of a session in MCP in the client<>Gateway direction.
	//
	// SessionID can be any ascii string.
	// https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#session-management
	//
	// We use the following format to encapsulate multiple MCP sessions:
	//
	//	{route name}@{subject}@{mcp-backend-name1}:{base64(mcp-session-id1)},...,{mcp-backend-nameN}:{base64(mcp-session-idN)}
	//
	// For example:
	//
	//	"some-awesome-route@mcp-user@backend1:MTIzNDU2,backend2:NjU0MzIx"
	//
	// The reason on using base64 is that MCP session IDs can be arbitrary binary data, so without it, the separator characters
	// (comma and colon) may conflict with the session ID content.
	//
	// The '{subject}@' prefix is optional and is only included if there is an authenticated token with a subject.
	// Using the subject in the session ID helps with preventing session hijacking attacks:
	// https://modelcontextprotocol.io/specification/2025-06-18/basic/security_best_practices#session-hijacking
	clientToGatewaySessionID string

	// secureClientToGatewaySessionID is an encrypted clientToGatewaySessionID.
	secureClientToGatewaySessionID string

	// clientToGatewaySessionID is the last event ID of a session in MCP in the client<>Gateway direction.
	// We use the following format to encapsulate multiple MCP sessions:
	//
	//	{mcp-backend-name1}:{base64(last-event-id1)},...,{mcp-backend-nameN}:{base64(last-event-idN)}
	//
	// For example:
	//
	//	backend1:MTIzNDU2,backend2:NjU0MzIx"
	clientToGatewayEventID string

	// secureClientToGatewayEventID is an encrypted clientToGatewayEventID.
	secureClientToGatewayEventID string

	// gatewayToMCPServerSessionID is the ID of a session in MCP in the Gateway<>MCPServer direction.
	gatewayToMCPServerSessionID string

	// compositeSessionEntry is used to track the session and last event ID for each backend in a composite session.
	compositeSessionEntry struct {
		backendName  string
		sessionID    gatewayToMCPServerSessionID
		lastEventID  string
		capabilities *mcpsdk.ServerCapabilities
	}
)

// Capability bitmask constants for encoding server capabilities in the session ID.
// Each bit represents a capability or sub-capability field.
const (
	capBitTools                = 1 << iota // bit 0: Tools non-nil
	capBitToolsListChanged                 // bit 1: Tools.ListChanged
	capBitPrompts                          // bit 2: Prompts non-nil
	capBitPromptsListChanged               // bit 3: Prompts.ListChanged
	capBitLogging                          // bit 4: Logging non-nil
	capBitResources                        // bit 5: Resources non-nil
	capBitResourcesListChanged             // bit 6: Resources.ListChanged
	capBitResourcesSubscribe               // bit 7: Resources.Subscribe
	capBitCompletions                      // bit 8: Completions non-nil

	// capBitAll is the bitmask with all capabilities set (used for backward compatibility).
	capBitAll = capBitTools | capBitToolsListChanged |
		capBitPrompts | capBitPromptsListChanged |
		capBitLogging |
		capBitResources | capBitResourcesListChanged | capBitResourcesSubscribe |
		capBitCompletions
)

// encodeCapabilityFlags encodes server capabilities as a zero-padded 3-char hex string.
func encodeCapabilityFlags(caps *mcpsdk.ServerCapabilities) string {
	if caps == nil {
		return "000"
	}
	var bits uint
	if caps.Tools != nil {
		bits |= capBitTools
		if caps.Tools.ListChanged {
			bits |= capBitToolsListChanged
		}
	}
	if caps.Prompts != nil {
		bits |= capBitPrompts
		if caps.Prompts.ListChanged {
			bits |= capBitPromptsListChanged
		}
	}
	if caps.Logging != nil {
		bits |= capBitLogging
	}
	if caps.Resources != nil {
		bits |= capBitResources
		if caps.Resources.ListChanged {
			bits |= capBitResourcesListChanged
		}
		if caps.Resources.Subscribe {
			bits |= capBitResourcesSubscribe
		}
	}
	if caps.Completions != nil {
		bits |= capBitCompletions
	}
	return fmt.Sprintf("%03x", bits)
}

// decodeCapabilityFlags decodes a hex string into server capabilities.
func decodeCapabilityFlags(hex string) *mcpsdk.ServerCapabilities {
	bits, err := strconv.ParseUint(hex, 16, 16)
	if err != nil {
		// Fallback: assume all capabilities for backward compatibility.
		bits = capBitAll
	}
	caps := &mcpsdk.ServerCapabilities{}
	if bits&capBitTools != 0 {
		caps.Tools = &mcpsdk.ToolCapabilities{
			ListChanged: bits&capBitToolsListChanged != 0,
		}
	}
	if bits&capBitPrompts != 0 {
		caps.Prompts = &mcpsdk.PromptCapabilities{
			ListChanged: bits&capBitPromptsListChanged != 0,
		}
	}
	if bits&capBitLogging != 0 {
		caps.Logging = &mcpsdk.LoggingCapabilities{}
	}
	if bits&capBitResources != 0 {
		caps.Resources = &mcpsdk.ResourceCapabilities{
			ListChanged: bits&capBitResourcesListChanged != 0,
			Subscribe:   bits&capBitResourcesSubscribe != 0,
		}
	}
	if bits&capBitCompletions != 0 {
		caps.Completions = &mcpsdk.CompletionCapabilities{}
	}
	return caps
}

// mergedCapabilities computes the union of all backend capabilities in this session.
// If ANY backend supports a capability, the merged result includes it.
// Sub-fields like ListChanged and Subscribe are OR'd across all backends.
func (s *session) mergedCapabilities() *mcpsdk.ServerCapabilities {
	merged := &mcpsdk.ServerCapabilities{}
	for _, entry := range s.perBackendSessions {
		caps := entry.capabilities
		if caps == nil {
			continue
		}
		if caps.Tools != nil {
			if merged.Tools == nil {
				merged.Tools = &mcpsdk.ToolCapabilities{}
			}
			merged.Tools.ListChanged = merged.Tools.ListChanged || caps.Tools.ListChanged
		}
		if caps.Prompts != nil {
			if merged.Prompts == nil {
				merged.Prompts = &mcpsdk.PromptCapabilities{}
			}
			merged.Prompts.ListChanged = merged.Prompts.ListChanged || caps.Prompts.ListChanged
		}
		if caps.Logging != nil {
			if merged.Logging == nil {
				merged.Logging = &mcpsdk.LoggingCapabilities{}
			}
		}
		if caps.Resources != nil {
			if merged.Resources == nil {
				merged.Resources = &mcpsdk.ResourceCapabilities{}
			}
			merged.Resources.ListChanged = merged.Resources.ListChanged || caps.Resources.ListChanged
			merged.Resources.Subscribe = merged.Resources.Subscribe || caps.Resources.Subscribe
		}
		if caps.Completions != nil {
			if merged.Completions == nil {
				merged.Completions = &mcpsdk.CompletionCapabilities{}
			}
		}
		for id, ext := range caps.Extensions {
			if merged.Extensions == nil {
				merged.Extensions = make(map[string]any, len(caps.Extensions))
			}
			// First backend to declare an extension wins, so the merge is stable regardless of
			// map iteration order.
			if _, ok := merged.Extensions[id]; !ok {
				merged.Extensions[id] = ext
			}
		}
	}
	return merged
}

// String implements fmt.Stringer.
func (g gatewayToMCPServerSessionID) String() string { return string(g) }

// String implements fmt.Stringer.
func (c clientToGatewaySessionID) String() string { return string(c) }

// backendSessionIDs parses the SessionID and returns a map of MCP backend name to MCP session ID.
func (c clientToGatewaySessionID) backendSessionIDs() (map[filterapi.MCPBackendName]*compositeSessionEntry, string, error) {
	perBackendSessionIDs := make(map[filterapi.MCPBackendName]*compositeSessionEntry)
	id := string(c)
	// The format is: {routeName}@{subject}@{backends}
	// We use LastIndex to find the backends boundary because the subject may contain '@'
	// (e.g. email addresses). The backends segment uses base64-encoded session IDs which
	// cannot contain '@', so LastIndex reliably finds the correct separator.
	lastAt := strings.LastIndex(id, "@")
	if lastAt < 0 {
		return nil, "", fmt.Errorf("invalid session ID: missing '@' separator")
	}
	backendSessions := id[lastAt+1:]
	prefix := id[:lastAt] // "{routeName}@{subject}" — subject may itself contain '@'
	firstAt := strings.Index(prefix, "@")
	if firstAt < 0 {
		return nil, "", fmt.Errorf("invalid session ID: missing '@' separator")
	}
	route := prefix[:firstAt]
	// The subject (prefix[firstAt+1:]) is retained inside the encrypted session ID for
	// anti-hijacking purposes but is not needed during parsing.

	// Each backend segment format: {backendName}:{base64(sessionID)}:{capHex}
	// The capHex field is optional for backward compatibility with old session IDs.
	for _, part := range strings.Split(backendSessions, ",") {
		// Split into at most 3 fields: backendName, base64SessionID, capHex.
		fields := strings.SplitN(part, ":", 3)
		if len(fields) < 2 {
			return nil, "", fmt.Errorf("invalid session ID: missing ':' separator in backend session ID part %q", part)
		}
		backendName := fields[0]
		if backendName == "" {
			return nil, "", fmt.Errorf("invalid session ID: empty backend name in part %q", part)
		}
		var sessionID gatewayToMCPServerSessionID
		sessionIDBase64 := fields[1]
		if sessionIDBase64 != "" { // Some servers are stateless hence no (==empty) session ID.
			decoded, err := base64.StdEncoding.DecodeString(sessionIDBase64)
			if err != nil {
				err = fmt.Errorf("invalid session ID: failed to base64 decode session ID in part %q: %w", part, err)
				return nil, "", err
			}
			sessionID = gatewayToMCPServerSessionID(decoded)
		}
		// Parse capability flags from the third field, defaulting to all capabilities
		// for backward compatibility with session IDs that don't include them.
		var caps *mcpsdk.ServerCapabilities
		if len(fields) == 3 {
			caps = decodeCapabilityFlags(fields[2])
		} else {
			caps = decodeCapabilityFlags("") // defaults to all capabilities
		}
		perBackendSessionIDs[backendName] = &compositeSessionEntry{
			backendName:  backendName,
			sessionID:    sessionID,
			capabilities: caps,
		}
	}
	return perBackendSessionIDs, route, nil
}

// String implements fmt.Stringer.
func (s secureClientToGatewaySessionID) String() string { return string(s) }

// clientToGatewaySessionIDFromEntries returns the ID of this session in MCP in the client<>Gateway direction.
func clientToGatewaySessionIDFromEntries(subject string, entries []compositeSessionEntry, routeName string) clientToGatewaySessionID {
	var b strings.Builder
	_, _ = b.WriteString(subject)
	_, _ = b.WriteString("@")
	for _, entry := range entries {
		_, _ = b.WriteString(entry.backendName)
		_, _ = b.WriteString(":")
		_, _ = b.WriteString(base64.StdEncoding.EncodeToString([]byte(entry.sessionID)))
		_, _ = b.WriteString(":")
		_, _ = b.WriteString(encodeCapabilityFlags(entry.capabilities))
		_, _ = b.WriteString(",")
	}
	sessionID := b.String()[:b.Len()-1] // string the trailing ','.
	sessionID = routeName + "@" + sessionID
	return clientToGatewaySessionID(sessionID)
}

func (e clientToGatewayEventID) backendEventIDs() map[filterapi.MCPBackendName]string {
	result := map[filterapi.MCPBackendName]string{}
	parts := strings.Split(string(e), ",")
	for _, part := range parts {
		colon := strings.Index(part, ":")
		if colon < 0 {
			continue
		}
		backendName := part[:colon]
		if backendName == "" {
			continue
		}
		eventID := part[colon+1:]
		if eventID != "" {
			decoded, err := base64.StdEncoding.DecodeString(eventID)
			if err != nil {
				continue
			}
			eventID = string(decoded)
		}
		result[backendName] = eventID
	}
	return result
}
