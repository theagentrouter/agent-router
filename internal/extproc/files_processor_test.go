// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
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
	"github.com/envoyproxy/ai-gateway/internal/mcpproxy"
)

// ---------------------------------------------------------------------------
// Helpers shared across this file
// ---------------------------------------------------------------------------

func newTestCodec() idcodec.Codec {
	return idcodec.NewAESGCMCodec(mcpproxy.NewPBKDF2AesGcmSessionCrypto("proc-test-seed", 4096), nil)
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

// encodeGatewayFileID is a test helper that mints a valid gateway file id.
func encodeGatewayFileID(t *testing.T, codec idcodec.Codec, ns, name, nativeID string) string {
	t.Helper()
	id, err := codec.Encode(idcodec.BackendID{
		Namespace: ns,
		Name:      name,
		Kind:      idcodec.KindFile,
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
	require.True(t, p.backendKnown)
	require.True(t, p.backendFromDecode)
	require.Equal(t, "ns", p.backendNamespace)
	require.Equal(t, "apple", p.backendName)

	// Path rewritten to native id.
	newPath, ok := responseHeaderValue(resp, ":path")
	require.True(t, ok)
	require.Contains(t, newPath, "file-native1")

	// Original path preserved for upstream filter.
	orig, ok := responseHeaderValue(resp, originalPathHeader)
	require.True(t, ok)
	require.Contains(t, orig, gatewayID)

	// Dynamic metadata pins the backend.
	dm := resp.GetDynamicMetadata()
	require.NotNil(t, dm)
	ns := dm.Fields[internalapi.AIGatewayFilterMetadataNamespace]
	require.NotNil(t, ns)
	sv := ns.GetStructValue().Fields[internalapi.AIGatewaySelectedBackendMetadataKey]
	require.Equal(t, "ns.apple", sv.GetStringValue())
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

func TestProcessRequestHeaders_Retrieve_BackendGone(t *testing.T) {
	// Backend ns/apple encoded in id but config has ns/banana — id decodes, but schemaForBackend misses.
	codec := newTestCodec()
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	config := runtimeConfigWithSchema("ns", "banana", "myroute", openAISchema) // different backend
	gatewayID := encodeGatewayFileID(t, codec, "ns", "apple", "file-native1")

	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodGet, ":path": "/v1/files/" + gatewayID},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusGone), int32(resp.GetImmediateResponse().Status.Code))
}

func TestProcessRequestHeaders_Retrieve_NonOpenAISchema(t *testing.T) {
	codec := newTestCodec()
	awsSchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}
	config := runtimeConfigWithSchema("ns", "apple", "myroute", awsSchema)
	gatewayID := encodeGatewayFileID(t, codec, "ns", "apple", "file-native1")

	p := &filesProcessor{
		codec:          codec,
		config:         config,
		requestHeaders: map[string]string{":method": http.MethodGet, ":path": "/v1/files/" + gatewayID},
		logger:         slog.Default(),
	}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, int32(http.StatusNotImplemented), int32(resp.GetImmediateResponse().Status.Code))
}

// ---------------------------------------------------------------------------
// handleIDBearingRequestHeaders — wrong kind id
// ---------------------------------------------------------------------------

func TestHandleIDBearingRequestHeaders_WrongKind(t *testing.T) {
	codec := newTestCodec()
	// Encode a batch id and try to use it as a file id path parameter.
	batchID, err := codec.Encode(idcodec.BackendID{
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
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
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

func TestProcessResponseBody_List_CallsBuildWalkResponse(t *testing.T) {
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		isUpstreamFilter: false,
		op:               filesOpList,
		codec:            codec,
		config:           config,
		logger:           slog.Default(),
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		listRouteName:    "myroute",
	}
	body := []byte(`{"object":"list","data":[{"id":"nat-1"}],"has_more":false}`)
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: body})
	require.NoError(t, err)
	// buildListWalkResponse produces a mutation.
	mutated := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, mutated)
}

// ---------------------------------------------------------------------------
// reEncodeResponse
// ---------------------------------------------------------------------------

func TestReEncodeResponse_BackendUnknown(t *testing.T) {
	p := &filesProcessor{logger: slog.Default(), backendKnown: false}
	resp := p.reEncodeResponse([]byte(`{"id":"native-1"}`))
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
}

func TestReEncodeResponse_EmptyBody(t *testing.T) {
	p := &filesProcessor{logger: slog.Default(), backendKnown: true}
	resp := p.reEncodeResponse([]byte{})
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
}

func TestReEncodeResponse_NoIDField(t *testing.T) {
	codec := newTestCodec()
	p := &filesProcessor{
		logger:           slog.Default(),
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		codec:            codec,
	}
	resp := p.reEncodeResponse([]byte(`{"object":"file","bytes":100}`))
	// No "id" field → 502, not a silent passthrough.
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
}

func TestReEncodeResponse_Success(t *testing.T) {
	codec := newTestCodec()
	p := &filesProcessor{
		logger:           slog.Default(),
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		codec:            codec,
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

func TestSetBackend_BackendFromDecode_NoOverride(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	routeProcessor := &filesProcessor{
		op:                filesOpRetrieve,
		backendFromDecode: true,
		backendNamespace:  "ns",
		backendName:       "original",
		backendKnown:      true,
		logger:            slog.Default(),
	}
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:   internalapi.PerRouteRuleRefBackendName("ns", "other", "route", 0, 0),
			Schema: openAISchema,
		},
	}
	err := upstreamProcessor.SetBackend(context.Background(), newBackend, "route", routeProcessor)
	require.NoError(t, err)
	// backendFromDecode=true → the router processor's backend must NOT be overridden.
	require.Equal(t, "original", routeProcessor.backendName)
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

func TestSetBackend_SetsListRouteName(t *testing.T) {
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
	require.Equal(t, "myroute", routeProcessor.listRouteName)
	require.Equal(t, "apple", routeProcessor.backendName)
	require.Equal(t, "ns", routeProcessor.backendNamespace)
	require.True(t, routeProcessor.backendKnown)
}

func TestSetBackend_WrongRouteProcessorType_Panics(t *testing.T) {
	upstreamProcessor := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	newBackend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{Name: "any", Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}},
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
// schemaForBackend
// ---------------------------------------------------------------------------

func TestSchemaForBackend_Found(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	config := runtimeConfigWithSchema("ns", "apple", "myroute", openAISchema)
	p := &filesProcessor{}
	got, ok := p.schemaForBackend(config, "ns", "apple")
	require.True(t, ok)
	require.Equal(t, filterapi.APISchemaOpenAI, got.Name)
}

func TestSchemaForBackend_NotFound(t *testing.T) {
	openAISchema := filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}
	config := runtimeConfigWithSchema("ns", "apple", "myroute", openAISchema)
	p := &filesProcessor{}
	_, ok := p.schemaForBackend(config, "ns", "banana")
	require.False(t, ok)
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
		logger:           slog.Default(),
		op:               filesOpRetrieve,
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		codec:            codec,
		responseHeaders:  map[string]string{"content-type": "application/json"},
	}
	require.NoError(t, p.resolveTranslator(openAISchema))

	resp := p.reEncodeResponse([]byte(`{"id":"file-nat2","object":"file"}`))
	mutated := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, mutated)
	require.Contains(t, string(mutated), `"file-`) // gateway-encoded id
}

// errEncodeCodec is a codec whose Encode always fails, used to exercise the re-encode error paths.
type errEncodeCodec struct{}

func (errEncodeCodec) Encode(idcodec.BackendID) (string, error) {
	return "", errors.New("boom")
}

func (errEncodeCodec) Decode(string) (idcodec.BackendID, error) {
	return idcodec.BackendID{}, errors.New("boom")
}

func TestReEncodeResponse_EncodeFailure_Returns502(t *testing.T) {
	p := &filesProcessor{
		logger:           slog.Default(),
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		codec:            errEncodeCodec{},
	}
	resp := p.reEncodeResponse([]byte(`{"id":"file-native-secret","object":"file"}`))
	// A controlled 502 error is returned rather than leaking the native id.
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
	require.NotContains(t, string(resp.GetImmediateResponse().Body), "file-native-secret")
}

func TestBuildListWalkResponse_EncodeFailure_Returns502(t *testing.T) {
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:            errEncodeCodec{},
		config:           config,
		logger:           slog.Default(),
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		listRouteName:    "myroute",
	}
	resp := p.buildListWalkResponse([]byte(`{"object":"list","data":[{"id":"file-native-secret","object":"file"}]}`))
	require.Equal(t, int32(http.StatusBadGateway), int32(resp.GetImmediateResponse().Status.Code))
	require.NotContains(t, string(resp.GetImmediateResponse().Body), "file-native-secret")
}

// ---------------------------------------------------------------------------
// buildListWalkResponse — first_id and terminal-page last_id paths
// ---------------------------------------------------------------------------

func TestBuildListWalkResponse_ReencodesFirstID(t *testing.T) {
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:            codec,
		config:           config,
		logger:           slog.Default(),
		op:               filesOpList,
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		listRouteName:    "myroute",
	}

	body := []byte(`{"object":"list","data":[{"id":"nat-1"}],"has_more":false,"first_id":"nat-1","last_id":"nat-1"}`)
	resp := p.buildListWalkResponse(body)
	out := resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
	require.NotEmpty(t, out)
	outStr := string(out)
	// first_id must be re-encoded (not raw native)
	require.NotContains(t, outStr, `"first_id":"nat-1"`)
	require.Contains(t, outStr, `"first_id":"file-`)
}

func TestBuildListWalkResponse_TerminalPage_ReencodesLastID(t *testing.T) {
	codec := newTestCodec()
	// Single backend walk — after one page, walk is complete (no has_more → terminal)
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:            codec,
		config:           config,
		logger:           slog.Default(),
		op:               filesOpList,
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		listRouteName:    "myroute",
		listStart:        backendKey{namespace: "ns", name: "apple"},
		listStartKnown:   true,
	}
	body := []byte(`{"object":"list","data":[{"id":"nat-1"}],"has_more":false,"last_id":"nat-1"}`)
	resp := p.buildListWalkResponse(body)
	out := string(resp.GetResponseBody().GetResponse().GetBodyMutation().GetBody())
	// Terminal page: has_more=false and last_id re-encoded to gateway id.
	require.Contains(t, out, `"has_more":false`)
	require.Contains(t, out, `"last_id":"file-`)
	require.NotContains(t, out, `"last_id":"nat-1"`)
}

func TestBuildListWalkResponse_EmptyData_Passthrough(t *testing.T) {
	// A response with a data array but no items: walk should still succeed and return has_more:false.
	codec := newTestCodec()
	config := runtimeConfigWithBackends(t, [3]string{"ns", "apple", "myroute"})
	p := &filesProcessor{
		codec:            codec,
		config:           config,
		logger:           slog.Default(),
		op:               filesOpList,
		backendKnown:     true,
		backendNamespace: "ns",
		backendName:      "apple",
		listRouteName:    "myroute",
	}
	body := []byte(`{"object":"list","data":[],"has_more":false}`)
	resp := p.buildListWalkResponse(body)
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

func TestRewriteAfterParam(t *testing.T) {
	require.Equal(t, "/v1/files?after=native-1&limit=2", rewriteAfterParam("/v1/files?limit=2", "native-1"))
	require.Equal(t, "/v1/files?limit=2", rewriteAfterParam("/v1/files?limit=2&after=old", ""))
	require.Equal(t, "/v1/files", rewriteAfterParam("/v1/files", ""))
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
