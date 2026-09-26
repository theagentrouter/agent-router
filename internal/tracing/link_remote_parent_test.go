// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"fmt"
	"io"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

const (
	testCallerTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	testCallerSpanID      = "00f067aa0ba902b7"
	testCallerTraceparent = "00-" + testCallerTraceID + "-" + testCallerSpanID + "-01"
)

func otlpStringAttrs(s *tracev1.Span) map[string]string {
	attrs := make(map[string]string, len(s.Attributes))
	for _, kv := range s.Attributes {
		attrs[kv.Key] = kv.Value.GetStringValue()
	}
	return attrs
}

// TestNewTracingFromEnv_RemoteParentAsLink verifies that when
// AI_GATEWAY_TRACING_REMOTE_PARENT_AS_LINK=true, a request carrying a caller
// traceparent produces a span that is the ROOT of a NEW trace, recording the
// caller's context as a span link (plus caller.trace_id/caller.span_id
// attributes for backends that drop links), instead of joining the caller's
// trace as a child span.
func TestNewTracingFromEnv_RemoteParentAsLink(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("AI_GATEWAY_TRACING_REMOTE_PARENT_AS_LINK", "true")
	collector, tracing := newTracingFromEnvForTest(t, io.Discard)

	carrier := propagation.MapCarrier{}
	span := tracing.ChatCompletionTracer().StartSpanAndInjectHeaders(
		t.Context(),
		map[string]string{"traceparent": testCallerTraceparent},
		carrier,
		&openai.ChatCompletionRequest{Model: openai.ModelGPT5Nano},
		nil,
	)
	require.NotNil(t, span)
	span.EndSpan()

	v1Span := collector.TakeSpan()
	require.NotNil(t, v1Span)

	// The span starts its own trace, as a root span.
	traceID := fmt.Sprintf("%032x", v1Span.TraceId)
	require.NotEqual(t, testCallerTraceID, traceID)
	require.Empty(t, v1Span.ParentSpanId)

	// The caller's context is preserved as a span link.
	require.Len(t, v1Span.Links, 1)
	require.Equal(t, testCallerTraceID, fmt.Sprintf("%032x", v1Span.Links[0].TraceId))
	require.Equal(t, testCallerSpanID, fmt.Sprintf("%016x", v1Span.Links[0].SpanId))

	// ... and as plain attributes, for trace backends that drop links.
	attrs := otlpStringAttrs(v1Span)
	require.Equal(t, testCallerTraceID, attrs["caller.trace_id"])
	require.Equal(t, testCallerSpanID, attrs["caller.span_id"])

	// The upstream request joins the NEW (gateway) trace, not the caller's.
	spanID := fmt.Sprintf("%016x", v1Span.SpanId)
	require.Equal(t, propagation.MapCarrier{
		"traceparent": fmt.Sprintf("00-%s-%s-01", traceID, spanID),
	}, carrier)
}

// TestNewTracingFromEnv_RemoteParentAsLink_DefaultOff pins the default:
// without the env var, the caller's traceparent is continued as the span's
// parent (upstream behavior).
func TestNewTracingFromEnv_RemoteParentAsLink_DefaultOff(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	// ClearTestEnv doesn't cover this var; pin it so an ambient =true in the
	// invoking shell/CI can't flip the default under test.
	t.Setenv("AI_GATEWAY_TRACING_REMOTE_PARENT_AS_LINK", "")
	collector, tracing := newTracingFromEnvForTest(t, io.Discard)

	span := tracing.ChatCompletionTracer().StartSpanAndInjectHeaders(
		t.Context(),
		map[string]string{"traceparent": testCallerTraceparent},
		propagation.MapCarrier{},
		&openai.ChatCompletionRequest{Model: openai.ModelGPT5Nano},
		nil,
	)
	require.NotNil(t, span)
	span.EndSpan()

	v1Span := collector.TakeSpan()
	require.NotNil(t, v1Span)
	require.Equal(t, testCallerTraceID, fmt.Sprintf("%032x", v1Span.TraceId))
	require.Equal(t, testCallerSpanID, fmt.Sprintf("%016x", v1Span.ParentSpanId))
	require.Empty(t, v1Span.Links)
	attrs := otlpStringAttrs(v1Span)
	require.NotContains(t, attrs, "caller.trace_id")
	require.NotContains(t, attrs, "caller.span_id")
}

// TestNewTracingFromEnv_RemoteParentAsLink_NoCallerContext verifies the
// enabled mode is inert when the caller propagates no trace context: a plain
// root span with no links and no caller.* attributes.
func TestNewTracingFromEnv_RemoteParentAsLink_NoCallerContext(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("AI_GATEWAY_TRACING_REMOTE_PARENT_AS_LINK", "true")
	collector, tracing := newTracingFromEnvForTest(t, io.Discard)

	span := tracing.ChatCompletionTracer().StartSpanAndInjectHeaders(
		t.Context(),
		map[string]string{},
		propagation.MapCarrier{},
		&openai.ChatCompletionRequest{Model: openai.ModelGPT5Nano},
		nil,
	)
	require.NotNil(t, span)
	span.EndSpan()

	v1Span := collector.TakeSpan()
	require.NotNil(t, v1Span)
	require.Empty(t, v1Span.ParentSpanId)
	require.Empty(t, v1Span.Links)
	attrs := otlpStringAttrs(v1Span)
	require.NotContains(t, attrs, "caller.trace_id")
	require.NotContains(t, attrs, "caller.span_id")
}

// TestNewTracingFromEnv_RemoteParentAsLink_MCP verifies the MCP tracer (a
// separate extraction path over JSON-RPC _meta) gets the same treatment.
func TestNewTracingFromEnv_RemoteParentAsLink_MCP(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("AI_GATEWAY_TRACING_REMOTE_PARENT_AS_LINK", "true")
	collector, tracing := newTracingFromEnvForTest(t, io.Discard)

	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)
	r := &jsonrpc.Request{ID: reqID, Method: "initialize"}
	p := &mcp.InitializeParams{Meta: map[string]any{"traceparent": testCallerTraceparent}}
	span := tracing.MCPTracer().StartSpanAndInjectMeta(t.Context(), r, p, nil)
	require.NotNil(t, span)
	span.EndSpan()

	v1Span := collector.TakeSpan()
	require.NotNil(t, v1Span)

	traceID := fmt.Sprintf("%032x", v1Span.TraceId)
	require.NotEqual(t, testCallerTraceID, traceID)
	require.Empty(t, v1Span.ParentSpanId)
	require.Len(t, v1Span.Links, 1)
	require.Equal(t, testCallerTraceID, fmt.Sprintf("%032x", v1Span.Links[0].TraceId))

	// The injected _meta traceparent carries the NEW (gateway) trace.
	meta := p.GetMeta()
	require.Contains(t, meta["traceparent"], traceID)
}
