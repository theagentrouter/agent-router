// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

var (
	_ Processor                                 = &mockProcessor{}
	_ translator.OpenAIChatCompletionTranslator = &mockTranslator{}
)

func newMockProcessor(_ *filterapi.RuntimeConfig, _ *slog.Logger) Processor {
	return &mockProcessor{}
}

// mockProcessor implements [Processor] for testing.
type mockProcessor struct {
	t                     *testing.T
	expHeaderMap          *corev3.HeaderMap
	expBody               *extprocv3.HttpBody
	retProcessingResponse *extprocv3.ProcessingResponse
	retErr                error
}

// SetBackend implements [Processor.SetBackend].
func (m mockProcessor) SetBackend(context.Context, *filterapi.RuntimeBackend, string, Processor) error {
	return nil
}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (m mockProcessor) ProcessRequestHeaders(_ context.Context, headerMap *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	require.Equal(m.t, m.expHeaderMap, headerMap)
	return m.retProcessingResponse, m.retErr
}

// ProcessRequestBody implements [Processor.ProcessRequestBody].
func (m mockProcessor) ProcessRequestBody(_ context.Context, body *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	require.Equal(m.t, m.expBody, body)
	return m.retProcessingResponse, m.retErr
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders].
func (m mockProcessor) ProcessResponseHeaders(_ context.Context, headerMap *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	require.Equal(m.t, m.expHeaderMap, headerMap)
	return m.retProcessingResponse, m.retErr
}

// ProcessResponseBody implements [Processor.ProcessResponseBody].
func (m mockProcessor) ProcessResponseBody(_ context.Context, body *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	require.Equal(m.t, m.expBody, body)
	return m.retProcessingResponse, m.retErr
}

// mockTranslator implements [translator.Translator] for testing.
type mockTranslator struct {
	t                           *testing.T
	expHeaders                  map[string]string
	expRequestBody              *openai.ChatCompletionRequest
	expResponseBody             *extprocv3.HttpBody
	retHeaderMutation           []internalapi.Header
	retBodyMutation             []byte
	retUsedToken                metrics.TokenUsage
	retResponseModel            internalapi.ResponseModel
	retErr                      error
	expForceRequestBodyMutation bool
}

// RequestBody implements [translator.OpenAIChatCompletionTranslator].
func (m *mockTranslator) RequestBody(_ []byte, body *openai.ChatCompletionRequest, forceRequestBodyMutation bool) (newHeaders []internalapi.Header, newBody []byte, err error) {
	require.Equal(m.t, m.expRequestBody, body)
	require.Equal(m.t, m.expForceRequestBodyMutation, forceRequestBodyMutation)
	return m.retHeaderMutation, m.retBodyMutation, m.retErr
}

// ResponseHeaders implements [translator.OpenAIChatCompletionTranslator].
func (m *mockTranslator) ResponseHeaders(headers map[string]string) (newHeaders []internalapi.Header, err error) {
	require.Equal(m.t, m.expHeaders, headers)
	return m.retHeaderMutation, m.retErr
}

// ResponseError implements [translator.OpenAIChatCompletionTranslator].
func (m *mockTranslator) ResponseError(_ map[string]string, body io.Reader) (newHeaders []internalapi.Header, newBody []byte, err error) {
	if m.expResponseBody != nil {
		buf, err := io.ReadAll(body)
		require.NoError(m.t, err)
		require.Equal(m.t, m.expResponseBody.Body, buf)
	}
	return m.retHeaderMutation, m.retBodyMutation, m.retErr
}

// ResponseBody implements [translator.OpenAIChatCompletionTranslator].
func (m *mockTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, _ tracingapi.ChatCompletionSpan) (newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error) {
	if m.expResponseBody != nil {
		buf, err := io.ReadAll(body)
		require.NoError(m.t, err)
		require.Equal(m.t, m.expResponseBody.Body, buf)
	}
	return m.retHeaderMutation, m.retBodyMutation, m.retUsedToken, m.retResponseModel, m.retErr
}

// mockExternalProcessingStream implements [extprocv3.ExternalProcessor_ProcessServer] for testing.
type mockExternalProcessingStream struct {
	t                 *testing.T
	ctx               context.Context
	expResponseOnSend *extprocv3.ProcessingResponse
	retRecv           *extprocv3.ProcessingRequest
	retErr            error
}

// Context implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) Context() context.Context {
	return m.ctx
}

// Send implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) Send(response *extprocv3.ProcessingResponse) error {
	require.Equal(m.t, m.expResponseOnSend, response)
	return m.retErr
}

// Recv implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) Recv() (*extprocv3.ProcessingRequest, error) {
	return m.retRecv, m.retErr
}

// SetHeader implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) SetHeader(_ metadata.MD) error { panic("TODO") }

// SendHeader implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) SendHeader(metadata.MD) error { panic("TODO") }

// SetTrailer implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) SetTrailer(metadata.MD) { panic("TODO") }

// SendMsg implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) SendMsg(any) error { panic("TODO") }

// RecvMsg implements [extprocv3.ExternalProcessor_ProcessServer].
func (m mockExternalProcessingStream) RecvMsg(any) error { panic("TODO") }

var _ extprocv3.ExternalProcessor_ProcessServer = &mockExternalProcessingStream{}

// mockMetricsFactory implements [metrics.Factory] for testing.
type mockMetricsFactory struct{}

// NewMetrics implements [metrics.Factory.NewMetrics].
func (m *mockMetricsFactory) NewMetrics() metrics.Metrics {
	return &mockMetrics{}
}

// mockMetrics implements [metrics.Metrics] for testing.
type mockMetrics struct {
	requestStart                 time.Time
	originalModel                string
	requestModel                 string
	responseModel                string
	backend                      string
	requestSuccessCount          int
	requestErrorCount            int
	inputTokenCount              int
	cachedInputTokenCount        int
	cacheCreationInputTokenCount int
	outputTokenCount             int
	// streamingOutputTokens tracks the cumulative output tokens recorded via RecordTokenLatency.
	streamingOutputTokens int
	timeToFirstToken      float64
	interTokenLatency     float64
	timeToFirstTokenMs    float64
	interTokenLatencyMs   float64
}

// StartRequest implements [metrics.Metrics].
func (m *mockMetrics) StartRequest(_ map[string]string) { m.requestStart = time.Now() }

// SetOriginalModel implements [metrics.Metrics].
func (m *mockMetrics) SetOriginalModel(originalModel internalapi.OriginalModel) {
	m.originalModel = originalModel
}

// SetRequestModel implements [metrics.Metrics].
func (m *mockMetrics) SetRequestModel(requestModel internalapi.RequestModel) {
	m.requestModel = requestModel
}

// SetResponseModel implements [metrics.Metrics].
func (m *mockMetrics) SetResponseModel(responseModel internalapi.ResponseModel) {
	m.responseModel = responseModel
}

// SetBackend implements [metrics.Metrics].
func (m *mockMetrics) SetBackend(backend *filterapi.Backend) { m.backend = backend.Name }

// RecordTokenUsage implements [metrics.Metrics].
func (m *mockMetrics) RecordTokenUsage(_ context.Context, usage metrics.TokenUsage, _ map[string]string) {
	if input, ok := usage.InputTokens(); ok {
		m.inputTokenCount += int(input)
	}
	if cachedInput, ok := usage.CachedInputTokens(); ok {
		m.cachedInputTokenCount += int(cachedInput)
	}
	if cacheCreationInput, ok := usage.CacheCreationInputTokens(); ok {
		m.cacheCreationInputTokenCount += int(cacheCreationInput)
	}
	if output, ok := usage.OutputTokens(); ok {
		m.outputTokenCount += int(output)
	}
}

// RecordTokenLatency implements [metrics.Metrics].
// For streaming responses, this tracks output tokens incrementally to compute latency metrics.
func (m *mockMetrics) RecordTokenLatency(_ context.Context, output uint32, _ bool, _ map[string]string) {
	m.streamingOutputTokens += int(output)
}

// GetTimeToFirstTokenMs implements [metrics.Metrics].
func (m *mockMetrics) GetTimeToFirstTokenMs() float64 {
	// If timeToFirstTokenMs is explicitly set, return it
	if m.timeToFirstTokenMs != 0 {
		return m.timeToFirstTokenMs
	}
	// Otherwise use the default behavior
	m.timeToFirstToken = 1.0
	return m.timeToFirstToken * 1000
}

// GetInterTokenLatencyMs implements [metrics.Metrics].
func (m *mockMetrics) GetInterTokenLatencyMs() float64 {
	// If interTokenLatencyMs is explicitly set, return it
	if m.interTokenLatencyMs != 0 {
		return m.interTokenLatencyMs
	}
	// Otherwise use the default behavior
	m.interTokenLatency = 0.5
	return m.interTokenLatency * 1000
}

// RecordRequestCompletion implements [metrics.Metrics].
func (m *mockMetrics) RecordRequestCompletion(_ context.Context, success bool, _ map[string]string) {
	if success {
		m.requestSuccessCount++
	} else {
		m.requestErrorCount++
	}
}

// RequireSelectedModel asserts the models set on the metrics.
func (m *mockMetrics) RequireSelectedModel(t *testing.T, originalModel, requestModel, responseModel string) {
	require.Equal(t, originalModel, m.originalModel)
	require.Equal(t, requestModel, m.requestModel)
	require.Equal(t, responseModel, m.responseModel)
}

// RequireModelAndBackendSet asserts the model and backend set on the metrics.
func (m *mockMetrics) RequireSelectedBackend(t *testing.T, backend string) {
	require.Equal(t, backend, m.backend)
}

// RequireRequestFailure asserts the request was marked as a failure.
func (m *mockMetrics) RequireRequestFailure(t *testing.T) {
	require.Zero(t, m.requestSuccessCount)
	require.Equal(t, 1, m.requestErrorCount)
}

func (m *mockMetrics) RequireTokensRecorded(t *testing.T, expectedInput, expectedCachedInput, expectedWriteCachedInput, expectedOutput int) {
	require.Equal(t, expectedInput, m.inputTokenCount)
	require.Equal(t, expectedCachedInput, m.cachedInputTokenCount)
	require.Equal(t, expectedWriteCachedInput, m.cacheCreationInputTokenCount)
	require.Equal(t, expectedOutput, m.outputTokenCount)
}

// RequireRequestNotCompleted asserts the request was not completed.
func (m *mockMetrics) RequireRequestNotCompleted(t *testing.T) {
	require.Zero(t, m.requestSuccessCount)
	require.Zero(t, m.requestErrorCount)
}

// RequireRequestSuccess asserts the request was marked as a success.
func (m *mockMetrics) RequireRequestSuccess(t *testing.T) {
	require.Equal(t, 1, m.requestSuccessCount)
	require.Zero(t, m.requestErrorCount)
}

var _ metrics.Metrics = &mockMetrics{}

// mockChatCompletionSpan implements [tracingapi.ChatCompletionSpan], recording how the span ended.
type mockChatCompletionSpan struct {
	endedOnErrorCount int
	endedCount        int
	errorStatusCode   int
	errorBody         []byte
}

func (m *mockChatCompletionSpan) RecordResponseChunk(*openai.ChatCompletionResponseChunk) {}

func (m *mockChatCompletionSpan) RecordResponse(*openai.ChatCompletionResponse) {}

func (m *mockChatCompletionSpan) EndSpanOnError(statusCode int, body []byte) {
	m.endedOnErrorCount++
	m.errorStatusCode = statusCode
	m.errorBody = body
}

func (m *mockChatCompletionSpan) EndSpan() { m.endedCount++ }

var _ tracingapi.ChatCompletionSpan = &mockChatCompletionSpan{}

// mockBackendChatCompletionSpan additionally implements [tracingapi.BackendSpan],
// which only spans whose semantic convention records the resolved backend do.
type mockBackendChatCompletionSpan struct {
	mockChatCompletionSpan
	backends []tracingapi.Backend
}

func (m *mockBackendChatCompletionSpan) RecordBackend(backend tracingapi.Backend) {
	m.backends = append(m.backends, backend)
}

var _ tracingapi.BackendSpan = &mockBackendChatCompletionSpan{}

// mockBackendAuthHandler implements [filterapi.BackendAuthHandler] for testing.
type mockBackendAuthHandler struct{}

// Do implements [filterapi.BackendAuthHandler.Do].
func (m *mockBackendAuthHandler) Do(context.Context, map[string]string, []byte) ([]internalapi.Header, error) {
	return []internalapi.Header{{"foo", "mock-auth-handler"}}, nil
}

// mockBackendAuthHandlerError implements [filterapi.BackendAuthHandler] for testing auth errors.
type mockBackendAuthHandlerError struct {
	err error
}

// Do implements [filterapi.BackendAuthHandler.Do].
func (m *mockBackendAuthHandlerError) Do(context.Context, map[string]string, []byte) ([]internalapi.Header, error) {
	return nil, m.err
}
