// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewEmbeddingOpenAIToAWSBedrockTranslator implements [Factory] for OpenAI to AWS Bedrock embedding translation.
func NewEmbeddingOpenAIToAWSBedrockTranslator(modelNameOverride internalapi.ModelNameOverride) OpenAIEmbeddingTranslator {
	return &openAIToAWSBedrockTranslatorV1Embedding{modelNameOverride: modelNameOverride}
}

// openAIToAWSBedrockTranslatorV1Embedding translates OpenAI embedding requests to AWS Bedrock InvokeModel requests.
type openAIToAWSBedrockTranslatorV1Embedding struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
	encodingFormat    *string
}

// RequestBody implements [OpenAIEmbeddingTranslator.RequestBody].
func (o *openAIToAWSBedrockTranslatorV1Embedding) RequestBody(_ []byte, req *openai.EmbeddingRequest, _ bool) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	model := req.Model
	if o.modelNameOverride != "" {
		model = o.modelNameOverride
	}
	o.requestModel = model
	o.encodingFormat = req.EncodingFormat

	if req.OfCompletion == nil {
		return nil, nil, fmt.Errorf("%w: AWS Bedrock Titan requires an input-based embedding request (messages not supported)", internalapi.ErrInvalidRequestBody)
	}

	var inputText string
	switch v := req.OfCompletion.Input.Value.(type) {
	case string:
		inputText = v
	case []string:
		if len(v) != 1 {
			return nil, nil, fmt.Errorf("%w: AWS Bedrock Titan does not support batch embeddings (got %d inputs)",
				internalapi.ErrInvalidRequestBody, len(v))
		}
		inputText = v[0]
	default:
		return nil, nil, fmt.Errorf("%w: unsupported input type %T", internalapi.ErrInvalidRequestBody, req.OfCompletion.Input.Value)
	}

	bedrockReq := awsbedrock.TitanEmbeddingRequest{
		InputText:  inputText,
		Dimensions: req.Dimensions,
	}

	mutatedBody, err = json.Marshal(bedrockReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal body: %w", err)
	}

	encodedModel := url.PathEscape(model)
	newHeaders = []internalapi.Header{
		{pathHeaderName, fmt.Sprintf("/model/%s/invoke", encodedModel)},
		{contentLengthHeaderName, strconv.Itoa(len(mutatedBody))},
	}
	return
}

// ResponseHeaders implements [OpenAIEmbeddingTranslator.ResponseHeaders].
func (o *openAIToAWSBedrockTranslatorV1Embedding) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	return nil, nil
}

// ResponseBody implements [OpenAIEmbeddingTranslator.ResponseBody].
// Decodes the InvokeModel response and converts it to an OpenAI EmbeddingResponse.
func (o *openAIToAWSBedrockTranslatorV1Embedding) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.EmbeddingsSpan) (
	newHeaders []internalapi.Header, mutatedBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	var titanResp awsbedrock.TitanEmbeddingResponse
	if err = json.NewDecoder(body).Decode(&titanResp); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal Titan embedding response: %w", err)
	}

	tokens := titanResp.InputTextTokenCount
	openaiResp := openai.EmbeddingResponse{
		Object: "list",
		Model:  o.requestModel,
		Data: []openai.Embedding{
			{
				Object:    "embedding",
				Index:     0,
				Embedding: encodeEmbedding(titanResp.Embedding, o.encodingFormat),
			},
		},
		Usage: openai.EmbeddingUsage{
			PromptTokens: tokens,
			TotalTokens:  tokens,
		},
	}

	mutatedBody, err = json.Marshal(openaiResp)
	if err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to marshal OpenAI embedding response: %w", err)
	}

	tokenUsage.SetInputTokens(uint32(tokens)) //nolint:gosec
	tokenUsage.SetTotalTokens(uint32(tokens)) //nolint:gosec

	if span != nil {
		span.RecordResponse(&openaiResp)
	}

	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(mutatedBody))}}
	responseModel = openaiResp.Model
	return
}

// ResponseError implements [OpenAIEmbeddingTranslator.ResponseError].
// Translate AWS Bedrock exceptions to OpenAI error type.
// The error type is stored in the "x-amzn-errortype" HTTP header for AWS error responses.
// If AWS Bedrock connection fails the error body is translated to OpenAI error type for events such as HTTP 503 or 504.
func (o *openAIToAWSBedrockTranslatorV1Embedding) ResponseError(
	respHeaders map[string]string,
	body io.Reader,
) ([]internalapi.Header, []byte, error) {
	statusCode := respHeaders[statusHeaderName]
	contentType := respHeaders[contentTypeHeaderName]
	awsErrorType := respHeaders[awsErrorTypeHeaderName]

	rawBody, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, fmt.Errorf("read error body: %w", err)
	}

	// Already OpenAI-style error — return nil headers to preserve original response headers.
	if isJSON(contentType) && awsErrorType == "" {
		return nil, rawBody, nil
	}

	var openaiErr openai.Error

	if isJSON(contentType) {
		openaiErr, err = translateBedrockJSONError(rawBody, awsErrorType, statusCode)
		if err != nil {
			return nil, nil, err
		}
	} else {
		openaiErr = buildGenericError(string(rawBody), statusCode)
	}

	mutatedBody, err := json.Marshal(openaiErr)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal error body: %w", err)
	}

	return buildHeaders(mutatedBody), mutatedBody, nil
}

func isJSON(contentType string) bool {
	return strings.Contains(contentType, jsonContentType)
}

func buildHeaders(body []byte) []internalapi.Header {
	return []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(body))},
	}
}

func translateBedrockJSONError(
	body []byte,
	errorType string,
	statusCode string,
) (openai.Error, error) {
	var bedrockErr awsbedrock.BedrockException
	if err := json.Unmarshal(body, &bedrockErr); err != nil {
		return openai.Error{}, fmt.Errorf("unmarshal bedrock error: %w", err)
	}

	return openai.Error{
		Type: "error",
		Error: openai.ErrorType{
			Type:    errorType,
			Message: bedrockErr.Message,
			Code:    &statusCode,
		},
	}, nil
}

func buildGenericError(message string, statusCode string) openai.Error {
	return openai.Error{
		Type: "error",
		Error: openai.ErrorType{
			Type:    awsBedrockBackendError,
			Message: message,
			Code:    &statusCode,
		},
	}
}
