// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	ratelimitfilterv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ratelimit/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/translator"
)

func TestBuildQuotaRateLimitFilter(t *testing.T) {
	srv := &Server{
		quotaRateLimitTimeout:         5,
		quotaRateLimitFailureModeDeny: false,
	}
	domain := "test-domain"
	filter, err := srv.buildQuotaRateLimitFilter(domain)
	require.NoError(t, err)
	require.NotNil(t, filter)
	require.Equal(t, quotaRateLimitFilterName, filter.Name)

	// Unmarshal and verify the filter config.
	cfg := &ratelimitfilterv3.RateLimit{}
	require.NoError(t, filter.GetTypedConfig().UnmarshalTo(cfg))
	require.Equal(t, domain, cfg.Domain)
	require.Equal(t, quotaRateLimitClusterName, cfg.RateLimitService.GrpcService.GetEnvoyGrpc().ClusterName)
	require.Equal(t, corev3.ApiVersion_V3, cfg.RateLimitService.TransportApiVersion)
	require.Equal(t, &durationpb.Duration{Seconds: 5}, cfg.Timeout)
	require.False(t, cfg.FailureModeDeny)
	require.True(t, cfg.DisableXEnvoyRatelimitedHeader)
	require.Equal(t, ratelimitfilterv3.RateLimit_DRAFT_VERSION_03, cfg.EnableXRatelimitHeaders)
	require.False(t, cfg.RateLimitedAsResourceExhausted)
}

func TestBuildQuotaRateLimitCluster(t *testing.T) {
	t.Run("default port", func(t *testing.T) {
		srv := &Server{
			quotaRateLimitServiceHost: "envoy-ai-gateway-ratelimit.envoy-gateway-system",
			quotaRateLimitServicePort: defaultQuotaRateLimitServicePort,
		}
		cluster := srv.buildQuotaRateLimitCluster()
		require.Equal(t, quotaRateLimitClusterName, cluster.Name)
		require.Equal(t, clusterv3.Cluster_STRICT_DNS, cluster.GetType())
		require.Equal(t, &durationpb.Duration{Seconds: 5}, cluster.ConnectTimeout)
		require.NotNil(t, cluster.LoadAssignment)
		require.Len(t, cluster.LoadAssignment.Endpoints, 1)
		require.Len(t, cluster.LoadAssignment.Endpoints[0].LbEndpoints, 1)

		ep := cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].GetEndpoint()
		require.Equal(t, "envoy-ai-gateway-ratelimit.envoy-gateway-system", ep.Address.GetSocketAddress().Address)
		require.Equal(t, uint32(defaultQuotaRateLimitServicePort), ep.Address.GetSocketAddress().GetPortValue())
	})

	t.Run("custom port", func(t *testing.T) {
		srv := &Server{
			quotaRateLimitServiceHost: "custom-ratelimit",
			quotaRateLimitServicePort: 9090,
		}
		cluster := srv.buildQuotaRateLimitCluster()
		ep := cluster.LoadAssignment.Endpoints[0].LbEndpoints[0].GetEndpoint()
		require.Equal(t, "custom-ratelimit", ep.Address.GetSocketAddress().Address)
		require.Equal(t, uint32(9090), ep.Address.GetSocketAddress().GetPortValue())
	})
}

func TestInjectQuotaRateLimitFilterIntoListeners(t *testing.T) {
	srv := &Server{
		quotaRateLimitTimeout:         5,
		quotaRateLimitFailureModeDeny: false,
	}

	buildTestListener := func(t *testing.T, httpFilters []*httpconnectionmanagerv3.HttpFilter) *listenerv3.Listener {
		t.Helper()
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: httpFilters,
		}
		hcmAny := mustToAny(t, hcm)
		return &listenerv3.Listener{
			Name: "test-listener",
			FilterChains: []*listenerv3.FilterChain{
				{
					Filters: []*listenerv3.Filter{
						{
							Name:       wellknown.HTTPConnectionManager,
							ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
						},
					},
				},
			},
		}
	}

	getHCMFilters := func(t *testing.T, ln *listenerv3.Listener) []*httpconnectionmanagerv3.HttpFilter {
		t.Helper()
		hcm, _, err := findHCM(ln.FilterChains[0])
		require.NoError(t, err)
		return hcm.HttpFilters
	}

	t.Run("injects filter before router", func(t *testing.T) {
		ln := buildTestListener(t, []*httpconnectionmanagerv3.HttpFilter{
			{Name: "envoy.filters.http.health_check"},
			{Name: wellknown.Router},
		})

		require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))

		filters := getHCMFilters(t, ln)
		require.Len(t, filters, 3)
		require.Equal(t, "envoy.filters.http.health_check", filters[0].Name)
		require.Equal(t, quotaRateLimitFilterName, filters[1].Name)
		require.False(t, filters[1].Disabled)
		require.Equal(t, wellknown.Router, filters[2].Name)

		// Verify the injected filter config.
		rlCfg := &ratelimitfilterv3.RateLimit{}
		require.NoError(t, filters[1].GetTypedConfig().UnmarshalTo(rlCfg))
		require.Equal(t, translator.QuotaDomain, rlCfg.Domain)
		require.Equal(t, quotaRateLimitClusterName, rlCfg.RateLimitService.GrpcService.GetEnvoyGrpc().ClusterName)
		require.Equal(t, corev3.ApiVersion_V3, rlCfg.RateLimitService.TransportApiVersion)
	})

	t.Run("filter already exists is a no-op", func(t *testing.T) {
		existingFilter, err := srv.buildQuotaRateLimitFilter(translator.QuotaDomain)
		require.NoError(t, err)

		ln := buildTestListener(t, []*httpconnectionmanagerv3.HttpFilter{
			existingFilter,
			{Name: wellknown.Router},
		})

		require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))

		filters := getHCMFilters(t, ln)
		require.Len(t, filters, 2)
	})

	t.Run("appends if no router filter found", func(t *testing.T) {
		ln := buildTestListener(t, []*httpconnectionmanagerv3.HttpFilter{
			{Name: "envoy.filters.http.health_check"},
		})

		require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))

		filters := getHCMFilters(t, ln)
		require.Len(t, filters, 2)
		require.Equal(t, quotaRateLimitFilterName, filters[1].Name)
	})

	t.Run("idempotent on repeated calls", func(t *testing.T) {
		ln := buildTestListener(t, []*httpconnectionmanagerv3.HttpFilter{
			{Name: wellknown.Router},
		})

		require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))
		require.Len(t, getHCMFilters(t, ln), 2)

		require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))
		require.Len(t, getHCMFilters(t, ln), 2)
	})

	t.Run("skips listeners without HCM", func(t *testing.T) {
		ln := &listenerv3.Listener{
			Name: "non-hcm",
			FilterChains: []*listenerv3.FilterChain{
				{
					Filters: []*listenerv3.Filter{
						{Name: "envoy.filters.network.tcp_proxy"},
					},
				},
			},
		}
		require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))
	})
}

func TestEnableQuotaRateLimitOnRoute(t *testing.T) {
	testPolicies := []aigv1a1.QuotaPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "be"}},
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		},
	}

	t.Run("sets per-route rate limit config", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, testPolicies, nil))
		require.NotNil(t, route.TypedPerFilterConfig)
		require.Contains(t, route.TypedPerFilterConfig, quotaRateLimitFilterName)

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))
		require.Equal(t, translator.QuotaDomain, perRoute.Domain)
		// 1 request-time + 1 stream-done (appended at end for unique model).
		require.Len(t, perRoute.RateLimits, 2)
	})

	t.Run("nil policies returns nil", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, nil, nil))
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("stream-done entry reads backend_name from metadata", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, testPolicies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Stream-done entry is at the end (index 1, after the 1 request-time entry).
		backendAction := perRoute.RateLimits[1].Actions[0]
		md := backendAction.GetMetadata()
		require.NotNil(t, md)
		require.Equal(t, translator.BackendNameDescriptorKey, md.DescriptorKey)
		require.Equal(t, aigv1b1.AIGatewayFilterMetadataNamespace, md.MetadataKey.Key)
		require.Equal(t, "ai_service_backend_name", md.MetadataKey.Path[0].GetKey())
		require.Equal(t, routev3.RateLimit_Action_MetaData_DYNAMIC, md.Source)
	})

	t.Run("request-time entry uses GenericKey for backend and model", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, testPolicies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		reqTime := perRoute.RateLimits[0]
		require.Equal(t, "default/be", reqTime.Actions[0].GetGenericKey().DescriptorValue)
		require.Equal(t, "gpt-4", reqTime.Actions[1].GetGenericKey().DescriptorValue)
	})

	t.Run("preserves existing TypedPerFilterConfig entries", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test-route",
			TypedPerFilterConfig: map[string]*anypb.Any{
				"some-other-filter": {},
			},
		}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, testPolicies, nil))
		require.Len(t, route.TypedPerFilterConfig, 2)
		require.Contains(t, route.TypedPerFilterConfig, "some-other-filter")
		require.Contains(t, route.TypedPerFilterConfig, quotaRateLimitFilterName)
	})
}

func TestBuildQuotaBackendPolicies(t *testing.T) {
	t.Run("empty policies", func(t *testing.T) {
		result := buildQuotaBackendPolicies(nil)
		require.Empty(t, result)
	})

	t.Run("single policy single target", func(t *testing.T) {
		policies := []aigv1a1.QuotaPolicy{
			{
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "backend-a"},
					},
				},
			},
		}
		policies[0].Namespace = "default"
		result := buildQuotaBackendPolicies(policies)
		require.Len(t, result, 1)
		require.Contains(t, result, "default/backend-a")
		require.Len(t, result["default/backend-a"], 1)
	})

	t.Run("multiple policies multiple targets", func(t *testing.T) {
		policies := []aigv1a1.QuotaPolicy{
			{
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "backend-a"},
						{Name: "backend-b"},
					},
				},
			},
			{
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "backend-c"},
					},
				},
			},
		}
		policies[0].Namespace = "ns1"
		policies[1].Namespace = "ns2"
		result := buildQuotaBackendPolicies(policies)
		require.Len(t, result, 3)
		require.Contains(t, result, "ns1/backend-a")
		require.Contains(t, result, "ns1/backend-b")
		require.Contains(t, result, "ns2/backend-c")
	})

	t.Run("same backend collects multiple policies", func(t *testing.T) {
		policies := []aigv1a1.QuotaPolicy{
			{
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "backend-a"},
					},
				},
			},
			{
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "backend-a"},
					},
				},
			},
		}
		policies[0].Namespace = "default"
		policies[1].Namespace = "default"
		result := buildQuotaBackendPolicies(policies)
		require.Len(t, result, 1)
		require.Contains(t, result, "default/backend-a")
		require.Len(t, result["default/backend-a"], 2)
	})
}

// verifyMetadataAction is a helper that asserts a rate limit action is a MetaData action
// with the expected descriptor key, metadata namespace, metadata path key, and source.
func verifyMetadataAction(t *testing.T, action *routev3.RateLimit_Action, descriptorKey, metadataPathKey string) {
	t.Helper()
	md := action.GetMetadata()
	require.NotNil(t, md, "expected Metadata action specifier")
	require.Equal(t, descriptorKey, md.DescriptorKey)
	require.Equal(t, aigv1b1.AIGatewayFilterMetadataNamespace, md.MetadataKey.Key)
	require.Len(t, md.MetadataKey.Path, 1)
	require.Equal(t, metadataPathKey, md.MetadataKey.Path[0].GetKey())
	require.Equal(t, routev3.RateLimit_Action_MetaData_DYNAMIC, md.Source)
}

func TestEnableQuotaRateLimitOnRoute_DescriptorChain(t *testing.T) {
	route := &routev3.Route{Name: "test-route"}
	policies := []aigv1a1.QuotaPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "test-backend"},
				},
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		},
	}
	require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

	perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
	require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

	// Simple case (no bucket rules): 1 request-time + 1 stream-done (appended at end).
	require.Len(t, perRoute.RateLimits, 2)

	// Request-time entry: GenericKey(backend_name) + GenericKey(model_name).
	reqTime := perRoute.RateLimits[0]
	require.Len(t, reqTime.Actions, 2)
	require.Equal(t, translator.BackendNameDescriptorKey, reqTime.Actions[0].GetGenericKey().DescriptorKey)
	require.Equal(t, "default/test-backend", reqTime.Actions[0].GetGenericKey().DescriptorValue)
	require.Equal(t, translator.ModelNameDescriptorKey, reqTime.Actions[1].GetGenericKey().DescriptorKey)
	require.Equal(t, "gpt-4", reqTime.Actions[1].GetGenericKey().DescriptorValue)

	// Stream-done entry (appended at end): Metadata(backend_name) + Metadata(model_name).
	streamDone := perRoute.RateLimits[1]
	require.Len(t, streamDone.Actions, 2)
	verifyMetadataAction(t, streamDone.Actions[0], translator.BackendNameDescriptorKey, "ai_service_backend_name")
	verifyMetadataAction(t, streamDone.Actions[1], translator.ModelNameDescriptorKey, "model_name_override")
	require.True(t, streamDone.ApplyOnStreamDone)
	require.NotNil(t, streamDone.HitsAddend)
}

func TestQuotaHitsAddend(t *testing.T) {
	ha := quotaHitsAddend()
	require.NotNil(t, ha)
	expectedFormat := fmt.Sprintf("%%DYNAMIC_METADATA(%s:%s)%%", aigv1b1.AIGatewayFilterMetadataNamespace, quotaCostMetadataKey)
	require.Equal(t, expectedFormat, ha.Format)
}

func TestEnableQuotaRateLimitOnRoute_HitsAddend(t *testing.T) {
	t.Run("nil policies returns nil without patching route", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, nil, nil))
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("bucket rule entries have request-time followed by stream-done at end", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "test-backend"},
					},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 10, Duration: "1m"},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// 1 bucket req-time + 1 default req-time + 2 stream-done at end = 4 entries.
		require.Len(t, perRoute.RateLimits, 4)
		// First 2 entries are request-time without HitsAddend.
		for i := 0; i < 2; i++ {
			rl := perRoute.RateLimits[i]
			require.Nil(t, rl.HitsAddend, "RateLimit entry %d should not have HitsAddend", i)
			require.False(t, rl.ApplyOnStreamDone, "RateLimit entry %d should not have ApplyOnStreamDone", i)
		}
		// Last 2 entries are stream-done with HitsAddend.
		for i := 2; i < 4; i++ {
			rl := perRoute.RateLimits[i]
			require.NotNil(t, rl.HitsAddend, "RateLimit entry %d should have HitsAddend", i)
			require.NotEmpty(t, rl.HitsAddend.Format, "RateLimit entry %d should have HitsAddend format", i)
			require.True(t, rl.ApplyOnStreamDone, "RateLimit entry %d should have ApplyOnStreamDone", i)
		}
	})
}

func TestInjectQuotaRateLimitFilterIntoListeners_FullHCMChain(t *testing.T) {
	srv := &Server{
		quotaRateLimitTimeout:         5,
		quotaRateLimitFailureModeDeny: false,
	}

	// Simulate the typical HCM filter chain: health_check, header_to_metadata, router.
	// After injection, the ratelimit filter should be inserted before the router.
	hcm := &httpconnectionmanagerv3.HttpConnectionManager{
		HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
			{Name: "envoy.filters.http.health_check"},
			{Name: "envoy.filters.http.header_to_metadata"},
			{Name: wellknown.Router},
		},
	}
	hcmAny := mustToAny(t, hcm)
	ln := &listenerv3.Listener{
		Name: "test-listener",
		FilterChains: []*listenerv3.FilterChain{
			{
				Filters: []*listenerv3.Filter{
					{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
					},
				},
			},
		},
	}

	require.NoError(t, srv.injectQuotaRateLimitFilterIntoListener(ln, translator.QuotaDomain))

	updatedHCM, _, err := findHCM(ln.FilterChains[0])
	require.NoError(t, err)
	require.Len(t, updatedHCM.HttpFilters, 4)

	// Verify ordering: health_check, header_to_metadata, ratelimit, router.
	require.Equal(t, "envoy.filters.http.health_check", updatedHCM.HttpFilters[0].Name)
	require.Equal(t, "envoy.filters.http.header_to_metadata", updatedHCM.HttpFilters[1].Name)
	require.Equal(t, quotaRateLimitFilterName, updatedHCM.HttpFilters[2].Name)
	require.False(t, updatedHCM.HttpFilters[2].Disabled)
	require.Equal(t, wellknown.Router, updatedHCM.HttpFilters[3].Name)

	// Verify the ratelimit filter's internal configuration.
	rlCfg := &ratelimitfilterv3.RateLimit{}
	require.NoError(t, updatedHCM.HttpFilters[2].GetTypedConfig().UnmarshalTo(rlCfg))
	require.Equal(t, translator.QuotaDomain, rlCfg.Domain)
	require.Equal(t, quotaRateLimitClusterName, rlCfg.RateLimitService.GrpcService.GetEnvoyGrpc().ClusterName)
	require.Equal(t, corev3.ApiVersion_V3, rlCfg.RateLimitService.TransportApiVersion)
	require.False(t, rlCfg.FailureModeDeny)
	require.True(t, rlCfg.DisableXEnvoyRatelimitedHeader)
	require.Equal(t, ratelimitfilterv3.RateLimit_DRAFT_VERSION_03, rlCfg.EnableXRatelimitHeaders)
}

func TestEnableQuotaRateLimitOnRoute_WithBucketRules(t *testing.T) {
	t.Run("exact header match generates HeaderValueMatch action", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{
												Headers: []egv1a1.HeaderMatch{
													{
														Name:  "x-api-key",
														Type:  ptr.To(egv1a1.HeaderMatchExact),
														Value: ptr.To("premium"),
													},
												},
											},
										},
										Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 10, Duration: "1m"},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// 1 bucket req-time + 1 default req-time + 2 stream-done (rule + default) = 4 RateLimit entries.
		require.Len(t, perRoute.RateLimits, 4)

		// Bucket rule request-time entry (index 0): backend_name + model_name + HeaderValueMatch = 3 actions.
		ruleEntry := perRoute.RateLimits[0]
		require.Len(t, ruleEntry.Actions, 3)

		hvm := ruleEntry.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, hvm)
		expectedKey := translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium")
		require.Equal(t, expectedKey, hvm.DescriptorKey)
		require.Equal(t, expectedKey, hvm.DescriptorValue)
		require.True(t, hvm.ExpectMatch.Value)
		require.Len(t, hvm.Headers, 1)
		require.Equal(t, "x-api-key", hvm.Headers[0].Name)
		require.Equal(t, "premium", hvm.Headers[0].GetStringMatch().GetExact())

		// Default bucket request-time entry (index 1): backend_name + model_name + GenericKey = 3 actions.
		defaultEntry := perRoute.RateLimits[1]
		require.Len(t, defaultEntry.Actions, 3)
		gk := defaultEntry.Actions[2].GetGenericKey()
		require.NotNil(t, gk)
		require.Equal(t, translator.DefaultBucketDescriptorKey(1), gk.DescriptorKey)
	})

	t.Run("distinct header generates RequestHeaders action", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{
												Headers: []egv1a1.HeaderMatch{
													{
														Name: "x-user-id",
														Type: ptr.To(egv1a1.HeaderMatchDistinct),
													},
												},
											},
										},
										Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1h"},
									},
								},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// 1 bucket req-time + 1 stream-done at end = 2 entries (no default bucket).
		require.Len(t, perRoute.RateLimits, 2)

		// Request-time bucket rule entry (index 0).
		ruleEntry := perRoute.RateLimits[0]
		require.Len(t, ruleEntry.Actions, 3)

		rh := ruleEntry.Actions[2].GetRequestHeaders()
		require.NotNil(t, rh)
		require.Equal(t, "x-user-id", rh.HeaderName)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-user-id", ""), rh.DescriptorKey)

		// Stream-done entry (index 1) must use the same RequestHeaders action so the
		// token cost is charged to the per-value bucket checked at request time,
		// instead of to a single bucket shared by every header value.
		streamDone := perRoute.RateLimits[1]
		require.True(t, streamDone.ApplyOnStreamDone)
		require.NotNil(t, streamDone.HitsAddend)
		require.Len(t, streamDone.Actions, 3)
		require.Nil(t, streamDone.Actions[2].GetGenericKey())
		sdRh := streamDone.Actions[2].GetRequestHeaders()
		require.NotNil(t, sdRh)
		require.Equal(t, "x-user-id", sdRh.HeaderName)
		require.Equal(t, rh.DescriptorKey, sdRh.DescriptorKey)
	})

	t.Run("regex header with invert", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("claude"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{
												Headers: []egv1a1.HeaderMatch{
													{
														Name:   "x-tier",
														Type:   ptr.To(egv1a1.HeaderMatchRegularExpression),
														Value:  ptr.To("premium|enterprise"),
														Invert: ptr.To(true),
													},
												},
											},
										},
										Quota: aigv1a1.QuotaValue{Limit: 5, Duration: "1m"},
									},
								},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Request-time bucket rule entry (index 0).
		ruleEntry := perRoute.RateLimits[0]
		hvm := ruleEntry.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.False(t, hvm.ExpectMatch.Value)
		require.Equal(t, "premium|enterprise", hvm.Headers[0].GetStringMatch().GetSafeRegex().Regex)
	})

	t.Run("empty client selectors uses GenericKey", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
									},
								},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Request-time bucket rule entry (index 0).
		ruleEntry := perRoute.RateLimits[0]
		gk := ruleEntry.Actions[2].GetGenericKey()
		require.NotNil(t, gk)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "", ""), gk.DescriptorKey)
	})

	t.Run("multiple headers across selectors are flattened", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{
												Headers: []egv1a1.HeaderMatch{
													{Name: "x-api-key", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
												},
											},
											{
												Headers: []egv1a1.HeaderMatch{
													{Name: "x-org", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("acme")},
												},
											},
										},
										Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1h"},
									},
								},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Request-time bucket rule entry (index 0): backend_name + model_name + 2 header actions = 4 actions.
		ruleEntry := perRoute.RateLimits[0]
		require.Len(t, ruleEntry.Actions, 4)

		hvm0 := ruleEntry.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, hvm0)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium"), hvm0.DescriptorKey)
		require.Equal(t, "x-api-key", hvm0.Headers[0].Name)

		hvm1 := ruleEntry.Actions[3].GetHeaderValueMatch()
		require.NotNil(t, hvm1)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 1, "x-org", "acme"), hvm1.DescriptorKey)
		require.Equal(t, "x-org", hvm1.Headers[0].Name)
	})

	t.Run("models without bucket rules do not add extra entries", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// 1 request-time + 1 stream-done at end for default bucket only (no bucket rules).
		require.Len(t, perRoute.RateLimits, 2)
	})

	t.Run("multiple policies with bucket rules aggregate entries", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 10, Duration: "1m"},
							},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("claude"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1h"},
									},
								},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// gpt-4: 1 bucket req + 1 default req = 2; claude: 1 bucket req = 1;
		// + 2 stream-done (rule-0 deduped across models, gpt-4 default) = 5
		require.Len(t, perRoute.RateLimits, 5)
	})

	t.Run("nil model name in per-model quota is skipped", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: nil,
							Quota:     aigv1a1.QuotaDefinition{},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		// No entries since nil model name with no bucket rules is skipped; returns nil early.
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("multiple bucket rules for same model", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "test-backend"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "x-api-key", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"},
									},
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "x-tier", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("free")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 10, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// 2 bucket req-time + 1 default req-time + 3 stream-done (rule 0, rule 1, default) = 6
		require.Len(t, perRoute.RateLimits, 6)

		// Verify bucket rule 0 (request-time entry at index 0)
		r0 := perRoute.RateLimits[0]
		require.Len(t, r0.Actions, 3) // backend_name + model_name + 1 header
		hvm0 := r0.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, hvm0)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium"), hvm0.DescriptorKey)
		require.Equal(t, "x-api-key", hvm0.Headers[0].Name)

		// Verify bucket rule 1 (request-time entry at index 1)
		r1 := perRoute.RateLimits[1]
		require.Len(t, r1.Actions, 3)
		hvm1 := r1.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, hvm1)
		require.Equal(t, translator.BucketRuleDescriptorKey(1, 0, "x-tier", "free"), hvm1.DescriptorKey)
		require.Equal(t, "x-tier", hvm1.Headers[0].Name)

		// Verify default bucket (request-time entry at index 2)
		dfl := perRoute.RateLimits[2]
		require.Len(t, dfl.Actions, 3)
		gk := dfl.Actions[2].GetGenericKey()
		require.NotNil(t, gk)
		require.Equal(t, translator.DefaultBucketDescriptorKey(2), gk.DescriptorKey)

		// Verify stream-done entry for rule 0 (index 3): base actions + HeaderValueMatch(expect_match=true)
		sd0 := perRoute.RateLimits[3]
		require.True(t, sd0.ApplyOnStreamDone)
		require.Len(t, sd0.Actions, 3)
		sdHvm0 := sd0.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, sdHvm0)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium"), sdHvm0.DescriptorKey)
		require.True(t, sdHvm0.ExpectMatch.Value)
		require.Equal(t, "x-api-key", sdHvm0.Headers[0].Name)

		// Verify stream-done entry for rule 1 (index 4): base actions + HeaderValueMatch(expect_match=true)
		sd1 := perRoute.RateLimits[4]
		require.True(t, sd1.ApplyOnStreamDone)
		require.Len(t, sd1.Actions, 3)
		sdHvm1 := sd1.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, sdHvm1)
		require.Equal(t, translator.BucketRuleDescriptorKey(1, 0, "x-tier", "free"), sdHvm1.DescriptorKey)
		require.True(t, sdHvm1.ExpectMatch.Value)
		require.Equal(t, "x-tier", sdHvm1.Headers[0].Name)

		// Verify stream-done entry for default bucket (index 5): base actions + GenericKey (always fires)
		sdDfl := perRoute.RateLimits[5]
		require.True(t, sdDfl.ApplyOnStreamDone)
		require.Len(t, sdDfl.Actions, 3)
		sdDflGk := sdDfl.Actions[2].GetGenericKey()
		require.NotNil(t, sdDflGk)
		require.Equal(t, translator.DefaultBucketDescriptorKey(2), sdDflGk.DescriptorKey)
	})

	t.Run("multiple policies same model different backends different selectors", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "gateway", UID: "uid-1"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend-a"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "foo", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("bar")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
							},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "gateway", UID: "uid-2"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend-b"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "foo2", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("bar")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 300, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Policy 1: 1 bucket req + 1 default req = 2
		// Policy 2: 1 bucket req + 1 default req = 2
		// Stream-done: 2 bucket rule (different selectors) + 1 default (deduped) = 3
		// Total: 4 req-time + 3 stream-done = 7
		require.Len(t, perRoute.RateLimits, 7)

		// Verify stream-done entries: indices 4, 5, 6 are stream-done.
		// Order: bucket foo=bar, default (both from policy 1 loop), then bucket foo2=bar (policy 2 loop).

		// Stream-done for bucket rule with foo=bar (from policy 1).
		sd0 := perRoute.RateLimits[4]
		require.True(t, sd0.ApplyOnStreamDone)
		sdHvm0 := sd0.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, sdHvm0)
		require.Equal(t, "foo", sdHvm0.Headers[0].Name)

		// Stream-done for default bucket (deduped across both policies, added during policy 1 loop).
		sdDfl := perRoute.RateLimits[5]
		require.True(t, sdDfl.ApplyOnStreamDone)
		sdDflGk := sdDfl.Actions[2].GetGenericKey()
		require.NotNil(t, sdDflGk)

		// Stream-done for bucket rule with foo2=bar (from policy 2) — must NOT be deduped.
		sd1 := perRoute.RateLimits[6]
		require.True(t, sd1.ApplyOnStreamDone)
		sdHvm1 := sd1.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, sdHvm1)
		require.Equal(t, "foo2", sdHvm1.Headers[0].Name)
	})

	t.Run("multiple policies same model same selectors are deduped", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		policies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "gateway", UID: "uid-1"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend-a"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "foo", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("bar")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
							},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "gateway", UID: "uid-2"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend-b"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "foo", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("bar")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
							},
						},
					},
				},
			},
		}

		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Policy 1: 1 bucket req + 1 default req = 2
		// Policy 2: 1 bucket req + 1 default req = 2
		// Stream-done: 1 bucket rule (same model+headers deduped across backends) + 1 default = 2
		// Total: 4 req-time + 2 stream-done = 6
		require.Len(t, perRoute.RateLimits, 6)
	})

	t.Run("nil policies list", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, nil, nil))
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("empty policies list", func(t *testing.T) {
		route := &routev3.Route{Name: "test-route"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, []aigv1a1.QuotaPolicy{}, nil))
		require.Nil(t, route.TypedPerFilterConfig)
	})
}

func TestBuildStringMatcher(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-api-key",
			Type:  ptr.To(egv1a1.HeaderMatchExact),
			Value: ptr.To("premium"),
		}
		sm := buildStringMatcher(header)
		require.Equal(t, "premium", sm.GetExact())
	})

	t.Run("regex match", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-tier",
			Type:  ptr.To(egv1a1.HeaderMatchRegularExpression),
			Value: ptr.To("premium|enterprise"),
		}
		sm := buildStringMatcher(header)
		require.Equal(t, "premium|enterprise", sm.GetSafeRegex().Regex)
	})

	t.Run("nil type defaults to exact", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-key",
			Type:  nil,
			Value: ptr.To("value"),
		}
		sm := buildStringMatcher(header)
		require.Equal(t, "value", sm.GetExact())
	})

	t.Run("nil value with exact type returns empty exact", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-key",
			Type:  ptr.To(egv1a1.HeaderMatchExact),
			Value: nil,
		}
		sm := buildStringMatcher(header)
		require.Empty(t, sm.GetExact())
	})

	t.Run("nil value with regex type returns empty exact fallback", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-key",
			Type:  ptr.To(egv1a1.HeaderMatchRegularExpression),
			Value: nil,
		}
		sm := buildStringMatcher(header)
		require.Empty(t, sm.GetExact())
	})

	t.Run("nil value and nil type returns empty exact", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name: "x-key",
		}
		sm := buildStringMatcher(header)
		require.Empty(t, sm.GetExact())
	})
}

func TestBuildBucketRuleLimitEntries(t *testing.T) {
	oneTarget := []gwapiv1a2.LocalPolicyTargetReference{
		{Name: "test-backend"},
	}

	t.Run("no bucket rules returns nil", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{}
		entries := buildBucketRuleLimitEntries("gpt-4", "default", quota, oneTarget, nil)
		require.Nil(t, entries)
	})

	t.Run("single bucket rule with no default", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{
					Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
				},
			},
		}
		entries := buildBucketRuleLimitEntries("gpt-4", "default", quota, oneTarget, nil)
		require.Len(t, entries, 1) // 1 request-time only (stream-done added by enableQuotaRateLimitOnRoute)
		// Request-time entry: backend_name + model_name + GenericKey = 3 actions
		require.Len(t, entries[0].Actions, 3)
		gk := entries[0].Actions[2].GetGenericKey()
		require.NotNil(t, gk)
		require.Nil(t, entries[0].HitsAddend)
		require.False(t, entries[0].ApplyOnStreamDone)
	})

	t.Run("single bucket rule with default", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
			},
			DefaultBucket: aigv1a1.QuotaValue{Limit: 10, Duration: "1m"},
		}
		entries := buildBucketRuleLimitEntries("gpt-4", "default", quota, oneTarget, nil)
		require.Len(t, entries, 2) // 1 bucket req-time + 1 default req-time (no stream-done)

		// Default bucket request-time entry (index 1)
		defaultEntry := entries[1]
		require.Len(t, defaultEntry.Actions, 3)
		gk := defaultEntry.Actions[2].GetGenericKey()
		require.NotNil(t, gk)
		require.Equal(t, translator.DefaultBucketDescriptorKey(1), gk.DescriptorKey)
		require.Equal(t, translator.DefaultBucketDescriptorKey(1), gk.DescriptorValue)
	})

	t.Run("zero limit default bucket is not added", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
			},
			DefaultBucket: aigv1a1.QuotaValue{Limit: 0},
		}
		entries := buildBucketRuleLimitEntries("gpt-4", "default", quota, oneTarget, nil)
		require.Len(t, entries, 1) // 1 request-time only (no default, no stream-done)
	})

	t.Run("bucket rule with client selectors", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{
					ClientSelectors: []egv1a1.RateLimitSelectCondition{
						{
							Headers: []egv1a1.HeaderMatch{
								{Name: "x-api-key", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
							},
						},
					},
					Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
				},
			},
		}
		entries := buildBucketRuleLimitEntries("gpt-4", "default", quota, oneTarget, nil)
		require.Len(t, entries, 1) // 1 request-time only (stream-done added by enableQuotaRateLimitOnRoute)
		// Request-time entry: backend_name + model_name + 1 header match = 3 actions
		require.Len(t, entries[0].Actions, 3)
		hvm := entries[0].Actions[2].GetHeaderValueMatch()
		require.NotNil(t, hvm)
	})

	t.Run("request-time entries use GenericKey", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
			},
		}
		entries := buildBucketRuleLimitEntries("gpt-4", "default", quota, oneTarget, nil)
		require.Len(t, entries, 1) // request-time only (stream-done added by enableQuotaRateLimitOnRoute)

		// Request-time entry: GenericKey actions for backend_name and model_name.
		reqTime := entries[0]
		require.Equal(t, translator.BackendNameDescriptorKey, reqTime.Actions[0].GetGenericKey().DescriptorKey)
		require.Equal(t, "default/test-backend", reqTime.Actions[0].GetGenericKey().DescriptorValue)
		require.Equal(t, translator.ModelNameDescriptorKey, reqTime.Actions[1].GetGenericKey().DescriptorKey)
		require.Equal(t, "gpt-4", reqTime.Actions[1].GetGenericKey().DescriptorValue)
	})
}

func TestEnableQuotaRateLimitOnRoute_MultiplePerModelQuotas(t *testing.T) {
	route := &routev3.Route{Name: "test-route"}
	policies := []aigv1a1.QuotaPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "bedrock-backend"},
				},
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("claude-sonnet-4-6"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"},
						},
					},
					{
						ModelName: ptr.To("claude-haiku-4-5"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 500, Duration: "1m"},
						},
					},
				},
			},
		},
	}

	t.Run("filters by backend override, only matching model included", func(t *testing.T) {
		modelInfo := &routeModelInfo{
			backendModels: map[string][]string{"bedrock-backend": {"claude-sonnet-4-6"}},
		}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, policies, modelInfo))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Only claude-sonnet-4-6 included (simple case: 1 req-time + 1 stream-done at end).
		require.Len(t, perRoute.RateLimits, 2)
		require.Equal(t, "claude-sonnet-4-6", perRoute.RateLimits[0].Actions[1].GetGenericKey().DescriptorValue)
	})

	t.Run("nil modelInfo includes all models", func(t *testing.T) {
		route2 := &routev3.Route{Name: "test-route-2"}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route2, policies, nil))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route2.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// Both models included: 2 req-time + 1 stream-done (simple deduped across models) = 3.
		require.Len(t, perRoute.RateLimits, 3)
	})

	t.Run("model with bucket rules and model without are handled correctly", func(t *testing.T) {
		route3 := &routev3.Route{Name: "test-route-3"}
		mixedPolicies := []aigv1a1.QuotaPolicy{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "gateway"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Name: "bedrock-backend"},
						{Name: "bedrock-backend-haiku"},
					},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("claude-sonnet-4-6"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{
										ClientSelectors: []egv1a1.RateLimitSelectCondition{
											{Headers: []egv1a1.HeaderMatch{
												{Name: "x-tier", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
											}},
										},
										Quota: aigv1a1.QuotaValue{Limit: 1000, Duration: "1m"},
									},
								},
								DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
							},
						},
						{
							ModelName: ptr.To("claude-haiku-4-5"),
							Quota: aigv1a1.QuotaDefinition{
								DefaultBucket: aigv1a1.QuotaValue{Limit: 500, Duration: "1m"},
							},
						},
					},
				},
			},
		}
		modelInfo := &routeModelInfo{
			backendModels: map[string][]string{
				"bedrock-backend":       {"claude-sonnet-4-6"},
				"bedrock-backend-haiku": {"claude-haiku-4-5"},
			},
		}
		require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route3, mixedPolicies, modelInfo))

		perRoute := &ratelimitfilterv3.RateLimitPerRoute{}
		require.NoError(t, route3.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(perRoute))

		// claude-sonnet-4-6 with bucket rules: 2 targets × (1 bucket + 1 default) = 4 req-time
		// claude-haiku-4-5 simple: 2 targets × 1 = 2 req-time
		// stream-done: 1 sonnet rule (deduped across targets) + 1 sonnet default + 1 haiku simple = 3
		// Total: 6 + 3 = 9
		require.Len(t, perRoute.RateLimits, 9)

		// Verify sonnet bucket rule uses policy model name (not ModelNameOverride).
		sonnetBucketReqTime := perRoute.RateLimits[0]
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-tier", "premium"),
			sonnetBucketReqTime.Actions[2].GetHeaderValueMatch().DescriptorKey)

		// Stream-done entries use policy model name consistently with request-time.
		sonnetRuleSD := perRoute.RateLimits[6]
		require.True(t, sonnetRuleSD.ApplyOnStreamDone)
		sdHvm := sonnetRuleSD.Actions[2].GetHeaderValueMatch()
		require.NotNil(t, sdHvm)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-tier", "premium"), sdHvm.DescriptorKey)

		sonnetDefaultSD := perRoute.RateLimits[7]
		require.True(t, sonnetDefaultSD.ApplyOnStreamDone)
		sdGk := sonnetDefaultSD.Actions[2].GetGenericKey()
		require.NotNil(t, sdGk)
		require.Equal(t, translator.DefaultBucketDescriptorKey(1), sdGk.DescriptorKey)
	})
}

func TestBuildClientSelectorActions(t *testing.T) {
	t.Run("empty selectors returns GenericKey", func(t *testing.T) {
		actions := buildClientSelectorActions(0, nil)
		require.Len(t, actions, 1)
		gk := actions[0].GetGenericKey()
		require.NotNil(t, gk)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "", ""), gk.DescriptorKey)
	})

	t.Run("selectors with no headers returns GenericKey", func(t *testing.T) {
		selectors := []egv1a1.RateLimitSelectCondition{
			{}, // no headers
		}
		actions := buildClientSelectorActions(0, selectors)
		require.Len(t, actions, 1)
		gk := actions[0].GetGenericKey()
		require.NotNil(t, gk)
	})

	t.Run("single header exact match", func(t *testing.T) {
		selectors := []egv1a1.RateLimitSelectCondition{
			{
				Headers: []egv1a1.HeaderMatch{
					{Name: "x-api-key", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
				},
			},
		}
		actions := buildClientSelectorActions(0, selectors)
		require.Len(t, actions, 1)
		hvm := actions[0].GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium"), hvm.DescriptorKey)
	})

	t.Run("single header distinct match", func(t *testing.T) {
		selectors := []egv1a1.RateLimitSelectCondition{
			{
				Headers: []egv1a1.HeaderMatch{
					{Name: "x-user-id", Type: ptr.To(egv1a1.HeaderMatchDistinct)},
				},
			},
		}
		actions := buildClientSelectorActions(0, selectors)
		require.Len(t, actions, 1)
		rh := actions[0].GetRequestHeaders()
		require.NotNil(t, rh)
		require.Equal(t, "x-user-id", rh.HeaderName)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-user-id", ""), rh.DescriptorKey)
	})

	t.Run("multiple headers across selectors flattened", func(t *testing.T) {
		selectors := []egv1a1.RateLimitSelectCondition{
			{
				Headers: []egv1a1.HeaderMatch{
					{Name: "h1", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("v1")},
				},
			},
			{
				Headers: []egv1a1.HeaderMatch{
					{Name: "h2", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("v2")},
					{Name: "h3", Type: ptr.To(egv1a1.HeaderMatchDistinct)},
				},
			},
		}
		actions := buildClientSelectorActions(1, selectors)
		require.Len(t, actions, 3) // 3 headers total

		// h1: HeaderValueMatch
		require.NotNil(t, actions[0].GetHeaderValueMatch())
		require.Equal(t, translator.BucketRuleDescriptorKey(1, 0, "h1", "v1"), actions[0].GetHeaderValueMatch().DescriptorKey)

		// h2: HeaderValueMatch
		require.NotNil(t, actions[1].GetHeaderValueMatch())
		require.Equal(t, translator.BucketRuleDescriptorKey(1, 1, "h2", "v2"), actions[1].GetHeaderValueMatch().DescriptorKey)

		// h3: RequestHeaders (Distinct)
		require.NotNil(t, actions[2].GetRequestHeaders())
		require.Equal(t, translator.BucketRuleDescriptorKey(1, 2, "h3", ""), actions[2].GetRequestHeaders().DescriptorKey)
	})
}

func TestBuildHeaderMatchAction(t *testing.T) {
	t.Run("exact match creates HeaderValueMatch with ExpectMatch true", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-api-key",
			Type:  ptr.To(egv1a1.HeaderMatchExact),
			Value: ptr.To("premium"),
		}
		action := buildHeaderMatchAction(0, 0, header)
		hvm := action.GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium"), hvm.DescriptorKey)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-api-key", "premium"), hvm.DescriptorValue)
		require.True(t, hvm.ExpectMatch.Value)
		require.Len(t, hvm.Headers, 1)
		require.Equal(t, "x-api-key", hvm.Headers[0].Name)
		require.Equal(t, "premium", hvm.Headers[0].GetStringMatch().GetExact())
	})

	t.Run("regex match creates HeaderValueMatch with regex", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-tier",
			Type:  ptr.To(egv1a1.HeaderMatchRegularExpression),
			Value: ptr.To("premium|enterprise"),
		}
		action := buildHeaderMatchAction(0, 0, header)
		hvm := action.GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.True(t, hvm.ExpectMatch.Value)
		require.Equal(t, "premium|enterprise", hvm.Headers[0].GetStringMatch().GetSafeRegex().Regex)
	})

	t.Run("distinct match creates RequestHeaders action", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name: "x-user-id",
			Type: ptr.To(egv1a1.HeaderMatchDistinct),
		}
		action := buildHeaderMatchAction(0, 0, header)
		rh := action.GetRequestHeaders()
		require.NotNil(t, rh)
		require.Equal(t, "x-user-id", rh.HeaderName)
		require.Equal(t, translator.BucketRuleDescriptorKey(0, 0, "x-user-id", ""), rh.DescriptorKey)
	})

	t.Run("invert true sets ExpectMatch false", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:   "x-tier",
			Type:   ptr.To(egv1a1.HeaderMatchExact),
			Value:  ptr.To("internal"),
			Invert: ptr.To(true),
		}
		action := buildHeaderMatchAction(0, 0, header)
		hvm := action.GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.False(t, hvm.ExpectMatch.Value)
	})

	t.Run("invert false sets ExpectMatch true", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:   "x-tier",
			Type:   ptr.To(egv1a1.HeaderMatchExact),
			Value:  ptr.To("external"),
			Invert: ptr.To(false),
		}
		action := buildHeaderMatchAction(0, 0, header)
		hvm := action.GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.True(t, hvm.ExpectMatch.Value)
	})

	t.Run("nil invert defaults to ExpectMatch true", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "x-tier",
			Type:  ptr.To(egv1a1.HeaderMatchExact),
			Value: ptr.To("standard"),
		}
		action := buildHeaderMatchAction(0, 0, header)
		hvm := action.GetHeaderValueMatch()
		require.NotNil(t, hvm)
		require.True(t, hvm.ExpectMatch.Value)
	})

	t.Run("descriptor key encodes model, rule, and match index", func(t *testing.T) {
		header := egv1a1.HeaderMatch{
			Name:  "h1",
			Type:  ptr.To(egv1a1.HeaderMatchExact),
			Value: ptr.To("v"),
		}
		action := buildHeaderMatchAction(3, 2, header)
		hvm := action.GetHeaderValueMatch()
		require.Equal(t, "rule-3-h1|v-match-2", hvm.DescriptorKey)
		require.Equal(t, "rule-3-h1|v-match-2", hvm.DescriptorValue)
	})
}

func TestBaseDescriptorActions(t *testing.T) {
	actions := baseDescriptorActions()
	require.Len(t, actions, 2)

	verifyMetadataAction(t, actions[0], translator.BackendNameDescriptorKey, "ai_service_backend_name")
	verifyMetadataAction(t, actions[1], translator.ModelNameDescriptorKey, "model_name_override")
}

// aiGatewayRouteMetadata returns route metadata that makes isRouteGeneratedByAIGateway return true.
func aiGatewayRouteMetadata(t *testing.T) *corev3.Metadata {
	t.Helper()
	s, err := structpb.NewStruct(map[string]any{
		"resources": []any{
			map[string]any{
				"annotations": map[string]any{
					internalapi.AIGatewayGeneratedHTTPRouteAnnotation: "true",
				},
			},
		},
	})
	require.NoError(t, err)
	return &corev3.Metadata{
		FilterMetadata: map[string]*structpb.Struct{
			"envoy-gateway": s,
		},
	}
}

// newTestServerWithRoute creates a Server backed by a fake k8s client that contains
// the given AIGatewayRoute and QuotaPolicy objects.
func newTestServerWithRoute(t *testing.T, route *aigv1b1.AIGatewayRoute, policies ...aigv1a1.QuotaPolicy) *Server {
	t.Helper()
	c := newFakeClient()
	if route != nil {
		require.NoError(t, c.Create(t.Context(), route))
	}
	for i := range policies {
		require.NoError(t, c.Create(t.Context(), &policies[i]))
	}
	s, err := New(c, logr.Discard(), udsPath, false, nil, nil, "envoy-ai-gateway-ratelimit.envoy-gateway-system", 5, false)
	require.NoError(t, err)
	return s
}

func TestBackendKeysForCluster(t *testing.T) {
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-a"},
						{Name: "backend-b"},
					},
				},
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-c"},
					},
				},
			},
		},
	}
	s := newTestServerWithRoute(t, route)

	t.Run("valid cluster name returns backend keys", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "httproute/default/myroute/rule/0")
		require.Equal(t, []string{"default/backend-a", "default/backend-b"}, keys)
	})

	t.Run("second rule index", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "httproute/default/myroute/rule/1")
		require.Equal(t, []string{"default/backend-c"}, keys)
	})

	t.Run("wrong number of parts returns nil", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "too/few/parts")
		require.Nil(t, keys)
	})

	t.Run("not starting with httproute returns nil", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "tcproute/default/myroute/rule/0")
		require.Nil(t, keys)
	})

	t.Run("non-numeric rule index returns nil", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "httproute/default/myroute/rule/abc")
		require.Nil(t, keys)
	})

	t.Run("route not found returns nil", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "httproute/default/nonexistent/rule/0")
		require.Nil(t, keys)
	})

	t.Run("rule index out of bounds returns nil", func(t *testing.T) {
		keys := s.backendKeysForCluster(context.Background(), "httproute/default/myroute/rule/99")
		require.Nil(t, keys)
	})
}

func TestClusterHasQuotaBackend(t *testing.T) {
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-a"},
						{Name: "backend-b"},
					},
				},
			},
		},
	}
	s := newTestServerWithRoute(t, route)

	quotaBackendPolicies := map[string][]aigv1a1.QuotaPolicy{
		"default/backend-a": {{Spec: aigv1a1.QuotaPolicySpec{}}},
	}

	t.Run("cluster with matching backend returns true", func(t *testing.T) {
		result := s.clusterHasQuotaBackend(context.Background(), "httproute/default/myroute/rule/0", quotaBackendPolicies)
		require.True(t, result)
	})

	t.Run("cluster with no matching backend returns false", func(t *testing.T) {
		noMatchPolicies := map[string][]aigv1a1.QuotaPolicy{
			"default/backend-x": {{Spec: aigv1a1.QuotaPolicySpec{}}},
		}
		result := s.clusterHasQuotaBackend(context.Background(), "httproute/default/myroute/rule/0", noMatchPolicies)
		require.False(t, result)
	})

	t.Run("invalid cluster name returns false", func(t *testing.T) {
		result := s.clusterHasQuotaBackend(context.Background(), "invalid", quotaBackendPolicies)
		require.False(t, result)
	})

	t.Run("nonexistent route returns false", func(t *testing.T) {
		result := s.clusterHasQuotaBackend(context.Background(), "httproute/default/missing/rule/0", quotaBackendPolicies)
		require.False(t, result)
	})

	t.Run("rule index out of bounds returns false", func(t *testing.T) {
		result := s.clusterHasQuotaBackend(context.Background(), "httproute/default/myroute/rule/5", quotaBackendPolicies)
		require.False(t, result)
	})

	t.Run("non-numeric rule index returns false", func(t *testing.T) {
		result := s.clusterHasQuotaBackend(context.Background(), "httproute/default/myroute/rule/bad", quotaBackendPolicies)
		require.False(t, result)
	})
}

func TestRouteHasQuotaBackend(t *testing.T) {
	aigwRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-a"},
					},
				},
			},
		},
	}
	s := newTestServerWithRoute(t, aigwRoute)

	quotaBackendPolicies := map[string][]aigv1a1.QuotaPolicy{
		"default/backend-a": {{Spec: aigv1a1.QuotaPolicySpec{}}},
	}

	t.Run("nil route action returns false", func(t *testing.T) {
		route := &routev3.Route{Name: "test"}
		result := s.routeHasQuotaBackend(context.Background(), route, quotaBackendPolicies)
		require.False(t, result)
	})

	t.Run("single cluster with quota backend returns true", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_Cluster{
						Cluster: "httproute/default/myroute/rule/0",
					},
				},
			},
		}
		result := s.routeHasQuotaBackend(context.Background(), route, quotaBackendPolicies)
		require.True(t, result)
	})

	t.Run("single cluster without quota backend returns false", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_Cluster{
						Cluster: "httproute/default/nonexistent/rule/0",
					},
				},
			},
		}
		result := s.routeHasQuotaBackend(context.Background(), route, quotaBackendPolicies)
		require.False(t, result)
	})

	t.Run("weighted clusters with one matching returns true", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_WeightedClusters{
						WeightedClusters: &routev3.WeightedCluster{
							Clusters: []*routev3.WeightedCluster_ClusterWeight{
								{Name: "httproute/default/nonexistent/rule/0"},
								{Name: "httproute/default/myroute/rule/0"},
							},
						},
					},
				},
			},
		}
		result := s.routeHasQuotaBackend(context.Background(), route, quotaBackendPolicies)
		require.True(t, result)
	})

	t.Run("weighted clusters with none matching returns false", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_WeightedClusters{
						WeightedClusters: &routev3.WeightedCluster{
							Clusters: []*routev3.WeightedCluster_ClusterWeight{
								{Name: "httproute/default/nonexistent/rule/0"},
							},
						},
					},
				},
			},
		}
		result := s.routeHasQuotaBackend(context.Background(), route, quotaBackendPolicies)
		require.False(t, result)
	})
}

func TestPoliciesForRoute(t *testing.T) {
	aigwRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-a"},
					},
				},
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-b"},
					},
				},
			},
		},
	}
	s := newTestServerWithRoute(t, aigwRoute)

	policyA := aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("uid-a")},
		Spec:       aigv1a1.QuotaPolicySpec{},
	}
	policyB := aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("uid-b")},
		Spec:       aigv1a1.QuotaPolicySpec{},
	}
	quotaBackendPolicies := map[string][]aigv1a1.QuotaPolicy{
		"default/backend-a": {policyA},
		"default/backend-b": {policyB},
	}

	t.Run("nil route action returns nil", func(t *testing.T) {
		route := &routev3.Route{Name: "test"}
		result := s.policiesForRoute(context.Background(), route, quotaBackendPolicies)
		require.Nil(t, result)
	})

	t.Run("single cluster collects policies", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_Cluster{
						Cluster: "httproute/default/myroute/rule/0",
					},
				},
			},
		}
		result := s.policiesForRoute(context.Background(), route, quotaBackendPolicies)
		require.Len(t, result, 1)
		require.Equal(t, types.UID("uid-a"), result[0].UID)
	})

	t.Run("weighted clusters collect policies from all clusters", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_WeightedClusters{
						WeightedClusters: &routev3.WeightedCluster{
							Clusters: []*routev3.WeightedCluster_ClusterWeight{
								{Name: "httproute/default/myroute/rule/0"},
								{Name: "httproute/default/myroute/rule/1"},
							},
						},
					},
				},
			},
		}
		result := s.policiesForRoute(context.Background(), route, quotaBackendPolicies)
		require.Len(t, result, 2)
	})

	t.Run("deduplicates policies with same UID", func(t *testing.T) {
		// Both backends reference the same policy.
		sharedPolicies := map[string][]aigv1a1.QuotaPolicy{
			"default/backend-a": {policyA},
			"default/backend-b": {policyA},
		}
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_WeightedClusters{
						WeightedClusters: &routev3.WeightedCluster{
							Clusters: []*routev3.WeightedCluster_ClusterWeight{
								{Name: "httproute/default/myroute/rule/0"},
								{Name: "httproute/default/myroute/rule/1"},
							},
						},
					},
				},
			},
		}
		result := s.policiesForRoute(context.Background(), route, sharedPolicies)
		require.Len(t, result, 1)
		require.Equal(t, types.UID("uid-a"), result[0].UID)
	})

	t.Run("cluster with no matching policies returns empty", func(t *testing.T) {
		route := &routev3.Route{
			Name: "test",
			Action: &routev3.Route_Route{
				Route: &routev3.RouteAction{
					ClusterSpecifier: &routev3.RouteAction_Cluster{
						Cluster: "httproute/default/nonexistent/rule/0",
					},
				},
			},
		}
		result := s.policiesForRoute(context.Background(), route, quotaBackendPolicies)
		require.Empty(t, result)
	})
}

func TestPatchRoutesWithQuotaRateLimits(t *testing.T) {
	aigwRoute := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "gpt-4", Namespace: "default"},
		Spec: aigv1b1.AIGatewayRouteSpec{
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend-a", ModelNameOverride: "gpt-4-turbo"},
					},
				},
			},
		},
	}
	s := newTestServerWithRoute(t, aigwRoute)

	quotaBackendPolicies := map[string][]aigv1a1.QuotaPolicy{
		"default/backend-a": {
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: aigv1a1.QuotaPolicySpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{{Name: "backend-a"}},
					PerModelQuotas: []aigv1a1.PerModelQuota{
						{
							ModelName: ptr.To("gpt-4-turbo"),
							Quota: aigv1a1.QuotaDefinition{
								BucketRules: []aigv1a1.QuotaRule{
									{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
								},
							},
						},
					},
				},
			},
		},
	}

	t.Run("patches route with quota backend and AI gateway annotation", func(t *testing.T) {
		routeConfig := &routev3.RouteConfiguration{
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name:     "test-route",
							Metadata: aiGatewayRouteMetadata(t),
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/default/gpt-4/rule/0",
									},
								},
							},
						},
					},
				},
			},
		}

		s.patchRoutesWithQuotaRateLimits(context.Background(), routeConfig, quotaBackendPolicies)

		route := routeConfig.VirtualHosts[0].Routes[0]
		require.NotNil(t, route.TypedPerFilterConfig)
		require.Contains(t, route.TypedPerFilterConfig, quotaRateLimitFilterName)
	})

	t.Run("skips route without AI gateway annotation", func(t *testing.T) {
		routeConfig := &routev3.RouteConfiguration{
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name: "non-aigw-route",
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/default/gpt-4/rule/0",
									},
								},
							},
						},
					},
				},
			},
		}

		s.patchRoutesWithQuotaRateLimits(context.Background(), routeConfig, quotaBackendPolicies)

		route := routeConfig.VirtualHosts[0].Routes[0]
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("skips route with nil route action", func(t *testing.T) {
		routeConfig := &routev3.RouteConfiguration{
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name:     "redirect-route",
							Metadata: aiGatewayRouteMetadata(t),
							Action: &routev3.Route_Redirect{
								Redirect: &routev3.RedirectAction{},
							},
						},
					},
				},
			},
		}

		s.patchRoutesWithQuotaRateLimits(context.Background(), routeConfig, quotaBackendPolicies)

		route := routeConfig.VirtualHosts[0].Routes[0]
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("skips route without quota backend", func(t *testing.T) {
		routeConfig := &routev3.RouteConfiguration{
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name:     "no-quota-route",
							Metadata: aiGatewayRouteMetadata(t),
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/default/nonexistent/rule/0",
									},
								},
							},
						},
					},
				},
			},
		}

		s.patchRoutesWithQuotaRateLimits(context.Background(), routeConfig, quotaBackendPolicies)

		route := routeConfig.VirtualHosts[0].Routes[0]
		require.Nil(t, route.TypedPerFilterConfig)
	})

	t.Run("patches multiple routes across virtual hosts", func(t *testing.T) {
		routeConfig := &routev3.RouteConfiguration{
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name:     "route-1",
							Metadata: aiGatewayRouteMetadata(t),
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/default/gpt-4/rule/0",
									},
								},
							},
						},
					},
				},
				{
					Routes: []*routev3.Route{
						{
							Name:     "route-2",
							Metadata: aiGatewayRouteMetadata(t),
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/default/gpt-4/rule/0",
									},
								},
							},
						},
					},
				},
			},
		}

		s.patchRoutesWithQuotaRateLimits(context.Background(), routeConfig, quotaBackendPolicies)

		require.Contains(t, routeConfig.VirtualHosts[0].Routes[0].TypedPerFilterConfig, quotaRateLimitFilterName)
		require.Contains(t, routeConfig.VirtualHosts[1].Routes[0].TypedPerFilterConfig, quotaRateLimitFilterName)
	})
}

func TestMaybeInjectQuotaRateLimiting(t *testing.T) {
	// buildTestListenerWithRDS creates a listener whose HCM references the given
	// route config name via RDS, so findListenerRouteConfigs can resolve it.
	buildTestListenerWithRDS := func(t *testing.T, routeConfigName string) *listenerv3.Listener {
		t.Helper()
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			RouteSpecifier: &httpconnectionmanagerv3.HttpConnectionManager_Rds{
				Rds: &httpconnectionmanagerv3.Rds{
					RouteConfigName: routeConfigName,
				},
			},
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: wellknown.Router},
			},
		}
		hcmAny := mustToAny(t, hcm)
		return &listenerv3.Listener{
			Name: "test-listener",
			FilterChains: []*listenerv3.FilterChain{
				{
					Filters: []*listenerv3.Filter{
						{
							Name:       wellknown.HTTPConnectionManager,
							ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
						},
					},
				},
			},
		}
	}

	t.Run("no quota policies returns clusters unchanged", func(t *testing.T) {
		s := newTestServerWithRoute(t, nil)
		clusters := []*clusterv3.Cluster{{Name: "existing"}}
		routes := []*routev3.RouteConfiguration{}

		result, err := s.maybeInjectQuotaRateLimiting(t.Context(), clusters, nil, routes)
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Equal(t, "existing", result[0].Name)
	})

	t.Run("quota policies without matching targets returns clusters unchanged", func(t *testing.T) {
		qp := aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "qp1", Namespace: "default"},
			Spec:       aigv1a1.QuotaPolicySpec{
				// No targetRefs -> buildQuotaBackendPolicies returns empty map.
			},
		}
		s := newTestServerWithRoute(t, nil, qp)
		clusters := []*clusterv3.Cluster{{Name: "existing"}}

		result, err := s.maybeInjectQuotaRateLimiting(t.Context(), clusters, nil, nil)
		require.NoError(t, err)
		require.Len(t, result, 1)
	})

	t.Run("adds rate limit cluster and injects filter into listener", func(t *testing.T) {
		aigwRoute := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "gpt-4", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{Name: "backend-a", ModelNameOverride: "gpt-4-turbo"},
						},
					},
				},
			},
		}
		qp := aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "qp1", Namespace: "default"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "backend-a"},
				},
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4-turbo"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		}
		s := newTestServerWithRoute(t, aigwRoute, qp)

		cluster := &clusterv3.Cluster{
			Name: "httproute/default/gpt-4/rule/0",
		}
		clusters := []*clusterv3.Cluster{cluster}

		routeConfig := &routev3.RouteConfiguration{
			Name: "test-route-config",
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name:     "test-route",
							Metadata: aiGatewayRouteMetadata(t),
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/default/gpt-4/rule/0",
									},
								},
							},
						},
					},
				},
			},
		}
		routes := []*routev3.RouteConfiguration{routeConfig}

		ln := buildTestListenerWithRDS(t, "test-route-config")
		listeners := []*listenerv3.Listener{ln}

		result, err := s.maybeInjectQuotaRateLimiting(t.Context(), clusters, listeners, routes)
		require.NoError(t, err)

		// Should have original cluster + rate limit cluster.
		require.Len(t, result, 2)
		require.Equal(t, quotaRateLimitClusterName, result[1].Name)

		// Verify filter was injected into the listener HCM.
		updatedHCM, _, err := findHCM(ln.FilterChains[0])
		require.NoError(t, err)
		require.Len(t, updatedHCM.HttpFilters, 2)
		require.Equal(t, quotaRateLimitFilterName, updatedHCM.HttpFilters[0].Name)
		require.False(t, updatedHCM.HttpFilters[0].Disabled)

		// Verify route was patched.
		patchedRoute := routeConfig.VirtualHosts[0].Routes[0]
		require.Contains(t, patchedRoute.TypedPerFilterConfig, quotaRateLimitFilterName)
	})

	t.Run("does not inject filter into listener without quota routes", func(t *testing.T) {
		aigwRoute := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{Name: "backend-a"},
						},
					},
				},
			},
		}
		qp := aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "qp1", Namespace: "default"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "backend-a"},
				},
			},
		}
		s := newTestServerWithRoute(t, aigwRoute, qp)

		// Listener references a route config that has no quota backends.
		ln := buildTestListenerWithRDS(t, "unrelated-route-config")
		routeConfig := &routev3.RouteConfiguration{
			Name: "unrelated-route-config",
			VirtualHosts: []*routev3.VirtualHost{
				{
					Routes: []*routev3.Route{
						{
							Name: "no-quota-route",
							Action: &routev3.Route_Route{
								Route: &routev3.RouteAction{
									ClusterSpecifier: &routev3.RouteAction_Cluster{
										Cluster: "httproute/other/otherroute/rule/0",
									},
								},
							},
						},
					},
				},
			},
		}

		_, err := s.maybeInjectQuotaRateLimiting(t.Context(), nil, []*listenerv3.Listener{ln}, []*routev3.RouteConfiguration{routeConfig})
		require.NoError(t, err)

		// Verify filter was NOT injected into the listener.
		updatedHCM, _, err := findHCM(ln.FilterChains[0])
		require.NoError(t, err)
		require.Len(t, updatedHCM.HttpFilters, 1)
		require.Equal(t, wellknown.Router, updatedHCM.HttpFilters[0].Name)
	})

	t.Run("does not duplicate rate limit cluster", func(t *testing.T) {
		aigwRoute := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "myroute", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
							{Name: "backend-a"},
						},
					},
				},
			},
		}
		qp := aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "qp1", Namespace: "default"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "backend-a"},
				},
			},
		}
		s := newTestServerWithRoute(t, aigwRoute, qp)

		existingRLCluster := s.buildQuotaRateLimitCluster()
		clusters := []*clusterv3.Cluster{existingRLCluster}

		result, err := s.maybeInjectQuotaRateLimiting(t.Context(), clusters, nil, nil)
		require.NoError(t, err)
		// Should not add another rate limit cluster.
		rlCount := 0
		for _, c := range result {
			if c.Name == quotaRateLimitClusterName {
				rlCount++
			}
		}
		require.Equal(t, 1, rlCount)
	})
}

func TestListQuotaPolicies(t *testing.T) {
	t.Run("returns empty list when no policies exist", func(t *testing.T) {
		s := newTestServerWithRoute(t, nil)
		policies, err := s.listQuotaPolicies(t.Context())
		require.NoError(t, err)
		require.Empty(t, policies)
	})

	t.Run("returns all policies across namespaces", func(t *testing.T) {
		qp1 := aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "qp1", Namespace: "ns1"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "backend-a"},
				},
			},
		}
		qp2 := aigv1a1.QuotaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "qp2", Namespace: "ns2"},
			Spec: aigv1a1.QuotaPolicySpec{
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Name: "backend-b"},
				},
			},
		}
		s := newTestServerWithRoute(t, nil, qp1, qp2)

		policies, err := s.listQuotaPolicies(t.Context())
		require.NoError(t, err)
		require.Len(t, policies, 2)
	})
}
