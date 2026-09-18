// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package v1alpha1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestAIGatewayRouteRule_GetStreamIdleTimeout(t *testing.T) {
	tests := []struct {
		name     string
		rule     *AIGatewayRouteRule
		expected time.Duration
	}{
		{name: "nil rule disables deadline", rule: nil, expected: 0},
		{name: "unset field disables deadline", rule: &AIGatewayRouteRule{}, expected: 0},
		{
			name:     "valid 5s duration is parsed",
			rule:     &AIGatewayRouteRule{StreamIdleTimeout: ptr.To(gwapiv1.Duration("5s"))},
			expected: 5 * time.Second,
		},
		{
			name:     "negative duration is treated as unset",
			rule:     &AIGatewayRouteRule{StreamIdleTimeout: ptr.To(gwapiv1.Duration("-1s"))},
			expected: 0,
		},
		{
			name:     "malformed duration falls back to zero rather than panicking",
			rule:     &AIGatewayRouteRule{StreamIdleTimeout: ptr.To(gwapiv1.Duration("nope"))},
			expected: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.rule.GetStreamIdleTimeout())
		})
	}
}

func TestAIGatewayRouteRule_GetTimeoutsWithDefaults(t *testing.T) {
	tests := []struct {
		name     string
		rule     *AIGatewayRouteRule
		expected *gwapiv1.HTTPRouteTimeouts
	}{
		{
			name: "nil timeouts should get default request timeout",
			rule: &AIGatewayRouteRule{
				Timeouts: nil,
			},
			expected: &gwapiv1.HTTPRouteTimeouts{
				Request: ptr.To(gwapiv1.Duration("60s")),
			},
		},
		{
			name: "timeouts with nil request should get default request timeout",
			rule: &AIGatewayRouteRule{
				Timeouts: &gwapiv1.HTTPRouteTimeouts{
					BackendRequest: ptr.To(gwapiv1.Duration("30s")),
				},
			},
			expected: &gwapiv1.HTTPRouteTimeouts{
				Request:        ptr.To(gwapiv1.Duration("60s")),
				BackendRequest: ptr.To(gwapiv1.Duration("30s")),
			},
		},
		{
			name: "timeouts with existing request should be preserved",
			rule: &AIGatewayRouteRule{
				Timeouts: &gwapiv1.HTTPRouteTimeouts{
					Request:        ptr.To(gwapiv1.Duration("45s")),
					BackendRequest: ptr.To(gwapiv1.Duration("30s")),
				},
			},
			expected: &gwapiv1.HTTPRouteTimeouts{
				Request:        ptr.To(gwapiv1.Duration("45s")),
				BackendRequest: ptr.To(gwapiv1.Duration("30s")),
			},
		},
		{
			name: "timeouts with only request timeout should be preserved",
			rule: &AIGatewayRouteRule{
				Timeouts: &gwapiv1.HTTPRouteTimeouts{
					Request: ptr.To(gwapiv1.Duration("120s")),
				},
			},
			expected: &gwapiv1.HTTPRouteTimeouts{
				Request: ptr.To(gwapiv1.Duration("120s")),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.rule.GetTimeoutsOrDefault()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAIGatewayRouteRuleBackendRef_IsInferencePool(t *testing.T) {
	tests := []struct {
		name     string
		ref      *AIGatewayRouteRuleBackendRef
		expected bool
	}{
		{
			name:     "Nil reference",
			ref:      nil,
			expected: false,
		},
		{
			name: "AIServiceBackend reference (no group/kind)",
			ref: &AIGatewayRouteRuleBackendRef{
				Name: "test-backend",
			},
			expected: false,
		},
		{
			name: "InferencePool reference",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:  "test-pool",
				Group: new(inferencePoolGroup),
				Kind:  new(inferencePoolKind),
			},
			expected: true,
		},
		{
			name: "Other resource reference",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:  "test-other",
				Group: new("other.group"),
				Kind:  new("OtherKind"),
			},
			expected: false,
		},
		{
			name: "Partial reference (only group)",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:  "test-partial",
				Group: new(inferencePoolGroup),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.ref.IsInferencePool()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAIGatewayRouteRuleBackendRef_IsAIServiceBackend(t *testing.T) {
	tests := []struct {
		name     string
		ref      *AIGatewayRouteRuleBackendRef
		expected bool
	}{
		{
			name: "AIServiceBackend reference (no group/kind)",
			ref: &AIGatewayRouteRuleBackendRef{
				Name: "test-backend",
			},
			expected: true,
		},
		{
			name: "InferencePool reference",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:  "test-pool",
				Group: new(inferencePoolGroup),
				Kind:  new(inferencePoolKind),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.ref.IsAIServiceBackend()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAIGatewayRouteRule_HasInferencePoolBackends(t *testing.T) {
	tests := []struct {
		name     string
		rule     *AIGatewayRouteRule
		expected bool
	}{
		{
			name:     "Nil rule",
			rule:     nil,
			expected: false,
		},
		{
			name: "No backends",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{},
			},
			expected: false,
		},
		{
			name: "Only AIServiceBackend references",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{
					{Name: "backend1"},
					{Name: "backend2"},
				},
			},
			expected: false,
		},
		{
			name: "Only InferencePool reference",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{
					{
						Name:  "pool1",
						Group: new(inferencePoolGroup),
						Kind:  new(inferencePoolKind),
					},
				},
			},
			expected: true,
		},
		{
			name: "Mixed references (should not happen due to validation)",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{
					{Name: "backend1"},
					{
						Name:  "pool1",
						Group: new(inferencePoolGroup),
						Kind:  new(inferencePoolKind),
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.rule.HasInferencePoolBackends()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAIGatewayRouteRule_HasAIServiceBackends(t *testing.T) {
	tests := []struct {
		name     string
		rule     *AIGatewayRouteRule
		expected bool
	}{
		{
			name:     "Nil rule",
			rule:     nil,
			expected: false,
		},
		{
			name: "No backends",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{},
			},
			expected: false,
		},
		{
			name: "Only AIServiceBackend references",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{
					{Name: "backend1"},
					{Name: "backend2"},
				},
			},
			expected: true,
		},
		{
			name: "Only InferencePool reference",
			rule: &AIGatewayRouteRule{
				BackendRefs: []AIGatewayRouteRuleBackendRef{
					{
						Name:  "pool1",
						Group: new(inferencePoolGroup),
						Kind:  new(inferencePoolKind),
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.rule.HasAIServiceBackends()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAIGatewayRouteRuleBackendRef_GetNamespace(t *testing.T) {
	tests := []struct {
		name             string
		ref              *AIGatewayRouteRuleBackendRef
		defaultNamespace string
		expected         string
	}{
		{
			name: "No namespace specified - should use default",
			ref: &AIGatewayRouteRuleBackendRef{
				Name: "test-backend",
			},
			defaultNamespace: "default",
			expected:         "default",
		},
		{
			name: "Empty namespace specified - should use default",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:      "test-backend",
				Namespace: ptr.To(gwapiv1.Namespace("")),
			},
			defaultNamespace: "default",
			expected:         "default",
		},
		{
			name: "Specific namespace specified - should use specified namespace",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:      "test-backend",
				Namespace: ptr.To(gwapiv1.Namespace("other-namespace")),
			},
			defaultNamespace: "default",
			expected:         "other-namespace",
		},
		{
			name: "Same namespace as default - should use specified namespace",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:      "test-backend",
				Namespace: ptr.To(gwapiv1.Namespace("default")),
			},
			defaultNamespace: "default",
			expected:         "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.ref.GetNamespace(tt.defaultNamespace)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAIGatewayRouteRuleBackendRef_IsCrossNamespace(t *testing.T) {
	tests := []struct {
		name           string
		ref            *AIGatewayRouteRuleBackendRef
		routeNamespace string
		expected       bool
	}{
		{
			name: "No namespace specified - not cross-namespace",
			ref: &AIGatewayRouteRuleBackendRef{
				Name: "test-backend",
			},
			routeNamespace: "default",
			expected:       false,
		},
		{
			name: "Empty namespace specified - not cross-namespace",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:      "test-backend",
				Namespace: ptr.To(gwapiv1.Namespace("")),
			},
			routeNamespace: "default",
			expected:       false,
		},
		{
			name: "Same namespace as route - not cross-namespace",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:      "test-backend",
				Namespace: ptr.To(gwapiv1.Namespace("default")),
			},
			routeNamespace: "default",
			expected:       false,
		},
		{
			name: "Different namespace from route - is cross-namespace",
			ref: &AIGatewayRouteRuleBackendRef{
				Name:      "test-backend",
				Namespace: ptr.To(gwapiv1.Namespace("other-namespace")),
			},
			routeNamespace: "default",
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.ref.IsCrossNamespace(tt.routeNamespace)
			require.Equal(t, tt.expected, result)
		})
	}
}
