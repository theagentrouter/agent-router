// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"testing"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	http11proxyv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/http_11_proxy/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// proxyExtensions holds the hand-encoded Http11ProxyUpstreamTransport fields of a wrapper.
type proxyExtensions struct {
	transportSocket *corev3.TransportSocket
	authorization   *tlsv3.SdsSecretConfig
}

// decodeProxyExtensions extracts the proxy_transport_socket (3) and proxy_authorization (4)
// fields from the http_11_proxy wrapper, which the pinned go-control-plane does not define.
func decodeProxyExtensions(t *testing.T, ts *corev3.TransportSocket) proxyExtensions {
	t.Helper()
	require.Equal(t, http11ProxyTransportSocketName, ts.GetName())
	var out proxyExtensions
	b := ts.GetTypedConfig().GetValue()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		require.GreaterOrEqual(t, n, 0)
		b = b[n:]
		if typ != protowire.BytesType || (num != http11ProxyFieldProxyTransportSocket && num != http11ProxyFieldProxyAuthorization) {
			n = protowire.ConsumeFieldValue(num, typ, b)
			require.GreaterOrEqual(t, n, 0)
			b = b[n:]
			continue
		}
		v, n := protowire.ConsumeBytes(b)
		require.GreaterOrEqual(t, n, 0)
		b = b[n:]
		switch num {
		case http11ProxyFieldProxyTransportSocket:
			out.transportSocket = &corev3.TransportSocket{}
			require.NoError(t, proto.Unmarshal(v, out.transportSocket))
		case http11ProxyFieldProxyAuthorization:
			out.authorization = &tlsv3.SdsSecretConfig{}
			require.NoError(t, proto.Unmarshal(v, out.authorization))
		}
	}
	return out
}

func proxyTLSContext(t *testing.T, ts *corev3.TransportSocket) *tlsv3.UpstreamTlsContext {
	t.Helper()
	require.NotNil(t, ts)
	require.Equal(t, tlsTransportSocketName, ts.GetName())
	tlsCtx := &tlsv3.UpstreamTlsContext{}
	require.NoError(t, ts.GetTypedConfig().UnmarshalTo(tlsCtx))
	return tlsCtx
}

// newServerWithForwardProxyConfig seeds a fake client with an AIGatewayRoute whose parent Gateway
// references a GatewayConfig configuring fp, plus the given objects (Secrets).
func newServerWithForwardProxyConfig(t *testing.T, fp *aigv1b1.GatewayConfigForwardProxy, secrets ...*corev1.Secret) *Server {
	t.Helper()
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: "eg-gateway"}},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend-a"}}},
			},
		},
	}))
	require.NoError(t, c.Create(t.Context(), gwGateway("eg-gateway", "gwconfig")))
	require.NoError(t, c.Create(t.Context(), &aigv1b1.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "gwconfig", Namespace: "default"},
		Spec:       aigv1b1.GatewayConfigSpec{ForwardProxy: fp},
	}))
	for _, secret := range secrets {
		require.NoError(t, c.Create(t.Context(), secret))
	}
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	return s
}

func secretWith(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func basicAuth(secretName string) *aigv1b1.GatewayConfigForwardProxyAuth {
	return &aigv1b1.GatewayConfigForwardProxyAuth{
		Basic: &aigv1b1.GatewayConfigForwardProxyBasicAuth{SecretRef: gwapiv1.SecretObjectReference{Name: gwapiv1.ObjectName(secretName)}},
	}
}

func TestMaybeModifyCluster_forwardProxyTLS(t *testing.T) {
	t.Run("hostname address: default SNI and system trust store", func(t *testing.T) {
		s := newServerWithForwardProxyConfig(t, &aigv1b1.GatewayConfigForwardProxy{
			Address: "proxy.corp:3128",
			TLS:     &aigv1b1.GatewayConfigForwardProxyTLS{},
		})
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		secrets := forwardProxySecrets{}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil, secrets))

		ts := cluster.TransportSocketMatches[0].TransportSocket
		// The typed fields are still decoded as usual.
		wrapper := unwrapHTTP11Proxy(t, ts)
		requireProxyAddress(t, wrapper, "proxy.corp", 3128)
		require.Equal(t, tlsTransportSocketName, wrapper.GetTransportSocket().GetName())

		ext := decodeProxyExtensions(t, ts)
		require.Nil(t, ext.authorization)
		tlsCtx := proxyTLSContext(t, ext.transportSocket)
		require.Equal(t, "proxy.corp", tlsCtx.GetSni())
		require.Equal(t, []string{"http/1.1"}, tlsCtx.GetCommonTlsContext().GetAlpnProtocols())
		vc := tlsCtx.GetCommonTlsContext().GetValidationContext()
		require.Equal(t, systemCACertPath, vc.GetTrustedCa().GetFilename())
		require.Len(t, vc.GetMatchTypedSubjectAltNames(), 1)
		require.Equal(t, tlsv3.SubjectAltNameMatcher_DNS, vc.GetMatchTypedSubjectAltNames()[0].GetSanType())
		require.Equal(t, "proxy.corp", vc.GetMatchTypedSubjectAltNames()[0].GetMatcher().GetExact())
		require.Empty(t, tlsCtx.GetCommonTlsContext().GetTlsCertificateSdsSecretConfigs())
		require.Empty(t, secrets)
	})

	t.Run("IP address: no SNI, IP SAN", func(t *testing.T) {
		s := newServerWithForwardProxyConfig(t, &aigv1b1.GatewayConfigForwardProxy{
			Address: "10.0.0.9:3128",
			TLS:     &aigv1b1.GatewayConfigForwardProxyTLS{},
		})
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil, forwardProxySecrets{}))

		tlsCtx := proxyTLSContext(t, decodeProxyExtensions(t, cluster.TransportSocketMatches[0].TransportSocket).transportSocket)
		require.Empty(t, tlsCtx.GetSni())
		san := tlsCtx.GetCommonTlsContext().GetValidationContext().GetMatchTypedSubjectAltNames()[0]
		require.Equal(t, tlsv3.SubjectAltNameMatcher_IP_ADDRESS, san.GetSanType())
		require.Equal(t, "10.0.0.9", san.GetMatcher().GetExact())
	})

	t.Run("explicit SNI, CA and client certificate", func(t *testing.T) {
		s := newServerWithForwardProxyConfig(t, &aigv1b1.GatewayConfigForwardProxy{
			Address: "10.0.0.9:3128",
			TLS: &aigv1b1.GatewayConfigForwardProxyTLS{
				SNI:                  ptr.To("proxy.internal"),
				CACertificateRef:     &gwapiv1.SecretObjectReference{Name: "proxy-ca"},
				ClientCertificateRef: &gwapiv1.SecretObjectReference{Name: "proxy-client"},
			},
		},
			secretWith("proxy-ca", map[string]string{"ca.crt": "CA-PEM"}),
			secretWith("proxy-client", map[string]string{"tls.crt": "CERT-PEM", "tls.key": "KEY-PEM"}),
		)
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		secrets := forwardProxySecrets{}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil, secrets))

		tlsCtx := proxyTLSContext(t, decodeProxyExtensions(t, cluster.TransportSocketMatches[0].TransportSocket).transportSocket)
		require.Equal(t, "proxy.internal", tlsCtx.GetSni())
		vc := tlsCtx.GetCommonTlsContext().GetValidationContext()
		require.Equal(t, []byte("CA-PEM"), vc.GetTrustedCa().GetInlineBytes())
		require.Equal(t, "proxy.internal", vc.GetMatchTypedSubjectAltNames()[0].GetMatcher().GetExact())

		const certName = "aigw-forward-proxy/default/gwconfig/client-cert"
		sds := tlsCtx.GetCommonTlsContext().GetTlsCertificateSdsSecretConfigs()
		require.Len(t, sds, 1)
		require.Equal(t, certName, sds[0].GetName())
		require.NotNil(t, sds[0].GetSdsConfig().GetAds())
		require.Contains(t, secrets, certName)
		cert := secrets[certName].GetTlsCertificate()
		require.Equal(t, []byte("CERT-PEM"), cert.GetCertificateChain().GetInlineBytes())
		require.Equal(t, []byte("KEY-PEM"), cert.GetPrivateKey().GetInlineBytes())
	})
}

func TestMaybeModifyCluster_forwardProxyAuth(t *testing.T) {
	const authSecretName = "aigw-forward-proxy/default/gwconfig/auth"

	t.Run("basic auth", func(t *testing.T) {
		s := newServerWithForwardProxyConfig(t, &aigv1b1.GatewayConfigForwardProxy{
			Address: "proxy.corp:3128",
			Auth:    basicAuth("proxy-creds"),
		}, secretWith("proxy-creds", map[string]string{"username": "user", "password": "pass"}))
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		secrets := forwardProxySecrets{}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil, secrets))

		ext := decodeProxyExtensions(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.Nil(t, ext.transportSocket) // Plaintext connection to the proxy.
		require.Equal(t, authSecretName, ext.authorization.GetName())
		require.NotNil(t, ext.authorization.GetSdsConfig().GetAds())
		require.Equal(t, corev3.ApiVersion_V3, ext.authorization.GetSdsConfig().GetResourceApiVersion())
		require.Len(t, secrets, 1)
		// base64("user:pass") == "dXNlcjpwYXNz"
		require.Equal(t, "Basic dXNlcjpwYXNz", secrets[authSecretName].GetGenericSecret().GetSecret().GetInlineString())
	})

	t.Run("header value", func(t *testing.T) {
		s := newServerWithForwardProxyConfig(t, &aigv1b1.GatewayConfigForwardProxy{
			Address: "proxy.corp:3128",
			TLS:     &aigv1b1.GatewayConfigForwardProxyTLS{},
			Auth: &aigv1b1.GatewayConfigForwardProxyAuth{
				HeaderValueRef: &gwapiv1.SecretObjectReference{Name: "proxy-token"},
			},
		}, secretWith("proxy-token", map[string]string{"value": "Bearer abc\n"}))
		cluster := clusterWithTLSMatch(t, "api.openai.com")
		secrets := forwardProxySecrets{}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, nil, secrets))

		ext := decodeProxyExtensions(t, cluster.TransportSocketMatches[0].TransportSocket)
		require.NotNil(t, ext.transportSocket)
		require.Equal(t, authSecretName, ext.authorization.GetName())
		require.Equal(t, "Bearer abc", secrets[authSecretName].GetGenericSecret().GetSecret().GetInlineString())
	})
}

func TestMaybeModifyCluster_forwardProxySecretErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fp      *aigv1b1.GatewayConfigForwardProxy
		secrets []*corev1.Secret
		wantErr string
	}{
		{
			name:    "missing secret",
			fp:      &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", Auth: basicAuth("proxy-creds")},
			wantErr: "proxy-creds",
		},
		{
			name:    "missing key",
			fp:      &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", Auth: basicAuth("proxy-creds")},
			secrets: []*corev1.Secret{secretWith("proxy-creds", map[string]string{"username": "user"})},
			wantErr: `missing the "password" key`,
		},
		{
			name:    "colon in username",
			fp:      &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", Auth: basicAuth("proxy-creds")},
			secrets: []*corev1.Secret{secretWith("proxy-creds", map[string]string{"username": "us:er", "password": "pass"})},
			wantErr: "must not contain a colon",
		},
		{
			name: "cross-namespace reference",
			fp: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", TLS: &aigv1b1.GatewayConfigForwardProxyTLS{
				CACertificateRef: &gwapiv1.SecretObjectReference{Name: "proxy-ca", Namespace: ptr.To[gwapiv1.Namespace]("other")},
			}},
			wantErr: "must be in the GatewayConfig namespace",
		},
		{
			name: "missing client certificate key",
			fp: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128", TLS: &aigv1b1.GatewayConfigForwardProxyTLS{
				ClientCertificateRef: &gwapiv1.SecretObjectReference{Name: "proxy-client"},
			}},
			secrets: []*corev1.Secret{secretWith("proxy-client", map[string]string{"tls.crt": "CERT"})},
			wantErr: `missing the "tls.key" key`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServerWithForwardProxyConfig(t, tc.fp, tc.secrets...)
			err := s.maybeModifyCluster(t.Context(), clusterWithTLSMatch(t, "api.openai.com"), nil, forwardProxySecrets{})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestMarshalHTTP11ProxyTransport(t *testing.T) {
	base := &http11proxyv3.Http11ProxyUpstreamTransport{
		TransportSocket:     tlsTransportSocket(t, "api.openai.com"),
		DefaultProxyAddress: &corev3.Address{Address: &corev3.Address_SocketAddress{SocketAddress: &corev3.SocketAddress{Address: "proxy.corp"}}},
	}

	t.Run("without extensions it matches the typed encoding", func(t *testing.T) {
		got, err := marshalHTTP11ProxyTransport(base, nil, nil)
		require.NoError(t, err)
		want, err := toAny(base)
		require.NoError(t, err)
		require.Equal(t, want.GetTypeUrl(), got.GetTypeUrl())
		require.Equal(t, want.GetValue(), got.GetValue())
	})

	t.Run("extensions survive a decode and re-encode of the cluster", func(t *testing.T) {
		got, err := marshalHTTP11ProxyTransport(base, tlsTransportSocket(t, "proxy.corp"), sdsSecretConfig("auth"))
		require.NoError(t, err)
		cluster := &clusterv3.Cluster{TransportSocket: &corev3.TransportSocket{
			Name:       http11ProxyTransportSocketName,
			ConfigType: &corev3.TransportSocket_TypedConfig{TypedConfig: got},
		}}
		// Simulate Envoy Gateway handling the returned cluster.
		b, err := proto.Marshal(cluster)
		require.NoError(t, err)
		decoded := &clusterv3.Cluster{}
		require.NoError(t, proto.Unmarshal(b, decoded))
		// Unpacking and re-packing the wrapper keeps its unknown fields too.
		wrapper := unwrapHTTP11Proxy(t, decoded.TransportSocket)
		require.NoError(t, decoded.TransportSocket.GetTypedConfig().MarshalFrom(wrapper))

		ext := decodeProxyExtensions(t, decoded.TransportSocket)
		require.Equal(t, "proxy.corp", proxyTLSContext(t, ext.transportSocket).GetSni())
		require.Equal(t, "auth", ext.authorization.GetName())
	})
}

func TestServerPostTranslateModify_forwardProxySecrets(t *testing.T) {
	s := newServerWithForwardProxyConfig(t, &aigv1b1.GatewayConfigForwardProxy{
		Address: "proxy.corp:3128",
		Auth:    basicAuth("proxy-creds"),
	}, secretWith("proxy-creds", map[string]string{"username": "user", "password": "pass"}))

	clusterA := clusterWithTLSMatch(t, "api.openai.com")
	clusterB := clusterWithTLSMatch(t, "api.openai.com")
	clusterB.Name = "httproute/default/myroute/rule/0/backend/0"
	existing := &tlsv3.Secret{Name: "eg-secret"}
	res, err := s.PostTranslateModify(t.Context(), &egextension.PostTranslateModifyRequest{
		Clusters: []*clusterv3.Cluster{clusterA, clusterB},
		Secrets:  []*tlsv3.Secret{existing},
	})
	require.NoError(t, err)

	// Both clusters reference the same secret, which is emitted once next to the existing ones.
	require.Len(t, res.Secrets, 2)
	require.Equal(t, existing, res.Secrets[0])
	require.Equal(t, "aigw-forward-proxy/default/gwconfig/auth", res.Secrets[1].GetName())
	for _, c := range []*clusterv3.Cluster{clusterA, clusterB} {
		ext := decodeProxyExtensions(t, c.TransportSocketMatches[0].TransportSocket)
		require.Equal(t, res.Secrets[1].GetName(), ext.authorization.GetName())
	}
}
