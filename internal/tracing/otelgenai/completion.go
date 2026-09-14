// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"strconv"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// The legacy completions API has prompts and choices rather than a
// conversation. OpenInference gave these their own attribute families
// (llm.prompts, llm.choices), but the GenAI conventions define only
// gen_ai.input.messages and gen_ai.output.messages for content.
//
// So each prompt becomes a single-part user message and each choice an
// assistant message. This interprets the existing content within a
// spec-defined attribute rather than minting a new gen_ai.* name, which would
// squat a namespace the specification owns.

// completionInputMessages converts prompts into user messages.
//
// The prompt field accepts a string, a list of strings, or pre-tokenized
// integer forms. Token arrays are recorded by count rather than decoded,
// because the gateway has no tokenizer for the target model.
func completionInputMessages(req *openai.CompletionRequest) []message {
	prompts := completionPrompts(req.Prompt.Value)
	msgs := make([]message, 0, len(prompts))
	for _, prompt := range prompts {
		msgs = append(msgs, message{Role: "user", Parts: textPart(prompt)})
	}
	return msgs
}

func completionPrompts(prompt any) []string {
	switch p := prompt.(type) {
	case string:
		if p == "" {
			return nil
		}
		return []string{p}
	case []string:
		return p
	case []int64:
		return []string{tokenPlaceholder(len(p))}
	case [][]int64:
		out := make([]string, 0, len(p))
		for _, tokens := range p {
			out = append(out, tokenPlaceholder(len(tokens)))
		}
		return out
	default:
		return nil
	}
}

// tokenPlaceholder describes a pre-tokenized prompt without inventing text for
// it. The count is useful; a decoded guess would not be.
func tokenPlaceholder(count int) string {
	return "<" + strconv.Itoa(count) + " tokens>"
}

// completionOutputMessages converts choices into assistant messages.
func completionOutputMessages(resp *openai.CompletionResponse) []message {
	msgs := make([]message, 0, len(resp.Choices))
	for i := range resp.Choices {
		choice := &resp.Choices[i]
		m := message{
			Role:         "assistant",
			Parts:        textPart(choice.Text),
			FinishReason: choice.FinishReason,
		}
		msgs = append(msgs, m)
	}
	return msgs
}
