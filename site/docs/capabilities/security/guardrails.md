---
id: guardrails
title: Content Guardrails
sidebar_position: 9
---

# Content Guardrails

`GuardrailPolicy` evaluates request or response payloads for an `AIServiceBackend`. Rules can use local regular expressions or external content-safety providers.

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
- `Mask` replaces detected text. Regex and Presidio use `maskReplacement`; Bedrock uses transformed output returned by the provider. Azure Text Analysis does not support Mask.

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

The Azure and Bedrock live tests are disabled unless all required environment variables for a provider are set:

- Presidio managed/external deployment: `TEST_PRESIDIO_ENDPOINT`, `TEST_PRESIDIO_BLOCKED_TEXT`, and optionally `TEST_PRESIDIO_API_KEY`.
- Azure: `TEST_AZURE_CONTENT_SAFETY_ENDPOINT`, `TEST_AZURE_CONTENT_SAFETY_API_KEY`, and `TEST_AZURE_CONTENT_SAFETY_BLOCKED_TEXT`.
- Bedrock: `TEST_AWS_BEDROCK_GUARDRAIL_REGION`, `TEST_AWS_BEDROCK_GUARDRAIL_ID`, `TEST_AWS_BEDROCK_GUARDRAIL_VERSION`, and `TEST_AWS_BEDROCK_GUARDRAIL_BLOCKED_TEXT`. AWS credentials use the standard credential chain.

Run them with:

```shell
go test ./internal/guardrails -run '^TestLive' -v
```
