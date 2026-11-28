# MCP Server and Per-Backend Client Spans

## Status and Scope

Proposed implementation design for [#2562](https://github.com/theagentrouter/agent-router/issues/2562).
This PR does not close that issue or change emitted spans. It specifies the
compatibility and dispatch contracts needed before implementation.

## Problem

`internal/tracing/mcp.go` currently creates one CLIENT span for the incoming MCP
request. Fan-out calls record backend-routing events on that span. Events record
which backends participated, but cannot represent a separate duration and outcome
for each operation. A partial failure can be obscured by a successful aggregate.

The gateway has two distinct protocol roles: server to the downstream client and
client to each upstream backend. Model those roles explicitly.

## Proposed Trace Shape

For one `tools/list` request dispatched to three backends:

```text
downstream MCP client span
  gateway tools/list SERVER
    tools/list CLIENT [backend=a, success]
    tools/list CLIENT [backend=b, success]
    tools/list CLIENT [backend=c, timeout]
```

Each backend's remote server span is parented by its corresponding CLIENT span.
The gateway SERVER span describes the client-visible operation; its status
continues to follow the current aggregation outcome policy. A successful
aggregate must not overwrite a failed child's status. Spans count operations,
not tools or list items, and do not duplicate token usage across parent and child.

## Compatibility

Initially support the new topology only under the GenAI convention, behind a
separate explicit MCP topology opt-in. Proposed setting:
`AI_GATEWAY_MCP_TRACE_TOPOLOGY=server_client`, with `legacy` as the default.
The exact name requires maintainer agreement. Selecting `gen_ai` alone must not
silently change the span tree of existing users. Reject unsupported combinations
at startup rather than ignoring configuration.

Keep OpenInference and the existing GenAI topology unchanged by default. In the
new mode, replace per-backend routing events with child spans; retain aggregate
begin/end events and list counts on the server span. Document the increase in
span volume (one server plus one client per actual dispatch) and dashboard impact.
Changing the default, if desired, is a separate release decision.

## Tracing Contract

Introduce a request-scoped trace handle that remains available even when spans
are not recording. The current `StartSpanAndInjectMeta` returns nil for unsampled
requests; that is insufficient as the sole owner of backend context propagation.
Do not use a recording decision to decide whether to propagate trace context.
Retain the existing no-op behavior when tracing is disabled entirely.

The handle owns the inbound context and starts independent backend operations.
Each operation returns its child context and an idempotent completion handle.
Completion records success or the observed error and ends the child once. Parent
completion must not manufacture success for unfinished children.

Keep the proxy independent of OTel SDK types. Define these operations in
`tracingapi` and implement them in `internal/tracing`, with a compatibility adapter
for existing vocabulary behavior. Do not add methods to every recorder merely
to accommodate this MCP-specific lifecycle.

### Context and Parameter Ownership

Extract the incoming parent from `_meta` with the existing HTTP-header fallback.
In the new topology, associate ambient transport context via a span link when it
differs, subject to the chosen MCP semantic-convention version. Inject each
backend CLIENT context into its outgoing request's `_meta` before serialization.

Fan-out currently shares the request and typed parameters across goroutines.
Each dispatch must own its request value, serialized params, and mutable `_meta`
map. Never call `SetMeta` on shared parameters from concurrent backend goroutines.
Prefer copying the request envelope and cloning the JSON params object, replacing
only tracing keys in its `_meta`; preserve unknown application fields. Reuse the
same per-dispatch carrier for any HTTP trace propagation so transport and MCP
propagation do not accidentally use a sibling backend's parent.

No mutation may be visible to another dispatch or to the original client params.
Notifications still receive propagation where supported but must not wait for a
JSON-RPC response that their protocol does not provide.

## Integration Points and Lifecycle

| Path                                                             | Start                                               | Finish                                                                           |
| ---------------------------------------------------------------- | --------------------------------------------------- | -------------------------------------------------------------------------------- |
| Session initialization in `mcpproxy.go`                          | Before each selected backend's initialize operation | Initialization result or failure, including any existing initialization exchange |
| Direct methods via `invokeAndProxyResponse` in `legacy.go`       | Before upstream invocation                          | Matching JSON-RPC result/error, tool `isError`, transport error, or cancellation |
| Aggregating methods via `sendToBackendsFiltered` in `session.go` | Before each actual backend dispatch                 | Matching JSON-RPC response/error or terminal transport failure                   |
| Notifications dispatched to backends                             | Before sending                                      | Transport acknowledgment/failure according to the protocol                       |

Do not finish a CLIENT operation merely when HTTP headers arrive. Conversely,
do not leave a completed MCP operation open until a long-lived SSE transport
closes. Match the JSON-RPC response ID and end at the operation boundary; unrelated
notifications or server-to-client requests must not end it. EOF before a required
response is incomplete, not success. HTTP and JSON-RPC errors and tool `isError`
remain distinguishable; closing the same operation twice is harmless.

Current fan-out code treats some cancellation and EOF paths as normal returns.
Tracing therefore needs an explicit completion signal from response parsing,
not just the return value of `sendRequestPerBackend`. Failed backend resolution
before dispatch belongs on the server operation; it is not a successful client
call. Backends excluded by selection produce no child span.

Existing reconnect or retry paths must not duplicate a logical operation's
completion. Transport attempt spans, if added later, sit below the CLIENT
operation. Background subscription transport lifetime and autonomous reverse
requests are separate follow-ups, not children indefinitely attached to an
already-completed request.

## Attributes and Privacy

The SERVER span owns the client-facing `mcp.session.id`. Each CLIENT span owns its
backend session ID and the backend name. Initialization sets its backend session
only after one is successfully negotiated. Preserve existing content-capture
settings; do not copy tool results into both parent and child by default.

Do not report the local Envoy listener as the actual upstream MCP server address.
When no authoritative upstream address is available, omit `server.address` and
identify the backend by its configured name. Likewise, omit the real client
address until there is a trusted source: neither raw X-Forwarded-For nor the
loopback `RemoteAddr` establishes the end user's address. Defining forwarded
header trust is outside this implementation.

Keep metric labels and span names low-cardinality. Backend session IDs belong
in spans, not metric labels. This change does not introduce per-request usage
export or user-identity attribution; those have separate proposals/PRs.

## Implementation Sequence

1. Add the opt-in configuration, request trace handle, and backend operation
   unit tests. Keep existing defaults and propagate unsampled contexts.
2. Integrate initialization and direct calls with independently owned params and
   complete success/error/cancellation handling.
3. Integrate fan-out at the matched-response boundary, including partial failure
   and notifications. Add proxy-level tests before enabling the full opt-in.
4. Document the span tree and migration, with deterministic end-to-end traces.

Do not expose the feature as complete after adding only direct tool-call spans.
Keep the opt-in internal/unreleased until all supported dispatch paths have
explicit coverage. Coordinate with the stateless MCP work in proposal 012 so
new protocol paths can reuse the operation handle without inheriting sessions.

## Acceptance Tests

- Three-backend fan-out yields one SERVER and three sibling CLIENT spans with
  the same trace ID and distinct span IDs. Upstream `_meta` points to each child.
- One backend timeout and two successes preserve the timeout child's error and
  the existing aggregate response/status semantics.
- Direct JSON, SSE, JSON-RPC error, tool `isError`, and pre-response EOF finish
  exactly once, including when an SSE connection remains open after the result.
- Cancellation completes active operations and does not leak goroutines or
  channels; excluded backends create no dispatch spans.
- Initialization failures and new/reused session IDs are correctly scoped.
- Unsampled incoming traces propagate independently to each backend without
  exporting sampled spans under a parent-based sampler.
- Race-enabled concurrent fan-out leaves original request metadata unchanged.
- Default-mode snapshots remain unchanged. Content-disabled tests contain no
  tool payloads or untrusted forwarded client addresses.

## References

- [Tracking issue and existing design discussion](https://github.com/theagentrouter/agent-router/issues/2562)
- [Current MCP tracing API](../../../internal/tracing/tracingapi/mcp.go)
- [Current tracer](../../../internal/tracing/mcp.go)
- [Fan-out dispatch](../../../internal/mcpproxy/session.go)
- [Stateless MCP proposal](../012-mcp-new-spec-rc/proposal.md)
- [OTel MCP conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/mcp.md)

The OTel MCP conventions are in Development. Pin the reviewed convention revision
in the implementation and explicitly document intentional compatibility choices.
