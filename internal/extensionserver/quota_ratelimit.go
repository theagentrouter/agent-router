// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	ratelimitv3 "github.com/envoyproxy/go-control-plane/envoy/config/ratelimit/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	ratelimitfilterv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ratelimit/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	matcherv3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	metadatav3 "github.com/envoyproxy/go-control-plane/envoy/type/metadata/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/go-logr/logr"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/translator"
)

const (
	// quotaRateLimitClusterName is the Envoy cluster name for the AI Gateway rate limit service.
	quotaRateLimitClusterName = "ai_gateway_ratelimit_cluster"
	// quotaRateLimitFilterName is the name of the rate limit HTTP filter inserted into the
	// HCM filter chain for QuotaPolicy enforcement. The name uses a suffix to avoid
	// conflicting with Envoy Gateway's own rate limit filter (e.g., from BackendTrafficPolicy).
	// The per-route TypedPerFilterConfig keys on this name to target the quota filter specifically.
	quotaRateLimitFilterName = "envoy.filters.http.ratelimit/ai-gateway-quota"
	// defaultQuotaRateLimitServicePort is the default gRPC port for the rate limit service.
	defaultQuotaRateLimitServicePort = 8081

	// quotaCostMetadataKey is the dynamic metadata key where ext_proc stores
	// the computed quota cost for the current request.
	quotaCostMetadataKey = "quota_cost"
)

// maybeInjectQuotaRateLimiting injects the rate limit HTTP filter into the HCM
// filter chain of listeners that serve AI Gateway routes with quota backends,
// adds the rate limit service cluster, and patches routes with rate limit actions.
func (s *Server) maybeInjectQuotaRateLimiting(
	ctx context.Context,
	clusters []*clusterv3.Cluster,
	listeners []*listenerv3.Listener,
	routes []*routev3.RouteConfiguration,
) ([]*clusterv3.Cluster, error) {
	// Find all QuotaPolicies and their target backends.
	quotaPolicies, err := s.listQuotaPolicies(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return clusters, nil
		}
		return clusters, fmt.Errorf("failed to list QuotaPolicies: %w", err)
	}
	if len(quotaPolicies) == 0 {
		return clusters, nil
	}

	// Build a map of "namespace/backendName" to the QuotaPolicies targeting each backend.
	quotaBackendPolicies := buildQuotaBackendPolicies(quotaPolicies)
	if len(quotaBackendPolicies) == 0 {
		return clusters, nil
	}

	// Add rate limit service cluster if it doesn't exist.
	clusterExists := false
	for _, c := range clusters {
		if c.Name == quotaRateLimitClusterName {
			clusterExists = true
			break
		}
	}
	if !clusterExists {
		rlCluster := s.buildQuotaRateLimitCluster()
		clusters = append(clusters, rlCluster)
		s.log.Info("Added quota rate limit cluster", "cluster", quotaRateLimitClusterName)
	}

	// Patch routes and track which route configs had quota backends enabled.
	quotaEnabledRoutes := make(map[string]bool)
	for _, routeConfig := range routes {
		if s.patchRoutesWithQuotaRateLimits(ctx, routeConfig, quotaBackendPolicies) {
			quotaEnabledRoutes[routeConfig.Name] = true
		}
	}

	// Only inject the rate limit filter into listeners whose routes have quota backends.
	for _, ln := range listeners {
		hasQuotaRoute := false
		for _, rcName := range findListenerRouteConfigs(ln) {
			if quotaEnabledRoutes[rcName] {
				hasQuotaRoute = true
				break
			}
		}
		if hasQuotaRoute {
			if err := s.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain); err != nil {
				s.log.Error(err, "failed to inject quota rate limit filter into listener", "listener", ln.Name)
			}
		}
	}

	return clusters, nil
}

// listQuotaPolicies lists all QuotaPolicy resources across all namespaces.
func (s *Server) listQuotaPolicies(ctx context.Context) ([]aigv1a1.QuotaPolicy, error) {
	var list aigv1a1.QuotaPolicyList
	if err := s.k8sClient.List(ctx, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// buildQuotaBackendPolicies builds a map from "namespace/backendName" keys to the
// QuotaPolicies that target each backend. This preserves the full QuotaPolicy data
// (including PerModelQuotas, BucketRules, and ClientSelectors) so that downstream
// functions can generate header-matching rate limit actions.
func buildQuotaBackendPolicies(policies []aigv1a1.QuotaPolicy) map[string][]aigv1a1.QuotaPolicy {
	backends := make(map[string][]aigv1a1.QuotaPolicy)
	for i := range policies {
		policy := &policies[i]
		for _, ref := range policy.Spec.TargetRefs {
			key := policy.Namespace + "/" + string(ref.Name)
			backends[key] = append(backends[key], *policy)
		}
	}
	return backends
}

// buildQuotaRateLimitCluster creates the Envoy cluster for the AI Gateway rate limit service.
func (s *Server) buildQuotaRateLimitCluster() *clusterv3.Cluster {
	return &clusterv3.Cluster{
		Name:                 quotaRateLimitClusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STRICT_DNS},
		ConnectTimeout:       &durationpb.Duration{Seconds: 5},
		Http2ProtocolOptions: &corev3.Http2ProtocolOptions{},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			ClusterName: quotaRateLimitClusterName,
			Endpoints: []*endpointv3.LocalityLbEndpoints{
				{
					LbEndpoints: []*endpointv3.LbEndpoint{
						{
							HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
								Endpoint: &endpointv3.Endpoint{
									Address: &corev3.Address{
										Address: &corev3.Address_SocketAddress{
											SocketAddress: &corev3.SocketAddress{
												Address: s.quotaRateLimitServiceHost,
												PortSpecifier: &corev3.SocketAddress_PortValue{
													PortValue: s.quotaRateLimitServicePort,
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
		},
	}
}

// injectQuotaRateLimitFilterIntoListener adds the quota rate limit HTTP filter
// into the HCM filter chain of the given listener. The filter is inserted before the
// router filter. It is a no-op on routes without per-route RateLimitPerRoute config.
func (s *Server) injectQuotaRateLimitFilterIntoListener(ln *listenerv3.Listener, domain string) error {
	filterChains := ln.GetFilterChains()
	if ln.DefaultFilterChain != nil {
		filterChains = append(filterChains, ln.DefaultFilterChain)
	}
	for _, currChain := range filterChains {
		httpConManager, hcmIndex, err := findHCM(currChain)
		if err != nil {
			continue
		}

		// Check if the filter already exists.
		alreadyExists := false
		for _, f := range httpConManager.HttpFilters {
			if f.Name == quotaRateLimitFilterName {
				alreadyExists = true
				break
			}
		}
		if alreadyExists {
			continue
		}

		rateLimitFilter, err := s.buildQuotaRateLimitFilter(domain)
		if err != nil {
			return fmt.Errorf("failed to build quota rate limit filter: %w", err)
		}
		// The filter is always enabled at the HCM level. Routes without
		// per-route RateLimitPerRoute config will have no rate limit actions,
		// making the filter a no-op. We cannot use Disabled=true here because
		// ext_proc clears the route cache after modifying headers, and the
		// filter chain is created based on the initial route match (before
		// ext_proc runs). The initial route typically lacks the per-route
		// config, so a disabled filter would never be re-enabled.

		// Insert before the router filter.
		inserted := false
		for i, f := range httpConManager.HttpFilters {
			if f.Name == wellknown.Router {
				httpConManager.HttpFilters = append(httpConManager.HttpFilters, nil)
				copy(httpConManager.HttpFilters[i+1:], httpConManager.HttpFilters[i:])
				httpConManager.HttpFilters[i] = rateLimitFilter
				inserted = true
				break
			}
		}
		if !inserted {
			httpConManager.HttpFilters = append(httpConManager.HttpFilters, rateLimitFilter)
		}

		hcmAny, err := toAny(httpConManager)
		if err != nil {
			return fmt.Errorf("failed to marshal HttpConnectionManager: %w", err)
		}
		currChain.Filters[hcmIndex].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny}
	}
	return nil
}

// buildQuotaRateLimitFilter creates the envoy.filters.http.ratelimit filter
// for QuotaPolicy enforcement in the HCM filter chain.
func (s *Server) buildQuotaRateLimitFilter(domain string) (*httpconnectionmanagerv3.HttpFilter, error) {
	rateLimitCfg := &ratelimitfilterv3.RateLimit{
		Domain: domain,
		RateLimitService: &ratelimitv3.RateLimitServiceConfig{
			GrpcService: &corev3.GrpcService{
				TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
					EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
						ClusterName: quotaRateLimitClusterName,
					},
				},
			},
			TransportApiVersion: corev3.ApiVersion_V3,
		},
		Timeout:                        &durationpb.Duration{Seconds: s.quotaRateLimitTimeout},
		FailureModeDeny:                s.quotaRateLimitFailureModeDeny,
		DisableXEnvoyRatelimitedHeader: true,
		EnableXRatelimitHeaders:        ratelimitfilterv3.RateLimit_DRAFT_VERSION_03,
		RateLimitedAsResourceExhausted: false,
	}

	cfgAny, err := anypb.New(rateLimitCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rate limit filter config: %w", err)
	}

	return &httpconnectionmanagerv3.HttpFilter{
		Name: quotaRateLimitFilterName,
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{
			TypedConfig: cfgAny,
		},
	}, nil
}

// patchRoutesWithQuotaRateLimits adds rate limit actions to routes that target
// AIServiceBackends with QuotaPolicies. The actions extract the backend name
// from dynamic metadata and the model name from the x-ai-eg-model header.
// Returns true if any route in the configuration was patched.
func (s *Server) patchRoutesWithQuotaRateLimits(
	ctx context.Context,
	routeConfig *routev3.RouteConfiguration,
	quotaBackendPolicies map[string][]aigv1a1.QuotaPolicy,
) bool {
	patched := false
	for _, vh := range routeConfig.VirtualHosts {
		for _, route := range vh.Routes {
			if !s.isRouteGeneratedByAIGateway(route) {
				continue
			}
			routeAction := route.GetRoute()
			if routeAction == nil {
				continue
			}
			if !s.routeHasQuotaBackend(ctx, route, quotaBackendPolicies) {
				continue
			}

			policies := s.policiesForRoute(ctx, route, quotaBackendPolicies)
			modelInfo := s.resolveRouteModelInfo(ctx, route)

			if err := enableQuotaRateLimitOnRoute(s.log, route, policies, modelInfo); err != nil {
				s.log.Error(err, "failed to enable quota rate limit on route", "route", route.Name)
			}
			patched = true
		}
	}
	return patched
}

// routeHasQuotaBackend checks whether any backend referenced by the route has
// a QuotaPolicy by resolving the cluster name to an AIGatewayRoute and checking
// its BackendRefs against the quotaBackendPolicies map.
func (s *Server) routeHasQuotaBackend(
	ctx context.Context,
	route *routev3.Route,
	quotaBackendPolicies map[string][]aigv1a1.QuotaPolicy,
) bool {
	routeAction := route.GetRoute()
	if routeAction == nil {
		return false
	}

	// Check single cluster.
	if clusterName := routeAction.GetCluster(); clusterName != "" {
		return s.clusterHasQuotaBackend(ctx, clusterName, quotaBackendPolicies)
	}

	// Check weighted clusters.
	if wc := routeAction.GetWeightedClusters(); wc != nil {
		for _, c := range wc.Clusters {
			if s.clusterHasQuotaBackend(ctx, c.Name, quotaBackendPolicies) {
				return true
			}
		}
	}

	return false
}

// clusterRouteInfo contains the resolved route rule information for a cluster.
type clusterRouteInfo struct {
	namespace string
	rule      *aigv1b1.AIGatewayRouteRule
}

// resolveClusterRule parses a cluster name and fetches the corresponding AIGatewayRoute rule.
// Cluster name format: "httproute/{namespace}/{routeName}/rule/{ruleIndex}"
func (s *Server) resolveClusterRule(ctx context.Context, clusterName string) *clusterRouteInfo {
	parts := strings.Split(clusterName, "/")
	if len(parts) != 5 || parts[0] != "httproute" {
		return nil
	}
	namespace := parts[1]
	routeName := parts[2]
	ruleIndex, err := strconv.Atoi(parts[4])
	if err != nil || ruleIndex < 0 {
		return nil
	}

	var aigwRoute aigv1b1.AIGatewayRoute
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      routeName,
	}, &aigwRoute); err != nil {
		return nil
	}

	if ruleIndex >= len(aigwRoute.Spec.Rules) {
		return nil
	}

	return &clusterRouteInfo{
		namespace: namespace,
		rule:      &aigwRoute.Spec.Rules[ruleIndex],
	}
}

// clusterHasQuotaBackend checks whether a cluster references any AIServiceBackend
// that has a QuotaPolicy attached.
func (s *Server) clusterHasQuotaBackend(ctx context.Context, clusterName string, quotaBackendPolicies map[string][]aigv1a1.QuotaPolicy) bool {
	info := s.resolveClusterRule(ctx, clusterName)
	if info == nil {
		return false
	}

	for _, backendRef := range info.rule.BackendRefs {
		key := info.namespace + "/" + backendRef.Name
		if _, ok := quotaBackendPolicies[key]; ok {
			return true
		}
	}
	return false
}

// policiesForRoute collects the deduplicated QuotaPolicies applicable to a route
// by resolving its clusters to backends and looking up the policies map.
func (s *Server) policiesForRoute(
	ctx context.Context,
	route *routev3.Route,
	quotaBackendPolicies map[string][]aigv1a1.QuotaPolicy,
) []aigv1a1.QuotaPolicy {
	seen := make(map[string]struct{})
	var result []aigv1a1.QuotaPolicy

	collectFromCluster := func(clusterName string) {
		backendKeys := s.backendKeysForCluster(ctx, clusterName)
		for _, key := range backendKeys {
			policies, ok := quotaBackendPolicies[key]
			if !ok {
				continue
			}
			for i := range policies {
				uid := string(policies[i].UID)
				if _, dup := seen[uid]; dup {
					continue
				}
				seen[uid] = struct{}{}
				result = append(result, policies[i])
			}
		}
	}

	routeAction := route.GetRoute()
	if routeAction == nil {
		return nil
	}
	if clusterName := routeAction.GetCluster(); clusterName != "" {
		collectFromCluster(clusterName)
	}
	if wc := routeAction.GetWeightedClusters(); wc != nil {
		for _, c := range wc.Clusters {
			collectFromCluster(c.Name)
		}
	}
	return result
}

// backendKeysForCluster resolves a cluster name to "namespace/backendName" keys
// by fetching the AIGatewayRoute and looking up its BackendRefs.
func (s *Server) backendKeysForCluster(ctx context.Context, clusterName string) []string {
	info := s.resolveClusterRule(ctx, clusterName)
	if info == nil {
		return nil
	}

	var keys []string
	for _, backendRef := range info.rule.BackendRefs {
		keys = append(keys, info.namespace+"/"+backendRef.Name)
	}
	return keys
}

// routeModelInfo holds the resolved model information for an Envoy route.
type routeModelInfo struct {
	// backendModels maps backend name → ModelNameOverrides from the AIGatewayRoute.
	// A single backend may appear multiple times in a rule with different overrides.
	// Used for filtering (a QuotaPolicy's target+modelName must match) and for
	// request-time descriptors (the actual value ext_proc will set).
	backendModels map[string][]string
}

// resolveRouteModelInfo determines the model information for an Envoy route by
// resolving its clusters back to AIGatewayRoute rules.
func (s *Server) resolveRouteModelInfo(ctx context.Context, route *routev3.Route) *routeModelInfo {
	routeAction := route.GetRoute()
	if routeAction == nil {
		return nil
	}

	info := &routeModelInfo{
		backendModels: make(map[string][]string),
	}
	seen := make(map[string]map[string]bool)

	collectFromCluster := func(clusterName string) {
		resolved := s.resolveClusterRule(ctx, clusterName)
		if resolved == nil {
			return
		}

		for _, br := range resolved.rule.BackendRefs {
			if br.ModelNameOverride != "" {
				if seen[br.Name] == nil {
					seen[br.Name] = make(map[string]bool)
				}
				if !seen[br.Name][br.ModelNameOverride] {
					seen[br.Name][br.ModelNameOverride] = true
					info.backendModels[br.Name] = append(info.backendModels[br.Name], br.ModelNameOverride)
				}
			}
		}
	}

	if clusterName := routeAction.GetCluster(); clusterName != "" {
		collectFromCluster(clusterName)
	}
	if wc := routeAction.GetWeightedClusters(); wc != nil {
		for _, c := range wc.Clusters {
			collectFromCluster(c.Name)
		}
	}

	if len(info.backendModels) == 0 {
		return nil
	}
	return info
}

// enableQuotaRateLimitOnRoute sets per-route rate limit actions via TypedPerFilterConfig.
// modelInfo provides the backend→ModelNameOverride mapping used for filtering (a policy's
// target and modelName must match a backend override) and for request-time descriptors.
// If nil, all models are included.
func enableQuotaRateLimitOnRoute(_ logr.Logger, route *routev3.Route, policies []aigv1a1.QuotaPolicy, modelInfo *routeModelInfo) error {
	var rateLimitActions []*routev3.RateLimit

	// streamDoneActions collects the stream-done RateLimit entries built inline during
	// the policy loop. They are appended to rateLimitActions at the end so all
	// request-time entries come first. seenStreamDoneKeys deduplicates across policies
	// that may produce the same descriptor key for the same model and rule index.
	var streamDoneActions []*routev3.RateLimit
	seenStreamDoneKeys := make(map[string]bool)

	var backendModels map[string][]string
	if modelInfo != nil {
		backendModels = modelInfo.backendModels
	}

	for i := range policies {
		policy := &policies[i]
		for _, pmq := range policy.Spec.PerModelQuotas {
			if pmq.ModelName == nil {
				continue
			}
			modelName := *pmq.ModelName
			// Skip this PerModelQuota unless at least one policy target is a backendRef
			// on this route whose ModelNameOverride matches the quota's model name.
			// The model name in the QuotaPolicy must match the ModelNameOverride set
			// in the AIGatewayRoute's BackendRef for the policy to apply.
			if modelInfo != nil {
				matched := false
				for _, target := range policy.Spec.TargetRefs {
					targetName := string(target.Name)
					if overrides, ok := modelInfo.backendModels[targetName]; ok {
						for _, override := range overrides {
							if override == modelName {
								matched = true
								break
							}
						}
						if matched {
							break
						}
					}
				}
				if !matched {
					continue
				}
			}

			if len(pmq.Quota.BucketRules) == 0 && pmq.Quota.DefaultBucket.Limit > 0 {
				entries := buildSimpleModelEntries(modelName, policy.Namespace, policy.Spec.TargetRefs, backendModels)
				rateLimitActions = append(rateLimitActions, entries...)
				// Simple case: 2-level stream-done (backend_name + model_name_override).
				// All simple entries are identical (metadata-only actions, same hits_addend).
				const simpleStreamDoneKey = "_simple_"
				if !seenStreamDoneKeys[simpleStreamDoneKey] {
					seenStreamDoneKeys[simpleStreamDoneKey] = true
					streamDoneActions = append(streamDoneActions, &routev3.RateLimit{
						Actions:           baseDescriptorActions(),
						HitsAddend:        quotaHitsAddend(),
						ApplyOnStreamDone: true,
					})
				}
			} else if len(pmq.Quota.BucketRules) > 0 {
				bucketActions := buildBucketRuleLimitEntries(modelName, policy.Namespace, &pmq.Quota, policy.Spec.TargetRefs, backendModels)
				rateLimitActions = append(rateLimitActions, bucketActions...)
				// Bucket rules: one stream-done per unique rule/header structure.
				// Stream-done actions read backend/model from dynamic metadata and
				// hits_addend uses a single quota_cost key, so entries are identical
				// regardless of target or model.
				for rIdx, rule := range pmq.Quota.BucketRules {
					headers := flattenAndSortClientSelectorHeaders(rule.ClientSelectors)
					var dupKey string
					for mIdx, hdr := range headers {
						dupKey += "|" + translator.BucketRuleDescriptorKey(rIdx, mIdx, hdr.Name, headerMatchKeyValue(hdr))
					}
					if len(headers) == 0 {
						dupKey += "|" + translator.BucketRuleDescriptorKey(rIdx, 0, "", "")
					}
					if !seenStreamDoneKeys[dupKey] {
						seenStreamDoneKeys[dupKey] = true
						clientActions := buildClientSelectorStreamDoneActions(rIdx, rule.ClientSelectors)
						streamDoneActions = append(streamDoneActions, &routev3.RateLimit{
							Actions:           append(baseDescriptorActions(), clientActions...),
							HitsAddend:        quotaHitsAddend(),
							ApplyOnStreamDone: true,
						})
					}
				}
				// Default bucket: 3-level stream-done with GenericKey (always fires).
				if pmq.Quota.DefaultBucket.Limit > 0 {
					defaultKey := translator.DefaultBucketDescriptorKey(len(pmq.Quota.BucketRules))
					dupDefaultKey := defaultKey
					if !seenStreamDoneKeys[dupDefaultKey] {
						seenStreamDoneKeys[dupDefaultKey] = true
						streamDoneActions = append(streamDoneActions, &routev3.RateLimit{
							Actions: append(baseDescriptorActions(), &routev3.RateLimit_Action{
								ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
									GenericKey: &routev3.RateLimit_Action_GenericKey{
										DescriptorKey:   defaultKey,
										DescriptorValue: defaultKey,
									},
								},
							}),
							HitsAddend:        quotaHitsAddend(),
							ApplyOnStreamDone: true,
						})
					}
				}
			}
		}
	}

	rateLimitActions = append(rateLimitActions, streamDoneActions...)

	if len(rateLimitActions) == 0 {
		return nil
	}

	perRouteConfig := &ratelimitfilterv3.RateLimitPerRoute{
		Domain:     translator.QuotaDomain,
		RateLimits: rateLimitActions,
	}

	perRouteAny, err := anypb.New(perRouteConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal RateLimitPerRoute: %w", err)
	}

	if route.TypedPerFilterConfig == nil {
		route.TypedPerFilterConfig = make(map[string]*anypb.Any)
	}
	route.TypedPerFilterConfig[quotaRateLimitFilterName] = perRouteAny
	return nil
}

// baseDescriptorActions returns the two base actions that read ai_service_backend_name
// and model_name_override from dynamic metadata set by the ext_proc filter.
// ai_service_backend_name contains the short "namespace/name" format that matches
// the rate limit service config, as opposed to backend_name which contains the
// full route ref path.
func baseDescriptorActions() []*routev3.RateLimit_Action {
	return []*routev3.RateLimit_Action{
		{
			ActionSpecifier: &routev3.RateLimit_Action_Metadata{
				Metadata: &routev3.RateLimit_Action_MetaData{
					DescriptorKey: translator.BackendNameDescriptorKey,
					MetadataKey: &metadatav3.MetadataKey{
						Key: aigv1b1.AIGatewayFilterMetadataNamespace,
						Path: []*metadatav3.MetadataKey_PathSegment{{
							Segment: &metadatav3.MetadataKey_PathSegment_Key{
								Key: "ai_service_backend_name",
							},
						}},
					},
					Source: routev3.RateLimit_Action_MetaData_DYNAMIC,
				},
			},
		},
		{
			ActionSpecifier: &routev3.RateLimit_Action_Metadata{
				Metadata: &routev3.RateLimit_Action_MetaData{
					DescriptorKey: translator.ModelNameDescriptorKey,
					MetadataKey: &metadatav3.MetadataKey{
						Key: aigv1b1.AIGatewayFilterMetadataNamespace,
						Path: []*metadatav3.MetadataKey_PathSegment{{
							Segment: &metadatav3.MetadataKey_PathSegment_Key{
								Key: "model_name_override",
							},
						}},
					},
					Source: routev3.RateLimit_Action_MetaData_DYNAMIC,
				},
			},
		},
	}
}

// buildSimpleModelEntries creates RateLimit entries for a model with no bucket rules.
// Produces 2-level descriptors (backend_name, model_name_override) matching the
// translator's simple case where rate_limit is directly on the model descriptor.
func buildSimpleModelEntries(modelName, policyNamespace string, targets []gwapiv1a2.LocalPolicyTargetReference, routeModelNames map[string][]string) []*routev3.RateLimit {
	var entries []*routev3.RateLimit

	// Request-time entries only. Stream-done is added once per model in enableQuotaRateLimitOnRoute.
	for _, target := range targets {
		resolvedModel := resolveModelName(string(target.Name), modelName, routeModelNames)
		entries = append(entries, &routev3.RateLimit{
			Actions: requestTimeBaseActions(policyNamespace, string(target.Name), resolvedModel),
		})
	}

	return entries
}

// quotaHitsAddend returns the HitsAddend that reads the quota cost from dynamic
// metadata stored by the ext_proc filter.
func quotaHitsAddend() *routev3.RateLimit_HitsAddend {
	return &routev3.RateLimit_HitsAddend{
		Format: fmt.Sprintf("%%DYNAMIC_METADATA(%s:%s)%%",
			aigv1b1.AIGatewayFilterMetadataNamespace, quotaCostMetadataKey),
	}
}

// buildBucketRuleLimitEntries creates RateLimit entries for a model's bucket rules.
// Each bucket rule and the default bucket produces one request-time entry per target
// backend. The model_name_override descriptor uses the resolved ModelNameOverride
// from the AIGatewayRoute (matching what ext_proc sets in dynamic metadata).
//
// Action order matches the translator's service config tree:
// backend_name (Level 0) → model_name_override (Level 1) → bucket_rule_key (Level 2)
func buildBucketRuleLimitEntries(modelName, policyNamespace string, quota *aigv1a1.QuotaDefinition, targets []gwapiv1a2.LocalPolicyTargetReference, routeModelNames map[string][]string) []*routev3.RateLimit {
	var entries []*routev3.RateLimit

	for _, target := range targets {
		resolvedModel := resolveModelName(string(target.Name), modelName, routeModelNames)

		for rIdx, rule := range quota.BucketRules {
			clientActions := buildClientSelectorActions(rIdx, rule.ClientSelectors)
			actions := requestTimeBaseActions(policyNamespace, string(target.Name), resolvedModel)
			actions = append(actions, clientActions...)
			entries = append(entries, &routev3.RateLimit{Actions: actions})
		}

		if quota.DefaultBucket.Limit > 0 {
			defaultKey := translator.DefaultBucketDescriptorKey(len(quota.BucketRules))
			defaultAction := &routev3.RateLimit_Action{
				ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
					GenericKey: &routev3.RateLimit_Action_GenericKey{
						DescriptorKey:   defaultKey,
						DescriptorValue: defaultKey,
					},
				},
			}
			actions := requestTimeBaseActions(policyNamespace, string(target.Name), resolvedModel)
			actions = append(actions, defaultAction)
			entries = append(entries, &routev3.RateLimit{Actions: actions})
		}
	}

	return entries
}

// resolveModelName returns the model name to use for request-time descriptors.
// If routeModelNames has an entry for the backend that matches fallback, that
// value is used. Otherwise falls back to the QuotaPolicy's modelName.
func resolveModelName(backendName, fallback string, routeModelNames map[string][]string) string {
	if models, ok := routeModelNames[backendName]; ok {
		for _, m := range models {
			if m == fallback {
				return m
			}
		}
	}
	return fallback
}

// requestTimeBaseActions returns GenericKey actions for backend_name and
// model_name_override using known static values. These are used in
// request-time entries before ext_proc has set dynamic metadata.
func requestTimeBaseActions(namespace, backendName, modelName string) []*routev3.RateLimit_Action {
	return []*routev3.RateLimit_Action{
		{
			ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
				GenericKey: &routev3.RateLimit_Action_GenericKey{
					DescriptorKey:   translator.BackendNameDescriptorKey,
					DescriptorValue: namespace + "/" + backendName,
				},
			},
		},
		{
			ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
				GenericKey: &routev3.RateLimit_Action_GenericKey{
					DescriptorKey:   translator.ModelNameDescriptorKey,
					DescriptorValue: modelName,
				},
			},
		},
	}
}

// buildClientSelectorActions converts ClientSelectors into rate limit actions.
// Headers from all selectors are flattened, sorted by name, and each becomes a
// separate action. The sort order matches the nested descriptor tree in the rate
// limit service config. If no selectors are specified, a GenericKey action is used.
func buildClientSelectorActions(
	ruleIndex int, selectors []egv1a1.RateLimitSelectCondition,
) []*routev3.RateLimit_Action {
	headers := flattenAndSortClientSelectorHeaders(selectors)

	if len(headers) == 0 {
		key := translator.BucketRuleDescriptorKey(ruleIndex, 0, "", "")
		return []*routev3.RateLimit_Action{
			{
				ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
					GenericKey: &routev3.RateLimit_Action_GenericKey{
						DescriptorKey:   key,
						DescriptorValue: key,
					},
				},
			},
		}
	}

	var actions []*routev3.RateLimit_Action
	for mIdx, header := range headers {
		actions = append(actions, buildHeaderMatchAction(ruleIndex, mIdx, header))
	}
	return actions
}

// buildClientSelectorStreamDoneActions is like buildClientSelectorActions but
// always uses ExpectMatch=true on HeaderValueMatch actions.
func buildClientSelectorStreamDoneActions(
	ruleIndex int, selectors []egv1a1.RateLimitSelectCondition,
) []*routev3.RateLimit_Action {
	headers := flattenAndSortClientSelectorHeaders(selectors)

	if len(headers) == 0 {
		key := translator.BucketRuleDescriptorKey(ruleIndex, 0, "", "")
		return []*routev3.RateLimit_Action{
			{
				ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
					GenericKey: &routev3.RateLimit_Action_GenericKey{
						DescriptorKey:   key,
						DescriptorValue: key,
					},
				},
			},
		}
	}

	var actions []*routev3.RateLimit_Action
	for mIdx, header := range headers {
		actions = append(actions, buildStreamDoneHeaderMatchAction(ruleIndex, mIdx, header))
	}
	return actions
}

// flattenAndSortClientSelectorHeaders collects all HeaderMatch entries from all
// ClientSelectors and sorts them by header Name for deterministic action ordering
// that matches the nested descriptor tree in the rate limit service config.
func flattenAndSortClientSelectorHeaders(selectors []egv1a1.RateLimitSelectCondition) []egv1a1.HeaderMatch {
	var headers []egv1a1.HeaderMatch
	for _, sel := range selectors {
		headers = append(headers, sel.Headers...)
	}
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].Name < headers[j].Name
	})
	return headers
}

// buildStreamDoneHeaderMatchAction is like buildHeaderMatchAction but always uses
// ExpectMatch=true. Distinct headers use the same RequestHeaders action as the
// request-time entry so that the token cost is charged to the per-value bucket
// that the request-time entry checked, instead of to a single shared bucket.
func buildStreamDoneHeaderMatchAction(
	ruleIndex, matchIndex int, header egv1a1.HeaderMatch,
) *routev3.RateLimit_Action {
	descriptorKey := translator.BucketRuleDescriptorKey(ruleIndex, matchIndex, header.Name, headerMatchKeyValue(header))

	if header.Type != nil && *header.Type == egv1a1.HeaderMatchDistinct {
		return &routev3.RateLimit_Action{
			ActionSpecifier: &routev3.RateLimit_Action_RequestHeaders_{
				RequestHeaders: &routev3.RateLimit_Action_RequestHeaders{
					HeaderName:    header.Name,
					DescriptorKey: descriptorKey,
				},
			},
		}
	}

	stringMatcher := buildStringMatcher(header)
	headerMatcher := &routev3.HeaderMatcher{
		Name: header.Name,
		HeaderMatchSpecifier: &routev3.HeaderMatcher_StringMatch{
			StringMatch: stringMatcher,
		},
	}
	return &routev3.RateLimit_Action{
		ActionSpecifier: &routev3.RateLimit_Action_HeaderValueMatch_{
			HeaderValueMatch: &routev3.RateLimit_Action_HeaderValueMatch{
				DescriptorKey:   descriptorKey,
				DescriptorValue: descriptorKey,
				ExpectMatch:     &wrapperspb.BoolValue{Value: true},
				Headers:         []*routev3.HeaderMatcher{headerMatcher},
			},
		},
	}
}

// buildHeaderMatchAction converts a single HeaderMatch into a rate limit action.
//   - Distinct: RateLimit_Action_RequestHeaders_ (each unique value gets its own bucket)
//   - Exact/RegularExpression: RateLimit_Action_HeaderValueMatch_ with StringMatcher
func buildHeaderMatchAction(
	ruleIndex, matchIndex int, header egv1a1.HeaderMatch,
) *routev3.RateLimit_Action {
	descriptorKey := translator.BucketRuleDescriptorKey(ruleIndex, matchIndex, header.Name, headerMatchKeyValue(header))

	// Distinct: use RequestHeaders action.
	if header.Type != nil && *header.Type == egv1a1.HeaderMatchDistinct {
		return &routev3.RateLimit_Action{
			ActionSpecifier: &routev3.RateLimit_Action_RequestHeaders_{
				RequestHeaders: &routev3.RateLimit_Action_RequestHeaders{
					HeaderName:    header.Name,
					DescriptorKey: descriptorKey,
				},
			},
		}
	}

	// Exact or RegularExpression: use HeaderValueMatch action.
	stringMatcher := buildStringMatcher(header)
	headerMatcher := &routev3.HeaderMatcher{
		Name: header.Name,
		HeaderMatchSpecifier: &routev3.HeaderMatcher_StringMatch{
			StringMatch: stringMatcher,
		},
	}

	expectMatch := header.Invert == nil || !*header.Invert

	return &routev3.RateLimit_Action{
		ActionSpecifier: &routev3.RateLimit_Action_HeaderValueMatch_{
			HeaderValueMatch: &routev3.RateLimit_Action_HeaderValueMatch{
				DescriptorKey:   descriptorKey,
				DescriptorValue: descriptorKey,
				ExpectMatch:     &wrapperspb.BoolValue{Value: expectMatch},
				Headers:         []*routev3.HeaderMatcher{headerMatcher},
			},
		},
	}
}

// headerMatchKeyValue returns the value to include in a BucketRuleDescriptorKey for a header.
// Distinct headers return empty (the value is per-request, not known at config time).
// Exact/Regex headers return the configured value.
func headerMatchKeyValue(header egv1a1.HeaderMatch) string {
	if header.Type != nil && *header.Type == egv1a1.HeaderMatchDistinct {
		return ""
	}
	if header.Value != nil {
		return *header.Value
	}
	return ""
}

// buildStringMatcher creates an Envoy StringMatcher from a HeaderMatch.
func buildStringMatcher(header egv1a1.HeaderMatch) *matcherv3.StringMatcher {
	matchType := egv1a1.HeaderMatchExact
	if header.Type != nil {
		matchType = *header.Type
	}

	switch matchType {
	case egv1a1.HeaderMatchRegularExpression:
		if header.Value != nil {
			return &matcherv3.StringMatcher{
				MatchPattern: &matcherv3.StringMatcher_SafeRegex{
					SafeRegex: &matcherv3.RegexMatcher{
						Regex: *header.Value,
					},
				},
			}
		}
	default: // Exact
		if header.Value != nil {
			return &matcherv3.StringMatcher{
				MatchPattern: &matcherv3.StringMatcher_Exact{
					Exact: *header.Value,
				},
			}
		}
	}

	// Fallback: empty exact match.
	return &matcherv3.StringMatcher{
		MatchPattern: &matcherv3.StringMatcher_Exact{
			Exact: "",
		},
	}
}
