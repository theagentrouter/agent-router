// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/lang"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// Ensure mcpSpan implements [tracingapi.MCPSpan].
var _ tracingapi.MCPSpan = (*mcpSpan)(nil)

// Ensure mcpTracer implements [tracingapi.MCPTracer].
var _ tracingapi.MCPTracer = (*mcpTracer)(nil)

// mcpVocabulary is the set of span names, attribute keys and events that one
// semantic convention emits for MCP requests. The two implementations
// (mcpVocabularyLegacy and mcpVocabularyOTel) are meant to be read side by side:
// everything that differs between conventions lives here, and everything that
// does not - trace context propagation, sampling, custom attribute mappings -
// lives in mcpTracer and is shared.
//
// Every field is non-nil in both vocabularies. A convention that does not record
// a signal supplies a no-op rather than a nil, so mcpSpan never has to branch.
type mcpVocabulary struct {
	// name identifies the convention, matching the semConvs entry it belongs to.
	name string
	// spanName derives the span name from the JSON-RPC method and its params.
	spanName func(method string, p mcp.Params) string
	// requestAttributes returns the attributes recorded when the span starts.
	requestAttributes func(req *jsonrpc.Request, p mcp.Params) []attribute.KeyValue
	// routeToBackend records the backend a request was routed to.
	routeToBackend func(span trace.Span, backend, sessionID string, isNew bool)
	// clientSession records the client-facing MCP session.
	clientSession func(span trace.Span, sessionID string)
	// event records a timestamped event, e.g. the list aggregation timeline.
	event func(span trace.Span, name string)
	// listResult records the size of an aggregated list result.
	listResult func(span trace.Span, result any)
	// toolCallResult records the tools/call result payload.
	toolCallResult func(span trace.Span, resultJSON []byte)
	// requestError records the failure class of a failed request. The span
	// status and the exception event are the same under both conventions, so
	// this covers only the convention-specific attributes.
	requestError func(span trace.Span, errType string, err error)
}

// mcpSpan is an implementation of [tracingapi.MCPSpan].
type mcpSpan struct {
	span  trace.Span
	vocab *mcpVocabulary
}

// RecordRouteToBackend implements [tracingapi.MCPSpan.RecordRouteToBackend].
func (s mcpSpan) RecordRouteToBackend(backend string, sessionID string, isNew bool) {
	s.vocab.routeToBackend(s.span, backend, sessionID, isNew)
}

// RecordClientSession implements [tracingapi.MCPSpan.RecordClientSession].
func (s mcpSpan) RecordClientSession(sessionID string) {
	s.vocab.clientSession(s.span, sessionID)
}

// AddEvent implements [tracingapi.MCPSpan.AddEvent].
func (s mcpSpan) AddEvent(name string) {
	s.vocab.event(s.span, name)
}

// RecordListResult implements [tracingapi.MCPSpan.RecordListResult].
func (s mcpSpan) RecordListResult(result any) {
	s.vocab.listResult(s.span, result)
}

// RecordToolCallResult implements [tracingapi.MCPSpan.RecordToolCallResult].
func (s mcpSpan) RecordToolCallResult(resultJSON []byte) {
	s.vocab.toolCallResult(s.span, resultJSON)
}

// EndSpanOnError implements [tracingapi.MCPSpan.EndSpanOnError].
func (s mcpSpan) EndSpanOnError(errType string, err error) {
	s.vocab.requestError(s.span, errType, err)
	s.span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", errType),
		attribute.String("exception.message", err.Error()),
	))
	s.span.SetStatus(codes.Error, err.Error())
	s.span.End()
}

// EndSpan implements [tracingapi.MCPSpan.EndSpan].
func (s mcpSpan) EndSpan() {
	s.span.SetStatus(codes.Ok, "")
	s.span.End()
}

// mcpTracer is an implementation of [tracingapi.MCPTracer].
type mcpTracer struct {
	tracer            trace.Tracer
	propagator        propagation.TextMapPropagator
	attributeMappings map[string]string
	vocab             *mcpVocabulary
}

func newMCPTracer(
	tracer trace.Tracer,
	propagator propagation.TextMapPropagator,
	attributeMappings map[string]string,
	vocab *mcpVocabulary,
) tracingapi.MCPTracer {
	return mcpTracer{
		tracer:            tracer,
		propagator:        propagator,
		attributeMappings: attributeMappings,
		vocab:             vocab,
	}
}

// StartSpanAndInjectMeta implements [tracingapi.MCPTracer.StartSpanAndInjectMeta].
func (m mcpTracer) StartSpanAndInjectMeta(ctx context.Context, req *jsonrpc.Request, param mcp.Params, headers http.Header) tracingapi.MCPSpan {
	attrs := m.vocab.requestAttributes(req, param)

	for srcName, targetName := range m.attributeMappings {
		// Check if the attribute is present in the metadata first, as this is the common place to add custom attributes
		// in MCP requests. Fall back to headers if not found in metadata.
		// If the attribute is not found there, check if there is any custom header to map.
		if metaValue := lang.CaseInsensitiveValue(param.GetMeta(), srcName); metaValue != "" {
			attrs = append(attrs, attribute.String(targetName, metaValue))
		} else if headerValue := headers.Get(srcName); headerValue != "" { // this is case-insensitive
			attrs = append(attrs, attribute.String(targetName, headerValue))
		}
	}

	// Extract trace context: headers first, then _meta on top. Extract returns the context it was
	// given when a carrier holds none, so either source alone works.
	mutableMeta := param.GetMeta()
	if mutableMeta == nil {
		mutableMeta = make(map[string]any)
	}
	mc := metaMapCarrier{
		m: mutableMeta,
	}
	parentCtx := m.propagator.Extract(ctx, propagation.HeaderCarrier(headers))
	parentCtx = m.propagator.Extract(parentCtx, mc)

	// Start the span with options appropriate for the semantic convention.
	spanName := m.vocab.spanName(req.Method, param)
	newCtx, span := m.tracer.Start(parentCtx, spanName, trace.WithSpanKind(trace.SpanKindClient))

	// Always inject trace context into the header mutation if provided.
	// This ensures trace propagation works even for unsampled spans.
	m.propagator.Inject(newCtx, mc)
	param.SetMeta(mc.m)

	// Only record request attributes if span is recording (sampled).
	if span.IsRecording() {
		span.SetAttributes(attrs...)
		return &mcpSpan{span: span, vocab: m.vocab}
	}

	return nil
}

// Ensure metaMapCarrier implements the [propagation.TextMapCarrier] interface.
var _ propagation.TextMapCarrier = metaMapCarrier{}

// metaMapCarrier adapts a map[string]any to implement the TextMapCarrier interface.
type metaMapCarrier struct {
	m map[string]any
}

// Get implements [propagation.TextMapCarrier.Get].
func (c metaMapCarrier) Get(key string) string {
	return fmt.Sprintf("%v", c.m[key])
}

// Set implements [propagation.TextMapCarrier.Set].
func (c metaMapCarrier) Set(key string, value string) {
	c.m[key] = value
}

// Keys implements [propagation.TextMapCarrier.Keys].
func (c metaMapCarrier) Keys() []string {
	keys := make([]string, 0, len(c.m))
	for k := range c.m {
		keys = append(keys, k)
	}

	return keys
}

// The noop* functions below are the "this convention does not record that
// signal" implementations of the mcpVocabulary function fields.

func noopSpanSession(trace.Span, string)      {}
func noopSpanEvent(trace.Span, string)        {}
func noopSpanListResult(trace.Span, any)      {}
func noopSpanPayload(trace.Span, []byte)      {}
func noopSpanError(trace.Span, string, error) {}
