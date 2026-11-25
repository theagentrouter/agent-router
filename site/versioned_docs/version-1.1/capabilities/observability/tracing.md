---
id: tracing
title: GenAI Distributed Tracing
sidebar_position: 7
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

Agent Router's router joins and records distributed traces when supplied
with an [OpenTelemetry](https://opentelemetry.io/) collector endpoint.

This guide provides an overview of the spans recorded by the AI Gateway and how
export them to your choice of OpenTelemetry collector.

## Overview

Agent Router's router joins and records distributed traces when supplied
with an [OpenTelemetry](https://opentelemetry.io/) collector endpoint.

Requests to the OpenAI Chat Completions, Completions (legacy), and Embeddings
endpoints are recorded as Spans which include typical timing and request
details. In addition, there are GenAI attributes representing the LLM or
Embeddings call including full request and response details, defined by
[OpenInference semantic conventions][openinference].

OpenInference attributes default to include full request and response data for
both chat completions and embeddings. This can be toggled with configuration,
but when enabled allows systems like [Arize Phoenix][phoenix] to perform
evaluations of production requests captured in OpenTelemetry spans.

For chat completions, this includes traditional LLM metrics such as correctness
and hallucination detection. For embeddings, it enables agentic RAG evaluations
focused on retrieval and semantic analysis.

## Trying it out

Before you begin, you'll need to complete the basic setup from the
[Basic Usage](/docs/getting-started/basic-usage) guide, which includes
installing Envoy Gateway and AI Gateway.

### Install Phoenix for LLM observability

```shell
# Install Phoenix using PostgreSQL storage.
helm install phoenix oci://registry-1.docker.io/arizephoenix/phoenix-helm \
  --namespace envoy-ai-gateway-system \
  --set auth.enableAuth=false \
  --set server.port=6006

# Wait for Phoenix to be ready (first run may take a few minutes to pull images)
kubectl wait --timeout=5m -n envoy-ai-gateway-system \
  pods -l app=phoenix --for=condition=Ready
```

### Configure AI Gateway with OpenTelemetry

Upgrade your AI Gateway installation with [OpenTelemetry configuration][otel-config]:

<CodeBlock language="shell">
{`helm upgrade aieg oci://docker.io/envoyproxy/ai-gateway-helm \\
    --version v${vars.aigwVersion} \\
    --namespace envoy-ai-gateway-system \\
    --set "extProc.extraEnvVars[0].name=OTEL_EXPORTER_OTLP_ENDPOINT" \\
    --set "extProc.extraEnvVars[0].value=http://phoenix-svc.envoy-ai-gateway-system:6006" \\
    --set "extProc.extraEnvVars[1].name=OTEL_METRICS_EXPORTER" \\
    --set "extProc.extraEnvVars[1].value=none"
# OTEL_SERVICE_NAME defaults to "ai-gateway" if not set
# OTEL_METRICS_EXPORTER=none because Phoenix only supports traces, not metrics
# Note: Use fully-qualified service name because ext-proc runs in envoy-gateway-system namespace`}
</CodeBlock>

Wait for the gateway pod to be ready:

```shell
kubectl wait --for=condition=Ready -n envoy-gateway-system \
  pods -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic
```

### Generate traces

Make requests to your AI Gateway to generate traces. Follow the instructions
from [Testing the Gateway](/docs/getting-started/basic-usage#testing-the-gateway)
in the Basic Usage guide to:

1. Set up port forwarding (if needed)
2. Test the chat completions endpoint

Each request will generate traces that are sent to Phoenix.

### Check Phoenix is receiving traces

```bash
kubectl logs -n envoy-ai-gateway-system deployment/phoenix | grep "POST /v1/traces"
```

### Access Phoenix UI

Port-forward to access the Phoenix dashboard:

```shell
kubectl port-forward -n envoy-ai-gateway-system svc/phoenix-svc 6006:6006
```

Then open http://localhost:6006 in your browser to explore the traces.

## Semantic Conventions

Spans are recorded using [OpenInference semantic conventions][openinference] by
default. You can instead emit the
[OpenTelemetry GenAI semantic conventions][otel-genai], which use `gen_ai.*`
attributes and are consumed by OpenTelemetry-native backends:

```yaml
extProc:
  extraEnvVars:
    - name: AI_GATEWAY_TRACING_SEMCONV
      value: "gen_ai"
```

| Value                    | Behavior                           |
| ------------------------ | ---------------------------------- |
| unset or `openinference` | OpenInference attributes (default) |
| `gen_ai`                 | OpenTelemetry GenAI attributes     |

Any other value fails startup rather than silently falling back, so a typo is
reported immediately instead of producing traces nobody is watching.

Only one convention is emitted at a time. Choosing `gen_ai` changes the shape of
your spans, so update dashboards and alerts before switching:

|           | OpenInference            | OpenTelemetry GenAI                             |
| --------- | ------------------------ | ----------------------------------------------- |
| Span name | `ChatCompletion`         | `chat {model}`                                  |
| Span kind | `INTERNAL`               | `CLIENT`                                        |
| Model     | `llm.model_name`         | `gen_ai.request.model`, `gen_ai.response.model` |
| Provider  | `llm.system`             | `gen_ai.provider.name`                          |
| Tokens    | `llm.token_count.prompt` | `gen_ai.usage.input_tokens`                     |
| Messages  | `llm.input_messages.N.*` | `gen_ai.input.messages` (single JSON value)     |
| Errors    | `exception` event        | `error.type` attribute                          |

Note that the GenAI conventions are still marked Development upstream, so
attribute names may change in future releases.

Since span names include the model, they are higher cardinality than
OpenInference's fixed names. This matters for backends that index on span name.

### Capturing message content with GenAI

Unlike OpenInference, which records request and response content by default, the
GenAI conventions treat message content as opt-in because it routinely contains
sensitive data. Enable it explicitly:

```yaml
extProc:
  extraEnvVars:
    - name: AI_GATEWAY_TRACING_SEMCONV
      value: "gen_ai"
    - name: OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT
      value: "true"
```

Token counts and sampling parameters are recorded either way, since the
conventions treat those as metadata rather than content.

Message content is currently mapped for chat completions and Anthropic
messages. Other endpoints record operation, model, usage and sampling
parameters. Several — image generation, speech, transcription, translation and
rerank — have no content attributes defined by the conventions at all.

The `OPENINFERENCE_HIDE_*` variables described below apply only to the
OpenInference convention. They have no effect when `gen_ai` is selected.

### MCP spans

`AI_GATEWAY_TRACING_SEMCONV` selects the vocabulary for MCP spans as well as for
the LLM endpoints. OpenInference defines no MCP conventions, so the default keeps
the gateway-specific attributes MCP spans have always used; `gen_ai` opts into
the [OpenTelemetry MCP semantic conventions][otel-mcp].

|               | default (`openinference`) | `gen_ai`                                       |
| ------------- | ------------------------- | ---------------------------------------------- |
| Span name     | `CallTool`, `ListTools`   | `tools/call {tool}`, `tools/list`              |
| Tool name     | `mcp.tool.name`           | `gen_ai.tool.name` + `gen_ai.operation.name`   |
| Prompt name   | `mcp.prompt.name`         | `gen_ai.prompt.name`                           |
| Request ID    | `mcp.request.id`          | `jsonrpc.request.id`                           |
| Transport     | `mcp.transport`           | `network.transport`, `network.protocol.*`      |
| Errors        | `exception` event         | `error.type`, `rpc.response.status_code`       |
| Session       | on the per-backend event  | also `mcp.session.id` on the span              |
| List sizes    | not recorded              | `mcp.tools.count`, `mcp.resources.count`, ...  |
| Tool call I/O | not recorded              | `gen_ai.tool.call.arguments`/`.result`, opt-in |

Tool call arguments and results are message content, so under `gen_ai` they
follow the same `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` opt-in as
the LLM endpoints. The default convention never records them.

:::note Deprecation
The gateway-specific MCP attributes are deprecated in favor of the OpenTelemetry
MCP conventions. They remain the default for now; a future release will announce
a version in which the default flips. Migrate by setting
`AI_GATEWAY_TRACING_SEMCONV=gen_ai` once your dashboards query the new names.
:::

## Privacy Configuration

:::note
This section applies to the default OpenInference convention. See
[Capturing message content with GenAI](#capturing-message-content-with-genai)
for the `gen_ai` equivalent.
:::

Control sensitive data in traces by adding
[OpenInference configuration][openinference-config] to Helm values when you
reconfigure the AI Gateway. There is [similar config][openinference-embeddings]
for embeddings:

For example, if you are using a `values.yaml` file instead of command line
arguments, you can add the following to control redaction:

```yaml
extProc:
  extraEnvVars:
    # Base OTEL configuration...
    # Hide sensitive data (all default to false)
    - name: OPENINFERENCE_HIDE_INPUTS
      value: "true" # Hide input messages to the LLM
    - name: OPENINFERENCE_HIDE_OUTPUTS
      value: "true" # Hide output messages from the LLM
    # Reduce volume for embeddings (all default to false)
    - name: OPENINFERENCE_HIDE_EMBEDDINGS_TEXT
      value: "true" # Hide embeddings input
    - name: OPENINFERENCE_HIDE_EMBEDDINGS_VECTORS
      value: "true" # Hide embeddings output
```

Note: Hiding inputs/outputs prevents human or LLM-as-a-Judge evaluation of your
LLM requests, such as done with the [Phoenix Evals library][phoenix-evals].

## Session Tracking

Sessions help track and organize related traces across multi-turn conversations
with your AI app. Maintaining context between interactions is key for
observability.

With sessions, you can:

- Track a conversation's full history in one thread.
- View inputs/outputs for a given agent.
- Monitor token usage and latency per conversation.

By tagging spans with a consistent session ID, you get a connected view of
performance across a user's journey.

The challenge is that requests to the gateway may not send traces, making
grouping difficult. Many GenAI frameworks allow you to set custom HTTP headers
when sending traffic to an LLM. Propagating sessions this way is simpler than
instrumenting applications with tracing code and can still achieve grouping.

There's no standard name for session ID headers, but there is a common attribute
in OpenTelemetry, [session.id][otel-session], which has special handling in some
OpenTelemetry platforms such as [Phoenix][phoenix-session].

To bridge this gap, Agent Router lets you map HTTP request headers to
OpenTelemetry attributes. You can define a base mapping shared by metrics,
spans, and access logs, plus optional per-signal mappings for metrics, spans,
and access logs.

- `controller.requestHeaderAttributes`
- `controller.spanRequestHeaderAttributes`
- `controller.metricsRequestHeaderAttributes`
- `controller.logRequestHeaderAttributes`

`controller.spanRequestHeaderAttributes` and `controller.logRequestHeaderAttributes` default to `agent-session-id:session.id` when unset (set them to an empty string to disable the default). Metrics never default to `session.id`.

Both of these use the same value format: a comma-separated list of
`<http-header>:<otel-attribute>` pairs. For example, if your session ID header
is `agent-session-id`, you can map it to the standard OpenTelemetry attribute
`session.id` like this: `agent-session-id:session.id`.

Some metrics systems will be able to do fine-grained aggregation, but not all.
Here's an example of keeping the default session mapping for spans/logs while
only adding a low-cardinality attribute to metrics:

<CodeBlock language="shell">
{`helm upgrade aieg oci://docker.io/envoyproxy/ai-gateway-helm \\
    --version v${vars.aigwVersion} \\
    --namespace envoy-ai-gateway-system \\
    --reuse-values \\
    --set "controller.metricsRequestHeaderAttributes=x-tenant-id:tenant.id"`}
</CodeBlock>

## Cleanup

To remove Phoenix and disable tracing:

<CodeBlock language="shell">
{`# Uninstall Phoenix
helm uninstall phoenix -n envoy-ai-gateway-system

# Disable tracing in AI Gateway

helm upgrade aieg oci://docker.io/envoyproxy/ai-gateway-helm \\
--version v${vars.aigwVersion} \\
--namespace envoy-ai-gateway-system \\
--reuse-values \\
--unset extProc.extraEnvVars`}
</CodeBlock>

## Per-Gateway Configuration

For deployments with multiple Gateways that need different tracing configurations,
use the `GatewayConfig` CRD instead of global Helm values. This allows you to:

- Configure different OTEL endpoints for different Gateways
- Set per-gateway service names for better trace organization
- Override global tracing settings for specific Gateways

### Example

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: production-tracing
  namespace: default
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://production-collector:4317"
        - name: OTEL_SERVICE_NAME
          value: "ai-gateway-production"
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: production-gateway
  annotations:
    aigateway.envoyproxy.io/gateway-config: production-tracing
spec:
  # ...
```

See the [Gateway Configuration](../gateway-config.md) guide for detailed information
on `GatewayConfig` usage, including environment variable precedence and shared configurations.

## See Also

- [Gateway Configuration](../gateway-config.md) - Per-gateway configuration using GatewayConfig
- [OpenInference Specification][openinference] - GenAI Semantic conventions for traces
- [OpenTelemetry Configuration][otel-config] - Environment variable reference
- [Arize Phoenix Documentation][phoenix] - LLM observability platform

---

[openinference]: https://github.com/Arize-ai/openinference/tree/main/spec
[otel-genai]: https://github.com/open-telemetry/semantic-conventions-genai
[otel-mcp]: https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/mcp.md
[openinference-config]: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
[openinference-embeddings]: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
[otel-config]: https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/
[phoenix]: https://docs.arize.com/phoenix
[phoenix-evals]: https://arize.com/docs/phoenix/evaluation/llm-evals
[otel-session]: https://opentelemetry.io/docs/specs/semconv/registry/attributes/session/
[phoenix-session]: https://arize.com/docs/phoenix/tracing/llm-traces/sessions
