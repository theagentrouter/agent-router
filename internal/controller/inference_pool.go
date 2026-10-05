// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// InferencePoolController implements [reconcile.TypedReconciler] for [gwaiev1.InferencePool].
//
// This handles the InferencePool resource and updates its status based on associated Gateways.
//
// Exported for testing purposes.
type InferencePoolController struct {
	client                 client.Client
	kube                   kubernetes.Interface
	logger                 logr.Logger
	inferencePoolEventChan chan event.GenericEvent
}

// errEndpointPickerRefMissing is returned by validateExtensionReference when the InferencePool's
// spec.endpointPickerRef is unset. This is a legal, schema-valid state (the field is optional as of
// Gateway API Inference Extension v1.5.0), but Envoy AI Gateway currently requires an endpoint picker
// to route traffic, so it is reported via the upstream-defined
// gwaiev1.InferencePoolReasonEndpointPickerRefMissing reason rather than a generic failure.
var errEndpointPickerRefMissing = errors.New("endpointPickerRef is not set")

// NewInferencePoolController creates a new reconcile.TypedReconciler for gwaiev1.InferencePool.
func NewInferencePoolController(
	client client.Client, kube kubernetes.Interface, logger logr.Logger,
	inferencePoolEventChan chan event.GenericEvent,
) *InferencePoolController {
	return &InferencePoolController{
		client:                 client,
		kube:                   kube,
		logger:                 logger,
		inferencePoolEventChan: inferencePoolEventChan,
	}
}

// Reconcile implements the [reconcile.TypedReconciler] for [gwaiev1.InferencePool].
func (c *InferencePoolController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var inferencePool gwaiev1.InferencePool
	if err := c.client.Get(ctx, req.NamespacedName, &inferencePool); err != nil {
		if client.IgnoreNotFound(err) == nil {
			c.logger.Info("Deleting InferencePool",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	c.logger.Info("Reconciling InferencePool", "namespace", req.Namespace, "name", req.Name)
	if err := c.syncInferencePool(ctx, &inferencePool); err != nil {
		c.logger.Error(err, "failed to sync InferencePool")
		reason := "NotAccepted"
		if errors.Is(err, errEndpointPickerRefMissing) {
			reason = string(gwaiev1.InferencePoolReasonEndpointPickerRefMissing)
		}
		c.updateInferencePoolStatus(ctx, &inferencePool, reason, err.Error())
		return ctrl.Result{}, err
	}
	c.updateInferencePoolStatus(ctx, &inferencePool, "Accepted", "InferencePool reconciled successfully")
	return ctrl.Result{}, nil
}

// syncInferencePool is the main logic for reconciling the InferencePool resource.
// This is decoupled from the Reconcile method to centralize the error handling and status updates.
func (c *InferencePoolController) syncInferencePool(ctx context.Context, inferencePool *gwaiev1.InferencePool) error {
	// Check if the ExtensionReference service exists.
	if err := c.validateExtensionReference(ctx, inferencePool); err != nil {
		return err
	}

	referencedGateways, err := c.getReferencedGateways(ctx, inferencePool)
	if err != nil {
		return err
	}

	c.logger.Info("Found referenced Gateways", "count", len(referencedGateways), "inferencePool", inferencePool.Name)
	return nil
}

// routeReferencesInferencePool checks if an AIGatewayRoute references the given InferencePool.
func (c *InferencePoolController) routeReferencesInferencePool(route *aigv1b1.AIGatewayRoute, inferencePoolName string) bool {
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if backendRef.IsInferencePool() && backendRef.Name == inferencePoolName {
				return true
			}
		}
	}
	return false
}

// getReferencedGateways returns all Gateways that reference the given InferencePool.
func (c *InferencePoolController) getReferencedGateways(ctx context.Context, inferencePool *gwaiev1.InferencePool) (map[string]*gwapiv1.Gateway, error) {
	// Find all Gateways across all namespaces.
	var gateways gwapiv1.GatewayList
	if err := c.client.List(ctx, &gateways); err != nil {
		return nil, fmt.Errorf("failed to list Gateways: %w", err)
	}

	referencedGateways := make(map[string]*gwapiv1.Gateway)

	// Check each Gateway to see if it references this InferencePool through routes.
	for i := range gateways.Items {
		gw := &gateways.Items[i]
		if c.gatewayReferencesInferencePool(ctx, gw, inferencePool.Name, inferencePool.Namespace) {
			gatewayKey := fmt.Sprintf("%s/%s", gw.Namespace, gw.Name)
			referencedGateways[gatewayKey] = gw
		}
	}

	return referencedGateways, nil
}

// validateExtensionReference checks if the ExtensionReference service exists.
func (c *InferencePoolController) validateExtensionReference(ctx context.Context, inferencePool *gwaiev1.InferencePool) error {
	if inferencePool.Spec.EndpointPickerRef == nil {
		return errEndpointPickerRefMissing
	}

	// Get the service name from ExtensionReference.
	serviceName := inferencePool.Spec.EndpointPickerRef.Name
	if serviceName == "" {
		return fmt.Errorf("ExtensionReference name is empty")
	}

	// Check if the service exists.
	var service corev1.Service
	if err := c.client.Get(ctx, client.ObjectKey{
		Name:      string(serviceName),
		Namespace: inferencePool.Namespace,
	}, &service); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Service not found - this is the error case we want to handle.
			return fmt.Errorf("ExtensionReference service %s not found in namespace %s", serviceName, inferencePool.Namespace)
		}
		// Other error occurred.
		return fmt.Errorf("failed to get ExtensionReference service %s: %w", serviceName, err)
	}

	// Service exists - validation passed.
	return nil
}

// gatewayReferencesInferencePool checks if a Gateway references the given InferencePool through any routes.
func (c *InferencePoolController) gatewayReferencesInferencePool(ctx context.Context, gateway *gwapiv1.Gateway, inferencePoolName string, inferencePoolNamespace string) bool {
	// Check AIGatewayRoutes in the same namespace as the InferencePool that reference this Gateway.
	var aiGatewayRoutes aigv1b1.AIGatewayRouteList
	if err := c.client.List(ctx, &aiGatewayRoutes, client.InNamespace(inferencePoolNamespace)); err != nil {
		c.logger.Error(err, "failed to list AIGatewayRoutes", "gateway", gateway.Name, "namespace", inferencePoolNamespace)
		return false
	}

	for i := range aiGatewayRoutes.Items {
		route := &aiGatewayRoutes.Items[i]
		// Check if this route references the Gateway.
		if c.routeReferencesGateway(route.Spec.ParentRefs, gateway.Name, gateway.Namespace, route.Namespace) {
			// Check if this route references the InferencePool.
			if c.routeReferencesInferencePool(route, inferencePoolName) {
				return true
			}
		}
	}

	// Check HTTPRoutes in the same namespace as the InferencePool that reference this Gateway.
	var httpRoutes gwapiv1.HTTPRouteList
	if err := c.client.List(ctx, &httpRoutes, client.InNamespace(inferencePoolNamespace)); err != nil {
		c.logger.Error(err, "failed to list HTTPRoutes", "gateway", gateway.Name, "namespace", inferencePoolNamespace)
		return false
	}

	for i := range httpRoutes.Items {
		route := &httpRoutes.Items[i]
		// Check if this route references the Gateway.
		if c.routeReferencesGateway(route.Spec.ParentRefs, gateway.Name, gateway.Namespace, route.Namespace) {
			// Check if this route references the InferencePool.
			if c.httpRouteReferencesInferencePool(route, inferencePoolName) {
				return true
			}
		}
	}

	return false
}

// routeReferencesGateway checks if a route references the given Gateway.
func (c *InferencePoolController) routeReferencesGateway(parentRefs []gwapiv1.ParentReference, gatewayName string, gatewayNamespace string, routeNamespace string) bool {
	for _, parentRef := range parentRefs {
		// Check if the name matches.
		if string(parentRef.Name) != gatewayName {
			continue
		}

		// Check namespace - if not specified in parentRef, it defaults to the route's namespace.
		if parentRef.Namespace != nil {
			if string(*parentRef.Namespace) == gatewayNamespace {
				return true
			}
		} else {
			// If namespace is not specified, it means same namespace as the route.
			// Check if the route's namespace matches the gateway's namespace.
			if routeNamespace == gatewayNamespace {
				return true
			}
		}
	}
	return false
}

// httpRouteReferencesInferencePool checks if an HTTPRoute references the given InferencePool.
func (c *InferencePoolController) httpRouteReferencesInferencePool(route *gwapiv1.HTTPRoute, inferencePoolName string) bool {
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if backendRef.Group != nil && string(*backendRef.Group) == "inference.networking.k8s.io" &&
				backendRef.Kind != nil && string(*backendRef.Kind) == "InferencePool" &&
				string(backendRef.Name) == inferencePoolName {
				return true
			}
		}
	}
	return false
}

// updateInferencePoolStatus updates the status of the InferencePool.
func (c *InferencePoolController) updateInferencePoolStatus(ctx context.Context, inferencePool *gwaiev1.InferencePool, conditionType string, message string) {
	// Check if this is an ExtensionReference validation error.
	isExtensionRefError := conditionType == "NotAccepted" &&
		(strings.Contains(message, "ExtensionReference service") && strings.Contains(message, "not found"))
	// Get the referenced Gateways from syncInferencePool logic.
	referencedGateways, err := c.getReferencedGateways(ctx, inferencePool)
	if err != nil {
		c.logger.Error(err, "failed to get referenced Gateways for status update")
		return
	}

	// Build Parents status.
	var parents []gwaiev1.ParentStatus
	for _, gw := range referencedGateways {
		// Set Gateway group and kind according to Gateway API defaults.
		gatewayGroup := "gateway.networking.k8s.io"
		gatewayKind := "Gateway"

		parentRef := gwaiev1.ParentReference{
			Group:     (*gwaiev1.Group)(&gatewayGroup),
			Kind:      gwaiev1.Kind(gatewayKind),
			Name:      gwaiev1.ObjectName(gw.Name),
			Namespace: gwaiev1.Namespace(gw.Namespace),
		}

		var conditions []metav1.Condition

		// Add the main condition (Accepted/NotAccepted).
		condition := buildAcceptedCondition(inferencePool.Generation, "ai-gateway-controller", conditionType, message)
		conditions = append(conditions, condition)

		// Add ResolvedRefs condition based on validation results.
		if isExtensionRefError {
			resolvedRefsCondition := buildResolvedRefsCondition(inferencePool.Generation, "ai-gateway-controller", false, "ResolvedRefs", message)
			conditions = append(conditions, resolvedRefsCondition)
		} else {
			// Add successful ResolvedRefs condition.
			resolvedRefsCondition := buildResolvedRefsCondition(inferencePool.Generation, "ai-gateway-controller", true, "ResolvedRefs", "All references resolved successfully")
			conditions = append(conditions, resolvedRefsCondition)
		}

		parents = append(parents, gwaiev1.ParentStatus{
			ParentRef:  parentRef,
			Conditions: conditions,
		})
	}

	// If no Gateways reference this InferencePool, clear all parents.
	// This correctly reflects that the InferencePool is not currently referenced by any Gateway.

	inferencePool.Status.Parents = parents
	if err := c.client.Status().Update(ctx, inferencePool); err != nil {
		c.logger.Error(err, "failed to update InferencePool status")
	}
}

// buildAcceptedCondition builds a condition for the InferencePool status.
func buildAcceptedCondition(gen int64, controllerName string, conditionType string, message string) metav1.Condition {
	status := metav1.ConditionTrue
	reason := "Accepted"
	switch conditionType {
	case "NotAccepted", string(gwaiev1.InferencePoolReasonEndpointPickerRefMissing):
		status = metav1.ConditionFalse
		reason = conditionType
		conditionType = string(gwaiev1.InferencePoolConditionAccepted)
	}

	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            fmt.Sprintf("InferencePool has been %s by controller %s: %s", reason, controllerName, message),
		ObservedGeneration: gen,
		LastTransitionTime: metav1.Now(),
	}
}

// gatewayEventHandler returns an event handler for Gateway resources.
func (c *InferencePoolController) gatewayEventHandler(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gwapiv1.Gateway)
	if !ok {
		return nil
	}

	// Find all InferencePools in the same namespace that might be affected by this Gateway.
	var inferencePools gwaiev1.InferencePoolList
	if err := c.client.List(ctx, &inferencePools, client.InNamespace(gateway.Namespace)); err != nil {
		c.logger.Error(err, "failed to list InferencePools for Gateway event", "gateway", gateway.Name)
		return nil
	}

	var requests []reconcile.Request
	for i := range inferencePools.Items {
		pool := &inferencePools.Items[i]
		// Check if this Gateway references the InferencePool.
		if c.gatewayReferencesInferencePool(ctx, gateway, pool.Name, pool.Namespace) {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKey{
					Name:      pool.Name,
					Namespace: pool.Namespace,
				},
			})
		}
	}

	return requests
}

// aiGatewayRouteEventHandler returns an event handler for AIGatewayRoute resources.
func (c *InferencePoolController) aiGatewayRouteEventHandler(_ context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*aigv1b1.AIGatewayRoute)
	if !ok {
		return nil
	}

	// Find all InferencePools referenced by this AIGatewayRoute.
	var requests []reconcile.Request
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if backendRef.IsInferencePool() {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKey{
						Name:      backendRef.Name,
						Namespace: route.Namespace,
					},
				})
			}
		}
	}

	return requests
}

// httpRouteEventHandler returns an event handler for HTTPRoute resources.
func (c *InferencePoolController) httpRouteEventHandler(_ context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*gwapiv1.HTTPRoute)
	if !ok {
		return nil
	}

	// Find all InferencePools referenced by this HTTPRoute.
	var requests []reconcile.Request
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if backendRef.Group != nil && string(*backendRef.Group) == "inference.networking.k8s.io" &&
				backendRef.Kind != nil && string(*backendRef.Kind) == "InferencePool" {
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKey{
						Name:      string(backendRef.Name),
						Namespace: route.Namespace,
					},
				})
			}
		}
	}

	return requests
}

// buildResolvedRefsCondition builds a ResolvedRefs condition for the InferencePool status.
func buildResolvedRefsCondition(gen int64, controllerName string, resolved bool, reason string, message string) metav1.Condition {
	status := metav1.ConditionTrue
	if !resolved {
		status = metav1.ConditionFalse
	}

	return metav1.Condition{
		Type:               "ResolvedRefs",
		Status:             status,
		Reason:             reason,
		Message:            fmt.Sprintf("Reference resolution by controller %s: %s", controllerName, message),
		ObservedGeneration: gen,
		LastTransitionTime: metav1.Now(),
	}
}
