// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package httperr maps upstream HTTP status codes to the error identifiers each
// tracing semantic convention reports.
package httperr

import "strconv"

// FallbackOpenInference is reported for statuses with no specific mapping.
const FallbackOpenInference = "Error"

// FallbackGenAI is the GenAI registry's well-known fallback value.
// See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/error/
const FallbackGenAI = "_OTHER"

// statusErrorTypes maps HTTP status codes to per-convention error identifiers.
// Keeping both columns in one table means the mappings cannot drift apart and a
// reviewer can audit the whole thing at a glance.
var statusErrorTypes = []struct {
	statuses []int
	// openInference mirrors the Python OpenAI SDK exception class names, which
	// the OpenInference goldens are recorded against.
	openInference string
	// genAI is the low-cardinality identifier used for the error.type attribute.
	genAI string
}{
	{statuses: []int{400}, openInference: "BadRequestError", genAI: "400"},
	{statuses: []int{401}, openInference: "AuthenticationError", genAI: "401"},
	{statuses: []int{403}, openInference: "PermissionDeniedError", genAI: "403"},
	{statuses: []int{404}, openInference: "NotFoundError", genAI: "404"},
	{statuses: []int{429}, openInference: "RateLimitError", genAI: "429"},
	{statuses: []int{500, 502, 503}, openInference: "InternalServerError", genAI: "500"},
}

// OpenInferenceErrorType returns the OpenInference error type for a status code.
func OpenInferenceErrorType(statusCode int) string {
	for _, e := range statusErrorTypes {
		for _, s := range e.statuses {
			if s == statusCode {
				return e.openInference
			}
		}
	}
	return FallbackOpenInference
}

// GenAIErrorType returns the value of the error.type attribute for a status code.
//
// The GenAI conventions ask for the provider's error code or another
// low-cardinality identifier, so any status outside the table falls back to the
// status code itself rather than inventing a name. Non-HTTP statuses fall back
// to FallbackGenAI to keep cardinality bounded.
func GenAIErrorType(statusCode int) string {
	for _, e := range statusErrorTypes {
		for _, s := range e.statuses {
			if s == statusCode {
				return e.genAI
			}
		}
	}
	if statusCode >= 400 && statusCode <= 599 {
		return strconv.Itoa(statusCode)
	}
	return FallbackGenAI
}
