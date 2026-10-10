// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package quotapolicy

import (
	"testing"

	"k8s.io/utils/ptr"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestTargetNamespace(t *testing.T) {
	t.Run("defaults to policy namespace", func(t *testing.T) {
		ref := gwapiv1a2.NamespacedPolicyTargetReference{Name: "backend"}
		if got := TargetNamespace(ref, "policy-ns"); got != "policy-ns" {
			t.Fatalf("TargetNamespace() = %q, want %q", got, "policy-ns")
		}
	})

	t.Run("uses explicit target namespace", func(t *testing.T) {
		ref := gwapiv1a2.NamespacedPolicyTargetReference{
			Name:      "backend",
			Namespace: ptr.To(gwapiv1a2.Namespace("backend-ns")),
		}
		if got := TargetNamespace(ref, "policy-ns"); got != "backend-ns" {
			t.Fatalf("TargetNamespace() = %q, want %q", got, "backend-ns")
		}
	})

	t.Run("treats an empty namespace as omitted", func(t *testing.T) {
		ref := gwapiv1a2.NamespacedPolicyTargetReference{
			Name:      "backend",
			Namespace: ptr.To(gwapiv1a2.Namespace("")),
		}
		if got := TargetNamespace(ref, "policy-ns"); got != "policy-ns" {
			t.Fatalf("TargetNamespace() = %q, want %q", got, "policy-ns")
		}
	})
}
