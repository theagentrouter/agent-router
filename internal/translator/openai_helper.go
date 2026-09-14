// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"fmt"
	"strings"

	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

// The following are helper functions for creating an OpenAI ChatCompletionRequest from an Anthropic MessagesRequest

// buildOpenAIChatCompletionRequest converts an Anthropic MessagesRequest into an OpenAI ChatCompletionRequest.
// It handles model override, system prompts, message conversion, tools, and tool choice.
func buildOpenAIChatCompletionRequest(body *anthropic.MessagesRequest, modelNameOverride internalapi.ModelNameOverride) *openai.ChatCompletionRequest {
	model := cmp.Or(modelNameOverride, body.Model)
	messages := anthropicMessagesToOpenAI(body)
	tools := anthropicToolsToOpenAI(body.Tools)
	var toolChoiceVal anthropic.ToolChoice
	if body.ToolChoice != nil {
		toolChoiceVal = *body.ToolChoice
	}
	toolChoice := anthropicToolChoiceToOpenAI(toolChoiceVal, len(tools) > 0)

	maxTokens := int64(body.MaxTokens)
	req := &openai.ChatCompletionRequest{
		Model:               model,
		Messages:            messages,
		MaxCompletionTokens: &maxTokens,
		Temperature:         body.Temperature,
		TopP:                body.TopP,
		Stream:              body.Stream,
	}

	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = toolChoice
	}

	if body.Stream {
		req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	// Map Anthropic thinking config to OpenAI thinking config.
	if body.Thinking != nil {
		req.Thinking = anthropicThinkingToOpenAI(body.Thinking)
	}

	return req
}

// anthropicThinkingToOpenAI converts an Anthropic Thinking config to OpenAI ThinkingUnion.
func anthropicThinkingToOpenAI(t *anthropic.Thinking) *openai.ThinkingUnion {
	if t == nil {
		return nil
	}
	switch {
	case t.Enabled != nil:
		return &openai.ThinkingUnion{OfEnabled: &openai.ThinkingEnabled{
			Type:         "enabled",
			BudgetTokens: int64(t.Enabled.BudgetTokens),
		}}
	case t.Disabled != nil:
		return &openai.ThinkingUnion{OfDisabled: &openai.ThinkingDisabled{
			Type: "disabled",
		}}
	case t.Adaptive != nil:
		return &openai.ThinkingUnion{OfAdaptive: &openai.ThinkingAdaptive{
			Type: "adaptive",
		}}
	default:
		return nil
	}
}

// anthropicMessagesToOpenAI converts Anthropic messages (including the system prompt) to OpenAI message format.
// It preserves tool_use blocks in assistant messages as OpenAI tool_calls, and converts
// tool_result blocks in user messages into separate OpenAI tool-role messages.
func anthropicMessagesToOpenAI(body *anthropic.MessagesRequest) []openai.ChatCompletionMessageParamUnion {
	var messages []openai.ChatCompletionMessageParamUnion

	// Prepend the system prompt as an OpenAI system message.
	if body.System != nil {
		systemText := anthropicSystemPromptToText(body.System)
		if systemText != "" {
			messages = append(messages, openai.ChatCompletionMessageParamUnion{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Content: openai.ContentUnion{Value: systemText},
					Role:    openai.ChatMessageRoleSystem,
				},
			})
		}
	}

	for _, msg := range body.Messages {
		switch msg.Role {
		case anthropic.MessageRoleUser:
			messages = appendAnthropicUserMessage(messages, msg)
		case anthropic.MessageRoleAssistant:
			messages = appendAnthropicAssistantMessage(messages, msg)
		}
	}

	return messages
}

// appendAnthropicAssistantMessage converts an Anthropic assistant message to OpenAI format.
// It extracts tool_use blocks as OpenAI tool_calls on the assistant message, and preserves
// text, thinking, and redacted_thinking content blocks.
//
// When thinking or redacted_thinking blocks are present, the content is encoded as a
// structured []ChatCompletionAssistantMessageParamContent array to preserve full fidelity.
// Otherwise, plain string content is used for backward compatibility.
func appendAnthropicAssistantMessage(messages []openai.ChatCompletionMessageParamUnion, msg anthropic.MessageParam) []openai.ChatCompletionMessageParamUnion {
	var toolCalls []openai.ChatCompletionMessageToolCallParam
	var contentParts []openai.ChatCompletionAssistantMessageParamContent
	hasThinking := false

	for _, block := range msg.Content.Array {
		switch {
		case block.Thinking != nil:
			hasThinking = true
			thinking := block.Thinking.Thinking
			signature := block.Thinking.Signature
			contentParts = append(contentParts, openai.ChatCompletionAssistantMessageParamContent{
				Type:      openai.ChatCompletionAssistantMessageParamContentTypeThinking,
				Text:      &thinking,
				Signature: &signature,
			})
		case block.RedactedThinking != nil:
			hasThinking = true
			contentParts = append(contentParts, openai.ChatCompletionAssistantMessageParamContent{
				Type:            openai.ChatCompletionAssistantMessageParamContentTypeRedactedThinking,
				RedactedContent: &openai.RedactedContentUnion{Value: block.RedactedThinking.Data},
			})
		case block.Text != nil:
			text := block.Text.Text
			contentParts = append(contentParts, openai.ChatCompletionAssistantMessageParamContent{
				Type: openai.ChatCompletionAssistantMessageParamContentTypeText,
				Text: &text,
			})
		case block.ToolUse != nil:
			args, _ := json.Marshal(block.ToolUse.Input)
			if args == nil {
				args = []byte("{}")
			}
			id := block.ToolUse.ID
			toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
				ID:   &id,
				Type: openai.ChatCompletionMessageToolCallTypeFunction,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      block.ToolUse.Name,
					Arguments: string(args),
				},
			})
		}
	}

	assistantMsg := &openai.ChatCompletionAssistantMessageParam{
		Role: openai.ChatMessageRoleAssistant,
	}

	if hasThinking {
		// Use structured content array to preserve thinking blocks.
		assistantMsg.Content = openai.StringOrAssistantRoleContentUnion{Value: contentParts}
	} else {
		// No thinking blocks — use plain string for backward compatibility.
		text := anthropicContentToText(msg.Content)
		if text != "" {
			assistantMsg.Content = openai.StringOrAssistantRoleContentUnion{Value: text}
		}
	}

	if len(toolCalls) > 0 {
		assistantMsg.ToolCalls = toolCalls
	}
	return append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: assistantMsg})
}

// appendAnthropicUserMessage converts an Anthropic user message to OpenAI format.
// It splits tool_result blocks into separate OpenAI tool-role messages and keeps
// text and image content as a user message. When image blocks are present,
// structured content parts are used instead of plain string content.
func appendAnthropicUserMessage(messages []openai.ChatCompletionMessageParamUnion, msg anthropic.MessageParam) []openai.ChatCompletionMessageParamUnion {
	// Emit tool-role messages for each tool_result block first, since OpenAI
	// expects tool results to immediately follow the assistant message that
	// generated the tool calls.
	for _, block := range msg.Content.Array {
		if block.ToolResult == nil {
			continue
		}
		content := toolResultToText(block.ToolResult)
		messages = append(messages, openai.ChatCompletionMessageParamUnion{
			OfTool: &openai.ChatCompletionToolMessageParam{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: block.ToolResult.ToolUseID,
				Content:    openai.ContentUnion{Value: content},
			},
		})
	}

	// Build structured content parts in a single pass. If image blocks are present,
	// use the structured array format; otherwise, fall back to plain string for compatibility.
	var parts []openai.ChatCompletionContentPartUserUnionParam
	hasImages := false
	for _, block := range msg.Content.Array {
		switch {
		case block.Text != nil:
			parts = append(parts, openai.ChatCompletionContentPartUserUnionParam{
				OfText: &openai.ChatCompletionContentPartTextParam{
					Type: string(openai.ChatCompletionContentPartTextTypeText),
					Text: block.Text.Text,
				},
			})
		case block.Image != nil:
			hasImages = true
			url := anthropicImageSourceToURL(block.Image.Source)
			parts = append(parts, openai.ChatCompletionContentPartUserUnionParam{
				OfImageURL: &openai.ChatCompletionContentPartImageParam{
					Type: openai.ChatCompletionContentPartImageTypeImageURL,
					ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
						URL: url,
					},
				},
			})
		}
	}
	if hasImages {
		if len(parts) > 0 {
			messages = append(messages, openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.StringOrUserRoleContentUnion{Value: parts},
					Role:    openai.ChatMessageRoleUser,
				},
			})
		}
	} else {
		text := anthropicContentToText(msg.Content)
		if text != "" {
			messages = append(messages, openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.StringOrUserRoleContentUnion{Value: text},
					Role:    openai.ChatMessageRoleUser,
				},
			})
		}
	}

	return messages
}

// anthropicImageSourceToURL converts an Anthropic ImageSource to an OpenAI-compatible URL.
func anthropicImageSourceToURL(source anthropic.ImageSource) string {
	switch {
	case source.Base64 != nil:
		return fmt.Sprintf("data:%s;base64,%s", source.Base64.MediaType, source.Base64.Data)
	case source.URL != nil:
		return source.URL.URL
	default:
		return ""
	}
}

// toolResultToText extracts text from a ToolResultBlockParam.
func toolResultToText(tr *anthropic.ToolResultBlockParam) string {
	if tr.Content == nil {
		return ""
	}
	if tr.Content.Text != "" {
		return tr.Content.Text
	}
	var sb strings.Builder
	for _, item := range tr.Content.Array {
		if item.Text != nil {
			sb.WriteString(item.Text.Text)
		}
	}
	return sb.String()
}

// anthropicSystemPromptToText extracts a plain string from an Anthropic system prompt,
// concatenating text blocks if the prompt is in array form.
func anthropicSystemPromptToText(s *anthropic.SystemPrompt) string {
	if s.Text != "" {
		return s.Text
	}
	var sb strings.Builder
	for _, t := range s.Texts {
		sb.WriteString(t.Text)
	}
	return sb.String()
}

// anthropicContentToText extracts a plain text string from Anthropic message content.
// For array content, text blocks are concatenated in order.
func anthropicContentToText(content anthropic.MessageContent) string {
	if content.Text != "" {
		return content.Text
	}
	var sb strings.Builder
	for _, block := range content.Array {
		if block.Text != nil {
			sb.WriteString(block.Text.Text)
		}
	}
	return sb.String()
}

// anthropicToolsToOpenAI converts Anthropic custom tools to OpenAI function tools.
// Only ToolUnion entries with a custom Tool variant are converted; built-in tool types are skipped.
func anthropicToolsToOpenAI(tools []anthropic.ToolUnion) []openai.Tool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Tool == nil {
			continue
		}
		result = append(result, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Tool.Name,
				Description: t.Tool.Description,
				Parameters:  t.Tool.InputSchema,
			},
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// anthropicToolChoiceToOpenAI converts an Anthropic tool_choice value to an OpenAI ChatCompletionToolChoiceUnion.
// Returns nil if no tools are present or the tool choice has no variant set.
func anthropicToolChoiceToOpenAI(tc anthropic.ToolChoice, hasTools bool) *openai.ChatCompletionToolChoiceUnion {
	if !hasTools {
		return nil
	}
	switch {
	case tc.Auto != nil:
		return &openai.ChatCompletionToolChoiceUnion{Value: string(openai.ToolChoiceTypeAuto)}
	case tc.None != nil:
		return &openai.ChatCompletionToolChoiceUnion{Value: string(openai.ToolChoiceTypeNone)}
	case tc.Any != nil:
		// Anthropic "any" maps to OpenAI "required" (model must call a tool).
		return &openai.ChatCompletionToolChoiceUnion{Value: string(openai.ToolChoiceTypeRequired)}
	case tc.Tool != nil:
		return &openai.ChatCompletionToolChoiceUnion{
			Value: openai.ChatCompletionNamedToolChoice{
				Type:     openai.ToolTypeFunction,
				Function: openai.ChatCompletionNamedToolChoiceFunction{Name: tc.Tool.Name},
			},
		}
	default:
		return nil
	}
}

// The following are helper functions that convert an OpenAI ChatCompletionResponse to an Anthropic MessagesRepsonse

// openAIResponseToAnthropic converts an OpenAI ChatCompletionResponse to an Anthropic MessagesResponse.
func openAIResponseToAnthropic(resp *openai.ChatCompletionResponse, model string) *anthropic.MessagesResponse {
	var content []anthropic.MessagesContentBlock
	var stopReason *anthropic.StopReason

	if len(resp.Choices) > 0 {
		choice := &resp.Choices[0]
		msg := &choice.Message

		// Convert thinking blocks first (Anthropic convention: thinking → text → tool_use).
		content = appendThinkingBlocks(content, msg)

		// Convert text content.
		if msg.Content != nil && *msg.Content != "" {
			content = append(content, anthropic.MessagesContentBlock{
				Text: &anthropic.TextBlock{Type: "text", Text: *msg.Content},
			})
		}

		// Convert tool calls to tool_use content blocks.
		for _, tc := range msg.ToolCalls {
			var input map[string]any
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			}
			// If tool call json string is malformed (OpenAI allows this because of it being cut off mid-stream)
			// then we set input to an empty map
			if input == nil {
				input = map[string]any{}
			}
			id := ""
			if tc.ID != nil {
				id = *tc.ID
			}
			content = append(content, anthropic.MessagesContentBlock{
				Tool: &anthropic.ToolUseBlock{
					Type:  "tool_use",
					ID:    id,
					Name:  tc.Function.Name,
					Input: input,
				},
			})
		}

		sr := openAIFinishReasonToAnthropic(choice.FinishReason)
		stopReason = &sr
	}

	usage := &anthropic.Usage{
		InputTokens:  float64(resp.Usage.PromptTokens),
		OutputTokens: float64(resp.Usage.CompletionTokens),
	}

	return &anthropic.MessagesResponse{
		ID:         resp.ID,
		Type:       anthropic.ConstantMessagesResponseTypeMessages("message"),
		Role:       anthropic.ConstantMessagesResponseRoleAssistant("assistant"),
		Content:    content,
		Model:      model,
		StopReason: stopReason,
		Usage:      usage,
	}
}

// appendThinkingBlocks converts OpenAI ThinkingBlocks and ReasoningContent to Anthropic
// thinking/redacted_thinking content blocks. ThinkingBlocks take priority over ReasoningContent.
func appendThinkingBlocks(content []anthropic.MessagesContentBlock, msg *openai.ChatCompletionResponseChoiceMessage) []anthropic.MessagesContentBlock {
	// Prefer ThinkingBlocks (structured, preserves signatures).
	if len(msg.ThinkingBlocks) > 0 {
		for _, tb := range msg.ThinkingBlocks {
			switch tb.Type {
			case "thinking":
				content = append(content, anthropic.MessagesContentBlock{
					Thinking: &anthropic.ThinkingBlock{Type: "thinking", Thinking: tb.Thinking, Signature: tb.Signature},
				})
			case "redacted_thinking":
				content = append(content, anthropic.MessagesContentBlock{
					RedactedThinking: &anthropic.RedactedThinkingBlock{Type: "redacted_thinking", Data: tb.Data},
				})
			}
		}
		return content
	}

	// Fall back to ReasoningContent (used by AWS Bedrock, Gemini, Qwen, DeepSeek).
	if msg.ReasoningContent == nil {
		return content
	}
	// Some models (Qwen, DeepSeek) return reasoning content as a plain string.
	if str, ok := msg.ReasoningContent.Value.(string); ok && str != "" {
		content = append(content, anthropic.MessagesContentBlock{
			Thinking: &anthropic.ThinkingBlock{Type: "thinking", Thinking: str},
		})
		return content
	}
	rc, ok := msg.ReasoningContent.Value.(*openai.ReasoningContent)
	if !ok || rc == nil || rc.ReasoningContent == nil {
		return content
	}
	if rc.ReasoningContent.ReasoningText != nil {
		content = append(content, anthropic.MessagesContentBlock{
			Thinking: &anthropic.ThinkingBlock{
				Type:      "thinking",
				Thinking:  rc.ReasoningContent.ReasoningText.Text,
				Signature: rc.ReasoningContent.ReasoningText.Signature,
			},
		})
	}
	if len(rc.ReasoningContent.RedactedContent) > 0 {
		content = append(content, anthropic.MessagesContentBlock{
			RedactedThinking: &anthropic.RedactedThinkingBlock{
				Type: "redacted_thinking",
				Data: string(rc.ReasoningContent.RedactedContent),
			},
		})
	}
	return content
}

// openAIFinishReasonToAnthropic maps an OpenAI finish_reason to an Anthropic StopReason.
func openAIFinishReasonToAnthropic(reason openai.ChatCompletionChoicesFinishReason) anthropic.StopReason {
	switch reason {
	case openai.ChatCompletionChoicesFinishReasonStop:
		return anthropic.StopReasonEndTurn
	case openai.ChatCompletionChoicesFinishReasonLength:
		return anthropic.StopReasonMaxTokens
	case openai.ChatCompletionChoicesFinishReasonToolCalls:
		return anthropic.StopReasonToolUse
	case openai.ChatCompletionChoicesFinishReasonContentFilter:
		return anthropic.StopReasonRefusal
	default:
		return anthropic.StopReasonEndTurn
	}
}

// The following are helpers that convert an OpenAI Stream to an Anthropic Stream (SSE conversion)

// The following structs are used to produce deterministic JSON for Anthropic SSE events.
// Using typed structs (instead of map[string]any) ensures sonic serializes fields in
// declaration order, making the output stable across runs.

type sseMessageStart struct {
	Type    string         `json:"type"`
	Message sseMessageBody `json:"message"`
}

type sseMessageBody struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Content      []any           `json:"content"`
	Model        string          `json:"model"`
	StopReason   any             `json:"stop_reason"`
	StopSequence any             `json:"stop_sequence"`
	Usage        sseMessageUsage `json:"usage"`
}

type sseMessageUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type sseContentBlockStartText struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock sseTextBlock `json:"content_block"`
}

type sseTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sseContentBlockStartTool struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock sseToolBlock `json:"content_block"`
}

type sseToolBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type sseContentBlockDeltaText struct {
	Type  string       `json:"type"`
	Index int          `json:"index"`
	Delta sseTextDelta `json:"delta"`
}

type sseTextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sseContentBlockDeltaTool struct {
	Type  string            `json:"type"`
	Index int               `json:"index"`
	Delta sseInputJSONDelta `json:"delta"`
}

type sseInputJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

type sseContentBlockStartThinking struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock sseThinkingInit `json:"content_block"`
}

type sseThinkingInit struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type sseContentBlockDeltaThinking struct {
	Type  string           `json:"type"`
	Index int              `json:"index"`
	Delta sseThinkingDelta `json:"delta"`
}

type sseThinkingDelta struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type sseContentBlockDeltaSignature struct {
	Type  string            `json:"type"`
	Index int               `json:"index"`
	Delta sseSignatureDelta `json:"delta"`
}

type sseSignatureDelta struct {
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

type sseContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type sseMessageDelta struct {
	Type  string              `json:"type"`
	Delta sseMessageDeltaBody `json:"delta"`
	Usage sseOutputUsage      `json:"usage"`
}

type sseMessageDeltaBody struct {
	StopReason   string `json:"stop_reason"`
	StopSequence any    `json:"stop_sequence"`
}

type sseOutputUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type sseMessageStop struct {
	Type string `json:"type"`
}

// openAIStreamToAnthropicState tracks the state for converting OpenAI SSE chunks to Anthropic SSE events.
type openAIStreamToAnthropicState struct {
	buffer           bytes.Buffer
	messageStarted   bool // flag indicating emitted message_start
	hasOpenBlock     bool // flag indicating emitted content_block_start but not content_block_stop
	hasThinkingBlock bool // flag indicating the open block is a thinking block
	closingEmitted   bool // flag indicating emitted content_block_stop + message_delta + message_stop
	messageID        string
	model            string
	stopReason       string // Anthropic stop_reason, mapped from OpenAI finish_reason
	inputTokens      int
	outputTokens     int
	tokenUsage       metrics.TokenUsage
	blockIndex       int                       // current Anthropic content block index
	activeTools      map[int64]*streamToolCall // keyed by OpenAI tool_call index
	requestModel     string
}

type streamToolCall struct {
	blockIdx int
	id       string
	name     string
}

// processBuffer processes the buffered OpenAI SSE data and emits Anthropic SSE events.
func (s *openAIStreamToAnthropicState) processBuffer(out *[]byte, endOfStream bool) error {
	// Loop through all event blocks that are separated by a blank line
	for {
		eventBlock, remaining, found := bytes.Cut(s.buffer.Bytes(), []byte("\n\n"))
		if !found {
			break
		}
		if err := s.processEventBlock(eventBlock, out); err != nil {
			return err
		}
		// Clear buffer and add back remaining SSE data
		s.buffer.Reset()
		s.buffer.Write(remaining)
	}

	// Handle any remaining data at end of stream.
	if endOfStream {
		if s.buffer.Len() > 0 {
			remaining := s.buffer.Bytes()
			s.buffer.Reset()
			if err := s.processEventBlock(remaining, out); err != nil {
				return err
			}
		}
		if !s.closingEmitted {
			return s.emitClosingEvents(out)
		}
	}
	return nil
}

// processEventBlock processes a single SSE event block (data between consecutive \n\n separators).
func (s *openAIStreamToAnthropicState) processEventBlock(block []byte, out *[]byte) error {
	var eventData []byte
	for line := range bytes.SplitSeq(block, []byte("\n")) {
		if after, ok := cutSSEDataPrefix(line); ok {
			data := bytes.TrimSpace(after)
			if len(data) > 0 {
				eventData = data
			}
		}
	}

	if len(eventData) == 0 {
		return nil
	}

	// Skip the [DONE] marker; closing events are emitted on the usage chunk or endOfStream.
	if bytes.Equal(eventData, sseDoneMessage) {
		return nil
	}

	var chunk openai.ChatCompletionResponseChunk
	if err := json.Unmarshal(eventData, &chunk); err != nil {
		// Skip malformed chunks silently.
		return nil
	}

	return s.handleChunk(&chunk, out)
}

// handleChunk converts a single OpenAI ChatCompletionResponseChunk to Anthropic SSE events.
func (s *openAIStreamToAnthropicState) handleChunk(chunk *openai.ChatCompletionResponseChunk, out *[]byte) error {
	// Update StreamState's message ID and model with chunk ID and model
	if chunk.ID != "" && s.messageID == "" {
		s.messageID = chunk.ID
	}
	if chunk.Model != "" && s.model == "" {
		s.model = chunk.Model
	}

	// Usage-only chunk (emitted when stream_options.include_usage=true)
	// One of the two ways to indicate stream end (other is endOfStream)
	if len(chunk.Choices) == 0 && chunk.Usage != nil {
		s.inputTokens = chunk.Usage.PromptTokens
		s.outputTokens = chunk.Usage.CompletionTokens
		// OpenAI's cached_tokens/cache_creation_input_tokens are a breakdown within
		// prompt_tokens, not additive like Anthropic's native cache fields, so we don't
		// forward them here to avoid double-counting.
		s.tokenUsage = metrics.ExtractTokenUsageFromExplicitCaching(
			int64(s.inputTokens),
			int64(s.outputTokens),
			ptr.To(int64(0)),
			ptr.To(int64(0)),
		)
		return s.emitClosingEvents(out)
	}

	if len(chunk.Choices) == 0 {
		return nil
	}

	// Choose first choice in chunk
	choice := &chunk.Choices[0]
	delta := choice.Delta

	// Emit message_start on the first meaningful delta.
	if !s.messageStarted && delta != nil {
		if err := s.emitMessageStart(out); err != nil {
			return err
		}
	}

	if delta != nil {
		// Handle reasoning/thinking content (must come before text).
		if delta.ReasoningContent != nil {
			if err := s.handleReasoningDelta(delta.ReasoningContent, out); err != nil {
				return err
			}
		}
		// Signatures ride on thinking_blocks (reasoning_content is a plain string).
		for i := range delta.ThinkingBlocks {
			tb := &delta.ThinkingBlocks[i]
			if err := s.handleReasoningDelta(&openai.StreamReasoningContent{
				Text: tb.Thinking, Signature: tb.Signature,
			}, out); err != nil {
				return err
			}
		}

		// Handle text content.
		if delta.Content != nil && *delta.Content != "" {
			// Close any open thinking block before starting a text block.
			if s.hasThinkingBlock {
				if err := s.closeThinkingBlock(out); err != nil {
					return err
				}
			}
			// Emit textblockstart if not started
			if !s.hasOpenBlock {
				if err := s.emitTextBlockStart(out); err != nil {
					return err
				}
			}
			if err := s.emitTextDelta(*delta.Content, out); err != nil {
				return err
			}
		}

		// Handle tool call deltas.
		for i := range delta.ToolCalls {
			if err := s.handleToolCallDelta(&delta.ToolCalls[i], out); err != nil {
				return err
			}
		}
	}

	// Store finish_reason for use in the closing events.
	if choice.FinishReason != "" {
		s.stopReason = string(openAIFinishReasonToAnthropic(choice.FinishReason))
	}

	return nil
}

// emitMessageStart emits the Anthropic message_start SSE event.
func (s *openAIStreamToAnthropicState) emitMessageStart(out *[]byte) error {
	s.messageStarted = true
	payload := sseMessageStart{
		Type: "message_start",
		Message: sseMessageBody{
			ID:           s.messageID,
			Type:         "message",
			Role:         "assistant",
			Content:      []any{},
			Model:        cmp.Or(s.model, s.requestModel),
			StopReason:   nil,
			StopSequence: nil,
			// Input tokens are not yet known; they will be reported in message_delta.usage.
			Usage: sseMessageUsage{InputTokens: 0, OutputTokens: 0},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal message_start: %w", err)
	}
	appendAnthropicSSEEvent(out, "message_start", data)
	return nil
}

// emitTextBlockStart emits a content_block_start SSE event for a text content block.
func (s *openAIStreamToAnthropicState) emitTextBlockStart(out *[]byte) error {
	s.hasOpenBlock = true
	payload := sseContentBlockStartText{
		Type:         "content_block_start",
		Index:        s.blockIndex,
		ContentBlock: sseTextBlock{Type: "text", Text: ""},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal content_block_start: %w", err)
	}
	appendAnthropicSSEEvent(out, "content_block_start", data)
	return nil
}

// emitTextDelta emits a content_block_delta SSE event with text content.
func (s *openAIStreamToAnthropicState) emitTextDelta(text string, out *[]byte) error {
	payload := sseContentBlockDeltaText{
		Type:  "content_block_delta",
		Index: s.blockIndex,
		Delta: sseTextDelta{Type: "text_delta", Text: text},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal content_block_delta: %w", err)
	}
	appendAnthropicSSEEvent(out, "content_block_delta", data)
	return nil
}

// handleReasoningDelta handles an OpenAI reasoning content delta and emits Anthropic thinking block events.
//
// Note: StreamReasoningContent also has a RedactedContent field (used by AWS Bedrock streaming).
// We do not handle it here because Anthropic's SSE protocol for redacted_thinking is a complete
// block (content_block_start + content_block_stop with no deltas), which requires a different emit
// pattern. This field does not appear in standard OpenAI-compatible backends (vLLM, etc.).
func (s *openAIStreamToAnthropicState) handleReasoningDelta(rc *openai.StreamReasoningContent, out *[]byte) error {
	if rc.Text == "" && rc.Signature == "" {
		return nil
	}
	// Close any open non-thinking block (e.g., text) before starting a thinking block.
	// This handles the unlikely case where reasoning arrives after text content.
	if !s.hasThinkingBlock && s.hasOpenBlock {
		if err := s.emitContentBlockStop(out); err != nil {
			return err
		}
		s.hasOpenBlock = false
		s.blockIndex++
	}
	// Ensure the thinking block is open before emitting any delta.
	if !s.hasThinkingBlock {
		if err := s.emitThinkingBlockStart(out); err != nil {
			return err
		}
	}
	// Handle thinking text.
	if rc.Text != "" {
		if err := s.emitThinkingDelta(rc.Text, out); err != nil {
			return err
		}
	}
	// Handle signature.
	if rc.Signature != "" {
		if err := s.emitSignatureDelta(rc.Signature, out); err != nil {
			return err
		}
	}
	return nil
}

// emitThinkingBlockStart emits a content_block_start SSE event for a thinking content block.
func (s *openAIStreamToAnthropicState) emitThinkingBlockStart(out *[]byte) error {
	s.hasOpenBlock = true
	s.hasThinkingBlock = true
	payload := sseContentBlockStartThinking{
		Type:         "content_block_start",
		Index:        s.blockIndex,
		ContentBlock: sseThinkingInit{Type: "thinking", Thinking: ""},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal thinking content_block_start: %w", err)
	}
	appendAnthropicSSEEvent(out, "content_block_start", data)
	return nil
}

// emitThinkingDelta emits a content_block_delta SSE event with thinking content.
func (s *openAIStreamToAnthropicState) emitThinkingDelta(text string, out *[]byte) error {
	payload := sseContentBlockDeltaThinking{
		Type:  "content_block_delta",
		Index: s.blockIndex,
		Delta: sseThinkingDelta{Type: "thinking_delta", Thinking: text},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal thinking_delta: %w", err)
	}
	appendAnthropicSSEEvent(out, "content_block_delta", data)
	return nil
}

// emitSignatureDelta emits a content_block_delta SSE event with a signature.
func (s *openAIStreamToAnthropicState) emitSignatureDelta(signature string, out *[]byte) error {
	payload := sseContentBlockDeltaSignature{
		Type:  "content_block_delta",
		Index: s.blockIndex,
		Delta: sseSignatureDelta{Type: "signature_delta", Signature: signature},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal signature_delta: %w", err)
	}
	appendAnthropicSSEEvent(out, "content_block_delta", data)
	return nil
}

// closeThinkingBlock closes an open thinking block by emitting content_block_stop and advancing the block index.
func (s *openAIStreamToAnthropicState) closeThinkingBlock(out *[]byte) error {
	if err := s.emitContentBlockStop(out); err != nil {
		return err
	}
	s.hasOpenBlock = false
	s.hasThinkingBlock = false
	s.blockIndex++
	return nil
}

// handleToolCallDelta handles an OpenAI tool call delta and emits Anthropic tool_use content block events.
func (s *openAIStreamToAnthropicState) handleToolCallDelta(tc *openai.ChatCompletionChunkChoiceDeltaToolCall, out *[]byte) error {
	tool, exists := s.activeTools[tc.Index]
	if !exists {
		// New tool call: close any open block (e.g., text or thinking block) and open a new tool_use block.
		if s.hasOpenBlock {
			if err := s.emitContentBlockStop(out); err != nil {
				return err
			}
			s.hasThinkingBlock = false
			s.blockIndex++
		}

		id := ""
		if tc.ID != nil {
			id = *tc.ID
		}
		tool = &streamToolCall{
			blockIdx: s.blockIndex,
			id:       id,
			name:     tc.Function.Name,
		}
		s.activeTools[tc.Index] = tool
		s.hasOpenBlock = true

		// Emit content_block_start for the new tool_use block.
		payload := sseContentBlockStartTool{
			Type:  "content_block_start",
			Index: tool.blockIdx,
			ContentBlock: sseToolBlock{
				Type:  "tool_use",
				ID:    id,
				Name:  tc.Function.Name,
				Input: map[string]any{},
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal tool content_block_start: %w", err)
		}
		appendAnthropicSSEEvent(out, "content_block_start", data)
	}

	// Emit input_json_delta for accumulated tool arguments.
	if tc.Function.Arguments != "" {
		payload := sseContentBlockDeltaTool{
			Type:  "content_block_delta",
			Index: tool.blockIdx,
			Delta: sseInputJSONDelta{Type: "input_json_delta", PartialJSON: tc.Function.Arguments},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal input_json_delta: %w", err)
		}
		appendAnthropicSSEEvent(out, "content_block_delta", data)
	}

	return nil
}

// emitContentBlockStop emits a content_block_stop SSE event for the current block.
func (s *openAIStreamToAnthropicState) emitContentBlockStop(out *[]byte) error {
	payload := sseContentBlockStop{Type: "content_block_stop", Index: s.blockIndex}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal content_block_stop: %w", err)
	}
	appendAnthropicSSEEvent(out, "content_block_stop", data)
	return nil
}

// emitClosingEvents emits content_block_stop (if a block is open), message_delta, and message_stop SSE events.
func (s *openAIStreamToAnthropicState) emitClosingEvents(out *[]byte) error {
	if s.closingEmitted {
		return nil
	}
	s.closingEmitted = true

	// Close the currently open content block.
	if s.hasOpenBlock {
		if err := s.emitContentBlockStop(out); err != nil {
			return err
		}
		s.hasOpenBlock = false
	}

	stopReason := s.stopReason
	if stopReason == "" {
		stopReason = string(anthropic.StopReasonEndTurn)
	}

	// Backfill input_tokens here (not message_start): OpenAI doesn't report it until now.
	msgDeltaPayload := sseMessageDelta{
		Type:  "message_delta",
		Delta: sseMessageDeltaBody{StopReason: stopReason, StopSequence: nil},
		Usage: sseOutputUsage{
			InputTokens:  s.inputTokens,
			OutputTokens: s.outputTokens,
		},
	}
	data, err := json.Marshal(msgDeltaPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal message_delta: %w", err)
	}
	appendAnthropicSSEEvent(out, "message_delta", data)

	// Emit message_stop.
	data, err = json.Marshal(sseMessageStop{Type: "message_stop"})
	if err != nil {
		return fmt.Errorf("failed to marshal message_stop: %w", err)
	}
	appendAnthropicSSEEvent(out, "message_stop", data)

	return nil
}

// appendAnthropicSSEEvent appends a formatted Anthropic SSE event to the output buffer.
func appendAnthropicSSEEvent(buf *[]byte, eventType string, data []byte) {
	*buf = append(*buf, "event: "...)
	*buf = append(*buf, eventType...)
	*buf = append(*buf, '\n')
	*buf = append(*buf, "data: "...)
	*buf = append(*buf, data...)
	*buf = append(*buf, '\n', '\n')
}
