---
id: tars
title: Connect Tetrate Agent Router Service (TARS)
sidebar_position: 4
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Tetrate Agent Router Service (TARS)

This guide will help you configure Agent Router to work with Tetrate Agent Router Service (TARS)'s models.

## Prerequisites

Before you begin, you'll need:

- An API key from [Tetrate Agent Router Service's platform](https://router.tetrate.ai/)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info Ready to proceed?
Ensure you have followed the steps in [Connect Providers](../connect-providers/)
:::

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/tars.yaml`}
</CodeBlock>

### 2. Configure Tetrate Agent Router Service (TARS) Credentials

Edit the `tars.yaml` file to replace the TARS placeholder value:

- Find the section containing `TARS_API_KEY`
- Replace it with your actual TARS API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control.
The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running,
then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f tars.yaml

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
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Completions (Legacy)

TARS fully supports the legacy completions endpoint:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "babbage-002",
    "prompt": "def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)",
    "max_tokens": 25,
    "temperature": 0.4,
    "top_p": 0.9
  }' \
  $GATEWAY_URL/v1/completions
```

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

5. Common errors:
   - 401: Invalid API key
   - 429: Rate limit exceeded
   - 503: TARS service unavailable
