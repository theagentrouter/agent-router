// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

func init() {
	SchemeBuilder.Register(&AIGatewayRoute{}, &AIGatewayRouteList{})
	SchemeBuilder.Register(&AIServiceBackend{}, &AIServiceBackendList{})
	SchemeBuilder.Register(&BackendSecurityPolicy{}, &BackendSecurityPolicyList{})
	SchemeBuilder.Register(&MCPRoute{}, &MCPRouteList{})
	SchemeBuilder.Register(&GatewayConfig{}, &GatewayConfigList{})
	SchemeBuilder.Register(&QuotaPolicy{}, &QuotaPolicyList{})
	SchemeBuilder.Register(&GuardrailPolicy{}, &GuardrailPolicyList{})
}

const GroupName = "aigateway.envoyproxy.io"

var (
	// SchemeGroupVersion is group version used to register these objects.
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: SchemeGroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource takes an unqualified resource and returns a Group qualified GroupResource
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// AddKnownTypes adds the list of known types to the given scheme for code generation.
func AddKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&AIGatewayRoute{},
		&AIGatewayRouteList{},
		&AIServiceBackend{},
		&AIServiceBackendList{},
		&BackendSecurityPolicy{},
		&BackendSecurityPolicyList{},
		&MCPRoute{},
		&MCPRouteList{},
		&GatewayConfig{},
		&GatewayConfigList{},
		&QuotaPolicy{},
		&QuotaPolicyList{},
		&GuardrailPolicy{},
		&GuardrailPolicyList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
