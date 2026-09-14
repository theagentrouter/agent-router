// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"io"
	"log/slog"

	"github.com/tidwall/sjson"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

const (
	pathHeaderName          = ":path"
	statusHeaderName        = ":status"
	contentTypeHeaderName   = "content-type"
	contentLengthHeaderName = "content-length"
	awsErrorTypeHeaderName  = "x-amzn-errortype"
	jsonContentType         = "application/json"
	eventStreamContentType  = "text/event-stream"
	openAIBackendError      = "OpenAIBackendError"
	awsBedrockBackendError  = "AWSBedrockBackendError"

	// Count-tokens route paths per backend.
	anthropicCountTokensPath        = "/v1/messages/count_tokens" // #nosec G101 -- Native Anthropic Messages count_tokens path, not a credential.
	awsBedrockCountTokensPathFormat = "/model/%s/count-tokens"    // #nosec G101 -- AWS Bedrock CountTokens path format (modelId placeholder), not a credential.
	gcpCountTokensModel             = "count-tokens"              // GCP Vertex AI virtual model for count-tokens.
)

// Translator translates the request and response messages between the client
// and the backend API schemas.
//
// ReqT represents the structured request body type.
// SpanT represents the tracing span type passed to ResponseBody. Use any if the implementation does not have tracing span yet.
//
// This is created per request and is not thread-safe.
type Translator[ReqT any, SpanT any] interface {
	// RequestBody translates the request body.
	//     - `raw` is the raw request body.
	//     - `body` is the parsed request body of type *ReqT.
	//     - `flag` is a boolean context flag. Depending on the specific implementation,
	//       this represents either `forceBodyMutation` or `onRetry`.
	RequestBody(raw []byte, body *ReqT, flag bool) (
		newHeaders []internalapi.Header,
		mutatedBody []byte,
		err error,
	)

	// ResponseHeaders translates the response headers.
	ResponseHeaders(headers map[string]string) (
		newHeaders []internalapi.Header,
		err error,
	)

	// ResponseBody translates the response body.
	//     - `span` is the tracing span of type SpanT. Implementations that do not
	//       require tracing in this step can ignore this argument.
	ResponseBody(respHeaders map[string]string, body io.Reader, endOfStream bool, span SpanT) (
		newHeaders []internalapi.Header,
		mutatedBody []byte,
		tokenUsage metrics.TokenUsage,
		responseModel internalapi.ResponseModel,
		err error,
	)

	// ResponseError translates the response error (non-2xx status codes).
	ResponseError(respHeaders map[string]string, body io.Reader) (
		newHeaders []internalapi.Header,
		mutatedBody []byte,
		err error,
	)
}

// ContentTypeSetter is an optional interface that translators can implement
// to receive the original request Content-Type header. This is needed for
// multipart/form-data endpoints where the translator needs the boundary
// to re-encode the body during model name override.
type ContentTypeSetter interface {
	SetContentType(ct string)
}

// RequestHeadersSetter is an optional interface for translators that need
// request headers to translate provider-specific request fields.
type RequestHeadersSetter interface {
	SetRequestHeaders(headers map[string]string)
}

// HeaderValueFilterSetter is an optional interface for translators that can filter individual
// values out of a multi-valued request header before forwarding upstream.
//
// It is called once per configured filter, so implementations must ignore headers they do not
// handle. mode is either "Denylist" (drop the listed values) or "Allowlist" (keep only the listed
// values); an unrecognized mode or an empty value list disables the filter.
type HeaderValueFilterSetter interface {
	SetHeaderValueFilter(name, mode string, values []string)
}

// ResponseRedactor is an optional interface that translators can implement
// to support response body redaction for debug logging.
type ResponseRedactor interface {
	// SetRedactionConfig configures redaction settings for the translator.
	SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger)
	// RedactBody creates a redacted copy of the response for safe logging.
	// Returns a new ChatCompletionResponse with sensitive fields redacted.
	// The original response is never modified.
	RedactBody(resp *openai.ChatCompletionResponse) *openai.ChatCompletionResponse
}

// AnthropicResponseRedactor is an optional interface that Anthropic translators
// can implement to support response body redaction for debug logging.
type AnthropicResponseRedactor interface {
	// SetRedactionConfig configures redaction settings for the translator.
	SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger)
	// RedactAnthropicBody creates a redacted copy of the Anthropic response for safe logging.
	// Returns a new MessagesResponse with sensitive fields redacted.
	// The original response is never modified.
	RedactAnthropicBody(resp *anthropicschema.MessagesResponse) *anthropicschema.MessagesResponse
}

type (
	// OpenAIChatCompletionTranslator translates the OpenAI's /chat/completions endpoint.
	OpenAIChatCompletionTranslator = Translator[openai.ChatCompletionRequest, tracingapi.ChatCompletionSpan]
	// OpenAIEmbeddingTranslator translates the OpenAI's /embeddings endpoint.
	OpenAIEmbeddingTranslator = Translator[openai.EmbeddingRequest, tracingapi.EmbeddingsSpan]
	// OpenAICompletionTranslator translates the OpenAI's /completions endpoint.
	OpenAICompletionTranslator = Translator[openai.CompletionRequest, tracingapi.CompletionSpan]
	// CohereRerankTranslator translates the Cohere's /v2/rerank endpoint.
	CohereRerankTranslator = Translator[cohereschema.RerankV2Request, tracingapi.RerankSpan]
	// AnthropicMessagesTranslator translates the Anthropic's /messages endpoint.
	AnthropicMessagesTranslator = Translator[anthropicschema.MessagesRequest, tracingapi.MessageSpan]
	// OpenAIImageGenerationTranslator translates the OpenAI's /images/generations endpoint.
	OpenAIImageGenerationTranslator = Translator[openai.ImageGenerationRequest, tracingapi.ImageGenerationSpan]
	// OpenAIResponsesTranslator translates the OpenAI's /responses endpoint.
	OpenAIResponsesTranslator = Translator[openai.ResponseRequest, tracingapi.ResponsesSpan]
	// OpenAISpeechTranslator translates the OpenAI's /v1/audio/speech endpoint.
	OpenAISpeechTranslator = Translator[openai.SpeechRequest, tracingapi.SpeechSpan]
	// OpenAIAudioTranscriptionTranslator translates the OpenAI's /v1/audio/transcriptions endpoint.
	OpenAIAudioTranscriptionTranslator = Translator[openai.TranscriptionRequest, tracingapi.TranscriptionSpan]
	// OpenAIAudioTranslationTranslator translates the OpenAI's /v1/audio/translations endpoint.
	OpenAIAudioTranslationTranslator = Translator[openai.TranslationRequest, tracingapi.TranslationSpan]
	// TokenizeTranslator translates the tokenize endpoint.
	TokenizeTranslator = Translator[tokenize.RequestUnion, tracingapi.TokenizeSpan]
	// OpenAIResponsesInputTokensTranslator translates the OpenAI's /v1/responses/input_tokens endpoint.
	OpenAIResponsesInputTokensTranslator = Translator[openai.ResponseRequest, tracingapi.ResponsesInputTokensSpan]
	// AnthropicCountTokensTranslator translates the Anthropic's /v1/messages/count_tokens endpoint.
	AnthropicCountTokensTranslator = Translator[anthropicschema.CountTokensRequest, tracingapi.CountTokensSpan]
)

var (
	// sjsonOptions are the options used for sjson operations in the translator.
	sjsonOptions = &sjson.Options{
		Optimistic: true,
		// Note: DO NOT set ReplaceInPlace to true since at the translation layer, which might be called multiple times per retry,
		// it must be ensured that the original body is not modified, i.e. the operation must be idempotent.
		ReplaceInPlace: false,
	}
	// sjsonOptionsInPlace are the options used for sjson operations that modify the body in place.
	// Note: make sure the original body is not modified when using this in the translation layer.
	sjsonOptionsInPlace = &sjson.Options{
		Optimistic:     true,
		ReplaceInPlace: true,
	}
)
