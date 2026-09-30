// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"testing"
	"time"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	htomv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_to_metadata/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestServer_createBackendListener(t *testing.T) {
	tests := []struct {
		name             string
		mcpHTTPFilters   []*httpconnectionmanagerv3.HttpFilter
		accessLogConfig  []*accesslogv3.AccessLog
		expectedListener *listenerv3.Listener
	}{
		{
			name:           "no filters",
			mcpHTTPFilters: nil,
			expectedListener: &listenerv3.Listener{
				Name: mcpBackendListenerName,
				Address: &corev3.Address{
					Address: &corev3.Address_SocketAddress{
						SocketAddress: &corev3.SocketAddress{
							Protocol: corev3.SocketAddress_TCP,
							Address:  "127.0.0.1",
							PortSpecifier: &corev3.SocketAddress_PortValue{
								PortValue: internalapi.MCPBackendListenerPort,
							},
						},
					},
				},
			},
		},
		{
			name:           "no filters with access logs",
			mcpHTTPFilters: nil,
			accessLogConfig: []*accesslogv3.AccessLog{
				{Name: "accesslog1"},
				{Name: "accesslog2"},
			},
			expectedListener: &listenerv3.Listener{
				Name: mcpBackendListenerName,
				Address: &corev3.Address{
					Address: &corev3.Address_SocketAddress{
						SocketAddress: &corev3.SocketAddress{
							Protocol: corev3.SocketAddress_TCP,
							Address:  "127.0.0.1",
							PortSpecifier: &corev3.SocketAddress_PortValue{
								PortValue: internalapi.MCPBackendListenerPort,
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: testr.New(t)}
			listener, err := s.createBackendListener(tt.mcpHTTPFilters, tt.accessLogConfig)
			require.NoError(t, err)

			require.Equal(t, tt.expectedListener.Name, listener.Name)
			require.Equal(t, tt.expectedListener.Address.GetSocketAddress().Address, listener.Address.GetSocketAddress().Address)
			require.Equal(t, tt.expectedListener.Address.GetSocketAddress().GetPortValue(), listener.Address.GetSocketAddress().GetPortValue())
			require.Equal(t, tt.expectedListener.Address.GetSocketAddress().Protocol, listener.Address.GetSocketAddress().Protocol)

			hcm, _, err := findHCM(listener.FilterChains[0])
			require.NoError(t, err)
			require.True(t, hcm.GetSchemeHeaderTransformation().GetMatchUpstream(), "SchemeHeaderTransformation.MatchUpstream should be true")
			require.Len(t, hcm.AccessLog, len(tt.accessLogConfig))
			for i := range tt.accessLogConfig {
				require.Equal(t, tt.accessLogConfig[i].Name, hcm.AccessLog[i].Name)
			}
		})
	}
}

func TestServer_createRoutesForBackendListener(t *testing.T) {
	tests := []struct {
		name          string
		routes        []*routev3.RouteConfiguration
		expectedRoute *routev3.RouteConfiguration
	}{
		{
			name:          "empty",
			routes:        []*routev3.RouteConfiguration{},
			expectedRoute: nil,
		},
		{
			name: "no MCP routes",
			routes: []*routev3.RouteConfiguration{
				{
					VirtualHosts: []*routev3.VirtualHost{
						{
							Name:   "test-vh",
							Routes: []*routev3.Route{{Name: "normal"}},
						},
					},
				},
			},
			expectedRoute: nil,
		},
		{
			name: "with MCP routes",
			routes: []*routev3.RouteConfiguration{
				{
					VirtualHosts: []*routev3.VirtualHost{
						{
							Name:   "test-vh",
							Routes: []*routev3.Route{{Name: "normal"}},
						},
					},
				},
				{
					VirtualHosts: []*routev3.VirtualHost{
						{
							Name:    "mcp-vh",
							Domains: []string{"*"},
							Routes: []*routev3.Route{
								{
									Name: internalapi.MCPPerBackendRefHTTPRoutePrefix + "foo/rule/0",
									Action: &routev3.Route_Route{
										Route: &routev3.RouteAction{ClusterSpecifier: &routev3.RouteAction_Cluster{}},
									},
								},
								{
									Name: internalapi.MCPPerBackendRefHTTPRoutePrefix + "bar/rule/1",
									Action: &routev3.Route_Route{
										Route: &routev3.RouteAction{ClusterSpecifier: &routev3.RouteAction_Cluster{}},
									},
								},
							},
						},
					},
				},
			},
			expectedRoute: &routev3.RouteConfiguration{
				Name: "aigateway-mcp-backend-listener-route-config",
				VirtualHosts: []*routev3.VirtualHost{
					{
						Domains: []string{"*"},
						Name:    "aigateway-mcp-backend-listener-wildcard",
						Routes: []*routev3.Route{
							{Name: internalapi.MCPPerBackendRefHTTPRoutePrefix + "foo/rule/0", Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{ClusterSpecifier: &routev3.RouteAction_Cluster{}},
							}},
							{Name: internalapi.MCPPerBackendRefHTTPRoutePrefix + "bar/rule/1", Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{ClusterSpecifier: &routev3.RouteAction_Cluster{}},
							}},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: testr.New(t)}
			route := s.createRoutesForBackendListener(tt.routes)
			if tt.expectedRoute == nil {
				require.Nil(t, route)
			} else {
				require.Empty(t, cmp.Diff(tt.expectedRoute, route, protocmp.Transform()))
			}
		})
	}
}

func TestServer_modifyMCPGatewayGeneratedCluster(t *testing.T) {
	tests := []struct {
		name       string
		standalone bool
		assertAddr func(*testing.T, *corev3.Address)
	}{
		{
			name: "uses UDS in controller mode",
			assertAddr: func(t *testing.T, address *corev3.Address) {
				require.Equal(t, internalapi.MCPProxySocketPath, address.GetPipe().GetPath())
			},
		},
		{
			name:       "uses TCP in standalone mode",
			standalone: true,
			assertAddr: func(t *testing.T, address *corev3.Address) {
				socket := address.GetSocketAddress()
				require.Equal(t, "127.0.0.1", socket.GetAddress())
				require.Equal(t, uint32(internalapi.MCPProxyPort), socket.GetPortValue())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normal := &clusterv3.Cluster{Name: "normal-cluster"}
			target := &clusterv3.Cluster{Name: internalapi.MCPMainHTTPRoutePrefix + "foo-bar/rule/0"}
			clusters := []*clusterv3.Cluster{normal, target}
			s := &Server{log: testr.New(t), isStandAloneMode: tt.standalone}
			s.modifyMCPGatewayGeneratedCluster(clusters)

			require.Equal(t, &clusterv3.Cluster{Name: "normal-cluster"}, normal)
			require.Equal(t, clusterv3.Cluster_STATIC, target.GetType())
			require.Equal(t, 10*time.Second, target.GetConnectTimeout().AsDuration())
			address := target.GetLoadAssignment().GetEndpoints()[0].GetLbEndpoints()[0].GetEndpoint().GetAddress()
			tt.assertAddr(t, address)
		})
	}
}

func TestServer_isMCPBackendHTTPFilter(t *testing.T) {
	tests := []struct {
		name     string
		filter   *httpconnectionmanagerv3.HttpFilter
		expected bool
	}{
		{
			name:     "MCP backend filter",
			filter:   &httpconnectionmanagerv3.HttpFilter{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test"},
			expected: true,
		},
		{
			name:     "regular filter",
			filter:   &httpconnectionmanagerv3.HttpFilter{Name: "envoy.filters.http.router"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: testr.New(t)}
			result := s.isMCPBackendHTTPFilter(tt.filter)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestServer_maybeUpdateMCPRoutes(t *testing.T) {
	emptyConfig := &anypb.Any{TypeUrl: "type.googleapis.com/google.protobuf.Empty"}

	tests := []struct {
		name           string
		routes         []*routev3.RouteConfiguration
		expectedRoutes []*routev3.RouteConfiguration
	}{
		{
			name: "removes JWT from backend routes",
			routes: []*routev3.RouteConfiguration{
				{
					VirtualHosts: []*routev3.VirtualHost{
						{
							Name: "vh",
							Routes: []*routev3.Route{
								{
									Name: internalapi.MCPMainHTTPRoutePrefix + "foo/rule/0",
									TypedPerFilterConfig: map[string]*anypb.Any{
										filterNameJWTAuthn:   emptyConfig,
										filterNameAPIKeyAuth: emptyConfig,
										filterNameExtAuth:    emptyConfig,
									},
								},
								{
									Name: internalapi.MCPMainHTTPRoutePrefix + "foo/rule/1",
									TypedPerFilterConfig: map[string]*anypb.Any{
										filterNameJWTAuthn:   emptyConfig,
										filterNameAPIKeyAuth: emptyConfig,
										filterNameExtAuth:    emptyConfig,
										"other-filter":       emptyConfig,
									},
								},
							},
						},
					},
				},
			},
			expectedRoutes: []*routev3.RouteConfiguration{
				{
					VirtualHosts: []*routev3.VirtualHost{
						{
							Name: "vh",
							Routes: []*routev3.Route{
								{
									Name: internalapi.MCPMainHTTPRoutePrefix + "foo/rule/0",
									TypedPerFilterConfig: map[string]*anypb.Any{
										filterNameJWTAuthn:   emptyConfig,
										filterNameAPIKeyAuth: emptyConfig,
										filterNameExtAuth:    emptyConfig,
									},
									// rule/0 (frontend MCP proxy route) gets the dynamic-metadata -> header
									// injection so the HTTP MCP proxy can read the backend subset; authn is
									// preserved.
									RequestHeadersToAdd: []*corev3.HeaderValueOption{
										{
											AppendAction:   corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
											KeepEmptyValue: true,
											Header: &corev3.HeaderValue{
												Key:   internalapi.MCPBackendSubsetHeader,
												Value: `%DYNAMIC_METADATA(["aigateway.envoy.io", "mcp_backend_subset"])%`,
											},
										},
									},
								},
								{
									Name: internalapi.MCPMainHTTPRoutePrefix + "foo/rule/1",
									TypedPerFilterConfig: map[string]*anypb.Any{
										"other-filter": emptyConfig,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: testr.New(t)}
			s.maybeUpdateMCPRoutes(tt.routes)
			require.Empty(t, cmp.Diff(tt.expectedRoutes, tt.routes, protocmp.Transform()))
		})
	}
}

func TestServer_extractMCPBackendFiltersFromMCPProxyListener(t *testing.T) {
	tests := []struct {
		name               string
		listeners          []*listenerv3.Listener
		expectedFilters    []*httpconnectionmanagerv3.HttpFilter
		expectedAccessLogs []*accesslogv3.AccessLog
	}{
		{
			name:               "no listeners",
			listeners:          []*listenerv3.Listener{},
			expectedFilters:    nil,
			expectedAccessLogs: nil,
		},
		{
			name: "listener with MCP backend filter without access logs",
			listeners: []*listenerv3.Listener{
				{
					Name: "test-listener",
					FilterChains: []*listenerv3.FilterChain{
						{
							Filters: []*listenerv3.Filter{
								{
									Name: wellknown.HTTPConnectionManager,
									ConfigType: &listenerv3.Filter_TypedConfig{
										TypedConfig: mustToAny(t, &httpconnectionmanagerv3.HttpConnectionManager{
											StatPrefix: "http",
											HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
												{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter"},
												{Name: "envoy.filters.http.router"},
											},
										}),
									},
								},
							},
						},
					},
				},
			},
			expectedFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter"},
			},
		},
		{
			name: "listener with MCP backend filter with access logs",
			listeners: []*listenerv3.Listener{
				{
					Name: "test-listener1",
					FilterChains: []*listenerv3.FilterChain{
						{
							Filters: []*listenerv3.Filter{
								{
									Name: wellknown.HTTPConnectionManager,
									ConfigType: &listenerv3.Filter_TypedConfig{
										TypedConfig: mustToAny(t, &httpconnectionmanagerv3.HttpConnectionManager{
											StatPrefix: "http",
											HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
												{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter"},
												{Name: "envoy.filters.http.router"},
											},
										}),
									},
								},
							},
						},
					},
				},
				{
					Name: "test-listener2",
					FilterChains: []*listenerv3.FilterChain{
						{
							Filters: []*listenerv3.Filter{
								{
									Name: wellknown.HTTPConnectionManager,
									ConfigType: &listenerv3.Filter_TypedConfig{
										TypedConfig: mustToAny(t, &httpconnectionmanagerv3.HttpConnectionManager{
											StatPrefix: "http",
											HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
												{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter2"},
												{Name: "envoy.filters.http.router"},
											},
											AccessLog: []*accesslogv3.AccessLog{
												{Name: "listener2-accesslog1"},
												{Name: "listener2-accesslog2"},
											},
										}),
									},
								},
							},
						},
					},
				},
				{
					Name: "test-listener3",
					FilterChains: []*listenerv3.FilterChain{
						{
							Filters: []*listenerv3.Filter{
								{
									Name: wellknown.HTTPConnectionManager,
									ConfigType: &listenerv3.Filter_TypedConfig{
										TypedConfig: mustToAny(t, &httpconnectionmanagerv3.HttpConnectionManager{
											StatPrefix: "http",
											HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
												{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter3"},
												{Name: "envoy.filters.http.router"},
											},
											AccessLog: []*accesslogv3.AccessLog{
												{Name: "listener3-accesslog1"},
												{Name: "listener3-accesslog2"},
											},
										}),
									},
								},
							},
						},
					},
				},
			},
			expectedFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter"},
				{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter2"},
				{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter3"},
			},
			expectedAccessLogs: []*accesslogv3.AccessLog{
				{Name: "listener3-accesslog1"},
				{Name: "listener3-accesslog2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: testr.New(t)}
			filters, accessLogConfigs, err := s.extractMCPBackendFiltersFromMCPProxyListener(tt.listeners)
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.expectedFilters, filters, protocmp.Transform()))
			require.Empty(t, cmp.Diff(tt.expectedAccessLogs, accessLogConfigs, protocmp.Transform()))
		})
	}
}

func TestServer_maybeGenerateResourcesForMCPGateway(t *testing.T) {
	tests := []struct {
		name          string
		req           *egextension.PostTranslateModifyRequest
		check         func(t *testing.T, req *egextension.PostTranslateModifyRequest)
		expectedError bool
	}{
		{
			name: "no listeners or routes",
			req: &egextension.PostTranslateModifyRequest{
				Listeners: []*listenerv3.Listener{},
				Routes:    []*routev3.RouteConfiguration{},
			},
			check: func(t *testing.T, req *egextension.PostTranslateModifyRequest) {
				require.Empty(t, req.Listeners)
				require.Empty(t, req.Routes)
			},
		},
		{
			name: "with MCP routes and listeners",
			req: &egextension.PostTranslateModifyRequest{
				Listeners: []*listenerv3.Listener{
					{
						Name: "test-listener",
						FilterChains: []*listenerv3.FilterChain{
							{
								Filters: []*listenerv3.Filter{
									{
										Name: wellknown.HTTPConnectionManager,
										ConfigType: &listenerv3.Filter_TypedConfig{
											TypedConfig: mustToAny(t, &httpconnectionmanagerv3.HttpConnectionManager{
												StatPrefix: "http",
												HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
													{Name: internalapi.MCPPerBackendHTTPRouteFilterPrefix + "test-filter"},
													{Name: "envoy.filters.http.router"},
												},
											}),
										},
									},
								},
							},
						},
					},
				},
				Routes: []*routev3.RouteConfiguration{
					{
						VirtualHosts: []*routev3.VirtualHost{
							{
								Name:    "mcp-vh",
								Domains: []string{"*"},
								Routes: []*routev3.Route{
									{
										Name: internalapi.MCPPerBackendRefHTTPRoutePrefix + "foo/rule/0",
										Action: &routev3.Route_Route{
											Route: &routev3.RouteAction{ClusterSpecifier: &routev3.RouteAction_Cluster{}},
										},
									},
								},
							},
						},
					},
				},
				Clusters: []*clusterv3.Cluster{
					{Name: internalapi.MCPMainHTTPRoutePrefix + "foo-bar/rule/0"},
				},
			},
			check: func(t *testing.T, req *egextension.PostTranslateModifyRequest) {
				require.Len(t, req.Listeners, 2)
				require.Equal(t, mcpBackendListenerName, req.Listeners[1].Name)

				require.Len(t, req.Routes, 2)
				require.Equal(t, "aigateway-mcp-backend-listener-route-config", req.Routes[1].Name)

				require.Len(t, req.Clusters, 1)
				require.Equal(t, internalapi.MCPMainHTTPRoutePrefix+"foo-bar/rule/0", req.Clusters[0].Name)
				require.Equal(t, clusterv3.Cluster_STATIC, req.Clusters[0].GetClusterDiscoveryType().(*clusterv3.Cluster_Type).Type)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: testr.New(t)}
			err := s.maybeGenerateResourcesForMCPGateway(tt.req)
			if tt.expectedError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				tt.check(t, tt.req)
			}
		})
	}
}

// TestServer_createBackendListener_headerToMetadataRules pins the header_to_metadata rules the MCP
// backend listener is built with: a stable order, and Remove set only for the internal metadata
// headers that must not reach the upstream MCP server.
func TestServer_createBackendListener_headerToMetadataRules(t *testing.T) {
	s := &Server{log: testr.New(t)}

	type rule struct {
		header string
		key    string
		remove bool
	}
	rulesOf := func(t *testing.T) []rule {
		t.Helper()
		listener, err := s.createBackendListener(nil, nil)
		require.NoError(t, err)
		hcm, _, err := findHCM(listener.FilterChains[0])
		require.NoError(t, err)
		i, filter := findHeaderToMetadataFilter(hcm.HttpFilters)
		require.NotEqual(t, -1, i)
		cfg := &htomv3.Config{}
		require.NoError(t, filter.GetTypedConfig().UnmarshalTo(cfg))
		rules := make([]rule, 0, len(cfg.RequestRules))
		for _, r := range cfg.RequestRules {
			rules = append(rules, rule{r.GetHeader(), r.GetOnHeaderPresent().GetKey(), r.GetRemove()})
		}
		return rules
	}

	got := rulesOf(t)
	require.Equal(t, []rule{
		{"x-ai-eg-mcp-backend", "mcp_backend", false},
		{"x-ai-eg-mcp-metadata-method", "mcp_method", true},
		{"x-ai-eg-mcp-metadata-request-id", "mcp_request_id", true},
		{"x-ai-eg-mcp-metadata-resource-uri", "mcp_resource_uri", true},
		{"x-ai-eg-mcp-metadata-tool-name", "mcp_tool_name", true},
	}, got)

	// Ranging a map is randomized per-iteration, so an unsorted build would differ across calls. An
	// unstable order changes the serialized listener and makes Envoy drain it on every translation.
	for range 20 {
		require.Equal(t, got, rulesOf(t))
	}
}
