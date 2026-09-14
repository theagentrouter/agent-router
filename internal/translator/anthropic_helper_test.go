// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

// mockErrorReader is a helper for testing io.Reader failures.
type mockErrorReader struct{}

func (r *mockErrorReader) Read(_ []byte) (n int, err error) {
	return 0, fmt.Errorf("mock reader error")
}

// New test function for helper coverage.
func TestHelperFunctions(t *testing.T) {
	t.Run("anthropicToOpenAIFinishReason invalid reason", func(t *testing.T) {
		_, err := anthropicToOpenAIFinishReason("unknown_reason")
		require.Error(t, err)
		require.Contains(t, err.Error(), "received invalid stop reason")
	})

	t.Run("anthropicRoleToOpenAIRole invalid role", func(t *testing.T) {
		_, err := anthropicRoleToOpenAIRole("unknown_role")
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid anthropic role")
	})
}

func TestTranslateOpenAItoAnthropicTools(t *testing.T) {
	anthropicTestTool := []anthropic.ToolUnionParam{
		{OfTool: &anthropic.ToolParam{Name: "get_weather", Description: anthropic.String("")}},
	}
	openaiTestTool := []openai.Tool{
		{Type: "function", Function: &openai.FunctionDefinition{Name: "get_weather"}},
	}
	tests := []struct {
		name               string
		openAIReq          *openai.ChatCompletionRequest
		expectedTools      []anthropic.ToolUnionParam
		expectedToolChoice anthropic.ToolChoiceUnionParam
		expectErr          bool
	}{
		{
			name: "auto tool choice",
			openAIReq: &openai.ChatCompletionRequest{
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
				Tools:      openaiTestTool,
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfAuto: &anthropic.ToolChoiceAutoParam{
					DisableParallelToolUse: anthropic.Bool(false),
				},
			},
		},
		{
			name: "any tool choice",
			openAIReq: &openai.ChatCompletionRequest{
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "any"},
				Tools:      openaiTestTool,
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfAny: &anthropic.ToolChoiceAnyParam{},
			},
		},
		{
			name: "specific tool choice by name",
			openAIReq: &openai.ChatCompletionRequest{
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: openai.ChatCompletionNamedToolChoice{Type: "function", Function: openai.ChatCompletionNamedToolChoiceFunction{Name: "my_func"}}},
				Tools:      openaiTestTool,
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfTool: &anthropic.ToolChoiceToolParam{Type: "tool", Name: "my_func"},
			},
		},
		{
			name: "tool definition",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
							Description: "Get the weather",
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
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_weather",
						Description: anthropic.String("Get the weather"),
						InputSchema: anthropic.ToolInputSchemaParam{
							Type: "object",
							Properties: map[string]any{
								"location": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
		{
			name: "eager_input_streaming true is forwarded",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:                "get_weather",
							EagerInputStreaming: ptr.To(true),
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:                "get_weather",
						Description:         anthropic.String(""),
						EagerInputStreaming: anthropic.Bool(true),
					},
				},
			},
		},
		{
			// An explicit false must survive as false rather than collapse into unset, since
			// Anthropic reads it as "keep buffering" even when the legacy beta header is on.
			name: "eager_input_streaming false is forwarded, not dropped",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:                "get_weather",
							EagerInputStreaming: ptr.To(false),
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:                "get_weather",
						Description:         anthropic.String(""),
						EagerInputStreaming: anthropic.Bool(false),
					},
				},
			},
		},
		{
			name: "eager_input_streaming omitted stays unset",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: openaiTestTool,
			},
			expectedTools: anthropicTestTool,
		},
		{
			name: "tool_definition_with_required_field",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
							Description: "Get the weather with a required location",
							Parameters: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"location": map[string]any{"type": "string"},
									"unit":     map[string]any{"type": "string"},
								},
								"required": []any{"location"},
							},
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_weather",
						Description: anthropic.String("Get the weather with a required location"),
						InputSchema: anthropic.ToolInputSchemaParam{
							Type: "object",
							Properties: map[string]any{
								"location": map[string]any{"type": "string"},
								"unit":     map[string]any{"type": "string"},
							},
							Required: []string{"location"},
						},
					},
				},
			},
		},
		{
			name: "tool definition with no parameters",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_time",
							Description: "Get the current time",
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_time",
						Description: anthropic.String("Get the current time"),
					},
				},
			},
		},
		{
			name: "disable parallel tool calls",
			openAIReq: &openai.ChatCompletionRequest{
				ToolChoice:        &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
				Tools:             openaiTestTool,
				ParallelToolCalls: ptr.To(false),
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfAuto: &anthropic.ToolChoiceAutoParam{
					DisableParallelToolUse: anthropic.Bool(true),
				},
			},
		},
		{
			name: "explicitly enable parallel tool calls",
			openAIReq: &openai.ChatCompletionRequest{
				Tools:             openaiTestTool,
				ToolChoice:        &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
				ParallelToolCalls: ptr.To(true),
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfAuto: &anthropic.ToolChoiceAutoParam{DisableParallelToolUse: anthropic.Bool(false)},
			},
		},
		{
			name: "default disable parallel tool calls to false (nil)",
			openAIReq: &openai.ChatCompletionRequest{
				Tools:      openaiTestTool,
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "auto"},
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfAuto: &anthropic.ToolChoiceAutoParam{DisableParallelToolUse: anthropic.Bool(false)},
			},
		},
		{
			name: "none tool choice",
			openAIReq: &openai.ChatCompletionRequest{
				Tools:      openaiTestTool,
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "none"},
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfNone: &anthropic.ToolChoiceNoneParam{},
			},
		},
		{
			name: "function tool choice",
			openAIReq: &openai.ChatCompletionRequest{
				Tools:      openaiTestTool,
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "function"},
			},
			expectedTools: anthropicTestTool,
			expectedToolChoice: anthropic.ToolChoiceUnionParam{
				OfTool: &anthropic.ToolChoiceToolParam{Name: "function"},
			},
		},
		{
			name: "invalid tool choice string",
			openAIReq: &openai.ChatCompletionRequest{
				Tools:      openaiTestTool,
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: "invalid_choice"},
			},
			expectErr: true,
		},
		{
			name: "rejects function tool with nil function definition",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type:     "function",
						Function: nil,
					},
				},
			},
			expectErr: true,
		},
		{
			name: "rejects non-function tools",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "retrieval",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "tool definition without type field",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
							Description: "Get the weather without type",
							Parameters: map[string]any{
								"properties": map[string]any{
									"location": map[string]any{"type": "string"},
								},
								"required": []any{"location"},
							},
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_weather",
						Description: anthropic.String("Get the weather without type"),
						InputSchema: anthropic.ToolInputSchemaParam{
							Type: "",
							Properties: map[string]any{
								"location": map[string]any{"type": "string"},
							},
							Required: []string{"location"},
						},
					},
				},
			},
		},
		{
			name: "tool definition without properties field",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
							Description: "Get the weather without properties",
							Parameters: map[string]any{
								"type":     "object",
								"required": []any{"location"},
							},
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_weather",
						Description: anthropic.String("Get the weather without properties"),
						InputSchema: anthropic.ToolInputSchemaParam{
							Type:     "object",
							Required: []string{"location"},
						},
					},
				},
			},
		},
		{
			name: "unsupported tool_choice type",
			openAIReq: &openai.ChatCompletionRequest{
				Tools:      openaiTestTool,
				ToolChoice: &openai.ChatCompletionToolChoiceUnion{Value: 123}, // Use an integer to trigger the default case.
			},
			expectErr: true,
		},
		{
			name: "nested schema in tool's defintions",
			openAIReq: &openai.ChatCompletionRequest{
				Tools: []openai.Tool{
					{
						Type: "function",
						Function: &openai.FunctionDefinition{
							Name:        "get_weather",
							Description: "Get the weather without type",
							Parameters: map[string]any{
								"properties": map[string]any{
									"location": map[string]any{"type": "string"},
								},
								"required": []any{"location"},
								"$defs": map[string]any{
									"ReferencePassage": map[string]any{
										"properties": map[string]any{
											"url": map[string]any{
												"title": "Url",
												"type":  "string",
											},
											"passage_id": map[string]any{
												"title": "Passage Id",
												"type":  "string",
											},
										},
										"required": []string{"url", "passage_id"},
										"title":    "ReferencePassage",
										"type":     "object",
									},
								},
							},
						},
					},
				},
			},
			expectedTools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_weather",
						Description: anthropic.String("Get the weather without type"),
						InputSchema: anthropic.ToolInputSchemaParam{
							Type: "",
							Properties: map[string]any{
								"location": map[string]any{"type": "string"},
							},
							Required: []string{"location"},
							ExtraFields: map[string]any{
								"$defs": map[string]any{
									"ReferencePassage": map[string]any{
										"properties": map[string]any{
											"url": map[string]any{
												"title": "Url",
												"type":  "string",
											},
											"passage_id": map[string]any{
												"title": "Passage Id",
												"type":  "string",
											},
										},
										"required": []string{"url", "passage_id"},
										"title":    "ReferencePassage",
										"type":     "object",
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
			tools, toolChoice, err := translateOpenAItoAnthropicTools(tt.openAIReq.Tools, tt.openAIReq.ToolChoice, tt.openAIReq.ParallelToolCalls)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.openAIReq.ToolChoice != nil {
					require.NotNil(t, toolChoice)
					require.Equal(t, *tt.expectedToolChoice.GetType(), *toolChoice.GetType())
					if tt.expectedToolChoice.GetName() != nil {
						require.Equal(t, *tt.expectedToolChoice.GetName(), *toolChoice.GetName())
					}
					if tt.expectedToolChoice.OfTool != nil {
						require.Equal(t, tt.expectedToolChoice.OfTool.Name, toolChoice.OfTool.Name)
					}
					if tt.expectedToolChoice.OfAuto != nil {
						require.Equal(t, tt.expectedToolChoice.OfAuto.DisableParallelToolUse, toolChoice.OfAuto.DisableParallelToolUse)
					}
				}
				if tt.openAIReq.Tools != nil {
					require.NotNil(t, tools)
					require.Len(t, tools, len(tt.expectedTools))
					require.Equal(t, tt.expectedTools[0].GetName(), tools[0].GetName())
					require.Equal(t, tt.expectedTools[0].GetType(), tools[0].GetType())
					require.Equal(t, tt.expectedTools[0].GetDescription(), tools[0].GetDescription())
					if tt.expectedTools[0].GetInputSchema().Properties != nil {
						require.Equal(t, tt.expectedTools[0].GetInputSchema().Properties, tools[0].GetInputSchema().Properties)
					}
					if tt.expectedTools[0].GetInputSchema().ExtraFields != nil {
						require.Equal(t, tt.expectedTools[0].GetInputSchema().ExtraFields, tools[0].GetInputSchema().ExtraFields)
					}
				}
			}
		})
	}
}

// TestFinishReasonTranslation covers specific cases for the anthropicToOpenAIFinishReason function.
func TestFinishReasonTranslation(t *testing.T) {
	tests := []struct {
		name                 string
		input                anthropic.StopReason
		expectedFinishReason openai.ChatCompletionChoicesFinishReason
		expectErr            bool
	}{
		{
			name:                 "max tokens stop reason",
			input:                anthropic.StopReasonMaxTokens,
			expectedFinishReason: openai.ChatCompletionChoicesFinishReasonLength,
		},
		{
			name:                 "refusal stop reason",
			input:                anthropic.StopReasonRefusal,
			expectedFinishReason: openai.ChatCompletionChoicesFinishReasonContentFilter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, err := anthropicToOpenAIFinishReason(tt.input)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedFinishReason, reason)
			}
		})
	}
}

// TestContentTranslationCoverage adds specific coverage for the openAIToAnthropicContent helper.
func TestContentTranslationCoverage(t *testing.T) {
	tests := []struct {
		name            string
		inputContent    any
		expectedContent []anthropic.ContentBlockParamUnion
		expectErr       bool
	}{
		{
			name:         "nil content",
			inputContent: nil,
		},
		{
			name:         "empty string content",
			inputContent: "",
		},
		{
			name: "pdf data uri",
			inputContent: []openai.ChatCompletionContentPartUserUnionParam{
				{OfImageURL: &openai.ChatCompletionContentPartImageParam{ImageURL: openai.ChatCompletionContentPartImageImageURLParam{URL: "data:application/pdf;base64,dGVzdA=="}}},
			},
			expectedContent: []anthropic.ContentBlockParamUnion{
				{
					OfDocument: &anthropic.DocumentBlockParam{
						Source: anthropic.DocumentBlockParamSourceUnion{
							OfBase64: &anthropic.Base64PDFSourceParam{
								Type:      constant.ValueOf[constant.Base64](),
								MediaType: constant.ValueOf[constant.ApplicationPDF](),
								Data:      "dGVzdA==",
							},
						},
					},
				},
			},
		},
		{
			name: "pdf url",
			inputContent: []openai.ChatCompletionContentPartUserUnionParam{
				{OfImageURL: &openai.ChatCompletionContentPartImageParam{ImageURL: openai.ChatCompletionContentPartImageImageURLParam{URL: "https://example.com/doc.pdf"}}},
			},
			expectedContent: []anthropic.ContentBlockParamUnion{
				{
					OfDocument: &anthropic.DocumentBlockParam{
						Source: anthropic.DocumentBlockParamSourceUnion{
							OfURL: &anthropic.URLPDFSourceParam{
								Type: constant.ValueOf[constant.URL](),
								URL:  "https://example.com/doc.pdf",
							},
						},
					},
				},
			},
		},
		{
			name: "image url",
			inputContent: []openai.ChatCompletionContentPartUserUnionParam{
				{OfImageURL: &openai.ChatCompletionContentPartImageParam{ImageURL: openai.ChatCompletionContentPartImageImageURLParam{URL: "https://example.com/image.png"}}},
			},
			expectedContent: []anthropic.ContentBlockParamUnion{
				{
					OfImage: &anthropic.ImageBlockParam{
						Source: anthropic.ImageBlockParamSourceUnion{
							OfURL: &anthropic.URLImageSourceParam{
								Type: constant.ValueOf[constant.URL](),
								URL:  "https://example.com/image.png",
							},
						},
					},
				},
			},
		},
		{
			name:         "audio content error",
			inputContent: []openai.ChatCompletionContentPartUserUnionParam{{OfInputAudio: &openai.ChatCompletionContentPartInputAudioParam{}}},
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := openAIToAnthropicContent(tt.inputContent)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Use direct assertions instead of cmp.Diff to avoid panics on unexported fields.
			require.Len(t, content, len(tt.expectedContent), "Number of content blocks should match")

			// Use direct assertions instead of cmp.Diff to avoid panics on unexported fields.
			require.Len(t, content, len(tt.expectedContent), "Number of content blocks should match")
			for i, expectedBlock := range tt.expectedContent {
				actualBlock := content[i]
				require.Equal(t, expectedBlock.GetType(), actualBlock.GetType(), "Content block types should match")
				if expectedBlock.OfDocument != nil {
					require.NotNil(t, actualBlock.OfDocument, "Expected a document block, but got nil")
					require.NotNil(t, actualBlock.OfDocument.Source, "Document source should not be nil")

					if expectedBlock.OfDocument.Source.OfBase64 != nil {
						require.NotNil(t, actualBlock.OfDocument.Source.OfBase64, "Expected a base64 source")
						require.Equal(t, expectedBlock.OfDocument.Source.OfBase64.Data, actualBlock.OfDocument.Source.OfBase64.Data)
					}
					if expectedBlock.OfDocument.Source.OfURL != nil {
						require.NotNil(t, actualBlock.OfDocument.Source.OfURL, "Expected a URL source")
						require.Equal(t, expectedBlock.OfDocument.Source.OfURL.URL, actualBlock.OfDocument.Source.OfURL.URL)
					}
				}
				if expectedBlock.OfImage != nil {
					require.NotNil(t, actualBlock.OfImage, "Expected an image block, but got nil")
					require.NotNil(t, actualBlock.OfImage.Source, "Image source should not be nil")

					if expectedBlock.OfImage.Source.OfURL != nil {
						require.NotNil(t, actualBlock.OfImage.Source.OfURL, "Expected a URL image source")
						require.Equal(t, expectedBlock.OfImage.Source.OfURL.URL, actualBlock.OfImage.Source.OfURL.URL)
					}
				}
			}

			for i, expectedBlock := range tt.expectedContent {
				actualBlock := content[i]
				if expectedBlock.OfDocument != nil {
					require.NotNil(t, actualBlock.OfDocument, "Expected a document block, but got nil")
					require.NotNil(t, actualBlock.OfDocument.Source, "Document source should not be nil")

					if expectedBlock.OfDocument.Source.OfBase64 != nil {
						require.NotNil(t, actualBlock.OfDocument.Source.OfBase64, "Expected a base64 source")
						require.Equal(t, expectedBlock.OfDocument.Source.OfBase64.Data, actualBlock.OfDocument.Source.OfBase64.Data)
					}
					if expectedBlock.OfDocument.Source.OfURL != nil {
						require.NotNil(t, actualBlock.OfDocument.Source.OfURL, "Expected a URL source")
						require.Equal(t, expectedBlock.OfDocument.Source.OfURL.URL, actualBlock.OfDocument.Source.OfURL.URL)
					}
				}
				if expectedBlock.OfImage != nil {
					require.NotNil(t, actualBlock.OfImage, "Expected an image block, but got nil")
					require.NotNil(t, actualBlock.OfImage.Source, "Image source should not be nil")

					if expectedBlock.OfImage.Source.OfURL != nil {
						require.NotNil(t, actualBlock.OfImage.Source.OfURL, "Expected a URL image source")
						require.Equal(t, expectedBlock.OfImage.Source.OfURL.URL, actualBlock.OfImage.Source.OfURL.URL)
					}
				}
			}
		})
	}
}

// TestSystemPromptExtractionCoverage adds specific coverage for the extractSystemPromptFromDeveloperMsg helper.
func TestSystemPromptExtractionCoverage(t *testing.T) {
	tests := []struct {
		name           string
		inputMsg       openai.ChatCompletionDeveloperMessageParam
		expectedPrompt string
	}{
		{
			name: "developer message with content parts",
			inputMsg: openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
					{Type: "text", Text: "part 1"},
					{Type: "text", Text: " part 2"},
				}},
			},
			expectedPrompt: "part 1 part 2",
		},
		{
			name:           "developer message with nil content",
			inputMsg:       openai.ChatCompletionDeveloperMessageParam{Content: openai.ContentUnion{Value: nil}},
			expectedPrompt: "",
		},
		{
			name: "developer message with string content",
			inputMsg: openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ContentUnion{Value: "simple string"},
			},
			expectedPrompt: "simple string",
		},
		{
			name: "developer message with text parts array",
			inputMsg: openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
					{Type: "text", Text: "text part"},
				}},
			},
			expectedPrompt: "text part",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, _ := extractSystemPromptFromDeveloperMsg(tt.inputMsg)
			require.Equal(t, tt.expectedPrompt, prompt)
		})
	}
}

func TestOutputConfigAvailable(t *testing.T) {
	tests := []struct {
		name      string
		apiSchema filterapi.APISchemaName
		model     string
		expected  bool
	}{
		// Models supported on both AWS Bedrock (InvokeModel) and GCP Vertex AI.
		{
			name:      "claude-sonnet-4-5 supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "claude-sonnet-4-5-20250514",
			expected:  true,
		},
		{
			name:      "claude-sonnet-4-5 supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "claude-sonnet-4-5@20250514",
			expected:  true,
		},
		{
			name:      "claude-opus-4-6 supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "claude-opus-4-6-20250514",
			expected:  true,
		},
		{
			name:      "claude-sonnet-4-6 supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "claude-sonnet-4-6",
			expected:  true,
		},
		// Newer models: supported on GCP Vertex AI, not on AWS Bedrock (InvokeModel).
		{
			name:      "claude-opus-4-7 supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "claude-opus-4-7",
			expected:  true,
		},
		{
			name:      "claude-opus-4-7 not supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "claude-opus-4-7",
			expected:  false,
		},
		{
			name:      "claude-opus-4-8 supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "claude-opus-4-8",
			expected:  true,
		},
		{
			name:      "claude-opus-4-8 not supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "claude-opus-4-8",
			expected:  false,
		},
		{
			name:      "claude-fable-5 supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "claude-fable-5",
			expected:  true,
		},
		{
			name:      "claude-fable-5 not supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "claude-fable-5",
			expected:  false,
		},
		// Unsupported models on either backend.
		{
			name:      "claude-3-sonnet not supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "claude-3-sonnet",
			expected:  false,
		},
		{
			name:      "claude-3.5-sonnet not supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "claude-3.5-sonnet",
			expected:  false,
		},
		{
			name:      "gpt-4 not supported on GCP",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			model:     "gpt-4",
			expected:  false,
		},
		{
			name:      "empty model not supported on AWS",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			model:     "",
			expected:  false,
		},
		// Schemas not wired up to buildAnthropicParams fail closed rather than
		// falling back to AWS's model list.
		{
			name:      "unrecognized schema fails closed",
			apiSchema: filterapi.APISchemaAnthropic,
			model:     "claude-sonnet-4-5-20250514",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := outputConfigAvailable(tt.apiSchema, tt.model)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAnthropicStreamParserTokenUsage_NoDoubleCounting(t *testing.T) {
	// This test verifies that cache tokens are not double-counted when
	// both message_start and message_delta report cache token usage.
	// The Anthropic API reports cumulative totals in message_delta, not
	// incremental deltas, so we must use Set (override) not Add (accumulate)
	// for cache tokens from message_delta.
	tests := []struct {
		name                       string
		messageStartInputTokens    int64
		messageStartCacheRead      int64
		messageStartCacheCreation  int64
		messageDeltaInputTokens    *int64
		messageDeltaCacheRead      *int64
		messageDeltaCacheCreation  *int64
		messageDeltaOutputTokens   int64
		expectedInputTokens        uint32
		expectedCachedTokens       uint32
		expectedCacheCreationToken uint32
		expectedOutputTokens       uint32
	}{
		{
			name:                       "cache tokens in both message_start and message_delta should not double count",
			messageStartInputTokens:    9,
			messageStartCacheRead:      1,
			messageStartCacheCreation:  0,
			messageDeltaInputTokens:    ptr.To[int64](9),
			messageDeltaCacheRead:      ptr.To[int64](1),
			messageDeltaCacheCreation:  ptr.To[int64](0),
			messageDeltaOutputTokens:   16,
			expectedInputTokens:        10, // 9 base + 1 cache_read, NOT 11 (9+1+1 double counted)
			expectedCachedTokens:       1,  // NOT 2 (1+1 double counted)
			expectedCacheCreationToken: 0,
			expectedOutputTokens:       16,
		},
		{
			name:                       "cache creation tokens in both message_start and message_delta should not double count",
			messageStartInputTokens:    5,
			messageStartCacheRead:      0,
			messageStartCacheCreation:  3,
			messageDeltaInputTokens:    ptr.To[int64](5),
			messageDeltaCacheRead:      ptr.To[int64](0),
			messageDeltaCacheCreation:  ptr.To[int64](3),
			messageDeltaOutputTokens:   10,
			expectedInputTokens:        8, // 5 base + 3 cache_creation, NOT 11
			expectedCachedTokens:       0,
			expectedCacheCreationToken: 3, // NOT 6
			expectedOutputTokens:       10,
		},
		{
			name:                       "both cache_read and cache_creation in both events should not double count",
			messageStartInputTokens:    9,
			messageStartCacheRead:      2,
			messageStartCacheCreation:  3,
			messageDeltaInputTokens:    ptr.To[int64](9),
			messageDeltaCacheRead:      ptr.To[int64](2),
			messageDeltaCacheCreation:  ptr.To[int64](3),
			messageDeltaOutputTokens:   20,
			expectedInputTokens:        14, // 9 + 2 + 3, NOT 19 (9+2+3+2+3)
			expectedCachedTokens:       2,  // NOT 4
			expectedCacheCreationToken: 3,  // NOT 6
			expectedOutputTokens:       20,
		},
		{
			name:                       "no cache tokens - baseline correctness",
			messageStartInputTokens:    9,
			messageStartCacheRead:      0,
			messageStartCacheCreation:  0,
			messageDeltaOutputTokens:   16,
			expectedInputTokens:        9,
			expectedCachedTokens:       0,
			expectedCacheCreationToken: 0,
			expectedOutputTokens:       16,
		},
		{
			name:                       "cache only in message_start, not in message_delta",
			messageStartInputTokens:    9,
			messageStartCacheRead:      5,
			messageStartCacheCreation:  2,
			messageDeltaOutputTokens:   16,
			expectedInputTokens:        16, // 9 + 5 + 2
			expectedCachedTokens:       5,
			expectedCacheCreationToken: 2,
			expectedOutputTokens:       16,
		},
		{
			name:                       "cache tokens only in message_delta are applied",
			messageStartInputTokens:    9,
			messageStartCacheRead:      0,
			messageStartCacheCreation:  0,
			messageDeltaInputTokens:    ptr.To[int64](9),
			messageDeltaCacheRead:      ptr.To[int64](5),
			messageDeltaCacheCreation:  ptr.To[int64](2),
			messageDeltaOutputTokens:   16,
			expectedInputTokens:        16, // 9 + 5 + 2 from message_delta
			expectedCachedTokens:       5,
			expectedCacheCreationToken: 2,
			expectedOutputTokens:       16,
		},
		{
			name:                       "corrected cache tokens in message_delta override message_start",
			messageStartInputTokens:    9,
			messageStartCacheRead:      5,
			messageStartCacheCreation:  2,
			messageDeltaInputTokens:    ptr.To[int64](9),
			messageDeltaCacheRead:      ptr.To[int64](1),
			messageDeltaCacheCreation:  ptr.To[int64](0),
			messageDeltaOutputTokens:   16,
			expectedInputTokens:        10, // corrected 9 + 1 + 0, NOT stale 9 + 5 + 2
			expectedCachedTokens:       1,
			expectedCacheCreationToken: 0,
			expectedOutputTokens:       16,
		},
		{
			name:                       "message_delta with only cache_read, no input_tokens field",
			messageStartInputTokens:    10,
			messageStartCacheRead:      0,
			messageStartCacheCreation:  0,
			messageDeltaInputTokens:    nil,              // not present in message_delta
			messageDeltaCacheRead:      ptr.To[int64](3), // only cache_read in delta
			messageDeltaCacheCreation:  nil,
			messageDeltaOutputTokens:   20,
			expectedInputTokens:        13, // 10 base + 3 cache_read
			expectedCachedTokens:       3,
			expectedCacheCreationToken: 0,
			expectedOutputTokens:       20,
		},
		{
			name:                       "message_delta with only cache_creation, no input_tokens field",
			messageStartInputTokens:    8,
			messageStartCacheRead:      0,
			messageStartCacheCreation:  0,
			messageDeltaInputTokens:    nil, // not present in message_delta
			messageDeltaCacheRead:      nil,
			messageDeltaCacheCreation:  ptr.To[int64](4), // only cache_creation in delta
			messageDeltaOutputTokens:   15,
			expectedInputTokens:        12, // 8 base + 4 cache_creation
			expectedCachedTokens:       0,
			expectedCacheCreationToken: 4,
			expectedOutputTokens:       15,
		},
		{
			name:                       "message_delta with both cache fields but no input_tokens field",
			messageStartInputTokens:    7,
			messageStartCacheRead:      0,
			messageStartCacheCreation:  0,
			messageDeltaInputTokens:    nil, // not present in message_delta
			messageDeltaCacheRead:      ptr.To[int64](2),
			messageDeltaCacheCreation:  ptr.To[int64](3),
			messageDeltaOutputTokens:   12,
			expectedInputTokens:        12, // 7 base + 2 cache_read + 3 cache_creation
			expectedCachedTokens:       2,
			expectedCacheCreationToken: 3,
			expectedOutputTokens:       12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := newAnthropicStreamParser("test-model")

			messageDeltaUsageFields := []string{fmt.Sprintf(`"output_tokens":%d`, tt.messageDeltaOutputTokens)}
			if tt.messageDeltaInputTokens != nil {
				messageDeltaUsageFields = append(messageDeltaUsageFields, fmt.Sprintf(`"input_tokens":%d`, *tt.messageDeltaInputTokens))
			}
			if tt.messageDeltaCacheRead != nil {
				messageDeltaUsageFields = append(messageDeltaUsageFields, fmt.Sprintf(`"cache_read_input_tokens":%d`, *tt.messageDeltaCacheRead))
			}
			if tt.messageDeltaCacheCreation != nil {
				messageDeltaUsageFields = append(messageDeltaUsageFields, fmt.Sprintf(`"cache_creation_input_tokens":%d`, *tt.messageDeltaCacheCreation))
			}

			// Build the SSE stream with message_start and message_delta events.
			sseStream := fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{%s}}

event: message_stop
data: {"type":"message_stop"}
`,
				tt.messageStartInputTokens,
				tt.messageStartCacheRead,
				tt.messageStartCacheCreation,
				strings.Join(messageDeltaUsageFields, ","),
			)

			_, _, tokenUsage, _, err := parser.Process(strings.NewReader(sseStream), true, nil)
			require.NoError(t, err)

			inputTokens, inputSet := tokenUsage.InputTokens()
			cachedTokens, cachedSet := tokenUsage.CachedInputTokens()
			cacheCreationTokens, cacheCreationSet := tokenUsage.CacheCreationInputTokens()
			outputTokens, outputSet := tokenUsage.OutputTokens()

			assert.True(t, inputSet, "input tokens should be set")
			assert.Equal(t, tt.expectedInputTokens, inputTokens, "input tokens mismatch")
			assert.True(t, cachedSet, "cached tokens should be set")
			assert.Equal(t, tt.expectedCachedTokens, cachedTokens, "cached tokens mismatch")
			assert.True(t, cacheCreationSet, "cache creation tokens should be set")
			assert.Equal(t, tt.expectedCacheCreationToken, cacheCreationTokens, "cache creation tokens mismatch")
			assert.True(t, outputSet, "output tokens should be set")
			assert.Equal(t, tt.expectedOutputTokens, outputTokens, "output tokens mismatch")
		})
	}
}

func TestAnthropicStreamParserTokenUsage_MessageDeltaNoUsagePreservesPrior(t *testing.T) {
	// A later message_delta that omits usage must NOT clobber output/reasoning
	// tokens set by an earlier message_delta. The SDK's MessageDeltaUsage uses
	// non-pointer int64 fields that default to 0 when absent, so the parser must
	// use presence (Valid()) rather than a bare value check — otherwise the
	// zero default would overwrite a previously set non-zero count.
	parser := newAnthropicStreamParser("test-model")

	sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":16,"output_tokens_details":{"thinking_tokens":4}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}

event: message_stop
data: {"type":"message_stop"}
`

	_, _, tokenUsage, _, err := parser.Process(strings.NewReader(sseStream), true, nil)
	require.NoError(t, err)

	inputTokens, inputSet := tokenUsage.InputTokens()
	outputTokens, outputSet := tokenUsage.OutputTokens()
	reasoningTokens, reasoningSet := tokenUsage.ReasoningTokens()

	assert.True(t, inputSet, "input tokens should be set")
	assert.Equal(t, uint32(10), inputTokens, "input tokens should be from message_start")
	// The first message_delta sets output_tokens=16; the second message_delta has
	// no usage field and must not zero it out.
	assert.True(t, outputSet, "output tokens should be set from the first message_delta")
	assert.Equal(t, uint32(16), outputTokens, "later no-usage message_delta must not zero out output tokens")
	assert.True(t, reasoningSet, "reasoning tokens should be set from the first message_delta")
	assert.Equal(t, uint32(4), reasoningTokens, "later no-usage message_delta must not zero out reasoning tokens")
}

func TestAnthropicStreamParserTokenUsage_MessageDeltaCacheWhenInputAlreadyHasCache(t *testing.T) {
	// Test the case where message_start has cache tokens and input_tokens,
	// and message_delta provides cache tokens but NOT input_tokens.
	// The code must subtract the existing cache tokens from the base input_tokens
	// before adding the new cache tokens from message_delta.
	parser := newAnthropicStreamParser("test-model")

	sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"cache_read_input_tokens":7,"cache_creation_input_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`

	_, _, tokenUsage, _, err := parser.Process(strings.NewReader(sseStream), true, nil)
	require.NoError(t, err)

	inputTokens, inputSet := tokenUsage.InputTokens()
	cachedTokens, cachedSet := tokenUsage.CachedInputTokens()
	cacheCreationTokens, cacheCreationSet := tokenUsage.CacheCreationInputTokens()

	assert.True(t, inputSet, "input tokens should be set")
	// message_start: inputTokens is set to 20+5+3=28 (total)
	// message_delta without input_tokens field:
	//   - baseInputTokens = 28 (total) - 5 (old cache_read) - 3 (old cache_creation) = 20 (base)
	//   - Then add new cache: 20 + 7 (new cache_read) + 2 (new cache_creation) = 29
	assert.Equal(t, uint32(29), inputTokens, "input tokens should be 29 (20 base + 7 cache_read + 2 cache_creation)")
	assert.True(t, cachedSet, "cached tokens should be set")
	assert.Equal(t, uint32(7), cachedTokens, "cached tokens should be from message_delta (7)")
	assert.True(t, cacheCreationSet, "cache creation tokens should be set")
	assert.Equal(t, uint32(2), cacheCreationTokens, "cache creation tokens should be from message_delta (2)")
}

func TestAnthropicStreamParserTokenUsage_MessageDeltaInvalidJSON(t *testing.T) {
	// Test that message_delta with invalid JSON in usage fields returns an error
	parser := newAnthropicStreamParser("test-model")

	sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":"invalid"}}

event: message_stop
data: {"type":"message_stop"}
`

	_, _, _, _, err := parser.Process(strings.NewReader(sseStream), true, nil)
	// Should return error due to invalid JSON in usage field
	require.Error(t, err, "should return error for invalid JSON in message_delta usage")
	assert.Contains(t, err.Error(), "unmarshal message_delta usage fields", "error message should mention message_delta usage unmarshal")
}

func TestEffortAvailable(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{
			name:     "claude-opus-4-6-20250514 supported",
			model:    "claude-opus-4-6-20250514",
			expected: true,
		},
		{
			name:     "claude-sonnet-4-6-20250514 supported",
			model:    "claude-sonnet-4-6-20250514",
			expected: true,
		},
		{
			name:     "claude-opus-4-5-20250514 supported",
			model:    "claude-opus-4-5-20250514",
			expected: true,
		},
		{
			name:     "claude-opus-4-7 supported",
			model:    "claude-opus-4-7",
			expected: true,
		},
		{
			name:     "claude-mythos-preview supported",
			model:    "claude-mythos-preview",
			expected: true,
		},
		{
			name:     "claude-sonnet-4-5-20250514 not supported",
			model:    "claude-sonnet-4-5-20250514",
			expected: false,
		},
		{
			name:     "claude-haiku-4-5-20250514 not supported",
			model:    "claude-haiku-4-5-20250514",
			expected: false,
		},
		{
			name:     "claude-3-sonnet not supported",
			model:    "claude-3-sonnet",
			expected: false,
		},
		{
			name:     "empty model not supported",
			model:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := effortAvailable(tt.model)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildAnthropicParamsWithStructuredOutput(t *testing.T) {
	tests := []struct {
		name           string
		apiSchema      filterapi.APISchemaName
		request        *openai.ChatCompletionRequest
		expectSchema   bool
		expectedSchema map[string]any
		expectErr      bool
	}{
		{
			name:      "structured output with json_schema on supported model",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-sonnet-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "test_schema",
							Schema: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
						},
					},
				},
			},
			expectSchema: true,
			expectedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
					},
				},
			},
		},
		{
			name:      "structured output skipped on unsupported model",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-3-sonnet",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "test_schema",
							Schema: []byte(`{"type":"object"}`),
						},
					},
				},
			},
			expectSchema: false,
		},
		{
			name:      "no response format",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-sonnet-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectSchema: false,
		},
		{
			name:      "invalid json schema returns error",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-sonnet-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "invalid_schema",
							Schema: []byte(`{invalid json`),
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name:      "structured output enabled on GCP for supported model",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-sonnet-4-6",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "test_schema",
							Schema: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
						},
					},
				},
			},
			expectSchema: true,
			expectedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
					},
				},
			},
		},
		{
			// claude-opus-4-8 is supported on GCP Vertex AI but not on AWS Bedrock
			// (InvokeModel), so structured output must be enabled here.
			name:      "structured output enabled on GCP for GCP-only model",
			apiSchema: filterapi.APISchemaGCPAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-8",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "test_schema",
							Schema: []byte(`{"type":"object"}`),
						},
					},
				},
			},
			expectSchema: true,
			expectedSchema: map[string]any{
				"type": "object",
			},
		},
		{
			// Mirror of the case above: the same GCP-only model must NOT enable
			// structured output on the AWS Bedrock (InvokeModel) path.
			name:      "structured output skipped on AWS for GCP-only model",
			apiSchema: filterapi.APISchemaAWSAnthropic,
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-8",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
				ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
					OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
						Type: "json_schema",
						JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
							Name:   "test_schema",
							Schema: []byte(`{"type":"object"}`),
						},
					},
				},
			},
			expectSchema: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := buildAnthropicParams(tt.request, tt.apiSchema, "")

			if tt.expectErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, params)

			if tt.expectSchema {
				require.NotNil(t, params.OutputConfig.Format.Schema)
				require.Equal(t, constant.JSONSchema("json_schema"), params.OutputConfig.Format.Type)
				require.Equal(t, tt.expectedSchema, params.OutputConfig.Format.Schema)
			} else {
				require.Nil(t, params.OutputConfig.Format.Schema)
			}
		})
	}

	t.Run("structured output enabled via modelNameOverride when request model is custom", func(t *testing.T) {
		request := &openai.ChatCompletionRequest{
			Model:               "my-custom-model", // User-defined name that doesn't match any supported model identifier.
			MaxCompletionTokens: ptr.To(int64(1024)),
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "test"},
				}},
			},
			ResponseFormat: &openai.ChatCompletionResponseFormatUnion{
				OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
					Type: "json_schema",
					JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
						Name:   "test_schema",
						Schema: []byte(`{"type": "object", "properties": {"name": {"type": "string"}}}`),
					},
				},
			},
		}
		// The modelNameOverride contains a recognized model identifier.
		params, err := buildAnthropicParams(request, filterapi.APISchemaAWSAnthropic, "us.anthropic.claude-sonnet-4-5-20250514-v1:0")
		require.NoError(t, err)
		require.NotNil(t, params)
		require.NotNil(t, params.OutputConfig.Format.Schema)
		require.Equal(t, constant.JSONSchema("json_schema"), params.OutputConfig.Format.Type)
	})
}

func TestBuildAnthropicParamsWithReasoningEffort(t *testing.T) {
	tests := []struct {
		name           string
		request        *openai.ChatCompletionRequest
		expectedEffort anthropic.OutputConfigEffort
	}{
		{
			name: "reasoning_effort low on supported model",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortLow,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: anthropic.OutputConfigEffortLow,
		},
		{
			name: "reasoning_effort medium on supported model",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortMedium,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: anthropic.OutputConfigEffortMedium,
		},
		{
			name: "reasoning_effort high on supported model",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortHigh,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: anthropic.OutputConfigEffortHigh,
		},
		{
			name: "reasoning_effort xhigh on supported model",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-7",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortXhigh,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: anthropic.OutputConfigEffort(openai.ReasoningEffortXhigh),
		},
		{
			name: "reasoning_effort max on supported model",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-6",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortMax,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: anthropic.OutputConfigEffortMax,
		},
		{
			name: "reasoning_effort skipped on unsupported model claude-3-sonnet",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-3-sonnet",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortHigh,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: "",
		},
		{
			name: "reasoning_effort skipped on unsupported model claude-sonnet-4-5",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-sonnet-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				ReasoningEffort:     openai.ReasoningEffortHigh,
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: "",
		},
		{
			name: "no reasoning_effort set",
			request: &openai.ChatCompletionRequest{
				Model:               "claude-opus-4-5-20250514",
				MaxCompletionTokens: ptr.To(int64(1024)),
				Messages: []openai.ChatCompletionMessageParamUnion{
					{OfUser: &openai.ChatCompletionUserMessageParam{
						Role:    "user",
						Content: openai.StringOrUserRoleContentUnion{Value: "test"},
					}},
				},
			},
			expectedEffort: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := buildAnthropicParams(tt.request, filterapi.APISchemaAWSAnthropic, "")
			require.NoError(t, err)
			require.NotNil(t, params)
			require.Equal(t, tt.expectedEffort, params.OutputConfig.Effort)
		})
	}

	t.Run("unsupported reasoning_effort returns error", func(t *testing.T) {
		request := &openai.ChatCompletionRequest{
			Model:               "claude-opus-4-5-20250514",
			MaxCompletionTokens: ptr.To(int64(1024)),
			ReasoningEffort:     "invalid",
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "test"},
				}},
			},
		}
		_, err := buildAnthropicParams(request, filterapi.APISchemaAWSAnthropic, "")
		require.Error(t, err)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		require.Contains(t, err.Error(), "unsupported reasoning effort level")
	})

	t.Run("reasoning_effort enabled via modelNameOverride when request model is custom", func(t *testing.T) {
		request := &openai.ChatCompletionRequest{
			Model:               "my-custom-model", // User-defined name that doesn't match effort models.
			MaxCompletionTokens: ptr.To(int64(1024)),
			ReasoningEffort:     openai.ReasoningEffortHigh,
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "test"},
				}},
			},
		}
		// The modelNameOverride contains a recognized model identifier.
		params, err := buildAnthropicParams(request, filterapi.APISchemaAWSAnthropic, "us.anthropic.claude-opus-4-5-20250514-v1:0")
		require.NoError(t, err)
		require.NotNil(t, params)
		require.Equal(t, anthropic.OutputConfigEffortHigh, params.OutputConfig.Effort)
	})

	t.Run("reasoning_effort skipped when modelNameOverride is unsupported model", func(t *testing.T) {
		request := &openai.ChatCompletionRequest{
			Model:               "claude-opus-4-5-20250514", // Request model matches, but override doesn't.
			MaxCompletionTokens: ptr.To(int64(1024)),
			ReasoningEffort:     openai.ReasoningEffortHigh,
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    "user",
					Content: openai.StringOrUserRoleContentUnion{Value: "test"},
				}},
			},
		}
		// The modelNameOverride points to an unsupported model.
		params, err := buildAnthropicParams(request, filterapi.APISchemaAWSAnthropic, "us.anthropic.claude-3-sonnet-20240229-v1:0")
		require.NoError(t, err)
		require.NotNil(t, params)
		require.Equal(t, anthropic.OutputConfigEffort(""), params.OutputConfig.Effort)
	})
}

func TestAnthropicStreamParser_StreamingTokenUsage(t *testing.T) {
	tests := []struct {
		name                        string
		events                      string
		expectedInputTokens         uint32
		expectedOutputTokens        uint32
		expectedTotalTokens         uint32
		expectedCachedTokens        uint32
		expectedCacheCreationTokens uint32
	}{
		{
			name: "with cache tokens",
			events: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_abc123", "type": "message", "role": "assistant", "content": [], "model": "claude-sonnet-4-6", "usage": {"input_tokens": 678, "cache_read_input_tokens": 13363, "cache_creation_input_tokens": 0, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hi"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 678, "cache_read_input_tokens": 13363, "cache_creation_input_tokens": 0, "output_tokens": 5}}

event: message_stop
data: {"type": "message_stop"}

`,
			expectedInputTokens:         14041, // 678 + 13363 + 0
			expectedOutputTokens:        5,
			expectedTotalTokens:         14046, // 14041 + 5
			expectedCachedTokens:        13363,
			expectedCacheCreationTokens: 0,
		},
		{
			name: "without cache tokens",
			events: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_abc456", "type": "message", "role": "assistant", "content": [], "model": "claude-sonnet-4-6", "usage": {"input_tokens": 100, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 100, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 10}}

event: message_stop
data: {"type": "message_stop"}

`,
			expectedInputTokens:         100,
			expectedOutputTokens:        10,
			expectedTotalTokens:         110,
			expectedCachedTokens:        0,
			expectedCacheCreationTokens: 0,
		},
		{
			name: "with cache creation tokens",
			events: `event: message_start
data: {"type": "message_start", "message": {"id": "msg_abc789", "type": "message", "role": "assistant", "content": [], "model": "claude-sonnet-4-6", "usage": {"input_tokens": 200, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 5000, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Response"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 200, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 5000, "output_tokens": 8}}

event: message_stop
data: {"type": "message_stop"}

`,
			expectedInputTokens:         5200, // 200 + 5000 + 0
			expectedOutputTokens:        8,
			expectedTotalTokens:         5208, // 5200 + 8
			expectedCachedTokens:        0,
			expectedCacheCreationTokens: 5000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := newAnthropicStreamParser("claude-sonnet-4-6")

			// Feed each event block separately (simulating chunked SSE delivery),
			// with the last chunk marked as endOfStream.
			chunks := splitSSEEvents(tt.events)
			var tokenUsage metrics.TokenUsage
			for i, chunk := range chunks {
				endOfStream := i == len(chunks)-1
				_, _, usage, _, err := parser.Process(strings.NewReader(chunk), endOfStream, nil)
				require.NoError(t, err)
				if endOfStream {
					tokenUsage = usage
				}
			}

			inputTokens, inputSet := tokenUsage.InputTokens()
			assert.True(t, inputSet, "InputTokens should be set")
			assert.Equal(t, tt.expectedInputTokens, inputTokens, "InputTokens mismatch")

			outputTokens, outputSet := tokenUsage.OutputTokens()
			assert.True(t, outputSet, "OutputTokens should be set")
			assert.Equal(t, tt.expectedOutputTokens, outputTokens, "OutputTokens mismatch")

			totalTokens, totalSet := tokenUsage.TotalTokens()
			assert.True(t, totalSet, "TotalTokens should be set")
			assert.Equal(t, tt.expectedTotalTokens, totalTokens, "TotalTokens mismatch")

			cachedTokens, cachedSet := tokenUsage.CachedInputTokens()
			assert.True(t, cachedSet, "CachedInputTokens should be set")
			assert.Equal(t, tt.expectedCachedTokens, cachedTokens, "CachedInputTokens mismatch")

			cacheCreation, cacheCreationSet := tokenUsage.CacheCreationInputTokens()
			assert.True(t, cacheCreationSet, "CacheCreationInputTokens should be set")
			assert.Equal(t, tt.expectedCacheCreationTokens, cacheCreation, "CacheCreationInputTokens mismatch")
		})
	}
}

func splitSSEEvents(data string) []string {
	parts := strings.Split(data, "\n\n")
	var events []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			events = append(events, p+"\n\n")
		}
	}
	return events
}

func TestOpenAIToAnthropicMessages_ToolResultCacheControl(t *testing.T) {
	ephemeral := anthropic.CacheControlEphemeralParam{Type: constant.ValueOf[constant.Ephemeral]()}

	t.Run("string content with message-level cache_control", func(t *testing.T) {
		msgs, system, err := openAIToAnthropicMessages([]openai.ChatCompletionMessageParamUnion{
			{OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "toolu_1",
				Content:    openai.ContentUnion{Value: "search results"},
				AnthropicContentFields: &openai.AnthropicContentFields{
					CacheControl: ephemeral,
				},
			}},
		})
		require.NoError(t, err)
		require.Empty(t, system)
		require.Len(t, msgs, 1)
		require.Len(t, msgs[0].Content, 1)
		require.NotNil(t, msgs[0].Content[0].OfToolResult)
		require.Equal(t, "toolu_1", msgs[0].Content[0].OfToolResult.ToolUseID)
		require.Equal(t, ephemeral, msgs[0].Content[0].OfToolResult.CacheControl)
	})

	t.Run("multipart content with message-level cache_control", func(t *testing.T) {
		msgs, _, err := openAIToAnthropicMessages([]openai.ChatCompletionMessageParamUnion{
			{OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "toolu_2",
				Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
					{Type: "text", Text: "part one"},
					{Type: "text", Text: "part two"},
				}},
				AnthropicContentFields: &openai.AnthropicContentFields{
					CacheControl: ephemeral,
				},
			}},
		})
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.NotNil(t, msgs[0].Content[0].OfToolResult)
		require.Equal(t, ephemeral, msgs[0].Content[0].OfToolResult.CacheControl)
		require.Len(t, msgs[0].Content[0].OfToolResult.Content, 2)
	})

	t.Run("consecutive tool results preserve per-message cache markers", func(t *testing.T) {
		msgs, _, err := openAIToAnthropicMessages([]openai.ChatCompletionMessageParamUnion{
			{OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "toolu_a",
				Content:    openai.ContentUnion{Value: "first"},
			}},
			{OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "toolu_b",
				Content:    openai.ContentUnion{Value: "second"},
				AnthropicContentFields: &openai.AnthropicContentFields{
					CacheControl: ephemeral,
				},
			}},
		})
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Len(t, msgs[0].Content, 2)
		require.Equal(t, anthropic.CacheControlEphemeralParam{}, msgs[0].Content[0].OfToolResult.CacheControl)
		require.Equal(t, ephemeral, msgs[0].Content[1].OfToolResult.CacheControl)
	})

	t.Run("unmarked tool result has no cache_control", func(t *testing.T) {
		msgs, _, err := openAIToAnthropicMessages([]openai.ChatCompletionMessageParamUnion{
			{OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "toolu_plain",
				Content:    openai.ContentUnion{Value: "plain result"},
			}},
		})
		require.NoError(t, err)
		require.Equal(t, anthropic.CacheControlEphemeralParam{}, msgs[0].Content[0].OfToolResult.CacheControl)
	})

	t.Run("content-part cache_control still applies when message-level is absent", func(t *testing.T) {
		msgs, _, err := openAIToAnthropicMessages([]openai.ChatCompletionMessageParamUnion{
			{OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: "toolu_part",
				Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
					{
						Type: "text",
						Text: "cached via content part",
						AnthropicContentFields: &openai.AnthropicContentFields{
							CacheControl: ephemeral,
						},
					},
				}},
			}},
		})
		require.NoError(t, err)
		require.Equal(t, ephemeral, msgs[0].Content[0].OfToolResult.CacheControl)
	})
}

// The space after the SSE field colon is optional per the specification, and some
// Anthropic-compatible backends omit it on both "event:" and "data:" lines. The
// parser must still recognize the events and yield usage.
func TestAnthropicStreamParser_NoSpaceAfterColon(t *testing.T) {
	p := newAnthropicStreamParser("claude-sonnet-4-5")

	const stream = `event:message_start
data:{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":9,"cache_read_input_tokens":1,"cache_creation_input_tokens":0,"output_tokens":0}}}

event:message_delta
data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":16}}

event:message_stop
data:{"type":"message_stop"}

`

	_, _, tokenUsage, _, err := p.Process(strings.NewReader(stream), true, nil)
	require.NoError(t, err)

	input, ok := tokenUsage.InputTokens()
	require.True(t, ok, "input tokens were not extracted")
	require.Equal(t, uint32(10), input)
	output, ok := tokenUsage.OutputTokens()
	require.True(t, ok, "output tokens were not extracted")
	require.Equal(t, uint32(16), output)
}
