---
id: files-api
title: Files API
sidebar_position: 11
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

The Envoy AI Gateway supports the OpenAI-compatible **Files API** (`/v1/files`), letting clients
upload, list, retrieve, download, and delete files through the gateway. Files are commonly used as
inputs to other workflows such as the [Batch API](./batch-api.md) and fine-tuning.

## Overview

Unlike chat completions or embeddings — which are stateless and routed per request — a **file is a
stateful object that lives on one specific upstream backend**. A file uploaded to backend A does not
exist on backend B, so every request that carries a file id (retrieve, download content, delete)
must reach **the backend that created the file**.

The gateway does this **without any server-side database**. When a file is created, the gateway
encrypts the serving backend's identity and the routing model into the opaque file id it returns to
the client. On later requests it decodes the id, routes by the recorded model, and verifies that the
request reached the backend that owns the file.

:::warning One backend per route rule

The Files API currently supports **exactly one backend** per `AIGatewayRoute` rule. A Files request
whose matching rule has more than one `backendRef` is rejected. Multi-backend routing may be added in
the future; gateway-issued file ids already carry the owning backend, so existing ids will remain
valid.

:::

:::info

Client-visible file ids (`file-...`) are **not** the provider's native ids. The gateway issues its
own encrypted, tamper-resistant ids and transparently translates between the two. Always use the id
the gateway returns; do not attempt to construct or reuse a provider-native id directly.

:::

## Supported Endpoints

| Operation | Method &amp; Path                 | Routed by                          |
| --------- | --------------------------------- | ---------------------------------- |
| Upload    | `POST /v1/files`                  | `model` form field (**required**)  |
| List      | `GET /v1/files`                   | `model` query param (**required**) |
| Retrieve  | `GET /v1/files/{file_id}`         | model recorded in the file id      |
| Content   | `GET /v1/files/{file_id}/content` | model recorded in the file id      |
| Delete    | `DELETE /v1/files/{file_id}`      | model recorded in the file id      |

:::warning Provider support

**OpenAI (and OpenAI-compatible providers) is the only backend schema supported today.** A Files
route pointed at a non-OpenAI schema (AWS Bedrock, GCP Vertex AI, Azure OpenAI, Anthropic, Cohere)
fails closed with an error. Support for other providers will be added in the future.

:::

## The `model` parameter is required

Because a file has no model of its own, the gateway uses a `model` value **only to select which
route/backend serves the request**. It is required for the two operations that do not already carry a
file id:

- **Upload** — supply `model` as a multipart form field (the OpenAI SDK sends it via `extra_body`).
  The gateway uses it to route the upload, then strips it from the body before forwarding upstream
  (the OpenAI Files API has no `model` field).
- **List** — supply `model` as a `?model=` query parameter. The gateway uses it to route the list,
  then strips it from the request before forwarding upstream.

Retrieve, content, and delete do **not** take a `model` — the model used at upload time is decoded
from the file id.

:::caution `model` selects a route, it does not filter files

`model` is a **routing selector, not a content filter**. Files carry no model, so
`GET /v1/files?model=<model>` returns **all** files on the backend that serves `<model>`, including
files that were uploaded while targeting a different model on the same backend. There is no way to
list "only the files created for model X".

:::

## Configuration

The Files API works with any OpenAI or OpenAI-compatible `AIServiceBackend`. The example below
defines a route that matches the `gpt-4o-mini` model and forwards to OpenAI. This is the same
`AIGatewayRoute` / `AIServiceBackend` / `BackendSecurityPolicy` pattern used for chat completions —
no Files-specific resource is required.

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-files
  namespace: default
spec:
  parentRefs:
    - name: envoy-ai-gateway-basic
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: gpt-4o-mini
      backendRefs:
        - name: envoy-ai-gateway-basic-openai
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: envoy-ai-gateway-basic-openai
  namespace: default
spec:
  schema:
    name: OpenAI
  backendRef:
    name: envoy-ai-gateway-basic-openai
    kind: Backend
    group: gateway.envoyproxy.io
```

:::tip Raise the buffer limit for uploads

File uploads can be large. Envoy Gateway defaults the client buffer limit to 32KB, which is too
small for most uploads. The `basic.yaml` example configures a `ClientTrafficPolicy` with a 50MB
buffer limit; make sure your gateway does the same. See
[Basic Usage](../../getting-started/basic-usage.md) for the full `ClientTrafficPolicy` example
(including the HTTP/2 window-size settings).

:::

### Encryption of file ids

The gateway encrypts the backend identity and routing model into every file id using AES-GCM with a PBKDF2-derived key —
the same mechanism used for MCP session encryption. The key is derived from a seed configured on the
external processor:

| Flag                                   | Default                 | Description                                                                            |
| -------------------------------------- | ----------------------- | -------------------------------------------------------------------------------------- |
| `--fileIDEncryptionSeed`               | `default-insecure-seed` | Seed used to derive the file id encryption key. **Change this in production.**         |
| `--fileIDEncryptionIterations`         | `100000`                | PBKDF2 iterations for key derivation.                                                  |
| `--fileIDFallbackEncryptionSeed`       | _(empty)_               | Optional fallback seed, tried on decode when the primary fails. Enables seed rotation. |
| `--fileIDFallbackEncryptionIterations` | `100000`                | PBKDF2 iterations for the fallback seed.                                               |

:::danger Never use the well-known default seed

If `--fileIDEncryptionSeed` is left at the well-known default (`default-insecure-seed`), **anyone who
knows this public default can decrypt and forge file and batch ids**. The external processor logs a
warning at startup when it detects the default. Set the seed to a secret, randomly generated value
before exposing the gateway to untrusted or multi-tenant clients.

:::

#### Kubernetes (Helm)

When deployed with the Helm chart, the controller passes the seed to every external processor it
manages. Configure it with the `controller.fileIDEncryption` values:

| Helm value                                        | Default  | Description                                                                                                                                                                                                                    |
| ------------------------------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `controller.fileIDEncryption.seed`                | `""`     | Seed for the id encryption key. When empty, the chart generates a random seed on install, stores it in the Secret `<controller fullname>-file-id-encryption`, and reuses it on upgrades.                                       |
| `controller.fileIDEncryption.existingSecret`      | `""`     | Name of an existing Secret (in the release namespace) holding the seed under the `seed` key. Takes precedence over `seed`. Recommended for `helm template` / GitOps tooling, which cannot look up a previously generated seed. |
| `controller.fileIDEncryption.iterations`          | `100000` | PBKDF2 iterations.                                                                                                                                                                                                             |
| `controller.fileIDEncryption.fallback.seed`       | `""`     | Previous seed, for rotation.                                                                                                                                                                                                   |
| `controller.fileIDEncryption.fallback.iterations` | `100000` | PBKDF2 iterations for the fallback seed.                                                                                                                                                                                       |

:::caution Keep the seed stable

File ids embed the owning backend and the provider-native id, so the seed must stay stable. Changing
it without setting the fallback to the previous value invalidates every previously issued file and
batch id.

To rotate the seed without invalidating existing ids, set the new value as the seed
(`--fileIDEncryptionSeed` / `controller.fileIDEncryption.seed`) and the old value as the fallback
(`--fileIDFallbackEncryptionSeed` / `controller.fileIDEncryption.fallback.seed`). Ids issued under
the old seed continue to decode while new ids use the new seed.

:::

## Usage

The examples below assume `$GATEWAY_URL` points at your gateway (for a Kubernetes deployment, see
[Basic Usage](../../getting-started/basic-usage.md); for the standalone CLI, this is
`http://localhost:1975` — see [`aigw run`](../../cli/run.md)). Replace `gpt-4o-mini` with a model
your route matches.

### Upload a file

The `model` field selects the backend and is stripped before the request reaches the provider.

<Tabs>
<TabItem value="curl" label="curl">

```bash
curl -s $GATEWAY_URL/v1/files \
  -F purpose=batch \
  -F model=gpt-4o-mini \
  -F file=@batchinput.jsonl
```

</TabItem>
<TabItem value="python" label="Python (OpenAI SDK)">

The OpenAI SDK has no `model` argument on `files.create`, so pass it via `extra_body`.

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:1975/v1", api_key="unused")

uploaded = client.files.create(
    file=open("batchinput.jsonl", "rb"),
    purpose="batch",
    extra_body={"model": "gpt-4o-mini"},
)
print(uploaded.id)  # file-... (a gateway-issued id)
```

</TabItem>
</Tabs>

Response:

```json
{
  "id": "file-abc123...",
  "object": "file",
  "bytes": 1234,
  "created_at": 1699061776,
  "filename": "batchinput.jsonl",
  "purpose": "batch"
}
```

The returned `id` is a **gateway-issued** id that encodes the serving backend and model. Use it for
all subsequent requests. The id is deterministic: the same file always gets the same id in
upload, list, retrieve, and delete responses (for the same routing model).

### List files

The `model` query parameter selects which route/backend to list from. It is required on the first
page; on later pages it may be omitted because the `after` id carries the model.

<Tabs>
<TabItem value="curl" label="curl">

```bash
curl -s "$GATEWAY_URL/v1/files?model=gpt-4o-mini&limit=20"
```

</TabItem>
<TabItem value="python" label="Python (OpenAI SDK)">

```python
page = client.files.list(extra_query={"model": "gpt-4o-mini"}, limit=20)
for f in page.data:
    print(f.id, f.filename)
```

</TabItem>
</Tabs>

Response:

```json
{
  "object": "list",
  "data": [
    {
      "id": "file-abc123...",
      "object": "file",
      "filename": "batchinput.jsonl",
      "purpose": "batch"
    }
  ],
  "has_more": false,
  "first_id": "file-abc123...",
  "last_id": "file-abc123..."
}
```

To paginate, pass the returned `last_id` as the `after` parameter on the next request
(`?after=<last_id>`). The gateway decodes it back to the provider-native id before forwarding, and
passes `has_more` through unchanged.

### Retrieve a file's metadata

<Tabs>
<TabItem value="curl" label="curl">

```bash
curl -s $GATEWAY_URL/v1/files/file-abc123...
```

</TabItem>
<TabItem value="python" label="Python (OpenAI SDK)">

```python
info = client.files.retrieve("file-abc123...")
print(info.filename, info.bytes)
```

</TabItem>
</Tabs>

Response:

```json
{
  "id": "file-abc123...",
  "object": "file",
  "bytes": 1234,
  "created_at": 1699061776,
  "filename": "batchinput.jsonl",
  "purpose": "batch"
}
```

### Download a file's content

<Tabs>
<TabItem value="curl" label="curl">

```bash
curl -s $GATEWAY_URL/v1/files/file-abc123.../content -o downloaded.jsonl
```

</TabItem>
<TabItem value="python" label="Python (OpenAI SDK)">

```python
content = client.files.content("file-abc123...")
content.write_to_file("downloaded.jsonl")
```

</TabItem>
</Tabs>

The content response is the raw file bytes, passed through unchanged.

### Delete a file

<Tabs>
<TabItem value="curl" label="curl">

```bash
curl -s -X DELETE $GATEWAY_URL/v1/files/file-abc123...
```

</TabItem>
<TabItem value="python" label="Python (OpenAI SDK)">

```python
deleted = client.files.delete("file-abc123...")
print(deleted.deleted)  # True
```

</TabItem>
</Tabs>

Response:

```json
{
  "id": "file-abc123...",
  "object": "file",
  "deleted": true
}
```

## Error responses

| Status            | When                                                                                                                                                              |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `400 Bad Request` | Upload without a `model` field, list without a `?model=` query parameter, or an invalid `after` id.                                                               |
| `404 Not Found`   | A file id that cannot be decoded (unknown, tampered, or forged), or an unsupported method/path.                                                                   |
| `500`             | The matched route rule has more than one backend, the file id belongs to a different backend than the one serving the route, or the backend schema is not OpenAI. |
| `502 Bad Gateway` | The upstream response contained a file id the gateway could not safely re-encode.                                                                                 |

## Caveats and limitations

- **One backend per route rule.** Requests are rejected if the matched rule has multiple
  `backendRefs`. Moving a model to a different backend makes previously issued file ids unusable.
- **OpenAI schema only.** Non-OpenAI backends are rejected. See the
  [provider support warning](#supported-endpoints).
- **`model` is required** for upload and list, and is a **routing selector, not a content filter** —
  a list returns all of the selected backend's files regardless of which model they were uploaded
  under.
- **Same model on multiple routes.** Both upload and list route by the `model` field. If two
  `AIGatewayRoute`s declare the **same** model, Envoy's first-match-wins selects one of them and the
  other route's files are not reachable via list. Use **distinct models per route**.
- **Change the encryption seed.** With the well-known default `fileIDEncryptionSeed`, anyone can
  decrypt and forge file ids. Set a secret, random value (the Helm chart generates one automatically
  when `controller.fileIDEncryption.seed` is empty). See [Encryption of file ids](#encryption-of-file-ids).
- **File ids are gateway-issued.** Client-visible ids are encrypted and are not the provider's native
  ids. Store and reuse exactly what the gateway returns.

## What's Next

- **[Batch API](./batch-api.md)** - Run batch jobs on uploaded input files
- **[Supported API Endpoints](./supported-endpoints.md)** - Full list of OpenAI-compatible endpoints
- **[Connect OpenAI](../../getting-started/connect-providers/openai.md)** - Configure an OpenAI backend
- **[Basic Usage](../../getting-started/basic-usage.md)** - Deploy a gateway and make your first request
- **[`aigw run`](../../cli/run.md)** - Run the gateway locally without Kubernetes
