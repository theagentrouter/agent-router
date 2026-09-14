// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/base64"
	stdjson "encoding/json" // nolint: depguard
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// wrapAnthropicSSEInEventStream wraps Anthropic SSE data in AWS EventStream format.
// AWS Bedrock base64-encodes each event's JSON data (which includes the type field) and wraps it in EventStream messages.
func wrapAnthropicSSEInEventStream(sseData string) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	encoder := eventstream.NewEncoder()

	// Parse SSE format to extract individual events
	// SSE format: "event: TYPE\ndata: JSON\n\n"
	events := bytes.Split([]byte(sseData), []byte("\n\n"))

	for _, eventBlock := range events {
		if len(bytes.TrimSpace(eventBlock)) == 0 {
			continue
		}

		// Extract both event type and data from the SSE event
		lines := bytes.Split(eventBlock, []byte("\n"))
		var eventType string
		var jsonData []byte
		for _, line := range lines {
			if bytes.HasPrefix(line, []byte("event: ")) {
				eventType = string(bytes.TrimPrefix(line, []byte("event: ")))
			} else if bytes.HasPrefix(line, []byte("data: ")) {
				jsonData = bytes.TrimPrefix(line, []byte("data: "))
			}
		}

		if len(jsonData) == 0 {
			continue
		}

		// AWS Bedrock Anthropic format includes the type in the JSON data itself
		// If the JSON doesn't already have a "type" field (like in malformed test cases),
		// we need to add it to match real AWS Bedrock behavior
		var finalJSON []byte
		if eventType != "" && !bytes.Contains(jsonData, []byte(`"type"`)) {
			// Prepend the type field to simulate real Anthropic event format
			// For malformed JSON, this creates something like: {"type": "message_start", {invalid...}
			// which is still malformed, but has the type field that can be extracted
			finalJSON = []byte(fmt.Sprintf(`{"type": "%s", %s`, eventType, string(jsonData[1:])))
			if jsonData[0] != '{' {
				// If it doesn't even start with {, just wrap it
				finalJSON = []byte(fmt.Sprintf(`{"type": "%s", "data": %s}`, eventType, string(jsonData)))
			}
		} else {
			finalJSON = jsonData
		}

		// Base64 encode the JSON data (this is what AWS Bedrock does)
		base64Data := base64.StdEncoding.EncodeToString(finalJSON)

		// Create a payload with the base64-encoded data in the "bytes" field
		payload := struct {
			Bytes string `json:"bytes"`
		}{
			Bytes: base64Data,
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		// Encode as EventStream message
		err = encoder.Encode(buf, eventstream.Message{
			Headers: eventstream.Headers{{Name: ":event-type", Value: eventstream.StringValue("chunk")}},
			Payload: payloadBytes,
		})
		if err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// TestResponseModel_AWSAnthropic tests that AWS Anthropic (non-streaming) returns the request model
// AWS Anthropic uses deterministic model mapping without virtualization
func TestResponseModel_AWSAnthropic(t *testing.T) {
	modelName := "anthropic.claude-sonnet-4-20250514-v1:0"
	translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", modelName)

	// Initialize translator with the model
	req := &openai.ChatCompletionRequest{
		Model:     "claude-sonnet-4",
		MaxTokens: ptr.To(int64(100)),
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.StringOrUserRoleContentUnion{Value: "Hello"},
					Role:    openai.ChatMessageRoleUser,
				},
			},
		},
	}
	reqBody, _ := json.Marshal(req)
	_, _, err := translator.RequestBody(reqBody, req, false)
	require.NoError(t, err)

	// AWS Anthropic response doesn't have model field, uses Anthropic format
	anthropicResponse := anthropic.Message{
		ID:   "msg_01XYZ",
		Type: constant.ValueOf[constant.Message](),
		Role: constant.ValueOf[constant.Assistant](),
		Content: []anthropic.ContentBlockUnion{
			{
				Type: "text",
				Text: "Hello!",
			},
		},
		StopReason: anthropic.StopReasonEndTurn,
		Usage: anthropic.Usage{
			InputTokens:  10,
			OutputTokens: 5,
		},
	}

	body, err := json.Marshal(anthropicResponse)
	require.NoError(t, err)

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel) // Returns the request model since no virtualization
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
}

func TestOpenAIToAWSAnthropicTranslatorV1ChatCompletion_RequestBody(t *testing.T) {
	// Define a common input request to use for both standard and vertex tests.
	openAIReq := &openai.ChatCompletionRequest{
		Model: "anthropic.claude-3-opus-20240229-v1:0",
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfSystem: &openai.ChatCompletionSystemMessageParam{Content: openai.ContentUnion{Value: "You are a helpful assistant."}, Role: openai.ChatMessageRoleSystem},
			},
			{
				OfUser: &openai.ChatCompletionUserMessageParam{Content: openai.StringOrUserRoleContentUnion{Value: "Hello!"}, Role: openai.ChatMessageRoleUser},
			},
		},
		MaxTokens:   ptr.To(int64(1024)),
		Temperature: ptr.To(0.7),
	}

	t.Run("AWS Bedrock InvokeModel Values Configured Correctly", func(t *testing.T) {
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
		hm, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)
		require.NotNil(t, body)

		// Check the path header.
		pathHeader := hm[0]
		require.Equal(t, pathHeaderName, pathHeader.Key())
		expectedPath := fmt.Sprintf("/model/%s/invoke", openAIReq.Model)
		require.Equal(t, expectedPath, pathHeader.Value())

		// Check the body content.
		require.NotNil(t, body)
		// Model should NOT be present in the body for AWS Bedrock.
		require.False(t, gjson.GetBytes(body, "model").Exists())
		// Anthropic version should be present for AWS Bedrock.
		require.Equal(t, BedrockDefaultVersion, gjson.GetBytes(body, "anthropic_version").String())
	})

	t.Run("Model Name Override", func(t *testing.T) {
		overrideModelName := "anthropic.claude-3-haiku-20240307-v1:0"
		// Instantiate the translator with the model name override.
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", overrideModelName)

		// Call RequestBody with the original request, which has a different model name.
		hm, _, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)

		// Check that the :path header uses the override model name.
		pathHeader := hm[0]
		require.Equal(t, pathHeaderName, pathHeader.Key())
		expectedPath := fmt.Sprintf("/model/%s/invoke", overrideModelName)
		require.Equal(t, expectedPath, pathHeader.Value())
	})

	t.Run("Model Name with ARN (URL encoding)", func(t *testing.T) {
		arnModelName := "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-opus-20240229-v1:0"
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", arnModelName)

		hm, _, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)

		// Check that the :path header uses URL-encoded model name.
		pathHeader := hm[0]
		require.Equal(t, pathHeaderName, pathHeader.Key())
		// url.PathEscape encodes slashes but not colons (colons are valid in URL paths)
		// So we expect slashes to be encoded as %2F
		require.Contains(t, pathHeader.Value(), "arn:aws:bedrock") // Colons are not encoded
		require.Contains(t, pathHeader.Value(), "%2Fanthropic")    // Slashes are encoded
	})

	t.Run("Streaming Request Validation", func(t *testing.T) {
		streamReq := &openai.ChatCompletionRequest{
			Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
			Messages:  []openai.ChatCompletionMessageParamUnion{},
			MaxTokens: ptr.To(int64(100)),
			Stream:    true,
		}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
		hm, body, err := translator.RequestBody(nil, streamReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)

		// Check that the :path header uses the invoke-with-response-stream endpoint.
		pathHeader := hm
		require.Equal(t, pathHeaderName, pathHeader[0].Key())
		expectedPath := fmt.Sprintf("/model/%s/invoke-with-response-stream", streamReq.Model)
		require.Equal(t, expectedPath, pathHeader[0].Value())

		// AWS Bedrock uses the endpoint path to indicate streaming (invoke-with-response-stream)
		// The Anthropic Messages API body format doesn't require a "stream" field
		// Verify the body is valid JSON with expected Anthropic fields
		require.True(t, gjson.GetBytes(body, "max_tokens").Exists())
		require.True(t, gjson.GetBytes(body, "anthropic_version").Exists())
	})

	t.Run("API Version Override", func(t *testing.T) {
		customAPIVersion := "bedrock-2024-01-01"
		// Instantiate the translator with the custom API version.
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator(customAPIVersion, "")

		// Call RequestBody with a standard request.
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		// Check that the anthropic_version in the body uses the custom version.
		require.Equal(t, customAPIVersion, gjson.GetBytes(body, "anthropic_version").String())
	})

	t.Run("Invalid Temperature (above bound)", func(t *testing.T) {
		invalidTempReq := &openai.ChatCompletionRequest{
			Model:       "anthropic.claude-3-opus-20240229-v1:0",
			Messages:    []openai.ChatCompletionMessageParamUnion{},
			MaxTokens:   ptr.To(int64(100)),
			Temperature: ptr.To(2.5),
		}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
		_, _, err := translator.RequestBody(nil, invalidTempReq, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf(tempNotSupportedError, *invalidTempReq.Temperature))
	})

	t.Run("Missing MaxTokens Passes With Zero", func(t *testing.T) {
		missingTokensReq := &openai.ChatCompletionRequest{
			Model:    "anthropic.claude-3-opus-20240229-v1:0",
			Messages: []openai.ChatCompletionMessageParamUnion{},
		}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, missingTokensReq, false)
		require.NoError(t, err)
		require.Equal(t, int64(0), gjson.GetBytes(body, "max_tokens").Int())
	})
}

func TestOpenAIToAWSAnthropicTranslatorV1ChatCompletion_ResponseBody(t *testing.T) {
	t.Run("invalid json body", func(t *testing.T) {
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
		_, _, _, _, err := translator.ResponseBody(map[string]string{statusHeaderName: "200"}, bytes.NewBufferString("invalid json"), true, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to unmarshal body")
	})

	tests := []struct {
		name                   string
		inputResponse          *anthropic.Message
		respHeaders            map[string]string
		expectedOpenAIResponse openai.ChatCompletionResponse
	}{
		{
			name: "basic text response",
			inputResponse: &anthropic.Message{
				ID:         "msg_01XYZ123",
				Model:      "claude-3-5-sonnet-20241022",
				Role:       constant.Assistant(anthropic.MessageParamRoleAssistant),
				Content:    []anthropic.ContentBlockUnion{{Type: "text", Text: "Hello there!"}},
				StopReason: anthropic.StopReasonEndTurn,
				Usage:      anthropic.Usage{InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 5},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ123",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens:     15,
					CompletionTokens: 20,
					TotalTokens:      35,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens: 5,
					},
					CompletionTokensDetails: &openai.CompletionTokensDetails{},
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index:        0,
						Message:      openai.ChatCompletionResponseChoiceMessage{Role: "assistant", Content: ptr.To("Hello there!")},
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					},
				},
			},
		},
		{
			name: "response with tool use",
			inputResponse: &anthropic.Message{
				ID:    "msg_01XYZ123",
				Model: "claude-3-5-sonnet-20241022",
				Role:  constant.Assistant(anthropic.MessageParamRoleAssistant),
				Content: []anthropic.ContentBlockUnion{
					{Type: "text", Text: "Ok, I will call the tool."},
					{Type: "tool_use", ID: "toolu_01", Name: "get_weather", Input: stdjson.RawMessage(`{"location": "Tokyo", "unit": "celsius"}`)},
				},
				StopReason: anthropic.StopReasonToolUse,
				Usage:      anthropic.Usage{InputTokens: 25, OutputTokens: 15, CacheReadInputTokens: 10},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ123",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens: 35, CompletionTokens: 15, TotalTokens: 50,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens: 10,
					},
					CompletionTokensDetails: &openai.CompletionTokensDetails{},
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index:        0,
						FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Role:    string(anthropic.MessageParamRoleAssistant),
							Content: ptr.To("Ok, I will call the tool."),
							ToolCalls: []openai.ChatCompletionMessageToolCallParam{
								{
									ID:   ptr.To("toolu_01"),
									Type: openai.ChatCompletionMessageToolCallTypeFunction,
									Function: openai.ChatCompletionMessageToolCallFunctionParam{
										Name:      "get_weather",
										Arguments: `{"location": "Tokyo", "unit": "celsius"}`,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.inputResponse)
			require.NoError(t, err, "Test setup failed: could not marshal input struct")

			translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
			hm, body, usedToken, _, err := translator.ResponseBody(tt.respHeaders, bytes.NewBuffer(body), true, nil)

			require.NoError(t, err, "Translator returned an unexpected internal error")
			require.NotNil(t, hm)
			require.NotNil(t, body)

			newBody := body
			require.NotNil(t, newBody)
			require.Len(t, hm, 1)
			require.Equal(t, contentLengthHeaderName, hm[0].Key())
			require.Equal(t, strconv.Itoa(len(newBody)), hm[0].Value())

			var gotResp openai.ChatCompletionResponse
			err = json.Unmarshal(newBody, &gotResp)
			require.NoError(t, err)

			expectedTokenUsage := tokenUsageFrom(
				int32(tt.expectedOpenAIResponse.Usage.PromptTokens),                            // nolint:gosec
				int32(tt.expectedOpenAIResponse.Usage.PromptTokensDetails.CachedTokens),        // nolint:gosec
				int32(tt.expectedOpenAIResponse.Usage.PromptTokensDetails.CacheCreationTokens), // nolint:gosec
				int32(tt.expectedOpenAIResponse.Usage.CompletionTokens),                        // nolint:gosec
				int32(tt.expectedOpenAIResponse.Usage.TotalTokens),                             // nolint:gosec
				int32(tt.expectedOpenAIResponse.Usage.CompletionTokensDetails.ReasoningTokens), // nolint:gosec
			)
			require.Equal(t, expectedTokenUsage, usedToken)

			if diff := cmp.Diff(tt.expectedOpenAIResponse, gotResp, cmpopts.IgnoreFields(openai.ChatCompletionResponse{}, "Created")); diff != "" {
				t.Errorf("ResponseBody mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenAIToAWSAnthropicTranslator_ResponseError(t *testing.T) {
	tests := []struct {
		name            string
		responseHeaders map[string]string
		inputBody       any
		expectedOutput  openai.Error
	}{
		{
			name: "non-json error response",
			responseHeaders: map[string]string{
				statusHeaderName:      "503",
				contentTypeHeaderName: "text/plain; charset=utf-8",
			},
			inputBody: "Service Unavailable",
			expectedOutput: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    awsBedrockBackendError,
					Code:    ptr.To("503"),
					Message: "Service Unavailable",
				},
			},
		},
		{
			name: "json error response",
			responseHeaders: map[string]string{
				statusHeaderName:       "400",
				contentTypeHeaderName:  "application/json",
				awsErrorTypeHeaderName: "ValidationException",
			},
			inputBody: &awsbedrock.BedrockException{
				Message: "messages: field is required",
			},
			expectedOutput: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "ValidationException",
					Code:    ptr.To("400"),
					Message: "messages: field is required",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reader io.Reader
			if bodyStr, ok := tt.inputBody.(string); ok {
				reader = bytes.NewBufferString(bodyStr)
			} else {
				bodyBytes, err := json.Marshal(tt.inputBody)
				require.NoError(t, err)
				reader = bytes.NewBuffer(bodyBytes)
			}

			o := &openAIToAWSAnthropicTranslatorV1ChatCompletion{}
			hm, body, err := o.ResponseError(tt.responseHeaders, reader)

			require.NoError(t, err)
			require.NotNil(t, body)
			require.NotNil(t, hm)
			require.Len(t, hm, 2)
			require.Equal(t, contentTypeHeaderName, hm[0].Key())
			require.Equal(t, jsonContentType, hm[0].Value()) //nolint:testifylint
			require.Equal(t, contentLengthHeaderName, hm[1].Key())
			require.Equal(t, strconv.Itoa(len(body)), hm[1].Value())

			var gotError openai.Error
			err = json.Unmarshal(body, &gotError)
			require.NoError(t, err)

			if diff := cmp.Diff(tt.expectedOutput, gotError); diff != "" {
				t.Errorf("ResponseError() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestResponseModel_AWSAnthropicStreaming tests that AWS Anthropic streaming returns the request model
// AWS Anthropic uses deterministic model mapping without virtualization
func TestResponseModel_AWSAnthropicStreaming(t *testing.T) {
	modelName := "anthropic.claude-sonnet-4-20250514-v1:0"
	sseStream := `event: message_start
data: {"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-sonnet-4@20250514", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 10, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null}, "usage": {"output_tokens": 5}}

event: message_stop
data: {"type": "message_stop"}

`
	// Wrap SSE data in AWS EventStream format
	eventStreamData, err := wrapAnthropicSSEInEventStream(sseStream)
	require.NoError(t, err)

	openAIReq := &openai.ChatCompletionRequest{
		Stream:    true,
		Model:     modelName,
		MaxTokens: new(int64),
	}

	translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "").(*openAIToAWSAnthropicTranslatorV1ChatCompletion)
	_, _, err = translator.RequestBody(nil, openAIReq, false)
	require.NoError(t, err)

	// Test streaming response - AWS Anthropic doesn't return model in response, uses request model
	_, _, tokenUsage, responseModel, err := translator.ResponseBody(map[string]string{}, bytes.NewReader(eventStreamData), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel) // Returns the request model since no virtualization
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
}

func TestOpenAIToAWSAnthropicTranslatorV1ChatCompletion_ResponseBody_Streaming(t *testing.T) {
	t.Run("handles simple text stream", func(t *testing.T) {
		sseStream := `
event: message_start
data: {"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-opus-4-20250514", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 25, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "!"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null}, "usage": {"output_tokens": 15}}

event: message_stop
data: {"type": "message_stop"}

`
		// Wrap SSE data in AWS EventStream format
		eventStreamData, err := wrapAnthropicSSEInEventStream(sseStream)
		require.NoError(t, err)

		openAIReq := &openai.ChatCompletionRequest{
			Stream:    true,
			Model:     "test-model",
			MaxTokens: new(int64),
		}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "").(*openAIToAWSAnthropicTranslatorV1ChatCompletion)
		_, _, err = translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		_, bm, _, _, err := translator.ResponseBody(map[string]string{}, bytes.NewReader(eventStreamData), true, nil)
		require.NoError(t, err)
		require.NotNil(t, bm)

		bodyStr := string(bm)
		require.Contains(t, bodyStr, `"content":"Hello"`)
		require.Contains(t, bodyStr, `"finish_reason":"stop"`)
		require.Contains(t, bodyStr, `"prompt_tokens":25`)
		require.Contains(t, bodyStr, `"completion_tokens":15`)
		require.Contains(t, bodyStr, string(sseDoneMessage))
		// The ping event is translated into an empty keep-alive chunk and does
		// not consume the role-bearing first chunk: the role still lands on the
		// first real content chunk.
		require.Contains(t, bodyStr, `"delta":{}}`)
		require.Contains(t, bodyStr, `"role":"assistant"`)
	})

	t.Run("emits empty keep-alive chunk on ping", func(t *testing.T) {
		sseStream := `
event: message_start
data: {"type": "message_start", "message": {"id": "msg_ping", "type": "message", "role": "assistant", "content": [], "model": "claude-opus-4-20250514", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 5, "output_tokens": 1}}}

event: ping
data: {"type": "ping"}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hi"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null}, "usage": {"output_tokens": 3}}

event: message_stop
data: {"type": "message_stop"}

`
		eventStreamData, err := wrapAnthropicSSEInEventStream(sseStream)
		require.NoError(t, err)

		openAIReq := &openai.ChatCompletionRequest{Stream: true, Model: "test-model", MaxTokens: new(int64)}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "").(*openAIToAWSAnthropicTranslatorV1ChatCompletion)
		_, _, err = translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		_, bm, _, _, err := translator.ResponseBody(map[string]string{}, bytes.NewReader(eventStreamData), true, nil)
		require.NoError(t, err)
		require.NotNil(t, bm)

		bodyStr := string(bm)
		// Each ping produces exactly one empty-delta keep-alive chunk.
		require.Equal(t, 2, strings.Count(bodyStr, `"delta":{}}`))
		// The empty keep-alive chunks carry the active message ID so downstream
		// clients see a well-formed chunk.
		require.Contains(t, bodyStr, `"id":"msg_ping"`)
		// The assistant role is still emitted on the first real content chunk,
		// not consumed by the preceding ping chunk.
		require.Contains(t, bodyStr, `"role":"assistant"`)
		require.Contains(t, bodyStr, `"content":"Hi"`)
		require.Contains(t, bodyStr, string(sseDoneMessage))
	})

	t.Run("handles tool use stream", func(t *testing.T) {
		sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_014p7gG3wDgGV9EUtLvnow3U","type":"message","role":"assistant","model":"claude-opus-4-20250514","stop_sequence":null,"usage":{"input_tokens":472,"output_tokens":2},"content":[],"stop_reason":null}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Checking weather"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01T1x1fJ34qAmk2tNTrN7Up6","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\": \"San Francisco, CA\", \"unit\": \"fahrenheit\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}

event: message_stop
data: {"type":"message_stop"}
`

		// Wrap SSE data in AWS EventStream format
		eventStreamData, err := wrapAnthropicSSEInEventStream(sseStream)
		require.NoError(t, err)

		openAIReq := &openai.ChatCompletionRequest{Stream: true, Model: "test-model", MaxTokens: new(int64)}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "").(*openAIToAWSAnthropicTranslatorV1ChatCompletion)
		_, _, err = translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		_, bm, _, _, err := translator.ResponseBody(map[string]string{}, bytes.NewReader(eventStreamData), true, nil)
		require.NoError(t, err)
		require.NotNil(t, bm)
		bodyStr := string(bm)

		require.Contains(t, bodyStr, `"content":"Checking weather"`)
		require.Contains(t, bodyStr, `"name":"get_weather"`)
		require.Contains(t, bodyStr, `"finish_reason":"tool_calls"`)
		require.Contains(t, bodyStr, string(sseDoneMessage))
	})
}

func TestAWSAnthropicStreamParser_ErrorHandling(t *testing.T) {
	runStreamErrTest := func(t *testing.T, sseStream string, endOfStream bool) error {
		// Wrap SSE data in AWS EventStream format
		eventStreamData, err := wrapAnthropicSSEInEventStream(sseStream)
		require.NoError(t, err)

		openAIReq := &openai.ChatCompletionRequest{Stream: true, Model: "test-model", MaxTokens: new(int64)}
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "").(*openAIToAWSAnthropicTranslatorV1ChatCompletion)
		_, _, err = translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		_, _, _, _, err = translator.ResponseBody(map[string]string{}, bytes.NewReader(eventStreamData), endOfStream, nil)
		return err
	}

	tests := []struct {
		name          string
		sseStream     string
		endOfStream   bool
		expectedError string
	}{
		{
			name:          "malformed message_start event",
			sseStream:     "event: message_start\ndata: {invalid\n\n",
			expectedError: "unmarshal message_start",
		},
		{
			name:          "malformed content_block_start event",
			sseStream:     "event: content_block_start\ndata: {invalid\n\n",
			expectedError: "failed to unmarshal content_block_start",
		},
		{
			name:          "malformed error event data",
			sseStream:     "event: error\ndata: {invalid\n\n",
			expectedError: "unparsable error event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runStreamErrTest(t, tt.sseStream, tt.endOfStream)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedError)
		})
	}

	t.Run("body read error", func(t *testing.T) {
		parser := newAnthropicStreamParser("test-model")
		_, _, _, _, err := parser.Process(&mockErrorReader{}, false, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read from stream body")
	})
}

func TestOpenAIToAWSAnthropicTranslator_ResponseHeaders(t *testing.T) {
	t.Run("non-streaming request", func(t *testing.T) {
		translator := &openAIToAWSAnthropicTranslatorV1ChatCompletion{
			streamParser: nil, // Not streaming
		}
		headers, err := translator.ResponseHeaders(nil)
		require.NoError(t, err)
		require.Empty(t, headers)
	})

	t.Run("streaming request", func(t *testing.T) {
		translator := &openAIToAWSAnthropicTranslatorV1ChatCompletion{
			streamParser: newAnthropicStreamParser("test-model"),
		}
		headers, err := translator.ResponseHeaders(nil)
		require.NoError(t, err)
		require.Len(t, headers, 1)
		require.Equal(t, contentTypeHeaderName, headers[0].Key())
		require.Equal(t, eventStreamContentType, headers[0].Value())
	})
}

func TestOpenAIToAWSAnthropicTranslator_EdgeCases(t *testing.T) {
	t.Run("response with model field from API", func(t *testing.T) {
		// AWS Anthropic may return model field in response
		modelName := "custom-override-model"
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", modelName)

		req := &openai.ChatCompletionRequest{
			Model:     "original-model",
			MaxTokens: ptr.To(int64(100)),
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.StringOrUserRoleContentUnion{Value: "Test"},
					Role:    openai.ChatMessageRoleUser,
				}},
			},
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		// Response with model field
		anthropicResp := anthropic.Message{
			ID:         "msg_123",
			Model:      "claude-3-opus-20240229",
			Role:       constant.Assistant(anthropic.MessageParamRoleAssistant),
			Content:    []anthropic.ContentBlockUnion{{Type: "text", Text: "Response"}},
			StopReason: anthropic.StopReasonEndTurn,
			Usage:      anthropic.Usage{InputTokens: 5, OutputTokens: 3},
		}

		body, err := json.Marshal(anthropicResp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), true, nil)
		require.NoError(t, err)
		// Should use model from response when available
		assert.Equal(t, anthropicResp.Model, responseModel)
	})

	t.Run("response without model field", func(t *testing.T) {
		// AWS Anthropic typically doesn't return model field
		modelName := "anthropic.claude-3-haiku-20240307-v1:0"
		translator := NewChatCompletionOpenAIToAWSAnthropicTranslator("", modelName)

		req := &openai.ChatCompletionRequest{
			Model:     "original-model",
			MaxTokens: ptr.To(int64(100)),
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.StringOrUserRoleContentUnion{Value: "Test"},
					Role:    openai.ChatMessageRoleUser,
				}},
			},
		}
		_, _, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		// Response without model field (typical for AWS Bedrock)
		anthropicResp := anthropic.Message{
			ID:         "msg_123",
			Role:       constant.Assistant(anthropic.MessageParamRoleAssistant),
			Content:    []anthropic.ContentBlockUnion{{Type: "text", Text: "Response"}},
			StopReason: anthropic.StopReasonEndTurn,
			Usage:      anthropic.Usage{InputTokens: 5, OutputTokens: 3},
		}

		body, err := json.Marshal(anthropicResp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(body), true, nil)
		require.NoError(t, err)
		// Should use request model when response doesn't have model field
		assert.Equal(t, modelName, responseModel)
	})
}
