// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"io"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

// openAIBatchPassthrough implements every [BatchesTranslator] method as a no-op: the batches
// processor (internal/extproc) substitutes native ids on the way upstream and re-encodes ids on the
// raw response on the way back, so OpenAI to OpenAI needs no translation. Each operation gets its
// own concrete type (embedding this one) so provider-specific translators can diverge per operation.
type openAIBatchPassthrough struct{}

type (
	openAIBatchCreateTranslator   struct{ openAIBatchPassthrough }
	openAIBatchRetrieveTranslator struct{ openAIBatchPassthrough }
	openAIBatchCancelTranslator   struct{ openAIBatchPassthrough }
	openAIBatchListTranslator     struct{ openAIBatchPassthrough }
)

var (
	_ BatchesTranslator = (*openAIBatchCreateTranslator)(nil)
	_ BatchesTranslator = (*openAIBatchRetrieveTranslator)(nil)
	_ BatchesTranslator = (*openAIBatchCancelTranslator)(nil)
	_ BatchesTranslator = (*openAIBatchListTranslator)(nil)
)

// RequestBody implements [Translator.RequestBody]. Returning a nil body keeps the processor's
// canonical (native-id-substituted) request unchanged; returning nil headers leaves :path/:method
// as the processor set them. For create, every batch endpoint is accepted, including ones the
// gateway does not know yet: an OpenAI-compatible upstream validates the endpoint itself.
func (openAIBatchPassthrough) RequestBody(_ []byte, _ *BatchesRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return nil, nil, nil
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
