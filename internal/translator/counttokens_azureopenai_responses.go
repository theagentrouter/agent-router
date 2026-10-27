// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// NewResponsesInputTokensOpenAIToAzureOpenAITranslator implements [Factory] for OpenAI to Azure OpenAI translation
// for /v1/responses/input_tokens.
func NewResponsesInputTokensOpenAIToAzureOpenAITranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesInputTokensTranslator {
	return &openAIToAzureOpenAITranslatorV1ResponsesInputTokens{
		apiVersion: apiVersion,
		responsesInputTokensToOpenAITranslator: responsesInputTokensToOpenAITranslator{
			modelNameOverride: modelNameOverride,
		},
	}
}

type openAIToAzureOpenAITranslatorV1ResponsesInputTokens struct {
	apiVersion string
	responsesInputTokensToOpenAITranslator
}

// RequestBody implements [OpenAIResponsesInputTokensTranslator.RequestBody].
func (o *openAIToAzureOpenAITranslatorV1ResponsesInputTokens) RequestBody(raw []byte, req *openai.ResponseRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	newHeaders, newBody, err = o.responsesInputTokensToOpenAITranslator.RequestBody(raw, req, forceBodyMutation)
	if err != nil {
		return nil, nil, err
	}

	p := newHeaders[0].Value()
	if p == "" {
		p = "/openai/responses/input_tokens"
	}
	newHeaders[0] = internalapi.Header{pathHeaderName, appendAzureOpenAIAPIVersion(p, o.apiVersion)}
	return
}
