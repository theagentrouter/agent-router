// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// AIServiceBackend is a resource that represents a single backend for AIGatewayRoute.
// A backend is a service that handles traffic with a concrete API specification.
//
// A AIServiceBackend is "attached" to a Backend which is either a k8s Service or a Backend resource of the Envoy Gateway.
//
// When a backend with an attached AIServiceBackend is used as a routing target in the AIGatewayRoute (more precisely, the
// HTTPRouteSpec defined in the AIGatewayRoute), the ai-gateway will generate the necessary configuration to do
// the backend specific logic in the final HTTPRoute.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:deprecatedversion:warning="aigateway.envoyproxy.io/v1alpha1 is deprecated; use aigateway.envoyproxy.io/v1beta1 instead"
type AIServiceBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the details of AIServiceBackend.
	Spec AIServiceBackendSpec `json:"spec,omitempty"`
	// Status defines the status details of the AIServiceBackend.
	Status AIServiceBackendStatus `json:"status,omitempty"`
}

// AIServiceBackendList contains a list of AIServiceBackends.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type AIServiceBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIServiceBackend `json:"items"`
}

// AIServiceBackendSpec details the AIServiceBackend configuration.
type AIServiceBackendSpec struct {
	// APISchema specifies the API schema of the output format of requests from
	// Envoy that this AIServiceBackend can accept as incoming requests.
	// Based on this schema, the ai-gateway will perform the necessary transformation for
	// the pair of AIGatewayRouteSpec.APISchema and AIServiceBackendSpec.APISchema.
	//
	// This is required to be set.
	//
	// +kubebuilder:validation:Required
	APISchema VersionedAPISchema `json:"schema"`
	// BackendRef is the reference to the Backend resource that this AIServiceBackend corresponds to.
	//
	// A backend must be a Backend resource of Envoy Gateway. Note that k8s Service will be supported
	// as a backend in the future. See https://github.com/envoyproxy/ai-gateway/issues/902 for more details.
	//
	// This is required to be set.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="has(self.kind) && self.kind == 'Backend' && has(self.group) && self.group == 'gateway.envoyproxy.io'",message="BackendRef must be a Backend resource of Envoy Gateway. See https://github.com/envoyproxy/ai-gateway/issues/902 for more details."
	BackendRef gwapiv1.BackendObjectReference `json:"backendRef"`

	// HeaderMutation defines the mutation of HTTP headers that will be applied to the request
	// before sending it to the backend.
	// +optional
	HeaderMutation *HTTPHeaderMutation `json:"headerMutation,omitempty"`

	// BodyMutation defines the mutation of HTTP request body JSON fields that will be applied to the request
	// before sending it to the backend.
	// +optional
	BodyMutation *HTTPBodyMutation `json:"bodyMutation,omitempty"`

	// ModelTranslationHints declares translation hints for models routed through this backend,
	// keyed by the exact ModelNameOverride used in an AIGatewayRouteRuleBackendRef that targets
	// this backend. This is the single source of truth for a model's hints on this backend: any
	// AIGatewayRoute, in any namespace, that references this backend with a matching
	// ModelNameOverride picks up these hints without repeating them.
	//
	// Since an AIServiceBackend has exactly one Schema, hints declared here are inherently scoped
	// to that provider — the same logical model hosted on a different AIServiceBackend (e.g. the
	// same Claude model on AWS Bedrock vs. GCP Vertex AI) is configured as a separate entry on
	// that other backend, which is correct because providers can genuinely support different
	// features for what is nominally the same model.
	//
	// +optional
	// +listType=map
	// +listMapKey=modelNameOverride
	ModelTranslationHints []ModelTranslationHintsEntry `json:"modelTranslationHints,omitempty"`

	// TODO: maybe add backend-level LLMRequestCost configuration that overrides the AIGatewayRoute-level LLMRequestCost.
	// 	That may be useful for the backend that has a different cost calculation logic.
}

// ModelTranslationHintsEntry associates ModelTranslationHints with a specific model routed
// through this backend.
type ModelTranslationHintsEntry struct {
	// ModelNameOverride identifies which model these hints apply to. Must match the
	// ModelNameOverride configured on an AIGatewayRouteRuleBackendRef that targets this backend
	// for the hints to take effect.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ModelNameOverride string `json:"modelNameOverride"`

	// ModelTranslationHints are the hints that apply to this model on this backend.
	ModelTranslationHints `json:",inline"`
}

// ModelTranslationHints provides model-specific hints to guide request and response translation.
// When set, translators use these values instead of model-name heuristics. All fields are
// optional; a nil field falls back to the built-in heuristic for backward compatibility.
//
// These hints are typically populated by a control plane from a model registry or catalog that
// tracks per-model metadata such as maximum output token limits and supported features.
type ModelTranslationHints struct {
	// MaxOutputTokens is the maximum number of tokens the model can produce in a single response.
	// When set, translators use this as the default max_tokens value for APIs that require it
	// (e.g., the Anthropic Messages API) when the client request omits max_tokens.
	//
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxOutputTokens *int64 `json:"maxOutputTokens,omitempty"`

	// SupportsResponseJSONSchema indicates whether the model accepts a JSON Schema object in
	// response_format (as opposed to only {"type": "json_object"}).
	//
	// +optional
	SupportsResponseJSONSchema *bool `json:"supportsResponseJsonSchema,omitempty"`

	// SupportsReasoningEffort indicates whether the model accepts a reasoning effort parameter
	// (Gemini thinking level / Anthropic output_config.effort).
	//
	// +optional
	SupportsReasoningEffort *bool `json:"supportsReasoningEffort,omitempty"`

	// SupportsOutputConfig indicates whether the model accepts an Anthropic-style output_config
	// field for structured output (e.g., Claude Opus/Sonnet 4.5 and later).
	//
	// +optional
	SupportsOutputConfig *bool `json:"supportsOutputConfig,omitempty"`
}

// HTTPHeaderMutation defines the mutation of HTTP headers that will be applied to the request
type HTTPHeaderMutation struct {
	// Set overwrites/adds the request with the given header (name, value)
	// before the action.
	//
	// Input:
	//   GET /foo HTTP/1.1
	//   my-header: foo
	//
	// Config:
	//   set:
	//   - name: "my-header"
	//     value: "bar"
	//
	// Output:
	//   GET /foo HTTP/1.1
	//   my-header: bar
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Set []gwapiv1.HTTPHeader `json:"set,omitempty"`

	// Remove the given header(s) from the HTTP request before the action. The
	// value of Remove is a list of HTTP header names. Note that the header
	// names are case-insensitive (see
	// https://datatracker.ietf.org/doc/html/rfc2616#section-4.2).
	//
	// Input:
	//   GET /foo HTTP/1.1
	//   my-header1: foo
	//   my-header2: bar
	//   my-header3: baz
	//
	// Config:
	//   remove: ["my-header1", "my-header3"]
	//
	// Output:
	//   GET /foo HTTP/1.1
	//   my-header2: bar
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	Remove []string `json:"remove,omitempty"`
}
