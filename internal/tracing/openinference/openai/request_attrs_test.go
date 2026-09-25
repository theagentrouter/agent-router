// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

var (
	basicReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.StringOrUserRoleContentUnion{Value: "Hello!"},
				Role:    openai.ChatMessageRoleUser,
			},
		}},
	}
	basicReqBody = mustJSON(basicReq)

	streamingReq = func() *openai.ChatCompletionRequest {
		streamingReq := *basicReq
		streamingReq.Stream = true
		return &streamingReq
	}()
	streamingReqBody = mustJSON(streamingReq)

	// Multimodal request with text and image.
	multimodalReq = &openai.ChatCompletionRequest{
		Model:     openai.ModelGPT5Nano,
		MaxTokens: ptr(int64(100)),
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfText: &openai.ChatCompletionContentPartTextParam{
							Text: "What is in this image?",
							Type: "text",
						}},
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
							},
							Type: "image_url",
						}},
					},
				},
			},
		}},
	}
	multimodalReqBody = mustJSON(multimodalReq)

	// Request with tools.
	toolsReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: "What is the weather like in Boston today?"},
			},
		}},
		ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
		Tools: []openai.Tool{{
			Type: "function",
			Function: &openai.FunctionDefinition{
				Name:        "get_current_weather",
				Description: "Get the current weather in a given location",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{
							"type":        "string",
							"description": "The city and state, e.g. San Francisco, CA",
						},
						"unit": map[string]any{
							"type": "string",
							"enum": []string{"celsius", "fahrenheit"},
						},
					},
					"required": []string{"location"},
				},
			},
		}},
	}
	toolsReqBody = mustJSON(toolsReq)

	// Request with audio content.
	audioReq = &openai.ChatCompletionRequest{
		Model: "gpt-4o-audio-preview",
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfText: &openai.ChatCompletionContentPartTextParam{
							Text: "Answer in up to 5 words: What do you hear in this audio?",
							Type: "text",
						}},
						{OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
							InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
								Data:   "REDACTED_BASE64_AUDIO_DATA",
								Format: "wav",
							},
							Type: "input_audio",
						}},
					},
				},
			},
		}},
	}
	audioReqBody = mustJSON(audioReq)

	// Request with JSON mode.
	jsonModeReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: "Generate a JSON object with three properties: name, age, and city."},
			},
		}},
		ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
			OfJSONObject: &openai.ChatCompletionResponseFormatJSONObjectParam{
				Type: "json_object",
			},
		},
	}
	jsonModeReqBody = mustJSON(jsonModeReq)

	// Request with system message.
	systemMessageReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{Value: "You are a helpful assistant."},
				},
			},
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "Hello!"},
				},
			},
		},
	}
	systemMessageReqBody = mustJSON(systemMessageReq)

	// Request with empty tool array.
	emptyToolsReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: "Hello!"},
			},
		}},
		Tools: []openai.Tool{},
	}
	emptyToolsReqBody = mustJSON(emptyToolsReq)

	// Request with tool message.
	toolMessageReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather?"},
				},
			},
			{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: nil},
					ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
						ID:   ptr("call_123"),
						Type: "function",
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      "get_weather",
							Arguments: `{"location": "NYC"}`,
						},
					}},
				},
			},
			{
				OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_123",
					Content:    openai.ContentUnion{Value: "Sunny, 72°F"},
				},
			},
		},
	}
	toolMessageReqBody = mustJSON(toolMessageReq)

	// Request with empty image URL.
	emptyImageURLReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role: openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{
					Value: []openai.ChatCompletionContentPartUserUnionParam{
						{OfText: &openai.ChatCompletionContentPartTextParam{
							Text: "What is this?",
							Type: "text",
						}},
						{OfImageURL: &openai.ChatCompletionContentPartImageParam{
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: "", // Empty URL.
							},
							Type: "image_url",
						}},
					},
				},
			},
		}},
	}
	emptyImageURLReqBody = mustJSON(emptyImageURLReq)

	// Request with empty user content.
	emptyContentReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: ""},
			},
		}},
	}
	emptyContentReqBody = mustJSON(emptyContentReq)

	// Request with assistant message string content.
	assistantStringReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Role:    openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{Value: "I'm doing well, thank you!"},
			},
		}},
	}
	assistantStringReqBody = mustJSON(assistantStringReq)

	// Request with assistant message array content.
	assistantArrayReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{
					Value: []openai.ChatCompletionAssistantMessageParamContent{
						{Type: "text", Text: ptr("Part 1")},
						{Type: "text", Text: ptr("Part 2")},
					},
				},
			},
		}},
	}
	assistantArrayReqBody = mustJSON(assistantArrayReq)

	// Request with developer message.
	developerReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: openai.ContentUnion{Value: "Internal developer note"},
			},
		}},
	}
	developerReqBody = mustJSON(developerReq)

	// Request with developer message array content.
	developerArrayReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Role: openai.ChatMessageRoleDeveloper,
				Content: openai.ContentUnion{
					Value: []openai.ChatCompletionContentPartTextParam{
						{Type: "text", Text: "instruction1"},
						{Type: "text", Text: "instruction2"},
					},
				},
			},
		}},
	}
	developerArrayReqBody = mustJSON(developerArrayReq)

	// Request with system message array content.
	systemArrayReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Role: openai.ChatMessageRoleSystem,
				Content: openai.ContentUnion{
					Value: []openai.ChatCompletionContentPartTextParam{
						{Type: "text", Text: "System instruction 1"},
						{Type: "text", Text: "System instruction 2"},
					},
				},
			},
		}},
	}
	systemArrayReqBody = mustJSON(systemArrayReq)

	// Request with tool message array content.
	toolArrayReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "call_456",
				Content: openai.ContentUnion{
					Value: []openai.ChatCompletionContentPartTextParam{
						{Type: "text", Text: "result1"},
						{Type: "text", Text: "result2"},
					},
				},
			},
		}},
	}
	toolArrayReqBody = mustJSON(toolArrayReq)

	// Request with nil content messages.
	nilContentReq = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: nil},
				},
			},
			{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: nil},
				},
			},
			{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{Value: nil},
				},
			},
		},
	}
	nilContentReqBody = mustJSON(nilContentReq)
)

func TestBuildRequestAttributes(t *testing.T) {
	tests := []struct {
		name          string
		req           *openai.ChatCompletionRequest
		reqBody       string
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:    "basic request",
			req:     basicReq,
			reqBody: string(basicReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(basicReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello!"),
			},
		},
		{
			name:    "multimodal request with text and image",
			req:     multimodalReq,
			reqBody: string(multimodalReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(multimodalReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano","max_tokens":100}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "What is in this image?"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "image.image.url"), "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "image"),
			},
		},
		{
			name:    "request with tools",
			req:     toolsReq,
			reqBody: string(toolsReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(toolsReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano","tool_choice":"auto"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "What is the weather like in Boston today?"),
				attribute.String("llm.tools.0.tool.json_schema", `{"type":"function","function":{"name":"get_current_weather","description":"Get the current weather in a given location","parameters":{"properties":{"location":{"description":"The city and state, e.g. San Francisco, CA","type":"string"},"unit":{"enum":["celsius","fahrenheit"],"type":"string"}},"required":["location"],"type":"object"}}}`),
			},
		},
		{
			name:    "request with audio content",
			req:     audioReq,
			reqBody: string(audioReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-4o-audio-preview"),
				attribute.String(openinference.InputValue, string(audioReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-4o-audio-preview"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Answer in up to 5 words: What do you hear in this audio?"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				// Audio content is skipped to match Python OpenInference behavior.
			},
		},
		{
			name:    "request with JSON mode",
			req:     jsonModeReq,
			reqBody: string(jsonModeReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(jsonModeReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano","response_format":{"type":"json_object"}}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Generate a JSON object with three properties: name, age, and city."),
			},
		},
		{
			name:    "request with system message",
			req:     systemMessageReq,
			reqBody: string(systemMessageReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(systemMessageReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleSystem),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "You are a helpful assistant."),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), "Hello!"),
			},
		},
		{
			name:    "request with empty tool array",
			req:     emptyToolsReq,
			reqBody: string(emptyToolsReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(emptyToolsReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello!"),
			},
		},
		{
			name:    "request with tool message",
			req:     toolMessageReq,
			reqBody: string(toolMessageReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(toolMessageReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "What's the weather?"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), openai.ChatMessageRoleAssistant),
				// Assistant message with nil content is skipped.
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), openai.ChatMessageRoleTool),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageContent), "Sunny, 72°F"),
			},
		},
		{
			name:    "request with multimodal content - empty image URL",
			req:     emptyImageURLReq,
			reqBody: string(emptyImageURLReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(emptyImageURLReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "What is this?"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				// Image with empty URL is skipped..
			},
		},
		{
			name:    "request with user message empty content",
			req:     emptyContentReq,
			reqBody: string(emptyContentReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(emptyContentReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				// Empty content is skipped..
			},
		},
		{
			name:    "multimodal request with text redaction",
			req:     multimodalReq,
			reqBody: string(multimodalReqBody),
			config: &openinference.TraceConfig{
				HideInputs:  true,
				HideOutputs: false,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano","max_tokens":100}`),
				// Messages are not included when HideInputs is true.
			},
		},
		{
			name:    "multimodal request with HideInputText",
			req:     multimodalReq,
			reqBody: string(multimodalReqBody),
			config: &openinference.TraceConfig{
				HideInputText: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(multimodalReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano","max_tokens":100}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "image.image.url"), "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "image"),
			},
		},
		{
			name:    "system message with HideInputText",
			req:     systemMessageReq,
			reqBody: string(systemMessageReqBody),
			config: &openinference.TraceConfig{
				HideInputText: true,
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(systemMessageReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleSystem),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), openai.ChatMessageRoleUser),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name:    "assistant message with string content",
			req:     assistantStringReq,
			reqBody: string(assistantStringReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(assistantStringReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleAssistant),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "I'm doing well, thank you!"),
			},
		},
		{
			name:    "assistant message with array content",
			req:     assistantArrayReq,
			reqBody: string(assistantArrayReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(assistantArrayReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleAssistant),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Part 1"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "Part 2"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
			},
		},
		{
			name:    "developer message with string content",
			req:     developerReq,
			reqBody: string(developerReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(developerReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleDeveloper),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Internal developer note"),
			},
		},
		{
			name:    "developer message with array content",
			req:     developerArrayReq,
			reqBody: string(developerArrayReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(developerArrayReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleDeveloper),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "instruction1"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "instruction2"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
			},
		},
		{
			name:    "system message with array content",
			req:     systemArrayReq,
			reqBody: string(systemArrayReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(systemArrayReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleSystem),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "System instruction 1"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "System instruction 2"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
			},
		},
		{
			name:    "tool message with array content",
			req:     toolArrayReq,
			reqBody: string(toolArrayReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(toolArrayReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleTool),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "result1"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "result2"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
			},
		},
		{
			name:    "messages with nil content",
			req:     nilContentReq,
			reqBody: string(nilContentReqBody),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, openai.ModelGPT5Nano),
				attribute.String(openinference.InputValue, string(nilContentReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-5-nano"}`),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), openai.ChatMessageRoleUser),
				// nil content is skipped
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), openai.ChatMessageRoleAssistant),
				// nil content is skipped
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), openai.ChatMessageRoleSystem),
				// nil content is skipped
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config == nil {
				tt.config = openinference.NewTraceConfig()
			}
			attrs := buildRequestAttributes(tt.req, tt.reqBody, tt.config)

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestIsBase64URL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{
			name:     "base64 image",
			url:      "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
			expected: true,
		},
		{
			name:     "not a base64 image URL",
			url:      "https://example.com/image.png",
			expected: false,
		},
		{
			name:     "base64 but not an image",
			url:      "data:text/plain;base64,SGVsbG8gV29ybGQh",
			expected: false,
		},
		{
			name:     "image but not base64",
			url:      "data:image/png,89504E470D0A1A0A",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isBase64URL(tt.url)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildCompletionRequestAttributes(t *testing.T) {
	basicCompletionReq := &openai.CompletionRequest{
		Model:  "gpt-3.5-turbo-instruct",
		Prompt: openai.PromptUnion{Value: "Say this is a test"},
	}
	basicCompletionReqBody := mustJSON(basicCompletionReq)

	arrayCompletionReq := &openai.CompletionRequest{
		Model:  "gpt-3.5-turbo-instruct",
		Prompt: openai.PromptUnion{Value: []string{"Say this is a test", "Say hello"}},
	}
	arrayCompletionReqBody := mustJSON(arrayCompletionReq)

	tokenCompletionReq := &openai.CompletionRequest{
		Model:  "gpt-3.5-turbo-instruct",
		Prompt: openai.PromptUnion{Value: []int64{1, 2, 3}},
	}
	tokenCompletionReqBody := mustJSON(tokenCompletionReq)

	tests := []struct {
		name          string
		req           *openai.CompletionRequest
		reqBody       []byte
		config        *openinference.TraceConfig
		expectedAttrs []attribute.KeyValue
	}{
		{
			name:    "basic string prompt",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(basicCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				attribute.String(openinference.PromptTextAttribute(0), "Say this is a test"),
			},
		},
		{
			name:    "array prompts",
			req:     arrayCompletionReq,
			reqBody: arrayCompletionReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(arrayCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				attribute.String(openinference.PromptTextAttribute(0), "Say this is a test"),
				attribute.String(openinference.PromptTextAttribute(1), "Say hello"),
			},
		},
		{
			name:    "token prompts not recorded",
			req:     tokenCompletionReq,
			reqBody: tokenCompletionReqBody,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(tokenCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				// No prompt attributes for token arrays
			},
		},
		{
			name:    "hide inputs",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{HideInputs: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, openinference.RedactedValue),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				// No prompts when HideInputs is true
			},
		},
		{
			name:    "hide prompts",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{HidePrompts: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(basicCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.LLMInvocationParameters, `{"model":"gpt-3.5-turbo-instruct"}`),
				// No prompt attributes when HidePrompts is true
			},
		},
		{
			name:    "hide invocation parameters",
			req:     basicCompletionReq,
			reqBody: basicCompletionReqBody,
			config:  &openinference.TraceConfig{HideLLMInvocationParameters: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
				attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
				attribute.String(openinference.LLMModelName, "gpt-3.5-turbo-instruct"),
				attribute.String(openinference.InputValue, string(basicCompletionReqBody)),
				attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
				attribute.String(openinference.PromptTextAttribute(0), "Say this is a test"),
				// No LLMInvocationParameters when HideLLMInvocationParameters is true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config == nil {
				tt.config = openinference.NewTraceConfig()
			}
			attrs := buildCompletionRequestAttributes(tt.req, tt.reqBody, tt.config)

			openinference.RequireAttributesEqual(t, tt.expectedAttrs, attrs)
		})
	}
}

func TestSetCustomToolCallOutputAttrs(t *testing.T) {
	tests := []struct {
		name          string
		callOutput    *openai.ResponseCustomToolCallOutputParam
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic string output without redaction",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_123",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("Tool execution result"),
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "tool_call_123"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), `"Tool execution result"`),
			},
		},
		{
			name: "output with HideInputText enabled",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_789",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("Sensitive output data"),
				},
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "output with empty string",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_empty",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr(""),
				},
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.ToolCallID), "tool_call_empty"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageContent), `""`),
			},
		},
		{
			name: "output with complex message",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_complex",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("Status: OK, Result: {\"computed\": true}"),
				},
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(4, openinference.ToolCallID), "tool_call_complex"),
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageContent), `"Status: OK, Result: {\"computed\": true}"`),
			},
		},
		{
			name: "different message index at index 10",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_10",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("output at index 10"),
				},
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(10, openinference.ToolCallID), "tool_call_10"),
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageContent), `"output at index 10"`),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_unhidden",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("visible data"),
				},
			},
			messageIndex: 11,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(11, openinference.ToolCallID), "tool_call_unhidden"),
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageContent), `"visible data"`),
			},
		},
		{
			name: "long output text",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_long",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("This is a long output message with special characters like !@#$%^&*()"),
				},
			},
			messageIndex: 5,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(5, openinference.ToolCallID), "tool_call_long"),
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageContent), `"This is a long output message with special characters like !@#$%^&*()"`),
			},
		},
		{
			name: "output with HideInputText for long text",
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "tool_call_secret",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("This contains sensitive data that should be redacted"),
				},
			},
			messageIndex: 6,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(6, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "call_001",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("result"),
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "call_001"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), `"result"`),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			callOutput: &openai.ResponseCustomToolCallOutputParam{
				CallID: "call_002",
				Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
					OfString: ptr("secret"),
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), openinference.RedactedValue),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setCustomToolCallOutputAttrs(tc.callOutput, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetFileSearchCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		fileSearch    *openai.ResponseFileSearchToolCall
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic file search call without redaction",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_123",
				Type: "file_search",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "file_search_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "file search call with HideInputText enabled",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_456",
				Type: "file_search",
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "file search call with empty ID",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "",
				Type: "file_search",
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), ""),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "file search call with empty type",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_empty_type",
				Type: "",
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallID), "file_search_empty_type"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionName), ""),
			},
		},
		{
			name: "file search call with different message index",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_10",
				Type: "file_search",
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallID), "file_search_10"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_unhidden",
				Type: "file_search",
			},
			messageIndex: 5,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallID), "file_search_unhidden"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "file search call with long ID",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_very_long_id_with_many_characters_that_should_be_stored_without_truncation",
				Type: "file_search",
			},
			messageIndex: 6,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallID), "file_search_very_long_id_with_many_characters_that_should_be_stored_without_truncation"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "file search call with HideInputText for long values",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_with_sensitive_data_very_long_id",
				Type: "file_search",
			},
			messageIndex: 7,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "file search call with special characters in ID",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_123!@#$%^&*()",
				Type: "file_search",
			},
			messageIndex: 8,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallID), "file_search_123!@#$%^&*()"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_001",
				Type: "file_search",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "file_search_001"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_002",
				Type: "file_search",
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "file search at high message index",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_high_index",
				Type: "file_search",
			},
			messageIndex: 50,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(50, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(50, 0, openinference.ToolCallID), "file_search_high_index"),
				attribute.String(openinference.InputMessageToolCallAttribute(50, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "file search with nil config uses default behavior",
			fileSearch: &openai.ResponseFileSearchToolCall{
				ID:   "file_search_nil_config",
				Type: "file_search",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "file_search_nil_config"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setFileSearchCallAttrs(tc.fileSearch, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetWebSearchCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		webSearch     *openai.ResponseFunctionWebSearch
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic web search call without redaction",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_123",
				Type: "function",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "websearch_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "function"),
			},
		},
		{
			name: "web search call with HideInputText enabled",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_456",
				Type: "web_search",
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "web search call with empty ID",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "",
				Type: "search",
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), ""),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "search"),
			},
		},
		{
			name: "web search call with empty type",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_empty_type",
				Type: "",
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallID), "websearch_empty_type"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionName), ""),
			},
		},
		{
			name: "web search call with different message index",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_10",
				Type: "web_search",
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallID), "websearch_10"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallFunctionName), "web_search"),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_unhidden",
				Type: "function",
			},
			messageIndex: 5,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallID), "websearch_unhidden"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallFunctionName), "function"),
			},
		},
		{
			name: "web search call with long ID",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_very_long_id_with_many_characters_that_should_be_stored_without_truncation",
				Type: "search",
			},
			messageIndex: 6,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallID), "websearch_very_long_id_with_many_characters_that_should_be_stored_without_truncation"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallFunctionName), "search"),
			},
		},
		{
			name: "web search call with HideInputText for long values",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_with_sensitive_data_very_long_id",
				Type: "confidential_search_type",
			},
			messageIndex: 7,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "web search call with special characters in ID",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_123!@#$%^&*()",
				Type: "search_with_special_chars_!@#$",
			},
			messageIndex: 8,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallID), "websearch_123!@#$%^&*()"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallFunctionName), "search_with_special_chars_!@#$"),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_001",
				Type: "search",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "websearch_001"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "search"),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_002",
				Type: "web_search",
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "web search at high message index",
			webSearch: &openai.ResponseFunctionWebSearch{
				ID:   "websearch_high_index",
				Type: "search",
			},
			messageIndex: 50,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(50, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(50, 0, openinference.ToolCallID), "websearch_high_index"),
				attribute.String(openinference.InputMessageToolCallAttribute(50, 0, openinference.ToolCallFunctionName), "search"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setWebSearchCallAttrs(tc.webSearch, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetFunctionCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		funcCall      *openai.ResponseFunctionToolCall
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic function call without redaction",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_123",
				Name:      "get_weather",
				Arguments: `{"city": "New York"}`,
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "call_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "get_weather"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"city": "New York"}`),
			},
		},
		{
			name: "function call with HideInputText enabled",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_456",
				Name:      "get_weather",
				Arguments: `{"city": "San Francisco"}`,
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "function call with empty arguments",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_empty_args",
				Name:      "get_current_time",
				Arguments: "",
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), "call_empty_args"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "get_current_time"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionArguments), ""),
			},
		},
		{
			name: "function call with complex nested JSON arguments",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_complex",
				Name:      "database_query",
				Arguments: `{"query": {"filter": {"type": "user", "status": "active"}, "limit": 10, "offset": 0}}`,
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallID), "call_complex"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionName), "database_query"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionArguments), `{"query": {"filter": {"type": "user", "status": "active"}, "limit": 10, "offset": 0}}`),
			},
		},
		{
			name: "function call with different message index",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_10",
				Name:      "search",
				Arguments: `{"query": "ai", "page": 2}`,
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallID), "call_10"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallFunctionName), "search"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallFunctionArguments), `{"query": "ai", "page": 2}`),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_unhidden",
				Name:      "calculate",
				Arguments: `{"x": 5, "y": 3, "operation": "add"}`,
			},
			messageIndex: 5,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallID), "call_unhidden"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallFunctionName), "calculate"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallFunctionArguments), `{"x": 5, "y": 3, "operation": "add"}`),
			},
		},
		{
			name: "function call with long function name",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_long_name",
				Name:      "get_comprehensive_user_profile_with_detailed_metadata",
				Arguments: `{"user_id": 123}`,
			},
			messageIndex: 6,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallID), "call_long_name"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallFunctionName), "get_comprehensive_user_profile_with_detailed_metadata"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallFunctionArguments), `{"user_id": 123}`),
			},
		},
		{
			name: "function call with long arguments",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_long_args",
				Name:      "process_text",
				Arguments: `{"text": "This is a very long argument string with multiple words and special characters like !@#$%^&*() that should be properly handled and stored in the trace attributes. The function should be able to handle arbitrarily long argument strings."}`,
			},
			messageIndex: 7,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallID), "call_long_args"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallFunctionName), "process_text"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallFunctionArguments), `{"text": "This is a very long argument string with multiple words and special characters like !@#$%^&*() that should be properly handled and stored in the trace attributes. The function should be able to handle arbitrarily long argument strings."}`),
			},
		},
		{
			name: "function call with HideInputText for long arguments",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_secret_args",
				Name:      "authenticate",
				Arguments: `{"api_key": "sk-1234567890abcdefghijklmnopqrstuvwxyz", "secret": "very_secret_data_that_should_not_be_logged"}`,
			},
			messageIndex: 8,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_001",
				Name:      "add",
				Arguments: `{"a": 1, "b": 2}`,
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "call_001"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "add"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"a": 1, "b": 2}`),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_002",
				Name:      "multiply",
				Arguments: `{"a": 3, "b": 4}`,
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "function call with special characters in function name",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_special",
				Name:      "get_user_info",
				Arguments: `{"name": "John O'Brien"}`,
			},
			messageIndex: 9,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(9, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(9, 0, openinference.ToolCallID), "call_special"),
				attribute.String(openinference.InputMessageToolCallAttribute(9, 0, openinference.ToolCallFunctionName), "get_user_info"),
				attribute.String(openinference.InputMessageToolCallAttribute(9, 0, openinference.ToolCallFunctionArguments), `{"name": "John O'Brien"}`),
			},
		},
		{
			name: "function call with empty name",
			funcCall: &openai.ResponseFunctionToolCall{
				CallID:    "call_empty_name",
				Name:      "",
				Arguments: `{}`,
			},
			messageIndex: 11,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(11, 0, openinference.ToolCallID), "call_empty_name"),
				attribute.String(openinference.InputMessageToolCallAttribute(11, 0, openinference.ToolCallFunctionName), ""),
				attribute.String(openinference.InputMessageToolCallAttribute(11, 0, openinference.ToolCallFunctionArguments), `{}`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setFunctionCallAttrs(tc.funcCall, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetFunctionCallOutputAttrs(t *testing.T) {
	tests := []struct {
		name          string
		callOutput    *openai.ResponseInputItemFunctionCallOutputParam
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic string output without redaction",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_123",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("Function execution result"),
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "tool_call_123"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), `"Function execution result"`),
			},
		},
		{
			name: "output with HideInputText enabled",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_456",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("Sensitive output data"),
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "output with empty string",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_empty",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr(""),
				},
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.ToolCallID), "tool_call_empty"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageContent), `""`),
			},
		},
		{
			name: "output with array structure",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_array_output",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfResponseFunctionCallOutputItemArray: []openai.ResponseInputItemFunctionCallOutputItemUnionParam{
						{
							OfInputText: &openai.ResponseInputTextContentParam{
								Type: "input_text",
								Text: "result from tool",
							},
						},
					},
				},
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.ToolCallID), "tool_call_array_output"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageContent), `[{"type":"input_text","text":"result from tool"}]`),
			},
		},
		{
			name: "output with multiple array items",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_multiple",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfResponseFunctionCallOutputItemArray: []openai.ResponseInputItemFunctionCallOutputItemUnionParam{
						{
							OfInputText: &openai.ResponseInputTextContentParam{
								Type: "input_text",
								Text: "first result",
							},
						},
						{
							OfInputText: &openai.ResponseInputTextContentParam{
								Type: "input_text",
								Text: "second result",
							},
						},
					},
				},
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(4, openinference.ToolCallID), "tool_call_multiple"),
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageContent), `[{"type":"input_text","text":"first result"},{"type":"input_text","text":"second result"}]`),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_unhidden",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("visible data"),
				},
			},
			messageIndex: 5,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(5, openinference.ToolCallID), "tool_call_unhidden"),
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageContent), `"visible data"`),
			},
		},
		{
			name: "long output text",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_long",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("This is a long output message with special characters like !@#$%^&*() and unicode ñáéíóú"),
				},
			},
			messageIndex: 6,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(6, openinference.ToolCallID), "tool_call_long"),
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageContent), `"This is a long output message with special characters like !@#$%^&*() and unicode ñáéíóú"`),
			},
		},
		{
			name: "output with HideInputText for long text",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_secret",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("This contains sensitive data that should be redacted"),
				},
			},
			messageIndex: 7,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(7, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_001",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("result"),
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "call_001"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), `"result"`),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_002",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("secret"),
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "different message index at index 10",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_10",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("output at index 10"),
				},
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(10, openinference.ToolCallID), "tool_call_10"),
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageContent), `"output at index 10"`),
			},
		},
		{
			name: "output with numeric value as string",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_number",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("42.5"),
				},
			},
			messageIndex: 8,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(8, openinference.ToolCallID), "tool_call_number"),
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageContent), `"42.5"`),
			},
		},
		{
			name: "output with boolean value as string",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_bool",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr("true"),
				},
			},
			messageIndex: 9,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(9, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(9, openinference.ToolCallID), "tool_call_bool"),
				attribute.String(openinference.InputMessageAttribute(9, openinference.MessageContent), `"true"`),
			},
		},
		{
			name: "output with JSON formatted string",
			callOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "tool_call_json_string",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: ptr(`{"status":"success","value":42}`),
				},
			},
			messageIndex: 11,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(11, openinference.ToolCallID), "tool_call_json_string"),
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageContent), `"{\"status\":\"success\",\"value\":42}"`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setFunctionCallOutputAttrs(tc.callOutput, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetCustomToolCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		callInput     *openai.ResponseCustomToolCall
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic tool call without redaction",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_123",
				Name:   "get_weather",
				Input:  "city=Boston",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "tool_call_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "get_weather"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"input":"city=Boston"}`),
			},
		},
		{
			name: "tool call with HideInputText enabled",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_456",
				Name:   "send_email",
				Input:  "to=user@example.com&subject=Hello",
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "tool call with empty input",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_789",
				Name:   "no_params_function",
				Input:  "",
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), "tool_call_789"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "no_params_function"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionArguments), `{"input":""}`),
			},
		},
		{
			name: "tool call with complex JSON input",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_complex",
				Name:   "process_data",
				Input:  `{"key": "value", "nested": {"field": 123}}`,
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallID), "tool_call_complex"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionName), "process_data"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallFunctionArguments), `{"input":"{\"key\": \"value\", \"nested\": {\"field\": 123}}"}`),
			},
		},
		{
			name: "tool call with special characters in input",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_special",
				Name:   "search",
				Input:  `query=hello "world" & co`,
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(4, 0, openinference.ToolCallID), "tool_call_special"),
				attribute.String(openinference.InputMessageToolCallAttribute(4, 0, openinference.ToolCallFunctionName), "search"),
				attribute.String(openinference.InputMessageToolCallAttribute(4, 0, openinference.ToolCallFunctionArguments), `{"input":"query=hello \"world\" & co"}`),
			},
		},
		{
			name: "tool call with different message index",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_index_5",
				Name:   "translate",
				Input:  "text=hello&lang=fr",
			},
			messageIndex: 5,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallID), "tool_call_index_5"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallFunctionName), "translate"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallFunctionArguments), `{"input":"text=hello&lang=fr"}`),
			},
		},
		{
			name: "tool call at high message index",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_20",
				Name:   "calc",
				Input:  "2+2",
			},
			messageIndex: 20,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(20, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(20, 0, openinference.ToolCallID), "tool_call_20"),
				attribute.String(openinference.InputMessageToolCallAttribute(20, 0, openinference.ToolCallFunctionName), "calc"),
				attribute.String(openinference.InputMessageToolCallAttribute(20, 0, openinference.ToolCallFunctionArguments), `{"input":"2+2"}`),
			},
		},
		{
			name: "tool call with HideInputText disabled explicitly",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_explicit_false",
				Name:   "visible_function",
				Input:  "visible_input",
			},
			messageIndex: 6,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallID), "tool_call_explicit_false"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallFunctionName), "visible_function"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallFunctionArguments), `{"input":"visible_input"}`),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing_key", "existing_value"),
			},
			callInput: &openai.ResponseCustomToolCall{
				CallID: "call_001",
				Name:   "func_001",
				Input:  "arg1",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing_key", "existing_value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "call_001"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "func_001"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"input":"arg1"}`),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior_key", "prior_value"),
				attribute.String("another_key", "another_value"),
			},
			callInput: &openai.ResponseCustomToolCall{
				CallID: "call_002",
				Name:   "secret_func",
				Input:  "secret_arg",
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior_key", "prior_value"),
				attribute.String("another_key", "another_value"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "tool call with long function name",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_long_name",
				Name:   "very_long_function_name_with_many_underscores_and_numbers_12345678",
				Input:  "param",
			},
			messageIndex: 7,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallID), "tool_call_long_name"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallFunctionName), "very_long_function_name_with_many_underscores_and_numbers_12345678"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallFunctionArguments), `{"input":"param"}`),
			},
		},
		{
			name: "tool call with newlines and escape sequences in input",
			callInput: &openai.ResponseCustomToolCall{
				CallID: "tool_call_escape",
				Name:   "process_text",
				Input:  "line1\nline2\ttabbed",
			},
			messageIndex: 8,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallID), "tool_call_escape"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallFunctionName), "process_text"),
				attribute.String(openinference.InputMessageToolCallAttribute(8, 0, openinference.ToolCallFunctionArguments), `{"input":"line1\nline2\ttabbed"}`),
			},
		},
		{
			name: "multiple existing attributes with redaction",
			initialAttrs: []attribute.KeyValue{
				attribute.String("attr1", "val1"),
				attribute.String("attr2", "val2"),
				attribute.String("attr3", "val3"),
			},
			callInput: &openai.ResponseCustomToolCall{
				CallID: "call_redact_multi",
				Name:   "multi_redact_func",
				Input:  "input_data",
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("attr1", "val1"),
				attribute.String("attr2", "val2"),
				attribute.String("attr3", "val3"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setCustomToolCallAttrs(tc.callInput, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetReasoningAttrs(t *testing.T) {
	tests := []struct {
		name          string
		reasoning     *openai.ResponseReasoningItem
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "single summary_text without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_1",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "This is a reasoning summary",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "This is a reasoning summary"),
			},
		},
		{
			name: "single summary_text with HideInputText enabled",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_2",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Sensitive reasoning content",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "multiple summary_text entries without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_3",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "First reasoning step",
					},
					{
						Type: "summary_text",
						Text: "Second reasoning step",
					},
					{
						Type: "summary_text",
						Text: "Third reasoning step",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "First reasoning step"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "Second reasoning step"),
				attribute.String(openinference.InputMessageContentAttribute(0, 2, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 2, "text"), "Third reasoning step"),
			},
		},
		{
			name: "multiple summary_text entries with HideInputText enabled",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_4",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Secret reasoning 1",
					},
					{
						Type: "summary_text",
						Text: "Secret reasoning 2",
					},
				},
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "empty summary list",
			reasoning: &openai.ResponseReasoningItem{
				ID:      "reasoning_5",
				Type:    "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{},
			},
			messageIndex: 1,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
			},
		},
		{
			name: "non-summary_text type should be skipped",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_6",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "other_type",
						Text: "This should be skipped",
					},
					{
						Type: "summary_text",
						Text: "This should be included",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "This should be included"),
			},
		},
		{
			name: "mixed types with HideInputText disabled explicitly",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_7",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "not_summary_text",
						Text: "Skip this",
					},
					{
						Type: "summary_text",
						Text: "Include this visible text",
					},
				},
			},
			messageIndex: 3,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(3, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(3, 1, "text"), "Include this visible text"),
			},
		},
		{
			name: "empty string summary text without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_8",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), ""),
			},
		},
		{
			name: "long reasoning text with special characters without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_9",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "This is a long reasoning summary with special characters: !@#$%^&*() and quotes \"like this\"",
					},
				},
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(4, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(4, 0, "text"), "This is a long reasoning summary with special characters: !@#$%^&*() and quotes \"like this\""),
			},
		},
		{
			name: "different message index at index 15",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_10",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Reasoning at high index",
					},
				},
			},
			messageIndex: 15,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(15, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(15, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(15, 0, "text"), "Reasoning at high index"),
			},
		},
		{
			name: "appends to existing attributes without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_11",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "New reasoning content",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing_key", "existing_value"),
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing_key", "existing_value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "New reasoning content"),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_12",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Secret reasoning content",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior_key", "prior_value"),
				attribute.String("another_key", "another_value"),
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior_key", "prior_value"),
				attribute.String("another_key", "another_value"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "multiple summaries with some non-summary_text types",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_13",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Valid summary 1",
					},
					{
						Type: "reasoning_step",
						Text: "This should be skipped",
					},
					{
						Type: "summary_text",
						Text: "Valid summary 2",
					},
					{
						Type: "other",
						Text: "Also skipped",
					},
					{
						Type: "summary_text",
						Text: "Valid summary 3",
					},
				},
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), "Valid summary 1"),
				attribute.String(openinference.InputMessageContentAttribute(2, 2, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 2, "text"), "Valid summary 2"),
				attribute.String(openinference.InputMessageContentAttribute(2, 4, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 4, "text"), "Valid summary 3"),
			},
		},
		{
			name: "multiline reasoning text without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_14",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Line 1\nLine 2\nLine 3",
					},
				},
			},
			messageIndex: 1,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), "Line 1\nLine 2\nLine 3"),
			},
		},
		{
			name: "only non-summary_text types",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_15",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "other_type",
						Text: "Should be skipped",
					},
					{
						Type: "reasoning_text",
						Text: "Also skipped",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
			},
		},
		{
			name: "empty string with HideInputText enabled",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_16",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "",
					},
				},
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "multiple existing attributes without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_17",
				Type: "reasoning",
				Summary: []openai.ResponseReasoningItemSummaryParam{
					{
						Type: "summary_text",
						Text: "Content with existing attributes",
					},
				},
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			initialAttrs: []attribute.KeyValue{
				attribute.String("attr1", "val1"),
				attribute.String("attr2", "val2"),
				attribute.String("attr3", "val3"),
			},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("attr1", "val1"),
				attribute.String("attr2", "val2"),
				attribute.String("attr3", "val3"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(3, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(3, 0, "text"), "Content with existing attributes"),
			},
		},
		{
			name: "large number of summary entries without redaction",
			reasoning: &openai.ResponseReasoningItem{
				ID:   "reasoning_18",
				Type: "reasoning",
				Summary: func() []openai.ResponseReasoningItemSummaryParam {
					var summaries []openai.ResponseReasoningItemSummaryParam
					for i := 0; i < 5; i++ {
						summaries = append(summaries, openai.ResponseReasoningItemSummaryParam{
							Type: "summary_text",
							Text: fmt.Sprintf("Summary %d", i),
						})
					}
					return summaries
				}(),
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: func() []attribute.KeyValue {
				attrs := []attribute.KeyValue{
					attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				}
				for i := 0; i < 5; i++ {
					attrs = append(attrs,
						attribute.String(openinference.InputMessageContentAttribute(0, i, "type"), "text"),
						attribute.String(openinference.InputMessageContentAttribute(0, i, "text"), fmt.Sprintf("Summary %d", i)),
					)
				}
				return attrs
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setReasoningAttrs(tc.reasoning, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetComputerCallOutputAttrs(t *testing.T) {
	tests := []struct {
		name          string
		callOutput    *openai.ResponseInputItemComputerCallOutputParam
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic screenshot output without redaction",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_123",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_abc123",
					ImageURL: "https://example.com/screenshot.png",
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "computer_call_123"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), `{"file_id":"file_abc123","image_url":"https://example.com/screenshot.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "output with HideInputText enabled",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_456",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_secret",
					ImageURL: "https://example.com/sensitive.png",
				},
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "output with empty file ID",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_empty",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "",
					ImageURL: "https://example.com/image.png",
				},
			},
			messageIndex: 3,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.ToolCallID), "computer_call_empty"),
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageContent), `{"image_url":"https://example.com/image.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "output with only image URL",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_url_only",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					ImageURL: "https://example.com/screenshot.jpg",
				},
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(4, openinference.ToolCallID), "computer_call_url_only"),
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageContent), `{"image_url":"https://example.com/screenshot.jpg","type":"computer_screenshot"}`),
			},
		},
		{
			name: "output with only file ID",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_file_only",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:   "computer_screenshot",
					FileID: "file_xyz789",
				},
			},
			messageIndex: 5,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(5, openinference.ToolCallID), "computer_call_file_only"),
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageContent), `{"file_id":"file_xyz789","type":"computer_screenshot"}`),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_visible",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_123",
					ImageURL: "https://example.com/visible.png",
				},
			},
			messageIndex: 6,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(6, openinference.ToolCallID), "computer_call_visible"),
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageContent), `{"file_id":"file_123","image_url":"https://example.com/visible.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "different message index at index 10",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_10",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_10",
					ImageURL: "https://example.com/index10.png",
				},
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(10, openinference.ToolCallID), "computer_call_10"),
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageContent), `{"file_id":"file_10","image_url":"https://example.com/index10.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "output with complex image URL containing query params",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_complex_url",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_complex",
					ImageURL: "https://example.com/screenshot.png?v=1&format=webp&quality=high",
				},
			},
			messageIndex: 7,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(7, openinference.ToolCallID), "computer_call_complex_url"),
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageContent), `{"file_id":"file_complex","image_url":"https://example.com/screenshot.png?v=1\u0026format=webp\u0026quality=high","type":"computer_screenshot"}`),
			},
		},
		{
			name: "output with HideInputText for screenshot",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_secret",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_confidential",
					ImageURL: "https://internal.example.com/secret.png",
				},
			},
			messageIndex: 8,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(8, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "call_001",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_001",
					ImageURL: "https://example.com/shot.png",
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "call_001"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), `{"file_id":"file_001","image_url":"https://example.com/shot.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "call_002",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_002",
					ImageURL: "https://example.com/secret.png",
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "output with URL containing special characters and unicode",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_unicode",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type:     "computer_screenshot",
					FileID:   "file_unicode_ñ",
					ImageURL: "https://example.com/screenshot_ñ_áéíóú.png",
				},
			},
			messageIndex: 9,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(9, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(9, openinference.ToolCallID), "computer_call_unicode"),
				attribute.String(openinference.InputMessageAttribute(9, openinference.MessageContent), `{"file_id":"file_unicode_ñ","image_url":"https://example.com/screenshot_ñ_áéíóú.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "minimal output with only required type field",
			callOutput: &openai.ResponseInputItemComputerCallOutputParam{
				CallID: "computer_call_minimal",
				Output: openai.ResponseComputerToolCallOutputScreenshotParam{
					Type: "computer_screenshot",
				},
			},
			messageIndex: 11,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(11, openinference.ToolCallID), "computer_call_minimal"),
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageContent), `{"type":"computer_screenshot"}`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setComputerCallOutputAttrs(tc.callOutput, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetComputerCallAttrs(t *testing.T) {
	tests := []struct {
		name          string
		computerCall  *openai.ResponseComputerToolCall
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic computer call without redaction",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_123",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "computer_call_123"),
			},
		},
		{
			name: "computer call with HideInputText enabled",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_456",
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
			},
		},
		{
			name: "computer call with empty CallID",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "",
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), ""),
			},
		},
		{
			name: "computer call with different message index",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_10",
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(10, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(10, 0, openinference.ToolCallID), "computer_call_10"),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_unhidden",
			},
			messageIndex: 3,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(3, 0, openinference.ToolCallID), "computer_call_unhidden"),
			},
		},
		{
			name: "computer call with long CallID",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_very_long_id_with_many_characters_that_should_be_stored_without_truncation",
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(4, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(4, 0, openinference.ToolCallID), "computer_call_very_long_id_with_many_characters_that_should_be_stored_without_truncation"),
			},
		},
		{
			name: "computer call with HideInputText for long ID",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_with_sensitive_data_very_long_id",
			},
			messageIndex: 5,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(5, 0, openinference.ToolCallID), openinference.RedactedValue),
			},
		},
		{
			name: "computer call with special characters in CallID",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_123!@#$%^&*()",
			},
			messageIndex: 6,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(6, 0, openinference.ToolCallID), "computer_call_123!@#$%^&*()"),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_001",
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "computer_call_001"),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_002",
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(1, 0, openinference.ToolCallID), openinference.RedactedValue),
			},
		},
		{
			name: "computer call with numeric CallID",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "12345",
			},
			messageIndex: 7,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(7, 0, openinference.ToolCallID), "12345"),
			},
		},
		{
			name: "computer call at high message index",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_high_index",
			},
			messageIndex: 50,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(50, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(50, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(50, 0, openinference.ToolCallID), "computer_call_high_index"),
			},
		},
		{
			name: "multiple computer calls with different indices",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_multi",
			},
			messageIndex: 99,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(99, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(99, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(99, 0, openinference.ToolCallID), "computer_call_multi"),
			},
		},
		{
			name: "computer call always sets assistant message role",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_role_test",
			},
			messageIndex: 11,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(11, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(11, 0, openinference.ToolCallID), "computer_call_role_test"),
			},
		},
		{
			name: "computer call always sets tool call message role",
			computerCall: &openai.ResponseComputerToolCall{
				CallID: "computer_call_tool_role_test",
			},
			messageIndex: 12,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(12, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(12, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(12, 0, openinference.ToolCallID), "computer_call_tool_role_test"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setComputerCallAttrs(tc.computerCall, tc.initialAttrs, tc.config, tc.messageIndex)
			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetOutputMsgAttrs(t *testing.T) {
	tests := []struct {
		name          string
		output        *openai.ResponseOutputMessage
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "basic output text without redaction",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "This is a response",
							},
						},
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "This is a response"),
			},
		},
		{
			name: "basic string content without redaction",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfString: ptr("This is a response"),
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "This is a response"),
			},
		},
		{
			name: "basic string content with redaction",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfString: ptr("This is a response"),
				},
			},
			messageIndex: 0,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "output text with HideInputText enabled",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Sensitive response data",
							},
						},
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "output refusal without redaction",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "I cannot help with this request",
							},
						},
					},
				},
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), "I cannot help with this request"),
			},
		},
		{
			name: "output refusal with HideInputText enabled",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "Refusal reason",
							},
						},
					},
				},
			},
			messageIndex: 3,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(3, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(3, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "empty content slice",
			output: &openai.ResponseOutputMessage{
				Role:    "assistant",
				Content: openai.ResponseOutputMessageContentUnion{},
			},
			messageIndex: 4,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(4, openinference.MessageRole), "assistant"),
			},
		},
		{
			name: "multiple content parts with mixed types",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "First response",
							},
						},
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Second response",
							},
						},
					},
				},
			},
			messageIndex: 5,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(5, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(5, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(5, 0, "text"), "First response"),
				attribute.String(openinference.InputMessageContentAttribute(5, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(5, 1, "text"), "Second response"),
			},
		},
		{
			name: "multiple content with text and refusal",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Here is some output",
							},
						},
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "Cannot do that",
							},
						},
					},
				},
			},
			messageIndex: 6,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(6, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(6, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(6, 0, "text"), "Here is some output"),
				attribute.String(openinference.InputMessageContentAttribute(6, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(6, 1, "text"), "Cannot do that"),
			},
		},
		{
			name: "multiple content with text and refusal, HideInputText enabled",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Here is some output",
							},
						},
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "Cannot do that",
							},
						},
					},
				},
			},
			messageIndex: 7,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(7, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(7, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(7, 0, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(7, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(7, 1, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "output text with empty string",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "",
							},
						},
					},
				},
			},
			messageIndex: 8,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(8, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(8, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(8, 0, "text"), ""),
			},
		},
		{
			name: "output refusal with empty string",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "",
							},
						},
					},
				},
			},
			messageIndex: 9,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(9, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(9, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(9, 0, "text"), ""),
			},
		},
		{
			name: "different message index at index 15",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Response at index 15",
							},
						},
					},
				},
			},
			messageIndex: 15,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(15, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(15, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(15, 0, "text"), "Response at index 15"),
			},
		},
		{
			name: "with HideInputText disabled explicitly",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "visible data",
							},
						},
					},
				},
			},
			messageIndex: 11,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(11, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(11, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(11, 0, "text"), "visible data"),
			},
		},
		{
			name: "long output text",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "This is a very long response text with special characters like !@#$%^&*() and unicode characters like café and 日本語",
							},
						},
					},
				},
			},
			messageIndex: 12,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(12, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(12, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(12, 0, "text"), "This is a very long response text with special characters like !@#$%^&*() and unicode characters like café and 日本語"),
			},
		},
		{
			name: "long output text with HideInputText enabled",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "This contains a lot of sensitive data that should be redacted completely",
							},
						},
					},
				},
			},
			messageIndex: 13,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(13, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(13, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(13, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "appends to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
			},
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "response",
							},
						},
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing", "value"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "response"),
			},
		},
		{
			name: "appends with redaction to existing attributes",
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
			},
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "secret",
							},
						},
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior", "attr"),
				attribute.String("another", "one"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "three content parts with different types",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "First text",
							},
						},
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "Cannot proceed",
							},
						},
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Last text",
							},
						},
					},
				},
			},
			messageIndex: 14,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(14, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(14, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 0, "text"), "First text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 1, "text"), "Cannot proceed"),
				attribute.String(openinference.InputMessageContentAttribute(14, 2, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 2, "text"), "Last text"),
			},
		},
		{
			name: "three content parts redacted",
			output: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "First text",
							},
						},
						{
							OfRefusal: &openai.ResponseOutputRefusalParam{
								Refusal: "Cannot proceed",
							},
						},
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Last text",
							},
						},
					},
				},
			},
			messageIndex: 14,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(14, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(14, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 0, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(14, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 1, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(14, 2, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(14, 2, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "user role in output message",
			output: &openai.ResponseOutputMessage{
				Role: "user",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "User response",
							},
						},
					},
				},
			},
			messageIndex: 16,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(16, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(16, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(16, 0, "text"), "User response"),
			},
		},
		{
			name: "tool role in output message",
			output: &openai.ResponseOutputMessage{
				Role: "tool",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{
							OfOutputText: &openai.ResponseOutputTextParam{
								Text: "Tool output",
							},
						},
					},
				},
			},
			messageIndex: 17,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(17, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageContentAttribute(17, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(17, 0, "text"), "Tool output"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setOutputMsgAttrs(tc.output, tc.initialAttrs, tc.config, tc.messageIndex)
			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetInputMsgContentAttrs(t *testing.T) {
	tests := []struct {
		name          string
		content       []openai.ResponseInputContentUnionParam
		messageIndex  int
		config        *openinference.TraceConfig
		initialAttrs  []attribute.KeyValue
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "single text content without redaction",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Hello, this is input text",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Hello, this is input text"),
			},
		},
		{
			name: "single text content with HideInputText enabled",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Sensitive text content",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "single image content without redaction",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/image.jpg",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), "https://example.com/image.jpg"),
			},
		},
		{
			name: "single image content with HideInputImages enabled",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/sensitive_image.jpg",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputImages: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "image.image.url"), openinference.RedactedValue),
			},
		},
		{
			name: "multiple text content parts without redaction",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "First text part",
					},
				},
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Second text part",
					},
				},
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Third text part",
					},
				},
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), "First text part"),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "text"), "Second text part"),
				attribute.String(openinference.InputMessageContentAttribute(2, 2, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 2, "text"), "Third text part"),
			},
		},
		{
			name: "multiple image content parts without redaction",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/image1.jpg",
					},
				},
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/image2.jpg",
					},
				},
			},
			messageIndex: 1,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "image.image.url"), "https://example.com/image1.jpg"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "image.image.url"), "https://example.com/image2.jpg"),
			},
		},
		{
			name: "mixed text and image content without redaction",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "What is in this image?",
					},
				},
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/photo.jpg",
					},
				},
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Please describe it",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "What is in this image?"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "image.image.url"), "https://example.com/photo.jpg"),
				attribute.String(openinference.InputMessageContentAttribute(0, 2, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 2, "text"), "Please describe it"),
			},
		},
		{
			name: "mixed text and image content with HideInputText enabled",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Secret question about image",
					},
				},
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/secret_photo.jpg",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "image.image.url"), "https://example.com/secret_photo.jpg"),
			},
		},
		{
			name: "mixed text and image content with both HideInputText and HideInputImages enabled",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Secret question",
					},
				},
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/secret_image.jpg",
					},
				},
			},
			messageIndex: 2,
			config:       &openinference.TraceConfig{HideInputText: true, HideInputImages: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), openinference.RedactedValue),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "image.image.url"), openinference.RedactedValue),
			},
		},
		{
			name:          "empty content slice",
			content:       []openai.ResponseInputContentUnionParam{},
			messageIndex:  3,
			config:        openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{},
		},
		{
			name: "text content with empty string",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), ""),
			},
		},
		{
			name: "text content with empty string and HideInputText enabled",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputText: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "image content with empty URL",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "",
					},
				},
			},
			messageIndex: 2,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "image.image.url"), ""),
			},
		},
		{
			name: "text content with special characters",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Special chars: !@#$%^&*() and quotes \"like this\"",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Special chars: !@#$%^&*() and quotes \"like this\""),
			},
		},
		{
			name: "text content with newlines and tabs",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Line 1\nLine 2\tTabbed",
					},
				},
			},
			messageIndex: 1,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), "Line 1\nLine 2\tTabbed"),
			},
		},
		{
			name: "text content with very long text",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "This is a very long text that goes on and on and on with lots of content. " +
							"It contains multiple sentences and even though it's very long, it should still be properly handled. " +
							"The system should be able to process arbitrarily long text content without any issues.",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "This is a very long text that goes on and on and on with lots of content. "+
					"It contains multiple sentences and even though it's very long, it should still be properly handled. "+
					"The system should be able to process arbitrarily long text content without any issues."),
			},
		},
		{
			name: "different message index",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Content at index 10",
					},
				},
			},
			messageIndex: 10,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(10, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(10, 0, "text"), "Content at index 10"),
			},
		},
		{
			name: "appends to existing attributes",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "New content",
					},
				},
			},
			messageIndex: 0,
			initialAttrs: []attribute.KeyValue{
				attribute.String("existing_key", "existing_value"),
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("existing_key", "existing_value"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "New content"),
			},
		},
		{
			name: "appends mixed content to existing attributes",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Question about image",
					},
				},
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/img.jpg",
					},
				},
			},
			messageIndex: 1,
			initialAttrs: []attribute.KeyValue{
				attribute.String("prior_key", "prior_value"),
				attribute.String("another_key", "another_value"),
			},
			config: openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String("prior_key", "prior_value"),
				attribute.String("another_key", "another_value"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), "Question about image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "image.image.url"), "https://example.com/img.jpg"),
			},
		},
		{
			name: "HideInputText disabled explicitly with text content",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "visible text",
					},
				},
			},
			messageIndex: 0,
			config:       &openinference.TraceConfig{HideInputText: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "visible text"),
			},
		},
		{
			name: "HideInputImages disabled explicitly with image content",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/visible_image.jpg",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputImages: false},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "image.image.url"), "https://example.com/visible_image.jpg"),
			},
		},
		{
			name: "image content with complex URL containing query parameters",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "https://example.com/image.jpg?v=1&format=webp&quality=high",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), "https://example.com/image.jpg?v=1&format=webp&quality=high"),
			},
		},
		{
			name: "base64 image URL without redaction",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA"),
			},
		},
		{
			name: "base64 image URL with HideInputImages enabled",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputImage: &openai.ResponseInputImageParam{
						Type:     "input_image",
						ImageURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
					},
				},
			},
			messageIndex: 1,
			config:       &openinference.TraceConfig{HideInputImages: true},
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "image.image.url"), openinference.RedactedValue),
			},
		},
		{
			name: "many content parts (stress test)",
			content: func() []openai.ResponseInputContentUnionParam {
				var parts []openai.ResponseInputContentUnionParam
				for i := 0; i < 5; i++ {
					parts = append(parts, openai.ResponseInputContentUnionParam{
						OfInputText: &openai.ResponseInputTextParam{
							Type: "input_text",
							Text: fmt.Sprintf("Text part %d", i),
						},
					})
				}
				return parts
			}(),
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: func() []attribute.KeyValue {
				var attrs []attribute.KeyValue
				for i := 0; i < 5; i++ {
					attrs = append(attrs,
						attribute.String(openinference.InputMessageContentAttribute(0, i, "type"), "text"),
						attribute.String(openinference.InputMessageContentAttribute(0, i, "text"), fmt.Sprintf("Text part %d", i)),
					)
				}
				return attrs
			}(),
		},
		{
			name: "file content (unhandled - should be skipped)",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputFile: &openai.ResponseInputFileParam{
						Type:   "input_file",
						FileID: "file_123",
					},
				},
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Text after file",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 1, "text"), "Text after file"),
			},
		},
		{
			name: "text content with unicode characters",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: "Unicode: café, naïve, 日本語, 中文, العربية",
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Unicode: café, naïve, 日本語, 中文, العربية"),
			},
		},
		{
			name: "text content with JSON string",
			content: []openai.ResponseInputContentUnionParam{
				{
					OfInputText: &openai.ResponseInputTextParam{
						Type: "input_text",
						Text: `{"key": "value", "nested": {"field": 123}}`,
					},
				},
			},
			messageIndex: 0,
			config:       openinference.NewTraceConfig(),
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), `{"key": "value", "nested": {"field": 123}}`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setInputMsgContentAttrs(tc.content, tc.initialAttrs, tc.config, tc.messageIndex)

			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetEasyInputMsgAttrs(t *testing.T) {
	tests := []struct {
		name          string
		input         *openai.EasyInputMessageParam
		config        *openinference.TraceConfig
		index         int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "string content without hiding",
			input: &openai.EasyInputMessageParam{
				Role: "user",
				Content: openai.EasyInputMessageContentUnionParam{
					OfString: ptr("Hello world"),
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello world"),
			},
		},
		{
			name: "string content with HideInputText",
			input: &openai.EasyInputMessageParam{
				Role: "assistant",
				Content: openai.EasyInputMessageContentUnionParam{
					OfString: ptr("Sensitive response"),
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "nil string content",
			input: &openai.EasyInputMessageParam{
				Role: "user",
				Content: openai.EasyInputMessageContentUnionParam{
					OfString: nil,
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
			},
		},
		{
			name: "content list without hiding",
			input: &openai.EasyInputMessageParam{
				Role: "user",
				Content: openai.EasyInputMessageContentUnionParam{
					OfInputItemContentList: []openai.ResponseInputContentUnionParam{
						{
							OfInputText: &openai.ResponseInputTextParam{
								Type: "input_text",
								Text: "Part 1",
							},
						},
						{
							OfInputImage: &openai.ResponseInputImageParam{
								Type:     "input_image",
								ImageURL: "https://example.com/image.jpg",
							},
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  2,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "text"), "Part 1"),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(2, 1, "image.image.url"), "https://example.com/image.jpg"),
			},
		},
		{
			name: "content list with HideInputImages",
			input: &openai.EasyInputMessageParam{
				Role: "user",
				Content: openai.EasyInputMessageContentUnionParam{
					OfInputItemContentList: []openai.ResponseInputContentUnionParam{
						{
							OfInputImage: &openai.ResponseInputImageParam{
								Type:     "input_image",
								ImageURL: "https://example.com/sensitive.jpg",
							},
						},
					},
				},
			},
			config: &openinference.TraceConfig{HideInputImages: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), openinference.RedactedValue),
			},
		},
		{
			name: "empty content list",
			input: &openai.EasyInputMessageParam{
				Role:    "system",
				Content: openai.EasyInputMessageContentUnionParam{},
			},
			config: openinference.NewTraceConfig(),
			index:  3,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "system"),
			},
		},
		{
			name: "string content with unicode characters",
			input: &openai.EasyInputMessageParam{
				Role: "user",
				Content: openai.EasyInputMessageContentUnionParam{
					OfString: ptr("Hello 世界 🌍"),
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello 世界 🌍"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setEasyInputMsgAttrs(tc.input, []attribute.KeyValue{}, tc.config, tc.index)
			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestSetInputMsgAttrs(t *testing.T) {
	tests := []struct {
		name          string
		input         *openai.ResponseInputItemMessageParam
		config        *openinference.TraceConfig
		index         int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "message with single text content",
			input: &openai.ResponseInputItemMessageParam{
				Role: "user",
				Content: []openai.ResponseInputContentUnionParam{
					{
						OfInputText: &openai.ResponseInputTextParam{
							Type: "input_text",
							Text: "Hello, assistant!",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Hello, assistant!"),
			},
		},
		{
			name: "message with multiple content parts",
			input: &openai.ResponseInputItemMessageParam{
				Role: "user",
				Content: []openai.ResponseInputContentUnionParam{
					{
						OfInputText: &openai.ResponseInputTextParam{
							Type: "input_text",
							Text: "What's in this image?",
						},
					},
					{
						OfInputImage: &openai.ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/photo.jpg",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), "What's in this image?"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(1, 1, "image.image.url"), "https://example.com/photo.jpg"),
			},
		},
		{
			name: "message with HideInputText config",
			input: &openai.ResponseInputItemMessageParam{
				Role: "assistant",
				Content: []openai.ResponseInputContentUnionParam{
					{
						OfInputText: &openai.ResponseInputTextParam{
							Type: "input_text",
							Text: "Secret response",
						},
					},
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "message with HideInputImages config",
			input: &openai.ResponseInputItemMessageParam{
				Role: "user",
				Content: []openai.ResponseInputContentUnionParam{
					{
						OfInputImage: &openai.ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "https://example.com/sensitive.jpg",
						},
					},
				},
			},
			config: &openinference.TraceConfig{HideInputImages: true},
			index:  2,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(2, 0, "image.image.url"), openinference.RedactedValue),
			},
		},
		{
			name: "message with empty content",
			input: &openai.ResponseInputItemMessageParam{
				Role:    "user",
				Content: []openai.ResponseInputContentUnionParam{},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
			},
		},
		{
			name: "message with base64 image URL",
			input: &openai.ResponseInputItemMessageParam{
				Role: "user",
				Content: []openai.ResponseInputContentUnionParam{
					{
						OfInputImage: &openai.ResponseInputImageParam{
							Type:     "input_image",
							ImageURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "image"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "image.image.url"), "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA"),
			},
		},
		{
			name: "tool message with multiple text parts",
			input: &openai.ResponseInputItemMessageParam{
				Role: "tool",
				Content: []openai.ResponseInputContentUnionParam{
					{
						OfInputText: &openai.ResponseInputTextParam{
							Type: "input_text",
							Text: "Result 1",
						},
					},
					{
						OfInputText: &openai.ResponseInputTextParam{
							Type: "input_text",
							Text: "Result 2",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  3,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(3, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageContentAttribute(3, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(3, 0, "text"), "Result 1"),
				attribute.String(openinference.InputMessageContentAttribute(3, 1, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(3, 1, "text"), "Result 2"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := setInputMsgAttrs(tc.input, []attribute.KeyValue{}, tc.config, tc.index)
			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestHandleInputItemUnionAttrs(t *testing.T) {
	tests := []struct {
		name          string
		item          *openai.ResponseInputItemUnionParam
		config        *openinference.TraceConfig
		index         int
		expectedAttrs []attribute.KeyValue
	}{
		{
			name: "easy input message",
			item: &openai.ResponseInputItemUnionParam{
				OfMessage: &openai.EasyInputMessageParam{
					Role: "user",
					Content: openai.EasyInputMessageContentUnionParam{
						OfString: ptr("Simple message"),
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Simple message"),
			},
		},
		{
			name: "input message with content array",
			item: &openai.ResponseInputItemUnionParam{
				OfInputMessage: &openai.ResponseInputItemMessageParam{
					Role: "user",
					Content: []openai.ResponseInputContentUnionParam{
						{
							OfInputText: &openai.ResponseInputTextParam{
								Type: "input_text",
								Text: "Text content",
							},
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Text content"),
			},
		},
		{
			name: "output message",
			item: &openai.ResponseInputItemUnionParam{
				OfOutputMessage: &openai.ResponseOutputMessage{
					Role: "assistant",
					Content: openai.ResponseOutputMessageContentUnion{
						OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
							{
								OfOutputText: &openai.ResponseOutputTextParam{
									Type: "output_text",
									Text: "Output text",
								},
							},
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(1, 0, "text"), "Output text"),
			},
		},
		{
			name: "file search call",
			item: &openai.ResponseInputItemUnionParam{
				OfFileSearchCall: &openai.ResponseFileSearchToolCall{
					ID:   "search_123",
					Type: "file_search",
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "search_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "file_search"),
			},
		},
		{
			name: "file search call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfFileSearchCall: &openai.ResponseFileSearchToolCall{
					ID:   "search_456",
					Type: "file_search",
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "web search call",
			item: &openai.ResponseInputItemUnionParam{
				OfWebSearchCall: &openai.ResponseFunctionWebSearch{
					ID:   "web_123",
					Type: "web_search",
				},
			},
			config: openinference.NewTraceConfig(),
			index:  2,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallID), "web_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(2, 0, openinference.ToolCallFunctionName), "web_search"),
			},
		},
		{
			name: "function call",
			item: &openai.ResponseInputItemUnionParam{
				OfFunctionCall: &openai.ResponseFunctionToolCall{
					CallID:    "call_123",
					Name:      "get_weather",
					Arguments: `{"location": "Boston"}`,
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "call_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "get_weather"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"location": "Boston"}`),
			},
		},
		{
			name: "function call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfFunctionCall: &openai.ResponseFunctionToolCall{
					CallID:    "call_456",
					Name:      "get_weather",
					Arguments: `{"location": "NYC"}`,
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "function call output",
			item: &openai.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
					CallID: "call_123",
					Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: ptr("Weather is sunny"),
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.ToolCallID), "call_123"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), `"Weather is sunny"`),
			},
		},
		{
			name: "function call output with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
					CallID: "call_456",
					Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: ptr("Secret result"),
					},
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "reasoning item",
			item: &openai.ResponseInputItemUnionParam{
				OfReasoning: &openai.ResponseReasoningItem{
					Summary: []openai.ResponseReasoningItemSummaryParam{
						{
							Type: "summary_text",
							Text: "Reasoning summary",
						},
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), "Reasoning summary"),
			},
		},
		{
			name: "reasoning item with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfReasoning: &openai.ResponseReasoningItem{
					Summary: []openai.ResponseReasoningItemSummaryParam{
						{
							Type: "summary_text",
							Text: "Secret reasoning",
						},
					},
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "type"), "text"),
				attribute.String(openinference.InputMessageContentAttribute(0, 0, "text"), openinference.RedactedValue),
			},
		},
		{
			name: "custom tool call",
			item: &openai.ResponseInputItemUnionParam{
				OfCustomToolCall: &openai.ResponseCustomToolCall{
					CallID: "custom_123",
					Name:   "custom_tool",
					Input:  `{"param": "value"}`,
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "custom_123"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "custom_tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"input":"{\"param\": \"value\"}"}`),
			},
		},
		{
			name: "custom tool call output",
			item: &openai.ResponseInputItemUnionParam{
				OfCustomToolCallOutput: &openai.ResponseCustomToolCallOutputParam{
					CallID: "custom_123",
					Output: openai.ResponseCustomToolCallOutputOutputUnionParam{
						OfString: ptr("Tool result"),
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  2,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.ToolCallID), "custom_123"),
				attribute.String(openinference.InputMessageAttribute(2, openinference.MessageContent), `"Tool result"`),
			},
		},
		{
			name: "computer call",
			item: &openai.ResponseInputItemUnionParam{
				OfComputerCall: &openai.ResponseComputerToolCall{
					CallID: "computer_123",
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "computer_123"),
			},
		},
		{
			name: "computer call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfComputerCall: &openai.ResponseComputerToolCall{
					CallID: "computer_456",
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
			},
		},
		{
			name: "computer call output",
			item: &openai.ResponseInputItemUnionParam{
				OfComputerCallOutput: &openai.ResponseInputItemComputerCallOutputParam{
					CallID: "computer_123",
					Output: openai.ResponseComputerToolCallOutputScreenshotParam{
						Type:     "computer_screenshot",
						FileID:   "file_123",
						ImageURL: "https://example.com/screenshot.png",
					},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  1,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.ToolCallID), "computer_123"),
				attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), `{"file_id":"file_123","image_url":"https://example.com/screenshot.png","type":"computer_screenshot"}`),
			},
		},
		{
			name: "computer call output with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfComputerCallOutput: &openai.ResponseInputItemComputerCallOutputParam{
					CallID: "computer_456",
					Output: openai.ResponseComputerToolCallOutputScreenshotParam{
						Type:     "computer_screenshot",
						FileID:   "file_456",
						ImageURL: "https://example.com/sensitive.png",
					},
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "compaction case",
			item: &openai.ResponseInputItemUnionParam{
				OfCompaction: &openai.ResponseCompactionItemParam{ID: "c-1", Type: "compaction"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "c-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "compaction"),
			},
		},
		{
			name: "image generation call",
			item: &openai.ResponseInputItemUnionParam{
				OfImageGenerationCall: &openai.ResponseInputItemImageGenerationCallParam{ID: "img-1", Type: "image_generation_call"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "img-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "image_generation_call"),
			},
		},
		{
			name: "code interpreter call",
			item: &openai.ResponseInputItemUnionParam{
				OfCodeInterpreterCall: &openai.ResponseCodeInterpreterToolCallParam{ID: "ci-1", Code: "print(1)", Type: "code_interpreter_call"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "ci-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "code_interpreter_call"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), "print(1)"),
			},
		},
		{
			name: "local shell call",
			item: &openai.ResponseInputItemUnionParam{
				OfLocalShellCall: &openai.ResponseInputItemLocalShellCallParam{
					CallID: "lsc-1",
					Type:   "local_shell_call",
					Action: openai.ResponseInputItemLocalShellCallActionParam{Command: []string{"ls"}},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "lsc-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "local_shell_call"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `["ls"]`),
			},
		},
		{
			name: "local shell call output",
			item: &openai.ResponseInputItemUnionParam{
				OfLocalShellCallOutput: &openai.ResponseInputItemLocalShellCallOutputParam{ID: "lsco-1", Output: "hello", Type: "local_shell_call_output"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "lsco-1"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "hello"),
			},
		},
		{
			name: "shell call",
			item: &openai.ResponseInputItemUnionParam{
				OfShellCall: &openai.ResponseInputItemShellCallParam{
					CallID: "sc-1",
					Type:   "shell_call",
					Action: openai.ResponseInputItemShellCallActionParam{Commands: []string{"echo hi"}},
				},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "sc-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "shell_call"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `["echo hi"]`),
			},
		},
		{
			name: "shell call output",
			item: &openai.ResponseInputItemUnionParam{
				OfShellCallOutput: &openai.ResponseInputItemShellCallOutputParam{CallID: "sc-1", Type: "shell_call_output"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "sc-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "shell_call_output"),
			},
		},
		{
			name: "apply patch call",
			item: &openai.ResponseInputItemUnionParam{
				OfApplyPatchCall: &openai.ResponseInputItemApplyPatchCallParam{CallID: "apc-1", Type: "apply_patch_call"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "apc-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "apply_patch_call"),
			},
		},
		{
			name: "apply patch call output",
			item: &openai.ResponseInputItemUnionParam{
				OfApplyPatchCallOutput: &openai.ResponseInputItemApplyPatchCallOutputParam{CallID: "apc-1", Output: "patched", Type: "apply_patch_call_output"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "apc-1"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "patched"),
			},
		},
		{
			name: "mcp list tools",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpListTools: &openai.ResponseMcpListTools{ID: "mlt-1", Type: "mcp_list_tools"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "mlt-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "mcp_list_tools"),
			},
		},
		{
			name: "mcp approval request",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpApprovalRequest: &openai.ResponseMcpApprovalRequest{ID: "mar-1", Name: "delete", Arguments: `{}`, Type: "mcp_approval_request"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "mar-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "delete"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{}`),
			},
		},
		{
			name: "mcp approval response",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpApprovalResponse: &openai.ResponseInputItemMcpApprovalResponseParam{ApprovalRequestID: "mar-1", Reason: "approved", Type: "mcp_approval_response"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), "mar-1"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "approved"),
			},
		},
		{
			name: "mcp call",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpCall: &openai.ResponseMcpCall{ID: "mc-1", Name: "search", Arguments: `{"q":"go"}`, Type: "mcp_call"},
			},
			config: openinference.NewTraceConfig(),
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), "mc-1"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), "search"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), `{"q":"go"}`),
			},
		},
		// HideInputText=true cases for all new handler functions.
		{
			name: "compaction case with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfCompaction: &openai.ResponseCompactionItemParam{ID: "c-1", Type: "compaction"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "image generation call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfImageGenerationCall: &openai.ResponseInputItemImageGenerationCallParam{ID: "img-1", Type: "image_generation_call"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "code interpreter call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfCodeInterpreterCall: &openai.ResponseCodeInterpreterToolCallParam{ID: "ci-1", Code: "print(1)", Type: "code_interpreter_call"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "local shell call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfLocalShellCall: &openai.ResponseInputItemLocalShellCallParam{
					CallID: "lsc-1",
					Type:   "local_shell_call",
					Action: openai.ResponseInputItemLocalShellCallActionParam{Command: []string{"ls"}},
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "local shell call output with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfLocalShellCallOutput: &openai.ResponseInputItemLocalShellCallOutputParam{ID: "lsco-1", Output: "hello", Type: "local_shell_call_output"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "shell call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfShellCall: &openai.ResponseInputItemShellCallParam{
					CallID: "sc-1",
					Type:   "shell_call",
					Action: openai.ResponseInputItemShellCallActionParam{Commands: []string{"echo hi"}},
				},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "shell call output with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfShellCallOutput: &openai.ResponseInputItemShellCallOutputParam{CallID: "sc-1", Type: "shell_call_output"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "apply patch call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfApplyPatchCall: &openai.ResponseInputItemApplyPatchCallParam{CallID: "apc-1", Type: "apply_patch_call"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "apply patch call output with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfApplyPatchCallOutput: &openai.ResponseInputItemApplyPatchCallOutputParam{CallID: "apc-1", Output: "patched", Type: "apply_patch_call_output"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "mcp list tools with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpListTools: &openai.ResponseMcpListTools{ID: "mlt-1", Type: "mcp_list_tools"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			},
		},
		{
			name: "mcp approval request with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpApprovalRequest: &openai.ResponseMcpApprovalRequest{ID: "mar-1", Name: "delete", Arguments: `{}`, Type: "mcp_approval_request"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
		{
			name: "mcp approval response with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpApprovalResponse: &openai.ResponseInputItemMcpApprovalResponseParam{ApprovalRequestID: "mar-1", Reason: "approved", Type: "mcp_approval_response"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "tool"),
				attribute.String(openinference.InputMessageAttribute(0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue),
			},
		},
		{
			name: "mcp call with HideInputText",
			item: &openai.ResponseInputItemUnionParam{
				OfMcpCall: &openai.ResponseMcpCall{ID: "mc-1", Name: "search", Arguments: `{"q":"go"}`, Type: "mcp_call"},
			},
			config: &openinference.TraceConfig{HideInputText: true},
			index:  0,
			expectedAttrs: []attribute.KeyValue{
				attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "assistant"),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallID), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
				attribute.String(openinference.InputMessageToolCallAttribute(0, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := handleInputItemUnionAttrs(tc.item, []attribute.KeyValue{}, tc.config, tc.index)
			openinference.RequireAttributesEqual(t, tc.expectedAttrs, attrs)
		})
	}
}

func TestRedactImageFromResponseRequestParameters(t *testing.T) {
	tests := []struct {
		name                 string
		input                any
		hideInputImages      bool
		base64ImageMaxLength int
		expectedImageURLS    map[string]string // path -> expected value
		shouldRedactImages   bool
		shouldHaveError      bool
	}{
		{
			name: "both flags false - should return unchanged",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "https://example.com/image.jpg",
							},
						},
					},
				},
			},
			hideInputImages:      false,
			base64ImageMaxLength: 0,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": "https://example.com/image.jpg",
			},
			shouldRedactImages: false,
			shouldHaveError:    false,
		},
		{
			name: "hideInputImages true - should redact all images",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "https://example.com/image.jpg",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
		{
			name: "base64ImageMaxLength set - should redact long base64 images",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
							},
						},
					},
				},
			},
			hideInputImages:      false,
			base64ImageMaxLength: 10,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
		{
			name: "base64ImageMaxLength set but URL too short - should not redact",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "data:image/png;base64,abc",
							},
						},
					},
				},
			},
			hideInputImages:      false,
			base64ImageMaxLength: 1000,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": "data:image/png;base64,abc",
			},
			shouldRedactImages: false,
			shouldHaveError:    false,
		},
		{
			name: "both hideInputImages and base64ImageMaxLength set",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 1000,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
		{
			name: "non-image content type - should not redact",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type": "input_text",
								"text": "Some text",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS:    map[string]string{},
			shouldRedactImages:   false,
			shouldHaveError:      false,
		},
		{
			name: "missing image_url field - should not error",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type": "input_image",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS:    map[string]string{},
			shouldRedactImages:   false,
			shouldHaveError:      false,
		},
		{
			name: "content is not array - should skip",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": map[string]any{
							"type":      "input_image",
							"image_url": "https://example.com/image.jpg",
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS:    map[string]string{},
			shouldRedactImages:   false,
			shouldHaveError:      false,
		},
		{
			name: "missing input field - should return unchanged",
			input: map[string]any{
				"model": "gpt-4",
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS:    map[string]string{},
			shouldRedactImages:   false,
			shouldHaveError:      false,
		},
		{
			name: "empty input array - should return unchanged",
			input: map[string]any{
				"input": []map[string]any{},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS:    map[string]string{},
			shouldRedactImages:   false,
			shouldHaveError:      false,
		},
		{
			name: "multiple images mixed with other content",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type": "input_text",
								"text": "What is this?",
							},
							{
								"type":      "input_image",
								"image_url": "https://example.com/image1.jpg",
							},
							{
								"type": "input_text",
								"text": "And this?",
							},
							{
								"type":      "input_image",
								"image_url": "https://example.com/image2.jpg",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS: map[string]string{
				"input.0.content.1.image_url": openinference.RedactedValue,
				"input.0.content.3.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
		{
			name: "multiple input items with different content",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "https://example.com/image1.jpg",
							},
						},
					},
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "https://example.com/image2.jpg",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": openinference.RedactedValue,
				"input.1.content.0.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
		{
			name: "base64 URL not matching pattern - should not redact by base64 check",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "data:application/json;base64,eyJrZXkiOiAidmFsdWUifQ==",
							},
						},
					},
				},
			},
			hideInputImages:      false,
			base64ImageMaxLength: 10,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": "data:application/json;base64,eyJrZXkiOiAidmFsdWUifQ==",
			},
			shouldRedactImages: false,
			shouldHaveError:    false,
		},
		{
			name: "empty image_url string - should not redact",
			input: map[string]any{
				"input": []map[string]any{
					{
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS: map[string]string{
				"input.0.content.0.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
		{
			name: "complex nested structure with multiple levels",
			input: map[string]any{
				"model": "gpt-4",
				"input": []map[string]any{
					{
						"role": "user",
						"content": []map[string]any{
							{
								"type": "input_text",
								"text": "First question",
							},
							{
								"type":      "input_image",
								"image_url": "https://example.com/img1.jpg",
							},
						},
					},
					{
						"role": "assistant",
						"content": []map[string]any{
							{
								"type": "input_text",
								"text": "My response",
							},
						},
					},
					{
						"role": "user",
						"content": []map[string]any{
							{
								"type":      "input_image",
								"image_url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
							},
						},
					},
				},
			},
			hideInputImages:      true,
			base64ImageMaxLength: 0,
			expectedImageURLS: map[string]string{
				"input.0.content.1.image_url": openinference.RedactedValue,
				"input.2.content.0.image_url": openinference.RedactedValue,
			},
			shouldRedactImages: true,
			shouldHaveError:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inputBytes := mustJSON(tc.input)
			result, err := redactImageFromResponseRequestParameters(inputBytes, tc.hideInputImages, tc.base64ImageMaxLength)

			if tc.shouldHaveError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify expected URLs
			for path, expectedValue := range tc.expectedImageURLS {
				actualValue := gjson.GetBytes(result, path).String()
				require.Equal(t, expectedValue, actualValue, "mismatch at path %s", path)
			}

			// If not redacting, the output should match the input
			if !tc.shouldRedactImages {
				require.Equal(t, string(inputBytes), string(result))
			}

			// Verify the result is valid JSON
			require.NoError(t, json.Unmarshal(result, &map[string]any{}))
		})
	}
}

func TestBuildResponsesRequestAttributes(t *testing.T) {
	tests := []struct {
		name   string
		req    *openai.ResponseRequest
		body   []byte
		config *openinference.TraceConfig
		check  func(t *testing.T, attrs []attribute.KeyValue)
	}{
		{
			name: "basic response request with model",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Hello!"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": "Hello!"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.SpanKind, openinference.SpanKindLLM))
				require.Contains(t, attrs, attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI))
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				require.Contains(t, attrs, attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello!"))
			},
		},
		{
			name: "response request without model",
			req: &openai.ResponseRequest{
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Test input"),
				},
			},
			body:   mustJSON(map[string]any{"input": "Test input"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.SpanKind, openinference.SpanKindLLM))
				require.Contains(t, attrs, attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI))
				require.Contains(t, attrs, attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Test input"))
			},
		},
		{
			name: "response request with instructions",
			req: &openai.ResponseRequest{
				Model:        "gpt-4o",
				Instructions: "You are a helpful assistant.",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("What is AI?"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "instructions": "You are a helpful assistant.", "input": "What is AI?"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.SpanKind, openinference.SpanKindLLM))
				require.Contains(t, attrs, attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI))
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "system"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "You are a helpful assistant."))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(1, openinference.MessageRole), "user"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(1, openinference.MessageContent), "What is AI?"))
			},
		},
		{
			name: "response request with hidden inputs",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Secret input"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": "Secret input"}),
			config: &openinference.TraceConfig{HideInputs: true},
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
			},
		},
		{
			name: "response request with hidden input text",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Private text"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": "Private text"}),
			config: &openinference.TraceConfig{HideInputText: true},
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), openinference.RedactedValue))
			},
		},
		{
			name: "response request with hidden input messages",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Test"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": "Test"}),
			config: &openinference.TraceConfig{HideInputMessages: true},
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				require.Contains(t, attrs, attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))
				// Verify no message attributes are present
				for _, attr := range attrs {
					require.NotContains(t, string(attr.Key), "llm.input_messages")
				}
			},
		},
		{
			name: "response request with hidden invocation parameters",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Test"),
				},
				Temperature: ptr(0.7),
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": "Test", "temperature": 0.7}),
			config: &openinference.TraceConfig{HideLLMInvocationParameters: true},
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				// Verify invocation parameters not present
				for _, attr := range attrs {
					require.NotEqual(t, openinference.LLMInvocationParameters, attr.Key)
				}
			},
		},
		{
			name: "empty input string",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr(""),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": ""}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), ""))
			},
		},
		{
			name: "empty instructions (should not create system message)",
			req: &openai.ResponseRequest{
				Model:        "gpt-4o",
				Instructions: "",
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Hello"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "input": "Hello"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageContent), "Hello"))
			},
		},
		{
			name: "nil input",
			req: &openai.ResponseRequest{
				Model: "gpt-4o",
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				require.Contains(t, attrs, attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))
			},
		},
		{
			name: "with temperature parameter",
			req: &openai.ResponseRequest{
				Model:       "gpt-4o",
				Temperature: ptr(0.8),
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("What's the weather?"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "temperature": 0.8, "input": "What's the weather?"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				require.Contains(t, attrs, attribute.String(openinference.InputMessageAttribute(0, openinference.MessageRole), "user"))
				// Verify invocation params include temperature
				var foundTemp bool
				for _, attr := range attrs {
					if attr.Key == openinference.LLMInvocationParameters && strings.Contains(attr.Value.AsString(), "temperature") {
						foundTemp = true
						break
					}
				}
				require.True(t, foundTemp, "should have temperature in invocation parameters")
			},
		},
		{
			name: "with max_output_tokens parameter",
			req: &openai.ResponseRequest{
				Model:           "gpt-4o",
				MaxOutputTokens: ptr(int64(100)),
				Input: openai.ResponseNewParamsInputUnion{
					OfString: ptr("Generate text"),
				},
			},
			body:   mustJSON(map[string]any{"model": "gpt-4o", "max_output_tokens": 100, "input": "Generate text"}),
			config: openinference.NewTraceConfig(),
			check: func(t *testing.T, attrs []attribute.KeyValue) {
				require.Contains(t, attrs, attribute.String(openinference.LLMModelName, "gpt-4o"))
				// Verify invocation params include max_output_tokens
				var foundMaxTokens bool
				for _, attr := range attrs {
					if attr.Key == openinference.LLMInvocationParameters && strings.Contains(attr.Value.AsString(), "max_output_tokens") {
						foundMaxTokens = true
						break
					}
				}
				require.True(t, foundMaxTokens, "should have max_output_tokens in invocation parameters")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildResponsesRequestAttributes(tt.req, tt.body, tt.config)
			tt.check(t, got)
		})
	}
}
