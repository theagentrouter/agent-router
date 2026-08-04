// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package cohere provides OpenInference semantic conventions hooks for
// Cohere instrumentation used by the ExtProc router filter.
package cohere

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// EmbedRecorder implements recorders for Cohere Embed spans.
type EmbedRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
	traceConfig *openinference.TraceConfig
}

// NewEmbedRecorderFromEnv creates an tracingapi.EmbedRecorder from environment variables
// using the OpenInference configuration specification.
func NewEmbedRecorderFromEnv() tracingapi.EmbedRecorder {
	return NewEmbedRecorder(nil)
}

// NewEmbedRecorder creates a tracingapi.EmbedRecorder with the given config using
// the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
func NewEmbedRecorder(config *openinference.TraceConfig) tracingapi.EmbedRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &EmbedRecorder{traceConfig: config}
}

// startOptsEmbed sets trace.SpanKindInternal as that's the span kind used in OpenInference.
var startOptsEmbed = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.EmbedRecorder.
func (r *EmbedRecorder) StartParams(*cohereschema.EmbedV2Request, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Embed", startOptsEmbed
}

// RecordRequest implements the same method as defined in tracingapi.EmbedRecorder.
func (r *EmbedRecorder) RecordRequest(span trace.Span, req *cohereschema.EmbedV2Request, body []byte) {
	span.SetAttributes(buildEmbedRequestAttributes(req, body, r.traceConfig)...)
}

// RecordResponseOnError implements the same method as defined in tracingapi.EmbedRecorder.
func (r *EmbedRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.EmbedRecorder.
func (r *EmbedRecorder) RecordResponse(span trace.Span, resp *cohereschema.EmbedV2Response) {
	// Build response attributes (excluding output.value) similar to embeddings.
	attrs := buildEmbedResponseAttributes(resp, r.traceConfig)

	// Add output.value respecting HideOutputs.
	bodyString := openinference.RedactedValue
	if !r.traceConfig.HideOutputs {
		if marshaled, err := json.Marshal(resp); err == nil {
			bodyString = string(marshaled)
		}
	}
	attrs = append(attrs, attribute.String(openinference.OutputValue, bodyString))

	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}

// buildEmbedRequestAttributes builds OpenInference attributes from the embed request.
func buildEmbedRequestAttributes(req *cohereschema.EmbedV2Request, body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemCohere),
		attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
	}

	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.EmbeddingModelName, req.Model))
	}

	if !config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, string(body)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	// Record embedding text attributes for string inputs only, consistent with OpenAI embeddings.
	if !config.HideInputs && !config.HideEmbeddingsText {
		for i, text := range req.Texts {
			attrs = append(attrs, attribute.String(openinference.EmbeddingTextAttribute(i), text))
		}
	}

	return attrs
}

// buildEmbedResponseAttributes builds OpenInference attributes from the embed response.
func buildEmbedResponseAttributes(resp *cohereschema.EmbedV2Response, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	// Include output MIME type only when outputs are not hidden.
	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))

		// Record embedding vectors as float arrays.
		// TODO: Consider supporting other embedding types (int8, uint8, binary, etc.)
		if !config.HideEmbeddingsVectors && resp.Embeddings != nil {
			for i, vec := range resp.Embeddings.Float {
				if len(vec) > 0 {
					attrs = append(attrs, attribute.Float64Slice(openinference.EmbeddingVectorAttribute(i), vec))
				}
			}
		}
	}

	// Token counts (metadata) are included even when outputs are hidden.
	if resp.Meta != nil && resp.Meta.Tokens != nil {
		if resp.Meta.Tokens.InputTokens != nil {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, int(*resp.Meta.Tokens.InputTokens)))
		}
		var total int
		if resp.Meta.Tokens.InputTokens != nil {
			total += int(*resp.Meta.Tokens.InputTokens)
		}
		if resp.Meta.Tokens.OutputTokens != nil {
			total += int(*resp.Meta.Tokens.OutputTokens)
		}
		if total > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, total))
		}
	}

	return attrs
}
