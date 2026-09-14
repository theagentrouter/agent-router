// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package tracingapi provides types for OpenTelemetry tracing support, notably to
// reduce chance of cyclic imports. No implementations besides no-op are here.
package tracingapi

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
)

type (
	// Tracing gives access to tracer types needed for endpoints such as OpenAI
	// chat completions, image generation, embeddings, and MCP requests.
	Tracing interface {
		// ChatCompletionTracer creates spans for OpenAI chat completion requests on /chat/completions endpoint.
		ChatCompletionTracer() ChatCompletionTracer
		// ImageGenerationTracer creates spans for OpenAI image generation requests.
		ImageGenerationTracer() ImageGenerationTracer
		// CompletionTracer creates spans for OpenAI completion requests on /completions endpoint.
		CompletionTracer() CompletionTracer
		// EmbeddingsTracer creates spans for OpenAI embeddings requests on /embeddings endpoint.
		EmbeddingsTracer() EmbeddingsTracer
		// ResponsesTracer creates spans for OpenAI responses requests on /v1/responses endpoint.
		ResponsesTracer() ResponsesTracer
		// SpeechTracer creates spans for OpenAI speech synthesis requests on /v1/audio/speech endpoint.
		SpeechTracer() SpeechTracer
		// TranscriptionTracer creates spans for OpenAI audio transcription requests on /v1/audio/transcriptions endpoint.
		TranscriptionTracer() TranscriptionTracer
		// TranslationTracer creates spans for OpenAI audio translation requests on /v1/audio/translations endpoint.
		TranslationTracer() TranslationTracer
		// RerankTracer creates spans for rerank requests.
		RerankTracer() RerankTracer
		// MessageTracer creates spans for Anthropic messages requests.
		MessageTracer() MessageTracer
		// TokenizeTracer creates spans for tokenize requests.
		TokenizeTracer() TokenizeTracer
		// ResponsesInputTokensTracer creates spans for OpenAI /v1/responses/input_tokens requests.
		ResponsesInputTokensTracer() ResponsesInputTokensTracer
		// CountTokensTracer creates spans for Anthropic count tokens requests.
		CountTokensTracer() CountTokensTracer
		// MCPTracer creates spans for MCP requests.
		MCPTracer() MCPTracer
		// Shutdown shuts down the tracer, flushing any buffered spans.
		Shutdown(context.Context) error
	}
	// RequestTracer standardizes tracer implementations for non-MCP requests.
	RequestTracer[ReqT any, RespT any, RespChunkT any] interface {
		// StartSpanAndInjectHeaders starts a span and injects trace context into the header mutation.
		//
		// Parameters:
		//   - ctx: might include a parent span context.
		//   - headers: Incoming HTTP headers used to extract parent trace context.
		//   - headerMutation: The new span will have its context written to these headers unless NoopTracing is used.
		//   - req: The typed request used to detect streaming and record request attributes.
		//   - body: contains the original raw request body as a byte slice.
		//
		// Returns nil unless the span is sampled.
		StartSpanAndInjectHeaders(ctx context.Context, headers map[string]string, carrier propagation.TextMapCarrier, req *ReqT, body []byte) Span[RespT, RespChunkT]
	}
	// ChatCompletionTracer creates spans for OpenAI chat completion requests.
	ChatCompletionTracer = RequestTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]
	// CompletionTracer creates spans for OpenAI completion requests.
	CompletionTracer = RequestTracer[openai.CompletionRequest, openai.CompletionResponse, openai.CompletionResponse]
	// EmbeddingsTracer creates spans for OpenAI embeddings requests.
	EmbeddingsTracer = RequestTracer[openai.EmbeddingRequest, openai.EmbeddingResponse, struct{}]
	// ImageGenerationTracer creates spans for OpenAI image generation requests.
	ImageGenerationTracer = RequestTracer[openai.ImageGenerationRequest, openai.ImageGenerationResponse, struct{}]
	// ResponsesTracer creates spans for OpenAI responses requests.
	ResponsesTracer = RequestTracer[openai.ResponseRequest, openai.Response, openai.ResponseStreamEventUnion]
	// SpeechTracer creates spans for OpenAI speech synthesis requests.
	SpeechTracer = RequestTracer[openai.SpeechRequest, []byte, openai.SpeechStreamChunk]
	// TranscriptionTracer creates spans for OpenAI audio transcription requests.
	// The chunk type is openai.TranscriptionStreamEvent because gpt-4o-transcribe and
	// gpt-4o-mini-transcribe emit SSE events when stream=true. whisper-1 and gpt-4o-transcribe-diarize
	// never stream, but the chunk recorder is still invoked with a zero-length slice for those — same
	// as any other non-streaming endpoint.
	TranscriptionTracer = RequestTracer[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent]
	// TranslationTracer creates spans for OpenAI audio translation requests.
	// Translation has no streaming per the OpenAI spec, so the chunk type stays struct{}.
	TranslationTracer = RequestTracer[openai.TranslationRequest, openai.TranslationResponse, struct{}]
	// RerankTracer creates spans for rerank requests.
	RerankTracer = RequestTracer[cohere.RerankV2Request, cohere.RerankV2Response, struct{}]
	// MessageTracer creates spans for Anthropic messages requests.
	MessageTracer = RequestTracer[anthropicschema.MessagesRequest, anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk]
	// TokenizeTracer creates spans for tokenize requests.
	TokenizeTracer = RequestTracer[tokenize.RequestUnion, tokenize.Response, struct{}]
	// ResponsesInputTokensTracer creates spans for OpenAI /v1/responses/input_tokens requests.
	ResponsesInputTokensTracer = RequestTracer[openai.ResponseRequest, openai.ResponsesInputTokensResponse, struct{}]
	// CountTokensTracer creates spans for Anthropic count tokens requests.
	CountTokensTracer = RequestTracer[anthropicschema.CountTokensRequest, anthropicschema.CountTokensResponse, struct{}]
)

type (
	// Span standardizes span interfaces, supporting both streaming and non-streaming endpoints.
	Span[RespT any, RespChunkT any] interface {
		// RecordResponseChunk records streaming response chunks. Implementations that do not support streaming should provide a no-op implementation.
		RecordResponseChunk(resp *RespChunkT)
		// RecordResponse records the response attributes to the span.
		RecordResponse(resp *RespT)
		// EndSpanOnError finalizes and ends the span with an error status.
		EndSpanOnError(statusCode int, body []byte)
		// EndSpan finalizes and ends the span.
		EndSpan()
	}
	// ChatCompletionSpan represents an OpenAI chat completion.
	ChatCompletionSpan = Span[openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]
	// CompletionSpan represents an OpenAI completion request.
	// Note: Completion streaming chunks are full CompletionResponse objects, not deltas like chat completions.
	CompletionSpan = Span[openai.CompletionResponse, openai.CompletionResponse]
	// EmbeddingsSpan represents an OpenAI embeddings request. The chunk type is unused and therefore set to struct{}.
	EmbeddingsSpan = Span[openai.EmbeddingResponse, struct{}]
	// ImageGenerationSpan represents an OpenAI image generation.
	ImageGenerationSpan = Span[openai.ImageGenerationResponse, struct{}]
	// ResponsesSpan represents an OpenAI responses request span.
	ResponsesSpan = Span[openai.Response, openai.ResponseStreamEventUnion]
	// SpeechSpan represents an OpenAI speech synthesis request span.
	SpeechSpan = Span[[]byte, openai.SpeechStreamChunk]
	// TranscriptionSpan represents an OpenAI audio transcription request span.
	TranscriptionSpan = Span[openai.TranscriptionResponse, openai.TranscriptionStreamEvent]
	// TranslationSpan represents an OpenAI audio translation request span.
	TranslationSpan = Span[openai.TranslationResponse, struct{}]
	// RerankSpan represents a rerank request span.
	RerankSpan = Span[cohere.RerankV2Response, struct{}]
	// MessageSpan represents an Anthropic messages request span.
	MessageSpan = Span[anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk]
	// TokenizeSpan represents a tokenize request span. The chunk type is unused and therefore set to struct{}.
	TokenizeSpan = Span[tokenize.Response, struct{}]
	// ResponsesInputTokensSpan represents an OpenAI /v1/responses/input_tokens request span.
	ResponsesInputTokensSpan = Span[openai.ResponsesInputTokensResponse, struct{}]
	// CountTokensSpan represents an Anthropic count tokens request span.
	CountTokensSpan = Span[anthropicschema.CountTokensResponse, struct{}]
)

type (
	// SpanRecorder standardizes recorder implementations for non-MCP tracers.
	SpanRecorder[ReqT any, RespT any, RespChunkT any] interface {
		SpanResponseRecorder[RespT, RespChunkT]
		// StartParams returns the name and options to start the span with.
		//
		// Parameters:
		//   - req: contains the typed request.
		//   - body: contains the complete request body.
		//
		// Note: Avoid expensive data conversions since the span might not be sampled.
		StartParams(req *ReqT, body []byte) (spanName string, opts []trace.SpanStartOption)
		// RecordRequest records request attributes to the span.
		RecordRequest(span trace.Span, req *ReqT, body []byte)
	}
	// SpanResponseRecorder standardizes response recording for non-MCP tracers.
	//
	// It is separated from SpanRecorder to allow reusing response recording logic.
	SpanResponseRecorder[RespT, RespChunkT any] interface {
		// RecordResponse records response attributes to the span.
		RecordResponse(span trace.Span, resp *RespT)
		// RecordResponseOnError ends recording the span with an error status.
		RecordResponseOnError(span trace.Span, statusCode int, body []byte)
		// RecordResponseChunks records response chunk attributes to the span for streaming response.
		RecordResponseChunks(span trace.Span, chunks []*RespChunkT)
	}
	// ChatCompletionRecorder records attributes to a span according to a semantic convention.
	ChatCompletionRecorder = SpanRecorder[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]
	// CompletionRecorder records attributes to a span according to a semantic convention.
	// Note: Completion streaming chunks are full CompletionResponse objects, not deltas like chat completions.
	CompletionRecorder = SpanRecorder[openai.CompletionRequest, openai.CompletionResponse, openai.CompletionResponse]
	// ImageGenerationRecorder records attributes to a span according to a semantic convention.
	ImageGenerationRecorder = SpanRecorder[openai.ImageGenerationRequest, openai.ImageGenerationResponse, struct{}]
	// EmbeddingsRecorder records attributes to a span according to a semantic convention.
	EmbeddingsRecorder = SpanRecorder[openai.EmbeddingRequest, openai.EmbeddingResponse, struct{}]
	// ResponsesRecorder records attributes to a span according to a semantic convention.
	ResponsesRecorder = SpanRecorder[openai.ResponseRequest, openai.Response, openai.ResponseStreamEventUnion]
	// SpeechRecorder records attributes to a span according to a semantic convention.
	SpeechRecorder = SpanRecorder[openai.SpeechRequest, []byte, openai.SpeechStreamChunk]
	// TranscriptionRecorder records attributes to a span according to a semantic convention.
	TranscriptionRecorder = SpanRecorder[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent]
	// TranslationRecorder records attributes to a span according to a semantic convention.
	TranslationRecorder = SpanRecorder[openai.TranslationRequest, openai.TranslationResponse, struct{}]
	// RerankRecorder records attributes to a span according to a semantic convention.
	RerankRecorder = SpanRecorder[cohere.RerankV2Request, cohere.RerankV2Response, struct{}]
	// MessageRecorder records attributes to a span according to a semantic convention.
	MessageRecorder = SpanRecorder[anthropicschema.MessagesRequest, anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk]
	// TokenizeRecorder records attributes to a span according to a semantic convention.
	TokenizeRecorder = SpanRecorder[tokenize.RequestUnion, tokenize.Response, struct{}]
	// ResponsesInputTokensRecorder records attributes to a span according to a semantic convention.
	ResponsesInputTokensRecorder = SpanRecorder[openai.ResponseRequest, openai.ResponsesInputTokensResponse, struct{}]
	// CountTokensRecorder records attributes to a span according to a semantic convention.
	CountTokensRecorder = SpanRecorder[anthropicschema.CountTokensRequest, anthropicschema.CountTokensResponse, struct{}]
)

// Backend describes the upstream a request was routed to. It is deliberately a
// small value type rather than filterapi.Backend so this package stays free of
// heavier dependencies.
type Backend struct {
	// Schema is the backend's API schema name, e.g. "OpenAI" or "AWSBedrock".
	Schema string
	// Name is the configured backend name, used when Schema has no well-known
	// mapping.
	Name string
}

// BackendSpan is implemented by spans that can record the resolved backend.
//
// It is an optional interface rather than part of Span because the backend is
// only known after routing, long after the span starts, and because only some
// semantic conventions record it. Callers must type-assert.
type BackendSpan interface {
	// RecordBackend records the resolved upstream backend on the span.
	RecordBackend(backend Backend)
}

// BackendRecorder is implemented by span recorders whose semantic convention
// records the resolved backend. Recorders that do not simply omit it.
type BackendRecorder interface {
	// RecordBackend records backend attributes to the span.
	RecordBackend(span trace.Span, backend Backend)
}

// NoopChunkRecorder provides a no-op RecordResponseChunks implementation for recorders that don't emit streaming chunks.
type NoopChunkRecorder[ChunkT any] struct{}

// RecordResponseChunks implements SpanRecorder.RecordResponseChunks as a no-op.
func (NoopChunkRecorder[ChunkT]) RecordResponseChunks(trace.Span, []*ChunkT) {}

// NoopTracing is a Tracing that doesn't do anything.
type NoopTracing struct{}

func (t NoopTracing) MCPTracer() MCPTracer {
	return NoopMCPTracer{}
}

// ChatCompletionTracer implements Tracing.ChatCompletionTracer.
func (NoopTracing) ChatCompletionTracer() ChatCompletionTracer {
	return NoopChatCompletionTracer{}
}

// CompletionTracer implements Tracing.CompletionTracer.
func (NoopTracing) CompletionTracer() CompletionTracer {
	return NoopCompletionTracer{}
}

// EmbeddingsTracer implements Tracing.EmbeddingsTracer.
func (NoopTracing) EmbeddingsTracer() EmbeddingsTracer {
	return NoopEmbeddingsTracer{}
}

// ImageGenerationTracer implements Tracing.ImageGenerationTracer.
func (NoopTracing) ImageGenerationTracer() ImageGenerationTracer {
	return NoopImageGenerationTracer{}
}

// ResponsesTracer implements Tracing.ResponsesTracer.
func (NoopTracing) ResponsesTracer() ResponsesTracer {
	return NoopResponsesTracer{}
}

// SpeechTracer implements Tracing.SpeechTracer.
func (NoopTracing) SpeechTracer() SpeechTracer {
	return NoopSpeechTracer{}
}

// TranscriptionTracer implements Tracing.TranscriptionTracer.
func (NoopTracing) TranscriptionTracer() TranscriptionTracer {
	return NoopTranscriptionTracer{}
}

// TranslationTracer implements Tracing.TranslationTracer.
func (NoopTracing) TranslationTracer() TranslationTracer {
	return NoopTranslationTracer{}
}

// RerankTracer implements Tracing.RerankTracer.
func (NoopTracing) RerankTracer() RerankTracer {
	return NoopRerankTracer{}
}

func (NoopTracing) MessageTracer() MessageTracer {
	return NoopMessageTracer{}
}

// TokenizeTracer implements Tracing.TokenizeTracer.
func (NoopTracing) TokenizeTracer() TokenizeTracer {
	return NoopTokenizeTracer{}
}

// ResponsesInputTokensTracer implements Tracing.ResponsesInputTokensTracer.
func (NoopTracing) ResponsesInputTokensTracer() ResponsesInputTokensTracer {
	return NoopResponsesInputTokensTracer{}
}

// CountTokensTracer implements Tracing.CountTokensTracer.
func (NoopTracing) CountTokensTracer() CountTokensTracer {
	return NoopCountTokensTracer{}
}

// Shutdown implements Tracing.Shutdown.
func (NoopTracing) Shutdown(context.Context) error {
	return nil
}

type (
	// NoopTracer implements RequestTracer without producing spans.
	NoopTracer[ReqT any, RespT any, RespChunkT any] struct{}
	// NoopChatCompletionTracer implements ChatCompletionTracer.
	NoopChatCompletionTracer = NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]
	// NoopCompletionTracer implements CompletionTracer.
	NoopCompletionTracer = NoopTracer[openai.CompletionRequest, openai.CompletionResponse, openai.CompletionResponse]
	// NoopEmbeddingsTracer implements EmbeddingsTracer.
	NoopEmbeddingsTracer = NoopTracer[openai.EmbeddingRequest, openai.EmbeddingResponse, struct{}]
	// NoopImageGenerationTracer implements ImageGenerationTracer.
	NoopImageGenerationTracer = NoopTracer[openai.ImageGenerationRequest, openai.ImageGenerationResponse, struct{}]
	// NoopResponsesTracer implements ResponsesTracer.
	NoopResponsesTracer = NoopTracer[openai.ResponseRequest, openai.Response, openai.ResponseStreamEventUnion]
	// NoopSpeechTracer implements SpeechTracer.
	NoopSpeechTracer = NoopTracer[openai.SpeechRequest, []byte, openai.SpeechStreamChunk]
	// NoopTranscriptionTracer implements TranscriptionTracer.
	NoopTranscriptionTracer = NoopTracer[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent]
	// NoopTranslationTracer implements TranslationTracer.
	NoopTranslationTracer = NoopTracer[openai.TranslationRequest, openai.TranslationResponse, struct{}]
	// NoopRerankTracer implements RerankTracer.
	NoopRerankTracer = NoopTracer[cohere.RerankV2Request, cohere.RerankV2Response, struct{}]
	// NoopMessageTracer implements MessageTracer.
	NoopMessageTracer = NoopTracer[anthropicschema.MessagesRequest, anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk]
	// NoopTokenizeTracer implements TokenizeTracer.
	NoopTokenizeTracer = NoopTracer[tokenize.RequestUnion, tokenize.Response, struct{}]
	// NoopResponsesInputTokensTracer implements ResponsesInputTokensTracer.
	NoopResponsesInputTokensTracer = NoopTracer[openai.ResponseRequest, openai.ResponsesInputTokensResponse, struct{}]
	// NoopCountTokensTracer implements CountTokensTracer.
	NoopCountTokensTracer = NoopTracer[anthropicschema.CountTokensRequest, anthropicschema.CountTokensResponse, struct{}]
)

// StartSpanAndInjectHeaders implements RequestTracer.StartSpanAndInjectHeaders.
func (NoopTracer[ReqT, RespT, RespChunkT]) StartSpanAndInjectHeaders(context.Context, map[string]string, propagation.TextMapCarrier, *ReqT, []byte) Span[RespT, RespChunkT] {
	var zero Span[RespT, RespChunkT]
	return zero
}
