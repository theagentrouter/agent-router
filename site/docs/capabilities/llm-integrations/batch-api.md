---
id: batch-api
title: Batch API
sidebar_position: 12
---

Agent Router supports the OpenAI-compatible **Batch API** (`/v1/batches`), letting clients
create, retrieve, list, and cancel asynchronous batch jobs through the gateway. A batch processes a
JSONL input file, uploaded through the [Files API](./files-api.md), and writes its results to an
output file that can be downloaded through the Files API.

On [GCP Vertex AI](#gcp-vertex-ai) backends, which have no OpenAI-compatible Files API, the input is
a Cloud Storage URI instead of an uploaded file, and the results are written to Cloud Storage.

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

| Operation | Method &amp; Path                    | Routed by                                                                               |
| --------- | ------------------------------------ | --------------------------------------------------------------------------------------- |
| Create    | `POST /v1/batches`                   | model recorded in the gateway `input_file_id`, or the `model` field for a `gs://` input |
| List      | `GET /v1/batches`                    | `model` query param, or the model in the `after` batch id                               |
| Retrieve  | `GET /v1/batches/{batch_id}`         | model recorded in the batch id                                                          |
| Cancel    | `POST /v1/batches/{batch_id}/cancel` | model recorded in the batch id                                                          |

Which batch `endpoint` values are accepted depends on the backend's provider. For OpenAI and
OpenAI-compatible backends the gateway forwards any `endpoint` unchanged, and the provider validates
it. GCP Vertex AI accepts `/v1/chat/completions` and `/v1/embeddings`, and rejects any other
endpoint with `400`.

:::warning Provider support

The Batch API supports the **`OpenAI`** schema (OpenAI and OpenAI-compatible providers) and the
**`GCPVertexAI`** schema. A Batch route pointed at any other schema fails closed with an error.

:::

## Routing

- **Create** takes no `model`. The gateway decodes the gateway file id in `input_file_id`, routes
  the request by the model the file was uploaded under, and replaces the id with the provider's own
  file id before forwarding. When `input_file_id` is a Cloud Storage URI (`gs://...`), there is no
  gateway id to carry the model, so the request must set the `model`
  [extension field](#gcp-vertex-ai) instead.
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
the Helm values under `controller.fileIDEncryption` (or the `AIGW_FILE_ID_ENCRYPTION_SEED`
environment variable with `aigw run`), as described in
[Encryption of file ids](./files-api.md#encryption-of-file-ids). The seed is delivered to the
external processors through the filter configuration, so it is not visible in the Pod spec or
process command line. With Helm, the chart generates a random seed on install when
`controller.fileIDEncryption.seed` is empty and reuses it on upgrades.

:::danger Never use the well-known default seed

If the seed is left at the well-known default (`default-insecure-seed`), **anyone who knows this
public default can decrypt and forge batch and file ids**. Set the seed to a secret,
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

## GCP Vertex AI

On a `GCPVertexAI` backend the gateway translates the Batch API to Vertex AI
[batch prediction jobs](https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/batch-prediction-gemini).
Batches for `/v1/chat/completions` and `/v1/embeddings` are supported.

Vertex AI has no OpenAI-compatible Files API, so the Files API is not used:

- **Input:** pass a Cloud Storage URI as `input_file_id`, for example
  `gs://my-bucket/batch/input.jsonl`. The file must already be in
  [Vertex AI batch format](https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/batch-prediction-from-cloud-storage),
  one `{"request": {...}}` object per line. The gateway passes the file to Vertex AI as-is; it does
  not convert OpenAI-format JSONL.
- **Output:** Vertex AI writes the results under a Cloud Storage prefix. Once the batch completes,
  `output_file_id` holds the `gs://` directory with the results. Read it with Cloud Storage tools;
  it cannot be downloaded through the gateway Files API. There is no `error_file_id`: Vertex AI
  records per-request errors inline in the results.

`input_file_id` and `output_file_id` are returned as `gs://` URIs, not gateway file ids. Batch ids
are still gateway-issued `batch-...` ids.

### Extension fields

The create request takes these [extension fields](./vendor-specific-fields.md) in addition to the
standard OpenAI fields:

| Field               | Required | Description                                                                                                                                          |
| ------------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `model`             | Yes      | Routes the request, and sets the Vertex AI model. Required because a `gs://` input carries no model. A backend `modelNameOverride` takes precedence. |
| `output_uri_prefix` | No       | `gs://` prefix Vertex AI writes the results under. Defaults to an `output/` folder next to the input file.                                           |
| `display_name`      | No       | The Vertex AI job display name. Defaults to `aigw-batch-<unix seconds>`.                                                                             |
| `labels`            | No       | Vertex AI job labels. Defaults to `metadata`. Keys and values are lowercased, and characters Vertex AI does not allow become `_`.                    |

With an OpenAI SDK, send them through `extra_body`:

```python
from openai import OpenAI

client = OpenAI(base_url=f"{GATEWAY_URL}/v1", api_key="unused")

batch = client.batches.create(
    input_file_id="gs://my-bucket/batch/input.jsonl",
    endpoint="/v1/chat/completions",
    completion_window="24h",
    metadata={"team": "ml"},
    extra_body={
        "model": "gemini-2.5-flash",
        "output_uri_prefix": "gs://my-bucket/batch/output/",
    },
)
batch = client.batches.retrieve(batch.id)
print(batch.status, batch.output_file_id)
```

Or with `curl`:

```bash
curl $GATEWAY_URL/v1/batches -H "Content-Type: application/json" -d '{
  "input_file_id": "gs://my-bucket/batch/input.jsonl",
  "endpoint": "/v1/chat/completions",
  "completion_window": "24h",
  "model": "gemini-2.5-flash",
  "output_uri_prefix": "gs://my-bucket/batch/output/"
}'
```

The extension fields are removed before a request is forwarded to an OpenAI backend.

### Status mapping

| Vertex AI job state                                           | Batch `status` |
| ------------------------------------------------------------- | -------------- |
| `JOB_STATE_QUEUED`, `JOB_STATE_PENDING`                       | `validating`   |
| `JOB_STATE_RUNNING`, `JOB_STATE_UPDATING`, `JOB_STATE_PAUSED` | `in_progress`  |
| `JOB_STATE_SUCCEEDED`, `JOB_STATE_PARTIALLY_SUCCEEDED`        | `completed`    |
| `JOB_STATE_FAILED`                                            | `failed`       |
| `JOB_STATE_CANCELLING`                                        | `cancelling`   |
| `JOB_STATE_CANCELLED`                                         | `cancelled`    |
| `JOB_STATE_EXPIRED`                                           | `expired`      |

`request_counts` comes from the job's completion statistics, and the timestamps from its create,
start, and end times. Vertex AI answers a cancel with an empty body, so cancel returns a minimal
batch with status `cancelling`; retrieve the batch for its full state.

### List and pagination

List works as for OpenAI backends, with two differences:

- The gateway filters each page to the routed model after Vertex AI returns it. A page can hold
  fewer than `limit` batches, or none, while `has_more` is still `true`; keep paging until
  `has_more` is `false`.
- The cursor for the next page is the response's `last_id`, an encrypted Vertex AI page token,
  rather than the id of the last batch. OpenAI SDK auto-paging sends the last batch's id instead,
  which is rejected with `422`, so page manually with `after=<last_id>`.

### Configuration

Configure the `GCPVertexAI` backend and its `GCPCredentials` `BackendSecurityPolicy` as in
[Connect GCP Vertex AI](../../getting-started/connect-providers/gcp-vertexai.md), then add a route
rule with exactly one backend for the model:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: gcp-batch
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
              value: gemini-2.5-flash
      backendRefs:
        - name: envoy-ai-gateway-basic-gcp
```

The credentials need these permissions:

- The service account the gateway uses needs `aiplatform.batchPredictionJobs.create`, `.get`,
  `.list`, and `.cancel`, for example through `roles/aiplatform.user`.
- The Vertex AI service agent (`service-<project-number>@gcp-sa-aiplatform.iam.gserviceaccount.com`)
  needs read access to the input objects and write access to the output prefix, for example
  `roles/storage.objectViewer` and `roles/storage.objectCreator` on the buckets.

## Error responses

| Status                     | When                                                                                                                                                                                                                                         |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `400 Bad Request`          | Create with a body that is not valid JSON, a missing or non-gateway `input_file_id`, a `gs://` `input_file_id` without `model`, or an `endpoint` the backend's provider does not support; list without `?model=`; an invalid `after` cursor. |
| `404 Not Found`            | A batch id that cannot be decoded (unknown, tampered, forged, or a file id), or an unsupported method/path.                                                                                                                                  |
| `422 Unprocessable Entity` | A request the GCP Vertex AI backend cannot translate: a non-`gs://` `input_file_id` or `output_uri_prefix`, or a list `after` cursor that is a batch id rather than the previous page's `last_id`.                                           |
| `500`                      | The matched route rule has more than one backend, the id belongs to a different backend than the one serving the route, or the backend schema is not supported.                                                                              |
| `502 Bad Gateway`          | A successful upstream response that could not be decoded, translated, or safely re-encoded.                                                                                                                                                  |

Upstream error responses keep their status code. OpenAI error bodies pass through unchanged, and
GCP Vertex AI errors are converted to the OpenAI error shape.

## Caveats and limitations

- **One backend per route rule**, as for the Files API. Supported schemas are `OpenAI` and
  `GCPVertexAI`.
- **GCP Vertex AI input must be in Vertex AI batch format.** The gateway does not convert
  OpenAI-format JSONL, and the results stay in Cloud Storage. Batch delete is not supported, as
  the OpenAI Batch API has no delete operation.
- **Token usage is not metered.** Batch work runs asynchronously on the provider, so the gateway
  does not record token usage for batch requests.
- **JSONL contents are not inspected.** The `model` in each input line is sent upstream as written.
- **Change the encryption seed.** With the well-known default seed, anyone can
  decrypt and forge batch ids. See [Configuration](#configuration).
- **Ids are gateway-issued.** Store and reuse exactly what the gateway returns.

## What's Next

- **[Files API](./files-api.md)** - Upload batch input files and download results
- **[Supported API Endpoints](./supported-endpoints.md)** - Full list of OpenAI-compatible endpoints
