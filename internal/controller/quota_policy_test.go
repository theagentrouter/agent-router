// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

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
		WithStatusSubresource(&aigv1b1.AIServiceBackend{}).
		WithStatusSubresource(&aigv1b1.AIGatewayRoute{})
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
			BackendRef: gwapiv1.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy targeting the backend.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "myquotapolicy", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName("mybackend"),
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

	// Reconcile a non-existent QuotaPolicy - this triggers the deletion path
	// (rebuilds all configs, which should succeed with no policies).
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
			BackendRef: gwapiv1.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy targeting the backend with valid config.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-sync-error", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName(backend.Name),
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
			BackendRef: gwapiv1.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy with an invalid duration.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-invalid-duration", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName(backend.Name),
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
			BackendRef: gwapiv1.BackendObjectReference{
				Name: "some-service",
				Port: ptrTo[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	// Create a QuotaPolicy.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-delete", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName(backend.Name),
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
				BackendRef: gwapiv1.BackendObjectReference{
					Name: gwapiv1.ObjectName(name + "-svc"),
					Port: ptrTo[gwapiv1.PortNumber](8080),
				},
			},
		}
		require.NoError(t, fakeClient.Create(t.Context(), backend))
	}

	// Create a QuotaPolicy targeting both backends.
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-multi-backend", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
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
			BackendRef: gwapiv1.BackendObjectReference{
				Name: "model-svc",
				Port: ptrTo[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	modelName := "gpt-4"
	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-per-model", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName(backend.Name),
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
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
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
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
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
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
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
				BackendRef: gwapiv1.BackendObjectReference{
					Name: gwapiv1.ObjectName(name + "-svc"),
					Port: ptrTo[gwapiv1.PortNumber](8080),
				},
			},
		}
		require.NoError(t, fakeClient.Create(t.Context(), backend))
	}

	// Create two QuotaPolicies targeting different backends.
	qp1 := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-1", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
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
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
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
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "target-1"},
				{Kind: "AIServiceBackend", Group: "aigateway.envoyproxy.io", Name: "target-2"},
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

// httpRouteHasEnvoyGatewayObservableChange reports whether Envoy Gateway's HTTPRoute
// watch predicate (generation, labels, or annotations) would fire.
func httpRouteHasEnvoyGatewayObservableChange(before, after *gwapiv1.HTTPRoute) bool {
	return before.Generation != after.Generation ||
		!maps.Equal(before.Labels, after.Labels) ||
		!maps.Equal(before.Annotations, after.Annotations)
}

func TestQuotaPolicyUpdate_RequeuesAIGatewayRouteAndUpdatesDerivedHTTPRoute(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	rateLimitRunner := newTestRunner(t)
	routeCh := make(chan event.GenericEvent, 100)
	gwCh := make(chan event.GenericEvent, 100)
	qpController := NewQuotaPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, rateLimitRunner, routeCh)
	routeController := NewAIGatewayRouteController(fakeClient, fake2.NewClientset(), ctrl.Log, gwCh, "/")
	namespace := "default"

	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "quota-backend", Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{
				Name: "quota-backend-svc",
				Port: ptrTo[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "quota-route", Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: backend.Name, Weight: ptrTo[int32](1)},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))

	qp := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "quota-policy", Namespace: namespace},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName(backend.Name),
				},
			},
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), qp))

	_, err := qpController.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: qp.Name},
	})
	require.NoError(t, err)
	require.Equal(t, route.Name, (<-routeCh).Object.GetName(), "initial QuotaPolicy reconcile must requeue the AIGatewayRoute")

	_, err = routeController.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: route.Name},
	})
	require.NoError(t, err)

	var httpRouteBefore gwapiv1.HTTPRoute
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: route.Name}, &httpRouteBefore))

	var updatedQP aigv1a1.QuotaPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: qp.Name}, &updatedQP))
	updatedQP.Spec.ServiceQuota.Quota.Limit = 250
	updatedQP.Spec.ServiceQuota.Quota.Duration = "1h"
	require.NoError(t, fakeClient.Update(t.Context(), &updatedQP))

	_, err = qpController.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: qp.Name},
	})
	require.NoError(t, err)

	select {
	case ev := <-routeCh:
		require.Equal(t, route.Name, ev.Object.GetName())
		require.Equal(t, namespace, ev.Object.GetNamespace())
	default:
		t.Fatal("QuotaPolicy update must requeue the AIGatewayRoute")
	}

	_, err = routeController.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: route.Name},
	})
	require.NoError(t, err)

	var httpRouteAfter gwapiv1.HTTPRoute
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: route.Name}, &httpRouteAfter))

	require.True(t, httpRouteHasEnvoyGatewayObservableChange(&httpRouteBefore, &httpRouteAfter),
		"QuotaPolicy update requeued the route but the derived HTTPRoute has no generation/label/annotation change Envoy Gateway would observe; before annotations=%v labels=%v gen=%d after annotations=%v labels=%v gen=%d",
		httpRouteBefore.Annotations, httpRouteBefore.Labels, httpRouteBefore.Generation,
		httpRouteAfter.Annotations, httpRouteAfter.Labels, httpRouteAfter.Generation)
}

func ptrTo[T any](v T) *T {
	return &v
}
