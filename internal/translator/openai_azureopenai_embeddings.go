// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"strconv"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// NewEmbeddingOpenAIToAzureOpenAITranslator implements [Factory] for OpenAI to Azure OpenAI translation
// for embeddings.
func NewEmbeddingOpenAIToAzureOpenAITranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAIEmbeddingTranslator {
	return &openAIToAzureOpenAITranslatorV1Embedding{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

// openAIToAzureOpenAITranslatorV1Embedding implements [OpenAIEmbeddingTranslator] for /embeddings.
type openAIToAzureOpenAITranslatorV1Embedding struct {
	apiVersion string
	openAIToOpenAITranslatorV1Embedding
}

// RequestBody implements [OpenAIEmbeddingTranslator.RequestBody].
func (o *openAIToAzureOpenAITranslatorV1Embedding) RequestBody(original []byte, req *openai.EmbeddingRequest, onRetry bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	modelName := req.Model
	if o.modelNameOverride != "" {
		// If modelName is set we override the model to be used for the request.
		newBody, err = sjson.SetBytesOptions(original, "model", o.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
		modelName = o.modelNameOverride
	}

	// Always set the path header to the embeddings endpoint so that the request is routed correctly.
	// Assume deployment_id is same as model name.
	pathTemplate := "/openai/deployments/%s/embeddings?api-version=%s"
	if onRetry && len(newBody) == 0 {
		newBody = original
	}
	newHeaders = []internalapi.Header{{pathHeaderName, fmt.Sprintf(pathTemplate, modelName, o.apiVersion)}}

	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}
