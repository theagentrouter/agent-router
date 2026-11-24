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
	"net/http"
	"strconv"
	"testing"

	"github.com/andybalholm/brotli"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/headermutator"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// fakeAuthHandler records the headers/body it was called with and returns a bearer token, or err.
type fakeAuthHandler struct {
	err         error
	gotPath     string
	gotBody     []byte
	calledTimes int
}

func (f *fakeAuthHandler) Do(_ context.Context, headers map[string]string, body []byte) ([]internalapi.Header, error) {
	f.calledTimes++
	f.gotPath = headers[":path"]
	f.gotBody = body
	if f.err != nil {
		return nil, f.err
	}
	headers["Authorization"] = "Bearer sk-test"
	return []internalapi.Header{{"Authorization", "Bearer sk-test"}}, nil
}

var _ filterapi.BackendAuthHandler = (*fakeAuthHandler)(nil)

// overwriteHeaderValue returns the value of an OVERWRITE_IF_EXISTS_OR_ADD set header.
func overwriteHeaderValue(resp *extprocv3.ProcessingResponse, key string) (string, bool) {
	for _, h := range resp.GetRequestHeaders().GetResponse().GetHeaderMutation().GetSetHeaders() {
		if h.GetHeader().GetKey() == key && h.GetAppendAction() == corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD {
			return string(h.GetHeader().GetRawValue()), true
		}
	}
	return "", false
}

func TestApplyBackendAuth(t *testing.T) {
	t.Run("nil handler only strips upstream host", func(t *testing.T) {
		hm := &extprocv3.HeaderMutation{}
		resp, err := applyUpstreamBackendAuth(context.Background(), nil, nil, hm, nil)
		require.NoError(t, err)
		require.Nil(t, resp)
		require.Empty(t, hm.SetHeaders)
		require.Equal(t, []string{internalapi.UpstreamHostHeader}, hm.RemoveHeaders)
	})
	t.Run("handler sees translated path and its headers overwrite", func(t *testing.T) {
		h := &fakeAuthHandler{}
		hm, _ := mutationsFromTranslationResult([]internalapi.Header{{":path", "/translated"}}, nil)
		reqHeaders := map[string]string{":path": "/v1/files", "authorization": "Bearer client"}
		resp, err := applyUpstreamBackendAuth(context.Background(), h, reqHeaders, hm, []byte("body"))
		require.NoError(t, err)
		require.Nil(t, resp)
		require.Equal(t, "/translated", h.gotPath)
		require.Equal(t, []byte("body"), h.gotBody)
		got := map[string]corev3.HeaderValueOption_HeaderAppendAction{}
		for _, sh := range hm.SetHeaders {
			got[sh.Header.Key] = sh.AppendAction
		}
		require.Equal(t, corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD, got["Authorization"])
	})
	t.Run("missing credential is a local 401", func(t *testing.T) {
		h := &fakeAuthHandler{err: fmt.Errorf("wrap: %w", backendauth.ErrCredentialMissing)}
		resp, err := applyUpstreamBackendAuth(context.Background(), h, map[string]string{}, &extprocv3.HeaderMutation{}, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, immediateStatus(resp))
	})
	t.Run("other handler error fails the request", func(t *testing.T) {
		h := &fakeAuthHandler{err: errors.New("boom")}
		_, err := applyUpstreamBackendAuth(context.Background(), h, map[string]string{}, &extprocv3.HeaderMutation{}, nil)
		require.ErrorContains(t, err, "failed to do auth request")
	})
}

func TestFiles_Upstream_AppliesBackendAuth(t *testing.T) {
	newBackend := func(h filterapi.BackendAuthHandler) *filterapi.RuntimeBackend {
		return &filterapi.RuntimeBackend{
			Backend: &filterapi.Backend{Name: "ns/apple", AIServiceBackendName: "ns/apple", Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}},
			Handler: h,
		}
	}
	t.Run("headers only", func(t *testing.T) {
		h := &fakeAuthHandler{}
		rp := &filesProcessor{
			logger:               slog.Default(),
			op:                   filesOpRetrieve,
			filesRequestMetadata: translator.FilesRequest{NativeID: "native-1", Path: "/v1/files/native-1", Method: http.MethodGet},
		}
		up := &filesProcessor{isUpstreamFilter: true, logger: slog.Default(), requestHeaders: map[string]string{}}
		require.NoError(t, up.SetBackend(context.Background(), newBackend(h), "route", rp))
		resp, err := up.ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
		require.NoError(t, err)
		require.Equal(t, 1, h.calledTimes)
		v, ok := overwriteHeaderValue(resp, "Authorization")
		require.True(t, ok)
		require.Equal(t, "Bearer sk-test", v)
		require.Contains(t, resp.GetRequestHeaders().GetResponse().GetHeaderMutation().GetRemoveHeaders(), internalapi.UpstreamHostHeader)
	})
	t.Run("missing credential is 401", func(t *testing.T) {
		h := &fakeAuthHandler{err: backendauth.ErrCredentialMissing}
		rp := &filesProcessor{logger: slog.Default(), op: filesOpRetrieve}
		up := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
		require.NoError(t, up.SetBackend(context.Background(), newBackend(h), "route", rp))
		resp, err := up.ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, immediateStatus(resp))
	})
}

func TestBatches_Upstream_AppliesBackendAuth(t *testing.T) {
	t.Run("create body is passed to the handler", func(t *testing.T) {
		h := &fakeAuthHandler{}
		up := newCreateUpstreamPair(t, &fakeBatchCreateTranslator{newBody: []byte(`{"x":1}`)}, "/v1/embeddings")
		up.handler = h
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, []byte(`{"x":1}`), h.gotBody)
		v, ok := overwriteHeaderValue(resp, "Authorization")
		require.True(t, ok)
		require.Equal(t, "Bearer sk-test", v)
	})
	t.Run("missing credential is 401", func(t *testing.T) {
		up := newCreateUpstreamPair(t, &fakeBatchCreateTranslator{}, "/v1/embeddings")
		up.handler = &fakeAuthHandler{err: backendauth.ErrCredentialMissing}
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, immediateStatus(resp))
	})
}

func TestBackendIdentity(t *testing.T) {
	routeName := internalapi.PerRouteRuleRefBackendName("route-ns", "openai", "chat", 0, 0)
	tests := []struct {
		name    string
		backend filterapi.Backend
		want    backendKey
		wantOK  bool
	}{
		{"canonical cross-namespace identity", filterapi.Backend{Name: routeName, AIServiceBackendName: "backend-ns/openai"}, backendKey{"backend-ns", "openai"}, true},
		{"missing canonical identity", filterapi.Backend{Name: routeName}, backendKey{}, false},
		{"malformed canonical identity", filterapi.Backend{Name: routeName, AIServiceBackendName: "openai"}, backendKey{}, false},
		{"canonical identity with extra segment", filterapi.Backend{Name: routeName, AIServiceBackendName: "ns/a/b"}, backendKey{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := backendIdentity(&tt.backend)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// crossNamespaceBackend returns a backend referenced from route-ns that targets backendNS/openai.
func crossNamespaceBackend(backendNS string, ref int) *filterapi.RuntimeBackend {
	return &filterapi.RuntimeBackend{Backend: &filterapi.Backend{
		Name:                 internalapi.PerRouteRuleRefBackendName("route-ns", "openai", "route", 0, ref),
		AIServiceBackendName: backendNS + "/openai",
		Schema:               filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
	}}
}

func TestSetBackend_CrossNamespaceIdentity(t *testing.T) {
	t.Run("files records canonical identity", func(t *testing.T) {
		rp := &filesProcessor{op: filesOpUpload, logger: slog.Default()}
		upstream := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
		require.NoError(t, upstream.SetBackend(context.Background(), crossNamespaceBackend("ns-a", 0), "route", rp))
		require.Equal(t, &backendKey{"ns-a", "openai"}, rp.backend)
	})
	t.Run("files rejects id minted for same-named backend in another namespace", func(t *testing.T) {
		rp := &filesProcessor{op: filesOpRetrieve, decodedBackend: &backendKey{"ns-a", "openai"}, logger: slog.Default()}
		upstream := &filesProcessor{isUpstreamFilter: true, logger: slog.Default()}
		err := upstream.SetBackend(context.Background(), crossNamespaceBackend("ns-b", 1), "route", rp)
		require.ErrorContains(t, err, "file id belongs to backend ns-a/openai, but the request was routed to ns-b/openai")
	})
	t.Run("batches records canonical identity", func(t *testing.T) {
		rp := &batchesProcessor{op: batchesOpList, logger: slog.Default()}
		upstream := &batchesProcessor{isUpstreamFilter: true, logger: slog.Default()}
		require.NoError(t, upstream.SetBackend(context.Background(), crossNamespaceBackend("ns-a", 0), "route", rp))
		require.Equal(t, &backendKey{"ns-a", "openai"}, rp.backend)
	})
	t.Run("batches rejects id minted for same-named backend in another namespace", func(t *testing.T) {
		rp := &batchesProcessor{op: batchesOpRetrieve, decodedBackend: &backendKey{"ns-a", "openai"}, logger: slog.Default()}
		upstream := &batchesProcessor{isUpstreamFilter: true, logger: slog.Default()}
		err := upstream.SetBackend(context.Background(), crossNamespaceBackend("ns-b", 1), "route", rp)
		require.ErrorContains(t, err, "id belongs to backend ns-a/openai, but the request was routed to ns-b/openai")
	})
}

// credentialOverrideAuthHandler mimics a credential-override handler: it reads the credential from
// the x-aigw-api-key input header and sets Authorization from it.
type credentialOverrideAuthHandler struct{}

func (credentialOverrideAuthHandler) Do(_ context.Context, headers map[string]string, _ []byte) ([]internalapi.Header, error) {
	key, ok := headers["x-aigw-api-key"]
	if !ok {
		return nil, backendauth.ErrCredentialMissing
	}
	return []internalapi.Header{{"Authorization", "Bearer " + key}}, nil
}

// credentialOverrideMutation is the backend header mutation the controller produces for a
// credential override, plus one configured set header.
var credentialOverrideMutation = &filterapi.HTTPHeaderMutation{
	Set:    []filterapi.HTTPHeader{{Name: "x-team", Value: "a"}, {Name: "Authorization", Value: "Bearer configured"}},
	Remove: []string{"x-aigw-api-key"},
}

// requireBackendHeaderMutationApplied checks that the credential input header is removed upstream,
// the configured header is set, and Authorization is derived from the override credential rather
// than overwritten by the configured set.
func requireBackendHeaderMutationApplied(t *testing.T, resp *extprocv3.ProcessingResponse) {
	t.Helper()
	require.Nil(t, resp.GetImmediateResponse())
	hm := resp.GetRequestHeaders().GetResponse().GetHeaderMutation()
	require.Contains(t, hm.GetRemoveHeaders(), "x-aigw-api-key")
	v, ok := overwriteHeaderValue(resp, "x-team")
	require.True(t, ok)
	require.Equal(t, "a", v)
	// Auth is applied after the configured sets, so its Authorization is the last one set.
	var last string
	for _, h := range hm.GetSetHeaders() {
		if h.GetHeader().GetKey() == "Authorization" {
			last = string(h.GetHeader().GetRawValue())
		}
	}
	require.Equal(t, "Bearer client-key", last)
}

func TestFiles_Upstream_AppliesBackendHeaderMutation(t *testing.T) {
	rp := &filesProcessor{
		logger:               slog.Default(),
		op:                   filesOpRetrieve,
		requestHeaders:       map[string]string{":path": "/v1/files/gw-id", "x-aigw-api-key": "client-key"},
		filesRequestMetadata: translator.FilesRequest{NativeID: "native-1", Path: "/v1/files/native-1", Method: http.MethodGet},
	}
	up := &filesProcessor{
		isUpstreamFilter: true,
		logger:           slog.Default(),
		requestHeaders:   map[string]string{":path": "/v1/files/gw-id", "x-aigw-api-key": "client-key"},
	}
	backend := &filterapi.RuntimeBackend{
		Backend: &filterapi.Backend{
			Name:                 internalapi.PerRouteRuleRefBackendName("ns", "apple", "route", 0, 0),
			AIServiceBackendName: "ns/apple",
			Schema:               filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
			HeaderMutation:       credentialOverrideMutation,
		},
		Handler: credentialOverrideAuthHandler{},
	}
	require.NoError(t, up.SetBackend(context.Background(), backend, "route", rp))
	resp, err := up.ProcessRequestHeaders(context.Background(), &corev3.HeaderMap{})
	require.NoError(t, err)
	requireBackendHeaderMutationApplied(t, resp)
}

func TestBatches_Upstream_AppliesBackendHeaderMutation(t *testing.T) {
	up := newCreateUpstreamPair(t, &fakeBatchCreateTranslator{newBody: []byte(`{"x":1}`)}, "/v1/embeddings")
	up.requestHeaders = map[string]string{":path": "/v1/batches", "x-aigw-api-key": "client-key"}
	up.handler = credentialOverrideAuthHandler{}
	up.headerMutator = headermutator.NewHeaderMutator(credentialOverrideMutation, up.requestHeaders)
	resp, err := up.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	requireBackendHeaderMutationApplied(t, resp)
}

func TestBatches_SetBackend_StoresHeaderMutator(t *testing.T) {
	rp := &batchesProcessor{op: batchesOpList, logger: slog.Default(), requestHeaders: map[string]string{}}
	up := &batchesProcessor{isUpstreamFilter: true, logger: slog.Default()}
	b := openAIBackend("ns", "apple", 0)
	b.Backend.HeaderMutation = credentialOverrideMutation
	require.NoError(t, up.SetBackend(context.Background(), b, "route", rp))
	require.NotNil(t, up.headerMutator)
}

// compressBody encodes body with the given content-encoding ("gzip" or "br").
func compressBody(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch encoding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "br":
		w = brotli.NewWriter(&buf)
	default:
		t.Fatalf("unsupported encoding %q", encoding)
	}
	_, err := w.Write(body)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// requireContentEncodingRemoved checks that the response replaced the body and dropped
// content-encoding, since the rewritten body is sent uncompressed.
func requireContentEncodingRemoved(t *testing.T, resp *extprocv3.ProcessingResponse) []byte {
	t.Helper()
	require.Nil(t, resp.GetImmediateResponse())
	common := resp.GetResponseBody().GetResponse()
	require.NotNil(t, common.GetBodyMutation())
	require.Contains(t, common.GetHeaderMutation().GetRemoveHeaders(), "content-encoding")
	body := common.GetBodyMutation().GetBody()
	cl, ok := responseBodyHeaderValue(resp, "content-length")
	require.True(t, ok)
	require.Equal(t, strconv.Itoa(len(body)), cl)
	return body
}

func TestDecodeResponseBody(t *testing.T) {
	plain := []byte(`{"id":"x"}`)
	for _, enc := range []string{"gzip", "br"} {
		t.Run(enc, func(t *testing.T) {
			got, isEncoded, err := decodeResponseBody(compressBody(t, enc, plain), enc)
			require.NoError(t, err)
			require.True(t, isEncoded)
			require.Equal(t, plain, got)
		})
	}
	t.Run("identity", func(t *testing.T) {
		got, isEncoded, err := decodeResponseBody(plain, "")
		require.NoError(t, err)
		require.False(t, isEncoded)
		require.Equal(t, plain, got)
	})
	t.Run("corrupt gzip", func(t *testing.T) {
		_, _, err := decodeResponseBody([]byte("not gzip"), "gzip")
		require.Error(t, err)
	})
	t.Run("truncated gzip", func(t *testing.T) {
		gz := compressBody(t, "gzip", plain)
		_, _, err := decodeResponseBody(gz[:len(gz)-4], "gzip")
		require.Error(t, err)
	})
}

func TestWithDecodedBody_PassThroughKeepsEncoding(t *testing.T) {
	resp := withDecodedBody(passThroughResponseBody(), true)
	require.Nil(t, resp.GetResponseBody().GetResponse())
}

func TestBatches_ResponseBody_CompressedUpstream(t *testing.T) {
	raw := []byte(`{"id":"batch_1","object":"batch","input_file_id":"file-in","output_file_id":"file-out"}`)
	listRaw := []byte(`{"object":"list","data":[{"id":"batch_1"}],"first_id":"batch_1","last_id":"batch_1","has_more":false}`)
	for _, enc := range []string{"gzip", "br"} {
		t.Run(enc+" retrieve", func(t *testing.T) {
			codec := newTestCodec()
			p := newResponseBatchesProcessor(codec, batchesOpRetrieve, "200")
			p.responseHeaders["content-encoding"] = enc
			resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: compressBody(t, enc, raw)})
			require.NoError(t, err)
			body := requireContentEncodingRemoved(t, resp)
			decodeOrFail(t, codec, gjson.GetBytes(body, "id").String(), idcodec.KindBatch, "batch_1")
			decodeOrFail(t, codec, gjson.GetBytes(body, "output_file_id").String(), idcodec.KindFile, "file-out")
		})
		t.Run(enc+" list", func(t *testing.T) {
			codec := newTestCodec()
			p := newResponseBatchesProcessor(codec, batchesOpList, "200")
			p.responseHeaders["content-encoding"] = enc
			resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: compressBody(t, enc, listRaw)})
			require.NoError(t, err)
			body := requireContentEncodingRemoved(t, resp)
			decodeOrFail(t, codec, gjson.GetBytes(body, "data.0.id").String(), idcodec.KindBatch, "batch_1")
			decodeOrFail(t, codec, gjson.GetBytes(body, "last_id").String(), idcodec.KindBatch, "batch_1")
		})
	}
	t.Run("corrupt body is 502", func(t *testing.T) {
		p := newResponseBatchesProcessor(newTestCodec(), batchesOpRetrieve, "200")
		p.responseHeaders["content-encoding"] = "gzip"
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte("not gzip")})
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, immediateStatus(resp))
	})
	t.Run("error response passes through encoded", func(t *testing.T) {
		p := newResponseBatchesProcessor(newTestCodec(), batchesOpRetrieve, "404")
		p.responseHeaders["content-encoding"] = "gzip"
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: compressBody(t, "gzip", []byte(`{"error":{}}`))})
		require.NoError(t, err)
		require.Nil(t, resp.GetResponseBody().GetResponse())
	})
}

func TestFiles_ResponseBody_CompressedUpstream(t *testing.T) {
	newP := func(op filesOperation, status, enc string) (*filesProcessor, idcodec.Codec) {
		codec := newTestCodec()
		p := &filesProcessor{
			op: op, codec: codec, logger: slog.Default(), routingModel: "m1",
			backend:         &backendKey{namespace: "ns", name: "apple"},
			responseHeaders: map[string]string{":status": status, "content-encoding": enc},
		}
		require.NoError(t, p.resolveTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}))
		return p, codec
	}
	for _, enc := range []string{"gzip", "br"} {
		t.Run(enc+" retrieve", func(t *testing.T) {
			p, codec := newP(filesOpRetrieve, "200", enc)
			raw := []byte(`{"id":"file-native","object":"file","purpose":"batch"}`)
			resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: compressBody(t, enc, raw)})
			require.NoError(t, err)
			body := requireContentEncodingRemoved(t, resp)
			decodeOrFail(t, codec, gjson.GetBytes(body, "id").String(), idcodec.KindFile, "file-native")
		})
		t.Run(enc+" list", func(t *testing.T) {
			p, codec := newP(filesOpList, "200", enc)
			raw := []byte(`{"object":"list","data":[{"id":"file-native"}],"has_more":false}`)
			resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: compressBody(t, enc, raw)})
			require.NoError(t, err)
			body := requireContentEncodingRemoved(t, resp)
			decodeOrFail(t, codec, gjson.GetBytes(body, "data.0.id").String(), idcodec.KindFile, "file-native")
		})
	}
	t.Run("corrupt body is 502", func(t *testing.T) {
		p, _ := newP(filesOpRetrieve, "200", "gzip")
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte("not gzip")})
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, immediateStatus(resp))
	})
	t.Run("corrupt error body passes through", func(t *testing.T) {
		p, _ := newP(filesOpRetrieve, "500", "gzip")
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte("not gzip")})
		require.NoError(t, err)
		require.Nil(t, resp.GetResponseBody().GetResponse())
	})
	t.Run("content download keeps encoding", func(t *testing.T) {
		p, _ := newP(filesOpContent, "200", "gzip")
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: compressBody(t, "gzip", []byte("raw file"))})
		require.NoError(t, err)
		require.Nil(t, resp.GetResponseBody().GetResponse())
	})
}
