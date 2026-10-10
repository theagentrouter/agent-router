// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json" //nolint: depguard // byte-stable hashing; sonic does not guarantee stable field order.
	"errors"
	"fmt"
	"sort"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/quotapolicy"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	// managedByValue is the value stamped on the managedByLabel of resources created by this operator.
	managedByValue                      = "envoy-ai-gateway"
	hostRewriteHTTPFilterName           = "ai-eg-host-rewrite"
	routeNotFoundResponseHTTPFilterName = "ai-eg-route-not-found-response"
	aigatewayUUIDAnnotationKey          = "aigateway.envoyproxy.io/uuid"
	egAnnotationPrefix                  = "gateway.envoyproxy.io/"
	// We use this annotation to ensure that Envoy Gateway reconciles the HTTPRoute when the backend refs change.
	// This will result in metadata being added to the underling Envoy route
	// @see https://gateway.envoyproxy.io/contributions/design/metadata/
	httpRouteBackendRefPriorityAnnotationKey           = egAnnotationPrefix + "backend-ref-priority"
	httpRouteAnnotationForAIGatewayGeneratedIndication = egAnnotationPrefix + internalapi.AIGatewayGeneratedHTTPRouteAnnotation
	// httpRouteQuotaPolicyHashAnnotationKey carries a hash of the QuotaPolicies attached to the
	// AIServiceBackends referenced by this route. QuotaPolicy is an AI Gateway CRD that Envoy Gateway
	// does not watch, so a QuotaPolicy change would otherwise regenerate a byte-identical HTTPRoute
	// (a no-op update) and never force Envoy Gateway to re-translate. Stamping the hash makes the
	// HTTPRoute genuinely change on a QuotaPolicy update, which triggers Envoy Gateway to re-translate
	// and re-run the extension server's PostTranslateModify hook (which injects the quota rate limit
	// filter, cluster, and per-route descriptors). Mirrors stampGatewayConfigHash in gateway.go.
	httpRouteQuotaPolicyHashAnnotationKey = egAnnotationPrefix + "quota-policy-hash"
	egOwningGatewayNameLabel              = egAnnotationPrefix + "owning-gateway-name"
	egOwningGatewayNamespaceLabel         = egAnnotationPrefix + "owning-gateway-namespace"
	// apiKeyInSecret is the key to store OpenAI API key.
	apiKeyInSecret = "apiKey"
	// GatewayConfigAnnotationKey is the annotation key used on Gateway objects to reference a GatewayConfig.
	// The value should be the name of the GatewayConfig resource in the same namespace as the Gateway.
	GatewayConfigAnnotationKey = "aigateway.envoyproxy.io/gateway-config"
)

// AIGatewayRouteController implements [reconcile.TypedReconciler].
//
// This handles the AIGatewayRoute resource and creates the necessary resources for the external process.
//
// Exported for testing purposes.
type AIGatewayRouteController struct {
	client client.Client
	kube   kubernetes.Interface
	logger logr.Logger
	// gatewayEventChan is a channel to send events to the gateway controller.
	gatewayEventChan chan event.GenericEvent
	// rootPrefix is the prefix for the root path of the AI Gateway.
	rootPrefix string
	// referenceGrantValidator validates cross-namespace references using ReferenceGrant.
	referenceGrantValidator *referenceGrantValidator
}

// NewAIGatewayRouteController creates a new reconcile.TypedReconciler[reconcile.Request] for the AIGatewayRoute resource.
func NewAIGatewayRouteController(
	client client.Client, kube kubernetes.Interface, logger logr.Logger,
	gatewayEventChan chan event.GenericEvent,
	rootPrefix string,
) *AIGatewayRouteController {
	return &AIGatewayRouteController{
		client:                  client,
		kube:                    kube,
		logger:                  logger,
		gatewayEventChan:        gatewayEventChan,
		rootPrefix:              rootPrefix,
		referenceGrantValidator: newReferenceGrantValidator(client),
	}
}

// Reconcile implements [reconcile.TypedReconciler].
func (c *AIGatewayRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.logger.Info("Reconciling AIGatewayRoute", "namespace", req.Namespace, "name", req.Name)

	var aiGatewayRoute aigv1b1.AIGatewayRoute
	if err := c.client.Get(ctx, req.NamespacedName, &aiGatewayRoute); err != nil {
		if client.IgnoreNotFound(err) == nil {
			c.logger.Info("Deleting AIGatewayRoute",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if err := c.syncAIGatewayRoute(ctx, &aiGatewayRoute); err != nil {
		c.logger.Error(err, "failed to sync AIGatewayRoute")
		c.updateAIGatewayRouteStatus(ctx, &aiGatewayRoute, aigv1b1.ConditionTypeNotAccepted, err.Error())
		return ctrl.Result{}, err
	}
	c.updateAIGatewayRouteStatus(ctx, &aiGatewayRoute, aigv1b1.ConditionTypeAccepted, "AI Gateway Route reconciled successfully")
	return reconcile.Result{}, nil
}

func getHostRewriteFilterName(baseName string) string {
	return fmt.Sprintf("%s-%s", hostRewriteHTTPFilterName, baseName)
}

func getRouteNotFoundFilterName(baseName string) string {
	return fmt.Sprintf("%s-%s", routeNotFoundResponseHTTPFilterName, baseName)
}

// generateHTTPRouteFilters returns two HTTPRouteFilter with the given AIGatewayRoute.
func generateHTTPRouteFilters(aiGatewayRoute *aigv1b1.AIGatewayRoute) []*egv1a1.HTTPRouteFilter {
	ns := aiGatewayRoute.Namespace
	baseName := aiGatewayRoute.Name

	hostRewriteName := getHostRewriteFilterName(baseName)
	notFoundName := getRouteNotFoundFilterName(baseName)

	return []*egv1a1.HTTPRouteFilter{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostRewriteName,
				Namespace: ns,
			},
			Spec: egv1a1.HTTPRouteFilterSpec{
				URLRewrite: &egv1a1.HTTPURLRewriteFilter{
					Hostname: &egv1a1.HTTPHostnameModifier{
						Type: egv1a1.BackendHTTPHostnameModifier,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      notFoundName,
				Namespace: ns,
			},
			Spec: egv1a1.HTTPRouteFilterSpec{
				DirectResponse: &egv1a1.HTTPDirectResponseFilter{
					StatusCode: ptr.To(404),
					Body: &egv1a1.CustomResponseBody{
						Inline: ptr.To(
							// "Likely" since the matching rule can be arbitrary, not necessarily matching on the model name.
							`No matching route found. It is likely because the model specified in your request is not configured in the Gateway.`,
						),
					},
				},
			},
		},
	}
}

// syncAIGatewayRoute is the main logic for reconciling the AIGatewayRoute resource.
// This is decoupled from the Reconcile method to centralize the error handling and status updates.
func (c *AIGatewayRouteController) syncAIGatewayRoute(ctx context.Context, aiGatewayRoute *aigv1b1.AIGatewayRoute) error {
	if handleFinalizer(ctx, c.client, c.logger, aiGatewayRoute, c.syncGateways) { // Propagate the AIGatewayRoute deletion all the way up to relevant Gateways.
		return nil
	}

	// Check if the static default HTTPRouteFilters exist per AIGatewayRoute.
	filters := generateHTTPRouteFilters(aiGatewayRoute)
	for _, base := range filters {
		var f egv1a1.HTTPRouteFilter
		if err := c.client.Get(ctx, client.ObjectKey{Name: base.Name, Namespace: base.Namespace}, &f); err != nil {
			if apierrors.IsNotFound(err) {
				if err = ctrlutil.SetControllerReference(aiGatewayRoute, base, c.client.Scheme()); err != nil {
					panic(fmt.Errorf("BUG: failed to set controller reference for HTTPRouteFilter: %w", err))
				}
				// Create the filter if it does not exist.
				if err = c.client.Create(ctx, base); err != nil {
					return fmt.Errorf("failed to create HTTPRouteFilter %s: %w", base.Name, err)
				}
				c.logger.Info("Created HTTPRouteFilter", "name", base.Name, "namespace", base.Namespace)
			} else {
				return fmt.Errorf("failed to get HTTPRouteFilter %s: %w", base.Name, err)
			}
		}
	}

	// Check if the HTTPRoute exists.
	c.logger.Info("syncing AIGatewayRoute", "namespace", aiGatewayRoute.Namespace, "name", aiGatewayRoute.Name)
	var httpRoute gwapiv1.HTTPRoute
	err := c.client.Get(ctx, client.ObjectKey{Name: aiGatewayRoute.Name, Namespace: aiGatewayRoute.Namespace}, &httpRoute)
	existingRoute := err == nil
	if apierrors.IsNotFound(err) {
		// This means that this AIGatewayRoute is a new one.
		httpRoute = gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:        aiGatewayRoute.Name,
				Namespace:   aiGatewayRoute.Namespace,
				Labels:      make(map[string]string),
				Annotations: make(map[string]string),
			},
			Spec: gwapiv1.HTTPRouteSpec{},
		}

		// Copy labels from AIGatewayRoute to HTTPRoute.
		for k, v := range aiGatewayRoute.Labels {
			httpRoute.Labels[k] = v
		}

		// Copy non-controller annotations from AIGatewayRoute to HTTPRoute.
		for k, v := range aiGatewayRoute.Annotations {
			httpRoute.Annotations[k] = v
		}
		if err = ctrlutil.SetControllerReference(aiGatewayRoute, &httpRoute, c.client.Scheme()); err != nil {
			panic(fmt.Errorf("BUG: failed to set controller reference for HTTPRoute: %w", err))
		}
	} else if err != nil {
		return fmt.Errorf("failed to get HTTPRoute: %w", err)
	}

	// Update the HTTPRoute with the new AIGatewayRoute. A backendRef that fails validation (e.g. a
	// missing or revoked ReferenceGrant) is dropped from the generated rule rather than aborting the
	// whole sync, so the HTTPRoute and the Gateways below still get updated to reflect the now-reduced
	// set of authorized backends instead of staying frozen at their last-good state. The error, if any,
	// is still returned at the end so the caller marks the AIGatewayRoute NotAccepted as before.
	newHTTPRouteErr := c.newHTTPRoute(ctx, &httpRoute, aiGatewayRoute)

	if existingRoute {
		c.logger.Info("updating HTTPRoute", "namespace", httpRoute.Namespace, "name", httpRoute.Name)
		if err = c.client.Update(ctx, &httpRoute); err != nil {
			return fmt.Errorf("failed to update HTTPRoute: %w", err)
		}
	} else {
		c.logger.Info("creating HTTPRoute", "namespace", httpRoute.Namespace, "name", httpRoute.Name)
		if err = c.client.Create(ctx, &httpRoute); err != nil {
			return fmt.Errorf("failed to create HTTPRoute: %w", err)
		}
	}

	if err = c.syncGateways(ctx, aiGatewayRoute); err != nil {
		return fmt.Errorf("failed to sync gw pods: %w", err)
	}

	if newHTTPRouteErr != nil {
		return fmt.Errorf("failed to construct a new HTTPRoute: %w", newHTTPRouteErr)
	}
	return nil
}

// newHTTPRoute updates the HTTPRoute with the new AIGatewayRoute.
//
// A backendRef that fails validation (e.g. a missing or revoked ReferenceGrant for a cross-namespace
// reference) is skipped rather than aborting the whole HTTPRoute construction: dst is still populated
// with every other authorized backendRef, and the validation errors are joined and returned so the
// caller can still surface them (e.g. to mark the AIGatewayRoute NotAccepted) without leaving dst stale.
func (c *AIGatewayRouteController) newHTTPRoute(ctx context.Context, dst *gwapiv1.HTTPRoute, aiGatewayRoute *aigv1b1.AIGatewayRoute) error {
	rewriteFilters := []gwapiv1.HTTPRouteFilter{{
		Type: gwapiv1.HTTPRouteFilterExtensionRef,
		ExtensionRef: &gwapiv1.LocalObjectReference{
			Group: "gateway.envoyproxy.io",
			Kind:  "HTTPRouteFilter",
			Name:  gwapiv1.ObjectName(getHostRewriteFilterName(aiGatewayRoute.Name)),
		},
	}}
	rules := make([]gwapiv1.HTTPRouteRule, 0, len(aiGatewayRoute.Spec.Rules)+1) // +1 for the default rule.
	var errs []error
	for i := range aiGatewayRoute.Spec.Rules {
		rule := &aiGatewayRoute.Spec.Rules[i]
		var backendRefs []gwapiv1.HTTPBackendRef
		for j := range rule.BackendRefs {
			br := &rule.BackendRefs[j]
			backendNamespace := br.GetNamespace(aiGatewayRoute.Namespace)
			dstName := fmt.Sprintf("%s.%s", br.Name, backendNamespace)

			if br.IsInferencePool() {
				// Handle InferencePool backend reference, honoring the (optionally cross-namespace)
				// namespace specified on the backendRef.
				if br.IsCrossNamespace(aiGatewayRoute.Namespace) {
					if err := c.referenceGrantValidator.validateInferencePoolReference(
						ctx,
						aiGatewayRoute.Namespace,
						backendNamespace,
						br.Name,
					); err != nil {
						c.logger.Error(err, "skipping InferencePool backendRef that failed ReferenceGrant validation",
							"namespace", aiGatewayRoute.Namespace, "name", aiGatewayRoute.Name, "backend", dstName)
						errs = append(errs, err)
						continue
					}
				}
				ns := gwapiv1.Namespace(backendNamespace)
				backendRefs = append(backendRefs,
					gwapiv1.HTTPBackendRef{BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Group:     (*gwapiv1.Group)(br.Group),
							Kind:      (*gwapiv1.Kind)(br.Kind),
							Name:      gwapiv1.ObjectName(br.Name),
							Namespace: &ns,
						},
						Weight: br.Weight,
					}},
				)
			} else {
				// Handle AIServiceBackend reference with cross-namespace validation.
				backend, err := c.validateAndGetBackend(ctx, aiGatewayRoute, br)
				if err != nil {
					c.logger.Error(err, "skipping AIServiceBackend backendRef that failed validation",
						"namespace", aiGatewayRoute.Namespace, "name", aiGatewayRoute.Name, "backend", dstName)
					errs = append(errs, fmt.Errorf("failed to get AIServiceBackend %s: %w", dstName, err))
					continue
				}

				// Copy the BackendObjectReference from the AIServiceBackend.
				backendObjRef := backend.Spec.BackendRef

				// Ensure the namespace is explicitly set in the BackendObjectReference
				// only for cross-namespace references.
				// If the AIServiceBackend is in a different namespace than the AIGatewayRoute,
				// the Backend it references is also in that namespace, and we need to set
				// the namespace explicitly in the HTTPRoute's backendRef.
				if backendObjRef.Namespace == nil && backend.Namespace != "" && backend.Namespace != aiGatewayRoute.Namespace {
					ns := gwapiv1.Namespace(backend.Namespace)
					backendObjRef.Namespace = &ns
				}

				backendRefs = append(backendRefs,
					gwapiv1.HTTPBackendRef{BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: backendObjRef,
						Weight:                 br.Weight,
					}},
				)
			}
		}
		var matches []gwapiv1.HTTPRouteMatch
		for j := range rule.Matches {
			matches = append(matches, gwapiv1.HTTPRouteMatch{
				Headers: rule.Matches[j].Headers,
				Path:    &gwapiv1.HTTPPathMatch{Value: &c.rootPrefix},
			})
		}
		rules = append(rules, gwapiv1.HTTPRouteRule{
			Name:        rule.Name,
			BackendRefs: backendRefs,
			Matches:     matches,
			Filters:     rewriteFilters,
			Timeouts:    rule.GetTimeoutsOrDefault(),
		})
	}

	rules = append(rules, gwapiv1.HTTPRouteRule{
		Name:    ptr.To[gwapiv1.SectionName]("route-not-found"),
		Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Value: &c.rootPrefix}}},
		Filters: []gwapiv1.HTTPRouteFilter{{
			Type: gwapiv1.HTTPRouteFilterExtensionRef,
			ExtensionRef: &gwapiv1.LocalObjectReference{
				Group: "gateway.envoyproxy.io",
				Kind:  "HTTPRouteFilter",
				Name:  gwapiv1.ObjectName(getRouteNotFoundFilterName(aiGatewayRoute.Name)),
			},
		}},
	})

	dst.Spec.Rules = rules

	// Initialize labels and annotations maps if they don't exist.
	if dst.Labels == nil {
		dst.Labels = make(map[string]string)
	}
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string)
	}

	// Copy labels from AIGatewayRoute to HTTPRoute.
	for k, v := range aiGatewayRoute.Labels {
		dst.Labels[k] = v
	}

	// Copy non-controller annotations from AIGatewayRoute to HTTPRoute.
	for k, v := range aiGatewayRoute.Annotations {
		dst.Annotations[k] = v
	}

	// HACK: We need to set an annotation so that Envoy Gateway reconciles the HTTPRoute when the backend refs change.
	dst.Annotations[httpRouteBackendRefPriorityAnnotationKey] = buildPriorityAnnotation(aiGatewayRoute.Spec.Rules)
	dst.Annotations[httpRouteAnnotationForAIGatewayGeneratedIndication] = "true"
	// HACK: Stamp a hash of the QuotaPolicies affecting this route's backends so that a QuotaPolicy
	// change (which Envoy Gateway does not watch) actually mutates the HTTPRoute, forcing Envoy Gateway
	// to re-translate and re-run PostTranslateModify. See httpRouteQuotaPolicyHashAnnotationKey.
	quotaHash, err := c.computeQuotaPolicyHash(ctx, aiGatewayRoute)
	if err != nil {
		return fmt.Errorf("failed to compute QuotaPolicy hash: %w", err)
	}
	if quotaHash == "" {
		delete(dst.Annotations, httpRouteQuotaPolicyHashAnnotationKey)
	} else {
		dst.Annotations[httpRouteQuotaPolicyHashAnnotationKey] = quotaHash
	}

	dst.Spec.ParentRefs = aiGatewayRoute.Spec.ParentRefs

	dst.Spec.Hostnames = aiGatewayRoute.Spec.Hostnames
	return errors.Join(errs...)
}

// syncGateways synchronizes the gateways referenced by the AIGatewayRoute by sending events to the gateway controller.
func (c *AIGatewayRouteController) syncGateways(ctx context.Context, aiGatewayRoute *aigv1b1.AIGatewayRoute) error {
	for _, p := range aiGatewayRoute.Spec.ParentRefs {
		gwNamespace := aiGatewayRoute.Namespace
		if p.Namespace != nil {
			gwNamespace = string(*p.Namespace)
		}
		if err := c.syncGateway(ctx, gwNamespace, string(p.Name)); err != nil {
			if aiGatewayRoute.DeletionTimestamp != nil && apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
	}
	return nil
}

// syncGateway is a helper function for syncGateways that sends one GenericEvent to the gateway controller.
func (c *AIGatewayRouteController) syncGateway(ctx context.Context, namespace, name string) error {
	var gw gwapiv1.Gateway
	if err := c.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("Gateway not found", "namespace", namespace, "name", name)
			return fmt.Errorf("gateway %s/%s not found: %w", namespace, name, err)
		}
		c.logger.Error(err, "failed to get Gateway", "namespace", namespace, "name", name)
		return fmt.Errorf("failed to get Gateway %s/%s: %w", namespace, name, err)
	}
	c.logger.Info("syncing Gateway", "namespace", gw.Namespace, "name", gw.Name)
	c.gatewayEventChan <- event.GenericEvent{Object: &gw}
	return nil
}

func (c *AIGatewayRouteController) backend(ctx context.Context, namespace, name string) (*aigv1b1.AIServiceBackend, error) {
	backend := &aigv1b1.AIServiceBackend{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, backend); err != nil {
		return nil, err
	}
	return backend, nil
}

// validateAndGetBackend validates a backend reference (including cross-namespace ReferenceGrant check)
// and returns the AIServiceBackend if valid.
func (c *AIGatewayRouteController) validateAndGetBackend(
	ctx context.Context,
	aiGatewayRoute *aigv1b1.AIGatewayRoute,
	backendRef *aigv1b1.AIGatewayRouteRuleBackendRef,
) (*aigv1b1.AIServiceBackend, error) {
	backendNamespace := backendRef.GetNamespace(aiGatewayRoute.Namespace)

	// Validate cross-namespace reference if applicable
	if backendRef.IsCrossNamespace(aiGatewayRoute.Namespace) {
		if err := c.referenceGrantValidator.validateAIServiceBackendReference(
			ctx,
			aiGatewayRoute.Namespace,
			backendNamespace,
			backendRef.Name,
		); err != nil {
			return nil, err
		}
	}

	// Get the backend
	backend, err := c.backend(ctx, backendNamespace, backendRef.Name)
	if err != nil {
		return nil, fmt.Errorf("AIServiceBackend %s.%s not found", backendRef.Name, backendNamespace)
	}

	return backend, nil
}

// updateAIGatewayRouteStatus updates the status of the AIGatewayRoute.
func (c *AIGatewayRouteController) updateAIGatewayRouteStatus(ctx context.Context, route *aigv1b1.AIGatewayRoute, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: route.Name, Namespace: route.Namespace}, route); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		route.Status.Conditions = newConditions(conditionType, message)
		return c.client.Status().Update(ctx, route)
	})
	if err != nil {
		c.logger.Error(err, "failed to update AIGatewayRoute status")
	}
}

// computeQuotaPolicyHash returns a stable hash of the effective QuotaPolicies
// that target AIServiceBackends referenced by this route. The hash is added to
// the generated HTTPRoute so QuotaPolicy changes force Envoy Gateway to
// reconcile the route.
func (c *AIGatewayRouteController) computeQuotaPolicyHash(ctx context.Context, route *aigv1b1.AIGatewayRoute) (string, error) {
	policies, err := c.fetchEffectiveQuotaPoliciesForRoute(ctx, route)
	if err != nil {
		return "", fmt.Errorf("failed to compute QuotaPolicy hash for route %s/%s: %w", route.Namespace, route.Name, err)
	}
	hash, err := hashQuotaPolicies(policies)
	if err != nil {
		return "", fmt.Errorf("failed to compute QuotaPolicy hash for route %s/%s: %w", route.Namespace, route.Name, err)
	}
	return hash, nil
}

// fetchEffectiveQuotaPoliciesForRoute returns the non-deleting QuotaPolicies
// targeting the AIServiceBackends referenced by route. Each policy contains
// only the target references that are valid for the policy's namespace.
func (c *AIGatewayRouteController) fetchEffectiveQuotaPoliciesForRoute(
	ctx context.Context,
	route *aigv1b1.AIGatewayRoute,
) ([]*aigv1a1.QuotaPolicy, error) {
	seenPolicies := make(map[string]*aigv1a1.QuotaPolicy)
	processedPolicies := make(map[string]struct{})
	seenBackendKeys := make(map[string]bool)
	grantCache := make(map[string]bool)
	for i := range route.Spec.Rules {
		for j := range route.Spec.Rules[i].BackendRefs {
			ref := &route.Spec.Rules[i].BackendRefs[j]
			if ref.IsInferencePool() {
				continue
			}
			backendNamespace := ref.GetNamespace(route.Namespace)
			key := namespacedNameIndexKey(string(ref.Name), backendNamespace)
			if seenBackendKeys[key] {
				continue
			}
			seenBackendKeys[key] = true

			var policies aigv1a1.QuotaPolicyList
			if err := c.client.List(ctx, &policies,
				client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingQuotaPolicy: key}); err != nil {
				return nil, fmt.Errorf("failed to list QuotaPolicies for backend %s: %w", key, err)
			}
			for k := range policies.Items {
				policy := &policies.Items[k]
				if !policy.DeletionTimestamp.IsZero() {
					continue
				}
				policyKey := policy.Namespace + "/" + policy.Name
				if _, processed := processedPolicies[policyKey]; processed {
					continue
				}
				processedPolicies[policyKey] = struct{}{}
				effective := policy.DeepCopy()
				effective.Spec.TargetRefs = make([]gwapiv1a2.NamespacedPolicyTargetReference, 0, len(policy.Spec.TargetRefs))
				for _, target := range policy.Spec.TargetRefs {
					targetNamespace := quotapolicy.TargetNamespace(target, policy.Namespace)
					if (target.Group != "" && target.Group != aiServiceBackendGroup) ||
						(target.Kind != "" && target.Kind != aiServiceBackendKind) {
						continue
					}
					grantKey := policy.Namespace + "\x00" + targetNamespace + "\x00" + string(target.Name)
					allowed, ok := grantCache[grantKey]
					if !ok {
						allowed = c.referenceGrantValidator.validateQuotaPolicyAIServiceBackendReference(
							ctx, policy.Namespace, targetNamespace, string(target.Name)) == nil
						grantCache[grantKey] = allowed
					}
					if !allowed {
						continue
					}
					effective.Spec.TargetRefs = append(effective.Spec.TargetRefs, target)
				}
				if len(effective.Spec.TargetRefs) > 0 {
					seenPolicies[policyKey] = effective
				}
			}
		}
	}
	policies := make([]*aigv1a1.QuotaPolicy, 0, len(seenPolicies))
	for _, policy := range seenPolicies {
		policies = append(policies, policy)
	}
	return policies, nil
}

// hashQuotaPolicies returns a stable eight-character SHA-256 hash of policies.
// The input policies are not modified.
func hashQuotaPolicies(policies []*aigv1a1.QuotaPolicy) (string, error) {
	if len(policies) == 0 {
		return "", nil
	}
	ordered := append([]*aigv1a1.QuotaPolicy(nil), policies...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Namespace != ordered[j].Namespace {
			return ordered[i].Namespace < ordered[j].Namespace
		}
		return ordered[i].Name < ordered[j].Name
	})
	type hashedPolicy struct {
		Namespace string                  `json:"namespace"`
		Name      string                  `json:"name"`
		Spec      aigv1a1.QuotaPolicySpec `json:"spec"`
	}
	hashed := make([]hashedPolicy, 0, len(ordered))
	for _, policy := range ordered {
		hashed = append(hashed, hashedPolicy{Namespace: policy.Namespace, Name: policy.Name, Spec: policy.Spec})
	}
	data, err := stdjson.Marshal(hashed)
	if err != nil {
		return "", fmt.Errorf("failed to marshal QuotaPolicies for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:4]), nil
}

// Build an annotation that contains the priority of each backend ref. This is used to ensure Envoy Gateway reconciles the
// HTTP route when the priorities change.
func buildPriorityAnnotation(rules []aigv1b1.AIGatewayRouteRule) string {
	priorities := make([]string, 0, len(rules))
	for i, rule := range rules {
		for _, br := range rule.BackendRefs {
			var priority uint32
			if br.Priority != nil {
				priority = *br.Priority
			}
			priorities = append(priorities, fmt.Sprintf("%d:%s:%d", i, br.Name, priority))
		}
	}
	return strings.Join(priorities, ",")
}
