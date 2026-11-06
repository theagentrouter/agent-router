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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// GatewayConfigController implements [reconcile.TypedReconciler] for [aigv1b1.GatewayConfig].
//
// This handles the GatewayConfig resource and notifies referencing Gateways of changes.
//
// Exported for testing purposes.
type GatewayConfigController struct {
	client client.Client
	logger logr.Logger
	// gatewayEventChan is a channel to send events to the gateway controller.
	gatewayEventChan chan event.GenericEvent
}

// NewGatewayConfigController creates a new reconcile.TypedReconciler[reconcile.Request] for the GatewayConfig resource.
func NewGatewayConfigController(
	client client.Client,
	logger logr.Logger,
	gatewayEventChan chan event.GenericEvent,
) *GatewayConfigController {
	return &GatewayConfigController{
		client:           client,
		logger:           logger,
		gatewayEventChan: gatewayEventChan,
	}
}

// Reconcile implements [reconcile.TypedReconciler] for [aigv1b1.GatewayConfig].
func (c *GatewayConfigController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.logger.Info("Reconciling GatewayConfig", "namespace", req.Namespace, "name", req.Name)

	var gatewayConfig aigv1b1.GatewayConfig
	if err := c.client.Get(ctx, req.NamespacedName, &gatewayConfig); err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("Deleting GatewayConfig", "namespace", req.Namespace, "name", req.Name)
			gatewayConfig.Name = req.Name
			gatewayConfig.Namespace = req.Namespace
			return ctrl.Result{}, c.syncGatewayConfig(ctx, &gatewayConfig)
		}
		return ctrl.Result{}, err
	}

	if err := c.syncGatewayConfig(ctx, &gatewayConfig); err != nil {
		c.logger.Error(err, "failed to sync GatewayConfig")
		c.updateGatewayConfigStatus(ctx, &gatewayConfig, aigv1b1.ConditionTypeNotAccepted, err.Error())
		return ctrl.Result{}, err
	}
	c.updateGatewayConfigStatus(ctx, &gatewayConfig, aigv1b1.ConditionTypeAccepted, "GatewayConfig reconciled successfully")
	return reconcile.Result{}, nil
}

// syncGatewayConfig is the main logic for reconciling the GatewayConfig resource.
func (c *GatewayConfigController) syncGatewayConfig(ctx context.Context, gatewayConfig *aigv1b1.GatewayConfig) error {
	// Find all Gateways that reference this GatewayConfig.
	referencingGateways, err := c.findReferencingGateways(ctx, gatewayConfig)
	if err != nil {
		return fmt.Errorf("failed to find referencing Gateways: %w", err)
	}

	// Notify all referencing Gateways to reconcile.
	c.notifyReferencingGateways(gatewayConfig, referencingGateways)

	return nil
}

// findReferencingGateways finds all Gateways in the same namespace that reference this GatewayConfig.
func (c *GatewayConfigController) findReferencingGateways(ctx context.Context, gatewayConfig *aigv1b1.GatewayConfig) ([]*gwapiv1.Gateway, error) {
	var gateways gwapiv1.GatewayList
	if err := c.client.List(
		ctx,
		&gateways,
		client.InNamespace(gatewayConfig.Namespace),
		client.MatchingFields{k8sClientIndexGatewayToGatewayConfig: gatewayConfig.Name},
	); err != nil {
		return nil, fmt.Errorf("failed to list Gateways: %w", err)
	}

	referencingGateways := make([]*gwapiv1.Gateway, 0, len(gateways.Items))
	for i := range gateways.Items {
		gw := &gateways.Items[i]
		referencingGateways = append(referencingGateways, gw)
	}

	return referencingGateways, nil
}

func (c *GatewayConfigController) notifyReferencingGateways(gatewayConfig *aigv1b1.GatewayConfig, referencingGateways []*gwapiv1.Gateway) {
	for _, gw := range referencingGateways {
		c.logger.Info("Notifying Gateway of GatewayConfig change",
			"gateway_namespace", gw.Namespace, "gateway_name", gw.Name,
			"gatewayconfig_name", gatewayConfig.Name)
		c.gatewayEventChan <- event.GenericEvent{Object: gw}
	}
}

// updateGatewayConfigStatus updates the status of the GatewayConfig.
func (c *GatewayConfigController) updateGatewayConfigStatus(ctx context.Context, gatewayConfig *aigv1b1.GatewayConfig, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: gatewayConfig.Name, Namespace: gatewayConfig.Namespace}, gatewayConfig); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		gatewayConfig.Status.Conditions = gatewayConfigConditions(conditionType, message)
		return c.client.Status().Update(ctx, gatewayConfig)
	})
	if err != nil {
		c.logger.Error(err, "failed to update GatewayConfig status")
	}
}

// gatewayConfigConditions creates new conditions for the GatewayConfig status.
func gatewayConfigConditions(conditionType string, message string) []metav1.Condition {
	status := metav1.ConditionTrue
	if conditionType == aigv1b1.ConditionTypeNotAccepted {
		status = metav1.ConditionFalse
	}

	return []metav1.Condition{
		{
			Type:               conditionType,
			Status:             status,
			Reason:             conditionType,
			Message:            message,
			LastTransitionTime: metav1.Now(),
		},
	}
}
