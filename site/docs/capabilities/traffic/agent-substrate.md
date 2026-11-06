---
id: agent-substrate
title: Use Agent Router from Agent Substrate
sidebar_position: 5.5
---

[Agent Substrate](https://github.com/agent-substrate/substrate) runs agents as _actors_ in
sandboxes and sends their outbound traffic through an egress gateway that decides what each
actor may reach. That gateway can also put an **actor JWT** on a request: a short-lived token,
signed by Substrate, whose subject names the actor (`actor/<atespace>/<actor>`).

Agent Router can use that identity as the key for usage-based rate limiting, so every agent
gets its own token budget from one rate limit rule, and no agent holds a provider credential.
This page explains the setup; the
[`examples/agent-substrate`](https://github.com/theagentrouter/agent-router/tree/main/examples/agent-substrate)
example builds all of it on a kind cluster and tests it.

:::tip Try it
`./setup.sh && ./test.sh` in `examples/agent-substrate` creates a kind cluster with Substrate,
Envoy Gateway and Agent Router, then checks per-actor metering, budget exhaustion, identity
forgery, egress denial and streaming.
:::

## How it works

```
Actor (sandbox, no credentials)
  │  POST http://agent-router.envoy-gateway-system.svc.cluster.local/v1/chat/completions
  │  Authorization: Bearer placeholder
  ▼
Substrate egress gateway
  • checks the actor's EgressPolicy
  • replaces Authorization with an actor JWT (sub = actor/<atespace>/<actor>)
  ▼
Agent Router
  1. JWT: verify against Substrate's JWKS, copy sub → x-actor-id
  2. Rate limit: one token bucket per x-actor-id
  3. Route to the model, inject the provider key, count tokens in the response
  ▼
Model provider
```

Substrate answers _who is calling_ and _whether it may reach Agent Router at all_. Agent Router
answers _how many tokens it may spend_ and holds the provider credentials.

## Prerequisites

- Agent Substrate with actor JWT support, installed with its default issuer
  (`https://idp.ate-system.svc`). Actor JWTs don't need a credential provider, so
  `--credential-provider='{"enabled":false}'` is enough.
- Agent Router with global rate limiting enabled (Redis). See
  [Usage-based Rate Limiting](./usage-based-ratelimiting.md).

## 1. Let actors reach Agent Router with an actor JWT

Give each actor an egress rule for Agent Router's Service, with an `actorJwt` effect:

```yaml
rules:
  - http:
      hostnames: ["agent-router.envoy-gateway-system.svc.cluster.local"]
      effects:
        replaceHeaders:
          - header: Authorization
            prefix: "Bearer "
            actorJwt:
              audiences: ["agent-router"]
              expirationSeconds: 900
```

```shell
kubectl ate create egress-policy actor-a -a agents -f egress-policy.yaml
```

- The gateway **replaces** the header; it doesn't add one. The agent must send an
  `Authorization` header (any value, such as `Bearer placeholder`). Without it the request
  arrives with no token and Agent Router returns `401`.
- Only hostnames listed in the policy resolve inside the sandbox. Use the full
  `.svc.cluster.local` name in both the rule and the agent's base URL.
- To give Agent Router's Service a stable name, set `envoyService.name` on the Gateway's
  `EnvoyProxy`. Envoy Gateway creates the Service in its own namespace.

## 2. Trust Substrate's identity provider

Substrate serves its signing keys at `https://idp.ate-system.svc/openid/v1/jwks`, with a
certificate from its Service DNS signer. Point a `Backend` at it and trust the CA from the
ClusterTrustBundle directly, so a CA rotation needs no copy step:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: substrate-idp
spec:
  endpoints:
    - fqdn:
        hostname: idp.ate-system.svc.cluster.local
        port: 443
---
apiVersion: gateway.networking.k8s.io/v1
kind: BackendTLSPolicy
metadata:
  name: substrate-idp
spec:
  targetRefs:
    - group: gateway.envoyproxy.io
      kind: Backend
      name: substrate-idp
  validation:
    caCertificateRefs:
      - group: certificates.k8s.io
        kind: ClusterTrustBundle
        name: servicedns.podcert.ate.dev:identity:primary-bundle
    hostname: idp.ate-system.svc
```

The certificate's only DNS name is `idp.ate-system.svc` (no `.cluster.local`), so that's the
`hostname` to verify.

## 3. Verify the actor JWT

Verify the token and copy its subject into `x-actor-id`:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: SecurityPolicy
metadata:
  name: agent-router
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: agent-router
  jwt:
    providers:
      - name: agent-substrate
        issuer: https://idp.ate-system.svc
        audiences: [agent-router]
        remoteJWKS:
          uri: https://idp.ate-system.svc/openid/v1/jwks
          backendRefs:
            - group: gateway.envoyproxy.io
              kind: Backend
              name: substrate-idp
              port: 443
        claimToHeaders:
          - header: x-actor-id
            claim: sub
```

`claimToHeaders` replaces any `x-actor-id` the agent sent, so an actor can't claim to be
another actor. Requests without a valid actor JWT get `401`.

## 4. One rate limit rule for every actor

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: actor-token-budget
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: agent-router
  rateLimit:
    type: Global
    global:
      rules:
        - clientSelectors:
            - headers:
                - name: x-actor-id
                  type: Distinct
          shared: true
          limit:
            requests: 500000 # tokens per actor per day
            unit: Day
          cost:
            request: { from: Number, number: 0 }
            response:
              from: Metadata
              metadata:
                namespace: io.envoy.ai_gateway
                key: llm_total_token
```

- `Distinct` on `x-actor-id` gives each actor its own bucket.
- `shared: true` makes that bucket span every route on the Gateway. Without it, each
  `AIGatewayRoute` rule (for example, one per model) has its own bucket, so the budget would be
  per actor _per model_.
- The cost comes from `llmRequestCosts` on the `AIGatewayRoute` (a `TotalToken` cost with
  `metadataKey: llm_total_token`). See [Usage-based Rate Limiting](./usage-based-ratelimiting.md).

## 5. Keep the actor's JWT away from providers

Envoy Gateway forwards the verified JWT upstream. For OpenAI-style backends Agent Router
overwrites `Authorization` with the provider key, but backends that authenticate with another
header (`AnthropicAPIKey` sets `x-api-key`, `AzureAPIKey` sets `api-key`) would pass the actor's
JWT to the provider. Remove it on the `AIServiceBackend`; the provider credential is set after
this removal, so it's safe on every backend:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: my-provider
spec:
  schema:
    name: OpenAI
  backendRef:
    name: my-provider
    kind: Backend
    group: gateway.envoyproxy.io
  headerMutation:
    remove:
      - authorization
```

## Different budgets per actor

The rule above gives every actor the same budget. To give specific actors a different one:

- **Per-actor rules.** When several rules match a request, all of them apply. Add a rule with
  an `Exact` match on the actor's ID and its own `limit`, and exclude that actor from the
  default rule with a second header match using `invert: true`, so the default doesn't cap it
  too. Changing a budget means editing the `BackendTrafficPolicy`.
- **Budgets from another system.** To change budgets without touching gateway config, set
  `limit.fromMetadata` on the rule (Envoy Gateway v1.9.0 or later) and have an ext_authz
  service write each actor's limit, as described in
  [Per-Tenant Limits Without Per-Tenant Policies](./usage-based-ratelimiting.md#per-tenant-limits-without-per-tenant-policies).
  Envoy Gateway runs ext_authz before JWT authentication by default, so add this to the
  Gateway's `EnvoyProxy` for the service to see the verified `x-actor-id`:

  ```yaml
  filterOrder:
    - name: envoy.filters.http.jwt_authn
      before: envoy.filters.http.ext_authz
  ```

## Limitations

- **Agents must call Agent Router's URL.** Agent Router only sees traffic addressed to it.
  Point the agent's model base URL at Agent Router, and don't give actors egress rules to the
  providers directly.
- **Every model call takes two hops**: Substrate's egress gateway, then Agent Router. In the
  example on a laptop kind cluster, this added about 3 ms at the median and 5 ms at p95 over
  calling the model directly.
- **This builds on Substrate's egress gateway; it doesn't replace it.** Substrate still decides
  what each actor may reach; Agent Router adds model routing, provider credentials and token
  budgets for the traffic sent to it.
- **The provider sees the actor's ID.** `x-actor-id` goes upstream with the request. Keep it there:
  the rate limit reads it again to charge the response, so if you add it to `headerMutation.remove`,
  nothing gets charged.
- **Budgets can be overshot by one response.** Tokens are charged when the response ends, so a
  budget of N tokens against C tokens per call allows `floor(N/C) + 1` calls.
- **`Day` is a fixed UTC day.** Budgets reset at 00:00 UTC, not 24 hours after first use.
