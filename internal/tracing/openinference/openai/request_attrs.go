// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

// llmInvocationParameters is the representation of LLMInvocationParameters,
// which includes all parameters except messages and tools, which have their
// own attributes.
// See: openinference-instrumentation-openai _request_attributes_extractor.py.
type llmInvocationParameters struct {
	openai.ChatCompletionRequest
	Messages []openai.ChatCompletionMessageParamUnion `json:"messages,omitempty"`
	Tools    []openai.Tool                            `json:"tools,omitempty"`
}

// buildRequestAttributes builds OpenInference attributes from the request.
func buildRequestAttributes(chatRequest *openai.ChatCompletionRequest, body string, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
		attribute.String(openinference.LLMModelName, chatRequest.Model),
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, body),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	if !config.HideLLMInvocationParameters {
		if invocationParamsJSON, err := json.Marshal(llmInvocationParameters{
			ChatCompletionRequest: *chatRequest,
		}); err == nil {
			attrs = append(attrs, attribute.String(openinference.LLMInvocationParameters, string(invocationParamsJSON)))
		}
	}

	// Note: compound match here is from Python OpenInference OpenAI config.py.
	if !config.HideInputs && !config.HideInputMessages {
		for i, msg := range chatRequest.Messages {
			role := msg.ExtractMessgaeRole()
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageRole), role))

			switch {
			case msg.OfUser != nil:
				switch content := msg.OfUser.Content.Value.(type) {
				case string:
					if content != "" {
						if config.HideInputText {
							content = openinference.RedactedValue
						}
						attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageContent), content))
					}
				case []openai.ChatCompletionContentPartUserUnionParam:
					for j, part := range content {
						switch {
						case part.OfText != nil:
							maybeRedacted := part.OfText.Text
							if config.HideInputText {
								maybeRedacted = openinference.RedactedValue
							}
							attrs = append(attrs,
								attribute.String(openinference.InputMessageContentAttribute(i, j, "text"), maybeRedacted),
								attribute.String(openinference.InputMessageContentAttribute(i, j, "type"), "text"),
							)
						case part.OfImageURL != nil && part.OfImageURL.ImageURL.URL != "":
							if !config.HideInputImages {
								urlKey := openinference.InputMessageContentAttribute(i, j, "image.image.url")
								typeKey := openinference.InputMessageContentAttribute(i, j, "type")
								url := part.OfImageURL.ImageURL.URL
								if isBase64URL(url) && len(url) > config.Base64ImageMaxLength {
									url = openinference.RedactedValue
								}
								attrs = append(attrs,
									attribute.String(urlKey, url),
									attribute.String(typeKey, "image"),
								)
							}
						case part.OfInputAudio != nil:
							// Skip recording audio content attributes to match Python OpenInference behavior.
							// Audio data is already included in input.value as part of the full request.
						case part.OfFile != nil:
							// TODO: skip file content for now.
						}
					}
				}
			case msg.OfAssistant != nil:
				switch content := msg.OfAssistant.Content.Value.(type) {
				case string:
					if content != "" {
						if config.HideInputText {
							content = openinference.RedactedValue
						}
						attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageContent), content))
					}
				case []openai.ChatCompletionAssistantMessageParamContent:
					for j, part := range content {
						if part.Type == "text" && part.Text != nil {
							maybeRedacted := *part.Text
							if config.HideInputText {
								maybeRedacted = openinference.RedactedValue
							}
							attrs = append(attrs,
								attribute.String(openinference.InputMessageContentAttribute(i, j, "text"), maybeRedacted),
								attribute.String(openinference.InputMessageContentAttribute(i, j, "type"), "text"),
							)
						}
					}
				}
			case msg.OfSystem != nil:
				switch content := msg.OfSystem.Content.Value.(type) {
				case string:
					if content != "" {
						if config.HideInputText {
							content = openinference.RedactedValue
						}
						attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageContent), content))
					}
				case []openai.ChatCompletionContentPartTextParam:
					for j, part := range content {
						maybeRedacted := part.Text
						if config.HideInputText {
							maybeRedacted = openinference.RedactedValue
						}
						attrs = append(attrs,
							attribute.String(openinference.InputMessageContentAttribute(i, j, "text"), maybeRedacted),
							attribute.String(openinference.InputMessageContentAttribute(i, j, "type"), "text"),
						)
					}
				}
			case msg.OfDeveloper != nil:
				switch content := msg.OfDeveloper.Content.Value.(type) {
				case string:
					if content != "" {
						if config.HideInputText {
							content = openinference.RedactedValue
						}
						attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageContent), content))
					}
				case []openai.ChatCompletionContentPartTextParam:
					for j, part := range content {
						maybeRedacted := part.Text
						if config.HideInputText {
							maybeRedacted = openinference.RedactedValue
						}
						attrs = append(attrs,
							attribute.String(openinference.InputMessageContentAttribute(i, j, "text"), maybeRedacted),
							attribute.String(openinference.InputMessageContentAttribute(i, j, "type"), "text"),
						)
					}
				}
			case msg.OfTool != nil:
				switch content := msg.OfTool.Content.Value.(type) {
				case string:
					if content != "" {
						if config.HideInputText {
							content = openinference.RedactedValue
						}
						attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageContent), content))
					}
				case []openai.ChatCompletionContentPartTextParam:
					for j, part := range content {
						maybeRedacted := part.Text
						if config.HideInputText {
							maybeRedacted = openinference.RedactedValue
						}
						attrs = append(attrs,
							attribute.String(openinference.InputMessageContentAttribute(i, j, "text"), maybeRedacted),
							attribute.String(openinference.InputMessageContentAttribute(i, j, "type"), "text"),
						)
					}
				}
			}
		}
	}

	// Add indexed attributes for each tool.
	for i, tool := range chatRequest.Tools {
		if toolJSON, err := json.Marshal(tool); err == nil {
			attrs = append(attrs,
				attribute.String(openinference.InputToolsAttribute(i), string(toolJSON)),
			)
		}
	}

	return attrs
}

// isBase64URL checks if a string is a base64-encoded image URL.
// See: https://github.com/Arize-ai/openinference/blob/main/python/openinference-instrumentation/src/openinference/instrumentation/config.py#L339
func isBase64URL(url string) bool {
	return strings.HasPrefix(url, "data:image/") && strings.Contains(url, "base64")
}

// embeddingsInvocationParameters is the representation of LLMInvocationParameters
// for embeddings, which includes all parameters except input.
type embeddingsInvocationParameters struct {
	Model          string  `json:"model"`
	EncodingFormat *string `json:"encoding_format,omitempty"`
	Dimensions     *int    `json:"dimensions,omitempty"`
	User           *string `json:"user,omitempty"`
}

// buildEmbeddingsRequestAttributes builds OpenInference attributes from the embeddings request.
func buildEmbeddingsRequestAttributes(embRequest *openai.EmbeddingRequest, body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	// Note: llm.system and llm.provider are not used in embedding spans per spec.
	// See: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md#attributes-not-used-in-embedding-spans
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindEmbedding),
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, string(body)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	if !config.HideLLMInvocationParameters {
		params := embeddingsInvocationParameters{
			Model:          embRequest.Model,
			EncodingFormat: embRequest.EncodingFormat,
			Dimensions:     embRequest.Dimensions,
			User:           embRequest.User,
		}
		if invocationParamsJSON, err := json.Marshal(params); err == nil {
			attrs = append(attrs, attribute.String(openinference.EmbeddingInvocationParameters, string(invocationParamsJSON)))
		}
	}

	// Record embedding text attributes for string inputs only.
	// We don't decode numeric tokens to text because:
	// 1. OpenAI-compatible backends may use different tokenizers (Ollama, LocalAI, etc.)
	// 2. The same token IDs mean different things in different tokenizers
	// 3. It would require model-specific tokenizer libraries (tiktoken, sentencepiece, etc.)
	// 4. Azure deployments don't affect this (they only host OpenAI models with cl100k_base)
	// Following OpenInference spec guidance to only record human-readable text.
	if !config.HideInputs && !config.HideEmbeddingsText {
		switch {
		case embRequest.OfCompletion != nil:
			switch input := embRequest.OfCompletion.Input.Value.(type) {
			case string:
				attrs = append(attrs, attribute.String(openinference.EmbeddingTextAttribute(0), input))
			case []string:
				for i, text := range input {
					attrs = append(attrs, attribute.String(openinference.EmbeddingTextAttribute(i), text))
				}
			// Token inputs are not recorded to reduce span size.
			case []int64:
			case [][]int64:
			}
		case embRequest.OfChat != nil:
			idx := 0
			for _, msg := range embRequest.OfChat.Messages {
				if msg.OfUser == nil {
					continue
				}
				switch content := msg.OfUser.Content.Value.(type) {
				case string:
					if content != "" {
						attrs = append(attrs, attribute.String(openinference.EmbeddingTextAttribute(idx), content))
						idx++
					}
				case []openai.ChatCompletionContentPartUserUnionParam:
					for _, part := range content {
						if part.OfText != nil && part.OfText.Text != "" {
							attrs = append(attrs, attribute.String(openinference.EmbeddingTextAttribute(idx), part.OfText.Text))
							idx++
						}
					}
				}
			}
		}
	}

	return attrs
}

// completionInvocationParameters is the representation of LLMInvocationParameters
// for completions, which includes all parameters except prompt, which has its
// own attributes.
// See: openinference-instrumentation-openai _request_attributes_extractor.py.
type completionInvocationParameters struct {
	openai.CompletionRequest
	Prompt *openai.PromptUnion `json:"prompt,omitempty"`
}

// buildCompletionRequestAttributes builds OpenInference attributes from the completions request.
func buildCompletionRequestAttributes(req *openai.CompletionRequest, body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
		attribute.String(openinference.LLMModelName, req.Model),
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, string(body)),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	if !config.HideLLMInvocationParameters {
		if invocationParamsJSON, err := json.Marshal(completionInvocationParameters{
			CompletionRequest: *req,
		}); err == nil {
			attrs = append(attrs, attribute.String(openinference.LLMInvocationParameters, string(invocationParamsJSON)))
		}
	}

	// Handle prompts using indexed attribute format.
	// Per OpenInference spec, we don't decode token arrays to text.
	if !config.HideInputs && !config.HidePrompts {
		switch prompt := req.Prompt.Value.(type) {
		case string:
			// Single string prompt
			attrs = append(attrs, attribute.String(openinference.PromptTextAttribute(0), prompt))
		case []string:
			// Array of string prompts
			for i, text := range prompt {
				attrs = append(attrs, attribute.String(openinference.PromptTextAttribute(i), text))
			}
		// Token inputs are not recorded per spec guidance to avoid decoding complexity.
		case []int64:
		case [][]int64:
		}
	}

	return attrs
}

// responsesInvocationParameters is the representation of LLMInvocationParameters for responses,
// which includes all parameters except input, instructions and tools, which have their
// own attributes.
// See: openinference-instrumentation-openai _responses_api.py.
type responsesInvocationParameters struct {
	openai.ResponseRequest
	Tools        []openai.ResponseToolUnion         `json:"tools,omitzero"`
	Instructions string                             `json:"instructions,omitzero"`
	Input        openai.ResponseNewParamsInputUnion `json:"input,omitzero"`
}

// buildResponsesRequestAttributes builds OpenTelemetry attributes for responses requests.
func buildResponsesRequestAttributes(req *openai.ResponseRequest, body []byte, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
	}

	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, req.Model))
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		// Redact images if hide input images is true OR if base64 image max length is set
		modifiedBody, err := redactImageFromResponseRequestParameters(body, config.HideInputImages, config.Base64ImageMaxLength)
		if err == nil {
			attrs = append(attrs, attribute.String(openinference.InputValue, string(modifiedBody)))
		}
	}
	attrs = append(attrs, attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))

	if !config.HideLLMInvocationParameters {
		if invocationParamsJSON, err := json.Marshal(responsesInvocationParameters{
			ResponseRequest: *req,
		}); err == nil {
			attrs = append(attrs, attribute.String(openinference.LLMInvocationParameters, string(invocationParamsJSON)))
		}
	}

	if !config.HideInputs && !config.HideInputMessages {
		messageIndex := 0
		if req.Instructions != "" {
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "system"))
			if config.HideInputText {
				attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue))
			} else {
				attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), req.Instructions))
			}
			messageIndex++
		}
		switch {
		case req.Input.OfString != nil:
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "user"))
			if config.HideInputText {
				attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue))
			} else {
				attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), *req.Input.OfString))
			}
		case req.Input.OfInputItemList != nil:
			for i := range req.Input.OfInputItemList {
				attrs = handleInputItemUnionAttrs(&req.Input.OfInputItemList[i], attrs, config, messageIndex)
				messageIndex++
			}
		}
	}

	// Add indexed attributes for each tool. Ranged by index to avoid copying ResponseToolUnion.
	for i := range req.Tools {
		if toolJSON, err := json.Marshal(&req.Tools[i]); err == nil {
			attrs = append(attrs,
				attribute.String(openinference.InputToolsAttribute(i), string(toolJSON)),
			)
		}
	}
	return attrs
}

func redactImageFromResponseRequestParameters(requestJSON []byte, hideInputImages bool, base64ImageMaxLength int) ([]byte, error) {
	// Return if hide input image is false and base64 image max length is not set
	if !hideInputImages && base64ImageMaxLength <= 0 {
		return requestJSON, nil
	}

	modifiedJSON := make([]byte, len(requestJSON))
	// deep copy to avoid mutating original body
	copy(modifiedJSON, requestJSON)

	// Iterate over input[]
	inputArray := gjson.GetBytes(requestJSON, "input")
	if !inputArray.IsArray() {
		return modifiedJSON, nil
	}

	inputIdx := 0
	inputArray.ForEach(func(_, inputItem gjson.Result) bool {
		content := inputItem.Get("content")
		// skip if content is not array
		if !content.IsArray() {
			inputIdx++
			return true
		}

		// Iterate over content[]
		contentIdx := 0
		content.ForEach(func(_, contentItem gjson.Result) bool {
			if contentItem.Get("type").String() != "input_image" {
				contentIdx++
				return true
			}

			imageURL := contentItem.Get("image_url")
			if !imageURL.Exists() || imageURL.Type != gjson.String {
				contentIdx++
				return true
			}

			url := imageURL.String()

			shouldRedact := hideInputImages ||
				(isBase64URL(url) && len(url) > base64ImageMaxLength)

			if shouldRedact {
				// Build JSON path dynamically - construct full path from root
				path := fmt.Sprintf("input.%d.content.%d.image_url", inputIdx, contentIdx)
				var err error
				modifiedJSON, err = sjson.SetBytesOptions(modifiedJSON, path, openinference.RedactedValue, &sjson.Options{ReplaceInPlace: true, Optimistic: true})
				if err != nil {
					contentIdx++
					return false
				}
			}

			contentIdx++
			return true
		})

		inputIdx++
		return true
	})

	return modifiedJSON, nil
}

func handleInputItemUnionAttrs(item *openai.ResponseInputItemUnionParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, index int) []attribute.KeyValue {
	switch {
	case item.OfMessage != nil:
		attrs = setEasyInputMsgAttrs(item.OfMessage, attrs, config, index)
	case item.OfInputMessage != nil:
		attrs = setInputMsgAttrs(item.OfInputMessage, attrs, config, index)
	case item.OfOutputMessage != nil:
		attrs = setOutputMsgAttrs(item.OfOutputMessage, attrs, config, index)
	case item.OfFileSearchCall != nil:
		attrs = setFileSearchCallAttrs(item.OfFileSearchCall, attrs, config, index)
	case item.OfComputerCall != nil:
		attrs = setComputerCallAttrs(item.OfComputerCall, attrs, config, index)
	case item.OfComputerCallOutput != nil:
		attrs = setComputerCallOutputAttrs(item.OfComputerCallOutput, attrs, config, index)
	case item.OfWebSearchCall != nil:
		attrs = setWebSearchCallAttrs(item.OfWebSearchCall, attrs, config, index)
	case item.OfFunctionCall != nil:
		attrs = setFunctionCallAttrs(item.OfFunctionCall, attrs, config, index)
	case item.OfFunctionCallOutput != nil:
		attrs = setFunctionCallOutputAttrs(item.OfFunctionCallOutput, attrs, config, index)
	case item.OfReasoning != nil:
		attrs = setReasoningAttrs(item.OfReasoning, attrs, config, index)
	case item.OfCustomToolCall != nil:
		attrs = setCustomToolCallAttrs(item.OfCustomToolCall, attrs, config, index)
	case item.OfCustomToolCallOutput != nil:
		attrs = setCustomToolCallOutputAttrs(item.OfCustomToolCallOutput, attrs, config, index)
	case item.OfCompaction != nil:
		attrs = setInputCompactionAttrs(item.OfCompaction, attrs, config, index)
	case item.OfImageGenerationCall != nil:
		attrs = setInputImageGenerationCallAttrs(item.OfImageGenerationCall, attrs, config, index)
	case item.OfCodeInterpreterCall != nil:
		attrs = setInputCodeInterpreterCallAttrs(item.OfCodeInterpreterCall, attrs, config, index)
	case item.OfLocalShellCall != nil:
		attrs = setInputLocalShellCallAttrs(item.OfLocalShellCall, attrs, config, index)
	case item.OfLocalShellCallOutput != nil:
		attrs = setInputLocalShellCallOutputAttrs(item.OfLocalShellCallOutput, attrs, config, index)
	case item.OfShellCall != nil:
		attrs = setInputShellCallAttrs(item.OfShellCall, attrs, config, index)
	case item.OfShellCallOutput != nil:
		attrs = setInputShellCallOutputAttrs(item.OfShellCallOutput, attrs, config, index)
	case item.OfApplyPatchCall != nil:
		attrs = setInputApplyPatchCallAttrs(item.OfApplyPatchCall, attrs, config, index)
	case item.OfApplyPatchCallOutput != nil:
		attrs = setInputApplyPatchCallOutputAttrs(item.OfApplyPatchCallOutput, attrs, config, index)
	case item.OfMcpListTools != nil:
		attrs = setInputMcpListToolsAttrs(item.OfMcpListTools, attrs, config, index)
	case item.OfMcpApprovalRequest != nil:
		attrs = setInputMcpApprovalRequestAttrs(item.OfMcpApprovalRequest, attrs, config, index)
	case item.OfMcpApprovalResponse != nil:
		attrs = setInputMcpApprovalResponseAttrs(item.OfMcpApprovalResponse, attrs, config, index)
	case item.OfMcpCall != nil:
		attrs = setInputMcpCallAttrs(item.OfMcpCall, attrs, config, index)
	}
	return attrs
}

func setEasyInputMsgAttrs(input *openai.EasyInputMessageParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, index int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(index, openinference.MessageRole), input.Role))
	switch {
	case input.Content.OfString != nil:
		if config.HideInputText {
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(index, openinference.MessageContent), openinference.RedactedValue))
		} else {
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(index, openinference.MessageContent), *input.Content.OfString))
		}
	case len(input.Content.OfInputItemContentList) != 0:
		attrs = setInputMsgContentAttrs(input.Content.OfInputItemContentList, attrs, config, index)
	}
	return attrs
}

func setInputMsgAttrs(input *openai.ResponseInputItemMessageParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, index int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(index, openinference.MessageRole), input.Role))
	attrs = setInputMsgContentAttrs(input.Content, attrs, config, index)
	return attrs
}

func setInputMsgContentAttrs(content []openai.ResponseInputContentUnionParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, index int) []attribute.KeyValue {
	for i, part := range content {
		switch {
		case part.OfInputText != nil:
			attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "type"), "text"))
			if config.HideInputText {
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "text"), openinference.RedactedValue))
			} else {
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "text"), part.OfInputText.Text))
			}
		case part.OfInputImage != nil:
			attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "type"), "image"))
			if config.HideInputImages {
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "image.image.url"), openinference.RedactedValue))
			} else {
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "image.image.url"), part.OfInputImage.ImageURL))
			}
		case part.OfInputFile != nil:
			// TODO: Handle input file
			// refer https://github.com/Arize-ai/openinference/blob/586a6a0289541cf98845d2c7f8a9941912240ea7/python/instrumentation/openinference-instrumentation-openai/src/openinference/instrumentation/openai/_attributes/_responses_api.py#L81
		}
	}
	return attrs
}

func setOutputMsgAttrs(output *openai.ResponseOutputMessage, attrs []attribute.KeyValue, config *openinference.TraceConfig, index int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(index, openinference.MessageRole), output.Role))
	switch {
	case output.Content.OfContentArray != nil:
		for i, part := range output.Content.OfContentArray {
			switch {
			case part.OfOutputText != nil:
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "type"), "text"))
				if config.HideInputText {
					attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "text"), openinference.RedactedValue))
				} else {
					attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "text"), part.OfOutputText.Text))
				}
			case part.OfRefusal != nil:
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "type"), "text"))
				if config.HideInputText {
					attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "text"), openinference.RedactedValue))
				} else {
					attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, i, "text"), part.OfRefusal.Refusal))
				}
			}
		}
	case output.Content.OfString != nil:
		attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, 0, "type"), "text"))
		if config.HideInputText {
			attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, 0, "text"), openinference.RedactedValue))
		} else {
			attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(index, 0, "text"), *output.Content.OfString))
		}
	}
	return attrs
}

func setFileSearchCallAttrs(f *openai.ResponseFileSearchToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), f.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), f.Type))
	}
	return attrs
}

func setComputerCallAttrs(c *openai.ResponseComputerToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"),
		attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID))
	}
	return attrs
}

func setComputerCallOutputAttrs(c *openai.ResponseInputItemComputerCallOutputParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), c.CallID))
		// output is a screenshot - serialize to JSON
		if content, err := json.Marshal(c.Output); err == nil {
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), string(content)))
		}
	}
	return attrs
}

func setWebSearchCallAttrs(w *openai.ResponseFunctionWebSearch, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), w.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), w.Type))
	}
	return attrs
}

func setFunctionCallAttrs(f *openai.ResponseFunctionToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), f.CallID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), f.Name),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), f.Arguments),
		)
	}
	return attrs
}

func setFunctionCallOutputAttrs(f *openai.ResponseInputItemFunctionCallOutputParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), f.CallID))
		// output can be str or complex type - serialize complex types to JSON
		if content, err := json.Marshal(f.Output); err == nil {
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), string(content)))
		}
	}
	return attrs
}

func setReasoningAttrs(r *openai.ResponseReasoningItem, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	for i, summary := range r.Summary {
		if summary.Type == "summary_text" {
			if config.HideInputText {
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(messageIndex, i, "type"), "text"),
					attribute.String(openinference.InputMessageContentAttribute(messageIndex, i, "text"), openinference.RedactedValue))
			} else {
				attrs = append(attrs, attribute.String(openinference.InputMessageContentAttribute(messageIndex, i, "type"), "text"),
					attribute.String(openinference.InputMessageContentAttribute(messageIndex, i, "text"), summary.Text))
			}
		}
	}
	return attrs
}

func setCustomToolCallAttrs(c *openai.ResponseCustomToolCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Name),
		)
		if data, err := json.Marshal(map[string]string{"input": c.Input}); err == nil {
			attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), string(data)))
		}
	}
	return attrs
}

func setCustomToolCallOutputAttrs(c *openai.ResponseCustomToolCallOutputParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue),
		)
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), c.CallID))
		// output can be str or complex type - serialize complex types to JSON
		if content, err := json.Marshal(c.Output); err == nil {
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), string(content)))
		}
	}
	return attrs
}

func setInputCompactionAttrs(c *openai.ResponseCompactionItemParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
	}
	return attrs
}

func setInputImageGenerationCallAttrs(c *openai.ResponseInputItemImageGenerationCallParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
	}
	return attrs
}

func setInputCodeInterpreterCallAttrs(c *openai.ResponseCodeInterpreterToolCallParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), c.Code))
	}
	return attrs
}

func setInputLocalShellCallAttrs(c *openai.ResponseInputItemLocalShellCallParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
		if data, err := json.Marshal(c.Action.Command); err == nil {
			attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), string(data)))
		}
	}
	return attrs
}

func setInputLocalShellCallOutputAttrs(c *openai.ResponseInputItemLocalShellCallOutputParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), c.Output))
	}
	return attrs
}

func setInputShellCallAttrs(c *openai.ResponseInputItemShellCallParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
		if data, err := json.Marshal(c.Action.Commands); err == nil {
			attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), string(data)))
		}
	}
	return attrs
}

func setInputShellCallOutputAttrs(c *openai.ResponseInputItemShellCallOutputParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
	}
	return attrs
}

func setInputApplyPatchCallAttrs(c *openai.ResponseInputItemApplyPatchCallParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
	}
	return attrs
}

func setInputApplyPatchCallOutputAttrs(c *openai.ResponseInputItemApplyPatchCallOutputParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), c.CallID),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), c.Output))
	}
	return attrs
}

func setInputMcpListToolsAttrs(c *openai.ResponseMcpListTools, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Type))
	}
	return attrs
}

func setInputMcpApprovalRequestAttrs(c *openai.ResponseMcpApprovalRequest, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Name),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), c.Arguments))
	}
	return attrs
}

func setInputMcpApprovalResponseAttrs(c *openai.ResponseInputItemMcpApprovalResponseParam, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "tool"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.ToolCallID), c.ApprovalRequestID),
			attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageContent), c.Reason))
	}
	return attrs
}

func setInputMcpCallAttrs(c *openai.ResponseMcpCall, attrs []attribute.KeyValue, config *openinference.TraceConfig, messageIndex int) []attribute.KeyValue {
	attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(messageIndex, openinference.MessageRole), "assistant"))
	if config.HideInputText {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), openinference.RedactedValue),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), openinference.RedactedValue))
	} else {
		attrs = append(attrs, attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallID), c.ID),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionName), c.Name),
			attribute.String(openinference.InputMessageToolCallAttribute(messageIndex, 0, openinference.ToolCallFunctionArguments), c.Arguments))
	}
	return attrs
}
