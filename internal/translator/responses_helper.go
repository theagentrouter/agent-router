// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

var defaultTextConfig = openai.ResponseTextConfig{
	Format: openai.ResponseFormatTextConfigUnionParam{
		OfText: &openai.ResponseFormatTextParam{Type: "text"},
	},
}

func chatCompletionToResponse(chatResp *openai.ChatCompletionResponse, requestModel string) *openai.Response {
	if chatResp == nil {
		return nil
	}

	resp := &openai.Response{
		ID:          chatResp.ID,
		Object:      "response",
		Model:       cmp.Or(chatResp.Model, requestModel),
		CreatedAt:   chatResp.Created,
		Status:      "completed",
		Text:        defaultTextConfig,
		Output:      []openai.ResponseOutputItemUnion{},
		ServiceTier: chatResp.ServiceTier,
	}

	if chatResp.Usage != (openai.Usage{}) {
		resp.Usage = responsesUsage(&chatResp.Usage)
	}

	for i := range chatResp.Choices {
		choice := &chatResp.Choices[i]
		status := applyResponsesFinishReason(resp, choice.FinishReason)
		if text := responsesReasoningText(choice.Message.ReasoningContent); text != "" {
			resp.Output = append(resp.Output, responsesReasoningItem("rs_"+chatResp.ID, text, status))
		}
		content := []openai.ResponseOutputMessageContentArrayUnion{}
		if text := choice.Message.Content; text != nil && *text != "" {
			content = append(content, openai.ResponseOutputMessageContentArrayUnion{OfOutputText: &openai.ResponseOutputTextParam{Type: "output_text", Text: *text}})
		}
		if refusal := choice.Message.Refusal; refusal != nil && *refusal != "" {
			content = append(content, openai.ResponseOutputMessageContentArrayUnion{OfRefusal: &openai.ResponseOutputRefusalParam{Type: "refusal", Refusal: *refusal}})
		}
		if len(content) > 0 {
			resp.Output = append(resp.Output, responsesMessageItem("msg_"+chatResp.ID, content, status))
		}

		for _, tc := range choice.Message.ToolCalls {
			resp.Output = append(resp.Output, openai.ResponseOutputItemUnion{
				OfFunctionCall: &openai.ResponseFunctionToolCall{
					ID:        ptr.Deref(tc.ID, ""),
					CallID:    ptr.Deref(tc.ID, ""),
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
					Type:      "function_call",
					Status:    status,
				},
			})
		}
	}

	return resp
}

func responsesUsage(usage *openai.Usage) *openai.ResponseUsage {
	if usage == nil {
		return nil
	}
	result := &openai.ResponseUsage{
		InputTokens: int64(usage.PromptTokens), OutputTokens: int64(usage.CompletionTokens), TotalTokens: int64(usage.TotalTokens),
	}
	if details := usage.PromptTokensDetails; details != nil {
		result.InputTokensDetails.CachedTokens = int64(details.CachedTokens)
		result.InputTokensDetails.CacheWriteTokens = int64(details.CacheWriteTokensValue())
	}
	if details := usage.CompletionTokensDetails; details != nil {
		result.OutputTokensDetails.ReasoningTokens = int64(details.ReasoningTokens)
	}
	return result
}

func applyResponsesRequestSettings(resp *openai.Response, req *openai.ResponseRequest) {
	if req == nil {
		return
	}
	resp.Temperature = ptr.Deref(req.Temperature, 1)
	resp.TopP = ptr.Deref(req.TopP, 1)
	resp.PresencePenalty = req.PresencePenalty
	resp.FrequencyPenalty = req.FrequencyPenalty
	resp.Tools = req.Tools
	resp.ToolChoice = req.ToolChoice
	resp.ParallelToolCalls = req.ParallelToolCalls
	resp.User = req.User
	resp.Store = ptr.To(false)
	resp.MaxOutputTokens = req.MaxOutputTokens
	resp.Reasoning = req.Reasoning
	resp.Text = openai.ResponseTextConfig{Format: req.Text.Format, Verbosity: req.Text.Verbosity}
	if resp.Text.Format == (openai.ResponseFormatTextConfigUnionParam{}) {
		resp.Text.Format = defaultTextConfig.Format
	}
}

func responsesMessageItem(id string, content []openai.ResponseOutputMessageContentArrayUnion, status string) openai.ResponseOutputItemUnion {
	return openai.ResponseOutputItemUnion{OfOutputMessage: &openai.ResponseOutputMessage{
		ID: id, Role: "assistant", Type: "message", Status: status,
		Content: openai.ResponseOutputMessageContentUnion{OfContentArray: content},
	}}
}

func responsesReasoningText(content *openai.ReasoningContentUnion) string {
	if content != nil {
		switch value := content.Value.(type) {
		case string:
			return value
		case *openai.ReasoningContent:
			if value != nil && value.ReasoningContent != nil && value.ReasoningContent.ReasoningText != nil {
				return value.ReasoningContent.ReasoningText.Text
			}
		}
	}
	return ""
}

func responsesReasoningItem(id, text, status string) openai.ResponseOutputItemUnion {
	return openai.ResponseOutputItemUnion{OfReasoning: &openai.ResponseReasoningItem{
		ID: id, Type: "reasoning", Status: status,
		Summary: []openai.ResponseReasoningItemSummaryParam{},
		Content: []openai.ResponseReasoningItemContentParam{{Type: "reasoning_text", Text: text}},
	}}
}

func responsesIncompleteReason(reason openai.ChatCompletionChoicesFinishReason) string {
	switch reason {
	case openai.ChatCompletionChoicesFinishReasonLength:
		return "max_output_tokens"
	case openai.ChatCompletionChoicesFinishReasonContentFilter, openai.ChatCompletionChoicesFinishReasonRecitation:
		return "content_filter"
	default:
		return ""
	}
}

func applyResponsesFinishReason(resp *openai.Response, reason openai.ChatCompletionChoicesFinishReason) string {
	switch reason {
	case openai.ChatCompletionChoicesFinishReasonError, openai.ChatCompletionChoicesFinishReasonMalformedFunctionCall,
		openai.ChatCompletionChoicesFinishReasonUnexpectedToolCall, openai.ChatCompletionChoicesFinishReasonLanguage,
		openai.ChatCompletionChoicesFinishReasonNoImage:
		resp.Status = "failed"
		resp.Error = openai.ResponseError{Code: "server_error", Message: "upstream model terminated with an error"}
		resp.IncompleteDetails = openai.ResponseIncompleteDetails{}
		return "incomplete"
	}
	if incomplete := responsesIncompleteReason(reason); incomplete != "" {
		if resp.Status != "failed" {
			resp.Status = "incomplete"
			resp.IncompleteDetails.Reason = incomplete
		}
		return "incomplete"
	}
	return "completed"
}

type streamingToolCallState struct {
	itemID      string
	id          string
	name        string
	arguments   strings.Builder
	outputIndex int64
}

type streamingReasoningState struct {
	text        strings.Builder
	outputIndex int64
}

type streamingRefusalState struct {
	text         strings.Builder
	contentIndex int64
}

type chatCompletionStreamToResponsesConverter struct {
	started        bool
	responseID     string
	model          string
	serviceTier    string
	created        openai.JSONUNIXTime
	seqNum         int64
	messageStarted bool
	messageID      string
	accText        strings.Builder
	textStarted    bool
	textIndex      int64
	refusal        *streamingRefusalState
	messageIndex   int64
	itemCount      int64
	toolCalls      map[int64]*streamingToolCallState
	reasoning      *streamingReasoningState
	finished       bool
	pendingStatus  string
	stopReason     openai.ChatCompletionChoicesFinishReason
	requestModel   string
	requestConfig  *openai.ResponseRequest
}

func (c *chatCompletionStreamToResponsesConverter) nextSeq() int64 {
	c.seqNum++
	return c.seqNum - 1
}

func (c *chatCompletionStreamToResponsesConverter) ensureMessage(output []byte) []byte {
	if c.messageStarted {
		return output
	}
	c.messageStarted = true
	c.messageIndex = c.itemCount
	c.itemCount++
	return append(output, marshalSSEEvent("response.output_item.added", openai.ResponseOutputItemAddedEvent{
		Item:        responsesMessageItem(c.messageID, []openai.ResponseOutputMessageContentArrayUnion{}, "in_progress"),
		OutputIndex: c.messageIndex, SequenceNumber: c.nextSeq(), Type: "response.output_item.added",
	})...)
}

func formatSSEEvent(eventType string, data []byte) []byte {
	var buf []byte
	buf = append(buf, "event: "...)
	buf = append(buf, eventType...)
	buf = append(buf, '\n')
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	return buf
}

func marshalSSEEvent(eventType string, v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return formatSSEEvent(eventType, data)
}

func (c *chatCompletionStreamToResponsesConverter) appendTerminalEvent(output []byte, status string, usage *openai.Usage) []byte {
	resp := c.buildFinalResponse(status, usage)
	eventType := "response." + status
	output = append(output, marshalSSEEvent(eventType, openai.ResponseCompletedEvent{
		Response:       resp,
		SequenceNumber: c.nextSeq(),
		Type:           eventType,
	})...)
	c.finished = false
	c.pendingStatus = ""
	return output
}

func (c *chatCompletionStreamToResponsesConverter) processLine(line []byte) (output []byte) {
	if !bytes.HasPrefix(line, sseDataPrefix) {
		return nil
	}
	data := bytes.TrimPrefix(line, sseDataPrefix)
	if bytes.Equal(data, sseDoneMessage) {
		return nil
	}

	chunk := &struct {
		openai.ChatCompletionResponseChunk
		Error *openai.ErrorType `json:"error"`
	}{}
	if err := json.Unmarshal(data, chunk); err != nil {
		return nil
	}
	if chunk.Error != nil {
		c.finished = false
		c.pendingStatus = ""
		return marshalSSEEvent("error", openai.ResponseErrorEvent{
			Type: "error", SequenceNumber: c.nextSeq(),
			Code:    cmp.Or(ptr.Deref(chunk.Error.Code, ""), chunk.Error.Type),
			Message: chunk.Error.Message, Param: ptr.Deref(chunk.Error.Param, ""),
		})
	}

	if chunk.Model != "" {
		c.model = chunk.Model
	}
	if chunk.ServiceTier != "" {
		c.serviceTier = chunk.ServiceTier
	}

	if !c.started {
		c.started = true
		c.responseID = cmp.Or(chunk.ID, fmt.Sprintf("resp_%d", time.Now().UnixNano()))
		c.created = chunk.Created
		c.messageID = fmt.Sprintf("msg_%s", c.responseID)

		resp := c.buildFinalResponse("in_progress", nil)
		output = append(output, marshalSSEEvent("response.created", openai.ResponseCreatedEvent{
			Response:       resp,
			SequenceNumber: c.nextSeq(),
			Type:           "response.created",
		})...)
		output = append(output, marshalSSEEvent("response.in_progress", openai.ResponseInProgressEvent{
			Response:       resp,
			SequenceNumber: c.nextSeq(),
			Type:           "response.in_progress",
		})...)
	}

	usage := chunk.Usage
	if usage != nil {
		if c.finished {
			output = c.appendTerminalEvent(output, c.pendingStatus, usage)
		}
	}

	if len(chunk.Choices) == 0 {
		return output
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	if delta != nil && delta.ReasoningContent != nil && delta.ReasoningContent.Text != "" {
		id := "rs_" + c.responseID
		if c.reasoning == nil {
			c.reasoning = &streamingReasoningState{outputIndex: c.itemCount}
			c.itemCount++
			output = append(output, marshalSSEEvent("response.output_item.added", openai.ResponseOutputItemAddedEvent{
				Type: "response.output_item.added", SequenceNumber: c.nextSeq(), OutputIndex: c.reasoning.outputIndex,
				Item: responsesReasoningItem(id, "", "in_progress"),
			})...)
		}
		c.reasoning.text.WriteString(delta.ReasoningContent.Text)
		output = append(output, marshalSSEEvent("response.reasoning_text.delta", openai.ResponseReasoningTextDeltaEvent{
			Type: "response.reasoning_text.delta", SequenceNumber: c.nextSeq(),
			ItemID: id, OutputIndex: c.reasoning.outputIndex, ContentIndex: 0, Delta: delta.ReasoningContent.Text,
		})...)
	}

	if delta != nil && delta.Content != nil && *delta.Content != "" {
		output = c.ensureMessage(output)
		if !c.textStarted {
			c.textStarted = true
			if c.refusal != nil {
				c.textIndex = 1
			}
			output = append(output, marshalSSEEvent("response.content_part.added", openai.ResponseContentPartAddedEvent{
				ContentIndex: c.textIndex,
				ItemID:       c.messageID,
				OutputIndex:  c.messageIndex,
				Part: openai.ResponseContentPartAddedEventPartUnion{
					OfResponseOutputText: &openai.ResponseOutputTextParam{
						Text: "",
						Type: "output_text",
					},
				},
				SequenceNumber: c.nextSeq(),
				Type:           "response.content_part.added",
			})...)
		}
		c.accText.WriteString(*delta.Content)
		output = append(output, marshalSSEEvent("response.output_text.delta", openai.ResponseTextDeltaEvent{
			ContentIndex:   c.textIndex,
			Delta:          *delta.Content,
			ItemID:         c.messageID,
			OutputIndex:    c.messageIndex,
			SequenceNumber: c.nextSeq(),
			Type:           "response.output_text.delta",
		})...)
	}

	if delta != nil && delta.Refusal != nil && *delta.Refusal != "" {
		output = c.ensureMessage(output)
		if c.refusal == nil {
			c.refusal = &streamingRefusalState{}
			if c.textStarted {
				c.refusal.contentIndex = 1
			}
			output = append(output, marshalSSEEvent("response.content_part.added", openai.ResponseContentPartAddedEvent{
				Type: "response.content_part.added", SequenceNumber: c.nextSeq(), ItemID: c.messageID,
				OutputIndex: c.messageIndex, ContentIndex: c.refusal.contentIndex,
				Part: openai.ResponseContentPartAddedEventPartUnion{OfResponseOutputRefusal: &openai.ResponseOutputRefusalParam{Type: "refusal"}},
			})...)
		}
		c.refusal.text.WriteString(*delta.Refusal)
		output = append(output, marshalSSEEvent("response.refusal.delta", openai.ResponseRefusalDeltaEvent{
			Type: "response.refusal.delta", SequenceNumber: c.nextSeq(), ItemID: c.messageID,
			OutputIndex: c.messageIndex, ContentIndex: c.refusal.contentIndex, Delta: *delta.Refusal,
		})...)
	}

	if delta != nil && len(delta.ToolCalls) > 0 {
		if c.toolCalls == nil {
			c.toolCalls = make(map[int64]*streamingToolCallState)
		}
		for _, tc := range delta.ToolCalls {
			state, exists := c.toolCalls[tc.Index]
			if !exists {
				state = &streamingToolCallState{
					outputIndex: c.itemCount,
					itemID:      cmp.Or(ptr.Deref(tc.ID, ""), fmt.Sprintf("fc_%d", tc.Index)),
				}
				c.itemCount++
				c.toolCalls[tc.Index] = state
			}
			if id := ptr.Deref(tc.ID, ""); id != "" {
				state.id = id
			}
			if tc.Function.Name != "" {
				state.name = tc.Function.Name
			}
			if !exists {
				output = append(output, marshalSSEEvent("response.output_item.added", openai.ResponseOutputItemAddedEvent{
					Item: openai.ResponseOutputItemUnion{
						OfFunctionCall: &openai.ResponseFunctionToolCall{
							ID:        state.itemID,
							CallID:    state.id,
							Name:      state.name,
							Arguments: "",
							Type:      "function_call",
							Status:    "in_progress",
						},
					},
					OutputIndex:    state.outputIndex,
					SequenceNumber: c.nextSeq(),
					Type:           "response.output_item.added",
				})...)
			}
			if tc.Function.Arguments != "" {
				state.arguments.WriteString(tc.Function.Arguments)
				output = append(output, marshalSSEEvent("response.function_call_arguments.delta", openai.ResponseFunctionCallArgumentsDeltaEvent{
					Delta:          tc.Function.Arguments,
					ItemID:         state.itemID,
					OutputIndex:    state.outputIndex,
					SequenceNumber: c.nextSeq(),
					Type:           "response.function_call_arguments.delta",
				})...)
			}
		}
	}

	if choice.FinishReason != "" {
		c.stopReason = choice.FinishReason
		response := c.buildFinalResponse("completed", nil)
		if c.reasoning != nil {
			output = append(output, marshalSSEEvent("response.reasoning_text.done", openai.ResponseReasoningTextDoneEvent{
				Type: "response.reasoning_text.done", SequenceNumber: c.nextSeq(),
				ItemID: "rs_" + c.responseID, OutputIndex: c.reasoning.outputIndex, ContentIndex: 0, Text: c.reasoning.text.String(),
			})...)
		}
		if c.textStarted {
			output = append(output, marshalSSEEvent("response.output_text.done", openai.ResponseTextDoneEvent{
				ContentIndex:   c.textIndex,
				ItemID:         c.messageID,
				OutputIndex:    c.messageIndex,
				SequenceNumber: c.nextSeq(),
				Text:           c.accText.String(),
				Type:           "response.output_text.done",
			})...)
			output = append(output, marshalSSEEvent("response.content_part.done", openai.ResponseContentPartDoneEvent{
				ContentIndex: c.textIndex,
				ItemID:       c.messageID,
				OutputIndex:  c.messageIndex,
				Part: openai.ResponseContentPartDoneEventPartUnion{
					OfResponseOutputText: &openai.ResponseOutputTextParam{
						Text: c.accText.String(),
						Type: "output_text",
					},
				},
				SequenceNumber: c.nextSeq(),
				Type:           "response.content_part.done",
			})...)
		}
		if c.refusal != nil {
			text := c.refusal.text.String()
			output = append(output, marshalSSEEvent("response.refusal.done", openai.ResponseRefusalDoneEvent{
				Type: "response.refusal.done", SequenceNumber: c.nextSeq(), ItemID: c.messageID,
				OutputIndex: c.messageIndex, ContentIndex: c.refusal.contentIndex, Refusal: text,
			})...)
			output = append(output, marshalSSEEvent("response.content_part.done", openai.ResponseContentPartDoneEvent{
				Type: "response.content_part.done", SequenceNumber: c.nextSeq(), ItemID: c.messageID,
				OutputIndex: c.messageIndex, ContentIndex: c.refusal.contentIndex,
				Part: openai.ResponseContentPartDoneEventPartUnion{OfResponseOutputRefusal: &openai.ResponseOutputRefusalParam{Type: "refusal", Refusal: text}},
			})...)
		}
		items := response.Output
		for i := range items {
			item := &items[i]
			if tool := item.OfFunctionCall; tool != nil {
				output = append(output, marshalSSEEvent("response.function_call_arguments.done", openai.ResponseFunctionCallArgumentsDoneEvent{
					Arguments:      tool.Arguments,
					ItemID:         tool.ID,
					Name:           tool.Name,
					OutputIndex:    int64(i),
					SequenceNumber: c.nextSeq(),
					Type:           "response.function_call_arguments.done",
				})...)
			}
			output = append(output, marshalSSEEvent("response.output_item.done", openai.ResponseOutputItemDoneEvent{
				Item:           *item,
				OutputIndex:    int64(i),
				SequenceNumber: c.nextSeq(),
				Type:           "response.output_item.done",
			})...)
		}
		if usage != nil {
			output = c.appendTerminalEvent(output, response.Status, usage)
		} else {
			c.finished = true
			c.pendingStatus = response.Status
		}
	}

	return output
}

func (c *chatCompletionStreamToResponsesConverter) buildFinalResponse(status string, usage *openai.Usage) openai.Response {
	resp := openai.Response{
		ID:          c.responseID,
		Object:      "response",
		Model:       cmp.Or(c.model, c.requestModel),
		CreatedAt:   c.created,
		Status:      status,
		Text:        defaultTextConfig,
		ServiceTier: c.serviceTier,
	}
	itemStatus := status
	if c.stopReason != "" {
		itemStatus = applyResponsesFinishReason(&resp, c.stopReason)
	}
	resp.Usage = responsesUsage(usage)
	applyResponsesRequestSettings(&resp, c.requestConfig)

	outputItems := make([]openai.ResponseOutputItemUnion, c.itemCount)
	if c.reasoning != nil {
		outputItems[c.reasoning.outputIndex] = responsesReasoningItem("rs_"+c.responseID, c.reasoning.text.String(), itemStatus)
	}
	if c.messageStarted {
		content := []openai.ResponseOutputMessageContentArrayUnion{}
		if c.textStarted {
			content = append(content, openai.ResponseOutputMessageContentArrayUnion{OfOutputText: &openai.ResponseOutputTextParam{Type: "output_text", Text: c.accText.String()}})
		}
		if c.refusal != nil {
			content = append(content, openai.ResponseOutputMessageContentArrayUnion{OfRefusal: &openai.ResponseOutputRefusalParam{Type: "refusal", Refusal: c.refusal.text.String()}})
			if c.textStarted && c.refusal.contentIndex == 0 {
				content[0], content[1] = content[1], content[0]
			}
		}
		outputItems[c.messageIndex] = responsesMessageItem(c.messageID, content, itemStatus)
	}
	for _, state := range c.toolCalls {
		outputItems[state.outputIndex] = openai.ResponseOutputItemUnion{
			OfFunctionCall: &openai.ResponseFunctionToolCall{
				ID:        state.itemID,
				CallID:    state.id,
				Name:      state.name,
				Arguments: state.arguments.String(),
				Type:      "function_call",
				Status:    itemStatus,
			},
		}
	}
	resp.Output = outputItems
	return resp
}

func responsesRequestToChatCompletionRequest(req *openai.ResponseRequest, model string) (openai.ChatCompletionRequest, error) {
	chatReq := openai.ChatCompletionRequest{
		Model:               model,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		PresencePenalty:     req.PresencePenalty,
		FrequencyPenalty:    req.FrequencyPenalty,
		Verbosity:           openaisdk.ChatCompletionNewParamsVerbosity(req.Text.Verbosity),
		MaxCompletionTokens: req.MaxOutputTokens,
		Stream:              req.Stream,
		ParallelToolCalls:   req.ParallelToolCalls,
		ReasoningEffort:     openai.ReasoningEffort(req.Reasoning.Effort),
		ServiceTier:         req.ServiceTier,
		User:                req.User,
	}
	if req.SafetyIdentifier != "" || req.PromptCacheKey != "" || req.PromptCacheRetention != "" {
		return chatReq, fmt.Errorf("%w: safety_identifier, prompt_cache_key, and prompt_cache_retention are not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if req.PreviousResponseID != "" {
		return chatReq, fmt.Errorf("%w: previous_response_id is not supported by Chat Completion backends; provide the conversation in input", internalapi.ErrInvalidRequestBody)
	}
	if req.Conversation.OfConversationObject != nil || ptr.Deref(req.Conversation.OfString, "") != "" {
		return chatReq, fmt.Errorf("%w: conversation is not supported by Chat Completion backends; provide the conversation in input", internalapi.ErrInvalidRequestBody)
	}
	if ptr.Deref(req.Background, false) {
		return chatReq, fmt.Errorf("%w: background mode is not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if req.StreamOptions.IncludeObfuscation {
		return chatReq, fmt.Errorf("%w: stream obfuscation is not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if ptr.Deref(req.Store, false) {
		return chatReq, fmt.Errorf("%w: response storage is not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if (req.Truncation != "" && req.Truncation != "disabled") || len(req.ContextManagement) > 0 {
		return chatReq, fmt.Errorf("%w: automatic truncation and context management are not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if req.Reasoning.Summary != "" || req.Reasoning.GenerateSummary != "" {
		return chatReq, fmt.Errorf("%w: reasoning summaries are not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if req.Prompt.ID != "" || req.Prompt.Version != "" || len(req.Prompt.Variables) > 0 {
		return chatReq, fmt.Errorf("%w: reusable prompts are not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}
	if req.TopLogprobs != nil || len(req.Include) > 0 {
		return chatReq, fmt.Errorf("%w: include and top_logprobs are not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
	}

	format := req.Text.Format
	switch {
	case format.OfJSONSchema != nil:
		f := format.OfJSONSchema
		schema, err := json.Marshal(f.Schema)
		if err != nil {
			return chatReq, fmt.Errorf("%w: invalid response format schema: %w", internalapi.ErrInvalidRequestBody, err)
		}
		chatReq.ResponseFormat = &openai.ChatCompletionResponseFormatUnion{OfJSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: openai.ChatCompletionResponseFormatJSONSchemaJSONSchema{
				Name: f.Name, Description: f.Description, Schema: schema, Strict: ptr.Deref(f.Strict, false),
			},
		}}
	case format.OfJSONObject != nil:
		chatReq.ResponseFormat = &openai.ChatCompletionResponseFormatUnion{OfJSONObject: &openai.ChatCompletionResponseFormatJSONObjectParam{Type: openai.ChatCompletionResponseFormatTypeJSONObject}}
	case format.OfText != nil:
		chatReq.ResponseFormat = &openai.ChatCompletionResponseFormatUnion{OfText: &openai.ChatCompletionResponseFormatTextParam{Type: openai.ChatCompletionResponseFormatTypeText}}
	}
	switch choice := req.ToolChoice; {
	case choice.OfToolChoiceMode != nil:
		chatReq.ToolChoice = &openai.ChatCompletionToolChoiceUnion{Value: *choice.OfToolChoiceMode}
	case choice.OfFunctionTool != nil:
		chatReq.ToolChoice = &openai.ChatCompletionToolChoiceUnion{Value: openai.ChatCompletionNamedToolChoice{
			Type:     openai.ToolTypeFunction,
			Function: openai.ChatCompletionNamedToolChoiceFunction{Name: choice.OfFunctionTool.Name},
		}}
	case choice != (openai.ResponseToolChoiceUnion{}):
		return chatReq, fmt.Errorf("%w: unsupported Responses tool choice", internalapi.ErrInvalidRequestBody)
	}

	if req.Stream {
		chatReq.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	var messages []openai.ChatCompletionMessageParamUnion

	if req.Instructions != "" {
		messages = append(messages, openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Role:    openai.ChatMessageRoleSystem,
				Content: openai.ContentUnion{Value: req.Instructions},
			},
		})
	}

	if req.Input.OfString != nil {
		messages = append(messages, openai.ChatCompletionMessageParamUnion{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: *req.Input.OfString},
			},
		})
	} else if req.Input.OfInputItemList != nil {
		var err error
		messages, err = convertInputItemsToMessages(req.Input.OfInputItemList, messages)
		if err != nil {
			return chatReq, err
		}
	}

	chatReq.Messages = messages

	for i := range req.Tools {
		tool := &req.Tools[i]
		if tool.OfFunction == nil {
			return chatReq, fmt.Errorf("%w: only function tools are supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
		}
		parameters, strict, err := responsesFunctionParameters(tool.OfFunction)
		if err != nil {
			return chatReq, fmt.Errorf("%w: invalid function parameters: %w", internalapi.ErrInvalidRequestBody, err)
		}
		chatReq.Tools = append(chatReq.Tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.OfFunction.Name,
				Description: tool.OfFunction.Description,
				Parameters:  parameters,
				Strict:      strict,
			},
		})
	}

	return chatReq, nil
}

func copyResponsesSchema(value any, depth int) (any, error) {
	if depth >= 100 {
		return nil, fmt.Errorf("maximum function schema depth exceeded")
	}
	switch source := value.(type) {
	case map[string]any:
		if source == nil {
			return source, nil
		}
		result := make(map[string]any, len(source))
		for key, value := range source {
			var err error
			result[key], err = copyResponsesSchema(value, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case []any:
		result := make([]any, len(source))
		for i, value := range source {
			var err error
			result[i], err = copyResponsesSchema(value, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	default:
		return value, nil
	}
}

func responsesFunctionParameters(tool *openai.FunctionToolParam) (map[string]any, bool, error) {
	if tool.Strict != nil {
		return tool.Parameters, *tool.Strict, nil
	}
	copied, err := copyResponsesSchema(tool.Parameters, 0)
	if err != nil {
		return nil, false, err
	}
	schema := copied.(map[string]any)
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	if schema["type"] != "object" {
		return tool.Parameters, false, nil
	}
	pending := []map[string]any{schema}
	for visited := 0; len(pending) > 0; visited++ {
		if visited >= 50000 {
			return nil, false, fmt.Errorf("maximum number of function schema nodes exceeded")
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, key := range []string{"allOf", "oneOf", "not", "if", "then", "else", "dependentRequired", "dependentSchemas", "patternProperties", "prefixItems"} {
			if _, unsupported := current[key]; unsupported {
				return tool.Parameters, false, nil
			}
		}
		for _, key := range []string{"properties", "$defs", "definitions"} {
			children, _ := current[key].(map[string]any)
			for _, child := range children {
				if child, ok := child.(map[string]any); ok {
					pending = append(pending, child)
				} else {
					return tool.Parameters, false, nil
				}
			}
		}
		if properties, ok := current["properties"].(map[string]any); ok || current["type"] == "object" {
			required := make([]any, 0, len(properties))
			for _, name := range slices.Sorted(maps.Keys(properties)) {
				required = append(required, name)
			}
			current["required"] = required
			current["additionalProperties"] = false
		}
		if items, ok := current["items"].(map[string]any); ok {
			pending = append(pending, items)
		}
		if variants, ok := current["anyOf"].([]any); ok {
			for _, variant := range variants {
				if variant, ok := variant.(map[string]any); ok {
					pending = append(pending, variant)
				}
			}
		}
	}
	return schema, true, nil
}

func convertInputItemsToMessages(items []openai.ResponseInputItemUnionParam, messages []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, error) {
	for i := range items {
		item := &items[i]
		switch {
		case item.OfMessage != nil || item.OfInputMessage != nil:
			msg := item.OfMessage
			if msg == nil {
				msg = &openai.EasyInputMessageParam{
					Role:    item.OfInputMessage.Role,
					Content: openai.EasyInputMessageContentUnionParam{OfInputItemContentList: item.OfInputMessage.Content},
				}
			}
			for _, part := range msg.Content.OfInputItemContentList {
				if part.OfInputText == nil && part.OfInputImage == nil {
					return nil, fmt.Errorf("%w: only text and image message content is supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
				}
				if image := part.OfInputImage; image != nil && (image.FileID != "" || image.ImageURL == "") {
					return nil, fmt.Errorf("%w: image inputs must use image_url; file_id is not supported by Chat Completion backends", internalapi.ErrInvalidRequestBody)
				}
				if part.OfInputImage != nil && msg.Role != openai.ChatMessageRoleUser {
					return nil, fmt.Errorf("%w: image content is only supported in user messages", internalapi.ErrInvalidRequestBody)
				}
			}
			messages = append(messages, convertEasyInputMessage(msg))

		case item.OfOutputMessage != nil:
			message := convertOutputMessage(item.OfOutputMessage)
			if n := len(messages); n > 0 && messages[n-1].OfAssistant != nil && len(messages[n-1].OfAssistant.ToolCalls) > 0 {
				previous := messages[n-1].OfAssistant
				text := message.OfAssistant.Content.Value.(string)
				if before, ok := previous.Content.Value.(string); ok && before != "" {
					text = before + "\n\n" + text
				}
				previous.Content = openai.StringOrAssistantRoleContentUnion{Value: text}
			} else {
				messages = append(messages, message)
			}

		case item.OfFunctionCall != nil:
			fc := item.OfFunctionCall
			messages = appendToolCallAsAssistantMessage(messages, openai.ChatCompletionMessageToolCallParam{
				ID: ptr.To(fc.CallID),
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      fc.Name,
					Arguments: fc.Arguments,
				},
				Type: openai.ChatCompletionMessageToolCallTypeFunction,
			})

		case item.OfFunctionCallOutput != nil:
			fco := item.OfFunctionCallOutput
			var content string
			if fco.Output.OfString != nil {
				content = *fco.Output.OfString
			} else {
				var parts []string
				for _, part := range fco.Output.OfResponseFunctionCallOutputItemArray {
					if part.OfInputText == nil {
						return nil, fmt.Errorf("%w: only text function call outputs are supported", internalapi.ErrInvalidRequestBody)
					}
					parts = append(parts, part.OfInputText.Text)
				}
				content = strings.Join(parts, "\n\n")
			}
			messages = append(messages, openai.ChatCompletionMessageParamUnion{
				OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					Content:    openai.ContentUnion{Value: content},
					ToolCallID: fco.CallID,
				},
			})
		default:
			return nil, fmt.Errorf("%w: unsupported Responses input item at index %d", internalapi.ErrInvalidRequestBody, i)
		}
	}

	return messages, nil
}

func appendToolCallAsAssistantMessage(messages []openai.ChatCompletionMessageParamUnion, toolCall openai.ChatCompletionMessageToolCallParam) []openai.ChatCompletionMessageParamUnion {
	n := len(messages)
	if n > 0 && messages[n-1].OfAssistant != nil {
		messages[n-1].OfAssistant.ToolCalls = append(messages[n-1].OfAssistant.ToolCalls, toolCall)
		return messages
	}
	return append(messages, openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			Role:      openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ChatCompletionMessageToolCallParam{toolCall},
		},
	})
}

func convertEasyInputMessage(msg *openai.EasyInputMessageParam) openai.ChatCompletionMessageParamUnion {
	contentValue := ptr.Deref(msg.Content.OfString, extractInputContentText(msg.Content.OfInputItemContentList))
	switch msg.Role {
	case openai.ChatMessageRoleAssistant:
		return openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Role:    openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{Value: contentValue},
			},
		}
	case openai.ChatMessageRoleSystem:
		return openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Role:    openai.ChatMessageRoleSystem,
				Content: openai.ContentUnion{Value: contentValue},
			},
		}
	case openai.ChatMessageRoleDeveloper:
		return openai.ChatCompletionMessageParamUnion{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: openai.ContentUnion{Value: contentValue},
			},
		}
	default:
		return openai.ChatCompletionMessageParamUnion{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: convertEasyInputContent(msg.Content),
			},
		}
	}
}

func extractInputContentText(content []openai.ResponseInputContentUnionParam) string {
	var parts []string
	for _, c := range content {
		if c.OfInputText != nil {
			parts = append(parts, c.OfInputText.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func convertEasyInputContent(content openai.EasyInputMessageContentUnionParam) openai.StringOrUserRoleContentUnion {
	if content.OfString != nil {
		return openai.StringOrUserRoleContentUnion{Value: *content.OfString}
	}
	if content.OfInputItemContentList != nil {
		var parts []openai.ChatCompletionContentPartUserUnionParam
		for _, c := range content.OfInputItemContentList {
			switch {
			case c.OfInputText != nil:
				parts = append(parts, openai.ChatCompletionContentPartUserUnionParam{
					OfText: &openai.ChatCompletionContentPartTextParam{
						Text: c.OfInputText.Text,
						Type: string(openai.ChatCompletionContentPartTextTypeText),
					},
				})
			case c.OfInputImage != nil:
				parts = append(parts, openai.ChatCompletionContentPartUserUnionParam{
					OfImageURL: &openai.ChatCompletionContentPartImageParam{
						ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
							URL:    c.OfInputImage.ImageURL,
							Detail: openai.ChatCompletionContentPartImageImageURLDetail(c.OfInputImage.Detail),
						},
						Type: openai.ChatCompletionContentPartImageTypeImageURL,
					},
				})
			}
		}
		return openai.StringOrUserRoleContentUnion{Value: parts}
	}
	return openai.StringOrUserRoleContentUnion{Value: ""}
}

func NewResponsesViaChatCompletionTranslator(inner OpenAIChatCompletionTranslator) OpenAIResponsesTranslator {
	return &responsesViaChatCompletionTranslator{inner: inner}
}

type responsesViaChatCompletionTranslator struct {
	inner           OpenAIChatCompletionTranslator
	requestModel    internalapi.RequestModel
	stream          bool
	buffered        []byte
	streamConverter chatCompletionStreamToResponsesConverter
}

func (t *responsesViaChatCompletionTranslator) RequestBody(_ []byte, req *openai.ResponseRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	t.requestModel = req.Model
	t.stream = req.Stream
	t.streamConverter.requestModel = req.Model
	t.streamConverter.requestConfig = &openai.ResponseRequest{
		Temperature: req.Temperature, TopP: req.TopP, Text: req.Text, Reasoning: req.Reasoning,
		PresencePenalty: req.PresencePenalty, FrequencyPenalty: req.FrequencyPenalty,
		Tools: req.Tools, ToolChoice: req.ToolChoice, ParallelToolCalls: req.ParallelToolCalls, User: req.User,
	}

	chatReq, err := responsesRequestToChatCompletionRequest(req, req.Model)
	if err != nil {
		return nil, nil, err
	}
	if chatReq.MaxCompletionTokens == nil {
		switch t.inner.(type) {
		case *openAIToAWSAnthropicTranslatorV1ChatCompletion, *openAIToGCPAnthropicTranslatorV1ChatCompletion:
			chatReq.MaxCompletionTokens = ptr.To(int64(4096))
		}
	}
	t.streamConverter.requestConfig.MaxOutputTokens = chatReq.MaxCompletionTokens

	raw, err := json.Marshal(chatReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal chat completion request: %w", err)
	}

	newHeaders, newBody, err = t.inner.RequestBody(raw, &chatReq, false)
	if err != nil {
		return nil, nil, err
	}

	if len(newBody) == 0 {
		newBody = raw
		newHeaders = append(newHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(newBody))})
	}

	return
}

func (t *responsesViaChatCompletionTranslator) ResponseHeaders(headers map[string]string) ([]internalapi.Header, error) {
	return t.inner.ResponseHeaders(headers)
}

func (t *responsesViaChatCompletionTranslator) ResponseBody(respHeaders map[string]string, body io.Reader, endOfStream bool, span tracingapi.ResponsesSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	if body == nil {
		return nil, nil, tokenUsage, t.requestModel, fmt.Errorf("body is nil")
	}

	rawBody, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, tokenUsage, t.requestModel, fmt.Errorf("failed to read body: %w", err)
	}

	innerHeaders, innerBody, tokenUsage, innerModel, innerErr := t.inner.ResponseBody(
		respHeaders, bytes.NewReader(rawBody), endOfStream, nil,
	)
	var overloaded *StreamOverloadedError
	var streamErr *StreamResponseError
	if innerErr != nil && (!t.stream || (!errors.As(innerErr, &overloaded) && !errors.As(innerErr, &streamErr))) {
		return innerHeaders, innerBody, tokenUsage, cmp.Or(innerModel, t.requestModel), innerErr
	}

	chatData := innerBody
	if chatData == nil {
		chatData = rawBody
	}

	if !t.stream {
		if len(chatData) == 0 {
			return nil, nil, tokenUsage, cmp.Or(innerModel, t.requestModel), fmt.Errorf("empty response body")
		}

		resp := &openai.ChatCompletionResponse{}
		if err = json.Unmarshal(chatData, resp); err != nil {
			return nil, nil, tokenUsage, cmp.Or(innerModel, t.requestModel), fmt.Errorf("failed to unmarshal chat completion response: %w", err)
		}
		fallbackModel := cmp.Or(innerModel, t.requestModel)
		responsesResp := chatCompletionToResponse(resp, fallbackModel)
		applyResponsesRequestSettings(responsesResp, t.streamConverter.requestConfig)
		newBody, err = json.Marshal(responsesResp)
		if err != nil {
			return nil, nil, tokenUsage, fallbackModel, fmt.Errorf("failed to marshal response: %w", err)
		}
		if span != nil {
			span.RecordResponse(responsesResp)
		}
		return withContentLengthHeader(innerHeaders, len(newBody)), newBody, tokenUsage, fallbackModel, nil
	}

	t.streamConverter.requestModel = cmp.Or(innerModel, t.requestModel)
	newBody = []byte{}
	t.buffered = append(t.buffered, chatData...)
	for {
		i := bytes.IndexByte(t.buffered, '\n')
		if i == -1 {
			break
		}
		line := t.buffered[:i]
		t.buffered = t.buffered[i+1:]
		newBody = append(newBody, t.streamConverter.processLine(line)...)
	}
	if endOfStream && t.streamConverter.finished {
		newBody = t.streamConverter.appendTerminalEvent(newBody, t.streamConverter.pendingStatus, nil)
	}
	if span != nil {
		for line := range bytes.SplitSeq(newBody, []byte("\n")) {
			if data, ok := cutSSEDataPrefix(line); ok {
				var event openai.ResponseStreamEventUnion
				if err := json.Unmarshal(data, &event); err == nil {
					span.RecordResponseChunk(&event)
				}
			}
		}
	}
	responseModel = cmp.Or(t.streamConverter.model, innerModel, t.requestModel)
	return innerHeaders, newBody, tokenUsage, responseModel, innerErr
}

func (t *responsesViaChatCompletionTranslator) ResponseError(respHeaders map[string]string, body io.Reader) ([]internalapi.Header, []byte, error) {
	return t.inner.ResponseError(respHeaders, body)
}

func withContentLengthHeader(headers []internalapi.Header, bodyLen int) []internalapi.Header {
	for i := range headers {
		if headers[i].Key() == contentLengthHeaderName {
			headers[i] = internalapi.Header{contentLengthHeaderName, strconv.Itoa(bodyLen)}
			return headers
		}
	}
	return append(headers, internalapi.Header{contentLengthHeaderName, strconv.Itoa(bodyLen)})
}

func convertOutputMessage(msg *openai.ResponseOutputMessage) openai.ChatCompletionMessageParamUnion {
	var textParts []string
	if msg.Content.OfString != nil {
		textParts = append(textParts, *msg.Content.OfString)
	} else if msg.Content.OfContentArray != nil {
		for _, block := range msg.Content.OfContentArray {
			if block.OfOutputText != nil {
				textParts = append(textParts, block.OfOutputText.Text)
			}
			if block.OfRefusal != nil {
				textParts = append(textParts, block.OfRefusal.Refusal)
			}
		}
	}
	return openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			Role:    openai.ChatMessageRoleAssistant,
			Content: openai.StringOrAssistantRoleContentUnion{Value: strings.Join(textParts, "\n\n")},
		},
	}
}
