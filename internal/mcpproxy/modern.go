// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
//
//nolint:unused // TODO: remove this once full era dispatch wiring is enabled.
package mcpproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// errModernListNoBackends is returned when list fan-out is invoked with an empty
// backend set (e.g. nothing selected) or every selected backend failed.
var errModernListNoBackends = errors.New("list request failed for all backends")

const (
	defaultTTLMs      = 0
	defaultCacheScope = "public"
)

// serveModernPOST handles modern (2026-07-28) stateless POST requests.
// This is the Phase 1 entry point for modern clients talking to modern backends.
// The JSON-RPC request has already been parsed by servePOST.
//
// startAt is the time when the overall HTTP request started, used for recording
// request duration metrics.
func (m *mcpRequestContext) serveModernPOST(w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, startAt time.Time) {
	var (
		ctx     = r.Context()
		err     error
		errType metrics.MCPErrorType
		result  handlerResult
		span    tracingapi.MCPSpan
		params  mcp.Params
	)
	// Arguments are captured in the closure so they are read after the handler
	// returns, not at defer registration. session is nil on the stateless
	// modern path; params may also be nil there.
	defer func() {
		m.recordPOSTCompletion(&postCompletion{
			ctx:     ctx,
			method:  req.Method,
			errType: errType,
			err:     err,
			startAt: startAt,
			span:    span,
			result:  result,
			params:  params,
		})
	}()

	// The Envoy frontend listener's HTTPRouteFilter adds the route name header.
	// The modern path is stateless, so every request needs it to locate the
	// route's backends (legacy recovers the route from the session ID after
	// initialize). A missing header is a gateway wiring bug, not a client error.
	route := r.Header.Get(internalapi.MCPRouteHeader)
	if route == "" {
		m.l.Error("missing route header on modern request")
		errType = metrics.MCPErrorInternal
		err = errors.New("missing route header")
		onErrorResponse(w, http.StatusInternalServerError, "missing route header")
		return
	}

	headerMethod := r.Header.Get(mcpMethodHeader)
	if headerMethod != req.Method {
		errType = metrics.MCPErrorInvalidJSONRPC
		err = fmt.Errorf("Mcp-Method header mismatch")
		onErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("Mcp-Method header '%s' does not match body method '%s'", headerMethod, req.Method))
		return
	}

	switch req.Method {
	case "initialize", "notifications/initialized":
		errType = metrics.MCPErrorUnsupportedMethod
		err = fmt.Errorf("method removed in 2026-07-28: %s", req.Method)
		onErrorResponse(w, http.StatusNotFound, "method removed in 2026-07-28: use server/discover")
		return
	case "ping":
		errType = metrics.MCPErrorUnsupportedMethod
		err = errors.New("ping removed in 2026-07-28")
		onErrorResponse(w, http.StatusNotFound, "ping removed in 2026-07-28")
		return
	case "logging/setLevel":
		errType = metrics.MCPErrorUnsupportedMethod
		err = errors.New("logging/setLevel removed in 2026-07-28")
		onErrorResponse(w, http.StatusNotFound, "logging/setLevel removed in 2026-07-28; use _meta logLevel")
		return
	}

	// The incoming request is the source of truth for backendSelector,
	// forwardHeaders, and per-tool authorization. Tests that invoke handlers
	// directly still set m.requestHeaders themselves.
	m.requestHeaders = r.Header

	// Dispatch based on method. Tracing spans are started only for supported
	// methods, matching serveLegacyPOST: parseParamsAndMaybeStartSpan is called
	// inside each case so unknown/removed methods never open a span.
	// Fan-out handlers additionally record per-backend routing via RecordRouteToBackend.
	switch req.Method {
	case "server/discover":
		p := &mcp.DiscoverParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onErrorResponse(w, http.StatusBadRequest, "invalid params")
			return
		}
		params = p
		result, err = m.handleServerDiscover(ctx, w, req, route, span)
	case "tools/list":
		p := &mcp.ListToolsParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onErrorResponse(w, http.StatusBadRequest, "invalid params")
			return
		}
		params = p
		result, err = m.handleModernToolsList(ctx, w, r, req, route, span)
	case "resources/list":
		p := &mcp.ListResourcesParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onErrorResponse(w, http.StatusBadRequest, "invalid params")
			return
		}
		params = p
		result, err = m.handleModernResourcesList(ctx, w, r, req, route, span)
	case "resources/templates/list":
		p := &mcp.ListResourceTemplatesParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onErrorResponse(w, http.StatusBadRequest, "invalid params")
			return
		}
		params = p
		result, err = m.handleModernResourceTemplatesList(ctx, w, r, req, route, span)
	case "prompts/list":
		p := &mcp.ListPromptsParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onErrorResponse(w, http.StatusBadRequest, "invalid params")
			return
		}
		params = p
		result, err = m.handleModernPromptsList(ctx, w, r, req, route, span)
	default:
		errType = metrics.MCPErrorUnsupportedMethod
		err = fmt.Errorf("unknown method: %s", req.Method)
		onErrorResponse(w, http.StatusNotFound, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}
	if errType == "" {
		errType = errorType(err)
	}
}

// resolveModernRouteBackends looks up the route and evaluates backendSelector
// against this request. The returned map is the only backend set discovery and
// list fan-out may talk to. Writes 404 (unknown route) or 403 (no matching
// backends) on failure.
//
// Header extraction matches newSession: route-level forwardHeaders are read
// before backendSelector, then per-backend ForwardHeaders are read for the
// selected set only.
func (m *mcpRequestContext) resolveModernRouteBackends(w http.ResponseWriter, route filterapi.MCPRouteName) (*mcpProxyConfigRoute, map[filterapi.MCPBackendName]filterapi.MCPBackend, error) {
	routeConfig, ok := m.routes[route]
	if !ok {
		onErrorResponse(w, http.StatusNotFound, "route not found")
		return nil, nil, fmt.Errorf("%w: %s", errBackendNotFound, route)
	}

	// 1. extract route level forward headers
	m.extraHeaders = extractForwardHeaders(m.requestHeaders, routeConfig.forwardHeaders)

	// 2. select authorized backends
	selected, err := m.selectAuthorizedBackends(route, routeConfig)
	if err != nil {
		onErrorResponse(w, http.StatusForbidden, "access denied")
		return nil, nil, err
	}

	// 3. extract per-backend forward headers
	m.perBackendExtraHeaders = m.extractPerBackendHeaders(selected)
	m.forwardHeadersResolved = true
	return routeConfig, selected, nil
}

// handleServerDiscover fans out server/discover to selected backends, merges results.
//
// This is a fan-out handler: it records per-backend metrics itself and sets
// perBackendMetricsRecorded so the generic recording in serveModernPOST is
// skipped.
func (m *mcpRequestContext) handleServerDiscover(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true
	_, selectedBackends, err := m.resolveModernRouteBackends(w, route)
	if err != nil {
		return handlerResult{}, err
	}

	var results []*mcp.DiscoverResult
	for _, backend := range selectedBackends {
		backendStartAt := time.Now()
		result, err := m.discoverBackend(ctx, route, backend)
		backendMetrics := m.metrics.WithBackend(backend.Name)
		if err != nil {
			m.l.Warn("server/discover failed for backend",
				slog.String("backend", backend.Name),
				slog.String("error", err.Error()))
			backendMetrics.RecordMethodErrorCount(ctx, req.Method, nil, metrics.MCPStatusError)
			backendMetrics.RecordRequestErrorDuration(ctx, backendStartAt, errorType(err), nil)
			continue
		}
		if span != nil {
			span.RecordRouteToBackend(backend.Name, "", true)
		}
		backendMetrics.RecordMethodCount(ctx, req.Method, nil)
		backendMetrics.RecordRequestDuration(ctx, backendStartAt, nil)
		results = append(results, result)
	}
	if len(results) == 0 {
		m.l.Error("server/discover failed for all backends", slog.String("route", route))
		onErrorResponse(w, http.StatusInternalServerError, "failed to discover any backend")
		return handlerResult{}, errors.New("failed to discover any backend")
	}
	merged := mergeDiscoverResults(m.l, results)
	merged.Instructions = fmt.Sprintf("Agent Router — MCP proxy aggregating %d backends", len(selectedBackends))
	writeJSONRPCResult(w, req.ID, merged)
	return handlerResult{}, nil
}

// discoverBackend sends a server/discover request to a single backend.
//
// TODO: this used to consult a per-instance capabilityCache to avoid a
// server/discover round-trip to every backend on every aggregated request. The
// cache was removed until we have a multiplexing-aware caching strategy (see the
// TODO on defaultTTLMs/defaultCacheScope in era.go), so every discover currently
// hits the backend live.
func (m *mcpRequestContext) discoverBackend(ctx context.Context, route filterapi.MCPRouteName, backend filterapi.MCPBackend) (*mcp.DiscoverResult, error) {
	id, _ := jsonrpc.MakeID(fmt.Sprintf("gw-discover-%s-%d", backend.Name, time.Now().UnixNano()))
	req := &jsonrpc.Request{
		ID:     id,
		Method: "server/discover",
		Params: discoverParams(),
	}

	resultRaw, err := m.sendModernRequest(ctx, req, route, backend)
	if err != nil {
		return nil, fmt.Errorf("server/discover request failed: %w", err)
	}

	var result mcp.DiscoverResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal DiscoverResult: %w", err)
	}

	return &result, nil
}

func discoverParams() []byte {
	return []byte(`{"_meta":{` +
		`"` + metaProtocolVersion + `":"` + protocolVersion20260728 + `",` +
		`"` + metaClientInfo + `":{"name":"agent-router","version":"1.0.0"},` +
		`"` + metaClientCapabilities + `":{}` +
		`}}`)
}

// mergeDiscoverResults merges multiple DiscoverResult from route backends into
// a single DiscoverResult representing the gateway's aggregated capabilities.
//
// Capabilities are aggregated using the same union/OR semantics as the stateful
// initialize path
func mergeDiscoverResults(l *slog.Logger, results []*mcp.DiscoverResult) *mcp.DiscoverResult {
	caps := make([]*mcp.ServerCapabilities, 0, len(results))
	cacheables := make([]mcp.Cacheable, 0, len(results))
	backends := make([]backendReportedVersions, 0, len(results))
	for i, r := range results {
		if r == nil {
			continue
		}
		caps = append(caps, r.Capabilities)
		cacheables = append(cacheables, r.Cacheable)
		backends = append(backends, backendReportedVersions{
			name:     fmt.Sprintf("backend[%d]", i),
			versions: r.SupportedVersions,
		})
	}
	ttlMs, cacheScope := mergeCachingHintsFromBackends(cacheables)

	// Negotiate a single protocol version across backends using the same
	// shared logic as the legacy initialize path. The modern flow always
	// speaks 2026-07-28 with clients, so cap the negotiation at that version.
	versions := []string{mergedProtocolVersion(l, protocolVersion20260728, backends)}
	return &mcp.DiscoverResult{
		SupportedVersions: versions,
		Capabilities:      unionServerCapabilities(caps),
		TTLMs:             ttlMs,
		CacheScope:        cacheScope,
	}
}

// handleModernToolsList handles tools/list on the modern stateless path (P1.7).
//
// Fan-out handler: records per-backend metrics itself and sets
// perBackendMetricsRecorded.
func (m *mcpRequestContext) handleModernToolsList(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true

	// mergeToolsList reads per-caller headers from m.requestHeaders for authorization.
	// In production this is set at construction (== r.Header); ensure it is populated
	// even when this handler is invoked directly (e.g. in tests) so auth stays enforced.
	m.requestHeaders = r.Header

	_, selected, err := m.resolveModernRouteBackends(w, route)
	if err != nil {
		return handlerResult{}, err
	}

	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListToolsResult](ctx, m, req, route, selected, span)
	if err != nil {
		onErrorResponse(w, http.StatusInternalServerError, "failed to list tools for all backends")
		return handlerResult{}, err
	}
	result := m.mergeToolsList(&session{route: route}, responses)
	if span != nil {
		span.RecordListResult(result)
		span.AddEvent(req.Method + " aggregation end")
	}
	writeJSONRPCResult(w, req.ID, &result)
	return handlerResult{}, nil
}

// handleModernResourcesList handles resources/list (P1.7 fan-out).
//
// Fan-out handler: records per-backend metrics itself and sets
// perBackendMetricsRecorded.
func (m *mcpRequestContext) handleModernResourcesList(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true
	m.requestHeaders = r.Header

	_, selected, err := m.resolveModernRouteBackends(w, route)
	if err != nil {
		return handlerResult{}, err
	}

	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListResourcesResult](ctx, m, req, route, selected, span)
	if err != nil {
		onErrorResponse(w, http.StatusInternalServerError, "failed to list resources for all backends")
		return handlerResult{}, err
	}
	result := m.mergeResourceList(&session{route: route}, responses)
	if span != nil {
		span.RecordListResult(result)
		span.AddEvent(req.Method + " aggregation end")
	}
	writeJSONRPCResult(w, req.ID, &result)
	return handlerResult{}, nil
}

// handleModernResourceTemplatesList handles resources/templates/list (P1.7 fan-out).
// Fans out to all backends and namespaces each template's uriTemplate with the
// backend prefix, mirroring resources/list.
func (m *mcpRequestContext) handleModernResourceTemplatesList(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true
	m.requestHeaders = r.Header

	_, selected, err := m.resolveModernRouteBackends(w, route)
	if err != nil {
		return handlerResult{}, err
	}
	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListResourceTemplatesResult](ctx, m, req, route, selected, span)
	if err != nil {
		onErrorResponse(w, http.StatusInternalServerError, "failed to list resource templates for all backends")
		return handlerResult{}, err
	}
	result := m.mergeResourcesTemplateList(&session{route: route}, responses)
	if span != nil {
		span.RecordListResult(result)
		span.AddEvent(req.Method + " aggregation end")
	}
	writeJSONRPCResult(w, req.ID, &result)
	return handlerResult{}, nil
}

// handleModernPromptsList handles prompts/list (P1.7 fan-out).
//
// Fan-out handler: records per-backend metrics itself and sets
// perBackendMetricsRecorded.
func (m *mcpRequestContext) handleModernPromptsList(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true
	m.requestHeaders = r.Header

	_, selected, err := m.resolveModernRouteBackends(w, route)
	if err != nil {
		return handlerResult{}, err
	}

	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListPromptsResult](ctx, m, req, route, selected, span)
	if err != nil {
		onErrorResponse(w, http.StatusInternalServerError, "failed to list prompts for all backends")
		return handlerResult{}, err
	}
	result := m.mergePromptsList(&session{route: route}, responses)
	if span != nil {
		span.RecordListResult(result)
		span.AddEvent(req.Method + " aggregation end")
	}
	writeJSONRPCResult(w, req.ID, &result)
	return handlerResult{}, nil
}

// sendToAllModernBackendsAndAggregateResponses fans out a modern (stateless) list request
// to the already-selected backend set and collects each backend's result unmarshaled into T.
// Backends that fail the request or whose result cannot be unmarshaled are logged and skipped,
// mirroring the "partial failure is non-fatal" behavior of the legacy aggregation
// path (sendToAllBackendsAndAggregateResponses).
//
// Returns errModernListNoBackends when backends is empty or every backend fails — callers
// should surface that as a hard error rather than an empty successful list.
//
// Callers must pass the set from resolveModernRouteBackends / selectBackends; this
// function does not re-evaluate backendSelector.
//
// The returned []broadCastResponse[T] is intentionally shaped like the legacy
// aggregation input so the modern handlers can reuse the same merge* functions
// (mergeToolsList, mergeResourceList, ...) and avoid drifting from the legacy
// prefixing/filtering/authorization/caching-hint logic.
func sendToAllModernBackendsAndAggregateResponses[T any](ctx context.Context, m *mcpRequestContext, req *jsonrpc.Request, route filterapi.MCPRouteName, backends map[filterapi.MCPBackendName]filterapi.MCPBackend, span tracingapi.MCPSpan) ([]broadCastResponse[T], error) {
	if span != nil {
		span.AddEvent(req.Method + " aggregation begin")
	}
	if len(backends) == 0 {
		m.l.Error(fmt.Sprintf("%s has no backends selected", req.Method), slog.String("route", route))
		return nil, fmt.Errorf("%w: %s has no backends selected for route %s", errModernListNoBackends, req.Method, route)
	}
	responses := make([]broadCastResponse[T], 0, len(backends))
	for backendName, backend := range backends {
		backendStartAt := time.Now()
		backendMetrics := m.metrics.WithBackend(backendName)
		resp, err := m.sendModernRequest(ctx, req, route, backend)
		if err != nil {
			m.l.Warn("modern list request failed for backend",
				slog.String("method", req.Method),
				slog.String("backend", backendName),
				slog.String("error", err.Error()))
			backendMetrics.RecordMethodErrorCount(ctx, req.Method, nil, metrics.MCPStatusError)
			backendMetrics.RecordRequestErrorDuration(ctx, backendStartAt, errorType(err), nil)
			continue
		}
		var result T
		if err := json.Unmarshal(resp, &result); err != nil {
			m.l.Warn("failed to unmarshal modern list response from backend",
				slog.String("method", req.Method),
				slog.String("backend", backendName),
				slog.String("error", err.Error()))
			backendMetrics.RecordMethodErrorCount(ctx, req.Method, nil, metrics.MCPStatusError)
			backendMetrics.RecordRequestErrorDuration(ctx, backendStartAt, metrics.MCPErrorInternal, nil)
			continue
		}
		if span != nil {
			span.RecordRouteToBackend(backendName, "", true)
		}
		backendMetrics.RecordMethodCount(ctx, req.Method, nil)
		backendMetrics.RecordRequestDuration(ctx, backendStartAt, nil)
		responses = append(responses, broadCastResponse[T]{backendName: backendName, res: result})
	}
	if len(responses) == 0 {
		m.l.Error(fmt.Sprintf("%s failed for all backends", req.Method), slog.String("route", route))
		return nil, fmt.Errorf("%w: %s failed for all backends on route %s", errModernListNoBackends, req.Method, route)
	}
	return responses, nil
}

// sendModernRequest sends a JSON-RPC request to a modern backend with proper headers (P1.6).
// Returns the raw result JSON. Handles both plain JSON and SSE response formats.
func (m *mcpRequestContext) sendModernRequest(ctx context.Context, req *jsonrpc.Request, route filterapi.MCPRouteName, backend filterapi.MCPBackend) (json.RawMessage, error) {
	body, err := jsonrpc.EncodeMessage(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.backendListenerAddr, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Keep modern forwarding behavior aligned with the legacy proxy path:
	// backend/route metadata headers, optional log/header mappings, original path,
	// and the forwardHeaders extracted in resolveModernRouteBackends / newSession.
	addMCPHeaders(httpReq, req, modernParamsForHeaderMetadata(req), route, backend.Name)
	m.applyLogHeaderMappings(httpReq, req)
	m.applyOriginalPathHeaders(httpReq)
	m.applyForwardHeaders(httpReq, route, backend)

	// P1.6: Set modern headers. No Mcp-Session-Id, no Last-Event-Id.
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, application/json")
	httpReq.Header.Set(mcpProtocolVersionHeader, protocolVersion20260728)
	httpReq.Header.Set(mcpMethodHeader, req.Method)
	httpReq.Header.Set(internalapi.MCPBackendHeader, backend.Name)
	httpReq.Header.Set(internalapi.MCPRouteHeader, route)

	// Set Mcp-Name if applicable.
	if name := extractNameForMethod(req.Method, json.RawMessage(req.Params)); name != "" {
		httpReq.Header.Set(mcpNameHeader, name)
	} else if req.Method == "tools/call" {
		return nil, fmt.Errorf("invalid tools/call params: missing required name")
	}

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("backend returned status %d: %s", resp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Determine response format: SSE or plain JSON.
	contentType := resp.Header.Get("Content-Type")
	var jsonPayload []byte

	if strings.Contains(contentType, "text/event-stream") || strings.HasPrefix(string(respBody), "event:") || strings.HasPrefix(string(respBody), "data:") {
		// Parse SSE: find the last "data:" line which contains the JSON-RPC response.
		jsonPayload = extractJSONFromSSE(respBody)
		if jsonPayload == nil {
			return nil, fmt.Errorf("no JSON-RPC response found in SSE stream")
		}
	} else {
		jsonPayload = respBody
	}

	// Parse JSON-RPC response. Use map to avoid jsonrpc.ID unmarshal issues with sonic.
	var rpcResp map[string]json.RawMessage
	if err := json.Unmarshal(jsonPayload, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w (body: %.200s)", err, string(jsonPayload))
	}
	if err := validateModernJSONRPCResponse(req, rpcResp); err != nil {
		return nil, err
	}
	return rpcResp["result"], nil
}

// validateModernJSONRPCResponse checks the minimum response shape needed for
// safe aggregation: the response id matches the request, and exactly one of
// result or error is present (non-null). It does not require modern result
// fields such as resultType; the gateway injects those on the client-facing
// response when needed.
func validateModernJSONRPCResponse(req *jsonrpc.Request, rpcResp map[string]json.RawMessage) error {
	wantID, err := json.Marshal(req.ID.Raw())
	if err != nil {
		return fmt.Errorf("marshal request id: %w", err)
	}
	gotID, ok := rpcResp["id"]
	if !ok || string(gotID) == "null" {
		return fmt.Errorf("backend response missing id")
	}
	if string(gotID) != string(wantID) {
		return fmt.Errorf("response id mismatch: got %s, want %s", gotID, wantID)
	}

	result, hasResult := rpcResp["result"]
	resultPresent := hasResult && string(result) != "null"
	errField, hasError := rpcResp["error"]
	errorPresent := hasError && string(errField) != "null"

	switch {
	case resultPresent && errorPresent:
		return fmt.Errorf("backend response has both result and error")
	case errorPresent:
		return fmt.Errorf("backend error: %s", string(errField))
	case !resultPresent:
		return fmt.Errorf("backend returned no result")
	}
	return nil
}

// extractJSONFromSSE parses an SSE response body and extracts the last JSON-RPC
// message from "data:" lines. MCP backends return the final result as the last event.
func extractJSONFromSSE(body []byte) []byte {
	var lastData []byte
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			data := strings.TrimSpace(after)
			if data != "" && data != "[DONE]" {
				lastData = []byte(data)
			}
		}
	}
	return lastData
}

// extractNameForMethod returns the name/uri field for methods that require Mcp-Name header.
func extractNameForMethod(method string, params json.RawMessage) string {
	if params == nil {
		return ""
	}
	switch method {
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(params, &p) == nil {
			return p.Name
		}
	case "prompts/get":
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(params, &p) == nil {
			return p.Name
		}
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(params, &p) == nil {
			return p.URI
		}
	}
	return ""
}

// modernParamsForHeaderMetadata best-effort parses params for methods where
// addMCPHeaders can enrich upstream metadata (tool name/resource URI).
func modernParamsForHeaderMetadata(req *jsonrpc.Request) mcp.Params {
	if req == nil || req.Params == nil {
		return nil
	}
	switch req.Method {
	case "tools/call":
		var p mcp.CallToolParams
		if json.Unmarshal(req.Params, &p) == nil {
			return &p
		}
	case "resources/read":
		var p mcp.ReadResourceParams
		if json.Unmarshal(req.Params, &p) == nil {
			return &p
		}
	case "resources/subscribe":
		var p mcp.SubscribeParams
		if json.Unmarshal(req.Params, &p) == nil {
			return &p
		}
	case "resources/unsubscribe":
		var p mcp.UnsubscribeParams
		if json.Unmarshal(req.Params, &p) == nil {
			return &p
		}
	}
	return nil
}

func writeJSONRPCResult(w http.ResponseWriter, id jsonrpc.ID, result any) {
	encoded, _ := json.Marshal(result)
	writeRawJSONRPCResult(w, id, encoded)
}

func writeRawJSONRPCResult(w http.ResponseWriter, id jsonrpc.ID, result json.RawMessage) {
	// P1.12: Ensure resultType: "complete" is present.
	result = ensureResultType(result)

	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id.Raw(),
		"result":  result,
	}
	encoded, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

// ensureResultType injects "resultType":"complete" if the field is absent (P1.12).
func ensureResultType(result json.RawMessage) json.RawMessage {
	if result == nil {
		return []byte(`{"resultType":"complete"}`)
	}
	var check map[string]json.RawMessage
	if json.Unmarshal(result, &check) != nil {
		return result
	}
	if _, ok := check["resultType"]; ok {
		return result // Already has resultType (could be "input_required" from MRTR).
	}
	check["resultType"] = json.RawMessage(`"complete"`)
	out, _ := json.Marshal(check)
	return out
}

// applyMergedCachingHints copies the most-restrictive ttlMs/cacheScope from
// backend responses onto a gateway-aggregated list result. SEP-2549 is
// backward compatible: servers MAY omit the fields (clients treat missing
// ttlMs as 0), and clients that do not understand them ignore extra result
// properties. Shared merge* functions therefore apply hints on both the
// legacy and modern list paths.
//
// https://modelcontextprotocol.io/seps/2549-TTL-for-list-results#backward-compatibility
func applyMergedCachingHints[T interface {
	GetTTLMs() int
	GetCacheScope() string
}](dst *mcp.Cacheable, responses []broadCastResponse[T]) {
	cacheables := make([]mcp.Cacheable, 0, len(responses))
	for _, r := range responses {
		cacheables = append(cacheables, mcp.Cacheable{
			TTLMs:      r.res.GetTTLMs(),
			CacheScope: r.res.GetCacheScope(),
		})
	}
	ttlMs, cacheScope := mergeCachingHintsFromBackends(cacheables)
	dst.TTLMs = ttlMs
	dst.CacheScope = cacheScope
}

// mergeCachingHintsFromBackends merges caching hints from multiple backends.
//
// Caching hints (SEP-2549) apply to complete results for server/discover,
// tools/list, prompts/list, resources/list, resources/templates/list, and
// resources/read. Modern servers MUST send them; older servers MAY omit them,
// in which case missing ttlMs is treated as 0 (immediately stale).
//
// TODO: come up with a proper multiplexing-aware caching strategy. Ideal way
// would be to coalesce and shield the backends using an internal cache slice
// so that fan-out requests don't crush downstream servers every time the shortest TTL expires.
func mergeCachingHintsFromBackends(results []mcp.Cacheable) (int, string) {
	ttlMs := defaultTTLMs
	cacheScope := defaultCacheScope
	hasTTL := false
	for _, result := range results {
		backendTTL := max(result.TTLMs, 0)
		if !hasTTL || backendTTL < ttlMs {
			ttlMs = backendTTL
			hasTTL = true
		}

		// Use the strictest scope across backends. Any non-public scope falls back
		// to private to prevent proxy responses from being cached too broadly.
		if result.CacheScope != "" && result.CacheScope != defaultCacheScope {
			cacheScope = "private"
		}
	}
	if len(results) == 0 {
		return defaultTTLMs, defaultCacheScope
	}
	return ttlMs, cacheScope
}

// applyForwardHeaders attaches the headers extracted for this request onto the
// outbound backend call. resolveModernRouteBackends extracts them first; this
// falls back to extracting from the current request when sendModernRequest is
// invoked directly (tests).
func (m *mcpRequestContext) applyForwardHeaders(httpReq *http.Request, route filterapi.MCPRouteName, backend filterapi.MCPBackend) {
	if !m.forwardHeadersResolved {
		if routeConfig := m.routes[route]; routeConfig != nil {
			m.extraHeaders = extractForwardHeaders(m.requestHeaders, routeConfig.forwardHeaders)
			m.perBackendExtraHeaders = m.extractPerBackendHeaders(routeConfig.backends)
			m.forwardHeadersResolved = true
		}
	}
	var perBackend map[string]string
	if m.perBackendExtraHeaders != nil {
		perBackend = m.perBackendExtraHeaders[backend.Name]
	}
	applyExtractedForwardHeaders(httpReq, m.extraHeaders, perBackend)
}
