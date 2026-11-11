// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// injectedQuotaCostModels runs cost injection for route against a policy pricing models on "models/backend".
func injectedQuotaCostModels(t *testing.T, models []string, rules []aigv1b1.AIGatewayRouteRule) []string {
	t.Helper()
	fakeClient := requireNewFakeClientWithIndexesForQuotaPolicy(t)
	policy := &aigv1a1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: "models"},
		Spec: aigv1a1.QuotaPolicySpec{
			TargetRefs: []gwapiv1a2.NamespacedPolicyTargetReference{{
				Group: aiServiceBackendGroup, Kind: aiServiceBackendKind, Name: "backend",
			}},
		},
	}
	for _, name := range models {
		policy.Spec.PerModelQuotas = append(policy.Spec.PerModelQuotas, aigv1a1.PerModelQuota{
			ModelName: ptr.To(name), Quota: aigv1a1.QuotaDefinition{CostExpression: ptr.To("input_tokens + output_tokens")},
		})
	}
	require.NoError(t, fakeClient.Create(t.Context(), policy))
	c := &GatewayController{
		client:                  fakeClient,
		logger:                  logr.Discard(),
		referenceGrantValidator: newReferenceGrantValidator(fakeClient),
	}
	route := &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "models"},
		Spec:       aigv1b1.AIGatewayRouteSpec{Rules: rules},
	}
	config := &filterapi.Config{Backends: []filterapi.Backend{{AIServiceBackendName: "models/backend"}}}
	c.injectQuotaPolicyCostExpressions(t.Context(), route, config, make(map[string]struct{}), "models/route")
	var injected []string
	for _, cost := range config.LLMRequestCosts {
		require.Equal(t, QuotaCostMetadataKey, cost.MetadataKey)
		require.Equal(t, "models/backend", cost.Backend)
		injected = append(injected, cost.Model)
	}
	return injected
}

func quotaTestModelHeader(value string, typ *gwapiv1.HeaderMatchType) gwapiv1.HTTPHeaderMatch {
	return gwapiv1.HTTPHeaderMatch{Name: gwapiv1.HTTPHeaderName(internalapi.ModelNameHeaderKeyDefault), Value: value, Type: typ}
}

func quotaTestExactModel(value string) aigv1b1.AIGatewayRouteRuleMatch {
	return aigv1b1.AIGatewayRouteRuleMatch{Headers: []gwapiv1.HTTPHeaderMatch{quotaTestModelHeader(value, nil)}}
}

func quotaTestRule(override string, matches ...aigv1b1.AIGatewayRouteRuleMatch) aigv1b1.AIGatewayRouteRule {
	return aigv1b1.AIGatewayRouteRule{
		Matches:     matches,
		BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend", ModelNameOverride: override}},
	}
}

func TestQuotaCostsCoverMixedOverrideAndUnversionedModels(t *testing.T) {
	models := []string{"claude-haiku-4-5@20251001", "claude-haiku-5-5", "claude-sonnet-5-5", "unrelated"}
	rules := []aigv1b1.AIGatewayRouteRule{
		quotaTestRule(models[0], quotaTestExactModel("claude-haiku-4-5")),
		quotaTestRule("", quotaTestExactModel("claude-haiku-5-5")),
		quotaTestRule("", quotaTestExactModel("claude-sonnet-5-5")),
	}
	require.ElementsMatch(t, models[:3], injectedQuotaCostModels(t, models, rules),
		"unversioned models must retain pricing alongside explicit overrides")
}

func TestQuotaCostsPreserveUnenumerableRouteModels(t *testing.T) {
	catchAll := quotaTestRule("")
	regex := quotaTestRule("", aigv1b1.AIGatewayRouteRuleMatch{Headers: []gwapiv1.HTTPHeaderMatch{
		quotaTestModelHeader("model-.*", ptr.To(gwapiv1.HeaderMatchRegularExpression)),
	}})
	nonModelHeader := aigv1b1.AIGatewayRouteRuleMatch{Headers: []gwapiv1.HTTPHeaderMatch{{Name: "x-tenant", Value: "acme"}}}
	allModels := []string{"model-a", "model-b", "model-c"}

	for _, tc := range []struct {
		name  string
		rules []aigv1b1.AIGatewayRouteRule
		want  []string
	}{
		{
			name:  "no overrides: exact plus catch-all without matches",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("", quotaTestExactModel("model-a")), catchAll},
			want:  allModels,
		},
		{
			name:  "no overrides: exact plus catch-all on non-model header",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("", quotaTestExactModel("model-a")), quotaTestRule("", nonModelHeader)},
			want:  allModels,
		},
		{
			name:  "no overrides: exact plus regex model match",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("", quotaTestExactModel("model-a")), regex},
			want:  allModels,
		},
		{
			name:  "no overrides: exact and catch-all matches in the same rule",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("", quotaTestExactModel("model-a"), nonModelHeader)},
			want:  allModels,
		},
		{
			name:  "override plus catch-all",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("model-a", quotaTestExactModel("public-a")), catchAll},
			want:  allModels,
		},
		{
			name:  "override plus regex model match",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("model-a", quotaTestExactModel("public-a")), regex},
			want:  allModels,
		},
		{
			name:  "no overrides: only exact matches stay filtered",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("", quotaTestExactModel("model-a")), quotaTestRule("", quotaTestExactModel("model-b"))},
			want:  []string{"model-a", "model-b"},
		},
		{
			name: "exact match header name is case-insensitive",
			rules: []aigv1b1.AIGatewayRouteRule{quotaTestRule("", aigv1b1.AIGatewayRouteRuleMatch{Headers: []gwapiv1.HTTPHeaderMatch{{
				Name: gwapiv1.HTTPHeaderName(strings.ToUpper(internalapi.ModelNameHeaderKeyDefault)), Value: "model-b",
			}}})},
			want: []string{"model-b"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ElementsMatch(t, tc.want, injectedQuotaCostModels(t, allModels, tc.rules))
		})
	}
}
