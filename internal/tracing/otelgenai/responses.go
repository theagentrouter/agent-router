// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"cmp"

	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// The Responses API is chat-shaped, so it reuses the shared message types.
// Sampling parameters are read from the request rather than the echoed
// response, because the response models temperature and top_p as non-pointer
// floats where zero is indistinguishable from unset.

func responsesRequestAttrs(req *openai.ResponseRequest) []attribute.KeyValue {
	var p params
	p.float64(RequestTemperature, req.Temperature)
	p.float64(RequestTopP, req.TopP)
	p.int64(RequestMaxTokens, req.MaxOutputTokens)
	return p.attrs
}

func responsesResponseAttrs(resp *openai.Response) []attribute.KeyValue {
	attrs := responseIdentityAttrs(resp.ID, resp.Model)
	if u := resp.Usage; u != nil {
		attrs = append(attrs, usageAttrs(int(u.InputTokens), int(u.OutputTokens))...)
		attrs = append(attrs, usageDetailAttrs(
			int(u.InputTokensDetails.CachedTokens), 0,
			int(u.OutputTokensDetails.ReasoningTokens))...)
	}
	return attrs
}

// responsesFoldChunks reconstructs the response from a streamed exchange.
//
// The terminal event (response.completed, response.incomplete or
// response.failed) carries the fully assembled response, so folding is a
// matter of taking the last one. A stream cut off before its terminal event
// yields an empty response, which records nothing rather than partial data.
func responsesFoldChunks(chunks []*openai.ResponseStreamEventUnion) *openai.Response {
	for i := len(chunks) - 1; i >= 0; i-- {
		c := chunks[i]
		if c == nil {
			continue
		}
		switch {
		case c.OfResponseCompleted != nil:
			return &c.OfResponseCompleted.Response
		case c.OfResponseIncomplete != nil:
			return &c.OfResponseIncomplete.Response
		case c.OfResponseFailed != nil:
			return &c.OfResponseFailed.Response
		}
	}
	return &openai.Response{}
}

// responsesInputMessages converts the input into convention messages.
//
// A bare string is a single user message. In the item form, messages and tool
// outputs each map to one message, while output items replayed from a prior
// turn go through responsesOutputMessages, so a turn is recorded the same way
// whether it is being produced or replayed. Messages with no parts are dropped.
func responsesInputMessages(req *openai.ResponseRequest) []message {
	items := req.Input.OfInputItemList
	if s := req.Input.OfString; s != nil {
		items = []openai.ResponseInputItemUnionParam{{OfMessage: &openai.EasyInputMessageParam{
			Role:    "user",
			Content: openai.EasyInputMessageContentUnionParam{OfString: s},
		}}}
	}

	var msgs []message
	var replayed []openai.ResponseOutputItemUnion
	for i := range items {
		item := &items[i]
		if out, ok := responsesReplayedOutputItem(item); ok {
			replayed = append(replayed, out)
			continue
		}
		msgs = append(msgs, responsesReplayedMessages(replayed)...)
		replayed = nil
		if m, ok := responsesInputMessage(item); ok && len(m.Parts) > 0 {
			msgs = append(msgs, m)
		}
	}
	return append(msgs, responsesReplayedMessages(replayed)...)
}

// responsesInputMessage maps an item that is a message in its own right.
func responsesInputMessage(item *openai.ResponseInputItemUnionParam) (message, bool) {
	switch {
	case item.OfMessage != nil:
		return message{
			Role:  cmp.Or(item.OfMessage.Role, "user"),
			Parts: responsesEasyContentParts(&item.OfMessage.Content),
		}, true
	case item.OfInputMessage != nil:
		return message{
			Role:  cmp.Or(item.OfInputMessage.Role, "user"),
			Parts: responsesInputContentParts(item.OfInputMessage.Content),
		}, true
	case item.OfFunctionCallOutput != nil:
		out := item.OfFunctionCallOutput
		return message{Role: "tool", Parts: []messagePart{{
			Type:    partTypeToolCallResponse,
			ID:      out.CallID,
			Content: responsesCallOutputText(&out.Output),
		}}}, true
	default:
		return message{}, false
	}
}

// responsesReplayedOutputItem recognises an output item replayed from a prior
// turn and returns it in the form the response side maps.
func responsesReplayedOutputItem(item *openai.ResponseInputItemUnionParam) (openai.ResponseOutputItemUnion, bool) {
	switch {
	case item.OfOutputMessage != nil:
		return openai.ResponseOutputItemUnion{OfOutputMessage: item.OfOutputMessage}, true
	case item.OfFunctionCall != nil:
		return openai.ResponseOutputItemUnion{OfFunctionCall: item.OfFunctionCall}, true
	case item.OfReasoning != nil:
		return openai.ResponseOutputItemUnion{OfReasoning: item.OfReasoning}, true
	case item.OfImageGenerationCall != nil:
		// The replayed form is a distinct type; only the fact of a generation is recorded.
		return openai.ResponseOutputItemUnion{OfImageGenerationCall: &openai.ResponseOutputItemImageGenerationCall{}}, true
	default:
		return openai.ResponseOutputItemUnion{}, false
	}
}

func responsesReplayedMessages(items []openai.ResponseOutputItemUnion) []message {
	return responsesOutputMessages(&openai.Response{Output: items})
}

func responsesEasyContentParts(content *openai.EasyInputMessageContentUnionParam) []messagePart {
	if content.OfString != nil {
		return textPart(*content.OfString)
	}
	return responsesInputContentParts(content.OfInputItemContentList)
}

// responsesInputContentParts maps request-side content blocks. Images are
// recorded by type only, as on the chat path.
func responsesInputContentParts(items []openai.ResponseInputContentUnionParam) []messagePart {
	parts := make([]messagePart, 0, len(items))
	for i := range items {
		switch item := &items[i]; {
		case item.OfInputText != nil:
			parts = append(parts, messagePart{Type: partTypeText, Content: item.OfInputText.Text})
		case item.OfInputImage != nil:
			parts = append(parts, messagePart{Type: partTypeImage})
		}
	}
	return parts
}

// responsesCallOutputText renders a function call output as text. The array
// form takes its first text block, as the chat path does for tool results.
func responsesCallOutputText(out *openai.ResponseInputItemFunctionCallOutputOutputUnionParam) string {
	if out.OfString != nil {
		return *out.OfString
	}
	for i := range out.OfResponseFunctionCallOutputItemArray {
		if t := out.OfResponseFunctionCallOutputItemArray[i].OfInputText; t != nil {
			return t.Text
		}
	}
	return ""
}

// responsesConversationID reads the conversation this request continues.
//
// The union carries either a bare id string or an object holding one.
func responsesConversationID(req *openai.ResponseRequest) string {
	if id := req.Conversation.OfString; id != nil {
		return *id
	}
	if c := req.Conversation.OfConversationObject; c != nil {
		return c.ID
	}
	return ""
}

// responsesSystemInstructions maps the instructions field, which this API
// models separately from the conversation.
func responsesSystemInstructions(req *openai.ResponseRequest) []messagePart {
	return textPart(req.Instructions)
}

// responsesOutputMessages converts the output items into convention messages.
//
// Output items are already a typed union, so each maps to a part by kind.
// Reasoning items are recorded by type only, matching how Anthropic thinking
// blocks are handled.
func responsesOutputMessages(resp *openai.Response) []message {
	m := message{Role: "assistant"}
	for i := range resp.Output {
		item := &resp.Output[i]
		switch {
		case item.OfOutputMessage != nil:
			if item.OfOutputMessage.Role != "" {
				m.Role = item.OfOutputMessage.Role
			}
			m.Parts = append(m.Parts, responsesContentParts(&item.OfOutputMessage.Content)...)
		case item.OfFunctionCall != nil:
			call := item.OfFunctionCall
			m.Parts = append(m.Parts, messagePart{
				Type:      partTypeToolCall,
				ID:        call.CallID,
				Name:      call.Name,
				Arguments: call.Arguments,
			})
		case item.OfReasoning != nil:
			m.Parts = append(m.Parts, messagePart{Type: partTypeReasoning})
		case item.OfImageGenerationCall != nil:
			m.Parts = append(m.Parts, messagePart{Type: partTypeImage})
		}
	}
	if len(m.Parts) == 0 {
		return nil
	}
	return []message{m}
}

func responsesContentParts(content *openai.ResponseOutputMessageContentUnion) []messagePart {
	if content.OfString != nil {
		return textPart(*content.OfString)
	}
	parts := make([]messagePart, 0, len(content.OfContentArray))
	for i := range content.OfContentArray {
		if text := content.OfContentArray[i].OfOutputText; text != nil {
			parts = append(parts, messagePart{Type: partTypeText, Content: text.Text})
		}
	}
	return parts
}
