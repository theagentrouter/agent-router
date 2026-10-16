// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestDecisionsInputMessages(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "absent", input: "", expected: ""},
		{name: "string", input: `"I was charged twice."`, expected: `[{"role":"user","parts":[{"type":"text","content":"I was charged twice."}]}]`},
		{
			name:     "messages with text and image",
			input:    `[{"role":"user","content":[{"type":"input_text","text":"Inspect this."},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]`,
			expected: `[{"role":"user","parts":[{"type":"text","content":"Inspect this."},{"type":"image"}]}]`,
		},
		{name: "message with string content", input: `[{"content":"hello"}]`, expected: `[{"role":"user","parts":[{"type":"text","content":"hello"}]}]`},
		{name: "malformed", input: `{"unexpected":true}`, expected: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := messagesAttr(InputMessages, decisionsInputMessages(&openai.DecisionRequest{Input: []byte(tc.input)}))
			if tc.expected == "" {
				require.Empty(t, attrs)
				return
			}
			require.Len(t, attrs, 1)
			require.JSONEq(t, tc.expected, attrs[0].Value.AsString())
		})
	}
}

func TestDecisionsSystemInstructions(t *testing.T) {
	attrs := partsAttr(SystemInstructions, decisionsSystemInstructions(&openai.DecisionRequest{
		Questions: []openai.DecisionQuestion{
			{Type: "predicate", Name: "has_dent", Instructions: "Does this item have a dent?"},
			{
				Type: "choice", Name: "department", Instructions: "Which department?",
				Choices: []openai.DecisionChoiceOption{{Value: []byte(`"billing"`), Description: "Payments."}},
			},
		},
	}))
	require.Len(t, attrs, 1)
	require.JSONEq(t, `[
		{"type":"text","content":"{\"type\":\"predicate\",\"name\":\"has_dent\",\"instructions\":\"Does this item have a dent?\"}"},
		{"type":"text","content":"{\"type\":\"choice\",\"name\":\"department\",\"instructions\":\"Which department?\",\"choices\":[{\"value\":\"billing\",\"description\":\"Payments.\"}]}"}
	]`, attrs[0].Value.AsString())

	require.Empty(t, partsAttr(SystemInstructions, decisionsSystemInstructions(&openai.DecisionRequest{})))
}

func TestDecisionsOutputMessages(t *testing.T) {
	probability := 0.92
	attrs := messagesAttr(OutputMessages, decisionsOutputMessages(&openai.DecisionResponse{
		Answers: []openai.DecisionAnswer{{Type: "predicate", Name: "has_dent", Probability: &probability}},
	}))
	require.Len(t, attrs, 1)
	require.JSONEq(t,
		`[{"role":"assistant","parts":[{"type":"text","content":"{\"type\":\"predicate\",\"name\":\"has_dent\",\"probability\":0.92}"}]}]`,
		attrs[0].Value.AsString())

	require.Empty(t, messagesAttr(OutputMessages, decisionsOutputMessages(&openai.DecisionResponse{})))
}

func TestDecisionsRecorder_Content(t *testing.T) {
	req := &openai.DecisionRequest{
		Model:     "gpt-6-luna",
		Input:     []byte(`"I was charged twice."`),
		Questions: []openai.DecisionQuestion{{Type: "predicate", Name: "refund", Instructions: "Is this a refund request?"}},
	}
	probability := 0.97
	resp := &openai.DecisionResponse{
		Model:   "gpt-6-luna",
		Answers: []openai.DecisionAnswer{{Type: "predicate", Name: "refund", Probability: &probability}},
	}

	tests := []struct {
		name    string
		capture bool
	}{
		{name: "capture enabled", capture: true},
		{name: "capture disabled", capture: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewDecisionsRecorder(&Config{CaptureMessageContent: tc.capture})
			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				r.RecordRequest(span, req, nil)
				r.RecordResponse(span, resp)
				return false
			})

			for _, key := range []string{InputMessages, SystemInstructions, OutputMessages} {
				require.Equal(t, tc.capture, hasAttr(span.Attributes, key), "attribute %s", key)
			}
			require.Contains(t, span.Attributes, attribute.String(OperationName, "decisions"))
		})
	}
}
