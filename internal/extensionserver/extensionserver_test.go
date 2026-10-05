// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	mutation_rulesv3 "github.com/envoyproxy/go-control-plane/envoy/config/common/mutation_rules/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	header_mutationv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_mutation/v3"
	htomv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_to_metadata/v3"
	upstream_codecv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/upstream_codec/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	httpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// mustToAny marshals the provided message to an Any message.
func mustToAny(t *testing.T, msg proto.Message) *anypb.Any {
	b, err := proto.Marshal(msg)
	require.NoError(t, err)
	const envoyAPIPrefix = "type.googleapis.com/"
	return &anypb.Any{
		TypeUrl: envoyAPIPrefix + string(msg.ProtoReflect().Descriptor().FullName()),
		Value:   b,
	}
}

func newFakeClient() client.Client {
	builder := fake.NewClientBuilder().WithScheme(controller.Scheme).
		WithStatusSubresource(&aigv1b1.AIGatewayRoute{}).
		WithStatusSubresource(&aigv1b1.AIServiceBackend{}).
		WithStatusSubresource(&aigv1b1.BackendSecurityPolicy{})
	return builder.Build()
}

const udsPath = "/tmp/uds/test.sock"

func TestNew(t *testing.T) {
	s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	require.NotNil(t, s)
}

func TestParseHostPort(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		wantHost string
		wantPort uint32
		wantErr  string
	}{
		{name: "hostname without port", address: "ratelimit", wantHost: "ratelimit", wantPort: defaultQuotaRateLimitServicePort},
		{name: "IPv6 without port", address: "2001:db8::1", wantHost: "2001:db8::1", wantPort: defaultQuotaRateLimitServicePort},
		{name: "IPv6 with port", address: "[2001:db8::1]:9090", wantHost: "2001:db8::1", wantPort: 9090},
		{name: "empty address", address: "", wantErr: "invalid host:port"},
		{name: "missing IPv6 closing bracket", address: "[2001:db8::1", wantErr: "invalid host:port"},
		{name: "multiple ports", address: "ratelimit:8080:9090", wantErr: "invalid host:port"},
		{name: "empty host", address: ":8080", wantErr: "host must be non-empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := parseHostPort(tt.address)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantHost, host)
			require.Equal(t, tt.wantPort, port)
		})
	}
}

func TestCheck(t *testing.T) {
	s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	_, err = s.Check(t.Context(), nil)
	require.NoError(t, err)
}

func TestWatch(t *testing.T) {
	s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	err = s.Watch(nil, nil)
	require.Error(t, err)
	require.Equal(t, "rpc error: code = Unimplemented desc = Watch is not implemented", err.Error())
}

func TestServerPostTranslateModify(t *testing.T) {
	t.Run("existing", func(t *testing.T) {
		s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		req := &egextension.PostTranslateModifyRequest{Clusters: []*clusterv3.Cluster{{Name: extProcUDSClusterName}}}
		res, err := s.PostTranslateModify(t.Context(), req)
		require.Equal(t, &egextension.PostTranslateModifyResponse{
			Clusters: req.Clusters, Secrets: req.Secrets,
		}, res)
		require.NoError(t, err)
	})
	t.Run("not existing", func(t *testing.T) {
		s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		res, err := s.PostTranslateModify(t.Context(), &egextension.PostTranslateModifyRequest{
			Clusters: []*clusterv3.Cluster{{Name: "foo"}},
		})
		require.NotNil(t, res)
		require.NoError(t, err)
		require.Len(t, res.Clusters, 2)
		require.Equal(t, "foo", res.Clusters[0].Name)
		require.Equal(t, extProcUDSClusterName, res.Clusters[1].Name)
	})
}

func Test_maybeModifyCluster(t *testing.T) {
	c := newFakeClient()

	// Create some fake AIGatewayRoute objects.
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myroute",
			Namespace: "ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "aaa", Priority: ptr.To[uint32](0)},
						{Name: "to-be-ignored", Weight: ptr.To[int32](0)},
						{Name: "bbb", Priority: ptr.To[uint32](1)},
					},
				},
			},
		},
	}))

	for _, tc := range []struct {
		c      *clusterv3.Cluster
		errLog string
	}{
		{c: &clusterv3.Cluster{}, errLog: "non-ai-gateway cluster name"},
		{c: &clusterv3.Cluster{
			Name: "httproute/ns/name/rule/invalid",
		}, errLog: "invalid HTTPRoute rule index"},
		{c: &clusterv3.Cluster{
			Name: "httproute/ns/myroute/rule/99999",
		}, errLog: `HTTPRoute rule index out of range`},
		{c: &clusterv3.Cluster{
			Name: "httproute/ns/myroute/rule/0",
		}, errLog: `LoadAssignment is nil`},
	} {
		t.Run("error/"+tc.errLog, func(t *testing.T) {
			var buf bytes.Buffer
			s, err := New(c, logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{})), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
			require.NoError(t, err)
			err = s.maybeModifyCluster(t.Context(), tc.c, nil)
			require.NoError(t, err)
			t.Logf("buf: %s", buf.String())
			require.Contains(t, buf.String(), tc.errLog)
		})
	}
	for _, tc := range []struct {
		name        string
		cluster     *clusterv3.Cluster
		expectedLog string
		expected    *clusterv3.Cluster
	}{
		{
			name: "nil LoadAssignment sets cluster metadata",
			// In standalone mode (aigw run), EDS-managed endpoints have LoadAssignment=nil.
			// The extension server must set cluster-level metadata so the upstream ext_proc
			// filter can resolve the backend name via the cluster metadata fallback path.
			cluster: &clusterv3.Cluster{
				Name: "httproute/ns/myroute/rule/0",
			},
			expectedLog: "msg=\"LoadAssignment is nil, setting cluster-level metadata\" logger=envoy-gateway-extension-server cluster_name=httproute/ns/myroute/rule/0\n",
			expected: &clusterv3.Cluster{
				Name: "httproute/ns/myroute/rule/0",
				Metadata: &corev3.Metadata{
					FilterMetadata: map[string]*structpb.Struct{
						internalapi.InternalEndpointMetadataNamespace: {
							Fields: map[string]*structpb.Value{
								internalapi.InternalMetadataBackendNameKey: structpb.NewStringValue(
									internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 0),
								),
							},
						},
					},
				},
				TypedExtensionProtocolOptions: map[string]*anypb.Any{
					"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": mustToAny(t, &httpv3.HttpProtocolOptions{
						UpstreamProtocolOptions: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
							ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
						}},
						HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
							{
								Name: aiGatewayExtProcName,
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &extprocv3.ExternalProcessor{
										MetadataOptions: &extprocv3.MetadataOptions{
											ReceivingNamespaces: &extprocv3.MetadataOptions_MetadataNamespaces{
												Untyped: []string{aigv1b1.AIGatewayFilterMetadataNamespace},
											},
										},
										AllowModeOverride: true,
										RequestAttributes: []string{
											internalapi.XDSUpstreamHostMetadataBackendNamePath,
											internalapi.XDSClusterMetadataBackendNamePath,
											internalapi.XDSUpstreamHostMetadataUpstreamHostPath,
											internalapi.XDSRouteMetadataRouteNamePath,
										},
										ProcessingMode: &extprocv3.ProcessingMode{
											RequestHeaderMode:  extprocv3.ProcessingMode_SEND,
											RequestBodyMode:    extprocv3.ProcessingMode_NONE,
											ResponseHeaderMode: extprocv3.ProcessingMode_SKIP,
											ResponseBodyMode:   extprocv3.ProcessingMode_NONE,
										},
										MessageTimeout: durationpb.New(10 * time.Second),
										GrpcService: &corev3.GrpcService{
											TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
												EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
													ClusterName: extProcUDSClusterName,
												},
											},
											Timeout: durationpb.New(30 * time.Second),
										},
									}),
								},
							},
							{
								Name: "envoy.filters.http.header_mutation",
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &header_mutationv3.HeaderMutation{
										Mutations: &header_mutationv3.Mutations{
											RequestMutations: []*mutation_rulesv3.HeaderMutation{
												{
													Action: &mutation_rulesv3.HeaderMutation_Append{
														Append: &corev3.HeaderValueOption{
															AppendAction: corev3.HeaderValueOption_ADD_IF_ABSENT,
															Header: &corev3.HeaderValue{
																Key:   "content-length",
																Value: `%DYNAMIC_METADATA(` + aigv1b1.AIGatewayFilterMetadataNamespace + `:content_length)%`,
															},
														},
													},
												},
											},
										},
									}),
								},
							},
							{
								Name: "envoy.filters.http.upstream_codec",
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &upstream_codecv3.UpstreamCodec{}),
								},
							},
						},
					}),
				},
			},
		},
		{
			name: "ok with LoadAssignment",
			cluster: &clusterv3.Cluster{
				Name: "httproute/ns/myroute/rule/0",
				LoadAssignment: &endpointv3.ClusterLoadAssignment{
					Endpoints: []*endpointv3.LocalityLbEndpoints{
						{
							LbEndpoints: []*endpointv3.LbEndpoint{
								{HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
									Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
										SocketAddress: &corev3.SocketAddress{Address: "aaa.bedrock-runtime.us-east-1.amazonaws.com"},
									}},
								}}},
							},
						},
						{
							LbEndpoints: []*endpointv3.LbEndpoint{
								{HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
									Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
										SocketAddress: &corev3.SocketAddress{Address: "bbb.bedrock-runtime.us-east-1.amazonaws.com"},
									}},
								}}},
							},
						},
					},
				},
			},
			expectedLog: "",
			expected: &clusterv3.Cluster{
				Name: "httproute/ns/myroute/rule/0",
				LoadAssignment: &endpointv3.ClusterLoadAssignment{
					Endpoints: []*endpointv3.LocalityLbEndpoints{
						{
							Priority: 0,
							LbEndpoints: []*endpointv3.LbEndpoint{
								{
									HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
										Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
											SocketAddress: &corev3.SocketAddress{Address: "aaa.bedrock-runtime.us-east-1.amazonaws.com"},
										}},
									}},
									Metadata: &corev3.Metadata{
										FilterMetadata: map[string]*structpb.Struct{
											internalapi.InternalEndpointMetadataNamespace: {
												Fields: map[string]*structpb.Value{
													internalapi.InternalMetadataBackendNameKey: structpb.NewStringValue(
														internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 0),
													),
													internalapi.InternalMetadataUpstreamHostKey: structpb.NewStringValue(
														"aaa.bedrock-runtime.us-east-1.amazonaws.com",
													),
												},
											},
										},
									},
								},
							},
						},
						{
							Priority: 1,
							LbEndpoints: []*endpointv3.LbEndpoint{
								{
									HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
										Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
											SocketAddress: &corev3.SocketAddress{Address: "bbb.bedrock-runtime.us-east-1.amazonaws.com"},
										}},
									}},
									Metadata: &corev3.Metadata{
										FilterMetadata: map[string]*structpb.Struct{
											internalapi.InternalEndpointMetadataNamespace: {
												Fields: map[string]*structpb.Value{
													internalapi.InternalMetadataBackendNameKey: structpb.NewStringValue(
														internalapi.PerRouteRuleRefBackendName("ns", "bbb", "myroute", 0, 2),
													),
													internalapi.InternalMetadataUpstreamHostKey: structpb.NewStringValue(
														"bbb.bedrock-runtime.us-east-1.amazonaws.com",
													),
												},
											},
										},
									},
								},
							},
						},
					},
				},
				TypedExtensionProtocolOptions: map[string]*anypb.Any{
					"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": mustToAny(t, &httpv3.HttpProtocolOptions{
						UpstreamProtocolOptions: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
							ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
						}},
						HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
							{
								Name: aiGatewayExtProcName,
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &extprocv3.ExternalProcessor{
										MetadataOptions: &extprocv3.MetadataOptions{
											ReceivingNamespaces: &extprocv3.MetadataOptions_MetadataNamespaces{
												Untyped: []string{aigv1b1.AIGatewayFilterMetadataNamespace},
											},
										},
										AllowModeOverride: true,
										RequestAttributes: []string{
											internalapi.XDSUpstreamHostMetadataBackendNamePath,
											internalapi.XDSClusterMetadataBackendNamePath,
											internalapi.XDSUpstreamHostMetadataUpstreamHostPath,
											internalapi.XDSRouteMetadataRouteNamePath,
										},
										ProcessingMode: &extprocv3.ProcessingMode{
											RequestHeaderMode:  extprocv3.ProcessingMode_SEND,
											RequestBodyMode:    extprocv3.ProcessingMode_NONE,
											ResponseHeaderMode: extprocv3.ProcessingMode_SKIP,
											ResponseBodyMode:   extprocv3.ProcessingMode_NONE,
										},
										MessageTimeout: durationpb.New(10 * time.Second),
										GrpcService: &corev3.GrpcService{
											TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
												EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
													ClusterName: extProcUDSClusterName,
												},
											},
											Timeout: durationpb.New(30 * time.Second),
										},
									}),
								},
							},
							{
								Name: "envoy.filters.http.header_mutation",
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &header_mutationv3.HeaderMutation{
										Mutations: &header_mutationv3.Mutations{
											RequestMutations: []*mutation_rulesv3.HeaderMutation{
												{
													Action: &mutation_rulesv3.HeaderMutation_Append{
														Append: &corev3.HeaderValueOption{
															AppendAction: corev3.HeaderValueOption_ADD_IF_ABSENT,
															Header: &corev3.HeaderValue{
																Key:   "content-length",
																Value: `%DYNAMIC_METADATA(` + aigv1b1.AIGatewayFilterMetadataNamespace + `:content_length)%`,
															},
														},
													},
												},
											},
										},
									}),
								},
							},
							{
								Name: "envoy.filters.http.upstream_codec",
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &upstream_codecv3.UpstreamCodec{}),
								},
							},
						},
					}),
				},
			},
		},
		{
			// Regression test: httpRouteRule.BackendRefs (read fresh from
			// the AIGatewayRoute) and cluster.LoadAssignment (translated by
			// Envoy Gateway) can be a revision apart, so LoadAssignment.
			// Endpoints can be shorter than the rule's non-zero-weight
			// BackendRefs. maybeModifyCluster must log and stop, not index
			// out of range - this reproduces the "index out of range"
			// panic the guard prevents.
			name: "fewer LoadAssignment endpoints than non-zero-weight backendRefs logs and stops",
			cluster: &clusterv3.Cluster{
				Name: "httproute/ns/myroute/rule/0",
				LoadAssignment: &endpointv3.ClusterLoadAssignment{
					Endpoints: []*endpointv3.LocalityLbEndpoints{
						{
							LbEndpoints: []*endpointv3.LbEndpoint{
								{HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
									Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
										SocketAddress: &corev3.SocketAddress{Address: "aaa.bedrock-runtime.us-east-1.amazonaws.com"},
									}},
								}}},
							},
						},
						// Only one LocalityLbEndpoints entry, even though the rule
						// has two non-zero-weight backendRefs ("aaa", "bbb") -
						// "bbb" has no entry here, as when the cluster was
						// translated from an earlier revision of the rule that
						// had one backendRef.
					},
				},
			},
			expectedLog: "msg=\"cluster LoadAssignment has fewer endpoint groups than non-zero-weight backendRefs\" logger=envoy-gateway-extension-server cluster_name=httproute/ns/myroute/rule/0 backend_index=2 load_assignment_endpoints=1\n",
			expected: &clusterv3.Cluster{
				Name: "httproute/ns/myroute/rule/0",
				LoadAssignment: &endpointv3.ClusterLoadAssignment{
					Endpoints: []*endpointv3.LocalityLbEndpoints{
						{
							// "aaa" still gets its metadata stamped - only the
							// backend(s) past the LoadAssignment's length are
							// skipped, not the whole cluster.
							Priority: 0,
							LbEndpoints: []*endpointv3.LbEndpoint{
								{
									HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
										Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
											SocketAddress: &corev3.SocketAddress{Address: "aaa.bedrock-runtime.us-east-1.amazonaws.com"},
										}},
									}},
									Metadata: &corev3.Metadata{
										FilterMetadata: map[string]*structpb.Struct{
											internalapi.InternalEndpointMetadataNamespace: {
												Fields: map[string]*structpb.Value{
													internalapi.InternalMetadataBackendNameKey: structpb.NewStringValue(
														internalapi.PerRouteRuleRefBackendName("ns", "aaa", "myroute", 0, 0),
													),
													internalapi.InternalMetadataUpstreamHostKey: structpb.NewStringValue(
														"aaa.bedrock-runtime.us-east-1.amazonaws.com",
													),
												},
											},
										},
									},
								},
							},
						},
					},
				},
				TypedExtensionProtocolOptions: map[string]*anypb.Any{
					"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": mustToAny(t, &httpv3.HttpProtocolOptions{
						UpstreamProtocolOptions: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
							ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
						}},
						HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
							{
								Name: aiGatewayExtProcName,
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &extprocv3.ExternalProcessor{
										MetadataOptions: &extprocv3.MetadataOptions{
											ReceivingNamespaces: &extprocv3.MetadataOptions_MetadataNamespaces{
												Untyped: []string{aigv1b1.AIGatewayFilterMetadataNamespace},
											},
										},
										AllowModeOverride: true,
										RequestAttributes: []string{
											internalapi.XDSUpstreamHostMetadataBackendNamePath,
											internalapi.XDSClusterMetadataBackendNamePath,
											internalapi.XDSUpstreamHostMetadataUpstreamHostPath,
											internalapi.XDSRouteMetadataRouteNamePath,
										},
										ProcessingMode: &extprocv3.ProcessingMode{
											RequestHeaderMode:  extprocv3.ProcessingMode_SEND,
											RequestBodyMode:    extprocv3.ProcessingMode_NONE,
											ResponseHeaderMode: extprocv3.ProcessingMode_SKIP,
											ResponseBodyMode:   extprocv3.ProcessingMode_NONE,
										},
										MessageTimeout: durationpb.New(10 * time.Second),
										GrpcService: &corev3.GrpcService{
											TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
												EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
													ClusterName: extProcUDSClusterName,
												},
											},
											Timeout: durationpb.New(30 * time.Second),
										},
									}),
								},
							},
							{
								Name: "envoy.filters.http.header_mutation",
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &header_mutationv3.HeaderMutation{
										Mutations: &header_mutationv3.Mutations{
											RequestMutations: []*mutation_rulesv3.HeaderMutation{
												{
													Action: &mutation_rulesv3.HeaderMutation_Append{
														Append: &corev3.HeaderValueOption{
															AppendAction: corev3.HeaderValueOption_ADD_IF_ABSENT,
															Header: &corev3.HeaderValue{
																Key:   "content-length",
																Value: `%DYNAMIC_METADATA(` + aigv1b1.AIGatewayFilterMetadataNamespace + `:content_length)%`,
															},
														},
													},
												},
											},
										},
									}),
								},
							},
							{
								Name: "envoy.filters.http.upstream_codec",
								ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
									TypedConfig: mustToAny(t, &upstream_codecv3.UpstreamCodec{}),
								},
							},
						},
					}),
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
						return slog.Attr{}
					}
					return a
				},
			})
			s, err := New(c, logr.FromSlogHandler(handler), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
			require.NoError(t, err)
			err = s.maybeModifyCluster(t.Context(), tc.cluster, nil)
			require.NoError(t, err)

			require.Equal(t, tc.expectedLog, buf.String())
			require.Equal(t, tc.expected, tc.cluster)
		})
	}
}

func TestParseAIGatewayClusterName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cluster  string
		expected aiGatewayClusterName
		wantErr  bool
	}{
		{
			name:    "route-level cluster",
			cluster: "httproute/ns/route/rule/3",
			expected: aiGatewayClusterName{
				namespace:       "ns",
				routeName:       "route",
				ruleIndex:       3,
				backendRefIndex: noBackendRefIndex,
			},
		},
		{
			name:    "per-backend cluster",
			cluster: "httproute/ns/route/rule/3/backend/2",
			expected: aiGatewayClusterName{
				namespace:       "ns",
				routeName:       "route",
				ruleIndex:       3,
				backendRefIndex: 2,
			},
		},
		{name: "missing backend marker", cluster: "httproute/ns/route/rule/3/not-backend/2", wantErr: true},
		{name: "negative backend index", cluster: "httproute/ns/route/rule/3/backend/-1", wantErr: true},
		{name: "invalid rule index", cluster: "httproute/ns/route/rule/nope", wantErr: true},
		{name: "non route cluster", cluster: "service/ns/route", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := parseAIGatewayClusterName(tc.cluster)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, actual)
		})
	}
}

func TestMaybeModifyClusterPerBackendClusterName(t *testing.T) {
	newServer := func(t *testing.T) *Server {
		t.Helper()
		c := newFakeClient()
		require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
			Spec: aigv1b1.AIGatewayRouteSpec{Rules: []aigv1b1.AIGatewayRouteRule{{
				BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
					{Name: "primary", Priority: ptr.To[uint32](0)},
					{Name: "fallback", Priority: ptr.To[uint32](1)},
				},
			}}},
		}))
		s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		return s
	}

	assertBackendName := func(t *testing.T, metadata *corev3.Metadata, expected string) {
		t.Helper()
		require.NotNil(t, metadata)
		filterMetadata := metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
		require.NotNil(t, filterMetadata)
		require.Equal(t, expected, filterMetadata.Fields[internalapi.InternalMetadataBackendNameKey].GetStringValue())
	}

	t.Run("sets endpoint metadata for the selected backend", func(t *testing.T) {
		cluster := &clusterv3.Cluster{
			Name: "httproute/ns/myroute/rule/0/backend/1",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{Endpoints: []*endpointv3.LocalityLbEndpoints{{
				LbEndpoints: []*endpointv3.LbEndpoint{{}},
			}}},
		}
		require.NoError(t, newServer(t).maybeModifyCluster(t.Context(), cluster, nil))
		require.Equal(t, uint32(1), cluster.LoadAssignment.Endpoints[0].Priority)
		assertBackendName(t, cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].Metadata,
			internalapi.PerRouteRuleRefBackendName("ns", "fallback", "myroute", 0, 1))
		require.Contains(t, cluster.TypedExtensionProtocolOptions, "envoy.extensions.upstreams.http.v3.HttpProtocolOptions")
	})

	t.Run("sets cluster metadata for EDS-managed endpoints", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "httproute/ns/myroute/rule/0/backend/0"}
		require.NoError(t, newServer(t).maybeModifyCluster(t.Context(), cluster, nil))
		assertBackendName(t, cluster.Metadata,
			internalapi.PerRouteRuleRefBackendName("ns", "primary", "myroute", 0, 0))
		require.Contains(t, cluster.TypedExtensionProtocolOptions, "envoy.extensions.upstreams.http.v3.HttpProtocolOptions")
	})
}

// Helper function to create an InferencePool ExtensionResource.
func createInferencePoolExtensionResource(name, namespace string) *egextension.ExtensionResource {
	unstructuredObj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "inference.networking.k8s.io/v1",
			"kind":       "InferencePool",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"targetPortNumber": int32(8080),
				"selector": map[string]any{
					"app": "test-inference",
				},
				"endpointPickerRef": map[string]any{
					"name": "test-epp",
				},
			},
		},
	}

	// Marshal to JSON bytes.
	jsonBytes, _ := unstructuredObj.MarshalJSON()
	return &egextension.ExtensionResource{
		UnstructuredBytes: jsonBytes,
	}
}

// createInferencePoolExtensionResourceNoEPPRef is like createInferencePoolExtensionResource but
// omits spec.endpointPickerRef, mirroring an InferencePool created without one -- a legal,
// schema-valid state as of Gateway API Inference Extension v1.5.0 (the field is optional).
func createInferencePoolExtensionResourceNoEPPRef(name, namespace string) *egextension.ExtensionResource {
	unstructuredObj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "inference.networking.k8s.io/v1",
			"kind":       "InferencePool",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"targetPortNumber": int32(8080),
				"selector": map[string]any{
					"app": "test-inference",
				},
			},
		},
	}
	jsonBytes, _ := unstructuredObj.MarshalJSON()
	return &egextension.ExtensionResource{
		UnstructuredBytes: jsonBytes,
	}
}

// createInferencePoolExtensionResourceWithAppProtocol is like createInferencePoolExtensionResource but
// sets an explicit spec.appProtocol, so tests can exercise the pool's appProtocol-dependent behavior.
func createInferencePoolExtensionResourceWithAppProtocol(name, namespace, appProtocol string) *egextension.ExtensionResource {
	unstructuredObj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "inference.networking.k8s.io/v1",
			"kind":       "InferencePool",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"targetPortNumber": int32(8080),
				"selector": map[string]any{
					"app": "test-inference",
				},
				"appProtocol": appProtocol,
				"endpointPickerRef": map[string]any{
					"name": "test-epp",
				},
			},
		},
	}

	jsonBytes, _ := unstructuredObj.MarshalJSON()
	return &egextension.ExtensionResource{
		UnstructuredBytes: jsonBytes,
	}
}

// TestMaybeModifyClusterExtended tests additional scenarios for maybeModifyCluster function.
func TestMaybeModifyClusterExtended(t *testing.T) {
	c := newFakeClient()

	// Create AIGatewayRoute with InferencePool backend.
	err := c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inference-route",
			Namespace: "test-ns",
		},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "inference-backend"},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	t.Run("AIGatewayRoute not found", func(t *testing.T) {
		var buf bytes.Buffer
		s, err := New(c, logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{})), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		cluster := &clusterv3.Cluster{Name: "httproute/test-ns/nonexistent-route/rule/0", Metadata: &corev3.Metadata{}}
		err = s.maybeModifyCluster(t.Context(), cluster, nil)
		require.NoError(t, err)
		require.Contains(t, buf.String(), "kipping non-AIGatewayRoute HTTPRoute cluster modification")
	})

	t.Run("cluster with InferencePool metadata and existing route", func(t *testing.T) {
		var buf bytes.Buffer
		s, err := New(c, logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{})), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		cluster := &clusterv3.Cluster{
			Name: "httproute/test-ns/inference-route/rule/0",
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					internalapi.InternalEndpointMetadataNamespace: {
						Fields: map[string]*structpb.Value{
							"per_route_rule_inference_pool": structpb.NewStringValue("test-ns/test-pool/test-epp/9002/duplex/false"),
						},
					},
				},
			},
		}

		err = s.maybeModifyCluster(t.Context(), cluster, nil)
		require.NoError(t, err)

		// Verify InferencePool metadata was added to cluster.
		require.NotNil(t, cluster.Metadata)
		require.NotNil(t, cluster.Metadata.FilterMetadata)
		require.Contains(t, cluster.Metadata.FilterMetadata, internalapi.InternalEndpointMetadataNamespace)

		// Verify HTTP protocol options were added.
		require.NotNil(t, cluster.TypedExtensionProtocolOptions)
		require.Contains(t, cluster.TypedExtensionProtocolOptions, "envoy.extensions.upstreams.http.v3.HttpProtocolOptions")
	})

	t.Run("cluster with existing HttpProtocolOptions", func(t *testing.T) {
		s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		// Create existing HttpProtocolOptions.
		existingPO := &httpv3.HttpProtocolOptions{
			UpstreamProtocolOptions: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{
				ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
					ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
				},
			},
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "existing-filter"},
			},
		}

		cluster := &clusterv3.Cluster{
			Name: "httproute/test-ns/inference-route/rule/0",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				Endpoints: []*endpointv3.LocalityLbEndpoints{
					{
						LbEndpoints: []*endpointv3.LbEndpoint{{}},
					},
				},
			},
			TypedExtensionProtocolOptions: map[string]*anypb.Any{
				"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": mustToAny(t, existingPO),
			},
		}

		err = s.maybeModifyCluster(t.Context(), cluster, nil)
		require.NoError(t, err)

		// Verify filters were added correctly.
		require.NotNil(t, cluster.TypedExtensionProtocolOptions)

		// Unmarshal and verify the updated protocol options.
		updatedPOAny := cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"]
		updatedPO := &httpv3.HttpProtocolOptions{}
		err = updatedPOAny.UnmarshalTo(updatedPO)
		require.NoError(t, err)

		// Should have ext_proc + header_mutation + existing_filter (which becomes the last filter).
		require.Len(t, updatedPO.HttpFilters, 3)
		require.Equal(t, "envoy.filters.http.ext_proc/aigateway", updatedPO.HttpFilters[0].Name)
		require.Equal(t, "envoy.filters.http.header_mutation", updatedPO.HttpFilters[1].Name)
		require.Equal(t, "existing-filter", updatedPO.HttpFilters[2].Name)
	})

	t.Run("cluster with existing ext_proc filter", func(t *testing.T) {
		s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		// Create HttpProtocolOptions with existing ext_proc filter.
		existingPO := &httpv3.HttpProtocolOptions{
			UpstreamProtocolOptions: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{
				ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
					ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
				},
			},
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.ext_proc/aigateway"},
			},
		}

		cluster := &clusterv3.Cluster{
			Name: "httproute/test-ns/inference-route/rule/0",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				Endpoints: []*endpointv3.LocalityLbEndpoints{
					{
						LbEndpoints: []*endpointv3.LbEndpoint{{}},
					},
				},
			},
			TypedExtensionProtocolOptions: map[string]*anypb.Any{
				"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": mustToAny(t, existingPO),
			},
		}

		err = s.maybeModifyCluster(t.Context(), cluster, nil)
		require.NoError(t, err)

		updatedPOAny := cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"]
		updatedPO := &httpv3.HttpProtocolOptions{}
		err = updatedPOAny.UnmarshalTo(updatedPO)
		require.NoError(t, err)

		// Our own filters are dropped and rebuilt, so the chain reflects the current config.
		require.Len(t, updatedPO.HttpFilters, 3)
		require.Equal(t, "envoy.filters.http.ext_proc/aigateway", updatedPO.HttpFilters[0].Name)
		require.Equal(t, "envoy.filters.http.header_mutation", updatedPO.HttpFilters[1].Name)
		require.Equal(t, "envoy.filters.http.upstream_codec", updatedPO.HttpFilters[2].Name)
	})

	t.Run("cluster with no existing HttpFilters", func(t *testing.T) {
		s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		cluster := &clusterv3.Cluster{
			Name: "httproute/test-ns/inference-route/rule/0",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				Endpoints: []*endpointv3.LocalityLbEndpoints{
					{
						LbEndpoints: []*endpointv3.LbEndpoint{{}},
					},
				},
			},
		}

		err = s.maybeModifyCluster(t.Context(), cluster, nil)
		require.NoError(t, err)

		// Verify filters were added correctly.
		require.NotNil(t, cluster.TypedExtensionProtocolOptions)

		updatedPOAny := cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"]
		updatedPO := &httpv3.HttpProtocolOptions{}
		err = updatedPOAny.UnmarshalTo(updatedPO)
		require.NoError(t, err)

		// Should have ext_proc + header_mutation + upstream_codec.
		require.Len(t, updatedPO.HttpFilters, 3)
		require.Equal(t, "envoy.filters.http.ext_proc/aigateway", updatedPO.HttpFilters[0].Name)
		require.Equal(t, "envoy.filters.http.header_mutation", updatedPO.HttpFilters[1].Name)
		require.Equal(t, "envoy.filters.http.upstream_codec", updatedPO.HttpFilters[2].Name)
	})

	t.Run("invalid HttpProtocolOptions unmarshal", func(t *testing.T) {
		var buf bytes.Buffer
		s, err := New(c, logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{})), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		// Create invalid Any message.
		invalidAny := &anypb.Any{
			TypeUrl: "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
			Value:   []byte("invalid-data"),
		}

		cluster := &clusterv3.Cluster{
			Name: "httproute/test-ns/inference-route/rule/0",
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				Endpoints: []*endpointv3.LocalityLbEndpoints{
					{
						LbEndpoints: []*endpointv3.LbEndpoint{{}},
					},
				},
			},
			TypedExtensionProtocolOptions: map[string]*anypb.Any{
				"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": invalidAny,
			},
		}

		err = s.maybeModifyCluster(t.Context(), cluster, nil)
		require.Error(t, err)
		require.Contains(t, buf.String(), "failed to unmarshal HttpProtocolOptions")
	})
}

// TestMaybeModifyListenerAndRoutes tests the maybeModifyListenerAndRoutes function.
func TestMaybeModifyListenerAndRoutes(t *testing.T) {
	s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	// Helper function to create a basic listener.
	createListener := func(name, routeConfigName string) *listenerv3.Listener {
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			RouteSpecifier: &httpconnectionmanagerv3.HttpConnectionManager_Rds{
				Rds: &httpconnectionmanagerv3.Rds{
					RouteConfigName: routeConfigName,
				},
			},
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.router"},
			},
		}

		return &listenerv3.Listener{
			Name: name,
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}
	}

	// Helper function to create a route with InferencePool metadata.
	createRouteWithInferencePool := func(routeName string) *routev3.Route {
		return &routev3.Route{
			Name: routeName,
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					internalapi.InternalEndpointMetadataNamespace: {
						Fields: map[string]*structpb.Value{
							"per_route_rule_inference_pool": structpb.NewStringValue("test-ns/test-pool/test-epp/9002/duplex/false"),
						},
					},
				},
			},
		}
	}

	t.Run("empty listeners and routes", func(_ *testing.T) {
		err := s.maybeModifyListenerAndRoutes([]*listenerv3.Listener{}, []*routev3.RouteConfiguration{})
		require.NoError(t, err)
	})

	t.Run("listener with envoy-gateway prefix is skipped", func(_ *testing.T) {
		listeners := []*listenerv3.Listener{
			createListener("envoy-gateway-proxy-stats-", "route-config"),
			createListener("envoy-gateway-proxy-ready-", "route-config"),
			createListener("normal-listener", "route-config"),
		}
		routes := []*routev3.RouteConfiguration{
			{
				Name: "route-config",
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name: "test-vh",
						Routes: []*routev3.Route{
							createRouteWithInferencePool("test-route"),
						},
					},
				},
			},
		}

		err := s.maybeModifyListenerAndRoutes(listeners, routes)
		require.NoError(t, err)
		// Should process only normal-listener, not envoy-gateway-listener.
	})

	t.Run("listener without RDS route config", func(_ *testing.T) {
		// Create listener without RDS configuration (using inline route config).
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			RouteSpecifier: &httpconnectionmanagerv3.HttpConnectionManager_RouteConfig{
				RouteConfig: &routev3.RouteConfiguration{
					Name: "inline-route",
				},
			},
		}

		listener := &listenerv3.Listener{
			Name: "inline-listener",
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}

		err := s.maybeModifyListenerAndRoutes([]*listenerv3.Listener{listener}, []*routev3.RouteConfiguration{})
		require.NoError(t, err)
		// Should handle gracefully when no RDS route config name is found.
	})

	t.Run("listener with nil default filter chain", func(_ *testing.T) {
		listener := &listenerv3.Listener{
			Name: "no-default-chain-listener",
			// No DefaultFilterChain set.
		}

		err := s.maybeModifyListenerAndRoutes([]*listenerv3.Listener{listener}, []*routev3.RouteConfiguration{})
		require.NoError(t, err)
		// Should handle gracefully when no default filter chain exists.
	})

	t.Run("route configuration with InferencePool routes", func(_ *testing.T) {
		listeners := []*listenerv3.Listener{
			createListener("test-listener", "test-route-config"),
		}

		routes := []*routev3.RouteConfiguration{
			{
				Name: "test-route-config",
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name: "test-vh",
						Routes: []*routev3.Route{
							createRouteWithInferencePool("inference-route"),
							{Name: "normal-route"}, // Route without InferencePool metadata.
						},
					},
				},
			},
		}

		err := s.maybeModifyListenerAndRoutes(listeners, routes)
		require.NoError(t, err)
		// Should identify and process InferencePool routes.
	})

	t.Run("multiple listeners with different route configs", func(_ *testing.T) {
		listeners := []*listenerv3.Listener{
			createListener("listener1", "route-config1"),
			createListener("listener2", "route-config2"),
		}

		routes := []*routev3.RouteConfiguration{
			{
				Name: "route-config1",
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name: "vh1",
						Routes: []*routev3.Route{
							createRouteWithInferencePool("route1"),
						},
					},
				},
			},
			{
				Name: "route-config2",
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name: "vh2",
						Routes: []*routev3.Route{
							{Name: "normal-route2"},
						},
					},
				},
			},
		}

		err := s.maybeModifyListenerAndRoutes(listeners, routes)
		require.NoError(t, err)

		// Should handle multiple listeners with different route configurations.
	})

	t.Run("listener with missing route config", func(_ *testing.T) {
		listeners := []*listenerv3.Listener{
			createListener("test-listener", "missing-route-config"),
		}

		routes := []*routev3.RouteConfiguration{
			{
				Name: "different-route-config",
				VirtualHosts: []*routev3.VirtualHost{
					{
						Name: "test-vh",
						Routes: []*routev3.Route{
							createRouteWithInferencePool("test-route"),
						},
					},
				},
			},
		}

		err := s.maybeModifyListenerAndRoutes(listeners, routes)
		require.NoError(t, err)
		// Should handle gracefully when referenced route config is not found.
	})
}

// TestPatchListenerWithInferencePoolFilters tests the patchListenerWithInferencePoolFilters function.
func TestPatchListenerWithInferencePoolFilters(t *testing.T) {
	s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	// Helper function to create an InferencePool.
	createInferencePool := func(name, namespace string) *gwaiev1.InferencePool {
		return &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: gwaiev1.InferencePoolSpec{
				TargetPorts: []gwaiev1.Port{{Number: 8080}},
				EndpointPickerRef: &gwaiev1.EndpointPickerRef{
					Name: "test-epp",
				},
			},
		}
	}

	// Helper function to create a listener with HCM.
	createListenerWithHCM := func(name string, httpFilters []*httpconnectionmanagerv3.HttpFilter) *listenerv3.Listener {
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: httpFilters,
		}

		return &listenerv3.Listener{
			Name: name,
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}
	}

	t.Run("listener with no filter chains", func(_ *testing.T) {
		listener := &listenerv3.Listener{
			Name: "test-listener",
		}
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		s.patchListenerWithInferencePoolFilters(listener, pools)
		// Should handle gracefully when no filter chains exist.
	})

	t.Run("listener with filter chains but no HCM", func(t *testing.T) {
		var buf bytes.Buffer
		server, err := New(newFakeClient(), logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{})), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		listener := &listenerv3.Listener{
			Name: "test-listener",
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name: "some-other-filter",
					},
				},
			},
		}
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		server.patchListenerWithInferencePoolFilters(listener, pools)
		require.Contains(t, buf.String(), "failed to find an HCM in the current chain")
	})

	t.Run("listener with existing inference pool filter", func(t *testing.T) {
		existingFilters := []*httpconnectionmanagerv3.HttpFilter{
			{Name: httpFilterNameForInferencePool(createInferencePool("test-pool", "test-ns"))},
			{Name: "envoy.filters.http.router"},
		}

		listener := createListenerWithHCM("test-listener", existingFilters)
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		s.patchListenerWithInferencePoolFilters(listener, pools)

		// Verify no additional filters were added since the filter already exists.
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{}
		err := listener.DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
		require.NoError(t, err)
		require.Len(t, hcm.HttpFilters, 2) // Should still have the same number of filters.
	})

	t.Run("listener with new inference pool filter", func(t *testing.T) {
		existingFilters := []*httpconnectionmanagerv3.HttpFilter{
			{Name: "envoy.filters.http.router"},
		}

		listener := createListenerWithHCM("test-listener", existingFilters)
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		s.patchListenerWithInferencePoolFilters(listener, pools)

		// Verify the new filter was added.
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{}
		err := listener.DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
		require.NoError(t, err)
		require.Len(t, hcm.HttpFilters, 2) // Should have inference pool filter + router.
		require.Equal(t, httpFilterNameForInferencePool(pools[0]), hcm.HttpFilters[0].Name)
		require.Equal(t, "envoy.filters.http.router", hcm.HttpFilters[1].Name)
	})

	t.Run("listener with multiple inference pools", func(t *testing.T) {
		existingFilters := []*httpconnectionmanagerv3.HttpFilter{
			{Name: "envoy.filters.http.router"},
		}

		listener := createListenerWithHCM("test-listener", existingFilters)
		pools := []*gwaiev1.InferencePool{
			createInferencePool("pool1", "test-ns"),
			createInferencePool("pool2", "test-ns"),
		}

		s.patchListenerWithInferencePoolFilters(listener, pools)

		// Verify both filters were added.
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{}
		err := listener.DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(hcm)
		require.NoError(t, err)
		require.Len(t, hcm.HttpFilters, 3) // Should have 2 inference pool filters + router.
		require.Equal(t, httpFilterNameForInferencePool(pools[0]), hcm.HttpFilters[0].Name)
		require.Equal(t, httpFilterNameForInferencePool(pools[1]), hcm.HttpFilters[1].Name)
		require.Equal(t, "envoy.filters.http.router", hcm.HttpFilters[2].Name)
	})

	t.Run("listener with both filter chains and default filter chain", func(t *testing.T) {
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.router"},
			},
		}

		listener := &listenerv3.Listener{
			Name: "test-listener",
			FilterChains: []*listenerv3.FilterChain{
				{
					Filters: []*listenerv3.Filter{
						{
							Name:       wellknown.HTTPConnectionManager,
							ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
						},
					},
				},
			},
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}

		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		s.patchListenerWithInferencePoolFilters(listener, pools)

		// Verify both filter chains were processed.
		// Check the first filter chain.
		hcm1 := &httpconnectionmanagerv3.HttpConnectionManager{}
		err := listener.FilterChains[0].Filters[0].GetTypedConfig().UnmarshalTo(hcm1)
		require.NoError(t, err)
		require.Len(t, hcm1.HttpFilters, 2)

		// Check the default filter chain.
		hcm2 := &httpconnectionmanagerv3.HttpConnectionManager{}
		err = listener.DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(hcm2)
		require.NoError(t, err)
		require.Len(t, hcm2.HttpFilters, 2)
	})

	t.Run("error marshaling updated HCM", func(_ *testing.T) {
		var buf bytes.Buffer
		server, err := New(newFakeClient(), logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{})), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		// Create a listener with an HCM that will cause marshaling issues.
		// This is a bit tricky to test, but we can create a scenario where the HCM is modified
		// in a way that might cause issues.
		listener := createListenerWithHCM("test-listener", []*httpconnectionmanagerv3.HttpFilter{
			{Name: "envoy.filters.http.router"},
		})
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		server.patchListenerWithInferencePoolFilters(listener, pools)
		// This test mainly ensures the error handling path is covered.
		// In normal cases, marshaling should succeed.
	})
}

// TestPatchVirtualHostWithInferencePool tests the patchVirtualHostWithInferencePool function.
func TestPatchVirtualHostWithInferencePool(t *testing.T) {
	s, err := New(newFakeClient(), logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	// Helper function to create an InferencePool.
	createInferencePool := func(name, namespace string) *gwaiev1.InferencePool {
		return &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: gwaiev1.InferencePoolSpec{
				TargetPorts: []gwaiev1.Port{{Number: 8080}},
				EndpointPickerRef: &gwaiev1.EndpointPickerRef{
					Name: "test-epp",
				},
			},
		}
	}

	// Helper function to create a route with InferencePool metadata.
	createRouteWithInferencePool := func(routeName string, pool *gwaiev1.InferencePool) *routev3.Route {
		metadata := &corev3.Metadata{
			FilterMetadata: map[string]*structpb.Struct{
				internalapi.InternalEndpointMetadataNamespace: {
					Fields: map[string]*structpb.Value{
						"per_route_rule_inference_pool": structpb.NewStringValue(
							fmt.Sprintf("%s/%s/test-epp/9002/duplex/false", pool.Namespace, pool.Name),
						),
					},
				},
			},
		}

		return &routev3.Route{
			Name:     routeName,
			Metadata: metadata,
		}
	}

	t.Run("virtual host with no routes", func(_ *testing.T) {
		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{},
		}
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)
		// Should handle gracefully when no routes exist.
	})

	t.Run("route without InferencePool metadata", func(t *testing.T) {
		normalRoute := &routev3.Route{
			Name: "normal-route",
		}
		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{normalRoute},
		}
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)

		// Verify the route was configured to disable all inference pool filters.
		require.NotNil(t, normalRoute.TypedPerFilterConfig)
		filterName := httpFilterNameForInferencePool(pools[0])
		require.Contains(t, normalRoute.TypedPerFilterConfig, filterName)
	})

	t.Run("route with matching InferencePool metadata", func(t *testing.T) {
		pool := createInferencePool("test-pool", "test-ns")
		inferenceRoute := createRouteWithInferencePool("inference-route", pool)

		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{inferenceRoute},
		}
		pools := []*gwaiev1.InferencePool{pool}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)

		// Verify the route was not configured to disable its own filter.
		// It should not have any TypedPerFilterConfig for its own filter.
		if inferenceRoute.TypedPerFilterConfig != nil {
			filterName := httpFilterNameForInferencePool(pool)
			require.NotContains(t, inferenceRoute.TypedPerFilterConfig, filterName)
		}
	})

	t.Run("route with different InferencePool metadata", func(t *testing.T) {
		pool1 := createInferencePool("pool1", "test-ns")
		pool2 := createInferencePool("pool2", "test-ns")

		// Route uses pool1, but we have both pool1 and pool2 in the system.
		inferenceRoute := createRouteWithInferencePool("inference-route", pool1)

		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{inferenceRoute},
		}
		pools := []*gwaiev1.InferencePool{pool1, pool2}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)

		// Verify the route disables pool2's filter but not pool1's filter.
		require.NotNil(t, inferenceRoute.TypedPerFilterConfig)

		pool1FilterName := httpFilterNameForInferencePool(pool1)
		pool2FilterName := httpFilterNameForInferencePool(pool2)

		require.NotContains(t, inferenceRoute.TypedPerFilterConfig, pool1FilterName)
		require.Contains(t, inferenceRoute.TypedPerFilterConfig, pool2FilterName)
	})

	t.Run("route with direct response containing 'No matching route found'", func(t *testing.T) {
		directResponseRoute := &routev3.Route{
			Name: "direct-response-route",
			Action: &routev3.Route_DirectResponse{
				DirectResponse: &routev3.DirectResponseAction{
					Body: &corev3.DataSource{
						Specifier: &corev3.DataSource_InlineString{
							InlineString: "No matching route found",
						},
					},
				},
			},
		}

		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{directResponseRoute},
		}
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)

		// Verify the direct response route was not skipped (And TypedPerFilterConfig added).
		require.NotNil(t, directResponseRoute.TypedPerFilterConfig)
	})

	t.Run("route with direct response not containing 'No matching route found'", func(t *testing.T) {
		directResponseRoute := &routev3.Route{
			Name: "direct-response-route",
			Action: &routev3.Route_DirectResponse{
				DirectResponse: &routev3.DirectResponseAction{
					Body: &corev3.DataSource{
						Specifier: &corev3.DataSource_InlineString{
							InlineString: "Some other response",
						},
					},
				},
			},
		}

		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{directResponseRoute},
		}
		pools := []*gwaiev1.InferencePool{createInferencePool("test-pool", "test-ns")}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)

		// Verify the direct response route was processed (TypedPerFilterConfig added).
		require.NotNil(t, directResponseRoute.TypedPerFilterConfig)
		filterName := httpFilterNameForInferencePool(pools[0])
		require.Contains(t, directResponseRoute.TypedPerFilterConfig, filterName)
	})

	t.Run("multiple routes with mixed scenarios", func(t *testing.T) {
		pool1 := createInferencePool("pool1", "test-ns")
		pool2 := createInferencePool("pool2", "test-ns")

		normalRoute := &routev3.Route{Name: "normal-route"}
		inferenceRoute1 := createRouteWithInferencePool("inference-route1", pool1)
		inferenceRoute2 := createRouteWithInferencePool("inference-route2", pool2)

		vh := &routev3.VirtualHost{
			Name:   "test-vh",
			Routes: []*routev3.Route{normalRoute, inferenceRoute1, inferenceRoute2},
		}
		pools := []*gwaiev1.InferencePool{pool1, pool2}

		err := s.patchVirtualHostWithInferencePool(vh, pools)
		require.NoError(t, err)

		// Verify normal route disables both filters.
		require.NotNil(t, normalRoute.TypedPerFilterConfig)
		require.Len(t, normalRoute.TypedPerFilterConfig, 2)

		// Verify inference route 1 disables only pool2's filter.
		require.NotNil(t, inferenceRoute1.TypedPerFilterConfig)
		require.Len(t, inferenceRoute1.TypedPerFilterConfig, 1)
		require.Contains(t, inferenceRoute1.TypedPerFilterConfig, httpFilterNameForInferencePool(pool2))

		// Verify inference route 2 disables only pool1's filter.
		require.NotNil(t, inferenceRoute2.TypedPerFilterConfig)
		require.Len(t, inferenceRoute2.TypedPerFilterConfig, 1)
		require.Contains(t, inferenceRoute2.TypedPerFilterConfig, httpFilterNameForInferencePool(pool1))
	})
}

// TestPostClusterModify tests the PostClusterModify method.
func TestPostClusterModify(t *testing.T) {
	logger := logr.Discard()
	s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	t.Run("nil cluster", func(t *testing.T) {
		req := &egextension.PostClusterModifyRequest{Cluster: nil}
		resp, err := s.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.Nil(t, resp)
	})

	t.Run("no backend extension resources", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "test-cluster"}
		req := &egextension.PostClusterModifyRequest{
			Cluster: cluster,
			PostClusterContext: &egextension.PostClusterExtensionContext{
				BackendExtensionResources: []*egextension.ExtensionResource{},
			},
		}
		resp, err := s.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, cluster, resp.Cluster)
	})

	t.Run("nil PostClusterContext", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "test-cluster"}
		req := &egextension.PostClusterModifyRequest{
			Cluster:            cluster,
			PostClusterContext: nil,
		}
		resp, err := s.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, cluster, resp.Cluster)
	})

	t.Run("with InferencePool backend", func(t *testing.T) {
		// Use a logger that captures output for debugging.
		var buf bytes.Buffer
		logger := logr.FromSlogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
		anotherServer, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		cluster := &clusterv3.Cluster{
			Name:     "test-cluster",
			LbPolicy: clusterv3.Cluster_ROUND_ROBIN,
		}
		inferencePool := createInferencePoolExtensionResource("test-pool", "default")

		// Debug: print the JSON to see what we're sending.
		t.Logf("InferencePool JSON: %s", string(inferencePool.UnstructuredBytes))

		req := &egextension.PostClusterModifyRequest{
			Cluster: cluster,
			PostClusterContext: &egextension.PostClusterExtensionContext{
				BackendExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := anotherServer.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, cluster, resp.Cluster)

		// Print logs for debugging.
		t.Logf("Server logs: %s", buf.String())

		// Verify cluster was modified for ORIGINAL_DST.
		if cluster.ClusterDiscoveryType == nil {
			t.Logf("ClusterDiscoveryType is nil - cluster was not modified")
			t.Logf("Cluster LbPolicy: %v", cluster.LbPolicy)
			t.FailNow()
		}
		require.NotNil(t, cluster.ClusterDiscoveryType)
		require.Equal(t, clusterv3.Cluster_ORIGINAL_DST, cluster.ClusterDiscoveryType.(*clusterv3.Cluster_Type).Type)
		require.Equal(t, clusterv3.Cluster_CLUSTER_PROVIDED, cluster.LbPolicy)
		require.Equal(t, durationpb.New(10*time.Second), cluster.ConnectTimeout)
		require.NotNil(t, cluster.LbConfig)
		require.Nil(t, cluster.LoadBalancingPolicy)
		require.Nil(t, cluster.EdsClusterConfig)
		require.NotNil(t, getInferencePoolByMetadata(cluster.Metadata))

		// Default (unset) appProtocol must result in explicit HTTP/1.1 upstream protocol options.
		poAny, ok := cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"]
		require.True(t, ok)
		po := &httpv3.HttpProtocolOptions{}
		require.NoError(t, poAny.UnmarshalTo(po))
		explicitConfig, ok := po.UpstreamProtocolOptions.(*httpv3.HttpProtocolOptions_ExplicitHttpConfig_)
		require.True(t, ok)
		require.IsType(t, &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{}, explicitConfig.ExplicitHttpConfig.ProtocolConfig)
	})

	t.Run("with InferencePool backend and appProtocol kubernetes.io/h2c", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "test-cluster"}
		inferencePool := createInferencePoolExtensionResourceWithAppProtocol("test-pool", "default", "kubernetes.io/h2c")

		req := &egextension.PostClusterModifyRequest{
			Cluster: cluster,
			PostClusterContext: &egextension.PostClusterExtensionContext{
				BackendExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := s.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)

		poAny, ok := cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"]
		require.True(t, ok)
		po := &httpv3.HttpProtocolOptions{}
		require.NoError(t, poAny.UnmarshalTo(po))
		explicitConfig, ok := po.UpstreamProtocolOptions.(*httpv3.HttpProtocolOptions_ExplicitHttpConfig_)
		require.True(t, ok)
		require.IsType(t, &httpv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{}, explicitConfig.ExplicitHttpConfig.ProtocolConfig)
	})

	t.Run("with InferencePool backend and appProtocol http", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "test-cluster"}
		inferencePool := createInferencePoolExtensionResourceWithAppProtocol("test-pool", "default", "http")

		req := &egextension.PostClusterModifyRequest{
			Cluster: cluster,
			PostClusterContext: &egextension.PostClusterExtensionContext{
				BackendExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := s.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)

		poAny, ok := cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"]
		require.True(t, ok)
		po := &httpv3.HttpProtocolOptions{}
		require.NoError(t, poAny.UnmarshalTo(po))
		explicitConfig, ok := po.UpstreamProtocolOptions.(*httpv3.HttpProtocolOptions_ExplicitHttpConfig_)
		require.True(t, ok)
		require.IsType(t, &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{}, explicitConfig.ExplicitHttpConfig.ProtocolConfig)
	})

	t.Run("with InferencePool backend and no endpointPickerRef", func(t *testing.T) {
		// Regression test: an InferencePool with endpointPickerRef unset must not panic,
		// and the cluster should be left as Envoy Gateway generated it since we don't yet
		// support routing traffic without an endpoint picker.
		cluster := &clusterv3.Cluster{
			Name:     "test-cluster",
			LbPolicy: clusterv3.Cluster_ROUND_ROBIN,
		}
		inferencePool := createInferencePoolExtensionResourceNoEPPRef("test-pool-no-epp-ref", "default")

		req := &egextension.PostClusterModifyRequest{
			Cluster: cluster,
			PostClusterContext: &egextension.PostClusterExtensionContext{
				BackendExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := s.PostClusterModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, cluster, resp.Cluster)

		// The cluster must be left unmodified: no ORIGINAL_DST rewrite, no EPP metadata.
		require.Nil(t, cluster.ClusterDiscoveryType)
		require.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
		require.Nil(t, cluster.Metadata)
	})
}

// TestPostRouteModify tests the PostRouteModify method.
func TestPostRouteModify(t *testing.T) {
	logger := logr.Discard()
	s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	t.Run("nil route", func(t *testing.T) {
		req := &egextension.PostRouteModifyRequest{Route: nil}
		resp, err := s.PostRouteModify(context.Background(), req)
		require.NoError(t, err)
		require.Nil(t, resp)
	})

	t.Run("no extension resources", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		req := &egextension.PostRouteModifyRequest{
			Route: route,
			PostRouteContext: &egextension.PostRouteExtensionContext{
				ExtensionResources: []*egextension.ExtensionResource{},
			},
		}
		resp, err := s.PostRouteModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, route, resp.Route)
	})

	t.Run("nil PostRouteContext", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		req := &egextension.PostRouteModifyRequest{
			Route:            route,
			PostRouteContext: nil,
		}
		resp, err := s.PostRouteModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, route, resp.Route)
	})

	t.Run("with InferencePool extension", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test-route",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{},
			},
		}
		inferencePool := createInferencePoolExtensionResource("test-pool", "default")
		req := &egextension.PostRouteModifyRequest{
			Route: route,
			PostRouteContext: &egextension.PostRouteExtensionContext{
				ExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := s.PostRouteModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, route, resp.Route)

		// Verify route was modified.
		require.Equal(t, wrapperspb.Bool(false), route.GetRoute().GetAutoHostRewrite())
		require.NotNil(t, route.TypedPerFilterConfig)
		require.NotNil(t, getInferencePoolByMetadata(route.Metadata))
	})

	t.Run("with InferencePool extension and DirectResponse route action", func(t *testing.T) {
		// When a route has a DirectResponse action (not Route_Route), GetRoute() returns nil.
		// Reject it so Envoy Gateway retains the last good configuration instead of
		// publishing an unresolved InferencePool as a direct response.
		route := &routev3.Route{
			Name: "test-route-direct-response",
			Action: &routev3.Route_DirectResponse{
				DirectResponse: &routev3.DirectResponseAction{
					Status: 403,
				},
			},
		}
		inferencePool := createInferencePoolExtensionResource("test-pool", "default")
		req := &egextension.PostRouteModifyRequest{
			Route: route,
			PostRouteContext: &egextension.PostRouteExtensionContext{
				ExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := s.PostRouteModify(context.Background(), req)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.ErrorContains(t, err, "cannot configure InferencePool default/test-pool")
		require.Nil(t, resp)
	})

	t.Run("with InferencePool extension and no endpointPickerRef", func(t *testing.T) {
		// Regression test: an InferencePool with endpointPickerRef unset must not panic,
		// and the route should be left as Envoy Gateway generated it since we don't yet
		// support routing traffic without an endpoint picker.
		route := &routev3.Route{
			Name: "test-route",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{},
			},
		}
		inferencePool := createInferencePoolExtensionResourceNoEPPRef("test-pool-no-epp-ref", "default")
		req := &egextension.PostRouteModifyRequest{
			Route: route,
			PostRouteContext: &egextension.PostRouteExtensionContext{
				ExtensionResources: []*egextension.ExtensionResource{inferencePool},
			},
		}
		resp, err := s.PostRouteModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, route, resp.Route)

		// Verify the route was left unmodified.
		require.Nil(t, route.GetRoute().GetAutoHostRewrite())
		require.Nil(t, route.TypedPerFilterConfig)
		require.Nil(t, route.Metadata)
	})
}

// TestMaybeSetStreamIdleTimeout tests that the route's per_try_idle_timeout is set
// from the AIGatewayRoute rule's StreamIdleTimeout.
func TestMaybeSetStreamIdleTimeout(t *testing.T) {
	c := newFakeClient()
	err := c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "ttft-route", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{StreamIdleTimeout: ptr.To(gwapiv1.Duration("10s"))},
				{}, // No StreamIdleTimeout.
			},
		},
	})
	require.NoError(t, err)
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	forwardingRoute := func(name string) *routev3.Route {
		return &routev3.Route{Name: name, Action: &routev3.Route_Route{Route: &routev3.RouteAction{}}}
	}
	call := func(t *testing.T, route *routev3.Route) {
		cache := make(map[client.ObjectKey]*aigv1b1.AIGatewayRoute)
		require.NoError(t, s.maybeSetStreamIdleTimeout(context.Background(), route, cache))
	}

	t.Run("sets per_try_idle_timeout when configured", func(t *testing.T) {
		route := forwardingRoute("httproute/default/ttft-route/rule/0/match/0")
		call(t, route)
		require.Equal(t, durationpb.New(10*time.Second), route.GetRoute().RetryPolicy.GetPerTryIdleTimeout())
	})

	t.Run("preserves existing retry policy", func(t *testing.T) {
		route := forwardingRoute("httproute/default/ttft-route/rule/0/match/0")
		route.GetRoute().RetryPolicy = &routev3.RetryPolicy{RetryOn: "reset", NumRetries: wrapperspb.UInt32(2)}
		call(t, route)
		require.Equal(t, "reset", route.GetRoute().RetryPolicy.RetryOn)
		require.Equal(t, uint32(2), route.GetRoute().RetryPolicy.NumRetries.GetValue())
		require.Equal(t, durationpb.New(10*time.Second), route.GetRoute().RetryPolicy.GetPerTryIdleTimeout())
	})

	t.Run("no timeout when rule has none", func(t *testing.T) {
		route := forwardingRoute("httproute/default/ttft-route/rule/1/match/0")
		call(t, route)
		require.Nil(t, route.GetRoute().RetryPolicy)
	})

	t.Run("ignores non-forwarding route", func(t *testing.T) {
		route := &routev3.Route{
			Name:   "httproute/default/ttft-route/rule/0/match/0",
			Action: &routev3.Route_DirectResponse{DirectResponse: &routev3.DirectResponseAction{Status: 403}},
		}
		call(t, route)
		require.Nil(t, route.GetRoute())
	})

	t.Run("ignores unrelated route name", func(t *testing.T) {
		route := forwardingRoute("some-other-route")
		call(t, route)
		require.Nil(t, route.GetRoute().RetryPolicy)
	})

	t.Run("ignores out-of-range rule index", func(t *testing.T) {
		route := forwardingRoute("httproute/default/ttft-route/rule/9/match/0")
		call(t, route)
		require.Nil(t, route.GetRoute().RetryPolicy)
	})

	t.Run("ignores non-numeric rule index", func(t *testing.T) {
		route := forwardingRoute("httproute/default/ttft-route/rule/x/match/0")
		call(t, route)
		require.Nil(t, route.GetRoute().RetryPolicy)
	})

	t.Run("ignores missing AIGatewayRoute", func(t *testing.T) {
		route := forwardingRoute("httproute/default/missing/rule/0/match/0")
		call(t, route)
		require.Nil(t, route.GetRoute().RetryPolicy)
	})

	t.Run("reuses cached lookups across routes", func(t *testing.T) {
		cache := make(map[client.ObjectKey]*aigv1b1.AIGatewayRoute)
		// Two routes for the same AIGatewayRoute, plus a repeated missing one, all sharing
		// the cache so the second lookup of each key is served from memory.
		for _, name := range []string{
			"httproute/default/ttft-route/rule/0/match/0",
			"httproute/default/ttft-route/rule/0/match/1",
			"httproute/default/missing/rule/0/match/0",
			"httproute/default/missing/rule/0/match/1",
		} {
			route := forwardingRoute(name)
			require.NoError(t, s.maybeSetStreamIdleTimeout(context.Background(), route, cache))
		}
		require.Contains(t, cache, client.ObjectKey{Namespace: "default", Name: "ttft-route"})
		require.Nil(t, cache[client.ObjectKey{Namespace: "default", Name: "missing"}])
	})
}

// TestApplyStreamIdleTimeouts tests that the per-try idle timeout is applied while walking
// the route configurations, and that unrelated routes are left untouched.
func TestApplyStreamIdleTimeouts(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "ttft-route", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{{StreamIdleTimeout: ptr.To(gwapiv1.Duration("7s"))}},
		},
	}))
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	forwarding := func(name string) *routev3.Route {
		return &routev3.Route{Name: name, Action: &routev3.Route_Route{Route: &routev3.RouteAction{}}}
	}
	configured := forwarding("httproute/default/ttft-route/rule/0/match/0")
	other := forwarding("some-other-route")
	routeConfigs := []*routev3.RouteConfiguration{{
		VirtualHosts: []*routev3.VirtualHost{{Routes: []*routev3.Route{configured, other}}},
	}}

	require.NoError(t, s.applyStreamIdleTimeouts(context.Background(), routeConfigs))
	require.Equal(t, durationpb.New(7*time.Second), configured.GetRoute().RetryPolicy.GetPerTryIdleTimeout())
	require.Nil(t, other.GetRoute().RetryPolicy)

	// A failed AIGatewayRoute lookup propagates out of the walk.
	failing, err := New(
		fake.NewClientBuilder().WithScheme(controller.Scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errors.New("boom")
				},
			}).Build(),
		logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	err = failing.applyStreamIdleTimeouts(context.Background(),
		[]*routev3.RouteConfiguration{{VirtualHosts: []*routev3.VirtualHost{{Routes: []*routev3.Route{
			forwarding("httproute/default/ttft-route/rule/0/match/0"),
		}}}}})
	require.ErrorContains(t, err, "boom")
}

// TestConstructInferencePoolsFrom tests the constructInferencePoolsFrom method.
func TestConstructInferencePoolsFrom(t *testing.T) {
	logger := logr.Discard()
	s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	t.Run("empty resources", func(t *testing.T) {
		result := s.constructInferencePoolsFrom([]*egextension.ExtensionResource{})
		require.Empty(t, result)
	})

	t.Run("valid InferencePool", func(t *testing.T) {
		inferencePool := createInferencePoolExtensionResource("test-pool", "default")
		result := s.constructInferencePoolsFrom([]*egextension.ExtensionResource{inferencePool})
		require.Len(t, result, 1)
		require.Equal(t, "test-pool", result[0].Name)
		require.Equal(t, "default", result[0].Namespace)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		invalidResource := &egextension.ExtensionResource{
			UnstructuredBytes: []byte("invalid json"),
		}
		result := s.constructInferencePoolsFrom([]*egextension.ExtensionResource{invalidResource})
		require.Empty(t, result)
	})

	t.Run("wrong API version", func(t *testing.T) {
		unstructuredObj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata": map[string]any{
					"name":      "test-service",
					"namespace": "default",
				},
			},
		}
		jsonBytes, _ := unstructuredObj.MarshalJSON()
		wrongResource := &egextension.ExtensionResource{
			UnstructuredBytes: jsonBytes,
		}
		result := s.constructInferencePoolsFrom([]*egextension.ExtensionResource{wrongResource})
		require.Empty(t, result)
	})
}

// TestInferencePoolHelperFunctions tests various helper functions for InferencePool.
func TestInferencePoolHelperFunctions(t *testing.T) {
	// Create a test InferencePool.
	pool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pool",
			Namespace: "test-ns",
		},
		Spec: gwaiev1.InferencePoolSpec{
			TargetPorts: []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: "test-epp",
			},
		},
	}

	t.Run("authorityForInferencePool", func(t *testing.T) {
		authority := authorityForInferencePool(pool)
		require.Equal(t, "test-epp.test-ns.svc:9002", authority)
	})

	t.Run("dnsNameForInferencePool", func(t *testing.T) {
		dnsName := dnsNameForInferencePool(pool)
		require.Equal(t, "test-epp.test-ns.svc", dnsName)
	})

	t.Run("clusterNameForInferencePool", func(t *testing.T) {
		clusterName := clusterNameForInferencePool(pool)
		require.Equal(t, "envoy.clusters.endpointpicker_test-pool_test-ns_ext_proc", clusterName)
	})

	t.Run("httpFilterNameForInferencePool", func(t *testing.T) {
		filterName := httpFilterNameForInferencePool(pool)
		require.Equal(t, "envoy.filters.http.ext_proc/endpointpicker/test-pool_test-ns_ext_proc", filterName)
	})

	t.Run("portForInferencePool default", func(t *testing.T) {
		port := portForInferencePool(pool)
		require.Equal(t, uint32(9002), port) // default port.
	})

	t.Run("portForInferencePool custom", func(t *testing.T) {
		customPool := pool.DeepCopy()
		customPort := gwaiev1.PortNumber(8888)
		customPool.Spec.EndpointPickerRef.Port = &gwaiev1.Port{Number: customPort}
		port := portForInferencePool(customPool)
		require.Equal(t, uint32(8888), port)
	})
}

// TestInferencePoolAnnotationHelpers tests the annotation helper functions.
func TestInferencePoolAnnotationHelpers(t *testing.T) {
	t.Run("getProcessingBodyModeFromAnnotations", func(t *testing.T) {
		t.Run("no annotations", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
				},
			}
			mode := getProcessingBodyModeFromAnnotations(pool)
			require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, mode)
		})

		t.Run("annotation set to duplex", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/processing-body-mode": "duplex",
					},
				},
			}
			mode := getProcessingBodyModeFromAnnotations(pool)
			require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, mode)
		})

		t.Run("annotation set to buffered", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/processing-body-mode": "buffered",
					},
				},
			}
			mode := getProcessingBodyModeFromAnnotations(pool)
			require.Equal(t, extprocv3.ProcessingMode_BUFFERED, mode)
		})

		t.Run("annotation set to invalid value", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/processing-body-mode": "invalid",
					},
				},
			}
			mode := getProcessingBodyModeFromAnnotations(pool)
			require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, mode)
		})
	})

	t.Run("getAllowModeOverrideFromAnnotations", func(t *testing.T) {
		t.Run("no annotations", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
				},
			}
			override := getAllowModeOverrideFromAnnotations(pool)
			require.False(t, override)
		})

		t.Run("annotation set to true", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/allow-mode-override": "true",
					},
				},
			}
			override := getAllowModeOverrideFromAnnotations(pool)
			require.True(t, override)
		})

		t.Run("annotation set to false", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/allow-mode-override": "false",
					},
				},
			}
			override := getAllowModeOverrideFromAnnotations(pool)
			require.False(t, override)
		})

		t.Run("annotation set to invalid value", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/allow-mode-override": "invalid",
					},
				},
			}
			override := getAllowModeOverrideFromAnnotations(pool)
			require.False(t, override)
		})
	})

	t.Run("getProcessingBodyModeStringFromAnnotations", func(t *testing.T) {
		t.Run("no annotations", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
				},
			}
			mode := getProcessingBodyModeStringFromAnnotations(pool)
			require.Equal(t, "duplex", mode)
		})

		t.Run("annotation set to duplex", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/processing-body-mode": "duplex",
					},
				},
			}
			mode := getProcessingBodyModeStringFromAnnotations(pool)
			require.Equal(t, "duplex", mode)
		})

		t.Run("annotation set to buffered", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/processing-body-mode": "buffered",
					},
				},
			}
			mode := getProcessingBodyModeStringFromAnnotations(pool)
			require.Equal(t, "buffered", mode)
		})

		t.Run("annotation set to invalid value", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/processing-body-mode": "invalid",
					},
				},
			}
			mode := getProcessingBodyModeStringFromAnnotations(pool)
			require.Equal(t, "invalid", mode) // Returns the raw value
		})
	})

	t.Run("getAllowModeOverrideStringFromAnnotations", func(t *testing.T) {
		t.Run("no annotations", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
				},
			}
			override := getAllowModeOverrideStringFromAnnotations(pool)
			require.Equal(t, "false", override)
		})

		t.Run("annotation set to true", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/allow-mode-override": "true",
					},
				},
			}
			override := getAllowModeOverrideStringFromAnnotations(pool)
			require.Equal(t, "true", override)
		})

		t.Run("annotation set to false", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/allow-mode-override": "false",
					},
				},
			}
			override := getAllowModeOverrideStringFromAnnotations(pool)
			require.Equal(t, "false", override)
		})

		t.Run("annotation set to invalid value", func(t *testing.T) {
			pool := &gwaiev1.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pool",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"aigateway.envoyproxy.io/allow-mode-override": "invalid",
					},
				},
			}
			override := getAllowModeOverrideStringFromAnnotations(pool)
			require.Equal(t, "invalid", override) // Returns the raw value
		})
	})
}

// TestBuildHTTPFilterForInferencePool tests the buildHTTPFilterForInferencePool function with annotations.
func TestBuildHTTPFilterForInferencePool(t *testing.T) {
	t.Run("default configuration", func(t *testing.T) {
		pool := &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "test-ns",
			},
			Spec: gwaiev1.InferencePoolSpec{
				EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "test-epp"},
			},
		}

		filter := buildHTTPFilterForInferencePool(pool)
		require.NotNil(t, filter)
		require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, filter.ProcessingMode.RequestBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, filter.ProcessingMode.ResponseBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.RequestTrailerMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.ResponseTrailerMode)
		require.False(t, filter.AllowModeOverride)
	})

	t.Run("with buffered mode annotation", func(t *testing.T) {
		pool := &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "test-ns",
				Annotations: map[string]string{
					"aigateway.envoyproxy.io/processing-body-mode": "buffered",
				},
			},
			Spec: gwaiev1.InferencePoolSpec{
				EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "test-epp"},
			},
		}

		filter := buildHTTPFilterForInferencePool(pool)
		require.NotNil(t, filter)
		require.Equal(t, extprocv3.ProcessingMode_BUFFERED, filter.ProcessingMode.RequestBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_BUFFERED, filter.ProcessingMode.ResponseBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.RequestTrailerMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.ResponseTrailerMode)
		require.False(t, filter.AllowModeOverride)
	})

	t.Run("with allow mode override annotation", func(t *testing.T) {
		pool := &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "test-ns",
				Annotations: map[string]string{
					"aigateway.envoyproxy.io/allow-mode-override": "true",
				},
			},
			Spec: gwaiev1.InferencePoolSpec{
				EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "test-epp"},
			},
		}

		filter := buildHTTPFilterForInferencePool(pool)
		require.NotNil(t, filter)
		require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, filter.ProcessingMode.RequestBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED, filter.ProcessingMode.ResponseBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.RequestTrailerMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.ResponseTrailerMode)
		require.True(t, filter.AllowModeOverride)
	})

	t.Run("with both annotations", func(t *testing.T) {
		pool := &gwaiev1.InferencePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pool",
				Namespace: "test-ns",
				Annotations: map[string]string{
					"aigateway.envoyproxy.io/processing-body-mode": "buffered",
					"aigateway.envoyproxy.io/allow-mode-override":  "true",
				},
			},
			Spec: gwaiev1.InferencePoolSpec{
				EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "test-epp"},
			},
		}

		filter := buildHTTPFilterForInferencePool(pool)
		require.NotNil(t, filter)
		require.Equal(t, extprocv3.ProcessingMode_BUFFERED, filter.ProcessingMode.RequestBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_BUFFERED, filter.ProcessingMode.ResponseBodyMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.RequestTrailerMode)
		require.Equal(t, extprocv3.ProcessingMode_SEND, filter.ProcessingMode.ResponseTrailerMode)
		require.True(t, filter.AllowModeOverride)
	})
}

// TestBuildExtProcClusterForInferencePoolEndpointPicker tests cluster building.
func TestBuildExtProcClusterForInferencePoolEndpointPicker(t *testing.T) {
	pool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pool",
			Namespace: "test-ns",
		},
		Spec: gwaiev1.InferencePoolSpec{
			TargetPorts:       []gwaiev1.Port{{Number: 8080}},
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{Name: "test-epp"},
		},
	}

	t.Run("valid pool", func(t *testing.T) {
		cluster, err := buildExtProcClusterForInferencePoolEndpointPicker(pool)
		require.NoError(t, err)
		require.NotNil(t, cluster)
		require.Equal(t, "envoy.clusters.endpointpicker_test-pool_test-ns_ext_proc", cluster.Name)
		require.Equal(t, clusterv3.Cluster_STRICT_DNS, cluster.GetType())
		require.Equal(t, clusterv3.Cluster_LEAST_REQUEST, cluster.LbPolicy)
		require.NotNil(t, cluster.LoadAssignment)
		require.Len(t, cluster.LoadAssignment.Endpoints, 1)
	})

	t.Run("nil pool panics", func(t *testing.T) {
		require.Panics(t, func() {
			_, _ = buildExtProcClusterForInferencePoolEndpointPicker(nil)
		})
	})
}

// TestBuildClustersForInferencePoolEndpointPickers tests building clusters from existing clusters.
func TestBuildClustersForInferencePoolEndpointPickers(t *testing.T) {
	// Create a cluster with InferencePool metadata.
	cluster := &clusterv3.Cluster{
		Name: "test-cluster",
		Metadata: &corev3.Metadata{
			FilterMetadata: map[string]*structpb.Struct{
				internalapi.InternalEndpointMetadataNamespace: {
					Fields: map[string]*structpb.Value{
						"per_route_rule_inference_pool": structpb.NewStringValue("test-ns/test-pool/test-epp/9002/duplex/false"),
					},
				},
			},
		},
	}

	t.Run("with InferencePool metadata", func(t *testing.T) {
		clusters := []*clusterv3.Cluster{cluster}
		result, err := buildClustersForInferencePoolEndpointPickers(clusters)
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Contains(t, result[0].Name, "endpointpicker")
	})

	t.Run("without InferencePool metadata", func(t *testing.T) {
		normalCluster := &clusterv3.Cluster{Name: "normal-cluster"}
		clusters := []*clusterv3.Cluster{normalCluster}
		result, err := buildClustersForInferencePoolEndpointPickers(clusters)
		require.NoError(t, err)
		require.Empty(t, result)
	})
}

// TestPostTranslateModify tests the PostTranslateModify method.
func TestPostTranslateModify(t *testing.T) {
	logger := logr.Discard()
	s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	t.Run("empty request", func(t *testing.T) {
		req := &egextension.PostTranslateModifyRequest{}
		resp, err := s.PostTranslateModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("with clusters", func(t *testing.T) {
		cluster := &clusterv3.Cluster{Name: "test-cluster"}
		req := &egextension.PostTranslateModifyRequest{
			Clusters: []*clusterv3.Cluster{cluster},
		}
		resp, err := s.PostTranslateModify(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		// Should have original cluster plus the UDS cluster.
		require.Len(t, resp.Clusters, 2)
		require.Equal(t, "test-cluster", resp.Clusters[0].Name)
		require.Equal(t, "ai-gateway-extproc-uds", resp.Clusters[1].Name)
	})

	t.Run("with log header mapping inserts header_to_metadata filter", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, ptr.To("agent-session-id:session.id"), "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: wellknown.Router}},
		}
		listener := &listenerv3.Listener{
			Name: "test-listener",
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}
		req := &egextension.PostTranslateModifyRequest{Listeners: []*listenerv3.Listener{listener}}
		resp, err := s.PostTranslateModify(context.Background(), req)
		require.NoError(t, err)
		require.Len(t, resp.Listeners, 1)

		outHCM := &httpconnectionmanagerv3.HttpConnectionManager{}
		err = resp.Listeners[0].DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(outHCM)
		require.NoError(t, err)
		require.Len(t, outHCM.HttpFilters, 2)
		require.Equal(t, headerToMetadataFilterName, outHCM.HttpFilters[0].Name)
		require.Equal(t, wellknown.Router, outHCM.HttpFilters[1].Name)

		htmCfg := &htomv3.Config{}
		err = outHCM.HttpFilters[0].GetTypedConfig().UnmarshalTo(htmCfg)
		require.NoError(t, err)
		require.Len(t, htmCfg.RequestRules, 1)
		require.Equal(t, "agent-session-id", htmCfg.RequestRules[0].Header)
		require.Equal(t, "session.id", htmCfg.RequestRules[0].OnHeaderPresent.GetKey())
	})

	t.Run("with existing header_to_metadata merges log mapping", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, ptr.To("agent-session-id:session.id"), "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		existingCfg := &htomv3.Config{
			RequestRules: []*htomv3.Config_Rule{
				{
					Header: "x-ai-eg-mcp-backend",
					OnHeaderPresent: &htomv3.Config_KeyValuePair{
						MetadataNamespace: aigv1b1.AIGatewayFilterMetadataNamespace,
						Key:               "mcp_backend",
						Type:              htomv3.Config_STRING,
					},
				},
			},
		}
		existingFilter := &httpconnectionmanagerv3.HttpFilter{
			Name:       headerToMetadataFilterName,
			ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: mustToAny(t, existingCfg)},
		}
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{existingFilter, {Name: wellknown.Router}},
		}
		listener := &listenerv3.Listener{
			Name: "test-listener",
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}
		req := &egextension.PostTranslateModifyRequest{Listeners: []*listenerv3.Listener{listener}}
		resp, err := s.PostTranslateModify(context.Background(), req)
		require.NoError(t, err)

		outHCM := &httpconnectionmanagerv3.HttpConnectionManager{}
		err = resp.Listeners[0].DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(outHCM)
		require.NoError(t, err)
		require.Len(t, outHCM.HttpFilters, 2)
		require.Equal(t, headerToMetadataFilterName, outHCM.HttpFilters[0].Name)

		mergedCfg := &htomv3.Config{}
		err = outHCM.HttpFilters[0].GetTypedConfig().UnmarshalTo(mergedCfg)
		require.NoError(t, err)
		var headers []string
		for _, rule := range mergedCfg.RequestRules {
			headers = append(headers, rule.Header)
		}
		require.ElementsMatch(t, []string{"x-ai-eg-mcp-backend", "agent-session-id"}, headers)
	})

	t.Run("without log header mapping leaves filters untouched", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, ptr.To(""), "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: wellknown.Router}},
		}
		listener := &listenerv3.Listener{
			Name: "test-listener",
			DefaultFilterChain: &listenerv3.FilterChain{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
					},
				},
			},
		}
		req := &egextension.PostTranslateModifyRequest{Listeners: []*listenerv3.Listener{listener}}
		resp, err := s.PostTranslateModify(context.Background(), req)
		require.NoError(t, err)
		require.Len(t, resp.Listeners, 1)

		outHCM := &httpconnectionmanagerv3.HttpConnectionManager{}
		err = resp.Listeners[0].DefaultFilterChain.Filters[0].GetTypedConfig().UnmarshalTo(outHCM)
		require.NoError(t, err)
		require.Len(t, outHCM.HttpFilters, 1)
		require.Equal(t, wellknown.Router, outHCM.HttpFilters[0].Name)
	})
}

// TestList tests the List method (health check).
func TestList(t *testing.T) {
	logger := logr.Discard()
	s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	t.Run("list health statuses", func(t *testing.T) {
		resp, err := s.List(context.Background(), &grpc_health_v1.HealthListRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotEmpty(t, resp.Statuses)
		require.Contains(t, resp.Statuses, "envoy-gateway-extension-server")
	})
}

// TestQuotaRateLimitConfiguration tests that quota rate limit parameters are correctly configured.
func TestQuotaRateLimitConfiguration(t *testing.T) {
	logger := logr.Discard()

	t.Run("default quota rate limit configuration", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)
		require.NotNil(t, s)
		require.Equal(t, int64(5), s.quotaRateLimitTimeout)
		require.False(t, s.quotaRateLimitFailureModeDeny)
		require.Equal(t, "envoy-ai-gateway-ratelimit.envoy-gateway-system", s.quotaRateLimitServiceHost)
		require.Equal(t, uint32(8081), s.quotaRateLimitServicePort)
	})

	t.Run("custom quota rate limit timeout", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "custom-ratelimit-service", 10, false)
		require.NoError(t, err)
		require.NotNil(t, s)
		require.Equal(t, int64(10), s.quotaRateLimitTimeout)
		require.False(t, s.quotaRateLimitFailureModeDeny)
		require.Equal(t, "custom-ratelimit-service", s.quotaRateLimitServiceHost)
		require.Equal(t, uint32(8081), s.quotaRateLimitServicePort)
	})

	t.Run("quota rate limit with failure mode deny enabled", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, true)
		require.NoError(t, err)
		require.NotNil(t, s)
		require.Equal(t, int64(5), s.quotaRateLimitTimeout)
		require.True(t, s.quotaRateLimitFailureModeDeny)
		require.Equal(t, "envoy-ai-gateway-ratelimit.envoy-gateway-system", s.quotaRateLimitServiceHost)
		require.Equal(t, uint32(8081), s.quotaRateLimitServicePort)
	})

	t.Run("custom quota rate limit with both parameters", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "my-custom-ratelimit", 30, true)
		require.NoError(t, err)
		require.NotNil(t, s)
		require.Equal(t, int64(30), s.quotaRateLimitTimeout)
		require.True(t, s.quotaRateLimitFailureModeDeny)
		require.Equal(t, "my-custom-ratelimit", s.quotaRateLimitServiceHost)
		require.Equal(t, uint32(8081), s.quotaRateLimitServicePort)
	})

	t.Run("custom quota rate limit host with port", func(t *testing.T) {
		s, err := New(newFakeClient(), logger, udsPath, false, nil, nil, "my-custom-ratelimit:9090", 5, false)
		require.NoError(t, err)
		require.NotNil(t, s)
		require.Equal(t, "my-custom-ratelimit", s.quotaRateLimitServiceHost)
		require.Equal(t, uint32(9090), s.quotaRateLimitServicePort)
	})
}

func TestRouteNameFromRouteConfigName(t *testing.T) {
	t.Run("extract namespaced route name", func(t *testing.T) {
		require.Equal(t, "ns/myroute", routeNameFromRouteConfigName("httproute/ns/myroute/rule/0"))
	})

	t.Run("ignore unexpected format", func(t *testing.T) {
		require.Empty(t, routeNameFromRouteConfigName("not-a-route-config-name"))
	})
}

func TestRouteNameFromEnvoyGatewayMetadata(t *testing.T) {
	t.Run("prefer namespaced resource identity", func(t *testing.T) {
		route := &routev3.Route{
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					"envoy-gateway": {
						Fields: map[string]*structpb.Value{
							"resources": structpb.NewListValue(&structpb.ListValue{
								Values: []*structpb.Value{
									structpb.NewStructValue(&structpb.Struct{
										Fields: map[string]*structpb.Value{
											"name":      structpb.NewStringValue("myroute"),
											"namespace": structpb.NewStringValue("default"),
										},
									}),
								},
							}),
						},
					},
				},
			},
		}
		require.Equal(t, "default/myroute", routeNameFromEnvoyGatewayMetadata(route))
	})

	t.Run("fallback to legacy name-only metadata", func(t *testing.T) {
		route := &routev3.Route{
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					"envoy-gateway": {
						Fields: map[string]*structpb.Value{
							"resources": structpb.NewListValue(&structpb.ListValue{
								Values: []*structpb.Value{
									structpb.NewStructValue(&structpb.Struct{
										Fields: map[string]*structpb.Value{
											"name": structpb.NewStringValue("legacy-route"),
										},
									}),
								},
							}),
						},
					},
				},
			},
		}
		require.Equal(t, "legacy-route", routeNameFromEnvoyGatewayMetadata(route))
	})
}

func TestEndpointUpstreamHost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint *endpointv3.LbEndpoint
		want     string
	}{
		{
			name:     "nil endpoint",
			endpoint: &endpointv3.LbEndpoint{},
			want:     "",
		},
		{
			name: "explicit endpoint hostname wins over socket address",
			endpoint: &endpointv3.LbEndpoint{
				HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
					Endpoint: &endpointv3.Endpoint{
						Hostname: "override.example.com",
						Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
							SocketAddress: &corev3.SocketAddress{Address: "bedrock-runtime.us-east-1.amazonaws.com"},
						}},
					},
				},
			},
			want: "override.example.com",
		},
		{
			// Normal EG fqdn Backend: Endpoint.Hostname is empty, the FQDN is in the socket address.
			name: "DNS socket address is used when endpoint hostname is empty",
			endpoint: &endpointv3.LbEndpoint{
				HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
					Endpoint: &endpointv3.Endpoint{
						Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
							SocketAddress: &corev3.SocketAddress{Address: "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"},
						}},
					},
				},
			},
			want: "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com",
		},
		{
			name: "IPv4 socket address is rejected",
			endpoint: &endpointv3.LbEndpoint{
				HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
					Endpoint: &endpointv3.Endpoint{
						Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
							SocketAddress: &corev3.SocketAddress{Address: "10.0.0.1"},
						}},
					},
				},
			},
			want: "",
		},
		{
			name: "IPv6 socket address is rejected",
			endpoint: &endpointv3.LbEndpoint{
				HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
					Endpoint: &endpointv3.Endpoint{
						Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
							SocketAddress: &corev3.SocketAddress{Address: "2001:db8::1"},
						}},
					},
				},
			},
			want: "",
		},
		{
			name: "no hostname and no address",
			endpoint: &endpointv3.LbEndpoint{
				HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{}},
			},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, endpointUpstreamHost(tc.endpoint))
		})
	}
}

func TestStampUpstreamHostMetadata(t *testing.T) {
	socketEndpoint := func(addr string) *endpointv3.LbEndpoint {
		return &endpointv3.LbEndpoint{HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
			Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
				SocketAddress: &corev3.SocketAddress{Address: addr},
			}},
		}}}
	}
	upstreamHost := func(ep *endpointv3.LbEndpoint) (string, bool) {
		m := ep.GetMetadata().GetFilterMetadata()[internalapi.InternalEndpointMetadataNamespace]
		if m == nil {
			return "", false
		}
		v, ok := m.Fields[internalapi.InternalMetadataUpstreamHostKey]
		return v.GetStringValue(), ok
	}
	for _, tc := range []struct {
		name string
		addr string
		want string // "" means not stamped
	}{
		{"public bedrock host is stamped", "bedrock-runtime.us-east-1.amazonaws.com", "bedrock-runtime.us-east-1.amazonaws.com"},
		{"vpce host is stamped", "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com", "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"},
		{"custom vpc host is stamped", "bedrock.corp.internal", "bedrock.corp.internal"},
		{"non-aws host is stamped too (harmless, unused by non-AWS handlers)", "api.openai.com", "api.openai.com"},
		{"ip host is not stamped", "10.0.0.1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := socketEndpoint(tc.addr)
			stampUpstreamHostMetadata(ep)
			got, ok := upstreamHost(ep)
			if tc.want == "" {
				require.False(t, ok, "expected no upstream_host stamp")
				return
			}
			require.True(t, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestSetEndpointMetadataUpstreamHost(t *testing.T) {
	t.Run("initializes metadata from nil", func(t *testing.T) {
		endpoint := &endpointv3.LbEndpoint{}
		setEndpointMetadataUpstreamHost(endpoint, "bedrock-runtime.us-east-1.amazonaws.com")
		m := endpoint.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
		require.Equal(t, "bedrock-runtime.us-east-1.amazonaws.com",
			m.Fields[internalapi.InternalMetadataUpstreamHostKey].GetStringValue())
	})

	t.Run("preserves existing fields", func(t *testing.T) {
		endpoint := &endpointv3.LbEndpoint{
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					internalapi.InternalEndpointMetadataNamespace: {
						Fields: map[string]*structpb.Value{
							internalapi.InternalMetadataBackendNameKey: structpb.NewStringValue("aws-bedrock"),
						},
					},
				},
			},
		}
		setEndpointMetadataUpstreamHost(endpoint, "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com")
		m := endpoint.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
		require.Equal(t, "aws-bedrock", m.Fields[internalapi.InternalMetadataBackendNameKey].GetStringValue())
		require.Equal(t, "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com",
			m.Fields[internalapi.InternalMetadataUpstreamHostKey].GetStringValue())
	})
}
