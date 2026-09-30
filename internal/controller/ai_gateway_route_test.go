// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestAIGatewayRouteController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, "/v1")

	err := fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "mytarget", Namespace: "default"}})
	require.NoError(t, err)
	err = fakeClient.Create(t.Context(), &aigv1b1.AIGatewayRoute{ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)

	// Do it for the second time with a slightly different configuration.
	var current aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "myroute"}, &current)
	// Make sure the finalizer is added.
	require.NoError(t, err)
	require.Contains(t, current.Finalizers, aiGatewayControllerFinalizer, "Finalizer should be added")
	current.Spec.ParentRefs = []gwapiv1a2.ParentReference{{Name: "mytarget"}}
	err = fakeClient.Update(t.Context(), &current)
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)

	var updated aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "myroute"}, &updated)
	require.NoError(t, err)

	require.Equal(t, "myroute", updated.Name)
	require.Equal(t, "default", updated.Namespace)
	require.Len(t, updated.Spec.ParentRefs, 1)
	require.Equal(t, "mytarget", string(updated.Spec.ParentRefs[0].Name))

	// Test the case where the AIGatewayRoute is being deleted.
	err = fakeClient.Delete(t.Context(), &aigv1b1.AIGatewayRoute{ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)
}

func TestAIGatewayRouteController_Reconcile_SyncError(t *testing.T) {
	// Test error path where syncAIGatewayRoute fails.
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, "/v1")

	// Create a route without creating the filter to cause sync error.
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "errorroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "nonexistent", Weight: ptr.To[int32](1)},
					},
				},
			},
		},
	}
	err := fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// This should fail during sync because backend doesn't exist.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "errorroute"}})
	require.Error(t, err)

	// Check that status was updated to NotAccepted.
	var updatedRoute aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "errorroute"}, &updatedRoute)
	require.NoError(t, err)
	require.Len(t, updatedRoute.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updatedRoute.Status.Conditions[0].Type)
}

func requireNewFakeClientWithIndexes(t *testing.T) client.Client {
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1b1.AIGatewayRoute{}).
		WithStatusSubresource(&aigv1b1.AIServiceBackend{}).
		WithStatusSubresource(&aigv1b1.BackendSecurityPolicy{})
	err := ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	})
	require.NoError(t, err)
	return builder.Build()
}

func TestAIGatewayRouterController_syncAIGatewayRoute(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	s := NewAIGatewayRouteController(fakeClient, kube, logr.Discard(), eventCh.Ch, "/")
	require.NotNil(t, s)

	for _, backend := range []*aigv1b1.AIServiceBackend{
		{ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: "ns1"}, Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace]("ns1")},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: "ns1"}, Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend2", Namespace: ptr.To[gwapiv1.Namespace]("ns1")},
		}},
	} {
		err := fakeClient.Create(t.Context(), backend, &client.CreateOptions{})
		require.NoError(t, err)
	}

	t.Run("existing", func(t *testing.T) {
		route := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns1"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple", Weight: ptr.To[int32](1)}, {Name: "orange", Weight: ptr.To[int32](1)}},
					},
				},
			},
		}
		err := fakeClient.Create(t.Context(), route, &client.CreateOptions{})
		require.NoError(t, err)
		httpRoute := &gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns1", Labels: map[string]string{managedByLabel: "envoy-ai-gateway"}},
			Spec:       gwapiv1.HTTPRouteSpec{},
		}
		err = fakeClient.Create(t.Context(), httpRoute, &client.CreateOptions{})
		require.NoError(t, err)

		// Then sync, which should update the HTTPRoute.
		require.NoError(t, s.syncAIGatewayRoute(t.Context(), route))
		var updatedHTTPRoute gwapiv1.HTTPRoute
		err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "myroute", Namespace: "ns1"}, &updatedHTTPRoute)
		require.NoError(t, err)
		require.Len(t, updatedHTTPRoute.Spec.Rules, 2) // 1 rule + 1 for the default rule.
		require.Len(t, updatedHTTPRoute.Spec.Rules[0].BackendRefs, 2)
		require.Equal(t, "some-backend1", string(updatedHTTPRoute.Spec.Rules[0].BackendRefs[0].Name))
		require.Equal(t, "some-backend2", string(updatedHTTPRoute.Spec.Rules[0].BackendRefs[1].Name))
		// Defaulting to the empty path, which shouldn't reach in practice.
		require.Empty(t, updatedHTTPRoute.Spec.Rules[1].BackendRefs)
		require.Equal(t, "/", *updatedHTTPRoute.Spec.Rules[1].Matches[0].Path.Value)

		// Check per AIGatewayRoute has the default host rewrite filter.
		var f egv1a1.HTTPRouteFilter
		hostRewriteName := fmt.Sprintf("%s-%s", hostRewriteHTTPFilterName, route.Name)
		err = s.client.Get(t.Context(), client.ObjectKey{Name: hostRewriteName, Namespace: "ns1"}, &f)
		require.NoError(t, err)
		require.Equal(t, hostRewriteName, f.Name)
		ok, _ := ctrlutil.HasOwnerReference(f.OwnerReferences, route, fakeClient.Scheme())
		require.True(t, ok, "expected hostRewriteFilter to have owner reference to AIGatewayRoute")

		// Also check per AIGatewayRoute has default route not found response filter.
		var notFoundFilter egv1a1.HTTPRouteFilter
		notFoundName := fmt.Sprintf("%s-%s", routeNotFoundResponseHTTPFilterName, route.Name)
		err = s.client.Get(t.Context(), client.ObjectKey{Name: notFoundName, Namespace: "ns1"}, &notFoundFilter)
		require.NoError(t, err)
		require.Equal(t, notFoundName, notFoundFilter.Name)
		ok, _ = ctrlutil.HasOwnerReference(notFoundFilter.OwnerReferences, route, fakeClient.Scheme())
		require.True(t, ok, "expected notFoundFilter to have owner reference to AIGatewayRoute")
	})
}

func Test_newHTTPRoute(t *testing.T) {
	for _, ns := range []string{"", "ns1"} {
		t.Run(fmt.Sprintf("namespace-%s", ns), func(t *testing.T) {
			var refNs *gwapiv1.Namespace
			if ns != "" {
				refNs = ptr.To(gwapiv1.Namespace(ns))
			}

			var (
				timeout1       gwapiv1.Duration = "30s"
				timeout2       gwapiv1.Duration = "60s"
				defaultTimeout gwapiv1.Duration = "60s"
			)
			fakeClient := requireNewFakeClientWithIndexes(t)
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			s := NewAIGatewayRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch, "/")
			httpRoute := &gwapiv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: ns},
				Spec:       gwapiv1.HTTPRouteSpec{},
			}
			aiGatewayRoute := &aigv1b1.AIGatewayRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: ns},
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
							BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple", Weight: ptr.To[int32](100)}},
							Matches: []aigv1b1.AIGatewayRouteRuleMatch{
								{Headers: []gwapiv1.HTTPHeaderMatch{
									{Name: "x-test", Value: "rule-0"},
								}},
							},
						},
						{
							Name: ptr.To[gwapiv1.SectionName]("weighted-backends"),
							BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
								{Name: "orange", Weight: ptr.To[int32](100), Priority: ptr.To[uint32](0)},
								{Name: "apple", Weight: ptr.To[int32](100), Priority: ptr.To[uint32](1)},
								{Name: "pineapple", Weight: ptr.To[int32](100), Priority: ptr.To[uint32](2)},
							},
							Matches: []aigv1b1.AIGatewayRouteRuleMatch{
								{Headers: []gwapiv1.HTTPHeaderMatch{
									{Name: "x-test", Value: "rule-1"},
								}},
							},
						},
						{
							BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "foo", Weight: ptr.To[int32](1)}},
							Timeouts:    &gwapiv1.HTTPRouteTimeouts{Request: &timeout1, BackendRequest: &timeout2},
							Matches: []aigv1b1.AIGatewayRouteRuleMatch{
								{Headers: []gwapiv1.HTTPHeaderMatch{
									{Name: "x-test", Value: "rule-2"},
								}},
							},
						},
					},
				},
			}

			for _, backend := range []*aigv1b1.AIServiceBackend{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: ns},
					Spec: aigv1b1.AIServiceBackendSpec{
						BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: refNs},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: ns},
					Spec: aigv1b1.AIServiceBackendSpec{
						BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend2", Namespace: refNs},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pineapple", Namespace: ns},
					Spec: aigv1b1.AIServiceBackendSpec{
						BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend3", Namespace: refNs},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: ns},
					Spec: aigv1b1.AIServiceBackendSpec{
						BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend4", Namespace: refNs},
					},
				},
			} {
				err := s.client.Create(t.Context(), backend, &client.CreateOptions{})
				require.NoError(t, err)
			}
			err := s.newHTTPRoute(t.Context(), httpRoute, aiGatewayRoute)
			require.NoError(t, err)

			rewriteFilters := []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterExtensionRef,
				ExtensionRef: &gwapiv1.LocalObjectReference{
					Group: "gateway.envoyproxy.io",
					Kind:  "HTTPRouteFilter",
					Name:  gwapiv1.ObjectName(getHostRewriteFilterName("myroute")),
				},
			}}
			expPath := &gwapiv1.HTTPPathMatch{Value: ptr.To("/")}
			expRules := []gwapiv1.HTTPRouteRule{
				{
					Matches: []gwapiv1.HTTPRouteMatch{
						{Headers: []gwapiv1.HTTPHeaderMatch{{Name: "x-test", Value: "rule-0"}}, Path: expPath},
					},
					BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: refNs}, Weight: ptr.To[int32](100)}}},
					Timeouts:    &gwapiv1.HTTPRouteTimeouts{Request: &defaultTimeout},
					Filters:     rewriteFilters,
				},
				{
					Name: ptr.To[gwapiv1.SectionName]("weighted-backends"),
					Matches: []gwapiv1.HTTPRouteMatch{
						{Headers: []gwapiv1.HTTPHeaderMatch{{Name: "x-test", Value: "rule-1"}}, Path: expPath},
					},
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "some-backend2", Namespace: refNs}, Weight: ptr.To[int32](100)}},
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: refNs}, Weight: ptr.To[int32](100)}},
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "some-backend3", Namespace: refNs}, Weight: ptr.To[int32](100)}},
					},
					Timeouts: &gwapiv1.HTTPRouteTimeouts{Request: &defaultTimeout},
					Filters:  rewriteFilters,
				},
				{
					Matches: []gwapiv1.HTTPRouteMatch{
						{Headers: []gwapiv1.HTTPHeaderMatch{{Name: "x-test", Value: "rule-2"}}, Path: expPath},
					},
					BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "some-backend4", Namespace: refNs}, Weight: ptr.To[int32](1)}}},
					Timeouts:    &gwapiv1.HTTPRouteTimeouts{Request: &timeout1, BackendRequest: &timeout2},
					Filters:     rewriteFilters,
				},
				{
					// The default rule.
					Name:    ptr.To[gwapiv1.SectionName]("route-not-found"),
					Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Value: ptr.To("/")}}},
					Filters: []gwapiv1.HTTPRouteFilter{
						{
							Type: gwapiv1.HTTPRouteFilterExtensionRef,
							ExtensionRef: &gwapiv1.LocalObjectReference{
								Group: "gateway.envoyproxy.io",
								Kind:  "HTTPRouteFilter",
								Name:  gwapiv1.ObjectName(getRouteNotFoundFilterName("myroute")),
							},
						},
					},
				},
			}
			require.Equal(t, expRules, httpRoute.Spec.Rules)
		})
	}
}

func TestAIGatewayRouteController_updateAIGatewayRouteStatus(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	s := NewAIGatewayRouteController(fakeClient, kube, logr.Discard(), eventCh.Ch, "/v1")

	r := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route1",
			Namespace: "default",
		},
	}
	err := s.client.Create(t.Context(), r, &client.CreateOptions{})
	require.NoError(t, err)

	s.updateAIGatewayRouteStatus(t.Context(), r, aigv1b1.ConditionTypeNotAccepted, "err")

	var updatedRoute aigv1b1.AIGatewayRoute
	err = s.client.Get(t.Context(), client.ObjectKey{Name: "route1", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	require.Len(t, updatedRoute.Status.Conditions, 1)
	require.Equal(t, "err", updatedRoute.Status.Conditions[0].Message)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updatedRoute.Status.Conditions[0].Type)

	s.updateAIGatewayRouteStatus(t.Context(), &updatedRoute, aigv1b1.ConditionTypeAccepted, "ok")
	err = s.client.Get(t.Context(), client.ObjectKey{Name: "route1", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	require.Len(t, updatedRoute.Status.Conditions, 1)
	require.Equal(t, "ok", updatedRoute.Status.Conditions[0].Message)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, updatedRoute.Status.Conditions[0].Type)
}

func Test_buildPriorityAnnotation(t *testing.T) {
	rules := []aigv1b1.AIGatewayRouteRule{
		{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "orange", Weight: ptr.To[int32](100), Priority: ptr.To[uint32](0)},
				{Name: "apple", Weight: ptr.To[int32](100), Priority: ptr.To[uint32](1)},
				{Name: "pineapple", Weight: ptr.To[int32](100), Priority: ptr.To[uint32](2)},
			},
		},
	}
	annotation := buildPriorityAnnotation(rules)
	require.Equal(t, "0:orange:0,0:apple:1,0:pineapple:2", annotation)
}

func Test_buildQuotaPolicyAnnotation(t *testing.T) {
	require.Empty(t, buildQuotaPolicyAnnotation(nil))

	p1 := aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-b", Namespace: "default"},
		Spec: aigv1a1.QuotaPolicySpec{
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			},
		},
	}
	p2 := aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "qp-a", Namespace: "default"},
		Spec: aigv1a1.QuotaPolicySpec{
			ServiceQuota: aigv1a1.ServiceQuotaDefinition{
				Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1h"},
			},
		},
	}

	sorted := buildQuotaPolicyAnnotation([]aigv1a1.QuotaPolicy{p1, p2})
	reversed := buildQuotaPolicyAnnotation([]aigv1a1.QuotaPolicy{p2, p1})
	require.Equal(t, sorted, reversed)
	require.True(t, strings.HasPrefix(sorted, "default/qp-a:"))
	require.Contains(t, sorted, ",default/qp-b:")

	updated := p1
	updated.Spec.ServiceQuota.Quota.Limit = 999
	require.NotEqual(t, buildQuotaPolicyAnnotation([]aigv1a1.QuotaPolicy{p1}), buildQuotaPolicyAnnotation([]aigv1a1.QuotaPolicy{updated}))
}

func TestAIGatewayRouteController_backend(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, kube, ctrl.Log, eventCh.Ch, "/v1")

	// Test successful backend retrieval.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: "default"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "backend1"},
		},
	}
	err := fakeClient.Create(t.Context(), backend)
	require.NoError(t, err)

	result, err := c.backend(t.Context(), "default", "test-backend")
	require.NoError(t, err)
	require.Equal(t, "test-backend", result.Name)

	// Test backend not found error.
	_, err = c.backend(t.Context(), "default", "nonexistent")
	require.Error(t, err)
}

func TestAIGatewayRouterController_syncGateway_notFound(t *testing.T) { // This is mostly for coverage.
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	s := NewAIGatewayRouteController(fakeClient, kube, logr.Discard(), eventCh.Ch, "/v1")
	err := s.syncGateway(context.Background(), "ns", "non-exist")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestAIGatewayRouteController_Reconcile_GatewayNotFound(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, kube, ctrl.Log, eventCh.Ch, "/v1")

	// Create AIGatewayRoute referencing a non-existent gateway.
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "broken-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("non-existent")}},
		},
	}
	err := fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Reconcile should fail and mark status as NotAccepted.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "broken-route"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-existent")

	// Verify the AIGatewayRoute status is NotAccepted.
	var current aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "broken-route"}, &current)
	require.NoError(t, err)
	require.Len(t, current.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, current.Status.Conditions[0].Type)
	require.Contains(t, current.Status.Conditions[0].Message, "not found")

	// create the gateway now so that the reconcile succeeds.
	err = fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "non-existent", Namespace: "default"}})
	require.NoError(t, err)

	// Reconcile should succeed.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "broken-route"}})
	require.NoError(t, err)

	// Verify the AIGatewayRoute status is Accepted.
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "broken-route"}, &current)
	require.NoError(t, err)
	require.Len(t, current.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, current.Status.Conditions[0].Type)
	require.Contains(t, current.Status.Conditions[0].Message, "reconciled successfully")
}

func TestAIGatewayRouteController_syncGateway_DeletionWithMissingGateway(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, kube, ctrl.Log, eventCh.Ch, "/v1")

	// Create the gateway first so that the initial reconcile succeeds.
	err := fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "temp-gw", Namespace: "default"}})
	require.NoError(t, err)

	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-to-delete",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("temp-gw")}},
		},
	}
	err = fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Initial reconcile to add the finalizer.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "route-to-delete"}})
	require.NoError(t, err)

	// Verify finalizer is present.
	var current aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "route-to-delete"}, &current)
	require.NoError(t, err)
	require.Contains(t, current.Finalizers, aiGatewayControllerFinalizer)

	// Now delete the gateway (simulating it being removed before the AIGatewayRoute).
	err = fakeClient.Delete(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "temp-gw", Namespace: "default"}})
	require.NoError(t, err)

	// Delete the AIGatewayRoute.
	err = fakeClient.Delete(t.Context(), &current)
	require.NoError(t, err)

	// Reconcile the deletion — should succeed even though the gateway is gone.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "route-to-delete"}})
	require.NoError(t, err)

	// Verify the AIGatewayRoute finalizer has been removed (object should be gone or have no finalizer).
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "route-to-delete"}, &current)
	if err == nil {
		require.NotContains(t, current.Finalizers, aiGatewayControllerFinalizer)
	} else {
		require.True(t, apierrors.IsNotFound(err))
	}
}

func Test_newHTTPRoute_InferencePool(t *testing.T) {
	c := requireNewFakeClientWithIndexes(t)

	// Create an AIGatewayRoute with InferencePool backend.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inference-route",
			Namespace: "test-ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
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

	controller := &AIGatewayRouteController{client: c}
	httpRoute := &gwapiv1.HTTPRoute{}

	err := controller.newHTTPRoute(context.Background(), httpRoute, aiGatewayRoute)
	require.NoError(t, err)

	// Verify HTTPRoute has correct backend reference for InferencePool.
	// Note: newHTTPRoute always adds a default "unreachable" rule, so we expect 2 rules total.
	require.Len(t, httpRoute.Spec.Rules, 2)
	require.Len(t, httpRoute.Spec.Rules[0].BackendRefs, 1)

	// Check the first rule (our InferencePool rule).
	backendRef := httpRoute.Spec.Rules[0].BackendRefs[0]
	require.Equal(t, "inference.networking.k8s.io", string(*backendRef.Group))
	require.Equal(t, "InferencePool", string(*backendRef.Kind))
	require.Equal(t, "test-inference-pool", string(backendRef.Name))
	require.Equal(t, "test-ns", string(*backendRef.Namespace))
	require.Equal(t, int32(100), *backendRef.Weight)

	// Check the second rule is the default "route-not-found" rule.
	require.Equal(t, "route-not-found", string(*httpRoute.Spec.Rules[1].Name))
	require.Empty(t, httpRoute.Spec.Rules[1].BackendRefs) // No backend refs for default rule.
}

func Test_newHTTPRoute_InferencePool_CrossNamespace(t *testing.T) {
	newRoute := func(poolNamespace string) *aigv1b1.AIGatewayRoute {
		return &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "inference-route",
				Namespace: "gw",
			},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{
								Name:      "my-pool",
								Namespace: ptr.To(gwapiv1.Namespace(poolNamespace)),
								Group:     ptr.To("inference.networking.k8s.io"),
								Kind:      ptr.To("InferencePool"),
								Weight:    ptr.To(int32(100)),
							},
						},
					},
				},
			},
		}
	}

	referenceGrant := &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-gw", Namespace: "default"},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{
				Group:     aiServiceBackendGroup,
				Kind:      aiGatewayRouteKind,
				Namespace: "gw",
			}},
			To: []gwapiv1b1.ReferenceGrantTo{{
				Group: inferencePoolGroup,
				Kind:  inferencePoolKind,
			}},
		},
	}

	t.Run("cross-namespace with ReferenceGrant honors the pool namespace", func(t *testing.T) {
		c := requireNewFakeClientWithIndexes(t)
		require.NoError(t, c.Create(t.Context(), referenceGrant))

		controller := &AIGatewayRouteController{client: c, referenceGrantValidator: newReferenceGrantValidator(c)}
		httpRoute := &gwapiv1.HTTPRoute{}
		require.NoError(t, controller.newHTTPRoute(t.Context(), httpRoute, newRoute("default")))

		require.Len(t, httpRoute.Spec.Rules, 2)
		require.Len(t, httpRoute.Spec.Rules[0].BackendRefs, 1)
		backendRef := httpRoute.Spec.Rules[0].BackendRefs[0]
		require.Equal(t, "InferencePool", string(*backendRef.Kind))
		require.Equal(t, "my-pool", string(backendRef.Name))
		// The generated HTTPRoute backendRef must carry the pool's namespace, not the route's.
		require.NotNil(t, backendRef.Namespace)
		require.Equal(t, "default", string(*backendRef.Namespace))
	})

	t.Run("cross-namespace without ReferenceGrant is rejected", func(t *testing.T) {
		c := requireNewFakeClientWithIndexes(t)

		controller := &AIGatewayRouteController{client: c, referenceGrantValidator: newReferenceGrantValidator(c)}
		httpRoute := &gwapiv1.HTTPRoute{}
		err := controller.newHTTPRoute(t.Context(), httpRoute, newRoute("default"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "cross-namespace reference from AIGatewayRoute in namespace gw "+
			"to InferencePool my-pool in namespace default is not permitted")
	})

	t.Run("same-namespace reference needs no ReferenceGrant", func(t *testing.T) {
		c := requireNewFakeClientWithIndexes(t)

		controller := &AIGatewayRouteController{client: c, referenceGrantValidator: newReferenceGrantValidator(c)}
		httpRoute := &gwapiv1.HTTPRoute{}
		require.NoError(t, controller.newHTTPRoute(t.Context(), httpRoute, newRoute("gw")))

		backendRef := httpRoute.Spec.Rules[0].BackendRefs[0]
		require.NotNil(t, backendRef.Namespace)
		require.Equal(t, "gw", string(*backendRef.Namespace))
	})
}

func Test_newHTTPRoute_LabelAndAnnotationPropagation(t *testing.T) {
	c := requireNewFakeClientWithIndexes(t)

	// Create test backends.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: "test-ns"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To(gwapiv1.Namespace("test-ns"))},
		},
	}
	require.NoError(t, c.Create(context.Background(), backend))

	// Create an AIGatewayRoute with custom labels and annotations.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-ns",
			Labels: map[string]string{
				"custom-label-1": "value-1",
				"custom-label-2": "value-2",
			},
			Annotations: map[string]string{
				"custom-annotation-1": "ann-value-1",
				"custom-annotation-2": "ann-value-2",
			},
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "test-backend", Weight: ptr.To[int32](100)},
					},
				},
			},
		},
	}

	controller := &AIGatewayRouteController{client: c}

	// Test initial HTTPRoute creation with labels and annotations.
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-ns",
		},
	}

	err := controller.newHTTPRoute(context.Background(), httpRoute, aiGatewayRoute)
	require.NoError(t, err)

	// Verify that all labels from AIGatewayRoute are copied to HTTPRoute.
	require.NotNil(t, httpRoute.Labels)
	require.Equal(t, "value-1", httpRoute.Labels["custom-label-1"])
	require.Equal(t, "value-2", httpRoute.Labels["custom-label-2"])

	// Verify that all annotations from AIGatewayRoute are copied to HTTPRoute.
	require.NotNil(t, httpRoute.Annotations)
	require.Equal(t, "ann-value-1", httpRoute.Annotations["custom-annotation-1"])
	require.Equal(t, "ann-value-2", httpRoute.Annotations["custom-annotation-2"])

	// Verify that controller-specific annotations are also present.
	require.Equal(t, "true", httpRoute.Annotations[httpRouteAnnotationForAIGatewayGeneratedIndication])
	require.Contains(t, httpRoute.Annotations, httpRouteQuotaPolicyAnnotationKey)

	// Test updating existing HTTPRoute with new labels and annotations.
	aiGatewayRoute.Labels["new-label"] = "new-value"
	aiGatewayRoute.Labels["custom-label-1"] = "new-value-1"
	aiGatewayRoute.Annotations["new-annotation"] = "new-ann-value"
	aiGatewayRoute.Annotations["custom-annotation-1"] = "new-ann-value-1"

	err = controller.newHTTPRoute(context.Background(), httpRoute, aiGatewayRoute)
	require.NoError(t, err)

	// Verify new labels and annotations are propagated.
	require.Equal(t, "new-value", httpRoute.Labels["new-label"])
	require.Equal(t, "new-ann-value", httpRoute.Annotations["new-annotation"])
	require.Equal(t, "new-value-1", httpRoute.Labels["custom-label-1"])
	require.Equal(t, "new-ann-value-1", httpRoute.Annotations["custom-annotation-1"])

	// Verify old labels and annotations are still present.
	require.Equal(t, "value-2", httpRoute.Labels["custom-label-2"])
	require.Equal(t, "ann-value-2", httpRoute.Annotations["custom-annotation-2"])
}

func Test_newHTTPRoute_Hostnames_NotSet(t *testing.T) {
	c := requireNewFakeClientWithIndexes(t)

	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: "test-ns"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To(gwapiv1.Namespace("test-ns"))},
		},
	}
	require.NoError(t, c.Create(context.Background(), backend))

	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "test-backend", Weight: ptr.To[int32](100)},
					},
				},
			},
		},
	}

	controller := &AIGatewayRouteController{client: c}
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-ns",
		},
	}

	err := controller.newHTTPRoute(context.Background(), httpRoute, aiGatewayRoute)
	require.NoError(t, err)

	require.Empty(t, httpRoute.Spec.Hostnames)
}

func Test_newHTTPRoute_Hostnames_Set(t *testing.T) {
	c := requireNewFakeClientWithIndexes(t)

	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: "test-ns"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To(gwapiv1.Namespace("test-ns"))},
		},
	}
	require.NoError(t, c.Create(context.Background(), backend))

	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Hostnames: []gwapiv1.Hostname{"api.example.com", "*.example.net", "sub.example.com"},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "test-backend", Weight: ptr.To[int32](100)},
					},
				},
			},
		},
	}

	controller := &AIGatewayRouteController{client: c, logger: logr.Discard()}
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "test-ns",
		},
	}

	err := controller.newHTTPRoute(context.Background(), httpRoute, aiGatewayRoute)
	require.NoError(t, err)

	expected := []gwapiv1.Hostname{"api.example.com", "*.example.net", "sub.example.com"}
	require.Equal(t, expected, httpRoute.Spec.Hostnames)
}

func TestAIGatewayRouteController_syncGateways_NamespaceDetermination(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()

	// Create test gateways in different namespaces.
	gateway1 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway1", Namespace: "default"},
	}
	gateway2 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway2", Namespace: "other-ns"},
	}

	err := fakeClient.Create(t.Context(), gateway1)
	require.NoError(t, err)
	err = fakeClient.Create(t.Context(), gateway2)
	require.NoError(t, err)

	// Create controller.
	c := NewAIGatewayRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch, "/v1")

	// Test AIGatewayRoute with parent references having different namespace configurations.
	aiGatewayRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default", // Route's namespace.
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1a2.ParentReference{
				{
					Name: "gateway1",
					// No namespace specified - should use route's namespace ("default").
				},
				{
					Name:      "gateway2",
					Namespace: ptr.To[gwapiv1a2.Namespace]("other-ns"), // Explicit namespace.
				},
			},
		},
	}

	// Call syncGateways.
	err = c.syncGateways(t.Context(), aiGatewayRoute)
	require.NoError(t, err)

	// Verify that events were sent for both gateways.
	// We should receive 2 events (one for each parent reference).
	gateways := eventCh.RequireItemsEventually(t, 2)
	require.Len(t, gateways, 2)

	// Extract the gateway names and namespaces from the events.
	gw1 := gateways[0]
	gw2 := gateways[1]

	// Verify first gateway: gateway1 in default namespace (inherited from route).
	require.Equal(t, "gateway1", gw1.Name)
	require.Equal(t, "default", gw1.Namespace)

	// Verify second gateway: gateway2 in other-ns (explicitly specified).
	require.Equal(t, "gateway2", gw2.Name)
	require.Equal(t, "other-ns", gw2.Namespace)
}

func TestAIGatewayRouteController_CrossNamespaceBackend_WithReferenceGrant(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, "/v1")

	// Create backend in backend-ns namespace
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cross-ns-backend",
			Namespace: "backend-ns",
		},
		Spec: aigv1b1.AIServiceBackendSpec{
			APISchema: aigv1b1.VersionedAPISchema{
				Name:    aigv1b1.APISchemaOpenAI,
				Version: ptr.To("v1"),
			},
			BackendRef: gwapiv1.BackendObjectReference{
				Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				Kind:  ptr.To(gwapiv1.Kind("Backend")),
				Name:  "my-backend",
			},
		},
	}
	err := fakeClient.Create(t.Context(), backend)
	require.NoError(t, err)

	// Create ReferenceGrant allowing cross-namespace access
	grant := &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-from-route-ns",
			Namespace: "backend-ns",
		},
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
	err = fakeClient.Create(t.Context(), grant)
	require.NoError(t, err)

	// Create AIGatewayRoute in route-ns that references backend in backend-ns
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cross-ns-route",
			Namespace: "route-ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:      "cross-ns-backend",
							Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
							Weight:    ptr.To[int32](1),
						},
					},
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Reconcile should succeed with ReferenceGrant
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "route-ns", Name: "cross-ns-route"},
	})
	require.NoError(t, err)

	// Verify that HTTPRoute was created
	var httpRoute gwapiv1.HTTPRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Namespace: "route-ns",
		Name:      "cross-ns-route",
	}, &httpRoute)
	require.NoError(t, err)

	// Verify the status is Accepted
	var updatedRoute aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Namespace: "route-ns",
		Name:      "cross-ns-route",
	}, &updatedRoute)
	require.NoError(t, err)
	require.Len(t, updatedRoute.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, updatedRoute.Status.Conditions[0].Type)
}

func TestAIGatewayRouteController_CrossNamespaceBackend_WithoutReferenceGrant(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, "/v1")

	// Create backend in backend-ns namespace
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cross-ns-backend",
			Namespace: "backend-ns",
		},
		Spec: aigv1b1.AIServiceBackendSpec{
			APISchema: aigv1b1.VersionedAPISchema{
				Name:    aigv1b1.APISchemaOpenAI,
				Version: ptr.To("v1"),
			},
			BackendRef: gwapiv1.BackendObjectReference{
				Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				Kind:  ptr.To(gwapiv1.Kind("Backend")),
				Name:  "my-backend",
			},
		},
	}
	err := fakeClient.Create(t.Context(), backend)
	require.NoError(t, err)

	// Create AIGatewayRoute in route-ns that references backend in backend-ns
	// WITHOUT creating a ReferenceGrant
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cross-ns-route",
			Namespace: "route-ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:      "cross-ns-backend",
							Namespace: ptr.To(gwapiv1.Namespace("backend-ns")),
							Weight:    ptr.To[int32](1),
						},
					},
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Reconcile should fail without ReferenceGrant
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "route-ns", Name: "cross-ns-route"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not permitted")
	require.Contains(t, err.Error(), "no valid ReferenceGrant found")

	// Verify the status is NotAccepted
	var updatedRoute aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Namespace: "route-ns",
		Name:      "cross-ns-route",
	}, &updatedRoute)
	require.NoError(t, err)
	require.Len(t, updatedRoute.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updatedRoute.Status.Conditions[0].Type)
}

func TestAIGatewayRouteController_SameNamespaceBackend_NoReferenceGrantNeeded(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewAIGatewayRouteController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, "/v1")

	// Create backend in same namespace as route
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "same-ns-backend",
			Namespace: "default",
		},
		Spec: aigv1b1.AIServiceBackendSpec{
			APISchema: aigv1b1.VersionedAPISchema{
				Name:    aigv1b1.APISchemaOpenAI,
				Version: ptr.To("v1"),
			},
			BackendRef: gwapiv1.BackendObjectReference{
				Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				Kind:  ptr.To(gwapiv1.Kind("Backend")),
				Name:  "my-backend",
			},
		},
	}
	err := fakeClient.Create(t.Context(), backend)
	require.NoError(t, err)

	// Create AIGatewayRoute in same namespace
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "same-ns-route",
			Namespace: "default",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{
							Name:   "same-ns-backend",
							Weight: ptr.To[int32](1),
							// No namespace specified, should use route's namespace
						},
					},
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Reconcile should succeed without ReferenceGrant for same-namespace reference
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "same-ns-route"},
	})
	require.NoError(t, err)

	// Verify the status is Accepted
	var updatedRoute aigv1b1.AIGatewayRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Namespace: "default",
		Name:      "same-ns-route",
	}, &updatedRoute)
	require.NoError(t, err)
	require.Len(t, updatedRoute.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, updatedRoute.Status.Conditions[0].Type)
}
