// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func TestReferenceGrantController_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	t.Run("ReferenceGrant created - triggers affected AIGatewayRoutes", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
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
		}

		affectedRoute := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "affected-route",
				Namespace: "route-ns",
			},
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
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

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
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: "backend-ns",
				Name:      "deleted-grant",
			},
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		// No events should be sent when grant is deleted
		require.Empty(t, aiGatewayRouteChan)
	})

	t.Run("ReferenceGrant being deleted - triggers affected AIGatewayRoutes before removal", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
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
		}

		affectedRoute := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "affected-route",
				Namespace: "route-ns",
			},
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

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		// First reconcile adds the finalizer and fires the "created" event.
		_, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Len(t, aiGatewayRouteChan, 1)
		<-aiGatewayRouteChan

		var grant gwapiv1b1.ReferenceGrant
		require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(referenceGrant), &grant))
		require.Contains(t, grant.Finalizers, aiGatewayControllerFinalizer)

		// Deleting the grant should not remove it right away since the finalizer is present.
		require.NoError(t, fakeClient.Delete(context.Background(), &grant))

		// Second reconcile should still see the grant (deletion in progress), fire the event for the
		// route it used to authorize, and then remove the finalizer so the grant is actually deleted.
		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		require.Len(t, aiGatewayRouteChan, 1)
		got := <-aiGatewayRouteChan
		require.Equal(t, affectedRoute.Name, got.Object.GetName())

		err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(referenceGrant), &grant)
		require.True(t, apierrors.IsNotFound(err))
	})

	t.Run("ReferenceGrant with no affected routes", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
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
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

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
		}

		route1 := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route-1",
				Namespace: "route-ns",
			},
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
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route-2",
				Namespace: "route-ns",
			},
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
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

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
	backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
	quotaPolicyChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()

	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, quotaPolicyChan)

	require.NotNil(t, controller)
	require.Equal(t, fakeClient, controller.client)
	require.Equal(t, logger, controller.logger)
	require.Equal(t, aiGatewayRouteChan, controller.aiGatewayRouteChan)
	require.Equal(t, backendSecurityPolicyChan, controller.backendSecurityPolicyChan)
	require.Equal(t, quotaPolicyChan, controller.quotaPolicyChan)
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
	backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()

	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

	// Try to reconcile a non-existent ReferenceGrant - this should be handled gracefully
	req := reconcile.Request{
		NamespacedName: client.ObjectKey{
			Namespace: "test-ns",
			Name:      "non-existent",
		},
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
	}

	// Create fake client without AIGatewayRoute in scheme to cause List error
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(referenceGrant).
		Build()

	aiGatewayRouteChan := make(chan event.GenericEvent, 10)
	backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()

	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

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
					ObjectMeta: metav1.ObjectMeta{
						Name:      "affected-route",
						Namespace: "route-ns",
					},
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
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unaffected-route",
						Namespace: "route-ns",
					},
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
			name: "Route outside the grant's from namespaces is still affected",
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
					ObjectMeta: metav1.ObjectMeta{
						Name:      "route-in-different-ns",
						Namespace: "other-ns",
					},
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
			// The route may have lost access when the grant was narrowed, so it must be reconciled.
			expectedRoutes: []string{"route-in-different-ns"},
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
					ObjectMeta: metav1.ObjectMeta{
						Name:      "route",
						Namespace: "route-ns",
					},
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
			expectedRoutes: []string{"route"},
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
			backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
			logger := logr.Discard()
			controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

			affectedRoutes, err := controller.getAffectedAIGatewayRoutes(
				context.Background(),
				tt.referenceGrant.Namespace,
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
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()
		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

		routes, err := controller.getAffectedAIGatewayRoutes(context.Background(), "backend-ns")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to list AIGatewayRoutes")
		require.Nil(t, routes)
	})
}

func TestReferenceGrantController_Reconcile_BackendSecurityPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	t.Run("ReferenceGrant created - triggers affected BackendSecurityPolicy", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-grant",
				Namespace: "secret-ns",
			},
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{
					{
						Group:     aiServiceBackendGroup,
						Kind:      backendSecurityPolicyKind,
						Namespace: "bsp-ns",
					},
				},
				To: []gwapiv1b1.ReferenceGrantTo{
					{
						Group: secretGroup,
						Kind:  secretKind,
					},
				},
			},
		}

		affectedBSP := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "affected-bsp",
				Namespace: "bsp-ns",
			},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name:      "api-key-secret",
						Namespace: ptr.To(gwapiv1.Namespace("secret-ns")),
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant, affectedBSP).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		require.Empty(t, aiGatewayRouteChan)
		require.Len(t, backendSecurityPolicyChan, 1)
		got := <-backendSecurityPolicyChan
		require.Equal(t, affectedBSP.Name, got.Object.GetName())
		require.Equal(t, affectedBSP.Namespace, got.Object.GetNamespace())
	})

	t.Run("ReferenceGrant unrelated to any BackendSecurityPolicy triggers no event", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-grant",
				Namespace: "secret-ns",
			},
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{
					{
						Group:     aiServiceBackendGroup,
						Kind:      backendSecurityPolicyKind,
						Namespace: "bsp-ns",
					},
				},
				To: []gwapiv1b1.ReferenceGrantTo{
					{
						Group: secretGroup,
						Kind:  secretKind,
					},
				},
			},
		}

		// BSP's secret is in a different namespace than the grant, so it is unaffected.
		unaffectedBSP := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "unaffected-bsp",
				Namespace: "bsp-ns",
			},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name:      "api-key-secret",
						Namespace: ptr.To(gwapiv1.Namespace("other-secret-ns")),
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant, unaffectedBSP).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		require.Empty(t, backendSecurityPolicyChan)
	})

	t.Run("ReferenceGrant being deleted - triggers affected BackendSecurityPolicies before removal", func(t *testing.T) {
		referenceGrant := &gwapiv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-grant",
				Namespace: "secret-ns",
			},
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{
					{
						Group:     aiServiceBackendGroup,
						Kind:      backendSecurityPolicyKind,
						Namespace: "bsp-ns",
					},
				},
				To: []gwapiv1b1.ReferenceGrantTo{
					{
						Group: secretGroup,
						Kind:  secretKind,
					},
				},
			},
		}

		affectedBSP := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "affected-bsp",
				Namespace: "bsp-ns",
			},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name:      "api-key-secret",
						Namespace: ptr.To(gwapiv1.Namespace("secret-ns")),
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(referenceGrant, affectedBSP).
			Build()

		aiGatewayRouteChan := make(chan event.GenericEvent, 10)
		backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
		logger := logr.Discard()

		controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(referenceGrant),
		}

		// First reconcile adds the finalizer and fires the "created" event.
		_, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Len(t, backendSecurityPolicyChan, 1)
		<-backendSecurityPolicyChan

		var grant gwapiv1b1.ReferenceGrant
		require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(referenceGrant), &grant))
		require.Contains(t, grant.Finalizers, aiGatewayControllerFinalizer)

		// Deleting the grant should not remove it right away since the finalizer is present.
		require.NoError(t, fakeClient.Delete(context.Background(), &grant))

		// Second reconcile should still see the grant (deletion in progress), fire the event for the
		// BackendSecurityPolicy it used to authorize, and then remove the finalizer so the grant is
		// actually deleted.
		result, err := controller.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, result)

		require.Len(t, backendSecurityPolicyChan, 1)
		got := <-backendSecurityPolicyChan
		require.Equal(t, affectedBSP.Name, got.Object.GetName())

		err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(referenceGrant), &grant)
		require.True(t, apierrors.IsNotFound(err))
	})
}

func TestReferenceGrantController_GetAffectedBackendSecurityPolicies(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	grant := gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grant",
			Namespace: "secret-ns",
		},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{
				{
					Group:     aiServiceBackendGroup,
					Kind:      backendSecurityPolicyKind,
					Namespace: "bsp-ns",
				},
			},
			To: []gwapiv1b1.ReferenceGrantTo{
				{
					Group: secretGroup,
					Kind:  secretKind,
				},
			},
		},
	}

	affectedBSP := aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "affected-bsp", Namespace: "bsp-ns"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{
					Name:      "api-key-secret",
					Namespace: ptr.To(gwapiv1.Namespace("secret-ns")),
				},
			},
		},
	}
	sameNamespaceBSP := aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "same-ns-bsp", Namespace: "bsp-ns"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{Name: "local-secret"},
			},
		},
	}
	otherNamespaceBSP := aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ns-bsp", Namespace: "other-bsp-ns"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{
					Name:      "api-key-secret",
					Namespace: ptr.To(gwapiv1.Namespace("secret-ns")),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&affectedBSP, &sameNamespaceBSP, &otherNamespaceBSP).
		Build()

	aiGatewayRouteChan := make(chan event.GenericEvent, 10)
	backendSecurityPolicyChan := make(chan event.GenericEvent, 10)
	logger := logr.Discard()
	controller := NewReferenceGrantController(fakeClient, logger, aiGatewayRouteChan, backendSecurityPolicyChan, nil)

	affected, err := controller.getAffectedBackendSecurityPolicies(context.Background(), grant.Namespace)
	require.NoError(t, err)

	names := make([]string, len(affected))
	for i, bsp := range affected {
		names[i] = bsp.Name
	}
	// other-ns-bsp is not in the grant's from namespaces, but it references a Secret in the grant's
	// namespace and may have just lost access, so it must be reconciled too.
	require.ElementsMatch(t, []string{"affected-bsp", "other-ns-bsp"}, names)
}

// TestReferenceGrantController_RevokedAccessIsReconciled verifies that the resources that lose access
// when a ReferenceGrant is narrowed or removed are reconciled, even though the grant's current spec no
// longer describes them.
func TestReferenceGrantController_RevokedAccessIsReconciled(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1b1.Install(scheme)
	_ = aigv1b1.AddToScheme(scheme)

	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "app"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{{
				BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{
					Name:      "backend",
					Namespace: ptr.To(gwapiv1.Namespace("shared")),
				}},
			}},
		},
	}
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp", Namespace: "app"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{
					Name:      "secret",
					Namespace: ptr.To(gwapiv1.Namespace("shared")),
				},
			},
		},
	}
	// A grant that was narrowed: it no longer lists the "app" namespace in its from entries.
	narrowedGrant := &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "shared"},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{Group: aiServiceBackendGroup, Kind: aiGatewayRouteKind, Namespace: "other"}},
			To:   []gwapiv1b1.ReferenceGrantTo{{Group: aiServiceBackendGroup, Kind: aiServiceBackendKind}},
		},
	}

	t.Run("narrowed grant", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route, bsp, narrowedGrant).Build()
		routeChan := make(chan event.GenericEvent, 10)
		bspChan := make(chan event.GenericEvent, 10)
		c := NewReferenceGrantController(fakeClient, logr.Discard(), routeChan, bspChan, nil)

		_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(narrowedGrant)})
		require.NoError(t, err)
		require.Len(t, routeChan, 1)
		require.Equal(t, "route", (<-routeChan).Object.GetName())
		require.Len(t, bspChan, 1)
		require.Equal(t, "bsp", (<-bspChan).Object.GetName())
	})

	t.Run("grant already gone", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route, bsp).Build()
		routeChan := make(chan event.GenericEvent, 10)
		bspChan := make(chan event.GenericEvent, 10)
		c := NewReferenceGrantController(fakeClient, logr.Discard(), routeChan, bspChan, nil)

		_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "shared", Name: "grant"}})
		require.NoError(t, err)
		require.Len(t, routeChan, 1)
		require.Len(t, bspChan, 1)
	})
}

func TestReferenceGrantController_BackendSecurityPolicyReferencesNamespace_OIDC(t *testing.T) {
	c := &ReferenceGrantController{}
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp", Namespace: "app"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: egv1a1.OIDC{
							ClientSecret: gwapiv1.SecretObjectReference{
								Name:      "oidc-secret",
								Namespace: ptr.To(gwapiv1.Namespace("shared")),
							},
						},
					},
				},
			},
		},
	}
	require.True(t, c.backendSecurityPolicyReferencesNamespace(bsp, "shared"))
	require.False(t, c.backendSecurityPolicyReferencesNamespace(bsp, "other"))
}

func TestReferenceGrantController_AffectedQuotaPolicies(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gwapiv1b1.Install(scheme))
	require.NoError(t, aigv1a1.AddToScheme(scheme))
	require.NoError(t, aigv1b1.AddToScheme(scheme))

	remote := ptr.To(gwapiv1.Namespace("providers"))
	remotePolicy := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "platform"},
		Spec: aigv1a1.QuotaPolicySpec{TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
			Name: "provider", Namespace: remote,
		}}},
	}
	localPolicy := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "local", Namespace: "providers"},
		Spec: aigv1a1.QuotaPolicySpec{TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
			Name: "provider",
		}}},
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(remotePolicy, localPolicy).
		WithIndex(&aigv1a1.QuotaPolicy{}, k8sClientIndexQuotaPolicyTargetNamespace, quotaPolicyTargetNamespaceIndexFunc)
	c := NewReferenceGrantController(builder.Build(), logr.Discard(),
		make(chan event.GenericEvent, 10), make(chan event.GenericEvent, 10), make(chan event.GenericEvent, 10))

	affected, err := c.getAffectedQuotaPolicies(t.Context(), "providers")
	require.NoError(t, err)
	require.Len(t, affected, 1)
	require.Equal(t, "remote", affected[0].Name)
}

func TestReferenceGrantController_Reconcile_QuotaPolicyReferenceGrantChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gwapiv1b1.Install(scheme))
	require.NoError(t, aigv1a1.AddToScheme(scheme))
	require.NoError(t, aigv1b1.AddToScheme(scheme))

	remotePolicy := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "platform"},
		Spec: aigv1a1.QuotaPolicySpec{TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
			Group:     aiServiceBackendGroup,
			Kind:      aiServiceBackendKind,
			Name:      "provider",
			Namespace: ptr.To(gwapiv1a2.Namespace("providers")),
		}}},
	}
	localPolicy := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "local", Namespace: "providers"},
		Spec: aigv1a1.QuotaPolicySpec{TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
			Group: aiServiceBackendGroup,
			Kind:  aiServiceBackendKind,
			Name:  "provider",
		}}},
	}
	referenceGrant := &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-grant", Namespace: "providers"},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{
				Group:     "aigateway.envoyproxy.io",
				Kind:      "QuotaPolicy",
				Namespace: "platform",
			}},
			To: []gwapiv1b1.ReferenceGrantTo{{
				Group: aiServiceBackendGroup,
				Kind:  aiServiceBackendKind,
				Name:  ptr.To(gwapiv1b1.ObjectName("provider")),
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(remotePolicy, localPolicy, referenceGrant).
		WithIndex(&aigv1a1.QuotaPolicy{}, k8sClientIndexQuotaPolicyTargetNamespace, quotaPolicyTargetNamespaceIndexFunc).
		Build()
	quotaPolicyChan := make(chan event.GenericEvent, 10)
	controller := NewReferenceGrantController(
		fakeClient,
		logr.Discard(),
		make(chan event.GenericEvent, 10),
		make(chan event.GenericEvent, 10),
		quotaPolicyChan,
	)
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(referenceGrant)}

	// A grant creation must enqueue the cross-namespace QuotaPolicy, but not
	// the same-namespace policy that does not require a grant.
	_, err := controller.Reconcile(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, quotaPolicyChan, 1)
	eventObject := (<-quotaPolicyChan).Object.(*aigv1a1.QuotaPolicy)
	require.Equal(t, "platform/remote", eventObject.Namespace+"/"+eventObject.Name)

	// A grant deletion must enqueue the previously authorized policy again so
	// its stale cross-namespace configuration can be removed.
	var currentGrant gwapiv1b1.ReferenceGrant
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(referenceGrant), &currentGrant))
	require.NoError(t, fakeClient.Delete(t.Context(), &currentGrant))
	_, err = controller.Reconcile(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, quotaPolicyChan, 1)
	eventObject = (<-quotaPolicyChan).Object.(*aigv1a1.QuotaPolicy)
	require.Equal(t, "platform/remote", eventObject.Namespace+"/"+eventObject.Name)
}
