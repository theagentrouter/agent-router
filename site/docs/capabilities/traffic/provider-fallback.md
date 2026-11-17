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

## References

- [Provider Fallback Example](https://github.com/theagentrouter/agent-router/tree/main/examples/provider_fallback)
- [`BackendTrafficPolicy` API Design](https://gateway.envoyproxy.io/contributions/design/backend-traffic-policy/)
