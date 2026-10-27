---
id: anthropic
title: Connect Anthropic
sidebar_position: 6
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Anthropic

This guide will help you configure Agent Router to work with Anthropic's models.

## Prerequisites

Before you begin, you'll need:

- An Anthropic API key from [Anthropic's console](https://console.anthropic.com)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Configuration Steps

:::info Ready to proceed?
Ensure you have followed the steps in [Connect Providers](../connect-providers/)
:::

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/anthropic.yaml`}
</CodeBlock>

### 2. Configure Anthropic Credentials

Edit the `anthropic.yaml` file to replace the Anthropic placeholder value:

- Find the section containing `ANTHROPIC_API_KEY`
- Replace it with your actual Anthropic API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control.
The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running,
then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f anthropic.yaml

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
    "model": "claude-sonnet-4-5",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
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
   - 503: Anthropic service unavailable

## Configuring More Models

To use more models, add more [AIGatewayRouteRule]s to the `anthropic.yaml` file with the model name in the `value` field.

For example, let's add [claude-haiku-3-5](https://docs.anthropic.com/en/docs/about-claude/models#claude-3.5-haiku) as a chat completion model:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-anthropic
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
              value: claude-sonnet-4-5
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: claude-haiku-3-5
      backendRefs:
        - name: envoy-ai-gateway-basic-anthropic
```

## Next Steps

After configuring Anthropic:

- [Connect OpenAI](./openai.md) to add another provider
- [Connect AWS Bedrock](./aws-bedrock.md) to add another provider

[AIGatewayRouteRule]: ../../api/api.mdx#aigatewayrouterule
