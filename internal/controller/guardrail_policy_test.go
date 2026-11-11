// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func TestGuardrailHTTPProviderToRuntimeEvaluator(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/v1/check", req.URL.Path)
		require.Equal(t, "Bearer custom-secret", req.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"action":"block","findings":[{"type":"PII","start":0,"end":16,"score":0.92}]}`))
	}))
	t.Cleanup(providerServer.Close)

	kube := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-guardrail-key", Namespace: "default"},
		Data:       map[string][]byte{"apiKey": []byte("custom-secret")},
	})
	controller := &GatewayController{kube: kube}
	converted, err := controller.guardrailProviderToFilterAPI(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeHTTP,
		HTTP: &aigv1b1.HTTPGuardrailProvider{
			Endpoint:        providerServer.URL,
			Path:            "/v1/check",
			APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "custom-guardrail-key"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "custom-secret", converted.HTTP.APIKey)
	runtimeConfig, err := filterapi.NewRuntimeConfig(t.Context(), &filterapi.Config{
		Guardrails: []filterapi.Guardrail{{Name: "custom", Phase: filterapi.GuardrailPhaseRequest, Provider: converted}},
	}, func(context.Context, *filterapi.BackendAuth) (filterapi.BackendAuthHandler, error) {
		return nil, nil
	}, guardrails.NewEvaluator)
	require.NoError(t, err)
	require.Len(t, runtimeConfig.Guardrails, 1)
	evaluation, err := runtimeConfig.Guardrails[0].Evaluator.Evaluate(t.Context(), []byte("user@example.com"), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
	require.Equal(t, "[REDACTED]", string(evaluation.Replacement))
}

func TestGuardrailHTTPProviderValidation(t *testing.T) {
	kube := fakekube.NewClientset()
	controller := &GuardrailPolicyController{kube: kube}
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeHTTP,
	}), "http guardrail configuration is required")
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeHTTP,
		HTTP: &aigv1b1.HTTPGuardrailProvider{Endpoint: "not-a-url"},
	}), "valid provider endpoint is required")
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeHTTP,
		HTTP: &aigv1b1.HTTPGuardrailProvider{Endpoint: "https://guardrail.example.com", Path: "analyze"},
	}), "path must start with /")
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeHTTP,
		HTTP: &aigv1b1.HTTPGuardrailProvider{
			Endpoint:        "https://guardrail.example.com",
			APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "missing"},
		},
	}), "failed to get secret missing")
	require.NoError(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type:   aigv1b1.GuardrailProviderTypeHTTP,
		Action: aigv1b1.GuardrailActionMask,
		HTTP:   &aigv1b1.HTTPGuardrailProvider{Endpoint: "https://guardrail.example.com"},
	}))
}

func TestGuardrailPolicySecretRefsIndexIncludesHTTPProvider(t *testing.T) {
	policy := &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "custom", Namespace: "default"},
		Spec: aigv1b1.GuardrailPolicySpec{Rules: []aigv1b1.GuardrailRule{{
			Name: "custom", Phase: aigv1b1.GuardrailPhaseRequest,
			Provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeHTTP,
				HTTP: &aigv1b1.HTTPGuardrailProvider{
					Endpoint:        "https://guardrail.example.com",
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "custom-guardrail-key"},
				},
			},
		}}},
	}
	require.Equal(t, []string{"custom-guardrail-key.default"}, guardrailPolicySecretRefsIndexFunc(policy))
}

func TestGuardrailModelArmorProviderToFilterAPI(t *testing.T) {
	kube := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "model-armor-sa", Namespace: "default"},
		Data:       map[string][]byte{"credentials": []byte(`{"type":"service_account"}`)},
	})
	controller := &GatewayController{kube: kube}
	converted, err := controller.guardrailProviderToFilterAPI(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type:   aigv1b1.GuardrailProviderTypeModelArmor,
		Action: aigv1b1.GuardrailActionMask,
		ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{
			Project:              "project-id",
			Location:             "us-central1",
			Template:             "template-id",
			Endpoint:             "https://modelarmor.example.com",
			CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "model-armor-sa"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, filterapi.GuardrailActionMask, converted.Action)
	require.Equal(t, &filterapi.ModelArmorGuardrailProvider{
		Endpoint:        "https://modelarmor.example.com",
		Project:         "project-id",
		Location:        "us-central1",
		Template:        "template-id",
		CredentialsJSON: `{"type":"service_account"}`,
	}, converted.ModelArmor)

	_, err = controller.guardrailProviderToFilterAPI(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeModelArmor,
	})
	require.ErrorContains(t, err, "model Armor configuration is required")
}

func TestGuardrailModelArmorProviderValidation(t *testing.T) {
	kube := fakekube.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "model-armor-sa", Namespace: "default"},
		Data:       map[string][]byte{"credentials": []byte(`{"type":"service_account"}`)},
	})
	controller := &GuardrailPolicyController{kube: kube}
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeModelArmor,
	}), "model Armor project, location, and template are required")
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type:       aigv1b1.GuardrailProviderTypeModelArmor,
		ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{Project: "project-id", Location: "us-central1"},
	}), "model Armor project, location, and template are required")
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeModelArmor,
		ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{
			Project: "project-id", Location: "us-central1", Template: "template-id", Endpoint: "not-a-url",
		},
	}), "valid provider endpoint is required")
	require.ErrorContains(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type: aigv1b1.GuardrailProviderTypeModelArmor,
		ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{
			Project: "project-id", Location: "us-central1", Template: "template-id",
			CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "missing"},
		},
	}), "failed to get secret missing")
	require.NoError(t, controller.validateGuardrailProvider(t.Context(), "default", &aigv1b1.GuardrailProvider{
		Type:   aigv1b1.GuardrailProviderTypeModelArmor,
		Action: aigv1b1.GuardrailActionMask,
		ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{
			Project: "project-id", Location: "us-central1", Template: "template-id",
			CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "model-armor-sa"},
		},
	}))
}

func TestGuardrailPolicySecretRefsIndexIncludesModelArmorProvider(t *testing.T) {
	policy := &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "model-armor", Namespace: "default"},
		Spec: aigv1b1.GuardrailPolicySpec{Rules: []aigv1b1.GuardrailRule{{
			Name: "model-armor", Phase: aigv1b1.GuardrailPhaseRequest,
			Provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeModelArmor,
				ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{
					Project: "project-id", Location: "us-central1", Template: "template-id",
					CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "model-armor-sa"},
				},
			},
		}}},
	}
	require.Equal(t, []string{"model-armor-sa.default"}, guardrailPolicySecretRefsIndexFunc(policy))
}

func TestGuardrailProviderValidation(t *testing.T) {
	const namespace = "default"
	otherNamespace := gwapiv1.Namespace("other")
	kube := fakekube.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "api-key", Namespace: namespace},
			Data:       map[string][]byte{"apiKey": []byte("secret")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "string-data", Namespace: namespace},
			StringData: map[string]string{"credentials": "[default]"},
		},
	)
	controller := &GuardrailPolicyController{kube: kube}
	tests := []struct {
		name     string
		provider aigv1b1.GuardrailProvider
		wantErr  string
	}{
		{
			name:     "unsupported action",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "x", Action: "Drop"},
			wantErr:  `unsupported action "Drop"`,
		},
		{
			name: "azure mask",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeAzureContentSafety, Action: aigv1b1.GuardrailActionMask,
			},
			wantErr: "azure Content Safety does not support mask",
		},
		{
			name:     "unsupported failure mode",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "x", FailureMode: "Retry"},
			wantErr:  `unsupported failureMode "Retry"`,
		},
		{
			name:     "regex",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "secret", Action: aigv1b1.GuardrailActionMask},
		},
		{
			name:     "regex missing pattern",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex},
			wantErr:  "regex pattern is required",
		},
		{
			name:     "regex invalid pattern",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "("},
			wantErr:  "invalid regex pattern",
		},
		{
			name: "presidio",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypePresidio,
				Presidio: &aigv1b1.PresidioGuardrailProvider{
					Endpoint:        "https://presidio.example.com",
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "api-key"},
				},
			},
		},
		{
			name:     "presidio missing configuration",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypePresidio},
			wantErr:  "presidio configuration is required",
		},
		{
			name: "presidio invalid endpoint",
			provider: aigv1b1.GuardrailProvider{
				Type:     aigv1b1.GuardrailProviderTypePresidio,
				Presidio: &aigv1b1.PresidioGuardrailProvider{Endpoint: "presidio"},
			},
			wantErr: "valid provider endpoint is required",
		},
		{
			name: "bedrock with secret in string data",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &aigv1b1.BedrockGuardrailProvider{
					Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
					Endpoint:             "https://bedrock.example.com",
					CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "string-data"},
				},
			},
		},
		{
			name: "bedrock missing identifier",
			provider: aigv1b1.GuardrailProvider{
				Type:    aigv1b1.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &aigv1b1.BedrockGuardrailProvider{Region: "us-east-1"},
			},
			wantErr: "bedrock region, guardrailIdentifier, and guardrailVersion are required",
		},
		{
			name: "bedrock invalid endpoint",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &aigv1b1.BedrockGuardrailProvider{
					Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1", Endpoint: "bedrock",
				},
			},
			wantErr: "valid provider endpoint is required",
		},
		{
			name: "azure",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{
					Endpoint:        "https://content-safety.example.com",
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "api-key"},
				},
			},
		},
		{
			name:     "azure missing configuration",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeAzureContentSafety},
			wantErr:  "azure Content Safety configuration is required",
		},
		{
			name: "azure invalid endpoint",
			provider: aigv1b1.GuardrailProvider{
				Type:               aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{Endpoint: "content-safety"},
			},
			wantErr: "valid provider endpoint is required",
		},
		{
			name: "azure missing secret reference",
			provider: aigv1b1.GuardrailProvider{
				Type:               aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{Endpoint: "https://content-safety.example.com"},
			},
			wantErr: "secret reference is required",
		},
		{
			name: "cross-namespace secret",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{
					Endpoint:        "https://content-safety.example.com",
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "api-key", Namespace: &otherNamespace},
				},
			},
			wantErr: "cross-namespace guardrail secret references are not supported",
		},
		{
			name: "secret missing key",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &aigv1b1.BedrockGuardrailProvider{
					Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
					CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "api-key"},
				},
			},
			wantErr: "secret api-key does not contain key credentials",
		},
		{
			name:     "unsupported provider",
			provider: aigv1b1.GuardrailProvider{Type: "Unknown"},
			wantErr:  `unsupported provider type "Unknown"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := controller.validateGuardrailProvider(t.Context(), namespace, &test.provider)
			if test.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantErr)
			}
		})
	}
}

func TestBackendToGuardrailPolicy(t *testing.T) {
	const namespace = "default"
	controllerClient := newGuardrailPolicyTestClient(t)
	for _, name := range []string{"targets-backend", "targets-other"} {
		target := gwapiv1.ObjectName("backend")
		if name == "targets-other" {
			target = "other"
		}
		require.NoError(t, controllerClient.Create(t.Context(), &aigv1b1.GuardrailPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aigv1b1.GuardrailPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{
					Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend", Name: target,
				}},
			},
		}))
	}
	controller := NewGuardrailPolicyController(controllerClient, fakekube.NewClientset(), ctrl.Log, make(chan event.GenericEvent, 1))

	requests := controller.BackendToGuardrailPolicy(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: namespace},
	})
	require.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: namespace, Name: "targets-backend",
	}}}, requests)
}

func TestGuardrailPolicyControllerReconcileNotFound(t *testing.T) {
	controller := NewGuardrailPolicyController(newGuardrailPolicyTestClient(t), fakekube.NewClientset(), ctrl.Log, make(chan event.GenericEvent, 1))
	result, err := controller.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "missing"}})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
}

func TestGuardrailPolicyControllerReconcileMissingTarget(t *testing.T) {
	const namespace = "default"
	controllerClient := newGuardrailPolicyTestClient(t)
	policy := &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Spec: aigv1b1.GuardrailPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{
				Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend", Name: "missing",
			}},
			Rules: []aigv1b1.GuardrailRule{{
				Name: "regex", Phase: aigv1b1.GuardrailPhaseRequest,
				Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "secret"},
			}},
		},
	}
	require.NoError(t, controllerClient.Create(t.Context(), policy))
	controller := NewGuardrailPolicyController(controllerClient, fakekube.NewClientset(), ctrl.Log, make(chan event.GenericEvent, 1))

	_, err := controller.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(policy)})
	require.ErrorContains(t, err, "target AIServiceBackend default/missing not found")

	var updated aigv1b1.GuardrailPolicy
	require.NoError(t, controllerClient.Get(t.Context(), client.ObjectKeyFromObject(policy), &updated))
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)
}

func TestGuardrailProviderToFilterAPI(t *testing.T) {
	const namespace = "default"
	otherNamespace := gwapiv1.Namespace("other")
	timeout, threshold, severity := int32(3), int32(70), int32(2)
	kube := fakekube.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "api-key", Namespace: namespace},
			Data:       map[string][]byte{"apiKey": []byte("api-secret")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "aws", Namespace: namespace},
			Data:       map[string][]byte{"credentials": []byte("[default]")},
		},
	)
	controller := &GatewayController{kube: kube}
	missing := &gwapiv1.SecretObjectReference{Name: "missing"}
	tests := []struct {
		name     string
		provider aigv1b1.GuardrailProvider
		want     filterapi.GuardrailProvider
		wantErr  string
	}{
		{
			name: "presidio",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypePresidio, TimeoutSeconds: &timeout,
				Presidio: &aigv1b1.PresidioGuardrailProvider{
					Endpoint: "https://presidio.example.com", Language: "es", ScoreThresholdPercent: &threshold,
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "api-key"},
				},
			},
			want: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypePresidio, TimeoutSeconds: 3,
				Presidio: &filterapi.PresidioGuardrailProvider{
					Endpoint: "https://presidio.example.com", Language: "es", ScoreThresholdPercent: 70, APIKey: "api-secret",
				},
			},
		},
		{
			name: "bedrock",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &aigv1b1.BedrockGuardrailProvider{
					Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
					Endpoint:             "https://bedrock.example.com",
					CredentialsSecretRef: &gwapiv1.SecretObjectReference{Name: "aws"},
				},
			},
			want: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &filterapi.BedrockGuardrailProvider{
					Endpoint: "https://bedrock.example.com", Region: "us-east-1",
					GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1", CredentialFileLiteral: "[default]",
				},
			},
		},
		{
			name: "azure",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{
					Endpoint: "https://content-safety.example.com", APIVersion: "2024-09-01", SeverityThreshold: &severity,
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "api-key"},
				},
			},
			want: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &filterapi.AzureContentSafetyGuardrailProvider{
					Endpoint: "https://content-safety.example.com", APIVersion: "2024-09-01", SeverityThreshold: &severity,
					APIKey: "api-secret",
				},
			},
		},
		{
			name:     "regex",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "secret", Action: aigv1b1.GuardrailActionMask},
			want:     filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex, Pattern: "secret", Action: filterapi.GuardrailActionMask},
		},
		{
			name:     "presidio missing configuration",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypePresidio},
			wantErr:  "presidio configuration is required",
		},
		{
			name: "presidio missing secret",
			provider: aigv1b1.GuardrailProvider{
				Type:     aigv1b1.GuardrailProviderTypePresidio,
				Presidio: &aigv1b1.PresidioGuardrailProvider{Endpoint: "https://presidio.example.com", APIKeySecretRef: missing},
			},
			wantErr: "missing",
		},
		{
			name:     "bedrock missing configuration",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeBedrockGuardrails},
			wantErr:  "bedrock configuration is required",
		},
		{
			name: "bedrock missing secret",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeBedrockGuardrails,
				Bedrock: &aigv1b1.BedrockGuardrailProvider{
					Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1", CredentialsSecretRef: missing,
				},
			},
			wantErr: "missing",
		},
		{
			name:     "azure missing configuration",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeAzureContentSafety},
			wantErr:  "azure Content Safety configuration is required",
		},
		{
			name: "azure missing secret reference",
			provider: aigv1b1.GuardrailProvider{
				Type:               aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{Endpoint: "https://content-safety.example.com"},
			},
			wantErr: "secret reference is required",
		},
		{
			name: "azure cross-namespace secret",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeAzureContentSafety,
				AzureContentSafety: &aigv1b1.AzureContentSafetyGuardrailProvider{
					Endpoint:        "https://content-safety.example.com",
					APIKeySecretRef: &gwapiv1.SecretObjectReference{Name: "api-key", Namespace: &otherNamespace},
				},
			},
			wantErr: "cross-namespace guardrail secret references are not supported",
		},
		{
			name:     "http missing configuration",
			provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeHTTP},
			wantErr:  "http guardrail configuration is required",
		},
		{
			name: "http missing secret",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeHTTP,
				HTTP: &aigv1b1.HTTPGuardrailProvider{Endpoint: "https://guardrail.example.com", APIKeySecretRef: missing},
			},
			wantErr: "missing",
		},
		{
			name: "model armor missing secret",
			provider: aigv1b1.GuardrailProvider{
				Type: aigv1b1.GuardrailProviderTypeModelArmor,
				ModelArmor: &aigv1b1.ModelArmorGuardrailProvider{
					Project: "project-id", Location: "us-central1", Template: "template-id", CredentialsSecretRef: missing,
				},
			},
			wantErr: "missing",
		},
		{
			name:     "unsupported provider",
			provider: aigv1b1.GuardrailProvider{Type: "Unknown"},
			wantErr:  `unsupported provider type "Unknown"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			converted, err := controller.guardrailProviderToFilterAPI(t.Context(), namespace, &test.provider)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, converted)
		})
	}
}

func TestGuardrailMaxPayloadBytes(t *testing.T) {
	requestLimit, responseLimit := int64(2048), int64(4096)
	policy := &aigv1b1.GuardrailPolicy{Spec: aigv1b1.GuardrailPolicySpec{
		MaxRequestBodyBytes: &requestLimit, MaxResponseBodyBytes: &responseLimit,
	}}
	require.Equal(t, requestLimit, guardrailMaxPayloadBytes(policy, aigv1b1.GuardrailPhaseRequest))
	require.Equal(t, responseLimit, guardrailMaxPayloadBytes(policy, aigv1b1.GuardrailPhaseResponse))
	require.Equal(t, defaultGuardrailMaxPayloadBytes, guardrailMaxPayloadBytes(&aigv1b1.GuardrailPolicy{}, aigv1b1.GuardrailPhaseResponse))
}

func TestInjectGuardrailsSkipsInjectedRulesAndForeignBackends(t *testing.T) {
	const namespace = "default"
	otherNamespace := gwapiv1.Namespace("other")
	controllerClient := newGuardrailPolicyTestClient(t)
	require.NoError(t, controllerClient.Create(t.Context(), &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Spec: aigv1b1.GuardrailPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend"}},
			Rules: []aigv1b1.GuardrailRule{
				{
					Name: "injected", Phase: aigv1b1.GuardrailPhaseRequest,
					Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "injected"},
				},
				{
					Name: "new", Phase: aigv1b1.GuardrailPhaseResponse,
					Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "new"},
				},
			},
		},
	}))
	controller := &GatewayController{client: controllerClient, kube: fakekube.NewClientset(), logger: ctrl.Log}
	config := &filterapi.Config{}
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
			BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "backend", Namespace: &otherNamespace},
				{Name: "other-backend"},
				{Name: "backend"},
			},
		}}},
	}
	injected := map[string]struct{}{"default/policy/injected": {}}
	require.NoError(t, controller.injectGuardrails(t.Context(), route, config, injected))
	require.Len(t, config.Guardrails, 1)
	require.Equal(t, "default/policy/new", config.Guardrails[0].Name)
	require.Equal(t, []string{"default/backend/route/route/rule/0/ref/2"}, config.Guardrails[0].Backends)
	require.Contains(t, injected, "default/policy/new")
}

func TestInjectGuardrailsListError(t *testing.T) {
	controllerClient := fake.NewClientBuilder().WithScheme(Scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("list failed")
		},
	}).Build()
	controller := &GatewayController{client: controllerClient, kube: fakekube.NewClientset(), logger: ctrl.Log}
	err := controller.injectGuardrails(t.Context(), &aigv1b1.AIGatewayRoute{}, &filterapi.Config{}, map[string]struct{}{})
	require.ErrorContains(t, err, "failed to list GuardrailPolicies: list failed")
}

func TestGuardrailPolicyControllerClientErrors(t *testing.T) {
	const namespace = "default"
	policy := &aigv1b1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Spec: aigv1b1.GuardrailPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{
				Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend", Name: "backend",
			}},
			Rules: []aigv1b1.GuardrailRule{{
				Name: "regex", Phase: aigv1b1.GuardrailPhaseRequest,
				Provider: aigv1b1.GuardrailProvider{Type: aigv1b1.GuardrailProviderTypeRegex, Pattern: "secret"},
			}},
		},
	}
	errClient := errors.New("api server unavailable")
	newController := func(t *testing.T, funcs interceptor.Funcs) *GuardrailPolicyController {
		builder := fake.NewClientBuilder().WithScheme(Scheme).
			WithStatusSubresource(&aigv1b1.GuardrailPolicy{}).
			WithObjects(policy.DeepCopy()).
			WithInterceptorFuncs(funcs)
		require.NoError(t, ApplyIndexing(t.Context(), func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
			builder = builder.WithIndex(obj, field, extractValue)
			return nil
		}))
		return NewGuardrailPolicyController(builder.Build(), fakekube.NewClientset(), ctrl.Log, make(chan event.GenericEvent, 1))
	}
	failList := interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errClient
	}}

	t.Run("get policy", func(t *testing.T) {
		controller := newController(t, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errClient
		}})
		_, err := controller.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(policy)})
		require.ErrorIs(t, err, errClient)
	})

	t.Run("get target backend", func(t *testing.T) {
		controller := newController(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*aigv1b1.AIServiceBackend); ok {
				return errClient
			}
			return c.Get(ctx, key, obj, opts...)
		}})
		_, err := controller.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(policy)})
		require.ErrorContains(t, err, "failed to get AIServiceBackend default/backend: api server unavailable")
	})

	t.Run("list policies and routes", func(t *testing.T) {
		controller := newController(t, failList)
		require.Nil(t, controller.SecretToGuardrailPolicy(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: namespace},
		}))
		require.Nil(t, controller.BackendToGuardrailPolicy(t.Context(), &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: namespace},
		}))
		controller.notifyAIGatewayRoutesForGuardrailPolicy(t.Context(), policy)
		require.Empty(t, controller.aiGatewayRouteChan)
	})

	t.Run("status update", func(t *testing.T) {
		controller := newController(t, interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return errClient
		}})
		// The status update error is logged, not returned.
		controller.updateGuardrailPolicyStatus(t.Context(), policy.DeepCopy(), aigv1b1.ConditionTypeAccepted, "ok")
	})

	t.Run("status update for deleted policy", func(t *testing.T) {
		controller := newController(t, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewNotFound(aigv1b1.SchemeGroupVersion.WithResource("guardrailpolicies").GroupResource(), policy.Name)
		}})
		controller.updateGuardrailPolicyStatus(t.Context(), policy.DeepCopy(), aigv1b1.ConditionTypeAccepted, "ok")
	})

	t.Run("status get error", func(t *testing.T) {
		controller := newController(t, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errClient
		}})
		controller.updateGuardrailPolicyStatus(t.Context(), policy.DeepCopy(), aigv1b1.ConditionTypeAccepted, "ok")
	})
}
