// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// GuardrailPolicy evaluates content safety checks for request and response payloads.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:metadata:labels="gateway.networking.k8s.io/policy=direct"
// +kubebuilder:deprecatedversion:warning="aigateway.envoyproxy.io/v1alpha1 is deprecated; use aigateway.envoyproxy.io/v1beta1 instead"
type GuardrailPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GuardrailPolicySpec `json:"spec,omitempty"`
	// Status defines the status details of the GuardrailPolicy.
	Status GuardrailPolicyStatus `json:"status,omitempty"`
}

// GuardrailPolicySpec contains the configured checks attached to an AIServiceBackend.
type GuardrailPolicySpec struct {
	// TargetRefs identify the AIServiceBackend resources this GuardrailPolicy is attached to.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(ref, ref.group == 'aigateway.envoyproxy.io' && ref.kind == 'AIServiceBackend')", message="targetRefs must reference AIServiceBackend resources"
	TargetRefs []gwapiv1a2.LocalPolicyTargetReference `json:"targetRefs,omitempty"`
	// Rules are executed in order and can evaluate request or response payloads.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:XValidation:rule="self.all(rule, self.exists_one(other, other.name == rule.name))",message="rule name must be unique within the policy"
	Rules []GuardrailRule `json:"rules,omitempty"`
	// MaxRequestBodyBytes is the largest request body evaluated by this policy.
	// +optional
	// +kubebuilder:default=10485760
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=52428800
	MaxRequestBodyBytes *int64 `json:"maxRequestBodyBytes,omitempty"`
	// MaxResponseBodyBytes is the largest response body buffered and evaluated by this policy.
	// +optional
	// +kubebuilder:default=10485760
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=52428800
	MaxResponseBodyBytes *int64 `json:"maxResponseBodyBytes,omitempty"`
}

// GuardrailRule defines one content-safety check to apply to a request or response.
type GuardrailRule struct {
	// Name is a stable identifier for the rule.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Phase determines whether the rule runs against the request or the response payload.
	//
	// +kubebuilder:validation:Enum=Request;Response
	Phase GuardrailPhase `json:"phase"`
	// Provider configures how the rule is evaluated.
	Provider GuardrailProvider `json:"provider"`
}

// GuardrailPhase determines when a guardrail runs.
type GuardrailPhase string

const (
	GuardrailPhaseRequest  GuardrailPhase = "Request"
	GuardrailPhaseResponse GuardrailPhase = "Response"
)

// GuardrailProvider describes the implementation used to evaluate a rule.
// +kubebuilder:validation:XValidation:rule="self.type != 'Regex' || (has(self.pattern) && self.pattern != ” && !has(self.presidio) && !has(self.bedrock) && !has(self.azureContentSafety))",message="Regex requires pattern and no external provider configuration"
// +kubebuilder:validation:XValidation:rule="self.type != 'Presidio' || (!has(self.pattern) && has(self.presidio) && !has(self.bedrock) && !has(self.azureContentSafety))",message="Presidio requires only presidio provider configuration"
// +kubebuilder:validation:XValidation:rule="self.type != 'Bedrock' || (!has(self.pattern) && has(self.bedrock) && !has(self.presidio) && !has(self.azureContentSafety))",message="Bedrock requires only bedrock provider configuration"
// +kubebuilder:validation:XValidation:rule="self.type != 'AzureContentSafety' || (!has(self.pattern) && has(self.azureContentSafety) && !has(self.presidio) && !has(self.bedrock))",message="AzureContentSafety requires only azureContentSafety provider configuration"
// +kubebuilder:validation:XValidation:rule="self.action != 'Mask' || self.type != 'AzureContentSafety'",message="AzureContentSafety does not support Mask"
type GuardrailProvider struct {
	// Type identifies the guardrail implementation.
	//
	// +kubebuilder:validation:Enum=Regex;Presidio;Bedrock;AzureContentSafety
	Type GuardrailProviderType `json:"type"`
	// Pattern is used for deterministic regex-based evaluations.
	//
	// +optional
	Pattern string `json:"pattern,omitempty"`
	// Action is the action taken when the rule is matched.
	//
	// +optional
	// +kubebuilder:default=Block
	// +kubebuilder:validation:Enum=Block;Monitor;Mask
	Action GuardrailAction `json:"action,omitempty"`
	// MaskReplacement is used by Regex and Presidio Mask actions.
	// +optional
	// +kubebuilder:default="[REDACTED]"
	MaskReplacement string `json:"maskReplacement,omitempty"`
	// Message is returned to the caller when the rule blocks a request or response.
	//
	// +optional
	Message string `json:"message,omitempty"`
	// Presidio configures the Presidio analyzer provider.
	//
	// +optional
	Presidio *PresidioGuardrailProvider `json:"presidio,omitempty"`
	// Bedrock configures AWS Bedrock Guardrails.
	//
	// +optional
	Bedrock *BedrockGuardrailProvider `json:"bedrock,omitempty"`
	// AzureContentSafety configures Azure AI Content Safety.
	//
	// +optional
	AzureContentSafety *AzureContentSafetyGuardrailProvider `json:"azureContentSafety,omitempty"`
	// TimeoutSeconds limits each external provider evaluation.
	// +optional
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
	// FailureMode determines whether provider errors block or allow the request.
	// +optional
	// +kubebuilder:default=FailClosed
	// +kubebuilder:validation:Enum=FailClosed;FailOpen
	FailureMode GuardrailFailureMode `json:"failureMode,omitempty"`
}

// PresidioGuardrailProvider configures calls to a Presidio analyzer service.
type PresidioGuardrailProvider struct {
	// +kubebuilder:validation:Format=uri
	Endpoint string `json:"endpoint"`
	// +optional
	// +kubebuilder:default=en
	Language string `json:"language,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ScoreThresholdPercent *int32 `json:"scoreThresholdPercent,omitempty"`
	// APIKeySecretRef optionally references a Secret whose apiKey entry is sent as a Bearer token.
	// +optional
	APIKeySecretRef *gwapiv1.SecretObjectReference `json:"apiKeySecretRef,omitempty"`
}

// BedrockGuardrailProvider configures calls to the AWS Bedrock ApplyGuardrail API.
type BedrockGuardrailProvider struct {
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`
	// +kubebuilder:validation:MinLength=1
	GuardrailIdentifier string `json:"guardrailIdentifier"`
	// +kubebuilder:validation:MinLength=1
	GuardrailVersion string `json:"guardrailVersion"`
	// Endpoint overrides the Bedrock runtime endpoint, primarily for private endpoints and testing.
	// +optional
	// +kubebuilder:validation:Format=uri
	Endpoint string `json:"endpoint,omitempty"`
	// CredentialsSecretRef optionally references a Secret whose credentials entry contains an AWS shared credentials file.
	// +optional
	CredentialsSecretRef *gwapiv1.SecretObjectReference `json:"credentialsSecretRef,omitempty"`
}

// AzureContentSafetyGuardrailProvider configures calls to Azure AI Content Safety.
type AzureContentSafetyGuardrailProvider struct {
	// +kubebuilder:validation:Format=uri
	Endpoint string `json:"endpoint"`
	// +optional
	// +kubebuilder:default="2024-09-01"
	APIVersion string `json:"apiVersion,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=7
	SeverityThreshold *int32 `json:"severityThreshold,omitempty"`
	// APIKeySecretRef references a Secret whose apiKey entry is sent to Azure.
	APIKeySecretRef *gwapiv1.SecretObjectReference `json:"apiKeySecretRef"`
}

// GuardrailProviderType is the guardrail implementation.
type GuardrailProviderType string

const (
	GuardrailProviderTypeRegex              GuardrailProviderType = "Regex"
	GuardrailProviderTypePresidio           GuardrailProviderType = "Presidio"
	GuardrailProviderTypeBedrockGuardrails  GuardrailProviderType = "Bedrock"
	GuardrailProviderTypeAzureContentSafety GuardrailProviderType = "AzureContentSafety"
)

// GuardrailAction defines the safeguard action.
type GuardrailAction string

const (
	GuardrailActionBlock   GuardrailAction = "Block"
	GuardrailActionMonitor GuardrailAction = "Monitor"
	GuardrailActionMask    GuardrailAction = "Mask"
)

// GuardrailFailureMode defines behavior when an external provider cannot evaluate content.
type GuardrailFailureMode string

const (
	GuardrailFailureModeFailClosed GuardrailFailureMode = "FailClosed"
	GuardrailFailureModeFailOpen   GuardrailFailureMode = "FailOpen"
)

// GuardrailPolicyList contains a list of GuardrailPolicy resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type GuardrailPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GuardrailPolicy `json:"items"`
}
