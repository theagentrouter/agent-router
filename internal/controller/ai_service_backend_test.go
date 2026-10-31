// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestAIServiceBackendController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventChan := internaltesting.NewControllerEventChan[*aigv1b1.AIGatewayRoute]()
	c := NewAIServiceBackendController(fakeClient, fake2.NewClientset(), ctrl.Log, eventChan.Ch)
	originals := []*aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: []gwapiv1a2.ParentReference{
					{
						Name:  "gtw",
						Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
						Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
					},
				},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						Matches:     []aigv1b1.AIGatewayRouteRuleMatch{{}},
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "mybackend"}},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute2", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: []gwapiv1a2.ParentReference{
					{
						Name:  "gtw",
						Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
						Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
					},
				},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						Matches:     []aigv1b1.AIGatewayRouteRuleMatch{{}},
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "mybackend"}},
					},
				},
			},
		},
	}
	for _, route := range originals {
		require.NoError(t, fakeClient.Create(t.Context(), route))
	}

	err := fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{ObjectMeta: metav1.ObjectMeta{Name: "mybackend", Namespace: "default"}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "mybackend"}})
	require.NoError(t, err)
	require.Equal(t, originals, eventChan.RequireItemsEventually(t, 2))

	// Check that the status was updated.
	var backend aigv1b1.AIServiceBackend
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "mybackend"}, &backend))
	require.Len(t, backend.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, backend.Status.Conditions[0].Type)
	require.Equal(t, "AIServiceBackend reconciled successfully", backend.Status.Conditions[0].Message)
	require.Contains(t, backend.Finalizers, aiGatewayControllerFinalizer, "Finalizer should be set")

	// Test the case where the AIServiceBackend is being deleted.
	err = fakeClient.Delete(t.Context(), &aigv1b1.AIServiceBackend{ObjectMeta: metav1.ObjectMeta{Name: "mybackend", Namespace: "default"}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "mybackend"}})
	require.NoError(t, err)
}

func TestAIServiceBackendController_Reconcile_error_with_multiple_bsps(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventChan := internaltesting.NewControllerEventChan[*aigv1b1.AIGatewayRoute]()
	c := NewAIServiceBackendController(fakeClient, fake2.NewClientset(), ctrl.Log, eventChan.Ch)

	const backendName, namespace = "mybackend", "default"
	// Create Multiple Backend Security Policies that target the same backend.
	for i := range 5 {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bsp-%d", i), Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: gwapiv1.ObjectName(backendName)}},
			},
		}
		require.NoError(t, fakeClient.Create(t.Context(), bsp))
	}

	err := fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: namespace}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: backendName}})
	require.ErrorContains(t, err, `multiple BackendSecurityPolicies found for AIServiceBackend mybackend: [bsp-0 bsp-1 bsp-2 bsp-3 bsp-4]`)
}

func TestAIServiceBackendController_validateBackendRef(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	eventChan := internaltesting.NewControllerEventChan[*aigv1b1.AIGatewayRoute]()
	c := NewAIServiceBackendController(fakeClient, fake2.NewClientset(), ctrl.Log, eventChan.Ch)

	pool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pool", Namespace: "default"},
		Spec: gwaiev1.InferencePoolSpec{
			Selector:    gwaiev1.LabelSelector{},
			TargetPorts: []gwaiev1.Port{{Number: 8000}},
			EndpointPickerRef: gwaiev1.EndpointPickerRef{
				Name: "epp-svc",
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), pool))

	t.Run("non_inference_pool_ref_is_noop", func(t *testing.T) {
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend"},
			},
		}
		require.NoError(t, c.validateBackendRef(t.Context(), backend))
	})

	t.Run("inference_pool_found", func(t *testing.T) {
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{
					Group: ptr.To(gwapiv1.Group(inferencePoolGroup)),
					Kind:  ptr.To(gwapiv1.Kind(inferencePoolKind)),
					Name:  "my-pool",
				},
			},
		}
		require.NoError(t, c.validateBackendRef(t.Context(), backend))
	})

	t.Run("inference_pool_not_found", func(t *testing.T) {
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{
					Group: ptr.To(gwapiv1.Group(inferencePoolGroup)),
					Kind:  ptr.To(gwapiv1.Kind(inferencePoolKind)),
					Name:  "missing-pool",
				},
			},
		}
		err := c.validateBackendRef(t.Context(), backend)
		require.ErrorContains(t, err, "missing-pool")
	})
}

func TestAIServiceBackendController_inferencePoolEventHandler(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesAndInferencePool(t)
	eventChan := internaltesting.NewControllerEventChan[*aigv1b1.AIGatewayRoute]()
	c := NewAIServiceBackendController(fakeClient, fake2.NewClientset(), ctrl.Log, eventChan.Ch)

	// Create two backends: one referencing the pool, one not.
	backendWithPool := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "b-pool", Namespace: "default"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{
				Group: ptr.To(gwapiv1.Group(inferencePoolGroup)),
				Kind:  ptr.To(gwapiv1.Kind(inferencePoolKind)),
				Name:  "my-pool",
			},
		},
	}
	backendWithoutPool := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "b-other", Namespace: "default"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend"},
		},
	}
	// Backend in namespace "a" that explicitly references a pool in namespace "other".
	backendCrossNamespace := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "b-cross", Namespace: "a"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{
				Group:     ptr.To(gwapiv1.Group(inferencePoolGroup)),
				Kind:      ptr.To(gwapiv1.Kind(inferencePoolKind)),
				Name:      "my-pool",
				Namespace: ptr.To(gwapiv1.Namespace("other")),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backendWithPool))
	require.NoError(t, fakeClient.Create(t.Context(), backendWithoutPool))
	require.NoError(t, fakeClient.Create(t.Context(), backendCrossNamespace))

	pool := &gwaiev1.InferencePool{ObjectMeta: metav1.ObjectMeta{Name: "my-pool", Namespace: "default"}}
	requests := c.inferencePoolEventHandler(t.Context(), pool)

	require.Len(t, requests, 1)
	require.Equal(t, reconcile.Request{NamespacedName: client.ObjectKey{Name: "b-pool", Namespace: "default"}}, requests[0])

	// A pool in namespace "other" should match the cross-namespace backend (which lives in namespace "a")
	// but not the default backend that implicitly references its own namespace.
	otherPool := &gwaiev1.InferencePool{ObjectMeta: metav1.ObjectMeta{Name: "my-pool", Namespace: "other"}}
	requests = c.inferencePoolEventHandler(t.Context(), otherPool)
	require.Len(t, requests, 1)
	require.Equal(t, reconcile.Request{NamespacedName: client.ObjectKey{Name: "b-cross", Namespace: "a"}}, requests[0])

	// A pool named "my-pool" in namespace "a" must NOT match the cross-namespace backend, which
	// references "other/my-pool" rather than its own namespace.
	poolInA := &gwaiev1.InferencePool{ObjectMeta: metav1.ObjectMeta{Name: "my-pool", Namespace: "a"}}
	requests = c.inferencePoolEventHandler(t.Context(), poolInA)
	require.Empty(t, requests)
}
