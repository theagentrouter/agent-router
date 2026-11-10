// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// The GenAI conventions encode messages as a single JSON array per direction,
// rather than one indexed attribute per message. Each message has a role and an
// ordered list of typed parts.
//
// See: https://github.com/open-telemetry/semantic-conventions-genai

// Part type discriminators defined by the conventions.
const (
	partTypeText             = "text"
	partTypeToolCall         = "tool_call"
	partTypeToolCallResponse = "tool_call_response"
	// Non-text modalities and reasoning blocks are recorded by type with no
	// content, so the shape of the conversation stays visible without embedding
	// binary payloads or reasoning traces in a span attribute.
	partTypeImage     = "image"
	partTypeAudio     = "audio"
	partTypeReasoning = "reasoning"
)

// messagePart is one element of a message's parts array. The fields are a union
// discriminated by Type; unused ones are omitted.
type messagePart struct {
	Type string `json:"type"`
	// Content holds the text for text parts and the result for tool responses.
	Content string `json:"content,omitempty"`
	// ID identifies a tool call or the call a response belongs to.
	ID string `json:"id,omitempty"`
	// Name is the invoked tool's name.
	Name string `json:"name,omitempty"`
	// Arguments carries the tool call arguments as provided by the model.
	Arguments string `json:"arguments,omitempty"`
}

// message is one entry of gen_ai.input.messages or gen_ai.output.messages.
type message struct {
	Role  string        `json:"role"`
	Parts []messagePart `json:"parts"`
	// FinishReason is set on output messages only.
	FinishReason string `json:"finish_reason,omitempty"`
}

// messagesAttr marshals messages into the given attribute, returning no
// attribute when there is nothing to record. Marshaling failures are dropped
// rather than partially recorded: a truncated or malformed JSON attribute is
// worse than an absent one, because backends fail to parse it.
func messagesAttr(key string, msgs []message) []attribute.KeyValue {
	if len(msgs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(msgs)
	if err != nil {
		return nil
	}
	return []attribute.KeyValue{attribute.String(key, string(encoded))}
}

// partsAttr marshals a bare parts array, which is the shape
// gen_ai.system_instructions takes.
func partsAttr(key string, parts []messagePart) []attribute.KeyValue {
	if len(parts) == 0 {
		return nil
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return nil
	}
	return []attribute.KeyValue{attribute.String(key, string(encoded))}
}

// chatInputMessages converts an OpenAI chat request into convention messages.
//
// System and developer messages are included here rather than split into
// gen_ai.system_instructions, because the gateway forwards them as ordinary
// messages and their position in the conversation is meaningful.
func chatInputMessages(req *openai.ChatCompletionRequest) []message {
	msgs := make([]message, 0, len(req.Messages))
	for i := range req.Messages {
		if m, ok := chatInputMessage(&req.Messages[i]); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

func chatInputMessage(msg *openai.ChatCompletionMessageParamUnion) (message, bool) {
	switch {
	case msg.OfUser != nil:
		return message{Role: msg.OfUser.Role, Parts: userParts(msg.OfUser.Content.Value)}, true
	case msg.OfSystem != nil:
		return message{Role: msg.OfSystem.Role, Parts: textParts(msg.OfSystem.Content.Value)}, true
	case msg.OfDeveloper != nil:
		return message{Role: msg.OfDeveloper.Role, Parts: textParts(msg.OfDeveloper.Content.Value)}, true
	case msg.OfTool != nil:
		return message{
			Role: msg.OfTool.Role,
			Parts: []messagePart{{
				Type:    partTypeToolCallResponse,
				ID:      msg.OfTool.ToolCallID,
				Content: firstText(textParts(msg.OfTool.Content.Value)),
			}},
		}, true
	case msg.OfAssistant != nil:
		return message{Role: msg.OfAssistant.Role, Parts: assistantParts(msg.OfAssistant)}, true
	default:
		return message{}, false
	}
}

// userParts handles user content, which is either a plain string or an ordered
// list of typed parts. Non-text parts such as images are represented by their
// type with no content, so the shape of the conversation stays visible without
// embedding binary payloads in a span attribute.
func userParts(content any) []messagePart {
	switch c := content.(type) {
	case string:
		return textPart(c)
	case []openai.ChatCompletionContentPartUserUnionParam:
		parts := make([]messagePart, 0, len(c))
		for i := range c {
			switch {
			case c[i].OfText != nil:
				parts = append(parts, messagePart{Type: partTypeText, Content: c[i].OfText.Text})
			case c[i].OfImageURL != nil:
				parts = append(parts, messagePart{Type: partTypeImage})
			case c[i].OfInputAudio != nil:
				parts = append(parts, messagePart{Type: partTypeAudio})
			}
		}
		return parts
	default:
		return nil
	}
}

func textParts(content any) []messagePart {
	switch c := content.(type) {
	case string:
		return textPart(c)
	case []openai.ChatCompletionContentPartTextParam:
		parts := make([]messagePart, 0, len(c))
		for i := range c {
			parts = append(parts, messagePart{Type: partTypeText, Content: c[i].Text})
		}
		return parts
	default:
		return nil
	}
}

func assistantParts(msg *openai.ChatCompletionAssistantMessageParam) []messagePart {
	var parts []messagePart
	switch c := msg.Content.Value.(type) {
	case string:
		parts = append(parts, textPart(c)...)
	case []openai.ChatCompletionAssistantMessageParamContent:
		for i := range c {
			if c[i].Text != nil {
				parts = append(parts, messagePart{Type: partTypeText, Content: *c[i].Text})
			}
		}
	}
	for i := range msg.ToolCalls {
		call := &msg.ToolCalls[i]
		part := messagePart{
			Type:      partTypeToolCall,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		}
		if call.ID != nil {
			part.ID = *call.ID
		}
		parts = append(parts, part)
	}
	return parts
}

// chatOutputMessages converts an OpenAI chat response into convention messages.
func chatOutputMessages(resp *openai.ChatCompletionResponse) []message {
	msgs := make([]message, 0, len(resp.Choices))
	for i := range resp.Choices {
		choice := &resp.Choices[i]
		m := message{
			Role:         choice.Message.Role,
			FinishReason: string(choice.FinishReason),
		}
		if choice.Message.Content != nil && *choice.Message.Content != "" {
			m.Parts = append(m.Parts, messagePart{Type: partTypeText, Content: *choice.Message.Content})
		}
		for j := range choice.Message.ToolCalls {
			call := &choice.Message.ToolCalls[j]
			part := messagePart{
				Type:      partTypeToolCall,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
			if call.ID != nil {
				part.ID = *call.ID
			}
			m.Parts = append(m.Parts, part)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func textPart(s string) []messagePart {
	if s == "" {
		return nil
	}
	return []messagePart{{Type: partTypeText, Content: s}}
}

func firstText(parts []messagePart) string {
	for _, p := range parts {
		if p.Type == partTypeText {
			return p.Content
		}
	}
	return ""
}
