// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// providerBySchema maps a backend API schema to the GenAI provider name.
//
// The keys mirror filterapi.APISchemaName. They are strings rather than the
// typed constants so this package does not depend on filterapi, matching the
// reason tracingapi.Backend carries strings.
//
// The same mapping exists in internal/metrics/metrics_impl.go for metric
// attributes. It is duplicated rather than shared because the metrics values
// are an established contract for existing dashboards: coupling them would mean
// a change here silently altering metrics. Keep the two in sync.
var providerBySchema = map[string]Provider{
	"OpenAI":       ProviderOpenAI,
	"AzureOpenAI":  ProviderAzureOpenAI,
	"AWSBedrock":   ProviderAWSBedrock,
	"AWSAnthropic": ProviderAWSAnthropic,
	"GCPVertexAI":  ProviderGCPVertexAI,
	"GCPAnthropic": ProviderGCPAnthropic,
	"Anthropic":    ProviderAnthropic,
	"Cohere":       ProviderCohere,
}

// ProviderForSchema returns the GenAI provider name for a backend API schema.
//
// Schemas with no well-known provider fall back to the configured backend name,
// which the conventions permit as a custom value.
func ProviderForSchema(schema, backendName string) string {
	if p, ok := providerBySchema[schema]; ok {
		return string(p)
	}
	return backendName
}

// RecordBackend implements tracingapi.BackendRecorder.
//
// gen_ai.provider.name is a required attribute but is only knowable after
// routing, so it is recorded here rather than at request time.
//
// The conventions also describe server.address and server.port, but neither is
// recorded: Envoy resolves the upstream cluster after ExtProc runs, so the only
// host visible here is the gateway's own listener. Reporting that would name the
// wrong server.
func (r *recorder[ReqT, RespT, ChunkT]) RecordBackend(span trace.Span, backend tracingapi.Backend) {
	if provider := ProviderForSchema(backend.Schema, backend.Name); provider != "" {
		span.SetAttributes(attribute.String(ProviderName, provider))
	}
}
