// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	egextension "github.com/envoyproxy/gateway/proto/extension"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	mutation_rulesv3 "github.com/envoyproxy/go-control-plane/envoy/config/common/mutation_rules/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	header_mutationv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_mutation/v3"
	upstream_codecv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/upstream_codec/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	httpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

const (
	extProcUDSClusterName = "ai-gateway-extproc-uds"
	aiGatewayExtProcName  = "envoy.filters.http.ext_proc/aigateway"
	// aiGatewayHeaderMutationName is matched by the filter strip in maybeModifyCluster.
	aiGatewayHeaderMutationName = "envoy.filters.http.header_mutation"
	noBackendRefIndex           = -1

	// Metadata Envoy Gateway stamps on the resources it builds, naming the objects each came from.
	envoyGatewayMetadataNamespace    = "envoy-gateway"
	envoyGatewayMetadataResourcesKey = "resources"
)

type aiGatewayClusterName struct {
	namespace       string
	routeName       string
	ruleIndex       int
	backendRefIndex int
}

// parseAIGatewayClusterName parses the cluster names generated for AIGatewayRoute rules.
// Envoy Gateway uses a route-level cluster name unless it creates a cluster for each backend.
func parseAIGatewayClusterName(name string) (aiGatewayClusterName, error) {
	parts := strings.Split(name, "/")
	if (len(parts) != 5 && len(parts) != 7) || parts[0] != "httproute" || parts[3] != "rule" {
		return aiGatewayClusterName{}, fmt.Errorf("invalid AIGatewayRoute cluster name %q", name)
	}

	ruleIndex, err := strconv.Atoi(parts[4])
	if err != nil || ruleIndex < 0 {
		return aiGatewayClusterName{}, fmt.Errorf("invalid HTTPRoute rule index %q", parts[4])
	}

	clusterName := aiGatewayClusterName{
		namespace:       parts[1],
		routeName:       parts[2],
		ruleIndex:       ruleIndex,
		backendRefIndex: noBackendRefIndex,
	}
	if len(parts) == 5 {
		return clusterName, nil
	}
	if parts[5] != "backend" {
		return aiGatewayClusterName{}, fmt.Errorf("invalid AIGatewayRoute backend cluster name %q", name)
	}

	backendRefIndex, err := strconv.Atoi(parts[6])
	if err != nil || backendRefIndex < 0 {
		return aiGatewayClusterName{}, fmt.Errorf("invalid HTTPRoute backend index %q", parts[6])
	}
	clusterName.backendRefIndex = backendRefIndex
	return clusterName, nil
}

// hasAIGatewayClusterName reports whether a cluster name has the shape maybeModifyCluster accepts.
// Plain HTTPRoutes share it, so this is a short-circuit, not a filter.
func hasAIGatewayClusterName(cluster *clusterv3.Cluster) bool {
	_, err := parseAIGatewayClusterName(cluster.GetName())
	return err == nil
}

// PostTranslateModify allows an extension to modify the clusters and secrets in the xDS config
// after the initial translation is complete. This method is responsible for:
//
// 1. Modifying existing clusters (e.g., adding metadata, adjusting configurations)
// 2. Adding additional clusters needed for InferencePool support (EPP clusters)
// 3. Ensuring the AI Gateway external processor UDS cluster exists
//
// For InferencePool support, this method creates additional STRICT_DNS clusters that
// connect to the endpoint picker services specified in InferencePool resources.
func (s *Server) PostTranslateModify(ctx context.Context, req *egextension.PostTranslateModifyRequest) (*egextension.PostTranslateModifyResponse, error) {
	var extProcUDSExist bool

	// Resolved once per snapshot: the namespaces are the Gateway's and apply to every AI cluster.
	var metadataForwardingNamespaces []string
	if slices.ContainsFunc(req.Clusters, hasAIGatewayClusterName) {
		var nsErr error
		if metadataForwardingNamespaces, nsErr = s.metadataForwardingNamespacesForSnapshot(ctx, req.Routes); nsErr != nil {
			return nil, fmt.Errorf("failed to resolve the metadata forwarding namespaces: %w", nsErr)
		}
	}

	// Process existing clusters - may add metadata or modify configurations.
	for _, cluster := range req.Clusters {
		if err := s.maybeModifyCluster(ctx, cluster, metadataForwardingNamespaces); err != nil {
			return nil, fmt.Errorf("failed to modify cluster %s: %w", cluster.Name, err)
		}
		extProcUDSExist = extProcUDSExist || cluster.Name == extProcUDSClusterName
	}

	// Add external processor clusters for InferencePool backends.
	// These clusters connect to the endpoint picker services (EPP) specified in InferencePool resources.
	cs, err := buildClustersForInferencePoolEndpointPickers(req.Clusters)
	if err != nil {
		return nil, fmt.Errorf("failed to build clusters for InferencePool endpoint pickers: %w", err)
	}
	req.Clusters = append(req.Clusters, cs...)

	// Envoy Gateway merges a rule's weighted InferencePool backendRefs into a single cluster and a
	// single non-weighted route, since it doesn't support WeightedClusters for custom backend kinds.
	// Recover the real per-pool weights from the owning HTTPRoute and rewrite each such route into a
	// genuine weighted-cluster route, with one cloned cluster per pool. See the "Weighted routing
	// across multiple InferencePools" overview comment above the weightedInferencePool type below
	// for the full mechanism.
	if req.Clusters, err = s.splitWeightedInferencePoolRoutes(ctx, req.Routes, req.Clusters); err != nil {
		return nil, fmt.Errorf("failed to split weighted InferencePool routes: %w", err)
	}

	// Modify listeners and routes to support InferencePool backends.
	if err = s.maybeModifyListenerAndRoutes(req.Listeners, req.Routes); err != nil {
		return nil, fmt.Errorf("failed to modify listeners and routes for InferencePool support: %w", err)
	}

	// Apply the per-rule stream idle timeout to the generated routes.
	if err = s.applyStreamIdleTimeouts(ctx, req.Routes); err != nil {
		return nil, fmt.Errorf("failed to apply stream idle timeouts: %w", err)
	}

	// Ensure the AI Gateway external processor UDS cluster exists.
	// This cluster is used for communication with the AI Gateway's main external processor.
	if !extProcUDSExist {
		po := &httpv3.HttpProtocolOptions{}
		po.UpstreamProtocolOptions = &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
			ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
				Http2ProtocolOptions: &corev3.Http2ProtocolOptions{
					// https://github.com/envoyproxy/gateway/blob/932b8b155fa562ae917da19b497a4370733478f1/internal/xds/translator/listener.go#L50-L53
					InitialConnectionWindowSize: wrapperspb.UInt32(1048576),
					InitialStreamWindowSize:     wrapperspb.UInt32(65536),
				},
			},
		}}
		var poAny *anypb.Any
		poAny, err = toAny(po)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal HttpProtocolOptions to Any: %w", err)
		}
		req.Clusters = append(req.Clusters, &clusterv3.Cluster{
			Name:                 extProcUDSClusterName,
			ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STATIC},
			// https://github.com/envoyproxy/gateway/blob/932b8b155fa562ae917da19b497a4370733478f1/api/v1alpha1/timeout_types.go#L25
			ConnectTimeout: &durationpb.Duration{Seconds: 10},
			TypedExtensionProtocolOptions: map[string]*anypb.Any{
				"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": poAny,
			},
			// Default is 32768 bytes == 32 KiB which seems small:
			// https://github.com/envoyproxy/gateway/blob/932b8b155fa562ae917da19b497a4370733478f1/internal/xds/translator/cluster.go#L49
			//
			// So, we set it to 50MBi.
			PerConnectionBufferLimitBytes: wrapperspb.UInt32(52428800),
			LoadAssignment: &endpointv3.ClusterLoadAssignment{
				ClusterName: extProcUDSClusterName,
				Endpoints: []*endpointv3.LocalityLbEndpoints{
					{
						LbEndpoints: []*endpointv3.LbEndpoint{
							{
								HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
									Endpoint: &endpointv3.Endpoint{
										Address: &corev3.Address{
											Address: &corev3.Address_Pipe{
												Pipe: &corev3.Pipe{
													Path: s.udsPath,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
		s.log.Info("Added extproc-uds cluster to the list of clusters")
	}

	// Generate the resources needed to support MCP Gateway functionality.
	if err = s.maybeGenerateResourcesForMCPGateway(req); err != nil {
		return nil, fmt.Errorf("failed to generate resources for MCP Gateway: %w", err)
	}
	if err = s.insertRequestHeaderToMetadataFilters(req.Listeners); err != nil {
		return nil, fmt.Errorf("failed to insert request header metadata filter: %w", err)
	}

	// Inject rate limit filter into listener HCM filter chains, add rate limit service cluster,
	// and patch routes with rate limit actions for QuotaPolicy enforcement.
	req.Clusters, err = s.maybeInjectQuotaRateLimiting(ctx, req.Clusters, req.Listeners, req.Routes)
	if err != nil {
		return nil, fmt.Errorf("failed to inject quota rate limiting: %w", err)
	}

	response := &egextension.PostTranslateModifyResponse{Clusters: req.Clusters, Secrets: req.Secrets, Listeners: req.Listeners, Routes: req.Routes}
	return response, nil
}

// applyStreamIdleTimeouts walks the generated route configurations and sets the per-try idle
// timeout on every AIGatewayRoute route whose rule configures StreamIdleTimeout.
// Lookups are cached to avoid hitting the API server more than once per route.
func (s *Server) applyStreamIdleTimeouts(ctx context.Context, routeConfigs []*routev3.RouteConfiguration) error {
	cache := make(map[client.ObjectKey]*aigv1b1.AIGatewayRoute)
	for _, rc := range routeConfigs {
		for _, vh := range rc.VirtualHosts {
			for _, route := range vh.Routes {
				if err := s.maybeSetStreamIdleTimeout(ctx, route, cache); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// maybeSetStreamIdleTimeout sets route.retry_policy.per_try_idle_timeout from the rule's
// StreamIdleTimeout. Envoy resets the upstream stream when no bytes arrive within that
// duration, so a retry policy covering `reset` falls over to the next backend before any
// response reaches the downstream client.
func (s *Server) maybeSetStreamIdleTimeout(ctx context.Context, route *routev3.Route, cache map[client.ObjectKey]*aigv1b1.AIGatewayRoute) error {
	action := route.GetRoute()
	if action == nil {
		// Not a forwarding route (e.g. DirectResponse, Redirect).
		return nil
	}

	key, ruleIndex, ok := parseHTTPRouteRuleFromRouteName(route.Name)
	if !ok {
		return nil
	}

	aigwRoute, err := s.retrieveAndCacheAIGatewayRoute(ctx, cache, key)
	if err != nil {
		return err
	}
	if aigwRoute == nil {
		// Not an AIGatewayRoute-owned route, or it was deleted during translation.
		return nil
	}

	// The list of rules in the AIGatewayRoute may have changed since this route was generated,
	// so we check the rule index is still valid before applying the timeout.
	if ruleIndex >= len(aigwRoute.Spec.Rules) {
		return nil
	}

	timeout := aigwRoute.Spec.Rules[ruleIndex].GetStreamIdleTimeout()
	if timeout <= 0 {
		return nil
	}

	if action.RetryPolicy == nil {
		action.RetryPolicy = &routev3.RetryPolicy{}
	}
	action.RetryPolicy.PerTryIdleTimeout = durationpb.New(timeout)
	return nil
}

// retrieveAndCacheAIGatewayRoute returns the AIGatewayRoute for the key and saves the result.
// If the route is not found it will be  cached as nil, so one translation pass hits the API server at most once per route.
func (s *Server) retrieveAndCacheAIGatewayRoute(ctx context.Context, cache map[client.ObjectKey]*aigv1b1.AIGatewayRoute, key client.ObjectKey) (*aigv1b1.AIGatewayRoute, error) {
	if cached, ok := cache[key]; ok {
		return cached, nil
	}
	var aigwRoute aigv1b1.AIGatewayRoute
	if err := s.k8sClient.Get(ctx, key, &aigwRoute); err != nil {
		if apierrors.IsNotFound(err) {
			cache[key] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get AIGatewayRoute %s/%s: %w", key.Namespace, key.Name, err)
	}
	cache[key] = &aigwRoute
	return &aigwRoute, nil
}

// maybeModifyCluster modifies clusters generated from AIGatewayRoute resources to add
// necessary configurations for AI Gateway functionality. This function performs several key tasks:
//
//  1. Populates cluster endpoint metadata per backend - This is a workaround until
//     https://github.com/envoyproxy/gateway/issues/5523 is resolved and endpoint-level metadata
//     is supported in external processors.
//
//  2. Inserts the upstream external processor filter for request/response processing.
//     See: https://github.com/envoyproxy/gateway/issues/5881
//
// 3. Inserts the header mutation filter for dynamic header modifications.
//
// 4. Configures special handling for InferencePool clusters (ORIGINAL_DST type).
//
// The resulting configuration is similar to the envoy.yaml files in tests/data-plane/.
// Only clusters with names matching the AIGatewayRoute pattern are modified.
func (s *Server) maybeModifyCluster(ctx context.Context, cluster *clusterv3.Cluster, metadataForwardingNamespaces []string) error {
	clusterName, err := parseAIGatewayClusterName(cluster.Name)
	if err != nil {
		s.log.Info("non-ai-gateway cluster name", "cluster_name", cluster.Name, "error", err)
		return nil
	}
	httpRouteNamespace := clusterName.namespace
	httpRouteName := clusterName.routeName
	httpRouteRuleIndex := clusterName.ruleIndex

	// Check if this rule has InferencePool backends.
	pools := getInferencePoolsByMetadata(cluster.Metadata)
	// Get the HTTPRoute object from the cluster name.
	var aigwRoute aigv1b1.AIGatewayRoute
	err = s.k8sClient.Get(ctx, client.ObjectKey{Namespace: httpRouteNamespace, Name: httpRouteName}, &aigwRoute)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.log.Info("Skipping non-AIGatewayRoute HTTPRoute cluster modification",
				"namespace", httpRouteNamespace, "name", httpRouteName)
			return nil
		}
		s.log.Error(err, "failed to get AIGatewayRoute object",
			"namespace", httpRouteNamespace, "name", httpRouteName)
		return err
	}

	// Get the backend from the HTTPRoute object.
	if httpRouteRuleIndex >= len(aigwRoute.Spec.Rules) {
		s.log.Info("HTTPRoute rule index out of range",
			"cluster_name", cluster.Name, "rule_index", httpRouteRuleIndex)
		return nil
	}
	httpRouteRule := &aigwRoute.Spec.Rules[httpRouteRuleIndex]
	if clusterName.backendRefIndex != noBackendRefIndex && clusterName.backendRefIndex >= len(httpRouteRule.BackendRefs) {
		s.log.Info("HTTPRoute backend index out of range",
			"cluster_name", cluster.Name, "backend_index", clusterName.backendRefIndex)
		return nil
	}

	// Only process LoadAssignment for non-InferencePool backends.
	if len(pools) == 0 {
		switch {
		case cluster.LoadAssignment == nil:
			// LoadAssignment is nil when the cluster's endpoints are EDS-managed: delivered out of band
			// (e.g. standalone mode), not inlined at translate time. There is no LbEndpoint here, so we
			// only set the backend name on cluster-level metadata; the upstream ext_proc filter resolves
			// the backend via XDSClusterMetadataBackendNamePath.
			//
			// TODO(aws-signing): no LbEndpoint to stamp here, so an AWS backend on an EDS cluster falls
			// back to the region-based default signing host and a VPCE host is lost. See ai-gateway#902 / #950.
			s.log.Info("LoadAssignment is nil, setting cluster-level metadata", "cluster_name", cluster.Name)
			if len(httpRouteRule.BackendRefs) > 0 {
				backendRefIndex := 0
				if clusterName.backendRefIndex != noBackendRefIndex {
					backendRefIndex = clusterName.backendRefIndex
				}
				backendRef := httpRouteRule.BackendRefs[backendRefIndex]
				setClusterMetadataBackendName(cluster, aigwRoute.Namespace, backendRef.Name, aigwRoute.Name, httpRouteRuleIndex, backendRefIndex)
			}
		case clusterName.backendRefIndex != noBackendRefIndex:
			backendRef := httpRouteRule.BackendRefs[clusterName.backendRefIndex]
			for _, endpoints := range cluster.LoadAssignment.Endpoints {
				if backendRef.Priority != nil {
					endpoints.Priority = *backendRef.Priority
				}
				for _, endpoint := range endpoints.LbEndpoints {
					setEndpointMetadataBackendName(endpoint, aigwRoute.Namespace, backendRef.Name, aigwRoute.Name, httpRouteRuleIndex, clusterName.backendRefIndex)
					stampUpstreamHostMetadata(endpoint)
				}
			}
		default:
			// Populate the metadata for each endpoint in the LoadAssignment.
			var lbEndpointIndex int
			for i, backendRef := range httpRouteRule.BackendRefs {
				// The weight of 0 means this backend is disabled and is not included in the LoadAssignment by EG,
				// so we skip it here.
				if backendRef.Weight != nil && *backendRef.Weight == 0 {
					continue
				}
				// httpRouteRule.BackendRefs comes from a fresh
				// s.k8sClient.Get(&aigwRoute); cluster.LoadAssignment is
				// whatever Envoy Gateway translated for this cluster. The two
				// can be a revision apart, so the rule can carry more
				// non-zero-weight BackendRefs than the cluster has endpoint
				// groups (e.g. an AIGatewayRoute gained a BackendRef whose
				// AIServiceBackend does not resolve yet, so the generated
				// HTTPRoute - and the cluster - stay a revision behind).
				// lbEndpointIndex only increases, so once it is out of range
				// every remaining backendRef is too; stop rather than index out
				// of bounds. The rule and backendRef indices are already
				// guarded the same way earlier in this function. The unmatched
				// backend(s) get their endpoint metadata stamped on a later
				// reconcile once the counts agree.
				if lbEndpointIndex >= len(cluster.LoadAssignment.Endpoints) {
					s.log.Info("cluster LoadAssignment has fewer endpoint groups than non-zero-weight backendRefs",
						"cluster_name", cluster.Name, "backend_index", i,
						"load_assignment_endpoints", len(cluster.LoadAssignment.Endpoints))
					break
				}
				endpoints := cluster.LoadAssignment.Endpoints[lbEndpointIndex]
				lbEndpointIndex++
				name := backendRef.Name
				namespace := aigwRoute.Namespace
				if backendRef.Priority != nil {
					endpoints.Priority = *backendRef.Priority
				}
				for _, endpoint := range endpoints.LbEndpoints {
					setEndpointMetadataBackendName(endpoint, namespace, name, aigwRoute.Name, httpRouteRuleIndex, i)
					stampUpstreamHostMetadata(endpoint)
				}
			}
		}
	} else {
		// we can only specify one backend in a rule for InferencePool.
		backendRefIndex := 0
		if clusterName.backendRefIndex != noBackendRefIndex {
			backendRefIndex = clusterName.backendRefIndex
		}
		backendRef := httpRouteRule.BackendRefs[backendRefIndex]
		setClusterMetadataBackendName(cluster, aigwRoute.Namespace, backendRef.Name, aigwRoute.Name, httpRouteRuleIndex, backendRefIndex)
	}

	// Route upstream egress through a forward proxy when the owning Gateway's GatewayConfig
	// configures one. InferencePool clusters use in-cluster ORIGINAL_DST endpoints, so they are
	// excluded.
	if len(pools) == 0 {
		if err = s.maybeWrapClusterInForwardProxy(ctx, cluster, &aigwRoute); err != nil {
			return fmt.Errorf("failed to configure forward proxy for cluster %s: %w", cluster.Name, err)
		}
	}

	if cluster.TypedExtensionProtocolOptions == nil {
		cluster.TypedExtensionProtocolOptions = make(map[string]*anypb.Any)
	}
	const httpProtocolOptions = "envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
	var po *httpv3.HttpProtocolOptions
	if raw, ok := cluster.TypedExtensionProtocolOptions[httpProtocolOptions]; ok {
		po = &httpv3.HttpProtocolOptions{}
		if err = raw.UnmarshalTo(po); err != nil {
			s.log.Error(err, "failed to unmarshal HttpProtocolOptions", "cluster_name", cluster.Name)
			return fmt.Errorf("failed to unmarshal HttpProtocolOptions: %w", err)
		}
	} else {
		po = &httpv3.HttpProtocolOptions{}
		po.UpstreamProtocolOptions = &httpv3.HttpProtocolOptions_ExplicitHttpConfig_{ExplicitHttpConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig{
			ProtocolConfig: &httpv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
		}}
	}

	// Envoy Gateway rebuilds HttpProtocolOptions from the IR every translation, so a cluster is
	// not expected to arrive carrying our filters. Drop them if one does, so the rebuild wins.
	po.HttpFilters = slices.DeleteFunc(po.HttpFilters, func(f *httpconnectionmanagerv3.HttpFilter) bool {
		return f.GetName() == aiGatewayExtProcName || f.GetName() == aiGatewayHeaderMutationName
	})

	extProcConfig := &extprocv3.ExternalProcessor{}
	extProcConfig.MetadataOptions = &extprocv3.MetadataOptions{
		ReceivingNamespaces: &extprocv3.MetadataOptions_MetadataNamespaces{
			Untyped: []string{aigv1b1.AIGatewayFilterMetadataNamespace},
		},
	}
	if len(metadataForwardingNamespaces) > 0 {
		extProcConfig.MetadataOptions.ForwardingNamespaces = &extprocv3.MetadataOptions_MetadataNamespaces{
			Untyped: metadataForwardingNamespaces,
		}
	}
	extProcConfig.AllowModeOverride = true
	extProcConfig.RequestAttributes = []string{
		internalapi.XDSUpstreamHostMetadataBackendNamePath,
		internalapi.XDSClusterMetadataBackendNamePath,
		internalapi.XDSUpstreamHostMetadataUpstreamHostPath,
		internalapi.XDSRouteMetadataRouteNamePath,
	}
	extProcConfig.ProcessingMode = &extprocv3.ProcessingMode{
		RequestHeaderMode: extprocv3.ProcessingMode_SEND,
		// At the upstream filter, it can access the original body in its memory, so it can perform the translation
		// as well as the authentication at the request headers. Hence, there's no need to send the request body to the extproc.
		RequestBodyMode: extprocv3.ProcessingMode_NONE,
		// Response will be handled at the router filter level so that we could avoid the shenanigans around the retry+the upstream filter.
		ResponseHeaderMode: extprocv3.ProcessingMode_SKIP,
		ResponseBodyMode:   extprocv3.ProcessingMode_NONE,
	}
	extProcConfig.MessageTimeout = durationpb.New(10 * time.Second)
	extProcConfig.GrpcService = &corev3.GrpcService{
		TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
			EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
				ClusterName: extProcUDSClusterName,
			},
		},
		Timeout: durationpb.New(30 * time.Second),
	}
	ecAny, err := toAny(extProcConfig)
	if err != nil {
		s.log.Error(err, "failed to marshal ExternalProcessor to Any", "cluster_name", cluster.Name)
		return fmt.Errorf("failed to marshal ExternalProcessor to Any: %w", err)
	}
	extProcFilter := &httpconnectionmanagerv3.HttpFilter{
		Name:       aiGatewayExtProcName,
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: ecAny},
	}

	hmAny, err := toAny(&header_mutationv3.HeaderMutation{
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
	})
	if err != nil {
		s.log.Error(err, "failed to marshal HeaderMutation to Any", "cluster_name", cluster.Name)
		return fmt.Errorf("failed to marshal HeaderMutation to Any: %w", err)
	}
	headerMutFilter := &httpconnectionmanagerv3.HttpFilter{
		Name:       aiGatewayHeaderMutationName,
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: hmAny},
	}

	if len(po.HttpFilters) > 0 {
		// Insert the ext_proc filter before the last filter since the last one is always the upstream codec filter.
		last := po.HttpFilters[len(po.HttpFilters)-1]
		po.HttpFilters = po.HttpFilters[:len(po.HttpFilters)-1]
		po.HttpFilters = append(po.HttpFilters, extProcFilter, headerMutFilter, last)
	} else {
		po.HttpFilters = append(po.HttpFilters, extProcFilter, headerMutFilter)
		// We always need the upstream_code filter as a last filter.
		upstreamCodec := &httpconnectionmanagerv3.HttpFilter{}
		upstreamCodec.Name = "envoy.filters.http.upstream_codec"
		var ucAny *anypb.Any
		ucAny, err = toAny(&upstream_codecv3.UpstreamCodec{})
		if err != nil {
			s.log.Error(err, "failed to marshal UpstreamCodec to Any", "cluster_name", cluster.Name)
			return fmt.Errorf("failed to marshal UpstreamCodec to Any: %w", err)
		}
		upstreamCodec.ConfigType = &httpconnectionmanagerv3.HttpFilter_TypedConfig{
			TypedConfig: ucAny,
		}
		po.HttpFilters = append(po.HttpFilters, upstreamCodec)
	}
	poAny, err := toAny(po)
	if err != nil {
		s.log.Error(err, "failed to marshal HttpProtocolOptions to Any", "cluster_name", cluster.Name)
		return fmt.Errorf("failed to marshal HttpProtocolOptions to Any: %w", err)
	}
	cluster.TypedExtensionProtocolOptions[httpProtocolOptions] = poAny
	return nil
}

// metadataForwardingNamespacesForSnapshot returns the untyped dynamic metadata namespaces Envoy
// forwards to the external processor on this snapshot's AI traffic. The declaration is the
// Gateway's, not the routes', so it comes from the Gateways owning the snapshot; under
// mergeGateways theirs combine.
func (s *Server) metadataForwardingNamespacesForSnapshot(ctx context.Context, routes []*routev3.RouteConfiguration) ([]string, error) {
	gateways := gatewaysInSnapshot(routes)
	if len(gateways) == 0 {
		routeConfigs := make([]string, 0, len(routes))
		for _, r := range routes {
			routeConfigs = append(routeConfigs, r.GetName())
		}
		s.log.Error(nil, "cannot tell which Gateway this xDS snapshot belongs to; forwarding no dynamic metadata, so credentialOverride.fromDynamicMetadata falls back to the configured credential",
			"route_configs", routeConfigs)
		return nil, nil
	}
	var namespaces []string
	for _, key := range gateways {
		gatewayConfig, err := s.gatewayConfigForGateway(ctx, key.Name, key.Namespace)
		if err != nil {
			return nil, err
		}
		if gatewayConfig == nil || gatewayConfig.Spec.ExtProc == nil {
			continue
		}
		namespaces = append(namespaces, gatewayConfig.Spec.ExtProc.MetadataForwardingNamespaces...)
	}
	slices.Sort(namespaces)
	return slices.Compact(namespaces), nil
}

// gatewaysInSnapshot returns the Gateways owning a snapshot, without duplicates. Envoy Gateway
// records the owning Gateway on virtual hosts only; Listener and RouteConfiguration carry none.
func gatewaysInSnapshot(routes []*routev3.RouteConfiguration) []client.ObjectKey {
	var gateways []client.ObjectKey
	for _, r := range routes {
		for _, vh := range r.GetVirtualHosts() {
			eg, ok := vh.GetMetadata().GetFilterMetadata()[envoyGatewayMetadataNamespace]
			if !ok {
				continue
			}
			resources, ok := eg.GetFields()[envoyGatewayMetadataResourcesKey]
			if !ok {
				continue
			}
			for _, resource := range resources.GetListValue().GetValues() {
				fields := resource.GetStructValue().GetFields()
				if fields["kind"].GetStringValue() != "Gateway" {
					continue
				}
				key := client.ObjectKey{
					Namespace: fields["namespace"].GetStringValue(),
					Name:      fields["name"].GetStringValue(),
				}
				if key.Namespace == "" || key.Name == "" || slices.Contains(gateways, key) {
					continue
				}
				gateways = append(gateways, key)
			}
		}
	}
	return gateways
}

// gatewayConfigForGateway returns the GatewayConfig named by the Gateway's
// "aigateway.envoyproxy.io/gateway-config" annotation. It returns nil when the Gateway, the
// annotation, or the GatewayConfig is absent.
func (s *Server) gatewayConfigForGateway(ctx context.Context, gatewayName, gatewayNamespace string) (*aigv1b1.GatewayConfig, error) {
	var gateway gwapiv1.Gateway
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: gatewayName, Namespace: gatewayNamespace}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get Gateway %s/%s: %w", gatewayNamespace, gatewayName, err)
	}
	configName, ok := gateway.Annotations[gatewayConfigAnnotationKey]
	if !ok || configName == "" {
		return nil, nil
	}
	// The GatewayConfig must be in the same namespace as the Gateway.
	var gatewayConfig aigv1b1.GatewayConfig
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: configName, Namespace: gatewayNamespace}, &gatewayConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get GatewayConfig %s/%s: %w", gatewayNamespace, configName, err)
	}
	return &gatewayConfig, nil
}

// --- Weighted routing across multiple InferencePools ---
//
// A single HTTPRoute rule may list 2+ InferencePool backendRefs with different weights (see
// site/docs/capabilities/inference/httproute-inferencepool.md's "Weighted Routing Across Multiple
// InferencePools" section for the user-facing behavior). Envoy Gateway's translator doesn't know
// how to build a native WeightedCluster route action for custom-Kind (InferencePool) backendRefs,
// so by the time this hook sees the rule, EG has already merged every one of the rule's weighted
// InferencePool backendRefs into a single Envoy cluster and a single, non-weighted route - the
// per-backendRef weights from the k8s HTTPRoute object are lost in translation.
//
// The functions below (splitWeightedInferencePoolRoutes and friends) undo that merge: they recover
// the real weights from the owning HTTPRoute rule and rebuild a genuine Envoy-native weighted
// route, mirroring istio/istio#61601's approach of overriding the ext_proc filter per weighted
// destination. Concretely, for a merged route naming N pools, M of which have a non-zero weight:
//  1. The one EG-merged cluster is cloned M times (buildInferencePoolWeightedCluster), one clone
//     per pool, each with its own metadata and its own upstream protocol options - removing the
//     merged cluster's restriction that every pool on a rule share one appProtocol.
//  2. The route's metadata is narrowed to the M pools, so patchVirtualHostWithInferencePool
//     disables the EPP filter of every zero-weight pool at the route level.
//  3. With M == 1, the route's Cluster specifier simply points at that pool's clone. With M >= 2,
//     it is replaced by a WeightedClusters specifier listing all M clones with their real integer
//     weights, so Envoy itself performs the weighted pick at request time.
//  4. Each WeightedCluster_ClusterWeight carries its own TypedPerFilterConfig, disabling every
//     sibling pool's EPP ext_proc filter and leaving its own pool's filter enabled by omitting an
//     entry for it (ExtProcPerRoute's "disabled" oneof is proto-constrained to only ever be true -
//     Envoy rejects Disabled: false - so there is no override that means "force-enabled"). Envoy
//     resolves a ClusterWeight's TypedPerFilterConfig as more specific than the Route's once it
//     has picked a weighted cluster for a request - so whichever pool Envoy's weighted pick lands
//     on, only that pool's own endpoint picker scores/forwards the request.

// weightedInferencePool pairs an InferencePool with the real weight its backendRef carries on the
// HTTPRoute rule that referenced it.
type weightedInferencePool struct {
	pool            *gwaiev1.InferencePool
	weight          int32
	backendRefIndex int
}

// inferencePoolKey returns the namespace/name identity of an InferencePool.
func inferencePoolKey(namespace, name string) string {
	return namespace + "/" + name
}

// splitWeightedInferencePoolRoutes walks every route in every VirtualHost of every RouteConfiguration
// and rewrites the ones EG merged from a 2+-InferencePool weighted rule (identified by
// getInferencePoolsByMetadata(route.Metadata) returning 2+ pools - PostRouteModify is what stamped
// that metadata onto the merged route in the first place) into routes targeting per-pool clusters.
// See the "Weighted routing across multiple InferencePools" overview comment above
// weightedInferencePool for the full mechanism.
//
// Routes are replaced in place (vh.Routes[i] = newRoute); no routes are added or removed. Any
// route this can't rewrite (owning HTTPRoute or rule not found, k8s error, no InferencePool
// backendRef left with non-zero weight, missing cluster) fails the whole pass: leaving the merged
// route as is would run every pool's EPP on every request, with whichever runs last deciding the
// endpoint regardless of the weights.
//
// Every per-pool cluster clone created while splitting is recorded in addedByName/addedOrder
// (deduped by name: the same merged cluster can be referenced by more than one route, one per
// match and per VirtualHost, and must clone to the exact same cluster names each time) and every
// merged cluster they replace is recorded in removed. Once every route has been processed, the
// returned cluster list is rebuilt from `clusters` with the removed entries filtered out and the
// added clones appended, preserving the original order for the untouched clusters so the xDS
// output doesn't churn between reconciliations.
func (s *Server) splitWeightedInferencePoolRoutes(ctx context.Context, routeConfigs []*routev3.RouteConfiguration, clusters []*clusterv3.Cluster) ([]*clusterv3.Cluster, error) {
	cache := make(map[client.ObjectKey]*gwapiv1.HTTPRoute)
	clusterByName := make(map[string]*clusterv3.Cluster, len(clusters))
	for _, c := range clusters {
		clusterByName[c.Name] = c
	}
	addedByName := make(map[string]*clusterv3.Cluster)
	var addedOrder []*clusterv3.Cluster
	removed := make(map[string]struct{})

	for _, rc := range routeConfigs {
		for _, vh := range rc.VirtualHosts {
			for i, route := range vh.Routes {
				pools := getInferencePoolsByMetadata(route.Metadata)
				if len(pools) < 2 {
					continue
				}
				newRoute, err := s.splitInferencePoolRoute(ctx, cache, route, pools, clusterByName, addedByName, &addedOrder, removed)
				if err != nil {
					return nil, fmt.Errorf("failed to split weighted InferencePool route %q: %w", route.Name, err)
				}
				vh.Routes[i] = newRoute
			}
		}
	}

	if len(addedOrder) == 0 && len(removed) == 0 {
		// Nothing was split: avoid needlessly reallocating the caller's cluster slice.
		return clusters, nil
	}
	result := make([]*clusterv3.Cluster, 0, len(clusters)+len(addedOrder))
	for _, c := range clusters {
		if _, gone := removed[c.Name]; gone {
			continue
		}
		result = append(result, c)
	}
	result = append(result, addedOrder...)
	return result, nil
}

// splitInferencePoolRoute handles a single route known to carry 2+ InferencePools in its metadata:
// it looks up the HTTPRoute rule that EG's translator produced route from (via the rule index in
// the route name), matches pools back to their real backendRef weights on that rule
// (matchInferencePoolWeights - this is the step that recovers what EG's merge discarded), and hands
// the result to buildWeightedInferencePoolRouteAction to actually rewrite the route/clusters.
func (s *Server) splitInferencePoolRoute(
	ctx context.Context,
	cache map[client.ObjectKey]*gwapiv1.HTTPRoute,
	route *routev3.Route,
	pools []*gwaiev1.InferencePool,
	clusterByName map[string]*clusterv3.Cluster,
	addedByName map[string]*clusterv3.Cluster,
	addedOrder *[]*clusterv3.Cluster,
	removed map[string]struct{},
) (*routev3.Route, error) {
	key, ruleIndex, ok := parseHTTPRouteRuleFromRouteName(route.Name)
	if !ok {
		return nil, fmt.Errorf("could not determine owning HTTPRoute rule from route name")
	}
	httpRoute, err := s.retrieveAndCacheHTTPRoute(ctx, cache, key)
	if err != nil {
		return nil, err
	}
	if httpRoute == nil {
		return nil, fmt.Errorf("HTTPRoute %s/%s not found", key.Namespace, key.Name)
	}
	// The HTTPRoute may have changed since this route was generated, so check the bounds.
	if ruleIndex >= len(httpRoute.Spec.Rules) {
		return nil, fmt.Errorf("rule index %d out of range for HTTPRoute %s/%s", ruleIndex, key.Namespace, key.Name)
	}
	weighted := matchInferencePoolWeights(httpRoute.Namespace, &httpRoute.Spec.Rules[ruleIndex], pools)
	// The pools decoded from route metadata don't carry spec.appProtocol, which each per-pool
	// cluster needs to pick its upstream protocol, so read it from the InferencePool itself.
	for _, wp := range weighted {
		var pool gwaiev1.InferencePool
		if err = s.k8sClient.Get(ctx, client.ObjectKey{Namespace: wp.pool.Namespace, Name: wp.pool.Name}, &pool); err != nil {
			return nil, fmt.Errorf("failed to get InferencePool %s/%s: %w", wp.pool.Namespace, wp.pool.Name, err)
		}
		wp.pool.Spec.AppProtocol = pool.Spec.AppProtocol
	}
	// When the rule also has non-InferencePool backends, Envoy Gateway emits a WeightedClusters
	// route action and, since custom backends have no endpoints, leaves the InferencePool settings
	// out of it: their merged cluster is still generated per setting, named after the backendRef
	// index, but nothing references it. Give the rewrite those names to find it by.
	destName := fmt.Sprintf("httproute/%s/%s/rule/%d", key.Namespace, key.Name, ruleIndex)
	var perSettingClusterNames []string
	for i := range httpRoute.Spec.Rules[ruleIndex].BackendRefs {
		if isInferencePoolBackendRef(&httpRoute.Spec.Rules[ruleIndex].BackendRefs[i]) {
			perSettingClusterNames = append(perSettingClusterNames, fmt.Sprintf("%s/backend/%d", destName, i))
		}
	}
	return buildWeightedInferencePoolRouteAction(route, weighted, key.Name, ruleIndex, perSettingClusterNames, clusterByName, addedByName, addedOrder, removed)
}

// parseHTTPRouteRuleFromRouteName extracts the owning HTTPRoute and rule index from a route name
// generated by Envoy Gateway, formatted as "httproute/<namespace>/<name>/rule/<index>/match/<...>".
func parseHTTPRouteRuleFromRouteName(name string) (key client.ObjectKey, ruleIndex int, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) < 5 || parts[0] != "httproute" || parts[3] != "rule" || parts[1] == "" || parts[2] == "" {
		return client.ObjectKey{}, 0, false
	}
	ruleIndex, err := strconv.Atoi(parts[4])
	if err != nil || ruleIndex < 0 {
		return client.ObjectKey{}, 0, false
	}
	return client.ObjectKey{Namespace: parts[1], Name: parts[2]}, ruleIndex, true
}

// retrieveAndCacheHTTPRoute returns the HTTPRoute for the key and caches the result, so one
// translation pass hits the API server at most once per HTTPRoute.
func (s *Server) retrieveAndCacheHTTPRoute(ctx context.Context, cache map[client.ObjectKey]*gwapiv1.HTTPRoute, key client.ObjectKey) (*gwapiv1.HTTPRoute, error) {
	if cached, ok := cache[key]; ok {
		return cached, nil
	}
	var httpRoute gwapiv1.HTTPRoute
	if err := s.k8sClient.Get(ctx, key, &httpRoute); err != nil {
		if apierrors.IsNotFound(err) {
			cache[key] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get HTTPRoute %s/%s: %w", key.Namespace, key.Name, err)
	}
	cache[key] = &httpRoute
	return &httpRoute, nil
}

// isInferencePoolBackendRef reports whether the backendRef targets an InferencePool.
func isInferencePoolBackendRef(ref *gwapiv1.HTTPBackendRef) bool {
	return ref.Group != nil && string(*ref.Group) == gwaiev1.GroupName &&
		ref.Kind != nil && string(*ref.Kind) == "InferencePool"
}

// matchInferencePoolWeights pairs each of pools with the real weight of its backendRefs on rule, in
// the same order as `pools`. backendRefs are matched by namespace/name, with the namespace
// defaulting to routeNamespace. Gateway API computes traffic shares over the full BackendRefs
// list, so the weights of backendRefs listing the same pool more than once are summed, and each
// pool is returned at most once. A pool whose weights sum to zero receives no traffic, so it is
// dropped and the returned slice can be shorter than `pools`.
func matchInferencePoolWeights(routeNamespace string, rule *gwapiv1.HTTPRouteRule, pools []*gwaiev1.InferencePool) []weightedInferencePool {
	weightByKey := make(map[string]int32, len(rule.BackendRefs))
	firstRefIndexByKey := make(map[string]int, len(rule.BackendRefs))
	for i := range rule.BackendRefs {
		ref := &rule.BackendRefs[i]
		if !isInferencePoolBackendRef(ref) {
			continue
		}
		weight := int32(1)
		if ref.Weight != nil {
			weight = *ref.Weight
		}
		if weight == 0 {
			continue
		}
		namespace := routeNamespace
		if ref.Namespace != nil && *ref.Namespace != "" {
			namespace = string(*ref.Namespace)
		}
		key := inferencePoolKey(namespace, string(ref.Name))
		weightByKey[key] += weight
		if _, ok := firstRefIndexByKey[key]; !ok {
			firstRefIndexByKey[key] = i
		}
	}

	result := make([]weightedInferencePool, 0, len(pools))
	for _, pool := range pools {
		key := inferencePoolKey(pool.Namespace, pool.Name)
		if w, ok := weightByKey[key]; ok {
			result = append(result, weightedInferencePool{pool: pool, weight: w, backendRefIndex: firstRefIndexByKey[key]})
			// Drop the key so a pool listed more than once in the metadata is returned once.
			delete(weightByKey, key)
		}
	}
	return result
}

// buildWeightedInferencePoolRouteAction clones route and rewrites the clone to send traffic to
// weighted's pools according to their real weights. route itself is never mutated - route
// objects can be shared/referenced elsewhere in the snapshot being built, so every rewrite here
// (and in buildInferencePoolWeightedCluster) works on a defensive proto.Clone.
//
//  1. The EG-merged InferencePool cluster is located (findMergedInferencePoolClusters) and the
//     first one used as the template for step 2. Any other backend the route's WeightedClusters already lists
//     (e.g. a Service on the same rule) is kept as is.
//  2. For each weighted pool, buildInferencePoolWeightedCluster clones that template into a
//     same-shaped cluster named "<templateClusterName>/inferencepool/<namespace>/<name>", scoped
//     to that pool alone (own metadata, own upstream protocol options). Clones are cached in
//     addedByName by that deterministic name so a rule reachable from multiple routes produces
//     exactly one clone per pool.
//  3. The clone's metadata is narrowed to the weighted pools, so patchVirtualHostWithInferencePool
//     - which runs after this and disables every pool outside a route's metadata - disables the
//     EPP filter of every zero-weight pool at the route level.
//  4. With a single weighted pool and no other backend, the clone's Cluster specifier points at
//     that pool's cluster. Otherwise it is switched to a WeightedClusters specifier listing one
//     ClusterWeight per pool, with Weight set to that pool's raw integer weight (the same raw
//     backendRef weights EG uses for the other backends), and a TypedPerFilterConfig disabling
//     every *sibling* pool's EPP ext_proc filter (the ClusterWeight's own pool gets no entry,
//     since ExtProcPerRoute has no valid "force-enabled" override). The kept non-pool
//     ClusterWeights get every pool's EPP filter disabled.
//  5. The merged clusters' names are recorded in removed so splitWeightedInferencePoolRoutes
//     drops them from the final cluster list once every route has been processed.
func buildWeightedInferencePoolRouteAction(
	route *routev3.Route,
	weighted []weightedInferencePool,
	httpRouteName string,
	ruleIndex int,
	perSettingClusterNames []string,
	clusterByName map[string]*clusterv3.Cluster,
	addedByName map[string]*clusterv3.Cluster,
	addedOrder *[]*clusterv3.Cluster,
	removed map[string]struct{},
) (*routev3.Route, error) {
	clone, ok := proto.Clone(route).(*routev3.Route)
	if !ok {
		return nil, fmt.Errorf("failed to clone route %q", route.Name)
	}
	routeAction := clone.GetRoute()
	if routeAction == nil {
		return nil, fmt.Errorf("route %q has no RouteAction to weight", route.Name)
	}
	mergedNames, otherClusterWeights, err := findMergedInferencePoolClusters(routeAction, perSettingClusterNames, clusterByName)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", route.Name, err)
	}
	template := clusterByName[mergedNames[0]]

	cloneNames := make([]string, 0, len(weighted))
	weightedPools := make([]*gwaiev1.InferencePool, 0, len(weighted))
	for _, wp := range weighted {
		cloneName := fmt.Sprintf("%s/inferencepool/%s/%s", template.Name, wp.pool.Namespace, wp.pool.Name)
		if _, ok = addedByName[cloneName]; !ok {
			var poolCluster *clusterv3.Cluster
			poolCluster, err = buildInferencePoolWeightedCluster(template, cloneName, wp.pool, httpRouteName, ruleIndex, wp.backendRefIndex)
			if err != nil {
				return nil, err
			}
			addedByName[cloneName] = poolCluster
			*addedOrder = append(*addedOrder, poolCluster)
		}
		cloneNames = append(cloneNames, cloneName)
		weightedPools = append(weightedPools, wp.pool)
	}

	// Narrow the route metadata to the pools that actually receive traffic. buildEPPMetadata
	// replaces the pool list, so the zero-weight pools are dropped.
	buildEPPMetadataForRoute(clone, weightedPools)
	for _, name := range mergedNames {
		removed[name] = struct{}{}
	}
	if len(weighted) == 0 {
		if len(otherClusterWeights) == 0 {
			return nil, fmt.Errorf("route %q has no non-zero-weight InferencePool backendRef and no other backend to keep", route.Name)
		}
		wc := routeAction.GetWeightedClusters()
		if wc == nil {
			wc = &routev3.WeightedCluster{}
		}
		wc.Clusters = otherClusterWeights
		routeAction.ClusterSpecifier = &routev3.RouteAction_WeightedClusters{WeightedClusters: wc}
		return clone, nil
	}

	if len(weighted) == 1 && len(otherClusterWeights) == 0 {
		routeAction.ClusterSpecifier = &routev3.RouteAction_Cluster{Cluster: cloneNames[0]}
		return clone, nil
	}

	disabledOverride, err := toAny(&extprocv3.ExtProcPerRoute{Override: &extprocv3.ExtProcPerRoute_Disabled{Disabled: true}})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal disabled ExtProcPerRoute override: %w", err)
	}
	clusterWeights := make([]*routev3.WeightedCluster_ClusterWeight, 0, len(otherClusterWeights)+len(weighted))
	for _, cw := range otherClusterWeights {
		// A non-InferencePool backend: no endpoint picker may run for it.
		if cw.TypedPerFilterConfig == nil {
			cw.TypedPerFilterConfig = make(map[string]*anypb.Any, len(weighted))
		}
		for _, wp := range weighted {
			cw.TypedPerFilterConfig[httpFilterNameForInferencePool(wp.pool)] = disabledOverride
		}
		clusterWeights = append(clusterWeights, cw)
	}
	for i, wp := range weighted {
		filterConfig := make(map[string]*anypb.Any, len(weighted)-1)
		for j, other := range weighted {
			if i == j {
				// This ClusterWeight's own pool: leave its filter enabled by omitting an entry.
				continue
			}
			filterConfig[httpFilterNameForInferencePool(other.pool)] = disabledOverride
		}
		clusterWeights = append(clusterWeights, &routev3.WeightedCluster_ClusterWeight{
			Name:                 cloneNames[i],
			Weight:               wrapperspb.UInt32(uint32(wp.weight)), // #nosec G115 -- backendRef weights are non-negative int32.
			TypedPerFilterConfig: filterConfig,
		})
	}
	// Reuse EG's WeightedCluster when there is one, so its other settings are kept.
	wc := routeAction.GetWeightedClusters()
	if wc == nil {
		wc = &routev3.WeightedCluster{}
	}
	wc.Clusters = clusterWeights
	routeAction.ClusterSpecifier = &routev3.RouteAction_WeightedClusters{WeightedClusters: wc}
	return clone, nil
}

// findMergedInferencePoolClusters locates the EG-merged InferencePool cluster(s) behind
// routeAction, returning their names (never empty on success) and the ClusterWeights of every
// other backend routeAction already routes to, which are to be kept as is:
//   - A Cluster specifier names the single merged cluster, the route's only backend.
//   - A WeightedClusters specifier means EG generated one cluster per backendRef, e.g. because the
//     rule mixes InferencePools with other backends. The merged clusters are then the ones named
//     after the rule's InferencePool backendRefs (perSettingClusterNames). EG normally doesn't list
//     them in the WeightedClusters at all, since custom backends have no endpoints, but they are
//     dropped from it if it does. Every other entry, including EG's "invalid-backend-cluster", is
//     kept. Cluster metadata can't be used to tell them apart: EG hands the route's extension
//     resources to PostClusterModify for every per-backendRef cluster of the rule.
func findMergedInferencePoolClusters(
	routeAction *routev3.RouteAction,
	perSettingClusterNames []string,
	clusterByName map[string]*clusterv3.Cluster,
) (mergedNames []string, others []*routev3.WeightedCluster_ClusterWeight, err error) {
	if name := routeAction.GetCluster(); name != "" {
		if _, ok := clusterByName[name]; !ok {
			return nil, nil, fmt.Errorf("cluster %q referenced by route not found", name)
		}
		return []string{name}, nil, nil
	}
	wc := routeAction.GetWeightedClusters()
	if wc == nil {
		return nil, nil, fmt.Errorf("route has neither a cluster nor weighted clusters to split")
	}
	merged := make(map[string]struct{}, len(perSettingClusterNames))
	for _, name := range perSettingClusterNames {
		if _, ok := clusterByName[name]; ok {
			merged[name] = struct{}{}
			mergedNames = append(mergedNames, name)
		}
	}
	if len(mergedNames) == 0 {
		return nil, nil, fmt.Errorf("none of the InferencePool clusters %v referenced by route found", perSettingClusterNames)
	}
	for _, cw := range wc.Clusters {
		if _, ok := merged[cw.Name]; !ok {
			others = append(others, cw)
		}
	}
	return mergedNames, others, nil
}

// buildInferencePoolWeightedCluster clones original - the single Envoy-Gateway-merged
// InferencePool cluster, already configured by PostClusterModify/handleInferencePoolCluster as
// ORIGINAL_DST with header-based load balancing (left as-is, since it's identical for every
// pool) - into a same-shaped cluster named name that is dedicated to pool alone:
//   - The InferencePool entry of the metadata (buildEPPMetadataForCluster) is narrowed to just
//     pool. Every other metadata entry is kept as is.
//   - The upstream protocol of the HttpProtocolOptions is recomputed from pool's own
//     spec.appProtocol (httpProtocolOptionsForInferencePoolBackend), instead of inheriting
//     whatever the merged cluster derived from only the first of its pools, so each pool gets its
//     own correct HTTP/1.1 vs. h2c upstream protocol regardless of what its siblings on the same
//     rule use. Every other field, such as upstream HTTP filters, is kept as is.
func buildInferencePoolWeightedCluster(original *clusterv3.Cluster, name string, pool *gwaiev1.InferencePool, httpRouteName string, routeRuleIndex, backendRefIndex int) (*clusterv3.Cluster, error) {
	clone, ok := proto.Clone(original).(*clusterv3.Cluster)
	if !ok {
		return nil, fmt.Errorf("failed to clone cluster %q", original.Name)
	}
	clone.Name = name
	buildEPPMetadataForCluster(clone, []*gwaiev1.InferencePool{pool})
	setClusterMetadataBackendName(clone, pool.Namespace, pool.Name, httpRouteName, routeRuleIndex, backendRefIndex)
	protocolOptions, err := httpProtocolOptionsForInferencePoolBackend(pool)
	if err != nil {
		return nil, fmt.Errorf("failed to build protocol options for InferencePool %s/%s: %w", pool.Namespace, pool.Name, err)
	}
	const httpProtocolOptionsKey = "envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
	existing, ok := clone.TypedExtensionProtocolOptions[httpProtocolOptionsKey]
	if !ok {
		if clone.TypedExtensionProtocolOptions == nil {
			clone.TypedExtensionProtocolOptions = make(map[string]*anypb.Any, 1)
		}
		clone.TypedExtensionProtocolOptions[httpProtocolOptionsKey] = protocolOptions[httpProtocolOptionsKey]
		return clone, nil
	}
	// Only swap the upstream protocol, keeping e.g. the upstream filters maybeModifyCluster added.
	po := &httpv3.HttpProtocolOptions{}
	if err = existing.UnmarshalTo(po); err != nil {
		return nil, fmt.Errorf("failed to unmarshal HttpProtocolOptions of cluster %q: %w", original.Name, err)
	}
	poolPO := &httpv3.HttpProtocolOptions{}
	if err = protocolOptions[httpProtocolOptionsKey].UnmarshalTo(poolPO); err != nil {
		return nil, fmt.Errorf("failed to unmarshal HttpProtocolOptions for InferencePool %s/%s: %w", pool.Namespace, pool.Name, err)
	}
	po.UpstreamProtocolOptions = poolPO.UpstreamProtocolOptions
	if clone.TypedExtensionProtocolOptions[httpProtocolOptionsKey], err = toAny(po); err != nil {
		return nil, fmt.Errorf("failed to marshal HttpProtocolOptions of cluster %q: %w", name, err)
	}
	return clone, nil
}

// maybeModifyListenerAndRoutes modifies listeners and routes to support InferencePool backends.
// This function performs the following operations:
// 1. Identifies listeners and routes that use InferencePool backends
// 2. Adds endpoint picker (EPP) external processor filters to relevant listeners
// 3. Configures per-route filters to disable EPP processing for non-InferencePool routes
// This ensures that only routes targeting InferencePool backends go through the endpoint picker.
func (s *Server) maybeModifyListenerAndRoutes(listeners []*listenerv3.Listener, routes []*routev3.RouteConfiguration) error {
	listenerNameToRouteNames := make(map[string][]string)
	listenerNameToListener := make(map[string]*listenerv3.Listener)
	for _, listener := range listeners {
		// Skips two special listeners used for stats and readiness probes created by Envoy Gateway.
		// https://github.com/envoyproxy/gateway/blob/e7c0e3430f20fd104454dc62f51b5352cde3b26a/internal/xds/bootstrap/bootstrap.yaml.tpl#L89
		// https://github.com/envoyproxy/gateway/blob/e7c0e3430f20fd104454dc62f51b5352cde3b26a/internal/xds/translator/listener_ready.go#L76
		//
		// This assumes that EG keeps the same naming convention, but even if not, the latest EG e2e test will catch any issue.
		//
		// The "normal" user-created listeners will have "${gateway obj namespace}-${gateway obj name}-..." format.
		if strings.HasPrefix(listener.Name, "envoy-gateway-proxy-stats-") || strings.HasPrefix(listener.Name, "envoy-gateway-proxy-ready-") {
			continue
		}
		listenerNameToRouteNames[listener.Name] = findListenerRouteConfigs(listener)
		listenerNameToListener[listener.Name] = listener
	}

	// inferencePoolRoutes builds a matrix of route configs and the inference pools they use.
	routeNameToRoute := make(map[string]*routev3.RouteConfiguration)
	routeNameToVHRouteNameToInferencePools := make(map[string]map[string][]*gwaiev1.InferencePool)
	for _, routeCfg := range routes {
		routeNameToRoute[routeCfg.Name] = routeCfg
		for _, vh := range routeCfg.VirtualHosts {
			for _, route := range vh.Routes {
				if pools := getInferencePoolsByMetadata(route.Metadata); len(pools) > 0 {
					if routeNameToVHRouteNameToInferencePools[routeCfg.Name] == nil {
						routeNameToVHRouteNameToInferencePools[routeCfg.Name] = make(map[string][]*gwaiev1.InferencePool)
					}
					routeNameToVHRouteNameToInferencePools[routeCfg.Name][route.Name] = pools
				}
			}
		}
	}

	// listenerToInferencePools builds a matrix of listeners and the inference pools they use.
	// routeNameToVHRouteNameToInferencePool is keyed per route match, so a pool reached by
	// several matches appears several times. A pool needs exactly one ext proc filter per
	// listener, so deduplicate here.
	listenerToInferencePools := make(map[string][]*gwaiev1.InferencePool)
	for listener, routeCfgNames := range listenerNameToRouteNames {
		seen := make(map[string]struct{})
		for _, name := range routeCfgNames {
			if routeNameToRoute[name] == nil {
				continue
			}
			if routeNameToVHRouteNameToInferencePools[name] == nil {
				continue
			}
			for _, pools := range routeNameToVHRouteNameToInferencePools[name] {
				for _, pool := range pools {
					key := httpFilterNameForInferencePool(pool)
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					listenerToInferencePools[listener] = append(listenerToInferencePools[listener], pool)
				}
			}
		}
	}

	// patch the listeners, the route configs and the virtual hosts with inference pool filters.
	for listener, pools := range listenerToInferencePools {
		s.log.Info("patching listener with inference pool filters", "listener", listener)
		s.patchListenerWithInferencePoolFilters(listenerNameToListener[listener], pools)
		routeCfgNames := listenerNameToRouteNames[listener]
		for _, routeCfgName := range routeCfgNames {
			routeCfg := routeNameToRoute[routeCfgName]
			if routeCfg == nil {
				continue
			}
			for _, vh := range routeCfg.VirtualHosts {
				s.log.Info("patching virtual host with inference pool filters", "listener", listener, "virtual_host", vh.Name)
				if err := s.patchVirtualHostWithInferencePool(vh, pools); err != nil {
					return fmt.Errorf("failed to patch virtual host %s in route config %s: %w", vh.Name, routeCfg.Name, err)
				}
			}
		}
	}

	for _, ln := range listeners {
		var enabled bool
		for _, name := range listenerNameToRouteNames[ln.Name] {
			routeCfg := routeNameToRoute[name]
			if routeCfg == nil {
				s.log.Info("skipping patching of non-existent route config", "route_config", name)
				continue
			}
			_enabled, err := s.enableRouterLevelAIGatewayExtProcOnRoute(routeCfg)
			if err != nil {
				return fmt.Errorf("failed to enable router level AI Gateway extproc on route config %s: %w", name, err)
			}
			enabled = enabled || _enabled
		}
		if enabled {
			s.log.Info("inserting AI Gateway extproc filter into listener", "listener", ln.Name)
			if err := s.insertRouterLevelAIGatewayExtProc(ln); err != nil {
				return fmt.Errorf("failed to insert AI Gateway extproc filter into listener %s: %w", ln.Name, err)
			}
		}
	}
	return nil
}

// patchListenerWithInferencePoolFilters adds the necessary HTTP filters to the listener to support InferencePool backends.
func (s *Server) patchListenerWithInferencePoolFilters(listener *listenerv3.Listener, inferencePools []*gwaiev1.InferencePool) {
	// First, get the filter chains from the listener.
	filterChains := listener.GetFilterChains()
	defaultFC := listener.DefaultFilterChain
	if defaultFC != nil {
		filterChains = append(filterChains, defaultFC)
	}
	// Go over all of the chains, and add the endpoint picker external processor filters.
	for _, currChain := range filterChains {
		httpConManager, hcmIndex, err := findHCM(currChain)
		if err != nil {
			s.log.Error(err, "failed to find an HCM in the current chain")
			continue
		}
		var poolFilters []*httpconnectionmanagerv3.HttpFilter
		// poolFilters is spliced into httpConManager.HttpFilters only after this loop, so the
		// search below cannot see what the loop already added. Track those names separately.
		added := make(map[string]struct{})
		for _, pool := range inferencePools {
			filterName := httpFilterNameForInferencePool(pool)
			if _, ok := added[filterName]; ok {
				continue
			}
			_, baIndex, searchErr := searchInferencePoolInFilterChain(pool, httpConManager.HttpFilters)
			if searchErr != nil {
				s.log.Error(searchErr, "failed to find an inference pool ext proc filter")
				continue
			}
			if baIndex == -1 {
				s.log.Info("adding inference pool ext proc filter", "pool", pool.Name)
				var eppExtProc *httpconnectionmanagerv3.HttpFilter
				eppExtProc, err = buildInferencePoolHTTPFilter(pool)
				if err != nil {
					s.log.Error(err, "failed to build inference pool ext proc filter", "pool", pool.Name)
					continue
				}
				added[filterName] = struct{}{}
				poolFilters = append(poolFilters, eppExtProc)
			}
		}
		if len(poolFilters) != 0 {
			length := len(httpConManager.HttpFilters)
			router := httpConManager.HttpFilters[length-1]
			httpConManager.HttpFilters = httpConManager.HttpFilters[:length-1]
			httpConManager.HttpFilters = append(httpConManager.HttpFilters, poolFilters...)
			httpConManager.HttpFilters = append(httpConManager.HttpFilters, router)
		}

		// Write the updated HCM back to the filter chain.
		hcmAny, err := toAny(httpConManager)
		if err != nil {
			s.log.Error(err, "failed to marshal updated HCM to Any")
			continue
		}
		currChain.Filters[hcmIndex].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny}
	}
}

// patchVirtualHostWithInferencePool adds the necessary per-route configuration to disable.
func (s *Server) patchVirtualHostWithInferencePool(vh *routev3.VirtualHost, inferencePools []*gwaiev1.InferencePool) error {
	inferenceMatrix := make(map[string]*gwaiev1.InferencePool)
	for _, pool := range inferencePools {
		inferenceMatrix[httpFilterNameForInferencePool(pool)] = pool
	}
	for _, route := range vh.Routes {
		override := &extprocv3.ExtProcPerRoute{
			Override: &extprocv3.ExtProcPerRoute_Disabled{
				Disabled: true,
			},
		}
		overrideAny, err := toAny(override)
		if err != nil {
			return fmt.Errorf("failed to marshal ExtProcPerRoute to Any: %w", err)
		}
		routePools := getInferencePoolsByMetadata(route.Metadata)
		routePoolKeys := make(map[string]struct{}, len(routePools))
		for _, pool := range routePools {
			routePoolKeys[httpFilterNameForInferencePool(pool)] = struct{}{}
		}
		for key, pool := range inferenceMatrix {
			if _, ok := routePoolKeys[key]; ok {
				// This route is one of the pools using this filter: leave it enabled.
				continue
			}
			s.log.Info("disabling inference pool filter", "route", route.Name, "filter", key, "pool", pool.Name)
			if route.TypedPerFilterConfig == nil {
				route.TypedPerFilterConfig = make(map[string]*anypb.Any)
			}
			route.TypedPerFilterConfig[key] = overrideAny
		}
	}
	return nil
}

// enableRouterLevelAIGatewayExtProcOnRoute checks if the extproc filter should be enabled for routes
// that are generated by AIGateway. It modifies the route configuration to enable the extproc filter
// for those routes. It returns true if any route was modified.
func (s *Server) enableRouterLevelAIGatewayExtProcOnRoute(routeConfig *routev3.RouteConfiguration) (bool, error) {
	enabled := false
	fcAny, err := toAny(&routev3.FilterConfig{
		Config: &anypb.Any{},
	})
	if err != nil {
		return false, fmt.Errorf("failed to marshal FilterConfig to Any: %w", err)
	}

	for _, vh := range routeConfig.VirtualHosts {
		for _, route := range vh.Routes {
			aiGatewayGenerated := s.isRouteGeneratedByAIGateway(route)
			if aiGatewayGenerated {
				enabled = true
				if route.TypedPerFilterConfig == nil {
					route.TypedPerFilterConfig = make(map[string]*anypb.Any)
				}
				// Enable the extproc filter for this route.
				route.TypedPerFilterConfig[aiGatewayExtProcName] = fcAny

				routeName := routeNameFromEnvoyGatewayMetadata(route)
				if routeName == "" {
					routeName = routeNameFromRouteConfigName(routeConfig.Name)
				}
				if routeName == "" {
					routeName = route.Name
				}
				ensureRouteInternalMetadata(route).Fields[internalapi.InternalMetadataRouteNameKey] = structpb.NewStringValue(routeName)
			}
		}
	}
	return enabled, nil
}

// insertRouterLevelAIGatewayExtProcExtProc inserts the AI Gateway external processor filter into the listener's filter chains.
func (s *Server) insertRouterLevelAIGatewayExtProc(listener *listenerv3.Listener) error {
	// First, get the filter chains from the listener.
	filterChains := listener.GetFilterChains()
	defaultFC := listener.DefaultFilterChain
	if defaultFC != nil {
		filterChains = append(filterChains, defaultFC)
	}
	// Go over all of the chains, and add the endpoint picker external processor filters.
	for _, currChain := range filterChains {
		httpConManager, hcmIndex, err := findHCM(currChain)
		if hcmIndex == -1 {
			// Chains without an HCM (e.g. TCP/UDP routes) cannot host the ext_proc filter.
			s.log.Error(err, "skipping filter chain without an HTTP connection manager", "listener", listener.Name)
			continue
		}
		// Check if the extproc filter is already present.
		if !shouldAIGatewayExtProcBeInserted(httpConManager.HttpFilters) {
			return nil // The filter is already present, nothing to do.
		}
		epAny, err := toAny(&extprocv3.ExternalProcessor{
			GrpcService: &corev3.GrpcService{
				TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
					EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
						ClusterName: extProcUDSClusterName,
					},
				},
			},
			MetadataOptions: &extprocv3.MetadataOptions{
				ReceivingNamespaces: &extprocv3.MetadataOptions_MetadataNamespaces{
					Untyped: []string{aigv1b1.AIGatewayFilterMetadataNamespace},
				},
			},
			ProcessingMode: &extprocv3.ProcessingMode{
				RequestHeaderMode:   extprocv3.ProcessingMode_SEND,
				RequestBodyMode:     extprocv3.ProcessingMode_BUFFERED,
				RequestTrailerMode:  extprocv3.ProcessingMode_SKIP,
				ResponseHeaderMode:  extprocv3.ProcessingMode_SEND,
				ResponseBodyMode:    extprocv3.ProcessingMode_BUFFERED,
				ResponseTrailerMode: extprocv3.ProcessingMode_SKIP,
			},
			MessageTimeout:    durationpb.New(10 * time.Second),
			FailureModeAllow:  false,
			AllowModeOverride: true,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal ExternalProcessor to Any: %w", err)
		}

		extProcFilter := &httpconnectionmanagerv3.HttpFilter{
			Name:       aiGatewayExtProcName,
			Disabled:   true, // Disable the filter by default, it will be enabled per route where the route is generated by AIGateway.
			ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: epAny},
		}

		// Insert the AI Gateway extproc filter as the first extproc filter.
		if err = insertAIGatewayExtProcFilter(httpConManager, extProcFilter); err != nil {
			return fmt.Errorf("failed to insert AI Gateway extproc filter: %w", err)
		}
		// Match the :scheme pseudo-header to the upstream transport protocol.
		httpConManager.SchemeHeaderTransformation = &corev3.SchemeHeaderTransformation{
			MatchUpstream: true,
		}
		hcAny, err := toAny(httpConManager)
		if err != nil {
			return fmt.Errorf("failed to marshal updated HCM to Any: %w", err)
		}
		// Write the updated HCM back to the filter chain.
		currChain.Filters[hcmIndex].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: hcAny}
	}
	return nil
}

func (s *Server) isRouteGeneratedByAIGateway(route *routev3.Route) bool {
	// The route metadata should look like this:
	//
	//	filterMetadata:
	//	 envoy-gateway:
	//	   resources:
	//	   - annotations:
	//	       ai-gateway-generated: true
	//      ....
	//
	// where "ai-gateway-generated" is the annotation that indicates that the route was generated by AIGateway.
	// The below is to find it and enable the extproc filter for that route if it is found.
	if route.Metadata == nil || route.Metadata.FilterMetadata == nil {
		s.log.Info("no metadata found in the route, skipping", "route", route.Name)
		return false
	}

	eg, ok := route.Metadata.FilterMetadata["envoy-gateway"]
	if !ok {
		s.log.Info("no envoy-gateway metadata found in the route, skipping", "route", route.Name)
		return false
	}
	resources, ok := eg.Fields["resources"]
	if !ok {
		s.log.Info("no resources found in the envoy-gateway metadata, skipping", "route", route.Name)
		return false
	}
	if resources.GetListValue() == nil || len(resources.GetListValue().Values) == 0 {
		s.log.Info("no resources found in the envoy-gateway metadata, skipping", "route", route.Name)
		return false
	}

	if s.isStandAloneMode {
		// In stand-alone mode, we don't have annotations to check, so instead use the name prefix.
		for _, resource := range resources.GetListValue().Values {
			// Skips all the MCP-related resources.
			if name, ok := resource.GetStructValue().Fields["name"]; ok {
				if strings.HasPrefix(name.GetStringValue(), internalapi.MCPGeneratedResourceCommonPrefix) {
					return false
				}
			}
		}
		return true
	}

	// Walk through the resources to find the AIGateway-generated HTTPRoute annotation.
	for _, resource := range resources.GetListValue().Values {
		if resource.GetStructValue() == nil {
			s.log.Info("resource is not a struct, skipping", "route", route.Name, "resource", resource)
			continue
		}
		annotations, ok := resource.GetStructValue().Fields["annotations"]
		if !ok {
			s.log.Info("no annotations found in the resource, skipping", "route", route.Name, "resource", resource)
			continue
		}
		if annotations.GetStructValue() == nil {
			s.log.Info("annotations is not a struct, skipping", "route", route.Name, "resource", resource)
			continue
		}
		_, ok = annotations.GetStructValue().Fields[internalapi.AIGatewayGeneratedHTTPRouteAnnotation]
		if ok {
			return true
		}
	}
	return false
}

func routeNameFromRouteConfigName(routeConfigName string) string {
	// Envoy Gateway generated route config names follow:
	// httproute/<namespace>/<route_name>/rule/<index>.
	// We rely on this format for the fallback route name extraction when
	// resource metadata does not carry the route name explicitly.
	parts := strings.Split(routeConfigName, "/")
	if len(parts) >= 3 && parts[0] == "httproute" {
		return fmt.Sprintf("%s/%s", parts[1], parts[2])
	}
	return ""
}

func routeNameFromEnvoyGatewayMetadata(route *routev3.Route) string {
	if route.Metadata == nil || route.Metadata.FilterMetadata == nil {
		return ""
	}
	eg, ok := route.Metadata.FilterMetadata["envoy-gateway"]
	if !ok || eg == nil || eg.Fields == nil {
		return ""
	}
	resources, ok := eg.Fields["resources"]
	if !ok || resources.GetListValue() == nil {
		return ""
	}
	for _, resource := range resources.GetListValue().Values {
		resourceStruct := resource.GetStructValue()
		if resourceStruct == nil || resourceStruct.Fields == nil {
			continue
		}
		namespace, ok := resourceStruct.Fields["namespace"]
		if ok && namespace.GetStringValue() != "" {
			name, nameOK := resourceStruct.Fields["name"]
			if nameOK && name.GetStringValue() != "" {
				return fmt.Sprintf("%s/%s", namespace.GetStringValue(), name.GetStringValue())
			}
		}
		name, ok := resourceStruct.Fields["name"]
		if ok && name.GetStringValue() != "" {
			return name.GetStringValue()
		}
	}
	return ""
}

// endpointUpstreamHost returns the DNS hostname of the given endpoint, or "" if it has none.
//
// Envoy Gateway translates a Backend `fqdn` endpoint so that the FQDN lands in
// Endpoint.Address.SocketAddress.Address, while Endpoint.Hostname is populated only from the Backend's
// optional top-level `hostname` field (EG NewDestEndpoint takes the FQDN as `host` and bep.Hostname as
// `hostname`). So for a normal Bedrock/VPCE backend Endpoint.Hostname is empty and the resolvable name is
// the socket address. We therefore resolve in order: Endpoint.Hostname if set, otherwise the socket
// address when it is a DNS name. An IP-literal socket address is rejected — signing over an IP matches no
// AWS endpoint — so such an endpoint yields no stamp and the signer falls back to the region default.
func endpointUpstreamHost(lbEndpoint *endpointv3.LbEndpoint) string {
	endpoint := lbEndpoint.GetEndpoint()
	if endpoint == nil {
		return ""
	}
	if hostname := endpoint.GetHostname(); hostname != "" {
		return hostname
	}
	// An IP-literal socket address is rejected
	if addr := endpoint.GetAddress().GetSocketAddress().GetAddress(); addr != "" && net.ParseIP(addr) == nil {
		return addr
	}
	return ""
}

// stampUpstreamHostMetadata stamps the resolved upstream host on the endpoint's metadata at
// config-translation time, so the data plane (e.g. the AWS backend auth handler, for SigV4 signing)
// uses the real upstream endpoint instead of re-deriving it at request time. It stamps every endpoint
// with a resolvable hostname regardless of provider: unused by non-AWS backends, but harmless — unlike
// gating on an AWS-Bedrock hostname pattern, it doesn't silently drop custom/VPCE hosts that don't match
// a known AWS naming convention (e.g. bedrock.corp.internal, bedrock-runtime-fips.<region>.amazonaws.com).
func stampUpstreamHostMetadata(endpoint *endpointv3.LbEndpoint) {
	if host := endpointUpstreamHost(endpoint); host != "" {
		setEndpointMetadataUpstreamHost(endpoint, host)
	}
}

func ensureRouteInternalMetadata(route *routev3.Route) *structpb.Struct {
	if route.Metadata == nil {
		route.Metadata = &corev3.Metadata{}
	}
	if route.Metadata.FilterMetadata == nil {
		route.Metadata.FilterMetadata = make(map[string]*structpb.Struct)
	}
	m, ok := route.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
	if !ok || m == nil {
		m = &structpb.Struct{}
		route.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace] = m
	}
	if m.Fields == nil {
		m.Fields = make(map[string]*structpb.Value)
	}
	return m
}

// setClusterMetadataBackendName sets the backend name on cluster-level metadata.
// This is used when endpoint-level metadata is unavailable (e.g. LoadAssignment is nil)
// or for InferencePool backends where cluster-level metadata is preferred.
func setClusterMetadataBackendName(cluster *clusterv3.Cluster, namespace, name, routeName string, routeRuleIndex, refIndex int) {
	if cluster.Metadata == nil {
		cluster.Metadata = &corev3.Metadata{}
	}
	if cluster.Metadata.FilterMetadata == nil {
		cluster.Metadata.FilterMetadata = make(map[string]*structpb.Struct)
	}
	m, ok := cluster.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
	if !ok {
		m = &structpb.Struct{}
		cluster.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace] = m
	}
	if m.Fields == nil {
		m.Fields = make(map[string]*structpb.Value)
	}
	m.Fields[internalapi.InternalMetadataBackendNameKey] = structpb.NewStringValue(
		internalapi.PerRouteRuleRefBackendName(namespace, name, routeName, routeRuleIndex, refIndex),
	)
}

// ensureEndpointAIGatewayMetadata returns the AI Gateway filter-metadata struct for the endpoint,
// creating the metadata containers as needed.
func ensureEndpointAIGatewayMetadata(endpoint *endpointv3.LbEndpoint) *structpb.Struct {
	if endpoint.Metadata == nil {
		endpoint.Metadata = &corev3.Metadata{}
	}
	if endpoint.Metadata.FilterMetadata == nil {
		endpoint.Metadata.FilterMetadata = make(map[string]*structpb.Struct)
	}
	m, ok := endpoint.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
	if !ok {
		m = &structpb.Struct{}
		endpoint.Metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace] = m
	}
	if m.Fields == nil {
		m.Fields = make(map[string]*structpb.Value)
	}
	return m
}

func setEndpointMetadataBackendName(endpoint *endpointv3.LbEndpoint, namespace, name, routeName string, routeRuleIndex, refIndex int) {
	ensureEndpointAIGatewayMetadata(endpoint).Fields[internalapi.InternalMetadataBackendNameKey] = structpb.NewStringValue(
		internalapi.PerRouteRuleRefBackendName(namespace, name, routeName, routeRuleIndex, refIndex),
	)
}

// setEndpointMetadataUpstreamHost stores the resolved upstream host on endpoint-level metadata so the
// upstream ext_proc filter can forward it to backend auth handlers that need it (e.g. AWS SigV4 signing).
func setEndpointMetadataUpstreamHost(endpoint *endpointv3.LbEndpoint, host string) {
	ensureEndpointAIGatewayMetadata(endpoint).Fields[internalapi.InternalMetadataUpstreamHostKey] = structpb.NewStringValue(host)
}

func shouldAIGatewayExtProcBeInserted(filters []*httpconnectionmanagerv3.HttpFilter) bool {
	for _, f := range filters {
		if f.Name == aiGatewayExtProcName {
			return false
		}
	}
	return true
}

// insertAIGatewayExtProcFilter inserts the AI Gateway extproc filter into the HTTP connection manager.
//
// The order is mostly simple: make sure that the AI Gateway extproc filter is the first extproc filter in the
// standard Envoy Gateway order. See:
// https://github.com/envoyproxy/gateway/blob/f1e6dab770fabc70d175237380eedfc1f9b1a9e5/internal/xds/translator/httpfilters.go#L93
//
// There is one exception: the HTTP buffer filter (envoy.filters.http.buffer). In Envoy Gateway's canonical
// filter order the buffer filter is placed *before* the ext_proc filters, so a user who raises the request
// buffer limit via the buffer filter (e.g. through an EnvoyPatchPolicy) expects that limit to apply to the AI
// Gateway extproc as well. The AI Gateway extproc runs with RequestBodyMode: BUFFERED, so inserting it ahead of
// the buffer filter would make it buffer the request body against the (smaller) default per-connection buffer
// limit and reject large request bodies with a 413 before the buffer filter's larger limit can take effect. To
// avoid this, when a buffer filter sits at or after the chosen insertion point we insert the AI Gateway extproc
// immediately after the last buffer filter instead. The AI Gateway extproc is then the first ext_proc filter to
// run after the buffer filter (not necessarily the first ext_proc filter overall), so a user-supplied ext_proc
// placed ahead of the buffer filter, e.g. api-key auth, still runs before it.
func insertAIGatewayExtProcFilter(mgr *httpconnectionmanagerv3.HttpConnectionManager, filter *httpconnectionmanagerv3.HttpFilter) error {
	insertIndex := -1
outer:
	for i, existingFilter := range mgr.HttpFilters { // TODO: Maybe searching backwards is faster, but not sure if it's worth the complexity.
		for _, prefix := range afterExtProcFilterPrefixes {
			if strings.HasPrefix(existingFilter.Name, prefix) {
				insertIndex = i
				break outer
			}
		}
	}
	if insertIndex == -1 {
		return errors.New("failed to find insertion point for AI Gateway extproc filter")
	}

	// Find the last buffer filter so we can insert the AI Gateway extproc after it (see the buffer-filter
	// exception in the function doc above).
	lastBufferIndex := -1
	for i, existingFilter := range mgr.HttpFilters {
		if strings.HasPrefix(existingFilter.Name, egv1a1.EnvoyFilterBuffer.String()) {
			lastBufferIndex = i
		}
	}
	// When the buffer filter sits at or after the ext_proc insertion point, insert the AI Gateway extproc
	// immediately after it instead. Behavior is unchanged when no buffer filter exists (lastBufferIndex == -1).
	if lastBufferIndex >= insertIndex {
		insertIndex = lastBufferIndex + 1
	}

	mgr.HttpFilters = append(mgr.HttpFilters, filter)
	copy(mgr.HttpFilters[insertIndex+1:], mgr.HttpFilters[insertIndex:])
	mgr.HttpFilters[insertIndex] = filter
	return nil
}

var afterExtProcFilterPrefixes = []string{
	egv1a1.EnvoyFilterExtProc.String(),
	egv1a1.EnvoyFilterWasm.String(),
	egv1a1.EnvoyFilterLua.String(),
	egv1a1.EnvoyFilterRBAC.String(),
	egv1a1.EnvoyFilterLocalRateLimit.String(),
	egv1a1.EnvoyFilterRateLimit.String(),
	egv1a1.EnvoyFilterCustomResponse.String(),
	egv1a1.EnvoyFilterCredentialInjector.String(),
	egv1a1.EnvoyFilterCompressor.String(),
	egv1a1.EnvoyFilterRouter.String(),
}

// findListenerRouteConfigs extracts route configuration names from the listener's filter chains.
func findListenerRouteConfigs(listener *listenerv3.Listener) []string {
	var names []string
	// First, get the filter chains from the listener.
	for _, filterChain := range listener.FilterChains {
		httpConManager, _, err := findHCM(filterChain)
		if err != nil {
			continue // Skip this filter chain if it doesn't have an HTTP connection manager.
		}
		rds := httpConManager.GetRds()
		if rds == nil {
			continue // Skip if no route discovery service is configured.
		}
		if rds.RouteConfigName != "" {
			names = append(names, rds.RouteConfigName)
		}
	}
	httpConManager, _, err := findHCM(listener.DefaultFilterChain)
	if err != nil {
		return names // Return names collected so far, even if default filter chain has no HCM.
	}
	rds := httpConManager.GetRds()
	if rds == nil {
		return names // Return names collected so far, even if no RDS in default filter chain.
	}
	return append(names, rds.RouteConfigName) // Add default filter chain's route config name.
}
