// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

// This file holds AWS Bedrock Converse conversion helpers shared by the chat
// completion translator (openai_awsbedrock.go) and the tokenize translator
// (tokenize_awsbedrock.go). Keeping them here avoids duplicating the OpenAI ->
// Bedrock message/tool conversion logic in both translators.

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// openAIToolsToBedrockToolConfig converts openai ChatCompletion tools (and optional
// tool_choice) into an aws bedrock tool configuration. toolChoice may be nil.
func openAIToolsToBedrockToolConfig(tools []openai.Tool, toolChoice *openai.ChatCompletionToolChoiceUnion, model string,
) (*awsbedrock.ToolConfiguration, error) {
	toolConfig := &awsbedrock.ToolConfiguration{}
	bedrockTools := make([]*awsbedrock.Tool, 0, len(tools))
	for i := range tools {
		toolDefinition := &tools[i]
		if toolDefinition.Function != nil {
			toolName := toolDefinition.Function.Name
			var toolDesc *string
			if toolDefinition.Function.Description != "" {
				toolDesc = &toolDefinition.Function.Description
			}
			tool := &awsbedrock.Tool{
				ToolSpec: &awsbedrock.ToolSpecification{
					Name:        &toolName,
					Description: toolDesc,
					InputSchema: &awsbedrock.ToolInputSchema{
						JSON: toolDefinition.Function.Parameters,
					},
				},
			}
			bedrockTools = append(bedrockTools, tool)
			// Bedrock's Tool is a union: an element sets either toolSpec or cachePoint, never both.
			if cachePointBlock := getCachePoint(toolDefinition.Function.AnthropicContentFields); cachePointBlock != nil {
				bedrockTools = append(bedrockTools, &awsbedrock.Tool{CachePoint: cachePointBlock})
			}
		}
	}
	toolConfig.Tools = bedrockTools

	if toolChoice != nil {
		if tc, ok := toolChoice.Value.(string); ok {
			switch tc {
			case "auto":
				toolConfig.ToolChoice = &awsbedrock.ToolChoice{
					Auto: &awsbedrock.AutoToolChoice{},
				}
			case "required":
				toolConfig.ToolChoice = &awsbedrock.ToolChoice{
					Any: &awsbedrock.AnyToolChoice{},
				}
			default:
				// Anthropic Claude supports tool_choice parameter with three options.
				// * `auto` allows Claude to decide whether to call any provided tools or not.
				// * `any` tells Claude that it must use one of the provided tools, but doesn't force a particular tool.
				// * `tool` allows us to force Claude to always use a particular tool.
				// The tool option is only applied to Anthropic Claude.
				if strings.Contains(model, "anthropic") && strings.Contains(model, "claude") {
					toolConfig.ToolChoice = &awsbedrock.ToolChoice{
						Tool: &awsbedrock.SpecificToolChoice{
							Name: &tc,
						},
					}
				}
			}
		} else if tc, ok := toolChoice.Value.(openai.ChatCompletionNamedToolChoice); ok {
			toolConfig.ToolChoice = &awsbedrock.ToolChoice{
				Tool: &awsbedrock.SpecificToolChoice{
					Name: &tc.Function.Name,
				},
			}
		} else {
			return nil, fmt.Errorf("%w: tool_choice type not supported", internalapi.ErrInvalidRequestBody)
		}
	}
	return toolConfig, nil
}

// openAIMessageToBedrockMessageRoleUser converts openai user role message.
func openAIMessageToBedrockMessageRoleUser(
	openAiMessage *openai.ChatCompletionUserMessageParam, role string,
) (*awsbedrock.Message, error) {
	if v, ok := openAiMessage.Content.Value.(string); ok {
		return &awsbedrock.Message{
			Role: role,
			Content: []*awsbedrock.ContentBlock{
				{Text: ptr.To(v)},
			},
		}, nil
	} else if contents, ok := openAiMessage.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam); ok {
		chatMessage := &awsbedrock.Message{Role: role}
		chatMessage.Content = make([]*awsbedrock.ContentBlock, 0, len(contents))
		for i := range contents {
			contentPart := &contents[i]
			if contentPart.OfText != nil {
				textContentPart := contentPart.OfText
				block := &awsbedrock.ContentBlock{
					Text: &textContentPart.Text,
				}
				chatMessage.Content = append(chatMessage.Content, block)
				cachePointBlock := getCachePoint(textContentPart.AnthropicContentFields)
				if cachePointBlock != nil {
					chatMessage.Content = append(chatMessage.Content, &awsbedrock.ContentBlock{
						CachePoint: cachePointBlock,
					})
				}
			} else if contentPart.OfImageURL != nil {
				imageContentPart := contentPart.OfImageURL
				contentType, b, err := parseDataURI(imageContentPart.ImageURL.URL)
				if err != nil {
					return nil, fmt.Errorf("%w: invalid image data URI", internalapi.ErrInvalidRequestBody)
				}
				var format string
				switch contentType {
				case mimeTypeImagePNG:
					format = "png"
				case mimeTypeImageJPEG:
					format = "jpeg"
				case mimeTypeImageGIF:
					format = "gif"
				case mimeTypeImageWEBP:
					format = "webp"
				default:
					return nil, fmt.Errorf("%w: unsupported image format %s", internalapi.ErrInvalidRequestBody, contentType)
				}

				block := &awsbedrock.ContentBlock{
					Image: &awsbedrock.ImageBlock{
						Format: format,
						Source: awsbedrock.ImageSource{
							Bytes: b, // Decoded data as bytes.
						},
					},
				}
				chatMessage.Content = append(chatMessage.Content, block)
				cachePointBlock := getCachePoint(imageContentPart.AnthropicContentFields)
				if cachePointBlock != nil {
					chatMessage.Content = append(chatMessage.Content, &awsbedrock.ContentBlock{
						CachePoint: cachePointBlock,
					})
				}
			}
		}
		return chatMessage, nil
	}
	return nil, fmt.Errorf("%w: unexpected content type for user message", internalapi.ErrInvalidRequestBody)
}

// unmarshalToolCallArguments is a helper method to unmarshal tool call arguments.
func unmarshalToolCallArguments(arguments string) (map[string]any, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool call arguments: %w", err)
	}
	return input, nil
}

// openAIMessageToBedrockMessageRoleAssistant converts openai assistant role message
// The tool content is appended to the bedrock message content list if tool_call is in openai message.
func openAIMessageToBedrockMessageRoleAssistant(
	openAiMessage *openai.ChatCompletionAssistantMessageParam, role string,
) (*awsbedrock.Message, error) {
	bedrockMessage := &awsbedrock.Message{Role: role}
	contentBlocks := make([]*awsbedrock.ContentBlock, 0)

	var contentParts []openai.ChatCompletionAssistantMessageParamContent
	if v, ok := openAiMessage.Content.Value.(string); ok && len(v) > 0 {
		// Case 1: Content is a simple string.
		contentParts = append(contentParts, openai.ChatCompletionAssistantMessageParamContent{Type: openai.ChatCompletionAssistantMessageParamContentTypeText, Text: &v})
	} else if singleContent, ok := openAiMessage.Content.Value.(openai.ChatCompletionAssistantMessageParamContent); ok {
		// Case 2: Content is a single object.
		contentParts = append(contentParts, singleContent)
	} else if sliceContent, ok := openAiMessage.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent); ok {
		// Case 3: Content is already a slice of objects.
		contentParts = sliceContent
	}

	for _, content := range contentParts {
		switch content.Type {
		case openai.ChatCompletionAssistantMessageParamContentTypeText:
			if content.Text != nil {
				block := &awsbedrock.ContentBlock{
					Text: content.Text,
				}
				contentBlocks = append(contentBlocks, block)
				cachePointBlock := getCachePoint(content.AnthropicContentFields)
				if cachePointBlock != nil {
					contentBlocks = append(contentBlocks, &awsbedrock.ContentBlock{
						CachePoint: cachePointBlock,
					})
				}
			}
		case openai.ChatCompletionAssistantMessageParamContentTypeThinking:
			if content.Text != nil {
				reasoningText := &awsbedrock.ReasoningTextBlock{
					Text: *content.Text,
				}
				if content.Signature != nil {
					reasoningText.Signature = *content.Signature
				}
				block := &awsbedrock.ContentBlock{
					ReasoningContent: &awsbedrock.ReasoningContentBlock{
						ReasoningText: reasoningText,
					},
				}
				contentBlocks = append(contentBlocks, block)
				cachePointBlock := getCachePoint(content.AnthropicContentFields)
				if cachePointBlock != nil {
					contentBlocks = append(contentBlocks, &awsbedrock.ContentBlock{
						CachePoint: cachePointBlock,
					})
				}
			}
		case openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking:
			if content.RedactedContent != nil {
				switch v := content.RedactedContent.Value.(type) {
				case []byte:
					block := &awsbedrock.ContentBlock{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							RedactedContent: v,
						},
					}
					contentBlocks = append(contentBlocks, block)
					cachePointBlock := getCachePoint(content.AnthropicContentFields)
					if cachePointBlock != nil {
						contentBlocks = append(contentBlocks, &awsbedrock.ContentBlock{
							CachePoint: cachePointBlock,
						})
					}
				case string:
					return nil, fmt.Errorf("%w: redacted_content must be a binary/bytes value in bedrock", internalapi.ErrInvalidRequestBody)
				default:
					return nil, fmt.Errorf("%w: redacted_content must be a binary/bytes value in bedrock", internalapi.ErrInvalidRequestBody)
				}
			}
		case openai.ChatCompletionAssistantMessageParamContentTypeRefusal:
			if content.Refusal != nil {
				block := &awsbedrock.ContentBlock{
					Text: content.Refusal,
				}
				contentBlocks = append(contentBlocks, block)
				cachePointBlock := getCachePoint(content.AnthropicContentFields)
				if cachePointBlock != nil {
					contentBlocks = append(contentBlocks, &awsbedrock.ContentBlock{
						CachePoint: cachePointBlock,
					})
				}
			}
		}
	}

	bedrockMessage.Content = contentBlocks

	for i := range openAiMessage.ToolCalls {
		toolCall := &openAiMessage.ToolCalls[i]
		if toolCall.ID == nil {
			return nil, fmt.Errorf("%w: tool_call at index %d is missing required field 'id'", internalapi.ErrInvalidRequestBody, i)
		}
		input, err := unmarshalToolCallArguments(toolCall.Function.Arguments)
		if err != nil {
			return nil, err
		}
		bedrockMessage.Content = append(bedrockMessage.Content,
			&awsbedrock.ContentBlock{
				ToolUse: &awsbedrock.ToolUseBlock{
					Name:      toolCall.Function.Name,
					ToolUseID: *toolCall.ID,
					Input:     input,
				},
			})
	}
	return bedrockMessage, nil
}

// openAIMessageToBedrockMessageRoleSystem converts openai system role message.
func openAIMessageToBedrockMessageRoleSystem(
	openAiMessage *openai.ChatCompletionSystemMessageParam, bedrockSystem *[]*awsbedrock.SystemContentBlock,
) error {
	if v, ok := openAiMessage.Content.Value.(string); ok {
		*bedrockSystem = append(*bedrockSystem, &awsbedrock.SystemContentBlock{
			Text: &v,
		})
	} else if contents, ok := openAiMessage.Content.Value.([]openai.ChatCompletionContentPartTextParam); ok {
		for i := range contents {
			contentPart := &contents[i]
			textContentPart := contentPart.Text
			block := &awsbedrock.SystemContentBlock{
				Text: &textContentPart,
			}
			*bedrockSystem = append(*bedrockSystem, block)
			cacheBlock := getCachePoint(contentPart.AnthropicContentFields)
			if cacheBlock != nil {
				*bedrockSystem = append(*bedrockSystem, &awsbedrock.SystemContentBlock{
					CachePoint: cacheBlock,
				})
			}
		}
	} else {
		return fmt.Errorf("%w: unexpected content type for system message", internalapi.ErrInvalidRequestBody)
	}
	return nil
}

// openAIMessageToBedrockMessageRoleTool converts openai tool role message.
func openAIMessageToBedrockMessageRoleTool(
	openAiMessage *openai.ChatCompletionToolMessageParam, role string,
) (*awsbedrock.Message, error) {
	// Validate and cast the openai content value into bedrock content block.
	content := make([]*awsbedrock.ToolResultContentBlock, 0)

	switch v := openAiMessage.Content.Value.(type) {
	case string:
		content = []*awsbedrock.ToolResultContentBlock{
			{
				Text: &v,
			},
		}
	case []openai.ChatCompletionContentPartTextParam:
		for _, part := range v {
			content = append(content, &awsbedrock.ToolResultContentBlock{
				Text: &part.Text,
			})
		}

	default:
		return nil, fmt.Errorf("%w: message 'content' must be a string or an array", internalapi.ErrInvalidRequestBody)
	}

	return &awsbedrock.Message{
		Role: role,
		Content: []*awsbedrock.ContentBlock{
			{
				ToolResult: &awsbedrock.ToolResultBlock{
					Content:   content,
					ToolUseID: &openAiMessage.ToolCallID,
				},
			},
		},
	}, nil
}

// bedrockResponseError translates an AWS Bedrock error response into an OpenAI error.
// Shared by the chat completion and tokenize AWS Bedrock translators.
func bedrockResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	var openaiError openai.Error
	if v, ok := respHeaders[contentTypeHeaderName]; ok && strings.Contains(v, jsonContentType) {
		var bedrockError awsbedrock.BedrockException
		if err = json.NewDecoder(body).Decode(&bedrockError); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal error body: %w", err)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    respHeaders[awsErrorTypeHeaderName],
				Message: bedrockError.Message,
				Code:    &statusCode,
			},
		}
	} else {
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    awsBedrockBackendError,
				Message: string(buf),
				Code:    &statusCode,
			},
		}
	}
	newBody, err = json.Marshal(openaiError)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}
