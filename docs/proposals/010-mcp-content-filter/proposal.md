# Per-Backend MCP Content Filter

| Field          | Value                                                                                 |
| -------------- | ------------------------------------------------------------------------------------- |
| Status         | Draft — RFC for maintainer review                                                     |
| Author(s)      | Envoy AI Gateway contributors                                                         |
| Target release | v0.6                                                                                  |
| Depends on     | Proposal [006 — MCP Gateway][proposal-006]                                            |
| Supersedes     | —                                                                                     |
| Implementation | Prototype landed on a contributor fork; production follow-up PRs are sequenced below. |

## TL;DR

Envoy AI Gateway proxies Model Context Protocol (MCP) servers behind a single
`MCPRoute`. MCP traffic carries free-text tool arguments and long-form
tool results that operators increasingly need to **inspect and rewrite in
flight** — to scrub PII, enforce evaluation-time exclusions, or substitute a
sanitized response for a backend reply. None of these cases fit the existing
MCP policy surfaces: `MCPBackendSecurityPolicy` authenticates the caller,
`MCPToolFilter` matches tool names, and neither looks at the body.

This proposal introduces **`MCPContentFilter`**, a per-backend API that
delegates the body decision to an **external HTTP service** that the
operator owns, plus a matching in-process runtime on the gateway's MCP
proxy. The gateway stays stateless with respect to policy. It contributes
only the things every filter deployment needs regardless of policy:
admission control, circuit-breaking, a bounded cache, audit emission,
shadow mode, sampling, and kill switches — all hot-reloadable.

The reliability primitives, API surface, and wire contract have been
implemented on a contributor fork and validated end-to-end against live
MCP backends; the data we've seen is referenced in
[§ 9 Validation](#9-validation). The upstream contribution will land in
three sequenced PRs outlined in [§ 12 Rollout plan](#12-rollout-plan).

---

## 1. Motivation

### 1.1 The shape of MCP traffic

Every `tools/call` that flows through the MCP proxy moves two kinds of
content:

- **Arguments** going to a backend. These are free-text parameters
  (ticket descriptions, user messages, log excerpts, support-case
  summaries) and they routinely contain customer PII.
- **Results** coming back from a backend. These are often SME-authored —
  root-cause analyses, resolution notes, post-closure remarks — and in an
  evaluation or training setting they can leak the exact answer that the
  agent is supposed to derive from first principles.

Both directions are body-level concerns. Neither can be reasoned about
from headers alone, and neither can be gated by tool name alone: the same
`jira_get_issue` call can be a perfectly legitimate lookup or a leak of
the ticket the agent is being graded on, depending on _which_ ticket is
being fetched by _which_ caller.

### 1.2 Why existing surfaces are insufficient

| Existing surface                           | What it handles                 | Why it is not enough for body-level policy                                                                                                                                               |
| ------------------------------------------ | ------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `MCPBackendSecurityPolicy`                 | Caller authentication & headers | Operates on identity, not on payload. Cannot redact fields or enforce exclusions.                                                                                                        |
| `MCPToolFilter`                            | Tool-name allow/deny            | Decision is on tool identity only. Cannot distinguish a permitted lookup from one that leaks evaluation ground truth.                                                                    |
| Generic Envoy `ext_proc` on the HTTP chain | Arbitrary HTTP request/response | Runs at the HTTP layer. Would need to re-parse JSON-RPC, re-implement MCP session handling, and re-derive per-backend policy — duplicating the very concerns the MCP proxy already owns. |
| In-process plugin ABI                      | Maximum performance             | Couples operator policy code to gateway release cadence; complicates supply chain; undermines the project's non-goal of embedding policy in the gateway.                                 |

Each of these works on a different axis. None crosses the gap into
**content-aware, MCP-native, per-backend** decisions.

### 1.3 Why the gateway is the right place

Two properties push content inspection toward the gateway rather than the
agent or the backend:

1. **Single chokepoint.** An operator who wants to guarantee that _no_
   MCP response reaches an agent without being scanned has exactly one
   place to wire that guarantee in — the MCP proxy — regardless of how
   many concurrent backends are aggregated behind one `MCPRoute`. Pushing
   the responsibility to individual backends requires auditing every
   backend owner. Pushing it to individual agents requires auditing every
   caller.
2. **Rollout affordances.** The gateway already owns session state,
   traffic mirroring infrastructure, circuit breaking, and observability.
   Operators need every one of those to roll out a content filter
   safely — shadow first, then enforce, with kill switches at the ready —
   and forcing each filter vendor to rebuild that stack in a sidecar is
   how incidents happen.

### 1.4 Use cases this unlocks

- **PII scrubbing**, either direction. Free-text arguments are redacted
  before they leave the perimeter; SME-authored resolution notes are
  redacted before they reach the caller.
- **Evaluation-time exclusion.** A running evaluation hides the target
  record (a ticket, a document, an RCA) from the agent under test, so
  scoring is not biased by leakage through the MCP search surface.
- **Sanitized substitution.** A backend returns the current state of a
  ticket; the filter substitutes the creation-time snapshot so the agent
  reasons about the original problem rather than the resolution that a
  human already wrote.
- **Compliance gating.** A regulated tenant requires that responses be
  evaluated by a specific approval policy; non-compliant responses are
  rejected and a standard JSON-RPC error is surfaced to the client.

These are different policies. They are united only by "the gateway needs
to look at the body and can rewrite it." That is the capability this
proposal adds.

---

## 2. Goals and non-goals

### 2.1 Goals

| Goal                           | What it guarantees                                                                                                                                                                                   |
| ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Per-backend opt-in             | A single `MCPRoute` can mix filtered and unfiltered backends. Omitting `contentFilter` on a backend is a no-op.                                                                                      |
| Two attachment models          | Operators can attach the filter inline on a backend **or** via a standalone `MCPContentFilter` that uses `targetRefs` — so platform/security teams can attach filters without owning the `MCPRoute`. |
| External policy, not embedded  | The gateway stays stateless with respect to policy. The filter service is owned, deployed, and scaled by the operator.                                                                               |
| Request or response, or both   | Each scope is enabled independently. The filter sees only what it is authorized to see.                                                                                                              |
| Safe defaults                  | Fail-open by default so a filter outage does not turn into a tool-call outage. `FailurePolicy: Fail` opt-in for compliance workloads.                                                                |
| No new protocol                | Rejections flow through the standard JSON-RPC error envelope. No gateway-invented error codes beyond reserved compliance/failure codes.                                                              |
| Observable by default          | Every decision, failure, and header value is a metric and an audit event, with bounded label cardinality.                                                                                            |
| Robust to a misbehaving filter | Per-invocation timeout, circuit breaker, sharded LRU cache, bounded admission, hedged retries, W3C trace propagation — all in-process.                                                               |
| Hot-reloadable controls        | Shadow mode, per-backend kill switch, cluster-wide kill switch, and sampling are all effective on the next call after a CRD or ConfigMap edit.                                                       |

### 2.2 Non-goals

- **Embedding a policy engine in the gateway.** Operators own their
  policy by satisfying a stable HTTP contract, not a Go plugin ABI.
- **Stream-scope filtering.** The filter operates on `tools/call` bodies
  only. Long-lived SSE notification streams and server-to-client JSON-RPC
  are out of scope for v1 (see [§ 13 Open questions](#13-open-questions)).
- **Domain-specific filter code in the gateway tree.** PII engines, LLM
  prompts, and backend-specific redactors belong in filter
  implementations and are deliberately out-of-tree. See [§ 11 Reference
  implementation](#11-reference-implementation).

---

## 3. Design overview

Three actors, one optional upstream. The gateway sits in line with every
tool call, the filter is an ordinary HTTP service, and the policy lives
entirely inside that filter. Neither the agent nor the MCP backend need
to know filtering is taking place.

```text
   ┌──────────────────────┐
   │      AI Agent        │
   │ (IDE, CLI, eval rig) │
   └──────────┬───────────┘
              │  JSON-RPC: tools/call
              ▼
   ┌──────────────────────────────────────────┐
   │           Envoy AI Gateway               │   ◀── MCPRoute CRD
   │           (internal/mcpproxy)            │       backendRefs[i].contentFilter
   │                                          │       — or —
   │   admission ▸ cache ▸ breaker ▸ HTTP     │       MCPContentFilter (targetRefs)
   └───────┬──────────────────────────┬───────┘
           │ Request scope            │ Response scope
           ▼                          ▼
   ┌─────────────────────────────────────────┐
   │          Filter service (HTTP)          │   ◀── operator-owned
   │     POST /filter     GET /healthz        │
   └───────┬──────────────────────┬──────────┘
           │ (optional upstream)  │
           ▼                      ▼
   ┌──────────────┐      ┌──────────────────┐
   │  LLM /       │      │  MCP backends    │
   │  classifier  │      │  (any)           │
   └──────────────┘      └──────────────────┘
```

For each `tools/call` that matches a configured scope, the gateway POSTs
a JSON envelope to the filter URL. The envelope contains the JSON-RPC
message (base64-encoded to preserve bytes), a bounded set of forwarded
client headers (allowlisted in `ForwardHeaders`), and routing metadata
(route name, backend name, scope, tool name). The filter replies with
one of three actions: `pass`, `redact`, or `reject`.

### 3.1 Request lifecycle

```text
 Agent      Gateway       Filter          Backend       (optional)
   │           │             │               │         upstream LLM
   │──tools/───▶             │               │              │
   │  call     │             │               │              │
   │           │── Request ──▶               │              │
   │           │◀──  pass ───│               │              │
   │           │             │               │              │
   │           │──────────── tools/call ─────▶              │
   │           │◀─────────── result ─────────│              │
   │           │             │               │              │
   │           │── Response ─▶               │              │
   │           │             │────── chat ───────────────── ▶
   │           │             │◀──── redacted reply ─────────│
   │           │◀── redact ──│               │              │
   │◀──result──│             │               │              │
```

### 3.2 Design principles

- **Policy is external.** The gateway never interprets body contents
  beyond what is needed to route, cache, and base64-encode. All domain
  knowledge lives in the filter service.
- **Defaults are safe.** Omitting `contentFilter` is a no-op. A filter
  outage surfaces as `X-Content-Filter-Status: failed-open` and, by
  default, forwards the original body rather than dropping the call.
- **Every decision is observable.** No silent allow, no silent redact.
  Every invocation emits a metric and a response header; every redaction
  and reject emits an audit event.
- **Every operational knob is hot-reloadable.** Shadow mode, per-backend
  kill switch, cluster-wide kill switch, and sampling ratio all take
  effect on the next call after a CRD edit — no pod restart, no traffic
  draining.

---

## 4. API surface

### 4.1 Inline form

`MCPContentFilterConfig` attaches directly to a single
`MCPRouteBackendRef`. It is the form most operators reach for when the
filter is co-owned with the route.

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: MCPRoute
metadata:
  name: example
spec:
  backendRefs:
    - name: supportgpt
      contentFilter:
        url: https://content-filter.mcp.svc.cluster.local:8443/filter
        scopes: [Request, Response]
        timeoutSeconds: 10
        failurePolicy: PassThrough
        mode: Enforce
        enabled: true
        forwardHeaders:
          - x-request-id
          - x-tenant-id
```

### 4.2 Standalone form

`MCPContentFilter` is a top-level, Gateway-API-style policy object that
selects routes via `spec.targetRefs`. It is the analogue of
`BackendSecurityPolicy`: a platform or security team can author one
filter and attach it to routes owned by separate application teams
without editing the `MCPRoute` spec.

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: MCPContentFilter
metadata:
  name: corp-pii-filter
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: MCPRoute
      name: example
      sectionName: supportgpt # optional — scope to one backend
  url: https://content-filter.mcp.svc.cluster.local:8443/filter
  scopes: [Request, Response]
  failurePolicy: PassThrough
```

**Conflict resolution.** When a standalone `MCPContentFilter` targets an
(`MCPRoute`, backend) pair, the standalone form wins and any inline
`contentFilter` on that backend is ignored — identical to how
`BackendSecurityPolicy` overrides inline auth. If two standalone objects
target the same pair, the conflict is a configuration error: the
backend's filter collapses to `nil`, a `Conflicted` condition is set on
both objects' status, and traffic fails closed on policy ambiguity rather
than silently picking a "winner". Cross-namespace references are not
supported in v1.

### 4.3 Cluster-scoped policy ConfigMap

A single `MCPContentFilterPolicy` is distributed to the gateway in the
content-filter-policy ConfigMap. It carries the operational levers that
are not per-route:

```yaml
# content-filter-policy ConfigMap
globalDisable: false
```

`GlobalDisable` is the process-wide kill switch. When `true`, every
`MCPContentFilter` attached to any backend is short-circuited: the
gateway emits `X-Content-Filter-Status: disabled`, increments the
corresponding metric, and forwards the original body unchanged. Intended
for incident response.

The ConfigMap is reloaded hot; no gateway restart.

### 4.4 Field reference

| Field                      | Type / rule                                                                        | Default       | Purpose                                                                      |
| -------------------------- | ---------------------------------------------------------------------------------- | ------------- | ---------------------------------------------------------------------------- |
| `url`                      | `string`, matches `^https?://.+$`, ≤ 1024 chars                                    | —             | Filter endpoint. `file://` / `unix://` etc. are rejected at CRD admission.   |
| `scopes`                   | set of `{Request, Response}`, 1–2 items                                            | —             | Which phases invoke the filter.                                              |
| `timeoutSeconds`           | int in `[1, 120]`                                                                  | `10`          | Per-invocation wall-clock deadline.                                          |
| `failurePolicy`            | enum `PassThrough` \| `Fail`                                                       | `PassThrough` | Behaviour when a verdict cannot be obtained (timeout / 5xx / malformed).     |
| `forwardHeaders`           | list of header names, ≤ 16                                                         | `[]`          | Allowlist of request headers copied into the filter envelope.                |
| `mode`                     | enum `Enforce` \| `Shadow`                                                         | `Enforce`     | Whether verdicts are applied or merely recorded.                             |
| `enabled`                  | bool                                                                               | `true`        | Per-backend kill switch; preserves the rest of the spec for quick re-enable. |
| `shadowSampleRatePermille` | int in `[0, 1000]`                                                                 | `1000`        | Sampling budget for shadow-mode invocations, in permille.                    |
| `spec.targetRefs[]`        | 1–16 entries targeting `aigateway.envoyproxy.io/MCPRoute` (optional `sectionName`) | —             | Standalone-form attachment surface. Same namespace as the target route.      |

### 4.5 Wire protocol

```json
// gateway → filter
{
  "route":       "example",
  "backend":     "supportgpt",
  "scope":       "Request",
  "mcpMethod":   "tools/call",
  "tool":        "lookup",
  "headers":     { "x-tenant-id": "acme" },
  "bodyBase64":  "...",
  "contentType": "application/json"
}

// filter → gateway
{
  "action":     "redact",
  "bodyBase64": "...",
  "reason":     "removed PII from result"
}
```

- `action` ∈ `{pass, redact, reject}`.
- `redact` requires `bodyBase64` (≤ 2 MiB after decode; larger is
  rejected by the gateway as a filter failure).
- `reject` surfaces the `reason` string to the client via a JSON-RPC
  error.
- A malformed response is treated identically to a 5xx from the filter
  and triggers `FailurePolicy`.

### 4.6 Response surface on the client side

- Header `X-Content-Filter-Status` is set on every proxied `tools/call`
  response. Canonical values: `pass | redact | reject | failed-open |
unavailable | disabled | shadow_would_{pass,redact,reject,fail} |
shadow_sampled_out`.
- JSON-RPC error codes:
  - `-32010` — filter explicitly rejected the call (`action: reject`).
  - `-32011` — `failurePolicy: Fail` and the filter was unavailable or
    produced an unusable verdict.
  - All other JSON-RPC errors continue to come from the backend.

No other on-wire additions.

---

## 5. Runtime and reliability

All runtime logic lives in `internal/mcpproxy/` and is dispatched by a
hot-swappable `Dispatcher` pointer so configuration changes do not
require a proxy restart. The dispatcher owns the compiled
`contentFilters` map; the request hot path takes a stable pointer
snapshot at the start of each invocation and never re-reads.

Two narrow invocation points:

- `handleToolCallRequest` invokes the Request scope after tool-name
  validation and session lookup, before the call is forwarded to the
  backend.
- `proxyResponseBody` invokes the Response scope after the backend
  replies, before the body is streamed back to the client.

Both sites honour `FailurePolicy`, emit `X-Content-Filter-Status`, and
record the same metrics. Request-scope outcomes are stashed on the
request context so the response handler can emit one coherent status
even when only one scope is configured.

### 5.1 Per-invocation pipeline

```text
  admission gate ──▶ cache lookup ──▶ circuit breaker check ─┐
                                                             ▼
                                                         HTTP call
                                                             │
                                              ┌──── hedge ───┤
                                              ▼              ▼
                               parse / validate ◀──── response body
                                              │
                                              ▼
                                     action dispatch
                                       (pass / redact / reject)
                                              │
                                              ▼
                                    metrics + audit log
```

### 5.2 Reliability primitives

Each primitive is local to the proxy and operates without coordination
with the filter service:

- **Admission gate.** Process-global buffered semaphore bounds concurrent
  filter calls across all routes and backends. Protects the filter
  service from a concurrent-connection stampede.
- **Sharded LRU cache.** 16 shards with per-shard mutex, keyed by a hash
  of `(route, backend, scope, body)`. Per-shard byte budget evicts
  proactively to keep RSS bounded under load. Stable-hash sharding
  ensures the fan-out across shards is flat.
- **Circuit breaker.** Three-state breaker with exponential backoff,
  tracking consecutive failures per `(route, backend)`.
- **Hedged retries.** After a configurable `HedgeAfter` budget, a second
  request is fired; the first complete response wins. Bounded by the
  per-invocation timeout and the admission gate.
- **Cardinality guard.** All metric labels are bounded; once a label set
  exceeds capacity, further distinct values collapse to an `_overflow_`
  sentinel so a misbehaving caller cannot blow up the metric store.
- **Atomic dispatcher swap.** CRD or ConfigMap updates are compiled into
  a fresh `contentFilters` map and published via a single atomic pointer
  swap. In-flight requests continue on the old snapshot; new requests
  pick up the new one without coordination.

### 5.3 Precedence

Knobs are checked in strict order on every call. The first that
short-circuits wins; no downstream check runs.

```text
  incoming tools/call
           │
           ▼
  ┌──────────────────┐   true   ┌────────────────────┐
  │  GlobalDisable?  │─────────▶│ status = disabled  │
  └────────┬─────────┘          └────────────────────┘
           │ false
           ▼
  ┌──────────────────┐   false  ┌────────────────────┐
  │  Enabled?        │─────────▶│ status = disabled  │
  └────────┬─────────┘          └────────────────────┘
           │ true
           ▼
  ┌──────────────────┐  Shadow  ┌────────────────────┐
  │  Mode?           │─────────▶│ sample → invoke →  │
  │                  │          │ forward ORIGINAL   │
  └────────┬─────────┘          └────────────────────┘
           │ Enforce
           ▼
  ┌──────────────────┐
  │ invoke filter    │
  │ apply verdict    │
  │ emit metric +    │
  │ audit event      │
  └──────────────────┘
```

### 5.4 Sampling

`ShadowSampleRatePermille` (0..1000) bounds the fraction of shadow-mode
invocations that actually hit the filter. It is expressed in permille so
operators can set 0.1 % granularity on high-traffic backends without
switching to floating point. Sampling happens **before** the filter is
contacted, so the knob is a hard cost and latency budget. The sampling
decision uses `crypto/rand` and is ignored in `Enforce` mode (enforcement
of only a fraction of calls would leak content intermittently and defeat
the purpose of filtering).

---

## 6. Observability

The gateway emits a small, policy-agnostic set of series. Richer
policy-specific metrics (PII anonymisation outcomes, LLM call counts,
cache hit rates) are the filter service's responsibility.

| Metric                       | Type    | Labels                          | Meaning                                                                                |
| ---------------------------- | ------- | ------------------------------- | -------------------------------------------------------------------------------------- |
| `mcp_filter_decisions_total` | counter | `route, backend, scope, action` | Verdicts applied — `pass`, `redact`, `reject`, `shadow_would_*`, `shadow_sampled_out`. |
| `mcp_filter_status_total`    | counter | `route, backend, status`        | Values emitted to the `X-Content-Filter-Status` header.                                |
| `mcp_filter_inflight`        | gauge   | `route, backend`                | Current in-flight filter invocations per backend.                                      |

All three vectors pass through the cardinality guard
([§ 5.2](#52-reliability-primitives)).

On top of the metrics, every redaction and every reject produces a
`RedactionAuditEvent` on a dedicated `slog` handler, so operators can
route compliance traffic to a separate sink without reshaping the main
application log. Trace context is propagated via the W3C `traceparent`
header to both the filter and the eventual backend, so a tool call is a
single trace from agent to filter to backend.

---

## 7. Security considerations

- **Scheme restriction.** `url` admits only `http://` or `https://`;
  `file://`, `unix://`, `data://` are rejected at CRD admission.
- **Bounded body.** The gateway refuses a filter response larger than
  2 MiB, preventing a malicious filter from exhausting pod memory.
- **Header allowlist.** No header is forwarded to the filter unless the
  operator explicitly names it in `forwardHeaders`. The CRD field
  description carries a strong warning against listing `Authorization`,
  `Cookie`, or similar credential-bearing headers unless the filter is
  in-scope for them.
- **Fail-closed opt-in.** `FailurePolicy: Fail` exists for regulated
  workloads where an unscanned response is worse than a user-visible
  failure.
- **Same-namespace targets.** Standalone `MCPContentFilter` objects must
  live in the same namespace as the target `MCPRoute`. This keeps policy
  attachment decisions visible to the namespace's RBAC surface.
- **PII-safe logging.** All filter-related log lines route through a
  safe-logging helper that strips bodies and replaces them with
  `(size, content-type)` summaries.

---

## 8. Alternatives considered

### 8.1 In-process plugin ABI (Go / Wasm)

Embedding filter code in the gateway process would minimise latency, but
it couples operator-owned policy to gateway release cadence, complicates
the supply chain (signing, SBOM, vetting), and violates a standing
project non-goal. A Wasm-based variant is left in
[§ 13 Open questions](#13-open-questions) but is not part of v1.

### 8.2 Generic Envoy `ext_proc`

`ext_proc` works on HTTP bodies. Using it would require the filter to
re-parse JSON-RPC, re-implement MCP session handling, and re-derive
per-backend policy — duplicating concerns the MCP proxy already owns.
The per-backend identity is the whole point of this feature, and
`ext_proc` has no native concept of "which MCP backend is this call
routed to?".

### 8.3 Sidecar-per-pod filter

A sidecar co-located with each gateway pod would make latency
predictable but forces the operator to own container lifecycle for every
filter, defeats filter-service scaling decisions (filter pods and
gateway pods do not need the same replica count), and multiplies the
blast radius of a filter restart. The proposed design supports this
topology — a sidecar is just a filter URL that happens to resolve to
`127.0.0.1` — but does not require it.

### 8.4 Policy embedded in each MCP backend

Forcing every backend to implement the policy itself is an auditing
nightmare: every new backend owner re-negotiates the policy. The point
of the gateway is to be the single chokepoint that makes that audit
scalable.

---

## 9. Validation

The design and implementation have been exercised end-to-end on a
contributor fork against live MCP backends (SupportGPT, Atlassian,
NuRAG, Glean) behind one `MCPRoute`. Two sets of runs are relevant:

- **Correctness.** Four representative tools per backend driven with a
  replay corpus demonstrate that (a) Request-scope and Response-scope
  invocations are independent, (b) `pass` / `redact` / `reject` all
  flow through the JSON-RPC envelope correctly, and (c) a shadow-mode
  flip to enforce produces no additional call-path changes — the only
  observable difference is which body is forwarded.
- **Safety under degraded filter.** With the filter service
  deliberately degraded (one connection-refused path, one 5xx path, one
  timeout path), the circuit breaker opens as expected, subsequent
  calls short-circuit to fail-open under `PassThrough`, and
  `failurePolicy: Fail` surfaces `-32011` to the client. The gateway
  itself did not drop any requests or crash in either mode.

The on-wire and observability surfaces presented here are exactly the
surfaces that have been exercised; nothing in this proposal is
speculative.

---

## 10. Operational modes

### 10.1 Enforce

The production mode. `pass` forwards the original body; `redact` swaps
in the filter-supplied replacement; `reject` returns JSON-RPC error
`-32010` whose message is the filter-supplied `reason`.

### 10.2 Shadow

The default rollout vehicle. The filter is invoked normally, but the
gateway always forwards the original body. The verdict surfaces through
three channels: `X-Content-Filter-Status: shadow_would_{pass,redact,
reject,fail}`, a `mcp_filter_decisions_total` counter with
`action="shadow_would_*"`, and a `RedactionAuditEvent`. Flipping `mode`
from `Shadow` to `Enforce` is a CRD edit; the dispatcher hot-swaps on
the next invocation.

### 10.3 Failure policies

| `failurePolicy`         | On filter outage                                                            | When to pick it                                                                                              |
| ----------------------- | --------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `PassThrough` (default) | Log, emit `X-Content-Filter-Status: failed-open`, forward original body.    | Workloads where a tool call must always complete. Accepts the risk of an unredacted window during an outage. |
| `Fail`                  | Log, emit `X-Content-Filter-Status: unavailable`, return JSON-RPC `-32011`. | Regulated or evaluation workloads where an unscanned response is strictly worse than no response.            |

### 10.4 Kill switches

- `MCPContentFilter.Enabled = false` — per-backend. Filter is skipped;
  `X-Content-Filter-Status: disabled` on the response.
- `MCPContentFilterPolicy.GlobalDisable = true` — cluster-wide. Every
  `MCPContentFilter` in the cluster is skipped in the same way.
  Intended for incident response.

---

## 11. Reference implementation

The upstream PRs in this series ship only what the gateway itself
needs: the CRDs, the dispatcher runtime, the wire contract, and a
**passthrough** conformance target — a minimal filter service that
unconditionally returns `action: pass` so operators can verify the wire
is healthy without paying any policy cost. The passthrough service is
deliberately trivial: ~200 lines, no dependencies beyond
`net/http`, one HTTP handler, one health endpoint.

Production-grade filter implementations — LLM-backed semantic
redactors, PII engines with NER models, backend-specific field strippers —
are deliberately **out-of-tree**. Keeping the gateway policy-agnostic
means the reference implementations can be authored, released, and
versioned separately, and multiple community implementations can
coexist without forcing the upstream repository to pick a winner.

A complete policy-rich reference implementation (orchestrator +
four backend-specific handlers + LLM-based evaluator + NER-based PII
anonymizer) is available at `nutanix-core/panacea-agent`'s
`services/aigw-content-filter/` tree and documented in a companion
design note; it is offered as one concrete example of what can be
built against this contract, not as the blessed implementation.

---

## 12. Rollout plan

Upstream landing is sequenced so each PR is independently reviewable
and independently revertible.

| PR    | Scope                                                                                    | Targets                                                                                                                                                                                                       |
| ----- | ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **1** | CRDs + controller + filter runtime + passthrough conformance target + unit tests         | `api/v1alpha1/mcp_route.go`, `internal/controller/`, `internal/mcpproxy/contentfilter*.go`, `internal/filterapi/mcp_content_filter_policy.go`, `tests/crdcel/testdata/mcpgatewayroutes/content_filter_*.yaml` |
| **2** | Shadow mode, sampling, kill switches + integration tests exercising the precedence table | `internal/mcpproxy/contentfilter_shadow.go`, same translator path                                                                                                                                             |
| **3** | E2E test + user-facing docs + example manifests                                          | `tests/e2e/mcp/content_filter_*`, `site/docs/capabilities/mcp/content-filter.md`, `examples/content-filter/*`                                                                                                 |

Each PR is preceded by a link back to this proposal. The prototype
branch that backs all three is available on the contributor fork at
`advaith-shesh73/ai-gateway`, branch `feat/mcp-content-filter`.

The out-of-tree reference implementation that validates the contract is
released on its own cadence and is not a dependency of this upstream
contribution.

---

## 13. Open questions

We have intentionally stopped short of answering the following; they
are listed here to solicit maintainer direction before v1.1.

### 13.1 Multiple filters per backend (the question this doc opens)

**Current design:** exactly one filter per `(MCPRoute, backend)` pair.
Either via inline `contentFilter`, or via a standalone
`MCPContentFilter` whose `targetRefs` selects it. A second match is a
configuration error and the backend fails closed on policy ambiguity.

**The question.** Do operators need to compose **multiple independent
filters** on the same backend — for example, an org-wide PII filter
authored by the security team composed with a per-route evaluation
filter authored by the data-science team?

Several shapes are on the table:

| Shape                                                                                                                                                                            | Pro                                                                                                          | Con                                                                                                                                                                                                              |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **(A) Keep singular.** Operators compose inside one filter service.                                                                                                              | Simplest mental model. Pipeline stays a straight line; one timeout, one breaker, one audit event per call.   | Forces cross-team coordination on a single deployment. Two teams with different release cadences share one CI pipeline and one pod.                                                                              |
| **(B) List on backendRef.** `MCPRouteBackendRef.contentFilters` is a list; filters run in declared order, aggregate verdict = most-restrictive-wins.                             | Low API surface area — one field change, same CRD. Composition is visible at a glance on the route.          | Aggregate verdict semantics get subtle: whose `reason` string wins on reject? Does the second filter see the first's redaction or the original body? Per-stage failure composition multiplies the decision tree. |
| **(C) Standalone stacking.** Multiple `MCPContentFilter` objects may target the same pair; explicit `spec.order` field disambiguates; aggregate verdict = most-restrictive-wins. | Platform team and application team each own their own object. Mirrors Gateway-API HTTPRoute filter ordering. | Status reporting when N filters target one pair is complex; Gateway-API's spec for `targetRefs` precedence on policy objects is still evolving.                                                                  |
| **(D) Dedicated chain CRD.** `MCPContentFilterChain` owns an ordered list; `MCPRouteBackendRef.contentFilterChain` or `MCPContentFilter.chainRef` references it.                 | Explicit. Decouples ordering from attachment. Works whether the chain is inline or standalone.               | A whole new CRD for what may be a narrow need. Status reporting on chain entries needs its own model.                                                                                                            |

Sub-questions that fall out of any non-(A) choice:

- **Ordering semantics.** Fixed-by-index (operator declares), or by some
  attribute on the filter (priority / class / annotation)? Does a
  standalone filter always win over an inline one, or does it depend on
  declared order?
- **Aggregate verdict rule.** Most-restrictive-wins is intuitive but
  hides the provenance of a `reject` — the client sees one reason
  string. Do we surface the winning stage in a new response header
  (`X-Content-Filter-Stage`), an audit field, or both?
- **Scope composition.** Can two filters attach to the same backend at
  the same scope (e.g., both on Response), or must distinct filters
  carve scopes between themselves?
- **Failure composition.** If stage 2 fails under `PassThrough` after
  stage 1 redacted, is the body the redacted-by-1 version or the
  original? The gateway is stateless across stages today; v1.1 needs
  a clear answer.
- **Observability.** `mcp_filter_decisions_total` currently has a
  `(route, backend, scope, action)` label set. A chain shape needs a
  `stage` label; that interacts with the cardinality guard.
- **Short-circuiting.** When stage 1 returns `reject`, does stage 2
  still run for observability? If yes, how is its verdict reported?

**Request for feedback.** Shape (A) — stay singular — is what this
proposal ships. Shape (B) is the smallest forward-compatible extension;
shape (C) follows Gateway-API precedent; shape (D) is the most
expressive. We are asking the community whether singular is enough for
v1 and, if not, which shape aligns best with Envoy AI Gateway's
broader direction on policy attachment.

### 13.2 Stream-scope filtering

v1 operates on `tools/call` bodies only. Long-lived SSE notification
streams and server-to-client JSON-RPC requests carry their own payloads
that could in principle also be filtered. Is this a v1.1 scope, or a
separate proposal? If the former, what is the wire shape — does the
filter see one envelope per chunk, one envelope per message, or a
connection-scoped subscription?

### 13.3 Transport

Wire is HTTP + JSON today. Would gRPC streaming materially reduce the
base64 envelope cost, and is the resulting CRD split (`protocol: HTTP |
GRPC`) worth the API surface increase?

### 13.4 Stateful filters

Some policies — for example, eval exclusion keyed on a session-level
ticket ID — benefit from filter-local state keyed by something other
than the per-call envelope. Should the wire carry a stable
`sessionToken` that operators can opt into, or do `forwardHeaders`
suffice?

### 13.5 Wasm variant

An in-process Wasm filter was rejected in favour of HTTP for v1
([§ 8.1](#81-in-process-plugin-abi-go--wasm)). Is there a v1.x variant
worth revisiting for latency-critical deployments that still avoids
coupling to the gateway release cycle?

### 13.6 Cross-namespace targets

Standalone `MCPContentFilter` objects must live in the same namespace
as their target route today. Platform teams that sit in a dedicated
namespace have asked for cross-namespace attachment; is this worth the
RBAC surface area, and how does it interact with Gateway-API's evolving
`ReferenceGrant` story?

---

## 14. Future work

In-tree hooks exist for the items below; the platform-side decisions
they depend on are out of scope for this proposal.

- **Shadow transcript sinks.** Shadow-mode verdicts are currently
  recorded on metrics + audit log only. A production rollout may want
  to retain the full would-be-redacted body for diff analysis. This
  requires an approved retention policy, a durable sink (audit-grade
  DB, object storage, or SIEM), and a schema versioning story for the
  audit event shape.
- **Canary routing.** Shadow mode answers "is the filter correct?" but
  not "is its latency acceptable?" under enforce. A canary mode that
  sends a cohort of real traffic through `Enforce` while the rest stays
  in `Shadow` is deferred until a Gateway-API-aligned cohort selector
  is agreed (header / percent / tenant).
- **Per-endpoint circuit breaker.** Sharding the breaker by upstream
  pod IP so one flaky filter pod does not open the breaker for healthy
  pods on the same endpoint. The right home for this may be Envoy's
  native outlier detection rather than the Go-side breaker.
- **Streaming chunk splitter.** Current measurement shows p99 well
  under the ceiling on batched fan-out, so parked until a production
  profile indicates otherwise.

---

## References

- Proposal [006 — MCP Gateway][proposal-006]
- Proposal [009 — Quota-Aware Routing][proposal-009]
- User-facing capability docs: [MCP / Content Filter][docs-capabilities]
- API reference: [`MCPContentFilter` in `api/v1alpha1/mcp_route.go`][api-ref]
- CRD fixtures: [`tests/crdcel/testdata/mcpgatewayroutes/content_filter_*.yaml`][fixtures]

[proposal-006]: ../006-mcp-gateway/proposal.md
[proposal-009]: ../009-quota-aware-routing/proposal.md
[docs-capabilities]: ../../../site/docs/capabilities/mcp/index.md
[api-ref]: ../../../api/v1alpha1/mcp_route.go
[fixtures]: ../../../tests/crdcel/testdata/mcpgatewayroutes/
