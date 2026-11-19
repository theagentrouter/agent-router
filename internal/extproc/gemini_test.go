// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"log/slog"
	"maps"
	"net/http"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const geminiTestPrefix = "/gateway/gemini/v1beta/models"

func newTestGeminiFactory() ProcessorFactory {
	return NewGeminiGenerateContentFactory(geminiTestPrefix,
		metrics.NewMetricsFactory(noop.NewMeterProvider().Meter("test"), nil, metrics.GenAIOperationGenerateContent),
		tracingapi.NoopTracing{}.GeminiGenerateContentTracer())
}

func TestGeminiGenerateContentFactory(t *testing.T) {
	factory := newTestGeminiFactory()
	config := &filterapi.RuntimeConfig{}
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, geminiTestPrefix + "/alias:generateContent"},
		{http.MethodPost, geminiTestPrefix + "/alias:countTokens"},
	} {
		processor, err := factory(config, map[string]string{":method": tc.method, ":path": tc.path}, slog.Default(), false, false)
		require.ErrorIs(t, err, errNoProcessor)
		require.Nil(t, processor)
	}

	path := geminiTestPrefix + "/alias:streamGenerateContent?alt=sse"
	headers := map[string]string{":method": http.MethodPost, ":path": path + "&key=incoming-key"}
	router, err := factory(config, headers, slog.Default(), false, false)
	require.NoError(t, err)
	body, err := router.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: []byte("{}")})
	require.NoError(t, err)
	require.True(t, body.GetRequestBody().GetResponse().GetClearRouteCache())
	require.Equal(t, "alias", headers[internalapi.ModelNameHeaderKeyDefault])
	require.Equal(t, path, headers[originalPathHeader])
}

func TestGeminiRetryCredentialOverride(t *testing.T) {
	for _, tc := range []struct {
		name, source, original, retry, expected string
		fallback                                bool
		set                                     string
	}{
		{"restores stripped authorization", "authorization", "request-key", "", "request-key", false, ""},
		{"replaces previous upstream key", internalapi.GoogleAPIKeyHeader, "request-key", "previous-backend-key", "request-key", false, ""},
		{"falls back when original source was absent", internalapi.GoogleAPIKeyHeader, "", "previous-backend-key", "configured-key", true, ""},
		{"respects an explicit backend header mutation", internalapi.GoogleAPIKeyHeader, "request-key", "previous-backend-key", "route-key", false, "route-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := newTestGeminiFactory()
			config := &filterapi.RuntimeConfig{}
			headers := map[string]string{":method": http.MethodPost, ":path": geminiTestPrefix + "/alias:generateContent"}
			if tc.original != "" {
				headers[tc.source] = tc.original
			}
			router, err := factory(config, headers, slog.Default(), false, false)
			require.NoError(t, err)
			_, err = router.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: []byte("{}")})
			require.NoError(t, err)
			backend := &filterapi.Backend{
				Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaGoogleAIStudio},
				Auth: &filterapi.BackendAuth{
					GoogleAPIKey: &filterapi.GoogleAPIKeyAuth{Key: "configured-key"},
					CredentialOverride: &filterapi.CredentialOverride{
						HeaderName:           tc.source,
						FallbackToConfigured: tc.fallback,
						InputHeadersToRemove: []string{tc.source},
					},
				},
				HeaderMutation: &filterapi.HTTPHeaderMutation{Remove: []string{tc.source}},
			}
			if tc.set != "" {
				backend.HeaderMutation.Set = []filterapi.HTTPHeader{{Name: tc.source, Value: tc.set}}
			}
			handler, err := backendauth.NewHandler(t.Context(), backend.Auth)
			require.NoError(t, err)
			runtime := &filterapi.RuntimeBackend{Backend: backend, Handler: handler}
			first := maps.Clone(headers)
			second := maps.Clone(headers)
			if tc.retry == "" {
				delete(second, tc.source)
			} else {
				second[tc.source] = tc.retry
			}
			for _, attempt := range []map[string]string{first, second} {
				upstream, err := factory(config, attempt, slog.Default(), true, false)
				require.NoError(t, err)
				require.NoError(t, upstream.SetBackend(t.Context(), runtime, "", router))
				resp, err := upstream.ProcessRequestHeaders(t.Context(), nil)
				require.NoError(t, err)
				require.Nil(t, resp.GetImmediateResponse())

				mutation := resp.GetRequestHeaders().GetResponse().GetHeaderMutation()
				outgoing := make(map[string]string)
				for _, entry := range mutation.GetSetHeaders() {
					outgoing[entry.GetHeader().GetKey()] = string(entry.GetHeader().GetRawValue())
				}
				require.Contains(t, mutation.GetRemoveHeaders(), tc.source)
				require.NotContains(t, outgoing, "authorization")
				require.Equal(t, tc.expected, outgoing[internalapi.GoogleAPIKeyHeader])
			}
		})
	}
}
