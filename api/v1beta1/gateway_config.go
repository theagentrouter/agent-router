// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1beta1

import (
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayConfig provides configuration for the AI Gateway external processor
// container that is deployed alongside the Gateway.
//
// A GatewayConfig is referenced by a Gateway via the annotation
// "aigateway.envoyproxy.io/gateway-config". The GatewayConfig must be in the
// same namespace as the Gateway that references it.
//
// This allows gateway-level configuration of the external processor, including
// environment variables (e.g., for tracing configuration) and resource requirements.
//
// Multiple Gateways can reference the same GatewayConfig to share configuration.
//
// Environment Variable Precedence:
// When merging environment variables, the following precedence applies (highest to lowest):
//  1. GatewayConfig.Spec.ExtProc.Kubernetes.Env (this resource)
//  2. Global controller flags (extProcExtraEnvVars)
//
// If the same environment variable name exists in both sources, the GatewayConfig
// value takes precedence.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gwconfig
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:storageversion
type GatewayConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the configuration for the external processor.
	Spec GatewayConfigSpec `json:"spec,omitempty"`
	// Status defines the status of the GatewayConfig.
	Status GatewayConfigStatus `json:"status,omitempty"`
}

// GatewayConfigList contains a list of GatewayConfig.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type GatewayConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GatewayConfig `json:"items"`
}

// GatewayConfigSpec defines the configuration for the AI Gateway.
type GatewayConfigSpec struct {
	// ExtProc defines the configuration for the external processor container.
	//
	// +optional
	ExtProc *GatewayConfigExtProc `json:"extProc,omitempty"`

	// GlobalLLMRequestCosts defines default LLM request costs that apply to all
	// routes referencing this GatewayConfig. These costs can be overridden on a
	// per-route basis via AIGatewayRoute.Spec.LLMRequestCosts.
	//
	// When a request matches a route, the cost calculation proceeds as follows:
	//  1. If the route defines LLMRequestCosts with a matching metadataKey, use that.
	//  2. Otherwise, fall back to the global cost with that metadataKey (if defined here).
	//  3. If neither exists, the cost is not calculated for that metadataKey.
	//
	// This allows you to define common cost formulas once at the gateway level
	// (e.g., billing_charges = input_tokens + output_tokens) and only override
	// them in specific routes when needed (e.g., premium routes with different pricing).
	//
	// +optional
	// +listType=map
	// +listMapKey=metadataKey
	GlobalLLMRequestCosts []LLMRequestCost `json:"globalLLMRequestCosts,omitempty"`

	// ForwardProxy routes all upstream AI/LLM traffic from Gateways referencing this
	// GatewayConfig through an HTTP CONNECT forward proxy. This is intended for data planes
	// in locked-down networks where direct egress to providers is not permitted and all
	// outbound traffic must traverse a proxy.
	//
	// +optional
	ForwardProxy *GatewayConfigForwardProxy `json:"forwardProxy,omitempty"`
}

// GatewayConfigExtProc holds configuration for the external processor.
type GatewayConfigExtProc struct {
	// Kubernetes defines the configuration for running the external processor as a Kubernetes container.
	//
	// +optional
	Kubernetes *egv1a1.KubernetesContainerSpec `json:"kubernetes,omitempty"`

	// MetadataForwardingNamespaces lists the untyped dynamic metadata namespaces Envoy forwards
	// to the external processor for this Gateway's AI traffic. A BackendSecurityPolicy using
	// credentialOverride.fromDynamicMetadata receives metadata only from a namespace listed here.
	//
	// Under Envoy Gateway's mergeGateways, every Gateway in the class shares one Envoy, so a
	// namespace any one of them declares is forwarded for all of their AI traffic.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self.all(n, n.matches('^[A-Za-z0-9._/-]+$'))",message="metadata namespaces may only contain letters, digits, '.', '_', '/' and '-'"
	MetadataForwardingNamespaces []string `json:"metadataForwardingNamespaces,omitempty"`
}

// GatewayConfigForwardProxy configures an HTTP CONNECT forward proxy for upstream egress.
type GatewayConfigForwardProxy struct {
	// Address is the "host:port" of the HTTP CONNECT proxy. The host may be a hostname or an
	// IP address; the port is required. Upstream connections are tunnelled through this proxy
	// via Envoy's http_11_proxy transport socket, preserving the upstream TLS session.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`

	// TLS enables TLS on the connection to the proxy itself (an HTTPS proxy). When unset, the
	// connection to the proxy is plaintext. The upstream TLS session, if any, still runs
	// end-to-end inside the tunnel.
	//
	// When TLS or Auth is set, a CONNECT request is always sent to the proxy, also for
	// plaintext upstreams.
	//
	// +optional
	TLS *GatewayConfigForwardProxyTLS `json:"tls,omitempty"`

	// Auth configures the credentials sent to the proxy in the Proxy-Authorization header of
	// the CONNECT request. When unset, no Proxy-Authorization header is sent.
	//
	// +optional
	Auth *GatewayConfigForwardProxyAuth `json:"auth,omitempty"`
}

// GatewayConfigForwardProxyTLS configures TLS for the connection to the forward proxy.
type GatewayConfigForwardProxyTLS struct {
	// SNI is the server name sent to the proxy and verified against its certificate. Defaults
	// to the host of the proxy address when it is a hostname. When the address host is an IP
	// address and SNI is unset, no SNI is sent.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	SNI *string `json:"sni,omitempty"`

	// CACertificateRef references a Secret in the same namespace as the GatewayConfig whose
	// "ca.crt" key holds the PEM-encoded CA bundle used to verify the proxy certificate.
	// When unset, the system trust store of the Envoy proxy is used.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.kind) || self.kind == 'Secret'",message="only Secret references are supported"
	// +kubebuilder:validation:XValidation:rule="!has(self.group) || self.group == ''",message="only core Secret references are supported"
	CACertificateRef *gwapiv1.SecretObjectReference `json:"caCertificateRef,omitempty"`

	// ClientCertificateRef references a kubernetes.io/tls Secret in the same namespace as the
	// GatewayConfig ("tls.crt" and "tls.key" keys) presented to the proxy as a client
	// certificate (mutual TLS).
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.kind) || self.kind == 'Secret'",message="only Secret references are supported"
	// +kubebuilder:validation:XValidation:rule="!has(self.group) || self.group == ''",message="only core Secret references are supported"
	ClientCertificateRef *gwapiv1.SecretObjectReference `json:"clientCertificateRef,omitempty"`
}

// GatewayConfigForwardProxyAuth configures the Proxy-Authorization credentials sent to the
// forward proxy. Exactly one of Basic or HeaderValueRef must be set.
//
// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.headerValueRef)",message="exactly one of basic or headerValueRef must be set"
type GatewayConfigForwardProxyAuth struct {
	// Basic configures username and password credentials, sent as
	// "Proxy-Authorization: Basic base64(username:password)" (RFC 7617).
	//
	// +optional
	Basic *GatewayConfigForwardProxyBasicAuth `json:"basic,omitempty"`

	// HeaderValueRef references a Secret in the same namespace as the GatewayConfig whose
	// "value" key is sent verbatim as the Proxy-Authorization header value, for example
	// "Bearer <token>". Use this for authentication schemes other than Basic.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.kind) || self.kind == 'Secret'",message="only Secret references are supported"
	// +kubebuilder:validation:XValidation:rule="!has(self.group) || self.group == ''",message="only core Secret references are supported"
	HeaderValueRef *gwapiv1.SecretObjectReference `json:"headerValueRef,omitempty"`
}

// GatewayConfigForwardProxyBasicAuth configures Basic credentials for the forward proxy.
type GatewayConfigForwardProxyBasicAuth struct {
	// SecretRef references a Secret in the same namespace as the GatewayConfig with the
	// "username" and "password" keys. The username must not contain a colon.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="!has(self.kind) || self.kind == 'Secret'",message="only Secret references are supported"
	// +kubebuilder:validation:XValidation:rule="!has(self.group) || self.group == ''",message="only core Secret references are supported"
	SecretRef gwapiv1.SecretObjectReference `json:"secretRef"`
}

// GatewayConfigStatus defines the observed state of GatewayConfig.
type GatewayConfigStatus struct {
	// Conditions describe the current conditions of the GatewayConfig.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
