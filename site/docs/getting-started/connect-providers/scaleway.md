---
id: scaleway
title: Connect Scaleway
sidebar_position: 9
---

# Connect Scaleway

This guide explains how to connect Agent Router to Scaleway Generative APIs using its OpenAI-compatible Chat Completions API.

## Prerequisites

Before you begin, you'll need:

- A Scaleway project with access to Generative APIs
- A Scaleway API secret key authorized for that project
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Connect Providers](./index.md) overview

## Configuration Steps

### 1. Download the configuration template

Download `examples/basic/scaleway.yaml` from the Agent Router repository.

### 2. Configure Scaleway credentials and project

Edit `scaleway.yaml` and replace:

- `SCW_PROJECT_ID` with your Scaleway project ID
- `SCW_MODEL_ID` with an available model ID
- `SCW_SECRET_KEY` with your Scaleway API secret key

The upstream endpoint is `https://api.scaleway.ai/<project_id>/v1`.

Discover available models using `GET /<project_id>/v1/models` with Bearer authentication.

:::caution Security Note
Never commit real API keys to version control. Use your organization secret-management process for production.
:::

### 3. Apply the configuration

Apply the updated Scaleway configuration to your existing Agent Router installation:

```shell
kubectl apply -f scaleway.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

### 4. Test Chat Completions

Use the gateway URL configured in the [Basic Usage](../basic-usage.md) guide.

Export the same model ID configured in `scaleway.yaml`:

```shell
export SCW_MODEL_ID="llama-3.3-70b-instruct"
```

Replace the example model ID with your selected Scaleway model.

```shell
curl -H "Content-Type: application/json" \
  -H "x-ai-eg-model: ${SCW_MODEL_ID}" \
  -d "{\"model\":\"${SCW_MODEL_ID}\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}" \
  "$GATEWAY_URL/v1/chat/completions"
```

## OpenAI-Compatible Configuration

Scaleway can also be configured as a generic OpenAI-compatible provider:

- Base URL: `https://api.scaleway.ai/<project_id>/v1`
- API key: Your Scaleway API secret key
- Model: A model ID returned by the project-specific `/models` endpoint

Agent Router uses `schema.name: OpenAI` and `schema.prefix: /<project_id>/v1`. No separate API schema is required.

## Troubleshooting

- `401` or `403`: Verify the API key and project permissions.
- `404`: Verify the project ID, endpoint prefix, and model ID.
- `429`: Check project quotas and rate limits.
- Routing errors: Ensure the model ID matches the `x-ai-eg-model` route in `scaleway.yaml`.

See [Supported Providers](/docs/capabilities/llm-integrations/supported-providers) for additional information.
