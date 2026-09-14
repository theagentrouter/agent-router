// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package endpointspec defines the EndpointSpec which is to bundle the translator, tracing
// and most importantly request and response types for different API endpoints.
package endpointspec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

type (
	// Spec defines methods for parsing request bodies and selecting translators
	// for different API endpoints.
	//
	// Type Parameters:
	// * ReqT: The request type.
	// * RespT: The response type.
	// * RespChunkT: The chunk type for streaming responses.
	//
	// This must be implemented by specific endpoint handlers to provide
	// custom logic for parsing and translation.
	Spec[ReqT, RespT, RespChunkT any] interface {
		// ParseBody parses the request body and returns the original model,
		// the parsed request, whether the request is streaming, any mutated body,
		// and an error if parsing fails.
		//
		// Parameters:
		// * body: The raw request body as a byte slice.
		// * costConfigured: A boolean indicating if cost metrics are configured.
		//
		// Returns:
		// * originalModel: The original model specified in the request.
		// * req: The parsed request of type ReqT.
		// * stream: A boolean indicating if the request is for streaming responses.
		// * mutatedBody: The possibly mutated request body as a byte slice. Or nil if no mutation is needed.
		// * err: An error if parsing fails.
		ParseBody(body []byte, costConfigured bool) (originalModel internalapi.OriginalModel, req *ReqT, stream bool, mutatedBody []byte, err error)
		// GetTranslator selects the appropriate translator based on the output API schema
		// and an optional model name override.
		//
		// Parameters:
		// * out: The output API schema for which the translator is needed.
		// * modelNameOverride: An optional model name to override the one specified in the request.
		//
		// Returns:
		// * translator: The selected translator of type Translator[ReqT, RespT, RespChunkT].
		// * err: An error if translator selection fails.
		GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.Translator[ReqT, tracingapi.Span[RespT, RespChunkT]], error)
		// RedactSensitiveInfoFromRequest creates a redacted copy of the request for safe debug logging.
		// Sensitive content (messages, images, audio, tool parameters, etc.) is replaced with placeholders
		// containing length and hash information to aid in debugging cache hits/misses and correlation.
		//
		// The returned request preserves the structure but removes actual sensitive data, making it
		// safe to include in logs. It should NOT be used for actual AI provider requests.
		//
		// Parameters:
		// * req: The original request to redact.
		//
		// Returns:
		// * redactedReq: A copy with sensitive fields replaced by [REDACTED LENGTH=n HASH=xxxx] placeholders.
		// * err: An error if redaction fails (implementation-specific).
		RedactSensitiveInfoFromRequest(req *ReqT) (redactedReq *ReqT, err error)
		// ParseMultipartBody parses a multipart/form-data request body.
		// Endpoints that don't support multipart should return an error.
		//
		// Parameters:
		// * body: The raw multipart request body.
		// * contentType: The Content-Type header value (includes boundary parameter).
		// * costConfigured: A boolean indicating if cost metrics are configured.
		//
		// Returns the same tuple as ParseBody.
		ParseMultipartBody(body []byte, contentType string, costConfigured bool) (originalModel internalapi.OriginalModel, req *ReqT, stream bool, mutatedBody []byte, err error)
	}
	// ChatCompletionsEndpointSpec implements EndpointSpec for /v1/chat/completions.
	ChatCompletionsEndpointSpec struct{}
	// CompletionsEndpointSpec implements EndpointSpec for /v1/completions.
	CompletionsEndpointSpec struct{}
	// EmbeddingsEndpointSpec implements EndpointSpec for /v1/embeddings.
	EmbeddingsEndpointSpec struct{}
	// ImageGenerationEndpointSpec implements EndpointSpec for /v1/images/generations.
	ImageGenerationEndpointSpec struct{}
	// ResponsesEndpointSpec implements EndpointSpec for /v1/responses.
	ResponsesEndpointSpec struct{}
	// MessagesEndpointSpec implements EndpointSpec for /v1/messages.
	MessagesEndpointSpec struct{}
	// RerankEndpointSpec implements EndpointSpec for /v2/rerank.
	RerankEndpointSpec struct{}
	// SpeechEndpointSpec implements EndpointSpec for /v1/audio/speech.
	SpeechEndpointSpec struct{}
	// TranscriptionEndpointSpec implements EndpointSpec for /v1/audio/transcriptions.
	TranscriptionEndpointSpec struct{}
	// TranslationEndpointSpec implements EndpointSpec for /v1/audio/translations.
	TranslationEndpointSpec struct{}
	// TokenizeEndpointSpec implements EndpointSpec for /tokenize.
	TokenizeEndpointSpec struct{}
	// ResponsesInputTokensEndpointSpec implements EndpointSpec for /v1/responses/input_tokens.
	ResponsesInputTokensEndpointSpec struct{}
	// MessagesCountTokensEndpointSpec implements EndpointSpec for /v1/messages/count_tokens.
	MessagesCountTokensEndpointSpec struct{}
)

var errMultipartNotSupported = fmt.Errorf("%w: multipart body not supported for this endpoint", internalapi.ErrMalformedRequest)

// ParseBody implements [EndpointSpec.ParseBody].
func (ChatCompletionsEndpointSpec) ParseBody(
	body []byte,
	costConfigured bool,
) (internalapi.OriginalModel, *openai.ChatCompletionRequest, bool, []byte, error) {
	var req openai.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/chat/completions: %w", internalapi.ErrMalformedRequest, err)
	}
	var mutatedBody []byte
	if req.Stream && costConfigured && (req.StreamOptions == nil || !req.StreamOptions.IncludeUsage) {
		// If the request is a streaming request and cost metrics are configured, we need to include usage in the response
		// to avoid the bypassing of the token usage calculation.
		req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
		// Rewrite the original bytes to include the stream_options.include_usage=true so that forcing the request body
		// mutation, which uses this raw body, will also result in the stream_options.include_usage=true.
		var err error
		mutatedBody, err = sjson.SetBytesOptions(body, "stream_options.include_usage", true, &sjson.Options{
			Optimistic: true,
			// Note: it is safe to do in-place replacement since this route level processor is executed once per request,
			// and the result can be safely shared among possible multiple retries.
			ReplaceInPlace: true,
		})
		if err != nil {
			return "", nil, false, nil, fmt.Errorf("%w: failed to set stream_options.include_usage", internalapi.ErrMalformedRequest)
		}
	}
	return req.Model, &req, req.Stream, mutatedBody, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (ChatCompletionsEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.ChatCompletionRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (ChatCompletionsEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.OpenAIChatCompletionTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewChatCompletionOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAWSOpenAI:
		return translator.NewChatCompletionOpenAIToAWSOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAWSBedrock:
		return translator.NewChatCompletionOpenAIToAWSBedrockTranslator(modelNameOverride), nil
	case filterapi.APISchemaAWSAnthropic:
		return translator.NewChatCompletionOpenAIToAWSAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAzureOpenAI:
		return translator.NewChatCompletionOpenAIToAzureOpenAITranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaGCPVertexAI:
		return translator.NewChatCompletionOpenAIToGCPVertexAITranslator(modelNameOverride), nil
	case filterapi.APISchemaGCPAnthropic:
		return translator.NewChatCompletionOpenAIToGCPAnthropicTranslator(schema.Version, modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (ChatCompletionsEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.ChatCompletionRequest) (redactedReq *openai.ChatCompletionRequest, err error) {
	// Create a shallow copy of the request
	redacted := *req

	// Redact all message content (user prompts, assistant responses, system messages, etc.)
	redacted.Messages = make([]openai.ChatCompletionMessageParamUnion, len(req.Messages))
	for i, msg := range req.Messages {
		redacted.Messages[i] = redactMessage(msg)
	}

	// Redact prediction content if present (cached prompts, prefill content)
	if req.PredictionContent != nil {
		redactedPrediction := *req.PredictionContent
		redactedPrediction.Content = redactContentUnion(req.PredictionContent.Content)
		redacted.PredictionContent = &redactedPrediction
	}

	// Tool definitions (name, description, parameters) are developer-authored schema metadata,
	// not user data — kept as-is.
	// Response format schemas and guided_json are developer-authored — kept as-is.

	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (CompletionsEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *openai.CompletionRequest, bool, []byte, error) {
	var openAIReq openai.CompletionRequest
	if err := json.Unmarshal(body, &openAIReq); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/completions: %w", internalapi.ErrMalformedRequest, err)
	}
	return openAIReq.Model, &openAIReq, openAIReq.Stream, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (CompletionsEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.CompletionRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (CompletionsEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.OpenAICompletionTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewCompletionOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (CompletionsEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.CompletionRequest) (redactedReq *openai.CompletionRequest, err error) {
	// The prompt is user-supplied text (string / []string / token arrays). Its value is a
	// bare interface{} populated by a custom unmarshaler, so redact it via the generic
	// JSON-tree redactor rather than enumerating every union variant.
	redacted := *req
	redacted.Prompt = openai.PromptUnion{Value: redactInterfaceValue(req.Prompt.Value)}
	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (EmbeddingsEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *openai.EmbeddingRequest, bool, []byte, error) {
	var openAIReq openai.EmbeddingRequest
	if err := json.Unmarshal(body, &openAIReq); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/embeddings: %w", internalapi.ErrMalformedRequest, err)
	}
	return openAIReq.Model, &openAIReq, false, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (EmbeddingsEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.EmbeddingRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (EmbeddingsEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.OpenAIEmbeddingTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewEmbeddingOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAzureOpenAI:
		return translator.NewEmbeddingOpenAIToAzureOpenAITranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaGCPVertexAI:
		return translator.NewEmbeddingOpenAIToGCPVertexAITranslator("", modelNameOverride), nil
	case filterapi.APISchemaAWSBedrock:
		return translator.NewEmbeddingOpenAIToAWSBedrockTranslator(modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (EmbeddingsEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.EmbeddingRequest) (redactedReq *openai.EmbeddingRequest, err error) {
	redacted := *req
	switch {
	case req.OfCompletion != nil:
		// Input is user-supplied text (string / []string / objects) in a bare-any union.
		redactedOfCompletion := *req.OfCompletion
		redactedOfCompletion.Input = openai.EmbeddingRequestInput{Value: redactInterfaceValue(req.OfCompletion.Input.Value)}
		redacted.OfCompletion = &redactedOfCompletion
		redacted.OfChat = nil
	case req.OfChat != nil:
		// Messages are chat content — reuse the ChatCompletions redaction helpers.
		redactedOfChat := *req.OfChat
		redactedOfChat.Messages = make([]openai.ChatCompletionMessageParamUnion, len(req.OfChat.Messages))
		for i, msg := range req.OfChat.Messages {
			redactedOfChat.Messages[i] = redactMessage(msg)
		}
		redacted.OfChat = &redactedOfChat
		redacted.OfCompletion = nil
	}
	return &redacted, nil
}

func (ImageGenerationEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *openai.ImageGenerationRequest, bool, []byte, error) {
	var openAIReq openai.ImageGenerationRequest
	if err := json.Unmarshal(body, &openAIReq); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/images/generations: %w", internalapi.ErrMalformedRequest, err)
	}
	return openAIReq.Model, &openAIReq, false, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (ImageGenerationEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.ImageGenerationRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (ImageGenerationEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.OpenAIImageGenerationTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewImageGenerationOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (ImageGenerationEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.ImageGenerationRequest) (redactedReq *openai.ImageGenerationRequest, err error) {
	// The prompt is user-supplied text describing the desired image; redact it.
	redacted := *req
	redacted.Prompt = redaction.RedactString(req.Prompt)
	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (ResponsesEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *openai.ResponseRequest, bool, []byte, error) {
	var openAIReq openai.ResponseRequest
	if err := json.Unmarshal(body, &openAIReq); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/responses: %w", internalapi.ErrMalformedRequest, err)
	}
	return openAIReq.Model, &openAIReq, openAIReq.Stream, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (ResponsesEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.ResponseRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (ResponsesEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.OpenAIResponsesTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewResponsesOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAWSOpenAI:
		return translator.NewResponsesOpenAIToAWSOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAzureOpenAI:
		return translator.NewResponsesOpenAIToAzureOpenAITranslator(schema.Version, modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (ResponsesEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.ResponseRequest) (redactedReq *openai.ResponseRequest, err error) {
	redacted := *req
	// Instructions and User are user-supplied text / PII.
	redacted.Instructions = redaction.RedactString(req.Instructions)
	redacted.User = redaction.RedactString(req.User)
	// Input and Prompt are JSON unions holding conversation items / content — redact every
	// nested string leaf via the generic redactor so user content inside any item type is caught.
	redacted.Input = redactUnionField(req.Input)
	redacted.Prompt = redactUnionField(req.Prompt)
	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (MessagesEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *anthropic.MessagesRequest, bool, []byte, error) {
	var anthropicReq anthropic.MessagesRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/messages: %w", internalapi.ErrMalformedRequest, err)
	}

	model := anthropicReq.Model
	if model == "" {
		return "", nil, false, nil, fmt.Errorf("%w: model field is required", internalapi.ErrInvalidRequestBody)
	}

	stream := anthropicReq.Stream
	return model, &anthropicReq, stream, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (MessagesEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *anthropic.MessagesRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (MessagesEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.AnthropicMessagesTranslator, error) {
	// Messages processor only supports Anthropic-native translators.
	switch schema.Name {
	case filterapi.APISchemaGCPAnthropic:
		return translator.NewAnthropicToGCPAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAWSAnthropic:
		return translator.NewAnthropicToAWSAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAnthropic:
		return translator.NewAnthropicToAnthropicTranslator(schema.AnthropicPrefix(), modelNameOverride), nil
	case filterapi.APISchemaOpenAI:
		return translator.NewAnthropicToChatCompletionOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAWSBedrock:
		return translator.NewAnthropicToAWSBedrockTranslator(modelNameOverride), nil
	default:
		return nil, fmt.Errorf("/v1/messages endpoint only supports backends that return native Anthropic format (Anthropic, GCPAnthropic, AWSAnthropic). OpenAI and AWSBedrock translation is also supported. Backend %s uses different model format", schema.Name)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (MessagesEndpointSpec) RedactSensitiveInfoFromRequest(req *anthropic.MessagesRequest) (redactedReq *anthropic.MessagesRequest, err error) {
	redacted := *req
	// Message content (text or []ContentBlockParam) and the system prompt hold user/developer
	// text. They are JSON unions, so redact every nested string leaf generically.
	redacted.Messages = make([]anthropic.MessageParam, len(req.Messages))
	for i, msg := range req.Messages {
		redactedMsg := msg
		redactedMsg.Content = redactUnionField(msg.Content)
		redacted.Messages[i] = redactedMsg
	}
	if req.System != nil {
		redacted.System = ptr.To(redactUnionField(*req.System))
	}
	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (MessagesCountTokensEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *anthropic.CountTokensRequest, bool, []byte, error) {
	var req anthropic.CountTokensRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/messages/count_tokens: %w", internalapi.ErrMalformedRequest, err)
	}

	model := req.Model
	if model == "" {
		return "", nil, false, nil, fmt.Errorf("%w: model field is required", internalapi.ErrInvalidRequestBody)
	}

	return model, &req, false, nil, nil
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (MessagesCountTokensEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.AnthropicCountTokensTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaGCPAnthropic:
		return translator.NewCountTokensToGCPAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAWSAnthropic, filterapi.APISchemaAWSBedrock:
		return translator.NewCountTokensToAWSAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAnthropic:
		return translator.NewCountTokensToAnthropicTranslator(modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema for /v1/messages/count_tokens: backend=%s", schema)
	}
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (MessagesCountTokensEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *anthropic.CountTokensRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (MessagesCountTokensEndpointSpec) RedactSensitiveInfoFromRequest(req *anthropic.CountTokensRequest) (redactedReq *anthropic.CountTokensRequest, err error) {
	return req, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (RerankEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *cohereschema.RerankV2Request, bool, []byte, error) {
	var req cohereschema.RerankV2Request
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v2/rerank: %w", internalapi.ErrMalformedRequest, err)
	}
	return req.Model, &req, false, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (RerankEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *cohereschema.RerankV2Request, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (RerankEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.CohereRerankTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaCohere:
		return translator.NewRerankCohereToCohereTranslator(schema.Version, modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (RerankEndpointSpec) RedactSensitiveInfoFromRequest(req *cohereschema.RerankV2Request) (redactedReq *cohereschema.RerankV2Request, err error) {
	redacted := *req
	// The query and the documents being ranked are user-provided content.
	redacted.Query = redaction.RedactString(req.Query)
	redacted.Documents = make([]string, len(req.Documents))
	for i, doc := range req.Documents {
		redacted.Documents[i] = redaction.RedactString(doc)
	}
	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (TokenizeEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *tokenize.RequestUnion, bool, []byte, error) {
	// RequestUnion.UnmarshalJSON discriminates chat vs completion by the presence
	// of a "messages" field, so a single unmarshal is enough here.
	var req tokenize.RequestUnion
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /tokenize: %w", internalapi.ErrMalformedRequest, err)
	}

	if err := req.Validate(); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: invalid tokenize request: %w", internalapi.ErrMalformedRequest, err)
	}

	var model string
	if req.CompletionRequest != nil {
		model = req.CompletionRequest.Model
	} else if req.ChatRequest != nil {
		model = req.ChatRequest.Model
	}

	// Tokenize requests are never streaming
	return model, &req, false, nil, nil
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (TokenizeEndpointSpec) GetTranslator(schema filterapi.VersionedAPISchema, modelNameOverride string) (translator.TokenizeTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewTokenizeTranslator(modelNameOverride), nil
	case filterapi.APISchemaGCPVertexAI:
		return translator.NewTokenizeToGCPVertexAITranslator(modelNameOverride), nil
	case filterapi.APISchemaGCPAnthropic:
		return translator.NewTokenizeToGCPAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAWSAnthropic:
		return translator.NewTokenizeToAWSAnthropicTranslator(schema.Version, modelNameOverride), nil
	case filterapi.APISchemaAWSBedrock:
		return translator.NewTokenizeToAWSBedrockTranslator(modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema for tokenize endpoint: backend=%s", schema.Name)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (TokenizeEndpointSpec) RedactSensitiveInfoFromRequest(req *tokenize.RequestUnion) (redactedReq *tokenize.RequestUnion, err error) {
	redacted := *req
	switch {
	case req.CompletionRequest != nil:
		redactedOfCompletion := *req.CompletionRequest
		// The prompt is user-supplied text to tokenize.
		redactedOfCompletion.Prompt = redaction.RedactString(req.Prompt)
		redacted.CompletionRequest = &redactedOfCompletion
		redacted.ChatRequest = nil
	case req.ChatRequest != nil:
		// Messages are chat content — reuse the ChatCompletions redaction helpers.
		redactedOfChat := *req.ChatRequest
		redactedOfChat.Messages = make([]openai.ChatCompletionMessageParamUnion, len(req.Messages))
		for i, msg := range req.Messages {
			redactedOfChat.Messages[i] = redactMessage(msg)
		}
		redacted.ChatRequest = &redactedOfChat
		redacted.CompletionRequest = nil
	}
	return &redacted, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (TokenizeEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *tokenize.RequestUnion, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// redactMessage redacts sensitive content from a chat message while preserving its type and structure.
// This dispatches to role-specific redaction functions based on the message type.
func redactMessage(msg openai.ChatCompletionMessageParamUnion) openai.ChatCompletionMessageParamUnion {
	switch {
	case msg.OfUser != nil:
		return redactUserMessage(msg)
	case msg.OfAssistant != nil:
		return redactAssistantMessage(msg)
	case msg.OfSystem != nil:
		return redactSystemMessage(msg)
	case msg.OfDeveloper != nil:
		return redactDeveloperMessage(msg)
	case msg.OfTool != nil:
		return redactToolMessage(msg)
	default:
		return msg
	}
}

// redactUserMessage redacts content from a user message, including text, images, audio, and files.
func redactUserMessage(msg openai.ChatCompletionMessageParamUnion) openai.ChatCompletionMessageParamUnion {
	redactedMsg := *msg.OfUser
	redactedMsg.Content = redactStringOrUserRoleContentUnion(msg.OfUser.Content)
	return openai.ChatCompletionMessageParamUnion{OfUser: &redactedMsg}
}

// redactAssistantMessage redacts content from an assistant message, including message text and tool call arguments.
func redactAssistantMessage(msg openai.ChatCompletionMessageParamUnion) openai.ChatCompletionMessageParamUnion {
	redactedMsg := *msg.OfAssistant
	redactedMsg.Content = redactStringOrAssistantRoleContentUnion(msg.OfAssistant.Content)
	// Redact tool call arguments (may contain data derived from user messages).
	// Function name is kept — it is the tool API name, not user data.
	if len(msg.OfAssistant.ToolCalls) > 0 {
		redactedMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.OfAssistant.ToolCalls))
		for i, tc := range msg.OfAssistant.ToolCalls {
			redactedToolCall := tc
			redactedToolCall.Function.Arguments = redaction.RedactString(tc.Function.Arguments)
			redactedMsg.ToolCalls[i] = redactedToolCall
		}
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &redactedMsg}
}

// redactSystemMessage redacts content from a system message.
func redactSystemMessage(msg openai.ChatCompletionMessageParamUnion) openai.ChatCompletionMessageParamUnion {
	redactedMsg := *msg.OfSystem
	redactedMsg.Content = redactContentUnion(msg.OfSystem.Content)
	return openai.ChatCompletionMessageParamUnion{OfSystem: &redactedMsg}
}

// redactDeveloperMessage redacts content from a developer message.
func redactDeveloperMessage(msg openai.ChatCompletionMessageParamUnion) openai.ChatCompletionMessageParamUnion {
	redactedMsg := *msg.OfDeveloper
	redactedMsg.Content = redactContentUnion(msg.OfDeveloper.Content)
	return openai.ChatCompletionMessageParamUnion{OfDeveloper: &redactedMsg}
}

// redactToolMessage redacts content from a tool message.
func redactToolMessage(msg openai.ChatCompletionMessageParamUnion) openai.ChatCompletionMessageParamUnion {
	redactedMsg := *msg.OfTool
	redactedMsg.Content = redactContentUnion(msg.OfTool.Content)
	return openai.ChatCompletionMessageParamUnion{OfTool: &redactedMsg}
}

// redactContentUnion redacts content from a ContentUnion, handling both string and structured content parts.
func redactContentUnion(content openai.ContentUnion) openai.ContentUnion {
	switch v := content.Value.(type) {
	case string:
		return openai.ContentUnion{Value: redaction.RedactString(v)}
	case []openai.ChatCompletionContentPartTextParam:
		redactedParts := make([]openai.ChatCompletionContentPartTextParam, len(v))
		for i, part := range v {
			redactedPart := part
			redactedPart.Text = redaction.RedactString(part.Text)
			redactedParts[i] = redactedPart
		}
		return openai.ContentUnion{Value: redactedParts}
	default:
		return content
	}
}

// redactStringOrUserRoleContentUnion redacts content from a StringOrUserRoleContentUnion.
func redactStringOrUserRoleContentUnion(content openai.StringOrUserRoleContentUnion) openai.StringOrUserRoleContentUnion {
	switch v := content.Value.(type) {
	case string:
		return openai.StringOrUserRoleContentUnion{Value: redaction.RedactString(v)}
	case []openai.ChatCompletionContentPartUserUnionParam:
		redactedParts := make([]openai.ChatCompletionContentPartUserUnionParam, len(v))
		for i, part := range v {
			redactedParts[i] = redactUserContentPart(part)
		}
		return openai.StringOrUserRoleContentUnion{Value: redactedParts}
	default:
		return content
	}
}

// redactStringOrAssistantRoleContentUnion redacts content from a StringOrAssistantRoleContentUnion.
func redactStringOrAssistantRoleContentUnion(content openai.StringOrAssistantRoleContentUnion) openai.StringOrAssistantRoleContentUnion {
	switch v := content.Value.(type) {
	case string:
		return openai.StringOrAssistantRoleContentUnion{Value: redaction.RedactString(v)}
	case []openai.ChatCompletionAssistantMessageParamContent:
		redactedParts := make([]openai.ChatCompletionAssistantMessageParamContent, len(v))
		for i, part := range v {
			redactedPart := part
			if part.Text != nil {
				redactedText := redaction.RedactString(*part.Text)
				redactedPart.Text = &redactedText
			}
			if part.Refusal != nil {
				redactedRefusal := redaction.RedactString(*part.Refusal)
				redactedPart.Refusal = &redactedRefusal
			}
			redactedParts[i] = redactedPart
		}
		return openai.StringOrAssistantRoleContentUnion{Value: redactedParts}
	case openai.ChatCompletionAssistantMessageParamContent:
		redactedPart := v
		if v.Text != nil {
			redactedText := redaction.RedactString(*v.Text)
			redactedPart.Text = &redactedText
		}
		if v.Refusal != nil {
			redactedRefusal := redaction.RedactString(*v.Refusal)
			redactedPart.Refusal = &redactedRefusal
		}
		return openai.StringOrAssistantRoleContentUnion{Value: redactedPart}
	default:
		return content
	}
}

// redactUserContentPart redacts sensitive content from a user message content part.
// Handles multiple content types: text, images (URLs or base64), audio, and file attachments.
func redactUserContentPart(part openai.ChatCompletionContentPartUserUnionParam) openai.ChatCompletionContentPartUserUnionParam {
	redacted := part

	// Redact plain text content
	if part.OfText != nil {
		redactedText := *part.OfText
		redactedText.Text = redaction.RedactString(part.OfText.Text)
		redacted.OfText = &redactedText
	}

	// Redact image URLs (may be data URLs with embedded base64 image data)
	if part.OfImageURL != nil {
		redactedImage := *part.OfImageURL
		redactedImage.ImageURL.URL = redaction.RedactString(part.OfImageURL.ImageURL.URL)
		redacted.OfImageURL = &redactedImage
	}

	// Redact audio data (typically base64-encoded audio)
	if part.OfInputAudio != nil {
		redactedAudio := *part.OfInputAudio
		redactedAudio.InputAudio.Data = redaction.RedactString(part.OfInputAudio.InputAudio.Data)
		redacted.OfInputAudio = &redactedAudio
	}

	// Redact file attachments (may contain sensitive documents or data)
	if part.OfFile != nil {
		redactedFile := *part.OfFile
		if part.OfFile.File.FileData != "" {
			redactedFile.File.FileData = redaction.RedactString(part.OfFile.File.FileData)
		}
		redacted.OfFile = &redactedFile
	}

	return redacted
}

// redactInterfaceValue redacts a bare interface{}/any field value (e.g.
// PromptUnion.Value, EmbeddingRequestInput.Value) whose concrete type is
// determined by a custom unmarshaler and may be a string, []string, [][]int,
// or arbitrary object tree. It normalizes via a marshal→unmarshal-into-any
// round-trip so [redaction.RedactJSONTree] sees the standard []any/map[string]any
// shapes, then redacts every string leaf. On a marshal error (the only reachable
// error path) it returns a single placeholder string so no raw content is ever
// logged; the subsequent unmarshal-into-any cannot fail on valid JSON output.
func redactInterfaceValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return redaction.RedactString("redaction-error")
	}
	// b is valid JSON straight from json.Marshal and generic is *any, so this
	// unmarshal cannot fail; discard the error and redact the generic tree.
	var generic any
	_ = json.Unmarshal(b, &generic)
	return redaction.RedactJSONTree(generic)
}

// redactUnionField redacts a typed-struct field whose value is a JSON union
// (e.g. MessageContent, SystemPrompt, ResponseNewParamsInputUnion,
// ResponsePromptParam). It marshals the field, redacts every string leaf in the
// resulting JSON tree, and unmarshals back into a new T. On a marshal error it
// returns the zero value of T (nil for pointers, an empty struct otherwise) so
// the field logs as absent rather than leaking content.
//
// The intermediate unmarshal-into-any and the re-marshal of the redacted tree
// are provably infallible — b is valid JSON from json.Marshal into *any, and
// [redaction.RedactJSONTree] yields only JSON-native types — so their errors are
// discarded. The final unmarshal back into T is fail-safe: on the impossible
// structural mismatch, out stays zero.
//
// Marshaling and unmarshaling go through pointers (&in / &out) so that any
// pointer-receiver MarshalJSON/UnmarshalJSON on T is actually invoked — passing
// a value T to json.Marshal would skip pointer-receiver methods and fall back to
// default struct marshaling, corrupting the union.
func redactUnionField[T any](in T) (out T) {
	b, err := json.Marshal(&in)
	if err != nil {
		return out
	}
	// b is valid JSON from json.Marshal and generic is *any, so this unmarshal
	// cannot fail. RedactJSONTree then redacts every string leaf, preserving
	// "type" discriminators so the tree still round-trips into T.
	var generic any
	_ = json.Unmarshal(b, &generic)
	// RedactJSONTree yields only JSON-native types, which json.Marshal always
	// accepts, so this re-marshal cannot fail either.
	redacted, _ := json.Marshal(redaction.RedactJSONTree(generic))
	_ = json.Unmarshal(redacted, &out) // on error, out stays zero (fail-safe)
	return out
}

// ParseBody implements [EndpointSpec.ParseBody].
func (SpeechEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *openai.SpeechRequest, bool, []byte, error) {
	var req openai.SpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("failed to unmarshal speech request: %w", err)
	}

	// Determine if streaming based on stream_format
	stream := req.StreamFormat != nil && *req.StreamFormat == openai.StreamFormatSSE

	return req.Model, &req, stream, nil, nil
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (SpeechEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.SpeechRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (SpeechEndpointSpec) GetTranslator(
	schema filterapi.VersionedAPISchema,
	modelNameOverride string,
) (translator.OpenAISpeechTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewSpeechOpenAIToOpenAITranslator(
			schema.OpenAIPrefix(),
			modelNameOverride,
		), nil
	default:
		return nil, fmt.Errorf("unsupported API schema for speech: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (SpeechEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.SpeechRequest) (redactedReq *openai.SpeechRequest, err error) {
	// Create a shallow copy of the request
	redacted := *req

	// Redact the input text (contains user-provided text to be synthesized)
	redacted.Input = redaction.RedactString(req.Input)

	// Redact instructions if present (may contain sensitive context)
	if req.Instructions != nil {
		redactedInstructions := redaction.RedactString(*req.Instructions)
		redacted.Instructions = &redactedInstructions
	}

	return &redacted, nil
}

// ParseBody implements [Spec.ParseBody]. Transcription uses multipart, so JSON body is not expected.
func (TranscriptionEndpointSpec) ParseBody(
	_ []byte, _ bool,
) (internalapi.OriginalModel, *openai.TranscriptionRequest, bool, []byte, error) {
	return "", nil, false, nil, fmt.Errorf("%w: expected multipart/form-data content type for /v1/audio/transcriptions", internalapi.ErrMalformedRequest)
}

// ParseMultipartBody implements [Spec.ParseMultipartBody] for /v1/audio/transcriptions.
func (TranscriptionEndpointSpec) ParseMultipartBody(
	body []byte, contentType string, _ bool,
) (internalapi.OriginalModel, *openai.TranscriptionRequest, bool, []byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse multipart form data: %w", internalapi.ErrMalformedRequest, err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse multipart form data: missing boundary", internalapi.ErrMalformedRequest)
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var req openai.TranscriptionRequest
	var hasModel, hasFile bool

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, false, nil, fmt.Errorf("%w: failed to parse multipart form data: %w", internalapi.ErrMalformedRequest, err)
		}

		switch part.FormName() {
		case "model":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read model field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.Model = val
			hasModel = true
		case "file":
			hasFile = true
			req.FileName = part.FileName()
			n, err := io.Copy(io.Discard, part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read file field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.FileSize = n
		case "language":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read language field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.Language = val
		case "prompt":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read prompt field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.Prompt = val
		case "response_format":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read response_format field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.ResponseFormat = val
		case "temperature":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read temperature field: %w", internalapi.ErrMalformedRequest, err)
			}
			t, parseErr := strconv.ParseFloat(val, 64)
			if parseErr != nil {
				return "", nil, false, nil, fmt.Errorf("%w: invalid temperature value %q: %w", internalapi.ErrMalformedRequest, val, parseErr)
			}
			req.Temperature = &t
		case "stream":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read stream field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.Stream = strings.EqualFold(val, "true")
		case "timestamp_granularities[]":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read timestamp_granularities field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.TimestampGranularities = append(req.TimestampGranularities, val)
		}
	}

	if !hasModel {
		return "", nil, false, nil, fmt.Errorf("%w: missing required field 'model'", internalapi.ErrMalformedRequest)
	}
	if !hasFile {
		return "", nil, false, nil, fmt.Errorf("%w: missing required field 'file'", internalapi.ErrMalformedRequest)
	}

	return req.Model, &req, req.Stream, nil, nil
}

// GetTranslator implements [Spec.GetTranslator].
func (TranscriptionEndpointSpec) GetTranslator(
	schema filterapi.VersionedAPISchema, modelNameOverride string,
) (translator.OpenAIAudioTranscriptionTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewTranscriptionOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema for audio transcription: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [Spec.RedactSensitiveInfoFromRequest].
func (TranscriptionEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.TranscriptionRequest) (*openai.TranscriptionRequest, error) {
	redacted := *req
	redacted.Prompt = redaction.RedactString(req.Prompt)
	return &redacted, nil
}

// ParseBody implements [Spec.ParseBody]. Translation uses multipart, so JSON body is not expected.
func (TranslationEndpointSpec) ParseBody(
	_ []byte, _ bool,
) (internalapi.OriginalModel, *openai.TranslationRequest, bool, []byte, error) {
	return "", nil, false, nil, fmt.Errorf("%w: expected multipart/form-data content type for /v1/audio/translations", internalapi.ErrMalformedRequest)
}

// ParseMultipartBody implements [Spec.ParseMultipartBody] for /v1/audio/translations.
// OpenAI's translation endpoint does not support streaming, so the stream return value is
// always false.
func (TranslationEndpointSpec) ParseMultipartBody(
	body []byte, contentType string, _ bool,
) (internalapi.OriginalModel, *openai.TranslationRequest, bool, []byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse multipart form data: %w", internalapi.ErrMalformedRequest, err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse multipart form data: missing boundary", internalapi.ErrMalformedRequest)
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var req openai.TranslationRequest
	var hasModel, hasFile bool

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, false, nil, fmt.Errorf("%w: failed to parse multipart form data: %w", internalapi.ErrMalformedRequest, err)
		}

		switch part.FormName() {
		case "model":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read model field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.Model = val
			hasModel = true
		case "file":
			hasFile = true
			req.FileName = part.FileName()
			n, err := io.Copy(io.Discard, part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read file field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.FileSize = n
		case "prompt":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read prompt field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.Prompt = val
		case "response_format":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read response_format field: %w", internalapi.ErrMalformedRequest, err)
			}
			req.ResponseFormat = val
		case "temperature":
			val, err := readFormField(part)
			if err != nil {
				return "", nil, false, nil, fmt.Errorf("%w: failed to read temperature field: %w", internalapi.ErrMalformedRequest, err)
			}
			t, parseErr := strconv.ParseFloat(val, 64)
			if parseErr != nil {
				return "", nil, false, nil, fmt.Errorf("%w: invalid temperature value %q: %w", internalapi.ErrMalformedRequest, val, parseErr)
			}
			req.Temperature = &t
		}
	}

	if !hasModel {
		return "", nil, false, nil, fmt.Errorf("%w: missing required field 'model'", internalapi.ErrMalformedRequest)
	}
	if !hasFile {
		return "", nil, false, nil, fmt.Errorf("%w: missing required field 'file'", internalapi.ErrMalformedRequest)
	}

	return req.Model, &req, false, nil, nil
}

// GetTranslator implements [Spec.GetTranslator].
func (TranslationEndpointSpec) GetTranslator(
	schema filterapi.VersionedAPISchema, modelNameOverride string,
) (translator.OpenAIAudioTranslationTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewTranslationOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema for audio translation: backend=%s", schema)
	}
}

// RedactSensitiveInfoFromRequest implements [Spec.RedactSensitiveInfoFromRequest].
func (TranslationEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.TranslationRequest) (*openai.TranslationRequest, error) {
	redacted := *req
	redacted.Prompt = redaction.RedactString(req.Prompt)
	return &redacted, nil
}

// ParseBody implements [EndpointSpec.ParseBody].
func (ResponsesInputTokensEndpointSpec) ParseBody(
	body []byte,
	_ bool,
) (internalapi.OriginalModel, *openai.ResponseRequest, bool, []byte, error) {
	var req openai.ResponseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil, false, nil, fmt.Errorf("%w: failed to parse JSON for /v1/responses/input_tokens: %w", internalapi.ErrMalformedRequest, err)
	}

	return req.Model, &req, false, nil, nil
}

// GetTranslator implements [EndpointSpec.GetTranslator].
func (ResponsesInputTokensEndpointSpec) GetTranslator(
	schema filterapi.VersionedAPISchema, modelNameOverride string,
) (translator.OpenAIResponsesInputTokensTranslator, error) {
	switch schema.Name {
	case filterapi.APISchemaOpenAI:
		return translator.NewResponsesInputTokensOpenAIToOpenAITranslator(schema.OpenAIPrefix(), modelNameOverride), nil
	case filterapi.APISchemaAzureOpenAI:
		return translator.NewResponsesInputTokensOpenAIToAzureOpenAITranslator(schema.Version, modelNameOverride), nil
	default:
		return nil, fmt.Errorf("unsupported API schema for /v1/responses/input_tokens: backend=%s", schema)
	}
}

// ParseMultipartBody implements [Spec.ParseMultipartBody].
func (ResponsesInputTokensEndpointSpec) ParseMultipartBody([]byte, string, bool) (internalapi.OriginalModel, *openai.ResponseRequest, bool, []byte, error) {
	return "", nil, false, nil, errMultipartNotSupported
}

// RedactSensitiveInfoFromRequest implements [EndpointSpec.RedactSensitiveInfoFromRequest].
func (ResponsesInputTokensEndpointSpec) RedactSensitiveInfoFromRequest(req *openai.ResponseRequest) (*openai.ResponseRequest, error) {
	return req, nil
}

// readFormField reads the entire value of a multipart form field as a string.
func readFormField(part *multipart.Part) (string, error) {
	data, err := io.ReadAll(part)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
