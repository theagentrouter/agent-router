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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fakekube "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

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
		// When set, the HTTPRouteFilter is expected to have credentialInjection configured (secretRef path).
		expCredentialValue string
		// expCredentialHeader is the expected header for the credential injection filter (secretRef path).
		expCredentialHeader *string
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
			name:               "secret ref API key default header",
			key:                &aigv1b1.MCPBackendAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "some-secret"}},
			expCredentialValue: "Bearer secretvalue",
			expFilterCount:     2,
			refPath:            ptr.To("/some/path"),
			expPath:            "/some/path",
		},
		{
			name:           "query param API key",
			key:            &aigv1b1.MCPBackendAPIKey{Inline: ptr.To("inline-key"), QueryParam: ptr.To("api_key")},
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
				require.True(t, *httpFilter.Spec.CredentialInjection.Overwrite)

				credSecret, getSecretErr := kubeClient.CoreV1().Secrets("default").Get(t.Context(), credSecretName, metav1.GetOptions{})
				require.NoError(t, getSecretErr)
				require.Equal(t, tt.expCredentialValue, string(credSecret.Data[egv1a1.InjectedCredentialKey]))

				// No plaintext should appear in any RequestHeaderModifier filter.
				for _, f := range httpRule.Filters {
					if f.RequestHeaderModifier != nil {
						for _, h := range f.RequestHeaderModifier.Set {
							require.NotContains(t, h.Value, "secretvalue", "plaintext API key must not appear in HTTPRoute")
						}
					}
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
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "", nil)
		require.NoError(t, err)

		var httpFilter egv1a1.HTTPRouteFilter
		err = c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: filterName}, &httpFilter)
		require.NoError(t, err)
		require.NotNil(t, httpFilter.Spec.URLRewrite)
		require.Nil(t, httpFilter.Spec.CredentialInjection)
	})

	t.Run("with credential injection", func(t *testing.T) {
		customHeader := "X-Custom-Key"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "managed-ref-primary", &customHeader)
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

	t.Run("deletes stale secret when transitioning away from credential injection", func(t *testing.T) {
		staleRefName := "stale-managed-ref"
		_, createErr := kubeClient.CoreV1().Secrets("default").Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: staleRefName, Namespace: "default"},
			Data:       map[string][]byte{egv1a1.InjectedCredentialKey: []byte("stale")},
		}, metav1.CreateOptions{})
		require.NoError(t, createErr)

		authorizationHeader := "Authorization"
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, staleRefName, &authorizationHeader)
		require.NoError(t, err)

		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, "", nil)
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
		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, oldRefName, &customHeader)
		require.NoError(t, err)

		err = ctrlr.ensureMCPBackendRefHTTPFilter(t.Context(), filterName, mcpRoute, newRefName, &customHeader)
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
				gotKey, readErr := ctrlr.readAPIKey(t.Context(), "default", tt.keySpec)
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
