// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package extensionserver

import (
	"fmt"
	"strings"

	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	luav3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/lua/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"google.golang.org/protobuf/types/known/anypb"
)

const postModelLuaMarker = "-- ai-gateway: after-model-extraction"

// Envoy Gateway 1.8.3 gives each policy its own Lua filter. Move only opted-in
// model guards; preserve their names, configs and route enablement unchanged.
func configurePostModelLua(listeners []*listenerv3.Listener) error {
	for _, listener := range listeners {
		chains := listener.GetFilterChains()
		if listener.DefaultFilterChain != nil {
			chains = append(chains, listener.DefaultFilterChain)
		}
		for _, chain := range chains {
			hcm, index, err := findHCM(chain)
			if err != nil {
				continue
			}
			var guards, remaining []*hcmv3.HttpFilter
			extprocIndex := -1
			for _, filter := range hcm.HttpFilters {
				config := filter.GetTypedConfig()
				lua := &luav3.Lua{}
				if strings.HasPrefix(filter.Name, "envoy.filters.http.lua") && config.MessageIs(lua) {
					if err := config.UnmarshalTo(lua); err != nil {
						return fmt.Errorf("decode listener Lua %s: %w", filter.Name, err)
					}
					if strings.HasPrefix(lua.GetDefaultSourceCode().GetInlineString(), postModelLuaMarker+"\n") {
						guards = append(guards, filter)
						continue
					}
				}
				remaining = append(remaining, filter)
				if filter.Name == aiGatewayExtProcName {
					extprocIndex = len(remaining) - 1
				}
			}
			if len(guards) == 0 {
				continue
			}
			if extprocIndex < 0 {
				return fmt.Errorf("listener %s has after-model Lua without AI extproc", listener.Name)
			}
			hcm.HttpFilters = append(remaining[:extprocIndex+1:extprocIndex+1], guards...)
			hcm.HttpFilters = append(hcm.HttpFilters, remaining[extprocIndex+1:]...)
			updated, err := anypb.New(hcm)
			if err != nil {
				return err
			}
			chain.Filters[index].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: updated}
		}
	}
	return nil
}
