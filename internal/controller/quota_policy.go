// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/runner"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/translator"
)

// QuotaPolicyController implements [reconcile.TypedReconciler] for [aigv1a1.QuotaPolicy].
type QuotaPolicyController struct {
	client                  client.Client
	kube                    kubernetes.Interface
	logger                  logr.Logger
	rateLimitRunner         *runner.Runner
	aiGatewayRouteChan      chan event.GenericEvent
	referenceGrantValidator *referenceGrantValidator
	// configCache stores rate limit configs per QuotaPolicy namespace/name.
	// This allows incremental updates when only one policy changes.
	configCache       map[string][]*rlsconfv3.RateLimitConfig
	policyBackendKeys map[string][]string
	mu                sync.RWMutex
}

// NewQuotaPolicyController creates a new reconciler for QuotaPolicy resources.
func NewQuotaPolicyController(
	client client.Client,
	kube kubernetes.Interface,
	logger logr.Logger,
	rateLimitRunner *runner.Runner,
	aiGatewayRouteChan chan event.GenericEvent,
) *QuotaPolicyController {
	return &QuotaPolicyController{
		client:                  client,
		kube:                    kube,
		logger:                  logger,
		rateLimitRunner:         rateLimitRunner,
		aiGatewayRouteChan:      aiGatewayRouteChan,
		referenceGrantValidator: newReferenceGrantValidator(client),
		configCache:             make(map[string][]*rlsconfv3.RateLimitConfig),
		policyBackendKeys:       make(map[string][]string),
	}
}

// Reconcile implements [reconcile.TypedReconciler] for [aigv1a1.QuotaPolicy].
func (c *QuotaPolicyController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var quotaPolicy aigv1a1.QuotaPolicy
	if err := c.client.Get(ctx, req.NamespacedName, &quotaPolicy); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}

		c.logger.Info(
			"Deleting QuotaPolicy",
			"namespace", req.Namespace,
			"name", req.Name,
		)

		if err := c.deleteQuotaPolicyConfig(ctx, req.NamespacedName); err != nil {
			return ctrl.Result{}, err
		}
		// If cached target keys exist, notify only the affected routes. Otherwise the deleted
		// object's cross-namespace targets are unknown, so a full route sweep is the only safe
		// fallback.

		hasTargetKeys, err := c.notifyQuotaPolicyRoutesOnDeletion(
			ctx, req.NamespacedName,
		)
		if err != nil {
			return ctrl.Result{}, err
		}

		if !hasTargetKeys {
			return ctrl.Result{}, c.notifyAllAIGatewayRoutes(ctx)
		}

		return ctrl.Result{}, nil
	}
	c.logger.Info("Reconciling QuotaPolicy", "namespace", req.Namespace, "name", req.Name)

	onDelete, err := handleFinalizer(ctx, c.client, c.logger, &quotaPolicy, func(ctx context.Context, policy *aigv1a1.QuotaPolicy) error {
		// Notify the AIGatewayRoutes targeting this policy's backends so their derived HTTPRoutes are
		// re-reconciled (re-stamping the quota-policy-hash annotation) and Envoy Gateway re-translates
		// without the deleted policy.
		c.notifyAIGatewayRoutes(ctx, policy)
		return c.deleteQuotaPolicyConfig(ctx, req.NamespacedName)
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if onDelete {
		return ctrl.Result{}, nil
	}

	cacheKey := NamespacedNameKey(quotaPolicy.Namespace, quotaPolicy.Name)
	c.mu.RLock()
	previousTargetKeys := slices.Clone(c.policyBackendKeys[cacheKey])
	c.mu.RUnlock()
	// After a controller restart, previousTargetKeys can be empty even for an
	// accepted policy. If that policy changes or removes a target, routes for
	// the old backend cannot be identified here and may retain a stale
	// quota-policy-hash until their gateway is translated for another reason.
	// Deletion has a separate full-sweep fallback because the deleted object no
	// longer contains its target references.

	// An invalid cost expression is reported in the status but not returned: only a change to the
	// policy can fix it, so retrying would repeat the same work without ever succeeding.
	costErr := validateQuotaCostExpressions(&quotaPolicy)
	err = c.syncQuotaPolicy(ctx, &quotaPolicy)
	var statusErr error
	if notAcceptedErr := errors.Join(err, costErr); notAcceptedErr != nil {
		reason := "ReconciliationFailed"
		var referenceErr *ReferenceNotPermittedError
		if errors.As(notAcceptedErr, &referenceErr) {
			reason = aigv1a1.ConditionReasonRefNotPermitted
		}
		statusErr = c.updateQuotaPolicyStatus(ctx, &quotaPolicy, aigv1a1.ConditionTypeNotAccepted, reason, notAcceptedErr.Error())
	} else {
		statusErr = c.updateQuotaPolicyStatus(ctx, &quotaPolicy, aigv1a1.ConditionTypeAccepted, "ReconciliationSucceeded", "QuotaPolicy reconciled successfully")
	}
	c.notifyAIGatewayRoutes(ctx, &quotaPolicy)
	if notifyErr := c.notifyQuotaPolicyRoutesForKeys(ctx, previousTargetKeys); notifyErr != nil {
		c.logger.Error(notifyErr, "failed to notify AIGatewayRoutes for previous QuotaPolicy targets",
			"policy", cacheKey)
	}
	return ctrl.Result{}, errors.Join(err, statusErr)
}

// defaultQuotaCostExpression is the cost expression of a per-model quota that does not set one.
const defaultQuotaCostExpression = "total_tokens"

// quotaCostExpression returns the cost expression of a per-model quota, and an error if it is not a
// valid one. The QuotaPolicy status and the cost injected into the filter config both rely on it, so
// an expression that the injection skips is reported in the status of its policy.
func quotaCostExpression(quota *aigv1a1.QuotaDefinition) (string, error) {
	expr := defaultQuotaCostExpression
	if quota.CostExpression != nil {
		expr = *quota.CostExpression
	}
	_, err := llmcostcel.NewProgram(expr)
	return expr, err
}

// maxQuotaCostExpressionErrorLength bounds the error reported for one cost expression. The CEL error
// quotes the expression, so without a bound one long expression would crowd the other models out of
// the status message.
const maxQuotaCostExpressionErrorLength = 512

// validateQuotaCostExpressions returns an error naming each per-model quota of the policy whose cost
// expression is invalid.
func validateQuotaCostExpressions(policy *aigv1a1.QuotaPolicy) error {
	var errs []error
	for i := range policy.Spec.PerModelQuotas {
		pmq := &policy.Spec.PerModelQuotas[i]
		if pmq.ModelName == nil {
			continue
		}
		if _, err := quotaCostExpression(&pmq.Quota); err != nil {
			msg := err.Error()
			if len(msg) > maxQuotaCostExpressionErrorLength {
				msg = strings.ToValidUTF8(msg[:maxQuotaCostExpressionErrorLength], "") + "..."
			}
			errs = append(errs, fmt.Errorf("invalid costExpression for model %q: %s", *pmq.ModelName, msg))
		}
	}
	return errors.Join(errs...)
}

func conditionStatus(conditionType string) metav1.ConditionStatus {
	if conditionType == aigv1a1.ConditionTypeAccepted {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// syncQuotaPolicy is the main reconciliation logic. It builds rate limit configs
// for the changed QuotaPolicy only, updates the cache, and pushes the merged
// configs to the xDS runner.
func (c *QuotaPolicyController) syncQuotaPolicy(ctx context.Context, policy *aigv1a1.QuotaPolicy) error {
	// Resolve target backends for this policy.
	var backends []*aigv1b1.AIServiceBackend
	var targetErrors []error
	for _, ref := range policy.Spec.TargetRefs {
		targetNamespace := TargetNamespace(ref, policy.Namespace)
		if err := c.referenceGrantValidator.validateQuotaPolicyAIServiceBackendReference(
			ctx, policy.Namespace, targetNamespace, string(ref.Name)); err != nil {
			// Validate authorization before reading the remote backend. This
			// avoids exposing whether an unauthorized target exists.
			targetErrors = append(targetErrors, fmt.Errorf(
				"QuotaPolicy target %s/%s is not permitted: %w",
				targetNamespace, ref.Name, err))
			continue
		}
		var backend aigv1b1.AIServiceBackend
		key := client.ObjectKey{
			Namespace: targetNamespace,
			Name:      string(ref.Name),
		}
		if err := c.client.Get(ctx, key, &backend); err != nil {
			if apierrors.IsNotFound(err) {
				c.logger.Info("AIServiceBackend not found, skipping",
					"namespace", key.Namespace, "name", key.Name,
					"quotaPolicy", policy.Name)
				continue
			}
			return fmt.Errorf("failed to get AIServiceBackend %s: %w", key, err)
		}
		backends = append(backends, &backend)
	}
	if len(backends) == 0 && len(policy.Spec.TargetRefs) > 0 {
		targetErrors = append(targetErrors, fmt.Errorf(
			"none of the %d authorized target AIServiceBackends were found for QuotaPolicy %s/%s",
			len(policy.Spec.TargetRefs), policy.Namespace, policy.Name))
	}

	// Build rate limit configs for this policy.
	var configs []*rlsconfv3.RateLimitConfig
	if len(backends) > 0 {
		var err error
		configs, err = translator.BuildRateLimitConfigs(policy, backends)
		if err != nil {
			return fmt.Errorf("failed to build rate limit configs for QuotaPolicy %s/%s: %w",
				policy.Namespace, policy.Name, err)
		}
	}
	// Update cache and push merged configs to xDS.
	// Hold the lock across both cache update and UpdateConfigs to prevent
	// out-of-order execution where a later reconcile's UpdateConfigs could
	// be overwritten by an earlier one completing after it.
	cacheKey := NamespacedNameKey(policy.Namespace, policy.Name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configCache[cacheKey] = configs
	targetKeys := make([]string, 0, len(policy.Spec.TargetRefs))
	for _, ref := range policy.Spec.TargetRefs {
		targetKeys = append(targetKeys, namespacedNameIndexKey(
			string(ref.Name), TargetNamespace(ref, policy.Namespace)))
	}
	c.policyBackendKeys[cacheKey] = targetKeys
	allConfigs := c.getMergedConfigsLocked()

	if err := c.rateLimitRunner.UpdateConfigs(ctx, allConfigs); err != nil {
		return err
	}
	if len(targetErrors) > 0 {
		return errors.Join(targetErrors...)
	}
	return nil
}

// deleteQuotaPolicyConfig removes a QuotaPolicy's configs from the cache
// and updates the xDS snapshot.
func (c *QuotaPolicyController) deleteQuotaPolicyConfig(ctx context.Context, key client.ObjectKey) error {
	cacheKey := NamespacedNameKey(key.Namespace, key.Name)
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.configCache, cacheKey)
	allConfigs := c.getMergedConfigsLocked()

	if err := c.rateLimitRunner.UpdateConfigs(ctx, allConfigs); err != nil {
		return err
	}
	return nil
}

// notifyQuotaPolicyRoutesOnDeletion uses the last successfully reconciled
// backend identities so deletion also reaches routes in other namespaces.
func (c *QuotaPolicyController) notifyQuotaPolicyRoutesOnDeletion(ctx context.Context, key client.ObjectKey) (bool, error) {
	cacheKey := NamespacedNameKey(key.Namespace, key.Name)
	c.mu.RLock()
	targetKeys := slices.Clone(c.policyBackendKeys[cacheKey])
	c.mu.RUnlock()
	if len(targetKeys) > 0 {
		if err := c.notifyQuotaPolicyRoutesForKeys(ctx, targetKeys); err != nil {
			return true, err
		}
	}
	c.mu.Lock()
	delete(c.policyBackendKeys, cacheKey)
	c.mu.Unlock()
	return len(targetKeys) > 0, nil
}

func (c *QuotaPolicyController) notifyQuotaPolicyRoutesForKeys(ctx context.Context, targetKeys []string) error {
	for _, backendKey := range targetKeys {
		var routes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &routes,
			client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: backendKey}); err != nil {
			return fmt.Errorf("failed to list AIGatewayRoutes for QuotaPolicy backend %s: %w", backendKey, err)
		}
		for i := range routes.Items {
			c.aiGatewayRouteChan <- event.GenericEvent{Object: &routes.Items[i]}
		}
	}
	return nil
}

// notifyAllAIGatewayRoutes is a conservative recovery path for a policy
// deletion whose prior target set was not retained in memory, such as after a
// controller restart. It prevents a stale generated route from retaining
// policy-derived configuration.
func (c *QuotaPolicyController) notifyAllAIGatewayRoutes(ctx context.Context) error {
	var routes aigv1b1.AIGatewayRouteList
	if err := c.client.List(ctx, &routes); err != nil {
		c.logger.Error(err, "failed to list AIGatewayRoutes for QuotaPolicy deletion recovery")
		return fmt.Errorf("failed to list AIGatewayRoutes for QuotaPolicy deletion recovery: %w", err)
	}
	for i := range routes.Items {
		c.aiGatewayRouteChan <- event.GenericEvent{Object: &routes.Items[i]}
	}
	return nil
}

// getMergedConfigsLocked merges all cached configs into a single RateLimitConfig.
// When multiple QuotaPolicies define the same descriptor path, the policy whose
// namespace/name is alphabetically first takes precedence. Keys are sorted to
// ensure deterministic snapshot generation.
// Caller must hold c.mu lock.
func (c *QuotaPolicyController) getMergedConfigsLocked() []*rlsconfv3.RateLimitConfig {
	var allDescriptors []*rlsconfv3.RateLimitDescriptor
	keys := make([]string, 0, len(c.configCache))
	for k := range c.configCache {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, cfg := range c.configCache[k] {
			allDescriptors = append(allDescriptors, cfg.Descriptors...)
		}
	}
	if len(allDescriptors) == 0 {
		return nil
	}
	merged := translator.MergeDescriptors(allDescriptors)
	return []*rlsconfv3.RateLimitConfig{
		{
			Name:        translator.QuotaDomain,
			Domain:      translator.QuotaDomain,
			Descriptors: merged,
		},
	}
}

// BackendToQuotaPolicy maps AIServiceBackend changes to QuotaPolicy reconcile
// requests. This is used as an EnqueueRequestsFromMapFunc handler so that
// when an AIServiceBackend changes, all QuotaPolicies targeting it are re-reconciled.
func (c *QuotaPolicyController) BackendToQuotaPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	var quotaPolicies aigv1a1.QuotaPolicyList
	key := namespacedNameIndexKey(obj.GetName(), obj.GetNamespace())
	if err := c.client.List(ctx, &quotaPolicies,
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingQuotaPolicy: key}); err != nil {
		c.logger.Error(err, "failed to list QuotaPolicies for backend", "backend", key)
		return nil
	}

	var requests []reconcile.Request
	for i := range quotaPolicies.Items {
		qp := &quotaPolicies.Items[i]
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(qp),
		})
	}
	return requests
}

// notifyAIGatewayRoutes sends events to the AIGatewayRoute controller for all
// routes that reference the backends targeted by the given QuotaPolicy.
// This triggers re-reconciliation of the HTTPRoute, which causes Envoy Gateway
// to re-translate xDS and call PostTranslateModify with the updated QuotaPolicy.
func (c *QuotaPolicyController) notifyAIGatewayRoutes(ctx context.Context, policy *aigv1a1.QuotaPolicy) {
	for _, ref := range policy.Spec.TargetRefs {
		key := namespacedNameIndexKey(string(ref.Name), TargetNamespace(ref, policy.Namespace))
		var aiGatewayRoutes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &aiGatewayRoutes,
			client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: key}); err != nil {
			c.logger.Error(err, "failed to list AIGatewayRoutes for backend", "backend", key)
			continue
		}
		for i := range aiGatewayRoutes.Items {
			route := &aiGatewayRoutes.Items[i]
			c.aiGatewayRouteChan <- event.GenericEvent{Object: route}
		}
	}
}

// maxConditionMessageLength is the longest condition message the API server accepts. A longer one makes
// the whole status update fail, which would leave the previous condition in place.
const maxConditionMessageLength = 32768

// updateQuotaPolicyStatus updates the status of the QuotaPolicy.
//
// A returned error makes controller-runtime requeue the policy, because no other event would
// trigger the reconcile that writes the status again.
func (c *QuotaPolicyController) updateQuotaPolicyStatus(ctx context.Context, policy *aigv1a1.QuotaPolicy, conditionType, reason, message string) error {
	if len(message) > maxConditionMessageLength {
		message = strings.ToValidUTF8(message[:maxConditionMessageLength], "")
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, policy); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		policy.Status.Conditions = []metav1.Condition{{
			Type:               conditionType,
			Status:             conditionStatus(conditionType),
			Reason:             reason,
			Message:            message,
			LastTransitionTime: metav1.Now(),
		}}
		return c.client.Status().Update(ctx, policy)
	})
	if err != nil {
		return fmt.Errorf("failed to update QuotaPolicy status: %w", err)
	}
	return nil
}

// TargetNamespace resolves the namespace of a QuotaPolicy target reference.
// An omitted or empty namespace means the namespace of the QuotaPolicy.
func TargetNamespace(
	ref gwapiv1a2.NamespacedPolicyTargetReference,
	policyNamespace string,
) string {
	if ref.Namespace == nil || *ref.Namespace == "" {
		return policyNamespace
	}
	return string(*ref.Namespace)
}
