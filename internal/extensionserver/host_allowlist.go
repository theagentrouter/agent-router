// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"fmt"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	hostallowlistv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/host_allowlist/v3"
	leastrequestv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/least_request/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/translator"
)

const (
	// hostAllowlistPolicyName is the Envoy extension name for the host_allowlist load balancing
	// policy (https://github.com/envoyproxy/envoy/pull/47805). It wraps a child picker and, per
	// request, excludes hosts whose identity is not present in the rate limit service's advertised
	// allowlist, returning 429 when every candidate host is excluded.
	hostAllowlistPolicyName = "envoy.load_balancing_policies.host_allowlist"
	// leastRequestPolicyName is the Envoy extension name for the least_request load balancing policy.
	// It is used as the host_allowlist child policy fallback when a cluster carries no explicit policy;
	// it matches the default Envoy Gateway applies to AI Gateway clusters.
	leastRequestPolicyName = "envoy.load_balancing_policies.least_request"
	// hostAllowlistCandidatesMetadataKey is the key, under the rate limit filter's dynamic metadata
	// namespace, that holds the list of live (non-exhausted) backend groups the rate limit service
	// advertises. Each entry is a struct of {backend_name, model_name_override}. It must match the key
	// emitted by the rate limit service (envoyproxy/ratelimit#1243).
	hostAllowlistCandidatesMetadataKey = "passedBackends"
)

// marshalAny is a variable so tests can exercise the defensive error paths around protobuf
// marshaling. In production it always points to anypb.New.
var marshalAny = anypb.New

// setEndpointMetadataQuotaRouting stamps the short "namespace/name" backend identity and the backend's
// model name override onto endpoint-level metadata under the AI Gateway namespace. These are the
// primary (backend_name) and secondary (model_name_override) host keys the Envoy host_allowlist load
// balancing policy matches against the rate limit service's passedBackends candidate list to exclude
// quota-exhausted backends during host selection.
//
// backend_name uses the short "namespace/name" form so it equals the rate limit service's backend_name
// descriptor value (translator.BackendDomainValue); the host_allowlist primary match is hierarchical,
// so the exact-equal case always matches. model_name_override is left empty when the backend has no
// override, which the host_allowlist policy treats as matching any candidate model.
func setEndpointMetadataQuotaRouting(endpoint *endpointv3.LbEndpoint, namespace, backendName, modelNameOverride string) {
	m := ensureEndpointAIGatewayMetadata(endpoint)
	m.Fields[internalapi.InternalMetadataShortBackendNameKey] = structpb.NewStringValue(
		translator.BackendDomainValue(namespace, backendName),
	)
	m.Fields[internalapi.InternalMetadataModelNameOverrideKey] = structpb.NewStringValue(modelNameOverride)
}

// wrapClusterLBInHostAllowlist wraps the cluster's existing load balancing policy in the host_allowlist
// policy so Envoy, during host selection, excludes backends the rate limit service reports as
// quota-exhausted. The existing picker becomes the child_policy, so weighted/least-request behavior is
// preserved for the hosts that remain allowlisted. It is a no-op for clusters that already carry the
// policy (idempotent across reconciles) and for CLUSTER_PROVIDED clusters (e.g. InferencePool), which
// delegate host selection and carry no endpoint-level backend metadata to match against.
func wrapClusterLBInHostAllowlist(cluster *clusterv3.Cluster) error {
	if cluster.GetLbPolicy() == clusterv3.Cluster_CLUSTER_PROVIDED {
		return nil
	}
	if existing := cluster.GetLoadBalancingPolicy(); existing != nil {
		for _, p := range existing.GetPolicies() {
			if p.GetTypedExtensionConfig().GetName() == hostAllowlistPolicyName {
				return nil
			}
		}
	}

	child := cluster.LoadBalancingPolicy
	if child == nil {
		lr, err := marshalAny(&leastrequestv3.LeastRequest{})
		if err != nil {
			return fmt.Errorf("failed to marshal default least_request policy: %w", err)
		}
		child = &clusterv3.LoadBalancingPolicy{
			Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
				TypedExtensionConfig: &corev3.TypedExtensionConfig{
					Name:        leastRequestPolicyName,
					TypedConfig: lr,
				},
			}},
		}
	}

	ha := &hostallowlistv3.HostAllowlist{
		// Envoy's ratelimit filter stores the rate limit service's response dynamic metadata under its
		// canonical name (it does not use the per-instance filter name), so read from there.
		MetadataNamespace:     wellknown.HTTPRateLimit,
		MetadataKey:           hostAllowlistCandidatesMetadataKey,
		HostMetadataNamespace: internalapi.InternalEndpointMetadataNamespace,
		// One shared key list names BOTH the host endpoint metadata fields and the candidate struct
		// fields the rate limit service emits. backend_name is the primary (hierarchical) key;
		// model_name_override is the secondary (exact, empty-matches-any) key.
		HostMetadataKeys: []string{
			internalapi.InternalMetadataShortBackendNameKey,
			internalapi.InternalMetadataModelNameOverrideKey,
		},
		// Fail open: when the rate limit metadata is absent (RLS unavailable, or route without a quota),
		// do not strand the request — fall back to the child policy across all hosts.
		FailClosedOnMissingMetadata: false,
		ChildPolicy:                 child,
	}
	haAny, err := marshalAny(ha)
	if err != nil {
		return fmt.Errorf("failed to marshal HostAllowlist policy: %w", err)
	}
	// Envoy selects the first policy in the list whose extension it supports. List host_allowlist
	// first, then the original picker as a fallback sibling: a data plane that predates the
	// host_allowlist extension (envoyproxy/envoy#47805 — e.g. the Envoy shipped with Envoy Gateway
	// v1.8/v1.9) silently falls back to the original picker instead of rejecting the whole cluster
	// (which would 503 the route and break plain quota enforcement). On a data plane that supports
	// host_allowlist the fallback is never reached, and quota-aware failover works as intended.
	policies := []*clusterv3.LoadBalancingPolicy_Policy{{
		TypedExtensionConfig: &corev3.TypedExtensionConfig{
			Name:        hostAllowlistPolicyName,
			TypedConfig: haAny,
		},
	}}
	policies = append(policies, child.GetPolicies()...)
	cluster.LoadBalancingPolicy = &clusterv3.LoadBalancingPolicy{Policies: policies}
	return nil
}
