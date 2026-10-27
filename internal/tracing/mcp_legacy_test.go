// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// The tests in this file pin the legacy MCP vocabulary, which is what the
// gateway emitted before the OTel MCP conventions were added and what it still
// emits by default. Every expectation here is the observable behavior users
// already have; a change that makes one of these fail is a breaking change to
// somebody's dashboard, not a test that needs updating.

func newTestLegacyMCPSpan(t *testing.T, method string, params mcp.Params) (tracingapi.MCPSpan, func() tracetest.SpanStub) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
	tracer := newMCPTracer(tp.Tracer("test"), autoprop.NewTextMapPropagator(), nil, mcpVocabularyLegacy())

	reqID, _ := jsonrpc.MakeID("test-id")
	req := &jsonrpc.Request{ID: reqID, Method: method}
	span := tracer.StartSpanAndInjectMeta(context.Background(), req, params, nil)
	require.NotNil(t, span)

	return span, func() tracetest.SpanStub {
		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		return spans[0]
	}
}

func TestLegacyMCP_StaticAttributes(t *testing.T) {
	span, exported := newTestLegacyMCPSpan(t, "tools/list", &mcp.ListToolsParams{})
	span.EndSpan()

	attrs := exported().Attributes
	require.Contains(t, attrs, attribute.String("mcp.method.name", "tools/list"))
	require.Contains(t, attrs, attribute.String("mcp.protocol.version", "2025-06-18"))
	require.Contains(t, attrs, attribute.String("mcp.transport", "http"))
	require.Contains(t, attrs, attribute.String("mcp.request.id", "{test-id}"))

	// None of the OTel vocabulary leaks into the default.
	for _, a := range attrs {
		require.NotContains(t, string(a.Key), "gen_ai.")
		require.NotEqual(t, "jsonrpc.request.id", string(a.Key))
		require.NotEqual(t, "network.transport", string(a.Key))
		require.NotEqual(t, "network.protocol.name", string(a.Key))
		require.NotEqual(t, "network.protocol.version", string(a.Key))
	}
}

func TestLegacyMCP_ParamsAttributes(t *testing.T) {
	cases := []struct {
		name     string
		p        mcp.Params
		expected []attribute.KeyValue
	}{
		{name: "initialize without client info", p: &mcp.InitializeParams{}},
		{
			name: "initialize with client info",
			p: &mcp.InitializeParams{ClientInfo: &mcp.Implementation{
				Name: "claude", Title: "Claude", Version: "1.0.0",
			}},
			expected: []attribute.KeyValue{
				attribute.String("mcp.client.name", "claude"),
				attribute.String("mcp.client.title", "Claude"),
				attribute.String("mcp.client.version", "1.0.0"),
			},
		},
		{name: "tools/list", p: &mcp.ListToolsParams{}},
		{
			name:     "tools/call keeps mcp.tool.name",
			p:        &mcp.CallToolParams{Name: "fake-tool"},
			expected: []attribute.KeyValue{attribute.String("mcp.tool.name", "fake-tool")},
		},
		{
			name:     "prompts/get keeps mcp.prompt.name",
			p:        &mcp.GetPromptParams{Name: "fake-prompt"},
			expected: []attribute.KeyValue{attribute.String("mcp.prompt.name", "fake-prompt")},
		},
		{
			name:     "logging/setLevel",
			p:        &mcp.SetLoggingLevelParams{Level: "info"},
			expected: []attribute.KeyValue{attribute.String("mcp.logging.level", "info")},
		},
		{name: "resources/list", p: &mcp.ListResourcesParams{}},
		{
			name:     "resources/read",
			p:        &mcp.ReadResourceParams{URI: "fake-uri"},
			expected: []attribute.KeyValue{attribute.String("mcp.resource.uri", "fake-uri")},
		},
		{
			name:     "resources/subscribe",
			p:        &mcp.SubscribeParams{URI: "fake-uri"},
			expected: []attribute.KeyValue{attribute.String("mcp.resource.uri", "fake-uri")},
		},
		{
			name:     "resources/unsubscribe",
			p:        &mcp.UnsubscribeParams{URI: "fake-uri"},
			expected: []attribute.KeyValue{attribute.String("mcp.resource.uri", "fake-uri")},
		},
		{
			name: "notifications/progress",
			p: &mcp.ProgressNotificationParams{
				Message: "fake-message", Progress: 100, ProgressToken: "fake-token",
			},
			expected: []attribute.KeyValue{
				attribute.Float64("mcp.notifications.progress", 100),
				attribute.String("mcp.notifications.progress.token", "fake-token"),
				attribute.String("mcp.notifications.progress.message", "fake-message"),
			},
		},
		{
			name: "completion/complete",
			p: &mcp.CompleteParams{Argument: mcp.CompleteParamsArgument{
				Name: "fake-name", Value: "fake-value",
			}},
			expected: []attribute.KeyValue{
				attribute.String("mcp.complete.argument.name", "fake-name"),
				attribute.String("mcp.complete.argument.value", "fake-value"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, legacyMCPParamsAttributes(tc.p))
		})
	}
}

func TestLegacyMCP_SpanName(t *testing.T) {
	// The PascalCase names follow the mcp-go SDK patterns and are what users'
	// span-name queries match on today.
	cases := map[string]string{
		"initialize":               "Initialize",
		"tools/list":               "ListTools",
		"tools/call":               "CallTool",
		"prompts/list":             "ListPrompts",
		"prompts/get":              "GetPrompt",
		"resources/list":           "ListResources",
		"resources/read":           "ReadResource",
		"resources/subscribe":      "Subscribe",
		"resources/unsubscribe":    "Unsubscribe",
		"resources/templates/list": "ListResourceTemplates",
		"logging/setLevel":         "SetLoggingLevel",
		"completion/complete":      "Complete",
		"ping":                     "Ping",
		"unknown/method":           "unknown/method",
	}

	for method, expected := range cases {
		t.Run(method, func(t *testing.T) {
			require.Equal(t, expected, legacyMCPSpanName(method, nil))
		})
	}
}

// TestLegacyMCP_SpanNameIgnoresTarget pins that the legacy names do not gain the
// tool or prompt suffix the OTel vocabulary appends.
func TestLegacyMCP_SpanNameIgnoresTarget(t *testing.T) {
	require.Equal(t, "CallTool", legacyMCPSpanName("tools/call", &mcp.CallToolParams{Name: "fake-tool"}))
	require.Equal(t, "GetPrompt", legacyMCPSpanName("prompts/get", &mcp.GetPromptParams{Name: "fake-prompt"}))
}

func TestLegacyMCP_RecordRouteToBackend(t *testing.T) {
	span, exported := newTestLegacyMCPSpan(t, "tools/call", &mcp.CallToolParams{Name: "fake-tool"})
	span.RecordRouteToBackend("backend-a", "sess-1234", true)
	span.EndSpan()

	stub := exported()
	require.Len(t, stub.Events, 1)
	require.Equal(t, "route to backend", stub.Events[0].Name)
	require.Contains(t, stub.Events[0].Attributes, attribute.String("mcp.backend.name", "backend-a"))
	require.Contains(t, stub.Events[0].Attributes, attribute.String("mcp.session.id", "sess-1234"))
	require.Contains(t, stub.Events[0].Attributes, attribute.Bool("mcp.session.new", true))
}

func TestLegacyMCP_EndSpanOnError(t *testing.T) {
	span, exported := newTestLegacyMCPSpan(t, "tools/call", &mcp.CallToolParams{Name: "fake-tool"})
	span.EndSpanOnError("invalid_param", &jsonrpc.Error{Code: -32602, Message: "invalid params"})

	stub := exported()
	require.Equal(t, codes.Error, stub.Status.Code)
	require.Len(t, stub.Events, 1)
	require.Equal(t, "exception", stub.Events[0].Name)
	require.Contains(t, stub.Events[0].Attributes, attribute.String("exception.type", "invalid_param"))

	// error.type and rpc.response.status_code belong to the OTel vocabulary.
	for _, a := range stub.Attributes {
		require.NotEqual(t, "error.type", string(a.Key))
		require.NotEqual(t, "rpc.response.status_code", string(a.Key))
	}
}

func TestLegacyMCP_EndSpanOnNonJSONRPCError(t *testing.T) {
	span, exported := newTestLegacyMCPSpan(t, "tools/list", &mcp.ListToolsParams{})
	span.EndSpanOnError("internal_error", errors.New("boom"))

	stub := exported()
	require.Equal(t, codes.Error, stub.Status.Code)
	require.Contains(t, stub.Events[0].Attributes, attribute.String("exception.message", "boom"))
}

// TestLegacyMCP_SignalsAddedLaterAreNoops pins that the signals introduced
// alongside the OTel vocabulary stay out of the default one. The MCP proxy calls
// these unconditionally, so they must be silent here rather than absent.
func TestLegacyMCP_SignalsAddedLaterAreNoops(t *testing.T) {
	span, exported := newTestLegacyMCPSpan(t, "tools/list", &mcp.ListToolsParams{})
	span.RecordClientSession("client-sess-1234")
	span.AddEvent("tools/list aggregation begin")
	span.RecordListResult(mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "a"}, {Name: "b"}}})
	span.RecordToolCallResult([]byte(`{"status":200}`))
	span.AddEvent("tools/list aggregation end")
	span.EndSpan()

	stub := exported()
	require.Empty(t, stub.Events)
	for _, a := range stub.Attributes {
		require.NotEqual(t, "mcp.session.id", string(a.Key))
		require.NotEqual(t, "mcp.tools.count", string(a.Key))
		require.NotEqual(t, "gen_ai.tool.call.result", string(a.Key))
	}
}
