// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	// aiGatewayRouteKind is the kind for AIGatewayRoute.
	aiGatewayRouteKind = "AIGatewayRoute"
	// backendSecurityPolicyKind is the kind for BackendSecurityPolicy.
	backendSecurityPolicyKind = "BackendSecurityPolicy"
	// secretGroup is the API group for the core Secret resource (the core group is the empty string).
	secretGroup = ""
	// secretKind is the kind for the core Secret resource.
	secretKind = "Secret"
)

// ReferenceGrantValidator validates cross-namespace references using ReferenceGrant resources.
type referenceGrantValidator struct {
	client client.Client
}

// NewReferenceGrantValidator creates a new ReferenceGrantValidator.
func newReferenceGrantValidator(c client.Client) *referenceGrantValidator {
	return &referenceGrantValidator{client: c}
}

// validateAIServiceBackendReference validates that an AIGatewayRoute can reference an AIServiceBackend
// in a different namespace by checking for a valid ReferenceGrant.
//
// Parameters:
//   - ctx: context for the operation
//   - routeNamespace: namespace of the AIGatewayRoute
//   - backendNamespace: namespace of the AIServiceBackend
//   - backendName: name of the AIServiceBackend (optional, for logging)
//
// Returns:
//   - error: nil if the reference is valid (same namespace or valid ReferenceGrant exists), error otherwise
func (v *referenceGrantValidator) validateAIServiceBackendReference(
	ctx context.Context,
	routeNamespace string,
	backendNamespace string,
	backendName string,
) error {
	return v.validateReference(ctx,
		aiServiceBackendGroup, aiGatewayRouteKind, routeNamespace,
		aiServiceBackendGroup, aiServiceBackendKind, backendNamespace, backendName)
}

// validateInferencePoolReference validates that an AIGatewayRoute can reference an InferencePool
// in a different namespace by checking for a valid ReferenceGrant.
//
// Parameters:
//   - ctx: context for the operation
//   - routeNamespace: namespace of the AIGatewayRoute
//   - poolNamespace: namespace of the InferencePool
//   - poolName: name of the InferencePool (optional, for logging)
//
// Returns:
//   - error: nil if the reference is valid (same namespace or valid ReferenceGrant exists), error otherwise
func (v *referenceGrantValidator) validateInferencePoolReference(
	ctx context.Context,
	routeNamespace string,
	poolNamespace string,
	poolName string,
) error {
	return v.validateReference(ctx,
		aiServiceBackendGroup, aiGatewayRouteKind, routeNamespace,
		inferencePoolGroup, inferencePoolKind, poolNamespace, poolName)
}

// validateSecretReference validates that a BackendSecurityPolicy can reference a core Secret in a
// different namespace by checking for a valid ReferenceGrant allowing BackendSecurityPolicy from
// bspNamespace to reference Secret in secretNamespace.
//
// This is a thin BackendSecurityPolicy-specific entry point over the generic validateReference;
// other resource types needing a similar check should add their own equally small wrapper instead
// of growing this one.
//
// Parameters:
//   - ctx: context for the operation
//   - bspNamespace: namespace of the BackendSecurityPolicy
//   - secretNamespace: namespace of the referenced Secret
//   - secretName: name of the Secret (optional, for the error message)
//
// Returns:
//   - error: nil if the reference is valid (same namespace or valid ReferenceGrant exists), error otherwise
func (v *referenceGrantValidator) validateSecretReference(
	ctx context.Context,
	bspNamespace string,
	secretNamespace string,
	secretName string,
) error {
	return v.validateReference(ctx,
		aiServiceBackendGroup, backendSecurityPolicyKind, bspNamespace,
		secretGroup, secretKind, secretNamespace, secretName)
}

// validateReference validates that a resource identified by fromGroup/fromKind in fromNamespace can
// reference a target resource identified by targetGroup/targetKind in targetNamespace, by checking
// for a valid ReferenceGrant. Same-namespace references are always allowed without a ReferenceGrant.
func (v *referenceGrantValidator) validateReference(
	ctx context.Context,
	fromGroup gwapiv1b1.Group,
	fromKind gwapiv1b1.Kind,
	fromNamespace string,
	targetGroup gwapiv1b1.Group,
	targetKind gwapiv1b1.Kind,
	targetNamespace string,
	targetName string,
) error {
	// Same namespace references don't need a ReferenceGrant.
	if fromNamespace == targetNamespace {
		return nil
	}

	indexKey := getReferenceGrantIndexKey(targetNamespace, string(targetKind))
	var referenceGrants gwapiv1b1.ReferenceGrantList
	if err := v.client.List(ctx, &referenceGrants,
		client.MatchingFields{k8sClientIndexReferenceGrantToTargetKind: indexKey},
	); err != nil {
		return fmt.Errorf("failed to list ReferenceGrants in namespace %s for kind %s: %w",
			targetNamespace, targetKind, err)
	}

	// Check if any ReferenceGrant allows this cross-namespace reference.
	for i := range referenceGrants.Items {
		grant := &referenceGrants.Items[i]
		if v.isReferenceGrantValid(grant, fromGroup, fromKind, fromNamespace, targetGroup, targetKind) {
			return nil
		}
	}

	return fmt.Errorf(
		"cross-namespace reference from %s in namespace %s to %s %s in namespace %s is not permitted: "+
			"no valid ReferenceGrant found in namespace %s. "+
			"A ReferenceGrant must allow %s from namespace %s to reference %s in namespace %s",
		fromKind, fromNamespace, targetKind, targetName, targetNamespace, targetNamespace, fromKind, fromNamespace, targetKind, targetNamespace,
	)
}

// isReferenceGrantValid checks if a ReferenceGrant allows a resource identified by fromGroup/fromKind
// in fromNamespace to reference the target resource identified by targetGroup/targetKind.
func (v *referenceGrantValidator) isReferenceGrantValid(
	grant *gwapiv1b1.ReferenceGrant,
	fromGroup gwapiv1b1.Group,
	fromKind gwapiv1b1.Kind,
	fromNamespace string,
	targetGroup gwapiv1b1.Group,
	targetKind gwapiv1b1.Kind,
) bool {
	// Check if the grant allows references from fromGroup/fromKind in fromNamespace.
	fromAllowed := false
	for _, from := range grant.Spec.From {
		if v.matchesFrom(&from, fromGroup, fromKind, fromNamespace) {
			fromAllowed = true
			break
		}
	}

	if !fromAllowed {
		return false
	}

	// Check if the grant allows references to the target resource.
	for _, to := range grant.Spec.To {
		if v.matchesTo(&to, targetGroup, targetKind) {
			return true
		}
	}

	return false
}

// matchesFrom checks if a ReferenceGrantFrom matches a reference from fromGroup/fromKind in fromNamespace.
func (v *referenceGrantValidator) matchesFrom(
	from *gwapiv1b1.ReferenceGrantFrom,
	fromGroup gwapiv1b1.Group,
	fromKind gwapiv1b1.Kind,
	fromNamespace string,
) bool {
	// Check group.
	if from.Group != fromGroup {
		return false
	}

	// Check kind.
	if from.Kind != fromKind {
		return false
	}

	// Check namespace.
	if from.Namespace != gwapiv1b1.Namespace(fromNamespace) {
		return false
	}

	return true
}

// matchesTo checks if a ReferenceGrantTo matches the target resource identified by targetGroup/targetKind.
func (v *referenceGrantValidator) matchesTo(to *gwapiv1b1.ReferenceGrantTo, targetGroup gwapiv1b1.Group, targetKind gwapiv1b1.Kind) bool {
	// Check group
	if to.Group != targetGroup {
		return false
	}

	// Check kind
	if to.Kind != targetKind {
		return false
	}

	// If a specific name is specified, we would need to check it here,
	// but ReferenceGrant typically doesn't specify individual resource names
	// (that's handled by the Name field which is optional in the spec)
	// For now, we only check group and kind as per Gateway API spec

	return true
}
