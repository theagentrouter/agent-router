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
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"k8s.io/utils/ptr"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/awsbedrock"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewAnthropicToAWSBedrockTranslator creates a translator for Anthropic Messages API to AWS Bedrock Converse API.
func NewAnthropicToAWSBedrockTranslator(modelNameOverride internalapi.ModelNameOverride) AnthropicMessagesTranslator {
	return &anthropicToAWSBedrockTranslator{modelNameOverride: modelNameOverride}
}

type anthropicToAWSBedrockTranslator struct {
	modelNameOverride internalapi.ModelNameOverride
	requestModel      internalapi.RequestModel
	stream            bool
	bufferedBody      []byte
	events            []awsbedrock.ConverseStreamEvent
	role              string
	responseID        string
	// pendingBlockStart tracks whether we have a non-tool contentBlockStart that hasn't been
	// emitted yet. Bedrock doesn't distinguish text vs thinking blocks at start time, so we
	// defer emitting content_block_start until the first delta tells us the actual type.
	pendingBlockStart    bool
	pendingBlockStartIdx int
	// streamingUsage tracks the latest usage from metadata events for emission in SSE.
	streamingUsage *awsbedrock.TokenUsage
}

// RequestBody implements [AnthropicMessagesTranslator.RequestBody].
func (a *anthropicToAWSBedrockTranslator) RequestBody(_ []byte, body *anthropicschema.MessagesRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	a.stream = body.Stream
	a.requestModel = cmp.Or(a.modelNameOverride, body.Model)

	var pathTemplate string
	if body.Stream {
		pathTemplate = "/model/%s/converse-stream"
	} else {
		pathTemplate = "/model/%s/converse"
	}

	encodedModelName := url.PathEscape(a.requestModel)

	var bedrockReq awsbedrock.ConverseInput

	// Promote any role: "system" messages from the messages array to the top-level system field.
	// This handles clients (e.g. Claude Code with mid-conversation-system beta) that send
	// system prompts as messages rather than the top-level parameter.
	messages, systemPrompt := promoteAnthropicSystemMessagesToParam(body)

	// Convert messages.
	bedrockReq.Messages = make([]*awsbedrock.Message, 0, len(messages))
	msgLen := len(messages)
	for i := 0; i < msgLen; {
		msg := &messages[i]
		switch msg.Role {
		case anthropicschema.MessageRoleUser:
			if isOnlyToolResult(msg) {
				bedrockMsg := a.convertToolResultMessage(msg)
				// Coalesce consecutive tool-result-only messages.
				for i+1 < msgLen && isOnlyToolResult(&messages[i+1]) {
					nextMsg := &messages[i+1]
					nextBedrockMsg := a.convertToolResultMessage(nextMsg)
					bedrockMsg.Content = append(bedrockMsg.Content, nextBedrockMsg.Content...)
					i++
				}
				bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
			} else {
				bedrockMsg, convErr := a.convertUserMessage(msg)
				if convErr != nil {
					return nil, nil, convErr
				}
				bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
			}
			i++
		case anthropicschema.MessageRoleAssistant:
			bedrockReq.Messages = append(bedrockReq.Messages, a.convertAssistantMessage(msg))
			i++
		default:
			return nil, nil, fmt.Errorf("%w: unexpected role: %s", internalapi.ErrInvalidRequestBody, msg.Role)
		}
	}

	// Convert system prompt.
	if systemPrompt != nil {
		bedrockReq.System = a.convertSystemPrompt(systemPrompt)
	}

	// Convert inference configuration.
	bedrockReq.InferenceConfig = &awsbedrock.InferenceConfiguration{}
	maxTokens := int64(body.MaxTokens)
	bedrockReq.InferenceConfig.MaxTokens = &maxTokens
	bedrockReq.InferenceConfig.Temperature = body.Temperature
	bedrockReq.InferenceConfig.TopP = body.TopP
	if len(body.StopSequences) > 0 {
		bedrockReq.InferenceConfig.StopSequences = body.StopSequences
	}

	// top_k goes into additionalModelRequestFields.
	if body.TopK != nil {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = make(map[string]interface{})
		}
		bedrockReq.AdditionalModelRequestFields["top_k"] = *body.TopK
	}

	// Convert thinking config.
	if body.Thinking != nil {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = make(map[string]interface{})
		}
		if body.Thinking.Enabled != nil {
			bedrockReq.AdditionalModelRequestFields["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": body.Thinking.Enabled.BudgetTokens,
			}
		} else if body.Thinking.Disabled != nil {
			bedrockReq.AdditionalModelRequestFields["thinking"] = map[string]any{
				"type": "disabled",
			}
		}
	}

	// Convert tools.
	if len(body.Tools) > 0 {
		a.convertTools(body, &bedrockReq)
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

// promoteAnthropicSystemMessagesToParam promotes role: "system" messages from the messages
// array to the top-level system parameter. It returns the filtered messages (without the system
// messages) and the effective system prompt, which is nil when the request had none.
// This handles clients (e.g. Claude Code with mid-conversation-system beta) that send
// system prompts as messages rather than the top-level parameter.
//
// Neither body.Messages nor body.System is mutated: the same parsed body is translated again on
// every retry or backend fallback, so merging in place would re-append the promoted blocks. The
// returned prompt aliases body.System when there is nothing to promote, so treat it as read-only.
//
// Each system message becomes its own text block so per-block fields like cache_control survive
// promotion and convertSystemPrompt can emit the corresponding cache points.
func promoteAnthropicSystemMessagesToParam(body *anthropicschema.MessagesRequest) ([]anthropicschema.MessageParam, *anthropicschema.SystemPrompt) {
	var promoted []anthropicschema.TextBlockParam
	var filtered []anthropicschema.MessageParam
	for _, msg := range body.Messages {
		if msg.Role == "system" {
			if msg.Content.Text != "" {
				promoted = append(promoted, anthropicschema.TextBlockParam{Type: "text", Text: msg.Content.Text})
			}
			for i := range msg.Content.Array {
				// Copy the whole block, not just its text, so cache_control survives.
				if t := msg.Content.Array[i].Text; t != nil && t.Text != "" {
					promoted = append(promoted, *t)
				}
			}
		} else {
			filtered = append(filtered, msg)
		}
	}
	if len(promoted) == 0 {
		return filtered, body.System
	}

	// Keep any existing system prompt rather than replacing it: a request may legitimately set both.
	// Top-level blocks come first, then the promoted messages in conversation order.
	merged := &anthropicschema.SystemPrompt{}
	if body.System != nil {
		// The string form is folded into the block list because convertSystemPrompt returns early
		// on it, which would otherwise drop the promoted blocks.
		if body.System.Text != "" {
			merged.Texts = append(merged.Texts, anthropicschema.TextBlockParam{Type: "text", Text: body.System.Text})
		}
		merged.Texts = append(merged.Texts, body.System.Texts...)
	}
	merged.Texts = append(merged.Texts, promoted...)
	return filtered, merged
}

// isOnlyToolResult returns true if the message is a user message whose content
// array consists entirely of tool_result blocks. This is used to decide whether
// the message should go through the tool-result coalescing path.
// Mixed-content messages (e.g. text + tool_result) are handled by
// convertUserMessage, which preserves all block types.
func isOnlyToolResult(msg *anthropicschema.MessageParam) bool {
	if msg.Role != anthropicschema.MessageRoleUser {
		return false
	}
	// Simple text-only content is never a tool-result-only message.
	if msg.Content.Text != "" {
		return false
	}
	if len(msg.Content.Array) == 0 {
		return false
	}
	for i := range msg.Content.Array {
		if msg.Content.Array[i].ToolResult == nil {
			return false
		}
	}
	return true
}

// anthropicCachePoint returns a Bedrock cache point block when the Anthropic cache_control field
// asks for one, otherwise nil. Bedrock only has a single ephemeral cache point type, so the
// ephemeral TTL is not representable and is dropped.
func anthropicCachePoint(cacheControl *anthropicschema.CacheControl) *awsbedrock.CachePointBlock {
	if cacheControl == nil || cacheControl.Ephemeral == nil {
		return nil
	}
	return &awsbedrock.CachePointBlock{Type: "default"}
}

// appendCachePoint appends a standalone cache point content block when cacheControl asks for one.
// Bedrock's ContentBlock is a union, so the cache point cannot ride on the block it follows.
func appendCachePoint(blocks []*awsbedrock.ContentBlock, cacheControl *anthropicschema.CacheControl) []*awsbedrock.ContentBlock {
	if cachePoint := anthropicCachePoint(cacheControl); cachePoint != nil {
		return append(blocks, &awsbedrock.ContentBlock{CachePoint: cachePoint})
	}
	return blocks
}

func (a *anthropicToAWSBedrockTranslator) convertUserMessage(msg *anthropicschema.MessageParam) (*awsbedrock.Message, error) {
	bedrockMsg := &awsbedrock.Message{Role: awsbedrock.ConversationRoleUser}
	if msg.Content.Text != "" {
		bedrockMsg.Content = []*awsbedrock.ContentBlock{
			{Text: ptr.To(msg.Content.Text)},
		}
		return bedrockMsg, nil
	}
	bedrockMsg.Content = make([]*awsbedrock.ContentBlock, 0, len(msg.Content.Array))
	for i := range msg.Content.Array {
		block := &msg.Content.Array[i]
		switch {
		case block.Text != nil:
			bedrockMsg.Content = append(bedrockMsg.Content, &awsbedrock.ContentBlock{
				Text: ptr.To(block.Text.Text),
			})
			bedrockMsg.Content = appendCachePoint(bedrockMsg.Content, block.Text.CacheControl)
		case block.Image != nil:
			imgBlock, err := a.convertImageBlock(block.Image)
			if err != nil {
				return nil, err
			}
			bedrockMsg.Content = append(bedrockMsg.Content, imgBlock)
			bedrockMsg.Content = appendCachePoint(bedrockMsg.Content, block.Image.CacheControl)
		case block.ToolResult != nil:
			bedrockMsg.Content = append(bedrockMsg.Content, a.convertToolResultBlock(block.ToolResult))
			bedrockMsg.Content = appendCachePoint(bedrockMsg.Content, block.ToolResult.CacheControl)
		}
	}
	return bedrockMsg, nil
}

func (a *anthropicToAWSBedrockTranslator) convertAssistantMessage(msg *anthropicschema.MessageParam) *awsbedrock.Message {
	bedrockMsg := &awsbedrock.Message{Role: awsbedrock.ConversationRoleAssistant}
	if msg.Content.Text != "" {
		bedrockMsg.Content = []*awsbedrock.ContentBlock{
			{Text: ptr.To(msg.Content.Text)},
		}
		return bedrockMsg
	}
	bedrockMsg.Content = make([]*awsbedrock.ContentBlock, 0, len(msg.Content.Array))
	for i := range msg.Content.Array {
		block := &msg.Content.Array[i]
		switch {
		case block.Text != nil:
			bedrockMsg.Content = append(bedrockMsg.Content, &awsbedrock.ContentBlock{
				Text: ptr.To(block.Text.Text),
			})
			bedrockMsg.Content = appendCachePoint(bedrockMsg.Content, block.Text.CacheControl)
		case block.Thinking != nil:
			bedrockMsg.Content = append(bedrockMsg.Content, &awsbedrock.ContentBlock{
				ReasoningContent: &awsbedrock.ReasoningContentBlock{
					ReasoningText: &awsbedrock.ReasoningTextBlock{
						Text:      block.Thinking.Thinking,
						Signature: block.Thinking.Signature,
					},
				},
			})
		case block.ToolUse != nil:
			bedrockMsg.Content = append(bedrockMsg.Content, &awsbedrock.ContentBlock{
				ToolUse: &awsbedrock.ToolUseBlock{
					Name:      block.ToolUse.Name,
					ToolUseID: block.ToolUse.ID,
					Input:     block.ToolUse.Input,
				},
			})
			bedrockMsg.Content = appendCachePoint(bedrockMsg.Content, block.ToolUse.CacheControl)
		case block.RedactedThinking != nil:
			bedrockMsg.Content = append(bedrockMsg.Content, &awsbedrock.ContentBlock{
				ReasoningContent: &awsbedrock.ReasoningContentBlock{
					RedactedContent: []byte(block.RedactedThinking.Data),
				},
			})
		}
	}
	return bedrockMsg
}

func (a *anthropicToAWSBedrockTranslator) convertToolResultMessage(msg *anthropicschema.MessageParam) *awsbedrock.Message {
	bedrockMsg := &awsbedrock.Message{Role: awsbedrock.ConversationRoleUser}
	bedrockMsg.Content = make([]*awsbedrock.ContentBlock, 0)
	for i := range msg.Content.Array {
		block := &msg.Content.Array[i]
		if block.ToolResult != nil {
			bedrockMsg.Content = append(bedrockMsg.Content, a.convertToolResultBlock(block.ToolResult))
			bedrockMsg.Content = appendCachePoint(bedrockMsg.Content, block.ToolResult.CacheControl)
		}
	}
	return bedrockMsg
}

func (a *anthropicToAWSBedrockTranslator) convertToolResultBlock(tr *anthropicschema.ToolResultBlockParam) *awsbedrock.ContentBlock {
	toolResult := &awsbedrock.ToolResultBlock{
		ToolUseID: ptr.To(tr.ToolUseID),
	}
	if tr.IsError {
		toolResult.Status = ptr.To("error")
	}
	if tr.Content != nil {
		if tr.Content.Text != "" {
			toolResult.Content = []*awsbedrock.ToolResultContentBlock{
				{Text: ptr.To(tr.Content.Text)},
			}
		} else if len(tr.Content.Array) > 0 {
			toolResult.Content = make([]*awsbedrock.ToolResultContentBlock, 0, len(tr.Content.Array))
			for i := range tr.Content.Array {
				item := &tr.Content.Array[i]
				if item.Text != nil {
					toolResult.Content = append(toolResult.Content, &awsbedrock.ToolResultContentBlock{
						Text: ptr.To(item.Text.Text),
					})
				}
			}
		}
	}
	return &awsbedrock.ContentBlock{ToolResult: toolResult}
}

func (a *anthropicToAWSBedrockTranslator) convertImageBlock(img *anthropicschema.ImageBlockParam) (*awsbedrock.ContentBlock, error) {
	if img.Source.Base64 == nil {
		return nil, fmt.Errorf("%w: only base64 image sources are supported", internalapi.ErrInvalidRequestBody)
	}
	var format string
	switch img.Source.Base64.MediaType {
	case "image/jpeg":
		format = "jpeg"
	case "image/png":
		format = "png"
	case "image/gif":
		format = "gif"
	case "image/webp":
		format = "webp"
	default:
		return nil, fmt.Errorf("%w: unsupported image format %s", internalapi.ErrInvalidRequestBody, img.Source.Base64.MediaType)
	}
	// Decode base64 data.
	decoded, err := base64.StdEncoding.DecodeString(img.Source.Base64.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 image data: %w", err)
	}
	return &awsbedrock.ContentBlock{
		Image: &awsbedrock.ImageBlock{
			Format: format,
			Source: awsbedrock.ImageSource{
				Bytes: decoded,
			},
		},
	}, nil
}

func (a *anthropicToAWSBedrockTranslator) convertSystemPrompt(system *anthropicschema.SystemPrompt) []*awsbedrock.SystemContentBlock {
	if system.Text != "" {
		text := system.Text
		return []*awsbedrock.SystemContentBlock{{Text: &text}}
	}
	blocks := make([]*awsbedrock.SystemContentBlock, 0, len(system.Texts))
	for i := range system.Texts {
		text := system.Texts[i].Text
		blocks = append(blocks, &awsbedrock.SystemContentBlock{Text: &text})
		if cachePoint := anthropicCachePoint(system.Texts[i].CacheControl); cachePoint != nil {
			blocks = append(blocks, &awsbedrock.SystemContentBlock{CachePoint: cachePoint})
		}
	}
	return blocks
}

func (a *anthropicToAWSBedrockTranslator) convertTools(body *anthropicschema.MessagesRequest, bedrockReq *awsbedrock.ConverseInput) {
	bedrockReq.ToolConfig = &awsbedrock.ToolConfiguration{}
	tools := make([]*awsbedrock.Tool, 0, len(body.Tools))
	for i := range body.Tools {
		tu := &body.Tools[i]
		if tu.Tool != nil {
			toolName := tu.Tool.Name
			var toolDesc *string
			if tu.Tool.Description != "" {
				toolDesc = &tu.Tool.Description
			}
			tool := &awsbedrock.Tool{
				ToolSpec: &awsbedrock.ToolSpecification{
					Name:        &toolName,
					Description: toolDesc,
					InputSchema: &awsbedrock.ToolInputSchema{
						JSON: tu.Tool.InputSchema,
					},
				},
			}
			tools = append(tools, tool)
			// Bedrock's Tool is a union: an element sets either toolSpec or cachePoint, never both.
			if cachePoint := anthropicCachePoint(tu.Tool.CacheControl); cachePoint != nil {
				tools = append(tools, &awsbedrock.Tool{CachePoint: cachePoint})
			}
		}
	}
	bedrockReq.ToolConfig.Tools = tools

	if body.ToolChoice != nil {
		switch {
		case body.ToolChoice.Auto != nil:
			bedrockReq.ToolConfig.ToolChoice = &awsbedrock.ToolChoice{
				Auto: &awsbedrock.AutoToolChoice{},
			}
		case body.ToolChoice.Any != nil:
			bedrockReq.ToolConfig.ToolChoice = &awsbedrock.ToolChoice{
				Any: &awsbedrock.AnyToolChoice{},
			}
		case body.ToolChoice.Tool != nil:
			name := body.ToolChoice.Tool.Name
			bedrockReq.ToolConfig.ToolChoice = &awsbedrock.ToolChoice{
				Tool: &awsbedrock.SpecificToolChoice{
					Name: &name,
				},
			}
		case body.ToolChoice.None != nil:
			// Bedrock doesn't have "none" tool choice, skip.
		}
	}
}

// ResponseHeaders implements [AnthropicMessagesTranslator.ResponseHeaders].
func (a *anthropicToAWSBedrockTranslator) ResponseHeaders(headers map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	if a.stream {
		contentType := headers[contentTypeHeaderName]
		if contentType == "application/vnd.amazon.eventstream" {
			newHeaders = []internalapi.Header{{contentTypeHeaderName, "text/event-stream"}}
		}
	}
	a.responseID = headers["x-amzn-requestid"]
	return
}

// ResponseBody implements [AnthropicMessagesTranslator.ResponseBody].
func (a *anthropicToAWSBedrockTranslator) ResponseBody(_ map[string]string, body io.Reader, _ bool, span tracingapi.MessageSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel internalapi.ResponseModel, err error,
) {
	responseModel = a.requestModel
	if a.stream {
		newBody = make([]byte, 0)
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to read body: %w", err)
		}
		a.bufferedBody = append(a.bufferedBody, buf...)
		a.extractAmazonEventStreamEvents()

		// Pre-scan for metadata usage so it's available when emitting message_delta.
		for i := range a.events {
			if a.events[i].EventType == awsbedrock.ConverseStreamEventTypeMetadata.String() && a.events[i].Usage != nil {
				a.streamingUsage = a.events[i].Usage
			}
		}

		for i := range a.events {
			event := &a.events[i]
			if usage := event.Usage; usage != nil {
				tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(usage.InputTokens, usage.OutputTokens,
					usage.CacheReadInputTokens, usage.CacheWriteInputTokens)
			}
			a.convertEventToAnthropicSSE(event, &newBody)
		}
		return
	}

	var bedrockResp awsbedrock.ConverseResponse
	if err = json.NewDecoder(body).Decode(&bedrockResp); err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}

	anthropicResp := &anthropicschema.MessagesResponse{
		ID:    a.responseID,
		Type:  "message",
		Role:  "assistant",
		Model: a.requestModel,
	}

	// Convert stop reason.
	if bedrockResp.StopReason != nil {
		stopReason := a.bedrockStopReasonToAnthropicStopReason(*bedrockResp.StopReason)
		anthropicResp.StopReason = &stopReason
	}

	// Convert content blocks.
	if bedrockResp.Output != nil {
		anthropicResp.Content = make([]anthropicschema.MessagesContentBlock, 0)
		for _, block := range bedrockResp.Output.Message.Content {
			switch {
			case block.Text != nil:
				anthropicResp.Content = append(anthropicResp.Content, anthropicschema.MessagesContentBlock{
					Text: &anthropicschema.TextBlock{
						Type: "text",
						Text: *block.Text,
					},
				})
			case block.ToolUse != nil:
				anthropicResp.Content = append(anthropicResp.Content, anthropicschema.MessagesContentBlock{
					Tool: &anthropicschema.ToolUseBlock{
						Type:  "tool_use",
						ID:    block.ToolUse.ToolUseID,
						Name:  block.ToolUse.Name,
						Input: block.ToolUse.Input,
					},
				})
			case block.ReasoningContent != nil:
				if block.ReasoningContent.ReasoningText != nil {
					anthropicResp.Content = append(anthropicResp.Content, anthropicschema.MessagesContentBlock{
						Thinking: &anthropicschema.ThinkingBlock{
							Type:      "thinking",
							Thinking:  block.ReasoningContent.ReasoningText.Text,
							Signature: block.ReasoningContent.ReasoningText.Signature,
						},
					})
				} else if block.ReasoningContent.RedactedContent != nil {
					anthropicResp.Content = append(anthropicResp.Content, anthropicschema.MessagesContentBlock{
						RedactedThinking: &anthropicschema.RedactedThinkingBlock{
							Type: "redacted_thinking",
							Data: string(block.ReasoningContent.RedactedContent),
						},
					})
				}
			}
		}
	}

	// Convert usage.
	if bedrockResp.Usage != nil {
		tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(
			bedrockResp.Usage.InputTokens, bedrockResp.Usage.OutputTokens,
			bedrockResp.Usage.CacheReadInputTokens, bedrockResp.Usage.CacheWriteInputTokens,
		)
		anthropicResp.Usage = &anthropicschema.Usage{
			InputTokens:  float64(bedrockResp.Usage.InputTokens),
			OutputTokens: float64(bedrockResp.Usage.OutputTokens),
		}
		if bedrockResp.Usage.CacheReadInputTokens != nil {
			anthropicResp.Usage.CacheReadInputTokens = float64(*bedrockResp.Usage.CacheReadInputTokens)
		}
		if bedrockResp.Usage.CacheWriteInputTokens != nil {
			anthropicResp.Usage.CacheCreationInputTokens = float64(*bedrockResp.Usage.CacheWriteInputTokens)
		}
	}

	if span != nil {
		span.RecordResponse(anthropicResp)
	}

	newBody, err = json.Marshal(anthropicResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to marshal body: %w", err)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// extractAmazonEventStreamEvents extracts [awsbedrock.ConverseStreamEvent] from the buffered body.
func (a *anthropicToAWSBedrockTranslator) extractAmazonEventStreamEvents() {
	r := bytes.NewReader(a.bufferedBody)
	dec := eventstream.NewDecoder()
	clear(a.events)
	a.events = a.events[:0]
	var lastRead int64
	for {
		msg, err := dec.Decode(r, nil)
		if err != nil {
			a.bufferedBody = a.bufferedBody[lastRead:]
			return
		}
		var event awsbedrock.ConverseStreamEvent
		eventType := msg.Headers.Get(":event-type")
		if eventType != nil {
			event.EventType = eventType.String()
		}
		if err := json.Unmarshal(msg.Payload, &event); err == nil {
			a.events = append(a.events, event)
		}
		lastRead = r.Size() - int64(r.Len())
	}
}

func (a *anthropicToAWSBedrockTranslator) convertEventToAnthropicSSE(event *awsbedrock.ConverseStreamEvent, out *[]byte) {
	switch event.EventType {
	case awsbedrock.ConverseStreamEventTypeMessageStart.String():
		if event.Role == nil {
			return
		}
		a.role = *event.Role
		inputTokens := 0
		if a.streamingUsage != nil {
			inputTokens = int(a.streamingUsage.InputTokens)
		}
		msgStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            a.responseID,
				"type":          "message",
				"role":          a.role,
				"content":       []any{},
				"model":         a.requestModel,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  inputTokens,
					"output_tokens": 0,
				},
			},
		}
		a.writeSSEEvent("message_start", msgStart, out)

	case awsbedrock.ConverseStreamEventTypeContentBlockStart.String():
		if event.Start != nil && event.Start.ToolUse != nil {
			// Tool use blocks are immediately identifiable.
			cbStart := map[string]any{
				"type":  "content_block_start",
				"index": event.ContentBlockIndex,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    event.Start.ToolUse.ToolUseID,
					"name":  event.Start.ToolUse.Name,
					"input": map[string]any{},
				},
			}
			a.writeSSEEvent("content_block_start", cbStart, out)
		} else {
			// Bedrock doesn't distinguish text vs thinking at block start time,
			// so we defer emitting content_block_start until the first delta.
			a.pendingBlockStart = true
			a.pendingBlockStartIdx = event.ContentBlockIndex
		}

	case awsbedrock.ConverseStreamEventTypeContentBlockDelta.String():
		if event.Delta == nil {
			return
		}
		switch {
		case event.Delta.Text != nil:
			a.flushPendingBlockStart("text", out)
			cbDelta := map[string]any{
				"type":  "content_block_delta",
				"index": event.ContentBlockIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": *event.Delta.Text,
				},
			}
			a.writeSSEEvent("content_block_delta", cbDelta, out)
		case event.Delta.ToolUse != nil:
			cbDelta := map[string]any{
				"type":  "content_block_delta",
				"index": event.ContentBlockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": event.Delta.ToolUse.Input,
				},
			}
			a.writeSSEEvent("content_block_delta", cbDelta, out)
		case event.Delta.ReasoningContent != nil:
			a.flushPendingBlockStart("thinking", out)
			rc := event.Delta.ReasoningContent
			if rc.Text != "" {
				cbDelta := map[string]any{
					"type":  "content_block_delta",
					"index": event.ContentBlockIndex,
					"delta": map[string]any{
						"type":     "thinking_delta",
						"thinking": rc.Text,
					},
				}
				a.writeSSEEvent("content_block_delta", cbDelta, out)
			}
			if rc.Signature != "" {
				cbDelta := map[string]any{
					"type":  "content_block_delta",
					"index": event.ContentBlockIndex,
					"delta": map[string]any{
						"type":      "signature_delta",
						"signature": rc.Signature,
					},
				}
				a.writeSSEEvent("content_block_delta", cbDelta, out)
			}
		}

	case awsbedrock.ConverseStreamEventTypeContentBlockStop.String():
		cbStop := map[string]any{
			"type":  "content_block_stop",
			"index": event.ContentBlockIndex,
		}
		a.writeSSEEvent("content_block_stop", cbStop, out)

	case awsbedrock.ConverseStreamEventTypeMessageStop.String():
		if event.StopReason == nil {
			return
		}
		stopReason := a.bedrockStopReasonToAnthropicStopReason(*event.StopReason)
		outputTokens := 0
		if a.streamingUsage != nil {
			outputTokens = int(a.streamingUsage.OutputTokens)
		}
		msgDelta := map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   string(stopReason),
				"stop_sequence": nil,
			},
			"usage": map[string]any{
				"output_tokens": outputTokens,
			},
		}
		a.writeSSEEvent("message_delta", msgDelta, out)
		msgStop := map[string]any{
			"type": "message_stop",
		}
		a.writeSSEEvent("message_stop", msgStop, out)

	case awsbedrock.ConverseStreamEventTypeMetadata.String():
		if event.Usage != nil {
			a.streamingUsage = event.Usage
		}
	}
}

// flushPendingBlockStart emits a deferred content_block_start with the resolved block type.
func (a *anthropicToAWSBedrockTranslator) flushPendingBlockStart(blockType string, out *[]byte) {
	if !a.pendingBlockStart {
		return
	}
	a.pendingBlockStart = false
	contentBlock := map[string]any{"type": blockType}
	switch blockType {
	case "text":
		contentBlock["text"] = ""
	case "thinking":
		contentBlock["thinking"] = ""
	}
	cbStart := map[string]any{
		"type":          "content_block_start",
		"index":         a.pendingBlockStartIdx,
		"content_block": contentBlock,
	}
	a.writeSSEEvent("content_block_start", cbStart, out)
}

func (a *anthropicToAWSBedrockTranslator) writeSSEEvent(eventType string, data any, out *[]byte) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	*out = append(*out, sseEventPrefixSpace...)
	*out = append(*out, eventType...)
	*out = append(*out, '\n')
	*out = append(*out, sseDataPrefixSpace...)
	*out = append(*out, jsonData...)
	*out = append(*out, '\n', '\n')
}

func (a *anthropicToAWSBedrockTranslator) bedrockStopReasonToAnthropicStopReason(stopReason string) anthropicschema.StopReason {
	switch stopReason {
	case awsbedrock.StopReasonEndTurn:
		return anthropicschema.StopReasonEndTurn
	case awsbedrock.StopReasonMaxTokens:
		return anthropicschema.StopReasonMaxTokens
	case awsbedrock.StopReasonStopSequence:
		return anthropicschema.StopReasonStopSequence
	case awsbedrock.StopReasonToolUse:
		return anthropicschema.StopReasonToolUse
	case awsbedrock.StopReasonContentFiltered:
		return anthropicschema.StopReasonEndTurn // best effort
	default:
		return anthropicschema.StopReasonEndTurn
	}
}

// ResponseError implements [AnthropicMessagesTranslator.ResponseError].
func (a *anthropicToAWSBedrockTranslator) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, mutatedBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	var errorMessage string
	if v, ok := respHeaders[contentTypeHeaderName]; ok && strings.Contains(v, jsonContentType) {
		var bedrockError awsbedrock.BedrockException
		if err = json.NewDecoder(body).Decode(&bedrockError); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal error body: %w", err)
		}
		errorMessage = bedrockError.Message
	} else {
		var buf []byte
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read error body: %w", err)
		}
		errorMessage = string(buf)
	}
	anthropicError := anthropicschema.ErrorResponse{
		Type: "error",
		Error: anthropicschema.ErrorResponseMessage{
			Type:    httpStatusToAnthropicErrorType(statusCode),
			Message: errorMessage,
		},
	}
	mutatedBody, err = json.Marshal(anthropicError)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(mutatedBody))},
	}
	return
}
