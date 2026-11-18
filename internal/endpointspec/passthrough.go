// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package endpointspec

import (
	"fmt"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

type (
	// PassthroughEndpointSpec implements EndpointSpec for JSON operations the
	// backend serves in the client's own format (vLLM /v1/rerank, /v1/score,
	// /tokenize, /detokenize, /pooling). The body is forwarded unchanged apart
	// from the model name override and is never stream-processed.
	PassthroughEndpointSpec struct{}
	// ChatCompletionsBatchEndpointSpec implements EndpointSpec for vLLM's
	// /v1/chat/completions/batch, whose messages field is a list of
	// conversations.
	ChatCompletionsBatchEndpointSpec struct{ PassthroughEndpointSpec }
)

// ParseBody implements [EndpointSpec.ParseBody].
func (PassthroughEndpointSpec) ParseBody(body []byte, _ bool) (internalapi.OriginalModel, *translator.PassthroughRequest, bool, []byte, error) {
	var req translator.PassthroughRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON request: %w", internalapi.ErrMalformedRequest, err)
	}
	if req.Model == "" {
		return "", nil, false, nil, fmt.Errorf("%w: model is required", internalapi.ErrMalformedRequest)
	}
	return req.Model, &req, false, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (PassthroughEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *translator.PassthroughRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator]. The backend API
// schema does not change a passthrough body, so every schema is accepted.
func (PassthroughEndpointSpec) GetTranslator(_ filterapi.VersionedAPISchema, modelNameOverride string) (translator.PassthroughTranslator, error) {
	return translator.NewPassthroughTranslator(modelNameOverride), nil
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
// The parsed view holds only the model name.
func (PassthroughEndpointSpec) RedactSensitiveInfoFromRequest(req *translator.PassthroughRequest) (*translator.PassthroughRequest, error) {
	return req, nil
}
