// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// ConditionTypeAccepted is a condition type for the reconciliation result
	// where resources are accepted.
	ConditionTypeAccepted = "Accepted"
	// ConditionTypeNotAccepted is a condition type for the reconciliation result
	// where resources are not accepted.
	ConditionTypeNotAccepted = "NotAccepted"
)

// Condition contains details for one aspect of the current state of this API Resource.
type Condition struct {
	// type of condition in CamelCase or in foo.example.com/CamelCase.
	//
	// +kubebuilder:validation:Enum=Accepted;NotAccepted
	// +kubebuilder:validation:MaxLength=316
	Type string `json:"type"`

	// status of the condition, one of True, False, Unknown.
	//
	// +kubebuilder:validation:Enum=True;False;Unknown
	Status metav1.ConditionStatus `json:"status"`

	// observedGeneration represents the .metadata.generation that the condition was set based upon.
	//
	// +kubebuilder:validation:Minimum=0
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastTransitionTime is the last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`

	// reason contains a programmatic identifier indicating the reason for the condition's last transition.
	//
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// message is a human readable message indicating details about the transition.
	//
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message"`
}

// AIGatewayRouteStatus contains the conditions by the reconciliation result.
type AIGatewayRouteStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	Conditions []Condition `json:"conditions"`
}

// AIServiceBackendStatus contains the conditions by the reconciliation result.
type AIServiceBackendStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	Conditions []Condition `json:"conditions"`
}

// BackendSecurityPolicyStatus contains the conditions by the reconciliation result.
type BackendSecurityPolicyStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	Conditions []Condition `json:"conditions"`
}

// MCPRouteStatus contains the conditions by the reconciliation result.
type MCPRouteStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	Conditions []Condition `json:"conditions"`
}

// QuotaPolicyStatus contains the conditions by the reconciliation result.
type QuotaPolicyStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	Conditions []Condition `json:"conditions"`
}
