---
id: guardrails
title: Content Guardrails
sidebar_position: 9
---

# Content Guardrails

`GuardrailPolicy` evaluates request or response payloads for an `AIServiceBackend`. Rules can use local regular expressions, native integrations with external content-safety providers, or any custom guardrail service that implements a small HTTP contract.

## Apply a regex guardrail

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GuardrailPolicy
metadata:
  name: pii-guardrails
spec:
  maxRequestBodyBytes: 10485760
  maxResponseBodyBytes: 10485760
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: openai
  rules:
    - name: block-sensitive-input
      phase: Request
      provider:
        type: Regex
        pattern: "(?i)password|social security number"
        action: Block
        message: Request contains sensitive content
```

A matched rule returns HTTP `403` with error type `GuardrailViolation`. Rules are scoped to the generated backends that reference the targeted `AIServiceBackend`.

Rules support three actions:

- `Block` rejects matching traffic.
- `Monitor` records matching traffic without blocking or changing it.
- `Mask` replaces detected text. Regex and Presidio use `maskReplacement`; Bedrock uses transformed output returned by the provider; HTTP uses the returned `replacement` or masks returned findings with `maskReplacement`; Model Armor uses the text de-identified by its Sensitive Data Protection filter. Azure Text Analysis does not support Mask.

When multiple policies target one backend, policies are evaluated in namespace/name order and rules retain declaration order. The first Block result stops evaluation.

## External providers

### Presidio

Presidio calls the analyzer `POST /analyze` endpoint. `scoreThresholdPercent` accepts values from 0 through 100. Authentication is optional.

```yaml
provider:
  type: Presidio
  timeoutSeconds: 5
  failureMode: FailClosed
  presidio:
    endpoint: http://presidio-analyzer.presidio.svc.cluster.local:3000
    language: en
    scoreThresholdPercent: 70
    apiKeySecretRef:
      name: presidio-key
```

When configured, the Secret must contain an `apiKey` entry, sent as a bearer token.

### AWS Bedrock Guardrails

Bedrock uses the ApplyGuardrail API and SigV4 signing. By default, the ext-proc uses the standard AWS credential chain, including IRSA and EKS Pod Identity.

```yaml
provider:
  type: Bedrock
  bedrock:
    region: us-east-1
    guardrailIdentifier: my-guardrail
    guardrailVersion: "1"
```

For static credentials, set `credentialsSecretRef` to a Secret whose `credentials` entry contains an AWS shared credentials file. `endpoint` can override the public Bedrock runtime endpoint for a private endpoint.

### Azure AI Content Safety

```yaml
provider:
  type: AzureContentSafety
  azureContentSafety:
    endpoint: https://my-resource.cognitiveservices.azure.com
    apiVersion: "2024-09-01"
    severityThreshold: 4
    apiKeySecretRef:
      name: azure-content-safety-key
```

The referenced Secret must contain an `apiKey` entry.

### Google Cloud Model Armor

Model Armor screens content with a [Model Armor template](https://docs.cloud.google.com/model-armor/overview). Request rules call `sanitizeUserPrompt` and Response rules call `sanitizeModelResponse` on the regional endpoint `https://modelarmor.<location>.rep.googleapis.com`. The filters (Responsible AI, prompt injection and jailbreak, malicious URLs, and Sensitive Data Protection) and their confidence levels are configured in the template, so the rule only references it:

```yaml
provider:
  type: ModelArmor
  timeoutSeconds: 10
  failureMode: FailClosed
  modelArmor:
    project: my-project
    location: us-central1
    template: my-template
```

A rule matches when Model Armor reports `MATCH_FOUND` for any filter in the template. An invocation result of `FAILURE` is treated as a provider error and follows the rule's failure mode.

By default, the ext-proc uses Google Application Default Credentials, including GKE Workload Identity. The identity needs the `roles/modelarmor.user` role. For static credentials, set `credentialsSecretRef` to a Secret whose `credentials` entry contains a service account key JSON. `endpoint` can override the regional endpoint for a private endpoint. Token requests honor the `AI_GATEWAY_GCP_AUTH_PROXY_URL` proxy, like GCP backend authentication.

`Mask` requires a template with advanced Sensitive Data Protection and a de-identify template; the gateway forwards the de-identified text returned by Model Armor. If any other filter also matches (for example, a jailbreak attempt), there is no safe replacement, so the rule fails instead of forwarding the content.

### Custom HTTP guardrails

Use the `HTTP` provider to integrate a guardrail service that has no native integration, such as an in-house classifier. The gateway calls the service over HTTP instead of running local executables.

```yaml
provider:
  type: HTTP
  action: Mask
  timeoutSeconds: 5
  failureMode: FailClosed
  http:
    endpoint: http://custom-guardrail.guardrails.svc.cluster.local:8080
    path: /analyze
    apiKeySecretRef:
      name: custom-guardrail-key
```

`path` defaults to `/analyze`. When `apiKeySecretRef` is set, the Secret must contain an `apiKey` entry, sent as a bearer token.

#### Request

For each extracted text fragment, the gateway sends `POST {endpoint}{path}`:

```json
{
  "text": "some user input",
  "context": {
    "stage": "input"
  }
}
```

`context.stage` is `input` for `Request` rules and `output` for `Response` rules. Services should ignore unknown fields, because more context may be added later.

#### Response

The service must return HTTP 2xx with:

```json
{
  "action": "allow",
  "findings": [
    {
      "type": "PII",
      "start": 10,
      "end": 20,
      "score": 0.92
    }
  ]
}
```

| Field         | Required | Description                                                                                                                               |
| ------------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `action`      | yes      | `allow`, `block`, or `modify`.                                                                                                            |
| `findings`    | no       | Detected spans. `start` and `end` are Unicode code point offsets into `text`, with `end` exclusive. `type` and `score` are informational. |
| `replacement` | no       | Replacement text for the whole fragment, used with `modify`.                                                                              |

The gateway interprets the response as follows:

| Service `action` | Result                                                                                                      |
| ---------------- | ----------------------------------------------------------------------------------------------------------- |
| `allow`          | The rule does not match.                                                                                    |
| `block`          | The rule matches. Findings, if present, are masked with `maskReplacement` for `Mask` rules.                 |
| `modify`         | The rule matches. `replacement` is used when present; otherwise findings are masked with `maskReplacement`. |

The rule's `action` decides what happens on a match: `Block` rejects the request or response, `Monitor` records it, and `Mask` rewrites the fragment. A `Mask` rule fails when the service reports a match without a replacement or valid findings, so unmasked content is never forwarded.

Non-2xx responses, invalid JSON, and missing or unknown actions are provider errors handled by `failureMode`.

A minimal service that blocks prompts containing a keyword could look like this:

```python
from fastapi import FastAPI
from pydantic import BaseModel

app = FastAPI()

class Context(BaseModel):
    stage: str

class Request(BaseModel):
    text: str
    context: Context

@app.post("/analyze")
def analyze(req: Request):
    start = req.text.lower().find("confidential")
    if start < 0:
        return {"action": "allow"}
    return {
        "action": "block",
        "findings": [{"type": "KEYWORD", "start": start, "end": start + len("confidential"), "score": 1.0}],
    }
```

Python string indexes are Unicode code points, which matches the offsets expected by the gateway.

## Failure behavior

External providers default to `failureMode: FailClosed`. Provider errors, missing Secrets, and revoked credentials prevent unchecked traffic. Use `FailOpen` only when availability is more important than enforcement:

```yaml
provider:
  type: Presidio
  failureMode: FailOpen
  timeoutSeconds: 3
  presidio:
    endpoint: http://presidio-analyzer.presidio.svc.cluster.local:3000
```

Credential Secret changes automatically requeue the policy and regenerate affected gateway configuration.

## Streaming responses

When an applicable response guardrail exists, the gateway keeps the upstream response buffered until evaluation completes. This prevents unsafe content from being partially delivered before a blocking or masking decision and gives Monitor rules a complete payload. Routes without response guardrails retain normal streaming behavior.

Request and response limits default to 10 MiB and can be configured independently with `maxRequestBodyBytes` and `maxResponseBodyBytes`, up to 50 MiB. An oversized payload follows the rule's failure mode.

## Observability

The ext-proc emits structured block and provider-failure logs without payload content. It also records `guardrail.evaluation` span events and the counter:

```text
aigateway.guardrail.evaluation.count
```

The counter attributes are `aigateway.guardrail.phase` (`Request` or `Response`) and `aigateway.guardrail.result` (`allowed`, `blocked`, or `error`). With the Prometheus exporter, dots are converted to underscores.

## More examples

See [`examples/guardrails`](https://github.com/envoyproxy/ai-gateway/tree/main/examples/guardrails) for local and external-provider manifests.

## Optional live-provider tests

Presidio is tested against its official analyzer image with Testcontainers. The test runs automatically when Docker is available and skips otherwise:

```shell
go test ./internal/guardrails -run '^TestPresidioEvaluatorContainer$' -v
```

The Azure, Bedrock, and Model Armor live tests are disabled unless all required environment variables for a provider are set:

- Presidio managed/external deployment: `TEST_PRESIDIO_ENDPOINT`, `TEST_PRESIDIO_BLOCKED_TEXT`, and optionally `TEST_PRESIDIO_API_KEY`.
- Azure: `TEST_AZURE_CONTENT_SAFETY_ENDPOINT`, `TEST_AZURE_CONTENT_SAFETY_API_KEY`, and `TEST_AZURE_CONTENT_SAFETY_BLOCKED_TEXT`.
- Bedrock: `TEST_AWS_BEDROCK_GUARDRAIL_REGION`, `TEST_AWS_BEDROCK_GUARDRAIL_ID`, `TEST_AWS_BEDROCK_GUARDRAIL_VERSION`, and `TEST_AWS_BEDROCK_GUARDRAIL_BLOCKED_TEXT`. AWS credentials use the standard credential chain.
- Model Armor: `TEST_GCP_MODEL_ARMOR_PROJECT`, `TEST_GCP_MODEL_ARMOR_LOCATION`, `TEST_GCP_MODEL_ARMOR_TEMPLATE`, and `TEST_GCP_MODEL_ARMOR_BLOCKED_TEXT`. Google credentials use Application Default Credentials.

Run them with:

```shell
go test ./internal/guardrails -run '^TestLive' -v
```
