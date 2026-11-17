// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicVertex "github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/tidwall/sjson"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// currently a requirement for GCP Vertex / Anthropic API https://docs.anthropic.com/en/api/claude-on-vertex-ai
const (
	gcpBackendError = "GCPBackendError"
)

// NewChatCompletionOpenAIToGCPAnthropicTranslator implements [Factory] for OpenAI to GCP Anthropic translation.
// This translator converts OpenAI ChatCompletion API requests to GCP Anthropic API format.
func NewChatCompletionOpenAIToGCPAnthropicTranslator(apiVersion string, modelNameOverride internalapi.ModelNameOverride) OpenAIChatCompletionTranslator {
	return &openAIToGCPAnthropicTranslatorV1ChatCompletion{
		apiVersion:        apiVersion,
		modelNameOverride: modelNameOverride,
	}
}

// openAIToGCPAnthropicTranslatorV1ChatCompletion translates OpenAI Chat Completions API to GCP Anthropic Claude API.
// This uses the Claude rawPredict and streamRawPredict APIs on Vertex AI:
// https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude
type openAIToGCPAnthropicTranslatorV1ChatCompletion struct {
	apiVersion        string
	modelNameOverride internalapi.ModelNameOverride
	streamParser      *anthropicStreamParser
	requestModel      internalapi.RequestModel
	// Redaction configuration for debug logging
	debugLogEnabled bool
	enableRedaction bool
	logger          *slog.Logger
}

// RequestBody implements [OpenAIChatCompletionTranslator.RequestBody] for GCP.
func (o *openAIToGCPAnthropicTranslatorV1ChatCompletion) RequestBody(_ []byte, openAIReq *openai.ChatCompletionRequest, _ bool) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	params, err := buildAnthropicParams(openAIReq, filterapi.APISchemaGCPAnthropic, o.modelNameOverride)
	if err != nil {
		return
	}

	body, err := json.Marshal(params)
	if err != nil {
		return
	}

	o.requestModel = openAIReq.Model
	if o.modelNameOverride != "" {
		// Use modelName override if set.
		o.requestModel = o.modelNameOverride
	}

	// GCP VERTEX PATH.
	specifier := "rawPredict"
	if openAIReq.Stream {
		specifier = "streamRawPredict"
		body, err = sjson.SetBytes(body, "stream", true)
		if err != nil {
			return
		}
		o.streamParser = newAnthropicStreamParser(o.requestModel)
	}

	path := buildGCPModelPathSuffix(gcpModelPublisherAnthropic, o.requestModel, specifier)
	// b. Set the "anthropic_version" key in the JSON body
	// Using same logic as anthropic go SDK: https://github.com/anthropics/anthropic-sdk-go/blob/e252e284244755b2b2f6eef292b09d6d1e6cd989/bedrock/bedrock.go#L167
	anthropicVersion := anthropicVertex.DefaultVersion
	if o.apiVersion != "" {
		anthropicVersion = o.apiVersion
	}
	body, err = sjson.SetBytes(body, anthropicVersionKey, anthropicVersion)
	if err != nil {
		return
	}
	newBody = body
	newHeaders = []internalapi.Header{{pathHeaderName, path}, {contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}

// ResponseError implements [OpenAIChatCompletionTranslator.ResponseError].
func (o *openAIToGCPAnthropicTranslatorV1ChatCompletion) ResponseError(respHeaders map[string]string, body io.Reader) (
	newHeaders []internalapi.Header, newBody []byte, err error,
) {
	statusCode := respHeaders[statusHeaderName]
	var openaiError openai.Error
	var decodeErr error

	// Check for a JSON content type to decide how to parse the error.
	if v, ok := respHeaders[contentTypeHeaderName]; ok && strings.Contains(v, jsonContentType) {
		var gcpError anthropic.ErrorResponse
		if decodeErr = json.NewDecoder(body).Decode(&gcpError); decodeErr != nil {
			// If we expect JSON but fail to decode, it's an internal translator error.
			return nil, nil, fmt.Errorf("failed to unmarshal JSON error body: %w", decodeErr)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    gcpError.Error.Type,
				Message: gcpError.Error.Message,
				Code:    &statusCode,
			},
		}
	} else {
		// If not JSON, read the raw body as the error message.
		var buf []byte
		buf, decodeErr = io.ReadAll(body)
		if decodeErr != nil {
			return nil, nil, fmt.Errorf("failed to read raw error body: %w", decodeErr)
		}
		openaiError = openai.Error{
			Type: "error",
			Error: openai.ErrorType{
				Type:    gcpBackendError,
				Message: string(buf),
				Code:    &statusCode,
			},
		}
	}

	// Marshal the translated OpenAI error.
	newBody, err = json.Marshal(openaiError)
	if err != nil {
		// This is an internal failure to create the response.
		return nil, nil, fmt.Errorf("failed to marshal OpenAI error body: %w", err)
	}
	newHeaders = []internalapi.Header{
		{contentTypeHeaderName, jsonContentType},
		{contentLengthHeaderName, strconv.Itoa(len(newBody))},
	}
	return
}

// SetRedactionConfig implements [ResponseRedactor.SetRedactionConfig].
func (o *openAIToGCPAnthropicTranslatorV1ChatCompletion) SetRedactionConfig(debugLogEnabled, enableRedaction bool, logger *slog.Logger) {
	o.debugLogEnabled = debugLogEnabled
	o.enableRedaction = enableRedaction
	o.logger = logger
}

// RedactBody implements [ResponseRedactor.RedactBody].
// Creates a redacted copy of the response for safe logging without modifying the original.
// Reuses the same redaction logic since GCP Anthropic responses are converted to OpenAI format.
func (o *openAIToGCPAnthropicTranslatorV1ChatCompletion) RedactBody(resp *openai.ChatCompletionResponse) *openai.ChatCompletionResponse {
	if resp == nil {
		return nil
	}

	// Create a shallow copy of the response
	redacted := *resp

	// Redact choices (contains AI-generated content)
	if len(resp.Choices) > 0 {
		redacted.Choices = make([]openai.ChatCompletionResponseChoice, len(resp.Choices))
		for i := range resp.Choices {
			redactedChoice := resp.Choices[i]
			redactedChoice.Message = redactGCPAnthropicResponseMessage(&resp.Choices[i].Message)
			redacted.Choices[i] = redactedChoice
		}
	}

	return &redacted
}

// redactGCPAnthropicResponseMessage redacts sensitive content from a GCP Anthropic response message
// that has been converted to OpenAI format.
func redactGCPAnthropicResponseMessage(msg *openai.ChatCompletionResponseChoiceMessage) openai.ChatCompletionResponseChoiceMessage {
	redactedMsg := *msg

	// Redact message content (AI-generated text)
	if msg.Content != nil {
		redactedContent := redaction.RedactString(*msg.Content)
		redactedMsg.Content = &redactedContent
	}

	// Redact tool call arguments (may contain data derived from user messages).
	// Function name is kept — it is the tool API name, not user data.
	if len(msg.ToolCalls) > 0 {
		redactedMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			redactedToolCall := tc
			redactedToolCall.Function.Arguments = redaction.RedactString(tc.Function.Arguments)
			redactedMsg.ToolCalls[i] = redactedToolCall
		}
	}

	// Redact audio data if present
	if msg.Audio != nil {
		redactedAudio := *msg.Audio
		redactedAudio.Data = redaction.RedactString(msg.Audio.Data)
		redactedAudio.Transcript = redaction.RedactString(msg.Audio.Transcript)
		redactedMsg.Audio = &redactedAudio
	}

	// Redact reasoning content if present (thinking blocks from Anthropic)
	if msg.ReasoningContent != nil {
		redactedMsg.ReasoningContent = redactReasoningContent(msg.ReasoningContent)
	}

	return redactedMsg
}

// ResponseHeaders implements [OpenAIChatCompletionTranslator.ResponseHeaders].
func (o *openAIToGCPAnthropicTranslatorV1ChatCompletion) ResponseHeaders(_ map[string]string) (
	newHeaders []internalapi.Header, err error,
) {
	if o.streamParser != nil {
		newHeaders = []internalapi.Header{{contentTypeHeaderName, eventStreamContentType}}
	}
	return
}

// ResponseBody implements [OpenAIChatCompletionTranslator.ResponseBody] for GCP Anthropic.
// GCP Anthropic uses deterministic model mapping without virtualization, where the requested model
// is exactly what gets executed. The response does not contain a model field, so we return
// the request model that was originally sent.
func (o *openAIToGCPAnthropicTranslatorV1ChatCompletion) ResponseBody(_ map[string]string, body io.Reader, endOfStream bool, span tracingapi.ChatCompletionSpan) (
	newHeaders []internalapi.Header, newBody []byte, tokenUsage metrics.TokenUsage, responseModel string, err error,
) {
	// If a stream parser was initialized, this is a streaming request.
	if o.streamParser != nil {
		return o.streamParser.Process(body, endOfStream, span)
	}

	var anthropicResp anthropic.Message
	if err = json.NewDecoder(body).Decode(&anthropicResp); err != nil {
		return nil, nil, tokenUsage, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}

	responseModel = o.requestModel
	if anthropicResp.Model != "" {
		responseModel = anthropicResp.Model
	}

	openAIResp, tokenUsage, err := messageToChatCompletion(&anthropicResp, responseModel)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", err
	}

	// Redact and log response when enabled
	if o.debugLogEnabled && o.enableRedaction && o.logger != nil {
		redactedResp := o.RedactBody(openAIResp)
		if jsonBody, marshalErr := json.Marshal(redactedResp); marshalErr == nil {
			o.logger.Debug("response body processing", slog.Any("response", string(jsonBody)))
		}
	}

	newBody, err = json.Marshal(openAIResp)
	if err != nil {
		return nil, nil, metrics.TokenUsage{}, "", fmt.Errorf("failed to marshal body: %w", err)
	}

	if span != nil {
		span.RecordResponse(openAIResp)
	}
	newHeaders = []internalapi.Header{{contentLengthHeaderName, strconv.Itoa(len(newBody))}}
	return
}
