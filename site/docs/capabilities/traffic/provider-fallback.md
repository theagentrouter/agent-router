---
id: provider-fallback
title: Provider Fallback
sidebar_position: 6
---

# Provider Fallback

Agent Router supports provider fallback to ensure high availability and reliability for AI/LLM workloads. With fallback, you can configure multiple upstream providers for a single route, so that if the primary provider fails (due to network errors, 5xx responses, or other health check failures), traffic is automatically routed to a healthy fallback provider.

## When to Use Fallback

- To ensure uninterrupted service when a primary AI/LLM provider is unavailable.
- To provide redundancy across multiple cloud or on-premise model providers.
- To implement active-active or active-passive failover strategies for critical AI workloads.

## How Fallback Works

- **Primary and Fallback Backends:** You can specify a prioritized list of backends in your `AIGatewayRoute` using `backendRefs`. The first backend is treated as primary, and subsequent backends are considered fallbacks.
- **Retry Policy:** Fallback is triggered based on retry policies, which can be configured using the [`BackendTrafficPolicy`](https://gateway.envoyproxy.io/contributions/design/backend-traffic-policy/) API.
- **Automatic Failover:** When the primary backend becomes unhealthy, Agent Router automatically shifts traffic to the next healthy fallback backend.

## Example

Below is an example configuration that demonstrates provider fallback from a failing upstream to AWS Bedrock:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: provider-fallback
  namespace: default
spec:
  parentRefs:
    - name: provider-fallback
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: us.meta.llama3-2-1b-instruct-v1:0
      backendRefs:
        - name: provider-fallback-always-failing-upstream # Primary backend (expected to fail)
          priority: 0
        - name: provider-fallback-aws # Fallback backend
          priority: 1
```

The corresponding `Backend` resources:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: provider-fallback-always-failing-upstream
  namespace: default
spec:
  endpoints:
    - fqdn:
        hostname: provider-fallback-always-failing-upstream.default.svc.cluster.local
        port: 443
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: provider-fallback-aws
  namespace: default
spec:
  endpoints:
    - fqdn:
        hostname: bedrock-runtime.us-east-1.amazonaws.com
        port: 443
```

## Configuring Fallback Behavior

Attach a `BackendTrafficPolicy` to the generated `HTTPRoute` to control retry behavior:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: provider-fallback
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: provider-fallback # HTTPRoute is created with the same name as AIGatewayRoute
  retry:
    # This ensures that only one attempt is made per priority.
    # For example, if the primary backend fails, it will not retry on the same backend.
    numAttemptsPerPriority: 1
    numRetries: 5
    perRetry:
      backOff:
        baseInterval: 100ms
        maxInterval: 10s
      timeout: 30s
    retryOn:
      httpStatusCodes:
        - 500
      triggers:
        - connect-failure
        - retriable-status-codes
```

## Troubleshooting

### The fallback backend is never reached

Priority failover happens through retries, so a retry policy that does not fire leaves every request on the primary backend. Check, in order:

1. **`retryOn.triggers` includes `retriable-status-codes`.** This is the most common cause. `retryOn.httpStatusCodes` only names _which_ status codes are retriable — it does not by itself make status codes a retry condition. Without the `retriable-status-codes` trigger, the list is inert and a `500` from the primary is returned to the client as-is:

   ```yaml
   retryOn:
     httpStatusCodes:
       - 500
     triggers:
       - connect-failure
       - retriable-status-codes # Required for httpStatusCodes to take effect.
   ```

2. **`numRetries` is large enough to leave the primary's priority.** With `numAttemptsPerPriority: 1`, reaching the backend at priority 1 costs one retry, so `numRetries` must be at least 1 — budget one attempt per priority you want to traverse.

3. **`targetRefs` points at the generated `HTTPRoute`.** The `BackendTrafficPolicy` attaches to the `HTTPRoute` that the `AIGatewayRoute` generates, which carries the same name as the `AIGatewayRoute`. A `targetRefs` entry naming something else — an `AIGatewayRoute` kind, or a `Backend` — configures no retry policy on this route.

4. **Priorities start at `0` and are contiguous.** A rule whose lowest priority is not `0`, or that skips a level, is not a valid priority set.

5. **The failure is one the retry policy can see.** A response the primary produced successfully — including a `4xx` such as `401` from a bad API key — is not a connection or `5xx` failure, so it is not retried and does not fall back. Simulate the failure with a `5xx` or an unreachable endpoint instead.

## References

- [Provider Fallback Example](https://github.com/theagentrouter/agent-router/tree/main/examples/provider_fallback)
- [`BackendTrafficPolicy` API Design](https://gateway.envoyproxy.io/contributions/design/backend-traffic-policy/)
