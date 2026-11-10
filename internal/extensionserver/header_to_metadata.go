// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"errors"
	"sort"
	"strings"

	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	htomv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_to_metadata/v3"
	httpconnectionmanagerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

const headerToMetadataFilterName = "envoy.filters.http.header_to_metadata"

func (s *Server) insertRequestHeaderToMetadataFilters(listeners []*listenerv3.Listener) error {
	if len(s.logRequestHeaderAttributes) == 0 {
		return nil
	}
	for _, ln := range listeners {
		if err := s.insertRequestHeaderToMetadataFilter(ln); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) insertRequestHeaderToMetadataFilter(listener *listenerv3.Listener) error {
	filterChains := listener.GetFilterChains()
	if listener.DefaultFilterChain != nil {
		filterChains = append(filterChains, listener.DefaultFilterChain)
	}
	for _, currChain := range filterChains {
		httpConManager, hcmIndex, err := findHCM(currChain)
		if hcmIndex == -1 {
			// Chains without an HCM (e.g. TCP/UDP routes) cannot host HTTP filters.
			s.log.Error(err, "skipping filter chain without an HTTP connection manager", "listener", listener.Name)
			continue
		}
		if filterIndex, filter := findHeaderToMetadataFilter(httpConManager.HttpFilters); filter != nil {
			typedConfig := filter.GetTypedConfig()
			if typedConfig == nil {
				return errors.New("header_to_metadata filter missing typed_config")
			}
			cfg := &htomv3.Config{}
			if unmarshalErr := typedConfig.UnmarshalTo(cfg); unmarshalErr != nil {
				return unmarshalErr
			}
			if mergeHeaderToMetadataRules(cfg, s.logRequestHeaderAttributes) {
				cfgAny, cfgErr := toAny(cfg)
				if cfgErr != nil {
					return cfgErr
				}
				httpConManager.HttpFilters[filterIndex].ConfigType = &httpconnectionmanagerv3.HttpFilter_TypedConfig{
					TypedConfig: cfgAny,
				}
				hcmAny, hcmErr := toAny(httpConManager)
				if hcmErr != nil {
					return hcmErr
				}
				currChain.Filters[hcmIndex].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny}
			}
			continue
		}
		filter, err := buildHeaderToMetadataFilter(s.logRequestHeaderAttributes)
		if err != nil {
			return err
		}
		if err = insertHeaderToMetadataFilter(httpConManager, filter); err != nil {
			return err
		}
		hcmAny, err := toAny(httpConManager)
		if err != nil {
			return err
		}
		currChain.Filters[hcmIndex].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: hcmAny}
	}
	return nil
}

func buildHeaderToMetadataFilter(attrs map[string]string) (*httpconnectionmanagerv3.HttpFilter, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(attrs))
	for header := range attrs {
		keys = append(keys, header)
	}
	sort.Strings(keys)

	cfg := &htomv3.Config{}
	for _, header := range keys {
		cfg.RequestRules = append(cfg.RequestRules, &htomv3.Config_Rule{
			Header: header,
			OnHeaderPresent: &htomv3.Config_KeyValuePair{
				MetadataNamespace: aigv1b1.AIGatewayFilterMetadataNamespace,
				Key:               attrs[header],
				Type:              htomv3.Config_STRING,
			},
		})
	}
	cfgAny, err := toAny(cfg)
	if err != nil {
		return nil, err
	}
	return &httpconnectionmanagerv3.HttpFilter{
		Name:       headerToMetadataFilterName,
		ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: cfgAny},
	}, nil
}

func insertHeaderToMetadataFilter(mgr *httpconnectionmanagerv3.HttpConnectionManager, filter *httpconnectionmanagerv3.HttpFilter) error {
	if filter == nil {
		return nil
	}
	for i, f := range mgr.HttpFilters {
		if f.Name == wellknown.Router {
			mgr.HttpFilters = append(mgr.HttpFilters[:i], append([]*httpconnectionmanagerv3.HttpFilter{filter}, mgr.HttpFilters[i:]...)...)
			return nil
		}
	}
	return errors.New("failed to find router filter")
}

func findHeaderToMetadataFilter(filters []*httpconnectionmanagerv3.HttpFilter) (int, *httpconnectionmanagerv3.HttpFilter) {
	for i, f := range filters {
		if f.Name == headerToMetadataFilterName {
			return i, f
		}
	}
	return -1, nil
}

func mergeHeaderToMetadataRules(cfg *htomv3.Config, attrs map[string]string) bool {
	if cfg == nil || len(attrs) == 0 {
		return false
	}
	existing := make(map[string]struct{}, len(cfg.RequestRules))
	for _, rule := range cfg.RequestRules {
		existing[strings.ToLower(rule.GetHeader())] = struct{}{}
	}
	var missing []string
	for header := range attrs {
		if _, ok := existing[strings.ToLower(header)]; ok {
			continue
		}
		missing = append(missing, header)
	}
	if len(missing) == 0 {
		return false
	}
	sort.Strings(missing)
	for _, header := range missing {
		cfg.RequestRules = append(cfg.RequestRules, &htomv3.Config_Rule{
			Header: header,
			OnHeaderPresent: &htomv3.Config_KeyValuePair{
				MetadataNamespace: aigv1b1.AIGatewayFilterMetadataNamespace,
				Key:               attrs[header],
				Type:              htomv3.Config_STRING,
			},
		})
	}
	return true
}
