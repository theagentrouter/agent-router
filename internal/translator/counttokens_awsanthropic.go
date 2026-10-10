// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewCountTokensToAWSAnthropicTranslator creates a translator for count_tokens to AWS Bedrock.
// AWS Bedrock has a dedicated CountTokens API at /model/{modelId}/count-tokens.
// The request wraps the Anthropic body in {"input":{"invokeModel":{"body":"<base64>"}}}
// and the response returns {"inputTokens": N}.
func NewCountTokensToAWSAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) AnthropicCountTokensTranslator {
	return &countTokensToAWSAnthropicTranslator{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

type countTokensToAWSAnthropicTranslator struct {
	apiVersion        string
	modelNameOverride internalapi.ModelNameOverride
}

// bedrockCountTokensResponse is the response from the AWS Bedrock CountTokens API.
type bedrockCountTokensResponse struct {
	InputTokens int64 `json:"inputTokens"`
}

// RequestBody implements [AnthropicCountTokensTranslator.RequestBody].
func (t *countTokensToAWSAnthropicTranslator) RequestBody(rawBody []byte, body *anthropicschema.CountTokensRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	model := cmp.Or(t.modelNameOverride, body.Model)

	// Build the Anthropic body for the InvokeModel format:
	// add anthropic_version, remove model and stream fields.
	invokeBody, err := sjson.SetBytesOptions(rawBody, anthropicVersionKey, t.apiVersion, sjsonOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set anthropic_version field: %w", err)
	}
	invokeBody, _ = sjson.DeleteBytesOptions(invokeBody, "model", sjsonOptionsInPlace)
	invokeBody, _ = sjson.DeleteBytesOptions(invokeBody, "stream", sjsonOptionsInPlace)

	// Bedrock's CountTokens with invokeModel format validates the body as if it were
	// a real InvokeModel request. Anthropic requires max_tokens, but count_tokens
	// clients don't send it. Add a default if missing.
	if !gjson.GetBytes(invokeBody, "max_tokens").Exists() {
		invokeBody, err = sjson.SetBytesOptions(invokeBody, "max_tokens", 1, sjsonOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set max_tokens: %w", err)
		}
	}

	// Wrap in Bedrock CountTokens request format:
	// {"input":{"invokeModel":{"body":"<base64-encoded invokeBody>"}}}
	encodedBody := base64.StdEncoding.EncodeToString(invokeBody)
	newBody = []byte("{}")
	newBody, err = sjson.SetBytesOptions(newBody, "input.invokeModel.body", encodedBody, sjsonOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to wrap body: %w", err)
	}

	// awsAnthropicCountTokensPath strips any cross-region inference prefix and builds
	// the /model/{modelId}/count-tokens path.
	path := awsAnthropicCountTokensPath(model)

	newHeaders = []internalapi.Header{{pathHeaderName, path}, {contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// ResponseHeaders implements [AnthropicCountTokensTranslator.ResponseHeaders].
func (t *countTokensToAWSAnthropicTranslator) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [AnthropicCountTokensTranslator.ResponseBody].
// The Bedrock CountTokens API returns {"inputTokens": N} which we convert to
// Anthropic format {"input_tokens": N}.
func (t *countTokensToAWSAnthropicTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.CountTokensSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	resp := &bedrockCountTokensResponse{}
	if err = json.NewDecoder(body).Decode(resp); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}

	// Convert Bedrock response to Anthropic format for the client.
	anthropicResp := &anthropicschema.CountTokensResponse{InputTokens: resp.InputTokens}
	if span != nil {
		span.RecordResponse(anthropicResp)
	}
	tokenUsage.SetInputTokens(uint32(resp.InputTokens)) //nolint:gosec

	// Return the Anthropic-format response body.
	newBody, err = json.Marshal(anthropicResp)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to marshal response: %w", err)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return newHeaders, newBody, tokenUsage, "", nil
}

// ResponseError implements [AnthropicCountTokensTranslator.ResponseError].
func (t *countTokensToAWSAnthropicTranslator) ResponseError(_ map[string]string, _ io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	return nil, nil, nil
}
