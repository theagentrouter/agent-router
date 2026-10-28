// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"errors"
	"fmt"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// ErrUnsupportedBatchEndpoint is returned (wrapped) by a create translator's RequestBody when the
// backend's provider cannot run batches for the requested endpoint. The processor answers it with
// a 400 invalid_request_error. Which endpoints are supported is decided per provider translator.
var ErrUnsupportedBatchEndpoint = errors.New("unsupported batch endpoint")

// BatchesRequest is the OpenAI-shaped request context passed to a Batches translator
// as the reused generic Translator's ReqT.
type BatchesRequest struct {
	// NativeID is the backend-native batch id, already substituted by the processor; "" for
	// create and list.
	NativeID string
	// Path is the request :path, already native-rewritten by the processor.
	Path string
	// Method is the request :method.
	Method string
	// Model is the routing model recorded in the gateway id (or the list "?model=" parameter).
	Model string
	// Query is the list query with the native "after" cursor already substituted; "" otherwise.
	Query string
	// Endpoint is the batch "endpoint" from the create request body; "" for other operations.
	Endpoint openai.BatchNewParamsEndpoint
}

// BatchesTranslator maps the OpenAI Batch API request/response shapes to a provider's batch API.
// SpanT is `any` because batches have no tracing span.
type BatchesTranslator = Translator[BatchesRequest, any]

// NewBatchCreateTranslator selects the POST /v1/batches translator.
//
// The create translator owns endpoint support: its RequestBody receives the requested endpoint in
// [BatchesRequest.Endpoint] and returns an error wrapping [ErrUnsupportedBatchEndpoint] when the
// provider cannot run batches for it. The OpenAI translator accepts every endpoint and leaves
// validation to the upstream.
func NewBatchCreateTranslator(schema filterapi.VersionedAPISchema) (BatchesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIBatchCreateTranslator{}, nil
	default:
		return nil, errUnsupportedBatchesSchema(schema)
	}
}

// NewBatchRetrieveTranslator selects the GET /v1/batches/{id} translator.
func NewBatchRetrieveTranslator(schema filterapi.VersionedAPISchema) (BatchesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIBatchRetrieveTranslator{}, nil
	default:
		return nil, errUnsupportedBatchesSchema(schema)
	}
}

// NewBatchCancelTranslator selects the POST /v1/batches/{id}/cancel translator.
func NewBatchCancelTranslator(schema filterapi.VersionedAPISchema) (BatchesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIBatchCancelTranslator{}, nil
	default:
		return nil, errUnsupportedBatchesSchema(schema)
	}
}

// NewBatchListTranslator selects the GET /v1/batches translator.
func NewBatchListTranslator(schema filterapi.VersionedAPISchema) (BatchesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIBatchListTranslator{}, nil
	default:
		return nil, errUnsupportedBatchesSchema(schema)
	}
}

// errUnsupportedBatchesSchema is the selection error the processor maps to HTTP 501 when
// a Batches route points at a backend whose batch translation does not yet exist.
func errUnsupportedBatchesSchema(schema filterapi.VersionedAPISchema) error {
	return fmt.Errorf("unsupported API schema for batches: backend=%s", schema)
}
