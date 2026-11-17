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
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestNewTokenizeToGCPVertexAITranslator(t *testing.T) {
	translator := NewTokenizeToGCPVertexAITranslator("")

	require.NotNil(t, translator)
	concrete := translator.(*ToGCPVertexAIV1Tokenize)
	require.Empty(t, concrete.modelNameOverride)
	require.Empty(t, concrete.requestModel)
}

func TestToGCPVertexAIV1Tokenize_RequestBody(t *testing.T) {
	t.Run("chat request - no model override", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "gemini-2.0-flash-001",
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
		require.Equal(t, "gemini-2.0-flash-001", translator.requestModel)
		require.Len(t, headers, 2)
		require.Equal(t, pathHeaderName, headers[0].Key())
		require.Contains(t, headers[0].Value(), "gemini-2.0-flash-001")
		require.Contains(t, headers[0].Value(), "countTokens")
		require.Equal(t, contentLengthHeaderName, headers[1].Key())
		require.Equal(t, strconv.Itoa(len(body)), headers[1].Value())

		// Verify the body contains GCP count tokens request
		var gcpReq gcp.CountTokenRequest
		require.NoError(t, json.Unmarshal(body, &gcpReq))
		require.NotNil(t, gcpReq.Contents)
		require.Len(t, gcpReq.Contents, 1)
		require.Equal(t, "user", gcpReq.Contents[0].Role)
		require.Len(t, gcpReq.Contents[0].Parts, 1)
		require.Equal(t, "Hello world", gcpReq.Contents[0].Parts[0].Text)
	})

	t.Run("chat request - with model override", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{
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
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "gemini-1.5-pro",
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

		var gcpReq gcp.CountTokenRequest
		require.NoError(t, json.Unmarshal(body, &gcpReq))
		require.NotNil(t, gcpReq.SystemInstruction)
		require.Len(t, gcpReq.SystemInstruction.Parts, 1)
		require.Equal(t, "You are a helpful assistant", gcpReq.SystemInstruction.Parts[0].Text)
		require.Len(t, gcpReq.Contents, 1) // System message should not be in contents
		require.Equal(t, "user", gcpReq.Contents[0].Role)
	})

	t.Run("completion request - converted to chat", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		req := &tokenize.RequestUnion{
			CompletionRequest: &tokenize.CompletionRequest{
				Model:  "gemini-2.0-flash-001",
				Prompt: "Hello world",
			},
		}

		headers, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "gemini-2.0-flash-001", translator.requestModel)
		require.Len(t, headers, 2)
		require.Contains(t, headers[0].Value(), "gemini-2.0-flash-001")
		require.Contains(t, headers[0].Value(), "countTokens")

		var gcpReq gcp.CountTokenRequest
		require.NoError(t, json.Unmarshal(body, &gcpReq))
		require.Len(t, gcpReq.Contents, 1)
		require.Equal(t, "user", gcpReq.Contents[0].Role)
	})

	t.Run("empty model string", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "", // Empty model is rejected: model is a required field.
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
		require.Contains(t, err.Error(), "model is required")
	})

	t.Run("message conversion error", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		// Create a request with invalid message structure that would cause conversion error
		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model:    "gemini-1.5-pro",
				Messages: []openai.ChatCompletionMessageParamUnion{}, // Empty messages
			},
		}

		_, _, err := translator.RequestBody(nil, req, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "error converting to Gemini request")
	})
}

func TestToGCPVertexAIV1Tokenize_ResponseBody(t *testing.T) {
	t.Run("valid GCP response", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{
			requestModel: "gemini-2.0-flash-001",
		}

		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 42,
		}

		responseJSON, err := json.Marshal(gcpResp)
		require.NoError(t, err)

		headers, body, tokenUsage, responseModel, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil, // No span for simplicity
		)

		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "gemini-2.0-flash-001", responseModel)
		require.Len(t, headers, 1)
		require.Equal(t, contentLengthHeaderName, headers[0].Key())

		// Verify the response is converted to OpenAI format
		var openAIResp tokenize.Response
		require.NoError(t, json.Unmarshal(body, &openAIResp))
		require.Equal(t, 42, openAIResp.Count)

		// The /tokenize endpoint does not populate token usage metrics,
		// matching the OpenAI tokenize translator.
		_, hasTokens := tokenUsage.InputTokens()
		require.False(t, hasTokens)
	})

	t.Run("empty response model fallback", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{
			requestModel: "gemini-1.5-pro",
		}

		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 0,
		}

		responseJSON, err := json.Marshal(gcpResp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil,
		)

		require.NoError(t, err)
		require.Equal(t, "gemini-1.5-pro", responseModel) // Falls back to request model
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{}

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

	t.Run("GCP response conversion error", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{}

		// Valid JSON but might cause conversion issues
		gcpResp := &genai.CountTokensResponse{
			TotalTokens: -1, // Negative token count might cause issues
		}

		responseJSON, err := json.Marshal(gcpResp)
		require.NoError(t, err)

		_, _, _, _, err = translator.ResponseBody(
			nil,
			bytes.NewReader(responseJSON),
			false,
			nil,
		)

		// Current implementation might not validate negative tokens
		// This documents potential improvement area
		require.NoError(t, err) // Current behavior
	})

	t.Run("response marshaling error simulation", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{}

		// Create a valid GCP response
		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 10,
		}

		responseJSON, err := json.Marshal(gcpResp)
		require.NoError(t, err)

		// The current implementation should not have marshaling errors with normal data
		_, _, _, _, err = translator.ResponseBody(nil, bytes.NewReader(responseJSON), false, nil)
		require.NoError(t, err)
	})

	t.Run("nil span handling", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{
			requestModel: "gemini-1.5-pro",
		}

		gcpResp := &genai.CountTokensResponse{TotalTokens: 5}
		responseJSON, err := json.Marshal(gcpResp)
		require.NoError(t, err)

		// Should not panic with nil span
		_, _, _, _, err = translator.ResponseBody(nil, bytes.NewReader(responseJSON), false, nil)
		require.NoError(t, err)
	})
}

func TestToGCPVertexAIV1Tokenize_ResponseError(t *testing.T) {
	translator := &ToGCPVertexAIV1Tokenize{}

	tests := []struct {
		name            string
		responseHeaders map[string]string
		input           string
		expectedType    string
		expectedCode    string
		expectedMessage string
	}{
		{
			name: "GCP structured error response",
			responseHeaders: map[string]string{
				":status":      "400",
				"content-type": "application/json",
			},
			input: `{
				"error": {
					"code": 400,
					"message": "Invalid request: model not found",
					"status": "INVALID_ARGUMENT",
					"details": [{"type": "additional_info", "value": "gemini-invalid not supported"}]
				}
			}`,
			expectedType:    "INVALID_ARGUMENT",
			expectedCode:    "400",
			expectedMessage: "Error: Invalid request: model not found\nDetails: [{\"type\": \"additional_info\", \"value\": \"gemini-invalid not supported\"}]",
		},
		{
			name: "GCP structured error without details",
			responseHeaders: map[string]string{
				":status":      "403",
				"content-type": "application/json",
			},
			input: `{
				"error": {
					"code": 403,
					"message": "Permission denied",
					"status": "PERMISSION_DENIED"
				}
			}`,
			expectedType:    "PERMISSION_DENIED",
			expectedCode:    "403",
			expectedMessage: "Permission denied",
		},
		{
			name: "Non-JSON error response",
			responseHeaders: map[string]string{
				":status":      "503",
				"content-type": "text/plain",
			},
			input:           "Service temporarily unavailable",
			expectedType:    "GCPVertexAIBackendError",
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
			expectedType:    "GCPVertexAIBackendError",
			expectedCode:    "504",
			expectedMessage: "<html><body>Gateway Timeout</body></html>",
		},
		{
			name: "Invalid JSON error",
			responseHeaders: map[string]string{
				":status":      "500",
				"content-type": "application/json",
			},
			input:           "{invalid json",
			expectedType:    "GCPVertexAIBackendError",
			expectedCode:    "500",
			expectedMessage: "{invalid json",
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

	t.Run("marshal error simulation", func(t *testing.T) {
		// This is hard to simulate with normal JSON marshal,
		// but we can test normal operation
		headers := map[string]string{":status": "400"}
		input := "test error"

		newHeaders, newBody, err := translator.ResponseError(headers, strings.NewReader(input))
		require.NoError(t, err)
		require.NotNil(t, newHeaders)
		require.NotNil(t, newBody)
	})
}

func TestToGCPVertexAIV1Tokenize_ResponseHeaders(t *testing.T) {
	translator := &ToGCPVertexAIV1Tokenize{}

	headers, err := translator.ResponseHeaders(map[string]string{
		"content-type":  "application/json",
		"custom-header": "value",
		"cache-control": "no-cache",
	})

	require.NoError(t, err)
	require.Nil(t, headers) // Current implementation returns nil
}

func TestToGCPVertexAIV1Tokenize_HelperMethods(t *testing.T) {
	translator := &ToGCPVertexAIV1Tokenize{}

	t.Run("tokenizeToGeminiCountToken", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model: "gemini-1.5-pro",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.StringOrUserRoleContentUnion{Value: "Test message"},
						Role:    openai.ChatMessageRoleUser,
					},
				},
			},
		}

		gcpReq, err := translator.tokenizeToGeminiCountToken(chatReq, "gemini-1.5-pro")
		require.NoError(t, err)
		require.NotNil(t, gcpReq)
		require.NotNil(t, gcpReq.Contents)
		require.Len(t, gcpReq.Contents, 1)
		require.Equal(t, "user", gcpReq.Contents[0].Role)
		require.Equal(t, "Test message", gcpReq.Contents[0].Parts[0].Text)
	})

	t.Run("geminiCountTokenToResponse", func(t *testing.T) {
		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 25,
		}

		tokenizeResp, err := translator.geminiCountTokenToResponse(gcpResp)
		require.NoError(t, err)
		require.NotNil(t, tokenizeResp)
		require.Equal(t, 25, tokenizeResp.Count)
	})

	t.Run("tokenizeToGeminiCountToken with invalid messages", func(t *testing.T) {
		chatReq := &tokenize.ChatRequest{
			Model:    "gemini-1.5-pro",
			Messages: []openai.ChatCompletionMessageParamUnion{}, // Empty messages
		}

		_, err := translator.tokenizeToGeminiCountToken(chatReq, "gemini-1.5-pro")
		require.Error(t, err) // Should error on empty messages
	})

	t.Run("geminiCountTokenToResponse with zero tokens", func(t *testing.T) {
		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 0,
		}

		tokenizeResp, err := translator.geminiCountTokenToResponse(gcpResp)
		require.NoError(t, err)
		require.Equal(t, 0, tokenizeResp.Count)
	})

	t.Run("geminiCountTokenToResponse with large token count", func(t *testing.T) {
		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 1000000,
		}

		tokenizeResp, err := translator.geminiCountTokenToResponse(gcpResp)
		require.NoError(t, err)
		require.Equal(t, 1000000, tokenizeResp.Count)
	})
}

func TestToGCPVertexAIV1Tokenize_IntegrationScenarios(t *testing.T) {
	t.Run("complete flow - request to response", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		// Step 1: Process request
		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "gemini-2.0-flash-001",
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

		// Step 2: Simulate GCP response
		gcpResponse := &genai.CountTokensResponse{
			TotalTokens: 7, // "Count tokens in this message" = 7 tokens
		}
		gcpResponseJSON, err := json.Marshal(gcpResponse)
		require.NoError(t, err)

		// Step 3: Process response
		_, respBody, tokenUsage, responseModel, err := translator.ResponseBody(
			nil,
			bytes.NewReader(gcpResponseJSON),
			false,
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, respBody)
		require.Equal(t, "gemini-2.0-flash-001", responseModel)

		// Step 4: Verify final OpenAI response
		var finalResp tokenize.Response
		require.NoError(t, json.Unmarshal(respBody, &finalResp))
		require.Equal(t, 7, finalResp.Count)

		// Token usage should ideally be populated but currently isn't
		_, hasTokens := tokenUsage.InputTokens()
		require.False(t, hasTokens) // Documents current limitation
	})

	t.Run("error flow - request error to response error", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		// Test error handling
		headers := map[string]string{
			":status":      "400",
			"content-type": "application/json",
		}

		errorBody := `{
			"error": {
				"code": 400,
				"message": "Invalid model specified",
				"status": "INVALID_ARGUMENT"
			}
		}`

		_, respBody, err := translator.ResponseError(headers, strings.NewReader(errorBody))
		require.NoError(t, err)
		require.NotNil(t, respBody)

		var errorResp openai.Error
		require.NoError(t, json.Unmarshal(respBody, &errorResp))
		require.Equal(t, "error", errorResp.Type)
		require.Equal(t, "INVALID_ARGUMENT", errorResp.Error.Type)
		require.Contains(t, errorResp.Error.Message, "Invalid model specified")
	})
}

// Test edge cases and identified improvements
func TestToGCPVertexAIV1Tokenize_IdentifiedIssues(t *testing.T) {
	t.Run("IMPROVEMENT: missing token usage extraction", func(t *testing.T) {
		translator := &ToGCPVertexAIV1Tokenize{}

		gcpResp := &genai.CountTokensResponse{
			TotalTokens: 42, // This should be used for token metrics
		}

		responseJSON, err := json.Marshal(gcpResp)
		require.NoError(t, err)

		_, _, tokenUsage, _, err := translator.ResponseBody(nil, bytes.NewReader(responseJSON), false, nil)
		require.NoError(t, err)

		// The /tokenize endpoint intentionally does not populate token usage
		// metrics, matching the OpenAI tokenize translator.
		inputTokens, hasInput := tokenUsage.InputTokens()
		totalTokens, hasTotal := tokenUsage.TotalTokens()

		require.False(t, hasInput)
		require.False(t, hasTotal)
		require.Equal(t, uint32(0), inputTokens)
		require.Equal(t, uint32(0), totalTokens)
	})

	t.Run("ISSUE RESOLVED: model field is now required", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		req := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "gemini-1.5-pro", // Model is now required field
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

		// Model field is now required so this should work without panic
		headers, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)
		require.NotNil(t, body)
		require.Equal(t, "gemini-1.5-pro", translator.requestModel)
		require.Len(t, headers, 2)
	})

	t.Run("IMPROVEMENT: input validation missing", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		// Test with nil ChatRequest
		req := &tokenize.RequestUnion{
			ChatRequest: nil,
		}

		// Union validation now runs first and catches this invalid state
		_, _, err := translator.RequestBody(nil, req, false)
		require.Error(t, err) // Should error on invalid union
		require.Contains(t, err.Error(), "one request type must be set")
	})

	t.Run("union validation now implemented", func(t *testing.T) {
		translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

		// Test 1: Invalid union state - both types set
		req := &tokenize.RequestUnion{
			CompletionRequest: &tokenize.CompletionRequest{
				Model:  "gpt-4",
				Prompt: "Hello",
			},
			ChatRequest: &tokenize.ChatRequest{
				Model: "gemini-2.0-flash-001",
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

		// Should now return an error for invalid union
		_, _, err := translator.RequestBody(nil, req, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "only one request type can be set")

		// Test 2: Invalid union state - no types set
		emptyReq := &tokenize.RequestUnion{}
		_, _, err = translator.RequestBody(nil, emptyReq, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "one request type must be set")

		// Test 3: Valid union state - only chat set (completion not supported for Gemini)
		validChatReq := &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "gemini-2.0-flash-001",
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
		_, _, err = translator.RequestBody(nil, validChatReq, false)
		require.NoError(t, err)
	})
}

func TestToGCPVertexAIV1Tokenize_MediaResolution(t *testing.T) {
	translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

	req := &tokenize.RequestUnion{
		ChatRequest: &tokenize.ChatRequest{
			Model: "gemini-2.0-flash-001",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    openai.ChatMessageRoleUser,
						Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
					},
				},
			},
			MediaResolution: "HIGH",
		},
	}

	_, body, err := translator.RequestBody(nil, req, false)
	require.NoError(t, err)

	var gcpReq gcp.CountTokenRequest
	require.NoError(t, json.Unmarshal(body, &gcpReq))
	require.NotNil(t, gcpReq.GenerationConfig)
	require.Equal(t, genai.MediaResolution("HIGH"), gcpReq.GenerationConfig.MediaResolution)
}

func TestToGCPVertexAIV1Tokenize_ToolsConversion(t *testing.T) {
	translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)

	req := &tokenize.RequestUnion{
		ChatRequest: &tokenize.ChatRequest{
			Model: "gemini-2.0-flash-001",
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
						Description: "Get weather",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}

	_, body, err := translator.RequestBody(nil, req, false)
	require.NoError(t, err)

	var gcpReq gcp.CountTokenRequest
	require.NoError(t, json.Unmarshal(body, &gcpReq))
	require.NotEmpty(t, gcpReq.Tools)
}

func TestToGCPVertexAIV1Tokenize_EmptyMessages(t *testing.T) {
	translator := &ToGCPVertexAIV1Tokenize{}

	chatReq := &tokenize.ChatRequest{
		Model:    "gemini-2.0-flash-001",
		Messages: []openai.ChatCompletionMessageParamUnion{},
	}

	_, err := translator.tokenizeToGeminiCountToken(chatReq, "gemini-2.0-flash-001")
	require.Error(t, err)
	require.Contains(t, err.Error(), "messages must produce at least one content entry")
}

// Benchmark tests for performance
func BenchmarkToGCPVertexAIV1Tokenize_RequestBody(b *testing.B) {
	translator := NewTokenizeToGCPVertexAITranslator("").(*ToGCPVertexAIV1Tokenize)
	req := &tokenize.RequestUnion{
		ChatRequest: &tokenize.ChatRequest{
			Model: "gemini-2.0-flash-001",
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

func BenchmarkToGCPVertexAIV1Tokenize_ResponseBody(b *testing.B) {
	translator := &ToGCPVertexAIV1Tokenize{requestModel: "gemini-2.0-flash-001"}
	gcpResp := &genai.CountTokensResponse{TotalTokens: 100}
	responseJSON, _ := json.Marshal(gcpResp)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, err := translator.ResponseBody(nil, bytes.NewReader(responseJSON), false, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestToGCPVertexAIV1Tokenize_RequestBody_EndpointResource(t *testing.T) {
	const reqJSON = `{
  "model": "client-model",
  "messages": [{"role": "user", "content": "ping"}],
  "tools": [{
    "type": "function",
    "function": {"name": "lookup", "parameters": {"type": "object", "properties": {"q": {"type": "string"}}, "additionalProperties": false}}
  }]
}`

	tests := []struct {
		name               string
		model              string
		wantPath           string
		wantToolJSONSchema bool
	}{
		{
			name:               "endpoint resource",
			model:              "endpoints/1234567890",
			wantPath:           "endpoints/1234567890:countTokens",
			wantToolJSONSchema: true,
		},
		{
			name:               "publisher model keeps the publisher path",
			model:              "gemini-3.5-flash",
			wantPath:           "publishers/google/models/gemini-3.5-flash:countTokens",
			wantToolJSONSchema: true,
		},
		{
			name:               "publisher model without feature support is unchanged",
			model:              "gemini-1.5-pro",
			wantPath:           "publishers/google/models/gemini-1.5-pro:countTokens",
			wantToolJSONSchema: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var chatReq tokenize.ChatRequest
			require.NoError(t, json.Unmarshal([]byte(reqJSON), &chatReq))

			headers, body, err := NewTokenizeToGCPVertexAITranslator(tc.model).RequestBody(nil, &tokenize.RequestUnion{ChatRequest: &chatReq}, false)
			require.NoError(t, err)

			var gotPath string
			for _, h := range headers {
				if h.Key() == pathHeaderName {
					gotPath = h.Value()
				}
			}
			require.Equal(t, tc.wantPath, gotPath)

			var got struct {
				Tools []struct {
					FunctionDeclarations []map[string]any `json:"functionDeclarations"`
				} `json:"tools"`
			}
			require.NoError(t, json.Unmarshal(body, &got))
			_, hasToolJSONSchema := got.Tools[0].FunctionDeclarations[0]["parametersJsonSchema"]
			require.Equal(t, tc.wantToolJSONSchema, hasToolJSONSchema)
		})
	}

	t.Run("missing endpoint ID", func(t *testing.T) {
		_, _, err := NewTokenizeToGCPVertexAITranslator("endpoints/").RequestBody(nil, &tokenize.RequestUnion{
			ChatRequest: &tokenize.ChatRequest{
				Model: "client-model",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{Role: openai.ChatMessageRoleUser, Content: openai.StringOrUserRoleContentUnion{Value: "ping"}}},
				},
			},
		}, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
}
