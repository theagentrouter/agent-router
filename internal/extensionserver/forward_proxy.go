// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"net"
	"strconv"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	http11proxyv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/http_11_proxy/v3"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

const (
	// http11ProxyTransportSocketName is the Envoy transport socket that tunnels an upstream
	// connection through an HTTP/1.1 CONNECT forward proxy.
	http11ProxyTransportSocketName = "envoy.transport_sockets.http_11_proxy"

	// gatewayConfigAnnotationKey mirrors controller.GatewayConfigAnnotationKey. It is redefined
	// here to avoid importing the controller package from the extension server. Keep in sync.
	gatewayConfigAnnotationKey = "aigateway.envoyproxy.io/gateway-config"
)

// maybeWrapClusterInForwardProxy wraps the upstream transport socket(s) of an AI Gateway cluster
// in Envoy's http_11_proxy transport socket when the owning Gateway's GatewayConfig configures a
// forward proxy. This routes upstream egress through an HTTP CONNECT proxy while preserving the
// original (TLS) transport nested inside the wrapper.
//
// It is a no-op when no forward proxy is configured for the route's Gateway(s).
func (s *Server) maybeWrapClusterInForwardProxy(ctx context.Context, cluster *clusterv3.Cluster, route *aigv1b1.AIGatewayRoute) error {
	addr, err := s.resolveForwardProxyAddr(ctx, route)
	if err != nil {
		return err
	}
	if addr == "" {
		return nil
	}
	proxyAddress, err := parseForwardProxyAddress(addr)
	if err != nil {
		return fmt.Errorf("invalid forward proxy address %q for cluster %s: %w", addr, cluster.Name, err)
	}
	return wrapClusterInForwardProxy(cluster, proxyAddress)
}

// resolveForwardProxyAddr returns the forward proxy "host:port" configured on the GatewayConfig
// referenced by any of the route's parent Gateways, or "" if none is configured.
//
// If multiple parent Gateways resolve conflicting proxy addresses, the first one found is used
// and a warning is logged.
func (s *Server) resolveForwardProxyAddr(ctx context.Context, route *aigv1b1.AIGatewayRoute) (string, error) {
	var resolved string
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
		addr, err := s.forwardProxyAddrForGateway(ctx, string(parentRef.Name), gwNamespace)
		if err != nil {
			return "", err
		}
		if addr == "" {
			continue
		}
		if resolved == "" {
			resolved = addr
		} else if resolved != addr {
			s.log.Info("multiple parent Gateways configure different forward proxies; using the first",
				"route", route.Name, "namespace", route.Namespace, "using", resolved, "ignored", addr)
		}
	}
	return resolved, nil
}

// forwardProxyAddrForGateway resolves the forward proxy address from the GatewayConfig referenced
// by the given Gateway's "aigateway.envoyproxy.io/gateway-config" annotation. It returns "" when
// the Gateway, its annotation, the GatewayConfig, or the forwardProxy field are absent.
func (s *Server) forwardProxyAddrForGateway(ctx context.Context, gatewayName, gatewayNamespace string) (string, error) {
	gatewayConfig, err := s.gatewayConfigForGateway(ctx, gatewayName, gatewayNamespace)
	if err != nil {
		return "", err
	}
	if gatewayConfig == nil || gatewayConfig.Spec.ForwardProxy == nil {
		return "", nil
	}
	return gatewayConfig.Spec.ForwardProxy.Address, nil
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

// wrapClusterInForwardProxy wraps every transport socket on the cluster (both the singular
// TransportSocket and each TransportSocketMatches entry) in an http_11_proxy transport socket
// pointing at proxyAddress. The operation is idempotent: sockets already wrapped are left as-is.
//
// Envoy Gateway attaches upstream TLS to AI Gateway clusters via TransportSocketMatches (one per
// backend), and only uses the singular TransportSocket for the proxy-protocol / dynamic-resolver
// cases, so both must be handled. If the cluster has no transport socket at all, a wrapper with a
// nil inner socket (plaintext) is set so egress still tunnels through the proxy.
func wrapClusterInForwardProxy(cluster *clusterv3.Cluster, proxyAddress *corev3.Address) error {
	wrapped := false
	if cluster.TransportSocket != nil {
		ts, err := wrapTransportSocket(cluster.TransportSocket, proxyAddress)
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
		ts, err := wrapTransportSocket(match.TransportSocket, proxyAddress)
		if err != nil {
			return err
		}
		match.TransportSocket = ts
		wrapped = true
	}
	if !wrapped {
		// No existing transport socket: wrap a nil (plaintext) inner so egress still tunnels.
		ts, err := wrapTransportSocket(nil, proxyAddress)
		if err != nil {
			return err
		}
		cluster.TransportSocket = ts
	}
	return nil
}

// wrapTransportSocket nests inner inside an http_11_proxy transport socket. If inner is already an
// http_11_proxy socket it is returned unchanged (idempotent). A nil inner defaults to raw_buffer.
func wrapTransportSocket(inner *corev3.TransportSocket, proxyAddress *corev3.Address) (*corev3.TransportSocket, error) {
	if inner != nil && inner.Name == http11ProxyTransportSocketName {
		return inner, nil
	}
	anyProxy, err := toAny(&http11proxyv3.Http11ProxyUpstreamTransport{
		TransportSocket:     inner,
		DefaultProxyAddress: proxyAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal http_11_proxy transport socket: %w", err)
	}
	return &corev3.TransportSocket{
		Name:       http11ProxyTransportSocketName,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: anyProxy},
	}, nil
}
