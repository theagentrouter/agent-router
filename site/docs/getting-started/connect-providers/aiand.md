---
id: aiand
title: Connect ai&
sidebar_position: 9
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect ai&

This guide will help you configure Agent Router to work with [ai&](https://aiand.com)'s open models, served from Japan, through its OpenAI-compatible API.

## Prerequisites

Before you begin, you'll need:

- An API key from the [ai& console](https://console.aiand.com)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info
Ready to proceed? Ensure you have followed the steps in [Connect Providers](./index.md).
:::

### 1. Download configuration template

<CodeBlock language="shell">{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/aiand.yaml`}</CodeBlock>

### 2. Configure ai& Credentials

Edit the `aiand.yaml` file to replace the ai& placeholder value:

- Find the section containing `AIAND_API_KEY`
- Replace it with your actual ai& API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control. The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f aiand.yaml
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

The template sends requests to ai&'s OpenAI-compatible endpoint at `https://api.aiand.com/v1`.

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers. See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/deepseek-v4.1-flash-1017",
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
    "model": "deepseek-ai/deepseek-v4.1-flash-1017",
    "messages": [
      { "role": "user", "content": "Write a short poem about inference." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Responses

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/deepseek-v4.1-flash-1017",
    "input": "Explain serverless inference in one sentence."
  }' \
  $GATEWAY_URL/v1/responses
```

#### Test Anthropic Messages

The gateway translates Anthropic Messages requests for this backend, so the same configuration also serves Anthropic-format clients:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/deepseek-v4.1-flash-1017",
    "max_tokens": 256,
    "messages": [
      { "role": "user", "content": "Explain serverless inference in one sentence." }
    ]
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

To stream the reply, add `"stream": true`:

```shell
curl -N -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/deepseek-v4.1-flash-1017",
    "max_tokens": 256,
    "messages": [
      { "role": "user", "content": "Write a short poem about inference." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

## Supported Models

The template configures `deepseek-ai/deepseek-v4.1-flash-1017`.

The current list, with each model's capabilities and prices, is public at `https://api.aiand.com/v1/models` (no API key needed) and in the [ai& model catalog](https://docs.aiand.com/models/catalog/).

## Troubleshooting

If you encounter issues:

- Verify your API key is correct and active
- Confirm the requested model ID appears in `https://api.aiand.com/v1/models`
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
- `503`: ai& service unavailable

## Configuring More Models

To use more models, add more matching rules to the `AIGatewayRoute` in `aiand.yaml`. Set each rule's `x-ai-eg-model` header value to a model ID from `https://api.aiand.com/v1/models`.

## Next Steps

After configuring ai&, return to [Connect Providers](./index.md) to add another provider.
