// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"time"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	least_requestv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/least_request/v3"
	override_hostv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/load_balancing_policies/override_host/v3"
	metadatav3 "github.com/envoyproxy/go-control-plane/envoy/type/metadata/v3"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// fallbackDNSRefreshRate is how often a FailOpen pool's cluster re-resolves its fallback Service.
const fallbackDNSRefreshRate = time.Second

// clusterRefInferencePool generates a unique reference for an InferencePool cluster.
//
// The 7th field, failureMode, is written only for FailOpen pools. FailClose pools keep the 6-field
// format, so an extension server from before the field existed still reads their metadata during
// a rolling upgrade.
func clusterRefInferencePool(namespace, name, serviceName string, servicePort uint32, bodyMode string, allowModeOverride string, failOpen bool) string {
	ref := fmt.Sprintf("%s/%s/%s/%d/%s/%s", namespace, name, serviceName, servicePort, bodyMode, allowModeOverride)
	if failOpen {
		ref += "/" + string(gwaiev1.EndpointPickerFailOpen)
	}
	return ref
}

// PostClusterModify is called by Envoy Gateway to allow extensions to modify clusters after they are generated.
// This method specifically handles InferencePool backend references by configuring clusters with ORIGINAL_DST
// type and header-based load balancing for endpoint picker integration.
//
// The method processes BackendExtensionResources to find InferencePool resources and configures
// the corresponding clusters to work with the Gateway API Inference Extension's endpoint picker pattern.
func (s *Server) PostClusterModify(_ context.Context, req *egextension.PostClusterModifyRequest) (*egextension.PostClusterModifyResponse, error) {
	if req.Cluster == nil {
		return nil, nil
	}

	// Check if we have backend extension resources (InferencePool resources).
	// If no extension resources are present, this is a regular AIServiceBackend cluster.
	if req.PostClusterContext == nil || len(req.PostClusterContext.BackendExtensionResources) == 0 {
		// No backend extension resources, skip modification and return cluster as-is.
		return &egextension.PostClusterModifyResponse{Cluster: req.Cluster}, nil
	}

	// Parse InferencePools resources from BackendExtensionResources.
	// BackendExtensionResources contains unstructured Kubernetes resources that were
	// referenced in the AIGatewayRoute's BackendRefs with non-empty Group and Kind fields.
	// If we found an InferencePool, configure the cluster for ORIGINAL_DST.
	if inferencePools := s.constructInferencePoolsFrom(req.PostClusterContext.BackendExtensionResources); inferencePools != nil {
		if len(inferencePools) != 1 {
			return nil, fmt.Errorf("BUG: at most one inferencepool can be referenced per route rule")
		}
		pool := inferencePools[0]
		if pool.Spec.EndpointPickerRef == nil {
			// No endpoint picker configured for this InferencePool (spec.endpointPickerRef is
			// optional as of Gateway API Inference Extension v1.5.0). We don't yet support
			// routing traffic without one, so leave the cluster as Envoy Gateway generated it
			// rather than wiring up EPP config that would panic on the nil reference.
			return &egextension.PostClusterModifyResponse{Cluster: req.Cluster}, nil
		}
		if err := s.handleInferencePoolCluster(req.Cluster, pool); err != nil {
			return nil, err
		}
	}

	return &egextension.PostClusterModifyResponse{Cluster: req.Cluster}, nil
}

// handleInferencePoolCluster modifies clusters that have InferencePool backends to work with the
// Gateway API Inference Extension's endpoint picker pattern.
//
// The endpoint picker chooses the destination endpoint for each request. How the cluster reaches it
// depends on the pool's failureMode:
//   - FailClose (the default): ORIGINAL_DST on the x-gateway-destination-endpoint header. A request
//     goes only to the endpoint the picker chose.
//   - FailOpen: the pool's ready endpoints with an override_host policy that prefers the picker's
//     choice and otherwise falls back to LeastRequest, so requests continue without the picker.
func (s *Server) handleInferencePoolCluster(cluster *clusterv3.Cluster, inferencePool *gwaiev1.InferencePool) error {
	// Set a reasonable connection timeout. This is quite long to accommodate AI workloads.
	cluster.ConnectTimeout = durationpb.New(10 * time.Second)

	if inferencePoolFailsOpen(inferencePool) {
		if err := configureFallbackClusterForInferencePool(cluster, inferencePool); err != nil {
			return err
		}
	} else {
		configureOriginalDstClusterForInferencePool(cluster)
	}

	// Configure the upstream HTTP protocol (HTTP/1.1 vs. cleartext HTTP/2) to match the
	// InferencePool's spec.appProtocol. This cluster's backend is a Pod, not a Kubernetes
	// Service, so Envoy Gateway has no appProtocol hint to translate on its own; without this,
	// Envoy always defaults to HTTP/1.1 and requests to h2c-only backends fail.
	protocolOptions, err := httpProtocolOptionsForInferencePoolBackend(inferencePool)
	if err != nil {
		return err
	}
	cluster.TypedExtensionProtocolOptions = protocolOptions

	// Add InferencePool metadata to the cluster for reference by other components.
	buildEPPMetadataForCluster(cluster, inferencePool)
	return nil
}

// configureOriginalDstClusterForInferencePool configures a FailClose pool's cluster as ORIGINAL_DST
// on the x-gateway-destination-endpoint header: every request goes to exactly the endpoint the
// endpoint picker chose, and fails if the picker doesn't answer.
func configureOriginalDstClusterForInferencePool(cluster *clusterv3.Cluster) {
	// Configure cluster for ORIGINAL_DST with header-based load balancing.
	// ORIGINAL_DST type allows Envoy to route to destinations specified in HTTP headers.
	cluster.ClusterDiscoveryType = &clusterv3.Cluster_Type{Type: clusterv3.Cluster_ORIGINAL_DST}

	// CLUSTER_PROVIDED load balancing policy is required for ORIGINAL_DST clusters.
	cluster.LbPolicy = clusterv3.Cluster_CLUSTER_PROVIDED

	// Configure original destination load balancer to use the x-gateway-destination-endpoint HTTP header.
	// The endpoint picker service will set this header to specify the target backend endpoint.
	cluster.LbConfig = &clusterv3.Cluster_OriginalDstLbConfig_{
		OriginalDstLbConfig: &clusterv3.Cluster_OriginalDstLbConfig{
			UseHttpHeader:  true,
			HttpHeaderName: internalapi.EndpointPickerHeaderKey,
		},
	}

	// Clear load balancing policy since we're using ORIGINAL_DST.
	cluster.LoadBalancingPolicy = nil

	// Remove EDS (Endpoint Discovery Service) config since we are using ORIGINAL_DST.
	// With ORIGINAL_DST, endpoints are determined dynamically via headers, not EDS.
	cluster.EdsClusterConfig = nil
}

// configureFallbackClusterForInferencePool configures a FailOpen pool's cluster so that requests
// still reach the pool when the endpoint picker is unavailable, and so that a retry can leave an
// endpoint that failed.
//
// The cluster's endpoints are the pool's ready Pods: STRICT_DNS on the headless fallback Service
// the controller creates for the pool (internalapi.InferencePoolFallbackServiceName), which has the
// pool's selector and target ports. Hosts are chosen by the override_host load balancing policy:
//   - the endpoint picker's choice, read from its dynamic metadata, when it made one;
//   - otherwise LeastRequest across the pool: when the picker is unreachable, or on a retry after
//     the chosen endpoint failed (with a retry policy on the route, Envoy Gateway adds the
//     previous_hosts retry predicate, which rejects it).
//
// The override is read from dynamic metadata, not the x-gateway-destination-endpoint request
// header, so a client cannot choose the upstream, and override_host only selects hosts that are
// already in the cluster.
func configureFallbackClusterForInferencePool(cluster *clusterv3.Cluster, pool *gwaiev1.InferencePool) error {
	policy, err := overrideHostLoadBalancingPolicy()
	if err != nil {
		return fmt.Errorf("failed to build load balancing policy for InferencePool %s/%s: %w", pool.Namespace, pool.Name, err)
	}
	cluster.ClusterDiscoveryType = &clusterv3.Cluster_Type{Type: clusterv3.Cluster_STRICT_DNS}
	// lb_policy is superseded by load_balancing_policy; reset whatever Envoy Gateway set.
	cluster.LbPolicy = clusterv3.Cluster_ROUND_ROBIN
	cluster.LbConfig = nil
	cluster.LoadBalancingPolicy = policy
	cluster.EdsClusterConfig = nil
	cluster.OutlierDetection = fallbackOutlierDetection()
	// Re-resolve the fallback Service every second. Envoy's default is 5 seconds, which is how long a
	// Pod that left the Service keeps getting requests, and how long the pool has no endpoints if
	// Envoy resolves the name before the controller has created the Service (when a pool is switched
	// to FailOpen the cluster and the Service are created concurrently).
	cluster.DnsRefreshRate = durationpb.New(fallbackDNSRefreshRate)
	cluster.DnsFailureRefreshRate = &clusterv3.Cluster_RefreshRate{
		BaseInterval: durationpb.New(fallbackDNSRefreshRate),
		MaxInterval:  durationpb.New(fallbackDNSRefreshRate),
	}

	host := fmt.Sprintf("%s.%s.svc", internalapi.InferencePoolFallbackServiceName(pool.Name), pool.Namespace)
	lbEndpoints := make([]*endpointv3.LbEndpoint, 0, len(pool.Spec.TargetPorts))
	for _, targetPort := range pool.Spec.TargetPorts {
		lbEndpoints = append(lbEndpoints, &endpointv3.LbEndpoint{
			HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
				Endpoint: &endpointv3.Endpoint{
					Address: &corev3.Address{
						Address: &corev3.Address_SocketAddress{
							SocketAddress: &corev3.SocketAddress{
								Address:       host,
								Protocol:      corev3.SocketAddress_TCP,
								PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: uint32(targetPort.Number)}, // #nosec G115 -- validated by the CRD to [1, 65535]
							},
						},
					},
				},
			},
		})
	}
	cluster.LoadAssignment = &endpointv3.ClusterLoadAssignment{
		ClusterName: cluster.Name,
		Endpoints:   []*endpointv3.LocalityLbEndpoints{{LbEndpoints: lbEndpoints}},
	}
	return nil
}

// overrideHostLoadBalancingPolicy returns an override_host policy that takes the host from the
// endpoint picker's dynamic metadata and otherwise falls back to LeastRequest.
func overrideHostLoadBalancingPolicy() (*clusterv3.LoadBalancingPolicy, error) {
	leastRequest, err := toAny(&least_requestv3.LeastRequest{})
	if err != nil {
		return nil, err
	}
	overrideHost, err := toAny(&override_hostv3.OverrideHost{
		OverrideHostSources: []*override_hostv3.OverrideHost_OverrideHostSource{{
			Metadata: &metadatav3.MetadataKey{
				Key:  internalapi.EndpointPickerMetadataNamespace,
				Path: []*metadatav3.MetadataKey_PathSegment{{Segment: &metadatav3.MetadataKey_PathSegment_Key{Key: internalapi.EndpointPickerHeaderKey}}},
			},
		}},
		FallbackPolicy: &clusterv3.LoadBalancingPolicy{
			Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
				TypedExtensionConfig: &corev3.TypedExtensionConfig{
					Name:        "envoy.load_balancing_policies.least_request",
					TypedConfig: leastRequest,
				},
			}},
		},
	})
	if err != nil {
		return nil, err
	}
	return &clusterv3.LoadBalancingPolicy{
		Policies: []*clusterv3.LoadBalancingPolicy_Policy{{
			TypedExtensionConfig: &corev3.TypedExtensionConfig{
				Name:        "envoy.load_balancing_policies.override_host",
				TypedConfig: overrideHost,
			},
		}},
	}, nil
}

// fallbackOutlierDetection ejects a pool endpoint that keeps failing at the connection level, so that
// requests stop going to a Pod that is Ready but broken until it recovers.
//
// The fallback cluster is resolved through DNS and has no active health check, so without this a
// retry can leave a failed endpoint but the next request picks it again. Only local origin failures
// (connect failures, resets and timeouts) count. Any HTTP status from the model server, including
// 500 for a bad request and 503 when it is overloaded, must not eject a Pod that answers, so 5xx,
// gateway failure and success rate ejection are disabled. At most half of the endpoints are ejected,
// so a pool-wide failure never empties the cluster.
//
// An endpoint the endpoint picker chooses is not affected: override_host selects it even while it is
// ejected, so this only keeps the fallback policy away from it.
func fallbackOutlierDetection() *clusterv3.OutlierDetection {
	return &clusterv3.OutlierDetection{
		SplitExternalLocalOriginErrors:         true,
		ConsecutiveLocalOriginFailure:          wrapperspb.UInt32(3),
		EnforcingConsecutiveLocalOriginFailure: wrapperspb.UInt32(100),
		EnforcingConsecutiveGatewayFailure:     wrapperspb.UInt32(0),
		EnforcingConsecutive_5Xx:               wrapperspb.UInt32(0),
		EnforcingSuccessRate:                   wrapperspb.UInt32(0),
		BaseEjectionTime:                       durationpb.New(30 * time.Second),
		MaxEjectionPercent:                     wrapperspb.UInt32(50),
	}
}
