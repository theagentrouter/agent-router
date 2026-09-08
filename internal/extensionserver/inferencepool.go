// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	upstreamsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/envoyproxy/ai-gateway/internal/inferencepool"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	// internalMetadataInferencePoolKey is the key used to store the inference pool metadata.
	// This is only used within the extension server for InferencePool cluster identification.
	internalMetadataInferencePoolKey = "per_route_rule_inference_pool"

	// defaultEndpointPickerPort is the default port for Gateway API Inference Extension endpoint picker services.
	// This port is commonly used by EPP (Endpoint Picker Protocol) services as defined in the
	// Gateway API Inference Extension specification and examples.
	// See: https://gateway-api-inference-extension.sigs.k8s.io/
	defaultEndpointPickerPort = 9002

	// processingBodyModeAnnotation is the annotation key for configuring processing body mode
	processingBodyModeAnnotation = "aigateway.envoyproxy.io/processing-body-mode"
	// allowModeOverrideAnnotation is the annotation key for configuring allow mode override
	allowModeOverrideAnnotation = "aigateway.envoyproxy.io/allow-mode-override"
)

func (s *Server) constructInferencePoolsFrom(extensionResources []*egextension.ExtensionResource) []*gwaiev1.InferencePool {
	// Parse InferencePool resources from BackendExtensionResources.
	// BackendExtensionResources contains unstructured Kubernetes resources that were
	// referenced in the AIGatewayRoute's BackendRefs with non-empty Group and Kind fields.
	var inferencePools []*gwaiev1.InferencePool
	for _, resource := range extensionResources {
		// Unmarshal the unstructured bytes to get the Kubernetes resource.
		// The resource is stored as JSON bytes in the extension context.
		var unstructuredObj unstructured.Unstructured
		if err := json.Unmarshal(resource.UnstructuredBytes, &unstructuredObj); err != nil {
			s.log.Error(err, "failed to unmarshal extension resource", "resource_size", len(resource.UnstructuredBytes))
			continue
		}

		// Check if this is an InferencePool resource from the Gateway API Inference Extension.
		// We only process InferencePool resources; other extension resources are ignored.
		if unstructuredObj.GetAPIVersion() == "inference.networking.k8s.io/v1" &&
			unstructuredObj.GetKind() == inferencepool.Kind {
			// Convert unstructured object to strongly-typed InferencePool.
			// This allows us to access the InferencePool's spec fields safely.
			var pool gwaiev1.InferencePool
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.Object, &pool); err != nil {
				s.log.Error(err, "failed to convert unstructured to InferencePool",
					"name", unstructuredObj.GetName(), "namespace", unstructuredObj.GetNamespace())
				continue
			}
			inferencePools = append(inferencePools, &pool)
		}
	}

	return inferencePools
}

// poolsWithoutEndpointPicker returns the namespace/name of each of pools that doesn't configure
// spec.endpointPickerRef. The field is optional as of Gateway API Inference Extension v1.5.0, but
// routing traffic without an endpoint picker isn't supported, so a rule referencing such a pool
// is rejected (rejectInferencePoolRoute).
//
// TODO(inferencepool-no-epp): support InferencePools without an endpoint picker, e.g. by
// load-balancing across the pool's selected Pods directly, instead of rejecting the rule.
func poolsWithoutEndpointPicker(pools []*gwaiev1.InferencePool) []string {
	var missing []string
	for _, pool := range pools {
		if pool.Spec.EndpointPickerRef == nil {
			missing = append(missing, inferencePoolKey(pool.Namespace, pool.Name))
		}
	}
	return missing
}

// inferencePoolKey returns the namespace/name identity of an InferencePool.
func inferencePoolKey(namespace, name string) string {
	return namespace + "/" + name
}

// rejectInferencePoolRoute makes route answer every request with a 500 and drops its InferencePool
// metadata, so no endpoint picker runs for it. It is used for rules this extension doesn't support,
// failing only that rule rather than the whole translation of the gateway.
func rejectInferencePoolRoute(route *routev3.Route) {
	route.Action = &routev3.Route_DirectResponse{DirectResponse: &routev3.DirectResponseAction{Status: http.StatusInternalServerError}}
	buildEPPMetadataForRoute(route, nil)
}

// getInferencePoolsByMetadata returns the InferencePools stored in the given cluster or route
// metadata. A cluster references exactly one InferencePool, but a route references every
// InferencePool of its rule, since a rule can be weighted across several of them.
func getInferencePoolsByMetadata(meta *corev3.Metadata) []*gwaiev1.InferencePool {
	if meta == nil || meta.FilterMetadata == nil {
		return nil
	}
	m, ok := meta.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
	if !ok || m.Fields == nil {
		return nil
	}
	v, ok := m.Fields[internalMetadataInferencePoolKey]
	if !ok {
		return nil
	}
	list := v.GetListValue()
	if list == nil {
		return nil
	}
	pools := make([]*gwaiev1.InferencePool, 0, len(list.Values))
	for _, item := range list.Values {
		if pool := parseInferencePoolMetadataString(item.GetStringValue()); pool != nil {
			pools = append(pools, pool)
		}
	}
	return pools
}

// parseInferencePoolMetadataString parses a single InferencePool reference encoded by
// buildEPPMetadata as "namespace/name/serviceName/port/bodyMode/allowModeOverride/appProtocol".
// The appProtocol segment is last because its value can itself contain "/" (e.g.
// "kubernetes.io/h2c"), and is empty when the pool doesn't set one.
func parseInferencePoolMetadataString(metadata string) *gwaiev1.InferencePool {
	result := strings.SplitN(metadata, "/", 7)
	if len(result) != 7 {
		return nil
	}
	appProtocol := gwaiev1.AppProtocol(result[6])
	ns := result[0]
	name := result[1]
	serviceName := result[2]
	port, err := strconv.ParseInt(result[3], 10, 32)
	if err != nil {
		return nil
	}
	processingBodyMode := result[4]
	allowModeOverride := result[5]
	return &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				processingBodyModeAnnotation: processingBodyMode,
				allowModeOverrideAnnotation:  allowModeOverride,
			},
		},
		Spec: gwaiev1.InferencePoolSpec{
			AppProtocol: appProtocol,
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: gwaiev1.ObjectName(serviceName),
				Port: ptr.To(gwaiev1.Port{Number: gwaiev1.PortNumber(port)}),
			},
		},
	}
}

// buildEPPMetadataForCluster adds InferencePool metadata to the cluster for reference by other components.
func buildEPPMetadataForCluster(cluster *clusterv3.Cluster, inferencePools []*gwaiev1.InferencePool) {
	// Initialize cluster metadata structure if not present.
	if cluster.Metadata == nil {
		cluster.Metadata = &corev3.Metadata{}
	}
	buildEPPMetadata(cluster.Metadata, inferencePools)
}

// buildEPPMetadataForRoute adds InferencePool metadata to the route for reference by other components.
func buildEPPMetadataForRoute(route *routev3.Route, inferencePools []*gwaiev1.InferencePool) {
	// Initialize route metadata structure if not present.
	if route.Metadata == nil {
		route.Metadata = &corev3.Metadata{}
	}
	buildEPPMetadata(route.Metadata, inferencePools)
}

// buildEPPMetadata adds a reference to every given InferencePool to the given metadata structure,
// one encoded string per pool, so getInferencePoolsByMetadata can recover the full set later.
func buildEPPMetadata(metadata *corev3.Metadata, inferencePools []*gwaiev1.InferencePool) {
	if metadata.FilterMetadata == nil {
		metadata.FilterMetadata = make(map[string]*structpb.Struct)
	}

	// Get or create the internal metadata namespace for AI Gateway.
	m, ok := metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
	if !ok {
		m = &structpb.Struct{}
		metadata.FilterMetadata[internalapi.InternalEndpointMetadataNamespace] = m
	}
	if m.Fields == nil {
		m.Fields = make(map[string]*structpb.Value)
	}

	// Store a reference to each InferencePool as metadata for later retrieval.
	// Each reference includes all information needed to build EPP clusters and filters.
	values := make([]*structpb.Value, 0, len(inferencePools))
	for _, inferencePool := range inferencePools {
		// Read processing body mode from annotations, default to "duplex" (FULL_DUPLEX_STREAMED)
		processingBodyMode := getProcessingBodyModeStringFromAnnotations(inferencePool)
		// Read allow mode override from annotations, default to false
		allowModeOverride := getAllowModeOverrideStringFromAnnotations(inferencePool)
		values = append(values, structpb.NewStringValue(
			clusterRefInferencePool(
				inferencePool.Namespace,
				inferencePool.Name,
				string(inferencePool.Spec.EndpointPickerRef.Name),
				portForInferencePool(inferencePool),
				processingBodyMode,
				allowModeOverride,
				string(inferencePool.Spec.AppProtocol),
			),
		))
	}
	m.Fields[internalMetadataInferencePoolKey] = structpb.NewListValue(&structpb.ListValue{Values: values})
}

// buildClustersForInferencePoolEndpointPickers builds and returns a "STRICT_DNS" cluster
// for each distinct InferencePool's endpoint picker service referenced by the given clusters,
// deduplicated by cluster name, since two different clusters can reference the same pool.
func buildClustersForInferencePoolEndpointPickers(clusters []*clusterv3.Cluster) ([]*clusterv3.Cluster, error) {
	result := make([]*clusterv3.Cluster, 0, len(clusters))
	seen := make(map[string]struct{})
	for _, cluster := range clusters {
		for _, pool := range getInferencePoolsByMetadata(cluster.Metadata) {
			name := clusterNameForInferencePool(pool)
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			c, err := buildExtProcClusterForInferencePoolEndpointPicker(pool)
			if err != nil {
				return nil, err
			}
			result = append(result, c)
		}
	}
	return result, nil
}

// buildExtProcClusterForInferencePoolEndpointPicker builds and returns a "STRICT_DNS" cluster
// for connecting to the InferencePool's endpoint picker service.
func buildExtProcClusterForInferencePoolEndpointPicker(pool *gwaiev1.InferencePool) (*clusterv3.Cluster, error) {
	name := clusterNameForInferencePool(pool)
	anyTLS, err := toAny(&tlsv3.UpstreamTlsContext{
		CommonTlsContext: &tlsv3.CommonTlsContext{
			ValidationContextType: &tlsv3.CommonTlsContext_ValidationContext{},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS context for InferencePool cluster %s: %w", name, err)
	}
	c := &clusterv3.Cluster{
		Name:           name,
		ConnectTimeout: durationpb.New(10 * time.Second),
		ClusterDiscoveryType: &clusterv3.Cluster_Type{
			Type: clusterv3.Cluster_STRICT_DNS,
		},
		LbPolicy: clusterv3.Cluster_LEAST_REQUEST,
		// Ensure Envoy accepts untrusted certificates.
		TransportSocket: &corev3.TransportSocket{
			Name: "envoy.transport_sockets.tls",
			ConfigType: &corev3.TransportSocket_TypedConfig{
				TypedConfig: anyTLS,
			},
		},
		LoadAssignment: &endpointv3.ClusterLoadAssignment{
			ClusterName: name,
			Endpoints: []*endpointv3.LocalityLbEndpoints{{
				LbEndpoints: []*endpointv3.LbEndpoint{{
					HealthStatus: corev3.HealthStatus_HEALTHY,
					HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
						Endpoint: &endpointv3.Endpoint{
							Address: &corev3.Address{
								Address: &corev3.Address_SocketAddress{
									SocketAddress: &corev3.SocketAddress{
										Address:  dnsNameForInferencePool(pool),
										Protocol: corev3.SocketAddress_TCP,
										PortSpecifier: &corev3.SocketAddress_PortValue{
											PortValue: portForInferencePool(pool),
										},
									},
								},
							},
						},
					},
				}},
			}},
		},
	}

	http2Opts := &upstreamsv3.HttpProtocolOptions{
		UpstreamProtocolOptions: &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig_{
			ExplicitHttpConfig: &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig{
				ProtocolConfig: &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
					Http2ProtocolOptions: &corev3.Http2ProtocolOptions{},
				},
			},
		},
	}

	anyHTTP2, err := toAny(http2Opts)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP2 options for InferencePool cluster %s: %w", name, err)
	}
	c.TypedExtensionProtocolOptions = map[string]*anypb.Any{
		httpProtocolOptionsKey: anyHTTP2,
	}

	return c, nil
}

// httpProtocolOptionsForInferencePoolBackend builds TypedExtensionProtocolOptions for the
// InferencePool's backend cluster (the ORIGINAL_DST cluster used to reach the pool's selected
// model-server Pods), honoring the InferencePool's spec.appProtocol.
//
// The InferencePool is not a Kubernetes Service, so Envoy Gateway has no appProtocol hint to
// translate into upstream HTTP protocol options for this cluster the way it would for a normal
// Service backend (see envoyproxy/gateway's resolveBackendProtocol, which maps the same
// "kubernetes.io/h2c" string to explicit HTTP/2). We derive it ourselves here: h2c gets explicit
// cleartext HTTP/2, everything else (including the default "http" and an unset value) gets
// explicit HTTP/1.1.
func httpProtocolOptionsForInferencePoolBackend(pool *gwaiev1.InferencePool) (map[string]*anypb.Any, error) {
	explicitHTTPConfig := &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig{
		ProtocolConfig: &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig_HttpProtocolOptions{},
	}
	if pool.Spec.AppProtocol == gwaiev1.AppProtocolH2C {
		explicitHTTPConfig.ProtocolConfig = &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
			Http2ProtocolOptions: &corev3.Http2ProtocolOptions{},
		}
	}

	poAny, err := toAny(&upstreamsv3.HttpProtocolOptions{
		UpstreamProtocolOptions: &upstreamsv3.HttpProtocolOptions_ExplicitHttpConfig_{
			ExplicitHttpConfig: explicitHTTPConfig,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP protocol options for InferencePool %s/%s: %w",
			pool.Namespace, pool.Name, err)
	}
	return map[string]*anypb.Any{httpProtocolOptionsKey: poAny}, nil
}

// buildInferencePoolHTTPFilter returns a HTTP filter for InferencePool.
func buildInferencePoolHTTPFilter(pool *gwaiev1.InferencePool) (*httpconnectionmanagerv3.HttpFilter, error) {
	poolFilter := buildHTTPFilterForInferencePool(pool)
	a, err := toAny(poolFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP filter for InferencePool %s/%s: %w", pool.GetNamespace(), pool.GetName(), err)
	}
	return &httpconnectionmanagerv3.HttpFilter{
		Name:       httpFilterNameForInferencePool(pool),
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: a},
	}, nil
}

// buildHTTPFilterForInferencePool returns the HTTP filter for the given InferencePool.
func buildHTTPFilterForInferencePool(pool *gwaiev1.InferencePool) *extprocv3.ExternalProcessor {
	// Read processing body mode from annotations, default to "duplex" (FULL_DUPLEX_STREAMED)
	processingBodyMode := getProcessingBodyModeFromAnnotations(pool)

	// Read allow mode override from annotations, default to false
	allowModeOverride := getAllowModeOverrideFromAnnotations(pool)

	return &extprocv3.ExternalProcessor{
		GrpcService: &corev3.GrpcService{
			TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
				EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
					ClusterName: clusterNameForInferencePool(pool),
					Authority:   authorityForInferencePool(pool),
				},
			},
		},
		ProcessingMode: &extprocv3.ProcessingMode{
			RequestHeaderMode:   extprocv3.ProcessingMode_SEND,
			RequestBodyMode:     processingBodyMode,
			RequestTrailerMode:  extprocv3.ProcessingMode_SEND,
			ResponseBodyMode:    processingBodyMode,
			ResponseHeaderMode:  extprocv3.ProcessingMode_SEND,
			ResponseTrailerMode: extprocv3.ProcessingMode_SEND,
		},
		AllowModeOverride: allowModeOverride,
		MessageTimeout:    durationpb.New(300 * time.Second),
		FailureModeAllow:  false,
	}
}

// getProcessingBodyModeFromAnnotations reads the processing body mode from InferencePool annotations.
// Returns FULL_DUPLEX_STREAMED for "duplex" (default) or BUFFERED for "buffered".
func getProcessingBodyModeFromAnnotations(pool *gwaiev1.InferencePool) extprocv3.ProcessingMode_BodySendMode {
	annotations := pool.GetAnnotations()
	if annotations == nil {
		return extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED // default to duplex
	}

	mode, exists := annotations[processingBodyModeAnnotation]
	if !exists {
		return extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED // default to duplex
	}

	switch mode {
	case "buffered":
		return extprocv3.ProcessingMode_BUFFERED
	case "duplex":
		return extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED
	default:
		// Invalid value, default to duplex
		return extprocv3.ProcessingMode_FULL_DUPLEX_STREAMED
	}
}

// getAllowModeOverrideFromAnnotations reads the allow mode override setting from InferencePool annotations.
// Returns false by default, true if annotation is set to "true".
func getAllowModeOverrideFromAnnotations(pool *gwaiev1.InferencePool) bool {
	annotations := pool.GetAnnotations()
	if annotations == nil {
		return false // default to false
	}

	value, exists := annotations[allowModeOverrideAnnotation]
	if !exists {
		return false // default to false
	}

	return value == "true"
}

// getProcessingBodyModeStringFromAnnotations reads the processing body mode from InferencePool annotations.
func getProcessingBodyModeStringFromAnnotations(pool *gwaiev1.InferencePool) string {
	annotations := pool.GetAnnotations()
	if annotations == nil {
		return "duplex" // default to duplex
	}

	// Only encode the values getProcessingBodyModeFromAnnotations understands, so a raw annotation
	// value can't add a "/" to the encoded metadata (see parseInferencePoolMetadataString).
	if annotations[processingBodyModeAnnotation] == "buffered" {
		return "buffered"
	}
	return "duplex"
}

// getAllowModeOverrideStringFromAnnotations reads the allow mode override setting from InferencePool annotations.
func getAllowModeOverrideStringFromAnnotations(pool *gwaiev1.InferencePool) string {
	annotations := pool.GetAnnotations()
	if annotations == nil {
		return "false" // default to false
	}

	// Only encode the values getAllowModeOverrideFromAnnotations understands, so a raw annotation
	// value can't add a "/" to the encoded metadata (see parseInferencePoolMetadataString).
	if annotations[allowModeOverrideAnnotation] == "true" {
		return "true"
	}
	return "false"
}

// authorityForInferencePool formats the gRPC authority based on the given InferencePool.
func authorityForInferencePool(pool *gwaiev1.InferencePool) string {
	ns := pool.GetNamespace()
	svc := pool.Spec.EndpointPickerRef.Name
	return fmt.Sprintf("%s.%s.svc:%d", svc, ns, portForInferencePool(pool))
}

// dnsNameForInferencePool formats the DNS name based on the given InferencePool.
func dnsNameForInferencePool(pool *gwaiev1.InferencePool) string {
	ns := pool.GetNamespace()
	svc := pool.Spec.EndpointPickerRef.Name
	return fmt.Sprintf("%s.%s.svc", svc, ns)
}

// portForInferencePool returns the port number for the given InferencePool.
func portForInferencePool(pool *gwaiev1.InferencePool) uint32 {
	if p := pool.Spec.EndpointPickerRef.Port; p == nil {
		return defaultEndpointPickerPort
	}
	portNumber := pool.Spec.EndpointPickerRef.Port.Number
	if portNumber < 0 || portNumber > 65535 {
		return defaultEndpointPickerPort // fallback to default port.
	}
	// Safe conversion: portNumber is validated to be in range [0, 65535].
	return uint32(portNumber) // #nosec G1151
}

// clusterNameForInferencePool returns the name of the ext_proc cluster for the given InferencePool.
func clusterNameForInferencePool(pool *gwaiev1.InferencePool) string {
	return fmt.Sprintf("envoy.clusters.endpointpicker_%s_%s_ext_proc", pool.GetName(), pool.GetNamespace())
}

// httpFilterNameForInferencePool returns the name of the ext_proc cluster for the given InferencePool.
func httpFilterNameForInferencePool(pool *gwaiev1.InferencePool) string {
	return fmt.Sprintf("envoy.filters.http.ext_proc/endpointpicker/%s_%s_ext_proc", pool.GetName(), pool.GetNamespace())
}

// Tries to find an HTTP connection manager in the provided filter chain.
func findHCM(filterChain *listenerv3.FilterChain) (*httpconnectionmanagerv3.HttpConnectionManager, int, error) {
	if filterChain == nil {
		return nil, -1, fmt.Errorf("filter chain is nil")
	}
	for filterIndex, filter := range filterChain.Filters {
		if filter.Name == wellknown.HTTPConnectionManager {
			hcm := new(httpconnectionmanagerv3.HttpConnectionManager)
			if err := filter.GetTypedConfig().UnmarshalTo(hcm); err != nil {
				return nil, -1, err
			}
			return hcm, filterIndex, nil
		}
	}
	return nil, -1, fmt.Errorf("unable to find HTTPConnectionManager in FilterChain: %s", filterChain.Name)
}

// Tries to find the inference pool ext proc filter in the provided chain.
func searchInferencePoolInFilterChain(pool *gwaiev1.InferencePool, chain []*httpconnectionmanagerv3.HttpFilter) (*extprocv3.ExternalProcessor, int, error) {
	for i, filter := range chain {
		if filter.Name == httpFilterNameForInferencePool(pool) {
			ep := new(extprocv3.ExternalProcessor)
			if err := filter.GetTypedConfig().UnmarshalTo(ep); err != nil {
				return nil, -1, err
			}
			return ep, i, nil
		}
	}
	return nil, -1, nil
}

// --- Weighted routing across multiple InferencePools ---
//
// A single HTTPRoute rule may list 2+ backendRefs, of which one or more are InferencePools (see
// site/docs/capabilities/inference/httproute-inferencepool.md's "Weighted Routing Across Multiple
// InferencePools" section for the user-facing behavior). Envoy Gateway translates such a rule into
// a WeightedClusters route action with one cluster per backendRef, carrying the backendRef weights,
// and drops the zero-weight backendRefs (envoyproxy/gateway#10270). Its extension hooks see:
//   - PostClusterModify, once per InferencePool cluster, with only that cluster's own pool, so each
//     cluster is set up for its own pool (handleInferencePoolCluster). Clusters of other backend
//     kinds, such as a Service, don't get the hook at all.
//   - PostRouteModify, once for the route, with every pool of the rule, so the route metadata names
//     all of them and patchVirtualHostWithInferencePool leaves every one of their EPP filters
//     enabled on the route.
//
// Left as is, every pool's endpoint picker would run on every request of the route, with whichever
// runs last deciding the endpoint regardless of the cluster Envoy picked. setInferencePoolClusterWeightFilters
// disables, on each ClusterWeight, the EPP filter of every pool of the route other than the
// cluster's own, so only the picked pool's endpoint picker scores and forwards the request. Envoy
// resolves a ClusterWeight's TypedPerFilterConfig as more specific than the Route's once it has
// picked a weighted cluster. The ClusterWeight's own pool gets no entry, since ExtProcPerRoute's
// "disabled" oneof is proto-constrained to only ever be true: there is no "force-enabled" override.

// setInferencePoolClusterWeightFilters disables, on every ClusterWeight of every route whose
// metadata names InferencePools, the EPP ext_proc filter of each of those pools that the
// ClusterWeight's cluster doesn't belong to. The pool of a cluster is read from the metadata
// PostClusterModify stamped on it (buildEPPMetadataForCluster); a cluster without one, such as a
// Service's or Envoy Gateway's "invalid-backend-cluster", gets every pool's filter disabled.
func setInferencePoolClusterWeightFilters(routeConfigs []*routev3.RouteConfiguration, clusters []*clusterv3.Cluster) {
	clusterPoolFilters := make(map[string]map[string]struct{})
	for _, c := range clusters {
		for _, pool := range getInferencePoolsByMetadata(c.Metadata) {
			if clusterPoolFilters[c.Name] == nil {
				clusterPoolFilters[c.Name] = make(map[string]struct{})
			}
			clusterPoolFilters[c.Name][httpFilterNameForInferencePool(pool)] = struct{}{}
		}
	}

	for _, rc := range routeConfigs {
		for _, vh := range rc.VirtualHosts {
			for _, route := range vh.Routes {
				wc := route.GetRoute().GetWeightedClusters()
				if wc == nil {
					continue
				}
				routePools := getInferencePoolsByMetadata(route.Metadata)
				if len(routePools) == 0 {
					continue
				}
				for _, cw := range wc.Clusters {
					own := clusterPoolFilters[cw.Name]
					for _, pool := range routePools {
						filterName := httpFilterNameForInferencePool(pool)
						if _, ok := own[filterName]; ok {
							// This ClusterWeight's own pool: leave its filter enabled by omitting an entry.
							continue
						}
						if cw.TypedPerFilterConfig == nil {
							cw.TypedPerFilterConfig = make(map[string]*anypb.Any, len(routePools))
						}
						cw.TypedPerFilterConfig[filterName] = disabledExtProcPerRoute()
					}
				}
			}
		}
	}
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
	// The pools of each route configuration, in route order, so the listener's EPP filters come
	// out in the same order on every translation and don't cause needless listener updates.
	routeNameToInferencePools := make(map[string][][]*gwaiev1.InferencePool)
	for _, routeCfg := range routes {
		routeNameToRoute[routeCfg.Name] = routeCfg
		for _, vh := range routeCfg.VirtualHosts {
			for _, route := range vh.Routes {
				if pools := getInferencePoolsByMetadata(route.Metadata); len(pools) > 0 {
					routeNameToInferencePools[routeCfg.Name] = append(routeNameToInferencePools[routeCfg.Name], pools)
				}
			}
		}
	}

	// listenerToInferencePools builds a matrix of listeners and the inference pools they use.
	// routeNameToInferencePools has an entry per route match, so a pool reached by
	// several matches appears several times. A pool needs exactly one ext proc filter per
	// listener, so deduplicate here.
	listenerToInferencePools := make(map[string][]*gwaiev1.InferencePool)
	for listener, routeCfgNames := range listenerNameToRouteNames {
		seen := make(map[string]struct{})
		for _, name := range routeCfgNames {
			if routeNameToRoute[name] == nil {
				continue
			}
			for _, pools := range routeNameToInferencePools[name] {
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
				s.patchVirtualHostWithInferencePool(vh, pools)
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

// disabledExtProcPerRouteValue is the marshaled ExtProcPerRoute that disables an ext_proc filter.
// It never changes, so it is marshaled once rather than for every route of every translation.
var disabledExtProcPerRouteValue = func() []byte {
	b, err := proto.Marshal(&extprocv3.ExtProcPerRoute{Override: &extprocv3.ExtProcPerRoute_Disabled{Disabled: true}})
	if err != nil {
		panic(fmt.Sprintf("failed to marshal disabled ExtProcPerRoute: %v", err))
	}
	return b
}()

// disabledExtProcPerRoute returns a new Any holding the ExtProcPerRoute that disables an ext_proc
// filter. Each call returns its own Any, so no proto is shared across translations.
func disabledExtProcPerRoute() *anypb.Any {
	return &anypb.Any{
		TypeUrl: "type.googleapis.com/" + string((&extprocv3.ExtProcPerRoute{}).ProtoReflect().Descriptor().FullName()),
		Value:   disabledExtProcPerRouteValue,
	}
}

// patchVirtualHostWithInferencePool adds the necessary per-route configuration to disable.
func (s *Server) patchVirtualHostWithInferencePool(vh *routev3.VirtualHost, inferencePools []*gwaiev1.InferencePool) {
	inferenceMatrix := make(map[string]*gwaiev1.InferencePool)
	for _, pool := range inferencePools {
		inferenceMatrix[httpFilterNameForInferencePool(pool)] = pool
	}
	for _, route := range vh.Routes {
		var overrideAny *anypb.Any
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
			if overrideAny == nil {
				overrideAny = disabledExtProcPerRoute()
			}
			route.TypedPerFilterConfig[key] = overrideAny
		}
	}
}
