// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	subsetv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/subset/v3"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// taggedEndpoint returns a single-endpoint locality tagged with the given sticky backend value.
func taggedEndpoint(backendValue string) *endpointv3.LocalityLbEndpoints {
	ep := &endpointv3.LbEndpoint{}
	tagLbEndpointWithStickyBackend(ep, backendValue)
	return &endpointv3.LocalityLbEndpoints{LbEndpoints: []*endpointv3.LbEndpoint{ep}}
}

func TestTagLbEndpointWithStickyBackend(t *testing.T) {
	ep := &endpointv3.LbEndpoint{}
	tagLbEndpointWithStickyBackend(ep, "ns.aaa")

	lb := ep.Metadata.FilterMetadata[internalapi.EnvoyLbMetadataNamespace]
	require.NotNil(t, lb)
	require.Equal(t, "ns.aaa", lb.Fields[internalapi.AIGatewaySelectedBackendMetadataKey].GetStringValue())

	// Tagging again with a different value overwrites and does not clobber other namespaces.
	ep.Metadata.FilterMetadata["other.ns"] = nil
	tagLbEndpointWithStickyBackend(ep, "ns.bbb")
	require.Equal(t, "ns.bbb", ep.Metadata.FilterMetadata[internalapi.EnvoyLbMetadataNamespace].
		Fields[internalapi.AIGatewaySelectedBackendMetadataKey].GetStringValue())
	require.Contains(t, ep.Metadata.FilterMetadata, "other.ns")
}

func TestWrapClusterLbPolicyWithStickySubset(t *testing.T) {
	t.Run("nil policy defaults child to round robin", func(t *testing.T) {
		c := &clusterv3.Cluster{Name: "c"}
		require.NoError(t, wrapClusterLbPolicyWithStickySubset(c))

		require.Len(t, c.LoadBalancingPolicy.Policies, 1)
		outer := c.LoadBalancingPolicy.Policies[0].TypedExtensionConfig
		require.Equal(t, subsetLbPolicyName, outer.Name)

		subset := &subsetv3.Subset{}
		require.NoError(t, outer.TypedConfig.UnmarshalTo(subset))
		require.Equal(t, subsetv3.Subset_ANY_ENDPOINT, subset.FallbackPolicy)
		require.Len(t, subset.SubsetSelectors, 1)
		require.Equal(t, []string{internalapi.AIGatewaySelectedBackendMetadataKey}, subset.SubsetSelectors[0].Keys)
		require.True(t, subset.LocalityWeightAware)
		require.True(t, subset.ScaleLocalityWeight)
		require.Equal(t, roundRobinLbPolicyName, subset.SubsetLbPolicy.Policies[0].TypedExtensionConfig.Name)
	})

	t.Run("existing policy becomes child", func(t *testing.T) {
		existing := &clusterv3.LoadBalancingPolicy{
			Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
				TypedExtensionConfig: &corev3.TypedExtensionConfig{Name: "envoy.load_balancing_policies.least_request"},
			}},
		}
		c := &clusterv3.Cluster{Name: "c", LoadBalancingPolicy: existing}
		require.NoError(t, wrapClusterLbPolicyWithStickySubset(c))

		subset := &subsetv3.Subset{}
		require.NoError(t, c.LoadBalancingPolicy.Policies[0].TypedExtensionConfig.TypedConfig.UnmarshalTo(subset))
		require.Equal(t, "envoy.load_balancing_policies.least_request",
			subset.SubsetLbPolicy.Policies[0].TypedExtensionConfig.Name)
	})

	t.Run("idempotent", func(t *testing.T) {
		c := &clusterv3.Cluster{Name: "c"}
		require.NoError(t, wrapClusterLbPolicyWithStickySubset(c))
		first := c.LoadBalancingPolicy
		require.NoError(t, wrapClusterLbPolicyWithStickySubset(c))
		// Same outer policy object/name; not re-wrapped into a nested subset.
		require.Equal(t, subsetLbPolicyName, c.LoadBalancingPolicy.Policies[0].TypedExtensionConfig.Name)
		subset := &subsetv3.Subset{}
		require.NoError(t, c.LoadBalancingPolicy.Policies[0].TypedExtensionConfig.TypedConfig.UnmarshalTo(subset))
		require.Equal(t, roundRobinLbPolicyName, subset.SubsetLbPolicy.Policies[0].TypedExtensionConfig.Name)
		require.Same(t, first, c.LoadBalancingPolicy)
	})
}

func TestCollectStickyBackends(t *testing.T) {
	clusters := []*clusterv3.Cluster{
		{
			Name: "httproute/ns/r/rule/0",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				Endpoints: []*endpointv3.LocalityLbEndpoints{
					taggedEndpoint("ns.aaa"),
					taggedEndpoint("ns.aaa"), // duplicate value -> deduped
					taggedEndpoint("ns.bbb"),
				},
			},
		},
		{Name: "no-load-assignment"},
		{
			Name: "untagged",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				Endpoints: []*endpointv3.LocalityLbEndpoints{{LbEndpoints: []*endpointv3.LbEndpoint{{}}}},
			},
		},
	}

	got := collectStickyBackends(clusters)
	require.Equal(t, map[string][]string{"httproute/ns/r/rule/0": {"ns.aaa", "ns.bbb"}}, got)
}

// newClusterRoute builds a route with a path prefix, header match, and a cluster action.
func newClusterRoute(name, cluster string) *routev3.Route {
	return &routev3.Route{
		Name: name,
		Match: &routev3.RouteMatch{
			PathSpecifier:   &routev3.RouteMatch_Prefix{Prefix: "/v1/files"},
			Headers:         []*routev3.HeaderMatcher{{Name: "x-test"}},
			QueryParameters: []*routev3.QueryParameterMatcher{{Name: "q"}},
		},
		Action: &routev3.Route_Route{Route: &routev3.RouteAction{
			ClusterSpecifier: &routev3.RouteAction_Cluster{Cluster: cluster},
		}},
	}
}

func TestSynthesizeStickyBackendRoutes(t *testing.T) {
	t.Run("two backends produce pinned routes prepended", func(t *testing.T) {
		vh := &routev3.VirtualHost{
			Name:   "vh",
			Routes: []*routev3.Route{newClusterRoute("route-0", "httproute/ns/r/rule/0")},
		}
		synthesizeStickyBackendRoutes(vh, map[string][]string{"httproute/ns/r/rule/0": {"ns.aaa", "ns.bbb"}})

		require.Len(t, vh.Routes, 3)
		require.Equal(t, "route-0/sticky/ns.aaa", vh.Routes[0].Name)
		require.Equal(t, "route-0/sticky/ns.bbb", vh.Routes[1].Name)
		require.Equal(t, "route-0", vh.Routes[2].Name) // original retained, last.

		sticky := vh.Routes[0]
		// Header/query matches stripped; path retained.
		require.Nil(t, sticky.Match.Headers)
		require.Nil(t, sticky.Match.QueryParameters)
		require.Equal(t, "/v1/files", sticky.Match.GetPrefix())
		// Dynamic metadata matcher gates on selected_backend.
		require.Len(t, sticky.Match.DynamicMetadata, 1)
		dm := sticky.Match.DynamicMetadata[0]
		require.Equal(t, internalapi.AIGatewayFilterMetadataNamespace, dm.Filter)
		require.Equal(t, internalapi.AIGatewaySelectedBackendMetadataKey, dm.Path[0].GetKey())
		require.Equal(t, "ns.aaa", dm.Value.GetStringMatch().GetExact())
		// MetadataMatch pins the subset LB.
		require.Equal(t, "ns.aaa", sticky.GetRoute().MetadataMatch.
			FilterMetadata[internalapi.EnvoyLbMetadataNamespace].
			Fields[internalapi.AIGatewaySelectedBackendMetadataKey].GetStringValue())
		// Original route is untouched.
		require.NotNil(t, vh.Routes[2].Match.Headers)
	})

	t.Run("single backend is pinned", func(t *testing.T) {
		// Even a single backend gets a sticky route: id-bearing requests (e.g. Files API)
		// route purely by selected_backend and carry no model header, so without the sticky
		// route they would match no route after the route cache is cleared.
		vh := &routev3.VirtualHost{
			Name:   "vh",
			Routes: []*routev3.Route{newClusterRoute("route-0", "httproute/ns/r/rule/0")},
		}
		synthesizeStickyBackendRoutes(vh, map[string][]string{"httproute/ns/r/rule/0": {"ns.aaa"}})
		require.Len(t, vh.Routes, 2)
		require.Equal(t, "route-0/sticky/ns.aaa", vh.Routes[0].Name)
		require.Equal(t, "route-0", vh.Routes[1].Name)
	})

	t.Run("no recorded backends is not pinned", func(t *testing.T) {
		vh := &routev3.VirtualHost{
			Name:   "vh",
			Routes: []*routev3.Route{newClusterRoute("route-0", "httproute/ns/r/rule/0")},
		}
		// The cluster of the route is not present in the sticky map.
		synthesizeStickyBackendRoutes(vh, map[string][]string{"httproute/ns/other/rule/0": {"ns.aaa", "ns.bbb"}})
		require.Len(t, vh.Routes, 1)
	})

	t.Run("non-cluster route skipped", func(t *testing.T) {
		vh := &routev3.VirtualHost{
			Name: "vh",
			Routes: []*routev3.Route{{
				Name:   "redirect",
				Action: &routev3.Route_Redirect{Redirect: &routev3.RedirectAction{}},
			}},
		}
		synthesizeStickyBackendRoutes(vh, map[string][]string{"httproute/ns/r/rule/0": {"ns.aaa", "ns.bbb"}})
		require.Len(t, vh.Routes, 1)
	})

	t.Run("idempotent", func(t *testing.T) {
		vh := &routev3.VirtualHost{
			Name:   "vh",
			Routes: []*routev3.Route{newClusterRoute("route-0", "httproute/ns/r/rule/0")},
		}
		backends := map[string][]string{"httproute/ns/r/rule/0": {"ns.aaa", "ns.bbb"}}
		synthesizeStickyBackendRoutes(vh, backends)
		synthesizeStickyBackendRoutes(vh, backends)
		require.Len(t, vh.Routes, 3) // No duplicates on second run.
	})
}
