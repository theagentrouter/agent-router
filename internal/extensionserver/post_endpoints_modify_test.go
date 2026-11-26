// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"errors"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
					Endpoints:   []*endpointv3.LocalityLbEndpoints{ipLocality("127.0.0.1", 9101), ipLocality("127.0.0.1", 9102)},
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

func TestSingleBackendRefIndex(t *testing.T) {
	zero := ptr.To[int32](0)
	for _, tc := range []struct {
		name            string
		refs            []aigv1b1.AIGatewayRouteRuleBackendRef
		backendRefIndex int
		wantIndex       int
		wantOK          bool
	}{
		{name: "no backendRefs", backendRefIndex: noBackendRefIndex, wantIndex: -1},
		{name: "all zero weight", refs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Weight: zero}}, backendRefIndex: noBackendRefIndex, wantIndex: -1},
		{name: "one", refs: []aigv1b1.AIGatewayRouteRuleBackendRef{{}}, backendRefIndex: noBackendRefIndex, wantIndex: 0, wantOK: true},
		{name: "one live", refs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Weight: zero}, {}}, backendRefIndex: noBackendRefIndex, wantIndex: 1, wantOK: true},
		{name: "several", refs: []aigv1b1.AIGatewayRouteRuleBackendRef{{}, {}}, backendRefIndex: noBackendRefIndex},
		{name: "per-backend cluster", refs: []aigv1b1.AIGatewayRouteRuleBackendRef{{}, {}}, backendRefIndex: 1, wantIndex: 1, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, ok := singleBackendRefIndex(&aigv1b1.AIGatewayRouteRule{BackendRefs: tc.refs}, tc.backendRefIndex)
			require.Equal(t, tc.wantOK, ok)
			if ok || tc.wantIndex == -1 {
				require.Equal(t, tc.wantIndex, index)
			}
		})
	}
}
