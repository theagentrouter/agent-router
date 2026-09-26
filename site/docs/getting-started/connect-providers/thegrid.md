---
id: thegrid
title: Connect The Grid
sidebar_position: 5
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect The Grid

This guide will help you configure Agent Router to work with [The Grid](https://thegrid.ai)'s instruments.

The Grid is an OpenAI-compatible inference API whose model ids are **market instruments** rather than fixed
models. You request a quality tier — `text-standard`, `code-prime`, `agent-max` — and The Grid acquires
qualifying inference on its market to serve the request. Lab-scoped markets such as `claude-opus-latest` and
`kimi-latest` are also available when you want to stay within one model family.

Because an instrument pools several backing models, the `model` field of a response names the model that
actually served the request rather than the instrument you asked for.

## Prerequisites

Before you begin, you'll need:

- An API key from [The Grid](https://thegrid.ai)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info Ready to proceed?
Ensure you have followed the steps in [Connect Providers](../connect-providers/)
:::

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/thegrid.yaml`}
</CodeBlock>

### 2. Configure The Grid Credentials

Edit the `thegrid.yaml` file to replace The Grid placeholder value:

- Find the section containing `THEGRID_API_KEY`
- Replace it with your actual The Grid API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control.
The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running,
then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f thegrid.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers.
See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "text-standard",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Choosing an instrument

The full list, with per-instrument context limits, capability flags and current prices, is published at
`GET https://api.thegrid.ai/v1/models`.

| Instrument                                                                                                                                                | Use                     |
| --------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `text-standard`, `code-standard`, `agent-standard`                                                                                                        | everyday work           |
| `text-prime`, `code-prime`, `agent-prime`                                                                                                                 | stronger reasoning      |
| `text-max`, `code-max`, `agent-max`                                                                                                                       | frontier                |
| `claude-opus-latest`, `gpt-sol-latest`, `gemini-pro-latest`, `kimi-latest`, `deepseek-pro-latest`, `glm-latest`, `minimax-latest`, `bytedance-pro-latest` | a specific model family |

:::note max_tokens
Instruments reason before answering, and reasoning tokens count against `max_tokens`. A budget that is too
small can be consumed entirely by reasoning, returning empty content with `finish_reason: "length"`. Leave
headroom for short answers.
:::

## Troubleshooting

If you encounter issues:

1. Verify your API key is correct and active

2. Check pod status:

   ```shell
   kubectl get pods -n envoy-gateway-system
   ```

3. View controller logs:

   ```shell
   kubectl logs -n envoy-ai-gateway-system deployment/ai-gateway-controller
   ```

4. View External Processor Logs

   ```shell
   kubectl logs -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic -c ai-gateway-extproc
   ```
