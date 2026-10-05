---
id: quota-policy
title: Quota Policy
sidebar_position: 6
---

# Quota Policy

`QuotaPolicy` enables token-based quota management for AI inference services in Agent Router.
When all related backend's quota are exceeded, requests are rejected with a `429 Too Many Requests` status code.

:::note QuotaPolicy vs. Rate Limiting
QuotaPolicy manages **total consumption budgets** (for example, 100,000 tokens per hour). This is
distinct from [usage-based rate limiting](./usage-based-ratelimiting.md), which controls **request
velocity** (for example, requests per second). Use QuotaPolicy when you need to cap cumulative token
spend across a time window.
:::

## Overview

Key features of QuotaPolicy:

- **Per-model token quotas** — assign token budgets to individual models served by an `AIServiceBackend`.
- **CEL cost expressions** — weight input, output, cached, and reasoning tokens differently when
  computing how much a request burns down a quota.
- **Client-selector bucket rules** — carve out per-tenant or per-header quotas using request attributes.
- **Shadow mode** — evaluate quota rules without enforcing them, for safe rollout.

## How It Works

1. A `QuotaPolicy` is attached to one or more `AIServiceBackend` resources via `targetRefs`.
2. For each completed request, the token cost is computed using the configured cost expression
   (defaults to `total_tokens`).
3. The cost is charged against the matching quota bucket (the per-model default bucket, or a matching
   bucket rule).
4. When all related quota buckets for that model are exceeded, subsequent matching requests receive `429 Too Many Requests`.

:::tip Prerequisites
Quota enforcement uses the same infrastructure as usage-based rate limiting:

1. **Redis Deployment**: A Redis instance for storing quota counters. See the [redis.yaml example](https://github.com/theagentrouter/agent-router/blob/main/examples/token_ratelimit/redis.yaml) for a simple deployment.
2. **Envoy Gateway Configuration**: Envoy Gateway must be configured at installation time to enable rate limiting and point to your Redis instance. See the [Envoy Gateway Installation Guide](../../getting-started/prerequisites.md#additional-features-rate-limiting-inferencepool-etc).

See [Usage-based Rate Limiting](./usage-based-ratelimiting.md) for more detail on the rate limit infrastructure that QuotaPolicy builds on.
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

| Value  | Window     |
| ------ | ---------- |
| `"1s"` | One second |
| `"1m"` | One minute |
| `"1h"` | One hour   |
| `"1d"` | One day    |

The window is fixed-size — arbitrary multiples such as `"30s"` or `"15m"` are **not** valid and will
be rejected by the CRD schema. Choose the `limit` to express your budget within one of these windows.

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
