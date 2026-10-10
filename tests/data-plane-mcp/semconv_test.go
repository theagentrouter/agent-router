// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplanemcp

import (
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// The rest of this suite runs without AI_GATEWAY_TRACING_SEMCONV set and asserts
// the legacy MCP vocabulary, which is the default and must not change. This file
// covers the opt-in path: the same requests through the same data plane, with
// the OTel MCP semantic conventions selected.

func TestMCPTracing_OTelSemConv(t *testing.T) {
	m := requireNewMCPEnvWithExtProcEnv(t,
		[]string{"AI_GATEWAY_TRACING_SEMCONV=gen_ai"},
		false, 1200*time.Second, defaultMCPPath)

	s := m.newSessionOTel(t)

	t.Run("tools/list uses the method name and records the count", func(t *testing.T) {
		tools, err := s.session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)

		span := m.collector.TakeSpan()
		require.Equal(t, "tools/list", span.Name)
		attrs := otelSpanAttributes(t, span)
		require.Equal(t, "tools/list", attrs["mcp.method.name"])
		require.Equal(t, "tcp", attrs["network.transport"])
		require.Equal(t, "http", attrs["network.protocol.name"])
		require.Equal(t, "1.1", attrs["network.protocol.version"])
		require.NotEmpty(t, attrs["jsonrpc.request.id"])

		// The legacy keys must be gone under this convention.
		require.NotContains(t, attrs, "mcp.transport")
		require.NotContains(t, attrs, "mcp.request.id")

		// The aggregated size is recorded as a count, and the aggregation is
		// bracketed by begin/end events.
		require.NotEmpty(t, tools.Tools)
		require.Equal(t, int64(len(tools.Tools)), otelSpanIntAttribute(t, span, "mcp.tools.count"))
		require.Equal(t, []string{
			"tools/list aggregation begin",
			"route to backend",
			"route to backend",
			"tools/list aggregation end",
		}, otelSpanEventNames(span))
	})

	t.Run("tools/call appends the tool name and marks the GenAI operation", func(t *testing.T) {
		_, err := s.session.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      defaultMCPBackendResourcePrefix + testmcp.ToolEcho.Tool.Name,
			Arguments: map[string]any{"message": "hello"},
		})
		require.NoError(t, err)

		span := m.collector.TakeSpan()
		toolName := defaultMCPBackendResourcePrefix + testmcp.ToolEcho.Tool.Name
		require.Equal(t, "tools/call "+toolName, span.Name)

		attrs := otelSpanAttributes(t, span)
		require.Equal(t, "tools/call", attrs["mcp.method.name"])
		require.Equal(t, "execute_tool", attrs["gen_ai.operation.name"])
		require.Equal(t, toolName, attrs["gen_ai.tool.name"])
		require.NotContains(t, attrs, "mcp.tool.name")

		// Content is a separate opt-in that this environment does not enable.
		require.NotContains(t, attrs, "gen_ai.tool.call.arguments")
		require.NotContains(t, attrs, "gen_ai.tool.call.result")
	})

	t.Run("session id is the client-facing one", func(t *testing.T) {
		_, err := s.session.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)

		span := m.collector.TakeSpan()
		attrs := otelSpanAttributes(t, span)
		// tools/list fans out to every backend, each with its own upstream
		// session recorded on its own event. The span-level attribute is the one
		// client-facing session, i.e. the ID the client itself holds, rather than
		// whichever backend goroutine happened to write last.
		require.Equal(t, s.session.ID(), attrs["mcp.session.id"])

		var eventSessions []string
		for _, e := range span.Events {
			if e.Name != "route to backend" {
				continue
			}
			for _, a := range e.Attributes {
				if a.Key == "mcp.session.id" {
					eventSessions = append(eventSessions, a.Value.GetStringValue())
				}
			}
		}
		require.Len(t, eventSessions, 2, "tools/list fans out to both backends")
		for _, es := range eventSessions {
			require.NotEqual(t, attrs["mcp.session.id"], es,
				"per-backend session must not be what the span records")
		}
	})

	t.Run("server peer is not recorded", func(t *testing.T) {
		_, err := s.session.ListResources(t.Context(), &mcp.ListResourcesParams{})
		require.NoError(t, err)

		attrs := otelSpanAttributes(t, m.collector.TakeSpan())
		require.NotContains(t, attrs, "server.address")
		require.NotContains(t, attrs, "server.port")
	})
}

// newSessionOTel mirrors newSession but asserts the initialize span under the
// OTel conventions instead of the legacy ones.
func (m *mcpEnv) newSessionOTel(t *testing.T) *mcpSession {
	t.Helper()
	s := m.newSessionWithoutSpanCheck(t)

	span := m.collector.TakeSpan()
	require.Equal(t, "initialize", span.Name)
	attrs := otelSpanAttributes(t, span)
	require.Equal(t, "initialize", attrs["mcp.method.name"])
	require.Equal(t, "demo-http-client", attrs["mcp.client.name"])
	require.Equal(t, s.session.ID(), attrs["mcp.session.id"])
	return s
}

func otelSpanAttributes(t *testing.T, span *tracev1.Span) map[string]string {
	t.Helper()
	require.NotNil(t, span, "expected span, actual nil")
	attrs := make(map[string]string)
	for _, a := range span.Attributes {
		if _, ok := a.Value.Value.(*commonv1.AnyValue_StringValue); ok {
			attrs[a.Key] = a.Value.GetStringValue()
		}
	}
	return attrs
}

func otelSpanIntAttribute(t *testing.T, span *tracev1.Span, key string) int64 {
	t.Helper()
	for _, a := range span.Attributes {
		if a.Key == key {
			return a.Value.GetIntValue()
		}
	}
	t.Fatalf("attribute %s not found in span: %s", key, span.String())
	return 0
}

func otelSpanEventNames(span *tracev1.Span) []string {
	names := make([]string, 0, len(span.Events))
	for _, e := range span.Events {
		names = append(names, e.Name)
	}
	return names
}
