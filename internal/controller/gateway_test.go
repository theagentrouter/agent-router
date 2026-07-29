// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
)

// requireLLMRequestCostsEqual asserts two LLMRequestCost slices are equal, printing a go-cmp diff on failure.
func requireLLMRequestCostsEqual(t *testing.T, want, got []filterapi.LLMRequestCost) {
	t.Helper()
	// Compare as sets (order-agnostic) since map iteration order is non-deterministic.
	less := func(a, b filterapi.LLMRequestCost) bool {
		if a.RouteName != b.RouteName {
			return a.RouteName < b.RouteName
		}
		return a.MetadataKey < b.MetadataKey
	}
	if diff := cmp.Diff(want, got, cmpopts.SortSlices(less)); diff != "" {
		t.Fatalf("LLMRequestCosts not equal (-want +got):\n%s", diff)
	}
}

func requireFilterConfigFromBundle(t *testing.T, kube kubernetes.Interface, namespace, gatewayName, gatewayNamespace string) filterapi.Config {
	t.Helper()

	configName := FilterConfigBundleIndexSecretName(gatewayName, gatewayNamespace)
	secret, err := kube.CoreV1().Secrets(namespace).Get(t.Context(), configName, metav1.GetOptions{})
	require.NoError(t, err)
	indexRaw := ""
	ok := false
	if b, exists := secret.Data[FilterConfigBundleIndexKey]; exists {
		indexRaw = string(b)
		ok = true
	} else if s, exists := secret.StringData[FilterConfigBundleIndexKey]; exists {
		indexRaw = s
		ok = true
	}
	require.True(t, ok)
	index, err := filterapi.UnmarshalConfigBundleIndex([]byte(indexRaw))
	require.NoError(t, err)

	cfg, err := filterapi.ReassembleBundleConfig(index, func(part filterapi.ConfigBundlePart) ([]byte, error) {
		partSecret, getErr := kube.CoreV1().Secrets(namespace).Get(t.Context(), part.Name, metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		if b, exists := partSecret.Data[FilterConfigBundlePartKey]; exists {
			return b, nil
		}
		if b, exists := partSecret.StringData[FilterConfigBundlePartKey]; exists {
			return []byte(b), nil
		}
		return nil, fmt.Errorf("missing key %q in part secret %s", FilterConfigBundlePartKey, part.Name)
	})
	require.NoError(t, err)
	return *cfg
}

func TestGatewayController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	fakeKube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, fakeKube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const namespace = "ns"
	t.Run("not found must be non error", func(t *testing.T) {
		res, err := c.Reconcile(t.Context(), ctrl.Request{})
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, res)
	})
	// Create a Gateway with attached AIGatewayRoutes.
	const okGwName = "ok-gw"
	err := fakeClient.Create(t.Context(), &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: okGwName, Namespace: namespace},
		Spec:       gwapiv1.GatewaySpec{},
	})
	require.NoError(t, err)
	targets := []gwapiv1a2.ParentReference{
		{
			Name:  okGwName,
			Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
			Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
		},
	}
	for _, aigwRoute := range []*aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route1", Namespace: namespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: targets,
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}}},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route2", Namespace: namespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: targets,
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "orange"}}},
				},
			},
		},
	} {
		err = fakeClient.Create(t.Context(), aigwRoute)
		require.NoError(t, err)
	}
	// We also need to create corresponding AIServiceBackends.
	for _, aigwRoute := range []*aigv1b1.AIServiceBackend{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: namespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](namespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: namespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](namespace)},
			},
		},
	} {
		err = fakeClient.Create(t.Context(), aigwRoute)
		require.NoError(t, err)
	}

	// At this point, no Gateway Pods are created, so this should be requeued.
	res, err := c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: okGwName, Namespace: namespace}})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{RequeueAfter: 5 * time.Second}, res)

	// Create a Gateway Pod and deployment.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-pod",
			Namespace: namespace,
			Labels: map[string]string{
				egOwningGatewayNameLabel:      okGwName,
				egOwningGatewayNamespaceLabel: namespace,
			},
		},
		Spec: corev1.PodSpec{},
	}
	_, err = fakeKube.CoreV1().Pods(namespace).Create(t.Context(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-deployment",
			Namespace: namespace,
			Labels: map[string]string{
				egOwningGatewayNameLabel:      okGwName,
				egOwningGatewayNamespaceLabel: namespace,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			UpdatedReplicas:    1,
			ReadyReplicas:      1,
			AvailableReplicas:  1,
		},
	}
	_, err = fakeKube.AppsV1().Deployments(namespace).Create(t.Context(), deployment, metav1.CreateOptions{})
	require.NoError(t, err)

	// Now, the reconcile should succeed and create the filter config secret.
	res, err = c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: okGwName, Namespace: namespace}})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, res)
	secret, err := fakeKube.CoreV1().Secrets(namespace).
		Get(t.Context(), FilterConfigBundleIndexSecretName(okGwName, namespace), metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, secret)
}

func TestGatewayController_reconcileFilterConfigSecret(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route1", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{Name: "apple"},
							{Name: "invalid-bsp-backend"},  // This should be ignored as the BSP is invalid.
							{Name: "non-existent-backend"}, // This should be ignored as the backend does not exist.
						},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{
								Headers: []gwapiv1.HTTPHeaderMatch{
									{
										Name:  internalapi.ModelNameHeaderKeyDefault,
										Value: "mymodel",
									},
								},
							},
						},
					},
				},
				LLMRequestCosts: []aigv1b1.LLMRequestCost{
					{MetadataKey: "foo", Type: aigv1b1.LLMRequestCostTypeInputToken},
					{MetadataKey: "bar", Type: aigv1b1.LLMRequestCostTypeOutputToken},
					{MetadataKey: "baz", Type: aigv1b1.LLMRequestCostTypeTotalToken},
					{MetadataKey: "qux", Type: aigv1b1.LLMRequestCostTypeCachedInputToken},
					{MetadataKey: "zoo", Type: aigv1b1.LLMRequestCostTypeCacheCreationInputToken},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route2", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "orange"}}},
				},
				LLMRequestCosts: []aigv1b1.LLMRequestCost{
					{MetadataKey: "foo", Type: aigv1b1.LLMRequestCostTypeInputToken}, // Same metadataKey as route1; scoped to this route in filter config.
					{MetadataKey: "cat", Type: aigv1b1.LLMRequestCostTypeCEL, CEL: ptr.To(`backend == 'foo.default' ?  input_tokens + output_tokens : total_tokens`)},
				},
			},
		},
	}
	// We also need to create corresponding AIServiceBackends.
	for _, aigwRoute := range []*aigv1b1.AIServiceBackend{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
				HeaderMutation: &aigv1b1.HTTPHeaderMutation{Set: []gwapiv1.HTTPHeader{
					// Header name should be normalized to lowercase in the filter config.
					{Name: "X-Foo", Value: "foo"},
				}, Remove: []string{"x-Bar"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "invalid-bsp-backend", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
	} {
		err := fakeClient.Create(t.Context(), aigwRoute)
		require.NoError(t, err)
	}

	// Create a BackendSecurityPolicy that is invalid (missing secret ref).
	err := fakeClient.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-bsp", Namespace: gwNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{Name: "non-existent-secret"},
			},
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  "invalid-bsp-backend",
				},
			},
		},
	})
	require.NoError(t, err)

	for range 2 { // Reconcile twice to make sure the secret update path is working.
		const someNamespace = "some-namespace"
		configName := FilterConfigBundleIndexSecretName("gw", gwNamespace)
		effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
		require.NoError(t, err)
		require.True(t, effective, "expected filter config to be effective")

		secret, err := kube.CoreV1().Secrets(someNamespace).Get(t.Context(), configName, metav1.GetOptions{})
		require.NoError(t, err)
		indexRaw := ""
		ok := false
		if b, exists := secret.Data[FilterConfigBundleIndexKey]; exists {
			indexRaw = string(b)
			ok = true
		} else if s, exists := secret.StringData[FilterConfigBundleIndexKey]; exists {
			indexRaw = s
			ok = true
		}
		require.True(t, ok)
		index, err := filterapi.UnmarshalConfigBundleIndex([]byte(indexRaw))
		require.NoError(t, err)
		cfg, err := filterapi.ReassembleBundleConfig(index, func(part filterapi.ConfigBundlePart) ([]byte, error) {
			partSecret, getErr := kube.CoreV1().Secrets(someNamespace).Get(t.Context(), part.Name, metav1.GetOptions{})
			if getErr != nil {
				return nil, getErr
			}
			if b, exists := partSecret.Data[FilterConfigBundlePartKey]; exists {
				return b, nil
			}
			if b, exists := partSecret.StringData[FilterConfigBundlePartKey]; exists {
				return []byte(b), nil
			}
			return nil, fmt.Errorf("missing key %q in part secret %s", FilterConfigBundlePartKey, part.Name)
		})
		require.NoError(t, err)
		fc := *cfg
		require.Equal(t, "dev", fc.Version)
		wantLLMRequestCosts := []filterapi.LLMRequestCost{
			{MetadataKey: "foo", RouteName: "ns/route1", Type: filterapi.LLMRequestCostTypeInputToken},
			{MetadataKey: "bar", RouteName: "ns/route1", Type: filterapi.LLMRequestCostTypeOutputToken},
			{MetadataKey: "baz", RouteName: "ns/route1", Type: filterapi.LLMRequestCostTypeTotalToken},
			{MetadataKey: "qux", RouteName: "ns/route1", Type: filterapi.LLMRequestCostTypeCachedInputToken},
			{MetadataKey: "zoo", RouteName: "ns/route1", Type: filterapi.LLMRequestCostTypeCacheCreationInputToken},
			{MetadataKey: "foo", RouteName: "ns/route2", Type: filterapi.LLMRequestCostTypeInputToken},
			{
				MetadataKey: "cat",
				RouteName:   "ns/route2",
				Type:        filterapi.LLMRequestCostTypeCEL,
				CEL:         `backend == 'foo.default' ?  input_tokens + output_tokens : total_tokens`,
			},
		}
		requireLLMRequestCostsEqual(t, wantLLMRequestCosts, fc.LLMRequestCosts)

		catProg, err := llmcostcel.NewProgram(wantLLMRequestCosts[6].CEL)
		require.NoError(t, err)
		catVal, err := llmcostcel.EvaluateProgram(catProg, "model", "foo.default", "ns/route2", 3, 0, 0, 4, 7, 0)
		require.NoError(t, err)
		require.Equal(t, uint64(7), catVal)

		require.Len(t, fc.Models, 1)
		require.Equal(t, "mymodel", fc.Models[0].Name)

		require.Len(t, fc.Backends[0].HeaderMutation.Set, 1)
		require.Len(t, fc.Backends[0].HeaderMutation.Remove, 1)
		require.Equal(t, "x-foo", fc.Backends[0].HeaderMutation.Set[0].Name)
		require.Equal(t, "foo", fc.Backends[0].HeaderMutation.Set[0].Value)
		require.Equal(t, "x-bar", fc.Backends[0].HeaderMutation.Remove[0])
	}
}

// TestGatewayController_reconcileFilterConfigSecret_HostnameScopedModels verifies that mixing routes
// with and without Spec.Hostnames produces a filter config where:
//   - each per-host list contains the host's own models AND every unscoped model (so the unscoped
//     route's models remain visible on host-matched /v1/models requests), and
//   - UnscopedModels is populated separately so unmatched hosts can fall back to it without leaking
//     host-scoped models.
func TestGatewayController_reconcileFilterConfigSecret_HostnameScopedModels(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "scoped-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Hostnames: []gwapiv1.Hostname{"api.example.com"},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "scoped-model"},
							}},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unscoped-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				// No Hostnames -> "unscoped": its models apply to every host.
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "orange"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "unscoped-model"},
							}},
						},
					},
				},
			},
		},
	}
	for _, b := range []*aigv1b1.AIServiceBackend{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
	} {
		require.NoError(t, fakeClient.Create(t.Context(), b))
	}

	const someNamespace = "some-namespace"
	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw-hostname", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective, "expected filter config to be effective")

	fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw-hostname", gwNamespace)

	// Global Models list still contains every model (used as fallback when no ModelsByHost is configured).
	require.ElementsMatch(t,
		[]string{"scoped-model", "unscoped-model"},
		[]string{fc.Models[0].Name, fc.Models[1].Name},
	)

	// UnscopedModels only holds the route-without-hostnames contribution.
	require.Len(t, fc.UnscopedModels, 1)
	require.Equal(t, "unscoped-model", fc.UnscopedModels[0].Name)

	// ModelsByHost["api.example.com"] should have BOTH the scoped model and the merged-in unscoped
	// model — otherwise the unscoped route's models would silently disappear on host-matched requests.
	require.Contains(t, fc.ModelsByHost, "api.example.com")
	gotHostModels := make([]string, 0, len(fc.ModelsByHost["api.example.com"]))
	for _, m := range fc.ModelsByHost["api.example.com"] {
		gotHostModels = append(gotHostModels, m.Name)
	}
	require.ElementsMatch(t, []string{"scoped-model", "unscoped-model"}, gotHostModels)
}

// TestGatewayController_reconcileFilterConfigSecret_AllUnscopedRoutesLeaveUnscopedModelsEmpty
// regression-locks the gate added to avoid duplicating Models into UnscopedModels when no route is
// hostname-scoped. Without the gate, every existing golden YAML that didn't expect an
// `unscopedModels:` field would break (as Test_translate did when this gate was missing).
func TestGatewayController_reconcileFilterConfigSecret_AllUnscopedRoutesLeaveUnscopedModelsEmpty(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route-only-unscoped", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				// No Hostnames — and no other route adds Hostnames either.
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "lone-model"},
							}},
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: gwNamespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
		},
	}))

	const someNamespace = "some-namespace"
	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw-unscoped-only", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective)

	fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw-unscoped-only", gwNamespace)

	require.Len(t, fc.Models, 1)
	require.Equal(t, "lone-model", fc.Models[0].Name)
	// Critical: with no scoped routes, ModelsByHost must be empty AND UnscopedModels must stay nil
	// so it's omitted from the marshalled YAML (omitempty).
	require.Empty(t, fc.ModelsByHost)
	require.Nil(t, fc.UnscopedModels)
}

// TestGatewayController_reconcileFilterConfigSecret_HeaderScopedModels verifies that rules with
// non-model exact-match headers produce separate ModelsByHeaderScope entries and that unscoped
// header models are promoted without leaking scoped models to other scopes.
func TestGatewayController_reconcileFilterConfigSecret_HeaderScopedModels(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "client-a-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: "x-jwt-sub", Value: "client-a"},
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "model-a"},
							}},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "client-b-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "orange"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: "x-jwt-sub", Value: "client-b"},
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "model-b"},
							}},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unscoped-header-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "banana"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "shared-model"},
							}},
						},
					},
				},
			},
		},
	}
	for _, b := range []*aigv1b1.AIServiceBackend{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "banana", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
	} {
		require.NoError(t, fakeClient.Create(t.Context(), b))
	}

	const someNamespace = "some-namespace"
	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw-header-scope", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective)

	fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw-header-scope", gwNamespace)

	require.Contains(t, fc.ModelsByHostAndScope, "")
	hsm := fc.ModelsByHostAndScope[""]
	require.Contains(t, hsm.ModelsByHeaderScope, "x-jwt-sub=client-a")
	require.Contains(t, hsm.ModelsByHeaderScope, "x-jwt-sub=client-b")
	require.Len(t, hsm.UnscopedHeaderModels, 1)
	require.Equal(t, "shared-model", hsm.UnscopedHeaderModels[0].Name)

	clientAModels := modelNames(hsm.ModelsByHeaderScope["x-jwt-sub=client-a"])
	require.ElementsMatch(t, []string{"model-a", "shared-model"}, clientAModels)
	clientBModels := modelNames(hsm.ModelsByHeaderScope["x-jwt-sub=client-b"])
	require.ElementsMatch(t, []string{"model-b", "shared-model"}, clientBModels)
}

// TestGatewayController_reconcileFilterConfigSecret_HostnameAndHeaderScopedModels verifies that
// hostname and header scoping compose: models land in the correct host bucket and scope key.
func TestGatewayController_reconcileFilterConfigSecret_HostnameAndHeaderScopedModels(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "scoped-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Hostnames: []gwapiv1.Hostname{"api.example.com"},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{Headers: []gwapiv1.HTTPHeaderMatch{
								{Name: "x-jwt-sub", Value: "client-a"},
								{Name: internalapi.ModelNameHeaderKeyDefault, Value: "host-scoped-model"},
							}},
						},
					},
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: gwNamespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
		},
	}))

	const someNamespace = "some-namespace"
	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw-host-header", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective)

	fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw-host-header", gwNamespace)
	require.Contains(t, fc.ModelsByHostAndScope, "api.example.com")
	hsm := fc.ModelsByHostAndScope["api.example.com"]
	require.Contains(t, hsm.ModelsByHeaderScope, "x-jwt-sub=client-a")
	require.Equal(t, []string{"host-scoped-model"}, modelNames(hsm.ModelsByHeaderScope["x-jwt-sub=client-a"]))
}

func modelNames(models []filterapi.Model) []string {
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Name)
	}
	return names
}

// TestGatewayController_reconcileFilterConfigSecret_RouteLevelLLMRequestCostAggregation verifies that
// routes sharing the same metadataKey each get their own filter-config row (scoped by routeName).
func TestGatewayController_reconcileFilterConfigSecret_RouteLevelLLMRequestCostAggregation(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	// Create two routes with DIFFERENT CEL expressions for the SAME metadataKey.
	// This is the core scenario of Issue #1688.
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "free-model-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "free-backend"}}},
				},
				LLMRequestCosts: []aigv1b1.LLMRequestCost{
					// Free model: cost is always 0
					{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeCEL, CEL: ptr.To("0")},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "paid-model-route", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "paid-backend"}}},
				},
				LLMRequestCosts: []aigv1b1.LLMRequestCost{
					// Paid model: cost calculation based on tokens
					{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeCEL, CEL: ptr.To("input_tokens + output_tokens")},
				},
			},
		},
	}

	// Create corresponding AIServiceBackends.
	for _, backend := range []*aigv1b1.AIServiceBackend{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "free-backend", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "paid-backend", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
	} {
		err := fakeClient.Create(t.Context(), backend)
		require.NoError(t, err)
	}

	const someNamespace = "some-namespace"

	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective, "expected filter config to be effective")
	fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw", gwNamespace)

	// Verify we have two backends and one filter-config row per route (same metadataKey).
	require.Len(t, fc.Backends, 2, "expected 2 backends")
	wantLLMRequestCosts := []filterapi.LLMRequestCost{
		{
			MetadataKey: "billing_charges",
			RouteName:   "ns/free-model-route",
			Type:        filterapi.LLMRequestCostTypeCEL,
			CEL:         "0",
		},
		{
			MetadataKey: "billing_charges",
			RouteName:   "ns/paid-model-route",
			Type:        filterapi.LLMRequestCostTypeCEL,
			CEL:         "input_tokens + output_tokens",
		},
	}
	requireLLMRequestCostsEqual(t, wantLLMRequestCosts, fc.LLMRequestCosts)

	freeProg, err := llmcostcel.NewProgram(wantLLMRequestCosts[0].CEL)
	require.NoError(t, err)
	val, err := llmcostcel.EvaluateProgram(freeProg, "model", "free-backend", "ns/free-model-route", 10, 0, 0, 5, 15, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(0), val)
	paidProg, err := llmcostcel.NewProgram(wantLLMRequestCosts[1].CEL)
	require.NoError(t, err)
	val, err = llmcostcel.EvaluateProgram(paidProg, "model", "paid-backend", "ns/paid-model-route", 10, 0, 0, 5, 15, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(15), val)
}

// TestGatewayController_reconcileFilterConfigSecret_RouteLevelLLMRequestCostAggregation_DuplicateMetadataKey
// verifies that duplicate metadata keys keep "last definition wins" semantics.
func TestGatewayController_reconcileFilterConfigSecret_RouteLevelLLMRequestCostAggregation_DuplicateMetadataKey(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route-with-duplicate-metadata", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "test-backend"}}},
				},
				LLMRequestCosts: []aigv1b1.LLMRequestCost{
					{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeInputToken},
					{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeOutputToken},
				},
			},
		},
	}

	err := fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: gwNamespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
		},
	})
	require.NoError(t, err)

	const someNamespace = "some-namespace"
	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective, "expected filter config to be effective")

	fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw", gwNamespace)
	// Controller deduplicates same (metadataKey, routeName): last definition wins.
	wantLLMRequestCosts := []filterapi.LLMRequestCost{
		{
			MetadataKey: "billing_charges",
			RouteName:   "ns/route-with-duplicate-metadata",
			Type:        filterapi.LLMRequestCostTypeOutputToken,
		},
	}
	requireLLMRequestCostsEqual(t, wantLLMRequestCosts, fc.LLMRequestCosts)
}

// TestGatewayController_reconcileFilterConfigSecret_InvalidCELExpression tests that invalid CEL
// expressions in LLMRequestCosts cause an error during reconciliation.
func TestGatewayController_reconcileFilterConfigSecret_InvalidCELExpression(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route-with-invalid-cel", Namespace: gwNamespace},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "test-backend"}}},
				},
				LLMRequestCosts: []aigv1b1.LLMRequestCost{
					// Invalid CEL expression - syntax error
					{MetadataKey: "cost", Type: aigv1b1.LLMRequestCostTypeCEL, CEL: ptr.To("invalid syntax (((")},
				},
			},
		},
	}

	// Create the backend
	err := fakeClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: gwNamespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
		},
	})
	require.NoError(t, err)

	const someNamespace = "some-namespace"
	_, err = c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid CEL expression")
}

func TestGatewayController_reconcileFilterConfigSecret_SkipsDeletedRoutes(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	now := metav1.Now()

	// Create routes: one active, one being deleted.
	routes := []aigv1b1.AIGatewayRoute{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "active-route",
				Namespace:         gwNamespace,
				DeletionTimestamp: nil, // Active route.
			},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{Name: "apple"},
						},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{
								Headers: []gwapiv1.HTTPHeaderMatch{
									{
										Name:  internalapi.ModelNameHeaderKeyDefault,
										Value: "mymodel",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "deleting-route",
				Namespace:         gwNamespace,
				DeletionTimestamp: &now, // Route being deleted.
			},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{Name: "orange"},
						},
						Matches: []aigv1b1.AIGatewayRouteRuleMatch{
							{
								Headers: []gwapiv1.HTTPHeaderMatch{
									{
										Name:  internalapi.ModelNameHeaderKeyDefault,
										Value: "deletedmodel",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Create AIServiceBackends for both routes.
	for _, backend := range []*aigv1b1.AIServiceBackend{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "apple", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend1", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "orange", Namespace: gwNamespace},
			Spec: aigv1b1.AIServiceBackendSpec{
				BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend2", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
			},
		},
	} {
		err := fakeClient.Create(t.Context(), backend)
		require.NoError(t, err)
	}

	const someNamespace = "some-namespace"
	configName := FilterConfigBundleIndexSecretName("gw", gwNamespace)

	// Reconcile filter config secret.
	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, routes, nil, "foouuid", nil)
	require.NoError(t, err)
	require.True(t, effective, "expected filter config to be effective")

	// Verify the secret was created and only contains data from the active route.
	secret, err := kube.CoreV1().Secrets(someNamespace).Get(t.Context(), configName, metav1.GetOptions{})
	require.NoError(t, err)
	indexRaw := ""
	ok := false
	if b, exists := secret.Data[FilterConfigBundleIndexKey]; exists {
		indexRaw = string(b)
		ok = true
	} else if s, exists := secret.StringData[FilterConfigBundleIndexKey]; exists {
		indexRaw = s
		ok = true
	}
	require.True(t, ok)
	index, err := filterapi.UnmarshalConfigBundleIndex([]byte(indexRaw))
	require.NoError(t, err)
	cfg, err := filterapi.ReassembleBundleConfig(index, func(part filterapi.ConfigBundlePart) ([]byte, error) {
		partSecret, getErr := kube.CoreV1().Secrets(someNamespace).Get(t.Context(), part.Name, metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		if b, exists := partSecret.Data[FilterConfigBundlePartKey]; exists {
			return b, nil
		}
		if b, exists := partSecret.StringData[FilterConfigBundlePartKey]; exists {
			return []byte(b), nil
		}
		return nil, fmt.Errorf("missing key %q in part secret %s", FilterConfigBundlePartKey, part.Name)
	})
	require.NoError(t, err)
	fc := *cfg

	// Should only have one model (from the active route), not two (deleted route should be skipped).
	require.Len(t, fc.Models, 1)
	require.Equal(t, "mymodel", fc.Models[0].Name)

	// Should only have one backend (from the active route).
	require.Len(t, fc.Backends, 1)
	require.Contains(t, fc.Backends[0].Name, "apple")
}

func TestGatewayController_bspToFilterAPIBackendAuth(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const namespace = "ns"
	for _, bsp := range []*aigv1b1.BackendSecurityPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bsp-apikey", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{Name: "api-key-secret"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "aws-credentials-file", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
				AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
					CredentialsFile: &aigv1b1.AWSCredentialsFile{
						SecretRef: &gwapiv1.SecretObjectReference{Name: "aws-credentials-file-secret"},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "aws-oidc", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
				AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
					OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "aws-default-chain", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
				AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
					Region: "us-west-2",
					// No CredentialsFile or OIDCExchangeToken - uses default credential chain
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "azure-oidc", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type:             aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
				AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp-sa-key-file", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
				GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
					CredentialsFile: &aigv1b1.GCPCredentialsFile{
						SecretRef: &gwapiv1.SecretObjectReference{Name: "gcp-sa-key-file"},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp-wif", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
				GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
					WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
						OIDCExchangeToken: aigv1b1.GCPOIDCExchangeToken{},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp-adc", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
				GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
					ProjectName: "test-project",
					Region:      "us-central1",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bsp-anthropic-apikey", Namespace: namespace},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey,
				AnthropicAPIKey: &aigv1b1.BackendSecurityPolicyAnthropicAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{Name: "api-key-secret"},
				},
			},
		},
	} {
		require.NoError(t, fakeClient.Create(t.Context(), bsp))
	}
	for _, s := range []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: namespace},
			StringData: map[string]string{apiKeyInSecret: "thisisapikey"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "aws-credentials-file-secret", Namespace: namespace},
			StringData: map[string]string{rotators.AwsCredentialsKey: "thisisawscredentials"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: rotators.GetBSPSecretName("aws-oidc"), Namespace: namespace},
			StringData: map[string]string{rotators.AwsCredentialsKey: "thisisawscredentials"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: rotators.GetBSPSecretName("azure-oidc"), Namespace: namespace},
			StringData: map[string]string{rotators.AzureAccessTokenKey: "thisisazurecredentials"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp-sa-key-file", Namespace: namespace},
			StringData: map[string]string{rotators.GCPServiceAccountJSON: "{}"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: rotators.GetBSPSecretName("gcp-wif"), Namespace: namespace},
			StringData: map[string]string{rotators.GCPAccessTokenKey: "thisisgcpcredentials"},
		},
	} {
		_, err := kube.CoreV1().Secrets(namespace).Create(t.Context(), s, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		bspName string
		exp     *filterapi.BackendAuth
	}{
		{
			bspName: "bsp-apikey",
			exp:     &filterapi.BackendAuth{APIKey: &filterapi.APIKeyAuth{Key: "thisisapikey"}},
		},
		{
			bspName: "aws-credentials-file",
			exp: &filterapi.BackendAuth{
				AWSAuth: &filterapi.AWSAuth{
					CredentialFileLiteral: "thisisawscredentials",
				},
			},
		},
		{
			bspName: "aws-oidc",
			exp: &filterapi.BackendAuth{
				AWSAuth: &filterapi.AWSAuth{CredentialFileLiteral: "thisisawscredentials"},
			},
		},
		{
			bspName: "aws-default-chain",
			exp: &filterapi.BackendAuth{
				AWSAuth: &filterapi.AWSAuth{
					Region: "us-west-2",
					// CredentialFileLiteral is empty - uses default credential chain (IRSA/Pod Identity)
				},
			},
		},
		{
			bspName: "azure-oidc",
			exp: &filterapi.BackendAuth{
				AzureAuth: &filterapi.AzureAuth{AccessToken: "thisisazurecredentials"},
			},
		},
		{
			bspName: "gcp-wif",
			exp: &filterapi.BackendAuth{
				GCPAuth: &filterapi.GCPAuth{AccessToken: "thisisgcpcredentials"},
			},
		},
		{
			bspName: "gcp-adc",
			exp: &filterapi.BackendAuth{
				GCPAuth: &filterapi.GCPAuth{
					Region:      "us-central1",
					ProjectName: "test-project",
				},
			},
		},
		{
			bspName: "bsp-anthropic-apikey",
			exp: &filterapi.BackendAuth{
				AnthropicAPIKey: &filterapi.AnthropicAPIKeyAuth{Key: "thisisapikey"},
			},
		},
	} {
		t.Run(tc.bspName, func(t *testing.T) {
			bsp := &aigv1b1.BackendSecurityPolicy{}
			err := fakeClient.Get(t.Context(), client.ObjectKey{
				Name:      tc.bspName,
				Namespace: namespace,
			}, bsp)
			require.NoError(t, err)
			auth, err := c.bspToFilterAPIBackendAuth(t.Context(), bsp)
			require.NoError(t, err)
			require.Equal(t, tc.exp, auth)
		})
	}
}

func TestGatewayController_bspToFilterAPIBackendAuth_ErrorCases(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	c := NewGatewayController(fakeClient, fake2.NewClientset(), ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	ctx := context.Background()
	namespace := "test-namespace"

	tests := []struct {
		name          string
		bspName       string
		bsp           *aigv1b1.BackendSecurityPolicy
		expectedError string
	}{
		{
			name:    "api key type with missing secret",
			bspName: "api-key-bsp",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "api-key-bsp", Namespace: namespace},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
					APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
						SecretRef: &gwapiv1.SecretObjectReference{
							Name: "missing-secret",
						},
					},
				},
			},
			expectedError: "failed to get secret missing-secret",
		},
		{
			name:    "aws credentials with credentials file missing secret",
			bspName: "aws-creds-file-bsp",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "aws-creds-file-bsp", Namespace: namespace},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
					AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
						Region: "us-west-2",
						CredentialsFile: &aigv1b1.AWSCredentialsFile{
							SecretRef: &gwapiv1.SecretObjectReference{
								Name: "missing-aws-secret",
							},
						},
					},
				},
			},
			expectedError: "failed to get secret missing-aws-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := c.bspToFilterAPIBackendAuth(ctx, tt.bsp)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedError)
			require.Nil(t, result)
		})
	}
}

func TestResolveCredentialOverride(t *testing.T) {
	truePtr := ptr.To(true)
	falsePtr := ptr.To(false)

	t.Run("nil override returns nil", func(t *testing.T) {
		result, err := resolveCredentialOverride(aigv1b1.BackendSecurityPolicyTypeAPIKey, nil, true)
		require.NoError(t, err)
		require.Nil(t, result)
	})

	t.Run("fromRequestHeaders default header for APIKey", func(t *testing.T) {
		result, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeAPIKey,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromRequestHeaders: &aigv1b1.CredentialOverrideFromRequestHeaders{},
			},
			true,
		)
		require.NoError(t, err)
		require.Equal(t, "x-aigw-api-key", result.HeaderName)
		require.Equal(t, "x-aigw-api-key", result.InputHeaderToRemove)
		require.True(t, result.FallbackToConfigured)
	})

	t.Run("fromRequestHeaders custom header name", func(t *testing.T) {
		result, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeAPIKey,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromRequestHeaders: &aigv1b1.CredentialOverrideFromRequestHeaders{
					Header:               "X-My-Key",
					FallbackToConfigured: falsePtr,
				},
			},
			true,
		)
		require.NoError(t, err)
		require.Equal(t, "x-my-key", result.HeaderName, "header name should be lowercased")
		require.False(t, result.FallbackToConfigured)
	})

	t.Run("fromRequestHeaders default header for AnthropicAPIKey", func(t *testing.T) {
		result, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromRequestHeaders: &aigv1b1.CredentialOverrideFromRequestHeaders{},
			},
			true,
		)
		require.NoError(t, err)
		require.Equal(t, "x-aigw-anthropic-api-key", result.HeaderName)
	})

	t.Run("fromDynamicMetadata", func(t *testing.T) {
		result, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeAPIKey,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromDynamicMetadata: &aigv1b1.CredentialOverrideFromDynamicMetadata{
					Namespace:            "envoy.filters.http.ext_authz",
					Key:                  "upstream_key",
					FallbackToConfigured: truePtr,
				},
			},
			true,
		)
		require.NoError(t, err)
		require.Equal(t, "envoy.filters.http.ext_authz", result.DynamicMetadataNamespace)
		require.Equal(t, "upstream_key", result.DynamicMetadataKey)
		require.True(t, result.FallbackToConfigured)
		require.Empty(t, result.InputHeaderToRemove, "dynamic metadata source has no strip header")
	})

	t.Run("fromDynamicMetadata default key for GCPCredentials", func(t *testing.T) {
		result, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromDynamicMetadata: &aigv1b1.CredentialOverrideFromDynamicMetadata{
					Namespace: "my.filter",
				},
			},
			true,
		)
		require.NoError(t, err)
		require.Equal(t, "x-aigw-gcp-access-token", result.DynamicMetadataKey)
	})

	t.Run("fallbackToConfigured=true with no static credential returns error", func(t *testing.T) {
		_, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeAPIKey,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromRequestHeaders: &aigv1b1.CredentialOverrideFromRequestHeaders{
					FallbackToConfigured: truePtr,
				},
			},
			false, // no static credential
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "fallbackToConfigured=true requires a static credential")
	})

	t.Run("fallbackToConfigured=false with no static credential is valid", func(t *testing.T) {
		result, err := resolveCredentialOverride(
			aigv1b1.BackendSecurityPolicyTypeAPIKey,
			&aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromRequestHeaders: &aigv1b1.CredentialOverrideFromRequestHeaders{
					FallbackToConfigured: falsePtr,
				},
			},
			false,
		)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, result.FallbackToConfigured)
	})
}

func TestGatewayController_bspToFilterAPIBackendAuth_WithOverride(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const namespace = "ns"

	require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp-with-override", Namespace: namespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{Name: "api-key-secret"},
			},
			CredentialOverride: &aigv1b1.BackendSecurityPolicyCredentialOverride{
				FromRequestHeaders: &aigv1b1.CredentialOverrideFromRequestHeaders{},
			},
		},
	}))
	_, err := kube.CoreV1().Secrets(namespace).Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: namespace},
		StringData: map[string]string{apiKeyInSecret: "thisisapikey"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	bsp := &aigv1b1.BackendSecurityPolicy{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: "bsp-with-override", Namespace: namespace}, bsp))

	auth, err := c.bspToFilterAPIBackendAuth(t.Context(), bsp)
	require.NoError(t, err)
	require.NotNil(t, auth.APIKey)
	require.Equal(t, "thisisapikey", auth.APIKey.Key)
	require.NotNil(t, auth.CredentialOverride)
	require.Equal(t, "x-aigw-api-key", auth.CredentialOverride.HeaderName)
	require.Equal(t, "x-aigw-api-key", auth.CredentialOverride.InputHeaderToRemove)
	require.True(t, auth.CredentialOverride.FallbackToConfigured)
}

func TestGatewayController_GetSecretData_ErrorCases(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	c := NewGatewayController(fakeClient, fake2.NewClientset(), ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	ctx := context.Background()
	namespace := "test-namespace"

	// Test missing secret.
	result, err := c.getSecretData(ctx, namespace, "missing-secret", "test-key")
	require.Error(t, err)
	require.Contains(t, err.Error(), "secrets \"missing-secret\" not found")
	require.Empty(t, result)
}

func TestGatewayController_annotateGatewayPods(t *testing.T) {
	egNamespace := "envoy-gateway-system"
	gwName, gwNamepsace := "gw", "ns"
	labels := map[string]string{
		egOwningGatewayNameLabel:      gwName,
		egOwningGatewayNamespaceLabel: gwNamepsace,
	}

	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	const v2Container = "ai-gateway-extproc:v2"
	const logLevel = "info"
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		v2Container, logLevel, false, nil, true)
	t.Run("pod with extproc", func(t *testing.T) {
		pod, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod1",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: c.extProcImage},
			}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		hasEffectiveRoute := true
		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, nil, nil, "some-uuid", hasEffectiveRoute, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		annotated, err := kube.CoreV1().Pods(egNamespace).Get(t.Context(), "pod1", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", annotated.Annotations[aigatewayUUIDAnnotationKey])

		// We also need to create a parent deployment for the pod.
		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-dep",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// Since it has already a sidecar container, passing the hasEffectiveRoute=false should result in adding an annotation to the deployment.
		hasEffectiveRoute = false
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "another-uuid", hasEffectiveRoute, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "foo-dep", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "another-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("pod without extproc", func(t *testing.T) {
		pod, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod2",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "foo"}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// We also need to create a parent deployment for the pod.
		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment1",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// When there's no effective route, this should not add the annotation to the deployment.
		hasEffectiveRoute := false
		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", hasEffectiveRoute, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment1", metav1.GetOptions{})
		require.NoError(t, err)
		_, exists := deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey]
		require.False(t, exists)

		// When there's an effective route, this should add the annotation to the deployment.
		hasEffectiveRoute = true
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", hasEffectiveRoute, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment1", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("pod with extproc but old version", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod3",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				// The old v1 container image is used here to simulate the pod without extproc.
				{Name: extProcContainerName, Image: "ai-gateway-extproc:v1"},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), pod, metav1.CreateOptions{})
		require.NoError(t, err)

		// We also need to create a parent deployment for the pod.
		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment2",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment2", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])

		// Simulate the pod's container image is updated to the new version.
		pod.Spec.Containers[0].Image = v2Container
		pod, err = kube.CoreV1().Pods(egNamespace).Update(t.Context(), pod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Call annotateGatewayPods again but the deployment's pod template should not be updated again.
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment2", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("pod with extproc but different log level", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod4",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				// The old v1 container image is used here to simulate the pod without extproc.
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-log-level", "debug"}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), pod, metav1.CreateOptions{})
		require.NoError(t, err)

		// We also need to create a parent deployment for the pod.
		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment3",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment3", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])

		// Simulate the pod's container args is updated to the new log level.
		pod.Spec.Containers[0].Args = []string{"-log-level", logLevel}
		pod, err = kube.CoreV1().Pods(egNamespace).Update(t.Context(), pod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Call annotateGatewayPods again but the deployment's pod template should not be updated again.
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment3", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("pod with extproc but missing mcpAddr", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod5",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel, "-adminPort", "1064"}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), pod, metav1.CreateOptions{})
		require.NoError(t, err)

		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment4",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// Call with needMCP=true - should trigger rollout due to missing -mcpAddr
		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "some-uuid", true, true)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation (rollout triggered).
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment4", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])

		// Simulate new pod created after rollout with -mcpAddr present
		pod.Spec.InitContainers[0].Args = []string{"-logLevel", logLevel, "-mcpAddr", ":9856", "-adminPort", "1064"}
		pod, err = kube.CoreV1().Pods(egNamespace).Update(t.Context(), pod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Call annotateGatewayPods again - should NOT trigger another rollout
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, []appsv1.Deployment{*deployment}, nil, "another-uuid", true, true)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Deployment annotation should remain unchanged (no new rollout)
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment4", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("deployment rollout in progress should requeue", func(t *testing.T) {
		// Create pod with sidecar
		podWithSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-sidecar",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		// Create pod without sidecar
		podWithoutSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-without-sidecar",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
		}
		_, err = kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithoutSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment-inconsistent",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		// Simulate rollout in progress.
		deployment.Generation = 2
		deployment.Status.ObservedGeneration = 1

		// Call with rollout in progress - should requeue.
		result, err := c.annotateGatewayPods(t.Context(),
			[]corev1.Pod{*podWithSidecar, *podWithoutSidecar},
			[]appsv1.Deployment{*deployment},
			nil,
			"some-uuid",
			true,
			false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{RequeueAfter: 5 * time.Second}, result)

		// Deployment should NOT be updated during inconsistent state
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment-inconsistent", metav1.GetOptions{})
		require.NoError(t, err)
		_, exists := deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey]
		require.False(t, exists, "deployment should not be updated when pods are inconsistent")
	})

	t.Run("inconsistent pods without rollout should force rollout", func(t *testing.T) {
		podWithSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-sidecar-force",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		podWithoutSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-without-sidecar-force",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
		}
		_, err = kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithoutSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment-force-rollout",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(),
			[]corev1.Pod{*podWithSidecar, *podWithoutSidecar},
			[]appsv1.Deployment{*deployment},
			nil,
			"force-rollout-uuid",
			true,
			false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment-force-rollout", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "force-rollout-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("terminating pods are ignored for consistency and annotation", func(t *testing.T) {
		now := metav1.Now()
		terminatingPodWithSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-terminating-sidecar",
				Namespace:         egNamespace,
				Labels:            labels,
				DeletionTimestamp: &now,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), terminatingPodWithSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		activePodWithoutSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-active-without-sidecar",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
		}
		_, err = kube.CoreV1().Pods(egNamespace).Create(t.Context(), activePodWithoutSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		deployment, err := kube.AppsV1().Deployments(egNamespace).Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment-ignore-terminating",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}},
			},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				UpdatedReplicas:    1,
				ReadyReplicas:      1,
				AvailableReplicas:  1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// Since terminating pod is ignored, active pods are consistent (without sidecar),
		// so no forced rollout should happen when there are no effective routes.
		result, err := c.annotateGatewayPods(t.Context(),
			[]corev1.Pod{*terminatingPodWithSidecar, *activePodWithoutSidecar},
			[]appsv1.Deployment{*deployment},
			nil,
			"ignore-terminating-uuid",
			false,
			false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Terminating pod should not be patched.
		terminatingPodWithSidecar, err = kube.CoreV1().Pods(egNamespace).Get(t.Context(), "pod-terminating-sidecar", metav1.GetOptions{})
		require.NoError(t, err)
		_, exists := terminatingPodWithSidecar.Annotations[aigatewayUUIDAnnotationKey]
		require.False(t, exists)

		// Deployment should not roll out in this case.
		deployment, err = kube.AppsV1().Deployments(egNamespace).Get(t.Context(), "deployment-ignore-terminating", metav1.GetOptions{})
		require.NoError(t, err)
		_, exists = deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey]
		require.False(t, exists)
	})

	t.Run("rollout in progress checks deployment status", func(t *testing.T) {
		tests := []struct {
			name        string
			deployments []appsv1.Deployment
			expected    bool
		}{
			{
				name: "observed generation behind generation requeues",
				deployments: []appsv1.Deployment{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "dep", Generation: 2},
						Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
						Status: appsv1.DeploymentStatus{
							ObservedGeneration: 1,
							UpdatedReplicas:    1,
							ReadyReplicas:      1,
							AvailableReplicas:  1,
						},
					},
				},
				expected: true,
			},
			{
				name: "old-template pods still present requeues",
				deployments: []appsv1.Deployment{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "dep", Generation: 1},
						Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(2))},
						Status: appsv1.DeploymentStatus{
							ObservedGeneration: 1,
							Replicas:           3,
							UpdatedReplicas:    2,
							ReadyReplicas:      3,
							AvailableReplicas:  3,
						},
					},
				},
				expected: true,
			},
			{
				name: "fully ready deployment does not requeue",
				deployments: []appsv1.Deployment{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "dep", Generation: 1},
						Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(2))},
						Status: appsv1.DeploymentStatus{
							ObservedGeneration: 1,
							UpdatedReplicas:    2,
							ReadyReplicas:      2,
							AvailableReplicas:  2,
						},
					},
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := isRolloutInProgress(tt.deployments, nil)
				require.Equal(t, tt.expected, got)
			})
		}
	})
}

func TestGatewayController_annotateDaemonSetGatewayPods(t *testing.T) {
	egNamespace := "envoy-gateway-system"
	gwName, gwNamepsace := "gw", "ns"
	labels := map[string]string{
		egOwningGatewayNameLabel:      gwName,
		egOwningGatewayNamespaceLabel: gwNamepsace,
	}

	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	const v2Container = "ai-gateway-extproc:v2"
	const logLevel = "info"
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		v2Container, logLevel, false, nil, true)

	t.Run("pod without extproc", func(t *testing.T) {
		pod, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod2",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "foo"}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// We also need to create a parent deployment for the pod.
		dss, err := kube.AppsV1().DaemonSets(egNamespace).Create(t.Context(), &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment1",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, nil, []appsv1.DaemonSet{*dss}, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err := kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "deployment1", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("pod with extproc but old version", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod3",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				// The old v1 container image is used here to simulate the pod without extproc.
				{Name: extProcContainerName, Image: "ai-gateway-extproc:v1"},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), pod, metav1.CreateOptions{})
		require.NoError(t, err)

		// We also need to create a parent DaemonSet for the pod.
		dss, err := kube.AppsV1().DaemonSets(egNamespace).Create(t.Context(), &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment2",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, nil, []appsv1.DaemonSet{*dss}, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err := kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "deployment2", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])

		// Simulate the pod's container image is updated to the new version.
		pod.Spec.Containers[0].Image = v2Container
		pod, err = kube.CoreV1().Pods(egNamespace).Update(t.Context(), pod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Call annotateGatewayPods again, but the deployment's pod template should not be updated again.
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, nil, []appsv1.DaemonSet{*dss}, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		deployment, err = kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "deployment2", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("pod with extproc but different log level", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod4",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{
				// The old v1 container image is used here to simulate the pod without extproc.
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-log-level", "debug"}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), pod, metav1.CreateOptions{})
		require.NoError(t, err)

		// We also need to create a parent DaemonSet for the pod.
		dss, err := kube.AppsV1().DaemonSets(egNamespace).Create(t.Context(), &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deployment3",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, nil, []appsv1.DaemonSet{*dss}, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		// Check the deployment's pod template has the annotation.
		deployment, err := kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "deployment3", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])

		// Simulate the pod's container log level is updated to the new version.
		pod.Spec.Containers[0].Args = []string{"-log-level", logLevel}
		pod, err = kube.CoreV1().Pods(egNamespace).Update(t.Context(), pod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Call annotateGatewayPods again, but the deployment's pod template should not be updated again.
		result, err = c.annotateGatewayPods(t.Context(), []corev1.Pod{*pod}, nil, []appsv1.DaemonSet{*dss}, "some-uuid", true, false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		deployment, err = kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "deployment3", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "some-uuid", deployment.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("daemonset rollout in progress should requeue", func(t *testing.T) {
		podWithSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-ds-sidecar-requeue",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		podWithoutSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-ds-no-sidecar-requeue",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
		}
		_, err = kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithoutSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		dss, err := kube.AppsV1().DaemonSets(egNamespace).Create(t.Context(), &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "ds-inconsistent-requeue",
				Namespace:  egNamespace,
				Labels:     labels,
				Generation: 2,
			},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}}},
			Status: appsv1.DaemonSetStatus{
				ObservedGeneration: 1,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(),
			[]corev1.Pod{*podWithSidecar, *podWithoutSidecar},
			nil,
			[]appsv1.DaemonSet{*dss},
			"uuid-requeue",
			true,
			false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{RequeueAfter: 5 * time.Second}, result)
	})

	t.Run("inconsistent pods without rollout should force rollout daemonset", func(t *testing.T) {
		podWithSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-ds-sidecar-force",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		podWithoutSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-ds-no-sidecar-force",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
		}
		_, err = kube.CoreV1().Pods(egNamespace).Create(t.Context(), podWithoutSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		dss, err := kube.AppsV1().DaemonSets(egNamespace).Create(t.Context(), &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ds-force-rollout",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(),
			[]corev1.Pod{*podWithSidecar, *podWithoutSidecar},
			nil,
			[]appsv1.DaemonSet{*dss},
			"uuid-force",
			true,
			false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		dss, err = kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "ds-force-rollout", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "uuid-force", dss.Spec.Template.Annotations[aigatewayUUIDAnnotationKey])
	})

	t.Run("terminating pods are ignored for consistency and annotation daemonset", func(t *testing.T) {
		now := metav1.Now()
		terminatingPodWithSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-ds-terminating-sidecar",
				Namespace:         egNamespace,
				Labels:            labels,
				DeletionTimestamp: &now,
			},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{
				{Name: extProcContainerName, Image: v2Container, Args: []string{"-logLevel", logLevel}},
			}},
		}
		_, err := kube.CoreV1().Pods(egNamespace).Create(t.Context(), terminatingPodWithSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		activePodWithoutSidecar := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-ds-active-no-sidecar",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
		}
		_, err = kube.CoreV1().Pods(egNamespace).Create(t.Context(), activePodWithoutSidecar, metav1.CreateOptions{})
		require.NoError(t, err)

		dss, err := kube.AppsV1().DaemonSets(egNamespace).Create(t.Context(), &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ds-ignore-terminating",
				Namespace: egNamespace,
				Labels:    labels,
			},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{}}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		result, err := c.annotateGatewayPods(t.Context(),
			[]corev1.Pod{*terminatingPodWithSidecar, *activePodWithoutSidecar},
			nil,
			[]appsv1.DaemonSet{*dss},
			"uuid-ignore-terminating",
			false,
			false)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, result)

		terminatingPodWithSidecar, err = kube.CoreV1().Pods(egNamespace).Get(t.Context(), "pod-ds-terminating-sidecar", metav1.GetOptions{})
		require.NoError(t, err)
		_, exists := terminatingPodWithSidecar.Annotations[aigatewayUUIDAnnotationKey]
		require.False(t, exists)

		dss, err = kube.AppsV1().DaemonSets(egNamespace).Get(t.Context(), "ds-ignore-terminating", metav1.GetOptions{})
		require.NoError(t, err)
		_, exists = dss.Spec.Template.Annotations[aigatewayUUIDAnnotationKey]
		require.False(t, exists)
	})

	t.Run("rollout in progress checks daemonset status", func(t *testing.T) {
		tests := []struct {
			name       string
			daemonSets []appsv1.DaemonSet
			expected   bool
		}{
			{
				name: "observed generation zero is ignored",
				daemonSets: []appsv1.DaemonSet{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "ds", Generation: 2},
						Status: appsv1.DaemonSetStatus{
							ObservedGeneration:     0,
							DesiredNumberScheduled: 1,
							UpdatedNumberScheduled: 0,
							NumberReady:            0,
							NumberAvailable:        0,
						},
					},
				},
				expected: false,
			},
			{
				name: "observed generation behind generation requeues",
				daemonSets: []appsv1.DaemonSet{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "ds", Generation: 2},
						Status: appsv1.DaemonSetStatus{
							ObservedGeneration:     1,
							DesiredNumberScheduled: 1,
							UpdatedNumberScheduled: 1,
							NumberReady:            1,
							NumberAvailable:        1,
						},
					},
				},
				expected: true,
			},
			{
				name: "old-template daemonset pods still present requeues",
				daemonSets: []appsv1.DaemonSet{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "ds", Generation: 1},
						Status: appsv1.DaemonSetStatus{
							ObservedGeneration:     1,
							DesiredNumberScheduled: 2,
							CurrentNumberScheduled: 3,
							UpdatedNumberScheduled: 2,
							NumberReady:            3,
							NumberAvailable:        3,
						},
					},
				},
				expected: true,
			},
			{
				name: "fully ready daemonset does not requeue",
				daemonSets: []appsv1.DaemonSet{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "ds", Generation: 1},
						Status: appsv1.DaemonSetStatus{
							ObservedGeneration:     1,
							DesiredNumberScheduled: 2,
							UpdatedNumberScheduled: 2,
							NumberReady:            2,
							NumberAvailable:        2,
						},
					},
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := isRolloutInProgress(nil, tt.daemonSets)
				require.Equal(t, tt.expected, got)
			})
		}
	})
}

func Test_schemaToFilterAPI(t *testing.T) {
	for i, tc := range []struct {
		in       aigv1b1.VersionedAPISchema
		expected filterapi.VersionedAPISchema
	}{
		{
			in:       aigv1b1.VersionedAPISchema{Name: aigv1b1.APISchemaOpenAI},
			expected: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI, Prefix: "v1"},
		},
		{
			in:       aigv1b1.VersionedAPISchema{Name: aigv1b1.APISchemaOpenAI, Prefix: ptr.To("v1/foo")},
			expected: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI, Prefix: "v1/foo"},
		},
		{
			in:       aigv1b1.VersionedAPISchema{Name: aigv1b1.APISchemaAWSBedrock},
			expected: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock},
		},
		{
			in:       aigv1b1.VersionedAPISchema{Name: aigv1b1.APISchemaAnthropic},
			expected: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAnthropic, Prefix: "v1"},
		},
		{
			in:       aigv1b1.VersionedAPISchema{Name: aigv1b1.APISchemaAnthropic, Prefix: ptr.To("gateway/v1")},
			expected: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAnthropic, Prefix: "gateway/v1"},
		},
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			require.Equal(t, tc.expected, schemaToFilterAPI(tc.in))
		})
	}
}

func TestGatewayController_backendWithMaybeBSP(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	const v2Container = "ai-gateway-extproc:v2"
	const logLevel = "info"
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system", v2Container, logLevel, false, nil, true)

	_, _, err := c.backendWithMaybeBSP(t.Context(), "foo", "bar")
	require.ErrorContains(t, err, `aiservicebackends.aigateway.envoyproxy.io "bar" not found`)

	// Create AIServiceBackend without BSP.
	backend := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "bar", Namespace: "foo"},
		Spec:       aigv1b1.AIServiceBackendSpec{},
	}
	require.NoError(t, fakeClient.Create(t.Context(), backend))

	backend, bsp, err := c.backendWithMaybeBSP(t.Context(), backend.Namespace, backend.Name)
	require.NoError(t, err, "should not error when backend exists without BSP")
	require.NotNil(t, backend)
	require.Nil(t, bsp, "should not return BSP when backend exists without BSP")

	// Create a new BSP for the existing backend, referencing the backend by name.
	const bspName = "bsp-bar"
	bspObj := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: backend.Namespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Name: gwapiv1.ObjectName(backend.Name), Kind: aiServiceBackendKind, Group: aiServiceBackendGroup},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), bspObj))
	require.NoError(t, fakeClient.Update(t.Context(), backend))

	// Check that we can retrieve the backend and BSP.
	backend, bsp, err = c.backendWithMaybeBSP(t.Context(), backend.Namespace, backend.Name)
	require.NoError(t, err, "should not error when backend exists with BSP")
	require.NotNil(t, backend, "should return backend when it exists")
	require.NotNil(t, bsp, "should return BSP when backend exists with BSP")
	require.Equal(t, bspName, bsp.Name, "should return the correct BSP name")

	// Create a new BSP that has the same target ref, and one that does not exist.
	bspWithTargetRefs := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp-bar-target-refs", Namespace: backend.Namespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Name: gwapiv1.ObjectName(backend.Name), Kind: aiServiceBackendKind, Group: aiServiceBackendGroup},
				{Name: gwapiv1.ObjectName("non-existent-backend"), Kind: aiServiceBackendKind, Group: aiServiceBackendGroup},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), bspWithTargetRefs))

	// Then it should result in the error due to multiple BSPs found.
	_, _, err = c.backendWithMaybeBSP(t.Context(), backend.Namespace, backend.Name)
	require.ErrorContains(t, err, "multiple BackendSecurityPolicies found for backend bar")
}

// Ensure MCP-only routes produce a correct MCPConfig in the filter Secret.
func TestGatewayController_reconcileFilterMCPConfigSecret(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	const gwNamespace = "ns"
	// Two routes with different CreationTimestamp for deterministic order.
	mcpRoutes := []aigv1b1.MCPRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "mcp-route-old", Namespace: gwNamespace, CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour))},
			Spec: aigv1b1.MCPRouteSpec{
				BackendRefs: []aigv1b1.MCPRouteBackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: gwapiv1.ObjectName("backendA"),
					},
					ToolSelector: &aigv1b1.MCPToolFilter{
						Include: []string{"toolA"},
					},
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "mcp-route-new", Namespace: gwNamespace, CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour))},
			Spec: aigv1b1.MCPRouteSpec{
				BackendRefs: []aigv1b1.MCPRouteBackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: gwapiv1.ObjectName("backendB"),
					},
					ToolSelector: &aigv1b1.MCPToolFilter{
						Include: []string{"toolB"},
					},
				}},
			},
		},
	}

	// Reconcile to produce the Secret with only MCP routes.
	const someNamespace = "some-namespace"
	configName := FilterConfigBundleIndexSecretName("gw", gwNamespace)

	effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, nil, nil, "mcp-uuid", nil)
	require.NoError(t, err)
	require.False(t, effective) // No MCP routes, so not effective.
	effective, err = c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, nil, mcpRoutes, "mcp-uuid", nil)
	require.NoError(t, err)
	require.True(t, effective)

	// Read back and verify MCPConfig fields from the bundle.
	secret, err := kube.CoreV1().Secrets(someNamespace).Get(t.Context(), configName, metav1.GetOptions{})
	require.NoError(t, err)
	indexRaw := ""
	ok := false
	if b, exists := secret.Data[FilterConfigBundleIndexKey]; exists {
		indexRaw = string(b)
		ok = true
	} else if s, exists := secret.StringData[FilterConfigBundleIndexKey]; exists {
		indexRaw = s
		ok = true
	}
	require.True(t, ok)
	index, err := filterapi.UnmarshalConfigBundleIndex([]byte(indexRaw))
	require.NoError(t, err)
	cfg, err := filterapi.ReassembleBundleConfig(index, func(part filterapi.ConfigBundlePart) ([]byte, error) {
		partSecret, getErr := kube.CoreV1().Secrets(someNamespace).Get(t.Context(), part.Name, metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		if b, exists := partSecret.Data[FilterConfigBundlePartKey]; exists {
			return b, nil
		}
		if b, exists := partSecret.StringData[FilterConfigBundlePartKey]; exists {
			return []byte(b), nil
		}
		return nil, fmt.Errorf("missing key %q in part secret %s", FilterConfigBundlePartKey, part.Name)
	})
	require.NoError(t, err)

	require.Equal(t, "mcp-uuid", cfg.UUID)
	require.NotNil(t, cfg.MCPConfig)
	require.Equal(t, "http://127.0.0.1:"+strconv.Itoa(internalapi.MCPBackendListenerPort), cfg.MCPConfig.BackendListenerAddr)
}

func TestGatewayController_writeFilterConfigBundleShards(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	namespace := "ns"
	gatewayName := "cfg-gw"
	gatewayNamespace := "cfg-ns"
	payload := append([]byte(strings.Repeat("x", filterConfigBundlePartSizeBytes*2+10)), []byte("中文字符")...)
	err := c.writeFilterConfigBundle(t.Context(), gatewayName, gatewayNamespace, namespace, payload, "uuid-1")
	require.NoError(t, err)

	indexSecretName := FilterConfigBundleIndexSecretName(gatewayName, gatewayNamespace)
	indexSecret, err := kube.CoreV1().Secrets(namespace).Get(t.Context(), indexSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	indexRaw, ok := indexSecret.StringData[FilterConfigBundleIndexKey]
	if !ok {
		if b, exists := indexSecret.Data[FilterConfigBundleIndexKey]; exists {
			indexRaw = string(b)
			ok = true
		}
	}
	require.True(t, ok)
	index, err := filterapi.UnmarshalConfigBundleIndex([]byte(indexRaw))
	require.NoError(t, err)
	require.Len(t, index.Parts, 3)

	var reassembled bytes.Buffer
	for _, part := range index.Parts {
		s, getErr := kube.CoreV1().Secrets(namespace).Get(t.Context(), part.Name, metav1.GetOptions{})
		require.NoError(t, getErr)
		chunk, partOK := s.Data[FilterConfigBundlePartKey]
		require.True(t, partOK)
		_, stringDataOK := s.StringData[FilterConfigBundlePartKey]
		require.False(t, stringDataOK)
		reassembled.Write(chunk)
	}
	require.Equal(t, payload, reassembled.Bytes())
	_, err = kube.CoreV1().Secrets(namespace).Get(t.Context(),
		filterConfigBundlePartSecretName(gatewayName, gatewayNamespace, maxFilterConfigBundleSlots-1), metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
	_, legacyOK := indexSecret.StringData[FilterConfigKeyInSecret]
	require.False(t, legacyOK)
}

func TestGatewayController_writeFilterConfigBundleShards_Overflow(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	kube := fake2.NewClientset()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	payload := []byte(strings.Repeat("x", filterConfigBundlePartSizeBytes*(maxFilterConfigBundleSlots+1)))
	err := c.writeFilterConfigBundle(t.Context(), "cfg-gw", "cfg-ns", "ns", payload, "uuid-1")
	require.ErrorContains(t, err, "exceeds max supported slots")
}

func Test_mcpConfig_ToolSelectorExclude(t *testing.T) {
	mcpRoutes := []aigv1b1.MCPRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "ns"},
			Spec: aigv1b1.MCPRouteSpec{
				BackendRefs: []aigv1b1.MCPRouteBackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: gwapiv1.ObjectName("backend"),
					},
					ToolSelector: &aigv1b1.MCPToolFilter{
						Include:      []string{"toolA"},
						Exclude:      []string{"toolB"},
						ExcludeRegex: []string{"^secret.*"},
					},
				}},
			},
		},
	}

	mc, effective := mcpConfig(mcpRoutes)
	require.True(t, effective)
	require.NotNil(t, mc)
	require.Len(t, mc.Routes, 1)
	require.Len(t, mc.Routes[0].Backends, 1)
	ts := mc.Routes[0].Backends[0].ToolSelector
	require.NotNil(t, ts)
	require.Equal(t, []string{"toolA"}, ts.Include)
	require.Equal(t, []string{"toolB"}, ts.Exclude)
	require.Equal(t, []string{"^secret.*"}, ts.ExcludeRegex)
}

func Test_mcpConfig_ForwardHeaders(t *testing.T) {
	renamed := "X-Backend-Auth"
	mcpRoutes := []aigv1b1.MCPRoute{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "ns"},
			Spec: aigv1b1.MCPRouteSpec{
				BackendRefs: []aigv1b1.MCPRouteBackendRef{
					{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Name: gwapiv1.ObjectName("backendA"),
						},
						ForwardHeaders: []aigv1b1.MCPHeaderForward{
							{Name: "X-Api-Key"},
							{Name: "Authorization", BackendHeader: &renamed},
						},
					},
					{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Name: gwapiv1.ObjectName("backendB"),
						},
					},
				},
			},
		},
	}

	mc, effective := mcpConfig(mcpRoutes)
	require.True(t, effective)
	require.NotNil(t, mc)
	require.Len(t, mc.Routes, 1)
	require.Len(t, mc.Routes[0].Backends, 2)

	backendA := mc.Routes[0].Backends[0]
	require.Equal(t, "backendA", backendA.Name)
	require.Len(t, backendA.ForwardHeaders, 2)
	require.Equal(t, filterapi.MCPHeaderForward{Name: "X-Api-Key"}, backendA.ForwardHeaders[0])
	require.Equal(t, filterapi.MCPHeaderForward{Name: "Authorization", BackendHeader: "X-Backend-Auth"}, backendA.ForwardHeaders[1])

	backendB := mc.Routes[0].Backends[1]
	require.Equal(t, "backendB", backendB.Name)
	require.Empty(t, backendB.ForwardHeaders)
}

func Test_mergeHeaderMutations(t *testing.T) {
	tests := []struct {
		name         string
		routeLevel   *aigv1b1.HTTPHeaderMutation
		backendLevel *aigv1b1.HTTPHeaderMutation
		expected     *aigv1b1.HTTPHeaderMutation
	}{
		{
			name:         "both nil",
			routeLevel:   nil,
			backendLevel: nil,
			expected:     nil,
		},
		{
			name:       "route nil, backend has values",
			routeLevel: nil,
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{{Name: "Backend-Header", Value: "backend-value"}},
				Remove: []string{"Backend-Remove"},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{{Name: "Backend-Header", Value: "backend-value"}},
				Remove: []string{"Backend-Remove"},
			},
		},
		{
			name: "route has values, backend nil",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{{Name: "Route-Header", Value: "route-value"}},
				Remove: []string{"Route-Remove"},
			},
			backendLevel: nil,
			expected: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{{Name: "Route-Header", Value: "route-value"}},
				Remove: []string{"Route-Remove"},
			},
		},
		{
			name: "no conflicts - different headers",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{{Name: "Route-Header", Value: "route-value"}},
				Remove: []string{"Route-Remove"},
			},
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{{Name: "Backend-Header", Value: "backend-value"}},
				Remove: []string{"Backend-Remove"},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{
					{Name: "Backend-Header", Value: "backend-value"},
					{Name: "Route-Header", Value: "route-value"},
				},
				Remove: []string{"backend-remove", "route-remove"},
			},
		},
		{
			name: "route overrides backend for same header name",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{{Name: "X-Custom", Value: "route-value"}},
			},
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{{Name: "X-Custom", Value: "backend-value"}},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{{Name: "X-Custom", Value: "route-value"}},
			},
		},
		{
			name: "case insensitive header name conflicts",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{{Name: "x-custom", Value: "route-value"}},
			},
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{{Name: "X-CUSTOM", Value: "backend-value"}},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{{Name: "x-custom", Value: "route-value"}},
			},
		},
		{
			name: "remove operations are combined and deduplicated",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Remove: []string{"X-Remove", "x-shared"},
			},
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Remove: []string{"X-Backend-Remove", "X-SHARED"},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Remove: []string{"x-backend-remove", "x-remove", "x-shared"},
			},
		},
		{
			name: "complex merge scenario",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{
					{Name: "X-Route-Only", Value: "route-only"},
					{Name: "X-Override", Value: "route-wins"},
				},
				Remove: []string{"X-Route-Remove", "x-shared-remove"},
			},
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{
					{Name: "X-Backend-Only", Value: "backend-only"},
					{Name: "x-override", Value: "backend-loses"},
				},
				Remove: []string{"X-Backend-Remove", "X-SHARED-REMOVE"},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Set: []gwapiv1.HTTPHeader{
					{Name: "X-Backend-Only", Value: "backend-only"},
					{Name: "X-Override", Value: "route-wins"},
					{Name: "X-Route-Only", Value: "route-only"},
				},
				Remove: []string{"x-backend-remove", "x-route-remove", "x-shared-remove"},
			},
		},
		{
			name: "empty mutations",
			routeLevel: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{},
				Remove: []string{},
			},
			backendLevel: &aigv1b1.HTTPHeaderMutation{
				Set:    []gwapiv1.HTTPHeader{},
				Remove: []string{},
			},
			expected: &aigv1b1.HTTPHeaderMutation{
				Set:    nil,
				Remove: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeHeaderMutations(tt.routeLevel, tt.backendLevel)

			if tt.expected == nil {
				require.Nil(t, result)
				return
			}

			require.NotNil(t, result)

			if d := cmp.Diff(tt.expected, result, cmpopts.SortSlices(func(a, b gwapiv1.HTTPHeader) bool {
				return a.Name < b.Name
			}), cmpopts.SortSlices(func(a, b string) bool {
				return a < b
			})); d != "" {
				t.Errorf("mergeHeaderMutations() mismatch (-expected +got):\n%s", d)
			}
		})
	}
}

func Test_bodyMutationToFilterAPI(t *testing.T) {
	tests := []struct {
		name     string
		input    *aigv1b1.HTTPBodyMutation
		expected *filterapi.HTTPBodyMutation
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name: "empty mutation",
			input: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{},
				Remove: []string{},
			},
			expected: &filterapi.HTTPBodyMutation{
				Set:    nil,
				Remove: []string{},
			},
		},
		{
			name: "only set operations",
			input: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "model", Value: "\"gpt-4\""},
					{Path: "temperature", Value: "0.7"},
					{Path: "max_tokens", Value: "100"},
				},
			},
			expected: &filterapi.HTTPBodyMutation{
				Set: []filterapi.HTTPBodyField{
					{Path: "model", Value: "\"gpt-4\""},
					{Path: "temperature", Value: "0.7"},
					{Path: "max_tokens", Value: "100"},
				},
				Remove: []string{},
			},
		},
		{
			name: "only remove operations",
			input: &aigv1b1.HTTPBodyMutation{
				Remove: []string{"internal_flag", "debug_mode", "temp_field"},
			},
			expected: &filterapi.HTTPBodyMutation{
				Set:    nil,
				Remove: []string{"internal_flag", "debug_mode", "temp_field"},
			},
		},
		{
			name: "both set and remove operations",
			input: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "service_tier", Value: "\"scale\""},
					{Path: "stream", Value: "true"},
					{Path: "metadata", Value: "{\"key\": \"value\"}"},
				},
				Remove: []string{"internal_flag", "debug"},
			},
			expected: &filterapi.HTTPBodyMutation{
				Set: []filterapi.HTTPBodyField{
					{Path: "service_tier", Value: "\"scale\""},
					{Path: "stream", Value: "true"},
					{Path: "metadata", Value: "{\"key\": \"value\"}"},
				},
				Remove: []string{"internal_flag", "debug"},
			},
		},
		{
			name: "complex json values",
			input: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "array_field", Value: "[1, 2, 3]"},
					{Path: "null_field", Value: "null"},
					{Path: "bool_field", Value: "false"},
					{Path: "nested_object", Value: "{\"nested\": {\"key\": \"value\"}}"},
				},
			},
			expected: &filterapi.HTTPBodyMutation{
				Set: []filterapi.HTTPBodyField{
					{Path: "array_field", Value: "[1, 2, 3]"},
					{Path: "null_field", Value: "null"},
					{Path: "bool_field", Value: "false"},
					{Path: "nested_object", Value: "{\"nested\": {\"key\": \"value\"}}"},
				},
				Remove: []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bodyMutationToFilterAPI(tt.input)
			if tt.expected == nil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			if d := cmp.Diff(tt.expected, result); d != "" {
				t.Errorf("bodyMutationToFilterAPI() mismatch (-expected +got):\n%s", d)
			}
		})
	}
}

// TestGatewayController_reconcileFilterConfigSecret_GlobalDefaults tests that
// global LLM request costs from GatewayConfig are properly included in the filter config
// when no routes override them.
func TestGatewayController_reconcileFilterConfigSecret_GlobalDefaults(t *testing.T) {
	tests := []struct {
		name                     string
		globalCosts              []aigv1b1.LLMRequestCost
		routes                   []aigv1b1.AIGatewayRoute
		expectedGlobalCosts      []filterapi.GlobalLLMRequestCost
		expectedRouteScopedCosts []filterapi.LLMRequestCost
	}{
		{
			name: "global defaults only, no routes",
			globalCosts: []aigv1b1.LLMRequestCost{
				{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeInputToken},
			},
			routes: []aigv1b1.AIGatewayRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "route1", Namespace: "ns"},
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend1"}}},
						},
					},
				},
			},
			expectedGlobalCosts: []filterapi.GlobalLLMRequestCost{
				{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken},
			},
			expectedRouteScopedCosts: nil, // No route-scoped costs
		},
		{
			name: "global defaults with route override",
			globalCosts: []aigv1b1.LLMRequestCost{
				{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeInputToken},
				{MetadataKey: "total_tokens", Type: aigv1b1.LLMRequestCostTypeTotalToken},
			},
			routes: []aigv1b1.AIGatewayRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "premium-route", Namespace: "ns"},
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend1"}}},
						},
						LLMRequestCosts: []aigv1b1.LLMRequestCost{
							{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeOutputToken}, // Override global
						},
					},
				},
			},
			expectedGlobalCosts: []filterapi.GlobalLLMRequestCost{
				{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken},
				{MetadataKey: "total_tokens", Type: filterapi.LLMRequestCostTypeTotalToken},
			},
			expectedRouteScopedCosts: []filterapi.LLMRequestCost{
				{MetadataKey: "billing_charges", RouteName: "ns/premium-route", Type: filterapi.LLMRequestCostTypeOutputToken},
			},
		},
		{
			name: "multiple routes with different overrides",
			globalCosts: []aigv1b1.LLMRequestCost{
				{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeInputToken},
			},
			routes: []aigv1b1.AIGatewayRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "free-route", Namespace: "ns"},
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend1"}}},
						},
						LLMRequestCosts: []aigv1b1.LLMRequestCost{
							{MetadataKey: "billing_charges", Type: aigv1b1.LLMRequestCostTypeCEL, CEL: ptr.To("0")}, // Free
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "standard-route", Namespace: "ns"},
					Spec: aigv1b1.AIGatewayRouteSpec{
						Rules: []aigv1b1.AIGatewayRouteRule{
							{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend1"}}},
						},
						// No override - will use global default
					},
				},
			},
			expectedGlobalCosts: []filterapi.GlobalLLMRequestCost{
				{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken},
			},
			expectedRouteScopedCosts: []filterapi.LLMRequestCost{
				{MetadataKey: "billing_charges", RouteName: "ns/free-route", Type: filterapi.LLMRequestCostTypeCEL, CEL: "0"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexes(t)
			kube := fake2.NewClientset()
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
			c := NewGatewayController(fakeClient, kube, ctrl.Log, "envoy-gateway-system",
				"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

			const gwNamespace = "ns"

			// Create AIServiceBackend
			backend := &aigv1b1.AIServiceBackend{
				ObjectMeta: metav1.ObjectMeta{Name: "backend1", Namespace: gwNamespace},
				Spec: aigv1b1.AIServiceBackendSpec{
					BackendRef: gwapiv1.BackendObjectReference{Name: "some-backend", Namespace: ptr.To[gwapiv1.Namespace](gwNamespace)},
				},
			}
			err := fakeClient.Create(t.Context(), backend)
			require.NoError(t, err)

			const someNamespace = "some-namespace"
			effective, err := c.reconcileFilterConfigSecret(t.Context(), "gw", gwNamespace, someNamespace, tt.routes, nil, "test-uuid", tt.globalCosts)
			require.NoError(t, err)
			require.True(t, effective)

			fc := requireFilterConfigFromBundle(t, kube, someNamespace, "gw", gwNamespace)

			// Compare global costs (order-agnostic)
			if diff := cmp.Diff(tt.expectedGlobalCosts, fc.GlobalLLMRequestCosts,
				cmpopts.SortSlices(func(a, b filterapi.GlobalLLMRequestCost) bool {
					return a.MetadataKey < b.MetadataKey
				})); diff != "" {
				t.Errorf("GlobalLLMRequestCosts mismatch (-want +got):\n%s", diff)
			}

			// Compare route-scoped costs (order-agnostic)
			requireLLMRequestCostsEqual(t, tt.expectedRouteScopedCosts, fc.LLMRequestCosts)
		})
	}
}

func Test_mergeBodyMutations(t *testing.T) {
	tests := []struct {
		name         string
		routeLevel   *aigv1b1.HTTPBodyMutation
		backendLevel *aigv1b1.HTTPBodyMutation
		expected     *aigv1b1.HTTPBodyMutation
	}{
		{
			name:         "both nil",
			routeLevel:   nil,
			backendLevel: nil,
			expected:     nil,
		},
		{
			name:       "route nil, backend has values",
			routeLevel: nil,
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{{Path: "backend_field", Value: "\"backend-value\""}},
				Remove: []string{"backend_remove"},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{{Path: "backend_field", Value: "\"backend-value\""}},
				Remove: []string{"backend_remove"},
			},
		},
		{
			name: "route has values, backend nil",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{{Path: "route_field", Value: "\"route-value\""}},
				Remove: []string{"route_remove"},
			},
			backendLevel: nil,
			expected: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{{Path: "route_field", Value: "\"route-value\""}},
				Remove: []string{"route_remove"},
			},
		},
		{
			name: "no conflicts - different fields",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{{Path: "route_field", Value: "\"route-value\""}},
				Remove: []string{"route_remove"},
			},
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{{Path: "backend_field", Value: "\"backend-value\""}},
				Remove: []string{"backend_remove"},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "backend_field", Value: "\"backend-value\""},
					{Path: "route_field", Value: "\"route-value\""},
				},
				Remove: []string{"backend_remove", "route_remove"},
			},
		},
		{
			name: "route overrides backend for same field path",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{{Path: "service_tier", Value: "\"route-value\""}},
			},
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{{Path: "service_tier", Value: "\"backend-value\""}},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{{Path: "service_tier", Value: "\"route-value\""}},
			},
		},
		{
			name: "remove operations are combined and deduplicated",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Remove: []string{"field1", "shared_field"},
			},
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Remove: []string{"field2", "shared_field"},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Remove: []string{"field1", "field2", "shared_field"},
			},
		},
		{
			name: "complex merge scenario",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "route_only", Value: "\"route-only\""},
					{Path: "override_field", Value: "\"route-wins\""},
					{Path: "temperature", Value: "0.8"},
				},
				Remove: []string{"route_remove", "shared_remove"},
			},
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "backend_only", Value: "\"backend-only\""},
					{Path: "override_field", Value: "\"backend-loses\""},
					{Path: "max_tokens", Value: "100"},
				},
				Remove: []string{"backend_remove", "shared_remove"},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "backend_only", Value: "\"backend-only\""},
					{Path: "max_tokens", Value: "100"},
					{Path: "override_field", Value: "\"route-wins\""},
					{Path: "route_only", Value: "\"route-only\""},
					{Path: "temperature", Value: "0.8"},
				},
				Remove: []string{"backend_remove", "route_remove", "shared_remove"},
			},
		},
		{
			name: "empty mutations",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{},
				Remove: []string{},
			},
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Set:    []aigv1b1.HTTPBodyField{},
				Remove: []string{},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Set:    nil,
				Remove: nil,
			},
		},
		{
			name: "different json value types",
			routeLevel: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "string_field", Value: "\"string-value\""},
					{Path: "number_field", Value: "42"},
				},
			},
			backendLevel: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "bool_field", Value: "true"},
					{Path: "object_field", Value: "{\"key\": \"value\"}"},
					{Path: "array_field", Value: "[1, 2, 3]"},
					{Path: "null_field", Value: "null"},
				},
			},
			expected: &aigv1b1.HTTPBodyMutation{
				Set: []aigv1b1.HTTPBodyField{
					{Path: "array_field", Value: "[1, 2, 3]"},
					{Path: "bool_field", Value: "true"},
					{Path: "null_field", Value: "null"},
					{Path: "number_field", Value: "42"},
					{Path: "object_field", Value: "{\"key\": \"value\"}"},
					{Path: "string_field", Value: "\"string-value\""},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeBodyMutations(tt.routeLevel, tt.backendLevel)
			if tt.expected == nil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			if d := cmp.Diff(tt.expected, result, cmpopts.SortSlices(func(a, b aigv1b1.HTTPBodyField) bool {
				return a.Path < b.Path
			}), cmpopts.SortSlices(func(a, b string) bool {
				return a < b
			})); d != "" {
				t.Errorf("mergeBodyMutations() mismatch (-expected +got):\n%s", d)
			}
		})
	}
}

func TestGatewayController_getObjectsForGatewayNamespaceInconsistency(t *testing.T) {
	const gwName, gwNamespace, egNamespace = "gw", "ns", "envoy-gateway-system"
	labels := map[string]string{
		egOwningGatewayNameLabel:      gwName,
		egOwningGatewayNamespaceLabel: gwNamespace,
	}
	gw := &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: gwName, Namespace: gwNamespace}}

	kube := fake2.NewClientset()
	c := NewGatewayController(requireNewFakeClientWithIndexes(t), kube, ctrl.Log, egNamespace,
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	// Place a pod in the gateway namespace and a pod in the envoy-gateway namespace so that
	// objects are found in two distinct namespaces, which should trigger the error.
	for _, ns := range []string{gwNamespace, egNamespace} {
		_, err := kube.CoreV1().Pods(ns).Create(t.Context(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-" + ns, Namespace: ns, Labels: labels},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	_, _, _, _, err := c.getObjectsForGateway(t.Context(), gw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "found gateway-labeled objects in multiple namespaces")
}

func TestGatewayController_getObjectsForGatewaySameNamespace(t *testing.T) {
	const gwName, ns = "gw", "shared"
	labels := map[string]string{
		egOwningGatewayNameLabel:      gwName,
		egOwningGatewayNamespaceLabel: ns,
	}
	gw := &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: gwName, Namespace: ns}}

	kube := fake2.NewClientset()
	c := NewGatewayController(requireNewFakeClientWithIndexes(t), kube, ctrl.Log, ns,
		"docker.io/envoyproxy/ai-gateway-extproc:latest", "info", false, nil, true)

	_, err := kube.CoreV1().Pods(ns).Create(t.Context(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: ns, Labels: labels},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kube.AppsV1().Deployments(ns).Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dep-1", Namespace: ns, Labels: labels},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	namespace, pods, deployments, _, err := c.getObjectsForGateway(t.Context(), gw)
	require.NoError(t, err)
	require.Equal(t, ns, namespace)
	require.Len(t, pods, 1)
	require.Len(t, deployments, 1)
}
