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

A single HTTPRoute rule can list more than one `InferencePool` backendRef. Agent Router splits traffic across the referenced pools according to each backendRef's `weight`, using the same weight semantics defined by the [Gateway API `BackendRef`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#backendref) (default `1`; a `weight: 0` backendRef receives no traffic).

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

:::info
This weighted, multi-pool split is currently only available through HTTPRoute. [AIGatewayRoute](./aigatewayroute-inferencepool.md) rules still allow only one InferencePool backend each.
:::

A rule can also mix InferencePools with other backend kinds, such as a Service, for example to move traffic from a Service to an InferencePool gradually. Requests sent to the Service don't go through any endpoint picker.

The following rules apply to any HTTPRoute rule that references an InferencePool:

- Every referenced InferencePool must set `endpointPickerRef`. An InferencePool that doesn't is treated like an invalid backendRef: its share of the traffic fails and the other backends keep their weights. The failed share returns `500`, unless another backend of the same rule has no ready endpoints: Envoy can only return one status code for a route's missing backends, so the failed share then returns `503`.
- If every backendRef has `weight: 0`, the rule returns `500`. A `weight: 0` backendRef receives no traffic ([`BackendRef`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#backendref)), so this follows the [`HTTPRouteRule`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#httprouterule) rule that requests with no backend to go to get a `500`.
- If a backendRef is invalid, for example because the InferencePool doesn't exist, its share of the traffic returns `500` and the other backends keep their weights, as the [`HTTPRouteRule`](https://gateway-api.sigs.k8s.io/reference/api-spec/main/spec/#httprouterule) specifies. As above, the share returns `503` instead when another backend of the same rule has no ready endpoints.

InferencePools without an `endpointPickerRef` may be supported in a future release.

:::info
Weighted routing across InferencePools requires Envoy Gateway v1.11.0 or later, which includes [envoyproxy/gateway#10328](https://github.com/envoyproxy/gateway/pull/10328).
:::

:::warning
On an older Envoy Gateway, an HTTPRoute rule that references more than one InferencePool isn't split: every request it matches returns `500`. Other rules and routes on the Gateway aren't affected.
:::

## Endpoint Picker Connection Keepalive

Envoy sends requests to the Endpoint Picker Provider (EPP) over long-lived HTTP/2 connections, typically one per Envoy worker thread. By default, Agent Router doesn't enable HTTP/2 keepalive on these connections, so Envoy only notices that a connection is broken when the endpoint picker closes it.

:::warning
If the node running the endpoint picker fails abruptly, nothing closes these connections. Envoy keeps sending requests over them, and the requests wait for a response that never arrives. In a test with two endpoint picker replicas using leader election (Calico CNI, kube-proxy in iptables mode), after the leader's Pod was force-deleted, every request through the affected Envoy failed for about 7 minutes and did not recover until the stale connections were cleared manually.
:::

To enable keepalive, set the following annotation on the InferencePool:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    # Enable HTTP/2 keepalive to the endpoint picker: "false" (default) or "true"
    aigateway.envoyproxy.io/endpoint-picker-http2-keepalive: "true"
spec:
  # ... other configuration ...
```

**Available values:**

- `"false"` (default): Keepalive is disabled. Leaving the annotation unset, or setting any other value, also keeps it disabled. Values are case-sensitive.
- `"true"`: Envoy sends an HTTP/2 PING on each connection every 15 seconds, plus a random jitter of up to 15%, and closes the connection if nothing arrives on it within 5 seconds.

With keepalive enabled, Envoy detects a dead connection within about 22 seconds at most; in the test above, it took 5 to 6 seconds. Requests then fail with `500` right away instead of waiting, and Envoy opens new connections through the endpoint picker Service. In the same test, traffic recovered about 18 seconds after the leader was deleted, which was the time the new endpoint picker took to become the leader.

### Endpoint Picker Requirements

The endpoint picker must accept a PING every 15 seconds. A gRPC server closes the connection with a `GOAWAY` when a client sends PINGs more often than its keepalive enforcement policy allows, and the grpc-go default allows far fewer PINGs than this. For llm-d-router, the policy that accepts these PINGs was added in [llm-d-router#3711](https://github.com/llm-d/llm-d-router/pull/3711); llm-d-router v1.0.0 doesn't include it.

Keepalive still works with an endpoint picker that doesn't accept these PINGs, but its idle connections are closed periodically. In tests against llm-d-router v1.0.0:

- While requests kept flowing, the endpoint picker closed no connections: about 33 minutes of continuous traffic and 1,983 requests.
- While no requests were flowing, the endpoint picker closed the connections with a `GOAWAY` within 30 to 60 seconds. Envoy opened new connections for the next requests, and all of them succeeded.
- A request sent at the moment the endpoint picker closes a connection can fail with `500`.

## Endpoint Picker Connection Keepalive

Envoy sends requests to the Endpoint Picker Provider (EPP) over long-lived HTTP/2 connections, typically one per Envoy worker thread. By default, Agent Router doesn't enable HTTP/2 keepalive on these connections, so Envoy only notices that a connection is broken when the endpoint picker closes it.

:::warning
If the node running the endpoint picker fails abruptly, nothing closes these connections. Envoy keeps sending requests over them, and the requests wait for a response that never arrives. In a test with two endpoint picker replicas using leader election (Calico CNI, kube-proxy in iptables mode), after the leader's Pod was force-deleted, every request through the affected Envoy failed for about 7 minutes and did not recover until the stale connections were cleared manually.
:::

To enable keepalive, set the following annotation on the InferencePool:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    # Enable HTTP/2 keepalive to the endpoint picker: "false" (default) or "true"
    aigateway.envoyproxy.io/endpoint-picker-http2-keepalive: "true"
spec:
  # ... other configuration ...
```

**Available values:**

- `"false"` (default): Keepalive is disabled. Leaving the annotation unset, or setting any other value, also keeps it disabled. Values are case-sensitive.
- `"true"`: Envoy sends an HTTP/2 PING on each connection every 15 seconds, plus a random jitter of up to 15%, and closes the connection if nothing arrives on it within 5 seconds.

With keepalive enabled, Envoy detects a dead connection within about 22 seconds at most; in the test above, it took 5 to 6 seconds. Requests then fail with `500` right away instead of waiting, and Envoy opens new connections through the endpoint picker Service. In the same test, traffic recovered about 18 seconds after the leader was deleted, which was the time the new endpoint picker took to become the leader.

### Endpoint Picker Requirements

The endpoint picker must accept a PING every 15 seconds. A gRPC server closes the connection with a `GOAWAY` when a client sends PINGs more often than its keepalive enforcement policy allows, and the grpc-go default allows far fewer PINGs than this. For llm-d-router, the policy that accepts these PINGs was added in [llm-d-router#3711](https://github.com/llm-d/llm-d-router/pull/3711); llm-d-router v1.0.0 doesn't include it.

Keepalive still works with an endpoint picker that doesn't accept these PINGs, but its idle connections are closed periodically. In tests against llm-d-router v1.0.0:

- While requests kept flowing, the endpoint picker closed no connections: about 33 minutes of continuous traffic and 1,983 requests.
- While no requests were flowing, the endpoint picker closed the connections with a `GOAWAY` within 30 to 60 seconds. Envoy opened new connections for the next requests, and all of them succeeded.
- A request sent at the moment the endpoint picker closes a connection can fail with `500`.

## Next Steps

- Explore [AIGatewayRoute + InferencePool](./aigatewayroute-inferencepool.md) for advanced AI-specific features
- Review [monitoring and observability](../observability/) best practices for inference workloads
- Learn more about the [Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io/) for advanced endpoint picker configurations
