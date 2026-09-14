// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewTokenizeToAWSAnthropicTranslator creates a translator for tokenize requests
// to AWS Bedrock CountTokens API using the InvokeModel format.
func NewTokenizeToAWSAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) TokenizeTranslator {
	return &ToAWSAnthropicV1Tokenize{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

// ToAWSAnthropicV1Tokenize translates OpenAI tokenize requests to AWS Bedrock CountTokens
// API using the InvokeModel format: {"input":{"invokeModel":{"body":"<base64>"}}}
type ToAWSAnthropicV1Tokenize struct {
	apiVersion        string
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
}

// buildAnthropicBody converts an OpenAI tokenize chat request to an Anthropic InvokeModel body.
// The result is JSON suitable for base64-encoding into the InvokeModel wrapper.
func (o *ToAWSAnthropicV1Tokenize) buildAnthropicBody(chatReq *tokenize.ChatRequest, model internalapi.RequestModel) ([]byte, error) {
	countTokensParam, err := openAIToAnthropicCountTokensParams(chatReq, model)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(countTokensParam)
	if err != nil {
		return nil, fmt.Errorf("error marshaling Anthropic request: %w", err)
	}

	// Set anthropic_version (required by Bedrock InvokeModel body validation).
	anthropicVersion := BedrockDefaultVersion
	if o.apiVersion != "" {
		anthropicVersion = o.apiVersion
	}
	body, err = sjson.SetBytesOptions(body, anthropicVersionKey, anthropicVersion, sjsonOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to set anthropic_version: %w", err)
	}

	// Bedrock validates the InvokeModel body as a real request. Anthropic requires
	// max_tokens, but tokenize clients don't send it. Add a default.
	body, err = sjson.SetBytesOptions(body, "max_tokens", 1, sjsonOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to set max_tokens: %w", err)
	}

	// Model goes in the URL path, not the body.
	body, _ = sjson.DeleteBytesOptions(body, "model", sjsonOptionsInPlace)

	return body, nil
}

// RequestBody implements [TokenizeTranslator.RequestBody] for AWS Anthropic.
func (o *ToAWSAnthropicV1Tokenize) RequestBody(_ []byte, tokenizeReq *tokenize.RequestUnion, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	if err = tokenizeReq.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid tokenize request: %w", err)
	}

	if tokenizeReq.ChatRequest != nil {
		o.requestModel = tokenizeReq.ChatRequest.Model
	} else if tokenizeReq.CompletionRequest != nil {
		o.requestModel = tokenizeReq.CompletionRequest.Model
		tokenizeReq.ChatRequest = &tokenize.ChatRequest{
			Model: tokenizeReq.CompletionRequest.Model,
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: tokenizeReq.Prompt},
				}},
			},
		}
		tokenizeReq.CompletionRequest = nil
	}

	if o.modelNameOverride != "" {
		o.requestModel = o.modelNameOverride
	}

	// Bedrock CountTokens rejects cross-region inference IDs, so the helper strips any
	// geography prefix and builds the /model/{modelId}/count-tokens path.
	path := awsAnthropicCountTokensPath(o.requestModel)

	anthropicBody, err := o.buildAnthropicBody(tokenizeReq.ChatRequest, o.requestModel)
	if err != nil {
		return nil, nil, fmt.Errorf("error building Anthropic body: %w", err)
	}

	// Wrap in InvokeModel format: base64-encode the Anthropic body.
	countTokensReq := &awsbedrock.CountTokensInvokeModelRequest{}
	countTokensReq.Input.InvokeModel.Body = base64.StdEncoding.EncodeToString(anthropicBody)
	newBody, err = json.Marshal(countTokensReq)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling count tokens request: %w", err)
	}

	newHeaders = []internalapi.Header{
		{pathHeaderName, path},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseError implements [TokenizeTranslator.ResponseError] for AWS Anthropic.
func (o *ToAWSAnthropicV1Tokenize) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	var openaiError openai.Error
	if v, ok := respHeaders[contentTypeHeaderName]; ok && strings.Contains(v, jsonContentType) {
		var bedrockError awsbedrock.BedrockException
		if err = json.NewDecoder(body).Decode(&bedrockError); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal error body: %w", err)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    respHeaders[awsErrorTypeHeaderName],
				Message: bedrockError.Message,
				Code:    &statusCode,
			},
		}
	} else {
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    awsBedrockBackendError,
				Message: string(buf),
				Code:    &statusCode,
			},
		}
	}
	newBody, err = json.Marshal(openaiError)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseHeaders implements [TokenizeTranslator.ResponseHeaders].
func (o *ToAWSAnthropicV1Tokenize) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [TokenizeTranslator.ResponseBody] for AWS Anthropic.
func (o *ToAWSAnthropicV1Tokenize) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.TokenizeSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	bedrockResp := &awsbedrock.CountTokensResponse{}
	if err = json.NewDecoder(body).Decode(bedrockResp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	responseModel = o.requestModel

	openAIResp := &tokenize.Response{
		Count: bedrockResp.InputTokens,
	}

	newBody, err = json.Marshal(openAIResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error marshaling response: %w", err)
	}

	if span != nil {
		span.RecordResponse(openAIResp)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}
