---
id: model-name-virtualization
title: Model Name Virtualization
sidebar_position: 7
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

Agent Router provides an advanced model name virtualization capability that allows you to manage and route requests to different AI models seamlessly.
This guide covers the key feature and configuration for model virtualization.

## Motivation

It is not uncommon for multiple AI providers to offer a similar or identical model, such as Llama-3-70b, etc.
However, each provider tends to have its own unique naming convention for the same model.
For example, `Claude 4 Sonnet` is hosted both on GCP and AWS Bedrock, but they have different model names:

- GCP: `claude-sonnet-4@20250514`, etc.
- AWS Bedrock: `anthropic.claude-sonnet-4-20250514-v1:0`

Even within the same provider, model names may vary based on deployment configurations or versions.
For example, an OpenAI Platform request to `gpt-5-nano` might result in a response from `gpt-5-nano-2025-08-07`.
Azure OpenAI uses deployment names in the URL path rather than model names in the request body, so you won't know the model in use until you get the response.

From downstream GenAI applications' perspective, it is beneficial to have a unified model name that abstracts away these differences. We do this while still availing the authoritative model name that served a response.

## Provider-specific Virtualization

AI providers handle model naming and execution differently to balance flexibility, determinism, and optimization. This section categorizes these behaviors into virtualization types, enabling Agent Router to abstract differences for unified downstream access while retaining executed model details where possible.

- **Automatic Routing & Resolution**: Providers select and optimize models at runtime behind generic identifiers, returning the actual executed version for reproducibility and debugging.
- **Static Model Execution**: Uses direct mapping to immutable identifiers or fixed versions for deterministic execution, ensuring requested models run exactly as specified, ideal for consistent embeddings.
- **Deterministic Snapshot Mapping**: Resolves models via endpoint/URL with dated snapshots (no response model field), providing consistent cross-platform behavior through timestamped versions.
- **URI-Based Resolution**: Ignores request body model field, using URI path for deployment-based routing while returning the actual versioned model name in responses.
- **Third-Party Delegation**: Delegates embeddings to specialized third-party services with separate APIs, integrating ecosystem expertise while focusing on core capabilities.

| Upstream         | API Type         | Virtualization Type            | Request Model Example                     | Response Model Example     |
| ---------------- | ---------------- | ------------------------------ | ----------------------------------------- | -------------------------- |
| **awsbedrock**   | Chat Completions | Static Model Execution         | `anthropic.claude-sonnet-4-20250514-v1:0` | N/A (no model field)       |
| **awsbedrock**   | Embeddings       | Static Model Execution         | `amazon.titan-embed-text-v2:0`            | N/A (no model field)       |
| **azureopenai**  | Chat Completions | URI-Based Resolution           | `{any-value}` (ignored)                   | `gpt-5-nano-2025-08-07`    |
| **azureopenai**  | Embeddings       | URI-Based Resolution           | `{any-value}` (ignored)                   | `text-embedding-ada-002-2` |
| **gcpanthropic** | Chat Completions | Deterministic Snapshot Mapping | `claude-sonnet-4@20250514`                | N/A (no model field)       |
| **gcpanthropic** | Embeddings       | Third-Party Delegation         | `voyage-3.5`                              | N/A (no model field)       |
| **gcpvertexai**  | Chat Completions | Deterministic Snapshot Mapping | `gemini-1.5-pro-002`                      | N/A (no model field)       |
| **gcpvertexai**  | Embeddings       | Static Model Execution         | `text-embedding-004`                      | N/A (no model field)       |
| **openai**       | Chat Completions | Automatic Routing & Resolution | `gpt-5-nano`                              | `gpt-5-nano-2025-08-07`    |
| **openai**       | Embeddings       | Static Model Execution         | `text-embedding-3-small`                  | `text-embedding-3-small`   |

### Azure OpenAI URI-Based Behavior

Azure OpenAI has a unique approach where [the model field in the request JSON is completely ignored][azure-model-ignored]. The deployment name in the URI path determines which model is actually used, and the response returns the actual versioned model name:

- **URI Format**: `https://{resource}.openai.azure.com/openai/deployments/{deployment-name}/chat/completions`
- **Request Body**: `{"model": "anything"}` ← This value is ignored
- **Response**: `{"model": "gpt-5-nano-2025-08-07"}` ← Returns the actual versioned model name

This means [Azure OpenAI doesn't read the model field from the request JSON at all][azure-deployment-resolution] - it uses the deployment name from the URI path exclusively, but returns the real underlying versioned model identifier in the response.

### AWS Bedrock Static Execution

AWS Bedrock uses static model execution with immutable versioned identifiers. The Converse API does not return a model field in responses. Each model ID like `anthropic.claude-sonnet-4-20250514-v1:0` represents a specific, frozen model version with no automatic updates or routing.

### GCP Anthropic Embeddings Delegation

GCP Anthropic delegates embeddings to [Voyage AI's specialized embedding models][anthropic-embeddings-voyage] rather than providing Claude-native embeddings. Available models include:

- `voyage-3.5` - Balanced performance embedding model
- `voyage-3-large` - Best quality general-purpose embeddings
- `voyage-code-3` - Optimized for code retrieval
- `voyage-law-2` - Legal domain-specific embeddings

This delegation approach allows Anthropic to focus on language model capabilities while leveraging domain expertise for embeddings.

### OpenAI Virtualization

OpenAI performs automatic model routing where [generic identifiers like `gpt-5-nano` may route to different model versions][openai-model-routing] like `gpt-5-nano-2025-08-07` based on availability and optimization.

### GCP Provider Behavior

Both GCP Anthropic and GCP Vertex AI use endpoint-based model resolution where [the model is specified in the URI path rather than returned in the response body][gcp-endpoint-resolution]. This provides deterministic behavior with timestamped snapshots.

## Virtualization with modelNameOverride API

In our top level AIGatewayRoute configuration, you can specify a `modelNameOverride` inside [AIGatewayRouteBackendRef](/api/api.mdx#aigatewayrouterulebackendref) on each route rule to override the model name that is sent to the upstream AI provider.
This feature is primarily designed for scenarios where you want to dynamically change the model name based on the actual AI provider the request is being sent to.

The example configuration looks like this:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: test-route
spec:
  targetRefs: [...]
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: claude-4-sonnet
      backendRefs:
        - name: aws-backend
          modelNameOverride: anthropic.claude-sonnet-4-20250514-v1:0
          weight: 50
        - name: gcp-backend
          modelNameOverride: claude-sonnet-4@20250514
          weight: 50
```

This configuration allows downstream applications to use a unified model name `claude-4-sonnet` while splitting traffic between the AWS Bedrock and GCP AI providers based on the specified `modelNameOverride`.
This is what the word "Virtualization" means in this context: abstracting away the differences in model names across different AI providers and providing a unified interface for downstream applications.
It also can be thought of as "one-to-many" aliasing of model names, where one unified model name can map to multiple different model names on different providers depending on the routing path.

## Virtualization for fallback scenarios

As we see in the [Provider Fallback](./provider-fallback) page, Agent Router allows you to fallback to a different AI provider if the primary one fails.
However, sometimes we want to fallback to a different model on the same provider.
For example, it is natural to set up the Agent Router in a way that if the primary expensive model fails (rate limit, etc), Envoy retries the request to a less expensive model on the same provider.
More concretely, if the request to `gpt-5-nano` fails, we want to retry it with `gpt-5-nano-mini` on the same OpenAI provider.

`modelNameOverride` can also be used in this scenario to achieve the desired behavior. The configuration would look like this:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: test-route
spec:
  targetRefs: [...]
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: gpt-5-nano
      backendRefs:
        - name: openai-backend
          # This doesn't specify modelNameOverride, so it will use the default model name `gpt-5-nano` in the request.
          priority: 0
        - name: openai-backend
          modelNameOverride: gpt-5-nano-mini
          priority: 1
```

With this configuration, assuming the retry is properly configured as per the [Provider Fallback](./provider-fallback) page, if the request to `gpt-5-nano` fails, Agent Router will automatically retry the request to `gpt-5-nano-mini` on the same OpenAI provider without requiring any changes to the downstream application.

---

[azure-model-ignored]: https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/chatgpt
[azure-deployment-resolution]: https://learn.microsoft.com/en-us/azure/ai-foundry/openai/faq
[anthropic-embeddings-voyage]: https://docs.anthropic.com/en/docs/build-with-claude/embeddings
[openai-model-routing]: https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/chatgpt
[gcp-endpoint-resolution]: https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude
