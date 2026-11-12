// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"io"
	"path"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

// openAIBatchPassthrough implements the response-side [BatchesTranslator] methods as no-ops: the
// batches processor (internal/extproc) substitutes native ids on the way upstream and re-encodes ids
// on the raw response on the way back, so OpenAI to OpenAI needs no body translation. Each operation
// gets its own concrete type (embedding this one) whose RequestBody sets ":path" with the backend's
// configured OpenAI prefix (AIServiceBackend schema prefix).
type openAIBatchPassthrough struct {
	// batchesPath is the prefixed upstream batches collection path, e.g. "/v1/batches".
	batchesPath string
}

type (
	openAIBatchCreateTranslator   struct{ openAIBatchPassthrough }
	openAIBatchRetrieveTranslator struct{ openAIBatchPassthrough }
	openAIBatchCancelTranslator   struct{ openAIBatchPassthrough }
	openAIBatchListTranslator     struct{ openAIBatchPassthrough }
)

// newOpenAIBatchPassthrough returns the shared OpenAI batch translator state for the given prefix.
func newOpenAIBatchPassthrough(prefix string) openAIBatchPassthrough {
	return openAIBatchPassthrough{batchesPath: path.Join("/", prefix, "batches")}
}

var (
	_ BatchesTranslator = (*openAIBatchCreateTranslator)(nil)
	_ BatchesTranslator = (*openAIBatchRetrieveTranslator)(nil)
	_ BatchesTranslator = (*openAIBatchCancelTranslator)(nil)
	_ BatchesTranslator = (*openAIBatchListTranslator)(nil)
)

// RequestBody implements [Translator.RequestBody] for create: POST {prefix}/batches. A nil body
// keeps the processor's native-id-substituted body. Every batch endpoint is accepted, including ones
// the gateway does not know yet: an OpenAI-compatible upstream validates the endpoint itself.
func (t *openAIBatchCreateTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return pathHeader(t.batchesPath + requestQuery(req.Path)), nil, nil
}

// RequestBody implements [Translator.RequestBody] for retrieve: GET {prefix}/batches/{native_id}.
func (t *openAIBatchRetrieveTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return pathHeader(t.batchesPath + "/" + req.NativeID + requestQuery(req.Path)), nil, nil
}

// RequestBody implements [Translator.RequestBody] for cancel: POST {prefix}/batches/{native_id}/cancel.
func (t *openAIBatchCancelTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return pathHeader(t.batchesPath + "/" + req.NativeID + "/cancel" + requestQuery(req.Path)), nil, nil
}

// RequestBody implements [Translator.RequestBody] for list: GET {prefix}/batches with the
// native-cursor-substituted query (the routing-only "model" parameter is already removed).
func (t *openAIBatchListTranslator) RequestBody(_ []byte, req *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return pathHeader(t.batchesPath + requestQuery(req.Path)), nil, nil
}

// ResponseHeaders implements [Translator.ResponseHeaders]. The OpenAI response headers need no
// translation.
func (openAIBatchPassthrough) ResponseHeaders(map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [Translator.ResponseBody]. The body is already OpenAI-shaped, so it
// passes through unchanged. Token usage is reported zero: batch usage is produced asynchronously by
// the upstream and is not attributable to this management request.
func (openAIBatchPassthrough) ResponseBody(_ map[string]string, _ io.Reader, _ bool, _ any) (
	newHeaders []internalapi.Header,
	mutatedBody []byte,
	tokenUsage metrics.TokenUsage,
	responseModel internalapi.ResponseModel,
	err error,
) {
	return nil, nil, metrics.TokenUsage{}, "", nil
}

// ResponseError implements [Translator.ResponseError]. OpenAI error envelopes pass through unchanged.
func (openAIBatchPassthrough) ResponseError(_ map[string]string, _ io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return nil, nil, nil
}
