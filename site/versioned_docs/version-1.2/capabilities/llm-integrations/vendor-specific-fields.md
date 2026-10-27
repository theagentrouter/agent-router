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
- **Supported Tool Fields** (set on an entry of `tools[].function`):
  - `eager_input_streaming`: Stream a tool's input as the model generates it, instead of buffering and validating each parameter value first. [Anthropic Docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming)

### AWS Anthropic

- **API Schema Name**: `AWSAnthropic`
- **Supported Tool Fields** (set on an entry of `tools[].function`):
  - `eager_input_streaming`: Stream a tool's input as the model generates it, instead of buffering and validating each parameter value first. [Anthropic Docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming)

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

### Using Eager Tool Input Streaming

By default, Anthropic buffers each tool parameter value and validates it before emitting it to the stream. Setting `eager_input_streaming` on a tool makes the model stream that tool's input as it is generated, so `tool_calls[].function.arguments` fragments start arriving sooner. This matches the per-token streaming behavior of OpenAI and most other backends.

```json
{
  "model": "gcp.claude-3-5-haiku",
  "messages": [
    {
      "role": "user",
      "content": "What's the weather in New York?"
    }
  ],
  "stream": true,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "eager_input_streaming": true,
        "parameters": {
          "type": "object",
          "properties": {
            "location": { "type": "string" }
          }
        }
      }
    }
  ]
}
```

The field is a tri-state. Omitting it defaults to buffered streaming, `true` enables eager streaming for that tool, and an explicit `false` also buffers but holds that even when the deprecated `fine-grained-tool-streaming-2025-05-14` beta header is present, which otherwise turns unset tools on.

How arguments are accumulated does not change: clients concatenate the `arguments` fragments exactly as before. What changes is the guarantee. With eager streaming the server no longer validates the fragments, so the accumulated string is not guaranteed to be valid JSON — a response ending with `finish_reason: "length"` can cut a parameter off midway. Guard the parse and handle failure rather than assuming it succeeds. See [Accumulating tool input deltas](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming#accumulating-tool-input-deltas).

### Field Conflicts

Vendor fields override translated fields when conflicts occur.

When using unified thinking configuration, the `thinking` field takes precedence over any provider-specific thinking configurations.

### Unsupported Fields/Backends

Fields and Backends other than specified in [Supported Backends](#supported-backends) will be ignored.
