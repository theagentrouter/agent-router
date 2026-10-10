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

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/quotapolicy"
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
	quotaPolicyChan           chan event.GenericEvent
}

// NewReferenceGrantController creates a new [reconcile.TypedReconciler] for ReferenceGrant.
func NewReferenceGrantController(
	c client.Client,
	logger logr.Logger,
	aiGatewayRouteChan chan event.GenericEvent,
	backendSecurityPolicyChan chan event.GenericEvent,
	quotaPolicyChan chan event.GenericEvent,
) *ReferenceGrantController {
	return &ReferenceGrantController{
		client:                    c,
		logger:                    logger,
		aiGatewayRouteChan:        aiGatewayRouteChan,
		backendSecurityPolicyChan: backendSecurityPolicyChan,
		quotaPolicyChan:           quotaPolicyChan,
	}
}

// Reconcile implements the [reconcile.TypedReconciler] for ReferenceGrant.
func (c *ReferenceGrantController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.logger.Info("Reconciling ReferenceGrant", "namespace", req.Namespace, "name", req.Name)

	var referenceGrant gwapiv1b1.ReferenceGrant
	if err := c.client.Get(ctx, req.NamespacedName, &referenceGrant); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// The ReferenceGrant is already gone, e.g. it was removed before the finalizer below
			// could be attached. Its spec is no longer available, but everything it could have
			// authorized references its namespace, so reconcile all of those.
			return ctrl.Result{}, c.triggerAffectedReconciles(ctx, req.Namespace)
		}
		return ctrl.Result{}, err
	}

	// A finalizer keeps the ReferenceGrant retrievable (with DeletionTimestamp set) until
	// triggerAffectedReconciles has run, so a deleted grant still wakes up everything it used to
	// authorize instead of leaving them with stale, now-invalid access.
	if handleFinalizer(ctx, c.client, c.logger, &referenceGrant, func(ctx context.Context, rg *gwapiv1b1.ReferenceGrant) error {
		return c.triggerAffectedReconciles(ctx, rg.Namespace)
	}) {
		return ctrl.Result{}, nil
	}

	if err := c.triggerAffectedReconciles(ctx, referenceGrant.Namespace); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// triggerAffectedReconciles triggers reconciliation of every AIGatewayRoute, BackendSecurityPolicy and QuotaPolicy
// that a ReferenceGrant in grantNamespace may affect, whether it was just created, updated, or is about to be deleted.
//
// The set of affected resources is derived from the grant's namespace rather than its current spec: when a grant is narrowed
// (e.g. a "from" entry is removed), the resources that just lost access are no longer described by the new spec, but they still
// need to be reconciled to drop that access.
func (c *ReferenceGrantController) triggerAffectedReconciles(ctx context.Context, grantNamespace string) error {
	// Get all AIGatewayRoutes that might be affected by this ReferenceGrant
	affectedRoutes, err := c.getAffectedAIGatewayRoutes(ctx, grantNamespace)
	if err != nil {
		c.logger.Error(err, "failed to get affected AIGatewayRoutes", "grant_namespace", grantNamespace)
		return err
	}

	// Trigger reconciliation for each affected AIGatewayRoute
	for _, route := range affectedRoutes {
		c.logger.Info("Triggering reconciliation for affected AIGatewayRoute",
			"route_namespace", route.Namespace, "route_name", route.Name, "grant_namespace", grantNamespace)
		c.aiGatewayRouteChan <- event.GenericEvent{Object: route}
	}

	// Get all BackendSecurityPolicies that might be affected by this ReferenceGrant
	affectedBackendSecurityPolicies, err := c.getAffectedBackendSecurityPolicies(ctx, grantNamespace)
	if err != nil {
		c.logger.Error(err, "failed to get affected BackendSecurityPolicies", "grant_namespace", grantNamespace)
		return err
	}

	// Trigger reconciliation for each affected BackendSecurityPolicy
	for _, bsp := range affectedBackendSecurityPolicies {
		c.logger.Info("Triggering reconciliation for affected BackendSecurityPolicy",
			"backendsecuritypolicy_namespace", bsp.Namespace, "backendsecuritypolicy_name", bsp.Name,
			"grant_namespace", grantNamespace)
		c.backendSecurityPolicyChan <- event.GenericEvent{Object: bsp}
	}

	if c.quotaPolicyChan != nil {
		affectedQuotaPolicies, err := c.getAffectedQuotaPolicies(ctx, grantNamespace)
		if err != nil {
			c.logger.Error(err, "failed to get affected QuotaPolicies")
			return err
		}
		for _, policy := range affectedQuotaPolicies {
			c.quotaPolicyChan <- event.GenericEvent{Object: policy}
		}
	}

	return nil
}

// getAffectedQuotaPolicies returns QuotaPolicies with a cross-namespace
// AIServiceBackend target in grantNamespace. The namespace index intentionally
// returns a superset so grant narrowing and broadening reconcile policies that
// were affected by the previous grant state as well as the current state.
func (c *ReferenceGrantController) getAffectedQuotaPolicies(
	ctx context.Context,
	grantNamespace string,
) ([]*aigv1a1.QuotaPolicy, error) {
	var policies aigv1a1.QuotaPolicyList
	if err := c.client.List(ctx, &policies,
		client.MatchingFields{k8sClientIndexQuotaPolicyTargetNamespace: grantNamespace}); err != nil {
		return nil, fmt.Errorf("failed to list QuotaPolicies: %w", err)
	}

	affected := make([]*aigv1a1.QuotaPolicy, 0, len(policies.Items))
	for i := range policies.Items {
		policy := &policies.Items[i]
		if policy.Namespace == grantNamespace {
			continue
		}
		for _, ref := range policy.Spec.TargetRefs {
			if quotapolicy.TargetNamespace(ref, policy.Namespace) != grantNamespace {
				continue
			}
			if (ref.Group == "" || ref.Group == aiServiceBackendGroup) &&
				(ref.Kind == "" || ref.Kind == aiServiceBackendKind) {
				affected = append(affected, policy)
				break
			}
		}
	}
	return affected, nil
}

// getAffectedAIGatewayRoutes returns all AIGatewayRoutes in a namespace other than grantNamespace that
// reference a backend in grantNamespace, i.e. every route whose access a ReferenceGrant in
// grantNamespace may grant or revoke.
func (c *ReferenceGrantController) getAffectedAIGatewayRoutes(ctx context.Context, grantNamespace string) ([]*aigv1b1.AIGatewayRoute, error) {
	var routes aigv1b1.AIGatewayRouteList
	if err := c.client.List(ctx, &routes); err != nil {
		return nil, fmt.Errorf("failed to list AIGatewayRoutes: %w", err)
	}

	var affectedRoutes []*aigv1b1.AIGatewayRoute
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.Namespace != grantNamespace && c.routeReferencesNamespace(route, grantNamespace) {
			affectedRoutes = append(affectedRoutes, route)
		}
	}
	return affectedRoutes, nil
}

// routeReferencesNamespace checks if an AIGatewayRoute has any backend references (AIServiceBackend or
// InferencePool) to a specific namespace.
func (c *ReferenceGrantController) routeReferencesNamespace(route *aigv1b1.AIGatewayRoute, namespace string) bool {
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if backendRef.GetNamespace(route.Namespace) == namespace {
				return true
			}
		}
	}
	return false
}

// getAffectedBackendSecurityPolicies returns all BackendSecurityPolicies in a namespace other than
// grantNamespace that reference a Secret in grantNamespace, i.e. every policy whose access a
// ReferenceGrant in grantNamespace may grant or revoke.
func (c *ReferenceGrantController) getAffectedBackendSecurityPolicies(ctx context.Context, grantNamespace string) ([]*aigv1b1.BackendSecurityPolicy, error) {
	var policies aigv1b1.BackendSecurityPolicyList
	if err := c.client.List(ctx, &policies); err != nil {
		return nil, fmt.Errorf("failed to list BackendSecurityPolicies: %w", err)
	}

	var affectedBackendSecurityPolicies []*aigv1b1.BackendSecurityPolicy
	for i := range policies.Items {
		bsp := &policies.Items[i]
		if bsp.Namespace != grantNamespace && c.backendSecurityPolicyReferencesNamespace(bsp, grantNamespace) {
			affectedBackendSecurityPolicies = append(affectedBackendSecurityPolicies, bsp)
		}
	}
	return affectedBackendSecurityPolicies, nil
}

// backendSecurityPolicyReferencesNamespace checks if a BackendSecurityPolicy has a Secret reference to a
// specific namespace.
func (c *ReferenceGrantController) backendSecurityPolicyReferencesNamespace(bsp *aigv1b1.BackendSecurityPolicy, namespace string) bool {
	if _, secretNamespace, ok := backendSecurityPolicySecretRef(bsp); ok && secretNamespace == namespace {
		return true
	}
	if oidc := getBackendSecurityPolicyAuthOIDC(&bsp.Spec); oidc != nil && oidc.ClientSecret.Namespace != nil {
		return string(*oidc.ClientSecret.Namespace) == namespace
	}
	return false
}
