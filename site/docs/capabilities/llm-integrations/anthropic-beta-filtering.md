---
id: anthropic-beta-filtering
title: Filtering Anthropic Beta Headers
sidebar_position: 12
---

Anthropic clients such as Claude Code send every beta feature they use as one comma-separated `anthropic-beta` header. Claude on AWS Bedrock and GCP Vertex AI rejects the whole request with a 400 if any single value is unsupported, and which values are supported differs by provider and by model. `headerValueFilters` on an `AIServiceBackend` removes individual values from that header before the request reaches the backend, so one unsupported value does not fail the request.

## Filter Modes

Each filter names a header and a mode:

- **`Allowlist`** forwards only the listed values and drops everything else. A new beta value is dropped until you add it, so requests keep working but the feature it gates stays off.
- **`Denylist`** (the default) drops only the listed values and forwards everything else. A new beta value reaches the backend without a config change, but if the backend rejects it, requests fail until you add it to the list.

## Example

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: my-bedrock-claude
spec:
  schema:
    name: AWSAnthropic
  backendRef:
    name: my-bedrock-backend
    kind: Backend
    group: gateway.envoyproxy.io
  headerValueFilters:
    - name: anthropic-beta
      mode: Allowlist
      values:
        - context-1m-2025-08-07
        - interleaved-thinking-2025-05-14
        - tool-search-tool-2025-10-19
```

[anthropic-beta-filters.yaml](https://github.com/theagentrouter/agent-router/blob/main/examples/basic/anthropic-beta-filters.yaml) has complete examples with full value lists, with an `Allowlist` and a `Denylist` for both AWS Bedrock (`AWSAnthropic`) and GCP Vertex AI (`GCPAnthropic`). The AWS `Allowlist` matches the built-in list.

## Things to Know When Writing a Filter

- Only `GCPAnthropic` and `AWSAnthropic` backends honor `headerValueFilters`, only for the `anthropic-beta` header, and only on `/v1/messages` requests. Setting it on a backend with any other schema is rejected.
- On `AWSAnthropic`, values are matched after Anthropic-API-only names are rewritten to their Bedrock names, so list the Bedrock name. For example, `advanced-tool-use-2025-11-20` is rewritten to `tool-search-tool-2025-10-19` before the filter runs, so a filter entry for `advanced-tool-use-2025-11-20` never matches.
- On `AWSAnthropic`, a backend without an `anthropic-beta` filter falls back to a built-in list of values known to be accepted by Bedrock. This fallback is deprecated and will be removed in a future release. Once you configure a filter, the built-in list no longer applies, so a `Denylist` must cover every value your clients send that Bedrock rejects. To keep the current behavior, use the `Allowlist` from the AWS example above.
- `GCPAnthropic` has no built-in list. Without a filter, every value is forwarded to Vertex AI unchanged.
- An empty `values` list leaves the header unchanged. On `AWSAnthropic` it still counts as a configured filter, so it also turns off the built-in list.
