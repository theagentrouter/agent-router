// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"fmt"
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
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

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
			unstructuredObj.GetKind() == "InferencePool" {
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

// getInferencePoolByMetadata returns the InferencePool from the cluster metadata.
func getInferencePoolByMetadata(meta *corev3.Metadata) *gwaiev1.InferencePool {
	var metadata string
	if meta != nil && meta.FilterMetadata != nil {
		m, ok := meta.FilterMetadata[internalapi.InternalEndpointMetadataNamespace]
		if ok && m.Fields != nil {
			v, ok := m.Fields[internalMetadataInferencePoolKey]
			if ok {
				metadata = v.GetStringValue()
			}
		}
	}

	result := strings.Split(metadata, "/")
	if len(result) != 6 {
		return nil
	}
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
			EndpointPickerRef: &gwaiev1.EndpointPickerRef{
				Name: gwaiev1.ObjectName(serviceName),
				Port: ptr.To(gwaiev1.Port{Number: gwaiev1.PortNumber(port)}),
			},
		},
	}
}

// buildMetadataForInferencePool adds InferencePool metadata to the cluster for reference by other components.
// encoded as a string in the format: "namespace/name/serviceName/port/bodyMode/allowModeOverride".
func buildEPPMetadataForCluster(cluster *clusterv3.Cluster, inferencePool *gwaiev1.InferencePool) {
	// Initialize cluster metadata structure if not present.
	if cluster.Metadata == nil {
		cluster.Metadata = &corev3.Metadata{}
	}
	buildEPPMetadata(cluster.Metadata, inferencePool)
}

// buildMetadataForInferencePool adds InferencePool metadata to the route for reference by other components.
func buildEPPMetadataForRoute(route *routev3.Route, inferencePool *gwaiev1.InferencePool) {
	// Initialize route metadata structure if not present.
	if route.Metadata == nil {
		route.Metadata = &corev3.Metadata{}
	}
	buildEPPMetadata(route.Metadata, inferencePool)
}

// buildEPPMetadata adds InferencePool metadata to the given metadata structure.
func buildEPPMetadata(metadata *corev3.Metadata, inferencePool *gwaiev1.InferencePool) {
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

	// Read processing body mode from annotations, default to "duplex" (FULL_DUPLEX_STREAMED)
	processingBodyMode := getProcessingBodyModeStringFromAnnotations(inferencePool)
	// Read allow mode override from annotations, default to false
	allowModeOverride := getAllowModeOverrideStringFromAnnotations(inferencePool)

	// Store InferencePool reference as metadata for later retrieval.
	// The reference includes all information needed to build EPP clusters and filters.
	m.Fields[internalMetadataInferencePoolKey] = structpb.NewStringValue(
		clusterRefInferencePool(
			inferencePool.Namespace,
			inferencePool.Name,
			string(inferencePool.Spec.EndpointPickerRef.Name),
			portForInferencePool(inferencePool),
			processingBodyMode,
			allowModeOverride,
		),
	)
}

// buildClustersForInferencePoolEndpointPickers builds and returns a "STRICT_DNS" cluster
// for each InferencePool's endpoint picker service.
func buildClustersForInferencePoolEndpointPickers(clusters []*clusterv3.Cluster) ([]*clusterv3.Cluster, error) {
	result := make([]*clusterv3.Cluster, 0, len(clusters))
	for _, cluster := range clusters {
		if pool := getInferencePoolByMetadata(cluster.Metadata); pool != nil {
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
		"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": anyHTTP2,
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
	const httpProtocolOptionsKey = "envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
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

	mode, exists := annotations[processingBodyModeAnnotation]
	if !exists {
		return "duplex" // default to duplex
	}

	return mode
}

// getAllowModeOverrideStringFromAnnotations reads the allow mode override setting from InferencePool annotations.
func getAllowModeOverrideStringFromAnnotations(pool *gwaiev1.InferencePool) string {
	annotations := pool.GetAnnotations()
	if annotations == nil {
		return "false" // default to false
	}

	value, exists := annotations[allowModeOverrideAnnotation]
	if !exists {
		return "false" // default to false
	}

	return value
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
