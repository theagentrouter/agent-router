// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package extensionserver

import (
	"testing"

	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	ratelimitv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ratelimit/v3"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	lua "github.com/yuin/gopher-lua"
	"k8s.io/utils/ptr"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
)

func TestAggregateUserBudgetSharesAdmissionAndResponseKeys(t *testing.T) {
	budget := &aigv1a1.AggregateUserBudget{
		Name: "shared", JWTProvider: "okta", TierClaim: "quota_group",
		TierLimits: map[string]uint32{"engineering": 1000000000, "testers": 60000000},
	}
	policy := aigv1a1.QuotaPolicy{Spec: aigv1a1.QuotaPolicySpec{
		AggregateUserBudget: budget,
		PerModelQuotas: []aigv1a1.PerModelQuota{
			{ModelName: ptr.To("model-a"), Quota: aigv1a1.QuotaDefinition{CostExpression: ptr.To("input_tokens")}},
			{ModelName: ptr.To("model-b"), Quota: aigv1a1.QuotaDefinition{CostExpression: ptr.To("output_tokens")}},
		},
	}}
	safetyLimits := []*routev3.RateLimit{{}}
	route := &routev3.Route{Action: &routev3.Route_Route{Route: &routev3.RouteAction{RateLimits: safetyLimits}}}
	require.NoError(t, enableQuotaRateLimitOnRoute(logr.Discard(), route, []aigv1a1.QuotaPolicy{policy}, nil))
	require.Equal(t, safetyLimits, route.GetRoute().RateLimits)
	config := &ratelimitv3.RateLimitPerRoute{}
	require.NoError(t, route.TypedPerFilterConfig[quotaRateLimitFilterName].UnmarshalTo(config))
	entries := config.RateLimits
	require.Len(t, entries, 2, "must not charge once per model/backend")
	check, charge := entries[0], entries[1]
	require.Equal(t, check.Actions, charge.Actions)
	require.Nil(t, check.Limit, "Envoy rejects inline limit overrides")
	require.Nil(t, charge.Limit)
	require.Zero(t, check.HitsAddend.Number.Value)
	require.True(t, charge.ApplyOnStreamDone)
	require.Contains(t, charge.HitsAddend.Format, "quota_cost")
	require.Len(t, check.Actions, 3)
	require.Equal(t, "shared", check.Actions[0].GetGenericKey().DescriptorValue)
	require.Equal(t, "aggregate_user", check.Actions[1].GetMetadata().DescriptorKey)
	require.Equal(t, "aggregate_tier", check.Actions[2].GetMetadata().DescriptorKey)
	require.NotNil(t, route.TypedPerFilterConfig[aggregateBudgetFilterName])
	script := aggregateBudgetLua(budget)
	require.Contains(t, script, `get("envoy.filters.http.jwt_authn")`)
	require.Contains(t, script, "claims.iss")
	require.Contains(t, script, "claims.sub")
	require.Contains(t, script, `"aggregate_tier", tier`)
	require.NotContains(t, script, "handle:headers()")
	require.Contains(t, script, `handle:respond({[":status"]="403"}`)
}

func TestLuaLiteralEscapesUntrustedConfiguration(t *testing.T) {
	require.Equal(t, `"\034\010\093\093"`, luaLiteral("\"\n]]"))
}

func TestAggregateBudgetUsesOnlyVerifiedJWTClaims(t *testing.T) {
	budget := &aigv1a1.AggregateUserBudget{
		Name: "shared", JWTProvider: "okta", TierClaim: "quota_group",
		TierLimits: map[string]uint32{"engineering": 1000000000, "testers": 60000000},
	}
	for _, tc := range []struct {
		name, claims, user string
		limit              int
		rejected           bool
	}{
		{"user-a", `{iss="https://issuer",sub="user-a",quota_group="engineering"}`, "14:https://issueruser-a", 1000000000, false},
		{"user-b", `{iss="https://issuer",sub="user-b",quota_group="engineering"}`, "14:https://issueruser-b", 1000000000, false},
		{"tier-change-preserves-user", `{iss="https://issuer",sub="user-a",quota_group="testers"}`, "14:https://issueruser-a", 60000000, false},
		{"missing-jwt", `nil`, "", 0, true},
		{"missing-sub", `{iss="https://issuer",quota_group="engineering"}`, "", 0, true},
		{"missing-issuer", `{sub="user-a",quota_group="engineering"}`, "", 0, true},
		{"unknown-tier", `{iss="https://issuer",sub="user-a",quota_group="unapproved"}`, "", 0, true},
		{"array-tier", `{iss="https://issuer",sub="user-a",quota_group={"engineering","testers"}}`, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := lua.NewState()
			defer state.Close()
			require.NoError(t, state.DoString(`
result = {}
handle = {}
function handle:streamInfo() return self end
function handle:dynamicMetadata() return self end
function handle:get(namespace) return {okta=`+tc.claims+`} end
function handle:set(namespace,key,value) result[key]=value end
function handle:logWarn(message) result.warning=message end
function handle:respond(headers,body) result.status=headers[":status"] end
function handle:headers() error("Must not read caller-supplied headers") end
`+aggregateBudgetLua(budget)+`
envoy_on_request(handle)
`))
			result := state.GetGlobal("result").(*lua.LTable)
			if tc.rejected {
				require.Equal(t, lua.LString("403"), result.RawGetString("status"))
				require.Equal(t, lua.LNil, result.RawGetString("aggregate_user"))
			} else {
				require.Equal(t, lua.LNil, result.RawGetString("status"))
				require.Equal(t, lua.LString(tc.user), result.RawGetString("aggregate_user"))
				tier := result.RawGetString("aggregate_tier").String()
				require.Equal(t, uint32(tc.limit), budget.TierLimits[tier])
			}
		})
	}
}
