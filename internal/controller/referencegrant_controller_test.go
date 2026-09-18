// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func TestReferenceGrantController_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	t.Run("ReferenceGrant created - triggers affected AIGatewayRoutes", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
			Name:      "test-grant",
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

		affectedRoute := &aigv1b1.AIGatewayRoute{
			Name:      "affected-route",
			Namespace: "route-ns",
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{
								Name:      "backend",
								Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
							},
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant, affectedRoute).
			Build()

		// Create a buffered channel to avoid blocking
		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		// Verify that an event was sent to the channel
		require.Len(t, aiGatewayRouteChan, 1)
		event := <-aiGatewayRouteChan
		require.Equal(t, affectedRoute.Name, event.Object.GetName())
		require.Equal(t, affectedRoute.Namespace, event.Object.GetNamespace())
	})

	t.Run("ReferenceGrant deleted - reconciles successfully", func(t *testing.T) {
		// When a ReferenceGrant is deleted, it doesn't exist in the cluster
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

		req := reconcile.Request{
			Namespace: "backend-ns",
			Name:      "deleted-grant",
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		// No events should be sent when grant is deleted
		require.Empty(t, aiGatewayRouteChan)
	})

	t.Run("ReferenceGrant with no affected routes", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
			Name:      "test-grant",
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

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		// No events should be sent when there are no affected routes
		require.Empty(t, aiGatewayRouteChan)
	})

	t.Run("ReferenceGrant with multiple affected routes", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
			Name:      "test-grant",
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

		route1 := &aigv1b1.AIGatewayRoute{
			Name:      "route-1",
			Namespace: "route-ns",
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{
								Name:      "backend-1",
								Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
							},
						},
					},
				},
			},
		}

		route2 := &aigv1b1.AIGatewayRoute{
			Name:      "route-2",
			Namespace: "route-ns",
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{
								Name:      "backend-2",
								Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
							},
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant, route1, route2).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		// Both routes should trigger events
		require.Len(t, aiGatewayRouteChan, 2)

		// Collect route names from events
		routeNames := make(map[string]bool)
		event1 := <-aiGatewayRouteChan
		routeNames[event1.Object.GetName()] = true
		event2 := <-aiGatewayRouteChan
		routeNames[event2.Object.GetName()] = true

		require.True(t, routeNames["route-1"])
		require.True(t, routeNames["route-2"])
	})
}

func TestNewReferenceGrantController(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	aiGatewayRouteChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()

	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

	require.NotNil(t, controller)
	require.Equal(t, fakeClient, controller.client)
	require.Equal(t, logger, controller.logger)
	require.Equal(t, aiGatewayRouteChan, controller.aiGatewayRouteChan)
}

// TestReferenceGrantController_Reconcile_GetError tests reconcile when Get returns error
func TestReferenceGrantController_Reconcile_GetError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	// Create a fake client that will return an error for Get operations
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	aiGatewayRouteChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()

	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

	// Try to reconcile a non-existent ReferenceGrant - this should be handled gracefully
	req := reconcile.Request{
		Namespace: "test-ns",
		Name:      "non-existent",
	}

	result, err := controller.Reconcile(context.Background(), req)
	require.NoError(t, err, "should ignore not found errors")
	require.Equal(t, reconcile.Result{}, result)
}

// TestReferenceGrantController_Reconcile_GetAffectedRoutesError tests when GetAffectedAIGatewayRoutes returns an error
func TestReferenceGrantController_Reconcile_GetAffectedRoutesError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)

	referenceGrant := &gwapiv1b1.ReferenceGrant{
		Name:      "test-grant",
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

	// Create fake client without AIGatewayRoute in scheme to cause List error
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(referenceGrant).
		Build()

	aiGatewayRouteChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()

	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

	req := reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(referenceGrant),
	}

	result, err := controller.Reconcile(context.Background(), req)
	require.Error(t, err, "should return error when GetAffectedAIGatewayRoutes fails")
	require.Equal(t, reconcile.Result{}, result)
}

func TestReferenceGrantController_GetAffectedAIGatewayRoutes(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	tests := []struct {
		name           string
		referenceGrant gwapiv1b1.ReferenceGrant
		routes         []aigv1b1.AIGatewayRoute
		expectedRoutes []string // route names that should be affected
	}{
		{
			name: "Grant with route referencing backend in grant namespace",
			referenceGrant: gwapiv1b1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-grant",
					Namespace: "backend-ns",
				},
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
			routes: []aigv1b1.AIGatewayRoute{
				{
					Name:      "affected-route",
					Namespace: "route-ns",
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{
								BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
									{
										Name:      "backend",
										Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
									},
								},
							},
						},
					},
				},
				{
					Name:      "unaffected-route",
					Namespace: "route-ns",
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{
								BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
									{
										Name: "local-backend",
										// No namespace specified, uses local namespace
									},
								},
							},
						},
					},
				},
			},
			expectedRoutes: []string{"affected-route"},
		},
		{
			name: "Grant with no matching routes",
			referenceGrant: gwapiv1b1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-grant",
					Namespace: "backend-ns",
				},
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
			routes: []aigv1b1.AIGatewayRoute{
				{
					Name:      "route-in-different-ns",
					Namespace: "other-ns",
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{
								BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
									{
										Name:      "backend",
										Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
									},
								},
							},
						},
					},
				},
			},
			expectedRoutes: []string{},
		},
		{
			name: "Grant for wrong kind",
			referenceGrant: gwapiv1b1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-grant",
					Namespace: "backend-ns",
				},
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
			routes: []aigv1b1.AIGatewayRoute{
				{
					Name:      "route",
					Namespace: "route-ns",
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{
								BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
									{
										Name:      "backend",
										Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
									},
								},
							},
						},
					},
				},
			},
			expectedRoutes: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with routes
			objs := make([]client.Object, len(tt.routes))
			for i := range tt.routes {
				objs[i] = &tt.routes[i]
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			aiGatewayRouteChan := make(chan event.GenericEvent, 10)
			logger := logr.Discard()
			controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

			affectedRoutes, err := controller.getAffectedAIGatewayRoutes(
				context.Background(),
				&tt.referenceGrant,
			)
			require.NoError(t, err)

			actualRouteNames := make([]string, len(affectedRoutes))
			for i, route := range affectedRoutes {
				actualRouteNames[i] = route.Name
			}

			require.ElementsMatch(t, tt.expectedRoutes, actualRouteNames)
		})
	}

	// Test case where List returns an error
	t.Run("List AIGatewayRoutes error", func(t *testing.T) {
		// Create a scheme without AIGatewayRoute to cause List error
		badScheme := runtime.NewScheme()
		_ = gwapiv1b1.Install(badScheme)
		fakeClient := fake.NewClientBuilder().
			WithScheme(badScheme).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()
		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

		grant := &gwapiv1b1.ReferenceGrant{
			Name:      "test-grant",
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

		routes, err := controller.getAffectedAIGatewayRoutes(context.Background(), grant)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to list AIGatewayRoutes")
		require.Nil(t, routes)
	})
}

// TestReferenceGrantController_GetAffectedAIGatewayRoutes_WithNonMatchingFrom tests getAffectedAIGatewayRoutes with non-matching From
func TestReferenceGrantController_GetAffectedAIGatewayRoutes_WithNonMatchingFrom(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	aiGatewayRouteChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()
	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan)

	grant := &gwapiv1b1.ReferenceGrant{
		Name:      "test-grant",
		Namespace: "backend-ns",
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{
				{
					Group:     "wrong.group", // Wrong group
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

	routes, err := controller.getAffectedAIGatewayRoutes(context.Background(), grant)
	require.NoError(t, err)
	require.Empty(t, routes, "should not return any routes when From doesn't match")
}
