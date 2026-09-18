# Guardrails Proposal

## Table of Contents

- [Guardrails Proposal](#guardrails-proposal)
  - [Table of Contents](#table-of-contents)
  - [Summary](#summary)
  - [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
  - [Prior Art](#prior-art)
    - [Built on Envoy Bedrock Guardrails](#built-on-envoy-bedrock-guardrails)
    - [Built on Envoy Azure Content Safety](#built-on-envoy-azure-content-safety)
    - [Lessons Applied to Agent Router](#lessons-applied-to-agent-router)
  - [Proposed Architecture](#proposed-architecture)
    - [Why ExtProc](#why-extproc)
    - [Policy Attachment and Backend Scoping](#policy-attachment-and-backend-scoping)
    - [Evaluation Lifecycle](#evaluation-lifecycle)
    - [Streaming Responses](#streaming-responses)
  - [API Design](#api-design)
    - [GuardrailPolicy](#guardrailpolicy)
    - [Provider Configuration](#provider-configuration)
    - [Actions](#actions)
    - [Failure Modes and Timeouts](#failure-modes-and-timeouts)
  - [Provider Behavior](#provider-behavior)
    - [Regex](#regex)
    - [Presidio](#presidio)
    - [AWS Bedrock Guardrails](#aws-bedrock-guardrails)
    - [Azure AI Content Safety](#azure-ai-content-safety)
  - [Payload Extraction](#payload-extraction)
  - [Credentials and Security](#credentials-and-security)
  - [Status and Reconciliation](#status-and-reconciliation)
  - [Observability](#observability)
  - [Performance and Reliability](#performance-and-reliability)
  - [Alternatives Considered](#alternatives-considered)
    - [Use Built on Envoy Dynamic Modules Directly](#use-built-on-envoy-dynamic-modules-directly)
    - [Configure Guardrails Directly on AIGatewayRoute](#configure-guardrails-directly-on-aigatewayroute)
    - [Use BackendSecurityPolicy](#use-backendsecuritypolicy)
    - [Use Only Provider-Native Model Guardrails](#use-only-provider-native-model-guardrails)
  - [Implementation Plan](#implementation-plan)
  - [Testing Strategy](#testing-strategy)
  - [Current Implementation and Gaps](#current-implementation-and-gaps)
  - [References](#references)

## Summary

This proposal introduces `GuardrailPolicy`, a backend-attached policy for evaluating LLM request and response content before it is sent to an AI provider or returned to a client. The policy provides a common API for local regular-expression checks and external safety providers, initially Presidio, AWS Bedrock Guardrails, and Azure AI Content Safety.

The proposed implementation uses the existing Agent Router external processor. The controller resolves policies and credentials into the filter configuration, while ext-proc buffers the relevant body, extracts content, invokes the configured evaluator, and applies the selected action.

This design is informed by the Built on Envoy Bedrock Guardrails and Azure Content Safety dynamic-module extensions. It adopts their strongest behavioral patterns while retaining Agent Router's Kubernetes policy model, backend scoping, provider-neutral configuration, existing ext-proc deployment, and observability conventions.

## Motivation

AI Gateway users need a consistent way to enforce content-safety requirements across different models and providers. Today, users must deploy an additional filter, application middleware, or provider-specific integration. Those approaches make policy attachment, credentials, observability, and failure behavior inconsistent across backends.

A native policy should allow platform administrators to:

- attach safety checks to one or more `AIServiceBackend` resources;
- inspect prompts before upstream routing and completions before downstream delivery;
- use deterministic local checks or managed safety services;
- choose blocking or observational behavior independently from provider failures;
- rotate credentials without restarting gateways; and
- observe allowed, blocked, and failed evaluations without recording sensitive payloads.

## Goals

- Define a Kubernetes-native `GuardrailPolicy` consistent with the repository's direct-policy conventions.
- Support both `v1alpha1` and `v1beta1`, with `v1beta1` as the storage version.
- Scope policies to `AIServiceBackend` resources and preserve route/backend isolation.
- Support request and response evaluation.
- Provide a provider-neutral runtime evaluator interface.
- Initially support Regex, Presidio, AWS Bedrock Guardrails, and Azure AI Content Safety.
- Support explicit timeout and fail-open/fail-closed behavior.
- Prevent partial delivery of blocked streaming responses.
- Integrate with existing logs, metrics, traces, config distribution, Secret watches, and status conditions.
- Keep provider credentials out of the CRD and user-visible status.

## Non-Goals

- Replacing provider-native model safety configuration.
- Defining a general-purpose Web Application Firewall API.
- Supporting arbitrary executable plugins supplied by users.
- Performing image, audio, or video moderation in the initial API.
- Guaranteeing identical safety classifications across providers.
- Retrying external safety calls in the initial implementation.
- Exposing provider response details or sensitive matched content to downstream clients.

## Prior Art

### Built on Envoy Bedrock Guardrails

The [Built on Envoy Bedrock Guardrails] extension is an Envoy dynamic-module HTTP filter. It recognizes OpenAI Chat Completions requests, extracts user prompts, invokes each configured Bedrock guardrail, and can apply block, mask, or no-op outcomes.

Notable design choices are:

- semantic extraction of user messages instead of sending the serialized request envelope;
- multiple Bedrock guardrails evaluated for one request;
- configurable request timeout;
- response outputs used to replace masked prompt content; and
- an Envoy cluster used for provider callouts.

The extension authenticates with a Bedrock API key. Agent Router already supports AWS workload credentials and SigV4, so this proposal uses the standard AWS credential chain or a referenced Kubernetes Secret instead.

### Built on Envoy Azure Content Safety

The [Built on Envoy Azure Content Safety] extension is also an Envoy dynamic-module HTTP filter. It auto-detects OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages payloads and uses phase-specific Azure APIs:

- Prompt Shield for request prompt-injection detection;
- Task Adherence for request tool-use alignment;
- Text Analysis for harmful response content; and
- Protected Material Detection for response content.

It supports block and monitor modes, fail-open behavior, selected categories, and independent severity thresholds for hate, self-harm, sexual, and violent content.

The extension demonstrates that vendor integration is more useful when it understands the LLM API schema and chooses a provider operation based on the intended safety check, rather than treating every JSON body as undifferentiated text.

### Lessons Applied to Agent Router

The following ideas should be adopted:

- buffer content before a blocking decision;
- extract semantically relevant text from known LLM schemas;
- distinguish enforcement mode from provider failure behavior;
- support provider-specific checks without leaking them into unrelated providers;
- allow per-category thresholds where the provider exposes them;
- short-circuit after a blocking result; and
- make provider latency and errors observable.

At the time of this proposal, both extension pages identify the implementation as version `0.12.0-dev` with an Envoy compatibility range of 1.38 through 1.39. The following choices remain specific to Agent Router:

- policy attachment through Kubernetes `targetRefs`;
- backend-specific scoping after route selection;
- credentials resolved through Kubernetes Secrets or cloud workload identity;
- configuration distributed through the existing ext-proc bundle;
- a provider-neutral evaluator interface; and
- no additional Envoy binary or dynamic-module deployment requirement.

## Proposed Architecture

```text
GuardrailPolicy + Secret
          |
          v
Gateway controller
  - validates targets and provider configuration
  - resolves credential references
  - scopes rules to generated backend identities
          |
          v
Filter configuration bundle
          |
          v
Envoy ext_proc
  - parses the known LLM request/response schema
  - selects rules for phase and chosen backend
  - invokes local or external evaluators
  - records logs, metrics, and span events
          |
          +--> allow / monitor
          |
          +--> block with HTTP 403
```

### Why ExtProc

Agent Router already uses ext-proc for request parsing, provider translation, credential handling, token accounting, and response processing. Guardrails require the same parsed content and backend identity.

Using the existing ext-proc has these advantages:

- no second extension packaging or compatibility lifecycle;
- no dependency on a narrow Envoy dynamic-module version range;
- direct reuse of endpoint schemas and translators;
- one configuration and observability path; and
- consistent behavior in controller-managed and standalone deployments.

A dynamic module may reduce cross-process overhead and can use Envoy clusters for callouts. However, adopting the [Built on Envoy repository] extensions directly would require reconciling their configuration, credentials, deployment, API parsing, and release compatibility with Agent Router. This proposal therefore uses them as prior art rather than as runtime dependencies.

### Policy Attachment and Backend Scoping

`GuardrailPolicy.spec.targetRefs` references one or more `AIServiceBackend` resources in the policy namespace. During gateway reconciliation, the controller maps each target to generated per-route backend identities. Runtime rules are evaluated only when the matching backend is selected.

Request rules that do not depend on backend selection may run at router level. Backend-attached rules run after selection and before upstream forwarding. Response rules run against the selected backend's response.

Multiple policies targeting the same backend compose additively. The controller sorts policies lexicographically by namespace and name, then preserves declaration order within each policy. Evaluation stops at the first blocking rule. Monitor and Mask rules continue to later rules after recording or applying their result.

### Evaluation Lifecycle

Rules execute in order for their phase and backend:

1. Select rules matching the request or response phase.
2. Filter rules by the selected generated backend identity.
3. Extract provider-relevant content from the parsed LLM payload.
4. Invoke the evaluator with the request context and configured timeout.
5. Record the result.
6. Stop at the first blocking result.
7. Continue after an allowed result or a fail-open provider error.

A provider error under `FailClosed` prevents unchecked traffic. A provider error under `FailOpen` records an error and allows evaluation to continue. Provider failures are not counted as allowed evaluations.

### Streaming Responses

A response cannot be safely blocked or rewritten after bytes have already reached the client. When any applicable response rule is configured, ext-proc must keep response-body processing buffered. Only routes without applicable response guardrails may switch to streamed response processing.

This increases latency and memory use for guarded streaming responses. All response rules, including Monitor, use buffering so providers receive a complete semantic payload rather than partial SSE events. Each policy defaults to a 10 MiB response evaluation limit and may configure up to 50 MiB, matching the Envoy per-connection buffer ceiling. The limit is checked before provider evaluation; exceeding it follows the rule's failure mode.

## API Design

### GuardrailPolicy

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GuardrailPolicy
metadata:
  name: content-safety
  namespace: ai-gateway
spec:
  maxRequestBodyBytes: 10485760
  maxResponseBodyBytes: 10485760
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: openai
  rules:
    - name: block-pii
      phase: Request
      provider:
        type: Presidio
        action: Block
        failureMode: FailClosed
        timeoutSeconds: 5
        message: Request contains sensitive information
        presidio:
          endpoint: http://presidio-analyzer.presidio.svc.cluster.local:3000
          language: en
          scoreThresholdPercent: 70
```

Rules have stable unique names and execute in order. The default action is `Block`; users may explicitly select `Monitor` or `Mask` when the provider supports it. The API reserves room for additional actions without forcing all providers to support identical capabilities.

### Provider Configuration

The provider is a tagged union. Admission validation requires exactly the configuration matching `provider.type`.

- `Regex` requires `pattern`.
- `Presidio` requires `presidio`.
- `Bedrock` requires `bedrock`.
- `AzureContentSafety` requires `azureContentSafety`.

Provider-specific configuration remains nested so new provider capabilities can be added without adding unrelated fields to every rule.

### Actions

The portable actions are:

- `Block`: return HTTP 403 with error type `GuardrailViolation`.
- `Monitor`: record a detection and continue without modifying traffic.
- `Mask`: replace detected text and continue evaluation.

Mask behavior depends on provider capability:

- Regex replaces matching substrings with `maskReplacement`, defaulting to `[REDACTED]`.
- Presidio replaces detected spans with `maskReplacement`.
- Bedrock uses transformed text returned by ApplyGuardrail.
- Azure Text Analysis does not return transformed content, so admission rejects `Mask` for Azure rules.

Mask is applied only to extracted text fields and never replaces unrelated model or configuration fields. Masked streaming responses remain buffered until the complete body has been evaluated and rewritten.

### Failure Modes and Timeouts

`failureMode` controls provider errors, not policy matches:

- `FailClosed` is the default and prevents unchecked traffic.
- `FailOpen` records the provider error and continues processing.

`timeoutSeconds` applies to each external evaluation. The initial implementation does not retry. This avoids multiplying tail latency and prevents ambiguous duplicate calls. If retries are added, they should be limited to clearly retryable transport and 5xx failures and remain inside the rule's total timeout budget.

## Provider Behavior

### Regex

Regex rules compile when the runtime configuration is loaded. Invalid patterns reject the configuration. Evaluation is local and deterministic.

Regex is useful for tests, organization-specific markers, and simple deny patterns. It is not a replacement for semantic safety classification.

### Presidio

[Presidio] calls the Analyzer `/analyze` endpoint with extracted text, language, and an optional score threshold. An optional Secret-provided API key can be sent as a bearer token for deployments that place authentication in front of Presidio.

The official Presidio analyzer image is suitable for deterministic integration tests through Testcontainers.

Future configuration may add entity allow/deny lists. Those should use Presidio entity names and avoid exposing matched text in status, logs, or metrics.

### AWS Bedrock Guardrails

Bedrock uses the [AWS Bedrock ApplyGuardrail] API. The request source is `INPUT` for requests and `OUTPUT` for responses. Authentication uses SigV4 with either:

- the standard AWS credential chain, including workload identity; or
- an AWS shared credentials file read from a Kubernetes Secret.

The Built on Envoy extension additionally supports Bedrock API-key authentication and mask results. API-key support can be added as another credential source if users require it. Mask support requires a separate action and schema-aware mutation contract.

Each rule invokes one Bedrock guardrail identifier/version. Multiple rules may target the same backend, but each external call adds latency and cost. Administrators should prefer consolidating related checks into one managed Bedrock guardrail where possible.

### Azure AI Content Safety

The initial [Azure AI Content Safety] provider configuration includes endpoint, API version, API key Secret, and severity threshold.

The Built on Envoy implementation highlights that Azure has multiple distinct safety operations. The API should eventually model the check explicitly instead of inferring it only from request/response phase:

- request Prompt Shield;
- request Task Adherence;
- request or response Text Analysis;
- response Protected Material Detection.

A future shape could add `azureContentSafety.check` and check-specific configuration. Per-category thresholds should also replace a single threshold when Text Analysis is selected. Until then, the implementation should be documented as Text Analysis over the configured evaluation input, not as complete Azure Content Safety feature parity.

## Payload Extraction

Provider callouts receive relevant user or assistant text, not an arbitrary serialized JSON envelope. Schema-aware extraction reduces false matches, removes model/configuration metadata, lowers provider cost, and avoids provider input-size limits.

The implementation walks known LLM text and container fields in deterministic path order and records each JSON path so Mask can write transformed text back into the same field. It recognizes fields such as `messages`, `content`, `text`, `input`, `prompt`, `instructions`, `system`, `choices`, and `output` while ignoring unrelated values such as model names and image URLs.

A future richer provider-neutral input may add structured fields such as:

```go
type EvaluationInput struct {
	Phase     GuardrailPhase
	Text      []string
	Documents []string
	Tools     []Tool
	Messages  []Message
}
```

External providers and Mask rules consume extracted fragments. Regex Block and Monitor preserve raw-body matching for backward compatibility. Unknown or malformed structured formats produce an evaluation error governed by `failureMode`; they are not silently interpreted as a known chat schema.

## Credentials and Security

Provider credentials are referenced from Kubernetes Secrets and resolved into the ext-proc configuration using the repository's existing bundle mechanism. The controller watches referenced Secrets so rotation or deletion triggers policy and route reconciliation.

Security requirements are:

- never include credentials in `GuardrailPolicy` status, logs, metrics, traces, examples, or error responses;
- never include evaluated payloads or matched text in default logs, metrics, or traces;
- restrict cross-namespace Secret references unless `ReferenceGrant` support is explicitly designed;
- preserve Secret file permissions in local tests;
- use workload identity instead of static AWS credentials where possible; and
- bound provider error bodies before logging or returning errors.

A fail-closed policy whose credentials become unavailable must not leave a stale accepted configuration active. A fail-open policy may omit the unavailable evaluator while recording the configuration failure.

## Status and Reconciliation

The controller sets `Accepted` only when:

- all target backends exist;
- all provider configuration is valid;
- regular expressions compile; and
- all required Secrets and keys are present.

Backend changes, Secret changes, policy updates, and policy deletion trigger reconciliation. Deletion uses the finalizer to notify affected routes before the policy disappears, ensuring stale rules are removed from generated configuration.

Status does not perform a live provider health check. Provider availability is a runtime concern represented by metrics, traces, logs, and failure mode.

## Observability

The runtime emits:

- structured logs for blocks and provider failures without payload content;
- `aigateway.guardrail.evaluation.count` with phase and result attributes; and
- `guardrail.evaluation` span events with rule name, phase, and result.

Metric attributes intentionally exclude rule names and error text to avoid unbounded cardinality. Result values are `allowed`, `blocked`, `monitored`, `masked`, and `error`. Provider type may be added because it is bounded, subject to consistency with the project's metric conventions.

The design should also expose provider-call latency in a future histogram so operators can measure the cost of each external integration.

## Performance and Reliability

External rules add network latency and may add provider cost. Rules currently execute sequentially and short-circuit on block. Sequential execution preserves deterministic ordering but makes worst-case latency the sum of rule timeouts.

The policy defines separate `maxRequestBodyBytes` and `maxResponseBodyBytes` values. Both default to 10 MiB and are capped at 50 MiB. The cap aligns with the configured Envoy per-connection buffer limit. These are evaluation limits rather than streaming truncation: fail-closed rules reject oversized payloads, while fail-open and Monitor rules record the error and continue.

Before increasing rule limits or adding many external checks, the implementation should further define:

- a total guardrail evaluation budget per phase;
- whether independent monitor-only rules can run concurrently;
- provider rate-limit handling.

No automatic retries are proposed initially. Connection reuse is delegated to `http.Client`. Provider contexts are canceled with the ext-proc request.

## Alternatives Considered

### Use Built on Envoy Dynamic Modules Directly

This provides mature provider-specific behavior close to Envoy and avoids ext-proc RPC overhead. It also provides Azure semantic parsing and Bedrock mask behavior today.

It is not selected for the initial implementation because it would introduce:

- a second extension deployment and release lifecycle;
- Envoy dynamic-module ABI/version compatibility constraints;
- separate configuration and credential models;
- duplicated endpoint parsing and observability; and
- additional integration work for controller-managed backend scoping.

The extensions remain valuable prior art, and sharing provider-neutral parsing or client libraries upstream may be preferable to independently maintaining equivalent logic.

### Configure Guardrails Directly on AIGatewayRoute

This makes route-level differences easy but duplicates policy across routes and couples safety configuration to routing. Backend attachment better represents a requirement that should follow a provider endpoint wherever it is referenced.

### Use BackendSecurityPolicy

`BackendSecurityPolicy` controls upstream authentication. Mixing content evaluation into it would combine unrelated ownership and status semantics.

### Use Only Provider-Native Model Guardrails

Provider-native model configuration does not cover cross-provider policy, request checks independent of inference, local Presidio deployments, or consistent gateway observability.

## Implementation Plan

1. Introduce the dual-version `GuardrailPolicy`, generated clients, CRD, and status.
2. Add target and Secret indexes, reconciliation, deletion propagation, and filter-config translation.
3. Add runtime compilation and backend-scoped request/response evaluation.
4. Add Regex, Presidio, Bedrock, and Azure Text Analysis evaluators.
5. Add failure modes, timeouts, logs, metrics, traces, and guarded-response buffering.
6. Add CRD admission, controller, HTTP-stub, Testcontainers, live-provider, and dataplane tests.
7. Add semantic payload extraction and schema-aware Mask mutation.
8. Add Monitor mode and deterministic multi-policy composition.
9. Add explicit request and response evaluation limits.
10. Add provider-specific Azure check selection.
11. Evaluate Bedrock API-key authentication.

Steps 1 through 9 describe the current implementation. Steps 10 and 11 are proposed follow-up work informed by the Built on Envoy extensions.

## Testing Strategy

- CRD admission tests for valid and invalid provider unions, endpoints, actions, limits, and unique rule names.
- Controller tests for target validation, Secret resolution and rotation, status, backend scoping, failure modes, and deletion.
- Runtime tests for regex compilation and evaluator construction.
- HTTP-stub tests for provider paths, payloads, headers, authentication, responses, malformed responses, and errors.
- Testcontainers integration with the pinned official Presidio analyzer image.
- Credential-gated live tests for Azure AI Content Safety and AWS Bedrock Guardrails.
- Envoy dataplane tests for request blocking, response blocking, allowed traffic, and streaming mode behavior.
- Staging tests for policy creation, update, Secret rotation, deletion, and multi-policy attachment.
- Load tests for provider latency, concurrency, and buffered response limits.

## Current Implementation and Gaps

The current implementation includes the policy resource, backend scoping, lifecycle reconciliation, external adapters, blocking, failure modes, timeout, response buffering, observability, and layered tests.

The following gaps remain intentionally visible for design review:

- Azure currently uses Text Analysis and does not yet expose Prompt Shield, Task Adherence, Protected Material Detection, categories, or per-category thresholds;
- schema extraction is field-oriented and does not yet expose structured tools/documents to providers;
- Mask is unsupported for Azure Text Analysis and for non-JSON payloads;
- there is no total per-phase timeout budget or provider latency histogram; and
- load testing is still needed to validate the selected 10 MiB default under production concurrency.

These gaps do not require changing the core policy-to-runtime architecture, but some require API additions before the feature is declared complete.

## References

[Built on Envoy Bedrock Guardrails]: https://builtonenvoy.io/extensions/bedrock-guardrails/
[Built on Envoy Azure Content Safety]: https://builtonenvoy.io/extensions/azure-content-safety/
[Built on Envoy repository]: https://github.com/tetratelabs/built-on-envoy
[AWS Bedrock ApplyGuardrail]: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ApplyGuardrail.html
[Azure AI Content Safety]: https://learn.microsoft.com/azure/ai-services/content-safety/
[Presidio]: https://presidio.dataprivacystack.org/
