// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

func TestAIServiceBackendController_deleteNotifiesRoutesDespiteMultipleBSPs(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventChan := internaltesting.NewControllerEventChan[*aigv1b1.AIGatewayRoute]()
	c := NewAIServiceBackendController(fakeClient, fake2.NewClientset(), ctrl.Log, eventChan.Ch)

	const backendName, namespace = "mybackend", "default"
	require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1a2.ParentReference{
				{Name: "gtw", Kind: ptr.To(gwapiv1a2.Kind("Gateway")), Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io"))},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{Matches: []aigv1b1.AIGatewayRouteRuleMatch{{}}, BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: backendName}}},
			},
		},
	}))
	for i := range 2 {
		require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bsp-%d", i), Namespace: namespace},
			Spec:       aigv1b1.BackendSecurityPolicySpec{TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: gwapiv1.ObjectName(backendName)}}},
		}))
	}
	backend := &aigv1b1.AIServiceBackend{ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: namespace}}
	require.NoError(t, fakeClient.Create(t.Context(), backend))
	_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: backendName}})
	require.ErrorContains(t, err, "multiple BackendSecurityPolicies")

	require.NoError(t, fakeClient.Delete(t.Context(), backend))
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: backendName}})
	require.NoError(t, err, "uniqueness check must not run on the delete path")
	got := eventChan.RequireItemsEventually(t, 1)
	require.Equal(t, "myroute", got[0].Name)

	var deleting aigv1b1.AIServiceBackend
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: backendName}, &deleting)
	if !apierrors.IsNotFound(err) {
		require.NoError(t, err)
		require.NotContains(t, deleting.Finalizers, aiGatewayControllerFinalizer)
	}
}
