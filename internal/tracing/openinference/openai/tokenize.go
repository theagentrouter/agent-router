// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai/tokenize"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// startOptsTokenize sets trace.SpanKindInternal as that's the span kind used in
// OpenInference.
var startOptsTokenize = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}

// TokenizeRecorder implements recorders for OpenInference tokenize spans.
type TokenizeRecorder struct {
	traceConfig                            *openinference.TraceConfig
	tracingapi.NoopChunkRecorder[struct{}] // Tokenize operations don't have streaming chunks
}

// NewTokenizeRecorderFromEnv creates an api.TokenizeRecorder
// from environment variables using the OpenInference configuration specification.
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewTokenizeRecorderFromEnv() tracingapi.TokenizeRecorder {
	return NewTokenizeRecorder(nil)
}

// NewTokenizeRecorder creates a tracingapi.TokenizeRecorder with the
// given config using the OpenInference configuration specification.
//
// Parameters:
//   - config: configuration for redaction. Defaults to NewTraceConfigFromEnv().
//
// See: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
func NewTokenizeRecorder(config *openinference.TraceConfig) tracingapi.TokenizeRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &TokenizeRecorder{traceConfig: config}
}

// StartParams implements the same method as defined in tracingapi.TokenizeRecorder.
func (r *TokenizeRecorder) StartParams(*tokenize.RequestUnion, []byte) (spanName string, opts []trace.SpanStartOption) {
	return "Tokenize", startOptsTokenize
}

// RecordRequest implements the same method as defined in tracingapi.TokenizeRecorder.
func (r *TokenizeRecorder) RecordRequest(span trace.Span, tokenizeReq *tokenize.RequestUnion, body []byte) {
	span.SetAttributes(buildTokenizeRequestAttributes(tokenizeReq, string(body), r.traceConfig)...)
}

// RecordResponseOnError implements the same method as defined in tracingapi.TokenizeRecorder.
func (r *TokenizeRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

// RecordResponse implements the same method as defined in tracingapi.TokenizeRecorder.
func (r *TokenizeRecorder) RecordResponse(span trace.Span, resp *tokenize.Response) {
	// Set output attributes.
	var attrs []attribute.KeyValue
	attrs = buildTokenizeResponseAttributes(resp, r.traceConfig)

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

// buildTokenizeRequestAttributes builds OpenInference attributes from a tokenize request.
func buildTokenizeRequestAttributes(req *tokenize.RequestUnion, body string, config *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	// Set span kind to token counter since tokenization is a token counting operation
	attrs = append(attrs,
		attribute.String(openinference.SpanKind, openinference.SpanKindTokenCounter),
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
	)

	// Extract model name from the union
	var model string
	if req.CompletionRequest != nil {
		model = req.CompletionRequest.Model
	} else if req.ChatRequest != nil {
		model = req.ChatRequest.Model
	}
	if model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, model))
	}

	// Add input value if not hidden
	if !config.HideInputs {
		attrs = append(attrs,
			attribute.String(openinference.InputValue, body),
			attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON),
		)
	}

	// Add tokenization-specific attributes
	if req.CompletionRequest != nil {
		addSpecialTokens := true
		if req.CompletionRequest.AddSpecialTokens != nil {
			addSpecialTokens = *req.CompletionRequest.AddSpecialTokens
		}
		attrs = append(attrs,
			attribute.String("tokenize.request_type", "completion"),
			attribute.Bool("tokenize.add_special_tokens", addSpecialTokens),
		)
		if req.CompletionRequest.ReturnTokenStrs != nil {
			attrs = append(attrs, attribute.Bool("tokenize.return_token_strs", *req.CompletionRequest.ReturnTokenStrs))
		}
	} else if req.ChatRequest != nil {
		addGenerationPrompt := true
		if req.AddGenerationPrompt != nil {
			addGenerationPrompt = *req.AddGenerationPrompt
		}
		attrs = append(attrs,
			attribute.String("tokenize.request_type", "chat"),
			attribute.Bool("tokenize.add_generation_prompt", addGenerationPrompt),
			attribute.Bool("tokenize.continue_final_message", req.ContinueFinalMessage),
			attribute.Bool("tokenize.add_special_tokens", req.ChatRequest.AddSpecialTokens),
		)
		if req.ChatRequest.ReturnTokenStrs != nil {
			attrs = append(attrs, attribute.Bool("tokenize.return_token_strs", *req.ChatRequest.ReturnTokenStrs))
		}
		if len(req.Messages) > 0 {
			attrs = append(attrs, attribute.Int("tokenize.message_count", len(req.Messages)))
		}
	}

	return attrs
}

// buildTokenizeResponseAttributes builds OpenInference attributes from a tokenize response.
func buildTokenizeResponseAttributes(resp *tokenize.Response, _ *openinference.TraceConfig) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	// Add tokenization results
	attrs = append(attrs, attribute.Int("tokenize.token_count", resp.Count))
	if resp.MaxModelLen > 0 {
		attrs = append(attrs, attribute.Int("tokenize.max_model_len", resp.MaxModelLen))
	}
	if len(resp.Tokens) > 0 {
		attrs = append(attrs, attribute.Int("tokenize.tokens_returned", len(resp.Tokens)))
	}
	if len(resp.TokenStrs) > 0 {
		attrs = append(attrs, attribute.Int("tokenize.token_strings_returned", len(resp.TokenStrs)))
	}

	// Output MIME type
	attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))

	return attrs
}
