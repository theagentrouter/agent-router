---
id: inference
title: Inference Optimization
---

# Inference Optimization

Agent Router offers smart ways to improve the speed and reliability of your AI/LLM tasks. This section explains how it uses intelligent routing and load balancing to manage inference requests across different backend endpoints efficiently.

![](/img/resource-model.png)

## Overview

The inference optimization capabilities in Agent Router enable:

- **Intelligent Endpoint Selection**: Automatically route requests to the most suitable inference endpoints based on real-time metrics and availability
- **Dynamic Load Balancing**: Distribute inference workloads across multiple backend instances for optimal resource utilization
- **Seamless Integration**: Work with both standard HTTPRoute and AI Gateway's enhanced AIGatewayRoute configurations
- **Extensible Architecture**: Support for custom endpoint picker providers (EPP) to implement domain-specific routing logic

## Getting Started

To get started with InferencePool support in Agent Router:

1. **[Learn about InferencePool Support](./inferencepool-support.md)**: Understand the core concepts and benefits
2. **[Try HTTPRoute + InferencePool](./httproute-inferencepool.md)**: Start with basic inference routing
3. **[Explore AIGatewayRoute + InferencePool](./aigatewayroute-inferencepool.md)**: Leverage advanced AI-specific features
