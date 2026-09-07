---
id: getting-started
title: Getting Started
sidebar_position: 2
---

# Getting Started with Envoy AI Gateway

Welcome to the Envoy AI Gateway getting started guide!

This guide will walk you through setting up and using Envoy AI Gateway, a tool for managing GenAI traffic using Envoy.
The getting started flow uses Kubernetes by default. If you want to run Envoy AI Gateway as a standalone local proxy with Docker Compose or a config file, use the [CLI run guide](../cli/run.md) and the [`cmd/aigw` Docker Compose examples](https://github.com/envoyproxy/ai-gateway/tree/main/cmd/aigw).

## Guide Structure

This getting started guide is organized into several sections:

1. [Prerequisites](./prerequisites.md)
   - Setting up your Kubernetes cluster
   - Installing required tools
   - Setting up Envoy Gateway

2. [Installation](./installation.md)
   - Installing Envoy AI Gateway
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

For local Docker or non-Kubernetes deployments, start with the [Envoy AI Gateway CLI](../cli/) instead.

## Need Help?

If you run into any issues:

- Join our [Community Slack](https://envoyproxy.slack.com/archives/C07Q4N24VAA)
- File an issue on [GitHub](https://github.com/envoyproxy/ai-gateway/issues)
