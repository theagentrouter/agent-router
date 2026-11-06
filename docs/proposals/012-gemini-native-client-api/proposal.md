# Google Native Client API Support

- author: [Zixin](https://github.com/CodePrometheus)

## Table of Contents

<!-- toc -->

- [Summary](#summary)
- [Goals](#goals)
- [Background](#background)
- [Design](#design)
  - [API Surface](#api-surface)
  - [Endpoint Matching](#endpoint-matching)
  - [Model Routing](#model-routing)
  - [Forwarding](#forwarding)
  - [Request and Response Handling](#request-and-response-handling)
  - [Models API](#models-api)
- [Implementation](#implementation)
- [References](#references)

<!-- /toc -->

## Summary

This proposal adds provider-native Google REST passthrough entrypoints to Envoy
AI Gateway. Clients that already use the Gemini Developer API or Vertex AI REST
API can send the same Google-shaped requests through the gateway:

```http
GET  /gemini/v1beta/models
POST /gemini/v1beta/models/gemini-2.5-flash:generateContent

POST /vertex-ai/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
```

`/gemini/*` targets the Gemini Developer API on
`generativelanguage.googleapis.com`. `/vertex-ai/*` targets Vertex AI REST on
the host that corresponds to the selected backend's configured location:

- `global` location uses `https://aiplatform.googleapis.com`
- multi-region locations such as `us` and `eu` use
  `https://aiplatform.{location}.rep.googleapis.com`
- single-region locations such as `us-central1` use
  `https://{location}-aiplatform.googleapis.com`

The gateway preserves the provider path, request body, response body,
streaming format, and error body while still applying gateway authentication,
backend authentication, model routing where a model can be extracted, and
token usage metrics where usage fields are known.

## Goals

- Expose `/gemini/{endpoint:path}` as a Gemini Developer API passthrough
  surface.
- Expose `/vertex-ai/{endpoint:path}` as a Vertex AI REST passthrough surface.
- Preserve provider-native paths, request bodies, response bodies, streaming
  responses, and error envelopes.
- Treat the provider API as the source of truth for valid native paths and
  methods; gateway recognizers enhance routing and observability but do not
  define the complete provider method set.
- Extract model context from recognized Google native resource paths early
  enough to support model-based routing, backend selection, provider
  authentication, and usage metrics.
- Keep the OpenAI-compatible `/v1/*` API as a separate normalized lane.

## Background

AI Gateway already supports Gemini models through the normalized
OpenAI-compatible lane. A client sends `POST /v1/chat/completions` or
`POST /v1/embeddings` with the model in the OpenAI request body. When the
selected backend schema is `GCPVertexAI`, the endpoint spec selects the
OpenAI-to-Vertex translator, which rewrites the request to Vertex AI Gemini REST
and converts the provider response back to the OpenAI-compatible response shape.

That path is the right interface for OpenAI-compatible clients, and it works
with existing model routing because the model is available in the request body.
It does not support Google native clients. Gemini Developer API clients call
Google REST paths where the model resource is `models/{model}`:

```http
POST /v1beta/models/gemini-2.5-flash:generateContent
POST /v1beta/models/gemini-2.5-flash:streamGenerateContent
POST /v1beta/models/gemini-embedding-001:embedContent
```

Vertex AI REST clients call Google Cloud resource paths where the model is part
of the full resource name:

```http
POST /v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
```

These requests also expect Google request bodies, response bodies, streaming
format, error envelope, and models catalog semantics. Sending them through
`/v1/*` would force clients to leave the Google native API shape.

The native Google surfaces therefore need path-aware matching before HTTPRoute
selection, so model-scoped requests can participate in model routing without
changing the provider-native request body.

## Design

### API Surface

The gateway exposes two default Google native passthrough prefixes:

| Prefix                       | Provider API         | Upstream host                             |
|------------------------------|----------------------|-------------------------------------------|
| `/gemini/{endpoint:path}`    | Gemini Developer API | `generativelanguage.googleapis.com`       |
| `/vertex-ai/{endpoint:path}` | Vertex AI REST       | derived from backend location (see below) |

The gateway-level `rootPrefix` is applied before these prefixes. With
`rootPrefix=/ai`, the Gemini generation path becomes:

```http
POST /ai/gemini/v1beta/models/gemini-2.5-flash:generateContent
```

The prefix is the gateway contract. The path after the prefix is the provider
REST path, and the HTTP method is preserved. For example:

```text
/gemini/v1beta/models
  -> https://generativelanguage.googleapis.com/v1beta/models

/gemini/v1beta/models/gemini-2.5-flash:generateContent
  -> https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent

/vertex-ai/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
  -> https://us-central1-aiplatform.googleapis.com/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
```

The provider API defines which paths and methods are valid. The gateway does
not maintain a complete provider method list and does not treat recognized
operations as the public API surface. Operation-specific handling is added only
where the gateway needs model extraction, streaming handling, provider auth,
or usage extraction.

### Endpoint Matching

`extproc.Server` today resolves a processor by exact path lookup against a
single map. This proposal adds a second matcher tier for prefix-shaped native
paths and keeps both tiers behind one resolution API. Resolution order is:

1. Exact match against the existing registered paths. The OpenAI-compatible
   `/v1/*` lane, Cohere, Anthropic, and the synthesized `/v1/models` endpoint
   continue to register here unchanged.
2. Longest-prefix match against the registered native prefixes. Each native
   prefix owns a registered processor for its surface.

Lookup runs on the path with query parameters stripped, and the original path
including query is preserved for forwarding. A request whose path does not
match either tier returns the existing 404 response.

The native prefix matchers accept:

```text
/gemini/{endpoint:path}
/vertex-ai/{endpoint:path}
```

Within those prefixes, the gateway recognizes model context from common
model-bearing path shapes:

```text
/gemini/{version}/models
/gemini/{version}/models/{model}
/gemini/{version}/models/{model}:{operation}

/vertex-ai/{version}/projects/{project}/locations/{location}/publishers/google/models/{model}:{operation}
/vertex-ai/{version}/projects/{project}/locations/{location}/endpoints/{endpoint}:{operation}
```

When a recognized shape is present, the matcher records a small request context:

| Field            | Gemini                             | Vertex AI                                                         |
|------------------|------------------------------------|-------------------------------------------------------------------|
| provider surface | `gemini`                           | `vertex-ai`                                                       |
| API version      | from path                          | from path                                                         |
| model            | from `models/{model}` when present | from `publishers/google/models/{model}`; empty for endpoint paths |
| operation        | suffix after `:`                   | suffix after `:`                                                  |
| project          | empty                              | from path                                                         |
| location         | empty                              | from path                                                         |
| original path    | full gateway path                  | full gateway path                                                 |
| streaming        | `streamGenerateContent`            | `streamGenerateContent`                                           |

Vertex `endpoints/{endpoint}` paths address tuned or deployed models by
endpoint id rather than by publisher model name. The gateway recognizes them
as native Vertex paths so backend selection and provider auth still apply,
but it does not extract a model id from them. Such requests passthrough
without model-based routing and without `modelNameOverride` rewriting.

`streamGenerateContent` is treated as the streaming operation on both surfaces.
Both APIs return a stream of `GenerateContentResponse` chunks and accept an
optional `?alt=sse` query parameter to request Server-Sent Events. The
gateway preserves whatever `alt` value the client sent, including no value at
all, and the streaming usage extractor handles whichever wire format the
provider returns. The gateway does not append `alt=sse` on behalf of the
client. Other query parameters are preserved unless they contain client
credentials.

Paths that do not match a known model-bearing shape are still forwarded through
the native surface. They are routed by the provider-native route, not by
model-specific route matching, and they do not participate in provider-aware
usage extraction until gateway-side handling is added for that resource shape.

### Model Routing

Model-scoped Google native operations set `x-ai-eg-model` during request header
processing. The router filter also records the original path and clears Envoy's
route cache so HTTPRoute can match on the extracted model.

For example:

```http
POST /gemini/v1beta/models/gemini-2.5-flash:generateContent
```

sets:

```text
x-ai-eg-model: gemini-2.5-flash
x-ai-eg-original-path: /gemini/v1beta/models/gemini-2.5-flash:generateContent
```

and returns `ClearRouteCache=true`.

Vertex AI publisher model paths use the same model header:

```http
POST /vertex-ai/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
```

sets:

```text
x-ai-eg-model: gemini-2.5-flash
```

Provider-scoped requests such as `GET /gemini/v1beta/models` do not set
`x-ai-eg-model`. They are routed by the configured native surface route rather
than model-specific route matching.

If a backend reference configures `modelNameOverride`, the override replaces
the model id inside the same provider resource shape. On Google native paths
the model is in the URL, not in the request body, so the override is applied
by rewriting the model segment of the path before the request is sent
upstream. For example, an override of `gemini-2.5-pro` rewrites
`/gemini/v1beta/models/gemini-2.5-flash:generateContent` to
`/gemini/v1beta/models/gemini-2.5-pro:generateContent`, and rewrites the
publisher model segment of a Vertex AI path in the same way. The override
does not change a Gemini Developer API path into a Vertex AI path or the
reverse.

### Forwarding

#### Gemini Developer API

The `/gemini` surface uses a `GoogleAIStudio` API schema and Gemini API key
authentication:

```yaml
schema:
  name: GoogleAIStudio
  version: v1beta
```

The gateway strips only the gateway prefix and forwards the remaining path to
`generativelanguage.googleapis.com`:

```text
/gemini/v1beta/models/gemini-2.5-flash:generateContent
  -> https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent
```

Backend credentials are injected as `x-goog-api-key`. Any client-supplied
credentials on the inbound request — `x-goog-api-key` header, `Authorization`
header, or `key` query parameter — are removed before forwarding so the
upstream request always uses the gateway-managed credential. Client-supplied
credential values are redacted in logs and traces.

#### Vertex AI

The `/vertex-ai` surface uses the existing `GCPVertexAI` API schema and GCP
credentials:

```yaml
schema:
  name: GCPVertexAI
```

The path after `/vertex-ai` is already a Vertex AI REST path. The gateway
forwards it without rebuilding the Google Cloud resource name:

```text
/vertex-ai/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
  -> https://us-central1-aiplatform.googleapis.com/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent
```

The selected backend's GCP credentials must authorize the path. The project and
location in the native Vertex AI path must match the selected backend's GCP
auth configuration. This keeps project and region selection under gateway
configuration while still accepting Vertex-native resource paths from clients.

Vertex native requests already carry the full Google Cloud resource path. The
existing GCP auth handler used by the OpenAI-compatible lane prepends
`/v1/projects/{project}/locations/{region}` to the `:path` and assumes the
translator wrote only the resource suffix. The native forwarding path uses a
separate code branch in the GCP auth handler that injects `Authorization`
without prepending any prefix, since `:path` is already the complete
`/v1/projects/.../locations/.../publishers/google/models/...:operation` form.

If the project or location embedded in the native path does not match the
selected backend's GCP auth configuration, the request is rejected before it
reaches the upstream with a `400 INVALID_ARGUMENT` Google REST error envelope.
Selecting a backend by the location encoded in the request path is out of
scope for this proposal; project and region selection remain a gateway
configuration concern.

Any client-supplied `Authorization` header on `/vertex-ai` is overwritten
with the gateway-managed access token before forwarding, and the original
client value is redacted in logs and traces.

### Request and Response Handling

Google native processors parse only the path and response fields needed for
routing, forwarding, streaming handling, and usage extraction. Request bodies
are forwarded in the provider-native shape. The provider remains the source of
truth for request schema validation and returns the provider error when a path,
method, or body is invalid.

Gateway-side handling for recognized operations:

| Operation               | Gateway handling                                                                                |
|-------------------------|-------------------------------------------------------------------------------------------------|
| `generateContent`       | Extract model from path and read `usageMetadata` from the response                              |
| `streamGenerateContent` | Extract model from path and read streaming `usageMetadata` from either JSON-array or SSE chunks |
| `countTokens`           | Extract model from path and read token count fields from the response                           |
| `embedContent`          | Extract model from path and preserve the provider embedding response                            |
| `batchEmbedContents`    | Extract model from path and preserve the provider batch embedding response                      |
| `predict`               | Extract Vertex publisher model from path and read embedding token statistics when present       |

The upstream response body and provider error body are returned in the provider
shape. `streamGenerateContent` remains the provider streaming response. The
provider returns either a JSON stream of `GenerateContentResponse` chunks or
Server-Sent Events depending on the request, the API version, and provider
defaults. The gateway forwards whatever wire format the provider returned,
preserves the response Content-Type, and does not transcode between the two.
The streaming usage extractor reads the latest `usageMetadata` regardless of
which format is on the wire.

Gateway-generated errors use the Google REST error envelope on Google native
surfaces. The inbound endpoint prefix selects which envelope formatter to use
for gateway-emitted 4xx and 5xx responses, so requests under `/gemini` and
`/vertex-ai` receive Google-shaped errors while OpenAI-compatible requests
continue to receive OpenAI-shaped errors:

```json
{
  "error": {
    "code": 400,
    "message": "invalid request: missing contents",
    "status": "INVALID_ARGUMENT"
  }
}
```

Usage extraction reads Google response metadata without reshaping the response:

| Response source                                        | Usage fields                                                                                                                             |
|--------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------|
| Gemini and Vertex `generateContent`                    | `usageMetadata.promptTokenCount`, `usageMetadata.candidatesTokenCount`, `usageMetadata.totalTokenCount`, cache token fields when present |
| Gemini and Vertex streaming                            | latest `usageMetadata` observed in the stream                                                                                            |
| Gemini `countTokens`                                   | `totalTokens`, `cachedContentTokenCount`, `promptTokensDetails`, `cacheTokensDetails`                                                    |
| Vertex `countTokens`                                   | `totalTokens`, `totalBillableCharacters`, `promptTokensDetails`                                                                          |
| Gemini and Vertex `embedContent`, `batchEmbedContents` | `usageMetadata.promptTokenCount` when present                                                                                            |
| Vertex text embedding `predict`                        | `predictions[].embeddings.statistics.token_count` when present                                                                           |

### Models API

`GET /gemini/{version}/models` and `GET /gemini/{version}/models/{model}` are
forwarded to the Gemini Developer API models resource. The response is the
provider catalog returned by Google, including fields such as `name`,
`baseModelId`, `version`, `displayName`, and `supportedGenerationMethods` when
Google returns them.

The gateway does not synthesize these native models responses from route
configuration. Gateway-visible route and backend inventory remains a separate
management concern.

## Implementation

Public API and config changes:

- Add `GoogleAIStudio` to the API schema enum and filter config schema.
- Add a `GeminiAPIKey` backend auth type that injects `x-goog-api-key`.
- Add endpoint prefix config for `gemini` and `vertexai`:

  ```yaml
  endpointConfig:
    rootPrefix: "/"
    openai: ""
    cohere: "/cohere"
    anthropic: "/anthropic"
    gemini: "/gemini"
    vertexai: "/vertex-ai"
  ```

Data plane changes:

- Extend `extproc.Server` with a two-tier resolver: the existing exact-match
  map continues to register OpenAI, Cohere, Anthropic, and `/v1/models`
  processors, and a new ordered prefix list registers the Gemini and Vertex AI
  native processors. Resolution checks the exact map first and falls back to
  longest-prefix match.
- Register Gemini and Vertex AI native prefix processors under their endpoint
  prefixes. Each processor parses the model-bearing path shape and produces
  the request context table above.
- Pass Google native request context from the router filter to the upstream
  filter with internal headers.
- Set `x-ai-eg-model` and `ClearRouteCache=true` for model-scoped native
  operations.
- Apply `modelNameOverride` on native paths by rewriting the model segment of
  the outbound `:path` rather than the request body.
- Keep existing OpenAI-compatible request translation unchanged on the `/v1/*`
  paths.

Forwarding and auth changes:

- Add a Gemini Developer API passthrough forwarder for `GoogleAIStudio` and
  the `GeminiAPIKey` backend auth handler.
- Add a Vertex AI native forwarder for `GCPVertexAI` paths under `/vertex-ai`.
- Branch the existing GCP auth handler on whether the inbound `:path` is
  already a complete Vertex resource path: the OpenAI lane keeps the
  prefix-prepending behavior, and the native lane only injects `Authorization`.
- Validate that the project and location embedded in a native Vertex AI path
  match the selected backend's GCP auth configuration, returning a
  `400 INVALID_ARGUMENT` Google REST error envelope on mismatch.
- Select the gateway error envelope formatter from the inbound endpoint
  prefix so gateway-emitted errors on `/gemini` and `/vertex-ai` use the
  Google REST envelope.
- Redact `x-goog-api-key` and credential query parameters from logs.

Observability changes:

- Add provider labels for Google AI Studio and Vertex AI native surfaces.
- Extract token usage from Google native response fields.
- Preserve `OriginalModel`, `RequestModel`, and `ResponseModel` semantics for
  native requests where those fields are available.

## References

- Gemini API reference: https://ai.google.dev/api
- Gemini API versions: https://ai.google.dev/gemini-api/docs/api-versions
- Gemini GenerateContent API: https://ai.google.dev/api/generate-content
- Gemini models API: https://ai.google.dev/api/models
- Gemini tokens API: https://ai.google.dev/api/tokens
- Gemini embeddings API: https://ai.google.dev/api/embeddings
- Vertex AI REST reference: https://cloud.google.com/vertex-ai/generative-ai/docs/reference/rest
- Vertex AI GenerateContent API: https://cloud.google.com/vertex-ai/generative-ai/docs/reference/rest/v1/projects.locations.publishers.models/generateContent
- Vertex AI text embeddings API: https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/text-embeddings-api
- Google API errors: https://cloud.google.com/apis/design/errors
