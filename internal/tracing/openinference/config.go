// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference

import (
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingenv"
)

// Environment variable names for trace configuration following Python OpenInference conventions.
// These environment variables control the privacy and observability settings for OpenInference tracing.
// See: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
const (
	// EnvHideLLMInvocationParameters is the environment variable for TraceConfig.HideLLMInvocationParameters.
	EnvHideLLMInvocationParameters = "OPENINFERENCE_HIDE_LLM_INVOCATION_PARAMETERS"
	// EnvHideInputs is the environment variable for TraceConfig.HideInputs.
	EnvHideInputs = "OPENINFERENCE_HIDE_INPUTS"
	// EnvHideOutputs is the environment variable for TraceConfig.HideOutputs.
	EnvHideOutputs = "OPENINFERENCE_HIDE_OUTPUTS"
	// EnvHideInputMessages is the environment variable for TraceConfig.HideInputMessages.
	EnvHideInputMessages = "OPENINFERENCE_HIDE_INPUT_MESSAGES"
	// EnvHideOutputMessages is the environment variable for TraceConfig.HideOutputMessages.
	EnvHideOutputMessages = "OPENINFERENCE_HIDE_OUTPUT_MESSAGES"
	// EnvHideInputImages is the environment variable for TraceConfig.HideInputImages.
	EnvHideInputImages = "OPENINFERENCE_HIDE_INPUT_IMAGES"
	// EnvHideInputText is the environment variable for TraceConfig.HideInputText.
	EnvHideInputText = "OPENINFERENCE_HIDE_INPUT_TEXT"
	// EnvHideOutputText is the environment variable for TraceConfig.HideOutputText.
	EnvHideOutputText = "OPENINFERENCE_HIDE_OUTPUT_TEXT"
	// EnvHideEmbeddingsText is the environment variable for TraceConfig.HideEmbeddingsText.
	EnvHideEmbeddingsText = "OPENINFERENCE_HIDE_EMBEDDINGS_TEXT"
	// EnvHideEmbeddingsVectors is the environment variable for TraceConfig.HideEmbeddingsVectors.
	EnvHideEmbeddingsVectors = "OPENINFERENCE_HIDE_EMBEDDINGS_VECTORS"
	// EnvBase64ImageMaxLength is the environment variable for TraceConfig.Base64ImageMaxLength.
	EnvBase64ImageMaxLength = "OPENINFERENCE_BASE64_IMAGE_MAX_LENGTH"
	// EnvHidePrompts is the environment variable for TraceConfig.HidePrompts.
	// Hides LLM prompts (completions API).
	// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
	EnvHidePrompts = "OPENINFERENCE_HIDE_PROMPTS"
	// EnvHideChoices is the environment variable for TraceConfig.HideChoices.
	// Hides LLM choices (completions API outputs).
	// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
	EnvHideChoices = "OPENINFERENCE_HIDE_CHOICES"
)

// Default values for trace configuration.
const (
	defaultHideLLMInvocationParameters = false
	defaultHidePrompts                 = false
	defaultHideChoices                 = false
	defaultHideInputs                  = false
	defaultHideOutputs                 = false
	defaultHideInputMessages           = false
	defaultHideOutputMessages          = false
	defaultHideInputImages             = false
	defaultHideInputText               = false
	defaultHideOutputText              = false
	defaultHideEmbeddingsVectors       = false
	defaultHideEmbeddingsText          = false
	defaultBase64ImageMaxLength        = 32000
)

// RedactedValue is the value used when content is hidden for privacy.
const RedactedValue = "__REDACTED__"

// TraceConfig helps you modify the observability level of your tracing.
// For instance, you may want to keep sensitive information from being logged.
// for security reasons, or you may want to limit the size of the base64.
// encoded images to reduce payloads.
//
// Use NewTraceConfig to create this from defaults or NewTraceConfigFromEnv
// to prioritize environment variables.
//
// This implementation follows the OpenInference configuration specification:
// https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
type TraceConfig struct {
	// HideLLMInvocationParameters controls whether LLM invocation parameters are hidden.
	// This is independent of HideInputs.
	HideLLMInvocationParameters bool
	// HideInputs controls whether input values and messages are hidden.
	// When true, hides both input.value and all input messages.
	HideInputs bool
	// HideOutputs controls whether output values and messages are hidden.
	// When true, hides both output.value and all output messages.
	HideOutputs bool
	// HideInputMessages controls whether all input messages are hidden.
	// Input messages are hidden if either HideInputs OR HideInputMessages is true.
	HideInputMessages bool
	// HideOutputMessages controls whether all output messages are hidden.
	// Output messages are hidden if either HideOutputs OR HideOutputMessages is true.
	HideOutputMessages bool
	// HideInputImages controls whether images from input messages are hidden.
	// Only applies when input messages are not already hidden.
	HideInputImages bool
	// HideInputText controls whether text from input messages is hidden.
	// Only applies when input messages are not already hidden.
	HideInputText bool
	// HideOutputText controls whether text from output messages is hidden.
	// Only applies when output messages are not already hidden.
	HideOutputText bool
	// HideEmbeddingsText controls whether embedding text is hidden.
	// Maps to OPENINFERENCE_HIDE_EMBEDDINGS_TEXT environment variable.
	// When true, embedding.embeddings.N.embedding.text attributes contain "__REDACTED__".
	HideEmbeddingsText bool
	// HideEmbeddingsVectors controls whether embedding vectors are hidden.
	// Maps to OPENINFERENCE_HIDE_EMBEDDINGS_VECTORS environment variable.
	// When true, embedding.embeddings.N.embedding.vector attributes contain "__REDACTED__".
	HideEmbeddingsVectors bool
	// Base64ImageMaxLength limits the characters of a base64 encoding of an image.
	Base64ImageMaxLength int
	// HidePrompts controls whether LLM prompts are hidden.
	// Maps to OPENINFERENCE_HIDE_PROMPTS environment variable.
	// Only applies to completions API (not chat completions).
	// When true, llm.prompts.N.prompt.text attributes contain "__REDACTED__".
	HidePrompts bool
	// HideChoices controls whether LLM choices are hidden.
	// Maps to OPENINFERENCE_HIDE_CHOICES environment variable.
	// Only applies to completions API outputs (not chat completions).
	// When true, llm.choices.N.completion.text attributes contain "__REDACTED__".
	HideChoices bool
}

// NewTraceConfig creates a new TraceConfig with default values.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
func NewTraceConfig() *TraceConfig {
	return &TraceConfig{
		HideLLMInvocationParameters: defaultHideLLMInvocationParameters,
		HideInputs:                  defaultHideInputs,
		HideOutputs:                 defaultHideOutputs,
		HideInputMessages:           defaultHideInputMessages,
		HideOutputMessages:          defaultHideOutputMessages,
		HideInputImages:             defaultHideInputImages,
		HideInputText:               defaultHideInputText,
		HideOutputText:              defaultHideOutputText,
		HideEmbeddingsVectors:       defaultHideEmbeddingsVectors,
		HideEmbeddingsText:          defaultHideEmbeddingsText,
		Base64ImageMaxLength:        defaultBase64ImageMaxLength,
		HidePrompts:                 defaultHidePrompts,
		HideChoices:                 defaultHideChoices,
	}
}

// CapturesMessages reports whether either input or output messages will be
// emitted as span attributes given the current hide flags.
func (c *TraceConfig) CapturesMessages() bool {
	return (!c.HideInputs && !c.HideInputMessages) || (!c.HideOutputs && !c.HideOutputMessages)
}

// NewTraceConfigFromEnv creates a new TraceConfig with values from environment
// variables or their corresponding defaults.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
func NewTraceConfigFromEnv() *TraceConfig {
	return &TraceConfig{
		HideLLMInvocationParameters: tracingenv.GetBoolEnv(EnvHideLLMInvocationParameters, defaultHideLLMInvocationParameters),
		HideInputs:                  tracingenv.GetBoolEnv(EnvHideInputs, defaultHideInputs),
		HideOutputs:                 tracingenv.GetBoolEnv(EnvHideOutputs, defaultHideOutputs),
		HideInputMessages:           tracingenv.GetBoolEnv(EnvHideInputMessages, defaultHideInputMessages),
		HideOutputMessages:          tracingenv.GetBoolEnv(EnvHideOutputMessages, defaultHideOutputMessages),
		HideInputImages:             tracingenv.GetBoolEnv(EnvHideInputImages, defaultHideInputImages),
		HideInputText:               tracingenv.GetBoolEnv(EnvHideInputText, defaultHideInputText),
		HideOutputText:              tracingenv.GetBoolEnv(EnvHideOutputText, defaultHideOutputText),
		HideEmbeddingsVectors:       tracingenv.GetBoolEnv(EnvHideEmbeddingsVectors, defaultHideEmbeddingsVectors),
		HideEmbeddingsText:          tracingenv.GetBoolEnv(EnvHideEmbeddingsText, defaultHideEmbeddingsText),
		Base64ImageMaxLength:        tracingenv.GetIntEnv(EnvBase64ImageMaxLength, defaultBase64ImageMaxLength),
		HidePrompts:                 tracingenv.GetBoolEnv(EnvHidePrompts, defaultHidePrompts),
		HideChoices:                 tracingenv.GetBoolEnv(EnvHideChoices, defaultHideChoices),
	}
}
