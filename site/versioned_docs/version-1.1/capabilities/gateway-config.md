---
id: gateway-config
title: Gateway Configuration
sidebar_position: 2
---

# Gateway Configuration

The `GatewayConfig` CRD provides gateway-scoped configuration for the AI Gateway external processor container. This allows you to configure environment variables and resource requirements at the Gateway level, rather than at the route level.

## Overview

Use `GatewayConfig` when you need to:

- Configure per-gateway OpenTelemetry tracing settings
- Set resource requirements (CPU/memory) for the external processor
- Share configuration across multiple Gateways
- Configure environment variables for different gateway instances without affecting others

## Usage

### Creating a GatewayConfig

Create a `GatewayConfig` resource with your desired configuration:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: my-gateway-config
  namespace: default
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4317"
        - name: OTEL_SERVICE_NAME
          value: "my-ai-gateway"
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
        limits:
          cpu: "500m"
          memory: "512Mi"
```

### Referencing from a Gateway

Reference the `GatewayConfig` from your Gateway using an annotation:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway
  namespace: default
  annotations:
    aigateway.envoyproxy.io/gateway-config: my-gateway-config
spec:
  gatewayClassName: envoy-gateway
  listeners:
    - name: http
      protocol: HTTP
      port: 8080
```

:::note
The `GatewayConfig` must be in the same namespace as the Gateway that references it.
:::

## Configuration Options

### Environment Variables

The `spec.extProc.kubernetes.env` field accepts a list of Kubernetes `EnvVar` objects:

```yaml
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4317"
        - name: OTEL_EXPORTER_OTLP_HEADERS
          value: "api-key=your-secret"
        - name: LOG_LEVEL
          value: "debug"
```

### Resource Requirements

The `spec.extProc.kubernetes.resources` field configures compute resources for the external processor container:

```yaml
spec:
  extProc:
    kubernetes:
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
        limits:
          cpu: "500m"
          memory: "512Mi"
```

If not specified, Kubernetes default resource allocations are used.

### Forward Proxy (Egress)

The `spec.forwardProxy` field routes all upstream AI/LLM traffic from Gateways referencing this `GatewayConfig` through an HTTP CONNECT forward proxy. This is intended for data planes deployed in locked-down networks that have no direct outbound access to providers and require all egress to traverse a proxy.

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: my-gateway-config
  namespace: default
spec:
  forwardProxy:
    # host:port of the HTTP CONNECT proxy (host may be a hostname or IP; port is required).
    address: proxy.corp:3128
```

Under the hood, each upstream cluster's transport socket is wrapped in Envoy's `http_11_proxy` transport socket, which opens an HTTP/1.1 `CONNECT` tunnel to the proxy and then establishes the upstream connection through it. The upstream TLS session is preserved end-to-end inside the tunnel, so the proxy sees only the `CONNECT` and the encrypted bytes — not the request contents.

:::note
The forward proxy applies to all AI Gateway upstream (LLM) clusters for Gateways that reference this `GatewayConfig`. InferencePool backends, which use in-cluster endpoints, are not proxied.
:::

:::warning
Proxy authentication (for example, `Proxy-Authorization`) and a TLS connection to the proxy itself are not currently supported. The proxy is expected to be reachable over plaintext HTTP and to authorize the gateway by network policy (for example, an IP allowlist).
:::

## Environment Variable Precedence

Environment variables can be configured at multiple levels. The precedence order is (highest to lowest):

1. **GatewayConfig.spec.extProc.kubernetes.env** - Highest priority
2. **Global controller flags** (`--extproc-extra-env-vars`) - Lower priority

When the same environment variable is defined at multiple levels, the higher precedence value is used.

### Example

If the controller is started with:

```
--extproc-extra-env-vars="LOG_LEVEL=info;GLOBAL_VAR=global"
```

And a GatewayConfig defines:

```yaml
env:
  - name: LOG_LEVEL
    value: "debug"
  - name: CONFIG_VAR
    value: "config"
```

The resulting environment variables will be:

- `LOG_LEVEL=debug` (GatewayConfig overrides global)
- `GLOBAL_VAR=global` (from global)
- `CONFIG_VAR=config` (from GatewayConfig)

## Shared Configuration

Multiple Gateways can reference the same `GatewayConfig`:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: shared-config
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_SERVICE_NAME
          value: "ai-gateway-cluster"
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gateway-1
  annotations:
    aigateway.envoyproxy.io/gateway-config: shared-config
spec:
  # ...
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gateway-2
  annotations:
    aigateway.envoyproxy.io/gateway-config: shared-config
spec:
  # ...
```

## Migration from Route-Level Configuration

The route-level resource configuration (`AIGatewayRoute.spec.filterConfig.externalProcessor.resources`) is deprecated. Migrate to `GatewayConfig`:

### Before (deprecated)

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: my-route
spec:
  filterConfig:
    type: ExternalProcessor
    externalProcessor:
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
```

### After (recommended)

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: my-config
spec:
  extProc:
    kubernetes:
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway
  annotations:
    aigateway.envoyproxy.io/gateway-config: my-config
spec:
  # ...
```

## Status

The `GatewayConfig` status reports the validity of the configuration:

```yaml
status:
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      message: "GatewayConfig reconciled successfully"
```

Possible condition types:

- `Accepted`: The configuration is valid and applied
- `NotAccepted`: The configuration has validation errors

## See Also

- [Tracing](./observability/tracing.md) - Configure distributed tracing for AI Gateway
- [Metrics](./observability/metrics.md) - Configure metrics collection
- [Examples](https://github.com/theagentrouter/agent-router/tree/main/examples/gateway-config) - Example YAML files
