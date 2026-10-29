// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	anthropicBackendError      = "AnthropicBackendError"
	anthropicDefaultVersion    = "2023-06-01"
	anthropicVersionHeaderName = "anthropic-version"
)

// NewChatCompletionOpenAIToAnthropicTranslator implements [Factory] for direct Anthropic translation.
func NewChatCompletionOpenAIToAnthropicTranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIChatCompletionTranslator {
	return &openAIToAnthropicTranslatorV1ChatCompletion{
		openAIToGCPAnthropicTranslatorV1ChatCompletion: openAIToGCPAnthropicTranslatorV1ChatCompletion{
			modelNameOverride: modelNameOverride,
		},
		path: path.Join("/", prefix, "messages"),
	}
}

// openAIToAnthropicTranslatorV1ChatCompletion reuses the GCP Anthropic implementation,
// overriding direct-provider request construction, error labeling, and response-model reporting.
type openAIToAnthropicTranslatorV1ChatCompletion struct {
	openAIToGCPAnthropicTranslatorV1ChatCompletion
	path string
}

// RequestBody implements [OpenAIChatCompletionTranslator.RequestBody].
func (o *openAIToAnthropicTranslatorV1ChatCompletion) RequestBody(_ []byte, openAIReq *openai.ChatCompletionRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	params, err := buildAnthropicParams(openAIReq, filterapi.APISchemaAnthropic, o.modelNameOverride)
	if err != nil {
		return
	}
	if params.MaxTokens == 0 {
		err = fmt.Errorf("%w: max_tokens is required by the Anthropic Messages API", internalapi.ErrInvalidRequestBody)
		return
	}

	o.requestModel = openAIReq.Model
	if o.modelNameOverride != "" {
		o.requestModel = o.modelNameOverride
	}
	params.Model = o.requestModel

	newBody, err = json.Marshal(params)
	if err != nil {
		return
	}
	if openAIReq.Stream {
		newBody, err = sjson.SetBytes(newBody, "stream", true)
		if err != nil {
			return
		}
		o.streamParser = newAnthropicStreamParser(o.requestModel)
		o.streamParser.useResponseModel = true
	}

	newHeaders = []internalapi.Header{
		{pathHeaderName, o.path},
		{anthropicVersionHeaderName, anthropicDefaultVersion},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseError uses the GCP Anthropic error envelope with a direct-provider fallback type.
func (o *openAIToAnthropicTranslatorV1ChatCompletion) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	newHeaders, newBody, err = o.openAIToGCPAnthropicTranslatorV1ChatCompletion.ResponseError(respHeaders, body)
	if err == nil && !strings.Contains(respHeaders[contentTypeHeaderName], jsonContentType) {
		newBody, err = sjson.SetBytes(newBody, "error.type", anthropicBackendError)
		if err == nil {
			newHeaders = []internalapi.Header{
				{contentTypeHeaderName, jsonContentType},
				{contentLengthHeaderName, strconv.Itoa(len(newBody))},
			}
		}
	}
	return
}
