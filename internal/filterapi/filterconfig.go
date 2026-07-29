// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package filterapi provides the configuration for the AI Gateway-implemented filter
// which is currently an external processor (See https://github.com/envoyproxy/ai-gateway/issues/90).
//
// This configuration must be decoupled from the Envoy Gateway types as well as its implementation
// details. Also, the configuration must not be tied with k8s so it can be tested and iterated
// without the need for the k8s cluster.
package filterapi

import (
	"log/slog"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// Config is the configuration for the Envoy AI Gateway filter.
type Config struct {
	// Version is the version of the AI Gateway, e.g., "v0.4.0" derived from internal/version package.
	// This is to ensure compatibility between the filter and the AI Gateway management plane.
	//
	// When there's discrepancy between the version set here (by the controller) and the version of the extproc
	// filter binary, which can happen during rolling upgrade, the filter will not load the configuration,
	// and keep working with the previous configuration.
	Version string `json:"version,omitempty"`
	// UUID is the unique identifier of the filter configuration assigned by the AI Gateway when the configuration is updated.
	UUID string `json:"uuid,omitempty"`
	// GlobalLLMRequestCosts configures gateway-level default costs for LLM requests.
	// These costs apply to all routes unless overridden by route-specific LLMRequestCosts.
	GlobalLLMRequestCosts []GlobalLLMRequestCost `json:"globalLLMRequestCosts,omitempty"`
	// LLMRequestCost configures the cost of each LLM-related request. Optional. If this is provided, the filter will populate
	// the "calculated" cost in the filter metadata at the end of the response body processing.
	LLMRequestCosts []LLMRequestCost `json:"llmRequestCosts,omitempty"`
	// Backends is the list of backends that this listener can route to.
	Backends []Backend `json:"backends,omitempty"`
	// Models is the list of models that this route is aware of. Used to populate the "/models" endpoint in OpenAI-compatible APIs.
	Models []Model `json:"models,omitempty"`
	// ModelsByHost is the list of models keyed by hostname. When present, the extproc "/v1/models" processor will prefer
	// the hostname-specific list over the global Models slice. Each per-host list also includes UnscopedModels so that
	// routes without hostname scoping remain visible alongside the host-specific routes.
	ModelsByHost map[string][]Model `json:"modelsByHost,omitempty"`
	// UnscopedModels is the list of models contributed by routes that did NOT declare hostnames. When ModelsByHost is
	// configured, requests to a host that doesn't match any entry fall back to this list (rather than to Models, which
	// would leak host-scoped models to unknown hosts).
	UnscopedModels []Model `json:"unscopedModels,omitempty"`
	// ModelsByHostAndScope maps hostname to header-scoped model groups. When a route has no hostnames, use "" as the
	// host key. Header scoping composes with hostname scoping: a rule with both hostnames and scope headers contributes
	// only when both match.
	ModelsByHostAndScope map[string]HeaderScopedModels `json:"modelsByHostAndScope,omitempty"`
	// MCPConfig is the configuration for the MCPRoute implementations.
	MCPConfig *MCPConfig `json:"mcpConfig,omitempty"`
}

// HeaderScopedModels groups models by header scope within a host (or globally when the host key is "").
type HeaderScopedModels struct {
	// ModelsByHeaderScope maps a canonical scope key to models. The scope key is a sorted, comma-separated list of
	// lowercased "name=value" pairs for all exact-match headers in a rule except x-ai-eg-model, e.g. "x-jwt-sub=client-a".
	ModelsByHeaderScope map[string][]Model `json:"modelsByHeaderScope,omitempty"`
	// UnscopedHeaderModels holds models from rules with no non-model exact-match headers. Visible to all header scopes
	// on the same host; also returned when the request does not match any scoped rule.
	UnscopedHeaderModels []Model `json:"unscopedHeaderModels,omitempty"`
}

// Model corresponds to the OpenAI model object in the OpenAI-compatible APIs
// and is used to populate the "/models" endpoint in OpenAI-compatible APIs.
type Model struct {
	// Name will be exported as the field of "ID" in OpenAI-compatible APIs.
	Name string
	// ownedBy will be exported as the field of "OwnedBy" in OpenAI-compatible API "/models".
	OwnedBy string
	// createdAt will be exported as the field of "Created" in OpenAI-compatible API "/models".
	CreatedAt time.Time
}

// GlobalLLMRequestCost specifies gateway-level default request cost configuration.
// This is identical to LLMRequestCost but without the RouteName field, as global costs
// apply to all routes and are not scoped to a specific route.
type GlobalLLMRequestCost struct {
	// MetadataKey is the key of the metadata storing the request cost.
	MetadataKey string `json:"metadataKey"`
	// Type is the kind of the request cost calculation.
	Type LLMRequestCostType `json:"type"`
	// CEL is the CEL expression to calculate the cost of the request.
	// This is not empty when the Type is LLMRequestCostTypeCEL.
	CEL string `json:"cel,omitempty"`
}

// LLMRequestCost specifies "where" the request cost is stored in the filter metadata as well as
// "how" the cost is calculated. By default, the cost is retrieved from "output token" in the response body.
//
// This can be used to subtract the usage token from the usage quota in the rate limit filter when
// the request completes combined with `apply_on_stream_done` and `hits_addend` fields of
// the rate limit configuration https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/route/v3/route_components.proto#config-route-v3-ratelimit
// introduced in Envoy 1.33.
type LLMRequestCost struct {
	// MetadataKey is the key of the metadata storing the request cost.
	MetadataKey string `json:"metadataKey"`
	// RouteName scopes this cost to a single AIGatewayRoute (format "namespace/name").
	// When empty, the cost applies to any request (wildcard). The controller sets this for each route.
	RouteName string `json:"routeName,omitempty"`
	// Type is the kind of the request cost calculation.
	Type LLMRequestCostType `json:"type"`
	// CEL is the CEL expression to calculate the cost of the request.
	// This is not empty when the Type is LLMRequestCostTypeCEL.
	CEL string `json:"cel,omitempty"`
	// Backend is an optional filter set exclusively by the QuotaPolicy controller.
	// It is NOT exposed in any user-facing CRD. When non-empty, this cost entry is
	// only evaluated when the serving backend's short name (namespace/name) matches.
	// This allows different QuotaPolicies targeting different backends to use different
	// cost expressions while sharing the same metadata key.
	Backend string `json:"backend,omitempty"`
	// Model is an optional filter set exclusively by the QuotaPolicy controller.
	// It is NOT exposed in any user-facing CRD. When non-empty, this cost entry is
	// only evaluated when the request's model name matches. This allows a single
	// metadata key to be shared across models without conflicting overwrites.
	Model string `json:"model,omitempty"`
}

// LLMRequestCostType specifies the kind of the request cost calculation.
type LLMRequestCostType string

const (
	// LLMRequestCostTypeOutputToken specifies that the request cost is calculated from the output token.
	LLMRequestCostTypeOutputToken LLMRequestCostType = "OutputToken"
	// LLMRequestCostTypeInputToken specifies that the request cost is calculated from the input token.
	LLMRequestCostTypeInputToken LLMRequestCostType = "InputToken"
	// LLMRequestCostTypeCachedInputToken specifies that the request cost is calculated from the cached read input token.
	LLMRequestCostTypeCachedInputToken LLMRequestCostType = "CachedInputToken"
	// LLMRequestCostTypeCacheCreationInputToken specifies that the request cost is calculated from the cache creation input token.
	LLMRequestCostTypeCacheCreationInputToken LLMRequestCostType = "CacheCreationInputToken"
	// LLMRequestCostTypeTotalToken specifies that the request cost is calculated from the total token.
	LLMRequestCostTypeTotalToken LLMRequestCostType = "TotalToken"
	// LLMRequestCostTypeReasoningToken specifies that the request cost is calculated from the reasoning token.
	LLMRequestCostTypeReasoningToken LLMRequestCostType = "ReasoningToken"
	// LLMRequestCostTypeCEL specifies that the request cost is calculated from the CEL expression.
	LLMRequestCostTypeCEL LLMRequestCostType = "CEL"
)

// VersionedAPISchema corresponds to VersionedAPISchema in api/v1alpha1/api.go.
type VersionedAPISchema struct {
	// Name is the name of the API schema.
	Name APISchemaName `json:"name"`
	// Version is the version of the API schema. Optional.
	Version string `json:"version,omitempty"`
	// Prefix is the prefix of the API schema. Optional. Used for OpenAI and Anthropic schemas.
	Prefix string `json:"prefix,omitempty"`
}

// OpenAIPrefix returns the OpenAI API prefix for the VersionedAPISchema.
func (v VersionedAPISchema) OpenAIPrefix() string {
	return v.Prefix
}

// AnthropicPrefix returns the Anthropic API prefix for the VersionedAPISchema.
func (v VersionedAPISchema) AnthropicPrefix() string {
	if v.Prefix == "" {
		return "v1"
	}
	return v.Prefix
}

// APISchemaName corresponds to APISchemaName in api/v1alpha1/api.go.
type APISchemaName string

const (
	// APISchemaOpenAI represents the standard OpenAI API schema.
	APISchemaOpenAI APISchemaName = "OpenAI"
	// APISchemaCohere represents the Cohere API schema.
	APISchemaCohere APISchemaName = "Cohere"
	// APISchemaAWSBedrock represents the AWS Bedrock API schema.
	// Used for models hosted on AWS Bedrock. Chat completions use the Converse API,
	// while embeddings use the InvokeModel API.
	APISchemaAWSBedrock APISchemaName = "AWSBedrock"
	// APISchemaAzureOpenAI represents the Azure OpenAI API schema.
	APISchemaAzureOpenAI APISchemaName = "AzureOpenAI"
	// APISchemaGCPVertexAI represents the Google Cloud Gemini API schema.
	// Used for Gemini models hosted on Google Cloud Vertex AI.
	APISchemaGCPVertexAI APISchemaName = "GCPVertexAI"
	// APISchemaGCPAnthropic represents the Google Cloud Anthropic API schema.
	// Used for Claude models hosted on Google Cloud Vertex AI.
	APISchemaGCPAnthropic APISchemaName = "GCPAnthropic"
	// APISchemaAnthropic represents the standard Anthropic API schema.
	APISchemaAnthropic APISchemaName = "Anthropic"
	// APISchemaAWSAnthropic represents the AWS Bedrock Anthropic API schema.
	// Used for Claude models hosted on AWS Bedrock. Supports both OpenAI and Anthropic input formats
	// depending on the endpoint path, similar to APISchemaGCPAnthropic.
	APISchemaAWSAnthropic APISchemaName = "AWSAnthropic"
)

// RouteRuleName is the name of the route rule.
type RouteRuleName string

// Backend corresponds to AIGatewayRouteRuleBackendRef in api/v1alpha1/api.go
// besides that this abstracts the concept of a backend at Envoy Gateway level to a simple name.
type Backend struct {
	// Name of the backend including the route name as well as the route rule index.
	Name              string                        `json:"name"`
	ModelNameOverride internalapi.ModelNameOverride `json:"modelNameOverride"`
	// Schema specifies the API schema of the output format of requests from.
	Schema VersionedAPISchema `json:"schema"`
	// Auth is the authn/z configuration for the backend. Optional.
	Auth *BackendAuth `json:"auth,omitempty"`
	// Sensitive Headers to be removed from the request before sending to the backend. Optional.
	HeaderMutation *HTTPHeaderMutation `json:"httpHeaderMutation,omitempty"`
	// Body mutations to be applied to the request before sending to the backend. Optional.
	BodyMutation *HTTPBodyMutation `json:"httpBodyMutation,omitempty"`
}

// BackendAuth corresponds partially to BackendSecurityPolicy in api/v1alpha1/api.go.
type BackendAuth struct {
	// APIKey is a location of the api key secret file.
	APIKey *APIKeyAuth `json:"apiKey,omitempty"`
	// AWSAuth specifies the location of the AWS credential file and region.
	AWSAuth *AWSAuth `json:"aws,omitempty"`
	// AzureAPIKey is the Azure OpenAI API key.
	AzureAPIKey *AzureAPIKeyAuth `json:"azureAPIKey,omitempty"`
	// AnthropicAPIKey is the Anthropic API key.
	AnthropicAPIKey *AnthropicAPIKeyAuth `json:"anthropicAPIKey,omitempty"`
	// AzureAuth specifies the location of Azure access token file.
	AzureAuth *AzureAuth `json:"azure,omitempty"`
	// GCPAuth specifies the location of GCP credential file.
	GCPAuth *GCPAuth `json:"gcp,omitempty"`
	// CredentialOverride, when non-nil, sources the credential per-request instead of the
	// static credential above. nil disables per-request sourcing (the default).
	CredentialOverride *CredentialOverride `json:"credentialOverride,omitempty"`
}

// CredentialOverride configures per-request credential sourcing for a backend.
// Exactly one of HeaderName or DynamicMetadataNamespace is set (resolved by the controller).
type CredentialOverride struct {
	// HeaderName is the request header that carries the per-request credential.
	// Set for fromRequestHeaders source; empty for fromDynamicMetadata source.
	HeaderName string `json:"headerName,omitempty"`
	// DynamicMetadataNamespace is the Envoy metadata namespace to read from.
	// Set for fromDynamicMetadata source; empty for fromRequestHeaders source.
	DynamicMetadataNamespace string `json:"dynamicMetadataNamespace,omitempty"`
	// DynamicMetadataKey is the key within DynamicMetadataNamespace.
	DynamicMetadataKey string `json:"dynamicMetadataKey,omitempty"`
	// FallbackToConfigured controls behaviour when the source value is absent.
	// true falls back to the static credential; false returns 401 to the caller.
	FallbackToConfigured bool `json:"fallbackToConfigured"`
	// InputHeaderToRemove is the header that should be stripped before the request
	// reaches the upstream backend. Only set for HeaderName source.
	// The controller adds this to the backend HeaderMutation.Remove list so the
	// HeaderMutator tells Envoy to remove it upstream while keeping it visible
	// in the local requestHeaders map for the handler to read.
	InputHeaderToRemove string `json:"inputHeaderToRemove,omitempty"`
}

// AWSAuth defines the credentials needed to access AWS.
type AWSAuth struct {
	// CredentialFileLiteral is the literal string of the AWS credential file. E.g.
	// [default]\naws_access_key_id = <access-key-id>\naws_secret_access_key = <secret-access-key>\naws_session_token = <session-token>.
	CredentialFileLiteral string `json:"credentialFileLiteral,omitempty"`
	Region                string `json:"region"`
}

// LogValue implements slog.LogValuer for AWSAuth to redact sensitive information.
func (a AWSAuth) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("credentialFileLiteral", "[REDACTED]"),
		slog.String("region", a.Region),
	)
}

// APIKeyAuth defines the file that will be mounted to the external proc.
type APIKeyAuth struct {
	// Key is the API key as a literal string.
	Key string `json:"key"`
}

// LogValue implements slog.LogValuer for APIKeyAuth to redact sensitive information.
func (a APIKeyAuth) LogValue() slog.Value {
	return slog.GroupValue(slog.String("key", "[REDACTED]"))
}

// AzureAPIKeyAuth defines the Azure OpenAI API key.
type AzureAPIKeyAuth struct {
	// Key is the Azure API key as a literal string.
	Key string `json:"key"`
}

// LogValue implements slog.LogValuer for AzureAPIKeyAuth to redact sensitive information.
func (a AzureAPIKeyAuth) LogValue() slog.Value {
	return slog.GroupValue(slog.String("key", "[REDACTED]"))
}

// AnthropicAPIKeyAuth defines the Anthropic API key.
type AnthropicAPIKeyAuth struct {
	// Key is the Anthropic API key as a literal string.
	Key string `json:"key"`
}

// LogValue implements slog.LogValuer for AnthropicAPIKeyAuth to redact sensitive information.
func (a AnthropicAPIKeyAuth) LogValue() slog.Value {
	return slog.GroupValue(slog.String("key", "[REDACTED]"))
}

// AzureAuth defines the file containing azure access token that will be mounted to the external proc.
type AzureAuth struct {
	// AccessToken is the access token as a literal string.
	AccessToken string `json:"accessToken"`
}

// LogValue implements slog.LogValuer for AzureAuth to redact sensitive information.
func (a AzureAuth) LogValue() slog.Value {
	return slog.GroupValue(slog.String("accessToken", "[REDACTED]"))
}

// GCPAuth defines the GCP authentication configuration used to access Google Cloud AI services.
type GCPAuth struct {
	// AccessToken is the access token as a literal string.
	// This token is obtained through GCP Workload Identity Federation and service account impersonation.
	// The token is automatically rotated by the BackendSecurityPolicy controller before expiration.
	AccessToken string `json:"accessToken"`
	// Region is the GCP region to use for the request.
	// This is used in URL path templates when making requests to GCP Vertex AI endpoints.
	// Examples: "us-central1", "europe-west4".
	Region string `json:"region"`
	// ProjectName is the GCP project name to use for the request.
	// This is used in URL path templates when making requests to GCP Vertex AI endpoints.
	// This should be the project where Vertex AI APIs are enabled.
	ProjectName string `json:"projectName"`
}

// LogValue implements slog.LogValuer for GCPAuth to redact sensitive information.
func (g GCPAuth) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("accessToken", "[REDACTED]"),
		slog.String("region", g.Region),
		slog.String("projectName", g.ProjectName),
	)
}

// HTTPHeaderMutation defines the mutation of HTTP headers that will be applied to the request
type HTTPHeaderMutation struct {
	// Set overwrites the request with the given header (name, value)
	// before the action.
	Set []HTTPHeader `json:"set,omitempty"`
	// Remove the given header(s) from the HTTP request before the action. The
	// value of Remove is a list of HTTP header names.
	// This is always ensured to be lower-cased like Envoy does internally.
	Remove []string `json:"remove,omitempty"`
}

// HTTPHeader represents an HTTP Header name and value as defined by RFC 7230.
type HTTPHeader struct {
	// Name is the name of the HTTP Header to be matched.
	// This is always ensured to be lower-cased like Envoy does internally.
	Name string `json:"name"`
	// Value is the value of HTTP Header to be matched.
	Value string `json:"value"`
}

// LogValue implements slog.LogValuer for HTTPHeader to redact sensitive information.
func (h HTTPHeader) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", h.Name),
		slog.String("value", "[REDACTED]"),
	)
}

// HTTPBodyMutation defines the mutation of HTTP request body JSON fields that will be applied to the request
type HTTPBodyMutation struct {
	// Set overwrites/adds the request body with the given JSON field (name, value)
	// before sending to the backend. Only top-level fields are currently supported.
	Set []HTTPBodyField `json:"set,omitempty"`
	// Remove the given JSON field(s) from the HTTP request body before sending to the backend.
	// The value of Remove is a list of top-level field names to remove.
	Remove []string `json:"remove,omitempty"`
}

// HTTPBodyField represents a JSON field name and value for body mutation
type HTTPBodyField struct {
	// Path is the top-level field name to set in the request body.
	// Examples: "service_tier", "max_tokens", "temperature"
	Path string `json:"path"`
	// Value is the JSON value to set at the specified field. This can be any valid JSON value:
	// string, number, boolean, object, array, or null.
	// The value will be parsed as JSON and inserted at the specified field.
	Value string `json:"value"`
}

// UnmarshalConfigYaml reads the file at the given path and unmarshals it into a Config struct.
func UnmarshalConfigYaml(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
