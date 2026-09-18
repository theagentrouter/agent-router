// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// metricsImplFactory implements the Factory interface for creating metricsImpl instances.
type metricsImplFactory struct {
	metrics                       *genAI
	requestHeaderAttributeMapping map[string]string // maps HTTP headers to metric attribute names.
	operation                     string
}

// NewMetrics implements [Factory.NewMetrics].
func (f *metricsImplFactory) NewMetrics() Metrics {
	return &metricsImpl{
		metrics:                       f.metrics,
		operation:                     f.operation,
		originalModel:                 "unknown",
		requestModel:                  "unknown",
		responseModel:                 "unknown",
		backend:                       "unknown",
		aiServiceBackend:              "unknown",
		requestHeaderAttributeMapping: f.requestHeaderAttributeMapping,
	}
}

// metricsImpl provides shared functionality for AI Gateway metrics implementations.
//
// This implements the Metrics interface.
type metricsImpl struct {
	metrics      *genAI
	operation    string
	requestStart time.Time
	// originalModel is the model name extracted from the incoming request body before any virtualization applies.
	originalModel string
	// requestModel is the original model from the request body.
	requestModel string
	// responseModel is the model that ultimately generated the response (may differ due to backend override).
	responseModel                 string
	backend                       string
	aiServiceBackend              string
	requestHeaderAttributeMapping map[string]string // maps HTTP headers to metric attribute names.

	// Fields for streaming token latency calculation, not used for non-streaming requests.

	firstTokenSent       bool
	timeToFirstToken     time.Duration // Duration to first token.
	interTokenLatencySec float64       // Average time per token after first, in seconds.
	totalOutputTokens    uint32
}

// StartRequest initializes timing for a new request.
func (b *metricsImpl) StartRequest(_ map[string]string) {
	b.requestStart = time.Now()
}

// SetOriginalModel sets the original model from the incoming request body before any virtualization applies.
// This is usually called after parsing the request body. e.g. gpt-5
func (b *metricsImpl) SetOriginalModel(originalModel internalapi.OriginalModel) {
	b.originalModel = originalModel
}

// SetRequestModel sets the model the request. This is usually called after parsing the request body. e.g. gpt-5-nano
func (b *metricsImpl) SetRequestModel(requestModel internalapi.RequestModel) {
	b.requestModel = requestModel
}

// SetResponseModel is the model that ultimately generated the response. e.g. gpt-5-nano-2025-08-07
func (b *metricsImpl) SetResponseModel(responseModel internalapi.ResponseModel) {
	b.responseModel = responseModel
}

// SetBackend sets the name of the backend to be reported in the metrics according to:
// https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/
func (b *metricsImpl) SetBackend(backend *filterapi.Backend) {
	switch backend.Schema.Name {
	case filterapi.APISchemaOpenAI:
		b.backend = genaiProviderOpenAI
	case filterapi.APISchemaAzureOpenAI:
		b.backend = genaiProviderAzureOpenAI
	case filterapi.APISchemaAWSBedrock:
		b.backend = genaiProviderAWSBedrock
	case filterapi.APISchemaAWSAnthropic:
		b.backend = genaiProviderAWSAnthropic
	case filterapi.APISchemaGCPVertexAI:
		b.backend = genaiProviderGCPVertexAI
	case filterapi.APISchemaGCPAnthropic:
		b.backend = genaiProviderGCPAnthropic
	case filterapi.APISchemaAnthropic:
		b.backend = genaiProviderAnthropic
	case filterapi.APISchemaCohere:
		b.backend = genaiProviderCohere
	default:
		b.backend = backend.Name
	}
	b.aiServiceBackend = internalapi.AIServiceBackendName(backend.Name)
}

// buildBaseAttributes creates the base attributes for metrics recording.
func (b *metricsImpl) buildBaseAttributes(headers map[string]string) attribute.Set {
	opt := attribute.Key(genaiAttributeOperationName).String(b.operation)
	provider := attribute.Key(genaiAttributeProviderName).String(b.backend)
	origModel := attribute.Key(genaiAttributeOriginalModel).String(b.originalModel)
	reqModel := attribute.Key(genaiAttributeRequestModel).String(b.requestModel)
	respModel := attribute.Key(genaiAttributeResponseModel).String(b.responseModel)
	backend := attribute.Key(genaiAttributeBackend).String(b.aiServiceBackend)
	if len(b.requestHeaderAttributeMapping) == 0 {
		return attribute.NewSet(opt, provider, origModel, reqModel, respModel, backend)
	}

	// Add header values as attributes based on the header mapping if headers are provided.
	attrs := []attribute.KeyValue{opt, provider, origModel, reqModel, respModel, backend}
	for headerName, labelName := range b.requestHeaderAttributeMapping {
		if headerValue, exists := headers[headerName]; exists {
			attrs = append(attrs, attribute.Key(labelName).String(headerValue))
		}
	}
	return attribute.NewSet(attrs...)
}

// RecordRequestCompletion records the completion of a request with success/failure status.
func (b *metricsImpl) RecordRequestCompletion(ctx context.Context, success bool, requestHeaders map[string]string) {
	attrs := b.buildBaseAttributes(requestHeaders)

	if success {
		// According to the semantic conventions, the error attribute should not be added for successful operations.
		b.metrics.requestLatency.Record(ctx, time.Since(b.requestStart).Seconds(), metric.WithAttributeSet(attrs))
	} else {
		// We don't have a set of typed errors yet, or a set of low-cardinality values, so we can just set the value to the
		// placeholder one. See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/error/#error-type
		b.metrics.requestLatency.Record(ctx, time.Since(b.requestStart).Seconds(),
			metric.WithAttributeSet(attrs),
			metric.WithAttributes(attribute.Key(genaiAttributeErrorType).String(genaiErrorTypeFallback)),
		)
	}
}

// RecordTokenUsage records token usage metrics.
func (b *metricsImpl) RecordTokenUsage(ctx context.Context, usage TokenUsage, requestHeaders map[string]string) {
	attrs := b.buildBaseAttributes(requestHeaders)

	if inputTokens, ok := usage.InputTokens(); ok {
		b.metrics.tokenUsage.Record(ctx, float64(inputTokens),
			metric.WithAttributeSet(attrs),
			metric.WithAttributes(attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeInput)),
		)
	}
	if cachedInputTokens, ok := usage.CachedInputTokens(); ok {
		b.metrics.tokenUsage.Record(ctx, float64(cachedInputTokens),
			metric.WithAttributeSet(attrs),
			metric.WithAttributes(attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeCachedInput)),
		)
	}
	if cacheCreationInputTokens, ok := usage.CacheCreationInputTokens(); ok {
		b.metrics.tokenUsage.Record(ctx, float64(cacheCreationInputTokens),
			metric.WithAttributeSet(attrs),
			metric.WithAttributes(attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeCacheCreationInput)),
		)
	}
	if outputTokens, ok := usage.OutputTokens(); ok {
		b.metrics.tokenUsage.Record(ctx, float64(outputTokens),
			metric.WithAttributeSet(attrs),
			metric.WithAttributes(attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeOutput)),
		)
	}
	if reasoningTokens, ok := usage.ReasoningTokens(); ok {
		b.metrics.tokenUsage.Record(ctx, float64(reasoningTokens),
			metric.WithAttributeSet(attrs),
			metric.WithAttributes(attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeReasoning)),
		)
	}
}

// GetTimeToFirstTokenMs implements [Metrics.GetTimeToFirstTokenMs].
func (b *metricsImpl) GetTimeToFirstTokenMs() float64 {
	return float64(b.timeToFirstToken.Milliseconds())
}

// GetInterTokenLatencyMs implements [Metrics.GetInterTokenLatencyMs].
func (b *metricsImpl) GetInterTokenLatencyMs() float64 {
	return b.interTokenLatencySec * 1000
}

// RecordTokenLatency implements [CompletionMetrics.RecordTokenLatency].
func (b *metricsImpl) RecordTokenLatency(ctx context.Context, tokens uint32, endOfStream bool, requestHeaders map[string]string) {
	attrs := b.buildBaseAttributes(requestHeaders)

	// Record time to first token on the first call for streaming responses.
	// This ensures we capture the metric even when token counts aren't available in streaming chunks.
	if !b.firstTokenSent {
		b.firstTokenSent = true
		b.timeToFirstToken = time.Since(b.requestStart)
		b.metrics.firstTokenLatency.Record(ctx, b.timeToFirstToken.Seconds(), metric.WithAttributeSet(attrs))
		return
	}

	// Track max cumulative tokens across the stream.
	if tokens > b.totalOutputTokens {
		b.totalOutputTokens = tokens
	}

	// Record once at end-of-stream using average from first token.
	// Per OTEL spec: time_per_output_token = (request_duration - time_to_first_token) / (output_tokens - 1).
	// This measures the average time for ALL tokens after the first one, not just after the first chunk.
	if endOfStream && b.totalOutputTokens > 1 {
		// Calculate time elapsed since first token was sent.
		currentElapsed := time.Since(b.requestStart)
		timeSinceFirstToken := currentElapsed - b.timeToFirstToken
		// Divide by (total_tokens - 1) as per spec, not by tokens after first chunk.
		b.interTokenLatencySec = timeSinceFirstToken.Seconds() / float64(b.totalOutputTokens-1)
		b.metrics.outputTokenLatency.Record(ctx, b.interTokenLatencySec, metric.WithAttributeSet(attrs))
	}
}
