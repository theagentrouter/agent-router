// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	anthropicVertex "github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	openaigo "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	claudeTestModel = "claude-3-opus-20240229"
	testTool        = "test_123"
)

// TestResponseModel_GCPAnthropic tests that GCP Anthropic (non-streaming) returns the request model
// GCP Anthropic uses deterministic model mapping without virtualization
func TestResponseModel_GCPAnthropic(t *testing.T) {
	modelName := "claude-sonnet-4@20250514"
	translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", modelName)

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

	// GCP Anthropic response doesn't have model field, uses Anthropic format
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

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_RequestBody(t *testing.T) {
	// Define a common input request to use for both standard and vertex tests.
	openAIReq := &openai.ChatCompletionRequest{
		Model: claudeTestModel,
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
	t.Run("Vertex Values Configured Correctly", func(t *testing.T) {
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		hm, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)
		require.NotNil(t, body)

		// Check the path header.
		pathHeader := hm[0]
		require.Equal(t, pathHeaderName, pathHeader.Key())
		expectedPath := fmt.Sprintf("publishers/anthropic/models/%s:rawPredict", openAIReq.Model)
		require.Equal(t, expectedPath, pathHeader.Value())

		// Check the body content.

		require.NotNil(t, body)
		// Model should NOT be present in the body for GCP Vertex.
		require.False(t, gjson.GetBytes(body, "model").Exists())
		// Anthropic version should be present for GCP Vertex.
		require.Equal(t, anthropicVertex.DefaultVersion, gjson.GetBytes(body, "anthropic_version").String())
	})

	t.Run("Model Name Override", func(t *testing.T) {
		overrideModelName := "claude-3"
		// Instantiate the translator with the model name override.
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", overrideModelName)

		// Call RequestBody with the original request, which has a different model name.
		hm, _, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)

		// Check that the :path header uses the override model name.
		pathHeader := hm[0]
		require.Equal(t, pathHeaderName, pathHeader.Key())
		expectedPath := fmt.Sprintf("publishers/anthropic/models/%s:rawPredict", overrideModelName)
		require.Equal(t, expectedPath, pathHeader.Value())
	})

	t.Run("Image Content Request", func(t *testing.T) {
		imageReq := &openai.ChatCompletionRequest{
			MaxCompletionTokens: ptr.To(int64(200)),
			Model:               "claude-3-opus-20240229",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.StringOrUserRoleContentUnion{
							Value: []openai.ChatCompletionContentPartUserUnionParam{
								{OfText: &openai.ChatCompletionContentPartTextParam{Text: "What is in this image?"}},
								{OfImageURL: &openai.ChatCompletionContentPartImageParam{
									ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
										URL: "data:image/jpeg;base64,dGVzdA==", // "test" in base64.
									},
								}},
							},
						},
						Role: openai.ChatMessageRoleUser,
					},
				},
			},
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, imageReq, false)
		require.NoError(t, err)

		imageBlock := gjson.GetBytes(body, "messages.0.content.1")
		require.Equal(t, "image", imageBlock.Get("type").String())
		require.Equal(t, "base64", imageBlock.Get("source.type").String())
		require.Equal(t, "image/jpeg", imageBlock.Get("source.media_type").String())
		require.Equal(t, "dGVzdA==", imageBlock.Get("source.data").String())
	})

	t.Run("Multiple System Prompts Concatenated", func(t *testing.T) {
		firstMsg := "First system prompt."
		secondMsg := "Second developer prompt."
		thirdMsg := "Hello!"
		multiSystemReq := &openai.ChatCompletionRequest{
			Model: claudeTestModel,
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfSystem: &openai.ChatCompletionSystemMessageParam{Content: openai.ContentUnion{Value: firstMsg}, Role: openai.ChatMessageRoleSystem}},
				{OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{Content: openai.ContentUnion{Value: secondMsg}, Role: openai.ChatMessageRoleDeveloper}},
				{OfUser: &openai.ChatCompletionUserMessageParam{Content: openai.StringOrUserRoleContentUnion{Value: thirdMsg}, Role: openai.ChatMessageRoleUser}},
			},
			MaxTokens: ptr.To(int64(100)),
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, multiSystemReq, false)
		require.NoError(t, err)

		require.Equal(t, firstMsg, gjson.GetBytes(body, "system.0.text").String())
		require.Equal(t, secondMsg, gjson.GetBytes(body, "system.1.text").String())
		require.Equal(t, thirdMsg, gjson.GetBytes(body, "messages.0.content.0.text").String())
	})

	t.Run("Streaming Request Validation", func(t *testing.T) {
		streamReq := &openai.ChatCompletionRequest{
			Model:     claudeTestModel,
			Messages:  []openai.ChatCompletionMessageParamUnion{},
			MaxTokens: ptr.To(int64(100)),
			Stream:    true,
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		hm, body, err := translator.RequestBody(nil, streamReq, false)
		require.NoError(t, err)
		require.NotNil(t, hm)

		// Check that the :path header uses the streamRawPredict specifier.
		pathHeader := hm
		require.Equal(t, pathHeaderName, pathHeader[0].Key())
		expectedPath := fmt.Sprintf("publishers/anthropic/models/%s:streamRawPredict", streamReq.Model)
		require.Equal(t, expectedPath, pathHeader[0].Value())

		require.True(t, gjson.GetBytes(body, "stream").Bool(), `body should contain "stream": true`)
	})

	t.Run("Test message param", func(t *testing.T) {
		openaiRequest := &openai.ChatCompletionRequest{
			Model:       claudeTestModel,
			Messages:    []openai.ChatCompletionMessageParamUnion{},
			Temperature: ptr.To(0.1),
			MaxTokens:   ptr.To(int64(100)),
			TopP:        ptr.To(0.1),
			Stop: openaigo.ChatCompletionNewParamsStopUnion{
				OfStringArray: []string{"stop1", "stop2"},
			},
		}
		messageParam, err := buildAnthropicParams(openaiRequest, filterapi.APISchemaGCPAnthropic, "")
		require.NoError(t, err)
		require.Equal(t, int64(100), messageParam.MaxTokens)
		require.Equal(t, "0.1", messageParam.TopP.String())
		require.Equal(t, "0.1", messageParam.Temperature.String())
		require.Equal(t, []string{"stop1", "stop2"}, messageParam.StopSequences)
	})

	t.Run("Test single stop", func(t *testing.T) {
		openaiRequest := &openai.ChatCompletionRequest{
			Model:       claudeTestModel,
			Messages:    []openai.ChatCompletionMessageParamUnion{},
			Temperature: ptr.To(0.1),
			MaxTokens:   ptr.To(int64(100)),
			TopP:        ptr.To(0.1),
			Stop: openaigo.ChatCompletionNewParamsStopUnion{
				OfString: openaigo.Opt[string]("stop1"),
			},
		}
		messageParam, err := buildAnthropicParams(openaiRequest, filterapi.APISchemaGCPAnthropic, "")
		require.NoError(t, err)
		require.Equal(t, int64(100), messageParam.MaxTokens)
		require.Equal(t, "0.1", messageParam.TopP.String())
		require.Equal(t, "0.1", messageParam.Temperature.String())
		require.Equal(t, []string{"stop1"}, messageParam.StopSequences)
	})

	t.Run("Invalid Temperature (above bound)", func(t *testing.T) {
		invalidTempReq := &openai.ChatCompletionRequest{
			Model:       claudeTestModel,
			Messages:    []openai.ChatCompletionMessageParamUnion{},
			MaxTokens:   ptr.To(int64(100)),
			Temperature: ptr.To(2.5),
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, _, err := translator.RequestBody(nil, invalidTempReq, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})

	t.Run("Invalid Temperature (below bound)", func(t *testing.T) {
		invalidTempReq := &openai.ChatCompletionRequest{
			Model:       claudeTestModel,
			Messages:    []openai.ChatCompletionMessageParamUnion{},
			MaxTokens:   ptr.To(int64(100)),
			Temperature: ptr.To(-2.5),
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, _, err := translator.RequestBody(nil, invalidTempReq, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})

	t.Run("Missing MaxTokens Passes With Zero", func(t *testing.T) {
		missingTokensReq := &openai.ChatCompletionRequest{
			Model:    claudeTestModel,
			Messages: []openai.ChatCompletionMessageParamUnion{},
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, missingTokensReq, false)
		require.NoError(t, err)
		require.Equal(t, int64(0), gjson.GetBytes(body, "max_tokens").Int())
	})
	t.Run("API Version Override", func(t *testing.T) {
		customAPIVersion := "bedrock-2023-05-31"
		// Instantiate the translator with the custom API version.
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator(customAPIVersion, "")

		// Call RequestBody with a standard request.
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		// Check that the anthropic_version in the body uses the custom version.

		require.Equal(t, customAPIVersion, gjson.GetBytes(body, "anthropic_version").String())
	})
	t.Run("Request with Thinking enabled", func(t *testing.T) {
		thinkingReq := &openai.ChatCompletionRequest{
			Model:     claudeTestModel,
			Messages:  []openai.ChatCompletionMessageParamUnion{},
			MaxTokens: ptr.To(int64(100)),
			Thinking: &openai.ThinkingUnion{
				OfEnabled: &openai.ThinkingEnabled{
					BudgetTokens:    100,
					Type:            "enabled",
					IncludeThoughts: true,
				},
			},
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, thinkingReq, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		require.NotNil(t, body)

		thinkingBlock := gjson.GetBytes(body, "thinking")
		require.True(t, thinkingBlock.Exists(), "The 'thinking' field should exist in the request body")
		require.True(t, thinkingBlock.IsObject(), "The 'thinking' field should be a JSON object")
		require.Equal(t, "enabled", thinkingBlock.Map()["type"].String())
	})
	t.Run("Request with Thinking disabled", func(t *testing.T) {
		thinkingReq := &openai.ChatCompletionRequest{
			Model:     claudeTestModel,
			Messages:  []openai.ChatCompletionMessageParamUnion{},
			MaxTokens: ptr.To(int64(100)),
			Thinking: &openai.ThinkingUnion{
				OfDisabled: &openai.ThinkingDisabled{
					Type: "disabled",
				},
			},
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, thinkingReq, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		require.NotNil(t, body)

		thinkingBlock := gjson.GetBytes(body, "thinking")
		require.True(t, thinkingBlock.Exists(), "The 'thinking' field should exist in the request body")
		require.True(t, thinkingBlock.IsObject(), "The 'thinking' field should be a JSON object")
		require.Equal(t, "disabled", thinkingBlock.Map()["type"].String())
	})
	t.Run("Request with Thinking enabled and display", func(t *testing.T) {
		thinkingReq := &openai.ChatCompletionRequest{
			Model:     claudeTestModel,
			Messages:  []openai.ChatCompletionMessageParamUnion{},
			MaxTokens: ptr.To(int64(100)),
			Thinking: &openai.ThinkingUnion{
				OfEnabled: &openai.ThinkingEnabled{
					BudgetTokens: 100,
					Type:         "enabled",
					Display:      "omitted",
				},
			},
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, thinkingReq, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		thinkingBlock := gjson.GetBytes(body, "thinking")
		require.True(t, thinkingBlock.Exists(), "The 'thinking' field should exist in the request body")
		require.True(t, thinkingBlock.IsObject(), "The 'thinking' field should be a JSON object")
		require.Equal(t, "enabled", thinkingBlock.Map()["type"].String())
		require.Equal(t, "omitted", thinkingBlock.Map()["display"].String())
	})
	t.Run("Request with Thinking adaptive and display", func(t *testing.T) {
		thinkingReq := &openai.ChatCompletionRequest{
			Model:     claudeTestModel,
			Messages:  []openai.ChatCompletionMessageParamUnion{},
			MaxTokens: ptr.To(int64(100)),
			Thinking: &openai.ThinkingUnion{
				OfAdaptive: &openai.ThinkingAdaptive{
					Type:    "adaptive",
					Display: "summarized",
				},
			},
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, thinkingReq, false)
		require.NoError(t, err)
		require.NotNil(t, body)

		thinkingBlock := gjson.GetBytes(body, "thinking")
		require.True(t, thinkingBlock.Exists(), "The 'thinking' field should exist in the request body")
		require.True(t, thinkingBlock.IsObject(), "The 'thinking' field should be a JSON object")
		require.Equal(t, "adaptive", thinkingBlock.Map()["type"].String())
		require.Equal(t, "summarized", thinkingBlock.Map()["display"].String())
	})
}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_ResponseBody(t *testing.T) {
	t.Run("invalid json body", func(t *testing.T) {
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
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
				Usage:      anthropic.Usage{InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 5, CacheCreationInputTokens: 3},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ123",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens:     18,
					CompletionTokens: 20,
					TotalTokens:      38,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens:        5,
						CacheCreationTokens: 3,
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
					{Type: "tool_use", ID: "toolu_01", Name: "get_weather", Input: []byte(`{"location":"Tokyo","unit":"celsius"}`)},
				},
				StopReason: anthropic.StopReasonToolUse,
				Usage:      anthropic.Usage{InputTokens: 25, OutputTokens: 15, CacheReadInputTokens: 10, CacheCreationInputTokens: 7},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ123",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens: 42, CompletionTokens: 15, TotalTokens: 57,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens:        10,
						CacheCreationTokens: 7,
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
										Arguments: `{"location":"Tokyo","unit":"celsius"}`,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "response with model field set",
			inputResponse: &anthropic.Message{
				ID:         "msg_01XYZ123",
				Model:      "claude-3-5-sonnet-20241022",
				Role:       constant.Assistant(anthropic.MessageParamRoleAssistant),
				Content:    []anthropic.ContentBlockUnion{{Type: "text", Text: "Model field test response."}},
				StopReason: anthropic.StopReasonEndTurn,
				Usage:      anthropic.Usage{InputTokens: 8, OutputTokens: 12, CacheReadInputTokens: 2},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ123",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens:     10,
					CompletionTokens: 12,
					TotalTokens:      22,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens: 2,
					},
					CompletionTokensDetails: &openai.CompletionTokensDetails{},
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index:        0,
						Message:      openai.ChatCompletionResponseChoiceMessage{Role: "assistant", Content: ptr.To("Model field test response.")},
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					},
				},
			},
		},
		{
			name: "response with thinking content",
			inputResponse: &anthropic.Message{
				ID:         "msg_01XYZ456",
				Model:      "claude-3-5-sonnet-20241022",
				Role:       constant.Assistant(anthropic.MessageParamRoleAssistant),
				Content:    []anthropic.ContentBlockUnion{{Type: "thinking", Thinking: "Let me think about this...", Signature: "signature_123"}},
				StopReason: anthropic.StopReasonEndTurn,
				Usage:      anthropic.Usage{InputTokens: 15, OutputTokens: 25, CacheReadInputTokens: 3},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ456",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens:     18,
					CompletionTokens: 25,
					TotalTokens:      43,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens: 3,
					},
					CompletionTokensDetails: &openai.CompletionTokensDetails{},
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index: 0,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Role: "assistant",
							ReasoningContent: &openai.ReasoningContentUnion{
								Value: &openai.ReasoningContent{
									ReasoningContent: &awsbedrock.ReasoningContentBlock{
										ReasoningText: &awsbedrock.ReasoningTextBlock{
											Text:      "Let me think about this...",
											Signature: "signature_123",
										},
									},
								},
							},
						},
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					},
				},
			},
		},
		{
			name: "response with redacted thinking content",
			inputResponse: &anthropic.Message{
				ID:         "msg_01XYZ789",
				Model:      "claude-3-5-sonnet-20241022",
				Role:       constant.Assistant(anthropic.MessageParamRoleAssistant),
				Content:    []anthropic.ContentBlockUnion{{Type: "redacted_thinking", Data: "redacted_data_content"}},
				StopReason: anthropic.StopReasonEndTurn,
				Usage:      anthropic.Usage{InputTokens: 12, OutputTokens: 18, CacheReadInputTokens: 1},
			},
			respHeaders: map[string]string{statusHeaderName: "200"},
			expectedOpenAIResponse: openai.ChatCompletionResponse{
				ID:      "msg_01XYZ789",
				Model:   "claude-3-5-sonnet-20241022",
				Created: openai.JSONUNIXTime(time.Unix(releaseDateUnix, 0)),
				Object:  "chat.completion",
				Usage: openai.Usage{
					PromptTokens:     13,
					CompletionTokens: 18,
					TotalTokens:      31,
					PromptTokensDetails: &openai.PromptTokensDetails{
						CachedTokens: 1,
					},
					CompletionTokensDetails: &openai.CompletionTokensDetails{},
				},
				Choices: []openai.ChatCompletionResponseChoice{
					{
						Index: 0,
						Message: openai.ChatCompletionResponseChoiceMessage{
							Role: "assistant",
							ReasoningContent: &openai.ReasoningContentUnion{
								Value: &openai.ReasoningContent{
									ReasoningContent: &awsbedrock.ReasoningContentBlock{
										RedactedContent: []byte("redacted_data_content"),
									},
								},
							},
						},
						FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.inputResponse)
			require.NoError(t, err, "Test setup failed: could not marshal input struct")

			translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
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

// TestMessageTranslation adds specific coverage for assistant and tool message translations.
func TestMessageTranslation(t *testing.T) {
	tests := []struct {
		name                  string
		inputMessages         []openai.ChatCompletionMessageParamUnion
		expectedAnthropicMsgs []anthropic.MessageParam
		expectedSystemBlocks  []anthropic.TextBlockParam
		expectErr             bool
	}{
		{
			name: "assistant message with text",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{Value: "Hello from the assistant."},
						Role:    openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role:    anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("Hello from the assistant.")},
				},
			},
		},
		{
			name: "assistant message with tool call",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:       ptr.To(testTool),
								Type:     openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_weather", Arguments: `{"location":"NYC"}`},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role: anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{
						{
							OfToolUse: &anthropic.ToolUseBlockParam{
								ID:    testTool,
								Type:  "tool_use",
								Name:  "get_weather",
								Input: map[string]any{"location": "NYC"},
							},
						},
					},
				},
			},
		},
		{
			name: "assistant message with refusal",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: openai.ChatCompletionAssistantMessageParamContent{
								Type:    openai.ChatCompletionAssistantMessageParamContentTypeRefusal,
								Refusal: ptr.To("I cannot answer that."),
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role:    anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("I cannot answer that.")},
				},
			},
		},
		{
			name: "tool message with text content",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						ToolCallID: testTool,
						Content: openai.ContentUnion{
							Value: "The weather is 72 degrees and sunny.",
						},
						Role: openai.ChatMessageRoleTool,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role: anthropic.MessageParamRoleUser,
					Content: []anthropic.ContentBlockParamUnion{
						{
							OfToolResult: &anthropic.ToolResultBlockParam{
								ToolUseID: testTool,
								Type:      "tool_result",
								Content: []anthropic.ToolResultBlockParamContentUnion{
									{
										OfText: &anthropic.TextBlockParam{
											Text: "The weather is 72 degrees and sunny.",
											Type: "text",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "system and developer messages",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{OfSystem: &openai.ChatCompletionSystemMessageParam{Content: openai.ContentUnion{Value: "System prompt."}, Role: openai.ChatMessageRoleSystem}},
				{OfUser: &openai.ChatCompletionUserMessageParam{Content: openai.StringOrUserRoleContentUnion{Value: "User message."}, Role: openai.ChatMessageRoleUser}},
				{OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{Content: openai.ContentUnion{Value: "Developer prompt."}, Role: openai.ChatMessageRoleDeveloper}},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role:    anthropic.MessageParamRoleUser,
					Content: []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("User message.")},
				},
			},
			expectedSystemBlocks: []anthropic.TextBlockParam{
				{Text: "System prompt."},
				{Text: "Developer prompt."},
			},
		},
		{
			name: "user message with content error",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.StringOrUserRoleContentUnion{
							Value: 0,
						},
						Role: openai.ChatMessageRoleUser,
					},
				},
			},
			expectErr: true,
		},
		{
			name: "assistant message with tool call error",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:       ptr.To(testTool),
								Type:     openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_weather", Arguments: `{"location":`},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectErr: true,
		},
		{
			name: "tool message with content error",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						ToolCallID: testTool,
						Content:    openai.ContentUnion{Value: 123},
						Role:       openai.ChatMessageRoleTool,
					},
				},
			},
			expectErr: true,
		},
		{
			name: "tool message with text parts array",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						ToolCallID: "tool_def",
						Content: openai.ContentUnion{
							Value: []openai.ChatCompletionContentPartTextParam{
								{
									Type: "text",
									Text: "Tool result with image: [image data]",
								},
							},
						},
						Role: openai.ChatMessageRoleTool,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role: anthropic.MessageParamRoleUser,
					Content: []anthropic.ContentBlockParamUnion{
						{
							OfToolResult: &anthropic.ToolResultBlockParam{
								ToolUseID: "tool_def",
								Type:      "tool_result",
								Content: []anthropic.ToolResultBlockParamContentUnion{
									{
										OfText: &anthropic.TextBlockParam{
											Text: "Tool result with image: [image data]",
											Type: "text",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "multiple tool messages aggregated correctly",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						ToolCallID: "tool_1",
						Content:    openai.ContentUnion{Value: `{"temp": "72F"}`},
						Role:       openai.ChatMessageRoleTool,
					},
				},
				{
					OfTool: &openai.ChatCompletionToolMessageParam{
						ToolCallID: "tool_2",
						Content:    openai.ContentUnion{Value: `{"time": "16:00"}`},
						Role:       openai.ChatMessageRoleTool,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role: anthropic.MessageParamRoleUser,
					Content: []anthropic.ContentBlockParamUnion{
						{
							OfToolResult: &anthropic.ToolResultBlockParam{
								ToolUseID: "tool_1",
								Type:      "tool_result",
								Content: []anthropic.ToolResultBlockParamContentUnion{
									{OfText: &anthropic.TextBlockParam{Text: `{"temp": "72F"}`, Type: "text"}},
								},
								IsError: anthropic.Bool(false),
							},
						},
						{
							OfToolResult: &anthropic.ToolResultBlockParam{
								ToolUseID: "tool_2",
								Type:      "tool_result",
								Content: []anthropic.ToolResultBlockParamContentUnion{
									{OfText: &anthropic.TextBlockParam{Text: `{"time": "16:00"}`, Type: "text"}},
								},
								IsError: anthropic.Bool(false),
							},
						},
					},
				},
			},
		},
		{
			name: "assistant message with thinking content",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{
									Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
									Text:      ptr.To("Let me think about this step by step..."),
									Signature: ptr.To("signature-123"),
								},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role: anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{
						anthropic.NewThinkingBlock("signature-123", "Let me think about this step by step..."),
					},
				},
			},
		},
		{
			name: "assistant message with thinking content missing signature",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{
									Type: openai.ChatCompletionAssistantMessageParamContentTypeThinking,
									Text: ptr.To("Let me think about this step by step..."),
									// Missing signature - should not create thinking block
								},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role:    anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{},
				},
			},
		},
		{
			name: "assistant message with thinking content missing text",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{
									Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
									Signature: ptr.To("signature-123"),
									// Missing text - should not create thinking block
								},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role:    anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{},
				},
			},
		},
		{
			name: "assistant message with redacted thinking content (string)",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{
									Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
									RedactedContent: &openai.RedactedContentUnion{Value: "redacted content as string"},
								},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectedAnthropicMsgs: []anthropic.MessageParam{
				{
					Role: anthropic.MessageParamRoleAssistant,
					Content: []anthropic.ContentBlockParamUnion{
						anthropic.NewRedactedThinkingBlock("redacted content as string"),
					},
				},
			},
		},
		{
			name: "assistant message with redacted thinking content ([]byte) - should fail",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{
									Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
									RedactedContent: &openai.RedactedContentUnion{Value: []byte("redacted content as bytes")},
								},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectErr: true,
		},
		{
			name: "assistant message with redacted thinking content (unsupported type) - should fail",
			inputMessages: []openai.ChatCompletionMessageParamUnion{
				{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.StringOrAssistantRoleContentUnion{
							Value: []openai.ChatCompletionAssistantMessageParamContent{
								{
									Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
									RedactedContent: &openai.RedactedContentUnion{Value: 123},
								},
							},
						},
						Role: openai.ChatMessageRoleAssistant,
					},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openAIReq := &openai.ChatCompletionRequest{Messages: tt.inputMessages}
			anthropicMsgs, systemBlocks, err := openAIToAnthropicMessages(openAIReq.Messages)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				// Compare the conversational messages.
				require.Len(t, anthropicMsgs, len(tt.expectedAnthropicMsgs), "Number of translated messages should match")
				for i, expectedMsg := range tt.expectedAnthropicMsgs {
					actualMsg := anthropicMsgs[i]
					require.Equal(t, expectedMsg.Role, actualMsg.Role, "Message roles should match")
					require.Len(t, actualMsg.Content, len(expectedMsg.Content), "Number of content blocks should match")
					for j, expectedContent := range expectedMsg.Content {
						actualContent := actualMsg.Content[j]
						require.Equal(t, *expectedContent.GetType(), *actualContent.GetType(), "Content block types should match")
						if expectedContent.OfText != nil {
							require.NotNil(t, actualContent.OfText)
							require.Equal(t, expectedContent.OfText.Text, actualContent.OfText.Text)
						}
						if expectedContent.OfToolUse != nil {
							require.NotNil(t, actualContent.OfToolUse)
							require.Equal(t, expectedContent.OfToolUse.ID, actualContent.OfToolUse.ID)
							require.Equal(t, expectedContent.OfToolUse.Name, actualContent.OfToolUse.Name)
							require.Equal(t, expectedContent.OfToolUse.Input, actualContent.OfToolUse.Input)
						}
						if expectedContent.OfToolResult != nil {
							require.NotNil(t, actualContent.OfToolResult)
							require.Equal(t, expectedContent.OfToolResult.ToolUseID, actualContent.OfToolResult.ToolUseID)
							require.Len(t, actualContent.OfToolResult.Content, len(expectedContent.OfToolResult.Content))
							if expectedContent.OfToolResult.Content[0].OfText != nil {
								require.Equal(t, expectedContent.OfToolResult.Content[0].OfText.Text, actualContent.OfToolResult.Content[0].OfText.Text)
							}
							if expectedContent.OfToolResult.Content[0].OfImage != nil {
								require.NotNil(t, actualContent.OfToolResult.Content[0].OfImage, "Actual image block should not be nil")
								require.NotNil(t, actualContent.OfToolResult.Content[0].OfImage.Source, "Actual image source should not be nil")
								if expectedContent.OfToolResult.Content[0].OfImage.Source.OfBase64 != nil {
									require.NotNil(t, actualContent.OfToolResult.Content[0].OfImage.Source.OfBase64, "Actual base64 source should not be nil")
									require.Equal(t, expectedContent.OfToolResult.Content[0].OfImage.Source.OfBase64.Data, actualContent.OfToolResult.Content[0].OfImage.Source.OfBase64.Data)
								}
							}
						}
					}
				}

				// Compare the system prompt blocks.
				require.Len(t, systemBlocks, len(tt.expectedSystemBlocks), "Number of system blocks should match")
				for i, expectedBlock := range tt.expectedSystemBlocks {
					actualBlock := systemBlocks[i]
					require.Equal(t, expectedBlock.Text, actualBlock.Text, "System block text should match")
				}
			}
		})
	}
}

// TestRedactedContentUnionSerialization tests the JSON marshaling/unmarshaling of RedactedContentUnion
func TestRedactedContentUnionSerialization(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedValue any
		expectError   bool
	}{
		{
			name:          "string value",
			input:         `"plain string"`,
			expectedValue: "plain string",
		},
		{
			name:          "base64 encoded bytes",
			input:         `"aGVsbG8gd29ybGQ="`, // "hello world" in base64
			expectedValue: []byte("hello world"),
		},
		{
			name:        "invalid json",
			input:       `{invalid}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var union openai.RedactedContentUnion
			err := json.Unmarshal([]byte(tt.input), &union)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedValue, union.Value)

			// Test marshaling back
			marshaled, err := json.Marshal(union)
			require.NoError(t, err)

			// For byte arrays, check they're base64 encoded
			if bytes, ok := tt.expectedValue.([]byte); ok {
				expected := base64.StdEncoding.EncodeToString(bytes)
				require.Equal(t, `"`+expected+`"`, string(marshaled))
			} else {
				// For strings, check round-trip
				require.Equal(t, tt.input, string(marshaled))
			}
		})
	}
}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_ResponseError(t *testing.T) {
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
					Type:    gcpBackendError,
					Code:    ptr.To("503"),
					Message: "Service Unavailable",
				},
			},
		},
		{
			name: "json error response",
			responseHeaders: map[string]string{
				statusHeaderName:      "400",
				contentTypeHeaderName: "application/json",
			},
			inputBody: &anthropic.ErrorResponse{
				Type: "error",
				Error: shared.ErrorObjectUnion{
					Type:    "invalid_request_error",
					Message: "Your max_tokens is too high.",
				},
			},
			expectedOutput: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    "invalid_request_error",
					Code:    ptr.To("400"),
					Message: "Your max_tokens is too high.",
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

			o := &openAIToGCPAnthropicTranslatorV1ChatCompletion{}
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

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_Cache(t *testing.T) {
	t.Run("full request with mixed caching", func(t *testing.T) {
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Messages: []openai.ChatCompletionMessageParamUnion{
				// System message with cache enabled.
				{OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role: openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: "text",
							Text: "You are a helpful assistant.",
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					}},
				}},
				// User message with cache enabled.
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfText: &openai.ChatCompletionContentPartTextParam{
							Type: "text",
							Text: "How's the weather?",
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						}},
					}},
				}},
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "I'll check the weather for you."},
					ToolCalls: []openai.ChatCompletionMessageToolCallParam{
						{
							ID: ptr.To("call_789"),
							Function: openai.ChatCompletionMessageToolCallFunctionParam{
								Name:      "get_weather",
								Arguments: `{"location": "New York"}`,
							},
							Type: openai.ChatCompletionMessageToolCallTypeFunction,
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					},
				}},
				// Tool message with cache enabled.
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_789",
					Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: "text",
							Text: "It's sunny and 75°F in New York.",
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					}},
				}},
				// User message with cache disabled.
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "Thanks! What about tomorrow?"},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// Check system message (cache enabled).
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("system.0.cache_control.type").String())

		// Check user message (cache enabled).
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.0.cache_control.type").String())

		// Check assistant message (text part is not cached, tool_use part IS cached)
		require.False(t, result.Get("messages.1.content.0.cache_control").Exists(), "text part of assistant message should not be cached")
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.1.content.1.cache_control.type").String(), "tool_use block should be cached")

		// Check tool message (aggregated into a user message, cache enabled)
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.2.content.0.cache_control.type").String())

		// Check second user message (cache disabled)
		require.False(t, result.Get("messages.3.content.0.cache_control").Exists())
	})

	t.Run("cache with different structures", func(t *testing.T) {
		type testCase struct {
			name        string
			content     any
			expectCache bool
		}

		testCases := []testCase{
			{
				name: "multi-part text cache enabled",
				content: []openai.ChatCompletionContentPartUserUnionParam{
					{OfText: &openai.ChatCompletionContentPartTextParam{
						Type: "text", Text: "This is a content part",
						AnthropicContentFields: &openai.AnthropicContentFields{CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()}},
					}},
				},
				expectCache: true,
			},
			{
				name: "multi-part text cache disabled (empty type)",
				content: []openai.ChatCompletionContentPartUserUnionParam{
					{OfText: &openai.ChatCompletionContentPartTextParam{
						Type: "text", Text: "This is a content part",
						AnthropicContentFields: &openai.AnthropicContentFields{CacheControl: anthropic.CacheControlEphemeralParam{Type: ""}},
					}},
				},
				expectCache: false,
			},
			{
				name: "multi-part text cache disabled (anthropic fields empty)",
				content: []openai.ChatCompletionContentPartUserUnionParam{
					{OfText: &openai.ChatCompletionContentPartTextParam{
						Type: "text", Text: "This is a content part",
						AnthropicContentFields: &openai.AnthropicContentFields{},
					}},
				},
				expectCache: false,
			},
			{
				name: "multi-part text cache disabled (missing anthropic fields)",
				content: []openai.ChatCompletionContentPartUserUnionParam{
					{OfText: &openai.ChatCompletionContentPartTextParam{
						Type: "text", Text: "This is a content part",
					}},
				},
				expectCache: false,
			},
			{
				name: "multi-part text cache missing",
				content: []openai.ChatCompletionContentPartUserUnionParam{
					{OfText: &openai.ChatCompletionContentPartTextParam{Type: "text", Text: "This is a content part"}},
				},
				expectCache: false,
			},
			{
				name:        "simple string content (caching not possible)",
				content:     "This is a test message",
				expectCache: false,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := &openai.ChatCompletionRequest{
					Model: "claude-3-haiku",
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: tc.content},
						}},
					},
					MaxTokens: ptr.To(int64(10)),
				}

				translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
				_, body, err := translator.RequestBody(nil, req, false)
				require.NoError(t, err)

				result := gjson.ParseBytes(body)
				cacheControl := result.Get("messages.0.content.0.cache_control")

				if tc.expectCache {
					require.True(t, cacheControl.Exists())
					require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), cacheControl.Get("type").String())
				} else {
					require.False(t, cacheControl.Exists())
				}
			})
		}
	})
	t.Run("cache with image content", func(t *testing.T) {
		req := &openai.ChatCompletionRequest{
			Model: "claude-3-opus",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{OfText: &openai.ChatCompletionContentPartTextParam{
								Text: "What's in this image?", Type: "text",
								AnthropicContentFields: &openai.AnthropicContentFields{
									CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
								},
							}},
							{OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: "image_url",
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "data:image/jpeg;base64,dGVzdA==",
								},
								AnthropicContentFields: &openai.AnthropicContentFields{
									CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
								},
							}},
						},
					},
				}},
			},
			MaxTokens: ptr.To(int64(50)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// Check that both the text part and the image part have cache_control.
		require.True(t, result.Get("messages.0.content.0.cache_control").Exists(), "cache should exist for text part")
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.0.cache_control.type").String())

		require.True(t, result.Get("messages.0.content.1.cache_control").Exists(), "cache should exist for image part")
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.1.cache_control.type").String())
	})
	t.Run("cache with mixed multi-modal content", func(t *testing.T) {
		// This test ensures that in a multi-part (text/image) message, one part
		// can be cached while the other is not.
		req := &openai.ChatCompletionRequest{
			Model: "claude-3-opus",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							// Text part: Caching NOT enabled
							{OfText: &openai.ChatCompletionContentPartTextParam{
								Text: "What's in this image?", Type: "text",
							}},
							// Image part: Caching IS enabled
							{OfImageURL: &openai.ChatCompletionContentPartImageParam{
								Type: "image_url",
								ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
									URL: "data:image/jpeg;base64,dGVzdA==",
								},
								AnthropicContentFields: &openai.AnthropicContentFields{
									CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
								},
							}},
						},
					},
				}},
			},
			MaxTokens: ptr.To(int64(50)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, req, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// Check text part (index 0) - should NOT be cached.
		require.False(t, result.Get("messages.0.content.0.cache_control").Exists(), "text part should not be cached")

		// Check image part (index 1) - SHOULD be cached.
		require.True(t, result.Get("messages.0.content.1.cache_control").Exists(), "image part should be cached")
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.1.cache_control.type").String())
	})
	t.Run("developer message caching", func(t *testing.T) {
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Messages: []openai.ChatCompletionMessageParamUnion{
				// Developer message with cache enabled.
				{OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
					Role: openai.ChatMessageRoleDeveloper,
					Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: "text",
							Text: "You are an expert Go programmer.",
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					}},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// Check that the developer message, which becomes part of the 'system' prompt, is cached.
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("system.0.cache_control.type").String())
	})
	t.Run("tool definition caching", func(t *testing.T) {
		// This test verifies that a cache_control field on a
		// FunctionDefinition (in the 'tools' array) is correctly translated.
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Tools: []openai.Tool{
				{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name: "get_weather",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{"type": "string"},
							},
						},
						AnthropicContentFields: &openai.AnthropicContentFields{
							CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
						},
					},
				},
			},
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather in New York?"},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// Check that the tool definition in the 'tools' array is cached.
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("tools.0.cache_control.type").String(), "tool definition should be cached")
		require.Equal(t, "get_weather", result.Get("tools.0.name").String())
	})
	t.Run("tool definition eager_input_streaming", func(t *testing.T) {
		// The OpenAI path builds the Anthropic request from typed structs, so a field absent
		// from FunctionDefinition cannot reach the backend at all. Assert on the serialized
		// body rather than the struct, including the explicit false, which Anthropic reads as
		// "keep buffering" rather than "unset".
		for _, tc := range []struct {
			name     string
			eager    *bool
			expected string
		}{
			{name: "true is forwarded", eager: ptr.To(true), expected: "true"},
			{name: "false is forwarded, not dropped", eager: ptr.To(false), expected: "false"},
			{name: "omitted stays absent", eager: nil, expected: ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				openAIReq := &openai.ChatCompletionRequest{
					Model: "gcp.claude-3.5-haiku",
					Tools: []openai.Tool{
						{
							Type: openai.ToolTypeFunction,
							Function: &openai.FunctionDefinition{
								Name:                "get_weather",
								EagerInputStreaming: tc.eager,
							},
						},
					},
					Messages: []openai.ChatCompletionMessageParamUnion{
						{OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather in New York?"},
						}},
					},
					MaxTokens: ptr.To(int64(100)),
				}

				translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
				_, body, err := translator.RequestBody(nil, openAIReq, false)
				require.NoError(t, err)

				require.Equal(t, tc.expected, gjson.ParseBytes(body).Get("tools.0.eager_input_streaming").Raw)
			})
		}
	})
	t.Run("aggregated tool messages with mixed caching", func(t *testing.T) {
		// This test ensures that caching is applied on a per-tool-message basis,
		// even when they are aggregated into a single user message.
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Messages: []openai.ChatCompletionMessageParamUnion{
				// First tool message, cache disabled.
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					Content:    openai.ContentUnion{Value: "Result for tool 1"},
					ToolCallID: "call_001",
				}},
				// Second tool message, cache not  constant.ValueOf[constant.Ephemeral]() (i.e., disabled).
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					Content:    openai.ContentUnion{Value: "Result for tool 2"},
					ToolCallID: "call_002",
				}},
				// Third tool message, cache enabled.
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_003",
					Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: "text",
							Text: "Result for tool 3",
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					}},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// The translator creates a single user message with three tool_result blocks.
		// The first & second block should NOT have cache_control.
		require.False(t, result.Get("messages.0.content.0.cache_control").Exists(), "first tool_result should not be cached")
		require.False(t, result.Get("messages.0.content.1.cache_control").Exists(), "second tool_result should not be cached")

		// The third block SHOULD have cache_control.
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.2.cache_control.type").String(), "third tool_result should be cached")
	})
	t.Run("assistant tool_call caching", func(t *testing.T) {
		// This test verifies that a cache_control field on a
		// ToolCall (in an assistant message) is correctly translated
		// to the corresponding tool_use block.
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "OK, I'll use the tool."},
					ToolCalls: []openai.ChatCompletionMessageToolCallParam{
						{
							ID:   ptr.To("call_789"),
							Type: openai.ChatCompletionMessageToolCallTypeFunction,
							Function: openai.ChatCompletionMessageToolCallFunctionParam{
								Name:      "get_weather",
								Arguments: `{"location": "New York"}`,
							},
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// The assistant message has two content parts: text and tool_use.
		// The text part should not be cached.
		require.False(t, result.Get("messages.0.content.0.cache_control").Exists(), "text part of assistant message should not be cached")

		// The tool_use part (index 1) should be cached.
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.1.cache_control.type").String(), "tool_use block should be cached")
		require.Equal(t, "tool_use", result.Get("messages.0.content.1.type").String())
		require.Equal(t, "call_789", result.Get("messages.0.content.1.id").String())
	})
	t.Run("assistant text content caching", func(t *testing.T) {
		// This test verifies that a cache_control field on an
		// assistant's text content part is correctly translated.
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role: openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{
						Value: openai.ChatCompletionAssistantMessageParamContent{
							Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
							Text: ptr.To("This is a cached assistant text response."),
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		// Check the assistant message's text content (index 0).
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.0.cache_control.type").String(), "assistant text block should be cached")
		require.Equal(t, "text", result.Get("messages.0.content.0.type").String())
		require.Equal(t, "This is a cached assistant text response.", result.Get("messages.0.content.0.text").String())
	})
	t.Run("aggregated tool messages with granular caching", func(t *testing.T) {
		// This test validates the logic in the 'case msg.OfTool != nil:' block.
		// It checks that caching is applied on a per-tool-message basis,
		// and that it correctly reads the cache flag from within the content parts.
		openAIReq := &openai.ChatCompletionRequest{
			Model: "gcp.claude-3.5-haiku",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					Content:    openai.ContentUnion{Value: "Result for tool 1"},
					ToolCallID: "call_001",
				}},
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_002",
					Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: "text",
							Text: "Result for tool 2 (no cache)",
						},
					}},
				}},
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_003",
					Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
						{
							Type: "text",
							Text: "Part 1 of result 3 (not cached)",
						},
						{
							Type: "text",
							Text: "Part 2 of result 3 (cached)",
							AnthropicContentFields: &openai.AnthropicContentFields{
								CacheControl: anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()},
							},
						},
					}},
				}},
			},
			MaxTokens: ptr.To(int64(100)),
		}

		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "")
		_, body, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		result := gjson.ParseBytes(body)

		require.Equal(t, "call_001", result.Get("messages.0.content.0.tool_use_id").String())
		require.False(t, result.Get("messages.0.content.0.cache_control").Exists(), "tool 1 (string) should not be cached")

		require.Equal(t, "call_002", result.Get("messages.0.content.1.tool_use_id").String())
		require.False(t, result.Get("messages.0.content.1.cache_control").Exists(), "tool 2 (no cache) should not be cached")

		require.Equal(t, "call_003", result.Get("messages.0.content.2.tool_use_id").String())
		require.Equal(t, string(constant.ValueOf[constant.Ephemeral]()), result.Get("messages.0.content.2.cache_control.type").String(), "tool 3 (with cache) should be cached")
	})
}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_SetRedactionConfig(t *testing.T) {
	translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "").(*openAIToGCPAnthropicTranslatorV1ChatCompletion)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	translator.SetRedactionConfig(true, true, logger)

	require.True(t, translator.debugLogEnabled)
	require.True(t, translator.enableRedaction)
	require.NotNil(t, translator.logger)
}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_RedactBody(t *testing.T) {
	translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "").(*openAIToGCPAnthropicTranslatorV1ChatCompletion)

	t.Run("nil response returns nil", func(t *testing.T) {
		result := translator.RedactBody(nil)
		require.Nil(t, result)
	})

	t.Run("redacts message content", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			ID:    "test-id",
			Model: "test-model",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index:   0,
					Message: openai.ChatCompletionResponseChoiceMessage{Role: "assistant", Content: ptr.To("sensitive content")},
				},
			},
		}

		result := translator.RedactBody(resp)
		require.NotNil(t, result)
		require.Equal(t, "test-id", result.ID)
		require.Len(t, result.Choices, 1)
		// Content should be redacted (not the original value)
		require.NotNil(t, result.Choices[0].Message.Content)
		require.NotEqual(t, "sensitive content", *result.Choices[0].Message.Content)
	})

	t.Run("redacts tool calls", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			ID:    "test-id",
			Model: "test-model",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: "assistant",
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("tool-1"),
								Type: openai.ChatCompletionMessageToolCallTypeFunction,
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "get_secret",
									Arguments: `{"password": "secret123"}`,
								},
							},
						},
					},
				},
			},
		}

		result := translator.RedactBody(resp)
		require.NotNil(t, result)
		require.Len(t, result.Choices, 1)
		require.Len(t, result.Choices[0].Message.ToolCalls, 1)
		// Tool call name is kept (API name, not user data); arguments are redacted
		require.Equal(t, "get_secret", result.Choices[0].Message.ToolCalls[0].Function.Name)
		require.NotEqual(t, `{"password": "secret123"}`, result.Choices[0].Message.ToolCalls[0].Function.Arguments)
	})

	t.Run("redacts audio data", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			ID:    "test-id",
			Model: "test-model",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: "assistant",
						Audio: &openai.ChatCompletionResponseChoiceMessageAudio{
							Data:       "base64-audio-data",
							Transcript: "sensitive transcript",
						},
					},
				},
			},
		}

		result := translator.RedactBody(resp)
		require.NotNil(t, result)
		require.Len(t, result.Choices, 1)
		require.NotNil(t, result.Choices[0].Message.Audio)
		// Audio data and transcript should be redacted
		require.NotEqual(t, "base64-audio-data", result.Choices[0].Message.Audio.Data)
		require.NotEqual(t, "sensitive transcript", result.Choices[0].Message.Audio.Transcript)
	})

	t.Run("redacts reasoning content", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			ID:    "test-id",
			Model: "test-model",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: "assistant",
						ReasoningContent: &openai.ReasoningContentUnion{
							Value: &openai.ReasoningContent{
								ReasoningContent: &awsbedrock.ReasoningContentBlock{
									ReasoningText: &awsbedrock.ReasoningTextBlock{
										Text:      "sensitive reasoning",
										Signature: "sig123",
									},
								},
							},
						},
					},
				},
			},
		}

		result := translator.RedactBody(resp)
		require.NotNil(t, result)
		require.Len(t, result.Choices, 1)
		require.NotNil(t, result.Choices[0].Message.ReasoningContent)
	})

	t.Run("empty choices returns empty choices", func(t *testing.T) {
		resp := &openai.ChatCompletionResponse{
			ID:      "test-id",
			Model:   "test-model",
			Choices: []openai.ChatCompletionResponseChoice{},
		}

		result := translator.RedactBody(resp)
		require.NotNil(t, result)
		require.Empty(t, result.Choices)
	})
}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_ResponseHeaders(t *testing.T) {
	t.Run("returns event-stream content type for streaming", func(t *testing.T) {
		openAIReq := &openai.ChatCompletionRequest{
			Stream:    true,
			Model:     "test-model",
			MaxTokens: ptr.To(int64(100)),
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "").(*openAIToGCPAnthropicTranslatorV1ChatCompletion)

		// Initialize the stream parser by calling RequestBody with streaming request
		_, _, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		// Now ResponseHeaders should return the streaming content type
		headers, err := translator.ResponseHeaders(nil)
		require.NoError(t, err)
		require.Len(t, headers, 1)
		require.Equal(t, contentTypeHeaderName, headers[0].Key())
		require.Equal(t, eventStreamContentType, headers[0].Value())
	})

	t.Run("returns no headers for non-streaming", func(t *testing.T) {
		openAIReq := &openai.ChatCompletionRequest{
			Stream:    false,
			Model:     "test-model",
			MaxTokens: ptr.To(int64(100)),
		}
		translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "").(*openAIToGCPAnthropicTranslatorV1ChatCompletion)

		// Initialize without streaming
		_, _, err := translator.RequestBody(nil, openAIReq, false)
		require.NoError(t, err)

		// ResponseHeaders should return nil for non-streaming
		headers, err := translator.ResponseHeaders(nil)
		require.NoError(t, err)
		require.Nil(t, headers)
	})
}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_ResponseBody_WithDebugLogging(t *testing.T) {
	// Create a buffer to capture log output
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "").(*openAIToGCPAnthropicTranslatorV1ChatCompletion)
	translator.SetRedactionConfig(true, true, logger)

	// Initialize translator with the model
	req := &openai.ChatCompletionRequest{
		Model:     "claude-3",
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

	// Create a response
	anthropicResponse := anthropic.Message{
		ID:   "msg_01XYZ",
		Type: constant.ValueOf[constant.Message](),
		Role: constant.ValueOf[constant.Assistant](),
		Content: []anthropic.ContentBlockUnion{
			{
				Type: "text",
				Text: "Hello! How can I help you?",
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

	_, _, _, _, err = translator.ResponseBody(nil, bytes.NewReader(body), true, nil)
	require.NoError(t, err)

	// Verify that debug logging occurred
	logOutput := logBuf.String()
	require.Contains(t, logOutput, "response body processing")
}

// mockSpan implements tracingapi.ChatCompletionSpan for testing
type mockSpan struct {
	recordedResponse *openai.ChatCompletionResponse
}

func (m *mockSpan) RecordResponseChunk(_ *openai.ChatCompletionResponseChunk) {}
func (m *mockSpan) RecordResponse(resp *openai.ChatCompletionResponse) {
	m.recordedResponse = resp
}
func (m *mockSpan) EndSpanOnError(_ int, _ []byte) {}
func (m *mockSpan) EndSpan()                       {}

func TestOpenAIToGCPAnthropicTranslatorV1ChatCompletion_ResponseBody_WithSpanRecording(t *testing.T) {
	translator := NewChatCompletionOpenAIToGCPAnthropicTranslator("", "").(*openAIToGCPAnthropicTranslatorV1ChatCompletion)

	// Initialize translator with the model
	req := &openai.ChatCompletionRequest{
		Model:     "claude-3",
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

	// Create a response
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

	// Create a mock span
	span := &mockSpan{}

	_, _, _, _, err = translator.ResponseBody(nil, bytes.NewReader(body), true, span)
	require.NoError(t, err)

	// Verify the span recorded the response
	require.NotNil(t, span.recordedResponse)
	require.Equal(t, "msg_01XYZ", span.recordedResponse.ID)
	require.Len(t, span.recordedResponse.Choices, 1)
	require.Equal(t, "Hello!", *span.recordedResponse.Choices[0].Message.Content)
}
