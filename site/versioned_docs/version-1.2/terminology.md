---
id: terminology
title: Terminology
sidebar_position: 6
---

# AI Gateway Glossary

This glossary provides definitions for key terms and concepts used in AI Gateway and GenAI traffic handling.

## Quick Reference

| Term             | Quick Definition                                   |
| ---------------- | -------------------------------------------------- |
| GenAI Gateway    | Gateway for managing AI model traffic              |
| Foundation Model | Base pre-trained AI model                          |
| Token            | Basic unit of text in LLM processing               |
| Token Usage      | Monitoring and limiting model resource consumption |
| Model Routing    | Directing requests to appropriate models           |
| Prompt           | Input text guiding AI model response               |
| Temperature      | Control for model output randomness                |

## Categories

- [AI/ML Fundamentals](#ai-ml-fundamentals): Token, Prompt, Context Window, Temperature
- [Inference Infrastructure](#inference-infrastructure): Inference Instance, Service, Model Provider
- [Gateway Components](#gateway-components): GenAI Gateway, Gateway API Inference Extension
- [Usage & Analytics](#usage--analytics): Usage Monitoring, Token Usage Limiting
- [Model Types & Management](#model-types--management): Foundation Model, Fine-Tuned Model
- [Content & Safety](#content--safety): Content Filtering

## AI/ML Fundamentals {#ai-ml-fundamentals}

### Context Window

The maximum amount of text (in tokens) that a model can process in a single request.

**Related**: [Token](#token)

### Prompt

The input text that guides the AI model's response, including instructions, context, and specific queries.

### Temperature

A parameter that controls the randomness/creativity of model outputs, typically ranging from 0 (deterministic) to 1 (more creative).

### Token

The basic unit of text processing in LLMs, representing parts of words or characters.

**Related**: [Context Window](#context-window) · [Token Cost](#token-cost)

### Token Cost

The financial or resource cost associated with token usage in model requests.

**Related**: [Token](#token) · [Rate of LLM Token Consumption](#rate-of-llm-token-consumption)

## Content & Safety {#content--safety}

### Content Filtering

A mechanism to screen and moderate AI-generated content to ensure compliance with ethical standards, company policies, or regulatory requirements.

## Gateway Components {#gateway-components}

### Gateway API Inference Extension

A Kubernetes SIG Network extension for Gateway API that provides specialized routing and load balancing capabilities for AI/ML workloads, handling traffic management at the level of inference instances.

**Related**: [Inference Instance](#inference-instance)

### GenAI Gateway

A specialized gateway solution designed to manage, monitor, and route traffic to Generative AI models. It provides capabilities such as load balancing, authorization, token usage monitoring, and integration with multiple model providers.

**Related**: [Token](#token) · [Model Provider](#model-provider)

### Hybrid GenAI Gateway

A GenAI Gateway configuration that supports both local inference instances and external cloud-based AI models, providing flexibility in deployment and cost management.

**Related**: [GenAI Gateway](#genai-gateway) · [Inference Instance](#inference-instance) · [Model Provider](#model-provider)

## Inference Infrastructure {#inference-infrastructure}

### Inference Instance

An individual compute resource or container used to run a machine learning model for generating AI outputs (inference).

### Inference Service

A service that provides model inference capabilities, including model loading, input processing, inference execution, and output formatting.

**Related**: [Inference Instance](#inference-instance)

### Model Endpoint

The API endpoint provided by a specific AI model, whether hosted by a cloud provider, open-source solution, or private deployment.

### Model Provider

Services providing AI model capabilities through APIs, which can be either first-party providers who develop their own models (like OpenAI, Anthropic) or third-party providers who host other companies' models (like AWS Bedrock, Azure OpenAI Service).

## Model Types & Management {#model-types--management}

### Fine-Tuned Model

A version of a base Generative AI model that has been customized for specific tasks or domains using additional training data.

**Related**: [Foundation Model](#foundation-model)

### Foundation Model

Foundation models are large-scale, pre-trained AI models designed to handle a broad range of tasks. They are trained on extensive datasets and can be fine-tuned or adapted to specific use cases.

**Related**: [Fine-Tuned Model](#fine-tuned-model)

### Model Routing

A feature in GenAI Gateways that dynamically routes requests to specific models or model versions based on client configuration, use case requirements, or service level agreements.

**Related**: [GenAI Gateway](#genai-gateway)

## Usage & Analytics {#usage--analytics}

### GenAI Usage Analytics

The collection and analysis of data regarding how users interact with AI models via the GenAI Gateway, including token usage, request patterns, and latency metrics.

**Related**: [GenAI Gateway](#genai-gateway) · [Token](#token)

### GenAI Usage Monitoring

The tracking of resource consumption across different types of models, including token-based monitoring for LLMs, image resolution and compute resources for LVMs, and combined metrics for multimodal models.

**Related**: [Token](#token)

### LLM Token Usage Limiting

A mechanism to monitor and control the number of tokens processed by an LLM GenAI model, including input, output, and total token limits.

**Related**: [Token](#token) · [GenAI Gateway](#genai-gateway)

### Rate of LLM Token Consumption

The speed at which tokens are consumed by an AI model during processing. This metric is crucial for cost estimation and performance optimization.

**Related**: [Token](#token)

:::note
This glossary is continuously evolving as the field of GenAI traffic handling develops. If you'd like to contribute or suggest changes, please visit our [GitHub repository](https://github.com/theagentrouter/agent-router).
:::

:::tip See Also

- Check our [Getting Started](./getting-started/index.md) guide for practical examples
- Join our [Community Discord](https://discord.gg/xuxtPq43gZ) for discussions
  :::
