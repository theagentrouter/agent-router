// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
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
	"golang.org/x/net/http/httpguts"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// handlerResult contains metadata from single-backend handler execution.
// This struct is returned alongside error from handlers that target a specific backend,
// enabling centralized metrics recording with the correct backend context.
// The struct can be extended with additional fields as needed (e.g., custom metrics tags).
type handlerResult struct {
	backendName string
}

// postCompletion is the final bookkeeping for a POST, shared by serveLegacyPOST
// and serveModernPOST. Callers invoke recordPOSTCompletion from a defer so these
// fields are read after the handler has set them. session is nil on the
// stateless modern path; params may also be nil there.
type postCompletion struct {
	ctx     context.Context
	method  string
	errType metrics.MCPErrorType
	err     error
	startAt time.Time
	span    tracingapi.MCPSpan
	result  handlerResult
	params  mcp.Params
	session *session
}

func onErrorResponse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

// servePOST is the era-neutral entry point for MCP POST requests. It reads and
// decodes the JSON-RPC message once, then dispatches to the modern stateless
// path or the legacy path based on the era detected from the request.
//
// Keeping this dispatcher thin is deliberate: once the legacy MCP spec is fully
// retired, the legacy branch and legacy.go can be removed in one step without
// touching the modern path.
func (m *mcpRequestContext) servePOST(w http.ResponseWriter, r *http.Request) {
	startAt := time.Now()

	limit := m.maxRequestBodySize
	if limit <= 0 {
		limit = defaultMaxRequestBodySize
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			onErrorResponse(w, http.StatusRequestEntityTooLarge, "request body too large")
			m.recordPOSTCompletion(&postCompletion{
				ctx:     r.Context(),
				errType: metrics.MCPErrorInternal,
				err:     err,
				startAt: startAt,
			})
			return
		}
		onErrorResponse(w, http.StatusBadRequest, err.Error())
		m.recordPOSTCompletion(&postCompletion{
			ctx:     r.Context(),
			errType: metrics.MCPErrorInternal,
			err:     err,
			startAt: startAt,
		})
		return
	}

	rawMsg, err := jsonrpc.DecodeMessage(body)
	if err != nil {
		onErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON-RPC message: %v", err))
		m.recordPOSTCompletion(&postCompletion{
			ctx:     r.Context(),
			errType: metrics.MCPErrorInvalidJSONRPC,
			err:     err,
			startAt: startAt,
		})
		return
	}

	// TODO: Detect era: modern or legacy

	m.serveLegacyPOST(w, r, rawMsg, startAt)
}

// recordPOSTCompletion logs completion, records metrics, and closes the tracing
// span. Handlers that fan out to multiple backends (e.g. tools/list,
// server/discover) set perBackendMetricsRecorded to skip generic per-backend
// recording and avoid double-counting. The tracing span is still closed.
func (m *mcpRequestContext) recordPOSTCompletion(c *postCompletion) {
	if m.l.Enabled(c.ctx, slog.LevelDebug) {
		m.l.Debug("Completed MCP POST request",
			slog.String("method", c.method),
			slog.String("error_type", string(c.errType)),
			slog.String("duration", time.Since(c.startAt).String()))
	}

	// The client-facing session is known for every legacy method except
	// initialize, which creates it and records it in handleInitializeRequest
	// instead. Attributes may be set any time before the span ends, so
	// recording it here covers every method from one place. Modern requests
	// are stateless, so session is nil.
	if c.span != nil && c.session != nil {
		c.span.RecordClientSession(string(c.session.clientGatewaySessionID()))
	}

	if m.perBackendMetricsRecorded {
		endMCPSpan(c.span, c.errType, c.err)
		return
	}

	metricsInstance := m.metrics
	if c.result.backendName != "" {
		metricsInstance = m.metrics.WithBackend(c.result.backendName)
	}

	if c.err != nil {
		applicationError := false
		var errToolCall *errToolCall
		if errors.As(c.err, &errToolCall) {
			applicationError = true
		}
		endMCPSpan(c.span, c.errType, c.err)
		if applicationError {
			metricsInstance.RecordMethodErrorCount(c.ctx, c.method, c.params, metrics.MCPStatusFailed)
		} else {
			metricsInstance.RecordMethodErrorCount(c.ctx, c.method, c.params, metrics.MCPStatusError)
		}
		metricsInstance.RecordRequestErrorDuration(c.ctx, c.startAt, c.errType, c.params)
		return
	}

	endMCPSpan(c.span, c.errType, nil)
	metricsInstance.RecordRequestDuration(c.ctx, c.startAt, c.params)
	// TODO: should we special case when this request is "Response" where method is empty?
	metricsInstance.RecordMethodCount(c.ctx, c.method, c.params)
}

func endMCPSpan(span tracingapi.MCPSpan, errType metrics.MCPErrorType, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.EndSpanOnError(string(errType), err)
	} else {
		span.EndSpan()
	}
}

// nameSeparator is used as the separator to avoid collision with any character in k8s resource names as well as base64 encoding.
// We can't use special characters as tool names must match the regex `[a-zA-Z0-9._-]+`.
const nameSeparator = "__"

// downstreamResourceName converts the upstream resource/prompt name to the downstream resource/prompt name by
// prefixing the backend name.
func downstreamResourceName(name string, backendName string) string {
	return fmt.Sprintf("%s%s%s", backendName, nameSeparator, name)
}

// upstreamResourceName converts the downstream tool/resource name to the upstream resource/prompt name by
// stripping the backend name prefix.
//
// We assume that all tool/resource names are prefixed with the backend name followed by an underscore, so
// it's an unrecoverable error if the tool/resource name doesn't contain an underscore and that's a client error.
func upstreamResourceName(fullName string) (backendName, name string, err error) {
	index := strings.Index(fullName, nameSeparator)
	if index < 0 {
		return "", "", fmt.Errorf("invalid resource name: %s", fullName)
	}
	return fullName[:index], fullName[index+len(nameSeparator):], nil
}

// uiSchemePrefix is the URI scheme the MCP Apps extension mandates for UI resources.
// Hosts only render resources whose scheme is exactly "ui", so the backend name must be
// encoded inside the URI rather than prepended to the scheme.
const uiSchemePrefix = "ui://"

// downstreamResourceURI converts the upstream resource URI to the downstream resource URI by
// encoding the URL. The URL will be in the form: <backend>+<scheme>://<path>
// We need to encode URLs in a way that the "path" part remains unchanged so that the Resource Templates
// can still match the resource URIs.
//
// ui:// URIs are the exception: the scheme must survive the rewrite, so the backend name is
// inserted as the leading path segment instead: ui://<backend>/<rest>.
func downstreamResourceURI(uri string, backendName string) string {
	if rest, ok := strings.CutPrefix(uri, uiSchemePrefix); ok {
		return uiSchemePrefix + backendName + "/" + rest
	}
	return fmt.Sprintf("%s+%s", backendName, uri)
}

// upstreamResourceURI converts the downstream resource URI to the upstream resource URI,
// the inverse of downstreamResourceURI: ui://<backend>/<rest> for UI resources and
// <backend>+<scheme>://<path> for everything else.
func upstreamResourceURI(fullURI string) (backendName, uri string, err error) {
	if rest, ok := strings.CutPrefix(fullURI, uiSchemePrefix); ok {
		backendName, rest, ok = strings.Cut(rest, "/")
		if !ok || backendName == "" {
			return "", "", fmt.Errorf("invalid resource URI: %s", fullURI)
		}
		return backendName, uiSchemePrefix + rest, nil
	}
	parts := strings.SplitN(fullURI, "+", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid resource URI: %s", fullURI)
	}
	return parts[0], parts[1], nil
}

// metaResourceURIPaths lists the _meta key paths whose leaf value is a resource URI that
// must be namespaced with the backend name in multiplexing mode so that a later
// resources/read on it routes back to the originating backend.
var metaResourceURIPaths = [][]string{
	{"ui", "resourceUri"}, // MCP Apps (_meta.ui.resourceUri).
	{"ui/resourceUri"},    // MCP Apps SDK flat form (_meta["ui/resourceUri"]).
}

// rewriteMetaResourceURIs namespaces every known resource URI in meta with the backend name.
// Returns true if anything was changed. No-ops on absent or unexpected shapes.
// Not idempotent: it must be applied exactly once per upstream payload.
func rewriteMetaResourceURIs(meta mcp.Meta, backendName filterapi.MCPBackendName) bool {
	changed := false
	for _, path := range metaResourceURIPaths {
		node := map[string]any(meta)
		for _, key := range path[:len(path)-1] {
			if node, _ = node[key].(map[string]any); node == nil {
				break
			}
		}
		if node == nil {
			continue
		}
		leaf := path[len(path)-1]
		if uri, ok := node[leaf].(string); ok && uri != "" {
			node[leaf] = downstreamResourceURI(uri, backendName)
			changed = true
		}
	}
	return changed
}

// rewriteToolResultURIs namespaces all resource URIs in a tools/call result: _meta fields
// (via rewriteMetaResourceURIs) and any ResourceLink / EmbeddedResource entries in Content.
// Returns true if anything was changed.
func rewriteToolResultURIs(result *mcp.CallToolResult, backendName filterapi.MCPBackendName) bool {
	changed := rewriteMetaResourceURIs(result.Meta, backendName)
	for _, c := range result.Content {
		switch v := c.(type) {
		case *mcp.ResourceLink:
			if v.URI != "" {
				v.URI = downstreamResourceURI(v.URI, backendName)
				changed = true
			}
		case *mcp.EmbeddedResource:
			if v.Resource != nil && v.Resource.URI != "" {
				v.Resource.URI = downstreamResourceURI(v.Resource.URI, backendName)
				changed = true
			}
		}
	}
	return changed
}

// extractForwardHeaders reads the configured headers from the incoming request to forward to backends.
func extractForwardHeaders(reqHeaders http.Header, headers []string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	result := make(map[string]string)
	for _, header := range headers {
		if value := reqHeaders.Get(header); value != "" {
			result[header] = value
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// extractPerBackendForwardHeaders reads per-backend header forwarding config from the incoming request.
// It supports header renaming: each entry maps a source header to an optional destination header name.
func extractPerBackendForwardHeaders(reqHeaders http.Header, mappings []filterapi.MCPHeaderForward) map[string]string {
	if len(mappings) == 0 {
		return nil
	}

	result := make(map[string]string)
	for _, m := range mappings {
		if value := reqHeaders.Get(m.Name); value != "" {
			result[m.ForwardName()] = value
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// addMCPHeaders adds the MCP metadata headers to the HTTP request.
func addMCPHeaders(httpReq *http.Request, msg jsonrpc.Message, params mcp.Params, routeName filterapi.MCPRouteName, backendName filterapi.MCPBackendName) {
	// MCP backend header is used for upstream MCP routing.
	httpReq.Header.Set(internalapi.MCPBackendHeader, backendName)
	httpReq.Header.Set(internalapi.MCPRouteHeader, routeName)
	if mcpReq, ok := msg.(*jsonrpc.Request); ok && mcpReq != nil {
		// MCP request headers are used to populate information in the envoy filter metadata.
		setMCPMetadataHeader(httpReq, internalapi.MCPMetadataHeaderRequestID, fmt.Sprintf("%v", mcpReq.ID.Raw()))
		setMCPMetadataHeader(httpReq, internalapi.MCPMetadataHeaderMethod, mcpReq.Method)

		if params != nil {
			// Mirrors the span attributes set in getMCPParamsAsAttributes, so the same information is
			// available in the access logs as well as in the traces.
			switch p := params.(type) {
			case *mcp.CallToolParams:
				setMCPMetadataHeader(httpReq, internalapi.MCPMetadataHeaderToolName, p.Name)
			case *mcp.ReadResourceParams:
				setMCPMetadataHeader(httpReq, internalapi.MCPMetadataHeaderResourceURI, p.URI)
			case *mcp.SubscribeParams:
				setMCPMetadataHeader(httpReq, internalapi.MCPMetadataHeaderResourceURI, p.URI)
			case *mcp.UnsubscribeParams:
				setMCPMetadataHeader(httpReq, internalapi.MCPMetadataHeaderResourceURI, p.URI)
			}
		}
	}
}

// maxMCPMetadataHeaderValueLen bounds a metadata header value. Resource URIs, tool names and JSON-RPC
// IDs all come from the client unvalidated, and these headers exist only so the access log can see
// them, so dropping an outsized value beats pushing the request over Envoy's max_request_headers_kb.
const maxMCPMetadataHeaderValueLen = 1024

// setMCPMetadataHeader sets an observability-only metadata header, skipping values that would make the
// request unsendable. [http.Transport] rejects header values containing control characters, so a
// resource URI or JSON-RPC ID holding one - trivially expressible as a JSON escape - would otherwise
// fail the whole proxied request, rather than reaching the backend in the body where it is legal.
//
// Dropped rather than truncated or escaped: an absent log field is honest, a mangled one is not.
func setMCPMetadataHeader(httpReq *http.Request, key, value string) {
	// httpguts is the same check [http.Transport] applies, so this cannot drift from what it rejects.
	if len(value) > maxMCPMetadataHeaderValueLen || !httpguts.ValidHeaderFieldValue(value) {
		return
	}
	httpReq.Header.Set(key, value)
}

type (
	// broadCastResponse represents the response from a backend along with the backend name.
	//
	// Used in sendToAllBackendsAndAggregateResponses.
	broadCastResponse[T any] struct {
		backendName string
		res         T
	}
	// broadCastResponseMergeFn is a function that merges multiple broadCastResponse into a single response type.
	//
	// Used in sendToAllBackendsAndAggregateResponses.
	broadCastResponseMergeFn[T any] func(*session, []broadCastResponse[T]) T
)

// mergeToolsList merges the list of tools from all backends and prepare the response message to be sent back to the client.
func (m *mcpRequestContext) mergeToolsList(s *session, responses []broadCastResponse[mcp.ListToolsResult]) mcp.ListToolsResult {
	// Use a non-nil empty slice so JSON encodes as [] not null; some clients reject tools:null.
	resp := mcp.ListToolsResult{Tools: make([]*mcp.Tool, 0)}
	route := m.routes[s.route]
	if route == nil {
		// This should never happen as the route must have been validated when the session is created.
		return resp
	}

	// Aggregate tools from all backends. Per-backend PrefixMode controls whether a backend's
	// tools are prefixed with "<backendName>__" (Always) or exposed as bare names (Never).
	// Never-mode bare names are exactly the ones in route.neverModeToolIndex, a static map
	// computed at config load from each Never-mode backend's declared toolSelector.include
	// (admission-validated for cross-backend uniqueness), so no runtime collision bookkeeping
	// is needed for them here. Always-mode backends prefix inline; both can coexist on the
	// same route. Tools are filtered by toolSelector and authorization before inclusion.
	for _, r := range responses {
		backendMode := route.effectivePrefixMode(r.backendName)
		selector := route.toolSelectors[r.backendName]
		for _, tool := range r.res.Tools {
			if selector != nil && !selector.allows(tool.Name) {
				continue
			}
			if route.authorization != nil {
				allowed, _ := m.authorizeRequest(route.authorization, &authorizationRequest{
					Headers:   m.requestHeaders,
					MCPMethod: "tools/call",
					Backend:   r.backendName,
					Tool:      tool.Name,
				})
				if !allowed {
					continue
				}
			}
			if backendMode != filterapi.PrefixModeNever {
				prefixed := downstreamResourceName(tool.Name, r.backendName)
				// Guard against an Always-mode backend's prefixed name accidentally colliding
				// with a bare name a Never-mode backend on this route declared ownership of.
				if owner, collision := route.neverModeToolIndex[prefixed]; collision {
					m.l.Warn("dropping MCP tool name that collides with a prefixMode=Never backend's declared bare name",
						slog.String("tool", prefixed),
						slog.String("always_mode_backend", r.backendName),
						slog.String("never_mode_backend", owner),
					)
					continue
				}
				tool.Name = prefixed
			}
			rewriteMetaResourceURIs(tool.Meta, r.backendName)
			resp.Tools = append(resp.Tools, tool)
		}
	}

	return resp
}

// mergeResourceList merges the list of resources from all backends and prepare the response message to be sent back to the client.
func (m *mcpRequestContext) mergeResourceList(_ *session, responses []broadCastResponse[mcp.ListResourcesResult]) mcp.ListResourcesResult {
	// Aggregate the resources from all responses with some logic to match the actual proxy behavior.
	// TODO: do we need a more sophisticated merging logic here?
	// TODO: how to handle NextCursor?
	resp := mcp.ListResourcesResult{Resources: make([]*mcp.Resource, 0)}
	for _, r := range responses {
		for _, res := range r.res.Resources {
			res.Name = downstreamResourceName(res.Name, r.backendName)
			res.URI = downstreamResourceURI(res.URI, r.backendName)
			resp.Resources = append(resp.Resources, res)
		}
	}
	return resp
}

// mergeResourcesTemplateList merges the list of resource templates from all backends and prepare the response message to be sent back to the client.
func (m *mcpRequestContext) mergeResourcesTemplateList(_ *session, responses []broadCastResponse[mcp.ListResourceTemplatesResult]) mcp.ListResourceTemplatesResult {
	resp := mcp.ListResourceTemplatesResult{ResourceTemplates: make([]*mcp.ResourceTemplate, 0)}
	for _, r := range responses {
		for _, res := range r.res.ResourceTemplates {
			res.Name = downstreamResourceName(res.Name, r.backendName)
			res.URITemplate = downstreamResourceURI(res.URITemplate, r.backendName)
			resp.ResourceTemplates = append(resp.ResourceTemplates, res)
		}
	}
	return resp
}

// mergePromptsList merges the list of prompts from all backends and prepare the response message to be sent back to the client.
func (m *mcpRequestContext) mergePromptsList(s *session, responses []broadCastResponse[mcp.ListPromptsResult]) mcp.ListPromptsResult {
	// Aggregate the resources from all responses with some logic to match the actual proxy behavior.
	aggregatedResponse := mcp.ListPromptsResult{Prompts: make([]*mcp.Prompt, 0)}

	route := m.routes[s.route]
	for _, r := range responses {
		backendMode := filterapi.PrefixModeAlways
		var selector *toolSelector
		var neverModePromptIndex map[string]string
		if route != nil {
			backendMode = route.effectivePrefixMode(r.backendName)
			selector = route.promptSelectors[r.backendName]
			neverModePromptIndex = route.neverModePromptIndex
		}
		for _, res := range r.res.Prompts {
			if selector != nil && !selector.allows(res.Name) {
				continue
			}
			// A prompt is exposed bare only when this backend is in Never mode AND it opted in
			// by declaring this exact name via promptSelector.include (statically indexed in
			// route.neverModePromptIndex at config load — see MCPPromptFilter). Backends that
			// don't declare a promptSelector keep the "<backendName>__" prefix even under Never
			// mode, since there's no admission-validated, unique name set to expose bare for them.
			if backendMode == filterapi.PrefixModeNever && neverModePromptIndex[res.Name] == r.backendName {
				aggregatedResponse.Prompts = append(aggregatedResponse.Prompts, res)
				continue
			}
			prefixed := downstreamResourceName(res.Name, r.backendName)
			if owner, collision := neverModePromptIndex[prefixed]; collision {
				m.l.Warn("dropping MCP prompt name that collides with a prefixMode=Never backend's declared bare name",
					slog.String("prompt", prefixed),
					slog.String("backend", r.backendName),
					slog.String("never_mode_backend", owner),
				)
				continue
			}
			res.Name = prefixed
			aggregatedResponse.Prompts = append(aggregatedResponse.Prompts, res)
		}
	}

	return aggregatedResponse
}
