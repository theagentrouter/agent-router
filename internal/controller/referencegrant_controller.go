// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// ReferenceGrantController implements [reconcile.TypedReconciler] for ReferenceGrant.
//
// This controller watches ReferenceGrant resources and triggers reconciliation of
// affected AIGatewayRoutes and BackendSecurityPolicies when grants are created, updated, or deleted.
//
// Exported for testing purposes.
type ReferenceGrantController struct {
	client                    client.Client
	logger                    logr.Logger
	aiGatewayRouteChan        chan event.GenericEvent
	backendSecurityPolicyChan chan event.GenericEvent
}

// NewReferenceGrantController creates a new [reconcile.TypedReconciler] for ReferenceGrant.
func NewReferenceGrantController(
	c client.Client,
	logger logr.Logger,
	aiGatewayRouteChan chan event.GenericEvent,
	backendSecurityPolicyChan chan event.GenericEvent,
) *ReferenceGrantController {
	return &ReferenceGrantController{
		client:                    c,
		logger:                    logger,
		aiGatewayRouteChan:        aiGatewayRouteChan,
		backendSecurityPolicyChan: backendSecurityPolicyChan,
	}
}

// Reconcile implements the [reconcile.TypedReconciler] for ReferenceGrant.
func (c *ReferenceGrantController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.logger.Info("Reconciling ReferenceGrant", "namespace", req.Namespace, "name", req.Name)

	var referenceGrant gwapiv1b1.ReferenceGrant
	if err := c.client.Get(ctx, req.NamespacedName, &referenceGrant); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// The ReferenceGrant is already gone, e.g. it was removed before the finalizer below
			// could be attached. There's no spec left to determine what it used to authorize.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// A finalizer keeps the ReferenceGrant retrievable (with DeletionTimestamp set) until
	// triggerAffectedReconciles has run, so a deleted grant still wakes up everything it used to
	// authorize instead of leaving them with stale, now-invalid access.
	if handleFinalizer(ctx, c.client, c.logger, &referenceGrant, c.triggerAffectedReconciles) {
		return ctrl.Result{}, nil
	}

	if err := c.triggerAffectedReconciles(ctx, &referenceGrant); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// triggerAffectedReconciles triggers reconciliation of every AIGatewayRoute and BackendSecurityPolicy
// that referenceGrant affects, whether it was just created, updated, or is about to be deleted.
func (c *ReferenceGrantController) triggerAffectedReconciles(ctx context.Context, referenceGrant *gwapiv1b1.ReferenceGrant) error {
	// Get all AIGatewayRoutes that might be affected by this ReferenceGrant
	affectedRoutes, err := c.getAffectedAIGatewayRoutes(ctx, referenceGrant)
	if err != nil {
		c.logger.Error(err, "failed to get affected AIGatewayRoutes",
			"namespace", referenceGrant.Namespace, "name", referenceGrant.Name)
		return err
	}

	// Trigger reconciliation for each affected AIGatewayRoute
	for _, route := range affectedRoutes {
		c.logger.Info("Triggering reconciliation for affected AIGatewayRoute",
			"route_namespace", route.Namespace, "route_name", route.Name,
			"grant_namespace", referenceGrant.Namespace, "grant_name", referenceGrant.Name)
		c.aiGatewayRouteChan <- event.GenericEvent{Object: route}
	}

	// Get all BackendSecurityPolicies that might be affected by this ReferenceGrant
	affectedBackendSecurityPolicies, err := c.getAffectedBackendSecurityPolicies(ctx, referenceGrant)
	if err != nil {
		c.logger.Error(err, "failed to get affected BackendSecurityPolicies",
			"namespace", referenceGrant.Namespace, "name", referenceGrant.Name)
		return err
	}

	// Trigger reconciliation for each affected BackendSecurityPolicy
	for _, bsp := range affectedBackendSecurityPolicies {
		c.logger.Info("Triggering reconciliation for affected BackendSecurityPolicy",
			"backendsecuritypolicy_namespace", bsp.Namespace, "backendsecuritypolicy_name", bsp.Name,
			"grant_namespace", referenceGrant.Namespace, "grant_name", referenceGrant.Name)
		c.backendSecurityPolicyChan <- event.GenericEvent{Object: bsp}
	}

	return nil
}

// getAffectedAIGatewayRoutes returns all AIGatewayRoutes that might be affected by a ReferenceGrant change.
// This is used to trigger reconciliation when a ReferenceGrant is created, updated, or deleted.
func (c *ReferenceGrantController) getAffectedAIGatewayRoutes(
	ctx context.Context,
	grant *gwapiv1b1.ReferenceGrant,
) ([]*aigv1b1.AIGatewayRoute, error) {
	var affectedRoutes []*aigv1b1.AIGatewayRoute

	// For each "from" reference in the grant, find AIGatewayRoutes in that namespace
	// that might reference AIServiceBackends in the grant's namespace
	for _, from := range grant.Spec.From {
		if from.Group != aiServiceBackendGroup || from.Kind != aiGatewayRouteKind {
			continue
		}

		var routes aigv1b1.AIGatewayRouteList
		if err := c.client.List(ctx, &routes, client.InNamespace(string(from.Namespace))); err != nil {
			return nil, fmt.Errorf("failed to list AIGatewayRoutes in namespace %s: %w", from.Namespace, err)
		}

		// Check if any of these routes reference backends in the grant's namespace
		for i := range routes.Items {
			route := &routes.Items[i]
			if c.routeReferencesNamespace(route, grant.Namespace) {
				affectedRoutes = append(affectedRoutes, route)
			}
		}
	}

	return affectedRoutes, nil
}

// routeReferencesNamespace checks if an AIGatewayRoute has any backend references to a specific namespace.
func (c *ReferenceGrantController) routeReferencesNamespace(route *aigv1b1.AIGatewayRoute, namespace string) bool {
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			// Only check AIServiceBackend references
			if backendRef.IsAIServiceBackend() {
				backendNs := backendRef.GetNamespace(route.Namespace)
				if backendNs == namespace {
					return true
				}
			}
		}
	}
	return false
}

// getAffectedBackendSecurityPolicies returns all BackendSecurityPolicies that might be affected by a
// ReferenceGrant change. This is used to trigger reconciliation when a ReferenceGrant is created, updated, or deleted.
func (c *ReferenceGrantController) getAffectedBackendSecurityPolicies(
	ctx context.Context,
	grant *gwapiv1b1.ReferenceGrant,
) ([]*aigv1b1.BackendSecurityPolicy, error) {
	var affectedBackendSecurityPolicies []*aigv1b1.BackendSecurityPolicy

	// For each "from" reference in the grant, find BackendSecurityPolicies in that namespace
	// that might reference Secrets in the grant's namespace
	for _, from := range grant.Spec.From {
		if from.Group != aiServiceBackendGroup || from.Kind != backendSecurityPolicyKind {
			continue
		}

		var policies aigv1b1.BackendSecurityPolicyList
		if err := c.client.List(ctx, &policies, client.InNamespace(string(from.Namespace))); err != nil {
			return nil, fmt.Errorf("failed to list BackendSecurityPolicies in namespace %s: %w", from.Namespace, err)
		}

		// Check if any of these policies reference secrets in the grant's namespace
		for i := range policies.Items {
			bsp := &policies.Items[i]
			if c.backendSecurityPolicyReferencesNamespace(bsp, grant.Namespace) {
				affectedBackendSecurityPolicies = append(affectedBackendSecurityPolicies, bsp)
			}
		}
	}

	return affectedBackendSecurityPolicies, nil
}

// backendSecurityPolicyReferencesNamespace checks if a BackendSecurityPolicy has a Secret reference to a
// specific namespace.
func (c *ReferenceGrantController) backendSecurityPolicyReferencesNamespace(bsp *aigv1b1.BackendSecurityPolicy, namespace string) bool {
	_, secretNamespace, ok := backendSecurityPolicySecretRef(bsp)
	return ok && secretNamespace == namespace
}
