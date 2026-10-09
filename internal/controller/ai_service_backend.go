// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// AIBackendController implements [reconcile.TypedReconciler] for [aigv1b1.AIServiceBackend].
//
// Exported for testing purposes.
type AIBackendController struct {
	client             client.Client
	kube               kubernetes.Interface
	logger             logr.Logger
	aiGatewayRouteChan chan event.GenericEvent
}

// NewAIServiceBackendController creates a new [reconcile.TypedReconciler] for [aigv1b1.AIServiceBackend].
func NewAIServiceBackendController(client client.Client, kube kubernetes.Interface, logger logr.Logger, aiGatewayRouteChan chan event.GenericEvent) *AIBackendController {
	return &AIBackendController{
		client:             client,
		kube:               kube,
		logger:             logger,
		aiGatewayRouteChan: aiGatewayRouteChan,
	}
}

// Reconcile implements the [reconcile.TypedReconciler] for [aigv1b1.AIServiceBackend].
func (c *AIBackendController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var aiBackend aigv1b1.AIServiceBackend
	if err := c.client.Get(ctx, req.NamespacedName, &aiBackend); err != nil {
		if client.IgnoreNotFound(err) == nil {
			c.logger.Info("Deleting AIServiceBackend",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	c.logger.Info("Reconciling AIServiceBackend", "namespace", req.Namespace, "name", req.Name)
	if err := c.syncAIServiceBackend(ctx, &aiBackend); err != nil {
		c.logger.Error(err, "failed to sync AIServiceBackend")
		c.updateAIServiceBackendStatus(ctx, &aiBackend, aigv1b1.ConditionTypeNotAccepted, err.Error())
		return ctrl.Result{}, err
	}
	c.updateAIServiceBackendStatus(ctx, &aiBackend, aigv1b1.ConditionTypeAccepted, "AIServiceBackend reconciled successfully")
	return ctrl.Result{}, nil
}

// syncAIServiceBackend is the main logic for reconciling the AIServiceBackend resource.
// This is decoupled from the Reconcile method to centralize the error handling and status updates.
func (c *AIBackendController) syncAIServiceBackend(ctx context.Context, aiBackend *aigv1b1.AIServiceBackend) error {
	if err := c.validateBackendRef(ctx, aiBackend); err != nil {
		return err
	}

	var backendSecurityPolicyList aigv1b1.BackendSecurityPolicyList
	key := fmt.Sprintf("%s.%s", aiBackend.Name, aiBackend.Namespace)
	if err := c.client.List(ctx, &backendSecurityPolicyList, client.InNamespace(aiBackend.Namespace),
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingBackendSecurityPolicy: key}); err != nil {
		return fmt.Errorf("failed to list BackendSecurityPolicyList: %w", err)
	}
	if len(backendSecurityPolicyList.Items) > 1 {
		var names []string
		for i := range backendSecurityPolicyList.Items {
			bsp := &backendSecurityPolicyList.Items[i]
			names = append(names, bsp.Name)
		}
		return fmt.Errorf("multiple BackendSecurityPolicies found for AIServiceBackend %s: %v",
			aiBackend.Name, names)
	}

	// Propagate the bsp events all the way up to relevant Gateways regardless of being deleted or not.
	_ = handleFinalizer(ctx, c.client, c.logger, aiBackend, nil)
	// Notify the AI Gateway Route controller about the AIServiceBackend change.
	var aiGatewayRoutes aigv1b1.AIGatewayRouteList
	err := c.client.List(ctx, &aiGatewayRoutes, client.MatchingFields{k8sClientIndexBackendToReferencingAIGatewayRoute: key})
	if err != nil {
		return fmt.Errorf("failed to list AIGatewayRouteList: %w", err)
	}
	for i := range aiGatewayRoutes.Items {
		aiGatewayRoute := &aiGatewayRoutes.Items[i]
		c.logger.Info("syncing AIGatewayRoute",
			"namespace", aiGatewayRoute.Namespace, "name", aiGatewayRoute.Name,
			"referenced_backend", aiBackend.Name, "referenced_backend_namespace", aiBackend.Namespace,
		)
		c.aiGatewayRouteChan <- event.GenericEvent{Object: aiGatewayRoute}
	}
	return nil
}

// validateBackendRef verifies that the referenced backend resource exists.
// For InferencePool references it checks that the pool is present in the cluster so that
// a clear status condition is surfaced when it is missing.
func (c *AIBackendController) validateBackendRef(ctx context.Context, aiBackend *aigv1b1.AIServiceBackend) error {
	br := aiBackend.Spec.BackendRef
	if br.Kind == nil || string(*br.Kind) != inferencePoolKind ||
		br.Group == nil || string(*br.Group) != inferencePoolGroup {
		return nil
	}
	ns := aiBackend.Namespace
	if br.Namespace != nil {
		ns = string(*br.Namespace)
	}
	var pool gwaiev1.InferencePool
	if err := c.client.Get(ctx, client.ObjectKey{Name: string(br.Name), Namespace: ns}, &pool); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return fmt.Errorf("referenced InferencePool %q not found in namespace %q", br.Name, ns)
		}
		return fmt.Errorf("failed to get referenced InferencePool %q: %w", br.Name, err)
	}
	return nil
}

// inferencePoolEventHandler maps InferencePool events to reconcile requests for any
// AIServiceBackend that references the changed pool.
func (c *AIBackendController) inferencePoolEventHandler(ctx context.Context, obj client.Object) []reconcile.Request {
	pool, ok := obj.(*gwaiev1.InferencePool)
	if !ok {
		return nil
	}
	var backends aigv1b1.AIServiceBackendList
	if err := c.client.List(ctx, &backends); err != nil {
		c.logger.Error(err, "failed to list AIServiceBackends for InferencePool event", "pool", pool.Name)
		return nil
	}
	var requests []reconcile.Request
	for i := range backends.Items {
		backend := &backends.Items[i]
		br := backend.Spec.BackendRef
		if br.Kind != nil && string(*br.Kind) == inferencePoolKind &&
			br.Group != nil && string(*br.Group) == inferencePoolGroup &&
			string(br.Name) == pool.Name {
			ns := backend.Namespace
			if br.Namespace != nil {
				ns = string(*br.Namespace)
			}
			if ns == pool.Namespace {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKey{Name: backend.Name, Namespace: backend.Namespace},
				})
			}
		}
	}
	return requests
}

// updateAIServiceBackendStatus updates the status of the AIServiceBackend.
func (c *AIBackendController) updateAIServiceBackendStatus(ctx context.Context, backend *aigv1b1.AIServiceBackend, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: backend.Name, Namespace: backend.Namespace}, backend); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		backend.Status.Conditions = newConditions(conditionType, message)
		return c.client.Status().Update(ctx, backend)
	})
	if err != nil {
		c.logger.Error(err, "failed to update AIServiceBackend status",
			"namespace", backend.Namespace, "name", backend.Name)
	}
}
