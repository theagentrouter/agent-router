// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import (
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// QuotaPolicy specifies token quota configuration for inference services.
// Generates rate limit configuration and tracks quota usage.
// Reject requests with 429 once all related quota to that request has been exceeded.
//
// TODO: Waiting on next release of Envoyproxy that will support routing based on non-exceeded quotas.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:metadata:labels="gateway.networking.k8s.io/policy=direct"
type QuotaPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              QuotaPolicySpec `json:"spec,omitempty"`
	// Status defines the status details of the QuotaPolicy.
	Status QuotaPolicyStatus `json:"status,omitempty"`
}

// QuotaPolicySpec specifies rules for computing token based costs of requests.
type QuotaPolicySpec struct {
	// TargetRefs are the names of the AIServiceBackend resources this QuotaPolicy is being attached to.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(ref, ref.group == 'aigateway.envoyproxy.io' && ref.kind == 'AIServiceBackend')", message="targetRefs must reference AIServiceBackend resources"
	TargetRefs []gwapiv1a2.LocalPolicyTargetReference `json:"targetRefs,omitempty"`
	// Quota for all models served by AIServiceBackend(s). This value can be overridden for specific models using the "PerModelQuotas"
	// configuration.
	//
	// Currently, the rate limit configuration is properly set up, but the descriptor set is not being set in the Envoy Configuration
	// TODO: Add changes in the extension server to support ServiceQuota enforcement.
	//
	// +optional
	ServiceQuota ServiceQuotaDefinition `json:"serviceQuota,omitempty"`
	// PerModelQuotas specifies quota for different models served by the AIServiceBackend(s) where this
	// policy is attached.
	//
	// When multiple QuotaPolicies define the same Model for the same AIServiceBackend,
	// the policy whose namespace/name is alphabetically first takes precedence.
	// Keys are sorted to ensure deterministic snapshot generation.
	//
	// +kubebuilder:validation:MaxItems=128
	// +optional
	PerModelQuotas []PerModelQuota `json:"perModelQuotas,omitempty"`
}

type ServiceQuotaDefinition struct {
	// CostExpression specifies a CEL expression for computing the quota burndown of the LLM-related request.
	// If no expression is specified the "total_tokens" value is used.
	// For example:
	//
	//  "input_tokens + cached_input_tokens + output_tokens"
	//
	// +optional
	CostExpression *string `json:"costExpression,omitempty"`
	// Quota value applicable to all requests.
	// A response with 429 HTTP status code is sent back to the client when
	// the selected requests have exceeded the quota.
	Quota QuotaValue `json:"quota"`
}

type PerModelQuota struct {
	// Model name for which the quota is specified.
	// This must match the ModelNameOverride set in the AIGatewayRoute's BackendRef
	// for the quota policy to apply to that backend.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ModelName *string `json:"modelName"`

	// Expression for computing request cost and rules for matching requests to quota buckets.
	//
	// +kubebuilder:validation:Required
	Quota QuotaDefinition `json:"quota"`
}

// QuotaDefinition specified expression for computing request cost and rules for matching requests to quota buckets.
type QuotaDefinition struct {
	// CostExpression specifies a CEL expression for computing the quota burndown of the LLM-related request.
	// If no expression is specified the "total_tokens" value is used.
	// For example:
	//
	//  "input_tokens + cached_input_tokens + output_tokens"
	//
	// +optional
	CostExpression *string `json:"costExpression,omitempty"`
	// The "Mode" determines how quota is charged to the "DefaultBucket" and matching "BucketRules".
	// In the "shared" mode the quota is charged to all matching "BucketRules" AND the "DefaultBucket"
	// and request is allowed only if the quota is available in at least one matching buckets.
	// Defaults to "Shared".
	//
	// +optional
	// +kubebuilder:default=Shared
	Mode QuotaBucketMode `json:"mode,omitempty"`
	// Quota applicable to all traffic. This value can be overridden for specific classes of requests
	// using the "BucketRules" configuration.
	//
	// +optional
	DefaultBucket QuotaValue `json:"defaultBucket"`
	// BucketRules are a list of client selectors and quotas. If a request
	// matches multiple rules, each of their associated quotas get applied, so a
	// single request might burn down the quota for multiple rules.
	//
	// Client selectors that match under the same model / service backend will be
	// combined with the first limit taking precedence.
	//
	// +optional
	BucketRules []QuotaRule `json:"bucketRules,omitempty"`
}

// QuotaBucketMode specifies whether the default and per request buckets values are exclusive or inclusive.
//
// TODO: Add Exclusive mode in the future.
// +kubebuilder:validation:Enum=Shared
type QuotaBucketMode string

const (
	QuotaBucketModeShared QuotaBucketMode = "Shared"
)

type QuotaRule struct {
	// ClientSelectors holds the list of conditions to select
	// specific clients using attributes from the traffic flow.
	// All individual select conditions must hold True for this rule
	// and its limit to be applied.
	// Currently, only header types are honored.
	//
	// If no client selectors are specified, the rule applies to all traffic of
	// the targeted AIServiceBackend.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=8
	ClientSelectors []egv1a1.RateLimitSelectCondition `json:"clientSelectors,omitempty"`
	// Quota value for given client selectors.
	// This quota is applied for traffic flows when the selectors
	// compute to True, causing the request to be counted towards the limit.
	// A response with 429 HTTP status code is sent back to the client when
	// the selected requests have exceeded the quota.
	Quota QuotaValue `json:"quota"`
	// ShadowMode indicates whether this quota rule runs in shadow mode.
	// When enabled, all quota checks are performed (cache lookups,
	// counter updates, telemetry generation), but the outcome is never enforced.
	// The request always succeeds, even if the configured quota is exceeded.
	//
	// +optional
	ShadowMode *bool `json:"shadowMode,omitempty"`
}

// QuotaValue defines the quota limits using sliding window.
type QuotaValue struct {
	// The limit alloted for a specified time window.
	Limit uint `json:"limit"`
	// Time window. Must be exactly one of: "1s" (1 second), "1m" (1 minute), "1h" (1 hour), "1d" (1 day), "1w" (1 week), "1mo" (1 month), or "1y" (1 year).
	//
	// +kubebuilder:validation:Enum="1s";"1m";"1h";"1d";"1w";"1mo";"1y"
	Duration string `json:"duration"`
}

// QuotaPolicyList contains a list of QuotaPolicy
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type QuotaPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaPolicy `json:"items"`
}
