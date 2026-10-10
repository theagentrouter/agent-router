// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"errors"
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	http11proxyv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/http_11_proxy/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller"
)

const tlsTransportSocketName = "envoy.transport_sockets.tls"

// tlsTransportSocket builds a TLS transport socket carrying the given SNI, as Envoy Gateway would
// attach to an AI Gateway upstream cluster.
func tlsTransportSocket(t *testing.T, sni string) *corev3.TransportSocket {
	return &corev3.TransportSocket{
		Name:       tlsTransportSocketName,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: mustToAny(t, &tlsv3.UpstreamTlsContext{Sni: sni})},
	}
}

// unwrapHTTP11Proxy asserts ts is an http_11_proxy transport socket and returns its config.
func unwrapHTTP11Proxy(t *testing.T, ts *corev3.TransportSocket) *http11proxyv3.Http11ProxyUpstreamTransport {
	t.Helper()
	require.NotNil(t, ts)
	require.Equal(t, http11ProxyTransportSocketName, ts.Name)
	h := &http11proxyv3.Http11ProxyUpstreamTransport{}
	require.NoError(t, ts.GetTypedConfig().UnmarshalTo(h))
	return h
}

// requireProxyAddress asserts the wrapper's default proxy address matches host and port.
func requireProxyAddress(t *testing.T, h *http11proxyv3.Http11ProxyUpstreamTransport, host string, port uint32) {
	t.Helper()
	sa := h.GetDefaultProxyAddress().GetSocketAddress()
	require.NotNil(t, sa)
	require.Equal(t, host, sa.GetAddress())
	require.Equal(t, port, sa.GetPortValue())
}

// newServerWithForwardProxy seeds a fake client with an AIGatewayRoute whose parent Gateway
// references a GatewayConfig. The GatewayConfig configures forwardProxy only when proxyAddr != "".
func newServerWithForwardProxy(t *testing.T, proxyAddr string) *Server {
	t.Helper()
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			// Namespace omitted on purpose: it must default to the route's namespace.
			ParentRefs: []gwapiv1.ParentReference{{Name: "eg-gateway"}},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend-a"}}},
			},
		},
	}))
	require.NoError(t, c.Create(t.Context(), &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "eg-gateway",
			Namespace:   "default",
			Annotations: map[string]string{gatewayConfigAnnotationKey: "gwconfig"},
		},
		Spec: gwapiv1.GatewaySpec{GatewayClassName: "eg"},
	}))
	gc := &aigv1b1.GatewayConfig{ObjectMeta: metav1.ObjectMeta{Name: "gwconfig", Namespace: "default"}}
	if proxyAddr != "" {
		gc.Spec.ForwardProxy = &aigv1b1.GatewayConfigForwardProxy{Address: proxyAddr}
	}
	require.NoError(t, c.Create(t.Context(), gc))
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	return s
}

// clusterWithTLSMatch builds a 5-part AI Gateway cluster with a single TLS transport-socket match
// and a matching load assignment (one endpoint per backendRef).
func clusterWithTLSMatch(t *testing.T, sni string) *clusterv3.Cluster {
	return &clusterv3.Cluster{
		Name:     "httproute/default/myroute/rule/0",
		Metadata: &corev3.Metadata{},
		TransportSocketMatches: []*clusterv3.Cluster_TransportSocketMatch{
			{Name: "httproute/default/myroute/rule/0/tls/0", TransportSocket: tlsTransportSocket(t, sni)},
		},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			Endpoints: []*endpointv3.LocalityLbEndpoints{{LbEndpoints: []*endpointv3.LbEndpoint{{}}}},
		},
	}
}

func TestMaybeModifyCluster_forwardProxy(t *testing.T) {
	t.Run("wraps TLS transport-socket match, preserving inner TLS", func(t *testing.T) {
		s := newServerWithForwardProxy(t, "proxy.corp:3128")
		cluster := clusterWithTLSMatch(t, "api.openai.com")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		requireProxyAddress(t, wrapper, "proxy.corp", 3128)
		// Inner TLS socket preserved with its SNI intact.
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())
		inner := &tlsv3.UpstreamTlsContext{}
		require.NoError(t, wrapper.GetTransportSocket().GetTypedConfig().UnmarshalTo(inner))
		require.Equal(t, "api.openai.com", inner.GetSni())
	})

	t.Run("wraps the singular transport socket", func(t *testing.T) {
		s := newServerWithForwardProxy(t, "10.0.0.9:8080")
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		// Move TLS onto the singular socket and drop the match.
		cluster.TransportSocket = tlsTransportSocket(t, "api.openai.com")
		cluster.TransportSocketMatches = nil

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocket)
		requireProxyAddress(t, wrapper, "10.0.0.9", 8080)
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())
	})

	t.Run("no forwardProxy leaves the socket unchanged", func(t *testing.T) {
		s := newServerWithForwardProxy(t, "") // GatewayConfig without forwardProxy.
		cluster := clusterWithTLSMatch(t, "api.openai.com")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		require.Equal(t, tlsTransportSocketName, cluster.TransportSocketMatches[0].TransportSocket.Name)
	})
}

func TestParseForwardProxyAddress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		addr     string
		wantErr  bool
		wantHost string
		wantPort uint32
	}{
		{name: "hostname", addr: "proxy.corp:3128", wantHost: "proxy.corp", wantPort: 3128},
		{name: "ipv4", addr: "10.0.0.9:8080", wantHost: "10.0.0.9", wantPort: 8080},
		{name: "ipv6", addr: "[::1]:3128", wantHost: "::1", wantPort: 3128},
		{name: "missing port", addr: "proxy.corp", wantErr: true},
		{name: "empty host", addr: ":3128", wantErr: true},
		{name: "zero port", addr: "proxy.corp:0", wantErr: true},
		{name: "non-numeric port", addr: "proxy.corp:http", wantErr: true},
		{name: "out-of-range port", addr: "proxy.corp:70000", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseForwardProxyAddress(tc.addr)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			sa := got.GetSocketAddress()
			require.Equal(t, tc.wantHost, sa.GetAddress())
			require.Equal(t, tc.wantPort, sa.GetPortValue())
		})
	}
}

func TestWrapClusterInForwardProxy(t *testing.T) {
	proxy, err := parseForwardProxyAddress("proxy.corp:3128")
	require.NoError(t, err)

	t.Run("idempotent: wrapping twice does not double-wrap", func(t *testing.T) {
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy))
		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		// The nested socket must be the original TLS socket, not another http_11_proxy wrapper.
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())
	})

	t.Run("no transport socket creates a plaintext-tunnel wrapper", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "httproute/default/myroute/rule/0"}
		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocket)
		requireProxyAddress(t, wrapper, "proxy.corp", 3128)
		require.Nil(t, wrapper.GetTransportSocket()) // nil inner => raw_buffer.
	})

	t.Run("skips transport-socket matches with no socket", func(t *testing.T) {
		cluster := &clusterv3.Cluster{
			Name: "httproute/default/myroute/rule/0",
			TransportSocketMatches: []*clusterv3.Cluster_TransportSocketMatch{
				{Name: "no-socket"}, // nil TransportSocket: skipped.
				{Name: "tls", TransportSocket: tlsTransportSocket(t, "api.openai.com")},
			},
		}
		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy))

		require.Nil(t, cluster.TransportSocketMatches[0].TransportSocket)
		unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[1].TransportSocket)
	})
}

// serverWithObjects builds a Server backed by a fake client seeded with the given objects.
func serverWithObjects(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(controller.Scheme)
	if len(objs) > 0 {
		b = b.WithObjects(objs...)
	}
	s, err := New(b.Build(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	return s
}

func gwGateway(name, configName string) *gwapiv1.Gateway {
	g := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg"},
	}
	if configName != "" {
		g.Annotations = map[string]string{gatewayConfigAnnotationKey: configName}
	}
	return g
}

func gwConfig(name, address string) *aigv1b1.GatewayConfig {
	gc := &aigv1b1.GatewayConfig{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	if address != "" {
		gc.Spec.ForwardProxy = &aigv1b1.GatewayConfigForwardProxy{Address: address}
	}
	return gc
}

func routeWithParents(parents ...gwapiv1.ParentReference) *aigv1b1.AIGatewayRoute {
	return &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec:       aigv1b1.AIGatewayRouteSpec{ParentRefs: parents},
	}
}

func TestResolveForwardProxyAddr(t *testing.T) {
	gwParent := gwapiv1.ParentReference{Name: "eg-gateway"}
	for _, tc := range []struct {
		name  string
		objs  []client.Object
		route *aigv1b1.AIGatewayRoute
		want  string
	}{
		{
			name:  "no parent refs",
			route: routeWithParents(),
			want:  "",
		},
		{
			name:  "gateway not found",
			route: routeWithParents(gwParent),
			want:  "",
		},
		{
			name:  "gateway without annotation",
			objs:  []client.Object{gwGateway("eg-gateway", "")},
			route: routeWithParents(gwParent),
			want:  "",
		},
		{
			name:  "annotation to missing gatewayconfig",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig")},
			route: routeWithParents(gwParent),
			want:  "",
		},
		{
			name:  "gatewayconfig without forwardProxy",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig"), gwConfig("gwconfig", "")},
			route: routeWithParents(gwParent),
			want:  "",
		},
		{
			name:  "configured",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig"), gwConfig("gwconfig", "proxy.corp:3128")},
			route: routeWithParents(gwParent),
			want:  "proxy.corp:3128",
		},
		{
			name:  "non-Gateway parent kind is skipped",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig"), gwConfig("gwconfig", "proxy.corp:3128")},
			route: routeWithParents(gwapiv1.ParentReference{Name: "eg-gateway", Kind: ptr.To[gwapiv1.Kind]("Service")}),
			want:  "",
		},
		{
			name: "conflicting parents: first configured wins",
			objs: []client.Object{
				gwGateway("gw-a", "gc-a"), gwConfig("gc-a", "proxy-a:3128"),
				gwGateway("gw-b", "gc-b"), gwConfig("gc-b", "proxy-b:3128"),
			},
			route: routeWithParents(
				gwapiv1.ParentReference{Name: "gw-a"},
				gwapiv1.ParentReference{Name: "gw-b"},
			),
			want: "proxy-a:3128",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := serverWithObjects(t, tc.objs...)
			got, err := s.resolveForwardProxyAddr(t.Context(), tc.route)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestResolveForwardProxyAddr_getError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(controller.Scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return errors.New("boom")
			},
		}).Build()
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	_, err = s.resolveForwardProxyAddr(t.Context(), routeWithParents(gwapiv1.ParentReference{Name: "eg-gateway"}))
	require.ErrorContains(t, err, "boom")
}

func TestMaybeModifyCluster_forwardProxyInvalidAddress(t *testing.T) {
	s := newServerWithForwardProxy(t, "missing-port") // not host:port.
	err := s.maybeModifyCluster(t.Context(), clusterWithTLSMatch(t, "api.openai.com"), nil)
	require.ErrorContains(t, err, "forward proxy")
}
