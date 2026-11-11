# A2A (Agent2Agent) Traffic: `A2ARoute` and `A2ABackend`

## Table of Contents

1. [Background and Motivation](#background-and-motivation)
2. [A2A Protocol Overview for Gateways](#a2a-protocol-overview-for-gateways)
3. [Current State](#current-state)
   - 3.1 [Envoy's A2A Filter](#envoys-a2a-filter)
   - 3.2 [Agent Router Building Blocks](#agent-router-building-blocks)
4. [Goals and Non-Goals](#goals-and-non-goals)
5. [Design](#design)
   - D1 [Data Plane: Envoy's A2A Filter and RBAC](#d1--data-plane-envoys-a2a-filter-and-rbac)
   - D2 [Resources: `A2ARoute` and `A2ABackend`](#d2--resources-a2aroute-and-a2abackend)
   - D3 [Exposing an Agent](#d3--exposing-an-agent)
   - D4 [Agent Card](#d4--agent-card)
   - D5 [Scoping the Filters to A2A Routes](#d5--scoping-the-filters-to-a2a-routes)
   - D6 [Authentication](#d6--authentication)
   - D7 [Method-Level Authorization](#d7--method-level-authorization)
   - D8 [Protocol Validation and Body Size](#d8--protocol-validation-and-body-size)
   - D9 [Timeouts](#d9--timeouts)
   - D10 [Observability](#d10--observability)
   - D11 [`aigw run`](#d11--aigw-run)
6. [API Proposal](#api-proposal)
7. [Translation to Envoy Configuration](#translation-to-envoy-configuration)
8. [Implementation Details](#implementation-details)
9. [Alternatives Considered](#alternatives-considered)
10. [Testing](#testing)
11. [Limitations](#limitations)

## Background and Motivation

[A2A] is an open protocol for agents to discover each other and exchange tasks. Agent Router already routes the other two kinds of agent traffic: model calls through `AIGatewayRoute` and tool calls through `MCPRoute`. Users who run all three through one gateway have no API for the third ([#2070]).

The only option today is the [A2A preview guide][preview], which wires Envoy's built-in A2A filter in by hand. As a running example throughout this proposal, take a `research` agent served by `Service/research-agent` on port 8080, to be reached at `https://agents.example.com/research`. Following the guide, the operator writes and maintains:

- an `HTTPRoute` with one rule for the agent card and one for the JSON-RPC endpoint, with request timeouts raised so that streaming responses are not cut at Envoy's 15s default;
- an `EnvoyPatchPolicy` that inserts `envoy.filters.http.a2a` into the listener by its xDS name;
- a `SecurityPolicy` on the JSON-RPC rule only, so that the card stays public;
- an `EnvoyProxy` access log format that reads the filter's dynamic metadata.

This has four problems:

1. **Raw xDS patching.** `EnvoyPatchPolicy` has to be enabled in Envoy Gateway, and anyone allowed to create one can inject arbitrary proxy configuration. The patch also depends on Envoy Gateway's internal listener naming.
2. **The filter applies to the whole listener.** Once inserted, it buffers and parses every JSON `POST` on the listener, so the guide recommends a dedicated Gateway for A2A traffic, or a second patch that disables the filter everywhere except the A2A routes.
3. **The card points around the gateway.** An agent's own card advertises the address the agent knows, typically its in-cluster URL. Clients that follow it bypass the gateway unless the operator hand-writes a card and serves it from the gateway.
4. **No method-level authorization.** The filter extracts the JSON-RPC method, but no API exposes it to authorization. Restricting which A2A operations a caller may invoke means hand-writing RBAC configuration through the same patch mechanism.

This proposal adds `A2ARoute` and `A2ABackend`. The running example becomes two short resources, `EnvoyPatchPolicy` is no longer needed, the filter is scoped to A2A routes, and method-level authorization becomes part of the API. For the card, serving an operator-provided card becomes part of the API; rewriting the agent's own card is out of scope (see [Non-Goals](#non-goals)).

## A2A Protocol Overview for Gateways

This section summarizes the parts of the [A2A specification][spec] (v1.0.1) that a gateway sees.

- **Discovery.** An agent publishes an Agent Card at `https://{server_domain}/.well-known/agent-card.json` (§8.2). Clients can also obtain cards from registries or by direct configuration. The card lists `supportedInterfaces`, each with a `url` and a `protocolBinding`, the `securitySchemes` the agent accepts, and the `securityRequirements` a client must meet. A card may carry JWS `signatures` over its canonical form (§8.4), so any change to a signed card invalidates it.
- **Bindings.** A2A defines JSON-RPC 2.0, gRPC, and HTTP+JSON/REST bindings with equivalent operations (§5). In the JSON-RPC binding (§9), every operation is a `POST` to the single URL taken from the card, with `Content-Type: application/json`; `application/a2a+json` is registered as the A2A media type (§14.1). `SendStreamingMessage` and `SubscribeToTask` respond with a Server-Sent Events stream.
- **Versions.** Clients send `A2A-Version: <major.minor>`, and an empty value means 0.3 (§3.6). Version 1.0 renamed every JSON-RPC method:

  | 1.0 (§5.3)                         | 0.3 ([v0.3.0 §3.5.6][spec-v03])       |
  | ---------------------------------- | ------------------------------------- |
  | `SendMessage`                      | `message/send`                        |
  | `SendStreamingMessage`             | `message/stream`                      |
  | `GetTask`                          | `tasks/get`                           |
  | `ListTasks`                        | `tasks/list`                          |
  | `CancelTask`                       | `tasks/cancel`                        |
  | `SubscribeToTask`                  | `tasks/resubscribe`                   |
  | `CreateTaskPushNotificationConfig` | `tasks/pushNotificationConfig/set`    |
  | `GetTaskPushNotificationConfig`    | `tasks/pushNotificationConfig/get`    |
  | `ListTaskPushNotificationConfigs`  | `tasks/pushNotificationConfig/list`   |
  | `DeleteTaskPushNotificationConfig` | `tasks/pushNotificationConfig/delete` |
  | `GetExtendedAgentCard`             | `agent/getAuthenticatedExtendedCard`  |

  The 0.3 specification lists `tasks/list` for the gRPC and REST bindings only.

- **Long-running work.** Work is tracked as tasks. Streams are not a reliable delivery mechanism (§3.7): a client that loses a stream reads the task's current state with `GetTask` or resumes updates for a non-terminal task with `SubscribeToTask`, which returns the current `Task` first (§3.1.3, §3.1.6).
- **Push notifications.** An agent can `POST` task updates to a webhook the client registered (§3.5.3). That request goes from the agent to the client and does not pass through the route that exposes the agent.
- **Extended card.** `GetExtendedAgentCard` (§3.1.11) is an ordinary JSON-RPC method that returns more detail to authenticated clients, so it is subject to the JSON-RPC endpoint's authentication and authorization.

## Current State

### Envoy's A2A Filter

Envoy has shipped [`envoy.filters.http.a2a`][a2a-filter] since v1.38.0, with its API marked work-in-progress. Agent Router currently requires Envoy Gateway v1.9.2 or later, which ships Envoy v1.39. The filter's behavior at that version:

- For a `POST` with `Content-Type` `application/json` or `application/a2a+json`, it buffers the body up to `max_request_body_size` (default 8 KiB, at most 10 MiB) and parses the JSON-RPC envelope. A body over the limit gets `413` (since v1.39.0).
- It writes `jsonrpc`, `method`, and `id` to dynamic metadata under the filter's configured name, plus method-specific fields for the 0.3 method names. The `method` value is the string from the request, so 1.0 and 0.3 clients produce different values for the same operation.
- With `traffic_mode: REJECT`, a request that is not a JSON-RPC 2.0 envelope gets `400`.
- It has no per-route configuration, so `traffic_mode` and `max_request_body_size` are shared by every route on a listener. Like any HTTP filter, it can be inserted disabled and enabled per route.
- `parser_config` and `storage_mode` are accepted but not used: the filter always uses its default parser and always writes dynamic metadata.

### Agent Router Building Blocks

- **Per-route filters.** The extension server already inserts the AI Gateway `ext_proc` filter disabled at the listener and enables it only on routes that Agent Router generated (`insertRouterLevelAIGatewayExtProc` and `enableRouterLevelAIGatewayExtProcOnRoute` in `internal/extensionserver/post_translate_modify.go`).
- **Generated routes.** `MCPRoute` is reconciled into generated `HTTPRoute`s and a `SecurityPolicy`. Because that `SecurityPolicy` targets a whole `HTTPRoute`, the extension server removes the authentication filters from the generated routes that must not require credentials (`maybeUpdateMCPRoutes` in `internal/extensionserver/mcproute.go`).
- **Ordered authorization in RBAC.** Envoy Gateway compiles `SecurityPolicy` authorization rules into the RBAC filter's matcher tree: one field matcher per rule in order, `on_no_match` for the default action, and a `DynamicMetadataInput` to read the JWT payload that `jwt_authn` stores under `envoy.filters.http.jwt_authn`, keyed by provider name.
- **`aigw run`.** `aigw run` and `aigw translate` reconcile `MCPRoute` with the real controller against a fake client (`cmd/aigw/translate.go`).
- **Backend CRDs.** `AIServiceBackend` references an Envoy Gateway `Backend` only, and Kubernetes `Service` support is tracked in [#902]. [Proposal 011](../011-mcp-backend-crd/proposal.md) defines `MCPBackend` the same way.

## Goals and Non-Goals

### Goals

- Expose one agent with one `A2ARoute`, under a hostname and a path prefix, with every Gateway API and Envoy Gateway resource generated by the controller.
- Use Envoy's A2A filter and RBAC filter as the data plane, with no new component, no `EnvoyPatchPolicy`, and no scripting filter.
- Enable the A2A filter only on A2A routes, leaving `AIGatewayRoute` and `MCPRoute` traffic on the same listener unchanged.
- Keep clients on the gateway for discovery, by passing the agent's card through or serving an operator-provided card.
- Authenticate clients on the JSON-RPC endpoint with JWT, API key, or external authorization, while the card stays public.
- Authorize by A2A method using 1.0 method names, with every rule also matching the 0.3 name of the same operation.
- Reject requests that are not JSON-RPC 2.0 and bound the request body size.
- Support long-lived streams with configurable request and idle timeouts.
- Make the A2A method and JSON-RPC request ID available to access logs.
- Support `A2ARoute` in `aigw run`.
- Keep the API extensible, so that the items under Non-Goals can be added without breaking changes.

### Non-Goals

- This proposal does **not** rewrite the agent's own card. Rewriting requires processing the response body, and it invalidates signed cards (§8.4); both need their own design.
- This proposal does **not** record response attributes such as task state or JSON-RPC error codes, and does not add A2A metrics. Both need response processing.
- This proposal does **not** add CEL to authorization rules. Method and JWT rules cover the A2A-specific need, and a `cel` field can be added to the rule later.
- This proposal does **not** add upstream authentication to agents. `BackendSecurityPolicy` can be extended to target `A2ABackend`, as proposal 011 does for `MCPBackend`.
- This proposal does **not** put more than one agent behind one route, aggregate cards, or discover agents by label.
- This proposal does **not** support the gRPC or HTTP+JSON/REST bindings. The A2A filter parses JSON-RPC only.
- This proposal does **not** cover push notifications, which go from the agent to the client.
- This proposal does **not** reference Kubernetes `Service` directly from `A2ABackend`; that follows [#902] for all backend CRDs.
- This proposal does **not** inspect message content. Content checks need to understand A2A's nested `params.message.parts`, which is separate work.
- This proposal does **not** support A2A versions before 0.3, which used `/.well-known/agent.json` for discovery.

## Design

### D1 — Data Plane: Envoy's A2A Filter and RBAC

The MCP gateway runs its own proxy because MCP needs session handling and the merging of notification streams from several servers ([proposal 006](../006-mcp-gateway/proposal.md)). An `A2ARoute` fronts a single agent: the gateway forwards each request body unchanged and only needs to read the JSON-RPC envelope, which Envoy's A2A filter already does. Authorization on the parsed method is a dynamic metadata match, which the RBAC filter already does.

On every listener that carries an A2A route, the extension server inserts two filters immediately before the router, both disabled by default:

1. `envoy.filters.http.a2a`, which validates the envelope and writes `method` and `id` to dynamic metadata.
2. `envoy.filters.http.rbac/a2a`, which evaluates the route's authorization rules against that metadata and the JWT payload.

Both run after the authentication filters that Envoy Gateway generates from `SecurityPolicy`, so the RBAC filter sees the verified JWT payload. The A2A filter pauses the filter chain until it has parsed the body, so the method is in metadata when the RBAC filter runs.

### D2 — Resources: `A2ARoute` and `A2ABackend`

- `A2ARoute` says where and how an agent is exposed: Gateways, hostnames, path, card handling, timeouts, authentication, and authorization.
- `A2ABackend` says how to reach the agent: an Envoy Gateway `Backend`, the agent's JSON-RPC path, and the path of its own card.

An `A2ARoute` exposes one agent. `backendRefs` is a list limited to one entry, so that it can later carry several backends serving the same agent without an API change. Separate agents are separate `A2ARoute`s, since each has its own card.

`A2ARoute` references `A2ABackend` rather than embedding a backend reference, following the direction of [proposal 011](../011-mcp-backend-crd/proposal.md): a backend resource is where `BackendSecurityPolicy` can attach when upstream authentication is added.

### D3 — Exposing an Agent

An agent is exposed under `hostnames` and a path prefix `path` (default `/`):

| Request                                  | Generated route                                                         |
| ---------------------------------------- | ----------------------------------------------------------------------- |
| `GET <path>/.well-known/agent-card.json` | Card route: passed through to the agent or served by the gateway ([D4]) |
| `POST <path>` (exact match)              | JSON-RPC route: rewritten to `A2ABackend.path` and forwarded            |

In the running example, `path: /research` serves the card at `/research/.well-known/agent-card.json` and the JSON-RPC endpoint at `/research`. With the default `path: /`, the card is at `/.well-known/agent-card.json` and the endpoint at `/`. Several agents can share one hostname under different paths, or use one hostname each with the default path.

The controller generates the card route and the JSON-RPC route as two separate `HTTPRoute`s. A `SecurityPolicy` attaches to a whole `HTTPRoute` unless it names a rule through `sectionName`. The `MCPRoute` controller avoids `sectionName` because some Gateway API implementations do not support rule names, and instead strips the authentication filters from public routes in the extension server. Two `HTTPRoute`s need neither: the generated `SecurityPolicy` targets the JSON-RPC route, and the card route has none.

### D4 — Agent Card

`A2ARoute.agentCard.mode` selects how the card route is served:

- **`Passthrough`** (default): the card route forwards to the agent, with the path rewritten to `A2ABackend.agentCardPath` (default `/.well-known/agent-card.json`, per §8.2). Use it when the agent already advertises its public URL. `agentCardPath` covers agents that serve their card elsewhere, for example several agents in one process.
- **`Static`**: the gateway responds with the card from `agentCard.static`, inline or from a ConfigMap, through an Envoy Gateway `HTTPRouteFilter` direct response with `Content-Type: application/json`. The operator writes the public URL into `supportedInterfaces`, and may sign the card.

The card content lives on `A2ARoute` because it contains the public URL, which the route's hostnames and path determine. `agentCardPath` lives on `A2ABackend` because it is a property of the agent.

When `mode` is `Passthrough` and `path` is not `/`, the agent's card can only be correct if the agent advertises the gateway's URL including the prefix. The controller notes this in the `Accepted` condition message, without rejecting the route.

A future `Rewrite` mode can be added to the enum without changing the existing fields.

### D5 — Scoping the Filters to A2A Routes

The extension server enables the A2A filter, and the RBAC filter when `authorization` is set, only on routes generated from an `A2ARoute`'s JSON-RPC route, through `typed_per_filter_config`, the same mechanism it uses for the AI Gateway `ext_proc` filter. Other routes on the listener, including `AIGatewayRoute` and `MCPRoute` routes, are not affected.

The card route does not enable the A2A filter. A card request has no body to parse, and after an unreleased change on Envoy's main branch, `REJECT` mode rejects any `GET` whose path is not exactly `/.well-known/agent-card.json`, which includes a card served under a path prefix.

### D6 — Authentication

`A2ARoute.securityPolicy` has `jwt`, `apiKeyAuth`, `extAuth`, and `mergeType`, using the Envoy Gateway types directly. The controller generates one `SecurityPolicy` targeting the JSON-RPC `HTTPRoute`. A2A clients present standard credentials and learn what to send from the card's `securitySchemes` and `securityRequirements`, so no A2A-specific discovery endpoint is involved.

`GetExtendedAgentCard` is a JSON-RPC method, so it is authenticated like any other call.

### D7 — Method-Level Authorization

`A2ARoute.securityPolicy.authorization` has a `defaultAction` (default `Deny`) and ordered `rules`. The first matching rule applies. A rule matches when all of its conditions match, and an omitted condition matches everything:

- `source.jwt`: required scopes and claims (`JWTSource`, the type `MCPRoute` uses). Requires `securityPolicy.jwt`.
- `target.methods`: A2A operations, by their 1.0 names.
- `action`: `Allow` (default) or `Deny`.

The extension server compiles the rules into the RBAC filter's matcher, the same structure Envoy Gateway uses for `SecurityPolicy` authorization:

- Each method becomes a match on `envoy.filters.http.a2a` metadata key `method` against the 1.0 name **or** its 0.3 name from the table above. A rule written as `CancelTask` therefore also matches `tasks/cancel`, and a deny rule cannot be bypassed by sending the other version's name.
- JWT scopes and claims are matched in `envoy.filters.http.jwt_authn` metadata under each configured provider's name, as Envoy Gateway does, and a rule matches if any provider's payload satisfies it.
- When `authorization` is set, the compiled matcher starts with an implicit rule that denies requests without a `method`. The A2A filter accepts a JSON-RPC response object (no `method`) as valid JSON-RPC, and such a request would otherwise skip every method rule and reach `defaultAction`.

### D8 — Protocol Validation and Body Size

The filter runs in `REJECT` mode, which is not configurable. In `PASS_THROUGH` mode a body that is not JSON-RPC carries no `method`, and authorization would depend on the implicit rule alone. Since the filter only runs on A2A JSON-RPC routes, `REJECT` affects no other traffic.

`max_request_body_size` is set per Gateway through a new field, `GatewayConfig.spec.a2a.maxRequestBodySizeBytes` (default 1 MiB instead of the filter's 8 KiB, at most the filter's 10 MiB). It cannot live on `A2ARoute`: the filter has no per-route configuration, and inserting one filter per distinct value would change the metadata namespace, which is the filter's name ([D10]).

### D9 — Timeouts

Envoy's route timeout covers the whole response, including a stream, and defaults to 15s. `A2ARoute` has:

- `timeouts` (Gateway API `HTTPRouteTimeouts`), applied to the JSON-RPC `HTTPRoute` as written. If the field is unset, the controller sets `request` and `backendRequest` to `30m`, the values `MCPRoute` uses.
- `streamIdleTimeout`, which ends a stream when the agent sends nothing for that long. The extension server sets it as `retry_policy.per_try_idle_timeout`, as it does for `AIGatewayRoute.streamIdleTimeout`.

A client whose stream ends can read the task with `GetTask` or, if the task is not in a terminal state, resume updates with `SubscribeToTask` (§3.1.3, §3.1.6). Operators with long-running tasks can raise `timeouts.request` and bound stalled streams with `streamIdleTimeout`.

### D10 — Observability

The A2A filter writes its metadata under its filter name, so the filter is named `envoy.filters.http.a2a`: access log formats written for the preview guide keep working. The documentation recommends:

```yaml
a2a.method: "%DYNAMIC_METADATA(envoy.filters.http.a2a:method)%"
jsonrpc.request.id: "%DYNAMIC_METADATA(envoy.filters.http.a2a:id)%"
```

`jsonrpc.request.id` matches the key the [access log documentation](../../../site/docs/capabilities/observability/accesslogs.md) uses for MCP. The filter's counters `requests_rejected`, `invalid_json`, and `body_too_large` are available as Envoy stats.

### D11 — `aigw run`

`aigw run` and `aigw translate` collect `A2ARoute` and `A2ABackend` from the input and reconcile them with the `A2ARoute` controller against the fake client, as they do for `MCPRoute`. The extension server identifies A2A routes by the `ai-eg-a2a-` name prefix in both modes, as it identifies MCP routes. In standalone mode, where it cannot read annotations and treats every route except the MCP ones as generated by `AIGatewayRoute`, it also uses that prefix to keep the AI Gateway `ext_proc` filter off A2A routes.

[D4]: #d4--agent-card
[D10]: #d10--observability

## API Proposal

### `A2ABackend` (`v1alpha1`)

```go
// A2ABackend represents one A2A agent upstream.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
type A2ABackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              A2ABackendSpec   `json:"spec,omitempty"`
	Status            A2ABackendStatus `json:"status,omitempty"`
}

type A2ABackendSpec struct {
	// BackendRef references the Envoy Gateway Backend that serves the agent.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="has(self.kind) && self.kind == 'Backend' && has(self.group) && self.group == 'gateway.envoyproxy.io'",message="must reference an Envoy Gateway Backend"
	BackendRef gwapiv1.BackendObjectReference `json:"backendRef"`

	// Path is the path of the agent's JSON-RPC endpoint.
	//
	// +kubebuilder:default:=/
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/')",message="path must start with '/'"
	// +optional
	Path *string `json:"path,omitempty"`

	// AgentCardPath is the path of the agent's own card. It is used when the A2ARoute
	// passes the card through.
	//
	// +kubebuilder:default:=/.well-known/agent-card.json
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/')",message="agentCardPath must start with '/'"
	// +optional
	AgentCardPath *string `json:"agentCardPath,omitempty"`
}

type A2ABackendStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### `A2ARoute` (`v1alpha1`)

```go
// A2ARoute exposes one A2A agent through a Gateway.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[-1:].type`
type A2ARoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              A2ARouteSpec   `json:"spec,omitempty"`
	Status            A2ARouteStatus `json:"status,omitempty"`
}

type A2ARouteSpec struct {
	// ParentRefs are the Gateways this route attaches to. They must be in the same
	// namespace as the A2ARoute.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self.all(match, match.kind == 'Gateway')", message="only Gateway is supported"
	ParentRefs []gwapiv1.ParentReference `json:"parentRefs"`

	// Hostnames are matched against the Host header, as in HTTPRouteSpec.
	//
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Hostnames []gwapiv1.Hostname `json:"hostnames,omitempty"`

	// Path is the prefix the agent is exposed under. The JSON-RPC endpoint is Path,
	// and the card is served at Path followed by "/.well-known/agent-card.json"
	// ("/.well-known/agent-card.json" when Path is "/").
	//
	// +kubebuilder:default:=/
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/') && (self == '/' || !self.endsWith('/'))",message="path must start with '/' and must not end with '/'"
	// +optional
	Path *string `json:"path,omitempty"`

	// BackendRefs references the A2ABackend that serves the agent.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	BackendRefs []A2ARouteBackendRef `json:"backendRefs"`

	// AgentCard configures how the card is served.
	//
	// +optional
	AgentCard *A2AAgentCard `json:"agentCard,omitempty"`

	// Timeouts applies to the JSON-RPC endpoint as written. If unset, Request and
	// BackendRequest are set to 30m.
	//
	// +optional
	Timeouts *gwapiv1.HTTPRouteTimeouts `json:"timeouts,omitempty"`

	// StreamIdleTimeout ends a response when the agent sends no bytes for this long.
	// If unset, no idle timeout applies.
	//
	// +optional
	StreamIdleTimeout *gwapiv1.Duration `json:"streamIdleTimeout,omitempty"`

	// SecurityPolicy configures authentication and authorization of the JSON-RPC
	// endpoint. The card is not protected.
	//
	// +optional
	SecurityPolicy *A2ARouteSecurityPolicy `json:"securityPolicy,omitempty"`
}

type A2ARouteBackendRef struct {
	// Name of the A2ABackend in the same namespace.
	//
	// +kubebuilder:validation:Required
	Name gwapiv1.ObjectName `json:"name"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.mode) && self.mode == 'Static') == has(self.static)",message="static must be set if and only if mode is Static"
type A2AAgentCard struct {
	// Mode selects how the card route is served.
	//
	// +kubebuilder:default:=Passthrough
	// +optional
	Mode A2AAgentCardMode `json:"mode,omitempty"`

	// Static is the card served when Mode is Static, inline or from a ConfigMap.
	// For a ConfigMap, the value of key "response.body" is used; if that key is
	// absent, the first value is used.
	//
	// +optional
	Static *egv1a1.CustomResponseBody `json:"static,omitempty"`
}

// +kubebuilder:validation:Enum=Passthrough;Static
type A2AAgentCardMode string

const (
	A2AAgentCardModePassthrough A2AAgentCardMode = "Passthrough"
	A2AAgentCardModeStatic      A2AAgentCardMode = "Static"
)

// +kubebuilder:validation:XValidation:rule="!has(self.authorization) || !has(self.authorization.rules) || !self.authorization.rules.exists(r, has(r.source)) || has(self.jwt)",message="jwt must be configured when an authorization rule has a jwt source"
type A2ARouteSecurityPolicy struct {
	// +optional
	JWT *egv1a1.JWT `json:"jwt,omitempty"`

	// +optional
	APIKeyAuth *egv1a1.APIKeyAuth `json:"apiKeyAuth,omitempty"`

	// +optional
	ExtAuth *egv1a1.ExtAuth `json:"extAuth,omitempty"`

	// +optional
	Authorization *A2ARouteAuthorization `json:"authorization,omitempty"`

	// MergeType has the same meaning as in MCPRouteSecurityPolicy.
	//
	// +optional
	MergeType *egv1a1.MergeType `json:"mergeType,omitempty"`
}

type A2ARouteAuthorization struct {
	// DefaultAction applies when no rule matches. Defaults to Deny.
	//
	// +optional
	DefaultAction *egv1a1.AuthorizationAction `json:"defaultAction,omitempty"`

	// Rules are evaluated in order; the first matching rule applies.
	//
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Rules []A2ARouteAuthorizationRule `json:"rules,omitempty"`
}

type A2ARouteAuthorizationRule struct {
	// Source matches the caller. If unset, every caller matches.
	//
	// +optional
	Source *A2AAuthorizationSource `json:"source,omitempty"`

	// Target matches the operation. If unset, every operation matches.
	//
	// +optional
	Target *A2AAuthorizationTarget `json:"target,omitempty"`

	// Action applies when the rule matches. Defaults to Allow.
	//
	// +optional
	Action *egv1a1.AuthorizationAction `json:"action,omitempty"`
}

type A2AAuthorizationSource struct {
	// +kubebuilder:validation:Required
	JWT JWTSource `json:"jwt"`
}

type A2AAuthorizationTarget struct {
	// Methods are A2A 1.0 JSON-RPC method names. Each also matches the 0.3 name of
	// the same operation.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=11
	// +listType=set
	Methods []A2AMethod `json:"methods"`
}

// +kubebuilder:validation:Enum=SendMessage;SendStreamingMessage;GetTask;ListTasks;CancelTask;SubscribeToTask;CreateTaskPushNotificationConfig;GetTaskPushNotificationConfig;ListTaskPushNotificationConfigs;DeleteTaskPushNotificationConfig;GetExtendedAgentCard
type A2AMethod string

type A2ARouteStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

### `GatewayConfig` (`v1beta1`)

```go
type GatewayConfigSpec struct {
	// ... existing fields ...

	// A2A configures the A2A filter on this Gateway's listeners.
	//
	// +optional
	A2A *GatewayConfigA2A `json:"a2a,omitempty"`
}

type GatewayConfigA2A struct {
	// MaxRequestBodySizeBytes is the largest JSON-RPC request body, in bytes, that the
	// A2A filter parses. Larger requests are rejected with 413.
	//
	// +kubebuilder:default:=1048576
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10485760
	// +optional
	MaxRequestBodySizeBytes *uint32 `json:"maxRequestBodySizeBytes,omitempty"`
}
```

### Full Configuration Example

The running example, with a static card and method-level authorization:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: research-agent
  namespace: default
spec:
  endpoints:
    - fqdn:
        hostname: research-agent.default.svc.cluster.local
        port: 8080
---
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: A2ABackend
metadata:
  name: research
  namespace: default
spec:
  backendRef:
    group: gateway.envoyproxy.io
    kind: Backend
    name: research-agent
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: research-agent-card
  namespace: default
data:
  response.body: |
    {
      "name": "Research Agent",
      "description": "Summarises research papers",
      "version": "1.0.0",
      "supportedInterfaces": [
        {"url": "https://agents.example.com/research", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}
      ],
      "capabilities": {"streaming": true},
      "securitySchemes": {"bearer": {"httpAuthSecurityScheme": {"scheme": "Bearer"}}},
      "securityRequirements": [{"schemes": {"bearer": {"list": []}}}],
      "defaultInputModes": ["text/plain"],
      "defaultOutputModes": ["text/plain"],
      "skills": []
    }
---
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: A2ARoute
metadata:
  name: research
  namespace: default
spec:
  parentRefs:
    - name: ai-gateway
      kind: Gateway
      group: gateway.networking.k8s.io
  hostnames:
    - agents.example.com
  path: /research
  backendRefs:
    - name: research
  agentCard:
    mode: Static
    static:
      type: ValueRef
      valueRef:
        group: ""
        kind: ConfigMap
        name: research-agent-card
  timeouts:
    request: 2h
  streamIdleTimeout: 5m
  securityPolicy:
    jwt:
      providers:
        - name: corp-idp
          issuer: https://idp.example.com
          remoteJWKS:
            uri: https://idp.example.com/.well-known/jwks.json
    authorization:
      defaultAction: Deny
      rules:
        - source:
            jwt:
              scopes: ["research.write"]
          target:
            methods: [SendMessage, SendStreamingMessage, CancelTask]
        - target:
            methods: [GetTask, ListTasks, SubscribeToTask, GetExtendedAgentCard]
```

## Translation to Envoy Configuration

For the example above, the controller generates:

| Resource                                    | Content                                                                                             |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `HTTPRoute` `ai-eg-a2a-card-research`       | `GET` exact `/research/.well-known/agent-card.json` → `ExtensionRef` to the `HTTPRouteFilter` below |
| `HTTPRouteFilter` `ai-eg-a2a-card-research` | `directResponse`, status 200, `contentType: application/json`, body from `agentCard.static`         |
| `HTTPRoute` `ai-eg-a2a-rpc-research`        | `POST` exact `/research` → `URLRewrite` to `/` → `Backend/research-agent`; `timeouts.request: 2h`   |
| `SecurityPolicy` `ai-eg-a2a-research`       | `jwt` from `securityPolicy.jwt`, targeting `HTTPRoute/ai-eg-a2a-rpc-research`                       |

With `mode: Passthrough`, the card `HTTPRoute` instead rewrites the path to `agentCardPath` and forwards to the backend, and no `HTTPRouteFilter` is generated.

In `PostTranslateModify`, the extension server:

1. Inserts `envoy.filters.http.a2a` (`traffic_mode: REJECT`, `max_request_body_size` from `GatewayConfig`) and `envoy.filters.http.rbac/a2a`, both `disabled: true`, immediately before the router on each listener that has an A2A route.
2. On each xDS route generated from an `ai-eg-a2a-rpc-` `HTTPRoute`, sets `typed_per_filter_config` to enable `envoy.filters.http.a2a`, and, when `authorization` is set, an `RBACPerRoute` with the compiled matcher. It reads the owning `A2ARoute` through its client, as it does for `AIGatewayRoute.streamIdleTimeout`.
3. Sets `retry_policy.per_try_idle_timeout` on the same routes when `streamIdleTimeout` is set.

The compiled matcher for the example, in rule order:

| #   | Predicate                                                                                                                                                            | Action |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------ |
| 0   | `method` absent (implicit)                                                                                                                                           | DENY   |
| 1   | JWT scope `research.write` **and** `method` ∈ {`SendMessage`, `message/send`, `SendStreamingMessage`, `message/stream`, `CancelTask`, `tasks/cancel`}                | ALLOW  |
| 2   | `method` ∈ {`GetTask`, `tasks/get`, `ListTasks`, `tasks/list`, `SubscribeToTask`, `tasks/resubscribe`, `GetExtendedAgentCard`, `agent/getAuthenticatedExtendedCard`} | ALLOW  |
| —   | no match (`defaultAction`)                                                                                                                                           | DENY   |

A JSON-RPC request then flows through:

```
client ── POST /research ──► Envoy listener
  envoy.filters.http.jwt_authn   verify the bearer token, store its payload in metadata
  envoy.filters.http.a2a         parse the envelope; 400 if not JSON-RPC, 413 if too large;
                                 store method and id in metadata
  envoy.filters.http.rbac/a2a    evaluate the matcher above; 403 on deny
  envoy.filters.http.router      rewrite /research → /, forward to Backend/research-agent
```

## Implementation Details

### Control Plane

1. **API**: `A2ARoute` and `A2ABackend` in `api/v1alpha1`, `GatewayConfigA2A` in `api/v1beta1`; generated DeepCopy, CRDs in the `ai-gateway-crds-helm` chart, and API docs.
2. **`A2ARouteController`**: reconciles each `A2ARoute` into the `HTTPRoute`s, `HTTPRouteFilter`, and `SecurityPolicy` above, owned by the `A2ARoute`, and sets `Accepted` or `NotAccepted`. A field index from `A2ARoute` to the `A2ABackend` names it references re-queues routes when an `A2ABackend` changes.
3. **Gateway sync**: changes to `A2ARoute` re-sync the parent Gateways, as `MCPRoute` changes do, so the extension server sees the current state.

### Extension Server

4. **Filter insertion and per-route enablement**, as in [Translation](#translation-to-envoy-configuration), next to the existing `ext_proc` insertion in `internal/extensionserver/post_translate_modify.go`.
5. **Authorization compiler**: `A2ARouteAuthorization` to an RBAC matcher, with the 1.0 to 0.3 method table and the implicit missing-method rule.
6. **Standalone mode**: route identification by the `ai-eg-a2a-` prefix ([D11]).

### `aigw`

7. `A2ARoute` and `A2ABackend` handling in `collectObjects` and `translateCustomResourceObjects` (`cmd/aigw/translate.go`).

### Delivery

The work lands in four pull requests, each usable on its own:

1. API, controller, generated routes, card handling, timeouts, and authentication.
2. A2A filter insertion and per-route enablement, `GatewayConfig.spec.a2a`, and the access log documentation.
3. Method-level authorization.
4. `aigw run` support, and the A2A capability page rewritten around `A2ARoute`.

[D11]: #d11--aigw-run

## Alternatives Considered

**An A2A proxy in the AI Gateway sidecar, like the MCP proxy.** Rejected: the MCP proxy exists for session handling and stream aggregation across servers ([proposal 006](../006-mcp-gateway/proposal.md)). An `A2ARoute` fronts one agent, and Envoy already parses the A2A envelope.

**A plain `HTTPRoute` plus an A2A policy resource.** The operator writes the routes and attaches a policy for A2A behavior and authorization. Rejected: the operator still writes the card and JSON-RPC rules, the timeouts, and the path rewrites for every agent, which is most of the work this proposal removes, and the configuration of one agent is spread across several resources.

**An inline backend reference in `A2ARoute`, as in `MCPRouteBackendRef` today.** Rejected: proposal 011 moves MCP from inline references to a backend resource. A new route type starts with the backend resource and avoids a later migration.

**A separate listener for A2A traffic.** Either a dedicated Gateway, as the preview guide recommends, or an internal listener that A2A routes forward to. Rejected: the filter can be enabled per route on the existing listener, which needs no extra listener and no extra hop.

**`PASS_THROUGH` mode.** Rejected: a body that is not JSON-RPC would reach the agent without a parsed method, so authorization would rely on the implicit rule alone ([D8](#d8--protocol-validation-and-body-size)).

**A request body limit on `A2ARoute`.** Rejected: the filter has no per-route configuration ([D8](#d8--protocol-validation-and-body-size)).

**Fetching and rewriting the agent's card in the controller.** Rejected: the controller would need network access to every agent, the served card would be stale between fetches, and rewriting invalidates signed cards.

**Method authorization through the generated `SecurityPolicy`.** Envoy Gateway v1.9 accepts CEL in `SecurityPolicy` authorization rules, and RBAC CEL can read the A2A filter's metadata. Rejected: CEL rules are not available in Envoy Gateway v1.8, which CI still covers, and the A2A filter would have to run before Envoy Gateway's RBAC filter, which ties the design to Envoy Gateway's internal filter order.

**Raw method strings in rules.** Rejected: a rule naming `CancelTask` would not stop a 0.3 client sending `tasks/cancel`.

**A switch that disables timeouts for streaming.** Rejected: it allows connections that never end and cannot express an idle bound. `timeouts` and `streamIdleTimeout` cover both.

## Testing

- **Unit tests**: controller output (generated resources and status) for both card modes and with and without authentication; extension server filter insertion, per-route enablement, and matcher compilation, including the method table and the implicit rule.
- **CRD validation tests** in `tests/crdcel` for the CEL rules above.
- **End-to-end tests** with a test agent in `tests/internal/testa2a`, built on the A2A project's Go SDK, which implements A2A 1.0 with a JSON-RPC server:
  - card served in both modes, and public while the JSON-RPC endpoint requires credentials;
  - `SendMessage`, and a `SendStreamingMessage` stream that outlives Envoy's 15s default;
  - authorization allowing and denying by method, for 1.0 and 0.3 names, and denying a request without a method;
  - `400` for a body that is not JSON-RPC and `413` above `maxRequestBodySizeBytes`;
  - an `AIGatewayRoute` on the same listener receiving a non-JSON-RPC body unchanged.
- **`aigw translate`** test data for an `A2ARoute`.

The `413` case requires Envoy v1.39; it is skipped in the CI job that runs Envoy Gateway v1.8.5.

## Limitations

- **Duplicate `method` keys.** The filter's JSON-RPC parser stops once it has the fields it extracts. For a 1.0 method name these are `jsonrpc`, `method`, and `id`; when all three appear before a second `method` key, the filter records the first `method`. The Go and Python standard library JSON parsers keep the last. Tested with Envoy v1.38.1 (the parser is unchanged in v1.39): `{"jsonrpc":"2.0","id":1,"method":"GetTask","method":"CancelTask"}` passes a rule that allows only `GetTask`. Method-level authorization cannot be relied on against a client that controls the request body until the filter rejects duplicate keys or records the last value; we will propose that change to Envoy.
- **JSON-RPC batches.** The filter accepts a top-level array as valid JSON-RPC but records no `method` for it (tested with Envoy v1.38.1). With `authorization` set, the implicit rule denies such requests; without it, they are forwarded to the agent. The A2A specification does not define batching.
- **Work-in-progress filter API.** Envoy marks the A2A filter API work-in-progress, so its configuration can change between Envoy releases.
- **Errors are plain HTTP.** Responses for `400`, `403`, and `413` are Envoy local replies, not JSON-RPC error objects.
- **Passthrough under a path prefix.** The agent's card is correct only if the agent advertises the gateway URL ([D4]).
- **Body size per Gateway.** `maxRequestBodySizeBytes` applies to every A2A route on a Gateway ([D8](#d8--protocol-validation-and-body-size)).

[A2A]: https://a2a-protocol.org
[spec]: https://github.com/a2aproject/A2A/blob/v1.0.1/docs/specification.md
[spec-v03]: https://github.com/a2aproject/A2A/blob/v0.3.0/docs/specification.md
[preview]: ../../../site/docs/capabilities/a2a/index.md
[a2a-filter]: https://github.com/envoyproxy/envoy/tree/v1.39.0/source/extensions/filters/http/a2a
[#2070]: https://github.com/theagentrouter/agent-router/issues/2070
[#902]: https://github.com/theagentrouter/agent-router/issues/902
