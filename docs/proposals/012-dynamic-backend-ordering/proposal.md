# Dynamic Per-Request LLM Backend Ordering Proposal

## Problem

Envoy Gateway's `AIGatewayRoute` can already configure multiple `AIServiceBackend` refs with
`Priority` tiers, which gives native failover: every request starts at priority `0` and only
advances to a higher-numbered tier when the current one is unhealthy. This is useful for static
failover, but it is not flexible enough for routing decisions that should differ per request.

For example, an AI gateway may want to:

- Pin a client session or conversation to the Azure OpenAI PTU endpoint that last served it,
  to save cost by reusing cached tokens, falling back to a different PTU endpoint and then a
  pay-as-you-go provider only if necessary.
- Route a request to a cheaper or lower-latency provider first based on request metadata,
  budget, or observed backend load, e.g., in multi-tenant deployments where each tenant may
  have different preferred providers or cost/latency rules.
- Avoid a backend that is known to be rate-limited or overloaded for this specific request.

None of these can be expressed with static priorities. The `Priority` field is fixed at config
load time, and Envoy's native priority load always starts at `0` for the initial attempt. Existing
`RetryPriority` extensions only affect retries, not the first attempt, so they cannot steer the
first hop to a different backend either.

What's needed, independent of any particular mechanism, is for the backend attempted on a given
request — and on every retry of that request — to be selectable by logic that lives outside
static Envoy config and can vary from one request to the next, without that logic having to be
re-expressed as a combinatorial set of static Envoy routing rules, and with a safe, well-defined
fallback for requests that logic has no opinion about.

## Overview

This proposal describes a mechanism for dynamically ordering LLM backends on a **per-request**
basis, driven by a custom `ext_proc` filter, and enforced by Envoy for both the initial attempt
and every retry. It spans two repositories:

- `envoy` — a new `envoy.load_balancing_policies.header_order` LoadBalancingPolicy extension
  (generic, not AI-Gateway-specific; proposed for upstream).
- `ai-gateway` — extension-server wiring that attaches it to generated clusters (necessarily
  AI-Gateway-specific; not something Envoy or Envoy Gateway core can express on their own).

**What we'd like feedback on** (see [Open Questions](#open-questions) for detail):

- Is a new `LoadBalancingPolicy` extension the right home for this, and is the
  wrapping-a-fallback-policy approach acceptable, or is there a more idiomatic mechanism we
  missed?
- Is reusing the existing `AIGatewayRouteRuleBackendRef.Priority` field as the addressing
  scheme (rather than adding a new field) the right call, and is a `PostTranslateModify`
  cluster patch an acceptable place for this vs. some other integration point?

## Goals

1. **Arbitrary per-request backend order**: honor a fully arbitrary, per-request ordering of
   LLM backends decided by an upstream `ext_proc` filter (e.g. "sticky to the last-served Azure
   OpenAI PTU endpoint, then fail over to a different PTU, then a different provider").
2. **Cover initial attempt and retries alike**: the ordering must apply to the very first
   attempt, not just retries.
3. **Work with FQDN-addressed backends**: backends are addressed by FQDN (including AWS/Azure
   PrivateLink VPC-endpoint DNS names — still hostnames, not static IPs) across multiple cloud
   providers, so the mechanism must work for named/DNS-resolved endpoints, not just literal IPs.
4. **No decision-logic duplication in Envoy config**: the order is decided per-request by logic
   that already exists in the ext_proc; we don't want to duplicate that decision-making inside
   Envoy config (e.g. as a combinatorial set of static routing rules).

## Architecture

```
┌──────────────┐  sets dynamic metadata:        ┌──────────────────────────────┐
│ Custom       │  envoy.ai_gateway.endpoint_order│ Envoy Router filter          │
│ ext_proc     │  order: [3, 0, 2]               │ picks route/cluster as usual │
│ (unmodified) ├────────────────────────────────►│ (unchanged)                 │
└──────────────┘                                 └──────────────┬───────────────┘
                                                                 │ every attempt
                                                                 │ (initial + retries)
                                                                ▼
                                          ┌──────────────────────────────────────┐
                                          │ envoy.load_balancing_policies.        │
                                          │ header_order (new Envoy extension)    │
                                          │  - chooseHost() reads attemptCount()  │
                                          │    -> index into the metadata order   │
                                          │  - order[attemptCount()-1] -> priority│
                                          │  - forces 100% load on that priority, │
                                          │    delegates host pick to fallback    │
                                          │  - falls back entirely to             │
                                          │    fallback_policy if metadata is     │
                                          │    absent/malformed or unhealthy      │
                                          └──────────────────┬───────────────────┘
                                                              │ fallback_policy picks
                                                              │ the actual host within
                                                              │ the chosen priority
                                                              ▼
                                          ┌──────────────────────────────────────┐
                                          │ Cluster's PrioritySet                  │
                                          │  P0 = Backend A (AIGatewayRouteRuleBackendRef.Priority=0) │
                                          │  P1 = Backend B (Priority=1)           │
                                          │  P2 = Backend C (Priority=2)           │
                                          └────────────────────────────────────────┘
```

## Key Design Decisions

### 1. A custom Envoy `LoadBalancingPolicy` extension, not a `RetryPriority` extension

A custom `LoadBalancingPolicy` extension, `envoy.load_balancing_policies.header_order`, wraps a
cluster's existing LB policy. `Upstream::LoadBalancer::chooseHost` is called for **every**
host-selection attempt on a cluster — the initial attempt and every retry alike — and receives a
`LoadBalancerContext` exposing the request's `StreamInfo`, including `attemptCount()` (`1` for
the initial attempt, `2` for the 1st retry, ...). The extension:

1. Reads an ordered list of priority indices from a configured dynamic-metadata
   namespace/key (set upstream by the ext_proc, e.g. `[3, 0, 2]`).
2. Uses `attemptCount() - 1` to index into that list, picking the priority for *this* attempt.
3. Forces 100% load onto that priority and delegates the actual host pick within it to a
   configured `fallback_policy` (round robin, least request, whatever the cluster would
   otherwise use) — so it never reimplements host-selection algorithms itself.
4. Falls back to `fallback_policy` unmodified if the metadata is absent/malformed, the listed
   priority is out of range, or has no healthy hosts (skipping forward through the list to the
   next entry that does).

This covers the initial attempt *and* every retry, using a single generic mechanism, without
requiring literal IPs and without combinatorial route-rule expansion.

An earlier prototype used a `RetryPriority` extension (`envoy.retry_priorities.header_order`)
instead. It works, but Envoy's router only calls `RetryPriority::determinePriorityLoad` when
actually retrying (`is_retry_ == true`, set inside `doRetry()`) — for the initial attempt it
short-circuits straight to the cluster's native priority load. So it can only ever steer
retries, never the initial attempt, which is the gap that led to the `LoadBalancingPolicy`
design instead.

### 2. Dynamic metadata, not a request header, as the signal

Dynamic metadata is used because it can't leak upstream to the backend (no header-strip filter
needed) and Envoy holds it as a native list of numbers, not a string that needs runtime parsing.

### 3. Reuse the existing `AIGatewayRouteRuleBackendRef.Priority` field for addressing

Each named backend gets its own dedicated Envoy priority tier via the **already-existing**
`AIGatewayRouteRuleBackendRef.Priority` field — no new CRD field. The extension only decides
*which* priority tier gets the load on a given attempt. This is the same tiering concept the
existing Envoy Gateway fallback behavior already uses, so no new user-facing configuration
concept is introduced (see [Open Questions](#open-questions) for whether this overloading is
acceptable).

### 4. Precondition: a rule's backends must merge into one Envoy cluster

`header_order` only reorders load *within a single cluster's* `PrioritySet` — it can't switch
between separate named clusters. This works when Envoy Gateway's HTTPRoute translator merges
every backendRef of a rule into one cluster (each backendRef as a `LocalityLbEndpoints` entry at
its own `Priority`). Envoy Gateway does this by default for a typical AI Gateway rule (all
FQDN-addressed backends, no per-backendRef Gateway API filters); it splits into per-backendRef
`WeightedClusters` instead when backends mix address types, have per-backendRef filters, mixed
protocol/SNI settings, or non-uniform zone-aware LB (`RouteDestination.NeedsClusterPerSetting()`
in `envoy-gateway`'s `internal/ir/xds.go`). In that split case `header_order` has nothing to
reorder, since no single `PrioritySet` spans the backends — the `ai-gateway` wiring detects and
skips this case rather than silently misbehaving. This is an accepted constraint, not a blocker:
it matches the same merge precondition that today's `Priority`-based failover already requires.

## Example: user-facing configuration

Nothing changes in how a user authors `AIGatewayRoute`/`AIServiceBackend`/`BackendTrafficPolicy`
config — this feature reuses the existing `Priority` field as-is. What changes is *runtime
behavior*: the order in which priority tiers are tried per request is no longer Envoy's fixed
native fallback (always tier `0` first, advancing only on unhealthy tiers) but whatever an
upstream ext_proc decides for that specific request.

Given a route with three backends already configured at distinct priorities:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: my-route
  namespace: ns
spec:
  parentRefs:
    - name: my-gateway
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: gpt-4o
      backendRefs:
        - name: backend-a   # Priority tier 0
          priority: 0
        - name: backend-b   # Priority tier 1
          priority: 1
        - name: backend-c   # Priority tier 2
          priority: 2
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: backend-a
  namespace: ns
spec:
  schema:
    name: OpenAI
  backendRef:
    name: backend-a
    kind: Backend
    group: gateway.envoyproxy.io
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: backend-a
  namespace: ns
spec:
  endpoints:
    - fqdn:
        hostname: backend-a.example.com
        port: 443
# backend-b (Priority 1, backend-b.example.com) and backend-c (Priority 2,
# backend-c.example.com) follow the same AIServiceBackend + Backend shape, omitted for brevity.
```

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: my-route
  namespace: ns
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: my-route
  retry:
    numRetries: 2 # unaffected by this feature; shown only to match the xDS example below
```

Today, without this feature, every request against this route — and every retry — starts at
Priority tier `0` (`backend-a`) and only advances to tier `1`/`2` if a tier is unhealthy. With
this feature, an ext_proc that, say, tracks which Azure OpenAI PTU endpoint a client was last
pinned to can instead emit, as dynamic metadata, a per-request override of that order:

```yaml
envoy.ai_gateway.endpoint_order:
  order: [2, 0, 1]   # this request: try backend-c first, then backend-a, then backend-b
```

Envoy then honors that exact order for this request's initial attempt and both retries — `backend-c`
on the initial attempt, `backend-a` on the 1st retry, `backend-b` on the 2nd — without any change
to the `AIGatewayRoute`/`AIServiceBackend`/`BackendTrafficPolicy` config above. A different
request on the same route can get a different `order` (or none at all, which falls back to the
native `[0, 1, 2]` behavior) purely based on what the ext_proc decides — no redeploy or route
change involved.

## Example: generated route and cluster

Continuing the example above: illustrative, hand-abbreviated xDS (irrelevant fields like
timeouts/health checks omitted) for the rule and `BackendTrafficPolicy` shown above — three
`AIServiceBackend` refs at `Priority` `0`, `1`, and `2`, with 2 retries configured. Route
matching/config is **unchanged** by this feature — the only diff from what Envoy Gateway would
generate today is the cluster's `load_balancing_policy`.

**Route** (unaffected — shown for context only):

```yaml
name: httproute/ns/my-route/rule/0/match/0
match:
  prefix: "/"
route:
  cluster: httproute/ns/my-route/rule/0
  retry_policy: # from BackendTrafficPolicy, not touched by this feature
    retry_on: "5xx,reset"
    num_retries: 2
```

**Cluster** — before this feature, `load_balancing_policy` would just be the
`least_request` entry now nested under `fallback_policy`:

```yaml
name: httproute/ns/my-route/rule/0
type: STRICT_DNS
load_assignment:
  cluster_name: httproute/ns/my-route/rule/0
  endpoints:
    - priority: 0
      lb_endpoints:
        - endpoint: { address: { socket_address: { address: backend-a.example.com, port_value: 443 } } }
    - priority: 1
      lb_endpoints:
        - endpoint: { address: { socket_address: { address: backend-b.example.com, port_value: 443 } } }
    - priority: 2
      lb_endpoints:
        - endpoint: { address: { socket_address: { address: backend-c.example.com, port_value: 443 } } }
load_balancing_policy:
  policies:
    - typed_extension_config:
        name: envoy.load_balancing_policies.header_order
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.load_balancing_policies.header_order.v3.HeaderOrder
          metadata_namespace: envoy.ai_gateway.endpoint_order
          metadata_key: order
          fallback_policy: # cluster's original load_balancing_policy, unchanged
            policies:
              - typed_extension_config:
                  name: envoy.load_balancing_policies.least_request
                  typed_config:
                    "@type": type.googleapis.com/envoy.extensions.load_balancing_policies.least_request.v3.LeastRequest
```

**Dynamic metadata** the ext_proc sets per request (drives which priority each attempt uses —
here: initial attempt → priority `2`, 1st retry → priority `0`, 2nd retry → priority `1`):

```yaml
envoy.ai_gateway.endpoint_order:
  order: [2, 0, 1]
```

## The `envoy` change (upstream ask)

New extension: `envoy.load_balancing_policies.header_order`.

**Proto** (`api/envoy/extensions/load_balancing_policies/header_order/v3/header_order.proto`):

```protobuf
message HeaderOrder {
  string metadata_namespace = 1 [(validate.rules).string = {min_len: 1}];
  string metadata_key = 2 [(validate.rules).string = {min_len: 1}];
  config.cluster.v3.LoadBalancingPolicy fallback_policy = 3
      [(validate.rules).message = {required: true}];
}
```

`fallback_policy` is required — used both as the child LB for host selection within the chosen
priority, and as the full fallback when the metadata is unusable.

**Implementation** (`source/extensions/load_balancing_policies/header_order/`):

- `HeaderOrderLbConfig` — parsed config; resolves the `fallback_policy`'s own
  `TypedLoadBalancerFactory` + `LoadBalancerConfig` at config-load time.
- `HeaderOrderLoadBalancer` (`Upstream::ThreadAwareLoadBalancer`) — holds the fallback LB,
  exposes a factory that creates the worker-local `LoadBalancerImpl`.
- `LoadBalancerImpl::chooseHost` — the core logic described above: parses the metadata order,
  indexes by `attemptCount()`, builds a `HealthyAndDegradedLoad` forcing 100% onto the chosen
  priority, wraps the `LoadBalancerContext` (`PriorityOverrideContext`, overriding only
  `determinePriorityLoad`) and delegates to `fallback_lb_->chooseHost(...)`.
- Standard `TypedLoadBalancerFactoryBase<HeaderOrder>` registered via `REGISTER_FACTORY` as
  `envoy.load_balancing_policies.header_order`; added to `extensions_build_config.bzl` next to
  the other `envoy.load_balancing_policies.*` entries.

**What's needed before this could be an upstream PR:**

- Bazel build verification (`bazel build //source/extensions/load_balancing_policies/header_order:config`).
- Unit tests mirroring an existing LB policy extension (e.g. `override_host`).
- Docs (`[#extension:]` protodoc entries, release notes).
- Confirmation that a new top-level extension (vs. e.g. an option on an existing policy) is
  the right shape, and that wrapping an arbitrary `fallback_policy` via a context-overriding
  shim is an acceptable pattern.

## The `ai-gateway` change

Even once `header_order` ships in stock Envoy, something still has to decide, per generated
`Cluster`, whether to wrap it in `header_order` and with what config — and that decision depends
on facts that only exist in the `AIGatewayRoute` CRD, which Envoy Gateway's core translator has
no concept of:

- Which rule a given generated cluster corresponds to (parsed from the cluster name).
- Whether that rule's backends configure more than one distinct `Priority`
  (`countDistinctBackendPriorities` — skips `InferencePool` refs, whose fallback is handled by
  the endpoint picker instead). If not, there's nothing to reorder.
- Whether the rule's backends actually merged into one cluster, per the precondition above (skip
  per-backendRef clusters).
- The dynamic-metadata namespace/key contract with AI Gateway's own ext_proc
  (`envoy.ai_gateway.endpoint_order` / `order` — new constants in
  `internal/internalapi/internalapi.go`).

There's no Envoy Gateway CRD field today for "wrap this cluster's LB in a named extension using
these AIGatewayRoute priorities," so nothing upstream of `ai-gateway` can make this call as-is.
The proposed implementation uses the extension server's existing `PostTranslateModify` hook —
the same mechanism AI Gateway already uses for `PerTryIdleTimeout` and ext_proc filter insertion
— to patch generated clusters after Envoy Gateway's translation:

- **`applyHeaderOrderLoadBalancingPolicy`** (`internal/extensionserver/post_translate_modify.go`)
  walks `req.Clusters` and calls **`maybeSetHeaderOrderLoadBalancingPolicy`** on each, which:
  1. Skips clusters not owned by an `AIGatewayRoute`, or that are per-backendRef clusters.
  2. Skips if the owning route/rule is missing or stale.
  3. Skips if the rule has fewer than 2 distinct `Priority` values.
  4. Skips if the cluster has no existing `LoadBalancingPolicy` to use as `fallback_policy`
     (verified against Envoy Gateway's cluster translator: it always populates the modern
     `Cluster.LoadBalancingPolicy` field, defaulting to `least_request` — never the legacy
     `lb_policy` enum — so this shouldn't trigger for ordinary AI Gateway clusters).
  5. Otherwise, replaces `cluster.LoadBalancingPolicy` with a single policy entry wrapping
     `header_order`, embedding the cluster's original `LoadBalancingPolicy` as `fallback_policy`.
- **`buildHeaderOrderLoadBalancingPolicyAny`** hand-encodes the `Any` payload for `HeaderOrder`
  (two strings + one submessage produced via `proto.Marshal` of the real
  `clusterv3.LoadBalancingPolicy` type) as a stopgap until generated Go bindings exist in
  `go-control-plane` for this extension; should be replaced with a normal `toAny(...)` call once
  those exist.

## End-to-End Request Flow

1. Ext_proc decides the order for this request and sets
   `envoy.ai_gateway.endpoint_order: {order: [3, 0, 2]}` as dynamic metadata.
2. Router matches the route/cluster as usual (unchanged).
3. `chooseHost` on the (now-wrapped) cluster LB: `attemptCount() == 1` → `order[0]` → priority
   `3`, if healthy; fallback policy picks the host within it.
4. If that attempt fails and `RetryPolicy.RetryOn` matches (configured separately via
   `BackendTrafficPolicy`, unaffected by this feature), Envoy retries: `attemptCount() == 2` →
   `order[1]` → priority `0`.
5. And so on, up to `NumRetries` or a successful response. If `order` runs out before attempts
   do, remaining attempts fall back to native LB entirely.
6. AI Gateway's upstream `ext_proc` filter is unaffected — it still reacts to whichever
   backend/host Envoy actually selected, exactly as today.

## Implementation Items

### 1. `envoy`: `header_order` LoadBalancingPolicy extension

- New proto (`header_order.proto`) with `metadata_namespace`, `metadata_key`, `fallback_policy`.
- `HeaderOrderLbConfig`, `HeaderOrderLoadBalancer`, `LoadBalancerImpl::chooseHost` as described
  above.
- Bazel `BUILD` files and `extensions_build_config.bzl` registration.
- Unit tests and protodoc entries.

### 2. `ai-gateway`: extension-server wiring

- New constants (`EndpointOrderMetadataNamespace`, `EndpointOrderMetadataKey`) in
  `internal/internalapi/internalapi.go`.
- `applyHeaderOrderLoadBalancingPolicy` / `maybeSetHeaderOrderLoadBalancingPolicy` /
  `buildHeaderOrderLoadBalancingPolicyAny` in `internal/extensionserver/post_translate_modify.go`.
- Unit tests: table-driven coverage of all skip branches plus the successful-wrap case.

### 3. Ext_proc (out of scope for these two repos)

- Emit `envoy.ai_gateway.endpoint_order` dynamic metadata per request based on whatever
  backend-selection logic already exists upstream of the router.

## Open Questions

**On the `envoy` extension:**

1. Is a new top-level `LoadBalancingPolicy` extension the right shape for "force a priority per
   attempt based on external signal, delegate the rest," or is there a better-fitting existing
   extension point?
2. Any concerns with the context-wrapping approach (`PriorityOverrideContext` overriding only
   `determinePriorityLoad`) to compose with an arbitrary `fallback_policy`?
3. Any concern with reading dynamic metadata (vs. a header) as the signal, from a LB policy
   specifically?

**On the `ai-gateway` integration:**

1. Is overloading `AIGatewayRouteRuleBackendRef.Priority` for this acceptable, or does ordering
   need its own field/semantics?
2. Is a `PostTranslateModify` cluster patch the right integration point, or is there a
   preferred alternative for this kind of "attach a custom xDS extension when a condition on the
   CRD holds" logic?
3. Given the dependency on an unmerged upstream Envoy extension, what's the preferred interim
   path — ship on a small internal Envoy patch until the upstream PR lands, or hold the
   ai-gateway-side change until Envoy support exists in a released version?

## Known Limitations

- Requires all of a rule's backends to merge into a single Envoy cluster.
- Granularity is Priority tier, not individual backend identity — backends needing independent
  ordering must have distinct `Priority` values (matches existing `Priority` semantics, so no
  new constraint, but worth calling out explicitly).
- If `order` is shorter than the number of attempts Envoy makes, the uncovered tail attempts
  silently fall back to native LB (intentional graceful degradation, but worth a validation
  warning if `BackendTrafficPolicy.NumRetries` commonly exceeds typical `order` length).
- Hand-rolled `Any` encoding on the Go side until generated bindings exist.
- Ext_proc itself is unmodified by this proposal — it still needs to be updated (outside these
  repos) to actually emit the `envoy.ai_gateway.endpoint_order` metadata.
