// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// FilesRequest is the OpenAI-shaped, request context passed to a Files translator
// as the reused generic Translator's ReqT.
type FilesRequest struct {
	// NativeID is the backend-native file id, already substituted by the processor.
	NativeID string
	// Path is the request :path, already native-rewritten by the processor.
	Path string
	// Method is the request :method.
	Method string
	// ContentType is the request content-type (carries the multipart boundary for upload).
	ContentType string
	// Model is the upload routing model; "" for the other operations.
	Model string
	// Query is the list query with the native "after" cursor already substituted; "" otherwise.
	Query string
}

// FilesTranslator maps the OpenAI Files request/response shapes to a provider's file API.
// SpanT is `any` because files have no tracing span, so instantiating
// the generic here introduces NO dependency on internal/tracing.
type FilesTranslator = Translator[FilesRequest, any]

// NewFileUploadTranslator selects the POST /v1/files translator.
func NewFileUploadTranslator(schema filterapi.VersionedAPISchema) (FilesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIFileUploadTranslator{}, nil
	default:
		return nil, errUnsupportedFilesSchema(schema)
	}
}

// NewFileRetrieveTranslator selects the GET /v1/files/{id} translator.
func NewFileRetrieveTranslator(schema filterapi.VersionedAPISchema) (FilesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIFileRetrieveTranslator{}, nil
	default:
		return nil, errUnsupportedFilesSchema(schema)
	}
}

// NewFileContentTranslator selects the GET /v1/files/{id}/content translator.
func NewFileContentTranslator(schema filterapi.VersionedAPISchema) (FilesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIFileContentTranslator{}, nil
	default:
		return nil, errUnsupportedFilesSchema(schema)
	}
}

// NewFileDeleteTranslator selects the DELETE /v1/files/{id} translator.
func NewFileDeleteTranslator(schema filterapi.VersionedAPISchema) (FilesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIFileDeleteTranslator{}, nil
	default:
		return nil, errUnsupportedFilesSchema(schema)
	}
}

// NewFileListTranslator selects the GET /v1/files translator.
func NewFileListTranslator(schema filterapi.VersionedAPISchema) (FilesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return &openAIFileListTranslator{}, nil
	default:
		return nil, errUnsupportedFilesSchema(schema)
	}
}

// errUnsupportedFilesSchema is the selection error the processor maps to HTTP 501 when
// a Files route points at a backend whose file translation does not yet exist.
func errUnsupportedFilesSchema(schema filterapi.VersionedAPISchema) error {
	return fmt.Errorf("unsupported API schema for files: backend=%s", schema)
}
