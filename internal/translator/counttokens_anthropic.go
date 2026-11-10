// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"strconv"

	"github.com/tidwall/sjson"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewCountTokensToAnthropicTranslator creates a passthrough translator for Anthropic count_tokens.
func NewCountTokensToAnthropicTranslator(modelNameOverride internalapi.ModelNameOverride) AnthropicCountTokensTranslator {
	return &countTokensToAnthropicTranslator{modelNameOverride: modelNameOverride}
}

type countTokensToAnthropicTranslator struct {
	modelNameOverride internalapi.ModelNameOverride
}

// RequestBody implements [AnthropicCountTokensTranslator.RequestBody].
func (t *countTokensToAnthropicTranslator) RequestBody(original []byte, _ *anthropicschema.CountTokensRequest, forceBodyMutation bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if t.modelNameOverride != "" {
		newBody, err = sjson.SetBytesOptions(original, "model", t.modelNameOverride, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set model name: %w", err)
		}
	}

	if forceBodyMutation && len(newBody) == 0 {
		newBody = original
	}

	newHeaders = []internalapi.Header{{pathHeaderName, anthropicCountTokensPath}}
	if len(newBody) > 0 {
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}
	return
}

// ResponseHeaders implements [AnthropicCountTokensTranslator.ResponseHeaders].
func (t *countTokensToAnthropicTranslator) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [AnthropicCountTokensTranslator.ResponseBody].
func (t *countTokensToAnthropicTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.CountTokensSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	resp := &anthropicschema.CountTokensResponse{}
	if err := json.NewDecoder(body).Decode(resp); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	if span != nil {
		span.RecordResponse(resp)
	}
	tokenUsage.SetInputTokens(uint32(resp.InputTokens)) //nolint:gosec
	return nil, nil, tokenUsage, "", nil
}

// ResponseError implements [AnthropicCountTokensTranslator.ResponseError].
func (t *countTokensToAnthropicTranslator) ResponseError(_ map[string]string, _ io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	// Passthrough — Anthropic error format is already correct.
	return nil, nil, nil
}
