// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy creates a fake client with indexes and
// status subresources needed for MCPBackendExtensionPolicy controller tests.
func requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t *testing.T) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1a1.MCPBackendExtensionPolicy{}).
		WithStatusSubresource(&aigv1b1.MCPRoute{}).
		WithStatusSubresource(&egv1a1.EnvoyExtensionPolicy{})
	err := ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	})
	require.NoError(t, err)
	return builder.Build()
}

// newTestMCPRouteForExtensionPolicy returns an MCPRoute in the default namespace with the given backends.
func newTestMCPRouteForExtensionPolicy(name string, backends ...gwapiv1.ObjectName) *aigv1b1.MCPRoute {
	route := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: "gw"}},
		},
	}
	for _, backend := range backends {
		route.Spec.BackendRefs = append(route.Spec.BackendRefs, aigv1b1.MCPRouteBackendRef{
			BackendObjectReference: gwapiv1.BackendObjectReference{Name: backend},
		})
	}
	return route
}

// newTestMCPBackendExtensionPolicy returns an MCPBackendExtensionPolicy in the default namespace with one dynamic module
// filter and the given targetRefs.
func newTestMCPBackendExtensionPolicy(name string, targetRefs ...gwapiv1.LocalPolicyTargetReferenceWithSectionName) *aigv1a1.MCPBackendExtensionPolicy {
	return &aigv1a1.MCPBackendExtensionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec: aigv1a1.MCPBackendExtensionPolicySpec{
			TargetRefs: targetRefs,
			DynamicModule: []egv1a1.DynamicModule{{
				Name:       "test-module",
				FilterName: ptr.To("set_request_header"),
				Config:     &apiextensionsv1.JSON{Raw: []byte(`{"name":"x-ext","value":"v1"}`)},
			}},
		},
	}
}

// mcpRouteTargetRef returns a targetRef to the named MCPRoute, optionally narrowed to one backend.
func mcpRouteTargetRef(name string, sectionName ...string) gwapiv1.LocalPolicyTargetReferenceWithSectionName {
	ref := gwapiv1.LocalPolicyTargetReferenceWithSectionName{
		LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
			Group: "aigateway.envoyproxy.io",
			Kind:  "MCPRoute",
			Name:  gwapiv1.ObjectName(name),
		},
	}
	if len(sectionName) > 0 {
		ref.SectionName = ptr.To(gwapiv1.SectionName(sectionName[0]))
	}
	return ref
}

func reconcileMCPBackendExtensionPolicy(t *testing.T, c *MCPBackendExtensionPolicyController, name string) error {
	t.Helper()
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
	require.Equal(t, ctrl.Result{}, res)
	return err
}

func requireMCPBackendExtensionPolicyCondition(t *testing.T, cl client.Client, name, conditionType, message string) {
	t.Helper()
	var policy aigv1a1.MCPBackendExtensionPolicy
	require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Name: name, Namespace: "default"}, &policy))
	require.Len(t, policy.Status.Conditions, 1)
	require.Equal(t, conditionType, policy.Status.Conditions[0].Type)
	require.Equal(t, message, policy.Status.Conditions[0].Message)
}

// newTestPolicyAncestorStatus returns an ancestor status with the given Accepted condition, as Envoy Gateway reports it
// on an EnvoyExtensionPolicy.
func newTestPolicyAncestorStatus(status metav1.ConditionStatus, reason, message string) gwapiv1.PolicyAncestorStatus {
	return gwapiv1.PolicyAncestorStatus{
		AncestorRef:    gwapiv1.ParentReference{Name: "gw"},
		ControllerName: "gateway.envoyproxy.io/gatewayclass-controller",
		Conditions: []metav1.Condition{{
			Type:               string(gwapiv1.PolicyConditionAccepted),
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: metav1.Now(),
		}},
	}
}

func TestMCPBackendExtensionPolicyController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPRouteForExtensionPolicy("r", "a", "b")))
	policy := newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))
	policy.Spec.Backends = []egv1a1.ExtensionBackend{{
		Name:       "sts",
		BackendRef: gwapiv1.BackendObjectReference{Name: "sts", Port: ptr.To(gwapiv1.PortNumber(8080))},
	}}
	require.NoError(t, fakeClient.Create(t.Context(), policy))

	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))

	// The generated EnvoyExtensionPolicy selects the labeled per-backend HTTPRoutes and carries the extensions unchanged.
	var eep egv1a1.EnvoyExtensionPolicy
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPBackendExtensionPolicyPrefix + "p1", Namespace: "default"}, &eep))
	require.Equal(t, []egv1a1.TargetSelector{{
		Group:       ptr.To(gwapiv1.Group(gwapiv1.GroupName)),
		Kind:        "HTTPRoute",
		MatchLabels: map[string]string{internalapi.MCPBackendExtensionPolicyLabel: "p1"},
	}}, eep.Spec.TargetSelectors)
	require.Empty(t, eep.Spec.TargetRefs)
	require.Equal(t, policy.Spec.Backends, eep.Spec.Backends)
	require.Equal(t, policy.Spec.DynamicModule, eep.Spec.DynamicModule)
	require.True(t, metav1.IsControlledBy(&eep, policy))

	requireMCPBackendExtensionPolicyCondition(t, fakeClient, "p1", aigv1a1.ConditionTypeAccepted, "MCPRoute r: applied to backends: a, b")
}

func TestMCPBackendExtensionPolicyController_Reconcile_NotFound(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "nonexistent"))

	var eeps egv1a1.EnvoyExtensionPolicyList
	require.NoError(t, fakeClient.List(t.Context(), &eeps))
	require.Empty(t, eeps.Items)
}

func TestMCPBackendExtensionPolicyController_Reconcile_Deletion(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPRouteForExtensionPolicy("r", "a")))
	policy := newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))
	// A finalizer keeps the policy around with a DeletionTimestamp after the delete.
	policy.Finalizers = []string{"test.io/finalizer"}
	require.NoError(t, fakeClient.Create(t.Context(), policy))
	require.NoError(t, fakeClient.Delete(t.Context(), policy))

	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))

	var eeps egv1a1.EnvoyExtensionPolicyList
	require.NoError(t, fakeClient.List(t.Context(), &eeps))
	require.Empty(t, eeps.Items)
	var current aigv1a1.MCPBackendExtensionPolicy
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: "p1", Namespace: "default"}, &current))
	require.Empty(t, current.Status.Conditions)
}

func TestMCPBackendExtensionPolicyController_Reconcile_UpdatesEnvoyExtensionPolicy(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPRouteForExtensionPolicy("r", "a")))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))))
	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))

	// Envoy Gateway reports on the generated object.
	eepKey := client.ObjectKey{Name: internalapi.MCPBackendExtensionPolicyPrefix + "p1", Namespace: "default"}
	var eep egv1a1.EnvoyExtensionPolicy
	require.NoError(t, fakeClient.Get(t.Context(), eepKey, &eep))
	eep.Status.Ancestors = []gwapiv1.PolicyAncestorStatus{newTestPolicyAncestorStatus(metav1.ConditionTrue, "Accepted", "accepted")}
	require.NoError(t, fakeClient.Status().Update(t.Context(), &eep))

	// Change the extension.
	var policy aigv1a1.MCPBackendExtensionPolicy
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: "p1", Namespace: "default"}, &policy))
	policy.Spec.DynamicModule[0].Config = &apiextensionsv1.JSON{Raw: []byte(`{"name":"x-ext","value":"v2"}`)}
	require.NoError(t, fakeClient.Update(t.Context(), &policy))
	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))

	require.NoError(t, fakeClient.Get(t.Context(), eepKey, &eep))
	require.Equal(t, policy.Spec.DynamicModule, eep.Spec.DynamicModule)
	require.Len(t, eep.Status.Ancestors, 1, "Envoy Gateway's status must be kept")
}

func TestMCPBackendExtensionPolicyController_Reconcile_SkipsUnchangedWrites(t *testing.T) {
	inner := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	var eepUpdates, statusUpdates int
	fakeClient := interceptor.NewClient(inner.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*egv1a1.EnvoyExtensionPolicy); ok {
				eepUpdates++
			}
			return cl.Update(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*aigv1a1.MCPBackendExtensionPolicy); ok {
				statusUpdates++
			}
			return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPRouteForExtensionPolicy("r", "a")))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))))

	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))
	require.Equal(t, 0, eepUpdates, "the EnvoyExtensionPolicy is created, not updated")
	require.Equal(t, 1, statusUpdates)

	// Nothing changed: no writes.
	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))
	require.Equal(t, 0, eepUpdates)
	require.Equal(t, 1, statusUpdates)
}

func TestMCPBackendExtensionPolicyController_Reconcile_NotOwned(t *testing.T) {
	for _, tt := range []struct {
		name            string
		ownerReferences []metav1.OwnerReference
	}{
		{name: "no owner"},
		{
			name: "controlled by another object",
			ownerReferences: []metav1.OwnerReference{{
				APIVersion: aigv1a1.SchemeGroupVersion.String(),
				Kind:       "MCPBackendExtensionPolicy",
				Name:       "p1",
				UID:        "previous-p1-uid",
				Controller: ptr.To(true),
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
			c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

			require.NoError(t, fakeClient.Create(t.Context(), newTestMCPRouteForExtensionPolicy("r", "a")))
			require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))))
			foreignSpec := egv1a1.EnvoyExtensionPolicySpec{
				PolicyTargetReferences: egv1a1.PolicyTargetReferences{
					TargetSelectors: []egv1a1.TargetSelector{{Kind: "HTTPRoute", MatchLabels: map[string]string{"app": "other"}}},
				},
			}
			require.NoError(t, fakeClient.Create(t.Context(), &egv1a1.EnvoyExtensionPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:            internalapi.MCPBackendExtensionPolicyPrefix + "p1",
					Namespace:       "default",
					OwnerReferences: tt.ownerReferences,
				},
				Spec: foreignSpec,
			}))

			err := reconcileMCPBackendExtensionPolicy(t, c, "p1")
			require.ErrorContains(t, err, "already exists and is not owned by this MCPBackendExtensionPolicy")

			var policy aigv1a1.MCPBackendExtensionPolicy
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: "p1", Namespace: "default"}, &policy))
			require.Len(t, policy.Status.Conditions, 1)
			require.Equal(t, aigv1a1.ConditionTypeNotAccepted, policy.Status.Conditions[0].Type)
			require.Contains(t, policy.Status.Conditions[0].Message, "already exists and is not owned")

			var eep egv1a1.EnvoyExtensionPolicy
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPBackendExtensionPolicyPrefix + "p1", Namespace: "default"}, &eep))
			require.Equal(t, foreignSpec, eep.Spec, "a foreign EnvoyExtensionPolicy must not be taken over")
			require.Equal(t, tt.ownerReferences, eep.OwnerReferences)
		})
	}
}

func TestMCPBackendExtensionPolicyController_Reconcile_RejectedByEnvoyGateway(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)

	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPRouteForExtensionPolicy("r", "a")))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))))
	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))

	var eep egv1a1.EnvoyExtensionPolicy
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPBackendExtensionPolicyPrefix + "p1", Namespace: "default"}, &eep))
	eep.Status.Ancestors = []gwapiv1.PolicyAncestorStatus{newTestPolicyAncestorStatus(metav1.ConditionFalse, "Invalid", "dynamic module not found")}
	require.NoError(t, fakeClient.Status().Update(t.Context(), &eep))

	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))
	requireMCPBackendExtensionPolicyCondition(t, fakeClient, "p1", aigv1a1.ConditionTypeNotAccepted,
		"EnvoyExtensionPolicy ai-eg-mcp-bre-p1 rejected by Envoy Gateway: Invalid: dynamic module not found")

	// Envoy Gateway accepts it again.
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: internalapi.MCPBackendExtensionPolicyPrefix + "p1", Namespace: "default"}, &eep))
	eep.Status.Ancestors = []gwapiv1.PolicyAncestorStatus{newTestPolicyAncestorStatus(metav1.ConditionTrue, "Accepted", "accepted")}
	require.NoError(t, fakeClient.Status().Update(t.Context(), &eep))

	require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))
	requireMCPBackendExtensionPolicyCondition(t, fakeClient, "p1", aigv1a1.ConditionTypeAccepted, "MCPRoute r: applied to backends: a")
}

func TestMCPBackendExtensionPolicyController_Reconcile_TargetRefs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		routes    []*aigv1b1.MCPRoute
		policies  []*aigv1a1.MCPBackendExtensionPolicy
		expType   string
		expStatus string
	}{
		{
			name:      "MCPRoute not found",
			policies:  []*aigv1a1.MCPBackendExtensionPolicy{newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("missing"))},
			expType:   aigv1a1.ConditionTypeNotAccepted,
			expStatus: "MCPRoute missing not found",
		},
		{
			name:      "sectionName names no backend",
			routes:    []*aigv1b1.MCPRoute{newTestMCPRouteForExtensionPolicy("r", "a")},
			policies:  []*aigv1a1.MCPBackendExtensionPolicy{newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r", "z"))},
			expType:   aigv1a1.ConditionTypeNotAccepted,
			expStatus: "MCPRoute r has no backendRef named z",
		},
		{
			name:      "MCPRoute without backendRefs",
			routes:    []*aigv1b1.MCPRoute{newTestMCPRouteForExtensionPolicy("r")},
			policies:  []*aigv1a1.MCPBackendExtensionPolicy{newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))},
			expType:   aigv1a1.ConditionTypeNotAccepted,
			expStatus: "MCPRoute r has no backendRefs",
		},
		{
			name:   "section policy applied",
			routes: []*aigv1b1.MCPRoute{newTestMCPRouteForExtensionPolicy("r", "a", "b")},
			policies: []*aigv1a1.MCPBackendExtensionPolicy{
				newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r", "a")),
			},
			expType:   aigv1a1.ConditionTypeAccepted,
			expStatus: "MCPRoute r: applied to backends: a",
		},
		{
			name:   "overridden on some backends",
			routes: []*aigv1b1.MCPRoute{newTestMCPRouteForExtensionPolicy("r", "a", "b")},
			policies: []*aigv1a1.MCPBackendExtensionPolicy{
				newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r")),
				newTestMCPBackendExtensionPolicy("p2", mcpRouteTargetRef("r", "a")),
			},
			expType:   aigv1a1.ConditionTypeAccepted,
			expStatus: "MCPRoute r: applied to backends: b; overridden for: a (by p2)",
		},
		{
			name:   "overridden on all backends",
			routes: []*aigv1b1.MCPRoute{newTestMCPRouteForExtensionPolicy("r", "a")},
			policies: []*aigv1a1.MCPBackendExtensionPolicy{
				newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r")),
				newTestMCPBackendExtensionPolicy("p2", mcpRouteTargetRef("r", "a")),
			},
			expType:   aigv1a1.ConditionTypeNotAccepted,
			expStatus: "MCPRoute r: overridden for all backends: a (by p2)",
		},
		{
			name:   "one of two targets missing",
			routes: []*aigv1b1.MCPRoute{newTestMCPRouteForExtensionPolicy("r", "a", "b")},
			policies: []*aigv1a1.MCPBackendExtensionPolicy{
				newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"), mcpRouteTargetRef("missing")),
			},
			expType:   aigv1a1.ConditionTypeNotAccepted,
			expStatus: "MCPRoute r: applied to backends: a, b; MCPRoute missing not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
			c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)
			for _, route := range tt.routes {
				require.NoError(t, fakeClient.Create(t.Context(), route))
			}
			for _, policy := range tt.policies {
				require.NoError(t, fakeClient.Create(t.Context(), policy))
			}

			require.NoError(t, reconcileMCPBackendExtensionPolicy(t, c, "p1"))
			requireMCPBackendExtensionPolicyCondition(t, fakeClient, "p1", tt.expType, tt.expStatus)
		})
	}
}

func Test_effectiveMCPBackendExtensionPolicies(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	t1 := metav1.NewTime(t0.Add(time.Minute))
	newPolicy := func(name string, created metav1.Time, targetRefs ...gwapiv1.LocalPolicyTargetReferenceWithSectionName) aigv1a1.MCPBackendExtensionPolicy {
		policy := newTestMCPBackendExtensionPolicy(name, targetRefs...)
		policy.CreationTimestamp = created
		return *policy
	}
	deleting := newPolicy("p1", t0, mcpRouteTargetRef("r"))
	deleting.DeletionTimestamp = &t1

	for _, tt := range []struct {
		name     string
		policies []aigv1a1.MCPBackendExtensionPolicy
		exp      map[gwapiv1.ObjectName]string
	}{
		{
			name: "no policies",
			exp:  map[gwapiv1.ObjectName]string{},
		},
		{
			name:     "route-wide policy applies to all backends",
			policies: []aigv1a1.MCPBackendExtensionPolicy{newPolicy("p1", t0, mcpRouteTargetRef("r"))},
			exp:      map[gwapiv1.ObjectName]string{"a": "p1", "b": "p1"},
		},
		{
			name: "section policy takes precedence even when newer",
			policies: []aigv1a1.MCPBackendExtensionPolicy{
				newPolicy("p1", t0, mcpRouteTargetRef("r")),
				newPolicy("p2", t1, mcpRouteTargetRef("r", "a")),
			},
			exp: map[gwapiv1.ObjectName]string{"a": "p2", "b": "p1"},
		},
		{
			name: "oldest policy wins",
			policies: []aigv1a1.MCPBackendExtensionPolicy{
				newPolicy("p1", t1, mcpRouteTargetRef("r")),
				newPolicy("p2", t0, mcpRouteTargetRef("r")),
			},
			exp: map[gwapiv1.ObjectName]string{"a": "p2", "b": "p2"},
		},
		{
			name: "same creation time falls back to the name",
			policies: []aigv1a1.MCPBackendExtensionPolicy{
				newPolicy("p2", t0, mcpRouteTargetRef("r")),
				newPolicy("p1", t0, mcpRouteTargetRef("r")),
			},
			exp: map[gwapiv1.ObjectName]string{"a": "p1", "b": "p1"},
		},
		{
			name: "policy being deleted is skipped",
			policies: []aigv1a1.MCPBackendExtensionPolicy{
				deleting,
				newPolicy("p2", t1, mcpRouteTargetRef("r")),
			},
			exp: map[gwapiv1.ObjectName]string{"a": "p2", "b": "p2"},
		},
		{
			name:     "targetRef to another MCPRoute is ignored",
			policies: []aigv1a1.MCPBackendExtensionPolicy{newPolicy("p1", t0, mcpRouteTargetRef("other"))},
			exp:      map[gwapiv1.ObjectName]string{},
		},
		{
			name:     "unknown sectionName assigns nothing",
			policies: []aigv1a1.MCPBackendExtensionPolicy{newPolicy("p1", t0, mcpRouteTargetRef("r", "z"))},
			exp:      map[gwapiv1.ObjectName]string{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			effective := effectiveMCPBackendExtensionPolicies(newTestMCPRouteForExtensionPolicy("r", "a", "b"), tt.policies)
			actual := make(map[gwapiv1.ObjectName]string, len(effective))
			for backend, policy := range effective {
				actual[backend] = policy.Name
			}
			require.Equal(t, tt.exp, actual)
		})
	}
}

func Test_mcpBackendExtensionPolicyTargetRefsIndexFunc(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)

	// A policy targeting two MCPRoutes, one of them twice.
	require.NoError(t, fakeClient.Create(t.Context(),
		newTestMCPBackendExtensionPolicy("indexed", mcpRouteTargetRef("r1"), mcpRouteTargetRef("r1", "a"), mcpRouteTargetRef("r2"))))

	for _, tt := range []struct {
		key string
		exp int
	}{
		{key: "r1.default", exp: 1},
		{key: "r2.default", exp: 1},
		{key: "nonexistent.default", exp: 0},
	} {
		var policies aigv1a1.MCPBackendExtensionPolicyList
		require.NoError(t, fakeClient.List(t.Context(), &policies,
			client.MatchingFields{k8sClientIndexMCPRouteToTargetingMCPBackendExtensionPolicy: tt.key}))
		require.Len(t, policies.Items, tt.exp, tt.key)
	}
}

func TestMCPBackendExtensionPolicyController_MCPRouteToMCPBackendExtensionPolicy(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p2", mcpRouteTargetRef("r", "a"))))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p3", mcpRouteTargetRef("s"))))

	requests := c.mcpRouteToMCPBackendExtensionPolicy(t.Context(), newTestMCPRouteForExtensionPolicy("r"))
	require.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "p1"}},
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "p2"}},
	}, requests)

	require.Empty(t, c.mcpRouteToMCPBackendExtensionPolicy(t.Context(), newTestMCPRouteForExtensionPolicy("unreferenced")))
}

func TestMCPBackendExtensionPolicyController_MCPBackendExtensionPolicyToSiblings(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCPBackendExtensionPolicy(t)
	c := NewMCPBackendExtensionPolicyController(fakeClient, ctrl.Log)
	p1 := newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"), mcpRouteTargetRef("s"))
	require.NoError(t, fakeClient.Create(t.Context(), p1))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p2", mcpRouteTargetRef("r", "a"))))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p3", mcpRouteTargetRef("s"))))
	require.NoError(t, fakeClient.Create(t.Context(), newTestMCPBackendExtensionPolicy("p4", mcpRouteTargetRef("t"))))

	// Every policy on r or s, each once; p1 itself is included and is reconciled anyway.
	requests := c.mcpBackendExtensionPolicyToSiblings(t.Context(), p1)
	require.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "p1"}},
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "p2"}},
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "p3"}},
	}, requests)
}

func Test_mcpBackendExtensionPolicyToMCPRoute(t *testing.T) {
	policy := newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"), mcpRouteTargetRef("r", "a"), mcpRouteTargetRef("s"))
	require.Equal(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "r"}},
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "s"}},
	}, mcpBackendExtensionPolicyToMCPRoute(t.Context(), policy))
}

func Test_mcpBackendExtensionPolicyTargetRefsChanged(t *testing.T) {
	base := newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r"))
	withChange := func(change func(*aigv1a1.MCPBackendExtensionPolicy)) *aigv1a1.MCPBackendExtensionPolicy {
		policy := base.DeepCopy()
		change(policy)
		return policy
	}

	for _, tt := range []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		exp    bool
	}{
		{name: "same targetRefs", oldObj: base, newObj: base.DeepCopy(), exp: false},
		{
			name:   "extensions changed",
			oldObj: base,
			newObj: withChange(func(p *aigv1a1.MCPBackendExtensionPolicy) { p.Spec.DynamicModule[0].FilterName = ptr.To("changed") }),
			exp:    false,
		},
		{
			name:   "targetRef name changed",
			oldObj: base,
			newObj: withChange(func(p *aigv1a1.MCPBackendExtensionPolicy) { p.Spec.TargetRefs[0].Name = "s" }),
			exp:    true,
		},
		{
			name:   "sectionName added",
			oldObj: base,
			newObj: withChange(func(p *aigv1a1.MCPBackendExtensionPolicy) {
				p.Spec.TargetRefs[0].SectionName = ptr.To(gwapiv1.SectionName("a"))
			}),
			exp: true,
		},
		{
			name:   "targetRef added",
			oldObj: base,
			newObj: withChange(func(p *aigv1a1.MCPBackendExtensionPolicy) {
				p.Spec.TargetRefs = append(p.Spec.TargetRefs, mcpRouteTargetRef("s"))
			}),
			exp: true,
		},
		{
			name: "targetRef removed",
			oldObj: withChange(func(p *aigv1a1.MCPBackendExtensionPolicy) {
				p.Spec.TargetRefs = append(p.Spec.TargetRefs, mcpRouteTargetRef("s"))
			}),
			newObj: base,
			exp:    true,
		},
		{name: "not a policy", oldObj: &aigv1b1.MCPRoute{}, newObj: &aigv1b1.MCPRoute{}, exp: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.exp, mcpBackendExtensionPolicyTargetRefsChanged.Update(event.UpdateEvent{ObjectOld: tt.oldObj, ObjectNew: tt.newObj}))
		})
	}

	t.Run("create and delete", func(t *testing.T) {
		require.True(t, mcpBackendExtensionPolicyTargetRefsChanged.Create(event.CreateEvent{Object: base}))
		require.True(t, mcpBackendExtensionPolicyTargetRefsChanged.Delete(event.DeleteEvent{Object: base}))
	})
}

func Test_rejectedByEnvoyGateway(t *testing.T) {
	notAccepted := newTestPolicyAncestorStatus(metav1.ConditionFalse, "Invalid", "bad")
	accepted := newTestPolicyAncestorStatus(metav1.ConditionTrue, "Accepted", "ok")
	otherType := accepted.DeepCopy()
	otherType.Conditions[0].Type = "ResolvedRefs"
	otherType.Conditions[0].Status = metav1.ConditionFalse

	for _, tt := range []struct {
		name      string
		ancestors []gwapiv1.PolicyAncestorStatus
		exp       []string
	}{
		{name: "no ancestors"},
		{name: "accepted", ancestors: []gwapiv1.PolicyAncestorStatus{accepted}},
		{name: "not accepted", ancestors: []gwapiv1.PolicyAncestorStatus{notAccepted}, exp: []string{"Invalid: bad"}},
		{name: "one of two ancestors not accepted", ancestors: []gwapiv1.PolicyAncestorStatus{accepted, notAccepted}, exp: []string{"Invalid: bad"}},
		{name: "other condition type is ignored", ancestors: []gwapiv1.PolicyAncestorStatus{*otherType}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			eep := &egv1a1.EnvoyExtensionPolicy{Status: gwapiv1.PolicyStatus{Ancestors: tt.ancestors}}
			require.Equal(t, tt.exp, rejectedByEnvoyGateway(eep))
		})
	}
}

func Test_newMCPEnvoyExtensionPolicy(t *testing.T) {
	// The policy type isn't registered in an empty scheme, so the owner reference can't be set.
	_, err := newMCPEnvoyExtensionPolicy(newTestMCPBackendExtensionPolicy("p1", mcpRouteTargetRef("r")), runtime.NewScheme())
	require.ErrorContains(t, err, "failed to set controller reference for EnvoyExtensionPolicy")
}

func Test_mcpBackendExtensionPolicyLabelValue(t *testing.T) {
	for _, tt := range []struct {
		name       string
		policyName string
		unchanged  bool
	}{
		{name: "short", policyName: "p", unchanged: true},
		{name: "max label length", policyName: strings.Repeat("a", 63), unchanged: true},
		{name: "one over", policyName: strings.Repeat("a", 64)},
		{name: "max policy name length", policyName: strings.Repeat("a", 239)},
		{name: "dots and dashes at the cut", policyName: strings.Repeat("ab.-", 60)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value := mcpBackendExtensionPolicyLabelValue(tt.policyName)
			require.Empty(t, content.IsLabelValue(value))
			require.Equal(t, value, mcpBackendExtensionPolicyLabelValue(tt.policyName), "must be deterministic")
			if tt.unchanged {
				require.Equal(t, tt.policyName, value)
			} else {
				require.LessOrEqual(t, len(value), 63)
				require.True(t, strings.HasPrefix(value, tt.policyName[:10]), "must keep a readable prefix")
			}
		})
	}

	t.Run("long names with the same prefix differ", func(t *testing.T) {
		prefix := strings.Repeat("a", 60)
		require.NotEqual(t, mcpBackendExtensionPolicyLabelValue(prefix+"-one"), mcpBackendExtensionPolicyLabelValue(prefix+"-two"))
	})
}
