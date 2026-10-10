// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestAnthropicToAWSBedrockTranslator_RequestBody(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")

	req := &anthropicschema.MessagesRequest{
		Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
		MaxTokens: 1024,
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Hello"},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	headers, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)
	require.NotNil(t, headers)
	require.NotNil(t, body)

	// Verify path header.
	assert.Equal(t, pathHeaderName, headers[0].Key())
	assert.Equal(t, "/model/anthropic.claude-3-sonnet-20240229-v1:0/converse", headers[0].Value())

	// Verify content-length header.
	assert.Equal(t, contentLengthHeaderName, headers[1].Key())

	// Verify the body is a valid Bedrock ConverseInput.
	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)
	require.Len(t, bedrockReq.Messages, 1)
	assert.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Messages[0].Role)
	require.Len(t, bedrockReq.Messages[0].Content, 1)
	assert.Equal(t, "Hello", *bedrockReq.Messages[0].Content[0].Text)
	require.NotNil(t, bedrockReq.InferenceConfig)
	assert.Equal(t, int64(1024), *bedrockReq.InferenceConfig.MaxTokens)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_ModelNameOverride(t *testing.T) {
	tests := []struct {
		name           string
		override       string
		inputModel     string
		expectedModel  string
		expectedInPath string
	}{
		{
			name:           "no override uses original model",
			override:       "",
			inputModel:     "anthropic.claude-3-haiku-20240307-v1:0",
			expectedModel:  "anthropic.claude-3-haiku-20240307-v1:0",
			expectedInPath: "anthropic.claude-3-haiku-20240307-v1:0",
		},
		{
			name:           "override replaces model in path",
			override:       "anthropic.claude-3-sonnet-20240229-v1:0",
			inputModel:     "anthropic.claude-3-haiku-20240307-v1:0",
			expectedModel:  "anthropic.claude-3-sonnet-20240229-v1:0",
			expectedInPath: "anthropic.claude-3-sonnet-20240229-v1:0",
		},
		{
			name:           "override with empty input model",
			override:       "anthropic.claude-3-opus-20240229-v1:0",
			inputModel:     "",
			expectedModel:  "anthropic.claude-3-opus-20240229-v1:0",
			expectedInPath: "anthropic.claude-3-opus-20240229-v1:0",
		},
		{
			name:           "model with ARN format",
			override:       "",
			inputModel:     "arn:aws:bedrock:eu-central-1:000000000:application-inference-profile/aaaaaaaaa",
			expectedModel:  "arn:aws:bedrock:eu-central-1:000000000:application-inference-profile/aaaaaaaaa",
			expectedInPath: "arn:aws:bedrock:eu-central-1:000000000:application-inference-profile%2Faaaaaaaaa",
		},
		{
			name:           "global model ID",
			override:       "",
			inputModel:     "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			expectedModel:  "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			expectedInPath: "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSBedrockTranslator(tt.override)

			req := &anthropicschema.MessagesRequest{
				Model:     tt.inputModel,
				MaxTokens: 1024,
				Messages: []anthropicschema.MessageParam{
					{
						Role:    anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{Text: "Hello"},
					},
				},
			}
			rawBody, err := json.Marshal(req)
			require.NoError(t, err)

			headers, _, err := translator.RequestBody(rawBody, req, false)
			require.NoError(t, err)
			require.NotNil(t, headers)

			pathHeader := headers[0]
			require.Equal(t, pathHeaderName, pathHeader.Key())
			expectedPath := "/model/" + tt.expectedInPath + "/converse"
			assert.Equal(t, expectedPath, pathHeader.Value())
		})
	}
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_StreamingPaths(t *testing.T) {
	tests := []struct {
		name               string
		stream             bool
		expectedPathSuffix string
	}{
		{
			name:               "non-streaming uses /converse",
			stream:             false,
			expectedPathSuffix: "/converse",
		},
		{
			name:               "streaming uses /converse-stream",
			stream:             true,
			expectedPathSuffix: "/converse-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSBedrockTranslator("")

			req := &anthropicschema.MessagesRequest{
				Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
				MaxTokens: 1024,
				Stream:    tt.stream,
				Messages: []anthropicschema.MessageParam{
					{
						Role:    anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{Text: "Hello"},
					},
				},
			}
			rawBody, err := json.Marshal(req)
			require.NoError(t, err)

			headers, _, err := translator.RequestBody(rawBody, req, false)
			require.NoError(t, err)

			pathHeader := headers[0]
			expectedPath := "/model/anthropic.claude-3-sonnet-20240229-v1:0" + tt.expectedPathSuffix
			assert.Equal(t, expectedPath, pathHeader.Value())
		})
	}
}

func TestAnthropicToAWSBedrockTranslator_URLEncoding(t *testing.T) {
	tests := []struct {
		name         string
		modelID      string
		expectedPath string
	}{
		{
			name:         "simple model ID with colon",
			modelID:      "anthropic.claude-3-sonnet-20240229-v1:0",
			expectedPath: "/model/anthropic.claude-3-sonnet-20240229-v1:0/converse",
		},
		{
			name:         "full ARN with multiple special characters",
			modelID:      "arn:aws:bedrock:us-east-1:123456789012:foundation-model/anthropic.claude-3-sonnet-20240229-v1:0",
			expectedPath: "/model/arn:aws:bedrock:us-east-1:123456789012:foundation-model%2Fanthropic.claude-3-sonnet-20240229-v1:0/converse",
		},
		{
			name:         "global model prefix",
			modelID:      "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
			expectedPath: "/model/global.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToAWSBedrockTranslator("")

			req := &anthropicschema.MessagesRequest{
				Model:     tt.modelID,
				MaxTokens: 1024,
				Messages: []anthropicschema.MessageParam{
					{
						Role:    anthropicschema.MessageRoleUser,
						Content: anthropicschema.MessageContent{Text: "Hello"},
					},
				},
			}
			rawBody, err := json.Marshal(req)
			require.NoError(t, err)

			headers, _, err := translator.RequestBody(rawBody, req, false)
			require.NoError(t, err)

			pathHeader := headers[0]
			assert.Equal(t, pathHeaderName, pathHeader.Key())
			assert.Equal(t, tt.expectedPath, pathHeader.Value())
		})
	}
}

func TestAnthropicToAWSBedrockTranslator_ResponseHeaders(t *testing.T) {
	t.Run("non-streaming does not modify headers", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		// Not streaming by default.
		headers, err := translator.ResponseHeaders(map[string]string{
			"content-type": "application/json",
		})
		require.NoError(t, err)
		assert.Nil(t, headers)
	})

	t.Run("streaming changes eventstream to text/event-stream", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		// Force streaming mode by calling RequestBody first.
		req := &anthropicschema.MessagesRequest{
			Model:     "test-model",
			MaxTokens: 100,
			Stream:    true,
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		rawBody, _ := json.Marshal(req)
		_, _, _ = translator.RequestBody(rawBody, req, false)

		headers, err := translator.ResponseHeaders(map[string]string{
			"content-type":     "application/vnd.amazon.eventstream",
			"x-amzn-requestid": "test-request-id",
		})
		require.NoError(t, err)
		require.Len(t, headers, 1)
		assert.Equal(t, contentTypeHeaderName, headers[0].Key())
		assert.Equal(t, "text/event-stream", headers[0].Value())
	})
}

func TestAnthropicToAWSBedrockTranslator_ResponseBody_NonStreaming(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	// Set up request model.
	req := &anthropicschema.MessagesRequest{
		Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
		MaxTokens: 1024,
		Messages: []anthropicschema.MessageParam{
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
		},
	}
	rawBody, _ := json.Marshal(req)
	_, _, _ = translator.RequestBody(rawBody, req, false)

	// Set response ID via ResponseHeaders.
	_, _ = translator.ResponseHeaders(map[string]string{
		"x-amzn-requestid": "req-123",
	})

	stopReason := "end_turn"
	bedrockResp := awsbedrock.ConverseResponse{
		Output: &awsbedrock.ConverseOutput{
			Message: awsbedrock.Message{
				Role: "assistant",
				Content: []*awsbedrock.ContentBlock{
					{Text: ptr.To("Hello! How can I help you?")},
				},
			},
		},
		StopReason: &stopReason,
		Usage: &awsbedrock.TokenUsage{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	}
	respBody, _ := json.Marshal(bedrockResp)

	headers, body, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader(respBody), false, nil)
	require.NoError(t, err)
	assert.Equal(t, "anthropic.claude-3-sonnet-20240229-v1:0", responseModel)
	require.NotNil(t, headers)
	require.NotNil(t, body)

	// Verify token usage.
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(20), outputTokens)

	// Verify the response is a valid Anthropic MessagesResponse.
	var anthropicResp anthropicschema.MessagesResponse
	err = json.Unmarshal(body, &anthropicResp)
	require.NoError(t, err)
	assert.Equal(t, "req-123", anthropicResp.ID)
	assert.Equal(t, "anthropic.claude-3-sonnet-20240229-v1:0", anthropicResp.Model)
	assert.Equal(t, anthropicschema.ConstantMessagesResponseRoleAssistant("assistant"), anthropicResp.Role)
	require.NotNil(t, anthropicResp.StopReason)
	assert.Equal(t, anthropicschema.StopReasonEndTurn, *anthropicResp.StopReason)
	require.Len(t, anthropicResp.Content, 1)
	require.NotNil(t, anthropicResp.Content[0].Text)
	assert.Equal(t, "Hello! How can I help you?", anthropicResp.Content[0].Text.Text)
	require.NotNil(t, anthropicResp.Usage)
	assert.Equal(t, float64(10), anthropicResp.Usage.InputTokens)
	assert.Equal(t, float64(20), anthropicResp.Usage.OutputTokens)
}

func TestAnthropicToAWSBedrockTranslator_ResponseBody_Streaming(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	// Set up streaming mode.
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Stream:    true,
		Messages: []anthropicschema.MessageParam{
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
		},
	}
	rawBody, _ := json.Marshal(req)
	_, _, _ = translator.RequestBody(rawBody, req, false)
	_, _ = translator.ResponseHeaders(map[string]string{
		"content-type":     "application/vnd.amazon.eventstream",
		"x-amzn-requestid": "stream-req-123",
	})

	// Build AWS EventStream messages programmatically.
	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{
			eventType: "messageStart",
			payload:   map[string]any{"role": "assistant"},
		},
		{
			eventType: "contentBlockStart",
			payload: map[string]any{
				"contentBlockIndex": 0,
			},
		},
		{
			eventType: "contentBlockDelta",
			payload: map[string]any{
				"contentBlockIndex": 0,
				"delta":             map[string]any{"text": "Hello"},
			},
		},
		{
			eventType: "contentBlockStop",
			payload: map[string]any{
				"contentBlockIndex": 0,
			},
		},
		{
			eventType: "messageStop",
			payload:   map[string]any{"stopReason": "end_turn"},
		},
		{
			eventType: "metadata",
			payload: map[string]any{
				"usage": map[string]any{
					"inputTokens":  5,
					"outputTokens": 3,
					"totalTokens":  8,
				},
			},
		},
	}

	var eventStreamData bytes.Buffer
	for _, ev := range events {
		payload, err := json.Marshal(ev.payload)
		require.NoError(t, err)
		writeEventStreamMessage(t, &eventStreamData, ev.eventType, payload)
	}

	_, body, tokenUsage, responseModel, err := translator.ResponseBody(nil, &eventStreamData, true, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-model", responseModel)

	// Verify token usage from metadata event.
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(5), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(3), outputTokens)

	// Verify SSE output contains expected events.
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "event: message_start")
	assert.Contains(t, bodyStr, "event: content_block_start")
	assert.Contains(t, bodyStr, "event: content_block_delta")
	assert.Contains(t, bodyStr, "event: content_block_stop")
	assert.Contains(t, bodyStr, "event: message_delta")
	assert.Contains(t, bodyStr, "event: message_stop")
	assert.Contains(t, bodyStr, `"text":"Hello"`)
	// Verify usage values are populated from metadata (not hardcoded 0).
	assert.Contains(t, bodyStr, `"input_tokens":5`)
	assert.Contains(t, bodyStr, `"output_tokens":3`)
	// Verify content_block_start has text type (resolved from delta).
	assert.Contains(t, bodyStr, `"content_block_start"`)
	assert.Contains(t, bodyStr, `"type":"text"`)
	assert.Contains(t, bodyStr, `"text":""`)
}

func TestAnthropicToAWSBedrockTranslator_ResponseBody_StreamingThinking(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 16000,
		Stream:    true,
		Messages: []anthropicschema.MessageParam{
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Think"}},
		},
	}
	rawBody, _ := json.Marshal(req)
	_, _, _ = translator.RequestBody(rawBody, req, false)
	_, _ = translator.ResponseHeaders(map[string]string{
		"content-type":     "application/vnd.amazon.eventstream",
		"x-amzn-requestid": "think-req",
	})

	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{
			eventType: "messageStart",
			payload:   map[string]any{"role": "assistant"},
		},
		// Thinking block: contentBlockStart with no Start field, followed by reasoning deltas.
		{
			eventType: "contentBlockStart",
			payload:   map[string]any{"contentBlockIndex": 0},
		},
		{
			eventType: "contentBlockDelta",
			payload: map[string]any{
				"contentBlockIndex": 0,
				"delta":             map[string]any{"reasoningContent": map[string]any{"text": "Let me think..."}},
			},
		},
		{
			eventType: "contentBlockStop",
			payload:   map[string]any{"contentBlockIndex": 0},
		},
		// Text block: contentBlockStart with no Start field, followed by text deltas.
		{
			eventType: "contentBlockStart",
			payload:   map[string]any{"contentBlockIndex": 1},
		},
		{
			eventType: "contentBlockDelta",
			payload: map[string]any{
				"contentBlockIndex": 1,
				"delta":             map[string]any{"text": "Here is my answer."},
			},
		},
		{
			eventType: "contentBlockStop",
			payload:   map[string]any{"contentBlockIndex": 1},
		},
		{
			eventType: "messageStop",
			payload:   map[string]any{"stopReason": "end_turn"},
		},
		{
			eventType: "metadata",
			payload: map[string]any{
				"usage": map[string]any{
					"inputTokens":  10,
					"outputTokens": 25,
					"totalTokens":  35,
				},
			},
		},
	}

	var eventStreamData bytes.Buffer
	for _, ev := range events {
		payload, err := json.Marshal(ev.payload)
		require.NoError(t, err)
		writeEventStreamMessage(t, &eventStreamData, ev.eventType, payload)
	}

	_, body, _, _, err := translator.ResponseBody(nil, &eventStreamData, true, nil)
	require.NoError(t, err)

	bodyStr := string(body)
	// The first content_block_start should be type=thinking (deferred from delta).
	// Check fields independently since JSON map key ordering is not guaranteed.
	assert.Contains(t, bodyStr, `"type":"thinking"`)
	assert.Contains(t, bodyStr, `"thinking":""`)

	// The second content_block_start should be type=text.
	assert.Contains(t, bodyStr, `"type":"text"`)
	assert.Contains(t, bodyStr, `"text":""`)
	// Verify thinking delta is present.
	assert.Contains(t, bodyStr, `"thinking_delta"`)
	assert.Contains(t, bodyStr, `"Let me think..."`)
	// Verify text delta is present.
	assert.Contains(t, bodyStr, `"text_delta"`)
	assert.Contains(t, bodyStr, `"Here is my answer."`)
}

// writeEventStreamMessage encodes a single AWS EventStream message.
func writeEventStreamMessage(t *testing.T, buf *bytes.Buffer, eventType string, payload []byte) {
	t.Helper()
	var msgBuf bytes.Buffer
	enc := eventstream.NewEncoder()
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			eventstream.Header{
				Name:  ":event-type",
				Value: eventstream.StringValue(eventType),
			},
			eventstream.Header{
				Name:  ":content-type",
				Value: eventstream.StringValue("application/json"),
			},
			eventstream.Header{
				Name:  ":message-type",
				Value: eventstream.StringValue("event"),
			},
		},
		Payload: payload,
	}
	err := enc.Encode(&msgBuf, msg)
	require.NoError(t, err)
	buf.Write(msgBuf.Bytes())
}

func TestAnthropicToAWSBedrockTranslator_ResponseError(t *testing.T) {
	t.Run("JSON error body", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		errorBody := `{"message":"Model not found"}`
		headers, body, err := translator.ResponseError(
			map[string]string{
				statusHeaderName:      "404",
				contentTypeHeaderName: "application/json",
			},
			strings.NewReader(errorBody),
		)
		require.NoError(t, err)
		require.NotNil(t, headers)
		require.NotNil(t, body)

		var errResp anthropicschema.ErrorResponse
		err = json.Unmarshal(body, &errResp)
		require.NoError(t, err)
		assert.Equal(t, "error", errResp.Type)
		assert.Equal(t, "not_found_error", errResp.Error.Type)
		assert.Equal(t, "Model not found", errResp.Error.Message)
	})

	t.Run("non-JSON error body", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		headers, body, err := translator.ResponseError(
			map[string]string{
				statusHeaderName:      "503",
				contentTypeHeaderName: "text/plain",
			},
			strings.NewReader("Service Unavailable"),
		)
		require.NoError(t, err)
		require.NotNil(t, headers)

		var errResp anthropicschema.ErrorResponse
		err = json.Unmarshal(body, &errResp)
		require.NoError(t, err)
		assert.Equal(t, "error", errResp.Type)
		assert.Equal(t, "service_unavailable_error", errResp.Error.Type)
		assert.Equal(t, "Service Unavailable", errResp.Error.Message)
	})

	t.Run("status code mappings", func(t *testing.T) {
		tests := []struct {
			status       string
			expectedType string
		}{
			{"400", "invalid_request_error"},
			{"401", "authentication_error"},
			{"403", "permission_error"},
			{"404", "not_found_error"},
			{"429", "rate_limit_error"},
			{"500", "internal_server_error"},
			{"503", "service_unavailable_error"},
			{"502", "internal_server_error"}, // default
		}
		for _, tt := range tests {
			t.Run(tt.status, func(t *testing.T) {
				translator := NewAnthropicToAWSBedrockTranslator("")
				_, body, err := translator.ResponseError(
					map[string]string{
						statusHeaderName:      tt.status,
						contentTypeHeaderName: "text/plain",
					},
					strings.NewReader("error"),
				)
				require.NoError(t, err)
				var errResp anthropicschema.ErrorResponse
				err = json.Unmarshal(body, &errResp)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedType, errResp.Error.Type)
			})
		}
	})
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_SystemPrompt(t *testing.T) {
	t.Run("simple text system prompt", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		req := &anthropicschema.MessagesRequest{
			Model:     "test-model",
			MaxTokens: 100,
			System:    &anthropicschema.SystemPrompt{Text: "You are a helpful assistant."},
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		rawBody, _ := json.Marshal(req)
		_, body, err := translator.RequestBody(rawBody, req, false)
		require.NoError(t, err)

		var bedrockReq awsbedrock.ConverseInput
		err = json.Unmarshal(body, &bedrockReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.System, 1)
		assert.Equal(t, "You are a helpful assistant.", *bedrockReq.System[0].Text)
	})

	t.Run("array system prompt", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		req := &anthropicschema.MessagesRequest{
			Model:     "test-model",
			MaxTokens: 100,
			System: &anthropicschema.SystemPrompt{
				Texts: []anthropicschema.TextBlockParam{
					{Text: "First instruction.", Type: "text"},
					{Text: "Second instruction.", Type: "text"},
				},
			},
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		rawBody, _ := json.Marshal(req)
		_, body, err := translator.RequestBody(rawBody, req, false)
		require.NoError(t, err)

		var bedrockReq awsbedrock.ConverseInput
		err = json.Unmarshal(body, &bedrockReq)
		require.NoError(t, err)
		require.Len(t, bedrockReq.System, 2)
		assert.Equal(t, "First instruction.", *bedrockReq.System[0].Text)
		assert.Equal(t, "Second instruction.", *bedrockReq.System[1].Text)
	})
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_Tools(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	temp := 0.7
	req := &anthropicschema.MessagesRequest{
		Model:       "test-model",
		MaxTokens:   100,
		Temperature: &temp,
		Tools: []anthropicschema.ToolUnion{
			{
				Tool: &anthropicschema.Tool{
					Type:        "custom",
					Name:        "get_weather",
					Description: "Get the weather",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`),
				},
			},
		},
		ToolChoice: &anthropicschema.ToolChoice{
			Auto: &anthropicschema.ToolChoiceAuto{Type: "auto"},
		},
		Messages: []anthropicschema.MessageParam{
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "What's the weather?"}},
		},
	}
	rawBody, _ := json.Marshal(req)
	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)

	require.NotNil(t, bedrockReq.ToolConfig)
	require.Len(t, bedrockReq.ToolConfig.Tools, 1)
	assert.Equal(t, "get_weather", *bedrockReq.ToolConfig.Tools[0].ToolSpec.Name)
	assert.Equal(t, "Get the weather", *bedrockReq.ToolConfig.Tools[0].ToolSpec.Description)
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice)
	assert.NotNil(t, bedrockReq.ToolConfig.ToolChoice.Auto)

	// Verify temperature.
	require.NotNil(t, bedrockReq.InferenceConfig)
	require.NotNil(t, bedrockReq.InferenceConfig.Temperature)
	assert.Equal(t, 0.7, *bedrockReq.InferenceConfig.Temperature)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_Thinking(t *testing.T) {
	t.Run("thinking enabled", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		req := &anthropicschema.MessagesRequest{
			Model:     "test-model",
			MaxTokens: 16000,
			Thinking: &anthropicschema.Thinking{
				Enabled: &anthropicschema.ThinkingEnabled{
					Type:         "enabled",
					BudgetTokens: 10000,
				},
			},
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Think about this"}},
			},
		}
		rawBody, _ := json.Marshal(req)
		_, body, err := translator.RequestBody(rawBody, req, false)
		require.NoError(t, err)

		var bedrockReq awsbedrock.ConverseInput
		err = json.Unmarshal(body, &bedrockReq)
		require.NoError(t, err)
		require.NotNil(t, bedrockReq.AdditionalModelRequestFields)
		thinking, ok := bedrockReq.AdditionalModelRequestFields["thinking"]
		require.True(t, ok)
		thinkingMap, ok := thinking.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "enabled", thinkingMap["type"])
		assert.Equal(t, float64(10000), thinkingMap["budget_tokens"])
	})

	t.Run("thinking disabled", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		req := &anthropicschema.MessagesRequest{
			Model:     "test-model",
			MaxTokens: 100,
			Thinking: &anthropicschema.Thinking{
				Disabled: &anthropicschema.ThinkingDisabled{Type: "disabled"},
			},
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
			},
		}
		rawBody, _ := json.Marshal(req)
		_, body, err := translator.RequestBody(rawBody, req, false)
		require.NoError(t, err)

		var bedrockReq awsbedrock.ConverseInput
		err = json.Unmarshal(body, &bedrockReq)
		require.NoError(t, err)
		require.NotNil(t, bedrockReq.AdditionalModelRequestFields)
		thinking, ok := bedrockReq.AdditionalModelRequestFields["thinking"]
		require.True(t, ok)
		thinkingMap, ok := thinking.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "disabled", thinkingMap["type"])
	})

	t.Run("top_k in additional fields", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		topK := 40
		req := &anthropicschema.MessagesRequest{
			Model:     "test-model",
			MaxTokens: 100,
			TopK:      &topK,
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
			},
		}
		rawBody, _ := json.Marshal(req)
		_, body, err := translator.RequestBody(rawBody, req, false)
		require.NoError(t, err)

		var bedrockReq awsbedrock.ConverseInput
		err = json.Unmarshal(body, &bedrockReq)
		require.NoError(t, err)
		require.NotNil(t, bedrockReq.AdditionalModelRequestFields)
		topKVal, ok := bedrockReq.AdditionalModelRequestFields["top_k"]
		require.True(t, ok)
		assert.Equal(t, float64(40), topKVal)
	})
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_AssistantMessage(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 16000,
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Hello"},
			},
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Thinking: &anthropicschema.ThinkingBlockParam{
							Type:      "thinking",
							Thinking:  "Let me reason about this.",
							Signature: "sig123",
						}},
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "response text"}},
						{ToolUse: &anthropicschema.ToolUseBlockParam{
							Type:  "tool_use",
							ID:    "tu_1",
							Name:  "calculator",
							Input: map[string]any{"expr": "1+1"},
						}},
						{RedactedThinking: &anthropicschema.RedactedThinkingBlockParam{
							Type: "redacted_thinking",
							Data: "redacted-data",
						}},
					},
				},
			},
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Thanks"},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)
	require.Len(t, bedrockReq.Messages, 3)

	// Check assistant message.
	assistantMsg := bedrockReq.Messages[1]
	assert.Equal(t, awsbedrock.ConversationRoleAssistant, assistantMsg.Role)
	require.Len(t, assistantMsg.Content, 4)

	// Thinking block.
	require.NotNil(t, assistantMsg.Content[0].ReasoningContent)
	require.NotNil(t, assistantMsg.Content[0].ReasoningContent.ReasoningText)
	assert.Equal(t, "Let me reason about this.", assistantMsg.Content[0].ReasoningContent.ReasoningText.Text)
	assert.Equal(t, "sig123", assistantMsg.Content[0].ReasoningContent.ReasoningText.Signature)

	// Text block.
	require.NotNil(t, assistantMsg.Content[1].Text)
	assert.Equal(t, "response text", *assistantMsg.Content[1].Text)

	// Tool use block.
	require.NotNil(t, assistantMsg.Content[2].ToolUse)
	assert.Equal(t, "calculator", assistantMsg.Content[2].ToolUse.Name)
	assert.Equal(t, "tu_1", assistantMsg.Content[2].ToolUse.ToolUseID)

	// Redacted thinking block.
	require.NotNil(t, assistantMsg.Content[3].ReasoningContent)
	assert.Equal(t, []byte("redacted-data"), assistantMsg.Content[3].ReasoningContent.RedactedContent)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_UserArrayContent(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	// Use a valid base64-encoded 1x1 PNG pixel.
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	req := &anthropicschema.MessagesRequest{
		Model:         "test-model",
		MaxTokens:     100,
		StopSequences: []string{"END", "STOP"},
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Describe this image"}},
						{Image: &anthropicschema.ImageBlockParam{
							Type: "image",
							Source: anthropicschema.ImageSource{
								Base64: &anthropicschema.Base64ImageSource{
									Type:      "base64",
									MediaType: "image/png",
									Data:      pngBase64,
								},
							},
						}},
					},
				},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)
	require.Len(t, bedrockReq.Messages, 1)

	userMsg := bedrockReq.Messages[0]
	assert.Equal(t, awsbedrock.ConversationRoleUser, userMsg.Role)
	require.Len(t, userMsg.Content, 2)

	// Text block.
	require.NotNil(t, userMsg.Content[0].Text)
	assert.Equal(t, "Describe this image", *userMsg.Content[0].Text)

	// Image block.
	require.NotNil(t, userMsg.Content[1].Image)
	assert.Equal(t, "png", userMsg.Content[1].Image.Format)
	assert.NotEmpty(t, userMsg.Content[1].Image.Source.Bytes)

	// Verify stop sequences.
	require.NotNil(t, bedrockReq.InferenceConfig)
	assert.Equal(t, []string{"END", "STOP"}, bedrockReq.InferenceConfig.StopSequences)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_UserArrayContentError(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Image: &anthropicschema.ImageBlockParam{
							Type: "image",
							Source: anthropicschema.ImageSource{
								Base64: &anthropicschema.Base64ImageSource{
									Type:      "base64",
									MediaType: "application/pdf",
									Data:      "not-used",
								},
							},
						}},
					},
				},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, _, err = translator.RequestBody(rawBody, req, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported image format application/pdf")
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_ToolResultMessages(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Tools: []anthropicschema.ToolUnion{
			{Tool: &anthropicschema.Tool{
				Type:        "custom",
				Name:        "get_weather",
				Description: "Get weather",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		ToolChoice: &anthropicschema.ToolChoice{
			Any: &anthropicschema.ToolChoiceAny{Type: "any"},
		},
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "What's the weather?"},
			},
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolUse: &anthropicschema.ToolUseBlockParam{
							Type:  "tool_use",
							ID:    "tu_abc",
							Name:  "get_weather",
							Input: map[string]any{"city": "NYC"},
						}},
					},
				},
			},
			// First tool result message.
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_abc",
							Content:   &anthropicschema.ToolResultContent{Text: "72°F and sunny"},
						}},
					},
				},
			},
			// Second consecutive tool result message (should be coalesced).
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_def",
							IsError:   true,
							Content: &anthropicschema.ToolResultContent{
								Array: []anthropicschema.ToolResultContentItem{
									{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Error: city not found"}},
								},
							},
						}},
					},
				},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)

	// Should be: user, assistant, user (coalesced tool results).
	// Consecutive tool_result messages are coalesced into a single Bedrock message.
	require.Len(t, bedrockReq.Messages, 3)

	// Coalesced tool result message contains both tool results.
	toolResultMsg := bedrockReq.Messages[2]
	assert.Equal(t, awsbedrock.ConversationRoleUser, toolResultMsg.Role)
	require.Len(t, toolResultMsg.Content, 2)

	// First tool result.
	require.NotNil(t, toolResultMsg.Content[0].ToolResult)
	assert.Equal(t, "tu_abc", *toolResultMsg.Content[0].ToolResult.ToolUseID)
	require.Len(t, toolResultMsg.Content[0].ToolResult.Content, 1)
	assert.Equal(t, "72°F and sunny", *toolResultMsg.Content[0].ToolResult.Content[0].Text)

	// Second tool result (error) coalesced into the same message.
	require.NotNil(t, toolResultMsg.Content[1].ToolResult)
	assert.Equal(t, "tu_def", *toolResultMsg.Content[1].ToolResult.ToolUseID)
	assert.Equal(t, "error", *toolResultMsg.Content[1].ToolResult.Status)
	require.Len(t, toolResultMsg.Content[1].ToolResult.Content, 1)
	assert.Equal(t, "Error: city not found", *toolResultMsg.Content[1].ToolResult.Content[0].Text)

	// Verify tool choice "any".
	require.NotNil(t, bedrockReq.ToolConfig)
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice)
	assert.NotNil(t, bedrockReq.ToolConfig.ToolChoice.Any)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_ToolResultMessagesWithSystemMessages(t *testing.T) {
	// Regression test: when system messages are present in the messages array,
	// promoteAnthropicSystemMessagesToParam filters them out, creating a shorter
	// `messages` slice. The coalescing loop must use the filtered `messages` slice
	// (not body.Messages) to avoid index misalignment and potential out-of-bounds access.
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Tools: []anthropicschema.ToolUnion{
			{Tool: &anthropicschema.Tool{
				Type:        "custom",
				Name:        "get_weather",
				Description: "Get weather",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		Messages: []anthropicschema.MessageParam{
			// System message at the start — will be promoted out of the messages slice.
			{
				Role:    "system",
				Content: anthropicschema.MessageContent{Text: "You are a helpful assistant."},
			},
			// Another system message — also promoted.
			{
				Role:    "system",
				Content: anthropicschema.MessageContent{Text: "Always be concise."},
			},
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "What's the weather?"},
			},
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolUse: &anthropicschema.ToolUseBlockParam{
							Type:  "tool_use",
							ID:    "tu_abc",
							Name:  "get_weather",
							Input: map[string]any{"city": "NYC"},
						}},
					},
				},
			},
			// First tool result message.
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_abc",
							Content:   &anthropicschema.ToolResultContent{Text: "72°F and sunny"},
						}},
					},
				},
			},
			// Second consecutive tool result message (should be coalesced with the first).
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_def",
							IsError:   true,
							Content: &anthropicschema.ToolResultContent{
								Array: []anthropicschema.ToolResultContentItem{
									{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Error: city not found"}},
								},
							},
						}},
					},
				},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)

	// After promoting 2 system messages, messages has 4 entries:
	//   user, assistant, user(tool_result), user(tool_result)
	// The two consecutive tool_result messages should be coalesced into one,
	// yielding 3 Bedrock messages: user, assistant, user(coalesced tool results).
	require.Len(t, bedrockReq.Messages, 3)

	// First message: user text.
	assert.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Messages[0].Role)
	require.Len(t, bedrockReq.Messages[0].Content, 1)
	assert.NotNil(t, bedrockReq.Messages[0].Content[0].Text)

	// Second message: assistant tool_use.
	assert.Equal(t, awsbedrock.ConversationRoleAssistant, bedrockReq.Messages[1].Role)

	// Third message: coalesced tool results (both tool_use_ids in one message).
	toolResultMsg := bedrockReq.Messages[2]
	assert.Equal(t, awsbedrock.ConversationRoleUser, toolResultMsg.Role)
	require.Len(t, toolResultMsg.Content, 2)
	require.NotNil(t, toolResultMsg.Content[0].ToolResult)
	assert.Equal(t, "tu_abc", *toolResultMsg.Content[0].ToolResult.ToolUseID)
	assert.Equal(t, "72°F and sunny", *toolResultMsg.Content[0].ToolResult.Content[0].Text)
	require.NotNil(t, toolResultMsg.Content[1].ToolResult)
	assert.Equal(t, "tu_def", *toolResultMsg.Content[1].ToolResult.ToolUseID)
	assert.Equal(t, "error", *toolResultMsg.Content[1].ToolResult.Status)
	assert.Equal(t, "Error: city not found", *toolResultMsg.Content[1].ToolResult.Content[0].Text)

	// Verify system prompts were promoted, each as its own block.
	require.NotNil(t, bedrockReq.System)
	require.Len(t, bedrockReq.System, 2)
	assert.Equal(t, "You are a helpful assistant.", *bedrockReq.System[0].Text)
	assert.Equal(t, "Always be concise.", *bedrockReq.System[1].Text)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_SingleToolResultNotCoalesced(t *testing.T) {
	// Verify that a single tool_result message (without a consecutive one) is NOT coalesced
	// but still goes through the tool result path (not convertUserMessage).
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Tools: []anthropicschema.ToolUnion{
			{Tool: &anthropicschema.Tool{
				Type:        "custom",
				Name:        "get_weather",
				Description: "Get weather",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "What's the weather?"},
			},
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolUse: &anthropicschema.ToolUseBlockParam{
							Type:  "tool_use",
							ID:    "tu_abc",
							Name:  "get_weather",
							Input: map[string]any{"city": "NYC"},
						}},
					},
				},
			},
			// Single tool result — no consecutive tool result to coalesce with.
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_abc",
							Content:   &anthropicschema.ToolResultContent{Text: "72°F and sunny"},
						}},
					},
				},
			},
			// Follow-up user text message — should NOT be coalesced with tool result.
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Thanks!"},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)

	// 4 messages: user, assistant, user(tool_result), user(text)
	require.Len(t, bedrockReq.Messages, 4)

	// Third message: tool result (not coalesced).
	toolResultMsg := bedrockReq.Messages[2]
	assert.Equal(t, awsbedrock.ConversationRoleUser, toolResultMsg.Role)
	require.Len(t, toolResultMsg.Content, 1)
	require.NotNil(t, toolResultMsg.Content[0].ToolResult)
	assert.Equal(t, "tu_abc", *toolResultMsg.Content[0].ToolResult.ToolUseID)

	// Fourth message: plain text (not coalesced with tool result).
	textMsg := bedrockReq.Messages[3]
	assert.Equal(t, awsbedrock.ConversationRoleUser, textMsg.Role)
	require.Len(t, textMsg.Content, 1)
	assert.NotNil(t, textMsg.Content[0].Text)
	assert.Equal(t, "Thanks!", *textMsg.Content[0].Text)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_UnexpectedRole(t *testing.T) {
	// Verify that an unexpected role returns an error.
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Hello"},
			},
			{
				Role:    "system", // Will be promoted to system param.
				Content: anthropicschema.MessageContent{Text: "You are helpful."},
			},
			{
				Role:    "invalid_role",
				Content: anthropicschema.MessageContent{Text: "This should fail"},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, _, err = translator.RequestBody(rawBody, req, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected role: invalid_role")
}

func TestAnthropicToAWSBedrockTranslator_ResponseBody_NonStreamingToolUseAndReasoning(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 4096,
		Messages: []anthropicschema.MessageParam{
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Use tools"}},
		},
	}
	rawBody, _ := json.Marshal(req)
	_, _, _ = translator.RequestBody(rawBody, req, false)
	_, _ = translator.ResponseHeaders(map[string]string{
		"x-amzn-requestid": "resp-456",
	})

	cacheRead := int64(50)
	cacheWrite := int64(10)
	stopReason := "tool_use"
	bedrockResp := awsbedrock.ConverseResponse{
		Output: &awsbedrock.ConverseOutput{
			Message: awsbedrock.Message{
				Role: "assistant",
				Content: []*awsbedrock.ContentBlock{
					{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							ReasoningText: &awsbedrock.ReasoningTextBlock{
								Text:      "I should use the calculator tool.",
								Signature: "reasoning-sig",
							},
						},
					},
					{
						ToolUse: &awsbedrock.ToolUseBlock{
							Name:      "calculator",
							ToolUseID: "tu_resp",
							Input:     map[string]any{"expr": "2+2"},
						},
					},
					{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							RedactedContent: []byte("secret-reasoning"),
						},
					},
				},
			},
		},
		StopReason: &stopReason,
		Usage: &awsbedrock.TokenUsage{
			InputTokens:           15,
			OutputTokens:          30,
			TotalTokens:           45,
			CacheReadInputTokens:  &cacheRead,
			CacheWriteInputTokens: &cacheWrite,
		},
	}
	respBody, _ := json.Marshal(bedrockResp)

	_, body, tokenUsage, _, err := translator.ResponseBody(nil, bytes.NewReader(respBody), false, nil)
	require.NoError(t, err)

	// Verify cache token usage is extracted.
	// ExtractTokenUsageFromExplicitCaching sums: inputTokens + cacheRead + cacheWrite = 15 + 50 + 10 = 75
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(75), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	assert.Equal(t, uint32(30), outputTokens)

	var anthropicResp anthropicschema.MessagesResponse
	err = json.Unmarshal(body, &anthropicResp)
	require.NoError(t, err)

	require.NotNil(t, anthropicResp.StopReason)
	assert.Equal(t, anthropicschema.StopReasonToolUse, *anthropicResp.StopReason)
	require.Len(t, anthropicResp.Content, 3)

	// Reasoning block.
	require.NotNil(t, anthropicResp.Content[0].Thinking)
	assert.Equal(t, "thinking", anthropicResp.Content[0].Thinking.Type)
	assert.Equal(t, "I should use the calculator tool.", anthropicResp.Content[0].Thinking.Thinking)
	assert.Equal(t, "reasoning-sig", anthropicResp.Content[0].Thinking.Signature)

	// Tool use block.
	require.NotNil(t, anthropicResp.Content[1].Tool)
	assert.Equal(t, "tool_use", anthropicResp.Content[1].Tool.Type)
	assert.Equal(t, "calculator", anthropicResp.Content[1].Tool.Name)
	assert.Equal(t, "tu_resp", anthropicResp.Content[1].Tool.ID)

	// Redacted thinking block.
	require.NotNil(t, anthropicResp.Content[2].RedactedThinking)
	assert.Equal(t, "redacted_thinking", anthropicResp.Content[2].RedactedThinking.Type)

	// Verify cache usage in response.
	require.NotNil(t, anthropicResp.Usage)
	assert.Equal(t, float64(50), anthropicResp.Usage.CacheReadInputTokens)
	assert.Equal(t, float64(10), anthropicResp.Usage.CacheCreationInputTokens)
}

func TestAnthropicToAWSBedrockTranslator_ResponseBody_StreamingToolUse(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Stream:    true,
		Messages: []anthropicschema.MessageParam{
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Use a tool"}},
		},
	}
	rawBody, _ := json.Marshal(req)
	_, _, _ = translator.RequestBody(rawBody, req, false)
	_, _ = translator.ResponseHeaders(map[string]string{
		"content-type":     "application/vnd.amazon.eventstream",
		"x-amzn-requestid": "tool-stream-req",
	})

	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{
			eventType: "messageStart",
			payload:   map[string]any{"role": "assistant"},
		},
		{
			eventType: "contentBlockStart",
			payload: map[string]any{
				"contentBlockIndex": 0,
				"start":             map[string]any{"toolUse": map[string]any{"name": "get_weather", "toolUseId": "tu_stream"}},
			},
		},
		{
			eventType: "contentBlockDelta",
			payload: map[string]any{
				"contentBlockIndex": 0,
				"delta":             map[string]any{"toolUse": map[string]any{"input": `{"city":"LA"}`}},
			},
		},
		{
			eventType: "contentBlockStop",
			payload:   map[string]any{"contentBlockIndex": 0},
		},
		{
			eventType: "messageStop",
			payload:   map[string]any{"stopReason": "tool_use"},
		},
		{
			eventType: "metadata",
			payload: map[string]any{
				"usage": map[string]any{"inputTokens": 8, "outputTokens": 12, "totalTokens": 20},
			},
		},
	}

	var eventStreamData bytes.Buffer
	for _, ev := range events {
		payload, err := json.Marshal(ev.payload)
		require.NoError(t, err)
		writeEventStreamMessage(t, &eventStreamData, ev.eventType, payload)
	}

	_, body, _, _, err := translator.ResponseBody(nil, &eventStreamData, true, nil)
	require.NoError(t, err)

	bodyStr := string(body)
	assert.Contains(t, bodyStr, "event: content_block_start")
	assert.Contains(t, bodyStr, `"type":"tool_use"`)
	assert.Contains(t, bodyStr, `"name":"get_weather"`)
	assert.Contains(t, bodyStr, `"id":"tu_stream"`)
	assert.Contains(t, bodyStr, "event: content_block_delta")
	assert.Contains(t, bodyStr, `"input_json_delta"`)
	assert.Contains(t, bodyStr, `"tool_use"`)
}

func TestPromoteAnthropicSystemMessagesToParam(t *testing.T) {
	t.Run("no system messages returns all messages", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			Messages: []anthropicschema.MessageParam{
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
			},
		}
		result, system := promoteAnthropicSystemMessagesToParam(body)
		require.Len(t, result, 1)
		require.Nil(t, system)
		require.Nil(t, body.System)
	})

	t.Run("single text system message is promoted", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			Messages: []anthropicschema.MessageParam{
				{Role: "system", Content: anthropicschema.MessageContent{Text: "You are helpful."}},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		result, system := promoteAnthropicSystemMessagesToParam(body)
		require.Len(t, result, 1)
		require.Equal(t, "user", string(result[0].Role))
		require.NotNil(t, system)
		require.Equal(t, []anthropicschema.TextBlockParam{{Type: "text", Text: "You are helpful."}}, system.Texts)
		require.Nil(t, body.System, "the shared request body must not be mutated")
	})

	t.Run("system message with content blocks is promoted", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			Messages: []anthropicschema.MessageParam{
				{
					Role: "system",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{
							{Text: &anthropicschema.TextBlockParam{Text: "You are Claude."}},
						},
					},
				},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		result, system := promoteAnthropicSystemMessagesToParam(body)
		require.Len(t, result, 1)
		require.NotNil(t, system)
		require.Equal(t, []anthropicschema.TextBlockParam{{Text: "You are Claude."}}, system.Texts)
	})

	t.Run("multiple system messages each become a block", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			Messages: []anthropicschema.MessageParam{
				{Role: "system", Content: anthropicschema.MessageContent{Text: "Be concise."}},
				{Role: "system", Content: anthropicschema.MessageContent{Text: "Be accurate."}},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
			},
		}
		result, system := promoteAnthropicSystemMessagesToParam(body)
		require.Len(t, result, 1)
		require.NotNil(t, system)
		require.Equal(t, []anthropicschema.TextBlockParam{
			{Type: "text", Text: "Be concise."},
			{Type: "text", Text: "Be accurate."},
		}, system.Texts)
	})

	t.Run("system message is added to existing string system param", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			System: &anthropicschema.SystemPrompt{Text: "You are Claude."},
			Messages: []anthropicschema.MessageParam{
				{Role: "system", Content: anthropicschema.MessageContent{Text: "Be concise."}},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		result, system := promoteAnthropicSystemMessagesToParam(body)
		require.Len(t, result, 1)
		require.NotNil(t, system)
		// Both survive, top-level first.
		require.Equal(t, []anthropicschema.TextBlockParam{
			{Type: "text", Text: "You are Claude."},
			{Type: "text", Text: "Be concise."},
		}, system.Texts)
		require.Empty(t, system.Text, "the string form must be folded into the blocks, convertSystemPrompt returns early on it")
		require.Equal(t, "You are Claude.", body.System.Text, "the shared request body must not be mutated")
	})

	t.Run("system message is added to existing system block array", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			System: &anthropicschema.SystemPrompt{Texts: []anthropicschema.TextBlockParam{
				{Type: "text", Text: "TOP LEVEL SYSTEM"},
			}},
			Messages: []anthropicschema.MessageParam{
				{Role: "system", Content: anthropicschema.MessageContent{Text: "MID CONVERSATION SYSTEM"}},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		result, system := promoteAnthropicSystemMessagesToParam(body)
		require.Len(t, result, 1)
		require.NotNil(t, system)
		require.Equal(t, []anthropicschema.TextBlockParam{
			{Type: "text", Text: "TOP LEVEL SYSTEM"},
			{Type: "text", Text: "MID CONVERSATION SYSTEM"},
		}, system.Texts)
		require.Equal(t, []anthropicschema.TextBlockParam{{Type: "text", Text: "TOP LEVEL SYSTEM"}}, body.System.Texts,
			"the shared request body must not be mutated")
	})

	t.Run("cache_control survives promotion", func(t *testing.T) {
		ephemeral := &anthropicschema.CacheControl{
			Ephemeral: &anthropicschema.CacheControlEphemeral{Type: "ephemeral"},
		}
		body := &anthropicschema.MessagesRequest{
			Messages: []anthropicschema.MessageParam{
				{
					Role: "system",
					Content: anthropicschema.MessageContent{
						Array: []anthropicschema.ContentBlockParam{
							{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "cached sys prompt", CacheControl: ephemeral}},
						},
					},
				},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		_, system := promoteAnthropicSystemMessagesToParam(body)
		require.NotNil(t, system)
		require.Len(t, system.Texts, 1)
		require.Equal(t, ephemeral, system.Texts[0].CacheControl,
			"a joined string cannot carry per-block cache control")
	})

	t.Run("repeated promotion yields the same system prompt", func(t *testing.T) {
		body := &anthropicschema.MessagesRequest{
			System: &anthropicschema.SystemPrompt{Texts: []anthropicschema.TextBlockParam{
				{Type: "text", Text: "TOP LEVEL SYSTEM"},
			}},
			Messages: []anthropicschema.MessageParam{
				{Role: "system", Content: anthropicschema.MessageContent{Text: "MID CONVERSATION SYSTEM"}},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
			},
		}
		_, first := promoteAnthropicSystemMessagesToParam(body)
		_, second := promoteAnthropicSystemMessagesToParam(body)
		require.Equal(t, first, second,
			"promotion must not accumulate across attempts")
	})

	t.Run("integration with full request", func(t *testing.T) {
		translator := NewAnthropicToAWSBedrockTranslator("")
		req := &anthropicschema.MessagesRequest{
			Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
			MaxTokens: 1024,
			Messages: []anthropicschema.MessageParam{
				{Role: "system", Content: anthropicschema.MessageContent{Text: "You are helpful."}},
				{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
			},
		}
		rawBody, err := json.Marshal(req)
		require.NoError(t, err)

		_, body, err := translator.RequestBody(rawBody, req, false)
		require.NoError(t, err)

		var bedrockReq awsbedrock.ConverseInput
		err = json.Unmarshal(body, &bedrockReq)
		require.NoError(t, err)

		require.Len(t, bedrockReq.Messages, 1)
		assert.Equal(t, awsbedrock.ConversationRoleUser, bedrockReq.Messages[0].Role)
		require.NotNil(t, bedrockReq.System)
		require.Len(t, bedrockReq.System, 1)
		assert.Equal(t, "You are helpful.", *bedrockReq.System[0].Text)
	})
}

// A retry or backend fallback translates the very same parsed struct again: processor_impl.go
// passes routerProcessor.originalRequestBody to RequestBody on every upstream attempt.
func TestAnthropicToAWSBedrockTranslator_RequestBody_SystemPromotionSurvivesRetry(t *testing.T) {
	body := &anthropicschema.MessagesRequest{
		Model:     "anthropic.claude-3-sonnet-20240229-v1:0",
		MaxTokens: 1024,
		System: &anthropicschema.SystemPrompt{Texts: []anthropicschema.TextBlockParam{
			{Type: "text", Text: "TOP LEVEL SYSTEM"},
		}},
		Messages: []anthropicschema.MessageParam{
			{Role: "system", Content: anthropicschema.MessageContent{Text: "MID CONVERSATION SYSTEM"}},
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hi"}},
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	systemTexts := func(t *testing.T) []string {
		// A fresh translator per attempt, matching SetBackend creating one per upstream filter.
		_, newBody, err := NewAnthropicToAWSBedrockTranslator("").RequestBody(rawBody, body, true)
		require.NoError(t, err)
		var bedrockReq awsbedrock.ConverseInput
		require.NoError(t, json.Unmarshal(newBody, &bedrockReq))
		texts := make([]string, 0, len(bedrockReq.System))
		for _, block := range bedrockReq.System {
			texts = append(texts, *block.Text)
		}
		return texts
	}

	want := []string{"TOP LEVEL SYSTEM", "MID CONVERSATION SYSTEM"}
	require.Equal(t, want, systemTexts(t))
	require.Equal(t, want, systemTexts(t), "a retry must not append the promoted blocks a second time")
	require.Equal(t, want, systemTexts(t))
}

func TestIsOnlyToolResult(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
		msg      anthropicschema.MessageParam
	}{
		{
			name:     "single tool_result block",
			expected: true,
			msg: anthropicschema.MessageParam{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_1",
							Content:   &anthropicschema.ToolResultContent{Text: "result"},
						}},
					},
				},
			},
		},
		{
			name:     "multiple tool_result blocks",
			expected: true,
			msg: anthropicschema.MessageParam{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_1",
							Content:   &anthropicschema.ToolResultContent{Text: "result 1"},
						}},
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_2",
							Content:   &anthropicschema.ToolResultContent{Text: "result 2"},
						}},
					},
				},
			},
		},
		{
			name:     "mixed text and tool_result blocks",
			expected: false,
			msg: anthropicschema.MessageParam{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Here is the result:"}},
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_1",
							Content:   &anthropicschema.ToolResultContent{Text: "72°F and sunny"},
						}},
					},
				},
			},
		},
		{
			name:     "plain text user message",
			expected: false,
			msg: anthropicschema.MessageParam{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "Hello"},
			},
		},
		{
			name:     "assistant message is never tool-result-only",
			expected: false,
			msg: anthropicschema.MessageParam{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_1",
							Content:   &anthropicschema.ToolResultContent{Text: "result"},
						}},
					},
				},
			},
		},
		{
			name:     "empty content array",
			expected: false,
			msg: anthropicschema.MessageParam{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{},
			},
		},
		{
			name:     "text array without tool_result",
			expected: false,
			msg: anthropicschema.MessageParam{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Hello"}},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isOnlyToolResult(&tt.msg))
		})
	}
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_MixedUserContentWithToolResult(t *testing.T) {
	// Regression test: a user message containing both text and tool_result blocks
	// must preserve all content blocks, not silently drop the text.
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role:    anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{Text: "What's the weather?"},
			},
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolUse: &anthropicschema.ToolUseBlockParam{
							Type:  "tool_use",
							ID:    "tu_abc",
							Name:  "get_weather",
							Input: map[string]any{"city": "NYC"},
						}},
					},
				},
			},
			// Mixed content: text + tool_result in the same user message.
			// This should go through convertUserMessage (not convertToolResultMessage),
			// preserving the text block.
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "Here is the result:"}},
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:      "tool_result",
							ToolUseID: "tu_abc",
							Content:   &anthropicschema.ToolResultContent{Text: "72°F and sunny"},
						}},
					},
				},
			},
		},
	}
	rawBody, err := json.Marshal(req)
	require.NoError(t, err)

	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	err = json.Unmarshal(body, &bedrockReq)
	require.NoError(t, err)

	// 3 messages: user(text), assistant(tool_use), user(text+tool_result)
	require.Len(t, bedrockReq.Messages, 3)

	// The third message should have both a text block and a tool_result block.
	mixedMsg := bedrockReq.Messages[2]
	assert.Equal(t, awsbedrock.ConversationRoleUser, mixedMsg.Role)
	require.Len(t, mixedMsg.Content, 2, "mixed user message should preserve both text and tool_result blocks")

	// First block: text.
	require.NotNil(t, mixedMsg.Content[0].Text)
	assert.Equal(t, "Here is the result:", *mixedMsg.Content[0].Text)

	// Second block: tool result.
	require.NotNil(t, mixedMsg.Content[1].ToolResult)
	assert.Equal(t, "tu_abc", *mixedMsg.Content[1].ToolResult.ToolUseID)
	require.Len(t, mixedMsg.Content[1].ToolResult.Content, 1)
	assert.Equal(t, "72°F and sunny", *mixedMsg.Content[1].ToolResult.Content[0].Text)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_CacheControl(t *testing.T) {
	ephemeral := func() *anthropicschema.CacheControl {
		return &anthropicschema.CacheControl{Ephemeral: &anthropicschema.CacheControlEphemeral{Type: "ephemeral"}}
	}

	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		System: &anthropicschema.SystemPrompt{
			Texts: []anthropicschema.TextBlockParam{
				{Type: "text", Text: "You are a helpful assistant", CacheControl: ephemeral()},
			},
		},
		Tools: []anthropicschema.ToolUnion{
			{
				Tool: &anthropicschema.Tool{
					Type:         "custom",
					Name:         "get_weather",
					Description:  "Get the weather",
					InputSchema:  json.RawMessage(`{"type":"object"}`),
					CacheControl: ephemeral(),
				},
			},
		},
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "cache this", CacheControl: ephemeral()}},
					},
				},
			},
			{
				Role: anthropicschema.MessageRoleAssistant,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "and this", CacheControl: ephemeral()}},
					},
				},
			},
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{ToolResult: &anthropicschema.ToolResultBlockParam{
							Type:         "tool_result",
							ToolUseID:    "tu_abc",
							Content:      &anthropicschema.ToolResultContent{Text: "72°F and sunny"},
							CacheControl: ephemeral(),
						}},
					},
				},
			},
		},
	}

	rawBody, err := json.Marshal(req)
	require.NoError(t, err)
	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	require.NoError(t, json.Unmarshal(body, &bedrockReq))

	// System: text block followed by its own cache point block.
	require.Len(t, bedrockReq.System, 2)
	require.NotNil(t, bedrockReq.System[0].Text)
	require.Nil(t, bedrockReq.System[1].Text)
	require.NotNil(t, bedrockReq.System[1].CachePoint)
	assert.Equal(t, "default", bedrockReq.System[1].CachePoint.Type)

	// Tools: tool spec followed by its own cache point element. Bedrock's Tool is a union, so the
	// cache point element must not carry a toolSpec key at all.
	require.Len(t, bedrockReq.ToolConfig.Tools, 2)
	require.NotNil(t, bedrockReq.ToolConfig.Tools[0].ToolSpec)
	require.Nil(t, bedrockReq.ToolConfig.Tools[0].CachePoint)
	require.Nil(t, bedrockReq.ToolConfig.Tools[1].ToolSpec)
	require.NotNil(t, bedrockReq.ToolConfig.Tools[1].CachePoint)
	rawTool, err := json.Marshal(bedrockReq.ToolConfig.Tools[1])
	require.NoError(t, err)
	assert.NotContains(t, string(rawTool), "toolSpec")

	// User text, assistant text and tool result each get their own trailing cache point block.
	require.Len(t, bedrockReq.Messages, 3)
	for i, msg := range bedrockReq.Messages {
		require.Len(t, msg.Content, 2, "messages[%d] should have a content block and a cache point block", i)
		require.NotNil(t, msg.Content[1].CachePoint, "messages[%d] is missing its cache point block", i)
		assert.Equal(t, "default", msg.Content[1].CachePoint.Type)
	}
	require.NotNil(t, bedrockReq.Messages[0].Content[0].Text)
	require.NotNil(t, bedrockReq.Messages[1].Content[0].Text)
	require.NotNil(t, bedrockReq.Messages[2].Content[0].ToolResult)
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_NoCacheControl(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		System: &anthropicschema.SystemPrompt{
			Texts: []anthropicschema.TextBlockParam{{Type: "text", Text: "You are a helpful assistant"}},
		},
		Tools: []anthropicschema.ToolUnion{
			{Tool: &anthropicschema.Tool{Type: "custom", Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		},
		Messages: []anthropicschema.MessageParam{
			{
				Role: anthropicschema.MessageRoleUser,
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{Type: "text", Text: "hello"}},
					},
				},
			},
		},
	}

	rawBody, err := json.Marshal(req)
	require.NoError(t, err)
	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "cachePoint")
}

func TestAnthropicToAWSBedrockTranslator_RequestBody_CacheControlOnPromotedSystemMessage(t *testing.T) {
	translator := NewAnthropicToAWSBedrockTranslator("")
	req := &anthropicschema.MessagesRequest{
		Model:     "test-model",
		MaxTokens: 100,
		Messages: []anthropicschema.MessageParam{
			{
				Role: "system",
				Content: anthropicschema.MessageContent{
					Array: []anthropicschema.ContentBlockParam{
						{Text: &anthropicschema.TextBlockParam{
							Type:         "text",
							Text:         "You are a helpful assistant",
							CacheControl: &anthropicschema.CacheControl{Ephemeral: &anthropicschema.CacheControlEphemeral{Type: "ephemeral"}},
						}},
					},
				},
			},
			{Role: anthropicschema.MessageRoleUser, Content: anthropicschema.MessageContent{Text: "Hello"}},
		},
	}

	rawBody, err := json.Marshal(req)
	require.NoError(t, err)
	_, body, err := translator.RequestBody(rawBody, req, false)
	require.NoError(t, err)

	var bedrockReq awsbedrock.ConverseInput
	require.NoError(t, json.Unmarshal(body, &bedrockReq))

	require.Len(t, bedrockReq.System, 2)
	require.NotNil(t, bedrockReq.System[0].Text)
	assert.Equal(t, "You are a helpful assistant", *bedrockReq.System[0].Text)
	require.NotNil(t, bedrockReq.System[1].CachePoint)
	assert.Equal(t, "default", bedrockReq.System[1].CachePoint.Type)
}
