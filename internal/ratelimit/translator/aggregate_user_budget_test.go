// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"encoding/json"
	"testing"

	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func TestAggregatePolicyRoundTripOmitsUnusedQuotaValues(t *testing.T) {
	data, err := json.Marshal(aggregatePolicy())
	require.NoError(t, err)
	require.NotContains(t, string(data), `"serviceQuota"`)
	require.NotContains(t, string(data), `"defaultBucket"`)
	require.NotContains(t, string(data), `"duration":""`)
}

func aggregatePolicy() *aigv1a1.QuotaPolicy {
	return &aigv1a1.QuotaPolicy{Spec: aigv1a1.QuotaPolicySpec{
		AggregateUserBudget: &aigv1a1.AggregateUserBudget{
			Name: "shared-budget", JWTProvider: "okta", TierClaim: "quota_group",
			TierLimits: map[string]uint32{"engineering": 1000000000, "testers": 60000000},
		},
		PerModelQuotas: []aigv1a1.PerModelQuota{{
			ModelName: ptr.To("model-a"),
			Quota:     aigv1a1.QuotaDefinition{CostExpression: ptr.To("(input_tokens * 2000u + output_tokens * 12000u + 999u) / 1000u")},
		}},
	}}
}

func TestAggregateBudgetDescriptorsExcludeBackendModelAndEnvironment(t *testing.T) {
	policy := aggregatePolicy()
	var previous []*rlsconfv3.RateLimitConfig
	for _, environment := range []string{"dev", "test", "prod"} {
		policy.Namespace = environment
		policy.Spec.PerModelQuotas[0].ModelName = ptr.To(environment + "-different-model")
		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{{}})
		require.NoError(t, err)
		require.Len(t, configs, 1)
		root := configs[0].Descriptors[0]
		require.Equal(t, AggregateBudgetDescriptorKey, root.Key)
		require.Equal(t, "shared-budget", root.Value)
		require.Len(t, root.Descriptors, 1)
		user := root.Descriptors[0]
		require.Equal(t, AggregateUserDescriptorKey, user.Key)
		require.Empty(t, user.Value)
		require.Nil(t, user.RateLimit)
		require.Len(t, user.Descriptors, 2)
		for _, tier := range user.Descriptors {
			require.Equal(t, AggregateTierDescriptorKey, tier.Key)
			require.Equal(t, policy.Spec.AggregateUserBudget.TierLimits[tier.Value], tier.RateLimit.RequestsPerUnit)
			require.Equal(t, rlsconfv3.RateLimitUnit_MONTH, tier.RateLimit.Unit)
			require.True(t, tier.QuotaMode)
		}
		if previous != nil {
			require.Equal(t, previous, configs)
		}
		previous = configs
	}
}

func TestAggregateBudgetRejectsAmbiguousConfiguration(t *testing.T) {
	for _, mutate := range []func(*aigv1a1.QuotaPolicy){
		func(p *aigv1a1.QuotaPolicy) { p.Spec.AggregateUserBudget.JWTProvider = "" },
		func(p *aigv1a1.QuotaPolicy) { p.Spec.AggregateUserBudget.TierLimits["testers"] = 0 },
		func(p *aigv1a1.QuotaPolicy) { p.Spec.PerModelQuotas[0].Quota.DefaultBucket.Limit = 1 },
		func(p *aigv1a1.QuotaPolicy) { p.Spec.PerModelQuotas[0].Quota.CostExpression = nil },
		func(p *aigv1a1.QuotaPolicy) { p.Spec.PerModelQuotas = nil },
	} {
		policy := aggregatePolicy()
		mutate(policy)
		_, err := BuildRateLimitConfigs(policy, nil)
		require.Error(t, err)
	}
}
