---
slug: multi-user-mcp-header-forwarding
title: "Multi-User MCP in Production: Per-User Identity at the Gateway Layer"
authors: [mohitgurnani]
tags: [features]
description: How Envoy AI Gateway's header forwarding gives MCP tool calls per-user identity without OAuth infrastructure — and when you need OAuth instead.
image: /img/og/blog/multi-user-mcp-header-forwarding.png
---

![Per-user identity at the gateway layer](/img/og/blog/multi-user-mcp-header-forwarding.png)

When teams first deploy MCP servers in production, the path of least resistance is a shared service account: one API token for Jira, one for Slack, one for GitHub — stored as Kubernetes Secrets and used for every request, regardless of who triggered the agent. This works until it doesn't. Comments post under the wrong name, users see data their own accounts can't access, and the RBAC rules carefully configured in your enterprise tools are silently bypassed.

Envoy AI Gateway ships per-backend header forwarding in the `MCPRoute` API — available since v0.6 and part of the now-GA v1.0 — which solves this for a common enterprise case with a single YAML addition. This post covers how it works, how it plays out in a real production deployment, and — just as important — when you should reach for OAuth or token exchange instead.

<!-- truncate -->

## The Problem with Shared Service Accounts

Consider an internal agent platform routing dozens of concurrent agents through Envoy AI Gateway, each serving a different engineer or SRE. With a shared Jira service account, every agent has the same access regardless of the triggering user's actual permissions. Slack tools can reach channels the triggering engineer isn't a member of. Jira comments appear under the service account's name, not the engineer's. Nothing is technically broken — and that is the problem. The permission model of every downstream tool is being quietly flattened into "whatever the bot can do."

The instinct is to reach for OAuth. The reality is more nuanced, and it helps to first untangle what "authentication" actually means in an MCP deployment.

## Who Authenticates What: The Two Questions

Every multi-user MCP setup has to answer two _separate_ questions, and most confusion comes from mixing them together:

1. **Does the gateway trust the incoming request?** — the front door.
2. **Whose name does the backend tool see?** — what happens inside.

The first question is perimeter authentication. The gateway should verify that a request comes from someone (or something) in your organization before doing anything else. Envoy AI Gateway supports this through the `MCPRoute` security policy (API key or OAuth) or Envoy Gateway's `SecurityPolicy` (OIDC/JWT). You want this in production regardless of everything below.

The second question is where multi-user identity lives. Once a request is inside, each backend MCP server needs _a_ credential — and each backend decides what the caller can see based on _who that credential says they are_. There are four answers, and each is right for a different situation:

- **A shared service account.** The backend sees the same bot identity for every user. Right when the tool has no meaningful per-user permissions — a weather API, a public docs search. Wrong everywhere RBAC matters.
- **Forward the user's own credential.** The user already holds a credential the backend accepts — a personal access token, or a corporate SSO token for internal tools. The gateway passes it through, and the backend sees the actual user. This is the pattern this post covers.
- **The agent as its own identity.** A nightly automation that files summary tickets shouldn't impersonate anyone — it should _be_ a machine identity with narrow, fixed permissions (OAuth client credentials). Right for headless automation acting on its own behalf.
- **Token exchange (RFC 8693).** The user's corporate token means nothing to an external SaaS on a different identity provider. A token service exchanges it for a downstream token that still represents the same user. Right for per-user identity _across_ an IdP boundary — at the cost of real infrastructure.

There is also the full OAuth authorization code flow with browser consent, which the MCP specification mandates for interactive clients talking to external servers. It is the right answer when a human is present — and architecturally impossible for a headless agent firing at 3am with no browser.

Lined up side by side, here is how the three headless per-user options compare against the interactive OAuth flow above — the shared service account is left out since it does not carry user identity at all:

| Pattern                   | Headless? | Per-user RBAC?   | New infrastructure?                         | Right when                                 |
| ------------------------- | --------- | ---------------- | ------------------------------------------- | ------------------------------------------ |
| OAuth auth code + PKCE    | ❌        | ✅               | OAuth Authorization Server (AS) per tool    | Interactive clients, external SaaS         |
| Client credentials        | ✅        | Fixed scope only | App registration                            | Agent acts as itself                       |
| Token exchange (RFC 8693) | ✅        | ✅               | Security Token Service (STS) + trust config | Cross-IdP boundary                         |
| **Header forwarding**     | ✅        | ✅               | **None**                                    | User's credential already works downstream |

![Decision path for choosing a downstream identity pattern](/img/blog/multi-user-mcp-decision.png)

## How Header Forwarding Works

The key design property is what _doesn't_ happen: Envoy AI Gateway's MCP proxy builds a fresh HTTP request for every upstream call. Client headers do not propagate to backend MCP servers by default — at all. Forwarding is an explicit, per-backend opt-in via `forwardHeaders` on each backend reference.

![Fan-out with per-backend header scoping](/img/blog/multi-user-mcp-fanout.png)

This matters most during fan-out. A single `tools/list` call fans out to every backend in the route, but each backend receives only the headers it explicitly opted into. A per-user Atlassian token configured for the Atlassian backend is never seen by any other backend in the same route. The absence of `forwardHeaders` on a backend means no identity propagation to that service, full stop.

Each entry can also rename the header on the way through: `name` selects the inbound header, and an optional `backendHeader` sets the name used toward the backend — useful when a backend expects a vendor-specific header.

## Level 1: PAT Passthrough — Zero Infrastructure

This is the simplest production-ready configuration. Many enterprise MCP servers — Atlassian, Glean, Sourcegraph — accept per-user authentication via custom HTTP headers carrying a personal access token (PAT). Every engineer already has one. The gateway just needs to deliver it to the right backend, and only that one:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: internal-tools
  namespace: default
spec:
  parentRefs:
    - name: envoy-ai-gateway
      kind: Gateway
      group: gateway.networking.k8s.io
  path: "/mcp"
  backendRefs:
    # Atlassian MCP — forward each user's personal access tokens.
    - name: atlassian-mcp
      kind: Backend
      group: gateway.envoyproxy.io
      path: "/mcp"
      forwardHeaders:
        - name: X-Atlassian-Jira-Personal-Token
        - name: X-Atlassian-Confluence-Personal-Token

    # Public weather API — no forwarding, no user identity.
    - name: weather-mcp
      kind: Backend
      group: gateway.envoyproxy.io
      path: "/mcp"
      securityPolicy:
        apiKey:
          secretRef:
            name: weather-api-key
```

The agent's MCP client is configured once with the user's PAT headers, and every request carries them automatically. The gateway forwards them to the Atlassian backend only; the weather backend keeps using its shared API key and never sees a user token.

When a backend expects a different header name than your clients send, rename in transit:

```yaml
forwardHeaders:
  - name: Authorization # extract from the inbound request
    backendHeader: X-User-Token # forward under this name
```

The rename is also how per-user identity and a service-account credential coexist on one backend: forward the user's token under a dedicated header while the backend's `securityPolicy` injects the service credential into `Authorization`. Note that credentials injected by a backend `securityPolicy` are applied at the Envoy layer and overwrite any forwarded header of the same name — so give the forwarded user identity its own header rather than expecting it to take precedence.

Two honest caveats. First, in this mode the gateway is not validating the PATs — the backend is. Keep inbound authentication at the front door (an `MCPRoute` security policy, or OIDC at the gateway) so the gateway itself is never an open proxy. Second, forward identity headers only to backends inside your trust perimeter; if a backend cannot validate the credential, it has no business receiving it.

## Level 2: OIDC + JWT Forwarding — The Same-IdP Pattern

PAT passthrough needs no identity provider at all, which is exactly why it is the easiest place to start. Once your platform has corporate SSO wired up, the same `forwardHeaders` mechanism supports a stronger variant: validate the user's corporate JWT at the perimeter, then forward it to internal tools that already trust the same IdP.

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: SecurityPolicy
metadata:
  name: corporate-oidc
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: envoy-ai-gateway
  oidc:
    clientID: "${OIDC_CLIENT_ID}"
    clientSecret:
      name: oidc-client-secret
    redirectURL: "https://gateway.company.com/oauth2/callback"
    provider:
      issuer: "https://company.okta.com"
```

With this in place, the gateway rejects unauthenticated requests at the door, and a backend opting into `forwardHeaders: [{name: Authorization}]` receives the same corporate JWT the internal tool already accepts through SSO — short-lived, validated at the perimeter, revocable at the IdP, with zero configuration in the downstream tool. This is the natural hardening step for internal tools that natively validate your corporate identity.

## Stateless Passthrough vs. Token Broker

Gateway products in this space often take a different approach: the user completes a consent flow or pastes a token once, the gateway stores it server-side, and clients afterwards just connect to a URL. It is fair to ask whether Envoy AI Gateway's model — the client sends the credential on every request — is a step backwards.

It isn't; it is a different point in the design space. Sending a credential per request is the normal model of HTTP authentication — every GitHub API call carries a token, every browser request carries cookies, and the MCP specification itself has clients send `Authorization` on each HTTP request. A broker doesn't eliminate that; it moves the sending from client→backend to gateway→backend, and in exchange the gateway becomes a stateful token vault: encrypted storage, rotation, and a high-value breach target — with central revocation as the payoff.

Passthrough keeps the gateway stateless. There is nothing to steal at rest, nothing to rotate, and nothing to migrate; the credential rests with the client platform, which for an enterprise agent platform is typically already holding the user's session. Short-lived, IdP-issued JWTs favor passthrough strongly. Long-lived tokens for external SaaS — where storage, refresh, and revocation genuinely need management — favor a broker or token exchange. The two models are complementary, and the same route can serve both.

## What This Looks Like in Practice

Picture a platform aggregating a dozen MCP backends behind one route — Jira, Confluence, Slack, GitHub Enterprise, and internal observability tools, collectively exposing well over a hundred tools. Enabling per-user identity for the backends that need it is a YAML addition to the existing `MCPRoute`. No OAuth app registrations, no token storage, no consent flows, no changes to the backends themselves.

The results are exactly what the RBAC model promises: Jira comments attribute to the triggering engineer, not a bot. Confluence and Jira queries return only what each engineer's own account can see. Agents triggered by different engineers surface different data, matching each user's actual permissions. And because forwarding is per-backend, the user tokens never touch the public-API backends sharing the same route.

## What's Next

Header forwarding covers the case where the user's credential already works downstream. For the cross-IdP case — calling Atlassian Cloud or GitHub.com with per-user identity — RFC 8693 OAuth token exchange support is actively in review. Between the two, the gateway layer is becoming the natural place where agent traffic picks up the right identity for each tool it touches.

Learn more:

1. Header forwarding documentation: https://aigateway.envoyproxy.io/docs/capabilities/mcp/#header-forwarding
2. Feature PR: https://github.com/envoyproxy/ai-gateway/pull/2047
3. Design discussion: https://github.com/envoyproxy/ai-gateway/issues/1966
4. OAuth token exchange (in review): https://github.com/envoyproxy/ai-gateway/pull/2092
5. MCP authorization specification: https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization
