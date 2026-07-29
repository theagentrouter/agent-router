---
id: supported-endpoints
title: Supported API Endpoints
sidebar_position: 9
---

The Envoy AI Gateway provides OpenAI-compatible API endpoints as well as the Anthropic-compatible API for routing and managing LLM/AI traffic. This page documents which OpenAI API endpoints and Anthropic-compatible API endpoints are currently supported and their capabilities.

## Overview

The Envoy AI Gateway acts as a proxy that accepts OpenAI-compatible and Anthropic-compatible requests and routes them to various AI providers. While it maintains compatibility with the OpenAI API specification, it currently supports a subset of the full OpenAI API.

## Supported Endpoints

### Chat Completions

**Endpoint:** `POST /v1/chat/completions`

**Status:** ✅ Fully Supported

**Description:** Create a chat completion response for the given conversation.

**Features:**

- ✅ Streaming and non-streaming responses
- ✅ Function calling
- ✅ Response format specification (including JSON schema)
- ✅ Temperature, top_p, and other sampling parameters
- ✅ System and user messages
- ✅ Audio and video inputs
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- AWS Bedrock (with automatic translation)
- Azure OpenAI (with automatic translation)
- GCP VertexAI (with automatic translation)
- GCP Anthropic (with automatic translation)
- Any OpenAI-compatible provider (Groq, Together AI, Mistral, Tetrate Agent Router Service, etc.)

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ]
  }' \
  $GATEWAY_URL/v1/chat/completions
```

### Anthropic Messages

**Endpoint:** `POST /anthropic/v1/messages`

**Status:** ✅ Fully Supported

**Description:** Send a structured list of input messages with text and/or image content, and the model will generate the next message in the conversation.

**Features:**

- ✅ Streaming and non-streaming responses
- ✅ Function calling
- ✅ Extended thinking
- ✅ Response format specification (including JSON schema)
- ✅ Temperature, top_p, and other sampling parameters
- ✅ System and user messages
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- Anthropic
- GCP Anthropic
- AWS Anthropic
- AWS Bedrock

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ],
    "max_tokens": 100
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

### Completions

**Endpoint:** `POST /v1/completions`

**Status:** ✅ Fully Supported

**Description:** Create a text completion for the given prompt (legacy endpoint).

**Features:**

- ✅ Non-streaming responses
- ✅ Streaming responses
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Temperature, top_p, and other sampling parameters
- ✅ Single and batch prompt processing
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing
- ✅ Full metrics support (token usage, request duration, time to first token, inter-token latency)

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports completions

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "babbage-002",
    "prompt": "def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)",
    "max_tokens": 25,
    "temperature": 0.4,
    "top_p": 0.9
  }' \
  $GATEWAY_URL/v1/completions
```

### Embeddings

**Endpoint:** `POST /v1/embeddings`

**Status:** ✅ Fully Supported

**Description:** Create embeddings for the given input text.

**Features:**

- ✅ Single and batch text embedding
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- AWS Bedrock (Titan models, with automatic translation)
- GCP VertexAI (with automatic translation)
- Any OpenAI-compatible provider that supports embeddings, including Azure OpenAI.

### Image Generation

**Endpoint:** `POST /v1/images/generations`

**Status:** ✅ Supported

**Description:** Generate one or more images from a text prompt using OpenAI-compatible models.

**Features:**

- **Non-streaming responses**: Returns JSON payload with image URLs or base64 content
- **Model selection**: Via request body `model` or `x-ai-eg-model` header
- **Parameters**: `prompt`, `size`, `n`, `quality`, `response_format`
- **Metrics**: Records image count, model, and size; token usage when provided
- **Provider fallback and load balancing**

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports image generations

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-1",
    "prompt": "a serene mountain landscape at sunrise in watercolor",
    "size": "1024x1024",
    "n": 1
  }' \
  $GATEWAY_URL/v1/images/generations
```

### Audio Transcriptions

**Endpoint:** `POST /v1/audio/transcriptions`

**Status:** ✅ Supported

**Description:** Transcribe audio into text in the language of the audio.

**Features:**

- ✅ Multipart/form-data file upload (OpenAI-compatible)
- ✅ Model selection via form field `model` or `x-ai-eg-model` header
- ✅ Optional parameters: `language`, `prompt`, `response_format`, `temperature`, `timestamp_granularities[]`
- ✅ JSON and verbose JSON response formats
- ✅ Provider fallback and load balancing
- ✅ Model name virtualization (override model names for backends)

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports audio transcriptions

**Example:**

```bash
curl -F "model=whisper-1" \
  -F "file=@audio.mp3" \
  -F "language=en" \
  $GATEWAY_URL/v1/audio/transcriptions
```

### Audio Translations

**Endpoint:** `POST /v1/audio/translations`

**Status:** ✅ Supported

**Description:** Translate audio into English text.

**Features:**

- ✅ Multipart/form-data file upload (OpenAI-compatible)
- ✅ Model selection via form field `model` or `x-ai-eg-model` header
- ✅ Optional parameters: `prompt`, `response_format`, `temperature`
- ✅ Provider fallback and load balancing
- ✅ Model name virtualization (override model names for backends)

**Supported Providers:**

- OpenAI
- Any OpenAI-compatible provider that supports audio translations

**Example:**

```bash
curl -F "model=whisper-1" \
  -F "file=@audio.mp3" \
  $GATEWAY_URL/v1/audio/translations
```

### Responses

**Endpoint:** `POST /v1/responses`

**Status:** ✅ Fully Supported

**Description:** Creates a model response. Provide text or image inputs to generate text or JSON outputs. Have the model call your own custom code or use built-in tools.

**Features:**

- ✅ Streaming and non-streaming responses
- ✅ Function calling
- ✅ MCP Tools support
- ✅ Reasoning
- ✅ Multi-turn conversations
- ✅ Native multimodal support for text and images
- ✅ Response format specification (including JSON schema)
- ✅ Temperature, top_p, and other sampling parameters
- ✅ System and user messages
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- OpenAI
- Azure OpenAI with an API version that supports Responses, such as `2025-04-01-preview`
- Any OpenAI-compatible provider (Groq, Together AI, Mistral, Tetrate Agent Router Service, etc.)

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "what is in this image?"},
          {
            "type": "input_image",
            "image_url": "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"
          }
        ]
      }
    ]
  }' \
  $GATEWAY_URL/v1/responses
```

### Rerank

**Endpoint:** `POST /cohere/v2/rerank`

**Status:** ✅ Fully Supported

**Description:** Rerank a list of documents for a given query to return relevance scores and an ordered list. Cohere-compatible API.

**Features:**

- ✅ Single-query document reranking
- ✅ Model selection via request body or `x-ai-eg-model` header
- ✅ Token usage tracking and cost calculation
- ✅ Provider fallback and load balancing

**Supported Providers:**

- Cohere
- Any Cohere-compatible provider that supports rerank, including vLLM.

**Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "rerank-english-v3.0",
    "query": "What is the capital of France?",
    "documents": [
      "Paris is the capital of France.",
      "Berlin is the capital of Germany."
    ]
  }' \
  $GATEWAY_URL/cohere/v2/rerank
```

### Tokenize

**Endpoint:** `POST /tokenize`

**Status:** ✅ Supported

**Description:** Count tokens for text input without generating a response. Useful for cost estimation, prompt optimization, and understanding model input limits. The request format is compatible with the [vLLM tokenize API](https://docs.vllm.ai/en/latest/api/tokenization.html).

**Features:**

- ✅ Chat message tokenization (OpenAI messages format)
- ✅ Completion prompt tokenization (single string prompt)
- ✅ Model selection via `model` field in request body
- ✅ Tool/function call tokenization support
- ✅ Provider fallback and load balancing
- ✅ Metrics support (request duration)

**Supported Providers:**

| Provider                            | API Schema     | Translation Target                                                                                                           | Notes                                                                                 |
| ----------------------------------- | -------------- | ---------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| OpenAI-compatible (e.g., vLLM)      | `OpenAI`       | Passthrough                                                                                                                  | vLLM natively supports `/tokenize`. OpenAI itself does not offer a tokenize REST API. |
| GCP Vertex AI (Gemini)              | `GCPVertexAI`  | [Gemini CountTokens API](https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/count-tokens)                 | Supports `media_resolution` parameter.                                                |
| GCP Anthropic (Claude on Vertex AI) | `GCPAnthropic` | [Anthropic MessageCountTokens API](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/count-tokens) | Uses `rawPredict` method with `count-tokens` virtual model.                           |
| AWS Bedrock (Anthropic)             | `AWSAnthropic` | [AWS Bedrock CountTokens API](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html)          | Uses the InvokeModel-style CountTokens API with the Anthropic Messages body.          |

**Chat Message Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.1-8B-Instruct",
    "messages": [
      {
        "role": "system",
        "content": "You are a helpful assistant."
      },
      {
        "role": "user",
        "content": "How many tokens is this message?"
      }
    ]
  }' \
  $GATEWAY_URL/tokenize
```

**Completion Prompt Example:**

```bash
curl -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.1-8B-Instruct",
    "prompt": "Once upon a time, in a land far away"
  }' \
  $GATEWAY_URL/tokenize
```

**Response Format:**

For translated backends (GCP Vertex AI, GCP Anthropic, AWS Bedrock Anthropic), the response contains only the token count:

```json
{
  "count": 15
}
```

For OpenAI-compatible backends that natively support tokenization (e.g., vLLM), the response contains the token count and additional fields may be present depending on request parameters:

```json
{
  "count": 15,
  "max_model_len": 131072,
  "tokens": [1234, 5678, 9012],
  "token_strs": ["Hello", " world", "!"]
}
```

**Configuration Notes:**

- For **vLLM backends**: Configure with `OpenAI` schema. vLLM natively provides `/tokenize` and the gateway passes the request through.
- For **GCP Vertex AI**: Configure with `GCPVertexAI` schema. Requests are automatically translated to the Gemini CountTokens API. Completion prompts are automatically converted to chat messages.
- For **GCP Anthropic**: Configure with `GCPAnthropic` schema. Requests are translated to the Anthropic MessageCountTokens API via `rawPredict`. Completion prompts are automatically converted to chat messages. Model version suffixes (`@default`, `@latest`) are automatically stripped.
- For **AWS Bedrock (Anthropic)**: Configure with `AWSAnthropic` schema. Requests are translated to the AWS Bedrock CountTokens API using the InvokeModel-style Anthropic Messages body. Completion prompts are automatically converted to chat messages. Cross-region inference (CRIS) model ID prefixes are automatically stripped.

### Models

**Endpoint:** `GET /v1/models`

**Description:** List available models configured in the AI Gateway.

**Features:**

- ✅ Returns models declared in AIGatewayRoute configurations
- ✅ OpenAI-compatible response format
- ✅ Model metadata (ID, owned_by, created timestamp)
- ✅ Hostname scoping when routes declare `spec.hostnames` (see v0.7 release notes)
- ✅ Header scoping when rules use exact-match headers other than `x-ai-eg-model` (e.g. `x-jwt-sub` for JWT tenant routing)

When multiple `AIGatewayRoute` rules on a shared hostname match on different exact headers, `GET /v1/models` returns only models from rules whose scope headers match the incoming request. Rules without scope headers remain visible to all requests on that host. Header scoping composes with hostname scoping.

Scope headers must be present on the request when the external processor runs (for example via JWT `claimToHeaders` with `recomputeRoute: true`, the same pattern used for chat routing).

**Example:**

```bash
curl $GATEWAY_URL/v1/models
```

**Response Format:**

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4o-mini",
      "object": "model",
      "created": 1677610602,
      "owned_by": "openai"
    }
  ]
}
```

## Provider-Endpoint Compatibility Table

The following table summarizes which providers support which endpoints:

| Provider                                                                                              | Chat Completions | Completions | Embeddings | Image Generation | Anthropic Messages | Rerank | Tokenize | Notes                                                                                                                |
| ----------------------------------------------------------------------------------------------------- | :--------------: | :---------: | :--------: | :--------------: | :----------------: | :----: | :------: | -------------------------------------------------------------------------------------------------------------------- |
| [OpenAI](https://platform.openai.com/docs/api-reference)                                              |        ✅        |     ✅      |     ✅     |        ❌        |         ✅         |   ❌   |    ❌    | OpenAI does not offer a tokenize REST API                                                                            |
| [AWS Bedrock](https://docs.aws.amazon.com/bedrock/latest/APIReference/)                               |        ✅        |     🚧      |     ✅     |        ❌        |         ❌         |   ❌   |    ❌    | Via API translation (embeddings: Titan models only)                                                                  |
| [Azure OpenAI](https://learn.microsoft.com/en-us/azure/ai-services/openai/reference)                  |        ✅        |     🚧      |     ✅     |        ❌        |         ⚠️         |   ❌   |    ❌    | Via API translation or via [OpenAI-compatible API](https://learn.microsoft.com/en-us/azure/ai-foundry/openai/latest) |
| [Google Gemini](https://ai.google.dev/gemini-api/docs/openai)                                         |        ✅        |     ⚠️      |     ✅     |        ⚠️        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Groq](https://console.groq.com/docs/openai)                                                          |        ✅        |     ❌      |     ❌     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Grok](https://docs.x.ai/docs/api-reference)                                                          |        ✅        |     ⚠️      |     ❌     |        ⚠️        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Together AI](https://docs.together.ai/docs/openai-api-compatibility)                                 |        ⚠️        |     ⚠️      |     ⚠️     |        ⚠️        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Cohere](https://docs.cohere.com/v2/docs/compatibility-api)                                           |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |   ✅   |    ❌    | Via OpenAI-compatible API and Cohere V2 API for rerank                                                               |
| [Mistral](https://docs.mistral.ai/api/)                                                               |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [DeepInfra](https://deepinfra.com/docs/inference)                                                     |        ✅        |     ⚠️      |     ✅     |        ⚠️        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [DeepSeek](https://api-docs.deepseek.com/)                                                            |        ⚠️        |     ⚠️      |     ❌     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Hunyuan](https://cloud.tencent.com/document/product/1729/111007)                                     |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Tencent LLM Knowledge Engine](https://www.tencentcloud.com/document/product/1255/70381)              |        ⚠️        |     ❌      |     ❌     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Tetrate Agent Router Service (TARS)](https://router.tetrate.ai/)                                     |        ⚠️        |     ⚠️      |     ⚠️     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Google Vertex AI](https://cloud.google.com/vertex-ai/docs/reference/rest)                            |        ✅        |     🚧      |     ✅     |        ❌        |         ❌         |   ❌   |    ✅    | Via API translation                                                                                                  |
| [Anthropic on Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude) |        ✅        |     ❌      |     🚧     |        ❌        |         ✅         |   ❌   |    ✅    | Via API translation                                                                                                  |
| [Anthropic on AWS Bedrock](https://aws.amazon.com/bedrock/anthropic/)                                 |        🚧        |     ❌      |     ❌     |        ❌        |         ✅         |   ❌   |    ✅    | Native Anthropic API                                                                                                 |
| [SambaNova](https://docs.sambanova.ai/sambastudio/latest/open-ai-api.html)                            |        ✅        |     ⚠️      |     ✅     |        ❌        |         ❌         |   ❌   |    ❌    | Via OpenAI-compatible API                                                                                            |
| [Anthropic](https://docs.claude.com/en/home)                                                          |        ✅        |     ❌      |     ❌     |        ❌        |         ✅         |   ❌   |    ❌    | Via OpenAI-compatible API and Native Anthropic API                                                                   |
| [vLLM](https://docs.vllm.ai/en/latest/)                                                               |        ✅        |     ✅      |     ✅     |        ❌        |         ❌         |   ❌   |    ✅    | Via OpenAI-compatible API; native `/tokenize` support                                                                |

- ✅ - Supported and Tested on Envoy AI Gateway CI
- ⚠️️ - Expected to work based on provider documentation, but not tested on the CI.
- ❌ - Not supported according to provider documentation.
- 🚧 - Unimplemented, or under active development but planned for future releases

## Custom endpoint prefixes

By default, the gateway registers provider endpoints under these prefixes:

- OpenAI: `/`
- Cohere: `/cohere`
- Anthropic: `/anthropic`

You can override them via Helm using values under `endpointConfig`:

```yaml
# values.yaml
endpointConfig:
  # Explicit provider roots
  openai: ""
  cohere: "/cohere"
  anthropic: "/anthropic"
  # rootPrefix applies to all routes; final paths are <rootPrefix><providerPrefix>/...
  # endpointConfig:
  #   rootPrefix: "/"
```

Or with helm CLI:

```bash
helm upgrade --install ai-gateway envoyproxy/ai-gateway-helm \
  -n envoy-ai-gateway-system --create-namespace \
  --set 'endpointConfig.openai=/' \
  --set 'endpointConfig.cohere=/cohere' \
  --set 'endpointConfig.anthropic=/anthropic'
```

Notes:

- `endpointConfig.rootPrefix` (default `/`) is prepended to all provider prefixes.
- Only these keys are accepted: `openaiPrefix`, `coherePrefix`, `anthropicPrefix`.
- If any key is omitted or empty, defaults are applied as listed above.

## What's Next

To learn more about configuring and using the Envoy AI Gateway with these endpoints:

- **[Supported Providers](./supported-providers.md)** - Complete list of supported AI providers and their configurations
- **[Usage-Based Rate Limiting](../traffic/usage-based-ratelimiting.md)** - Configure token-based rate limiting and cost controls
- **[Provider Fallback](../traffic/provider-fallback.md)** - Set up automatic failover between providers for high availability
- **[Metrics and Monitoring](../observability/metrics.md)** - Monitor usage, costs, and performance metrics

[issue#609]: https://github.com/envoyproxy/ai-gateway/issues/609
