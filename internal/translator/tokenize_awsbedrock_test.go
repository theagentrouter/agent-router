// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestNewTokenizeToAWSBedrockTranslator(t *testing.T) {
	translator := NewTokenizeToAWSBedrockTranslator("")

	require.NotNil(t, translator)
	concrete := translator.(*ToAWSBedrockV1Tokenize)
	require.Empty(t, concrete.modelNameOverride)
	require.Empty(t, concrete.requestModel)
}

func TestToAWSBedrockV1Tokenize_RequestBody(t *testing.T) {
	t.Run("chat request - no model override", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "anthropic.claude-3-opus-20240229-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello world"},
							Role:    openai.ChatMessageRoleUser,
						},
					},
				},
			},
		}

		headers, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "anthropic.claude-3-opus-20240229-v1:0", translator.requestModel)
		require.Len(t, headers, 2)
		require.Equal(t, pathHeaderName, headers[0].Key())
		require.Contains(t, headers[0].Value(), "/model/")
		require.Contains(t, headers[0].Value(), "/count-tokens")
		require.Equal(t, contentLengthHeaderName, headers[1].Key())
		require.Equal(t, strconv.Itoa(len(body)), headers[1].Value())

		// Verify the body contains AWS Bedrock CountTokens request
		var bedrockReq awsbedrock.CountTokensConverseRequest
		require.NoError(t, json.Unmarshal(body, &bedrockReq))
		require.NotNil(t, bedrockReq.Input.Converse)
		require.NotNil(t, bedrockReq.Input.Converse.Messages)
		require.Len(t, bedrockReq.Input.Converse.Messages, 1)
		require.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Input.Converse.Messages[0].Role)
		require.Len(t, bedrockReq.Input.Converse.Messages[0].Content, 1)
	})

	t.Run("chat request - with model override", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{
			modelNameOverride: "override-model",
		}

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "original-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{Value: "Test message"},
							Role:    openai.ChatMessageRoleUser,
						},
					},
				},
			},
		}

		headers, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "override-model", translator.requestModel)
		require.Len(t, headers, 2)
		require.Contains(t, headers[0].Value(), "override-model")
	})

	t.Run("chat request - with system instruction", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "anthropic.claude-3-opus-20240229-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{Value: "You are a helpful assistant"},
							Role:    openai.ChatMessageRoleSystem,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
							Role:    openai.ChatMessageRoleUser,
						},
					},
				},
			},
		}

		_, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		var bedrockReq awsbedrock.CountTokensConverseRequest
		require.NoError(t, json.Unmarshal(body, &bedrockReq))
		require.NotNil(t, bedrockReq.Input.Converse)
		require.NotNil(t, bedrockReq.Input.Converse.System)
		require.Len(t, bedrockReq.Input.Converse.System, 1)
		require.Equal(t, "You are a helpful assistant", *bedrockReq.Input.Converse.System[0].Text)
		require.Len(t, bedrockReq.Input.Converse.Messages, 1) // System message should not be in messages
		require.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Input.Converse.Messages[0].Role)
	})

	t.Run("completion request - converted to chat", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			CompletionRequest: &tokenize.CompletionRequest{
				Model:  "anthropic.claude-3-opus-20240229-v1:0",
				Prompt: "Hello world",
			},
		}

		headers, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "anthropic.claude-3-opus-20240229-v1:0", translator.requestModel)
		require.Len(t, headers, 2)

		var bedrockReq awsbedrock.CountTokensConverseRequest
		require.NoError(t, json.Unmarshal(body, &bedrockReq))
		require.NotNil(t, bedrockReq.Input.Converse)
		require.Len(t, bedrockReq.Input.Converse.Messages, 1)
		require.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Input.Converse.Messages[0].Role)
		require.Equal(t, "Hello world", *bedrockReq.Input.Converse.Messages[0].Content[0].Text)
	})

	t.Run("invalid union - both types set", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			CompletionRequest: &tokenize.CompletionRequest{
				Model:  "anthropic.claude-3-opus-20240229-v1:0",
				Prompt: "Hello",
			},
			ChatRequest: &tokenize.ChatRequest{
				Model: "anthropic.claude-3-opus-20240229-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{Value: "Test"},
							Role:    openai.ChatMessageRoleUser,
						},
					},
				},
			},
		}

		_, _, err := translator.RequestBody(nil, req, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "only one request type can be set")
	})

	t.Run("invalid union - no types set", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			// Neither completion nor chat request set
		}

		_, _, err := translator.RequestBody(nil, req, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "one request type must be set")
	})
}

func TestToAWSBedrockV1Tokenize_ResponseBody(t *testing.T) {
	t.Run("valid AWS Bedrock response", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{
			requestModel: "anthropic.claude-3-opus-20240229-v1:0",
		}

		bedrockResp := &awsbedrock.CountTokensResponse{
			InputTokens: 42,
		}

		responseJSON, err := json.Marshal(bedrockResp)
		require.NoError(t, err)

		headers, body, tokenUsage, responseModel, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil, // No span for simplicity
		)

		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "anthropic.claude-3-opus-20240229-v1:0", responseModel)
		require.Len(t, headers, 1)
		require.Equal(t, contentLengthHeaderName, headers[0].Key())

		// Verify the response is converted to OpenAI format
		var openAIResp tokenize.Response
		require.NoError(t, json.Unmarshal(body, &openAIResp))
		require.Equal(t, 42, openAIResp.Count)

		// Token usage should be extracted from Bedrock response
		_, hasTokens := tokenUsage.InputTokens()
		require.False(t, hasTokens) // Current behavior - token usage not implemented
	})

	t.Run("empty response model fallback", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{
			requestModel: "anthropic.claude-3-haiku-20240307-v1:0",
		}

		bedrockResp := &awsbedrock.CountTokensResponse{
			InputTokens: 0,
		}

		responseJSON, err := json.Marshal(bedrockResp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil,
		)

		require.NoError(t, err)
		require.Equal(t, "anthropic.claude-3-haiku-20240307-v1:0", responseModel) // Falls back to request model
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{}

		invalidJSON := "invalid json response"

		_, _, _, _, err := translator.ResponseBody(
			nil,
			bytes.NewReader([]byte(invalidJSON)),
			false,
			nil,
		)

		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to unmarshal body")
	})

	t.Run("response conversion handles zero tokens", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{}

		bedrockResp := &awsbedrock.CountTokensResponse{
			InputTokens: 0, // Zero tokens
		}

		responseJSON, err := json.Marshal(bedrockResp)
		require.NoError(t, err)

		_, body, _, _, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil,
		)

		require.NoError(t, err)

		var openAIResp tokenize.Response
		require.NoError(t, json.Unmarshal(body, &openAIResp))
		require.Equal(t, 0, openAIResp.Count)
	})

	t.Run("response conversion handles large token count", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{}

		bedrockResp := &awsbedrock.CountTokensResponse{
			InputTokens: 100000, // Large token count
		}

		responseJSON, err := json.Marshal(bedrockResp)
		require.NoError(t, err)

		_, body, _, _, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil,
		)

		require.NoError(t, err)

		var openAIResp tokenize.Response
		require.NoError(t, json.Unmarshal(body, &openAIResp))
		require.Equal(t, 100000, openAIResp.Count)
	})

	t.Run("response handles zero input tokens", func(t *testing.T) {
		translator := &ToAWSBedrockV1Tokenize{}

		bedrockResp := &awsbedrock.CountTokensResponse{
			InputTokens: 0, // Zero input tokens
		}

		responseJSON, err := json.Marshal(bedrockResp)
		require.NoError(t, err)

		_, body, _, _, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil,
		)

		require.NoError(t, err)

		var openAIResp tokenize.Response
		require.NoError(t, json.Unmarshal(body, &openAIResp))
		require.Equal(t, 0, openAIResp.Count)
	})
}

func TestToAWSBedrockV1Tokenize_ResponseError(t *testing.T) {
	translator := &ToAWSBedrockV1Tokenize{}

	tests := []struct {
		name            string
		responseHeaders map[string]string
		input           string
		expectedType    string
		expectedCode    string
		expectedMessage string
	}{
		{
			name: "AWS Bedrock structured error response",
			responseHeaders: map[string]string{
				":status":          "400",
				"content-type":     "application/json",
				"x-amzn-errortype": "ValidationException",
			},
			input: `{
				"message": "Invalid model ARN specified"
			}`,
			expectedType:    "ValidationException",
			expectedCode:    "400",
			expectedMessage: "Invalid model ARN specified",
		},
		{
			name: "Non-JSON error response",
			responseHeaders: map[string]string{
				":status":      "503",
				"content-type": "text/plain",
			},
			input:           "Service temporarily unavailable",
			expectedType:    awsBedrockBackendError,
			expectedCode:    "503",
			expectedMessage: "Service temporarily unavailable",
		},
		{
			name: "HTML error response",
			responseHeaders: map[string]string{
				":status":      "504",
				"content-type": "text/html",
			},
			input:           "<html><body>Gateway Timeout</body></html>",
			expectedType:    awsBedrockBackendError,
			expectedCode:    "504",
			expectedMessage: "<html><body>Gateway Timeout</body></html>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, newBody, err := translator.ResponseError(tt.responseHeaders, strings.NewReader(tt.input))

			require.NoError(t, err)
			require.NotNil(t, headers)
			require.NotNil(t, newBody)

			require.Len(t, headers, 2)
			require.Equal(t, contentTypeHeaderName, headers[0].Key())
			require.Equal(t, jsonContentType, headers[0].Value()) //nolint:testifylint // comparing header value, not JSON
			require.Equal(t, contentLengthHeaderName, headers[1].Key())
			require.Equal(t, strconv.Itoa(len(newBody)), headers[1].Value())

			var openAIError openai.Error
			require.NoError(t, json.Unmarshal(newBody, &openAIError))
			require.Equal(t, "error", openAIError.Type)
			require.Equal(t, tt.expectedType, openAIError.Error.Type)
			require.Equal(t, tt.expectedCode, *openAIError.Error.Code)
			require.Equal(t, tt.expectedMessage, openAIError.Error.Message)
		})
	}

	t.Run("read error", func(t *testing.T) {
		// Create a reader that will fail
		pr, pw := io.Pipe()
		_ = pw.CloseWithError(fmt.Errorf("simulated read error"))

		_, _, err := translator.ResponseError(map[string]string{":status": "500"}, pr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read error body")
	})
}

func TestToAWSBedrockV1Tokenize_ResponseHeaders(t *testing.T) {
	translator := &ToAWSBedrockV1Tokenize{}

	headers, err := translator.ResponseHeaders(map[string]string{
		"content-type":  "application/json",
		"custom-header": "value",
		"cache-control": "no-cache",
	})

	require.NoError(t, err)
	require.Nil(t, headers) // Current implementation returns nil
}

func TestToAWSBedrockV1Tokenize_HelperMethods(t *testing.T) {
	translator := &ToAWSBedrockV1Tokenize{}

	t.Run("tokenizeToBedrockCountTokens", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.StringOrUserRoleContentUnion{Value: "Test message"},
						Role:    openai.ChatMessageRoleUser,
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.NotNil(t, bedrockReq)
		require.NotNil(t, bedrockReq.Input.Converse)
		require.NotNil(t, bedrockReq.Input.Converse.Messages)
		require.Len(t, bedrockReq.Input.Converse.Messages, 1)
		require.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Input.Converse.Messages[0].Role)
	})

	t.Run("bedrockCountTokensToResponse", func(t *testing.T) {
		bedrockResp := &awsbedrock.CountTokensResponse{
			InputTokens: 25,
		}

		tokenizeResp, err := translator.bedrockCountTokensToResponse(bedrockResp)
		require.NoError(t, err)
		require.NotNil(t, tokenizeResp)
		require.Equal(t, 25, tokenizeResp.Count)
	})

	t.Run("tokenizeToBedrockCountTokens with empty messages", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model:    "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{}, // Empty messages
		}

		_, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err) // Empty messages are valid for Bedrock
	})
}

func TestToAWSBedrockV1Tokenize_IntegrationScenarios(t *testing.T) {
	t.Run("complete flow - request to response", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		// Step 1: Process request
		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "anthropic.claude-3-opus-20240229-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{Value: "Count tokens in this message"},
							Role:    openai.ChatMessageRoleUser,
						},
					},
				},
			},
		}

		reqHeaders, reqBody, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, reqBody)
		require.Len(t, reqHeaders, 2)

		// Step 2: Simulate AWS Bedrock CountTokens response
		bedrockResponse := &awsbedrock.CountTokensResponse{
			InputTokens: 7, // "Count tokens in this message" = 7 tokens
		}
		bedrockResponseJSON, err := json.Marshal(bedrockResponse)
		require.NoError(t, err)

		// Step 3: Process response
		_, respBody, tokenUsage, responseModel, err := translator.ResponseBody(
			nil,
			bytes.NewReader(bedrockResponseJSON),
			false,
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, respBody)
		require.Equal(t, "anthropic.claude-3-opus-20240229-v1:0", responseModel)

		// Step 4: Verify final OpenAI response
		var finalResp tokenize.Response
		require.NoError(t, json.Unmarshal(respBody, &finalResp))
		require.Equal(t, 7, finalResp.Count)

		// Token usage should ideally be populated but currently isn't
		_, hasTokens := tokenUsage.InputTokens()
		require.False(t, hasTokens) // Documents current limitation
	})

	t.Run("error flow - request error to response error", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		// Test error handling
		headers := map[string]string{
			":status":          "400",
			"content-type":     "application/json",
			"x-amzn-errortype": "ValidationException",
		}

		errorBody := `{
			"message": "Invalid model ARN specified"
		}`

		_, respBody, err := translator.ResponseError(headers, strings.NewReader(errorBody))
		require.NoError(t, err)
		require.NotNil(t, respBody)

		var errorResp openai.Error
		require.NoError(t, json.Unmarshal(respBody, &errorResp))
		require.Equal(t, "error", errorResp.Type)
		require.Equal(t, "ValidationException", errorResp.Error.Type)
		require.Contains(t, errorResp.Error.Message, "Invalid model ARN specified")
	})
}

func TestToAWSBedrockV1Tokenize_MessageConversion(t *testing.T) {
	translator := &ToAWSBedrockV1Tokenize{}

	t.Run("user message with multi-part text content", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role: openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{
							Value: []openai.ChatCompletionContentPartUserUnionParam{
								{OfText: &openai.ChatCompletionContentPartTextParam{Text: "Hello"}},
								{OfText: &openai.ChatCompletionContentPartTextParam{Text: "World"}},
							},
						},
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.Input.Converse.Messages, 1)
		require.Len(t, bedrockReq.Input.Converse.Messages[0].Content, 2)
		require.Equal(t, "Hello", *bedrockReq.Input.Converse.Messages[0].Content[0].Text)
		require.Equal(t, "World", *bedrockReq.Input.Converse.Messages[0].Content[1].Text)
	})

	t.Run("assistant message with string content", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "Hi"},
					},
				},
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role:    openai.ChatMessageRoleAssistant,
						Content: openai.StringOrAssistantRoleContentUnion{Value: "Hello there!"},
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.Input.Converse.Messages, 2)
		require.Equal(t, "assistant", bedrockReq.Input.Converse.Messages[1].Role)
		require.Len(t, bedrockReq.Input.Converse.Messages[1].Content, 1)
		require.Equal(t, "Hello there!", *bedrockReq.Input.Converse.Messages[1].Content[0].Text)
	})

	t.Run("assistant message with slice content", func(t *testing.T) {
		text := "part one"
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "Hi"},
					},
				},
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role: openai.ChatMessageRoleAssistant,
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{Type: openai.ChatCompletionAssistantMessageParamContentTypeText, Text: &text},
							},
						},
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.Input.Converse.Messages, 2)
		require.Equal(t, "assistant", bedrockReq.Input.Converse.Messages[1].Role)
		require.Len(t, bedrockReq.Input.Converse.Messages[1].Content, 1)
		require.Equal(t, "part one", *bedrockReq.Input.Converse.Messages[1].Content[0].Text)
	})

	t.Run("tool message with string content", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "Use the tool"},
					},
				},
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						Role:       openai.ChatMessageRoleTool,
						ToolCallID: "call_123",
						Content: openai.ContentUnion{
							Value: "tool result text",
						},
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.Input.Converse.Messages, 2)
		require.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Input.Converse.Messages[1].Role)
		require.NotNil(t, bedrockReq.Input.Converse.Messages[1].Content[0].ToolResult)
		require.Equal(t, "call_123", *bedrockReq.Input.Converse.Messages[1].Content[0].ToolResult.ToolUseID)
		require.Equal(t, "tool result text", *bedrockReq.Input.Converse.Messages[1].Content[0].ToolResult.Content[0].Text)
	})

	t.Run("tool message with text parts content", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "Use the tool"},
					},
				},
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						Role:       openai.ChatMessageRoleTool,
						ToolCallID: "call_456",
						Content: openai.ContentUnion{
							Value: []openai.ChatCompletionContentPartTextParam{
								{Text: "result part 1"},
								{Text: "result part 2"},
							},
						},
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.Input.Converse.Messages, 2)
		toolResult := bedrockReq.Input.Converse.Messages[1].Content[0].ToolResult
		require.Len(t, toolResult.Content, 2)
		require.Equal(t, "result part 1", *toolResult.Content[0].Text)
		require.Equal(t, "result part 2", *toolResult.Content[1].Text)
	})

	t.Run("system message with text parts content", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfSystem: &openai.ChatCompletionSystemMessageParam{
						Role: openai.ChatMessageRoleSystem,
						Content: openai.ContentUnion{
							Value: []openai.ChatCompletionContentPartTextParam{
								{Text: "system part 1"},
								{Text: "system part 2"},
							},
						},
					},
				},
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "Hi"},
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.Input.Converse.System, 2)
		require.Equal(t, "system part 1", *bedrockReq.Input.Converse.System[0].Text)
		require.Equal(t, "system part 2", *bedrockReq.Input.Converse.System[1].Text)
	})

	t.Run("with tools configuration", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather?"},
					},
				},
			},
			Tools: []openai.Tool{
				{
					Type: "function",
					Function: &openai.FunctionDefinition{
						Name:        "get_weather",
						Description: "Get the current weather",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{"type": "string"},
							},
						},
					},
				},
				{
					Type: "function",
					Function: &openai.FunctionDefinition{
						Name: "no_desc_tool",
					},
				},
			},
		}

		bedrockReq, err := translator.tokenizeToBedrockCountTokens(chatReq)
		require.NoError(t, err)
		require.NotNil(t, bedrockReq.Input.Converse.ToolConfig)
		require.Len(t, bedrockReq.Input.Converse.ToolConfig.Tools, 2)
		require.Equal(t, "get_weather", *bedrockReq.Input.Converse.ToolConfig.Tools[0].ToolSpec.Name)
		require.Equal(t, "Get the current weather", *bedrockReq.Input.Converse.ToolConfig.Tools[0].ToolSpec.Description)
		require.Equal(t, "no_desc_tool", *bedrockReq.Input.Converse.ToolConfig.Tools[1].ToolSpec.Name)
		require.Nil(t, bedrockReq.Input.Converse.ToolConfig.Tools[1].ToolSpec.Description)
	})
}

func TestToAWSBedrockV1Tokenize_CRISPrefixStripping(t *testing.T) {
	t.Run("CRIS prefix stripped from model path", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "us.anthropic.claude-sonnet-4-6",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hi"},
						},
					},
				},
			},
		}

		headers, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.Contains(t, headers[0].Value(), "anthropic.claude-sonnet-4-6")
		require.NotContains(t, headers[0].Value(), "us.anthropic")
	})

	t.Run("non-CRIS prefix not stripped", func(t *testing.T) {
		translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "anthropic.claude-3-haiku-20240307-v1:0",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hi"},
						},
					},
				},
			},
		}

		headers, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.Contains(t, headers[0].Value(), "anthropic.claude-3-haiku-20240307-v1:0")
	})

	// Provider-agnostic stripping of every CRIS geography prefix, for any provider.
	crisCases := []struct {
		name       string
		model      string
		wantSub    string // remains after stripping
		notWantSub string // prefix that must be gone
	}{
		{"us. prefix", "us.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "us.anthropic"},
		{"eu. prefix", "eu.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "eu.anthropic"},
		{"apac. prefix", "apac.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "apac."},
		{"us-gov. prefix", "us-gov.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "us-gov."},
		{"uk. prefix", "uk.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "uk.anthropic"},
		{"au. prefix", "au.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "au.anthropic"},
		{"jp. prefix", "jp.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "jp.anthropic"},
		{"global. prefix", "global.anthropic.claude-sonnet-4-6", "/model/anthropic.claude-sonnet-4-6/count-tokens", "global."},
		{"apac. prefix on non-anthropic model", "apac.amazon.nova-pro", "/model/amazon.nova-pro/count-tokens", "apac."},
	}
	for _, tc := range crisCases {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)
			req := &tokenize.RequestUnion{
				ChatRequest: &tokenize.ChatRequest{
					Model: tc.model,
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Hi"},
						}},
					},
				},
			}
			headers, _, err := translator.RequestBody(nil, req, false)
			require.NoError(t, err)
			require.Contains(t, headers[0].Value(), tc.wantSub)
			require.NotContains(t, headers[0].Value(), tc.notWantSub)
		})
	}
}

// Benchmark tests for performance
func BenchmarkToAWSBedrockV1Tokenize_RequestBody(b *testing.B) {
	translator := NewTokenizeToAWSBedrockTranslator("").(*ToAWSBedrockV1Tokenize)
	req := &tokenize.RequestUnion{
		ChatRequest: &tokenize.ChatRequest{
			Model: "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.StringOrUserRoleContentUnion{Value: "Benchmark test message"},
						Role:    openai.ChatMessageRoleUser,
					},
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := translator.RequestBody(nil, req, false)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkToAWSBedrockV1Tokenize_ResponseBody(b *testing.B) {
	translator := &ToAWSBedrockV1Tokenize{requestModel: "anthropic.claude-3-opus-20240229-v1:0"}
	bedrockResp := &awsbedrock.CountTokensResponse{
		InputTokens: 100,
	}
	responseJSON, _ := json.Marshal(bedrockResp)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, err := translator.ResponseBody(nil, bytes.NewReader(responseJSON), false, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}
