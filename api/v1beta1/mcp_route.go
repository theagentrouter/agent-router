// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1beta1

import (
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// MCPRoutePrefixMode controls how tool and prompt names are prefixed when exposed to clients.
//
// PrefixMode can be set at the route level (MCPRouteSpec) and overridden per backend
// (MCPRouteBackendRef); the per-backend value takes precedence when set. A single route can
// therefore mix Always and Never backends. This is intentional: it lets an operator keep the
// route-level default (Always is recommended for most routes, since it guarantees a collision-free
// catalog without any extra configuration) while opting specific, well-known backends into bare
// names, e.g. to match tool names an existing client or interactive MCP App already hardcodes.
// Per-backend override is meant as a targeted escape hatch, not a replacement for choosing one
// naming policy for the route as a whole.
//
// +kubebuilder:validation:Enum=Always;Never
type MCPRoutePrefixMode string

const (
	// MCPRoutePrefixModeAlways prefixes tool/prompt names with `<backendName>__`.
	// This is the default and preserves existing behavior.
	MCPRoutePrefixModeAlways MCPRoutePrefixMode = "Always"

	// MCPRoutePrefixModeNever exposes tool/prompt names unprefixed.
	// Tool names must be unique across all Never-mode backends on the route.
	MCPRoutePrefixModeNever MCPRoutePrefixMode = "Never"
)

// MCPRoute defines how to route MCP requests to the backend MCP servers.
//
// This serves as a way to define a "unified" AI API for a Gateway which allows downstream
// clients to use a single schema API to interact with multiple MCP backends.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:storageversion
type MCPRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the details of the MCPRoute.
	Spec MCPRouteSpec `json:"spec,omitempty"`
	// Status defines the status details of the MCPRoute.
	Status MCPRouteStatus `json:"status,omitempty"`
}

// MCPRouteList contains a list of MCPRoute.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type MCPRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPRoute `json:"items"`
}

// MCPRouteSpec details the MCPRoute configuration.
type MCPRouteSpec struct {
	// ParentRefs are the names of the Gateway resources this MCPRoute is being attached to.
	// Cross namespace references are not supported. In other words, the Gateway resources must be in the
	// same namespace as the MCPRoute. Currently, each reference's Kind must be Gateway.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(match, match.kind == 'Gateway')", message="only Gateway is supported"
	ParentRefs []gwapiv1.ParentReference `json:"parentRefs"`

	// Path is the HTTP endpoint path that serves MCP requests over the Streamable HTTP transport.
	// If not specified, the default is "/mcp".
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=/mcp
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Path *string `json:"path,omitempty"`

	// Headers are HTTP headers that must match for this route to be selected.
	// Multiple match values are ANDed together, meaning, a request must match all the specified headers to select the route.
	//
	// +listType=map
	// +listMapKey=name
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Headers []gwapiv1.HTTPHeaderMatch `json:"headers,omitempty"`

	// Hostnames is a list of hostnames matched against the HTTP Host header to select this MCPRoute.
	// This is equivalent to the Hostnames field in the Gateway API HTTPRouteSpec. When specified, the
	// generated HTTPRoute includes these hostnames, so the MCP route lands in the per-host virtual host
	// instead of the wildcard vhost (the same scoping AIGatewayRoute.Hostnames provides).
	// See https://gateway-api.sigs.k8s.io/reference/api-types/httproute/#hostnames
	// for the details of the Hostnames field in the Gateway API.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Hostnames []gwapiv1.Hostname `json:"hostnames,omitempty"`

	// BackendRefs is a list of backend references to the MCP servers.
	// These MCP servers will be aggregated and exposed as a single MCP endpoint to the clients.
	// From the client's perspective, they only need to configure a single MCP server URL, e.g. "https://api.example.com/mcp",
	// and the Envoy AI Gateway will route the requests to the appropriate MCP server based on the requests.
	//
	// All names must be unique within this list to avoid potential tools, resources, etc. name collisions.
	// Also, cross-namespace references are not supported. In other words, the backend MCP servers must be in the
	// same namespace as the MCPRoute.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:XValidation:rule="self.all(i, self.exists_one(j, j.name == i.name))", message="all backendRefs names must be unique"
	BackendRefs []MCPRouteBackendRef `json:"backendRefs"`

	// SecurityPolicy defines the security policy for this MCPRoute.
	//
	// +kubebuilder:validation:Optional
	// +optional
	SecurityPolicy *MCPRouteSecurityPolicy `json:"securityPolicy,omitempty"`

	// BackendTrafficPolicy configures the auto-generated Envoy Gateway BackendTrafficPolicy
	// resources for this MCPRoute, for example the OAuth protected resource metadata policy
	// created when SecurityPolicy.OAuth is configured.
	//
	// +kubebuilder:validation:Optional
	// +optional
	BackendTrafficPolicy *MCPRouteBackendTrafficPolicy `json:"backendTrafficPolicy,omitempty"`

	// BackendSelector restricts which of this route's backends a given request may fan
	// out to, evaluated once per candidate backend when a client session is initialized.
	// If unspecified, all backends on the route are considered.
	//
	// +kubebuilder:validation:Optional
	// +optional
	BackendSelector *MCPBackendSelector `json:"backendSelector,omitempty"`

	// PrefixMode controls how tool and prompt names from all backends on this route are prefixed.
	//
	// +kubebuilder:validation:Optional
	// +optional
	PrefixMode *MCPRoutePrefixMode `json:"prefixMode,omitempty"`
}

// MCPRouteBackendTrafficPolicy configures how the Envoy Gateway BackendTrafficPolicy
// resources auto-generated for a MCPRoute interact with other BackendTrafficPolicy
// resources (e.g. an operator-defined rate limiting policy) targeting the same HTTPRoute.
type MCPRouteBackendTrafficPolicy struct {
	// MergeType determines how the auto-generated BackendTrafficPolicy (for example, the
	// OAuth protected resource metadata policy) is merged with other BackendTrafficPolicy
	// configurations targeting the same route or a parent Gateway/Listener.
	//
	// When set, the auto-generated BackendTrafficPolicy will merge with the closest parent
	// BackendTrafficPolicy in the route's attachment hierarchy instead of overriding it
	// entirely, per Envoy Gateway's merge semantics.
	//
	// If unset, no merging occurs, and the auto-generated BackendTrafficPolicy fully
	// overrides any other BackendTrafficPolicy targeting the same route.
	//
	// See: https://gateway.envoyproxy.io/docs/tasks/traffic/backend-traffic-policy/
	//
	// +kubebuilder:validation:XValidation:rule="self != 'Replace'",message="Replace is not a valid MergeType for BackendTrafficPolicy"
	// +optional
	MergeType *egv1a1.MergeType `json:"mergeType,omitempty"`
}

// MCPRouteBackendRef wraps a EG's BackendObjectReference to reference an MCP server.
// TODO: move to a standalone MCPBackend CRD to avoid k8s object size limit.
type MCPRouteBackendRef struct {
	gwapiv1.BackendObjectReference `json:",inline"`

	// Path is the HTTP endpoint path of the backend MCP server.
	// If not specified, the default is "/mcp".
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=/mcp
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Path *string `json:"path,omitempty"`

	// ToolSelector filters the tools exposed by this MCP server.
	// Supports exact matches and RE2-compatible regular expressions for both include and exclude patterns.
	// If not specified, all tools from the MCP server are exposed.
	// +kubebuilder:validation:Optional
	// +optional
	ToolSelector *MCPToolFilter `json:"toolSelector,omitempty"`

	// PromptSelector filters the prompts exposed by this MCP server, analogous to ToolSelector.
	// If not specified, all prompts from the MCP server are exposed.
	//
	// This is also used to determine which prompt names can be safely exposed unprefixed when
	// this backend's effective PrefixMode is Never: only prompts named in Include are validated
	// for cross-backend uniqueness and exposed bare. If unset while PrefixMode is Never, this
	// backend's prompts are not exposed bare (they keep the `<backendName>__` prefix) because
	// their names cannot be proven unique at admission time.
	//
	// +kubebuilder:validation:Optional
	// +optional
	PromptSelector *MCPPromptFilter `json:"promptSelector,omitempty"`

	// TODO: we can add resource selectors in the future.

	// SecurityPolicy is the security policy to apply to this MCP server.
	//
	// +kubebuilder:validation:Optional
	// +optional
	SecurityPolicy *MCPBackendSecurityPolicy `json:"securityPolicy,omitempty"`

	// ForwardHeaders specifies HTTP headers to extract from the incoming client request
	// and forward to this backend MCP server.
	// This enables per-user authentication passthrough (e.g., personal access tokens)
	// without requiring OAuth configuration.
	// Each entry specifies a header name to extract and an optional rename for the backend.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ForwardHeaders []MCPHeaderForward `json:"forwardHeaders,omitempty"`

	// PrefixMode controls how tool and prompt names from this backend are prefixed.
	// Takes precedence over the route-level PrefixMode when set.
	//
	// +kubebuilder:validation:Optional
	// +optional
	PrefixMode *MCPRoutePrefixMode `json:"prefixMode,omitempty"`
}

// MCPHeaderForward specifies a header to extract from the incoming request and forward to a backend.
type MCPHeaderForward struct {
	// Name is the header name to extract from the incoming client request.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// BackendHeader is the header name to use when forwarding to the backend.
	// If not specified, the original header name is used.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +optional
	BackendHeader *string `json:"backendHeader,omitempty"`
}

// MCPToolFilter filters tools using include and exclude patterns with exact matches or regular expressions.
// Exclude rules take precedence over include rules (deny-wins). When both include and exclude are specified,
// a tool must match an include rule AND not match any exclude rule to be allowed.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.include) && has(self.includeRegex))", message="include and includeRegex are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.exclude) && has(self.excludeRegex))", message="exclude and excludeRegex are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="has(self.include) || has(self.includeRegex) || has(self.exclude) || has(self.excludeRegex)", message="at least one of include, includeRegex, exclude, or excludeRegex must be specified"
type MCPToolFilter struct {
	// Include is a list of tool names to include. Only the specified tools will be available.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Include []string `json:"include,omitempty"`

	// IncludeRegex is a list of RE2-compatible regular expressions that, when matched, include the tool.
	// Only tools matching these patterns will be available.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	IncludeRegex []string `json:"includeRegex,omitempty"`

	// Exclude is a list of tool names to exclude. The specified tools will not be available.
	// Exclude rules take precedence over include rules.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Exclude []string `json:"exclude,omitempty"`

	// ExcludeRegex is a list of RE2-compatible regular expressions that, when matched, exclude the tool.
	// Tools matching these patterns will not be available. Exclude rules take precedence over include rules.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ExcludeRegex []string `json:"excludeRegex,omitempty"`
}

// MCPPromptFilter filters prompts using include and exclude patterns with exact matches or regular expressions.
// Exclude rules take precedence over include rules (deny-wins). When both include and exclude are specified,
// a prompt must match an include rule AND not match any exclude rule to be allowed.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.include) && has(self.includeRegex))", message="include and includeRegex are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.exclude) && has(self.excludeRegex))", message="exclude and excludeRegex are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="has(self.include) || has(self.includeRegex) || has(self.exclude) || has(self.excludeRegex)", message="at least one of include, includeRegex, exclude, or excludeRegex must be specified"
type MCPPromptFilter struct {
	// Include is a list of prompt names to include. Only the specified prompts will be available.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Include []string `json:"include,omitempty"`

	// IncludeRegex is a list of RE2-compatible regular expressions that, when matched, include the prompt.
	// Only prompts matching these patterns will be available.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	IncludeRegex []string `json:"includeRegex,omitempty"`

	// Exclude is a list of prompt names to exclude. The specified prompts will not be available.
	// Exclude rules take precedence over include rules.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Exclude []string `json:"exclude,omitempty"`

	// ExcludeRegex is a list of RE2-compatible regular expressions that, when matched, exclude the prompt.
	// Prompts matching these patterns will not be available. Exclude rules take precedence over include rules.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ExcludeRegex []string `json:"excludeRegex,omitempty"`
}

// MCPBackendSecurityPolicy defines the security policy for a backend MCP server.
type MCPBackendSecurityPolicy struct {
	// APIKey is a mechanism to access a backend. The API key will be injected into the request headers.
	// +optional
	APIKey *MCPBackendAPIKey `json:"apiKey,omitempty"`
}

// MCPBackendAPIKey defines the configuration for the API Key Authentication to a backend.
// When both `header` and `queryParam` are unspecified, the API key will be injected into the "Authorization" header by default.
//
// +kubebuilder:validation:XValidation:rule="(has(self.secretRef) && !has(self.inline)) || (!has(self.secretRef) && has(self.inline))", message="exactly one of secretRef or inline must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.header) && has(self.queryParam))", message="only one of header or queryParam can be set"
type MCPBackendAPIKey struct {
	// secretRef is the Kubernetes secret which contains the API keys.
	// The key of the secret should be "apiKey".
	// +optional
	SecretRef *gwapiv1.SecretObjectReference `json:"secretRef,omitempty"`

	// Inline contains the API key as an inline string.
	//
	// +optional
	Inline *string `json:"inline,omitempty"`

	// Header is the HTTP header to inject the API key into. If not specified,
	// defaults to "Authorization".
	// When the header is "Authorization", the injected header value will be
	// prefixed with "Bearer ".
	//
	// Either one of Header or QueryParam can be specified to inject the API key.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +optional
	Header *string `json:"header,omitempty"`

	// QueryParam is the HTTP query parameter to inject the API key into.
	// For example, if QueryParam is set to "api_key", and the API key is "mysecretkey", the request URL will be modified to include
	// "?api_key=mysecretkey".
	//
	// Either one of Header or QueryParam can be specified to inject the API key.
	//
	// Note: Embedding credentials in URLs (including query parameters) is generally not recommended because URLs can be exposed in logs
	// and intermediary systems; prefer header-based injection when possible.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +optional
	QueryParam *string `json:"queryParam,omitempty"`
}

// MCPRouteSecurityPolicy defines the security policy for a MCPRoute.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.authorization) && self.authorization.rules.exists(r, has(r.source) && has(r.source.jwt)) && !has(self.oauth))",message="oauth must be configured when any authorization rule uses a jwt source"
type MCPRouteSecurityPolicy struct {
	// OAuth defines the configuration for the MCP spec compatible OAuth authentication.
	//
	// +optional
	OAuth *MCPRouteOAuth `json:"oauth,omitempty"`

	// APIKeyAuth defines the configuration for the API Key Authentication.
	//
	// +optional
	APIKeyAuth *egv1a1.APIKeyAuth `json:"apiKeyAuth,omitempty"`

	// ExtAuth defines the configuration for External Authorization.
	//
	// +optional
	ExtAuth *egv1a1.ExtAuth `json:"extAuth,omitempty"`

	// Authorization defines the configuration for the MCP spec compatible authorization.
	//
	// +optional
	Authorization *MCPRouteAuthorization `json:"authorization,omitempty"`

	// MergeType determines how the auto-generated SecurityPolicy for this MCPRoute (which
	// enforces OAuth/JWT, API key, or external authorization) is merged with other
	// SecurityPolicy configurations targeting the same route or a parent Gateway/Listener.
	//
	// When set, the auto-generated SecurityPolicy will merge with the closest parent
	// SecurityPolicy in the route's attachment hierarchy (for example, one targeting a
	// Gateway, Gateway listener, ListenerSet, or ListenerSet listener) instead of
	// overriding it entirely, per Envoy Gateway's merge semantics.
	//
	// If unset, no merging occurs, and the auto-generated SecurityPolicy fully overrides
	// any other SecurityPolicy targeting the same route.
	//
	// See: https://gateway.envoyproxy.io/docs/tasks/security/apikey-authn/
	//
	// +kubebuilder:validation:XValidation:rule="self != 'Replace'",message="Replace is not a valid MergeType for SecurityPolicy"
	// +optional
	MergeType *egv1a1.MergeType `json:"mergeType,omitempty"`
}

// MCPRouteOAuth defines a MCP spec compatible OAuth authentication configuration for a MCPRoute.
// Reference: https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization
type MCPRouteOAuth struct {
	// Issuer is the authorization server's issuer identity.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=uri
	Issuer string `json:"issuer"`

	// Audiences is a list of JWT audiences allowed access.
	// It is recommended to set this field for token audience validation, as it is a security best practice to prevent token misuse.
	// Reference: https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization#token-audience-binding-and-validation
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Audiences []string `json:"audiences"`

	// JWKS defines how a JSON Web Key Sets (JWKS) can be obtained to verify the access tokens presented by the clients.
	//
	// If not specified, the JWKS URI will be discovered from the OAuth 2.0 Authorization Server Metadata
	// as per RFC 8414 by querying the `/.well-known/oauth-authorization-server` endpoint on the Issuer.
	//
	// +optional
	JWKS *JWKS `json:"jwks,omitempty"`

	// ProtectedResourceMetadata defines the OAuth 2.0 Resource Server Metadata as per RFC 8414.
	// This is used to expose the metadata endpoint for mcp clients to discover the authorization servers,
	// supported scopes, and JWKS URI.
	//
	// +kubebuilder:validation:Required
	ProtectedResourceMetadata ProtectedResourceMetadata `json:"protectedResourceMetadata"`

	// ClaimToHeaders specifies JWT claims to extract and forward as HTTP headers to backend MCP servers.
	// This enables backends to access user identity for authorization, auditing, or personalization.
	//
	// Security considerations:
	// - Any client-provided headers matching the configured header names will be stripped to prevent forgery
	// - Only the specified claims are extracted; the full JWT is not forwarded to backends
	// - Consider using a header prefix (e.g., "X-Jwt-Claim-") to avoid conflicts with other headers
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=16
	// +optional
	ClaimToHeaders []egv1a1.ClaimToHeader `json:"claimToHeaders,omitempty"`
}

// MCPRouteAuthorization defines the authorization configuration for a MCPRoute.
type MCPRouteAuthorization struct {
	// DefaultAction is the action to take when no rules match. If unspecified, defaults to Deny.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=Deny
	// +optional
	DefaultAction *egv1a1.AuthorizationAction `json:"defaultAction,omitempty"`

	// Rules defines a list of authorization rules.
	// These rules are evaluated in order, the first matching rule will be applied,
	// and the rest will be skipped.
	//
	// If no rules are defined, the default action will be applied to all requests.
	//
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Rules []MCPRouteAuthorizationRule `json:"rules,omitempty"`
}

// MCPRouteAuthorizationRule defines an authorization rule for MCPRoute based on the MCP authorization spec.
// Reference: https://modelcontextprotocol.io/specification/draft/basic/authorization#scope-challenge-handling
type MCPRouteAuthorizationRule struct {
	// Source defines the authorization source for this rule.
	// If not specified, the rule will match all sources.
	//
	// +kubebuilder:validation:Optional
	Source *MCPAuthorizationSource `json:"source,omitempty"`

	// Target defines the authorization target for this rule.
	// If not specified, the rule will match all targets.
	//
	// +kubebuilder:validation:Optional
	Target *MCPAuthorizationTarget `json:"target,omitempty"`

	// CEL specifies a Common Expression Language (CEL) expression evaluated for this rule.
	// The expression must return a boolean; evaluation errors or non-boolean results
	// are treated as "no match".
	//
	// Example CEL expressions:
	//	* `request.method == "POST"`
	//	* `request.headers["x-custom-header"] == "AllowedValue"`
	//	* `request.mcp.tool in ["toolA", "toolB"]`
	//
	// Available attributes in the CEL expression:
	//
	//	* request.method: HTTP method such as GET or POST. Type: string.
	//	* request.headers: map of headers with lowercased keys, first value only. Type: map[string]string.
	//	* request.headers_all: map of headers with lowercased keys, all values. Type: map[string][]string.
	//	* request.path: request path such as /mcp. Type: string.
	//	* request.auth.jwt.claims: JWT claims when a bearer JWT is present. Type: map[string]any.
	//	* request.auth.jwt.scopes: JWT scopes when a bearer JWT is present. Type: []string.
	//	* request.mcp.method: MCP method such as tools/list or tools/call. Type: string.
	//	* request.mcp.backend: upstream backend name (for example, "kiwi" or "github"). Type: string.
	//	* request.mcp.tool: tool name without backend prefix (for example, "list_issues"). Type: string.
	//	* request.mcp.params: parameters of the MCP method, including keys like "_meta" and "arguments". Type: object.
	//
	// Note: The CEL expression support is experimental, and the attributes
	// available to the expression may change in future releases.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	CEL *string `json:"cel,omitempty"`

	// Action is the authorization decision for matching requests. If unspecified, defaults to Allow.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=Allow
	// +optional
	Action *egv1a1.AuthorizationAction `json:"action,omitempty"`
}

// MCPBackendSelector defines which of a MCPRoute's backends a request may fan out to.
type MCPBackendSelector struct {
	// DefaultAction is the action to take when no rules match. If unspecified, defaults to Deny.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=Deny
	// +optional
	DefaultAction *egv1a1.AuthorizationAction `json:"defaultAction,omitempty"`

	// Rules defines a list of backend selection rules.
	// These rules are evaluated in order, the first matching rule will be applied,
	// and the rest will be skipped.
	//
	// If no rules are defined, DefaultAction is applied to every candidate backend
	// (defaults to Deny).
	//
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Rules []MCPBackendSelectorRule `json:"rules,omitempty"`
}

// MCPBackendSelectorRule defines a single backend selection rule.
type MCPBackendSelectorRule struct {
	// CEL specifies a Common Expression Language (CEL) expression, evaluated once for
	// each backend already listed in backendRefs. It does not parse a list of backends
	// out of the request. Each evaluation binds request.mcp.backend to the name of the
	// one candidate backend under test, so the expression should answer "is this backend
	// allowed", not "which backends should be used". The expression must return a
	// boolean; evaluation errors or non-boolean results are treated as "no match". A
	// match means the rule applies, and Action below decides whether that's an Allow or
	// a Deny for that backend.
	//
	// Example CEL expressions:
	//	* `request.mcp.backend in request.auth.jwt.claims.mcp_backends`
	//	* `("," + request.headers["x-ai-eg-mcp-backend-subset"] + ",").contains("," + request.mcp.backend + ",")`
	//
	// Available attributes are the same as documented on MCPRouteAuthorizationRule.CEL,
	// except request.mcp.method, request.mcp.tool, and request.mcp.params are not
	// populated (no MCP method has been selected yet at backend-selection time).
	//
	// Note: The CEL expression support is experimental, and the attributes
	// available to the expression may change in future releases.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	CEL *string `json:"cel,omitempty"`

	// Action is the decision for matching backends. If unspecified, defaults to Allow.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=Allow
	// +optional
	Action *egv1a1.AuthorizationAction `json:"action,omitempty"`
}

// MCPAuthorizationTarget defines the target of an authorization rule.
type MCPAuthorizationTarget struct {
	// Tools defines the list of tools this rule applies to.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Tools []ToolCall `json:"tools"`
}

// MCPAuthorizationSource defines the source of an authorization rule.
type MCPAuthorizationSource struct {
	// JWT defines the JWT scopes required for this rule to match.
	//
	// +kubebuilder:validation:Required
	JWT JWTSource `json:"jwt"`

	// TODO: JWTSource can be optional in the future when we support more source types.
}

// JWTSource defines the MCP authorization source for JWT tokens.
// At least one of scopes or claims must be provided.
// Scopes and claims are AND-ed: when both are specified, both sets must match.
//
// +kubebuilder:validation:XValidation:rule="(has(self.scopes) && size(self.scopes) > 0) || (has(self.claims) && size(self.claims) > 0)",message="either scopes or claims must be specified"
type JWTSource struct {
	// Scopes defines the list of JWT scopes required for the rule.
	// If multiple scopes are specified, all scopes must be present in the JWT for the rule to match.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Scopes []egv1a1.JWTScope `json:"scopes,omitempty"`

	// Claims defines the list of JWT claims required for the rule. Each claim must exist on the token
	// and have at least one of the expected values. Use to enforce tenant or subject-based access.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=16
	// +optional
	// +kubebuilder:validation:XValidation:rule="!self.exists(c, c.name == 'scope')",message="'scope' claim name is reserved for OAuth scopes"
	Claims []egv1a1.JWTClaim `json:"claims,omitempty"`
}

// ToolCall represents a tool call in the MCP authorization target.
type ToolCall struct {
	// Backend is the name of the backend this tool belongs to.
	//
	// +kubebuilder:validation:Required
	Backend string `json:"backend"`

	// Tool is the name of the tool.
	//
	// +kubebuilder:validation:Required
	Tool string `json:"tool"`
}

// JWKS defines how to obtain JSON Web Key Sets (JWKS) either from a remote HTTP/HTTPS endpoint or from a local source.
// +kubebuilder:validation:XValidation:rule="has(self.remoteJWKS) || has(self.localJWKS)", message="either remoteJWKS or localJWKS must be specified."
// +kubebuilder:validation:XValidation:rule="!(has(self.remoteJWKS) && has(self.localJWKS))", message="remoteJWKS and localJWKS cannot both be specified."
type JWKS struct {
	// RemoteJWKS defines how to fetch and cache JSON Web Key Sets (JWKS) from a remote
	// HTTP/HTTPS endpoint.
	//
	// +optional
	RemoteJWKS *egv1a1.RemoteJWKS `json:"remoteJWKS,omitempty"`

	// LocalJWKS defines how to get the JSON Web Key Sets (JWKS) from a local source.
	//
	// +optional
	LocalJWKS *egv1a1.LocalJWKS `json:"localJWKS,omitempty"`
}

// ProtectedResourceMetadata represents the Protected Resource Metadata	of the MCP server as per RFC 9728.
//
// References:
// * https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization#authorization-server-location
// * https://datatracker.ietf.org/doc/html/rfc9728#name-protected-resource-metadata
type ProtectedResourceMetadata struct {
	// Resource is the identifier of the protected resource.
	// This should match the MCPRoute's URL. For example, if the MCPRoute's URL is
	// "https://api.example.com/mcp", the Resource should be "https://api.example.com/mcp".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=uri
	Resource string `json:"resource"`

	// ResourceName is a human-readable name for the protected resource.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=256
	// +optional
	ResourceName *string `json:"resourceName,omitempty"`

	// ScopesSupported defines the minimal set of scopes required for the basic functionality of the MCPRoute.
	// It should avoid broad or overly permissive scopes to prevent clients from requesting tokens with excessive privileges.
	//
	// If an operation requires additional scopes that are not present in the access token, the client will receive a
	// 403 Forbidden response that includes the required scopes in the `scope` field of the `WWW-Authenticate` header.
	// This enables incremental privilege elevation through targeted `WWW-Authenticate: scope="..."` challenges when
	// privileged operations are first attempted.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ScopesSupported []string `json:"scopesSupported,omitempty"`

	// ResourceSigningAlgValuesSupported is a list of JWS signing algorithms supported by the resource server.
	// These algorithms are used in the "alg" field of the JOSE header in signed tokens.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +optional
	ResourceSigningAlgValuesSupported []string `json:"resourceSigningAlgValuesSupported,omitempty"`

	// ResourceDocumentation is a URL that provides human-readable documentation for the resource.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Format=uri
	// +optional
	ResourceDocumentation *string `json:"resourceDocumentation,omitempty"`

	// ResourcePolicyURI is a URL that points to the resource server's policy document.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Format=uri
	// +optional
	ResourcePolicyURI *string `json:"resourcePolicyUri,omitempty"`
}
