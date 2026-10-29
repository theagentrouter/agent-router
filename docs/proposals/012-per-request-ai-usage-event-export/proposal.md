# Table of Contents

<!-- toc -->

- [Summary](#summary)
- [Motivation and Use Cases](#motivation-and-use-cases)
- [Goals](#goals)
- [Non-goals](#non-goals)
- [Design](#design)
  - [UsageEvent](#usageevent)
    - [Example payloads](#example-payloads)
  - [HTTP Sink](#http-sink)
  - [Configuration](#configuration)
  - [Deployment Topology](#deployment-topology)
  - [Observability](#observability)
- [Implementation Plan](#implementation-plan)
- [Risks](#risks)
- [Future work](#future-work)
- [Alternatives Considered](#alternatives-considered)

<!-- /toc -->

---

# Summary

Envoy AI Gateway already exposes per-request AI usage through metrics, traces, and access logs. These observability surfaces are intentionally best-effort and are well suited for dashboards, alerting, debugging, and capacity planning. They do not provide any indication of whether an individual usage record was successfully delivered to another system.

Some deployments require a per-request usage record with delivery acknowledgement for downstream accounting, attribution, reconciliation, or audit workflows. Today, operators must reconstruct these records from best-effort telemetry, where loss is undetectable.

This proposal adds a normalized `UsageEvent` export interface. A `UsageEvent` is emitted once per completed AI request and synchronously published through a pluggable `UsageEventSink`. The sink acknowledges receipt, allowing the gateway to distinguish exported events from dropped events. The gateway remains stateless: it stores no events, performs no retries, and makes no guarantees beyond sink acknowledgement.

The initial implementation provides an HTTP sink. Additional transports can be implemented independently without changing the gateway API.

---

# Motivation and Use Cases

Existing observability mechanisms are well suited for monitoring, debugging, alerting, and capacity planning. They are intentionally best-effort and do not indicate whether an individual usage record was successfully delivered to another system.

Some downstream workflows require more than best-effort telemetry. They need to know whether a per-request usage record was successfully exported so that missing records can be detected and acted upon.

Examples include:

- **Multi-tenant attribution** — allocating shared inference capacity across tenants, teams, or research groups.
- **Provider reconciliation** — comparing gateway-observed token usage with provider-reported usage and investigating discrepancies.
- **Incident forensics** — reconstructing request-level activity during outages or runaway consumption events where sampled telemetry is insufficient.
- **Abuse and anomaly detection** — detecting request-level usage patterns where missing records reduce detection accuracy.
- **Capacity attribution** — allocating self-hosted GPU or accelerator usage to the workloads that consumed them.
- **Quota and budget systems** — downstream systems that consume completed request records to enforce organizational policies.
- **Offline analytics** — exporting normalized usage records into data warehouses or lakehouses for reporting, cost analysis, model evaluation, and operational insights.
- **Usage accounting** — downstream systems may use exported events for showback, chargeback, or commercial metering.

This proposal does not implement any of these workflows within Envoy AI Gateway. Instead, it provides a normalized per-request `UsageEvent` together with acknowledgement of whether that event was successfully exported, enabling external systems to build these capabilities while keeping the gateway stateless and vendor-neutral.

---

# Goals

- Emit one normalized `UsageEvent` for every completed AI request.
- Provide explicit acknowledgement of event delivery.
- Make event loss observable through metrics.
- Keep Envoy AI Gateway stateless.
- Keep the event schema stable and additive.
- Keep business logic, pricing, billing, and storage outside the gateway.

---

# Non-goals

- Durable storage.
- Billing or pricing.
- Retry queues.
- Policy enforcement.
- Delivery gating.
- Local event persistence.
- Prompt or response logging.

---

# Design

A `UsageEvent` is constructed from metadata the gateway already maintains on the response path.

After construction, the event is synchronously published through a `UsageEventSink`.

```go
type UsageEventSink interface {
    Publish(ctx context.Context, event UsageEvent) error
}
```

If publication succeeds, the event is counted as exported.

If publication fails or times out, the event is counted as dropped and request processing continues normally. The gateway intentionally does not retry, buffer, or persist events after a failed publication. Event loss is surfaced through metrics so operators can detect failures and alert accordingly.

## UsageEvent

A `UsageEvent` represents a single completed AI request. It is constructed from request and response metadata already available within the gateway.

Every request reaching a terminal response emits an event. When the provider returns no usage, token fields are zero.

The schema is designed to be stable and additive. Fields may be added over time, but existing fields will not change semantics or be removed.

A representative structure is shown below:

```go
// UsageEvent is the normalized per-request AI usage record emitted by the gateway.
type UsageEvent struct {
    // SchemaVersion allows consumers to detect breaking changes.
    // Fields are additive-only within a version.
    SchemaVersion string `json:"schema_version"`

    // Stable identifier for deduplication.
    EventID string `json:"event_id"`

    // Unix timestamp in milliseconds when the event was emitted.
    EmittedAt int64 `json:"emitted_at"`

    // Stable request identifier.
    RequestID string `json:"request_id"`

    // Gateway-authoritative request outcome.
    Status string `json:"status"`

    // HTTP status returned to the client.
    StatusCode int `json:"status_code"`

    // Upstream provider.
    Provider string `json:"provider"`

    // Selected backend.
    Backend string `json:"backend"`

    // Model requested by the client.
    ModelRequested string `json:"model_requested"`

    // Model returned by the provider.
    ModelResponse string `json:"model_response"`

    // Usage reported by the provider.
    InputTokens int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
    CachedInputTokens int `json:"cached_input_tokens"`
    CacheWriteInputTokens int `json:"cache_write_input_tokens"`
    ReasoningTokens int `json:"reasoning_tokens"`

    // Optional attribution metadata.
    Attributes map[string]string `json:"attributes,omitempty"`
}
```

`event_id` is derived from the gateway-generated request UUID, route, and backend. It remains stable for that event; receivers may use it for deduplication, but the gateway does not persist IDs across process restarts.

The schema intentionally excludes prompt and response content.

### Example payloads

An OpenAI reasoning request with a cache hit. The provider's `prompt_tokens` was 130, of which 10 were cache reads, so `input_tokens` is the remaining 120 uncached input:

```json
{
  "schema_version": "v1",
  "event_id": "req-abc123|llmroute|openai-primary",
  "emitted_at": 1753961025123,
  "request_id": "req-abc123",
  "status": "succeeded",
  "status_code": 200,
  "provider": "openai",
  "backend": "openai-primary",
  "model_requested": "o4-mini",
  "model_response": "o4-mini",
  "input_tokens": 120,
  "output_tokens": 480,
  "cached_input_tokens": 10,
  "cache_write_input_tokens": 0,
  "reasoning_tokens": 320,
  "attributes": {
    "tenant.id": "tenant-a",
    "user.id": "user-123"
  }
}
```

An Anthropic request that both writes and reads the prompt cache. Cache counts are reported separately from `input_tokens`:

```json
{
  "schema_version": "v1",
  "event_id": "req-def456|llmroute|anthropic-primary",
  "emitted_at": 1753961026777,
  "request_id": "req-def456",
  "status": "succeeded",
  "status_code": 200,
  "provider": "anthropic",
  "backend": "anthropic-primary",
  "model_requested": "claude-sonnet-4",
  "model_response": "claude-sonnet-4",
  "input_tokens": 45,
  "output_tokens": 210,
  "cached_input_tokens": 1024,
  "cache_write_input_tokens": 2048,
  "reasoning_tokens": 0,
  "attributes": {
    "tenant.id": "tenant-b",
    "user.id": "user-456"
  }
}
```

## HTTP Sink

The initial implementation provides an HTTP sink.

The HTTP sink accepts serialized `UsageEvent` records and returns success only after the event has been accepted according to the receiver's durability policy.

The HTTP sink is intentionally transport-agnostic. Implementations may forward events to databases, message queues, event buses, or other systems. Those forwarding semantics are outside the scope of this proposal.

Future sink implementations may use different transports while implementing the same `UsageEventSink` interface.

The proposal standardizes only the gateway-facing `UsageEventSink` abstraction and the `UsageEvent` schema. The behavior of downstream systems, including durability guarantees, retries, persistence, routing, and protocol-specific semantics, is intentionally left to sink implementations.

## Configuration

- `--usage-events-http-url` — Specifies the HTTP endpoint where usage events are published.
- `--usage-events-timeout-ms` — Sets the publication timeout. Exceeding it is treated as a failed export.
- `--usage-events-attributes` — Configures additional allowlisted key-value attributes to include with each `UsageEvent`.

## Deployment Topology

The sink is an HTTP endpoint, so where the receiver runs is an operator decision. Two patterns are worth describing, because they trade off differently against the synchronous publish budget.

**Colocated receiver (sidecar or DaemonSet, addressed over loopback).** Publication is a sub-millisecond local call, so the timeout budget is rarely reached. If the receiver writes to a local buffer before acknowledging, the event also survives an extproc crash, since the receiver is a separate process. In plain terms this is a log-shipping pipeline that answers back: the architecture is not being replaced, acknowledgement is the piece being added to it.

**Remote receiver (cluster Service or external endpoint).** Simpler to operate as a shared service, and appropriate where the consumer already runs a central ingest tier. The tradeoff is that receiver latency and availability sit on the request path up to the configured timeout, so `--usage-events-timeout-ms` should be sized against observed receiver latency and publication latency monitored.

In either topology the receiver should acknowledge only after meeting its own durability policy. An acknowledgement returned earlier removes the only property this path has over access logs.

## Observability

The gateway exposes metrics to track event export behavior:

- Total events constructed.
- Successfully exported events.
- Dropped events due to publication failure or timeout.
- Publication latency.

These metrics allow operators to detect event loss, monitor sink health, and understand the impact of export on request latency.

---

# Implementation Plan

1. **Define the `UsageEvent` schema** — the flat record and its JSON serialization, with `schema_version` set to `v1`.
2. **Add the `UsageEventSink` interface** with an initial `http` implementation.
3. **Construct the event in ExtProc** — build the `UsageEvent` from request and response metadata already available on the response path, normalizing provider token usage into the non-overlapping input components (`input_tokens`, `cached_input_tokens`, `cache_write_input_tokens`, `reasoning_tokens`).
4. **Add the synchronous publisher** — single attempt with a bounded timeout; count the event as exported on acknowledgement and as dropped on any failure or timeout, then continue request processing unchanged.
5. **Add configuration flags** — `--usage-events-http-url`, `--usage-events-timeout-ms`, and `--usage-events-attributes`; the feature is disabled when no URL is configured.
6. **Add observability** — the constructed, exported, dropped, and publication-latency metrics.
7. **Ship a reference receiver** under `examples/` that acknowledges events and deduplicates on `event_id`.
8. **Add integration tests** — event construction, token normalization, attribute extraction, and sink behavior for success, failure, and timeout using an in-process HTTP receiver.
9. **Document the deployment trust boundary** — attribution-header handling and the colocated-receiver topology.

---

# Risks

## Request latency

Because publication occurs on the request path, enabling this feature adds latency to completed requests. The feature is disabled by default so operators can opt in only when acknowledgement semantics are required.

## Event loss

The gateway remains stateless and does not buffer or persist events for now. Publication failures are observable through metrics.

## Attribution trust boundary

Header-derived attribution depends on deployment configuration. Operators should expose only trusted metadata through configured allowlists.

---

# Future work

The following are natural extensions that build on the same `UsageEventSink` abstraction and schema without changing the gateway-facing API:

- **Additional sink implementations** — transports such as Kafka, gRPC, or cloud pub/sub messaging, implemented against the same `UsageEventSink` interface.
- **Sink circuit breaking** — fast-failing event publication after consecutive sink timeouts or failures to prevent latency degradation on the request path when a sink is unavailable.
- **Alternative attribution sources** — deriving attribution from authenticated identity or policy metadata rather than request headers alone.

---

# Alternatives Considered

## Access logs

Access logs are intentionally best-effort and provide no acknowledgement of successful delivery. This proposal addresses that specific limitation while preserving existing observability mechanisms.

## Asynchronous publication

Acknowledgement is only meaningful if publication completes before request processing finishes. An asynchronous export would improve latency but could not distinguish successfully exported events from events lost before transmission.

## Gateway-managed retries and failover sinks

To keep the initial implementation simple, the gateway neither retries, buffers, nor fails over to secondary sinks. Operators needing durable delivery, `zero event loss`, or dead-letter queues can use a durable receiver (such as a local sidecar collector) as the HTTP sink. Handling retries, local disk buffering, and secondary routing in the sink tier keeps the gateway proxy stateless and performant. Bounded retries, asynchronous delivery, secondary failover sinks, or short-lived in-memory buffering may be considered later if needed.
