---
id: scaling
title: Scaling the AI Gateway Controller
sidebar_position: 3
---

# Scaling the AI Gateway Controller

The AI Gateway controller has two independent components with different scaling
characteristics:

1. **Kubernetes controller** — reconciles CRDs and performs mutable operations
   against the Kubernetes API (requires leader election).
2. **Extension server** — a gRPC server that handles Envoy Gateway extension
   calls. It is read-only with respect to the Kubernetes API and scales
   horizontally.

Because Envoy Gateway's xDS server communicates with all Envoy instances, and
each EG replica in turn calls the AIGW extension server, the extension server
can become a bottleneck under load. Running multiple AIGW controller replicas
distributes this load across pods.

:::note
Leader election applies only to the Kubernetes controller portion. The
extension server starts and serves traffic on every replica, including
non-leader pods, so all replicas handle extension server requests from the
moment they are ready.
:::

## Recommended Configuration

### Helm

Set `controller.replicaCount` and ensure `controller.leaderElection.enabled`
is `true` (the default):

```yaml
controller:
  replicaCount: 2
  leaderElection:
    enabled: true
```

Apply with:

```shell
helm upgrade --install ai-gateway-helm \
  oci://docker.io/envoyproxy/ai-gateway-helm \
  --namespace envoy-ai-gateway-system \
  --values values.yaml
```

### Kubernetes Deployment (without Helm)

Edit the controller `Deployment` directly:

```shell
kubectl -n envoy-ai-gateway-system scale deployment ai-gateway-controller \
  --replicas=2
```

Or patch the `Deployment` manifest:

```yaml
spec:
  replicas: 2
```

## Horizontal Pod Autoscaler

For dynamic workloads you can pair the above with an HPA. The extension server
is CPU-bound, so CPU utilization is a reasonable metric:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: ai-gateway-controller
  namespace: envoy-ai-gateway-system
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: ai-gateway-controller
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

## Resource Requests and Limits

Size each replica according to the number of Envoy instances it will serve.
A reasonable starting point:

```yaml
controller:
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "256Mi"
```

Adjust based on observed usage in your environment.

## See Also

- [Gateway Configuration](./gateway-config.md) — per-gateway ext_proc settings
- [Observability](./observability/) — metrics to monitor controller load
