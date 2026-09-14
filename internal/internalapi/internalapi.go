// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package internalapi provides constants and functions used across the boundary
// among controller, extension server and extproc.
package internalapi

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

const (
	// EnvoyAIGatewayHeaderPrefix is the prefix for special headers used by AI Gateway, either for internal or external use.
	EnvoyAIGatewayHeaderPrefix = "x-ai-eg-"
	// EnvoyOriginalPathHeader is the Envoy header used to preserve the original request path.
	EnvoyOriginalPathHeader = "x-envoy-original-path"
	// OriginalPathHeader is the AI Gateway header used to preserve the original request path.
	OriginalPathHeader = EnvoyAIGatewayHeaderPrefix + "original-path"
	// InternalEndpointMetadataNamespace is the namespace used for the dynamic metadata for internal use.
	InternalEndpointMetadataNamespace = "aigateway.envoy.io"
	// InternalMetadataBackendNameKey is the key used to store the backend name
	InternalMetadataBackendNameKey = "per_route_rule_backend_name"
	// InternalMetadataUpstreamHostKey is the key used to store the resolved upstream host on endpoint
	// metadata (e.g. consumed by the AWS backend auth handler for SigV4 signing).
	InternalMetadataUpstreamHostKey = "upstream_host"
	// InternalMetadataRouteNameKey is the key used to store the route name.
	InternalMetadataRouteNameKey = "aigw_route_name"
	// UpstreamHostHeader carries the upstream host resolved at config time from the upstream ext_proc
	// filter to backend auth handlers. The AWS handler derives its SigV4 signing region from this host,
	// so there is no separate region header.
	UpstreamHostHeader = EnvoyAIGatewayHeaderPrefix + "upstream-host"
	// MCPBackendHeader is the special header key used to specify the target backend name.
	MCPBackendHeader = EnvoyAIGatewayHeaderPrefix + "mcp-backend"
	// MCPRouteHeader is the special header key used to identify the mcp route.
	MCPRouteHeader = EnvoyAIGatewayHeaderPrefix + "mcp-route"
	// MCPSubjectHeader carries the authenticated subject (the JWT "sub" claim) as extracted
	// and verified by Envoy's JWT filter via a claimToHeaders mapping.
	MCPSubjectHeader = EnvoyAIGatewayHeaderPrefix + "mcp-subject"
	// MCPBackendSubsetHeader is the trusted, shim-supplied comma-separated backend subset a request may fan out to.
	MCPBackendSubsetHeader = EnvoyAIGatewayHeaderPrefix + "mcp-backend-subset"
	// MCPBackendSubsetMetadataKey is the dynamic metadata key the shim sets; Envoy renders it into MCPBackendSubsetHeader.
	MCPBackendSubsetMetadataKey = "mcp_backend_subset"
	// MCPBackendListenerPort is the port for the MCP backend listener.
	MCPBackendListenerPort = 10088
	// MCPProxyPort is the port where the MCP proxy listens.
	MCPProxyPort = 9856
	// MCPGeneratedResourceCommonPrefix is the common prefix for all MCP-related generated resources.
	MCPGeneratedResourceCommonPrefix = "ai-eg-mcp-"
	// MCPMainHTTPRoutePrefix is the prefix for the main HTTPRoute resources generated for MCP.
	MCPMainHTTPRoutePrefix = MCPGeneratedResourceCommonPrefix + "main-"
	// MCPPerBackendRefHTTPRoutePrefix is the prefix for the per-backend-ref HTTPRoute resources generated for MCP.
	MCPPerBackendRefHTTPRoutePrefix = MCPGeneratedResourceCommonPrefix + "br-"
	// MCPPerBackendHTTPRouteFilterPrefix is the prefix for the HTTP route filter names for per-backend resources.
	MCPPerBackendHTTPRouteFilterPrefix = MCPGeneratedResourceCommonPrefix + "brf-"
	// MCPPerBackendCredentialSecretPrefix is the prefix for the credential secrets created for per-backend credential injection.
	MCPPerBackendCredentialSecretPrefix = MCPGeneratedResourceCommonPrefix + "cred-"

	// MCPMetadataHeaderPrefix is the prefix for special headers used to pass metadata in the filter metadata.
	// These headers are added internally to the requests to the upstream servers so they can be populated in the filter
	// metadata. These headers are considered just internal, and they'll be removed once they are stored in the filter
	// metadata to avoid sending unnecessary information to the upstream servers.
	MCPMetadataHeaderPrefix = "x-ai-eg-mcp-metadata-"
	// MCPMetadataHeaderRequestID is the special header key used to pass the MCP request ID in the filter metadata.
	MCPMetadataHeaderRequestID = MCPMetadataHeaderPrefix + "request-id"
	// MCPMetadataHeaderMethod is the special header key used to pass the MCP method in the filter metadata.
	MCPMetadataHeaderMethod = MCPMetadataHeaderPrefix + "method"
	// MCPMetadataHeaderToolName is the special header key used to pass the MCP tool name in the filter metadata.
	MCPMetadataHeaderToolName = MCPMetadataHeaderPrefix + "tool-name"
	// MCPMetadataHeaderResourceURI is the special header key used to pass the MCP resource URI in the filter metadata.
	MCPMetadataHeaderResourceURI = MCPMetadataHeaderPrefix + "resource-uri"

	// AWSCredentialOverrideHeaderPrefix is the default prefix for the three headers carrying a
	// per-request SigV4 credential. SigV4 takes three inputs, so unlike other auth types this is a
	// prefix, not a full header name.
	AWSCredentialOverrideHeaderPrefix = "x-aigw-aws-" //nolint:gosec // G101: a header name prefix, not a credential.
	// AWSCredentialOverrideMetadataKey is the default metadata key for a per-request AWS
	// credential. One key, not a prefix: the value is a struct holding all three inputs.
	AWSCredentialOverrideMetadataKey = "x-aigw-aws-credentials" //nolint:gosec // G101: a metadata key name, not a credential.
)

// AWSCredentialOverrideHeaderNames derives the three SigV4 header names from a prefix. The
// controller builds its strip list from it, the extproc reads them; it lives here so both agree.
func AWSCredentialOverrideHeaderNames(prefix string) (accessKeyID, secretAccessKey, sessionToken string) {
	return prefix + "access-key-id", prefix + "secret-access-key", prefix + "session-token"
}

// MCPInternalHeadersToMetadata maps special MCP headers to metadata keys.
//
// Only headers that do not survive to the router belong here. Headers the MCP proxy sets and leaves
// on the request - mcp-session-id, x-ai-eg-mcp-route - are already readable from an access log with
// %REQ(...)%, on both the MCP proxy listener and the backend listener, so mapping them would only add
// a second name for the same value.
var MCPInternalHeadersToMetadata = map[string]string{
	MCPBackendHeader:             "mcp_backend",
	MCPMetadataHeaderMethod:      "mcp_method",
	MCPMetadataHeaderRequestID:   "mcp_request_id",
	MCPMetadataHeaderToolName:    "mcp_tool_name",
	MCPMetadataHeaderResourceURI: "mcp_resource_uri",
}

const (
	// LogFormatText selects human-readable log output. This is the default for every binary.
	LogFormatText = "text"
	// LogFormatJSON selects JSON log output, for log pipelines that parse structured records.
	LogFormatJSON = "json"
)

// ValidateLogFormat checks that format is one of the supported log output formats. Callers that
// validate another binary's format wrap the error to say whose it is.
func ValidateLogFormat(format string) error {
	if format != LogFormatText && format != LogFormatJSON {
		return fmt.Errorf("invalid log format: %q, must be %q or %q", format, LogFormatText, LogFormatJSON)
	}
	return nil
}

const (
	// EndpointPickerHeaderKey is the header key used to specify the target backend endpoint.
	// This is the default header name in the reference implementation:
	// https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/2b5b337b45c3289e5f9367b2c19deef021722fcd/pkg/epp/server/runserver.go#L63
	EndpointPickerHeaderKey = "x-gateway-destination-endpoint"
)

const (
	// XDSClusterMetadataBackendNamePath is the full attribute path to access the backend name in cluster metadata in xDS attributes.
	XDSClusterMetadataBackendNamePath = "xds.cluster_metadata.filter_metadata['aigateway.envoy.io']['per_route_rule_backend_name']"
	// XDSUpstreamHostMetadataBackendNamePath is the full attribute path to access the backend name in upstream host metadata in xDS attributes.
	XDSUpstreamHostMetadataBackendNamePath = "xds.upstream_host_metadata.filter_metadata['aigateway.envoy.io']['per_route_rule_backend_name']"
	// XDSUpstreamHostMetadataUpstreamHostPath is the full attribute path to access the resolved upstream host in upstream host metadata in xDS attributes.
	XDSUpstreamHostMetadataUpstreamHostPath = "xds.upstream_host_metadata.filter_metadata['aigateway.envoy.io']['upstream_host']"
	// XDSRouteMetadataRouteNamePath is the full attribute path to access the route name in route metadata in xDS attributes.
	XDSRouteMetadataRouteNamePath = "xds.route_metadata.filter_metadata['aigateway.envoy.io']['aigw_route_name']"
)

// PerRouteRuleRefBackendName generates a unique backend name for a per-route rule,
// i.e., the unique identifier for a backend that is associated with a specific
// route rule in a specific AIGatewayRoute.
func PerRouteRuleRefBackendName(namespace, name, routeName string, routeRuleIndex, refIndex int) string {
	return fmt.Sprintf("%s/%s/route/%s/rule/%d/ref/%d", namespace, name, routeName, routeRuleIndex, refIndex)
}

// awsBedrockHostRE matches an AWS Bedrock runtime host — public, FIPS, PrivateLink (VPCE), or the
// newer api.aws domain — and captures the region, e.g. bedrock-runtime.us-east-1.amazonaws.com,
// bedrock-runtime-fips.us-east-1.amazonaws.com, vpce-<id>.bedrock-runtime.us-east-1.vpce.amazonaws.com,
// and bedrock-runtime.us-east-1.api.aws all yield "us-east-1". The anchors reject a spoofed suffix such
// as bedrock-runtime.us-east-1.amazonaws.com.evil.com.
var awsBedrockHostRE = regexp.MustCompile(`(?:^|\.)bedrock-runtime(?:-fips)?\.([a-z0-9-]+)\.(?:vpce\.amazonaws\.com|amazonaws\.com|api\.aws)$`)

// AWSBedrockRegionFromHost returns the AWS region encoded in a Bedrock signing host, or "" if no region
// can be derived from host. It is used to self-correct the SigV4 signing region when the resolved
// upstream host disagrees with the handler's configured region (e.g. a VPCE in a different region than
// the gateway was configured for).
//
// Any other host — including a custom/internal hostname such as bedrock.corp.internal, which encodes no
// region at all — yields "", and the caller falls back to its statically configured region. That
// fallback is correct in that case, not a bug: there is no region to extract from an arbitrary hostname.
func AWSBedrockRegionFromHost(host string) string {
	if m := awsBedrockHostRE.FindStringSubmatch(host); m != nil {
		return m[1]
	}
	return ""
}

const (
	// AIGatewayGeneratedHTTPRouteAnnotation is the annotation key used to mark
	// HTTPRoute resources that are generated by the AI Gateway controller.
	AIGatewayGeneratedHTTPRouteAnnotation = "ai-gateway-generated"
)

// ParseRequestHeaderAttributeMapping parses comma-separated key-value pairs for header-to-attribute mapping.
// The input format is "header1:attribute1,header2:attribute2" where header names are HTTP request
// headers and attribute names are Otel span or metric attributes.
// Example: "agent-session-id:session.id,x-tenant-id:tenant.id".
//
// Note: This serves a different purpose than OTEL's OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_SERVER_REQUEST,
// which captures headers as span attributes for tracing.
//
// Note: We do not need to convert to Prometheus format (e.g., agent-session-id → session.id) here,
// as that's done implicitly in the Prometheus exporter.
func ParseRequestHeaderAttributeMapping(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}

	result := make(map[string]string)
	pairs := strings.Split(s, ",")

	for i, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return nil, fmt.Errorf("empty header-attribute pair at position %d", i+1)
		}

		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid header-attribute pair at position %d: %q (expected format: header:attribute)", i+1, pair)
		}

		header := strings.TrimSpace(parts[0])
		attribute := strings.TrimSpace(parts[1])

		if header == "" || attribute == "" {
			return nil, fmt.Errorf("empty header or attribute at position %d: %q", i+1, pair)
		}

		result[header] = attribute
	}

	return result, nil
}

// MergeRequestHeaderAttributeMappings merges two header-to-attribute mappings.
// Keys in override replace keys in base.
func MergeRequestHeaderAttributeMappings(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(override))
	maps.Copy(merged, base)
	maps.Copy(merged, override)
	return merged
}

// FormatRequestHeaderAttributeMapping formats a header-to-attribute mapping into a stable, comma-separated string.
// The output is sorted by header name to make it deterministic for tests and configs.
func FormatRequestHeaderAttributeMapping(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		_, _ = b.WriteString(k)
		b.WriteByte(':')
		_, _ = b.WriteString(m[k])
	}
	return b.String()
}

// EndpointPrefixes represents well-known endpoint prefixes that AI Gateway supports.
type EndpointPrefixes struct {
	// OpenAI defaults to "/"
	OpenAI string
	// Cohere defaults to "/cohere"
	Cohere string
	// Anthropic defaults to "/anthropic"
	Anthropic string
}

// ParseEndpointPrefixes parses a comma-separated list of key:value pairs to populate EndpointPrefixes.
//
// Recognized keys (case-sensitive):
//   - openai
//   - cohere
//   - anthropic
//
// Format example:
//
//	"openai:/,cohere:/cohere,anthropic:/anthropic"
//
// Unknown keys cause an error; values must be non-empty.
func ParseEndpointPrefixes(s string) (EndpointPrefixes, error) {
	out := EndpointPrefixes{
		OpenAI:    "/",
		Cohere:    "/cohere",
		Anthropic: "/anthropic",
	}
	if s == "" {
		return out, nil
	}

	pairs := strings.Split(s, ",")
	for i, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return EndpointPrefixes{}, fmt.Errorf("empty endpointPrefixes pair at position %d", i+1)
		}

		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return EndpointPrefixes{}, fmt.Errorf("invalid endpointPrefixes pair at position %d: %q (expected format: key:value)", i+1, pair)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "openai":
			out.OpenAI = value
		case "cohere":
			out.Cohere = value
		case "anthropic":
			out.Anthropic = value
		default:
			return EndpointPrefixes{}, fmt.Errorf("unknown endpointPrefixes key %q at position %d (allowed: openai, cohere, anthropic)", key, i+1)
		}
	}
	return out, nil
}

// ModelNameHeaderKeyDefault is the default header key for the model name.
const ModelNameHeaderKeyDefault = aigv1b1.AIModelHeaderKey

// ModelNameHeaderKey is the configurable header key whose value is set by the gateway
// based on the model extracted from the request body.
//
// This header is automatically populated by the gateway and cannot be set by end users
// as it will be overwritten. The flow is:
//  1. Router filter extracts OriginalModel from request body and sets this header
//  2. HTTPRoute uses this header value for model-based routing
//  3. If backend has ModelNameOverride, the header is updated with the override value
//  4. Metrics and observability systems use the final header value
//
// Defaults to ModelNameHeaderKeyDefault.
type ModelNameHeaderKey = string

// ModelNameOverride represents a backend-specific model name that overrides
// the OriginalModel in the client request to the router.
//
// Configuration:
//   - Set via aigv1b1.AIGatewayRouteRuleBackendRef
//   - Replaces the OriginalModel with a backend-specific model name
//
// Example:
//   - server requests: "llama3-2-1b"
//   - Override to: "us.meta.llama3-2-1b-instruct-v1:0" (for AWS Bedrock)
//
// Effects:
//   - Updates the header specified by ModelNameHeaderKey
//   - Used by routing, rate limiting, and observability systems
type ModelNameOverride = string

// OriginalModel is the model name extracted from the incoming request body
// before any virtualization applies.
//
// Flow:
//  1. Router filter extracts model from request body
//  2. If ModelNameOverride is configured, RequestModel differs from OriginalModel
//  3. Provider responds with ResponseModel (may differ from RequestModel)
//
// Example:
//  1. OriginalModel: OpenAI Client sends: {"model": "gpt-5"}
//  2. RequestModel: ModelNameOverride replaces with "gpt-5-nano"
//  3. ResponseModel: OpenAI Platform sends: {"model": "gpt-5-nano-2025-08-07"}
//
// ### OpenTelemetry
//
// In OpenTelemetry Generative AI Metrics, this is an attribute on metrics such
// as "gen_ai.server.token.usage". For example, an OpenAI Chat Completion
// request to the "gpt-5" model results in a plain text string attribute:
// "gen_ai.original.model" -> "gpt-5"
type OriginalModel = string

// RequestModel is the name of the model sent in the request to perform a
// completion or to create embeddings.
//
// This is either the model received by the router's OpenAI Chat Completions or
// Embeddings endpoints, or a ModelNameOverride.
//
// This is not necessarily the same as ResponseModel, and in some cases like
// Azure OpenAI Service, this field isn't read at all.
//
// ### OpenTelemetry
//
// The RequestModel is a key attribute for correlating metrics with spans.
//
// In OpenInference (span semantics), this is the "model" field of invocation
// parameters, explaining how the LLM was invoked. For example, an OpenAI
// Chat Completion request to the "gpt-5-nano" model results in an JSON string
// attribute: "llm.invocation_parameters" -> {"model": "gpt-5-nano"}
//
// In OpenTelemetry Generative AI Metrics, this is an attribute on metrics such
// as "gen_ai.server.token.usage". For example, an OpenAI Chat Completion
// request to the "gpt-5-nano" model results in a plain text string attribute:
// "gen_ai.request.model" -> "gpt-5-nano"
type RequestModel = string

// ResponseModel is the name of the model that generated a response to a
// completion or embeddings request.
//
// ### Relationship to RequestModel
//
// This may differ from the RequestModel unless the provider is deterministic:
//   - Static Model Execution (AWS Bedrock)
//   - Deterministic Snapshot Mapping (GCP providers)
//
// In virtualized providers, this may be different:
//   - URI-Based Resolution (Azure OpenAI)
//   - Automatic Routing & Resolution: (OpenAI Platform)
//
// See https://aigateway.envoyproxy.io/docs/capabilities/traffic/model-name-virtualization
//
// ### OpenTelemetry
//
// The ResponseModel is even more important that RequestModel for evaluation
// use cases as it is the only field that authoritatively explains the model
// used for a completion. It is a key attribute for correlating metrics with
// spans.
//
// In OpenInference (span semantics), this is the "model_name" attribute.
// parameters, explaining how the LLM was invoked. For example, an OpenAI
// Chat Completion request to the "gpt-5-nano" model results in a plain text
// attribute of the latest model: "llm.model_name" -> "gpt-5-nano-2025-08-07"
//
// In OpenTelemetry Generative AI Metrics, this is an attribute on metrics such
// as "gen_ai.server.token.usage". For example, an OpenAI Chat Completion
// request to the "gpt-5-nano" model results in a plain text attribute of the
// latest model: "gen_ai.response.model" -> "gpt-5-nano-2025-08-07"
type ResponseModel = string

// AIGatewayFilterMetadataNamespace is the namespace used for the filter metadata related to AI Gateway.
//
// For example, token usage, input/output tokens, and request costs are stored in this namespace.
// Aliased from aigv1b1.AIGatewayFilterMetadataNamespace to avoid making ExtProc directly depend
// on the control plane API which is not a concern of ExtProc.
const AIGatewayFilterMetadataNamespace = aigv1b1.AIGatewayFilterMetadataNamespace
