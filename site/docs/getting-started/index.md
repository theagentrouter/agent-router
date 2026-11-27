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
Kubernetes and no Docker required (Linux and macOS).

Install `aigw` and start it:

```shell
curl -sSL https://theagentrouter.ai/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
aigw run
```

The first time, `aigw run` asks which providers you want to use (OpenAI,
Anthropic, Tetrate Agent Router Service, Azure OpenAI, Ollama or any
OpenAI-compatible API) and their API keys, then starts routing to them. It can
save the configuration, so the next `aigw run` starts right away. The API keys
are not saved: the setup tells you which environment variables to export for the
next runs.

When it prints `listening on http://localhost:1975`, send a request from a
second terminal, using a model of a provider you configured:

```shell
curl http://localhost:1975/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-5-mini", "messages": [{"role": "user", "content": "Hello!"}]}'
```

The setup prints a ready-to-use request for the providers you chose. Point any
OpenAI-compatible client or SDK at `http://localhost:1975/v1`, or Anthropic
clients at `http://localhost:1975/anthropic`.

`aigw` can also be configured from the same environment variables as the OpenAI
and Anthropic SDKs, front MCP servers, and run custom configuration files. See
[Installation](../cli/installation.md) for other ways to get the `aigw` binary,
and [aigw run](../cli/run.md) for all the options.

## Call models from Python

Building an agent in Python? The [Python SDK](../python-sdk.md) runs Agent
Router for you: `pip install theagentrouter`, then call OpenAI, Anthropic,
Gemini, Bedrock and other providers' models with LiteLLM's API. On macOS (Apple
silicon) and recent Linux, the first call starts a local router; elsewhere, and
later in production, the same code points at a shared Agent Router with two
environment variables.

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
