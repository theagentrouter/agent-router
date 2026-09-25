---
id: compatibility
title: Compatibility Matrix
sidebar_position: 6
---

# Compatibility Matrix

This document provides compatibility information for Agent Router releases with their dependencies.

| Agent Router | Envoy Gateway                 | Kubernetes | Gateway API | Inference Extension | Support Status |
| ------------ | ----------------------------- | ---------- | ----------- | ------------------- | -------------- |
| main         | v1.8.1+ (Envoy Proxy v1.38.x) | v1.32+     | v1.6.x      | v1.6.0              | Development    |
| v1.0.x       | v1.8.1+ (Envoy Proxy v1.38.x) | v1.32+     | v1.5.x      | v1.0.2              | Supported      |
| v0.7.x       | v1.8.x+ (Envoy Proxy v1.38.x) | v1.32+     | v1.5.x      | v1.0.2              | Supported      |
| v0.6.x       | v1.7.x+ (Envoy Proxy v1.37.x) | v1.32+     | v1.4.x      | v1.0.2              | Supported      |
| v0.5.x       | v1.6.x+ (Envoy Proxy v1.35.x) | v1.32+     | v1.4.x      | v1.0.2              | Supported      |
| others       | N/A                           | N/A        | N/A         | N/A                 | End of Life    |

Note that "compatibility" means that these specific combinations have been tested and verified to work together.
Other versions may work but are not officially supported.
Please refer to our [Support Policy](https://github.com/theagentrouter/agent-router/blob/main/RELEASES.md#support-policy) for more details
on how we manage releases and support for different versions.

To upgrade to a new Agent Router version, make sure upgrade your dependencies accordingly to maintain compatibility, especially make sure that
Envoy Gateway and Gateway API versions are up-to-date as per the compatibility matrix above. Then, upgrade the AI Gateway using the standard helm upgrade process.

## Upgrading and Migration

### Helm Upgrade Commands

```bash
# 1. Upgrade CRDs
helm upgrade aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version \
  envoy-ai-gateway-system < NEW_VERSION > -n

# 2. Upgrade application
helm upgrade aieg oci://docker.io/envoyproxy/ai-gateway-helm \
  --version \
  envoy-ai-gateway-system < NEW_VERSION > -n
```

### Migrating from v1alpha1 to v1beta1

AIGatewayRoute, AIServiceBackend, BackendSecurityPolicy, GatewayConfig, and MCPRoute support both v1alpha1 (deprecated) and v1beta1. When you upgrade:

- **Existing v1alpha1 resources** continue to work. The API server can serve them via both v1alpha1 and v1beta1 endpoints.
- **Storage version migration** is not automatic. To migrate existing resources to v1beta1 storage, you must manually re-apply them or use the storage migration API.
- **New resources** should use `apiVersion: aigateway.envoyproxy.io/v1beta1`.
- **QuotaPolicy** remains v1alpha1-only (no v1beta1 available).

#### Migrating Storage Version

Storage version migration requires manual action. After upgrading the CRDs, existing resources remain stored in v1alpha1 format until you explicitly trigger migration. The Kubernetes API server can serve these resources via both v1alpha1 and v1beta1 endpoints, but the underlying storage version in etcd won't change automatically.

To migrate existing resources from v1alpha1 to v1beta1 storage version:

**Re-apply resources** (recommended for small deployments)

Update your manifests to use v1beta1 and re-apply them with `kubectl apply`.

For more information about storage version migration in Kubernetes, see the [Kubernetes documentation](https://kubernetes.io/docs/concepts/overview/working-with-objects/storage-version/#migrating-to-a-different-storage-version).

To adopt v1beta1 in your manifests, change the `apiVersion` field and re-apply:

```yaml
# Before (v1alpha1, deprecated)
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
# ...

# After (v1beta1, preferred)
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
# ...
```
