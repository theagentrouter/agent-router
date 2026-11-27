// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

type decisionInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type decisionInputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func decisionsInputMessages(req *openai.DecisionRequest) []message {
	if len(req.Input) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(req.Input, &text); err == nil {
		return []message{{Role: "user", Parts: textPart(text)}}
	}
	var items []decisionInputMessage
	if err := json.Unmarshal(req.Input, &items); err != nil {
		return nil
	}
	msgs := make([]message, 0, len(items))
	for i := range items {
		role := items[i].Role
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, message{Role: role, Parts: decisionInputParts(items[i].Content)})
	}
	return msgs
}

func decisionInputParts(content json.RawMessage) []messagePart {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return textPart(text)
	}
	var parts []decisionInputPart
	if err := json.Unmarshal(content, &parts); err != nil {
		return nil
	}
	out := make([]messagePart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "input_text":
			out = append(out, messagePart{Type: partTypeText, Content: p.Text})
		case "input_image":
			out = append(out, messagePart{Type: partTypeImage})
		}
	}
	return out
}

// decisionsSystemInstructions maps the questions, which carry the instructions
// the model evaluates the input against, separately from the input itself.
func decisionsSystemInstructions(req *openai.DecisionRequest) []messagePart {
	parts := make([]messagePart, 0, len(req.Questions))
	for i := range req.Questions {
		encoded, err := json.Marshal(req.Questions[i])
		if err != nil {
			continue
		}
		parts = append(parts, messagePart{Type: partTypeText, Content: string(encoded)})
	}
	return parts
}

// decisionsOutputMessages records each answer as a JSON text part, since the
// conventions define no part type for typed decision answers.
func decisionsOutputMessages(resp *openai.DecisionResponse) []message {
	if len(resp.Answers) == 0 {
		return nil
	}
	parts := make([]messagePart, 0, len(resp.Answers))
	for i := range resp.Answers {
		encoded, err := json.Marshal(resp.Answers[i])
		if err != nil {
			continue
		}
		parts = append(parts, messagePart{Type: partTypeText, Content: string(encoded)})
	}
	return []message{{Role: "assistant", Parts: parts}}
}
