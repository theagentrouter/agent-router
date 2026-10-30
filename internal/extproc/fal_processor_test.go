// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"log/slog"
	"maps"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type (
	falRouterFilter   = routerProcessor[fal.Request, fal.Response, struct{}, endpointspec.FalEndpointSpec]
	falUpstreamFilter = upstreamProcessor[fal.Request, fal.Response, struct{}, endpointspec.FalEndpointSpec]
)

func Test_falProcessor_RouteAndUpstream(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        []byte
		backend     string
		expModel    string
		expUpstream string
	}{
		{
			name:        "run",
			path:        "/fal/run/fal-ai/flux/dev",
			body:        []byte(`{"prompt":"a cat"}`),
			backend:     "fal-run",
			expModel:    "fal-ai/flux/dev",
			expUpstream: "/fal-ai/flux/dev",
		},
		{
			name:        "queue submit",
			path:        "/fal/queue/fal-ai/flux/schnell",
			body:        []byte(`{"prompt":"a cat"}`),
			backend:     "fal-queue",
			expModel:    "fal-ai/flux/schnell",
			expUpstream: "/fal-ai/flux/schnell",
		},
		{
			name:        "queue status without body",
			path:        "/fal/queue/fal-ai/flux/requests/7f1c/status?logs=1",
			backend:     "fal-queue",
			expModel:    "fal-ai/flux",
			expUpstream: "/fal-ai/flux/requests/7f1c/status?logs=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := NewFactory(&mockMetricsFactory{}, tracingapi.NoopFalTracer{}, endpointspec.FalEndpointSpec{Prefix: "/fal", URLScheme: internalapi.FalURLSchemeHTTPS})
			cfg := &filterapi.RuntimeConfig{}

			routerHeaders := map[string]string{":path": tt.path, internalapi.ModelNameHeaderKeyDefault: "spoofed"}
			rp, err := factory(cfg, routerHeaders, slog.Default(), false, false)
			require.NoError(t, err)
			router := rp.(*falRouterFilter)

			resp, err := router.ProcessRequestHeaders(t.Context(), &corev3.HeaderMap{})
			require.NoError(t, err)
			require.True(t, resp.GetRequestHeaders().GetResponse().GetClearRouteCache())
			require.Equal(t, tt.expModel, routerHeaders[internalapi.ModelNameHeaderKeyDefault])
			if tt.body != nil {
				resp, err = router.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: tt.body, EndOfStream: true})
				require.NoError(t, err)
				require.Equal(t, tt.expModel, string(resp.GetRequestBody().GetResponse().GetHeaderMutation().GetSetHeaders()[0].Header.RawValue))
			}

			up, err := factory(cfg, maps.Clone(routerHeaders), slog.Default(), true, false)
			require.NoError(t, err)
			upstream := up.(*falUpstreamFilter)
			handler, err := backendauth.NewHandler(t.Context(), &filterapi.BackendAuth{FalAPIKey: &filterapi.FalAPIKeyAuth{Key: "fal-secret"}})
			require.NoError(t, err)
			require.NoError(t, upstream.SetBackend(t.Context(), &filterapi.RuntimeBackend{
				Backend: &filterapi.Backend{Name: tt.backend, Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaFal}},
				Handler: handler,
			}, "fal-route", router))

			resp, err = upstream.ProcessRequestHeaders(t.Context(), &corev3.HeaderMap{})
			require.NoError(t, err)
			common := resp.GetRequestHeaders().GetResponse()
			require.Equal(t, extprocv3.CommonResponse_CONTINUE, common.GetStatus())
			require.Nil(t, common.GetBodyMutation())
			set := map[string]string{}
			for _, h := range common.GetHeaderMutation().GetSetHeaders() {
				set[h.Header.Key] = string(h.Header.RawValue)
			}
			require.Equal(t, tt.expUpstream, set[":path"])
			require.Equal(t, "Key fal-secret", set["Authorization"])

			mm := upstream.metrics.(*mockMetrics)
			_, err = upstream.ProcessResponseHeaders(t.Context(), &corev3.HeaderMap{Headers: []*corev3.HeaderValue{{Key: ":status", RawValue: []byte("200")}}})
			require.NoError(t, err)
			resp, err = upstream.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: []byte(`{"status":"COMPLETED"}`), EndOfStream: true})
			require.NoError(t, err)
			require.Nil(t, resp.GetResponseBody().GetResponse().GetBodyMutation())
			mm.RequireSelectedBackend(t, tt.backend)
			mm.RequireSelectedModel(t, tt.expModel, tt.expModel, tt.expModel)
			mm.RequireRequestSuccess(t)
		})
	}
}

func Test_falProcessor_Response(t *testing.T) {
	const falQueueSubmit = `{"request_id":"r","response_url":"https://queue.fal.run/fal-ai/flux/requests/r",` +
		`"status_url":"https://queue.fal.run/fal-ai/flux/requests/r/status",` +
		`"cancel_url":"https://queue.fal.run/fal-ai/flux/requests/r/cancel"}`
	tests := []struct {
		name        string
		path        string
		body        []byte
		backend     string
		respHeaders map[string]string
		chunks      []string
		expContains string
		expCost     float64
	}{
		{
			name:        "queue submit urls point at the gateway",
			path:        "/fal/queue/fal-ai/flux/schnell",
			body:        []byte(`{"prompt":"a cat"}`),
			backend:     "fal-queue",
			respHeaders: map[string]string{":status": "200"},
			chunks:      []string{falQueueSubmit},
			expContains: "https://gw.example/fal/queue/fal-ai/flux/requests/r",
		},
		{
			name:        "billable units cost recorded in dynamic metadata",
			path:        "/fal/run/fal-ai/flux/dev",
			body:        []byte(`{"prompt":"a cat"}`),
			backend:     "fal-run",
			respHeaders: map[string]string{":status": "200", "x-fal-billable-units": "2"},
			chunks:      []string{`{"images":[{"url":"https://v3.fal.media/files/a.png"}]}`},
			expCost:     2000,
		},
		{
			name:        "streamed status rewrites urls and charges no units",
			path:        "/fal/queue/fal-ai/flux/requests/r/status/stream",
			backend:     "fal-queue",
			respHeaders: map[string]string{":status": "200", "x-fal-billable-units": "2"},
			chunks: []string{
				"data: {\"status\":\"IN_PROGRESS\",\"status_url\":\"https://queue.fa",
				"l.run/fal-ai/flux/requests/r/status\"}\n\ndata: {\"status\":\"COMPLETED\"}\n\n",
			},
			expContains: "https://gw.example/fal/queue/fal-ai/flux/requests/r/status",
			expCost:     0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prog, err := llmcostcel.NewProgram("uint(billable_units * 1000.0)")
			require.NoError(t, err)
			cfg := &filterapi.RuntimeConfig{RequestCosts: []filterapi.RuntimeRequestCost{{
				CELProg:        prog,
				LLMRequestCost: &filterapi.LLMRequestCost{RouteName: "fal-route", Type: filterapi.LLMRequestCostTypeCEL, MetadataKey: "fal_cost"},
			}}}
			factory := NewFactory(&mockMetricsFactory{}, tracingapi.NoopFalTracer{}, endpointspec.FalEndpointSpec{Prefix: "/fal", URLScheme: internalapi.FalURLSchemeHTTPS})

			routerHeaders := map[string]string{":path": tt.path, ":authority": "gw.example", ":scheme": "https"}
			rp, err := factory(cfg, routerHeaders, slog.Default(), false, false)
			require.NoError(t, err)
			router := rp.(*falRouterFilter)
			_, err = router.ProcessRequestHeaders(t.Context(), &corev3.HeaderMap{})
			require.NoError(t, err)
			if tt.body != nil {
				_, err = router.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: tt.body, EndOfStream: true})
				require.NoError(t, err)
			}

			upstreamHeaders := maps.Clone(routerHeaders)
			upstreamHeaders[":authority"] = "queue.fal.run"
			up, err := factory(cfg, upstreamHeaders, slog.Default(), true, false)
			require.NoError(t, err)
			upstream := up.(*falUpstreamFilter)
			handler, err := backendauth.NewHandler(t.Context(), &filterapi.BackendAuth{FalAPIKey: &filterapi.FalAPIKeyAuth{Key: "fal-secret"}})
			require.NoError(t, err)
			require.NoError(t, upstream.SetBackend(t.Context(), &filterapi.RuntimeBackend{
				Backend: &filterapi.Backend{Name: tt.backend, Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaFal}},
				Handler: handler,
			}, "fal-route", router))
			_, err = upstream.ProcessRequestHeaders(t.Context(), &corev3.HeaderMap{})
			require.NoError(t, err)

			var respHeaders []*corev3.HeaderValue
			for k, v := range tt.respHeaders {
				respHeaders = append(respHeaders, &corev3.HeaderValue{Key: k, RawValue: []byte(v)})
			}
			_, err = upstream.ProcessResponseHeaders(t.Context(), &corev3.HeaderMap{Headers: respHeaders})
			require.NoError(t, err)

			var (
				out []byte
				res *extprocv3.ProcessingResponse
			)
			for i, chunk := range tt.chunks {
				res, err = upstream.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: []byte(chunk), EndOfStream: i == len(tt.chunks)-1})
				require.NoError(t, err)
				if b := res.GetResponseBody().GetResponse().GetBodyMutation().GetBody(); b != nil {
					out = append(out, b...)
				} else {
					out = append(out, chunk...)
				}
			}
			if tt.expContains != "" {
				require.Contains(t, string(out), tt.expContains)
			}
			require.NotContains(t, string(out), "fal.run/")
			cost := res.GetDynamicMetadata().GetFields()[internalapi.AIGatewayFilterMetadataNamespace].
				GetStructValue().GetFields()["fal_cost"].GetNumberValue()
			require.Equal(t, tt.expCost, cost)
		})
	}
}
