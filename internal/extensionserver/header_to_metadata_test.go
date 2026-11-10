// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"testing"

	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	htomv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_to_metadata/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/stretchr/testify/require"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func TestBuildHeaderToMetadataFilter(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		filter, err := buildHeaderToMetadataFilter(nil)
		require.NoError(t, err)
		require.Nil(t, filter)
	})
	t.Run("sorted", func(t *testing.T) {
		filter, err := buildHeaderToMetadataFilter(map[string]string{
			"agent-session-id": "session.id",
			"x-tenant-id":      "tenant.id",
		})
		require.NoError(t, err)
		require.NotNil(t, filter)
		require.Equal(t, headerToMetadataFilterName, filter.Name)

		cfg := &htomv3.Config{}
		require.NoError(t, filter.GetTypedConfig().UnmarshalTo(cfg))
		require.Len(t, cfg.RequestRules, 2)
		require.Equal(t, "agent-session-id", cfg.RequestRules[0].GetHeader())
		require.Equal(t, "x-tenant-id", cfg.RequestRules[1].GetHeader())
		require.Equal(t, aigv1b1.AIGatewayFilterMetadataNamespace, cfg.RequestRules[0].GetOnHeaderPresent().MetadataNamespace)
		require.Equal(t, "session.id", cfg.RequestRules[0].GetOnHeaderPresent().Key)
		require.Equal(t, aigv1b1.AIGatewayFilterMetadataNamespace, cfg.RequestRules[1].GetOnHeaderPresent().MetadataNamespace)
		require.Equal(t, "tenant.id", cfg.RequestRules[1].GetOnHeaderPresent().Key)
	})
}

func Test_insertHeaderToMetadataFilter(t *testing.T) {
	t.Run("nil-filter", func(t *testing.T) {
		mgr := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: wellknown.Router}},
		}
		require.NoError(t, insertHeaderToMetadataFilter(mgr, nil))
		require.Len(t, mgr.HttpFilters, 1)
		require.Equal(t, wellknown.Router, mgr.HttpFilters[0].Name)
	})
	t.Run("router-missing", func(t *testing.T) {
		mgr := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: aiGatewayExtProcName}},
		}
		err := insertHeaderToMetadataFilter(mgr, &httpconnectionmanagerv3.HttpFilter{Name: headerToMetadataFilterName})
		require.EqualError(t, err, "failed to find router filter")
	})
	t.Run("insert-before-router", func(t *testing.T) {
		mgr := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{
				{Name: aiGatewayExtProcName},
				{Name: wellknown.Router},
			},
		}
		filter := &httpconnectionmanagerv3.HttpFilter{Name: headerToMetadataFilterName}
		require.NoError(t, insertHeaderToMetadataFilter(mgr, filter))
		require.Len(t, mgr.HttpFilters, 3)
		require.Equal(t, aiGatewayExtProcName, mgr.HttpFilters[0].Name)
		require.Equal(t, headerToMetadataFilterName, mgr.HttpFilters[1].Name)
		require.Equal(t, wellknown.Router, mgr.HttpFilters[2].Name)
	})
}

func TestFindHeaderToMetadataFilter(t *testing.T) {
	filters := []*httpconnectionmanagerv3.HttpFilter{
		{Name: aiGatewayExtProcName},
		{Name: headerToMetadataFilterName},
	}
	index, filter := findHeaderToMetadataFilter(filters)
	require.Equal(t, 1, index)
	require.NotNil(t, filter)
	require.Equal(t, headerToMetadataFilterName, filter.Name)
}

func TestMergeHeaderToMetadataRules(t *testing.T) {
	t.Run("nil-config", func(t *testing.T) {
		require.False(t, mergeHeaderToMetadataRules(nil, map[string]string{"x-tenant-id": "tenant.id"}))
	})
	t.Run("empty-attrs", func(t *testing.T) {
		require.False(t, mergeHeaderToMetadataRules(&htomv3.Config{}, nil))
	})
	t.Run("no-missing", func(t *testing.T) {
		cfg := &htomv3.Config{
			RequestRules: []*htomv3.Config_Rule{{Header: "x-tenant-id"}},
		}
		changed := mergeHeaderToMetadataRules(cfg, map[string]string{"x-tenant-id": "tenant.id"})
		require.False(t, changed)
		require.Len(t, cfg.RequestRules, 1)
		require.Equal(t, "x-tenant-id", cfg.RequestRules[0].GetHeader())
	})
	t.Run("append-missing-sorted", func(t *testing.T) {
		cfg := &htomv3.Config{
			RequestRules: []*htomv3.Config_Rule{{Header: "x-tenant-id"}},
		}
		changed := mergeHeaderToMetadataRules(cfg, map[string]string{
			"agent-session-id":  "session.id",
			"x-forwarded-proto": "url.scheme",
			"x-tenant-id":       "tenant.id",
		})
		require.True(t, changed)
		require.Len(t, cfg.RequestRules, 3)
		require.Equal(t, "x-tenant-id", cfg.RequestRules[0].GetHeader())
		require.Equal(t, "agent-session-id", cfg.RequestRules[1].GetHeader())
		require.Equal(t, "x-forwarded-proto", cfg.RequestRules[2].GetHeader())
		require.Equal(t, "session.id", cfg.RequestRules[1].GetOnHeaderPresent().Key)
		require.Equal(t, "url.scheme", cfg.RequestRules[2].GetOnHeaderPresent().Key)
		require.Equal(t, aigv1b1.AIGatewayFilterMetadataNamespace, cfg.RequestRules[1].GetOnHeaderPresent().MetadataNamespace)
		require.Equal(t, aigv1b1.AIGatewayFilterMetadataNamespace, cfg.RequestRules[2].GetOnHeaderPresent().MetadataNamespace)
	})
}

func TestInsertRequestHeaderToMetadataFilter(t *testing.T) {
	t.Run("missing-typed-config", func(t *testing.T) {
		s := &Server{logRequestHeaderAttributes: map[string]string{"x-tenant-id": "tenant.id"}}
		filter := &httpconnectionmanagerv3.HttpFilter{Name: headerToMetadataFilterName}
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{filter, {Name: wellknown.Router}},
		}
		hcmAny, err := toAny(hcm)
		require.NoError(t, err)
		listener := &listenerv3.Listener{
			FilterChains: []*listenerv3.FilterChain{
				{
					Filters: []*listenerv3.Filter{{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
					}},
				},
			},
		}
		err = s.insertRequestHeaderToMetadataFilter(listener)
		require.EqualError(t, err, "header_to_metadata filter missing typed_config")
	})
	t.Run("skip-non-hcm-chain", func(t *testing.T) {
		s := &Server{logRequestHeaderAttributes: map[string]string{"x-tenant-id": "tenant.id"}}
		hcm := &httpconnectionmanagerv3.HttpConnectionManager{
			HttpFilters: []*httpconnectionmanagerv3.HttpFilter{{Name: wellknown.Router}},
		}
		hcmAny, err := toAny(hcm)
		require.NoError(t, err)
		listener := &listenerv3.Listener{
			FilterChains: []*listenerv3.FilterChain{
				// TCP proxy chain without an HCM must be skipped, not fail the walk.
				{Filters: []*listenerv3.Filter{{Name: "envoy.filters.network.tcp_proxy"}}},
				{
					Filters: []*listenerv3.Filter{{
						Name:       wellknown.HTTPConnectionManager,
						ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny},
					}},
				},
			},
		}
		require.NoError(t, s.insertRequestHeaderToMetadataFilter(listener))
		// The HTTP chain got the filter; the TCP chain is untouched.
		hcmAfter := &httpconnectionmanagerv3.HttpConnectionManager{}
		require.NoError(t, listener.FilterChains[1].Filters[0].GetTypedConfig().UnmarshalTo(hcmAfter))
		require.Len(t, hcmAfter.HttpFilters, 2)
		require.Equal(t, headerToMetadataFilterName, hcmAfter.HttpFilters[0].Name)
		require.Len(t, listener.FilterChains[0].Filters, 1)
		require.Equal(t, "envoy.filters.network.tcp_proxy", listener.FilterChains[0].Filters[0].Name)
	})
}
