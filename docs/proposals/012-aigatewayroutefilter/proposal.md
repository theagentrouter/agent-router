# Router-phase `ext_proc` via an `AIGatewayExtensionPolicy` CRD

A short, top-to-bottom read (goal, gap, CRD, usage scenarios, architecture). For the
full mechanics — xDS injection, Envoy composite placement, code changes, live-cluster
validation — see the companion **[detailed-proposal.md](./detailed-proposal.md)**.

## What are we aiming at?

Let users attach a **router-phase `ext_proc`** (a semantic router, PII redactor,
auth mutator, …) to AI Gateway traffic through a **typed, validated CRD** —
`AIGatewayExtensionPolicy` — instead of hand-written `EnvoyPatchPolicy` JSON. The
policy describes the ext_proc (reusing Envoy Gateway's `extProc` shape), says which
traffic it is **for** (`targetRef` to an `AIGatewayRoute`), and **where it runs**
(`attachmentMode`).

"Router-phase" = it runs **before** the AI Gateway decides the model/route, so it
can mutate the request body/headers that routing is based on.

## What does it lack today?

- **The two-route problem.** `x-ai-eg-model` is set server-side by the AI Gateway
  ext_proc, so a fresh request first lands on a header-less **catch-all** route; only
  after `ClearRouteCache` does it re-match the real route. Anything that must run
  before routing therefore has to hang off the shared catch-all.
- **Today's workarounds are fragile.** Slotting a second ext_proc in front is done
  via `EnvoyPatchPolicy` composite-wrap or `EnvoyExtensionPolicy` on the route:
  - raw, index-pinned JSON (RFC 6901 pointer into `http_filters[]`), unvalidated and
    version-fragile;
  - manual filter ordering (`--extProcBeforeFilterNames`);
  - it sits on the shared catch-all, so a mistake affects **all** traffic.
- **No first-class, gated, name-based option** that survives model/route churn and
  confines the blast radius to just the traffic that should be touched.

## Proposed CRD

```yaml
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: AIGatewayExtensionPolicy
metadata:
  name: semantic-router
  namespace: default
spec:
  # WHICH AIGatewayRoute this policy attaches to.
  targetRef:
    group: aigateway.envoyproxy.io
    kind: AIGatewayRoute
    name: chat
  # WHERE to attach the extension for this route's generated HTTPRoutes.
  # All (default) | FallbackOnly | RouteOnly
  attachmentMode: All
  # WHAT runs: same shape as EnvoyExtensionPolicy.spec.extProc.
  extProc:
    backendRefs:
      - name: semantic-router-svc
        port: 8080
    processingMode:
      request: Buffered
      response: Skip
    messageTimeout: 250ms
```

| Field            | Meaning                                                                                  |
| ---------------- | ---------------------------------------------------------------------------------------- |
| `targetRef`      | The single `AIGatewayRoute` this policy applies to.                                      |
| `attachmentMode` | Which generated HTTPRoute rules receive the policy (`FallbackOnly`, `RouteOnly`, `All`). |
| `extProc`        | The ext_proc definition, identical to `EnvoyExtensionPolicy.spec.extProc`.               |

`AIGatewayRoute` is **unchanged** — no new `filterRefs` field; attachment lives on
the policy.

## Usage scenarios

`attachmentMode` selects where the policy is enabled across HTTPRoutes generated for
the targeted `AIGatewayRoute`:

| `attachmentMode` | Behavior mapping                                                                                                            |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `FallbackOnly`   | Enable only on catch-all (`route-not-found`) routes.                                                                        |
| `RouteOnly`      | Enable only on route rules (for example `x-ai-eg-model` / header-based rules) generated from the targeted `AIGatewayRoute`. |
| `All` (default)  | Enable on both catch-all and route rules.                                                                                   |

This gives one clear policy target (`AIGatewayRoute`) while still controlling whether
the extension executes only on fallback traffic, only on route rules, or everywhere
for that route.

## Proposed architecture

### Control plane (controller + Envoy Gateway)

- A new controller **watches `AIGatewayExtensionPolicy`**, validates it, resolves
  `targetRef`, and sets status conditions (`Accepted`, `ResolvedRefs`).
- **Re-translation trigger** (so Envoy picks up policy edits): preferred path is
  registering the CRD under Envoy Gateway's `extensionManager.policyResources`, so EG
  watches it natively and re-translates on any change — **no HTTPRoute annotation
  hack**. (Requires a `targetRef`, same-namespace attachment to the Gateway, and EG
  RBAC on the CRD; details in [detailed-proposal.md](./detailed-proposal.md).)
- The generated `HTTPRoute` structure is **not changed** by the policy.

### Data plane (extension server at `PostTranslateModify`)

- List the `AIGatewayExtensionPolicy` objects and, for each, **synthesize an ext_proc
  cluster** for its `extProc.backendRefs`.
- Insert a **composite filter** (`envoy.filters.http.composite`) into the listener HCM
  chain, **added disabled**, **ordered before** the AI Gateway ext_proc.
- **Enable** it per route via a `CompositePerRoute` (wrapped in
  `envoy.config.route.v3.FilterConfig`) whose match tree is keyed on the policy
  attachment and whose action delegates to the ext_proc:
  - `FallbackOnly` → catch-all route(s) only
  - `RouteOnly` → route rules only
  - `All` → both.
- Injection is **stateless / full-recompute**: delete or edit a policy and the next
  translation simply omits or updates it — nothing to tear down.

> **Envoy version prerequisite.** Route-level `CompositePerRoute` over RDS needs
> **Envoy ≥ 1.39** (contains Envoy PR #43996); ≤ 1.38.x NACKs the `RouteConfiguration`.
> Validated on `envoyproxy/envoy:distroless-dev`. See [detailed-proposal.md](./detailed-proposal.md).

## Request flow (at a glance)

```
Client → catch-all route (no x-ai-eg-model yet)
       → composite gate: do the policy headers match?
           yes → run the wrapped ext_proc (mutate body/headers)
       → AI Gateway ext_proc: parse body, set x-ai-eg-model, ClearRouteCache
       → re-match the real model-keyed route → backend
```

The composite runs **before** the AI Gateway ext_proc on the first pass, so its
mutation is in place by the time the model is derived. Existing catch-all /
`ClearRouteCache` mechanics are **reused, not modified**.

## Open questions

- Should an **empty `headers`** gate be disallowed (or require explicit opt-in), since
  it re-widens the blast radius to all first-pass traffic?
- Which **backend kinds** should `extProc.backendRefs` allow (`Service` only vs. EG
  `Backend`)?
- **Trigger mechanism** and **cross-namespace** attachment for `policyResources`
  (same-namespace-only today).
- Ordering when **multiple policies** overlap on one route.

_Full treatment of each: [detailed-proposal.md](./detailed-proposal.md)._
