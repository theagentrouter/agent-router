// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakekube "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

// Helper: fake client configured for MCP tests with status subresource enabled.
func requireNewFakeClientWithIndexesForMCP(t *testing.T) client.Client {
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1b1.MCPRoute{})
	err := ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	})
	require.NoError(t, err)
	return builder.Build()
}

func TestMCPRouteController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), ctrl.Log, eventCh.Ch)

	// Create target Gateway referenced by ParentRefs.
	err := fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "mytarget", Namespace: "default"}})
	require.NoError(t, err)

	// Create MCPRoute with two backends and default path prefix.
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myroute",
			Namespace: "default",
			Labels:    map[string]string{"l1": "v1"},
			Annotations: map[string]string{
				"a1": "v1",
			},
		},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("mytarget")}},
			Headers:    []gwapiv1.HTTPHeaderMatch{{Name: "x-test-header", Value: "abc"}},
			Hostnames:  []gwapiv1.Hostname{"api.example.com", "*.example.net"},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name:      "svc-a",
						Namespace: ptr.To(gwapiv1.Namespace("default")),
					},
				},
				{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name:      "svc-b",
						Namespace: ptr.To(gwapiv1.Namespace("default")),
					},
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Reconcile should create/update an HTTPRoute and mark status accepted.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)

	// Verify finalizer added.
	var current aigv1b1.MCPRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "myroute"}, &current)
	require.NoError(t, err)
	require.Contains(t, current.Finalizers, aiGatewayControllerFinalizer, "Finalizer should be added")

	// Verify generated HTTPRoutes.
	var mainHTTPRoute gwapiv1.HTTPRoute
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPMainHTTPRoutePrefix + "myroute", Namespace: "default"}, &mainHTTPRoute)
	require.NoError(t, err)
	require.Len(t, mainHTTPRoute.Spec.Rules, 1)

	// Verify the mcp-proxy rule.
	require.Equal(t, "/mcp", *mainHTTPRoute.Spec.Rules[0].Matches[0].Path.Value)
	require.Equal(t, route.Spec.Headers, mainHTTPRoute.Spec.Rules[0].Matches[0].Headers)
	require.Len(t, mainHTTPRoute.Spec.Rules[0].BackendRefs, 1)
	require.Equal(t, gwapiv1.ObjectName(mcpProxySharedBackendName), mainHTTPRoute.Spec.Rules[0].BackendRefs[0].Name)

	// Verify the shared Backend exists in the MCPRoute namespace, has no controller owner (it is
	// shared across all MCPRoutes/Gateways in the namespace), and is tagged as managed by us.
	var sharedBackend egv1a1.Backend
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &sharedBackend)
	require.NoError(t, err)
	require.Nil(t, metav1.GetControllerOf(&sharedBackend))
	require.Equal(t, managedByValue, sharedBackend.Labels[managedByLabel])

	// Since HTTPRouteRule name is experimental in Gateway API, and some vendors (e.g. GKE Gateway) do not
	// support it yet, we currently do not set the sectionName to avoid compatibility issues.
	// The jwt filter will be removed from backend routes in the extension server.
	// TODO: set the rule name and target the SecurityPolicy with jwt authn to the mcp-proxy rule only when the
	// HTTPRouteRule name is in stable channel.
	require.Nil(t, mainHTTPRoute.Spec.Rules[0].Name)

	// Labels/annotations propagated.
	require.Equal(t, "v1", mainHTTPRoute.Labels["l1"])
	require.Equal(t, "v1", mainHTTPRoute.Annotations["a1"])
	// ParentRefs copied to HTTPRoute.
	require.Equal(t, route.Spec.ParentRefs, mainHTTPRoute.Spec.ParentRefs)
	// Hostnames copied to HTTPRoute so the route lands in the per-host vhost.
	require.Equal(t, route.Spec.Hostnames, mainHTTPRoute.Spec.Hostnames)

	// Verify the two per-backend HTTPRoute created.
	for _, refName := range []gwapiv1.ObjectName{"svc-a", "svc-b"} {
		var httpRoute gwapiv1.HTTPRoute
		err = fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpPerBackendRefHTTPRouteName(route.Name, refName), Namespace: "default"}, &httpRoute)
		require.NoError(t, err)
		require.Len(t, httpRoute.Spec.Rules, 1)
		rule := httpRoute.Spec.Rules[0]
		require.Equal(t, "/", *rule.Matches[0].Path.Value)
		require.Len(t, rule.BackendRefs, 1)
		require.Equal(t, refName, rule.BackendRefs[0].Name)
		headers := rule.Matches[0].Headers
		require.Len(t, headers, 2)
		require.Equal(t, internalapi.MCPBackendHeader, string(headers[0].Name))
		require.Equal(t, string(refName), headers[0].Value)
		require.Equal(t, internalapi.MCPRouteHeader, string(headers[1].Name))
		require.Equal(t, "default/myroute", headers[1].Value)
		// Labels/annotations propagated.
		require.Equal(t, "v1", httpRoute.Labels["l1"])
		require.Equal(t, "v1", httpRoute.Annotations["a1"])
		// ParentRefs copied to HTTPRoute.
		require.Equal(t, route.Spec.ParentRefs, httpRoute.Spec.ParentRefs)
		// Hostnames copied to HTTPRoute so the route lands in the per-host vhost.
		require.Equal(t, route.Spec.Hostnames, httpRoute.Spec.Hostnames)
	}

	// Let's update the route to remove one backend and change path.
	current.Spec.BackendRefs = current.Spec.BackendRefs[:1]
	current.Spec.Path = ptr.To("/custom/")
	err = fakeClient.Update(t.Context(), &current)
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)

	// Verify main HTTPRoute updated.
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPMainHTTPRoutePrefix + "myroute", Namespace: "default"}, &mainHTTPRoute)
	require.NoError(t, err)
	require.Len(t, mainHTTPRoute.Spec.Rules, 1)
	require.Equal(t, "/custom/", *mainHTTPRoute.Spec.Rules[0].Matches[0].Path.Value)
	require.Len(t, mainHTTPRoute.Spec.Rules[0].BackendRefs, 1)
	require.Equal(t, gwapiv1.ObjectName(mcpProxySharedBackendName), mainHTTPRoute.Spec.Rules[0].BackendRefs[0].Name)

	// svc-a (still in BackendRefs) per-backend HTTPRoute should still exist.
	var keptRoute gwapiv1.HTTPRoute
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpPerBackendRefHTTPRouteName(route.Name, "svc-a"), Namespace: "default"}, &keptRoute)
	require.NoError(t, err)

	// svc-b (removed from BackendRefs) per-backend HTTPRoute should have been deleted (orphan cleanup).
	var orphanedRoute gwapiv1.HTTPRoute
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpPerBackendRefHTTPRouteName(route.Name, "svc-b"), Namespace: "default"}, &orphanedRoute)
	require.True(t, apierrors.IsNotFound(err), "orphaned per-backend HTTPRoute for svc-b should have been deleted")

	// The corresponding HTTPRouteFilter for svc-b should also have been deleted.
	var orphanedFilter egv1a1.HTTPRouteFilter
	filterName := mcpBackendRefFilterName(route, "svc-b")
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: filterName, Namespace: "default"}, &orphanedFilter)
	require.True(t, apierrors.IsNotFound(err), "orphaned HTTPRouteFilter for svc-b should have been deleted")

	// Delete flow shouldn't error, and deleting the last MCPRoute should clean up the shared Backend.
	err = fakeClient.Delete(t.Context(), &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &sharedBackend)
	require.True(t, apierrors.IsNotFound(err), "shared MCP proxy Backend should be deleted after the last MCPRoute is removed")
}

// TestMCPRouteController_SharedBackendPerNamespace verifies that all MCPRoutes in a namespace —
// including one attached to multiple Gateways — share a single mcp-proxy Backend, and that the
// Backend is removed only once the last MCPRoute in the namespace is gone.
func TestMCPRouteController_SharedBackendPerNamespace(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), ctrl.Log, eventCh.Ch)

	// Two Gateways in the same namespace.
	for _, gw := range []string{"gw1", "gw2"} {
		require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: gw, Namespace: "default"}}))
	}

	newRoute := func(name string, parents ...gwapiv1.ObjectName) *aigv1b1.MCPRoute {
		refs := make([]gwapiv1.ParentReference, len(parents))
		for i, p := range parents {
			refs[i] = gwapiv1.ParentReference{Name: p}
		}
		return &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: refs,
				BackendRefs: []aigv1b1.MCPRouteBackendRef{{BackendObjectReference: gwapiv1.BackendObjectReference{
					Name: gwapiv1.ObjectName("svc-" + name), Namespace: ptr.To(gwapiv1.Namespace("default")),
				}}},
			},
		}
	}
	// r1 attaches to both Gateways; r2 attaches to one. Both live in the same namespace.
	r1, r2 := newRoute("r1", "gw1", "gw2"), newRoute("r2", "gw1")
	require.NoError(t, fakeClient.Create(t.Context(), r1))
	require.NoError(t, fakeClient.Create(t.Context(), r2))

	reconcile := func(name string) {
		_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
		require.NoError(t, err)
	}
	reconcile("r1")
	reconcile("r2")

	// Exactly one shared Backend for the namespace, owner-less and tagged as managed by us.
	var backends egv1a1.BackendList
	require.NoError(t, fakeClient.List(t.Context(), &backends, client.InNamespace("default")))
	require.Len(t, backends.Items, 1)
	require.Equal(t, mcpProxySharedBackendName, backends.Items[0].Name)
	require.Nil(t, metav1.GetControllerOf(&backends.Items[0]))
	require.Equal(t, managedByValue, backends.Items[0].Labels[managedByLabel])

	// Both main HTTPRoutes reference the shared Backend.
	for _, name := range []string{"r1", "r2"} {
		var hr gwapiv1.HTTPRoute
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPMainHTTPRoutePrefix + name, Namespace: "default"}, &hr))
		require.Equal(t, gwapiv1.ObjectName(mcpProxySharedBackendName), hr.Spec.Rules[0].BackendRefs[0].Name)
	}

	// Deleting r1 must NOT remove the Backend, because r2 still uses it.
	require.NoError(t, fakeClient.Delete(t.Context(), r1))
	reconcile("r1")
	var backend egv1a1.Backend
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &backend))

	// Deleting r2 (the last MCPRoute) removes the shared Backend.
	require.NoError(t, fakeClient.Delete(t.Context(), r2))
	reconcile("r2")
	err := fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &backend)
	require.True(t, apierrors.IsNotFound(err), "shared Backend should be deleted after the last MCPRoute in the namespace is removed")
}

// TestMCPRouteController_SharedBackend_PreservesUnmanagedBackend verifies that a Backend which
// happens to share the fixed name but was not created by us (no managed-by label) is neither
// modified on ensure nor deleted when the last MCPRoute in the namespace is removed.
func TestMCPRouteController_SharedBackend_PreservesUnmanagedBackend(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), ctrl.Log, eventCh.Ch)

	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw1", Namespace: "default"}}))

	// A user-owned Backend with the same fixed name but WITHOUT our managed-by label.
	userBackend := &egv1a1.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: mcpProxySharedBackendName, Namespace: "default", Labels: map[string]string{"owner": "someone-else"}},
		Spec:       egv1a1.BackendSpec{Endpoints: []egv1a1.BackendEndpoint{{IP: &egv1a1.IPEndpoint{Address: "10.0.0.1", Port: 1234}}}},
	}
	require.NoError(t, fakeClient.Create(t.Context(), userBackend))

	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs:  []gwapiv1.ParentReference{{Name: "gw1"}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-a", Namespace: ptr.To(gwapiv1.Namespace("default"))}}},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))

	reconcile := func(name string) {
		_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
		require.NoError(t, err)
	}
	reconcile("r1")

	// ensure must not have stamped our label onto the user's Backend nor changed its endpoint.
	var got egv1a1.Backend
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &got))
	require.NotEqual(t, managedByValue, got.Labels[managedByLabel])
	require.Equal(t, "10.0.0.1", got.Spec.Endpoints[0].IP.Address)

	// Deleting the last MCPRoute must NOT delete a Backend we do not own.
	require.NoError(t, fakeClient.Delete(t.Context(), route))
	reconcile("r1")
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &got),
		"user-owned Backend of the same name must be preserved")
}

// TestMCPRouteController_DeletesLegacyPerRouteBackend verifies the one-time migration: a legacy
// per-MCPRoute Backend ({ns}-{name}-mcp-proxy) owned by the route is removed on reconcile, and the
// shared per-namespace Backend is created in its place.
func TestMCPRouteController_DeletesLegacyPerRouteBackend(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), ctrl.Log, eventCh.Ch)

	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw1", Namespace: "default"}}))

	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default", UID: "myroute-uid"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs:  []gwapiv1.ParentReference{{Name: "gw1"}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-a", Namespace: ptr.To(gwapiv1.Namespace("default"))}}},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))
	var created aigv1b1.MCPRoute
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: "myroute", Namespace: "default"}, &created))

	// Legacy per-route Backend controller-owned by the MCPRoute (the pre-refactor scheme).
	legacyName := "default-myroute-mcp-proxy"
	legacy := &egv1a1.Backend{
		ObjectMeta: metav1.ObjectMeta{
			Name: legacyName, Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "aigateway.envoyproxy.io/v1beta1", Kind: "MCPRoute",
				Name: "myroute", UID: created.UID, Controller: ptr.To(true),
			}},
		},
		Spec: egv1a1.BackendSpec{Endpoints: []egv1a1.BackendEndpoint{{IP: &egv1a1.IPEndpoint{Address: mcpProxyBackendDummyIP, Port: int32(internalapi.MCPProxyPort)}}}},
	}
	require.NoError(t, fakeClient.Create(t.Context(), legacy))

	_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "myroute"}})
	require.NoError(t, err)

	// Legacy Backend removed; shared Backend created and labeled.
	var gone egv1a1.Backend
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: legacyName, Namespace: "default"}, &gone)
	require.True(t, apierrors.IsNotFound(err), "legacy per-route Backend should be deleted on migration")
	var shared egv1a1.Backend
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &shared))
	require.Equal(t, managedByValue, shared.Labels[managedByLabel])
}

func Test_newHTTPRoute_MCP_PathAndBackendsAndMetadata(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	ctrlr := NewMCPRouteController(c, nil, logr.Discard(), eventCh.Ch)

	httpRoute := &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"}}
	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "mcp-route",
			Namespace:   "ns",
			Labels:      map[string]string{"k1": "v1"},
			Annotations: map[string]string{"ann1": "v1"},
		},
		Spec: aigv1b1.MCPRouteSpec{
			Path:       ptr.To("/custom/"),
			Headers:    []gwapiv1.HTTPHeaderMatch{{Name: "x-match", Value: "yes"}},
			ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("gw")}},
		},
	}

	err := ctrlr.newMainHTTPRoute(httpRoute, mcpRoute, mcpProxySharedBackendName)
	require.NoError(t, err)

	require.Len(t, httpRoute.Spec.Rules, 1)
	require.Equal(t, "/custom/", *httpRoute.Spec.Rules[0].Matches[0].Path.Value)
	require.Len(t, httpRoute.Spec.Rules[0].BackendRefs, 1)
	require.Equal(t, gwapiv1.ObjectName(mcpProxySharedBackendName), httpRoute.Spec.Rules[0].BackendRefs[0].Name)

	// Metadata propagated.
	require.Equal(t, "v1", httpRoute.Labels["k1"])
	require.Equal(t, "v1", httpRoute.Annotations["ann1"])

	// ParentRefs copied over.
	require.Equal(t, mcpRoute.Spec.ParentRefs, httpRoute.Spec.ParentRefs)
}

func Test_newHTTPRoute_MCPOauth(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	ctrlr := NewMCPRouteController(c, nil, logr.Discard(), eventCh.Ch)

	httpRoute := &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"}}
	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"},
		Spec: aigv1b1.MCPRouteSpec{
			SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{OAuth: &aigv1b1.MCPRouteOAuth{}},
			Path:           ptr.To("/mcp"),
			Headers:        []gwapiv1.HTTPHeaderMatch{{Name: "x-match", Value: "yes"}},
			ParentRefs:     []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("gw")}},
			BackendRefs:    []aigv1b1.MCPRouteBackendRef{{}},
		},
	}

	err := ctrlr.newMainHTTPRoute(httpRoute, mcpRoute, mcpProxySharedBackendName)
	require.NoError(t, err)

	require.Len(t, httpRoute.Spec.Rules, 4) // 3 default routes for oauth which begins from index 1.
	oauthRules := httpRoute.Spec.Rules[1:]
	require.Equal(t, "oauth-protected-resource-metadata", string(ptr.Deref(oauthRules[0].Name, "")))
	require.Equal(t, "oauth-authorization-server-metadata", string(ptr.Deref(oauthRules[1].Name, "")))
	require.Equal(t, "oauth-authorization-server-metadata-oidc", string(ptr.Deref(oauthRules[2].Name, "")))
	require.Equal(t, "/.well-known/oauth-protected-resource/mcp", ptr.Deref(oauthRules[0].Matches[0].Path.Value, ""))
	require.Equal(t, "/.well-known/oauth-authorization-server/mcp", ptr.Deref(oauthRules[1].Matches[0].Path.Value, ""))
	require.Equal(t, "/.well-known/openid-configuration/mcp", ptr.Deref(oauthRules[2].Matches[0].Path.Value, ""))
	for _, rule := range oauthRules {
		require.Equal(t, mcpRoute.Spec.Headers, rule.Matches[0].Headers)
	}
}

func Test_newHTTPRoute_MCP_Hostnames(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	ctrlr := NewMCPRouteController(c, nil, logr.Discard(), eventCh.Ch)

	t.Run("set", func(t *testing.T) {
		httpRoute := &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"}}
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"},
			Spec: aigv1b1.MCPRouteSpec{
				Path:       ptr.To("/mcp"),
				ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("gw")}},
				Hostnames:  []gwapiv1.Hostname{"api.example.com", "*.example.net", "sub.example.com"},
			},
		}

		err := ctrlr.newMainHTTPRoute(httpRoute, mcpRoute, mcpProxySharedBackendName)
		require.NoError(t, err)
		require.Equal(t, mcpRoute.Spec.Hostnames, httpRoute.Spec.Hostnames)
	})

	t.Run("not set", func(t *testing.T) {
		httpRoute := &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"}}
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "mcp-route", Namespace: "ns"},
			Spec: aigv1b1.MCPRouteSpec{
				Path:       ptr.To("/mcp"),
				ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("gw")}},
			},
		}

		err := ctrlr.newMainHTTPRoute(httpRoute, mcpRoute, mcpProxySharedBackendName)
		require.NoError(t, err)
		require.Empty(t, httpRoute.Spec.Hostnames)
	})
}

func TestMCPRouteController_updateMCPRouteStatus(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	ctrlr := &MCPRouteController{client: fakeClient, logger: logr.Discard()}

	r := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route1", Namespace: "default"}}
	err := fakeClient.Create(t.Context(), r)
	require.NoError(t, err)

	ctrlr.updateMCPRouteStatus(t.Context(), r, aigv1b1.ConditionTypeNotAccepted, "err")
	var updated aigv1b1.MCPRoute
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "route1", Namespace: "default"}, &updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, "err", updated.Status.Conditions[0].Message)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)

	ctrlr.updateMCPRouteStatus(t.Context(), &updated, aigv1b1.ConditionTypeAccepted, "ok")
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "route1", Namespace: "default"}, &updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, "ok", updated.Status.Conditions[0].Message)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, updated.Status.Conditions[0].Type)
}

func TestMCPRouteController_syncGateway_notFound(t *testing.T) { // coverage for not-found branch.
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	s := NewMCPRouteController(fakeClient, fakekube.NewClientset(), logr.Discard(), eventCh.Ch)
	err := s.syncGateway(context.Background(), "ns", "non-exist")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestMCPRouteController_mcpRuleWithAPIKeyBackendSecurity(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	kubeClient := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "default"},
		Data:       map[string][]byte{"apiKey": []byte("secretvalue")},
	})
	ctrlr := NewMCPRouteController(c, kubeClient, logr.Discard(), eventCh.Ch)

	tests := []struct {
		name string
		key  *aigv1b1.MCPBackendAPIKey
		// expCredentialValue is the expected value stored in the credential secret's InjectedCredentialKey key.
		// When set, the HTTPRouteFilter is expected to have credentialInjection configured
		// (secretRef, or inline with injectionPolicy IfNotPresent).
		expCredentialValue string
		// expCredentialHeader is the expected header for the credential injection filter (secretRef path).
		expCredentialHeader *string
		// expOverwrite is the expected HTTPCredentialInjectionFilter.overwrite value.
		// When nil and expCredentialValue is set, overwrite is expected to be true.
		expOverwrite *bool
		// expInlineHeader is the expected RequestHeaderModifier header/value (inline path).
		expInlineHeader *internalapi.Header
		// expFilterCount is the expected number of filters on the HTTPRouteRule.
		expFilterCount int
		refPath        *string
		expPath        string
	}{
		{
			name:            "inline API key default header",
			key:             &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key")},
			expInlineHeader: &internalapi.Header{"Authorization", "Bearer inline-key"},
			expFilterCount:  3,
			expPath:         "/mcp",
		},
		{
			name:            "inline API key custom header",
			key:             &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), Header: ptr.To("X-API-KEY")},
			expInlineHeader: &internalapi.Header{"X-API-KEY", "inline-key"},
			expFilterCount:  3,
			expPath:         "/mcp",
		},
		{
			name:            "inline API key injectionPolicy Always keeps RequestHeaderModifier",
			key:             &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionAlways)},
			expInlineHeader: &internalapi.Header{"Authorization", "Bearer inline-key"},
			expFilterCount:  3,
			expPath:         "/mcp",
		},
		{
			name:            "inline API key injectionPolicy Always custom header",
			key:             &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), Header: ptr.To("X-Api-Key"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionAlways)},
			expInlineHeader: &internalapi.Header{"X-Api-Key", "inline-key"},
			expFilterCount:  3,
			expPath:         "/mcp",
		},
		{
			name:               "inline API key injectionPolicy IfNotPresent uses credentialInjection",
			key:                &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent)},
			expCredentialValue: "Bearer inline-key",
			expOverwrite:       ptr.To(false),
			expFilterCount:     2,
			expPath:            "/mcp",
		},
		{
			name:                "inline API key injectionPolicy IfNotPresent custom header",
			key:                 &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), Header: ptr.To("X-API-KEY"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent)},
			expCredentialValue:  "inline-key",
			expCredentialHeader: ptr.To("X-API-KEY"),
			expOverwrite:        ptr.To(false),
			expFilterCount:      2,
			expPath:             "/mcp",
		},
		{
			name:               "secret ref API key default header",
			key:                &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret"}},
			expCredentialValue: "Bearer secretvalue",
			expFilterCount:     2,
			refPath:            ptr.To("/some/path"),
			expPath:            "/some/path",
		},
		{
			name:                "secret ref API key custom header",
			key:                 &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret"}, Header: ptr.To("X-Api-Key")},
			expCredentialValue:  "secretvalue",
			expCredentialHeader: ptr.To("X-Api-Key"),
			expFilterCount:      2,
			expPath:             "/mcp",
		},
		{
			name:                "secret ref API key injectionPolicy Always custom header",
			key:                 &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret"}, Header: ptr.To("X-GitHub-Token"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionAlways)},
			expCredentialValue:  "secretvalue",
			expCredentialHeader: ptr.To("X-GitHub-Token"),
			expOverwrite:        ptr.To(true),
			expFilterCount:      2,
			expPath:             "/mcp",
		},
		{
			name:               "secret ref API key injectionPolicy IfNotPresent",
			key:                &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret"}, InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent)},
			expCredentialValue: "Bearer secretvalue",
			expOverwrite:       ptr.To(false),
			expFilterCount:     2,
			expPath:            "/mcp",
		},
		{
			name:                "secret ref API key injectionPolicy IfNotPresent custom header",
			key:                 &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret"}, Header: ptr.To("X-API-KEY"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent)},
			expCredentialValue:  "secretvalue",
			expCredentialHeader: ptr.To("X-API-KEY"),
			expOverwrite:        ptr.To(false),
			expFilterCount:      2,
			expPath:             "/mcp",
		},
		{
			name:           "query param API key",
			key:            &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), QueryParam: ptr.To("api_key")},
			expFilterCount: 2,
			expPath:        "/mcp?api_key=inline-key",
		},
		{
			name:           "query param API key ignores injectionPolicy Always",
			key:            &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), QueryParam: ptr.To("api_key"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionAlways)},
			expFilterCount: 2,
			expPath:        "/mcp?api_key=inline-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpRoute := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"}}
			httpRule, err := ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(),
				mcpRoute,
				&aigv1b1.MCPRouteBackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name:      "svc-a",
						Namespace: ptr.To(gwapiv1.Namespace("default")),
					},
					SecurityPolicy: &aigv1b1.MCPBackendSecurityPolicy{APIKey: tt.key},
					Path:           tt.refPath,
				},
			)
			require.NoError(t, err)
			require.Len(t, httpRule.Matches, 1)
			require.Equal(t, "/", *httpRule.Matches[0].Path.Value)
			headers := httpRule.Matches[0].Headers
			require.Len(t, headers, 2)
			require.Equal(t, internalapi.MCPBackendHeader, string(headers[0].Name))
			require.Equal(t, "svc-a", headers[0].Value)
			require.Equal(t, internalapi.MCPRouteHeader, string(headers[1].Name))
			require.Contains(t, headers[1].Value, "route-a")

			require.Len(t, httpRule.Filters, tt.expFilterCount)

			// The first filter is always the EG extension ref filter for URL host rewrite.
			egFilter := httpRule.Filters[0]
			require.Equal(t, gwapiv1.HTTPRouteFilterExtensionRef, egFilter.Type)
			require.NotNil(t, egFilter.ExtensionRef)
			require.Equal(t, gwapiv1.Group("gateway.envoyproxy.io"), egFilter.ExtensionRef.Group)
			require.Equal(t, gwapiv1.Kind("HTTPRouteFilter"), egFilter.ExtensionRef.Kind)
			require.Contains(t, string(egFilter.ExtensionRef.Name), internalapi.MCPPerBackendHTTPRouteFilterPrefix)
			var httpFilter egv1a1.HTTPRouteFilter
			err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: string(egFilter.ExtensionRef.Name)}, &httpFilter)
			require.NoError(t, err)
			require.NotNil(t, httpFilter.Spec.URLRewrite)
			require.NotNil(t, httpFilter.Spec.URLRewrite.Hostname)
			require.Equal(t, egv1a1.BackendHTTPHostnameModifier, httpFilter.Spec.URLRewrite.Hostname.Type)

			switch {
			case tt.expCredentialValue != "":
				// SecretRef path: credentialInjection on HTTPRouteFilter, no RequestHeaderModifier.
				require.NotNil(t, httpFilter.Spec.CredentialInjection, "expected credentialInjection on the HTTPRouteFilter")
				credSecretName := string(httpFilter.Spec.CredentialInjection.Credential.ValueRef.Name)
				require.Equal(t, mcpCredentialSecretName(mcpRoute, "svc-a"), credSecretName)
				require.Equal(t, tt.expCredentialHeader, httpFilter.Spec.CredentialInjection.Header)
				require.Equal(t, ptr.Deref(tt.expOverwrite, true), *httpFilter.Spec.CredentialInjection.Overwrite)

				credSecret, getSecretErr := kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
				require.NoError(t, getSecretErr)
				require.Equal(t, tt.expCredentialValue, string(credSecret.Data[egv1a1.InjectedCredentialKey]))

				// No plaintext should appear in any RequestHeaderModifier filter.
				for _, f := range httpRule.Filters {
					require.Nil(t, f.RequestHeaderModifier, "credentialInjection path must not use RequestHeaderModifier")
				}
			case tt.expInlineHeader != nil:
				// Inline path: RequestHeaderModifier, no credentialInjection, no credential Secret.
				require.Nil(t, httpFilter.Spec.CredentialInjection, "inline key should not use credentialInjection")

				reqHeaderFilter := httpRule.Filters[1]
				require.Equal(t, gwapiv1.HTTPRouteFilterRequestHeaderModifier, reqHeaderFilter.Type)
				require.NotNil(t, reqHeaderFilter.RequestHeaderModifier)
				found := false
				for _, set := range reqHeaderFilter.RequestHeaderModifier.Set {
					if set.Name == gwapiv1.HTTPHeaderName(tt.expInlineHeader.Key()) &&
						set.Value == tt.expInlineHeader.Value() {
						found = true
						break
					}
				}
				require.Truef(t, found, "expected header %v in %v", tt.expInlineHeader, reqHeaderFilter.RequestHeaderModifier.Set)

				// No credential Secret should be created for inline keys.
				credSecretName := mcpCredentialSecretName(mcpRoute, "svc-a")
				_, err = kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
				require.True(t, apierrors.IsNotFound(err), "no credential secret should exist for inline API key")
			default:
				// Query param path: no credentialInjection, no RequestHeaderModifier.
				require.Nil(t, httpFilter.Spec.CredentialInjection, "expected no credentialInjection for query param case")
			}

			// The last filter is always the path rewrite filter.
			pathRewriteFilter := httpRule.Filters[len(httpRule.Filters)-1]
			require.Equal(t, gwapiv1.HTTPRouteFilterURLRewrite, pathRewriteFilter.Type)
			require.NotNil(t, pathRewriteFilter.URLRewrite)
			require.NotNil(t, pathRewriteFilter.URLRewrite.Path)
			require.Equal(t, gwapiv1.FullPathHTTPPathModifier, pathRewriteFilter.URLRewrite.Path.Type)
			require.Equal(t, tt.expPath, *pathRewriteFilter.URLRewrite.Path.ReplaceFullPath)
		})
	}
}

func TestMCPRouteController_staleCredentialSecretCleanup(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	kubeClient := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-secret", Namespace: "default"},
		Data:       map[string][]byte{"apiKey": []byte("my-secret-key")},
	})
	ctrlr := NewMCPRouteController(c, kubeClient, logr.Discard(), eventCh.Ch)

	mcpRoute := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-cleanup", Namespace: "default"}}
	backendRef := &aigv1b1.MCPRouteBackendRef{
		BackendObjectReference: gwapiv1.BackendObjectReference{
			Name:      "svc-b",
			Namespace: ptr.To(gwapiv1.Namespace("default")),
		},
		SecurityPolicy: &aigv1b1.MCPBackendSecurityPolicy{
			APIKey: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "api-secret"}},
		},
	}

	// Step 1: Create an HTTPRouteRule with secretRef-based credential injection.
	_, err := ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(), mcpRoute, backendRef)
	require.NoError(t, err)

	// Verify the credential secret was created.
	credSecretName := mcpCredentialSecretName(mcpRoute, "svc-b")
	credSecret, err := kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "Bearer my-secret-key", string(credSecret.Data[egv1a1.InjectedCredentialKey]))

	// Step 2: Remove the security policy from the backend ref and reconcile again.
	backendRef.SecurityPolicy = nil
	_, err = ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(), mcpRoute, backendRef)
	require.NoError(t, err)

	// Verify the stale credential secret was deleted.
	_, err = kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "expected credential secret to be deleted, but got: %v", err)

	// Step 3: Verify that calling again without a security policy is idempotent (no error on missing secret).
	backendRef.SecurityPolicy = nil
	_, err = ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(), mcpRoute, backendRef)
	require.NoError(t, err)
}

func TestMCPRouteController_inlineAPIKeyIfNotPresentTransition(t *testing.T) {
	tests := []struct {
		name          string
		header        *string
		backendName   gwapiv1.ObjectName
		wantSecretVal string
		wantSetHeader gwapiv1.HTTPHeader
	}{
		{
			name:          "default Authorization header prefixes Bearer",
			backendName:   "svc-c",
			wantSecretVal: "Bearer inline-key",
			wantSetHeader: gwapiv1.HTTPHeader{Name: "Authorization", Value: "Bearer inline-key"},
		},
		{
			name:          "custom header injects the key verbatim",
			header:        ptr.To("X-Api-Key"),
			backendName:   "svc-d",
			wantSecretVal: "inline-key",
			wantSetHeader: gwapiv1.HTTPHeader{Name: "X-Api-Key", Value: "inline-key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := requireNewFakeClientWithIndexesForMCP(t)
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			kubeClient := fakekube.NewClientset()
			ctrlr := NewMCPRouteController(c, kubeClient, logr.Discard(), eventCh.Ch)

			mcpRoute := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-inline-if-not-present", Namespace: "default"}}
			backendRef := &aigv1b1.MCPRouteBackendRef{
				BackendObjectReference: gwapiv1.BackendObjectReference{
					Name:      tt.backendName,
					Namespace: ptr.To(gwapiv1.Namespace("default")),
				},
				SecurityPolicy: &aigv1b1.MCPBackendSecurityPolicy{
					APIKey: &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), Header: tt.header, InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent)},
				},
			}

			httpRule, err := ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(), mcpRoute, backendRef)
			require.NoError(t, err)

			credSecretName := mcpCredentialSecretName(mcpRoute, tt.backendName)
			credSecret, err := kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tt.wantSecretVal, string(credSecret.Data[egv1a1.InjectedCredentialKey]))
			for _, f := range httpRule.Filters {
				require.Nil(t, f.RequestHeaderModifier, "injectionPolicy IfNotPresent inline keys must not use RequestHeaderModifier")
			}

			var httpFilter egv1a1.HTTPRouteFilter
			err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: mcpBackendRefFilterName(mcpRoute, tt.backendName)}, &httpFilter)
			require.NoError(t, err)
			require.NotNil(t, httpFilter.Spec.CredentialInjection)
			require.False(t, *httpFilter.Spec.CredentialInjection.Overwrite)
			require.Equal(t, tt.header, httpFilter.Spec.CredentialInjection.Header)

			backendRef.SecurityPolicy.APIKey.InjectionPolicy = ptr.To(aigv1b1.MCPBackendAPIKeyInjectionAlways)
			httpRule, err = ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(), mcpRoute, backendRef)
			require.NoError(t, err)

			_, err = kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
			require.True(t, apierrors.IsNotFound(err), "expected credential secret to be deleted after switching to injectionPolicy Always")

			found := false
			for _, f := range httpRule.Filters {
				if f.RequestHeaderModifier == nil {
					continue
				}
				for _, h := range f.RequestHeaderModifier.Set {
					if h.Name == tt.wantSetHeader.Name && h.Value == tt.wantSetHeader.Value {
						found = true
					}
				}
			}
			require.Truef(t, found, "injectionPolicy Always inline keys should use RequestHeaderModifier %v", tt.wantSetHeader)
		})
	}
}

func TestMCPRouteController_ensureMCPBackendRefHTTPFilter(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	kubeClient := fakekube.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-secret", Namespace: "default"},
			Data:       map[string][]byte{"apiKey": []byte("test-api-key")},
		},
	)
	ctrlr := NewMCPRouteController(c, kubeClient, logr.Discard(), eventCh.Ch)

	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
	}
	err := c.Create(t.Context(), mcpRoute)
	require.NoError(t, err)

	filterName := mcpBackendRefFilterName(mcpRoute, "some-name")

	t.Run("without credential injection", func(t *testing.T) {
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "", nil, true)
		require.NoError(t, err)

		var httpFilter egv1a1.HTTPRouteFilter
		err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: filterName}, &httpFilter)
		require.NoError(t, err)
		require.NotNil(t, httpFilter.Spec.URLRewrite)
		require.Nil(t, httpFilter.Spec.CredentialInjection)
	})

	t.Run("with credential injection", func(t *testing.T) {
		customHeader := "X-Custom-Key"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "managed-ref-primary", &customHeader, true)
		require.NoError(t, err)

		var httpFilter egv1a1.HTTPRouteFilter
		err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: filterName}, &httpFilter)
		require.NoError(t, err)
		require.NotNil(t, httpFilter.Spec.URLRewrite)
		require.NotNil(t, httpFilter.Spec.CredentialInjection)
		require.Equal(t, &customHeader, httpFilter.Spec.CredentialInjection.Header)
		require.True(t, *httpFilter.Spec.CredentialInjection.Overwrite)
		require.Equal(t, gwapiv1.ObjectName("managed-ref-primary"), httpFilter.Spec.CredentialInjection.Credential.ValueRef.Name)
	})

	t.Run("with credential injection overwrite false", func(t *testing.T) {
		customHeader := "Authorization"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "managed-ref-no-overwrite", &customHeader, false)
		require.NoError(t, err)

		var httpFilter egv1a1.HTTPRouteFilter
		err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: filterName}, &httpFilter)
		require.NoError(t, err)
		require.NotNil(t, httpFilter.Spec.CredentialInjection)
		require.Equal(t, &customHeader, httpFilter.Spec.CredentialInjection.Header)
		require.False(t, *httpFilter.Spec.CredentialInjection.Overwrite)
		require.Equal(t, gwapiv1.ObjectName("managed-ref-no-overwrite"), httpFilter.Spec.CredentialInjection.Credential.ValueRef.Name)
	})

	t.Run("with credential injection overwrite false custom header", func(t *testing.T) {
		customHeader := "X-Api-Key"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "managed-ref-no-overwrite-custom", &customHeader, false)
		require.NoError(t, err)

		var httpFilter egv1a1.HTTPRouteFilter
		err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: filterName}, &httpFilter)
		require.NoError(t, err)
		require.NotNil(t, httpFilter.Spec.CredentialInjection)
		require.Equal(t, &customHeader, httpFilter.Spec.CredentialInjection.Header)
		require.False(t, *httpFilter.Spec.CredentialInjection.Overwrite)
		require.Equal(t, gwapiv1.ObjectName("managed-ref-no-overwrite-custom"), httpFilter.Spec.CredentialInjection.Credential.ValueRef.Name)
	})

	t.Run("deletes stale secret when transitioning away from credential injection", func(t *testing.T) {
		staleRefName := "stale-managed-ref"
		_, createErr := kubeClient.CoreV1().Secrets("default").Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: staleRefName, Namespace: "default"},
			Data:       map[string][]byte{egv1a1.InjectedCredentialKey: []byte("stale")},
		}, metav1.CreateOptions{})
		require.NoError(t, createErr)

		authorizationHeader := "Authorization"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, staleRefName, &authorizationHeader, true)
		require.NoError(t, err)

		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "", nil, true)
		require.NoError(t, err)

		_, getErr := kubeClient.CoreV1().Secrets("default").Get(t.Context(), staleRefName, metav1.GetOptions{})
		require.True(t, apierrors.IsNotFound(getErr), "expected stale credential secret to be deleted")
	})

	t.Run("deletes old secret when rotating credential injection secret", func(t *testing.T) {
		oldRefName := "old-managed-ref"
		newRefName := "new-managed-ref"
		_, createErr := kubeClient.CoreV1().Secrets("default").Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: oldRefName, Namespace: "default"},
			Data:       map[string][]byte{egv1a1.InjectedCredentialKey: []byte("old")},
		}, metav1.CreateOptions{})
		require.NoError(t, createErr)

		customHeader := "X-Custom-Key"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, oldRefName, &customHeader, true)
		require.NoError(t, err)

		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, newRefName, &customHeader, true)
		require.NoError(t, err)

		_, getErr := kubeClient.CoreV1().Secrets("default").Get(t.Context(), oldRefName, metav1.GetOptions{})
		require.True(t, apierrors.IsNotFound(getErr), "expected old credential secret to be deleted")
	})
}

func TestMCPRouteController_credentialHelpers(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	kubeClient := fakekube.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "api-secret", Namespace: "default"},
			Data:       map[string][]byte{"apiKey": []byte("initial-key")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "missing-api-key", Namespace: "default"},
			Data:       map[string][]byte{"other": []byte("value")},
		},
	)
	ctrlr := NewMCPRouteController(c, kubeClient, logr.Discard(), eventCh.Ch)

	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
	}
	err := c.Create(t.Context(), mcpRoute)
	require.NoError(t, err)

	t.Run("ensureCredentialSecret", func(t *testing.T) {
		secretRefKey := &aigv1b1.MCPBackendAPIKey{
			SecretRef: &gwapiv1.SecretObjectReference{Name: "api-secret"},
		}

		err = ctrlr.ensureCredentialSecret(t.Context(), "managed-ref-secret", mcpRoute, secretRefKey)
		require.NoError(t, err)

		credSecret, getErr := kubeClient.CoreV1().Secrets("default").Get(t.Context(), "managed-ref-secret", metav1.GetOptions{})
		require.NoError(t, getErr)
		require.Equal(t, "Bearer initial-key", string(credSecret.Data[egv1a1.InjectedCredentialKey]))

		_, updateErr := kubeClient.CoreV1().Secrets("default").Update(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "api-secret", Namespace: "default"},
			Data:       map[string][]byte{"apiKey": []byte("rotated-key")},
		}, metav1.UpdateOptions{})
		require.NoError(t, updateErr)

		err = ctrlr.ensureCredentialSecret(t.Context(), "managed-ref-secret", mcpRoute, secretRefKey)
		require.NoError(t, err)

		credSecret, getErr = kubeClient.CoreV1().Secrets("default").Get(t.Context(), "managed-ref-secret", metav1.GetOptions{})
		require.NoError(t, getErr)
		require.Equal(t, "Bearer rotated-key", string(credSecret.Data[egv1a1.InjectedCredentialKey]))

		header := "X-API-Key"
		inlineKey := &aigv1b1.MCPBackendAPIKey{
			Inline: ptr.To("inline-key"),
			Header: &header,
		}
		err = ctrlr.ensureCredentialSecret(t.Context(), "managed-ref-inline", mcpRoute, inlineKey)
		require.NoError(t, err)

		credSecret, getErr = kubeClient.CoreV1().Secrets("default").Get(t.Context(), "managed-ref-inline", metav1.GetOptions{})
		require.NoError(t, getErr)
		require.Equal(t, "inline-key", string(credSecret.Data[egv1a1.InjectedCredentialKey]))

		customHeader := "X-GitHub-Token"
		secretRefCustomHeader := &aigv1b1.MCPBackendAPIKey{
			SecretRef: &gwapiv1.SecretObjectReference{Name: "api-secret"},
			Header:    &customHeader,
		}
		err = ctrlr.ensureCredentialSecret(t.Context(), "managed-ref-custom-header", mcpRoute, secretRefCustomHeader)
		require.NoError(t, err)

		credSecret, getErr = kubeClient.CoreV1().Secrets("default").Get(t.Context(), "managed-ref-custom-header", metav1.GetOptions{})
		require.NoError(t, getErr)
		require.Equal(t, "rotated-key", string(credSecret.Data[egv1a1.InjectedCredentialKey]))
	})

	t.Run("readAPIKey", func(t *testing.T) {
		tests := []struct {
			name      string
			keySpec   *aigv1b1.MCPBackendAPIKey
			wantKey   string
			wantError string
		}{
			{
				name:    "inline key",
				keySpec: &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-value")},
				wantKey: "inline-value",
			},
			{
				name:    "secretRef key",
				keySpec: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "api-secret"}},
				wantKey: "rotated-key",
			},
			{
				name:      "missing apiKey in secret",
				keySpec:   &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "missing-api-key"}},
				wantError: "does not contain 'apiKey' key",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				gotKey, readErr := ctrlr.readAPIKey(t.Context(), &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "default"}}, tt.keySpec)
				if tt.wantError != "" {
					require.Error(t, readErr)
					require.Contains(t, readErr.Error(), tt.wantError)
					return
				}

				require.NoError(t, readErr)
				require.Equal(t, tt.wantKey, gotKey)
			})
		}
	})
}

func TestMCPRouteController_syncGateways_NamespaceCrossReference(t *testing.T) {
	c := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()

	gateway1 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway1", Namespace: "default"},
	}
	gateway2 := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway2", Namespace: "other-ns"},
	}

	err := c.Create(t.Context(), gateway1)
	require.NoError(t, err)
	err = c.Create(t.Context(), gateway2)
	require.NoError(t, err)

	ctrlr := NewMCPRouteController(c, fakekube.NewClientset(), logr.Discard(), eventCh.Ch)

	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{Name: gwapiv1.ObjectName("gateway1"), Namespace: ptr.To(gwapiv1.Namespace("default"))},
				{Name: gwapiv1.ObjectName("gateway2"), Namespace: ptr.To(gwapiv1.Namespace("other-ns"))},
			},
		},
	}
	err = ctrlr.syncGateways(t.Context(), mcpRoute)
	require.NoError(t, err)

	// Verify that events were sent for both gateways.
	// We should receive 2 events (one for each parent reference).
	gateways := eventCh.RequireItemsEventually(t, 2)
	require.Len(t, gateways, 2)

	require.Equal(t, "gateway1", gateways[0].Name)
	require.Equal(t, "default", gateways[0].Namespace)
	require.Equal(t, "gateway2", gateways[1].Name)
	require.Equal(t, "other-ns", gateways[1].Namespace)
}

func TestMCPRouteController_Reconcile_GatewayNotFound(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), ctrl.Log, eventCh.Ch)

	// Create MCPRoute referencing a non-existent gateway.
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "broken-route",
			Namespace: "default",
		},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("non-existent")}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name:      "svc-a",
						Namespace: ptr.To(gwapiv1.Namespace("default")),
					},
				},
			},
		},
	}
	err := fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Reconcile should fail and mark status as NotAccepted.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "broken-route"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-existent")

	// Verify the MCPRoute status is NotAccepted.
	var current aigv1b1.MCPRoute
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

	// Verify the MCPRoute status is Accepted.
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "broken-route"}, &current)
	require.NoError(t, err)
	require.Len(t, current.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, current.Status.Conditions[0].Type)
	require.Contains(t, current.Status.Conditions[0].Message, "reconciled successfully")
}

func TestMCPRouteController_Reconcile_DeletionWithMissingGateway(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), ctrl.Log, eventCh.Ch)

	// Create the gateway first so that the initial reconcile succeeds.
	err := fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "temp-gw", Namespace: "default"}})
	require.NoError(t, err)

	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-to-delete",
			Namespace: "default",
		},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: gwapiv1.ObjectName("temp-gw")}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name:      "svc-a",
						Namespace: ptr.To(gwapiv1.Namespace("default")),
					},
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), route)
	require.NoError(t, err)

	// Initial reconcile to add the finalizer.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "route-to-delete"}})
	require.NoError(t, err)

	// Verify finalizer is present.
	var current aigv1b1.MCPRoute
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "route-to-delete"}, &current)
	require.NoError(t, err)
	require.Contains(t, current.Finalizers, aiGatewayControllerFinalizer)

	// Now delete the gateway (simulating it being removed before the MCPRoute).
	err = fakeClient.Delete(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "temp-gw", Namespace: "default"}})
	require.NoError(t, err)

	// Delete the MCPRoute.
	err = fakeClient.Delete(t.Context(), &current)
	require.NoError(t, err)

	// Reconcile the deletion — should succeed even though the gateway is gone.
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "route-to-delete"}})
	require.NoError(t, err)

	// Verify the MCPRoute finalizer has been removed (object should be gone or have no finalizer).
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "route-to-delete"}, &current)
	if err == nil {
		require.NotContains(t, current.Finalizers, aiGatewayControllerFinalizer)
	} else {
		require.True(t, apierrors.IsNotFound(err))
	}
}

func TestValidatePerBackendPrefixMode(t *testing.T) {
	always := aigv1b1.MCPRoutePrefixModeAlways
	never := aigv1b1.MCPRoutePrefixModeNever

	backendRef := func(name string, prefixMode *aigv1b1.MCPRoutePrefixMode, include ...string) aigv1b1.MCPRouteBackendRef {
		ref := aigv1b1.MCPRouteBackendRef{
			BackendObjectReference: gwapiv1.BackendObjectReference{Name: gwapiv1.ObjectName(name)},
			PrefixMode:             prefixMode,
		}
		if len(include) > 0 {
			ref.ToolSelector = &aigv1b1.MCPToolFilter{Include: include}
		}
		return ref
	}

	backendRefWithPrompts := func(name string, prefixMode *aigv1b1.MCPRoutePrefixMode, toolInclude, promptInclude []string) aigv1b1.MCPRouteBackendRef {
		ref := backendRef(name, prefixMode, toolInclude...)
		ref.PromptSelector = &aigv1b1.MCPPromptFilter{Include: promptInclude}
		return ref
	}

	tests := []struct {
		name        string
		routeMode   *aigv1b1.MCPRoutePrefixMode
		backendRefs []aigv1b1.MCPRouteBackendRef
		wantErr     string // empty means no error expected
	}{
		{
			name:        "no prefixMode anywhere defaults to Always: no validation required",
			routeMode:   nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{backendRef("a", nil), backendRef("b", nil)},
		},
		{
			name:        "per-backend Never without toolSelector.include: rejected",
			routeMode:   nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{backendRef("a", &never)},
			wantErr:     `prefixMode Never requires backend "a" to explicitly enumerate its tools via toolSelector.include`,
		},
		{
			name:      "per-backend Never with unique tools: accepted",
			routeMode: nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", &never, "search"),
				backendRef("b", &never, "list"),
			},
		},
		{
			name:      "per-backend Never with colliding tools: rejected",
			routeMode: nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", &never, "search"),
				backendRef("b", &never, "search"),
			},
			wantErr: `prefixMode Never requires tool names to be unique across backends, but "search" is declared by both "a" and "b"`,
		},
		{
			// Route-level PrefixMode=Never must fall back onto backends that don't set their
			// own PrefixMode, mirroring effectivePrefixMode's runtime resolution. Without this,
			// a route-level Never route with no per-backend override would bypass validation
			// entirely, even though every backend behaves as Never at request time.
			name:        "route-level Never inherited by backend without own PrefixMode: still requires toolSelector.include",
			routeMode:   &never,
			backendRefs: []aigv1b1.MCPRouteBackendRef{backendRef("a", nil)},
			wantErr:     `prefixMode Never requires backend "a" to explicitly enumerate its tools via toolSelector.include`,
		},
		{
			name:      "route-level Never inherited by two backends with colliding tools: rejected",
			routeMode: &never,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", nil, "search"),
				backendRef("b", nil, "search"),
			},
			wantErr: `prefixMode Never requires tool names to be unique across backends, but "search" is declared by both "a" and "b"`,
		},
		{
			name:      "route-level Never inherited by backends with unique tools: accepted",
			routeMode: &never,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", nil, "search"),
				backendRef("b", nil, "list"),
			},
		},
		{
			// Per-backend PrefixMode takes precedence over the route-level default, so a
			// backend explicitly opting back into Always is exempt from the Never validation
			// even though the route default is Never.
			name:      "per-backend Always overrides route-level Never: exempt from validation",
			routeMode: &never,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", &always), // no toolSelector.include, would fail if treated as Never.
			},
		},
		{
			name:      "route-level Always (explicit) with no per-backend override: no validation required",
			routeMode: &always,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", nil),
			},
		},
		{
			// promptSelector is opt-in: a Never-mode backend that only declares toolSelector
			// (no promptSelector) is still accepted, because it just means the backend's
			// prompts stay prefixed (see mcpproxy.mergePromptsList) rather than being rejected.
			name:      "per-backend Never with tools but no promptSelector: accepted",
			routeMode: nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", &never, "search"),
			},
		},
		{
			name:      "per-backend Never with unique promptSelector.include across backends: accepted",
			routeMode: nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRefWithPrompts("a", &never, []string{"search"}, []string{"greeting"}),
				backendRefWithPrompts("b", &never, []string{"list"}, []string{"farewell"}),
			},
		},
		{
			name:      "per-backend Never with colliding promptSelector.include: rejected",
			routeMode: nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRefWithPrompts("a", &never, []string{"search"}, []string{"greeting"}),
				backendRefWithPrompts("b", &never, []string{"list"}, []string{"greeting"}),
			},
			wantErr: `prefixMode Never requires prompt names to be unique across backends, but "greeting" is declared by both "a" and "b"`,
		},
		{
			// Tool and prompt names live in separate namespaces (tools/call vs prompts/get), so
			// the same literal name declared as a tool by one backend and a prompt by another
			// must not be treated as a collision.
			name:      "per-backend Never with same name as tool on one backend and prompt on another: accepted",
			routeMode: nil,
			backendRefs: []aigv1b1.MCPRouteBackendRef{
				backendRef("a", &never, "shared-name"),
				backendRefWithPrompts("b", &never, []string{"list"}, []string{"shared-name"}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &aigv1b1.MCPRoute{
				Spec: aigv1b1.MCPRouteSpec{
					PrefixMode:  tt.routeMode,
					BackendRefs: tt.backendRefs,
				},
			}
			err := validatePerBackendPrefixMode(route)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

// mcpRouteSecretGrant returns a ReferenceGrant in secretNamespace that allows MCPRoute from
// routeNamespace to reference the named Secret (every Secret when name is empty).
func mcpRouteSecretGrant(secretNamespace, routeNamespace, name string) *gwapiv1b1.ReferenceGrant {
	to := gwapiv1b1.ReferenceGrantTo{Group: secretGroup, Kind: secretKind}
	if name != "" {
		to.Name = ptr.To(gwapiv1b1.ObjectName(name))
	}
	return &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-" + routeNamespace, Namespace: secretNamespace},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{
				Group:     aiServiceBackendGroup,
				Kind:      mcpRouteKind,
				Namespace: gwapiv1b1.Namespace(routeNamespace),
			}},
			To: []gwapiv1b1.ReferenceGrantTo{to},
		},
	}
}

// TestMCPRouteController_apiKeySecretRefNamespace verifies that apiKey.secretRef.namespace selects
// the Secret's namespace and that a Secret in another namespace is read only when a ReferenceGrant
// allows it. A same-named Secret in the route's own namespace must never stand in for it.
func TestMCPRouteController_apiKeySecretRefNamespace(t *testing.T) {
	const routeNS, secretNS = "default", "shared"
	tests := []struct {
		name string
		key  *aigv1b1.MCPBackendAPIKey
		// grant is created in secretNS before the rule is built.
		grant *gwapiv1b1.ReferenceGrant
		// expCredentialValue is the value expected in the managed credential Secret (header path).
		expCredentialValue string
		// expPath is the expected rewrite path (query parameter path).
		expPath string
		// expNotPermitted expects a ReferenceNotPermittedError and no managed credential Secret.
		expNotPermitted bool
	}{
		{
			name: "cross-namespace secret with grant",
			key: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{
				Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace(secretNS)),
			}},
			grant:              mcpRouteSecretGrant(secretNS, routeNS, "some-secret"),
			expCredentialValue: "Bearer shared-value",
		},
		{
			name: "cross-namespace secret with grant for every secret",
			key: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{
				Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace(secretNS)),
			}, Header: ptr.To("X-API-Key")},
			grant:              mcpRouteSecretGrant(secretNS, routeNS, ""),
			expCredentialValue: "shared-value",
		},
		{
			name: "cross-namespace secret with grant on the query parameter path",
			key: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{
				Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace(secretNS)),
			}, QueryParam: ptr.To("api_key")},
			grant:   mcpRouteSecretGrant(secretNS, routeNS, "some-secret"),
			expPath: "/mcp?api_key=shared-value",
		},
		{
			name: "cross-namespace secret without grant",
			key: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{
				Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace(secretNS)),
			}},
			expNotPermitted: true,
		},
		{
			name: "explicit same namespace needs no grant",
			key: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{
				Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace(routeNS)),
			}},
			expCredentialValue: "Bearer route-ns-value",
		},
		{
			name: "empty namespace means the route namespace",
			key: &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{
				Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace("")),
			}},
			expCredentialValue: "Bearer route-ns-value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := requireNewFakeClientWithIndexesForMCP(t)
			if tt.grant != nil {
				require.NoError(t, c.Create(t.Context(), tt.grant))
			}
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			kubeClient := fakekube.NewClientset(
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: routeNS}, Data: map[string][]byte{"apiKey": []byte("route-ns-value")}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: secretNS}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
			)
			ctrlr := NewMCPRouteController(c, kubeClient, logr.Discard(), eventCh.Ch)
			mcpRoute := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: routeNS}}
			ref := &aigv1b1.MCPRouteBackendRef{
				BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-a", Namespace: ptr.To(gwapiv1.Namespace(routeNS))},
				SecurityPolicy:         &aigv1b1.MCPBackendSecurityPolicy{APIKey: tt.key},
			}
			rule, err := ctrlr.mcpBackendRefToHTTPRouteRule(t.Context(), mcpRoute, ref)
			credName := mcpCredentialSecretName(mcpRoute, "svc-a")
			if tt.expNotPermitted {
				require.Error(t, err)
				var notPermitted *ReferenceNotPermittedError
				require.ErrorAs(t, err, &notPermitted)
				require.Equal(t, mcpRouteKind, notPermitted.FromKind)
				require.Equal(t, secretNS, notPermitted.TargetNamespace)
				require.Equal(t, "some-secret", notPermitted.TargetName)
				_, err = kubeClient.CoreV1().Secrets(routeNS).Get(t.Context(), credName, metav1.GetOptions{})
				require.True(t, apierrors.IsNotFound(err), "no credential Secret must be written for a denied reference")
				return
			}
			require.NoError(t, err)
			if tt.expPath != "" {
				var rewrite *gwapiv1.HTTPURLRewriteFilter
				for _, f := range rule.Filters {
					if f.Type == gwapiv1.HTTPRouteFilterURLRewrite {
						rewrite = f.URLRewrite
					}
				}
				require.NotNil(t, rewrite)
				require.Equal(t, tt.expPath, *rewrite.Path.ReplaceFullPath)
				return
			}
			cred, err := kubeClient.CoreV1().Secrets(routeNS).Get(t.Context(), credName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tt.expCredentialValue, string(cred.Data[egv1a1.InjectedCredentialKey]))
		})
	}
}

// countSecretReads records how many Secret reads the controller issues per namespace without changing
// what the fake clientset returns.
func countSecretReads(kube *fakekube.Clientset) map[string]int {
	reads := map[string]int{}
	kube.PrependReactor("get", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		reads[a.GetNamespace()]++
		return false, nil, nil
	})
	return reads
}

// requireUserSecretExists asserts that a Secret the user owns is still there, reading it through the
// tracker so the read does not count as a controller read.
func requireUserSecretExists(t *testing.T, kube *fakekube.Clientset, namespace, name string) {
	t.Helper()
	_, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), namespace, name)
	require.NoErrorf(t, err, "the user's Secret %s/%s must never be touched", namespace, name)
}

// requirePerBackendResources asserts whether the per-backend HTTPRoute, HTTPRouteFilter and credential
// Secret of backend exist. When present is true, wantCredential is the expected value of the credential
// Secret, and an empty wantCredential means the backend must have no credential Secret (no secretRef
// header injection, for example the query-parameter path).
func requirePerBackendResources(t *testing.T, c client.Client, kube *fakekube.Clientset, route *aigv1b1.MCPRoute, backend gwapiv1.ObjectName, present bool, wantCredential string) {
	t.Helper()
	var httpRoute gwapiv1.HTTPRoute
	routeErr := c.Get(t.Context(), client.ObjectKey{Name: mcpPerBackendRefHTTPRouteName(route.Name, backend), Namespace: route.Namespace}, &httpRoute)
	var filter egv1a1.HTTPRouteFilter
	filterErr := c.Get(t.Context(), client.ObjectKey{Name: mcpBackendRefFilterName(route, backend), Namespace: route.Namespace}, &filter)
	cred, credErr := kube.CoreV1().Secrets(route.Namespace).Get(t.Context(), mcpCredentialSecretName(route, backend), metav1.GetOptions{})
	if !present {
		require.Truef(t, apierrors.IsNotFound(routeErr), "per-backend HTTPRoute of %s must not exist: %v", backend, routeErr)
		require.Truef(t, apierrors.IsNotFound(filterErr), "HTTPRouteFilter of %s must not exist: %v", backend, filterErr)
		require.Truef(t, apierrors.IsNotFound(credErr), "credential Secret of %s must not exist: %v", backend, credErr)
		return
	}
	require.NoErrorf(t, routeErr, "per-backend HTTPRoute of %s", backend)
	require.NoErrorf(t, filterErr, "HTTPRouteFilter of %s", backend)
	if wantCredential == "" {
		require.Truef(t, apierrors.IsNotFound(credErr), "backend %s must have no credential Secret: %v", backend, credErr)
		return
	}
	require.NoErrorf(t, credErr, "credential Secret of %s", backend)
	require.Equal(t, wantCredential, string(cred.Data[egv1a1.InjectedCredentialKey]))
}

// requireMCPRouteCondition asserts the single status condition of route and that its message contains
// every one of substrings.
func requireMCPRouteCondition(t *testing.T, c client.Client, route *aigv1b1.MCPRoute, condType string, substrings ...string) {
	t.Helper()
	var current aigv1b1.MCPRoute
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(route), &current))
	require.Len(t, current.Status.Conditions, 1)
	require.Equal(t, condType, current.Status.Conditions[0].Type)
	for _, s := range substrings {
		require.Contains(t, current.Status.Conditions[0].Message, s)
	}
}

// mcpRouteSecretRefBackend returns a backendRef in the route namespace whose apiKey.secretRef points at
// secretNamespace/secretName; an empty secretNamespace leaves the namespace unset.
func mcpRouteSecretRefBackend(name gwapiv1.ObjectName, routeNamespace, secretNamespace, secretName string) aigv1b1.MCPRouteBackendRef {
	ref := &gwapiv1.SecretObjectReference{Name: gwapiv1.ObjectName(secretName)}
	if secretNamespace != "" {
		ref.Namespace = ptr.To(gwapiv1.Namespace(secretNamespace))
	}
	return aigv1b1.MCPRouteBackendRef{
		BackendObjectReference: gwapiv1.BackendObjectReference{Name: name, Namespace: ptr.To(gwapiv1.Namespace(routeNamespace))},
		SecurityPolicy:         &aigv1b1.MCPBackendSecurityPolicy{APIKey: &aigv1b1.MCPBackendAPIKey{SecretRef: ref}},
	}
}

// TestMCPRouteController_Reconcile_SecretRefGrantRevoked verifies that a backend whose cross-namespace
// apiKey.secretRef loses its ReferenceGrant converges to the never-granted state: its per-backend
// HTTPRoute, HTTPRouteFilter and credential Secret are deleted, the route's other backends and its main
// HTTPRoute keep being synced, the route is NotAccepted, no Secret is read in the denied namespace, and a
// grant that comes back restores the backend with the referenced value.
func TestMCPRouteController_Reconcile_SecretRefGrantRevoked(t *testing.T) {
	const routeNS, secretNS = "default", "shared"
	secretRef := &gwapiv1.SecretObjectReference{Name: "some-secret", Namespace: ptr.To(gwapiv1.Namespace(secretNS))}
	tests := []struct {
		name   string
		apiKey *aigv1b1.MCPBackendAPIKey
		// wantCredential is the credential Secret value while granted; empty on the query-parameter path.
		wantCredential string
		// wantRewrite is the per-backend HTTPRoute's full-path rewrite while granted (query-parameter path).
		wantRewrite string
	}{
		{name: "authorization header", apiKey: &aigv1b1.MCPBackendAPIKey{SecretRef: secretRef}, wantCredential: "Bearer shared-value"},
		{name: "custom header", apiKey: &aigv1b1.MCPBackendAPIKey{SecretRef: secretRef, Header: ptr.To("X-API-Key")}, wantCredential: "shared-value"},
		{name: "query parameter", apiKey: &aigv1b1.MCPBackendAPIKey{SecretRef: secretRef, QueryParam: ptr.To("api_key")}, wantRewrite: "/mcp?api_key=shared-value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCP(t)
			kubeClient := fakekube.NewClientset(
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: routeNS}, Data: map[string][]byte{"apiKey": []byte("route-ns-value")}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: secretNS}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
			)
			secretReads := countSecretReads(kubeClient)
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
			require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: routeNS}}))

			route := &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: routeNS},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
					BackendRefs: []aigv1b1.MCPRouteBackendRef{
						{
							BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-granted", Namespace: ptr.To(gwapiv1.Namespace(routeNS))},
							SecurityPolicy:         &aigv1b1.MCPBackendSecurityPolicy{APIKey: tt.apiKey},
						},
						{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-plain", Namespace: ptr.To(gwapiv1.Namespace(routeNS))}},
						{
							BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-inline", Namespace: ptr.To(gwapiv1.Namespace(routeNS))},
							SecurityPolicy: &aigv1b1.MCPBackendSecurityPolicy{APIKey: &aigv1b1.MCPBackendAPIKey{
								Inline: ptr.To("inline-key"), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent),
							}},
						},
					},
				},
			}
			require.NoError(t, fakeClient.Create(t.Context(), route))
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}
			const notPermitted = "backend svc-granted: cross-namespace reference from MCPRoute in namespace default to Secret some-secret in namespace shared is not permitted"

			// requireConverged asserts the never-granted state: nothing for svc-granted, everything else in place.
			requireConverged := func() {
				t.Helper()
				requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, notPermitted,
					"must allow MCPRoute from namespace default to reference Secret in namespace shared")
				requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-granted", false, "")
				requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-plain", true, "")
				requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-inline", true, "Bearer inline-key")
				var mainRoute gwapiv1.HTTPRoute
				require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPMainHTTPRoutePrefix + route.Name, Namespace: routeNS}, &mainRoute))
				requireUserSecretExists(t, kubeClient, routeNS, "some-secret")
				requireUserSecretExists(t, kubeClient, secretNS, "some-secret")
				var routes gwapiv1.HTTPRouteList
				require.NoError(t, fakeClient.List(t.Context(), &routes, client.InNamespace(routeNS)))
				for i := range routes.Items {
					for _, rule := range routes.Items[i].Spec.Rules {
						for _, f := range rule.Filters {
							if f.URLRewrite != nil && f.URLRewrite.Path != nil && f.URLRewrite.Path.ReplaceFullPath != nil {
								require.NotContains(t, *f.URLRewrite.Path.ReplaceFullPath, "shared-value", "the key must not remain in any HTTPRoute")
							}
						}
					}
				}
			}
			// requireGranted asserts the granted state of svc-granted.
			requireGranted := func() {
				t.Helper()
				requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeAccepted)
				requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-granted", true, tt.wantCredential)
				if tt.wantRewrite == "" {
					return
				}
				var httpRoute gwapiv1.HTTPRoute
				require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpPerBackendRefHTTPRouteName(route.Name, "svc-granted"), Namespace: routeNS}, &httpRoute))
				require.Len(t, httpRoute.Spec.Rules, 1)
				var rewrite string
				for _, f := range httpRoute.Spec.Rules[0].Filters {
					if f.Type == gwapiv1.HTTPRouteFilterURLRewrite {
						rewrite = *f.URLRewrite.Path.ReplaceFullPath
					}
				}
				require.Equal(t, tt.wantRewrite, rewrite)
			}

			// Never granted: the other backends are synced, the denied one has nothing.
			_, err := c.Reconcile(t.Context(), req)
			require.ErrorContains(t, err, notPermitted)
			requireConverged()
			require.Zero(t, secretReads[secretNS], "no Secret may be read in the denied namespace")

			// Granted.
			grant := mcpRouteSecretGrant(secretNS, routeNS, "some-secret")
			require.NoError(t, fakeClient.Create(t.Context(), grant))
			_, err = c.Reconcile(t.Context(), req)
			require.NoError(t, err)
			requireGranted()
			require.Positive(t, secretReads[secretNS])

			// Revoked: the generated objects go and the rest of the route is still synced (a Gateway event is sent).
			require.NoError(t, fakeClient.Delete(t.Context(), grant))
			readsWhileGranted := secretReads[secretNS]
			eventsBefore := len(eventCh.Ch)
			_, err = c.Reconcile(t.Context(), req)
			require.ErrorContains(t, err, notPermitted)
			requireConverged()
			require.Equal(t, readsWhileGranted, secretReads[secretNS], "no Secret may be read while denied")
			require.Greater(t, len(eventCh.Ch), eventsBefore, "the Gateway must still be synced")

			// A restart (a fresh controller on the same state) keeps the converged state and recreates nothing.
			_, err = NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch).Reconcile(t.Context(), req)
			require.ErrorContains(t, err, notPermitted)
			requireConverged()
			require.Equal(t, readsWhileGranted, secretReads[secretNS])

			// Objects left behind by a controller that stopped before the revocation go on the first reconcile.
			require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: mcpPerBackendRefHTTPRouteName(route.Name, "svc-granted"), Namespace: routeNS}}))
			require.NoError(t, fakeClient.Create(t.Context(), &egv1a1.HTTPRouteFilter{ObjectMeta: metav1.ObjectMeta{Name: mcpBackendRefFilterName(route, "svc-granted"), Namespace: routeNS}}))
			_, err = kubeClient.CoreV1().Secrets(routeNS).Create(t.Context(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: mcpCredentialSecretName(route, "svc-granted"), Namespace: routeNS},
				Data:       map[string][]byte{egv1a1.InjectedCredentialKey: []byte("stale")},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = c.Reconcile(t.Context(), req)
			require.ErrorContains(t, err, notPermitted)
			requireConverged()

			// Granted again, narrowed to another Secret name, restored.
			grant = mcpRouteSecretGrant(secretNS, routeNS, "some-secret")
			require.NoError(t, fakeClient.Create(t.Context(), grant))
			_, err = c.Reconcile(t.Context(), req)
			require.NoError(t, err)
			requireGranted()

			updateGrant := func(mutate func(*gwapiv1b1.ReferenceGrant)) {
				t.Helper()
				var current gwapiv1b1.ReferenceGrant
				require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(grant), &current))
				mutate(&current)
				require.NoError(t, fakeClient.Update(t.Context(), &current))
			}
			updateGrant(func(g *gwapiv1b1.ReferenceGrant) { g.Spec.To[0].Name = ptr.To(gwapiv1b1.ObjectName("another-secret")) })
			_, err = c.Reconcile(t.Context(), req)
			require.ErrorContains(t, err, notPermitted)
			requireConverged()
			updateGrant(func(g *gwapiv1b1.ReferenceGrant) { g.Spec.To[0].Name = ptr.To(gwapiv1b1.ObjectName("some-secret")) })
			_, err = c.Reconcile(t.Context(), req)
			require.NoError(t, err)
			requireGranted()
		})
	}
}

// TestMCPRouteController_Reconcile_SecretRefGrantRevokedMixedRoute verifies that only the backends whose
// grant is missing are converged, each backend's objects following its own grant.
func TestMCPRouteController_Reconcile_SecretRefGrantRevokedMixedRoute(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	kubeClient := fakekube.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "shared"}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "other"}, Data: map[string][]byte{"apiKey": []byte("other-value")}},
	)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				mcpRouteSecretRefBackend("svc-a", "default", "shared", "some-secret"),
				mcpRouteSecretRefBackend("svc-b", "default", "other", "some-secret"),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}
	const deniedA = "backend svc-a: cross-namespace reference from MCPRoute in namespace default to Secret some-secret in namespace shared is not permitted"
	const deniedB = "backend svc-b: cross-namespace reference from MCPRoute in namespace default to Secret some-secret in namespace other is not permitted"

	// Only svc-a is granted: svc-b is denied and has nothing, svc-a is complete.
	grantA := mcpRouteSecretGrant("shared", "default", "some-secret")
	require.NoError(t, fakeClient.Create(t.Context(), grantA))
	_, err := c.Reconcile(t.Context(), req)
	require.ErrorContains(t, err, deniedB)
	require.NotContains(t, err.Error(), "backend svc-a:")
	requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, deniedB)
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-a", true, "Bearer shared-value")
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-b", false, "")

	// Both granted.
	grantB := mcpRouteSecretGrant("other", "default", "some-secret")
	require.NoError(t, fakeClient.Create(t.Context(), grantB))
	_, err = c.Reconcile(t.Context(), req)
	require.NoError(t, err)
	requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeAccepted)
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-a", true, "Bearer shared-value")
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-b", true, "Bearer other-value")

	// svc-a revoked: only its objects go.
	require.NoError(t, fakeClient.Delete(t.Context(), grantA))
	_, err = c.Reconcile(t.Context(), req)
	require.ErrorContains(t, err, deniedA)
	require.NotContains(t, err.Error(), "backend svc-b:")
	requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, deniedA)
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-a", false, "")
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-b", true, "Bearer other-value")

	// Both revoked in one reconcile: both converge and the status names both.
	require.NoError(t, fakeClient.Delete(t.Context(), grantB))
	_, err = c.Reconcile(t.Context(), req)
	require.ErrorContains(t, err, deniedA)
	require.ErrorContains(t, err, deniedB)
	requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, deniedA, deniedB)
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-a", false, "")
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-b", false, "")
	requireUserSecretExists(t, kubeClient, "shared", "some-secret")
	requireUserSecretExists(t, kubeClient, "other", "some-secret")
}

// TestMCPRouteController_Reconcile_SecretRefGrantRevokedAfterFailingBackend verifies that a revoked
// backend converges even when a backend earlier in the spec has an unrelated persistent error that stops
// the per-backend loop, that the failing backend keeps its last good objects, and that the status
// reports the denial next to that error.
func TestMCPRouteController_Reconcile_SecretRefGrantRevokedAfterFailingBackend(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	kubeClient := fakekube.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "broken-secret", Namespace: "default"}, Data: map[string][]byte{"apiKey": []byte("broken-value")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "shared"}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
	)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				mcpRouteSecretRefBackend("svc-broken", "default", "", "broken-secret"),
				mcpRouteSecretRefBackend("svc-revoked", "default", "shared", "some-secret"),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}
	grant := mcpRouteSecretGrant("shared", "default", "some-secret")
	require.NoError(t, fakeClient.Create(t.Context(), grant))
	_, err := c.Reconcile(t.Context(), req)
	require.NoError(t, err)
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-broken", true, "Bearer broken-value")
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-revoked", true, "Bearer shared-value")

	// The same-namespace Secret of the first backend disappears and the grant of the second is revoked.
	require.NoError(t, kubeClient.CoreV1().Secrets("default").Delete(t.Context(), "broken-secret", metav1.DeleteOptions{}))
	require.NoError(t, fakeClient.Delete(t.Context(), grant))
	_, err = c.Reconcile(t.Context(), req)
	require.ErrorContains(t, err, `failed to get secret for API key: secrets "broken-secret" not found`)
	require.ErrorContains(t, err, notPermittedMessage("svc-revoked", "default", "some-secret", "shared"))
	var notPermitted *ReferenceNotPermittedError
	require.ErrorAs(t, err, &notPermitted, "the joined error must still unwrap to the denial")
	requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, `secrets "broken-secret" not found`, notPermittedMessage("svc-revoked", "default", "some-secret", "shared"))
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-revoked", false, "")
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-broken", true, "Bearer broken-value")
}

// TestMCPRouteController_Reconcile_NonGrantErrorsKeepResources verifies the stated non-goal: every error
// other than a denied ReferenceGrant keeps the objects of the last successful reconcile, and a failed
// cleanup of a denied backend is retried by the next reconcile.
func TestMCPRouteController_Reconcile_NonGrantErrorsKeepResources(t *testing.T) {
	type fixture struct {
		fakeClient client.Client
		kubeClient *fakekube.Clientset
		c          *MCPRouteController
		route      *aigv1b1.MCPRoute
		req        reconcile.Request
		grant      *gwapiv1b1.ReferenceGrant
	}
	// newGrantedFixture reconciles a route with a granted cross-namespace backend and a same-namespace
	// backend to the Accepted state.
	newGrantedFixture := func(t *testing.T) fixture {
		t.Helper()
		f := fixture{
			fakeClient: requireNewFakeClientWithIndexesForMCP(t),
			kubeClient: fakekube.NewClientset(
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "shared"}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "local-secret", Namespace: "default"}, Data: map[string][]byte{"apiKey": []byte("local-value")}},
			),
		}
		eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
		f.c = NewMCPRouteController(f.fakeClient, f.kubeClient, logr.Discard(), eventCh.Ch)
		require.NoError(t, f.fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
		f.route = &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
				BackendRefs: []aigv1b1.MCPRouteBackendRef{
					mcpRouteSecretRefBackend("svc-a", "default", "shared", "some-secret"),
					mcpRouteSecretRefBackend("svc-local", "default", "", "local-secret"),
				},
			},
		}
		require.NoError(t, f.fakeClient.Create(t.Context(), f.route))
		f.req = reconcile.Request{NamespacedName: client.ObjectKeyFromObject(f.route)}
		f.grant = mcpRouteSecretGrant("shared", "default", "some-secret")
		require.NoError(t, f.fakeClient.Create(t.Context(), f.grant))
		_, err := f.c.Reconcile(t.Context(), f.req)
		require.NoError(t, err)
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-a", true, "Bearer shared-value")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-local", true, "Bearer local-value")
		return f
	}

	t.Run("transient error reading the referenced Secret", func(t *testing.T) {
		f := newGrantedFixture(t)
		failing := true
		f.kubeClient.PrependReactor("get", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if failing && a.GetNamespace() == "shared" {
				return true, nil, apierrors.NewInternalError(errors.New("apiserver unavailable"))
			}
			return false, nil, nil
		})
		_, err := f.c.Reconcile(t.Context(), f.req)
		require.ErrorContains(t, err, "failed to get secret for API key")
		require.ErrorContains(t, err, "apiserver unavailable")
		requireMCPRouteCondition(t, f.fakeClient, f.route, aigv1b1.ConditionTypeNotAccepted, "apiserver unavailable")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-a", true, "Bearer shared-value")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-local", true, "Bearer local-value")
		failing = false
		_, err = f.c.Reconcile(t.Context(), f.req)
		require.NoError(t, err)
		requireMCPRouteCondition(t, f.fakeClient, f.route, aigv1b1.ConditionTypeAccepted)
	})

	t.Run("referenced Secret deleted while granted", func(t *testing.T) {
		f := newGrantedFixture(t)
		require.NoError(t, f.kubeClient.CoreV1().Secrets("shared").Delete(t.Context(), "some-secret", metav1.DeleteOptions{}))
		_, err := f.c.Reconcile(t.Context(), f.req)
		require.ErrorContains(t, err, `secrets "some-secret" not found`)
		requireMCPRouteCondition(t, f.fakeClient, f.route, aigv1b1.ConditionTypeNotAccepted, `secrets "some-secret" not found`)
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-a", true, "Bearer shared-value")
	})

	t.Run("transient error listing ReferenceGrants", func(t *testing.T) {
		f := newGrantedFixture(t)
		inner, ok := f.fakeClient.(client.WithWatch)
		require.True(t, ok)
		failing := true
		listErr := errors.New("cache unavailable")
		c := NewMCPRouteController(interceptor.NewClient(inner, interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, isGrants := list.(*gwapiv1b1.ReferenceGrantList); isGrants && failing {
					return listErr
				}
				return cl.List(ctx, list, opts...)
			},
		}), f.kubeClient, logr.Discard(), internaltesting.NewControllerEventChan[*gwapiv1.Gateway]().Ch)
		_, err := c.Reconcile(t.Context(), f.req)
		require.ErrorContains(t, err, "failed to validate the Secret reference of backend svc-a")
		require.ErrorIs(t, err, listErr)
		requireMCPRouteCondition(t, f.fakeClient, f.route, aigv1b1.ConditionTypeNotAccepted, "cache unavailable")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-a", true, "Bearer shared-value")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-local", true, "Bearer local-value")
		failing = false
		_, err = c.Reconcile(t.Context(), f.req)
		require.NoError(t, err)
		requireMCPRouteCondition(t, f.fakeClient, f.route, aigv1b1.ConditionTypeAccepted)
	})

	t.Run("failed cleanup of a denied backend is retried", func(t *testing.T) {
		f := newGrantedFixture(t)
		credName := mcpCredentialSecretName(f.route, "svc-a")
		failing := true
		f.kubeClient.PrependReactor("delete", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if del, ok := a.(k8stesting.DeleteAction); ok && failing && del.GetName() == credName {
				return true, nil, apierrors.NewInternalError(errors.New("apiserver unavailable"))
			}
			return false, nil, nil
		})
		require.NoError(t, f.fakeClient.Delete(t.Context(), f.grant))
		_, err := f.c.Reconcile(t.Context(), f.req)
		require.ErrorContains(t, err, "failed to delete the resources of backend svc-a")
		require.ErrorContains(t, err, "failed to delete credential secret "+credName)
		var httpRoute gwapiv1.HTTPRoute
		require.True(t, apierrors.IsNotFound(f.fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpPerBackendRefHTTPRouteName(f.route.Name, "svc-a"), Namespace: "default"}, &httpRoute)))
		_, err = f.kubeClient.CoreV1().Secrets("default").Get(t.Context(), credName, metav1.GetOptions{})
		require.NoError(t, err, "the credential Secret could not be deleted yet")
		// The next reconcile finishes the cleanup and reports the denial.
		failing = false
		_, err = f.c.Reconcile(t.Context(), f.req)
		require.ErrorContains(t, err, "backend svc-a: cross-namespace reference from MCPRoute in namespace default to Secret some-secret in namespace shared is not permitted")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-a", false, "")
		requirePerBackendResources(t, f.fakeClient, f.kubeClient, f.route, "svc-local", true, "Bearer local-value")
	})
}

// TestMCPRouteController_Reconcile_DeniedRouteDeleted verifies that deleting an MCPRoute with a denied
// backend still runs the finalizer path only: no error, the route and the shared Backend are gone.
func TestMCPRouteController_Reconcile_DeniedRouteDeleted(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	kubeClient := fakekube.NewClientset()
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs:  []gwapiv1.ParentReference{{Name: "gw"}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{mcpRouteSecretRefBackend("svc-a", "default", "shared", "some-secret")},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}
	_, err := c.Reconcile(t.Context(), req)
	require.ErrorContains(t, err, "is not permitted")
	var current aigv1b1.MCPRoute
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &current))
	require.Contains(t, current.Finalizers, aiGatewayControllerFinalizer)

	require.NoError(t, fakeClient.Delete(t.Context(), &current))
	_, err = c.Reconcile(t.Context(), req)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), req.NamespacedName, &current)))
	var sharedBackend egv1a1.Backend
	require.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: "default"}, &sharedBackend)))
}

// notPermittedMessage is the error and status text for a backend whose cross-namespace secretRef has
// no ReferenceGrant: "backend <name>: " followed by the ReferenceNotPermittedError.
func notPermittedMessage(backend, routeNamespace, secretName, secretNamespace string) string {
	return fmt.Sprintf("backend %s: %s", backend, (&ReferenceNotPermittedError{
		FromGroup: string(aiServiceBackendGroup), FromKind: mcpRouteKind, FromNamespace: routeNamespace,
		TargetGroup: string(secretGroup), TargetKind: secretKind, TargetName: secretName, TargetNamespace: secretNamespace,
	}).Error())
}

// TestMCPRouteController_Reconcile_CollidingBackendLeavesTheOtherRouteAlone verifies that a backend
// whose derived object names collide with another MCPRoute's backend never deletes or rewrites that
// route's objects: route "github-mcp" with backend "prod" and route "github" with backend "mcp-prod"
// both derive "<prefix>github-mcp-prod". The colliding backend fails with its denial or with an error
// that names the collision, and the first route keeps its HTTPRoute, its HTTPRouteFilter with the
// credential injection, and its credential value on every retry.
func TestMCPRouteController_Reconcile_CollidingBackendLeavesTheOtherRouteAlone(t *testing.T) {
	tests := []struct {
		name      string
		colliding aigv1b1.MCPRouteBackendRef
		expError  string
	}{
		{
			name:      "denied cross-namespace secretRef",
			colliding: mcpRouteSecretRefBackend("mcp-prod", "default", "shared", "token-b"),
			expError:  notPermittedMessage("mcp-prod", "default", "token-b", "shared"),
		},
		{
			name:      "readable secretRef",
			colliding: mcpRouteSecretRefBackend("mcp-prod", "default", "", "token-b"),
			expError:  "credential secret ai-eg-mcp-cred-github-mcp-prod is controlled by another MCPRoute",
		},
		{
			name: "inline key with the default injection policy",
			colliding: aigv1b1.MCPRouteBackendRef{
				BackendObjectReference: gwapiv1.BackendObjectReference{Name: "mcp-prod", Namespace: ptr.To(gwapiv1.Namespace("default"))},
				SecurityPolicy:         &aigv1b1.MCPBackendSecurityPolicy{APIKey: &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("b-value")}},
			},
			expError: "HTTPRouteFilter ai-eg-mcp-brf-github-mcp-prod is controlled by another MCPRoute",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCP(t)
			kubeClient := fakekube.NewClientset(
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-a", Namespace: "default"}, Data: map[string][]byte{"apiKey": []byte("a-value")}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-b", Namespace: "default"}, Data: map[string][]byte{"apiKey": []byte("b-value")}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-b", Namespace: "shared"}, Data: map[string][]byte{"apiKey": []byte("shared-b-value")}},
			)
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
			require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
			newRoute := func(name string, backend aigv1b1.MCPRouteBackendRef) *aigv1b1.MCPRoute {
				return &aigv1b1.MCPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       aigv1b1.MCPRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}}, BackendRefs: []aigv1b1.MCPRouteBackendRef{backend}},
				}
			}
			first := newRoute("github-mcp", mcpRouteSecretRefBackend("prod", "default", "", "token-a"))
			second := newRoute("github", tt.colliding)
			require.Equal(t, mcpPerBackendRefHTTPRouteName(first.Name, "prod"), mcpPerBackendRefHTTPRouteName(second.Name, "mcp-prod"))
			require.NoError(t, fakeClient.Create(t.Context(), first))
			require.NoError(t, fakeClient.Create(t.Context(), second))

			_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(first)})
			require.NoError(t, err)
			requireFirstIntact := func() {
				t.Helper()
				requirePerBackendResources(t, fakeClient, kubeClient, first, "prod", true, "Bearer a-value")
				var filter egv1a1.HTTPRouteFilter
				require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: mcpBackendRefFilterName(first, "prod"), Namespace: "default"}, &filter))
				require.NotNil(t, filter.Spec.CredentialInjection, "the first route's filter must keep its credential injection")
				require.Equal(t, mcpCredentialSecretName(first, "prod"), string(filter.Spec.CredentialInjection.Credential.ValueRef.Name))
			}
			requireFirstIntact()

			// The second route fails on every reconcile and every reconcile must leave the first route alone.
			for range 2 {
				_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(second)})
				require.ErrorContains(t, err, tt.expError)
				requireFirstIntact()
			}
			requireMCPRouteCondition(t, fakeClient, second, aigv1b1.ConditionTypeNotAccepted, tt.expError)
		})
	}
}

// TestMCPRouteController_deletePerBackendResources verifies which objects the per-backend cleanup
// removes: those the MCPRoute controls and those without a controller reference, never those another
// object controls, whether that is another MCPRoute, a same-named MCPRoute of another API group or
// another kind.
func TestMCPRouteController_deletePerBackendResources(t *testing.T) {
	route := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default", UID: "uid-a"}}
	controller := func(apiVersion, kind, name string) *metav1.OwnerReference {
		return &metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: ptr.To(true)}
	}
	tests := []struct {
		name string
		// owner becomes the controller reference of the three objects; nil leaves them without one.
		owner      *metav1.OwnerReference
		expDeleted bool
	}{
		{name: "controlled by the route", owner: controller(aigv1b1.SchemeGroupVersion.String(), "MCPRoute", "route-a"), expDeleted: true},
		{name: "controlled by an earlier MCPRoute of the same name", owner: &metav1.OwnerReference{APIVersion: aigv1b1.SchemeGroupVersion.String(), Kind: "MCPRoute", Name: "route-a", UID: "uid-previous", Controller: ptr.To(true)}, expDeleted: true},
		{name: "no controller reference", expDeleted: true},
		{name: "controlled by another route", owner: controller(aigv1b1.SchemeGroupVersion.String(), "MCPRoute", "route-b")},
		{name: "controlled by a same-named MCPRoute of another API group", owner: controller("other.example.com/v1", "MCPRoute", "route-a")},
		{name: "controlled by another kind", owner: controller("apps/v1", "Deployment", "route-a")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCP(t)
			kubeClient := fakekube.NewClientset()
			c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), internaltesting.NewControllerEventChan[*gwapiv1.Gateway]().Ch)
			meta := func(name string) metav1.ObjectMeta {
				m := metav1.ObjectMeta{Name: name, Namespace: route.Namespace}
				if tt.owner != nil {
					m.OwnerReferences = []metav1.OwnerReference{*tt.owner}
				}
				return m
			}
			httpRouteName := mcpPerBackendRefHTTPRouteName(route.Name, "svc")
			require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.HTTPRoute{ObjectMeta: meta(httpRouteName)}))
			require.NoError(t, fakeClient.Create(t.Context(), &egv1a1.HTTPRouteFilter{ObjectMeta: meta(mcpBackendRefFilterName(route, "svc"))}))
			_, err := kubeClient.CoreV1().Secrets(route.Namespace).Create(t.Context(), &corev1.Secret{
				ObjectMeta: meta(mcpCredentialSecretName(route, "svc")),
				Data:       map[string][]byte{egv1a1.InjectedCredentialKey: []byte("kept")},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			require.NoError(t, c.deletePerBackendResources(t.Context(), route, httpRouteName))
			requirePerBackendResources(t, fakeClient, kubeClient, route, "svc", !tt.expDeleted, "kept")
			// Repeating the call changes nothing.
			require.NoError(t, c.deletePerBackendResources(t.Context(), route, httpRouteName))
			requirePerBackendResources(t, fakeClient, kubeClient, route, "svc", !tt.expDeleted, "kept")
		})
	}
}

// TestMCPRouteController_deletePerBackendResources_KeepsReplacedObjects verifies the UID precondition:
// when the object the cleanup read has been replaced since (the read came from a cache, or another
// MCPRoute re-created the object in between), the API server answers Conflict and the replacement
// survives. The interceptors play the cache (stale UID on reads) and the API server (precondition
// check on deletes).
func TestMCPRouteController_deletePerBackendResources_KeepsReplacedObjects(t *testing.T) {
	route := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default", UID: "uid-a"}}
	inner, ok := requireNewFakeClientWithIndexesForMCP(t).(client.WithWatch)
	require.True(t, ok)
	conflict := func(name string) error {
		return apierrors.NewConflict(schema.GroupResource{Resource: "objects"}, name, errors.New("the UID in the precondition does not match the UID in record"))
	}
	fakeClient := interceptor.NewClient(inner, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			obj.SetUID("stale-" + obj.GetUID())
			return nil
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			live := obj.DeepCopyObject().(client.Object)
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
				return err
			}
			do := &client.DeleteOptions{}
			do.ApplyOptions(opts)
			if do.Preconditions != nil && do.Preconditions.UID != nil && *do.Preconditions.UID != live.GetUID() {
				return conflict(obj.GetName())
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	kubeClient := fakekube.NewClientset()
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	kubeClient.PrependReactor("get", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		get := a.(k8stesting.GetAction)
		obj, err := kubeClient.Tracker().Get(secretsGVR, get.GetNamespace(), get.GetName())
		if err != nil {
			return true, nil, err
		}
		stale := obj.DeepCopyObject().(*corev1.Secret)
		stale.UID = "stale-" + stale.UID
		return true, stale, nil
	})
	kubeClient.PrependReactor("delete", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		del := a.(k8stesting.DeleteActionImpl)
		obj, err := kubeClient.Tracker().Get(secretsGVR, del.GetNamespace(), del.GetName())
		if err != nil {
			return true, nil, err
		}
		if p := del.DeleteOptions.Preconditions; p != nil && p.UID != nil && *p.UID != obj.(*corev1.Secret).UID {
			return true, nil, conflict(del.GetName())
		}
		return false, nil, nil
	})
	c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), internaltesting.NewControllerEventChan[*gwapiv1.Gateway]().Ch)
	owned := func(name string, uid types.UID) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: route.Namespace, UID: uid, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: aigv1b1.SchemeGroupVersion.String(), Kind: "MCPRoute", Name: route.Name, UID: route.UID, Controller: ptr.To(true),
		}}}
	}
	httpRouteName := mcpPerBackendRefHTTPRouteName(route.Name, "svc")
	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.HTTPRoute{ObjectMeta: owned(httpRouteName, "uid-route")}))
	require.NoError(t, fakeClient.Create(t.Context(), &egv1a1.HTTPRouteFilter{ObjectMeta: owned(mcpBackendRefFilterName(route, "svc"), "uid-filter")}))
	_, err := kubeClient.CoreV1().Secrets(route.Namespace).Create(t.Context(), &corev1.Secret{
		ObjectMeta: owned(mcpCredentialSecretName(route, "svc"), "uid-secret"),
		Data:       map[string][]byte{egv1a1.InjectedCredentialKey: []byte("kept")},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	// Every delete is answered with Conflict, which the cleanup treats as "replaced, judged next time".
	require.NoError(t, c.deletePerBackendResources(t.Context(), route, httpRouteName))
	requirePerBackendResources(t, inner, kubeClient, route, "svc", true, "kept")
}

// TestMCPRouteController_Reconcile_ManyDenialsFitTheStatus revokes the grant of routes whose backends
// all read the same foreign Secret. The CRD caps a condition message at 32768 characters, so the status
// lists the denials up to a limit and counts the rest; it must still turn NotAccepted. A route with one
// denial more than the limit lists them all. The fake client rejects longer messages the way the API
// server does.
func TestMCPRouteController_Reconcile_ManyDenialsFitTheStatus(t *testing.T) {
	const conditionMessageLimit = 32768
	for _, backends := range []int{maxReportedDenials + 1, 150} {
		t.Run(fmt.Sprintf("%d denied backends", backends), func(t *testing.T) {
			inner, ok := requireNewFakeClientWithIndexesForMCP(t).(client.WithWatch)
			require.True(t, ok)
			fakeClient := interceptor.NewClient(inner, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if route, ok := obj.(*aigv1b1.MCPRoute); ok {
						for i, cond := range route.Status.Conditions {
							if len(cond.Message) > conditionMessageLimit {
								return fmt.Errorf("status.conditions[%d].message: Too long: may not be more than %d bytes", i, conditionMessageLimit)
							}
						}
					}
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
			})
			kubeClient := fakekube.NewClientset(
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "shared"}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
			)
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
			require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
			grant := mcpRouteSecretGrant("shared", "default", "")
			require.NoError(t, fakeClient.Create(t.Context(), grant))

			route := &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
				Spec:       aigv1b1.MCPRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}}},
			}
			for i := range backends {
				route.Spec.BackendRefs = append(route.Spec.BackendRefs,
					mcpRouteSecretRefBackend(gwapiv1.ObjectName(fmt.Sprintf("svc-%03d", i)), "default", "shared", "some-secret"))
			}
			require.NoError(t, fakeClient.Create(t.Context(), route))
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}
			_, err := c.Reconcile(t.Context(), req)
			require.NoError(t, err)
			requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeAccepted)

			require.NoError(t, fakeClient.Delete(t.Context(), grant))
			_, err = c.Reconcile(t.Context(), req)
			require.Error(t, err)
			var current aigv1b1.MCPRoute
			require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &current))
			require.Len(t, current.Status.Conditions, 1)
			cond := current.Status.Conditions[0]
			require.Equal(t, aigv1b1.ConditionTypeNotAccepted, cond.Type)
			require.LessOrEqual(t, len(cond.Message), conditionMessageLimit)
			if backends > maxReportedDenials+1 {
				require.Contains(t, cond.Message, fmt.Sprintf("backend svc-%03d: ", maxReportedDenials-1))
				require.NotContains(t, cond.Message, fmt.Sprintf("backend svc-%03d: ", maxReportedDenials))
				require.Contains(t, cond.Message, fmt.Sprintf("%d more backends have a Secret reference that is not permitted", backends-maxReportedDenials))
			} else {
				for i := range backends {
					require.Contains(t, cond.Message, fmt.Sprintf("backend svc-%03d: ", i))
				}
				require.NotContains(t, cond.Message, "more backends")
			}
			requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-000", false, "")
			requirePerBackendResources(t, fakeClient, kubeClient, route, gwapiv1.ObjectName(fmt.Sprintf("svc-%03d", backends-1)), false, "")
		})
	}
}

// TestMCPRouteController_Reconcile_SweepErrorKeepsEarlierDenials verifies that when the revocation
// sweep fails on a later backend, the denials it already acted on stay in the error and the status:
// their objects are gone, so the status must say why.
func TestMCPRouteController_Reconcile_SweepErrorKeepsEarlierDenials(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	kubeClient := fakekube.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret", Namespace: "shared"}, Data: map[string][]byte{"apiKey": []byte("shared-value")}},
	)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, kubeClient, logr.Discard(), eventCh.Ch)
	require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				mcpRouteSecretRefBackend("svc-a", "default", "shared", "some-secret"),
				mcpRouteSecretRefBackend("svc-b", "default", "shared", "some-secret"),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), route))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}
	grant := mcpRouteSecretGrant("shared", "default", "")
	require.NoError(t, fakeClient.Create(t.Context(), grant))
	_, err := c.Reconcile(t.Context(), req)
	require.NoError(t, err)

	// The grant goes and svc-b's credential Secret cannot be deleted for a while.
	failing := true
	credB := mcpCredentialSecretName(route, "svc-b")
	kubeClient.PrependReactor("delete", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if del, ok := a.(k8stesting.DeleteAction); ok && failing && del.GetName() == credB {
			return true, nil, apierrors.NewInternalError(errors.New("apiserver unavailable"))
		}
		return false, nil, nil
	})
	require.NoError(t, fakeClient.Delete(t.Context(), grant))
	_, err = c.Reconcile(t.Context(), req)
	deniedA := notPermittedMessage("svc-a", "default", "some-secret", "shared")
	require.ErrorContains(t, err, "failed to delete the resources of backend svc-b")
	require.ErrorContains(t, err, deniedA)
	requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, "failed to delete the resources of backend svc-b", deniedA)
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-a", false, "")

	failing = false
	_, err = c.Reconcile(t.Context(), req)
	require.ErrorContains(t, err, deniedA)
	require.ErrorContains(t, err, notPermittedMessage("svc-b", "default", "some-secret", "shared"))
	requirePerBackendResources(t, fakeClient, kubeClient, route, "svc-b", false, "")
}

// TestMCPRouteController_Reconcile_EmptyInlineKey verifies that an empty inline key, which the CRD
// admits, is reported as an error on the paths that would otherwise read a Secret (injectionPolicy
// IfNotPresent and queryParam) instead of dereferencing the missing secretRef.
func TestMCPRouteController_Reconcile_EmptyInlineKey(t *testing.T) {
	for name, apiKey := range map[string]*aigv1b1.MCPBackendAPIKey{
		"injectionPolicy IfNotPresent": {Inline: ptr.To(""), InjectionPolicy: ptr.To(aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent)},
		"queryParam":                   {Inline: ptr.To(""), QueryParam: ptr.To("api_key")},
	} {
		t.Run(name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCP(t)
			c := NewMCPRouteController(fakeClient, fakekube.NewClientset(), logr.Discard(), internaltesting.NewControllerEventChan[*gwapiv1.Gateway]().Ch)
			require.NoError(t, fakeClient.Create(t.Context(), &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"}}))
			route := &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
					BackendRefs: []aigv1b1.MCPRouteBackendRef{{
						BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-a", Namespace: ptr.To(gwapiv1.Namespace("default"))},
						SecurityPolicy:         &aigv1b1.MCPBackendSecurityPolicy{APIKey: apiKey},
					}},
				},
			}
			require.NoError(t, fakeClient.Create(t.Context(), route))
			_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)})
			require.ErrorContains(t, err, "apiKey has neither a non-empty inline value nor a secretRef")
			requireMCPRouteCondition(t, fakeClient, route, aigv1b1.ConditionTypeNotAccepted, "neither a non-empty inline value nor a secretRef")
		})
	}
}
