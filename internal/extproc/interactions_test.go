// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"log/slog"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type (
	interactionsProcessorRouterFilter   = routerProcessor[gcp.InteractionsRequest, struct{}, struct{}, endpointspec.InteractionsEndpointSpec]
	interactionsProcessorUpstreamFilter = upstreamProcessor[gcp.InteractionsRequest, struct{}, struct{}, endpointspec.InteractionsEndpointSpec]
)

// gcpPathAuthHandler mimics the GCP auth handler: it prefixes ":path" with the project and
// location, sets Authorization, and records the body it was given.
type gcpPathAuthHandler struct{ gotBody []byte }

// Do implements [filterapi.BackendAuthHandler.Do].
func (h *gcpPathAuthHandler) Do(_ context.Context, requestHeaders map[string]string, body []byte) ([]internalapi.Header, error) {
	h.gotBody = body
	newPath := "/v1/projects/p1/locations/global/" + requestHeaders[":path"]
	return []internalapi.Header{{":path", newPath}, {"Authorization", "Bearer token"}}, nil
}

// TestInteractions_EndToEndRequestPath runs a create request through the router and upstream
// processors built by the factory registered for the Interactions API.
func TestInteractions_EndToEndRequestPath(t *testing.T) {
	factory := NewFactory(&mockMetricsFactory{}, tracingapi.NoopTracer[gcp.InteractionsRequest, struct{}, struct{}]{}, endpointspec.InteractionsEndpointSpec{})
	config := &filterapi.RuntimeConfig{}
	newRouter := func(t *testing.T) *interactionsProcessorRouterFilter {
		proc, err := factory(config, map[string]string{":method": "POST", ":path": "/gemini/v1beta/interactions"}, slog.Default(), false, false)
		require.NoError(t, err)
		return proc.(*interactionsProcessorRouterFilter)
	}

	t.Run("store=false is added and sent upstream", func(t *testing.T) {
		router := newRouter(t)
		resp, err := router.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: []byte(`{"agent":"antigravity-preview-09-2026","input":"hi"}`), EndOfStream: true})
		require.NoError(t, err)
		model, ok := func() (string, bool) {
			for _, h := range resp.GetRequestBody().GetResponse().GetHeaderMutation().GetSetHeaders() {
				if h.Header.Key == internalapi.ModelNameHeaderKeyDefault {
					return string(h.Header.RawValue), true
				}
			}
			return "", false
		}()
		require.True(t, ok)
		require.Equal(t, "antigravity-preview-09-2026", model, "routed by the agent")

		proc, err := factory(config, map[string]string{":method": "POST", ":path": "/gemini/v1beta/interactions"}, slog.Default(), true, false)
		require.NoError(t, err)
		upstream := proc.(*interactionsProcessorUpstreamFilter)
		handler := &gcpPathAuthHandler{}
		require.NoError(t, upstream.SetBackend(t.Context(), &filterapi.RuntimeBackend{
			Backend: &filterapi.Backend{Name: "gcp/vertex/route/r/rule/0/ref/0", Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaGCPVertexAI}},
			Handler: handler,
		}, "default/r", router))

		resp, err = upstream.ProcessRequestHeaders(t.Context(), nil)
		require.NoError(t, err)
		common := resp.GetRequestHeaders().GetResponse()
		require.Equal(t, extprocv3.CommonResponse_CONTINUE_AND_REPLACE, common.Status)
		body := common.GetBodyMutation().GetBody()
		require.Equal(t, gjson.False, gjson.GetBytes(body, "store").Type, "the upstream body carries store=false")
		require.Equal(t, body, handler.gotBody)
		var lastPath string
		for _, h := range common.GetHeaderMutation().GetSetHeaders() {
			if h.Header.Key == ":path" {
				lastPath = string(h.Header.RawValue)
			}
		}
		require.Equal(t, "/v1/projects/p1/locations/global/interactions", lastPath)
	})

	t.Run("store=true is answered with a 400", func(t *testing.T) {
		router := newRouter(t)
		resp, err := router.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: []byte(`{"agent":"a","store":true}`), EndOfStream: true})
		require.NoError(t, err)
		ir := resp.GetImmediateResponse()
		require.NotNil(t, ir)
		require.Equal(t, 400, int(ir.GetStatus().GetCode()))
		require.Contains(t, string(ir.GetBody()), "'store' must be false")
	})
}
