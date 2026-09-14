// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package openinference provides OpenInference semantic conventions for
// OpenTelemetry tracing.
package openinference

import "fmt"

// OpenInference Span Kind constants.
//
// These constants define the type of operation represented by a span.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md
const (
	// SpanKind identifies the type of operation (required for all OpenInference spans).
	SpanKind = "openinference.span.kind"

	// SpanKindLLM indicates a Large Language Model operation.
	SpanKindLLM = "LLM"

	// SpanKindEmbedding indicates an Embedding operation.
	SpanKindEmbedding = "EMBEDDING"

	// SpanKindTokenCounter indicates a token counting operation (e.g., tokenize, count_tokens).
	SpanKindTokenCounter = "TOKEN_COUNTER"
)

// LLM Operation constants.
//
// These constants define attributes for Large Language Model operations.
// following OpenInference semantic conventions.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#llm-spans
const (
	// LLMSystem identifies the AI system/product (e.g., "openai").
	LLMSystem = "llm.system"

	// LLMModelName specifies the model name (e.g., "gpt-4", "gpt-3.5-turbo").
	LLMModelName = "llm.model_name"

	// LLMInvocationParameters contains the invocation parameters as JSON string.
	LLMInvocationParameters = "llm.invocation_parameters"
)

// LLMSystem Values.
const (
	// LLMSystemOpenAI for OpenAI systems.
	LLMSystemOpenAI = "openai"
	// LLMSystemCohere for Cohere systems.
	LLMSystemCohere = "cohere"
	// LLMSystemAnthropic for Anthropic systems.
	LLMSystemAnthropic = "anthropic"
)

// Input/Output constants.
//
// These constants define attributes for capturing input and output data.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#inputoutput
const (
	// InputValue contains the input data as a string (typically JSON).
	InputValue = "input.value"

	// InputMimeType specifies the MIME type of the input data.
	InputMimeType = "input.mime_type"

	// OutputValue contains the output data as a string (typically JSON).
	OutputValue = "output.value"

	// OutputMimeType specifies the MIME type of the output data.
	OutputMimeType = "output.mime_type"

	// MimeTypeJSON for JSON content.
	MimeTypeJSON = "application/json"

	// OutputAudioDuration is the attribute key for audio duration in transcription/translation responses.
	OutputAudioDuration = "output.audio_duration"

	// OutputLanguage is the attribute key for detected language in transcription/translation responses.
	OutputLanguage = "output.language"
)

// Completions API constants (Legacy Text Completion).
//
// These constants define attributes for the legacy completions API, distinct from chat completions.
// They use indexed attribute format with discriminated union structure for future expansion.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#completions-api-legacy-text-completion
const (
	// LLMPrompts prefix for prompt attributes in completions API.
	// Usage: llm.prompts.{index}.prompt.text
	// Prompts provided to a completions API, indexed starting from 0.
	LLMPrompts = "llm.prompts"

	// LLMChoices prefix for completion choice attributes in completions API.
	// Usage: llm.choices.{index}.completion.text
	// Text choices returned from a completions API, indexed starting from 0.
	LLMChoices = "llm.choices"
)

// LLM Message constants.
//
// These constants define attributes for LLM input and output messages using.
// flattened attribute format. Messages are indexed starting from 0.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#llm-spans
const (
	// LLMInputMessages prefix for input message attributes.
	// Usage: llm.input_messages.{index}.message.role, llm.input_messages.{index}.message.content.
	LLMInputMessages = "llm.input_messages"

	// LLMOutputMessages prefix for output message attributes.
	// Usage: llm.output_messages.{index}.message.role, llm.output_messages.{index}.message.content.
	LLMOutputMessages = "llm.output_messages"

	// MessageRole suffix for message role (e.g., "user", "assistant", "system").
	MessageRole = "message.role"

	// MessageContent suffix for message content.
	MessageContent = "message.content"
)

// Token Count constants.
//
// These constants define attributes for token usage tracking.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#llm-spans
const (
	// LLMTokenCountPrompt contains the number of tokens in the prompt.
	LLMTokenCountPrompt = "llm.token_count.prompt" // #nosec G101

	// LLMTokenCountCompletion contains the number of tokens in the completion.
	LLMTokenCountCompletion = "llm.token_count.completion" // #nosec G101

	// LLMTokenCountTotal contains the total number of tokens.
	LLMTokenCountTotal = "llm.token_count.total" // #nosec G101
)

// Tool Call constants.
//
// These constants define attributes for function/tool calling in LLM operations.
// Used when LLM responses include tool calls.
// Reference: Python OpenAI instrumentation (not in core spec).
const (
	// LLMTools contains the list of available tools as JSON.
	// Format: llm.tools.{index}.tool.json_schema.
	LLMTools = "llm.tools"

	// MessageToolCalls prefix for tool calls in messages.
	// Format: message.tool_calls.{index}.tool_call.{attribute}.
	MessageToolCalls = "message.tool_calls"

	// ToolCallID suffix for tool call ID.
	ToolCallID = "tool_call.id"

	// ToolCallFunctionName suffix for function name in a tool call.
	ToolCallFunctionName = "tool_call.function.name"

	// ToolCallFunctionArguments suffix for function arguments as JSON string.
	ToolCallFunctionArguments = "tool_call.function.arguments"
)

// Extended Token Count constants.
//
// These constants define additional token count attributes for detailed usage tracking.
// They provide granular information about token consumption for cost analysis and
// performance monitoring.
// Reference: OpenInference specification and Python OpenAI instrumentation.
const (
	// LLMTokenCountPromptCacheHit represents the number of prompt tokens successfully.
	// retrieved from cache (cache hits). This enables tracking of cache efficiency
	// and cost savings from cached prompts.
	LLMTokenCountPromptCacheHit = "llm.token_count.prompt_details.cache_read" // #nosec G101

	// LLMTokenCountPromptCacheWrite represents the number of prompt tokens
	// created to cache (cache write). This enables tracking of cache efficiency
	// and cost savings from cached prompts.
	LLMTokenCountPromptCacheWrite = "llm.token_count.prompt_details.cache_creation" // #nosec G101

	// LLMTokenCountPromptAudio represents the number of audio tokens in the prompt.
	// Used for multimodal models that support audio input.
	LLMTokenCountPromptAudio = "llm.token_count.prompt_details.audio" // #nosec G101

	// LLMTokenCountCompletionReasoning represents the number of tokens used for
	// reasoning or chain-of-thought processes in the completion. This helps track
	// the computational cost of complex reasoning tasks.
	LLMTokenCountCompletionReasoning = "llm.token_count.completion_details.reasoning" // #nosec G101

	// LLMTokenCountCompletionAudio represents the number of audio tokens in the.
	// completion. Used for models that generate audio output.
	LLMTokenCountCompletionAudio = "llm.token_count.completion_details.audio" // #nosec G101
)

// InputMessageAttribute creates an attribute key for input messages.
func InputMessageAttribute(index int, suffix string) string {
	return fmt.Sprintf("%s.%d.%s", LLMInputMessages, index, suffix)
}

// InputMessageContentAttribute creates an attribute key for input message content.
func InputMessageContentAttribute(messageIndex, contentIndex int, suffix string) string {
	return fmt.Sprintf("%s.%d.message.contents.%d.message_content.%s", LLMInputMessages, messageIndex, contentIndex, suffix)
}

// InputMessageToolCallAttribute creates an attribute key for a input tool call.
func InputMessageToolCallAttribute(messageIndex, toolCallIndex int, suffix string) string {
	return fmt.Sprintf("%s.%d.%s.%d.%s", LLMInputMessages, messageIndex, MessageToolCalls, toolCallIndex, suffix)
}

// OutputMessageAttribute creates an attribute key for output messages.
func OutputMessageAttribute(index int, suffix string) string {
	return fmt.Sprintf("%s.%d.%s", LLMOutputMessages, index, suffix)
}

// OutputMessageToolCallAttribute creates an attribute key for a tool call.
func OutputMessageToolCallAttribute(messageIndex, toolCallIndex int, suffix string) string {
	return fmt.Sprintf("%s.%d.%s.%d.%s", LLMOutputMessages, messageIndex, MessageToolCalls, toolCallIndex, suffix)
}

// OutputMessageContentAttribute creates an attribute key for input message content.
func OutputMessageContentAttribute(messageIndex, contentIndex int, suffix string) string {
	return fmt.Sprintf("%s.%d.message.contents.%d.message_content.%s", LLMOutputMessages, messageIndex, contentIndex, suffix)
}

// InputToolsAttribute creates an attribute key for input tools.
func InputToolsAttribute(index int) string {
	return fmt.Sprintf("%s.%d.tool.json_schema", LLMTools, index)
}

// Embedding Operation constants.
//
// These constants define attributes for Embedding operations.
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
const (
	// EmbeddingModelName specifies the name of the embedding model.
	// Example: "text-embedding-3-small"
	EmbeddingModelName = "embedding.model_name"

	// EmbeddingInvocationParameters contains the invocation parameters as JSON string.
	// This includes parameters sent to the model excluding input.
	// Example: {"model": "text-embedding-3-small", "encoding_format": "float"}
	EmbeddingInvocationParameters = "embedding.invocation_parameters"

	// EmbeddingEmbeddings is the prefix for embedding data attributes in batch operations.
	// Forms the base for nested attributes like embedding.embeddings.{index}.embedding.text
	// and embedding.embeddings.{index}.embedding.vector.
	EmbeddingEmbeddings = "embedding.embeddings"
)

// EmbeddingTextAttribute creates an attribute key for embedding input text.
// Format: embedding.embeddings.{index}.embedding.text
//
// Text attributes are populated ONLY when the input is already text (strings).
// These attributes are recorded during the request phase to ensure availability even on errors.
//
// Token IDs (pre-tokenized integer arrays) are NOT decoded to text because:
//   - Cross-provider incompatibility: Same token IDs represent different text across tokenizers
//     (OpenAI uses cl100k_base, Ollama uses BERT/WordPiece/etc.)
//   - Runtime impossibility: OpenAI-compatible APIs may serve any model with unknown tokenizers
//   - Heavy dependencies: Supporting all tokenizers would require libraries beyond tiktoken
func EmbeddingTextAttribute(index int) string {
	return fmt.Sprintf("%s.%d.embedding.text", EmbeddingEmbeddings, index)
}

// EmbeddingVectorAttribute creates an attribute key for embedding output vector.
// Format: embedding.embeddings.{index}.embedding.vector
//
// Vector attributes MUST contain float arrays, regardless of the API response format:
//   - Float response format: Store vectors directly as float arrays
//   - Base64 response format: MUST decode base64-encoded strings to float arrays before recording
//     (Base64 encoding is ~25% more compact in transmission but must be decoded for consistency)
//     Example: "AACAPwAAAEA=" → [1.5, 2.0]
func EmbeddingVectorAttribute(index int) string {
	return fmt.Sprintf("%s.%d.embedding.vector", EmbeddingEmbeddings, index)
}

// PromptTextAttribute creates an attribute key for prompt text in completions API.
// Format: llm.prompts.{index}.prompt.text
//
// Example: llm.prompts.0.prompt.text, llm.prompts.1.prompt.text
//
// The nested structure (.prompt.text) uses a discriminated union pattern that mirrors
// llm.input_messages and llm.output_messages, allowing for future expansion.
//
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#completions-api-legacy-text-completion
func PromptTextAttribute(index int) string {
	return fmt.Sprintf("%s.%d.prompt.text", LLMPrompts, index)
}

// ChoiceTextAttribute creates an attribute key for completion choice text.
// Format: llm.choices.{index}.completion.text
//
// Example: llm.choices.0.completion.text, llm.choices.1.completion.text
//
// The nested structure (.completion.text) uses a discriminated union pattern that mirrors
// the prompt structure, allowing for future expansion with additional choice metadata.
//
// Reference: https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md#completions-api-legacy-text-completion
func ChoiceTextAttribute(index int) string {
	return fmt.Sprintf("%s.%d.completion.text", LLMChoices, index)
}
