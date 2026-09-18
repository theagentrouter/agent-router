// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"fmt"
	"strings"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	roundrobinv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/round_robin/v3"
	subsetv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/subset/v3"
	matcherv3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// Backend-sticky routing primitive.
//
// Some endpoints (e.g. the OpenAI Files/Batch APIs) must route every id-bearing request back
// to the exact backend that produced the id, even though a route rule's backends are all
// endpoints of a single cluster. This primitive provides that affinity with three cooperating
// pieces, all keyed on internalapi.AIGatewaySelectedBackendMetadataKey:
//
//  1. Each backend's endpoints are tagged (under the "envoy.lb" namespace) with the backend's
//     identity value (see tagLbEndpointWithStickyBackend).
//  2. The cluster's load-balancing policy is wrapped in a subset policy whose selector is that
//     key, so a request can be confined to a single backend's endpoints
//     (see wrapClusterLbPolicyWithStickySubset). When a request carries no selection, the
//     ANY_ENDPOINT fallback makes it behave like normal weighted load balancing.
//  3. Per-backend "sticky" routes are synthesized; each matches on the request's selected_backend
//     dynamic metadata and pins the subset LB to that backend (see synthesizeStickyBackendRoutes).
//
// The router-level ext_proc emits the selected_backend dynamic metadata (and clears the route
// cache) once it decodes the target backend from an opaque id, which triggers a re-match onto
// the corresponding sticky route.

const (
	// subsetLbPolicyName is the well-known name of Envoy's subset load-balancing policy extension.
	subsetLbPolicyName = "envoy.load_balancing_policies.subset"
	// roundRobinLbPolicyName is the well-known name of Envoy's round-robin load-balancing policy extension.
	roundRobinLbPolicyName = "envoy.load_balancing_policies.round_robin"
	// stickyRouteNameInfix marks (and detects) synthesized sticky routes: "<src>/sticky/<ns>.<backend>".
	stickyRouteNameInfix = "/sticky/"
)

// tagLbEndpointWithStickyBackend records the owning backend's identity on an endpoint under the
// "envoy.lb" namespace so the subset load balancer can select it by selected_backend value.
func tagLbEndpointWithStickyBackend(endpoint *endpointv3.LbEndpoint, backendValue string) {
	if endpoint.Metadata == nil {
		endpoint.Metadata = &corev3.Metadata{}
	}
	if endpoint.Metadata.FilterMetadata == nil {
		endpoint.Metadata.FilterMetadata = make(map[string]*structpb.Struct)
	}
	lb, ok := endpoint.Metadata.FilterMetadata[internalapi.EnvoyLbMetadataNamespace]
	if !ok {
		lb = &structpb.Struct{}
		endpoint.Metadata.FilterMetadata[internalapi.EnvoyLbMetadataNamespace] = lb
	}
	if lb.Fields == nil {
		lb.Fields = make(map[string]*structpb.Value)
	}
	lb.Fields[internalapi.AIGatewaySelectedBackendMetadataKey] = structpb.NewStringValue(backendValue)
}

// wrapClusterLbPolicyWithStickySubset wraps a cluster's load-balancing policy in a subset policy
// keyed on selected_backend, delegating endpoint-picking within a subset to the cluster's existing
// typed policy (or round robin if none). It is idempotent: a cluster already wrapped is left as is.
func wrapClusterLbPolicyWithStickySubset(cluster *clusterv3.Cluster) error {
	if existing := cluster.LoadBalancingPolicy; existing != nil && len(existing.Policies) > 0 {
		if tec := existing.Policies[0].GetTypedExtensionConfig(); tec != nil && tec.GetName() == subsetLbPolicyName {
			return nil // Already wrapped.
		}
	}

	// The subset LB delegates endpoint-picking within the chosen subset to a child policy.
	// Reuse the cluster's existing typed policy when present; otherwise default to round robin.
	childPolicy := cluster.LoadBalancingPolicy
	if childPolicy == nil || len(childPolicy.Policies) == 0 {
		rrAny, err := toAny(&roundrobinv3.RoundRobin{})
		if err != nil {
			return fmt.Errorf("failed to marshal RoundRobin to Any: %w", err)
		}
		childPolicy = &clusterv3.LoadBalancingPolicy{
			Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
				TypedExtensionConfig: &corev3.TypedExtensionConfig{
					Name:        roundRobinLbPolicyName,
					TypedConfig: rrAny,
				},
			}},
		}
	}

	subsetAny, err := toAny(&subsetv3.Subset{
		// No selection metadata on a request => fall back to load balancing across all endpoints.
		FallbackPolicy: subsetv3.Subset_ANY_ENDPOINT,
		SubsetSelectors: []*subsetv3.Subset_LbSubsetSelector{
			{Keys: []string{internalapi.AIGatewaySelectedBackendMetadataKey}},
		},
		LocalityWeightAware: true,
		ScaleLocalityWeight: true,
		SubsetLbPolicy:      childPolicy,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal Subset to Any: %w", err)
	}

	cluster.LoadBalancingPolicy = &clusterv3.LoadBalancingPolicy{
		Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
			TypedExtensionConfig: &corev3.TypedExtensionConfig{
				Name:        subsetLbPolicyName,
				TypedConfig: subsetAny,
			},
		}},
	}
	return nil
}

// synthesizeStickyBackendRoutes adds, for every backend of each AI Gateway route in the virtual
// host, a cloned "sticky" route gated on the selected_backend dynamic metadata and pinned to that
// backend's endpoint subset. stickyBackends maps a cluster name to the backend identity values
// (SelectedBackendMetadataValue) of the endpoints it contains.
//
// Sticky routes are prepended so that, after the router-level ext_proc emits selected_backend and
// clears the route cache, the re-match lands on the pinned route. The function is idempotent.
func synthesizeStickyBackendRoutes(vh *routev3.VirtualHost, stickyBackends map[string][]string) {
	if len(stickyBackends) == 0 {
		return
	}
	// Track existing route names so re-running over the same virtual host does not duplicate
	// sticky routes (idempotency).
	existing := make(map[string]struct{}, len(vh.Routes))
	for _, r := range vh.Routes {
		existing[r.GetName()] = struct{}{}
	}
	var sticky []*routev3.Route
	for _, route := range vh.Routes {
		if isStickyBackendRoute(route) {
			continue // Already synthesized.
		}
		ra := route.GetRoute()
		if ra == nil {
			continue
		}
		backends := stickyBackends[ra.GetCluster()]
		// A cluster with no recorded backends (e.g. a non-AI-Gateway route) cannot be pinned.
		// A sticky route is synthesized even for a single backend: requests that route purely by
		// selected_backend (e.g. the Files API id-bearing requests) carry no model header, so the
		// original model-matched route would not match them after the route cache is cleared —
		// the sticky route is the only route they can match.
		if len(backends) == 0 {
			continue
		}
		for _, backendValue := range backends {
			name := route.GetName() + stickyRouteNameInfix + backendValue
			if _, ok := existing[name]; ok {
				continue // Already synthesized for this backend.
			}
			existing[name] = struct{}{}
			sticky = append(sticky, newStickyBackendRoute(route, backendValue))
		}
	}
	if len(sticky) > 0 {
		vh.Routes = append(sticky, vh.Routes...)
	}
}

// newStickyBackendRoute clones src into a route that matches only when the request's selected_backend
// dynamic metadata equals backendValue, and pins the subset LB to that backend.
func newStickyBackendRoute(src *routev3.Route, backendValue string) *routev3.Route {
	r, _ := proto.Clone(src).(*routev3.Route)
	r.Name = src.GetName() + stickyRouteNameInfix + backendValue

	// Keep the original path match but drop header/query matches: selection is purely by path +
	// decoded backend, and the original (non-sticky) route still covers any header/query cases.
	if r.Match == nil {
		r.Match = &routev3.RouteMatch{}
	}
	r.Match.Headers = nil
	r.Match.QueryParameters = nil
	r.Match.DynamicMetadata = []*matcherv3.MetadataMatcher{{
		Filter: internalapi.AIGatewayFilterMetadataNamespace,
		Path: []*matcherv3.MetadataMatcher_PathSegment{{
			Segment: &matcherv3.MetadataMatcher_PathSegment_Key{Key: internalapi.AIGatewaySelectedBackendMetadataKey},
		}},
		Value: &matcherv3.ValueMatcher{
			MatchPattern: &matcherv3.ValueMatcher_StringMatch{
				StringMatch: &matcherv3.StringMatcher{
					MatchPattern: &matcherv3.StringMatcher_Exact{Exact: backendValue},
				},
			},
		},
	}}

	if ra := r.GetRoute(); ra != nil {
		ra.MetadataMatch = &corev3.Metadata{
			FilterMetadata: map[string]*structpb.Struct{
				internalapi.EnvoyLbMetadataNamespace: {
					Fields: map[string]*structpb.Value{
						internalapi.AIGatewaySelectedBackendMetadataKey: structpb.NewStringValue(backendValue),
					},
				},
			},
		}
	}
	return r
}

// isStickyBackendRoute reports whether a route was synthesized by synthesizeStickyBackendRoutes.
func isStickyBackendRoute(route *routev3.Route) bool {
	return strings.Contains(route.GetName(), stickyRouteNameInfix)
}

// collectStickyBackends builds a map from cluster name to the distinct backend identity values
// (selected_backend) tagged on its endpoints by tagLbEndpointWithStickyBackend. It is the input to
// synthesizeStickyBackendRoutes, derived purely from the clusters already modified in this pass.
func collectStickyBackends(clusters []*clusterv3.Cluster) map[string][]string {
	result := make(map[string][]string)
	for _, cluster := range clusters {
		if cluster.LoadAssignment == nil {
			continue
		}
		var values []string
		seen := make(map[string]struct{})
		for _, locality := range cluster.LoadAssignment.Endpoints {
			for _, ep := range locality.LbEndpoints {
				v := stickyBackendValueOfEndpoint(ep)
				if v == "" {
					continue
				}
				if _, ok := seen[v]; ok {
					continue
				}
				seen[v] = struct{}{}
				values = append(values, v)
			}
		}
		if len(values) > 0 {
			result[cluster.Name] = values
		}
	}
	return result
}

// stickyBackendValueOfEndpoint returns the selected_backend value tagged on an endpoint, or "".
func stickyBackendValueOfEndpoint(ep *endpointv3.LbEndpoint) string {
	if ep.Metadata == nil {
		return ""
	}
	lb, ok := ep.Metadata.FilterMetadata[internalapi.EnvoyLbMetadataNamespace]
	if !ok || lb.Fields == nil {
		return ""
	}
	return lb.Fields[internalapi.AIGatewaySelectedBackendMetadataKey].GetStringValue()
}
