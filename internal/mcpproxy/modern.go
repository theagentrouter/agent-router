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
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	// resources/read results are typically user-specific, so when a backend
	// omits cacheScope the gateway defaults to "private" per the caching SEP:
	// https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/caching#cache-scope-field
	defaultResourcesReadCacheScope = "private"
)

// subscriptionListenIntent captures the client's original subscriptions/listen
// opt-ins so outbound notifications can be filtered after backend fan-out.
type subscriptionListenIntent struct {
	resourceURIs         map[string]struct{} // gateway-namespaced URIs the client asked for
	toolsListChanged     bool
	promptsListChanged   bool
	resourcesListChanged bool
}

// backendJSONRPCError wraps a raw JSON-RPC error object returned by a backend.
// It preserves the structured error so it can be forwarded to the client as-is
// rather than being stringified into a text/plain 500 response.
type backendJSONRPCError struct {
	raw json.RawMessage
}

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
		onRequestError(w, http.StatusInternalServerError, -32603, "missing route header", req.ID)
		return
	}

	headerMethod := r.Header.Get(mcpMethodHeader)
	if headerMethod != req.Method {
		errType = metrics.MCPErrorInvalidJSONRPC
		err = fmt.Errorf("Mcp-Method header mismatch")
		onRequestError(w, http.StatusBadRequest, errCodeHeaderMismatch, fmt.Sprintf("Mcp-Method header '%s' does not match body method '%s'", headerMethod, req.Method), req.ID)
		return
	}

	switch req.Method {
	case "initialize", "notifications/initialized":
		errType = metrics.MCPErrorUnsupportedMethod
		err = fmt.Errorf("method removed in 2026-07-28: %s", req.Method)
		onRequestError(w, http.StatusNotFound, errCodeMethodNotFound, "method removed in 2026-07-28: use server/discover", req.ID)
		return
	case "ping":
		errType = metrics.MCPErrorUnsupportedMethod
		err = errors.New("ping removed in 2026-07-28")
		onRequestError(w, http.StatusNotFound, errCodeMethodNotFound, "ping removed in 2026-07-28", req.ID)
		return
	case "logging/setLevel":
		errType = metrics.MCPErrorUnsupportedMethod
		err = errors.New("logging/setLevel removed in 2026-07-28")
		onRequestError(w, http.StatusNotFound, errCodeMethodNotFound, "logging/setLevel removed in 2026-07-28; use _meta logLevel", req.ID)
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
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleServerDiscover(ctx, w, req, route, span)
	case "tools/list":
		p := &mcp.ListToolsParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernToolsList(ctx, w, r, req, route, span)
	case "resources/list":
		p := &mcp.ListResourcesParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernResourcesList(ctx, w, r, req, route, span)
	case "resources/templates/list":
		p := &mcp.ListResourceTemplatesParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernResourceTemplatesList(ctx, w, r, req, route, span)
	case "prompts/list":
		p := &mcp.ListPromptsParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernPromptsList(ctx, w, r, req, route, span)
	case "tools/call":
		p := &mcp.CallToolParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernToolsCall(ctx, w, r, req, route, span)
	case "resources/read":
		p := &mcp.ReadResourceParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernResourcesRead(ctx, w, r, req, route, span)
	case "prompts/get":
		p := &mcp.GetPromptParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernPromptsGet(ctx, w, r, req, route, span)
	case "subscriptions/listen":
		p := &mcp.SubscriptionsListenParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleSubscriptionsListen(ctx, w, r, req, route, span)
	case "completion/complete":
		p := &mcp.CompleteParams{}
		span, err = parseParamsAndMaybeStartSpan(ctx, m, req, p, r.Header)
		if err != nil {
			errType = metrics.MCPErrorInvalidParam
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid params", req.ID)
			return
		}
		params = p
		result, err = m.handleModernComplete(ctx, w, r, req, route, span)
	default:
		// Client→server notifications are fire-and-forget: they carry no id and
		// must never receive a response body. Accept them silently so the gateway
		// doesn't reject valid notifications (e.g. notifications/progress) that
		// it doesn't need to forward. Non-notification unknown methods are still
		// rejected with 404.
		if strings.HasPrefix(req.Method, "notifications/") && !req.ID.IsValid() {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		errType = metrics.MCPErrorUnsupportedMethod
		err = fmt.Errorf("unknown method: %s", req.Method)
		onRequestError(w, http.StatusNotFound, errCodeMethodNotFound, fmt.Sprintf("unknown method: %s", req.Method), req.ID)
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
func (m *mcpRequestContext) resolveModernRouteBackends(w http.ResponseWriter, route filterapi.MCPRouteName, id jsonrpc.ID) (*mcpProxyConfigRoute, map[filterapi.MCPBackendName]filterapi.MCPBackend, error) {
	routeConfig, ok := m.routes[route]
	if !ok {
		onRequestError(w, http.StatusNotFound, errCodeInvalidParams, "route not found", id)
		return nil, nil, fmt.Errorf("%w: %s", errBackendNotFound, route)
	}

	// 1. extract route level forward headers
	m.extraHeaders = extractForwardHeaders(m.requestHeaders, routeConfig.forwardHeaders)

	// 2. select authorized backends
	selected, err := m.selectAuthorizedBackends(route, routeConfig)
	if err != nil {
		onRequestError(w, http.StatusForbidden, errCodeInvalidRequest, "access denied", id)
		return nil, nil, err
	}

	// 3. extract per-backend forward headers
	m.perBackendExtraHeaders = m.extractPerBackendHeaders(selected)
	m.forwardHeadersResolved = true
	return routeConfig, selected, nil
}

// lookupSelectedBackend returns the named backend from the already-selected
// set produced by resolveModernRouteBackends. Writes 403 if the backend is on
// the route but excluded by backendSelector, or 404 if it is unknown.
func lookupSelectedBackend(w http.ResponseWriter, routeConfig *mcpProxyConfigRoute, selected map[filterapi.MCPBackendName]filterapi.MCPBackend, backendName string, id jsonrpc.ID) (filterapi.MCPBackend, error) {
	if backend, ok := selected[backendName]; ok {
		return backend, nil
	}
	if _, onRoute := routeConfig.backends[backendName]; onRoute {
		onRequestError(w, http.StatusForbidden, errCodeInvalidRequest, "access denied", id)
		return filterapi.MCPBackend{}, errors.New("authorization failed")
	}
	onRequestError(w, http.StatusNotFound, errCodeInvalidParams, fmt.Sprintf("unknown backend %s", backendName), id)
	return filterapi.MCPBackend{}, fmt.Errorf("%w: %s", errBackendNotFound, backendName)
}

// handleServerDiscover fans out server/discover to selected backends, merges results.
//
// This is a fan-out handler: it records per-backend metrics itself and sets
// perBackendMetricsRecorded so the generic recording in serveModernPOST is
// skipped.
func (m *mcpRequestContext) handleServerDiscover(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true
	_, selectedBackends, err := m.resolveModernRouteBackends(w, route, req.ID)
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
		onRequestError(w, http.StatusInternalServerError, -32603, "failed to discover any backend", req.ID)
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
		Cacheable: mcp.Cacheable{
			TTLMs:      ttlMs,
			CacheScope: cacheScope,
		},
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

	_, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListToolsResult](ctx, m, req, route, selected, span)
	if err != nil {
		onRequestError(w, http.StatusInternalServerError, -32603, "failed to list tools for all backends", req.ID)
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

	_, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListResourcesResult](ctx, m, req, route, selected, span)
	if err != nil {
		onRequestError(w, http.StatusInternalServerError, -32603, "failed to list resources for all backends", req.ID)
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

	_, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}
	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListResourceTemplatesResult](ctx, m, req, route, selected, span)
	if err != nil {
		onRequestError(w, http.StatusInternalServerError, -32603, "failed to list resource templates for all backends", req.ID)
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

	_, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	responses, err := sendToAllModernBackendsAndAggregateResponses[mcp.ListPromptsResult](ctx, m, req, route, selected, span)
	if err != nil {
		onRequestError(w, http.StatusInternalServerError, -32603, "failed to list prompts for all backends", req.ID)
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

// handleModernToolsCall handles tools/call on the modern stateless path (P1.5).
// Routes to the single backend identified by the backend__toolName prefix.
func (m *mcpRequestContext) handleModernToolsCall(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.requestHeaders = r.Header

	routeConfig, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	// Extract tool name from params.
	var params mcp.CallToolParams
	if err = json.Unmarshal(req.Params, &params); err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid tools/call params", req.ID)
		return handlerResult{}, fmt.Errorf("invalid tools/call params: %w", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
	}

	// Resolve the backend: Never-mode backends expose bare tool names (resolved
	// via the static, per-route neverModeToolIndex computed at config load), so
	// try that first and fall back to parsing the "<backend>__<tool>" prefix.
	// This mirrors legacy handleToolCallRequest so both eras route identically.
	var (
		backendName, upstreamName string
		resolvedFromIndex         bool
	)
	if indexedBackend, inIndex := routeConfig.neverModeToolIndex[params.Name]; inIndex {
		backendName, upstreamName, resolvedFromIndex = indexedBackend, params.Name, true
	}
	if !resolvedFromIndex {
		backendName, upstreamName, err = upstreamResourceName(params.Name)
		if err != nil {
			onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid tool name: %v", err), req.ID)
			return handlerResult{}, fmt.Errorf("%w: %s", errInvalidToolName, params.Name)
		}
	}
	result := handlerResult{backendName: backendName}

	backend, err := lookupSelectedBackend(w, routeConfig, selected, backendName, req.ID)
	if err != nil {
		return result, err
	}

	// Enforce per-route tool selector filters.
	if selector := routeConfig.toolSelectors[backendName]; selector != nil && !selector.allows(upstreamName) {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid tool name: %s", upstreamName), req.ID)
		return result, fmt.Errorf("%w: %s", errInvalidToolName, upstreamName)
	}

	// Enforce per-route authorization (same semantics as legacy path).
	if routeConfig.authorization != nil {
		httpPath := ""
		if r.URL != nil {
			httpPath = r.URL.Path
		}
		allowed, requiredScopes := m.authorizeRequest(routeConfig.authorization, &authorizationRequest{
			Headers:    r.Header,
			HTTPMethod: r.Method,
			Host:       r.Host,
			HTTPPath:   httpPath,
			MCPMethod:  req.Method,
			Backend:    backendName,
			Tool:       upstreamName,
			Params:     &params,
		})
		if !allowed {
			// Include a scope challenge when available.
			if len(requiredScopes) > 0 {
				if challenge := buildInsufficientScopeHeader(requiredScopes, routeConfig.authorization.ResourceMetadataURL); challenge != "" {
					w.Header().Set("WWW-Authenticate", challenge)
				}
			}
			onRequestError(w, http.StatusForbidden, errCodeInvalidRequest, "access denied", req.ID)
			return result, errors.New("authorization failed")
		}
	}

	// Rewrite the params with the unprefixed name.
	params.Name = upstreamName
	rewrittenParams, err := json.Marshal(params)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid tools/call params: %v", err), req.ID)
		return result, fmt.Errorf("invalid tools/call params: %w", err)
	}
	req.Params = rewrittenParams

	// Send and proxy (P1.9: MRTR passthrough). Re-prefix any resource URIs the
	// backend returned (ResourceLink / EmbeddedResource entries and _meta) back
	// into the gateway's downstream namespace, matching legacy maybeResponseModify.
	// Interim MRTR results (resultType: "input_required") are passed through verbatim.
	return m.sendModernRequestAndProxy(ctx, w, req, route, backend, result, span,
		func(resp json.RawMessage) (json.RawMessage, bool) {
			return rewriteToolsCallResult(resp, backendName)
		})
}

// handleModernResourcesRead handles resources/read (P1.5 single-target).
func (m *mcpRequestContext) handleModernResourcesRead(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.requestHeaders = r.Header

	routeConfig, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	var params mcp.ReadResourceParams
	if err = json.Unmarshal(req.Params, &params); err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid resources/read params", req.ID)
		return handlerResult{}, fmt.Errorf("invalid resources/read params: %w", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
	}

	backendName, upstreamURI, err := upstreamResourceURI(params.URI)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid resource URI: %v", err), req.ID)
		return handlerResult{}, fmt.Errorf("%w: %s", errInvalidToolName, params.URI)
	}
	result := handlerResult{backendName: backendName}

	backend, err := lookupSelectedBackend(w, routeConfig, selected, backendName, req.ID)
	if err != nil {
		return result, err
	}

	params.URI = upstreamURI
	rewrittenParams, err := json.Marshal(params)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid resources/read params: %v", err), req.ID)
		return result, fmt.Errorf("invalid resources/read params: %w", err)
	}
	req.Params = rewrittenParams

	// Send and proxy. Re-prefix content URIs back to downstream form so clients
	// see consistent namespaced URIs, and ensure caching hints are present.
	// Interim MRTR results (resultType: "input_required") are not cacheable and
	// pass through verbatim, so only complete results are rewritten.
	return m.sendModernRequestAndProxy(ctx, w, req, route, backend, result, span,
		func(resp json.RawMessage) (json.RawMessage, bool) {
			return rewriteResourcesReadResult(resp, backendName)
		})
}

// rewriteResourcesReadResult re-prefixes content URIs and injects caching hints
// on a complete resources/read result. Per the MCP caching SEP, servers MUST
// include ttlMs/cacheScope on resultType:"complete" resources/read responses;
// interim MRTR results (resultType:"input_required") are not cacheable and are
// left untouched. URI rewriting is shared with legacy via
// rewriteResourcesReadContentsURIs so unknown fields are preserved.
// Returns (rewritten, true) for a complete result, or (nil, false) when the
// caller should pass the result through verbatim.
//
// https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/caching#cacheable-results
func rewriteResourcesReadResult(result json.RawMessage, backendName string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(result, &m) != nil {
		return nil, false
	}
	// Non-complete (input_required) results are not cacheable; leave untouched.
	if rt, ok := m["resultType"]; ok {
		var s string
		if json.Unmarshal(rt, &s) == nil && s != "" && s != "complete" {
			return nil, false
		}
	}
	if _, ok := m["inputRequests"]; ok {
		return nil, false
	}

	_ = rewriteResourcesReadContentsURIs(m, backendName)

	// Inject caching hints when the backend omits them (or sends an empty
	// cacheScope from a zero-value Cacheable). ttlMs defaults to 0 (immediately
	// stale); cacheScope defaults to "private" because resources/read content
	// is typically caller-specific.
	// https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/caching#cacheable-results
	if raw, ok := m["ttlMs"]; !ok || string(raw) == "null" {
		m["ttlMs"] = json.RawMessage(strconv.Itoa(defaultTTLMs))
	}
	if raw, ok := m["cacheScope"]; !ok || string(raw) == "null" || string(raw) == `""` {
		scope, _ := json.Marshal(defaultResourcesReadCacheScope)
		m["cacheScope"] = scope
	}

	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}

// handleModernPromptsGet handles prompts/get (P1.5 single-target).
func (m *mcpRequestContext) handleModernPromptsGet(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.requestHeaders = r.Header

	routeConfig, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	var params mcp.GetPromptParams
	if err = json.Unmarshal(req.Params, &params); err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid prompts/get params", req.ID)
		return handlerResult{}, fmt.Errorf("invalid prompts/get params: %w", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
	}

	// Never-mode backends may expose bare prompt names (via neverModePromptIndex),
	// otherwise the "<backend>__<prompt>" prefix is parsed. Shared with legacy via
	// resolvePromptBackend so both eras resolve names identically.
	backendName, upstreamName, err := m.resolvePromptBackend(route, params.Name)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid prompt name: %v", err), req.ID)
		return handlerResult{}, fmt.Errorf("%w: %s", errInvalidToolName, params.Name)
	}
	result := handlerResult{backendName: backendName}

	backend, err := lookupSelectedBackend(w, routeConfig, selected, backendName, req.ID)
	if err != nil {
		return result, err
	}

	// Enforce per-route prompt selector filters (parity with the tools/call path).
	if selector := routeConfig.promptSelectors[backendName]; selector != nil && !selector.allows(upstreamName) {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid prompt name: %s", upstreamName), req.ID)
		return result, fmt.Errorf("%w: %s", errInvalidToolName, upstreamName)
	}

	params.Name = upstreamName
	rewrittenParams, err := json.Marshal(params)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid prompts/get params: %v", err), req.ID)
		return result, fmt.Errorf("invalid prompts/get params: %w", err)
	}
	req.Params = rewrittenParams

	return m.sendModernRequestAndProxy(ctx, w, req, route, backend, result, span, nil)
}

// handleModernComplete handles completion/complete (P1.5 single-target).
func (m *mcpRequestContext) handleModernComplete(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.requestHeaders = r.Header

	routeConfig, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	var params mcp.CompleteParams
	if err = json.Unmarshal(req.Params, &params); err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid completion/complete params", req.ID)
		return handlerResult{}, fmt.Errorf("invalid completion/complete params: %w", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
	}
	if params.Ref == nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "completion/complete requires a ref", req.ID)
		return handlerResult{}, fmt.Errorf("%w: missing ref", errInvalidToolName)
	}

	// completion/complete targets a specific prompt or resource by ref. Resolve
	// the owning backend from the ref and unprefix it, matching legacy
	// handleCompletionComplete. Either Name (ref/prompt) or URI (ref/resource)
	// carries the namespaced identifier depending on Ref.Type.
	// https://modelcontextprotocol.io/specification/2026-07-28
	var backendName string
	switch params.Ref.Type {
	case "ref/prompt":
		backendName, params.Ref.Name, err = m.resolvePromptBackend(route, params.Ref.Name)
	case "ref/resource":
		backendName, params.Ref.URI, err = upstreamResourceURI(params.Ref.URI)
	default:
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("unsupported ref type: %s", params.Ref.Type), req.ID)
		return handlerResult{}, fmt.Errorf("%w: unsupported ref type %s", errInvalidToolName, params.Ref.Type)
	}
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid ref %s: %v", cmp.Or(params.Ref.Name, params.Ref.URI), err), req.ID)
		return handlerResult{}, err
	}
	result := handlerResult{backendName: backendName}

	backend, err := lookupSelectedBackend(w, routeConfig, selected, backendName, req.ID)
	if err != nil {
		return result, err
	}

	// Rewrite params with the unprefixed ref.
	rewrittenParams, err := json.Marshal(&params)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid completion/complete params: %v", err), req.ID)
		return result, fmt.Errorf("invalid completion/complete params: %w", err)
	}
	req.Params = rewrittenParams

	return m.sendModernRequestAndProxy(ctx, w, req, route, backend, result, span, nil)
}

// handleSubscriptionsListen handles subscriptions/listen (P1.8).
// Opens SSE streams to backends and merges notification events.
//
// This is a long-lived streaming handler, not a request/response call, so it
// does not fit the request-duration metric shape. It sets
// perBackendMetricsRecorded to skip the generic recording in serveModernPOST,
// mirroring how the legacy path treats streaming/notification methods.
//
// Resource subscription URIs are gateway-namespaced (backend+scheme://…). Before
// fan-out they are partitioned by owning backend and sent upstream unprefixed,
// matching every other single-target handler. Notifications are filtered against
// the client's original opt-ins before forwarding.
func (m *mcpRequestContext) handleSubscriptionsListen(ctx context.Context, w http.ResponseWriter, r *http.Request, req *jsonrpc.Request, route filterapi.MCPRouteName, span tracingapi.MCPSpan) (handlerResult, error) {
	m.perBackendMetricsRecorded = true
	m.requestHeaders = r.Header

	routeConfig, selected, err := m.resolveModernRouteBackends(w, route, req.ID)
	if err != nil {
		return handlerResult{}, err
	}

	var params mcp.SubscriptionsListenParams
	if err = json.Unmarshal(req.Params, &params); err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, "invalid subscriptions/listen params", req.ID)
		return handlerResult{}, fmt.Errorf("invalid subscriptions/listen params: %w", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
	}

	intent, perBackendURIs, err := partitionResourceSubscriptions(params.Notifications)
	if err != nil {
		onRequestError(w, http.StatusBadRequest, errCodeInvalidParams, fmt.Sprintf("invalid resource subscription URI: %v", err), req.ID)
		return handlerResult{}, err
	}
	for backendName := range perBackendURIs {
		if _, err := lookupSelectedBackend(w, routeConfig, selected, backendName, req.ID); err != nil {
			return handlerResult{}, err
		}
	}

	wantListChanged := intent.toolsListChanged || intent.promptsListChanged || intent.resourcesListChanged

	// Set SSE response headers only after params are validated — a 400 cannot
	// follow a started event-stream.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	// Fan out a per-backend listen request: only backends that own a subscribed
	// URI (or that must receive list_changed opt-ins) are contacted, and each
	// sees bare upstream URIs rather than gateway-namespaced ones.
	events := make(chan *sseEvent)
	var wg sync.WaitGroup
	for _, backend := range selected {
		uris := perBackendURIs[backend.Name]
		if len(uris) == 0 && !wantListChanged {
			continue
		}
		backendParams := buildBackendListenParams(&params, uris)
		paramsBytes, marshalErr := json.Marshal(backendParams)
		if marshalErr != nil {
			continue
		}
		backendReq := &jsonrpc.Request{
			Method: req.Method,
			ID:     req.ID,
			Params: paramsBytes,
		}
		body, encErr := jsonrpc.EncodeMessage(backendReq)
		if encErr != nil {
			continue
		}
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, m.backendListenerAddr, bytes.NewReader(body))
		if reqErr != nil {
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set(mcpProtocolVersionHeader, protocolVersion20260728)
		httpReq.Header.Set(mcpMethodHeader, "subscriptions/listen")
		httpReq.Header.Set(internalapi.MCPBackendHeader, backend.Name)
		httpReq.Header.Set(internalapi.MCPRouteHeader, route)

		resp, doErr := m.client.Do(httpReq)
		if doErr != nil {
			m.l.Warn("subscriptions/listen failed", slog.String("backend", backend.Name), slog.String("error", doErr.Error()))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}
		if span != nil {
			span.RecordRouteToBackend(backend.Name, "", true)
		}
		// Defer close immediately so bodyclose can see the cleanup path; the
		// reader goroutine still drains the body until EOF or cancel.
		defer resp.Body.Close()
		backendName := backend.Name
		wg.Go(func() {
			parser := newSSEEventParser(resp.Body, backendName)
			for {
				event, err := parser.next()
				if event != nil {
					select {
					case events <- event:
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
					return
				}
			}
		})
	}

	// Close the events channel once all backend readers finish so the merge loop
	// can drain and exit cleanly.
	go func() {
		wg.Wait()
		close(events)
	}()

	// Merge loop: forward rewritten, intent-filtered events to the client,
	// sending periodic keep-alives while idle. When all backends close
	// (gracefully or otherwise), the gateway writes a completion result per
	// the subscriptions spec before returning.
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	done := ctx.Done()
	for {
		select {
		case <-done:
			// Client disconnected — on Streamable HTTP this IS the
			// cancellation signal per the spec. Upstream bodies are closed
			// by deferred resp.Body.Close, tearing down backend streams.
			return handlerResult{}, nil
		case event, ok := <-events:
			if !ok {
				// All backend streams ended. Write a graceful completion
				// result so the client knows the subscription closed cleanly
				// rather than via a transport drop.
				// https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/subscriptions#graceful-closure
				writeSSECompletionResult(w, req.ID)
				if flusher != nil {
					flusher.Flush()
				}
				return handlerResult{}, nil
			}
			m.forwardSubscriptionEvent(w, event, intent)
			if flusher != nil {
				flusher.Flush()
			}
		case <-keepAlive.C:
			_, _ = w.Write([]byte(": keep-alive\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// partitionResourceSubscriptions splits gateway-namespaced resource subscription
// URIs by owning backend, returning the client's intent (namespaced URIs + list
// flags) and a per-backend list of bare upstream URIs. URIs that do not parse
// fail the request; callers must still verify each backend is selected.
func partitionResourceSubscriptions(notifs *mcp.NotificationSubscriptions) (subscriptionListenIntent, map[filterapi.MCPBackendName][]string, error) {
	intent := subscriptionListenIntent{resourceURIs: make(map[string]struct{})}
	perBackend := make(map[filterapi.MCPBackendName][]string)
	if notifs == nil {
		return intent, perBackend, nil
	}
	intent.toolsListChanged = notifs.ToolsListChanged
	intent.promptsListChanged = notifs.PromptsListChanged
	intent.resourcesListChanged = notifs.ResourcesListChanged

	for _, uri := range notifs.ResourceSubscriptions {
		backendName, upstreamURI, err := upstreamResourceURI(uri)
		if err != nil {
			return subscriptionListenIntent{}, nil, fmt.Errorf("%w: %s", err, uri)
		}
		intent.resourceURIs[uri] = struct{}{}
		perBackend[backendName] = append(perBackend[backendName], upstreamURI)
	}
	return intent, perBackend, nil
}

// buildBackendListenParams copies the client listen params with resource
// subscriptions replaced by the bare upstream URIs for one backend.
func buildBackendListenParams(client *mcp.SubscriptionsListenParams, upstreamURIs []string) *mcp.SubscriptionsListenParams {
	out := &mcp.SubscriptionsListenParams{Meta: client.Meta}
	if client.Notifications == nil {
		return out
	}
	n := *client.Notifications
	n.ResourceSubscriptions = append([]string(nil), upstreamURIs...)
	out.Notifications = &n
	return out
}

// forwardSubscriptionEvent rewrites and conditionally forwards a single SSE
// notification event from a backend to the downstream client. resources/updated
// URIs are re-prefixed into the gateway namespace; notifications the client did
// not opt into are dropped. Backend graceful-closure responses (jsonrpc.Response
// to the listen request) are silently consumed — the gateway synthesizes its own
// completion result once all backends close. notifications/cancelled from a
// backend (server-initiated teardown) is forwarded to the client.
func (m *mcpRequestContext) forwardSubscriptionEvent(w io.Writer, event *sseEvent, intent subscriptionListenIntent) {
	kept := event.messages[:0]
	for _, msg := range event.messages {
		switch v := msg.(type) {
		case *jsonrpc.Response:
			// Backend sent a graceful completion result for its listen
			// request. Swallow it; the gateway emits its own once all
			// backend streams end.
			continue
		case *jsonrpc.Request:
			if v == nil {
				continue
			}
			switch v.Method {
			case "notifications/resources/updated":
				rewritten, ok := rewriteUpdatedURI(json.RawMessage(v.Params), event.backend)
				if !ok {
					continue
				}
				v.Params = []byte(rewritten)
				var envelope struct {
					URI string `json:"uri"`
				}
				if json.Unmarshal(rewritten, &envelope) != nil {
					continue
				}
				if _, subscribed := intent.resourceURIs[envelope.URI]; !subscribed {
					continue
				}
			case "notifications/tools/list_changed":
				if !intent.toolsListChanged {
					continue
				}
			case "notifications/resources/list_changed":
				if !intent.resourcesListChanged {
					continue
				}
			case "notifications/prompts/list_changed":
				if !intent.promptsListChanged {
					continue
				}
			case "notifications/subscriptions/acknowledged":
				if rewritten, ok := rewriteAcknowledgedSubscriptions(json.RawMessage(v.Params), event.backend, intent.resourceURIsForBackend(event.backend)); ok {
					v.Params = []byte(rewritten)
				}
			case "notifications/cancelled":
				// Server-initiated subscription teardown (spec: server MUST
				// send this when it tears down the stream). Forward as-is.
			default:
				// Unknown notification type — forward for extensibility.
			}
		default:
			continue
		}
		kept = append(kept, msg)
	}
	if len(kept) == 0 {
		return
	}
	event.messages = kept
	event.writeAndMaybeFlush(w)
}

// writeSSECompletionResult writes a graceful subscriptions/listen completion
// result as an SSE event. Per the spec, the server SHOULD respond with
// resultType:"complete" before closing the stream so the client knows the
// subscription ended cleanly.
// https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/subscriptions#graceful-closure
func writeSSECompletionResult(w io.Writer, listenID jsonrpc.ID) {
	result := map[string]any{
		"resultType": "complete",
	}
	encoded, _ := json.Marshal(result)
	resp := &jsonrpc.Response{ID: listenID, Result: encoded}
	data, _ := jsonrpc.EncodeMessage(resp)
	_, _ = w.Write([]byte("event: message\n"))
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}

// rewriteAcknowledgedSubscriptions normalizes resourceSubscriptions in a
// notifications/subscriptions/acknowledged params payload so the client sees
// the gateway-namespaced URI array it subscribed with.
//
// Per the subscriptions SEP the field is a string[]. Some backends (notably
// go-sdk) currently emit a boolean true instead; when that happens we
// substitute clientURIs — the gateway-namespaced URIs this backend was asked
// to honor — so the client can confirm what was acknowledged.
func rewriteAcknowledgedSubscriptions(params json.RawMessage, backendName string, clientURIs []string) (json.RawMessage, bool) {
	if len(params) == 0 {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return nil, false
	}
	notifsRaw, ok := m["notifications"]
	if !ok {
		return nil, false
	}
	var notifs map[string]json.RawMessage
	if json.Unmarshal(notifsRaw, &notifs) != nil {
		return nil, false
	}
	urisRaw, ok := notifs["resourceSubscriptions"]
	if !ok {
		return nil, false
	}

	var uris []string
	if json.Unmarshal(urisRaw, &uris) == nil && len(uris) > 0 {
		// Backend echoed an array of upstream URIs — re-prefix for the client.
		for i, uri := range uris {
			uris[i] = downstreamResourceURI(uri, backendName)
		}
	} else {
		// Boolean true / null / empty / unexpected shape — substitute the
		// client's gateway-namespaced URIs for this backend so the ack is
		// still a string[] as the spec requires.
		uris = append([]string(nil), clientURIs...)
	}
	prefixed, err := json.Marshal(uris)
	if err != nil {
		return nil, false
	}
	notifs["resourceSubscriptions"] = prefixed
	notifsOut, err := json.Marshal(notifs)
	if err != nil {
		return nil, false
	}
	m["notifications"] = notifsOut
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}

// resourceURIsForBackend returns the gateway-namespaced resource subscription
// URIs from intent that belong to backendName, in stable order.
func (intent subscriptionListenIntent) resourceURIsForBackend(backendName string) []string {
	var uris []string
	for uri := range intent.resourceURIs {
		owner, _, err := upstreamResourceURI(uri)
		if err == nil && owner == backendName {
			uris = append(uris, uri)
		}
	}
	sort.Strings(uris)
	return uris
}

// rewriteUpdatedURI re-prefixes the "uri" field in a notifications/resources/updated
// params payload with the backend name, returning (rewritten, true) on success.
func rewriteUpdatedURI(params json.RawMessage, backendName string) (json.RawMessage, bool) {
	if len(params) == 0 {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return nil, false
	}
	uriRaw, ok := m["uri"]
	if !ok {
		return nil, false
	}
	var uri string
	if json.Unmarshal(uriRaw, &uri) != nil || uri == "" {
		return nil, false
	}
	prefixed, _ := json.Marshal(downstreamResourceURI(uri, backendName))
	m["uri"] = prefixed
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
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

// modernResultRewriter optionally transforms a backend's raw JSON-RPC result
// before it is written back to the client (e.g. re-prefixing namespaced URIs or
// injecting caching hints). It returns (rewritten, true) to send the rewritten
// payload, or (nil, false) to pass the original result through verbatim — which
// is how interim MRTR (resultType: "input_required") results are preserved.
type modernResultRewriter func(result json.RawMessage) (json.RawMessage, bool)

// sendModernRequestAndProxy sends req to a single modern backend and proxies the
// response back to the client, centralizing the send / error-handling / write
// sequence shared by every single-target modern handler (tools/call,
// resources/read, prompts/get, completion/complete).
// It records the per-backend routing span (when span is non-nil), sends the
// request, writes a 500 error response on failure, applies the optional rewrite,
// and finally writes the JSON-RPC result. rewrite may be nil to pass the backend
// result through unchanged.
func (m *mcpRequestContext) sendModernRequestAndProxy(
	ctx context.Context,
	w http.ResponseWriter,
	req *jsonrpc.Request,
	route filterapi.MCPRouteName,
	backend filterapi.MCPBackend,
	result handlerResult,
	span tracingapi.MCPSpan,
	rewrite modernResultRewriter,
) (handlerResult, error) {
	if span != nil {
		span.RecordRouteToBackend(backend.Name, "", false)
	}

	resp, err := m.sendModernRequest(ctx, req, route, backend)
	if err != nil {
		// If the backend returned a structured JSON-RPC error, forward it to the
		// client as a proper JSON-RPC error response instead of stringifying it
		// into a text/plain 500.
		var bjErr *backendJSONRPCError
		if errors.As(err, &bjErr) {
			writeBackendJSONRPCError(w, req.ID, bjErr.raw)
			return result, err
		}
		onRequestError(w, http.StatusInternalServerError, -32603, fmt.Sprintf("call to %s failed: %v", backend.Name, err), req.ID)
		return result, err
	}

	if rewrite != nil {
		if rewritten, ok := rewrite(resp); ok {
			resp = rewritten
		}
	}
	writeRawJSONRPCResult(w, req.ID, resp)
	return result, nil
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
		return &backendJSONRPCError{raw: errField}
	case !resultPresent:
		return fmt.Errorf("backend returned no result")
	}
	return nil
}

// extractJSONFromSSE parses an SSE response body and extracts the last JSON-RPC
// message from "data:" lines. MCP backends return the final result as the last event.
func extractJSONFromSSE(body []byte) []byte {
	var lastData []byte
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
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
		backendTTL := result.TTLMs
		if backendTTL < 0 {
			backendTTL = 0
		}
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

func (e *backendJSONRPCError) Error() string {
	return fmt.Sprintf("backend JSON-RPC error: %s", string(e.raw))
}

// writeBackendJSONRPCError writes a backend's JSON-RPC error as a proper
// JSON-RPC error response to the client, preserving the original error code,
// message, and data. The HTTP status is 200 so the JSON-RPC layer can parse it.
func writeBackendJSONRPCError(w http.ResponseWriter, id jsonrpc.ID, raw json.RawMessage) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id.Raw(),
		"error":   raw,
	}
	encoded, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
