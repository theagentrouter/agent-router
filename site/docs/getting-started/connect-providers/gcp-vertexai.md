---
id: gcp-vertexai
title: Connect GCP VertexAI
sidebar_position: 3
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect GCP VertexAI

This guide will help you configure Agent Router to work with GCP VertexAI's Gemini and Anthropic models.

## Prerequisites

Before you begin, you'll need:

- GCP credentials with access to GCP VertexAI
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Authentication Options

Agent Router supports three authentication methods for GCP VertexAI:

1. **Application Default Credentials (ADC)** - Recommended for GKE with Workload Identity
2. **Service Account Key Files** - For explicit JSON credentials
3. **Workload Identity Federation** - For cross-cloud authentication
4. **Pass-Through** - The client supplies its own GCP access token, project, and region

### Option 1: Application Default Credentials (Recommended for GKE)

When running on GKE with Workload Identity, configure only `projectName` and `region`:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: BackendSecurityPolicy
metadata:
  name: gcp-credentials
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: your-backend
  type: GCPCredentials
  gcpCredentials:
    projectName: YOUR_PROJECT_NAME
    region: us-central1
```

ADC automatically handles credential rotation via GKE Workload Identity.

### Option 2: Service Account Key Files

For non-GKE environments, use a service account key file:

1. Your GCP project id and name.
2. In your GCP project, enable VertexAI API access.
3. Create a GCP service account and generate the JSON key file.

:::caution Security Note
Service account key files should be avoided in production when possible. Use ADC/Workload Identity instead.
:::

### Option 4: Pass-Through Mode

Pass-through mode makes the gateway credential-less for a backend: it still translates the request
and builds the VertexAI URL path, but it does not obtain, rotate, or attach any GCP credentials.
The calling client authenticates to VertexAI itself, and selects the GCP project and region per
request through headers.

This is useful when tokens already exist on the caller side — for example a multi-tenant platform
where each caller has its own GCP project and short-lived token, and you do not want the gateway to
hold credentials for any of them.

Set `isPassthrough: true` and omit `projectName`/`region`:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: BackendSecurityPolicy
metadata:
  name: gcp-credentials-passthrough
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: your-backend
  type: GCPCredentials
  gcpCredentials:
    isPassthrough: true
```

`projectName` and `region` are normally required, but are optional when `isPassthrough` is `true`.

#### Request headers

In pass-through mode the client is responsible for these headers:

| Header          | Required                           | Purpose                                                       |
| --------------- | ---------------------------------- | ------------------------------------------------------------- |
| `Authorization` | Yes                                | `Bearer <GCP access token>`. Forwarded to VertexAI unchanged. |
| `gcp-project`   | Unless `projectName` is configured | GCP project used in the VertexAI URL path.                    |
| `gcp-region`    | Unless `region` is configured      | GCP location used in the VertexAI URL path.                   |

Configured values take precedence: if `projectName` and/or `region` are set alongside
`isPassthrough: true`, the configured value wins and the corresponding header is ignored. This lets
you pin the project while letting callers choose the region, or the other way around.

If a value is neither configured nor present as a header, the request is rejected and the external
processor logs `gcp-project header must be specified` (or `gcp-region header must be specified`).

Example request:

```shell
curl -H "Content-Type: application/json" \
  -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  -H "gcp-project: YOUR_PROJECT_NAME" \
  -H "gcp-region: us-central1" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Things to be aware of

- `gcp-region` only changes the URL path (`/v1/projects/<project>/locations/<region>/...`); it does
  not change which upstream host the request is sent to. Route to an `AIServiceBackend` whose
  hostname serves the location you pass — for example `us-central1-aiplatform.googleapis.com` for
  `us-central1` — otherwise VertexAI rejects the request.
- The gateway performs no credential rotation or validation in this mode. An expired or
  insufficiently scoped token surfaces as a 401/403 from VertexAI.
- Because the caller chooses the project and the token, apply gateway-level authentication and
  authorization in front of the route so that only trusted clients can use it.

## Configuration Steps

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/gcp_vertex.yaml`}
</CodeBlock>

### 2. Configure GCP Credentials

Edit the `gcp_vertex.yaml` file to replace these placeholder values:

- `GCP_PROJECT_NAME`: Your GCP project name
- `GCP_REGION`: GCP region
- Update the generated service account key JSON string in the secret

:::caution Security Note
Make sure to keep your GCP service account credentials secure and never commit them to version control.
The credentials will be stored in Kubernetes secrets.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running,
then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f gcp_vertex.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers.
See the [Basic Usage](../basic-usage.md) page for instructions.

To access a Gemini model with chat completion endpoint:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

To access an Anthropic model with chat completion endpoint:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-7-sonnet@20250219",
    "messages": [
      {
        "role": "user",
        "content": "What is capital of France?"
      }
    ],
    "max_completion_tokens": 100
  }' \
  $GATEWAY_URL/v1/chat/completions
```

Expected output:

```json
{
  "choices": [
    {
      "finish_reason": "stop",
      "index": 0,
      "message": {
        "content": "The capital of France is Paris. Paris is not only the capital city but also the largest city in France, known for its cultural significance, historic landmarks like the Eiffel Tower and the Louvre Museum, and its influence in fashion, art, and cuisine.",
        "role": "assistant"
      }
    }
  ],
  "object": "chat.completion",
  "usage": { "completion_tokens": 58, "prompt_tokens": 13, "total_tokens": 71 }
}
```

You can also access an Anthropic model with native Anthropic messages endpoint:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-7-sonnet@20250219",
    "messages": [
      {
        "role": "user",
        "content": "What is capital of France?"
      }
    ],
    "max_tokens": 100
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

## Troubleshooting

If you encounter issues:

1. Verify your GCP credentials are correct and active
2. Check pod status:
   ```shell
   kubectl get pods -n envoy-gateway-system
   ```
3. View controller logs:
   ```shell
   kubectl logs -n envoy-ai-gateway-system deployment/ai-gateway-controller
   ```
4. Common errors:
   - 401/403: Invalid credentials or insufficient permissions
   - 404: Model not found or not available in a region
   - 429: Rate limit exceeded

## Configuring More Models

To use more models, add more [AIGatewayRouteRule]s to the `gcp_vertex.yaml` file with the [model ID] in the `value` field. For example, to use [Claude 3 Sonnet]

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-gcp-gemini
  namespace: default
spec:
  schema:
    name: OpenAI
  parentRefs:
    - name: envoy-ai-gateway-basic
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: gemini-2.5-flash-pro
      backendRefs:
        - name: envoy-ai-gateway-basic-gcp
```

[AIGatewayRouteRule]: ../../api/api.mdx#aigatewayrouterule
[model ID]: https://cloud.google.com/vertex-ai/generative-ai/docs/models
[Anthropic Claude]: https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude
