// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// Anthropic messages map onto the same role/parts structure as OpenAI chat
// completions, so they reuse the shared message types in messages.go. Only the
// extraction differs.

// anthropicRequestAttrs builds the sampling parameters for a messages request.
//
// MaxTokens is required and non-pointer in this API, so it is recorded whenever
// it is set to a usable value.
func anthropicRequestAttrs(req *anthropicschema.MessagesRequest) []attribute.KeyValue {
	var p params
	p.float64(RequestTemperature, req.Temperature)
	p.float64(RequestTopP, req.TopP)
	p.int(RequestTopK, req.TopK)
	if req.MaxTokens > 0 {
		p.attrs = append(p.attrs, attribute.Int64(RequestMaxTokens, int64(req.MaxTokens)))
	}
	p.stringSlice(RequestStopSequences, req.StopSequences)
	return p.attrs
}

// anthropicResponseAttrs builds the response identity, usage and stop reason.
func anthropicResponseAttrs(resp *anthropicschema.MessagesResponse) []attribute.KeyValue {
	attrs := responseIdentityAttrs(resp.ID, resp.Model)
	if u := resp.Usage; u != nil {
		attrs = append(attrs, usageAttrs(int(u.InputTokens), int(u.OutputTokens))...)
		attrs = append(attrs, usageDetailAttrs(
			int(u.CacheReadInputTokens), int(u.CacheCreationInputTokens), 0)...)
	}
	if resp.StopReason != nil && *resp.StopReason != "" {
		attrs = append(attrs, attribute.StringSlice(ResponseFinishReasons,
			[]string{string(*resp.StopReason)}))
	}
	return attrs
}

// anthropicInputMessages converts a messages request into convention messages.
//
// The system prompt is a separate field in this API rather than a message, so
// it maps to gen_ai.system_instructions instead of being folded into the
// conversation. This differs from the OpenAI mapping, where system messages
// occupy a position in the message list.
func anthropicInputMessages(req *anthropicschema.MessagesRequest) []message {
	return anthropicMessages(req.Messages)
}

// anthropicMessages converts a conversation into convention messages. It keys
// off the message slice rather than the request, so the messages and
// count_tokens endpoints, which carry the same conversation under different
// request types, cannot describe it differently.
func anthropicMessages(in []anthropicschema.MessageParam) []message {
	msgs := make([]message, 0, len(in))
	for i := range in {
		m := &in[i]
		msgs = append(msgs, message{
			Role:  string(m.Role),
			Parts: anthropicContentParts(&m.Content),
		})
	}
	return msgs
}

// anthropicSystemInstructions extracts the system prompt, which this API models
// as either a plain string or a list of text blocks.
func anthropicSystemInstructions(req *anthropicschema.MessagesRequest) []messagePart {
	return anthropicSystemParts(req.System)
}

// anthropicSystemParts converts a system prompt, shared by the endpoints that
// accept one.
func anthropicSystemParts(system *anthropicschema.SystemPrompt) []messagePart {
	if system == nil {
		return nil
	}
	if system.Text != "" {
		return textPart(system.Text)
	}
	parts := make([]messagePart, 0, len(system.Texts))
	for i := range system.Texts {
		if text := system.Texts[i].Text; text != "" {
			parts = append(parts, messagePart{Type: partTypeText, Content: text})
		}
	}
	return parts
}

// anthropicContentParts converts request content, which is either a plain
// string or an ordered list of typed blocks.
func anthropicContentParts(content *anthropicschema.MessageContent) []messagePart {
	if content.Text != "" {
		return textPart(content.Text)
	}
	parts := make([]messagePart, 0, len(content.Array))
	for i := range content.Array {
		block := &content.Array[i]
		switch {
		case block.Text != nil:
			parts = append(parts, messagePart{Type: partTypeText, Content: block.Text.Text})
		case block.ToolUse != nil:
			parts = append(parts, messagePart{
				Type:      partTypeToolCall,
				ID:        block.ToolUse.ID,
				Name:      block.ToolUse.Name,
				Arguments: marshalToolInput(block.ToolUse.Input),
			})
		case block.ToolResult != nil:
			parts = append(parts, messagePart{
				Type: partTypeToolCallResponse,
				ID:   block.ToolResult.ToolUseID,
			})
		case block.Thinking != nil:
			parts = append(parts, messagePart{Type: partTypeReasoning})
		case block.Image != nil:
			parts = append(parts, messagePart{Type: partTypeImage})
		}
	}
	return parts
}

// anthropicOutputMessages converts a messages response into a single assistant
// message, which is the shape this API always returns.
func anthropicOutputMessages(resp *anthropicschema.MessagesResponse) []message {
	m := message{Role: string(resp.Role)}
	if resp.StopReason != nil {
		m.FinishReason = string(*resp.StopReason)
	}
	for i := range resp.Content {
		block := &resp.Content[i]
		switch {
		case block.Text != nil:
			m.Parts = append(m.Parts, messagePart{Type: partTypeText, Content: block.Text.Text})
		case block.Tool != nil:
			m.Parts = append(m.Parts, messagePart{
				Type:      partTypeToolCall,
				ID:        block.Tool.ID,
				Name:      block.Tool.Name,
				Arguments: marshalToolInput(block.Tool.Input),
			})
		case block.Thinking != nil:
			m.Parts = append(m.Parts, messagePart{Type: partTypeReasoning})
		}
	}
	if len(m.Parts) == 0 && m.FinishReason == "" {
		return nil
	}
	return []message{m}
}

// marshalToolInput renders tool arguments as JSON text, matching the string
// form OpenAI already uses for the same field. An unmarshalable input yields
// the empty string rather than a partial value.
func marshalToolInput(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(encoded)
}
