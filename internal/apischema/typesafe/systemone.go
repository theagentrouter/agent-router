// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package typesafe contains TypeSafe AI API schema definitions.
package typesafe

import "github.com/envoyproxy/ai-gateway/internal/json"

// SystemOneRequest represents the request body for POST /v1/systemone.
// Docs: https://docs.typesafe.ai/api.md
//
// State, Instructions and Criteria accept strings, objects or arrays, so they
// are kept as raw JSON and never reshaped by the gateway.
type SystemOneRequest struct {
	// Model identifier to use, e.g. "jev-latest".
	Model string `json:"model"`
	// State is the application state the questions are evaluated against.
	State json.RawMessage `json:"state,omitempty"`
	// Questions maps a caller-chosen question id to its definition.
	Questions map[string]SystemOneQuestion `json:"questions"`
}

// SystemOneQuestion is a single typed question.
type SystemOneQuestion struct {
	// Type is "noul", "choice" or "score".
	Type string `json:"type"`
	// Instructions describe the question to answer.
	Instructions json.RawMessage `json:"instructions,omitempty"`
	// Criteria is optional for noul, a map for choice and an array for score.
	Criteria json.RawMessage `json:"criteria,omitempty"`
}

// SystemOneResponse represents the response from POST /v1/systemone.
// Docs: https://docs.typesafe.ai/api.md
//
// The gateway only acts on Model and Usage. Answers are a discriminated union
// on "type" (noul, choice, score) whose shape varies per type, so they are kept
// raw and round-trip byte-for-byte into traces.
type SystemOneResponse struct {
	// Model is the resolved model that served the request, e.g. "jev-1.13.0".
	Model string `json:"model"`
	// Answers maps each question id to its answer.
	Answers map[string]json.RawMessage `json:"answers"`
	// Usage is the token usage for the request.
	Usage *SystemOneUsage `json:"usage,omitempty"`
}

// SystemOneUsage captures token accounting for the request. Output tokens are
// reported but not billed by TypeSafe.
type SystemOneUsage struct {
	InputTokens  *int `json:"input_tokens,omitempty"`
	OutputTokens *int `json:"output_tokens,omitempty"`
}

// SystemOneError is the error body returned by the TypeSafe API, e.g.
//
//	{"detail":{"error_type":"authentication_error","message":"Cannot authenticate ..."}}
//
// Native JSON errors are passed through untouched; the gateway only
// synthesizes this shape when the upstream returns a non-JSON error.
type SystemOneError struct {
	Detail SystemOneErrorDetail `json:"detail"`
}

// SystemOneErrorDetail carries the error type and message.
type SystemOneErrorDetail struct {
	// ErrorType observed values are "authentication_error" (401) and
	// "api_usage_error" (400).
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

// Error types observed from the TypeSafe API, plus the one the gateway uses
// when wrapping a non-JSON upstream failure.
const (
	ErrorTypeAuthentication = "authentication_error"
	ErrorTypeAPIUsage       = "api_usage_error"
	ErrorTypeAPI            = "api_error"
)
