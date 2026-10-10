// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/quotapolicy"
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
		if client.IgnoreNotFound(err) == nil {
			if err = c.deleteQuotaPolicyConfig(ctx, req.NamespacedName); err != nil {
				return ctrl.Result{}, err
			}
			// Spec.TargetRefs are no longer available, so notify every AIGatewayRoute that references
			// a backend in this namespace (wherever the route lives) instead.
			if c.notifyQuotaPolicyRoutesOnDeletion(ctx, req.NamespacedName) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, c.notifyAIGatewayRoutesForNamespace(ctx, req.Namespace)
		}
		return ctrl.Result{}, err
	}
	c.logger.Info("Reconciling QuotaPolicy", "namespace", req.Namespace, "name", req.Name)

	if handleFinalizer(ctx, c.client, c.logger, &quotaPolicy, func(ctx context.Context, policy *aigv1a1.QuotaPolicy) error {
		// Notify the AIGatewayRoutes targeting this policy's backends so their derived HTTPRoutes are
		// re-reconciled (re-stamping the quota-policy-hash annotation) and Envoy Gateway re-translates
		// without the deleted policy.
		c.notifyAIGatewayRoutes(ctx, policy)
		return c.deleteQuotaPolicyConfig(ctx, req.NamespacedName)
	}) {
		return ctrl.Result{}, nil
	}

	cacheKey := fmt.Sprintf("%s/%s", quotaPolicy.Namespace, quotaPolicy.Name)
	c.mu.RLock()
	previousTargetKeys := append([]string(nil), c.policyBackendKeys[cacheKey]...)
	c.mu.RUnlock()
	needsRouteRecovery := len(previousTargetKeys) == 0 && quotaPolicyWasAccepted(&quotaPolicy)

	err := c.syncQuotaPolicy(ctx, &quotaPolicy)
	if err != nil {
		reason := "ReconciliationFailed"
		var referenceErr *ReferenceNotPermittedError
		if errors.As(err, &referenceErr) {
			reason = aigv1a1.ConditionReasonRefNotPermitted
		}
		c.updateQuotaPolicyStatus(ctx, &quotaPolicy, aigv1a1.ConditionTypeNotAccepted, reason, err.Error())
	} else {
		c.updateQuotaPolicyStatus(ctx, &quotaPolicy, aigv1a1.ConditionTypeAccepted, "ReconciliationSucceeded", "QuotaPolicy reconciled successfully")
	}
	c.notifyAIGatewayRoutes(ctx, &quotaPolicy)
	c.notifyQuotaPolicyRoutesForKeys(ctx, previousTargetKeys)
	if needsRouteRecovery {
		c.notifyAllAIGatewayRoutes(ctx)
	}
	return ctrl.Result{}, err
}

func conditionStatus(conditionType string) metav1.ConditionStatus {
	if conditionType == aigv1a1.ConditionTypeAccepted {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func quotaPolicyWasAccepted(policy *aigv1a1.QuotaPolicy) bool {
	for _, condition := range policy.Status.Conditions {
		if condition.Type == aigv1a1.ConditionTypeAccepted &&
			condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// syncQuotaPolicy is the main reconciliation logic. It builds rate limit configs
// for the changed QuotaPolicy only, updates the cache, and pushes the merged
// configs to the xDS runner.
func (c *QuotaPolicyController) syncQuotaPolicy(ctx context.Context, policy *aigv1a1.QuotaPolicy) error {
	// Resolve target backends for this policy.
	var backends []*aigv1b1.AIServiceBackend
	var targetErrors []error
	for _, ref := range policy.Spec.TargetRefs {
		targetNamespace := quotapolicy.TargetNamespace(ref, policy.Namespace)
		if (ref.Group != "" && ref.Group != aiServiceBackendGroup) ||
			(ref.Kind != "" && ref.Kind != aiServiceBackendKind) {
			targetErrors = append(targetErrors, fmt.Errorf(
				"QuotaPolicy target %s/%s has unsupported group/kind %q/%q",
				targetNamespace, ref.Name, ref.Group, ref.Kind))
			continue
		}
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
	cacheKey := fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configCache[cacheKey] = configs
	targetKeys := make([]string, 0, len(policy.Spec.TargetRefs))
	for _, ref := range policy.Spec.TargetRefs {
		targetKeys = append(targetKeys, fmt.Sprintf("%s.%s", string(ref.Name), quotapolicy.TargetNamespace(ref, policy.Namespace)))
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
	cacheKey := fmt.Sprintf("%s/%s", key.Namespace, key.Name)
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
func (c *QuotaPolicyController) notifyQuotaPolicyRoutesOnDeletion(ctx context.Context, key client.ObjectKey) bool {
	cacheKey := fmt.Sprintf("%s/%s", key.Namespace, key.Name)
	c.mu.RLock()
	targetKeys := append([]string(nil), c.policyBackendKeys[cacheKey]...)
	c.mu.RUnlock()
	if len(targetKeys) > 0 {
		c.notifyQuotaPolicyRoutesForKeys(ctx, targetKeys)
	}
	c.mu.Lock()
	delete(c.policyBackendKeys, cacheKey)
	c.mu.Unlock()
	return len(targetKeys) > 0
}

func (c *QuotaPolicyController) notifyQuotaPolicyRoutesForKeys(ctx context.Context, targetKeys []string) {
	for _, backendKey := range targetKeys {
		var routes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &routes,
			client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: backendKey}); err != nil {
			c.logger.Error(err, "failed to list AIGatewayRoutes for deleted QuotaPolicy backend", "backend", backendKey)
			continue
		}
		for i := range routes.Items {
			c.aiGatewayRouteChan <- event.GenericEvent{Object: &routes.Items[i]}
		}
	}
}

// notifyAllAIGatewayRoutes is a conservative recovery path for a policy
// deletion whose prior target set was not retained in memory, such as after a
// controller restart. It prevents a stale generated route from retaining
// policy-derived configuration.
func (c *QuotaPolicyController) notifyAllAIGatewayRoutes(ctx context.Context) {
	var routes aigv1b1.AIGatewayRouteList
	if err := c.client.List(ctx, &routes); err != nil {
		c.logger.Error(err, "failed to list AIGatewayRoutes for QuotaPolicy deletion recovery")
		return
	}
	for i := range routes.Items {
		c.aiGatewayRouteChan <- event.GenericEvent{Object: &routes.Items[i]}
	}
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
		key := namespacedNameIndexKey(string(ref.Name), quotapolicy.TargetNamespace(ref, policy.Namespace))
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

// notifyAIGatewayRoutesForNamespace sends one event for each AIGatewayRoute that references an
// AIServiceBackend in the given namespace, wherever the route lives. It is used on the QuotaPolicy
// deletion (not-found) path, where Spec.TargetRefs are no longer available.
func (c *QuotaPolicyController) notifyAIGatewayRoutesForNamespace(ctx context.Context, namespace string) error {
	var backends aigv1b1.AIServiceBackendList
	if err := c.client.List(ctx, &backends, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list AIServiceBackends in namespace %s: %w", namespace, err)
	}
	notified := make(map[client.ObjectKey]struct{})
	for i := range backends.Items {
		key := namespacedNameIndexKey(backends.Items[i].Name, namespace)
		var aiGatewayRoutes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &aiGatewayRoutes,
			client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: key}); err != nil {
			return fmt.Errorf("failed to list AIGatewayRoutes for backend %s: %w", key, err)
		}
		for j := range aiGatewayRoutes.Items {
			route := &aiGatewayRoutes.Items[j]
			// A route referencing several backends in this namespace is found once per backend;
			// notify it only once.
			routeKey := client.ObjectKeyFromObject(route)
			if _, ok := notified[routeKey]; ok {
				continue
			}
			notified[routeKey] = struct{}{}
			c.logger.Info("notifying AIGatewayRoute of QuotaPolicy deletion",
				"route", route.Name, "namespace", route.Namespace)
			c.aiGatewayRouteChan <- event.GenericEvent{Object: route}
		}
	}
	return nil
}

// updateQuotaPolicyStatus updates the status of the QuotaPolicy.
func (c *QuotaPolicyController) updateQuotaPolicyStatus(ctx context.Context, policy *aigv1a1.QuotaPolicy, conditionType, reason, message string) {
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
		c.logger.Error(err, "failed to update QuotaPolicy status",
			"namespace", policy.Namespace, "name", policy.Name)
	}
}
