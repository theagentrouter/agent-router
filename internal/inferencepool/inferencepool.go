// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package inferencepool holds helpers shared by the controller and the extension server for
// Gateway API Inference Extension InferencePool backends.
package inferencepool

import (
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// Group is the API group of InferencePool resources.
	Group = gwaiev1.GroupName
	// Kind is the kind of InferencePool resources.
	Kind = "InferencePool"
)

// IsBackendRef reports whether ref targets an InferencePool.
func IsBackendRef(ref *gwapiv1.BackendRef) bool {
	return ref.Group != nil && string(*ref.Group) == Group &&
		ref.Kind != nil && string(*ref.Kind) == Kind
}
