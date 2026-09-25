// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	htomv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_to_metadata/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

const (
	mcpBackendListenerName = "aigateway-mcp-backend-listener"
	filterNameJWTAuthn     = "envoy.filters.http.jwt_authn"
	filterNameAPIKeyAuth   = "envoy.filters.http.api_key_auth" // #nosec G101
	filterNameExtAuth      = "envoy.filters.http.ext_authz"
)

// Generate the resources needed to support MCP Gateway functionality.
func (s *Server) maybeGenerateResourcesForMCPGateway(req *egextension.PostTranslateModifyRequest) error {
	if len(req.Listeners) == 0 || len(req.Routes) == 0 {
		return nil // Nothing to do, mostly for unit tests.
	}
	// Update existing MCP routes to remove JWT authn filter from non-proxy rules.
	// Order matters: do this before moving rules to the backend listener.
	s.maybeUpdateMCPRoutes(req.Routes)

	// Create routes for the backend listener first to determine if MCP processing is needed
	mcpBackendRoutes := s.createRoutesForBackendListener(req.Routes)

	// Only create the backend listener if there are routes for it
	if mcpBackendRoutes != nil {
		// Extract MCP backend filters from existing listeners and create the backend listener with those filters.
		mcpBackendHTTPFilters, accessLogConfig, err := s.extractMCPBackendFiltersFromMCPProxyListener(req.Listeners)
		if err != nil {
			return fmt.Errorf("failed to extract MCP backend filters from existing listeners: %w", err)
		}
		l, err := s.createBackendListener(mcpBackendHTTPFilters, accessLogConfig)
		if err != nil {
			return fmt.Errorf("failed to create MCP backend listener: %w", err)
		}
		req.Listeners = append(req.Listeners, l)
		req.Routes = append(req.Routes, mcpBackendRoutes)
	}

	// Modify routes with mcp-gateway-generated annotation to use mcpproxy-cluster.
	s.modifyMCPGatewayGeneratedCluster(req.Clusters)
	return nil
}

// createBackendListener creates the backend listener for MCP Gateway.
func (s *Server) createBackendListener(mcpHTTPFilters []*httpconnectionmanagerv3.HttpFilter, accessLogConfig []*accesslogv3.AccessLog) (*listenerv3.Listener, error) {
	httpConManager := &httpconnectionmanagerv3.HttpConnectionManager{
		StatPrefix: fmt.Sprintf("%s-http", mcpBackendListenerName),
		AccessLog:  accessLogConfig,
		// Match the :scheme pseudo-header to the upstream transport protocol.
		SchemeHeaderTransformation: &corev3.SchemeHeaderTransformation{
			MatchUpstream: true,
		},
		RouteSpecifier: &httpconnectionmanagerv3.HttpConnectionManager_Rds{
			Rds: &httpconnectionmanagerv3.Rds{
				RouteConfigName: fmt.Sprintf("%s-route-config", mcpBackendListenerName),
				ConfigSource: &corev3.ConfigSource{
					ConfigSourceSpecifier: &corev3.ConfigSource_Ads{
						Ads: &corev3.AggregatedConfigSource{},
					},
					ResourceApiVersion: corev3.ApiVersion_V3,
				},
			},
		},
	}

	// Add MCP HTTP filters (like credential injection filters) to the backend listener.
	for _, filter := range mcpHTTPFilters {
		s.log.Info("Adding MCP HTTP filter to backend listener", "filterName", filter.Name)
		httpConManager.HttpFilters = append(httpConManager.HttpFilters, filter)
	}

	// Add the header-to-metadata filter to populate MCP metadata so that it can be accessed in the access logs.
	// The MCP Proxy will add these headers to the request (because it does not have direct access to the filter metadata).
	// Here we configure the header-to-metadata filter to extract those headers, populate the filter metadata, and clean
	// the headers up from the request before sending it upstream.
	headersToMetadata := &htomv3.Config{}
	// Sorted, because ranging a map emits the rules in a different order every time. That changes the
	// serialized listener, which bumps the xDS version and makes Envoy replace and drain this listener
	// on every translation, even when no MCP config changed. buildHeaderToMetadataFilter sorts for the
	// same reason.
	for _, h := range slices.Sorted(maps.Keys(internalapi.MCPInternalHeadersToMetadata)) {
		headersToMetadata.RequestRules = append(headersToMetadata.RequestRules,
			&htomv3.Config_Rule{
				Header: h,
				OnHeaderPresent: &htomv3.Config_KeyValuePair{
					MetadataNamespace: aigv1b1.AIGatewayFilterMetadataNamespace,
					Key:               internalapi.MCPInternalHeadersToMetadata[h],
					Type:              htomv3.Config_STRING,
				},
				// If the header was an internal MCP header, we remove it before sending the request upstream.
				Remove: strings.HasPrefix(h, internalapi.MCPMetadataHeaderPrefix),
			},
		)
	}
	a, err := toAny(headersToMetadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal header to metadata filter config: %w", err)
	}
	httpConManager.HttpFilters = append(httpConManager.HttpFilters, &httpconnectionmanagerv3.HttpFilter{
		Name:       "envoy.filters.http.header_to_metadata",
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: a},
	})

	// Add Router filter as the terminal HTTP filter.
	a, err = toAny(&routerv3.Router{})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal router filter config: %w", err)
	}
	httpConManager.HttpFilters = append(httpConManager.HttpFilters, &httpconnectionmanagerv3.HttpFilter{
		Name:       wellknown.Router,
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: a},
	})

	a, err = toAny(httpConManager)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal HTTP Connection Manager for backend listener: %w", err)
	}
	return &listenerv3.Listener{
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
		FilterChains: []*listenerv3.FilterChain{
			{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: a},
					},
				},
			},
		},
	}, nil
}

// maybeUpdateMCPRoutes updates the mcp routes with necessary changes for MCP Gateway.
func (s *Server) maybeUpdateMCPRoutes(routes []*routev3.RouteConfiguration) {
	for _, routeConfig := range routes {
		for _, vh := range routeConfig.VirtualHosts {
			for _, route := range vh.Routes {
				if strings.Contains(route.Name, internalapi.MCPMainHTTPRoutePrefix) {
					// The frontend mcp proxy route(rule/0) forwards to the in-process HTTP MCP proxy.
					if strings.Contains(route.Name, "rule/0") {
						// The MCP proxy can only read HTTP headers, so render the trusted shim's
						// dynamic metadata into headers here. See mcpProxyDynamicMetadataHeaders.
						route.RequestHeadersToAdd = append(route.RequestHeadersToAdd, mcpProxyDynamicMetadataHeaders()...)
						continue
					}
					// Remove the authn filters from the well-known and backend routes.
					// TODO: remove this step once the SecurityPolicy can target the MCP proxy route rule only.
					for _, filterName := range []string{filterNameJWTAuthn, filterNameAPIKeyAuth, filterNameExtAuth} {
						if _, ok := route.TypedPerFilterConfig[filterName]; ok {
							s.log.Info("removing authn filter from well-known and backend routes", "route", route.Name, "filter", filterName)
							delete(route.TypedPerFilterConfig, filterName)
						}
					}
				}
			}
		}
	}
}

// mcpProxyDynamicMetadataHeaders returns the request header mutations applied on the
// route into the in-process MCP proxy. The MCP proxy is a plain HTTP server and can
// only read headers, so values the trusted shim sets as (unforgeable) dynamic metadata
// are rendered into headers here. When a metadata value is empty/absent, Envoy overwrites
// the subset header with an empty value so a client-supplied value cannot bypass the proxy's
// static fallback behavior.
func mcpProxyDynamicMetadataHeaders() []*corev3.HeaderValueOption {
	return []*corev3.HeaderValueOption{
		dynamicMetadataHeader(internalapi.MCPBackendSubsetHeader, internalapi.MCPBackendSubsetMetadataKey),
	}
}

// dynamicMetadataHeader builds a request header whose value is sourced from the dynamic
// metadata key under internalapi.InternalEndpointMetadataNamespace using Envoy's
// %DYNAMIC_METADATA% command operator. OVERWRITE and KeepEmptyValue ensure any
// client-supplied copy is replaced even when trusted metadata is empty or absent.
func dynamicMetadataHeader(header, metadataKey string) *corev3.HeaderValueOption {
	return &corev3.HeaderValueOption{
		AppendAction:   corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
		KeepEmptyValue: true,
		Header: &corev3.HeaderValue{
			Key:   header,
			Value: fmt.Sprintf(`%%DYNAMIC_METADATA(["%s", "%s"])%%`, internalapi.InternalEndpointMetadataNamespace, metadataKey),
		},
	}
}

// createRoutesForBackendListener creates routes for the backend listener.
// The HCM of the backend listener will have all the per-backendRef HTTP routes.
//
// Returns nil if no MCP routes are found.
func (s *Server) createRoutesForBackendListener(routes []*routev3.RouteConfiguration) *routev3.RouteConfiguration {
	var backendListenerRoutes []*routev3.Route
	for _, routeConfig := range routes {
		for _, vh := range routeConfig.VirtualHosts {
			var originalRoutes []*routev3.Route
			for _, route := range vh.Routes {
				if strings.Contains(route.Name, internalapi.MCPPerBackendRefHTTPRoutePrefix) {
					s.log.Info("found MCP route, processing for backend listener", "route", route.Name)
					// Copy the route and modify it to use the backend header and mcpproxy-cluster.
					marshaled, err := proto.Marshal(route)
					if err != nil {
						s.log.Error(err, "failed to marshal route for backend MCP listener", "route", route)
						continue
					}
					copiedRoute := &routev3.Route{}
					if err := proto.Unmarshal(marshaled, copiedRoute); err != nil {
						s.log.Error(err, "failed to unmarshal route for backend MCP listener", "route", route)
						continue
					}
					if routeAction := route.GetRoute(); routeAction != nil {
						if _, ok := routeAction.ClusterSpecifier.(*routev3.RouteAction_Cluster); ok {
							backendListenerRoutes = append(backendListenerRoutes, copiedRoute)
							continue
						}
					}
				}
				originalRoutes = append(originalRoutes, route)
			}
			vh.Routes = originalRoutes
		}
	}
	if len(backendListenerRoutes) == 0 {
		return nil
	}

	s.log.Info("created routes for MCP backend listener", "numRoutes", len(backendListenerRoutes))
	mcpRouteConfig := &routev3.RouteConfiguration{
		Name: fmt.Sprintf("%s-route-config", mcpBackendListenerName),
		VirtualHosts: []*routev3.VirtualHost{
			{
				Name:    fmt.Sprintf("%s-wildcard", mcpBackendListenerName),
				Domains: []string{"*"},
				Routes:  backendListenerRoutes,
			},
		},
	}
	return mcpRouteConfig
}

// modifyMCPGatewayGeneratedCluster points MCP routes at the proxy listener used by
// the current deployment mode.
func (s *Server) modifyMCPGatewayGeneratedCluster(clusters []*clusterv3.Cluster) {
	for _, c := range clusters {
		if strings.Contains(c.Name, internalapi.MCPMainHTTPRoutePrefix) && strings.HasSuffix(c.Name, "/rule/0") {
			name := c.Name
			address := &corev3.Address{
				Address: &corev3.Address_Pipe{
					Pipe: &corev3.Pipe{Path: internalapi.MCPProxySocketPath},
				},
			}
			if s.isStandAloneMode {
				address = &corev3.Address{
					Address: &corev3.Address_SocketAddress{
						SocketAddress: &corev3.SocketAddress{
							Address: "127.0.0.1",
							PortSpecifier: &corev3.SocketAddress_PortValue{
								PortValue: internalapi.MCPProxyPort,
							},
						},
					},
				}
			}
			*c = clusterv3.Cluster{
				Name:                 name,
				ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STATIC},
				ConnectTimeout:       &durationpb.Duration{Seconds: 10},
				LoadAssignment: &endpointv3.ClusterLoadAssignment{
					ClusterName: name,
					Endpoints: []*endpointv3.LocalityLbEndpoints{
						{
							LbEndpoints: []*endpointv3.LbEndpoint{
								{
									HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
										Endpoint: &endpointv3.Endpoint{
											Address: address,
										},
									},
								},
							},
						},
					},
				},
			}
		}
	}
}

// extractMCPBackendFiltersFromMCPProxyListener scans through MCP proxy listeners to find HTTP filters
// that correspond to MCP backend processing (those with MCPBackendFilterPrefix in their names)
// and extracts them from the proxy listeners so they can be moved to the backend listener.
//
// This method also returns the access log configuration to use in the MCP backend listener. We want to use the same
// access log configuration that has been configured in the Gateway.
// The challenge is that the MCP backend listener will have a single HCM, and here we have N listeners, each with its own
// HCM, so we need to decide how to properly configure the access logs in the backend listener based on multiple input
// access log configurations.
//
// The Envoy Gateway extension server works, it will call the main `PostTranslateModify` individually for each gateway. This means
// that this method will receive ONLY listeners for the same gateway.
// Since the access logs are configured in the EnvoyProxy resource, and the Gateway object targets the EnvoyProxy resource via the
// "infrastructure" setting, it is guaranteed that all listeners here will have the same access log configuration, so it is safe to
// just pick the first one.
//
// When using the envoy Gateway `mergeGateways` feature, this method will receive all the listeners attached to the GatewayClass instead.
// This is still safe because in the end all Gateway objects will be attached to the same "infrastructure", so it is still safe to assume
// that all received listeners will have the same access log configuration
func (s *Server) extractMCPBackendFiltersFromMCPProxyListener(listeners []*listenerv3.Listener) ([]*httpconnectionmanagerv3.HttpFilter, []*accesslogv3.AccessLog, error) {
	var (
		mcpHTTPFilters  []*httpconnectionmanagerv3.HttpFilter
		accessLogConfig []*accesslogv3.AccessLog
	)

	for _, listener := range listeners {
		// Skip the backend MCP listener if it already exists.
		if listener.Name == mcpBackendListenerName {
			continue
		}

		// Get filter chains from the listener.
		filterChains := listener.GetFilterChains()
		defaultFC := listener.DefaultFilterChain
		if defaultFC != nil {
			filterChains = append(filterChains, defaultFC)
		}

		// Go through all filter chains to find HTTP Connection Managers.
		for _, chain := range filterChains {
			httpConManager, hcmIndex, err := findHCM(chain)
			if err != nil {
				continue // Skip chains without HCM.
			}

			// All listeners will have the same access log configuration, as they all belong to the same gateway
			// and share the infrastructure. We can just return any not-empty access log config and use that
			// to configure the MCP backend listener with the same settings.
			accessLogConfig = httpConManager.AccessLog

			// Look for MCP HTTP filters in this HCM and extract them.
			var remainingFilters []*httpconnectionmanagerv3.HttpFilter
			for _, filter := range httpConManager.HttpFilters {
				if s.isMCPBackendHTTPFilter(filter) {
					s.log.Info("Found MCP HTTP filter, extracting from original listener", "filterName", filter.Name, "listener", listener.Name)
					mcpHTTPFilters = append(mcpHTTPFilters, filter)
				} else {
					remainingFilters = append(remainingFilters, filter)
				}
			}

			// Update the HCM with remaining filters (MCP filters removed).
			if len(remainingFilters) != len(httpConManager.HttpFilters) {
				httpConManager.HttpFilters = remainingFilters

				// Write the updated HCM back to the filter chain.
				tc := &listenerv3.Filter_TypedConfig{}
				tc.TypedConfig, err = toAny(httpConManager)
				chain.Filters[hcmIndex].ConfigType = tc
				if err != nil {
					return nil, nil, fmt.Errorf("failed to marshal updated HCM for listener %s: %w", listener.Name, err)
				}
			}
		}
	}

	if len(mcpHTTPFilters) > 0 {
		s.log.Info("Extracted MCP HTTP filters", "count", len(mcpHTTPFilters))
	}
	return mcpHTTPFilters, accessLogConfig, nil
}

// isMCPBackendHTTPFilter checks if an HTTP filter is used for MCP backend processing.
func (s *Server) isMCPBackendHTTPFilter(filter *httpconnectionmanagerv3.HttpFilter) bool {
	// Check if the filter name contains the MCP prefix
	// MCP HTTPRouteFilters are typically named with the MCPPerBackendHTTPRouteFilterPrefix.
	if strings.Contains(filter.Name, internalapi.MCPPerBackendHTTPRouteFilterPrefix) {
		return true
	}

	return false
}
