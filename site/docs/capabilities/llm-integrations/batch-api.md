---
id: batch-api
title: Batch API
sidebar_position: 12
---

The Envoy AI Gateway supports the OpenAI-compatible **Batch API** (`/v1/batches`), letting clients
create, retrieve, list, and cancel asynchronous batch jobs through the gateway. A batch processes a
JSONL input file, uploaded through the [Files API](./files-api.md), and writes its results to an
output file that can be downloaded through the Files API.

## Overview

Like a file, a **batch is a stateful object that lives on one specific upstream backend**: the
backend that holds its input file. The gateway keeps no database. Instead, it encrypts the owning
backend and the routing model into every batch id it returns, and routes each later request on that
batch back to the same backend.

The batch's `input_file_id`, `output_file_id`, and `error_file_id` are returned as gateway file ids
for the same backend and model, so results can be downloaded directly with
`GET /v1/files/{output_file_id}/content`.

:::warning One backend per route rule

The Batch API currently supports **exactly one backend** per `AIGatewayRoute` rule, the same as the
Files API. A Batch request whose matching rule has more than one `backendRef` is rejected.

:::

:::info

Client-visible batch ids (`batch-...`) and file ids (`file-...`) are **not** the provider's native
ids. Always use the ids the gateway returns.

:::

## Supported Endpoints

| Operation | Method &amp; Path                    | Routed by                                                 |
| --------- | ------------------------------------ | --------------------------------------------------------- |
| Create    | `POST /v1/batches`                   | model recorded in the gateway `input_file_id`             |
| List      | `GET /v1/batches`                    | `model` query param, or the model in the `after` batch id |
| Retrieve  | `GET /v1/batches/{batch_id}`         | model recorded in the batch id                            |
| Cancel    | `POST /v1/batches/{batch_id}/cancel` | model recorded in the batch id                            |

Which batch `endpoint` values are accepted depends on the backend's provider. For OpenAI and
OpenAI-compatible backends the gateway forwards any `endpoint` unchanged, and the provider validates
it. Translators for other providers will reject the endpoints that provider cannot run.

:::warning Provider support

**OpenAI (and OpenAI-compatible providers) is the only backend schema supported today.** A Batch
route pointed at a non-OpenAI schema fails closed with an error.

:::

## Routing

- **Create** takes no `model`. The gateway decodes the gateway file id in `input_file_id`, routes
  the request by the model the file was uploaded under, and replaces the id with the provider's own
  file id before forwarding.
- **Retrieve and cancel** decode the batch id in the path and route by the model recorded in it.
- **List** requires a `?model=` query parameter, unless the request carries a gateway `after`
  cursor, in which case the cursor's model is used. `model` is stripped before forwarding upstream.
  Like Files, `model` selects a route; it does not filter batches.

The `model` field inside each JSONL line of the input file is **not** rewritten by the gateway; it
is sent to the provider as written.

## Configuration

No Batch-specific resource is required. Use the same `AIGatewayRoute` and `AIServiceBackend` as for
the [Files API](./files-api.md#configuration): batches are served by the route that matches the model
used to upload the input file.

Batch ids are encrypted with the same key as file ids. There is no separate Batch setting: configure
`--fileIDEncryptionSeed`, or the Helm values under `controller.fileIDEncryption`, as described in
[Encryption of file ids](./files-api.md#encryption-of-file-ids). With Helm, the chart generates a
random seed on install when `controller.fileIDEncryption.seed` is empty and reuses it on upgrades.

:::danger Never use the well-known default seed

If `--fileIDEncryptionSeed` is left at the well-known default (`default-insecure-seed`), **anyone who
knows this public default can decrypt and forge batch and file ids**. Set the seed to a secret,
randomly generated value before exposing the gateway to untrusted or multi-tenant clients.

:::

:::caution Keep the seed stable

Changing the seed without setting the fallback to the previous value invalidates every previously
issued batch id, including the ids of batches that are still running. To rotate it, set the old value
as the fallback seed; see [Encryption of file ids](./files-api.md#encryption-of-file-ids).

:::

## Usage

The examples assume `$GATEWAY_URL` points at your gateway and that your route matches `gpt-4o-mini`.

```bash
# 1. Upload the JSONL input file (model selects the backend).
curl -F "purpose=batch" -F "model=gpt-4o-mini" -F "file=@batchinput.jsonl" \
  $GATEWAY_URL/v1/files
# => {"id":"file-<gateway-id>", ...}

# 2. Create the batch. No model is needed: it is taken from the input file id.
curl $GATEWAY_URL/v1/batches -H "Content-Type: application/json" -d '{
  "input_file_id": "file-<gateway-id>",
  "endpoint": "/v1/chat/completions",
  "completion_window": "24h"
}'
# => {"id":"batch-<gateway-id>", "status":"validating", ...}

# 3. Check its status.
curl $GATEWAY_URL/v1/batches/batch-<gateway-id>

# 4. Download the results once completed.
curl $GATEWAY_URL/v1/files/<output_file_id>/content

# List batches (model is required for the first page) and cancel.
curl "$GATEWAY_URL/v1/batches?model=gpt-4o-mini&limit=20"
curl -X POST $GATEWAY_URL/v1/batches/batch-<gateway-id>/cancel
```

With the OpenAI Python SDK:

```python
from openai import OpenAI

client = OpenAI(base_url=f"{GATEWAY_URL}/v1", api_key="unused")

f = client.files.create(file=open("batchinput.jsonl", "rb"), purpose="batch",
                        extra_body={"model": "gpt-4o-mini"})
batch = client.batches.create(input_file_id=f.id, endpoint="/v1/chat/completions",
                              completion_window="24h")
batch = client.batches.retrieve(batch.id)
for b in client.batches.list(extra_query={"model": "gpt-4o-mini"}):
    print(b.id, b.status)
if batch.output_file_id:
    print(client.files.content(batch.output_file_id).text)
```

`model` is only needed for the first page. Later pages work whether or not `model` is sent again,
because the `after` cursor is a gateway batch id that carries the model.

## Error responses

| Status            | When                                                                                                                                                                                                                     |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `400 Bad Request` | Create with a body that is not valid JSON, a missing or non-gateway `input_file_id`, or an `endpoint` the backend's provider does not support; list without `?model=`; an `after` cursor that is not a gateway batch id. |
| `404 Not Found`   | A batch id that cannot be decoded (unknown, tampered, forged, or a file id), or an unsupported method/path.                                                                                                              |
| `500`             | The matched route rule has more than one backend, the id belongs to a different backend than the one serving the route, or the backend schema is not OpenAI.                                                             |
| `502 Bad Gateway` | A successful upstream response whose ids could not be safely re-encoded. Upstream error responses are passed through unchanged.                                                                                          |

## Caveats and limitations

- **One backend per route rule**, and **OpenAI schema only**, as for the Files API.
- **Token usage is not metered.** Batch work runs asynchronously on the provider, so the gateway
  does not record token usage for batch requests.
- **JSONL contents are not inspected.** The `model` in each input line is sent upstream as written.
- **Change the encryption seed.** With the well-known default `fileIDEncryptionSeed`, anyone can
  decrypt and forge batch ids. See [Configuration](#configuration).
- **Ids are gateway-issued.** Store and reuse exactly what the gateway returns.

## What's Next

- **[Files API](./files-api.md)** - Upload batch input files and download results
- **[Supported API Endpoints](./supported-endpoints.md)** - Full list of OpenAI-compatible endpoints
