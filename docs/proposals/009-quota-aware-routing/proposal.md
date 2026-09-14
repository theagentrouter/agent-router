# Quota-Aware Routing Proposal

## Overview

`QuotaPolicy` currently enforces token quota for the backend that Envoy has already selected.
This proposal is the inspiration of that implementation with quota-aware backend selection so AI Gateway can route
to Provisioned Throughput (PT) backends while quota is available and fall back to On-Demand capacity
when PT quota is exhausted.

The system reuses existing `AIGatewayRoute` backend refs and routing rules to define endpoint pools.
Quota checks are performed with backend/model descriptors derived from `QuotaPolicy`, and the target
routing path exposes quota-available backends to Envoy before final backend selection.

## Goals

1. **Capacity-Aware Routing**: Route requests to PT backends when quota is available, automatically fallback to on-demand backends when PT quota is exhausted
2. **Reuse Existing Primitives**: Leverage existing `backendRefs` and routing rules to define PT and on-demand endpoint pools across multiple regions/providers

## Architecture

The target architecture has two related paths:

1. **Pre-routing availability check**: evaluate candidate backends for the requested model and expose
   which candidates still have quota before backend selection.
2. **Post-response quota charging**: keep the existing stream-done charging path so actual token usage
   updates the backend/model quota counters after the selected backend responds.

Rate limit configuration is based on the metadata and limits set for a given model override name.
The metadata represents the backend descriptor identity, currently `backend_name`
(`namespace/backend`) plus `model_name_override`. During xDS translation, the Envoy extension server
builds route-level rate limit actions for the matching route backend refs and attaches the related
descriptor sets as `typed_per_filter_config` for the route-level rate limit filter. Those descriptor
sets are parsed from the `QuotaPolicy` limits and `clientSelectors` that apply to each backend/model
pair.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Target Request Flow                                 │
└─────────────────────────────────────────────────────────────────────────────┘

                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                  Router-Level AI Gateway ExtProc Filter                     │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │  1. Parse request and resolve the requested model                   │    │
│  └─────────────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Quota Rate Limit Service                                │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │  1. Receives candidate backend/model descriptor sets                │    │
│  │  2. Evaluates each backend quota                                    │    │
│  │  3. Returns OK with passedDescriptors when any candidate passes   │    │
│  │  4. Returns 429 only when all candidate backends are exhausted      │    │
│  └─────────────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Envoy Router                                        │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │  Selects from available backends using route priority and weight    │    │
│  └─────────────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┼─────────────────┐
                    │               │                 │
            ┌───────▼───────┐ ┌─────▼──────┐  ┌───────▼───────┐
            │ Backend 1 (PT)│ │ Backend 2  │  │ Backend 3 (OD)│
            │ priority: 0   │ │ priority: 0│  │ priority: 1   │
            │ AWS us-east-1 │ │ GCP central│  │ Anthropic API │
            └───────┬───────┘ └─────┬──────┘  └───────┬───────┘
                    │               │                 │
                    └───────────────┼─────────────────┘
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Existing Stream-Done Quota Charging                     │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │  1. ext_proc records selected backend/model dynamic metadata        │    │
│  │  2. ext_proc computes quota_cost from response token usage          │    │
│  │  3. rate limit filter charges backend/model quota using hits_addend │    │
│  └─────────────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Key Design Decisions

### 1. Per-Backend Quota Check at Router-Level Rate Limit Filter

The **router-level rate limit filter** uses backend/model descriptors to check quota before the
router sends the request upstream:

**Configuration:**

- A single router-level rate limit filter contains descriptors for quota-enabled backends
- Rate limit descriptors include backend name and model override name for granular tracking
- Cost calculation based on model provider pricing (input/output tokens, cached tokens, etc.)
- Existing descriptors are configured with `quota_mode: true`; the proposed routing extension needs
  an explicit metadata contract that reports which candidate descriptors still have quota available

**How It Works:**

- Today, request-time rate limit actions enforce quota for the descriptors attached to the generated
  route, and stream-done actions charge the selected backend/model quota after token usage is known
- The target behavior evaluates candidate backend/model descriptors for the matched route rule
- Backends that do not exceed their quota appear in the proposed `passedDescriptors`
  metadata
- The metadata identity must include the `backend_name` and `model_name_override` descriptor values
- Envoy uses this metadata to constrain route selection to quota-available candidates

**Routing Decision (in Envoy):**

- Envoy uses the available-backend metadata to filter the route's existing backend refs before
  applying priority and weight

### 2. Reuse Existing BackendRefs for Endpoint Pools

Endpoint pools are defined by the existing `AIGatewayRoute.rules[].backendRefs` list. A
`QuotaPolicy` attaches to individual `AIServiceBackend` resources with `targetRefs`; it does not
define the pool. During route translation, the extension server can resolve the route's backend refs,
find the `QuotaPolicy` resources that target those backends, and build the route-level rate limit
actions for the relevant backend/model descriptor sets.

Each backend ref contributes:

- `name`: the candidate `AIServiceBackend`.
- `modelNameOverride`: the model name used in the quota descriptor. The matching
  `QuotaPolicy.spec.perModelQuotas[].modelName` must use this value.
- `priority`: the fallback tier. Lower numbers are preferred.
- `weight`: distribution within the same priority tier after exhausted backends are removed.

The quota-aware routing decision should preserve the existing route semantics:

1. Build the candidate set from the matching route rule's backend refs.
2. Filter out candidates whose backend/model quota is exhausted.
3. Select from the lowest remaining priority value.
4. Apply `weight` among available candidates with that same priority.
5. Return `429` only when no candidate remains.

```yaml
backendRefs:
  # Provisioned throughput pool. These are preferred while quota is available.
  - name: aws-claude-pt-us-east-1
    modelNameOverride: claude-4-sonnet
    priority: 0
    weight: 50
  - name: aws-claude-pt-us-west-2
    modelNameOverride: claude-4-sonnet
    priority: 0
    weight: 50

  # On-demand fallback. Used only when priority 0 candidates are exhausted.
  - name: anthropic-claude-ondemand
    modelNameOverride: claude-4-sonnet
    priority: 1
    weight: 1
```

### 3. Preserve Stream-Done Quota Charging

The availability check determines whether a backend can receive the request, but it does not know
the final token cost. The implemented stream-done path remains the source of truth for charging:

1. ext_proc records the selected backend as `ai_service_backend_name` and the model as
   `model_name_override`.
2. ext_proc computes `quota_cost` from response token usage and the `QuotaPolicy` cost expression.
3. The route-level rate limit action with `apply_on_stream_done` charges the selected
   backend/model descriptor with `hits_addend`.

This preserves the current accounting model while adding a pre-routing availability signal.

## API Design

### AIServiceBackend

`AIServiceBackend` continues to describe a concrete upstream backend. It does not carry a
`backendQuotaRef`; quota is attached from `QuotaPolicy.spec.targetRefs`.

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: aws-claude-pt-us-east-1
  namespace: ai-gateway
spec:
  schema:
    name: AWSBedrock
  backendRef:
    group: gateway.envoyproxy.io
    kind: Backend
    name: bedrock-us-east-1
```

### QuotaPolicy

`QuotaPolicy` is attached to one or more `AIServiceBackend` resources with `targetRefs`. Per-model
quota entries are keyed by `modelName`, and that value must match the `modelNameOverride` configured
on the route backend ref for the quota to apply to that backend.

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: QuotaPolicy
metadata:
  name: aws-claude-pt-us-east-1-quota
  namespace: ai-gateway
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: aws-claude-pt-us-east-1
  perModelQuotas:
    - modelName: claude-4-sonnet
      quota:
        mode: Shared
        costExpression: "input_tokens + output_tokens * 3u + cached_input_tokens / 10u"
        defaultBucket:
          limit: 20000
          duration: "1m"
        bucketRules:
          - clientSelectors:
              - headers:
                  - name: service_tier
                    type: Exact
                    value: reserved
            quota:
              limit: 20000
              duration: "1m"
          - clientSelectors:
              - headers:
                  - name: service_tier
                    type: Exact
                    value: default
            quota:
              limit: 10000
              duration: "1m"
```

### AIGatewayRoute Backend Pool

`AIGatewayRoute.rules[].backendRefs` defines the candidate pool for quota-aware routing. `priority`
orders fallback tiers, and `weight` distributes traffic across available backends within the same
priority. Each backend ref that participates in model-specific quota needs a `modelNameOverride`
matching the related `QuotaPolicy.spec.perModelQuotas[].modelName`.

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: claude-route
  namespace: ai-gateway
spec:
  parentRefs:
    - name: ai-gateway

  rules:
    - matches:
        - headers:
            - name: x-ai-eg-model
              value: claude-4-sonnet

      backendRefs:
        # Provisioned throughput pool.
        - name: aws-claude-pt-us-east-1
          modelNameOverride: claude-4-sonnet
          priority: 0
          weight: 50
        - name: aws-claude-pt-us-west-2
          modelNameOverride: claude-4-sonnet
          priority: 0
          weight: 50

        # On-demand fallback.
        - name: anthropic-claude-ondemand
          modelNameOverride: claude-4-sonnet
          priority: 1
          weight: 1
```

## Rate Limit Service Configuration

Rate limit configuration is derived from the `QuotaPolicy` resources that target the backends in an
`AIGatewayRoute` rule. The rate limit service config is keyed by backend metadata and
`modelNameOverride`, and the route-level rate limit actions emit descriptor sets for each candidate
backend/model pair.

The controller builds the rate limit service descriptor tree from:

- `QuotaPolicy.spec.targetRefs`: the `AIServiceBackend` resources represented by `backend_name`.
- `QuotaPolicy.spec.perModelQuotas[].modelName`: the model quota represented by
  `model_name_override`.
- `quota.defaultBucket`: the default backend/model quota.
- `quota.bucketRules`: additional client-selector buckets, currently based on header selectors.
- `quota.costExpression`: the expression ext_proc evaluates into `quota_cost` for stream-done
  charging.

The Envoy extension server then builds route-level rate limit actions and attaches them to the
generated route's rate limit `typed_per_filter_config`. Those actions send the related descriptor
sets to the rate limit filter based on the limits and client selectors defined in `QuotaPolicy`.

The same descriptor tree supports two checks:

1. **Request-time availability check**: send candidate backend/model descriptors in QuotaMode. The
   rate limit service reports which descriptors still have quota available, for example through
   the proposed `passedDescriptors` metadata.
2. **Stream-done charging**: after a backend responds, ext_proc writes selected backend/model
   metadata and computes `quota_cost`; the rate limit filter charges the matching descriptor using
   `hits_addend`.

### Per-Backend Quota Descriptors (Router Level — QuotaMode)

```yaml
domain: ai-gateway-quota
descriptors:
  # AWS Claude PT us-east-1 backend
  - key: backend_name
    value: ai-gateway/aws-claude-pt-us-east-1
    descriptors:
      - key: model_name_override
        value: claude-4-sonnet
        rate_limit:
          unit: minute
          requests_per_unit: 20000 # PT backend capacity
        quota_mode: true

  # AWS Claude PT us-west-2 backend
  - key: backend_name
    value: ai-gateway/aws-claude-pt-us-west-2
    descriptors:
      - key: model_name_override
        value: claude-4-sonnet
        rate_limit:
          unit: minute
          requests_per_unit: 15000
        quota_mode: true

  # On-demand backend.
  - key: backend_name
    value: ai-gateway/anthropic-claude-ondemand
    descriptors:
      - key: model_name_override
        value: claude-4-sonnet
        rate_limit:
          unit: minute
          requests_per_unit: 1000000
        quota_mode: true
```

Bucket rules add descriptor levels under the backend/model path. For example, an exact
`service_tier: reserved` selector adds a stable descriptor key/value for that rule:

```yaml
domain: ai-gateway-quota
descriptors:
  - key: backend_name
    value: ai-gateway/aws-claude-pt-us-east-1
    descriptors:
      - key: model_name_override
        value: claude-4-sonnet
        descriptors:
          - key: rule-0-service_tier|reserved-match-0
            value: rule-0-service_tier|reserved-match-0
            rate_limit:
              unit: minute
              requests_per_unit: 20000
            quota_mode: true
          - key: rule-1-service_tier|default-match-0
            value: rule-1-service_tier|default-match-0
            rate_limit:
              unit: minute
              requests_per_unit: 10000
            quota_mode: true
          - key: rule-2-match--1
            value: rule-2-match--1
            rate_limit:
              unit: minute
              requests_per_unit: 20000
            quota_mode: true
```

### Route-Level Rate Limit Actions

For each generated AI Gateway route, the extension server creates rate limit actions that match the
descriptor tree above.

Request-time actions use static descriptor values for candidate backends and models:

```yaml
typed_per_filter_config:
  envoy.filters.http.ratelimit/ai-gateway-quota:
    "@type": type.googleapis.com/envoy.extensions.filters.http.ratelimit.v3.RateLimitPerRoute
    domain: ai-gateway-quota
    rate_limits:
      - actions:
          - generic_key:
              descriptor_key: backend_name
              descriptor_value: ai-gateway/aws-claude-pt-us-east-1
          - generic_key:
              descriptor_key: model_name_override
              descriptor_value: claude-4-sonnet
```

Stream-done actions read the selected backend/model from dynamic metadata and charge the actual
response cost:

```yaml
typed_per_filter_config:
  envoy.filters.http.ratelimit/ai-gateway-quota:
    "@type": type.googleapis.com/envoy.extensions.filters.http.ratelimit.v3.RateLimitPerRoute
    domain: ai-gateway-quota
    rate_limits:
      - apply_on_stream_done: true
        hits_addend:
          format: "%DYNAMIC_METADATA(io.envoy.ai_gateway:quota_cost)%"
        actions:
          - metadata:
              descriptor_key: backend_name
              source: DYNAMIC
              metadata_key:
                key: io.envoy.ai_gateway
                path:
                  - key: ai_service_backend_name
          - metadata:
              descriptor_key: model_name_override
              source: DYNAMIC
              metadata_key:
                key: io.envoy.ai_gateway
                path:
                  - key: model_name_override
```

## Sequence Diagram

### Implemented: Backend/Model Quota Enforcement

```
┌──────┐  ┌─────────────────┐  ┌──────────┐  ┌─────────┐  ┌─────────┐  ┌──────────────┐
│Client│  │Envoy Route      │  │Rate Limit│  │Envoy    │  │Backend  │  │AI Gateway    │
│      │  │RL Filter        │  │Service   │  │Router   │  │         │  │ext_proc      │
└──┬───┘  └────────┬────────┘  └────┬─────┘  └────┬────┘  └────┬────┘  └──────┬───────┘
   │ POST /chat     │                │             │            │              │
   │───────────────>│                │             │            │              │
   │                │ Check backend/model quota    │            │              │
   │                │ descriptors for this route   │            │              │
   │                │───────────────>│             │            │              │
   │                │        OK      │             │            │              │
   │                │<───────────────│             │            │              │
   │                │                │             │ Envoy selects backend     │
   │                │─────────────────────────────>│            │              │
   │                │                │             │ Route request             │
   │                │                │             │───────────>│              │
   │                │                │             │            │ Response     │
   │                │                │             │<───────────│              │
   │                │                │             │            │              │
   │                │ Response/token usage observed by ext_proc │              │
   │                │────────────────────────────────────────────────────────>│
   │                │                │             │            │ quota_cost   │
   │                │<────────────────────────────────────────────────────────│
   │                │ Stream-done charge using selected                       │
   │                │ backend/model metadata and hits_addend                  │
   │                │───────────────>│             │            │              │
   │                │      charged   │             │            │              │
   │                │<───────────────│             │            │              │
   │<───────────────│                │             │            │              │
   │ Response       │                │             │            │              │
```

In the implemented path, Envoy owns route and backend selection. The quota rate limit filter enforces
the backend/model descriptors configured on the generated route, and AI Gateway ext_proc contributes
the dynamic metadata and `quota_cost` used for stream-done charging. If the applicable
backend/model quota is exhausted, the request can be rejected with `429`, but the current
implementation does not use quota state to make Envoy select a different backend.

### Target: Quota-Aware Backend Selection

```
┌──────┐  ┌────────────┐  ┌─────────────────┐  ┌──────────┐  ┌─────────┐
│Client│  │AI Gateway  │  │Envoy Route      │  │Rate Limit│  │Envoy    │
│      │  │ext_proc    │  │RL Filter        │  │Service   │  │Router   │
└──┬───┘  └─────┬──────┘  └────────┬────────┘  └────┬─────┘  └────┬────┘
   │ POST /chat │                  │                │             │
   │───────────>│                  │                │             │
   │            │ Parse request and resolve model   │             │
   │            │─────────────────>│                │             │
   │            │                  │ Check candidate backend/model │
   │            │                  │ descriptors for matched route │
   │            │                  │───────────────>│             │
   │            │                  │ passedDescriptors   │
   │            │                  │ = [PT-west-2, OD]             │
   │            │                  │<───────────────│             │
   │            │                  │ Publish available backend     │
   │            │                  │ metadata                      │
   │            │                  │                              │
   │            │                  │                │ Select from  │
   │            │                  │                │ available P0 │
   │            │                  │                │ backends,    │
   │            │                  │                │ then weight  │
   │            │                  │                │             │
   │            │                  │                │ Route to     │
   │            │                  │                │ PT-west-2    │
   │<──────────────────────────────────────────────────────────────│
   │ Response   │                  │                │             │
```

If all priority `0` provisioned-throughput backends are absent from
`passedDescriptors`, Envoy selects an available priority `1` backend. If no candidate
backend is available, the rate limit filter returns `429` before a backend is selected.

### Legend

- **Priority 0 (P0)**: Provisioned Throughput backends (PT-east-1, PT-west-2)
- **Priority 1 (P1)**: On-demand backends (fallback)
- **passedDescriptors**: Proposed dynamic metadata containing descriptor identities for
  backends that still have quota available. Each entry must identify `backend_name` and
  `model_name_override`.
- **Stream-done charge**: The already-implemented path that charges actual response token usage to
  the selected backend/model quota.

## Metrics and Observability

```go
var (
	quotaCheckTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_quota_checks_total",
			Help: "Total quota checks per backend",
		},
		[]string{"backend", "model", "result"}, // result: allowed, exhausted, error
	)

	quotaFallbackTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_quota_fallbacks_total",
			Help: "Total fallbacks due to quota exceeded",
		},
		[]string{"from_backend", "to_backend", "from_priority", "to_priority"},
	)

	quotaRouteDecisionTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_quota_route_decisions_total",
			Help: "Total quota-aware routing decisions",
		},
		[]string{"backend", "model", "priority", "decision", "reason"},
	)

	quotaUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ai_gateway_quota_utilization_ratio",
			Help: "Current quota utilization (0.0-1.0+)",
		},
		[]string{"backend", "model", "capacity_type"},
	)
)
```

## Implementation Items

### Implemented

- `QuotaPolicy` API with `targetRefs`, `perModelQuotas`, `defaultBucket`, `bucketRules`,
  header-based client selectors, `costExpression`, and bucket-rule `shadowMode`.
- Quota descriptor generation keyed by `backend_name` (`namespace/backend`) and
  `model_name_override`.
- QuotaPolicy controller reconciliation that resolves targeted `AIServiceBackend` resources, builds
  rate limit service configs, merges policy descriptors deterministically, and pushes xDS snapshots
  to the AI Gateway rate limit service.
- Envoy extension server injection of the quota rate limit cluster, quota HTTP rate limit filter,
  and per-route `RateLimitPerRoute` `typed_per_filter_config`.
- Route-level request-time rate limit actions for backend/model descriptors and bucket-rule
  descriptors.
- ext_proc quota cost injection from `QuotaPolicy` cost expressions, using the shared `quota_cost`
  dynamic metadata key.
- ext_proc selected backend/model metadata emission with `ai_service_backend_name` and
  `model_name_override`.
- Stream-done quota charging with `apply_on_stream_done` and `hits_addend` from
  `%DYNAMIC_METADATA(io.envoy.ai_gateway:quota_cost)%`.
- Runtime enforcement for the selected backend/model quota: once the quota is exhausted, subsequent
  matching requests receive `429`.

### Remaining Work

- Define the exact rate limit filter metadata contract for `passedDescriptors` and how
  backend/model descriptor identities are represented.
- Extend request-time quota checks to evaluate every candidate backend in the matched
  `AIGatewayRoute` rule and publish the available candidate set.
- Teach Envoy routing to consume the available-backend metadata and select only from
  quota-available candidates.
- Preserve `priority` and `weight` semantics after exhausted backends are filtered out.
- Return `429` only when no backend candidate has available quota.
- Add metrics for quota availability checks, quota-based fallback, selected backend, and all-exhausted
  decisions.
- Add unit and e2e coverage for priority `0` fallback, on-demand fallback, all-exhausted behavior,
  and stream-done charging after quota-aware selection.

## Testing and Acceptance

- Existing selected-backend quota enforcement continues to pass: a backend/model over quota returns
  `429` for subsequent matching requests.
- A priority `0` backend that is exhausted is excluded from `passedDescriptors` and is not
  selected for new requests.
- Multiple available priority `0` backends are selected according to `weight`.
- When all priority `0` backends are exhausted, Envoy selects an available priority `1` backend.
- When every candidate backend is exhausted, the request returns `429` before routing to a backend.
- Stream-done charging updates quota for the backend/model that actually handled the request after
  quota-aware selection.

## Compatibility and Limitations

- `QuotaPolicy` remains `v1alpha1`; `AIServiceBackend` and `AIGatewayRoute` examples use `v1beta1`.
- Quota attachment remains policy-driven through `QuotaPolicy.spec.targetRefs`; there is no
  `AIServiceBackend.spec.backendQuotaRef`.
- Per-model quota matching depends on `AIGatewayRoute.rules[].backendRefs[].modelNameOverride`
  matching `QuotaPolicy.spec.perModelQuotas[].modelName`.
- Backend-wide `serviceQuota` is not part of the implemented enforcement path for this proposal.
- `bucketRules.clientSelectors` currently rely on header selectors for quota buckets.

## Open Questions

1. Should fallback be transparent to the client or return a header indicating fallback occurred?

2. How to handle streaming requests where token count is unknown upfront?
   - Option: Estimate based on input tokens
   - Option: Reserve capacity and reconcile after response

3. Should there be a "sticky" preference to avoid flip-flopping between backends near quota boundary?
