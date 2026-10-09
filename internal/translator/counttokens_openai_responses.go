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

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewResponsesInputTokensOpenAIToOpenAITranslator creates a passthrough translator for OpenAI /v1/responses/input_tokens.
func NewResponsesInputTokensOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIResponsesInputTokensTranslator {
	return &responsesInputTokensToOpenAITranslator{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "responses/input_tokens"),
	}
}

type responsesInputTokensToOpenAITranslator struct {
	modelNameOverride internalapi.ModelNameOverride
	path              string
}

// RequestBody implements [OpenAIResponsesInputTokensTranslator.RequestBody].
func (t *responsesInputTokensToOpenAITranslator) RequestBody(original []byte, _ *openai.ResponseRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if t.modelNameOverride != "" {
		newBody, err = sjson.SetBytesOptions(original, "model", t.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
	}

	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)

	newHeaders = []internalapi.Header{{pathHeaderName, t.path}}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [OpenAIResponsesInputTokensTranslator.ResponseHeaders].
func (t *responsesInputTokensToOpenAITranslator) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [OpenAIResponsesInputTokensTranslator.ResponseBody].
func (t *responsesInputTokensToOpenAITranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.ResponsesInputTokensSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	resp := &openai.ResponsesInputTokensResponse{}
	if err := json.NewDecoder(body).Decode(resp); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	if span != nil {
		span.RecordResponse(resp)
	}
	tokenUsage.SetInputTokens(uint32(resp.InputTokens)) //nolint:gosec
	return nil, nil, tokenUsage, "", nil
}

// ResponseError implements [OpenAIResponsesInputTokensTranslator.ResponseError].
func (t *responsesInputTokensToOpenAITranslator) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}
