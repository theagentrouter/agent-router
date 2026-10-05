// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func requireNewFakeClientWithIndexesAndInferencePool(t *testing.T) client.Client {
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1b1.AIGatewayRoute{}).
		WithStatusSubresource(&aigv1b1.AIServiceBackend{}).
		WithStatusSubresource(&aigv1b1.BackendSecurityPolicy{}).
		WithStatusSubresource(&gwaiev1.InferencePool{})
	err := ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	})
	require.NoError(t, err)
	return builder.Build()
}

func TestInferencePoolController_ExtensionReferenceValidation(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create an InferencePool with ExtensionReference pointing to a non-existent service.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "non-existent-service",
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Reconcile the InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
	})
	require.Error(t, err, "Expected error due to non-existent ExtensionReference service")
	require.Contains(t, err.Error(), "ExtensionReference service non-existent-service not found")
	require.Equal(t, ctrl.Result{}, result)

	// Check that the InferencePool status was updated with ResolvedRefs condition.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool",
		Namespace: "default",
	}, &updatedInferencePool))

	// Since there are no Gateways referencing this InferencePool, the status should be empty.
	// But the error should have been handled and the status updated appropriately.
	require.Empty(t, updatedInferencePool.Status.Parents, "InferencePool should have no parent status when not referenced by any Gateway")
}

func TestInferencePoolController_ExtensionReferenceValidationSuccess(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create the service that the InferencePool will reference.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-service",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 9002,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), service))

	// Create an InferencePool with ExtensionReference pointing to the existing service.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "existing-service",
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Reconcile the InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
	})
	require.NoError(t, err, "Expected no error when ExtensionReference service exists")
	require.Equal(t, ctrl.Result{}, result)

	// Check that the InferencePool status was updated successfully.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool",
		Namespace: "default",
	}, &updatedInferencePool))

	// Since there are no Gateways referencing this InferencePool, the status should be empty.
	require.Empty(t, updatedInferencePool.Status.Parents, "InferencePool should have no parent status when not referenced by any Gateway")
}

func TestInferencePoolController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create the service that the InferencePool will reference.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-epp",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 9002,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), service))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Create an AIGatewayRoute that references an InferencePool.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "test-gateway",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:   "test-inference-pool",
							Group:  ptr.To("inference.networking.k8s.io"),
							Kind:   ptr.To("InferencePool"),
							Weight: ptr.To(int32(100)),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "test-epp",
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Reconcile the InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	// Check that the InferencePool status was updated.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool",
		Namespace: "default",
	}, &updatedInferencePool))

	// Verify that the status contains the expected parent Gateway.
	require.Len(t, updatedInferencePool.Status.Parents, 1)
	parent := updatedInferencePool.Status.Parents[0]

	require.Equal(t, "gateway.networking.k8s.io", string(*parent.ParentRef.Group))
	require.Equal(t, "Gateway", string(parent.ParentRef.Kind))
	require.Equal(t, "test-gateway", string(parent.ParentRef.Name))
	require.Equal(t, "default", string(parent.ParentRef.Namespace))

	// Verify that the conditions are set correctly.
	require.Len(t, parent.Conditions, 2, "Should have both Accepted and ResolvedRefs conditions")

	// Find and verify the Accepted condition.
	var acceptedCondition *metav1.Condition
	var resolvedRefsCondition *metav1.Condition
	for i := range parent.Conditions {
		condition := &parent.Conditions[i]
		switch condition.Type {
		case "Accepted":
			acceptedCondition = condition
		case "ResolvedRefs":
			resolvedRefsCondition = condition
		}
	}

	require.NotNil(t, acceptedCondition, "Should have Accepted condition")
	require.Equal(t, metav1.ConditionTrue, acceptedCondition.Status)
	require.Equal(t, "Accepted", acceptedCondition.Reason)

	require.NotNil(t, resolvedRefsCondition, "Should have ResolvedRefs condition")
	require.Equal(t, metav1.ConditionTrue, resolvedRefsCondition.Status)
	require.Equal(t, "ResolvedRefs", resolvedRefsCondition.Reason)
}

func TestInferencePoolController_NoReferencingGateways(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create the service that the InferencePool will reference.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-epp",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 9002,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), service))

	// Create an InferencePool without any referencing AIGatewayRoutes.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "test-epp",
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Reconcile the InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	// Check that the InferencePool status was updated.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool",
		Namespace: "default",
	}, &updatedInferencePool))

	// Verify that the status has no parents since no Gateway references this InferencePool.
	require.Empty(t, updatedInferencePool.Status.Parents, "InferencePool should have no parent status when not referenced by any Gateway")
}

func TestBuildAcceptedCondition(t *testing.T) {
	condition := buildAcceptedCondition(1, "test-controller", "Accepted", "test message")

	require.Equal(t, "Accepted", condition.Type)
	require.Equal(t, metav1.ConditionTrue, condition.Status)
	require.Equal(t, "Accepted", condition.Reason)
	require.Contains(t, condition.Message, "InferencePool has been Accepted by controller test-controller: test message")
	require.Equal(t, int64(1), condition.ObservedGeneration)

	// Test NotAccepted condition.
	condition = buildAcceptedCondition(2, "test-controller", "NotAccepted", "error message")

	require.Equal(t, "Accepted", condition.Type)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, "NotAccepted", condition.Reason)
	require.Contains(t, condition.Message, "InferencePool has been NotAccepted by controller test-controller: error message")
	require.Equal(t, int64(2), condition.ObservedGeneration)

	// Test other condition types that should result in True status and keep the original type.
	condition = buildAcceptedCondition(3, "test-controller", "SomeOtherType", "other message")

	require.Equal(t, "SomeOtherType", condition.Type)        // Type is preserved for non-"NotAccepted" types.
	require.Equal(t, metav1.ConditionTrue, condition.Status) // Status is True for non-"NotAccepted" types.
	require.Equal(t, "Accepted", condition.Reason)           // Reason is "Accepted" for non-"NotAccepted" types.
	require.Contains(t, condition.Message, "InferencePool has been Accepted by controller test-controller: other message")
	require.Equal(t, int64(3), condition.ObservedGeneration)
}

func TestBuildResolvedRefsCondition(t *testing.T) {
	// Test successful ResolvedRefs condition.
	condition := buildResolvedRefsCondition(1, "test-controller", true, "ResolvedRefs", "all references resolved")

	require.Equal(t, "ResolvedRefs", condition.Type)
	require.Equal(t, metav1.ConditionTrue, condition.Status)
	require.Equal(t, "ResolvedRefs", condition.Reason)
	require.Contains(t, condition.Message, "Reference resolution by controller test-controller: all references resolved")
	require.Equal(t, int64(1), condition.ObservedGeneration)

	// Test failed ResolvedRefs condition.
	condition = buildResolvedRefsCondition(2, "test-controller", false, "BackendNotFound", "service not found")

	require.Equal(t, "ResolvedRefs", condition.Type)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, "BackendNotFound", condition.Reason)
	require.Contains(t, condition.Message, "Reference resolution by controller test-controller: service not found")
	require.Equal(t, int64(2), condition.ObservedGeneration)
}

func TestInferencePoolController_HTTPRouteReferencesInferencePool(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Test HTTPRoute that references InferencePool.
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-http-route",
			Namespace: "default",
		},
		Spec: gwapiv1.HTTPRouteSpec{
			Rules: []gwapiv1.HTTPRouteRule{
				{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{
							BackendRef: gwapiv1.BackendRef{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")),
									Kind:  ptr.To(gwapiv1.Kind("InferencePool")),
									Name:  "test-inference-pool",
								},
							},
						},
					},
				},
			},
		},
	}

	// Test positive case.
	result := c.httpRouteReferencesInferencePool(httpRoute, "test-inference-pool")
	require.True(t, result, "Should return true when HTTPRoute references the InferencePool")

	// Test negative case - different name.
	result = c.httpRouteReferencesInferencePool(httpRoute, "different-pool")
	require.False(t, result, "Should return false when HTTPRoute doesn't reference the InferencePool")

	// Test HTTPRoute without InferencePool backend.
	httpRouteNoInferencePool := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-http-route-no-pool",
			Namespace: "default",
		},
		Spec: gwapiv1.HTTPRouteSpec{
			Rules: []gwapiv1.HTTPRouteRule{
				{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{
							BackendRef: gwapiv1.BackendRef{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Name: "regular-service",
								},
							},
						},
					},
				},
			},
		},
	}

	result = c.httpRouteReferencesInferencePool(httpRouteNoInferencePool, "test-inference-pool")
	require.False(t, result, "Should return false when HTTPRoute doesn't have InferencePool backend")
}

func TestInferencePoolController_RouteReferencesGateway(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Test with matching gateway name and namespace.
	parentRefs := []gwapiv1.ParentReference{
		{
			Name:      "test-gateway",
			Namespace: ptr.To(gwapiv1.Namespace("test-namespace")),
		},
	}

	result := c.routeReferencesGateway(parentRefs, "test-gateway", "test-namespace", "test-namespace")
	require.True(t, result, "Should return true when route references the gateway with matching namespace")

	// Test with matching gateway name but different namespace.
	result = c.routeReferencesGateway(parentRefs, "test-gateway", "different-namespace", "test-namespace")
	require.False(t, result, "Should return false when route references the gateway with different namespace")

	// Test with different gateway name.
	result = c.routeReferencesGateway(parentRefs, "different-gateway", "test-namespace", "test-namespace")
	require.False(t, result, "Should return false when route references different gateway")

	// Test with nil namespace (should match any namespace).
	parentRefsNoNamespace := []gwapiv1.ParentReference{
		{
			Name: "test-gateway",
		},
	}

	result = c.routeReferencesGateway(parentRefsNoNamespace, "test-gateway", "any-namespace", "any-namespace")
	require.True(t, result, "Should return true when route references gateway without namespace specified")

	// Test with empty parent refs.
	result = c.routeReferencesGateway([]gwapiv1.ParentReference{}, "test-gateway", "test-namespace", "test-namespace")
	require.False(t, result, "Should return false when no parent refs")
}

func TestInferencePoolController_GatewayReferencesInferencePool(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Create an AIGatewayRoute that references the Gateway and InferencePool.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-namespace",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name:      "test-gateway",
					Namespace: ptr.To(gwapiv1.Namespace("default")),
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Test positive case - Gateway references InferencePool through AIGatewayRoute.
	result := c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "test-namespace")
	require.True(t, result, "Should return true when Gateway references InferencePool through AIGatewayRoute")

	// Test negative case - different InferencePool name.
	result = c.gatewayReferencesInferencePool(context.Background(), gateway, "different-pool", "test-namespace")
	require.False(t, result, "Should return false when Gateway doesn't reference the specified InferencePool")

	// Test negative case - different namespace.
	result = c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "different-namespace")
	require.False(t, result, "Should return false when InferencePool is in different namespace")
}

func TestInferencePoolController_gatewayEventHandler(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Create an AIGatewayRoute that references the InferencePool.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "test-gateway",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	res := c.gatewayEventHandler(t.Context(), gateway)
	require.Len(t, res, 1, "Should return one InferencePool for Gateway with AIGatewayRoute referencing it")
	require.Equal(t, inferencePool.Name, res[0].Name, "Should return the correct InferencePool name")
	require.Equal(t, inferencePool.Namespace, res[0].Namespace, "Should return the correct InferencePool namespace")
}

func TestInferencePoolController_aiGatewayRouteEventHandler(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create an AIGatewayRoute that references an InferencePool.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}

	require.Empty(t, c.aiGatewayRouteEventHandler(t.Context(), nil))
	require.Empty(t, c.aiGatewayRouteEventHandler(t.Context(), &aigv1b1.AIGatewayRoute{}))
	res := c.aiGatewayRouteEventHandler(t.Context(), aiGatewayRoute)
	require.Len(t, res, 1, "Should return one InferencePool for AIGatewayRoute with BackendRef")
	require.Equal(t, "test-inference-pool", res[0].Name, "Should return the correct InferencePool name")
	require.Equal(t, aiGatewayRoute.Namespace, res[0].Namespace, "Should return the correct InferencePool namespace")
}

func TestInferencePoolController_httpRouteEventHandler(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create an HTTPRoute that references an InferencePool.
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-http-route",
			Namespace: "default",
		},
		Spec: gwapiv1.HTTPRouteSpec{
			Rules: []gwapiv1.HTTPRouteRule{
				{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{
							BackendRef: gwapiv1.BackendRef{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")),
									Kind:  ptr.To(gwapiv1.Kind("InferencePool")),
									Name:  "test-inference-pool",
								},
							},
						},
					},
				},
			},
		},
	}

	require.Empty(t, c.aiGatewayRouteEventHandler(t.Context(), nil))
	require.Empty(t, c.aiGatewayRouteEventHandler(t.Context(), &aigv1b1.AIGatewayRoute{}))
	res := c.httpRouteEventHandler(t.Context(), httpRoute)
	require.Len(t, res, 1, "Should return one InferencePool for HTTPRoute with BackendRef")
	require.Equal(t, "test-inference-pool", res[0].Name, "Should return the correct InferencePool name")
	require.Equal(t, httpRoute.Namespace, res[0].Namespace, "Should return the correct InferencePool namespace")
}

func TestInferencePoolController_EdgeCases(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Test reconcile with non-existent InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "non-existent-pool",
			Namespace: "default",
		},
	})
	require.NoError(t, err, "Should not error when InferencePool doesn't exist")
	require.Equal(t, ctrl.Result{}, result)

	// Test InferencePool with empty ExtensionRef name.
	inferencePoolEmptyExtRef := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-empty-ext",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "", // Empty name.
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePoolEmptyExtRef))

	result, err = c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool-empty-ext",
			Namespace: "default",
		},
	})
	require.Error(t, err, "Should error when ExtensionRef name is empty")
	require.Contains(t, err.Error(), "ExtensionReference name is empty")
	require.Equal(t, ctrl.Result{}, result)
}

func TestInferencePoolController_CrossNamespaceReferences(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway in a different namespace.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "gateway-namespace",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Create an AIGatewayRoute in the InferencePool namespace that references the Gateway in a different namespace.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name:      "test-gateway",
					Namespace: ptr.To(gwapiv1.Namespace("gateway-namespace")),
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Create the service that the InferencePool will reference.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-epp",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 9002,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), service))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "test-epp",
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Reconcile the InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	// Check that the InferencePool status was updated with the cross-namespace Gateway.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool",
		Namespace: "default",
	}, &updatedInferencePool))

	// Verify that the status contains the expected parent Gateway from different namespace.
	require.Len(t, updatedInferencePool.Status.Parents, 1)
	parent := updatedInferencePool.Status.Parents[0]

	require.Equal(t, "gateway.networking.k8s.io", string(*parent.ParentRef.Group))
	require.Equal(t, "Gateway", string(parent.ParentRef.Kind))
	require.Equal(t, "test-gateway", string(parent.ParentRef.Name))
	require.Equal(t, "gateway-namespace", string(parent.ParentRef.Namespace))
}

func TestInferencePoolController_UpdateInferencePoolStatus(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Create an AIGatewayRoute that references an InferencePool.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "test-gateway",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-inference-pool",
			Namespace:  "default",
			Generation: 5, // Set a specific generation for testing.
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Test updateInferencePoolStatus with NotAccepted condition.
	c.updateInferencePoolStatus(context.Background(), inferencePool, "NotAccepted", "test error message")

	// Check that the status was updated.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool",
		Namespace: "default",
	}, &updatedInferencePool))

	// Verify that the status contains the expected parent Gateway with error condition.
	require.Len(t, updatedInferencePool.Status.Parents, 1)
	parent := updatedInferencePool.Status.Parents[0]

	require.Equal(t, "test-gateway", string(parent.ParentRef.Name))
	require.Len(t, parent.Conditions, 2, "Should have both Accepted and ResolvedRefs conditions")

	// Find the conditions.
	var acceptedCondition, resolvedRefsCondition *metav1.Condition
	for i := range parent.Conditions {
		condition := &parent.Conditions[i]
		switch condition.Type {
		case "Accepted":
			acceptedCondition = condition
		case "ResolvedRefs":
			resolvedRefsCondition = condition
		}
	}

	require.NotNil(t, acceptedCondition, "Should have Accepted condition")
	require.Equal(t, metav1.ConditionFalse, acceptedCondition.Status)
	require.Equal(t, "NotAccepted", acceptedCondition.Reason)
	require.Equal(t, int64(5), acceptedCondition.ObservedGeneration)

	require.NotNil(t, resolvedRefsCondition, "Should have ResolvedRefs condition")
	require.Equal(t, metav1.ConditionTrue, resolvedRefsCondition.Status)
	require.Equal(t, "ResolvedRefs", resolvedRefsCondition.Reason)
}

func TestInferencePoolController_GetReferencedGateways_ErrorHandling(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}

	// Test getReferencedGateways with various scenarios.
	gateways, err := c.getReferencedGateways(context.Background(), inferencePool)
	require.NoError(t, err)
	require.Empty(t, gateways, "Should return empty list when no routes reference the InferencePool")

	// Create an AIGatewayRoute that references the InferencePool but no Gateway.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route-no-gateway",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	gateways, err = c.getReferencedGateways(context.Background(), inferencePool)
	require.NoError(t, err)
	require.Empty(t, gateways, "Should return empty list when route has no parent refs")
}

func TestInferencePoolController_GatewayReferencesInferencePool_HTTPRoute(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Create an HTTPRoute that references the Gateway and InferencePool.
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-http-route",
			Namespace: "test-namespace",
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{
						Name:      "test-gateway",
						Namespace: ptr.To(gwapiv1.Namespace("default")),
					},
				},
			},
			Rules: []gwapiv1.HTTPRouteRule{
				{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{
							BackendRef: gwapiv1.BackendRef{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")),
									Kind:  ptr.To(gwapiv1.Kind("InferencePool")),
									Name:  "test-inference-pool",
								},
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), httpRoute))

	// Test positive case - Gateway references InferencePool through HTTPRoute.
	result := c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "test-namespace")
	require.True(t, result, "Should return true when Gateway references InferencePool through HTTPRoute")

	// Test negative case - different InferencePool name.
	result = c.gatewayReferencesInferencePool(context.Background(), gateway, "different-pool", "test-namespace")
	require.False(t, result, "Should return false when Gateway doesn't reference the specified InferencePool")

	// Test negative case - different namespace.
	result = c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "different-namespace")
	require.False(t, result, "Should return false when InferencePool is in different namespace")
}

func TestInferencePoolController_ValidateExtensionReference_EdgeCases(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))
	// Test with service in different namespace (should fail).
	serviceOtherNS := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-other-ns",
			Namespace: "other-namespace",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 9002,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), serviceOtherNS))

	inferencePoolOtherNS := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-other-ns",
			Namespace: "default", // InferencePool in default namespace.
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "service-other-ns", // Refers to service in other-namespace.
			},
		},
	}

	err := c.validateExtensionReference(context.Background(), inferencePoolOtherNS)
	require.Error(t, err, "Should error when ExtensionReference service is in different namespace")
	require.Contains(t, err.Error(), "ExtensionReference service service-other-ns not found in namespace default")
}

func TestInferencePoolController_Reconcile_ErrorHandling(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Test reconcile with InferencePool that has empty ExtensionRef name.
	inferencePoolEmptyName := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-empty-name",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "", // Empty name.
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePoolEmptyName))

	// This should trigger the error path in Reconcile.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool-empty-name",
			Namespace: "default",
		},
	})
	require.Error(t, err, "Should error when ExtensionRef name is empty")
	require.Contains(t, err.Error(), "ExtensionReference name is empty")
	require.Equal(t, ctrl.Result{}, result)

	// Test reconcile with InferencePool that has non-existent ExtensionRef service.
	inferencePoolNonExistentService := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-non-existent",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "non-existent-service",
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePoolNonExistentService))

	// This should trigger the error path in Reconcile.
	result, err = c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool-non-existent",
			Namespace: "default",
		},
	})
	require.Error(t, err, "Should error when ExtensionRef service doesn't exist")
	require.Contains(t, err.Error(), "ExtensionReference service non-existent-service not found")
	require.Equal(t, ctrl.Result{}, result)
}

func TestInferencePoolController_Reconcile_EndpointPickerRefMissing(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway and an AIGatewayRoute referencing the InferencePool so that a parent
	// status entry is produced.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway-no-epp-ref",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-no-epp-ref",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{Name: "test-gateway-no-epp-ref"},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool-no-epp-ref",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Create an InferencePool with EndpointPickerRef unset. This is a legal, schema-valid state
	// as of Gateway API Inference Extension v1.5.0 (the field is optional).
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-no-epp-ref",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool-no-epp-ref",
			Namespace: "default",
		},
	})
	require.Error(t, err, "Should error when endpointPickerRef is unset")
	require.Contains(t, err.Error(), "endpointPickerRef is not set")
	require.Equal(t, ctrl.Result{}, result)

	// The Accepted condition must use the upstream-defined EndpointPickerRefMissing reason
	// (per gwaiev1.InferencePoolReasonEndpointPickerRefMissing), not a generic "NotAccepted",
	// so that conformance tooling recognizes this as a legal (if unaccepted) state.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool-no-epp-ref",
		Namespace: "default",
	}, &updatedInferencePool))

	require.Len(t, updatedInferencePool.Status.Parents, 1)
	parent := updatedInferencePool.Status.Parents[0]

	var acceptedCondition *metav1.Condition
	for i := range parent.Conditions {
		if parent.Conditions[i].Type == string(gwaiev1.InferencePoolConditionAccepted) {
			acceptedCondition = &parent.Conditions[i]
		}
	}
	require.NotNil(t, acceptedCondition, "Should have Accepted condition")
	require.Equal(t, metav1.ConditionFalse, acceptedCondition.Status)
	require.Equal(t, string(gwaiev1.InferencePoolReasonEndpointPickerRefMissing), acceptedCondition.Reason)
}

func TestInferencePoolController_SyncInferencePool_EdgeCases(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Test syncInferencePool with InferencePool that has no referenced gateways.
	inferencePoolNoGateways := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-no-gateways",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePoolNoGateways))

	// Create the service that the InferencePool will reference.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-epp-no-gateways",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 9002,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), service))
	inferencePoolNoGateways.Spec.EndpointPickerRef = &gwaiev1.EndpointPickerRef{
		Name: "test-epp-no-gateways",
	}
	require.NoError(t, fakeClient.Update(context.Background(), inferencePoolNoGateways))

	// Reconcile should succeed even when no gateways reference the InferencePool.
	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Name:      "test-inference-pool-no-gateways",
			Namespace: "default",
		},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	// Check that the InferencePool status is empty (no parents).
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool-no-gateways",
		Namespace: "default",
	}, &updatedInferencePool))

	require.Empty(t, updatedInferencePool.Status.Parents, "Should have no parent statuses when no gateways reference the InferencePool")
}

func TestInferencePoolController_GetReferencedGateways_ComplexScenarios(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inference-pool-complex",
			Namespace: "default",
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}

	// Create multiple Gateways.
	gateway1 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-1",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway1))

	gateway2 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-2",
			Namespace: "other-namespace",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway2))

	// Create AIGatewayRoutes that reference different gateways.
	aiGatewayRoute1 := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-1",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "gateway-1",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool-complex",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute1))

	aiGatewayRoute2 := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-2",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name:      "gateway-2",
					Namespace: ptr.To(gwapiv1.Namespace("other-namespace")),
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool-complex",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute2))

	// Create HTTPRoute that also references the InferencePool.
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "http-route-complex",
			Namespace: "default",
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{
						Name: "gateway-1",
					},
				},
			},
			Rules: []gwapiv1.HTTPRouteRule{
				{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{
							BackendRef: gwapiv1.BackendRef{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Group: ptr.To(gwapiv1.Group("inference.networking.k8s.io")),
									Kind:  ptr.To(gwapiv1.Kind("InferencePool")),
									Name:  "test-inference-pool-complex",
								},
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), httpRoute))

	// Test getReferencedGateways should return both gateways.
	gateways, err := c.getReferencedGateways(context.Background(), inferencePool)
	require.NoError(t, err)
	require.Len(t, gateways, 2, "Should return both gateways that reference the InferencePool")

	// Verify the gateways are the expected ones.
	gatewayNames := make(map[string]string)
	for _, gw := range gateways {
		gatewayNames[gw.Name] = gw.Namespace
	}
	require.Contains(t, gatewayNames, "gateway-1")
	require.Equal(t, "default", gatewayNames["gateway-1"])
	require.Contains(t, gatewayNames, "gateway-2")
	require.Equal(t, "other-namespace", gatewayNames["gateway-2"])
}

func TestInferencePoolController_UpdateInferencePoolStatus_MultipleGateways(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create multiple Gateways.
	gateway1 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-1",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway1))

	gateway2 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-2",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway2))

	// Create AIGatewayRoutes that reference different gateways.
	aiGatewayRoute1 := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-1",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "gateway-1",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool-multi",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute1))

	aiGatewayRoute2 := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-2",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "gateway-2",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool-multi",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute2))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-inference-pool-multi",
			Namespace:  "default",
			Generation: 10, // Set a specific generation for testing.
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Test updateInferencePoolStatus with Accepted condition for multiple gateways.
	c.updateInferencePoolStatus(context.Background(), inferencePool, "Accepted", "all references resolved")

	// Check that the status was updated for both gateways.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool-multi",
		Namespace: "default",
	}, &updatedInferencePool))

	// Verify that the status contains both parent Gateways.
	require.Len(t, updatedInferencePool.Status.Parents, 2, "Should have status for both gateways")

	// Verify both gateways have the correct conditions.
	for _, parent := range updatedInferencePool.Status.Parents {
		require.Len(t, parent.Conditions, 2, "Should have both Accepted and ResolvedRefs conditions")

		// Find the conditions.
		var acceptedCondition, resolvedRefsCondition *metav1.Condition
		for i := range parent.Conditions {
			condition := &parent.Conditions[i]
			switch condition.Type {
			case "Accepted":
				acceptedCondition = condition
			case "ResolvedRefs":
				resolvedRefsCondition = condition
			}
		}

		require.NotNil(t, acceptedCondition, "Should have Accepted condition")
		require.Equal(t, metav1.ConditionTrue, acceptedCondition.Status)
		require.Equal(t, "Accepted", acceptedCondition.Reason)
		require.Equal(t, int64(10), acceptedCondition.ObservedGeneration)

		require.NotNil(t, resolvedRefsCondition, "Should have ResolvedRefs condition")
		require.Equal(t, metav1.ConditionTrue, resolvedRefsCondition.Status)
		require.Equal(t, "ResolvedRefs", resolvedRefsCondition.Reason)
	}
}

func TestInferencePoolController_GatewayReferencesInferencePool_NoRoutes(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway-no-routes",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Test gatewayReferencesInferencePool when there are no routes at all.
	result := c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "default")
	require.False(t, result, "Should return false when there are no routes")

	// Test gatewayReferencesInferencePool when there are routes but they don't reference the gateway.
	aiGatewayRouteNoRef := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-no-ref",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "different-gateway", // Different gateway.
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRouteNoRef))

	result = c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "default")
	require.False(t, result, "Should return false when routes don't reference the gateway")

	// Test gatewayReferencesInferencePool when routes reference the gateway but not the InferencePool.
	aiGatewayRouteNoPool := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-no-pool",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "test-gateway-no-routes", // Correct gateway.
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name: "different-pool", // Different pool.
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRouteNoPool))

	result = c.gatewayReferencesInferencePool(context.Background(), gateway, "test-inference-pool", "default")
	require.False(t, result, "Should return false when routes don't reference the InferencePool")
}

func TestInferencePoolController_UpdateInferencePoolStatus_ExtensionRefError(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	c := NewInferencePoolController(fakeClient, kubefake.NewSimpleClientset(), ctrl.Log, make(chan event.GenericEvent))

	// Create a Gateway.
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway-ext-error",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), gateway))

	// Create an AIGatewayRoute that references the InferencePool.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-ext-error",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name: "test-gateway-ext-error",
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:  "test-inference-pool-ext-error",
							Group: ptr.To("inference.networking.k8s.io"),
							Kind:  ptr.To("InferencePool"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), aiGatewayRoute))

	// Create an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-inference-pool-ext-error",
			Namespace:  "default",
			Generation: 15, // Set a specific generation for testing.
		},
		Spec: gwaiev1.InferencePoolSpec{
			Selector: gwaiev1.LabelSelector{MatchLabels: map[gwaiev1.LabelKey]gwaiev1.LabelValue{
				"app": "test-app",
			}},
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), inferencePool))

	// Test updateInferencePoolStatus with ExtensionReference error message.
	extRefErrorMessage := "ExtensionReference service non-existent-service not found in namespace default"
	c.updateInferencePoolStatus(context.Background(), inferencePool, "NotAccepted", extRefErrorMessage)

	// Check that the status was updated with ExtensionReference error.
	var updatedInferencePool gwaiev1.InferencePool
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-inference-pool-ext-error",
		Namespace: "default",
	}, &updatedInferencePool))

	// Verify that the status contains the expected parent Gateway with ExtensionReference error.
	require.Len(t, updatedInferencePool.Status.Parents, 1)
	parent := updatedInferencePool.Status.Parents[0]

	require.Equal(t, "test-gateway-ext-error", string(parent.ParentRef.Name))
	require.Len(t, parent.Conditions, 2, "Should have both Accepted and ResolvedRefs conditions")

	// Find the conditions.
	var acceptedCondition, resolvedRefsCondition *metav1.Condition
	for i := range parent.Conditions {
		condition := &parent.Conditions[i]
		switch condition.Type {
		case "Accepted":
			acceptedCondition = condition
		case "ResolvedRefs":
			resolvedRefsCondition = condition
		}
	}

	require.NotNil(t, acceptedCondition, "Should have Accepted condition")
	require.Equal(t, metav1.ConditionFalse, acceptedCondition.Status)
	require.Equal(t, "NotAccepted", acceptedCondition.Reason)
	require.Equal(t, int64(15), acceptedCondition.ObservedGeneration)

	require.NotNil(t, resolvedRefsCondition, "Should have ResolvedRefs condition")
	require.Equal(t, metav1.ConditionFalse, resolvedRefsCondition.Status, "ResolvedRefs should be False for ExtensionReference error")
	require.Equal(t, "ResolvedRefs", resolvedRefsCondition.Reason)
	require.Contains(t, resolvedRefsCondition.Message, extRefErrorMessage)
}
