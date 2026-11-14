// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/runner"
)

// requireNewFakeClientWithIndexesForQuotaPolicy creates a fake client with indexes and
// status subresources needed for QuotaPolicy controller tests.
func requireNewFakeClientWithIndexesForQuotaPolicy(t *testing.T) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1a1.QuotaPolicy{}).
		WithStatusSubresource(&aigv1b1.AIServiceBackend{})
	err := ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	})
	require.NoError(t, err)
	return builder.Build()
}

// newTestRunner creates a Runner with an initialized cache for use in tests.
// The runner is started in a background goroutine and stopped when the test completes.
func newTestRunner(t *testing.T) *runner.Runner {
	t.Helper()
	r := runner.New(ctrl.Log, 0)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = r.Start(ctx) }()
	// Wait for the cache to be initialized.
	require.Eventually(t, func() bool {
		return r.UpdateConfigs(t.Context(), nil) == nil
	}, 5*time.Second, 50*time.Millisecond)
	return r
}

func TestQuotaPolicyController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create an AIServiceBackend.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "mybackend", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1a2.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1a2.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy targeting the backend.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "myquotapolicy", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "mybackend",
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	// Reconcile should succeed.
	res, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "myquotapolicy"},
	})
	require.NoError(t, err)
	require.False(t, res.Requeue)

	// Verify status is Accepted.
	var updatedQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "myquotapolicy"}, &updatedQP))
	require.Len(t, updatedQP.Status.Conditions, 1)
	require.Equal(t, aigv1a1.ConditionTypeAccepted, updatedQP.Status.Conditions[0].Type)
	require.Equal(t, "QuotaPolicy reconciled successfully", updatedQP.Status.Conditions[0].Message)
	require.Contains(t, updatedQP.Finalizers, aiGatewayControllerFinalizer, "Finalizer should be added")
}

func TestQuotaPolicyController_Reconcile_NotFound(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))

	// Reconcile a non-existent QuotaPolicy. Deletion cleanup is handled in the finalizer callback, so
	// the NotFound path is now a no-op that simply returns without error.
	res, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "nonexistent"},
	})
	require.NoError(t, err)
	require.False(t, res.Requeue)
}

func TestQuotaPolicyController_Reconcile_SyncError(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	// Use a runner whose cache is not initialized to trigger UpdateConfigs error.
	uninitializedRunner := runner.New(ctrl.Log, 0)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, uninitializedRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create an AIServiceBackend.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-for-error", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1a2.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1a2.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy targeting the backend with valid config.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-sync-error", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1a2.ObjectName(backend.Name),
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	// Reconcile should fail because the runner's cache is not initialized.
	_, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "qp-sync-error"},
	})
	require.Error(t, err)

	// Verify status is NotAccepted.
	var updatedQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "qp-sync-error"}, &updatedQP))
	require.Len(t, updatedQP.Status.Conditions, 1)
	require.Equal(t, aigv1a1.ConditionTypeNotAccepted, updatedQP.Status.Conditions[0].Type)
}

func TestQuotaPolicyController_Reconcile_InvalidDuration(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create an AIServiceBackend.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-invalid-dur", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1a2.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1a2.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy with an invalid duration.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-invalid-duration", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1a2.ObjectName(backend.Name),
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "invalid"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	// Reconcile should fail because the duration is invalid.
	_, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "qp-invalid-duration"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to build rate limit configs")

	// Verify status is NotAccepted.
	var updatedQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "qp-invalid-duration"}, &updatedQP))
	require.Len(t, updatedQP.Status.Conditions, 1)
	require.Equal(t, aigv1a1.ConditionTypeNotAccepted, updatedQP.Status.Conditions[0].Type)
}

func TestQuotaPolicyController_Reconcile_Deletion(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create an AIServiceBackend.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-delete", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1a2.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1a2.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-delete", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1a2.ObjectName(backend.Name),
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	// First reconcile to add finalizer and sync.
	_, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "qp-delete"},
	})
	require.NoError(t, err)

	// Verify finalizer was added.
	var createdQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "qp-delete"}, &createdQP))
	require.Contains(t, createdQP.Finalizers, aiGatewayControllerFinalizer)

	// Delete the QuotaPolicy.
	require.NoError(t, fakeClient.Delete(t.Context(), &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-delete", Namespace: namespace},
	}))

	// Reconcile after deletion should succeed (handles finalizer cleanup).
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "qp-delete"},
	})
	require.NoError(t, err)
}

// drainRouteEvents non-blockingly collects all pending "namespace/name" identifiers from the channel.
func drainRouteEvents(ch chan event.GenericEvent) []string {
	var got []string
	for {
		select {
		case ev := <-ch:
			got = append(got, ev.Object.GetNamespace()+"/"+ev.Object.GetName())
		default:
			return got
		}
	}
}

// TestQuotaPolicyController_Reconcile_Deletion_NotifiesRoutes verifies that deleting a QuotaPolicy
// notifies the AIGatewayRoutes targeting its backends via the finalizer callback, including routes in
// a different namespace that reference the backend cross-namespace. This is what forces those routes'
// derived HTTPRoutes to be re-reconciled (re-stamping the quota-policy-hash annotation) so Envoy
// Gateway re-translates without the deleted policy.
func TestQuotaPolicyController_Reconcile_Deletion_NotifiesRoutes(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	routeCh := make(chan event.GenericEvent, 100)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, routeCh)

	const nsA, nsB = "ns-a", "ns-b"

	// Backend in namespace A.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-x", Namespace: nsA},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-service", Port: ptrTo[gwapiv1.PortNumber](8080)},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Route in the same namespace as the policy (namespace A).
	routeA := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: nsA},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend-x"}}},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), routeA))

	// Route in a different namespace (B) referencing the backend in namespace A cross-namespace.
	routeB := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-b", Namespace: nsB},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
					{Name: "backend-x", Namespace: ptrTo(gwapiv1.Namespace(nsA))},
				}},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), routeB))

	// QuotaPolicy in namespace A targeting the backend.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-x", Namespace: nsA},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "backend-x"},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	// First reconcile adds the finalizer (and notifies routes via the create/update path).
	_, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: nsA, Name: "qp-x"},
	})
	require.NoError(t, err)
	// Discard the create/update-path notifications; we only care about the deletion path below.
	drainRouteEvents(routeCh)

	// Mark the QuotaPolicy for deletion. The finalizer keeps the object around so the finalizer
	// callback (which still has access to Spec.TargetRefs) runs on the next reconcile.
	require.NoError(t, fakeClient.Delete(t.Context(), qp))

	// Reconcile the deletion: the finalizer callback must notify both routes via the
	// BackendToReferencingAIGatewayRoute index.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: nsA, Name: "qp-x"},
	})
	require.NoError(t, err)

	notified := drainRouteEvents(routeCh)
	require.Contains(t, notified, nsA+"/route-a", "same-namespace route should be notified on deletion")
	require.Contains(t, notified, nsB+"/route-b", "cross-namespace route should be notified on deletion")
}

// quotaConfigCached reports whether the controller still holds a cached rate limit config for the
// given "namespace/name" QuotaPolicy key.
func quotaConfigCached(c *QuotaPolicyController, key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.configCache[key]
	return ok
}

// TestQuotaPolicyController_Reconcile_Deletion_ClearedFinalizer_CleansConfig covers a QuotaPolicy that
// disappears without the finalizer callback running (e.g. its finalizer is removed out of band). The
// not-found reconcile path must still drop the policy's entry from the rate limit config cache,
// otherwise its limits would linger until the controller restarts.
func TestQuotaPolicyController_Reconcile_Deletion_ClearedFinalizer_CleansConfig(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	const namespace, name = "default", "qp-cleared"
	cacheKey := NamespacedNameKey(namespace, name)

	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-cleared", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-service", Port: ptrTo[gwapiv1.PortNumber](8080)},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: gwapiv1.ObjectName(backend.Name)},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
	// First reconcile adds the finalizer and populates the config cache.
	_, err := c.Reconcile(t.Context(), req)
	require.NoError(t, err)
	require.True(t, quotaConfigCached(c, cacheKey), "config cache should hold the policy after sync")

	// Remove the finalizer out of band, then delete so the object is gone immediately and the
	// finalizer callback never runs.
	var current aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &current))
	current.Finalizers = nil
	require.NoError(t, fakeClient.Update(t.Context(), &current))
	require.NoError(t, fakeClient.Delete(t.Context(), &current))

	// The not-found reconcile path must clean the config cache.
	_, err = c.Reconcile(t.Context(), req)
	require.NoError(t, err)
	require.False(t, quotaConfigCached(c, cacheKey),
		"config cache entry must be removed when the policy is gone, even without the finalizer callback")
}

// TestQuotaPolicyController_Reconcile_Deletion_NotFound_NotifiesRoutes covers the not-found deletion
// path: with Spec.TargetRefs no longer available, every AIGatewayRoute referencing a backend in the
// policy's namespace must be notified (including cross-namespace routes), and each exactly once even
// when it references multiple backends in that namespace.
func TestQuotaPolicyController_Reconcile_Deletion_NotFound_NotifiesRoutes(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	routeCh := make(chan event.GenericEvent, 100)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, newTestRunner(t), routeCh)
	const nsA, nsB = "ns-a", "ns-b"

	for _, name := range []string{"backend-x", "backend-y"} {
		require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsA},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-service", Port: ptrTo[gwapiv1.PortNumber](8080)},
			},
		}))
	}

	// route-a references BOTH backends in ns-a: deletion finds it twice, but it must be notified once.
	routeA := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: nsA},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend-x"}, {Name: "backend-y"}},
		}}},
	}
	require.NoError(t, fakeClient.Create(t.Context(), routeA))

	// route-b lives in ns-b and references backend-x in ns-a cross-namespace.
	routeB := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-b", Namespace: nsB},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend-x", Namespace: ptrTo(gwapiv1.Namespace(nsA))}},
		}}},
	}
	require.NoError(t, fakeClient.Create(t.Context(), routeB))

	// route-c references a backend in a namespace unrelated to the deleted policy.
	// A full sweep must still notify it because the deleted policy's target namespace
	// is unknown after the object disappears.
	routeC := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-c", Namespace: "ns-c"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{
				Name: "backend-z", Namespace: ptrTo(gwapiv1.Namespace("ns-z")),
			}},
		}}},
	}
	require.NoError(t, fakeClient.Create(t.Context(), routeC))

	// The policy does not exist, so this reconcile takes the not-found deletion path.
	_, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: nsA, Name: "qp-gone"},
	})
	require.NoError(t, err)

	require.ElementsMatch(t, []string{nsA + "/route-a", nsB + "/route-b", "ns-c/route-c"}, drainRouteEvents(routeCh),
		"all routes must be notified exactly once via the full-sweep fallback")
}

// TestQuotaPolicyController_Reconcile_Deletion_NotFound_ReturnsNotifyError verifies that a failed
// lookup on the not-found deletion path propagates the error so the reconcile is requeued rather than
// silently leaving routes un-notified.
func TestQuotaPolicyController_Reconcile_Deletion_NotFound_ReturnsNotifyError(t *testing.T) {
	inner, ok := requireNewFakeClientWithIndexesForQuotaPolicy(t).(client.WithWatch)
	require.True(t, ok)
	listErr := errors.New("informer not synced")
	fakeClient := interceptor.NewClient(inner, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, isRouteList := list.(*aigv1b1.AIGatewayRouteList); isRouteList {
				return listErr
			}
			return cl.List(ctx, list, opts...)
		},
	})
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, newTestRunner(t), make(chan event.GenericEvent, 100))

	// The policy does not exist, so this reconcile takes the not-found deletion path.
	_, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "qp-gone"},
	})
	require.ErrorIs(t, err, listErr, "a failed lookup must requeue instead of leaving routes un-notified")
}

func TestQuotaPolicyController_Reconcile_MultipleBackends(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create two AIServiceBackends.
	for _, name := range []string{"backend-1", "backend-2"} {
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1a2.BackendObjectReference{
					Name: gwapiv1a2.ObjectName(name + "-svc"),
					Port: ptrTo[gwapiv1a2.PortNumber](8080),
				},
			},
		}
		require.NoError(t, fakeClient.Create(t.Context(), backend))
	}

	// Create a QuotaPolicy targeting both backends.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-multi-backend", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "backend-1",
				},
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "backend-2",
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 500, Duration: "1h"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	res, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "qp-multi-backend"},
	})
	require.NoError(t, err)
	require.False(t, res.Requeue)

	var updatedQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "qp-multi-backend"}, &updatedQP))
	require.Len(t, updatedQP.Status.Conditions, 1)
	require.Equal(t, aigv1a1.ConditionTypeAccepted, updatedQP.Status.Conditions[0].Type)
}

func TestQuotaPolicyController_Reconcile_PerModelQuotas(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-model", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1a2.BackendObjectReference{
				Name: "model-svc",
				Port: ptrTo[gwapiv1a2.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	modelName := "gpt-4"
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-per-model", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1a2.ObjectName(backend.Name),
				},
			},
			PerModelQuotas: []aigv1a1.PerModelQuota{
				{
					ModelName: &modelName,
					Quota: aigv1a1.QuotaDefinition{
						Mode:          aigv1a1.QuotaBucketModeShared,
						DefaultBucket: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	res, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "qp-per-model"},
	})
	require.NoError(t, err)
	require.False(t, res.Requeue)

	var updatedQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "qp-per-model"}, &updatedQP))
	require.Len(t, updatedQP.Status.Conditions, 1)
	require.Equal(t, aigv1a1.ConditionTypeAccepted, updatedQP.Status.Conditions[0].Type)
}

func TestQuotaPolicyController_BackendToQuotaPolicy(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create QuotaPolicies targeting different backends.
	qp1 := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-1", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "backend-a",
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	qp2 := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-2", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "backend-a",
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1h"},
			},
		},
	}
	qp3 := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-3", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "backend-b",
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 300, Duration: "1m"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp1))
	require.NoError(t, fakeClient.Create(t.Context(), qp2))
	require.NoError(t, fakeClient.Create(t.Context(), qp3))

	// Simulate an AIServiceBackend "backend-a" change.
	backendA := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-a", Namespace: namespace},
	}
	requests := c.BackendToQuotaPolicy(t.Context(), backendA)

	// Should return reconcile requests for qp-1 and qp-2 (both target backend-a).
	require.Len(t, requests, 2)
	names := make([]string, len(requests))
	for i, req := range requests {
		names[i] = req.Name
	}
	require.Contains(t, names, "qp-1")
	require.Contains(t, names, "qp-2")

	// Simulate an AIServiceBackend "backend-b" change.
	backendB := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-b", Namespace: namespace},
	}
	requests = c.BackendToQuotaPolicy(t.Context(), backendB)

	// Should return reconcile request for qp-3 only.
	require.Len(t, requests, 1)
	require.Equal(t, "qp-3", requests[0].Name)

	// Simulate a backend change for a backend that no QuotaPolicy targets.
	backendC := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-c", Namespace: namespace},
	}
	requests = c.BackendToQuotaPolicy(t.Context(), backendC)
	require.Empty(t, requests)
}

func TestQuotaPolicyController_Reconcile_MultiplePolicies(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create backends.
	for _, name := range []string{"be-1", "be-2"} {
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1a2.BackendObjectReference{
					Name: gwapiv1a2.ObjectName(name + "-svc"),
					Port: ptrTo[gwapiv1a2.PortNumber](8080),
				},
			},
		}
		require.NoError(t, fakeClient.Create(t.Context(), backend))
	}

	// Create two QuotaPolicies targeting different backends.
	qp1 := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-1", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "be-1"},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	qp2 := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-2", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "be-2"},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1h"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp1))
	require.NoError(t, fakeClient.Create(t.Context(), qp2))

	// Reconciling one policy should rebuild all configs (including both policies).
	res, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: "policy-1"},
	})
	require.NoError(t, err)
	require.False(t, res.Requeue)

	var updated aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: "policy-1"}, &updated))
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, aigv1a1.ConditionTypeAccepted, updated.Status.Conditions[0].Type)
}

func Test_quotaPolicyTargetRefsIndexFunc(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)

	// Create a QuotaPolicy targeting two backends.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "indexed-qp", Namespace: "default"},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "target-1"},
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "target-2"},
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "target-3", Namespace: ptrTo(gwapiv1a2.Namespace("backend-ns"))},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	// Look up by first target.
	var policies aigv1a1.QuotaPolicyList
	err := fakeClient.List(t.Context(), &policies,
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingQuotaPolicy: "target-1.default"})
	require.NoError(t, err)
	require.Len(t, policies.Items, 1)
	require.Equal(t, "indexed-qp", policies.Items[0].Name)

	err = fakeClient.List(t.Context(), &policies,
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingQuotaPolicy: "target-3.backend-ns"})
	require.NoError(t, err)
	require.Len(t, policies.Items, 1)
	require.Equal(t, "indexed-qp", policies.Items[0].Name)

	// Look up by second target.
	err = fakeClient.List(t.Context(), &policies,
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingQuotaPolicy: "target-2.default"})
	require.NoError(t, err)
	require.Len(t, policies.Items, 1)
	require.Equal(t, "indexed-qp", policies.Items[0].Name)

	// Look up by non-existent target.
	err = fakeClient.List(t.Context(), &policies,
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingQuotaPolicy: "nonexistent.default"})
	require.NoError(t, err)
	require.Empty(t, policies.Items)
}

func TestQuotaPolicyController_CrossNamespaceReferenceGrant(t *testing.T) {
	t.Run("rejects before reading unauthorized remote backend", func(t *testing.T) {
		fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
		controller := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, newTestRunner(t), make(chan event.GenericEvent, 10))

		backend := &aigv1b1.AIServiceBackend{ObjectMeta: metav1.ObjectMeta{
			Name: "provider", Namespace: "providers",
		}}
		policy := &aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "budget", Namespace: "platform"},
			Spec: aigv1a1.QuotaPolicySpec{TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
				Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend",
				Name: "provider", Namespace: ptrTo(gwapiv1a2.Namespace("providers")),
			}}},
		}
		require.NoError(t, fakeClient.Create(t.Context(), backend))
		require.NoError(t, fakeClient.Create(t.Context(), policy))

		_, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: "platform", Name: "budget"},
		})
		require.Error(t, err)
		var updated aigv1a1.QuotaPolicy
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(policy), &updated))
		require.Equal(t, aigv1a1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)
		require.Equal(t, aigv1a1.ConditionReasonRefNotPermitted, updated.Status.Conditions[0].Reason)
	})

	t.Run("accepts a valid remote grant", func(t *testing.T) {
		fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
		controller := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, newTestRunner(t), make(chan event.GenericEvent, 10))
		backend := &aigv1b1.AIServiceBackend{ObjectMeta: metav1.ObjectMeta{
			Name: "provider", Namespace: "providers",
		}}
		policy := &aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "budget", Namespace: "platform"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
					Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend",
					Name: "provider", Namespace: ptrTo(gwapiv1a2.Namespace("providers")),
				}},
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1h"},
				},
			},
		}
		grant := &gwapiv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: "allow-quota", Namespace: "providers"},
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{{
					Group: aiServiceBackendGroup, Kind: quotaPolicyKind, Namespace: "platform",
				}},
				To: []gwapiv1b1.ReferenceGrantTo{{
					Group: aiServiceBackendGroup, Kind: aiServiceBackendKind,
				}},
			},
		}
		require.NoError(t, fakeClient.Create(t.Context(), backend))
		require.NoError(t, fakeClient.Create(t.Context(), policy))
		require.NoError(t, fakeClient.Create(t.Context(), grant))

		_, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: "platform", Name: "budget"},
		})
		require.NoError(t, err)
		var updated aigv1a1.QuotaPolicy
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(policy), &updated))
		require.Equal(t, aigv1a1.ConditionTypeAccepted, updated.Status.Conditions[0].Type)
	})
}

func TestTargetNamespace(t *testing.T) {
	t.Run("defaults to policy namespace", func(t *testing.T) {
		ref := gwapiv1a2.NamespacedPolicyTargetReference{Name: "backend"}
		require.Equal(t, "policy-ns", TargetNamespace(ref, "policy-ns"))
	})

	t.Run("uses explicit target namespace", func(t *testing.T) {
		ref := gwapiv1a2.NamespacedPolicyTargetReference{
			Name:      "backend",
			Namespace: ptrTo(gwapiv1a2.Namespace("backend-ns")),
		}
		require.Equal(t, "backend-ns", TargetNamespace(ref, "policy-ns"))
	})

	t.Run("treats an empty namespace as omitted", func(t *testing.T) {
		ref := gwapiv1a2.NamespacedPolicyTargetReference{
			Name:      "backend",
			Namespace: ptrTo(gwapiv1a2.Namespace("")),
		}
		require.Equal(t, "policy-ns", TargetNamespace(ref, "policy-ns"))
	})
}

func ptrTo[T any](v T) *T {
	return &v
}

func TestQuotaPolicyController_Reconcile_InvalidCostExpression(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))
	namespace := "default"

	// Create an AIServiceBackend.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-invalid-cost", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1a2.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1a2.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	localTarget := gwapiv1a2.NamespacedPolicyTargetReference{
		Kind:  "AIServiceBackend",
		Group: "aigateway.envoyproxy.io",
		Name:  gwapiv1a2.ObjectName(backend.Name),
	}
	perModelQuota := func(modelName, costExpression *string) aigv1a1.PerModelQuota {
		return aigv1a1.PerModelQuota{
			ModelName: modelName,
			Quota: aigv1a1.QuotaDefinition{
				CostExpression: costExpression,
				DefaultBucket:  aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		}
	}
	createPolicy := func(t *testing.T, name string, target gwapiv1a2.NamespacedPolicyTargetReference, quotas ...aigv1a1.PerModelQuota) {
		t.Helper()
		require.NoError(t, fakeClient.Create(t.Context(), &aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs:     []gwapiv1a2.NamespacedPolicyTargetReference{target},
				PerModelQuotas: quotas,
			},
		}))
	}
	// reconcilePolicy reconciles the QuotaPolicy and returns the resulting condition and the error.
	reconcilePolicy := func(t *testing.T, name string) (metav1.Condition, error) {
		t.Helper()
		key := types.NamespacedName{Namespace: namespace, Name: name}
		_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: key})
		var updatedQP aigv1a1.QuotaPolicy
		require.NoError(t, fakeClient.Get(t.Context(), key, &updatedQP))
		require.Len(t, updatedQP.Status.Conditions, 1)
		return updatedQP.Status.Conditions[0], err
	}

	t.Run("reported in the status without failing the reconcile", func(t *testing.T) {
		const name = "qp-invalid-cost"
		createPolicy(t, name, localTarget,
			perModelQuota(ptrTo("model-ok"), ptrTo("input_tokens + output_tokens")),
			perModelQuota(ptrTo("model-bad"), ptrTo("total_tokens +")),
		)

		// The error is not returned: only a change to the policy can fix the expression.
		condition, err := reconcilePolicy(t, name)
		require.NoError(t, err)
		require.Equal(t, aigv1a1.ConditionTypeNotAccepted, condition.Type)
		require.Equal(t, metav1.ConditionFalse, condition.Status)
		require.Equal(t, "ReconciliationFailed", condition.Reason)
		require.Contains(t, condition.Message, `invalid costExpression for model "model-bad": cannot compile CEL expression`)
		require.NotContains(t, condition.Message, "model-ok")

		// The rate limit config of both models is still served.
		c.mu.RLock()
		configs := c.configCache[NamespacedNameKey(namespace, name)]
		c.mu.RUnlock()
		require.Len(t, configs, 1)
		require.Len(t, configs[0].Descriptors, 1)
		require.Len(t, configs[0].Descriptors[0].Descriptors, 2)

		// Fixing the expression makes the policy accepted.
		var qp aigv1a1.QuotaPolicy
		require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: name}, &qp))
		qp.Spec.PerModelQuotas[1].Quota.CostExpression = ptrTo("total_tokens * 2u")
		require.NoError(t, fakeClient.Update(t.Context(), &qp))
		condition, err = reconcilePolicy(t, name)
		require.NoError(t, err)
		require.Equal(t, aigv1a1.ConditionTypeAccepted, condition.Type)
	})

	for _, tc := range []struct {
		name           string
		modelName      *string
		costExpression *string
		// expErr is the part of the CEL error expected in the message. Empty means the policy is accepted.
		expErr string
	}{
		{name: "unset uses the default", modelName: ptrTo("model"), costExpression: nil},
		{name: "valid", modelName: ptrTo("model"), costExpression: ptrTo("total_tokens * 2u")},
		{name: "quota without a model name is skipped", modelName: nil, costExpression: ptrTo("total_tokens +")},
		{name: "empty", modelName: ptrTo("model"), costExpression: ptrTo(""), expErr: "cannot compile CEL expression"},
		{name: "undeclared variable", modelName: ptrTo("model"), costExpression: ptrTo("prompt_tokens + completion_tokens"), expErr: "undeclared reference to 'prompt_tokens'"},
		{name: "literal without the u suffix", modelName: ptrTo("model"), costExpression: ptrTo("total_tokens * 2"), expErr: "found no matching overload"},
		{name: "result is not an integer", modelName: ptrTo("model"), costExpression: ptrTo("double(total_tokens) * 0.5"), expErr: "CEL expression result is not an integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "qp-" + strings.ReplaceAll(tc.name, " ", "-")
			createPolicy(t, name, localTarget, perModelQuota(tc.modelName, tc.costExpression))

			condition, err := reconcilePolicy(t, name)
			require.NoError(t, err)
			if tc.expErr == "" {
				require.Equal(t, aigv1a1.ConditionTypeAccepted, condition.Type)
				return
			}
			require.Equal(t, aigv1a1.ConditionTypeNotAccepted, condition.Type)
			require.Contains(t, condition.Message, `invalid costExpression for model "model": `)
			require.Contains(t, condition.Message, tc.expErr)
		})
	}

	t.Run("message is bounded", func(t *testing.T) {
		// The CEL error quotes the expression. 512 is the bound for one expression and 32768 is the
		// longest condition message the API server accepts.
		longExpression := ptrTo(strings.Repeat("total_tokens + ", 20) + "+")

		const single = "qp-long-cost"
		createPolicy(t, single, localTarget, perModelQuota(ptrTo("model"), longExpression))
		condition, err := reconcilePolicy(t, single)
		require.NoError(t, err)
		require.Equal(t, aigv1a1.ConditionTypeNotAccepted, condition.Type)
		require.Len(t, condition.Message, len(`invalid costExpression for model "model": `)+512+len("..."))

		const many = "qp-many-long-costs"
		quotas := make([]aigv1a1.PerModelQuota, 128)
		for i := range quotas {
			quotas[i] = perModelQuota(ptrTo(fmt.Sprintf("model-%03d", i)), longExpression)
		}
		createPolicy(t, many, localTarget, quotas...)
		condition, err = reconcilePolicy(t, many)
		require.NoError(t, err)
		require.Equal(t, aigv1a1.ConditionTypeNotAccepted, condition.Type)
		require.Contains(t, condition.Message, `invalid costExpression for model "model-000": `)
		require.Len(t, condition.Message, 32768)

		// A character that straddles a bound is dropped rather than cut in half. The two expressions differ
		// by one byte, so the bound for one expression falls inside a two-byte character of exactly one of them.
		var lengths []int
		for i, padding := range []string{"", " "} {
			name := fmt.Sprintf("qp-multibyte-cost-%d", i)
			createPolicy(t, name, localTarget, perModelQuota(ptrTo("model"), ptrTo(padding+`"`+strings.Repeat("é", 400)+`" +`)))
			condition, err = reconcilePolicy(t, name)
			require.NoError(t, err)
			lengths = append(lengths, len(condition.Message)-len(`invalid costExpression for model "model": `)-len("..."))
		}
		require.ElementsMatch(t, []int{511, 512}, lengths)

		// The bound of the whole message falls inside a two-byte character of this model name.
		const multibyteModel = "qp-multibyte-model"
		createPolicy(t, multibyteModel, localTarget, perModelQuota(ptrTo("m"+strings.Repeat("é", 20000)), ptrTo("total_tokens +")))
		condition, err = reconcilePolicy(t, multibyteModel)
		require.NoError(t, err)
		require.Len(t, condition.Message, 32767)
	})

	t.Run("unauthorized target keeps its reason", func(t *testing.T) {
		const name = "qp-unauthorized-invalid-cost"
		remoteTarget := localTarget
		remoteTarget.Name = "provider"
		remoteTarget.Namespace = ptrTo(gwapiv1a2.Namespace("providers"))
		createPolicy(t, name, remoteTarget, perModelQuota(ptrTo("model"), ptrTo("total_tokens +")))

		// The unauthorized target is still returned, so that a later ReferenceGrant is picked up.
		condition, err := reconcilePolicy(t, name)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "invalid costExpression")
		require.Equal(t, aigv1a1.ConditionTypeNotAccepted, condition.Type)
		require.Equal(t, aigv1a1.ConditionReasonRefNotPermitted, condition.Reason)
		require.Contains(t, condition.Message, "is not permitted")
		require.Contains(t, condition.Message, `invalid costExpression for model "model": `)
	})
}

func TestQuotaPolicyController_Reconcile_StatusUpdateError(t *testing.T) {
	rateLimitRunner := newTestRunner(t)
	namespace := "default"
	const policyName = "qp-status-error"

	for _, tc := range []struct {
		name           string
		costExpression string
		// statusUpdateErr is returned by the status update. secondGetErr is returned by the second read of
		// the policy: Reconcile reads the policy once, so the second read is the one before the status update.
		statusUpdateErr, secondGetErr error
		// expErr is the error expected from Reconcile. Empty means that it succeeds.
		expErr string
	}{
		{
			name:            "accepted policy",
			costExpression:  "total_tokens * 2u",
			statusUpdateErr: errors.New("status update failed"),
			expErr:          "failed to update QuotaPolicy status: status update failed",
		},
		{
			name:            "invalid cost expression",
			costExpression:  "total_tokens +",
			statusUpdateErr: errors.New("status update failed"),
			expErr:          "failed to update QuotaPolicy status: status update failed",
		},
		{
			name:           "policy cannot be read",
			costExpression: "total_tokens * 2u",
			secondGetErr:   errors.New("get failed"),
			expErr:         "failed to update QuotaPolicy status: get failed",
		},
		{
			name:           "policy deleted in the meantime",
			costExpression: "total_tokens * 2u",
			secondGetErr:   apierrors.NewNotFound(aigv1a1.Resource("quotapolicies"), policyName),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := requireNewFakeClientWithIndexesForQuotaPolicy(t)
			policyGets := 0
			fakeClient := interceptor.NewClient(inner.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, isPolicy := obj.(*aigv1a1.QuotaPolicy); isPolicy && tc.secondGetErr != nil {
						if policyGets++; policyGets == 2 {
							return tc.secondGetErr
						}
					}
					return cl.Get(ctx, key, obj, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if tc.statusUpdateErr != nil {
						return tc.statusUpdateErr
					}
					return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
				},
			})
			c := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, make(chan event.GenericEvent, 100))

			// Create an AIServiceBackend.
			backend := &aigv1b1.AIServiceBackend{
				ObjectMeta: metav1.ObjectMeta{Name: "backend-status-error", Namespace: namespace},
				Spec: aigv1b1.AIServiceBackendSpec{
					BackendRef: gwapiv1a2.BackendObjectReference{
						Name: "some-service",
						Port: ptrTo[gwapiv1a2.PortNumber](8080),
					},
				},
			}
			require.NoError(t, fakeClient.Create(t.Context(), backend))

			// Create a QuotaPolicy targeting the backend.
			qp := &aigv1a1.QuotaPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: policyName, Namespace: namespace},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{
						{
							Kind:  "AIServiceBackend",
							Group: "aigateway.envoyproxy.io",
							Name:  gwapiv1a2.ObjectName(backend.Name),
						},
					},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptrTo("model"),
							Quota: aigv1a1.QuotaDefinition{
								CostExpression: ptrTo(tc.costExpression),
								DefaultBucket:  aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
							},
						},
					},
				},
			}
			require.NoError(t, fakeClient.Create(t.Context(), qp))

			_, err := c.Reconcile(t.Context(), reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: namespace, Name: policyName},
			})
			if tc.expErr == "" {
				require.NoError(t, err)
				return
			}
			// Reconcile should fail so that the status update is retried: nothing else would write it again.
			require.EqualError(t, err, tc.expErr)
		})
	}
}
