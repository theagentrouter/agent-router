# Per-agent token budgets for Agent Substrate actors

This example puts [Agent Router](https://github.com/theagentrouter/agent-router) behind the
egress gateway of [Agent Substrate](https://github.com/agent-substrate/substrate), so every
agent (a Substrate _actor_) gets its own daily token budget. It uses configuration only: no
changes to Substrate or Agent Router, and no extra services.

What you'll see:

1. Two actors call the same model through Agent Router, which meters tokens **per actor**.
2. Each actor has its **own daily budget**, from **one shared rate limit rule** keyed on the
   actor's identity.
3. When actor A's budget is spent, actor A gets `429`; actor B keeps working.
4. An actor can't claim to be another actor, and an actor without an egress rule can't reach
   Agent Router at all.
5. The model provider never sees the actor's identity token, only Agent Router's provider key.

It's written for platform engineers who already run agents on Kubernetes. Everything runs on
a local kind cluster against a mock LLM, so no API keys are needed.

## How it works

```
Actor (gVisor sandbox, no credentials)
  │  POST http://agent-router.envoy-gateway-system.svc.cluster.local/v1/chat/completions
  │  Authorization: Bearer placeholder
  ▼
atunnel ──mTLS CONNECT──▶ Substrate egress gateway (atenet-egress)
  • identifies the actor and checks its EgressPolicy
  • replaces Authorization with an actor JWT (sub = actor/<atespace>/<actor>)
  ▼
Agent Router (Envoy Gateway + Agent Router)
  1. JWT: verify against Substrate's JWKS, copy sub → x-actor-id
  2. Rate limit: one token bucket per x-actor-id
  3. Agent Router: route to the model, inject the provider key, count tokens in the response
  ▼
Mock LLM (OpenAI-compatible, 200 tokens per response)
```

Substrate vouches for _who_ is calling (the actor JWT). Agent Router decides _how much_ that
caller may spend, and holds the provider key: the actor never holds a credential.

Three pieces make up a per-agent budget, and none of them needs custom code:

| Piece                                          | Comes from                                                                                                          |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| **Bucket key**: which agent is counted         | the verified JWT subject, copied to `x-actor-id`                                                                    |
| **Counter**: tokens spent today                | the rate limit service (Redis), charged with the token count Agent Router reads from each response                  |
| **Bucket size**: how much each agent may spend | the rule's `limit` (the same for every actor here; see [Different budgets per actor](#different-budgets-per-actor)) |

Two details make the identity trustworthy:

- `x-actor-id` comes only from a JWT signed by Substrate's identity provider. A value the
  actor sends itself is replaced.
- Substrate's sandbox only resolves hostnames listed in the actor's egress policy, and its
  egress gateway refuses everything else. An actor with no rule for Agent Router can't
  reach it, and no actor here can reach the model directly.

## Prerequisites

- Docker. Tested with Docker Desktop on Apple Silicon with 8 CPUs and 24 GiB; the kind node
  itself uses about 3 GiB of memory and 4.5 GB of disk. Plan for about 15 GB of free disk
  including the local registry and the build caches for Substrate's images.
- `kubectl`, `helm` (v3), `go` (1.27 or later; Go downloads the exact toolchain it needs),
  `git`, `jq` and `curl`.
- Bash. macOS's bash 3.2 is fine.

`setup.sh` uses Substrate's own scripts, which fetch the pinned kind version through Go. You
don't need kind, ko or `kubectl-ate` installed: `setup.sh` builds `kubectl-ate` into `.work/bin`.

## Pinned versions

| Component          | Version                                                                                                                                                  |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agent Substrate    | `main` at [`404c7851`](https://github.com/agent-substrate/substrate/commit/404c7851ddb1f6a9a8d886870f66cd4ad251dfbd), Envoy dataplane, built from source |
| Agent Router       | `v1.2.1` (charts `oci://docker.io/envoyproxy/ai-gateway-crds-helm` and `ai-gateway-helm`)                                                                |
| Envoy Gateway      | `v1.9.3` (`oci://docker.io/envoyproxy/gateway-helm`); Agent Router v1.2 needs v1.9.2 or later                                                            |
| Rate limit service | `docker.io/envoyproxy/ratelimit:60d8e81b`, Redis `redis:8.2.4-alpine3.22` (from [`../token_ratelimit`](../token_ratelimit/))                             |
| Kubernetes         | `kindest/node:v1.37.0`, via kind `v0.33.0` pinned in Substrate's `hack/tools/kind`                                                                       |
| Example images     | Go `1.27.3`, base `gcr.io/distroless/static-debian13:nonroot` (pinned by digest in the Dockerfiles)                                                      |

Override `SUBSTRATE_SHA`, `ENVOY_GATEWAY_VERSION` or `AGENT_ROUTER_VERSION` to try other
versions. Substrate is pre-1.0 and its APIs change; the pinned commit is what was tested.

## Quick start

```shell
./setup.sh   # 3 to 7 minutes with warm caches; it builds Substrate from source
./test.sh    # 6 checks, each printing PASS or FAIL with evidence
./cleanup.sh # deletes the kind cluster (add --all to also remove the local registry and .work/)
```

`setup.sh`:

1. Clones Substrate at the pinned commit into `.work/substrate`.
2. Creates the kind cluster `agent-router-substrate` and a local registry on `localhost:5001`
   with Substrate's `hack/create-kind-cluster.sh`.
3. Installs Substrate with its credential provider off
   (`--credential-provider='{"enabled":false}'`). Actor JWTs don't need one.
4. Installs Redis, Envoy Gateway (with global rate limiting) and Agent Router.
5. Builds the mock LLM and actor images and pushes them to the local registry.
6. Applies the Agent Router configuration in [`agent-router/`](agent-router/).
7. Creates a gVisor worker pool, the `agents` atespace, the `llm-agent` actor template and
   three actors. Actors A and B get an egress policy for Agent Router; actor C gets none.
8. Waits for the first successful call from actor B.

To use a cluster name other than `agent-router-substrate`, set `KIND_CLUSTER_NAME` for all
three scripts.

## Walkthrough

The commands below assume you're in this directory, with the kind context selected:

```shell
kubectl config use-context kind-agent-router-substrate
alias kubectl-ate="$PWD/.work/bin/kubectl-ate"
```

### 1. Reach the actors

Substrate's ingress is mTLS-only, so use `kubectl ate proxy` to open a local port to each
actor (run these in a second terminal, or background them):

```shell
kubectl-ate proxy actor actor-a 28101:80 -a agents &
kubectl-ate proxy actor actor-b 28102:80 -a agents &
kubectl-ate proxy actor actor-c 28103:80 -a agents &
```

Each actor runs a small HTTP server ([`substrate/actor/main.go`](substrate/actor/main.go)).
`POST /` makes one chat completion from inside the sandbox and reports what came back:

```shell
curl -s localhost:28101/ -d '{}' | jq '{status, usage, headers}'
```

```json
{
  "status": 200,
  "usage": {
    "prompt_tokens": 50,
    "completion_tokens": 150,
    "total_tokens": 200
  },
  "headers": {
    "x-ratelimit-limit": "500, 500;w=86400",
    "x-ratelimit-remaining": "500",
    "x-ratelimit-reset": "22308",
    "x-upstream-auth": "provider-key",
    "x-upstream-saw-actor-id": "actor/agents/actor-a"
  }
}
```

`x-ratelimit-*` describes actor A's own bucket: 500 tokens per day, nothing spent before this
call. The two `x-upstream-*` headers come from the mock LLM: the model received Agent Router's
provider key (not the actor's JWT) and the actor identity Agent Router verified.

### 2. Spend actor A's budget

Each actor has 500 tokens per day and each call costs 200. Tokens are counted **after** each
response (Agent Router only knows the usage once the model reports it), and a request is
admitted while the bucket isn't yet over the limit. So calls 1 to 3 succeed (the bucket goes
200, 400, 600) and call 4 is refused:

```shell
for i in 1 2 3; do curl -s localhost:28101/ -d '{}' | jq -c '{status, remaining: .headers["x-ratelimit-remaining"]}'; done
```

```
{"status":200,"remaining":"300"}
{"status":200,"remaining":"100"}
{"status":429,"remaining":"0"}
```

(The call from step 1 already spent 200.) Actor B has its own bucket, so it isn't affected:

```shell
curl -s localhost:28102/ -d '{}' | jq -c '{status, limit: .headers["x-ratelimit-limit"], remaining: .headers["x-ratelimit-remaining"]}'
```

```
{"status":200,"limit":"500, 500;w=86400","remaining":"500"}
```

In general, a budget of N tokens against C tokens per call allows `floor(N/C) + 1` calls: an
agent can overshoot by up to one response.

### 3. Try to impersonate another actor

Actor A is out of budget; actor B still has some. Actor A now sends its own `x-actor-id`,
claiming to be actor B:

```shell
curl -s localhost:28101/ -d '{"headers":{"x-actor-id":"actor/agents/actor-b"}}' \
  | jq -c '{status, remaining: .headers["x-ratelimit-remaining"]}'
```

```
{"status":429,"remaining":"0"}
```

The JWT filter replaces the header with the verified `sub`, so the request is still counted
(and refused) as actor A. A pod that isn't an actor gets `401`, whether it sends no token, a
forged JWT or a bare `x-actor-id` header.

### 4. An actor without an egress rule

Actor C has no egress policy. Inside its sandbox, Agent Router's name doesn't resolve:

```shell
curl -s "localhost:28103/lookup?host=agent-router.envoy-gateway-system.svc.cluster.local" | jq -c .
```

```
{"addrs":null,"error":"lookup agent-router.envoy-gateway-system.svc.cluster.local on 169.254.17.1:53: no such host","host":"agent-router.envoy-gateway-system.svc.cluster.local","not_found":true}
```

Dialing the Service IP directly doesn't help either: Substrate's egress gateway refuses the
connection and logs `egress denied: actor has no egress policy`.

### 5. Streaming

Streamed responses pass through both hops and are charged when the stream ends:

```shell
curl -s localhost:28102/ -d '{"stream":true}' | jq -c '{status, chunks, content, usage}'
```

```
{"status":200,"chunks":8,"content":"Hello from the mock LLM.","usage":{"prompt_tokens":50,"completion_tokens":150,"total_tokens":200}}
```

The actor didn't ask for usage in the stream. Agent Router adds
`stream_options.include_usage` itself when token costs are configured, so it can count the
tokens.

### 6. See it in the access log

Envoy writes its access log every few seconds, so give it a moment after the calls above:

```shell
kubectl -n envoy-gateway-system logs -l gateway.envoyproxy.io/owning-gateway-name=agent-router -c envoy \
  | grep '^{"' | jq -c 'select(.path=="/v1/chat/completions") | {actor, total_tokens, response_code, ratelimit_remaining}'
```

```
{"actor":"actor/agents/actor-a","total_tokens":200,"response_code":200,"ratelimit_remaining":"500"}
{"actor":"actor/agents/actor-a","total_tokens":null,"response_code":429,"ratelimit_remaining":"0"}
{"actor":"actor/agents/actor-b","total_tokens":200,"response_code":200,"ratelimit_remaining":"500"}
```

## The configuration

### Substrate: let the actor reach Agent Router, with an actor JWT

[`substrate/egress-policy-a.yaml`](substrate/egress-policy-a.yaml), applied with
`kubectl ate create egress-policy actor-a -a agents -f substrate/egress-policy-a.yaml`:

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

- The gateway **replaces** a header the actor sends; it doesn't add one. The actor must send
  some `Authorization` header (here `Bearer placeholder`).
- Use the full `.svc.cluster.local` name, in both the rule and the URL the actor calls: only
  listed hostnames resolve inside the sandbox.
- The gateway caches policies for about 10 seconds, so a new policy can take that long to apply.

### Agent Router: verify the actor, then count its tokens

**Gateway** ([`agent-router/gateway.yaml`](agent-router/gateway.yaml)). The `EnvoyProxy` gives
the Gateway's Service a fixed name (`agent-router` in `envoy-gateway-system`), which is the URL
actors call, and logs each request's actor and token count.

**Trust Substrate's identity provider** ([`agent-router/idp-trust.yaml`](agent-router/idp-trust.yaml)).
A `Backend` for `idp.ate-system.svc` and a `BackendTLSPolicy` that trusts Substrate's Service
DNS CA straight from its ClusterTrustBundle. The certificate's only DNS name is
`idp.ate-system.svc`, so that's the `hostname` to verify:

```yaml
validation:
  caCertificateRefs:
    - group: certificates.k8s.io
      kind: ClusterTrustBundle
      name: servicedns.podcert.ate.dev:identity:primary-bundle
  hostname: idp.ate-system.svc
```

**Verify the actor JWT** ([`agent-router/security.yaml`](agent-router/security.yaml)):

```yaml
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

**One rate limit rule for every actor** ([`agent-router/ratelimit.yaml`](agent-router/ratelimit.yaml)):

```yaml
rateLimit:
  type: Global
  global:
    rules:
      - clientSelectors:
          - headers:
              - name: x-actor-id
                type: Distinct # a bucket per actor
        shared: true # ...across every route on the Gateway
        limit:
          requests: 500 # tokens per actor per day
          unit: Day
        cost:
          request: { from: Number, number: 0 } # check on the way in, don't spend
          response: # charge the reported tokens at the end
            from: Metadata
            metadata:
              namespace: io.envoy.ai_gateway
              key: llm_total_token
```

- Without `shared: true`, each route rule (for example, one per model) gets its own bucket and
  the budget is really per actor per model.
- `unit: Day` is a fixed UTC day: budgets reset at 00:00 UTC.

**Route, provider key and token counting** ([`agent-router/route.yaml`](agent-router/route.yaml)).
An `AIGatewayRoute` to the mock LLM, with `llmRequestCosts` writing each response's total
tokens to `io.envoy.ai_gateway/llm_total_token`, and a `BackendSecurityPolicy` holding the
(fake) provider key. The mock refuses any request that doesn't carry that key.

Envoy Gateway forwards the verified JWT upstream, so the `AIServiceBackend` removes it:

```yaml
headerMutation:
  remove:
    - authorization
```

Agent Router sets the provider credential after this removal, so it's safe on every backend.
It matters most for providers that authenticate with another header (`AnthropicAPIKey` sets
`x-api-key`, `AzureAPIKey` sets `api-key`): without it, the actor's JWT would reach the
provider.

## Different budgets per actor

The example gives every actor the same budget. The bucket key (`x-actor-id`) already
identifies each actor, so giving some actors a different _size_ is a matter of where the
number comes from.

**Per-actor rules (released Envoy Gateway).** When several rules match a request, all of them
apply and any one can refuse it. Add a rule for the actor and exclude it from the default
with `invert`, so it isn't held to the default as well. Tested with Envoy Gateway v1.9.3:
actor A was refused after 500 tokens, actor B allowed 5,000.

```yaml
rules:
  # Default: 500 tokens/day for every actor except actor-b.
  - clientSelectors:
      - headers:
          - { name: x-actor-id, type: Distinct }
          - {
              name: x-actor-id,
              type: Exact,
              value: actor/agents/actor-b,
              invert: true,
            }
    shared: true
    limit: { requests: 500, unit: Day }
    cost: { ... } # as above
  # actor-b: 5000 tokens/day.
  - clientSelectors:
      - headers:
          - { name: x-actor-id, type: Exact, value: actor/agents/actor-b }
    shared: true
    limit: { requests: 5000, unit: Day }
    cost: { ... }
```

Budgets live in the `BackendTrafficPolicy`, so changing one is a gateway config change. The
same pattern works for API keys: Envoy Gateway's API key authentication can put the client ID
in a header (`apiKeyAuth.forwardClientIDHeader`), and the rules key on that header instead.

**Per-value overrides (proposed).** [envoyproxy/gateway#9771](https://github.com/envoyproxy/gateway/pull/9771)
(open) adds `overrides` to a `Distinct` rule, so per-key limits sit next to the default without
the `invert` bookkeeping.

**Budgets from another system.** To change budgets without touching gateway config, the rule
can read each request's limit from dynamic metadata (`limit.fromMetadata`, Envoy Gateway v1.9.0
or later), written by an ext_authz service that looks up the actor. This needs a service, and
an `EnvoyProxy.spec.filterOrder` entry so the JWT filter runs before ext_authz. We verified
this while building the example, but it isn't part of it.

## Things to know

- **Agents must call Agent Router's URL.** This is an integration on top of Substrate's
  egress gateway, not a replacement for it: Substrate decides _whether_ an actor may reach
  Agent Router, and Agent Router decides how many tokens it may spend.
- **Every model call takes two hops.** Measured on a laptop kind cluster, the full path adds
  about 3 ms at the median and 5 ms at p95 over calling the model directly.
- **Overshoot.** Tokens are charged after the response, so an agent can exceed its budget by
  up to one response.
- **Keep the JWT away from providers.** When you swap the mock for a real provider, keep
  `headerMutation: {remove: [authorization]}` on its `AIServiceBackend`.
- **Many models.** Keep `shared: true` so each actor has one budget across all routes.
- **The provider sees `x-actor-id`.** It goes upstream with the request. Keep it there: the rate
  limit reads it again to charge the response, so if you add it to `headerMutation.remove`,
  nothing gets charged.

## Files

```
├── setup.sh / test.sh / cleanup.sh
├── agent-router/          # Gateway, route, JWT, IdP trust, rate limit rule
├── mock-llm/              # OpenAI-compatible mock, 200 tokens per response, plain and SSE
└── substrate/
    ├── actor/             # the demo actor: calls Agent Router with a placeholder key
    ├── workerpool.yaml    # gVisor WorkerPool (modeled on Substrate's demos/egress)
    ├── template.sh        # creates the atespace and ActorTemplate (pinned by digest)
    └── egress-policy-a.yaml, egress-policy-b.yaml
```

## Cleanup

```shell
./cleanup.sh       # delete the kind cluster
./cleanup.sh --all # also remove the local registry container and .work/
```
