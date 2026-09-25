// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1beta1

import (
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// BackendSecurityPolicyType specifies the type of auth mechanism used to access a backend.
type BackendSecurityPolicyType string

const (
	BackendSecurityPolicyTypeAPIKey           BackendSecurityPolicyType = "APIKey"
	BackendSecurityPolicyTypeAWSCredentials   BackendSecurityPolicyType = "AWSCredentials"
	BackendSecurityPolicyTypeAzureAPIKey      BackendSecurityPolicyType = "AzureAPIKey"
	BackendSecurityPolicyTypeAnthropicAPIKey  BackendSecurityPolicyType = "AnthropicAPIKey" // #nosec G101
	BackendSecurityPolicyTypeAzureCredentials BackendSecurityPolicyType = "AzureCredentials"
	BackendSecurityPolicyTypeGCPCredentials   BackendSecurityPolicyType = "GCPCredentials"
)

// BackendSecurityPolicy specifies configuration for authentication and authorization rules on the traffic
// exiting the gateway to the backend.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:metadata:labels="gateway.networking.k8s.io/policy=direct"
// +kubebuilder:storageversion
type BackendSecurityPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackendSecurityPolicySpec `json:"spec,omitempty"`
	// Status defines the status details of the BackendSecurityPolicy.
	Status BackendSecurityPolicyStatus `json:"status,omitempty"`
}

// BackendSecurityPolicySpec specifies authentication rules on access the provider from the Gateway.
// Only one mechanism to access a backend(s) can be specified.
//
// Only one type of BackendSecurityPolicy can be defined.
// +kubebuilder:validation:MaxProperties=4
// +kubebuilder:validation:XValidation:rule="self.type == 'APIKey' ? (has(self.apiKey) && !has(self.awsCredentials) && !has(self.azureAPIKey) && !has(self.azureCredentials) && !has(self.gcpCredentials) && !has(self.anthropicAPIKey)) : true",message="When type is APIKey, only apiKey field should be set"
// +kubebuilder:validation:XValidation:rule="self.type == 'AWSCredentials' ? (has(self.awsCredentials) && !has(self.apiKey) && !has(self.azureAPIKey) && !has(self.azureCredentials) && !has(self.gcpCredentials) && !has(self.anthropicAPIKey)) : true",message="When type is AWSCredentials, only awsCredentials field should be set"
// +kubebuilder:validation:XValidation:rule="self.type == 'AzureAPIKey' ? (has(self.azureAPIKey) && !has(self.apiKey) && !has(self.awsCredentials) && !has(self.azureCredentials) && !has(self.gcpCredentials) && !has(self.anthropicAPIKey)) : true",message="When type is AzureAPIKey, only azureAPIKey field should be set"
// +kubebuilder:validation:XValidation:rule="self.type == 'AzureCredentials' ? (has(self.azureCredentials) && !has(self.apiKey) && !has(self.awsCredentials) && !has(self.azureAPIKey) && !has(self.gcpCredentials) && !has(self.anthropicAPIKey)) : true",message="When type is AzureCredentials, only azureCredentials field should be set"
// +kubebuilder:validation:XValidation:rule="self.type == 'GCPCredentials' ? (has(self.gcpCredentials) && !has(self.apiKey) && !has(self.awsCredentials) && !has(self.azureAPIKey) && !has(self.azureCredentials) && !has(self.anthropicAPIKey)) : true",message="When type is GCPCredentials, only gcpCredentials field should be set"
// +kubebuilder:validation:XValidation:rule="self.type == 'AnthropicAPIKey' ? (has(self.anthropicAPIKey) && !has(self.apiKey) && !has(self.awsCredentials) && !has(self.azureAPIKey) && !has(self.azureCredentials) && !has(self.gcpCredentials)) : true",message="When type is AnthropicAPIKey, only anthropicAPIKey field should be set"
type BackendSecurityPolicySpec struct {
	// TargetRefs are the names of the AIServiceBackend or InferencePool resources this BackendSecurityPolicy is being attached to.
	// Attaching multiple BackendSecurityPolicies to the same resource is invalid and will result in an error
	// during the reconciliation of the resource.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(ref, (ref.group == 'aigateway.envoyproxy.io' && ref.kind == 'AIServiceBackend') || (ref.group == 'inference.networking.k8s.io' && ref.kind == 'InferencePool'))", message="targetRefs must reference AIServiceBackend or InferencePool resources"
	TargetRefs []gwapiv1a2.LocalPolicyTargetReference `json:"targetRefs,omitempty"`

	// Type specifies the type of the backend security policy.
	//
	// +kubebuilder:validation:Enum=APIKey;AWSCredentials;AzureAPIKey;AzureCredentials;GCPCredentials;AnthropicAPIKey
	Type BackendSecurityPolicyType `json:"type"`

	// APIKey is a mechanism to access a backend(s). The API key will be injected into the Authorization header.
	//
	// +optional
	APIKey *BackendSecurityPolicyAPIKey `json:"apiKey,omitempty"`

	// AWSCredentials is a mechanism to access a backend(s). AWS specific logic will be applied.
	//
	// +optional
	AWSCredentials *BackendSecurityPolicyAWSCredentials `json:"awsCredentials,omitempty"`

	// AzureAPIKey is a mechanism to access Azure OpenAI backend(s). The API key will be injected into the api-key header.
	//
	// +optional
	AzureAPIKey *BackendSecurityPolicyAzureAPIKey `json:"azureAPIKey,omitempty"`

	// AzureCredentials is a mechanism to access a backend(s). Azure OpenAI specific logic will be applied.
	//
	// +optional
	AzureCredentials *BackendSecurityPolicyAzureCredentials `json:"azureCredentials,omitempty"`

	// GCPCredentials is a mechanism to access a backend(s). GCP specific logic will be applied.
	//
	// +optional
	GCPCredentials *BackendSecurityPolicyGCPCredentials `json:"gcpCredentials,omitempty"`

	// AnthropicAPIKey is a mechanism to access Anthropic backend(s). The API key will be injected into the "x-api-key" header.
	// https://docs.claude.com/en/api/overview#authentication
	//
	// +optional
	AnthropicAPIKey *BackendSecurityPolicyAnthropicAPIKey `json:"anthropicAPIKey,omitempty"`

	// CredentialOverride, when set, sources the upstream credential per-request instead of using
	// the static credential configured above.
	//
	// For AWSCredentials the credential is the three-part SigV4 input rather than a single string;
	// see the source types for how each carries it.
	//
	// +optional
	CredentialOverride *BackendSecurityPolicyCredentialOverride `json:"credentialOverride,omitempty"`
}

// BackendSecurityPolicyList contains a list of BackendSecurityPolicy
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type BackendSecurityPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackendSecurityPolicy `json:"items"`
}

// BackendSecurityPolicyAPIKey specifies the API key.
type BackendSecurityPolicyAPIKey struct {
	// SecretRef is the reference to the secret containing the API key.
	// ai-gateway must be given the permission to read this secret.
	// The key of the secret should be "apiKey".
	SecretRef *gwapiv1.SecretObjectReference `json:"secretRef"`
}

// BackendSecurityPolicyAzureAPIKey specifies the Azure OpenAI API key.
type BackendSecurityPolicyAzureAPIKey struct {
	// SecretRef is the reference to the secret containing the Azure API key.
	// ai-gateway must be given the permission to read this secret.
	// The key of the secret should be "apiKey".
	SecretRef *gwapiv1.SecretObjectReference `json:"secretRef"`
}

// BackendSecurityPolicyOIDC specifies OIDC related fields.
type BackendSecurityPolicyOIDC struct {
	// OIDC is used to obtain oidc tokens via an SSO server which will be used to exchange for provider credentials.
	//
	// +kubebuilder:validation:Required
	OIDC egv1a1.OIDC `json:"oidc"`

	// GrantType is the method application gets access token.
	//
	// +optional
	GrantType string `json:"grantType,omitempty"`

	// Aud defines the audience that this ID Token is intended for.
	//
	// +optional
	Aud string `json:"aud,omitempty"`
}

type GCPWorkloadIdentityFederationConfig struct {
	// ProjectID is the GCP project ID.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ProjectID string `json:"projectID"`

	// WorkloadIdentityProviderName is the name of the external identity provider as registered on Google Cloud Platform.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	WorkloadIdentityProviderName string `json:"workloadIdentityProviderName"`

	// OIDCExchangeToken specifies the oidc configurations used to obtain an oidc token. The oidc token will be
	// used to obtain temporary credentials to access GCP.
	//
	// +kubebuilder:validation:Required
	OIDCExchangeToken GCPOIDCExchangeToken `json:"oidcExchangeToken"`

	// WorkloadIdentityPoolName is the name of the workload identity pool defined in GCP.
	// https://cloud.google.com/iam/docs/workload-identity-federation?hl=en
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	WorkloadIdentityPoolName string `json:"workloadIdentityPoolName"`

	// ServiceAccountImpersonation is the service account impersonation configuration.
	// This is used to impersonate a service account when getting access token.
	//
	// +optional
	ServiceAccountImpersonation *GCPServiceAccountImpersonationConfig `json:"serviceAccountImpersonation,omitempty"`
}

type GCPOIDCExchangeToken struct {
	// BackendSecurityPolicyOIDC is the generic OIDC fields.
	BackendSecurityPolicyOIDC `json:",inline"`
}

// GCPWorkloadIdentityProvider specifies the external identity provider to be used to authenticate against GCP.
// The external identity provider can be AWS, Microsoft, etc but must be pre-registered in the GCP project
//
// https://cloud.google.com/iam/docs/workload-identity-federation
type GCPWorkloadIdentityProvider struct {
	// Name of the external identity provider as registered on Google Cloud Platform.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// OIDCProvider is the generic OIDCProvider fields.
	//
	// +kubebuilder:validation:Required
	OIDCProvider BackendSecurityPolicyOIDC `json:"OIDCProvider"`
}

type GCPServiceAccountImpersonationConfig struct {
	// ServiceAccountName is the name of the service account to impersonate.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ServiceAccountName string `json:"serviceAccountName"`
}

// BackendSecurityPolicyGCPCredentials contains the supported authentication mechanisms to access GCP.
//
// When neither CredentialsFile nor WorkloadIdentityFederationConfig is specified, Application Default
// Credentials (ADC) will be used. This supports GKE Workload Identity, environment variables, and
// default service account credentials when running on GCP.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.credentialsFile) && has(self.workloadIdentityFederationConfig))",message="At most one of credentialsFile or workloadIdentityFederationConfig may be specified"
type BackendSecurityPolicyGCPCredentials struct {
	// ProjectName is the GCP project name.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ProjectName string `json:"projectName"`
	// Region is the GCP region associated with the policy.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// CredentialsFile specifies the service account credentials file to use for the GCP provider.
	//
	// +optional
	CredentialsFile *GCPCredentialsFile `json:"credentialsFile,omitempty"`

	// WorkloadIdentityFederationConfig is the configuration for the GCP Workload Identity Federation.
	//
	// +optional
	WorkloadIdentityFederationConfig *GCPWorkloadIdentityFederationConfig `json:"workloadIdentityFederationConfig,omitempty"`
}

// BackendSecurityPolicyAzureCredentials contains the supported authentication mechanisms to access Azure.
// Only one of ClientSecretRef or OIDCExchangeToken must be specified. Credentials will not be generated if
// neither are set.
//
// +kubebuilder:validation:XValidation:rule="(has(self.clientSecretRef) && !has(self.oidcExchangeToken)) || (!has(self.clientSecretRef) && has(self.oidcExchangeToken))",message="Exactly one of clientSecretRef or oidcExchangeToken must be specified"
type BackendSecurityPolicyAzureCredentials struct {
	// ClientID is a unique identifier for an application in Azure.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ClientID string `json:"clientID"`

	// TenantId is a unique identifier for an Azure Active Directory instance.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	TenantID string `json:"tenantID"`

	// ClientSecretRef is the reference to the secret containing the Azure client secret.
	// ai-gateway must be given the permission to read this secret.
	// The key of secret should be "client-secret".
	//
	// +optional
	ClientSecretRef *gwapiv1.SecretObjectReference `json:"clientSecretRef,omitempty"`

	// OIDCExchangeToken specifies the oidc configurations used to obtain an oidc token. The oidc token will be
	// used to obtain temporary credentials to access Azure.
	//
	// +optional
	OIDCExchangeToken *AzureOIDCExchangeToken `json:"oidcExchangeToken,omitempty"`
}

// AzureOIDCExchangeToken specifies credentials to obtain oidc token from a sso server.
// For Azure, the controller will query Azure Entra ID to get an Azure Access Token,
// and store them in a secret.
type AzureOIDCExchangeToken struct {
	// BackendSecurityPolicyOIDC is the generic OIDC fields.
	BackendSecurityPolicyOIDC `json:",inline"`
}

// BackendSecurityPolicyAWSCredentials contains the supported authentication mechanisms to access AWS.
//
// When neither CredentialsFile nor OIDCExchangeToken is specified, the AWS SDK's default credential chain
// will be used. This automatically supports various authentication methods in the following order:
//  1. Environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN)
//  2. EKS Pod Identity - automatically rotates credentials for pods in EKS clusters
//  3. IAM Roles for Service Accounts (IRSA) - injects credentials via mounted service account tokens
//  4. EC2 instance metadata (IAM instance roles)
//  5. ECS task roles
//
// The default credential chain is recommended for Kubernetes deployments as it supports automatic
// credential rotation without manual configuration. Credentials are refreshed automatically when
// they approach expiration (typically hourly for IRSA and Pod Identity).
type BackendSecurityPolicyAWSCredentials struct {
	// Region specifies the AWS region associated with the policy.
	//
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// CredentialsFile specifies the credentials file to use for the AWS provider.
	// When specified, this takes precedence over the default credential chain.
	//
	// +optional
	CredentialsFile *AWSCredentialsFile `json:"credentialsFile,omitempty"`

	// OIDCExchangeToken specifies the oidc configurations used to obtain an oidc token. The oidc token will be
	// used to obtain temporary credentials to access AWS.
	// When specified, this takes precedence over the default credential chain.
	//
	// +optional
	OIDCExchangeToken *AWSOIDCExchangeToken `json:"oidcExchangeToken,omitempty"`
}

// AWSCredentialsFile specifies the credentials file to use for the AWS provider.
// Envoy reads the secret file, and the profile to use is specified by the Profile field.
type AWSCredentialsFile struct {
	// SecretRef is the reference to the credential file.
	//
	// The secret should contain the AWS credentials file keyed on "credentials".
	SecretRef *gwapiv1.SecretObjectReference `json:"secretRef"`

	// Profile is the profile to use in the credentials file.
	//
	// +kubebuilder:default=default
	Profile string `json:"profile,omitempty"`
}

// AWSOIDCExchangeToken specifies credentials to obtain oidc token from a sso server.
// For AWS, the controller will query STS to obtain AWS AccessKeyId, SecretAccessKey, and SessionToken,
// and store them in a temporary credentials file.
type AWSOIDCExchangeToken struct {
	// BackendSecurityPolicyOIDC is the generic OIDC fields.
	BackendSecurityPolicyOIDC `json:",inline"`

	// AwsRoleArn is the AWS IAM Role with the permission to use specific resources in AWS account
	// which maps to the temporary AWS security credentials exchanged using the authentication token issued by OIDC provider.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	AwsRoleArn string `json:"awsRoleArn"`
}

// GCPCredentialsFile specifies the service account key json file to authenticate with GCP provider.
type GCPCredentialsFile struct {
	// SecretRef is the reference to the credential file.
	//
	// The secret should contain the GCP service account credentials file keyed on "service_account.json".
	SecretRef *gwapiv1.SecretObjectReference `json:"secretRef"`
}

// BackendSecurityPolicyAnthropicAPIKey specifies the Anthropic API key.
type BackendSecurityPolicyAnthropicAPIKey struct {
	// SecretRef is the reference to the secret containing the Anthropic API key.
	// ai-gateway must be given the permission to read this secret.
	// The key of the secret should be "apiKey".
	SecretRef *gwapiv1.SecretObjectReference `json:"secretRef"`
}

// BackendSecurityPolicyCredentialOverride configures per-request credential sourcing.
// A trusted upstream filter (ext_authz or a preceding ext_proc) resolves the caller's
// credential and delivers it via one of the two sources below.
// Exactly one source must be configured.
//
// +kubebuilder:validation:MinProperties=1
// +kubebuilder:validation:MaxProperties=1
type BackendSecurityPolicyCredentialOverride struct {
	// FromRequestHeaders sources the credential from a request header injected by a trusted
	// ingress filter. The gateway strips the header before forwarding to the upstream backend.
	// Because any client can set request headers, this source is only safe when a trusted filter
	// removes any client-supplied copy and writes the canonical value.
	//
	// +optional
	FromRequestHeaders *CredentialOverrideFromRequestHeaders `json:"fromRequestHeaders,omitempty"`

	// FromDynamicMetadata sources the credential from Envoy dynamic metadata set by a trusted
	// filter (e.g. ext_authz). Metadata cannot be forged by the client and is the preferred source.
	//
	// +optional
	FromDynamicMetadata *CredentialOverrideFromDynamicMetadata `json:"fromDynamicMetadata,omitempty"`
}

// CredentialOverrideFromRequestHeaders sources the per-request credential from a request header.
type CredentialOverrideFromRequestHeaders struct {
	// Header is the name of the request header that carries the credential.
	// Defaults to the x-aigw-* header for the configured auth type:
	//   APIKey          → x-aigw-api-key
	//   AnthropicAPIKey → x-aigw-anthropic-api-key
	//   AzureAPIKey     → x-aigw-azure-api-key
	//   AzureCredentials → x-aigw-azure-access-token
	//   GCPCredentials  → x-aigw-gcp-access-token
	//
	// For AWSCredentials this is a prefix, not a single header name, since SigV4 needs three
	// inputs. Defaults to "x-aigw-aws-", yielding:
	//   x-aigw-aws-access-key-id
	//   x-aigw-aws-secret-access-key
	//   x-aigw-aws-session-token     (optional; omit for long-lived credentials)
	// All three are stripped before the backend sees them. An access key ID without a secret
	// access key, or the reverse, fails the request instead of falling back.
	//
	// Prefer FromDynamicMetadata for AWSCredentials. Headers are client-settable, and a secret
	// access key on the wire is a worse leak than an API key.
	//
	// +optional
	Header string `json:"header,omitempty"`

	// FallbackToConfigured controls behaviour when the header is absent.
	// true (default) falls back to the static credential configured in the policy.
	// false returns a 401 to the client without forwarding the request.
	//
	// +kubebuilder:default=true
	// +optional
	FallbackToConfigured *bool `json:"fallbackToConfigured,omitempty"`
}

// CredentialOverrideFromDynamicMetadata sources the per-request credential from Envoy
// dynamic metadata produced by a trusted filter running earlier in the filter chain.
type CredentialOverrideFromDynamicMetadata struct {
	// Namespace is the Envoy metadata namespace written by the trusted filter.
	// Example: "envoy.filters.http.ext_authz"
	//
	// List the namespace in the Gateway's GatewayConfig under
	// extProc.metadataForwardingNamespaces. Envoy forwards only the namespaces listed there,
	// and only untyped metadata is read.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Key is the metadata key within the namespace. Defaults to the x-aigw-* name for the auth type.
	//
	// For every type except AWSCredentials the value must be a string holding the credential.
	// For AWSCredentials it must be a struct, since SigV4 needs three inputs:
	//   accessKeyId
	//   secretAccessKey
	//   sessionToken     (optional; omit for long-lived credentials)
	// Field names are fixed. accessKeyId without secretAccessKey, or the reverse, fails the
	// request instead of falling back. Defaults to "x-aigw-aws-credentials".
	//
	// +optional
	Key string `json:"key,omitempty"`

	// FallbackToConfigured controls behaviour when the metadata key is absent.
	// true (default) falls back to the static credential configured in the policy.
	// false returns a 401 to the client without forwarding the request.
	//
	// +kubebuilder:default=true
	// +optional
	FallbackToConfigured *bool `json:"fallbackToConfigured,omitempty"`
}
