// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package openai provides OpenInference semantic conventions hooks for
// OpenAI instrumentation used by the ExtProc router filter.
package openai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// ImageGenerationRecorder implements recorders for OpenInference image generation spans.
type ImageGenerationRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
	traceConfig *openinference.TraceConfig
}

// NewImageGenerationRecorderFromEnv creates an tracingapi.ImageGenerationRecorder
// from environment variables using the OpenInference configuration specification.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewImageGenerationRecorderFromEnv() tracingapi.ImageGenerationRecorder {
	return NewImageGenerationRecorder(nil)
}

// NewImageGenerationRecorder creates a tracingapi.ImageGenerationRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewImageGenerationRecorder(config *openinference.TraceConfig) tracingapi.ImageGenerationRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &ImageGenerationRecorder{traceConfig: config}
}

// startOpts sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var imageGenStartOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.ImageGenerationRecorder.
func (r *ImageGenerationRecorder) StartParams(*openai.ImageGenerationRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "ImagesResponse", imageGenStartOpts
}

// RecordRequest implements the same method as defined in tracingapi.ImageGenerationRecorder.
func (r *ImageGenerationRecorder) RecordRequest(span trace.Span, req *openai.ImageGenerationRequest, body []byte) {
	span.SetAttributes(buildImageGenerationRequestAttributes(req, string(body), r.traceConfig)...)
}

// RecordResponse implements the same method as defined in tracingapi.ImageGenerationRecorder.
func (r *ImageGenerationRecorder) RecordResponse(span trace.Span, resp *openai.ImageGenerationResponse) {
	// Set output attributes.
	var attrs []attribute.KeyValue
	bodyString := openinference.RedactedValue
	if !r.traceConfig.HideOutputs {
		marshaled, err := json.Marshal(resp)
		if err == nil {
			bodyString = string(marshaled)
		}
	}
	// Match ChatCompletion recorder: include output MIME type and value
	attrs = append(attrs,
		attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
		attribute.String(openinference.OutputValue, bodyString),
	)
	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}

// RecordResponseOnError implements the same method as defined in tracingapi.ImageGenerationRecorder.
func (r *ImageGenerationRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, r.traceConfig, statusCode, string(body))
}

// buildImageGenerationRequestAttributes builds OpenInference attributes from the image generation request.
func buildImageGenerationRequestAttributes(_ *openai.ImageGenerationRequest, body string, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, body),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	if !config.HideLLMInvocationParameters {
		attrs = append(attrs, attribute.String(openinference.LLMInvocationParameters, body))
	}

	return attrs
}
