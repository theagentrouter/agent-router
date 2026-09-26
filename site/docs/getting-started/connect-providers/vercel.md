---
id: vercel
title: Connect Vercel AI Gateway
sidebar_position: 4
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Vercel AI Gateway

This guide will help you configure Agent Router to route traffic to [Vercel AI Gateway](https://vercel.com/docs/ai-gateway),
which exposes models from many providers behind a single OpenAI-compatible endpoint at
`https://ai-gateway.vercel.sh/v1`.

Because Vercel AI Gateway speaks the [OpenAI Chat Completions API](https://vercel.com/docs/ai-gateway/sdks-and-apis/openai-chat-completions)
and authenticates with `Authorization: Bearer <token>`, it is configured with the `OpenAI` schema and
an API key, the same as any other OpenAI-compatible provider. This is useful when you want a single
egress and spend-tracking layer for models that are not hosted in your own cluster.

## Prerequisites

Before you begin, you'll need:

- An [AI Gateway API key](https://vercel.com/docs/ai-gateway/authentication-and-byok#api-key)
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide

Because Vercel AI Gateway fronts many models, the route in this example matches every value of
`x-ai-eg-model` rather than a fixed list. Remove the mock route from the basic setup first so the
two do not overlap:

```shell
kubectl delete aigatewayroute envoy-ai-gateway-basic
```

## Configuration Steps

:::info Ready to proceed?
Ensure you have followed the steps in [Connect Providers](../connect-providers/)
:::

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/envoyproxy/ai-gateway/${vars.aigwGitRef}/examples/basic/vercel.yaml`}
</CodeBlock>

### 2. Configure Vercel AI Gateway Credentials

Edit the `vercel.yaml` file to replace the Vercel placeholder value:

- Find the section containing `VERCEL_AI_GATEWAY_API_KEY`
- Replace it with your actual Vercel AI Gateway API key

:::caution Security Note
Make sure to keep your API key secure and never commit it to version control.
The key will be stored in a Kubernetes secret.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running,
then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f vercel.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers.
See the [Basic Usage](../basic-usage.md) page for instructions.

Vercel AI Gateway model IDs follow the `creator/model-name` format, for example `openai/gpt-4o-mini`
or `anthropic/claude-sonnet-4.5`. See [Models & Providers](https://vercel.com/docs/ai-gateway/models-and-providers)
for the available IDs, or query `https://ai-gateway.vercel.sh/v1/models`.

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Embeddings

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "openai/text-embedding-3-small",
    "input": "Agent Router"
  }' \
  $GATEWAY_URL/v1/embeddings
```

## Using the native Anthropic API

Vercel AI Gateway also serves the native Anthropic Messages API on the same host. To route to it,
configure a second `AIServiceBackend` with the `Anthropic` schema and an `AnthropicAPIKey`
`BackendSecurityPolicy`, which sends the key in the `x-api-key` header:

```yaml
spec:
  schema:
    name: Anthropic
```

Requests then go to `$GATEWAY_URL/anthropic/v1/messages`. See
[Supported Providers](/docs/capabilities/llm-integrations/supported-providers) for the full
schema and authentication matrix.

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
   - 403: The API key's project does not have access to the requested model
   - 429: Rate limit or spend limit exceeded
   - 502: Upstream model provider unavailable
