---
id: connect-providers
title: Connect Providers
sidebar_position: 5
---

# Connect Providers

After setting up the basic AI Gateway with the mock backend, you can configure it to work with real AI model providers. This section will guide you through connecting different AI providers to your gateway.

## Example Providers

In this getting started guide you'll find quickstart setups to connect to the following providers:

- [Tetrate Agent Router Service (TARS)](./tars.md) - Connect to Tetrate Agent Router Service's models
- [OpenAI](./openai.md) - Connect to OpenAI's GPT models
- [Anthropic](./anthropic.md) - Connect to Anthropic's Claude models
- [AWS Bedrock](./aws-bedrock.md) - Access AWS Bedrock's suite of foundation models
- [Azure OpenAI](./azure-openai.md) - Access Azure OpenAI's suite of foundation models
- [GCP VertexAI](./gcp-vertexai.md) - Access GCP Gemini and Anthropic models on VertexAI

:::tip
To learn how to connect to providers see [Connecting to AI Providers](/docs/capabilities/llm-integrations/connect-providers) and you can view all [Supported Providers](/docs/capabilities/llm-integrations/supported-providers).
:::

## Before You Begin

Before configuring any provider, complete the [Basic Usage](../basic-usage.md) guide.

## Security Best Practices

When configuring AI providers, keep these security considerations in mind:

- Store credentials securely using Kubernetes secrets
- Never commit API keys or credentials to version control
- Regularly rotate your credentials
- Use the principle of least privilege when setting up access
- Monitor usage and set up appropriate rate limits

## Next Steps

Choose your provider to get started:

- [Connect Tetrate Agent Router Service (TARS)](./tars.md)
- [Connect OpenAI](./openai.md)
- [Connect Anthropic](./anthropic.md)
- [Connect AWS Bedrock](./aws-bedrock.md)
- [Connect Azure OpenAI](./azure-openai.md)
- [Connect GCP VertexAI](./gcp-vertexai.md)
