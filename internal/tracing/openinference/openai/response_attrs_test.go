// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

var (
	basicResp = &openai.ChatCompletionResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: openai.JSONUNIXTime(time.Unix(1234567890, 0)),
		Model:   openai.ModelGPT5Nano,
		Choices: []openai.ChatCompletionResponseChoice{{
			Index: 0,
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:    "assistant",
				Content: ptr("Hello! How can I help you today?"),
			},
			FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
		}},
		Usage: openai.Usage{
			PromptTokens:     20,
			CompletionTokens: 10,
			TotalTokens:      30,
		},
	}
	basicRespBody = mustJSON(basicResp)

	toolsResp = &openai.ChatCompletionResponse{
		ID:    "chatcmpl-123",
		Model: openai.ModelGPT5Nano,
		Choices: []openai.ChatCompletionResponseChoice{{
			Index: 0,
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:    "assistant",
				Content: ptr("I can help you with that."),
				ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
					ID:   ptr("call_123"),
					Type: openai.ChatCompletionMessageToolCallType("function"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      "get_weather",
						Arguments: `{"location":"NYC"}`,
					},
				}},
			},
			FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
		}},
		Usage: openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	detailedResp = &openai.ChatCompletionResponse{
		ID:                "chatcmpl-Bx5kNovDsMvLVkXYomgZvfV95lhEd",
		Object:            "chat.completion",
		Created:           openai.JSONUNIXTime(time.Unix(1753423143, 0)),
		Model:             "gpt-4.1-nano-2025-04-14",
		ServiceTier:       "default",
		SystemFingerprint: "fp_38343a2f8f",
		Choices: []openai.ChatCompletionResponseChoice{{
			Index: 0,
			Message: openai.ChatCompletionResponseChoiceMessage{
				Role:    "assistant",
				Content: ptr("Hello! How can I assist you today?"),
			},
			FinishReason: openai.ChatCompletionChoicesFinishReasonStop,
		}},
		Usage: openai.Usage{
			PromptTokens:     9,
			CompletionTokens: 9,
			TotalTokens:      18,
			PromptTokensDetails: &openai.PromptTokensDetails{
				AudioTokens:  0,
				CachedTokens: 0,
			},
			CompletionTokensDetails: &openai.CompletionTokensDetails{
				AcceptedPredictionTokens: 0,
				AudioTokens:              0,
				ReasoningTokens:          0,
				RejectedPredictionTokens: 0,
			},
		},
	}
)

var (
	embeddingsResp = &openai.EmbeddingResponse{
		Object: "list",
		Data: []openai.Embedding{
			{
				Object: "embedding",
				Embedding: openai.EmbeddingUnion{
					Value: []float64{0.1, -0.2, 0.3},
				},
				Index: 0,
			},
		},
		Model: "text-embedding-3-small",
		Usage: openai.EmbeddingUsage{
			PromptTokens: 5,
			TotalTokens:  5,
		},
	}

	embeddingsBase64Resp = &openai.EmbeddingResponse{
		Object: "list",
		Data: []openai.Embedding{
			{
				Object: "embedding",
				// Base64 encoding of two float32 values: 0.5 and -0.5 in little-endian
				// 0.5 in float32 = 0x3f000000, -0.5 in float32 = 0xbf000000
				Embedding: openai.EmbeddingUnion{
					Value: "AAAAPwAAAL8=", // base64 of [0x00, 0x00, 0x00, 0x3f, 0x00, 0x00, 0x00, 0xbf]
				},
				Index: 0,
			},
		},
		Model: "text-embedding-3-small",
		Usage: openai.EmbeddingUsage{
			PromptTokens: 3,
			TotalTokens:  3,
		},
	}
)

func TestBuildResponseAttributes(t *testing.T) {
	tests := []struct {
		name          string
		resp          *openai.ChatCompletionResponse
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "successful response",
			resp: basicResp,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageContent), "Hello! How can I help you today?"),
				attribute.Int(openinference.LLMTokenCountPrompt, 20),
				attribute.Int(openinference.LLMTokenCountCompletion, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "response with tool calls",
			resp: toolsResp,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageContent), "I can help you with that."),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "call_123"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "get_weather"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"location":"NYC"}`),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountCompletion, 20),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "response with detailed usage",
			resp: detailedResp,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-4.1-nano-2025-04-14"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageContent), "Hello! How can I assist you today?"),
				attribute.Int(openinference.LLMTokenCountPrompt, 9),
				attribute.Int(openinference.LLMTokenCountPromptAudio, 0),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, 0),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 0),
				attribute.Int(openinference.LLMTokenCountCompletion, 9),
				attribute.Int(openinference.LLMTokenCountCompletionAudio, 0),
				attribute.Int(openinference.LLMTokenCountCompletionReasoning, 0),
				attribute.Int(openinference.LLMTokenCountTotal, 18),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := buildResponseAttributes(tt.resp, openinference.NewTraceConfig())

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestBuildEmbeddingsResponseAttributes(t *testing.T) {
	tests := []struct {
		name          string
		resp          *openai.EmbeddingResponse
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:   "successful embeddings response with float vectors",
			resp:   embeddingsResp,
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(0), []float64{0.1, -0.2, 0.3}),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountTotal, 5),
			},
		},
		{
			name:   "embeddings response with base64 vectors",
			resp:   embeddingsBase64Resp,
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Float64Slice(openinference.EmbeddingVectorAttribute(0), []float64{0.5, -0.5}),
				attribute.Int(openinference.LLMTokenCountPrompt, 3),
				attribute.Int(openinference.LLMTokenCountTotal, 3),
			},
		},
		{
			name: "hide embeddings vectors",
			resp: embeddingsResp,
			config: &openinference.TraceConfig{
				HideEmbeddingsVectors: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountTotal, 5),
			},
		},
		{
			name: "hide outputs",
			resp: embeddingsResp,
			config: &openinference.TraceConfig{
				HideOutputs: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.EmbeddingModelName, "text-embedding-3-small"),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountTotal, 5),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := buildEmbeddingsResponseAttributes(tt.resp, tt.config)

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestDecodeBase64Embeddings(t *testing.T) {
	tests := []struct {
		name           string
		encoded        string
		expectedFloats []float64
		expectError    bool
	}{
		{
			name: "decode two float32 values",
			// 0.5 in float32 = 0x3f000000, -0.5 in float32 = 0xbf000000 (little-endian)
			encoded:        "AAAAPwAAAL8=",
			expectedFloats: []float64{0.5, -0.5},
			expectError:    false,
		},
		{
			name: "decode single float32 value",
			// 1.0 in float32 = 0x3f800000 (little-endian)
			encoded:        "AACAPw==",
			expectedFloats: []float64{1.0},
			expectError:    false,
		},
		{
			name:           "invalid base64",
			encoded:        "not-valid-base64!@#",
			expectedFloats: nil,
			expectError:    true,
		},
		{
			name:           "empty string",
			encoded:        "",
			expectedFloats: []float64{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			floats, err := decodeBase64Embeddings(tt.encoded)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(floats) != len(tt.expectedFloats) {
				t.Errorf("expected %d floats, got %d", len(tt.expectedFloats), len(floats))
				return
			}

			for i, expected := range tt.expectedFloats {
				if floats[i] != expected {
					t.Errorf("float[%d]: expected %f, got %f", i, expected, floats[i])
				}
			}
		})
	}
}

func TestBuildCompletionResponseAttributes(t *testing.T) {
	basicCompletionResp := &openai.CompletionResponse{
		Model: "gpt-3.5-turbo-instruct",
		Choices: []openai.CompletionChoice{
			{
				Text:  "This is a test",
				Index: ptr(0),
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     5,
			CompletionTokens: 4,
			TotalTokens:      9,
		},
	}

	multiChoiceResp := &openai.CompletionResponse{
		Model: "gpt-3.5-turbo-instruct",
		Choices: []openai.CompletionChoice{
			{
				Text:  "First choice",
				Index: ptr(0),
			},
			{
				Text:  "Second choice",
				Index: ptr(1),
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     5,
			CompletionTokens: 6,
			TotalTokens:      11,
		},
	}

	tests := []struct {
		name          string
		resp          *openai.CompletionResponse
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic response",
			resp: basicCompletionResp,
			config: &openinference.TraceConfig{
				HideOutputs: false,
				HideChoices: false,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.ChoiceTextAttribute(0), "This is a test"),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
			},
		},
		{
			name: "multiple choices",
			resp: multiChoiceResp,
			config: &openinference.TraceConfig{
				HideOutputs: false,
				HideChoices: false,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.ChoiceTextAttribute(0), "First choice"),
				attribute.String(openinference.ChoiceTextAttribute(1), "Second choice"),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 6),
				attribute.Int(openinference.LLMTokenCountTotal, 11),
			},
		},
		{
			name: "hide outputs",
			resp: basicCompletionResp,
			config: &openinference.TraceConfig{
				HideOutputs: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				// No OutputMimeType when HideOutputs is true
				// No choices when HideOutputs is true
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
			},
		},
		{
			name: "hide choices",
			resp: basicCompletionResp,
			config: &openinference.TraceConfig{
				HideOutputs: false,
				HideChoices: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				// No choice text attributes when HideChoices is true
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 4),
				attribute.Int(openinference.LLMTokenCountTotal, 9),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := buildCompletionResponseAttributes(tt.resp, tt.config)

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseOutputMsgAttrs(t *testing.T) {
	tests := []struct {
		name          string
		msg           *openai.ResponseOutputMessage
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "output message with text content",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-123",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Type: "output_text",
								Text: "Hello! This is a test response.",
							},
						},
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "Hello! This is a test response."),
			},
		},
		{
			name: "output message with string content",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-123",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfString: ptr("Hello! This is a test response."),
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "Hello! This is a test response."),
			},
		},
		{
			name: "output message with string content redacted",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-123",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfString: ptr("Hello! This is a test response."),
				},
			},
			config:       &openinference.TraceConfig{HideOutputText: true},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "output message with refusal",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-124",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Type:    "refusal",
								Refusal: "I cannot provide that information.",
							},
						},
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "I cannot provide that information."),
			},
		},
		{
			name: "output message with refusal hideoutput text",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-124",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Type:    "refusal",
								Refusal: "I cannot provide that information.",
							},
						},
					},
				},
			},
			config:       &openinference.TraceConfig{HideOutputText: true},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "output message with hidden text",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-125",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Type: "output_text",
								Text: "Sensitive information",
							},
						},
					},
				},
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "output message with multiple content items",
			msg: &openai.ResponseOutputMessage{
				ID:   "msg-126",
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Type: "output_text",
								Text: "First part",
							},
						},
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Type: "output_text",
								Text: "Second part",
							},
						},
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(1, 0, "text"), "First part"),
				attribute.String(openinference.OutputMessageContentAttribute(1, 1, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(1, 1, "text"), "Second part"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseOutputMsgAttrs(tt.msg, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseFunctionCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		call          *openai.ResponseFunctionToolCall
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "function call with arguments",
			call: &openai.ResponseFunctionToolCall{
				ID:        "call-456",
				CallID:    "call_123",
				Name:      "get_weather",
				Arguments: `{"location":"NYC","unit":"fahrenheit"}`,
				Type:      "function_call",
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "call_123"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "get_weather"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"location":"NYC","unit":"fahrenheit"}`),
			},
		},
		{
			name: "function call with hidden output",
			call: &openai.ResponseFunctionToolCall{
				ID:        "call-457",
				CallID:    "call_456",
				Name:      "get_stock_price",
				Arguments: `{"symbol":"AAPL"}`,
				Type:      "function_call",
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "function call with empty arguments",
			call: &openai.ResponseFunctionToolCall{
				ID:        "call-458",
				CallID:    "call_789",
				Name:      "get_current_time",
				Arguments: `{}`,
				Type:      "function_call",
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 2,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(2, 0, openinference.ToolCallID), "call_789"),
				attribute.String(openinference.OutputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "get_current_time"),
				attribute.String(openinference.OutputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionArguments), `{}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseFunctionCallAttrs(tt.call, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseFileSearchCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		call          *openai.ResponseFileSearchToolCall
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "file search call with queries",
			call: &openai.ResponseFileSearchToolCall{
				ID:      "fs-001",
				Type:    "file_search_call",
				Status:  "completed",
				Queries: []string{"project documentation", "API reference"},
				Results: []openai.ResponseFileSearchToolCallResultParam{
					{
						FileID:   "file-123",
						Filename: "README.md",
						Score:    ptr(0.95),
						Text:     "This is the project documentation...",
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "fs-001"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "file_search_call"),
			},
		},
		{
			name: "file search call with hidden output",
			call: &openai.ResponseFileSearchToolCall{
				ID:      "fs-002",
				Type:    "file_search_call",
				Status:  "completed",
				Queries: []string{"sensitive data"},
				Results: []openai.ResponseFileSearchToolCallResultParam{},
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "file search call at different message index",
			call: &openai.ResponseFileSearchToolCall{
				ID:      "fs-003",
				Type:    "file_search_call",
				Status:  "in_progress",
				Queries: []string{"search query"},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 3,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(3, 0, openinference.ToolCallID), "fs-003"),
				attribute.String(openinference.OutputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionName), "file_search_call"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseFileSearchCallAttrs(tt.call, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseComputerCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		call          *openai.ResponseComputerToolCall
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "computer call with click action",
			call: &openai.ResponseComputerToolCall{
				ID:     "computer-001",
				CallID: "comp_click_123",
				Type:   "computer_call",
				Status: "completed",
				Action: openai.ResponseComputerToolCallActionUnionParam{
					OfClick: &openai.ResponseComputerToolCallActionClickParam{
						Type:   "click",
						X:      100,
						Y:      200,
						Button: "left",
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "comp_click_123"),
			},
		},
		{
			name: "computer call with hidden output",
			call: &openai.ResponseComputerToolCall{
				ID:     "computer-002",
				CallID: "comp_type_456",
				Type:   "computer_call",
				Status: "completed",
				Action: openai.ResponseComputerToolCallActionUnionParam{
					OfType: &openai.ResponseComputerToolCallActionTypeParam{
						Type: "type",
						Text: "sensitive input",
					},
				},
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
			},
		},
		{
			name: "computer call with screenshot action",
			call: &openai.ResponseComputerToolCall{
				ID:     "computer-003",
				CallID: "comp_screenshot_789",
				Type:   "computer_call",
				Status: "completed",
				Action: openai.ResponseComputerToolCallActionUnionParam{
					OfScreenshot: &openai.ResponseComputerToolCallActionScreenshotParam{
						Type: "screenshot",
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallID), "comp_screenshot_789"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseComputerCallAttrs(tt.call, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseReasoningAttrs(t *testing.T) {
	tests := []struct {
		name          string
		reasoning     *openai.ResponseReasoningItem
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "reasoning item with single summary",
			reasoning: &openai.ResponseReasoningItem{
				ID:     "reason-001",
				Type:   "reasoning",
				Status: "completed",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "The model analyzed the problem step by step.",
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "The model analyzed the problem step by step."),
			},
		},
		{
			name: "reasoning item with multiple summaries",
			reasoning: &openai.ResponseReasoningItem{
				ID:     "reason-002",
				Type:   "reasoning",
				Status: "completed",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "First step of reasoning",
					},
					{
						Type: "summary_text",
						Text: "Second step of reasoning",
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "First step of reasoning"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 1, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 1, "text"), "Second step of reasoning"),
			},
		},
		{
			name: "reasoning item with hidden text",
			reasoning: &openai.ResponseReasoningItem{
				ID:     "reason-003",
				Type:   "reasoning",
				Status: "completed",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Sensitive reasoning process",
					},
				},
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "reasoning item with empty summaries",
			reasoning: &openai.ResponseReasoningItem{
				ID:      "reason-004",
				Type:    "reasoning",
				Status:  "in_progress",
				Summary: []openai.ResponseReasoningItemSummaryParam{},
			},
			config:        openinference.NewTraceConfig(),
			messageIndex:  2,
			expectedAttrs: []attribute.KeyValue{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseReasoningAttrs(tt.reasoning, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseWebSearchCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		webSearch     *openai.ResponseFunctionWebSearch
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "web search call with search action",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:     "ws-001",
				Type:   "web_search_call",
				Status: "completed",
				Action: openai.ResponseFunctionWebSearchActionUnionParam{
					OfSearch: &openai.ResponseFunctionWebSearchActionSearchParam{
						Type:  "search",
						Query: "golang concurrency patterns",
						Sources: []openai.ResponseFunctionWebSearchActionSearchSourceParam{
							{
								Type: "url",
								URL:  "https://golang.org/doc/effective_go",
							},
						},
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "ws-001"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "web_search_call"),
			},
		},
		{
			name: "web search call with open page action",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:     "ws-002",
				Type:   "web_search_call",
				Status: "completed",
				Action: openai.ResponseFunctionWebSearchActionUnionParam{
					OfOpenPage: &openai.ResponseFunctionWebSearchActionOpenPageParam{
						Type: "open_page",
						URL:  "https://example.com/page",
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "ws-002"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "web_search_call"),
			},
		},
		{
			name: "web search call with hidden output",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:     "ws-003",
				Type:   "web_search_call",
				Status: "completed",
				Action: openai.ResponseFunctionWebSearchActionUnionParam{
					OfFind: &openai.ResponseFunctionWebSearchActionFindParam{
						Type:    "find",
						URL:     "https://private.example.com",
						Pattern: "secret",
					},
				},
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "web search call at different message index",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:     "ws-004",
				Type:   "web_search_call",
				Status: "in_progress",
				Action: openai.ResponseFunctionWebSearchActionUnionParam{
					OfSearch: &openai.ResponseFunctionWebSearchActionSearchParam{
						Type:    "search",
						Query:   "test query",
						Sources: []openai.ResponseFunctionWebSearchActionSearchSourceParam{},
					},
				},
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 2,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(2, 0, openinference.ToolCallID), "ws-004"),
				attribute.String(openinference.OutputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "web_search_call"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseWebSearchCallAttrs(tt.webSearch, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseCustomToolCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		toolCall      *openai.ResponseCustomToolCall
		config        *openinference.TraceConfig
		messageIndex  int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "custom tool call with simple input",
			toolCall: &openai.ResponseCustomToolCall{
				ID:     "custom-001",
				CallID: "custom_call_123",
				Name:   "my_custom_tool",
				Input:  "simple input",
				Type:   "custom_tool_call",
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "custom_call_123"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "my_custom_tool"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"input":"simple input"}`),
			},
		},
		{
			name: "custom tool call with JSON input",
			toolCall: &openai.ResponseCustomToolCall{
				ID:     "custom-002",
				CallID: "custom_call_456",
				Name:   "data_processor",
				Input:  `{"key":"value","nested":{"data":"test"}}`,
				Type:   "custom_tool_call",
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallID), "custom_call_456"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), "data_processor"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), `{"input":"{\"key\":\"value\",\"nested\":{\"data\":\"test\"}}"}`),
			},
		},
		{
			name: "custom tool call with hidden output",
			toolCall: &openai.ResponseCustomToolCall{
				ID:     "custom-003",
				CallID: "custom_call_789",
				Name:   "sensitive_tool",
				Input:  "secret data",
				Type:   "custom_tool_call",
			},
			config: &openinference.TraceConfig{
				HideOutputText: true,
			},
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "custom tool call with empty input",
			toolCall: &openai.ResponseCustomToolCall{
				ID:     "custom-004",
				CallID: "custom_call_empty",
				Name:   "no_param_tool",
				Input:  "",
				Type:   "custom_tool_call",
			},
			config:       openinference.NewTraceConfig(),
			messageIndex: 0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "custom_call_empty"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "no_param_tool"),
				attribute.String(openinference.OutputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"input":""}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := setResponseCustomToolCallAttrs(tt.toolCall, []attribute.KeyValue{}, tt.config, tt.messageIndex)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetResponseOutputAttrs(t *testing.T) {
	// Helper config
	defaultConfig := openinference.NewTraceConfig()
	redactedConfig := &openinference.TraceConfig{HideOutputText: true}

	// Minimal stubs for each union type
	msgContent := openai.ResponseOutputMessageContentUnion{
		OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
			{OfOutputText: &openai.ResponseOutputTextParam{Text: "hi", Type: "output_text"}},
		},
	}
	msg := &openai.ResponseOutputMessage{Role: "assistant", Content: msgContent, Type: "message"}
	fnCall := &openai.ResponseFunctionToolCall{ID: "id", Name: "fn", Arguments: "{}", CallID: "call_id", Type: "function_call"}
	fileSearch := &openai.ResponseFileSearchToolCall{ID: "id", Queries: []string{"q"}, Type: "file_search_call"}
	computerCall := &openai.ResponseComputerToolCall{ID: "id", CallID: "call_id", Type: "computer_call"}
	reasoning := &openai.ResponseReasoningItem{ID: "id", Type: "reasoning"}
	webSearch := &openai.ResponseFunctionWebSearch{ID: "id", Type: "web_search_call"}
	customTool := &openai.ResponseCustomToolCall{ID: "cid", CallID: "callid", Name: "tool", Input: "input", Type: "custom_tool_call"}

	// Each case triggers a different branch in setResponseOutputAttrs
	cases := []struct {
		name   string
		union  openai.ResponseOutputItemUnion
		config *openinference.TraceConfig
		want   []attribute.KeyValue
	}{
		{
			name:   "OfOutputMessage",
			union:  openai.ResponseOutputItemUnion{OfOutputMessage: msg},
			config: defaultConfig,
			want:   setResponseOutputMsgAttrs(msg, nil, defaultConfig, 0),
		},
		{
			name:   "OfFunctionCall",
			union:  openai.ResponseOutputItemUnion{OfFunctionCall: fnCall},
			config: defaultConfig,
			want:   setResponseFunctionCallAttrs(fnCall, nil, defaultConfig, 0),
		},
		{
			name:   "OfFileSearchCall",
			union:  openai.ResponseOutputItemUnion{OfFileSearchCall: fileSearch},
			config: defaultConfig,
			want:   setResponseFileSearchCallAttrs(fileSearch, nil, defaultConfig, 0),
		},
		{
			name:   "OfComputerCall",
			union:  openai.ResponseOutputItemUnion{OfComputerCall: computerCall},
			config: defaultConfig,
			want:   setResponseComputerCallAttrs(computerCall, nil, defaultConfig, 0),
		},
		{
			name:   "OfReasoning",
			union:  openai.ResponseOutputItemUnion{OfReasoning: reasoning},
			config: defaultConfig,
			want:   setResponseReasoningAttrs(reasoning, nil, defaultConfig, 0),
		},
		{
			name:   "OfWebSearchCall",
			union:  openai.ResponseOutputItemUnion{OfWebSearchCall: webSearch},
			config: defaultConfig,
			want:   setResponseWebSearchCallAttrs(webSearch, nil, defaultConfig, 0),
		},
		{
			name:   "OfCustomToolCall",
			union:  openai.ResponseOutputItemUnion{OfCustomToolCall: customTool},
			config: defaultConfig,
			want:   setResponseCustomToolCallAttrs(customTool, nil, defaultConfig, 0),
		},
		{
			name:   "OfCustomToolCall redacted",
			union:  openai.ResponseOutputItemUnion{OfCustomToolCall: customTool},
			config: redactedConfig,
			want:   setResponseCustomToolCallAttrs(customTool, nil, redactedConfig, 0),
		},
		// The rest are TODO branches, just check passthrough (should not panic or add attrs)
		{
			name:   "OfImageGenerationCall",
			union:  openai.ResponseOutputItemUnion{OfImageGenerationCall: &openai.ResponseOutputItemImageGenerationCall{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfCodeInterpreterCall",
			union:  openai.ResponseOutputItemUnion{OfCodeInterpreterCall: &openai.ResponseCodeInterpreterToolCall{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfLocalShellCall",
			union:  openai.ResponseOutputItemUnion{OfLocalShellCall: &openai.ResponseOutputItemLocalShellCall{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfMcpCall",
			union:  openai.ResponseOutputItemUnion{OfMcpCall: &openai.ResponseMcpCall{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfMcpListTools",
			union:  openai.ResponseOutputItemUnion{OfMcpListTools: &openai.ResponseMcpListTools{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfMcpApprovalRequest",
			union:  openai.ResponseOutputItemUnion{OfMcpApprovalRequest: &openai.ResponseMcpApprovalRequest{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfShellCall",
			union:  openai.ResponseOutputItemUnion{OfShellCall: &openai.ResponseFunctionShellToolCall{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfShellCallOutput",
			union:  openai.ResponseOutputItemUnion{OfShellCallOutput: &openai.ResponseFunctionShellToolCallOutput{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfApplyPatchCall",
			union:  openai.ResponseOutputItemUnion{OfApplyPatchCall: &openai.ResponseApplyPatchToolCall{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfApplyPatchCallOutput",
			union:  openai.ResponseOutputItemUnion{OfApplyPatchCallOutput: &openai.ResponseApplyPatchToolCallOutput{}},
			config: defaultConfig,
			want:   nil,
		},
		{
			name:   "OfCompaction",
			union:  openai.ResponseOutputItemUnion{OfCompaction: &openai.ResponseCompactionItem{}},
			config: defaultConfig,
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := setResponseOutputAttrs(&tc.union, nil, tc.config, 0)
			openinference.RequireAttributesEqual(t, tc.want, got)
		})
	}
}

func TestBuildResponsesResponseAttributes(t *testing.T) {
	tests := []struct {
		name          string
		resp          *openai.Response
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic response with model and usage",
			resp: &openai.Response{
				Model: "gpt-4o",
				Usage: &openai.ResponseUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountCompletion, 20),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "response with cache tokens",
			resp: &openai.Response{
				Model: "gpt-4-turbo",
				Usage: &openai.ResponseUsage{
					InputTokens:  100,
					OutputTokens: 50,
					TotalTokens:  150,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     25,
						CacheWriteTokens: 10,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4-turbo"),
				attribute.Int(openinference.LLMTokenCountPrompt, 100),
				attribute.Int(openinference.LLMTokenCountCompletion, 50),
				attribute.Int(openinference.LLMTokenCountTotal, 150),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, 25),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 10),
			},
		},
		{
			name: "response with reasoning tokens",
			resp: &openai.Response{
				Model: "o1",
				Usage: &openai.ResponseUsage{
					InputTokens:  50,
					OutputTokens: 200,
					TotalTokens:  250,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 100,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "o1"),
				attribute.Int(openinference.LLMTokenCountPrompt, 50),
				attribute.Int(openinference.LLMTokenCountCompletion, 200),
				attribute.Int(openinference.LLMTokenCountTotal, 250),
				attribute.Int(openinference.LLMTokenCountCompletionReasoning, 100),
			},
		},
		{
			name: "hide outputs config",
			resp: &openai.Response{
				Model: "gpt-4o",
				Usage: &openai.ResponseUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: &openinference.TraceConfig{HideOutputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
				attribute.String(openinference.OutputValue, openinference.RedactedValue),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountCompletion, 20),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "empty model name (should not add LLMModelName attribute)",
			resp: &openai.Response{
				Model: "",
				Usage: &openai.ResponseUsage{
					InputTokens:  5,
					OutputTokens: 10,
					TotalTokens:  15,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.Int(openinference.LLMTokenCountPrompt, 5),
				attribute.Int(openinference.LLMTokenCountCompletion, 10),
				attribute.Int(openinference.LLMTokenCountTotal, 15),
			},
		},
		{
			name: "nil usage (should not add token count attributes)",
			resp: &openai.Response{
				Model:  "gpt-4o",
				Usage:  nil,
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
			},
		},
		{
			name: "zero token counts (should not add zero token attributes)",
			resp: &openai.Response{
				Model: "gpt-4o",
				Usage: &openai.ResponseUsage{
					InputTokens:  0,
					OutputTokens: 0,
					TotalTokens:  0,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
			},
		},
		{
			name: "response with output message and hide output messages",
			resp: &openai.Response{
				Model: "gpt-4o",
				Usage: &openai.ResponseUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{
					{
						OfOutputMessage: &openai.ResponseOutputMessage{
							Role: "assistant",
							Content: openai.ResponseOutputMessageContentUnion{
								OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
									{
										OfOutputText: &openai.ResponseOutputTextParam{
											Type: "output_text",
											Text: "Hello!",
										},
									},
								},
							},
							Type: "message",
						},
					},
				},
			},
			config: &openinference.TraceConfig{HideOutputMessages: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountCompletion, 20),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "response with output message (not hidden)",
			resp: &openai.Response{
				Model: "gpt-4o",
				Usage: &openai.ResponseUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{
					{
						OfOutputMessage: &openai.ResponseOutputMessage{
							Role: "assistant",
							Content: openai.ResponseOutputMessageContentUnion{
								OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
									{
										OfOutputText: &openai.ResponseOutputTextParam{
											Type: "output_text",
											Text: "Hello!",
										},
									},
								},
							},
							Type: "message",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "Hello!"),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountCompletion, 20),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "response with multiple outputs",
			resp: &openai.Response{
				Model: "gpt-4o",
				Usage: &openai.ResponseUsage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     0,
						CacheWriteTokens: 0,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 0,
					},
				},
				Output: []openai.ResponseOutputItemUnion{
					{
						OfOutputMessage: &openai.ResponseOutputMessage{
							Role: "assistant",
							Content: openai.ResponseOutputMessageContentUnion{
								OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
									{
										OfOutputText: &openai.ResponseOutputTextParam{
											Type: "output_text",
											Text: "First message",
										},
									},
								},
							},
							Type: "message",
						},
					},
					{
						OfFunctionCall: &openai.ResponseFunctionToolCall{
							CallID:    "call_123",
							ID:        "func_123",
							Name:      "get_weather",
							Arguments: `{"location":"NYC"}`,
							Type:      "function_call",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4o"),
				attribute.String(openinference.OutputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.OutputMessageContentAttribute(0, 0, "text"), "First message"),
				attribute.String(openinference.OutputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallID), "call_123"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), "get_weather"),
				attribute.String(openinference.OutputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), `{"location":"NYC"}`),
				attribute.Int(openinference.LLMTokenCountPrompt, 10),
				attribute.Int(openinference.LLMTokenCountCompletion, 20),
				attribute.Int(openinference.LLMTokenCountTotal, 30),
			},
		},
		{
			name: "all token details populated",
			resp: &openai.Response{
				Model: "gpt-4-turbo",
				Usage: &openai.ResponseUsage{
					InputTokens:  100,
					OutputTokens: 200,
					TotalTokens:  300,
					InputTokensDetails: openai.ResponseUsageInputTokensDetails{
						CachedTokens:     30,
						CacheWriteTokens: 20,
					},
					OutputTokensDetails: openai.ResponseUsageOutputTokensDetails{
						ReasoningTokens: 50,
					},
				},
				Output: []openai.ResponseOutputItemUnion{},
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMModelName, "gpt-4-turbo"),
				attribute.Int(openinference.LLMTokenCountPrompt, 100),
				attribute.Int(openinference.LLMTokenCountCompletion, 200),
				attribute.Int(openinference.LLMTokenCountTotal, 300),
				attribute.Int(openinference.LLMTokenCountPromptCacheHit, 30),
				attribute.Int(openinference.LLMTokenCountPromptCacheWrite, 20),
				attribute.Int(openinference.LLMTokenCountCompletionReasoning, 50),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildResponsesResponseAttributes(tt.resp, tt.config)
			openinference.RequireAttributesEqual(t, tt.expectedAttrs, got)
		})
	}
}

// An empty response model must not emit llm.model_name at all — on the
// streaming path an empty value would overwrite the request-time
// llm.model_name attribute (OTel attributes are last-write-wins).
func TestBuildResponseAttributesEmptyModelOmitsModelName(t *testing.T) {
	resp := &openai.ChatCompletionResponse{}
	attrs := buildResponseAttributes(resp, &openinference.TraceConfig{})
	for _, attr := range attrs {
		require.NotEqual(t, openinference.LLMModelName, string(attr.Key),
			"llm.model_name must not be set from an empty response model")
	}
}
