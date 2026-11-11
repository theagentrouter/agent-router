---
id: header-body-mutations
title: Header and Body Mutations
sidebar_position: 7
---

# Header and Body Mutations

Agent Router allows you to mutate HTTP headers and JSON request body fields before requests are sent to backends. This is useful for adding provider-specific headers, setting service tiers, or removing internal fields before forwarding to upstream providers.

## Use Cases

- Add custom headers required by specific backends (e.g., organization IDs, API keys).
- Set JSON body fields like `service_tier`, `max_tokens`, or `temperature` per backend.
- Remove internal or sensitive fields before forwarding requests to upstream providers.
- Override default values on a per-backend or per-route basis.

## Where Mutations Can Be Configured

Mutations can be configured at two levels:

- **AIServiceBackend** — Mutations defined here apply to **all requests** routed to this backend, regardless of which route matched.
- **AIGatewayRoute backendRef** — Mutations defined here apply **only when traffic matches a specific route rule** and is sent to the referenced backend.

:::note Precedence
When both route-level and backend-level mutations are defined, **route-level takes precedence** over backend-level for conflicting operations. Non-conflicting operations from both levels are applied together.
:::

## Header Mutations

Use `headerMutation` to add, overwrite, or remove HTTP headers on the outgoing request.

### Setting Headers

The `set` field overwrites an existing header or adds it if not present. A maximum of 16 entries is allowed.

```yaml
headerMutation:
  set:
    - name: "x-custom-header"
      value: "my-value"
```

For example, given an incoming request with `my-header: foo`, the following configuration changes it to `my-header: bar`:

```yaml
headerMutation:
  set:
    - name: "my-header"
      value: "bar"
```

### Removing Headers

The `remove` field removes the specified headers from the request. Header names are **case-insensitive** (per [RFC 2616](https://datatracker.ietf.org/doc/html/rfc2616#section-4.2)). A maximum of 16 entries is allowed.

```yaml
headerMutation:
  remove:
    - "x-internal-header"
    - "x-debug-header"
```

## Body Mutations

Use `bodyMutation` to add, overwrite, or remove top-level JSON fields in the HTTP request body.

:::warning
Only **top-level** fields are currently supported. Nested field paths are not available.
:::

### Setting Body Fields

The `set` field overwrites an existing JSON field or adds it if not present. A maximum of 16 entries is allowed.

```yaml
bodyMutation:
  set:
    - path: "service_tier"
      value: '"scale"'
    - path: "max_tokens"
      value: "4096"
    - path: "temperature"
      value: "0.7"
```

The `value` field is parsed as raw JSON. This means different value types require different formatting:

| Type    | Example `value`      | Result in JSON body |
| ------- | -------------------- | ------------------- |
| String  | `'"scale"'`          | `"scale"`           |
| Number  | `"42"`               | `42`                |
| Boolean | `"true"`             | `true`              |
| Object  | `'{"key": "value"}'` | `{"key": "value"}`  |
| Array   | `"[1, 2, 3]"`        | `[1, 2, 3]`         |
| Null    | `"null"`             | `null`              |

:::tip
String values require inner quotes. For example, to set a field to the string `"scale"`, use `'"scale"'` in YAML. Numeric and boolean values do not need inner quotes.
:::

### Removing Body Fields

The `remove` field removes the specified top-level fields from the request body. A maximum of 16 entries is allowed.

```yaml
bodyMutation:
  remove:
    - "internal_flag"
    - "debug_mode"
```

## Complete Examples

### Example 1: AIServiceBackend with Mutations

This example configures an `AIServiceBackend` that adds a custom organization header and sets `service_tier` while removing an internal tracking field for all requests routed to this backend:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIServiceBackend
metadata:
  name: my-openai-backend
spec:
  schema:
    name: OpenAI
  backendRef:
    name: my-openai-backend
    kind: Backend
    group: gateway.envoyproxy.io
  headerMutation:
    set:
      - name: "x-custom-org"
        value: "my-org-id"
  bodyMutation:
    set:
      - path: "service_tier"
        value: '"scale"'
    remove:
      - "internal_tracking_id"
```

### Example 2: AIGatewayRoute backendRef with Mutations

This example configures route-level mutations that apply only when requests match a specific route rule. Here, requests for `gpt-4` get a premium header and an increased `max_tokens` value:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
metadata:
  name: my-route
spec:
  parentRefs:
    - name: my-gateway
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: gpt-4
      backendRefs:
        - name: my-openai-backend
          headerMutation:
            set:
              - name: "x-route-specific"
                value: "premium"
          bodyMutation:
            set:
              - path: "max_tokens"
                value: "8192"
```

## Precedence Rules

:::note
When both route-level and backend-level mutations are defined for the same backend:

- **Route-level mutations take precedence** over backend-level mutations for conflicting operations (e.g., both setting the same header name or body field path).
- **Non-conflicting operations from both levels are applied together.** For example, if the backend-level sets header `x-org` and the route-level sets header `x-tier`, both headers are added to the request.
  :::

## References

- [AIServiceBackend](../../api/api.mdx#aiservicebackend)
- [AIGatewayRoute](../../api/api.mdx#aigatewayroute)
