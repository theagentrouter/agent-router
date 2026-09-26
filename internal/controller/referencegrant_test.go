// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
)

func TestReferenceGrantValidator_ValidateAIServiceBackendReference(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	tests := []struct {
		name                string
		routeNamespace      string
		backendNamespace    string
		backendName         string
		referenceGrants     []gwapiv1b1.ReferenceGrant
		expectedError       bool
		expectedErrorString string
	}{
		{
			name:             "Same namespace reference - should succeed",
			routeNamespace:   "default",
			backendNamespace: "default",
			backendName:      "test-backend",
			referenceGrants:  []gwapiv1b1.ReferenceGrant{},
			expectedError:    false,
		},
		{
			name:             "Cross-namespace with valid ReferenceGrant - should succeed",
			routeNamespace:   "route-ns",
			backendNamespace: "backend-ns",
			backendName:      "test-backend",
			referenceGrants: []gwapiv1b1.ReferenceGrant{
				{
					Name:      "allow-from-route-ns",
					Namespace: "backend-ns",
					Spec: gwapiv1b1.ReferenceGrantSpec{
						From: []gwapiv1b1.ReferenceGrantFrom{
							{
								Group:     aiServiceBackendGroup,
								Kind:      aiGatewayRouteKind,
								Namespace: "route-ns",
							},
						},
						To: []gwapiv1b1.ReferenceGrantTo{
							{
								Group: aiServiceBackendGroup,
								Kind:  aiServiceBackendKind,
							},
						},
					},
				},
			},
			expectedError: false,
		},
		{
			name:             "Cross-namespace without ReferenceGrant - should fail",
			routeNamespace:   "route-ns",
			backendNamespace: "backend-ns",
			backendName:      "test-backend",
			referenceGrants:  []gwapiv1b1.ReferenceGrant{},
			expectedError:    true,
			expectedErrorString: "cross-namespace reference from AIGatewayRoute in namespace route-ns " +
				"to AIServiceBackend test-backend in namespace backend-ns is not permitted",
		},
		{
			name:             "Cross-namespace with ReferenceGrant for wrong namespace - should fail",
			routeNamespace:   "route-ns",
			backendNamespace: "backend-ns",
			backendName:      "test-backend",
			referenceGrants: []gwapiv1b1.ReferenceGrant{
				{
					Name:      "allow-from-other-ns",
					Namespace: "backend-ns",
					Spec: gwapiv1b1.ReferenceGrantSpec{
						From: []gwapiv1b1.ReferenceGrantFrom{
							{
								Group:     aiServiceBackendGroup,
								Kind:      aiGatewayRouteKind,
								Namespace: "other-ns", // Wrong namespace
							},
						},
						To: []gwapiv1b1.ReferenceGrantTo{
							{
								Group: aiServiceBackendGroup,
								Kind:  aiServiceBackendKind,
							},
						},
					},
				},
			},
			expectedError:       true,
			expectedErrorString: "is not permitted",
		},
		{
			name:             "Cross-namespace with ReferenceGrant for wrong kind - should fail",
			routeNamespace:   "route-ns",
			backendNamespace: "backend-ns",
			backendName:      "test-backend",
			referenceGrants: []gwapiv1b1.ReferenceGrant{
				{
					Name:      "allow-wrong-kind",
					Namespace: "backend-ns",
					Spec: gwapiv1b1.ReferenceGrantSpec{
						From: []gwapiv1b1.ReferenceGrantFrom{
							{
								Group:     aiServiceBackendGroup,
								Kind:      "WrongKind", // Wrong kind
								Namespace: "route-ns",
							},
						},
						To: []gwapiv1b1.ReferenceGrantTo{
							{
								Group: aiServiceBackendGroup,
								Kind:  aiServiceBackendKind,
							},
						},
					},
				},
			},
			expectedError:       true,
			expectedErrorString: "is not permitted",
		},
		{
			name:             "Cross-namespace with ReferenceGrant allowing wrong target - should fail",
			routeNamespace:   "route-ns",
			backendNamespace: "backend-ns",
			backendName:      "test-backend",
			referenceGrants: []gwapiv1b1.ReferenceGrant{
				{
					Name:      "allow-wrong-target",
					Namespace: "backend-ns",
					Spec: gwapiv1b1.ReferenceGrantSpec{
						From: []gwapiv1b1.ReferenceGrantFrom{
							{
								Group:     aiServiceBackendGroup,
								Kind:      aiGatewayRouteKind,
								Namespace: "route-ns",
							},
						},
						To: []gwapiv1b1.ReferenceGrantTo{
							{
								Group: aiServiceBackendGroup,
								Kind:  "WrongTargetKind", // Wrong target kind
							},
						},
					},
				},
			},
			expectedError:       true,
			expectedErrorString: "is not permitted",
		},
		{
			name:             "Cross-namespace with multiple ReferenceGrants, one valid - should succeed",
			routeNamespace:   "route-ns",
			backendNamespace: "backend-ns",
			backendName:      "test-backend",
			referenceGrants: []gwapiv1b1.ReferenceGrant{
				{
					Name:      "invalid-grant",
					Namespace: "backend-ns",
					Spec: gwapiv1b1.ReferenceGrantSpec{
						From: []gwapiv1b1.ReferenceGrantFrom{
							{
								Group:     aiServiceBackendGroup,
								Kind:      "WrongKind",
								Namespace: "route-ns",
							},
						},
						To: []gwapiv1b1.ReferenceGrantTo{
							{
								Group: aiServiceBackendGroup,
								Kind:  aiServiceBackendKind,
							},
						},
					},
				},
				{
					Name:      "valid-grant",
					Namespace: "backend-ns",
					Spec: gwapiv1b1.ReferenceGrantSpec{
						From: []gwapiv1b1.ReferenceGrantFrom{
							{
								Group:     aiServiceBackendGroup,
								Kind:      aiGatewayRouteKind,
								Namespace: "route-ns",
							},
						},
						To: []gwapiv1b1.ReferenceGrantTo{
							{
								Group: aiServiceBackendGroup,
								Kind:  aiServiceBackendKind,
							},
						},
					},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with ReferenceGrants and the index
			objs := make([]client.Object, len(tt.referenceGrants))
			for i := range tt.referenceGrants {
				objs[i] = &tt.referenceGrants[i]
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithIndex(&gwapiv1b1.ReferenceGrant{}, k8sClientIndexReferenceGrantToTargetKind, referenceGrantToTargetKindIndexFunc).
				Build()

			validator := newReferenceGrantValidator(fakeClient)
			err := validator.validateAIServiceBackendReference(
				context.Background(),
				tt.routeNamespace,
				tt.backendNamespace,
				tt.backendName,
			)

			if tt.expectedError {
				require.Error(t, err)
				if tt.expectedErrorString != "" {
					require.Contains(t, err.Error(), tt.expectedErrorString)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}

	// Test case where List returns an error
	t.Run("List ReferenceGrants error", func(t *testing.T) {
		// Create a scheme without ReferenceGrant to cause List error
		badScheme := runtime.NewScheme()
		fakeClient := fake.NewClientBuilder().
			WithScheme(badScheme).
			Build()

		validator := newReferenceGrantValidator(fakeClient)
		err := validator.validateAIServiceBackendReference(
			context.Background(),
			"route-ns",
			"backend-ns",
			"test-backend",
		)

		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to list ReferenceGrants")
	})
}

// TestReferenceGrantValidator_ValidateInferencePoolReference tests cross-namespace InferencePool
// references governed by ReferenceGrant, mirroring the AIServiceBackend behavior.
func TestReferenceGrantValidator_ValidateInferencePoolReference(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	inferencePoolGrant := func(fromNS string, toGroup gwapiv1b1.Group, toKind gwapiv1b1.Kind) gwapiv1b1.ReferenceGrant {
		return gwapiv1b1.ReferenceGrant{
			Name: "grant", Namespace: "pool-ns",
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{{
					Group:     aiServiceBackendGroup,
					Kind:      aiGatewayRouteKind,
					Namespace: gwapiv1b1.Namespace(fromNS),
				}},
				To: []gwapiv1b1.ReferenceGrantTo{{Group: toGroup, Kind: toKind}},
			},
		}
	}

	tests := []struct {
		name                string
		routeNamespace      string
		poolNamespace       string
		poolName            string
		referenceGrants     []gwapiv1b1.ReferenceGrant
		expectedError       bool
		expectedErrorString string
	}{
		{
			name:           "Same namespace reference - should succeed",
			routeNamespace: "default",
			poolNamespace:  "default",
			poolName:       "my-pool",
		},
		{
			name:            "Cross-namespace with valid ReferenceGrant - should succeed",
			routeNamespace:  "route-ns",
			poolNamespace:   "pool-ns",
			poolName:        "my-pool",
			referenceGrants: []gwapiv1b1.ReferenceGrant{inferencePoolGrant("route-ns", inferencePoolGroup, inferencePoolKind)},
		},
		{
			name:           "Cross-namespace without ReferenceGrant - should fail",
			routeNamespace: "route-ns",
			poolNamespace:  "pool-ns",
			poolName:       "my-pool",
			expectedError:  true,
			expectedErrorString: "cross-namespace reference from AIGatewayRoute in namespace route-ns " +
				"to InferencePool my-pool in namespace pool-ns is not permitted",
		},
		{
			name:            "Cross-namespace with ReferenceGrant for wrong target kind - should fail",
			routeNamespace:  "route-ns",
			poolNamespace:   "pool-ns",
			poolName:        "my-pool",
			referenceGrants: []gwapiv1b1.ReferenceGrant{inferencePoolGrant("route-ns", aiServiceBackendGroup, aiServiceBackendKind)},
			expectedError:   true,
			// Grant targets AIServiceBackend, so the InferencePool index lookup finds no matching grant.
			expectedErrorString: "is not permitted",
		},
		{
			name:                "Cross-namespace with ReferenceGrant for wrong from namespace - should fail",
			routeNamespace:      "route-ns",
			poolNamespace:       "pool-ns",
			poolName:            "my-pool",
			referenceGrants:     []gwapiv1b1.ReferenceGrant{inferencePoolGrant("other-ns", inferencePoolGroup, inferencePoolKind)},
			expectedError:       true,
			expectedErrorString: "is not permitted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, len(tt.referenceGrants))
			for i := range tt.referenceGrants {
				objs[i] = &tt.referenceGrants[i]
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithIndex(&gwapiv1b1.ReferenceGrant{}, k8sClientIndexReferenceGrantToTargetKind, referenceGrantToTargetKindIndexFunc).
				Build()

			validator := newReferenceGrantValidator(fakeClient)
			err := validator.validateInferencePoolReference(
				context.Background(),
				tt.routeNamespace,
				tt.poolNamespace,
				tt.poolName,
			)

			if tt.expectedError {
				require.Error(t, err)
				if tt.expectedErrorString != "" {
					require.Contains(t, err.Error(), tt.expectedErrorString)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestReferenceGrantValidator_MatchesFrom_WrongGroup tests matchesFrom with wrong group
func TestReferenceGrantValidator_MatchesFrom_WrongGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	validator := newReferenceGrantValidator(fakeClient)

	from := &gwapiv1b1.ReferenceGrantFrom{
		Group:     "wrong.group",
		Kind:      aiGatewayRouteKind,
		Namespace: "route-ns",
	}

	result := validator.matchesFrom(from, "route-ns")
	require.False(t, result, "should return false for wrong group")
}

// TestReferenceGrantValidator_MatchesFrom_WrongKind tests matchesFrom with wrong kind
func TestReferenceGrantValidator_MatchesFrom_WrongKind(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	validator := newReferenceGrantValidator(fakeClient)

	from := &gwapiv1b1.ReferenceGrantFrom{
		Group:     aiServiceBackendGroup,
		Kind:      "WrongKind",
		Namespace: "route-ns",
	}

	result := validator.matchesFrom(from, "route-ns")
	require.False(t, result, "should return false for wrong kind")
}

// TestReferenceGrantValidator_MatchesFrom_WrongNamespace tests matchesFrom with wrong namespace
func TestReferenceGrantValidator_MatchesFrom_WrongNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	validator := newReferenceGrantValidator(fakeClient)

	from := &gwapiv1b1.ReferenceGrantFrom{
		Group:     aiServiceBackendGroup,
		Kind:      aiGatewayRouteKind,
		Namespace: "wrong-ns",
	}

	result := validator.matchesFrom(from, "route-ns")
	require.False(t, result, "should return false for wrong namespace")
}

// TestReferenceGrantValidator_MatchesTo_WrongGroup tests matchesTo with wrong group
func TestReferenceGrantValidator_MatchesTo_WrongGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	validator := newReferenceGrantValidator(fakeClient)

	to := &gwapiv1b1.ReferenceGrantTo{
		Group: "wrong.group",
		Kind:  aiServiceBackendKind,
	}

	result := validator.matchesTo(to, aiServiceBackendGroup, aiServiceBackendKind)
	require.False(t, result, "should return false for wrong group")
}

// TestReferenceGrantValidator_MatchesTo_WrongKind tests matchesTo with wrong kind
func TestReferenceGrantValidator_MatchesTo_WrongKind(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	validator := newReferenceGrantValidator(fakeClient)

	to := &gwapiv1b1.ReferenceGrantTo{
		Group: aiServiceBackendGroup,
		Kind:  "WrongKind",
	}

	result := validator.matchesTo(to, aiServiceBackendGroup, aiServiceBackendKind)
	require.False(t, result, "should return false for wrong kind")
}

// TestReferenceGrantValidator_WithIndex tests that the validator uses the index for efficient lookups
func TestReferenceGrantValidator_WithIndex(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1a1.AddToScheme(scheme)

	// Create fake client with the ReferenceGrant index
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&gwapiv1b1.ReferenceGrant{}, k8sClientIndexReferenceGrantToTargetKind, referenceGrantToTargetKindIndexFunc).
		Build()

	// Create multiple ReferenceGrants with different target kinds
	grant1 := &gwapiv1b1.ReferenceGrant{
		Name:      "grant-aiservicebackend",
		Namespace: "backend-ns",
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{
				{
					Group:     aiServiceBackendGroup,
					Kind:      aiGatewayRouteKind,
					Namespace: "route-ns",
				},
			},
			To: []gwapiv1b1.ReferenceGrantTo{
				{
					Group: aiServiceBackendGroup,
					Kind:  aiServiceBackendKind,
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

	require.NoError(t, fakeClient.Create(context.Background(), grant1))
	require.NoError(t, fakeClient.Create(context.Background(), grant2))

	validator := newReferenceGrantValidator(fakeClient)

	t.Run("validates with index - AIServiceBackend allowed", func(t *testing.T) {
		// This should succeed because grant1 allows AIGatewayRoute from route-ns to AIServiceBackend
		err := validator.validateAIServiceBackendReference(
			context.Background(),
			"route-ns",
			"backend-ns",
			"test-backend",
		)
		require.NoError(t, err)
	})

	t.Run("validates with index - different namespace denied", func(t *testing.T) {
		// This should fail because no grant allows from "other-ns"
		err := validator.validateAIServiceBackendReference(
			context.Background(),
			"other-ns",
			"backend-ns",
			"test-backend",
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "is not permitted")
	})

	t.Run("index filters out irrelevant grants", func(t *testing.T) {
		// Verify that the index only returns AIServiceBackend grants, not Secret grants
		// We can verify this indirectly by checking that validation works correctly
		// even though grant2 (Secret) exists in the same namespace

		// Add another grant that allows AIGatewayRoute from other-ns but to Secret (not AIServiceBackend)
		grant3 := &gwapiv1b1.ReferenceGrant{
			Name:      "grant-other-ns-secret",
			Namespace: "backend-ns",
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{
					{
						Group:     aiServiceBackendGroup,
						Kind:      aiGatewayRouteKind,
						Namespace: "other-ns",
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
		require.NoError(t, fakeClient.Create(context.Background(), grant3))

		// This should still fail because grant3 doesn't allow AIServiceBackend
		err := validator.validateAIServiceBackendReference(
			context.Background(),
			"other-ns",
			"backend-ns",
			"test-backend",
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "is not permitted")
	})
}

// TestGetReferenceGrantIndexKey tests the index key generation
func TestGetReferenceGrantIndexKey(t *testing.T) {
	tests := []struct {
		name        string
		namespace   string
		kind        string
		expectedKey string
	}{
		{
			name:        "AIServiceBackend in backend-ns",
			namespace:   "backend-ns",
			kind:        "AIServiceBackend",
			expectedKey: "backend-ns.AIServiceBackend",
		},
		{
			name:        "Secret in default namespace",
			namespace:   "default",
			kind:        "Secret",
			expectedKey: "default.Secret",
		},
		{
			name:        "CustomResource in custom-ns",
			namespace:   "custom-ns",
			kind:        "CustomResource",
			expectedKey: "custom-ns.CustomResource",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := getReferenceGrantIndexKey(tt.namespace, tt.kind)
			require.Equal(t, tt.expectedKey, key)
		})
	}
}
