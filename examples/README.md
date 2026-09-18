# Envoy AI Gateway Examples

This directory contains various examples demonstrating different features and use cases of Envoy AI Gateway.

### [Basic Setup](./basic/)

A comprehensive example showing how to set up Envoy AI Gateway with multiple providers including OpenAI, AWS Bedrock, and Azure OpenAI.

## Model Context Protocol (MCP)

### [MCP Gateway](./mcp/)

Examples demonstrating how to configure the MCP Gateway to connect AI agents to external tools and data sources. Includes:

- Server multiplexing with multiple MCP backends (GitHub, Context7, AWS Knowledge, etc.)
- Tool filtering to control exposed capabilities
- OAuth authentication with Keycloak
- Combining LLM routes and MCP routes on the same Gateway

### [Goose Integration](./goose/)

Shows how to integrate the Goose AI agent framework with MCP tools,
demonstrating unified routing of both LLM and MCP calls through the Envoy AI
Gateway for single-agent origin.

## Advanced Features

### [Provider Fallback](./provider_fallback/)

Shows how to configure automatic failover between multiple AI providers for high availability.

### [Token Rate Limiting](./token_ratelimit/)

Demonstrates usage-based rate limiting to control costs and prevent abuse.

### [Cache Control](cache/cache_control.md)

Shows how to use prompt caching with Claude models to reduce costs and improve performance through provider-agnostic cache control.

### [Monitoring](./monitoring/)

Example setup for comprehensive monitoring and observability with Prometheus and Grafana.

### [Guardrails](./guardrails/)

Shows how to enforce request and response content policies with regex, Presidio, AWS Bedrock Guardrails, and Azure AI Content Safety.
