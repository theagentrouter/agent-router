# Router-Phase ext_proc via an `AIGatewayExtensionPolicy` CRD

## Table of Contents

<!-- toc -->

- [Summary](#summary)
- [Background](#background)
  - [How an AIGatewayRoute becomes an HTTPRoute](#how-an-aigatewayroute-becomes-an-httproute)
  - [The two-route / landing-route problem (recap)](#the-two-route--landing-route-problem-recap)
  - [Why the existing workarounds fall short](#why-the-existing-workarounds-fall-short)
- [Problem Statement](#problem-statement)
- [Goals / Non-Goals](#goals--non-goals)
- [Proposal](#proposal)
  - [Overview](#overview)
  - [The `AIGatewayExtensionPolicy` CRD](#the-aigatewayextensionpolicy-crd)
  - [Attaching a policy via `targetRef`](#attaching-a-policy-via-targetref)
  - [`attachmentMode`: where the policy is enabled](#attachmentmode-where-the-policy-is-enabled)
  - [Request flow](#request-flow)
  - [How the ext_proc is added at `PostTranslateModify`](#how-the-ext_proc-is-added-at-posttranslatemodify)
  - [Building the policy → route mapping (code)](#building-the-policy--route-mapping-code)
  - [Enablement: targeted routes and catch-all](#enablement-targeted-routes-and-catch-all)
  - [Composite placement vs. per-route configuration](#composite-placement-vs-per-route-configuration)
  - [Envoy version requirement (CompositePerRoute over RDS)](#envoy-version-requirement-compositeperroute-over-rds)
  - [Policy lifecycle: add, update, and remove](#policy-lifecycle-add-update-and-remove)
  - [Alternative: EG-native re-translation via `policyResources` (no annotation hack)](#alternative-eg-native-re-translation-via-policyresources-no-annotation-hack)
- [Code Changes](#code-changes)
  - [1. API types (`api/v1alpha1`)](#1-api-types-apiv1alpha1)
  - [2. Controller (`internal/controller`)](#2-controller-internalcontroller)
  - [3. Extension server (`internal/extensionserver`)](#3-extension-server-internalextensionserver)
  - [4. Manifests, RBAC, generated code](#4-manifests-rbac-generated-code)
  - [5. Tests](#5-tests)
- [End-to-end example: from CRDs to xDS](#end-to-end-example-from-crds-to-xds)
- [Validation on a live cluster](#validation-on-a-live-cluster)
- [Why this avoids the catch-all blast radius](#why-this-avoids-the-catch-all-blast-radius)
- [Alternatives Considered](#alternatives-considered)
- [Open Questions](#open-questions)

<!-- /toc -->

## Summary

This proposal introduces a new CRD, **`AIGatewayExtensionPolicy`**, that describes a
router-phase `ext_proc` (modeled on the `extProc` field of Envoy Gateway's
`EnvoyExtensionPolicy`) and **attaches to a single `AIGatewayRoute` via
`targetRef`**. The policy also includes **`attachmentMode`** that decides where the
extension is enabled:

- `FallbackOnly`: catch-alls only
- `RouteOnly`: route rules only
- `All`: both catch-alls and route rules.

The AI Gateway extension server, during its existing `PostTranslateModify` phase,
inserts a composite filter — named with its canonical registered name
`envoy.filters.http.composite` (this matters; see
[Envoy version requirement](#envoy-version-requirement-compositeperroute-over-rds)) —
(added **disabled**) into the listener HCM filter chain **ahead of the AI Gateway
`ext_proc`**, and then attaches a **`CompositePerRoute`** — whose match tree is keyed
on the policy's `headers` and whose `ExecuteFilterAction` delegates to the described
`ext_proc`. The `CompositePerRoute` is wrapped in an
`envoy.config.route.v3.FilterConfig` before being placed in the route's
`typed_per_filter_config` (mirroring how the AI Gateway router `ext_proc` is enabled
per route). Attaching the per-route config both **enables** the disabled composite
for that route and supplies its match tree. The composite is enabled on **all rule
routes of the targeted `AIGatewayRoute`(s)** and on **all catch-all
(`route-not-found`) routes** (so a request is covered on its first pass, before
`x-ai-eg-model` exists and before routing is finalized). (See
[Composite placement vs. per-route configuration](#composite-placement-vs-per-route-configuration)
for why `CompositePerRoute` — not `ExtensionWithMatcher` — is the mechanism that
makes per-route enablement possible.)

> **Envoy version prerequisite.** Resolving a route-level `CompositePerRoute` via
> Envoy Gateway's RDS requires **Envoy ≥ 1.39 / a `main` (dev) build that contains
> Envoy PR [#43996](https://github.com/envoyproxy/envoy/pull/43996) (commit
> `e842507299`, 2026-04-29)**. Envoy ≤ 1.38.x rejects the entire `RouteConfiguration`
> (see [Envoy version requirement](#envoy-version-requirement-compositeperroute-over-rds)).
> This was validated end-to-end on a cluster running `envoyproxy/envoy:distroless-dev`
> (`1.39.0-dev`); see [Validation on a live cluster](#validation-on-a-live-cluster).

This is a **declarative, name-based, validated** replacement for the
`EnvoyPatchPolicy` composite-wrap workaround: it keeps the "run a second ext_proc
before the AI Gateway decides the route" capability, but removes the index-pinned
raw-JSON fragility, and — because the composite gates on the policy's `headers` —
shrinks the blast radius from "all catch-all traffic" to "only traffic carrying
those headers."

## Background

### How an AIGatewayRoute becomes an HTTPRoute

The controller renders each `AIGatewayRoute` into a single `HTTPRoute`
(`internal/controller/ai_gateway_route.go`, `newHTTPRoute`) whose rules are:

- **One rule per `AIGatewayRoute` rule** — a path-prefix match (the root prefix,
  e.g. `/v1`) plus any header matches the user declared (model-based routing is a
  header match on `x-ai-eg-model`).
- **One controller-injected catch-all rule** — named `route-not-found`, matching
  only the root path prefix with no header match, appended last so that a request
  always matches _something_ on the prefix even before the model is known.

### The two-route / landing-route problem (recap)

Because `x-ai-eg-model` is produced **server-side** by the AI Gateway router-phase
`ext_proc` (it parses the body, extracts the model, sets the header, and returns
`ClearRouteCache: true`), a fresh request has no `x-ai-eg-model` on its first pass.
The only rule it can match is the header-less **catch-all**. Only after
`ClearRouteCache` does Envoy re-match onto the correct header-keyed rule.

Consequently, anything that must run **before routing is finalized** runs while the
request is on the **catch-all** route, not on its eventual destination route. On a
single Gateway with multiple `AIGatewayRoute`s, all catch-alls collapse (same
prefix, no headers) to one surviving rule via Gateway-API conflict resolution, so
all first-pass traffic funnels through a single shared catch-all.

### Why the existing workarounds fall short

Slotting a second `ext_proc` (e.g. a semantic-router) in front of the AI Gateway
filter today is done via `EnvoyPatchPolicy` composite-wrap or `EnvoyExtensionPolicy`
on the generated route. Both must hang the filter off the shared catch-all, so a
misconfiguration affects **all** traffic; and the `EnvoyPatchPolicy` variant is
additionally index-pinned (RFC 6901 JSON Pointer into `http_filters[]`),
unvalidated, version-fragile, and requires manual filter ordering via
`--extProcBeforeFilterNames`.

(See Proposal 012 for the full treatment of these problems.)

## Problem Statement

We want a **first-class, declarative** way to run a router-phase `ext_proc`
before the AI Gateway makes its routing decision, that:

1. does not require hand-written `EnvoyPatchPolicy` JSON or manual filter ordering,
2. is validated and name-based (survives model/route churn and EG upgrades), and
3. limits the blast radius of a misconfiguration to the traffic the `headers` gate
   selects, rather than to all catch-all traffic.

## Goals / Non-Goals

**Goals**

- A CRD describing a router-phase `ext_proc`, attached to one
  `AIGatewayRoute` via `targetRef`.
- Route enablement selection via `attachmentMode`:
  `FallbackOnly`, `RouteOnly`, `All`.
- Reuse the existing catch-all / `ClearRouteCache` routing mechanics unchanged.

**Non-Goals**

- Changing how header-keyed / catch-all rules are generated.
- Replacing the AI Gateway router/upstream `ext_proc` split.
- Defining the mutation-service wire contract (that is Proposal 012's concern;
  this proposal is about _how a second ext_proc is wired in and gated_, and is
  complementary — the wrapped ext_proc may be a semantic-router or anything else).

## Proposal

### Overview

Add an `AIGatewayExtensionPolicy` CRD (an `ext_proc` description +
`targetRef` + `attachmentMode`). At `PostTranslateModify`, the extension server
resolves the mapping and injects a composite `ext_proc` into the listener (added
disabled, ordered before the AI Gateway `ext_proc`), then enables it according to
`attachmentMode`.

```
  AIGatewayExtensionPolicy "sr"                         AIGatewayRoute "chat"
  ┌────────────────────────────────┐                    ┌──────────────────────┐
  │ spec.targetRef:                 │  targets           │ rules:               │
  │   kind: AIGatewayRoute          │ ─────────────────► │   - matches: [...]    │
  │   name: chat                    │                    │     backendRefs:[...] │
  │ spec.attachmentMode: All        │                    │   - ...               │
  │ spec.extProc:                   │   enabled by mode: catch-all only,
  │   backendRefs: [sr-svc]         │   route rules only, or both.
  │   processingMode: {...}         │
  └────────────────────────────────┘
```

### The `AIGatewayExtensionPolicy` CRD

A new namespaced CRD in `api/v1alpha1`. Its spec reuses Envoy Gateway's `ExtProc`
type (so ext_proc semantics — backend refs, processing mode, timeouts, metadata
options — match `EnvoyExtensionPolicy` 1:1), adds a single `targetRef` (policy
attachment) and an `attachmentMode` selector:

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayExtensionPolicy
metadata:
  name: semantic-router
  namespace: default
spec:
  # Policy attachment: which AIGatewayRoute this applies to.
  targetRef:
    group: aigateway.envoyproxy.io
    kind: AIGatewayRoute
    name: chat
  # AttachmentMode determines which underlying routes receive this policy.
  # Defaults to "All" to guarantee the extension is executed for all targeted traffic.
  #
  # +optional
  # +kubebuilder:default=All
  attachmentMode: All
  # The ext_proc to run (same shape as EnvoyExtensionPolicy.spec.extProc).
  extProc:
    backendRefs:
      - name: semantic-router-svc
        port: 8080
    processingMode:
      request: Buffered
      response: Skip
    messageTimeout: 250ms
```

### Attaching a policy via `targetRef`

Attachment is expressed on the **policy** (not the route): `spec.targetRef` selects
exactly one `AIGatewayRoute`. `AIGatewayRoute` and its rules are **unchanged** — no
`filterRefs` field is added.

### `attachmentMode`: where the policy is enabled

`attachmentMode` determines where the generated `CompositePerRoute` is attached for
the targeted `AIGatewayRoute`:

| `attachmentMode` | Behavior                                                                                                 |
| ---------------- | -------------------------------------------------------------------------------------------------------- |
| `FallbackOnly`   | Enable only on catch-all (`route-not-found`) routes.                                                     |
| `RouteOnly`      | Enable only on `x-ai-eg-model` or header-based route rules generated from the targeted `AIGatewayRoute`. |
| `All`            | Enable on both catch-all and route rules.                                                                |

### Request flow

```
  ┌────────┐   POST /v1/chat/completions      ┌──────────────────────────────┐
  │ Client │   x-tenant-id: premium           │ Envoy listener (HCM filters)  │
  │        │   {"model":"auto", ...}          │                               │
  └───┬────┘─────────────────────────────────►│                               │
      │                                        │ 1) match catch-all rule       │
      │                                        │    (no x-ai-eg-model yet)     │
      │                                        │ 2) composite ext_proc gate:   │
      │                                        │    x-tenant-id == premium ?   │
      │                                        │        yes → run SR ext_proc  │──► SR svc
      │                                        │            (mutate body,      │◄── 200 body
      │                                        │             set model=gpt-4o) │
      │                                        │ 3) AI Gateway ext_proc:       │
      │                                        │    parse body, set            │
      │                                        │    x-ai-eg-model=gpt-4o,       │
      │                                        │    ClearRouteCache            │
      │                                        │ 4) re-match header-keyed rule │
      │                                        │    x-ai-eg-model==gpt-4o       │
      │◄─────────────── response ──────────────│    → backend                  │
```

The composite runs **before** the AI Gateway `ext_proc`, so its body mutation is
in place by the time the model is derived; the existing `ClearRouteCache` re-match
then routes to the concrete backend. The two-route / landing-route machinery is
**reused**, not modified.

### How the ext_proc is added at `PostTranslateModify`

The AI Gateway extension server already patches xDS in `PostTranslateModify`
(`internal/extensionserver/post_translate_modify.go`) for InferencePool, the AI
Gateway router `ext_proc`, and quota rate limiting. We add one more step there,
structured like `maybeInjectQuotaRateLimiting`:

1. **Fetch the mapping.** List `AIGatewayExtensionPolicy`s (and, to resolve
   targets, `AIGatewayRoute`s) via `s.k8sClient`. For each policy `targetRef` that
   selects an `AIGatewayRoute`, build an entry:
   `{ policyName, headers, extProc, clusterName }` keyed by the target route.
2. **Ensure the ext_proc service cluster exists.** Because the backend is referenced
   from _our_ CRD, Envoy Gateway does not synthesize a cluster for it. We build one
   the same way `buildQuotaRateLimitCluster` / the EPP clusters are built, and append
   to `req.Clusters` (dedup like the `extProcUDSExist` guard).
3. **Insert a `Composite` filter (disabled) before the AI Gateway `ext_proc`.** Add
   a single composite filter (`Composite{}`, no matcher), **named with its canonical
   registered name `envoy.filters.http.composite`**, into the HCM filter chain with
   `disabled: true`, reusing the ordering logic from
   `insertRouterLevelAIGatewayExtProc` / `insertAIGatewayExtProcFilter`. A disabled
   filter does nothing until a route supplies per-route config, which both enables
   and configures it — exactly how the AI Gateway ext_proc itself is inserted
   disabled-at-HCM then enabled per route. **Do not** wrap it in
   `ExtensionWithMatcher` — that makes the match tree gateway-global and cannot be
   scoped per route.
4. **Build the per-route `CompositePerRoute`.** For each entry, construct an
   `extprocv3.ExternalProcessor` from `spec.extProc`, wrap it in a
   `filters.http.composite.v3.ExecuteFilterAction`, and place it as the `on_match`
   action of a `CompositePerRoute.matcher` whose `HttpRequestHeaderMatchInput`
   predicates come from the policy's `headers`.
5. **Enable the `CompositePerRoute` on the targeted rule routes and all catch-all
   routes** (next section) via `TypedPerFilterConfig["envoy.filters.http.composite"]`.
   The `CompositePerRoute` is **wrapped in an `envoy.config.route.v3.FilterConfig`**
   (`FilterConfig{ config: <CompositePerRoute Any> }`) before being stored on the
   route — the same wrapper the AI Gateway router `ext_proc` uses for per-route
   enablement. Supplying this per-route config re-enables the disabled composite for
   that route. On routes with no per-route config the composite stays disabled (pure
   no-op).

### Building the policy → route mapping (code)

Step 1 above is the correlation step, and it is worth spelling out because it is the
part reviewers most need to agree on. The extension server has no direct pointer
from an xDS route back to an `AIGatewayRoute`; instead it relies on the naming
convention that the generated `HTTPRoute` (and therefore the xDS route config) is
named after the `AIGatewayRoute` (`httproute/<namespace>/<name>/rule/<index>`, the
same convention `maybeModifyCluster` already parses). So the mapping is keyed by
`"<namespace>/<aiGatewayRouteName>"`, which lets us find the routes to enable while
walking route configs.

```go
// extensionPolicyEntry is one resolved (policy -> targeted AIGatewayRoute)
// attachment. Multiple entries on the same route share ONE composite filter and are
// combined into a single CompositePerRoute whose matcher has one arm per entry
// (keyed by that entry's headers). See the note after this snippet.
type extensionPolicyEntry struct {
	// name is the per-policy delegate name used for the ExecuteFilterAction's inner
	// filter (unique per policy). The composite filter name and the per-route
	// TypedPerFilterConfig key are the SHARED composite name, not this.
	name string
	// headers are the policy's gate headers used to gate the composite. Present on
	// both the first (catch-all) pass and the rule routes.
	headers []aigv1b1.AIGatewayExtensionPolicyHeaderMatch
	// extProc is copied verbatim from the AIGatewayExtensionPolicy spec.
	extProc egv1a1.ExtProc
	// clusterName is the Envoy cluster synthesized for the ext_proc backend.
	clusterName string
}

// buildExtensionPolicyEntries lists AIGatewayExtensionPolicies and returns a map
// keyed by "namespace/aiGatewayRouteName" -> entries (one per targetRef). Mirrors
// the shape of buildQuotaBackendPolicies (quota_ratelimit.go) and reads via
// s.k8sClient like listQuotaPolicies / maybeModifyCluster.
func (s *Server) buildExtensionPolicyEntries(ctx context.Context) (map[string][]extensionPolicyEntry, error) {
	var policies aigv1b1.AIGatewayExtensionPolicyList
	if err := s.k8sClient.List(ctx, &policies); err != nil {
		return nil, err
	}
	out := make(map[string][]extensionPolicyEntry)
	for i := range policies.Items {
		p := &policies.Items[i]
		for _, ref := range p.Spec.TargetRefs {
			if !isAIGatewayRouteTargetRef(ref) { // group+kind == AIGatewayRoute
				continue
			}
			// targetRefs are LocalPolicyTargetReference: the route lives in the
			// policy's own namespace.
			key := p.Namespace + "/" + string(ref.Name)
			out[key] = append(out[key], extensionPolicyEntry{
				name:        extensionPolicyName(p.Namespace, p.Name),
				headers:     p.Spec.Headers,
				extProc:     p.Spec.ExtProc,
				clusterName: extensionPolicyClusterName(p.Namespace, p.Name),
			})
		}
	}
	return out, nil
}
```

The consumer then walks `req.Routes`. For each route config it enables the composite
on (a) the **rule routes** of the targeted `AIGatewayRoute` with that route's own
policies, and (b) **every catch-all (`route-not-found`) route** with **all** policies
attached to _any_ `AIGatewayRoute` — because on the first pass a request funnels
through whatever single catch-all survives Gateway-API conflict resolution,
regardless of which route eventually owns it (this is the catch-all caveat from the
`attachmentMode` discussion above):

```go
// allEntries is the union of every policy attachment across all routes, deduped by
// policy name. Used to enable ALL policies on every catch-all route.
allEntries := unionEntries(entriesByRoute)

for _, routeCfg := range req.Routes {
	key := aiGatewayRouteKeyFromRouteConfigName(routeCfg.Name) // "ns/name" or ""
	routeEntries := entriesByRoute[key]
	for _, vh := range routeCfg.VirtualHosts {
		for _, r := range vh.Routes {
			var entries []extensionPolicyEntry
			switch {
			case isCatchAllRoute(r): // route name / metadata carries "route-not-found"
				entries = allEntries // all policies on every catch-all (first pass)
			case len(routeEntries) > 0:
				entries = routeEntries // policies targeting this route, on its rules
			}
			if len(entries) == 0 {
				continue
			}
			// Entries collapse into ONE CompositePerRoute whose matcher has one arm
			// per entry (gate = entry.headers, action = ExecuteFilterAction(entry.extProc)).
			// Keyed by the shared composite filter name, since typed_per_filter_config
			// must key on a listener filter. Supplying it re-enables the disabled composite.
			cpr := buildCompositePerRoute(entries) // matcher_list with len(entries) arms
			setTypedPerFilterConfig(r, extensionPolicyCompositeName, cpr)
		}
	}
}
```

Because `typed_per_filter_config` is keyed by filter name and there is a single
composite filter per listener, several policies enabled on the same route must be
merged into one `CompositePerRoute` with multiple matcher arms (not multiple map
entries). This is a natural fit: the `matcher_list` evaluates arms in order and runs
the first matching `ExecuteFilterAction`.

Because the map is rebuilt from the live CRDs on every `PostTranslateModify`, the
injected set is always **derived state** — there is nothing to reconcile or delete
by hand (see the lifecycle section below).

### Enablement: targeted routes and catch-all

The composite is added **disabled** at the HCM, then explicitly enabled per route by
attaching a `CompositePerRoute` via `TypedPerFilterConfig`. Enablement is applied to:

- **All rule routes of every targeted `AIGatewayRoute`** — so the composite still
  runs for a request whose _first_ pass matches a rule route directly (a rule keyed
  on client-supplied headers such as `x-tenant-id`), rather than transiting the
  catch-all. Uses the same route-identification approach as
  `enableRouterLevelAIGatewayExtProcOnRoute` / `isRouteGeneratedByAIGateway`.
- **All `route-not-found` catch-all routes** — with _every_ attached policy (not
  only the target route's), because on the first pass all traffic funnels through
  the single surviving catch-all (same prefix, no headers) after Gateway-API
  conflict resolution. Missing a policy there would make it unreachable on the first
  pass. The catch-all rule carries `Name: "route-not-found"`, which surfaces in the
  xDS route name and is matched on.

On any route without a `CompositePerRoute` the composite stays disabled and is a pure
no-op.

> **Single execution — the filter chain runs once (no double run).** A natural worry
> is that enabling the composite on both the catch-all and the rule routes makes the
> ext*proc run twice. It does not. Envoy runs the downstream HTTP filter chain
> **once per request**; `ClearRouteCache` only causes the \_route* to be re-selected
> by the router (and by filters that run _after_ the clear) — it does **not**
> re-execute filters that have already run. The composite sits ahead of the AI
> Gateway ext_proc, so it executes **exactly once**, against whatever route the
> request is on at that moment, and is not re-invoked when `ClearRouteCache` later
> swaps the route.
>
> So why enable it on the rule routes at all, if it only ever runs once? Because a
> request's _first_ pass does not always land on the catch-all. A rule keyed on
> **server-added** `x-ai-eg-model` is never matchable on the first pass, so those
> requests always run the composite on the **catch-all**. But a rule keyed purely on
> **client-supplied** headers (e.g. `x-tenant-id`) can be matched **directly** on the
> first pass, without transiting the catch-all. Enabling on both the catch-all and
> the targeted rule routes guarantees the composite runs on whichever route the first
> pass hits — still exactly once per request. Idempotency is therefore **not**
> required on account of re-routing.

### Composite placement vs. per-route configuration

A natural objection (and a genuine Envoy constraint) is that a composite wrapped in
**`ExtensionWithMatcher`** is a **gateway/listener-level** construct: its
`xds_matcher` match tree applies to the whole HCM filter chain and cannot be scoped
per route by toggling it on/off. That is true, and it is _not_ the mechanism this
proposal relies on.

Envoy exposes **two mutually-exclusive** ways to drive the composite filter (the
proto docs warn: never mix them — "Never set [the `matcher`] field when using the
Composite filter with the ExtensionWithMatcher which will result in undefined
behavior"):

| Mechanism                                                                 | Where the match tree lives                | Per-route?                                                                       |
| ------------------------------------------------------------------------- | ----------------------------------------- | -------------------------------------------------------------------------------- |
| `ExtensionWithMatcher{ xds_matcher }` wrapping the composite              | listener/HCM (gateway-level)              | only via `ExtensionWithMatcherPerRoute` _override_; wrapper anchored at listener |
| **bare `Composite{}` in `http_filters` + `CompositePerRoute{ matcher }`** | **the route's `typed_per_filter_config`** | **yes — this is the per-route API**                                              |

This proposal uses the **second** mechanism. The `envoy.filters.http.composite`
filter is present (disabled) in the HCM chain (its _presence_ is gateway-level —
which is true of every per-route filter: the filter must exist on the listener for a
route's `typed_per_filter_config` to bind, e.g. the AI Gateway ext_proc itself is
inserted disabled at the listener and enabled per route). The **match tree and the
`ExecuteFilterAction` that runs the policy's ext_proc live in a `CompositePerRoute`
attached to each enabled route** (the targeted rule routes and the catch-all
routes). Supplying that per-route config both re-enables the disabled composite for
the route and provides its matcher. `CompositePerRoute.matcher` is documented as the
"override of the match tree for this route," and `envoy.filters.http.ext_proc` is a
valid `ExecuteFilterAction` delegate.

Because we write this xDS directly in `PostTranslateModify` (rather than through
Envoy Gateway's `EnvoyExtensionPolicy`), we are bound only by raw-Envoy capability,
which supports `CompositePerRoute`. So per-route enablement holds — via
`CompositePerRoute`, not `ExtensionWithMatcher`.

References: [Composite filter proto](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/composite/v3/composite.proto)
(`Composite`, `CompositePerRoute`, `ExecuteFilterAction`),
[Composite filter docs](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/composite_filter),
[per-route matcher support (envoy#27088)](https://github.com/envoyproxy/envoy/pull/27088).

### Envoy version requirement (CompositePerRoute over RDS)

Making `CompositePerRoute` work through Envoy Gateway's RDS delivery has **two hard
requirements** that were only discovered while validating on a cluster. Both are
reflected in the implementation.

**1. The composite filter MUST use its canonical name, and the per-route config MUST
be wrapped in `FilterConfig`.** Since Envoy 1.22, per-route `typed_per_filter_config`
is resolved **strictly by the config's type URL** (the map-key name is ignored;
`no_extension_lookup_by_name` is on). Envoy Gateway also validates RDS route configs
**eagerly and independently of the listener filter chain**. Two consequences:

- The HCM composite filter is named `envoy.filters.http.composite` (the canonical
  registered name), and the per-route key matches it.
- The `CompositePerRoute` is wrapped in `envoy.config.route.v3.FilterConfig`
  (`FilterConfig{ config: <Any> }`) — a core route type that always passes eager RDS
  validation — exactly as the AI Gateway router `ext_proc` per-route enablement does.

**2. The data-plane Envoy MUST be new enough to register `CompositePerRoute` for
by-type resolution.** This is the important one and is the subject of **failure #2**
below.

> **Failure #2 — Envoy ≤ 1.38.x NACKs the entire RouteConfiguration.**
> On Envoy `1.37.0` and `1.38.3`, attaching a `CompositePerRoute` (raw _or_
> `FilterConfig`-wrapped) causes Envoy to reject the whole route config:
>
> ```
> gRPC config for type.googleapis.com/envoy.config.route.v3.RouteConfiguration
> rejected: Didn't find a registered implementation for
> 'envoy.filters.http.composite' with type URL:
> 'envoy.extensions.filters.http.composite.v3.CompositePerRoute'
> ```
>
> Because the RDS update is rejected, **every route disappears and all requests get
> `HTTP 404`** (not just the policy-targeted traffic).
>
> **Root cause (upstream Envoy):** in Envoy ≤ 1.38.x the composite factory is
> declared as `DualFactoryBase<Composite>` — passing only the `ConfigProto`
> template argument. Since `DualFactoryBase<ConfigProto, RouteConfigProto = ConfigProto>`,
> the route-config type silently **defaults to `Composite`**, so
> `createEmptyRouteConfigProto()` returns `Composite` and the factory's
> `configTypes()` never registers `CompositePerRoute` in the by-type registry. As a
> result `getFactoryByType("…CompositePerRoute")` returns `nullptr` and Envoy throws.
> (The bare `Composite{}` filter on the listener is accepted fine; only the
> _route-level_ `CompositePerRoute` is unresolvable.)
>
> **Fix / required version:** Envoy PR
> [#43996](https://github.com/envoyproxy/envoy/pull/43996) ("composite: add inline
> matcher support", commit `e842507299`, merged **2026-04-29**) changes the factory to
> `CommonFactoryBase<Composite, CompositePerRoute>` and adds
> `createRouteSpecificFilterConfigTyped(const CompositePerRoute&, …)`, which registers
> `CompositePerRoute` by type. This landed **after** the 1.38.x branch was cut, so it
> is present only on Envoy `main` / the eventual **1.39** release. **This feature
> therefore requires Envoy ≥ 1.39 (or a `main`/dev build ≥ `e842507299`).**
>
> **Concrete image used to validate:** `docker.io/envoyproxy/envoy:distroless-dev`
> (the rolling `main` distroless build, reporting `1.39.0-dev`). Set it on the
> `EnvoyProxy` CR the Gateway uses:
>
> ```yaml
> apiVersion: gateway.envoyproxy.io/v1alpha1
> kind: EnvoyProxy
> spec:
>   provider:
>     kubernetes:
>       envoyDeployment:
>         container:
>           image: docker.io/envoyproxy/envoy:distroless-dev
> ```
>
> Note: the stale `envoyproxy/envoy-dev` Docker Hub repo (last updated Aug 2025) does
> **not** contain the fix — use `envoyproxy/envoy:distroless-dev`.
>
> If a fixed Envoy is not available, the only 1.37/1.38-compatible alternative is to
> gate the composite at the **listener level** via `ExtensionWithMatcher` (gateway-wide,
> header-gated), giving up strict per-route scoping — see
> [Alternatives Considered](#alternatives-considered).

### Policy lifecycle: add, update, and remove

**Q: When an `AIGatewayExtensionPolicy` is deleted (or its `targetRef` / `attachmentMode` changes),
will the extension server remove the composite-wrapped `ext_proc`?**

**Yes — automatically, with no explicit teardown code.** `PostTranslateModify` is a
**stateless, full-recompute** hook: on every invocation it rebuilds the injected
xDS from scratch out of (a) the xDS Envoy Gateway hands it and (b) the current set
of `AIGatewayExtensionPolicy` / `AIGatewayRoute` CRDs it lists via `s.k8sClient`.
The composite is _derived state_, not stored state. So:

- **Delete the policy (or drop a `targetRef`)** → `buildExtensionPolicyEntries` no
  longer emits that entry → the next translation's xDS simply does not contain the
  composite's cluster or that policy's `CompositePerRoute` arm; if no policy remains,
  the composite is enabled on no route and stays a disabled no-op.

There is nothing stale to clean up because the injection is never persisted between
translations — this is exactly how the existing InferencePool / quota injections
behave.

**The one real requirement is _triggering_ a re-translation.** Envoy Gateway
re-runs `PostTranslateModify` when _its_ watched inputs change (Gateways,
HTTPRoutes, …). It does **not** watch our CRDs, and — importantly — an
`AIGatewayExtensionPolicy` change does **not** by itself change the generated
`HTTPRoute` (routing structure is untouched by the policy), so EG would not
otherwise notice. Two mechanisms cover this, both already used in the codebase:

1. **Controller watch → gateway resync.** The controller watches
   `AIGatewayExtensionPolicy` (and `AIGatewayRoute`); on any change it resolves the
   targeted routes and calls `syncGateways`, which sends a `GenericEvent` to the
   gateway controller and forces the Gateway (hence its xDS) to be re-translated.
2. **Encode attached-policy identity into an HTTPRoute annotation.** `newHTTPRoute`
   already sets a `HACK` annotation so EG reconciles when backend refs change
   (`httpRouteBackendRefPriorityAnnotationKey = buildPriorityAnnotation(...)`). The
   controller stamps an analogous annotation on each targeted route's generated
   `HTTPRoute` encoding the set of attached policies (e.g. a hash of policy
   names/generations). When a policy is added/removed/changed, the annotation
   changes → the `HTTPRoute` diffs → EG re-translates → the recompute drops (or adds)
   the composite. Because the annotation is stamped only on the **targeted** routes'
   HTTPRoutes (not on every route), the re-translation trigger stays proportional to
   the number of targeted routes.

Together these guarantee that add/update/remove of a policy deterministically
converges the injected xDS, with the composite removed as soon as the attachment is
gone.

### Alternative: EG-native re-translation via `policyResources` (no annotation hack)

Both triggering mechanisms above are _workarounds_ for the same root cause: Envoy
Gateway does not, by default, watch `AIGatewayExtensionPolicy`, and a policy change
does not alter the generated `HTTPRoute`, so EG never re-translates on its own. The
controller-driven `syncGateways` and — especially — the "stamp a hash annotation on
the targeted `HTTPRoute`s" trick (mechanism #2) exist only to _nudge_ EG into
re-translating. The annotation approach in particular is a hack: it mutates
`HTTPRoute`s the policy does not own purely as a change-detection side channel, and
it couples the re-translation blast radius to how many routes we stamp.

**Envoy Gateway can watch our CRD natively.** `EnvoyGateway.extensionManager`
exposes a `policyResources` field: any GVK listed there is treated as a
_directly-attached extension-server policy_. EG then (a) watches that GVK with a
generation-changed predicate, (b) re-enqueues the `GatewayClass` on every
create/update/delete, and (c) includes the policy objects in the resource tree it
translates. That re-enqueue is a full re-translation, so `PostTranslateModify`
re-runs and our stateless recompute picks up the change — **with no HTTPRoute
annotation, no `syncGateways`, and no field index.**

```yaml
# Envoy Gateway config (helm values / EnvoyGateway CR)
extensionManager:
  policyResources:
    - group: aigateway.envoyproxy.io
      version: v1alpha1
      kind: AIGatewayExtensionPolicy
  hooks:
    xdsTranslator:
      post: [Translation, Cluster, Route]
```

With this in place the controller no longer stamps the `extension-policies`
annotation and no longer resyncs gateways on policy changes; it shrinks to
status-only (or EG owns the policy status entirely). The extension server is
unchanged — it still lists policies via `s.k8sClient` and recomputes.

**Requirements and caveats (validated on a live cluster).** This path is not free;
EG imposes rules on `policyResources` objects:

- **A `targetRef` is mandatory.** EG's `processExtensionServerPolicies`
  rejects any object without one (`"not a policy object - no targetRef or targetRefs
found …"`) and drops it from the resource tree, so it never triggers re-translation.
  This means `targetRef` does double duty: it is both the user's declared scope and
  the re-translation trigger.
- **Same-namespace attachment only.** The target reference is a
  `LocalPolicyTargetReferenceWithSectionName` (no namespace), and EG resolves it in
  the _policy's own_ namespace (`resolveExtServerPolicyGatewayTargetRef` keys on
  `policy.GetNamespace()`). So the policy must live in the **same namespace as the
  target Gateway**; cross-namespace attachment is not supported today and needs an
  upstream EG change (tracked as a separate EG issue).
- **RBAC.** EG's ServiceAccount needs `get/list/watch` on the CRD and `update/patch`
  on its `/status` subresource (EG writes policy status). The AI Gateway CRDs helm
  chart must grant these to the EG SA.

#### Scoped enablement via `attachmentMode`

`targetRef` is always a single `AIGatewayRoute`. Scope is controlled by
`attachmentMode`:

| `attachmentMode` | Where the composite is enabled                                                                       |
| ---------------- | ---------------------------------------------------------------------------------------------------- |
| `FallbackOnly`   | Only catch-all (`route-not-found`) routes generated for the targeted `AIGatewayRoute`.               |
| `RouteOnly`      | Only `x-ai-eg-model` or other header-based rule routes generated from the targeted `AIGatewayRoute`. |
| `All`            | Both catch-all and rule routes generated from the targeted `AIGatewayRoute`.                         |

**Router-phase caveat for `AIGatewayRoute` scope.** Router-phase mutation must run on
the request's _first pass_, while it is still on the header-less catch-all and
`x-ai-eg-model` does not yet exist (see [the two-route
problem](#the-two-route--landing-route-problem-recap)); the HCM filter chain runs
once and `ClearRouteCache` does not re-run it. So `AIGatewayRoute`-scoped enablement
only reaches a first-pass request when that route is matchable on the first pass
(e.g. keyed on a **client** header, or served on a distinct path/host so its
catch-all does not collapse into the shared one). For the common single-endpoint
setup where all routes share `/v1/chat/completions` and every catch-all collapses to
one surviving rule, use `attachmentMode: All` to guarantee first-pass coverage for
that route's traffic; use `FallbackOnly` or `RouteOnly` only when that narrower
attachment behavior is explicitly desired.

## Code Changes

Brief, file-by-file. The intent is to mirror existing patterns (EPP ext_proc
injection, quota rate-limit injection) rather than introduce new machinery.

### 1. API types (`api/v1alpha1`)

- **`api/v1alpha1/ai_gateway_extension_policy.go`** (new): `AIGatewayExtensionPolicy`,
  `AIGatewayExtensionPolicyList`, `AIGatewayExtensionPolicySpec` (embedding
  `egv1a1.ExtProc`, plus `TargetRef` and `AttachmentMode`), and
  `AIGatewayExtensionPolicyStatus`.
  Kubebuilder markers copied from `AIGatewayRoute` / `QuotaPolicy`.
- **`api/v1alpha1/ai_gateway_route.go`**: **unchanged** (no `filterRefs`; attachment
  lives on the policy).
- **`api/v1alpha1/registry.go`**: register the new kinds in the scheme (alongside
  `AIGatewayRoute`, `QuotaPolicy`, etc.).

```go
// api/v1alpha1/ai_gateway_extension_policy.go
type AIGatewayExtensionPolicySpec struct {
	// TargetRef selects the AIGatewayRoute this policy attaches to.
	TargetRef gwapiv1a2.LocalPolicyTargetReference `json:"targetRef"`

	// AttachmentMode determines which underlying routes receive this policy.
	// Defaults to "All" to guarantee the extension is executed for all targeted traffic.
	//
	// +optional
	// +kubebuilder:default=All
	AttachmentMode *RouteAttachmentMode `json:"attachmentMode,omitempty"`

	// ExtProc mirrors EnvoyExtensionPolicy's extProc so semantics match EG.
	ExtProc egv1a1.ExtProc `json:"extProc"`
}

type RouteAttachmentMode string
```

### 2. Controller (`internal/controller`)

- Watch `AIGatewayExtensionPolicy`; on change, resolve `targetRef` and resync the
  owning gateways of the targeted `AIGatewayRoute`s (reuse `syncGateways`) so
  `PostTranslateModify` re-runs.
- Stamp the attached-policy annotation on each targeted route's generated
  `HTTPRoute` (see lifecycle); set Accepted / ResolvedRefs status conditions on the
  policy.
- `newHTTPRoute` route/rule structure is **unchanged** (only the annotation is
  added).
- Optionally add a field index `policy → gateways` for cheap lookups.

### 3. Extension server (`internal/extensionserver`)

- **`extension_policy.go`** (new): `maybeInjectAIGatewayExtensionPolicies(ctx,
clusters, listeners, routes)`, called from `PostTranslateModify` next to
  `maybeInjectQuotaRateLimiting`. Contains the fetch/mapping, cluster build,
  composite insertion, and per-route `CompositePerRoute` attachment described above.
  Uses `buildExtensionPolicyEntries` (the `targetRef`-keyed mapping above) and
  `enableCompositeOnTargetedAndCatchAllRoutes`.
- **`post_translate_modify.go`**: one added call in `PostTranslateModify`:

```go
req.Clusters, err = s.maybeInjectAIGatewayExtensionPolicies(ctx, req.Clusters, req.Listeners, req.Routes)
if err != nil {
	return nil, fmt.Errorf("failed to inject AIGatewayExtensionPolicies: %w", err)
}
```

- New helpers (all local to `extension_policy.go`): `buildExtensionPolicyCluster`,
  `insertCompositeBeforeAIGatewayExtProc` (adds `envoy.filters.http.composite`
  disabled, once per listener), `buildCompositePerRoute` (the `CompositePerRoute{
matcher → ExecuteFilterAction(ext_proc) }` construction), and
  `enableCompositeOnTargetedAndCatchAllRoutes`.

The heart of the injection is `buildCompositePerRoute`. It turns the resolved
`extensionPolicyEntry` list for one route into a single
`xds.type.matcher.v3.Matcher` (a `matcher_list`) with **one arm per policy**: each
arm's predicate ANDs that policy's `headers`, and its `on_match` action is an
`ExecuteFilterAction` that delegates to the entry's `ExternalProcessor`. The whole
matcher is wrapped in a `CompositePerRoute` and marshalled to an `Any`. The caller
then wraps that `Any` in an `envoy.config.route.v3.FilterConfig`
(`FilterConfig{ config: <CompositePerRoute Any> }`) before dropping it into the
route's `TypedPerFilterConfig` under `extensionPolicyCompositeName` — the
`FilterConfig` indirection is what lets the config survive Envoy Gateway's eager RDS
validation (it is a registered core route type), deferring the inner
`CompositePerRoute` to filter-chain association at runtime, exactly as the AI Gateway
router `ext_proc` per-route enablement does.

Note the two distinct `TypedExtensionConfig` families involved: the matcher tree
(inputs and actions) uses the **xDS** core/matcher types
(`github.com/cncf/xds/go/xds/...`), while the delegated filter inside
`ExecuteFilterAction` uses Envoy's **config.core.v3** `TypedExtensionConfig`. Mixing
them up is the most common compile/most-annoying-runtime error here.

```go
import (
	xdscorev3 "github.com/cncf/xds/go/xds/core/v3"
	xdsmatcherv3 "github.com/cncf/xds/go/xds/type/matcher/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	matcherv3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	compositev3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/composite/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
)

// buildCompositePerRoute builds the per-route composite config for one route from
// all policy attachments (entries) enabled on it. Attached on the route's
// TypedPerFilterConfig, keyed by extensionPolicyCompositeName; supplying it also
// re-enables the disabled composite. Uses CompositePerRoute (NOT ExtensionWithMatcher)
// so the match tree is per-route.
func buildCompositePerRoute(entries []extensionPolicyEntry) (*anypb.Any, error) {
	matchers := make([]*xdsmatcherv3.Matcher_MatcherList_FieldMatcher, 0, len(entries))
	for i := range entries {
		e := &entries[i]

		// (1) Build the delegated ext_proc as an ExecuteFilterAction. The inner
		// TypedExtensionConfig is Envoy's config.core.v3 flavor.
		extProcAny, err := toAny(e.extProc) // *extprocv3.ExternalProcessor
		if err != nil {
			return nil, err
		}
		execAny, err := toAny(&compositev3.ExecuteFilterAction{
			TypedConfig: &corev3.TypedExtensionConfig{
				Name:        e.name, // per-policy delegate name
				TypedConfig: extProcAny,
			},
		})
		if err != nil {
			return nil, err
		}

		// (2) Build the predicate that ANDs this policy's gate headers.
		predicate, err := buildHeaderPredicate(e.headers)
		if err != nil {
			return nil, err
		}

		// (3) One arm: predicate -> run the ExecuteFilterAction. The on_match
		// action is an xDS-core TypedExtensionConfig ("composite-action").
		matchers = append(matchers, &xdsmatcherv3.Matcher_MatcherList_FieldMatcher{
			Predicate: predicate,
			OnMatch: &xdsmatcherv3.Matcher_OnMatch{
				OnMatch: &xdsmatcherv3.Matcher_OnMatch_Action{
					Action: &xdscorev3.TypedExtensionConfig{
						Name:        "composite-action",
						TypedConfig: execAny,
					},
				},
			},
		})
	}

	// (4) Wrap all arms in a matcher_list and hand it to CompositePerRoute.
	return toAny(&compositev3.CompositePerRoute{
		Matcher: &xdsmatcherv3.Matcher{
			MatcherType: &xdsmatcherv3.Matcher_MatcherList_{
				MatcherList: &xdsmatcherv3.Matcher_MatcherList{Matchers: matchers},
			},
		},
	})
}

// buildHeaderPredicate turns a policy's gate headers into a predicate that requires
// ALL of them (logical AND). A single header collapses to one SinglePredicate;
// multiple headers become an and_matcher over SinglePredicates. (An empty list is
// handled by the caller as "no gate" — the arm always matches.)
func buildHeaderPredicate(hs []aigv1b1.AIGatewayExtensionPolicyHeaderMatch) (*xdsmatcherv3.Matcher_MatcherList_Predicate, error) {
	single := func(h aigv1b1.AIGatewayExtensionPolicyHeaderMatch) (*xdsmatcherv3.Matcher_MatcherList_Predicate, error) {
		// Input: envoy.type.matcher.v3.HttpRequestHeaderMatchInput, packed into an
		// xDS-core TypedExtensionConfig.
		inputAny, err := toAny(&matcherv3.HttpRequestHeaderMatchInput{HeaderName: h.Name})
		if err != nil {
			return nil, err
		}
		// Exact value match.
		sm := &xdsmatcherv3.StringMatcher{
			MatchPattern: &xdsmatcherv3.StringMatcher_Exact{Exact: h.Value},
		}
		return &xdsmatcherv3.Matcher_MatcherList_Predicate{
			MatchType: &xdsmatcherv3.Matcher_MatcherList_Predicate_SinglePredicate_{
				SinglePredicate: &xdsmatcherv3.Matcher_MatcherList_Predicate_SinglePredicate{
					Input: &xdscorev3.TypedExtensionConfig{Name: "request-headers", TypedConfig: inputAny},
					Matcher: &xdsmatcherv3.Matcher_MatcherList_Predicate_SinglePredicate_ValueMatch{
						ValueMatch: sm,
					},
				},
			},
		}, nil
	}

	if len(hs) == 1 {
		return single(hs[0])
	}
	preds := make([]*xdsmatcherv3.Matcher_MatcherList_Predicate, 0, len(hs))
	for _, h := range hs {
		p, err := single(h)
		if err != nil {
			return nil, err
		}
		preds = append(preds, p)
	}
	return &xdsmatcherv3.Matcher_MatcherList_Predicate{
		MatchType: &xdsmatcherv3.Matcher_MatcherList_Predicate_AndMatcher{
			AndMatcher: &xdsmatcherv3.Matcher_MatcherList_Predicate_PredicateList{Predicate: preds},
		},
	}, nil
}

// extensionPolicyCompositeName is the single composite filter shared by all
// AIGatewayExtensionPolicy attachments on a listener. Per-route CompositePerRoute
// entries key on this name.
//
// It MUST be the canonical registered composite name. Envoy validates RDS route
// configs independently of the listener filter chain and resolves a route's
// typed_per_filter_config strictly by type URL (name lookup is disabled since 1.22).
// A non-canonical name would fail resolution and Envoy would NACK the whole
// RouteConfiguration ("Didn't find a registered implementation … CompositePerRoute").
const extensionPolicyCompositeName = "envoy.filters.http.composite"

// insertCompositeBeforeAIGatewayExtProc inserts one composite filter (Composite{},
// no matcher, disabled) into each HCM filter chain of the listener, immediately
// before the AI Gateway ext_proc filter (aiGatewayExtProcName). Because it is added
// disabled, it does nothing until a route supplies a CompositePerRoute (which both
// enables and configures it), so it is safe to keep on every AIG listener. It is
// idempotent and mirrors the write-back style of insertRouterLevelAIGatewayExtProc /
// injectQuotaRateLimitFilterIntoListener.
//
// MUST run after the AI Gateway ext_proc has been inserted (i.e. after
// maybeModifyListenerAndRoutes), so the ordering anchor exists on the chain.
func (s *Server) insertCompositeBeforeAIGatewayExtProc(ln *listenerv3.Listener) error {
	filterChains := ln.GetFilterChains()
	if ln.DefaultFilterChain != nil {
		filterChains = append(filterChains, ln.DefaultFilterChain)
	}
	for _, currChain := range filterChains {
		httpConManager, hcmIndex, err := findHCM(currChain)
		if err != nil {
			return fmt.Errorf("failed to find HCM in filter chain: %w", err)
		}

		// Single pass over httpConManager.HttpFilters to compute:
		//   - alreadyPresent: is extensionPolicyCompositeName already in the chain?
		//   - aiGatewayIndex:  index of aiGatewayExtProcName (our ordering anchor).
		// (Trivial loop omitted for brevity.)
		alreadyPresent, aiGatewayIndex := scanCompositeAndAnchor(httpConManager.HttpFilters)

		if alreadyPresent {
			continue // Idempotent across re-translations.
		}
		if aiGatewayIndex == -1 {
			continue // No AI Gateway ext_proc on this chain => nothing to gate.
		}

		compositeAny, err := toAny(&compositev3.Composite{})
		if err != nil {
			return fmt.Errorf("failed to marshal Composite to Any: %w", err)
		}
		compositeFilter := &httpconnectionmanagerv3.HttpFilter{
			Name:       extensionPolicyCompositeName,
			Disabled:   true, // enabled per route via CompositePerRoute
			ConfigType: &httpconnectionmanagerv3.HttpFilter_TypedConfig{TypedConfig: compositeAny},
		}

		// Insert immediately before the AI Gateway ext_proc filter (append + shift).
		httpConManager.HttpFilters = append(httpConManager.HttpFilters, nil)
		copy(httpConManager.HttpFilters[aiGatewayIndex+1:], httpConManager.HttpFilters[aiGatewayIndex:])
		httpConManager.HttpFilters[aiGatewayIndex] = compositeFilter

		// Write the updated HCM back into the filter chain.
		hcAny, err := toAny(httpConManager)
		if err != nil {
			return fmt.Errorf("failed to marshal updated HCM to Any: %w", err)
		}
		currChain.Filters[hcmIndex].ConfigType = &listenerv3.Filter_TypedConfig{TypedConfig: hcAny}
	}
	return nil
}
```

`compositev3` is
`github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/composite/v3`.
Insertion is anchored on `aiGatewayExtProcName` rather than the router filter so the
composite is guaranteed to sit directly ahead of the AI Gateway ext_proc regardless
of what other ext_proc/EPP filters EG placed on the chain — the ordering guarantee
the `EnvoyPatchPolicy` workaround had to assert by hand.

### 4. Manifests, RBAC, generated code

- `manifests/charts/ai-gateway-crds-helm/templates/aigateway.envoyproxy.io_aigatewayextensionpolicies.yaml` (new CRD manifest). `AIGatewayRoute` CRD is unchanged.
- Add `aigatewayextensionpolicies` (+`/status`) to controller RBAC.
- Regenerate `zz_generated.deepcopy.go`, clientset/informers/listers, and
  `site/docs/api/api.mdx`.

### 5. Tests

- `internal/extensionserver/extension_policy_test.go`: given AIGatewayRoute + policy
  fixtures and a synthetic `PostTranslateModifyRequest`, assert the composite is
  inserted (disabled) before `ai-gateway-extproc`, gated by the right header
  matchers, the ext_proc cluster exists, and it's enabled on the targeted rule routes
  and on `route-not-found`.
- Controller tests for `targetRef` resolution/status and the attached-policy
  annotation.
- `api/v1alpha1` deepcopy/registry tests.
- `cmd/aigw` translate golden files if an example is added.

## End-to-end example: from CRDs to xDS

This section walks a single concrete example all the way from the user-authored
CRDs to the xDS that Envoy runs, so the whole path can be reviewed at once.

### Step 0 — user-authored CRDs

```yaml
# The extension policy (semantic router): targetRef + attachmentMode + extProc.
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayExtensionPolicy
metadata:
  name: semantic-router
  namespace: default
spec:
  targetRef:
    group: aigateway.envoyproxy.io
    kind: AIGatewayRoute
    name: chat
  attachmentMode: All
  extProc:
    backendRefs:
      - name: semantic-router-svc # a Service (or EG Backend) in the namespace
        port: 8080
    processingMode:
      request: Buffered
      response: Skip
    messageTimeout: 250ms
---
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayRoute
metadata:
  name: chat
  namespace: default
spec:
  parentRefs:
    - name: my-gateway
      kind: Gateway
  rules:
    # rule[0]: premium tenants (unchanged — no filter field on the route).
    - matches:
        - headers:
            - name: x-tenant-id
              value: premium
      backendRefs:
        - name: gpt-4o-backend
    # rule[1]: normal model-keyed routing.
    - matches:
        - headers:
            - name: x-ai-eg-model
              value: llama-3
      backendRefs:
        - name: llama-backend
```

### Step 1 — controller renders the HTTPRoute (unchanged structure + one annotation)

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: chat # same name as the AIGatewayRoute
  namespace: default
  annotations:
    aigateway.envoyproxy.io/generated: "true"
    # existing HACK annotation that forces EG re-translation on ref changes:
    aigateway.envoyproxy.io/backend-ref-priority: "<hash>"
    # NEW: encodes attached-policy identity so add/remove re-translates:
    aigateway.envoyproxy.io/extension-policies: "default/semantic-router@<gen>"
spec:
  rules:
    - matches:
        [
          {
            path: { value: /v1 },
            headers: [{ name: x-tenant-id, value: premium }],
          },
        ]
      backendRefs: [{ name: gpt-4o-backend, ... }]
    - matches:
        [
          {
            path: { value: /v1 },
            headers: [{ name: x-ai-eg-model, value: llama-3 }],
          },
        ]
      backendRefs: [{ name: llama-backend, ... }]
    - name: route-not-found # controller-injected catch-all (unchanged)
      matches: [{ path: { value: /v1 } }]
      filters: [{ extensionRef: ai-eg-route-not-found-response }]
```

Note the policy did **not** add an HTTPRoute filter or change any route match — it
only produced the `extension-policies` annotation on the targeted route.

### Step 2 — Envoy Gateway translates to xDS and calls `PostTranslateModify`

EG produces the baseline xDS (listener with HCM, route config
`httproute/default/chat/rule/*`, clusters for the backends). The AI Gateway
extension server then patches it. After `buildExtensionPolicyEntries` resolves:

```
entriesByRoute["default/chat"] = [
  {
    name:        "aigw-extpolicy/default/semantic-router",
    headers:     [ {name: x-tenant-id, value: premium} ],   // policy.spec.headers
    extProc:     <from AIGatewayExtensionPolicy>,
    clusterName: "aigw-extpolicy/default/semantic-router",
  },
]
```

the following xDS is added/modified.

**(a) A cluster for the ext_proc backend** (synthesized by us, since EG does not
create one for our CRD):

```yaml
name: aigw-extpolicy/default/semantic-router
type: STRICT_DNS
load_assignment:
  endpoints:
    - lb_endpoints:
        - endpoint:
            {
              address:
                {
                  socket_address:
                    { address: semantic-router-svc.default, port_value: 8080 },
                },
            }
```

**(b) A single `Composite` filter (disabled), inserted into the HCM before the AI
Gateway `ext_proc`.** No matcher here — it does nothing until a route supplies a
`CompositePerRoute` (which also re-enables it):

```yaml
http_filters:
  # ... (buffer, api-key auth, etc.) ...
  - name: envoy.filters.http.composite # NEW, composite (disabled)
    disabled: true # canonical registered name
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.composite.v3.Composite
  - name: envoy.filters.http.ext_proc/aigateway # existing AI Gateway ext_proc
    disabled: true
  - name: envoy.filters.http.router
```

**(c) The catch-all route enables + configures the composite (gate + delegate) and
enables the AI Gateway ext_proc.** The same `CompositePerRoute` is attached to the
targeted rule routes (`rule/0`, `rule/1`) as well:

```yaml
# route config: httproute/default/chat/rule/2  (the route-not-found catch-all)
- name: httproute/default/chat/rule/2/match/0-... # name resolves to route-not-found
  match: { prefix: /v1 }
  typed_per_filter_config:
    envoy.filters.http.ext_proc/aigateway:
      "@type": type.googleapis.com/envoy.config.route.v3.FilterConfig
      config: {}
    envoy.filters.http.composite: # canonical composite filter name
      # CompositePerRoute is wrapped in FilterConfig so it survives EG's eager RDS
      # validation (FilterConfig is a registered core route type); the inner
      # CompositePerRoute is resolved at filter-chain association time.
      "@type": type.googleapis.com/envoy.config.route.v3.FilterConfig
      config:
        "@type": type.googleapis.com/envoy.extensions.filters.http.composite.v3.CompositePerRoute
        matcher:
          matcher_list:
            matchers:
              - predicate:
                  single_predicate:
                    input:
                      "@type": type.googleapis.com/envoy.type.matcher.v3.HttpRequestHeaderMatchInput
                      header_name: x-tenant-id
                    value_match: { exact: premium } # from policy.spec.headers
                on_match:
                  action:
                    name: composite-action
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.composite.v3.ExecuteFilterAction
                      typed_config:
                        name: aigw-extpolicy/default/semantic-router
                        typed_config:
                          "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
                          grpc_service:
                            {
                              envoy_grpc:
                                {
                                  cluster_name: aigw-extpolicy/default/semantic-router,
                                },
                            }
                          processing_mode:
                            {
                              request_body_mode: BUFFERED,
                              response_header_mode: SKIP,
                            }
                          message_timeout: 0.250s
```

The composite is enabled on the catch-all **and** the targeted rule routes so it runs
on whichever route the request's _first_ pass matches. The per-route key is the
**canonical composite filter name** (`envoy.filters.http.composite`), not a
per-policy name, because `typed_per_filter_config` must key on a filter present in
the HCM chain and is resolved by type URL. The value is a `FilterConfig` wrapping the
`CompositePerRoute` so it passes Envoy Gateway's eager RDS validation.
The delegated ext_proc runs **exactly once**: the HTTP filter chain executes a single
time per request, and `ClearRouteCache` re-routing does not re-invoke the composite
(see Enablement note). For a model-routed request it runs on the catch-all pass; for
a request that matches `rule/0` directly via `x-tenant-id` on the first pass it runs
there instead.

### Step 3 — request-time behavior

```
POST /v1/chat/completions   x-tenant-id: premium   {"model":"auto", ...}

  match catch-all (rule/2)
    │
    ├─ composite gate: x-tenant-id == premium → run semantic-router ext_proc
    │      → body mutated: {"model":"gpt-4o", ...}
    │
    ├─ AI Gateway ext_proc: parse body → x-ai-eg-model=gpt-4o → ClearRouteCache
    │
    └─ re-match → rule[0] (x-tenant-id==premium) → gpt-4o-backend
                  (or a header-keyed model rule, per your route design)
```

A request **without** `x-tenant-id: premium` fails the composite gate on the
catch-all, the semantic router never runs, and it proceeds exactly as today — this
is the shrunk blast radius in action.

### Removal

Delete the `AIGatewayExtensionPolicy` (or drop its `targetRef`): the controller
rewrites the `extension-policies` annotation on the targeted route (and/or resyncs
the gateway) → EG re-translates → the recompute yields no entry for `default/chat`
→ the ext_proc cluster (a) is absent and no route gets a `CompositePerRoute`, so the
composite (b) stays a disabled no-op (c). No teardown code runs; nothing is
re-emitted.

## Validation on a live cluster

The end-to-end path was exercised on a real cluster to confirm the design works and
to surface the failures documented above. Setup:

- A trivial gRPC ext_proc (`testextproc/`) that logs every phase and stamps an
  `x-extproc-<phase>: seen` header on each of `REQUEST_HEADERS`, `REQUEST_BODY`,
  `RESPONSE_HEADERS`, `RESPONSE_BODY`, deployed as a `Deployment` + `Service`.
- An `AIGatewayExtensionPolicy` targeting an existing `AIGatewayRoute`, gated on
  `x-enable-extproc: "true"`, with `extProc.backendRefs` pointing at the test service.
- The AI Gateway controller image carrying this implementation, and — critically —
  the data-plane Envoy set to `docker.io/envoyproxy/envoy:distroless-dev`
  (`1.39.0-dev`, contains PR #43996).

Results:

- **Without** the `x-enable-extproc: true` header → `HTTP 200`, **no** `x-extproc-*`
  response headers; the ext_proc is never contacted (gate not matched).
- **With** the header → `HTTP 200` with `x-extproc-response-headers: seen` and
  `x-extproc-response-body: seen`; the test ext_proc logs show all four phases,
  confirming the composite-wrapped router-phase ext_proc ran.
- The final `EnvoyProxy`/Envoy config dump shows the `envoy.filters.http.composite`
  filter inserted **disabled** in the HCM chain ahead of the AI Gateway ext_proc, and
  the route-level `typed_per_filter_config` carrying the `FilterConfig`-wrapped
  `CompositePerRoute` with the header gate and the `ExecuteFilterAction` delegate.

## Why this avoids the catch-all blast radius

| Concern                        | EnvoyPatchPolicy / EnvoyExtensionPolicy   | `AIGatewayExtensionPolicy` (this proposal)              |
| ------------------------------ | ----------------------------------------- | ------------------------------------------------------- |
| Raw listener JSON patch        | yes (EPP)                                 | **no** (typed xDS at `PostTranslateModify`)             |
| Index-pinned / re-key on churn | yes (EPP)                                 | **no** (name-based)                                     |
| Filter ordering                | manual (`--extProcBeforeFilterNames`)     | **enforced** by insertion helper                        |
| Where it attaches              | shared catch-all (all first-pass traffic) | targeted routes + catch-all, but **gated** by `headers` |
| Blast radius on misconfig      | all catch-all traffic                     | **only traffic carrying the policy `headers`**          |
| Validated CRD                  | no (free-form JSON)                       | **yes**                                                 |

The routing mechanics are **reused**: the composite runs during the catch-all pass
and mutates the request; the AI Gateway `ext_proc` then derives `x-ai-eg-model` and
`ClearRouteCache` re-matches to the concrete backend.

## Alternatives Considered

- **`EnvoyPatchPolicy` composite-wrap / `EnvoyExtensionPolicy` on the route.** The
  status-quo workarounds; rejected for the shared-catch-all blast radius and (for
  EPP) index-pinned fragility.
- **A new HTTPRoute filter type instead of xDS injection.** Rejected because the
  request is on the catch-all on the first pass, so a route-attached filter cannot
  be scoped to the destination rule without the same catch-all problem.
- **Listener-level `ExtensionWithMatcher` gate (instead of per-route
  `CompositePerRoute`).** This is the only option that works on Envoy ≤ 1.38.x, which
  cannot resolve `CompositePerRoute` over RDS (failure #2). It header-gates the
  composite for the whole HCM filter chain rather than per route, giving up strict
  per-route scoping (the gate is gateway-wide). Preferred approach remains
  `CompositePerRoute` on Envoy ≥ 1.39; this is the compatibility fallback for older
  data planes.

## Open Questions

1. **Empty `headers`.** An empty `headers` list means "no gate" (runs for all
   first-pass traffic on the catch-all), which reintroduces the full blast radius.
   Should validation require at least one header, or accept empty only with an
   explicit opt-in field?
2. **Backend reference kinds.** Should `spec.extProc.backendRefs` allow EG `Backend`
   objects (needs address resolution) or only `Service` (simple STRICT_DNS)?
3. **Multiple policies per route / per gateway.** A single composite filter per
   listener; multiple policies enabled on one route merge into one `CompositePerRoute`
   with one matcher arm each. Since `matcher_list` runs the first matching arm, define
   arm ordering (policy name? creation time? most-specific first?) when several
   policies' `headers` overlap.
4. **First-pass coverage vs. rule-route enablement.** Because the filter chain runs
   once and `ClearRouteCache` does not re-run it, enabling on the targeted rule routes
   only matters for rules matchable on the first pass (client-header-keyed). For the
   common `x-ai-eg-model`-keyed rules it is a pure no-op (the request always runs the
   composite on the catch-all). Is it worth enabling on rule routes at all, or should
   enablement be catch-all-only to keep the injected xDS minimal?
5. **Cross-route catch-all fan-out.** In `All` / `FallbackOnly` mode, validate that
   enablement still scopes correctly to generated routes for the targeted
   `AIGatewayRoute`, especially when multiple routes share one surviving catch-all.
6. **Standalone mode / `cmd/aigw`.** Ensure the injection path honors
   `isStandAloneMode` and the offline translate flow.
7. **Status/observability.** Conditions on `AIGatewayExtensionPolicy` (Accepted,
   ResolvedRefs) and metrics for gate hit-rate and ext_proc latency.
8. **Triggering mechanism.** Adopt the EG-native
   [`policyResources`](#alternative-eg-native-re-translation-via-policyresources-no-annotation-hack)
   watch (no annotation hack, but requires a `targetRef`, same-namespace attachment,
   and EG RBAC) versus the controller-driven annotation/`syncGateways` trigger. If
   `policyResources` is adopted, do we scope by target kind (`Gateway` = all
   catch-alls, `AIGatewayRoute` = that route's routes only), and how do we handle the
   cross-namespace limitation upstream?
