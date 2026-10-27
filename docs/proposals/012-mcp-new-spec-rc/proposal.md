# MCP Spec Release Candidate (2026-07-28): Stateless Protocol Conformance

## Table of Contents

1. [Background and Motivation](#background-and-motivation)
2. [Current State](#current-state)

- 2.1 [Session-Based Architecture](#session-based-architecture)
- 2.2 [Method Dispatch and Routing](#method-dispatch-and-routing)
- 2.3 [Notification Streaming](#notification-streaming)
- 2.4 [Server-to-Client Requests](#server-to-client-requests)

3. [Spec Changelog Summary](#spec-changelog-summary)

- 3.1 [Major Changes (Breaking)](#major-changes-breaking)
- 3.2 [Minor Changes](#minor-changes)
- 3.3 [Deprecations](#deprecations)
- 3.4 [Pass-Through / No Gateway Work](#pass-through--no-gateway-work)
- 3.5 [Not Relevant to the Gateway](#not-relevant-to-the-gateway)

4. [Goals and Non-Goals](#goals-and-non-goals)
5. [Scope & Prioritization](#scope--prioritization)
6. [Terminology](#terminology)
7. [The Core Problem: Multiplexing Without Sessions](#the-core-problem-multiplexing-without-sessions)
8. [Compatibility Matrix](#compatibility-matrix)

- 8.1 [Cell 1 — Legacy Client ↔ Legacy Backend](#cell-1--legacy-client--legacy-backend)
- 8.2 [Cell 2 — Modern Client ↔ Modern Backend](#cell-2--modern-client--modern-backend)
- 8.3 [Cell 3 — Modern Client ↔ Legacy Backend](#cell-3--modern-client--legacy-backend)
- 8.4 [Cell 4 — Legacy Client ↔ Modern Backend](#cell-4--legacy-client--modern-backend)

9. [Design Decisions](#design-decisions)

- 9.1 [D1 — Downstream (Client) Era Detection](#d1--downstream-client-era-detection)
- 9.2 [D2 — Upstream (Backend) Era Detection](#d2--upstream-backend-era-detection)
- 9.3 [D3 — Capability / server/discover Cache](#d3--capability--serverdiscover-cache)
- 9.4 [D4 — MRTR Translation Strategy](#d4--mrtr-translation-strategy)
- 9.5 [D5 — Upstream Session Pool (Cell 3)](#d5--upstream-session-pool-cell-3)

10. [Phased Implementation Plan](#phased-implementation-plan)

- 10.1 [Phase 0 — Foundations + Modern Path (No Behavior Change)](#phase-0--foundations--modern-path-no-behavior-change)
- 10.2 [Phase 1 — Integration (Modern ↔ Modern Goes Live)](#phase-1--integration-modern--modern-goes-live)
- 10.3 [Phase 2–4: Deferred](#phase-24-deferred-documented-for-architectural-context)

11. [Spec Item → Phase Mapping](#spec-item--phase-mapping)
12. [Gotchas and Risks](#gotchas-and-risks)

## Background and Motivation

The [MCP specification Release Candidate (2026-07-28)](https://modelcontextprotocol.io/specification/draft/changelog) introduces the most significant protocol revision since the initial release. The overarching theme is **statelessness**: the `initialize` handshake, protocol-level sessions (`Mcp-Session-Id`), and server-initiated JSON-RPC requests are all removed. In their place, every request carries its own protocol version, client capabilities, and identity in `_meta` fields, and a new `server/discover` RPC replaces `initialize` for capability discovery. Server-to-client interactions are replaced by the Multi Round-Trip Requests (MRTR) pattern, and notification streaming moves from a standalone GET endpoint to `subscriptions/listen`.

Envoy AI Gateway's MCP proxy ([proposal 006](../006-mcp-gateway/proposal.md)) was built on the session-based `2025-06-18` protocol. The session is the central abstraction: it multiplexes multiple backend connections, carries per-backend capabilities, and enables notification merging and server-to-client request routing. The new spec removes this abstraction entirely.

This proposal defines how the gateway achieves conformance with the RC spec while maintaining full backward compatibility with legacy (`2025-06-18` / `2025-11-25`) clients and backends. It covers every item in the spec changelog, assigns each to an implementation phase, and provides the compatibility matrix for all client × backend era combinations.

**Tracking issue:** [envoyproxy/ai-gateway#2323](https://github.com/envoyproxy/ai-gateway/issues/2323)

## Current State

### Session-Based Architecture

Today, the MCP proxy uses an encrypted composite session ID as the backbone of its multiplexing design:

```
{route}@{subject}@{backend1}:{base64(sid1)}:{capHex1},{backend2}:{base64(sid2)}:{capHex2},...
```

On `initialize`, `newSession()` (`mcpproxy.go:156`) fans out an `initialize` request to every backend of the route, collects each backend's `Mcp-Session-Id` and capabilities into `compositeSessionEntry` values, and `clientToGatewaySessionIDFromEntries()` (`session.go`) packs them into a single encrypted client-facing session ID. Every subsequent request decrypts that ID (`sessionFromID`, `mcpproxy.go:255`) to recover:

- The route name
- The subject (for anti-hijack validation)
- Per-backend upstream session IDs
- Per-backend capabilities

### Method Dispatch and Routing

`servePOST` (`handlers.go:201`) requires a session ID for all requests except `initialize`. The method switch dispatches to handlers for `tools/call`, `tools/list`, `resources/read`, `prompts/list`, `ping`, `logging/setLevel`, and other JSON-RPC methods. Single-target calls (e.g., `tools/call`) use the `backend__toolName` prefix encoding to route to the correct backend. Fan-out calls (e.g., `tools/list`) iterate all backends in the session and merge results.

### Notification Streaming

`serveGET` (`handlers.go:134`) opens a long-lived SSE stream, fans out GET requests to all backends, and merges notification events using unified event IDs with `Last-Event-ID` resumability support.

### Server-to-Client Requests

Server-initiated requests (sampling, elicitation, roots) arrive on the backend SSE stream and are forwarded to the client with a modified JSON-RPC ID that encodes the backend name and original ID type. The client's response is decoded and routed back to the correct backend (`handleClientToServerResponse`, `handlers.go:606-700`).

## Spec Changelog Summary

The following tables classify every item from the [official changelog](https://modelcontextprotocol.io/specification/draft/changelog) by gateway impact.

### Major Changes (Breaking)

| Issue Ref | SEP / Change | Title                                                                                                                                                                                                                                                       | Gateway Impact                                                                                                                    |
| --------- | ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| #A1       | SEP-2567     | Stateless Streamable HTTP — remove `Mcp-Session-Id` header and HTTP `DELETE` session termination                                                                                                                                                            | Heavy — `session.go` composite session ID is the central abstraction; `handlers.go:329` requires session for all non-`initialize` |
| #A2       | SEP-2575     | Stateless protocol — remove `initialize`/`notifications/initialized` handshake, `ping`, `logging/setLevel`; add `server/discover` and `subscriptions/listen`; drop SSE resumability (`Last-Event-ID`)                                                       | Heavy — `handlers.go:364/384/472/1128`, `sse.go`, `session.go:194` notification merge                                             |
| #A3       | SEP-2322     | Multi Round-Trip Requests (MRTR) — `InputRequiredResult` with required `resultType` field; replaces server-initiated requests (sampling/elicitation/roots)                                                                                                  | Heavy — `handlers.go:606-700` server-to-client request ID encoding, `maybeServerToClientRequestModify`                            |
| #A4       | SEP-2575     | Per-request `_meta` fields — `io.modelcontextprotocol/protocolVersion` (required), `io.modelcontextprotocol/clientInfo` (required), `io.modelcontextprotocol/clientCapabilities` (required), `io.modelcontextprotocol/logLevel` (optional) on every request | Moderate — body injection in `invokeJSONRPCRequest`                                                                               |

### Minor Changes

| Issue Ref | SEP / Change | Title                                                                                                                                                                                            | Gateway Impact                                                                                  |
| --------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------- |
| #B1       | SEP-2549     | Cacheable results — `ttlMs` and `cacheScope` fields on `tools/list`, `prompts/list`, `resources/list`, `resources/read`, `resources/templates/list`                                              | Moderate — `handleToolsListRequest` aggregates across backends; proxy-level caching opportunity |
| #B2       | — (non-SEP)  | Deterministic `tools/list` ordering across aggregated backends                                                                                                                                   | Light — `handlers.go:1620` merge order                                                          |
| #C1       | SEP-2468     | Authorization response `iss` validation — authorization servers SHOULD include `iss` per RFC 9207; clients MUST validate                                                                         | Moderate — Envoy JWT verify (`mcproute.go:32`); gateway generates filter config                 |
| #C2       | SEP-2352     | Client credentials bound to issuing authz server — clients MUST key credentials by issuer, MUST NOT reuse across servers                                                                         | Moderate — `tokenprovider/`, `rotators/` persist credentials                                    |
| #D1       | SEP-2243     | Required `Mcp-Method` / `Mcp-Name` headers on Streamable HTTP POST + `x-mcp-header` for custom headers from tool parameters + reject on header↔body mismatch                                    | Moderate — `handlers.go` body-parse dispatch; enables intermediary routing without body parsing |
| #D2       | SEP-414      | OTel `_meta` trace-context conventions — formalize `traceparent`, `tracestate`, `baggage` keys                                                                                                   | Light (verify) — `tracing/mcp.go:76` already injects `_meta` trace context                      |
| #D3       | — (non-SEP)  | `extensions` field on `ClientCapabilities` and `ServerCapabilities`                                                                                                                              | Light — `session.go:647` merges field-by-field but drops `extensions`; needs fix                |
| #D4       | — (non-SEP)  | JSON-RPC error code renumbering + allocation policy: `-32002` → `-32602`, new codes `-32020` (HeaderMismatch), `-32021` (MissingRequiredClientCapability), `-32022` (UnsupportedProtocolVersion) | Moderate — `handlers.go` switches on `jsonrpcErr.Code`, mints own errors                        |
| #D5       | SEP-2575     | Remove `notifications/elicitation/complete` notification and `elicitationId` field                                                                                                               | Light — under MRTR, correlation is via `requestState`; gateway passes through                   |
| #D6       | SEP-2106     | JSON Schema 2020-12 for `inputSchema`/`outputSchema`; `structuredContent` may be any JSON; `$ref` resolution requirements                                                                        | Light — gateway doesn't validate schemas; opaque pass-through                                   |

### Deprecations

| Issue Ref | SEP / Change | Title                                                                                                         | Gateway Impact                                                                                                                  |
| --------- | ------------ | ------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| #E1       | SEP-2577     | Deprecate Roots, Sampling, and Logging features (12-month deprecation window; remain functional)              | Light — `handlers.go` handles all three today; keep working, add deprecation notes                                              |
| #E2       | SEP-2596     | Feature lifecycle policy — deprecate HTTP+SSE transport and `includeContext` values `thisServer`/`allServers` | Light — `sse.go` transport handling; no removal needed                                                                          |
| #E3       | SEP-2663     | Tasks moved to `io.modelcontextprotocol/tasks` extension                                                      | Light — no task handling today; extension pass-through only                                                                     |
| #E4       | PR #2858     | Deprecate Dynamic Client Registration in favor of Client ID Metadata Documents                                | Light — gateway only passes `registration_endpoint` in served metadata (`mcp_route_security_policy.go:642`); no DCR origination |

### Pass-Through / No Gateway Work

| SEP / Change | Title                        | Why No Work                                                                |
| ------------ | ---------------------------- | -------------------------------------------------------------------------- |
| SEP-2106     | JSON Schema 2020-12 adoption | Gateway doesn't validate `inputSchema`/`outputSchema`; opaque pass-through |
| SEP-2663     | Tasks extension              | No task handling today; JSON-RPC pass-through                              |

### Not Relevant to the Gateway

| SEP / Change | Title                                             | Why Excluded                                                                                            |
| ------------ | ------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| SEP-837      | `application_type` in Dynamic Client Registration | OAuth client-registration concern; gateway is a resource server / token validator, never originates DCR |
| PR #2858     | DCR → Client ID Metadata Documents (CIMD)         | Client-registration mechanism; gateway only passes `registration_endpoint` through in served metadata   |
| SEP-1850     | Formalized PR-based SEP workflow                  | Governance process only; no gateway code                                                                |

## Goals and Non-Goals

### Goals

- Achieve conformance with the MCP specification RC (`2026-07-28`) for the modern ↔ modern path.
- Maintain full backward compatibility with legacy (`2025-06-18`, `2025-11-25`) clients and backends.
- Support the full 2×2 compatibility matrix: legacy/modern clients against legacy/modern backends.
- Implement a phased rollout where each phase is independently shippable and leaves prior behavior intact.
- Cover every item in the spec changelog, with explicit phase assignment and justification.
- Enable future Envoy-native routing on `Mcp-Method`/`Mcp-Name` headers without body parsing.
- Preserve existing observability (metrics, tracing) across both protocol eras.

### Non-Goals

- This proposal does **not** change how clients authenticate to the gateway (`MCPRouteSecurityPolicy`).
- This proposal does **not** change the MCPRoute/MCPBackend CRD structure (see [proposal 011](../011-mcp-backend-crd/proposal.md)).
- This proposal does **not** introduce a distributed session store (Redis, etc.) for the modern path.
- This proposal does **not** remove support for deprecated features (Roots, Sampling, Logging) within the 12-month deprecation window.
- This proposal does **not** add stdio transport support.

## Scope & Prioritization

> **Review decision:** Focus on **Phase 0 and Phase 1** first. The official MCP spec release is imminent (`2026-07-28`), so delivering modern↔modern conformance is the priority. Cross-era translation (Phase 2) can wait until modern clients actually emerge and legacy backends remain common — today most new backends will ship modern from day one.

### Immediate Scope (This Proposal Delivers)

| Phase       | Objective                                                                                             | Outcome                                                        |
| ----------- | ----------------------------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| **Phase 0** | Build the modern path as additive, unreferenced code staged along the request lifecycle (PRs 0.1–0.4) | Zero behavior change; all existing tests pass; unlocks Phase 1 |
| **Phase 1** | Activate era dispatch — Modern↔Modern (Cell 2) goes live; Legacy↔Legacy (Cell 1) unchanged          | Spec-conformant gateway for modern clients and modern backends |

### Deferred (Future Proposals)

| Phase   | Objective                                                   | Rationale for Deferral                                                                                                |
| ------- | ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Phase 2 | Cross-era translation (Cells 3 & 4)                         | No modern clients exist yet; legacy backends will upgrade organically. Revisit when mixed-era deployments become real |
| Phase 3 | Cacheable results, deterministic ordering, minor spec items | Optimization layer; not required for conformance                                                                      |
| Phase 4 | Auth hardening, deprecations, cleanup                       | Non-urgent; deprecation window is 12 months                                                                           |

The design decisions (D1–D5) and compatibility matrix in this document remain the full architecture vision. Phases 2–4 are intentionally kept here as reference for when they become relevant.

## Terminology

Following the spec's terminology:

- **Modern**: protocol versions that convey version, identity, and capabilities as per-request metadata (revision `2026-07-28` and later).
- **Legacy**: protocol versions that establish a session with an `initialize` handshake (`2025-11-25` and earlier).
- **Dual-era**: an implementation that supports both modern and legacy versions. The gateway will be dual-era.
- **MRTR**: Multi Round-Trip Requests — the pattern replacing server-initiated JSON-RPC requests.
- **Era**: whether a client or backend speaks legacy or modern protocol.

## The Core Problem: Multiplexing Without Sessions

Today, the encrypted session ID carries everything the proxy needs for stateless (from the proxy process's perspective) request routing: the route name, the subject, per-backend session IDs, and per-backend capabilities. In the `2026-07-28` spec, there is no session ID. The proxy must source each piece from elsewhere:

| Information Previously in Session   | Stateless Replacement                                                                                                                                                                                                                                                    |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Route name**                      | Already arrives on **every** request in the `x-ai-eg-mcp-route` header (`internalapi.MCPRouteHeader`), set by the Envoy frontend `HTTPRouteFilter`. Today read only on `initialize` (`handlers.go:376`); must be read on every request.                                  |
| **Backend for single-target calls** | Already encoded in the **namespaced name/URI**: `downstreamResourceName` → `backend__tool`, `downstreamResourceURI` → `backend+scheme://…` (`handlers.go:1204-1243`). `upstreamResourceName`/`upstreamResourceURI` decode it. Now also appears in the `Mcp-Name` header. |
| **Per-backend session IDs**         | None needed. Modern backends are stateless; forward each request directly. The existing `sessionID == ""` stateless-backend path (`session.go:72`) already models this.                                                                                                  |
| **Per-backend capabilities**        | Fetch via `**server/discover`** per backend and **cache it. This is backend server state, not per-client session state. Each gateway instance rebuilds independently.                                                                                                    |
| **Subject / anti-hijack**           | No session ⇒ no session hijacking vector. Per-request bearer-token auth (`authorization.go`, `extractSubject`) already covers identity.                                                                                                                                  |

The encrypted-session-ID mechanism is replaced by **(1) name-prefix routing** (already exists for single-target calls) plus **(2) a per-backend** `server/discover` **capability cache** for fan-out operations. Route and identity are already per-request. No shared session store is required for the modern↔modern path.

## Compatibility Matrix

The gateway is simultaneously a **server** (to downstream clients) and a **client** (to upstream backends). Each face is independently legacy or modern, giving four cells:

```
                            BACKEND
                    Legacy              Modern
           ┌──────────────────┬───────────────────────┐
  Legacy   │  Cell 1          │  Cell 4               │
  CLIENT   │  As-is today     │  Mint client session, │
           │  (unchanged)     │  stateless upstream   │
           ├──────────────────┼───────────────────────┤
  Modern   │  Cell 3          │  Cell 2               │
           │  Invent upstream │  Fully stateless      │
           │  session +       │  (target design)      │
           │  translate       │                       │
           └──────────────────┴───────────────────────┘
```

### Cell 1 — Legacy Client ↔ Legacy Backend

**Today's behavior. Unchanged.** Client sends `initialize` → `newSession` fans out `initialize` to backends, mints encrypted session ID. GET stream merges notifications. Server-to-client requests ride the SSE stream with the JSON-RPC-ID-encoding trick (`handlers.go:606-675`).

No code changes in any phase. The existing MCP proxy test suite must continue to pass unmodified.

### Cell 2 — Modern Client ↔ Modern Backend

**Fully stateless. Target design.** This is the primary deliverable of Phase 1.

- **No session object.** Route from header; backend from namespaced `Mcp-Name`/name/URI.
- **Inject** `_meta` (protocolVersion, clientInfo, clientCapabilities, logLevel) and set `Mcp-Method` / `Mcp-Name` / `MCP-Protocol-Version` on each upstream request.
- **Fan-out** (`tools/list`, `resources/list`, `prompts/list`) uses the capability cache to pick backends; merge functions are unchanged.
- `**server/discover` answered by merging cached backend discovery results.
- **MRTR flows straight through**: `InputRequiredResult` returned to client; the client's retry `tools/call` re-routes to the same backend by name prefix. No cross-instance ID encoding needed — the retry is a normal request; `requestState` is opaque and passes through.
- `**subscriptions/listen` fans out to backends, merges notification streams. No `Last-Event-ID`.
- **Removed methods** (`ping`, `logging/setLevel`) return appropriate errors. GET and DELETE return `405`.

### Cell 3 — Modern Client ↔ Legacy Backend

The gateway is stateless downstream but the backend requires a session. The gateway must **invent and own** the upstream session.

- **Gateway-managed upstream session pool** keyed by `route+backend+subject`. On the first modern request targeting a legacy backend, lazily run `initializeSession()`, obtain its `Mcp-Session-Id`, and hold it in the pool. Reuse across requests; reap on idle/TTL via `Close()`.
- **MRTR↔SSE translation**: a legacy backend delivers server-to-client requests (sampling/elicitation/roots) on its SSE stream. The gateway converts them into MRTR `InputRequiredResult` for the modern client. The modern client's retry `inputResponses` are converted back to JSON-RPC responses on the backend's stream.
- `**subscriptions/listen` → legacy GET SSE stream: the gateway opens a legacy GET stream to the backend on behalf of the modern client.
- **Capabilities** sourced from the invented upstream session (or a `server/discover` shim).

**Tradeoffs:**

- Reintroduces statefulness (backend session pool), but only for legacy backends.
- Gateway-instance-local backend affinity: if a request for the same legacy backend lands on another gateway instance, that instance initializes its own upstream session — correctness preserved, at the cost of extra upstream sessions.
- MRTR↔SSE translation is the most delicate part; elicitation/sampling round-trips must preserve request identity and timeouts.

### Cell 4 — Legacy Client ↔ Modern Backend

The gateway keeps a client-facing session but the upstream is stateless.

- Accept client `initialize`; answer from the **capability cache** (built via `server/discover` for each modern backend). Mint the encrypted client-facing session ID with **empty backend session IDs** — the already-supported `sessionID == ""` case (`session.go:72`). `newSession` gains a modern-backend branch that skips upstream `initialize` and instead calls `server/discover`.
- Per client request, inject modern `_meta` + `Mcp-Method`/`Mcp-Name` headers upstream.
- **MRTR → legacy server-to-client**: modern backend returns `InputRequiredResult`; the gateway converts it to a legacy server-to-client request on the client's SSE stream (reusing `maybeServerToClientRequestModify` ID-encoding). The client's response is converted back to an MRTR retry upstream.
- **Legacy GET stream → upstream** `subscriptions/listen`: merged.

**Tradeoffs:**

- The encrypted-session machinery stays alive for legacy clients indefinitely. This is fine — it is additive and isolated behind the era detection branch.

## Design Decisions

### D1 — Downstream (Client) Era Detection

Follow the spec's dual-era server rule, applied in `servePOST`/`serveGET` before the method switch:

- Method `initialize` (or a GET/DELETE with `Mcp-Session-Id`, or any request carrying a decryptable gateway session ID) ⇒ **legacy** path (existing code unchanged).
- A request carrying `_meta.io.modelcontextprotocol/protocolVersion` (and/or the modern required header `Mcp-Method`) with a modern version string ⇒ **stateless** path.
- Missing required modern `_meta` on a non-legacy request ⇒ `-32602` (Invalid Params) + `400`.
- Header↔body mismatch (`Mcp-Method` ≠ JSON-RPC `method`, or `Mcp-Name` ≠ `params.name/uri`) ⇒ `-32020` (HeaderMismatch) + `400`.
- Unsupported protocol version ⇒ `-32022` (UnsupportedProtocolVersion) + `400` with `data.supported` listing gateway's supported versions.

```go
type era int

const (
	eraLegacy era = iota
	eraModern
)

func detectClientEra(r *http.Request, msg jsonrpc.Message) era {
	// An initialize request is always legacy.
	if req, ok := msg.(*jsonrpc.Request); ok && req.Method == "initialize" {
		return eraLegacy
	}
	// A session ID header indicates legacy.
	if r.Header.Get(sessionIDHeader) != "" {
		return eraLegacy
	}
	// Mcp-Method header is the simplest modern signal.
	if r.Header.Get(mcpMethodHeader) != "" {
		return eraModern
	}
	// Fallback: check _meta in body for protocolVersion.
	// ...
	return eraLegacy // conservative default
}
```

### D2 — Upstream (Backend) Era Detection

Three strategies, combined:

1. **Auto-detect + cache (spec fallback):** Send `server/discover` to the backend; a valid `DiscoverResult` or a modern JSON-RPC error (`-32022`/`-32020`/`-32021`) ⇒ modern. A `400/404/405` without a recognized modern error body ⇒ fall back to `initialize` ⇒ legacy. Cache era per `route+backend` origin; re-probe on failure.
2. **Explicit config field** on `MCPBackend` (`internal/filterapi/mcpconfig.go`), e.g., `Stateless *bool`. Useful for deterministic behavior in air-gapped setups.
3. **Both** — auto-detect by default with an optional config override.

**Recommendation:** Option 3 (both). Auto-detect is the zero-config default; the config field provides an escape hatch for environments where probing is undesirable.

### D3 — Capability / server/discover Cache

A lazy, per-instance in-memory cache keyed by `route+backend` (used for routing / `server/discover` aggregation in Phase 0.2; invalidation ownership lives with `subscriptions/listen` in Phase 0.4). **Proxy-level result caching** for list/read methods (`ttlMs` / `cacheScope` on tools, prompts, resources — #B1) is deferred to Phase 3.

- **TTL** from `DiscoverResult.ttlMs` (honor `cacheScope`); when aggregating across backends, return the most-restrictive hints (smallest `ttlMs`; `private` if any backend is private).
- **Invalidation** when a `notifications/tools/list_changed`, `notifications/resources/list_changed`, or `notifications/prompts/list_changed` notification is observed on a `subscriptions/listen` stream.
- **Rebuild** independently per gateway instance ⇒ preserves stateless horizontal scaling; no new infra.
- **Pre-warm on config load** + periodic refresh for backends that support `subscriptions/listen` (Phase 3 follow-up; not required for Phase 0/1 conformance).

Fits naturally with the existing `multiWatcherSignaler` (`config.go`) pattern for signaling tool changes.

```go
type capabilityCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]*cacheEntry
}

type cacheKey struct {
	route   filterapi.MCPRouteName
	backend filterapi.MCPBackendName
}

type cacheEntry struct {
	result    *DiscoverResult
	era       era
	expiresAt time.Time
}
```

### D4 — MRTR Translation Strategy

For cross-era cells (3 and 4), the gateway must translate between MRTR and server-initiated requests:

**Legacy backend → Modern client (Cell 3):**

```
Backend SSE stream delivers:
  { "jsonrpc": "2.0", "id": 42, "method": "sampling/createMessage", "params": {...} }

Gateway converts to MRTR response on the pending request:
  { "jsonrpc": "2.0", "id": <client-req-id>, "result": {
      "resultType": "input_required",
      "inputRequests": [{ "type": "sampling/createMessage", "params": {...},
                          "requestState": "<encoded: backend+original-id>" }]
  }}

Client retries with inputResponses:
  { "method": "tools/call", "params": { ..., "inputResponses": [
      { "requestState": "<encoded>", "result": {...} }
  ]}}

Gateway decodes requestState, sends JSON-RPC response to backend SSE stream:
  { "jsonrpc": "2.0", "id": 42, "result": {...} }
```

**Modern backend → Legacy client (Cell 4):**

The reverse: `InputRequiredResult` from the modern backend is converted to a server-to-client request on the legacy client's SSE stream using the existing `maybeServerToClientRequestModify` ID-encoding. The legacy client's JSON-RPC response is converted back to an MRTR retry with `inputResponses`.

### D5 — Upstream Session Pool (Cell 3)

For modern clients talking to legacy backends, the gateway must maintain upstream sessions:

```go
type upstreamSessionPool struct {
	mu       sync.RWMutex
	sessions map[sessionPoolKey]*pooledSession
}

type sessionPoolKey struct {
	route   filterapi.MCPRouteName
	backend filterapi.MCPBackendName
	subject string
}

type pooledSession struct {
	sessionID    gatewayToMCPServerSessionID
	capabilities mcp.ServerCapabilities
	lastUsed     time.Time
	sseStream    io.ReadCloser // long-lived notification stream
}
```

- Keyed by `route+backend+subject` for tenant isolation.
- Lazily initialized on first modern request targeting a legacy backend.
- Reaped on idle timeout or explicit TTL.
- Gateway-instance-local; no distributed coordination required (correctness preserved with redundant sessions across instances).

## Phased Implementation Plan

> **Active scope:** Phase 0 and Phase 1 only. Phases 2–4 are documented below for architectural context but are deferred per review feedback.

The split is deliberate. **Phase 0 builds the entire modern path but never runs it** — every PR is additive and the new code is unreachable, so the live handlers stay routed to legacy and no existing behavior changes. **Phase 1 is activation only** — we flip the dispatcher so handlers start routing to modern as well, and add the E2E coverage that proves it.

### Staging principle: request lifecycle

Phase 0 PRs are staged **along the request lifecycle**, not by “feature pile.” Each PR owns one stage, declares what it consumes / produces, and has an explicit acceptance gate. Later PRs must **consume** earlier outputs rather than re-deriving them (e.g. client era is classified once in PR 0.1; PR 0.3 must not re-infer it).

```
Client request
    │
    ▼
┌─────────────────────────────────────┐
│ PR 0.1  Downstream ingress          │  parse · detect client era · validate
│         & classification            │  headers/_meta · protocol errors
└─────────────────┬───────────────────┘
                  │ validated legacy | validated modern | protocol error
                  ▼
┌─────────────────────────────────────┐
│ PR 0.2  Backend selection           │  backendSelector · server/discover
│         & discovery                 │  era evidence · version intersection
└─────────────────┬───────────────────┘
                  │ selected homogeneous backends + DiscoverResult(s)
                  ▼
┌─────────────────────────────────────┐
│ PR 0.3  Modern forwarding           │  upstream headers · fan-out / single-target
│         & response handling         │  envelope validation · merge / partial-success
└─────────────────┬───────────────────┘
                  │
                  ▼
┌─────────────────────────────────────┐
│ PR 0.4  Subscriptions               │  subscriptions/listen · stream merge
│                                     │  cache invalidation ownership
└─────────────────┬───────────────────┘
                  │
                  ▼
┌─────────────────────────────────────┐
│ Phase 1  Activation                 │  wire detectClientEra into serve*
│                                     │  E2E + observability
└─────────────────────────────────────┘
```

**Downstream PR mapping (current work):**

| Lifecycle stage                       | Proposal PR | Tracking PR                                                            | Notes                                                                    |
| ------------------------------------- | ----------- | ---------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| Ingress & classification              | **0.1**     | [#2518](https://github.com/envoyproxy/ai-gateway/pull/2518)            | Owns `detectClientEra` + modern validation; sole classification boundary |
| Backend selection & discovery         | **0.2**     | Split from [#2545](https://github.com/envoyproxy/ai-gateway/pull/2545) | Discovery / era evidence / version intersection only                     |
| Modern forwarding & response handling | **0.3**     | Split from [#2545](https://github.com/envoyproxy/ai-gateway/pull/2545) | Consumes 0.1 validated request; no second client-era check               |
| Subscriptions                         | **0.4**     | Follow-up after 0.2/0.3                                                | Keep `subscriptions/listen` ownership explicit; not part of discovery    |
| Activation                            | **1.1**     | After 0.1–0.4 land                                                     | First behavior change                                                    |

> **Boundary problem this staging fixes:** [#2518](https://github.com/envoyproxy/ai-gateway/pull/2518) introduces `detectClientEra` with validation, while [#2545](https://github.com/envoyproxy/ai-gateway/pull/2545) independently re-infers the client era. Under this plan, **only PR 0.1 classifies the client**; later stages receive a validated request (or a protocol error) and must not re-run era detection.

> **Caching:** Proxy-level result caching (`ttlMs` / `cacheScope` on list/read results, #B1) stays **deferred to Phase 3**. Discovery may keep a simple per-backend `DiscoverResult` cache for routing; do not treat a full Phase 0.2 checklist as complete if only part of discovery ships — call out the remaining PR explicitly.

---

### Phase 0 — Foundations + Modern Path (No Behavior Change)

**Objective:** Land the full stateless implementation as unreferenced code, staged by request-lifecycle ownership. Zero behavior change; all existing unit and E2E tests pass **unmodified**.

> **Applies to every PR in this phase:** `detectClientEra` is never called from `servePOST` / `serveGET` / `serveDELETE`. **Handlers remain routed to legacy** in all of these PRs. Nothing here is reachable until Phase 1 wires it in.

**Estimated effort:** 8–12 days, 4 PRs.

#### PR 0.1 — Downstream ingress and classification

**Owns:** one-shot request parse, client-era detection, modern header/`_meta` validation, shared protocol errors / header codecs, and the mechanical legacy split.

**Produces for later phases:** a validated legacy request, a validated modern request, or a protocol error. Downstream PRs must consume this output — they must not re-infer client era.

**Depends on:** nothing (foundation).

**Tracking:** [#2518](https://github.com/envoyproxy/ai-gateway/pull/2518).

- Version + header + `_meta` constants and the supported-version set (P0.1–P0.3).
- `era` type and the `detectClientEra(r, msg)` helper (P0.4).
- New error codes `-32020` (HeaderMismatch), `-32021` (MissingRequiredCapability), `-32022` (UnsupportedProtocolVersion) with constructors, plus the `-32002` → `-32602` renumber and the rule to accept `-32002` from legacy backends and translate it for modern clients (P0.5, P1.13).
- `onJSONRPCError(w, status, jsonrpcErr)` writer (P0.6).
- `Mcp-Name` base64-sentinel encode/decode helpers (P0.7).
- Confirm the Envoy `HTTPRouteFilter` sets `x-ai-eg-mcp-route` on all methods, not just `initialize` (P0.8).
- A thin request dispatcher plus a `serveLegacyPOST` entrypoint **hardcoded to legacy** (P0.9).
- Move legacy-only HTTP handlers into `legacy.go`; keep cross-era/shared helpers in `handlers.go` (P0.10–P0.11).
- Header/`_meta` validation helpers written as pure functions with no caller yet (P1.2) — including `Mcp-Method` and `Mcp-Name`.

_Files:_ new `era.go`, `modern.go`, `legacy.go`.

_Acceptance:_ later phases receive either a validated legacy request, a validated modern request, or a protocol error. `go build ./...` passes; full existing suite green unmodified; unit tests for era detection, error constructors, validation helpers, and `Mcp-Name` round-trip. Legacy HTTP / observability behavior unchanged.

**Pre-req before PR 0.2:** verify `github.com/modelcontextprotocol/go-sdk` (currently `v1.6.1`) exposes `DiscoverResult`/`DiscoverParams`, `InputRequiredResult`, the `resultType` field on `Result`, `SubscriptionsListenParams`, the `2026-07-28` version constant, and error codes `-32020`/`-32021`/`-32022`. If unavailable: bump the SDK, or hand-roll local types and migrate when the SDK catches up.

#### PR 0.2 — Backend selection and discovery

**Owns:** `backendSelector`, per-backend `server/discover`, backend-era evidence, and common protocol-version calculation. Does **not** own fan-out forwarding, response merge for tools/resources/prompts, or `subscriptions/listen`.

**Consumes:** validated modern request shape / helpers from PR 0.1 (still not wired into `servePOST`).

**Produces:** per-backend discovery results with positive / unknown / contradictory era evidence; the set of protocol versions common to the gateway and all usable selected backends; rejection of mixed or unresolved backend sets per the homogeneous-route requirement.

**Depends on:** PR 0.1.

**Tracking:** first half of the former [#2545](https://github.com/envoyproxy/ai-gateway/pull/2545) scope (split out).

- Apply `backendSelector` for the route.
- Discover every selected backend via `server/discover` (simple per-`route+backend` cache for routing is OK; proxy-level result caching remains Phase 3).
- Preserve positive, unknown, and contradictory backend-era evidence — do not collapse unknowns into a guessed era.
- Calculate protocol versions common to the gateway and all usable selected backends (`supportedVersions` intersection).
- Reject mixed or unresolved backend sets according to the homogeneous-route requirement (all-legacy or all-modern per `MCPRoute`).
- Server-facing `server/discover` aggregator: merge into one `DiscoverResult` — `supportedVersions` intersected, `capabilities` unioned (reuse `mergedCapabilities`), `serverInfo` = gateway identity; when aggregating discover cache hints across backends, use the most-restrictive config (smallest `ttlMs`; `cacheScope: "private"` if any backend is private).

_Files:_ new `discovery.go`; `handlers.go`. _Spec:_ #A2.

_Acceptance:_ unit tests against fake backends returning varied `DiscoverResult` and era evidence; mixed/unresolved sets rejected; version intersection correct. Legacy path untouched. If this PR ships only part of discovery, name the remaining PR rather than marking the full 0.2 checklist done.

#### PR 0.3 — Modern forwarding and response handling

**Owns:** constructing upstream requests, fan-out and single-target routing, upstream JSON-RPC envelope validation, and partial-success / zero-success merge behavior.

**Consumes:** the **validated modern request from PR 0.1** (no second `detectClientEra` / client-era inference) and discovery / capability evidence from PR 0.2.

**Produces:** modern method handlers ready to be dispatched at activation — still unreachable from `servePOST` until Phase 1.

**Depends on:** PR 0.1 and PR 0.2.

**Tracking:** second half of the former [#2545](https://github.com/envoyproxy/ai-gateway/pull/2545) scope (split out).

- Consume the validated request from Phase 0.1 rather than performing another client-era check.
- Construct the required upstream headers (`Mcp-Method` / `Mcp-Name` / `MCP-Protocol-Version`) and inject the `_meta` block; make `sendRequestPerBackend` era-aware so it stops hardcoding `2025-06-18` and drops `Mcp-Session-Id`/`Last-Event-Id` on the modern branch (P1.6).
- Modern fan-out for `tools/list`, `resources/list`, `prompts/list`: reuse `sendToAllBackendsAndAggregateResponses` / merge helpers; source capability checks from discovery evidence, not `cse.capabilities`; set `resultType: "complete"` on merged results (P1.7).
- Stateless single-target routing — modern variants of `handleToolCallRequest`, `handleResourceReadRequest`, `handlePromptGetRequest`, `handleResourcesSubscribe/Unsubscribe`, `handleCompletionComplete`. Select the backend from the namespaced name/URI with no `compositeSessionEntry`, rewrite the body to the unprefixed name, and **recompute** `Mcp-Name` to match (P1.5).
- Validate upstream JSON-RPC envelopes before merging results; define partial-success and zero-success behavior explicitly.
- MRTR passthrough (modern↔modern) — proxy `InputRequiredResult` and retry verbatim with opaque `requestState`, no ID rewriting; treat an absent `resultType` as `"complete"` (P1.9, P1.12).
- Reject removed methods on the modern path — `ping`/`logging/setLevel`/`initialize` → `-32601`; GET/DELETE → `405` (P1.10).
- Read `x-ai-eg-mcp-route` on every modern request, not just `initialize` (P1.11).

_Files:_ `handlers.go`, `session.go`. _Spec:_ #A1, #A3, #A4, #D1.

_Acceptance:_ unit tests for prefix round-trip with header recompute, `_meta` injection, era-aware upstream send, fan-out / single-target paths, envelope validation, partial- and zero-success merges, MRTR passthrough, and each rejected method. No independent client-era inference in this PR. Legacy branch byte-identical.

#### PR 0.4 — Subscriptions

**Owns:** `subscriptions/listen` end-to-end on the modern path — fan-out, SSE stream merge, keep-alives, and **capability-cache invalidation ownership**. Kept separate so discovery (0.2) and forwarding (0.3) stay reviewable without stream-lifecycle complexity.

**Consumes:** validated modern request (0.1) and selected backends / discovery evidence (0.2).

**Depends on:** PR 0.1 and PR 0.2 (0.3 recommended but not strictly required if listen is independent of list/call handlers).

**Tracking:** follow-up after the #2545 split; do not treat 0.2 as “complete” if listen remains undone.

- `subscriptions/listen` handler in new `subscriptions.go`: fan out the POST to route backends, merge response SSE streams into one client response, no `Last-Event-ID`, keep-alive via SSE comment lines (P1.8).
- Explicit ownership of cache invalidation: on `*_list_changed` notifications, invalidate the per-backend discovery cache from PR 0.2.
- Document that proxy-level result-cache invalidation (#B1) is **out of scope** here and lands with Phase 3.

_Files:_ new `subscriptions.go`; `discovery.go` (invalidation hooks only). _Spec:_ #A2.

_Acceptance:_ unit tests for stream merge, keep-alive, and discovery-cache invalidation on list-changed notifications. Legacy GET SSE path untouched.

**Shared constant block introduced across Phase 0:**

```go
const (
	protocolVersion20250618 = "2025-06-18"
	protocolVersion20251125 = "2025-11-25"
	protocolVersion20260728 = "2026-07-28"

	mcpMethodHeader = "Mcp-Method"
	mcpNameHeader   = "Mcp-Name"

	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaLogLevel           = "io.modelcontextprotocol/logLevel"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
	metaSubscriptionID     = "io.modelcontextprotocol/subscriptionId"

	errCodeHeaderMismatch             = -32020
	errCodeMissingRequiredCapability  = -32021
	errCodeUnsupportedProtocolVersion = -32022
	errCodeResourceNotFound           = -32602 // was -32002
)

var supportedVersions = []string{protocolVersion20260728, protocolVersion20251125, protocolVersion20250618}
```

---

### Phase 1 — Integration (Modern ↔ Modern Goes Live)

**Objective:** Point the entrypoints at the era dispatcher so the handlers **start routing to modern as well**, and Cell 2 (modern↔modern) serves real traffic. Cell 1 (legacy↔legacy) is unchanged. This is the first behavior change, but it is a small diff — the implementation already landed in Phase 0.

**Estimated effort:** 2–4 days.

#### PR 1.1 — Activation: wire handlers to era detection + E2E

**Owns:** wiring only — no new modern-path logic beyond the dispatcher flip.

**Consumes:** PRs 0.1–0.4 (validated classification, discovery, forwarding, subscriptions).

**Depends on:** PR 0.1 required; 0.2–0.4 required for a complete modern Cell 2. Do not activate a partial modern surface without documenting which methods remain unreachable.

- Wire `detectClientEra` into `servePOST` / `serveGET` / `serveDELETE`.
- Dispatch **only** validated requests from the PR 0.1 classification path: legacy → existing code untouched; modern → `servePOSTStateless`; GET/DELETE → `405` (P1.1).
- Require positive homogeneous backend-era evidence (from PR 0.2) before enabling the modern route for a given `MCPRoute`.
- Hook capability-cache invalidation into the `subscriptions/listen` notification path if not already connected in PR 0.4.
- Verify metrics and tracing are identical across both branches.

**E2E:** modern client → modern backend covering `server/discover`, `tools/list`, `tools/call`, `resources/read`, `prompts/get`, `subscriptions/listen`, an MRTR round-trip, and each rejected method. The existing legacy E2E suite runs unmodified as the regression gate. Until Phase 1, unit tests alone cover the additive Phase 0 code (it is intentionally uncallable from HTTP entrypoints).

#### PR 1.2 — Integration follow-ups (as needed)

Kept separate so PR 1.1 stays reviewable. Likely none committed up front, pulled in only if the integration surfaces them:

- Backend era detection (D2) if a modern-client → legacy-backend combination shows up early — at minimum a clean error rather than a crash.
- A conformance run against the RC test vectors.
- Docs and `examples/mcp` dual-era updates if maintainers want them before Phase 4.

---

### Phase 2–4: Deferred (Documented for Architectural Context)

> **Status:** Deferred per review. Cross-era support can wait until modern clients emerge and legacy backends remain common. These phases are retained for completeness but are **not in active scope**.

> **Note on backend homogeneity:** With this proposal (Phase 0 + Phase 1), an `MCPRoute` is only supported when **all of its backends share a single era** — either all legacy or all modern, but **not mixed**. A route that fans out across both legacy and modern backends requires the cross-era translation machinery (upstream session pool, MRTR↔SSE translation, per-backend era detection) that lives in the deferred Phase 2/3. Until those phases land, mixed-era routes are out of scope and should be split so each route targets backends of a single era.

Phase 2 — Cross-Era Translation (Cells 3 and 4)

**Objective:** Enable modern clients to talk to legacy backends (Cell 3) and legacy clients to talk to modern backends (Cell 4).

| Item | Description                                                                                                                                                                                            | Spec Items Addressed | Files                                  |
| ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------- | -------------------------------------- |
| P2.1 | **Backend era detection + cache** (D2) — `backendEra(route, backend)` with auto-detect + optional config field on `MCPBackend`                                                                         | #F1                  | new `era.go`, `filterapi/mcpconfig.go` |
| P2.2 | **Upstream session pool** (Cell 3) — gateway-managed pool keyed by `route+backend+subject`; lazy `initializeSession()`; idle reap via `Close()`                                                        | #F1, #A1             | new `session_pool.go`                  |
| P2.3 | **MRTR↔SSE translator** (Cell 3) — convert legacy server→client SSE requests to `InputRequiredResult`; convert modern client MRTR retry `inputResponses` back to JSON-RPC responses on backend stream | #A3, #F1             | new `translate.go`                     |
| P2.4 | `**subscriptions/listen` → legacy GET (Cell 3) — map modern `subscriptions/listen` to legacy GET SSE stream per backend                                                                                | #A2, #F1             | `subscriptions.go`                     |
| P2.5 | **Modern-backend branch in** `newSession` (Cell 4) — skip upstream `initialize`; build `compositeSessionEntry` with `sessionID == ""` and capabilities from `server/discover`                          | #F1                  | `mcpproxy.go`                          |
| P2.6 | `**_meta`/header injection for legacy clients (Cell 4) — inject modern headers/`_meta` on upstream calls from legacy client sessions                                                                   | #F1                  | `session.go`                           |
| P2.7 | **MRTR → legacy SSE translator** (Cell 4) — convert modern `InputRequiredResult` to server-to-client requests on client SSE stream; convert client responses to MRTR retry upstream                    | #A3, #F1             | `translate.go`                         |
| P2.8 | **Legacy GET → upstream** `subscriptions/listen` (Cell 4) — open upstream `subscriptions/listen`, merge into legacy client GET stream                                                                  | #A2, #F1             | `session.go`                           |
| P2.9 | **Translation fidelity limits** — document unsupported edge cases: cancellation across eras, timeout propagation, progress token mismatches                                                            | #F1                  | docs                                   |

Phase 3 — Cacheable Results, Deterministic Ordering, Minor Spec Items

**Objective:** Implement proxy-level caching, deterministic tool ordering, and remaining minor spec items.

| Item | Description                                                                                                                                                                                                                                                                                                    | Spec Items Addressed | Files                            |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------- | -------------------------------- |
| P3.1 | **Proxy-level result caching** — cache `tools/list`, `prompts/list`, `resources/list`, `resources/read`, `resources/templates/list` results using `ttlMs` and `cacheScope`. `cacheScope: "public"` allows shared cache across clients; `"private"` is per-client. Invalidate on `*_list_changed` notifications | #B1                  | new `cache.go` or `discovery.go` |
| P3.2 | **Deterministic** `tools/list` **ordering** — sort merged tools by `backend__toolName` lexicographically across aggregated backends for stable LLM prompt cache hit rates                                                                                                                                      | #B2                  | `handlers.go`                    |
| P3.3 | `**extensions` field preservation — fix `mergedCapabilities` (`session.go:647`) to merge the `extensions` map instead of dropping it                                                                                                                                                                           | #D3                  | `session.go`                     |
| P3.4 | **OTel** `_meta` **verification** — verify `traceparent`, `tracestate`, `baggage` keys conform to the formalized conventions. No new code expected; `tracing/mcp.go:76` already injects these                                                                                                                  | #D2                  | `tracing/mcp.go`                 |
| P3.5 | `**Mcp-Param-{Name}` headers — support `x-mcp-header` annotated tool parameters: extract from tool input schema, set corresponding `Mcp-Param-{Name}` headers on upstream requests                                                                                                                             | #D1                  | `handlers.go`                    |
| P3.6 | `**resultType` on all results — audit every handler to ensure `resultType: "complete"` is set on all result payloads sent to modern clients                                                                                                                                                                    | #A3                  | `handlers.go`                    |
| P3.7 | **Error code allocation compliance** — ensure gateway-minted error codes fall within `-32020` to `-32099` (MCP reserved range) or `-32000` to `-32019` (implementation-defined, grandfathered). No codes in the unallocated ranges                                                                             | #D4                  | `handlers.go`                    |
| P3.8 | **Capability-cache pre-warm and periodic refresh** — pre-warm on config load + periodic refresh via `multiWatcherSignaler`                                                                                                                                                                                     | #A2                  | `discovery.go`, `config.go`      |

When aggregating cache hints (`ttlMs`, `cacheScope`) across backends, return the most restrictive config: the smallest `ttlMs` seen from all responses, and `cacheScope: "private"` if any backend is private (`"public"` only if all are public). No finer aggregation — most-restrictive is a reasonable starting point.

Phase 4 — Auth Hardening, Deprecations, and Cleanup

**Objective:** Address authorization spec changes, formalize deprecation handling, update docs and examples.

| Item | Description                                                                                                                                                                                                          | Spec Items Addressed | Files                             |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------- | --------------------------------- |
| P4.1 | **Authorization response** `iss` **validation** — when generating Envoy `jwt_authn` config, include validation that the `iss` parameter in authorization responses matches the recorded issuer per RFC 9207          | #C1                  | `mcproute.go`, `authorization.go` |
| P4.2 | **Client credentials issuer binding** — ensure `tokenprovider/` and `rotators/` key persisted credentials by the issuer identifier and do not reuse across authorization servers. Re-register on authz server change | #C2                  | `tokenprovider/`, `rotators/`     |
| P4.3 | **Deprecation annotations** — add log warnings when legacy-only features (Roots, Sampling, Logging, HTTP+SSE, `includeContext`) are used. Do not remove functionality                                                | #E1, #E2             | `handlers.go`, `sse.go`           |
| P4.4 | **Tasks extension pass-through** — ensure `io.modelcontextprotocol/tasks` extension methods are passed through without gateway intervention                                                                          | #E3                  | `handlers.go`                     |
| P4.5 | **DCR deprecation note** — update served metadata to indicate DCR is deprecated in favor of CIMD. No functional change; gateway only passes `registration_endpoint`                                                  | #E4                  | `mcp_route_security_policy.go`    |
| P4.6 | **Docs, examples, versioned capability pages** — update `site/docs/capabilities/mcp`, `examples/mcp` with dual-era examples. Version-specific documentation                                                          | #F2                  | `site/`, `examples/`              |
| P4.7 | **Conformance and E2E testing** — dual-era test matrix + benchmarks                                                                                                                                                  | #F3                  | `*_test.go`, `e2e/`               |

## Spec Item → Phase Mapping

Every item from the spec changelog and the tracking issue, mapped to a phase:

| Issue Ref | SEP / Change | Title                                                                                                         | Phase                                                                |
| --------- | ------------ | ------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| #A1       | SEP-2567     | Stateless sessions (remove `Mcp-Session-Id`)                                                                  | Phase 0.3 (handlers), Phase 1 (activation)                           |
| #A2       | SEP-2575     | Stateless protocol (`server/discover`, `subscriptions/listen`, remove `initialize`/`ping`/`logging/setLevel`) | Phase 0.2 (discover), Phase 0.4 (listen), Phase 1 (activation)       |
| #A3       | SEP-2322     | MRTR + `resultType`                                                                                           | Phase 0.3 (passthrough), Phase 1 (activation), Phase 2 (translation) |
| #A4       | SEP-2575     | Per-request `_meta` fields                                                                                    | Phase 0.1 (validation), Phase 0.3 (injection), Phase 1               |
| #B1       | SEP-2549     | Cacheable results (`ttlMs`, `cacheScope`)                                                                     | Phase 3 (proxy-level); discover hints only in Phase 0.2              |
| #B2       | —            | Deterministic `tools/list` ordering                                                                           | Phase 3                                                              |
| #C1       | SEP-2468     | Authorization response `iss` validation                                                                       | Phase 4                                                              |
| #C2       | SEP-2352     | Client credentials issuer binding                                                                             | Phase 4                                                              |
| #D1       | SEP-2243     | `Mcp-Method`/`Mcp-Name` headers + `x-mcp-header`                                                              | Phase 0.1 (validate), Phase 0.3 (forward), Phase 3 (`x-mcp-header`)  |
| #D2       | SEP-414      | OTel `_meta` conventions                                                                                      | Phase 3 (verify)                                                     |
| #D3       | —            | `extensions` field on capabilities                                                                            | Phase 3                                                              |
| #D4       | —            | Error code renumbering                                                                                        | Phase 0.1 (constants), Phase 1 (enforcement)                         |
| #D5       | SEP-2575     | Remove `notifications/elicitation/complete`                                                                   | Phase 1 (no-op; MRTR replaces)                                       |
| #D6       | SEP-2106     | JSON Schema 2020-12                                                                                           | No work (pass-through)                                               |
| #E1       | SEP-2577     | Deprecate Roots/Sampling/Logging                                                                              | Phase 4                                                              |
| #E2       | SEP-2596     | Deprecate HTTP+SSE + `includeContext`                                                                         | Phase 4                                                              |
| #E3       | SEP-2663     | Tasks → extension                                                                                             | Phase 4                                                              |
| #E4       | PR #2858     | Deprecate DCR → CIMD                                                                                          | Phase 4                                                              |
| #F1       | —            | Backward compatibility (dual-era)                                                                             | Phase 0.1 (detection), Phase 2 (translation)                         |
| #F2       | —            | Docs and examples                                                                                             | Phase 4                                                              |
| #F3       | —            | Conformance and E2E testing                                                                                   | Phase 1 (Cell 2 E2E), Phase 4 (full matrix)                          |

## Gotchas and Risks

### Phase 0/1 Risks (Immediate)

#### Header↔Body Validation is Bidirectional

Because the gateway rewrites `Mcp-Name`/body when stripping the `backend__` prefix, it must **recompute** the outgoing `Mcp-Name` header to match the rewritten body. A spec-compliant backend will reject the request with `-32020` (HeaderMismatch) if they don't match. Same when adding the prefix back on responses.

#### Route Header on Every Request

Modern routing relies on `x-ai-eg-mcp-route` being present on **all** methods, not just `initialize`. Verify the Envoy `HTTPRouteFilter` sets it unconditionally. Today only `initialize` reads it (`handlers.go:376`). **Confirmed:** the controller sets this header on the route rule (not per-method), so it applies to all requests hitting the route.

#### `sendRequestPerBackend` Hardcodes `2025-06-18`

`sendRequestPerBackend` (`session.go:347`) hardcodes `protocolVersion20250618` (`session.go:367`) and always sets `Mcp-Session-Id`/`Last-Event-Id`. This must become era-aware for Phase 1.

#### No Resumability in Modern

The modern path must not use combined event-ID encoding (`secureClientToGatewayEventID`). `Last-Event-ID` must be ignored on modern requests. A broken response stream means the client re-issues the request with a new request ID.

#### `resultType` Default

Absent `resultType` from legacy backends must be treated as `"complete"`. The gateway must inject `resultType: "complete"` on all results forwarded to modern clients.

### Deferred Risks (Phase 2+)

#### MRTR `requestState` Opacity

In Cell 3 (modern client → legacy backend), the gateway encodes backend identity and original JSON-RPC ID into `requestState`. This must be tamper-resistant (signed or encrypted) to prevent routing attacks. Reuse the existing `SessionCrypto` infrastructure.

#### Horizontal Scaling with Legacy Backends (Cell 3)

Each gateway instance maintains its own upstream session pool for legacy backends. This means the same backend may have multiple concurrent sessions from different gateway instances. This is correct but wasteful. For most deployments where the number of legacy backends is small, this is acceptable.

#### Deprecated but Live

Roots, Sampling, and Logging must keep working during the 12-month deprecation window. Do not strip handlers; add deprecation warnings. (Phase 4 concern.).
