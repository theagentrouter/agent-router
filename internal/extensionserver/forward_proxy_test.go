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

// endpointProxyAddress unmarshals the endpoint's http_11_proxy proxy address metadata, or
// returns nil when the endpoint carries none (it connects directly).
func endpointProxyAddress(t *testing.T, lbEndpoint *endpointv3.LbEndpoint) *corev3.Address {
	t.Helper()
	anyAddr, ok := lbEndpoint.GetMetadata().GetTypedFilterMetadata()[http11ProxyAddressMetadataKey]
	if !ok {
		return nil
	}
	require.NotNil(t, anyAddr)
	address := &corev3.Address{}
	require.NoError(t, anyAddr.UnmarshalTo(address))
	return address
}

// newServerWithForwardProxy seeds a fake client with an AIGatewayRoute whose parent Gateway
// references a GatewayConfig. The GatewayConfig configures forwardProxy only when proxyAddr != "".
func newServerWithForwardProxy(t *testing.T, proxyAddr string, noProxy ...string) *Server {
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
		gc.Spec.ForwardProxy = &aigv1b1.GatewayConfigForwardProxy{Address: proxyAddr, NoProxy: noProxy}
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

// lbEndpointWithAddress builds an LbEndpoint whose upstream address is the given host or IP.
func lbEndpointWithAddress(address string) *endpointv3.LbEndpoint {
	return &endpointv3.LbEndpoint{
		HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
			Endpoint: &endpointv3.Endpoint{
				Address: &corev3.Address{
					Address: &corev3.Address_SocketAddress{
						SocketAddress: &corev3.SocketAddress{
							Address:       address,
							PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: 443},
						},
					},
				},
			},
		},
	}
}

// clusterWithEndpointAddresses builds a 5-part AI Gateway cluster with a TLS transport-socket
// match and one endpoint per given address.
func clusterWithEndpointAddresses(t *testing.T, addresses ...string) *clusterv3.Cluster {
	lbEndpoints := make([]*endpointv3.LbEndpoint, 0, len(addresses))
	for _, address := range addresses {
		lbEndpoints = append(lbEndpoints, lbEndpointWithAddress(address))
	}
	return &clusterv3.Cluster{
		Name:     "httproute/default/myroute/rule/0",
		Metadata: &corev3.Metadata{},
		TransportSocketMatches: []*clusterv3.Cluster_TransportSocketMatch{
			{Name: "httproute/default/myroute/rule/0/tls/0", TransportSocket: tlsTransportSocket(t, "api.openai.com")},
		},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			Endpoints: []*endpointv3.LocalityLbEndpoints{{LbEndpoints: lbEndpoints}},
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

func TestMaybeModifyCluster_forwardProxyNoProxy(t *testing.T) {
	// maybeModifyCluster needs one backendRef per endpoint to stamp endpoint metadata, so the
	// route has two backends for the mixed cluster cases.
	newServer := func(t *testing.T, noProxy []string) *Server {
		t.Helper()
		c := newFakeClient()
		require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{Name: "eg-gateway"}},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend-a"}, {Name: "backend-b"}}},
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
		require.NoError(t, c.Create(t.Context(), &aigv1b1.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "gwconfig", Namespace: "default"},
			Spec: aigv1b1.GatewayConfigSpec{
				ForwardProxy: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", NoProxy: noProxy},
			},
		}))
		s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		return s
	}

	t.Run("mixed cluster: bypassed endpoint direct, other through proxy", func(t *testing.T) {
		s := newServer(t, []string{".corp"})
		cluster := clusterWithEndpointAddresses(t, "api.openai.com", "internal-llm.corp")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		// The wrapper carries no default proxy address; the per-endpoint metadata carries it.
		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, wrapper.GetDefaultProxyAddress())
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())

		endpoints := clusterEndpoints(cluster)
		require.Len(t, endpoints, 2)

		proxied := endpointProxyAddress(t, endpoints[0])
		sa := proxied.GetSocketAddress()
		require.NotNil(t, sa)
		require.Equal(t, "proxy.corp", sa.GetAddress())
		require.Equal(t, uint32(3128), sa.GetPortValue())

		// The bypassed endpoint carries no proxy address metadata and connects directly.
		require.Nil(t, endpointProxyAddress(t, endpoints[1]))
	})

	t.Run("all endpoints bypass: cluster left untouched", func(t *testing.T) {
		s := newServer(t, []string{".corp"})
		cluster := clusterWithEndpointAddresses(t, "internal-llm.corp", "other-llm.corp")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		require.Equal(t, tlsTransportSocketName, cluster.TransportSocketMatches[0].TransportSocket.Name)
		for _, endpoint := range clusterEndpoints(cluster) {
			require.Nil(t, endpointProxyAddress(t, endpoint))
		}
	})

	t.Run("no endpoint matches: every endpoint stamped, no default", func(t *testing.T) {
		s := newServer(t, []string{".corp"})
		cluster := clusterWithEndpointAddresses(t, "api.openai.com", "api.anthropic.com")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, wrapper.GetDefaultProxyAddress())
		for _, endpoint := range clusterEndpoints(cluster) {
			require.NotNil(t, endpointProxyAddress(t, endpoint))
		}
	})

	t.Run("no match on IP endpoint by domain entry", func(t *testing.T) {
		s := newServer(t, []string{".corp"})
		cluster := clusterWithEndpointAddresses(t, "10.2.3.4", "api.openai.com")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		// Domain entries do not match IPs and vice versa: both endpoints are stamped.
		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, wrapper.GetDefaultProxyAddress())
		require.NotNil(t, endpointProxyAddress(t, clusterEndpoints(cluster)[0]))
		require.NotNil(t, endpointProxyAddress(t, clusterEndpoints(cluster)[1]))
	})

	t.Run("EDS-managed cluster without endpoints: proxied via default", func(t *testing.T) {
		s := newServer(t, []string{".corp"})
		cluster := clusterWithTLSMatch(t, "internal-llm.corp")
		cluster.LoadAssignment = nil // EDS-managed: endpoints are delivered out of band.

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		requireProxyAddress(t, wrapper, "proxy.corp", 3128)
	})

	t.Run("invalid entries are dropped and proxied", func(t *testing.T) {
		s := newServer(t, []string{"10.0.0.0/33"}) // out-of-range prefix: unmatchable.
		cluster := clusterWithEndpointAddresses(t, "10.1.2.3")

		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, wrapper.GetDefaultProxyAddress())
		require.NotNil(t, endpointProxyAddress(t, clusterEndpoints(cluster)[0]))
	})
}

func TestMaybeModifyCluster_forwardProxyNoProxyWildcard(t *testing.T) {
	s := newServerWithForwardProxy(t, "proxy.corp:3128", "*")
	cluster := clusterWithEndpointAddresses(t, "api.openai.com", "10.1.2.3")

	require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil))

	// "*" bypasses everything: the cluster is left untouched.
	require.Equal(t, tlsTransportSocketName, cluster.TransportSocketMatches[0].TransportSocket.Name)
	for _, endpoint := range clusterEndpoints(cluster) {
		require.Nil(t, endpointProxyAddress(t, endpoint))
	}
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

func TestParseNoProxyEntries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		entries     []string
		wantInvalid []string
		wantBypass  bool
		want        *noProxyMatcher
	}{
		{
			name:       "empty",
			entries:    nil,
			want:       &noProxyMatcher{},
			wantBypass: false,
		},
		{
			name:        "wildcard short-circuits",
			entries:     []string{".corp", "*"},
			want:        &noProxyMatcher{bypassAll: true, domains: []string{"corp"}},
			wantBypass:  true,
			wantInvalid: nil,
		},
		{
			name:        "domain entries normalized",
			entries:     []string{".Example.COM", "localhost"},
			want:        &noProxyMatcher{domains: []string{"example.com", "localhost"}},
			wantBypass:  false,
			wantInvalid: nil,
		},
		{
			name:        "invalid cidr dropped",
			entries:     []string{"10.0.0.0/33", "not-an-ip/8"},
			want:        &noProxyMatcher{},
			wantBypass:  false,
			wantInvalid: []string{"10.0.0.0/33", "not-an-ip/8"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, invalid := parseNoProxyEntries(tc.entries)
			require.Equal(t, tc.wantInvalid, invalid)
			require.Equal(t, tc.want.domains, got.domains)
			require.Equal(t, tc.want.bypassAll, got.bypassAll)
		})
	}

	t.Run("ip and cidr entries", func(t *testing.T) {
		got, invalid := parseNoProxyEntries([]string{"10.1.2.3", "10.0.0.0/8", "fd00::/8", "::1"})
		require.Empty(t, invalid)
		require.Len(t, got.ipNets, 4)
		require.Empty(t, got.domains)
		require.False(t, got.bypassAll)
	})
}

func TestNoProxyMatcher_bypassesEndpoint(t *testing.T) {
	endpoint := func(address string) *endpointv3.LbEndpoint {
		return lbEndpointWithAddress(address)
	}
	endpointWithHostname := func(address, hostname string) *endpointv3.LbEndpoint {
		lb := lbEndpointWithAddress(address)
		lb.GetEndpoint().Hostname = hostname
		return lb
	}

	for _, tc := range []struct {
		name     string
		entries  []string
		endpoint *endpointv3.LbEndpoint
		want     bool
	}{
		{name: "wildcard matches everything", entries: []string{"*"}, endpoint: endpoint("api.openai.com"), want: true},
		{name: "exact domain", entries: []string{"example.com"}, endpoint: endpoint("example.com"), want: true},
		{name: "domain suffix", entries: []string{"example.com"}, endpoint: endpoint("api.example.com"), want: true},
		{name: "leading dot domain", entries: []string{".example.com"}, endpoint: endpoint("api.example.com"), want: true},
		{name: "leading dot matches the domain itself", entries: []string{".example.com"}, endpoint: endpoint("example.com"), want: true},
		{name: "domain match is case-insensitive", entries: []string{"Example.COM"}, endpoint: endpoint("api.EXAMPLE.com"), want: true},
		{name: "suffix must be on a label boundary", entries: []string{"ample.com"}, endpoint: endpoint("api.example.com"), want: false},
		{name: "unrelated domain", entries: []string{"example.com"}, endpoint: endpoint("api.openai.com"), want: false},
		{name: "domain entry does not match an IP endpoint", entries: []string{"example.com"}, endpoint: endpoint("10.1.2.3"), want: false},
		{name: "exact IP", entries: []string{"10.1.2.3"}, endpoint: endpoint("10.1.2.3"), want: true},
		{name: "ipv4 cidr contains", entries: []string{"10.0.0.0/8"}, endpoint: endpoint("10.1.2.3"), want: true},
		{name: "ipv4 cidr excludes", entries: []string{"10.0.0.0/8"}, endpoint: endpoint("192.168.1.1"), want: false},
		{name: "ipv6 exact", entries: []string{"::1"}, endpoint: endpoint("::1"), want: true},
		{name: "ipv6 cidr contains", entries: []string{"fd00::/8"}, endpoint: endpoint("fd12::1"), want: true},
		{name: "ipv6 cidr excludes", entries: []string{"fd00::/8"}, endpoint: endpoint("2001:db8::1"), want: false},
		{name: "cidr entry does not match a DNS endpoint", entries: []string{"10.0.0.0/8"}, endpoint: endpoint("api.openai.com"), want: false},
		{name: "endpoint hostname matches domain entry", entries: []string{"example.com"}, endpoint: endpointWithHostname("10.1.2.3", "api.example.com"), want: true},
		{name: "endpoint with no address never matches", entries: []string{"*"}, endpoint: &endpointv3.LbEndpoint{}, want: true},
		{name: "addressless endpoint with domain entry", entries: []string{"example.com"}, endpoint: &endpointv3.LbEndpoint{}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, invalid := parseNoProxyEntries(tc.entries)
			require.Empty(t, invalid)
			require.Equal(t, tc.want, m.bypassesEndpoint(tc.endpoint))
		})
	}
}

func TestWrapClusterTransportSockets(t *testing.T) {
	proxy, err := parseForwardProxyAddress("proxy.corp:3128")
	require.NoError(t, err)

	t.Run("idempotent: wrapping twice does not double-wrap", func(t *testing.T) {
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		require.NoError(t, wrapClusterTransportSockets(cluster, proxy))
		require.NoError(t, wrapClusterTransportSockets(cluster, proxy))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		// The nested socket must be the original TLS socket, not another http_11_proxy wrapper.
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())
	})

	t.Run("no transport socket creates a plaintext-tunnel wrapper", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "httproute/default/myroute/rule/0"}
		require.NoError(t, wrapClusterTransportSockets(cluster, proxy))

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
		require.NoError(t, wrapClusterTransportSockets(cluster, proxy))

		require.Nil(t, cluster.TransportSocketMatches[0].TransportSocket)
		unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[1].TransportSocket)
	})

	t.Run("nil default proxy address leaves the wrapper default unset", func(t *testing.T) {
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		require.NoError(t, wrapClusterTransportSockets(cluster, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, wrapper.GetDefaultProxyAddress())
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())
	})
}

func TestWrapClusterInForwardProxy_noProxy(t *testing.T) {
	proxy, err := parseForwardProxyAddress("proxy.corp:3128")
	require.NoError(t, err)

	t.Run("nil matcher wraps with the default proxy address", func(t *testing.T) {
		cluster := clusterWithEndpointAddresses(t, "api.openai.com")
		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, nil))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		requireProxyAddress(t, wrapper, "proxy.corp", 3128)
	})

	t.Run("wildcard leaves the cluster untouched", func(t *testing.T) {
		cluster := clusterWithEndpointAddresses(t, "api.openai.com")
		noProxy, invalid := parseNoProxyEntries([]string{"*"})
		require.Empty(t, invalid)

		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, noProxy))

		require.Equal(t, tlsTransportSocketName, cluster.TransportSocketMatches[0].TransportSocket.Name)
	})

	t.Run("all endpoints bypass: cluster untouched", func(t *testing.T) {
		cluster := clusterWithEndpointAddresses(t, "api.example.com", "api.corp")
		noProxy, invalid := parseNoProxyEntries([]string{"example.com", ".corp"})
		require.Empty(t, invalid)

		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, noProxy))

		require.Equal(t, tlsTransportSocketName, cluster.TransportSocketMatches[0].TransportSocket.Name)
		for _, endpoint := range clusterEndpoints(cluster) {
			require.Nil(t, endpointProxyAddress(t, endpoint))
		}
	})

	t.Run("no endpoints: wrapped with default proxy address", func(t *testing.T) {
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		cluster.LoadAssignment = nil // EDS-managed: endpoints are delivered out of band.
		noProxy, invalid := parseNoProxyEntries([]string{".corp"})
		require.Empty(t, invalid)

		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, noProxy))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		requireProxyAddress(t, wrapper, "proxy.corp", 3128)
	})

	t.Run("mixed endpoints: stamped per endpoint, no default", func(t *testing.T) {
		cluster := clusterWithEndpointAddresses(t, "api.openai.com", "internal-llm.corp")
		noProxy, invalid := parseNoProxyEntries([]string{".corp"})
		require.Empty(t, invalid)

		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, noProxy))

		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, wrapper.GetDefaultProxyAddress())
		require.NotNil(t, endpointProxyAddress(t, clusterEndpoints(cluster)[0]))
		require.Nil(t, endpointProxyAddress(t, clusterEndpoints(cluster)[1]))
	})

	t.Run("idempotent on re-wrap", func(t *testing.T) {
		cluster := clusterWithEndpointAddresses(t, "api.openai.com")
		noProxy, invalid := parseNoProxyEntries([]string{".corp"})
		require.Empty(t, invalid)

		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, noProxy))
		require.NoError(t, wrapClusterInForwardProxy(cluster, proxy, noProxy))

		// The socket is not double-wrapped and the endpoint is not double-stamped.
		wrapper := unwrapHTTP11Proxy(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())
		require.Len(t, clusterEndpoints(cluster)[0].GetMetadata().GetTypedFilterMetadata(), 1)
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

func gwConfig(name, address string, noProxy ...string) *aigv1b1.GatewayConfig {
	gc := &aigv1b1.GatewayConfig{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	if address != "" {
		gc.Spec.ForwardProxy = &aigv1b1.GatewayConfigForwardProxy{Address: address, NoProxy: noProxy}
	}
	return gc
}

func routeWithParents(parents ...gwapiv1.ParentReference) *aigv1b1.AIGatewayRoute {
	return &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec:       aigv1b1.AIGatewayRouteSpec{ParentRefs: parents},
	}
}

func TestResolveForwardProxyConfig(t *testing.T) {
	gwParent := gwapiv1.ParentReference{Name: "eg-gateway"}
	for _, tc := range []struct {
		name  string
		objs  []client.Object
		route *aigv1b1.AIGatewayRoute
		want  *aigv1b1.GatewayConfigForwardProxy
	}{
		{
			name:  "no parent refs",
			route: routeWithParents(),
			want:  nil,
		},
		{
			name:  "gateway not found",
			route: routeWithParents(gwParent),
			want:  nil,
		},
		{
			name:  "gateway without annotation",
			objs:  []client.Object{gwGateway("eg-gateway", "")},
			route: routeWithParents(gwParent),
			want:  nil,
		},
		{
			name:  "annotation to missing gatewayconfig",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig")},
			route: routeWithParents(gwParent),
			want:  nil,
		},
		{
			name:  "gatewayconfig without forwardProxy",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig"), gwConfig("gwconfig", "")},
			route: routeWithParents(gwParent),
			want:  nil,
		},
		{
			name:  "configured",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig"), gwConfig("gwconfig", "proxy.corp:3128")},
			route: routeWithParents(gwParent),
			want:  &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128"},
		},
		{
			name: "configured with noProxy",
			objs: []client.Object{
				gwGateway("eg-gateway", "gwconfig"),
				gwConfig("gwconfig", "proxy.corp:3128", ".corp", "10.0.0.0/8", "*"),
			},
			route: routeWithParents(gwParent),
			want:  &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", NoProxy: []string{".corp", "10.0.0.0/8", "*"}},
		},
		{
			name:  "non-Gateway parent kind is skipped",
			objs:  []client.Object{gwGateway("eg-gateway", "gwconfig"), gwConfig("gwconfig", "proxy.corp:3128")},
			route: routeWithParents(gwapiv1.ParentReference{Name: "eg-gateway", Kind: ptr.To[gwapiv1.Kind]("Service")}),
			want:  nil,
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
			want: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy-a:3128"},
		},
		{
			name: "conflicting noProxy: first configured wins",
			objs: []client.Object{
				gwGateway("gw-a", "gc-a"), gwConfig("gc-a", "proxy-a:3128", ".corp"),
				gwGateway("gw-b", "gc-b"), gwConfig("gc-b", "proxy-a:3128"),
			},
			route: routeWithParents(
				gwapiv1.ParentReference{Name: "gw-a"},
				gwapiv1.ParentReference{Name: "gw-b"},
			),
			want: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy-a:3128", NoProxy: []string{".corp"}},
		},
		{
			name: "identical configs resolve without conflict",
			objs: []client.Object{
				gwGateway("gw-a", "gc-a"), gwConfig("gc-a", "proxy-a:3128", ".corp"),
				gwGateway("gw-b", "gc-b"), gwConfig("gc-b", "proxy-a:3128", ".corp"),
			},
			route: routeWithParents(
				gwapiv1.ParentReference{Name: "gw-a"},
				gwapiv1.ParentReference{Name: "gw-b"},
			),
			want: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy-a:3128", NoProxy: []string{".corp"}},
		},
		{
			name: "reordered noProxy lists are not a conflict",
			objs: []client.Object{
				gwGateway("gw-a", "gc-a"), gwConfig("gc-a", "proxy-a:3128", ".corp", "10.0.0.0/8"),
				gwGateway("gw-b", "gc-b"), gwConfig("gc-b", "proxy-a:3128", "10.0.0.0/8", ".corp"),
			},
			route: routeWithParents(
				gwapiv1.ParentReference{Name: "gw-a"},
				gwapiv1.ParentReference{Name: "gw-b"},
			),
			want: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy-a:3128", NoProxy: []string{".corp", "10.0.0.0/8"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := serverWithObjects(t, tc.objs...)
			got, err := s.resolveForwardProxyConfig(t.Context(), tc.route)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestResolveForwardProxyConfig_getError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(controller.Scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return errors.New("boom")
			},
		}).Build()
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	_, err = s.resolveForwardProxyConfig(t.Context(), routeWithParents(gwapiv1.ParentReference{Name: "eg-gateway"}))
	require.ErrorContains(t, err, "boom")
}

func TestMaybeModifyCluster_forwardProxyInvalidAddress(t *testing.T) {
	s := newServerWithForwardProxy(t, "missing-port") // not host:port.
	err := s.maybeModifyCluster(t.Context(), clusterWithTLSMatch(t, "api.openai.com"), nil)
	require.ErrorContains(t, err, "forward proxy")
}
