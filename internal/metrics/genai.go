// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import "go.opentelemetry.io/otel/metric"

const (
	// Metric names, attributes and values according to the Semantic Conventions for Generative AI Metrics.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/

	genaiMetricClientTokenUsage         = "gen_ai.client.token.usage" //nolint:gosec // metric name, not credential
	genaiMetricServerRequestDuration    = "gen_ai.server.request.duration"
	genaiMetricServerTimeToFirstToken   = "gen_ai.server.time_to_first_token"   //nolint:gosec // metric name, not credential
	genaiMetricServerTimePerOutputToken = "gen_ai.server.time_per_output_token" //nolint:gosec // metric name, not credential

	genaiAttributeOperationName = "gen_ai.operation.name"
	genaiAttributeProviderName  = "gen_ai.provider.name"
	genaiAttributeOriginalModel = "gen_ai.original.model"
	genaiAttributeRequestModel  = "gen_ai.request.model"
	genaiAttributeResponseModel = "gen_ai.response.model"
	genaiAttributeTokenType     = "gen_ai.token.type" //nolint:gosec // metric name, not credential
	genaiAttributeErrorType     = "error.type"

	GenAIOperationChat                 GenAIOperation = "chat"
	GenAIOperationCompletion           GenAIOperation = "completion"
	GenAIOperationEmbedding            GenAIOperation = "embeddings"
	GenAIOperationMessages             GenAIOperation = "messages"
	GenAIOperationImageGeneration      GenAIOperation = "image_generation"
	GenAIOperationResponses            GenAIOperation = "responses"
	GenAIOperationSpeech               GenAIOperation = "speech"
	GenAIOperationTranscription        GenAIOperation = "transcription"
	GenAIOperationTranslation          GenAIOperation = "translation"
	GenAIOperationRerank               GenAIOperation = "rerank"
	GenAIOperationTokenize             GenAIOperation = "tokenize"
	GenAIOperationResponsesInputTokens GenAIOperation = "responses_input_tokens"
	GenAIOperationCountTokens          GenAIOperation = "count_tokens"

	// Provider names according to the Semantic Conventions for Generative AI Metrics.
	// See: https://opentelemetry.io/docs/specs/semconv/attributes-registry/gen-ai/
	genaiProviderOpenAI       = "openai"
	genaiProviderAzureOpenAI  = "azure.openai"
	genaiProviderAWSBedrock   = "aws.bedrock"
	genaiProviderAWSAnthropic = "aws.anthropic"
	genaiProviderGCPVertexAI  = "gcp.vertex_ai"
	genaiProviderGCPAnthropic = "gcp.anthropic"
	genaiProviderAnthropic    = "anthropic"
	genaiProviderCohere       = "cohere"

	genaiTokenTypeInput  = "input"
	genaiTokenTypeOutput = "output"
	// "cached_input" is not yet part of the spec but has been proposed:
	// https://github.com/open-telemetry/semantic-conventions/issues/1959
	//
	// However, the spec says "a custom value MAY be used.", so we can use it now.
	genaiTokenTypeCachedInput        = "cached_input"
	genaiTokenTypeCacheCreationInput = "cache_creation_input"
	genaiTokenTypeReasoning          = "reasoning"
	genaiErrorTypeFallback           = "_OTHER"
)

// GenAIOperation represents the type of generative AI operation i.e. the endpoint being called.
type GenAIOperation string

// genAI holds metrics according to the Semantic Conventions for Generative AI Metrics.
// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/.
type genAI struct {
	// Number of tokens processed.
	// Note: We record gen_ai.client.token.usage because the AI Gateway acts as a client to upstream AI services.
	// Since we pass through token counts without manipulation, this metric accurately reflects token usage
	// from our perspective as a client of the upstream AI provider.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiclienttokenusage
	tokenUsage metric.Float64Histogram
	// requestLatency is the total latency of the request.
	// Measured from the start of the received request headers in extproc to the end of the processed response body in extproc.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiserverrequestduration
	requestLatency metric.Float64Histogram
	// firstTokenLatency is the latency to receive the first token.
	// Measured from the start of the received request headers in extproc to the receiving of the first token in the response body in extproc.
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_to_first_token
	firstTokenLatency metric.Float64Histogram
	// outputTokenLatency is the time per output token generated after the first token.
	// Calculated by: (request_duration - time_to_first_token) / (output_tokens - 1)
	// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_per_output_token
	outputTokenLatency metric.Float64Histogram
}

// newGenAI creates a new genAI metrics instance.
func newGenAI(meter metric.Meter) *genAI {
	return &genAI{
		tokenUsage: mustRegisterHistogram(meter,
			genaiMetricClientTokenUsage,
			metric.WithDescription("Number of tokens processed."),
			metric.WithUnit("token"),
			metric.WithExplicitBucketBoundaries(1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864),
		),
		requestLatency: mustRegisterHistogram(meter,
			genaiMetricServerRequestDuration,
			metric.WithDescription("Generative AI server request duration such as time-to-last byte or last output token."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92),
		),
		firstTokenLatency: mustRegisterHistogram(meter,
			genaiMetricServerTimeToFirstToken,
			metric.WithDescription("Time to receive first token in streaming responses."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.02, 0.04, 0.06, 0.08, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 15.0, 20.0, 30.0, 45.0, 60.0),
		),
		outputTokenLatency: mustRegisterHistogram(meter,
			genaiMetricServerTimePerOutputToken,
			metric.WithDescription("Time per output token generated after the first token for successful responses."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1.0, 2.5),
		),
	}
}
