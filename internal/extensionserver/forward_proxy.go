// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	http11proxyv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/http_11_proxy/v3"
	"google.golang.org/protobuf/types/known/anypb"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

const (
	// http11ProxyTransportSocketName is the Envoy transport socket that tunnels an upstream
	// connection through an HTTP/1.1 CONNECT forward proxy.
	http11ProxyTransportSocketName = "envoy.transport_sockets.http_11_proxy"

	// http11ProxyAddressMetadataKey is the typed filter metadata key Envoy's http_11_proxy
	// transport socket reads the per-endpoint proxy address from. An endpoint carrying the key
	// connects through that proxy; without it, and without a default proxy address on the
	// wrapper, the endpoint connects directly.
	http11ProxyAddressMetadataKey = "envoy.http11_proxy_transport_socket.proxy_address"

	// gatewayConfigAnnotationKey mirrors controller.GatewayConfigAnnotationKey. It is redefined
	// here to avoid importing the controller package from the extension server. Keep in sync.
	gatewayConfigAnnotationKey = "aigateway.envoyproxy.io/gateway-config"
)

// maybeWrapClusterInForwardProxy wraps the upstream transport socket(s) of an AI Gateway cluster
// in Envoy's http_11_proxy transport socket when the owning Gateway's GatewayConfig configures a
// forward proxy. This routes upstream egress through an HTTP CONNECT proxy while preserving the
// original (TLS) transport nested inside the wrapper. Endpoints matching the forwardProxy.noProxy
// list are left to connect directly.
//
// It is a no-op when no forward proxy is configured for the route's Gateway(s).
func (s *Server) maybeWrapClusterInForwardProxy(ctx context.Context, cluster *clusterv3.Cluster, route *aigv1b1.AIGatewayRoute) error {
	forwardProxy, err := s.resolveForwardProxyConfig(ctx, route)
	if err != nil || forwardProxy == nil {
		return err
	}
	proxyAddress, err := parseForwardProxyAddress(forwardProxy.Address)
	if err != nil {
		return fmt.Errorf("invalid forward proxy address %q for cluster %s: %w", forwardProxy.Address, cluster.Name, err)
	}
	var noProxy *noProxyMatcher
	if len(forwardProxy.NoProxy) > 0 {
		var invalid []string
		noProxy, invalid = parseNoProxyEntries(forwardProxy.NoProxy)
		if len(invalid) > 0 {
			s.log.Info("ignoring invalid forwardProxy.noProxy entries; their traffic is proxied",
				"cluster", cluster.Name, "entries", invalid)
		}
	}
	return wrapClusterInForwardProxy(cluster, proxyAddress, noProxy)
}

// resolveForwardProxyConfig returns the forward proxy configuration from the GatewayConfig
// referenced by any of the route's parent Gateways, or nil if none is configured.
//
// If multiple parent Gateways resolve conflicting forward proxy configurations, the first one
// found is used and a warning is logged.
func (s *Server) resolveForwardProxyConfig(ctx context.Context, route *aigv1b1.AIGatewayRoute) (*aigv1b1.GatewayConfigForwardProxy, error) {
	var resolved *aigv1b1.GatewayConfigForwardProxy
	for i := range route.Spec.ParentRefs {
		parentRef := &route.Spec.ParentRefs[i]
		// AIGatewayRoute parent refs are Gateways; skip anything explicitly typed otherwise.
		if parentRef.Kind != nil && *parentRef.Kind != "Gateway" {
			continue
		}
		gwNamespace := route.Namespace
		if parentRef.Namespace != nil {
			gwNamespace = string(*parentRef.Namespace)
		}
		forwardProxy, err := s.forwardProxyConfigForGateway(ctx, string(parentRef.Name), gwNamespace)
		if err != nil {
			return nil, err
		}
		if forwardProxy == nil {
			continue
		}
		if resolved == nil {
			resolved = forwardProxy
		} else if resolved.Address != forwardProxy.Address || !slices.Equal(canonicalList(resolved.NoProxy), canonicalList(forwardProxy.NoProxy)) {
			s.log.Info("multiple parent Gateways configure different forward proxies; using the first",
				"route", route.Name, "namespace", route.Namespace,
				"using", resolved.Address, "usingNoProxy", resolved.NoProxy,
				"ignored", forwardProxy.Address, "ignoredNoProxy", forwardProxy.NoProxy)
		}
	}
	return resolved, nil
}

// canonicalList returns a sorted copy of entries so semantically equal, differently ordered
// lists compare equal. noProxy entries match independently of order.
func canonicalList(entries []string) []string {
	sorted := slices.Clone(entries)
	slices.Sort(sorted)
	return sorted
}

// forwardProxyConfigForGateway returns the forward proxy configuration from the GatewayConfig
// referenced by the given Gateway's "aigateway.envoyproxy.io/gateway-config" annotation. It
// returns nil when the Gateway, its annotation, the GatewayConfig, or the forwardProxy field
// are absent.
//
// The returned configuration aliases the fetched GatewayConfig and must be treated as read-only.
func (s *Server) forwardProxyConfigForGateway(ctx context.Context, gatewayName, gatewayNamespace string) (*aigv1b1.GatewayConfigForwardProxy, error) {
	gatewayConfig, err := s.gatewayConfigForGateway(ctx, gatewayName, gatewayNamespace)
	if err != nil || gatewayConfig == nil {
		return nil, err
	}
	return gatewayConfig.Spec.ForwardProxy, nil
}

// parseForwardProxyAddress parses a "host:port" proxy address into an Envoy Address. Unlike
// parseHostPort, the port is mandatory: a forward proxy has no meaningful default port.
func parseForwardProxyAddress(addr string) (*corev3.Address, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("expected host:port: %w", err)
	}
	if host == "" {
		return nil, fmt.Errorf("host must not be empty")
	}
	// Parse as uint16 so out-of-range ports (> 65535) are rejected here rather than by Envoy.
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	if port == 0 {
		return nil, fmt.Errorf("port must be non-zero")
	}
	return &corev3.Address{
		Address: &corev3.Address_SocketAddress{
			SocketAddress: &corev3.SocketAddress{
				Address:       host,
				Protocol:      corev3.SocketAddress_TCP,
				PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: uint32(port)},
			},
		},
	}, nil
}

// noProxyMatcher decides, per upstream endpoint, whether it bypasses the forward proxy. It is the
// equivalent of the no_proxy environment variable, resolved once per cluster translation.
type noProxyMatcher struct {
	// bypassAll is set when any entry is "*" and short-circuits all matching.
	bypassAll bool
	// ipNets are the IP/CIDR entries, with exact-IP entries normalized to single-host networks.
	ipNets []*net.IPNet
	// domains are the domain entries, lowercased and without the leading dot.
	domains []string
}

// parseNoProxyEntries parses noProxy entries into a matcher. Entries that are neither a
// wildcard nor a parsable IP, CIDR, or domain (e.g. an out-of-range prefix length) are returned
// as invalid: they are dropped from the matcher, so traffic to them stays proxied.
func parseNoProxyEntries(entries []string) (*noProxyMatcher, []string) {
	m := &noProxyMatcher{}
	var invalid []string
	for _, entry := range entries {
		switch {
		case entry == "*":
			m.bypassAll = true
			return m, invalid
		case strings.Contains(entry, "/"):
			if _, ipNet, err := net.ParseCIDR(entry); err == nil {
				m.ipNets = append(m.ipNets, ipNet)
				continue
			}
			invalid = append(invalid, entry)
		default:
			if ip := net.ParseIP(entry); ip != nil {
				m.ipNets = append(m.ipNets, ipHostNetwork(ip))
				continue
			}
			domain := strings.TrimPrefix(strings.ToLower(entry), ".")
			if domain == "" {
				invalid = append(invalid, entry)
				continue
			}
			m.domains = append(m.domains, domain)
		}
	}
	return m, invalid
}

// ipHostNetwork normalizes an exact-IP entry into a single-host IPNet.
func ipHostNetwork(ip net.IP) *net.IPNet {
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(net.IPv4len*8, net.IPv4len*8)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(net.IPv6len*8, net.IPv6len*8)}
}

// bypassesEndpoint reports whether the endpoint's destination matches a noProxy entry and must
// connect directly. IP-literal endpoints match IP/CIDR entries; DNS-named endpoints match domain
// entries. Endpoints with neither a hostname nor a resolvable address never match, so they are
// proxied.
func (m *noProxyMatcher) bypassesEndpoint(lbEndpoint *endpointv3.LbEndpoint) bool {
	if m.bypassAll {
		return true
	}
	address := lbEndpoint.GetEndpoint().GetAddress().GetSocketAddress().GetAddress()
	if ip := net.ParseIP(address); ip != nil {
		for _, ipNet := range m.ipNets {
			if ipNet.Contains(ip) {
				return true
			}
		}
	}
	// Envoy Gateway translates a Backend fqdn endpoint so that the FQDN lands in the socket
	// address, while Endpoint.Hostname is populated only from the Backend's optional top-level
	// hostname field (see endpointUpstreamHost).
	host := lbEndpoint.GetEndpoint().GetHostname()
	if host == "" {
		host = address
	}
	host = strings.ToLower(host)
	for _, domain := range m.domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// wrapClusterInForwardProxy routes the cluster's upstream egress through the HTTP CONNECT proxy
// at proxyAddress, honouring the noProxy exclusions when noProxy is non-nil.
//
// Without exclusions the cluster is wrapped with the proxy as the wrapper's default proxy
// address, so every endpoint tunnels through it. With exclusions the decisions are per
// endpoint: when every endpoint bypasses (or "*" is configured) the cluster is left untouched;
// otherwise the wrapper carries no default proxy address and each endpoint that must be proxied
// is stamped with the proxy address in its endpoint metadata. An unstamped endpoint then
// resolves no proxy address in Envoy and connects directly, while a stamped one tunnels through
// the proxy.
func wrapClusterInForwardProxy(cluster *clusterv3.Cluster, proxyAddress *corev3.Address, noProxy *noProxyMatcher) error {
	if noProxy == nil {
		return wrapClusterTransportSockets(cluster, proxyAddress)
	}
	if noProxy.bypassAll {
		// "*" bypasses everything: leave the cluster's egress untouched.
		return nil
	}
	endpoints := clusterEndpoints(cluster)
	if len(endpoints) == 0 {
		// EDS-managed clusters deliver endpoints out of band, and a cluster with no endpoints
		// yet has nothing to match: proxy everything, so the exclusions fail closed.
		return wrapClusterTransportSockets(cluster, proxyAddress)
	}
	allBypass := true
	for _, lbEndpoint := range endpoints {
		if !noProxy.bypassesEndpoint(lbEndpoint) {
			allBypass = false
			break
		}
	}
	if allBypass {
		return nil
	}
	for _, lbEndpoint := range endpoints {
		if !noProxy.bypassesEndpoint(lbEndpoint) {
			if err := stampEndpointProxyAddress(lbEndpoint, proxyAddress); err != nil {
				return err
			}
		}
	}
	return wrapClusterTransportSockets(cluster, nil)
}

// clusterEndpoints returns the cluster's inlined load assignment endpoints, or nil when the
// cluster's endpoints are not inlined (e.g. EDS-managed clusters).
func clusterEndpoints(cluster *clusterv3.Cluster) []*endpointv3.LbEndpoint {
	var endpoints []*endpointv3.LbEndpoint
	for _, locality := range cluster.GetLoadAssignment().GetEndpoints() {
		endpoints = append(endpoints, locality.GetLbEndpoints()...)
	}
	return endpoints
}

// stampEndpointProxyAddress records the proxy address in the endpoint's typed filter metadata,
// where Envoy's http_11_proxy transport socket picks it up when connecting through the proxy.
func stampEndpointProxyAddress(lbEndpoint *endpointv3.LbEndpoint, proxyAddress *corev3.Address) error {
	anyAddress, err := toAny(proxyAddress)
	if err != nil {
		return fmt.Errorf("failed to marshal proxy address metadata: %w", err)
	}
	if lbEndpoint.Metadata == nil {
		lbEndpoint.Metadata = &corev3.Metadata{}
	}
	if lbEndpoint.Metadata.TypedFilterMetadata == nil {
		lbEndpoint.Metadata.TypedFilterMetadata = make(map[string]*anypb.Any)
	}
	lbEndpoint.Metadata.TypedFilterMetadata[http11ProxyAddressMetadataKey] = anyAddress
	return nil
}

// wrapClusterTransportSockets wraps every transport socket on the cluster (both the singular
// TransportSocket and each TransportSocketMatches entry) in an http_11_proxy transport socket
// with the given default proxy address, which may be nil when per-endpoint metadata carries the
// proxy address instead. The operation is idempotent: sockets already wrapped are left as-is.
//
// Envoy Gateway attaches upstream TLS to AI Gateway clusters via TransportSocketMatches (one per
// backend), and only uses the singular TransportSocket for the proxy-protocol / dynamic-resolver
// cases, so both must be handled. If the cluster has no transport socket at all, a wrapper with a
// nil inner socket (plaintext) is set so egress still tunnels through the proxy.
func wrapClusterTransportSockets(cluster *clusterv3.Cluster, defaultProxyAddress *corev3.Address) error {
	wrapped := false
	if cluster.TransportSocket != nil {
		ts, err := wrapTransportSocket(cluster.TransportSocket, defaultProxyAddress)
		if err != nil {
			return err
		}
		cluster.TransportSocket = ts
		wrapped = true
	}
	for i := range cluster.TransportSocketMatches {
		match := cluster.TransportSocketMatches[i]
		if match.TransportSocket == nil {
			continue
		}
		ts, err := wrapTransportSocket(match.TransportSocket, defaultProxyAddress)
		if err != nil {
			return err
		}
		match.TransportSocket = ts
		wrapped = true
	}
	if !wrapped {
		// No existing transport socket: wrap a nil (plaintext) inner so egress still tunnels.
		ts, err := wrapTransportSocket(nil, defaultProxyAddress)
		if err != nil {
			return err
		}
		cluster.TransportSocket = ts
	}
	return nil
}

// wrapTransportSocket nests inner inside an http_11_proxy transport socket. If inner is already an
// http_11_proxy socket it is returned unchanged (idempotent). A nil inner defaults to raw_buffer.
//
// Envoy Gateway rebuilds clusters from its IR on every translation, so a cluster is expected to
// arrive here unwrapped; the idempotency guard only keeps an accidental re-wrap from nesting
// wrappers, and it does not rewrite an existing wrapper's default proxy address.
func wrapTransportSocket(inner *corev3.TransportSocket, defaultProxyAddress *corev3.Address) (*corev3.TransportSocket, error) {
	if inner != nil && inner.Name == http11ProxyTransportSocketName {
		return inner, nil
	}
	anyProxy, err := toAny(&http11proxyv3.Http11ProxyUpstreamTransport{
		TransportSocket:     inner,
		DefaultProxyAddress: defaultProxyAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal http_11_proxy transport socket: %w", err)
	}
	return &corev3.TransportSocket{
		Name:       http11ProxyTransportSocketName,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: anyProxy},
	}, nil
}
