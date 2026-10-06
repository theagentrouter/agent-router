---
id: prompt-caching
title: Prompt Caching
sidebar_position: 6
---

# Prompt Caching

Agent Router provides provider-agnostic prompt caching through a unified `cache_control` API. The same cache syntax works across multiple providers: Direct Anthropic, GCP Vertex AI (Claude models), and AWS Bedrock (Claude models). This reduces costs and improves response times by caching frequently-used content like system prompts, tool definitions, and reference documents.

## Supported Providers

| Provider               | API Schema            | Cache Support |
| ---------------------- | --------------------- | ------------- |
| Anthropic (Direct)     | `Anthropic`           | Native        |
| GCP Vertex AI (Claude) | `GCPAnthropic`        | Translated    |
| AWS Bedrock (Claude)   | `AWSBedrockAnthropic` | Translated    |

## How It Works

- Add `cache_control: {"type": "ephemeral"}` to content blocks in your request.
- AI Gateway translates this to the provider-specific format automatically.
- Cache is maintained per-provider; all providers require a minimum of 1,024 tokens for caching.
- A maximum of 4 cache breakpoints are allowed per request across all providers.
- On cache hit, the provider charges reduced input token costs.

## Usage

No gateway-side configuration is needed. Caching is controlled entirely at the request level by adding `cache_control` to the content blocks you want cached.

### Basic Example: System Prompt Caching

Cache a system prompt so that subsequent requests reuse the cached content:

```json
{
  "model": "claude-sonnet-4-5",
  "messages": [
    {
      "role": "system",
      "content": [
        {
          "type": "text",
          "text": "You are a helpful assistant with extensive knowledge...(long system prompt)...",
          "cache_control": { "type": "ephemeral" }
        }
      ]
    },
    {
      "role": "user",
      "content": "What is the capital of France?"
    }
  ]
}
```

### Multiple Cache Points

You can place up to 4 cache breakpoints in a single request to cache different parts of the conversation:

```json
{
  "model": "claude-sonnet-4-5",
  "messages": [
    {
      "role": "system",
      "content": [
        {
          "type": "text",
          "text": "System instructions...",
          "cache_control": { "type": "ephemeral" }
        }
      ]
    },
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "Reference document content...",
          "cache_control": { "type": "ephemeral" }
        },
        {
          "type": "text",
          "text": "Question about the document"
        }
      ]
    }
  ]
}
```

### Tool Definition Caching

Cache complex tool schemas that remain the same across requests:

```json
{
  "model": "claude-sonnet-4-5",
  "messages": [
    {
      "role": "user",
      "content": "Help me search for information about cloud computing trends."
    }
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "search_knowledge_base",
        "description": "Search through a comprehensive knowledge base...",
        "parameters": {
          "type": "object",
          "properties": {
            "query": {
              "type": "string",
              "description": "Natural language search query"
            }
          },
          "required": ["query"]
        },
        "cache_control": { "type": "ephemeral" }
      }
    }
  ]
}
```

## Response Format

When caching is active, the response includes cache information in the `usage` field:

```json
{
  "usage": {
    "prompt_tokens": 2000,
    "completion_tokens": 150,
    "prompt_tokens_details": {
      "cached_tokens": 1800
    }
  }
}
```

- `cached_tokens` indicates the number of tokens served from cache at a reduced cost.
- Cache write tokens are tracked internally for billing purposes.

## Best Practices

:::tip

- Place `cache_control` on content that exceeds 1,024 tokens. Content below this threshold will not be cached.
- Cache system prompts, tool definitions, and reference documents that do not change between requests.
- Position cache breakpoints strategically -- cached content must appear at the beginning of the message.
- Monitor `cached_tokens` in responses to verify caching effectiveness and measure cost savings.
  :::

## Provider-Specific Notes

:::note

- **All providers**: Minimum 1,024 tokens per cached block, maximum 4 cache breakpoints per request.
- **Anthropic Direct**: Uses the native `cache_control` field directly with no translation.
- **GCP Vertex AI**: AI Gateway translates `cache_control` to Vertex AI's caching format automatically.
- **AWS Bedrock**: AI Gateway translates `cache_control` to Bedrock's cachePoint format automatically.
- All providers support the `"ephemeral"` cache type.
- Existing requests without `cache_control` continue to work with no changes.
  :::

## Further Reading

- [Prompt Caching Examples](https://github.com/theagentrouter/agent-router/tree/main/examples/cache) -- Detailed examples with curl commands for each provider.
- [Connecting to GCP Vertex AI](../../getting-started/connect-providers/gcp-vertexai.md) -- Set up GCP Vertex AI as a provider.
- [Connecting to AWS Bedrock](../../getting-started/connect-providers/aws-bedrock.md) -- Set up AWS Bedrock as a provider.
