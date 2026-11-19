// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewGeminiGenerateContentRecorder records metadata for native Gemini generation.
func NewGeminiGenerateContentRecorder(config *Config) tracingapi.GeminiGenerateContentRecorder {
	return &recorder[gcp.GeminiGenerateContentRequest, gcp.GeminiGenerateContentResponse, gcp.GeminiGenerateContentResponse]{
		operation:     OperationGenerateContent,
		config:        configOrEnv(config),
		requestModel:  func(r *gcp.GeminiGenerateContentRequest) string { return r.Model },
		responseAttrs: geminiResponseAttrs,
		chunkAttrs: func(chunks []*gcp.GeminiGenerateContentResponse) []attribute.KeyValue {
			if len(chunks) == 0 {
				return nil
			}
			return geminiResponseAttrs(chunks[len(chunks)-1])
		},
	}
}

func geminiResponseAttrs(resp *gcp.GeminiGenerateContentResponse) []attribute.KeyValue {
	attrs := responseIdentityAttrs(resp.ResponseID, resp.ModelVersion)
	if usage := resp.UsageMetadata; usage != nil {
		input, output, _ := gcp.GeminiUsageCounts(usage)
		attrs = append(attrs, usageAttrs(input, output)...)
		attrs = append(attrs,
			attribute.Int(UsageCacheReadInputTokens, int(usage.CachedContentTokenCount)),
			attribute.Int(UsageReasoningOutputTokens, int(usage.ThoughtsTokenCount)),
		)
	}
	return attrs
}
