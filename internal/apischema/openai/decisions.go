// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import "github.com/envoyproxy/ai-gateway/internal/json"

// DecisionRequest represents the request body for POST /v1/decisions.
// Input is kept as raw JSON because the API accepts either a string or an array
// of user messages containing text and inline base64 images.
// Docs: https://developers.openai.com/api/reference/resources/decisions/methods/create
type DecisionRequest struct {
	Model     string             `json:"model"`
	Input     json.RawMessage    `json:"input"`
	Questions []DecisionQuestion `json:"questions"`
	// SafetyIdentifier is an opaque caller-provided end-user identifier.
	SafetyIdentifier string `json:"safety_identifier,omitempty"`
}

// DecisionQuestion is a predicate, choice, or score question.
// Docs: https://developers.openai.com/api/docs/guides/decisions
// SDK: https://github.com/openai/openai-go/blob/v3.73.0/decision.go
type DecisionQuestion struct {
	Type         string                 `json:"type"`
	Name         string                 `json:"name,omitempty"`
	Instructions string                 `json:"instructions"`
	Choices      []DecisionChoiceOption `json:"choices,omitempty"`
	Levels       []DecisionScoreLevel   `json:"levels,omitempty"`
}

// DecisionChoiceOption is one allowed result for a choice question. Value is raw because choice values are typed as either strings or booleans.
// SDK: https://github.com/openai/openai-go/blob/v3.73.0/decision.go
type DecisionChoiceOption struct {
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description,omitempty"`
}

// DecisionScoreLevel is one ordered level for a score question.
// Docs: https://developers.openai.com/api/docs/guides/decisions
type DecisionScoreLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// DecisionResponse represents a response from POST /v1/decisions.
// Docs: https://developers.openai.com/api/reference/resources/decisions/methods/create
type DecisionResponse struct {
	Answers []DecisionAnswer `json:"answers"`
	Model   string           `json:"model,omitempty"`
	Usage   *ResponseUsage   `json:"usage,omitempty"`
}

// DecisionAnswer is a predicate, choice, score, or refusal answer. Pointer
// numeric fields preserve valid zero values while omitting fields belonging to
// other variants.
// Docs: https://developers.openai.com/api/reference/resources/decisions/methods/create
// Docs: https://developers.openai.com/api/docs/guides/decisions
type DecisionAnswer struct {
	Type          string                `json:"type"`
	Name          string                `json:"name,omitempty"`
	Probability   *float64              `json:"probability,omitempty"`
	Choice        json.RawMessage       `json:"choice,omitempty"`
	Probabilities []DecisionProbability `json:"probabilities,omitempty"`
	Confidence    *float64              `json:"confidence,omitempty"`
	Score         *float64              `json:"score,omitempty"`
}

// DecisionProbability is one entry in a choice or score probability
// distribution. Value is raw because choices use strings or booleans, while
// score levels use numeric indices.
// Docs: https://developers.openai.com/api/docs/guides/decisions
// SDK: https://github.com/openai/openai-go/blob/v3.73.0/decision.go
type DecisionProbability struct {
	Value       json.RawMessage `json:"value"`
	Label       string          `json:"label,omitempty"`
	Probability float64         `json:"probability"`
}
