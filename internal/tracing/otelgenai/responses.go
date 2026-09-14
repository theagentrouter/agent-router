// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
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
