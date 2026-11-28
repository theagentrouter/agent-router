---
id: runcrate
title: Connect Runcrate
sidebar_position: 9
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Runcrate

This guide will help you configure Agent Router to work with Runcrate's open models through its OpenAI-compatible API.

## Prerequisites

Before you begin, you'll need:

- A Runcrate API key. See the [Runcrate documentation](https://www.runcrate.ai/docs) for how to create one
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info
Ready to proceed? Ensure you have followed the steps in [Connect Providers](./index.md).
:::

### 1. Download configuration template

<CodeBlock language="shell">{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/runcrate.yaml`}</CodeBlock>

### 2. Configure Runcrate Credentials

Edit the `runcrate.yaml` file to replace the Runcrate placeholder value:

- Find the section containing `RUNCRATE_API_KEY`
- Replace it with your actual Runcrate API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control. The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f runcrate.yaml
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

The template sends requests to Runcrate's OpenAI-compatible endpoint at `https://api.runcrate.ai/v1`.

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers. See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/DeepSeek-V4.1-Flash",
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
    "model": "deepseek-ai/DeepSeek-V4.1-Flash",
    "messages": [
      { "role": "user", "content": "Write a short poem about inference." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/v1/chat/completions
```

## Supported Models

The template configures `deepseek-ai/DeepSeek-V4.1-Flash`. Other model IDs include `moonshotai/Kimi-K3` and `zai-org/GLM-5.3`.

The current list is returned by `GET https://api.runcrate.ai/v1/models`, which needs no API key. Models that can call tools list `tools` in `supported_features`.

## Troubleshooting

If you encounter issues:

- Verify your API key is correct and active
- Confirm the requested model ID appears in the `GET https://api.runcrate.ai/v1/models` response
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

## Configuring More Models

To use more models, add more matching rules to the `AIGatewayRoute` in `runcrate.yaml`. Set each rule's `x-ai-eg-model` header value to a model ID returned by `GET https://api.runcrate.ai/v1/models`.

For example, add Kimi and GLM models alongside the default DeepSeek model:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-runcrate
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
              value: deepseek-ai/DeepSeek-V4.1-Flash
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: moonshotai/Kimi-K3
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: zai-org/GLM-5.3
      backendRefs:
        - name: envoy-ai-gateway-basic-runcrate
```

## Next Steps

After configuring Runcrate, return to [Connect Providers](./index.md) to add another provider.
