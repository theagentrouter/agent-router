---
id: usage-based-ratelimiting
title: Usage-based Rate Limiting
sidebar_position: 5
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

This guide focuses on AI Gateway's specific capabilities for token-based rate limiting in LLM requests. For general rate limiting concepts and configurations, refer to [Envoy Gateway's Rate Limiting documentation](https://gateway.envoyproxy.io/docs/tasks/traffic/global-rate-limit/).

:::info Quota Policy vs. Rate Limiting
AI Gateway also provides [Quota Policy](./quota-policy.md) for managing **total consumption budgets** (for example, 100,000 tokens per hour). Use QuotaPolicy when you need to cap cumulative token spend, and usage-based rate limiting (this page) when you need to control **request velocity**.
:::

## Overview

AI Gateway leverages Envoy Gateway's Global Rate Limit API to provide token-based rate limiting for LLM requests. Key features include:

- Token usage tracking based on model and user identifiers
- Configuration for tracking input, output, and total token metadata from LLM responses
- Model-specific rate limiting using AI Gateway headers (`x-ai-eg-model`) which is inserted by the AI Gateway filter with the model name extracted from the request body.
- Support for custom token cost calculations using CEL expressions

## Token Usage Behavior

AI Gateway has specific behavior for token tracking and rate limiting:

1. **Token Extraction**: AI Gateway automatically extracts token usage from LLM responses that follow the OpenAI schema format. The token counts are stored in the metadata specified in your `llmRequestCosts` configuration.

2. **Rate Limit Timing**: Token usage is charged after the response completes. When a request is received:
   - Envoy checks the token budget using usage that has already been charged
   - If the existing charged usage is over the limit, the request is rejected with a 429 status code
   - If the request is admitted, AI Gateway extracts token usage from the completed response and charges it towards the total

3. **Token Types**:
   - `InputToken`: Counts tokens in the request prompt
   - `CachedInputToken`: Counts _cached_ input tokens in the request prompt
   - `OutputToken`: Counts tokens in the model's response
   - `TotalToken`: Combines both input and output tokens
   - `CEL`: Allows custom token calculations using CEL expressions

4. **Multiple Rate Limits**: You can configure multiple rate limit rules for the same user-model combination. For example:
   - Limit total tokens per hour
   - Separate limits for input and output tokens
   - Custom limits using CEL expressions

5. **Per Route Custom Token Calculation**: The `llmRequestCosts` defined in your `AIGatewayRoute` spec are scoped exclusively to that specific route. This means multiple `AIGatewayRoute` resources can define the exact same metadata keys, but the gateway will independently apply the correct calculations based on the route's name and namespace.

To map the request to the correct calculation, the AI Gateway parses the route from the Envoy Gateway Metadata. If that metadata is not present, it falls back to parsing the route name from the route configuration, assuming Envoy Gateway has generated the name in the following format: `httproute/<namespace>/<name>/rule/<index>`.

:::note
For model providers with OpenAI schema transformations (like AWS Bedrock), AI Gateway automatically captures token usage through its request/response transformer. This enables consistent token tracking and rate limiting across different AI services using a unified OpenAI-compatible format.
:::

### Streaming responses

For streaming responses, token usage is only known after the stream completes and the final usage
data has been observed. AI Gateway does not interrupt an already admitted stream if that stream
pushes the token count over the configured limit. Instead, the stream completes, its token usage is
charged at stream end, and later matching requests are rejected with `429 Too Many Requests` until
enough usage expires from the rate-limit window.

For example, with a 1,000-token hourly limit, a request that is admitted while the bucket is empty
can stream 1,200 tokens successfully. The bucket is then 200 tokens over the limit, and subsequent
matching requests are limited until the window has enough capacity again.

## Configuration

:::tip Prerequisites

Rate limiting requires two components to be configured:

1. **Redis Deployment**: A Redis instance must be running to store rate limit data. See the [redis.yaml example](https://github.com/theagentrouter/agent-router/blob/main/examples/token_ratelimit/redis.yaml) for a simple deployment.

2. **Envoy Gateway Configuration**: Envoy Gateway must be configured at installation time to enable rate limiting and point to your Redis instance. See [Envoy Gateway Installation Guide](../../getting-started/prerequisites.md#additional-features-rate-limiting-inferencepool-etc)

:::

### 1. Configure Token Tracking

AI Gateway automatically tracks token usage for each request. Configure which token counts you want to track in your `AIGatewayRoute`:

```yaml
spec:
  llmRequestCosts:
    - metadataKey: llm_input_token
      type: InputToken # Counts tokens in the request
    - metadataKey: llm_cached_input_token
      type: CachedInputToken # Counts cached input tokens in the request prompt
    - metadataKey: llm_output_token
      type: OutputToken # Counts tokens in the response
    - metadataKey: llm_total_token
      type: TotalToken # Tracks combined usage
```

For advanced token calculations specific to your use case:

```yaml
spec:
  llmRequestCosts:
    - metadataKey: custom_cost
      type: CEL
      cel: "(input_tokens - cached_input_tokens) + (cached_input_tokens * 0.1) + output_tokens * 1.5" # Example: Weight cached tokens less and weight output tokens more heavily
```

LLMRequestCosts can be defined on a per-route level.

### 2. Configure Rate Limits

AI Gateway uses Envoy Gateway's Global Rate Limit API to configure rate limits. Rate limits should be defined using a combination of user and model identifiers to properly control costs at the model level. Configure this using a `BackendTrafficPolicy`:

#### Example: Cost-Based Model Rate Limiting

The following example demonstrates a common use case where different models have different token limits based on their costs. This is useful when:

- You want to limit expensive models (like GPT-4) more strictly than cheaper ones
- You need to implement different quotas for different tiers of service
- You want to prevent cost overruns while still allowing flexibility with cheaper models

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: model-specific-token-limit-policy
  namespace: default
spec:
  targetRefs:
    - name: envoy-ai-gateway-token-ratelimit
      kind: Gateway
      group: gateway.networking.k8s.io
  rateLimit:
    type: Global
    global:
      rules:
        # Rate limit rule for GPT-4: 1000 total tokens per hour per user
        # Stricter limit due to higher cost per token
        - clientSelectors:
            - headers:
                - name: x-tenant-id
                  type: Distinct
                - name: x-ai-eg-model
                  type: Exact
                  value: gpt-4
          limit:
            requests: 1000 # 1000 total tokens per hour
            unit: Hour
          cost:
            request:
              from: Number
              number: 0 # Set to 0 so only token usage counts
            response:
              from: Metadata
              metadata:
                namespace: io.envoy.ai_gateway
                key: llm_total_token # Uses total tokens from the responses
        # Rate limit rule for GPT-3.5: 5000 total tokens per hour per user
        # Higher limit since the model is more cost-effective
        - clientSelectors:
            - headers:
                - name: x-tenant-id
                  type: Distinct
                - name: x-ai-eg-model
                  type: Exact
                  value: gpt-3.5-turbo
          limit:
            requests: 5000 # 5000 total tokens per hour (higher limit for less expensive model)
            unit: Hour
          cost:
            request:
              from: Number
              number: 0 # Set to 0 so only token usage counts
            response:
              from: Metadata
              metadata:
                namespace: io.envoy.ai_gateway
                key: llm_total_token # Uses total tokens from the response
```

:::warning
When configuring rate limits:

1. Always set the request cost number to 0 to ensure only token usage counts towards the limit
2. Set appropriate limits for different models based on their costs and capabilities
3. Ensure both user and model identifiers are used in rate limiting rules
   :::

## Per-Tenant Limits Without Per-Tenant Policies

The rate limits above use a static limit value defined in the `BackendTrafficPolicy` (for example `requests: 1000`). If each tenant needs a different number, that forces a policy per tenant — and, because the policy attaches to routes, often a set of routes per tenant too. At a few thousand tenants that is the dominant cost of the configuration.

Envoy can instead read the limit value from **dynamic metadata** on each request, so one shared policy serves every tenant and the tenant's own number arrives with the request. Envoy Gateway exposes this as `limit.fromMetadata` on the rate limit rule.

Nothing about this requires AI Gateway. Any Envoy filter that writes dynamic metadata can supply the number, and the policy points directly at whatever namespace that filter writes to. AI Gateway's part is the other half — turning the LLM's token usage into the **cost** charged against the budget, which is covered above and is the piece nothing else can do.

:::note Requires Envoy Gateway with `limit.fromMetadata`
`limit.fromMetadata` was added in [envoyproxy/gateway#9216](https://github.com/envoyproxy/gateway/pull/9216). Older releases silently drop the field, which leaves the static `requests` in force. Check your Envoy Gateway version before relying on it.
:::

### 1. Emit the limit from a preceding filter

The limit must be a struct with `requests_per_unit` and `unit`, which is what Envoy's rate limit override reads. `unit` is one of `SECOND`, `MINUTE`, `HOUR`, `DAY`, `MONTH`, `YEAR`.

With `ext_authz`, the auth server already resolves the tenant, so it can return the budget in the same Check response. Envoy files everything a Check response returns under the `envoy.filters.http.ext_authz` namespace:

```json
{
  "dynamic_metadata": {
    "fields": {
      "total_limit": {
        "struct_value": {
          "fields": {
            "requests_per_unit": { "number_value": 100000 },
            "unit": { "string_value": "HOUR" }
          }
        }
      }
    }
  }
}
```

Dynamic metadata is deliberate here rather than a request header: it can only be written by filters in the chain, never by the downstream client, so a tenant cannot raise its own limit. No header stripping is needed to make it safe.

### 2. Read it in a `BackendTrafficPolicy`

Point `limit.fromMetadata` at the namespace and key the filter wrote. The static `requests`/`unit` stay as the default that applies when the metadata is absent:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: tenant-token-budget
  namespace: default
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: envoy-ai-gateway
  rateLimit:
    type: Global
    global:
      rules:
        - clientSelectors:
            - headers:
                - name: x-tenant-id
                  type: Distinct
          limit:
            requests: 1000 # default when ext_authz says nothing
            unit: Hour
            fromMetadata:
              namespace: envoy.filters.http.ext_authz # where ext_authz writes
              key: total_limit
          cost:
            request: { from: Number, number: 0 } # charge tokens, not requests
            response:
              from: Metadata
              metadata:
                namespace: io.envoy.ai_gateway
                key: llm_total_token # emitted by globalLLMRequestCosts
```

`Distinct` on `x-tenant-id` gives each tenant its own bucket, and `fromMetadata` gives each bucket its own size. One policy, arbitrary per-tenant numbers, no per-tenant objects.

### 3. Separate input, output and total budgets

A tenant plan is usually stated as a token triplet rather than one number. Declare one cost key per token kind on the `GatewayConfig`:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: envoy-ai-gateway
  namespace: default
spec:
  globalLLMRequestCosts:
    - metadataKey: llm_input_token
      type: InputToken
    - metadataKey: llm_output_token
      type: OutputToken
    - metadataKey: llm_total_token
      type: TotalToken
```

Have the auth server return `input_limit`, `output_limit` and `total_limit`, then add one rule per kind, each pairing its limit key with the matching cost key. A tenant stops at whichever budget it exhausts first.

### Behavior to plan for

| Metadata value                               | Effect                                           |
| -------------------------------------------- | ------------------------------------------------ |
| a valid `{ requests_per_unit, unit }` struct | becomes the limit for that request               |
| absent                                       | the static `requests`/`unit` on the rule applies |
| `requests_per_unit: 0`                       | a real limit of `0` — see below                  |

:::warning Missing metadata fails open to the static default
When the metadata is absent — the auth server didn't set it, or was bypassed — the request falls back to the static `requests`/`unit`. Because that direction is permissive, keep the static value a **conservative ceiling** rather than a placeholder.
:::

:::caution A `requests_per_unit` of `0` suspends the tenant
`0` is a real limit, and it is the deliberate way to suspend a tenant. Exactly when it starts denying depends on how you charge cost:

- **Token cost** (`cost.request.from: Number, number: 0`, charged from the response) — the budget is checked before anything is charged, so the request already in flight is admitted and everything after it is denied.
- **Request count** (no `cost` block, one unit per request) — denied immediately, including the first request.

So a source meaning _"I have no opinion about this request"_ must **omit** the field entirely. Emitting `0` from an uninitialized field, a failed lookup, or a tenant record with no quota set suspends that tenant instead of falling back.
:::

:::note When the source can't produce the struct
Some sources emit their own shape and can't be changed — `jwt_authn` writes claims verbatim, and a third-party auth sidecar may only return a string like `"100000/HOUR"`. Envoy's override reads only the `{ requests_per_unit, unit }` struct, so in that case you need a filter in between to transcode it, such as a small Lua filter writing the struct into its own metadata namespace and pointing `fromMetadata` there.
:::

## Making Requests

For proper cost control and rate limiting, requests must include:

- `x-tenant-id`: Identifies the user making the request

Example request:

```shell
curl -H "Content-Type: application/json" \
  -H "x-tenant-id: user123" \
  -d '{
        "model": "gpt-4",
        "messages": [
            {
                "role": "user",
                "content": "Hello!"
            }
        ]
    }' \
  $GATEWAY_URL/v1/chat/completions
```
