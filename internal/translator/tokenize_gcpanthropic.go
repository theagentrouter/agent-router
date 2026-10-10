// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicVertex "github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	// Error type constants for GCP Anthropic responses
	gcpAnthropicBackendError = "GCPAnthropicBackendError"
)

// NewTokenizeToGCPAnthropicTranslator implements [Factory] for tokenize to GCP Anthropic translation.
func NewTokenizeToGCPAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) TokenizeTranslator {
	return &ToGCPAnthropicV1Tokenize{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

// ToGCPAnthropicV1Tokenize translates tokenize API requests to GCP Anthropic format.
// Converts OpenAI-compatible tokenize requests to GCP Anthropic Messages API format for token counting.
// Uses the count-tokens model with rawPredict method for token counting.
type ToGCPAnthropicV1Tokenize struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
	apiVersion        string
}

// anthropicTokensCountToResponse converts an Anthropic MessageTokensCount response to OpenAI tokenize format.
// Extracts the input token count from the token counting response.
func (o *ToGCPAnthropicV1Tokenize) anthropicTokensCountToResponse(anthropicResp *anthropic.MessageTokensCount) (*tokenize.Response, error) {
	tokenizeResp := &tokenize.Response{
		Count: int(anthropicResp.InputTokens),
	}

	return tokenizeResp, nil
}

// RequestBody implements [TokenizeTranslator.RequestBody] for GCP Anthropic.
// This method translates an OpenAI tokenize request to GCP Anthropic Messages format.
// Only the fields that affect the token count are mapped (messages, model, system, tools);
// see openAIToAnthropicCountTokensParams for why the remaining MessageCountTokensParams
// fields (cache_control, output_config, thinking, tool_choice) are intentionally omitted.
func (o *ToGCPAnthropicV1Tokenize) RequestBody(_ []byte, tokenizeReq *tokenize.RequestUnion, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Validate that the union has exactly one request type set
	if err = tokenizeReq.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid tokenize request: %w", err)
	}

	// Store the request model to use as fallback for response model.
	// If this is a CompletionRequest, convert the prompt to a single user message
	// since GCP Anthropic count-tokens only supports the Messages format.
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

	// GCP Vertex AI's count-tokens endpoint does not accept "@default" or "@latest"
	// version aliases. Strip them for count-tokens only.
	o.requestModel = strings.TrimSuffix(o.requestModel, "@default")
	o.requestModel = strings.TrimSuffix(o.requestModel, "@latest")

	// The GCP Anthropic count-tokens endpoint uses "count-tokens" as a virtual model name
	// in the path, while the actual Claude model name is specified in the request body.
	// See: https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/count-tokens
	path := buildGCPModelPathSuffix(gcpModelPublisherAnthropic, gcpCountTokensModel, gcpMethodRawPredict)

	anthropicReq, err := openAIToAnthropicCountTokensParams(tokenizeReq.ChatRequest, o.requestModel)
	if err != nil {
		return nil, nil, fmt.Errorf("error converting to Anthropic request: %w", err)
	}

	newBody, err = json.Marshal(anthropicReq)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling Anthropic messages request: %w", err)
	}

	// Add anthropic_version field (required by GCP)
	anthropicVersion := anthropicVertex.DefaultVersion
	if o.apiVersion != "" {
		anthropicVersion = o.apiVersion
	}
	newBody, err = sjson.SetBytes(newBody, "anthropic_version", anthropicVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set anthropic_version: %w", err)
	}

	newHeaders = []internalapi.Header{
		{pathHeaderName, path},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseError implements [TokenizeTranslator.ResponseError] for GCP Anthropic.
// Translate GCP Anthropic exceptions to OpenAI error type.
func (o *ToGCPAnthropicV1Tokenize) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	return translateGCPAnthropicErrorToOpenAI(respHeaders, body)
}

// ResponseHeaders implements [TokenizeTranslator.ResponseHeaders].
func (o *ToGCPAnthropicV1Tokenize) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [TokenizeTranslator.ResponseBody] for GCP Anthropic.
// This method translates a GCP Anthropic MessageTokensCount response to OpenAI tokenize format.
// GCP Anthropic uses deterministic model mapping without virtualization, where the requested model
// is exactly what gets executed. We extract token count from the token counting response.
func (o *ToGCPAnthropicV1Tokenize) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.TokenizeSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	anthropicResp := &anthropic.MessageTokensCount{}
	if err = json.NewDecoder(body).Decode(anthropicResp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	responseModel = o.requestModel

	// Convert to OpenAI format.
	openAIResp, err := o.anthropicTokensCountToResponse(anthropicResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error converting Anthropic response to OpenAI format: %w", err)
	}

	// Marshal the OpenAI response.
	newBody, err = json.Marshal(openAIResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error marshaling OpenAI response: %w", err)
	}

	if span != nil {
		span.RecordResponse(openAIResp)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// translateGCPAnthropicErrorToOpenAI translates GCP Anthropic error responses to OpenAI error format.
// GCP error responses typically contain JSON with error details or plain text error messages.
func translateGCPAnthropicErrorToOpenAI(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	var openaiError openai.Error
	var decodeErr error

	// Check for a JSON content type to decide how to parse the error.
	if v, ok := respHeaders[contentTypeHeaderName]; ok && strings.Contains(v, jsonContentType) {
		var gcpError anthropic.ErrorResponse
		if decodeErr = json.NewDecoder(body).Decode(&gcpError); decodeErr != nil {
			// If we expect JSON but fail to decode, it's an internal translator error.
			return nil, nil, fmt.Errorf("failed to unmarshal JSON error body: %w", decodeErr)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    gcpError.Error.Type,
				Message: gcpError.Error.Message,
				Code:    &statusCode,
			},
		}
	} else {
		// If not JSON, read the raw body as the error message.
		var buf []byte
		buf, decodeErr = io.ReadAll(body)
		if decodeErr != nil {
			return nil, nil, fmt.Errorf("failed to read raw error body: %w", decodeErr)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    gcpAnthropicBackendError,
				Message: string(buf),
				Code:    &statusCode,
			},
		}
	}

	// Marshal the translated OpenAI error.
	newBody, err = json.Marshal(openaiError)
	if err != nil {
		// This is an internal failure to create the response.
		return nil, nil, fmt.Errorf("failed to marshal OpenAI error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}
