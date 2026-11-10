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

## Next Steps

- Explore [AIGatewayRoute + InferencePool](./aigatewayroute-inferencepool.md) for advanced AI-specific features
- Review [monitoring and observability](../observability/) best practices for inference workloads
- Learn more about the [Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io/) for advanced endpoint picker configurations
