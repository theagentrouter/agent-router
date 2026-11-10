---
id: grokified
title: Connect Grokified
sidebar_position: 8
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Grokified

This guide will help you configure Agent Router to work with Grok models through Grokified's OpenAI-compatible API.

## Prerequisites

Before you begin, you'll need:

- A Grokified API key. Sign up at [grokified.com/login](https://grokified.com/login) and create a key in the dashboard. Keys start with `gk_live_` and are shown once.
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info
Ready to proceed? Ensure you have followed the steps in [Connect Providers](./index.md).
:::

### 1. Download configuration template

<CodeBlock language="shell">{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/grokified.yaml`}</CodeBlock>

### 2. Configure Grokified Credentials

Edit the `grokified.yaml` file to replace the Grokified placeholder value:

- Find the section containing `GROKIFIED_API_KEY`
- Replace it with your actual Grokified API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control. The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f grokified.yaml
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

The template sends requests to Grokified's OpenAI-compatible endpoint at `https://api.grokified.com/v1`.

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers. See the [Basic Usage](../basic-usage.md) page for instructions.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "grok-build-0.1",
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
    "model": "grok-build-0.1",
    "messages": [
      { "role": "user", "content": "Write a short poem about gateways." }
    ],
    "stream": true
  }' \
  $GATEWAY_URL/v1/chat/completions
```

## Supported Models

The template configures `grok-build-0.1` (a coding model with a 256K context window) and `grok-4.6` (a general model with a 500K context window). `grok-4.7` is also served, but it needs a Basic or higher Grokified plan: on a prepaid account the API returns a `403` with the code `plan_capability_required`.

See the [Grokified model list](https://grokified.com/docs/models) for the current models, context windows and prices. Grokified charges 50% of the list price on every request, and each non-streaming response carries a `usage.grokified` block with the list price, the amount charged and the remaining balance.

## Troubleshooting

If you encounter issues:

- Verify your API key is correct and active
- Confirm the requested model ID appears in the [Grokified model list](https://grokified.com/docs/models)
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
- `402`: Out of credits; top up the Grokified balance
- `403`: The model needs a higher plan (`plan_capability_required`)
- `429`: Upstream capacity is busy; retry after the interval in the `Retry-After` header

## Configuring More Models

To use more models, add more matching rules to the `AIGatewayRoute` in `grokified.yaml`. Set each rule's `x-ai-eg-model` header value to a model ID from the [Grokified model list](https://grokified.com/docs/models).

For example, add `grok-4.7` alongside the default models (needs a Basic or higher plan):

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-grokified
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
              value: grok-build-0.1
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: grok-4.6
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: grok-4.7
      backendRefs:
        - name: envoy-ai-gateway-basic-grokified
```

## Next Steps

After configuring Grokified, return to [Connect Providers](./index.md) to add another provider.
