// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	// LLMTokenCountPromptCacheWrite5m and LLMTokenCountPromptCacheWrite1h extend
	// the aggregate cache_creation attribute with provider-reported TTL subsets.
	// These are gateway extensions, not standardized OpenInference attributes.
	LLMTokenCountPromptCacheWrite5m = "llm.token_count.prompt_details.cache_creation_5m" // #nosec G101
	LLMTokenCountPromptCacheWrite1h = "llm.token_count.prompt_details.cache_creation_1h" // #nosec G101
)

// CacheTokenUsageRecorder records cache creation TTL counts independently of
// response content visibility. Missing counts stay absent; no split is inferred.
type CacheTokenUsageRecorder struct{}

// RecordCacheTokenUsage implements [tracingapi.CacheTokenUsageRecorder].
func (CacheTokenUsageRecorder) RecordCacheTokenUsage(span trace.Span, usage tracingapi.CacheTokenUsage) {
	if count, ok := usage.CacheCreation5mInputTokens(); ok {
		span.SetAttributes(attribute.Int64(LLMTokenCountPromptCacheWrite5m, int64(count)))
	}
	if count, ok := usage.CacheCreation1hInputTokens(); ok {
		span.SetAttributes(attribute.Int64(LLMTokenCountPromptCacheWrite1h, int64(count)))
	}
}
