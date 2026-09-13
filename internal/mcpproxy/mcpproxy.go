// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/lang"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// mcpRequestContext serves /mcp endpoint.
type mcpRequestContext struct {
	*ProxyConfig
	metrics                   metrics.MCPMetrics
	requestHeaders            http.Header
	originalPath              string
	perBackendMetricsRecorded bool
}

// defaultMaxRequestBodySize is the default maximum allowed POST body size in bytes (4 MiB).
const defaultMaxRequestBodySize = 4 * 1024 * 1024

var errNoMatchingBackendSelector = errors.New("backendSelector matches no route backends")

// getMaxRequestBodySize returns the configured POST body limit from the environment variable,
// falling back to 4 MiB if the variable is unset or invalid.
func getMaxRequestBodySize() int64 {
	if v, ok := os.LookupEnv("MCP_PROXY_MAX_REQUEST_BODY_SIZE"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxRequestBodySize
}

// NewMCPProxy creates a new MCPProxy instance.
func NewMCPProxy(l *slog.Logger, mcpMetrics metrics.MCPMetrics, tracer tracingapi.MCPTracer, sessionCrypto SessionCrypto, logRequestHeaderAttributes map[string]string) (*ProxyConfig, *http.ServeMux, error) {
	toolChangeSignaler := newMultiWatcherSignaler() // used to signal changes to all active sessions.
	cfg := &ProxyConfig{
		toolChangeSignaler:         toolChangeSignaler,
		tracer:                     tracer,
		sessionCrypto:              sessionCrypto,
		l:                          l,
		client:                     http.Client{}, // No timeout as it's enforced at Envoy level.
		logRequestHeaderAttributes: maps.Clone(logRequestHeaderAttributes),
		maxRequestBodySize:         getMaxRequestBodySize(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(
		// Must match all paths since the route selection happens at Envoy level and the "route" header is already
		// set when it reaches here. We use that to select the appropriate backends, so we don't need to have different paths here.
		//
		// For example, if we mistakenly set /mcp here, only the route with prefix /mcp will be matched, and other routes
		// with different prefixes will not be matched, which is not desired.
		"/", func(w http.ResponseWriter, r *http.Request) {
			proxy := &mcpRequestContext{
				metrics:        mcpMetrics.WithRequestAttributes(r),
				ProxyConfig:    cfg,
				requestHeaders: r.Header,
				originalPath:   originalPathForRequest(r),
			}
			switch r.Method {
			case http.MethodGet:
				proxy.serveGET(w, r)
			case http.MethodPost:
				proxy.servePOST(w, r)
			case http.MethodDelete:
				proxy.serverDELETE(w, r)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
	return cfg, mux, nil
}

func originalPathForRequest(r *http.Request) string {
	if r.RequestURI != "" {
		return r.RequestURI
	}
	if r.URL == nil {
		return ""
	}
	return r.URL.RequestURI()
}

func setHeaderIfMissing(h http.Header, key, value string) {
	if h.Get(key) != "" {
		return
	}
	h.Set(key, value)
}

func (m *mcpRequestContext) applyOriginalPathHeaders(req *http.Request) {
	setHeaderIfMissing(req.Header, internalapi.OriginalPathHeader, m.originalPath)
	setHeaderIfMissing(req.Header, internalapi.EnvoyOriginalPathHeader, m.originalPath)
}

func (m *mcpRequestContext) applyLogHeaderMappings(req *http.Request, msg jsonrpc.Message) {
	if req == nil || len(m.logRequestHeaderAttributes) == 0 {
		return
	}
	meta := extractMetaFromJSONRPCMessage(msg)
	for header := range m.logRequestHeaderAttributes {
		if value := lang.CaseInsensitiveValue(meta, header); value != "" {
			req.Header.Set(header, value)
			continue
		}
		if m.requestHeaders == nil {
			continue
		}
		if value := m.requestHeaders.Get(header); value != "" {
			req.Header.Set(header, value)
		}
	}
}

func extractMetaFromJSONRPCMessage(msg jsonrpc.Message) map[string]any {
	req, ok := msg.(*jsonrpc.Request)
	if !ok || req == nil || len(req.Params) == 0 {
		return nil
	}
	var params struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil
	}
	return params.Meta
}

// newSession creates a new session for a downstream client.
// It multiplexes the initialize request to all backends defined in the MCPRoute associated with the downstream request.
// startAt is the time when the overall HTTP request started, used for recording request duration metrics.
func (m *mcpRequestContext) newSession(ctx context.Context, p *mcp.InitializeParams, routeName filterapi.MCPRouteName, subject string, span tracingapi.MCPSpan, startAt time.Time) (*session, error) {
	m.l.Debug("creating new MCP session")

	backends := m.routes[routeName]
	if backends == nil {
		return nil, fmt.Errorf("no backends found for route %s", routeName)
	}

	forwardHeaders := extractForwardHeaders(m.requestHeaders, backends.forwardHeaders)

	// spec.backendSelector, if configured, is evaluated once per candidate backend here,
	// at session-initialize time, rather than on every subsequent call in the session.
	// With no selector configured, all backends are considered (unchanged behavior).
	selectedBackends := backends.backends
	if backends.backendSelector != nil {
		filtered := make(map[filterapi.MCPBackendName]filterapi.MCPBackend, len(backends.backends))
		// The JWT and CEL headers are identical for every candidate backend in this loop --
		// only request.mcp.backend changes -- so parse/build them once and reuse across
		// all candidates instead of redoing it per backend.
		authzCtx := m.newAuthzContext(&authorizationRequest{Headers: m.requestHeaders})
		for name, backend := range backends.backends {
			allowed, _ := m.authorizeRequestWith(backends.backendSelector, &authorizationRequest{
				Headers: m.requestHeaders,
				Backend: name,
			}, authzCtx)
			if allowed {
				filtered[name] = backend
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("%w for route %s", errNoMatchingBackendSelector, routeName)
		}
		selectedBackends = filtered
	}

	// Extract per-backend forward headers.
	perBackendHeaders := make(map[filterapi.MCPBackendName]map[string]string)
	for _, backend := range selectedBackends {
		if len(backend.ForwardHeaders) > 0 {
			if h := extractPerBackendForwardHeaders(m.requestHeaders, backend.ForwardHeaders); h != nil {
				perBackendHeaders[backend.Name] = h
			}
		}
	}

	var (
		wg      sync.WaitGroup
		entries []compositeSessionEntry
		counter int
	)
	entries = make([]compositeSessionEntry, len(selectedBackends))

	if m.l.Enabled(ctx, slog.LevelDebug) {
		m.l.Debug("initializing MCP sessions to backends", slog.String("route", routeName), slog.Any("backends", backends))
	}
	for _, backend := range selectedBackends {
		entryIndex := counter
		counter++
		// Initialize sessions to all backends in parallel to reduce the overall latency of session creation.
		wg.Go(func() {
			if m.l.Enabled(ctx, slog.LevelDebug) {
				m.l.Debug("creating MCP session", slog.String("backend", backend.Name))
			}
			backendStartAt := time.Now()
			initResult, err := m.initializeSession(ctx, routeName, backend, p, startAt)
			if err != nil {
				m.l.Error("failed to create MCP session", slog.String("backend", backend.Name), slog.String("error", err.Error()))
				// If one backend fails, don't fail the overall connection. Create a session to the rest of the backends, as they
				// may provide the needed methods.
				// TODO: should we record a metric for this?
				return
			}
			m.metrics.WithBackend(backend.Name).RecordInitializationDuration(ctx, backendStartAt, p)
			if m.l.Enabled(ctx, slog.LevelDebug) {
				m.l.Debug("created MCP session", slog.String("backend", backend.Name), slog.String("session_id", string(initResult.sessionID)))
			}
			if span != nil {
				span.RecordRouteToBackend(backend.Name, string(initResult.sessionID), true)
			}
			entries[entryIndex] = compositeSessionEntry{
				sessionID:       initResult.sessionID,
				backendName:     backend.Name,
				capabilities:    initResult.result.Capabilities,
				protocolVersion: initResult.result.ProtocolVersion,
			}
		})
	}
	wg.Wait()
	// Remove empty entries (failed initializations).
	finalEntries := make([]compositeSessionEntry, 0, len(entries))
	for _, e := range entries {
		// When backendName is empty, it means the initialization failed.
		// As per the comment above, we just skip the failed backends.
		if len(e.backendName) > 0 {
			finalEntries = append(finalEntries, e)
		}
	}
	if len(finalEntries) == 0 {
		// All initializations failed, which means we cannot provide any meaningful operation so we fail the session creation.
		return nil, errors.New("failed to create MCP session to any backend")
	}

	encrypted, err := m.sessionCrypto.Encrypt(string(clientToGatewaySessionIDFromEntries(subject, finalEntries, routeName)))
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt session ID: %w", err)
	}

	// Build perBackendSessions map from finalEntries
	perBackendSessions := make(map[filterapi.MCPBackendName]*compositeSessionEntry, len(finalEntries))
	for i := range finalEntries {
		entry := &finalEntries[i]
		perBackendSessions[entry.backendName] = entry
	}

	return &session{
		reqCtx:                 m,
		id:                     secureClientToGatewaySessionID(encrypted),
		route:                  routeName,
		perBackendSessions:     perBackendSessions,
		extraHeaders:           forwardHeaders,
		perBackendExtraHeaders: perBackendHeaders,
	}, nil
}

// sessionFromID returns the session with the given ID, or error if not found or invalid.
func (m *mcpRequestContext) sessionFromID(id secureClientToGatewaySessionID, lastEvent secureClientToGatewayEventID) (*session, error) {
	decrypted, err := m.sessionCrypto.Decrypt(string(id))
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt session ID: %w", err)
	}

	perBackendSessionIDs, route, err := clientToGatewaySessionID(decrypted).backendSessionIDs()
	if err != nil {
		return nil, err
	}
	if len(lastEvent) != 0 {
		decryptedEventID, err := m.sessionCrypto.Decrypt(string(lastEvent))
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt last event ID: %w", err)
		}
		eventIDs := clientToGatewayEventID(decryptedEventID).backendEventIDs()
		for backend, eventID := range eventIDs {
			entity, ok := perBackendSessionIDs[backend]
			if ok {
				entity.lastEventID = eventID
			}
		}
	}

	// Extract forward headers from the current request based on the route's forwardHeaders config.
	var extraHeaders map[string]string
	var perBackendHeaders map[filterapi.MCPBackendName]map[string]string
	if routeConfig := m.routes[route]; routeConfig != nil {
		extraHeaders = extractForwardHeaders(m.requestHeaders, routeConfig.forwardHeaders)
		// Extract per-backend forward headers.
		perBackendHeaders = make(map[filterapi.MCPBackendName]map[string]string)
		for _, backend := range routeConfig.backends {
			if len(backend.ForwardHeaders) > 0 {
				if h := extractPerBackendForwardHeaders(m.requestHeaders, backend.ForwardHeaders); h != nil {
					perBackendHeaders[backend.Name] = h
				}
			}
		}
	}

	return &session{id: id, route: route, reqCtx: m, perBackendSessions: perBackendSessionIDs, extraHeaders: extraHeaders, perBackendExtraHeaders: perBackendHeaders}, nil
}

type initializeResult struct {
	sessionID gatewayToMCPServerSessionID
	result    *mcp.InitializeResult
}

func (m *mcpRequestContext) initializeSession(ctx context.Context, routeName filterapi.MCPRouteName, backend filterapi.MCPBackend, p *mcp.InitializeParams, startAt time.Time) (*initializeResult, error) {
	// Send the initialize request to the MCP backend listener.
	reqID := mustJSONRPCRequestID()
	var (
		sessionID  string
		initResult *mcp.InitializeResult
	)
	{
		// Scoping per each request to avoid leaking Close.
		initializeReq, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal MCP initialize params: %w", err)
		}
		mcpReq := &jsonrpc.Request{Method: "initialize", Params: initializeReq, ID: reqID}
		resp, err := m.invokeJSONRPCRequest(ctx, routeName, backend, nil, mcpReq, p)
		if err != nil {
			return nil, fmt.Errorf("failed to send MCP initialize request: %w", err)
		}
		defer func() {
			ensureHTTPConnectionReused(resp)
		}()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("MCP initialize request failed with status code %d and body=%s", resp.StatusCode, string(body))
		}
		if m.l.Enabled(ctx, slog.LevelDebug) {
			m.l.Debug("MCP initialize request succeeded",
				slog.String("backend", backend.Name),
				slog.String("content type", resp.Header.Get("Content-Type")),
			)
		}

		// Note: some servers are stateless hence no (==empty) session ID.
		sessionID = resp.Header.Get(sessionIDHeader)
		if m.l.Enabled(ctx, slog.LevelDebug) {
			m.l.Debug("initialized MCP session", slog.String("backend", backend.Name), slog.String("session_id", sessionID))
		}

		var rawMsg jsonrpc.Message
		var sseReader io.Reader = resp.Body
		if resp.Header.Get("Content-Type") != "text/event-stream" {
			body, _ := io.ReadAll(resp.Body)
			msg, ok := tryDecodeJSONRPCMessage(body)
			if ok {
				rawMsg = msg
			} else {
				// Not valid JSON-RPC; fall through to SSE parser with the already-read bytes.
				sseReader = bytes.NewReader(body)
			}
		}
		if rawMsg == nil {
			parser := newSSEEventParser(sseReader, backend.Name)
			var readErr error
			for rawMsg == nil {
				event, parseErr := parser.next()
				// TODO: handle reconnect. We need to re-arrange the event ID so that it will also contain the backend name and the original session ID.
				// 	Since event ID can be arbitrary string, we can shove each backend's last even ID into the event ID just like the session ID.
				if event != nil {
					// Some backends emit non-response events (keep-alives with an
					// empty data line, notifications) before the initialize result.
					// Skip those and keep reading until we find the JSON-RPC response.
					for _, msg := range event.messages {
						if _, ok := msg.(*jsonrpc.Response); ok {
							rawMsg = msg
						}
					}
				}
				if rawMsg != nil {
					// Found the response; a trailing EOF on this same event is not a failure.
					break
				}
				if parseErr != nil {
					readErr = parseErr
					if !errors.Is(parseErr, io.EOF) && !strings.Contains(parseErr.Error(), "context deadline exceeded") {
						m.l.Error("failed to read MCP GET response body", slog.String("error", parseErr.Error()))
					}
					break
				}
			}
			if rawMsg == nil {
				// The SSE stream ended (EOF/deadline) or errored before any JSON-RPC
				// response arrived. Surface a clear error instead of falling through to
				// the misleading "MCP message is not a response: <nil>".
				if readErr != nil && !errors.Is(readErr, io.EOF) && !strings.Contains(readErr.Error(), "context deadline exceeded") {
					return nil, fmt.Errorf("failed to read MCP initialize response from backend %q: %w", backend.Name, readErr)
				}
				return nil, fmt.Errorf("MCP initialize stream from backend %q ended before a JSON-RPC response was received", backend.Name)
			}
		}

		msg, ok := rawMsg.(*jsonrpc.Response)
		if !ok {
			m.l.Warn("MCP message is not a response", slog.String("type", fmt.Sprintf("%T", rawMsg)))
			return nil, fmt.Errorf("MCP message is not a response: %T", rawMsg)
		}
		// TODO: do we need to merge and return the result back?

		err = json.Unmarshal(msg.Result, &initResult)
		if err != nil {
			m.l.Warn("Failed to decode MCP initialize result", slog.String("error", err.Error()))
			return nil, fmt.Errorf("failed to decode MCP initialize result: %w", err)
		}
		if m.l.Enabled(ctx, slog.LevelDebug) {
			m.l.Debug("MCP session initialized", slog.Any("capabilities", initResult.Capabilities))
		}
		backendMetrics := m.metrics.WithBackend(backend.Name)
		backendMetrics.RecordServerCapabilities(ctx, initResult.Capabilities, p)
		backendMetrics.RecordMethodCount(ctx, "initialize", p)
		backendMetrics.RecordRequestDuration(ctx, startAt, p)
	}

	// Need to invoke "notifications/initialized" to complete the initialization.
	{
		// Send the notifications/initialized request to the MCP backend listener.
		mcpReq := &jsonrpc.Request{Method: "notifications/initialized", Params: emptyJSONRPCMessage}
		resp, err := m.invokeJSONRPCRequest(ctx, routeName, backend, &compositeSessionEntry{
			sessionID: gatewayToMCPServerSessionID(sessionID),
		}, mcpReq, p)
		if err != nil {
			return nil, fmt.Errorf("failed to send MCP notifications/initialized request: %w", err)
		}
		defer func() {
			ensureHTTPConnectionReused(resp)
		}()
		if resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("MCP notifications/initialized request failed with status code %d, body=%s", resp.StatusCode, string(body))
		}
		m.metrics.WithBackend(backend.Name).RecordMethodCount(ctx, "notifications/initialized", p)
	}
	if m.l.Enabled(ctx, slog.LevelDebug) {
		m.l.Debug("sent MCP notifications/initialized", slog.String("backend", backend.Name), slog.String("session_id", sessionID))
	}
	return &initializeResult{
		sessionID: gatewayToMCPServerSessionID(sessionID),
		result:    initResult,
	}, nil
}

func (m *mcpRequestContext) invokeJSONRPCRequest(ctx context.Context, routeName filterapi.MCPRouteName, backend filterapi.MCPBackend, cse *compositeSessionEntry, msg jsonrpc.Message, params mcp.Params) (*http.Response, error) {
	encoded, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode MCP message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.backendListenerAddr, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP notifications/initialized request: %w", err)
	}
	addMCPHeaders(req, msg, params, routeName, backend.Name)
	m.applyLogHeaderMappings(req, msg)
	m.applyOriginalPathHeaders(req)
	if cse != nil {
		if len(cse.sessionID) > 0 {
			req.Header.Set(sessionIDHeader, string(cse.sessionID))
		}
		if len(cse.lastEventID) > 0 {
			req.Header.Set(lastEventIDHeader, cse.lastEventID)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	// Forward configured headers to backend.
	if routeConfig := m.routes[routeName]; routeConfig != nil {
		// Route-level headers (e.g., OAuth claimToHeaders).
		for _, header := range routeConfig.forwardHeaders {
			if value := m.requestHeaders.Get(header); value != "" {
				req.Header.Set(header, value)
			}
		}
		// Per-backend headers (from MCPRouteBackendRef.forwardHeaders) with optional renaming.
		if b, ok := routeConfig.backends[backend.Name]; ok {
			for _, fh := range b.ForwardHeaders {
				if value := m.requestHeaders.Get(fh.Name); value != "" {
					req.Header.Set(fh.ForwardName(), value)
				}
			}
		}
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send MCP notifications/initialized request: %w", err)
	}
	return resp, nil
}

func (m *mcpRequestContext) getBackendForRoute(route, backend filterapi.MCPBackendName) (filterapi.MCPBackend, error) {
	r := m.routes[route]
	if r == nil {
		return filterapi.MCPBackend{}, fmt.Errorf("no route found for %q", route)
	}
	b, ok := r.backends[backend]
	if !ok {
		return filterapi.MCPBackend{}, fmt.Errorf("no backend found for %q in route %q", backend, route)
	}
	return b, nil
}

func mustJSONRPCRequestID() jsonrpc.ID {
	id, err := jsonrpc.MakeID(uuid.NewString())
	if err != nil {
		panic(err)
	}
	return id
}

// ensureHTTPConnectionReused reads and closes the response body to allow connection reuse.
// The following comment is on [http.Response.Body]:
//
//	The default HTTP client's Transport may not
//	reuse HTTP/1.x "keep-alive" TCP connections if the Body is
//	not read to completion and closed.
func ensureHTTPConnectionReused(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
