// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopenai

import (
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// ChatCassettes returns a slice of all cassettes for chat completions.
// Unlike image generation—which *requires* an image_generation tool call—
// audio synthesis is natively supported.
func ChatCassettes() []Cassette {
	return cassettes(chatRequests)
}

// chatRequests contains the actual request body for each cassette and are
// needed for re-recording the cassettes to get realistic responses.
//
// Prefer bodies in the OpenAI OpenAPI examples to making them up manually.
// See https://github.com/openai/openai-openapi/tree/manual_spec
var chatRequests = map[Cassette]*openai.ChatCompletionRequest{
	CassetteChatBasic:      cassetteChatBasic,
	CassetteAzureChatBasic: cassetteChatBasic,
	CassetteChatStreaming:  withStream(cassetteChatBasic),
	CassetteChatTools: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "What is the weather like in Boston today?",
					},
				},
			},
		},
		Tools: []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
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
			},
		},
		ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
	},
	CassetteChatMultimodal: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{
								OfText: &openai.ChatCompletionContentPartTextParam{
									Type: string(openai.ChatCompletionContentPartTextTypeText),
									Text: "What is in this image?",
								},
							},
							{
								OfImageURL: &openai.ChatCompletionContentPartImageParam{
									Type: openai.ChatCompletionContentPartImageTypeImageURL,
									ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
										URL: "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
									},
								},
							},
						},
					},
				},
			},
		},
		MaxCompletionTokens: ptr.To[int64](100),
	},
	CassetteChatMultiturn: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
					Role: openai.ChatMessageRoleDeveloper,
					Content: openai.ContentUnion{
						Value: "You are a helpful assistant.",
					},
				},
			},
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Hello!",
					},
				},
			},
			{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role: openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{
						Value: "Hello! How can I assist you today?",
					},
				},
			},
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Answer in up to 5 words: What's the weather like?",
					},
				},
			},
		},
		Temperature: new(1.0),
	},
	CassetteChatJSONMode: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Generate a JSON object with three properties: name, age, and city.",
					},
				},
			},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
			OfJSONObject: &openai.ChatCompletionResponseFormatJSONObjectParam{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		},
	},
	CassetteChatNoMessages: {
		Model:    openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{},
	},
	CassetteChatParallelTools: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "What is the weather like in San Francisco?",
					},
				},
			},
		},
		Tools: []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
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
			},
		},
		ToolChoice:        &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
		ParallelToolCalls: new(true),
	},
	CassetteChatBadRequest: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: nil,
					},
				},
			},
		},
		Temperature: new(-0.5),
		MaxTokens:   ptr.To[int64](0),
	},
	CassetteChatImageToText: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{
								OfText: &openai.ChatCompletionContentPartTextParam{
									Type: string(openai.ChatCompletionContentPartTextTypeText),
									Text: "Answer in up to 5 words: What's in this image?",
								},
							},
							{
								OfImageURL: &openai.ChatCompletionContentPartImageParam{
									Type: openai.ChatCompletionContentPartImageTypeImageURL,
									ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
										URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAXElEQVR42mNgoAkofv8fBVOkmQyD/sPwn1UlYAzlE6cZpgGmGZlPkgHYDCHKCcia0AwgHlCm+c+f/9gwabajG0CsK+DOxmIA8YZQ6gXkhISG6W8ALj7RtuMTgwMA0WTdqiU1ensAAAAASUVORK5CYII=",
									},
								},
							},
						},
					},
				},
			},
		},
	},
	CassetteChatUnknownModel: {
		Model: "gpt-4.1-nano-wrong",
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Hello!",
					},
				},
			},
		},
	},
	CassetteChatReasoning: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "A bat and ball cost $1.10. Bat costs $1 more than ball. Ball cost?",
					},
				},
			},
		},
	},
	CassetteChatAudioToText: {
		Model: openai.ModelGPT4oAudioPreview,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{
								OfText: &openai.ChatCompletionContentPartTextParam{
									Type: string(openai.ChatCompletionContentPartTextTypeText),
									Text: "Answer in up to 5 words: What do you hear in this audio?",
								},
							},
							{
								OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{
									Type: openai.ChatCompletionContentPartInputAudioTypeInputAudio,
									InputAudio: openai.ChatCompletionContentPartInputAudioInputAudioParam{
										Data:   "UklGRlwEAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YTgEAADY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycn2NjY2NjY2NjY2CcnJycnJycnJyfY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycn2NjY2NjY2NjY2CcnJycnJycnJyfY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycn2NjY2NjY2NjY2CcnJycnJycnJyfY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycn2NjY2NjY2NjY2CcnJycnJycnJyfY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycn2NjY2NjY2NjY2NgnJycnJycnJyfY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycn2NjY2NjY2NjY2CcnJycnJycnJyfY2NjY2NjY2NjYJycnJycnJycnJ9jY2NjY2NjY2NgnJycnJycnJycnv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/v0BAQEBAv7+/v79AQEBAQL+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAv7+/v79AQEBAQL+/v7+/QEBAQEC/v7+/v0BAQEBAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIA=", // 8-bit style jump sound, 0.135s, 8kHz mono WAV.
										Format: openai.ChatCompletionContentPartInputAudioInputAudioFormatWAV,
									},
								},
							},
						},
					},
				},
			},
		},
	},
	CassetteChatTextToAudio: {
		Model: openai.ModelGPT4oMiniAudioPreview,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Say a single short 'beep' sound, as brief as possible.",
					},
				},
			},
		},
		Audio: &openai.ChatCompletionAudioParam{
			Format: openai.ChatCompletionAudioFormatOpus,
			Voice:  openai.ChatCompletionAudioVoiceAlloy,
		},
		Modalities: []openai.ChatCompletionModality{
			openai.ChatCompletionModalityText,
			openai.ChatCompletionModalityAudio,
		},
	},
	CassetteChatDetailedUsage:          cassetteChatDetailedUsage,
	CassetteChatStreamingDetailedUsage: withStreamUsage(cassetteChatDetailedUsage),
	CassetteChatTextToImageTool: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role: openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{
						Value: "You are an AI assistant that generates simple, sketch-style images with minimal detail. When asked to generate an image, create it with low quality settings for cost efficiency.",
					},
				},
			},
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Draw a simple, minimalist image of a cute cat playing with a ball of yarn in a sketch style.",
					},
				},
			},
		},
		Tools: []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        "generate_image",
					Description: "Generate a simple, minimalist image based on the given prompt in sketch style with low quality for cost efficiency.",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prompt": map[string]any{
								"type":        "string",
								"description": "The text description of the image to generate.",
							},
						},
						"required": []string{"prompt"},
					},
				},
			},
		},
		ToolChoice: &openai.ChatCompletionToolChoiceUnion{
			Value: openai.ChatCompletionNamedToolChoice{
				Type: openai.ToolTypeFunction,
				Function: openai.ChatCompletionNamedToolChoiceFunction{
					Name: "generate_image",
				},
			},
		},
		MaxCompletionTokens: ptr.To[int64](150),
	},
	CassetteChatWebSearch:          cassetteChatWebSearch,
	CassetteChatStreamingWebSearch: withStream(cassetteChatWebSearch),
	CassetteChatOpenAIAgentsPython: {
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role: openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{
						Value: "You are a financial research planner. Given a request for financial analysis, produce a set of web searches to gather the context needed. Aim for recent headlines, earnings  calls or 10‑K snippets, analyst commentary, and industry background. Output between 5 and 15 search terms to query for.",
					},
				},
			},
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Query: tell me about acme",
					},
				},
			},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
			OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
				JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
					Name:   "final_output",
					Strict: true,
					Schema: json.RawMessage(`{
  "$defs": {
    "FinancialSearchItem": {
      "additionalProperties": false,
      "properties": {
        "query": {
          "title": "Query",
          "type": "string"
        },
        "reason": {
          "title": "Reason",
          "type": "string"
        }
      },
      "required": [
        "reason",
        "query"
      ],
      "title": "FinancialSearchItem",
      "type": "object"
    }
  },
  "additionalProperties": false,
  "properties": {
    "searches": {
      "items": {
        "$ref": "#/$defs/FinancialSearchItem"
      },
      "title": "Searches",
      "type": "array"
    }
  },
  "required": [
    "searches"
  ],
  "title": "FinancialSearchPlan",
  "type": "object"
}`),
				},
			},
		},
		Stream: false,
	},
}

// Cassettes that also have streaming variants.
var (
	cassetteChatBasic = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "Hello!",
					},
				},
			},
		},
	}
	cassetteChatDetailedUsage = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT5Nano,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
					Role: openai.ChatMessageRoleDeveloper,
					Content: openai.ContentUnion{
						Value: "Hello, I'm a developer!",
					},
				},
			},
		},
		Temperature:         new(1.0),
		MaxCompletionTokens: ptr.To[int64](100),
	}
	cassetteChatWebSearch = &openai.ChatCompletionRequest{
		Model: openai.ModelGPT4oMiniSearchPreview,
		Messages: []openai.ChatCompletionMessageParamUnion{
			{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: "In up to 5 words, tell me what's at https://httpbin.org/base64/dGVzdA== and include citations",
					},
				},
			},
		},
		WebSearchOptions: &openai.WebSearchOptions{
			SearchContextSize: openai.WebSearchContextSizeLow,
		},
	}
)

func withStream(req *openai.ChatCompletionRequest) *openai.ChatCompletionRequest {
	if req == nil {
		return nil
	}
	clone := *req // shallow copy.
	clone.Stream = true
	return &clone
}

func withStreamUsage(req *openai.ChatCompletionRequest) *openai.ChatCompletionRequest {
	clone := withStream(req)
	clone.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	return clone
}
