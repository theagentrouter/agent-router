// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

func encodeGatewayID(t *testing.T, codec idcodec.Codec, kind, ns, name, model, nativeID string) string {
	t.Helper()
	id, err := codec.Encode(&idcodec.BackendID{Namespace: ns, Name: name, Kind: kind, Model: model, NativeID: nativeID})
	require.NoError(t, err)
	return id
}

func newBatchesProcessorForPath(codec idcodec.Codec, method, path string) *batchesProcessor {
	return &batchesProcessor{
		codec:          codec,
		requestHeaders: map[string]string{":method": method, ":path": path},
		logger:         slog.Default(),
	}
}

func immediateStatus(resp *extprocv3.ProcessingResponse) int {
	if ir := resp.GetImmediateResponse(); ir != nil {
		return int(ir.Status.Code)
	}
	return 0
}

func openAIBackend(ns, name string, ref int) *filterapi.RuntimeBackend {
	return &filterapi.RuntimeBackend{Backend: &filterapi.Backend{
		Name:   internalapi.PerRouteRuleRefBackendName(ns, name, "route", 0, ref),
		Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
	}}
}

func TestClassifyBatchesRequest(t *testing.T) {
	tests := []struct {
		method, path string
		wantOp       batchesOperation
		wantID       string
		wantErr      bool
	}{
		{http.MethodPost, "/v1/batches", batchesOpCreate, "", false},
		{http.MethodGet, "/v1/batches?limit=2", batchesOpList, "", false},
		{http.MethodGet, "/prefix/v1/batches/", batchesOpList, "", false},
		{http.MethodGet, "/v1/batches/batch-x", batchesOpRetrieve, "batch-x", false},
		{http.MethodPost, "/v1/batches/batch-x/cancel", batchesOpCancel, "batch-x", false},
		{http.MethodDelete, "/v1/batches/batch-x", 0, "", true},
		{http.MethodGet, "/v1/batches/batch-x/cancel", 0, "", true},
		{http.MethodPost, "/v1/batches/batch-x", 0, "", true},
		{http.MethodPut, "/v1/batches", 0, "", true},
		{http.MethodGet, "/v1/batchesfoo", 0, "", true},
		{http.MethodGet, "/v1/files", 0, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			op, id, err := classifyBatchesRequest(tc.method, tc.path)
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

func TestBatches_RequestHeaders_UpstreamAndUnsupported(t *testing.T) {
	p := &batchesProcessor{isUpstreamFilter: true, requestHeaders: map[string]string{}, logger: slog.Default()}
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.IsType(t, &extprocv3.ProcessingResponse_RequestHeaders{}, resp.Response)
	require.Nil(t, resp.GetImmediateResponse())

	p = newBatchesProcessorForPath(newTestCodec(), http.MethodDelete, "/v1/batches/x")
	resp, err = p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, immediateStatus(resp))
}

func TestBatches_Create_DefersToBody(t *testing.T) {
	p := newBatchesProcessorForPath(newTestCodec(), http.MethodPost, "/v1/batches")
	resp, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, resp.GetRequestHeaders().Response.ClearRouteCache)
	_, ok := responseHeaderValue(resp, internalapi.ModelNameHeaderKeyDefault)
	require.False(t, ok)
}

func TestBatches_Create_RoutesByInputFile(t *testing.T) {
	codec := newTestCodec()
	fileID := encodeGatewayID(t, codec, idcodec.KindFile, "ns", "apple", "gpt-4o-mini", "file-native-1")
	p := newBatchesProcessorForPath(codec, http.MethodPost, "/v1/batches")
	_, err := p.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)

	body := `{"input_file_id":"` + fileID + `","endpoint":"/v1/chat/completions","completion_window":"24h","metadata":{"a":"b"}}`
	resp, err := p.ProcessRequestBody(context.Background(), &extprocv3.HttpBody{Body: []byte(body)})
	require.NoError(t, err)
	common := resp.GetRequestBody().Response
	require.True(t, common.ClearRouteCache)
	newBody := common.BodyMutation.GetBody()
	require.Equal(t, "file-native-1", gjson.GetBytes(newBody, "input_file_id").String())
	require.Equal(t, "b", gjson.GetBytes(newBody, "metadata.a").String())

	var model string
	for _, h := range common.HeaderMutation.SetHeaders {
		if h.Header.Key == internalapi.ModelNameHeaderKeyDefault {
			model = string(h.Header.RawValue)
		}
	}
	require.Equal(t, "gpt-4o-mini", model)
	require.Equal(t, &backendKey{namespace: "ns", name: "apple"}, p.decodedBackend)
	require.Equal(t, "gpt-4o-mini", p.routingModel)
}

func TestBatches_Create_Rejections(t *testing.T) {
	codec := newTestCodec()
	batchID := encodeGatewayID(t, codec, idcodec.KindBatch, "ns", "apple", "m1", "batch_native")
	noModel := encodeGatewayID(t, codec, idcodec.KindFile, "ns", "apple", "", "file-native")
	tests := map[string]string{
		"not json":              `nope`,
		"missing input file":    `{"endpoint":"/v1/embeddings"}`,
		"native file id":        `{"input_file_id":"file-abc","endpoint":"/v1/embeddings"}`,
		"batch id as file":      `{"input_file_id":"` + batchID + `","endpoint":"/v1/embeddings"}`,
		"file id without model": `{"input_file_id":"` + noModel + `","endpoint":"/v1/embeddings"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			p := newBatchesProcessorForPath(codec, http.MethodPost, "/v1/batches")
			p.op = batchesOpCreate
			resp, err := p.ProcessRequestBody(context.Background(), &extprocv3.HttpBody{Body: []byte(body)})
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, immediateStatus(resp))
		})
	}
}

func TestBatches_RequestBody_NonCreatePassThrough(t *testing.T) {
	p := newBatchesProcessorForPath(newTestCodec(), http.MethodPost, "/v1/batches/x/cancel")
	p.op = batchesOpCancel
	resp, err := p.ProcessRequestBody(context.Background(), &extprocv3.HttpBody{Body: []byte(`{}`)})
	require.NoError(t, err)
	require.Nil(t, resp.GetRequestBody().GetResponse())
}

func TestBatches_RetrieveAndCancel_RewritePath(t *testing.T) {
	codec := newTestCodec()
	id := encodeGatewayID(t, codec, idcodec.KindBatch, "ns", "apple", "m1", "batch_native")
	tests := []struct{ method, path, want string }{
		{http.MethodGet, "/v1/batches/" + id, "/v1/batches/batch_native"},
		{http.MethodPost, "/root/v1/batches/" + id + "/cancel?x=1", "/root/v1/batches/batch_native/cancel?x=1"},
	}
	for _, tc := range tests {
		p := newBatchesProcessorForPath(codec, tc.method, tc.path)
		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		path, _ := responseHeaderValue(resp, ":path")
		require.Equal(t, tc.want, path)
		model, _ := responseHeaderValue(resp, internalapi.ModelNameHeaderKeyDefault)
		require.Equal(t, "m1", model)
		orig, _ := responseHeaderValue(resp, originalPathHeader)
		require.Equal(t, tc.path, orig)
		require.Equal(t, &backendKey{namespace: "ns", name: "apple"}, p.decodedBackend)
	}
}

func TestBatches_Retrieve_InvalidOrWrongKindID(t *testing.T) {
	codec := newTestCodec()
	fileID := encodeGatewayID(t, codec, idcodec.KindFile, "ns", "apple", "m1", "file-native")
	for _, id := range []string{"batch_native", "batch-forged", fileID} {
		p := newBatchesProcessorForPath(codec, http.MethodGet, "/v1/batches/"+id)
		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, immediateStatus(resp))
	}
}

func TestBatches_List(t *testing.T) {
	codec := newTestCodec()

	t.Run("missing model", func(t *testing.T) {
		p := newBatchesProcessorForPath(codec, http.MethodGet, "/v1/batches")
		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, immediateStatus(resp))
	})
	t.Run("model stripped", func(t *testing.T) {
		p := newBatchesProcessorForPath(codec, http.MethodGet, "/v1/batches?model=m2&limit=3")
		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		path, _ := responseHeaderValue(resp, ":path")
		require.Equal(t, "/v1/batches?limit=3", path)
		require.Equal(t, "m2", p.routingModel)
	})
	t.Run("after cursor supplies model", func(t *testing.T) {
		after := encodeGatewayID(t, codec, idcodec.KindBatch, "ns", "apple", "m1", "batch_9")
		p := newBatchesProcessorForPath(codec, http.MethodGet, "/v1/batches?limit=2&after="+after)
		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		path, _ := responseHeaderValue(resp, ":path")
		require.Equal(t, "/v1/batches?after=batch_9&limit=2", path)
		require.Equal(t, "m1", p.routingModel)
		require.Equal(t, &backendKey{namespace: "ns", name: "apple"}, p.decodedBackend)
	})
	t.Run("file id as cursor rejected", func(t *testing.T) {
		after := encodeGatewayID(t, codec, idcodec.KindFile, "ns", "apple", "m1", "file-9")
		p := newBatchesProcessorForPath(codec, http.MethodGet, "/v1/batches?model=m1&after="+after)
		resp, err := p.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, immediateStatus(resp))
	})
}

func newResponseBatchesProcessor(codec idcodec.Codec, op batchesOperation, status string) *batchesProcessor {
	p := newBatchesProcessorForPath(codec, http.MethodGet, "/v1/batches")
	p.op = op
	p.routingModel = "m1"
	p.backend = &backendKey{namespace: "ns", name: "apple"}
	p.responseHeaders = map[string]string{":status": status}
	return p
}

func decodeOrFail(t *testing.T, codec idcodec.Codec, id, wantKind, wantNative string) {
	t.Helper()
	d, err := codec.Decode(id)
	require.NoError(t, err)
	require.Equal(t, idcodec.BackendID{Namespace: "ns", Name: "apple", Kind: wantKind, Model: "m1", NativeID: wantNative}, d)
}

func TestBatches_ResponseBody_ReEncodesIDs(t *testing.T) {
	codec := newTestCodec()
	p := newResponseBatchesProcessor(codec, batchesOpRetrieve, "200")
	raw := `{"id":"batch_1","object":"batch","status":"completed","input_file_id":"file-in","output_file_id":"file-out","error_file_id":null,"request_counts":{"total":2}}`
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(raw)})
	require.NoError(t, err)
	body := resp.GetResponseBody().Response.BodyMutation.GetBody()

	decodeOrFail(t, codec, gjson.GetBytes(body, "id").String(), idcodec.KindBatch, "batch_1")
	decodeOrFail(t, codec, gjson.GetBytes(body, "input_file_id").String(), idcodec.KindFile, "file-in")
	decodeOrFail(t, codec, gjson.GetBytes(body, "output_file_id").String(), idcodec.KindFile, "file-out")
	require.Equal(t, gjson.Null, gjson.GetBytes(body, "error_file_id").Type)
	require.Equal(t, int64(2), gjson.GetBytes(body, "request_counts.total").Int())

	cl, ok := responseBodyHeaderValue(resp, "content-length")
	require.True(t, ok)
	require.Equal(t, len(body), atoiOrFail(t, cl))
}

func atoiOrFail(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		require.True(t, c >= '0' && c <= '9')
		n = n*10 + int(c-'0')
	}
	return n
}

func TestBatches_ResponseBody_List(t *testing.T) {
	codec := newTestCodec()
	p := newResponseBatchesProcessor(codec, batchesOpList, "200")
	raw := `{"object":"list","data":[{"id":"batch_1","input_file_id":"file-a"},{"id":"batch_2","input_file_id":"file-b","output_file_id":"file-c"}],"first_id":"batch_1","last_id":"batch_2","has_more":true}`
	resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(raw)})
	require.NoError(t, err)
	body := resp.GetResponseBody().Response.BodyMutation.GetBody()

	decodeOrFail(t, codec, gjson.GetBytes(body, "data.0.id").String(), idcodec.KindBatch, "batch_1")
	decodeOrFail(t, codec, gjson.GetBytes(body, "data.1.output_file_id").String(), idcodec.KindFile, "file-c")
	decodeOrFail(t, codec, gjson.GetBytes(body, "first_id").String(), idcodec.KindBatch, "batch_1")
	decodeOrFail(t, codec, gjson.GetBytes(body, "last_id").String(), idcodec.KindBatch, "batch_2")
	require.Equal(t, gjson.GetBytes(body, "data.1.id").String(), gjson.GetBytes(body, "last_id").String())
	require.True(t, gjson.GetBytes(body, "has_more").Bool())
}

func TestBatches_ResponseBody_Failures(t *testing.T) {
	codec := newTestCodec()

	t.Run("non-2xx passes through", func(t *testing.T) {
		p := newResponseBatchesProcessor(codec, batchesOpRetrieve, "404")
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(`{"error":{}}`)})
		require.NoError(t, err)
		require.Nil(t, resp.GetResponseBody().GetResponse())
	})
	t.Run("upstream filter passes through", func(t *testing.T) {
		p := &batchesProcessor{isUpstreamFilter: true}
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(`{"id":"x"}`)})
		require.NoError(t, err)
		require.Nil(t, resp.GetResponseBody().GetResponse())
	})
	for name, tc := range map[string]struct {
		op   batchesOperation
		body string
		mod  func(*batchesProcessor)
	}{
		"backend unknown": {batchesOpRetrieve, `{"id":"x"}`, func(p *batchesProcessor) { p.backend = nil }},
		"empty body":      {batchesOpRetrieve, ``, nil},
		"missing id":      {batchesOpCancel, `{"status":"cancelling"}`, nil},
		"list not array":  {batchesOpList, `{"object":"list"}`, nil},
		"list item no id": {batchesOpList, `{"data":[{"status":"x"}]}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			p := newResponseBatchesProcessor(codec, tc.op, "200")
			if tc.mod != nil {
				tc.mod(p)
			}
			resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(tc.body)})
			require.NoError(t, err)
			require.Equal(t, http.StatusBadGateway, immediateStatus(resp))
		})
	}
	t.Run("encode failure", func(t *testing.T) {
		p := newResponseBatchesProcessor(errEncodeCodec{}, batchesOpRetrieve, "200")
		resp, err := p.ProcessResponseBody(context.Background(), &extprocv3.HttpBody{Body: []byte(`{"id":"x"}`)})
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, immediateStatus(resp))
	})
}

func TestBatches_ProcessResponseHeaders_Captures(t *testing.T) {
	p := newBatchesProcessorForPath(newTestCodec(), http.MethodGet, "/v1/batches")
	_, err := p.ProcessResponseHeaders(context.Background(), &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
		{Key: ":status", RawValue: []byte("201")},
	}})
	require.NoError(t, err)
	require.Equal(t, "201", p.responseHeaders[":status"])
}

func TestBatches_SetBackend(t *testing.T) {
	upstream := &batchesProcessor{isUpstreamFilter: true, logger: slog.Default()}

	t.Run("sets backend and translator", func(t *testing.T) {
		rp := &batchesProcessor{op: batchesOpCreate, decodedBackend: &backendKey{"ns", "apple"}, logger: slog.Default()}
		require.NoError(t, upstream.SetBackend(context.Background(), openAIBackend("ns", "apple", 0), "route", rp))
		require.Equal(t, &backendKey{"ns", "apple"}, rp.backend)
		require.NotNil(t, rp.translator)
	})
	t.Run("decoded backend mismatch", func(t *testing.T) {
		rp := &batchesProcessor{op: batchesOpRetrieve, decodedBackend: &backendKey{"ns", "orig"}, logger: slog.Default()}
		err := upstream.SetBackend(context.Background(), openAIBackend("ns", "other", 0), "route", rp)
		require.ErrorContains(t, err, "id belongs to backend ns/orig")
	})
	t.Run("multiple backends in rule", func(t *testing.T) {
		config := runtimeConfigWithBackends(t, [3]string{"ns", "a", "route"}, [3]string{"ns", "b", "route"})
		rp := &batchesProcessor{op: batchesOpList, config: config, logger: slog.Default()}
		err := upstream.SetBackend(context.Background(), openAIBackend("ns", "a", 0), "route", rp)
		require.ErrorContains(t, err, "exactly one backend per route rule")
	})
	t.Run("non-openai schema", func(t *testing.T) {
		rp := &batchesProcessor{op: batchesOpRetrieve, logger: slog.Default()}
		b := openAIBackend("ns", "apple", 0)
		b.Backend.Schema = filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}
		require.ErrorContains(t, upstream.SetBackend(context.Background(), b, "route", rp), "unsupported API schema for batches")
	})
	t.Run("wrong route processor type panics", func(t *testing.T) {
		require.Panics(t, func() {
			_ = upstream.SetBackend(context.Background(), openAIBackend("ns", "apple", 0), "route", &filesProcessor{})
		})
	})
}

func TestBatches_ResolveTranslator_UnsupportedOp(t *testing.T) {
	p := &batchesProcessor{op: batchesOperation(99)}
	require.Error(t, p.resolveTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}))
}

func TestNewBatchesProcessorFactory(t *testing.T) {
	f := NewBatchesProcessorFactory(newTestCodec())
	proc, err := f(nil, map[string]string{}, slog.Default(), true, false)
	require.NoError(t, err)
	bp, ok := proc.(*batchesProcessor)
	require.True(t, ok)
	require.True(t, bp.isUpstreamFilter)
}

// fakeBatchCreateTranslator is a create translator that rejects one endpoint, fails on another, and
// optionally rewrites the body, to exercise the processor's handling of translator results.
type fakeBatchCreateTranslator struct {
	translator.BatchesTranslator
	rejectEndpoint, failEndpoint openai.BatchNewParamsEndpoint
	newBody                      []byte
	got                          *translator.BatchesRequest
}

func (f *fakeBatchCreateTranslator) RequestBody(_ []byte, req *translator.BatchesRequest, _ bool) ([]internalapi.Header, []byte, error) {
	f.got = req
	switch req.Endpoint {
	case f.rejectEndpoint:
		return nil, nil, fmt.Errorf("provider X: %w: %s", translator.ErrUnsupportedBatchEndpoint, req.Endpoint)
	case f.failEndpoint:
		return nil, nil, errors.New("boom")
	}
	return nil, f.newBody, nil
}

// newCreateUpstreamPair returns an upstream batches processor linked to a router processor that has
// already processed a create body for the given endpoint.
func newCreateUpstreamPair(t *testing.T, tr translator.BatchesTranslator, endpoint string) *batchesProcessor {
	t.Helper()
	codec := newTestCodec()
	fileID := encodeGatewayID(t, codec, idcodec.KindFile, "ns", "apple", "m1", "file-native")
	rp := newBatchesProcessorForPath(codec, http.MethodPost, "/v1/batches")
	_, err := rp.ProcessRequestHeaders(context.Background(), nil)
	require.NoError(t, err)
	resp, err := rp.ProcessRequestBody(context.Background(), &extprocv3.HttpBody{
		Body: []byte(`{"input_file_id":"` + fileID + `","endpoint":"` + endpoint + `","completion_window":"24h"}`),
	})
	require.NoError(t, err)
	require.Nil(t, resp.GetImmediateResponse(), "router must not reject on endpoint")
	rp.translator = tr
	return &batchesProcessor{isUpstreamFilter: true, logger: slog.Default(), routeProcessor: rp}
}

func TestBatches_Upstream_EndpointCheckedByTranslator(t *testing.T) {
	fake := func() *fakeBatchCreateTranslator {
		return &fakeBatchCreateTranslator{
			rejectEndpoint: openai.BatchNewParamsEndpointV1Videos,
			failEndpoint:   openai.BatchNewParamsEndpointV1ImagesEdits,
		}
	}

	t.Run("unsupported endpoint is 400", func(t *testing.T) {
		up := newCreateUpstreamPair(t, fake(), "/v1/videos")
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, immediateStatus(resp))
		require.Contains(t, string(resp.GetImmediateResponse().GetBody()), "unsupported batch endpoint")
	})
	t.Run("other translator error fails the request", func(t *testing.T) {
		up := newCreateUpstreamPair(t, fake(), "/v1/images/edits")
		_, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.ErrorContains(t, err, "failed to transform batches request")
	})
	t.Run("user-facing translator error is 422", func(t *testing.T) {
		tr := fake()
		tr.failEndpoint = ""
		up := newCreateUpstreamPair(t, &userFacingErrTranslator{tr}, "/v1/embeddings")
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, immediateStatus(resp))
	})
	t.Run("supported endpoint passes request context", func(t *testing.T) {
		tr := fake()
		up := newCreateUpstreamPair(t, tr, "/v1/embeddings")
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Nil(t, resp.GetImmediateResponse())
		require.Nil(t, resp.GetRequestHeaders().GetResponse().GetBodyMutation())
		require.Equal(t, openai.BatchNewParamsEndpointV1Embeddings, tr.got.Endpoint)
		require.Equal(t, "m1", tr.got.Model)
	})
	t.Run("translator body replaces request body", func(t *testing.T) {
		tr := fake()
		tr.newBody = []byte(`{"translated":true}`)
		up := newCreateUpstreamPair(t, tr, "/v1/embeddings")
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		common := resp.GetRequestHeaders().GetResponse()
		require.Equal(t, extprocv3.CommonResponse_CONTINUE_AND_REPLACE, common.Status)
		require.JSONEq(t, `{"translated":true}`, string(common.BodyMutation.GetBody()))
		require.NotNil(t, resp.DynamicMetadata)
	})
	t.Run("openai accepts every endpoint", func(t *testing.T) {
		tr, err := translator.NewBatchCreateTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI})
		require.NoError(t, err)
		for _, ep := range []string{"/v1/moderations", "/v1/images/generations", "/v1/videos", "/v1/not-yet-known"} {
			up := newCreateUpstreamPair(t, tr, ep)
			resp, err := up.ProcessRequestHeaders(context.Background(), nil)
			require.NoError(t, err)
			require.Nil(t, resp.GetImmediateResponse(), ep)
		}
	})
	t.Run("non-create operations reach the translator without a body", func(t *testing.T) {
		codec := newTestCodec()
		id := encodeGatewayID(t, codec, idcodec.KindBatch, "ns", "apple", "m1", "batch_native")
		rp := newBatchesProcessorForPath(codec, http.MethodPost, "/v1/batches/"+id+"/cancel")
		_, err := rp.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		tr := fake()
		rp.translator = tr
		up := &batchesProcessor{isUpstreamFilter: true, logger: slog.Default(), routeProcessor: rp}
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Nil(t, resp.GetImmediateResponse())
		require.Nil(t, resp.GetRequestHeaders().GetResponse().GetBodyMutation())
		require.Equal(t, "batch_native", tr.got.NativeID)
		require.Equal(t, "/v1/batches/batch_native/cancel", tr.got.Path)
		require.Equal(t, "m1", tr.got.Model)
	})
	t.Run("no backend resolved is a no-op", func(t *testing.T) {
		up := &batchesProcessor{isUpstreamFilter: true, logger: slog.Default()}
		resp, err := up.ProcessRequestHeaders(context.Background(), nil)
		require.NoError(t, err)
		require.Nil(t, resp.GetRequestHeaders().GetResponse())
	})
}

// userFacingErrTranslator wraps a fake translator and fails RequestBody with a user-facing error.
type userFacingErrTranslator struct{ *fakeBatchCreateTranslator }

func (u *userFacingErrTranslator) RequestBody(_ []byte, req *translator.BatchesRequest, _ bool) ([]internalapi.Header, []byte, error) {
	u.got = req
	return nil, nil, fmt.Errorf("%w: bad field", internalapi.ErrInvalidRequestBody)
}
