// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"sort"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

const (
	// QuotaDomain is the single rate limit domain used for all QuotaPolicy enforcement.
	// All backends share this domain, with backend_name descriptors distinguishing them.
	QuotaDomain = "ai-gateway-quota"

	// BackendNameDescriptorKey is the descriptor key used for backend-based rate limiting.
	// This matches the descriptor key sent by the rate limit MetaData action that reads
	// the backend name from dynamic metadata set by the upstream ext_proc filter.
	BackendNameDescriptorKey = "backend_name"

	// ModelNameDescriptorKey is the descriptor key used for model-based rate limiting.
	// This matches the descriptor key sent by the rate limit MetaData action that reads
	// the model name from model_name_override in dynamic metadata set by the ext_proc filter.
	ModelNameDescriptorKey = "model_name_override"
)

// KeyedDescriptor pairs a leaf rate limit descriptor with a comparable key that
// uniquely identifies its position in the descriptor tree. The key uses semantic
// names (header names/values for client selectors) so that two policies producing
// the same logical path can be merged by simple key comparison.
//
// Key format: {key}_{depth}_{value}/{key}_{depth}_{value}/...
// Example: "backend_name_0_default/openai/model_name_override_1_gpt-4/x-api-key_2_premium"
type KeyedDescriptor struct {
	ComparableKey string
	Descriptor    *rlsconfv3.RateLimitDescriptor
}

// ComparableKeySegment builds one segment of a comparable key.
func ComparableKeySegment(key string, depth int, value string) string {
	return fmt.Sprintf("%s_%d_%s", key, depth, value)
}

// BackendDomainValue returns the backend_name descriptor value for an AIServiceBackend.
// Format: "{namespace}/{backend-name}"
func BackendDomainValue(namespace, backendName string) string {
	return namespace + "/" + backendName
}

// headerComparableValue returns the value to use in a comparable key for a HeaderMatch.
// Distinct headers use empty string; Exact/Regex use the header value.
func headerComparableValue(header egv1a1.HeaderMatch) string {
	if header.Type != nil && *header.Type == egv1a1.HeaderMatchDistinct {
		return ""
	}
	if header.Value != nil {
		return *header.Value
	}
	return ""
}

// BucketRuleDescriptorKey returns the descriptor key for a bucket rule's header match.
// The "|" separator between header name and value prevents ambiguity ("|" is illegal in
// HTTP header names per RFC 7230). For catch-all rules, headerName and headerValue are empty.
func BucketRuleDescriptorKey(ruleIndex, matchIndex int, headerName, headerValue string) string {
	if headerName == "" {
		return fmt.Sprintf("rule-%d-match-%d", ruleIndex, matchIndex)
	}
	if headerValue == "" {
		return fmt.Sprintf("rule-%d-%s-match-%d", ruleIndex, headerName, matchIndex)
	}
	return fmt.Sprintf("rule-%d-%s|%s-match-%d", ruleIndex, headerName, headerValue, matchIndex)
}

// DefaultBucketDescriptorKey returns the descriptor key for a model's default bucket.
// Model name is omitted because this descriptor is nested under parent backend_name
// and model_name_override descriptors that already provide uniqueness.
func DefaultBucketDescriptorKey(numRules int) string {
	return fmt.Sprintf("rule-%d-match--1", numRules)
}

// BuildRateLimitConfigs translates a QuotaPolicy and its resolved target
// AIServiceBackends into a single rate limit service configuration.
// All backends share the same domain, distinguished by backend_name descriptors.
func BuildRateLimitConfigs(
	policy *aigv1a1.QuotaPolicy,
	backends []*aigv1b1.AIServiceBackend,
) ([]*rlsconfv3.RateLimitConfig, error) {
	var backendDescriptors []*rlsconfv3.RateLimitDescriptor
	for _, backend := range backends {
		desc, err := buildBackendDescriptor(policy, backend)
		if err != nil {
			return nil, fmt.Errorf("failed to build descriptors for backend %s/%s: %w",
				backend.Namespace, backend.Name, err)
		}
		if desc != nil {
			backendDescriptors = append(backendDescriptors, desc)
		}
	}
	if len(backendDescriptors) == 0 {
		return nil, nil
	}

	return []*rlsconfv3.RateLimitConfig{
		{
			Name:        QuotaDomain,
			Domain:      QuotaDomain,
			Descriptors: backendDescriptors,
		},
	}, nil
}

func buildBackendDescriptor(
	policy *aigv1a1.QuotaPolicy,
	backend *aigv1b1.AIServiceBackend,
) (*rlsconfv3.RateLimitDescriptor, error) {
	desc, _, err := buildBackendDescriptorKeyed(policy, backend)
	return desc, err
}

// buildBackendDescriptorKeyed creates a backend_name descriptor and also returns
// KeyedDescriptor entries for all leaf descriptors under it.
func buildBackendDescriptorKeyed(
	policy *aigv1a1.QuotaPolicy,
	backend *aigv1b1.AIServiceBackend,
) (*rlsconfv3.RateLimitDescriptor, []KeyedDescriptor, error) {
	backendValue := BackendDomainValue(backend.Namespace, backend.Name)
	backendKeySegment := ComparableKeySegment(BackendNameDescriptorKey, 0, backendValue)

	var modelDescriptors []*rlsconfv3.RateLimitDescriptor
	var allKeyed []KeyedDescriptor

	for _, pmq := range policy.Spec.PerModelQuotas {
		if pmq.ModelName == nil {
			continue
		}
		modelName := *pmq.ModelName
		desc, keyed, err := buildPerModelDescriptorKeyed(modelName, &pmq.Quota, backendKeySegment)
		if err != nil {
			return nil, nil, fmt.Errorf("model %q: %w", modelName, err)
		}
		modelDescriptors = append(modelDescriptors, desc)
		allKeyed = append(allKeyed, keyed...)
	}

	if policy.Spec.ServiceQuota.Quota.Limit > 0 {
		desc, err := buildServiceQuotaDescriptor(&policy.Spec.ServiceQuota)
		if err != nil {
			return nil, nil, fmt.Errorf("service quota: %w", err)
		}
		modelDescriptors = append(modelDescriptors, desc)
		allKeyed = append(allKeyed, KeyedDescriptor{
			ComparableKey: backendKeySegment + "/" + ComparableKeySegment(ModelNameDescriptorKey, 1, ""),
			Descriptor:    desc,
		})
	}

	if len(modelDescriptors) == 0 {
		return nil, nil, nil
	}

	return &rlsconfv3.RateLimitDescriptor{
		Key:         BackendNameDescriptorKey,
		Value:       backendValue,
		Descriptors: modelDescriptors,
	}, allKeyed, nil
}

// buildPerModelDescriptor creates a descriptor that matches a specific model name.
// descriptorModelName is used as the model_name_override descriptor value (may differ
// when a ModelNameOverride is set on the AIGatewayRoute backend ref).
//
// Simple case (no bucket rules):
//
//	key: model_name_override
//	value: "gpt-4"
//	rate_limit:
//	  requests_per_unit: 100
//	  unit: MINUTE
//
// With bucket rules (client selectors, nested and sorted by header name):
//
//	key: model_name_override
//	value: "gpt-4"
//	descriptors:
//	  - key: rule-0-match-0                  ← first header (sorted)
//	    value: rule-0-match-0
//	    descriptors:
//	      - key: rule-0-match-1              ← second header (sorted)
//	        value: rule-0-match-1
//	        rate_limit: ...                  ← only on leaf
func buildPerModelDescriptor(descriptorModelName string, quota *aigv1a1.QuotaDefinition) (*rlsconfv3.RateLimitDescriptor, error) {
	desc, _, err := buildPerModelDescriptorKeyed(descriptorModelName, quota, "")
	return desc, err
}

// buildPerModelDescriptorKeyed creates a model-level descriptor and also returns
// KeyedDescriptor entries for all leaf descriptors. parentKeyPrefix is the
// comparable key prefix from ancestor descriptors (e.g., the backend segment).
// descriptorModelName is used as the model_name_override descriptor value.
func buildPerModelDescriptorKeyed(descriptorModelName string, quota *aigv1a1.QuotaDefinition, parentKeyPrefix string) (*rlsconfv3.RateLimitDescriptor, []KeyedDescriptor, error) {
	modelSegment := ComparableKeySegment(ModelNameDescriptorKey, 1, descriptorModelName)
	modelPrefix := modelSegment
	if parentKeyPrefix != "" {
		modelPrefix = parentKeyPrefix + "/" + modelSegment
	}

	desc := &rlsconfv3.RateLimitDescriptor{
		Key:   ModelNameDescriptorKey,
		Value: descriptorModelName,
	}

	if len(quota.BucketRules) == 0 {
		policy, err := quotaValueToPolicy(&quota.DefaultBucket)
		if err != nil {
			return nil, nil, err
		}
		desc.RateLimit = policy
		desc.QuotaMode = true
		return desc, []KeyedDescriptor{{
			ComparableKey: modelPrefix,
			Descriptor:    desc,
		}}, nil
	}

	var nested []*rlsconfv3.RateLimitDescriptor
	var keyed []KeyedDescriptor
	for rIdx, rule := range quota.BucketRules {
		ruleDescs, err := buildBucketRuleDescriptors(rIdx, &rule)
		if err != nil {
			return nil, nil, fmt.Errorf("bucket rule %d: %w", rIdx, err)
		}
		nested = append(nested, ruleDescs...)

		// Build comparable keys using semantic header names/values.
		headers := flattenAndSortHeaders(rule.ClientSelectors)
		leafKey := modelPrefix
		if len(headers) == 0 {
			leafKey += "/" + ComparableKeySegment("__catch_all", 2, "")
		} else {
			for depth, header := range headers {
				leafKey += "/" + ComparableKeySegment(header.Name, depth+2, headerComparableValue(header))
			}
		}
		for _, rd := range ruleDescs {
			keyed = append(keyed, KeyedDescriptor{
				ComparableKey: leafKey,
				Descriptor:    findLeafDescriptor(rd),
			})
		}
	}

	if quota.DefaultBucket.Limit > 0 {
		defaultPolicy, err := quotaValueToPolicy(&quota.DefaultBucket)
		if err != nil {
			return nil, nil, err
		}
		defaultKey := DefaultBucketDescriptorKey(len(quota.BucketRules))
		defaultDesc := &rlsconfv3.RateLimitDescriptor{
			Key:       defaultKey,
			Value:     defaultKey,
			RateLimit: defaultPolicy,
			QuotaMode: true,
		}
		nested = append(nested, defaultDesc)
		keyed = append(keyed, KeyedDescriptor{
			ComparableKey: modelPrefix + "/" + ComparableKeySegment("__default", 2, ""),
			Descriptor:    defaultDesc,
		})
	}

	desc.Descriptors = nested
	return desc, keyed, nil
}

// findLeafDescriptor walks a descriptor chain to find the deepest (leaf) descriptor.
func findLeafDescriptor(desc *rlsconfv3.RateLimitDescriptor) *rlsconfv3.RateLimitDescriptor {
	for len(desc.Descriptors) > 0 {
		desc = desc.Descriptors[0]
	}
	return desc
}

// buildServiceQuotaDescriptor creates a catch-all descriptor that applies to
// all models (when no PerModelQuota matches). Uses only the key without a
// specific value so that any model name will match.
func buildServiceQuotaDescriptor(sq *aigv1a1.ServiceQuotaDefinition) (*rlsconfv3.RateLimitDescriptor, error) {
	policy, err := quotaValueToPolicy(&sq.Quota)
	if err != nil {
		return nil, err
	}
	return &rlsconfv3.RateLimitDescriptor{
		Key:       ModelNameDescriptorKey,
		RateLimit: policy,
		QuotaMode: true,
	}, nil
}

// buildBucketRuleDescriptors creates a nested chain of descriptors for a single
// bucket rule. Header matches from all ClientSelectors are flattened, sorted by
// header name, and nested so that the rate limit service enforces AND logic at
// the descriptor level (matching the Envoy action chain order).
//
// Descriptor value strategy per match type:
//   - Distinct: key only (no value). The RequestHeaders action sends the actual header
//     value as the descriptor value; a fixed value entry would never match.
//   - Exact / Regex: key and value both set to the BucketRuleDescriptorKey. The
//     HeaderValueMatch action sends the fixed DescriptorValue (not the actual header
//     value), so the service config must match that same fixed string.
func buildBucketRuleDescriptors(ruleIndex int, rule *aigv1a1.QuotaRule) ([]*rlsconfv3.RateLimitDescriptor, error) {
	policy, err := quotaValueToPolicy(&rule.Quota)
	if err != nil {
		return nil, err
	}
	shadowMode := rule.ShadowMode != nil && *rule.ShadowMode

	// Flatten and sort all header matches across all ClientSelectors.
	allHeaders := flattenAndSortHeaders(rule.ClientSelectors)

	// No headers: single catch-all descriptor for this rule.
	if len(allHeaders) == 0 {
		key := BucketRuleDescriptorKey(ruleIndex, 0, "", "")
		return []*rlsconfv3.RateLimitDescriptor{{
			Key:        key,
			Value:      key,
			RateLimit:  policy,
			ShadowMode: shadowMode,
			QuotaMode:  true,
		}}, nil
	}

	// Build a nested chain of descriptors. The rate limit, shadow mode, and
	// quota mode are applied only to the leaf (deepest) descriptor.
	// Each level corresponds to one header match in sorted order.
	var root *rlsconfv3.RateLimitDescriptor
	var leaf *rlsconfv3.RateLimitDescriptor
	for mIdx, header := range allHeaders {
		key := BucketRuleDescriptorKey(ruleIndex, mIdx, header.Name, headerMatchValue(header))
		desc := &rlsconfv3.RateLimitDescriptor{Key: key}
		if header.Type == nil || *header.Type != egv1a1.HeaderMatchDistinct {
			desc.Value = key
		}
		if root == nil {
			root = desc
		} else {
			leaf.Descriptors = []*rlsconfv3.RateLimitDescriptor{desc}
		}
		leaf = desc
	}
	leaf.RateLimit = policy
	leaf.ShadowMode = shadowMode
	leaf.QuotaMode = true

	return []*rlsconfv3.RateLimitDescriptor{root}, nil
}

// headerMatchValue returns the value to include in a BucketRuleDescriptorKey for a header.
// Distinct headers return empty (the value is per-request, not known at config time).
// Exact/Regex headers return the configured value.
func headerMatchValue(header egv1a1.HeaderMatch) string {
	if header.Type != nil && *header.Type == egv1a1.HeaderMatchDistinct {
		return ""
	}
	if header.Value != nil {
		return *header.Value
	}
	return ""
}

// flattenAndSortHeaders collects all HeaderMatch entries from all ClientSelectors
// and sorts them by header Name for deterministic descriptor nesting order.
func flattenAndSortHeaders(selectors []egv1a1.RateLimitSelectCondition) []egv1a1.HeaderMatch {
	var headers []egv1a1.HeaderMatch
	for _, sel := range selectors {
		headers = append(headers, sel.Headers...)
	}
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].Name < headers[j].Name
	})
	return headers
}

func quotaValueToPolicy(qv *aigv1a1.QuotaValue) (*rlsconfv3.RateLimitPolicy, error) {
	unit, err := parseDuration(qv.Duration)
	if err != nil {
		return nil, fmt.Errorf("invalid duration %q: %w", qv.Duration, err)
	}
	return &rlsconfv3.RateLimitPolicy{
		RequestsPerUnit: uint32(qv.Limit), //nolint:gosec
		Unit:            unit,
	}, nil
}

// parseDuration accepts exactly "1s", "1m", "1h", "1d", "1w", "1mo", or "1y".
func parseDuration(s string) (rlsconfv3.RateLimitUnit, error) {
	switch s {
	case "1s":
		return rlsconfv3.RateLimitUnit_SECOND, nil
	case "1m":
		return rlsconfv3.RateLimitUnit_MINUTE, nil
	case "1h":
		return rlsconfv3.RateLimitUnit_HOUR, nil
	case "1d":
		return rlsconfv3.RateLimitUnit_DAY, nil
	case "1w":
		return rlsconfv3.RateLimitUnit_WEEK, nil
	case "1mo":
		return rlsconfv3.RateLimitUnit_MONTH, nil
	case "1y":
		return rlsconfv3.RateLimitUnit_YEAR, nil
	default:
		return 0, fmt.Errorf("unsupported duration %q: must be one of 1s, 1m, 1h, 1d, 1w, 1mo, 1y", s)
	}
}

// BackendNameFromDomain extracts the namespace and backend name from a BackendDomainValue string.
func BackendNameFromDomain(domain string) (namespace, name string, ok bool) {
	parts := strings.SplitN(domain, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
