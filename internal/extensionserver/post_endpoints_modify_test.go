// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func newTestEndpointsServer(t *testing.T, c client.Client) *Server {
	t.Helper()
	s, err := New(c, logr.FromSlogHandler(slog.NewTextHandler(io.Discard, nil)), udsPath, false, nil, nil,
		"envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	return s
}

// ipLocality returns an EDS locality with one static IP endpoint, as Envoy Gateway builds for a
// Backend `ip` endpoint.
func ipLocality(addr string, port uint32) *endpointv3.LocalityLbEndpoints {
	return &endpointv3.LocalityLbEndpoints{LbEndpoints: []*endpointv3.LbEndpoint{{
		HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
			Address: &corev3.Address{Address: &corev3.Address_SocketAddress{SocketAddress: &corev3.SocketAddress{
				Address: addr, PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: port},
			}}},
		}},
	}}}
}

// egLocality returns a locality as Envoy Gateway v1.8+ builds it for an EG Backend: its metadata
// names the Backend it was built from. zone is set for the per-zone localities of preferLocal and
// weightedZones.
func egLocality(backendNamespace, backendName, zone, addr string, port uint32) *endpointv3.LocalityLbEndpoints {
	l := ipLocality(addr, port)
	l.Locality = &corev3.Locality{Zone: zone}
	l.Metadata = &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
		envoyGatewayMetadataNamespace: {Fields: map[string]*structpb.Value{
			envoyGatewayMetadataResourcesKey: structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{
				structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
					"kind":      structpb.NewStringValue("Backend"),
					"name":      structpb.NewStringValue(backendName),
					"namespace": structpb.NewStringValue(backendNamespace),
				}}),
			}}),
		}},
	}}
	return l
}

// regionLocality returns a non-zonal locality as Envoy Gateway v1.8+ builds it for the HTTPRoute
// backendRef at httpRouteBackendIndex: its Region names the backendRef, and its weight and priority are
// the backendRef's.
func regionLocality(rule string, httpRouteBackendIndex int, backendNamespace, backendName string, weight, priority uint32) *endpointv3.LocalityLbEndpoints {
	l := egLocality(backendNamespace, backendName, "", "10.0.0.1", 80)
	l.Locality = &corev3.Locality{Region: fmt.Sprintf("%s/backend/%d", rule, httpRouteBackendIndex)}
	l.LoadBalancingWeight = wrapperspb.UInt32(weight)
	l.Priority = priority
	return l
}

// createAIServiceBackend creates an AIServiceBackend referencing the EG Backend of the same name.
func createAIServiceBackend(t *testing.T, c client.Client, namespace, name, backendName string) {
	t.Helper()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{BackendRef: gwapiv1.BackendObjectReference{
			Name:  gwapiv1.ObjectName(backendName),
			Kind:  ptr.To[gwapiv1.Kind]("Backend"),
			Group: ptr.To[gwapiv1.Group]("gateway.envoyproxy.io"),
		}},
	}))
}

// createHTTPRoute creates the HTTPRoute the controller generates for an AIGatewayRoute, with one
// rule per element of rules, each listing the given EG Backends.
func createHTTPRoute(t *testing.T, c client.Client, namespace, name string, rules ...[]string) {
	t.Helper()
	route := &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	for _, backends := range rules {
		var rule gwapiv1.HTTPRouteRule
		for _, b := range backends {
			rule.BackendRefs = append(rule.BackendRefs, gwapiv1.HTTPBackendRef{BackendRef: gwapiv1.BackendRef{
				BackendObjectReference: gwapiv1.BackendObjectReference{
					Name:  gwapiv1.ObjectName(b),
					Kind:  ptr.To[gwapiv1.Kind]("Backend"),
					Group: ptr.To[gwapiv1.Group]("gateway.envoyproxy.io"),
				},
			}})
		}
		route.Spec.Rules = append(route.Spec.Rules, rule)
	}
	require.NoError(t, c.Create(t.Context(), route))
}

// endpointBackendName returns the per-route-rule backend name the upstream ext_proc filter reads
// from an endpoint (XDSUpstreamHostMetadataBackendNamePath).
func endpointBackendName(ep *endpointv3.LbEndpoint) string {
	return ep.GetMetadata().GetFilterMetadata()[internalapi.InternalEndpointMetadataNamespace].
		GetFields()[internalapi.InternalMetadataBackendNameKey].GetStringValue()
}

// clusterBackendName returns the backend name the upstream ext_proc filter falls back to when an
// endpoint has none (XDSClusterMetadataBackendNamePath).
func clusterBackendName(c *clusterv3.Cluster) string {
	return c.GetMetadata().GetFilterMetadata()[internalapi.InternalEndpointMetadataNamespace].
		GetFields()[internalapi.InternalMetadataBackendNameKey].GetStringValue()
}

// TestEDSClusterWithSeveralBackends covers an AIGatewayRoute rule with two AIServiceBackends whose
// Backends use static IP endpoints. Envoy Gateway translates the rule into a single EDS cluster (no
// inline LoadAssignment) and a separate ClusterLoadAssignment with one locality per backendRef.
// Every endpoint must resolve to its own backend, not to one named on the shared cluster.
func TestEDSClusterWithSeveralBackends(t *testing.T) {
	for _, tc := range []struct {
		name        string
		backendRefs []aigv1b1.AIGatewayRouteRuleBackendRef
	}{
		{
			name: "weights",
			backendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "backend-a", Weight: ptr.To[int32](50)},
				{Name: "backend-b", Weight: ptr.To[int32](50)},
			},
		},
		{
			name: "priorities",
			backendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "backend-a", Priority: ptr.To[uint32](0)},
				{Name: "backend-b", Priority: ptr.To[uint32](1)},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient()
			require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "repro", Namespace: "default"},
				Spec:       aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{BackendRefs: tc.backendRefs}}},
			}))
			createAIServiceBackend(t, c, "default", "backend-a", "backend-a")
			createAIServiceBackend(t, c, "default", "backend-b", "backend-b")
			createHTTPRoute(t, c, "default", "repro", []string{"backend-a", "backend-b"})
			s := newTestEndpointsServer(t, c)

			const clusterName = "httproute/default/repro/rule/0"
			cluster := &clusterv3.Cluster{
				Name:                 clusterName,
				ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_EDS},
				EdsClusterConfig:     &clusterv3.Cluster_EdsClusterConfig{ServiceName: clusterName},
			}
			require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))
			require.Empty(t, clusterBackendName(cluster),
				"a cluster shared by several backends must not name one of them for all its endpoints")

			resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
				LoadAssignment: &endpointv3.ClusterLoadAssignment{
					ClusterName: clusterName,
					Endpoints: []*endpointv3.LocalityLbEndpoints{
						egLocality("default", "backend-a", "", "127.0.0.1", 9101),
						egLocality("default", "backend-b", "", "127.0.0.1", 9102),
					},
				},
			})
			require.NoError(t, err)
			cla := resp.GetLoadAssignment()
			require.Len(t, cla.Endpoints, 2)
			require.Equal(t, internalapi.PerRouteRuleRefBackendName("default", "backend-a", "repro", 0, 0),
				endpointBackendName(cla.Endpoints[0].LbEndpoints[0]))
			require.Equal(t, internalapi.PerRouteRuleRefBackendName("default", "backend-b", "repro", 0, 1),
				endpointBackendName(cla.Endpoints[1].LbEndpoints[0]))
			if tc.backendRefs[1].Priority != nil {
				require.Equal(t, uint32(0), cla.Endpoints[0].Priority)
				require.Equal(t, uint32(1), cla.Endpoints[1].Priority)
			}
		})
	}
}

func TestPostEndpointsModify(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "aaa"},
				{Name: "disabled", Weight: ptr.To[int32](0)},
				{Name: "bbb"},
			}},
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "pool", Group: ptr.To("inference.networking.k8s.io"), Kind: ptr.To("InferencePool")},
			}},
		}},
	}))
	s := newTestEndpointsServer(t, c)

	t.Run("nil load assignment", func(t *testing.T) {
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{})
		require.NoError(t, err)
		require.Nil(t, resp.GetLoadAssignment())
	})

	unchanged := func(name string) *endpointv3.ClusterLoadAssignment {
		return &endpointv3.ClusterLoadAssignment{ClusterName: name, Endpoints: []*endpointv3.LocalityLbEndpoints{ipLocality("10.0.0.1", 80)}}
	}
	for _, name := range []string{
		"service/ns/foo",                         // not an AIGatewayRoute cluster name
		"httproute/ns/plain-httproute/rule/0",    // no such AIGatewayRoute
		"httproute/ns/myroute/rule/9",            // rule index out of range
		"httproute/ns/myroute/rule/0/backend/99", // backend index out of range
		"httproute/ns/myroute/rule/1",            // InferencePool rule
	} {
		t.Run("unchanged/"+name, func(t *testing.T) {
			cla := unchanged(name)
			resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{LoadAssignment: cla})
			require.NoError(t, err)
			// An empty response keeps Envoy Gateway's LoadAssignment as is, without sending it back.
			require.Nil(t, resp.GetLoadAssignment())
			require.True(t, proto.Equal(unchanged(name), cla))
		})
	}

	t.Run("zero-weight backendRef is skipped", func(t *testing.T) {
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: "httproute/ns/myroute/rule/0",
				Endpoints:   []*endpointv3.LocalityLbEndpoints{ipLocality("10.0.0.1", 80), ipLocality("10.0.0.2", 80)},
			},
		})
		require.NoError(t, err)
		eps := resp.GetLoadAssignment().Endpoints
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 0), endpointBackendName(eps[0].LbEndpoints[0]))
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 0, 2), endpointBackendName(eps[1].LbEndpoints[0]))
	})

	t.Run("per-backend cluster", func(t *testing.T) {
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: "httproute/ns/myroute/rule/0/backend/2",
				Endpoints:   []*endpointv3.LocalityLbEndpoints{ipLocality("10.0.0.2", 80)},
			},
		})
		require.NoError(t, err)
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 0, 2),
			endpointBackendName(resp.GetLoadAssignment().Endpoints[0].LbEndpoints[0]))
	})

	t.Run("get error", func(t *testing.T) {
		failing := newTestEndpointsServer(t, interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return errors.New("boom")
			},
		}))
		_, err := failing.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: unchanged("httproute/ns/myroute/rule/0"),
		})
		require.ErrorContains(t, err, "boom")
	})
}

func TestMaybeModifyCluster_nilLoadAssignmentSingleBackend(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "only"}}},
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "disabled", Weight: ptr.To[int32](0)},
				{Name: "live"},
			}},
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "aaa"}, {Name: "bbb"}}},
		}},
	}))
	for _, name := range []string{"only", "disabled", "live", "aaa", "bbb"} {
		createAIServiceBackend(t, c, "ns", name, name)
	}
	s := newTestEndpointsServer(t, c)

	for _, tc := range []struct {
		cluster  string
		expected string
	}{
		{cluster: "httproute/ns/myroute/rule/0", expected: internalapi.PerRouteRuleRefBackendName("ns", "only", "myroute", 0, 0)},
		{cluster: "httproute/ns/myroute/rule/1", expected: internalapi.PerRouteRuleRefBackendName("ns", "live", "myroute", 1, 1)},
		// A per-backend cluster holds a single backend even when its rule has several.
		{cluster: "httproute/ns/myroute/rule/2/backend/1", expected: internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 2, 1)},
		{cluster: "httproute/ns/myroute/rule/2", expected: ""},
	} {
		t.Run(tc.cluster, func(t *testing.T) {
			cluster := &clusterv3.Cluster{Name: tc.cluster}
			require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))
			require.Equal(t, tc.expected, clusterBackendName(cluster))
		})
	}
}

func TestMatchBackendRefs(t *testing.T) {
	key := func(name string) *backendRefKey {
		return &backendRefKey{kind: "Backend", key: client.ObjectKey{Namespace: "ns", Name: name}}
	}
	emit := func(names ...string) []backendRefKey {
		var keys []backendRefKey
		for _, n := range names {
			keys = append(keys, *key(n))
		}
		return keys
	}
	const amb = ambiguousBackendRef
	for _, tc := range []struct {
		name     string
		resolved []*backendRefKey
		emitted  []backendRefKey
		want     []int
	}{
		{name: "all kept", resolved: []*backendRefKey{key("a"), key("b")}, emitted: emit("a", "b"), want: []int{0, 1}},
		{name: "unresolved left out", resolved: []*backendRefKey{nil, key("a"), key("b")}, emitted: emit("a", "b"), want: []int{1, 2}},
		{name: "shared Backend all kept", resolved: []*backendRefKey{key("a"), key("a")}, emitted: emit("a", "a"), want: []int{0, 1}},
		// One of two backendRefs to the same Backend was left out, e.g. for a missing ReferenceGrant,
		// which the AIServiceBackend lookup cannot see.
		{name: "shared Backend one left out", resolved: []*backendRefKey{key("a"), key("a")}, emitted: emit("a"), want: []int{amb}},
		{name: "shared Backend one left out, others kept", resolved: []*backendRefKey{key("a"), key("b"), key("a")}, emitted: emit("b", "a"), want: []int{1, 2}},
		{name: "revision skew", resolved: []*backendRefKey{key("a")}, emitted: emit("b"), want: []int{amb}},
		{name: "nothing emitted", resolved: []*backendRefKey{key("a")}, want: []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchBackendRefs(tc.resolved, tc.emitted))
		})
	}
}

// TestStampLoadAssignment_localityMetadata checks that localities are mapped to backendRefs by the
// Backend Envoy Gateway records on them, not by position.
func TestStampLoadAssignment_localityMetadata(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{
			// Rule 0: "missing" has no AIServiceBackend, so the controller leaves it out of the
			// HTTPRoute and Envoy Gateway builds no locality for it.
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "missing"},
				{Name: "aaa", Priority: ptr.To[uint32](0)},
				{Name: "bbb", Priority: ptr.To[uint32](1)},
			}},
			// Rule 1: two AIServiceBackends on the same EG Backend.
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "aaa"}, {Name: "aaa-alias"}}},
			// Rule 2: a scale tier failing over to the default tier of the same upstream, after a
			// backendRef the controller left out of the HTTPRoute.
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
				{Name: "missing"},
				{Name: "aaa-scale", Priority: ptr.To[uint32](0)},
				{Name: "aaa-default", Priority: ptr.To[uint32](1)},
			}},
		}},
	}))
	createAIServiceBackend(t, c, "ns", "aaa-scale", "backend-aaa")
	createAIServiceBackend(t, c, "ns", "aaa-default", "backend-aaa")
	createAIServiceBackend(t, c, "ns", "aaa", "backend-aaa")
	createAIServiceBackend(t, c, "ns", "bbb", "backend-bbb")
	createAIServiceBackend(t, c, "ns", "aaa-alias", "backend-aaa")
	s := newTestEndpointsServer(t, c)

	t.Run("zonal localities and a dropped backendRef", func(t *testing.T) {
		// With preferLocal or weightedZones, Envoy Gateway builds one locality per zone and backend.
		cla := &endpointv3.ClusterLoadAssignment{
			ClusterName: "httproute/ns/myroute/rule/0",
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				egLocality("ns", "backend-aaa", "zone-a", "10.0.0.1", 80),
				egLocality("ns", "backend-aaa", "zone-b", "10.0.0.2", 80),
				egLocality("ns", "backend-bbb", "zone-a", "10.0.1.1", 80),
			},
		}
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{LoadAssignment: cla})
		require.NoError(t, err)
		eps := resp.GetLoadAssignment().Endpoints
		aaa := internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 1)
		bbb := internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 0, 2)
		require.Equal(t, aaa, endpointBackendName(eps[0].LbEndpoints[0]))
		require.Equal(t, aaa, endpointBackendName(eps[1].LbEndpoints[0]))
		require.Equal(t, bbb, endpointBackendName(eps[2].LbEndpoints[0]))
		require.Equal(t, []uint32{0, 0, 1}, []uint32{eps[0].Priority, eps[1].Priority, eps[2].Priority})
	})

	t.Run("backendRefs sharing a Backend are told apart by their HTTPRoute backendRef", func(t *testing.T) {
		const rule = "httproute/ns/myroute/rule/2"
		// The HTTPRoute holds aaa-scale at index 0 and aaa-default at index 1; the cluster order does
		// not matter.
		cla := &endpointv3.ClusterLoadAssignment{
			ClusterName: rule,
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				regionLocality(rule, 1, "ns", "backend-aaa", 1, 1),
				regionLocality(rule, 0, "ns", "backend-aaa", 1, 0),
			},
		}
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{LoadAssignment: cla})
		require.NoError(t, err)
		eps := resp.GetLoadAssignment().Endpoints
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "aaa-default", "myroute", 2, 2), endpointBackendName(eps[0].LbEndpoints[0]))
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "aaa-scale", "myroute", 2, 1), endpointBackendName(eps[1].LbEndpoints[0]))
		require.Equal(t, []uint32{1, 0}, []uint32{eps[0].Priority, eps[1].Priority})

		// Same on an inline LoadAssignment.
		cluster := &clusterv3.Cluster{
			Name: rule,
			LoadAssignment: &endpointv3.ClusterLoadAssignment{Endpoints: []*endpointv3.LocalityLbEndpoints{
				regionLocality(rule, 0, "ns", "backend-aaa", 1, 0),
				regionLocality(rule, 1, "ns", "backend-aaa", 1, 1),
			}},
		}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))
		eps = cluster.LoadAssignment.Endpoints
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "aaa-scale", "myroute", 2, 1), endpointBackendName(eps[0].LbEndpoints[0]))
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "aaa-default", "myroute", 2, 2), endpointBackendName(eps[1].LbEndpoints[0]))
	})

	t.Run("backendRefs sharing a Backend reordered while a translation was in flight", func(t *testing.T) {
		const rule = "httproute/ns/myroute/rule/2"
		// Translated from a revision that listed aaa-default (priority 1) before aaa-scale (priority
		// 0); the current one lists aaa-scale first. The Region indices are stale, which the
		// priorities reveal.
		cla := &endpointv3.ClusterLoadAssignment{
			ClusterName: rule,
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				regionLocality(rule, 0, "ns", "backend-aaa", 1, 1),
				regionLocality(rule, 1, "ns", "backend-aaa", 1, 0),
			},
		}
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{LoadAssignment: cla})
		require.NoError(t, err)
		for _, l := range resp.GetLoadAssignment().Endpoints {
			require.Empty(t, endpointBackendName(l.LbEndpoints[0]))
		}
	})

	t.Run("backendRefs sharing a Backend that cannot be told apart are left unstamped", func(t *testing.T) {
		const rule = "httproute/ns/myroute/rule/2"
		malformed := egLocality("ns", "backend-aaa", "", "10.0.0.1", 80)
		malformed.Locality = &corev3.Locality{Region: rule + "/backend/x"}
		for name, tc := range map[string]struct {
			endpoints []*endpointv3.LocalityLbEndpoints
			expected  []string
		}{
			// preferLocal / weightedZones: per-zone localities carry no Region.
			"zonal": {
				endpoints: []*endpointv3.LocalityLbEndpoints{
					egLocality("ns", "backend-aaa", "zone-a", "10.0.0.1", 80),
					egLocality("ns", "backend-aaa", "zone-b", "10.0.0.2", 80),
				},
				expected: []string{"", ""},
			},
			"malformed region": {
				endpoints: []*endpointv3.LocalityLbEndpoints{regionLocality(rule, 0, "ns", "backend-aaa", 1, 0), malformed},
				expected:  []string{internalapi.PerRouteRuleRefBackendName("ns", "aaa-scale", "myroute", 2, 1), ""},
			},
		} {
			t.Run(name, func(t *testing.T) {
				cla := &endpointv3.ClusterLoadAssignment{ClusterName: rule, Endpoints: tc.endpoints}
				resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{LoadAssignment: cla})
				require.NoError(t, err)
				for i, l := range resp.GetLoadAssignment().Endpoints {
					require.Equal(t, tc.expected[i], endpointBackendName(l.LbEndpoints[0]))
				}
			})
		}
	})

	t.Run("ambiguous or unknown Backend is left unstamped", func(t *testing.T) {
		cla := &endpointv3.ClusterLoadAssignment{
			ClusterName: "httproute/ns/myroute/rule/1",
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				egLocality("ns", "backend-aaa", "", "10.0.0.1", 80),
				egLocality("ns", "backend-unknown", "", "10.0.0.9", 80),
			},
		}
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{LoadAssignment: cla})
		require.NoError(t, err)
		for _, l := range resp.GetLoadAssignment().Endpoints {
			require.Empty(t, endpointBackendName(l.LbEndpoints[0]))
		}
	})

	t.Run("inline LoadAssignment", func(t *testing.T) {
		cluster := &clusterv3.Cluster{
			Name: "httproute/ns/myroute/rule/0",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{Endpoints: []*endpointv3.LocalityLbEndpoints{
				egLocality("ns", "backend-bbb", "", "10.0.1.1", 80),
				egLocality("ns", "backend-aaa", "", "10.0.0.1", 80),
			}},
		}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))
		eps := cluster.LoadAssignment.Endpoints
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 0, 2), endpointBackendName(eps[0].LbEndpoints[0]))
		require.Equal(t, internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 1), endpointBackendName(eps[1].LbEndpoints[0]))
	})

	t.Run("AIServiceBackend get error", func(t *testing.T) {
		failing := newTestEndpointsServer(t, interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*aigv1b1.AIServiceBackend); ok {
					return errors.New("boom")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}))
		_, err := failing.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: "httproute/ns/myroute/rule/0",
				Endpoints:   []*endpointv3.LocalityLbEndpoints{egLocality("ns", "backend-aaa", "", "10.0.0.1", 80)},
			},
		})
		require.ErrorContains(t, err, "boom")
	})
}

// TestMaybeModifyCluster_nilLoadAssignmentDroppedBackendRef checks that, without inline endpoints,
// whether a cluster holds a single backend is decided from the backendRefs the controller kept in the
// generated HTTPRoute, not from every backendRef the AIGatewayRoute lists.
func TestMaybeModifyCluster_nilLoadAssignmentDroppedBackendRef(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "aaa"}, {Name: "missing"}}},
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "missing"}, {Name: "bbb"}}},
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "aaa"}, {Name: "bbb"}}},
			{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "aaa"}, {Name: "aaa-alias"}}},
		}},
	}))
	createAIServiceBackend(t, c, "ns", "aaa", "backend-aaa")
	createAIServiceBackend(t, c, "ns", "bbb", "backend-bbb")
	createAIServiceBackend(t, c, "ns", "aaa-alias", "backend-aaa")
	createHTTPRoute(t, c, "ns", "myroute",
		[]string{"backend-aaa"},
		[]string{"backend-bbb"},
		[]string{"backend-aaa", "backend-bbb"},
		[]string{"backend-aaa", "backend-aaa"},
	)
	s := newTestEndpointsServer(t, c)

	for _, tc := range []struct {
		cluster  string
		expected string
	}{
		{cluster: "httproute/ns/myroute/rule/0", expected: internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 0)},
		{cluster: "httproute/ns/myroute/rule/1", expected: internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 1, 1)},
		{cluster: "httproute/ns/myroute/rule/2", expected: ""},
		{cluster: "httproute/ns/myroute/rule/3", expected: ""},
	} {
		t.Run(tc.cluster, func(t *testing.T) {
			cluster := &clusterv3.Cluster{Name: tc.cluster}
			require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))
			require.Equal(t, tc.expected, clusterBackendName(cluster))
		})
	}

	t.Run("HTTPRoute get error", func(t *testing.T) {
		failing := newTestEndpointsServer(t, interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*gwapiv1.HTTPRoute); ok {
					return errors.New("boom")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}))
		require.ErrorContains(t, failing.maybeModifyCluster(t.Context(), &clusterv3.Cluster{Name: "httproute/ns/myroute/rule/0"}, nil), "boom")
	})
}

// TestStampLoadAssignment_rejectedCrossNamespaceBackendRef checks that a cross-namespace backendRef
// that the controller left out of the HTTPRoute for lack of a ReferenceGrant does not make a kept
// backendRef resolving to the same Backend ambiguous.
func TestStampLoadAssignment_rejectedCrossNamespaceBackendRef(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
			{Name: "rejected", Namespace: ptr.To[gwapiv1.Namespace]("other")},
			{Name: "aaa"},
		}}}},
	}))
	createAIServiceBackend(t, c, "ns", "aaa", "backend-aaa")
	// An AIServiceBackend in another namespace using the same EG Backend.
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "rejected", Namespace: "other"},
		Spec: aigv1b1.AIServiceBackendSpec{BackendRef: gwapiv1.BackendObjectReference{
			Name:      "backend-aaa",
			Namespace: ptr.To[gwapiv1.Namespace]("ns"),
			Kind:      ptr.To[gwapiv1.Kind]("Backend"),
			Group:     ptr.To[gwapiv1.Group]("gateway.envoyproxy.io"),
		}},
	}))
	createHTTPRoute(t, c, "ns", "myroute", []string{"backend-aaa"})
	s := newTestEndpointsServer(t, c)
	const rule = "httproute/ns/myroute/rule/0"
	aaa := internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 1)

	t.Run("locality", func(t *testing.T) {
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: rule,
				Endpoints:   []*endpointv3.LocalityLbEndpoints{regionLocality(rule, 0, "ns", "backend-aaa", 1, 0)},
			},
		})
		require.NoError(t, err)
		require.Equal(t, aaa, endpointBackendName(resp.GetLoadAssignment().Endpoints[0].LbEndpoints[0]))
	})

	t.Run("zonal locality", func(t *testing.T) {
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: rule,
				Endpoints:   []*endpointv3.LocalityLbEndpoints{egLocality("ns", "backend-aaa", "zone-a", "10.0.0.1", 80)},
			},
		})
		require.NoError(t, err)
		require.Equal(t, aaa, endpointBackendName(resp.GetLoadAssignment().Endpoints[0].LbEndpoints[0]))
	})

	t.Run("nil LoadAssignment", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: rule}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))
		require.Equal(t, aaa, clusterBackendName(cluster))
	})

	t.Run("granted", func(t *testing.T) {
		// Once a ReferenceGrant allows it, the controller keeps both backendRefs, and a single
		// locality cannot be told apart without its Region.
		require.NoError(t, c.Create(t.Context(), &gwapiv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "other"},
			Spec: gwapiv1b1.ReferenceGrantSpec{
				From: []gwapiv1b1.ReferenceGrantFrom{{Group: "aigateway.envoyproxy.io", Kind: "AIGatewayRoute", Namespace: "ns"}},
				To:   []gwapiv1b1.ReferenceGrantTo{{Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend"}},
			},
		}))
		t.Cleanup(func() {
			_ = c.Delete(context.Background(), &gwapiv1b1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "other"}})
		})
		resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: rule,
				Endpoints:   []*endpointv3.LocalityLbEndpoints{egLocality("ns", "backend-aaa", "zone-a", "10.0.0.1", 80)},
			},
		})
		require.NoError(t, err)
		require.Empty(t, endpointBackendName(resp.GetLoadAssignment().Endpoints[0].LbEndpoints[0]))
	})
}

// TestStampLoadAssignment_httpRoutePriorityFilters checks that the priority a locality is checked
// against comes from the backendPriority HTTPRouteFilters of the generated HTTPRoute.
func TestStampLoadAssignment_httpRoutePriorityFilters(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
			{Name: "aaa-scale", Weight: ptr.To[int32](3)},
			{Name: "aaa-default", Priority: ptr.To[uint32](2)},
		}}}},
	}))
	createAIServiceBackend(t, c, "ns", "aaa-scale", "backend-aaa")
	createAIServiceBackend(t, c, "ns", "aaa-default", "backend-aaa")
	backendRef := func(weight *int32, filters ...gwapiv1.HTTPRouteFilter) gwapiv1.HTTPBackendRef {
		return gwapiv1.HTTPBackendRef{
			BackendRef: gwapiv1.BackendRef{
				BackendObjectReference: gwapiv1.BackendObjectReference{
					Name:  "backend-aaa",
					Kind:  ptr.To[gwapiv1.Kind]("Backend"),
					Group: ptr.To[gwapiv1.Group]("gateway.envoyproxy.io"),
				},
				Weight: weight,
			},
			Filters: filters,
		}
	}
	require.NoError(t, c.Create(t.Context(), &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: gwapiv1.HTTPRouteSpec{Rules: []gwapiv1.HTTPRouteRule{{BackendRefs: []gwapiv1.HTTPBackendRef{
			backendRef(ptr.To[int32](3)),
			backendRef(nil, gwapiv1.HTTPRouteFilter{
				Type: gwapiv1.HTTPRouteFilterExtensionRef,
				ExtensionRef: &gwapiv1.LocalObjectReference{
					Group: "gateway.envoyproxy.io", Kind: "HTTPRouteFilter", Name: "ai-eg-backend-priority-2",
				},
			}),
		}}}},
	}))
	s := newTestEndpointsServer(t, c)
	const rule = "httproute/ns/myroute/rule/0"

	for name, tc := range map[string]struct {
		locality *endpointv3.LocalityLbEndpoints
		expected string
	}{
		"scale":                  {regionLocality(rule, 0, "ns", "backend-aaa", 3, 0), internalapi.PerRouteRuleRefBackendName("ns", "aaa-scale", "myroute", 0, 0)},
		"default":                {regionLocality(rule, 1, "ns", "backend-aaa", 1, 2), internalapi.PerRouteRuleRefBackendName("ns", "aaa-default", "myroute", 0, 1)},
		"stale weight":           {regionLocality(rule, 0, "ns", "backend-aaa", 1, 0), ""},
		"stale priority":         {regionLocality(rule, 1, "ns", "backend-aaa", 1, 0), ""},
		"stale index past range": {regionLocality(rule, 2, "ns", "backend-aaa", 1, 0), ""},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := s.PostEndpointsModify(t.Context(), &egextension.PostEndpointsModifyRequest{
				LoadAssignment: &endpointv3.ClusterLoadAssignment{ClusterName: rule, Endpoints: []*endpointv3.LocalityLbEndpoints{tc.locality}},
			})
			require.NoError(t, err)
			require.Equal(t, tc.expected, endpointBackendName(resp.GetLoadAssignment().Endpoints[0].LbEndpoints[0]))
		})
	}
}
