// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// envRemoteParentAsLink opts the gateway's spans out of joining the caller's
// trace. By default the extracted remote context becomes the span's parent,
// so a consumer that instruments its own LLM client sees the gateway's span
// land inside its trace — backends that key project/tenant attribution on the
// trace (e.g. Arize Phoenix, whose trace rows are globally unique by trace_id
// and whose per-project token/cost rollups sum every LLM span in the trace's
// project) then double-count each request. When set to "true", every span
// instead starts a NEW trace as a root span, recording the caller's context
// as a span link plus caller.trace_id/caller.span_id attributes (Phoenix
// drops OTLP links at ingestion, so attributes keep the correlation
// queryable). Propagation itself is unchanged: the upstream request is
// stamped with the gateway's own trace context.
const envRemoteParentAsLink = "AI_GATEWAY_TRACING_REMOTE_PARENT_AS_LINK"

// remoteParentAsLinkTracer wraps a trace.Tracer so that a REMOTE parent in
// the start context (i.e. one extracted from the caller's traceparent) is
// demoted to a span link on a new root span. In-process parents are left
// untouched. Wrapping the shared tracer covers every span-start site — the
// generic request tracer behind all LLM endpoints and the MCP tracer.
type remoteParentAsLinkTracer struct {
	embedded.Tracer
	delegate trace.Tracer
}

func (t remoteParentAsLinkTracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() && sc.IsRemote() {
		opts = append(opts,
			trace.WithNewRoot(),
			trace.WithLinks(trace.Link{SpanContext: sc}),
			trace.WithAttributes(
				attribute.String("caller.trace_id", sc.TraceID().String()),
				attribute.String("caller.span_id", sc.SpanID().String()),
			),
		)
	}
	return t.delegate.Start(ctx, spanName, opts...)
}
