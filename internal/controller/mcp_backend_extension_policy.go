// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// httpRouteAnnotationForMCPBackendExtensionPolicy is internalapi.MCPBackendExtensionPolicyAnnotation with Envoy
// Gateway's annotation prefix, so Envoy Gateway copies it into the route metadata.
const httpRouteAnnotationForMCPBackendExtensionPolicy = egAnnotationPrefix + internalapi.MCPBackendExtensionPolicyAnnotation

// MCPBackendExtensionPolicyController implements [reconcile.TypedReconciler] for [aigv1a1.MCPBackendExtensionPolicy].
//
// For each policy it renders one EnvoyExtensionPolicy, which selects by label the per-backend HTTPRoutes the policy is
// in effect for, and reports the outcome in the policy's status. The MCPRouteController sets the labels and
// annotations, and the extension server copies the policy's dynamic module filters to the MCP backend listener. Envoy
// Gateway validates and applies the extensions, and fails routes closed when they are invalid.
type MCPBackendExtensionPolicyController struct {
	client client.Client
	logger logr.Logger
}

// NewMCPBackendExtensionPolicyController creates a new reconciler for MCPBackendExtensionPolicy resources.
func NewMCPBackendExtensionPolicyController(client client.Client, logger logr.Logger) *MCPBackendExtensionPolicyController {
	return &MCPBackendExtensionPolicyController{client: client, logger: logger}
}

// Reconcile implements [reconcile.TypedReconciler] for [aigv1a1.MCPBackendExtensionPolicy].
func (c *MCPBackendExtensionPolicyController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.logger.Info("Reconciling MCPBackendExtensionPolicy", "namespace", req.Namespace, "name", req.Name)

	var policy aigv1a1.MCPBackendExtensionPolicy
	if err := c.client.Get(ctx, req.NamespacedName, &policy); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// The generated EnvoyExtensionPolicy is garbage collected with the policy.
			c.logger.Info("Deleting MCPBackendExtensionPolicy",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !policy.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	accepted, message, err := c.syncMCPBackendExtensionPolicy(ctx, &policy)
	if err != nil {
		c.logger.Error(err, "failed to sync MCPBackendExtensionPolicy")
		c.updateMCPBackendExtensionPolicyStatus(ctx, &policy, aigv1a1.ConditionTypeNotAccepted, err.Error())
		return ctrl.Result{}, err
	}
	if !accepted {
		c.updateMCPBackendExtensionPolicyStatus(ctx, &policy, aigv1a1.ConditionTypeNotAccepted, message)
		return ctrl.Result{}, nil
	}
	c.updateMCPBackendExtensionPolicyStatus(ctx, &policy, aigv1a1.ConditionTypeAccepted, message)
	return ctrl.Result{}, nil
}

// syncMCPBackendExtensionPolicy is the main logic for reconciling the MCPBackendExtensionPolicy resource.
// It returns whether the policy is accepted on all its targets, and the status message.
func (c *MCPBackendExtensionPolicyController) syncMCPBackendExtensionPolicy(ctx context.Context, policy *aigv1a1.MCPBackendExtensionPolicy) (bool, string, error) {
	eep, err := c.syncEnvoyExtensionPolicy(ctx, policy)
	if err != nil {
		return false, "", err
	}

	// Envoy Gateway validates the extensions. When it rejects the EnvoyExtensionPolicy, for example for a dynamic
	// module missing from the EnvoyProxy, it fails the selected routes closed; report its reason.
	if rejected := rejectedByEnvoyGateway(eep); len(rejected) > 0 {
		return false, fmt.Sprintf("EnvoyExtensionPolicy %s rejected by Envoy Gateway: %s", eep.Name, strings.Join(rejected, "; ")), nil
	}

	accepted := true
	var messages []string
	for _, targetRef := range policy.Spec.TargetRefs {
		ok, message, err := c.evaluateTargetRef(ctx, policy, targetRef)
		if err != nil {
			return false, "", err
		}
		accepted = accepted && ok
		messages = append(messages, message)
	}
	return accepted, strings.Join(messages, "; "), nil
}

// syncEnvoyExtensionPolicy creates or updates the EnvoyExtensionPolicy generated for the policy.
func (c *MCPBackendExtensionPolicyController) syncEnvoyExtensionPolicy(ctx context.Context, policy *aigv1a1.MCPBackendExtensionPolicy) (*egv1a1.EnvoyExtensionPolicy, error) {
	eep, err := newMCPEnvoyExtensionPolicy(policy, c.client.Scheme())
	if err != nil {
		return nil, err
	}

	var existing egv1a1.EnvoyExtensionPolicy
	err = c.client.Get(ctx, client.ObjectKeyFromObject(eep), &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get EnvoyExtensionPolicy: %w", err)
	}
	if apierrors.IsNotFound(err) {
		c.logger.Info("Creating EnvoyExtensionPolicy", "namespace", eep.Namespace, "name", eep.Name)
		if err = c.client.Create(ctx, eep); err != nil {
			return nil, fmt.Errorf("failed to create EnvoyExtensionPolicy: %w", err)
		}
		return eep, nil
	}
	// Never take over an object the policy doesn't control: one someone else created under this name, or one left by a
	// deleted policy of the same name that garbage collection hasn't removed yet.
	if !metav1.IsControlledBy(&existing, policy) {
		return nil, fmt.Errorf("EnvoyExtensionPolicy %s already exists and is not owned by this MCPBackendExtensionPolicy", existing.Name)
	}
	// Update the existing object, which carries Envoy Gateway's status, and only on change: this runs on every status
	// update Envoy Gateway makes.
	if equality.Semantic.DeepEqual(existing.Spec, eep.Spec) {
		return &existing, nil
	}
	existing.Spec = eep.Spec
	c.logger.Info("Updating EnvoyExtensionPolicy", "namespace", existing.Namespace, "name", existing.Name)
	if err = c.client.Update(ctx, &existing); err != nil {
		return nil, fmt.Errorf("failed to update EnvoyExtensionPolicy: %w", err)
	}
	return &existing, nil
}

// newMCPEnvoyExtensionPolicy returns the EnvoyExtensionPolicy generated for the policy, owned by it. Both controllers
// use it: the MCPBackendExtensionPolicyController creates and updates the object, and the MCPRouteController creates
// it if missing before labeling a route (see setMCPBackendExtensionPolicy).
func newMCPEnvoyExtensionPolicy(policy *aigv1a1.MCPBackendExtensionPolicy, scheme *runtime.Scheme) (*egv1a1.EnvoyExtensionPolicy, error) {
	eep := &egv1a1.EnvoyExtensionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpEnvoyExtensionPolicyName(policy.Name),
			Namespace: policy.Namespace,
		},
		Spec: egv1a1.EnvoyExtensionPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetSelectors: []egv1a1.TargetSelector{{
					Group:       ptr.To(gwapiv1.Group(gwapiv1.GroupName)),
					Kind:        "HTTPRoute",
					MatchLabels: map[string]string{internalapi.MCPBackendExtensionPolicyLabel: mcpBackendExtensionPolicyLabelValue(policy.Name)},
				}},
			},
			Backends:      policy.Spec.Backends,
			DynamicModule: policy.Spec.DynamicModule,
		},
	}
	if err := ctrlutil.SetControllerReference(policy, eep, scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference for EnvoyExtensionPolicy: %w", err)
	}
	return eep, nil
}

// evaluateTargetRef returns whether the policy is in effect on the given target, and a status message for it.
func (c *MCPBackendExtensionPolicyController) evaluateTargetRef(ctx context.Context, policy *aigv1a1.MCPBackendExtensionPolicy, targetRef gwapiv1.LocalPolicyTargetReferenceWithSectionName) (bool, string, error) {
	var mcpRoute aigv1b1.MCPRoute
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: string(targetRef.Name)}, &mcpRoute); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("MCPRoute %s not found", targetRef.Name), nil
		}
		return false, "", fmt.Errorf("failed to get MCPRoute %s: %w", targetRef.Name, err)
	}

	var backends []gwapiv1.ObjectName
	for i := range mcpRoute.Spec.BackendRefs {
		name := mcpRoute.Spec.BackendRefs[i].Name
		if targetRef.SectionName == nil || string(*targetRef.SectionName) == string(name) {
			backends = append(backends, name)
		}
	}
	if len(backends) == 0 {
		if targetRef.SectionName != nil {
			return false, fmt.Sprintf("MCPRoute %s has no backendRef named %s", mcpRoute.Name, *targetRef.SectionName), nil
		}
		return false, fmt.Sprintf("MCPRoute %s has no backendRefs", mcpRoute.Name), nil
	}

	policies, err := listMCPBackendExtensionPoliciesForMCPRoute(ctx, c.client, &mcpRoute)
	if err != nil {
		return false, "", fmt.Errorf("failed to list MCPBackendExtensionPolicies: %w", err)
	}
	effective := effectiveMCPBackendExtensionPolicies(&mcpRoute, policies)
	var applied, overridden []string
	for _, name := range backends {
		if winner, ok := effective[name]; ok {
			if winner.Name == policy.Name {
				applied = append(applied, string(name))
			} else {
				overridden = append(overridden, fmt.Sprintf("%s (by %s)", name, winner.Name))
			}
		}
	}
	if len(applied) == 0 {
		return false, fmt.Sprintf("MCPRoute %s: overridden for all backends: %s", mcpRoute.Name, strings.Join(overridden, ", ")), nil
	}
	message := fmt.Sprintf("MCPRoute %s: applied to backends: %s", mcpRoute.Name, strings.Join(applied, ", "))
	if len(overridden) > 0 {
		message += "; overridden for: " + strings.Join(overridden, ", ")
	}
	return true, message, nil
}

// rejectedByEnvoyGateway returns the Accepted=False conditions Envoy Gateway reported on the EnvoyExtensionPolicy.
func rejectedByEnvoyGateway(eep *egv1a1.EnvoyExtensionPolicy) []string {
	var rejected []string
	for _, ancestor := range eep.Status.Ancestors {
		for _, cond := range ancestor.Conditions {
			if cond.Type == string(gwapiv1.PolicyConditionAccepted) && cond.Status == metav1.ConditionFalse {
				rejected = append(rejected, fmt.Sprintf("%s: %s", cond.Reason, cond.Message))
			}
		}
	}
	return rejected
}

// updateMCPBackendExtensionPolicyStatus updates the status of the MCPBackendExtensionPolicy.
func (c *MCPBackendExtensionPolicyController) updateMCPBackendExtensionPolicyStatus(ctx context.Context, policy *aigv1a1.MCPBackendExtensionPolicy, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: policy.Name, Namespace: policy.Namespace}, policy); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		// The policy is also reconciled on Envoy Gateway's status updates, so write only on change, as Envoy Gateway's
		// own status updater does.
		if conditions := policy.Status.Conditions; len(conditions) == 1 &&
			conditions[0].Type == conditionType && conditions[0].Message == message {
			return nil
		}
		policy.Status.Conditions = newConditions(conditionType, message)
		return c.client.Status().Update(ctx, policy)
	})
	if err != nil {
		c.logger.Error(err, "failed to update MCPBackendExtensionPolicy status")
	}
}

// mcpRouteToMCPBackendExtensionPolicy maps MCPRoute changes to the MCPBackendExtensionPolicies targeting the MCPRoute,
// so their status follows changes to the MCPRoute's backendRefs.
func (c *MCPBackendExtensionPolicyController) mcpRouteToMCPBackendExtensionPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	policies, err := listMCPBackendExtensionPoliciesForMCPRoute(ctx, c.client, obj)
	if err != nil {
		c.logger.Error(err, "failed to list MCPBackendExtensionPolicies for MCPRoute",
			"namespace", obj.GetNamespace(), "name", obj.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(policies))
	for i := range policies {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies[i])})
	}
	return requests
}

// mcpBackendExtensionPolicyToSiblings maps MCPBackendExtensionPolicy changes to the policies targeting the same
// MCPRoutes, since a change can move a backend from one policy to another.
func (c *MCPBackendExtensionPolicyController) mcpBackendExtensionPolicyToSiblings(ctx context.Context, obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	for _, mcpRoute := range mcpBackendExtensionPolicyToMCPRoute(ctx, obj) {
		route := &aigv1b1.MCPRoute{ObjectMeta: metav1.ObjectMeta{Name: mcpRoute.Name, Namespace: mcpRoute.Namespace}}
		for _, request := range c.mcpRouteToMCPBackendExtensionPolicy(ctx, route) {
			if !slices.Contains(requests, request) {
				requests = append(requests, request)
			}
		}
	}
	return requests
}

// mcpBackendExtensionPolicyToMCPRoute maps MCPBackendExtensionPolicy changes to the MCPRoutes it targets, so their
// per-backend HTTPRoutes are relabeled. On updates it is called for both the old and the new object, so MCPRoutes
// that are no longer targeted are reconciled too.
func mcpBackendExtensionPolicyToMCPRoute(_ context.Context, obj client.Object) []reconcile.Request {
	policy := obj.(*aigv1a1.MCPBackendExtensionPolicy)
	var requests []reconcile.Request
	for _, targetRef := range policy.Spec.TargetRefs {
		request := reconcile.Request{NamespacedName: client.ObjectKey{Name: string(targetRef.Name), Namespace: policy.Namespace}}
		if !slices.Contains(requests, request) {
			requests = append(requests, request)
		}
	}
	return requests
}

// mcpBackendExtensionPolicyTargetRefsChanged filters MCPBackendExtensionPolicy updates down to targetRefs changes.
// Which policy is in effect on a backend depends only on targetRefs, names and creation times, so editing the
// extensions alone doesn't need the MCPRoutes or the sibling policies reconciled.
var mcpBackendExtensionPolicyTargetRefsChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldPolicy, oldOK := e.ObjectOld.(*aigv1a1.MCPBackendExtensionPolicy)
		newPolicy, newOK := e.ObjectNew.(*aigv1a1.MCPBackendExtensionPolicy)
		return !oldOK || !newOK || !equality.Semantic.DeepEqual(oldPolicy.Spec.TargetRefs, newPolicy.Spec.TargetRefs)
	},
}

// listMCPBackendExtensionPoliciesForMCPRoute returns the MCPBackendExtensionPolicies targeting the given MCPRoute.
func listMCPBackendExtensionPoliciesForMCPRoute(ctx context.Context, c client.Client, mcpRoute client.Object) ([]aigv1a1.MCPBackendExtensionPolicy, error) {
	var policies aigv1a1.MCPBackendExtensionPolicyList
	key := namespacedNameIndexKey(mcpRoute.GetName(), mcpRoute.GetNamespace())
	if err := c.List(ctx, &policies, client.InNamespace(mcpRoute.GetNamespace()),
		client.MatchingFields{k8sClientIndexMCPRouteToTargetingMCPBackendExtensionPolicy: key}); err != nil {
		return nil, err
	}
	return policies.Items, nil
}

// effectiveMCPBackendExtensionPolicies returns the policy in effect for each backendRef of the MCPRoute, following
// Gateway API policy attachment (GEP-713): a policy naming the backend in sectionName beats one targeting the whole
// MCPRoute, then the oldest wins, then the name that sorts first. Backends without a policy are left out.
//
// Deciding here, rather than leaving it to Envoy Gateway's ordering of the generated EnvoyExtensionPolicies, means
// each per-backend HTTPRoute is selected by exactly one of them.
func effectiveMCPBackendExtensionPolicies(mcpRoute *aigv1b1.MCPRoute, policies []aigv1a1.MCPBackendExtensionPolicy) map[gwapiv1.ObjectName]*aigv1a1.MCPBackendExtensionPolicy {
	ordered := make([]*aigv1a1.MCPBackendExtensionPolicy, 0, len(policies))
	for i := range policies {
		if policies[i].DeletionTimestamp.IsZero() {
			ordered = append(ordered, &policies[i])
		}
	}
	slices.SortFunc(ordered, func(a, b *aigv1a1.MCPBackendExtensionPolicy) int {
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			if a.CreationTimestamp.Before(&b.CreationTimestamp) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})

	effective := make(map[gwapiv1.ObjectName]*aigv1a1.MCPBackendExtensionPolicy)
	for i := range mcpRoute.Spec.BackendRefs {
		backendName := mcpRoute.Spec.BackendRefs[i].Name
		var sectionPolicy, routePolicy *aigv1a1.MCPBackendExtensionPolicy
		for _, policy := range ordered {
			for _, targetRef := range policy.Spec.TargetRefs {
				if string(targetRef.Name) != mcpRoute.Name {
					continue
				}
				switch {
				case targetRef.SectionName != nil && string(*targetRef.SectionName) == string(backendName):
					if sectionPolicy == nil {
						sectionPolicy = policy
					}
				case targetRef.SectionName == nil:
					if routePolicy == nil {
						routePolicy = policy
					}
				}
			}
		}
		if sectionPolicy != nil {
			effective[backendName] = sectionPolicy
		} else if routePolicy != nil {
			effective[backendName] = routePolicy
		}
	}
	return effective
}

// setMCPBackendExtensionPolicy labels and annotates the per-backend HTTPRoute with the policy in effect for its
// backend, or removes both when there is none. The generated EnvoyExtensionPolicy selects the route by the label;
// the extension server reads the annotation, which Envoy Gateway copies into the route metadata, to copy only the
// policy's filters to the MCP backend listener.
//
// The policy's EnvoyExtensionPolicy is ensured to exist before the label switches to it. Otherwise no
// EnvoyExtensionPolicy would select the route until it was created, and the backend would run without an extension in
// between.
func (c *MCPRouteController) setMCPBackendExtensionPolicy(ctx context.Context, httpRoute *gwapiv1.HTTPRoute, policy *aigv1a1.MCPBackendExtensionPolicy) error {
	if policy == nil {
		delete(httpRoute.Labels, internalapi.MCPBackendExtensionPolicyLabel)
		delete(httpRoute.Annotations, httpRouteAnnotationForMCPBackendExtensionPolicy)
		return nil
	}
	labelValue := mcpBackendExtensionPolicyLabelValue(policy.Name)
	if httpRoute.Labels[internalapi.MCPBackendExtensionPolicyLabel] == labelValue &&
		httpRoute.Annotations[httpRouteAnnotationForMCPBackendExtensionPolicy] == policy.Name {
		return nil
	}
	if err := c.ensureMCPEnvoyExtensionPolicy(ctx, policy); err != nil {
		return err
	}
	if httpRoute.Labels == nil {
		httpRoute.Labels = make(map[string]string)
	}
	if httpRoute.Annotations == nil {
		httpRoute.Annotations = make(map[string]string)
	}
	httpRoute.Labels[internalapi.MCPBackendExtensionPolicyLabel] = labelValue
	httpRoute.Annotations[httpRouteAnnotationForMCPBackendExtensionPolicy] = policy.Name
	return nil
}

// ensureMCPEnvoyExtensionPolicy creates the EnvoyExtensionPolicy generated for the policy if it is missing. Keeping it
// up to date is the MCPBackendExtensionPolicyController's job: an existing one with an older spec still carries the
// policy's extensions until that controller updates it.
func (c *MCPRouteController) ensureMCPEnvoyExtensionPolicy(ctx context.Context, policy *aigv1a1.MCPBackendExtensionPolicy) error {
	var existing egv1a1.EnvoyExtensionPolicy
	key := client.ObjectKey{Name: mcpEnvoyExtensionPolicyName(policy.Name), Namespace: policy.Namespace}
	err := c.client.Get(ctx, key, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get EnvoyExtensionPolicy: %w", err)
	}
	// An object the policy doesn't control (someone else's, or one left by a deleted policy of the same name awaiting
	// garbage collection) may not select the label. Fail, so the label stays as it is and the reconcile is retried.
	if err == nil && !metav1.IsControlledBy(&existing, policy) {
		return fmt.Errorf("EnvoyExtensionPolicy %s already exists and is not owned by MCPBackendExtensionPolicy %s", existing.Name, policy.Name)
	}

	if apierrors.IsNotFound(err) {
		eep, err := newMCPEnvoyExtensionPolicy(policy, c.client.Scheme())
		if err != nil {
			return err
		}
		c.logger.Info("Creating EnvoyExtensionPolicy", "namespace", eep.Namespace, "name", eep.Name)
		if err = c.client.Create(ctx, eep); err != nil && !apierrors.IsAlreadyExists(err) {
			// AlreadyExists: created concurrently, normally by the MCPBackendExtensionPolicyController. If it isn't
			// this policy's, the next reconcile here fails the ownership check above.
			return fmt.Errorf("failed to create EnvoyExtensionPolicy: %w", err)
		}
	}
	return nil
}

// mcpBackendExtensionPolicyLabelValue returns the value of internalapi.MCPBackendExtensionPolicyLabel for a policy.
// Policy names longer than a label value allows are truncated and suffixed with a hash of the full name.
func mcpBackendExtensionPolicyLabelValue(policyName string) string {
	if len(policyName) <= k8sLabelValueMaxLen {
		return policyName
	}
	return truncateAndAppendHash(policyName, shortStableHash(policyName), k8sLabelValueMaxLen)
}

func mcpEnvoyExtensionPolicyName(policyName string) string {
	return fmt.Sprintf("%s%s", internalapi.MCPBackendExtensionPolicyPrefix, policyName)
}
