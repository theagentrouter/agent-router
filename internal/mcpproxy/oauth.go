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

const (
	// oauthProtectedResourceMetadataPath is the well-known path prefix that serves the OAuth
	// Protected Resource Metadata document.
	//
	// References:
	// * https://datatracker.ietf.org/doc/html/rfc9728#name-protected-resource-metadata
	// * https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization#authorization-server-location
	oauthProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
)

// isProtectedResourceMetadataRequest reports whether the request targets the OAuth Protected
// Resource Metadata endpoint. Envoy has already matched the request to the dedicated route rule
// by the time it reaches the proxy; this only distinguishes it from MCP traffic on the same
// listener, so an exact prefix match on the well-known path is sufficient.
func isProtectedResourceMetadataRequest(path string) bool {
	if path == oauthProtectedResourceMetadataPath {
		return true
	}
	return strings.HasPrefix(path, oauthProtectedResourceMetadataPath+"/")
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
func externalPath(r *http.Request) string {
	return r.URL.Path
}

// resourceIdentifier returns the RFC 9728 resource identifier for the MCP endpoint this request
// was made against. resourcePath is the path of the MCP endpoint itself, i.e. the request path
// with any well-known prefix already removed.
//
// A configured override always wins, so an operator fronted by something that rewrites the
// externally visible URL without forwarding headers can still pin the value.
func resourceIdentifier(r *http.Request, oauth *filterapi.MCPRouteOAuth, resourcePath string) string {
	if oauth != nil && oauth.Resource != "" {
		// Emitted exactly as configured, including any trailing slash. The static direct
		// response this replaced did the same, so a route that pins resource sees a
		// byte-identical document before and after this change.
		return oauth.Resource
	}
	identifier := externalScheme(r) + "://" + r.Host + resourcePath
	return strings.TrimSuffix(identifier, "/")
}

// resourceMetadataURL returns the URL of the Protected Resource Metadata document for the MCP
// endpoint this request was made against, per RFC 9728 section 3.1: the well-known path is
// inserted between the identifier's authority and its path component.
func resourceMetadataURL(r *http.Request, oauth *filterapi.MCPRouteOAuth, resourcePath string) string {
	// buildResourceMetadataURL has always trimmed a trailing slash before splicing in the
	// well-known path, so the challenge URL keeps that normalization even though the document
	// reproduces the configured value verbatim.
	identifier := strings.TrimSuffix(resourceIdentifier(r, oauth, resourcePath), "/")

	prefixLen := 0
	switch {
	case strings.HasPrefix(identifier, "https://"):
		prefixLen = len("https://")
	case strings.HasPrefix(identifier, "http://"):
		prefixLen = len("http://")
	}

	baseURL, pathComponent := identifier, ""
	if i := strings.IndexByte(identifier[prefixLen:], '/'); i >= 0 {
		baseURL = identifier[:prefixLen+i]
		pathComponent = identifier[prefixLen+i:]
	}

	// Some clients do not expect the path component in the resource_metadata URL, but the spec
	// requires them to honor the value returned here. The document cannot be exposed at the
	// root because several MCP routes with different OAuth settings may share a listener.
	return baseURL + oauthProtectedResourceMetadataPath + pathComponent
}

// serveOAuthProtectedResourceMetadata handles a request for the OAuth Protected Resource
// Metadata document. The MCPRoute is identified by the header Envoy sets on the dedicated
// route rule, the same way MCP traffic is routed.
func (m *mcpRequestContext) serveOAuthProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	// No method gate. The rule this replaced was an HTTPRouteFilter direct response matching
	// on path alone, so Envoy answered every method with the document. Rejecting anything here
	// would be a change in client-visible behaviour.
	routeName := r.Header.Get(internalapi.MCPRouteHeader)
	var oauth *filterapi.MCPRouteOAuth
	if m.mcpProxyConfig != nil {
		if route := m.routes[routeName]; route != nil {
			oauth = route.oauth
		}
	}
	if oauth == nil {
		// Either the route is unknown to this proxy instance, or it does not configure OAuth.
		// Both mean there is no metadata document to serve for it.
		m.l.Debug("no OAuth configuration for MCP route, not serving protected resource metadata",
			"route", routeName, "path", externalPath(r))
		http.NotFound(w, r)
		return
	}

	m.writeProtectedResourceMetadata(w, r, oauth)
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
func (m *mcpRequestContext) writeProtectedResourceMetadata(w http.ResponseWriter, r *http.Request, oauth *filterapi.MCPRouteOAuth) {
	// The MCP endpoint path is the request path with the well-known prefix removed. For an
	// MCPRoute serving "/mcp" the request arrives at "/.well-known/oauth-protected-resource/mcp".
	resourcePath := strings.TrimPrefix(externalPath(r), oauthProtectedResourceMetadataPath)

	doc := map[string]any{
		"resource":                 resourceIdentifier(r, oauth, resourcePath),
		"authorization_servers":    []string{oauth.Issuer},
		"bearer_methods_supported": []string{"header"},
	}
	if oauth.ResourceName != "" {
		doc["resource_name"] = oauth.ResourceName
	}
	if len(oauth.ScopesSupported) > 0 {
		doc["scopes_supported"] = oauth.ScopesSupported
	}
	if len(oauth.ResourceSigningAlgValuesSupported) > 0 {
		doc["resource_signing_alg_values_supported"] = oauth.ResourceSigningAlgValuesSupported
	}
	if oauth.ResourceDocumentation != "" {
		doc["resource_documentation"] = oauth.ResourceDocumentation
	}
	if oauth.ResourcePolicyURI != "" {
		doc["resource_policy_uri"] = oauth.ResourcePolicyURI
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
	if oauth.Resource == "" {
		// The body depends on the forwarded scheme, so a shared cache must key on it. Host
		// needs no Vary: it is already part of the effective request URI a cache keys on.
		// Omitted when resource is pinned: that response is request-independent, exactly as
		// the static direct response was, and the header would itself be a behaviour change.
		h.Set("Vary", "X-Forwarded-Proto")
	}
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write(body); err != nil {
		m.l.Debug("failed to write OAuth protected resource metadata response", "error", err)
	}
}
