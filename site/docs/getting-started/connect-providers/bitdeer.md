---
id: bitdeer
title: Connect Bitdeer AI
sidebar_position: 7
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../_vars.json';

# Connect Bitdeer AI

This guide will help you configure Agent Router to work with Bitdeer AI's serverless models through its OpenAI-compatible API.

## Prerequisites

Before you begin, you'll need:

- An API key from the [Bitdeer AI Console](https://www.bitdeer.ai/model/apikeys)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info
Ready to proceed? Ensure you have followed the steps in [Connect Providers](./index.md).
:::

### 1. Download configuration template

<CodeBlock language="shell">{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/bitdeer.yaml`}</CodeBlock>

### 2. Configure Bitdeer AI Credentials

Edit the `bitdeer.yaml` file to replace the Bitdeer placeholder value:

- Find the section containing `BITDEER_API_KEY`
- Replace it with your actual Bitdeer AI API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control. The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f bitdeer.yaml
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

The template sends requests to Bitdeer AI's OpenAI-compatible endpoint at `https://api-inference.bitdeer.ai/v1`.

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers. See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/DeepSeek-V4-Pro",
    "messages": [
      { "role": "user", "content": "Explain serverless inference in one sentence." }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Streaming

```shell
curl -N -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/DeepSeek-V4-Pro",
    "messages": [
      { "role": "user", "content": "Write a short poem about inference." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/v1/chat/completions
```

## Supported Models

The template configures `deepseek-ai/DeepSeek-V4-Pro`. Other model IDs include `Qwen/Qwen3.5-397B-A17B` and `moonshotai/Kimi-K2.6`.

See the [Bitdeer AI Model Catalog](https://developers.bitdeer.ai/docs/api/models-catalog) for the current list and each model's supported capabilities.

## Troubleshooting

If you encounter issues:

- Verify your API key is correct and active
- Confirm the requested model ID appears in the Bitdeer AI Model Catalog
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

- `401`: Invalid API key
- `429`: Rate limit exceeded
- `503`: Bitdeer AI service unavailable

## Configuring More Models

To use more models, add more matching rules to the `AIGatewayRoute` in `bitdeer.yaml`. Set each rule's `x-ai-eg-model` header value to a model ID from the [Bitdeer AI Model Catalog](https://developers.bitdeer.ai/docs/api/models-catalog).

For example, add Qwen and Kimi models alongside the default DeepSeek model:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-bitdeer
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
              value: deepseek-ai/DeepSeek-V4-Pro
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: Qwen/Qwen3.5-397B-A17B
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: moonshotai/Kimi-K2.6
      backendRefs:
        - name: envoy-ai-gateway-basic-bitdeer
```

## Next Steps

After configuring Bitdeer AI, return to [Connect Providers](./index.md) to add another provider.
