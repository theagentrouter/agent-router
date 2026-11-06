---
id: empiriolabs
title: Connect EmpirioLabs AI
sidebar_position: 9
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect EmpirioLabs AI

This guide will help you configure Agent Router to work with EmpirioLabs AI models through its OpenAI-compatible API.

## Prerequisites

Before you begin, you'll need:

- An EmpirioLabs AI API key. Sign in to the [EmpirioLabs AI dashboard](https://platform.empiriolabs.ai/dashboard/api-keys) and create a key on the API Keys page. Keys start with `sk-empiriolabs-`.
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info
Ready to proceed? Ensure you have followed the steps in [Connect Providers](./index.md).
:::

### 1. Download configuration template

<CodeBlock language="shell">{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/empiriolabs.yaml`}</CodeBlock>

### 2. Configure EmpirioLabs AI Credentials

Edit the `empiriolabs.yaml` file to replace the EmpirioLabs AI placeholder value:

- Find the section containing `EMPIRIOLABS_API_KEY`
- Replace it with your actual EmpirioLabs AI API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control. The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f empiriolabs.yaml
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

The template sends requests to the EmpirioLabs AI OpenAI-compatible endpoint at `https://api.empiriolabs.ai/v1`.

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers. See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "qwen3-8-flash",
    "messages": [
      { "role": "user", "content": "Explain what an AI gateway does in one sentence." }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Streaming

```shell
curl -N -H "Content-Type: application/json" \
  -d '{
    "model": "mistral-small-4",
    "messages": [
      { "role": "user", "content": "Write a short poem about gateways." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Embeddings

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-v4",
    "input": "Agent Router sends each request to the right AI provider."
  }' \
  $GATEWAY_URL/v1/embeddings
```

## Supported Models

The template routes `qwen3-8-flash` and `mistral-small-4` for chat completions and `text-embedding-v4` for embeddings. Models that reason return their reasoning in a `reasoning_content` field, and reasoning tokens count toward `max_tokens`, so leave room for the answer when you set a limit.

See the [EmpirioLabs AI models page](https://empiriolabs.ai/models) for the current models and prices, and the [EmpirioLabs AI documentation](https://docs.empiriolabs.ai/compatibility) for the OpenAI-compatible API.

## Troubleshooting

If you encounter issues:

- Verify your API key is correct and active
- Confirm the requested model ID appears on the [EmpirioLabs AI models page](https://empiriolabs.ai/models)
- Check pod status:

  ```shell
  kubectl get pods -n envoy-gateway-system
  ```

- View controller logs:

  ```shell
  kubectl logs -n envoy-ai-gateway-system deployment/ai-gateway-controller
  ```

- View External Processor logs:

  ```shell
  kubectl logs -n envoy-gateway-system \
    -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
    -c ai-gateway-extproc
  ```

Common errors:

- `401`: Invalid or missing API key
- `402`: The account balance cannot cover the request; add credits on the Billing page of the dashboard
- `404`: The model ID is not in the `AIGatewayRoute` rules, or is not an EmpirioLabs AI model ID
- `429`: Rate limit exceeded; retry with exponential backoff

## Configuring More Models

To use more models, add more matching rules to the `AIGatewayRoute` in `empiriolabs.yaml`. Set each rule's `x-ai-eg-model` header value to a model ID from the [EmpirioLabs AI models page](https://empiriolabs.ai/models).

For example, add `qwen3-7-max` alongside the default models:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-empiriolabs
  namespace: default
spec:
  parentRefs:
    - name: envoy-ai-gateway-basic
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: qwen3-8-flash
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: mistral-small-4
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: text-embedding-v4
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: qwen3-7-max
      backendRefs:
        - name: envoy-ai-gateway-basic-empiriolabs
```

## Next Steps

After configuring EmpirioLabs AI, return to [Connect Providers](./index.md) to add another provider.
