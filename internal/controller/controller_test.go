// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func Test_aiGatewayRouteIndexFunc(t *testing.T) {
	c := requireNewFakeClientWithIndexes(t)

	// Create an AIGatewayRoute.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		Name:      "myroute",
		Namespace: "default",
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1a2.ParentReference{
				{Name: "mytarget", Kind: ptr.To(gwapiv1a2.Kind("Gateway"))},
				{Name: "mytarget2", Kind: ptr.To(gwapiv1a2.Kind("HTTPRoute"))},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					Matches: []aigv1b1.AIGatewayRouteRuleMatch{},
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend1", Weight: ptr.To[int32](1)},
						{Name: "backend2", Weight: ptr.To[int32](1)},
					},
				},
			},
		},
	}
	require.NoError(t, c.Create(t.Context(), aiGatewayRoute))

	var aiGatewayRoutes aigv1b1.AIGatewayRouteList
	err := c.List(t.Context(), &aiGatewayRoutes,
		client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: "backend1.default"})
	require.NoError(t, err)
	require.Len(t, aiGatewayRoutes.Items, 1)
	require.Equal(t, aiGatewayRoute.Name, aiGatewayRoutes.Items[0].Name)

	err = c.List(t.Context(), &aiGatewayRoutes,
		client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: "backend2.default"})
	require.NoError(t, err)
	require.Len(t, aiGatewayRoutes.Items, 1)
	require.Equal(t, aiGatewayRoute.Name, aiGatewayRoutes.Items[0].Name)
}

func Test_backendSecurityPolicyIndexFunc(t *testing.T) {
	for _, bsp := range []struct {
		name                  string
		backendSecurityPolicy *aigv1b1.BackendSecurityPolicy
		expKey                string
	}{
		{
			name: "api key with namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-1", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
					APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
						SecretRef: &gwapiv1.SecretObjectReference{
							Name:      "some-secret1",
							Namespace: ptr.To[gwapiv1.Namespace]("foo"),
						},
					},
				},
			},
			expKey: "some-secret1.foo",
		},
		{
			name: "api key without namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-2", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
					APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
						SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret2"},
					},
				},
			},
			expKey: "some-secret2.ns",
		},
		{
			name: "aws credentials with namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-3", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
					AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
						CredentialsFile: &aigv1b1.AWSCredentialsFile{
							SecretRef: &gwapiv1.SecretObjectReference{
								Name: "some-secret3", Namespace: ptr.To[gwapiv1.Namespace]("foo"),
							},
						},
					},
				},
			},
			expKey: "some-secret3.foo",
		},
		{
			name: "aws credentials without namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-4", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
					AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
						CredentialsFile: &aigv1b1.AWSCredentialsFile{
							SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret4"},
						},
					},
				},
			},
			expKey: "some-secret4.ns",
		},
		{
			name: "Azure api key with namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-5", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureAPIKey,
					AzureAPIKey: &aigv1b1.BackendSecurityPolicyAzureAPIKey{
						SecretRef: &gwapiv1.SecretObjectReference{
							Name:      "some-secret5",
							Namespace: ptr.To[gwapiv1.Namespace]("foo"),
						},
					},
				},
			},
			expKey: "some-secret5.foo",
		},
		{
			name: "Azure api key without namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-6", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureAPIKey,
					AzureAPIKey: &aigv1b1.BackendSecurityPolicyAzureAPIKey{
						SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret6"},
					},
				},
			},
			expKey: "some-secret6.ns",
		},
		{
			name: "Azure credentials with namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-7", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
					AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
						ClientSecretRef: &gwapiv1.SecretObjectReference{
							Name:      "some-secret7",
							Namespace: ptr.To[gwapiv1.Namespace]("foo"),
						},
					},
				},
			},
			expKey: "some-secret7.foo",
		},
		{
			name: "Azure credentials without namespace",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-8", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
					AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
						ClientSecretRef: &gwapiv1.SecretObjectReference{
							Name: "some-secret8",
						},
					},
				},
			},
			expKey: "some-secret8.ns",
		},
		{
			name: "AWS OIDC exchange token",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-9", Namespace: "foo",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
					AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
						OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{},
					},
				},
			},
			expKey: "some-backend-security-policy-9.foo",
		},
		{
			name: "Azure OIDC exchange token",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-10", Namespace: "foo",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
					AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
						OIDCExchangeToken: &aigv1b1.AzureOIDCExchangeToken{},
					},
				},
			},
			expKey: "some-backend-security-policy-10.foo",
		},
		{
			name: "anthropic api key",
			backendSecurityPolicy: &aigv1b1.BackendSecurityPolicy{
				Name: "some-backend-security-policy-2", Namespace: "ns",
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey,
					AnthropicAPIKey: &aigv1b1.BackendSecurityPolicyAnthropicAPIKey{
						SecretRef: &gwapiv1.SecretObjectReference{Name: "some-aaaa"},
					},
				},
			},
			expKey: "some-aaaa.ns",
		},
	} {
		t.Run(bsp.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(Scheme).
				WithIndex(&aigv1b1.BackendSecurityPolicy{}, k8sClientIndexSecretToReferencingBackendSecurityPolicy, backendSecurityPolicyIndexFunc).
				Build()

			require.NoError(t, c.Create(t.Context(), bsp.backendSecurityPolicy))

			var backendSecurityPolicies aigv1b1.BackendSecurityPolicyList
			err := c.List(t.Context(), &backendSecurityPolicies,
				client.MatchingFields{k8sClientIndexSecretToReferencingBackendSecurityPolicy: bsp.expKey})
			require.NoError(t, err)

			require.Len(t, backendSecurityPolicies.Items, 1)
			require.Equal(t, bsp.backendSecurityPolicy.Name, backendSecurityPolicies.Items[0].Name)
			require.Equal(t, bsp.backendSecurityPolicy.Namespace, backendSecurityPolicies.Items[0].Namespace)
		})
	}
}

func Test_getSecretNameAndNamespace(t *testing.T) {
	secretRef := &gwapiv1.SecretObjectReference{
		Name:      "mysecret",
		Namespace: ptr.To[gwapiv1.Namespace]("default"),
	}
	require.Equal(t, "mysecret.default", getSecretNameAndNamespace(secretRef, "foo"))
	secretRef.Namespace = nil
	require.Equal(t, "mysecret.foo", getSecretNameAndNamespace(secretRef, "foo"))
}

func Test_referenceGrantToTargetKindIndexFunc(t *testing.T) {
	tests := []struct {
		name           string
		referenceGrant *gwapiv1b1.ReferenceGrant
		expectedKeys   []string
	}{
		{
			name: "single target kind - AIServiceBackend",
			referenceGrant: &gwapiv1b1.ReferenceGrant{
				Name:      "grant1",
				Namespace: "backend-ns",
				Spec: gwapiv1b1.ReferenceGrantSpec{
					From: []gwapiv1b1.ReferenceGrantFrom{
						{
							Group:     "aigateway.envoyproxy.io",
							Kind:      "AIGatewayRoute",
							Namespace: "route-ns",
						},
					},
					To: []gwapiv1b1.ReferenceGrantTo{
						{
							Group: "aigateway.envoyproxy.io",
							Kind:  "AIServiceBackend",
						},
					},
				},
			},
			expectedKeys: []string{"backend-ns.AIServiceBackend"},
		},
		{
			name: "multiple target kinds",
			referenceGrant: &gwapiv1b1.ReferenceGrant{
				Name:      "grant2",
				Namespace: "backend-ns",
				Spec: gwapiv1b1.ReferenceGrantSpec{
					From: []gwapiv1b1.ReferenceGrantFrom{
						{
							Group:     "aigateway.envoyproxy.io",
							Kind:      "AIGatewayRoute",
							Namespace: "route-ns",
						},
					},
					To: []gwapiv1b1.ReferenceGrantTo{
						{
							Group: "aigateway.envoyproxy.io",
							Kind:  "AIServiceBackend",
						},
						{
							Group: "",
							Kind:  "Secret",
						},
					},
				},
			},
			expectedKeys: []string{"backend-ns.AIServiceBackend", "backend-ns.Secret"},
		},
		{
			name: "empty group for core resources",
			referenceGrant: &gwapiv1b1.ReferenceGrant{
				Name:      "grant3",
				Namespace: "other-ns",
				Spec: gwapiv1b1.ReferenceGrantSpec{
					From: []gwapiv1b1.ReferenceGrantFrom{
						{
							Group:     "gateway.networking.k8s.io",
							Kind:      "HTTPRoute",
							Namespace: "route-ns",
						},
					},
					To: []gwapiv1b1.ReferenceGrantTo{
						{
							Group: "",
							Kind:  "Service",
						},
					},
				},
			},
			expectedKeys: []string{"other-ns.Service"},
		},
		{
			name: "no target kinds",
			referenceGrant: &gwapiv1b1.ReferenceGrant{
				Name:      "grant4",
				Namespace: "backend-ns",
				Spec: gwapiv1b1.ReferenceGrantSpec{
					From: []gwapiv1b1.ReferenceGrantFrom{
						{
							Group:     "aigateway.envoyproxy.io",
							Kind:      "AIGatewayRoute",
							Namespace: "route-ns",
						},
					},
					To: []gwapiv1b1.ReferenceGrantTo{},
				},
			},
			expectedKeys: nil, // nil is returned for empty To array
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := referenceGrantToTargetKindIndexFunc(tt.referenceGrant)
			require.Equal(t, tt.expectedKeys, keys)
		})
	}
}

func Test_referenceGrantIndexWithQuery(t *testing.T) {
	// Create a fake client with the ReferenceGrant index
	c := fake.NewClientBuilder().
		WithScheme(Scheme).
		WithIndex(&gwapiv1b1.ReferenceGrant{}, k8sClientIndexReferenceGrantToTargetKind, referenceGrantToTargetKindIndexFunc).
		Build()

	// Create multiple ReferenceGrants with different target kinds
	grant1 := &gwapiv1b1.ReferenceGrant{
		Name:      "grant-aiservicebackend",
		Namespace: "backend-ns",
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{
				{
					Group:     "aigateway.envoyproxy.io",
					Kind:      "AIGatewayRoute",
					Namespace: "route-ns",
				},
			},
			To: []gwapiv1b1.ReferenceGrantTo{
				{
					Group: "aigateway.envoyproxy.io",
					Kind:  "AIServiceBackend",
				},
			},
		},
	}

	grant2 := &gwapiv1b1.ReferenceGrant{
		Name:      "grant-secret",
		Namespace: "backend-ns",
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{
				{
					Group:     "gateway.networking.k8s.io",
					Kind:      "HTTPRoute",
					Namespace: "route-ns",
				},
			},
			To: []gwapiv1b1.ReferenceGrantTo{
				{
					Group: "",
					Kind:  "Secret",
				},
			},
		},
	}

	grant3 := &gwapiv1b1.ReferenceGrant{
		Name:      "grant-multiple",
		Namespace: "backend-ns",
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{
				{
					Group:     "aigateway.envoyproxy.io",
					Kind:      "AIGatewayRoute",
					Namespace: "route-ns",
				},
			},
			To: []gwapiv1b1.ReferenceGrantTo{
				{
					Group: "aigateway.envoyproxy.io",
					Kind:  "AIServiceBackend",
				},
				{
					Group: "",
					Kind:  "Secret",
				},
			},
		},
	}

	require.NoError(t, c.Create(t.Context(), grant1))
	require.NoError(t, c.Create(t.Context(), grant2))
	require.NoError(t, c.Create(t.Context(), grant3))

	t.Run("query for AIServiceBackend grants in backend-ns", func(t *testing.T) {
		var grants gwapiv1b1.ReferenceGrantList
		err := c.List(t.Context(), &grants,
			client.MatchingFields{k8sClientIndexReferenceGrantToTargetKind: "backend-ns.AIServiceBackend"})
		require.NoError(t, err)

		// Should find grant1 and grant3 (both allow AIServiceBackend in backend-ns)
		require.Len(t, grants.Items, 2)
		names := []string{grants.Items[0].Name, grants.Items[1].Name}
		require.Contains(t, names, "grant-aiservicebackend")
		require.Contains(t, names, "grant-multiple")
	})

	t.Run("query for Secret grants in backend-ns", func(t *testing.T) {
		var grants gwapiv1b1.ReferenceGrantList
		err := c.List(t.Context(), &grants,
			client.MatchingFields{k8sClientIndexReferenceGrantToTargetKind: "backend-ns.Secret"})
		require.NoError(t, err)

		// Should find grant2 and grant3 (both allow Secret in backend-ns)
		require.Len(t, grants.Items, 2)
		names := []string{grants.Items[0].Name, grants.Items[1].Name}
		require.Contains(t, names, "grant-secret")
		require.Contains(t, names, "grant-multiple")
	})

	t.Run("query for non-existent kind", func(t *testing.T) {
		var grants gwapiv1b1.ReferenceGrantList
		err := c.List(t.Context(), &grants,
			client.MatchingFields{k8sClientIndexReferenceGrantToTargetKind: "backend-ns.NonExistentKind"})
		require.NoError(t, err)

		// Should find nothing
		require.Empty(t, grants.Items)
	})

	t.Run("query with wrong namespace", func(t *testing.T) {
		var grants gwapiv1b1.ReferenceGrantList
		err := c.List(t.Context(), &grants,
			client.MatchingFields{k8sClientIndexReferenceGrantToTargetKind: "wrong-ns.AIServiceBackend"})
		require.NoError(t, err)

		// Should find nothing (wrong namespace)
		require.Empty(t, grants.Items)
	})
}

func Test_handleFinalizer(t *testing.T) {
	tests := []struct {
		name               string
		hasFinalizer       bool
		hasDeletionTS      bool
		clientUpdateError  bool
		onDeletionFnError  bool
		expectedOnDelete   bool
		expectedFinalizers []string
		expectCallback     bool
	}{
		{
			name:               "add finalizer to new object",
			hasFinalizer:       false,
			hasDeletionTS:      false,
			expectedOnDelete:   false,
			expectedFinalizers: []string{aiGatewayControllerFinalizer},
		},
		{
			name:               "add finalizer to new object with update error",
			hasFinalizer:       false,
			hasDeletionTS:      false,
			clientUpdateError:  true,
			expectedOnDelete:   false,
			expectedFinalizers: []string{aiGatewayControllerFinalizer},
		},
		{
			name:               "object already has finalizer",
			hasFinalizer:       true,
			hasDeletionTS:      false,
			expectedOnDelete:   false,
			expectedFinalizers: []string{aiGatewayControllerFinalizer},
		},
		{
			name:               "object being deleted, remove finalizer",
			hasFinalizer:       true,
			hasDeletionTS:      true,
			expectedOnDelete:   true,
			expectedFinalizers: []string{},
			expectCallback:     true,
		},
		{
			name:               "object being deleted, callback error",
			hasFinalizer:       true,
			hasDeletionTS:      true,
			onDeletionFnError:  true,
			expectedOnDelete:   true,
			expectedFinalizers: []string{},
			expectCallback:     true,
		},
		{
			name:               "object being deleted, client update error",
			hasFinalizer:       true,
			hasDeletionTS:      true,
			clientUpdateError:  true,
			expectedOnDelete:   true,
			expectedFinalizers: []string{},
			expectCallback:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &aigv1b1.AIGatewayRoute{
				Name: "test-object", Namespace: "test-namespace",
			}

			if tc.hasFinalizer {
				obj.Finalizers = []string{aiGatewayControllerFinalizer}
			}

			if tc.hasDeletionTS {
				obj.DeletionTimestamp = new(metav1.Now())
			}

			callbackExecuted := false
			var onDeletionFn func(context.Context, *aigv1b1.AIGatewayRoute) error
			if tc.expectCallback {
				onDeletionFn = func(context.Context, *aigv1b1.AIGatewayRoute) error {
					callbackExecuted = true
					if tc.onDeletionFnError {
						return fmt.Errorf("mock deletion error")
					}
					return nil
				}
			}
			onDelete := handleFinalizer(context.Background(),
				&mockClient{updateErr: tc.clientUpdateError}, logr.Discard(), obj, onDeletionFn)
			require.Equal(t, tc.expectedOnDelete, onDelete)
			require.Equal(t, tc.expectedFinalizers, obj.Finalizers)
			require.Equal(t, tc.expectCallback, callbackExecuted)
		})
	}
}

// mockClients implements client.Client with a custom Update method for testing purposes.
type mockClient struct {
	client.Client
	updateErr bool
}

// Updates implements the client.Client interface for the mock client.
func (m *mockClient) Update(context.Context, client.Object, ...client.UpdateOption) error {
	if m.updateErr {
		return fmt.Errorf("mock update error")
	}
	return nil
}

func Test_aiGatewayRouteToAttachedGatewayIndexFunc(t *testing.T) {
	tests := []struct {
		name            string
		route           *aigv1b1.AIGatewayRoute
		expectedIndexes []string
	}{
		{
			name: "parentRef cross-namespace reference",
			route: &aigv1b1.AIGatewayRoute{
				Name:      "ai-route",
				Namespace: "envoy-ai-gateway-system",
				Spec: aigv1b1.AIGatewayRouteSpec{
					ParentRefs: []gwapiv1a2.ParentReference{
						{
							Name:      "my-gateway",
							Namespace: ptr.To[gwapiv1a2.Namespace]("envoy-gateway-system"),
							Kind:      ptr.To(gwapiv1a2.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{"my-gateway.envoy-gateway-system"},
		},
		{
			name: "parentRef same namespace as route",
			route: &aigv1b1.AIGatewayRoute{
				Name:      "ai-route",
				Namespace: "production",
				Spec: aigv1b1.AIGatewayRouteSpec{
					ParentRefs: []gwapiv1a2.ParentReference{
						{
							Name: "my-gateway",
							Kind: ptr.To(gwapiv1a2.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{"my-gateway.production"},
		},
		{
			name: "multiple parentRefs with mixed namespaces",
			route: &aigv1b1.AIGatewayRoute{
				Name:      "ai-route",
				Namespace: "app-namespace",
				Spec: aigv1b1.AIGatewayRouteSpec{
					ParentRefs: []gwapiv1a2.ParentReference{
						{
							Name:      "gateway-1",
							Namespace: ptr.To[gwapiv1a2.Namespace]("infra-namespace"),
							Kind:      ptr.To(gwapiv1a2.Kind("Gateway")),
						},
						{
							Name: "gateway-2",
							Kind: ptr.To(gwapiv1a2.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{
				"gateway-1.infra-namespace",
				"gateway-2.app-namespace",
			},
		},
		{
			name: "targetRefs always use route namespace",
			route: &aigv1b1.AIGatewayRoute{
				Name:      "ai-route",
				Namespace: "default",
				Spec: aigv1b1.AIGatewayRouteSpec{
					ParentRefs: []gwapiv1a2.ParentReference{
						{
							Name:      "parent-gateway",
							Namespace: ptr.To[gwapiv1a2.Namespace]("system"),
							Kind:      ptr.To(gwapiv1a2.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{"parent-gateway.system"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexes := aiGatewayRouteToAttachedGatewayIndexFunc(tt.route)
			require.ElementsMatch(t, tt.expectedIndexes, indexes)
		})
	}
}

func Test_mcpRouteToAttachedGatewayIndexFunc(t *testing.T) {
	tests := []struct {
		name            string
		route           *aigv1b1.MCPRoute
		expectedIndexes []string
	}{
		{
			name: "parentRef cross-namespace reference",
			route: &aigv1b1.MCPRoute{
				Name:      "mcp-route",
				Namespace: "envoy-mcp-gateway-system",
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{
							Name:      "my-gateway",
							Namespace: ptr.To(gwapiv1.Namespace("envoy-gateway-system")),
							Kind:      ptr.To(gwapiv1.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{"my-gateway.envoy-gateway-system"},
		},
		{
			name: "parentRef same namespace as route",
			route: &aigv1b1.MCPRoute{
				Name:      "mcp-route",
				Namespace: "production",
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{
							Name: "my-gateway",
							Kind: ptr.To(gwapiv1.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{"my-gateway.production"},
		},
		{
			name: "multiple parentRefs with mixed namespaces",
			route: &aigv1b1.MCPRoute{
				Name:      "mcp-route",
				Namespace: "app-namespace",
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{
							Name:      "gateway-1",
							Namespace: ptr.To(gwapiv1.Namespace("infra-namespace")),
							Kind:      ptr.To(gwapiv1.Kind("Gateway")),
						},
						{
							Name: "gateway-2",
							Kind: ptr.To(gwapiv1.Kind("Gateway")),
						},
					},
				},
			},
			expectedIndexes: []string{
				"gateway-1.infra-namespace",
				"gateway-2.app-namespace",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexes := mcpRouteToAttachedGatewayIndexFunc(tt.route)
			require.ElementsMatch(t, tt.expectedIndexes, indexes)
		})
	}
}

func Test_isKubernetes133OrLater(t *testing.T) {
	require.False(t, isKubernetes133OrLater(&version.Info{}, logr.Discard()))
	require.False(t, isKubernetes133OrLater(&version.Info{Major: "invalid"}, logr.Discard()))
	require.False(t, isKubernetes133OrLater(&version.Info{Major: "1", Minor: "invalid"}, logr.Discard()))
	require.False(t, isKubernetes133OrLater(&version.Info{Major: "1", Minor: "1"}, logr.Discard()))
	require.False(t, isKubernetes133OrLater(&version.Info{Major: "1", Minor: "32"}, logr.Discard()))
	require.True(t, isKubernetes133OrLater(&version.Info{Major: "1", Minor: "33"}, logr.Discard()))
	require.True(t, isKubernetes133OrLater(&version.Info{Major: "1", Minor: "40"}, logr.Discard()))
}
