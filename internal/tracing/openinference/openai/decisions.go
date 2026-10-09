// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	openaischema "github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// DecisionsRecorder records OpenInference attributes for Decisions requests.
type DecisionsRecorder struct {
	tracingapi.NoopChunkRecorder[struct{}]
	traceConfig *openinference.TraceConfig
}

func NewDecisionsRecorder(config *openinference.TraceConfig) tracingapi.DecisionsRecorder {
	if config == nil {
		config = openinference.NewTraceConfigFromEnv()
	}
	return &DecisionsRecorder{traceConfig: config}
}

func (*DecisionsRecorder) StartParams(*openaischema.DecisionRequest, []byte) (string, []trace.SpanStartOption) {
	return "Decisions", []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}
}

func (r *DecisionsRecorder) RecordRequest(span trace.Span, req *openaischema.DecisionRequest, body []byte) {
	attrs := []attribute.KeyValue{
		attribute.String(openinference.LLMSystem, openinference.LLMSystemOpenAI),
		attribute.String(openinference.SpanKind, openinference.SpanKindLLM),
	}
	if req.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, req.Model))
	}
	if r.traceConfig.HideInputs {
		attrs = append(attrs, attribute.String(openinference.InputValue, openinference.RedactedValue))
	} else {
		modifiedBody, err := redactImageFromResponseRequestParameters(
			body, r.traceConfig.HideInputImages, r.traceConfig.Base64ImageMaxLength,
		)
		if err == nil {
			attrs = append(attrs, attribute.String(openinference.InputValue, string(modifiedBody)))
		}
		attrs = append(attrs, attribute.String(openinference.InputMimeType, openinference.MimeTypeJSON))
	}
	span.SetAttributes(attrs...)
}

func (*DecisionsRecorder) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	openinference.RecordResponseError(span, statusCode, string(body))
}

func (r *DecisionsRecorder) RecordResponse(span trace.Span, resp *openaischema.DecisionResponse) {
	body := openinference.RedactedValue
	attrs := []attribute.KeyValue{}
	if resp.Model != "" {
		attrs = append(attrs, attribute.String(openinference.LLMModelName, resp.Model))
	}
	if u := resp.Usage; u != nil {
		if u.InputTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPrompt, int(u.InputTokens)))
		}
		if u.OutputTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletion, int(u.OutputTokens)))
		}
		if u.TotalTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountTotal, int(u.TotalTokens)))
		}
		if u.InputTokensDetails.CachedTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPromptCacheHit, int(u.InputTokensDetails.CachedTokens)))
		}
		if cacheWriteTokens := u.InputTokensDetails.CacheWriteTokensValue(); cacheWriteTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountPromptCacheWrite, int(cacheWriteTokens)))
		}
		if u.OutputTokensDetails.ReasoningTokens > 0 {
			attrs = append(attrs, attribute.Int(openinference.LLMTokenCountCompletionReasoning, int(u.OutputTokensDetails.ReasoningTokens)))
		}
	}
	if !r.traceConfig.HideOutputs {
		if marshaled, err := json.Marshal(resp); err == nil {
			body = string(marshaled)
		}
		attrs = append(attrs, attribute.String(openinference.OutputMimeType, openinference.MimeTypeJSON))
	}
	attrs = append(attrs, attribute.String(openinference.OutputValue, body))
	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
}
