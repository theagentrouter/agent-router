// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func requireNewServerWithMockProcessor(t *testing.T) (*Server, *mockProcessor) {
	s, err := NewServer(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})), false)
	require.NoError(t, err)
	require.NotNil(t, s)
	s.config = &filterapi.RuntimeConfig{}

	m := newMockProcessor(s.config, s.logger)
	s.Register("/", func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		return m, nil
	})

	return s, m.(*mockProcessor)
}

func TestServer_LoadConfig(t *testing.T) {
	config := &filterapi.Config{}
	s := &Server{}
	err := s.LoadConfig(t.Context(), config)
	require.NoError(t, err)
	require.NotNil(t, s.config)
}

func TestServer_Check(t *testing.T) {
	s, _ := requireNewServerWithMockProcessor(t)

	res, err := s.Check(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, res.Status)
}

func TestServer_Watch(t *testing.T) {
	s, _ := requireNewServerWithMockProcessor(t)

	err := s.Watch(nil, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "Watch is not implemented")
}

func TestServer_List(t *testing.T) {
	s, _ := requireNewServerWithMockProcessor(t)

	res, err := s.List(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, res.Statuses["extproc"].Status)
}

func TestServer_processMsg(t *testing.T) {
	t.Run("unknown request type", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())
		_, err := s.processMsg(ctx, p, &extprocv3.ProcessingRequest{}, "test-req-id", false)
		require.ErrorContains(t, err, "unknown request type")
	})
	t.Run("request headers", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}}}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}
		p.t = t
		p.expHeaderMap = hm
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{Headers: hm}},
		}
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
	})
	t.Run("request body", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		reqBody := &extprocv3.HttpBody{}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}
		p.t = t
		p.expBody = reqBody
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: reqBody},
		}
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
	})
	t.Run("request headers with immediate response (missing required header)", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}}}
		// Simulate what happens when ProcessRequestHeaders returns an error response
		expResponse := &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extprocv3.ImmediateResponse{
					Status: &typev3.HttpStatus{Code: typev3.StatusCode_BadRequest},
					Body:   []byte(`{"type":"error","error":{"type":"BadRequest","code":"400","message":"missing required header"}}`),
				},
			},
		}
		p.t = t
		p.expHeaderMap = hm
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{Headers: hm}},
		}
		// This should not panic even though response type is ImmediateResponse
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
		// Verify it's actually an ImmediateResponse
		_, ok := resp.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
		require.True(t, ok, "expected ImmediateResponse type")
	})
	t.Run("request body with immediate response (malformed request)", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		reqBody := &extprocv3.HttpBody{}
		// Simulate what happens when ProcessRequestBody returns an error response
		// (e.g., for malformed JSON) - it returns ImmediateResponse instead of RequestBody
		expResponse := &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extprocv3.ImmediateResponse{
					Status: &typev3.HttpStatus{Code: typev3.StatusCode_BadRequest},
					Body:   []byte(`{"type":"error","error":{"type":"BadRequest","code":"400","message":"malformed request"}}`),
				},
			},
		}
		p.t = t
		p.expBody = reqBody
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: reqBody},
		}
		// This should not panic even though response type is ImmediateResponse
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
		// Verify it's actually an ImmediateResponse
		_, ok := resp.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
		require.True(t, ok, "expected ImmediateResponse type")
	})
	t.Run("response headers", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}}}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}
		p.t = t
		p.expHeaderMap = hm
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_ResponseHeaders{ResponseHeaders: &extprocv3.HttpHeaders{Headers: hm}},
		}
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
	})
	t.Run("error response headers", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: ":status", Value: "504"}}}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}
		p.t = t
		p.expHeaderMap = hm
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_ResponseHeaders{ResponseHeaders: &extprocv3.HttpHeaders{Headers: hm}},
		}
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
	})
	t.Run("response body", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)
		ctx := context.WithValue(t.Context(), loggerContextKey, slog.Default())

		reqBody := &extprocv3.HttpBody{}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}
		p.t = t
		p.expBody = reqBody
		p.retProcessingResponse = expResponse
		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: reqBody},
		}
		resp, err := s.processMsg(ctx, p, req, "test-req-id", false)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, expResponse, resp)
	})
}

func TestServer_Process(t *testing.T) {
	t.Run("context done", func(t *testing.T) {
		s, _ := requireNewServerWithMockProcessor(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		ms := &mockExternalProcessingStream{t: t, ctx: ctx}
		err := s.Process(ms)
		require.ErrorContains(t, err, "context canceled")
	})
	t.Run("recv iof", func(t *testing.T) {
		s, _ := requireNewServerWithMockProcessor(t)
		ms := &mockExternalProcessingStream{t: t, retErr: io.EOF, ctx: t.Context()}
		err := s.Process(ms)
		require.NoError(t, err)
	})
	t.Run("recv canceled", func(t *testing.T) {
		s, _ := requireNewServerWithMockProcessor(t)
		ms := &mockExternalProcessingStream{t: t, retErr: status.Error(codes.Canceled, "someerror"), ctx: t.Context()}
		err := s.Process(ms)
		require.NoError(t, err)
	})
	t.Run("recv generic error", func(t *testing.T) {
		s, _ := requireNewServerWithMockProcessor(t)
		ms := &mockExternalProcessingStream{t: t, retErr: errors.New("some error"), ctx: t.Context()}
		err := s.Process(ms)
		require.ErrorContains(t, err, "some error")
	})
	t.Run("upstream filter", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)

		hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: originalPathHeader, Value: "/"}, {Key: internalReqIDHeader, Value: "test-req-id-123"}, {Key: "foo", Value: "bar"}}}
		p.t = t
		p.expHeaderMap = hm
		req := &extprocv3.ProcessingRequest{
			Attributes: map[string]*structpb.Struct{
				"envoy.filters.http.ext_proc": {Fields: map[string]*structpb.Value{"something": {}}},
			},
			Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{Headers: hm}},
		}
		ms := &mockExternalProcessingStream{t: t, ctx: t.Context(), retRecv: req}
		err := s.Process(ms)
		require.ErrorContains(t, err, `missing backend name in attributes at path: xds.upstream_host_metadata.filter_metadata['aigateway.envoy.io']['per_route_rule_backend_name']`)
	})
	t.Run("ok", func(t *testing.T) {
		s, p := requireNewServerWithMockProcessor(t)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: ":path", Value: "/"}, {Key: "foo", Value: "bar"}}}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}
		p.t = t
		p.expHeaderMap = hm
		p.retProcessingResponse = expResponse

		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{Headers: hm}},
		}
		ms := &mockExternalProcessingStream{t: t, ctx: ctx, retRecv: req, expResponseOnSend: expResponse}
		err := s.Process(ms)
		require.ErrorContains(t, err, "context deadline exceeded")
	})
	t.Run("without going through request headers phase", func(t *testing.T) {
		// This is a regression test as in #419.
		s, _ := requireNewServerWithMockProcessor(t)
		s.debugLogEnabled = false // Disable debug log path to avoid redacted header logging interfering with the test.
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}
		req := &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
			ResponseHeaders: &extprocv3.HttpHeaders{Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: ":status", Value: "403"}}}},
		}}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		ms := &mockExternalProcessingStream{t: t, ctx: ctx, retRecv: req, expResponseOnSend: expResponse}
		err := s.Process(ms)
		require.ErrorContains(t, err, "context deadline exceeded")
	})
}

func TestServer_setBackend(t *testing.T) {
	s, _ := requireNewServerWithMockProcessor(t)
	s.config.Backends = map[string]*filterapi.RuntimeBackend{"openai": {Backend: &filterapi.Backend{Name: "openai"}}}
	mockProc := &mockProcessor{}

	for _, isEndpointPicker := range []bool{true, false} {
		t.Run(fmt.Sprintf("isEndpointPicker=%t", isEndpointPicker), func(t *testing.T) {
			attributeKey := internalapi.XDSUpstreamHostMetadataBackendNamePath
			if isEndpointPicker {
				attributeKey = internalapi.XDSClusterMetadataBackendNamePath
			}

			err := s.setBackend(t.Context(), mockProc, "aaaaaaaaaaaa", isEndpointPicker, &extprocv3.ProcessingRequest{
				Attributes: map[string]*structpb.Struct{
					"envoy.filters.http.ext_proc": {Fields: map[string]*structpb.Value{
						attributeKey: {Kind: &structpb.Value_StringValue{
							StringValue: "openai",
						}},
						internalapi.XDSRouteMetadataRouteNamePath: {Kind: &structpb.Value_StringValue{
							StringValue: "route-a",
						}},
					}},
				},
				Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{}},
			})
			// Should be an error as there is no router processor registered for openai backend.
			// The purpose of this test is to verify that the backend name is correctly extracted.
			require.ErrorContains(t, err, `no router processor found, request_id=aaaaaaaaaaaa, backend=openai`)
		})
	}
}

func TestResolveBackendName(t *testing.T) {
	const backendName = "default/openai/route/aigw-run/rule/0/ref/0"

	for _, tc := range []struct {
		name             string
		attributes       map[string]*structpb.Value
		isEndpointPicker bool
		expected         string
		expectedErr      string
	}{
		{
			name: "direct metadata path (upstream host)",
			attributes: map[string]*structpb.Value{
				internalapi.XDSUpstreamHostMetadataBackendNamePath: structpb.NewStringValue(backendName),
			},
			expected: backendName,
		},
		{
			name: "direct metadata path (cluster, endpoint picker)",
			attributes: map[string]*structpb.Value{
				internalapi.XDSClusterMetadataBackendNamePath: structpb.NewStringValue(backendName),
			},
			isEndpointPicker: true,
			expected:         backendName,
		},
		{
			name: "fallback to cluster metadata when upstream host metadata missing",
			attributes: map[string]*structpb.Value{
				internalapi.XDSClusterMetadataBackendNamePath: structpb.NewStringValue(backendName),
			},
			expected: backendName,
		},
		{
			name:        "missing backend name for router",
			attributes:  map[string]*structpb.Value{},
			expectedErr: "rpc error: code = Internal desc = missing backend name in attributes at path: xds.upstream_host_metadata.filter_metadata['aigateway.envoy.io']['per_route_rule_backend_name']",
		},
		{
			name:             "missing backend name for endpoint picker",
			attributes:       map[string]*structpb.Value{},
			isEndpointPicker: true,
			expectedErr:      "rpc error: code = Internal desc = missing backend name in attributes at path: xds.cluster_metadata.filter_metadata['aigateway.envoy.io']['per_route_rule_backend_name']",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := resolveBackendName(
				tc.isEndpointPicker,
				&structpb.Struct{Fields: tc.attributes},
			)
			if tc.expectedErr != "" {
				require.EqualError(t, err, tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, actual)
		})
	}
}

func TestResolveRouteName(t *testing.T) {
	const routeName = "my-route"
	attributes := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			internalapi.XDSRouteMetadataRouteNamePath: structpb.NewStringValue(routeName),
		},
	}
	actual := resolveRouteName(attributes)
	require.Equal(t, routeName, actual)

	actual = resolveRouteName(&structpb.Struct{Fields: map[string]*structpb.Value{}})
	require.Empty(t, actual)
}

func TestSetAWSSigningAttributes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       map[string]string // headers already present (e.g. spoofed by a downstream client)
		attributes *structpb.Struct
		wantHost   string
	}{
		{
			name: "host from metadata",
			attributes: &structpb.Struct{Fields: map[string]*structpb.Value{
				internalapi.XDSUpstreamHostMetadataUpstreamHostPath: structpb.NewStringValue("vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"),
			}},
			wantHost: "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com",
		},
		{
			name:       "missing metadata",
			attributes: &structpb.Struct{Fields: map[string]*structpb.Value{}},
		},
		{
			name: "nil metadata",
		},
		{
			// Client-spoofed header must not survive when xDS supplies no metadata.
			name:       "client-supplied host is cleared when no xDS metadata",
			seed:       map[string]string{internalapi.UpstreamHostHeader: "attacker.example.com"},
			attributes: &structpb.Struct{Fields: map[string]*structpb.Value{}},
		},
		{
			// Trusted xDS metadata must overwrite any client-supplied value.
			name: "xDS metadata overrides client-supplied host",
			seed: map[string]string{internalapi.UpstreamHostHeader: "attacker.example.com"},
			attributes: &structpb.Struct{Fields: map[string]*structpb.Value{
				internalapi.XDSUpstreamHostMetadataUpstreamHostPath: structpb.NewStringValue("vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"),
			}},
			wantHost: "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			maps.Copy(headers, tc.seed)
			setUpstreamHostAttributes(headers, tc.attributes)
			if tc.wantHost == "" {
				require.NotContains(t, headers, internalapi.UpstreamHostHeader)
			} else {
				require.Equal(t, tc.wantHost, headers[internalapi.UpstreamHostHeader])
			}
		})
	}
}

func TestServer_ProcessorSelection(t *testing.T) {
	s, err := NewServer(slog.Default(), false)
	require.NoError(t, err)
	require.NotNil(t, s)

	s.config = &filterapi.RuntimeConfig{}
	s.Register("/one", func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		// Returning nil guarantees that the test will fail if this processor is selected.
		return nil, nil
	})
	s.Register("/two", func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		return &mockProcessor{
			t:                     t,
			expHeaderMap:          &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: ":path", Value: "/two"}, {Key: "x-request-id", Value: "original-req-id"}}},
			retProcessingResponse: &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}},
		}, nil
	})

	t.Run("unknown path", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extprocv3.HttpHeaders{
					Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: ":path", Value: "/unknown"}}},
				},
			},
		}
		expResponse := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extprocv3.ImmediateResponse{
				Status:     &typev3.HttpStatus{Code: typev3.StatusCode_NotFound},
				Body:       []byte("unsupported path: /unknown"),
				GrpcStatus: &extprocv3.GrpcStatus{Status: uint32(codes.NotFound)},
			},
		}}
		ms := &mockExternalProcessingStream{t: t, ctx: ctx, retRecv: req, expResponseOnSend: expResponse}

		err = s.Process(ms)
		require.Equal(t, codes.NotFound, status.Convert(err).Code())
		require.ErrorContains(t, err, "unsupported path: /unknown")
	})

	t.Run("known path", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		req := &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extprocv3.HttpHeaders{
					Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
						{Key: ":path", Value: "/two"},
						{Key: "x-request-id", Value: "original-req-id"},
					}},
				},
			},
		}
		expResponse := &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_RequestHeaders{
				RequestHeaders: &extprocv3.HeadersResponse{
					Response: &extprocv3.CommonResponse{
						HeaderMutation: &extprocv3.HeaderMutation{
							SetHeaders: []*corev3.HeaderValueOption{
								{
									Header: &corev3.HeaderValue{
										Key:      internalReqIDHeader,
										RawValue: []byte("original-req-id-test-internal-req-id"),
									},
								},
							},
						},
					},
				},
			},
		}

		s.uuidFn = func() string {
			return "test-internal-req-id"
		}

		ms := &mockExternalProcessingStream{t: t, ctx: ctx, retRecv: req, expResponseOnSend: expResponse}

		err = s.Process(ms)
		require.ErrorContains(t, err, "context deadline exceeded")
	})
}

func Test_filterSensitiveHeadersForLogging(t *testing.T) {
	hm := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "foo", Value: "bar"}, {Key: "dog", RawValue: []byte("cat")}, {Key: "authorization", Value: "sensitive"},
		},
	}
	filtered := filterSensitiveHeadersForLogging(hm, []string{"authorization"})
	require.Equal(t, []slog.Attr{
		slog.String("foo", "bar"),
		slog.String("dog", "cat"),
		slog.String("authorization", "[REDACTED]"),
	}, filtered)
	// Check original one should not be modified.
	require.Len(t, hm.Headers, 3)
	require.Contains(t, hm.Headers, &corev3.HeaderValue{Key: "foo", Value: "bar"})
	require.Contains(t, hm.Headers, &corev3.HeaderValue{Key: "dog", RawValue: []byte("cat")})
	require.Contains(t, hm.Headers, &corev3.HeaderValue{Key: "authorization", Value: "sensitive"})
}

func Test_isSensitiveHeader(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"authorization", true},
		{"Authorization", true},
		{"x-api-key", true},
		// x-aigw- headers carry per-request credential overrides.
		{"x-aigw-api-key", true},
		{"x-aigw-aws-secret-access-key", true},
		// The prefix is configurable, so keeping an existing injector's names must not lose
		// redaction. Match on the credential-part suffix.
		{"x-aws-secret-access-key", true},
		{"x-aws-access-key-id", true},
		{"x-aws-session-token", true},
		{"X-Tenant-AWS-Secret-Access-Key", true},
		{"content-type", false},
		{"x-request-id", false},
		{"user-agent", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			require.Equal(t, tc.want, isSensitiveHeader(tc.key, sensitiveHeaderKeys))
		})
	}
}

func Test_filterRequestBodyResponseHeaders(t *testing.T) {
	buf := internaltesting.CaptureOutput("test")[0]
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	resp := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: &extprocv3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{
								Key:      ":path",
								RawValue: []byte("/model/some-random-model/converse"),
							}},
							{Header: &corev3.HeaderValue{
								Key:      "Authorization",
								RawValue: []byte("sensitive"),
							}},
						},
						RemoveHeaders: []string{"x-envoy-original-path"},
					},
					BodyMutation: &extprocv3.BodyMutation{},
				},
			},
		},
	}
	rb := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
	filtered := redactRequestBodyResponse(rb, logger, []string{"authorization"}, false)
	require.NotNil(t, filtered)
	filteredMutation := filtered.RequestBody.Response.GetHeaderMutation()
	require.Equal(t, []string{"x-envoy-original-path"}, filteredMutation.GetRemoveHeaders())
	require.Equal(t, []*corev3.HeaderValueOption{
		{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/model/some-random-model/converse")}},
		{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("[REDACTED]")}},
	}, filteredMutation.GetSetHeaders())
	// Original one should not be modified, otherwise it will be an unexpected behavior.
	originalMutation := resp.Response.(*extprocv3.ProcessingResponse_RequestBody).RequestBody.Response.GetHeaderMutation()
	require.Equal(t, []string{"x-envoy-original-path"}, originalMutation.GetRemoveHeaders())
	require.Equal(t, []*corev3.HeaderValueOption{
		{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/model/some-random-model/converse")}},
		{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("sensitive")}},
	}, originalMutation.GetSetHeaders())
	require.Contains(t, buf.String(), "filtering sensitive header")

	t.Run("handle nil response", func(t *testing.T) {
		filtered := redactRequestBodyResponse(nil, logger, []string{"authorization"}, false)
		require.NotNil(t, filtered)
		require.Equal(t, &extprocv3.ProcessingResponse_RequestBody{}, filtered)
	})
}

func Test_redactProcessingResponseRequestHeaders(t *testing.T) {
	buf := internaltesting.CaptureOutput("test")[0]
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	resp := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: &extprocv3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{
								Key:      ":path",
								RawValue: []byte("/v1/chat/completions"),
							}},
							{Header: &corev3.HeaderValue{
								Key:      "Authorization",
								RawValue: []byte("Bearer secret-backend-key"),
							}},
						},
						RemoveHeaders: []string{"x-envoy-original-path"},
					},
					BodyMutation: &extprocv3.BodyMutation{
						Mutation: &extprocv3.BodyMutation_Body{
							Body: []byte(`{"prompt":"secret prompt"}`),
						},
					},
					ClearRouteCache: true,
				},
			},
		},
	}
	rh := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders)

	t.Run("redactBody=false redacts headers only", func(t *testing.T) {
		filtered := redactProcessingResponseRequestHeaders(rh, logger, []string{"authorization"}, false)
		require.NotNil(t, filtered)
		filteredMutation := filtered.RequestHeaders.Response.GetHeaderMutation()
		require.Equal(t, []string{"x-envoy-original-path"}, filteredMutation.GetRemoveHeaders())
		// The injected backend credential must be redacted even with redactBody off.
		require.Equal(t, []*corev3.HeaderValueOption{
			{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/v1/chat/completions")}},
			{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("[REDACTED]")}},
		}, filteredMutation.GetSetHeaders())
		require.NotContains(t, buf.String(), "secret-backend-key")

		// Body stays raw when redactBody is false.
		require.JSONEq(t, `{"prompt":"secret prompt"}`, string(filtered.RequestHeaders.Response.GetBodyMutation().GetBody()))
		// ClearRouteCache is preserved.
		require.True(t, filtered.RequestHeaders.Response.GetClearRouteCache())

		// Original response is not modified.
		originalMutation := rh.RequestHeaders.Response.GetHeaderMutation()
		require.Equal(t, []*corev3.HeaderValueOption{
			{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/v1/chat/completions")}},
			{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("Bearer secret-backend-key")}},
		}, originalMutation.GetSetHeaders())
	})

	t.Run("redactBody=true redacts headers and body", func(t *testing.T) {
		filtered := redactProcessingResponseRequestHeaders(rh, logger, []string{"authorization"}, true)
		require.NotNil(t, filtered)
		filteredMutation := filtered.RequestHeaders.Response.GetHeaderMutation()
		require.Equal(t, []*corev3.HeaderValueOption{
			{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/v1/chat/completions")}},
			{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("[REDACTED]")}},
		}, filteredMutation.GetSetHeaders())

		bodyStr := string(filtered.RequestHeaders.Response.GetBodyMutation().GetBody())
		require.Contains(t, bodyStr, "[REDACTED LENGTH=")
		require.NotContains(t, bodyStr, "secret prompt")

		// Original body still unmodified.
		require.JSONEq(t, `{"prompt":"secret prompt"}`, string(rh.RequestHeaders.Response.GetBodyMutation().GetBody()))
	})

	t.Run("handle nil response", func(t *testing.T) {
		filtered := redactProcessingResponseRequestHeaders(nil, logger, []string{"authorization"}, false)
		require.NotNil(t, filtered)
		require.Equal(t, &extprocv3.ProcessingResponse_RequestHeaders{}, filtered)
	})
}

// TestServer_processMsg_RedactsCredentialMutation asserts that a backend
// credential injected via the request-headers header mutation is redacted in the
// "request headers processed" debug log even when enableRedaction is false —
// header/credential redaction must be independent of the body-content redaction
// flag.
func TestServer_processMsg_RedactsCredentialMutation(t *testing.T) {
	buf := internaltesting.CaptureOutput("test")[0]
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	s, err := NewServer(logger, false) // enableRedaction=false, debug on
	require.NoError(t, err)
	s.config = &filterapi.RuntimeConfig{}
	m := newMockProcessor(s.config, s.logger).(*mockProcessor)
	s.Register("/", func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		return m, nil
	})

	hm := &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}}}
	expResponse := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: &extprocv3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{{
							Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("Bearer secret-backend-key")},
						}},
					},
				},
			},
		},
	}
	m.t = t
	m.expHeaderMap = hm
	m.retProcessingResponse = expResponse

	ctx := context.WithValue(t.Context(), loggerContextKey, logger)
	req := &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{Headers: hm}},
	}
	// Upstream filter path is where the backend Authorization is injected.
	resp, err := s.processMsg(ctx, m, req, "test-req-id", true)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// The actual response sent to Envoy is unchanged.
	require.Equal(t, []byte("Bearer secret-backend-key"),
		resp.GetRequestHeaders().GetResponse().GetHeaderMutation().GetSetHeaders()[0].Header.RawValue)
	// But the debug log must not leak the credential.
	require.Contains(t, buf.String(), "[REDACTED]")
	require.NotContains(t, buf.String(), "secret-backend-key")
}

func Test_redactRequestBodyResponseFull(t *testing.T) {
	buf := internaltesting.CaptureOutput("test")[0]
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	resp := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: &extprocv3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{
								Key:      ":path",
								RawValue: []byte("/model/some-random-model/converse"),
							}},
							{Header: &corev3.HeaderValue{
								Key:      "Authorization",
								RawValue: []byte("sensitive"),
							}},
						},
						RemoveHeaders: []string{"x-envoy-original-path"},
					},
					BodyMutation: &extprocv3.BodyMutation{
						Mutation: &extprocv3.BodyMutation_Body{
							Body: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"secret prompt"}]}`),
						},
					},
				},
			},
		},
	}
	rb := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
	filtered := redactRequestBodyResponse(rb, logger, []string{"authorization"}, true)
	require.NotNil(t, filtered)

	// Verify headers are redacted
	filteredMutation := filtered.RequestBody.Response.GetHeaderMutation()
	require.Equal(t, []string{"x-envoy-original-path"}, filteredMutation.GetRemoveHeaders())
	require.Equal(t, []*corev3.HeaderValueOption{
		{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/model/some-random-model/converse")}},
		{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("[REDACTED]")}},
	}, filteredMutation.GetSetHeaders())

	// Verify body is redacted
	filteredBodyMutation := filtered.RequestBody.Response.GetBodyMutation()
	require.NotNil(t, filteredBodyMutation)
	bodyBytes := filteredBodyMutation.GetBody()
	require.NotNil(t, bodyBytes)
	bodyStr := string(bodyBytes)
	require.Contains(t, bodyStr, "[REDACTED LENGTH=")
	require.Contains(t, bodyStr, "HASH=")
	require.NotContains(t, bodyStr, "secret prompt") // Original content should not be present

	// Original one should not be modified
	originalMutation := resp.Response.(*extprocv3.ProcessingResponse_RequestBody).RequestBody.Response.GetHeaderMutation()
	require.Equal(t, []*corev3.HeaderValueOption{
		{Header: &corev3.HeaderValue{Key: ":path", RawValue: []byte("/model/some-random-model/converse")}},
		{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("sensitive")}},
	}, originalMutation.GetSetHeaders())
	originalBodyMutation := resp.Response.(*extprocv3.ProcessingResponse_RequestBody).RequestBody.Response.GetBodyMutation()
	require.JSONEq(t, `{"model":"gpt-4","messages":[{"role":"user","content":"secret prompt"}]}`, string(originalBodyMutation.GetBody()))

	t.Run("handle nil response", func(t *testing.T) {
		filtered := redactRequestBodyResponse(nil, logger, []string{"authorization"}, true)
		require.NotNil(t, filtered)
		require.Equal(t, &extprocv3.ProcessingResponse_RequestBody{}, filtered)
	})
}

func Test_redactResponseBodyResponseFull(t *testing.T) {
	buf := internaltesting.CaptureOutput("test")[0]
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	resp := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: &extprocv3.HeaderMutation{
						SetHeaders: []*corev3.HeaderValueOption{
							{Header: &corev3.HeaderValue{
								Key:      "content-type",
								RawValue: []byte("application/json"),
							}},
							{Header: &corev3.HeaderValue{
								Key:      "x-api-key",
								RawValue: []byte("sk-secret123"),
							}},
							{Header: &corev3.HeaderValue{
								Key:      "Authorization",
								RawValue: []byte("Bearer token-abc"),
							}},
						},
						RemoveHeaders: []string{"x-envoy-upstream-service-time"},
					},
					BodyMutation: &extprocv3.BodyMutation{
						Mutation: &extprocv3.BodyMutation_Body{
							Body: []byte(`{"response": "AI generated content"}`),
						},
					},
				},
			},
		},
	}
	rb := resp.Response.(*extprocv3.ProcessingResponse_ResponseBody)
	filtered := redactResponseBodyResponseFull(rb, logger, []string{"authorization", "x-api-key"})
	require.NotNil(t, filtered)
	filteredMutation := filtered.ResponseBody.Response.GetHeaderMutation()
	require.Equal(t, []string{"x-envoy-upstream-service-time"}, filteredMutation.GetRemoveHeaders())
	require.Equal(t, []*corev3.HeaderValueOption{
		{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")}},
		{Header: &corev3.HeaderValue{Key: "x-api-key", RawValue: []byte("[REDACTED]")}},
		{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("[REDACTED]")}},
	}, filteredMutation.GetSetHeaders())
	// Original one should not be modified, otherwise it will be an unexpected behavior.
	originalMutation := resp.Response.(*extprocv3.ProcessingResponse_ResponseBody).ResponseBody.Response.GetHeaderMutation()
	require.Equal(t, []string{"x-envoy-upstream-service-time"}, originalMutation.GetRemoveHeaders())
	require.Equal(t, []*corev3.HeaderValueOption{
		{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")}},
		{Header: &corev3.HeaderValue{Key: "x-api-key", RawValue: []byte("sk-secret123")}},
		{Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte("Bearer token-abc")}},
	}, originalMutation.GetSetHeaders())
	require.Contains(t, buf.String(), "filtering sensitive header")

	// Verify body content is redacted
	filteredBodyMutation := filtered.ResponseBody.Response.GetBodyMutation()
	require.NotNil(t, filteredBodyMutation)
	bodyBytes := filteredBodyMutation.GetBody()
	require.NotNil(t, bodyBytes)
	bodyStr := string(bodyBytes)
	require.Contains(t, bodyStr, "[REDACTED LENGTH=")
	require.Contains(t, bodyStr, "HASH=")
	require.NotContains(t, bodyStr, "AI generated content") // Original content should not be present
	// Original body should not be modified
	originalBodyMutation := resp.Response.(*extprocv3.ProcessingResponse_ResponseBody).ResponseBody.Response.GetBodyMutation()
	require.JSONEq(t, `{"response": "AI generated content"}`, string(originalBodyMutation.GetBody()))

	t.Run("handle nil response", func(t *testing.T) {
		filtered := redactResponseBodyResponseFull(nil, logger, []string{"authorization"})
		require.NotNil(t, filtered)
		require.Equal(t, &extprocv3.ProcessingResponse_ResponseBody{}, filtered)
	})

	t.Run("handle nil body mutation", func(t *testing.T) {
		respWithoutBody := &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseBody{
				ResponseBody: &extprocv3.BodyResponse{
					Response: &extprocv3.CommonResponse{
						HeaderMutation: &extprocv3.HeaderMutation{
							SetHeaders: []*corev3.HeaderValueOption{
								{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("text/plain")}},
							},
						},
						BodyMutation: nil,
					},
				},
			},
		}
		rb := respWithoutBody.Response.(*extprocv3.ProcessingResponse_ResponseBody)
		filtered := redactResponseBodyResponseFull(rb, logger, []string{})
		require.NotNil(t, filtered)
		require.Nil(t, filtered.ResponseBody.Response.GetBodyMutation())
	})

	t.Run("handle empty body", func(t *testing.T) {
		respWithEmptyBody := &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseBody{
				ResponseBody: &extprocv3.BodyResponse{
					Response: &extprocv3.CommonResponse{
						BodyMutation: &extprocv3.BodyMutation{
							Mutation: &extprocv3.BodyMutation_Body{
								Body: []byte{},
							},
						},
					},
				},
			},
		}
		rb := respWithEmptyBody.Response.(*extprocv3.ProcessingResponse_ResponseBody)
		filtered := redactResponseBodyResponseFull(rb, logger, []string{})
		require.NotNil(t, filtered)
		bodyMutation := filtered.ResponseBody.Response.GetBodyMutation()
		require.NotNil(t, bodyMutation)
		// Empty body should not be redacted
		require.Equal(t, []byte{}, bodyMutation.GetBody())
	})
}

func Test_redactBodyMutation(t *testing.T) {
	t.Run("redact body content with hash", func(t *testing.T) {
		originalBody := []byte(`{"choices": [{"message": {"content": "This is sensitive AI generated content with personal information"}}]}`)
		bodyMutation := &extprocv3.BodyMutation{
			Mutation: &extprocv3.BodyMutation_Body{
				Body: originalBody,
			},
		}

		redacted := redactBodyMutation(bodyMutation)
		require.NotNil(t, redacted)
		redactedBody := redacted.GetBody()
		require.NotNil(t, redactedBody)

		redactedStr := string(redactedBody)
		require.Contains(t, redactedStr, "[REDACTED LENGTH=")
		require.Contains(t, redactedStr, "HASH=")
		require.NotContains(t, redactedStr, "sensitive AI generated content")
		require.NotContains(t, redactedStr, "personal information")

		// Verify it contains the expected length
		expectedLen := len(originalBody)
		require.Contains(t, redactedStr, fmt.Sprintf("LENGTH=%d", expectedLen))

		// Verify original is not modified
		require.Equal(t, originalBody, bodyMutation.GetBody())
	})

	t.Run("handle nil body mutation", func(t *testing.T) {
		redacted := redactBodyMutation(nil)
		require.Nil(t, redacted)
	})

	t.Run("handle empty body", func(t *testing.T) {
		bodyMutation := &extprocv3.BodyMutation{
			Mutation: &extprocv3.BodyMutation_Body{
				Body: []byte{},
			},
		}

		redacted := redactBodyMutation(bodyMutation)
		require.NotNil(t, redacted)
		// Empty body should be returned as-is, not redacted
		require.Equal(t, []byte{}, redacted.GetBody())
	})

	t.Run("handle ClearBody mutation", func(t *testing.T) {
		bodyMutation := &extprocv3.BodyMutation{
			Mutation: &extprocv3.BodyMutation_ClearBody{
				ClearBody: true,
			},
		}

		redacted := redactBodyMutation(bodyMutation)
		require.NotNil(t, redacted)
		// ClearBody should be returned as-is
		require.Equal(t, bodyMutation, redacted)
	})

	t.Run("verify hash consistency", func(t *testing.T) {
		originalBody := []byte("test content")
		bodyMutation := &extprocv3.BodyMutation{
			Mutation: &extprocv3.BodyMutation_Body{
				Body: originalBody,
			},
		}

		// Redact twice and verify same hash
		redacted1 := redactBodyMutation(bodyMutation)
		redacted2 := redactBodyMutation(bodyMutation)

		require.Equal(t, redacted1.GetBody(), redacted2.GetBody())
	})
}

func Test_headersToMap(t *testing.T) {
	hm := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "foo", Value: "bar"},
			{Key: "dog", RawValue: []byte("cat")},
		},
	}
	m := headersToMap(hm)
	require.Equal(t, map[string]string{"foo": "bar", "dog": "cat"}, m)
}

func TestServer_ProcessorForPath_QueryParameterStripping(t *testing.T) {
	s, err := NewServer(slog.Default(), false)
	require.NoError(t, err)
	require.NotNil(t, s)

	s.config = &filterapi.RuntimeConfig{}

	// Register processors for different base paths.
	mockProc := &mockProcessor{}
	s.Register("/v1/messages", func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		return mockProc, nil
	})
	s.Register("/anthropic/v1/messages", func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		return mockProc, nil
	})

	tests := []struct {
		name           string
		requestHeaders map[string]string
		isUpstream     bool
		expectSuccess  bool
		expectedError  string
	}{
		{
			name: "path_without_query_params",
			requestHeaders: map[string]string{
				":path": "/v1/messages",
			},
			isUpstream:    false,
			expectSuccess: true,
		},
		{
			name: "path_with_beta_query_param",
			requestHeaders: map[string]string{
				":path": "/v1/messages?beta=true",
			},
			isUpstream:    false,
			expectSuccess: true,
		},
		{
			name: "path_with_multiple_query_params",
			requestHeaders: map[string]string{
				":path": "/v1/messages?beta=true&stream=false&version=2",
			},
			isUpstream:    false,
			expectSuccess: true,
		},
		{
			name: "anthropic_path_with_beta_param",
			requestHeaders: map[string]string{
				":path": "/anthropic/v1/messages?beta=true",
			},
			isUpstream:    false,
			expectSuccess: true,
		},
		{
			name: "unknown_path_without_query_params",
			requestHeaders: map[string]string{
				":path": "/unknown/path",
			},
			isUpstream:    false,
			expectSuccess: false,
			expectedError: "no processor registered for the given path: /unknown/path",
		},
		{
			name: "unknown_path_with_query_params",
			requestHeaders: map[string]string{
				":path": "/unknown/path?param=value",
			},
			isUpstream:    false,
			expectSuccess: false,
			expectedError: "no processor registered for the given path: /unknown/path",
		},
		{
			name: "upstream_filter_with_original_path_header",
			requestHeaders: map[string]string{
				originalPathHeader: "/v1/messages?beta=true&other=param",
			},
			isUpstream:    true,
			expectSuccess: true,
		},
		{
			name: "empty_path",
			requestHeaders: map[string]string{
				":path": "",
			},
			isUpstream:    false,
			expectSuccess: false,
			expectedError: "no processor registered for the given path: ",
		},
		{
			name: "path_with_only_query_params",
			requestHeaders: map[string]string{
				":path": "?beta=true",
			},
			isUpstream:    false,
			expectSuccess: false,
			expectedError: "no processor registered for the given path: ",
		},
		{
			name: "path_with_fragment_and_query",
			requestHeaders: map[string]string{
				":path": "/v1/messages?beta=true#fragment",
			},
			isUpstream:    false,
			expectSuccess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor, err := s.processorForPath(tt.requestHeaders, tt.isUpstream, slog.Default())

			if tt.expectSuccess {
				require.NoError(t, err)
				require.NotNil(t, processor)
				require.Equal(t, mockProc, processor)
			} else {
				require.Error(t, err)
				require.Nil(t, processor)
				if tt.expectedError != "" {
					require.Contains(t, err.Error(), tt.expectedError)
				}
			}
		})
	}
}
