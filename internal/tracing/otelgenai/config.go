// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingenv"
)

// EnvCaptureMessageContent is the environment variable for Config.CaptureMessageContent.
const EnvCaptureMessageContent = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"

// The GenAI semantic conventions classify message content as opt-in, because it
// routinely contains sensitive data. This inverts the OpenInference default,
// which captures content unless told otherwise.
const defaultCaptureMessageContent = false

// Config controls what the OTel GenAI recorders emit.
//
// Unlike openinference.TraceConfig, which models a lattice of independent hide
// flags, the GenAI conventions define a single content switch. Reusing
// TraceConfig here would mean OPENINFERENCE_HIDE_* variables silently governing
// gen_ai.* output, which would surprise operators and risks a redaction bypass
// when migrating between conventions.
type Config struct {
	// CaptureMessageContent enables InputMessages, OutputMessages,
	// SystemInstructions and ToolDefinitions.
	CaptureMessageContent bool
}

// NewConfig creates a Config with default values.
func NewConfig() *Config {
	return &Config{CaptureMessageContent: defaultCaptureMessageContent}
}

// NewConfigFromEnv creates a Config from environment variables, falling back to
// defaults for unset or unparseable values.
func NewConfigFromEnv() *Config {
	return &Config{
		CaptureMessageContent: tracingenv.GetBoolEnv(EnvCaptureMessageContent, defaultCaptureMessageContent),
	}
}

// CapturesMessages reports whether message content will be emitted as span
// attributes. It mirrors openinference.TraceConfig.CapturesMessages so both
// conventions answer the span-limit question the same way.
func (c *Config) CapturesMessages() bool {
	return c.CaptureMessageContent
}
