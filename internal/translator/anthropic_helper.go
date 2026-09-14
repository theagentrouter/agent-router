// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicParam "github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	openAIconstant "github.com/openai/openai-go/shared/constant"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	anthropicVersionKey   = "anthropic_version"
	tempNotSupportedError = "temperature %.2f is not supported by Anthropic (must be between 0.0 and 1.0)"

	anthropicBetaHeaderName = "anthropic-beta"
)

// anthropicInputSchemaKeysToSkip defines the keys from an OpenAI function parameter map
// that are handled explicitly and should not go into the ExtraFields map.
var anthropicInputSchemaKeysToSkip = map[string]struct{}{
	"required":   {},
	"type":       {},
	"properties": {},
}

// openAIToolParamsToAnthropicInputSchema converts OpenAI function parameters to an Anthropic ToolInputSchemaParam.
func openAIToolParamsToAnthropicInputSchema(parameters any) (anthropic.ToolInputSchemaParam, error) {
	var schema anthropic.ToolInputSchemaParam
	if parameters == nil {
		return schema, nil
	}
	paramsMap, ok := parameters.(map[string]any)
	if !ok {
		return schema, fmt.Errorf("failed to cast tool parameters to map[string]any")
	}
	if typeVal, ok := paramsMap["type"].(string); ok {
		schema.Type = constant.Object(typeVal)
	}
	if propsVal, ok := paramsMap["properties"].(map[string]any); ok {
		schema.Properties = propsVal
	}
	if requiredVal, ok := paramsMap["required"].([]any); ok {
		requiredSlice := make([]string, len(requiredVal))
		for i, v := range requiredVal {
			if s, ok := v.(string); ok {
				requiredSlice[i] = s
			}
		}
		schema.Required = requiredSlice
	}
	extraFields := make(map[string]any)
	for key, value := range paramsMap {
		if _, found := anthropicInputSchemaKeysToSkip[key]; found {
			continue
		}
		extraFields[key] = value
	}
	schema.ExtraFields = extraFields
	return schema, nil
}

func anthropicToOpenAIFinishReason(stopReason anthropic.StopReason) (openai.ChatCompletionChoicesFinishReason, error) {
	switch stopReason {
	// The most common stop reason. Indicates Claude finished its response naturally.
	// or Claude encountered one of your custom stop sequences.
	// TODO: A better way to return pause_turn
	// TODO: "pause_turn" Used with server tools like web search when Claude needs to pause a long-running operation.
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence, anthropic.StopReasonPauseTurn:
		return openai.ChatCompletionChoicesFinishReasonStop, nil
	case anthropic.StopReasonMaxTokens: // Claude stopped because it reached the max_tokens limit specified in your request.
		// TODO: do we want to return an error? see: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/implement-tool-use#handling-the-max-tokens-stop-reason
		return openai.ChatCompletionChoicesFinishReasonLength, nil
	case anthropic.StopReasonToolUse:
		return openai.ChatCompletionChoicesFinishReasonToolCalls, nil
	case anthropic.StopReasonRefusal:
		return openai.ChatCompletionChoicesFinishReasonContentFilter, nil
	default:
		return "", fmt.Errorf("received invalid stop reason %v", stopReason)
	}
}

// validateTemperatureForAnthropic checks if the temperature is within Anthropic's supported range (0.0 to 1.0).
// Returns an error if the value is greater than 1.0.
func validateTemperatureForAnthropic(temp *float64) error {
	if temp != nil && (*temp < 0.0 || *temp > 1.0) {
		return fmt.Errorf("%w: "+tempNotSupportedError, internalapi.ErrInvalidRequestBody, *temp)
	}
	return nil
}

// translateAnthropicToolChoice converts the OpenAI tool_choice parameter to the Anthropic format.
func translateAnthropicToolChoice(openAIToolChoice *openai.ChatCompletionToolChoiceUnion, disableParallelToolUse anthropicParam.Opt[bool]) (anthropic.ToolChoiceUnionParam, error) {
	var toolChoice anthropic.ToolChoiceUnionParam

	if openAIToolChoice == nil {
		return toolChoice, nil
	}

	switch choice := openAIToolChoice.Value.(type) {
	case string:
		switch choice {
		case string(openAIconstant.ValueOf[openAIconstant.Auto]()):
			toolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
			toolChoice.OfAuto.DisableParallelToolUse = disableParallelToolUse
		case "required", "any":
			toolChoice = anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
			toolChoice.OfAny.DisableParallelToolUse = disableParallelToolUse
		case "none":
			toolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
		case string(openAIconstant.ValueOf[openAIconstant.Function]()):
			// This is how anthropic forces tool use.
			// TODO: should we check if strict true in openAI request, and if so, use this?
			toolChoice = anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: choice}}
			toolChoice.OfTool.DisableParallelToolUse = disableParallelToolUse
		default:
			return anthropic.ToolChoiceUnionParam{}, fmt.Errorf("unsupported tool_choice value: %s", choice)
		}
	case openai.ChatCompletionNamedToolChoice:
		if choice.Type == openai.ToolTypeFunction && choice.Function.Name != "" {
			toolChoice = anthropic.ToolChoiceUnionParam{
				OfTool: &anthropic.ToolChoiceToolParam{
					Type:                   constant.Tool("tool"),
					Name:                   choice.Function.Name,
					DisableParallelToolUse: disableParallelToolUse,
				},
			}
		}
	default:
		return anthropic.ToolChoiceUnionParam{}, fmt.Errorf("unsupported tool_choice type: %T", openAIToolChoice)
	}
	return toolChoice, nil
}

func isAnthropicSupportedImageMediaType(mediaType string) bool {
	switch anthropic.Base64ImageSourceMediaType(mediaType) {
	case anthropic.Base64ImageSourceMediaTypeImageJPEG,
		anthropic.Base64ImageSourceMediaTypeImagePNG,
		anthropic.Base64ImageSourceMediaTypeImageGIF,
		anthropic.Base64ImageSourceMediaTypeImageWebP:
		return true
	default:
		return false
	}
}

// translateOpenAItoAnthropicTools translates OpenAI tool and tool_choice parameters
// into the Anthropic format and returns translated tool & tool choice.
func translateOpenAItoAnthropicTools(openAITools []openai.Tool, openAIToolChoice *openai.ChatCompletionToolChoiceUnion, parallelToolCalls *bool) (tools []anthropic.ToolUnionParam, toolChoice anthropic.ToolChoiceUnionParam, err error) {
	if len(openAITools) > 0 {
		anthropicTools := make([]anthropic.ToolUnionParam, 0, len(openAITools))
		for _, openAITool := range openAITools {
			if openAITool.Type != openai.ToolTypeFunction {
				err = fmt.Errorf("%w: unsupported tool type: %s", internalapi.ErrInvalidRequestBody, openAITool.Type)
				return
			}
			if openAITool.Function == nil {
				err = fmt.Errorf("%w: tool of type 'function' is missing function definition", internalapi.ErrInvalidRequestBody)
				return
			}
			toolParam := anthropic.ToolParam{
				Name:        openAITool.Function.Name,
				Description: anthropic.String(openAITool.Function.Description),
			}

			if openAITool.Function.Strict {
				toolParam.Strict = anthropic.Bool(true)
			}

			if openAITool.Function.EagerInputStreaming != nil {
				toolParam.EagerInputStreaming = anthropic.Bool(*openAITool.Function.EagerInputStreaming)
			}

			if isCacheEnabled(openAITool.Function.AnthropicContentFields) {
				toolParam.CacheControl = anthropic.NewCacheControlEphemeralParam()
			}

			if openAITool.Function.Parameters != nil {
				toolParam.InputSchema, err = openAIToolParamsToAnthropicInputSchema(openAITool.Function.Parameters)
				if err != nil {
					return
				}
			}

			anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{OfTool: &toolParam})
			if len(anthropicTools) > 0 {
				tools = anthropicTools
			}
		}

		// 2. Handle the tool_choice parameter.
		// disable parallel tool use default value is false
		// see: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/implement-tool-use#parallel-tool-use
		disableParallelToolUse := anthropic.Bool(false)
		if parallelToolCalls != nil {
			// OpenAI variable checks to allow parallel tool calls.
			// Anthropic variable checks to disable, so need to use the inverse.
			disableParallelToolUse = anthropic.Bool(!*parallelToolCalls)
		}

		toolChoice, err = translateAnthropicToolChoice(openAIToolChoice, disableParallelToolUse)
		if err != nil {
			return
		}

	}
	return
}

// convertImageContentToAnthropic translates an OpenAI image URL into the corresponding Anthropic content block.
// It handles data URIs for various image types and PDFs, as well as remote URLs.
func convertImageContentToAnthropic(imageURL string, fields *openai.AnthropicContentFields) (anthropic.ContentBlockParamUnion, error) {
	var cacheControlParam anthropic.CacheControlEphemeralParam
	if isCacheEnabled(fields) {
		cacheControlParam = fields.CacheControl
	}

	switch {
	case strings.HasPrefix(imageURL, "data:"):
		contentType, data, err := parseDataURI(imageURL)
		if err != nil {
			return anthropic.ContentBlockParamUnion{}, fmt.Errorf("failed to parse image URL: %w", err)
		}
		base64Data := base64.StdEncoding.EncodeToString(data)
		if contentType == string(constant.ValueOf[constant.ApplicationPDF]()) {
			pdfSource := anthropic.Base64PDFSourceParam{Data: base64Data}
			docBlock := anthropic.NewDocumentBlock(pdfSource)
			docBlock.OfDocument.CacheControl = cacheControlParam
			return docBlock, nil
		}
		if isAnthropicSupportedImageMediaType(contentType) {
			imgBlock := anthropic.NewImageBlockBase64(contentType, base64Data)
			imgBlock.OfImage.CacheControl = cacheControlParam
			return imgBlock, nil
		}
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("invalid media_type for image '%s'", contentType)
	case strings.HasSuffix(strings.ToLower(imageURL), ".pdf"):
		docBlock := anthropic.NewDocumentBlock(anthropic.URLPDFSourceParam{URL: imageURL})
		docBlock.OfDocument.CacheControl = cacheControlParam
		return docBlock, nil
	default:
		imgBlock := anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: imageURL})
		imgBlock.OfImage.CacheControl = cacheControlParam
		return imgBlock, nil
	}
}

func isCacheEnabled(fields *openai.AnthropicContentFields) bool {
	return fields != nil && fields.CacheControl.Type == constant.ValueOf[constant.Ephemeral]()
}

// convertContentPartsToAnthropic iterates over a slice of OpenAI content parts
// and converts each into an Anthropic content block.
func convertContentPartsToAnthropic(parts []openai.ChatCompletionContentPartUserUnionParam) ([]anthropic.ContentBlockParamUnion, error) {
	resultContent := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, contentPart := range parts {
		switch {
		case contentPart.OfText != nil:
			textBlock := anthropic.NewTextBlock(contentPart.OfText.Text)
			if isCacheEnabled(contentPart.OfText.AnthropicContentFields) {
				textBlock.OfText.CacheControl = contentPart.OfText.CacheControl
			}
			resultContent = append(resultContent, textBlock)

		case contentPart.OfImageURL != nil:
			block, err := convertImageContentToAnthropic(contentPart.OfImageURL.ImageURL.URL, contentPart.OfImageURL.AnthropicContentFields)
			if err != nil {
				return nil, err
			}
			resultContent = append(resultContent, block)

		case contentPart.OfInputAudio != nil:
			return nil, fmt.Errorf("input audio content not supported yet")
		case contentPart.OfFile != nil:
			return nil, fmt.Errorf("file content not supported yet")
		}
	}
	return resultContent, nil
}

// Helper: Convert OpenAI message content to Anthropic content.
func openAIToAnthropicContent(content any) ([]anthropic.ContentBlockParamUnion, error) {
	switch v := content.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []anthropic.ContentBlockParamUnion{
			anthropic.NewTextBlock(v),
		}, nil
	case []openai.ChatCompletionContentPartUserUnionParam:
		return convertContentPartsToAnthropic(v)
	case openai.ContentUnion:
		switch val := v.Value.(type) {
		case string:
			if val == "" {
				return nil, nil
			}
			return []anthropic.ContentBlockParamUnion{
				anthropic.NewTextBlock(val),
			}, nil
		case []openai.ChatCompletionContentPartTextParam:
			var contentBlocks []anthropic.ContentBlockParamUnion
			for _, part := range val {
				textBlock := anthropic.NewTextBlock(part.Text)
				// In an array of text parts, each can have its own cache setting.
				if isCacheEnabled(part.AnthropicContentFields) {
					textBlock.OfText.CacheControl = part.CacheControl
				}
				contentBlocks = append(contentBlocks, textBlock)
			}
			return contentBlocks, nil
		default:
			return nil, fmt.Errorf("unsupported ContentUnion value type: %T", val)
		}
	}
	return nil, fmt.Errorf("unsupported OpenAI content type: %T", content)
}

// extractSystemPromptFromDeveloperMsg flattens content and checks for cache flags.
// It returns the combined string and a boolean indicating if any part was cacheable.
func extractSystemPromptFromDeveloperMsg(msg openai.ChatCompletionDeveloperMessageParam) (msgValue string, cacheParam *anthropic.CacheControlEphemeralParam) {
	switch v := msg.Content.Value.(type) {
	case nil:
		return
	case string:
		msgValue = v
		return
	case []openai.ChatCompletionContentPartTextParam:
		// Concatenate all text parts and check for caching.
		var sb strings.Builder
		for _, part := range v {
			sb.WriteString(part.Text)
			if isCacheEnabled(part.AnthropicContentFields) {
				cacheParam = &part.CacheControl
			}
		}
		msgValue = sb.String()
		return
	default:
		return
	}
}

func anthropicRoleToOpenAIRole(role anthropic.MessageParamRole) (string, error) {
	switch role {
	case anthropic.MessageParamRoleAssistant:
		return openai.ChatMessageRoleAssistant, nil
	case anthropic.MessageParamRoleUser:
		return openai.ChatMessageRoleUser, nil
	default:
		return "", fmt.Errorf("invalid anthropic role %v", role)
	}
}

// processAssistantContent processes a single assistant content block and adds it to the content blocks.
func processAssistantContent(contentBlocks []anthropic.ContentBlockParamUnion, content openai.ChatCompletionAssistantMessageParamContent) ([]anthropic.ContentBlockParamUnion, error) {
	switch content.Type {
	case openai.ChatCompletionAssistantMessageParamContentTypeRefusal:
		if content.Refusal != nil {
			contentBlocks = append(contentBlocks, anthropic.NewTextBlock(*content.Refusal))
		}
	case openai.ChatCompletionAssistantMessageParamContentTypeText:
		if content.Text != nil {
			textBlock := anthropic.NewTextBlock(*content.Text)
			if isCacheEnabled(content.AnthropicContentFields) {
				textBlock.OfText.CacheControl = content.CacheControl
			}
			contentBlocks = append(contentBlocks, textBlock)
		}
	case openai.ChatCompletionAssistantMessageParamContentTypeThinking:
		// Thinking content requires both text and signature
		if content.Text != nil && content.Signature != nil {
			contentBlocks = append(contentBlocks, anthropic.NewThinkingBlock(*content.Signature, *content.Text))
		}
	case openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking:
		if content.RedactedContent != nil {
			switch v := content.RedactedContent.Value.(type) {
			case string:
				contentBlocks = append(contentBlocks, anthropic.NewRedactedThinkingBlock(v))
			default:
				return nil, fmt.Errorf("unsupported RedactedContent type: %T, expected string", v)
			}
		}
	default:
		return nil, fmt.Errorf("content type not supported: %v", content.Type)
	}
	return contentBlocks, nil
}

// openAIMessageToAnthropicMessageRoleAssistant converts an OpenAI assistant message to Anthropic content blocks.
// The tool_use content is appended to the Anthropic message content list if tool_calls are present.
func openAIMessageToAnthropicMessageRoleAssistant(openAiMessage *openai.ChatCompletionAssistantMessageParam) (anthropicMsg anthropic.MessageParam, err error) {
	contentBlocks := make([]anthropic.ContentBlockParamUnion, 0)
	if v, ok := openAiMessage.Content.Value.(string); ok && len(v) > 0 {
		contentBlocks = append(contentBlocks, anthropic.NewTextBlock(v))
	} else if content, ok := openAiMessage.Content.Value.(openai.ChatCompletionAssistantMessageParamContent); ok {
		contentBlocks, err = processAssistantContent(contentBlocks, content)
		if err != nil {
			return
		}
	} else if contents, ok := openAiMessage.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent); ok {
		for _, content := range contents {
			contentBlocks, err = processAssistantContent(contentBlocks, content)
			if err != nil {
				return
			}
		}
	}

	// Handle tool_calls (if any).
	for i := range openAiMessage.ToolCalls {
		toolCall := &openAiMessage.ToolCalls[i]
		if toolCall.ID == nil {
			err = fmt.Errorf("%w: tool_call at index %d is missing required field 'id'", internalapi.ErrInvalidRequestBody, i)
			return
		}
		var input map[string]any
		if err = json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
			err = fmt.Errorf("failed to unmarshal tool call arguments: %w", err)
			return
		}
		toolUse := anthropic.ToolUseBlockParam{
			ID:    *toolCall.ID,
			Type:  "tool_use",
			Name:  toolCall.Function.Name,
			Input: input,
		}

		if isCacheEnabled(toolCall.AnthropicContentFields) {
			toolUse.CacheControl = toolCall.CacheControl
		}

		contentBlocks = append(contentBlocks, anthropic.ContentBlockParamUnion{OfToolUse: &toolUse})
	}

	return anthropic.MessageParam{
		Role:    anthropic.MessageParamRoleAssistant,
		Content: contentBlocks,
	}, nil
}

// openAIToAnthropicMessages converts OpenAI messages to Anthropic message params type, handling all roles and system/developer logic.
func openAIToAnthropicMessages(openAIMsgs []openai.ChatCompletionMessageParamUnion) (anthropicMessages []anthropic.MessageParam, systemBlocks []anthropic.TextBlockParam, err error) {
	for i := 0; i < len(openAIMsgs); {
		msg := &openAIMsgs[i]
		switch {
		case msg.OfSystem != nil:
			devParam := systemMsgToDeveloperMsg(*msg.OfSystem)
			systemText, cacheControl := extractSystemPromptFromDeveloperMsg(devParam)
			systemBlock := anthropic.TextBlockParam{Text: systemText}
			if cacheControl != nil {
				systemBlock.CacheControl = *cacheControl
			}
			systemBlocks = append(systemBlocks, systemBlock)
			i++
		case msg.OfDeveloper != nil:
			systemText, cacheControl := extractSystemPromptFromDeveloperMsg(*msg.OfDeveloper)
			systemBlock := anthropic.TextBlockParam{Text: systemText}
			if cacheControl != nil {
				systemBlock.CacheControl = *cacheControl
			}
			systemBlocks = append(systemBlocks, systemBlock)
			i++
		case msg.OfUser != nil:
			message := *msg.OfUser
			var content []anthropic.ContentBlockParamUnion
			content, err = openAIToAnthropicContent(message.Content.Value)
			if err != nil {
				return
			}
			anthropicMsg := anthropic.MessageParam{
				Role:    anthropic.MessageParamRoleUser,
				Content: content,
			}
			anthropicMessages = append(anthropicMessages, anthropicMsg)
			i++
		case msg.OfAssistant != nil:
			assistantMessage := msg.OfAssistant
			var messages anthropic.MessageParam
			messages, err = openAIMessageToAnthropicMessageRoleAssistant(assistantMessage)
			if err != nil {
				return
			}
			anthropicMessages = append(anthropicMessages, messages)
			i++
		case msg.OfTool != nil:
			// Aggregate all consecutive tool messages into a single user message
			// to support parallel tool use.
			var toolResultBlocks []anthropic.ContentBlockParamUnion
			for i < len(openAIMsgs) && openAIMsgs[i].ExtractMessgaeRole() == openai.ChatMessageRoleTool {
				currentMsg := &openAIMsgs[i]
				toolMsg := currentMsg.OfTool

				var contentBlocks []anthropic.ContentBlockParamUnion
				contentBlocks, err = openAIToAnthropicContent(toolMsg.Content)
				if err != nil {
					return
				}

				var toolContent []anthropic.ToolResultBlockParamContentUnion
				var cacheControl *anthropic.CacheControlEphemeralParam

				for i := range contentBlocks {
					var trb anthropic.ToolResultBlockParamContentUnion
					// Check if the translated part has caching enabled.
					switch {
					case contentBlocks[i].OfText != nil:
						trb.OfText = contentBlocks[i].OfText
						cacheControl = &contentBlocks[i].OfText.CacheControl
					case contentBlocks[i].OfImage != nil:
						trb.OfImage = contentBlocks[i].OfImage
						cacheControl = &contentBlocks[i].OfImage.CacheControl
					case contentBlocks[i].OfDocument != nil:
						trb.OfDocument = contentBlocks[i].OfDocument
						cacheControl = &contentBlocks[i].OfDocument.CacheControl
					}
					toolContent = append(toolContent, trb)
				}

				isError := false
				if contentStr, ok := toolMsg.Content.Value.(string); ok {
					var contentMap map[string]any
					if json.Unmarshal([]byte(contentStr), &contentMap) == nil {
						if _, ok = contentMap["error"]; ok {
							isError = true
						}
					}
				}

				toolResultBlock := anthropic.ToolResultBlockParam{
					ToolUseID: toolMsg.ToolCallID,
					Type:      "tool_result",
					Content:   toolContent,
					IsError:   anthropic.Bool(isError),
				}

				// Prefer message-level cache_control so string tool results can
				// carry a breakpoint; fall back to content-part markers.
				switch {
				case isCacheEnabled(toolMsg.AnthropicContentFields):
					toolResultBlock.CacheControl = toolMsg.CacheControl
				case cacheControl != nil:
					toolResultBlock.CacheControl = *cacheControl
				}

				toolResultBlockUnion := anthropic.ContentBlockParamUnion{OfToolResult: &toolResultBlock}
				toolResultBlocks = append(toolResultBlocks, toolResultBlockUnion)
				i++
			}
			// Append all aggregated tool results.
			anthropicMsg := anthropic.MessageParam{
				Role:    anthropic.MessageParamRoleUser,
				Content: toolResultBlocks,
			}
			anthropicMessages = append(anthropicMessages, anthropicMsg)
		default:
			err = fmt.Errorf("unsupported OpenAI role type: %s", msg.ExtractMessgaeRole())
			return
		}
	}
	return
}

// NewThinkingConfigParamUnion converts a ThinkingUnion into a ThinkingConfigParamUnion.
func getThinkingConfigParamUnion(tu *openai.ThinkingUnion) *anthropic.ThinkingConfigParamUnion {
	if tu == nil {
		return nil
	}

	result := &anthropic.ThinkingConfigParamUnion{}

	switch {
	case tu.OfEnabled != nil:
		result.OfEnabled = &anthropic.ThinkingConfigEnabledParam{
			BudgetTokens: tu.OfEnabled.BudgetTokens,
			Type:         constant.Enabled(tu.OfEnabled.Type),
			Display:      anthropic.ThinkingConfigEnabledDisplay(tu.OfEnabled.Display),
		}
	case tu.OfDisabled != nil:
		result.OfDisabled = &anthropic.ThinkingConfigDisabledParam{
			Type: constant.Disabled(tu.OfDisabled.Type),
		}
	case tu.OfAdaptive != nil:
		result.OfAdaptive = &anthropic.ThinkingConfigAdaptiveParam{
			Type:    constant.Adaptive(tu.OfAdaptive.Type),
			Display: anthropic.ThinkingConfigAdaptiveDisplay(tu.OfAdaptive.Display),
		}
	}

	return result
}

// modelContainsAny checks if the model string contains any of the given identifiers (case-insensitive).
func modelContainsAny(model internalapi.RequestModel, identifiers []string) bool {
	modelLower := strings.ToLower(model)
	for _, id := range identifiers {
		if strings.Contains(modelLower, id) {
			return true
		}
	}
	return false
}

// Structured output (OutputConfig) support differs by backend. The supported
// model list on AWS Bedrock (InvokeModel) is a strict subset of the list on
// GCP Vertex AI, so the lists are maintained separately and selected by schema.
// See: https://platform.claude.com/docs/en/build-with-claude/structured-outputs

// awsOutputConfigModels lists model identifiers that support structured outputs
// on AWS Bedrock via the InvokeModel API: Claude Opus 4.6, Claude Sonnet 4.6,
// Claude Sonnet 4.5, Claude Opus 4.5, and Claude Haiku 4.5.
var awsOutputConfigModels = []string{
	"opus-4-5",   // Claude Opus 4.5
	"sonnet-4-5", // Claude Sonnet 4.5
	"haiku-4-5",  // Claude Haiku 4.5
	"opus-4-6",   // Claude Opus 4.6
	"sonnet-4-6", // Claude Sonnet 4.6
}

// gcpOutputConfigModels lists model identifiers that support structured outputs
// on GCP Vertex AI: Claude Fable 5, Claude Mythos 5, Claude Opus 4.8, Claude
// Mythos Preview, Claude Opus 4.7, Claude Opus 4.6, Claude Sonnet 5, Claude
// Sonnet 4.6, Claude Sonnet 4.5, Claude Opus 4.5, and Claude Haiku 4.5.
var gcpOutputConfigModels = []string{
	"opus-4-5",       // Claude Opus 4.5
	"sonnet-4-5",     // Claude Sonnet 4.5
	"haiku-4-5",      // Claude Haiku 4.5
	"opus-4-6",       // Claude Opus 4.6
	"sonnet-4-6",     // Claude Sonnet 4.6
	"opus-4-7",       // Claude Opus 4.7
	"opus-4-8",       // Claude Opus 4.8
	"sonnet-5",       // Claude Sonnet 5
	"fable-5",        // Claude Fable 5
	"mythos-5",       // Claude Mythos 5
	"mythos-preview", // Claude Mythos Preview
}

func outputConfigAvailable(apiSchema filterapi.APISchemaName, model internalapi.RequestModel) bool {
	switch apiSchema {
	case filterapi.APISchemaGCPAnthropic:
		return modelContainsAny(model, gcpOutputConfigModels)
	case filterapi.APISchemaAWSAnthropic:
		return modelContainsAny(model, awsOutputConfigModels)
	default:
		return false
	}
}

// effortModels lists model identifiers that support the output_config.effort parameter.
// The effort parameter is supported by Claude Fable 5, Claude Mythos 5, Claude Opus 4.8, Claude Mythos Preview,
// Claude Opus 4.7, Claude Opus 4.6, Claude Sonnet 5, Claude Sonnet 4.6, and Claude Opus 4.5.
// See: https://platform.claude.com/docs/en/build-with-claude/effort
var effortModels = []string{
	"opus-4-5",       // Claude Opus 4.5
	"opus-4-6",       // Claude Opus 4.6
	"opus-4-7",       // Claude Opus 4.7
	"opus-4-8",       // Claude Opus 4.8
	"sonnet-4-6",     // Claude Sonnet 4.6
	"sonnet-5",       // Claude Sonnet 5
	"fable-5",        // Claude Fable 5
	"mythos-5",       // Claude Mythos 5
	"mythos-preview", // Claude Mythos Preview
}

func effortAvailable(model internalapi.RequestModel) bool {
	return modelContainsAny(model, effortModels)
}

// mapReasoningEffortToOutputConfigEffort converts OpenAI reasoning effort levels to Anthropic output config effort levels.
// Supported levels: "low", "medium", "high", "xhigh", "max".
func mapReasoningEffortToOutputConfigEffort(reasonEffort openai.ReasoningEffort) (anthropic.OutputConfigEffort, error) {
	switch reasonEffort {
	case openai.ReasoningEffortLow:
		return anthropic.OutputConfigEffortLow, nil
	case openai.ReasoningEffortMedium:
		return anthropic.OutputConfigEffortMedium, nil
	case openai.ReasoningEffortHigh:
		return anthropic.OutputConfigEffortHigh, nil
	case openai.ReasoningEffortXhigh:
		return anthropic.OutputConfigEffortXhigh, nil
	case openai.ReasoningEffortMax:
		return anthropic.OutputConfigEffortMax, nil
	default:
		return "", fmt.Errorf("%w: unsupported reasoning effort level: %q (supported: low, medium, high, xhigh, max)", internalapi.ErrInvalidRequestBody, reasonEffort)
	}
}

// buildAnthropicParams is a helper function that translates an OpenAI request
// into the parameter struct required by the Anthropic SDK.
// The apiSchema parameter indicates the backend API schema (e.g., APISchemaAWSAnthropic,
// APISchemaGCPAnthropic) and is used to gate backend-specific feature support.
func buildAnthropicParams(openAIReq *openai.ChatCompletionRequest, apiSchema filterapi.APISchemaName, modelNameOverride internalapi.ModelNameOverride) (params *anthropic.MessageNewParams, err error) {
	// 1. Handle simple parameters.
	// max_tokens is required by the Anthropic API but optional in the OpenAI API.
	// If not set, pass 0 and let the Anthropic API reject the request.
	var maxTokensVal int64
	if maxTokens := cmp.Or(openAIReq.MaxCompletionTokens, openAIReq.MaxTokens); maxTokens != nil {
		maxTokensVal = *maxTokens
	}

	// Translate openAI contents to anthropic params.
	// 2. Translate messages and system prompts.
	messages, systemBlocks, err := openAIToAnthropicMessages(openAIReq.Messages)
	if err != nil {
		return
	}

	// 3. Translate tools and tool choice.
	tools, toolChoice, err := translateOpenAItoAnthropicTools(openAIReq.Tools, openAIReq.ToolChoice, openAIReq.ParallelToolCalls)
	if err != nil {
		return
	}

	// 4. Construct the final struct in one place.
	params = &anthropic.MessageNewParams{
		Messages:   messages,
		MaxTokens:  maxTokensVal,
		System:     systemBlocks,
		Tools:      tools,
		ToolChoice: toolChoice,
	}

	// 5. Handle structured outputs (ResponseFormat -> OutputConfig).
	// See: https://platform.claude.com/docs/en/build-with-claude/structured-outputs
	// Structured output is generally available on both AWS Bedrock and GCP Vertex AI.
	// Use modelNameOverride for feature checks when available, as it is more
	// reliable than the user-provided model name which may be arbitrarily set.
	featureCheckModel := openAIReq.Model
	if modelNameOverride != "" {
		featureCheckModel = modelNameOverride
	}
	if openAIReq.ResponseFormat != nil && openAIReq.ResponseFormat.OfJSONSchema != nil && outputConfigAvailable(apiSchema, featureCheckModel) {
		// Convert OpenAI JSON schema to Anthropic OutputConfig format
		var schemaMap map[string]any
		if err = json.Unmarshal(openAIReq.ResponseFormat.OfJSONSchema.JSONSchema.Schema, &schemaMap); err != nil {
			return nil, fmt.Errorf("failed to parse JSON schema: %w", err)
		}
		params.OutputConfig = anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{
				Type:   constant.JSONSchema("json_schema"),
				Schema: schemaMap,
			},
		}
	}

	// Map OpenAI reasoning_effort to Anthropic output_config.effort.
	if openAIReq.ReasoningEffort != "" && effortAvailable(featureCheckModel) {
		effort, effortErr := mapReasoningEffortToOutputConfigEffort(openAIReq.ReasoningEffort)
		if effortErr != nil {
			return nil, effortErr
		}
		params.OutputConfig.Effort = effort
	}

	if openAIReq.Temperature != nil {
		if err = validateTemperatureForAnthropic(openAIReq.Temperature); err != nil {
			return nil, err
		}
		params.Temperature = anthropic.Float(*openAIReq.Temperature)
	}
	if openAIReq.TopP != nil {
		params.TopP = anthropic.Float(*openAIReq.TopP)
	}
	if openAIReq.Stop.OfString.Valid() {
		params.StopSequences = []string{openAIReq.Stop.OfString.String()}
	} else if openAIReq.Stop.OfStringArray != nil {
		params.StopSequences = openAIReq.Stop.OfStringArray
	}

	// 5. Handle Vendor specific fields.
	// Since GCPAnthropic follows the Anthropic API, we also check for Anthropic vendor fields.
	if openAIReq.Thinking != nil {
		params.Thinking = *getThinkingConfigParamUnion(openAIReq.Thinking)
	}

	return params, nil
}

// anthropicToolUseToOpenAICalls converts Anthropic tool_use content blocks to OpenAI tool calls.
func anthropicToolUseToOpenAICalls(block *anthropic.ContentBlockUnion) ([]openai.ChatCompletionMessageToolCallParam, error) {
	var toolCalls []openai.ChatCompletionMessageToolCallParam
	if block.Type != string(constant.ValueOf[constant.ToolUse]()) {
		return toolCalls, nil
	}
	argsBytes, err := json.Marshal(block.Input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tool_use input: %w", err)
	}
	toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
		ID:   &block.ID,
		Type: openai.ChatCompletionMessageToolCallTypeFunction,
		Function: openai.ChatCompletionMessageToolCallFunctionParam{
			Name:      block.Name,
			Arguments: string(argsBytes),
		},
	})

	return toolCalls, nil
}

// following are streaming part

var (
	sseEventPrefixSpace = []byte("event: ")
	sseEventPrefix      = []byte("event:")
	emptyStrPtr         = ptr.To("")
)

// streamingToolCall holds the state for a single tool call that is being streamed.
type streamingToolCall struct {
	id        string
	name      string
	inputJSON string
}

// anthropicStreamParser manages the stateful translation of an Anthropic SSE stream
// to an OpenAI-compatible SSE stream.
type anthropicStreamParser struct {
	buffer          bytes.Buffer
	activeMessageID string
	activeToolCalls map[int64]*streamingToolCall
	toolIndex       int64
	tokenUsage      metrics.TokenUsage
	stopReason      anthropic.StopReason
	requestModel    internalapi.RequestModel
	sentFirstChunk  bool
	created         openai.JSONUNIXTime
}

// newAnthropicStreamParser creates a new parser for a streaming request.
func newAnthropicStreamParser(requestModel string) *anthropicStreamParser {
	toolIdx := int64(-1)
	return &anthropicStreamParser{
		requestModel:    requestModel,
		activeToolCalls: make(map[int64]*streamingToolCall),
		toolIndex:       toolIdx,
	}
}

func (p *anthropicStreamParser) writeChunk(eventBlock []byte, buf *[]byte) error {
	chunk, err := p.parseAndHandleEvent(eventBlock)
	if err != nil {
		return err
	}
	if chunk != nil {
		err := serializeOpenAIChatCompletionChunk(chunk, buf)
		if err != nil {
			return err
		}
	}
	return nil
}

type messageDeltaUsageFields struct {
	Usage *struct {
		InputTokens              *int64 `json:"input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (p *anthropicStreamParser) updateInputUsageFromMessageDelta(data []byte) error {
	// message_delta provides cumulative (not incremental) token counts.
	// This function handles input_tokens, cache_read_input_tokens, and cache_creation_input_tokens
	// from message_delta. We use Set (not Add) because these are cumulative totals.
	// This prevents double-counting when both message_start and message_delta report the same
	// cache tokens, and also handles cases where:
	// - Cache tokens are reported only in message_delta (not in message_start)
	// - message_delta provides corrected/updated cache token values that override message_start
	var event messageDeltaUsageFields
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("unmarshal message_delta usage fields: %w", err)
	}
	if event.Usage == nil {
		return nil
	}

	u := event.Usage
	inputPresent := u.InputTokens != nil && *u.InputTokens >= 0
	cacheReadPresent := u.CacheReadInputTokens != nil && *u.CacheReadInputTokens >= 0
	cacheCreationPresent := u.CacheCreationInputTokens != nil && *u.CacheCreationInputTokens >= 0
	if !inputPresent && !cacheReadPresent && !cacheCreationPresent {
		return nil
	}

	baseInputTokens := uint32(0)
	if inputPresent {
		baseInputTokens = uint32(*u.InputTokens) //nolint:gosec
	} else if inputTokens, ok := p.tokenUsage.InputTokens(); ok {
		baseInputTokens = inputTokens
		if cachedTokens, ok := p.tokenUsage.CachedInputTokens(); ok && baseInputTokens >= cachedTokens {
			baseInputTokens -= cachedTokens
		}
		if cacheCreationTokens, ok := p.tokenUsage.CacheCreationInputTokens(); ok && baseInputTokens >= cacheCreationTokens {
			baseInputTokens -= cacheCreationTokens
		}
	}

	cachedTokens, _ := p.tokenUsage.CachedInputTokens()
	if cacheReadPresent {
		cachedTokens = uint32(*u.CacheReadInputTokens) //nolint:gosec
		p.tokenUsage.SetCachedInputTokens(cachedTokens)
	}

	cacheCreationTokens, _ := p.tokenUsage.CacheCreationInputTokens()
	if cacheCreationPresent {
		cacheCreationTokens = uint32(*u.CacheCreationInputTokens) //nolint:gosec
		p.tokenUsage.SetCacheCreationInputTokens(cacheCreationTokens)
	}

	p.tokenUsage.SetInputTokens(baseInputTokens + cachedTokens + cacheCreationTokens)
	return nil
}

// Process reads from the Anthropic SSE stream, translates events to OpenAI chunks,
// and returns the mutations for Envoy.
func (p *anthropicStreamParser) Process(body io.Reader, endOfStream bool, span tracingapi.ChatCompletionSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	newBody = make([]byte, 0)
	_ = span // TODO: add support for streaming chunks in tracing.
	responseModel = p.requestModel
	if _, err = p.buffer.ReadFrom(body); err != nil {
		err = fmt.Errorf("failed to read from stream body: %w", err)
		return
	}

	for {
		eventBlock, remaining, found := bytes.Cut(p.buffer.Bytes(), []byte("\n\n"))
		if !found {
			break
		}

		if err = p.writeChunk(eventBlock, &newBody); err != nil {
			return
		}

		p.buffer.Reset()
		p.buffer.Write(remaining)
	}

	if endOfStream && p.buffer.Len() > 0 {
		finalEventBlock := p.buffer.Bytes()
		p.buffer.Reset()

		if err = p.writeChunk(finalEventBlock, &newBody); err != nil {
			return
		}
	}

	if endOfStream {
		inputTokens, _ := p.tokenUsage.InputTokens()
		outputTokens, _ := p.tokenUsage.OutputTokens()
		p.tokenUsage.SetTotalTokens(inputTokens + outputTokens)
		totalTokens, _ := p.tokenUsage.TotalTokens()
		cachedTokens, _ := p.tokenUsage.CachedInputTokens()
		cacheCreationTokens, _ := p.tokenUsage.CacheCreationInputTokens()
		reasoningTokens, _ := p.tokenUsage.ReasoningTokens()
		finalChunk := openai.ChatCompletionResponseChunk{
			ID:      p.activeMessageID,
			Created: p.created,
			Object:  "chat.completion.chunk",
			Choices: []openai.ChatCompletionResponseChunkChoice{},
			Usage: &openai.Usage{
				PromptTokens:     int(inputTokens),
				CompletionTokens: int(outputTokens),
				TotalTokens:      int(totalTokens),
				PromptTokensDetails: &openai.PromptTokensDetails{
					CachedTokens:        int(cachedTokens),
					CacheCreationTokens: int(cacheCreationTokens),
				},
				CompletionTokensDetails: &openai.CompletionTokensDetails{
					ReasoningTokens: int(reasoningTokens),
				},
			},
			Model: p.requestModel,
		}

		// Add active tool calls to the final chunk.
		var toolCalls []openai.ChatCompletionChunkChoiceDeltaToolCall
		for toolIndex, tool := range p.activeToolCalls {
			toolCalls = append(toolCalls, openai.ChatCompletionChunkChoiceDeltaToolCall{
				ID:   &tool.id,
				Type: openai.ChatCompletionMessageToolCallTypeFunction,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      tool.name,
					Arguments: tool.inputJSON,
				},
				Index: toolIndex,
			})
		}

		if len(toolCalls) > 0 {
			delta := openai.ChatCompletionResponseChunkChoiceDelta{
				ToolCalls: toolCalls,
			}
			finalChunk.Choices = append(finalChunk.Choices, openai.ChatCompletionResponseChunkChoice{
				Delta: &delta,
			})
		}

		if finalChunk.Usage.PromptTokens > 0 || finalChunk.Usage.CompletionTokens > 0 || len(finalChunk.Choices) > 0 {
			err := serializeOpenAIChatCompletionChunk(&finalChunk, &newBody)
			if err != nil {
				return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to marshal final stream chunk: %w", err)
			}
		}
		// Add the final [DONE] message to indicate the end of the stream.
		newBody = append(newBody, sseDataPrefixSpace...)
		newBody = append(newBody, sseDoneMessage...)
		newBody = append(newBody, '\n', '\n')
	}
	tokenUsage = p.tokenUsage
	return
}

func (p *anthropicStreamParser) parseAndHandleEvent(eventBlock []byte) (*openai.ChatCompletionResponseChunk, error) {
	var eventType []byte
	var eventData []byte

	lines := bytes.SplitSeq(eventBlock, []byte("\n"))
	for line := range lines {
		if after, ok := cutSSEFieldPrefix(line, sseEventPrefix); ok {
			eventType = bytes.TrimSpace(after)
		} else if after, ok := cutSSEDataPrefix(line); ok {
			// This handles JSON data that might be split across multiple 'data:' lines
			// by concatenating them (Anthropic's format).
			data := bytes.TrimSpace(after)
			eventData = append(eventData, data...)
		}
	}

	if len(eventType) > 0 && len(eventData) > 0 {
		return p.handleAnthropicStreamEvent(eventType, eventData)
	}

	return nil, nil
}

func (p *anthropicStreamParser) handleAnthropicStreamEvent(eventType []byte, data []byte) (*openai.ChatCompletionResponseChunk, error) {
	switch string(eventType) {
	case string(constant.ValueOf[constant.MessageStart]()):
		var event anthropic.MessageStartEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("unmarshal message_start: %w", err)
		}
		p.activeMessageID = event.Message.ID
		p.created = openai.JSONUNIXTime(time.Now())
		u := event.Message.Usage
		usage := metrics.ExtractTokenUsageFromExplicitCaching(
			u.InputTokens,
			u.OutputTokens,
			&u.CacheReadInputTokens,
			&u.CacheCreationInputTokens,
		)
		// Set all input token counts (input, cache read, cache creation) from message_start.
		// message_delta may also contain these fields but only output_tokens is used from it.
		if input, ok := usage.InputTokens(); ok {
			p.tokenUsage.SetInputTokens(input)
		}
		if cached, ok := usage.CachedInputTokens(); ok {
			p.tokenUsage.SetCachedInputTokens(cached)
		}
		if cacheCreation, ok := usage.CacheCreationInputTokens(); ok {
			p.tokenUsage.SetCacheCreationInputTokens(cacheCreation)
		}

		// reset the toolIndex for each message
		p.toolIndex = -1
		return nil, nil

	case string(constant.ValueOf[constant.ContentBlockStart]()):
		var event anthropic.ContentBlockStartEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("failed to unmarshal content_block_start: %w", err)
		}
		if event.ContentBlock.Type == string(constant.ValueOf[constant.ToolUse]()) || event.ContentBlock.Type == string(constant.ValueOf[constant.ServerToolUse]()) {
			p.toolIndex++
			var argsJSON string
			// Check if the input field is provided directly in the start event.
			if event.ContentBlock.Input != nil {
				switch input := event.ContentBlock.Input.(type) {
				case map[string]any:
					// for case where "input":{}, skip adding it to arguments.
					if len(input) > 0 {
						argsBytes, err := json.Marshal(input)
						if err != nil {
							return nil, fmt.Errorf("failed to marshal tool use input: %w", err)
						}
						argsJSON = string(argsBytes)
					}
				default:
					// although golang sdk defines type of Input to be any,
					// python sdk requires the type of Input to be Dict[str, object]:
					// https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/tool_use_block.py#L14.
					return nil, fmt.Errorf("unexpected tool use input type: %T", input)
				}
			}

			// Store the complete input JSON in our state.
			p.activeToolCalls[p.toolIndex] = &streamingToolCall{
				id:        event.ContentBlock.ID,
				name:      event.ContentBlock.Name,
				inputJSON: argsJSON,
			}

			delta := openai.ChatCompletionResponseChunkChoiceDelta{
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
					{
						Index: p.toolIndex,
						ID:    &event.ContentBlock.ID,
						Type:  openai.ChatCompletionMessageToolCallTypeFunction,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name: event.ContentBlock.Name,
							// Include the arguments if they are available.
							Arguments: argsJSON,
						},
					},
				},
			}
			return p.constructOpenAIChatCompletionChunk(&delta, ""), nil
		}
		if event.ContentBlock.Type == string(constant.ValueOf[constant.Thinking]()) {
			delta := openai.ChatCompletionResponseChunkChoiceDelta{Content: emptyStrPtr}
			return p.constructOpenAIChatCompletionChunk(&delta, ""), nil
		}

		if event.ContentBlock.Type == string(constant.ValueOf[constant.RedactedThinking]()) {
			// This is a latency-hiding event, ignore it.
			return nil, nil
		}

		return nil, nil

	case string(constant.ValueOf[constant.MessageDelta]()):
		var event anthropic.MessageDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("unmarshal message_delta: %w", err)
		}
		// Update input and cache token usage from message_delta.
		// This handles cases where cache tokens are only in message_delta,
		// or where message_delta provides corrected totals that override message_start.
		if err := p.updateInputUsageFromMessageDelta(data); err != nil {
			return nil, err
		}
		u := event.Usage
		// message_delta provides cumulative (not incremental) output token counts.
		// Use Set (not Add) because the value is cumulative.
		// message_start typically reports output_tokens=0, and message_delta
		// provides the final output token count.
		//
		// Guard with the SDK's JSON presence fields (Valid()): MessageDeltaUsage
		// uses non-pointer int64 fields that default to 0 when absent, so a bare
		// value check cannot distinguish "not provided" from "actually zero". A
		// stream may emit multiple message_delta events (usage is cumulative), so a
		// later message_delta that omits usage must NOT clobber output/reasoning
		// tokens set by an earlier one — see
		// https://docs.anthropic.com/en/api/messages-streaming
		if u.JSON.OutputTokens.Valid() {
			p.tokenUsage.SetOutputTokens(uint32(u.OutputTokens)) //nolint:gosec
		}
		if u.JSON.OutputTokensDetails.Valid() && u.OutputTokensDetails.JSON.ThinkingTokens.Valid() {
			p.tokenUsage.SetReasoningTokens(uint32(u.OutputTokensDetails.ThinkingTokens)) //nolint:gosec
		}
		if event.Delta.StopReason != "" {
			p.stopReason = event.Delta.StopReason
		}
		return nil, nil

	case string(constant.ValueOf[constant.ContentBlockDelta]()):
		var event anthropic.ContentBlockDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("unmarshal content_block_delta: %w", err)
		}
		switch event.Delta.Type {
		case string(constant.ValueOf[constant.TextDelta]()), string(constant.ValueOf[constant.ThinkingDelta]()):
			// Treat thinking_delta just like a text_delta.
			delta := openai.ChatCompletionResponseChunkChoiceDelta{Content: &event.Delta.Text}
			return p.constructOpenAIChatCompletionChunk(&delta, ""), nil
		case string(constant.ValueOf[constant.InputJSONDelta]()):
			tool, ok := p.activeToolCalls[p.toolIndex]
			if !ok {
				return nil, fmt.Errorf("received input_json_delta for unknown tool at index %d", p.toolIndex)
			}
			delta := openai.ChatCompletionResponseChunkChoiceDelta{
				ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
					{
						Index: p.toolIndex,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Arguments: event.Delta.PartialJSON,
						},
					},
				},
			}
			tool.inputJSON += event.Delta.PartialJSON
			return p.constructOpenAIChatCompletionChunk(&delta, ""), nil
		}

	case string(constant.ValueOf[constant.ContentBlockStop]()):
		// This event is for state cleanup, no chunk is sent.
		var event anthropic.ContentBlockStopEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("unmarshal content_block_stop: %w", err)
		}
		delete(p.activeToolCalls, p.toolIndex)
		return nil, nil

	case string(constant.ValueOf[constant.MessageStop]()):
		var event anthropic.MessageStopEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("unmarshal message_stop: %w", err)
		}

		if p.stopReason == "" {
			p.stopReason = anthropic.StopReasonEndTurn
		}

		finishReason, err := anthropicToOpenAIFinishReason(p.stopReason)
		if err != nil {
			return nil, err
		}
		return p.constructOpenAIChatCompletionChunk(&openai.ChatCompletionResponseChunkChoiceDelta{}, finishReason), nil

	case string(constant.ValueOf[constant.Error]()):
		var errEvent anthropic.ErrorResponse
		if err := json.Unmarshal(data, &errEvent); err != nil {
			return nil, fmt.Errorf("unparsable error event: %s", string(data))
		}
		return nil, fmt.Errorf("anthropic stream error: %s - %s", errEvent.Error.Type, errEvent.Error.Message)

	case "ping":
		// Anthropic sends ping events periodically to keep the stream alive.
		// Emit an empty chunk (empty delta, no finish reason) so that idle
		// downstream connections stay alive during long gaps between content
		// events. An empty delta does not carry content or the assistant role,
		// so it does not consume the role-bearing "first chunk" slot; the role
		// is still emitted on the first real content/tool-call chunk.
		return p.constructOpenAIChatCompletionChunk(&openai.ChatCompletionResponseChunkChoiceDelta{}, ""), nil
	}
	return nil, nil
}

// constructOpenAIChatCompletionChunk builds the stream chunk.
func (p *anthropicStreamParser) constructOpenAIChatCompletionChunk(delta *openai.ChatCompletionResponseChunkChoiceDelta, finishReason openai.ChatCompletionChoicesFinishReason) *openai.ChatCompletionResponseChunk {
	// Add the 'assistant' role to the very first chunk of the response.
	if !p.sentFirstChunk {
		// Only add the role if the delta actually contains content or a tool call.
		if delta.Content != nil || len(delta.ToolCalls) > 0 {
			delta.Role = openai.ChatMessageRoleAssistant
			p.sentFirstChunk = true
		}
	}

	return &openai.ChatCompletionResponseChunk{
		ID:      p.activeMessageID,
		Created: p.created,
		Object:  "chat.completion.chunk",
		Choices: []openai.ChatCompletionResponseChunkChoice{
			{
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
		Model: p.requestModel,
	}
}

// messageToChatCompletion is to translate from anthropic API's response Message into OpenAI API's response ChatCompletion
func messageToChatCompletion(anthropicResp *anthropic.Message, responseModel internalapi.RequestModel) (openAIResp *openai.ChatCompletionResponse, tokenUsage metrics.TokenUsage, err error) {
	openAIResp = &openai.ChatCompletionResponse{
		ID:      anthropicResp.ID,
		Model:   responseModel,
		Object:  string(openAIconstant.ValueOf[openAIconstant.ChatCompletion]()),
		Choices: make([]openai.ChatCompletionResponseChoice, 0),
		Created: openai.JSONUNIXTime(time.Now()),
	}
	usage := anthropicResp.Usage
	tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(
		usage.InputTokens,
		usage.OutputTokens,
		&usage.CacheReadInputTokens,
		&usage.CacheCreationInputTokens,
	)
	tokenUsage.SetReasoningTokens(uint32(usage.OutputTokensDetails.ThinkingTokens)) //nolint:gosec
	inputTokens, _ := tokenUsage.InputTokens()
	outputTokens, _ := tokenUsage.OutputTokens()
	totalTokens, _ := tokenUsage.TotalTokens()
	cachedTokens, _ := tokenUsage.CachedInputTokens()
	cacheCreationTokens, _ := tokenUsage.CacheCreationInputTokens()
	reasoningTokens, _ := tokenUsage.ReasoningTokens()
	openAIResp.Usage = openai.Usage{
		CompletionTokens: int(outputTokens),
		PromptTokens:     int(inputTokens),
		TotalTokens:      int(totalTokens),
		PromptTokensDetails: &openai.PromptTokensDetails{
			CachedTokens:        int(cachedTokens),
			CacheCreationTokens: int(cacheCreationTokens),
		},
		CompletionTokensDetails: &openai.CompletionTokensDetails{
			ReasoningTokens: int(reasoningTokens),
		},
	}

	finishReason, err := anthropicToOpenAIFinishReason(anthropicResp.StopReason)
	if err != nil {
		return nil, metrics.TokenUsage{}, err
	}

	role, err := anthropicRoleToOpenAIRole(anthropic.MessageParamRole(anthropicResp.Role))
	if err != nil {
		return nil, metrics.TokenUsage{}, err
	}

	choice := openai.ChatCompletionResponseChoice{
		Index:        0,
		Message:      openai.ChatCompletionResponseChoiceMessage{Role: role},
		FinishReason: finishReason,
	}

	for i := range anthropicResp.Content { // NOTE: Content structure is massive, do not range over values.
		output := &anthropicResp.Content[i]
		switch output.Type {
		case string(constant.ValueOf[constant.ToolUse]()):
			if output.ID != "" {
				toolCalls, toolErr := anthropicToolUseToOpenAICalls(output)
				if toolErr != nil {
					return nil, metrics.TokenUsage{}, fmt.Errorf("failed to convert anthropic tool use to openai tool call: %w", toolErr)
				}
				choice.Message.ToolCalls = append(choice.Message.ToolCalls, toolCalls...)
			}
		case string(constant.ValueOf[constant.Text]()):
			if output.Text != "" {
				if choice.Message.Content == nil {
					choice.Message.Content = &output.Text
				}
			}
		case string(constant.ValueOf[constant.Thinking]()):
			if output.Thinking != "" {
				choice.Message.ReasoningContent = &openai.ReasoningContentUnion{
					Value: &openai.ReasoningContent{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							ReasoningText: &awsbedrock.ReasoningTextBlock{
								Text:      output.Thinking,
								Signature: output.Signature,
							},
						},
					},
				}
			}
		case string(constant.ValueOf[constant.RedactedThinking]()):
			if output.Data != "" {
				choice.Message.ReasoningContent = &openai.ReasoningContentUnion{
					Value: &openai.ReasoningContent{
						ReasoningContent: &awsbedrock.ReasoningContentBlock{
							RedactedContent: []byte(output.Data),
						},
					},
				}
			}
		}
	}
	openAIResp.Choices = append(openAIResp.Choices, choice)
	return openAIResp, tokenUsage, nil
}

// awsAnthropicCountTokensPath builds the AWS Bedrock CountTokens request path
// (POST /model/{modelId}/count-tokens) for an Anthropic (Claude) model.
//
// This is Anthropic-specific, not generic Bedrock: CountTokens does not accept
// cross-region inference (CRIS) model IDs (e.g. "us.anthropic.claude-sonnet-4-6"
// returns "The provided model doesn't support counting tokens"). A CRIS ID prepends
// a geography prefix (e.g. "us.", "eu.", "apac.", "us-gov.") to the base model ID;
// anchor on the "anthropic." provider segment and drop anything before it, so every
// geography prefix is handled regardless of length. A bare base ID
// ("anthropic.claude-...") has the segment at index 0 and is left as-is. The base
// model ID is then URL-escaped so ARNs and special characters are safe in the path.
// See: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html
func awsAnthropicCountTokensPath(model string) string {
	if i := strings.Index(model, "anthropic."); i > 0 {
		model = model[i:]
	}
	return fmt.Sprintf(awsBedrockCountTokensPathFormat, url.PathEscape(model))
}

// openAIToAnthropicCountTokensParams builds the Anthropic MessageCountTokensParams
// from an OpenAI-compatible tokenize chat request. Shared by GCP and AWS Anthropic tokenize translators.
//
// Only the fields that affect the counted input tokens are mapped: Messages, Model,
// System, and Tools. Per the Anthropic count_tokens endpoint, the counted input covers
// system prompts, tools, images, PDFs, and current-turn thinking blocks (all of which
// arrive via Messages/System/Tools here). The remaining MessageCountTokensParams fields
// are intentionally omitted because they do not change the returned count:
//   - CacheControl: a no-op for token counting; caching only happens during message creation.
//   - Thinking: the thinking config adds no input tokens; only thinking blocks already
//     present in Messages count.
//   - OutputConfig: structured outputs constrain decoding (output), not the input prompt.
//   - ToolChoice: a generation-time control, not counted content.
func openAIToAnthropicCountTokensParams(chatReq *tokenize.ChatRequest, model internalapi.RequestModel) (*anthropic.MessageCountTokensParams, error) {
	messages, systemBlocks, err := openAIToAnthropicMessages(chatReq.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}

	params := &anthropic.MessageCountTokensParams{
		Messages: messages,
		Model:    model,
	}

	if len(systemBlocks) > 0 {
		if len(systemBlocks) == 1 {
			params.System = anthropic.MessageCountTokensParamsSystemUnion{
				OfString: anthropic.String(systemBlocks[0].Text),
			}
		} else {
			textBlocks := make([]anthropic.TextBlockParam, len(systemBlocks))
			for i, block := range systemBlocks {
				textBlocks[i] = anthropic.TextBlockParam{Text: block.Text}
			}
			params.System = anthropic.MessageCountTokensParamsSystemUnion{
				OfTextBlockArray: textBlocks,
			}
		}
	}

	if len(chatReq.Tools) > 0 {
		params.Tools = make([]anthropic.MessageCountTokensToolUnionParam, 0, len(chatReq.Tools))
		for _, tool := range chatReq.Tools {
			if tool.Function == nil {
				continue
			}
			inputSchema, err := openAIToolParamsToAnthropicInputSchema(tool.Function.Parameters)
			if err != nil {
				return nil, err
			}
			params.Tools = append(params.Tools, anthropic.MessageCountTokensToolParamOfTool(
				inputSchema,
				tool.Function.Name,
			))
		}
	}

	return params, nil
}
