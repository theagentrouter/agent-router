// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"net/url"
	"regexp"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// GuardrailPolicyController implements [reconcile.TypedReconciler] for [aigv1b1.GuardrailPolicy].
type GuardrailPolicyController struct {
	client             client.Client
	kube               kubernetes.Interface
	logger             logr.Logger
	aiGatewayRouteChan chan event.GenericEvent
}

// NewGuardrailPolicyController creates a new reconciler for GuardrailPolicy resources.
func NewGuardrailPolicyController(client client.Client, kube kubernetes.Interface, logger logr.Logger, aiGatewayRouteChan chan event.GenericEvent) *GuardrailPolicyController {
	return &GuardrailPolicyController{
		client:             client,
		kube:               kube,
		logger:             logger,
		aiGatewayRouteChan: aiGatewayRouteChan,
	}
}

// Reconcile implements [reconcile.TypedReconciler] for [aigv1b1.GuardrailPolicy].
func (c *GuardrailPolicyController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var policy aigv1b1.GuardrailPolicy
	if err := c.client.Get(ctx, req.NamespacedName, &policy); err != nil {
		if client.IgnoreNotFound(err) == nil {
			c.logger.Info("Deleting GuardrailPolicy",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	c.logger.Info("Reconciling GuardrailPolicy", "namespace", req.Namespace, "name", req.Name)
	if handleFinalizer(ctx, c.client, c.logger, &policy, func(ctx context.Context, policy *aigv1b1.GuardrailPolicy) error {
		c.notifyAIGatewayRoutesForGuardrailPolicy(ctx, policy)
		return nil
	}) {
		return ctrl.Result{}, nil
	}
	if err := c.syncGuardrailPolicy(ctx, &policy); err != nil {
		c.logger.Error(err, "failed to sync GuardrailPolicy")
		c.updateGuardrailPolicyStatus(ctx, &policy, aigv1b1.ConditionTypeNotAccepted, err.Error())
		c.notifyAIGatewayRoutesForGuardrailPolicy(ctx, &policy)
		return ctrl.Result{}, err
	}

	c.updateGuardrailPolicyStatus(ctx, &policy, aigv1b1.ConditionTypeAccepted, "GuardrailPolicy reconciled successfully")
	c.notifyAIGatewayRoutesForGuardrailPolicy(ctx, &policy)
	return ctrl.Result{}, nil
}

// SecretToGuardrailPolicy maps Secret changes to GuardrailPolicy reconcile requests.
func (c *GuardrailPolicyController) SecretToGuardrailPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	var policies aigv1b1.GuardrailPolicyList
	key := fmt.Sprintf("%s.%s", obj.GetName(), obj.GetNamespace())
	if err := c.client.List(ctx, &policies,
		client.MatchingFields{k8sClientIndexSecretToReferencingGuardrailPolicy: key}); err != nil {
		c.logger.Error(err, "failed to list GuardrailPolicies for secret", "secret", key)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
	}
	return requests
}

func (c *GuardrailPolicyController) syncGuardrailPolicy(ctx context.Context, policy *aigv1b1.GuardrailPolicy) error {
	for i := range policy.Spec.Rules {
		if err := c.validateGuardrailProvider(ctx, policy.Namespace, &policy.Spec.Rules[i].Provider); err != nil {
			return fmt.Errorf("rule %q: %w", policy.Spec.Rules[i].Name, err)
		}
	}

	for _, ref := range policy.Spec.TargetRefs {
		var backend aigv1b1.AIServiceBackend
		key := client.ObjectKey{Namespace: policy.Namespace, Name: string(ref.Name)}
		if err := c.client.Get(ctx, key, &backend); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("target AIServiceBackend %s not found", key)
			}
			return fmt.Errorf("failed to get AIServiceBackend %s: %w", key, err)
		}
	}

	return nil
}

func (c *GuardrailPolicyController) validateGuardrailProvider(ctx context.Context, namespace string, provider *aigv1b1.GuardrailProvider) error {
	if provider.Action != "" && provider.Action != aigv1b1.GuardrailActionBlock && provider.Action != aigv1b1.GuardrailActionMonitor && provider.Action != aigv1b1.GuardrailActionMask {
		return fmt.Errorf("unsupported action %q", provider.Action)
	}
	if provider.Action == aigv1b1.GuardrailActionMask && provider.Type == aigv1b1.GuardrailProviderTypeAzureContentSafety {
		return fmt.Errorf("azure Content Safety does not support mask")
	}
	if provider.FailureMode != "" && provider.FailureMode != aigv1b1.GuardrailFailureModeFailClosed && provider.FailureMode != aigv1b1.GuardrailFailureModeFailOpen {
		return fmt.Errorf("unsupported failureMode %q", provider.FailureMode)
	}
	switch provider.Type {
	case aigv1b1.GuardrailProviderTypeRegex:
		if provider.Pattern == "" {
			return fmt.Errorf("regex pattern is required")
		}
		if _, err := regexp.Compile(provider.Pattern); err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
	case aigv1b1.GuardrailProviderTypePresidio:
		if provider.Presidio == nil {
			return fmt.Errorf("presidio configuration is required")
		}
		if err := validateGuardrailEndpoint(provider.Presidio.Endpoint); err != nil {
			return err
		}
		if provider.Presidio.APIKeySecretRef != nil {
			return c.validateGuardrailSecret(ctx, namespace, provider.Presidio.APIKeySecretRef, "apiKey")
		}
	case aigv1b1.GuardrailProviderTypeBedrockGuardrails:
		if provider.Bedrock == nil || provider.Bedrock.Region == "" || provider.Bedrock.GuardrailIdentifier == "" || provider.Bedrock.GuardrailVersion == "" {
			return fmt.Errorf("bedrock region, guardrailIdentifier, and guardrailVersion are required")
		}
		if provider.Bedrock.Endpoint != "" {
			if err := validateGuardrailEndpoint(provider.Bedrock.Endpoint); err != nil {
				return err
			}
		}
		if provider.Bedrock.CredentialsSecretRef != nil {
			return c.validateGuardrailSecret(ctx, namespace, provider.Bedrock.CredentialsSecretRef, "credentials")
		}
	case aigv1b1.GuardrailProviderTypeAzureContentSafety:
		if provider.AzureContentSafety == nil {
			return fmt.Errorf("azure Content Safety configuration is required")
		}
		if err := validateGuardrailEndpoint(provider.AzureContentSafety.Endpoint); err != nil {
			return err
		}
		return c.validateGuardrailSecret(ctx, namespace, provider.AzureContentSafety.APIKeySecretRef, "apiKey")
	default:
		return fmt.Errorf("unsupported provider type %q", provider.Type)
	}
	return nil
}

func validateGuardrailEndpoint(endpoint string) error {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("valid provider endpoint is required")
	}
	return nil
}

func (c *GuardrailPolicyController) validateGuardrailSecret(ctx context.Context, namespace string, ref *gwapiv1.SecretObjectReference, key string) error {
	if ref == nil {
		return fmt.Errorf("secret reference is required")
	}
	if ref.Namespace != nil && string(*ref.Namespace) != namespace {
		return fmt.Errorf("cross-namespace guardrail secret references are not supported")
	}
	secret, err := c.kube.CoreV1().Secrets(namespace).Get(ctx, string(ref.Name), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get secret %s: %w", ref.Name, err)
	}
	if _, ok := secret.Data[key]; !ok {
		if _, ok = secret.StringData[key]; !ok {
			return fmt.Errorf("secret %s does not contain key %s", ref.Name, key)
		}
	}
	return nil
}

// BackendToGuardrailPolicy maps AIServiceBackend changes to GuardrailPolicy reconcile requests.
func (c *GuardrailPolicyController) BackendToGuardrailPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	var policies aigv1b1.GuardrailPolicyList
	key := fmt.Sprintf("%s.%s", obj.GetName(), obj.GetNamespace())
	if err := c.client.List(ctx, &policies,
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingGuardrailPolicy: key}); err != nil {
		c.logger.Error(err, "failed to list GuardrailPolicies for backend", "backend", key)
		return nil
	}

	var requests []reconcile.Request
	for i := range policies.Items {
		policy := &policies.Items[i]
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(policy),
		})
	}
	return requests
}

func (c *GuardrailPolicyController) notifyAIGatewayRoutesForGuardrailPolicy(ctx context.Context, policy *aigv1b1.GuardrailPolicy) {
	for _, ref := range policy.Spec.TargetRefs {
		key := fmt.Sprintf("%s.%s", ref.Name, policy.Namespace)
		var routes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &routes,
			client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: key}); err != nil {
			c.logger.Error(err, "failed to list AIGatewayRoutes for guardrail policy", "backend", key)
			continue
		}
		for i := range routes.Items {
			route := &routes.Items[i]
			c.logger.Info("notifying AIGatewayRoute of GuardrailPolicy change",
				"route", route.Name, "namespace", route.Namespace,
				"guardrailPolicy", policy.Name)
			c.aiGatewayRouteChan <- event.GenericEvent{Object: route}
		}
	}
}

func (c *GuardrailPolicyController) updateGuardrailPolicyStatus(ctx context.Context, policy *aigv1b1.GuardrailPolicy, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, policy); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		policy.Status.Conditions = newConditions(conditionType, message)
		return c.client.Status().Update(ctx, policy)
	})
	if err != nil {
		c.logger.Error(err, "failed to update GuardrailPolicy status",
			"namespace", policy.Namespace, "name", policy.Name)
	}
}
