// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3http "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/protobuf/types/known/structpb"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/bodymutator"
	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/headermutator"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func TestNewFactory(t *testing.T) {
	cfg := &filterapi.RuntimeConfig{}
	headers := map[string]string{"foo": "bar"}

	t.Run("router", func(t *testing.T) {
		t.Parallel()

		factory := NewFactory(nil, tracingapi.NoopChatCompletionTracer{}, endpointspec.ChatCompletionsEndpointSpec{})
		proc, err := factory(cfg, headers, slog.Default(), false, false)
		require.NoError(t, err)
		require.IsType(t, &chatCompletionProcessorRouterFilter{}, proc)

		router := proc.(*chatCompletionProcessorRouterFilter)
		require.Equal(t, cfg, router.config)
		require.Equal(t, headers, router.requestHeaders)
		require.NotNil(t, router.logger)
		require.NotNil(t, router.tracer)
	})

	t.Run("upstream", func(t *testing.T) {
		t.Parallel()

		factory := NewFactory(&mockMetricsFactory{}, tracingapi.NoopChatCompletionTracer{}, endpointspec.ChatCompletionsEndpointSpec{})
		proc, err := factory(cfg, headers, slog.Default(), true, false)
		require.NoError(t, err)
		require.IsType(t, &chatCompletionProcessorUpstreamFilter{}, proc)

		upstream := proc.(*chatCompletionProcessorUpstreamFilter)
		require.Equal(t, headers, upstream.requestHeaders)
		require.IsType(t, &mockMetrics{}, upstream.metrics)
	})
}

type (
	chatCompletionProcessorRouterFilter   = routerProcessor[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk, endpointspec.ChatCompletionsEndpointSpec]
	chatCompletionProcessorUpstreamFilter = upstreamProcessor[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk, endpointspec.ChatCompletionsEndpointSpec]
	transcriptionProcessorRouterFilter    = routerProcessor[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent, endpointspec.TranscriptionEndpointSpec]
	transcriptionProcessorUpstreamFilter  = upstreamProcessor[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent, endpointspec.TranscriptionEndpointSpec]
	messagesProcessorRouterFilter         = routerProcessor[anthropicschema.MessagesRequest, anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk, endpointspec.MessagesEndpointSpec]
	messagesProcessorUpstreamFilter       = upstreamProcessor[anthropicschema.MessagesRequest, anthropicschema.MessagesResponse, anthropicschema.MessagesStreamChunk, endpointspec.MessagesEndpointSpec]
)

type mockTracer struct {
	tracingapi.NoopChatCompletionTracer
	startSpanCalled bool
	returnedSpan    tracingapi.ChatCompletionSpan
}

func (m *mockTracer) StartSpanAndInjectHeaders(_ context.Context, _ map[string]string, carrier propagation.TextMapCarrier, _ *openai.ChatCompletionRequest, _ []byte) tracingapi.ChatCompletionSpan {
	m.startSpanCalled = true
	carrier.Set("tracing-header", "1")
	if m.returnedSpan != nil {
		return m.returnedSpan
	}
	return nil
}

func Test_chatCompletionProcessorRouterFilter_ProcessRequestBody(t *testing.T) {
	t.Run("body parser error", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			tracer: tracingapi.NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{},
			config: &filterapi.RuntimeConfig{},
			logger: slog.Default(),
		}
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: []byte("nonjson")})
		require.NoError(t, err, "Should not return error when returning immediate response")
		require.NotNil(t, resp, "Response should not be nil")

		immediateResp, ok := resp.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
		require.True(t, ok, "Response should be an immediate response")
		require.Equal(t, typev3.StatusCode(400), immediateResp.ImmediateResponse.Status.Code)
		body := string(immediateResp.ImmediateResponse.Body)
		require.Contains(t, body, `"type":"error"`)
		require.Contains(t, body, `"type":"BadRequest"`)
		require.Contains(t, body, `"code":"400"`)
		require.Contains(t, body, "malformed request: failed to parse JSON for /v1/chat/completions:")
	})

	t.Run("ok", func(t *testing.T) {
		headers := map[string]string{":path": "/foo"}
		p := &chatCompletionProcessorRouterFilter{
			config:         &filterapi.RuntimeConfig{},
			requestHeaders: headers,
			logger:         slog.Default(),
			tracer:         tracingapi.NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{},
		}
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", false, nil)})
		require.NoError(t, err)
		require.NotNil(t, resp)
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
		require.True(t, ok)
		require.NotNil(t, re)
		require.NotNil(t, re.RequestBody)
		setHeaders := re.RequestBody.GetResponse().GetHeaderMutation().SetHeaders
		require.Len(t, setHeaders, 3)
		require.Equal(t, internalapi.ModelNameHeaderKeyDefault, setHeaders[0].Header.Key)
		require.Equal(t, "some-model", string(setHeaders[0].Header.RawValue))
		require.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, setHeaders[0].AppendAction)
		require.Equal(t, internalapi.OriginalPathHeader, setHeaders[1].Header.Key)
		require.Equal(t, "/foo", string(setHeaders[1].Header.RawValue))
		require.Equal(t, internalapi.EnvoyOriginalPathHeader, setHeaders[2].Header.Key)
		require.Equal(t, "/foo", string(setHeaders[2].Header.RawValue))
	})

	t.Run("original path headers overwrite pre-existing values", func(t *testing.T) {
		// A client-supplied or pre-existing original-path header must not shadow the
		// gateway's own value: extproc is the authoritative writer of these headers.
		headers := map[string]string{
			":path":                             "/foo",
			internalapi.OriginalPathHeader:      "/client-supplied",
			internalapi.EnvoyOriginalPathHeader: "/client-supplied",
		}
		p := &chatCompletionProcessorRouterFilter{
			config:         &filterapi.RuntimeConfig{},
			requestHeaders: headers,
			logger:         slog.Default(),
			tracer:         tracingapi.NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{},
		}
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", false, nil)})
		require.NoError(t, err)
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
		require.True(t, ok)
		setHeaders := re.RequestBody.GetResponse().GetHeaderMutation().SetHeaders
		require.Len(t, setHeaders, 3)
		require.Equal(t, internalapi.OriginalPathHeader, setHeaders[1].Header.Key)
		require.Equal(t, "/foo", string(setHeaders[1].Header.RawValue))
		require.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, setHeaders[1].AppendAction)
		require.Equal(t, internalapi.EnvoyOriginalPathHeader, setHeaders[2].Header.Key)
		require.Equal(t, "/foo", string(setHeaders[2].Header.RawValue))
		require.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, setHeaders[2].AppendAction)
		// The in-place request header map is updated too, not just the mutation.
		require.Equal(t, "/foo", headers[internalapi.EnvoyOriginalPathHeader])
	})

	t.Run("model header overwrites pre-existing value", func(t *testing.T) {
		// x-ai-eg-model is owned by the gateway, so a client-supplied value must be
		// overwritten rather than appended (which would produce a multi-value header).
		headers := map[string]string{
			":path":                               "/foo",
			internalapi.ModelNameHeaderKeyDefault: "client-supplied",
		}
		p := &chatCompletionProcessorRouterFilter{
			config:         &filterapi.RuntimeConfig{},
			requestHeaders: headers,
			logger:         slog.Default(),
			tracer:         tracingapi.NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{},
		}
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", false, nil)})
		require.NoError(t, err)
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
		require.True(t, ok)
		setHeaders := re.RequestBody.GetResponse().GetHeaderMutation().SetHeaders
		require.Equal(t, internalapi.ModelNameHeaderKeyDefault, setHeaders[0].Header.Key)
		require.Equal(t, "some-model", string(setHeaders[0].Header.RawValue))
		require.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, setHeaders[0].AppendAction)
		// The in-place request header map is updated too, not just the mutation.
		require.Equal(t, "some-model", headers[internalapi.ModelNameHeaderKeyDefault])
	})

	t.Run("span creation", func(t *testing.T) {
		headers := map[string]string{":path": "/v1/chat/completions"}
		span := &testotel.MockSpan{}
		mockTracerInstance := &mockTracer{returnedSpan: span}

		p := &chatCompletionProcessorRouterFilter{
			config:         &filterapi.RuntimeConfig{},
			requestHeaders: headers,
			logger:         slog.Default(),
			tracer:         mockTracerInstance,
		}

		// Test with streaming request.
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "test-model", true, nil)})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.True(t, mockTracerInstance.startSpanCalled)
		require.Equal(t, span, p.span)
		require.True(t, p.originalRequestBody.Stream)

		// Verify headers are injected.
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
		require.True(t, ok)
		headerMutation := re.RequestBody.GetResponse().GetHeaderMutation()
		require.Contains(t, headerMutation.SetHeaders, &corev3.HeaderValueOption{
			Header: &corev3.HeaderValue{
				Key:      "tracing-header",
				RawValue: []byte("1"),
			},
		})
	})

	t.Run("ok_stream_without_include_usage", func(t *testing.T) {
		for _, opt := range []*openai.StreamOptions{nil, {IncludeUsage: false}} {
			headers := map[string]string{":path": "/foo"}
			p := &chatCompletionProcessorRouterFilter{
				config: &filterapi.RuntimeConfig{
					// Ensure that the stream_options.include_usage be forced to true.
					RequestCosts: []filterapi.RuntimeRequestCost{{}},
				},
				requestHeaders: headers,
				logger:         slog.Default(),
				tracer:         tracingapi.NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{},
			}
			resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", true, opt)})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, p.originalRequestBody.StreamOptions)
			require.True(t, p.forceBodyMutation)
			require.True(t, p.originalRequestBody.StreamOptions.IncludeUsage)
			require.Equal(t, "some-model", p.originalModel)
			require.Contains(t, string(p.originalRequestBodyRaw), `"stream_options":{"include_usage":true}`)
		}
	})

	t.Run("ok_stream_without_include_usage_global_costs_only", func(t *testing.T) {
		for _, opt := range []*openai.StreamOptions{nil, {IncludeUsage: false}} {
			headers := map[string]string{":path": "/foo"}
			p := &chatCompletionProcessorRouterFilter{
				config: &filterapi.RuntimeConfig{
					// Gateway-level defaults alone must still force include_usage for streaming token accounting.
					GlobalRequestCosts: []filterapi.RuntimeGlobalRequestCost{{}},
				},
				requestHeaders: headers,
				logger:         slog.Default(),
				tracer:         tracingapi.NoopTracer[openai.ChatCompletionRequest, openai.ChatCompletionResponse, openai.ChatCompletionResponseChunk]{},
			}
			resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", true, opt)})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, p.originalRequestBody.StreamOptions)
			require.True(t, p.forceBodyMutation)
			require.True(t, p.originalRequestBody.StreamOptions.IncludeUsage)
			require.Contains(t, string(p.originalRequestBodyRaw), `"stream_options":{"include_usage":true}`)
		}
	})
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessResponseHeaders(t *testing.T) {
	t.Run("error translation", func(t *testing.T) {
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t, expHeaders: make(map[string]string)}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
			parent:     &chatCompletionProcessorRouterFilter{},
		}
		mt.retErr = errors.New("test error")
		_, err := p.ProcessResponseHeaders(t.Context(), nil)
		require.ErrorContains(t, err, "test error")
		mm.RequireRequestFailure(t)
	})
	t.Run("ok/non-streaming", func(t *testing.T) {
		inHeaders := &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}, {Key: "dog", RawValue: []byte("cat")}},
		}
		expHeaders := map[string]string{"foo": "bar", "dog": "cat"}
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t, expHeaders: expHeaders}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
			parent:     &chatCompletionProcessorRouterFilter{},
		}
		res, err := p.ProcessResponseHeaders(t.Context(), inHeaders)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response
		require.Empty(t, commonRes.HeaderMutation.SetHeaders)
		mm.RequireRequestNotCompleted(t)
		require.Nil(t, res.ModeOverride)
	})
	t.Run("ok/streaming", func(t *testing.T) {
		inHeaders := &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{{Key: ":status", Value: "200"}, {Key: "dog", RawValue: []byte("cat")}},
		}
		expHeaders := map[string]string{":status": "200", "dog": "cat"}
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t, expHeaders: expHeaders}
		p := &chatCompletionProcessorUpstreamFilter{translator: mt, metrics: mm, parent: &chatCompletionProcessorRouterFilter{stream: true}}
		res, err := p.ProcessResponseHeaders(t.Context(), inHeaders)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response
		require.Empty(t, commonRes.HeaderMutation.SetHeaders)
		require.Equal(t, []string{"content-length"}, commonRes.HeaderMutation.RemoveHeaders)
		require.Equal(t, &extprocv3http.ProcessingMode{ResponseBodyMode: extprocv3http.ProcessingMode_STREAMED}, res.ModeOverride)
	})
	t.Run("error/streaming", func(t *testing.T) {
		inHeaders := &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{{Key: ":status", Value: "500"}, {Key: "dog", RawValue: []byte("cat")}},
		}
		expHeaders := map[string]string{":status": "500", "dog": "cat"}
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t, expHeaders: expHeaders}
		p := &chatCompletionProcessorUpstreamFilter{translator: mt, metrics: mm, parent: &chatCompletionProcessorRouterFilter{stream: true}}
		res, err := p.ProcessResponseHeaders(t.Context(), inHeaders)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response
		require.Empty(t, commonRes.HeaderMutation)
		require.Nil(t, res.ModeOverride)
	})
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessResponseBody(t *testing.T) {
	t.Run("error translation", func(t *testing.T) {
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
			parent:     &chatCompletionProcessorRouterFilter{},
		}
		mt.retErr = errors.New("test error")
		_, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{})
		require.ErrorContains(t, err, "test error")
		mm.RequireRequestFailure(t)
		require.Zero(t, mm.inputTokenCount)
	})
	t.Run("ok", func(t *testing.T) {
		inBody := &extprocv3.HttpBody{Body: []byte("some-body"), EndOfStream: true}
		mm := &mockMetrics{}
		mt := &mockTranslator{
			t: t, expResponseBody: inBody,
			retHeaderMutation: []internalapi.Header{{"foo", "bar"}},
			retResponseModel:  internalapi.ResponseModel("some_model"),
		}
		mt.retUsedToken.SetOutputTokens(123)
		mt.retUsedToken.SetInputTokens(1)
		mt.retUsedToken.SetCachedInputTokens(1)
		mt.retUsedToken.SetCacheCreationInputTokens(3)

		celProgInt, err := llmcostcel.NewProgram("54321")
		require.NoError(t, err)
		celProgUint, err := llmcostcel.NewProgram("uint(9999)")
		require.NoError(t, err)
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
			parent: &chatCompletionProcessorRouterFilter{
				stream: true,
				config: &filterapi.RuntimeConfig{
					RequestCosts: []filterapi.RuntimeRequestCost{
						{LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeOutputToken, MetadataKey: "output_token_usage"}},
						{LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeInputToken, MetadataKey: "input_token_usage"}},
						{LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeCachedInputToken, MetadataKey: "cached_input_token_usage"}},
						{LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeCacheCreationInputToken, MetadataKey: "cache_creation_input_token_usage"}},
						{
							CELProg:        celProgInt,
							LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeCEL, MetadataKey: "cel_int"},
						},
						{
							CELProg:        celProgUint,
							LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeCEL, MetadataKey: "cel_uint"},
						},
					},
				},
			},
			requestHeaders:    map[string]string{internalapi.ModelNameHeaderKeyDefault: "ai_gateway_llm"},
			responseHeaders:   map[string]string{":status": "200"},
			backendName:       "some_backend",
			routeName:         "some_route",
			modelNameOverride: "ai_gateway_llm",
		}
		res, err := p.ProcessResponseBody(t.Context(), inBody)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseBody).ResponseBody.Response
		require.Nil(t, commonRes.BodyMutation)
		require.Len(t, commonRes.HeaderMutation.SetHeaders, 1)
		require.Equal(t, "foo", commonRes.HeaderMutation.SetHeaders[0].Header.Key)
		require.Equal(t, "bar", string(commonRes.HeaderMutation.SetHeaders[0].Header.RawValue))
		mm.RequireRequestSuccess(t)
		require.Equal(t, 1, mm.inputTokenCount)
		require.Equal(t, 123, mm.outputTokenCount)

		md := res.DynamicMetadata
		require.NotNil(t, md)
		require.Equal(t, float64(123), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["output_token_usage"].GetNumberValue())
		require.Equal(t, float64(1), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["input_token_usage"].GetNumberValue())
		require.Equal(t, float64(1), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["cached_input_token_usage"].GetNumberValue())
		require.Equal(t, float64(3), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["cache_creation_input_token_usage"].GetNumberValue())
		require.Equal(t, float64(54321), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["cel_int"].GetNumberValue())
		require.Equal(t, float64(9999), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["cel_uint"].GetNumberValue())
		require.Equal(t, "ai_gateway_llm", md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue().Fields["model_name_override"].GetStringValue())
		require.Equal(t, "some_backend", md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue().Fields["ai_service_backend_name"].GetStringValue())
		require.Equal(t, "some_route", md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue().Fields["route_name"].GetStringValue())
		require.Equal(t, "some_model", md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue().Fields["response_model"].GetStringValue())
	})

	// Verify we record failure for non-2xx responses and do it exactly once (defer suppressed).
	t.Run("non-2xx status failure once", func(t *testing.T) {
		inBody := &extprocv3.HttpBody{Body: []byte("error-body"), EndOfStream: true}
		expHeadMut := []internalapi.Header{{"foo", "bar"}}
		expBodyMut := []byte("error-body")
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t, expResponseBody: inBody, retHeaderMutation: expHeadMut, retBodyMutation: expBodyMut}
		p := &chatCompletionProcessorUpstreamFilter{
			translator:      mt,
			metrics:         mm,
			responseHeaders: map[string]string{":status": "500"},
			parent:          &chatCompletionProcessorRouterFilter{},
		}
		res, err := p.ProcessResponseBody(t.Context(), inBody)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseBody).ResponseBody.Response
		require.Equal(t, "error-body", string(commonRes.BodyMutation.GetBody()))
		require.Len(t, commonRes.HeaderMutation.SetHeaders, 1)
		require.Equal(t, "foo", commonRes.HeaderMutation.SetHeaders[0].Header.Key)
		require.Equal(t, []byte("bar"), commonRes.HeaderMutation.SetHeaders[0].Header.RawValue)
		require.Len(t, commonRes.HeaderMutation.SetHeaders, 1)
		require.Equal(t, "foo", commonRes.HeaderMutation.SetHeaders[0].Header.Key)
		require.Equal(t, []byte("bar"), commonRes.HeaderMutation.SetHeaders[0].Header.RawValue)
		mm.RequireRequestFailure(t)
	})

	// Verify the stale content-encoding header is removed when a compressed error body is replaced
	// with the uncompressed translated error.
	t.Run("non-2xx status removes content-encoding", func(t *testing.T) {
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		_, err := gz.Write([]byte("error-body"))
		require.NoError(t, err)
		require.NoError(t, gz.Close())

		expHeadMut := []internalapi.Header{{"foo", "bar"}}
		expBodyMut := []byte("translated-error-body")
		mm := &mockMetrics{}
		mt := &mockTranslator{
			t: t,
			// The translator must receive the decompressed body.
			expResponseBody:   &extprocv3.HttpBody{Body: []byte("error-body")},
			retHeaderMutation: expHeadMut,
			retBodyMutation:   expBodyMut,
		}
		p := &chatCompletionProcessorUpstreamFilter{
			translator:       mt,
			metrics:          mm,
			responseHeaders:  map[string]string{":status": "500", "content-encoding": "gzip"},
			responseEncoding: "gzip",
			parent:           &chatCompletionProcessorRouterFilter{},
		}
		res, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: compressed.Bytes(), EndOfStream: true})
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseBody).ResponseBody.Response
		require.Equal(t, "translated-error-body", string(commonRes.BodyMutation.GetBody()))
		require.Contains(t, commonRes.HeaderMutation.RemoveHeaders, "content-encoding")
		mm.RequireRequestFailure(t)
	})

	// Verify streaming only records completion on EndOfStream.
	t.Run("streaming completion only at end", func(t *testing.T) {
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t}
		p := &chatCompletionProcessorUpstreamFilter{
			translator:      mt,
			metrics:         mm,
			responseHeaders: map[string]string{":status": "200"},
			parent: &chatCompletionProcessorRouterFilter{
				stream: true,
				config: &filterapi.RuntimeConfig{},
			},
		}
		// First chunk (not end of stream) should not complete the request.
		chunk := &extprocv3.HttpBody{Body: []byte("chunk-1"), EndOfStream: false}
		mt.expResponseBody = chunk
		mt.retUsedToken = metrics.TokenUsage{} // no usage yet in early chunks.
		_, err := p.ProcessResponseBody(t.Context(), chunk)
		require.NoError(t, err)
		mm.RequireRequestNotCompleted(t)
		require.Zero(t, mm.inputTokenCount)
		require.Zero(t, mm.streamingOutputTokens) // first chunk has 0 output tokens

		// Final chunk should mark success and record usage once.
		final := &extprocv3.HttpBody{Body: []byte("chunk-final"), EndOfStream: true}
		mt.expResponseBody = final
		mt.retUsedToken.SetInputTokens(5)
		mt.retUsedToken.SetCachedInputTokens(3)
		mt.retUsedToken.SetCacheCreationInputTokens(21)
		mt.retUsedToken.SetOutputTokens(138)
		mt.retUsedToken.SetTotalTokens(143)
		_, err = p.ProcessResponseBody(t.Context(), final)
		require.NoError(t, err)
		mm.RequireRequestSuccess(t)
		require.Equal(t, 5, mm.inputTokenCount)
		require.Equal(t, 138, mm.outputTokenCount)
		require.Equal(t, 138, mm.streamingOutputTokens) // accumulated output tokens from stream
		require.Equal(t, 3, mm.cachedInputTokenCount)
		require.Equal(t, 21, mm.cacheCreationInputTokenCount)
	})

	// Verify dynamic metadata (used for the access log) is populated as soon as a chunk carries
	// usage, even if the stream never reaches EndOfStream (e.g. the downstream client disconnects
	// right after the terminal SSE frame, before Envoy observes end of stream).
	t.Run("streaming usage chunk without EndOfStream still sets dynamic metadata", func(t *testing.T) {
		mm := &mockMetrics{}
		mt := &mockTranslator{t: t}
		p := &chatCompletionProcessorUpstreamFilter{
			translator:      mt,
			metrics:         mm,
			responseHeaders: map[string]string{":status": "200"},
			parent: &chatCompletionProcessorRouterFilter{
				stream: true,
				config: &filterapi.RuntimeConfig{
					RequestCosts: []filterapi.RuntimeRequestCost{
						{LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "some_route", Type: filterapi.LLMRequestCostTypeOutputToken, MetadataKey: "output_token_usage"}},
					},
				},
			},
			routeName: "some_route",
		}

		// First chunk carries no usage: dynamic metadata must not be set yet.
		chunk := &extprocv3.HttpBody{Body: []byte("chunk-1"), EndOfStream: false}
		mt.expResponseBody = chunk
		mt.retUsedToken = metrics.TokenUsage{}
		res, err := p.ProcessResponseBody(t.Context(), chunk)
		require.NoError(t, err)
		require.Nil(t, res.DynamicMetadata)

		// The chunk carrying the terminal usage payload is not EndOfStream (the client disconnected
		// right after reading it), but dynamic metadata must be populated from this chunk anyway.
		usageChunk := &extprocv3.HttpBody{Body: []byte("chunk-usage"), EndOfStream: false}
		mt.expResponseBody = usageChunk
		mt.retUsedToken.SetOutputTokens(138)
		res, err = p.ProcessResponseBody(t.Context(), usageChunk)
		require.NoError(t, err)
		md := res.DynamicMetadata
		require.NotNil(t, md)
		require.Equal(t, float64(138), md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["output_token_usage"].GetNumberValue())
	})
}

func bodyFromModel(t *testing.T, model string, stream bool, streamOptions *openai.StreamOptions) []byte {
	openAIReq := &openai.ChatCompletionRequest{}
	openAIReq.Model = model
	openAIReq.Stream = stream
	openAIReq.StreamOptions = streamOptions
	bytes, err := json.Marshal(openAIReq)
	require.NoError(t, err)
	return bytes
}

func Test_chatCompletionProcessorUpstreamFilter_SetBackend(t *testing.T) {
	headers := map[string]string{":path": "/foo"}
	mm := &mockMetrics{}
	p := &chatCompletionProcessorUpstreamFilter{
		requestHeaders: headers,
		metrics:        mm,
	}
	r := &chatCompletionProcessorRouterFilter{}
	err := p.SetBackend(t.Context(), &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:              "some-backend",
			Schema:            filterapi.VersionedAPISchema{Name: "some-schema", Version: "v10.0"},
			ModelNameOverride: "ai_gateway_llm",
		},
	}, "test-route", r)
	require.ErrorContains(t, err, "unsupported API schema: backend")
	mm.RequireRequestFailure(t)
	require.Zero(t, mm.inputTokenCount)
	mm.RequireSelectedBackend(t, "some-backend")
	require.Equal(t, r, p.parent)
	// Verify upstreamFilter is NOT set when translator creation fails.
	// This prevents a nil-translator panic when the router processes the response
	// (the nil check on upstreamFilter at ProcessResponseHeaders/ProcessResponseBody
	// must fall through to passThroughProcessor).
	require.Nil(t, r.upstreamFilter, "upstreamFilter must remain nil when SetBackend fails")
}

// Test_chatCompletionProcessorUpstreamFilter_SetBackend_recordsBackend pins that
// the resolved backend reaches the span, and that recording it is optional: the
// backend is only known after routing, and only some semantic conventions record
// it, so the span is type-asserted rather than required to implement it.
func Test_chatCompletionProcessorUpstreamFilter_SetBackend_recordsBackend(t *testing.T) {
	setBackend := func(t *testing.T, span tracingapi.ChatCompletionSpan) {
		t.Helper()
		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: map[string]string{":path": "/foo"},
			metrics:        &mockMetrics{},
		}
		// The schema is unsupported so translator creation fails, but the
		// backend is recorded before that, which is what this asserts.
		err := p.SetBackend(t.Context(), &filterapi.RuntimeBackend{
			Backend: &filterapi.Backend{
				Name:   "some-backend",
				Schema: filterapi.VersionedAPISchema{Name: "some-schema", Version: "v10.0"},
			},
		}, "test-route", &chatCompletionProcessorRouterFilter{span: span})
		require.Error(t, err)
	}

	t.Run("span that records backends", func(t *testing.T) {
		span := &mockBackendChatCompletionSpan{}
		setBackend(t, span)
		require.Equal(t, []tracingapi.Backend{
			{Schema: "some-schema", Name: "some-backend"},
		}, span.backends)
	})

	t.Run("span that does not", func(t *testing.T) {
		setBackend(t, &mockChatCompletionSpan{})
	})

	t.Run("no span at all", func(t *testing.T) {
		setBackend(t, nil)
	})
}

// Test_chatCompletionProcessorUpstreamFilter_SetBackend_unsupportedSchema_noResponsePanic
// verifies that when SetBackend fails due to an unsupported schema, subsequent
// response processing does not panic. Before the fix for #1941, upstreamFilter
// was assigned before the translator was created, so the router's nil check on
// upstreamFilter would pass but the nil translator would cause a panic.
func Test_chatCompletionProcessorUpstreamFilter_SetBackend_unsupportedSchema_noResponsePanic(t *testing.T) {
	headers := map[string]string{":path": "/foo"}
	mm := &mockMetrics{}
	p := &chatCompletionProcessorUpstreamFilter{
		requestHeaders: headers,
		metrics:        mm,
	}
	r := &chatCompletionProcessorRouterFilter{}

	err := p.SetBackend(t.Context(), &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   "bad-backend",
			Schema: filterapi.VersionedAPISchema{Name: "unsupported-schema", Version: "v1"},
		},
	}, "", r)
	require.Error(t, err)
	require.Nil(t, r.upstreamFilter, "upstreamFilter must remain nil on translator creation failure")

	// Simulate response arriving after the failed SetBackend.
	// This must NOT panic; it should fall through to passThroughProcessor.
	resp, err := r.ProcessResponseHeaders(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	resp, err = r.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: []byte("error"), EndOfStream: true})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessRequestHeaders(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		stream, forcedIncludeUsage bool
	}{
		{name: "non-streaming", stream: false, forcedIncludeUsage: false},
		{name: "streaming", stream: true, forcedIncludeUsage: false},
		{name: "streaming with forced include usage", stream: true, forcedIncludeUsage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("translator error - internal", func(t *testing.T) {
				headers := map[string]string{":path": "/foo", internalapi.ModelNameHeaderKeyDefault: "some-model"}
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				var body openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &body))
				tr := &mockTranslator{t: t, retErr: errors.New("internal database error with credentials"), expRequestBody: &body}
				mm := &mockMetrics{}
				p := &chatCompletionProcessorUpstreamFilter{
					parent: &chatCompletionProcessorRouterFilter{
						config:                 &filterapi.RuntimeConfig{},
						logger:                 slog.Default(),
						originalRequestBodyRaw: someBody,
						originalRequestBody:    &body,
						originalModel:          "some-model",
						stream:                 tc.stream,
					},
					requestHeaders: headers,
					metrics:        mm,
					translator:     tr,
				}
				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.Error(t, err, "Should return an error")
				require.Contains(t, err.Error(), "failed to transform request")
				// Internal errors should use the normal error path (nil response)
				require.Nil(t, resp, "Response should be nil for internal errors")

				mm.RequireRequestFailure(t)
				require.Zero(t, mm.inputTokenCount)
				// Verify models were set even though processing failed
				require.Equal(t, "some-model", mm.originalModel)
				require.Equal(t, "some-model", mm.requestModel)
			})
			t.Run("translator error - user facing", func(t *testing.T) {
				headers := map[string]string{":path": "/foo", internalapi.ModelNameHeaderKeyDefault: "some-model"}
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				var body openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &body))
				tr := &mockTranslator{t: t, retErr: fmt.Errorf("%w: missing required field", internalapi.ErrInvalidRequestBody), expRequestBody: &body}
				mm := &mockMetrics{}
				p := &chatCompletionProcessorUpstreamFilter{
					parent: &chatCompletionProcessorRouterFilter{
						config:                 &filterapi.RuntimeConfig{},
						logger:                 slog.Default(),
						originalRequestBodyRaw: someBody,
						originalRequestBody:    &body,
						originalModel:          "some-model",
						stream:                 tc.stream,
					},
					requestHeaders: headers,
					metrics:        mm,
					translator:     tr,
					logger:         slog.Default(),
				}
				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.NoError(t, err, "Should not return error when returning immediate response")
				require.NotNil(t, resp, "Response should not be nil")

				immediateResp, ok := resp.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
				require.True(t, ok, "Response should be an immediate response")
				require.Equal(t, typev3.StatusCode(422), immediateResp.ImmediateResponse.Status.Code)
				require.JSONEq(t, `{"type":"error","error":{"type":"UnprocessableEntity","code":"422","message":"invalid request body: missing required field"}}`, string(immediateResp.ImmediateResponse.Body))

				mm.RequireRequestFailure(t)
				require.Zero(t, mm.inputTokenCount)
				// Verify models were set even though processing failed
				require.Equal(t, "some-model", mm.originalModel)
				require.Equal(t, "some-model", mm.requestModel)
			})
			t.Run("local reply is not reprocessed as an upstream response", func(t *testing.T) {
				headers := map[string]string{":path": "/foo", internalapi.ModelNameHeaderKeyDefault: "some-model"}
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				var body openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &body))
				// retErr doubles as a probe: if the response path translated this local reply,
				// ResponseError would return it and ProcessResponseBody would fail.
				tr := &mockTranslator{t: t, retErr: fmt.Errorf("%w: missing required field", internalapi.ErrInvalidRequestBody), expRequestBody: &body}
				mm := &mockMetrics{}
				span := &mockChatCompletionSpan{}
				p := &chatCompletionProcessorUpstreamFilter{
					parent: &chatCompletionProcessorRouterFilter{
						config:                 &filterapi.RuntimeConfig{},
						logger:                 slog.Default(),
						originalRequestBodyRaw: someBody,
						originalRequestBody:    &body,
						originalModel:          "some-model",
						stream:                 tc.stream,
						span:                   span,
					},
					requestHeaders: headers,
					metrics:        mm,
					translator:     tr,
					logger:         slog.Default(),
				}

				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.NoError(t, err)
				immediateResp, ok := resp.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
				require.True(t, ok)
				require.Equal(t, typev3.StatusCode(422), immediateResp.ImmediateResponse.Status.Code)
				require.True(t, p.parent.localReplyEmitted, "the gateway answered the request itself")

				// The span is ended where the local reply is produced, since the response path skips it.
				require.Equal(t, 1, span.endedOnErrorCount)
				require.Equal(t, 422, span.errorStatusCode)
				require.Equal(t, immediateResp.ImmediateResponse.Body, span.errorBody)

				// Envoy delivers the local reply back through the response path.
				headersResp, err := p.ProcessResponseHeaders(t.Context(), &corev3.HeaderMap{
					Headers: []*corev3.HeaderValue{{Key: ":status", RawValue: []byte("422")}},
				})
				require.NoError(t, err)
				require.Nil(t, headersResp.GetResponseHeaders().GetResponse(), "the headers of the local reply must be left untouched")

				bodyResp, err := p.ProcessResponseBody(t.Context(),
					&extprocv3.HttpBody{Body: immediateResp.ImmediateResponse.Body, EndOfStream: true})
				require.NoError(t, err, "the local reply must not be translated as an upstream error")
				require.Nil(t, bodyResp.GetResponseBody().GetResponse(), "the body written by the gateway must be left untouched")

				// Completion was already recorded when the local reply was produced.
				mm.RequireRequestFailure(t)
				require.Equal(t, 1, span.endedOnErrorCount, "the span must not be ended twice")
				require.Zero(t, span.endedCount)
			})
			t.Run("auth handler error", func(t *testing.T) {
				headers := map[string]string{":path": "/foo", internalapi.ModelNameHeaderKeyDefault: "some-model"}
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				var body openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &body))
				tr := &mockTranslator{t: t, expRequestBody: &body}
				mm := &mockMetrics{}
				// Create a mock auth handler that returns an error
				authHandler := &mockBackendAuthHandlerError{err: errors.New("authentication failed")}
				p := &chatCompletionProcessorUpstreamFilter{
					parent: &chatCompletionProcessorRouterFilter{
						config:                 &filterapi.RuntimeConfig{},
						logger:                 slog.Default(),
						originalRequestBodyRaw: someBody,
						originalRequestBody:    &body,
						originalModel:          "some-model",
						stream:                 tc.stream,
					},
					requestHeaders: headers,
					metrics:        mm,
					translator:     tr,
					handler:        authHandler,
				}
				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.Error(t, err, "Should return an error")
				require.Contains(t, err.Error(), "failed to do auth request: authentication failed")
				require.Nil(t, resp, "Response should be nil for auth errors")

				mm.RequireRequestFailure(t)
				require.Zero(t, mm.inputTokenCount)
				require.Equal(t, "some-model", mm.originalModel)
				require.Equal(t, "some-model", mm.requestModel)
			})
			t.Run("credential missing returns 401", func(t *testing.T) {
				headers := map[string]string{":path": "/foo", internalapi.ModelNameHeaderKeyDefault: "some-model"}
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				var body openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &body))
				tr := &mockTranslator{t: t, expRequestBody: &body}
				mm := &mockMetrics{}
				// Handler returns ErrCredentialMissing — simulates fallbackToConfigured=false with absent source.
				authHandler := &mockBackendAuthHandlerError{err: backendauth.ErrCredentialMissing}
				p := &chatCompletionProcessorUpstreamFilter{
					parent: &chatCompletionProcessorRouterFilter{
						config:                 &filterapi.RuntimeConfig{},
						logger:                 slog.Default(),
						originalRequestBodyRaw: someBody,
						originalRequestBody:    &body,
						originalModel:          "some-model",
						stream:                 tc.stream,
					},
					requestHeaders: headers,
					metrics:        mm,
					translator:     tr,
					handler:        authHandler,
				}
				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.NoError(t, err, "ErrCredentialMissing must not propagate as a Go error")
				require.NotNil(t, resp)

				immediateResp, ok := resp.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
				require.True(t, ok, "response should be an ImmediateResponse")
				require.Equal(t, typev3.StatusCode(401), immediateResp.ImmediateResponse.Status.Code)
				require.Contains(t, string(immediateResp.ImmediateResponse.Body), "missing upstream credential")
				require.True(t, p.parent.localReplyEmitted)

				// Envoy delivers the local reply back through the response path.
				headersResp, err := p.ProcessResponseHeaders(t.Context(), &corev3.HeaderMap{
					Headers: []*corev3.HeaderValue{{Key: ":status", RawValue: []byte("401")}},
				})
				require.NoError(t, err)
				require.Nil(t, headersResp.GetResponseHeaders().GetResponse())
				bodyResp, err := p.ProcessResponseBody(t.Context(),
					&extprocv3.HttpBody{Body: immediateResp.ImmediateResponse.Body, EndOfStream: true})
				require.NoError(t, err)
				require.Nil(t, bodyResp.GetResponseBody().GetResponse())

				// The completion recorded below stays at one despite that second pass.
				mm.RequireRequestFailure(t)
				require.Equal(t, "some-model", mm.originalModel)
				require.Equal(t, "some-model", mm.requestModel)
			})
			t.Run("ok", func(t *testing.T) {
				LogRequestHeaderAttributes = map[string]string{"agent-session-id": "session.id"}
				t.Cleanup(func() { LogRequestHeaderAttributes = nil })
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				headers := map[string]string{
					":path":                               "/foo",
					internalapi.ModelNameHeaderKeyDefault: "some-model",
					"agent-session-id":                    "session-123",
				}
				headerMut := []internalapi.Header{{"a", "b"}}
				bodyMut := []byte("some body")

				var expBody openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &expBody))
				if tc.stream && tc.forcedIncludeUsage {
					expBody.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
				}

				mt := &mockTranslator{
					t: t, expRequestBody: &expBody, retHeaderMutation: headerMut,
					retBodyMutation: bodyMut, expForceRequestBodyMutation: tc.forcedIncludeUsage,
				}
				mm := &mockMetrics{}
				p := &chatCompletionProcessorUpstreamFilter{
					parent: &chatCompletionProcessorRouterFilter{
						config:                 &filterapi.RuntimeConfig{},
						logger:                 slog.Default(),
						originalRequestBodyRaw: someBody,
						originalRequestBody:    &expBody,
						stream:                 tc.stream,
						originalModel:          "some-model",
						forceBodyMutation:      tc.forcedIncludeUsage,
					},
					requestHeaders: headers,
					metrics:        mm,
					translator:     mt,
					handler:        &mockBackendAuthHandler{},
				}
				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.NoError(t, err)
				require.Equal(t, mt, p.translator)
				require.NotNil(t, resp)
				commonRes := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
				require.Equal(t, string(bodyMut), string(commonRes.BodyMutation.GetBody()))
				require.Len(t, commonRes.HeaderMutation.SetHeaders, 2)
				require.Equal(t, "a", commonRes.HeaderMutation.SetHeaders[0].Header.Key)
				require.Equal(t, []byte("b"), commonRes.HeaderMutation.SetHeaders[0].Header.RawValue)
				require.Equal(t, "foo", commonRes.HeaderMutation.SetHeaders[1].Header.Key)
				require.Equal(t, "mock-auth-handler", string(commonRes.HeaderMutation.SetHeaders[1].Header.RawValue))
				// The internal AWS signing-host header is stripped before egress so a client can't spoof it.
				require.Contains(t, commonRes.HeaderMutation.RemoveHeaders, internalapi.UpstreamHostHeader)

				md := resp.DynamicMetadata
				require.NotNil(t, md)
				require.Equal(t, "session-123", md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
					GetStructValue().Fields["session.id"].GetStringValue())

				mm.RequireRequestNotCompleted(t)
				// Verify models were set
				require.Equal(t, "some-model", mm.originalModel)
				require.Equal(t, "some-model", mm.requestModel)
				// Response model not set yet - only set when we get actual response
				require.Empty(t, mm.responseModel)
			})
		})
	}
}

func Test_messagesProcessorUpstreamFilter_ProcessRequestHeaders_AWSAnthropicBetaHeader(t *testing.T) {
	body := anthropicschema.MessagesRequest{
		Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
		MaxTokens: 128,
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "hello"},
			},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	headers := map[string]string{
		":path":                               "/v1/messages",
		internalapi.ModelNameHeaderKeyDefault: body.Model,
		"anthropic-beta":                      "interleaved-thinking-2025-05-14,context-1m-2025-08-07",
	}
	mm := &mockMetrics{}
	p := &messagesProcessorUpstreamFilter{
		requestHeaders: headers,
		metrics:        mm,
	}
	r := &messagesProcessorRouterFilter{
		eh:                     endpointspec.MessagesEndpointSpec{},
		config:                 &filterapi.RuntimeConfig{},
		logger:                 slog.Default(),
		originalRequestBodyRaw: raw,
		originalRequestBody:    &body,
		originalModel:          body.Model,
	}

	err = p.SetBackend(t.Context(), &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   "aws-anthropic",
			Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSAnthropic, Version: "bedrock-2023-05-31"},
		},
	}, "test-route", r)
	require.NoError(t, err)

	resp, err := p.ProcessRequestHeaders(t.Context(), nil)
	require.NoError(t, err)
	commonRes := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response

	var translatedBody map[string]any
	err = json.Unmarshal(commonRes.BodyMutation.GetBody(), &translatedBody)
	require.NoError(t, err)
	require.Equal(t, "bedrock-2023-05-31", translatedBody["anthropic_version"])
	require.NotContains(t, translatedBody, "model")
	require.NotContains(t, translatedBody, "stream")

	betaValues, ok := translatedBody["anthropic_beta"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"}, betaValues)
}

// Test_chatCompletionProcessorUpstreamFilter_ProcessRequestHeaders_BodyReplaceContract
// locks the contract for when the upstream filter must NOT replace the request
// body: when the translator returns no body, no backend HTTPBodyMutation is
// configured, and forceBodyMutation is false, the upstream filter must emit
// CONTINUE rather than CONTINUE_AND_REPLACE. Issuing CONTINUE_AND_REPLACE with
// the captured original body would clobber any body mutation applied by an
// earlier ext_proc filter in the chain. Header mutations and auth headers
// must still apply on the CONTINUE branch.
//
// The sibling subtest pins the opposite half of the contract: when the
// translator DID emit a body, the upstream filter must continue to issue
// CONTINUE_AND_REPLACE, so a future refactor cannot quietly flip the contract
// back.
func Test_chatCompletionProcessorUpstreamFilter_ProcessRequestHeaders_BodyReplaceContract(t *testing.T) {
	t.Run("no translator body, no mutator, no force -> CONTINUE", func(t *testing.T) {
		someBody := bodyFromModel(t, "some-model", false, nil)
		headers := map[string]string{
			":path":                               "/foo",
			internalapi.ModelNameHeaderKeyDefault: "some-model",
		}
		var expBody openai.ChatCompletionRequest
		require.NoError(t, json.Unmarshal(someBody, &expBody))

		pathRewrite := []internalapi.Header{{":path", "/v1/chat/completions"}}
		mt := &mockTranslator{
			t:                           t,
			expRequestBody:              &expBody,
			retHeaderMutation:           pathRewrite,
			retBodyMutation:             nil,
			expForceRequestBodyMutation: false,
		}
		mm := &mockMetrics{}
		p := &chatCompletionProcessorUpstreamFilter{
			parent: &chatCompletionProcessorRouterFilter{
				config:                 &filterapi.RuntimeConfig{},
				logger:                 slog.Default(),
				originalRequestBodyRaw: someBody,
				originalRequestBody:    &expBody,
				originalModel:          "some-model",
				stream:                 false,
				forceBodyMutation:      false,
			},
			requestHeaders: headers,
			metrics:        mm,
			translator:     mt,
			handler:        &mockBackendAuthHandler{},
			// No-config body mutator: HasMutations() returns false. This mirrors
			// SetBackend's call to bodymutator.NewBodyMutator(nil, ...) when the
			// route has no HTTPBodyMutation.
			bodyMutator: bodymutator.NewBodyMutator(nil, someBody),
		}

		resp, err := p.ProcessRequestHeaders(t.Context(), nil)
		require.NoError(t, err)
		require.NotNil(t, resp)

		commonRes := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
		require.Equal(t, extprocv3.CommonResponse_CONTINUE, commonRes.Status,
			"must NOT issue CONTINUE_AND_REPLACE when nothing actually needs to mutate the body — that path silently replays the original body and clobbers earlier filters' mutations")
		require.Nil(t, commonRes.BodyMutation, "no body mutation should ride on a CONTINUE response")

		require.NotNil(t, commonRes.HeaderMutation)
		require.Len(t, commonRes.HeaderMutation.SetHeaders, 2,
			"header mutations from the translator (path rewrite) and the auth handler must still apply on the CONTINUE branch")
		require.Equal(t, ":path", commonRes.HeaderMutation.SetHeaders[0].Header.Key)
		require.Equal(t, []byte("/v1/chat/completions"), commonRes.HeaderMutation.SetHeaders[0].Header.RawValue)
		require.Equal(t, "foo", commonRes.HeaderMutation.SetHeaders[1].Header.Key)
		require.Equal(t, "mock-auth-handler", string(commonRes.HeaderMutation.SetHeaders[1].Header.RawValue))

		// No body change -> no content-length restamp.
		// buildRequestHeaderDynamicMetadata returns nil when LogRequestHeaderAttributes is empty.
		require.Nil(t, resp.DynamicMetadata,
			"buildContentLengthDynamicMetadataOnRequest must not be called when the body is not replaced")
	})

	t.Run("translator body present -> CONTINUE_AND_REPLACE", func(t *testing.T) {
		someBody := bodyFromModel(t, "some-model", false, nil)
		headers := map[string]string{
			":path":                               "/foo",
			internalapi.ModelNameHeaderKeyDefault: "some-model",
		}
		var expBody openai.ChatCompletionRequest
		require.NoError(t, json.Unmarshal(someBody, &expBody))

		bodyMut := []byte("translator-emitted body")
		mt := &mockTranslator{
			t:                           t,
			expRequestBody:              &expBody,
			retHeaderMutation:           []internalapi.Header{{":path", "/v1/chat/completions"}},
			retBodyMutation:             bodyMut,
			expForceRequestBodyMutation: false,
		}
		mm := &mockMetrics{}
		p := &chatCompletionProcessorUpstreamFilter{
			parent: &chatCompletionProcessorRouterFilter{
				config:                 &filterapi.RuntimeConfig{},
				logger:                 slog.Default(),
				originalRequestBodyRaw: someBody,
				originalRequestBody:    &expBody,
				originalModel:          "some-model",
				stream:                 false,
				forceBodyMutation:      false,
			},
			requestHeaders: headers,
			metrics:        mm,
			translator:     mt,
			handler:        &mockBackendAuthHandler{},
			bodyMutator:    bodymutator.NewBodyMutator(nil, someBody),
		}

		resp, err := p.ProcessRequestHeaders(t.Context(), nil)
		require.NoError(t, err)
		require.NotNil(t, resp)

		commonRes := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
		require.Equal(t, extprocv3.CommonResponse_CONTINUE_AND_REPLACE, commonRes.Status,
			"existing CONTINUE_AND_REPLACE behavior must be preserved when the translator produced a body")
		require.Equal(t, bodyMut, commonRes.BodyMutation.GetBody())
	})
}

func Test_chatCompletionProcessorUpstreamFilter_MergeWithTokenLatencyMetadata(t *testing.T) {
	t.Run("empty metadata", func(t *testing.T) {
		mm := &mockMetrics{}
		mt := &mockTranslator{}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
			parent: &chatCompletionProcessorRouterFilter{
				logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				stream: true,
				config: &filterapi.RuntimeConfig{},
			},
		}
		metadata := &structpb.Struct{Fields: map[string]*structpb.Value{}}
		p.mergeWithTokenLatencyMetadata(metadata)

		val, ok := metadata.Fields[internalapi.AIGatewayFilterMetadataNamespace]
		require.True(t, ok)

		inner := val.GetStructValue()
		require.NotNil(t, inner)
		require.Equal(t, 1000.0, inner.Fields["token_latency_ttft"].GetNumberValue())
		require.Equal(t, 500.0, inner.Fields["token_latency_itl"].GetNumberValue())
	})
	t.Run("existing metadata", func(t *testing.T) {
		mm := &mockMetrics{}
		mt := &mockTranslator{}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
			parent: &chatCompletionProcessorRouterFilter{
				logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				stream: true,
				config: &filterapi.RuntimeConfig{},
			},
		}
		existingInner := &structpb.Struct{Fields: map[string]*structpb.Value{
			"tokenCost":        {Kind: &structpb.Value_NumberValue{NumberValue: float64(200)}},
			"inputTokenUsage":  {Kind: &structpb.Value_NumberValue{NumberValue: float64(300)}},
			"outputTokenUsage": {Kind: &structpb.Value_NumberValue{NumberValue: float64(400)}},
		}}
		metadata := &structpb.Struct{Fields: map[string]*structpb.Value{
			internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(existingInner),
		}}
		p.mergeWithTokenLatencyMetadata(metadata)

		val, ok := metadata.Fields[internalapi.AIGatewayFilterMetadataNamespace]
		require.True(t, ok)
		inner := val.GetStructValue()
		require.NotNil(t, inner)
		require.Equal(t, 1000.0, inner.Fields["token_latency_ttft"].GetNumberValue())
		require.Equal(t, 500.0, inner.Fields["token_latency_itl"].GetNumberValue())
		require.Equal(t, 200.0, inner.Fields["tokenCost"].GetNumberValue())
		require.Equal(t, 300.0, inner.Fields["inputTokenUsage"].GetNumberValue())
		require.Equal(t, 400.0, inner.Fields["outputTokenUsage"].GetNumberValue())
	})
}

func TestChatCompletionsProcessorRouterFilter_ProcessResponseHeaders_ProcessResponseBody(t *testing.T) {
	t.Run("no ok path with passthrough", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			span:                nil,
			originalRequestBody: &openai.ChatCompletionRequest{Stream: true},
		}
		_, err := p.ProcessResponseHeaders(t.Context(), nil)
		require.NoError(t, err)
		_, err = p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{EndOfStream: true})
		require.NoError(t, err)
	})
	t.Run("ok path with upstream filter", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			span:                nil,
			originalRequestBody: &openai.ChatCompletionRequest{Stream: true},
			upstreamFilter: &chatCompletionProcessorUpstreamFilter{
				translator: &mockTranslator{t: t, expHeaders: map[string]string{}},
				metrics:    &mockMetrics{},
				parent: &chatCompletionProcessorRouterFilter{
					logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
					config: &filterapi.RuntimeConfig{},
				},
			},
		}
		resp, err := p.ProcessResponseHeaders(t.Context(), &corev3.HeaderMap{Headers: []*corev3.HeaderValue{}})
		require.NoError(t, err)
		require.NotNil(t, resp)

		resp, err = p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: []byte("some body")})
		require.NoError(t, err)
		require.NotNil(t, resp)
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_ResponseBody)
		require.True(t, ok)
		require.NotNil(t, re)
		require.NotNil(t, re.ResponseBody)
		require.NotNil(t, re.ResponseBody.Response)
		require.IsType(t, &extprocv3.BodyMutation{}, re.ResponseBody.Response.BodyMutation)
		require.IsType(t, &extprocv3.HeaderMutation{}, re.ResponseBody.Response.HeaderMutation)
	})
}

func TestChatCompletionProcessorRouterFilter_ProcessResponseBody_SpanHandling(t *testing.T) {
	t.Run("passthrough without span", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			span:                nil,
			originalRequestBody: &openai.ChatCompletionRequest{Stream: false},
		}
		_, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{EndOfStream: true, Body: []byte("response")})
		require.NoError(t, err)
		// Should not panic when span is nil.
	})

	t.Run("upstream filter span", func(t *testing.T) {
		span := &testotel.MockSpan{}
		mt := &mockTranslator{t: t}
		p := &chatCompletionProcessorRouterFilter{
			originalRequestBody: &openai.ChatCompletionRequest{Stream: true},
			upstreamFilter: &chatCompletionProcessorUpstreamFilter{
				responseHeaders: map[string]string{":status": "200"},
				translator:      mt,
				metrics:         &mockMetrics{},
				parent: &chatCompletionProcessorRouterFilter{
					logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
					config: &filterapi.RuntimeConfig{},
					span:   span,
				},
			},
		}

		finalBody := &extprocv3.HttpBody{EndOfStream: true, Body: []byte("final")}
		mt.expResponseBody = finalBody
		_, err := p.ProcessResponseBody(t.Context(), finalBody)
		require.NoError(t, err)
		require.True(t, span.EndSpanCalled)
	})

	t.Run("upstream filter error with span", func(t *testing.T) {
		span := &testotel.MockSpan{}
		p := &chatCompletionProcessorRouterFilter{
			originalRequestBody: &openai.ChatCompletionRequest{Stream: false},
			upstreamFilter: &chatCompletionProcessorUpstreamFilter{
				responseHeaders: map[string]string{":status": "500"},
				translator:      &mockTranslator{t: t},
				metrics:         &mockMetrics{},
				parent: &chatCompletionProcessorRouterFilter{
					logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
					config: &filterapi.RuntimeConfig{},
					span:   span,
				},
			},
		}

		errorBody := "error response"
		_, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{EndOfStream: true, Body: []byte(errorBody)})
		require.NoError(t, err)
		require.Equal(t, 500, span.ErrorStatus)
		require.Equal(t, errorBody, span.ErrBody)
	})
}

func Test_chatCompletionProcessorUpstreamFilter_SensitiveHeaders_RemoveAndRestore(t *testing.T) {
	headerMutation := filterapi.HTTPHeaderMutation{
		Remove: []string{"authorization", "x-api-key"},
		Set:    []filterapi.HTTPHeader{{Name: "x-new-header", Value: "new-value"}},
	}
	originalHeaders := map[string]string{
		"authorization": "secret",
		"x-api-key":     "key123",
		"other":         "value",
	}
	body := openai.ChatCompletionRequest{Model: "test-model"}
	raw := []byte(`{"model":"test-model"}`)

	t.Run("remove headers", func(t *testing.T) {
		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: map[string]string{"authorization": "secret", "x-api-key": "key123", "other": "value"},
			headerMutator:  headermutator.NewHeaderMutator(&headerMutation, originalHeaders),
			metrics:        &mockMetrics{},
			translator:     &mockTranslator{t: t, expForceRequestBodyMutation: true, expRequestBody: &body},
			parent: &chatCompletionProcessorRouterFilter{
				upstreamFilterCount:    2, // simulate retry scenario
				originalRequestBody:    &body,
				originalRequestBodyRaw: raw,
				logger:                 slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				config:                 &filterapi.RuntimeConfig{},
			},
		}

		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)

		headerMutation := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response.HeaderMutation
		require.NotNil(t, headerMutation)
		require.ElementsMatch(t, []string{"authorization", "x-api-key", internalapi.UpstreamHostHeader}, headerMutation.RemoveHeaders)
		// Sensitive headers remain locally for metrics, but will be stripped upstream by Envoy.
		require.Equal(t, "secret", p.requestHeaders["authorization"])
		require.Equal(t, "key123", p.requestHeaders["x-api-key"])
		require.Equal(t, "value", p.requestHeaders["other"])
	})

	t.Run("set headers", func(t *testing.T) {
		// Simulate that sensitive headers were removed and now need to be restored.
		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: map[string]string{"other": "value"},
			headerMutator:  headermutator.NewHeaderMutator(&filterapi.HTTPHeaderMutation{Set: headerMutation.Set}, originalHeaders),
			metrics:        &mockMetrics{},
			translator:     &mockTranslator{t: t, expForceRequestBodyMutation: true, expRequestBody: &body},
			parent: &chatCompletionProcessorRouterFilter{
				upstreamFilterCount:    2, // simulate retry scenario
				originalRequestBody:    &body,
				originalRequestBodyRaw: raw,
				logger:                 slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				config:                 &filterapi.RuntimeConfig{},
			},
		}

		// Call the actual method to trigger restoration logic.
		_, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)

		// Now check that sensitive headers are restored.
		require.Equal(t, "secret", p.requestHeaders["authorization"])
		require.Equal(t, "key123", p.requestHeaders["x-api-key"])
		require.Equal(t, "value", p.requestHeaders["other"])

		// Now check the set headers in the mutation.
		require.Equal(t, "new-value", p.requestHeaders["x-new-header"])
	})

	t.Run("restore headers", func(t *testing.T) {
		// Simulate that sensitive headers were removed and now need to be restored.
		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: map[string]string{"other": "value"},
			headerMutator:  headermutator.NewHeaderMutator(nil, originalHeaders),
			metrics:        &mockMetrics{},
			translator:     &mockTranslator{t: t, expForceRequestBodyMutation: true, expRequestBody: &body},
			parent: &chatCompletionProcessorRouterFilter{
				upstreamFilterCount:    2, // simulate retry scenario
				originalRequestBody:    &body,
				originalRequestBodyRaw: raw,
				logger:                 slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				config:                 &filterapi.RuntimeConfig{},
			},
		}

		// Call the actual method to trigger restoration logic.
		_, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)

		// Now check that sensitive headers are restored.
		require.Equal(t, "secret", p.requestHeaders["authorization"])
		require.Equal(t, "key123", p.requestHeaders["x-api-key"])
		require.Equal(t, "value", p.requestHeaders["other"])
	})
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessRequestHeaders_SpoofedUpstreamHostStrippedOnRetry(t *testing.T) {
	body := openai.ChatCompletionRequest{Model: "test-model"}
	raw := []byte(`{"model":"test-model"}`)

	p := &chatCompletionProcessorUpstreamFilter{
		// A downstream client spoofed the internal upstream-host header; it must never reach the
		// upstream provider, on a retry same as on the initial request.
		requestHeaders: map[string]string{internalapi.UpstreamHostHeader: "attacker.example.com"},
		metrics:        &mockMetrics{},
		translator:     &mockTranslator{t: t, expForceRequestBodyMutation: true, expRequestBody: &body},
		handler:        &mockBackendAuthHandler{},
		parent: &chatCompletionProcessorRouterFilter{
			upstreamFilterCount:    2, // simulate retry scenario
			originalRequestBody:    &body,
			originalRequestBodyRaw: raw,
			logger:                 slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
			config:                 &filterapi.RuntimeConfig{},
		},
	}
	require.True(t, p.onRetry())

	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)

	headerMutation := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response.HeaderMutation
	require.Contains(t, headerMutation.RemoveHeaders, internalapi.UpstreamHostHeader)
	for _, h := range headerMutation.SetHeaders {
		require.NotEqual(t, internalapi.UpstreamHostHeader, h.Header.Key, "spoofed upstream-host header must not be re-set")
	}
}

func Test_ProcessRequestHeaders_SetsRequestModel(t *testing.T) {
	headers := map[string]string{":path": "/v1/chat/completions", internalapi.ModelNameHeaderKeyDefault: "header-model"}
	body := openai.ChatCompletionRequest{Model: "body-model"}
	raw, _ := json.Marshal(body)
	mm := &mockMetrics{}
	p := &chatCompletionProcessorUpstreamFilter{
		requestHeaders: headers,
		metrics:        mm,
		translator:     &mockTranslator{t: t, expRequestBody: &body},
		parent: &chatCompletionProcessorRouterFilter{
			originalRequestBody:    &body,
			originalRequestBodyRaw: raw,
			originalModel:          "body-model",
			logger:                 slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
			config:                 &filterapi.RuntimeConfig{},
		},
	}
	_, _ = p.ProcessRequestHeaders(t.Context(), nil)
	// Should use the override model from the header, as that's what is sent upstream.
	require.Equal(t, "body-model", mm.originalModel)
	require.Equal(t, "header-model", mm.requestModel)
	// Response model is not set until we get actual response
	require.Empty(t, mm.responseModel)
}

// Test_ProcessResponseBody_UsesActualResponseModel verifies that
// the actual response model from the API response is used for metrics, not the request model.
// This is important because OpenAI may return a more specific model version than what was
// requested (e.g., "gpt-5-nano-2025-08-07" instead of "gpt-5-nano"), as described in the
// model virtualization documentation.
func Test_ProcessResponseBody_UsesActualResponseModel(t *testing.T) {
	headers := map[string]string{":path": "/v1/chat/completions"}
	body := openai.ChatCompletionRequest{Model: "gpt-5-nano"}
	raw, _ := json.Marshal(body)
	mm := &mockMetrics{}

	// Create a mock translator that returns token usage with response model
	// Simulating OpenAI's automatic routing where gpt-5-nano routes to gpt-5-nano-2025-08-07
	mt := &mockTranslator{
		t:                t,
		expRequestBody:   &body,
		expHeaders:       map[string]string{":status": "200"},
		retResponseModel: "gpt-5-nano-2025-08-07",
	}
	mt.retUsedToken.SetInputTokens(10)
	mt.retUsedToken.SetOutputTokens(20)

	p := &chatCompletionProcessorUpstreamFilter{
		requestHeaders: headers,
		metrics:        mm,
		translator:     mt,
		parent: &chatCompletionProcessorRouterFilter{
			originalRequestBody:    &body,
			originalRequestBodyRaw: raw,
			logger:                 slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
			config:                 &filterapi.RuntimeConfig{},
			originalModel:          "gpt-5-nano",
		},
	}

	// First process request headers
	_, err := p.ProcessRequestHeaders(t.Context(), nil)
	require.NoError(t, err)

	// Process response headers (required before body)
	responseHeaders := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: ":status", Value: "200"},
		},
	}
	_, err = p.ProcessResponseHeaders(t.Context(), responseHeaders)
	require.NoError(t, err)

	// Now process response body (should override with actual response model)
	// Simple response JSON that the translator will parse
	responseBytes := []byte(`{"model":"gpt-5-nano-2025-08-07","choices":[{"message":{"content":"test"}}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
	_, err = p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{
		Body:        responseBytes,
		EndOfStream: true,
	})
	require.NoError(t, err)

	// Verify that response model was set from the actual API response
	// Request was for gpt-5-nano but OpenAI returned the versioned gpt-5-nano-2025-08-07
	// In this test, original and request are the same (no override)
	mm.RequireSelectedModel(t, "gpt-5-nano", "gpt-5-nano", "gpt-5-nano-2025-08-07")
	require.Equal(t, 10, mm.inputTokenCount)
	require.Equal(t, 20, mm.outputTokenCount)
	mm.RequireRequestSuccess(t)
}

func TestChatCompletionProcessorUpstreamFilter_ProcessRequestHeaders_WithBodyMutations(t *testing.T) {
	t.Run("body mutations applied correctly", func(t *testing.T) {
		headers := map[string]string{
			":path":         "/v1/chat/completions",
			"x-ai-eg-model": "gpt-4",
		}

		requestBody := &openai.ChatCompletionRequest{
			Model: "gpt-4",
		}
		requestBodyRaw := []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}], "service_tier": "default", "max_tokens": 1000}`)

		bodyMutations := &filterapi.HTTPBodyMutation{
			Remove: []string{"internal_flag"},
			Set: []filterapi.HTTPBodyField{
				{Path: "service_tier", Value: "\"scale\""},
				{Path: "max_tokens", Value: "2000"},
				{Path: "temperature", Value: "0.8"},
			},
		}

		translator := mockTranslator{
			t:                           t,
			expRequestBody:              requestBody,
			expForceRequestBodyMutation: false,
			retBodyMutation:             requestBodyRaw,
			retErr:                      nil,
		}

		chatMetrics := &mockMetrics{}
		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: headers,
			metrics:        chatMetrics,
			handler:        &mockBackendAuthHandler{},
		}

		backend := &filterapi.Backend{
			Name:         "test-backend",
			Schema:       filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
			BodyMutation: bodyMutations,
		}

		rp := &chatCompletionProcessorRouterFilter{
			originalRequestBody:    requestBody,
			originalRequestBodyRaw: requestBodyRaw,
			requestHeaders:         headers,
		}

		err := p.SetBackend(context.Background(), &filterapi.RuntimeBackend{
			Backend: backend,
			Handler: &mockBackendAuthHandler{},
		}, "test-route", rp)
		require.NoError(t, err)

		p.translator = &translator

		require.NotNil(t, p.bodyMutator)

		ctx := context.Background()
		response, err := p.ProcessRequestHeaders(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, response)

		testBodyMutation := []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}], "service_tier": "default", "internal_flag": true, "max_tokens": 1000}`)
		mutatedBody, err := p.bodyMutator.Mutate(testBodyMutation)
		require.NoError(t, err)

		var result map[string]interface{}
		err = json.Unmarshal(mutatedBody, &result)
		require.NoError(t, err)

		require.Equal(t, "scale", result["service_tier"])
		require.Equal(t, float64(2000), result["max_tokens"])
		require.Equal(t, 0.8, result["temperature"])
		require.NotContains(t, result, "internal_flag")
		require.Equal(t, "gpt-4", result["model"])
	})

	t.Run("body mutator with retry", func(t *testing.T) {
		headers := map[string]string{":path": "/v1/chat/completions"}
		chatMetrics := &mockMetrics{}

		originalRequestBodyRaw := []byte(`{"model": "gpt-4", "service_tier": "default", "messages": [{"role": "user", "content": "Hello"}]}`)
		requestBody := &openai.ChatCompletionRequest{
			Model: "gpt-4",
		}

		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: headers,
			metrics:        chatMetrics,
		}

		bodyMutations := &filterapi.HTTPBodyMutation{
			Set: []filterapi.HTTPBodyField{
				{Path: "service_tier", Value: "\"reserved\""},
				{Path: "temperature", Value: "0.7"},
			},
		}

		backend := &filterapi.Backend{
			Name:         "test-backend",
			Schema:       filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
			BodyMutation: bodyMutations,
		}

		rp := &chatCompletionProcessorRouterFilter{
			originalRequestBody:    requestBody,
			originalRequestBodyRaw: originalRequestBodyRaw,
			requestHeaders:         headers,
			upstreamFilterCount:    2,
		}

		err := p.SetBackend(context.Background(), &filterapi.RuntimeBackend{
			Backend: backend,
			Handler: &mockBackendAuthHandler{},
		}, "test-route", rp)
		require.NoError(t, err)

		require.NotNil(t, p.bodyMutator)
		require.True(t, p.onRetry())

		// Modified body from previous backend attempt - mutations applied to this WITHOUT restoration
		modifiedBody := []byte(`{"model": "gpt-4", "service_tier": "modified", "extra": "field", "messages": [{"role": "user", "content": "Modified"}]}`)
		mutatedBody, err := p.bodyMutator.Mutate(modifiedBody)
		require.NoError(t, err)

		var result map[string]interface{}
		err = json.Unmarshal(mutatedBody, &result)
		require.NoError(t, err)

		// Verify mutations were applied to the modified body (not restored to original)
		require.Equal(t, "reserved", result["service_tier"], "Mutation should set service_tier to reserved")
		require.Equal(t, 0.7, result["temperature"], "Mutation should set temperature to 0.7")
		require.Equal(t, "gpt-4", result["model"], "Model should be preserved from modified body")
		require.Equal(t, "field", result["extra"], "Extra field from modified body should be preserved")

		messages, ok := result["messages"].([]interface{})
		require.True(t, ok)
		require.Len(t, messages, 1)
		firstMessage, ok := messages[0].(map[string]interface{})
		require.True(t, ok)
		require.Equal(t, "Modified", firstMessage["content"], "Message content from modified body should be preserved")
	})

	t.Run("initial streaming request with forceBodyMutation and route body mutations", func(t *testing.T) {
		// This test verifies the bug fix: when forceBodyMutation=true (due to stream_options)
		// but onRetry=false (initial request), route body mutations should be applied to the
		// translated body, NOT restore the original OpenAI body.
		//
		// Bug scenario: Initial streaming Bedrock request with cost config enabled
		// - forceBodyMutation=true (stream_options.include_usage injected)
		// - onRetry=false (initial request, not retry)
		// - Route has body mutations configured
		// Expected: Body mutations applied to Bedrock-translated body
		// Bug behavior: Body mutator incorrectly restored original OpenAI body, erasing translation

		headers := map[string]string{":path": "/v1/chat/completions"}
		chatMetrics := &mockMetrics{}

		// Original OpenAI request body (before translation)
		originalRequestBodyRaw := []byte(`{"model":"bedrock.us.claude-sonnet-4.5","messages":[{"role":"user","content":"Hello"}],"stream":true,"stream_options":{"include_usage":true}}`)
		requestBody := &openai.ChatCompletionRequest{
			Model:  "bedrock.us.claude-sonnet-4.5",
			Stream: true,
			StreamOptions: &openai.StreamOptions{
				IncludeUsage: true,
			},
		}

		// Route-level body mutations to apply
		bodyMutations := &filterapi.HTTPBodyMutation{
			Set: []filterapi.HTTPBodyField{
				{Path: "serviceTier", Value: `{"type": "default"}`},
			},
		}

		// Simulated Bedrock-translated body (different format than OpenAI)
		bedrockTranslatedBody := []byte(`{"messages":[{"role":"user","content":[{"text":"Hello"}]}],"inferenceConfig":{"maxTokens":1000}}`)

		// Mock translator that returns Bedrock format
		translator := mockTranslator{
			t:                           t,
			expRequestBody:              requestBody,
			expForceRequestBodyMutation: true, // Should be true due to stream_options
			retBodyMutation:             bedrockTranslatedBody,
		}

		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: headers,
			metrics:        chatMetrics,
			logger:         slog.Default(),
			translator:     &translator,
			handler:        &mockBackendAuthHandler{},
		}

		backend := &filterapi.Backend{
			Name:         "test-bedrock-backend",
			Schema:       filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock},
			BodyMutation: bodyMutations,
		}

		rp := &chatCompletionProcessorRouterFilter{
			originalRequestBody:    requestBody,
			originalRequestBodyRaw: originalRequestBodyRaw,
			requestHeaders:         headers,
			upstreamFilterCount:    0,    // Initial request (SetBackend will increment to 1)
			forceBodyMutation:      true, // Set by stream_options injection
			config:                 &filterapi.RuntimeConfig{},
			logger:                 slog.Default(),
		}

		err := p.SetBackend(context.Background(), &filterapi.RuntimeBackend{
			Backend: backend,
			Handler: &mockBackendAuthHandler{},
		}, "test-route", rp)
		require.NoError(t, err)

		// Restore translator after SetBackend
		p.translator = &translator

		require.NotNil(t, p.bodyMutator)
		require.False(t, p.onRetry()) // Initial request, NOT a retry

		ctx := context.Background()
		response, err := p.ProcessRequestHeaders(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, response)

		// Verify the body mutation was applied to the Bedrock-translated body
		commonRes := response.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
		mutatedBody := commonRes.BodyMutation.GetBody()
		require.NotNil(t, mutatedBody)

		// Parse the mutated body
		var result map[string]interface{}
		err = json.Unmarshal(mutatedBody, &result)
		require.NoError(t, err)

		// Verify route mutations were applied
		serviceTier, ok := result["serviceTier"].(map[string]interface{})
		require.True(t, ok, "serviceTier should be an object")
		require.Equal(t, "default", serviceTier["type"], "Route body mutation should set serviceTier.type")

		// Verify Bedrock format is preserved (has messages, inferenceConfig)
		require.Contains(t, result, "messages", "Bedrock translation should be preserved")
		require.Contains(t, result, "inferenceConfig", "Bedrock translation should be preserved")

		inferenceConfig, ok := result["inferenceConfig"].(map[string]interface{})
		require.True(t, ok, "inferenceConfig should be an object")
		require.Equal(t, float64(1000), inferenceConfig["maxTokens"], "Original maxTokens from Bedrock translation should be preserved")

		// Verify OpenAI-specific fields are NOT present (confirming translation wasn't erased)
		require.NotContains(t, result, "stream", "OpenAI format should not be present - translation was erased if this fails")
		require.NotContains(t, result, "stream_options", "OpenAI format should not be present - translation was erased if this fails")
	})

	t.Run("body mutation without body restoration", func(t *testing.T) {
		// Tests both first attempt and retry: body mutations are applied to translated body without restoration
		// First attempt: OpenAI format -> Bedrock format with body mutations applied
		// Retry: Mutations applied to modified Bedrock body (not restored to original OpenAI)
		headers := map[string]string{":path": "/v1/chat/completions"}
		chatMetrics := &mockMetrics{}

		originalRequestBodyRaw := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"Hello"}],"stream":true}`)
		requestBody := &openai.ChatCompletionRequest{
			Model:  "claude-sonnet-4.5",
			Stream: true,
		}

		bodyMutations := &filterapi.HTTPBodyMutation{
			Set: []filterapi.HTTPBodyField{
				{Path: "serviceTier", Value: `{"type": "default"}`},
			},
		}

		bedrockTranslatedBody := []byte(`{"messages":[{"role":"user","content":[{"text":"Hello"}]}],"inferenceConfig":{"temperature":0.7,"maxTokens":1024}}`)

		translator := mockTranslator{
			t:                           t,
			expRequestBody:              requestBody,
			expForceRequestBodyMutation: false,
			retBodyMutation:             bedrockTranslatedBody,
		}

		p := &chatCompletionProcessorUpstreamFilter{
			requestHeaders: headers,
			metrics:        chatMetrics,
			logger:         slog.Default(),
			translator:     &translator,
			handler:        &mockBackendAuthHandler{},
		}

		backend := &filterapi.Backend{
			Name:         "retry-backend",
			Schema:       filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock},
			BodyMutation: bodyMutations,
		}

		rp := &chatCompletionProcessorRouterFilter{
			originalRequestBody:    requestBody,
			originalRequestBodyRaw: originalRequestBodyRaw,
			requestHeaders:         headers,
			upstreamFilterCount:    0,
			forceBodyMutation:      false,
			config:                 &filterapi.RuntimeConfig{},
			logger:                 slog.Default(),
		}

		err := p.SetBackend(context.Background(), &filterapi.RuntimeBackend{
			Backend: backend,
			Handler: &mockBackendAuthHandler{},
		}, "test-route", rp)
		require.NoError(t, err)

		p.translator = &translator

		require.NotNil(t, p.bodyMutator)
		require.False(t, p.onRetry())

		// --- First Attempt ---
		ctx := context.Background()
		response, err := p.ProcessRequestHeaders(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, response)

		commonRes := response.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
		mutatedBody := commonRes.BodyMutation.GetBody()
		require.NotNil(t, mutatedBody)

		var result map[string]interface{}
		err = json.Unmarshal(mutatedBody, &result)
		require.NoError(t, err)

		// Verify mutations were applied to the translated Bedrock body
		serviceTier, ok := result["serviceTier"].(map[string]interface{})
		require.True(t, ok, "serviceTier should be present after mutation")
		require.Equal(t, "default", serviceTier["type"], "Body mutation should set serviceTier.type to default")

		// Verify other Bedrock fields from translation are preserved
		inferenceConfig, ok := result["inferenceConfig"].(map[string]interface{})
		require.True(t, ok, "inferenceConfig should be present in Bedrock format")
		require.Equal(t, 0.7, inferenceConfig["temperature"], "Temperature from Bedrock translation should be preserved")
		require.Equal(t, float64(1024), inferenceConfig["maxTokens"], "maxTokens from Bedrock translation should be preserved")

		// Verify Bedrock format is maintained (mutations applied to translated body, not original)
		require.NotContains(t, result, "stream", "OpenAI 'stream' field should NOT be present in Bedrock format")
		require.NotContains(t, result, "model", "OpenAI 'model' field should NOT be present in Bedrock format")
		require.Contains(t, result, "messages", "Bedrock 'messages' field should be present")

		// --- Retry Scenario ---
		modifiedBedrockBody := []byte(`{"messages":[{"role":"user","content":[{"text":"Hello"}]}],"inferenceConfig":{"temperature":0.7,"maxTokens":1024},"serviceTier":{"type":"auto"}}`)

		retryTranslator := mockTranslator{
			t:                           t,
			expRequestBody:              requestBody,
			expForceRequestBodyMutation: true,
			retBodyMutation:             modifiedBedrockBody,
		}

		retryBackend := &filterapi.Backend{
			Name:         "retry-backend-2",
			Schema:       filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock},
			BodyMutation: bodyMutations, // Use same mutation as first attempt
		}

		// Simulate retry by incrementing upstreamFilterCount
		err = p.SetBackend(context.Background(), &filterapi.RuntimeBackend{
			Backend: retryBackend,
			Handler: &mockBackendAuthHandler{},
		}, "test-route", rp)
		require.NoError(t, err)

		// Restore translator for retry
		p.translator = &retryTranslator

		require.NotNil(t, p.bodyMutator)
		require.True(t, p.onRetry())

		// Process request headers for retry
		retryResponse, err := p.ProcessRequestHeaders(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, retryResponse)

		// Verify the body mutation was applied to the modified Bedrock body
		retryCommonRes := retryResponse.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
		retryMutatedBody := retryCommonRes.BodyMutation.GetBody()
		require.NotNil(t, retryMutatedBody)

		var retryResult map[string]interface{}
		err = json.Unmarshal(retryMutatedBody, &retryResult)
		require.NoError(t, err)

		// Verify retry mutations were applied to the modified Bedrock body (NOT restored to original)
		retryServiceTier, ok := retryResult["serviceTier"].(map[string]interface{})
		require.True(t, ok, "serviceTier should be present after retry mutation")
		require.Equal(t, "default", retryServiceTier["type"], "Retry mutation should set serviceTier.type to default")

		retryInferenceConfig, ok := retryResult["inferenceConfig"].(map[string]interface{})
		require.True(t, ok, "inferenceConfig should be present in Bedrock format")
		require.Equal(t, 0.7, retryInferenceConfig["temperature"], "Temperature should be preserved from modified body")
		require.Equal(t, float64(1024), retryInferenceConfig["maxTokens"], "maxTokens should be preserved from modified body")

		// Verify Bedrock format is still maintained (not restored to OpenAI)
		require.NotContains(t, retryResult, "stream", "OpenAI 'stream' field should NOT be present on retry")
		require.NotContains(t, retryResult, "model", "OpenAI 'model' field should NOT be present on retry")
		require.Contains(t, retryResult, "messages", "Bedrock 'messages' field should be present on retry")
	})
}

func Test_buildDynamicMetadata(t *testing.T) {
	t.Run("sets model_name_override from request headers", func(t *testing.T) {
		costs := &metrics.TokenUsage{}
		headers := map[string]string{internalapi.ModelNameHeaderKeyDefault: "gpt-4"}

		md, err := buildDynamicMetadata(nil, []filterapi.RuntimeRequestCost{}, costs, headers, "", "", "")
		require.NoError(t, err)
		require.NotNil(t, md)

		inner := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Equal(t, "gpt-4", inner.Fields["model_name_override"].GetStringValue())
	})

	t.Run("model_name_override reflects actual model after backend override", func(t *testing.T) {
		costs := &metrics.TokenUsage{}
		// After backend override, the header contains the backend-specific model name.
		headers := map[string]string{internalapi.ModelNameHeaderKeyDefault: "us.anthropic.claude-sonnet-4.5-v2"}

		md, err := buildDynamicMetadata(nil, []filterapi.RuntimeRequestCost{}, costs, headers, "default/my-backend", "", "")
		require.NoError(t, err)
		require.NotNil(t, md)

		inner := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Equal(t, "us.anthropic.claude-sonnet-4.5-v2", inner.Fields["model_name_override"].GetStringValue())
	})

	t.Run("sets ai_service_backend_name when provided", func(t *testing.T) {
		costs := &metrics.TokenUsage{}
		headers := map[string]string{internalapi.ModelNameHeaderKeyDefault: "gpt-4"}

		md, err := buildDynamicMetadata(nil, []filterapi.RuntimeRequestCost{}, costs, headers, "ns/backend-a", "", "")
		require.NoError(t, err)
		require.NotNil(t, md)

		inner := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Equal(t, "ns/backend-a", inner.Fields["ai_service_backend_name"].GetStringValue())
		require.Nil(t, inner.Fields["backend_name"], "emitted in the request headers phase instead")
	})

	t.Run("omits ai_service_backend_name when empty", func(t *testing.T) {
		costs := &metrics.TokenUsage{}
		headers := map[string]string{internalapi.ModelNameHeaderKeyDefault: "gpt-4"}

		md, err := buildDynamicMetadata(nil, []filterapi.RuntimeRequestCost{}, costs, headers, "", "", "")
		require.NoError(t, err)
		require.NotNil(t, md)

		inner := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Nil(t, inner.Fields["ai_service_backend_name"])
	})

	t.Run("includes token usage costs alongside model_name_override", func(t *testing.T) {
		config := &filterapi.RuntimeConfig{
			RequestCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeOutputToken, MetadataKey: "output_tokens"}},
				{LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeInputToken, MetadataKey: "input_tokens"}},
			},
		}
		costs := &metrics.TokenUsage{}
		costs.SetOutputTokens(100)
		costs.SetInputTokens(50)
		headers := map[string]string{internalapi.ModelNameHeaderKeyDefault: "claude-sonnet"}

		md, err := buildDynamicMetadata(nil, config.RequestCosts, costs, headers, "default/backend", "", "")
		require.NoError(t, err)
		require.NotNil(t, md)

		inner := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Equal(t, "claude-sonnet", inner.Fields["model_name_override"].GetStringValue())
		require.Equal(t, "default/backend", inner.Fields["ai_service_backend_name"].GetStringValue())
		require.Equal(t, float64(100), inner.Fields["output_tokens"].GetNumberValue())
		require.Equal(t, float64(50), inner.Fields["input_tokens"].GetNumberValue())
	})

	t.Run("model_name_override is empty string when header not set", func(t *testing.T) {
		costs := &metrics.TokenUsage{}
		headers := map[string]string{}

		md, err := buildDynamicMetadata(nil, []filterapi.RuntimeRequestCost{}, costs, headers, "", "", "")
		require.NoError(t, err)
		require.NotNil(t, md)

		inner := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		// model_name_override should still be present, just with empty value.
		require.Empty(t, inner.Fields["model_name_override"].GetStringValue())
	})
}

func Test_buildBackendDynamicMetadata(t *testing.T) {
	t.Run("empty backend name yields no metadata", func(t *testing.T) {
		require.Nil(t, buildBackendDynamicMetadata(""))
	})

	t.Run("emits the full per-route rule ref name", func(t *testing.T) {
		name := internalapi.PerRouteRuleRefBackendName("ns", "backend", "route", 0, 1)
		md := buildBackendDynamicMetadata(name)
		require.NotNil(t, md)
		require.Equal(t, name, md.Fields[internalapi.AIGatewayFilterMetadataNamespace].
			GetStructValue().Fields["backend_name"].GetStringValue())
	})
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessRequestHeaders_backendMetadata(t *testing.T) {
	const backendName = "ns/backend/route/route-a/rule/0/ref/1"

	for _, tc := range []struct {
		name            string
		retBodyMutation []byte // drives the CONTINUE vs CONTINUE_AND_REPLACE branch.
	}{
		{name: "body replaced", retBodyMutation: []byte("some body")},
		{name: "body untouched", retBodyMutation: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := bodyFromModel(t, "some-model", false, nil)
			var expBody openai.ChatCompletionRequest
			require.NoError(t, json.Unmarshal(body, &expBody))

			p := &chatCompletionProcessorUpstreamFilter{
				parent: &chatCompletionProcessorRouterFilter{
					config:                 &filterapi.RuntimeConfig{},
					logger:                 slog.Default(),
					originalRequestBodyRaw: body,
					originalRequestBody:    &expBody,
					originalModel:          "some-model",
				},
				requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "some-model"},
				metrics:        &mockMetrics{},
				translator:     &mockTranslator{t: t, expRequestBody: &expBody, retBodyMutation: tc.retBodyMutation},
				backendName:    backendName,
			}

			resp, err := p.ProcessRequestHeaders(t.Context(), nil)
			require.NoError(t, err)
			require.NotNil(t, resp.DynamicMetadata)
			require.Equal(t, backendName, resp.DynamicMetadata.
				Fields[internalapi.AIGatewayFilterMetadataNamespace].
				GetStructValue().Fields["backend_name"].GetStringValue())
		})
	}
}

func Test_mergeDynamicMetadata(t *testing.T) {
	t.Run("nil base returns extra", func(t *testing.T) {
		extra := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(&structpb.Struct{
					Fields: map[string]*structpb.Value{
						"key1": structpb.NewStringValue("val1"),
					},
				}),
			},
		}
		result := mergeDynamicMetadata(nil, extra)
		require.Equal(t, extra, result)
	})

	t.Run("nil extra returns base", func(t *testing.T) {
		base := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(&structpb.Struct{
					Fields: map[string]*structpb.Value{
						"key1": structpb.NewStringValue("val1"),
					},
				}),
			},
		}
		result := mergeDynamicMetadata(base, nil)
		require.Equal(t, base, result)
	})

	t.Run("merges extra fields into base", func(t *testing.T) {
		base := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(&structpb.Struct{
					Fields: map[string]*structpb.Value{
						"backend_name": structpb.NewStringValue("ns/backend"),
					},
				}),
			},
		}
		extra := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(&structpb.Struct{
					Fields: map[string]*structpb.Value{
						"model_name_override": structpb.NewStringValue("gpt-4"),
					},
				}),
			},
		}
		result := mergeDynamicMetadata(base, extra)
		inner := result.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Equal(t, "ns/backend", inner.Fields["backend_name"].GetStringValue())
		require.Equal(t, "gpt-4", inner.Fields["model_name_override"].GetStringValue())
	})

	t.Run("extra overwrites existing keys in base", func(t *testing.T) {
		base := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(&structpb.Struct{
					Fields: map[string]*structpb.Value{
						"key": structpb.NewStringValue("old-value"),
					},
				}),
			},
		}
		extra := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				internalapi.AIGatewayFilterMetadataNamespace: structpb.NewStructValue(&structpb.Struct{
					Fields: map[string]*structpb.Value{
						"key": structpb.NewStringValue("new-value"),
					},
				}),
			},
		}
		result := mergeDynamicMetadata(base, extra)
		inner := result.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
		require.Equal(t, "new-value", inner.Fields["key"].GetStringValue())
	})
}

func buildTestMultipartBody(t *testing.T, fields map[string]string, filename string, fileData []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, writer.WriteField(k, v))
	}
	if filename != "" && fileData != nil {
		part, err := writer.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = part.Write(fileData)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes(), writer.FormDataContentType()
}

func Test_transcriptionProcessorRouterFilter_ProcessRequestBody_MultipartDispatch(t *testing.T) {
	body, ct := buildTestMultipartBody(t, map[string]string{
		"model": "whisper-1",
	}, "test.mp3", []byte("fake-audio-data"))

	headers := map[string]string{":path": "/v1/audio/transcriptions", "content-type": ct}
	p := &transcriptionProcessorRouterFilter{
		config:         &filterapi.RuntimeConfig{},
		requestHeaders: headers,
		logger:         slog.Default(),
		tracer:         tracingapi.NoopTracer[openai.TranscriptionRequest, openai.TranscriptionResponse, openai.TranscriptionStreamEvent]{},
	}

	resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: body})
	require.NoError(t, err)
	require.NotNil(t, resp)

	re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
	require.True(t, ok)
	require.NotNil(t, re.RequestBody)

	setHeaders := re.RequestBody.GetResponse().GetHeaderMutation().SetHeaders
	require.GreaterOrEqual(t, len(setHeaders), 1)
	require.Equal(t, internalapi.ModelNameHeaderKeyDefault, setHeaders[0].Header.Key)
	require.Equal(t, "whisper-1", string(setHeaders[0].Header.RawValue))
}

func Test_transcriptionProcessorUpstreamFilter_SetBackend_ContentTypeSetter(t *testing.T) {
	contentType := "multipart/form-data; boundary=testboundary"
	headers := map[string]string{":path": "/v1/audio/transcriptions", "content-type": contentType}
	mm := &mockMetrics{}
	p := &transcriptionProcessorUpstreamFilter{
		requestHeaders: headers,
		metrics:        mm,
	}
	r := &transcriptionProcessorRouterFilter{
		requestHeaders: headers,
	}

	err := p.SetBackend(t.Context(), &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   "transcription-backend",
			Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
		},
	}, "", r)
	require.NoError(t, err)
	require.NotNil(t, p.translator)
	require.NotNil(t, r.upstreamFilter)
}

func TestBuildDynamicMetadata_routeScoped(t *testing.T) {
	hdr := map[string]string{internalapi.ModelNameHeaderKeyDefault: "m"}

	tests := []struct {
		name           string
		requestCosts   []filterapi.RuntimeRequestCost
		inputTokens    uint32
		totalTokens    uint32
		requestHeaders map[string]string
		backendName    string
		routeName      string
		wantCostValues map[string]float64
		wantAbsent     []string
	}{
		{
			name: "route name mismatch key absent",
			requestCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "k", RouteName: "ns/other", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			inputTokens:    10,
			requestHeaders: hdr,
			backendName:    "be",
			routeName:      "ns/this",
			wantAbsent:     []string{"k"},
		},
		{
			name: "two routes same key different route names",
			requestCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "billing", RouteName: "ns/free", Type: filterapi.LLMRequestCostTypeTotalToken}},
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "billing", RouteName: "ns/paid", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			inputTokens:    3,
			totalTokens:    99,
			requestHeaders: hdr,
			backendName:    "be",
			routeName:      "ns/paid",
			wantCostValues: map[string]float64{"billing": 3},
		},
		{
			name: "model filter skips non-matching model",
			requestCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "quota_cost", RouteName: "ns/r", Type: filterapi.LLMRequestCostTypeInputToken, Model: "claude"}},
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "quota_cost", RouteName: "ns/r", Type: filterapi.LLMRequestCostTypeTotalToken, Model: "gpt-4"}},
			},
			inputTokens:    10,
			totalTokens:    25,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "gpt-4"},
			backendName:    "be",
			routeName:      "ns/r",
			wantCostValues: map[string]float64{"quota_cost": 25},
		},
		{
			name: "empty model field matches any model",
			requestCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "cost", RouteName: "ns/r", Type: filterapi.LLMRequestCostTypeInputToken, Model: ""}},
			},
			inputTokens:    7,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "any-model"},
			backendName:    "be",
			routeName:      "ns/r",
			wantCostValues: map[string]float64{"cost": 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tu metrics.TokenUsage
			tu.SetInputTokens(tt.inputTokens)
			tu.SetTotalTokens(tt.totalTokens)

			md, err := buildDynamicMetadata(nil, tt.requestCosts, &tu, tt.requestHeaders, tt.backendName, tt.routeName, "")
			require.NoError(t, err)

			ns := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue().Fields
			for k, want := range tt.wantCostValues {
				require.Equal(t, want, ns[k].GetNumberValue(), "key %q", k)
			}
			for _, k := range tt.wantAbsent {
				_, exists := ns[k]
				require.False(t, exists, "key %q should be absent from metadata", k)
			}
		})
	}
}

// TestBuildDynamicMetadata_GlobalAndRouteScoped tests the two-tier precedence logic
// where route-scoped costs override global costs on a per-metadataKey basis.
func TestBuildDynamicMetadata_GlobalAndRouteScoped(t *testing.T) {
	tests := []struct {
		name           string
		globalCosts    []filterapi.RuntimeGlobalRequestCost
		routeCosts     []filterapi.RuntimeRequestCost
		inputTokens    uint32
		outputTokens   uint32
		totalTokens    uint32
		requestHeaders map[string]string
		backendName    string
		routeName      string
		wantCostValues map[string]float64
		wantAbsent     []string
	}{
		{
			name: "global cost used when no route override",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			routeCosts:     []filterapi.RuntimeRequestCost{},
			inputTokens:    100,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/standard-route",
			wantCostValues: map[string]float64{"billing_charges": 100},
		},
		{
			name: "route cost overrides global for same metadataKey",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			routeCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "billing_charges", RouteName: "ns/premium-route", Type: filterapi.LLMRequestCostTypeOutputToken}},
			},
			inputTokens:    100,
			outputTokens:   50,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/premium-route",
			wantCostValues: map[string]float64{"billing_charges": 50}, // Route override (output tokens) wins
		},
		{
			name: "global cost used for different route",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			routeCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "billing_charges", RouteName: "ns/premium-route", Type: filterapi.LLMRequestCostTypeOutputToken}},
			},
			inputTokens:    100,
			outputTokens:   50,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/standard-route", // Different route - uses global
			wantCostValues: map[string]float64{"billing_charges": 100},
		},
		{
			name: "multiple keys mixed global and route",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "billing_charges", Type: filterapi.LLMRequestCostTypeInputToken}},
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "total_tokens", Type: filterapi.LLMRequestCostTypeTotalToken}},
			},
			routeCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "billing_charges", RouteName: "ns/route1", Type: filterapi.LLMRequestCostTypeOutputToken}},
			},
			inputTokens:    100,
			outputTokens:   50,
			totalTokens:    150,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/route1",
			wantCostValues: map[string]float64{
				"billing_charges": 50,  // Route override
				"total_tokens":    150, // Global (no route override)
			},
		},
		{
			name: "route cost for wrong route does not override global",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "cost", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			routeCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "cost", RouteName: "ns/other-route", Type: filterapi.LLMRequestCostTypeOutputToken}},
			},
			inputTokens:    100,
			outputTokens:   50,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/this-route",
			wantCostValues: map[string]float64{"cost": 100}, // Global wins (route cost is for different route)
		},
		{
			name: "no global or route cost for key means key absent",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "other_key", Type: filterapi.LLMRequestCostTypeInputToken}},
			},
			routeCosts: []filterapi.RuntimeRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "route_key", RouteName: "ns/other-route", Type: filterapi.LLMRequestCostTypeOutputToken}},
			},
			inputTokens:    100,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/this-route",
			wantCostValues: map[string]float64{"other_key": 100},
			wantAbsent:     []string{"missing_key", "route_key"}, // route_key doesn't match route
		},
		{
			name: "CEL expression in global cost",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{
					GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "custom_cost", Type: filterapi.LLMRequestCostTypeCEL, CEL: "input_tokens + input_tokens + output_tokens"},
					CELProg:              mustCompileCEL(t, "input_tokens + input_tokens + output_tokens"),
				},
			},
			routeCosts:     []filterapi.RuntimeRequestCost{},
			inputTokens:    100,
			outputTokens:   50,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/route1",
			wantCostValues: map[string]float64{"custom_cost": 250}, // 100 + 100 + 50 = 250
		},
		{
			name: "CEL expression in route overrides global",
			globalCosts: []filterapi.RuntimeGlobalRequestCost{
				{
					GlobalLLMRequestCost: &filterapi.GlobalLLMRequestCost{MetadataKey: "custom_cost", Type: filterapi.LLMRequestCostTypeCEL, CEL: "input_tokens + output_tokens"},
					CELProg:              mustCompileCEL(t, "input_tokens + output_tokens"),
				},
			},
			routeCosts: []filterapi.RuntimeRequestCost{
				{
					LLMRequestCost: &filterapi.LLMRequestCost{MetadataKey: "custom_cost", RouteName: "ns/free-route", Type: filterapi.LLMRequestCostTypeCEL, CEL: "0"},
					CELProg:        mustCompileCEL(t, "0"),
				},
			},
			inputTokens:    100,
			outputTokens:   50,
			requestHeaders: map[string]string{internalapi.ModelNameHeaderKeyDefault: "model"},
			backendName:    "backend",
			routeName:      "ns/free-route",
			wantCostValues: map[string]float64{"custom_cost": 0}, // Route CEL overrides global
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tu metrics.TokenUsage
			tu.SetInputTokens(tt.inputTokens)
			tu.SetOutputTokens(tt.outputTokens)
			tu.SetTotalTokens(tt.totalTokens)

			md, err := buildDynamicMetadata(tt.globalCosts, tt.routeCosts, &tu, tt.requestHeaders, tt.backendName, tt.routeName, "")
			require.NoError(t, err)

			ns := md.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue().Fields
			for k, want := range tt.wantCostValues {
				require.Equal(t, want, ns[k].GetNumberValue(), "key %q", k)
			}
			for _, k := range tt.wantAbsent {
				_, exists := ns[k]
				require.False(t, exists, "key %q should be absent from metadata", k)
			}
		})
	}
}

// mustCompileCEL is a test helper that compiles a CEL expression or fails the test.
func mustCompileCEL(t *testing.T, expr string) cel.Program {
	t.Helper()
	prog, err := llmcostcel.NewProgram(expr)
	require.NoError(t, err)
	return prog
}
