// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

var _ tracingapi.Tracing = (*tracingImpl)(nil)

type tracingImpl struct {
	chatCompletionTracer       tracingapi.ChatCompletionTracer
	completionTracer           tracingapi.CompletionTracer
	imageGenerationTracer      tracingapi.ImageGenerationTracer
	embeddingsTracer           tracingapi.EmbeddingsTracer
	responsesTracer            tracingapi.ResponsesTracer
	speechTracer               tracingapi.SpeechTracer
	transcriptionTracer        tracingapi.TranscriptionTracer
	translationTracer          tracingapi.TranslationTracer
	rerankTracer               tracingapi.RerankTracer
	messageTracer              tracingapi.MessageTracer
	tokenizeTracer             tracingapi.TokenizeTracer
	responsesInputTokensTracer tracingapi.ResponsesInputTokensTracer
	countTokensTracer          tracingapi.CountTokensTracer
	mcpTracer                  tracingapi.MCPTracer
	// shutdown is nil when we didn't create tp.
	shutdown func(context.Context) error
}

// ChatCompletionTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) ChatCompletionTracer() tracingapi.ChatCompletionTracer {
	return t.chatCompletionTracer
}

// CompletionTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) CompletionTracer() tracingapi.CompletionTracer {
	return t.completionTracer
}

// EmbeddingsTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) EmbeddingsTracer() tracingapi.EmbeddingsTracer {
	return t.embeddingsTracer
}

// ImageGenerationTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) ImageGenerationTracer() tracingapi.ImageGenerationTracer {
	return t.imageGenerationTracer
}

// ResponsesTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) ResponsesTracer() tracingapi.ResponsesTracer {
	return t.responsesTracer
}

// SpeechTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) SpeechTracer() tracingapi.SpeechTracer {
	return t.speechTracer
}

// TranscriptionTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) TranscriptionTracer() tracingapi.TranscriptionTracer {
	return t.transcriptionTracer
}

// TranslationTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) TranslationTracer() tracingapi.TranslationTracer {
	return t.translationTracer
}

// RerankTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) RerankTracer() tracingapi.RerankTracer {
	return t.rerankTracer
}

// MCPTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) MCPTracer() tracingapi.MCPTracer {
	return t.mcpTracer
}

// MessageTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) MessageTracer() tracingapi.MessageTracer {
	return t.messageTracer
}

// TokenizeTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) TokenizeTracer() tracingapi.TokenizeTracer {
	return t.tokenizeTracer
}

// ResponsesInputTokensTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) ResponsesInputTokensTracer() tracingapi.ResponsesInputTokensTracer {
	return t.responsesInputTokensTracer
}

// CountTokensTracer implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) CountTokensTracer() tracingapi.CountTokensTracer {
	return t.countTokensTracer
}

// Shutdown implements the same method as documented on tracingapi.Tracing.
func (t *tracingImpl) Shutdown(ctx context.Context) error {
	if t.shutdown != nil {
		return t.shutdown(ctx)
	}
	return nil
}

// NewTracingFromEnv configures OpenTelemetry tracing based on environment
// variables and optional header attribute mapping.
//
// Parameters:
//   - headerAttributeMapping: maps HTTP headers to otel span attributes (e.g. map["agent-session-id"]="session.id").
//     If nil, no header mapping is applied.
//
// Returns a tracing graph that is noop when disabled.
func NewTracingFromEnv(ctx context.Context, stdout io.Writer, headerAttributeMapping map[string]string) (tracingapi.Tracing, error) {
	// Return no-op tracing if disabled.
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return tracingapi.NoopTracing{}, nil
	}

	// Check for traces-specific exporter first.
	exporter := os.Getenv("OTEL_TRACES_EXPORTER")
	if exporter == "none" {
		return tracingapi.NoopTracing{}, nil
	}

	// If no traces-specific exporter is set, check if OTLP endpoints are configured.
	// According to OTEL spec, we should use OTLP if any endpoint is configured.
	// The autoexport library will handle the endpoint precedence correctly:
	// 1. OTEL_EXPORTER_OTLP_TRACES_ENDPOINT (traces-specific)
	// 2. OTEL_EXPORTER_OTLP_ENDPOINT (generic base endpoint).
	if exporter == "" {
		hasOTLPEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
			os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""

		if !hasOTLPEndpoint {
			// No tracing configured.
			return tracingapi.NoopTracing{}, nil
		}
		// Fall through to use autoexport which will handle OTLP configuration.
	}

	// Resolve the semantic convention before building the SDK, because the
	// convention determines the span attribute limits below.
	recorders, err := newRecordersFromEnv()
	if err != nil {
		return nil, err
	}

	// Create resource with service name, defaulting to "ai-gateway" if not set.
	// First create default resource, then one from env, then our fallback.
	// The merge order ensures env vars override our default.
	defaultRes := resource.Default()
	envRes, err := resource.New(ctx,
		resource.WithFromEnv(),      // Read OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
		resource.WithTelemetrySDK(), // Add telemetry SDK info.
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource from env: %w", err)
	}

	// Only set our default if service.name wasn't set via env
	// We hardcode "service.name" to avoid pinning semconv version.
	fallbackRes := resource.NewSchemaless(
		attribute.String("service.name", "ai-gateway"),
	)

	// Merge in order: default -> fallback -> env (env takes precedence).
	res, err := resource.Merge(defaultRes, fallbackRes)
	if err != nil {
		return nil, fmt.Errorf("failed to merge default resources: %w", err)
	}
	res, err = resource.Merge(res, envRes)
	if err != nil {
		return nil, fmt.Errorf("failed to merge env resource: %w", err)
	}

	// Indexed message attributes scale with conversation length and exceed
	// OTEL's default cap of 128, silently truncating spans. Lift the cap only
	// for conventions that emit them, so the others retain OTEL defaults.
	spanLimits := sdktrace.NewSpanLimits()
	if recorders.unboundedAttributeCount {
		spanLimits.AttributeCountLimit = -1
	}

	// Create the tracer provider, special casing console for sync and tests.
	var tp *sdktrace.TracerProvider
	if exporter == "console" {
		stdoutExporter, err := stdouttrace.New(stdouttrace.WithWriter(stdout))
		if err != nil {
			return nil, fmt.Errorf("failed to create console exporter: %w", err)
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithSyncer(stdoutExporter),
			sdktrace.WithResource(res),
			sdktrace.WithRawSpanLimits(spanLimits),
		)

	} else { // Configure exporter via ENV variables like OTEL_TRACES_EXPORTER.
		autoExporter, err := autoexport.NewSpanExporter(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create exporter: %w", err)
		}
		// Configure batcher via ENV variables like OTEL_BSP_SCHEDULE_DELAY.
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(autoExporter),
			sdktrace.WithResource(res),
			sdktrace.WithRawSpanLimits(spanLimits),
		)
	}

	// Configure propagation via the OTEL_PROPAGATORS ENV variable.
	propagator := autoprop.NewTextMapPropagator()

	// Use provided header attribute mapping.
	headerAttrs := headerAttributeMapping

	var tracer trace.Tracer = tp.Tracer("envoyproxy/ai-gateway")
	// See the constant's doc for why a caller's remote context may be
	// recorded as a link on a new root span instead of as the parent.
	if os.Getenv(envRemoteParentAsLink) == "true" {
		tracer = remoteParentAsLinkTracer{delegate: tracer}
	}
	return &tracingImpl{
		chatCompletionTracer: newChatCompletionTracer(
			tracer,
			propagator,
			recorders.chatCompletion,
			headerAttrs,
		),
		imageGenerationTracer: newImageGenerationTracer(
			tracer,
			propagator,
			recorders.imageGeneration,
		),
		completionTracer: newCompletionTracer(
			tracer,
			propagator,
			recorders.completion,
			headerAttrs,
		),
		embeddingsTracer: newEmbeddingsTracer(
			tracer,
			propagator,
			recorders.embeddings,
			headerAttrs,
		),
		responsesTracer: newResponsesTracer(
			tracer,
			propagator,
			recorders.responses,
			headerAttrs,
		),
		speechTracer: newSpeechTracer(
			tracer,
			propagator,
			recorders.speech,
			headerAttrs,
		),
		transcriptionTracer: newTranscriptionTracer(
			tracer,
			propagator,
			recorders.transcription,
			headerAttrs,
		),
		translationTracer: newTranslationTracer(
			tracer,
			propagator,
			recorders.translation,
			headerAttrs,
		),
		rerankTracer: newRerankTracer(
			tracer,
			propagator,
			recorders.rerank,
			headerAttrs,
		),
		messageTracer: newMessageTracer(
			tracer,
			propagator,
			recorders.message,
			headerAttrs,
		),
		tokenizeTracer: newTokenizeTracer(
			tracer,
			propagator,
			recorders.tokenize,
			headerAttrs,
		),
		responsesInputTokensTracer: newResponsesInputTokensTracer(
			tracer,
			propagator,
			recorders.responsesInputTokens,
			headerAttrs,
		),
		countTokensTracer: newCountTokensTracer(
			tracer,
			propagator,
			recorders.countTokens,
			headerAttrs,
		),
		mcpTracer: newMCPTracer(tracer, propagator, headerAttrs, recorders.mcp),
		shutdown:  tp.Shutdown, // we have to shut down what we create.
	}, nil
}
