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
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewTokenizeToAWSBedrockTranslator implements [Factory] for tokenize to AWS Bedrock translation.
func NewTokenizeToAWSBedrockTranslator(modelNameOverride internalapi.ModelNameOverride) TokenizeTranslator {
	return &ToAWSBedrockV1Tokenize{
		modelNameOverride: modelNameOverride,
	}
}

// ToAWSBedrockV1Tokenize translates tokenize API requests to AWS Bedrock format.
// Converts OpenAI-compatible tokenize requests to AWS Bedrock Converse API format for token counting.
// Uses the Converse API with minimal token generation to extract input token usage statistics.
type ToAWSBedrockV1Tokenize struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
}

// tokenizeToBedrockCountTokens converts an OpenAI tokenize chat request to AWS Bedrock CountTokens format.
// AWS Bedrock has a dedicated CountTokens endpoint that counts input tokens without generating any output.
// The API expects: {"input": {"converse": {"messages": [...], "system": [...], "toolConfig": {...}}}}
func (o *ToAWSBedrockV1Tokenize) tokenizeToBedrockCountTokens(tokenizeChatReq *tokenize.ChatRequest) (*awsbedrock.CountTokensConverseRequest, error) {
	var converseInput awsbedrock.CountTokensConverseInput

	// Convert Chat Completion messages to Bedrock format using the shared converters
	// (see converse_helper.go) so the tokenize path counts exactly what the chat path sends.
	converseInput.Messages = make([]*awsbedrock.Message, 0, len(tokenizeChatReq.Messages))
	for i := range tokenizeChatReq.Messages {
		msg := &tokenizeChatReq.Messages[i]
		role := msg.ExtractMessgaeRole()
		switch {
		case msg.OfUser != nil:
			bedrockMessage, err := openAIMessageToBedrockMessageRoleUser(msg.OfUser, role)
			if err != nil {
				return nil, err
			}
			converseInput.Messages = append(converseInput.Messages, bedrockMessage)
		case msg.OfAssistant != nil:
			bedrockMessage, err := openAIMessageToBedrockMessageRoleAssistant(msg.OfAssistant, role)
			if err != nil {
				return nil, err
			}
			converseInput.Messages = append(converseInput.Messages, bedrockMessage)
		case msg.OfSystem != nil:
			if converseInput.System == nil {
				converseInput.System = make([]*awsbedrock.SystemContentBlock, 0)
			}
			if err := openAIMessageToBedrockMessageRoleSystem(msg.OfSystem, &converseInput.System); err != nil {
				return nil, err
			}
		case msg.OfTool != nil:
			bedrockMessage, err := openAIMessageToBedrockMessageRoleTool(msg.OfTool, awsbedrock.ConversationRoleUser)
			if err != nil {
				return nil, err
			}
			converseInput.Messages = append(converseInput.Messages, bedrockMessage)
		default:
			return nil, fmt.Errorf("unexpected role: %s", role)
		}
	}

	// Convert ToolConfiguration if tools are present.
	if len(tokenizeChatReq.Tools) > 0 {
		toolConfig, err := openAIToolsToBedrockToolConfig(tokenizeChatReq.Tools, nil, tokenizeChatReq.Model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert tools: %w", err)
		}
		converseInput.ToolConfig = toolConfig
	}

	req := &awsbedrock.CountTokensConverseRequest{}
	req.Input.Converse = &converseInput
	return req, nil
}

// bedrockCountTokensToResponse converts an AWS Bedrock CountTokens response to OpenAI tokenize format.
// Extracts the input token count from the CountTokens response.
func (o *ToAWSBedrockV1Tokenize) bedrockCountTokensToResponse(bedrockResp *awsbedrock.CountTokensResponse) (*tokenize.Response, error) {
	tokenizeResp := &tokenize.Response{
		Count: bedrockResp.InputTokens,
	}

	return tokenizeResp, nil
}

// RequestBody implements [TokenizeTranslator.RequestBody] for AWS Bedrock.
// This method translates an OpenAI tokenize request to AWS Bedrock CountTokens format.
func (o *ToAWSBedrockV1Tokenize) RequestBody(_ []byte, tokenizeReq *tokenize.RequestUnion, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	// Validate that the union has exactly one request type set
	if err = tokenizeReq.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid tokenize request: %w", err)
	}

	// Store the request model to use as fallback for response model.
	// If this is a CompletionRequest, convert the prompt to a single user message
	// since Bedrock's CountTokens API only supports the Converse format.
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
		// Use modelName override if set.
		o.requestModel = o.modelNameOverride
	}

	// Build the correct path for AWS Bedrock CountTokens API
	// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
	pathTemplate := "/model/%s/count-tokens"
	pathModel := o.requestModel
	// AWS Bedrock's CountTokens API requires a base foundation-model ID; it rejects
	// cross-region inference (CRIS) profile IDs (e.g. "us.anthropic.claude-sonnet-4-6",
	// "apac.amazon.nova-pro", "global.anthropic.claude-...") with "The provided model doesn't
	// support counting tokens". Inference endpoints (InvokeModel, Converse) require the CRIS ID
	// and share the same modelNameOverride, so we strip the geography prefix for count-tokens
	// only. This is provider-agnostic (unlike the Anthropic-specific path) because the Converse
	// translator also serves Amazon Nova, Meta Llama, etc.
	// See: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
	for _, prefix := range []string{"us-gov.", "us.", "eu.", "apac.", "uk.", "au.", "jp.", "global."} {
		if rest, ok := strings.CutPrefix(pathModel, prefix); ok {
			pathModel = rest
			break
		}
	}
	// URL encode the model name for the path to handle ARNs with special characters
	encodedModelName := url.PathEscape(pathModel)
	path := fmt.Sprintf(pathTemplate, encodedModelName)

	bedrockReq, err := o.tokenizeToBedrockCountTokens(tokenizeReq.ChatRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("error converting to Bedrock request: %w", err)
	}

	newBody, err = json.Marshal(bedrockReq)
	if err != nil {
		return nil, nil, fmt.Errorf("error marshaling Bedrock count tokens request: %w", err)
	}

	newHeaders = []internalapi.Header{
		{pathHeaderName, path},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// ResponseError implements [TokenizeTranslator.ResponseError] for AWS Bedrock.
// Translate AWS Bedrock exceptions to OpenAI error type.
func (o *ToAWSBedrockV1Tokenize) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	return bedrockResponseError(respHeaders, body)
}

// ResponseHeaders implements [TokenizeTranslator.ResponseHeaders].
func (o *ToAWSBedrockV1Tokenize) ResponseHeaders(map[string]string) (newHeaders []internalapi.Header, err error) {
	return nil, nil
}

// ResponseBody implements [TokenizeTranslator.ResponseBody] for AWS Bedrock.
// This method translates an AWS Bedrock CountTokens response to OpenAI tokenize format.
// AWS Bedrock uses static model execution without virtualization, where the requested model
// is exactly what gets executed. We extract token count from the CountTokens response.
func (o *ToAWSBedrockV1Tokenize) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.TokenizeSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	bedrockResp := &awsbedrock.CountTokensResponse{}
	if err = json.NewDecoder(body).Decode(bedrockResp); err != nil {
		return nil, nil, tokenUsage, responseModel, fmt.Errorf("failed to unmarshal body: %w", err)
	}

	responseModel = o.requestModel

	// Convert to OpenAI format.
	openAIResp, err := o.bedrockCountTokensToResponse(bedrockResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("error converting Bedrock response to OpenAI format: %w", err)
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
