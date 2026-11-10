---
id: vendor-specific-fields
title: Extension Fields
---

# Extension Fields

The AI Gateway supports extension fields that allow you to specify unified or backend-specific parameters directly as inline fields in your OpenAI-compatible requests. These fields are applied during the translation process to the target backend's native API format.

## Overview

Extension fields enable you to:

- Use advanced backend-specific features not available in the OpenAI API
- Use unified configuration fields that work across multiple providers not available in the OpenAI API

### Vendor Extension Fields

Vendor specific fields are specified as inline fields in your OpenAI request and are applied after the standard OpenAI-to-backend translation.

### Unified Extension Fields

For thinking/reasoning capabilities, you can use a unified `thinking` field that automatically translates to the correct backend-specific format:

- **GCP Vertex AI (Gemini)**: Translates to `generationConfig.thinkingConfig`
- **GCP Anthropic**: Uses `thinking` field directly
- **AWS Bedrock**: Uses `thinking` field directly

This unified approach allows you to write provider-agnostic requests while still leveraging thinking capabilities.

## Supported Backends

The following backends support extension fields:

### GCP Vertex AI (Gemini)

- **API Schema Name**: `GCPVertexAI`
- **Supported Fields**:
  - `safetySettings`: Configure the safety settings for gemini models that translates to `SafetySetting`. [Gemini Docs](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/multimodal/configure-safety-filters)
  - `thinking`: Configure thinking process for reasoning models that automatically translates to `generationConfig.thinkingConfig`. [Gemini Docs](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/thinking)
- **Supported Tools**:
  - `google_search`: Enable Google Search grounding for Gemini models. Configuration options vary by platform: `exclude_domains` and `blocking_confidence` are Vertex AI only, while `time_range_filter` is Gemini API only. [Google Search Grounding Docs](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/grounding/grounding-with-google-search)

### GCP Anthropic

- **API Schema Name**: `GCPAnthropic`
- **Supported Fields**:
  - `thinking`: Configuration for enabling Claude's extended thinking. [Anthropic Docs](https://docs.anthropic.com/en/api/messages#body-thinking)

### AWS Bedrock

- **API Schema Name**: `AWSBedrock`
- **Supported Fields**:
  - `thinking`: Configuration for enabling Anthropic Claude's extended thinking. [AWS Docs](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-extended-thinking.html)

## Usage

Add extension fields directly as inline fields in your OpenAI request:

### Using Unified Thinking Configuration

The simplest way to enable thinking capabilities across all providers is to use the unified `thinking` field:

```json
{
  "model": "gemini-2.5-pro",
  "messages": [
    {
      "role": "user",
      "content": "Explain quantum computing and show me a simple code example."
    }
  ],
  "temperature": 0.7,
  "max_tokens": 2000,
  "thinking": {
    "type": "enabled",
    "budget_tokens": 1000,
    "includeThoughts": true
  }
}
```

This configuration will work with any provider that supports thinking, automatically translating to the correct backend format.

### Using Provider-Specific Fields

For more fine-grained control or provider-specific features, you can use the vendor-specific fields like `safetySettings` for gemini models:

```json
{
  "model": "gemini-1.5-pro",
  "messages": [
    {
      "role": "user",
      "content": "Explain quantum computing and show me a simple code example."
    }
  ],
  "temperature": 0.7,
  "max_tokens": 2000,
  "safetySettings": [
    {
      "category": "HARM_CATEGORY_HARASSMENT",
      "threshold": "BLOCK_ONLY_HIGH"
    }
  ]
}
```

### Using Google Search Grounding

To enable Google Search grounding for Gemini models, add `google_search` to the tools array.

For basic usage without configuration options:

```json
{
  "model": "gemini-2.0-flash",
  "messages": [
    {
      "role": "user",
      "content": "What are the latest developments in quantum computing?"
    }
  ],
  "tools": [
    {
      "type": "google_search"
    }
  ]
}
```

For Vertex AI, you can add filtering options:

```json
{
  "model": "gemini-2.0-flash",
  "messages": [
    {
      "role": "user",
      "content": "What are the latest developments in quantum computing?"
    }
  ],
  "tools": [
    {
      "type": "google_search",
      "google_search": {
        "exclude_domains": ["example.com"],
        "blocking_confidence": "BLOCK_LOW_AND_ABOVE"
      }
    }
  ]
}
```

### Field Conflicts

Vendor fields override translated fields when conflicts occur.

When using unified thinking configuration, the `thinking` field takes precedence over any provider-specific thinking configurations.

### Unsupported Fields/Backends

Fields and Backends other than specified in [Supported Backends](#supported-backends) will be ignored.
