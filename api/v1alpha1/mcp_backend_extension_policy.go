// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import (
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// MCPBackendExtensionPolicy attaches Envoy Gateway extensions to the requests the MCP proxy sends to MCPRoute
// backends.
//
// For each policy, AI Gateway generates one EnvoyExtensionPolicy that selects, by label, the per-backend HTTPRoutes
// the policy is in effect for, and copies the resulting filters to the MCP backend listener.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:metadata:labels="gateway.networking.k8s.io/policy=direct"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 239",message="name must be at most 239 characters, as the generated EnvoyExtensionPolicy name adds a 14-character prefix"
type MCPBackendExtensionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the details of MCPBackendExtensionPolicy.
	Spec MCPBackendExtensionPolicySpec `json:"spec"`
	// Status defines the status details of the MCPBackendExtensionPolicy.
	Status MCPBackendExtensionPolicyStatus `json:"status,omitempty"`
}

// MCPBackendExtensionPolicySpec specifies the Envoy Gateway extensions attached to MCPRoute backends.
//
// The extension fields reuse Envoy Gateway's EnvoyExtensionPolicy types unchanged.
type MCPBackendExtensionPolicySpec struct {
	// TargetRefs are the MCPRoutes, in the policy's namespace, that this policy attaches to. SectionName selects one
	// backend by its MCPRoute.spec.backendRefs[].name; without it, the policy applies to all backends of the MCPRoute.
	//
	// When several policies select the same backend, one takes effect, following Gateway API policy attachment
	// (GEP-713): a policy with a sectionName beats one without, then the oldest wins, then the name that sorts first.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(ref, ref.group == 'aigateway.envoyproxy.io' && ref.kind == 'MCPRoute')", message="targetRefs must reference MCPRoute resources"
	TargetRefs []gwapiv1.LocalPolicyTargetReferenceWithSectionName `json:"targetRefs"`
	// Backends declares the callout targets of the extensions.
	// Same type and semantics as EnvoyExtensionPolicy.spec.backends.
	//
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	// +optional
	Backends []egv1a1.ExtensionBackend `json:"backends,omitempty"`
	// DynamicModule is an ordered list of dynamic module filters.
	// Same type and semantics as EnvoyExtensionPolicy.spec.dynamicModule.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	DynamicModule []egv1a1.DynamicModule `json:"dynamicModule"`
}

// MCPBackendExtensionPolicyList contains a list of MCPBackendExtensionPolicy
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type MCPBackendExtensionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPBackendExtensionPolicy `json:"items"`
}
