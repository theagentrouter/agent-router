// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mainlib

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	prometheusmodel "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
)

func TestStartAdminServer_Metrics(t *testing.T) {
	tests := []struct {
		name           string
		metricFamilies []*prometheusmodel.MetricFamily
		expectedBody   string
	}{
		{
			name: "successful chat completion - ollama with qwen2.5:0.5b",
			metricFamilies: []*prometheusmodel.MetricFamily{
				{
					Name: new("gen_ai_client_token_usage"),
					Help: new("Number of tokens processed."),
					Type: prometheusmodel.MetricType_HISTOGRAM.Enum(),
					Metric: []*prometheusmodel.Metric{
						{
							Label: []*prometheusmodel.LabelPair{
								{Name: new("gen_ai_operation_name"), Value: new("chat")},
								{Name: new("gen_ai_provider_name"), Value: new("openai")},
								{Name: new("gen_ai_request_model"), Value: new("qwen2.5:0.5b")},
								{Name: new("gen_ai_response_model"), Value: new("qwen2.5:0.5b")},
								{Name: new("gen_ai_token_type"), Value: new("input")},
								{Name: new("otel_scope_name"), Value: new("envoyproxy/ai-gateway")},
								{Name: new("otel_scope_schema_url"), Value: new("")},
								{Name: new("otel_scope_version"), Value: new("")},
							},
							Histogram: &prometheusmodel.Histogram{
								SampleCount: proto.Uint64(1),
								SampleSum:   proto.Float64(44),
								Bucket: []*prometheusmodel.Bucket{
									{CumulativeCount: proto.Uint64(1), UpperBound: new(math.Inf(1))},
								},
							},
						},
						{
							Label: []*prometheusmodel.LabelPair{
								{Name: new("gen_ai_operation_name"), Value: new("chat")},
								{Name: new("gen_ai_provider_name"), Value: new("openai")},
								{Name: new("gen_ai_request_model"), Value: new("qwen2.5:0.5b")},
								{Name: new("gen_ai_response_model"), Value: new("qwen2.5:0.5b")},
								{Name: new("gen_ai_token_type"), Value: new("output")},
								{Name: new("otel_scope_name"), Value: new("envoyproxy/ai-gateway")},
								{Name: new("otel_scope_schema_url"), Value: new("")},
								{Name: new("otel_scope_version"), Value: new("")},
							},
							Histogram: &prometheusmodel.Histogram{
								SampleCount: proto.Uint64(1),
								SampleSum:   proto.Float64(14),
								Bucket: []*prometheusmodel.Bucket{
									{CumulativeCount: proto.Uint64(1), UpperBound: new(math.Inf(1))},
								},
							},
						},
					},
				},
				{
					Name: new("gen_ai_server_request_duration_seconds"),
					Help: new("Generative AI server request duration such as time-to-last byte or last output token."),
					Type: prometheusmodel.MetricType_HISTOGRAM.Enum(),
					Metric: []*prometheusmodel.Metric{
						{
							Label: []*prometheusmodel.LabelPair{
								{Name: new("gen_ai_operation_name"), Value: new("chat")},
								{Name: new("gen_ai_provider_name"), Value: new("openai")},
								{Name: new("gen_ai_request_model"), Value: new("qwen2.5:0.5b")},
								{Name: new("gen_ai_response_model"), Value: new("qwen2.5:0.5b")},
								{Name: new("otel_scope_name"), Value: new("envoyproxy/ai-gateway")},
								{Name: new("otel_scope_schema_url"), Value: new("")},
								{Name: new("otel_scope_version"), Value: new("")},
							},
							Histogram: &prometheusmodel.Histogram{
								SampleCount: proto.Uint64(1),
								SampleSum:   new(10.808095311),
								Bucket: []*prometheusmodel.Bucket{
									{CumulativeCount: proto.Uint64(1), UpperBound: new(math.Inf(1))},
								},
							},
						},
					},
				},
			},
			expectedBody: `# HELP gen_ai_client_token_usage Number of tokens processed.
# TYPE gen_ai_client_token_usage histogram
gen_ai_client_token_usage_bucket{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",gen_ai_token_type="input",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version="",le="+Inf"} 1
gen_ai_client_token_usage_sum{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",gen_ai_token_type="input",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version=""} 44
gen_ai_client_token_usage_count{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",gen_ai_token_type="input",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version=""} 1
gen_ai_client_token_usage_bucket{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",gen_ai_token_type="output",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version="",le="+Inf"} 1
gen_ai_client_token_usage_sum{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",gen_ai_token_type="output",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version=""} 14
gen_ai_client_token_usage_count{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",gen_ai_token_type="output",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version=""} 1
# HELP gen_ai_server_request_duration_seconds Generative AI server request duration such as time-to-last byte or last output token.
# TYPE gen_ai_server_request_duration_seconds histogram
gen_ai_server_request_duration_seconds_bucket{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version="",le="+Inf"} 1
gen_ai_server_request_duration_seconds_sum{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version=""} 10.808095311
gen_ai_server_request_duration_seconds_count{gen_ai_operation_name="chat",gen_ai_provider_name="openai",gen_ai_request_model="qwen2.5:0.5b",gen_ai_response_model="qwen2.5:0.5b",otel_scope_name="envoyproxy/ai-gateway",otel_scope_schema_url="",otel_scope_version=""} 1
`,
		},
		{
			name:           "no metrics - no requests made yet",
			metricFamilies: []*prometheusmodel.MetricFamily{},
			expectedBody:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lis, err := listen(t.Context(), t.Name(), "tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer lis.Close() //nolint:errcheck

			mockHealthClient := &mockHealthClient{
				checkResp: &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING},
			}
			mockRegistry := &mockPrometheusGatherer{metricFamilies: tt.metricFamilies}

			s := startAdminServer(lis, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})), mockRegistry, mockHealthClient)
			defer s.Shutdown(context.Background()) //nolint:errcheck

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			s.Handler.ServeHTTP(rr, req)

			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, tt.expectedBody, rr.Body.String())
		})
	}
}

func TestStartAdminServer_Health(t *testing.T) {
	tests := []struct {
		name               string
		healthClient       *mockHealthClient
		expectedStatusCode int
		expectedBody       string
	}{
		{
			name: "healthy - ExternalProcessorServer is serving",
			healthClient: &mockHealthClient{
				checkResp: &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING},
			},
			expectedStatusCode: http.StatusOK,
			expectedBody:       "OK\n",
		},
		{
			name: "unhealthy - ExternalProcessorServer not serving",
			healthClient: &mockHealthClient{
				checkResp: &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING},
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "unhealthy status: NOT_SERVING\n",
		},
		{
			name: "error - ExternalProcessorServer check RPC failed",
			healthClient: &mockHealthClient{
				checkErr: fmt.Errorf("connection refused"),
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "health check RPC failed: connection refused\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lis, err := listen(t.Context(), t.Name(), "tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer lis.Close() //nolint:errcheck

			mockRegistry := &mockPrometheusGatherer{metricFamilies: []*prometheusmodel.MetricFamily{}}
			s := startAdminServer(lis, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})), mockRegistry, tt.healthClient)
			defer s.Shutdown(context.Background()) //nolint:errcheck

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			s.Handler.ServeHTTP(rr, req)

			require.Equal(t, tt.expectedStatusCode, rr.Code)
			require.Equal(t, tt.expectedBody, rr.Body.String())
		})
	}
}

type mockPrometheusGatherer struct {
	metricFamilies []*prometheusmodel.MetricFamily
}

func (m *mockPrometheusGatherer) Gather() ([]*prometheusmodel.MetricFamily, error) {
	return m.metricFamilies, nil
}

type mockHealthClient struct {
	checkResp *grpc_health_v1.HealthCheckResponse
	checkErr  error
}

func (m *mockHealthClient) Check(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (*grpc_health_v1.HealthCheckResponse, error) {
	if m.checkErr != nil {
		return nil, m.checkErr
	}
	return m.checkResp, nil
}

func (m *mockHealthClient) List(context.Context, *grpc_health_v1.HealthListRequest, ...grpc.CallOption) (*grpc_health_v1.HealthListResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockHealthClient) Watch(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[grpc_health_v1.HealthCheckResponse], error) {
	return nil, fmt.Errorf("not implemented")
}

func TestNewGrpcClient(t *testing.T) {
	tests := []struct {
		name          string
		setupListener func(t *testing.T) net.Addr
		opts          []grpc.DialOption
		expectedError string
	}{
		{
			name: "tcp listener",
			setupListener: func(t *testing.T) net.Addr {
				lis, err := listen(t.Context(), t.Name(), "tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { lis.Close() })
				return lis.Addr()
			},
		},
		{
			name: "unix listener",
			setupListener: func(t *testing.T) net.Addr {
				unixPath := t.TempDir() + "/test.sock"
				lis, err := listen(t.Context(), t.Name(), "unix", unixPath)
				require.NoError(t, err)
				t.Cleanup(func() { lis.Close() })
				return lis.Addr()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := tt.setupListener(t)

			conn, err := newGrpcClient(addr, tt.opts...)

			if tt.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedError)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, conn)
			defer conn.Close()
		})
	}
}
