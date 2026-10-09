// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/tracing/httperr"
)

// RecordResponseError records an upstream error on the span per the GenAI
// semantic conventions, which use the error.type attribute rather than the
// exception event that OpenInference relies on.
//
// The response body is only included in the status description when content
// capture is enabled: provider error bodies routinely echo the request, so
// copying them unconditionally would leak prompt content past the opt-in.
func RecordResponseError(span trace.Span, config *Config, statusCode int, body string) {
	span.SetAttributes(attribute.String(ErrorType, httperr.GenAIErrorType(statusCode)))

	description := fmt.Sprintf("Error code: %d", statusCode)
	if config.CaptureMessageContent && len(body) > 0 {
		description = fmt.Sprintf("Error code: %d - %s", statusCode, body)
	}
	span.SetStatus(codes.Error, description)
}
