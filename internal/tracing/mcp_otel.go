// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// mcpVocabularyOTel follows the OpenTelemetry MCP semantic conventions. Users
// select it with AI_GATEWAY_TRACING_SEMCONV=gen_ai; it is not the default,
// because its attribute keys and span names differ from mcpVocabularyLegacy and
// switching would break dashboards built on the latter.
//
// captureContent mirrors the GenAI message-content opt-in
// (OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT). Tool call arguments and
// results are message content, so they are recorded only when it is enabled.
//
// Attribute names are hardcoded rather than imported from the versioned semconv
// package, matching how the LLM recorders do it; mcp_semconv_test.go pins them
// so an upstream rename surfaces as a test failure instead of silently changing
// what users query.
func mcpVocabularyOTel(captureContent bool) *mcpVocabulary {
	return &mcpVocabulary{
		name:     "gen_ai",
		spanName: otelMCPSpanName,
		requestAttributes: func(req *jsonrpc.Request, p mcp.Params) []attribute.KeyValue {
			return otelMCPRequestAttributes(req, p, captureContent)
		},
		routeToBackend: func(span trace.Span, backend, sessionID string, isNew bool) {
			// The per-backend session is gateway-to-backend and there is one per
			// backend, so it stays on the event. The span-level mcp.session.id
			// is the client-facing session, recorded by clientSession below.
			span.AddEvent("route to backend", trace.WithAttributes(
				attribute.String("mcp.backend.name", backend),
				attribute.String("mcp.session.id", sessionID),
				attribute.Bool("mcp.session.new", isNew),
			))
		},
		clientSession: func(span trace.Span, sessionID string) {
			if sessionID == "" {
				return
			}
			span.SetAttributes(attribute.String("mcp.session.id", sessionID))
		},
		event: func(span trace.Span, name string) {
			span.AddEvent(name)
		},
		listResult: otelMCPListResult,
		toolCallResult: func(span trace.Span, resultJSON []byte) {
			if !captureContent || len(resultJSON) == 0 {
				return
			}
			span.SetAttributes(attribute.String("gen_ai.tool.call.result", string(resultJSON)))
		},
		requestError: func(span trace.Span, errType string, err error) {
			// error.type is the OTel span attribute for the failure class; the
			// JSON-RPC numeric code, when present, is rpc.response.status_code.
			span.SetAttributes(attribute.String("error.type", errType))
			var jsonrpcErr *jsonrpc.Error
			if errors.As(err, &jsonrpcErr) {
				span.SetAttributes(attribute.Int64("rpc.response.status_code", jsonrpcErr.Code))
			}
		},
	}
}

// otelMCPListResult records the size of an aggregated list result.
//
// Only the element count is recorded. Names and payloads are content and are
// deliberately left off the span to keep cardinality bounded and avoid leaking
// tool/resource inventories.
func otelMCPListResult(span trace.Span, result any) {
	switch v := result.(type) {
	case mcp.ListToolsResult:
		span.SetAttributes(attribute.Int("mcp.tools.count", len(v.Tools)))
	case mcp.ListResourcesResult:
		span.SetAttributes(attribute.Int("mcp.resources.count", len(v.Resources)))
	case mcp.ListResourceTemplatesResult:
		span.SetAttributes(attribute.Int("mcp.resource_templates.count", len(v.ResourceTemplates)))
	case mcp.ListPromptsResult:
		span.SetAttributes(attribute.Int("mcp.prompts.count", len(v.Prompts)))
	}
}

func otelMCPRequestAttributes(req *jsonrpc.Request, p mcp.Params, captureContent bool) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("mcp.protocol.version", "2025-06-18"),
		// network.transport is the OSI transport ("tcp"); network.protocol.* the
		// application protocol. The gateway forwards to a local plain-HTTP/1.1
		// listener, so the version is fixed.
		attribute.String("network.transport", "tcp"),
		attribute.String("network.protocol.name", "http"),
		attribute.String("network.protocol.version", "1.1"),
		attribute.String("jsonrpc.request.id", fmt.Sprintf("%v", req.ID)),
		attribute.String("mcp.method.name", req.Method),
	}
	return append(attrs, otelMCPParamsAttributes(p, captureContent)...)
}

func otelMCPParamsAttributes(p mcp.Params, captureContent bool) []attribute.KeyValue {
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
		attrs = append(attrs,
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", params.Name),
		)
		// Tool call arguments are message content, so they follow the GenAI
		// opt-in rather than being recorded unconditionally.
		if captureContent && params.Arguments != nil {
			if raw, err := json.Marshal(params.Arguments); err == nil {
				attrs = append(attrs, attribute.String("gen_ai.tool.call.arguments", string(raw)))
			}
		}
	case *mcp.GetPromptParams:
		attrs = append(attrs, attribute.String("gen_ai.prompt.name", params.Name))
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

// otelMCPSpanName derives the span name following the OTel MCP convention: the
// raw method name, with the target appended for tools/call and prompts/get. The
// resource URI is deliberately omitted from resources/* names to keep span name
// cardinality bounded.
func otelMCPSpanName(method string, p mcp.Params) string {
	switch params := p.(type) {
	case *mcp.CallToolParams:
		if params.Name != "" {
			return method + " " + params.Name
		}
	case *mcp.GetPromptParams:
		if params.Name != "" {
			return method + " " + params.Name
		}
	}
	return method
}
