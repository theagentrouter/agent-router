// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// The GenAI conventions specify CLIENT for inference spans. This gateway is a
// client of the upstream provider, which is also how it reports metrics.
// See: internal/metrics/genai.go
var startOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindClient)}

// recorder implements every endpoint's span recorder. The endpoints differ only
// in how the model is read from the request and which attributes the response
// contributes, so those are the only two things a constructor supplies. Keeping
// one implementation means span name, span kind, error handling and status are
// provably identical across endpoints.
type recorder[ReqT, RespT, ChunkT any] struct {
	operation Operation
	config    *Config

	// Returns the empty string when the endpoint has no model.
	requestModel func(*ReqT) string
	// The three below are nil when the endpoint carries nothing the conventions
	// describe, or in chunkAttrs' case does not stream.
	requestAttrs  func(*ReqT) []attribute.KeyValue
	responseAttrs func(*RespT) []attribute.KeyValue
	chunkAttrs    func([]*ChunkT) []attribute.KeyValue

	// inputMessages and outputMessages contribute captured message content.
	// They are only consulted when Config.CaptureMessageContent is set, and are
	// nil for endpoints that carry no messages.
	inputMessages  func(*ReqT) []message
	outputMessages func(*RespT) []message
	// systemInstructions contributes gen_ai.system_instructions. It is only set
	// for APIs that model the system prompt as a field separate from the
	// conversation, such as Anthropic messages.
	systemInstructions func(*ReqT) []messagePart
	// toolDefinitions contributes gen_ai.tool.definitions. Nil for endpoints
	// that do not accept tools.
	toolDefinitions func(*ReqT) []toolDefinition
	// conversationID reads the conversation this request belongs to, for APIs
	// that track one. It returns the empty string when there is none.
	conversationID func(*ReqT) string
	// chunkMessages contributes captured output content on the streaming path.
	// It exists separately from outputMessages because chunks must be folded
	// before they can be mapped.
	chunkMessages func([]*ChunkT) []message
	// foldChunks reconstructs the response from streaming chunks. When set, the
	// streaming path reuses responseAttrs and outputMessages, which makes
	// streaming and unary responses identical by construction rather than by
	// two implementations agreeing. It takes precedence over chunkAttrs.
	foldChunks func([]*ChunkT) *RespT
}

// StartParams implements tracingapi.SpanRecorder.
//
// The conventions name inference spans "{operation} {model}". The model is
// omitted when unknown rather than emitting a trailing space.
func (r *recorder[ReqT, RespT, ChunkT]) StartParams(req *ReqT, _ []byte) (string, []trace.SpanStartOption) {
	name := string(r.operation)
	if model := r.requestModel(req); model != "" {
		name += " " + model
	}
	return name, startOpts
}

// RecordRequest implements tracingapi.SpanRecorder.
func (r *recorder[ReqT, RespT, ChunkT]) RecordRequest(span trace.Span, req *ReqT, _ []byte) {
	attrs := []attribute.KeyValue{
		attribute.String(OperationName, string(r.operation)),
	}
	if model := r.requestModel(req); model != "" {
		attrs = append(attrs, attribute.String(RequestModel, model))
	}
	// Sampling parameters are metadata, not content, so they are recorded
	// regardless of the content capture setting.
	if r.requestAttrs != nil {
		attrs = append(attrs, r.requestAttrs(req)...)
	}
	// The conversation id is an identifier, not content, so it is not gated.
	if r.conversationID != nil {
		if id := r.conversationID(req); id != "" {
			attrs = append(attrs, attribute.String(ConversationID, id))
		}
	}
	if r.config.CaptureMessageContent {
		if r.inputMessages != nil {
			attrs = append(attrs, messagesAttr(InputMessages, r.inputMessages(req))...)
		}
		if r.systemInstructions != nil {
			attrs = append(attrs, partsAttr(SystemInstructions, r.systemInstructions(req))...)
		}
		if r.toolDefinitions != nil {
			attrs = append(attrs, toolDefinitionsAttr(r.toolDefinitions(req))...)
		}
	}
	span.SetAttributes(attrs...)
}

// RecordResponse implements tracingapi.SpanResponseRecorder.
func (r *recorder[ReqT, RespT, ChunkT]) RecordResponse(span trace.Span, resp *RespT) {
	var attrs []attribute.KeyValue
	if r.responseAttrs != nil {
		attrs = r.responseAttrs(resp)
	}
	if r.config.CaptureMessageContent && r.outputMessages != nil {
		attrs = append(attrs, messagesAttr(OutputMessages, r.outputMessages(resp))...)
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	span.SetStatus(codes.Ok, "")
}

// RecordResponseChunks implements tracingapi.SpanResponseRecorder.
func (r *recorder[ReqT, RespT, ChunkT]) RecordResponseChunks(span trace.Span, chunks []*ChunkT) {
	// Endpoints whose chunks reconstruct the response delegate to the unary
	// path, so there is only one mapping to keep correct.
	if r.foldChunks != nil {
		if len(chunks) == 0 {
			span.SetStatus(codes.Ok, "")
			return
		}
		r.RecordResponse(span, r.foldChunks(chunks))
		return
	}

	var attrs []attribute.KeyValue
	if r.chunkAttrs != nil {
		attrs = r.chunkAttrs(chunks)
	}
	if r.config.CaptureMessageContent && r.chunkMessages != nil {
		attrs = append(attrs, messagesAttr(OutputMessages, r.chunkMessages(chunks))...)
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	span.SetStatus(codes.Ok, "")
}

// RecordResponseOnError implements tracingapi.SpanResponseRecorder.
func (r *recorder[ReqT, RespT, ChunkT]) RecordResponseOnError(span trace.Span, statusCode int, body []byte) {
	RecordResponseError(span, r.config, statusCode, string(body))
}

// usageAttrs builds the token usage attributes, omitting counts that are absent.
// The conventions treat usage as metadata, so it is recorded regardless of
// whether message content capture is enabled.
//
// There is deliberately no total: the conventions omit it because it is
// derivable, unlike OpenInference's llm.token_count.total.
func usageAttrs(inputTokens, outputTokens int) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if inputTokens > 0 {
		attrs = append(attrs, attribute.Int(UsageInputTokens, inputTokens))
	}
	if outputTokens > 0 {
		attrs = append(attrs, attribute.Int(UsageOutputTokens, outputTokens))
	}
	return attrs
}

// usageDetailAttrs builds the cache and reasoning token breakdowns.
//
// The conventions define no equivalent of OpenInference's audio token counts,
// so those are omitted rather than emitted as custom attributes.
func usageDetailAttrs(cacheRead, cacheCreation, reasoning int) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if cacheRead > 0 {
		attrs = append(attrs, attribute.Int(UsageCacheReadInputTokens, cacheRead))
	}
	if cacheCreation > 0 {
		attrs = append(attrs, attribute.Int(UsageCacheCreationInputTokens, cacheCreation))
	}
	if reasoning > 0 {
		attrs = append(attrs, attribute.Int(UsageReasoningOutputTokens, reasoning))
	}
	return attrs
}
