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
	http11proxyv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/http_11_proxy/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	matcherv3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"k8s.io/apimachinery/pkg/api/equality"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/forwardproxy"
)

const (
	// http11ProxyTransportSocketName is the Envoy transport socket that tunnels an upstream
	// connection through an HTTP/1.1 CONNECT forward proxy.
	http11ProxyTransportSocketName = "envoy.transport_sockets.http_11_proxy"
	// tlsTransportSocketName is the Envoy TLS transport socket.
	tlsTransportSocketName = "envoy.transport_sockets.tls"

	// gatewayConfigAnnotationKey mirrors controller.GatewayConfigAnnotationKey. It is redefined
	// here to avoid importing the controller package from the extension server. Keep in sync.
	gatewayConfigAnnotationKey = "aigateway.envoyproxy.io/gateway-config"

	// systemCACertPath is the system trust store of the Envoy proxy image, used to verify the
	// forward proxy when no CA certificate is configured. Same path Envoy Gateway uses.
	systemCACertPath = "/etc/ssl/certs/ca-certificates.crt"

	// forwardProxySecretPrefix prefixes the names of the SDS secrets generated for forward proxies.
	forwardProxySecretPrefix = "aigw-forward-proxy" // #nosec G101 -- secret name prefix, not a credential.

	// Field numbers of Http11ProxyUpstreamTransport not present in the pinned go-control-plane.
	// See api/envoy/extensions/transport_sockets/http_11_proxy/v3/upstream_http_11_connect.proto.
	//
	// TODO: replace the hand encoding with the typed fields once go-control-plane is bumped.
	http11ProxyFieldProxyTransportSocket protowire.Number = 3
	http11ProxyFieldProxyAuthorization   protowire.Number = 4
)

// forwardProxySecrets collects the SDS secrets generated while wrapping clusters, keyed by name,
// so that clusters sharing a GatewayConfig emit each secret once.
type forwardProxySecrets map[string]*tlsv3.Secret

// sorted returns the collected secrets ordered by name, for deterministic output.
func (f forwardProxySecrets) sorted() []*tlsv3.Secret {
	out := make([]*tlsv3.Secret, 0, len(f))
	for _, s := range f {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *tlsv3.Secret) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// forwardProxyConfig is the resolved Envoy configuration of a forward proxy.
type forwardProxyConfig struct {
	// address is the proxy address.
	address *corev3.Address
	// transportSocket is the transport socket for the connection to the proxy itself, or nil for
	// plaintext.
	transportSocket *corev3.TransportSocket
	// authorization references the SDS secret holding the Proxy-Authorization value, or nil.
	authorization *tlsv3.SdsSecretConfig
}

// maybeWrapClusterInForwardProxy wraps the upstream transport socket(s) of an AI Gateway cluster
// in Envoy's http_11_proxy transport socket when the owning Gateway's GatewayConfig configures a
// forward proxy. This routes upstream egress through an HTTP CONNECT proxy while preserving the
// original (TLS) transport nested inside the wrapper. SDS secrets needed by the proxy
// configuration are added to secrets.
//
// It is a no-op when no forward proxy is configured for the route's Gateway(s).
func (s *Server) maybeWrapClusterInForwardProxy(ctx context.Context, cluster *clusterv3.Cluster, route *aigv1b1.AIGatewayRoute, secrets forwardProxySecrets) error {
	gatewayConfig, err := s.resolveForwardProxy(ctx, route)
	if err != nil {
		return err
	}
	if gatewayConfig == nil {
		return nil
	}
	cfg, err := s.buildForwardProxyConfig(ctx, gatewayConfig, secrets)
	if err != nil {
		return fmt.Errorf("invalid forward proxy for cluster %s: %w", cluster.Name, err)
	}
	return wrapClusterInForwardProxy(cluster, cfg)
}

// resolveForwardProxy returns the GatewayConfig configuring a forward proxy referenced by any of
// the route's parent Gateways, or nil if none is configured.
//
// If multiple parent Gateways resolve conflicting forward proxies, the first one found is used
// and a warning is logged.
func (s *Server) resolveForwardProxy(ctx context.Context, route *aigv1b1.AIGatewayRoute) (*aigv1b1.GatewayConfig, error) {
	var resolved *aigv1b1.GatewayConfig
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
		gatewayConfig, err := s.gatewayConfigForGateway(ctx, string(parentRef.Name), gwNamespace)
		if err != nil {
			return nil, err
		}
		if gatewayConfig == nil || gatewayConfig.Spec.ForwardProxy == nil {
			continue
		}
		if resolved == nil {
			resolved = gatewayConfig
		} else if !sameForwardProxy(resolved, gatewayConfig) {
			s.log.Info("multiple parent Gateways configure different forward proxies; using the first",
				"route", route.Name, "namespace", route.Namespace,
				"using", resolved.Namespace+"/"+resolved.Name, "ignored", gatewayConfig.Namespace+"/"+gatewayConfig.Name)
		}
	}
	return resolved, nil
}

// sameForwardProxy reports whether two GatewayConfigs configure the same forward proxy. Secret
// references resolve in the GatewayConfig namespace, so configs referencing Secrets are only the
// same when they live in the same namespace.
func sameForwardProxy(a, b *aigv1b1.GatewayConfig) bool {
	if !equality.Semantic.DeepEqual(a.Spec.ForwardProxy, b.Spec.ForwardProxy) {
		return false
	}
	return a.Namespace == b.Namespace || len(forwardproxy.SecretRefs(a.Namespace, a.Spec.ForwardProxy)) == 0
}

// buildForwardProxyConfig resolves the Envoy configuration of the GatewayConfig's forward proxy,
// adding the SDS secrets it references to secrets.
func (s *Server) buildForwardProxyConfig(ctx context.Context, gatewayConfig *aigv1b1.GatewayConfig, secrets forwardProxySecrets) (*forwardProxyConfig, error) {
	fp := gatewayConfig.Spec.ForwardProxy
	host, address, err := parseForwardProxyAddress(fp.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", fp.Address, err)
	}
	cfg := &forwardProxyConfig{address: address}
	if fp.TLS == nil && fp.Auth == nil {
		return cfg, nil
	}

	creds, err := forwardproxy.LoadCredentials(ctx, s.k8sClient, gatewayConfig.Namespace, fp)
	if err != nil {
		return nil, err
	}
	secretName := func(kind string) string {
		return fmt.Sprintf("%s/%s/%s/%s", forwardProxySecretPrefix, gatewayConfig.Namespace, gatewayConfig.Name, kind)
	}

	if fp.TLS != nil {
		tlsCtx := &tlsv3.UpstreamTlsContext{
			CommonTlsContext: &tlsv3.CommonTlsContext{
				// The proxy speaks HTTP/1.1 CONNECT; see the proxy_transport_socket docs.
				AlpnProtocols: []string{"http/1.1"},
				ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{
					ValidationContext: proxyValidationContext(host, fp.TLS, creds.CACert),
				},
			},
		}
		if fp.TLS.SNI != nil {
			tlsCtx.Sni = *fp.TLS.SNI
		} else if net.ParseIP(host) == nil {
			tlsCtx.Sni = host
		}
		if creds.ClientCert != nil {
			name := secretName("client-cert")
			secrets[name] = &tlsv3.Secret{
				Name: name,
				Type: &tlsv3.Secret_TlsCertificate{TlsCertificate: &tlsv3.TlsCertificate{
					CertificateChain: inlineBytes(creds.ClientCert),
					PrivateKey:       inlineBytes(creds.ClientKey),
				}},
			}
			tlsCtx.CommonTlsContext.TlsCertificateSdsSecretConfigs = []*tlsv3.SdsSecretConfig{sdsSecretConfig(name)}
		}
		anyTLS, err := toAny(tlsCtx)
		if err != nil {
			return nil, err
		}
		cfg.transportSocket = &corev3.TransportSocket{
			Name:       tlsTransportSocketName,
			ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: anyTLS},
		}
	}

	if creds.ProxyAuthorization != "" {
		name := secretName("auth")
		secrets[name] = &tlsv3.Secret{
			Name: name,
			Type: &tlsv3.Secret_GenericSecret{GenericSecret: &tlsv3.GenericSecret{
				Secret: &corev3.DataSource{Specifier: &corev3.DataSource_InlineString{InlineString: creds.ProxyAuthorization}},
			}},
		}
		cfg.authorization = sdsSecretConfig(name)
	}
	return cfg, nil
}

// proxyValidationContext builds the validation context used to verify the proxy certificate:
// the configured CA (or the system trust store) and a subject alt name matching the SNI, or the
// address IP when no SNI is sent.
func proxyValidationContext(host string, cfg *aigv1b1.GatewayConfigForwardProxyTLS, caCert []byte) *tlsv3.CertificateValidationContext {
	vc := &tlsv3.CertificateValidationContext{
		TrustedCa: &corev3.DataSource{Specifier: &corev3.DataSource_Filename{Filename: systemCACertPath}},
	}
	if caCert != nil {
		vc.TrustedCa = inlineBytes(caCert)
	}
	san := &tlsv3.SubjectAltNameMatcher{SanType: tlsv3.SubjectAltNameMatcher_DNS}
	switch {
	case cfg.SNI != nil:
		san.Matcher = exactMatcher(*cfg.SNI)
	case net.ParseIP(host) != nil:
		san.SanType = tlsv3.SubjectAltNameMatcher_IP_ADDRESS
		san.Matcher = exactMatcher(host)
	default:
		san.Matcher = exactMatcher(host)
	}
	vc.MatchTypedSubjectAltNames = []*tlsv3.SubjectAltNameMatcher{san}
	return vc
}

func exactMatcher(s string) *matcherv3.StringMatcher {
	return &matcherv3.StringMatcher{MatchPattern: &matcherv3.StringMatcher_Exact{Exact: s}}
}

func inlineBytes(b []byte) *corev3.DataSource {
	return &corev3.DataSource{Specifier: &corev3.DataSource_InlineBytes{InlineBytes: b}}
}

// sdsSecretConfig references a secret delivered over ADS, as Envoy Gateway does for its own
// secrets.
func sdsSecretConfig(name string) *tlsv3.SdsSecretConfig {
	return &tlsv3.SdsSecretConfig{
		Name: name,
		SdsConfig: &corev3.ConfigSource{
			ResourceApiVersion:    corev3.ApiVersion_V3,
			ConfigSourceSpecifier: &corev3.ConfigSource_Ads{Ads: &corev3.AggregatedConfigSource{}},
		},
	}
}

// parseForwardProxyAddress parses a "host:port" proxy address into its host and an Envoy Address.
// Unlike parseHostPort, the port is mandatory: a forward proxy has no meaningful default port.
func parseForwardProxyAddress(addr string) (string, *corev3.Address, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", nil, fmt.Errorf("expected host:port: %w", err)
	}
	if host == "" {
		return "", nil, fmt.Errorf("host must not be empty")
	}
	// Parse as uint16 so out-of-range ports (> 65535) are rejected here rather than by Envoy.
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", nil, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	if port == 0 {
		return "", nil, fmt.Errorf("port must be non-zero")
	}
	return host, &corev3.Address{
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
// configured by cfg. The operation is idempotent: sockets already wrapped are left as-is.
//
// Envoy Gateway attaches upstream TLS to AI Gateway clusters via TransportSocketMatches (one per
// backend), and only uses the singular TransportSocket for the proxy-protocol / dynamic-resolver
// cases, so both must be handled. If the cluster has no transport socket at all, a wrapper with a
// nil inner socket (plaintext) is set so egress still tunnels through the proxy.
func wrapClusterInForwardProxy(cluster *clusterv3.Cluster, cfg *forwardProxyConfig) error {
	wrapped := false
	if cluster.TransportSocket != nil {
		ts, err := wrapTransportSocket(cluster.TransportSocket, cfg)
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
		ts, err := wrapTransportSocket(match.TransportSocket, cfg)
		if err != nil {
			return err
		}
		match.TransportSocket = ts
		wrapped = true
	}
	if !wrapped {
		// No existing transport socket: wrap a nil (plaintext) inner so egress still tunnels.
		ts, err := wrapTransportSocket(nil, cfg)
		if err != nil {
			return err
		}
		cluster.TransportSocket = ts
	}
	return nil
}

// wrapTransportSocket nests inner inside an http_11_proxy transport socket. If inner is already an
// http_11_proxy socket it is returned unchanged (idempotent). A nil inner defaults to raw_buffer.
func wrapTransportSocket(inner *corev3.TransportSocket, cfg *forwardProxyConfig) (*corev3.TransportSocket, error) {
	if inner != nil && inner.Name == http11ProxyTransportSocketName {
		return inner, nil
	}
	anyProxy, err := marshalHTTP11ProxyTransport(&http11proxyv3.Http11ProxyUpstreamTransport{
		TransportSocket:     inner,
		DefaultProxyAddress: cfg.address,
	}, cfg.transportSocket, cfg.authorization)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal http_11_proxy transport socket: %w", err)
	}
	return &corev3.TransportSocket{
		Name:       http11ProxyTransportSocketName,
		ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: anyProxy},
	}, nil
}

// marshalHTTP11ProxyTransport marshals base and appends the proxy_transport_socket and
// proxy_authorization fields, which the pinned go-control-plane does not define yet. Protobuf
// keeps unknown fields when the message is decoded and re-encoded, so they reach Envoy intact.
func marshalHTTP11ProxyTransport(base *http11proxyv3.Http11ProxyUpstreamTransport, proxyTransportSocket *corev3.TransportSocket, proxyAuthorization *tlsv3.SdsSecretConfig) (*anypb.Any, error) {
	b, err := proto.Marshal(base)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		num protowire.Number
		msg proto.Message
	}{
		{http11ProxyFieldProxyTransportSocket, proxyTransportSocket},
		{http11ProxyFieldProxyAuthorization, proxyAuthorization},
	} {
		if f.msg == nil || !f.msg.ProtoReflect().IsValid() {
			continue
		}
		fb, err := proto.Marshal(f.msg)
		if err != nil {
			return nil, err
		}
		b = protowire.AppendTag(b, f.num, protowire.BytesType)
		b = protowire.AppendBytes(b, fb)
	}
	return &anypb.Any{
		TypeUrl: "type.googleapis.com/" + string(base.ProtoReflect().Descriptor().FullName()),
		Value:   b,
	}, nil
}
