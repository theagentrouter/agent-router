// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package anthropic

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// MessageRecorder implements recorders for OpenInference chat completion spans.
type MessageRecorder struct {
	traceConfig *openinference.TraceConfig
}

// NewMessageRecorderFromEnv creates an tracingapi.MessageRecorder
// from environment variables using the OpenInference configuration specification.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewMessageRecorderFromEnv() tracingapi.MessageRecorder {
	return NewMessageRecorder(nil)
}

// NewMessageRecorder creates a tracingapi.MessageRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewMessageRecorder(config *openinference.TraceConfig) tracingapi.MessageRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &MessageRecorder{traceConfig: config}
}

// startOpts sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var startOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// StartParams implements the same method as defined in tracingapi.MessageRecorder.
func (r *MessageRecorder) StartParams(*anthropic.MessagesRequest, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Message", startOpts
}

// RecordRequest implements the same method as defined in tracingapi.MessageRecorder.
func (r *MessageRecorder) RecordRequest(span trace.Span, chatReq *anthropic.MessagesRequest, body []byte) {
	span.SetAttributes(buildRequestAttributes(chatReq, string(body), r.traceConfig)...)
}

// RecordResponseChunks implements the same method as defined in tracingapi.MessageRecorder.
func (r *MessageRecorder) RecordResponseChunks(span trace.Span, chunks []*anthropic.MessagesStreamChunk) {
	if len(chunks) > 0 {
		span.AddEvent("First Token Stream Event")
	}
	converted := convertSSEToResponse(chunks)
	r.RecordResponse(span, converted)
}

// RecordResponseOnError implements the same method as defined in tracingapi.MessageRecorder.
func (r *MessageRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.MessageRecorder.
func (r *MessageRecorder) RecordResponse(span trace.Span, resp *anthropic.MessagesResponse) {
	// Set output attributes.
	var attrs []attribute.KeyValue
	attrs = buildResponseAttributes(resp, r.traceConfig)

	bodyString := openinference.RedactedValue
	if !r.traceConfig.HideOutputs {
		marshaled, err := json.Marshal(resp)
		if err == nil {
			bodyString = string(marshaled)
		}
	}
	attrs = append(attrs, attribute.String(openinference.OutputValue, bodyString))
	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}

// llmInvocationParameters is the representation of LLMInvocationParameters,
// which includes all parameters except messages and tools, which have their
// own attributes.
// See: openinference-instrumentation-openai _request_attributes_extractor.py.
type llmInvocationParameters struct {
	anthropic.MessagesRequest
	Messages []anthropic.MessageParam `json:"messages,omitempty"`
	Tools    []anthropic.Tool         `json:"tools,omitempty"`
}

// buildRequestAttributes builds OpenInference attributes from the request.
func buildRequestAttributes(req *anthropic.MessagesRequest, body string, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemAnthropic),
		attribute.String(openinference.LLMModelName, req.Model),
	}

	if config.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, body),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	if !config.HideLLMInvocationParameters {
		if invocationParamsJSON, err := json.Marshal(llmInvocationParameters{
			MessagesRequest: *req,
		}); err == nil {
			attrs = append(attrs, attribute.String(openinference.LLMInvocationParameters, string(invocationParamsJSON)))
		}
	}

	if !config.HideInputs && !config.HideInputMessages {
		for i, msg := range req.Messages {
			role := msg.Role
			attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageRole), string(role)))
			switch content := msg.Content; {
			case content.Text != "":
				maybeRedacted := content.Text
				if config.HideInputText {
					maybeRedacted = openinference.RedactedValue
				}
				attrs = append(attrs, attribute.String(openinference.InputMessageAttribute(i, openinference.MessageContent), maybeRedacted))
			case content.Array != nil:
				for j, param := range content.Array {
					switch {
					case param.Text != nil:
						maybeRedacted := param.Text.Text
						if config.HideInputText {
							maybeRedacted = openinference.RedactedValue
						}
						attrs = append(attrs,
							attribute.String(openinference.InputMessageContentAttribute(i, j, "text"), maybeRedacted),
							attribute.String(openinference.InputMessageContentAttribute(i, j, "type"), "text"),
						)
					default:
						// TODO: support for other content types.
					}
				}
			}
		}
	}

	// Add indexed attributes for each tool.
	for i, tool := range req.Tools {
		if toolJSON, err := json.Marshal(tool); err == nil {
			attrs = append(attrs,
				attribute.String(fmt.Sprintf("%s.%d.tool.json_schema", openinference.LLMTools, i), string(toolJSON)),
			)
		}
	}
	return attrs
}

func buildResponseAttributes(resp *anthropic.MessagesResponse, config *openinference.TraceConfig) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMModelName, resp.Model),
	}

	if !config.HideOutputs {
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
	}

	// Note: compound match here is from Python OpenInference OpenAI config.py.
	role := resp.Role
	if !config.HideOutputs && !config.HideOutputMessages {
		for i, content := range resp.Content {
			attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(i, openinference.MessageRole), string(role)))

			switch {
			case content.Text != nil:
				txt := content.Text.Text
				if config.HideOutputText {
					txt = openinference.RedactedValue
				}
				attrs = append(attrs, attribute.String(openinference.OutputMessageAttribute(i, openinference.MessageContent), txt))
			case content.Tool != nil:
				tool := content.Tool
				attrs = append(attrs,
					attribute.String(openinference.OutputMessageToolCallAttribute(i, 0, openinference.ToolCallID), tool.ID),
					attribute.String(openinference.OutputMessageToolCallAttribute(i, 0, openinference.ToolCallFunctionName), tool.Name),
				)
				inputStr, err := json.Marshal(tool.Input)
				if err == nil {
					attrs = append(attrs,
						attribute.String(openinference.OutputMessageToolCallAttribute(i, 0, openinference.ToolCallFunctionArguments), string(inputStr)),
					)
				}
			}
		}
	}

	// Token counts are considered metadata and are still included even when output content is hidden.
	u := resp.Usage
	cacheReadTokens := int64(u.CacheReadInputTokens)
	cacheCreationTokens := int64(u.CacheCreationInputTokens)
	cost := metrics.ExtractTokenUsageFromExplicitCaching(
		int64(u.InputTokens),
		int64(u.OutputTokens),
		&cacheReadTokens,
		&cacheCreationTokens,
	)
	input, _ := cost.InputTokens()
	cacheRead, _ := cost.CachedInputTokens()
	cacheCreation, _ := cost.CacheCreationInputTokens()
	output, _ := cost.OutputTokens()
	total, _ := cost.TotalTokens()

	attrs = append(attrs,
		attribute.Int(openinference.LLMTokenCountPrompt, int(input)),
		attribute.Int(openinference.LLMTokenCountPromptCacheHit, int(cacheRead)),
		attribute.Int(openinference.LLMTokenCountPromptCacheWrite, int(cacheCreation)),
		attribute.Int(openinference.LLMTokenCountCompletion, int(output)),
		attribute.Int(openinference.LLMTokenCountTotal, int(total)),
	)
	return attrs
}

// convertSSEToResponse folds streaming chunks back into the response the
// provider sent, so streaming and unary produce identical attributes.
func convertSSEToResponse(chunks []*anthropic.MessagesStreamChunk) *anthropic.MessagesResponse {
	return anthropic.MessagesResponseFromStream(chunks)
}
