// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/fal"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

func TestFalEndpointSpec_ModelFromPath(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		path      string
		expModel  string
		expStream bool
		expOK     bool
	}{
		{name: "run", prefix: "/fal", path: "/fal/run/fal-ai/flux/dev", expModel: "fal-ai/flux/dev", expOK: true},
		{name: "run stream", prefix: "/fal", path: "/fal/run/fal-ai/flux/dev/stream", expModel: "fal-ai/flux/dev", expStream: true, expOK: true},
		{name: "queue submit", prefix: "/fal", path: "/fal/queue/fal-ai/flux/schnell", expModel: "fal-ai/flux/schnell", expOK: true},
		{name: "queue status", prefix: "/fal", path: "/fal/queue/fal-ai/flux/requests/7f1c/status", expModel: "fal-ai/flux", expOK: true},
		{name: "queue status stream", prefix: "/fal", path: "/fal/queue/fal-ai/flux/requests/7f1c/status/stream", expModel: "fal-ai/flux", expStream: true, expOK: true},
		{name: "openrouter path", prefix: "/fal", path: "/fal/run/openrouter/router/openai/v1/chat/completions", expModel: "openrouter/router/openai/v1/chat/completions", expOK: true},
		{name: "no endpoint", prefix: "/fal", path: "/fal/run/"},
		{name: "custom prefix", prefix: "/gen", path: "/gen/queue/fal-ai/flux/schnell", expModel: "fal-ai/flux/schnell", expOK: true},
		{name: "default path under custom prefix", prefix: "/gen", path: "/fal/run/fal-ai/flux/dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, stream, ok := FalEndpointSpec{Prefix: tt.prefix}.ModelFromPath(tt.path)
			require.Equal(t, tt.expOK, ok)
			require.Equal(t, tt.expModel, model)
			require.Equal(t, tt.expStream, stream)
		})
	}
}

func TestFalEndpointSpec_ParseBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "app input", body: `{"prompt":"a cat","image_size":"square"}`},
		{name: "app input with a model field", body: `{"model":"anthropic/claude-3.5-sonnet","prompt":"hi"}`},
		{name: "not json", body: `prompt=a+cat`},
		{name: "empty", body: ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, req, stream, mutated, err := FalEndpointSpec{}.ParseBody([]byte(tt.body), true)
			require.NoError(t, err)
			require.Empty(t, model)
			require.Equal(t, &fal.Request{}, req)
			require.False(t, stream)
			require.Nil(t, mutated)

			model, req, stream, mutated, err = FalEndpointSpec{}.ParseMultipartBody([]byte(tt.body), "multipart/form-data; boundary=x", true)
			require.NoError(t, err)
			require.Empty(t, model)
			require.Equal(t, &fal.Request{}, req)
			require.False(t, stream)
			require.Nil(t, mutated)
		})
	}
}

func TestFalEndpointSpec_GetTranslator(t *testing.T) {
	tests := []struct {
		name     string
		schema   filterapi.APISchemaName
		override string
		expErr   string
	}{
		{name: "fal", schema: filterapi.APISchemaFal},
		{name: "openai", schema: filterapi.APISchemaOpenAI, expErr: "unsupported API schema"},
		{name: "model override", schema: filterapi.APISchemaFal, override: "fal-ai/flux/schnell", expErr: "model name override is not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := FalEndpointSpec{Prefix: "/fal"}.GetTranslator(filterapi.VersionedAPISchema{Name: tt.schema}, tt.override)
			if tt.expErr != "" {
				require.ErrorContains(t, err, tt.expErr)
				require.Error(t, internalapi.GetUserFacingError(err))
				return
			}
			require.NoError(t, err)
			require.NotNil(t, tr)
		})
	}
}

func TestFalEndpointSpec_GetTranslator_URLScheme(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		expBody string
	}{
		{name: "unset uses the request scheme", expBody: `{"status_url":"http://gw.example/fal/queue/fal-ai/flux/requests/r/status"}`},
		{name: "https", scheme: internalapi.FalURLSchemeHTTPS, expBody: `{"status_url":"https://gw.example/fal/queue/fal-ai/flux/requests/r/status"}`},
		{name: "http", scheme: internalapi.FalURLSchemeHTTP, expBody: `{"status_url":"http://gw.example/fal/queue/fal-ai/flux/requests/r/status"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := FalEndpointSpec{Prefix: "/fal", URLScheme: tt.scheme}.GetTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaFal}, "")
			require.NoError(t, err)
			const path = "/fal/queue/fal-ai/flux/requests/r/status"
			tr.(translator.RequestHeadersSetter).SetRequestHeaders(map[string]string{internalapi.OriginalPathHeader: path})
			tr.(translator.ClientRequestHeadersSetter).SetClientRequestHeaders(map[string]string{
				internalapi.OriginalPathHeader: path,
				":authority":                   "gw.example",
				":scheme":                      "http",
			})
			_, _, err = tr.RequestBody(nil, &fal.Request{}, false)
			require.NoError(t, err)
			_, body, _, _, err := tr.ResponseBody(map[string]string{":status": "200"},
				bytes.NewBufferString(`{"status_url":"https://queue.fal.run/fal-ai/flux/requests/r/status"}`), true, nil)
			require.NoError(t, err)
			require.Equal(t, tt.expBody, string(body))
		})
	}
}

func TestFalEndpointSpec_RedactSensitiveInfoFromRequest(t *testing.T) {
	req := &fal.Request{}
	got, err := FalEndpointSpec{}.RedactSensitiveInfoFromRequest(req)
	require.NoError(t, err)
	require.Equal(t, req, got)
}
