---
id: getting-started
title: Getting Started
sidebar_position: 2
---

# Getting Started with Agent Router

Agent Router runs two ways. The fastest is one command on your laptop; the same
configuration then ships to Kubernetes for production.

## Run locally in 60 seconds

The standalone CLI starts an OpenAI-compatible router on your machine — no
Kubernetes, no Docker required (Linux and macOS):

```shell
OPENAI_API_KEY=sk-your-key aigw run
```

Then point any OpenAI-compatible client or SDK at `http://localhost:1975/v1`:

```shell
curl http://localhost:1975/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-5", "messages": [{"role": "user", "content": "Say this is a test!"}]}'
```

`aigw` auto-configures from the same environment variables as the OpenAI SDK,
and can also front self-hosted models (Ollama, vLLM) and MCP servers. See
[Installation](../cli/installation.md) for how to get the `aigw` binary, and
[aigw run](../cli/run.md) for provider auto-configuration, MCP gateway mode, and
custom configuration files.

## Deploy on Kubernetes

For production, Agent Router runs as a control plane on Envoy Gateway in your
Kubernetes cluster. It uses the same configuration API you test locally with
`aigw run`, so what works on your laptop deploys unchanged. This guide walks
through the Kubernetes path:

1. [Prerequisites](./prerequisites.md)
   - Setting up your Kubernetes cluster
   - Installing required tools
   - Setting up Envoy Gateway

2. [Installation](./installation.md)
   - Installing Agent Router
   - Configuring the gateway
   - Verifying the installation

3. [Basic Usage](./basic-usage.md)
   - Deploying a basic configuration
   - Making your first request
   - Understanding the response format

4. [Connect Providers](./connect-providers)
   - Setting up OpenAI integration
   - Configuring AWS Bedrock
   - Managing credentials securely

For local Docker or non-Kubernetes deployments, start with the [Agent Router CLI](../cli/) instead.

## Need Help?

If you run into any issues:

- Join our [Community Discord](https://discord.gg/xuxtPq43gZ)
- File an issue on [GitHub](https://github.com/theagentrouter/agent-router/issues)
