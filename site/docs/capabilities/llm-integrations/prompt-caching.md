---
id: prompt-caching
title: Prompt Caching
sidebar_position: 6
---

# Prompt Caching

Agent Router provides provider-agnostic prompt caching through a unified `cache_control` API. The same cache syntax works across multiple providers: Direct Anthropic, GCP Vertex AI (Claude and Gemini models), and AWS Bedrock (Claude models). This reduces costs and improves response times by caching frequently-used content like system prompts, tool definitions, and reference documents.

## Supported Providers

| Provider               | API Schema            | Cache Support                                                                   |
| ---------------------- | --------------------- | ------------------------------------------------------------------------------- |
| Anthropic (Direct)     | `Anthropic`           | Native                                                                          |
| GCP Vertex AI (Claude) | `GCPAnthropic`        | Translated                                                                      |
| AWS Bedrock (Claude)   | `AWSBedrockAnthropic` | Translated                                                                      |
| GCP Vertex AI (Gemini) | `GCPVertexAI`         | Gateway-managed, needs Redis; see [GCP Vertex AI Gemini](#gcp-vertex-ai-gemini) |

## How It Works

- Add `cache_control: {"type": "ephemeral"}` to content blocks in your request.
- AI Gateway translates this to the provider-specific format automatically.
- Cache is maintained per-provider. Claude models require a minimum of 1,024 tokens for caching.
- Claude models allow a maximum of 4 cache breakpoints per request. Gemini uses only the last one.
- On cache hit, the provider charges reduced input token costs.

## Usage

Caching is controlled at the request level by adding `cache_control` to the content blocks you want cached. For the Claude providers, no gateway-side configuration is needed. GCP Vertex AI Gemini models accept the same markers, but caching for them is managed by the gateway and needs a Redis store; see [GCP Vertex AI Gemini](#gcp-vertex-ai-gemini).

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

- Place `cache_control` on content that exceeds the provider's minimum: 1,024 tokens for Claude, and a model-specific minimum for Gemini. Content below the minimum is not cached.
- Cache system prompts, tool definitions, and reference documents that do not change between requests.
- Position cache breakpoints strategically -- cached content must appear at the beginning of the message.
- Monitor `cached_tokens` in responses to verify caching effectiveness and measure cost savings.
  :::

## Provider-Specific Notes

:::note

- **Claude providers**: Minimum 1,024 tokens per cached block, maximum 4 cache breakpoints per request.
- **Anthropic Direct**: Uses the native `cache_control` field directly with no translation.
- **GCP Vertex AI (Claude)**: AI Gateway translates `cache_control` to Vertex AI's caching format automatically.
- **GCP Vertex AI (Gemini)**: The gateway creates and reuses Vertex AI cached contents; the minimum size depends on the model. See [GCP Vertex AI Gemini](#gcp-vertex-ai-gemini).
- **AWS Bedrock**: AI Gateway translates `cache_control` to Bedrock's cachePoint format automatically.
- All providers support the `"ephemeral"` cache type.
- Existing requests without `cache_control` continue to work with no changes.
  :::

## GCP Vertex AI Gemini

Agent Router can cache a prefix of a conversation for **GCP Vertex AI Gemini** backends, using
the same Anthropic-style `cache_control` markers you use for Claude. Requests that share the
cached prefix send only the new messages to Gemini, and the cached tokens are billed at the
reduced cache rate.

Unlike Claude, Gemini does not cache implicitly from markers. A Vertex AI cache is a separate
resource that has to be created, named, and then referenced on each request. The gateway does
this for you and records which cache each prefix maps to in a shared Redis store, so every
gateway replica reuses a cache created by any of them.

### How it works

1. The gateway finds the **last** message with a `cache_control` marker. Everything up to and
   including that message is the cached prefix, together with the request's tools and system
   instructions.
2. It computes a key for the prefix and looks it up in Redis.
3. **Hit:** it sends Gemini only the messages after the prefix, with a reference to the
   existing cache.
4. **Miss:** it creates a Vertex AI `cachedContents` entry for the prefix, stores its name in
   Redis, and sends the request as on a hit.

Requests without `cache_control` markers are not affected.

### Requirements

- A GCP Vertex AI backend set up as described in [Connect GCP Vertex AI](../../getting-started/connect-providers/gcp-vertexai.md),
  using the `GCPVertexAI` schema.
- GCP credentials with permission to create and list cached contents
  (`aiplatform.cachedContents.create` and `aiplatform.cachedContents.list`). The
  `roles/aiplatform.user` role includes both.
- A Redis-compatible server reachable from the gateway's extproc pods.

### Step 1: Run Redis

Any Redis-compatible server works. For example, in the cluster:

```shell
helm install redis oci://registry-1.docker.io/bitnamicharts/redis \
  -n redis-system --create-namespace \
  --set auth.enabled=false --set architecture=standalone
```

This creates the service `redis-master.redis-system.svc.cluster.local:6379`.

### Step 2: Enable context caching

Set the store URL in the Agent Router Helm values. Setting it turns on context caching for
**every** backend with the `GCPVertexAI` schema; leaving it empty turns it off.

```yaml
extProc:
  contextCache:
    # host:port, or a redis:// URL such as redis://user:password@host:6379/0
    storeURL: redis-master.redis-system.svc.cluster.local:6379
    # TTL used when a cache_control marker does not set one.
    # Whole seconds, at least 60s. Default 300s.
    defaultTTL: 300s
```

Apply it with `helm upgrade`. The gateway pods restart once to pick up the new settings.

:::warning
The store URL is passed to the extproc container as a command-line argument. If it contains a
password, the password is visible in the pod spec.
:::

### Step 3: Mark the prefix to cache

Add `cache_control` to the content you want cached. The prefix must be at least the model's
minimum cache size; see Google's
[context caching overview](https://cloud.google.com/vertex-ai/generative-ai/docs/context-cache/context-cache-overview)
for the minimum for each model.

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {
        "role": "system",
        "content": [
          {
            "type": "text",
            "text": "You are a legal assistant. <long reference document>",
            "cache_control": {"type": "ephemeral", "ttl": "1h"}
          }
        ]
      },
      {"role": "user", "content": "Summarize section 3."}
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

Send the same request again with a different question. The second request reuses the cache.

#### Where markers are recognized

- On content parts of **system** messages whose content is a list of parts.
- On **user** message text, image and audio parts.
- On **tool** messages.

Markers on assistant messages, or on a system message whose content is a plain string, are
ignored.

#### Cache lifetime

| `ttl` on the marker | Cache lifetime                    |
| ------------------- | --------------------------------- |
| `5m`                | 300 seconds                       |
| `1h`                | 3600 seconds                      |
| not set             | `extProc.contextCache.defaultTTL` |

### Step 4: Verify

- **Response:** on a hit, `usage.prompt_tokens_details.cached_tokens` shows the number of
  tokens served from the cache.
- **Metrics:** tokens written to a new cache are recorded in `gen_ai.client.token.usage` with
  `gen_ai.token.type` = `cache_creation_input`. See [Metrics](../observability/metrics.md).
- **Logs:** with the extproc at debug log level, look for `gcpcache: cache miss, creating`,
  `gcpcache: cache created`, and `gcpcache: cache hit`.

### Behavior and limits

- **Only the last marker counts.** Unlike Claude, which supports up to 4 breakpoints, the
  gateway creates one cache per request from the start of the conversation up to the last
  marked message.
- **Tools and system instructions are part of the cache.** On a cached request they are not
  sent again, so changing them produces a different cache.
- **Concurrent first requests may each create a cache.** If several replicas see a new prefix
  at the same moment, each may create its own cache, and each create is billed. Afterwards all
  replicas reuse whichever one Redis holds.
- **Caches are scoped to the GCP project and region** of the backend.

#### When caching fails

Caching is an optimization, so most failures fall back to sending the full request to Gemini
without a cache. The request still succeeds; it is just not cached.

| Failure                                                                                                 | What happens                                                                                                                                               |
| ------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Redis unreachable, or a read or write fails                                                             | Served uncached.                                                                                                                                           |
| Creating the cache returns HTTP 400, usually because the prefix is below the model's minimum cache size | Served uncached. The prefix is not tried again for one `defaultTTL`, so later requests skip the create.                                                    |
| Creating the cache returns 429, a 5xx error, or a network error                                         | Served uncached. The next request tries again.                                                                                                             |
| Creating the cache returns 401, 403 or 404 (credentials, permission, project, model or region)          | **The request fails** with a `502` that carries GCP's status and message. These are setup problems, so they are reported instead of silently costing more. |

## Further Reading

- [Prompt Caching Examples](https://github.com/theagentrouter/agent-router/tree/main/examples/cache) -- Detailed examples with curl commands for each provider.
- [Connecting to GCP Vertex AI](../../getting-started/connect-providers/gcp-vertexai.md) -- Set up GCP Vertex AI as a provider.
- [Connecting to AWS Bedrock](../../getting-started/connect-providers/aws-bedrock.md) -- Set up AWS Bedrock as a provider.
