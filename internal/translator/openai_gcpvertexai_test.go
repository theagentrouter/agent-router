// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	openaigo "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

// TestResponseModel_GCPVertexAIStreaming tests that GCP Vertex AI streaming returns the request model
// GCP Vertex AI uses deterministic model mapping without virtualization
func TestResponseModel_GCPVertexAIStreaming(t *testing.T) {
	modelName := "gemini-1.5-pro-002"
	translator := NewChatCompletionOpenAIToGCPVertexAITranslator(modelName).(*openAIToGCPVertexAITranslatorV1ChatCompletion)

	// Initialize translator with streaming request
	req := &openai.ChatCompletionRequest{
		Model:  "gemini-1.5-pro",
		Stream: true,
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
	require.True(t, translator.stream)

	// Vertex AI streaming response in JSONL format
	streamResponse := `{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}
`

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(streamResponse)), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel) // Returns the request model since no virtualization
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_RequestBody(t *testing.T) {
	wantBdy := []byte(`{
    "contents": [
        {
            "parts": [
                {
                    "text": "Tell me about AI Gateways"
                }
            ],
            "role": "user"
        }
    ],
    "tools": null,
    "generationConfig": {
        "maxOutputTokens": 100,
        "stopSequences": ["stop1", "stop2"],
        "temperature": 0.1
    },
    "systemInstruction": {
        "parts": [
            {
                "text": "You are a helpful assistant"
            }
        ]
    }
}
`)

	wantBdyWithTools := []byte(`{
    "contents": [
        {
            "parts": [
                {
                    "text": "What's the weather in San Francisco?"
                }
            ],
            "role": "user"
        }
    ],
    "tools": [
        {
            "functionDeclarations": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state, e.g. San Francisco, CA"
                            },
                            "unit": {
                                "type": "string",
                                "enum": ["celsius", "fahrenheit"]
                            }
                        },
                        "required": ["location"]
                    }
                }
            ]
        }
    ],
    "generationConfig": {},
    "systemInstruction": {
        "parts": [
            {
                "text": "You are a helpful assistant"
            }
        ]
    }
}
`)

	wantBdyWithVendorFields := []byte(`{
    "contents": [
        {
            "parts": [
                {
                    "text": "Test with standard fields"
                }
            ],
            "role": "user"
        }
    ],
    "tools": [
        {
            "functionDeclarations": [
                {
                    "name": "test_function",
                    "description": "A test function",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "param1": {
                                "type": "string"
                            }
                        }
                    }
                }
            ]
        }
    ],
    "generationConfig": {
        "maxOutputTokens": 1024,
        "stopSequences": ["stop"],
        "temperature": 0.7,
          "thinkingConfig": {
            "includeThoughts": true,
            "thinkingBudget":  1000
        }
    }
}`)

	wantBdyWithSafetySettingFields := []byte(`{
    "contents": [
        {
            "parts": [
                {
                    "text": "Test with safety setting"
                }
            ],
            "role": "user"
        }
    ],
    "tools": [
        {
            "functionDeclarations": [
                {
                    "name": "test_function",
                    "description": "A test function",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "param1": {
                                "type": "string"
                            }
                        }
                    }
                }
            ]
        }
    ],
    "generationConfig": {
        "maxOutputTokens": 1024,
        "temperature": 0.7
    },
    "safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"}]
}`)

	wantBdyWithMediaResolutionFields := []byte(`{
    "contents": [
        {
            "parts": [
                {
                    "text": "Test with media resolution"
                }
            ],
            "role": "user"
        }
    ],
    "tools": [
        {
            "functionDeclarations": [
                {
                    "name": "test_function",
                    "description": "A test function",
                    "parametersJsonSchema": {
                        "type": "object",
                        "properties": {
                            "param1": {
                                "type": "string"
                            }
                        }
                    }
                }
            ]
        }
    ],
    "generationConfig": {
        "maxOutputTokens": 1024,
		"mediaResolution": "high",
        "temperature": 0.7
    }
}`)

	wantBdyWithGuidedChoice := []byte(`{
  "contents": [
    {
      "parts": [
        {
          "text": "Test with guided choice"
        }
      ],
      "role": "user"
    }
  ],
  "tools": [
    {
      "functionDeclarations": [
        {
          "name": "test_function",
          "description": "A test function",
          "parameters": {
            "type": "object",
            "properties": {
              "param1": {
                "type": "string"
              }
            }
          }
        }
      ]
    }
  ],
  "generationConfig": {
    "maxOutputTokens": 1024,
    "temperature": 0.7,
    "responseMimeType": "text/x.enum",
    "responseSchema": {
      "enum": [
        "Positive",
        "Negative"
      ],
      "type": "STRING"
    }
  }
}`)

	wantBdyWithGuidedRegex := []byte(`{
  "contents": [
    {
      "parts": [
        {
          "text": "Test with guided regex"
        }
      ],
      "role": "user"
    }
  ],
  "tools": [
    {
      "functionDeclarations": [
        {
          "name": "test_function",
          "description": "A test function",
          "parameters": {
            "type": "object",
            "properties": {
              "param1": {
                "type": "string"
              }
            }
          }
        }
      ]
    }
  ],
  "generationConfig": {
    "maxOutputTokens": 1024,
    "temperature": 0.7,
    "responseMimeType": "application/json",
    "responseSchema": {
      "pattern": "\\w+@\\w+\\.com\\n",
      "type": "STRING"
    }
  }
}`)

	wantBdyWithEnterpriseWebSearch := []byte(`{
    "contents": [
        {
            "parts": [
                {
                    "text": "Test with web grounding for enterprise"
                }
            ],
            "role": "user"
        }
    ],
    "tools": [
        {
            "enterpriseWebSearch": {}
        }
    ],
    "generationConfig": {
        "maxOutputTokens": 1024,
        "temperature": 0.7
    }
}`)

	tests := []struct {
		name              string
		modelNameOverride internalapi.ModelNameOverride
		input             openai.ChatCompletionRequest
		onRetry           bool
		wantError         bool
		wantHeaderMut     []internalapi.Header
		wantBody          []byte
	}{
		{
			name: "basic request",
			input: openai.ChatCompletionRequest{
				Stream:      false,
				Model:       "gemini-pro",
				Temperature: ptr.To(0.1),
				MaxTokens:   ptr.To(int64(100)),
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"stop1", "stop2"},
				},
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: "You are a helpful assistant",
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "Tell me about AI Gateways",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			},
			onRetry:   false,
			wantError: false,
			// Since these are stub implementations, we expect nil mutations.
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-pro:generateContent"},
			},
			wantBody: wantBdy,
		},
		{
			name: "basic request with streaming",
			input: openai.ChatCompletionRequest{
				Stream:      true,
				Model:       "gemini-pro",
				Temperature: ptr.To(0.1),
				MaxTokens:   ptr.To(int64(100)),
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"stop1", "stop2"},
				},
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: "You are a helpful assistant",
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "Tell me about AI Gateways",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			},
			onRetry:   false,
			wantError: false,
			// Since these are stub implementations, we expect nil mutations.
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-pro:streamGenerateContent?alt=sse"},
			},
			wantBody: wantBdy,
		},
		{
			name:              "model name override",
			modelNameOverride: "gemini-flash",
			input: openai.ChatCompletionRequest{
				Stream:      false,
				Model:       "gemini-pro",
				Temperature: ptr.To(0.1),
				MaxTokens:   ptr.To(int64(100)),
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfStringArray: []string{"stop1", "stop2"},
				},
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: "You are a helpful assistant",
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "Tell me about AI Gateways",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
			},
			onRetry:   false,
			wantError: false,
			// Since these are stub implementations, we expect nil mutations.
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-flash:generateContent"},
			},
			wantBody: wantBdy,
		},
		{
			name: "request with tools",
			input: openai.ChatCompletionRequest{
				Stream: false,
				Model:  "gemini-pro",
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfSystem: &openai.ChatCompletionSystemMessageParam{
							Content: openai.ContentUnion{
								Value: "You are a helpful assistant",
							},
							Role: openai.ChatMessageRoleSystem,
						},
					},
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Content: openai.StringOrUserRoleContentUnion{
								Value: "What's the weather in San Francisco?",
							},
							Role: openai.ChatMessageRoleUser,
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
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
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-pro:generateContent"},
			},
			wantBody: wantBdyWithTools,
		},
		{
			name: "Request with gcp thinking fields",
			input: openai.ChatCompletionRequest{
				Model:       "gemini-1.5-pro",
				Temperature: ptr.To(0.7),
				MaxTokens:   ptr.To(int64(1024)),
				Stop: openaigo.ChatCompletionNewParamsStopUnion{
					OfString: openaigo.Opt[string]("stop"),
				},
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Test with standard fields"},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "test_function",
							Description: "A test function",
							Parameters: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"param1": map[string]any{
										"type": "string",
									},
								},
							},
						},
					},
				},
				Thinking: &openai.ThinkingUnion{
					OfEnabled: &openai.ThinkingEnabled{
						IncludeThoughts: true,
						BudgetTokens:    1000,
						Type:            "enabled",
					},
				},
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-1.5-pro:generateContent"},
			},
			wantBody: wantBdyWithVendorFields,
		},
		{
			name: "Request with gcp safety setting fields",
			input: openai.ChatCompletionRequest{
				Model:       "gemini-1.5-pro",
				Temperature: ptr.To(0.7),
				MaxTokens:   ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Test with safety setting"},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "test_function",
							Description: "A test function",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"param1": map[string]interface{}{
										"type": "string",
									},
								},
							},
						},
					},
				},
				GCPVertexAIVendorFields: &openai.GCPVertexAIVendorFields{
					SafetySettings: []*genai.SafetySetting{
						{
							Category:  "HARM_CATEGORY_HARASSMENT",
							Threshold: "BLOCK_ONLY_HIGH",
						},
					},
				},
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-1.5-pro:generateContent"},
			},
			wantBody: wantBdyWithSafetySettingFields,
		},
		{
			name: "Request with media resolution fields",
			input: openai.ChatCompletionRequest{
				Model:       "gemini-3-pro",
				Temperature: ptr.To(0.7),
				MaxTokens:   ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Test with media resolution"},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "test_function",
							Description: "A test function",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"param1": map[string]interface{}{
										"type": "string",
									},
								},
							},
						},
					},
				},
				GCPVertexAIVendorFields: &openai.GCPVertexAIVendorFields{
					GenerationConfig: &openai.GCPVertexAIGenerationConfig{
						MediaResolution: "high",
					},
				},
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-3-pro:generateContent"},
			},
			wantBody: wantBdyWithMediaResolutionFields,
		},
		{
			name: "Request with guided choice fields",
			input: openai.ChatCompletionRequest{
				Model:       "gemini-1.5-pro",
				Temperature: ptr.To(0.7),
				MaxTokens:   ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Test with guided choice"},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "test_function",
							Description: "A test function",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"param1": map[string]interface{}{
										"type": "string",
									},
								},
							},
						},
					},
				},
				GuidedChoice: []string{"Positive", "Negative"},
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-1.5-pro:generateContent"},
			},
			wantBody: wantBdyWithGuidedChoice,
		},
		{
			name: "Request with guided regex fields",
			input: openai.ChatCompletionRequest{
				Model:       "gemini-1.5-pro",
				Temperature: ptr.To(0.7),
				MaxTokens:   ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Test with guided regex"},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        "test_function",
							Description: "A test function",
							Parameters: map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"param1": map[string]interface{}{
										"type": "string",
									},
								},
							},
						},
					},
				},
				GuidedRegex: "\\w+@\\w+\\.com\\n",
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-1.5-pro:generateContent"},
			},
			wantBody: wantBdyWithGuidedRegex,
		},
		{
			name: "Request with gcp web grounding for enterprise",
			input: openai.ChatCompletionRequest{
				Model:       "gemini-1.5-pro",
				Temperature: ptr.To(0.7),
				MaxTokens:   ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{
						OfUser: &openai.ChatCompletionUserMessageParam{
							Role:    openai.ChatMessageRoleUser,
							Content: openai.StringOrUserRoleContentUnion{Value: "Test with web grounding for enterprise"},
						},
					},
				},
				Tools: []openai.Tool{
					{
						Type: "enterprise_search",
					},
				},
			},
			onRetry:   false,
			wantError: false,
			wantHeaderMut: []internalapi.Header{
				{":path", "publishers/google/models/gemini-1.5-pro:generateContent"},
			},
			wantBody: wantBdyWithEnterpriseWebSearch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewChatCompletionOpenAIToGCPVertexAITranslator(tc.modelNameOverride)
			headerMut, bodyMut, err := translator.RequestBody(nil, &tc.input, tc.onRetry)
			if tc.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Separate the content-length header from the others and assert it
			// matches the serialized body length, rather than hardcoding a
			// byte count that breaks whenever the body changes by a byte.
			var gotHeaders []internalapi.Header
			foundContentLength := false
			for _, h := range headerMut {
				if h.Key() == contentLengthHeaderName {
					assert.Equal(t, strconv.Itoa(len(bodyMut)), h.Value(),
						"content-length header should equal the serialized body length")
					foundContentLength = true
					continue
				}
				gotHeaders = append(gotHeaders, h)
			}
			assert.True(t, foundContentLength, "content-length header should be set")

			if diff := cmp.Diff(tc.wantHeaderMut, gotHeaders); diff != "" {
				t.Errorf("HeaderMutation mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantBody, bodyMut, bodyMutTransformer(t)); diff != "" {
				t.Errorf("BodyMutation mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_ResponseHeaders(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		headers   map[string]string
		wantError bool
	}{
		{
			name:      "basic headers",
			modelName: "gemini-pro",
			headers: map[string]string{
				"content-type": "application/json",
			},
			wantError: false,
		},
		// TODO: Add more test cases when implementation is ready.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewChatCompletionOpenAIToGCPVertexAITranslator(tc.modelName)
			_, err := translator.ResponseHeaders(tc.headers)
			if tc.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_ResponseBody(t *testing.T) {
	tests := []struct {
		name              string
		modelNameOverride internalapi.ModelNameOverride
		respHeaders       map[string]string
		body              string
		stream            bool
		endOfStream       bool
		wantError         bool
		wantHeaderMut     []internalapi.Header
		wantBodyMut       []byte
		wantTokenUsage    metrics.TokenUsage
	}{
		{
			name: "successful response",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `{
				"candidates": [
					{
						"content": {
							"parts": [
								{
									"text": "AI Gateways act as intermediaries between clients and LLM services."
								}
							]
						},
						"finishReason": "STOP",
						"safetyRatings": []
					}
				],
				"promptFeedback": {
					"safetyRatings": []
				},
				"usageMetadata": {
					"promptTokenCount": 10,
					"candidatesTokenCount": 15,
					"totalTokenCount": 35,
                    "cachedContentTokenCount": 10,
                    "thoughtsTokenCount": 10
				}
			}`,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: []internalapi.Header{{contentLengthHeaderName, "353"}},
			wantBodyMut: []byte(`{
    "choices": [
        {
            "finish_reason": "stop",
            "index": 0,
            "message": {
                "content": "AI Gateways act as intermediaries between clients and LLM services.",
                "role": "assistant"
            }
        }
    ],
    "object": "chat.completion",
    "usage": {
        "completion_tokens": 25,
        "completion_tokens_details": {
            "reasoning_tokens": 10
        },
        "prompt_tokens": 10,
        "prompt_tokens_details": {
            "cached_tokens": 10
        },
        "total_tokens": 35
    }
}`),
			wantTokenUsage: tokenUsageFrom(10, 10, -1, 25, 35, 10),
		},
		{
			name: "response with safety ratings",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `{
				"candidates": [
					{
						"content": {
							"parts": [
								{
									"text": "This is a safe response from the AI assistant."
								}
							]
						},
						"finishReason": "STOP",
						"safetyRatings": [
							{
								"category": "HARM_CATEGORY_HARASSMENT",
								"probability": "LOW"
							},
							{
								"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT",
								"probability": "NEGLIGIBLE"
							},
							{
								"category": "HARM_CATEGORY_DANGEROUS_CONTENT",
								"probability": "MEDIUM"
							}
						]
					}
				],
				"promptFeedback": {
					"safetyRatings": []
				},
				"usageMetadata": {
					"promptTokenCount": 8,
					"candidatesTokenCount": 12,
					"totalTokenCount": 20
				}
			}`,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: []internalapi.Header{{contentLengthHeaderName, "515"}},
			wantBodyMut: []byte(`{
    "choices": [
        {
            "finish_reason": "stop",
            "index": 0,
            "message": {
                "content": "This is a safe response from the AI assistant.",
                "role": "assistant",
                "safety_ratings": [
                    {
                        "category": "HARM_CATEGORY_HARASSMENT",
                        "probability": "LOW"
                    },
                    {
                        "category": "HARM_CATEGORY_SEXUALLY_EXPLICIT",
                        "probability": "NEGLIGIBLE"
                    },
                    {
                        "category": "HARM_CATEGORY_DANGEROUS_CONTENT",
                        "probability": "MEDIUM"
                    }
                ]
            }
        }
    ],
    "object": "chat.completion",
    "usage": {
        "completion_tokens": 12,
        "completion_tokens_details": {},
        "prompt_tokens": 8,
        "prompt_tokens_details": {},
        "total_tokens": 20
    }
}`),
			wantTokenUsage: tokenUsageFrom(8, 0, -1, 12, 20, 0),
		},
		{
			name: "empty response",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body:           `{}`,
			endOfStream:    true,
			wantError:      false,
			wantHeaderMut:  []internalapi.Header{{contentLengthHeaderName, "28"}},
			wantBodyMut:    []byte(`{"object":"chat.completion"}`),
			wantTokenUsage: tokenUsageFrom(0, -1, -1, 0, 0, -1),
		},
		{
			name: "single stream chunk response",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}

`,
			stream:        true,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: nil,
			wantBodyMut: []byte(`data: {"choices":[{"index":0,"delta":{"content":"Hello","role":"assistant"}}],"object":"chat.completion.chunk"}

data: {"choices":[],"object":"chat.completion.chunk","usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"completion_tokens_details":{},"prompt_tokens_details":{}}}

data: [DONE]
`),
			wantTokenUsage: tokenUsageFrom(5, 0, -1, 3, 8, 0), // Does not support cache creation.
		},
		{
			name: "response with model version field",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `{
				"modelVersion": "gemini-1.5-pro-002",
				"candidates": [
					{
						"content": {
							"parts": [
								{
									"text": "Response with model version set."
								}
							]
						},
						"finishReason": "STOP",
						"safetyRatings": []
					}
				],
				"promptFeedback": {
					"safetyRatings": []
				},
				"usageMetadata": {
					"promptTokenCount": 6,
					"candidatesTokenCount": 8,
					"totalTokenCount": 14
				}
			}`,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: []internalapi.Header{{contentLengthHeaderName, "306"}},
			wantBodyMut: []byte(`{
    "choices": [
        {
            "finish_reason": "stop",
            "index": 0,
            "message": {
                "content": "Response with model version set.",
                "role": "assistant"
            }
        }
    ],
	"model": "gemini-1.5-pro-002",
    "object": "chat.completion",
    "usage": {
        "completion_tokens": 8,
        "completion_tokens_details": {},
        "prompt_tokens": 6,
        "prompt_tokens_details": {},
        "total_tokens": 14
    }
}`),
			wantTokenUsage: tokenUsageFrom(6, 0, -1, 8, 14, 0), // Does not support Cache Creation.
		},

		{
			name: "response with multiple candidates",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `{
    "candidates": [
        {
            "content": {
                "parts": [
                    {
                        "text": "This is a safe response from the AI assistant."
                    }
                ]
            },
            "finishReason": "STOP"
        },
        {
            "content": {
                "parts": [
                    {
                        "text": "This is a safer response from the AI assistant."
                    }
                ]
            },
            "finishReason": "STOP"
        }
    ],
	"model": "gemini-1.5-pro-002",
    "usageMetadata": {
        "promptTokenCount": 8,
        "candidatesTokenCount": 12,
        "totalTokenCount": 20
    }
}`,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: []internalapi.Header{{contentLengthHeaderName, "418"}},
			wantBodyMut: []byte(`{
    "choices": [
        {
            "finish_reason": "stop",
            "index": 0,
            "message": {
                "content": "This is a safe response from the AI assistant.",
                "role": "assistant"
            }
        },
		{
            "finish_reason": "stop",
            "index": 1,
            "message": {
                "content": "This is a safer response from the AI assistant.",
                "role": "assistant"
            }
        }
    ],
    "object": "chat.completion",
    "usage": {
        "completion_tokens": 12,
        "completion_tokens_details": {},
        "prompt_tokens": 8,
        "prompt_tokens_details": {},
        "total_tokens": 20
    }
}`),
			wantTokenUsage: tokenUsageFrom(8, 0, -1, 12, 20, 0), // Does not support Cache Creation.
		},
		{
			name: "response with thought summary",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `{
				"candidates": [
					{
						"content": {
							"parts": [
								{
									"text": "Let me think step by step.",
									"thought": true
								},
								{
									"text": "AI Gateways act as intermediaries between clients and LLM services."
								}
							]
						},
						"finishReason": "STOP",
						"safetyRatings": []
					}
				],
				"promptFeedback": {
					"safetyRatings": []
				},
				"usageMetadata": {
					"promptTokenCount": 10,
					"candidatesTokenCount": 15,
					"totalTokenCount": 35,
                    "cachedContentTokenCount": 10,
                    "thoughtsTokenCount": 10
				}
			}`,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: []internalapi.Header{{contentLengthHeaderName, "402"}},
			wantBodyMut: []byte(`{
    "choices": [
        {
            "finish_reason": "stop",
            "index": 0,
            "message": {
                "content": "AI Gateways act as intermediaries between clients and LLM services.",
                "reasoning_content": "Let me think step by step.",
                "role": "assistant"
            }
        }
    ],
    "object": "chat.completion",
    "usage": {
        "completion_tokens": 25,
        "completion_tokens_details": {
            "reasoning_tokens": 10
        },
        "prompt_tokens": 10,
        "prompt_tokens_details": {
            "cached_tokens": 10
        },
        "total_tokens": 35
    }
}`),

			wantTokenUsage: tokenUsageFrom(10, 10, -1, 25, 35, 10), // Does not support Cache Creation.
		},
		{
			name: "stream chunks with thought summary",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `data: {"candidates":[{"content":{"parts":[{"text":"let me think step by step and reply you.", "thought": true}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":18,"thoughtsTokenCount":10}}`,
			stream:        true,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: nil,
			wantBodyMut: []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think step by step and reply you."}}],"object":"chat.completion.chunk"}

data: {"choices":[{"index":0,"delta":{"content":"Hello","role":"assistant"}}],"object":"chat.completion.chunk"}

data: {"choices":[],"object":"chat.completion.chunk","usage":{"prompt_tokens":5,"completion_tokens":13,"total_tokens":18,"completion_tokens_details":{"reasoning_tokens":10},"prompt_tokens_details":{}}}

data: [DONE]
`),
			wantTokenUsage: tokenUsageFrom(5, 0, -1, 13, 18, 10), // Does not support Cache Creation.
		},
		{
			name: "stream chunks with thought signature on text part",
			respHeaders: map[string]string{
				"content-type": "application/json",
			},
			body: `data: {"candidates":[{"content":{"parts":[{"text":"let me think about this.", "thought": true}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"The answer is 42.", "thoughtSignature": "dGVzdHNpZ25hdHVyZQ=="}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":8,"totalTokenCount":33,"thoughtsTokenCount":15}}`,
			stream:        true,
			endOfStream:   true,
			wantError:     false,
			wantHeaderMut: nil,
			wantBodyMut: []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think about this."}}],"object":"chat.completion.chunk"}

data: {"choices":[{"index":0,"delta":{"content":"The answer is 42.","role":"assistant","thinking_blocks":[{"type":"thinking","signature":"dGVzdHNpZ25hdHVyZQ=="}]}}],"object":"chat.completion.chunk"}

data: {"choices":[],"object":"chat.completion.chunk","usage":{"prompt_tokens":10,"completion_tokens":23,"total_tokens":33,"completion_tokens_details":{"reasoning_tokens":15},"prompt_tokens_details":{}}}

data: [DONE]
`),
			wantTokenUsage: tokenUsageFrom(10, 0, -1, 23, 33, 15),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := bytes.NewReader([]byte(tc.body))
			translator := openAIToGCPVertexAITranslatorV1ChatCompletion{
				modelNameOverride: tc.modelNameOverride,
				stream:            tc.stream,
			}
			headerMut, bodyMut, tokenUsage, _, err := translator.ResponseBody(tc.respHeaders, reader, tc.endOfStream, nil)
			if tc.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			if diff := cmp.Diff(tc.wantHeaderMut, headerMut, cmpopts.IgnoreUnexported(extprocv3.HeaderMutation{}, corev3.HeaderValueOption{}, corev3.HeaderValue{})); diff != "" {
				t.Errorf("HeaderMutation mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantBodyMut, bodyMut, bodyMutTransformer(t)); diff != "" {
				t.Errorf("BodyMutation mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantTokenUsage, tokenUsage, cmp.AllowUnexported(metrics.TokenUsage{})); diff != "" {
				t.Errorf("TokenUsage mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingResponseHeaders(t *testing.T) {
	eventStreamHeaderMutation := []internalapi.Header{{"content-type", "text/event-stream"}}

	tests := []struct {
		name            string
		stream          bool
		headers         map[string]string
		wantMutation    []internalapi.Header
		wantContentType string
	}{
		{
			name:         "non-streaming response",
			stream:       false,
			headers:      map[string]string{"content-type": "application/json"},
			wantMutation: nil,
		},
		{
			name:            "streaming response with application/json",
			stream:          true,
			headers:         map[string]string{"content-type": "application/json"},
			wantMutation:    eventStreamHeaderMutation,
			wantContentType: "text/event-stream",
		},
		{
			name:            "streaming response with text/event-stream",
			stream:          true,
			headers:         map[string]string{"content-type": "text/event-stream"},
			wantMutation:    eventStreamHeaderMutation,
			wantContentType: "text/event-stream",
		},
		{
			name:         "streaming response with other content-type",
			stream:       true,
			headers:      map[string]string{"content-type": "text/plain"},
			wantMutation: eventStreamHeaderMutation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{
				stream: tt.stream,
			}

			headerMut, err := translator.ResponseHeaders(tt.headers)
			require.NoError(t, err)

			if diff := cmp.Diff(tt.wantMutation, headerMut); diff != "" {
				t.Errorf("HeaderMutation mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingResponseBody(t *testing.T) {
	// Test basic streaming response conversion.
	translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{
		stream: true,
	}

	tests := []struct {
		name          string
		gcpChunk      string
		expectedUsage metrics.TokenUsage
		assertBody    func(t *testing.T, bodyStr string)
	}{
		{
			name:          "single candidate in streaming response",
			gcpChunk:      `{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP"}]}`,
			expectedUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
		},
		{
			name:          "finish reason without content (MAX_TOKENS)",
			gcpChunk:      `{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":21,"candidatesTokenCount":71,"totalTokenCount":92}}`,
			expectedUsage: tokenUsageFrom(21, 0, -1, 71, 92, 0),
			assertBody: func(t *testing.T, bodyStr string) {
				// The finish_reason chunk must contain "delta":{} to comply with the OpenAI streaming format.
				require.Contains(t, bodyStr, `"delta":{}`)
				require.Contains(t, bodyStr, `"finish_reason":"length"`)
				// The usage chunk must have empty choices.
				require.Contains(t, bodyStr, `"choices":[]`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headerMut, bodyMut, tokenUsage, _, err := translator.handleStreamingResponse(
				bytes.NewReader([]byte(tt.gcpChunk)),
				false,
				nil,
			)

			require.Nil(t, headerMut)
			require.NoError(t, err)
			require.NotNil(t, bodyMut)
			// Check that the response is in SSE format.
			bodyStr := string(bodyMut)
			require.Contains(t, bodyStr, "data: ")
			require.Contains(t, bodyStr, "chat.completion.chunk")
			require.Equal(t, tt.expectedUsage, tokenUsage)
			if tt.assertBody != nil {
				tt.assertBody(t, bodyStr)
			}
		})
	}
}

func TestExtractToolCallsFromGeminiPartsStream(t *testing.T) {
	toolCalls := []openai.ChatCompletionChunkChoiceDeltaToolCall{}
	tests := []struct {
		name     string
		input    []*genai.Part
		expected func([]openai.ChatCompletionChunkChoiceDeltaToolCall) bool // validator function since UUIDs are random
		wantErr  bool
		errMsg   string
	}{
		{
			name:  "nil parts",
			input: nil,
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				return calls == nil
			},
		},
		{
			name:  "empty parts",
			input: []*genai.Part{},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				return calls == nil
			},
		},
		{
			name: "parts without function calls",
			input: []*genai.Part{
				{Text: "some text"},
				nil,
				{Text: "more text"},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				return calls == nil
			},
		},
		{
			name: "single function call",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "get_weather",
						Args: map[string]any{
							"location": "San Francisco",
							"unit":     "celsius",
						},
					},
				},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				if len(calls) != 1 {
					return false
				}
				call := calls[0]
				return call.ID != nil && *call.ID != "" && // UUID should be non-empty
					call.Type == openai.ChatCompletionMessageToolCallTypeFunction &&
					call.Function.Name == "get_weather" &&
					call.Function.Arguments == `{"location":"San Francisco","unit":"celsius"}` &&
					call.Index == 0 // First tool call should have index 0
			},
		},
		{
			name: "multiple function calls",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "function1",
						Args: map[string]any{"param1": "value1"},
					},
				},
				{Text: "some text between"},
				{
					FunctionCall: &genai.FunctionCall{
						Name: "function2",
						Args: map[string]any{"param2": float64(42)},
					},
				},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				if len(calls) != 2 {
					return false
				}
				// Verify first call
				call1 := calls[0]
				if call1.ID == nil || *call1.ID == "" ||
					call1.Type != openai.ChatCompletionMessageToolCallTypeFunction ||
					call1.Function.Name != "function1" ||
					call1.Function.Arguments != `{"param1":"value1"}` ||
					call1.Index != 0 { // First tool call should have index 0
					return false
				}
				// Verify second call
				call2 := calls[1]
				if call2.ID == nil || *call2.ID == "" ||
					call2.Type != openai.ChatCompletionMessageToolCallTypeFunction ||
					call2.Function.Name != "function2" ||
					call2.Function.Arguments != `{"param2":42}` ||
					call2.Index != 1 { // Second tool call should have index 1
					return false
				}
				// Verify IDs are different (UUIDs should be unique)
				return *call1.ID != *call2.ID
			},
		},
		{
			name: "function call with nil part",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "test_func",
						Args: map[string]any{"test": "value"},
					},
				},
				nil, // nil part should be skipped
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				if len(calls) != 1 {
					return false
				}
				call := calls[0]
				return call.ID != nil && *call.ID != "" &&
					call.Function.Name == "test_func" &&
					call.Index == 0 // Single tool call should have index 0
			},
		},
		{
			name: "function call with empty args",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "no_args_func",
						Args: map[string]any{},
					},
				},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				if len(calls) != 1 {
					return false
				}
				call := calls[0]
				return call.ID != nil && *call.ID != "" &&
					call.Function.Name == "no_args_func" &&
					call.Function.Arguments == `{}` &&
					call.Index == 0 // Single tool call should have index 0
			},
		},
		{
			name: "function call with nil args",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "nil_args_func",
						Args: nil,
					},
				},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				if len(calls) != 1 {
					return false
				}
				call := calls[0]
				return call.ID != nil && *call.ID != "" &&
					call.Function.Name == "nil_args_func" &&
					call.Function.Arguments == `null` &&
					call.Index == 0 // Single tool call should have index 0
			},
		},
		{
			name: "function call with complex nested args",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "complex_func",
						Args: map[string]any{
							"user": map[string]any{
								"name": "John",
								"age":  30,
							},
							"items": []any{
								map[string]any{"id": 1, "name": "item1"},
								map[string]any{"id": 2, "name": "item2"},
							},
							"active": true,
						},
					},
				},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				if len(calls) != 1 {
					return false
				}
				call := calls[0]
				// Parse the JSON to verify structure since order might vary
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					return false
				}
				user, ok := args["user"].(map[string]any)
				if !ok || user["name"] != "John" || user["age"] != float64(30) {
					return false
				}
				items, ok := args["items"].([]any)
				if !ok || len(items) != 2 {
					return false
				}
				active, ok := args["active"].(bool)
				if !ok || !active {
					return false
				}
				return call.ID != nil && *call.ID != "" &&
					call.Function.Name == "complex_func" &&
					call.Index == 0 // Single tool call should have index 0
			},
		},
		{
			name: "part with nil function call",
			input: []*genai.Part{
				{
					FunctionCall: nil,
					Text:         "some text",
				},
			},
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				return calls == nil
			},
		},
		{
			name: "function call with unmarshalable args",
			input: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "test_func",
						Args: map[string]any{
							"channel": make(chan int), // channels cannot be marshaled to JSON
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "failed to marshal function arguments",
			expected: func(calls []openai.ChatCompletionChunkChoiceDeltaToolCall) bool {
				return calls == nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)
			calls, _, err := o.extractToolCallsFromGeminiPartsStream(toolCalls, tt.input, json.MarshalForDeterministicTesting)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}

			require.NoError(t, err)

			if !tt.expected(calls) {
				t.Errorf("extractToolCallsFromGeminiPartsStream() result validation failed. Got: %+v", calls)
			}
		})
	}
}

// TestExtractToolCallsStreamVsNonStream tests the differences between streaming and non-streaming extraction
func TestExtractToolCallsStreamVsNonStream(t *testing.T) {
	toolCalls := []openai.ChatCompletionMessageToolCallParam{}
	toolCallsStream := []openai.ChatCompletionChunkChoiceDeltaToolCall{}
	parts := []*genai.Part{
		{
			FunctionCall: &genai.FunctionCall{
				Name: "test_function",
				Args: map[string]any{
					"param1": "value1",
					"param2": 42,
				},
			},
		},
	}
	o := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)

	// Get results from both functions
	streamCalls, _, err := o.extractToolCallsFromGeminiPartsStream(toolCallsStream, parts, json.MarshalForDeterministicTesting)
	require.NoError(t, err)
	require.Len(t, streamCalls, 1)

	nonStreamCalls, _, err := extractToolCallsFromGeminiParts(toolCalls, parts, json.MarshalForDeterministicTesting)
	require.NoError(t, err)
	require.Len(t, nonStreamCalls, 1)

	streamCall := streamCalls[0]
	nonStreamCall := nonStreamCalls[0]

	// Verify function name and arguments are the same
	assert.Equal(t, nonStreamCall.Function.Name, streamCall.Function.Name)
	assert.JSONEq(t, nonStreamCall.Function.Arguments, streamCall.Function.Arguments)
	assert.Equal(t, openai.ChatCompletionMessageToolCallTypeFunction, streamCall.Type)

	// Verify differences:
	// 1. Stream version should have Index field set to 0 for the first tool call
	assert.Equal(t, int64(0), streamCall.Index)

	// 2. Stream version should have a UUID (non-empty string) as ID
	assert.NotNil(t, streamCall.ID)
	assert.NotEmpty(t, *streamCall.ID)
	// UUID should be longer than a simple sequential ID
	assert.Greater(t, len(*streamCall.ID), 10, "Stream ID should be a UUID, got: %s", *streamCall.ID)

	// 3. Non-stream version should have a UUID as well (both generate UUIDs now)
	assert.NotNil(t, nonStreamCall.ID)
	assert.NotEmpty(t, *nonStreamCall.ID)

	// 4. IDs should be different between the two calls (different UUIDs)
	assert.NotEqual(t, *streamCall.ID, *nonStreamCall.ID)

	// Type checking: ensure we get the right types back
	assert.IsType(t, []openai.ChatCompletionChunkChoiceDeltaToolCall{}, streamCalls)
	assert.IsType(t, []openai.ChatCompletionMessageToolCallParam{}, nonStreamCalls)
}

// TestExtractToolCallsStreamIndexing specifically tests that multiple tool calls get correct indices
func TestExtractToolCallsStreamIndexing(t *testing.T) {
	toolCalls := []openai.ChatCompletionChunkChoiceDeltaToolCall{}
	parts := []*genai.Part{
		{
			FunctionCall: &genai.FunctionCall{
				Name: "first_function",
				Args: map[string]any{"param": "value1"},
			},
		},
		{Text: "some text"}, // non-function part should be skipped
		{
			FunctionCall: &genai.FunctionCall{
				Name: "second_function",
				Args: map[string]any{"param": "value2"},
			},
		},
		{
			FunctionCall: &genai.FunctionCall{
				Name: "third_function",
				Args: map[string]any{"param": "value3"},
			},
		},
	}
	o := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)

	calls, _, err := o.extractToolCallsFromGeminiPartsStream(toolCalls, parts, json.MarshalForDeterministicTesting)
	require.NoError(t, err)
	require.Len(t, calls, 3)

	// Verify each tool call has the correct index
	for i, call := range calls {
		assert.Equal(t, int64(i), call.Index, "Tool call %d should have index %d", i, i)
		assert.NotNil(t, call.ID)
		assert.NotEmpty(t, *call.ID)
		assert.Equal(t, openai.ChatCompletionMessageToolCallTypeFunction, call.Type)
	}

	// Verify specific function names and arguments
	assert.Equal(t, "first_function", calls[0].Function.Name)
	assert.JSONEq(t, `{"param":"value1"}`, calls[0].Function.Arguments)

	assert.Equal(t, "second_function", calls[1].Function.Name)
	assert.JSONEq(t, `{"param":"value2"}`, calls[1].Function.Arguments)

	assert.Equal(t, "third_function", calls[2].Function.Name)
	assert.JSONEq(t, `{"param":"value3"}`, calls[2].Function.Arguments)

	// Verify all IDs are unique
	ids := make(map[string]bool)
	for _, call := range calls {
		assert.False(t, ids[*call.ID], "Tool call ID should be unique: %s", *call.ID)
		ids[*call.ID] = true
	}
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingParallelToolIndex(t *testing.T) {
	translator := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)
	// Mock multiple GCP streaming response with parallel tool calls
	gcpToolCallsChunk := `data: {
    "candidates": [
        {
            "content": {
                "parts": [
                    {
                        "functionCall": {
                            "name": "get_weather",
                            "args": {
                                "location": "New York City"
                            }
                        }
                    }
                ],
                "role": "model"
            }
        }
]}

data: {"candidates": [
        {
            "content": {
                "parts": [
                    {
                        "functionCall": {
                            "name": "get_weather",
                            "args": {
                                "location": "Shang Hai"
                            }
                        }
                    }
                ],
                "role": "model"
            }
        }
]}`

	expectedChatCompletionChunks := []openai.ChatCompletionResponseChunk{
		{
			Choices: []openai.ChatCompletionResponseChunkChoice{
				{
					Index: int64(0),
					Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
						Role: "assistant",
						ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
							{
								Index: int64(0),
								ID:    ptr.To("123"),
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Arguments: `{"location":"New York City"}`,
									Name:      "get_weather",
								},
								Type: "function",
							},
						},
					},
				},
			},
			Object: "chat.completion.chunk",
		},
		{
			Choices: []openai.ChatCompletionResponseChunkChoice{
				{
					Index: int64(0),
					Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
						Role: "assistant",
						ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
							{
								Index: int64(1),
								ID:    ptr.To("123"),
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Arguments: `{"location":"Shang Hai"}`,
									Name:      "get_weather",
								},
								Type: "function",
							},
						},
					},
				},
			},
			Object: "chat.completion.chunk",
		},
	}

	headerMut, body, _, _, err := translator.handleStreamingResponse(
		bytes.NewReader([]byte(gcpToolCallsChunk)),
		false,
		nil,
	)

	require.Nil(t, headerMut)
	require.NoError(t, err)
	require.NotNil(t, body)

	chatCompletionChunks := getChatCompletionResponseChunk(body)
	require.Len(t, chatCompletionChunks, 2)

	for idx, chunk := range chatCompletionChunks {
		chunk.Choices[0].Delta.ToolCalls[0].ID = ptr.To("123")
		require.Equal(t, chunk, expectedChatCompletionChunks[idx])
	}
}

// TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingToolCallWithSignature tests that
// streaming tool calls with thought signatures are correctly translated.
func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingToolCallWithSignature(t *testing.T) {
	translator := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)

	// GCP streaming response with thinking followed by tool call with signature
	gcpStreamingChunk := `data: {"candidates":[{"content":{"parts":[{"text":"let me think about this.", "thought": true}]}}]}

data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"location":"Paris"}},"thoughtSignature":"dG9vbGNhbGxzaWduYXR1cmU="}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":10,"totalTokenCount":25}}`

	headerMut, body, tokenUsage, _, err := translator.handleStreamingResponse(
		bytes.NewReader([]byte(gcpStreamingChunk)),
		false,
		nil,
	)

	require.Nil(t, headerMut)
	require.NoError(t, err)
	require.NotNil(t, body)

	chatCompletionChunks := getChatCompletionResponseChunk(body)
	// We expect 3 chunks: thinking content, tool call with signature, and usage
	require.Len(t, chatCompletionChunks, 3)

	// Verify first chunk (thinking content)
	firstChunk := chatCompletionChunks[0]
	assert.Equal(t, "assistant", firstChunk.Choices[0].Delta.Role)
	require.NotNil(t, firstChunk.Choices[0].Delta.ReasoningContent)
	assert.Equal(t, "let me think about this.", firstChunk.Choices[0].Delta.ReasoningContent.Text)

	// Verify second chunk (tool call with signature)
	secondChunk := chatCompletionChunks[1]
	assert.Equal(t, openai.ChatCompletionChoicesFinishReason("tool_calls"), secondChunk.Choices[0].FinishReason)
	require.Len(t, secondChunk.Choices[0].Delta.ToolCalls, 1)
	assert.Equal(t, "get_weather", secondChunk.Choices[0].Delta.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"location":"Paris"}`, secondChunk.Choices[0].Delta.ToolCalls[0].Function.Arguments)

	// Verify signature is present in thinking_blocks (reasoning_content stays a plain string)
	require.Nil(t, secondChunk.Choices[0].Delta.ReasoningContent)
	require.Len(t, secondChunk.Choices[0].Delta.ThinkingBlocks, 1)
	assert.Equal(t, "thinking", secondChunk.Choices[0].Delta.ThinkingBlocks[0].Type)
	assert.Equal(t, "dG9vbGNhbGxzaWduYXR1cmU=", secondChunk.Choices[0].Delta.ThinkingBlocks[0].Signature)

	// Third chunk is usage - verify it exists
	thirdChunk := chatCompletionChunks[2]
	assert.NotNil(t, thirdChunk.Usage)

	// Verify token usage
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(15), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), outputTokens)
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingToolCallSplitFinishReason(t *testing.T) {
	// Newer Gemini models (e.g. gemini-3.5-flash, gemini-3.1-flash-lite) stream
	// the functionCall and the terminal STOP in separate chunks: the functionCall
	// chunk carries no finishReason, and a trailing chunk carries finishReason=STOP
	// with an empty text part (no functionCall). The completion's finish_reason
	// must still be "tool_calls", not "stop". (Older Gemini models carried both in
	// a single chunk; that case is covered by
	// TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingToolCallWithSignature.)
	translator := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-3.5-flash").(*openAIToGCPVertexAITranslatorV1ChatCompletion)

	gcpStreamingChunk := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"location":"Paris"}},"thoughtSignature":"dG9vbGNhbGxzaWduYXR1cmU="}]}}],"usageMetadata":{"trafficType":"ON_DEMAND"}}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":10,"totalTokenCount":25,"thoughtsTokenCount":8}}`

	headerMut, body, tokenUsage, _, err := translator.handleStreamingResponse(
		bytes.NewReader([]byte(gcpStreamingChunk)),
		false,
		nil,
	)

	require.Nil(t, headerMut)
	require.NoError(t, err)
	require.NotNil(t, body)

	chatCompletionChunks := getChatCompletionResponseChunk(body)
	// We expect 3 chunks: tool call (no finish_reason), terminal STOP, and usage.
	require.Len(t, chatCompletionChunks, 3)

	// First chunk carries the tool call and no finish_reason yet.
	firstChunk := chatCompletionChunks[0]
	require.Len(t, firstChunk.Choices, 1)
	assert.Equal(t, openai.ChatCompletionChoicesFinishReason(""), firstChunk.Choices[0].FinishReason)
	require.Len(t, firstChunk.Choices[0].Delta.ToolCalls, 1)
	assert.Equal(t, "get_weather", firstChunk.Choices[0].Delta.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"location":"Paris"}`, firstChunk.Choices[0].Delta.ToolCalls[0].Function.Arguments)

	// Second chunk carries the terminal STOP but no tool call. The finish_reason
	// must be rewritten to "tool_calls" because a tool call was already streamed.
	secondChunk := chatCompletionChunks[1]
	require.Len(t, secondChunk.Choices, 1)
	assert.Equal(t, openai.ChatCompletionChoicesFinishReason("tool_calls"), secondChunk.Choices[0].FinishReason)
	assert.Empty(t, secondChunk.Choices[0].Delta.ToolCalls)

	// Third chunk is usage.
	thirdChunk := chatCompletionChunks[2]
	assert.NotNil(t, thirdChunk.Usage)

	// Completion tokens = candidatesTokenCount(10) + thoughtsTokenCount(8).
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(18), outputTokens)
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingEndOfStream(t *testing.T) {
	translator := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)

	// Test end of stream marker.
	_, body, _, _, err := translator.handleStreamingResponse(
		bytes.NewReader([]byte("")),
		true,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, body)

	// Check that [DONE] marker is present.
	bodyStr := string(body)
	require.Contains(t, bodyStr, "data: [DONE]")
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_parseGCPStreamingChunks(t *testing.T) {
	tests := []struct {
		name         string
		bufferedBody []byte
		input        string
		wantChunks   []genai.GenerateContentResponse
		wantBuffered []byte
	}{
		{
			name:         "single complete chunk",
			bufferedBody: nil,
			input: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}

`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
					UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
						PromptTokenCount:     5,
						CandidatesTokenCount: 3,
						TotalTokenCount:      8,
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name:         "multiple complete chunks",
			bufferedBody: nil,
			input: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":" world"}]}}]}

`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: " world"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name:         "incomplete chunk at end",
			bufferedBody: nil,
			input: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}

data: {"candidates":[{"content":{"parts":`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
				},
			},
			wantBuffered: []byte(`{"candidates":[{"content":{"parts":`),
		},
		{
			name:         "buffered data with new complete chunk",
			bufferedBody: []byte(`{"candidates":[{"content":{"parts":`),
			input: `[{"text":"buffered"}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"new"}]}}]}

`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "buffered"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "new"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name: "invalid JSON chunk in bufferedBody - ignored",
			bufferedBody: []byte(`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}

data: invalid-json

data: {"candidates":[{"content":{"parts":[{"text":"world"}]}}]}

`),
			input: `data: {"candidates":[{"content":{"parts":[{"text":"Foo"}]}}]}`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "world"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Foo"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name:         "invalid JSON chunk in middle - ignored",
			bufferedBody: nil,
			input: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}

data: invalid-json

data: {"candidates":[{"content":{"parts":[{"text":"world"}]}}]}

`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "world"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name:         "empty input",
			bufferedBody: nil,
			input:        "",
			wantChunks:   nil,
			wantBuffered: nil,
		},
		{
			name:         "chunk without data prefix",
			bufferedBody: nil,
			input: `{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}

`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name:         "CRLF CRLF delimiter",
			bufferedBody: nil,
			input:        `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}` + "\r\n\r\n" + `data: {"candidates":[{"content":{"parts":[{"text":"World"}]}}]}`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Hello"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "World"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
		{
			name:         "CR CR delimiter",
			bufferedBody: nil,
			input:        `data: {"candidates":[{"content":{"parts":[{"text":"Test"}]}}]}` + "\r\r" + `data: {"candidates":[{"content":{"parts":[{"text":"Message"}]}}]}`,
			wantChunks: []genai.GenerateContentResponse{
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Test"},
								},
							},
						},
					},
				},
				{
					Candidates: []*genai.Candidate{
						{
							Content: &genai.Content{
								Parts: []*genai.Part{
									{Text: "Message"},
								},
							},
						},
					},
				},
			},
			wantBuffered: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{
				bufferedBody: tc.bufferedBody,
			}

			chunks, err := translator.parseGCPStreamingChunks(strings.NewReader(tc.input))

			require.NoError(t, err)

			// Compare chunks using cmp with options to handle pointer fields.
			if diff := cmp.Diff(tc.wantChunks, chunks,
				cmpopts.IgnoreUnexported(genai.GenerateContentResponse{}),
				cmpopts.IgnoreUnexported(genai.Candidate{}),
				cmpopts.IgnoreUnexported(genai.Content{}),
				cmpopts.IgnoreUnexported(genai.Part{}),
				cmpopts.IgnoreUnexported(genai.UsageMetadata{}),
			); diff != "" {
				t.Errorf("chunks mismatch (-want +got):\n%s", diff)
			}

			// Check buffered body.
			if diff := cmp.Diff(tc.wantBuffered, translator.bufferedBody); diff != "" {
				t.Errorf("buffered body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingResponseBody_IncompleteFirstChunkThenComplete
// tests that incomplete first chunks return []byte{} (not nil) and subsequent chunks are properly translated.
// Simulates large thoughtSignature being split across TCP packets in Gemini reasoning models.
func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_StreamingResponseBody_IncompleteFirstChunkThenComplete(t *testing.T) {
	translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{
		stream:       true,
		requestModel: "gemini-2.5-pro",
	}

	// Large signature (~832 chars) simulating real thoughtSignature from reasoning models.
	largeSignature := strings.Repeat("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/", 13)

	// First chunk: incomplete JSON cut mid-signature (simulates TCP packet boundary).
	firstChunkData := `data: {"candidates":[{"content":{"parts":[{"text":"Let me analyze this problem.","thought":true},{"text":"The answer is 42.","thoughtSignature":"` + largeSignature[:400]
	firstChunk := []byte(firstChunkData)

	_, newBody1, _, _, err := translator.ResponseBody(nil, bytes.NewReader(firstChunk), false, nil)

	require.NoError(t, err)
	// newBody1 must be []byte{}, not nil. Nil causes Envoy to pass through original Gemini format.
	require.NotNil(t, newBody1, "newBody must not be nil")
	require.Empty(t, newBody1, "newBody should be empty when data is buffered")

	// Second chunk: rest of signature + JSON closing + usage metadata.
	secondChunkData := largeSignature[400:] + `"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30}}

`
	secondChunk := []byte(secondChunkData)

	_, newBody2, tokenUsage, _, err := translator.ResponseBody(nil, bytes.NewReader(secondChunk), false, nil)

	require.NoError(t, err)
	require.NotNil(t, newBody2)
	require.NotEmpty(t, newBody2, "should contain translated OpenAI format")

	bodyStr := string(newBody2)
	require.Contains(t, bodyStr, "data: {", "should be SSE format")
	require.Contains(t, bodyStr, `"object":"chat.completion.chunk"`, "should be OpenAI format")
	require.Contains(t, bodyStr, "reasoning_content", "thought should translate to reasoning_content")
	require.Contains(t, bodyStr, "The answer is 42", "response text should be present")
	require.Contains(t, bodyStr, "signature", "thoughtSignature should translate to signature")

	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(20), outputTokens)
}

func TestOpenAIToGCPVertexAITranslatorV1ChatCompletion_ResponseError(t *testing.T) {
	tests := []struct {
		name           string
		headers        map[string]string
		body           string
		expectedErrMsg string
		wantError      openai.Error
		description    string
	}{
		{
			name: "JSON error response with complete GCP error structure",
			headers: map[string]string{
				statusHeaderName: "400",
			},
			body: `{
  "error": {
    "code": 400,
    "message": "Invalid JSON payload received. Unknown name \"fake\": Cannot find field.",
    "status": "INVALID_ARGUMENT",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.BadRequest",
        "fieldViolations": [
          {
            "description": "Invalid JSON payload received. Unknown name \"fake\": Cannot find field."
          }
        ]
      }
    ]
  }
}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type: "INVALID_ARGUMENT",
					Message: `Error: Invalid JSON payload received. Unknown name "fake": Cannot find field.
Details: [
      {
        "@type": "type.googleapis.com/google.rpc.BadRequest",
        "fieldViolations": [
          {
            "description": "Invalid JSON payload received. Unknown name \"fake\": Cannot find field."
          }
        ]
      }
    ]`,
					Code: ptr.To("400"),
				},
			},
		},
		{
			name: "Plain text error response",
			headers: map[string]string{
				statusHeaderName: "503",
			},
			body: "Service temporarily unavailable",
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    gcpVertexAIBackendError,
					Message: "Service temporarily unavailable",
					Code:    ptr.To("503"),
				},
			},
		},
		{
			name: "Invalid JSON in error response",
			headers: map[string]string{
				statusHeaderName: "400",
			},
			body: `{"error": invalid json}`,
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    gcpVertexAIBackendError,
					Message: `{"error": invalid json}`,
					Code:    ptr.To("400"),
				},
			},
		},
		{
			name: "Empty body handling",
			headers: map[string]string{
				statusHeaderName: "500",
			},
			body:        "", // Empty body to simulate no content.
			description: "Should handle empty body gracefully.",
			wantError: openai.Error{
				Type: "error",
				Error: openai.ErrorType{
					Type:    gcpVertexAIBackendError,
					Message: "",
					Code:    ptr.To("500"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewChatCompletionOpenAIToGCPVertexAITranslator("gemini-2.0-flash-001").(*openAIToGCPVertexAITranslatorV1ChatCompletion)

			body := strings.NewReader(tt.body)

			headerMutation, bodyBytes, err := translator.ResponseError(tt.headers, body)

			if tt.expectedErrMsg != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErrMsg)
				return
			}

			require.NoError(t, err)
			if tt.description != "" {
				require.NoError(t, err, tt.description)
			}
			require.NotNil(t, bodyBytes)
			require.NotNil(t, headerMutation)

			// Verify that the body mutation contains a valid OpenAI error response.
			var openaiError openai.Error
			err = json.Unmarshal(bodyBytes, &openaiError)
			require.NoError(t, err)

			if diff := cmp.Diff(tt.wantError, openaiError); diff != "" {
				t.Errorf("OpenAI error mismatch (-want +got):\n%s", diff)
			}

			// Verify header mutation contains content-length header.
			foundContentLength := slices.ContainsFunc(
				headerMutation,
				func(header internalapi.Header) bool { return header.Key() == contentLengthHeaderName },
			)
			assert.True(t, foundContentLength, "content-length header should be set")
		})
	}
}

func bodyMutTransformer(_ *testing.T) cmp.Option {
	return cmp.Transformer("BodyMutationsToBodyBytes", func(raw []byte) map[string]any {
		if raw == nil {
			return nil
		}

		var bdy map[string]any
		if err := json.Unmarshal(raw, &bdy); err != nil {
			// The response body may not be valid JSON for streaming requests.
			return map[string]any{
				"BodyMutation": string(raw),
			}
		}
		return bdy
	})
}

// TestResponseModel_GCPVertexAI tests that GCP Vertex AI returns the request model (no response field)
func TestResponseModel_GCPVertexAI(t *testing.T) {
	modelName := "gemini-1.5-pro-002"
	translator := NewChatCompletionOpenAIToGCPVertexAITranslator(modelName)

	// Initialize translator with the model
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
	}
	reqBody, _ := json.Marshal(req)
	_, _, err := translator.RequestBody(reqBody, req, false)
	require.NoError(t, err)

	// Vertex AI response doesn't have model field
	vertexResponse := `{
		"candidates": [{
			"content": {
				"parts": [{"text": "Hello"}],
				"role": "model"
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5,
			"totalTokenCount": 15
		}
	}`

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, bytes.NewReader([]byte(vertexResponse)), true, nil)
	require.NoError(t, err)
	require.Equal(t, modelName, responseModel) // Returns the request model
	inputTokens, ok := tokenUsage.InputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(10), inputTokens)
	outputTokens, ok := tokenUsage.OutputTokens()
	require.True(t, ok)
	require.Equal(t, uint32(5), outputTokens)
}

func TestGCPVertexAIRedactBody(t *testing.T) {
	t.Run("redacts message content", func(t *testing.T) {
		translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{}

		originalContent := "This is sensitive AI-generated content from GCP"
		resp := &openai.ChatCompletionResponse{
			ID:     "chatcmpl-gcp-123",
			Model:  "gemini-1.5-pro",
			Object: "chat.completion",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    "assistant",
						Content: &originalContent,
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}

		redacted := translator.RedactBody(resp)

		// Verify original is not modified
		require.Equal(t, "This is sensitive AI-generated content from GCP", *resp.Choices[0].Message.Content)

		// Verify redacted copy has redacted content
		require.NotNil(t, redacted.Choices[0].Message.Content)
		require.Contains(t, *redacted.Choices[0].Message.Content, "[REDACTED LENGTH=")
		require.Contains(t, *redacted.Choices[0].Message.Content, "HASH=")
		require.NotContains(t, *redacted.Choices[0].Message.Content, "sensitive")

		// Verify non-sensitive fields are preserved
		require.Equal(t, "chatcmpl-gcp-123", redacted.ID)
		require.Equal(t, "gemini-1.5-pro", redacted.Model)
		require.Equal(t, 10, redacted.Usage.PromptTokens)
		require.Equal(t, 5, redacted.Usage.CompletionTokens)
	})

	t.Run("redacts tool calls", func(t *testing.T) {
		translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{}

		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-gcp-456",
			Model: "gemini-1.5-pro",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role: "assistant",
						ToolCalls: []openai.ChatCompletionMessageToolCallParam{
							{
								ID:   ptr.To("call_gcp_123"),
								Type: "function",
								Function: openai.ChatCompletionMessageToolCallFunctionParam{
									Name:      "search_web",
									Arguments: `{"query": "GCP Vertex AI pricing"}`,
								},
							},
						},
					},
				},
			},
		}

		redacted := translator.RedactBody(resp)

		// Verify original is not modified
		require.Equal(t, "search_web", resp.Choices[0].Message.ToolCalls[0].Function.Name)
		require.Contains(t, resp.Choices[0].Message.ToolCalls[0].Function.Arguments, "GCP Vertex AI")

		// Verify redacted copy: name kept, arguments redacted
		require.Len(t, redacted.Choices[0].Message.ToolCalls, 1)
		require.Equal(t, "search_web", redacted.Choices[0].Message.ToolCalls[0].Function.Name)
		require.Contains(t, redacted.Choices[0].Message.ToolCalls[0].Function.Arguments, "[REDACTED")
		require.NotContains(t, redacted.Choices[0].Message.ToolCalls[0].Function.Arguments, "GCP Vertex AI")
	})

	t.Run("redacts reasoning content", func(t *testing.T) {
		translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{}

		originalContent := "Main response"
		reasoningContent := "This is extended thinking content from Gemini"
		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-gcp-789",
			Model: "gemini-1.5-pro",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    "assistant",
						Content: &originalContent,
						ReasoningContent: &openai.ReasoningContentUnion{
							Value: reasoningContent,
						},
					},
				},
			},
		}

		redacted := translator.RedactBody(resp)

		// Verify original is not modified
		require.Equal(t, "This is extended thinking content from Gemini", resp.Choices[0].Message.ReasoningContent.Value)

		// Verify redacted copy has redacted reasoning content
		require.NotNil(t, redacted.Choices[0].Message.ReasoningContent)
		redactedReasoning, ok := redacted.Choices[0].Message.ReasoningContent.Value.(string)
		require.True(t, ok)
		require.Contains(t, redactedReasoning, "[REDACTED")
		require.NotContains(t, redactedReasoning, "extended thinking")
	})

	t.Run("handles nil response", func(t *testing.T) {
		translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{}

		redacted := translator.RedactBody(nil)

		require.Nil(t, redacted)
	})

	t.Run("does not modify original response", func(t *testing.T) {
		translator := &openAIToGCPVertexAITranslatorV1ChatCompletion{}

		originalContent := "Original GCP content"
		resp := &openai.ChatCompletionResponse{
			ID:    "chatcmpl-gcp-999",
			Model: "gemini-1.5-pro",
			Choices: []openai.ChatCompletionResponseChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionResponseChoiceMessage{
						Role:    "assistant",
						Content: &originalContent,
					},
				},
			},
		}

		// Create a copy of the original for comparison
		originalContentCopy := *resp.Choices[0].Message.Content

		// Redact the response
		_ = translator.RedactBody(resp)

		// Verify original is completely unchanged
		require.Equal(t, originalContentCopy, *resp.Choices[0].Message.Content)
		require.NotContains(t, *resp.Choices[0].Message.Content, "[REDACTED")
	})
}
