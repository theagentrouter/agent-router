// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"

	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/runner"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/translator"
)

// QuotaPolicyController implements [reconcile.TypedReconciler] for [aigv1a1.QuotaPolicy].
type QuotaPolicyController struct {
	client             client.Client
	apiReader          client.Reader
	kube               kubernetes.Interface
	logger             logr.Logger
	rateLimitRunner    *runner.Runner
	aiGatewayRouteChan chan event.GenericEvent
	// configCache stores rate limit configs per QuotaPolicy namespace/name.
	// This allows incremental updates when only one policy changes.
	configCache map[string][]*rlsconfv3.RateLimitConfig
	mu          sync.RWMutex
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
		client:             client,
		apiReader:          client,
		kube:               kube,
		logger:             logger,
		rateLimitRunner:    rateLimitRunner,
		aiGatewayRouteChan: aiGatewayRouteChan,
		configCache:        make(map[string][]*rlsconfv3.RateLimitConfig),
	}
}

// Reconcile implements [reconcile.TypedReconciler] for [aigv1a1.QuotaPolicy].
func (c *QuotaPolicyController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var quotaPolicy aigv1a1.QuotaPolicy
	if err := c.client.Get(ctx, req.NamespacedName, &quotaPolicy); err != nil {
		if client.IgnoreNotFound(err) == nil {
			c.logger.Info("Deleting QuotaPolicy",
				"namespace", req.Namespace, "name", req.Name)
			if err = c.deleteQuotaPolicyConfig(ctx, req.NamespacedName); err != nil {
				return ctrl.Result{}, err
			}
			c.notifyAllAIGatewayRoutesInNamespace(ctx, req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	c.logger.Info("Reconciling QuotaPolicy", "namespace", req.Namespace, "name", req.Name)

	onDelete, err := handleFinalizer(ctx, c.client, c.apiReader, &quotaPolicy, func(ctx context.Context, _ *aigv1a1.QuotaPolicy) error {
		return c.deleteQuotaPolicyConfig(ctx, req.NamespacedName)
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if onDelete {
		return ctrl.Result{}, nil
	}

	if err := c.syncQuotaPolicy(ctx, &quotaPolicy); err != nil {
		c.logger.Error(err, "failed to sync QuotaPolicy")
		c.updateQuotaPolicyStatus(ctx, &quotaPolicy, aigv1a1.ConditionTypeNotAccepted, err.Error())
		return ctrl.Result{}, err
	}
	c.updateQuotaPolicyStatus(ctx, &quotaPolicy, aigv1a1.ConditionTypeAccepted, "QuotaPolicy reconciled successfully")
	c.notifyAIGatewayRoutes(ctx, &quotaPolicy)
	return ctrl.Result{}, nil
}

// syncQuotaPolicy is the main reconciliation logic. It builds rate limit configs
// for the changed QuotaPolicy only, updates the cache, and pushes the merged
// configs to the xDS runner.
func (c *QuotaPolicyController) syncQuotaPolicy(ctx context.Context, policy *aigv1a1.QuotaPolicy) error {
	// Resolve target backends for this policy.
	var backends []*aigv1b1.AIServiceBackend
	for _, ref := range policy.Spec.TargetRefs {
		var backend aigv1b1.AIServiceBackend
		key := client.ObjectKey{
			Namespace: policy.Namespace,
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
		return fmt.Errorf("none of the %d target AIServiceBackends were found for QuotaPolicy %s/%s, will retry",
			len(policy.Spec.TargetRefs), policy.Namespace, policy.Name)
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
	allConfigs := c.getMergedConfigsLocked()

	return c.rateLimitRunner.UpdateConfigs(ctx, allConfigs)
}

// deleteQuotaPolicyConfig removes a QuotaPolicy's configs from the cache
// and updates the xDS snapshot.
func (c *QuotaPolicyController) deleteQuotaPolicyConfig(ctx context.Context, key client.ObjectKey) error {
	cacheKey := fmt.Sprintf("%s/%s", key.Namespace, key.Name)
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.configCache, cacheKey)
	allConfigs := c.getMergedConfigsLocked()

	return c.rateLimitRunner.UpdateConfigs(ctx, allConfigs)
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
	key := fmt.Sprintf("%s.%s", obj.GetName(), obj.GetNamespace())
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
		key := fmt.Sprintf("%s.%s", ref.Name, policy.Namespace)
		var aiGatewayRoutes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &aiGatewayRoutes,
			client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: key}); err != nil {
			c.logger.Error(err, "failed to list AIGatewayRoutes for backend", "backend", key)
			continue
		}
		for i := range aiGatewayRoutes.Items {
			route := &aiGatewayRoutes.Items[i]
			c.logger.Info("notifying AIGatewayRoute of QuotaPolicy change",
				"route", route.Name, "namespace", route.Namespace,
				"quotaPolicy", policy.Name)
			if err := enqueueGenericEvent(c.aiGatewayRouteChan, route); err != nil {
				c.logger.Error(err, "failed to notify AIGatewayRoute of QuotaPolicy change",
					"route", route.Name, "namespace", route.Namespace)
			}
		}
	}
}

// notifyAllAIGatewayRoutesInNamespace sends events for all AIGatewayRoutes in
// the given namespace. Used on QuotaPolicy deletion when targetRefs are no
// longer available.
func (c *QuotaPolicyController) notifyAllAIGatewayRoutesInNamespace(ctx context.Context, namespace string) {
	var aiGatewayRoutes aigv1b1.AIGatewayRouteList
	if err := c.client.List(ctx, &aiGatewayRoutes, client.InNamespace(namespace)); err != nil {
		c.logger.Error(err, "failed to list AIGatewayRoutes in namespace", "namespace", namespace)
		return
	}
	for i := range aiGatewayRoutes.Items {
		route := &aiGatewayRoutes.Items[i]
		c.logger.Info("notifying AIGatewayRoute of QuotaPolicy deletion",
			"route", route.Name, "namespace", route.Namespace)
		if err := enqueueGenericEvent(c.aiGatewayRouteChan, route); err != nil {
			c.logger.Error(err, "failed to notify AIGatewayRoute of QuotaPolicy deletion",
				"route", route.Name, "namespace", route.Namespace)
		}
	}
}

// updateQuotaPolicyStatus updates the status of the QuotaPolicy.
func (c *QuotaPolicyController) updateQuotaPolicyStatus(ctx context.Context, policy *aigv1a1.QuotaPolicy, conditionType string, message string) {
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
		c.logger.Error(err, "failed to update QuotaPolicy status",
			"namespace", policy.Namespace, "name", policy.Name)
	}
}
