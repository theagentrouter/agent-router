// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fakekube "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/guardrails"
)

func newGuardrailPolicyTestClient(t *testing.T) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1b1.GuardrailPolicy{})
	require.NoError(t, ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	}))
	return builder.Build()
}

func TestGuardrailPolicyControllerReconcileProviderReadiness(t *testing.T) {
	const namespace = "default"
	tests := []struct {
		name          string
		withSecret    bool
		wantCondition string
		wantError     bool
	}{
		{name: "accepted with provider secret", withSecret: true, wantCondition: aigv1b1.ConditionTypeAccepted},
		{name: "not accepted without provider secret", wantCondition: aigv1b1.ConditionTypeNotAccepted, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kube := fakekube.NewClientset()
			if test.withSecret {
				_, err := kube.CoreV1().Secrets(namespace).Create(t.Context(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "azure-key", Namespace: namespace},
					Data:       map[string][]byte{"apiKey": []byte("secret")},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			controllerClient := newGuardrailPolicyTestClient(t)
			require.NoError(t, controllerClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
				ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: namespace},
			}))
			policy := &aigv1b1.GuardrailPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
				Spec: aigv1b1.GuardrailPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{
						Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend", Name: "backend",
					}},
					Rules: []aigv1b1.GuardrailRule{{
						Name: "azure", Phase: aigv1b1.GuardrailPhaseRequest,
						Provider: aigv1b1.GuardrailProvider{
							Type: aigv1b1.GuardrailProviderTypeAzureContentSafety,
							AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{
								Endpoint:        "https://content-safety.example.com",
								APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "azure-key"},
							},
						},
					}},
				},
			}
			require.NoError(t, controllerClient.Create(t.Context(), policy))
			controller := NewGuardrailPolicyController(controllerClient, kube, ctrl.Log, make(chan event.GenericEvent, 1))
			_, err := controller.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: policy.Name}})
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			var updated aigv1b1.GuardrailPolicy
			require.NoError(t, controllerClient.Get(t.Context(), client.ObjectKeyFromObject(policy), &updated))
			require.Len(t, updated.Status.Conditions, 1)
			require.Equal(t, test.wantCondition, updated.Status.Conditions[0].Type)
		})
	}
}

func TestGuardrailPolicyControllerDeletionNotifiesRoutes(t *testing.T) {
	const namespace = "default"
	controllerClient := newGuardrailPolicyTestClient(t)
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}},
		}}},
	}
	require.NoError(t, controllerClient.Create(t.Context(), route))
	require.NoError(t, controllerClient.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: namespace},
	}))
	policy := &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "policy",
			Namespace: namespace,
		},
		Spec: aigv1b1.GuardrailPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend"}},
			Rules: []aigv1b1.GuardrailRule{{
				Name: "deny", Phase: aigv1b1.GuardrailPhaseRequest,
				Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "secret"},
			}},
		},
	}
	require.NoError(t, controllerClient.Create(t.Context(), policy))
	routeEvents := make(chan event.GenericEvent, 1)
	controller := NewGuardrailPolicyController(controllerClient, fakekube.NewClientset(), ctrl.Log, routeEvents)
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(policy)}
	_, err := controller.Reconcile(t.Context(), request)
	require.NoError(t, err)
	<-routeEvents
	require.NoError(t, controllerClient.Delete(t.Context(), policy))

	_, err = controller.Reconcile(t.Context(), request)
	require.NoError(t, err)
	select {
	case got := <-routeEvents:
		require.Equal(t, route.Name, got.Object.GetName())
		require.Equal(t, route.Namespace, got.Object.GetNamespace())
	default:
		t.Fatal("expected route notification when GuardrailPolicy is deleted")
	}

	var updated aigv1b1.GuardrailPolicy
	err = controllerClient.Get(t.Context(), client.ObjectKeyFromObject(policy), &updated)
	if err == nil {
		require.NotContains(t, updated.Finalizers, aiGatewayControllerFinalizer)
	} else {
		require.True(t, apierrors.IsNotFound(err), "unexpected get error: %v", err)
	}
}

func TestGuardrailProviderToFilterAPIResolvesSecret(t *testing.T) {
	const namespace = "default"
	kube := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "presidio-key", Namespace: namespace},
		Data:       map[string][]byte{"apiKey": []byte("secret-value")},
	})
	controller := &GatewayController{kube: kube}
	provider, err := controller.guardrailProviderToFilterAPI(t.Context(), namespace, &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypePresidio,
		Presidio: &aigv1b1.PresidioGuardrailProvider{
			Endpoint:        "https://presidio.example.com",
			APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "presidio-key"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "secret-value", provider.Presidio.APIKey)
}

func TestInjectGuardrailsTargetsRouteBackends(t *testing.T) {
	const namespace = "default"
	controllerClient := newGuardrailPolicyTestClient(t)
	for _, policy := range []*aigv1b1.GuardrailPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "selected", Namespace: namespace},
			Spec: aigv1b1.GuardrailPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend"}},
				Rules: []aigv1b1.GuardrailRule{{
					Name: "deny", Phase: aigv1b1.GuardrailPhaseRequest,
					Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "secret"},
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "ignored", Namespace: namespace},
			Spec: aigv1b1.GuardrailPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "other-backend"}},
				Rules: []aigv1b1.GuardrailRule{{
					Name: "ignored", Phase: aigv1b1.GuardrailPhaseRequest,
					Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "ignored"},
				}},
			},
		},
	} {
		require.NoError(t, controllerClient.Create(t.Context(), policy))
	}

	controller := &GatewayController{client: controllerClient, kube: fakekube.NewClientset(), logger: ctrl.Log}
	config := &filterapi.Config{}
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}},
		}}},
	}
	require.NoError(t, controller.injectGuardrails(t.Context(), route, config, map[string]struct{}{}))
	require.Len(t, config.Guardrails, 1)
	require.Equal(t, "default/selected/deny", config.Guardrails[0].Name)
	require.Equal(t, "secret", config.Guardrails[0].Provider.Pattern)
	require.Equal(t, []string{"default/backend/route/route/rule/0/ref/0"}, config.Guardrails[0].Backends)
}

func TestInjectGuardrailsUsesDeterministicPolicyOrder(t *testing.T) {
	const namespace = "default"
	controllerClient := newGuardrailPolicyTestClient(t)
	for _, name := range []string{"zeta", "alpha"} {
		require.NoError(t, controllerClient.Create(t.Context(), &aigv1b1.GuardrailPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aigv1b1.GuardrailPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend"}},
				Rules: []aigv1b1.GuardrailRule{{
					Name: "rule", Phase: aigv1b1.GuardrailPhaseRequest,
					Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: name},
				}},
			},
		}))
	}
	controller := &GatewayController{client: controllerClient, kube: fakekube.NewClientset(), logger: ctrl.Log}
	config := &filterapi.Config{}
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}},
		}}},
	}
	require.NoError(t, controller.injectGuardrails(t.Context(), route, config, map[string]struct{}{}))
	require.Len(t, config.Guardrails, 2)
	require.Equal(t, "default/alpha/rule", config.Guardrails[0].Name)
	require.Equal(t, "default/zeta/rule", config.Guardrails[1].Name)
	require.Equal(t, defaultGuardrailMaxPayloadBytes, config.Guardrails[0].MaxPayloadBytes)
}

func TestSecretToGuardrailPolicy(t *testing.T) {
	const namespace = "default"
	controllerClient := newGuardrailPolicyTestClient(t)
	policy := &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Spec: aigv1b1.GuardrailPolicySpec{Rules: []aigv1b1.GuardrailRule{{
			Name: "presidio", Phase: aigv1b1.GuardrailPhaseRequest,
			Provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypePresidio,
				Presidio: &aigv1b1.PresidioGuardrailProvider{
					Endpoint:        "https://presidio.example.com",
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "provider-key"},
				},
			},
		}}},
	}
	require.NoError(t, controllerClient.Create(t.Context(), policy))
	controller := NewGuardrailPolicyController(controllerClient, fakekube.NewClientset(), ctrl.Log, make(chan event.GenericEvent, 1))

	requests := controller.SecretToGuardrailPolicy(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-key", Namespace: namespace},
	})
	require.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: namespace, Name: policy.Name,
	}}}, requests)
}

func TestInjectGuardrailsConfigurationFailureModes(t *testing.T) {
	const namespace = "default"
	for _, test := range []struct {
		name          string
		failureMode   aigv1b1.GuardrailFailureMode
		action        aigv1b1.GuardrailAction
		wantGuardrail bool
	}{
		{name: "fail closed publishes blocking fallback", wantGuardrail: true},
		{name: "fail open omits unavailable rule", failureMode: aigv1b1.GuardrailFailureModeFailOpen},
		{name: "monitor omits unavailable rule", action: aigv1b1.GuardrailActionMonitor},
	} {
		t.Run(test.name, func(t *testing.T) {
			controllerClient := newGuardrailPolicyTestClient(t)
			policy := &aigv1b1.GuardrailPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
				Spec: aigv1b1.GuardrailPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend"}},
					Rules: []aigv1b1.GuardrailRule{{
						Name: "azure", Phase: aigv1b1.GuardrailPhaseRequest,
						Provider: aigv1b1.GuardrailProvider{
							Type:        aigv1b1.GuardrailProviderTypeAzureContentSafety,
							Action:      test.action,
							FailureMode: test.failureMode,
							AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{
								Endpoint:        "https://content-safety.example.com",
								APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "missing"},
							},
						},
					}},
				},
			}
			require.NoError(t, controllerClient.Create(t.Context(), policy))
			controller := &GatewayController{client: controllerClient, kube: fakekube.NewClientset(), logger: ctrl.Log}
			config := &filterapi.Config{}
			route := &aigv1b1.AIGatewayRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: namespace},
				Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}},
				}}},
			}
			require.NoError(t, controller.injectGuardrails(t.Context(), route, config, map[string]struct{}{}))
			if !test.wantGuardrail {
				require.Empty(t, config.Guardrails)
				return
			}
			require.Len(t, config.Guardrails, 1)
			require.Equal(t, filterapi.GuardrailProviderTypeRegex, config.Guardrails[0].Provider.Type)
			require.Equal(t, `(?s).*`, config.Guardrails[0].Provider.Pattern)
		})
	}
}

func TestGuardrailPolicyToRuntimeIntegration(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"entity_type":"EMAIL_ADDRESS","score":0.99}]`))
	}))
	t.Cleanup(providerServer.Close)

	kube := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "presidio-key", Namespace: "default"},
		Data:       map[string][]byte{"apiKey": []byte("secret")},
	})
	controller := &GatewayController{kube: kube}
	converted, err := controller.guardrailProviderToFilterAPI(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypePresidio,
		Presidio: &aigv1b1.PresidioGuardrailProvider{
			Endpoint:        providerServer.URL,
			APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "presidio-key"},
		},
	})
	require.NoError(t, err)
	runtimeConfig, err := filterapi.NewRuntimeConfig(t.Context(), &filterapi.Config{
		Guardrails: []filterapi.Guardrail{{Name: "pii", Phase: filterapi.GuardrailPhaseRequest, Provider: converted}},
	}, func(context.Context, *filterapi.BackendAuth) (filterapi.BackendAuthHandler, error) {
		return nil, nil
	}, guardrails.NewEvaluator)
	require.NoError(t, err)
	require.Len(t, runtimeConfig.Guardrails, 1)
	evaluation, err := runtimeConfig.Guardrails[0].Evaluator.Evaluate(t.Context(), []byte("user@example.com"), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
}
