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

// NewDecisionsOpenAIToOpenAITranslator creates an OpenAI Decisions passthrough
// translator with optional path prefixing and model virtualization.
func NewDecisionsOpenAIToOpenAITranslator(prefix string, modelNameOverride internalapi.ModelNameOverride) OpenAIDecisionsTranslator {
	return &openAIToOpenAITranslatorV1Decisions{
		modelNameOverride: modelNameOverride,
		path:              path.Join("/", prefix, "decisions"),
	}
}

type openAIToOpenAITranslatorV1Decisions struct {
	modelNameOverride internalapi.ModelNameOverride
	path              string
	requestModel      internalapi.RequestModel
}

// RequestBody preserves the Decisions wire format and changes only the model
// when a route configured model virtualization.
func (o *openAIToOpenAITranslatorV1Decisions) RequestBody(original []byte, req *openai.DecisionRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	o.requestModel = req.Model
	if o.modelNameOverride != "" {
		newBody, err = sjson.SetBytesOptions(original, "model", o.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model: %w", err)
		}
		o.requestModel = o.modelNameOverride
	}

	newHeaders = []internalapi.Header{{pathHeaderName, o.path}}
	newBody = forceOriginalBodyIfEmpty(forceBodyMutation, newBody, original)
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

func (*openAIToOpenAITranslatorV1Decisions) ResponseHeaders(map[string]string) ([]internalapi.Header, error) {
	return nil, nil
}

// ResponseBody decodes the response for tracing and token accounting but leaves
// the upstream bytes untouched.
func (o *openAIToOpenAITranslatorV1Decisions) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.DecisionsSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	var resp openai.DecisionResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, nil, tokenUsage, o.requestModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}
	if span != nil {
		span.RecordResponse(&resp)
	}
	responseModel = resp.Model
	if responseModel == "" {
		responseModel = o.requestModel
	}
	if resp.Usage != nil {
		tokenUsage.SetInputTokens(uint32(resp.Usage.InputTokens))                                             // #nosec G115
		tokenUsage.SetOutputTokens(uint32(resp.Usage.OutputTokens))                                           // #nosec G115
		tokenUsage.SetTotalTokens(uint32(resp.Usage.TotalTokens))                                             // #nosec G115
		tokenUsage.SetCachedInputTokens(uint32(resp.Usage.InputTokensDetails.CachedTokens))                   // #nosec G115
		tokenUsage.SetCacheCreationInputTokens(uint32(resp.Usage.InputTokensDetails.CacheWriteTokensValue())) // #nosec G115
		tokenUsage.SetReasoningTokens(uint32(resp.Usage.OutputTokensDetails.ReasoningTokens))                 // #nosec G115
	}
	return nil, nil, tokenUsage, responseModel, nil
}

func (*openAIToOpenAITranslatorV1Decisions) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return convertErrorOpenAIToOpenAIError(respHeaders, body)
}
