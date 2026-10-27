// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"bytes"
	"log/slog"
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	httpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestInsertAIGatewayExtProcFilter(t *testing.T) {
	tests := []struct {
		name                string
		existingFilters     []*httpconnectionmanagerv3.HttpFilter
		expectedPosition    int
		shouldPanic         bool
		expectedPanicMsg    string
		expectedFilterCount int
	}{
		{
			name:                "insert with only router filter",
			existingFilters:     []*httpconnectionmanagerv3.HttpFilter{{Name: "envoy.filters.http.router"}},
			expectedPosition:    0,
			expectedFilterCount: 2,
		},
		{
			name: "insert before router filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 3,
		},
		{
			name: "insert before extproc filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.ext_proc.existing"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before multiple extproc filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.ext_proc.existing"},
				{Name: "envoy.filters.http.ext_proc.existing.another"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 5,
		},
		{
			name: "insert before wasm filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.wasm"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before lua filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.lua"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before rbac filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.rbac"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before local_ratelimit filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.local_ratelimit"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before ratelimit filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.ratelimit"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before custom_response filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.custom_response"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before credential_injector filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.credential_injector"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert before compressor filter",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.compressor"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 4,
		},
		{
			name: "insert at end when only early filters present",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.cors"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    2,
			expectedFilterCount: 4,
		},
		{
			name: "insert with multiple filters requiring ordering",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.fault"},
				{Name: "envoy.filters.http.cors"},
				{Name: "envoy.filters.http.ext_proc.other"},
				{Name: "envoy.filters.http.rbac"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    2,
			expectedFilterCount: 6,
		},
		{
			// Mirrors the EKS setup where an api-key ext_proc and a buffer filter are added ahead of AI
			// Gateway. The ext_proc at index 0 matches afterExtProcFilterPrefixes, but the buffer filter
			// must still run first so its larger request buffer limit applies to AI Gateway's BUFFERED
			// extproc. AI Gateway is inserted after the buffer filter (position 2).
			name: "insert after buffer when ext_proc precedes buffer",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.ext_proc.apikey"},
				{Name: "envoy.filters.http.buffer"},
				{Name: "envoy.filters.http.jwt_authn"},
				{Name: "envoy.filters.http.rbac"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    2,
			expectedFilterCount: 6,
		},
		{
			// When the buffer filter already precedes the first ext_proc filter, AI Gateway is inserted
			// right after the buffer filter (position 1), preserving Envoy Gateway's buffer-before-extproc
			// ordering.
			name: "insert after buffer when buffer precedes ext_proc",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.buffer"},
				{Name: "envoy.filters.http.ext_proc.apikey"},
				{Name: "envoy.filters.http.rbac"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    1,
			expectedFilterCount: 5,
		},
		{
			// Regression guard: with no buffer filter present, insertion behavior is unchanged and AI
			// Gateway lands ahead of the first ext_proc filter (position 0).
			name: "no buffer filter leaves ext_proc insertion unchanged",
			existingFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: "envoy.filters.http.ext_proc.apikey"},
				{Name: "envoy.filters.http.rbac"},
				{Name: "envoy.filters.http.router"},
			},
			expectedPosition:    0,
			expectedFilterCount: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &httpconnectionmanagerv3.HttpConnectionManager{
				HttpFilters: make([]*httpconnectionmanagerv3.HttpFilter, len(tt.existingFilters)),
			}
			copy(mgr.HttpFilters, tt.existingFilters)

			newFilter := &httpconnectionmanagerv3.HttpFilter{
				Name:       aiGatewayExtProcName,
				ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: &anypb.Any{}},
			}

			err := insertAIGatewayExtProcFilter(mgr, newFilter)
			require.NoError(t, err)

			require.Len(t, mgr.HttpFilters, tt.expectedFilterCount)
			require.Equal(t, aiGatewayExtProcName, mgr.HttpFilters[tt.expectedPosition].Name)

			for i, originalFilter := range tt.existingFilters {
				if i < tt.expectedPosition {
					require.Equal(t, originalFilter.Name, mgr.HttpFilters[i].Name, "filter at position %d should be preserved", i)
				} else {
					require.Equal(t, originalFilter.Name, mgr.HttpFilters[i+1].Name, "filter at position %d should be shifted by 1", i)
				}
			}
		})
	}
}

func TestInsertHeaderToMetadataFilter(t *testing.T) {
	hcm := &httpconnectionmanagerv3.HttpConnectionManager{
		HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: wellknown.Router}},
	}
	filter, err := buildHeaderToMetadataFilter(map[string]string{"agent-session-id": "session.id"})
	require.NoError(t, err)
	err = insertHeaderToMetadataFilter(hcm, filter)
	require.NoError(t, err)
	require.Len(t, hcm.HttpFilters, 2)
	require.Equal(t, headerToMetadataFilterName, hcm.HttpFilters[0].Name)
	require.Equal(t, wellknown.Router, hcm.HttpFilters[1].Name)
}

func TestServer_isRouteGeneratedByAIGateway(t *testing.T) {
	emptyStruct, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)

	structWithEmptyResources, err := structpb.NewStruct(map[string]any{
		"resources": nil,
	})
	require.NoError(t, err)

	withAnnotationsListStruct, err := structpb.NewStruct(map[string]any{
		"resources": []any{
			map[string]any{
				"annotations": map[string]any{},
			},
		},
	})
	require.NoError(t, err)

	withOKAnnotationsListStruct, err := structpb.NewStruct(map[string]any{
		"resources": []any{
			map[string]any{
				"annotations": map[string]any{
					internalapi.AIGatewayGeneratedHTTPRouteAnnotation: "true",
				},
			},
		},
	})
	require.NoError(t, err)

	for _, tt := range []struct {
		name     string
		route    *routev3.Route
		expected bool
	}{
		{
			name:     "no metadata",
			route:    &routev3.Route{},
			expected: false,
		},
		{
			name: "no metadata.Fields",
			route: &routev3.Route{
				Metadata: &corev3.Metadata{},
			},
			expected: false,
		},
		{
			name: "no metadata.Fields 'envoy-ai_gateway'",
			route: &routev3.Route{
				Metadata: &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{}},
			},
			expected: false,
		},
		{
			name: "no resources in metadata.Fields 'envoy-gateway'",
			route: &routev3.Route{
				Metadata: &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
					"envoy-gateway": emptyStruct,
				}},
			},
			expected: false,
		},
		{
			name: "resources do not have annotations",
			route: &routev3.Route{
				Metadata: &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
					"envoy-gateway": structWithEmptyResources,
				}},
			},
			expected: false,
		},
		{
			name: "annotations are empty",
			route: &routev3.Route{
				Metadata: &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
					"envoy-gateway": withAnnotationsListStruct,
				}},
			},
			expected: false,
		},
		{
			name: "annotations are empty",
			route: &routev3.Route{
				Metadata: &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
					"envoy-gateway": withOKAnnotationsListStruct,
				}},
			},
			expected: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{log: zap.New()}
			result := s.isRouteGeneratedByAIGateway(tt.route)
			require.Equal(t, tt.expected, result)
		})
	}
}

func Test_shouldAIGatewayExtProcBeInserted(t *testing.T) {
	tests := []struct {
		name     string
		filters  []*httpconnectionmanagerv3.HttpFilter
		expected bool
	}{
		{
			filters:  []*httpconnectionmanagerv3.HttpFilter{{}},
			expected: true,
		},
		{
			filters:  []*httpconnectionmanagerv3.HttpFilter{{Name: aiGatewayExtProcName}},
			expected: false,
		},
		{
			filters:  []*httpconnectionmanagerv3.HttpFilter{{}, {Name: aiGatewayExtProcName}, {}},
			expected: false,
		},
		{
			filters:  []*httpconnectionmanagerv3.HttpFilter{{}, {}},
			expected: true,
		},
	}

	for _, tt := range tests {
		result := shouldAIGatewayExtProcBeInserted(tt.filters)
		require.Equal(t, tt.expected, result)
	}
}

func TestServer_insertRouterLevelAIGatewayExtProc_setsSchemeHeaderTransformation(t *testing.T) {
	hcm := &httpconnectionmanagerv3.HttpConnectionManager{
		HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: wellknown.Router}},
	}
	listener := &listenerv3.Listener{
		DefaultFilterChain: &listenerv3.FilterChain{
			Filters: []*listenerv3.Filter{
				{
					Name:       wellknown.HTTPConnectionManager,
					ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, hcm)},
				},
			},
		},
	}
	s := &Server{log: zap.New()}
	require.NoError(t, s.insertRouterLevelAIGatewayExtProc(listener))

	updatedHCM, _, err := findHCM(listener.DefaultFilterChain)
	require.NoError(t, err)
	require.True(t, updatedHCM.GetSchemeHeaderTransformation().GetMatchUpstream(),
		"SchemeHeaderTransformation.MatchUpstream must be true so :scheme matches upstream TLS transport")
}

func Test_findListenerRouteConfigs(t *testing.T) {
	newHCM := func(name string) *httpconnectionmanagerv3.HttpConnectionManager {
		return &httpconnectionmanagerv3.HttpConnectionManager{
			RouteSpecifier: &httpconnectionmanagerv3.HttpConnectionManager_Rds{
				Rds: &httpconnectionmanagerv3.Rds{RouteConfigName: name},
			},
		}
	}
	l := &listenerv3.Listener{
		DefaultFilterChain: &listenerv3.FilterChain{
			Filters: []*listenerv3.Filter{
				{
					Name:       wellknown.HTTPConnectionManager,
					ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, newHCM("foo"))},
				},
			},
		},
		FilterChains: []*listenerv3.FilterChain{
			{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: mustToAny(t, newHCM("bar"))},
					},
				},
			},
			// Non-HCM filter chain.
			{Filters: []*listenerv3.Filter{}},
		},
	}
	names := findListenerRouteConfigs(l)
	require.ElementsMatch(t, []string{"foo", "bar"}, names)
}

// extProcFilterConfig returns the upstream ai-gateway ext_proc configuration of the cluster.
func extProcFilterConfig(t *testing.T, cluster *clusterv3.Cluster) *extprocv3.ExternalProcessor {
	t.Helper()
	po := &httpv3.HttpProtocolOptions{}
	require.NoError(t, cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"].UnmarshalTo(po))
	for _, f := range po.HttpFilters {
		if f.Name == aiGatewayExtProcName {
			cfg := &extprocv3.ExternalProcessor{}
			require.NoError(t, f.GetTypedConfig().UnmarshalTo(cfg))
			return cfg
		}
	}
	t.Fatal("upstream ext_proc filter not found")
	return nil
}

// resourceMetadata builds the metadata Envoy Gateway stamps onto a virtual host, naming the
// Gateway API objects it came from.
func resourceMetadata(resources ...map[string]string) *corev3.Metadata {
	values := make([]*structpb.Value, 0, len(resources))
	for _, resource := range resources {
		fields := make(map[string]*structpb.Value, len(resource))
		for k, v := range resource {
			fields[k] = structpb.NewStringValue(v)
		}
		values = append(values, structpb.NewStructValue(&structpb.Struct{Fields: fields}))
	}
	return &corev3.Metadata{FilterMetadata: map[string]*structpb.Struct{
		"envoy-gateway": {Fields: map[string]*structpb.Value{
			"resources": structpb.NewListValue(&structpb.ListValue{Values: values}),
		}},
	}}
}

// snapshotOf builds the route configurations of a snapshot owned by the named Gateways, all in
// namespace "ns", each on its own virtual host as Envoy Gateway produces them.
func snapshotOf(gatewayNames ...string) []*routev3.RouteConfiguration {
	vhosts := make([]*routev3.VirtualHost, 0, len(gatewayNames))
	for _, name := range gatewayNames {
		vhosts = append(vhosts, &routev3.VirtualHost{
			Name:     name + "/example_com",
			Metadata: resourceMetadata(map[string]string{"kind": "Gateway", "namespace": "ns", "name": name}),
		})
	}
	return []*routev3.RouteConfiguration{{Name: "route-config", VirtualHosts: vhosts}}
}

func newMetadataForwardingServer(t *testing.T) (*Server, client.Client) {
	t.Helper()
	c := newFakeClient()
	newGateway := func(name, configName string) *gwapiv1.Gateway {
		gw := &gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg"},
		}
		if configName != "" {
			gw.Annotations = map[string]string{gatewayConfigAnnotationKey: configName}
		}
		return gw
	}
	newGatewayConfig := func(name string, namespaces ...string) *aigv1b1.GatewayConfig {
		return &aigv1b1.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec: aigv1b1.GatewayConfigSpec{
				ExtProc: &aigv1b1.GatewayConfigExtProc{MetadataForwardingNamespaces: namespaces},
			},
		}
	}
	newRoute := func(name string, parents ...string) *aigv1b1.AIGatewayRoute {
		route := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "some-backend"},
						{Name: "plain-backend"},
					}},
				},
			},
		}
		for _, parent := range parents {
			route.Spec.ParentRefs = append(route.Spec.ParentRefs, gwapiv1.ParentReference{Name: gwapiv1.ObjectName(parent)})
		}
		return route
	}
	for _, obj := range []client.Object{
		newGateway("eg-gateway", "gwconfig"),
		newGatewayConfig("gwconfig", "envoy.filters.http.ext_authz"),
		newGateway("other-gateway", "other-gwconfig"),
		newGatewayConfig("other-gwconfig", "other.ns", "envoy.filters.http.ext_authz"),
		newGateway("plain-gateway", ""),
		newRoute("myroute", "eg-gateway"),
		newRoute("plainroute", "plain-gateway"),
		// Attached to a Gateway that declares and one that does not.
		newRoute("sharedroute", "eg-gateway", "plain-gateway"),
	} {
		require.NoError(t, c.Create(t.Context(), obj))
	}
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	return s, c
}

// The declaration is the Gateway's, so it comes from the Gateways owning the snapshot.
func Test_metadataForwardingNamespacesForSnapshot(t *testing.T) {
	s, _ := newMetadataForwardingServer(t)

	t.Run("the snapshot's Gateway decides", func(t *testing.T) {
		got, err := s.metadataForwardingNamespacesForSnapshot(t.Context(), snapshotOf("eg-gateway"))
		require.NoError(t, err)
		require.Equal(t, []string{"envoy.filters.http.ext_authz"}, got)
	})

	t.Run("a Gateway with no GatewayConfig declares nothing", func(t *testing.T) {
		got, err := s.metadataForwardingNamespacesForSnapshot(t.Context(), snapshotOf("plain-gateway"))
		require.NoError(t, err)
		require.Empty(t, got)
	})

	// mergeGateways puts several Gateways behind one Envoy, so their declarations combine.
	t.Run("merged Gateways combine, sorted and deduped", func(t *testing.T) {
		got, err := s.metadataForwardingNamespacesForSnapshot(t.Context(), snapshotOf("eg-gateway", "other-gateway"))
		require.NoError(t, err)
		require.Equal(t, []string{"envoy.filters.http.ext_authz", "other.ns"}, got)
	})

	t.Run("non-Gateway resources are ignored", func(t *testing.T) {
		routes := []*routev3.RouteConfiguration{{VirtualHosts: []*routev3.VirtualHost{{
			Metadata: resourceMetadata(
				map[string]string{"kind": "HTTPRoute", "namespace": "ns", "name": "other-gateway"},
				map[string]string{"kind": "Gateway", "namespace": "ns", "name": "eg-gateway"},
			),
		}}}}
		got, err := s.metadataForwardingNamespacesForSnapshot(t.Context(), routes)
		require.NoError(t, err)
		require.Equal(t, []string{"envoy.filters.http.ext_authz"}, got)
	})

	// Nothing identifying the Gateway means nothing is forwarded, logged rather than silent.
	t.Run("an unidentifiable snapshot forwards nothing and says so", func(t *testing.T) {
		var buf bytes.Buffer
		logged, err := New(newFakeClient(), logr.FromSlogHandler(slog.NewTextHandler(&buf, nil)), udsPath,
			false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
		require.NoError(t, err)

		got, err := logged.metadataForwardingNamespacesForSnapshot(t.Context(),
			[]*routev3.RouteConfiguration{{VirtualHosts: []*routev3.VirtualHost{{Name: "example_com"}}}})
		require.NoError(t, err)
		require.Empty(t, got)
		require.Contains(t, buf.String(), "cannot tell which Gateway this xDS snapshot belongs to")
	})
}

func Test_maybeModifyCluster_forwardsDeclaredMetadataNamespaces(t *testing.T) {
	s, _ := newMetadataForwardingServer(t)

	modify := func(t *testing.T, clusterName string, gatewayNames ...string) *extprocv3.ExternalProcessor {
		t.Helper()
		namespaces, err := s.metadataForwardingNamespacesForSnapshot(t.Context(), snapshotOf(gatewayNames...))
		require.NoError(t, err)
		cluster := &clusterv3.Cluster{Name: clusterName}
		require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, namespaces))
		return extProcFilterConfig(t, cluster)
	}

	t.Run("declared namespaces reach every cluster of the route", func(t *testing.T) {
		for _, name := range []string{"httproute/ns/myroute/rule/0", "httproute/ns/myroute/rule/0/backend/1"} {
			cfg := modify(t, name, "eg-gateway")
			require.Equal(t, []string{aigv1b1.AIGatewayFilterMetadataNamespace},
				cfg.MetadataOptions.GetReceivingNamespaces().GetUntyped())
			require.Equal(t, []string{"envoy.filters.http.ext_authz"},
				cfg.MetadataOptions.GetForwardingNamespaces().GetUntyped())
		}
	})

	t.Run("no GatewayConfig means no forwarding namespaces", func(t *testing.T) {
		require.Nil(t, modify(t, "httproute/ns/plainroute/rule/0", "plain-gateway").
			MetadataOptions.GetForwardingNamespaces())
	})

	// The same cluster name reaches every parent's snapshot, so a route must not widen a Gateway.
	t.Run("a route parent does not widen another Gateway", func(t *testing.T) {
		require.Equal(t, []string{"envoy.filters.http.ext_authz"},
			modify(t, "httproute/ns/sharedroute/rule/0", "eg-gateway").
				MetadataOptions.GetForwardingNamespaces().GetUntyped())

		require.Nil(t, modify(t, "httproute/ns/sharedroute/rule/0", "plain-gateway").
			MetadataOptions.GetForwardingNamespaces())
	})
}

// A cluster handed back with our filters on it must end up with the current forwarding
// namespaces and no duplicates.
func Test_maybeModifyCluster_rebuildsOwnFiltersOnExistingChain(t *testing.T) {
	c := newFakeClient()
	require.NoError(t, c.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "ns"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Name: "eg-gateway"}},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "some-backend"}}},
			},
		},
	}))
	require.NoError(t, c.Create(t.Context(), &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "eg-gateway",
			Namespace:   "ns",
			Annotations: map[string]string{gatewayConfigAnnotationKey: "gwconfig"},
		},
		Spec: gwapiv1.GatewaySpec{GatewayClassName: "eg"},
	}))
	require.NoError(t, c.Create(t.Context(), &aigv1b1.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "gwconfig", Namespace: "ns"},
		Spec: aigv1b1.GatewayConfigSpec{
			ExtProc: &aigv1b1.GatewayConfigExtProc{
				MetadataForwardingNamespaces: []string{"envoy.filters.http.ext_authz"},
			},
		},
	}))
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)

	// A chain as an earlier pass left it: our two filters, no forwarding namespaces.
	stale, err := toAny(&extprocv3.ExternalProcessor{
		MetadataOptions: &extprocv3.MetadataOptions{
			ReceivingNamespaces: &extprocv3.MetadataOptions_MetadataNamespaces{
				Untyped: []string{aigv1b1.AIGatewayFilterMetadataNamespace},
			},
		},
	})
	require.NoError(t, err)
	poAny, err := toAny(&httpv3.HttpProtocolOptions{
		HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
			{Name: aiGatewayExtProcName, ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: stale}},
			{Name: aiGatewayHeaderMutationName},
			{Name: "envoy.filters.http.upstream_codec"},
		},
	})
	require.NoError(t, err)
	cluster := &clusterv3.Cluster{
		Name: "httproute/ns/myroute/rule/0",
		TypedExtensionProtocolOptions: map[string]*anypb.Any{
			"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": poAny,
		},
	}

	require.NoError(t, s.maybeModifyCluster(t.Context(), cluster, []string{"envoy.filters.http.ext_authz"}))

	cfg := extProcFilterConfig(t, cluster)
	require.Equal(t, []string{"envoy.filters.http.ext_authz"},
		cfg.MetadataOptions.GetForwardingNamespaces().GetUntyped())
	require.Equal(t, []string{aigv1b1.AIGatewayFilterMetadataNamespace},
		cfg.MetadataOptions.GetReceivingNamespaces().GetUntyped())

	po := &httpv3.HttpProtocolOptions{}
	require.NoError(t, cluster.TypedExtensionProtocolOptions["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"].UnmarshalTo(po))
	var names []string
	for _, f := range po.HttpFilters {
		names = append(names, f.GetName())
	}
	require.Equal(t, []string{aiGatewayExtProcName, aiGatewayHeaderMutationName, "envoy.filters.http.upstream_codec"}, names)
}
