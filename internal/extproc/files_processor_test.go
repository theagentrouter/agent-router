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
	"net/http"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	openai "github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// ---------------------------------------------------------------------------
// Helpers shared across this file
// ---------------------------------------------------------------------------

func newTestCodec() idcodec.Codec {
	c, err := idcodec.NewCodec("proc-test-seed", 4096, "", 0)
	if err != nil {
		panic(err)
	}
	return c
}

// runtimeConfigWithSchema builds a minimal RuntimeConfig with one backend that has the given schema.
func runtimeConfigWithSchema(ns, name, route string, schema filterapi.VersionedAPISchema) *filterapi.RuntimeConfig {
	key := internalapi.PerRouteRuleRefBackendName(ns, name, route, 0, 0)
	return &filterapi.RuntimeConfig{
		Backends: map[string]*filterapi.RuntimeBackend{
			key: {Backend: &filterapi.Backend{Name: key, Schema: schema}},
		},
	}
}

// runtimeConfigWithBackends builds a RuntimeConfig whose Backends map mirrors production keys
// (composite per-route-rule names). Each (ns, name, route) tuple is a separate ref of rule 0 of
// that route, so tuples sharing a route form a multi-backend rule.
func runtimeConfigWithBackends(t *testing.T, specs ...[3]string) *filterapi.RuntimeConfig {
	t.Helper()
	backends := map[string]*filterapi.RuntimeBackend{}
	for i, s := range specs {
		key := internalapi.PerRouteRuleRefBackendName(s[0], s[1], s[2], 0, i)
		backends[key] = &filterapi.RuntimeBackend{
			Backend: &filterapi.Backend{
				Name:   key,
				Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
			},
		}
	}
	return &filterapi.RuntimeConfig{Backends: backends}
}

// encodeGatewayFileID is a test helper that mints a valid gateway file id.
func encodeGatewayFileID(t *testing.T, codec idcodec.Codec, ns, name, nativeID string) string {
	t.Helper()
	id, err := codec.Encode(&idcodec.BackendID{
		Namespace: ns,
		Name:      name,
		Kind:      idcodec.KindFile,
		Model:     "m1",
		NativeID:  nativeID,
	})
	require.NoError(t, err)
	return id
}

// newProcessorForPath creates a router-level filesProcessor for a given path and method.
func newProcessorForPath(method, path string, config *filterapi.RuntimeConfig) *filesProcessor {
	return &filesProcessor{
		codec:          newTestCodec(),
		config:         config,
		requestHeaders: map[string]string{":method": method, ":path": path},
		logger:         slog.Default(),
	}
}

// responseHeader reads a set-header value from a ProcessingResponse_RequestHeaders mutation.
func responseHeaderValue(resp *extprocv3.ProcessingResponse, key string) (string, bool) {
	rh := resp.GetRequestHeaders()
	if rh == nil || rh.Response == nil || rh.Response.HeaderMutation == nil {
		return "", false
	}
	for _, h := range rh.Response.HeaderMutation.SetHeaders {
		if h.Header.Key == key {
			return string(h.Header.RawValue), true
		}
	}
	return "", false
}

// responseBodyHeaderValue reads a set-header from a ProcessingResponse_ResponseBody mutation.
func responseBodyHeaderValue(resp *extprocv3.ProcessingResponse, key string) (string, bool) {
	rb := resp.GetResponseBody()
	if rb == nil || rb.Response == nil || rb.Response.HeaderMutation == nil {
		return "", false
	}
	for _, h := range rb.Response.HeaderMutation.SetHeaders {
		if h.Header.Key == key {
			return string(h.Header.RawValue), true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// classifyFilesRequest
// ---------------------------------------------------------------------------

func TestClassifyFilesRequest(t *testing.T) {
	for _, tc := range []struct {
		method  string
		path    string
		wantOp  filesOperation
		wantID  string
		wantErr bool
	}{
		// upload
		{http.MethodPost, "/v1/files", filesOpUpload, "", false},
		{http.MethodPost, "/v1/files/", filesOpUpload, "", false},
		// list
		{http.MethodGet, "/v1/files", filesOpList, "", false},
		{http.MethodGet, "/v1/files?limit=2&model=m", filesOpList, "", false},
		// retrieve
		{http.MethodGet, "/v1/files/file-abc123", filesOpRetrieve, "file-abc123", false},
		{http.MethodGet, "/v1/files/file-abc123?foo=bar", filesOpRetrieve, "file-abc123", false},
		// content
		{http.MethodGet, "/v1/files/file-abc123/content", filesOpContent, "file-abc123", false},
		// delete
		{http.MethodDelete, "/v1/files/file-abc123", filesOpDelete, "file-abc123", false},
		// unsupported methods on collection
		{http.MethodDelete, "/v1/files", 0, "", true},
		{http.MethodPut, "/v1/files", 0, "", true},
		// unsupported methods on item
		{http.MethodPost, "/v1/files/file-abc123", 0, "", true},
		{http.MethodPost, "/v1/files/file-abc123/content", 0, "", true},
		// not a files path
		{"GET", "/v1/chat/completions", 0, "", true},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			op, id, err := classifyFilesRequest(tc.method, tc.path)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantOp, op)
			require.Equal(t, tc.wantID, id)
		})
	}
}

// ---------------------------------------------------------------------------
// ProcessRequestHeaders — all branches via the router path
// ---------------------------------------------------------------------------

func TestProcessRequestHeaders_UpstreamFilterPassthrough(t *testing.T) {
	p := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	resp, err := p.ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	_, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders)
	require.True(t, ok)
}

func TestProcessRequestHeaders_UnsupportedPath(t *testing.T) {
	p := newProcessorForPath(http.MethodPatch, "/v1/files", nil)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusNotFound), int32(resp.GetImmediateResponse().Status.Code))
}

func TestProcessRequestHeaders_Upload(t *testing.T) {
	// Upload defers routing to ProcessRequestBody, but must set originalPathHeader now so the
	// upstream filter can resolve this processor from its request headers (before the body-phase
	// header mutation is applied).
	p := newProcessorForPath(http.MethodPost, "/v1/files", nil)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	_, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders)
	require.True(t, ok)
	require.Equal(t, filesOpUpload, p.op)
	// originalPathHeader must be set so the upstream filter can resolve the filesProcessor.
	orig, ok := responseHeaderValue(resp, originalPathHeader)
	require.True(t, ok, "originalPathHeader must be set in the RequestHeaders response")
	require.Equal(t, "/v1/files", orig)
}

func TestProcessRequestHeaders_List_MissingModel(t *testing.T) {
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := newProcessorForPath(http.MethodGet, "/v1/files", config)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusBadRequest), int32(resp.GetImmediateResponse().Status.Code))
}

func TestProcessRequestHeaders_List_WithModel(t *testing.T) {
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := newProcessorForPath(http.MethodGet, "/v1/files?model=m2", config)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	// Should route, not return an error.
	require.Nil(t, resp.GetImmediateResponse())
	require.Equal(t, filesOpList, p.op)
}

func TestProcessRequestHeaders_List_AfterGatewayID(t *testing.T) {
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	after := encodeGatewayFileID(t, codec, "ns", "apple", "file-native-9")
	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodGet, ":path": "/v1/files?limit=2&after=" + after},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	// The model recorded in the id is used for routing when ?model= is absent.
	model, ok := responseHeaderValue(resp, internalapi.ModelNameHeaderKeyDefault)
	require.True(t, ok)
	require.Equal(t, "m1", model)
	newPath, ok := responseHeaderValue(resp, ":path")
	require.True(t, ok)
	require.Equal(t, "/v1/files?after=file-native-9&limit=2", newPath)
	require.Equal(t, &backendKey{namespace: "ns", name: "apple"}, p.decodedBackend)
}

func TestProcessRequestHeaders_List_StripsModel(t *testing.T) {
	p := newProcessorForPath(http.MethodGet, "/v1/files?model=m2&limit=3", nil)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	newPath, ok := responseHeaderValue(resp, ":path")
	require.True(t, ok)
	require.Equal(t, "/v1/files?limit=3", newPath)
	require.Equal(t, "m2", p.routingModel)
}

func TestProcessRequestHeaders_List_InvalidAfter(t *testing.T) {
	p := newProcessorForPath(http.MethodGet, "/v1/files?model=m&after=file-native", nil)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusBadRequest), int32(resp.GetImmediateResponse().Status.Code))
}

func TestProcessRequestHeaders_Retrieve_InvalidID(t *testing.T) {
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := newProcessorForPath(http.MethodGet, "/v1/files/not-a-valid-id", config)
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusNotFound), int32(resp.GetImmediateResponse().Status.Code))
}

func TestProcessRequestHeaders_Retrieve_ValidID(t *testing.T) {
	codec := newTestCodec()
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	config := runtimeConfigWithSchema("ns", "apple", "myroute", openAISchema)
	gatewayID := encodeGatewayFileID(t, codec, "ns", "apple", "file-native1")

	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodGet, ":path": "/v1/files/" + gatewayID},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	require.Equal(t, filesOpRetrieve, p.op)
	require.Equal(t, &backendKey{namespace: "ns", name: "apple"}, p.decodedBackend)
	require.Nil(t, p.backend) // set only by SetBackend

	// Path rewritten to native id.
	newPath, ok := responseHeaderValue(resp, ":path")
	require.True(t, ok)
	require.Contains(t, newPath, "file-native1")

	// Original path preserved for upstream filter.
	orig, ok := responseHeaderValue(resp, originalPathHeader)
	require.True(t, ok)
	require.Contains(t, orig, gatewayID)

	// Routed by the model recorded in the id; no sticky dynamic metadata.
	model, ok := responseHeaderValue(resp, internalapi.ModelNameHeaderKeyDefault)
	require.True(t, ok)
	require.Equal(t, "m1", model)
	require.Nil(t, resp.GetDynamicMetadata())
	require.True(t, resp.GetRequestHeaders().GetResponse().GetClearRouteCache())
}

func TestProcessRequestHeaders_Delete_ValidID(t *testing.T) {
	codec := newTestCodec()
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	config := runtimeConfigWithSchema("ns", "apple", "myroute", openAISchema)
	gatewayID := encodeGatewayFileID(t, codec, "ns", "apple", "file-del1")

	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodDelete, ":path": "/v1/files/" + gatewayID},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	require.Equal(t, filesOpDelete, p.op)
}

func TestProcessRequestHeaders_Content_ValidID(t *testing.T) {
	codec := newTestCodec()
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	config := runtimeConfigWithSchema("ns", "apple", "myroute", openAISchema)
	gatewayID := encodeGatewayFileID(t, codec, "ns", "apple", "file-content1")

	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodGet, ":path": "/v1/files/" + gatewayID + "/content"},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	require.Equal(t, filesOpContent, p.op)

	// Path should include /content suffix.
	newPath, ok := responseHeaderValue(resp, ":path")
	require.True(t, ok)
	require.Contains(t, newPath, "/content")
}

// ---------------------------------------------------------------------------
// handleIDBearingRequestHeaders — wrong kind id
// ---------------------------------------------------------------------------

func TestHandleIDBearingRequestHeaders_WrongKind(t *testing.T) {
	codec := newTestCodec()
	// Encode a batch id and try to use it as a file id path parameter.
	batchID, err := codec.Encode(&idcodec.BackendID{
		Namespace: "ns", Name: "apple", Kind: idcodec.KindBatch, NativeID: "batch-native1",
	})
	require.NoError(t, err)

	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodGet, ":path": "/v1/files/" + batchID},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	// batch- id is kind != KindFile → 404
	require.Equal(t, int32(http.StatusNotFound), int32(resp.GetImmediateResponse().Status.Code))
}

// ---------------------------------------------------------------------------
// rewrittenPath
// ---------------------------------------------------------------------------

func TestRewrittenPath(t *testing.T) {
	p := &filesProcessor{
		requestHeaders: map[string]string{":path": "/v1/files/old-gw-id?foo=bar"},
	}

	got := p.rewrittenPath(filesOpRetrieve, "file-native")
	require.Equal(t, "/v1/files/file-native?foo=bar", got)

	// content adds /content suffix
	got = p.rewrittenPath(filesOpContent, "file-native")
	require.Equal(t, "/v1/files/file-native/content?foo=bar", got)
}

func TestRewrittenPath_WithPrefix(t *testing.T) {
	p := &filesProcessor{
		requestHeaders: map[string]string{":path": "/openai/v1/files/gw-id"},
	}
	got := p.rewrittenPath(filesOpDelete, "nat-del")
	require.Equal(t, "/openai/v1/files/nat-del", got)
}

// ---------------------------------------------------------------------------
// ProcessResponseHeaders
// ---------------------------------------------------------------------------

func TestProcessResponseHeaders_UpstreamFilter(t *testing.T) {
	p := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	resp, err := p.ProcessResponseHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	_, ok := resp.Response.(*extprocv3.ProcessingResponse_ResponseHeaders)
	require.True(t, ok)
	require.Nil(t, p.responseHeaders) // upstream filter does not capture
}

func TestProcessResponseHeaders_RouterCaptures(t *testing.T) {
	p := &filesProcessor{isUpstreamFilter: false, logger: slog.Default()}
	headers := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "content-type", RawValue: []byte("application/json")},
			{Key: "x-request-id", RawValue: []byte("req-123")},
		},
	}
	resp, err := p.ProcessResponseHeaders(context.Background(), headers)
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	_, ok := resp.Response.(*extprocv3.ProcessingResponse_ResponseHeaders)
	require.True(t, ok)
	require.Equal(t, "application/json", p.responseHeaders["content-type"])
	require.Equal(t, "req-123", p.responseHeaders["x-request-id"])
}

// ---------------------------------------------------------------------------
// ProcessResponseBody
// ---------------------------------------------------------------------------

func TestProcessResponseBody_UpstreamFilter(t *testing.T) {
	p := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(`{"id":"x"}`)})
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse())
	// Upstream filter does not mutate the body.
	_, ok := resp.Response.(*extprocv3.ProcessingResponse_ResponseBody)
	require.True(t, ok)
	require.Nil(t, resp.GetResponseBody().GetResponse())
}

func TestProcessResponseBody_NonSuccessStatus_PassThrough(t *testing.T) {
	for _, op := range []filesOperation{filesOpUpload, filesOpList, filesOpRetrieve, filesOpDelete} {
		for _, status := range []string{"400", "401", "404", "502", "503"} {
			// backend=nil mirrors a connect failure, where SetBackend never ran.
			p := &filesProcessor{
				op:              op,
				codec:           newTestCodec(),
				logger:          slog.Default(),
				responseHeaders: map[string]string{":status": status},
			}
			body := []byte("upstream connect error or disconnect/reset before headers")
			resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: body})
			require.NoError(t, err)
			require.Nil(t, resp.GetImmediateResponse(), "op=%d status=%s", op, status)
			require.Nil(t, resp.GetResponseBody().GetResponse(), "op=%d status=%s", op, status)
		}
	}
}

func TestIsSuccessResponse(t *testing.T) {
	for _, tc := range []struct {
		headers map[string]string
		want    bool
	}{
		{nil, true},
		{map[string]string{}, true},
		{map[string]string{":status": "200"}, true},
		{map[string]string{":status": "204"}, true},
		{map[string]string{":status": "garbage"}, true},
		{map[string]string{":status": "302"}, false},
		{map[string]string{":status": "400"}, false},
		{map[string]string{":status": "502"}, false},
	} {
		p := &filesProcessor{responseHeaders: tc.headers}
		require.Equal(t, tc.want, p.isSuccessResponse(), "headers=%v", tc.headers)
	}
}

func TestProcessResponseBody_Content_PassThrough(t *testing.T) {
	p := &filesProcessor{
		isUpstreamFilter: false,
		op:               filesOpContent,
		logger:           slog.Default(),
	}
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte("raw bytes")})
	require.NoError(t, err)
	// content is raw bytes — pass through unchanged (nil response = no mutation)
	require.Nil(t, resp.GetResponseBody().GetResponse())
}

func TestProcessResponseBody_Upload_ReEncodesID(t *testing.T) {
	codec := newTestCodec()
	p := &filesProcessor{
		isUpstreamFilter: false,
		op:               filesOpUpload,
		codec:            codec,
		logger:           slog.Default(),
		backend:          &backendKey{namespace: "ns", name: "apple"},
	}
	body := []byte(`{"id":"file-native-123","object":"file","bytes":100,"purpose":"batch"}`)
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: body})
	require.NoError(t, err)

	mutated := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, mutated)

	// The gateway id in the mutated body must decode back to the native id.
	mutatedStr := string(mutated)
	require.Contains(t, mutatedStr, `"file-`)             // gateway id prefix
	require.NotContains(t, mutatedStr, "file-native-123") // native id must not be visible
}

func TestReEncode_StableIDAcrossOperations(t *testing.T) {
	codec := newTestCodec()
	newP := func(op filesOperation) *filesProcessor {
		return &filesProcessor{
			op: op, codec: codec, logger: slog.Default(),
			backend: &backendKey{namespace: "ns", name: "apple"}, routingModel: "m1",
		}
	}
	idOf := func(resp *extprocv3.ProcessingResponse, path string) string {
		body := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
		require.NotEmpty(t, body)
		var v map[string]any
		require.NoError(t, json.Unmarshal(body, &v))
		if path == "list" {
			return v["data"].([]any)[0].(map[string]any)["id"].(string)
		}
		return v["id"].(string)
	}
	obj := []byte(`{"id":"file-native-1","object":"file"}`)
	upload := idOf(newP(filesOpUpload).reEncodeResponse(obj), "")
	retrieve := idOf(newP(filesOpRetrieve).reEncodeResponse(obj), "")
	del := idOf(newP(filesOpDelete).reEncodeResponse([]byte(`{"id":"file-native-1","object":"file","deleted":true}`)), "")
	list := idOf(newP(filesOpList).reEncodeListResponse([]byte(`{"object":"list","data":[{"id":"file-native-1"}],"has_more":false}`)), "list")
	require.Equal(t, upload, retrieve)
	require.Equal(t, upload, del)
	require.Equal(t, upload, list)
}

func TestReEncodeResponse_RecordsRoutingModel(t *testing.T) {
	codec := newTestCodec()
	p := &filesProcessor{
		op:           filesOpUpload,
		codec:        codec,
		logger:       slog.Default(),
		backend:      &backendKey{namespace: "ns", name: "apple"},
		routingModel: "gpt-4o-mini",
	}
	resp := p.reEncodeResponse([]byte(`{"id":"file-native-1","object":"file"}`))
	var out openai.FileObject
	require.NoError(t, json.Unmarshal(resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody(), &out))
	decoded, err := codec.Decode(out.ID)
	require.NoError(t, err)
	require.Equal(t, idcodec.BackendID{Namespace: "ns", Name: "apple", Kind: idcodec.KindFile, Model: "gpt-4o-mini", NativeID: "file-native-1"}, decoded)
}

func TestProcessResponseBody_List_ReEncodes(t *testing.T) {
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		isUpstreamFilter: false,
		op:               filesOpList,
		codec:            codec,
		config:           config,
		logger:           slog.Default(),
		backend:          &backendKey{namespace: "ns", name: "apple"},
	}
	body := []byte(`{"object":"list","data":[{"id":"nat-1"}],"has_more":false}`)
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: body})
	require.NoError(t, err)
	// reEncodeListResponse produces a mutation.
	mutated := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, mutated)
}

// ---------------------------------------------------------------------------
// reEncodeResponse
// ---------------------------------------------------------------------------

func TestReEncodeResponse_BackendUnknown(t *testing.T) {
	p := &filesProcessor{logger: slog.Default()}
	resp := p.reEncodeResponse([]byte(`{"id":"native-1"}`))
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
}

func TestReEncodeResponse_EmptyBody(t *testing.T) {
	p := &filesProcessor{logger: slog.Default(), backend: &backendKey{namespace: "ns", name: "apple"}}
	resp := p.reEncodeResponse([]byte{})
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
}

func TestReEncodeResponse_NoIDField(t *testing.T) {
	codec := newTestCodec()
	p := &filesProcessor{
		logger:  slog.Default(),
		backend: &backendKey{namespace: "ns", name: "apple"},
		codec:   codec,
	}
	resp := p.reEncodeResponse([]byte(`{"object":"file","bytes":100}`))
	// No "id" field → 502, not a silent passthrough.
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
}

func TestReEncodeResponse_Success(t *testing.T) {
	codec := newTestCodec()
	p := &filesProcessor{
		logger:  slog.Default(),
		backend: &backendKey{namespace: "ns", name: "apple"},
		codec:   codec,
	}
	resp := p.reEncodeResponse([]byte(`{"id":"file-nat1","object":"file"}`))
	mutated := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, mutated)
	// id must be a gateway-encoded id
	require.Contains(t, string(mutated), `"file-`)
	// content-length header set
	clv, ok := responseBodyHeaderValue(resp, "content-length")
	require.True(t, ok)
	require.NotEmpty(t, clv)
}

// ---------------------------------------------------------------------------
// SetBackend
// ---------------------------------------------------------------------------

func TestSetBackend_DecodedBackendMismatch_Error(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	routeProcessor := &filesProcessor{
		op:             filesOpRetrieve,
		decodedBackend: &backendKey{namespace: "ns", name: "original"},
		logger:         slog.Default(),
	}
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   internalapi.PerRouteRuleRefBackendName("ns", "other", "route", 0, 0),
			Schema: openAISchema,
		},
	}
	err := upstreamProcessor.SetBackend(context.Background(), newBackend, "route", routeProcessor)
	require.ErrorContains(t, err, "file id belongs to backend ns/original")
}

func TestSetBackend_DecodedBackendMatch(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	routeProcessor := &filesProcessor{
		op:             filesOpRetrieve,
		decodedBackend: &backendKey{namespace: "ns", name: "apple"},
		logger:         slog.Default(),
	}
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   internalapi.PerRouteRuleRefBackendName("ns", "apple", "route", 0, 0),
			Schema: openAISchema,
		},
	}
	require.NoError(t, upstreamProcessor.SetBackend(context.Background(), newBackend, "route", routeProcessor))
	require.NotNil(t, routeProcessor.translator)
}

func TestSetBackend_MultipleBackendsInRule_Error(t *testing.T) {
	config := runtimeConfigWithBackends(t,
		[3]string{"ns", "apple", "myroute"},
		[3]string{"ns", "banana", "myroute"},
		[3]string{"ns", "cherry", "otherroute"},
	)
	routeProcessor := &filesProcessor{op: filesOpUpload, config: config, logger: slog.Default()}
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	backend := config.Backends[internalapi.PerRouteRuleRefBackendName("ns", "apple", "myroute", 0, 0)]
	err := upstreamProcessor.SetBackend(context.Background(), backend, "myroute", routeProcessor)
	require.ErrorContains(t, err, "exactly one backend per route rule")
	require.Nil(t, routeProcessor.backend)

	// A single-backend rule in the same config is accepted.
	backend = config.Backends[internalapi.PerRouteRuleRefBackendName("ns", "cherry", "otherroute", 0, 2)]
	require.NoError(t, upstreamProcessor.SetBackend(context.Background(), backend, "otherroute", routeProcessor))
	require.Equal(t, "cherry", routeProcessor.backend.name)
}

func TestRouteRuleKey(t *testing.T) {
	k, ok := routeRuleKey(internalapi.PerRouteRuleRefBackendName("ns", "apple", "my-route", 2, 3))
	require.True(t, ok)
	require.Equal(t, "route/my-route/rule/2", k)
	_, ok = routeRuleKey("testupstream-openai")
	require.False(t, ok)
}

func TestSetBackend_NonOpenAISchema_Error(t *testing.T) {
	awsSchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}
	routeProcessor := &filesProcessor{
		op:     filesOpUpload,
		logger: slog.Default(),
	}
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   internalapi.PerRouteRuleRefBackendName("ns", "aws-b", "route", 0, 0),
			Schema: awsSchema,
		},
	}
	err := upstreamProcessor.SetBackend(context.Background(), newBackend, "route", routeProcessor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "files API not supported for schema")
}

func TestSetBackend_SetsBackend(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	routeProcessor := &filesProcessor{
		op:     filesOpList,
		logger: slog.Default(),
	}
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   internalapi.PerRouteRuleRefBackendName("ns", "apple", "myroute", 0, 0),
			Schema: openAISchema,
		},
	}
	err := upstreamProcessor.SetBackend(context.Background(), newBackend, "myroute", routeProcessor)
	require.NoError(t, err)
	require.Equal(t, &backendKey{namespace: "ns", name: "apple"}, routeProcessor.backend)
}

func TestSetBackend_WrongRouteProcessorType_Panics(t *testing.T) {
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{Name: "ns/any/route/r/rule/0/ref/0", Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}},
	}
	// Passing a non-*filesProcessor as routeProcessor should panic (BUG guard).
	require.Panics(t, func() {
		_ = upstreamProcessor.SetBackend(context.Background(), newBackend, "", &passThroughProcessor{})
	})
}

// ---------------------------------------------------------------------------
// resolveTranslator — all op branches
// ---------------------------------------------------------------------------

func TestResolveTranslator_AllOps(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	awsSchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}

	for _, op := range []filesOperation{filesOpUpload, filesOpList, filesOpRetrieve, filesOpContent, filesOpDelete} {
		p := &filesProcessor{op: op, logger: slog.Default()}
		require.NoError(t, p.resolveTranslator(openAISchema), "op %d should resolve for OpenAI", op)
		require.NotNil(t, p.translator)

		p2 := &filesProcessor{op: op, logger: slog.Default()}
		require.Error(t, p2.resolveTranslator(awsSchema), "op %d should fail for AWS", op)
	}
}

func TestResolveTranslator_UnsupportedOp(t *testing.T) {
	p := &filesProcessor{op: filesOperation(99), logger: slog.Default()}
	err := p.resolveTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// multipartBoundary
// ---------------------------------------------------------------------------

func TestMultipartBoundary(t *testing.T) {
	require.Equal(t, "abc123", multipartBoundary("multipart/form-data; boundary=abc123"))
	require.Empty(t, multipartBoundary("application/json"))
	require.Empty(t, multipartBoundary(""))
	require.Empty(t, multipartBoundary("multipart/form-data")) // no boundary param
}

// ---------------------------------------------------------------------------
// extraBodyModel
// ---------------------------------------------------------------------------

func TestExtraBodyModel(t *testing.T) {
	require.Equal(t, "gpt-4", extraBodyModel(&openai.FileNewParams{
		ExtraBody: map[string]any{"model": []byte("gpt-4")},
	}))
	require.Equal(t, "gpt-4", extraBodyModel(&openai.FileNewParams{
		ExtraBody: map[string]any{"model": "gpt-4"},
	}))
	require.Empty(t, extraBodyModel(&openai.FileNewParams{
		ExtraBody: map[string]any{"model": 123},
	}))
	require.Empty(t, extraBodyModel(&openai.FileNewParams{}))
}

// ---------------------------------------------------------------------------
// reEncodeResponse — translator path (responseHeaders set)
// ---------------------------------------------------------------------------

func TestReEncodeResponse_WithResponseHeaders(t *testing.T) {
	// When responseHeaders is set, the translator is invoked. For OpenAI (no-op), the body is
	// unchanged and the id is still re-encoded normally.
	codec := newTestCodec()
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	p := &filesProcessor{
		logger:          slog.Default(),
		op:              filesOpRetrieve,
		backend:         &backendKey{namespace: "ns", name: "apple"},
		codec:           codec,
		responseHeaders: map[string]string{"content-type": "application/json"},
	}
	require.NoError(t, p.resolveTranslator(openAISchema))

	resp := p.reEncodeResponse([]byte(`{"id":"file-nat2","object":"file"}`))
	mutated := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, mutated)
	require.Contains(t, string(mutated), `"file-`) // gateway-encoded id
}

// errEncodeCodec is a codec whose Encode always fails, used to exercise the re-encode error paths.
type errEncodeCodec struct{}

func (errEncodeCodec) Encode(*idcodec.BackendID) (string, error) {
	return "", errors.New("boom")
}

func (errEncodeCodec) Decode(string) (idcodec.BackendID, error) {
	return idcodec.BackendID{}, errors.New("boom")
}

func TestReEncodeResponse_EncodeFailure_Returns502(t *testing.T) {
	p := &filesProcessor{
		logger:  slog.Default(),
		backend: &backendKey{namespace: "ns", name: "apple"},
		codec:   errEncodeCodec{},
	}
	resp := p.reEncodeResponse([]byte(`{"id":"file-native-secret","object":"file"}`))
	// A controlled 502 error is returned rather than leaking the native id.
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
	require.NotContains(t, string(resp.GetImmediateResponse().Body), "file-native-secret")
}

func TestReEncodeListResponse_EncodeFailure_Returns502(t *testing.T) {
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:   errEncodeCodec{},
		config:  config,
		logger:  slog.Default(),
		backend: &backendKey{namespace: "ns", name: "apple"},
	}
	resp := p.reEncodeListResponse([]byte(`{"object":"list","data":[{"id":"file-native-secret","object":"file"}]}`))
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
	require.NotContains(t, string(resp.GetImmediateResponse().Body), "file-native-secret")
}

// ---------------------------------------------------------------------------
// reEncodeListResponse — first_id and terminal-page last_id paths
// ---------------------------------------------------------------------------

func TestReEncodeListResponse_ReencodesFirstID(t *testing.T) {
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:   codec,
		config:  config,
		logger:  slog.Default(),
		op:      filesOpList,
		backend: &backendKey{namespace: "ns", name: "apple"},
	}

	body := []byte(`{"object":"list","data":[{"id":"nat-1"}],"has_more":false,"first_id":"nat-1","last_id":"nat-1"}`)
	resp := p.reEncodeListResponse(body)
	out := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, out)
	outStr := string(out)
	// first_id must be re-encoded (not raw native)
	require.NotContains(t, outStr, `"first_id":"nat-1"`)
	require.Contains(t, outStr, `"first_id":"file-`)
}

func TestReEncodeListResponse_TerminalPage_ReencodesLastID(t *testing.T) {
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:   codec,
		config:  config,
		logger:  slog.Default(),
		op:      filesOpList,
		backend: &backendKey{namespace: "ns", name: "apple"},
	}
	body := []byte(`{"object":"list","data":[{"id":"nat-1"}],"has_more":false,"last_id":"nat-1"}`)
	resp := p.reEncodeListResponse(body)
	out := string(resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody())
	// Terminal page: has_more=false and last_id re-encoded to gateway id.
	require.Contains(t, out, `"has_more":false`)
	require.Contains(t, out, `"last_id":"file-`)
	require.NotContains(t, out, `"last_id":"nat-1"`)
}

func TestReEncodeListResponse_EmptyData_Passthrough(t *testing.T) {
	// A response with a data array but no items still succeeds and keeps has_more:false.
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:   codec,
		config:  config,
		logger:  slog.Default(),
		op:      filesOpList,
		backend: &backendKey{namespace: "ns", name: "apple"},
	}
	body := []byte(`{"object":"list","data":[],"has_more":false}`)
	resp := p.reEncodeListResponse(body)
	out := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, out)
	require.Contains(t, string(out), `"has_more":false`)
}

// ---------------------------------------------------------------------------
// stripMultipartField — error path (bad boundary)
// ---------------------------------------------------------------------------

func TestStripMultipartField_BadBoundary(t *testing.T) {
	// SetBoundary fails for a boundary containing a null byte (invalid per mime package).
	_, err := stripMultipartField([]byte("any body"), "invalid\x00boundary", "model")
	require.Error(t, err)
}

func TestStripMultipartField_FieldAbsent(t *testing.T) {
	// If the field is not present, the result should be identical to the input.
	body, ct := buildTestMultipartBody(t, map[string]string{"purpose": "batch"}, "f.jsonl", []byte("{}"))
	boundary := multipartBoundary(ct)
	stripped, err := stripMultipartField(body, boundary, "model") // "model" not in body
	require.NoError(t, err)
	require.NotEmpty(t, stripped)
	// The purpose field must still be present.
	require.Contains(t, string(stripped), "purpose")
}

func TestSetQueryParam(t *testing.T) {
	require.Equal(t, "/v1/files?after=native-1&limit=2", setQueryParam("/v1/files?limit=2", "after", "native-1"))
	require.Equal(t, "/v1/files?limit=2", setQueryParam("/v1/files?limit=2&after=old", "after", ""))
	require.Equal(t, "/v1/files", setQueryParam("/v1/files", "after", ""))
	// Removing an absent parameter leaves the path (and its original encoding) untouched.
	require.Equal(t, "/v1/files?b=1&a=2", setQueryParam("/v1/files?b=1&a=2", "model", ""))
	require.Equal(t, "/v1/files?limit=2", setQueryParam("/v1/files?model=m1&limit=2", "model", ""))
	require.Equal(t, "/v1/files", setQueryParam("/v1/files?model=m1", "model", ""))
	// A malformed query is returned unchanged.
	require.Equal(t, "/v1/files?%zz", setQueryParam("/v1/files?%zz", "after", "x"))
}

// ---------------------------------------------------------------------------
// NewFilesProcessorFactory
// ---------------------------------------------------------------------------

func TestNewFilesProcessorFactory(t *testing.T) {
	codec := newTestCodec()
	factory := NewFilesProcessorFactory(codec)
	require.NotNil(t, factory)

	config := &filterapi.RuntimeConfig{}
	headers := map[string]string{":method": http.MethodGet, ":path": "/v1/files"}
	proc, err := factory(config, headers, slog.Default(), false, false)
	require.NoError(t, err)
	require.NotNil(t, proc)
}

// ---------------------------------------------------------------------------
// Translator wiring (fake translator)
// ---------------------------------------------------------------------------

// fakeFilesTranslator records calls and returns configured mutations.
type fakeFilesTranslator struct {
	gotRaw []byte
	gotReq *translator.FilesRequest

	reqHeaders  []internalapi.Header
	reqBody     []byte
	reqErr      error
	respHeaders []internalapi.Header
	respBody    []byte
	errBody     []byte
	gotErrBody  []byte
}

func (f *fakeFilesTranslator) RequestBody(raw []byte, req *translator.FilesRequest, _ bool) ([]internalapi.Header, []byte, error) {
	f.gotRaw, f.gotReq = raw, req
	return f.reqHeaders, f.reqBody, f.reqErr
}

func (f *fakeFilesTranslator) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return f.respHeaders, nil
}

func (f *fakeFilesTranslator) ResponseBody(_ map[string]string, _ io.Reader, _ bool, _ any) (
	[]internalapi.Header, []byte, metrics.TokenUsage, internalapi.ResponseModel, error,
) {
	return nil, f.respBody, metrics.TokenUsage{}, "", nil
}

func (f *fakeFilesTranslator) ResponseError(_ map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	f.gotErrBody, _ = io.ReadAll(body)
	return nil, f.errBody, nil
}

func upstreamWithRouteProcessor(rp *filesProcessor) *filesProcessor {
	return &filesProcessor{isUpstreamFilter: true, logger: slog.Default(), routeProcessor: rp}
}

func TestUpstreamRequest_NoParent_PassThrough(t *testing.T) {
	resp, err := upstreamWithRouteProcessor(nil).ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	require.Nil(t, resp.GetRequestHeaders().GetResponse())
}

func TestUpstreamRequest_HeadersOnly(t *testing.T) {
	ft := &fakeFilesTranslator{reqHeaders: []internalapi.Header{{":path", "/provider/files/native-1"}}}
	rp := &filesProcessor{
		translator:           ft,
		filesRequestMetadata: translator.FilesRequest{NativeID: "native-1", Path: "/v1/files/native-1", Method: http.MethodGet},
	}
	resp, err := upstreamWithRouteProcessor(rp).ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	common := resp.GetRequestHeaders().GetResponse()
	require.Equal(t, extprocv3.CommonResponse_CONTINUE, common.Status)
	require.Nil(t, common.BodyMutation)
	path, ok := responseHeaderValue(resp, ":path")
	require.True(t, ok)
	require.Equal(t, "/provider/files/native-1", path)
	require.Equal(t, "native-1", ft.gotReq.NativeID)
	require.Nil(t, ft.gotRaw)
}

func TestUpstreamRequest_ReplacesBody(t *testing.T) {
	ft := &fakeFilesTranslator{reqBody: []byte("translated")}
	rp := &filesProcessor{
		translator:           ft,
		requestBody:          []byte("stripped-multipart"),
		filesRequestMetadata: translator.FilesRequest{Model: "m1", Method: http.MethodPost},
	}
	resp, err := upstreamWithRouteProcessor(rp).ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	common := resp.GetRequestHeaders().GetResponse()
	require.Equal(t, extprocv3.CommonResponse_CONTINUE_AND_REPLACE, common.Status)
	require.Equal(t, []byte("translated"), common.BodyMutation.GetBody())
	cl, ok := responseHeaderValue(resp, "content-length")
	require.True(t, ok)
	require.Equal(t, "10", cl)
	require.Equal(t, []byte("stripped-multipart"), ft.gotRaw)
	require.Equal(t, "m1", ft.gotReq.Model)
}

func TestUpstreamRequest_Errors(t *testing.T) {
	rp := &filesProcessor{translator: &fakeFilesTranslator{reqErr: errors.New("boom")}}
	_, err := upstreamWithRouteProcessor(rp).ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.ErrorContains(t, err, "boom")

	rp = &filesProcessor{translator: &fakeFilesTranslator{reqErr: fmt.Errorf("%w: bad purpose", internalapi.ErrInvalidRequestBody)}}
	resp, err := upstreamWithRouteProcessor(rp).ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusUnprocessableEntity), int32(resp.GetImmediateResponse().Status.Code))
}

func TestSetBackend_LinksParent(t *testing.T) {
	routeProcessor := &filesProcessor{op: filesOpList, logger: slog.Default()}
	up := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	b := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "r"})
	for _, rb := range b.Backends {
		require.NoError(t, up.SetBackend(context.Background(), rb, "r", routeProcessor))
	}
	require.Same(t, routeProcessor, up.routeProcessor)
}

func TestRouterRecordsFilesRequest(t *testing.T) {
	codec := newTestCodec()
	gw := encodeGatewayFileID(t, codec, "ns", "apple", "native-9")
	p := newProcessorForPath(http.MethodDelete, "/v1/files/"+gw, nil)
	p.codec = codec
	_, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "native-9", p.filesRequestMetadata.NativeID)
	require.Equal(t, "/v1/files/native-9", p.filesRequestMetadata.Path)
	require.Equal(t, http.MethodDelete, p.filesRequestMetadata.Method)

	p = newProcessorForPath(http.MethodGet, "/v1/files?model=m1&limit=2", nil)
	_, err = p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "?limit=2", p.filesRequestMetadata.Query)
	require.Equal(t, "/v1/files?limit=2", p.filesRequestMetadata.Path)
}

func TestResponseHeaders_Translated(t *testing.T) {
	p := &filesProcessor{logger: slog.Default(), translator: &fakeFilesTranslator{respHeaders: []internalapi.Header{{"x-p", "1"}}}}
	resp, err := p.ProcessResponseHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	hm := resp.GetResponseHeaders().GetResponse().GetHeaderMutation()
	require.Len(t, hm.SetHeaders, 1)
	require.Equal(t, "x-p", hm.SetHeaders[0].Header.Key)
}

func TestResponseError_Translated(t *testing.T) {
	ft := &fakeFilesTranslator{errBody: []byte(`{"error":{}}`)}
	p := &filesProcessor{logger: slog.Default(), op: filesOpRetrieve, translator: ft, responseHeaders: map[string]string{":status": "404"}}
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte("provider err")})
	require.NoError(t, err)
	require.Equal(t, []byte("provider err"), ft.gotErrBody)
	require.JSONEq(t, `{"error":{}}`, string(resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()))
	cl, ok := responseBodyHeaderValue(resp, "content-length")
	require.True(t, ok)
	require.Equal(t, "12", cl)
}

func TestContentResponse_Translated(t *testing.T) {
	ft := &fakeFilesTranslator{respBody: []byte("unwrapped")}
	p := &filesProcessor{logger: slog.Default(), op: filesOpContent, translator: ft, responseHeaders: map[string]string{":status": "200"}}
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte("wrapped")})
	require.NoError(t, err)
	require.Equal(t, []byte("unwrapped"), resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody())
}
