// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracingapi

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPTracer creates spans for MCP requests.
type MCPTracer interface {
	// StartSpanAndInjectMeta starts a span and injects trace context into
	// the _meta mutation.
	//
	// The parent trace context is taken from the HTTP headers first, then the JSON-RPC _meta on
	// top, so _meta wins when a client propagates in both. Either source alone works.
	//
	// Parameters:
	//   - ctx: might include a parent span context.
	//   - req: Incoming MCP request message.
	//   - param: Incoming MCP parameter used to extract parent trace context.
	//   - headers: Request HTTP request headers, also used to extract parent trace context.
	//
	// Returns nil unless the span is sampled.
	StartSpanAndInjectMeta(ctx context.Context, req *jsonrpc.Request, param mcp.Params, headers http.Header) MCPSpan
}

// MCPSpan represents an MCP span.
//
// A span records the vocabulary of the semantic convention selected by the
// AI_GATEWAY_TRACING_SEMCONV environment variable. Methods describing signals a
// convention does not define are no-ops under that convention rather than
// errors, so the MCP proxy calls them unconditionally.
type MCPSpan interface {
	// RecordRouteToBackend records the backend that was routed to. It is called
	// once per backend, so for the methods that broadcast (initialize, the
	// list aggregations) it is called several times on the same span.
	RecordRouteToBackend(backend string, session string, isNew bool)
	// RecordClientSession records the client-facing MCP session, i.e. the
	// session between the MCP client and this gateway. Unlike the per-backend
	// session passed to RecordRouteToBackend, there is exactly one per request,
	// which is what the conventions mean by mcp.session.id.
	RecordClientSession(sessionID string)
	// AddEvent records a timestamped event (OTel annotation) on the span, giving
	// list operations a "begin"/"end" timeline alongside their attributes.
	AddEvent(name string)
	// RecordListResult records the size of an aggregated list result
	// (tools/list, resources/list, prompts/list, resources/templates/list) as a
	// count attribute. Content is never recorded; only the count is.
	RecordListResult(result any)
	// RecordToolCallResult records the tools/call result payload as the
	// gen_ai.tool.call.result attribute. The raw result JSON is recorded only
	// when message-content capture is enabled; otherwise the call is a no-op.
	RecordToolCallResult(resultJSON []byte)
	// EndSpan finalizes and ends the span.
	EndSpan()
	// EndSpanOnError finalizes and ends the span with an error status.
	EndSpanOnError(errType string, err error)
}

// Ensure NoopMCPTracer implements [MCPTracer].
var _ MCPTracer = NoopMCPTracer{}

// NoopMCPTracer is a no-op implementation of [MCPTracer].
type NoopMCPTracer struct{}

// StartSpanAndInjectMeta implements [MCPTracer.StartSpanAndInjectMeta].
func (NoopMCPTracer) StartSpanAndInjectMeta(context.Context, *jsonrpc.Request, mcp.Params, http.Header) MCPSpan {
	return nil
}
