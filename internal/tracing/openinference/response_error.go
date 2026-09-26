// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
//
// Package openinference provides shared OpenInference helpers.
package openinference

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/tracing/httperr"
)

// RecordResponseError processes error responses and updates span accordingly.
//
// The response body is only included in the error description when content
// capture is enabled for both inputs and outputs: provider error bodies
// routinely echo the request, so recording them unconditionally would leak
// content past the TraceConfig opt-out. The GenAI tracer applies the same
// rule in internal/tracing/otelgenai.
func RecordResponseError(span trace.Span, config *TraceConfig, statusCode int, body string) {
	errorType := httperr.OpenInferenceErrorType(statusCode)

	// Format error message following Go conventions.
	errorMsg := fmt.Sprintf("Error code: %d", statusCode)
	if len(body) > 0 && config != nil && !config.HideInputs && !config.HideOutputs {
		errorMsg = fmt.Sprintf("Error code: %d - %s", statusCode, body)
	}

	// Add exception event following OpenTelemetry semantic conventions.
	// The event name MUST be "exception" per the spec.
	span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", errorType),
		attribute.String("exception.message", errorMsg),
	))

	// Set span status to error with the message.
	span.SetStatus(codes.Error, errorMsg)
}
