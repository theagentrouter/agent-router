// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// spanFactory is a function type that creates a new SpanT given a trace.Span and a Recorder.
type spanFactory[ReqT any, RespT any, RespChunkT any] func(trace.Span, tracingapi.SpanRecorder[ReqT, RespT, RespChunkT]) tracingapi.Span[RespT, RespChunkT]

// requestTracerImpl implements RequestTracer for various request and span types.
type requestTracerImpl[ReqT any, RespT any, RespChunkT any] struct {
	tracer           trace.Tracer
	propagator       propagation.TextMapPropagator
	recorder         tracingapi.SpanRecorder[ReqT, RespT, RespChunkT]
	headerAttributes map[string]string
	newSpan          spanFactory[ReqT, RespT, RespChunkT]
}

var (
	_ tracingapi.ChatCompletionTracer       = (*chatCompletionTracer)(nil)
	_ tracingapi.EmbeddingsTracer           = (*embeddingsTracer)(nil)
	_ tracingapi.CompletionTracer           = (*completionTracer)(nil)
	_ tracingapi.ImageGenerationTracer      = (*imageGenerationTracer)(nil)
	_ tracingapi.ResponsesTracer            = (*responsesTracer)(nil)
	_ tracingapi.SpeechTracer               = (*speechTracer)(nil)
	_ tracingapi.TranscriptionTracer        = (*transcriptionTracer)(nil)
	_ tracingapi.TranslationTracer          = (*translationTracer)(nil)
	_ tracingapi.RerankTracer               = (*rerankTracer)(nil)
	_ tracingapi.ResponsesInputTokensTracer = (*responsesInputTokensTracer)(nil)
	_ tracingapi.CountTokensTracer          = (*countTokensTracer)(nil)
	_ tracingapi.TokenizeTracer             = (*tokenizeTracer)(nil)
)

type (
	chatCompletionTracer       = requestTracerImpl[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]
	embeddingsTracer           = requestTracerImpl[openai.EmbeddingRequest, openai.EmbeddingResponse, struct{}]
	completionTracer           = requestTracerImpl[openai.CompletionRequest, openai.CompletionResponse, openai.CompletionResponse]
	imageGenerationTracer      = requestTracerImpl[openai.ImageGenerationRequest, openai.ImageGenerationResponse, struct{}]
	responsesTracer            = requestTracerImpl[openai.ResponseRequest, openai.Response, openai.ResponseStreamEventUnion]
	speechTracer               = requestTracerImpl[openai.SpeechRequest, []byte, openai.SpeechStreamChunk]
	transcriptionTracer        = requestTracerImpl[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent]
	translationTracer          = requestTracerImpl[openai.TranslationRequest, openai.TranslationResponse, struct{}]
	rerankTracer               = requestTracerImpl[cohereschema.RerankV2Request, cohereschema.RerankV2Response, struct{}]
	responsesInputTokensTracer = requestTracerImpl[openai.ResponseRequest, openai.ResponsesInputTokensResponse, struct{}]
	countTokensTracer          = requestTracerImpl[anthropicschema.CountTokensRequest, anthropicschema.CountTokensResponse, struct{}]
	tokenizeTracer             = requestTracerImpl[tokenize.RequestUnion, tokenize.Response, struct{}]
)

func newRequestTracer[ReqT any, RespT any, RespChunkT any](
	tracer trace.Tracer,
	propagator propagation.TextMapPropagator,
	recorder tracingapi.SpanRecorder[ReqT, RespT, RespChunkT],
	headerAttributes map[string]string,
	newSpan spanFactory[ReqT, RespT, RespChunkT],
) tracingapi.RequestTracer[ReqT, RespT, RespChunkT] {
	if _, ok := tracer.(noop.Tracer); ok {
		return tracingapi.NoopTracer[ReqT, RespT, RespChunkT]{}
	}
	return &requestTracerImpl[ReqT, RespT, RespChunkT]{
		tracer:           tracer,
		propagator:       propagator,
		recorder:         recorder,
		headerAttributes: headerAttributes,
		newSpan:          newSpan,
	}
}

func (t *requestTracerImpl[ReqT, RespT, ChunkT]) StartSpanAndInjectHeaders(
	ctx context.Context,
	headers map[string]string,
	carrier propagation.TextMapCarrier,
	req *ReqT,
	body []byte,
) tracingapi.Span[RespT, ChunkT] {
	parentCtx := t.propagator.Extract(ctx, propagation.MapCarrier(headers))
	spanName, opts := t.recorder.StartParams(req, body)
	newCtx, span := t.tracer.Start(parentCtx, spanName, opts...)

	t.propagator.Inject(newCtx, carrier)

	var zero tracingapi.Span[RespT, ChunkT]
	if !span.IsRecording() {
		return zero
	}

	t.recorder.RecordRequest(span, req, body)

	if len(t.headerAttributes) > 0 {
		attrs := make([]attribute.KeyValue, 0, len(t.headerAttributes))
		for headerName, attrName := range t.headerAttributes {
			if headerValue, ok := headers[headerName]; ok {
				attrs = append(attrs, attribute.String(attrName, headerValue))
			}
		}
		if len(attrs) > 0 {
			span.SetAttributes(attrs...)
		}
	}

	return t.newSpan(span, t.recorder)
}

func newChatCompletionTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.ChatCompletionRecorder, headerAttributes map[string]string) tracingapi.ChatCompletionTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.ChatCompletionRecorder) tracingapi.ChatCompletionSpan {
			return &chatCompletionSpan{span: span, recorder: recorder}
		},
	)
}

func newEmbeddingsTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.EmbeddingsRecorder, headerAttributes map[string]string) tracingapi.EmbeddingsTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.EmbeddingsRecorder) tracingapi.EmbeddingsSpan {
			return &embeddingsSpan{span: span, recorder: recorder}
		},
	)
}

func newCompletionTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.CompletionRecorder, headerAttributes map[string]string) tracingapi.CompletionTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.CompletionRecorder) tracingapi.CompletionSpan {
			return &completionSpan{span: span, recorder: recorder}
		},
	)
}

func newImageGenerationTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.ImageGenerationRecorder) tracingapi.ImageGenerationTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		nil,
		func(span trace.Span, recorder tracingapi.ImageGenerationRecorder) tracingapi.ImageGenerationSpan {
			return &imageGenerationSpan{span: span, recorder: recorder}
		},
	)
}

func newResponsesTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.ResponsesRecorder, headerAttributes map[string]string) tracingapi.ResponsesTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.ResponsesRecorder) tracingapi.ResponsesSpan {
			return &responsesSpan{span: span, recorder: recorder}
		},
	)
}

func newSpeechTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.SpeechRecorder, headerAttributes map[string]string) tracingapi.SpeechTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.SpeechRecorder) tracingapi.SpeechSpan {
			return &speechSpan{span: span, recorder: recorder}
		},
	)
}

func newTranscriptionTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.TranscriptionRecorder, headerAttributes map[string]string) tracingapi.TranscriptionTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.TranscriptionRecorder) tracingapi.TranscriptionSpan {
			return &transcriptionSpan{span: span, recorder: recorder}
		},
	)
}

func newTranslationTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.TranslationRecorder, headerAttributes map[string]string) tracingapi.TranslationTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.TranslationRecorder) tracingapi.TranslationSpan {
			return &translationSpan{span: span, recorder: recorder}
		},
	)
}

func newRerankTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.RerankRecorder, headerAttributes map[string]string) tracingapi.RerankTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.RerankRecorder) tracingapi.RerankSpan {
			return &rerankSpan{span: span, recorder: recorder}
		},
	)
}

func newMessageTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.MessageRecorder, headerAttributes map[string]string) tracingapi.MessageTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.MessageRecorder) tracingapi.MessageSpan {
			return &messageSpan{span: span, recorder: recorder}
		},
	)
}

func newTokenizeTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.TokenizeRecorder, headerAttributes map[string]string) tracingapi.TokenizeTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.TokenizeRecorder) tracingapi.TokenizeSpan {
			return &tokenizeSpan{span: span, recorder: recorder}
		},
	)
}

func newResponsesInputTokensTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.ResponsesInputTokensRecorder, headerAttributes map[string]string) tracingapi.ResponsesInputTokensTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.ResponsesInputTokensRecorder) tracingapi.ResponsesInputTokensSpan {
			return &responsesInputTokensSpan{span: span, recorder: recorder}
		},
	)
}

func newCountTokensTracer(tracer trace.Tracer, propagator propagation.TextMapPropagator, recorder tracingapi.CountTokensRecorder, headerAttributes map[string]string) tracingapi.CountTokensTracer {
	return newRequestTracer(
		tracer,
		propagator,
		recorder,
		headerAttributes,
		func(span trace.Span, recorder tracingapi.CountTokensRecorder) tracingapi.CountTokensSpan {
			return &countTokensSpan{span: span, recorder: recorder}
		},
	)
}
