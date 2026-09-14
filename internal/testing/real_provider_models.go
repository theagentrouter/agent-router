// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internaltesting

// Model IDs used by tests that exercise real provider backends.
//
// These are centralized here so that when a provider retires a model (which
// surfaces as a 4xx/deprecation error from the real API), the swap is a
// single-line change instead of hunting down duplicated string literals across
// the test suite.
//
// Note: AWS Bedrock example manifests (e.g. examples/basic/aws.yaml,
// examples/provider_fallback/base.yaml, tests/data-plane/envoy.yaml) route by
// the exact model name and must be kept in sync manually, as they cannot
// reference these Go constants.
const (
	// Chat completion models (one per real provider).

	// OpenAIModelName is the OpenAI chat completion model.
	OpenAIModelName = "gpt-5-nano"
	// AWSBedrockModelName is the AWS Bedrock chat completion model.
	AWSBedrockModelName = "us.amazon.nova-micro-v1:0"
	// AzureOpenAIModelName is the Azure OpenAI chat completion model.
	AzureOpenAIModelName = "o1"
	// GeminiModelName is the Gemini chat completion model.
	GeminiModelName = "gemini-3.1-flash-lite"
	// GroqModelName is the Groq chat completion model.
	GroqModelName = "llama-3.1-8b-instant"
	// GrokModelName is the Grok (xAI) chat completion model.
	GrokModelName = "grok-3"
	// SambaNovaModelName is the SambaNova chat completion model.
	SambaNovaModelName = "Meta-Llama-3.1-8B-Instruct"
	// DeepInfraModelName is the DeepInfra chat completion model.
	DeepInfraModelName = "meta-llama/Meta-Llama-3-8B-Instruct"

	// Embeddings models (one per real provider that supports embeddings).

	// OpenAIEmbeddingsModelName is the OpenAI embeddings model.
	OpenAIEmbeddingsModelName = "text-embedding-3-small"
	// AWSBedrockEmbeddingsModelName is the AWS Bedrock embeddings model.
	AWSBedrockEmbeddingsModelName = "amazon.titan-embed-text-v2:0"
	// GeminiEmbeddingsModelName is the Gemini embeddings model.
	GeminiEmbeddingsModelName = "gemini-embedding-001"
	// SambaNovaEmbeddingsModelName is the SambaNova embeddings model.
	SambaNovaEmbeddingsModelName = "E5-Mistral-7B-Instruct"
	// DeepInfraEmbeddingsModelName is the DeepInfra embeddings model.
	DeepInfraEmbeddingsModelName = "BAAI/bge-base-en-v1.5"

	// Anthropic Messages API models.

	// AnthropicModelName is the direct Anthropic API model.
	AnthropicModelName = "claude-sonnet-4-5"
	// AWSBedrockAnthropicGlobalModelName is the Claude model served by AWS
	// Bedrock via the global cross-region inference profile.
	AWSBedrockAnthropicGlobalModelName = "global.anthropic.claude-sonnet-4-5-20250929-v1:0"
	// AWSBedrockAnthropicUSModelName is the Claude model served by AWS Bedrock
	// via the US cross-region inference profile.
	AWSBedrockAnthropicUSModelName = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
)
