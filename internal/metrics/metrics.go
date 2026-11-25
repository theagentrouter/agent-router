// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"context"
	"io"
	"os"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// NewMeterFromEnv configures an OpenTelemetry MeterProvider based on environment variables,
// always incorporating the provided Prometheus reader. It optionally includes additional exporters
// (e.g., console or OTLP) if enabled via environment variables. The function returns a metric.Meter
// for instrumentation and a shutdown function to gracefully close the provider.
//
// The stdout parameter directs output for the console exporter (use os.Stdout in production).
// Environment variables checked directly include:
//   - OTEL_SDK_DISABLED: If "true", disables OTEL exporters.
//   - OTEL_METRICS_EXPORTER: Supported values are "none", "console", "prometheus", "otlp".
//   - OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT: Enables OTLP if set.
//
// Prometheus is always enabled via the provided promReader; other exporters are added conditionally.
func NewMeterFromEnv(ctx context.Context, stdout io.Writer, promReader sdkmetric.Reader) (metric.Meter, func(context.Context) error, error) {
	// Initialize options for the MeterProvider, starting with the required Prometheus reader.
	var options []sdkmetric.Option
	options = append(options, sdkmetric.WithReader(promReader))

	// Add OTEL exporters only if the SDK is not disabled.
	if os.Getenv("OTEL_SDK_DISABLED") != "true" {
		exporter := os.Getenv("OTEL_METRICS_EXPORTER")
		hasOTLPEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
			os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != ""

		// Proceed if exporter is "console" or if OTLP is implied (not "none" or "prometheus" with endpoint set).
		if exporter == "console" || (exporter != "none" && exporter != "prometheus" && hasOTLPEndpoint) {
			// Configure resource with default attributes, fallback service name, and environment overrides.
			defaultRes := resource.Default()
			envRes, err := resource.New(ctx,
				resource.WithFromEnv(),
				resource.WithTelemetrySDK(),
			)
			if err != nil {
				return nil, nil, err
			}
			// Ensure a service name is set if not provided via environment.
			// We hardcode "service.name" to avoid pinning semconv version.
			fallbackRes := resource.NewSchemaless(
				attribute.String("service.name", "ai-gateway"),
			)
			res, err := resource.Merge(defaultRes, fallbackRes)
			if err != nil {
				return nil, nil, err
			}
			res, err = resource.Merge(res, envRes)
			if err != nil {
				return nil, nil, err
			}
			options = append(options, sdkmetric.WithResource(res))

			if exporter == "console" {
				// Configure console exporter with a PeriodicReader for aggregated metric export.
				exp, err := newNonEmptyConsoleExporter(stdout)
				if err != nil {
					return nil, nil, err
				}
				reader := sdkmetric.NewPeriodicReader(exp)
				options = append(options, sdkmetric.WithReader(reader))
			} else {
				// Use autoexport for OTLP, which internally handles PeriodicReader for aggregation.
				otelReader, err := autoexport.NewMetricReader(ctx)
				if err != nil {
					return nil, nil, err
				}
				options = append(options, sdkmetric.WithReader(otelReader))
			}
		}
	}

	// Create and return the MeterProvider with all configured options.
	mp := sdkmetric.NewMeterProvider(options...)
	return mp.Meter("envoyproxy/ai-gateway"), mp.Shutdown, nil
}

// Metrics is the interface for the base AI Gateway metrics.
type Metrics interface {
	// StartRequest initializes timing for a new request.
	StartRequest(headers map[string]string)
	// SetOriginalModel sets the original model from the incoming request body before any virtualization applies.
	// This is usually called after parsing the request body. Example: gpt-5
	SetOriginalModel(originalModel internalapi.OriginalModel)
	// SetRequestModel sets the model from the request. This is usually called after parsing the request body.
	// Example: gpt-5-nano
	SetRequestModel(requestModel internalapi.RequestModel)
	// SetResponseModel sets the model that ultimately generated the response.
	// Example: gpt-5-nano-2025-08-07
	SetResponseModel(responseModel internalapi.ResponseModel)
	// SetBackend sets the selected backend when the routing decision has been made. This is usually called
	// after parsing the request body to determine the model and invoke the routing logic.
	SetBackend(backend *filterapi.Backend)
	// RecordRequestCompletion records the completion of the request, including success status.
	RecordRequestCompletion(ctx context.Context, success bool, requestHeaders map[string]string)
	// RecordTokenUsage records token usage metrics.
	//
	// Depending on the endpoint, some token types are not available and should be passed as OptUint32None.
	RecordTokenUsage(ctx context.Context, usage TokenUsage, requestHeaders map[string]string)

	// Streaming-specific metrics methods, not used by all implementations.

	// GetTimeToFirstTokenMs returns the time to first token in stream mode in milliseconds.
	GetTimeToFirstTokenMs() float64
	// GetInterTokenLatencyMs returns the inter token latency in stream mode in milliseconds.
	GetInterTokenLatencyMs() float64
	// RecordTokenLatency records latency metrics for token generation.
	RecordTokenLatency(ctx context.Context, accumulatedOutputToken uint32, endOfStream bool, requestHeaders map[string]string)
}

// Factory is a closure that creates a new Metrics instance for a given operation.
type Factory interface {
	// NewMetrics creates a new Metrics instance for the specified operation name.
	NewMetrics() Metrics
}

// NewMetricsFactory returns a Factory to create a new Metrics instance.
func NewMetricsFactory(meter metric.Meter, requestHeaderLabelMapping map[string]string, operation GenAIOperation) Factory {
	return &metricsImplFactory{metrics: newGenAI(meter), requestHeaderAttributeMapping: requestHeaderLabelMapping, operation: string(operation)}
}

// TokenUsage represents the token usage reported usually by the backend API in the response body.
//
// Fields are not exported to control the optionality of each field via the accompanying boolean flags.
type TokenUsage struct {
	// InputTokens is the number of tokens consumed from the input.
	inputTokens uint32
	// OutputTokens is the number of tokens consumed from the output.
	outputTokens uint32
	// TotalTokens is the total number of tokens consumed.
	totalTokens uint32
	// CachedInputTokens is the total number of tokens read from cache.
	cachedInputTokens uint32
	// CacheCreationInputTokens is the total number of tokens written to cache.
	cacheCreationInputTokens uint32
	// ReasoningTokens is the number of reasoning tokens consumed.
	reasoningTokens uint32

	inputTokenSet, outputTokenSet, totalTokenSet, cachedInputTokenSet, cacheCreationInputTokenSet, reasoningTokenSet bool
}

// InputTokens returns the number of input tokens and whether it was set.
func (u *TokenUsage) InputTokens() (uint32, bool) {
	return u.inputTokens, u.inputTokenSet
}

// OutputTokens returns the number of output tokens and whether it was set.
func (u *TokenUsage) OutputTokens() (uint32, bool) {
	return u.outputTokens, u.outputTokenSet
}

// TotalTokens returns the number of total tokens and whether it was set.
func (u *TokenUsage) TotalTokens() (uint32, bool) {
	return u.totalTokens, u.totalTokenSet
}

// CachedInputTokens returns the number of cached input tokens and whether it was set.
func (u *TokenUsage) CachedInputTokens() (uint32, bool) {
	return u.cachedInputTokens, u.cachedInputTokenSet
}

// CacheCreationInputTokens returns the number of cache creation input tokens and whether it was set.
func (u *TokenUsage) CacheCreationInputTokens() (uint32, bool) {
	return u.cacheCreationInputTokens, u.cacheCreationInputTokenSet
}

// SetInputTokens sets the number of input tokens and marks the field as set.
func (u *TokenUsage) SetInputTokens(tokens uint32) {
	u.inputTokens = tokens
	u.inputTokenSet = true
}

// SetOutputTokens sets the number of output tokens and marks the field as set.
func (u *TokenUsage) SetOutputTokens(tokens uint32) {
	u.outputTokens = tokens
	u.outputTokenSet = true
}

// SetTotalTokens sets the number of total tokens and marks the field as set.
func (u *TokenUsage) SetTotalTokens(tokens uint32) {
	u.totalTokens = tokens
	u.totalTokenSet = true
}

// SetCachedInputTokens sets the number of cached input tokens and marks the field as set.
func (u *TokenUsage) SetCachedInputTokens(tokens uint32) {
	u.cachedInputTokens = tokens
	u.cachedInputTokenSet = true
}

// SetCacheCreationInputTokens sets the number of cache creation input tokens and marks the field as set.
func (u *TokenUsage) SetCacheCreationInputTokens(tokens uint32) {
	u.cacheCreationInputTokens = tokens
	u.cacheCreationInputTokenSet = true
}

// ReasoningTokens returns the number of reasoning tokens and whether it was set.
func (u *TokenUsage) ReasoningTokens() (uint32, bool) {
	return u.reasoningTokens, u.reasoningTokenSet
}

// SetReasoningTokens sets the number of reasoning tokens and marks the field as set.
func (u *TokenUsage) SetReasoningTokens(tokens uint32) {
	u.reasoningTokens = tokens
	u.reasoningTokenSet = true
}

// AddInputTokens increments the recorded input tokens and marks the field as set.
func (u *TokenUsage) AddInputTokens(tokens uint32) {
	u.inputTokenSet = true
	u.inputTokens += tokens
}

// AddOutputTokens increments the recorded output tokens and marks the field as set.
func (u *TokenUsage) AddOutputTokens(tokens uint32) {
	u.outputTokenSet = true
	u.outputTokens += tokens
}

// AddCachedInputTokens increments the recorded cached input tokens and marks the field as set.
func (u *TokenUsage) AddCachedInputTokens(tokens uint32) {
	u.cachedInputTokenSet = true
	u.cachedInputTokens += tokens
}

// AddCacheCreationInputTokens increments the recorded cache creation input tokens and marks the field as set.
func (u *TokenUsage) AddCacheCreationInputTokens(tokens uint32) {
	u.cacheCreationInputTokenSet = true
	u.cacheCreationInputTokens += tokens
}

// AddReasoningTokens increments the recorded reasoning tokens and marks the field as set.
func (u *TokenUsage) AddReasoningTokens(tokens uint32) {
	u.reasoningTokenSet = true
	u.reasoningTokens += tokens
}

// IsZero reports whether no token usage field has been set.
func (u *TokenUsage) IsZero() bool {
	return !u.inputTokenSet && !u.outputTokenSet && !u.totalTokenSet &&
		!u.cachedInputTokenSet && !u.cacheCreationInputTokenSet && !u.reasoningTokenSet
}

// Override updates the TokenUsage fields with values from another TokenUsage instance.
// Only fields that are marked as set in the other instance will override the current values.
func (u *TokenUsage) Override(other TokenUsage) {
	if other.inputTokenSet {
		u.inputTokens = other.inputTokens
		u.inputTokenSet = true
	}
	if other.outputTokenSet {
		u.outputTokens = other.outputTokens
		u.outputTokenSet = true
	}
	if other.totalTokenSet {
		u.totalTokens = other.totalTokens
		u.totalTokenSet = true
	}
	if other.cachedInputTokenSet {
		u.cachedInputTokens = other.cachedInputTokens
		u.cachedInputTokenSet = true
	}
	if other.cacheCreationInputTokenSet {
		u.cacheCreationInputTokens = other.cacheCreationInputTokens
		u.cacheCreationInputTokenSet = true
	}
	if other.reasoningTokenSet {
		u.reasoningTokens = other.reasoningTokens
		u.reasoningTokenSet = true
	}
}

// ExtractTokenUsageFromExplicitCaching extracts the correct token usage from upstream Anthropic or AWS Bedrock token usage response.
// The total input tokens is the summation of:
// input_tokens + cache_creation_input_tokens + cache_read_input_tokens
// This is to unify the usage response returned by envoy ai gateway for both explicit and implicit caching.
//
// This function works for both streaming and non-streaming responses by accepting
// the common usage fields that exist from anthropic or AWS bedrock usage structures.
func ExtractTokenUsageFromExplicitCaching(inputTokens, outputTokens int64, cacheReadTokens, cacheCreationTokens *int64) TokenUsage {
	var usage TokenUsage
	totalInputTokens := inputTokens
	if cacheCreationTokens != nil {
		totalInputTokens += *cacheCreationTokens
		usage.SetCacheCreationInputTokens(uint32(*cacheCreationTokens)) //nolint:gosec
	}
	if cacheReadTokens != nil {
		totalInputTokens += *cacheReadTokens
		usage.SetCachedInputTokens(uint32(*cacheReadTokens)) //nolint:gosec
	}
	usage.SetInputTokens(uint32(totalInputTokens))                //nolint:gosec
	usage.SetOutputTokens(uint32(outputTokens))                   //nolint:gosec
	usage.SetTotalTokens(uint32(totalInputTokens + outputTokens)) //nolint:gosec
	return usage
}
