# Scale-from-Zero / Hibernate-Aware Routing Proposal

## Overview

GPU-backed model deployments are increasingly scaled to zero replicas (KEDA, cluster autoscalers) or
hibernated (suspended GPU pools, VM hibernate/resume) when idle, because idle accelerator capacity is
the single largest cost driver in self-hosted inference. Today, when a request arrives for such a
backend, Envoy AI Gateway returns an immediate error: a connect failure for a Service with zero pods,
or a local `503 no_healthy_upstream` for a cluster with zero endpoints. The client is left to
implement its own retry-and-wake logic, which every client does differently (or not at all).

This proposal makes the gateway **cold-start aware**: for a backend explicitly marked as
scale-from-zero capable, the gateway keeps the (already buffered) request in flight using a
**user-configured** Envoy retry policy sized to a wait budget, fires a **wake-up HTTP endpoint**
after the first failed attempt to trigger resume, and delivers the request once the backend is up
— returning a clean, OpenAI-shaped error with `Retry-After` only after the budget is exhausted.

No queueing subsystem is introduced, and the gateway does not inject any xDS configuration on the
user's behalf. The design deliberately composes existing, user-owned primitives: an
Envoy Gateway `BackendTrafficPolicy` retry policy (the same pattern already used for provider
fallback), the retry-safe upstream ext_proc filter, and per-endpoint priority failover.

## Goals

1. **Deliver instead of 503**: a request to a scaled-to-zero/hibernated backend is delivered once
   the backend becomes ready, provided it becomes ready within the wait budget configured on a
   user-attached retry policy.
2. **Trigger wake-up**: optionally call a user-provided HTTP endpoint (Nutanix hibernate-resume API,
   KEDA admin hook, custom GPU-pool controller) after the first failed attempt against a cold
   backend, deduplicated so a burst of concurrent requests produces one wake call.
3. **Fail cleanly**: once the user-configured retry budget is exhausted, return an OpenAI-shaped
   error with `Retry-After` rather than a raw Envoy local reply.
4. **Reuse existing primitives**: no request queue in the gateway; no new filters; no gateway-side
   injection of retry policy — compose the existing `BackendTrafficPolicy` retry mechanism (already
   used for provider fallback), the existing upstream ext_proc retry path, and priority-based
   failover.
5. **Stay Kubernetes-agnostic in the data plane**: the extproc must not gain any Kubernetes
   dependency; everything must keep working in the standalone `aigw` CLI.

## Background

### Why the gateway, and not the client or the autoscaler?

- Clients speak OpenAI-compatible APIs and expect synchronous semantics; pushing "poll until the GPU
  pool resumes" onto every client duplicates logic and leaks infrastructure details.
- KEDA's http-add-on and Knative's activator solve this for generic HTTP by holding requests in a
  proxy while scaling up — proving the pattern — but they insert another hop, know nothing about
  model routing (`x-ai-eg-model`), and don't exist for non-Kubernetes resume APIs such as VM
  hibernation.
- The Gateway API Inference Extension (GIE) Endpoint Picker already queues requests during
  scale-from-zero for `InferencePool` backends via its Flow Control layer. That covers the
  InferencePool path (see below) but not `AIServiceBackend` backends — external endpoints, plain
  Services via Envoy Gateway `Backend` resources, or hibernated VMs — and provides no wake trigger.

### What happens today

For an `AIServiceBackend` whose underlying endpoint is down, Envoy surfaces one of two failures,
depending on cluster shape:

- **Endpoint present but nothing listening** (static endpoint, hibernated VM, Service with pods
  terminating): TCP connect refused → upstream connect failure.
- **Zero endpoints** (EDS/Service with zero ready pods): instant **local** `503 no_healthy_upstream` —
  no connection is ever attempted.

Any design must therefore treat both `connect-failure` and locally generated `503` as "cold" signals.

Two existing properties make a retry-based design safe with the AI Gateway extproc in the path:

- Each Envoy retry re-invokes the upstream ext_proc filter; the extproc already re-runs backend
  selection, request translation, and upstream auth per attempt (this is how cross-backend fallback
  works today).
- The request body is already buffered (BUFFERED ext_proc mode), so retrying loses no data and holds
  no additional per-request state in the extproc.

### Feasibility validation

A data-plane PoC (real Envoy + extproc + fake upstream, no Kubernetes) validated the mechanism using
a route retry policy of `connect-failure,reset,retriable-status-codes(503)` with exponential backoff
(0.25s base, 2s max, 10 attempts), with both AI Gateway ext_proc filters in the path:

- A request issued while **nothing was listening** on the backend port, with a listener started
  3 seconds later, was delivered successfully: `200`, end-to-end latency 4.0s, response body intact
  through the translation path.
- A request whose backend never appeared failed only after the retry budget was exhausted: `503`
  after 9.4s, with the raw Envoy local reply body `upstream connect error or disconnect/reset before
headers. retried and the latest reset reason: remote connection failure` — which is exactly the
  client-hostile output the terminal-error shaping in this proposal replaces.

One sharp edge surfaced by the PoC: **outlier detection interacts badly with scale-from-zero**. If
the sole endpoint of a cold backend is ejected, connect-refused turns into a non-retriable local
`no_healthy_upstream` reply and the retry-based hold is defeated. The control plane must not apply
ejection-style outlier detection to cold-start-enabled clusters (or must cap `max_ejection_percent`
below 100%).

## Architecture

```
Client ── POST /v1/chat/completions ──▶ Envoy
  │
  ▼
Router-level ext_proc (parse model, set x-ai-eg-model, ClearRouteCache)
  │
  ▼
Route selected. Route carries a USER-ATTACHED BackendTrafficPolicy retry policy
(same pattern as provider fallback), documented to look like:
  retryOn: connect-failure, reset, retriable-status-codes(503)
  backoff: exponential, budget ≈ maxWait; route timeout raised ≥ maxWait
  │
  ▼
Attempt 1 ──▶ Upstream ext_proc (translation + auth)
  ▼
connect refused / local 503 (zero endpoints)
  │              └─ backend has coldStart.wake? ──▶ async wake HTTP call, fired
  │                 on this first failed attempt (per-backend singleflight +
  │                 coolDown; never blocks; not fired again while warm)
  ▼
Envoy retries w/ backoff (per the user-attached BackendTrafficPolicy)
  │      each retry re-enters the upstream ext_proc: re-select, re-translate,
  │      re-auth; endpoint priority failover to a warm backend still applies
  ▼
Backend resumes (KEDA scale-up / hibernate-resume / pool wake) ──▶ attempt N succeeds
  │
  ▼
Response flows back through the router-level response path as usual

maxWait exceeded ──▶ router-level ext_proc rewrites the terminal 503 into an
                     OpenAI-shaped error with Retry-After: <retryAfterSeconds>
```

## API Design

A new optional `coldStart` field on `AIServiceBackendSpec`. Hibernation is a property of the
backend, not the route, so per-backend is the natural scope; routes referencing the backend inherit
the behavior.

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: llama-70b-gpu-pool
spec:
  schema:
    name: OpenAI
  backendRef:
    name: llama-70b
    kind: Backend
    group: gateway.envoyproxy.io
  coldStart:
    # Advisory wait budget: (a) the value the docs tell you to size your own
    # BackendTrafficPolicy retry budget/backoff and route timeout against, and
    # (b) used to compute the terminal error's Retry-After value. The gateway
    # does not derive or inject a retry policy from this field itself — see
    # "Key Design Decisions" #2.
    maxWait: 180s # default 60s, max 600s
    wake:
      httpEndpoint:
        url: "https://prism.example.com/api/nutanix/v1/vms/my-vm/resume"
        method: POST # default POST
        timeout: 10s # default 5s
        headers:
          - name: Content-Type
            value: application/json
        # Optional: each key in the secret becomes a request header,
        # e.g. Authorization: Bearer <token>. Resolved by the controller,
        # keeping the extproc Kubernetes-agnostic.
        secretRef:
          name: wake-auth
      # Minimum interval between wake calls per backend; a burst of requests
      # produces at most one call per coolDown window.
      coolDown: 60s # default 30s
    failure:
      # Retry-After header value on the terminal 503.
      retryAfterSeconds: 30 # default 30
```

Config propagation follows the established path: CRD → controller filter-config generation →
`filterapi.Backend` (new `ColdStart` field) → extproc runtime config. The `filterapi` layer stays
free of Kubernetes types; the wake secret is resolved by the controller the same way
BackendSecurityPolicy API keys are today.

## Key Design Decisions

### 1. Retry-based hold, not a request queue in the gateway

The gateway does not queue or park requests in the extproc. The "hold" is Envoy's retry loop:

- The codebase deliberately keeps retry semantics out of the extproc and delegates them to Envoy
  (response handling was moved to the router filter precisely to stay out of the retry path). A
  blocking hold in the extproc would pin a gRPC stream and goroutine per held request and fight the
  ext_proc message timeouts; the retry loop holds no additional state.
- Each retry attempt is a true readiness probe of the real data path — a pod can be `Ready` before
  the model server actually accepts work; a successful attempt is the only reliable signal.
- Streaming responses are unaffected: retries happen before any response bytes are committed
  downstream.
- Priority-based failover composes for free: while backend A is waking, an endpoint-priority
  fallback to a warm backend B still applies within the same retry loop.

A readiness-gated blocking hold in the extproc (poll a readiness URL, then release) is listed under
Future Work as an opt-in refinement, not part of this proposal.

### 2. User-configured retry policy, not gateway-injected — for API consistency

The gateway does **not** inject a retry policy on the user's behalf. Users attach an Envoy Gateway
`BackendTrafficPolicy` with retries to the generated HTTPRoute themselves — this is already the
documented provider-fallback pattern in this project, and `coldStart` follows the same convention
rather than introducing a second, inconsistent way of getting retry behavior configured. This also
means there's no "does the injected policy conflict with a user policy" question to resolve — there
is only ever one retry policy, and the user owns it.

The documentation for this feature includes a recommended `BackendTrafficPolicy` shape: retry on
`connect-failure`, `reset`, and `retriable-status-codes: 503` (the two cold failure modes surface
differently — connect-refused vs. local `no_healthy_upstream` 503 — so both must be covered), with
backoff and route timeout sized to the backend's `coldStart.maxWait`.

The docs also call out the 100% outlier-ejection gotcha from the Feasibility validation above as a
required companion setting for any cold-start-enabled cluster, regardless of who configures the
retry policy.

### 3. Wake trigger: generic HTTP endpoint, fired from the extproc, deduplicated

- A generic HTTP call covers Nutanix hibernate-resume, custom GPU-pool controllers, KEDA admin
  endpoints, and anything else — without vendor SDKs in the gateway. `wake.httpEndpoint` is a
  one-member struct today so that alternatives (e.g. a `scaleRef` that patches a Kubernetes scale
  subresource) can be added later without breaking the API.
- The upstream ext_proc filter fires the wake call **asynchronously**, only after the **first
  attempt against a cold-start backend fails** — not on every request arrival — so an
  already-warm backend never takes a wasted wake call; it never blocks or delays the request
  itself. A per-backend singleflight guard plus the `coolDown` window ensures a burst of N
  concurrent requests produces one wake call.
- If `wake` is omitted, the feature degrades to "patient retrying", which is exactly what KEDA
  http-add-on or Knative activator deployments need — the retried traffic itself triggers their
  scale-up.

### 4. InferencePool backends are out of scope — GIE already solves them

For `InferencePool` backends, the GIE Endpoint Picker's Flow Control layer already queues requests
while pods scale from zero (its ext_proc filter runs with a 300s message timeout for exactly this
reason). Duplicating that queue in the gateway would be wasted and conflicting work. `coldStart` is
therefore only valid on `AIServiceBackend`. The user documentation will include a
"scale-from-zero with InferencePool" section pointing at GIE Flow Control, and an e2e test will
cover the integrated behavior.

### 5. Terminal failure shaping

When the budget is exhausted, the terminal 503 flows back through the router-level response path,
where the extproc already rewrites upstream errors into the client-facing API schema. For cold-start
backends it additionally sets `Retry-After: <retryAfterSeconds>`, giving well-behaved clients a
correct backoff hint.

## Implementation Phases

Each phase is independently mergeable and tested:

1. **API + config plumbing**: `coldStart` on `AIServiceBackendSpec` (v1beta1 + v1alpha1 mirror),
   CEL validation, `filterapi.Backend.ColdStart`, controller filter-config generation including wake
   secret resolution. CRD CEL tests + controller tests. No behavior change.
2. **extproc wake trigger + terminal error shaping**: async wake call fired after the first failed
   attempt, singleflight + coolDown dedup, wake metrics, terminal 503 `Retry-After` shaping. Unit
   tests (exactly one wake under concurrency) + a data-plane test whose wake endpoint starts the
   upstream listener (full loop), using a user-configured `BackendTrafficPolicy` for the retry/hold.
3. **Docs, examples, e2e**: the recommended `BackendTrafficPolicy` shape and outlier-ejection guard
   as user-facing documentation, example manifests, user docs (including the KEDA/Knative and
   InferencePool guidance), kind-based e2e with a Deployment scaled 0→1 by a wake shim.

## Out of Scope

- **Request queueing/prioritization in the gateway** — a different problem (see the prioritized
  inference queue discussion in issue #1228); this proposal holds individual in-flight requests only.
- **InferencePool scale-from-zero** — owned by GIE Flow Control (see Key Design Decision 4).
- **Scaling workloads directly** (patching scale subresources, KEDA CRDs): the wake endpoint is the
  extension point; workload-specific shims stay user-owned.
- **Predictive pre-warming** — waking backends ahead of anticipated traffic.

## Future Work

- **Readiness-gated hold in the extproc**: opt-in blocking wait on a readiness URL for precise
  wake-to-dispatch latency, bounded by `maxWait` and a held-request cap. Requires raising the
  upstream ext_proc message timeout for cold-start backends.
- **`wake.scaleRef`**: native Kubernetes scale-subresource wake as an alternative to the HTTP
  endpoint.
- **Cold-start observability**: metrics for wake latency (wake call → first successful attempt),
  retry counts, and budget exhaustion, plus access-log fields.

## Open Questions for Community Discussion

Resolved during review (retry injection, wake-firing heuristic, naming, per-backend scope, and
`aigw` parity — see revision history / PR discussion for the prior alternatives considered). One
question remains genuinely open:

1. Retrying on 503 cannot distinguish a local `no_healthy_upstream` 503 from a genuine upstream 503.
   For a backend explicitly opted into `coldStart` this seems acceptable (and bounded by the
   user-configured retry budget); should we instead scope 503 retries with response-flag/
   header-based gating? No consensus yet — looking for input.
