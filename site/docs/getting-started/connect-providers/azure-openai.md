---
id: azure-openai
title: Connect Azure OpenAI
sidebar_position: 3
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect Azure OpenAI

This guide will help you configure Agent Router to work with Azure OpenAI's foundation models.

There are two ways to do the [Azure OpenAI authentication](https://learn.microsoft.com/en-us/azure/ai-services/openai/reference#authentication): Microsoft Entra ID and API Key.

We will use Microsoft Entra ID to authenticate an application to use the Azure OpenAI service. You can obtain an access token using the OAuth 2.0 client credentials grant flow. This process involves registering the application in Microsoft Entra ID (formerly Azure Active Directory), configuring the appropriate permissions, and acquiring a token from the Microsoft identity platform. The access token is then used as proof of authorization in API requests to the Azure OpenAI endpoint.

For detailed steps, refer to the official [Microsoft documentation](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow#get-a-token).

API Key authentication is not supported yet.

## Prerequisites

Before you begin, you'll need:

- Azure credentials with access to OpenAI service.
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview

## Azure Credential Setup

1. An Azure account with OpenAI service access enabled
2. Your Azure tenant ID, client ID, and client secret key
3. Enabled model access to "GPT-4o"

## Configuration Steps

### 1. Download configuration template

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/azure_openai.yaml`}
</CodeBlock>

### 2. Configure Azure Credentials

Edit the `azure_openai.yaml` file to replace these placeholder values:

- `AZURE_TENANT_ID`: Your Azure tenant ID
- `AZURE_CLIENT_ID`: Your Azure client ID
- `AZURE_CLIENT_SECRET`: Your Azure client secret

:::caution Security Note
Keep your Azure credentials secure and never commit them to version control.
The credentials will be stored in Kubernetes secrets.
:::

### 3. Apply Configuration

Apply the updated configuration and wait for the Gateway pod to be ready. If you already have a Gateway running, then the secret credential update will be picked up automatically in a few seconds.

```shell
kubectl apply -f azure_openai.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

### 4. Test the Configuration

You should have set `$GATEWAY_URL` as part of the basic setup before connecting to providers.
See the [Basic Usage](../basic-usage.md) page for instructions.

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": "Hi."
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

## Troubleshoot

If you encounter issues:

1. Verify your Azure credentials are correct and active
2. Check pod status

```shell
kubectl get pods -n envoy-gateway-system
```

3. View controller logs:

```shell
kubectl logs -n envoy-ai-gateway-system deployment/ai-gateway-controller
```

4. Common errors:
   - 401/403: Invalid credentials or insufficient permissions
   - 404: Model not found or not available in region
   - 429: Rate limit exceeded

## Configuring More Models

To use more models, add more [AIGatewayRouteRule]s to the `azure_openai.yaml` file with the [model ID] in the `value` field. For example, to use [GPT-4.5 Preview]

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-azure
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
              value: gpt-4.5-preview
      backendRefs:
        - name: envoy-ai-gateway-basic-azure
```

[AIGatewayRouteRule]: ../../api/api.mdx#aigatewayrouterule
[model ID]: https://learn.microsoft.com/en-us/azure/ai-services/openai/concepts/models
[GPT-4.5 Preview]: https://learn.microsoft.com/en-us/azure/ai-services/openai/concepts/models?tabs=global-standard%2Cstandard-chat-completions#gpt-45-preview
