// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package otelgenai implements the OpenTelemetry GenAI semantic conventions for
// span recording.
//
// See: https://github.com/open-telemetry/semantic-conventions-genai
//
// Attribute names are hardcoded rather than taken from a versioned semconv
// package, matching the convention used elsewhere in this repo (see
// internal/metrics/genai.go). Emitted attribute names are a user-visible
// contract: a dependency bump must not silently rename them and break
// dashboards. The spec vocabulary is verified against the versioned semconv
// package in tests instead, so an upstream change surfaces as a test failure
// describing it.
package otelgenai

// Span attribute names from the GenAI semantic conventions.
// See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/gen-ai/
const (
	OperationName  = "gen_ai.operation.name"
	ConversationID = "gen_ai.conversation.id"
	ProviderName   = "gen_ai.provider.name"

	RequestModel            = "gen_ai.request.model"
	RequestMaxTokens        = "gen_ai.request.max_tokens" //nolint:gosec // attribute name, not credential
	RequestTemperature      = "gen_ai.request.temperature"
	RequestTopP             = "gen_ai.request.top_p"
	RequestTopK             = "gen_ai.request.top_k"
	RequestFrequencyPenalty = "gen_ai.request.frequency_penalty"
	RequestPresencePenalty  = "gen_ai.request.presence_penalty"
	RequestStopSequences    = "gen_ai.request.stop_sequences"
	RequestSeed             = "gen_ai.request.seed"
	RequestChoiceCount      = "gen_ai.request.choice.count"
	RequestEncodingFormats  = "gen_ai.request.encoding_formats"

	ResponseID            = "gen_ai.response.id"
	ResponseModel         = "gen_ai.response.model"
	ResponseFinishReasons = "gen_ai.response.finish_reasons"

	UsageInputTokens              = "gen_ai.usage.input_tokens"                //nolint:gosec // attribute name, not credential
	UsageOutputTokens             = "gen_ai.usage.output_tokens"               //nolint:gosec // attribute name, not credential
	UsageCacheReadInputTokens     = "gen_ai.usage.cache_read.input_tokens"     //nolint:gosec // attribute name, not credential
	UsageCacheCreationInputTokens = "gen_ai.usage.cache_creation.input_tokens" //nolint:gosec // attribute name, not credential
	UsageReasoningOutputTokens    = "gen_ai.usage.reasoning.output_tokens"     //nolint:gosec // attribute name, not credential

	// The four message attributes below carry conversation content and are only
	// emitted when capture is explicitly enabled. See config.go.
	InputMessages      = "gen_ai.input.messages"
	OutputMessages     = "gen_ai.output.messages"
	SystemInstructions = "gen_ai.system_instructions"
	ToolDefinitions    = "gen_ai.tool.definitions"

	EmbeddingsDimensionCount = "gen_ai.embeddings.dimension.count"

	// ErrorType is owned by the error registry, not the GenAI one.
	// See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/error/
	ErrorType = "error.type"
)

// Operation is the value of the OperationName attribute.
type Operation string

// Operations used by this gateway.
//
// The GenAI registry defines a closed set of well-known values, but permits
// custom ones. Endpoints without a registry value use a custom operation named
// after the endpoint.
//
// These deliberately do NOT reuse metrics.GenAIOperation: those values are the
// established contract for existing dashboards and alerts, and two of them
// (completion, messages) differ from the registry value required here.
// Changing the metrics values would break users; changing these would make the
// spans non-conformant. The divergence is intentional. See internal/metrics/genai.go.
const (
	// OperationChat is the registry value for chat completions.
	OperationChat Operation = "chat"
	// OperationTextCompletion is the registry value for legacy completions.
	// Note: metrics report this operation as "completion".
	OperationTextCompletion Operation = "text_completion"
	// OperationEmbeddings is the registry value for embeddings.
	OperationEmbeddings Operation = "embeddings"

	// The following are custom values: the registry has no equivalent.
	OperationImageGeneration Operation = "image_generation"
	OperationSpeech          Operation = "speech"
	OperationTranscription   Operation = "transcription"
	OperationTranslation     Operation = "translation"
	OperationRerank          Operation = "rerank"
	OperationTokenize        Operation = "tokenize"
)

// Provider is the value of the ProviderName attribute.
type Provider string

// Providers recognized by the GenAI registry.
// See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/gen-ai/
const (
	ProviderOpenAI       Provider = "openai"
	ProviderAzureOpenAI  Provider = "azure.openai"
	ProviderAWSBedrock   Provider = "aws.bedrock"
	ProviderAWSAnthropic Provider = "aws.anthropic"
	ProviderGCPVertexAI  Provider = "gcp.vertex_ai"
	ProviderGCPAnthropic Provider = "gcp.anthropic"
	ProviderAnthropic    Provider = "anthropic"
	ProviderCohere       Provider = "cohere"
)
