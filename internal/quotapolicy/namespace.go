// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package quotapolicy contains shared QuotaPolicy helpers.
package quotapolicy

import gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

// TargetNamespace resolves the namespace of a QuotaPolicy target reference.
// An omitted or empty namespace means the namespace of the QuotaPolicy.
func TargetNamespace(
	ref gwapiv1a2.NamespacedPolicyTargetReference,
	policyNamespace string,
) string {
	if ref.Namespace == nil || *ref.Namespace == "" {
		return policyNamespace
	}
	return string(*ref.Namespace)
}
