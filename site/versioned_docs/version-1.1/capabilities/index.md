---
id: capabilities
title: Capabilities
sidebar_position: 5
---

# Agent Router Capabilities

Welcome to the Agent Router capabilities documentation! This section provides detailed information about the various features and capabilities that Agent Router offers to help you manage and optimize your AI/LLM traffic.

## LLM Providers Integrations

Support for various Large Language Model providers:

- **[Connecting to AI Providers](./llm-integrations/connect-providers.md)**: Learn how to establish connectivity with any supported AI provider
- **[Supported Providers](./llm-integrations/supported-providers.md)**: Compatible AI/LLM service providers
- **[Supported Endpoints](./llm-integrations/supported-endpoints.md)**: Available API endpoints and operations
- **[Vendor-Specific Fields](./llm-integrations/vendor-specific-fields.md)**: Use backend-specific parameters and access provider-unique capabilities in your OpenAI-compatible requests
- **[Prompt Caching](./llm-integrations/prompt-caching.md)**: Provider-agnostic prompt caching using unified cache_control API

## Inference Optimization

Advanced inference optimization capabilities for AI/LLM workloads:

- **[InferencePool Support](./inference/inferencepool-support.md)**: Intelligent routing and load balancing for inference endpoints
- **[HTTPRoute + InferencePool](./inference/httproute-inferencepool.md)**: Basic inference routing with standard Gateway API
- **[AIGatewayRoute + InferencePool](./inference/aigatewayroute-inferencepool.md)**: Advanced AI-specific routing with enhanced features

## Gateway Configuration

- **[GatewayConfig](./gateway-config.md)**: Gateway-scoped configuration for the external processor (env vars, resources, shared settings)
- **[Scaling](./scaling.md)**: Configure multiple controller replicas and horizontal pod autoscaling for production deployments

## Traffic Management

Comprehensive traffic handling and routing capabilities:

- **[Model Virtualization](./traffic/model-virtualization.md)**: Abstract and virtualize AI models
- **[Provider Fallback](./traffic/provider-fallback.md)**: Automatic failover between AI providers
- **[Quota Policy](./traffic/quota-policy.md)**: Token-based quota management for controlling total consumption budgets
- **[Usage-based Rate Limiting](./traffic/usage-based-ratelimiting.md)**: Token-aware rate limiting for AI workloads
- **[Header and Body Mutations](./traffic/header-body-mutations.md)**: Customize HTTP headers and JSON body fields per backend or route

## Security

Robust security features for AI gateway deployments:

- **[Upstream Authentication](./security/upstream-auth.mdx)**: Secure authentication to upstream AI services

## Model Context Protocol (MCP)

Connect AI agents to external tools and data sources:

- **[MCP Gateway](./mcp/)**: Server multiplexing, tool routing, OAuth authentication, and observability for MCP workloads

## Observability

Monitoring and observability tools for AI workloads:

- **[Metrics](./observability/metrics.md)**: Comprehensive metrics collection and monitoring
