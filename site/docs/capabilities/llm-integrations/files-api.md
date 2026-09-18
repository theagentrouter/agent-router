---
id: files-api
title: Files API
sidebar_position: 11
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

The Envoy AI Gateway supports the OpenAI-compatible **Files API** (`/v1/files`), letting clients
upload, list, retrieve, download, and delete files through the gateway. Files are commonly used as
inputs to other workflows such as the Batch API and fine-tuning.

## Overview

Unlike chat completions or embeddings — which are stateless and routed per request — a **file is a
stateful object that lives on one specific upstream backend**. A file uploaded to backend A does not
exist on backend B. The gateway therefore has to guarantee two things:

- Every request that carries a file id (retrieve, download content, delete) is routed **back to the
  exact backend that created the file**.
- A single `list` presents **one unified view stitched across all of a route's backends**.

The gateway achieves this **without any server-side database**. When a file is created, the gateway
encrypts the serving backend's identity into the opaque file id it returns to the client, and decodes
it again on subsequent requests to route them back to the same backend.

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
| Retrieve  | `GET /v1/files/{file_id}`         | file id                            |
| Content   | `GET /v1/files/{file_id}/content` | file id                            |
| Delete    | `DELETE /v1/files/{file_id}`      | file id                            |

:::warning Provider support

**OpenAI (and OpenAI-compatible providers) is the only backend schema supported today.** A Files
route pointed at a non-OpenAI schema (AWS Bedrock, GCP Vertex AI, Azure OpenAI, Anthropic, Cohere)
fails closed with `501 Not Implemented`. Support for other providers will be added in the future.

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

Retrieve, content, and delete do **not** take a `model` — the backend is decoded from the file id.

:::caution `model` selects a route, it does not filter files

`model` is a **routing selector, not a content filter**. Files carry no model, so
`GET /v1/files?model=<model>` returns **all** files on the backend(s) that serve `<model>`, including
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

The gateway encrypts backend identity into every file id using AES-GCM with a PBKDF2-derived key —
the same mechanism used for MCP session encryption. The key is derived from a seed configured on the
external processor:

| Flag                                   | Default                 | Description                                                                            |
| -------------------------------------- | ----------------------- | -------------------------------------------------------------------------------------- |
| `--fileIDEncryptionSeed`               | `default-insecure-seed` | Seed used to derive the file id encryption key. **Change this in production.**         |
| `--fileIDEncryptionIterations`         | `100000`                | PBKDF2 iterations for key derivation.                                                  |
| `--fileIDFallbackEncryptionSeed`       | _(empty)_               | Optional fallback seed, tried on decode when the primary fails. Enables seed rotation. |
| `--fileIDFallbackEncryptionIterations` | `100000`                | PBKDF2 iterations for the fallback seed.                                               |

:::caution Change the encryption seed in production

The default seed (`default-insecure-seed`) is insecure and shared. If it is not changed, file ids
issued by one deployment can be decoded by any other deployment using the default. Always set
`fileIDEncryptionSeed` to a strong, secret value in production.

To rotate the seed without invalidating existing file ids, set the new value as
`fileIDEncryptionSeed` and the old value as `fileIDFallbackEncryptionSeed`. Ids issued under the old
seed continue to decode while new ids use the new seed.

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

The returned `id` is a **gateway-issued** id that encodes the serving backend. Use it for all
subsequent requests.

### List files

The `model` query parameter is required and selects which route/backend to list from.

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
(`?model=gpt-4o-mini&after=<last_id>`). The gateway walks every backend that serves the model, so a
single list transparently spans all of the route's backends. See
[Cross-backend pagination](#cross-backend-pagination) below.

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

## Cross-backend pagination

When a route has multiple backends (for example, two OpenAI-compatible backends behind the same
model), each backend only knows about the files uploaded to it. The gateway presents a **single,
unified list** by walking the route's backends one page at a time.

- The **first page** (`?model=<model>` with no `after`) is served by a backend the load balancer
  picks.
- Each page returns an encrypted pagination cursor as `last_id`. Passing it back as `after` advances
  the walk — first through the current backend's remaining files, then on to the next backend,
  until all backends have been visited.

Follow `last_id` → `after` exactly as you would with the stock OpenAI Files API. No client-side
awareness of the individual backends is required.

### How the pagination works

The gateway implements the cross-backend walk as a **stateless deterministic cycle** — no database
or server-side session is required:

1. **Backend ordering.** The route's backends are sorted alphabetically by `namespace/name`. This
   order is stable across all gateway replicas and across restarts, so a cursor always resumes at
   the same position regardless of which replica issues or reads it.

2. **First page.** The load balancer selects the starting backend. After serving the page,
   `last_id` contains an encrypted **list-walk cursor** (`flcur-...`) that records the walk start
   backend, the current backend, and the native `after` position within it.

3. **Subsequent pages.** The cursor is decoded from the `after` query parameter. The gateway pins
   the request to the recorded backend via sticky routing, rewrites `?after=` to the backend-native
   cursor, and forwards the request. When the backend's last page is consumed, the cursor advances
   to the next backend in the cycle.

4. **Walk completion.** The walk terminates when it cycles back to the starting backend. The final
   page sets `has_more: false` and `last_id` to the last re-encoded file ID (not a cursor).

:::info `has_more: true` does not always mean more files on the same backend

The gateway may return `has_more: true` even when the current backend has no more files. This
happens when there are remaining backends in the cycle that have not yet been visited. The cursor
stitches the transition transparently — just continue passing `last_id` as `after`.

:::

## Error responses

| Status                | When                                                                                                       |
| --------------------- | ---------------------------------------------------------------------------------------------------------- |
| `400 Bad Request`     | Upload without a `model` field, or list without a `?model=` query parameter, or an invalid `after` cursor. |
| `404 Not Found`       | A file id that cannot be decoded (unknown, tampered, or forged), or an unsupported method/path.            |
| `410 Gone`            | The backend encoded in the id or cursor no longer exists in the configuration.                             |
| `501 Not Implemented` | The target backend's schema is not OpenAI (see [Provider support](#supported-endpoints)).                  |

## Caveats and limitations

- **OpenAI schema only.** Non-OpenAI backends return `501`. See the
  [provider support warning](#supported-endpoints).
- **`model` is required** for upload and list, and is a **routing selector, not a content filter** —
  a list returns all of the selected backend(s) files regardless of which model they were uploaded
  under.
- **Same model on multiple routes.** Both upload and list route by the `x-ai-eg-model` header. If two
  `AIGatewayRoute`s declare the **same** model, Envoy's first-match-wins selects one of them and the
  other route's files are not reachable via list. Prefer **one route with multiple `backendRefs`**
  for a model, or **distinct models per route**.
- **Change the encryption seed.** The default `fileIDEncryptionSeed` is insecure; set a strong secret
  value in production (see [Encryption of file ids](#encryption-of-file-ids)).
- **File ids are gateway-issued.** Client-visible ids are encrypted and are not the provider's native
  ids. Store and reuse exactly what the gateway returns.

## What's Next

- **[Supported API Endpoints](./supported-endpoints.md)** - Full list of OpenAI-compatible endpoints
- **[Connect OpenAI](../../getting-started/connect-providers/openai.md)** - Configure an OpenAI backend
- **[Basic Usage](../../getting-started/basic-usage.md)** - Deploy a gateway and make your first request
- **[`aigw run`](../../cli/run.md)** - Run the gateway locally without Kubernetes
