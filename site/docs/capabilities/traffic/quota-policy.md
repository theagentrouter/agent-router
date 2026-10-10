---
id: quota-policy
title: Quota Policy
sidebar_position: 6
---

# Quota Policy

`QuotaPolicy` enables token-based quota management for AI inference services in Agent Router.
When all related backend's quota are exceeded, requests are rejected with a `429 Too Many Requests` status code.

:::note QuotaPolicy vs. usage-based rate limiting
`QuotaPolicy` manages the platform's upstream consumption budget: how many tokens the gateway may
spend against a provider backend and model across all routes and consumers. [Usage-based rate
limiting](./usage-based-ratelimiting.md) manages consumer usage: how much a client may consume on a
route. Both can count tokens and return `429 Too Many Requests`, but they answer different questions:
whether the platform has exhausted its budget for a provider/model, or whether a client has exceeded
its allowance. See [Choosing a policy](#choosing-a-policy) for details.
:::

## Overview

Key features of QuotaPolicy:

- **Per-model token quotas** — assign token budgets to individual models served by an `AIServiceBackend`.
- **CEL cost expressions** — weight input, output, cached, and reasoning tokens differently when
  computing how much a request burns down a quota.
- **Client-selector bucket rules** — carve out per-tenant or per-header quotas using request attributes.
- **Shadow mode** — evaluate quota rules without enforcing them, for safe rollout.

## Choosing a Policy

Both policies use Envoy's rate limit protocol and Redis-backed counters, but `QuotaPolicy` is enforced
by a dedicated AI Gateway rate limit service. Usage-based rate limiting uses Envoy Gateway's global
rate limit service. Choose between them based on the budget's intent and scope:

|                  | `QuotaPolicy`                                                                                                                               | Usage-based rate limiting                                                                                             |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Configuration    | One AI Gateway policy containing the token cost and quota buckets                                                                           | `llmRequestCosts` on an `AIGatewayRoute`, plus an Envoy Gateway `BackendTrafficPolicy`                                |
| Scope            | Backend- and model-scoped - the budget follows an `AIServiceBackend` across every route that sends traffic to it                            | Route/Gateway-scoped - budgets are keyed by client descriptors on a route                                             |
| Token accounting | One default or custom CEL cost per model                                                                                                    | Multiple metadata keys can track and limit input, output, total, or custom token costs separately                     |
| Client budgets   | Default and header-selected buckets, with optional shadow mode                                                                              | Envoy Gateway client selectors and rate limit rules                                                                   |
| Enforcement      | In `Shared` mode, a request is denied only when all applicable quota buckets are exhausted; the quota is evaluated for the selected backend | A request is denied when any matched rate limit is exceeded; limits are evaluated from the route's client descriptors |
| Time windows     | Exactly one second, minute, hour, or day                                                                                                    | One second, minute, hour, day, month, or year                                                                         |

Use `QuotaPolicy` when the platform needs to protect its provider budget for a backend and model.
For example, a platform team can give every tenant a separate daily budget for an expensive model,
set a shared backend budget alongside those tenant budgets, and evaluate a new tenant rule in shadow
mode. The policy stays with the `AIServiceBackend` even when multiple routes send traffic to it.

Use usage-based rate limiting when you need to define each client's allowance on a route. It is also
the natural choice when you need lower-level Envoy Gateway controls, route-specific token cost
metadata, separate limits for input and output tokens, or a monthly or yearly window.

## How It Works

1. A `QuotaPolicy` is attached to one or more `AIServiceBackend` resources via `targetRefs`.
2. For each completed request, the token cost is computed using the configured cost expression
   (defaults to `total_tokens`).
3. The cost is charged against the matching quota bucket (the per-model default bucket, or a matching
   bucket rule).
4. When all related quota buckets for that model are exceeded, subsequent matching requests receive `429 Too Many Requests`.

### Cross-Namespace Backends

`targetRefs[].namespace` is optional. If it is omitted, the backend is looked up in the
`QuotaPolicy` namespace. Set it explicitly when the policy and backend are in different namespaces:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: QuotaPolicy
metadata:
  name: shared-provider-budget
  namespace: platform
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: provider
      namespace: providers
```

Same-namespace QuotaPolicy references do not require a `ReferenceGrant`. A cross-namespace
QuotaPolicy reference must be authorized by a `ReferenceGrant` in the AIServiceBackend namespace.
The grant must allow `QuotaPolicy` from the policy namespace to reference `AIServiceBackend`; a
`to.name` restricts the grant to one backend, while an omitted `to.name` allows all AIServiceBackends
in that namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-platform-quota
  namespace: providers
spec:
  from:
    - group: aigateway.envoyproxy.io
      kind: QuotaPolicy
      namespace: platform
  to:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: provider # omit name to allow every AIServiceBackend in providers
```

The QuotaPolicy and route relationships are independent. A route that references this backend from
another namespace still requires its own route-to-backend `ReferenceGrant`. Grant only the
controller service account the cross-namespace `get/list/watch` permissions it needs; a QuotaPolicy
grant does not authorize routing traffic.

:::tip Prerequisites
Quota enforcement requires two components that are not deployed by the AI Gateway Helm chart today:

1. **Redis** stores quota counters. See the
   [redis.yaml example](https://github.com/theagentrouter/agent-router/blob/main/examples/token_ratelimit/redis.yaml)
   for a simple deployment.
2. **A dedicated rate limit service** evaluates the `ai-gateway-quota` domain. It must use the AI
   Gateway controller's xDS server for configuration, with node ID `envoy-ai-gateway-ratelimit`, and
   listen at the controller's `quotaRateLimitServiceAddr`. The
   [quota E2E manifest](https://github.com/theagentrouter/agent-router/blob/main/tests/e2e/testdata/backend_quota_ratelimit.yaml)
   provides a deployment example.

Envoy Gateway's rate-limit addon is a separate service used by usage-based rate limiting. It is not
required for a QuotaPolicy-only deployment, although both services can use the same Redis instance.

By default, `controller.quotaRateLimitFailureModeDeny` is `false`. If the dedicated service is absent
or unreachable, quota checks fail open and requests continue without enforcement. Set it to `true`
if unavailable quota enforcement should reject requests instead.

For a deployment example, including the rate-limit service, Redis, xDS, service discovery, and
verification, see the
[quota E2E manifest](https://github.com/theagentrouter/agent-router/blob/main/tests/e2e/testdata/backend_quota_ratelimit.yaml).
:::

## Configuration

### Per-Model Quotas

Use `perModelQuotas` to apply a token budget to a specific model served by the targeted backend(s).

:::warning The model name must match the route
A `perModelQuotas` entry only applies when its `modelName` matches the `modelNameOverride` set on the
`AIGatewayRoute` rule's `backendRefs` for the targeted backend. If they do not match, the quota is
silently **not** applied.
:::

Given an `AIGatewayRoute` that routes a model to the backend:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
metadata:
  name: my-route
spec:
  rules:
    - backendRefs:
        - name: my-backend
          modelNameOverride: my-model # <-- the QuotaPolicy modelName must match this
```

attach a `QuotaPolicy` to the backend:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: QuotaPolicy
metadata:
  name: my-quota-policy
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: my-backend
  perModelQuotas:
    - modelName: "my-model"
      quota:
        mode: Shared
        defaultBucket:
          limit: 10000 # Maximum tokens allowed in the window.
          duration: "1h" # Sliding window.
```

You can attach quotas for multiple models, each with its own budget:

```yaml
perModelQuotas:
  - modelName: gpt-4
    quota:
      defaultBucket:
        limit: 10000 # Strict limit for the expensive model.
        duration: "1h"
  - modelName: gpt-3.5-turbo
    quota:
      defaultBucket:
        limit: 100000 # Higher limit for the cost-effective model.
        duration: "1h"
```

:::note
When multiple `QuotaPolicy` resources define the same model for the same `AIServiceBackend`, the
policy whose namespace/name sorts alphabetically first takes precedence.
:::

### Custom Cost Expression

By default, a request's cost is its `total_tokens`. You can override this with a
[CEL](https://github.com/google/cel-spec) expression that weights token types differently. The
following variables are available in a `costExpression`:

| Variable                      | Type   | Description                                      |
| ----------------------------- | ------ | ------------------------------------------------ |
| `input_tokens`                | uint   | Prompt / input tokens.                           |
| `output_tokens`               | uint   | Completion / output tokens.                      |
| `total_tokens`                | uint   | Total tokens (the default cost).                 |
| `cached_input_tokens`         | uint   | Input tokens served from the provider's cache.   |
| `cache_creation_input_tokens` | uint   | Input tokens charged for writing to the cache.   |
| `reasoning_tokens`            | uint   | Reasoning tokens (for reasoning-capable models). |
| `model`                       | string | The resolved model name.                         |
| `backend`                     | string | The serving backend name.                        |
| `route_name`                  | string | The route name.                                  |

```yaml
perModelQuotas:
  - modelName: gpt-4
    quota:
      # Cached input tokens count as 1/10 of a regular input token;
      # output tokens count 6x.
      costExpression: "input_tokens + cached_input_tokens / 10u + output_tokens * 6u"
      defaultBucket:
        limit: 50000
        duration: "1h"
```

:::tip
Use a custom cost expression when token types have significantly different costs with your provider —
for example, output tokens are typically more expensive than input tokens.

The token variables are **unsigned integers**, so numeric literals must carry a `u` suffix (for
example `output_tokens * 6u`) and the expression must evaluate to a non-negative integer. Integer
division truncates (`cached_input_tokens / 10u`); for an exact fractional weight, cast through
floating point — for example `uint(double(cached_input_tokens) * 0.1)`.
:::

### Bucket Mode

The `mode` field on a per-model `quota` controls how the `defaultBucket` and matching `bucketRules`
interact when a request matches one or more rules.

Currently only **`Shared`** mode is supported (it is also the default, so the field can be omitted):

- The request is charged to **all** matching `bucketRules` **and** the `defaultBucket`.
- The request is allowed only if quota is available in **at least one** matching bucket.

```yaml
perModelQuotas:
  - modelName: gpt-4
    quota:
      mode: Shared # Default — may be omitted.
      defaultBucket:
        limit: 10000
        duration: "1h"
```

:::note
An exclusive bucket mode (charging matching rules **or** the default bucket, but not both) is planned
but not yet available. The only accepted value today is `Shared`.
:::

### Client Selectors with Bucket Rules

Bucket rules let you carve out dedicated quotas for specific clients identified by request attributes
such as headers. This is useful for multi-tenant deployments where each tenant needs its own budget.
Because the mode is `Shared`, a request that matches a bucket rule is charged against **both** that
rule's bucket **and** the `defaultBucket`.

```yaml
perModelQuotas:
  - modelName: gpt-4
    quota:
      defaultBucket:
        limit: 10000 # Shared budget across all tenants.
        duration: "1h"
      bucketRules:
        # Premium tenant gets a dedicated, higher per-tenant budget.
        - clientSelectors:
            - headers:
                - name: x-tenant-id
                  type: Exact
                  value: premium-tenant
          quota:
            limit: 50000
            duration: "1h"
```

Use `type: Distinct` to create a separate bucket for every unique value of a header — for example, a
per-tenant budget keyed on the tenant ID:

```yaml
bucketRules:
  - clientSelectors:
      - headers:
          - name: x-tenant-id
            type: Distinct # One bucket per unique tenant ID.
    quota:
      limit: 5000
      duration: "1h"
```

Supported header match types are `Exact`, `Distinct`, and `RegularExpression`.

`clientSelectors` reuse the Envoy Gateway [`RateLimitSelectCondition`](https://gateway.envoyproxy.io/docs/api/extension_types/#ratelimitselectcondition) type, but QuotaPolicy currently applies only the `headers` matcher. The other fields on that type (`sourceCIDR`, `methods`, `path`, `queryParams`) are accepted by the schema but **not yet honored** for quota buckets.

### Shadow Mode

Shadow mode lets you test a bucket rule without rejecting traffic. When `shadowMode` is enabled on a
bucket rule, all quota checks are performed (cache lookups, counter updates, telemetry), but the
outcome is never enforced — the request always succeeds even if the quota is exceeded.

```yaml
bucketRules:
  - clientSelectors:
      - headers:
          - name: x-tenant-id
            type: Distinct
    quota:
      limit: 5000
      duration: "1h"
    shadowMode: true # Evaluate but do not enforce.
```

:::tip
Use shadow mode when rolling out a new quota rule. Monitor the telemetry to confirm the limit is set
correctly, then enable enforcement by removing `shadowMode` (or setting it to `false`).
:::

Shadow mode is configured per bucket rule. It cannot be set on the `defaultBucket`.

## Duration Format

The `duration` field selects the sliding-window size. It must be exactly one of the following values:

| Value   | Window     |
| ------- | ---------- |
| `"1s"`  | One second |
| `"1m"`  | One minute |
| `"1h"`  | One hour   |
| `"1d"`  | One day    |
| `"1w"`  | One week   |
| `"1mo"` | One month  |
| `"1y"`  | One year   |

The window is fixed-size — arbitrary multiples such as `"30s"` or `"15m"` are **not** valid and will
be rejected by the CRD schema. Choose the `limit` to express your budget within one of these windows.

The one-day maximum is a current `QuotaPolicy` API restriction, not an inherent limitation of token
accounting. Token-based rate limiting configured through an Envoy Gateway `BackendTrafficPolicy`
also supports `Month` and `Year` units. Use [usage-based rate limiting](./usage-based-ratelimiting.md)
when a token budget needs either of those longer windows.

## Service-Wide Quota

:::warning Not yet available
The API exposes a backend-wide `serviceQuota` field — intended to apply a single budget across all
models on a backend — but it is **not yet enforced** (it is currently a known TODO in the API).
Configuring it has no effect on traffic, so use [Per-Model Quotas](#per-model-quotas) to enforce
token quotas today. This section will document `serviceQuota` once enforcement lands.
:::

## Full Example

The following `QuotaPolicy` combines per-model quotas, custom cost expressions, bucket rules with
client selectors, and shadow mode.

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: QuotaPolicy
metadata:
  name: full-quota-policy
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: my-backend

  perModelQuotas:
    - modelName: gpt-4
      quota:
        costExpression: "input_tokens + cached_input_tokens / 10u + output_tokens * 6u"
        mode: Shared
        defaultBucket:
          limit: 10000
          duration: "1h"
        bucketRules:
          # Premium tenant gets a dedicated, higher quota.
          - clientSelectors:
              - headers:
                  - name: x-tenant-id
                    type: Exact
                    value: premium-tenant
            quota:
              limit: 50000
              duration: "1h"
          # Track per-tenant usage in shadow mode (no enforcement).
          - clientSelectors:
              - headers:
                  - name: x-tenant-id
                    type: Distinct
            quota:
              limit: 5000
              duration: "1h"
            shadowMode: true

    - modelName: gpt-3.5-turbo
      quota:
        defaultBucket:
          limit: 100000
          duration: "1h"
```

## References

- [API Reference](../../api/api.mdx)
- [Usage-Based Rate Limiting](./usage-based-ratelimiting.md)
- [Provider Fallback](./provider-fallback.md)
