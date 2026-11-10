// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"net/http"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// isProtectedResourceMetadataRequest reports whether the request targets the OAuth Protected
// Resource Metadata endpoint. Envoy has already matched the request to the dedicated route rule
// by the time it reaches the proxy; this only distinguishes it from MCP traffic on the same
// listener, so an exact prefix match on the well-known path is sufficient.
func isProtectedResourceMetadataRequest(path string) bool {
	if path == internalapi.OAuthProtectedResourceMetadataPath {
		return true
	}
	return strings.HasPrefix(path, internalapi.OAuthProtectedResourceMetadataPath+"/")
}

// externalScheme returns the scheme the client used to reach the gateway.
//
// The proxy is reached over plain HTTP from the local Envoy instance, so the downstream scheme
// is only visible through the forwarded header Envoy sets from the downstream connection. Note
// that this makes the advertised identifier depend on a header, so a gateway that terminates
// untrusted traffic must be configured to sanitize X-Forwarded-Proto (Envoy does this by
// default unless the listener is explicitly told to trust the downstream value).
func externalScheme(r *http.Request) string {
	if proto := r.Header.Get("x-forwarded-proto"); proto != "" {
		// A comma-separated list may appear when multiple proxies are chained; the first
		// entry is the one closest to the client.
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		if proto = strings.TrimSpace(proto); proto != "" {
			return proto
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// externalPath returns the request path the client used.
//
// The generated HTTPRoute rules for MCP traffic and for the well-known endpoints carry no
// URLRewrite filter, so the path Envoy forwards is the path the client sent. Deliberately not
// consulting x-ai-eg-original-path here: nothing strips that header off inbound requests, so
// honoring it would let a client choose which document the proxy serves and which path the
// advertised resource identifier names.
//
// URL.Path is percent-decoded, so a client that requested "/my%20mcp" would be told its
// resource was a different path. EscapedPath keeps the encoding the client sent. The well-known
// prefix is only unreserved characters, so the escaped and decoded forms share it:
// isProtectedResourceMetadataRequest and the TrimPrefix in writeProtectedResourceMetadata
// still select the same suffix.
func externalPath(r *http.Request) string {
	return r.URL.EscapedPath()
}

// resourceIdentifier returns the RFC 9728 resource identifier for the MCP endpoint this request
// was made against. resourcePath is the path of the MCP endpoint itself, i.e. the request path
// with any well-known prefix already removed.
//
// A configured override always wins, so an operator fronted by something that rewrites the
// externally visible URL without forwarding headers can still pin the value.
//
// r.Host is used without an allow-list here on purpose. The well-known rule lives on the same
// generated HTTPRoute as the MCP endpoint and carries the MCPRoute's hostnames, so Envoy only
// selects it for a Host those hostnames match; any other Host never reaches the proxy. An
// MCPRoute without hostnames is reachable on every authority its listener accepts, and the
// identifier names whichever one the client used.
func resourceIdentifier(r *http.Request, prm *filterapi.MCPRouteOAuthProtectedResourceMetadata, resourcePath string) string {
	if prm != nil && prm.Resource != "" {
		// Emitted exactly as configured, including any trailing slash. The static direct
		// response this replaced did the same, so a route that pins resource sees a
		// byte-identical document before and after this change.
		return prm.Resource
	}
	identifier := externalScheme(r) + "://" + r.Host + resourcePath
	return strings.TrimSuffix(identifier, "/")
}

// resourceMetadataURL returns the URL of the Protected Resource Metadata document for the MCP
// endpoint this request was made against, per RFC 9728 section 3.1.
//
// The challenge URL drops a trailing slash on the identifier even though the document reproduces
// a pinned value verbatim, matching the 401 challenge the controller builds for the same route.
func resourceMetadataURL(r *http.Request, prm *filterapi.MCPRouteOAuthProtectedResourceMetadata, resourcePath string) string {
	return internalapi.OAuthProtectedResourceMetadataURL(resourceIdentifier(r, prm, resourcePath))
}

// serveOAuthProtectedResourceMetadata handles a request for the OAuth Protected Resource
// Metadata document. The MCPRoute is identified by the header Envoy sets on the dedicated
// route rule, the same way MCP traffic is routed.
func (m *mcpRequestContext) serveOAuthProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	// No method gate. The rule this replaced was an HTTPRouteFilter direct response matching
	// on path alone, so Envoy answered every method with the document. Rejecting anything here
	// would be a change in client-visible behaviour.
	routeName := r.Header.Get(internalapi.MCPRouteHeader)
	var route *mcpProxyConfigRoute
	if m.mcpProxyConfig != nil {
		route = m.routes[routeName]
	}
	if route == nil {
		// Envoy only sends this request for a programmed MCPRoute, so a route unknown to this
		// proxy means its config has not been loaded yet, e.g. during an extproc restart. A 404
		// would make the client conclude the resource has no metadata and abandon the OAuth flow,
		// whereas a 503 tells it to retry.
		m.l.Debug("MCP route not loaded, cannot serve protected resource metadata yet",
			"route", routeName, "path", externalPath(r))
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if route.prm == nil {
		m.l.Debug("no OAuth configuration for MCP route, not serving protected resource metadata",
			"route", routeName, "path", externalPath(r))
		http.NotFound(w, r)
		return
	}

	m.writeProtectedResourceMetadata(w, r, route.prm)
}

// writeProtectedResourceMetadataCORSHeaders reproduces exactly the headers ensureCORSHeaders
// put on the HTTPRouteFilter direct response. Browser-based MCP clients fetch this document
// cross-origin, and mcp-protocol-version is the request header they send with it.
func writeProtectedResourceMetadataCORSHeaders(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET")
	h.Set("Access-Control-Allow-Headers", "mcp-protocol-version")
}

// writeProtectedResourceMetadata writes the RFC 9728 Protected Resource Metadata document for
// the route, with the resource identifier derived from this very request.
func (m *mcpRequestContext) writeProtectedResourceMetadata(w http.ResponseWriter, r *http.Request, prm *filterapi.MCPRouteOAuthProtectedResourceMetadata) {
	// The MCP endpoint path is the request path with the well-known prefix removed. For an
	// MCPRoute serving "/mcp" the request arrives at "/.well-known/oauth-protected-resource/mcp".
	resourcePath := strings.TrimPrefix(externalPath(r), internalapi.OAuthProtectedResourceMetadataPath)

	doc := map[string]any{
		"resource":                 resourceIdentifier(r, prm, resourcePath),
		"authorization_servers":    []string{prm.Issuer},
		"bearer_methods_supported": []string{"header"},
	}
	if prm.ResourceName != "" {
		doc["resource_name"] = prm.ResourceName
	}
	if len(prm.ScopesSupported) > 0 {
		doc["scopes_supported"] = prm.ScopesSupported
	}
	if len(prm.ResourceSigningAlgValuesSupported) > 0 {
		doc["resource_signing_alg_values_supported"] = prm.ResourceSigningAlgValuesSupported
	}
	if prm.ResourceDocumentation != "" {
		doc["resource_documentation"] = prm.ResourceDocumentation
	}
	if prm.ResourcePolicyURI != "" {
		doc["resource_policy_uri"] = prm.ResourcePolicyURI
	}

	body, err := json.Marshal(doc)
	if err != nil {
		m.l.Error("failed to marshal OAuth protected resource metadata", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/json")
	writeProtectedResourceMetadataCORSHeaders(h)
	if prm.Resource == "" {
		// The document names this request's host and scheme. A cache that stores
		// /.well-known/* by path alone would serve one host's identifier to clients of
		// another, so the response must not be stored. Omitted when resource is pinned:
		// that response is request-independent, exactly as the static direct response was.
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write(body); err != nil {
		m.l.Debug("failed to write OAuth protected resource metadata response", "error", err)
	}
}
