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

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
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
			Backend: &filterapi.Backend{Name: "ns/apple", Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}},
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
