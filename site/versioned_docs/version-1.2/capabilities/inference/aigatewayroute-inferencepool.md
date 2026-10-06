---
id: aigatewayroute-inferencepool
title: AIGatewayRoute + InferencePool Guide
sidebar_position: 3
---

import Setup from './\_setup.mdx';

# AIGatewayRoute + InferencePool Guide

This guide demonstrates how to use InferencePool with AIGatewayRoute for advanced AI-specific inference routing. This approach provides enhanced features like model-based routing, token rate limiting, and advanced observability.

<Setup />

## Step 4: Configure Gateway and AIGatewayRoute

Create a Gateway and AIGatewayRoute with multiple InferencePool backends:

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: inference-pool-with-aigwroute
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: inference-pool-with-aigwroute
  namespace: default
spec:
  gatewayClassName: inference-pool-with-aigwroute
  listeners:
    - name: http
      protocol: HTTP
      port: 80
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: inference-pool-with-aigwroute
  namespace: default
spec:
  parentRefs:
    - name: inference-pool-with-aigwroute
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    # Route for vLLM Llama model via InferencePool
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: meta-llama/Llama-3.1-8B-Instruct
      backendRefs:
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: vllm-llama3-8b-instruct
    # Route for Mistral model via InferencePool
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: mistral:latest
      backendRefs:
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: mistral
    # Route for traditional backend (non-InferencePool)
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: some-cool-self-hosted-model
      backendRefs:
        - name: envoy-ai-gateway-basic-testupstream
EOF
```

## Step 5: Test the Configuration

Test different model routing scenarios:

```bash
# Get the Gateway external IP
GATEWAY_IP=$(kubectl get gateway inference-pool-with-aigwroute -o jsonpath='{.status.addresses[0].value}')
```

Test vLLM Llama model (routed via InferencePool):

```bash
curl -H "Content-Type: application/json" \
  -d '{
        "model": "meta-llama/Llama-3.1-8B-Instruct",
        "messages": [
            {
                "role": "user",
                "content": "Hi. Say this is a test"
            }
        ]
    }' \
  http://$GATEWAY_IP/v1/chat/completions
```

Test Mistral model (routed via InferencePool):

```bash
curl -H "Content-Type: application/json" \
  -d '{
        "model": "mistral:latest",
        "messages": [
            {
                "role": "user",
                "content": "Hi. Say this is a test"
            }
        ]
    }' \
  http://$GATEWAY_IP/v1/chat/completions
```

Test AIService backend (non-InferencePool):

```bash
curl -H "Content-Type: application/json" \
  -d '{
        "model": "some-cool-self-hosted-model",
        "messages": [
            {
                "role": "user",
                "content": "Hi. Say this is a test"
            }
        ]
    }' \
  http://$GATEWAY_IP/v1/chat/completions
```

## Advanced Features

### Model-Based Routing

AIGatewayRoute automatically extracts the model name from the request body and routes to the appropriate backend:

- **Automatic Extraction**: No need to manually set headers
- **Dynamic Routing**: Different models can use different InferencePools
- **Mixed Backends**: Combine InferencePool and AIServiceBackend in the same route based on model name by request Body.

### Token Rate Limiting

Configure token-based rate limiting for InferencePool backends:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: inference-pool-with-rate-limiting
spec:
  # ... other configuration ...
  llmRequestCosts:
    - metadataKey: llm_input_token
      type: InputToken
    - metadataKey: llm_output_token
      type: OutputToken
    - metadataKey: llm_total_token
      type: TotalToken
```

### Enhanced Observability

AIGatewayRoute provides rich metrics for InferencePool usage:

- **Model-specific metrics**: Track usage per model
- **Token consumption**: Monitor token usage and costs
- **Endpoint performance**: Detailed metrics per inference endpoint

## InferencePool Configuration Annotations

InferencePool supports configuration annotations to customize the external processor behavior:

### Processing Body Mode

Configure how the external processor handles request and response bodies:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    # Configure processing body mode: "duplex" (default) or "buffered"
    aigateway.envoyproxy.io/processing-body-mode: "buffered"
spec:
  # ... other configuration ...
```

**Available values:**

- `"duplex"` (default): Uses `FULL_DUPLEX_STREAMED` mode for streaming processing
- `"buffered"`: Uses `BUFFERED` mode for buffered processing

### Allow Mode Override

Configure whether the external processor can override the processing mode:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    # Configure allow mode override: "false" (default) or "true"
    aigateway.envoyproxy.io/allow-mode-override: "true"
spec:
  # ... other configuration ...
```

**Available values:**

- `"false"` (default): External processor cannot override the processing mode
- `"true"`: External processor can override the processing mode

### Combined Configuration

You can use both annotations together:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    aigateway.envoyproxy.io/processing-body-mode: "buffered"
    aigateway.envoyproxy.io/allow-mode-override: "true"
spec:
  # ... other configuration ...
```

## Key Advantages over HTTPRoute

### Advanced OpenAI Routing

- Built-in OpenAI API schema validation
- Seamless integration with OpenAI SDKs
- Route multiple models in a single listener
- Mix InferencePool and traditional backends
- Automatic model extraction from request body

### AI-Specific Features

- Token-based rate limiting
- Model performance metrics
- Cost tracking and management
- Request/response transformation

## Next Steps

- Explore [token rate limiting](../traffic/usage-based-ratelimiting.md) in detail
- Review [observability best practices](../observability/) for AI workloads
- Configure [backend security policies](../security/upstream-auth.mdx) for your inference endpoints
- Learn more about the [Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io/) for advanced endpoint picker configurations
