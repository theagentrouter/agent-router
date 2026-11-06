# Inference Gateway API Extension Support

## Proposal Summary

This proposal extends Envoy AI Gateway (EAIG) to support the Gateway API Inference Extension (GAIE), enabling seamless integration of both egress (external AI providers) and ingress (internal Kubernetes LLM deployments) traffic management.

By allowing `AIServiceBackend.BackendRef` to reference GAIE's `InferencePool`, we unlock advanced capabilities like intelligent load balancing with fallbacks while maintaining a unified API.

This strategic enhancement addresses user's needs without disrupting existing implementations, positioning EAIG as a comprehensive solution for all AI traffic management.

**This document is about:**

- Expanding the EAIG project scope to support GAIE
- How it integrates with the existing EAIG APIs
- Proposed implementation details

## Background

[Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io) (GAIE) is a new extension of the Kubernetes Gateway API that addresses serving large language models (LLMs) inside Kubernetes, with particular focus on intelligent load balancing decisions.

Envoy AI Gateway (EAIG) was initially designed to route traffic to different AI providers, serving primarily as an egress solution.

The EAIG MVP features targeted application developers with an egress focus, while the GAIE serves AI platform teams with a Kubernetes ingress focus.

To make EAIG a comprehensive solution for all AI traffic management, this proposal is to extend EAIG to support GAIE.

## Expanding Project Scope

By evolving EAIG's focus beyond routing traffic to external AI providers (egress) to include internal Kubernetes LLM deployments (ingress) through GAIE implementation we position EAIG as a comprehensive traffic management solution for all AI traffic.

Note that this enhancement is an addition to the existing EAIG APIs, and does not compete with existing functionality for the egress use case.

## API Integration Approach

We propose enabling the existing `AIServiceBackend.BackendRef` to reference GAIE's `InferencePool`. This approach allows users to leverage GAIE's advanced features without changing existing APIs. Translation and security policy functionalities will continue to work as designed.

```diff
--- a/api/v1alpha1/api.go
+++ b/api/v1alpha1/api.go
@@ -334,7 +334,7 @@ type AIServiceBackendSpec struct {
        APISchema VersionedAPISchema `json:"schema"`
        // BackendRef is the reference to the Backend resource that this AIServiceBackend corresponds to.
        //
-       // A backend can be of either k8s Service or Backend resource of Envoy Gateway.
+       // A backend can be of either k8s Service, Backend resource of Envoy Gateway, or InferencePool of Gateway API Inference Extension.
        //
        // This is required to be set.
        //
```

This integration ensures a single, unified configuration API layer for both ingress and egress traffic, avoiding the maintenance burden of parallel APIs for different traffic types.

## Implementation Notes

- `BackendRef` pointing to an `InferencePool` will leverage the endpoint picker mechanism described in [GAIE PR #445](https://github.com/kubernetes-sigs/gateway-api-inference-extension/pull/445), using dynamic metadata-based load balancing policies.

- The existing abstraction where extproc resides will remain unchanged, keeping it independent of Kubernetes/control plane specifics while ensuring the `filterapi` layer supports GAIE load balancing policies.

- As a future optimization, we can conditionally disable translation and buffering when an `AIGatewayRoute` references only one `InferencePool`-based `AIServiceBackend` without translation requirements, aligning with GAIE's reference implementation.

## Options considered

One consideration was to implement this in Envoy Gateway directly, however as this is directly related to AI Traffic, we believe Envoy AI Gateway is the best initial home for this enhancement. In the future, this enhancement may be migrated to the Envoy Gateway project, and hence still be available to EAIG users.
