// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package extensionserver

import (
	"fmt"
	"sort"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	luav3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/lua/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	metadatav3 "github.com/envoyproxy/go-control-plane/envoy/type/metadata/v3"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/translator"
)

const aggregateBudgetFilterName = "envoy.filters.http.lua/aggregate-user-budget"

func (s *Server) injectAggregateBudgetGuard(listener *listenerv3.Listener) error {
	chains := listener.GetFilterChains()
	if listener.DefaultFilterChain != nil {
		chains = append(chains, listener.DefaultFilterChain)
	}
	for _, chain := range chains {
		hcm, index, err := findHCM(chain)
		if err != nil {
			continue
		}
		found := false
		for _, filter := range hcm.HttpFilters {
			found = found || filter.Name == aggregateBudgetFilterName
		}
		if found {
			continue
		}
		config, err := anypb.New(&luav3.Lua{
			DefaultSourceCode: &corev3.DataSource{
				Specifier: &corev3.DataSource_InlineString{InlineString: "function envoy_on_request(handle) end"},
			},
		})
		if err != nil {
			return fmt.Errorf("marshal aggregate identity filter: %w", err)
		}
		for i, filter := range hcm.HttpFilters {
			if filter.Name == quotaRateLimitFilterName {
				guard := &hcmv3.HttpFilter{
					Name:       aggregateBudgetFilterName,
					ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: config},
				}
				hcm.HttpFilters = append(hcm.HttpFilters, nil)
				copy(hcm.HttpFilters[i+1:], hcm.HttpFilters[i:])
				hcm.HttpFilters[i] = guard
				break
			}
		}
		config, err = anypb.New(hcm)
		if err != nil {
			return fmt.Errorf("marshal aggregate identity listener: %w", err)
		}
		chain.Filters[index].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: config}
	}
	return nil
}

func aggregateMetadataKey(key string) *metadatav3.MetadataKey {
	return &metadatav3.MetadataKey{
		Key: aigv1b1.AIGatewayFilterMetadataNamespace,
		Path: []*metadatav3.MetadataKey_PathSegment{{
			Segment: &metadatav3.MetadataKey_PathSegment_Key{Key: key},
		}},
	}
}

func aggregateBudgetEntries(budget *aigv1a1.AggregateUserBudget) []*routev3.RateLimit {
	actions := func() []*routev3.RateLimit_Action {
		return []*routev3.RateLimit_Action{{
			ActionSpecifier: &routev3.RateLimit_Action_GenericKey_{
				GenericKey: &routev3.RateLimit_Action_GenericKey{
					DescriptorKey: translator.AggregateBudgetDescriptorKey, DescriptorValue: budget.Name,
				},
			},
		}, {
			ActionSpecifier: &routev3.RateLimit_Action_Metadata{
				Metadata: &routev3.RateLimit_Action_MetaData{
					DescriptorKey: translator.AggregateUserDescriptorKey,
					MetadataKey:   aggregateMetadataKey("aggregate_user"),
					Source:        routev3.RateLimit_Action_MetaData_DYNAMIC,
				},
			},
		}, {
			ActionSpecifier: &routev3.RateLimit_Action_Metadata{
				Metadata: &routev3.RateLimit_Action_MetaData{
					DescriptorKey: translator.AggregateTierDescriptorKey,
					MetadataKey:   aggregateMetadataKey("aggregate_tier"),
					Source:        routev3.RateLimit_Action_MetaData_DYNAMIC,
				},
			},
		}}
	}
	return []*routev3.RateLimit{{
		Actions: actions(), HitsAddend: &routev3.RateLimit_HitsAddend{Number: wrapperspb.UInt64(0)},
	}, {
		Actions: actions(), HitsAddend: quotaHitsAddend(), ApplyOnStreamDone: true,
	}}
}

// luaLiteral uses decimal byte escapes so arbitrary claim/tier strings cannot
// inject Lua code or depend on Lua's Unicode escape support.
func luaLiteral(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(value); i++ {
		fmt.Fprintf(&b, "\\%03d", value[i])
	}
	b.WriteByte('"')
	return b.String()
}

func aggregateBudgetLua(budget *aigv1a1.AggregateUserBudget) string {
	tiers := make([]string, 0, len(budget.TierLimits))
	for tier := range budget.TierLimits {
		tiers = append(tiers, tier)
	}
	sort.Strings(tiers)
	var limits strings.Builder
	for _, tier := range tiers {
		fmt.Fprintf(&limits, "[%s]=%d,", luaLiteral(tier), budget.TierLimits[tier])
	}
	return fmt.Sprintf(`
function envoy_on_request(handle)
  local payloads = handle:streamInfo():dynamicMetadata():get("envoy.filters.http.jwt_authn") or {}
  local claims = payloads[%s]
  if type(claims) ~= "table" or type(claims.iss) ~= "string" or claims.iss == "" or
     type(claims.sub) ~= "string" or claims.sub == "" then
    handle:logWarn("Aggregate budget rejected request without verified user identity")
    handle:respond({[":status"]="403"}, "Verified budget identity required")
    return
  end
  local limits = {%s}
  local tier = claims[%s]
  local limit = type(tier) == "string" and limits[tier] or nil
  if limit == nil then
    handle:logWarn("Aggregate budget rejected request without an approved tier")
    handle:respond({[":status"]="403"}, "Approved budget tier required")
    return
  end
  local metadata = handle:streamInfo():dynamicMetadata()
  metadata:set(%s, "aggregate_user", tostring(#claims.iss) .. ":" .. claims.iss .. claims.sub)
  metadata:set(%s, "aggregate_tier", tier)
end
`, luaLiteral(budget.JWTProvider), limits.String(), luaLiteral(budget.TierClaim),
		luaLiteral(aigv1b1.AIGatewayFilterMetadataNamespace), luaLiteral(aigv1b1.AIGatewayFilterMetadataNamespace))
}

func configureAggregateBudget(route *routev3.Route, policy *aigv1a1.QuotaPolicy) error {
	if err := translator.ValidateAggregateUserBudget(policy); err != nil {
		return err
	}
	if route.GetRoute() == nil {
		return fmt.Errorf("aggregate user budgets require a forwarding route")
	}
	config, err := anypb.New(&luav3.LuaPerRoute{
		Override: &luav3.LuaPerRoute_SourceCode{
			SourceCode: &corev3.DataSource{
				Specifier: &corev3.DataSource_InlineString{InlineString: aggregateBudgetLua(policy.Spec.AggregateUserBudget)},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal aggregate user budget guard: %w", err)
	}
	if route.TypedPerFilterConfig == nil {
		route.TypedPerFilterConfig = make(map[string]*anypb.Any)
	}
	route.TypedPerFilterConfig[aggregateBudgetFilterName] = config
	return nil
}
