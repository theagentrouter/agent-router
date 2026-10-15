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

// AIGatewayRouteStatus contains the conditions by the reconciliation result.
type AIGatewayRouteStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// AIServiceBackendStatus contains the conditions by the reconciliation result.
type AIServiceBackendStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BackendSecurityPolicyStatus contains the conditions by the reconciliation result.
type BackendSecurityPolicyStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MCPRouteStatus contains the conditions by the reconciliation result.
type MCPRouteStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GuardrailPolicyStatus contains the conditions by the reconciliation result.
type GuardrailPolicyStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// QuotaPolicyStatus contains the conditions by the reconciliation result.
type QuotaPolicyStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
