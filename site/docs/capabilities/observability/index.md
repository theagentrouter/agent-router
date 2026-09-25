---
id: observability
title: Observability
---

Agent Router extends the capabilities of Envoy Gateway, and as you run Envoy Gateway you have access to the foundational observability in the Envoy Gateway system.
We recommend you familiarize yourself with the [Envoy Gateway Observability Documentation](https://gateway.envoyproxy.io/docs/tasks/observability/).

## AI/LLM Observability Features

The Agent Router provides specialized observability capabilities for AI and LLM workloads:

- **[GenAI Metrics](./metrics.md)** - Prometheus metrics following OpenTelemetry Gen AI semantic conventions for monitoring token usage, latency, and model performance.
- **[GenAI Tracing](./tracing.md)** - OpenTelemetry integration with OpenInference semantic conventions for LLM request tracing and evaluation.
- **[Access Logs with AI/LLM metadata](./accesslogs.md)** - AI metadata produced by the AI gateway (model name, token usage, etc.) can be included in the Envoy Access Logs.
- **[Gateway Configuration](../gateway-config.md)** - Per-gateway configuration of the external processor container, including environment variables for tracing and resource requirements.
