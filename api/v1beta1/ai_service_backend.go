// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1beta1

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
// +kubebuilder:storageversion
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
//
// +kubebuilder:validation:XValidation:rule="!has(self.headerValueFilters) || self.schema.name == 'GCPAnthropic' || self.schema.name == 'AWSAnthropic'",message="headerValueFilters is only honored by GCPAnthropic and AWSAnthropic backends"
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

	// HeaderValueFilters filter individual values out of multi-valued request headers before sending
	// the request to this backend, so that one value an upstream provider does not accept does not
	// fail the whole request. At most one filter per header name.
	//
	// Only GCPAnthropic and AWSAnthropic backends honor this, and today only for the
	// `anthropic-beta` header — a filter naming any other header is accepted but has no effect.
	// Setting it on a backend with any other schema is rejected.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	HeaderValueFilters []HTTPHeaderValueFilter `json:"headerValueFilters,omitempty"`

	// TODO: maybe add backend-level LLMRequestCost configuration that overrides the AIGatewayRoute-level LLMRequestCost.
	// 	That may be useful for the backend that has a different cost calculation logic.
}
