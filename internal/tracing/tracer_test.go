// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/typesafe"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

var (
	startOpts = []oteltrace.SpanStartOption{oteltrace.WithSpanKind(oteltrace.SpanKindServer)}

	req = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.StringOrUserRoleContentUnion{Value: "Hello!"},
				Role:    openai.ChatMessageRoleUser,
			},
		}},
	}
)

type tracerConstructor[ReqT any, RespT, RespChunkT any] func(oteltrace.Tracer, propagation.TextMapPropagator, map[string]string) tracingapi.RequestTracer[ReqT, RespT, RespChunkT]

var chatCompletionTracerCtor = func(tr oteltrace.Tracer, prop propagation.TextMapPropagator, headerAttrs map[string]string) tracingapi.ChatCompletionTracer {
	return newChatCompletionTracer(tr, prop, testChatCompletionRecorder{}, headerAttrs)
}

type requestTracerLifecycleTest[ReqT any, RespT, RespChunkT any] struct {
	constructor      tracerConstructor[ReqT, RespT, RespChunkT]
	req              *ReqT
	headers          map[string]string
	headerAttrs      map[string]string
	reqBody          []byte
	expectedSpanName string
	expectedAttrs    []attribute.KeyValue
	expectedTraceID  string
	expectedSpanType any
	recordAndEnd     func(span tracingapi.Span[RespT, RespChunkT])
	assertAttrs      func(*testing.T, []attribute.KeyValue)
}

func runRequestTracerLifecycleTest[ReqT any, RespT, RespChunkT any](t *testing.T, tc requestTracerLifecycleTest[ReqT, RespT, RespChunkT]) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
	tracer := tc.constructor(tp.Tracer("test"), autoprop.NewTextMapPropagator(), tc.headerAttrs)

	carrier := propagation.MapCarrier{}
	span := tracer.StartSpanAndInjectHeaders(t.Context(), tc.headers, carrier, tc.req, tc.reqBody)
	require.IsType(t, tc.expectedSpanType, span)

	tc.recordAndEnd(span)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	actualSpan := spans[0]
	require.Equal(t, tc.expectedSpanName, actualSpan.Name)
	if tc.assertAttrs != nil {
		tc.assertAttrs(t, actualSpan.Attributes)
	} else {
		require.Equal(t, tc.expectedAttrs, actualSpan.Attributes)
	}
	require.Empty(t, actualSpan.Events)

	traceID := actualSpan.SpanContext.TraceID().String()
	if tc.expectedTraceID != "" {
		require.Equal(t, tc.expectedTraceID, traceID)
	}
	spanID := actualSpan.SpanContext.SpanID().String()
	require.Equal(t,
		propagation.MapCarrier{
			"traceparent": fmt.Sprintf("00-%s-%s-01", traceID, spanID),
		}, carrier)
}

func testNoopTracer[ReqT any, RespT, RespChunkT any](t *testing.T, name string, ctor tracerConstructor[ReqT, RespT, RespChunkT], newReq func() *ReqT) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		noopTracer := noop.Tracer{}
		tracer := ctor(noopTracer, autoprop.NewTextMapPropagator(), nil)
		require.IsType(t, tracingapi.NoopTracer[ReqT, RespT, RespChunkT]{}, tracer)

		headers := map[string]string{}
		carrier := propagation.MapCarrier{}
		req := newReq()
		span := tracer.StartSpanAndInjectHeaders(context.Background(), headers, carrier, req, []byte("{}"))
		require.Nil(t, span)
		require.Empty(t, carrier)
	})
}

func testUnsampledTracer[ReqT any, RespT, RespChunkT any](t *testing.T, name string, ctor tracerConstructor[ReqT, RespT, RespChunkT], newReq func() *ReqT) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		tp := trace.NewTracerProvider(trace.WithSampler(trace.NeverSample()))
		tracer := ctor(tp.Tracer("test"), autoprop.NewTextMapPropagator(), nil)

		headers := map[string]string{}
		carrier := propagation.MapCarrier{}
		req := newReq()
		span := tracer.StartSpanAndInjectHeaders(context.Background(), headers, carrier, req, []byte("{}"))
		require.Nil(t, span)
		require.NotEmpty(t, carrier)
	})
}

func TestChatCompletionTracer_StartSpanAndInjectHeaders(t *testing.T) {
	respBody := &openai.ChatCompletionResponse{
		ID:     "chatcmpl-abc123",
		Object: "chat.completion",
		Model:  "gpt-4.1-nano",
		Choices: []openai.ChatCompletionResponseChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionResponseChoiceMessage{
					Role:    "assistant",
					Content: ptr.To("hello world"),
				},
				FinishReason: "stop",
			},
		},
	}
	respBodyBytes, err := json.Marshal(respBody)
	require.NoError(t, err)
	bodyLen := len(respBodyBytes)

	reqStream := *req
	reqStream.Stream = true

	tests := []struct {
		name             string
		req              *openai.ChatCompletionRequest
		existingHeaders  map[string]string
		expectedSpanName string
		expectedAttrs    []attribute.KeyValue
		expectedTraceID  string
	}{
		{
			name:             "non-streaming request",
			req:              req,
			existingHeaders:  map[string]string{},
			expectedSpanName: "non-stream len: 70",
			expectedAttrs: []attribute.KeyValue{
				attribute.String("req", "stream: false"),
				attribute.Int("reqBodyLen", 70),
				attribute.Int("statusCode", 200),
				attribute.Int("respBodyLen", bodyLen),
			},
		},
		{
			name:             "streaming request",
			req:              &reqStream,
			existingHeaders:  map[string]string{},
			expectedSpanName: "stream len: 84",
			expectedAttrs: []attribute.KeyValue{
				attribute.String("req", "stream: true"),
				attribute.Int("reqBodyLen", 84),
				attribute.Int("statusCode", 200),
				attribute.Int("respBodyLen", bodyLen),
			},
		},
		{
			name: "with existing trace context",
			req:  req,
			existingHeaders: map[string]string{
				"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			expectedSpanName: "non-stream len: 70",
			expectedAttrs: []attribute.KeyValue{
				attribute.String("req", "stream: false"),
				attribute.Int("reqBodyLen", 70),
				attribute.Int("statusCode", 200),
				attribute.Int("respBodyLen", bodyLen),
			},
			expectedTraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody, err := json.Marshal(tt.req)
			require.NoError(t, err)

			headers := make(map[string]string, len(tt.existingHeaders))
			for k, v := range tt.existingHeaders {
				headers[k] = v
			}

			runRequestTracerLifecycleTest(t, requestTracerLifecycleTest[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{
				constructor:      chatCompletionTracerCtor,
				req:              tt.req,
				headers:          headers,
				reqBody:          reqBody,
				expectedSpanName: tt.expectedSpanName,
				expectedAttrs:    tt.expectedAttrs,
				expectedTraceID:  tt.expectedTraceID,
				expectedSpanType: (*chatCompletionSpan)(nil),
				recordAndEnd: func(span tracingapi.ChatCompletionSpan) {
					span.RecordResponse(respBody)
					span.EndSpan()
				},
			})
		})
	}
}

var transcriptionTracerCtor = func(tr oteltrace.Tracer, prop propagation.TextMapPropagator, headerAttrs map[string]string) tracingapi.TranscriptionTracer {
	return newTranscriptionTracer(tr, prop, testTranscriptionRecorder{}, headerAttrs)
}

var translationTracerCtor = func(tr oteltrace.Tracer, prop propagation.TextMapPropagator, headerAttrs map[string]string) tracingapi.TranslationTracer {
	return newTranslationTracer(tr, prop, testTranslationRecorder{}, headerAttrs)
}

func TestRequestTracers_Noop(t *testing.T) {
	testNoopTracer(t, "chat completion", chatCompletionTracerCtor, func() *openai.ChatCompletionRequest {
		return &openai.ChatCompletionRequest{Model: "test"}
	})
	testNoopTracer(t, "transcription", transcriptionTracerCtor, func() *openai.TranscriptionRequest {
		return &openai.TranscriptionRequest{Model: "whisper-1"}
	})
	testNoopTracer(t, "translation", translationTracerCtor, func() *openai.TranslationRequest {
		return &openai.TranslationRequest{Model: "whisper-1"}
	})
}

func TestRequestTracers_Unsampled(t *testing.T) {
	testUnsampledTracer(t, "chat completion", chatCompletionTracerCtor, func() *openai.ChatCompletionRequest {
		return &openai.ChatCompletionRequest{Model: "test"}
	})
	testUnsampledTracer(t, "transcription", transcriptionTracerCtor, func() *openai.TranscriptionRequest {
		return &openai.TranscriptionRequest{Model: "whisper-1"}
	})
	testUnsampledTracer(t, "translation", translationTracerCtor, func() *openai.TranslationRequest {
		return &openai.TranslationRequest{Model: "whisper-1"}
	})
}

func TestRequestTracer_HeaderAttributeMapping(t *testing.T) {
	t.Run("chat completion", func(t *testing.T) {
		headers := map[string]string{
			"agent-session-id": "abc123",
			"x-tenant-id":      "user456",
			"x-other":          "ignored",
		}
		reqBody, err := json.Marshal(req)
		require.NoError(t, err)

		spanName := fmt.Sprintf("non-stream len: %d", len(reqBody))

		runRequestTracerLifecycleTest(t, requestTracerLifecycleTest[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{
			constructor:      chatCompletionTracerCtor,
			req:              req,
			headers:          headers,
			headerAttrs:      map[string]string{"agent-session-id": "session.id", "x-tenant-id": "tenant.id"},
			reqBody:          reqBody,
			expectedSpanName: spanName,
			expectedSpanType: (*chatCompletionSpan)(nil),
			recordAndEnd: func(span tracingapi.ChatCompletionSpan) {
				span.EndSpan()
			},
			assertAttrs: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Len(t, attrs, 4)
				attrMap := make(map[attribute.Key]attribute.Value, len(attrs))
				for _, attr := range attrs {
					attrMap[attr.Key] = attr.Value
				}
				require.Equal(t, "stream: false", attrMap["req"].AsString())
				require.Equal(t, len(reqBody), int(attrMap["reqBodyLen"].AsInt64()))
				require.Equal(t, "abc123", attrMap["session.id"].AsString())
				require.Equal(t, "user456", attrMap["tenant.id"].AsString())
			},
		})
	})
}

func TestNewCompletionTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newCompletionTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testCompletionRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		openai.CompletionRequest,
		openai.CompletionResponse,
		openai.CompletionResponse,
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &openai.CompletionRequest{}, []byte("{}"))
	require.IsType(t, (*completionSpan)(nil), s)
}

func TestNewEmbeddingsTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newEmbeddingsTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testEmbeddingsRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		openai.EmbeddingRequest,
		openai.EmbeddingResponse,
		struct{},
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
}

func TestNewRerankTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newRerankTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testRerankTracerRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		cohere.RerankV2Request,
		cohere.RerankV2Response,
		struct{},
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &cohere.RerankV2Request{
		TopN: ptr.To(1),
	}, []byte("{}"))
	require.IsType(t, (*rerankSpan)(nil), s)
}

func TestNewSystemOneTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newSystemOneTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testSystemOneTracerRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		typesafe.SystemOneRequest,
		typesafe.SystemOneResponse,
		struct{},
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &typesafe.SystemOneRequest{Model: "jev-latest"}, []byte("{}"))
	require.IsType(t, (*systemOneSpan)(nil), s)
}

func TestNewImageGenerationTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := newImageGenerationTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testImageGenerationRecorder{})
	impl, ok := tracer.(*requestTracerImpl[
		openai.ImageGenerationRequest,
		openai.ImageGenerationResponse,
		struct{},
	])
	require.True(t, ok)
	require.Nil(t, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &openai.ImageGenerationRequest{}, []byte("{}"))
	require.IsType(t, (*imageGenerationSpan)(nil), s)
}

func TestResponsesTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newResponsesTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testResponsesRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		openai.ResponseRequest,
		openai.Response,
		openai.ResponseStreamEventUnion,
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &openai.ResponseRequest{}, []byte("{}"))
	require.IsType(t, (*responsesSpan)(nil), s)
}

func TestNewTranscriptionTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newTranscriptionTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testTranscriptionRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		openai.TranscriptionRequest,
		openai.TranscriptionResponse,
		openai.TranscriptionStreamEvent,
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &openai.TranscriptionRequest{Model: "whisper-1"}, []byte("{}"))
	require.IsType(t, (*transcriptionSpan)(nil), s)
}

func TestNewTranslationTracer_BuildsGenericRequestTracer(t *testing.T) {
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	headerAttrs := map[string]string{"agent-session-id": "session.id"}

	tracer := newTranslationTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), testTranslationRecorder{}, headerAttrs)
	impl, ok := tracer.(*requestTracerImpl[
		openai.TranslationRequest,
		openai.TranslationResponse,
		struct{},
	])
	require.True(t, ok)
	require.Equal(t, headerAttrs, impl.headerAttributes)
	require.NotNil(t, impl.newSpan)
	s := tracer.StartSpanAndInjectHeaders(context.Background(), nil, propagation.MapCarrier{}, &openai.TranslationRequest{Model: "whisper-1"}, []byte("{}"))
	require.IsType(t, (*translationSpan)(nil), s)
}

type testChatCompletionRecorder struct{}

func (r testChatCompletionRecorder) RecordResponseChunks(span oteltrace.Span, chunks []*openai.ChatCompletionResponseChunk) {
	span.SetAttributes(attribute.Int("eventCount", len(chunks)))
}

func (r testChatCompletionRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(attribute.Int("statusCode", statusCode))
	span.SetAttributes(attribute.String("errorBody", string(body)))
}

func (testChatCompletionRecorder) StartParams(req *openai.ChatCompletionRequest, body []byte) (spanName string, opts []oteltrace.SpanStartOption) {
	if req.Stream {
		return fmt.Sprintf("stream len: %d", len(body)), startOpts
	}
	return fmt.Sprintf("non-stream len: %d", len(body)), startOpts
}

func (testChatCompletionRecorder) RecordRequest(span oteltrace.Span, req *openai.ChatCompletionRequest, body []byte) {
	span.SetAttributes(attribute.String("req", fmt.Sprintf("stream: %v", req.Stream)))
	span.SetAttributes(attribute.Int("reqBodyLen", len(body)))
}

func (testChatCompletionRecorder) RecordResponse(span oteltrace.Span, resp *openai.ChatCompletionResponse) {
	span.SetAttributes(attribute.Int("statusCode", 200))
	body, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	span.SetAttributes(attribute.Int("respBodyLen", len(body)))
}

var _ tracingapi.EmbeddingsRecorder = testEmbeddingsRecorder{}

type testEmbeddingsRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
}

func (testEmbeddingsRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(attribute.Int("statusCode", statusCode))
	span.SetAttributes(attribute.String("errorBody", string(body)))
}

func (testEmbeddingsRecorder) StartParams(_ *openai.EmbeddingRequest, _ []byte) (spanName string, opts []oteltrace.SpanStartOption) {
	return "Embeddings", startOpts
}

func (testEmbeddingsRecorder) RecordRequest(span oteltrace.Span, req *openai.EmbeddingRequest, body []byte) {
	span.SetAttributes(attribute.String("model", req.Model))
	span.SetAttributes(attribute.Int("reqBodyLen", len(body)))
}

func (testEmbeddingsRecorder) RecordResponse(span oteltrace.Span, resp *openai.EmbeddingResponse) {
	span.SetAttributes(attribute.Int("statusCode", 200))
	body, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	span.SetAttributes(attribute.Int("respBodyLen", len(body)))
}

type testCompletionRecorder struct{}

func (r testCompletionRecorder) RecordResponseChunks(span oteltrace.Span, chunks []*openai.CompletionResponse) {
	span.SetAttributes(attribute.Int("eventCount", len(chunks)))
}

func (r testCompletionRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(attribute.Int("statusCode", statusCode))
	span.SetAttributes(attribute.String("errorBody", string(body)))
}

func (testCompletionRecorder) StartParams(req *openai.CompletionRequest, body []byte) (spanName string, opts []oteltrace.SpanStartOption) {
	if req.Stream {
		return fmt.Sprintf("completion-stream len: %d", len(body)), startOpts
	}
	return fmt.Sprintf("completion-non-stream len: %d", len(body)), startOpts
}

func (testCompletionRecorder) RecordRequest(span oteltrace.Span, req *openai.CompletionRequest, body []byte) {
	span.SetAttributes(attribute.String("req", fmt.Sprintf("stream: %v", req.Stream)))
	span.SetAttributes(attribute.Int("reqBodyLen", len(body)))
}

func (testCompletionRecorder) RecordResponse(span oteltrace.Span, resp *openai.CompletionResponse) {
	span.SetAttributes(attribute.Int("statusCode", 200))
	body, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	span.SetAttributes(attribute.Int("respBodyLen", len(body)))
}

// Mock recorder for testing image generation span
type testImageGenerationRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
}

func (r testImageGenerationRecorder) StartParams(_ *openai.ImageGenerationRequest, _ []byte) (string, []oteltrace.SpanStartOption) {
	return "ImagesResponse", nil
}

func (r testImageGenerationRecorder) RecordRequest(span oteltrace.Span, req *openai.ImageGenerationRequest, _ []byte) {
	span.SetAttributes(
		attribute.String("model", req.Model),
		attribute.String("prompt", req.Prompt),
		attribute.String("size", req.Size),
	)
}

func (r testImageGenerationRecorder) RecordResponse(span oteltrace.Span, resp *openai.ImageGenerationResponse) {
	respBytes, _ := json.Marshal(resp)
	span.SetAttributes(
		attribute.Int("statusCode", 200),
		attribute.Int("respBodyLen", len(respBytes)),
	)
}

func (r testImageGenerationRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(
		attribute.Int("statusCode", statusCode),
		attribute.String("errorBody", string(body)),
	)
}

type testSystemOneTracerRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
}

func (testSystemOneTracerRecorder) StartParams(*typesafe.SystemOneRequest, []byte) (string, []oteltrace.SpanStartOption) {
	return "SystemOne", []oteltrace.SpanStartOption{oteltrace.WithSpanKind(oteltrace.SpanKindServer)}
}

func (testSystemOneTracerRecorder) RecordRequest(span oteltrace.Span, req *typesafe.SystemOneRequest, _ []byte) {
	span.SetAttributes(attribute.String("model", req.Model))
}

func (testSystemOneTracerRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, _ []byte) {
	span.SetAttributes(attribute.Int("status", statusCode))
}

func (testSystemOneTracerRecorder) RecordResponse(span oteltrace.Span, resp *typesafe.SystemOneResponse) {
	span.SetAttributes(attribute.String("response_model", resp.Model))
}

type testRerankTracerRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
}

func (testRerankTracerRecorder) StartParams(*cohere.RerankV2Request, []byte) (string, []oteltrace.SpanStartOption) {
	return "Rerank", []oteltrace.SpanStartOption{oteltrace.WithSpanKind(oteltrace.SpanKindServer)}
}

func (testRerankTracerRecorder) RecordRequest(span oteltrace.Span, req *cohere.RerankV2Request, body []byte) {
	span.SetAttributes(
		attribute.String("model", req.Model),
		attribute.String("query", req.Query),
		attribute.Int("top_n", *req.TopN),
		attribute.Int("reqBodyLen", len(body)),
	)
}

func (testRerankTracerRecorder) RecordResponse(span oteltrace.Span, resp *cohere.RerankV2Response) {
	span.SetAttributes(attribute.Int("statusCode", 200))
	b, _ := json.Marshal(resp)
	span.SetAttributes(attribute.Int("respBodyLen", len(b)))
}

func (testRerankTracerRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(attribute.Int("statusCode", statusCode))
	span.SetAttributes(attribute.String("errorBody", string(body)))
}

// Mock recorder for testing responses span
type testResponsesRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
}

func (r testResponsesRecorder) StartParams(_ *openai.ResponseRequest, _ []byte) (string, []oteltrace.SpanStartOption) {
	return "Responses", startOpts
}

func (r testResponsesRecorder) RecordRequest(span oteltrace.Span, req *openai.ResponseRequest, body []byte) {
	span.SetAttributes(
		attribute.String("model", req.Model),
		attribute.Int("reqBodyLen", len(body)),
	)
}

func (r testResponsesRecorder) RecordResponse(span oteltrace.Span, resp *openai.Response) {
	respBytes, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	span.SetAttributes(
		attribute.Int("statusCode", 200),
		attribute.Int("respBodyLen", len(respBytes)),
	)
}

func (r testResponsesRecorder) RecordResponseChunks(span oteltrace.Span, chunks []*openai.ResponseStreamEventUnion) {
	span.SetAttributes(attribute.Int("eventCount", len(chunks)))
}

func (r testResponsesRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(
		attribute.Int("statusCode", statusCode),
		attribute.String("errorBody", string(body)),
	)
}

type testTranscriptionRecorder struct {
	tracingapi.NoopChunkRecorder[openai.TranscriptionStreamEvent]
}

func (testTranscriptionRecorder) StartParams(_ *openai.TranscriptionRequest, _ []byte) (string, []oteltrace.SpanStartOption) {
	return "Transcription", startOpts
}

func (testTranscriptionRecorder) RecordRequest(span oteltrace.Span, req *openai.TranscriptionRequest, body []byte) {
	span.SetAttributes(
		attribute.String("model", req.Model),
		attribute.Int("reqBodyLen", len(body)),
	)
}

func (testTranscriptionRecorder) RecordResponse(span oteltrace.Span, resp *openai.TranscriptionResponse) {
	span.SetAttributes(attribute.Int("statusCode", 200))
	if resp != nil {
		span.SetAttributes(attribute.String("text", resp.Text))
	}
}

func (testTranscriptionRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(attribute.Int("statusCode", statusCode))
	span.SetAttributes(attribute.String("errorBody", string(body)))
}

type testTranslationRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
}

func (testTranslationRecorder) StartParams(_ *openai.TranslationRequest, _ []byte) (string, []oteltrace.SpanStartOption) {
	return "Translation", startOpts
}

func (testTranslationRecorder) RecordRequest(span oteltrace.Span, req *openai.TranslationRequest, body []byte) {
	span.SetAttributes(
		attribute.String("model", req.Model),
		attribute.Int("reqBodyLen", len(body)),
	)
}

func (testTranslationRecorder) RecordResponse(span oteltrace.Span, resp *openai.TranslationResponse) {
	span.SetAttributes(attribute.Int("statusCode", 200))
	if resp != nil {
		span.SetAttributes(attribute.String("text", resp.Text))
	}
}

func (testTranslationRecorder) RecordResponseOnError(span oteltrace.Span, statusCode int, body []byte) {
	span.SetAttributes(attribute.Int("statusCode", statusCode))
	span.SetAttributes(attribute.String("errorBody", string(body)))
}
