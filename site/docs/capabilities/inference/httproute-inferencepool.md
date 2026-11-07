---
id: httproute-inferencepool
title: HTTPRoute + InferencePool Guide
sidebar_position: 2
---

import Setup from './\_setup.mdx';

# HTTPRoute + InferencePool Guide

This guide shows how to use InferencePool with the standard Gateway API HTTPRoute for intelligent inference routing. This approach provides basic load balancing and endpoint selection capabilities for inference workloads.

![](/img/inference-httproute.svg)

<Setup />

## Step 4: Configure Gateway and HTTPRoute

Create a Gateway and HTTPRoute that uses the InferencePool:

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: inference-pool-with-httproute
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: inference-pool-with-httproute
  namespace: default
spec:
  gatewayClassName: inference-pool-with-httproute
  listeners:
    - name: http
      protocol: HTTP
      port: 80
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: inference-pool-with-httproute
  namespace: default
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: inference-pool-with-httproute
      namespace: default
  rules:
    - backendRefs:
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: vllm-llama3-8b-instruct
          namespace: default
          weight: 1
      matches:
        - path:
            type: PathPrefix
            value: /
      timeouts:
        request: 60s
EOF
```

## Step 5: Test the Configuration

Once deployed, you can test the inference routing:

```bash
# Get the Gateway external IP
GATEWAY_IP=$(kubectl get gateway inference-pool-with-httproute -o jsonpath='{.status.addresses[0].value}')
```

```bash
# Send a test inference request
curl -X POST "http://${GATEWAY_IP}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {
        "role": "user",
        "content": "Say this is a test"
      }
    ],
    "model": "meta-llama/Llama-3.1-8B-Instruct"
  }'
```

## How It Works

### Request Processing Flow

1. **Client Request**: Client sends inference request to the Gateway
2. **Route Matching**: HTTPRoute matches the request based on path prefix
3. **InferencePool Resolution**: Envoy Gateway resolves the InferencePool backend reference
4. **Endpoint Selection**: Endpoint Picker Provider (EPP) selects the optimal endpoint
5. **Request Forwarding**: Request is forwarded to the selected inference backend
6. **Response Return**: Response is returned to the client

## Weighted Routing Across Multiple InferencePools

A single HTTPRoute rule can list more than one `InferencePool` backendRef. Envoy AI Gateway splits traffic across the referenced pools according to each backendRef's `weight`, using the same weight semantics defined by the [Gateway API `BackendRef`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#backendref) (default `1`; a `weight: 0` backendRef receives no traffic).

This is useful for canary rollouts of a new model version, splitting load across pools running different accelerators, or gradually shifting traffic from one InferencePool to another.

For example, to send roughly 70% of traffic to `primary-inference-pool` and 30% to `secondary-inference-pool`:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: inference-pool-weighted
  namespace: default
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: inference-pool-with-httproute
      namespace: default
  rules:
    - backendRefs:
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: primary-inference-pool
          namespace: default
          port: 8080
          weight: 70
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: secondary-inference-pool
          namespace: default
          port: 8080
          weight: 30
      matches:
        - path:
            type: PathPrefix
            value: /
```

Each InferencePool keeps its own Endpoint Picker Provider (EPP), so a request assigned to `secondary-inference-pool` is only ever scored and forwarded by that pool's EPP — the pools remain fully independent of one another. Weights can be changed at any time by updating the HTTPRoute; there's no need to touch the InferencePool resources themselves.

> **Note**: This weighted, multi-pool split is currently only available through HTTPRoute. [AIGatewayRoute](./aigatewayroute-inferencepool.md) rules still allow only one InferencePool backend each.

A rule can also mix InferencePools with other backend kinds, such as a Service, for example to move traffic from a Service to an InferencePool gradually. Requests sent to the Service don't go through any endpoint picker.

The following rules apply to any HTTPRoute rule that references an InferencePool:

- Every referenced InferencePool must set `endpointPickerRef`. If one doesn't, all of the rule's requests return `500`.
- If every backendRef has `weight: 0`, the rule returns `500`. A `weight: 0` backendRef receives no traffic ([`BackendRef`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#backendref)), so this follows the [`HTTPRouteRule`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#httprouterule) rule that requests with no backend to go to get a `500`.
- If a backendRef is invalid, for example because the InferencePool doesn't exist, its share of the traffic returns `500` and the other backends keep their weights, as the [`HTTPRouteRule`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#httprouterule) specifies.

InferencePools without an `endpointPickerRef` may be supported in a future release.

> **Note**: Weighted routing across InferencePools requires an Envoy Gateway version that builds one cluster per custom backendRef ([envoyproxy/gateway#10270](https://github.com/envoyproxy/gateway/pull/10270)). With an older Envoy Gateway, a rule with more than one backendRef that is an InferencePool doesn't work.

## Next Steps

- Explore [AIGatewayRoute + InferencePool](./aigatewayroute-inferencepool.md) for advanced AI-specific features
- Review [monitoring and observability](../observability/) best practices for inference workloads
- Learn more about the [Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io/) for advanced endpoint picker configurations
