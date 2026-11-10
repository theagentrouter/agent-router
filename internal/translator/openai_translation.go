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

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewTranslationOpenAIToOpenAITranslator implements [OpenAIAudioTranslationTranslator]
// for OpenAI to OpenAI translation for audio translations.
func NewTranslationOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIAudioTranslationTranslator {
	return &openAIToOpenAITranslatorV1Translation{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "audio", "translations"),
	}
}

type openAIToOpenAITranslatorV1Translation struct {
	modelNameOverride internalapi.ModelNameOverride
	path              string
	requestModel      internalapi.RequestModel
	contentType       string
}

// RequestBody implements [OpenAIAudioTranslationTranslator.RequestBody].
func (o *openAIToOpenAITranslatorV1Translation) RequestBody(original []byte, req *openai.TranslationRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	o.requestModel = req.Model

	if o.modelNameOverride != "" && o.contentType != "" {
		var newContentType string
		var rewriteErr error
		newBody, newContentType, rewriteErr = rewriteMultipartModel(original, o.contentType, o.modelNameOverride)
		if rewriteErr != nil {
			return nil, nil, fmt.Errorf("failed to rewrite multipart model: %w", rewriteErr)
		}
		newHeaders = append(newHeaders,
			internalapi.Header{contentTypeHeaderName, newContentType},
			internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))},
		)
		o.requestModel = o.modelNameOverride
	}

	newHeaders = append(newHeaders, internalapi.Header{pathHeaderName, o.path})

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)

	if len(newBody) > 0 && o.modelNameOverride == "" {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [OpenAIAudioTranslationTranslator.ResponseHeaders].
func (o *openAIToOpenAITranslatorV1Translation) ResponseHeaders(_ map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [OpenAIAudioTranslationTranslator.ResponseBody].
func (o *openAIToOpenAITranslatorV1Translation) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.TranslationSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	responseModel = o.requestModel
	if span != nil {
		data, readErr := io.ReadAll(body)
		if readErr == nil {
			var resp openai.TranslationResponse
			if jsonErr := json.Unmarshal(data, &resp); jsonErr == nil {
				span.RecordResponse(&resp)
			} else {
				span.RecordResponse(&openai.TranslationResponse{
					Text: string(data),
				})
			}
		}
	}
	return
}

// ResponseError implements [OpenAIAudioTranslationTranslator.ResponseError].
func (o *openAIToOpenAITranslatorV1Translation) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}

// SetContentType sets the content-type from the original request for multipart parsing during model rewrite.
func (o *openAIToOpenAITranslatorV1Translation) SetContentType(ct string) {
	o.contentType = ct
}
