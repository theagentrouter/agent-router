// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

func TestBackendDomainValue(t *testing.T) {
	require.Equal(t, "default/my-backend", BackendDomainValue("default", "my-backend"))
	require.Equal(t, "ns1/svc", BackendDomainValue("ns1", "svc"))
}

func TestBackendNameFromDomain(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		ns, name, ok := BackendNameFromDomain("default/my-backend")
		require.True(t, ok)
		require.Equal(t, "default", ns)
		require.Equal(t, "my-backend", name)
	})

	t.Run("no slash", func(t *testing.T) {
		_, _, ok := BackendNameFromDomain("no-slash")
		require.False(t, ok)
	})

	t.Run("empty string", func(t *testing.T) {
		_, _, ok := BackendNameFromDomain("")
		require.False(t, ok)
	})

	t.Run("multiple slashes preserved in name", func(t *testing.T) {
		ns, name, ok := BackendNameFromDomain("ns/name/extra")
		require.True(t, ok)
		require.Equal(t, "ns", ns)
		require.Equal(t, "name/extra", name)
	})
}

func TestBucketRuleDescriptorKey(t *testing.T) {
	require.Equal(t, "rule-0-match-0", BucketRuleDescriptorKey(0, 0, "", ""))
	require.Equal(t, "rule-2-match-1", BucketRuleDescriptorKey(2, 1, "", ""))
}

func TestDefaultBucketDescriptorKey(t *testing.T) {
	require.Equal(t, "rule-3-match--1", DefaultBucketDescriptorKey(3))
	require.Equal(t, "rule-0-match--1", DefaultBucketDescriptorKey(0))
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantUnit  rlsconfv3.RateLimitUnit
		wantError bool
	}{
		{"1 second", "1s", rlsconfv3.RateLimitUnit_SECOND, false},
		{"1 minute", "1m", rlsconfv3.RateLimitUnit_MINUTE, false},
		{"1 hour", "1h", rlsconfv3.RateLimitUnit_HOUR, false},
		{"1 day", "1d", rlsconfv3.RateLimitUnit_DAY, false},
		{"1 week", "1w", rlsconfv3.RateLimitUnit_WEEK, false},
		{"1 month", "1mo", rlsconfv3.RateLimitUnit_MONTH, false},
		{"1 year", "1y", rlsconfv3.RateLimitUnit_YEAR, false},
		{"30 seconds rejected", "30s", 0, true},
		{"5 minutes rejected", "5m", 0, true},
		{"2 hours rejected", "2h", 0, true},
		{"24 hours rejected", "24h", 0, true},
		{"invalid duration", "abc", 0, true},
		{"negative duration", "-1s", 0, true},
		{"zero duration", "0s", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit, err := parseDuration(tt.input)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantUnit, unit)
		})
	}
}

func TestQuotaValueToPolicy(t *testing.T) {
	t.Run("1 minute 100 limit", func(t *testing.T) {
		qv := &aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}
		policy, err := quotaValueToPolicy(qv)
		require.NoError(t, err)
		require.Equal(t, uint32(100), policy.RequestsPerUnit)
		require.Equal(t, rlsconfv3.RateLimitUnit_MINUTE, policy.Unit)
	})

	t.Run("1 hour", func(t *testing.T) {
		qv := &aigv1a1.QuotaValue{Limit: 500, Duration: "1h"}
		policy, err := quotaValueToPolicy(qv)
		require.NoError(t, err)
		require.Equal(t, uint32(500), policy.RequestsPerUnit)
		require.Equal(t, rlsconfv3.RateLimitUnit_HOUR, policy.Unit)
	})

	t.Run("1 second", func(t *testing.T) {
		qv := &aigv1a1.QuotaValue{Limit: 10, Duration: "1s"}
		policy, err := quotaValueToPolicy(qv)
		require.NoError(t, err)
		require.Equal(t, uint32(10), policy.RequestsPerUnit)
		require.Equal(t, rlsconfv3.RateLimitUnit_SECOND, policy.Unit)
	})

	t.Run("custom duration rejected", func(t *testing.T) {
		qv := &aigv1a1.QuotaValue{Limit: 20, Duration: "5m"}
		_, err := quotaValueToPolicy(qv)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid duration")
	})

	t.Run("invalid duration", func(t *testing.T) {
		qv := &aigv1a1.QuotaValue{Limit: 100, Duration: "bad"}
		_, err := quotaValueToPolicy(qv)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid duration")
	})
}

func TestBuildServiceQuotaDescriptor(t *testing.T) {
	t.Run("basic service quota", func(t *testing.T) {
		sq := &aigv1a1.ServiceQuotaDefinition{
			Quota: aigv1a1.QuotaValue{Limit: 1000, Duration: "1h"},
		}
		desc, err := buildServiceQuotaDescriptor(sq)
		require.NoError(t, err)
		require.Equal(t, ModelNameDescriptorKey, desc.Key)
		require.Empty(t, desc.Value) // catch-all, no specific value
		require.NotNil(t, desc.RateLimit)
		require.Equal(t, uint32(1000), desc.RateLimit.RequestsPerUnit)
		require.Equal(t, rlsconfv3.RateLimitUnit_HOUR, desc.RateLimit.Unit)
	})

	t.Run("invalid duration returns error", func(t *testing.T) {
		sq := &aigv1a1.ServiceQuotaDefinition{
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "xyz"},
		}
		_, err := buildServiceQuotaDescriptor(sq)
		require.Error(t, err)
	})
}

func TestBuildPerModelDescriptor(t *testing.T) {
	t.Run("no bucket rules applies default directly", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
		}
		desc, err := buildPerModelDescriptor("gpt-4", quota)
		require.NoError(t, err)
		require.Equal(t, ModelNameDescriptorKey, desc.Key)
		require.Equal(t, "gpt-4", desc.Value)
		require.NotNil(t, desc.RateLimit)
		require.Equal(t, uint32(100), desc.RateLimit.RequestsPerUnit)
		require.Nil(t, desc.Descriptors)
	})

	t.Run("with bucket rules creates descriptors array", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"}},
			},
			DefaultBucket: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
		}
		desc, err := buildPerModelDescriptor("gpt-4", quota)
		require.NoError(t, err)
		require.Equal(t, "gpt-4", desc.Value)
		require.Nil(t, desc.RateLimit)      // rate limit on nested descriptors, not parent
		require.Len(t, desc.Descriptors, 2) // 1 bucket rule + 1 default
	})

	t.Run("with bucket rules and no default bucket", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"}},
			},
			DefaultBucket: aigv1a1.QuotaValue{Limit: 0}, // zero limit = no default
		}
		desc, err := buildPerModelDescriptor("gpt-4", quota)
		require.NoError(t, err)
		require.Len(t, desc.Descriptors, 1) // only the bucket rule
	})

	t.Run("multiple bucket rules", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"}},
				{Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"}},
				{Quota: aigv1a1.QuotaValue{Limit: 10, Duration: "1s"}},
			},
			DefaultBucket: aigv1a1.QuotaValue{Limit: 5, Duration: "1s"},
		}
		desc, err := buildPerModelDescriptor("model-x", quota)
		require.NoError(t, err)
		require.Len(t, desc.Descriptors, 4) // 3 bucket rules + 1 default

		// Verify default bucket key
		defaultDesc := desc.Descriptors[3]
		require.Equal(t, DefaultBucketDescriptorKey(3), defaultDesc.Key)
	})

	t.Run("invalid duration in default bucket", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "invalid"},
		}
		_, err := buildPerModelDescriptor("gpt-4", quota)
		require.Error(t, err)
	})

	t.Run("invalid duration in bucket rule", func(t *testing.T) {
		quota := &aigv1a1.QuotaDefinition{
			BucketRules: []aigv1a1.QuotaRule{
				{Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "bad"}},
			},
		}
		_, err := buildPerModelDescriptor("gpt-4", quota)
		require.Error(t, err)
		require.Contains(t, err.Error(), "bucket rule 0")
	})
}

func TestBuildBucketRuleDescriptors(t *testing.T) {
	t.Run("no client selectors creates single descriptor", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.Equal(t, "rule-0-match-0", descs[0].Key)
		require.Equal(t, "rule-0-match-0", descs[0].Value)
		require.NotNil(t, descs[0].RateLimit)
		require.Nil(t, descs[0].Descriptors)
	})

	t.Run("one header creates single descriptor", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{
					Headers: []egv1a1.HeaderMatch{
						{Name: "x-api-key"},
					},
				},
			},
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.Equal(t, "rule-0-x-api-key-match-0", descs[0].Key)
		require.NotNil(t, descs[0].RateLimit)
		require.Nil(t, descs[0].Descriptors)
	})

	t.Run("two headers creates nested chain sorted by name", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{
					Headers: []egv1a1.HeaderMatch{
						{Name: "x-org"},
						{Name: "x-api-key"},
					},
				},
			},
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)

		// Root: x-api-key sorted first (match-0).
		require.Equal(t, "rule-0-x-api-key-match-0", descs[0].Key)
		require.Nil(t, descs[0].RateLimit)
		require.Len(t, descs[0].Descriptors, 1)

		// Leaf: x-org sorted second (match-1), rate_limit only here.
		leaf := descs[0].Descriptors[0]
		require.Equal(t, "rule-0-x-org-match-1", leaf.Key)
		require.NotNil(t, leaf.RateLimit)
		require.Equal(t, uint32(100), leaf.RateLimit.RequestsPerUnit)
		require.Nil(t, leaf.Descriptors)
	})

	t.Run("three headers across multiple selectors creates nested chain sorted by name", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{
					Headers: []egv1a1.HeaderMatch{
						{Name: "h3"},
					},
				},
				{
					Headers: []egv1a1.HeaderMatch{
						{Name: "h1"},
						{Name: "h2"},
					},
				},
			},
			Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1h"},
		}
		descs, err := buildBucketRuleDescriptors(1, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)

		// Nested chain: h1 (match-0) → h2 (match-1) → h3 (match-2, leaf with rate_limit).
		root := descs[0]
		require.Equal(t, "rule-1-h1-match-0", root.Key)
		require.Nil(t, root.RateLimit)
		require.Len(t, root.Descriptors, 1)

		mid := root.Descriptors[0]
		require.Equal(t, "rule-1-h2-match-1", mid.Key)
		require.Nil(t, mid.RateLimit)
		require.Len(t, mid.Descriptors, 1)

		leaf := mid.Descriptors[0]
		require.Equal(t, "rule-1-h3-match-2", leaf.Key)
		require.NotNil(t, leaf.RateLimit)
		require.Equal(t, uint32(50), leaf.RateLimit.RequestsPerUnit)
		require.Equal(t, rlsconfv3.RateLimitUnit_HOUR, leaf.RateLimit.Unit)
		require.Nil(t, leaf.Descriptors)
	})

	t.Run("shadow mode enabled", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			Quota:      aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
			ShadowMode: ptr.To(true),
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.True(t, descs[0].ShadowMode)
	})

	t.Run("shadow mode disabled", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			Quota:      aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
			ShadowMode: ptr.To(false),
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.False(t, descs[0].ShadowMode)
	})

	t.Run("shadow mode nil", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.False(t, descs[0].ShadowMode)
	})

	t.Run("shadow mode applied to leaf descriptor in nested chain", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{Headers: []egv1a1.HeaderMatch{{Name: "h1"}, {Name: "h2"}}},
			},
			Quota:      aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
			ShadowMode: ptr.To(true),
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		// Shadow mode only on leaf.
		require.False(t, descs[0].ShadowMode)
		leaf := descs[0].Descriptors[0]
		require.True(t, leaf.ShadowMode)
	})

	t.Run("distinct header produces key-only descriptor", func(t *testing.T) {
		// Distinct headers use RequestHeaders action which sends the actual header value.
		// The service config must use key-only (no value) so it matches any value.
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{Headers: []egv1a1.HeaderMatch{
					{Name: "x-user-id", Type: ptr.To(egv1a1.HeaderMatchDistinct)},
				}},
			},
			Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.Equal(t, BucketRuleDescriptorKey(0, 0, "x-user-id", ""), descs[0].Key)
		require.Empty(t, descs[0].Value) // key-only: matches any value
		require.NotNil(t, descs[0].RateLimit)
	})

	t.Run("exact header produces key+value descriptor", func(t *testing.T) {
		// Exact headers use HeaderValueMatch which sends the fixed DescriptorValue.
		// The service config must use key+value matching that same fixed string.
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{Headers: []egv1a1.HeaderMatch{
					{Name: "x-api-key", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
				}},
			},
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(0, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		key := BucketRuleDescriptorKey(0, 0, "x-api-key", "premium")
		require.Equal(t, key, descs[0].Key)
		require.Equal(t, key, descs[0].Value) // fixed value matches HeaderValueMatch DescriptorValue
	})

	t.Run("mixed distinct and exact headers nested sorted by name", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{Headers: []egv1a1.HeaderMatch{
					{Name: "x-user-id", Type: ptr.To(egv1a1.HeaderMatchDistinct)},
					{Name: "x-tier", Type: ptr.To(egv1a1.HeaderMatchExact), Value: ptr.To("premium")},
				}},
			},
			Quota: aigv1a1.QuotaValue{Limit: 75, Duration: "1h"},
		}
		descs, err := buildBucketRuleDescriptors(2, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)

		// Root: x-tier sorted first (match-0), exact → key+value.
		require.Equal(t, BucketRuleDescriptorKey(2, 0, "x-tier", "premium"), descs[0].Key)
		require.Equal(t, BucketRuleDescriptorKey(2, 0, "x-tier", "premium"), descs[0].Value)
		require.Nil(t, descs[0].RateLimit)
		require.Len(t, descs[0].Descriptors, 1)

		// Leaf: x-user-id sorted second (match-1), distinct → key-only.
		leaf := descs[0].Descriptors[0]
		require.Equal(t, BucketRuleDescriptorKey(2, 1, "x-user-id", ""), leaf.Key)
		require.Empty(t, leaf.Value)
		require.NotNil(t, leaf.RateLimit)
	})

	t.Run("invalid duration", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "bad"},
		}
		_, err := buildBucketRuleDescriptors(0, rule)
		require.Error(t, err)
	})

	t.Run("different rule index", func(t *testing.T) {
		rule := &aigv1a1.QuotaRule{
			Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
		}
		descs, err := buildBucketRuleDescriptors(5, rule)
		require.NoError(t, err)
		require.Len(t, descs, 1)
		require.Equal(t, "rule-5-match-0", descs[0].Key)
	})
}

func TestBuildBackendDescriptor(t *testing.T) {
	t.Run("no per-model quotas and no service quota returns nil", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "default"
		backend.Name = "my-backend"

		desc, err := buildBackendDescriptor(policy, backend)
		require.NoError(t, err)
		require.Nil(t, desc)
	})

	t.Run("per-model quota with nil model name is skipped", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: nil,
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "default"
		backend.Name = "my-backend"

		desc, err := buildBackendDescriptor(policy, backend)
		require.NoError(t, err)
		require.Nil(t, desc) // nil model name is skipped, no service quota either
	})

	t.Run("per-model quota creates backend descriptor", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "ns1"
		backend.Name = "openai"

		desc, err := buildBackendDescriptor(policy, backend)
		require.NoError(t, err)
		require.NotNil(t, desc)
		require.Equal(t, BackendNameDescriptorKey, desc.Key)
		require.Equal(t, "ns1/openai", desc.Value)
		require.Len(t, desc.Descriptors, 1)
		require.Equal(t, ModelNameDescriptorKey, desc.Descriptors[0].Key)
		require.Equal(t, "gpt-4", desc.Descriptors[0].Value)
	})

	t.Run("service quota adds catch-all descriptor", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 5000, Duration: "1h"},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "default"
		backend.Name = "backend"

		desc, err := buildBackendDescriptor(policy, backend)
		require.NoError(t, err)
		require.NotNil(t, desc)
		require.Len(t, desc.Descriptors, 1)
		require.Equal(t, ModelNameDescriptorKey, desc.Descriptors[0].Key)
		require.Empty(t, desc.Descriptors[0].Value) // catch-all
	})

	t.Run("per-model and service quota combined", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
					{
						ModelName: ptr.To("claude"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 200, Duration: "1m"},
						},
					},
				},
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 10000, Duration: "1h"},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "default"
		backend.Name = "multi-model"

		desc, err := buildBackendDescriptor(policy, backend)
		require.NoError(t, err)
		require.NotNil(t, desc)
		// 2 per-model + 1 service quota catch-all = 3
		require.Len(t, desc.Descriptors, 3)
		require.Equal(t, "gpt-4", desc.Descriptors[0].Value)
		require.Equal(t, "claude", desc.Descriptors[1].Value)
		require.Empty(t, desc.Descriptors[2].Value) // catch-all
	})

	t.Run("service quota with zero limit is not added", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 0, Duration: "1h"},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "default"
		backend.Name = "b"

		desc, err := buildBackendDescriptor(policy, backend)
		require.NoError(t, err)
		require.Nil(t, desc) // limit 0 is not > 0, so no service quota descriptor
	})

	t.Run("invalid per-model duration returns wrapped error", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("bad-model"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "xyz"},
						},
					},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "ns"
		backend.Name = "b"

		_, err := buildBackendDescriptor(policy, backend)
		require.Error(t, err)
		require.Contains(t, err.Error(), `model "bad-model"`)
	})

	t.Run("invalid service quota duration returns wrapped error", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 100, Duration: "xyz"},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{}
		backend.Namespace = "ns"
		backend.Name = "b"

		_, err := buildBackendDescriptor(policy, backend)
		require.Error(t, err)
		require.Contains(t, err.Error(), "service quota")
	})
}

func TestBuildRateLimitConfigs(t *testing.T) {
	t.Run("nil backends returns nil", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{}
		configs, err := BuildRateLimitConfigs(policy, nil)
		require.NoError(t, err)
		require.Nil(t, configs)
	})

	t.Run("empty backends returns nil", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{}
		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{})
		require.NoError(t, err)
		require.Nil(t, configs)
	})

	t.Run("backends with no matching quotas returns nil", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{},
		}
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "default"},
		}

		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{backend})
		require.NoError(t, err)
		require.Nil(t, configs)
	})

	t.Run("single backend with per-model quota", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "openai", Namespace: "default"},
		}

		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{backend})
		require.NoError(t, err)
		require.Len(t, configs, 1)
		require.Equal(t, QuotaDomain, configs[0].Domain)
		require.Equal(t, QuotaDomain, configs[0].Name)
		require.Len(t, configs[0].Descriptors, 1)
		require.Equal(t, BackendNameDescriptorKey, configs[0].Descriptors[0].Key)
		require.Equal(t, "default/openai", configs[0].Descriptors[0].Value)
	})

	t.Run("multiple backends", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "1m"},
						},
					},
				},
			},
		}
		b1 := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "openai", Namespace: "ns1"},
		}
		b2 := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "azure", Namespace: "ns2"},
		}

		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{b1, b2})
		require.NoError(t, err)
		require.Len(t, configs, 1) // single config with shared domain
		require.Len(t, configs[0].Descriptors, 2)
		require.Equal(t, "ns1/openai", configs[0].Descriptors[0].Value)
		require.Equal(t, "ns2/azure", configs[0].Descriptors[1].Value)
	})

	t.Run("invalid duration in per-model quota returns error", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							DefaultBucket: aigv1a1.QuotaValue{Limit: 100, Duration: "bad"},
						},
					},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
		}

		_, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{backend})
		require.Error(t, err)
		require.Contains(t, err.Error(), "ns/b")
	})

	t.Run("complex policy with bucket rules", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{
						ModelName: ptr.To("gpt-4"),
						Quota: aigv1a1.QuotaDefinition{
							BucketRules: []aigv1a1.QuotaRule{
								{
									Quota:      aigv1a1.QuotaValue{Limit: 200, Duration: "1m"},
									ShadowMode: ptr.To(true),
								},
								{
									Quota: aigv1a1.QuotaValue{Limit: 50, Duration: "1m"},
								},
							},
							DefaultBucket: aigv1a1.QuotaValue{Limit: 10, Duration: "1m"},
						},
					},
				},
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 10000, Duration: "1h"},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "openai", Namespace: "default"},
		}

		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{backend})
		require.NoError(t, err)
		require.Len(t, configs, 1)

		backendDesc := configs[0].Descriptors[0]
		require.Equal(t, "default/openai", backendDesc.Value)
		// per-model (gpt-4 with bucket rules) + service quota catch-all = 2
		require.Len(t, backendDesc.Descriptors, 2)

		// gpt-4 descriptor has bucket rule descriptors
		gpt4Desc := backendDesc.Descriptors[0]
		require.Equal(t, "gpt-4", gpt4Desc.Value)
		require.Nil(t, gpt4Desc.RateLimit) // has descriptors array instead
		// 2 bucket rules + 1 default = 3 descriptors
		require.Len(t, gpt4Desc.Descriptors, 3)
		require.True(t, gpt4Desc.Descriptors[0].ShadowMode)
		require.False(t, gpt4Desc.Descriptors[1].ShadowMode)

		// Verify default bucket descriptor key
		defaultDesc := gpt4Desc.Descriptors[2]
		require.Equal(t, DefaultBucketDescriptorKey(2), defaultDesc.Key)

		// service quota catch-all
		serviceDesc := backendDesc.Descriptors[1]
		require.Empty(t, serviceDesc.Value)
		require.NotNil(t, serviceDesc.RateLimit)
	})

	t.Run("nil model name entries are skipped", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				PerModelQuotas: []aigv1a1.PerModelQuota{
					{ModelName: nil},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
		}

		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{backend})
		require.NoError(t, err)
		require.Nil(t, configs)
	})

	t.Run("only service quota", func(t *testing.T) {
		policy := &aigv1a1.QuotaPolicy{
			Spec: aigv1a1.QuotaPolicySpec{
				ServiceQuota: aigv1a1.ServiceQuotaDefinition{
					Quota: aigv1a1.QuotaValue{Limit: 5000, Duration: "1h"},
				},
			},
		}
		backend := &aigv1b1.AIServiceBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "prod"},
		}

		configs, err := BuildRateLimitConfigs(policy, []*aigv1b1.AIServiceBackend{backend})
		require.NoError(t, err)
		require.Len(t, configs, 1)
		require.Len(t, configs[0].Descriptors, 1)

		backendDesc := configs[0].Descriptors[0]
		require.Equal(t, "prod/svc", backendDesc.Value)
		require.Len(t, backendDesc.Descriptors, 1)
		require.Empty(t, backendDesc.Descriptors[0].Value) // catch-all
		require.Equal(t, uint32(5000), backendDesc.Descriptors[0].RateLimit.RequestsPerUnit)
		require.Equal(t, rlsconfv3.RateLimitUnit_HOUR, backendDesc.Descriptors[0].RateLimit.Unit)
	})
}
