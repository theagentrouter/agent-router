// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"maps"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	gcpModelPublisherGoogle        = "google"
	gcpModelPublisherAnthropic     = "anthropic"
	gcpMethodGenerateContent       = "generateContent"
	gcpMethodStreamGenerateContent = "streamGenerateContent"
	gcpMethodCountTokens           = "countTokens"
	gcpMethodRawPredict            = "rawPredict"
)

// dummyThoughtSignature is Google's documented compatibility escape for clients that cannot echo
// back a thought_signature on function-call parts in multi-turn requests, see
// https://ai.google.dev/gemini-api/docs/thought-signatures.
var dummyThoughtSignature = []byte("skip_thought_signature_validator")

// geminiResponseMode represents the type of response mode for Gemini requests
type geminiResponseMode string

const (
	responseModeNone  geminiResponseMode = "NONE"
	responseModeText  geminiResponseMode = "TEXT"
	responseModeJSON  geminiResponseMode = "JSON"
	responseModeEnum  geminiResponseMode = "ENUM"
	responseModeRegex geminiResponseMode = "REGEX"
)

// -------------------------------------------------------------
// Request Conversion Helper for OpenAI to GCP Gemini Translator
// -------------------------------------------------------------.

// openAIMessagesToGeminiContents converts OpenAI messages to Gemini Contents and SystemInstruction.
func openAIMessagesToGeminiContents(messages []openai.ChatCompletionMessageParamUnion, requestModel internalapi.RequestModel) ([]genai.Content, *genai.Content, error) {
	var gcpContents []genai.Content
	var systemInstruction *genai.Content
	knownToolCalls := make(map[string]string)
	var gcpParts []*genai.Part

	for _, msgUnion := range messages {
		switch {
		case msgUnion.OfDeveloper != nil:
			msg := msgUnion.OfDeveloper
			inst, err := developerMsgToGeminiParts(*msg)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid developer message: %w", err)
			}
			if len(inst) != 0 {
				if systemInstruction == nil {
					systemInstruction = &genai.Content{}
				}
				systemInstruction.Parts = append(systemInstruction.Parts, inst...)
			}
		case msgUnion.OfSystem != nil:
			msg := msgUnion.OfSystem
			devMsg := systemMsgToDeveloperMsg(*msg)
			inst, err := developerMsgToGeminiParts(devMsg)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid system message: %w", err)
			}
			if len(inst) != 0 {
				if systemInstruction == nil {
					systemInstruction = &genai.Content{}
				}
				systemInstruction.Parts = append(systemInstruction.Parts, inst...)
			}
		case msgUnion.OfUser != nil:
			msg := msgUnion.OfUser
			parts, err := userMsgToGeminiParts(*msg, requestModel)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid user message: %w", err)
			}
			gcpParts = append(gcpParts, parts...)
		case msgUnion.OfTool != nil:
			msg := msgUnion.OfTool
			part, err := toolMsgToGeminiParts(*msg, knownToolCalls)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid tool message: %w", err)
			}
			gcpParts = append(gcpParts, part)
		case msgUnion.OfAssistant != nil:
			// Flush any accumulated user/tool parts before assistant.
			if len(gcpParts) > 0 {
				gcpContents = append(gcpContents, genai.Content{Role: genai.RoleUser, Parts: gcpParts})
				gcpParts = nil
			}
			msg := msgUnion.OfAssistant
			assistantParts, toolCalls, err := assistantMsgToGeminiParts(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid assistant message: %w", err)
			}
			maps.Copy(knownToolCalls, toolCalls)
			gcpContents = append(gcpContents, genai.Content{Role: genai.RoleModel, Parts: assistantParts})
		default:
			return nil, nil, fmt.Errorf("%w: invalid role in message", internalapi.ErrInvalidRequestBody)
		}
	}

	// If there are any remaining parts after processing all messages, add them as user content.
	if len(gcpParts) > 0 {
		gcpContents = append(gcpContents, genai.Content{Role: genai.RoleUser, Parts: gcpParts})
	}
	return gcpContents, systemInstruction, nil
}

// mapDetailMediaResolution converts OpenAI image detail levels to Gemini media resolution levels.
// Returns MediaResolutionUnspecified for "auto" detail level, which indicates the caller should not
// set any specific resolution and let Gemini use its default behavior.
func mapDetailMediaResolution(detail openai.ChatCompletionContentPartImageImageURLDetail) (genai.PartMediaResolutionLevel, error) {
	switch detail {
	case openai.ChatCompletionContentPartImageImageURLDetailLow:
		return genai.PartMediaResolutionLevelMediaResolutionLow, nil
	case openai.ChatCompletionContentPartImageImageURLDetailMedium:
		return genai.PartMediaResolutionLevelMediaResolutionMedium, nil
	case openai.ChatCompletionContentPartImageImageURLDetailHigh:
		return genai.PartMediaResolutionLevelMediaResolutionHigh, nil
	case openai.ChatCompletionContentPartImageImageURLDetailAuto:
		// Return unspecified to indicate no specific resolution should be set
		return genai.PartMediaResolutionLevelMediaResolutionUnspecified, nil
	default:
		return "", fmt.Errorf("%w: unsupported detail level: %q (supported: low, medium, high, auto)", internalapi.ErrInvalidRequestBody, detail)
	}
}

// developerMsgToGeminiParts converts OpenAI developer message to Gemini Content.
func developerMsgToGeminiParts(msg openai.ChatCompletionDeveloperMessageParam) ([]*genai.Part, error) {
	var parts []*genai.Part

	switch contentValue := msg.Content.Value.(type) {
	case string:
		if contentValue != "" {
			parts = append(parts, genai.NewPartFromText(contentValue))
		}
	case []openai.ChatCompletionContentPartTextParam:
		if len(contentValue) > 0 {
			for _, textParam := range contentValue {
				if textParam.Text != "" {
					parts = append(parts, genai.NewPartFromText(textParam.Text))
				}
			}
		}
	default:
		return nil, fmt.Errorf("%w: message 'content' must be a string or an array", internalapi.ErrInvalidRequestBody)

	}
	return parts, nil
}

// userMsgToGeminiParts converts OpenAI user message to Gemini Parts.
func userMsgToGeminiParts(msg openai.ChatCompletionUserMessageParam, requestModel internalapi.RequestModel) ([]*genai.Part, error) {
	var parts []*genai.Part
	switch contentValue := msg.Content.Value.(type) {
	case string:
		if contentValue != "" {
			parts = append(parts, genai.NewPartFromText(contentValue))
		}
	case []openai.ChatCompletionContentPartUserUnionParam:
		for _, content := range contentValue {
			switch {
			case content.OfText != nil:
				parts = append(parts, genai.NewPartFromText(content.OfText.Text))
			case content.OfImageURL != nil:
				imgURL := content.OfImageURL.ImageURL.URL
				if imgURL == "" {
					// If image URL is empty, we skip it.
					continue
				}

				parsedURL, err := url.Parse(imgURL)
				if err != nil {
					return nil, fmt.Errorf("%w: invalid image URL", internalapi.ErrInvalidRequestBody)
				}

				var p *genai.Part
				if parsedURL.Scheme == "data" {
					mimeType, imgBytes, parseErr := parseDataURI(imgURL)
					if parseErr != nil {
						return nil, fmt.Errorf("%w: invalid image data URI", internalapi.ErrInvalidRequestBody)
					}
					p = genai.NewPartFromBytes(imgBytes, mimeType)
				} else {
					// Identify mimeType based in image url.
					mimeType := mimeTypeImageJPEG // Default to jpeg if unknown.
					if mt := mime.TypeByExtension(path.Ext(imgURL)); mt != "" {
						mimeType = mt
					}
					p = genai.NewPartFromURI(imgURL, mimeType)
				}

				// Handle media resolution for both data URI and regular URL cases
				if content.OfImageURL.ImageURL.Detail != "" && mediaResolutionAvailable(requestModel) {
					mediaResolution, err := mapDetailMediaResolution(content.OfImageURL.ImageURL.Detail)
					if err != nil {
						return nil, fmt.Errorf("%w: invalid detail", internalapi.ErrInvalidRequestBody)
					}
					// Always set MediaResolution when Detail is specified
					p.MediaResolution = &genai.PartMediaResolution{
						Level: mediaResolution,
					}
				}
				parts = append(parts, p)
			case content.OfInputAudio != nil:
				audio := content.OfInputAudio.InputAudio
				if audio.Data == "" {
					return nil, fmt.Errorf("%w: audio content must provide data", internalapi.ErrInvalidRequestBody)
				}
				var mimeType string
				switch audio.Format {
				case openai.ChatCompletionContentPartInputAudioInputAudioFormatWAV:
					mimeType = mimeTypeAudioWAV
				case openai.ChatCompletionContentPartInputAudioInputAudioFormatMP3:
					mimeType = mimeTypeAudioMP3
				default:
					return nil, fmt.Errorf("%w: unsupported audio format: %q", internalapi.ErrInvalidRequestBody, audio.Format)
				}
				audioBytes, err := base64.StdEncoding.DecodeString(audio.Data)
				if err != nil {
					return nil, fmt.Errorf("%w: invalid audio data encoding: %w", internalapi.ErrInvalidRequestBody, err)
				}
				parts = append(parts, genai.NewPartFromBytes(audioBytes, mimeType))
			case content.OfFile != nil:
				file := content.OfFile.File
				if file.FileID != "" {
					return nil, fmt.Errorf("%w: file_id is not supported yet", internalapi.ErrInvalidRequestBody)
				}
				if file.FileData == "" {
					return nil, fmt.Errorf("%w: file content must provide file_data", internalapi.ErrInvalidRequestBody)
				}
				mimeType, fileBytes, err := parseDataURI(file.FileData)
				if err != nil {
					return nil, fmt.Errorf("%w: invalid file data URI", internalapi.ErrInvalidRequestBody)
				}
				parts = append(parts, genai.NewPartFromBytes(fileBytes, mimeType))
			}
		}
	default:
		return nil, fmt.Errorf("%w: message 'content' must be a string or an array", internalapi.ErrInvalidRequestBody)
	}
	return parts, nil
}

// toolMsgToGeminiParts converts OpenAI tool message to Gemini Parts.
func toolMsgToGeminiParts(msg openai.ChatCompletionToolMessageParam, knownToolCalls map[string]string) (*genai.Part, error) {
	var part *genai.Part
	name := knownToolCalls[msg.ToolCallID]
	funcResponse := ""
	switch contentValue := msg.Content.Value.(type) {
	case string:
		funcResponse = contentValue
	case []openai.ChatCompletionContentPartTextParam:
		for _, textParam := range contentValue {
			if textParam.Text != "" {
				funcResponse += textParam.Text
			}
		}
	default:
		return nil, fmt.Errorf("%w: message 'content' must be a string or an array", internalapi.ErrInvalidRequestBody)
	}

	part = genai.NewPartFromFunctionResponse(name, map[string]any{"output": funcResponse})
	return part, nil
}

// assistantMsgToGeminiParts converts OpenAI assistant message to Gemini Parts and known tool calls.
func assistantMsgToGeminiParts(msg *openai.ChatCompletionAssistantMessageParam) ([]*genai.Part, map[string]string, error) {
	var parts []*genai.Part
	var thoughtSignature []byte // Collect signature from thinking content

	// First pass: scan content to find thinking signature
	if v, ok := msg.Content.Value.([]openai.ChatCompletionAssistantMessageParamContent); ok {
		for _, contPart := range v {
			if contPart.Type == openai.ChatCompletionAssistantMessageParamContentTypeThinking {
				if contPart.Signature != nil && *contPart.Signature != "" {
					// Decode base64 signature
					sigBytes, err := base64.StdEncoding.DecodeString(*contPart.Signature)
					if err != nil {
						return nil, nil, fmt.Errorf("failed to decode thought signature: %w", err)
					}
					thoughtSignature = sigBytes
					break // Only use first signature
				}
			}
		}
	}

	// Fall back to the first non-empty signature echoed back via thinking_blocks (see
	// https://docs.litellm.ai/docs/reasoning_content) when no content-part signature was found.
	if thoughtSignature == nil {
		for _, block := range msg.ThinkingBlocks {
			if block.Signature != "" {
				sigBytes, err := base64.StdEncoding.DecodeString(block.Signature)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to decode thought signature: %w", err)
				}
				thoughtSignature = sigBytes
				break // Only use first signature.
			}
		}
	}

	// Handle tool calls in the assistant message.
	knownToolCalls := make(map[string]string)
	for i, toolCall := range msg.ToolCalls {
		if toolCall.ID == nil {
			return nil, nil, fmt.Errorf("%w: tool_call at index %d is missing required field 'id'", internalapi.ErrInvalidRequestBody, i)
		}
		knownToolCalls[*toolCall.ID] = toolCall.Function.Name
		var parsedArgs map[string]any
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &parsedArgs); err != nil {
			return nil, nil, fmt.Errorf("%w: function arguments must be valid JSON", internalapi.ErrInvalidRequestBody)
		}

		funcCallPart := genai.NewPartFromFunctionCall(toolCall.Function.Name, parsedArgs)

		// According to https://ai.google.dev/gemini-api/docs/thought-signatures, if the model generates parallel function calls in a response, the thought_signature is attached only to the first functionCall part. Subsequent  functionCall parts in the same response will not contain a signature.
		if i == 0 {
			if thoughtSignature != nil {
				funcCallPart.ThoughtSignature = thoughtSignature
			} else {
				// No signature was echoed back by the client at all (e.g. an OpenAI-schema client
				// that doesn't round-trip thinking_blocks). Gemini 3.x rejects function calls in
				// multi-turn requests that are missing a thought_signature, so fall back to
				// Google's documented compatibility escape.
				funcCallPart.ThoughtSignature = dummyThoughtSignature
			}
		}

		parts = append(parts, funcCallPart)
	}

	// Handle content in the assistant message.
	switch v := msg.Content.Value.(type) {
	case string:
		if v != "" {
			parts = append(parts, genai.NewPartFromText(v))
		}
	case []openai.ChatCompletionAssistantMessageParamContent:
		for _, contPart := range v {
			switch contPart.Type {
			case openai.ChatCompletionAssistantMessageParamContentTypeText:
				if contPart.Text != nil && *contPart.Text != "" {
					parts = append(parts, genai.NewPartFromText(*contPart.Text))
				}
			case openai.ChatCompletionAssistantMessageParamContentTypeThinking:
				if contPart.Text != nil && *contPart.Text != "" {
					thoughtPart := genai.NewPartFromText(*contPart.Text)
					thoughtPart.Thought = true

					// Only attach signature to thought part if there are NO tool calls
					if len(msg.ToolCalls) == 0 && contPart.Signature != nil && *contPart.Signature != "" {
						sigBytes, err := base64.StdEncoding.DecodeString(*contPart.Signature)
						if err != nil {
							return nil, nil, fmt.Errorf("failed to decode thought signature: %w", err)
						}
						thoughtPart.ThoughtSignature = sigBytes
					}

					parts = append(parts, thoughtPart)
				}
			case openai.ChatCompletionAssistantMessageParamContentTypeRefusal:
				// Refusal messages are currently ignored in this implementation.
			default:
				return nil, nil, fmt.Errorf("%w: unsupported content type in assistant message: %s", internalapi.ErrInvalidRequestBody, contPart.Type)
			}
		}
	case nil:
		// No content provided, this is valid.
	default:
		return nil, nil, fmt.Errorf("%w: message 'content' must be a string or an array", internalapi.ErrInvalidRequestBody)
	}

	return parts, knownToolCalls, nil
}

// openAIToolsToGeminiTools converts OpenAI tools to Gemini tools.
// This function combines all the openai tools into a single Gemini Tool as distinct function declarations.
// This is mainly done because some Gemini models do not support multiple tools in a single request.
// This behavior might need to change in future based on model capabilities.
// Example Input
// [
//
//	{
//	  "type": "function",
//	  "function": {
//	    "name": "add",
//	    "description": "Add two numbers",
//	    "parameters": {
//	      "properties": {
//	        "a": {
//	          "type": "integer"
//	        },
//	        "b": {
//	          "type": "integer"
//	        }
//	      },
//	      "required": [
//	        "a",
//	        "b"
//	      ],
//	      "type": "object"
//	    }
//	  }
//	}
//
// ]
//
// Example Output
// [
//
//	{
//	  "functionDeclarations": [
//	    {
//	      "description": "Add two numbers",
//	      "name": "add",
//	      "parametersJsonSchema": {
//	        "properties": {
//	          "a": {
//	            "type": "integer"
//	          },
//	          "b": {
//	            "type": "integer"
//	          }
//	        },
//	        "required": [
//	          "a",
//	          "b"
//	        ],
//	        "type": "object"
//	      }
//	    }
//	  ]
//	}
//
// ].
func openAIToolsToGeminiTools(openaiTools []openai.Tool, parametersJSONSchemaAvailable bool) ([]genai.Tool, error) {
	if len(openaiTools) == 0 {
		return nil, nil
	}

	var genaiTools []genai.Tool

	var functionDecls []*genai.FunctionDeclaration

	for _, tool := range openaiTools {
		switch tool.Type {
		case openai.ToolTypeFunction:
			if tool.Function != nil {

				functionDecl := &genai.FunctionDeclaration{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
				}

				if parametersJSONSchemaAvailable {
					functionDecl.ParametersJsonSchema = tool.Function.Parameters
				} else if tool.Function.Parameters != nil {
					paramsMap, ok := tool.Function.Parameters.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("%w: tool %s parameters must be a JSON object", internalapi.ErrInvalidRequestBody, tool.Function.Name)
					}

					if len(paramsMap) > 0 {
						var err error
						if functionDecl.Parameters, err = jsonSchemaToGemini(paramsMap); err != nil {
							return nil, fmt.Errorf("invalid JSON schema for parameters in tool %s: %w", tool.Function.Name, err)
						}
					}
				}
				functionDecls = append(functionDecls, functionDecl)
			}
		case openai.ToolTypeImageGeneration:
			return nil, fmt.Errorf("%w: tool-type image generation not supported yet", internalapi.ErrInvalidRequestBody)
		case openai.ToolTypeEnterpriseWebSearch:
			genaiTools = append(genaiTools, genai.Tool{
				EnterpriseWebSearch: &genai.EnterpriseWebSearch{},
			})
		case openai.ToolTypeGoogleSearch:
			gs := &genai.GoogleSearch{}
			if tool.GoogleSearch != nil {
				gs.ExcludeDomains = tool.GoogleSearch.ExcludeDomains
				if tool.GoogleSearch.BlockingConfidence != "" {
					gs.BlockingConfidence = genai.PhishBlockThreshold(tool.GoogleSearch.BlockingConfidence)
				}
				if tool.GoogleSearch.TimeRangeFilter != nil {
					gs.TimeRangeFilter = openAITimeRangeFilterToGemini(tool.GoogleSearch.TimeRangeFilter)
				}
			}
			genaiTools = append(genaiTools, genai.Tool{
				GoogleSearch: gs,
			})
		default:
			return nil, fmt.Errorf("%w: unsupported tool type: %s", internalapi.ErrInvalidRequestBody, tool.Type)
		}
	}
	// Only return nil if there are no tools at all (neither function declarations nor other tools)
	if len(functionDecls) == 0 && len(genaiTools) == 0 {
		return nil, nil
	}

	// Only append function declarations if there are any
	if len(functionDecls) > 0 {
		genaiTools = append(genaiTools, genai.Tool{
			FunctionDeclarations: functionDecls,
		})
	}

	return genaiTools, nil
}

func openAITimeRangeFilterToGemini(filter *openai.GCPTimeRangeFilter) *genai.Interval {
	if filter == nil {
		return nil
	}
	interval := &genai.Interval{}
	if filter.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, filter.StartTime); err == nil {
			interval.StartTime = t
		}
	}
	if filter.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, filter.EndTime); err == nil {
			interval.EndTime = t
		}
	}
	return interval
}

// openAIToolChoiceToGeminiToolConfig converts OpenAI tool_choice to Gemini ToolConfig.
// Example Input
//
//	{
//	 "type": "function",
//	 "function": {
//	   "name": "myfunc"
//	 }
//	}
//
// Example Output
//
//	{
//	 "functionCallingConfig": {
//	   "mode": "ANY",
//	   "allowedFunctionNames": [
//	     "myfunc"
//	   ]
//	 }
//	}
func openAIToolChoiceToGeminiToolConfig(toolChoice *openai.ChatCompletionToolChoiceUnion) (*genai.ToolConfig, error) {
	if toolChoice == nil {
		return nil, nil
	}
	switch tc := toolChoice.Value.(type) {
	case string:
		switch tc {
		case "auto":
			return &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}}, nil
		case "none":
			return &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}}, nil
		case "required":
			return &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAny}}, nil
		default:
			return nil, fmt.Errorf("%w: unsupported tool_choice value '%s'", internalapi.ErrInvalidRequestBody, tc)
		}
	case openai.ChatCompletionNamedToolChoice:
		return &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode:                 genai.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{tc.Function.Name},
			},
			RetrievalConfig: nil,
		}, nil
	default:
		return nil, fmt.Errorf("%w: tool_choice type not supported", internalapi.ErrInvalidRequestBody)
	}
}

// ------------------------------------------------------------
// Gemini Version-Specific Feature Availability Functions
// ------------------------------------------------------------
//
// These functions implement version-specific feature gating for Gemini models
// to prevent potential breaking existing codes when using newer features with older model versions.
//
// Check and Update these functions when new Gemini model versions are released
// and their feature support changes.

// it only works with models after gemini2.5 according to https://ai.google.dev/gemini-api/docs/structured-output#json-schema, separate it as a small function to make it easier to maintain
func responseJSONSchemaAvailable(requestModel internalapi.RequestModel) bool {
	return strings.Contains(requestModel, "gemini") && (strings.Contains(requestModel, "2.5") || strings.Contains(requestModel, "3"))
}

// mediaResolutionAvailable checks if the model supports media resolution settings.
// Only Gemini 3.0+ models support this feature for controlling image/video quality.
func mediaResolutionAvailable(requestModel internalapi.RequestModel) bool {
	return strings.Contains(requestModel, "gemini") && strings.Contains(requestModel, "3")
}

// reasoningEffortAvailable checks if the model supports reasoning effort.
// Only Gemini 3.0+ models support this feature for controlling reasoning depth.
func reasoningEffortAvailable(requestModel internalapi.RequestModel) bool {
	return strings.Contains(requestModel, "gemini") && strings.Contains(requestModel, "3")
}

// isGeminiFlashModel checks if the model is a Gemini Flash model.
func isGeminiFlashModel(model internalapi.RequestModel) bool {
	return strings.Contains(strings.ToLower(model), "flash")
}

// mapReasoningEffortToThinkingLevel converts OpenAI reasoning effort levels to Gemini thinking levels.
// The mapping depends on the model type:
// - "none" → ThinkingLevelMinimal (Gemini Flash only)
// - "low" → ThinkingLevelLow
// - "medium" → ThinkingLevelMedium for Flash, ThinkingLevelHigh for Pro
// - "high" → ThinkingLevelHigh
// https://docs.cloud.google.com/vertex-ai/generative-ai/docs/start/get-started-with-gemini-3#openai-example
func mapReasoningEffortToThinkingLevel(reasonEffort openai.ReasoningEffort, model internalapi.RequestModel) (genai.ThinkingLevel, error) {
	isFlash := isGeminiFlashModel(model)

	switch reasonEffort {
	case openai.ReasoningEffortNone:
		if !isFlash {
			return "", fmt.Errorf("%w: reasoning effort 'none' is only supported for Gemini Flash models", internalapi.ErrInvalidRequestBody)
		}
		return genai.ThinkingLevelMinimal, nil
	case openai.ReasoningEffortLow:
		return genai.ThinkingLevelLow, nil
	case openai.ReasoningEffortMedium:
		if isFlash {
			return genai.ThinkingLevelMedium, nil
		}
		return genai.ThinkingLevelHigh, nil
	case openai.ReasoningEffortHigh:
		if !isFlash {
			return "", fmt.Errorf("%w: reasoning effort 'high' is only supported for Gemini Flash models", internalapi.ErrInvalidRequestBody)
		}
		return genai.ThinkingLevelHigh, nil
	default:
		return "", fmt.Errorf("%w: unsupported reasoning effort level: %q (supported: none, low, medium, high)", internalapi.ErrInvalidRequestBody, reasonEffort)
	}
}

// openAIReqToGeminiGenerationConfig converts OpenAI request to Gemini GenerationConfig.
func openAIReqToGeminiGenerationConfig(openAIReq *openai.ChatCompletionRequest, requestModel internalapi.RequestModel) (*genai.GenerationConfig, geminiResponseMode, error) {
	responseMode := responseModeNone
	gc := &genai.GenerationConfig{}
	if openAIReq.Temperature != nil {
		f := float32(*openAIReq.Temperature)
		gc.Temperature = &f
	}
	if openAIReq.TopP != nil {
		f := float32(*openAIReq.TopP)
		gc.TopP = &f
	}

	if openAIReq.Seed != nil {
		seed := int32(*openAIReq.Seed) // nolint:gosec
		gc.Seed = &seed
	}

	if openAIReq.TopLogProbs != nil {
		logProbs := int32(*openAIReq.TopLogProbs) // nolint:gosec
		gc.Logprobs = &logProbs
	}

	if openAIReq.LogProbs != nil {
		gc.ResponseLogprobs = *openAIReq.LogProbs
	}

	formatSpecifiedCount := 0

	if openAIReq.ResponseFormat != nil {
		formatSpecifiedCount++
		switch {
		case openAIReq.ResponseFormat.OfText != nil:
			responseMode = responseModeText
			gc.ResponseMIMEType = mimeTypeTextPlain
		case openAIReq.ResponseFormat.OfJSONObject != nil:
			responseMode = responseModeJSON
			gc.ResponseMIMEType = mimeTypeApplicationJSON
		case openAIReq.ResponseFormat.OfJSONSchema != nil:
			gc.ResponseMIMEType = mimeTypeApplicationJSON
			var schemaMap map[string]any
			if err := json.Unmarshal([]byte(openAIReq.ResponseFormat.OfJSONSchema.JSONSchema.Schema), &schemaMap); err != nil {
				return nil, responseMode, fmt.Errorf("%w: invalid json schema", internalapi.ErrInvalidRequestBody)
			}

			responseMode = responseModeJSON

			if responseJSONSchemaAvailable(requestModel) {
				gc.ResponseJsonSchema = schemaMap
			} else {
				convertedSchema, err := jsonSchemaToGemini(schemaMap)
				if err != nil {
					return nil, responseMode, fmt.Errorf("invalid JSON schema: %w", err)
				}
				gc.ResponseSchema = convertedSchema

			}
		}
	}

	if openAIReq.GuidedChoice != nil {
		formatSpecifiedCount++
		if existSchema := gc.ResponseSchema != nil || gc.ResponseJsonSchema != nil; existSchema {
			return nil, responseMode, fmt.Errorf("%w: duplicate json schema specifications", internalapi.ErrInvalidRequestBody)
		}

		responseMode = responseModeEnum
		gc.ResponseMIMEType = mimeTypeApplicationEnum
		gc.ResponseSchema = &genai.Schema{Type: "STRING", Enum: openAIReq.GuidedChoice}
	}
	if openAIReq.GuidedRegex != "" {
		formatSpecifiedCount++
		if existSchema := gc.ResponseSchema != nil || gc.ResponseJsonSchema != nil; existSchema {
			return nil, responseMode, fmt.Errorf("%w: duplicate json schema specifications", internalapi.ErrInvalidRequestBody)
		}
		responseMode = responseModeRegex
		gc.ResponseMIMEType = mimeTypeApplicationJSON
		gc.ResponseSchema = &genai.Schema{Type: "STRING", Pattern: openAIReq.GuidedRegex}
	}
	if openAIReq.GuidedJSON != nil {
		formatSpecifiedCount++
		if existSchema := gc.ResponseSchema != nil || gc.ResponseJsonSchema != nil; existSchema {
			return nil, responseMode, fmt.Errorf("%w: duplicate json schema specifications", internalapi.ErrInvalidRequestBody)
		}
		responseMode = responseModeJSON

		gc.ResponseMIMEType = mimeTypeApplicationJSON
		gc.ResponseJsonSchema = openAIReq.GuidedJSON
	}
	if openAIReq.ReasoningEffort != "" && reasoningEffortAvailable(requestModel) {
		thinkLevel, err := mapReasoningEffortToThinkingLevel(openAIReq.ReasoningEffort, requestModel)
		if err != nil {
			return nil, responseMode, fmt.Errorf("invalid reasoning effort: %w", err)
		}
		gc.ThinkingConfig = &genai.ThinkingConfig{
			ThinkingLevel: thinkLevel,
		}
	}

	// ResponseFormat and guidedJSON/guidedChoice/guidedRegex are mutually exclusive.
	// Verify only one is specified.
	if formatSpecifiedCount > 1 {
		return nil, responseMode, fmt.Errorf("%w: only one of responseFormat, guidedChoice, guidedRegex, guidedJSON can be specified", internalapi.ErrInvalidRequestBody)
	}

	if openAIReq.N != nil {
		gc.CandidateCount = int32(*openAIReq.N) // nolint:gosec
	}

	maxTokens := cmp.Or(openAIReq.MaxCompletionTokens, openAIReq.MaxTokens)
	if maxTokens != nil {
		gc.MaxOutputTokens = int32(*maxTokens) // nolint:gosec
	}

	if openAIReq.PresencePenalty != nil {
		gc.PresencePenalty = openAIReq.PresencePenalty
	}
	if openAIReq.FrequencyPenalty != nil {
		gc.FrequencyPenalty = openAIReq.FrequencyPenalty
	}
	if openAIReq.Stop.OfString.Valid() {
		gc.StopSequences = []string{openAIReq.Stop.OfString.String()}
	} else if openAIReq.Stop.OfStringArray != nil {
		gc.StopSequences = openAIReq.Stop.OfStringArray
	}
	return gc, responseMode, nil
}

// --------------------------------------------------------------
// Response Conversion Helper for GCP Gemini to OpenAI Translator
// --------------------------------------------------------------.

// geminiCandidatesToOpenAIChoices converts Gemini candidates to OpenAI choices.
func geminiCandidatesToOpenAIChoices(candidates []*genai.Candidate, responseMode geminiResponseMode) ([]openai.ChatCompletionResponseChoice, error) {
	choices := make([]openai.ChatCompletionResponseChoice, 0, len(candidates))

	for idx, candidate := range candidates {
		if candidate == nil {
			continue
		}

		// Create the choice.
		choice := openai.ChatCompletionResponseChoice{
			Index: int64(idx),
		}

		toolCalls := []openai.ChatCompletionMessageToolCallParam{}
		var err error

		if candidate.Content != nil {
			message := openai.ChatCompletionResponseChoiceMessage{
				Role: openai.ChatMessageRoleAssistant,
			}
			// Extract thought summary and text from parts.
			thoughtSummary, content, signature := extractTextAndThoughtSummaryFromGeminiParts(candidate.Content.Parts, responseMode)
			if thoughtSummary != "" {
				message.ReasoningContent = &openai.ReasoningContentUnion{Value: thoughtSummary}
			}
			if signature != "" {
				message.ThinkingBlocks = append(message.ThinkingBlocks, openai.ThinkingBlock{
					Type: "thinking", Thinking: thoughtSummary, Signature: signature,
				})
			}

			if content != "" {
				message.Content = &content
			}

			// Extract tool calls if any.
			var toolCallSignature string
			toolCalls, toolCallSignature, err = extractToolCallsFromGeminiParts(toolCalls, candidate.Content.Parts, json.Marshal)
			if err != nil {
				return nil, fmt.Errorf("error extracting tool calls: %w", err)
			}
			message.ToolCalls = toolCalls

			if toolCallSignature != "" && len(message.ThinkingBlocks) == 0 {
				message.ThinkingBlocks = append(message.ThinkingBlocks, openai.ThinkingBlock{
					Type: "thinking", Signature: toolCallSignature,
				})
			} else if toolCallSignature != "" && len(message.ThinkingBlocks) > 0 {
				message.ThinkingBlocks[0].Signature = toolCallSignature
			}

			// If there's no content but there are tool calls, set content to nil.
			if content == "" && len(toolCalls) > 0 {
				message.Content = nil
			}

			choice.Message = message

		}

		if candidate.SafetyRatings != nil {
			if choice.Message.Role == "" {
				choice.Message.Role = openai.ChatMessageRoleAssistant
			}

			choice.Message.SafetyRatings = candidate.SafetyRatings
		}

		if candidate.GroundingMetadata != nil {
			if choice.Message.Role == "" {
				choice.Message.Role = openai.ChatMessageRoleAssistant
			}

			choice.Message.GroundingMetadata = candidate.GroundingMetadata
		}

		// Handle logprobs if available.
		if candidate.LogprobsResult != nil {
			choice.Logprobs = geminiLogprobsToOpenAILogprobs(*candidate.LogprobsResult)
		}

		choice.FinishReason = geminiFinishReasonToOpenAI(candidate.FinishReason, toolCalls)

		choices = append(choices, choice)
	}

	return choices, nil
}

// Define a type constraint that includes both stream and non-stream tool call slice types.
type toolCallSlice interface {
	[]openai.ChatCompletionMessageToolCallParam | []openai.ChatCompletionChunkChoiceDeltaToolCall
}

// geminiFinishReasonToOpenAI converts Gemini finish reason to OpenAI finish reason.
func geminiFinishReasonToOpenAI[T toolCallSlice](reason genai.FinishReason, toolCalls T) openai.ChatCompletionChoicesFinishReason {
	switch reason {
	case genai.FinishReasonStop:
		if len(toolCalls) > 0 {
			return openai.ChatCompletionChoicesFinishReasonToolCalls
		}
		return openai.ChatCompletionChoicesFinishReasonStop
	case genai.FinishReasonMaxTokens:
		return openai.ChatCompletionChoicesFinishReasonLength
	case genai.FinishReasonSafety, genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent,
		genai.FinishReasonSPII, genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent:
		return openai.ChatCompletionChoicesFinishReasonContentFilter
	case genai.FinishReasonRecitation, genai.FinishReasonImageRecitation:
		return openai.ChatCompletionChoicesFinishReasonRecitation
	case genai.FinishReasonMalformedFunctionCall:
		return openai.ChatCompletionChoicesFinishReasonMalformedFunctionCall
	case genai.FinishReasonUnexpectedToolCall:
		return openai.ChatCompletionChoicesFinishReasonUnexpectedToolCall
	case genai.FinishReasonLanguage:
		return openai.ChatCompletionChoicesFinishReasonLanguage
	case genai.FinishReasonNoImage:
		return openai.ChatCompletionChoicesFinishReasonNoImage
	case "":
		// For intermediate chunks in a streaming response, the finish reason is an empty string.
		// This is normal behavior and should not be treated as an error.
		return ""
	default:
		return openai.ChatCompletionChoicesFinishReasonError
	}
}

// extractTextAndThoughtSummaryFromGeminiParts extracts thought summary and text from Gemini parts.
func extractTextAndThoughtSummaryFromGeminiParts(parts []*genai.Part, responseMode geminiResponseMode) (string, string, string) {
	var textBuilder strings.Builder
	var thoughtBuilder strings.Builder
	var signatureBuilder strings.Builder

	for _, part := range parts {
		if part != nil && part.Text != "" {
			if part.Thought {
				thoughtBuilder.WriteString(part.Text)
			} else {
				if responseMode == responseModeRegex {
					// GCP doesn't natively support REGEX response modes, so we instead express them as json schema.
					// This causes the response to be wrapped in double-quotes.
					// E.g. `"positive"` (the double-quotes at the start and end are unwanted)
					// Here we remove the wrapping double-quotes.
					part.Text = strings.TrimPrefix(part.Text, "\"")
					part.Text = strings.TrimSuffix(part.Text, "\"")
				}
				if responseMode == responseModeEnum {
					// GCP's ENUM response mode is expected to return  exactly one of the provided enum values.
					//  However, sometimes, gemini models can emit surrounding whitespace (e.g. ` negative`
					// instead of `negative`), which breaks exact-match consumers. Trim it so the
					// output matches the requested choices verbatim.
					part.Text = strings.TrimSpace(part.Text)
				}
				// ThoughtSignature is only appended with Thought as False
				if part.ThoughtSignature != nil {
					signatureBuilder.WriteString(base64.StdEncoding.EncodeToString(part.ThoughtSignature))
				}
				textBuilder.WriteString(part.Text)
			}
		}
	}
	return thoughtBuilder.String(), textBuilder.String(), signatureBuilder.String()
}

// extractToolCallsFromGeminiParts extracts tool calls and thought signature from Gemini parts.
// Returns: (toolCalls, signature, error)
func extractToolCallsFromGeminiParts(toolCalls []openai.ChatCompletionMessageToolCallParam, parts []*genai.Part, argsMarshaler json.Marshaler,
) ([]openai.ChatCompletionMessageToolCallParam, string, error) {
	var signatureBuilder strings.Builder

	for _, part := range parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}

		// Convert function call arguments to JSON string.
		args, err := argsMarshaler(part.FunctionCall.Args)
		if err != nil {
			return nil, "", fmt.Errorf("failed to marshal function arguments: %w", err)
		}

		// Generate a random ID for the tool call.
		toolCallID := uuid.New().String()

		toolCall := openai.ChatCompletionMessageToolCallParam{
			ID:   &toolCallID,
			Type: openai.ChatCompletionMessageToolCallTypeFunction,
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      part.FunctionCall.Name,
				Arguments: string(args),
			},
		}

		toolCalls = append(toolCalls, toolCall)

		// Extract ThoughtSignature if present (only the first one)
		if part.ThoughtSignature != nil && signatureBuilder.Len() == 0 {
			signatureBuilder.WriteString(base64.StdEncoding.EncodeToString(part.ThoughtSignature))
		}
	}

	if len(toolCalls) == 0 {
		return nil, "", nil
	}

	return toolCalls, signatureBuilder.String(), nil
}

// geminiUsageToOpenAIUsage converts Gemini usage metadata to OpenAI usage.
func geminiUsageToOpenAIUsage(metadata *genai.GenerateContentResponseUsageMetadata) openai.Usage {
	if metadata == nil {
		return openai.Usage{}
	}

	return openai.Usage{
		CompletionTokens: int(metadata.CandidatesTokenCount) + int(metadata.ThoughtsTokenCount),
		PromptTokens:     int(metadata.PromptTokenCount),
		TotalTokens:      int(metadata.TotalTokenCount),
		PromptTokensDetails: &openai.PromptTokensDetails{
			CachedTokens: int(metadata.CachedContentTokenCount),
		},
		CompletionTokensDetails: &openai.CompletionTokensDetails{
			ReasoningTokens: int(metadata.ThoughtsTokenCount),
		},
	}
}

// geminiLogprobsToOpenAILogprobs converts Gemini logprobs to OpenAI logprobs.
func geminiLogprobsToOpenAILogprobs(logprobsResult genai.LogprobsResult) openai.ChatCompletionChoicesLogprobs {
	if len(logprobsResult.ChosenCandidates) == 0 {
		return openai.ChatCompletionChoicesLogprobs{}
	}

	content := make([]openai.ChatCompletionTokenLogprob, 0, len(logprobsResult.ChosenCandidates))

	for i := 0; i < len(logprobsResult.ChosenCandidates); i++ {
		chosen := logprobsResult.ChosenCandidates[i]

		var topLogprobs []openai.ChatCompletionTokenLogprobTopLogprob

		// Process top candidates if available.
		if i < len(logprobsResult.TopCandidates) && logprobsResult.TopCandidates[i] != nil {
			topCandidates := logprobsResult.TopCandidates[i].Candidates
			if len(topCandidates) > 0 {
				topLogprobs = make([]openai.ChatCompletionTokenLogprobTopLogprob, 0, len(topCandidates))
				for _, tc := range topCandidates {
					topLogprobs = append(topLogprobs, openai.ChatCompletionTokenLogprobTopLogprob{
						Token:   tc.Token,
						Logprob: float64(tc.LogProbability),
					})
				}
			}
		}

		// Create token logprob.
		tokenLogprob := openai.ChatCompletionTokenLogprob{
			Token:       chosen.Token,
			Logprob:     float64(chosen.LogProbability),
			TopLogprobs: topLogprobs,
		}

		content = append(content, tokenLogprob)
	}

	// Return the logprobs.
	return openai.ChatCompletionChoicesLogprobs{
		Content: content,
	}
}

// buildGCPModelPathSuffix constructs a path Suffix with an optional queryParams where each string is in the form of "%s=%s".
func buildGCPModelPathSuffix(publisher, model, gcpMethod string, queryParams ...string) string {
	pathSuffix := fmt.Sprintf("publishers/%s/models/%s:%s", publisher, model, gcpMethod)

	if len(queryParams) > 0 {
		pathSuffix += "?" + strings.Join(queryParams, "&")
	}
	return pathSuffix
}
