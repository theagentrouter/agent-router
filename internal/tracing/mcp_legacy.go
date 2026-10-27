// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// mcpVocabularyLegacy is the gateway-specific MCP vocabulary that predates the
// OTel MCP semantic conventions. It is the default, and its output is frozen:
// it must keep emitting exactly what it emitted before mcpVocabularyOTel
// existed, because users' dashboards query these names.
//
// It is deprecated in favor of mcpVocabularyOTel, which users select with
// AI_GATEWAY_TRACING_SEMCONV=gen_ai. Signals added after the split (the list
// counts, the aggregation timeline, tool call content) are deliberately not
// recorded here; they only exist under the OTel vocabulary.
func mcpVocabularyLegacy() *mcpVocabulary {
	return &mcpVocabulary{
		name:              "openinference",
		spanName:          legacyMCPSpanName,
		requestAttributes: legacyMCPRequestAttributes,
		routeToBackend: func(span trace.Span, backend, sessionID string, isNew bool) {
			span.AddEvent("route to backend", trace.WithAttributes(
				attribute.String("mcp.backend.name", backend),
				attribute.String("mcp.session.id", sessionID),
				attribute.Bool("mcp.session.new", isNew),
			))
		},
		// The legacy vocabulary reports the session only on the per-backend
		// event above, so there is nothing to record for the client-facing one.
		clientSession:  noopSpanSession,
		event:          noopSpanEvent,
		listResult:     noopSpanListResult,
		toolCallResult: noopSpanPayload,
		// The failure class is carried by the shared exception event and the
		// span status; the legacy vocabulary adds no attributes of its own.
		requestError: noopSpanError,
	}
}

func legacyMCPRequestAttributes(req *jsonrpc.Request, p mcp.Params) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("mcp.protocol.version", "2025-06-18"),
		attribute.String("mcp.transport", "http"),
		attribute.String("mcp.request.id", fmt.Sprintf("%v", req.ID)),
		attribute.String("mcp.method.name", req.Method),
	}
	return append(attrs, legacyMCPParamsAttributes(p)...)
}

func legacyMCPParamsAttributes(p mcp.Params) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	switch params := p.(type) {
	case *mcp.InitializeParams:
		if params.ClientInfo != nil {
			attrs = append(attrs,
				attribute.String("mcp.client.name", params.ClientInfo.Name),
				attribute.String("mcp.client.title", params.ClientInfo.Title),
				attribute.String("mcp.client.version", params.ClientInfo.Version),
			)
		}
	case *mcp.CallToolParams:
		attrs = append(attrs, attribute.String("mcp.tool.name", params.Name))
	case *mcp.GetPromptParams:
		attrs = append(attrs, attribute.String("mcp.prompt.name", params.Name))
	case *mcp.SetLoggingLevelParams:
		attrs = append(attrs, attribute.String("mcp.logging.level", string(params.Level)))
	case *mcp.ListResourcesParams:
	case *mcp.ReadResourceParams:
		attrs = append(attrs, attribute.String("mcp.resource.uri", params.URI))
	case *mcp.SubscribeParams:
		attrs = append(attrs, attribute.String("mcp.resource.uri", params.URI))
	case *mcp.UnsubscribeParams:
		attrs = append(attrs, attribute.String("mcp.resource.uri", params.URI))
	case *mcp.ProgressNotificationParams:
		if params.Progress != 0 {
			attrs = append(attrs, attribute.Float64("mcp.notifications.progress", params.Progress))
		}
		if params.ProgressToken != nil {
			attrs = append(attrs, attribute.String("mcp.notifications.progress.token", fmt.Sprintf("%v", params.ProgressToken)))
		}
		if len(params.Message) > 0 {
			attrs = append(attrs, attribute.String("mcp.notifications.progress.message", params.Message))
		}
	case *mcp.CompleteParams:
		if len(params.Argument.Name) > 0 {
			attrs = append(attrs, attribute.String("mcp.complete.argument.name", params.Argument.Name))
		}
		if len(params.Argument.Value) > 0 {
			attrs = append(attrs, attribute.String("mcp.complete.argument.value", params.Argument.Value))
		}
	}

	return attrs
}

// legacyMCPSpanName converts MCP method names to span names following mcp-go SDK patterns.
func legacyMCPSpanName(method string, _ mcp.Params) string {
	switch method {
	case "initialize":
		return "Initialize"
	case "tools/list":
		return "ListTools"
	case "tools/call":
		return "CallTool"
	case "prompts/list":
		return "ListPrompts"
	case "prompts/get":
		return "GetPrompt"
	case "resources/list":
		return "ListResources"
	case "resources/read":
		return "ReadResource"
	case "resources/subscribe":
		return "Subscribe"
	case "resources/unsubscribe":
		return "Unsubscribe"
	case "resources/templates/list":
		return "ListResourceTemplates"
	case "logging/setLevel":
		return "SetLoggingLevel"
	case "completion/complete":
		return "Complete"
	case "ping":
		return "Ping"
	default:
		return method
	}
}
