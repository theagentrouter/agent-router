---
id: wally
title: Connect Wally
sidebar_position: 9
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Wally

This guide will help you configure Agent Router to work with Wally's open-weight models through its OpenAI-compatible API.

## Prerequisites

Before you begin, you'll need:

- An API key from the [RunAnywhere Console](https://console.runanywhere.ai)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info
Ready to proceed? Ensure you have followed the steps in [Connect Providers](./index.md).
:::

### 1. Download configuration template

<CodeBlock language="shell">{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/wally.yaml`}</CodeBlock>

### 2. Configure Wally Credentials

Edit the `wally.yaml` file to replace the Wally placeholder value:

- Find the section containing `WALLY_API_KEY`
- Replace it with your actual Wally API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control. The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f wally.yaml
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

The template sends requests to Wally's OpenAI-compatible endpoint at `https://inference.runanywhere.ai/v1`.

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers. See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4.1-flash",
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
    "model": "deepseek-v4.1-flash",
    "messages": [
      { "role": "user", "content": "Write a short poem about inference." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/v1/chat/completions
```

## Supported Models

The template configures `deepseek-v4.1-flash`. Other model IDs include `glm-5.3-flash` and `kimi-k3`.

`GET https://inference.runanywhere.ai/v1/models` returns the current list without an API key. See [Wally pricing](https://www.runwally.com/pricing) for current rates.

## Troubleshooting

If you encounter issues:

- Verify your API key is correct and active
- Confirm the requested model ID appears in `https://inference.runanywhere.ai/v1/models`
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
- `503`: Wally service unavailable

## Configuring More Models

To use more models, add more matching rules to the `AIGatewayRoute` in `wally.yaml`. Set each rule's `x-ai-eg-model` header value to a model ID from `https://inference.runanywhere.ai/v1/models`.

For example, add GLM and Kimi models alongside the default DeepSeek model:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-wally
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
              value: deepseek-v4.1-flash
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: glm-5.3-flash
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: kimi-k3
      backendRefs:
        - name: envoy-ai-gateway-basic-wally
```

## Next Steps

After configuring Wally, return to [Connect Providers](./index.md) to add another provider.
