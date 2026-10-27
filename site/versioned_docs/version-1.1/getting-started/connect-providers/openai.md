---
id: openai
title: Connect OpenAI
sidebar_position: 2
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect OpenAI

This guide will help you configure Agent Router to work with OpenAI's models.

## Prerequisites

Before you begin, you'll need:

- An OpenAI API key from [OpenAI's platform](https://platform.openai.com)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info Ready to proceed?
Ensure you have followed the steps in [Connect Providers](../connect-providers/)
:::

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/openai.yaml`}
</CodeBlock>

### 2. Configure OpenAI Credentials

Edit the `openai.yaml` file to replace the OpenAI placeholder value:

- Find the section containing `OPENAI_API_KEY`
- Replace it with your actual OpenAI API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control.
The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running,
then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f openai.yaml

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

OpenAI supports the legacy completions endpoint with specific models:

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
   - 503: OpenAI service unavailable

## Configuring More Models

To use more models, add more [AIGatewayRouteRule]s to the `openai.yaml` file with the [model alias] in the `value` field.

For example, let's add [o1] as a chat completion model, and [text-embedding-ada-002](https://platform.openai.com/docs/models/text-embedding-ada-002) as embedding models:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-openai
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
              value: o1
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: text-embedding-ada-002
      backendRefs:
        - name: envoy-ai-gateway-basic-openai
```

## Next Steps

After configuring OpenAI:

- [Connect AWS Bedrock](./aws-bedrock.md) to add another provider

[AIGatewayRouteRule]: ../../api/api.mdx#aigatewayrouterule
[model alias]: https://platform.openai.com/docs/models#current-model-aliases
[o1]: https://platform.openai.com/docs/models#o1
