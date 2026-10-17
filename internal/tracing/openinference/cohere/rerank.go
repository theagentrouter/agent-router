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

// RerankRecorder implements recorders for Cohere Rerank spans.
type RerankRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
	traceConfig *openinference.TraceConfig
}

// NewRerankRecorderFromEnv creates an tracingapi.RerankRecorder from environment variables
// using the OpenInference configuration specification.
func NewRerankRecorderFromEnv() tracingapi.RerankRecorder {
	return NewRerankRecorder(nil)
}

// NewRerankRecorder creates a tracingapi.RerankRecorder with the given config using
// the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
func NewRerankRecorder(config *openinference.TraceConfig) tracingapi.RerankRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &RerankRecorder{traceConfig: config}
}

// startOptsRerank sets trace.SpanKindInternal as that's the span kind used in OpenInference.
var startOptsRerank = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.RerankRecorder.
func (r *RerankRecorder) StartParams(*cohereschema.RerankV2Request, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Rerank", startOptsRerank
}

// RecordRequest implements the same method as defined in tracingapi.RerankRecorder.
func (r *RerankRecorder) RecordRequest(span trace.Span, req *cohereschema.RerankV2Request, body []byte) {
	span.SetAttributes(buildRerankRequestAttributes(req, body, r.traceConfig)...)
}

// RecordResponseOnError implements the same method as defined in tracingapi.RerankRecorder.
func (r *RerankRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, r.traceConfig, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.RerankRecorder.
func (r *RerankRecorder) RecordResponse(span trace.Span, resp *cohereschema.RerankV2Response) {
	// Build response attributes (excluding output.value) similar to embeddings.
	attrs := buildRerankResponseAttributes(resp, r.traceConfig)

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

// buildRerankRequestAttributes builds OpenInference attributes from the rerank request.
func buildRerankRequestAttributes(req *cohereschema.RerankV2Request, body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemCohere),
		attribute.String(openinference.SpanKind, openinference.SpanKindReranker),
	}

	// Reranker-specific attributes from request
	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.RerankerModelName, req.Model))
	}
	if req.TopN != nil {
		attrs = append(attrs, attribute.Int(openinference.RerankerTopK, *req.TopN))
	}
	if req.Query != "" {
		attrs = append(attrs, attribute.String(openinference.RerankerQuery, req.Query))
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, string(body)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	// No reranker-specific invocation_parameters attribute is used; model/top_k captured above.

	// Add input documents when inputs are not hidden.
	if !config.HideInputs {
		for i, doc := range req.Documents {
			if doc != "" {
				attrs = append(attrs, attribute.String(openinference.RerankerInputDocumentAttribute(i, openinference.DocumentContent), doc))
			}
		}
	}

	return attrs
}

// buildRerankResponseAttributes builds OpenInference attributes from the rerank response.
func buildRerankResponseAttributes(resp *cohereschema.RerankV2Response, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	// Include output MIME type only when outputs are not hidden.
	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))

		// Record individual rerank results as output documents when outputs are not hidden.
		for i, rres := range resp.Results {
			attrs = append(attrs, attribute.Float64(openinference.RerankerOutputDocumentAttribute(i, openinference.DocumentScore), rres.RelevanceScore))
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
