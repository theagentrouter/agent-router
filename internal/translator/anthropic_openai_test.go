// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestAnthropicToOpenAITranslator_RequestBody(t *testing.T) {
	tests := []struct {
		name          string
		body          *anthropic.MessagesRequest
		modelOverride string
		wantModel     string
		wantStreaming bool
		wantStopSeqs  []string
	}{
		{
			name: "basic request sets correct path header",
			body: &anthropic.MessagesRequest{
				Model:     "claude-3-haiku",
				MaxTokens: 100,
				Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hello"}}},
			},
			wantModel: "claude-3-haiku",
		},
		{
			name: "model override replaces model in body",
			body: &anthropic.MessagesRequest{
				Model:     "claude-3-haiku",
				MaxTokens: 100,
				Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			},
			modelOverride: "gpt-4o",
			wantModel:     "gpt-4o",
		},
		{
			name: "streaming request sets stream and stream_options",
			body: &anthropic.MessagesRequest{
				Model:     "claude-3",
				MaxTokens: 50,
				Stream:    true,
				Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			},
			wantModel:     "claude-3",
			wantStreaming: true,
		},
		{
			name: "stop sequences added to body",
			body: &anthropic.MessagesRequest{
				Model:         "claude-3",
				MaxTokens:     100,
				Messages:      []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
				StopSequences: []string{"Human:", "AI:"},
			},
			wantModel:    "claude-3",
			wantStopSeqs: []string{"Human:", "AI:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToChatCompletionOpenAITranslator("v1", tt.modelOverride)
			headers, body, err := translator.RequestBody(nil, tt.body, false)
			require.NoError(t, err)
			require.NotNil(t, headers)
			require.NotNil(t, body)

			// Verify the two headers: path and content-length.
			require.Len(t, headers, 2)
			assert.Equal(t, pathHeaderName, headers[0].Key())
			assert.Equal(t, "/v1/chat/completions", headers[0].Value())
			assert.Equal(t, contentLengthHeaderName, headers[1].Key())

			// Verify body contains the correct model.
			var req map[string]any
			require.NoError(t, json.Unmarshal(body, &req))
			assert.Equal(t, tt.wantModel, req["model"])

			if tt.wantStreaming {
				assert.Equal(t, true, req["stream"])
				streamOpts, ok := req["stream_options"].(map[string]any)
				require.True(t, ok, "stream_options should be a map")
				assert.Equal(t, true, streamOpts["include_usage"])
			}

			if len(tt.wantStopSeqs) > 0 {
				stopSeqs, ok := req["stop"].([]any)
				require.True(t, ok, "stop should be an array")
				require.Len(t, stopSeqs, len(tt.wantStopSeqs))
				for i, s := range tt.wantStopSeqs {
					assert.Equal(t, s, stopSeqs[i])
				}
			}
		})
	}
}

func TestAnthropicToOpenAITranslator_RequestBody_CustomPrefix(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		wantPath string
	}{
		{name: "standard v1 prefix", prefix: "v1", wantPath: "/v1/chat/completions"},
		{name: "custom prefix with namespace", prefix: "gateway/v1", wantPath: "/gateway/v1/chat/completions"},
		{name: "empty prefix", prefix: "", wantPath: "/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToChatCompletionOpenAITranslator(tt.prefix, "")
			headers, body, err := translator.RequestBody(nil, &anthropic.MessagesRequest{
				Model:     "claude-3",
				MaxTokens: 100,
				Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
			}, false)
			require.NoError(t, err)
			require.NotNil(t, body)
			require.Len(t, headers, 2)
			assert.Equal(t, pathHeaderName, headers[0].Key())
			assert.Equal(t, tt.wantPath, headers[0].Value())
		})
	}
}

func TestAnthropicToOpenAITranslator_ResponseHeaders(t *testing.T) {
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
	headers, err := translator.ResponseHeaders(map[string]string{
		"content-type": "application/json",
		"x-custom":     "value",
	})
	require.NoError(t, err)
	assert.Nil(t, headers, "ResponseHeaders should always return nil for passthrough")
}

func TestAnthropicToOpenAITranslator_ResponseBody_NonStreaming(t *testing.T) {
	t.Run("text content is converted to Anthropic format", func(t *testing.T) {
		translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
		reqBody := &anthropic.MessagesRequest{
			Model:     "claude-3-haiku",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		_, _, err := translator.RequestBody(nil, reqBody, false)
		require.NoError(t, err)

		content := "Hello from OpenAI!"
		openAIResp := openai.ChatCompletionResponse{
			ID:    "chatcmpl-123",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Content: &content,
						Role:    "assistant",
					},
				},
			},
			Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 20},
		}
		respBytes, err := json.Marshal(openAIResp)
		require.NoError(t, err)

		headers, body, tokenUsage, responseModel, err := translator.ResponseBody(
			map[string]string{"content-type": "application/json"},
			bytes.NewReader(respBytes),
			true,
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, body)

		// Response model should come from the OpenAI response.
		assert.Equal(t, "gpt-4o", responseModel)

		// Verify content-length header.
		require.Len(t, headers, 1)
		assert.Equal(t, contentLengthHeaderName, headers[0].Key())

		// Verify token usage extracted correctly.
		inputTokens, inputSet := tokenUsage.InputTokens()
		outputTokens, outputSet := tokenUsage.OutputTokens()
		assert.True(t, inputSet)
		assert.Equal(t, uint32(10), inputTokens)
		assert.True(t, outputSet)
		assert.Equal(t, uint32(20), outputTokens)

		// Verify Anthropic response body.
		var anthropicResp anthropic.MessagesResponse
		require.NoError(t, json.Unmarshal(body, &anthropicResp))
		assert.Equal(t, "chatcmpl-123", anthropicResp.ID)
		assert.Equal(t, "gpt-4o", anthropicResp.Model)
		require.Len(t, anthropicResp.Content, 1)
		require.NotNil(t, anthropicResp.Content[0].Text)
		assert.Equal(t, "Hello from OpenAI!", anthropicResp.Content[0].Text.Text)
		require.NotNil(t, anthropicResp.StopReason)
		assert.Equal(t, anthropic.StopReasonEndTurn, *anthropicResp.StopReason)
	})

	t.Run("model falls back to request model when absent in response", func(t *testing.T) {
		translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
		reqBody := &anthropic.MessagesRequest{
			Model:     "claude-3-haiku",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		_, _, err := translator.RequestBody(nil, reqBody, false)
		require.NoError(t, err)

		content := "Hi!"
		openAIResp := openai.ChatCompletionResponse{
			// No Model field.
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					Message:      openai.ChatCompletionResponseChoiceMessage{Content: &content},
				},
			},
		}
		respBytes, err := json.Marshal(openAIResp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(
			map[string]string{"content-type": "application/json"},
			bytes.NewReader(respBytes),
			true,
			nil,
		)
		require.NoError(t, err)
		assert.Equal(t, "claude-3-haiku", responseModel, "should fall back to request model")
	})

	t.Run("model override is used as fallback", func(t *testing.T) {
		translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "gpt-4o-override")
		reqBody := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
		}
		_, _, err := translator.RequestBody(nil, reqBody, false)
		require.NoError(t, err)

		// OpenAI response with no model field.
		content := "Hi!"
		openAIResp := openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionResponseChoice{
				{Message: openai.ChatCompletionResponseChoiceMessage{Content: &content}},
			},
		}
		respBytes, err := json.Marshal(openAIResp)
		require.NoError(t, err)

		_, _, _, responseModel, err := translator.ResponseBody(
			map[string]string{},
			bytes.NewReader(respBytes),
			true,
			nil,
		)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4o-override", responseModel)
	})

	t.Run("tool call response converted to tool_use blocks", func(t *testing.T) {
		translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
		reqBody := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Get weather"}}},
		}
		_, _, err := translator.RequestBody(nil, reqBody, false)
		require.NoError(t, err)

		toolID := "call-abc"
		openAIResp := openai.ChatCompletionResponse{
			ID:    "chatcmpl-456",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
					Message: openai.ChatCompletionResponseChoiceMessage{
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID: &toolID,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_weather",
									Arguments: `{"location":"NYC"}`,
								},
							},
						},
					},
				},
			},
			Usage: openai.Usage{PromptTokens: 15, CompletionTokens: 8},
		}
		respBytes, err := json.Marshal(openAIResp)
		require.NoError(t, err)

		_, body, _, _, err := translator.ResponseBody(
			map[string]string{"content-type": "application/json"},
			bytes.NewReader(respBytes),
			true,
			nil,
		)
		require.NoError(t, err)

		var anthropicResp anthropic.MessagesResponse
		require.NoError(t, json.Unmarshal(body, &anthropicResp))
		require.Len(t, anthropicResp.Content, 1)
		require.NotNil(t, anthropicResp.Content[0].Tool)
		assert.Equal(t, "call-abc", anthropicResp.Content[0].Tool.ID)
		assert.Equal(t, "get_weather", anthropicResp.Content[0].Tool.Name)
		assert.Equal(t, map[string]any{"location": "NYC"}, anthropicResp.Content[0].Tool.Input)
		require.NotNil(t, anthropicResp.StopReason)
		assert.Equal(t, anthropic.StopReasonToolUse, *anthropicResp.StopReason)
	})
}

func TestAnthropicToOpenAITranslator_ResponseBody_Streaming(t *testing.T) {
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "claude-3-haiku")

	// Initialize streaming mode via RequestBody.
	reqBody := &anthropic.MessagesRequest{
		Model:     "claude-3-haiku",
		MaxTokens: 100,
		Stream:    true,
		Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hello"}}},
	}
	_, _, err := translator.RequestBody(nil, reqBody, false)
	require.NoError(t, err)

	// Feed all OpenAI SSE chunks at once.
	// Chunk 1: text delta → message_start + content_block_start + content_block_delta
	// Chunk 2: finish_reason → stores stop reason
	// Chunk 3: usage-only → content_block_stop + message_delta + message_stop
	// [DONE]: skipped
	input := "data: {\"id\":\"chatcmpl-xyz\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello!\"}}],\"model\":\"gpt-4o\"}\n\n" +
		"data: {\"id\":\"chatcmpl-xyz\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-xyz\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	_, body, tokenUsage, responseModel, err := translator.ResponseBody(
		map[string]string{"content-type": "text/event-stream"},
		strings.NewReader(input),
		true,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, body)

	// Model should come from the OpenAI chunk, not the model override.
	assert.Equal(t, "gpt-4o", responseModel)

	// Verify token usage extracted from the usage chunk.
	inputTokens, inputSet := tokenUsage.InputTokens()
	outputTokens, outputSet := tokenUsage.OutputTokens()
	assert.True(t, inputSet)
	assert.Equal(t, uint32(10), inputTokens)
	assert.True(t, outputSet)
	assert.Equal(t, uint32(5), outputTokens)

	// Verify output SSE event sequence.
	events := parseSSEEventsFromBytes(body)
	require.Len(t, events, 6)
	assert.Equal(t, "message_start", events[0].eventType)
	assert.Equal(t, "content_block_start", events[1].eventType)
	assert.Equal(t, "content_block_delta", events[2].eventType)
	assert.Equal(t, "content_block_stop", events[3].eventType)
	assert.Equal(t, "message_delta", events[4].eventType)
	assert.Equal(t, "message_stop", events[5].eventType)

	// Spot-check specific event data.
	require.JSONEq(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello!"}}`, events[2].data)
	require.JSONEq(t, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":5}}`, events[4].data)
	require.JSONEq(t, `{"type":"message_stop"}`, events[5].data)
}

func TestAnthropicToOpenAITranslator_ResponseBody_StreamingRequestModelFallback(t *testing.T) {
	// When the OpenAI chunk has no model, responseModel should fall back to requestModel.
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "my-override")
	reqBody := &anthropic.MessagesRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Stream:    true,
		Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
	}
	_, _, err := translator.RequestBody(nil, reqBody, false)
	require.NoError(t, err)

	// Usage-only chunk with no model information at all.
	input := "data: {\"id\":\"chatcmpl-noop\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"

	_, _, _, responseModel, err := translator.ResponseBody(
		map[string]string{},
		strings.NewReader(input),
		true,
		nil,
	)
	require.NoError(t, err)
	// Model from chunks is empty, so falls back to modelNameOverride (which is the requestModel stored).
	assert.Equal(t, "my-override", responseModel)
}

func TestAnthropicToOpenAITranslator_ResponseError(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		body        string
		wantErrType string
		wantErrMsg  string
	}{
		{
			name:        "JSON error from OpenAI backend",
			headers:     map[string]string{contentTypeHeaderName: "application/json"},
			body:        `{"type":"error","error":{"type":"invalid_request_error","message":"Bad request"}}`,
			wantErrType: "invalid_request_error",
			wantErrMsg:  "Bad request",
		},
		{
			name:        "JSON error with numeric code from OpenAI-compatible backend",
			headers:     map[string]string{contentTypeHeaderName: "application/json"},
			body:        `{"type":"error","error":{"type":"invalid_request_error","message":"Bad request","param":null,"code":400}}`,
			wantErrType: "invalid_request_error",
			wantErrMsg:  "Bad request",
		},
		{
			name:        "non-JSON 400 error",
			headers:     map[string]string{statusHeaderName: "400", contentTypeHeaderName: "text/plain"},
			body:        "Bad request body",
			wantErrType: "invalid_request_error",
			wantErrMsg:  "Bad request body",
		},
		{
			name:        "non-JSON 401 error",
			headers:     map[string]string{statusHeaderName: "401", contentTypeHeaderName: "text/plain"},
			body:        "Unauthorized",
			wantErrType: "authentication_error",
			wantErrMsg:  "Unauthorized",
		},
		{
			name:        "non-JSON 403 error",
			headers:     map[string]string{statusHeaderName: "403", contentTypeHeaderName: "text/plain"},
			body:        "Forbidden",
			wantErrType: "permission_error",
			wantErrMsg:  "Forbidden",
		},
		{
			name:        "non-JSON 404 error",
			headers:     map[string]string{statusHeaderName: "404", contentTypeHeaderName: "text/plain"},
			body:        "Not found",
			wantErrType: "not_found_error",
			wantErrMsg:  "Not found",
		},
		{
			name:        "non-JSON 413 error",
			headers:     map[string]string{statusHeaderName: "413", contentTypeHeaderName: "text/plain"},
			body:        "Request too large",
			wantErrType: "request_too_large",
			wantErrMsg:  "Request too large",
		},
		{
			name:        "non-JSON 429 error",
			headers:     map[string]string{statusHeaderName: "429", contentTypeHeaderName: "text/plain"},
			body:        "Rate limited",
			wantErrType: "rate_limit_error",
			wantErrMsg:  "Rate limited",
		},
		{
			name:        "non-JSON 500 error",
			headers:     map[string]string{statusHeaderName: "500", contentTypeHeaderName: "text/plain"},
			body:        "Internal error",
			wantErrType: "internal_server_error",
			wantErrMsg:  "Internal error",
		},
		{
			name:        "non-JSON 503 error",
			headers:     map[string]string{statusHeaderName: "503", contentTypeHeaderName: "text/plain"},
			body:        "Service unavailable",
			wantErrType: "service_unavailable_error",
			wantErrMsg:  "Service unavailable",
		},
		{
			name:        "non-JSON 529 error",
			headers:     map[string]string{statusHeaderName: "529", contentTypeHeaderName: "text/plain"},
			body:        "Overloaded",
			wantErrType: "overloaded_error",
			wantErrMsg:  "Overloaded",
		},
		{
			name:        "non-JSON unknown status defaults to internal_server_error",
			headers:     map[string]string{statusHeaderName: "599", contentTypeHeaderName: "text/plain"},
			body:        "Unknown error",
			wantErrType: "internal_server_error",
			wantErrMsg:  "Unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
			headers, mutatedBody, err := translator.ResponseError(tt.headers, strings.NewReader(tt.body))
			require.NoError(t, err)
			require.NotNil(t, mutatedBody)

			// Verify content-type and content-length headers are set.
			require.Len(t, headers, 2)
			assert.Equal(t, contentTypeHeaderName, headers[0].Key())
			assert.Equal(t, jsonContentType, headers[0].Value()) //nolint:testifylint
			assert.Equal(t, contentLengthHeaderName, headers[1].Key())

			// Verify the Anthropic error body.
			var errResp anthropic.ErrorResponse
			require.NoError(t, json.Unmarshal(mutatedBody, &errResp))
			assert.Equal(t, "error", errResp.Type)
			assert.Equal(t, tt.wantErrType, errResp.Error.Type)
			assert.Equal(t, tt.wantErrMsg, errResp.Error.Message)
		})
	}
}

func TestAnthropicToOpenAITranslator_RedactAnthropicBody(t *testing.T) {
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
	tr := translator.(*anthropicToOpenAIV1ChatCompletionTranslator)

	t.Run("nil response returns nil", func(t *testing.T) {
		assert.Nil(t, tr.RedactAnthropicBody(nil))
	})

	t.Run("non-nil response is shallow-copied with content redacted", func(t *testing.T) {
		resp := &anthropic.MessagesResponse{
			ID:    "msg-123",
			Model: "gpt-4o",
			Content: []anthropic.MessagesContentBlock{
				{Text: &anthropic.TextBlock{Type: "text", Text: "some sensitive text"}},
			},
		}
		redacted := tr.RedactAnthropicBody(resp)
		require.NotNil(t, redacted)

		// The original response must not be modified.
		assert.Equal(t, "some sensitive text", resp.Content[0].Text.Text)

		// Top-level non-content fields are preserved.
		assert.Equal(t, "msg-123", redacted.ID)
		assert.Equal(t, "gpt-4o", redacted.Model)

		// Content blocks are present (redaction creates a new slice).
		require.Len(t, redacted.Content, 1)
	})

	t.Run("empty content response is safe", func(t *testing.T) {
		resp := &anthropic.MessagesResponse{
			ID:      "msg-empty",
			Model:   "gpt-4o",
			Content: nil,
		}
		redacted := tr.RedactAnthropicBody(resp)
		require.NotNil(t, redacted)
		assert.Equal(t, "msg-empty", redacted.ID)
		assert.Nil(t, redacted.Content)
	})
}

// mockMessageSpan implements tracingapi.MessageSpan for testing.
type mockMessageSpan struct {
	recordedResponse *anthropic.MessagesResponse
}

func (m *mockMessageSpan) RecordResponse(resp *anthropic.MessagesResponse) {
	m.recordedResponse = resp
}
func (m *mockMessageSpan) RecordResponseChunk(_ *anthropic.MessagesStreamChunk) {}
func (m *mockMessageSpan) EndSpanOnError(_ int, _ []byte)                       {}
func (m *mockMessageSpan) EndSpan()                                             {}

// buildOpenAITextResponse is a helper that marshals a simple OpenAI ChatCompletionResponse
// containing a single text choice and returns it as a bytes.Reader.
func buildOpenAITextResponse(id, model, text string) *bytes.Reader {
	content := text
	resp := openai.ChatCompletionResponse{
		ID:    id,
		Model: model,
		Choices: []openai.ChatCompletionResponseChoice{
			{
				FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				Message:      openai.ChatCompletionResponseChoiceMessage{Content: &content, Role: "assistant"},
			},
		},
		Usage: openai.Usage{PromptTokens: 5, CompletionTokens: 3},
	}
	b, _ := json.Marshal(resp)
	return bytes.NewReader(b)
}

// initNonStreamingTranslator initialises the translator for a basic non-streaming request so
// that requestModel and stream fields are correctly populated before calling ResponseBody.
func initNonStreamingTranslator(t *testing.T, modelOverride string) *anthropicToOpenAIV1ChatCompletionTranslator {
	t.Helper()
	tr := NewAnthropicToChatCompletionOpenAITranslator("v1", modelOverride).(*anthropicToOpenAIV1ChatCompletionTranslator)
	req := &anthropic.MessagesRequest{
		Model:     "claude-3-haiku",
		MaxTokens: 100,
		Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
	}
	_, _, err := tr.RequestBody(nil, req, false)
	require.NoError(t, err)
	return tr
}

// SetRedactionConfig should store all three parameters on the struct.
func TestAnthropicToOpenAITranslator_SetRedactionConfig(t *testing.T) {
	tr := NewAnthropicToChatCompletionOpenAITranslator("v1", "").(*anthropicToOpenAIV1ChatCompletionTranslator)

	assert.False(t, tr.debugLogEnabled)
	assert.False(t, tr.enableRedaction)
	assert.Nil(t, tr.logger)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr.SetRedactionConfig(true, true, logger)

	assert.True(t, tr.debugLogEnabled)
	assert.True(t, tr.enableRedaction)
	assert.Equal(t, logger, tr.logger)
}

// Debug logging should only fire when debugLogEnabled AND enableRedaction AND logger != nil.
func TestAnthropicToOpenAITranslator_ResponseBody_DebugLogging(t *testing.T) {
	makeLogger := func(buf *bytes.Buffer) *slog.Logger {
		return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	t.Run("logs when all conditions are true", func(t *testing.T) {
		var buf bytes.Buffer
		tr := initNonStreamingTranslator(t, "")
		tr.SetRedactionConfig(true, true, makeLogger(&buf))

		_, _, _, _, err := tr.ResponseBody(
			map[string]string{},
			buildOpenAITextResponse("id-1", "gpt-4o", "hello"),
			true,
			nil,
		)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "response body processing")
	})

	t.Run("no log when debugLogEnabled is false", func(t *testing.T) {
		var buf bytes.Buffer
		tr := initNonStreamingTranslator(t, "")
		tr.SetRedactionConfig(false, true, makeLogger(&buf))

		_, _, _, _, err := tr.ResponseBody(
			map[string]string{},
			buildOpenAITextResponse("id-2", "gpt-4o", "hello"),
			true,
			nil,
		)
		require.NoError(t, err)
		assert.Empty(t, buf.String())
	})

	t.Run("no log when enableRedaction is false", func(t *testing.T) {
		var buf bytes.Buffer
		tr := initNonStreamingTranslator(t, "")
		tr.SetRedactionConfig(true, false, makeLogger(&buf))

		_, _, _, _, err := tr.ResponseBody(
			map[string]string{},
			buildOpenAITextResponse("id-3", "gpt-4o", "hello"),
			true,
			nil,
		)
		require.NoError(t, err)
		assert.Empty(t, buf.String())
	})

	t.Run("no log when logger is nil", func(t *testing.T) {
		tr := initNonStreamingTranslator(t, "")
		tr.SetRedactionConfig(true, true, nil)

		// Should not panic even though logger is nil (guarded by the nil check).
		_, _, _, _, err := tr.ResponseBody(
			map[string]string{},
			buildOpenAITextResponse("id-4", "gpt-4o", "hello"),
			true,
			nil,
		)
		require.NoError(t, err)
	})
}

// When a non-nil span is provided, RecordResponse should be called with the converted response.
func TestAnthropicToOpenAITranslator_ResponseBody_SpanRecording(t *testing.T) {
	t.Run("span RecordResponse called with converted response", func(t *testing.T) {
		tr := initNonStreamingTranslator(t, "")
		span := &mockMessageSpan{}

		_, _, _, _, err := tr.ResponseBody(
			map[string]string{},
			buildOpenAITextResponse("chatcmpl-span", "gpt-4o", "span content"),
			true,
			span,
		)
		require.NoError(t, err)
		require.NotNil(t, span.recordedResponse, "RecordResponse should have been called")
		assert.Equal(t, "chatcmpl-span", span.recordedResponse.ID)
	})

	t.Run("nil span does not panic", func(t *testing.T) {
		tr := initNonStreamingTranslator(t, "")

		_, _, _, _, err := tr.ResponseBody(
			map[string]string{},
			buildOpenAITextResponse("chatcmpl-nospan", "gpt-4o", "no span"),
			true,
			nil,
		)
		require.NoError(t, err)
	})
}

func TestAnthropicMessagesToOpenAI_ToolConversation(t *testing.T) {
	t.Run("tool_use in assistant message becomes tool_calls", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "write hello to file"}},
				{
					Role: anthropic.MessageRoleAssistant,
					Content: anthropic.MessageContent{
						Array: []anthropic.ContentBlockParam{
							{ToolUse: &anthropic.ToolUseBlockParam{
								Type: "tool_use", ID: "tool-1", Name: "Write",
								Input: map[string]any{"file_path": "test.txt", "content": "hello"},
							}},
						},
					},
				},
				{
					Role: anthropic.MessageRoleUser,
					Content: anthropic.MessageContent{
						Array: []anthropic.ContentBlockParam{
							{ToolResult: &anthropic.ToolResultBlockParam{
								Type: "tool_result", ToolUseID: "tool-1",
								Content: &anthropic.ToolResultContent{Text: "File written successfully"},
							}},
						},
					},
				},
			},
		}
		msgs := anthropicMessagesToOpenAI(body)
		// system (none), user, assistant with tool_calls, tool result
		require.Len(t, msgs, 3)

		// First message: user
		require.NotNil(t, msgs[0].OfUser)
		assert.Equal(t, "write hello to file", msgs[0].OfUser.Content.Value)

		// Second message: assistant with tool_calls
		require.NotNil(t, msgs[1].OfAssistant)
		require.Len(t, msgs[1].OfAssistant.ToolCalls, 1)
		assert.Equal(t, "Write", msgs[1].OfAssistant.ToolCalls[0].Function.Name)
		assert.Equal(t, "tool-1", *msgs[1].OfAssistant.ToolCalls[0].ID)
		assert.Contains(t, msgs[1].OfAssistant.ToolCalls[0].Function.Arguments, `"file_path"`)

		// Third message: tool result
		require.NotNil(t, msgs[2].OfTool)
		assert.Equal(t, "tool-1", msgs[2].OfTool.ToolCallID)
		assert.Equal(t, "File written successfully", msgs[2].OfTool.Content.Value)
	})

	t.Run("tool_result with error preserves error text", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "do something"}},
				{
					Role: anthropic.MessageRoleAssistant,
					Content: anthropic.MessageContent{
						Array: []anthropic.ContentBlockParam{
							{ToolUse: &anthropic.ToolUseBlockParam{
								Type: "tool_use", ID: "tool-2", Name: "Bash",
								Input: map[string]any{"command": "ls"},
							}},
						},
					},
				},
				{
					Role: anthropic.MessageRoleUser,
					Content: anthropic.MessageContent{
						Array: []anthropic.ContentBlockParam{
							{ToolResult: &anthropic.ToolResultBlockParam{
								Type: "tool_result", ToolUseID: "tool-2",
								Content: &anthropic.ToolResultContent{Text: "permission denied"},
								IsError: true,
							}},
							{Text: &anthropic.TextBlockParam{Type: "text", Text: "try again"}},
						},
					},
				},
			},
		}
		msgs := anthropicMessagesToOpenAI(body)
		// user, assistant, tool result, user text
		require.Len(t, msgs, 4)

		require.NotNil(t, msgs[2].OfTool)
		assert.Equal(t, "tool-2", msgs[2].OfTool.ToolCallID)
		assert.Equal(t, "permission denied", msgs[2].OfTool.Content.Value)

		require.NotNil(t, msgs[3].OfUser)
		assert.Equal(t, "try again", msgs[3].OfUser.Content.Value)
	})

	t.Run("assistant message with text and tool_use", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "help"}},
				{
					Role: anthropic.MessageRoleAssistant,
					Content: anthropic.MessageContent{
						Array: []anthropic.ContentBlockParam{
							{Text: &anthropic.TextBlockParam{Type: "text", Text: "I'll write that for you."}},
							{ToolUse: &anthropic.ToolUseBlockParam{
								Type: "tool_use", ID: "tool-3", Name: "Write",
								Input: map[string]any{"file_path": "out.txt", "content": "data"},
							}},
						},
					},
				},
			},
		}
		msgs := anthropicMessagesToOpenAI(body)
		require.Len(t, msgs, 2)

		assistantMsg := msgs[1].OfAssistant
		require.NotNil(t, assistantMsg)
		assert.Equal(t, "I'll write that for you.", assistantMsg.Content.Value)
		require.Len(t, assistantMsg.ToolCalls, 1)
		assert.Equal(t, "Write", assistantMsg.ToolCalls[0].Function.Name)
	})

	t.Run("plain text messages still work", func(t *testing.T) {
		body := &anthropic.MessagesRequest{
			Model:     "claude-3",
			MaxTokens: 100,
			System:    &anthropic.SystemPrompt{Text: "You are helpful."},
			Messages: []anthropic.MessageParam{
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "hi"}},
				{Role: anthropic.MessageRoleAssistant, Content: anthropic.MessageContent{Text: "hello!"}},
				{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "bye"}},
			},
		}
		msgs := anthropicMessagesToOpenAI(body)
		// system + 3 messages
		require.Len(t, msgs, 4)
		require.NotNil(t, msgs[0].OfSystem)
		require.NotNil(t, msgs[1].OfUser)
		require.NotNil(t, msgs[2].OfAssistant)
		assert.Equal(t, "hello!", msgs[2].OfAssistant.Content.Value)
		require.NotNil(t, msgs[3].OfUser)
	})
}

func TestAnthropicToOpenAITranslator_RequestBody_ThinkingConfig(t *testing.T) {
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
	body := &anthropic.MessagesRequest{
		Model:     "claude-3",
		MaxTokens: 8192,
		Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Think"}}},
		Thinking:  &anthropic.Thinking{Enabled: &anthropic.ThinkingEnabled{Type: "enabled", BudgetTokens: 4096}},
	}
	headers, newBody, err := translator.RequestBody(nil, body, false)
	require.NoError(t, err)
	require.NotNil(t, newBody)
	require.Len(t, headers, 2)

	var req map[string]any
	require.NoError(t, json.Unmarshal(newBody, &req))
	thinking, ok := req["thinking"].(map[string]any)
	require.True(t, ok, "expected thinking config in request body")
	assert.Equal(t, "enabled", thinking["type"])
	assert.Equal(t, float64(4096), thinking["budget_tokens"])
}

func TestAnthropicToOpenAITranslator_RequestBody_MultiTurnWithThinking(t *testing.T) {
	// Multi-turn conversation with thinking blocks in history should not error.
	// This is the primary regression scenario from vLLM's test suite.
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
	body := &anthropic.MessagesRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []anthropic.MessageParam{
			{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Turn 1 question"}},
			{
				Role: anthropic.MessageRoleAssistant,
				Content: anthropic.MessageContent{
					Array: []anthropic.ContentBlockParam{
						{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "Reasoning for turn 1.", Signature: "s_t1"}},
						{Text: &anthropic.TextBlockParam{Type: "text", Text: "Answer for turn 1."}},
					},
				},
			},
			{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Turn 2 question"}},
			{
				Role: anthropic.MessageRoleAssistant,
				Content: anthropic.MessageContent{
					Array: []anthropic.ContentBlockParam{
						{Thinking: &anthropic.ThinkingBlockParam{Type: "thinking", Thinking: "Reasoning for turn 2.", Signature: "s_t2"}},
						{Text: &anthropic.TextBlockParam{Type: "text", Text: "Answer for turn 2."}},
					},
				},
			},
			{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Turn 3 question"}},
		},
	}
	_, newBody, err := translator.RequestBody(nil, body, false)
	require.NoError(t, err)
	require.NotNil(t, newBody)

	// Verify the body can be parsed and contains the expected messages
	var req map[string]any
	require.NoError(t, json.Unmarshal(newBody, &req))
	messages, ok := req["messages"].([]any)
	require.True(t, ok)
	// 3 user + 2 assistant = 5 messages
	assert.Len(t, messages, 5)
}

func TestAnthropicToOpenAITranslator_ResponseBody_NonStreaming_Thinking(t *testing.T) {
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "")
	reqBody := &anthropic.MessagesRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hi"}}},
	}
	_, _, err := translator.RequestBody(nil, reqBody, false)
	require.NoError(t, err)

	content := "The answer is 42."
	openAIResp := openai.ChatCompletionResponse{
		ID:    "chatcmpl-think",
		Model: "gpt-4o",
		Choices: []openai.ChatCompletionResponseChoice{
			{
				FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
				Message: openai.ChatCompletionResponseChoiceMessage{
					Content: &content,
					Role:    "assistant",
					ThinkingBlocks: []openai.ThinkingBlock{
						{Type: "thinking", Thinking: "Let me calculate...", Signature: "sig_calc"},
					},
				},
			},
		},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 20},
	}
	respBytes, err := json.Marshal(openAIResp)
	require.NoError(t, err)

	_, body, _, _, err := translator.ResponseBody(
		map[string]string{"content-type": "application/json"},
		bytes.NewReader(respBytes),
		true,
		nil,
	)
	require.NoError(t, err)

	var anthropicResp anthropic.MessagesResponse
	require.NoError(t, json.Unmarshal(body, &anthropicResp))

	// Should have thinking block before text
	require.Len(t, anthropicResp.Content, 2)
	require.NotNil(t, anthropicResp.Content[0].Thinking)
	assert.Equal(t, "Let me calculate...", anthropicResp.Content[0].Thinking.Thinking)
	assert.Equal(t, "sig_calc", anthropicResp.Content[0].Thinking.Signature)
	require.NotNil(t, anthropicResp.Content[1].Text)
	assert.Equal(t, "The answer is 42.", anthropicResp.Content[1].Text.Text)
}

func TestAnthropicToOpenAITranslator_ResponseBody_Streaming_Thinking(t *testing.T) {
	translator := NewAnthropicToChatCompletionOpenAITranslator("v1", "claude-3")
	reqBody := &anthropic.MessagesRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Stream:    true,
		Messages:  []anthropic.MessageParam{{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Think"}}},
	}
	_, _, err := translator.RequestBody(nil, reqBody, false)
	require.NoError(t, err)

	// Simulate OpenAI streaming with reasoning content followed by text content
	input := `data: {"id":"chatcmpl-sr","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":{"text":"Thinking..."}}}],"model":"gpt-4o"}` + "\n\n" +
		`data: {"id":"chatcmpl-sr","choices":[{"index":0,"delta":{"content":"Answer"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-sr","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-sr","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	_, body, _, _, err := translator.ResponseBody(
		map[string]string{"content-type": "text/event-stream"},
		strings.NewReader(input),
		true,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, body)

	events := parseSSEEventsFromBytes(body)

	// Should contain thinking content block start and text content block start
	var blockTypes []string
	for _, e := range events {
		if e.eventType == "content_block_start" {
			if bytes.Contains([]byte(e.data), []byte(`"thinking"`)) {
				blockTypes = append(blockTypes, "thinking")
			} else if bytes.Contains([]byte(e.data), []byte(`"text"`)) {
				blockTypes = append(blockTypes, "text")
			}
		}
	}
	assert.Equal(t, []string{"thinking", "text"}, blockTypes)
}

// responseBodyStreaming should return an error when streamState is nil.
func TestAnthropicToOpenAITranslator_ResponseBody_StreamStateNilGuard(t *testing.T) {
	tr := NewAnthropicToChatCompletionOpenAITranslator("v1", "").(*anthropicToOpenAIV1ChatCompletionTranslator)
	// Manually enable streaming without initialising streamState to trigger the nil guard.
	tr.stream = true
	tr.streamState = nil

	_, _, _, _, err := tr.ResponseBody(
		map[string]string{},
		strings.NewReader("data: {}\n\n"),
		true,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream state not initialized")
}
