# MCPBackendExtensionPolicy: Envoy Gateway Extensions for MCP Backend Traffic

## Table of Contents

- [Background and Motivation](#background-and-motivation)
- [Current State](#current-state)
  - [How an MCP Request Reaches a Backend](#how-an-mcp-request-reaches-a-backend)
  - [Why Envoy Gateway Policies Can't Reach Hop 2](#why-envoy-gateway-policies-cant-reach-hop-2)
- [Goals](#goals)
- [Solution Overview](#solution-overview)
- [API Proposal](#api-proposal)
  - [Why an MCPBackendExtensionPolicy CRD](#why-an-mcpbackendextensionpolicy-crd)
- [Design](#design)
  - [What agent-router Does](#what-agent-router-does)
  - [Which Filters Reach the Backend Listener](#which-filters-reach-the-backend-listener)
  - [Precedence](#precedence)
  - [Status](#status)
- [Examples](#examples)
  - [Per-Backend Credentials: Token Exchange](#per-backend-credentials-token-exchange)
  - [A Module for Every Other Backend](#a-module-for-every-other-backend)
- [Implementation Details](#implementation-details)
- [Relationship to Other Proposals](#relationship-to-other-proposals)
- [Compatibility](#compatibility)

## Background and Motivation

An MCP request through agent-router makes two hops: from the client to the MCP proxy, and from the MCP proxy to each
backend through an internal **backend listener**. Only the second hop belongs to a single backend, which makes it the
place for anything that differs per backend:

- **Upstream identity.** Sign requests, or exchange the caller's token for one the backend accepts.
- **Traffic shaping.** Rewrite headers, attach audit metadata, or apply limits for one backend only.
- **Guardrails.** Check tool-call arguments before they leave for a third-party MCP server, and check tool results from
  a less trusted server before they reach the agent.

Nothing can attach to that hop today. Users run into this from different directions, for example token exchange
([#2036](https://github.com/theagentrouter/agent-router/issues/2036)) and evidence checks on URL-bearing tool calls
([#2803](https://github.com/theagentrouter/agent-router/issues/2803)). Giving each its own API would make agent-router
grow a feature every time.

Envoy Gateway already provides extensions through `EnvoyExtensionPolicy`. What's missing is a way to point them at
hop 2.

## Current State

### How an MCP Request Reaches a Backend

For each MCPRoute, the controller generates a main HTTPRoute for the MCP proxy and one HTTPRoute per backend
(`ai-eg-mcp-br-<route>-<backend>`), plus an `ai-eg-mcp-brf-...` HTTPRouteFilter for per-backend credentials. Envoy
Gateway translates them onto the Gateway listener. In `PostTranslateModify`, the extension server creates
`aigateway-mcp-backend-listener` and moves the per-backend routes and their filters onto it. The MCP proxy selects a
backend's route with the `x-ai-eg-mcp-backend` header.

```text
client ──▶ Gateway listener ──▶ MCP proxy ──▶ backend listener ──▶ MCP server
            hop 1                               hop 2: one route per backend
```

### Why Envoy Gateway Policies Can't Reach Hop 2

An `EnvoyExtensionPolicy` on a generated per-backend HTTPRoute is accepted, but the extension never runs: the
extension server moves the route to the backend listener and leaves the filter on the Gateway listener, since it only
moves filters whose names contain `ai-eg-mcp-brf-`. And the generated route names are internal, not something users
should target.

## Goals

- Attach Envoy Gateway extensions to the hop-2 traffic of one MCPRoute backend, or of all its backends.
- Start with Envoy Gateway's dynamic modules. Other extension types, such as Lua, ext_proc and Wasm, can be added later.

## Solution Overview

Add a namespaced CRD, `MCPBackendExtensionPolicy`. It targets an MCPRoute, or one of its backends through
`sectionName`, and carries Envoy Gateway's dynamic module extension (`dynamicModule`, with its `backends`) unchanged.

agent-router stays a relay. For each MCPBackendExtensionPolicy it generates one EnvoyExtensionPolicy that selects the
targeted backends' HTTPRoutes, and the extension server copies the resulting filters to the backend listener. Envoy
Gateway does the rest, as it does for any EnvoyExtensionPolicy: validation, callout clusters and failing closed.

```text
MCPBackendExtensionPolicy ──▶ EnvoyExtensionPolicy ──▶ per-backend HTTPRoutes ──▶ filters on the backend listener
  written by the user          generated               labeled and annotated       copied by the extension server
```

## API Proposal

The CRD is `v1alpha1`. Only `targetRefs` is MCP-specific; the rest are Envoy Gateway's types.

```go
// MCPBackendExtensionPolicy attaches Envoy Gateway extensions to the requests the MCP proxy sends to MCPRoute
// backends.
//
// For each policy, AI Gateway generates one EnvoyExtensionPolicy that selects, by label, the per-backend HTTPRoutes
// the policy is in effect for, and copies the resulting filters to the MCP backend listener.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:metadata:labels="gateway.networking.k8s.io/policy=direct"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 239",message="name must be at most 239 characters, as the generated EnvoyExtensionPolicy name adds a 14-character prefix"
type MCPBackendExtensionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the details of MCPBackendExtensionPolicy.
	Spec MCPBackendExtensionPolicySpec `json:"spec"`
	// Status defines the status details of the MCPBackendExtensionPolicy.
	Status MCPBackendExtensionPolicyStatus `json:"status,omitempty"`
}

// MCPBackendExtensionPolicySpec specifies the Envoy Gateway extensions attached to MCPRoute backends.
//
// The extension fields reuse Envoy Gateway's EnvoyExtensionPolicy types unchanged.
type MCPBackendExtensionPolicySpec struct {
	// TargetRefs are the MCPRoutes, in the policy's namespace, that this policy attaches to. SectionName selects one
	// backend by its MCPRoute.spec.backendRefs[].name; without it, the policy applies to all backends of the MCPRoute.
	//
	// When several policies select the same backend, one takes effect, following Gateway API policy attachment
	// (GEP-713): a policy with a sectionName beats one without, then the oldest wins, then the name that sorts first.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(ref, ref.group == 'aigateway.envoyproxy.io' && ref.kind == 'MCPRoute')", message="targetRefs must reference MCPRoute resources"
	TargetRefs []gwapiv1.LocalPolicyTargetReferenceWithSectionName `json:"targetRefs"`
	// Backends declares the callout targets of the extensions.
	// Same type and semantics as EnvoyExtensionPolicy.spec.backends.
	//
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	// +optional
	Backends []egv1a1.ExtensionBackend `json:"backends,omitempty"`
	// DynamicModule is an ordered list of dynamic module filters.
	// Same type and semantics as EnvoyExtensionPolicy.spec.dynamicModule.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	DynamicModule []egv1a1.DynamicModule `json:"dynamicModule"`
}

// MCPBackendExtensionPolicyStatus contains the conditions by the reconciliation result.
type MCPBackendExtensionPolicyStatus struct {
	// Conditions is the list of conditions by the reconciliation result.
	// Currently, at most one condition is set.
	//
	// Known .status.conditions.type are: "Accepted", "NotAccepted".
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### Why an MCPBackendExtensionPolicy CRD

A field on `MCPRouteBackendRef` would be simpler to read, but:

- MCPRoute already has an object-size TODO, and [proposal 011](../011-mcp-backend-crd/proposal.md) is moving backend
  configuration out of it.
- A separate resource is more flexible than overloading the existing MCP APIs.

## Design

### What agent-router Does

1. **The MCPRoute controller picks the MCPBackendExtensionPolicy for each backend** and marks that backend's HTTPRoute
   with a label (`aigateway.envoyproxy.io/mcp-backend-extension-policy`) and an annotation
   (`gateway.envoyproxy.io/ai-gateway-mcp-backend-extension-policy`). The generated EnvoyExtensionPolicy selects the
   label; Envoy Gateway copies the annotation into the route's metadata for the extension server. The controller creates
   the EnvoyExtensionPolicy before switching the label, so a backend never runs without its extension in between.
2. **The MCPBackendExtensionPolicy controller renders one EnvoyExtensionPolicy per MCPBackendExtensionPolicy**,
   `ai-eg-mcp-bre-<name>`. It selects the labeled HTTPRoutes with `targetSelectors`, carries the extension fields
   unchanged, and is owned by its MCPBackendExtensionPolicy, so it is deleted with it.
3. **The extension server copies the MCPBackendExtensionPolicy's dynamic module filters** to the backend listener
   (see below). Copies keep Envoy Gateway's order, so `EnvoyProxy.spec.filterOrder` applies on hop 2 too.

An extension then sees every request the MCP proxy sends to that backend, not only tool calls, and its response.

### Which Filters Reach the Backend Listener

Envoy Gateway applies Gateway-wide policies to every route on a listener, so a per-backend route may also enable
filters of other policies, such as the ext_authz filter of a Gateway-wide SecurityPolicy. The extension server copies a
filter to the backend listener only if all of the following hold:

- **The filter belongs to the route's MCPBackendExtensionPolicy:** the route's metadata carries the
  `ai-gateway-mcp-backend-extension-policy` annotation, and the filter's name contains the generated
  EnvoyExtensionPolicy's `ai-eg-mcp-bre-` prefix, matched the same way as the existing `ai-eg-mcp-brf-` filters.
- **The route enables the filter** in its `typed_per_filter_config`. The generated EnvoyExtensionPolicy selects the
  route directly, by its label, so it replaces any Gateway-wide EnvoyExtensionPolicy on that route.
- **The filter is a dynamic module:** its name starts with `envoy.filters.http.dynamic_modules`, the filter-type prefix
  by which Envoy Gateway also matches `EnvoyProxy.spec.filterOrder`.

### Precedence

An MCPBackendExtensionPolicy can target a whole MCPRoute or one of its backends, so several can select the same
backend. Only one takes effect there, following Gateway API policy attachment
([GEP-713](https://gateway-api.sigs.k8s.io/geps/gep-713/)): one that names the backend in `sectionName` beats one that
targets the whole MCPRoute, then the oldest wins, then the name that sorts first.

### Status

As with the other agent-router policies, the status has one condition, `Accepted` or `NotAccepted`. The message lists,
per MCPRoute, the backends the MCPBackendExtensionPolicy applies to and those where a more specific one won. It is
`NotAccepted` when a targeted MCPRoute or backend doesn't exist, when another MCPBackendExtensionPolicy wins on every
backend of a target, or when Envoy Gateway rejects the generated EnvoyExtensionPolicy, in which case the message
carries Envoy Gateway's reason.

## Examples

Module `config` is defined by each module; agent-router passes it through.

### Per-Backend Credentials: Token Exchange

With the user's `Authorization` header forwarded to the `github` backend (`forwardHeaders`), a token-exchange module
([RFC 8693](https://datatracker.ietf.org/doc/html/rfc8693)) swaps it for a token issued for that backend:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: MCPBackendExtensionPolicy
metadata:
  name: github-token-exchange
  namespace: default
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: MCPRoute
      name: enterprise-mcp
      sectionName: github
  backends:
    - name: sts
      backendRef:
        group: gateway.envoyproxy.io
        kind: Backend
        name: idp
        port: 443
  dynamicModule:
    - name: token-exchange
      filterName: token-exchange
      config:
        extension_backend: sts
        token_exchange_url: idp.example.com/oauth2/token
        client_id: gateway
        audience: github-mcp
```

agent-router labels and annotates `ai-eg-mcp-br-enterprise-mcp-github` and creates:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyExtensionPolicy
metadata:
  name: ai-eg-mcp-bre-github-token-exchange
  namespace: default
  ownerReferences:
    - apiVersion: aigateway.envoyproxy.io/v1alpha1
      kind: MCPBackendExtensionPolicy
      name: github-token-exchange
      controller: true
spec:
  targetSelectors:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      matchLabels:
        aigateway.envoyproxy.io/mcp-backend-extension-policy: github-token-exchange
  # backends and dynamicModule copied from the MCPBackendExtensionPolicy
```

### A Module for Every Other Backend

Without `sectionName`, the MCPBackendExtensionPolicy covers every backend that has no more specific one. Next to the
token-exchange example, this runs a request-checking module on every backend except `github`:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: MCPBackendExtensionPolicy
metadata:
  name: check-tool-calls
  namespace: default
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: MCPRoute
      name: enterprise-mcp
  dynamicModule:
    - name: guardrails
      filterName: tool-call-check
      config:
        deny_tools: [delete_repository]
```

## Implementation Details

- **MCPRoute controller** (`internal/controller/mcp_route.go`): set the label and annotation while syncing the
  per-backend HTTPRoutes. Watch MCPBackendExtensionPolicies, but only creation, deletion and `targetRefs` changes; edits
  to the extensions don't change which one applies where. Before labeling a route, create the matching
  EnvoyExtensionPolicy if it doesn't exist yet, as the controller already does for the shared MCP proxy Backend.
- **MCPBackendExtensionPolicy controller** (`internal/controller/mcp_backend_extension_policy.go`): render the
  EnvoyExtensionPolicy and write status. It watches sibling MCPBackendExtensionPolicies (for precedence), MCPRoutes,
  and the owned EnvoyExtensionPolicy without a generation predicate, so Envoy Gateway's status updates come through.
- **Extension server** (`internal/extensionserver/mcproute.go`): besides moving the `ai-eg-mcp-brf-` filters as
  today, copy the dynamic module filters that annotated per-backend routes enable to the backend listener. This
  follows the existing `ai-gateway-generated` annotation, which the extension server already reads from route
  metadata.
- **MCP proxy:** no change.

Tests:

- **Unit:** the filter copy, precedence, labeling, the generated EnvoyExtensionPolicy, and status.
- **CEL:** each validation rule on the CRD.
- **End to end:** a test dynamic module sets a request header on hop 2, and each backend checks that it receives the
  value set by its effective MCPBackendExtensionPolicy.

## Relationship to Other Proposals

- **[011: MCPBackend CRD](../011-mcp-backend-crd/proposal.md).** Proposal 011 still generates one HTTPRoute per
  `backendRef`, whether it points to an inline `Backend` or an `MCPBackend`. So the label, the annotation, the generated
  EnvoyExtensionPolicy and the extension server stay the same. Supporting `MCPBackend` targets only changes how the
  effective MCPBackendExtensionPolicy is resolved:
  - **API:** the `targetRefs` CEL rule also accepts `kind: MCPBackend`, without `sectionName`.
  - **Precedence:** in `effectiveMCPBackendExtensionPolicies`, a `backendRef` of `kind: MCPBackend` also matches
    MCPBackendExtensionPolicies that target that MCPBackend, ranked between the two existing levels: MCPRoute with
    `sectionName`, then MCPBackend, then MCPRoute.
  - **Lookup and watches:** the targetRefs index is keyed by target kind and name, and an MCPBackendExtensionPolicy
    that targets an MCPBackend is mapped to the MCPRoutes referencing it through proposal 011's `MCPRoute → MCPBackend`
    field index.
  - **Fan-out:** an MCPBackend shared by several MCPRoutes yields one labeled HTTPRoute per MCPRoute, all selected by
    the same generated EnvoyExtensionPolicy, with no additional code.
  - **Status:** the condition message reports, per referencing MCPRoute, whether the MCPBackendExtensionPolicy is in
    effect for that backend.
- **[#2077: MCP content filter](https://github.com/theagentrouter/agent-router/pull/2077).** Complementary: it works on
  decoded JSON-RPC inside the MCP proxy, this works on hop-2 traffic in Envoy.
- **[#2266: filter order around ext_proc](https://github.com/theagentrouter/agent-router/pull/2266).** If it lands,
  the MCPBackendExtensionPolicy controller can copy its annotation to the generated EnvoyExtensionPolicy, which uses
  `targetSelectors` rather than the `targetRefs` that PR reads.

## Compatibility

- Existing MCPRoutes are unaffected: without an MCPBackendExtensionPolicy, the extension server does exactly what it
  does today.
