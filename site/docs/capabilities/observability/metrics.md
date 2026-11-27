---
id: metrics
title: AI/LLM Metrics
sidebar_position: 6
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

When using the Agent Router, it will collect AI specific metrics and expose them to Prometheus for monitoring by default.
This guide provides an overview of the metrics collected by the AI Gateway and how to monitor them using Prometheus.

## Overview

Agent Router is designed to intercept and process AI/LLM requests, that enables it to collect metrics for monitoring and observability.
Currently, it collects metrics and exports them to Prometheus for monitoring in the OpenTelemetry format as specified by the [OpenTelemetry Gen AI Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/attributes-registry/gen-ai/).
Not all metrics are supported yet, but the Agent Router will continue to add more metrics in the future.

### Supported Endpoints

Metrics are collected for the following LLM endpoints:

- **`/v1/chat/completions`** - Chat completions (streaming and non-streaming)
- **`/v1/completions`** - Legacy text completions (streaming and non-streaming)
- **`/v1/embeddings`** - Text embeddings
- **`/cohere/v2/rerank`** - Rerank
- **`/typesafe/v1/systemone`** - TypeSafe System One (Jev)
- **`/anthropic/v1/messages`** - Anthropic messages (streaming and non-streaming)

For example, the Agent Router collects metrics such as:

- [**`gen_ai.client.token.usage`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiclienttokenusage): Number of tokens processed. The attribute `gen_ai.token.type` separates `input` and `output` tokens, plus `cached_input`, `cache_creation_input` and `reasoning` tokens when the response reports them.
- [**`gen_ai.server.request.duration`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiserverrequestduration): Measured from the start of the received request headers in the Agent Router filter to the end of the processed response body processing.
- [**`gen_ai.server.time_to_first_token`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_to_first_token): Measured from the start of the received request headers in the Agent Router filter to the receiving of the first token in the response body handling.
- [**`gen_ai.server.time_per_output_token`**](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#metric-gen_aiservertime_per_output_token): The latency between consecutive tokens, if supported, or by chunks/tokens otherwise.

Each metric comes with some default attributes such as:

- `gen_ai.operation.name`
  - `chat`: For `/v1/chat/completions` endpoint.
  - `completion`: For `/v1/completions` endpoint.
  - `embedding`: For `/v1/embeddings` endpoint.
  - `rerank`: For `/cohere/v2/rerank` endpoint.
  - `systemone`: For `/typesafe/v1/systemone` endpoint.
  - `image_generation`: For `/v1/images/generations` endpoint.
  - `messages`: For `/anthropic/v1/messages` endpoint.
- `gen_ai.original.model` - The original model name from the request body
- `gen_ai.request.model` - The model name requested (may be overridden)
- `gen_ai.response.model` - The model name returned in the response
- `gen_ai.provider.name` - The provider name (e.g., `openai`, `anthropic`)
- `gen_ai.backend` - The `AIServiceBackend` that served the request, as `namespace/name`

:::tip

You can enrich the metrics with custom labels extracted from HTTP request headers. Use `controller.requestHeaderAttributes` for a base mapping shared with spans and access logs, and `controller.metricsRequestHeaderAttributes` for metrics-only mappings. Metrics never default to `session.id` because it is high-cardinality. See [values.yaml](https://github.com/theagentrouter/agent-router/blob/main/manifests/charts/ai-gateway-helm/values.yaml) for more details including other configurations.

:::

## Scrape cost and label cardinality

Each Envoy proxy pod serves two Prometheus endpoints:

- `/metrics` on the `aigw-admin` port, with the `gen_ai` metrics described above.
- `/stats/prometheus` on the `metrics` port, with Envoy's own statistics.

Both endpoints build their full response on every scrape. The cost depends on how many series each one holds and how often it is scraped, not on how many requests the gateway serves.

### `gen_ai` metrics

Each metric above is a histogram. Every distinct combination of attribute values is a separate series, and each series adds its number of bucket boundaries plus 3 lines to every scrape:

- `gen_ai.client.token.usage`: 17 lines for each token type.
- `gen_ai.server.request.duration`: 17 lines, plus 17 more once a request has failed, because failed requests also carry `error.type`.
- `gen_ai.server.time_to_first_token`: 24 lines.
- `gen_ai.server.time_per_output_token`: 16 lines.

For example, streaming chat requests for one model on one backend cost 108 lines once one of them has failed, or 159 lines when responses also report cached and reasoning tokens.

Request header attributes multiply this by the number of distinct header values. This applies to `controller.metricsRequestHeaderAttributes` and also to `controller.requestHeaderAttributes`, because the base mapping is applied to metrics too. Measured with one header mapped to a metric attribute, streaming chat requests on one model, and one failed request per header value:

| Distinct header values | Requests | Lines per scrape of one pod       |
| ---------------------- | -------- | --------------------------------- |
| 10                     | 30       | 1,081                             |
| 10                     | 10,000   | 1,081                             |
| 500                    | 1,500    | 54,001 (about 20 MB uncompressed) |
| 5,000                  | 15,000   | 148,001 (the limit, see below)    |

Series are kept until the external processor restarts. Map only low-cardinality headers to metrics, such as an environment, a region, or a tenant ID when there are only a few tenants. Keep user, customer and session IDs on spans and access logs instead, with `controller.spanRequestHeaderAttributes` and `controller.logRequestHeaderAttributes`. Setting either of these replaces its default, `agent-session-id:session.id`, so keep that pair in the list if you rely on it.

Each metric holds at most 2,000 series, the default limit of the OpenTelemetry Go SDK. After that, new attribute combinations are recorded in one series labelled `otel_metric_overflow="true"`, which has no model, token type or header attributes. This caps the `gen_ai` metrics at about 148,000 lines per pod. If the overflow series appears, map fewer headers to metrics.

Prometheus relabeling, such as `metric_relabel_configs` or `metricRelabelings` with `labeldrop` in a `PodMonitor`, runs after the scrape. It does not reduce the work on the proxy pod, and Prometheus still parses every line. To remove a header attribute, remove its mapping.

### Envoy statistics

Envoy builds its whole statistics page on every scrape, with about 230 lines for each upstream cluster that has received traffic. If you only need the `gen_ai` metrics, as the Grafana dashboard in `examples/monitoring` does, scrape only the `aigw-admin` port. Otherwise, limit the statistics Envoy creates with `spec.telemetry.metrics.matches` in the `EnvoyProxy` that your Gateway uses (see [ProxyMetrics](https://gateway.envoyproxy.io/docs/api/extension_types/#proxymetrics)). For example, merge this `telemetry` block into the `EnvoyProxy` from the basic example:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: envoy-ai-gateway-basic
  namespace: default
spec:
  telemetry:
    metrics:
      matches:
        - type: Exact
          value: cluster_manager.warming_clusters
        - type: Prefix
          value: server.
        - type: RegularExpression
          value: "^(listener.+downstream_cx_active|udp.+downstream_sess_active)$"
        - type: RegularExpression
          value: "^cluster.+(membership_(degraded|healthy|total)|upstream_cx_active|upstream_rq_[0-9]xx|upstream_rq_time)$"
        - type: RegularExpression
          value: "^http.+downstream_rq_([0-9]xx|time)$"
```

It keeps the statistics Envoy Gateway recommends never disabling, and the active listener connection and UDP session counts that Envoy Gateway reads while a proxy pod shuts down. Keep that pattern in any list you write: without it, each pod waits for the full drain timeout, 60 seconds by default, before it stops. The example also keeps response classes, latency and active connections per cluster, downstream response classes and latency, and the `server.*` statistics. In a test with 1,000 clusters that had traffic, it reduced Envoy's page from 232,494 to 54,259 lines (77% fewer).

The Prometheus configuration in `examples/monitoring/monitoring.yaml` scrapes both endpoints every 3 seconds, which suits a demo. A longer scrape interval reduces this work in proportion.

## Trying it out

Before you begin, you'll need to complete the basic setup from the [Basic Usage](/docs/getting-started/basic-usage) guide.

Then, you can install the prometheus using the following commands:

<CodeBlock language="shell">
{`kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/monitoring/monitoring.yaml`}
</CodeBlock>

Let's wait for a while until the Prometheus is up and running.

```shell
kubectl wait --for=condition=ready pod -l app=prometheus -n monitoring
```

To access the Prometheus dashboard, you need to port-forward the Prometheus service to your local machine like this:

```shell
kubectl port-forward -n monitoring svc/prometheus 9090:9090
```

Now open your browser and navigate to `http://localhost:9090` to access the Prometheus dashboard to explore the metrics.

Alternatively, you can make the following requests to see the raw metrics:

```shell
curl http://localhost:9090/api/v1/query --data-urlencode \
  'query=sum(gen_ai_client_token_usage_sum{gateway_envoyproxy_io_owning_gateway_name = "envoy-ai-gateway-basic"}) by (gen_ai_request_model, gen_ai_token_type)' \
  | jq '.data.result[]'
```

and then you would get the response like this, assuming you have made some requests with the model `gpt-4o-mini`:

```json lines
{
  "metric": {
    "gen_ai_request_model": "gpt-4o-mini",
    "gen_ai_token_type": "input"
  },
  "value": [
    1743105857.684,
    "12"
  ]
}
{
  "metric": {
    "gen_ai_request_model": "gpt-4o-mini",
    "gen_ai_token_type": "output"
  },
  "value": [
    1743105857.684,
    "13"
  ]
}
```
