// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi

// MCPConfig is the configuration for the MCP listener and routing.
type MCPConfig struct {
	// BackendListenerAddr is the address that speaks plain HTTP and can be used to
	// route to each backend directly without interruption.
	//
	// The listener should only listen on the local interface, and equipped with
	// the HCM filter with the plain header-based routing for each backend based
	// on the [internalapi.MCPBackendHeader] header.
	BackendListenerAddr string `json:"backendListenerAddr"`

	// Routes is the list of routes that this listener can route to.
	Routes []MCPRoute `json:"routes,omitempty"`
}

// PrefixMode controls how tool and prompt names are prefixed when exposed to clients.
type PrefixMode string

const (
	// PrefixModeAlways prefixes tool/prompt names with "<backend>__" for every backend.
	// This is the default and preserves existing behavior.
	PrefixModeAlways PrefixMode = "Always"

	// PrefixModeNever exposes tool/prompt names unprefixed. The gateway resolves the
	// target backend at call time using the index built from the most recent tools/list
	// response for this session. Tool names must be unique across all backends on the route.
	PrefixModeNever PrefixMode = "Never"
)

// MCPRoute is the route configuration for routing to each MCP backend based on the tool name.
type MCPRoute struct {
	// Name is the fully qualified identifier of a MCPRoute.
	// This name is set in [internalapi.MCPRouteHeader] header to identify the route.
	Name MCPRouteName `json:"name"`

	// Backends is the list of backends that this route can route to.
	Backends []MCPBackend `json:"backends"`

	// Authorization is the authorization configuration for this route.
	Authorization *MCPRouteAuthorization `json:"authorization,omitempty"`

	// BackendSelector restricts which of this route's backends a request may fan out to.
	// It reuses the same MCPRouteAuthorization shape and CEL engine as Authorization above,
	// but is evaluated once per candidate backend at session-initialize time.
	BackendSelector *MCPRouteAuthorization `json:"backendSelector,omitempty"`

	// ForwardHeaders specifies HTTP headers to extract from the incoming request and forward to backend MCP servers.
	ForwardHeaders []string `json:"forwardHeaders,omitempty"`

	// PrefixMode is the route-level fallback PrefixMode applied to backends that do not
	// set their own per-backend PrefixMode. Defaults to Always if unset.
	PrefixMode PrefixMode `json:"prefixMode,omitempty"`
}

// MCPBackend is the MCP backend configuration.
type MCPBackend struct {
	// Name is the fully qualified identifier of a MCP backend.
	// This name is set in [internalapi.MCPBackendHeader] header to route the request to the specific backend.
	Name MCPBackendName `json:"name"`

	// ToolSelector filters the tools exposed by this backend. If not set, all tools are exposed.
	ToolSelector *MCPToolSelector `json:"toolSelector,omitempty"`

	// PromptSelector filters the prompts exposed by this backend. If not set, all prompts are
	// exposed, but this backend's prompts are never exposed bare under PrefixModeNever (see
	// MCPPromptSelector).
	PromptSelector *MCPPromptSelector `json:"promptSelector,omitempty"`

	// ForwardHeaders specifies HTTP headers to extract from the incoming request and forward to this backend.
	// Each entry maps a source header name to an optional destination header name.
	ForwardHeaders []MCPHeaderForward `json:"forwardHeaders,omitempty"`

	// PrefixMode controls how tool and prompt names from this backend are prefixed.
	// When set, overrides the route-level PrefixMode for this specific backend.
	// Defaults to Always if unset.
	PrefixMode PrefixMode `json:"prefixMode,omitempty"`
}

// MCPHeaderForward specifies a header to extract from the incoming request and forward to a backend.
type MCPHeaderForward struct {
	// Name is the header name to extract from the incoming client request.
	Name string `json:"name"`

	// BackendHeader is the header name to use when forwarding to the backend.
	// If empty, the original header name is used.
	BackendHeader string `json:"backendHeader,omitempty"`
}

// ForwardName returns the header name to use when forwarding to the backend.
// If BackendHeader is set, it is used; otherwise the original Name is used.
func (h MCPHeaderForward) ForwardName() string {
	if h.BackendHeader != "" {
		return h.BackendHeader
	}
	return h.Name
}

// MCPBackendName is the name of the MCP backend.
type MCPBackendName = string

// MCPToolSelector filters tools using include and exclude patterns with exact matches or regular expressions.
type MCPToolSelector struct {
	// Include is a list of tool names to include. Only the specified tools will be available.
	Include []string `json:"include,omitempty"`

	// IncludeRegex is a list of RE2-compatible regular expressions that, when matched, include the tool.
	// Only tools matching these patterns will be available.
	// TODO: regex is almost completely absent in the MCP ecosystem, consider removing this for simplicity.
	IncludeRegex []string `json:"includeRegex,omitempty"`

	// Exclude is a list of tool names to exclude. The specified tools will not be available.
	// Exclude rules take precedence over include rules.
	Exclude []string `json:"exclude,omitempty"`

	// ExcludeRegex is a list of RE2-compatible regular expressions that, when matched, exclude the tool.
	// Tools matching these patterns will not be available. Exclude rules take precedence over include rules.
	ExcludeRegex []string `json:"excludeRegex,omitempty"`
}

// MCPPromptSelector filters prompts using include and exclude patterns with exact matches or regular expressions.
//
// Unlike MCPToolSelector, declaring Include here also determines which prompt names may be
// exposed unprefixed under PrefixModeNever: the admission-time controller validation only
// allows a Never-mode backend's prompts to be exposed bare when they are enumerated here (see
// validatePerBackendPrefixMode), because otherwise their uniqueness across backends can't be
// proven ahead of time.
type MCPPromptSelector struct {
	// Include is a list of prompt names to include. Only the specified prompts will be available.
	Include []string `json:"include,omitempty"`

	// IncludeRegex is a list of RE2-compatible regular expressions that, when matched, include the prompt.
	// Only prompts matching these patterns will be available.
	IncludeRegex []string `json:"includeRegex,omitempty"`

	// Exclude is a list of prompt names to exclude. The specified prompts will not be available.
	// Exclude rules take precedence over include rules.
	Exclude []string `json:"exclude,omitempty"`

	// ExcludeRegex is a list of RE2-compatible regular expressions that, when matched, exclude the prompt.
	// Prompts matching these patterns will not be available. Exclude rules take precedence over include rules.
	ExcludeRegex []string `json:"excludeRegex,omitempty"`
}

// MCPRouteName is the name of the MCP route.
type MCPRouteName = string

// MCPRouteAuthorization defines the authorization configuration for a MCPRoute.
type MCPRouteAuthorization struct {
	// DefaultAction is the action to take when no rules match.
	DefaultAction AuthorizationAction `json:"defaultAction"`

	// Rules defines a list of authorization rules.
	// Requests that match any rule and satisfy the rule's conditions will be allowed.
	// Requests that do not match any rule or fail to satisfy the matched rule's conditions will be denied.
	// If no rules are defined, all requests will be denied.
	Rules []MCPRouteAuthorizationRule `json:"rules,omitempty"`

	// ResourceMetadataURL is the URI of the OAuth Protected Resource Metadata document for this route.
	// This is used to populate the WWW-Authenticate header when scope-based authorization fails.
	ResourceMetadataURL string `json:"resourceMetadataURL,omitempty"`
}

type AuthorizationAction string

const (
	// AuthorizationActionAllow is the action to allow the request.
	AuthorizationActionAllow AuthorizationAction = "Allow"
	// AuthorizationActionDeny is the action to deny the request.
	AuthorizationActionDeny AuthorizationAction = "Deny"
)

// MCPRouteAuthorizationRule defines an authorization rule for MCPRoute based on the MCP authorization spec.
// Reference: https://modelcontextprotocol.io/specification/draft/basic/authorization#scope-challenge-handling
type MCPRouteAuthorizationRule struct {
	// Action is the action to take when the rule matches.
	Action AuthorizationAction `json:"action"`

	// Source defines the authorization source for this rule.
	// If not specified, the rule will match all sources.
	Source *MCPAuthorizationSource `json:"source,omitempty"`

	// Target defines the authorization target for this rule.
	// If not specified, the rule will match all targets.
	Target *MCPAuthorizationTarget `json:"target,omitempty"`

	// CEL specifies a Common Expression Language (CEL) expression for this rule.
	// The expression must evaluate to true for the rule to match.
	// If empty, no CEL condition is applied.
	CEL *string `json:"cel,omitempty"`
}

type MCPAuthorizationTarget struct {
	// Tools defines the list of tools this rule applies to.
	Tools []ToolCall `json:"tools"`
}

type MCPAuthorizationSource struct {
	// JWT defines the JWT scopes required for this rule to match.
	JWT JWTSource `json:"jwt"`
}

type JWTSource struct {
	// Scopes defines the list of JWT scopes required for the rule.
	// If multiple scopes are specified, all scopes must be present in the JWT for the rule to match.
	Scopes []string `json:"scopes,omitempty"`

	// Claims defines the list of JWT claims required for the rule. Each claim must exist on the token
	// and have at least one of the expected values.
	Claims []JWTClaim `json:"claims,omitempty"`
}

type JWTClaim struct {
	// Name is the name of the claim, supports dotted paths for nested claims.
	Name string `json:"name"`

	// ValueType indicates whether the claim value is expected to be a string or string array.
	ValueType JWTClaimValueType `json:"valueType"`

	// Values are the values that the claim must match.
	// If the claim is a string type, the specified value must match exactly.
	// If the claim is a string array type, the specified value must match one of the values in the array.
	// If multiple values are specified, one of the values must match for the rule to match.
	Values []string `json:"values"`
}

type JWTClaimValueType string

const (
	JWTClaimValueTypeString      JWTClaimValueType = "String"
	JWTClaimValueTypeStringArray JWTClaimValueType = "StringArray"
)

type ToolCall struct {
	// Backend is the name of the backend this tool belongs to.
	Backend string `json:"backend"`

	// Tool is the name of the tool.
	Tool string `json:"tool"`
}
