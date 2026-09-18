// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// NewChatCompletionOpenAIToAzureOpenAITranslator implements [Factory] for OpenAI to Azure OpenAI translations.
// Except RequestBody method requires modification to satisfy Microsoft Azure OpenAI spec
// https://learn.microsoft.com/en-us/azure/ai-services/openai/reference#chat-completions, other interface methods
// are identical to NewChatCompletionOpenAIToOpenAITranslator's interface implementations.
func NewChatCompletionOpenAIToAzureOpenAITranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAIChatCompletionTranslator {
	return &openAIToAzureOpenAITranslatorV1ChatCompletion{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

// NewResponsesOpenAIToAzureOpenAITranslator implements [Factory] for OpenAI to Azure OpenAI translation
// for responses.
func NewResponsesOpenAIToAzureOpenAITranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesTranslator {
	return &openAIToAzureOpenAITranslatorV1Responses{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

// openAIToAzureOpenAITranslatorV1ChatCompletion adapts OpenAI requests for Azure OpenAI Service.
// Azure ignores the model field in the request body, using deployment name from the URI path instead:
// https://learn.microsoft.com/en-us/azure/ai-foundry/openai/reference#chat-completions
type openAIToAzureOpenAITranslatorV1ChatCompletion struct {
	apiVersion string
	openAIToOpenAITranslatorV1ChatCompletion
}

func (o *openAIToAzureOpenAITranslatorV1ChatCompletion) RequestBody(raw []byte, req *openai.ChatCompletionRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	modelName := req.Model
	if o.modelNameOverride != "" {
		// If modelName is set we override the model to be used for the request.
		modelName = o.modelNameOverride
	}
	// Ensure the response includes a model. This is set to accommodate test or
	// misimplemented backends.
	o.requestModel = modelName

	// Azure OpenAI uses a {deployment-id} that may match the deployed model's name.
	// We use the routed model as the deployment, stored in the path.
	pathTemplate := "/openai/deployments/%s/chat/completions?api-version=%s"
	newHeaders = []internalapi.Header{{pathHeaderName, fmt.Sprintf(pathTemplate, modelName, o.apiVersion)}}
	if req.Stream {
		o.stream = true
	}

	// On retry, the path might have changed to a different provider. So, this will ensure that the path is always set to OpenAI.
	if forceBodyMutation {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(raw))})
	}
	return
}

// openAIToAzureOpenAITranslatorV1Responses adapts OpenAI Responses requests for Azure OpenAI Service.
type openAIToAzureOpenAITranslatorV1Responses struct {
	apiVersion string
	openAIToOpenAITranslatorV1Responses
}

// RequestBody implements [OpenAIResponsesTranslator.RequestBody].
func (o *openAIToAzureOpenAITranslatorV1Responses) RequestBody(raw []byte, req *openai.ResponseRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	newHeaders, newBody, err = o.openAIToOpenAITranslatorV1Responses.RequestBody(raw, req, forceBodyMutation)
	if err != nil {
		return nil, nil, err
	}

	path := newHeaders[0].Value()
	if path == "" {
		path = "/openai/responses"
	}
	newHeaders[0] = internalapi.Header{pathHeaderName, appendAzureOpenAIAPIVersion(path, o.apiVersion)}
	return
}

func appendAzureOpenAIAPIVersion(path, apiVersion string) string {
	separator := "?"
	if strings.Contains(path, "?") && !strings.HasSuffix(path, "?") && !strings.HasSuffix(path, "&") {
		separator = "&"
	} else if strings.HasSuffix(path, "?") || strings.HasSuffix(path, "&") {
		separator = ""
	}
	return path + separator + "api-version=" + apiVersion
}
