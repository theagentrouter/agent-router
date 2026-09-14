# Configurable Tool Name Prefixing for MCP Multiplexing (`prefixMode`)

## Table of Contents

- [Configurable Tool Name Prefixing for MCP Multiplexing (`prefixMode`)](#configurable-tool-name-prefixing-for-mcp-multiplexing-prefixmode)
  - [Table of Contents](#table-of-contents)
  - [Background and Motivation](#background-and-motivation)
  - [Current State](#current-state)
    - [How Prefixing Works](#how-prefixing-works)
    - [Where It Breaks: Interactive MCP Apps](#where-it-breaks-interactive-mcp-apps)
    - [Relationship to the Rendering Fix (PR #2412)](#relationship-to-the-rendering-fix-pr-2412)
  - [Goals and Non-Goals](#goals-and-non-goals)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
  - [Design](#design)
    - [The `prefixMode` Field](#the-prefixmode-field)
    - [How `Never` Mode Routes a Call](#how-never-mode-routes-a-call)
    - [Where the Field Lives: Route-Level vs Per-Backend](#where-the-field-lives-route-level-vs-per-backend)
    - [Handling Duplicate Names](#handling-duplicate-names)
    - [Interaction With Per-Tool Authorization](#interaction-with-per-tool-authorization)
    - [Resource URIs Are Unaffected](#resource-uris-are-unaffected)
  - [Prior Art: Agentgateway](#prior-art-agentgateway)
  - [Limitations](#limitations)
  - [API Proposal](#api-proposal)
  - [Implementation Details](#implementation-details)
    - [Data plane (`internal/mcpproxy`)](#data-plane-internalmcpproxy)
    - [Control plane](#control-plane)
    - [Tests](#tests)
  - [Alternatives Considered](#alternatives-considered)

## Background and Motivation

When an `MCPRoute` multiplexes several MCP servers behind one endpoint, the gateway prefixes every tool and prompt name with the backend name and a `__` separator. As a running example throughout this proposal: `vanillajs` is a backend MCP server, registered under that name in the route's `backendRefs` (it is the MCP Apps sample server `@modelcontextprotocol/server-basic-vanillajs`, used to verify [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412)), and `get-time` is a tool that server exposes. The gateway advertises that tool to clients as `vanillajs__get-time`. The prefix does double duty: it prevents name collisions when two backends expose a tool with the same name, and it carries the routing information the proxy needs to send a `tools/call` back to the originating backend.

This works for a model calling tools by their advertised names. It breaks for MCP Apps (the [ext-apps](https://github.com/modelcontextprotocol/ext-apps) extension), where a rendered UI resource calls tools _from inside the iframe_ using names hardcoded in server-authored HTML/JS. Those names are the backend's own unprefixed names (`get-time`), which the gateway never advertised and cannot resolve. The interactive call fails with `invalid tool name`.

[PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) fixes MCP Apps _rendering_ by preserving the `ui://` scheme when namespacing resource URIs. Rendering is a prerequisite for anything interactive, but it does not make in-app tool calls work. This proposal covers the second half: a `prefixMode` option that lets an operator expose tool names unprefixed so the names baked into app HTML match what the gateway routes on.

[Issue #2316](https://github.com/envoyproxy/ai-gateway/issues/2316) independently asks for control over the `<backend>__` prefix, for reasons unrelated to MCP Apps (prefixes make tool names long, leak internal backend names, and force teams to rename deployments to change a prefix). Solving that issue is not the goal of this proposal; the goal is the MCP Apps case. But the mechanism overlaps: `prefixMode` also answers the "disable when there is a single backend" case in [#2316](https://github.com/envoyproxy/ai-gateway/issues/2316), and `Never` gives its unique-names case bare tool names as a side effect. The remaining ask there, a custom per-backend alias, stays out of scope (see [Alternatives](#alternatives-considered)).

## Current State

### How Prefixing Works

Prefixing and its inverse live in `internal/mcpproxy/handlers.go`:

```go
// Using "__" as the separator to avoid collision with any character in k8s resource names as well as base64 encoding.
// We can't use special characters as tool names must match the regex `[a-zA-Z0-9._-]+`.
const nameSeparator = "__"

// downstreamResourceName converts the upstream resource/prompt name to the downstream resource/prompt name by
// prefixing the backend name.
func downstreamResourceName(name string, backendName string) string {
	return fmt.Sprintf("%s%s%s", backendName, nameSeparator, name)
}

// upstreamResourceName converts the downstream tool/resource name to the upstream resource/prompt name by
// stripping the backend name prefix.
//
// We assume that all tool/resource names are prefixed with the backend name followed by an underscore, so
// it's an unrecoverable error if the tool/resource name doesn't contain an underscore and that's a client error.
func upstreamResourceName(fullName string) (backendName, name string, err error) {
	index := strings.Index(fullName, nameSeparator)
	if index < 0 {
		return "", "", fmt.Errorf("invalid resource name: %s", fullName)
	}
	return fullName[:index], fullName[index+len(nameSeparator):], nil
}
```

`mergeToolsList` applies `downstreamResourceName` to every tool from every backend when building the `tools/list` response, and `mergePromptsList` does the same for prompts. On the call path, `handleToolCallRequest` derives the backend entirely from the name:

```go
func (m *mcpRequestContext) handleToolCallRequest(...) {
	backendName, toolName, err := upstreamResourceName(p.Name) // needs the "__"
	...
	backend, err := m.getBackendForRoute(s.route, backendName)
	...
	selector := route.toolSelectors[backendName]
	if selector != nil && !selector.allows(toolName) { ... }
	// authorization keyed on (backendName, toolName)
}
```

`handlePromptGetRequest` splits `prompts/get` names the same way, and `handleCompletionComplete` does it for `completion/complete` requests with a `ref/prompt` reference, so those methods are bound to the prefix too. Routing, tool-selector filtering, and per-tool authorization all key off the backend name recovered from the prefix. Prefixing is unconditional today: even a single-backend route prefixes.

### Where It Breaks: Interactive MCP Apps

The ext-apps flow is: a `tools/call` returns a UI resource, the host reads it (`resources/read`), renders it in an iframe, and the app's JavaScript can then issue its own `tools/call` requests. Checking the [ext-apps specification](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx) confirms two things:

- The host forwards the app's `tools/call` to the server with the **bare tool name and no reference to the UI resource or backend that rendered the iframe**. So the gateway has no origin signal to scope an unprefixed name back to a backend.
- There is **no host-provided API for the iframe to discover tool names**. `hostContext.toolInfo` describes only the tool that launched the view. App-only tools (`visibility: ["app"]`, hidden from the model and callable only from the iframe) have their names hardcoded in the server's HTML with no way to learn the gateway's renamed version.

So the app rendered from the `vanillajs` backend calls its tool by the name baked into its HTML, `get-time`; the gateway runs `upstreamResourceName("get-time")`, finds no `__`, and rejects the call. Display-only apps work after [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412); interactive ones do not.

### Relationship to the Rendering Fix (PR #2412)

The two problems are separable and the fixes are independent:

| Concern                 | Depends on                           | Fixed by                                                                                               |
| ----------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------------------ |
| App renders at all      | Resource URI keeps `ui://` scheme    | [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) (namespaces as `ui://<backend>/<rest>`) |
| In-app tool call routes | Tool name matches what the app calls | This proposal (`prefixMode: Never`)                                                                    |

[PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) could merge on its own. This proposal builds on top of it: rendering has to work before interactivity is worth anything, and the URI namespacing [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) introduces stays in place regardless of `prefixMode` (see [Resource URIs Are Unaffected](#resource-uris-are-unaffected)).

## Goals and Non-Goals

### Goals

- Add a `prefixMode` option to `MCPRoute` that controls how tool and prompt names are prefixed: always, only when needed, or never.
- In `Never` mode, expose tool and prompt names unprefixed and route calls by resolving the name against the route's backends at call time, so names hardcoded in MCP App HTML resolve correctly.
- Preserve today's behavior by default. Existing routes keep unconditional prefixing and existing tool names.
- Define the behavior when two backends on the same route expose the same name under `Never` mode.
- Keep the resource URI namespacing from [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) intact in all modes.

### Non-Goals

- This proposal does **not** change resource URI encoding or the MCP Apps rendering path. That is [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412).
- This proposal does **not** add a custom per-backend prefix string or alias (the other half of [#2316](https://github.com/envoyproxy/ai-gateway/issues/2316)). That could be a follow-up if there is still value in that and is discussed under [Alternatives](#alternatives-considered).
- This proposal does **not** try to make the fully general case work. Multiple backends, overlapping tool names, and app-only tools cannot all be satisfied at the gateway. See [Limitations](#limitations). The root gap is in the ext-apps spec and is possibly worth raising upstream.

## Design

### The `prefixMode` Field

Add a `prefixMode` enum to the `MCPRoute` API with three values, matching the model Agentgateway settled on:

- **`Always`**: prefix tool and prompt names with `<backend>__`, even for a single backend. This is today's behavior and the **default**, so existing routes are unchanged.
- **`Conditional`**: prefix only when the route has more than one backend. A single-backend route exposes bare names. This directly answers the "disable prefix when there is one backend" ask in [#2316](https://github.com/envoyproxy/ai-gateway/issues/2316).
- **`Never`**: never prefix. Names are exposed bare regardless of backend count, and calls are routed by resolving the name against the route's backends at call time. This is the mode that makes interactive MCP Apps work.

Default is `Always` rather than `Conditional` so that upgrading changes no advertised tool name for anyone. This looks like a divergence from Agentgateway, whose default is `Conditional`, but both defaults follow the same principle: codify the project's pre-existing behavior. Agentgateway already prefixed conditionally before adding `Never` (its enum had `CONDITIONAL = 0` from the start); Envoy AI Gateway has always prefixed unconditionally. A `Conditional` default here would silently rename every tool on every existing single-backend route on upgrade, breaking any client, agent configuration, or client-side allowlist that stores the prefixed names.

`Conditional` also has a sharp edge worth naming: adding a second backend to a previously single-backend route renames every advertised tool at that moment. `Always` and `Never` are stable under backend count changes; `Conditional` is not. Operators who want bare names should generally prefer `Never` and accept its uniqueness constraint, using `Conditional` only when they want automatic collision protection and can tolerate the rename when scaling out.

### How `Never` Mode Routes a Call

In `Always`/`Conditional` mode with a prefix present, routing is unchanged: `upstreamResourceName` splits the name and the backend is known before anything else runs.

In `Never` mode the name has no `__` to split, so the proxy resolves the target by name. The authoritative resolution is a `tools/list` fan-out to the route's backends: whichever backend advertises the name (after tool-selector filtering) is the target. This matches Agentgateway, which resolves each call by fanning out a list and notes list-response caching as a follow-up.

The fan-out result can be cached per replica, keyed by route, to avoid paying the list round-trips on every call. The cache must be exactly that, a cache: the proxy's session design is stateless (session state is encoded in the encrypted session ID so any replica can serve any request, per [proposal 006](../006-mcp-gateway/proposal.md)), so correctness cannot depend on a warm index. A cold replica falls back to the fan-out, and a cached entry is invalidated when a backend emits `notifications/tools/list_changed` or on TTL expiry. A stale entry is also self-correcting: if the resolved backend rejects the call with an unknown-tool error, the proxy re-resolves once before failing.

Resolution runs against the _filtered_ tool set, after `toolSelector` include/exclude rules. A name excluded from backend `b` by its selector neither routes to `b` nor makes another backend's identically named tool ambiguous. Authorization deliberately plays no part in resolution: routing must not depend on caller identity, so a name served by two selector-allowed backends is ambiguous even if the caller is only authorized for one of them.

`upstreamResourceName` gains a mode-aware path: when the route is in `Never` mode it skips the `__` split and calls the resolver instead. The `__` split and all existing behavior stay exactly as-is for the other modes.

### Where the Field Lives: Route-Level vs Per-Backend

`prefixMode` belongs on `MCPRouteSpec`, not on individual backend refs.

`Conditional` ("prefix only with more than one backend") and the collision-freeness that `Never` depends on are both properties of the whole route, not of one backend. Call-time resolution scans every backend on the route, so the mode has to be consistent across them. A per-backendRef field would allow a route to mix `Never` and `Always` backends, which has no coherent meaning: a bare name would have to be resolved against some backends and matched by prefix against others.

This is not a divergence from Agentgateway. Its `prefixMode` sits on its `MCPBackend` message, which _contains_ the list of multiplexed targets (`repeated MCPTarget targets`), so the field already scopes to the aggregation group. That group is the semantic analog of Envoy AI Gateway's `MCPRoute`, not of an individual `backendRef`. Route-level placement here is the equivalent design.

One naming caution: [proposal 011](../011-mcp-backend-crd/proposal.md) introduces an Envoy AI Gateway CRD also called `MCPBackend`, but that one represents a _single_ upstream server. Despite the shared name, it is the wrong home for this field; `prefixMode` stays on `MCPRouteSpec` regardless of how 011 lands.

A per-backend custom prefix string is a separate, compatible feature and can be added later without conflicting with this field. See [Alternatives](#alternatives-considered).

### Handling Duplicate Names

In `Never` mode, two backends on the same route can expose a tool with the same name, and a bare name no longer identifies which one. The first design constraint is that **ambiguity must be a terminal outcome**. Resolving it by picking a backend (first wins, priority order) is off the table: per-tool authorization is keyed on `(backend, tool)`, so an arbitrary pick would apply one backend's authorization policy to what may be an attempt to reach the other's tool. Using authorization itself to break the tie is also out, per [How `Never` Mode Routes a Call](#how-never-mode-routes-a-call): routing must not depend on caller identity.

Given that, the advertise side is settled and this proposal does not change it: a colliding name is omitted from the merged `tools/list`, because a duplicate entry is meaningless to a client. Agentgateway does exactly this and logs a warning (`dropping ambiguous MCP names served by multiple targets: ...`); this proposal keeps that behavior as-is.

The only open question is what happens when the omitted name is _called_ anyway, which it will be: the caller `Never` mode exists for is an iframe app invoking a name hardcoded in its HTML, so it never read `tools/list` and the omission is invisible to it. That single call-time error string is the entire divergence from Agentgateway:

- **Agentgateway today**: the call fails as a generic unknown tool. The only record of the collision is the gateway's log line, so the app developer cannot tell "misconfigured route" from "typo in my HTML".
- **This proposal**: the call fails with a distinct "ambiguous tool name" error, which tells the app developer the name exists but collides and points at the fix (rename the tool, split the route, or use a prefixed mode).

This proposal takes the distinct error. To be clear about the scope: the list behavior is identical to Agentgateway; the difference is one error string returned on a call, nothing more. One refinement on that error's content: the client-facing message stays generic (`ambiguous tool name: served by multiple backends`), with the colliding backend names recorded in gateway logs and metrics only. Naming backends in a client-visible error would leak the internal names that `Never` mode deliberately hides, one of the motivations in [#2316](https://github.com/envoyproxy/ai-gateway/issues/2316).

The weight of this divergence is small and worth stating plainly: it is a diagnostics improvement, not a safety requirement. Matching Agentgateway's generic failure verbatim would be equally safe under Envoy AI Gateway's authorization model, since neither behavior ever routes an ambiguous name. The distinct error is preferred only because it is strictly more informative at negligible cost, and the choice is easy to revisit.

The `MCPRoute` validation should also warn at admission time when a `Never`-mode route has backends with statically known overlapping tool names, so the problem surfaces before runtime where possible. Static warning cannot catch collisions that appear dynamically (a backend adding a tool at runtime via `notifications/tools/list_changed`), which is another reason the call-time error carries the diagnostic load: with a generic failure, a backend shipping a new tool can silently break another backend's existing tool with no client-visible signal at all.

### Interaction With Per-Tool Authorization

Authorization rules match on `ToolCall{Backend, Tool}` (`internal/filterapi/mcpconfig.go`). In `Always` mode the backend is known from the prefix, so authorization runs before backend resolution. In `Never` mode the order flips: **resolve the backend from the name first, then authorize the resolved `(backend, tool)`**. If resolution is ambiguous, deny before authorizing. The `tools/list` side already authorizes per backend as it iterates responses in `mergeToolsList`, so the advertised set stays correct without changes; only the call path needs the reordering.

### Resource URIs Are Unaffected

`prefixMode` scopes to **names** (tools, prompts, and resource display names), not to resource URIs. Resource and resource-template names are prefixed today too (`mergeResourceList`, `mergeResourcesTemplateList`), but they carry no routing information: `resources/read`, `resources/subscribe`/`unsubscribe`, and `ref/resource` completions all route by URI (`upstreamResourceURI`). So resource names follow `prefixMode` for display consistency with zero routing impact. Resource URIs keep the namespacing [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) introduces (`ui://<backend>/<rest>` for UI resources, `<backend>+<scheme>://` otherwise). Those URIs are self-contained routing handles that the gateway mints and the host echoes back on `resources/read`; they do not depend on a client hardcoding anything, so there is no reason to strip them. Agentgateway draws the same line: its `prefix: never` affects tool and prompt names, and resource URIs continue to use prefixing and federated routing. Keeping URI namespacing on in every mode is what lets rendering ([PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412)) and interactive calls (this proposal) both work at once.

## Prior Art: Agentgateway

Agentgateway hit this exact problem and shipped a fix in [agentgateway/agentgateway#2531](https://github.com/agentgateway/agentgateway/pull/2531) ("mcp: add 'prefix: never' to fix multiplexing for app originated tool calls", merged 2026-07-16). Its shape closely matches what this proposal recommends, which is good validation:

- A `prefixMode` enum with `Conditional` (default), `Always`, and `Never`, surfaced in both file config and the k8s CRD.
- In `Never` mode, names are exposed unprefixed and each call is routed by discovering which target serves the name (a `tools/list`/`prompts/list` fan-out).
- Resource URI federation is left on prefixing and is explicitly not affected.

Its documented trade-offs carry over directly: synthetic calls always fan out in this mode, calls need a list to resolve the target (a follow-up adds list-response caching), and names must be unique across targets.

The one place this proposal diverges is duplicate handling, and the divergence is narrow. Agentgateway drops duplicated names from the merged list with a warning log; this proposal does the same on the list side but additionally returns a distinct ambiguity error when the dropped name is called, rather than a generic unknown-tool failure. As argued in [Handling Duplicate Names](#handling-duplicate-names), this is a diagnostics improvement for hardcoded-name callers, not a safety requirement; matching Agentgateway exactly would be equally safe. It is a conscious refinement of the prior art, not an oversight.

For context, the two projects also independently converged on the URI-encoding approach in [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412): Agentgateway keeps the `ui://` scheme by carrying the target in the URI authority (`ui://<target>+<rest>`), and [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412) carries the backend in a leading path segment (`ui://<backend>/<rest>`). Both preserve the literal `ui://` scheme hosts require.

## Limitations

`Never` mode fixes interactive MCP Apps for the common cases: a single backend, or multiple backends whose tool names do not collide. It does not solve everything, and the proposal should not claim it does.

- **Colliding tool names across backends can't both work:** If two backends both expose `get-time` and an app calls it bare, the gateway cannot tell which one the iframe meant. The ext-apps spec carries no origin marker on the call, so there is no signal to disambiguate. This proposal rejects the call; it does not route it.
- **App-only tools remain fragile in multi-backend routes:** Tools with `visibility: ["app"]` are hardcoded in server HTML and have no discovery API. `Never` mode makes their bare names resolvable when names are unique, but a collision is unrecoverable, and the host's own visibility rejection (which is by name) gets confusing when the name it knows differs from the name the app calls.
- **Extra calls:** `Never` mode may fan out a `tools/list` to resolve a cold call. Caching list results reduces this and is a reasonable follow-up, as Agentgateway also noted.
- **Operators opt into the trade-off:** Because the default stays `Always`, nobody gets unprefixed names or the collision constraint unless they choose it. That is the right default: `Never` trades collision-safety for compatibility with app-hardcoded names, and only the operator knows whether their route's names are unique.

The general case (multiple backends, overlapping names, app-only tools) is genuinely unsolvable at any gateway with today's ext-apps spec, because the spec neither marks call origin nor lets the iframe discover tool names. That is possibly worth filing upstream against [modelcontextprotocol/ext-apps](https://github.com/modelcontextprotocol/ext-apps) as a separate effort; it is out of scope here.

## API Proposal

Add a `PrefixMode` type and a `prefixMode` field to `MCPRouteSpec` in `api/v1beta1/mcp_route.go`:

```go
// PrefixMode controls how tool and prompt names from backends are prefixed
// with the backend name when exposed to clients on this route.
//
// +kubebuilder:validation:Enum=Always;Conditional;Never
type PrefixMode string

const (
	// PrefixModeAlways prefixes tool and prompt names with "<backend>__" for
	// every backend, including single-backend routes. This is the default and
	// matches the behavior of releases before this field existed.
	PrefixModeAlways PrefixMode = "Always"

	// PrefixModeConditional prefixes only when the route has more than one
	// backend. A single-backend route exposes bare names.
	PrefixModeConditional PrefixMode = "Conditional"

	// PrefixModeNever never prefixes. Names are exposed bare and tool/prompt
	// calls are routed by resolving the name against the route's backends at
	// call time. Tool names must be unique across the route's backends; a call
	// that resolves to more than one backend is rejected. Required for
	// interactive MCP Apps, whose in-app tool calls use the backend's
	// unprefixed names hardcoded in server-authored HTML.
	PrefixModeNever PrefixMode = "Never"
)
```

```go
type MCPRouteSpec struct {
	// ... existing fields ...

	// PrefixMode controls how tool and prompt names are prefixed with the
	// backend name. Resource URIs are namespaced independently and are not
	// affected by this field.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=Always
	// +optional
	PrefixMode *PrefixMode `json:"prefixMode,omitempty"`
}
```

The field is threaded through to the proxy config (`internal/filterapi/mcpconfig.go` `MCPRoute`) so the data plane can select the routing path.

Example: a single MCP Apps backend exposed with unprefixed names so its rendered app's in-app calls resolve.

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: vanillajs-app
spec:
  parentRefs:
    - name: aigw-run
      kind: Gateway
      group: gateway.networking.k8s.io
  path: "/mcp"
  prefixMode: Never
  backendRefs:
    - name: vanillajs
      kind: Backend
      group: gateway.envoyproxy.io
```

With `prefixMode: Never`, `get-time` is advertised as `get-time` (not `vanillajs__get-time`), the rendered app's hardcoded `get-time` call resolves to the `vanillajs` backend, and interactive updates work.

## Implementation Details

### Data plane (`internal/mcpproxy`)

1. **Advertise side:** `mergeToolsList` and `mergePromptsList` consult the route's `prefixMode`:
   - `Always`: prefix as today.
   - `Conditional`: prefix only if the route has more than one backend.
   - `Never`: do not prefix; omit names served by more than one backend from the merged list (logging the collision and the backends involved) and opportunistically warm the per-replica resolution cache with each surviving name as it is merged.

2. **Route side:** `handleToolCallRequest`, `handlePromptGetRequest`, and `handleCompletionComplete` (which resolves `ref/prompt` completion references by prompt name) branch on `prefixMode`:
   - Non-`Never`: `upstreamResourceName` as today.
   - `Never`: resolve the backend from the per-replica cache; if cold, fan out a `tools/list` to resolve; if the name resolves to more than one backend, reject with a distinct "ambiguous tool name" error (generic message to the client; colliding backend names in gateway logs and metrics only, per [Handling Duplicate Names](#handling-duplicate-names)). Then run the existing selector and authorization checks against the resolved `(backend, tool)`.

3. **Authorization ordering:** In `Never` mode, resolve the backend before authorizing (see [Interaction With Per-Tool Authorization](#interaction-with-per-tool-authorization)). No change to the `tools/list` authorization path.

4. **Resolution cache:** A per-replica `map[toolName]backendName` (and one for prompts) keyed by route, populated from list fan-out responses and consulted on calls. It is an optimization only; the list fan-out remains the authoritative resolution so that any replica can serve any request (see [How `Never` Mode Routes a Call](#how-never-mode-routes-a-call)). Duplicate detection happens at resolution time, on the selector-filtered set.

### Control plane

- Add `PrefixMode` to `api/v1beta1` with the enum and default, regenerate CRDs and DeepCopy.
- Thread the value into the filter config (`internal/filterapi/mcpconfig.go`) via the `MCPRoute` controller.
- Optional admission-time validation: warn when a `Never`-mode route has backends with statically known overlapping tool names.

### Tests

- Unit tests in `internal/mcpproxy` for all three modes: advertised names, call routing, cold-index fan-out, and ambiguous-name rejection.
- An e2e test (extending `tests/e2e/mcp_route_test.go` and the `tests/internal/testmcp` fixtures) that renders a real MCP Apps resource under `prefixMode: Never` and drives an in-app tool call end to end, asserting it resolves. This closes the loop that [PR #2412](https://github.com/envoyproxy/ai-gateway/pull/2412)'s tests stop short of, since it covers rendering only.

## Alternatives Considered

**Rewrite tool names inside the app's HTML/JS:** The gateway could try to rewrite hardcoded names in the served UI resource so `get-time` becomes `vanillajs__get-time`. Rejected: the content is server-authored, possibly minified, and names can be built at runtime in JavaScript. This is fragile and a layering violation.

**Per-backendRef `prefixMode`:** Place the field on each entry in `backendRefs`. Rejected because `Conditional` and collision-freeness are route-wide properties and call-time resolution scans the whole route; a mix of `Never` and `Always` backends on one route has no clear meaning. Note that Agentgateway is not a precedent for per-target placement: its field sits on the object that contains the target list, which corresponds to `MCPRoute` here (see [Where the Field Lives](#where-the-field-lives-route-level-vs-per-backend)).

**Custom per-backend prefix string (alias):** [Issue #2316](https://github.com/envoyproxy/ai-gateway/issues/2316) also asks for a custom prefix per backend, e.g. `billing` instead of `acme-billing-mcp`. This is a compatible, separate feature: it keeps prefixing (and prefix-based routing) but changes the string. It does not help MCP Apps, whose HTML hardcodes bare names, so it is out of scope here. It can be added later alongside `prefixMode` without conflict.

**Pure drop, matching Agentgateway exactly:** Omit colliding names from `tools/list` (which this proposal also does) and let calls to them fail as unknown tools. Equally safe, and viable if maintainers prefer strict parity with the prior art. Not preferred because the callers this mode exists for hardcode their tool names and never see `tools/list`, so the call-time error is the only place the collision can be communicated to them; a distinct ambiguity error costs a few lines and turns a dead-end failure into an actionable one. See [Handling Duplicate Names](#handling-duplicate-names).
