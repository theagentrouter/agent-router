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
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewChatCompletionOpenAIToAWSBedrockTranslator implements [Factory] for OpenAI to AWS Bedrock translation.
func NewChatCompletionOpenAIToAWSBedrockTranslator(modelNameOverride internalapi.ModelNameOverride) OpenAIChatCompletionTranslator {
	return &openAIToAWSBedrockTranslatorV1ChatCompletion{modelNameOverride: modelNameOverride}
}

// openAIToAWSBedrockTranslator translates OpenAI Chat Completions API requests to AWS Bedrock Converse API.
// Note: This uses the Converse API directly, not Bedrock's OpenAI-compatible API:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html
type openAIToAWSBedrockTranslatorV1ChatCompletion struct {
	modelNameOverride internalapi.ModelNameOverride
	stream            bool
	bufferedBody      []byte
	events            []awsbedrock.ConverseStreamEvent
	// role is from MessageStartEvent in chunked messages, and used for all openai chat completion chunk choices.
	// Translator is created for each request/response stream inside external processor, accordingly the role is not reused by multiple streams.
	role             string
	requestModel     internalapi.RequestModel
	responseID       string
	toolIndex        int64
	activeToolStream bool
	// Redaction configuration for debug logging
	debugLogEnabled bool
	enableRedaction bool
	logger          *slog.Logger
}

func getAwsBedrockThinkingMap(tu *openai.ThinkingUnion) map[string]any {
	if tu == nil {
		return nil
	}

	resultMap := make(map[string]any)

	switch {
	case tu.OfEnabled != nil:
		reasoningConfigMap := map[string]any{
			"type":          "enabled",
			"budget_tokens": tu.OfEnabled.BudgetTokens,
		}
		if tu.OfEnabled.Display != "" {
			reasoningConfigMap["display"] = tu.OfEnabled.Display
		}
		resultMap["thinking"] = reasoningConfigMap
	case tu.OfDisabled != nil:
		reasoningConfigMap := map[string]any{
			"type": "disabled",
		}
		resultMap["thinking"] = reasoningConfigMap
	case tu.OfAdaptive != nil:
		reasoningConfigMap := map[string]any{
			"type": "adaptive",
		}
		if tu.OfAdaptive.Display != "" {
			reasoningConfigMap["display"] = tu.OfAdaptive.Display
		}
		resultMap["thinking"] = reasoningConfigMap
	}

	return resultMap
}

// getCachePoint returns a cache point block for AWS Bedrock if cache control is enabled, otherwise nil.
func getCachePoint(fields *openai.AnthropicContentFields) *awsbedrock.CachePointBlock {
	if isCacheEnabled(fields) {
		return &awsbedrock.CachePointBlock{
			Type: "default",
		}
	}
	return nil
}

// RequestBody implements [OpenAIChatCompletionTranslator.RequestBody].
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) RequestBody(_ []byte, openAIReq *openai.ChatCompletionRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	var pathTemplate string
	if openAIReq.Stream {
		o.stream = true
		pathTemplate = "/model/%s/converse-stream"
	} else {
		pathTemplate = "/model/%s/converse"
	}

	o.requestModel = openAIReq.Model
	if o.modelNameOverride != "" {
		// Use modelName override if set.
		o.requestModel = o.modelNameOverride
	}

	// URL encode the model name for the path to handle ARNs with special characters
	encodedModelName := url.PathEscape(o.requestModel)

	var bedrockReq awsbedrock.ConverseInput
	// Convert InferenceConfiguration.
	bedrockReq.InferenceConfig = &awsbedrock.InferenceConfiguration{}
	bedrockReq.InferenceConfig.Temperature = openAIReq.Temperature
	bedrockReq.InferenceConfig.TopP = openAIReq.TopP

	if openAIReq.ServiceTier != "" {
		bedrockReq.ServiceTier = &awsbedrock.ServiceTier{Type: openAIReq.ServiceTier}
	}

	bedrockReq.InferenceConfig.MaxTokens = cmp.Or(openAIReq.MaxCompletionTokens, openAIReq.MaxTokens)

	if openAIReq.Stop.OfString.Valid() {
		bedrockReq.InferenceConfig.StopSequences = []string{openAIReq.Stop.OfString.String()}
	} else if openAIReq.Stop.OfStringArray != nil {
		bedrockReq.InferenceConfig.StopSequences = openAIReq.Stop.OfStringArray
	}

	// Handle thinking config (for Anthropic models)
	if openAIReq.Thinking != nil {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = make(map[string]interface{})
		}
		bedrockReq.AdditionalModelRequestFields = getAwsBedrockThinkingMap(openAIReq.Thinking)
	}

	// Forward reasoning_effort as reasoning_config (for GLM, Nova, and other models)
	if openAIReq.ReasoningEffort != "" {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = make(map[string]interface{})
		}
		bedrockReq.AdditionalModelRequestFields["reasoning_config"] = string(openAIReq.ReasoningEffort)
	}

	// Convert Chat Completion messages.
	err = o.openAIMessageToBedrockMessage(openAIReq, &bedrockReq)
	if err != nil {
		return nil, nil, err
	}
	// Convert ToolConfiguration.
	if len(openAIReq.Tools) > 0 {
		bedrockReq.ToolConfig, err = openAIToolsToBedrockToolConfig(openAIReq.Tools, openAIReq.ToolChoice, openAIReq.Model)
		if err != nil {
			return nil, nil, err
		}
	}

	newBody, err = json.Marshal(bedrockReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{pathHeaderName, fmt.Sprintf(pathTemplate, encodedModelName)},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// openAIToolsToBedrockToolConfiguration converts openai ChatCompletion tools to aws bedrock tool configurations.
// openAIMessageToBedrockMessage converts openai ChatCompletion messages to aws bedrock messages.
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) openAIMessageToBedrockMessage(openAIReq *openai.ChatCompletionRequest,
	bedrockReq *awsbedrock.ConverseInput,
) error {
	// Convert Messages.
	bedrockReq.Messages = make([]*awsbedrock.Message, 0, len(openAIReq.Messages))
	openAIReqMessageLen, i := len(openAIReq.Messages), 0
	for i < openAIReqMessageLen {
		msg := &openAIReq.Messages[i]
		role := msg.ExtractMessgaeRole()
		switch {
		case msg.OfUser != nil:
			userMessage := msg.OfUser
			bedrockMessage, err := openAIMessageToBedrockMessageRoleUser(userMessage, role)
			if err != nil {
				return err
			}
			bedrockReq.Messages = append(bedrockReq.Messages, bedrockMessage)
		case msg.OfAssistant != nil:
			assistantMessage := msg.OfAssistant
			bedrockMessage, err := openAIMessageToBedrockMessageRoleAssistant(assistantMessage, role)
			if err != nil {
				return err
			}
			// Some clients, like OpenCode, can send assistant messages with nil or empty string content and no tool calls,
			// which would translate to an empty content array that Bedrock Converse rejects.
			if len(bedrockMessage.Content) > 0 {
				bedrockReq.Messages = append(bedrockReq.Messages, bedrockMessage)
			}
		case msg.OfSystem != nil:
			if bedrockReq.System == nil {
				bedrockReq.System = make([]*awsbedrock.SystemContentBlock, 0)
			}
			systemMessage := msg.OfSystem
			err := openAIMessageToBedrockMessageRoleSystem(systemMessage, &bedrockReq.System)
			if err != nil {
				return err
			}
		case msg.OfDeveloper != nil:
			message := msg.OfDeveloper
			if bedrockReq.System == nil {
				bedrockReq.System = []*awsbedrock.SystemContentBlock{}
			}

			if text, ok := message.Content.Value.(string); ok {
				bedrockReq.System = append(bedrockReq.System, &awsbedrock.SystemContentBlock{
					Text: &text,
				})
			} else {
				if contents, ok := message.Content.Value.([]openai.ChatCompletionContentPartTextParam); ok {
					for i := range contents {
						contentPart := &contents[i]
						textContentPart := contentPart.Text
						block := &awsbedrock.SystemContentBlock{
							Text: &textContentPart,
						}
						bedrockReq.System = append(bedrockReq.System, block)
						cacheBlock := getCachePoint(contentPart.AnthropicContentFields)
						if cacheBlock != nil {
							bedrockReq.System = append(bedrockReq.System, &awsbedrock.SystemContentBlock{
								CachePoint: cacheBlock,
							})
						}
					}
				} else {
					return fmt.Errorf("%w: unexpected content type for developer message", internalapi.ErrInvalidRequestBody)
				}
			}
		case msg.OfTool != nil:
			toolMessage := msg.OfTool
			// Bedrock does not support a tool role, merging to the user role.
			bedrockMessage, err := openAIMessageToBedrockMessageRoleTool(toolMessage, awsbedrock.ConversationRoleUser)
			if err != nil {
				return err
			}
			// Coalesce consecutive tool messages following a user message.
			for i+1 < openAIReqMessageLen {
				nextMessage := &openAIReq.Messages[i+1]
				if nextMessage.ExtractMessgaeRole() != openai.ChatMessageRoleTool {
					break
				}

				nextToolMessage := nextMessage.OfTool
				nextBedrockMessage, err := openAIMessageToBedrockMessageRoleTool(nextToolMessage, awsbedrock.ConversationRoleUser)
				if err != nil {
					return err
				}
				if len(nextBedrockMessage.Content) > 0 {
					bedrockMessage.Content = append(bedrockMessage.Content, nextBedrockMessage.Content[0])
				}
				i++
			}

			bedrockReq.Messages = append(bedrockReq.Messages, bedrockMessage)
		default:
			return fmt.Errorf("%w: unexpected role: %s", internalapi.ErrInvalidRequestBody, msg.ExtractMessgaeRole())
		}

		i++
	}
	return nil
}

// ResponseHeaders implements [OpenAIChatCompletionTranslator.ResponseHeaders].
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) ResponseHeaders(headers map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	if o.stream {
		contentType := headers["content-type"]
		if contentType == "application/vnd.amazon.eventstream" {
			// We need to change the content-type to text/event-stream for streaming responses.
			newHeaders = []internalapi.Header{{contentTypeHeaderName, "text/event-stream"}}
		}
	}
	o.responseID = headers["x-amzn-requestid"]
	return
}

func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) bedrockStopReasonToOpenAIStopReason(
	stopReason *string,
) openai.ChatCompletionChoicesFinishReason {
	if stopReason == nil {
		return openai.ChatCompletionChoicesFinishReasonStop
	}

	switch *stopReason {
	case awsbedrock.StopReasonStopSequence, awsbedrock.StopReasonEndTurn:
		return openai.ChatCompletionChoicesFinishReasonStop
	case awsbedrock.StopReasonMaxTokens:
		return openai.ChatCompletionChoicesFinishReasonLength
	case awsbedrock.StopReasonContentFiltered:
		return openai.ChatCompletionChoicesFinishReasonContentFilter
	case awsbedrock.StopReasonToolUse:
		return openai.ChatCompletionChoicesFinishReasonToolCalls
	default:
		return openai.ChatCompletionChoicesFinishReasonStop
	}
}

func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) bedrockToolUseToOpenAICalls(
	toolUse *awsbedrock.ToolUseBlock,
) *openai.ChatCompletionMessageToolCallParam {
	if toolUse == nil {
		return nil
	}
	arguments, err := json.Marshal(toolUse.Input)
	if err != nil {
		return nil
	}
	return &openai.ChatCompletionMessageToolCallParam{
		ID: &toolUse.ToolUseID,
		Function: openai.ChatCompletionMessageToolCallFunctionParam{
			Name:      toolUse.Name,
			Arguments: string(arguments),
		},
		Type: openai.ChatCompletionMessageToolCallTypeFunction,
	}
}

// ResponseError implements [OpenAIChatCompletionTranslator.ResponseError].
// Translate AWS Bedrock exceptions to OpenAI error type.
// The error type is stored in the "x-amzn-errortype" HTTP header for AWS error responses.
// If AWS Bedrock connection fails the error body is translated to OpenAI error type for events such as HTTP 503 or 504.
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	return bedrockResponseError(respHeaders, body)
}

// ResponseBody implements [OpenAIChatCompletionTranslator.ResponseBody].
// AWS Bedrock uses static model execution without virtualization, where the requested model
// is exactly what gets executed. The response does not contain a model field, so we return
// the request model that was originally sent.
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, span tracingapi.ChatCompletionSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	responseModel = o.requestModel
	if o.stream {
		newBody = make([]byte, 0)
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to read body: %w", err)
		}
		o.bufferedBody = append(o.bufferedBody, buf...)
		o.extractAmazonEventStreamEvents()

		for i := range o.events {
			event := &o.events[i]
			if usage := event.Usage; usage != nil {
				tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(usage.InputTokens, usage.OutputTokens,
					usage.CacheReadInputTokens, usage.CacheWriteInputTokens)
			}
			oaiEvent, ok := o.convertEvent(event)
			if !ok {
				continue
			}
			err = serializeOpenAIChatCompletionChunk(oaiEvent, &newBody)
			if err != nil {
				return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to marshal streaming event: %w", err)
			}
			if span != nil {
				span.RecordResponseChunk(oaiEvent)
			}
		}

		if endOfStream {
			newBody = append(newBody, sseDoneFullLine...)
		}
		return
	}

	var bedrockResp awsbedrock.ConverseResponse
	if err = json.NewDecoder(body).Decode(&bedrockResp); err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	// Bedrock can return HTTP 200 with a body that has no "output" field
	// (e.g. Coral framework errors like UnknownOperationException, guardrail
	// interventions, or empty/null responses). Guard the dereference so we
	// surface a real error to the caller instead of panicking with a nil
	// pointer dereference on bedrockResp.Output.Message below.
	if bedrockResp.Output == nil {
		return nil, nil, metrics.TokenUsage{}, responseModel, fmt.Errorf(
			"bedrock response missing 'output' (stopReason=%q)",
			ptr.Deref(bedrockResp.StopReason, ""),
		)
	}
	openAIResp := &openai.ChatCompletionResponse{
		// We use request model as response model since bedrock does not return the modelName in the response.
		Model:   o.requestModel,
		Object:  "chat.completion",
		Created: openai.JSONUNIXTime(time.Now()),
		Choices: make([]openai.ChatCompletionResponseChoice, 0),
		ID:      o.responseID,
	}

	if bedrockResp.ServiceTier != nil {
		openAIResp.ServiceTier = bedrockResp.ServiceTier.Type
	}

	// Convert token usage.
	if bedrockResp.Usage != nil {
		tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(bedrockResp.Usage.InputTokens, bedrockResp.Usage.OutputTokens,
			bedrockResp.Usage.CacheReadInputTokens, bedrockResp.Usage.CacheWriteInputTokens)
		totalTokens, _ := tokenUsage.TotalTokens()
		inputTokens, _ := tokenUsage.InputTokens()
		outputTokens, _ := tokenUsage.OutputTokens()
		openAIResp.Usage = openai.Usage{
			TotalTokens:      int(totalTokens),
			PromptTokens:     int(inputTokens),
			CompletionTokens: int(outputTokens),
		}
		if bedrockResp.Usage.CacheReadInputTokens != nil || bedrockResp.Usage.CacheWriteInputTokens != nil {
			openAIResp.Usage.PromptTokensDetails = &openai.PromptTokensDetails{}
		}
		if bedrockResp.Usage.CacheReadInputTokens != nil {
			tokenUsage.SetCachedInputTokens(uint32(*bedrockResp.Usage.CacheReadInputTokens)) //nolint:gosec
			openAIResp.Usage.PromptTokensDetails.CachedTokens = int(*bedrockResp.Usage.CacheReadInputTokens)
		}
		if bedrockResp.Usage.CacheWriteInputTokens != nil {
			tokenUsage.SetCacheCreationInputTokens(uint32(*bedrockResp.Usage.CacheWriteInputTokens)) //nolint:gosec
			openAIResp.Usage.PromptTokensDetails.CacheCreationTokens = int(*bedrockResp.Usage.CacheWriteInputTokens)
		}
	}

	// AWS Bedrock Converse API does not support N(multiple choices) > 0, so there could be only one choice.
	choice := openai.ChatCompletionResponseChoice{
		Index: int64(0),
		Message: openai.ChatCompletionResponseChoiceMessage{
			Role: bedrockResp.Output.Message.Role,
		},
		FinishReason: o.bedrockStopReasonToOpenAIStopReason(bedrockResp.StopReason),
	}

	for _, output := range bedrockResp.Output.Message.Content {
		// The AWS Content Block data type is a UNION,
		// so only one of the members can be specified when used or returned.
		// see: https: //docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ContentBlock.html
		switch {
		case output.ToolUse != nil:
			toolCall := o.bedrockToolUseToOpenAICalls(output.ToolUse)
			choice.Message.ToolCalls = append(choice.Message.ToolCalls, *toolCall)
		case output.Text != nil:
			// We expect only one text content block in the response.
			if choice.Message.Content == nil {
				choice.Message.Content = output.Text
			}
		case output.ReasoningContent != nil:
			// Reasoning text goes to the plain-string reasoning_content; signature and
			// redacted content go to thinking_blocks (they cannot be represented in a
			// plain string). This mirrors the streaming path and the Gemini helper.
			if rt := output.ReasoningContent.ReasoningText; rt != nil {
				// Only set reasoning_content when there is actual text; a
				// signature-only block must not emit an empty string.
				if rt.Text != "" {
					choice.Message.ReasoningContent = &openai.ReasoningContentUnion{Value: rt.Text}
				}
				// Surface the block (text and/or signature) so a signature still
				// round-trips even without accompanying text.
				if rt.Text != "" || rt.Signature != "" {
					choice.Message.ThinkingBlocks = append(choice.Message.ThinkingBlocks, openai.ThinkingBlock{
						Type: "thinking", Thinking: rt.Text, Signature: rt.Signature,
					})
				}
			}
			if rc := output.ReasoningContent.RedactedContent; rc != nil {
				choice.Message.ThinkingBlocks = append(choice.Message.ThinkingBlocks, openai.ThinkingBlock{
					Type: "redacted_thinking", Data: base64.StdEncoding.EncodeToString(rc),
				})
			}
		}
	}
	openAIResp.Choices = append(openAIResp.Choices, choice)

	// Redact and log response when enabled
	if o.debugLogEnabled && o.enableRedaction && o.logger != nil {
		redactedResp := o.RedactBody(openAIResp)
		if jsonBody, marshalErr := json.Marshal(redactedResp); marshalErr == nil {
			o.logger.Debug("response body processing", slog.Any("response", string(jsonBody)))
		}
	}

	newBody, err = json.Marshal(openAIResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to marshal body: %w", err)
	}
	if span != nil {
		span.RecordResponse(openAIResp)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// extractAmazonEventStreamEvents extracts [awsbedrock.ConverseStreamEvent] from the buffered body.
// The extracted events are stored in the processor's events field.
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) extractAmazonEventStreamEvents() {
	// TODO: Maybe reuse the reader and decoder.
	r := bytes.NewReader(o.bufferedBody)
	dec := eventstream.NewDecoder()
	clear(o.events)
	o.events = o.events[:0]
	var lastRead int64
	for {
		msg, err := dec.Decode(r, nil)
		if err != nil {
			o.bufferedBody = o.bufferedBody[lastRead:]
			return
		}
		var event awsbedrock.ConverseStreamEvent
		eventType := msg.Headers.Get(":event-type")
		if eventType != nil {
			event.EventType = eventType.String()
		}
		if err := json.Unmarshal(msg.Payload, &event); err == nil {
			o.events = append(o.events, event)
		}
		lastRead = r.Size() - int64(r.Len())
	}
}

var emptyString = ""

// convertEvent converts an [awsbedrock.ConverseStreamEvent] to an [openai.ChatCompletionResponseChunk].
// This is a static method and does not require a receiver, but defined as a method for namespacing.
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) convertEvent(event *awsbedrock.ConverseStreamEvent) (*openai.ChatCompletionResponseChunk, bool) {
	const object = "chat.completion.chunk"
	chunk := &openai.ChatCompletionResponseChunk{
		Object: object, Model: o.requestModel, ID: o.responseID,
		Created: openai.JSONUNIXTime(time.Now()),
		Choices: []openai.ChatCompletionResponseChunkChoice{},
	}

	switch event.EventType {
	// Usage event.
	case awsbedrock.ConverseStreamEventTypeMetadata.String():
		if event.ServiceTier != nil {
			chunk.ServiceTier = event.ServiceTier.Type
		}

		if event.Usage == nil {
			return chunk, false
		}
		tokenUsage := metrics.ExtractTokenUsageFromExplicitCaching(event.Usage.InputTokens, event.Usage.OutputTokens,
			event.Usage.CacheReadInputTokens, event.Usage.CacheWriteInputTokens)
		totalTokens, _ := tokenUsage.TotalTokens()
		inputTokens, _ := tokenUsage.InputTokens()
		outputTokens, _ := tokenUsage.OutputTokens()
		chunk.Usage = &openai.Usage{
			TotalTokens:      int(totalTokens),
			PromptTokens:     int(inputTokens),
			CompletionTokens: int(outputTokens),
		}
		if event.Usage.CacheReadInputTokens != nil || event.Usage.CacheWriteInputTokens != nil {
			chunk.Usage.PromptTokensDetails = &openai.PromptTokensDetails{}
		}
		if event.Usage.CacheReadInputTokens != nil {
			chunk.Usage.PromptTokensDetails.CachedTokens = int(*event.Usage.CacheReadInputTokens)
		}
		if event.Usage.CacheWriteInputTokens != nil {
			chunk.Usage.PromptTokensDetails.CacheCreationTokens = int(*event.Usage.CacheWriteInputTokens)
		}
	// messageStart event.
	case awsbedrock.ConverseStreamEventTypeMessageStart.String():
		if event.Role == nil {
			return chunk, false
		}
		chunk.Choices = append(chunk.Choices, openai.ChatCompletionResponseChunkChoice{
			Index: 0,
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				Role:    *event.Role,
				Content: &emptyString,
			},
		})
		o.role = *event.Role
	// contentBlockDelta event.
	case awsbedrock.ConverseStreamEventTypeContentBlockDelta.String():
		if event.Delta == nil {
			return chunk, false
		}
		switch {
		case event.Delta.Text != nil:
			chunk.Choices = append(chunk.Choices, openai.ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
					Role:    o.role,
					Content: event.Delta.Text,
				},
			})
		case event.Delta.ToolUse != nil:
			chunk.Choices = append(chunk.Choices, openai.ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
					Role: o.role,
					ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
						{
							Function: openai.ChatCompletionMessageToolCallFunctionParam{
								Arguments: event.Delta.ToolUse.Input,
							},
							Type:  openai.ChatCompletionMessageToolCallTypeFunction,
							Index: o.toolIndex,
						},
					},
				},
			})
		case event.Delta.ReasoningContent != nil:
			delta := &openai.ChatCompletionResponseChunkChoiceDelta{Role: o.role}

			// Reasoning text goes to the plain-string reasoning_content; signatures and
			// redacted content go to thinking_blocks (they cannot be represented in a
			// plain string).
			if event.Delta.ReasoningContent.Text != "" {
				delta.ReasoningContent = &openai.StreamReasoningContent{
					Text: event.Delta.ReasoningContent.Text,
				}
			}
			if sig := event.Delta.ReasoningContent.Signature; sig != "" {
				delta.ThinkingBlocks = append(delta.ThinkingBlocks, openai.ThinkingBlock{
					Type: "thinking", Signature: sig,
				})
			}
			if rc := event.Delta.ReasoningContent.RedactedContent; rc != nil {
				delta.ThinkingBlocks = append(delta.ThinkingBlocks, openai.ThinkingBlock{
					Type: "redacted_thinking", Data: base64.StdEncoding.EncodeToString(rc),
				})
			}

			chunk.Choices = append(chunk.Choices, openai.ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: delta,
			})
		}
	// contentBlockStart event.
	case awsbedrock.ConverseStreamEventTypeContentBlockStart.String():
		if event.Start == nil {
			return chunk, false
		}
		if event.Start.ToolUse != nil {
			o.activeToolStream = true
			chunk.Choices = append(chunk.Choices, openai.ChatCompletionResponseChunkChoice{
				Index: 0,
				Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
					Role: o.role,
					ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
						{
							ID: &event.Start.ToolUse.ToolUseID,
							Function: openai.ChatCompletionMessageToolCallFunctionParam{
								Name: event.Start.ToolUse.Name,
							},
							Type:  openai.ChatCompletionMessageToolCallTypeFunction,
							Index: o.toolIndex,
						},
					},
				},
			})
		}
	// MessageStop event.
	case awsbedrock.ConverseStreamEventTypeMessageStop.String():
		if event.StopReason == nil {
			return chunk, false
		}
		chunk.Choices = append(chunk.Choices, openai.ChatCompletionResponseChunkChoice{
			Index: 0,
			Delta: &openai.ChatCompletionResponseChunkChoiceDelta{
				Role:    o.role,
				Content: ptr.To(emptyString),
			},
			FinishReason: o.bedrockStopReasonToOpenAIStopReason(event.StopReason),
		})
	case awsbedrock.ConverseStreamEventTypeContentBlockStop.String():
		// this is the content stop event if none of the above is set.
		if o.activeToolStream {
			o.toolIndex++
			o.activeToolStream = false
		}
		return chunk, false
	default:
		return chunk, false
	}
	return chunk, true
}

// SetRedactionConfig implements [ResponseRedactor.SetRedactionConfig].
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger) {
	o.debugLogEnabled = debugLogEnabled
	o.enableRedaction = enableRedaction
	o.logger = logger
}

// RedactBody implements [ResponseRedactor.RedactBody].
// Creates a redacted copy of the response for safe logging without modifying the original.
// Reuses the same redaction logic since AWS Bedrock responses are converted to OpenAI format.
func (o *openAIToAWSBedrockTranslatorV1ChatCompletion) RedactBody(resp *openai.ChatCompletionResponse) *openai.ChatCompletionResponse {
	if resp == nil {
		return nil
	}

	// Create a shallow copy of the response
	redacted := *resp

	// Redact choices (contains AI-generated content)
	if len(resp.Choices) > 0 {
		redacted.Choices = make([]openai.ChatCompletionResponseChoice, len(resp.Choices))
		for i := range resp.Choices {
			redactedChoice := resp.Choices[i]
			redactedChoice.Message = redactAWSBedrockResponseMessage(&resp.Choices[i].Message)
			redacted.Choices[i] = redactedChoice
		}
	}

	return &redacted
}

// redactAWSBedrockResponseMessage redacts sensitive content from an AWS Bedrock response message
// that has been converted to OpenAI format.
func redactAWSBedrockResponseMessage(msg *openai.ChatCompletionResponseChoiceMessage) openai.ChatCompletionResponseChoiceMessage {
	redactedMsg := *msg

	// Redact message content (AI-generated text)
	if msg.Content != nil {
		redactedContent := redaction.RedactString(*msg.Content)
		redactedMsg.Content = &redactedContent
	}

	// Redact tool call arguments (may contain data derived from user messages).
	// Function name is kept — it is the tool API name, not user data.
	if len(msg.ToolCalls) > 0 {
		redactedMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			redactedToolCall := tc
			redactedToolCall.Function.Arguments = redaction.RedactString(tc.Function.Arguments)
			redactedMsg.ToolCalls[i] = redactedToolCall
		}
	}

	// Redact audio data if present
	if msg.Audio != nil {
		redactedAudio := *msg.Audio
		redactedAudio.Data = redaction.RedactString(msg.Audio.Data)
		redactedAudio.Transcript = redaction.RedactString(msg.Audio.Transcript)
		redactedMsg.Audio = &redactedAudio
	}

	// Redact reasoning content if present (AWS Bedrock thinking blocks)
	if msg.ReasoningContent != nil {
		redactedMsg.ReasoningContent = redactReasoningContent(msg.ReasoningContent)
	}

	return redactedMsg
}
