// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

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
		ID:        chatResp.ID,
		Object:    "response",
		Model:     cmp.Or(chatResp.Model, requestModel),
		CreatedAt: chatResp.Created,
		Status:    "completed",
		Text:      defaultTextConfig,
	}

	if chatResp.Usage.PromptTokens != 0 || chatResp.Usage.CompletionTokens != 0 || chatResp.Usage.TotalTokens != 0 {
		resp.Usage = &openai.ResponseUsage{
			InputTokens:  int64(chatResp.Usage.PromptTokens),
			OutputTokens: int64(chatResp.Usage.CompletionTokens),
			TotalTokens:  int64(chatResp.Usage.TotalTokens),
		}
	}

	for i := range chatResp.Choices {
		choice := &chatResp.Choices[i]
		status := "completed"
		switch choice.FinishReason {
		case openai.ChatCompletionChoicesFinishReasonLength:
			status = "incomplete"
			resp.Status = "incomplete"
		case openai.ChatCompletionChoicesFinishReasonStop, openai.ChatCompletionChoicesFinishReasonToolCalls:
			status = "completed"
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

		if choice.Message.Content != nil && *choice.Message.Content != "" {
			resp.Output = append(resp.Output, openai.ResponseOutputItemUnion{
				OfOutputMessage: &openai.ResponseOutputMessage{
					ID:   fmt.Sprintf("msg_%s", chatResp.ID),
					Role: "assistant",
					Type: "message",
					Content: openai.ResponseOutputMessageContentUnion{
						OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
							{OfOutputText: &openai.ResponseOutputTextParam{
								Text: *choice.Message.Content,
								Type: "output_text",
							}},
						},
					},
					Status: status,
				},
			})
		}
	}

	return resp
}

type streamingToolCallState struct {
	id        string
	name      string
	arguments strings.Builder
}

type chatCompletionStreamToResponsesConverter struct {
	started        bool
	responseID     string
	model          string
	created        openai.JSONUNIXTime
	seqNum         int64
	messageStarted bool
	messageID      string
	accText        strings.Builder
	outputIndex    int64
	toolCalls      map[int64]*streamingToolCallState
	finished       bool
	pendingStatus  string
	requestModel   string
}

func (c *chatCompletionStreamToResponsesConverter) nextSeq() int64 {
	c.seqNum++
	return c.seqNum
}

func (c *chatCompletionStreamToResponsesConverter) emptyResponse() openai.Response {
	return openai.Response{
		ID:        c.responseID,
		Object:    "response",
		Model:     cmp.Or(c.model, c.requestModel),
		CreatedAt: c.created,
		Status:    "in_progress",
		Text:      defaultTextConfig,
	}
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

func (c *chatCompletionStreamToResponsesConverter) appendCompletedEvent(output []byte, status string, usage *openai.Usage) []byte {
	resp := c.buildFinalResponse(status, usage)
	output = append(output, marshalSSEEvent("response.completed", openai.ResponseCompletedEvent{
		Response:       resp,
		SequenceNumber: c.nextSeq(),
		Type:           "response.completed",
	})...)
	c.finished = false
	c.pendingStatus = ""
	return output
}

func (c *chatCompletionStreamToResponsesConverter) processLine(line []byte) (output []byte, usage *openai.Usage) {
	if !bytes.HasPrefix(line, sseDataPrefix) {
		return nil, nil
	}
	data := bytes.TrimPrefix(line, sseDataPrefix)
	if bytes.Equal(data, sseDoneMessage) {
		return nil, nil
	}

	chunk := &openai.ChatCompletionResponseChunk{}
	if err := json.Unmarshal(data, chunk); err != nil {
		return nil, nil
	}

	if chunk.Model != "" {
		c.model = chunk.Model
	}

	if !c.started {
		c.started = true
		c.responseID = cmp.Or(chunk.ID, fmt.Sprintf("resp_%d", time.Now().UnixNano()))
		c.created = chunk.Created
		c.messageID = fmt.Sprintf("msg_%s", c.responseID)

		resp := c.emptyResponse()
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

	if chunk.Usage != nil {
		usage = chunk.Usage
		if c.finished {
			output = c.appendCompletedEvent(output, c.pendingStatus, usage)
		}
	}

	if len(chunk.Choices) == 0 {
		return output, usage
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	if delta != nil && delta.Content != nil && *delta.Content != "" {
		if !c.messageStarted {
			c.messageStarted = true
			output = append(output, marshalSSEEvent("response.output_item.added", openai.ResponseOutputItemAddedEvent{
				Item: openai.ResponseOutputItemUnion{
					OfOutputMessage: &openai.ResponseOutputMessage{
						ID:   c.messageID,
						Role: "assistant",
						Type: "message",
						Content: openai.ResponseOutputMessageContentUnion{
							OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{},
						},
						Status: "in_progress",
					},
				},
				OutputIndex:    c.outputIndex,
				SequenceNumber: c.nextSeq(),
				Type:           "response.output_item.added",
			})...)
			output = append(output, marshalSSEEvent("response.content_part.added", openai.ResponseContentPartAddedEvent{
				ContentIndex: 0,
				ItemID:       c.messageID,
				OutputIndex:  c.outputIndex,
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
			ContentIndex:   0,
			Delta:          *delta.Content,
			ItemID:         c.messageID,
			OutputIndex:    c.outputIndex,
			SequenceNumber: c.nextSeq(),
			Type:           "response.output_text.delta",
		})...)
	}

	if delta != nil && len(delta.ToolCalls) > 0 {
		if c.toolCalls == nil {
			c.toolCalls = make(map[int64]*streamingToolCallState)
		}
		for _, tc := range delta.ToolCalls {
			state, exists := c.toolCalls[tc.Index]
			if !exists {
				state = &streamingToolCallState{}
				if tc.ID != nil {
					state.id = *tc.ID
				}
				state.name = tc.Function.Name
				c.toolCalls[tc.Index] = state

				itemID := cmp.Or(state.id, fmt.Sprintf("fc_%d", tc.Index))
				output = append(output, marshalSSEEvent("response.output_item.added", openai.ResponseOutputItemAddedEvent{
					Item: openai.ResponseOutputItemUnion{
						OfFunctionCall: &openai.ResponseFunctionToolCall{
							ID:        itemID,
							CallID:    state.id,
							Name:      state.name,
							Arguments: "",
							Type:      "function_call",
							Status:    "in_progress",
						},
					},
					OutputIndex:    int64(len(c.toolCalls)) - 1 + boolToInt64(c.messageStarted),
					SequenceNumber: c.nextSeq(),
					Type:           "response.output_item.added",
				})...)
			}
			if tc.Function.Arguments != "" {
				state.arguments.WriteString(tc.Function.Arguments)
				itemID := cmp.Or(state.id, fmt.Sprintf("fc_%d", tc.Index))
				outIdx := tc.Index + boolToInt64(c.messageStarted)
				output = append(output, marshalSSEEvent("response.function_call_arguments.delta", openai.ResponseFunctionCallArgumentsDeltaEvent{
					Delta:          tc.Function.Arguments,
					ItemID:         itemID,
					OutputIndex:    outIdx,
					SequenceNumber: c.nextSeq(),
					Type:           "response.function_call_arguments.delta",
				})...)
			}
		}
	}

	if choice.FinishReason != "" {
		switch choice.FinishReason {
		case openai.ChatCompletionChoicesFinishReasonStop, openai.ChatCompletionChoicesFinishReasonLength:
			if c.messageStarted {
				status := "completed"
				if choice.FinishReason == openai.ChatCompletionChoicesFinishReasonLength {
					status = "incomplete"
				}
				output = append(output, marshalSSEEvent("response.output_text.done", openai.ResponseTextDoneEvent{
					ContentIndex:   0,
					ItemID:         c.messageID,
					OutputIndex:    c.outputIndex,
					SequenceNumber: c.nextSeq(),
					Text:           c.accText.String(),
					Type:           "response.output_text.done",
				})...)
				output = append(output, marshalSSEEvent("response.content_part.done", openai.ResponseContentPartDoneEvent{
					ContentIndex: 0,
					ItemID:       c.messageID,
					OutputIndex:  c.outputIndex,
					Part: openai.ResponseContentPartDoneEventPartUnion{
						OfResponseOutputText: &openai.ResponseOutputTextParam{
							Text: c.accText.String(),
							Type: "output_text",
						},
					},
					SequenceNumber: c.nextSeq(),
					Type:           "response.content_part.done",
				})...)
				output = append(output, marshalSSEEvent("response.output_item.done", openai.ResponseOutputItemDoneEvent{
					Item: openai.ResponseOutputItemUnion{
						OfOutputMessage: &openai.ResponseOutputMessage{
							ID:   c.messageID,
							Role: "assistant",
							Type: "message",
							Content: openai.ResponseOutputMessageContentUnion{
								OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
									{OfOutputText: &openai.ResponseOutputTextParam{Text: c.accText.String(), Type: "output_text"}},
								},
							},
							Status: status,
						},
					},
					OutputIndex:    c.outputIndex,
					SequenceNumber: c.nextSeq(),
					Type:           "response.output_item.done",
				})...)
			}
		case openai.ChatCompletionChoicesFinishReasonToolCalls:
			for _, idx := range sortedToolCallKeys(c.toolCalls) {
				state := c.toolCalls[idx]
				itemID := cmp.Or(state.id, fmt.Sprintf("fc_%d", idx))
				outIdx := idx + boolToInt64(c.messageStarted)
				output = append(output, marshalSSEEvent("response.function_call_arguments.done", openai.ResponseFunctionCallArgumentsDoneEvent{
					Arguments:      state.arguments.String(),
					ItemID:         itemID,
					Name:           state.name,
					OutputIndex:    outIdx,
					SequenceNumber: c.nextSeq(),
					Type:           "response.function_call_arguments.done",
				})...)
				output = append(output, marshalSSEEvent("response.output_item.done", openai.ResponseOutputItemDoneEvent{
					Item: openai.ResponseOutputItemUnion{
						OfFunctionCall: &openai.ResponseFunctionToolCall{
							ID:        itemID,
							CallID:    state.id,
							Name:      state.name,
							Arguments: state.arguments.String(),
							Type:      "function_call",
							Status:    "completed",
						},
					},
					OutputIndex:    outIdx,
					SequenceNumber: c.nextSeq(),
					Type:           "response.output_item.done",
				})...)
			}
		}

		responseStatus := "completed"
		if choice.FinishReason == openai.ChatCompletionChoicesFinishReasonLength {
			responseStatus = "incomplete"
		}
		if usage != nil {
			output = c.appendCompletedEvent(output, responseStatus, usage)
		} else {
			c.finished = true
			c.pendingStatus = responseStatus
		}
	}

	return output, usage
}

func (c *chatCompletionStreamToResponsesConverter) buildFinalResponse(status string, usage *openai.Usage) openai.Response {
	resp := openai.Response{
		ID:        c.responseID,
		Object:    "response",
		Model:     cmp.Or(c.model, c.requestModel),
		CreatedAt: c.created,
		Status:    status,
		Text:      defaultTextConfig,
	}
	if usage != nil {
		resp.Usage = &openai.ResponseUsage{
			InputTokens:  int64(usage.PromptTokens),
			OutputTokens: int64(usage.CompletionTokens),
			TotalTokens:  int64(usage.TotalTokens),
		}
	}

	var outputItems []openai.ResponseOutputItemUnion
	if c.messageStarted {
		outputItems = append(outputItems, openai.ResponseOutputItemUnion{
			OfOutputMessage: &openai.ResponseOutputMessage{
				ID:   c.messageID,
				Role: "assistant",
				Type: "message",
				Content: openai.ResponseOutputMessageContentUnion{
					OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
						{OfOutputText: &openai.ResponseOutputTextParam{Text: c.accText.String(), Type: "output_text"}},
					},
				},
				Status: "completed",
			},
		})
	}
	for _, idx := range sortedToolCallKeys(c.toolCalls) {
		state := c.toolCalls[idx]
		itemID := cmp.Or(state.id, fmt.Sprintf("fc_%d", idx))
		outputItems = append(outputItems, openai.ResponseOutputItemUnion{
			OfFunctionCall: &openai.ResponseFunctionToolCall{
				ID:        itemID,
				CallID:    state.id,
				Name:      state.name,
				Arguments: state.arguments.String(),
				Type:      "function_call",
				Status:    "completed",
			},
		})
	}
	resp.Output = outputItems
	return resp
}

func sortedToolCallKeys(toolCalls map[int64]*streamingToolCallState) []int64 {
	keys := make([]int64, 0, len(toolCalls))
	for idx := range toolCalls {
		keys = append(keys, idx)
	}
	slices.Sort(keys)
	return keys
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func responsesRequestToChatCompletionRequest(req *openai.ResponseRequest, model string) openai.ChatCompletionRequest {
	chatReq := openai.ChatCompletionRequest{
		Model:               model,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		MaxCompletionTokens: req.MaxOutputTokens,
		Stream:              req.Stream,
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
		messages = convertInputItemsToMessages(req.Input.OfInputItemList, messages)
	}

	chatReq.Messages = messages

	for i := range req.Tools {
		tool := &req.Tools[i]
		if tool.OfFunction != nil {
			chatReq.Tools = append(chatReq.Tools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        tool.OfFunction.Name,
					Description: tool.OfFunction.Description,
					Parameters:  tool.OfFunction.Parameters,
				},
			})
		}
	}

	return chatReq
}

func convertInputItemsToMessages(items []openai.ResponseInputItemUnionParam, messages []openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	var pendingToolCalls []openai.ChatCompletionMessageToolCallParam

	for i := range items {
		item := &items[i]
		switch {
		case item.OfMessage != nil:
			if len(pendingToolCalls) > 0 {
				messages = appendToolCallsAsAssistantMessage(messages, pendingToolCalls)
				pendingToolCalls = nil
			}
			messages = append(messages, convertEasyInputMessage(item.OfMessage))

		case item.OfInputMessage != nil:
			if len(pendingToolCalls) > 0 {
				messages = appendToolCallsAsAssistantMessage(messages, pendingToolCalls)
				pendingToolCalls = nil
			}
			messages = append(messages, convertInputItemMessage(item.OfInputMessage))

		case item.OfOutputMessage != nil:
			if len(pendingToolCalls) > 0 {
				messages = appendToolCallsAsAssistantMessage(messages, pendingToolCalls)
				pendingToolCalls = nil
			}
			messages = append(messages, convertOutputMessage(item.OfOutputMessage))

		case item.OfFunctionCall != nil:
			fc := item.OfFunctionCall
			pendingToolCalls = append(pendingToolCalls, openai.ChatCompletionMessageToolCallParam{
				ID: ptr.To(fc.CallID),
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      fc.Name,
					Arguments: fc.Arguments,
				},
				Type: openai.ChatCompletionMessageToolCallTypeFunction,
			})

		case item.OfFunctionCallOutput != nil:
			if len(pendingToolCalls) > 0 {
				messages = appendToolCallsAsAssistantMessage(messages, pendingToolCalls)
				pendingToolCalls = nil
			}
			fco := item.OfFunctionCallOutput
			var content string
			if fco.Output.OfString != nil {
				content = *fco.Output.OfString
			}
			messages = append(messages, openai.ChatCompletionMessageParamUnion{
				OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					Content:    openai.ContentUnion{Value: content},
					ToolCallID: fco.CallID,
				},
			})
		}
	}

	if len(pendingToolCalls) > 0 {
		messages = appendToolCallsAsAssistantMessage(messages, pendingToolCalls)
	}

	return messages
}

func appendToolCallsAsAssistantMessage(messages []openai.ChatCompletionMessageParamUnion, toolCalls []openai.ChatCompletionMessageToolCallParam) []openai.ChatCompletionMessageParamUnion {
	n := len(messages)
	if n > 0 && messages[n-1].OfAssistant != nil {
		messages[n-1].OfAssistant.ToolCalls = append(messages[n-1].OfAssistant.ToolCalls, toolCalls...)
		return messages
	}
	return append(messages, openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			Role:      openai.ChatMessageRoleAssistant,
			ToolCalls: toolCalls,
		},
	})
}

func convertEasyInputMessage(msg *openai.EasyInputMessageParam) openai.ChatCompletionMessageParamUnion {
	contentValue := extractEasyInputText(msg.Content)
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

func extractEasyInputText(content openai.EasyInputMessageContentUnionParam) string {
	if content.OfString != nil {
		return *content.OfString
	}
	if content.OfInputItemContentList != nil {
		var parts []string
		for _, c := range content.OfInputItemContentList {
			if c.OfInputText != nil {
				parts = append(parts, c.OfInputText.Text)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func convertInputItemMessage(msg *openai.ResponseInputItemMessageParam) openai.ChatCompletionMessageParamUnion {
	switch msg.Role {
	case openai.ChatMessageRoleUser:
		var parts []openai.ChatCompletionContentPartUserUnionParam
		for _, c := range msg.Content {
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
		return openai.ChatCompletionMessageParamUnion{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: parts},
			},
		}
	case openai.ChatMessageRoleAssistant:
		return openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Role:    openai.ChatMessageRoleAssistant,
				Content: openai.StringOrAssistantRoleContentUnion{Value: extractInputContentText(msg.Content)},
			},
		}
	case openai.ChatMessageRoleSystem:
		return openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Role:    openai.ChatMessageRoleSystem,
				Content: openai.ContentUnion{Value: extractInputContentText(msg.Content)},
			},
		}
	case openai.ChatMessageRoleDeveloper:
		return openai.ChatCompletionMessageParamUnion{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: openai.ContentUnion{Value: extractInputContentText(msg.Content)},
			},
		}
	default:
		return openai.ChatCompletionMessageParamUnion{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Role:    openai.ChatMessageRoleUser,
				Content: openai.StringOrUserRoleContentUnion{Value: extractInputContentText(msg.Content)},
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
	inner                  OpenAIChatCompletionTranslator
	requestModel           internalapi.RequestModel
	stream                 bool
	buffered               []byte
	streamConverter        chatCompletionStreamToResponsesConverter
	streamingResponseModel internalapi.ResponseModel
}

func (t *responsesViaChatCompletionTranslator) RequestBody(_ []byte, req *openai.ResponseRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	t.requestModel = req.Model
	t.stream = req.Stream
	t.streamConverter.requestModel = req.Model

	chatReq := responsesRequestToChatCompletionRequest(req, req.Model)

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

func (t *responsesViaChatCompletionTranslator) ResponseBody(respHeaders map[string]string, body io.Reader, endOfStream bool, _ tracingapi.ResponsesSpan) (
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
	if innerErr != nil {
		return innerHeaders, innerBody, tokenUsage, cmp.Or(innerModel, t.requestModel), innerErr
	}

	chatData := innerBody
	if len(chatData) == 0 {
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
		newBody, err = json.Marshal(responsesResp)
		if err != nil {
			return nil, nil, tokenUsage, fallbackModel, fmt.Errorf("failed to marshal response: %w", err)
		}
		return withContentLengthHeader(innerHeaders, len(newBody)), newBody, tokenUsage, fallbackModel, nil
	}

	t.buffered = append(t.buffered, chatData...)
	for {
		i := bytes.IndexByte(t.buffered, '\n')
		if i == -1 {
			break
		}
		line := t.buffered[:i]
		t.buffered = t.buffered[i+1:]
		if len(line) == 0 {
			continue
		}
		converted, _ := t.streamConverter.processLine(line)
		if converted != nil {
			newBody = append(newBody, converted...)
		}
	}
	if t.streamConverter.model != "" {
		t.streamingResponseModel = t.streamConverter.model
	}
	responseModel = cmp.Or(t.streamingResponseModel, innerModel, t.requestModel)
	return innerHeaders, newBody, tokenUsage, responseModel, nil
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
