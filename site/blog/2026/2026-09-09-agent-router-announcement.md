---
slug: envoy-ai-gateway-is-now-agent-router
date: 2026-09-09
title: Envoy AI Gateway is becoming Agent Router, an Agentic AI Foundation project
authors: [maintainers]
tags: [announcements, news]
og_subtitle: Same code. Same maintainers. New home. Joining the Agentic AI Foundation on 10 September.
image: /img/og/blog/envoy-ai-gateway-is-now-agent-router.png
description: >-
  On 10 September, Envoy AI Gateway officially joins the Agentic AI Foundation as Agent Router.
  Same code, same maintainers, same APIs. Here is what changes, what does not, and where to find us.
---

![Agent Router. Formerly Envoy AI Gateway. Joining the Agentic AI Foundation on 10 September.](/img/og/blog/envoy-ai-gateway-is-now-agent-router.png)

On 10 September, **Agent Router**, previously **Envoy AI Gateway**, officially joins the
**Agentic AI Foundation**.

Same code. Same maintainers. New home.

<!-- truncate -->

## A new home

Envoy AI Gateway started as an Envoy sub-project within the CNCF. On 10 September it becomes a
standalone project and moves into the Agentic AI Foundation.

This is great news for the community. The AAIF is home to the agentic stack ecosystem, and being
part of it puts Agent Router closer to the projects and people building that stack. We get to
collaborate more directly, and keep building the solutions that power agents together with everyone
else in the foundation.

## What does not change

Nothing you deploy is renamed. The product name changes in prose only.

- **CRDs and API group stay as they are.** `AIGatewayRoute`, `AIServiceBackend`,
  `BackendSecurityPolicy`, and the `aigateway.envoyproxy.io` API group are not being renamed.
- **The CLI is still `aigw`**, and the `envoy-ai-gateway-system` namespace stays the same.
- **Same maintainers, same release cadence, same Apache 2.0 license.**
- **Envoy stays underneath.** Agent Router is still built on Envoy and Envoy Gateway.

Your manifests from yesterday apply tomorrow. There is nothing to migrate.

## What does change

- **The name.** Envoy AI Gateway becomes Agent Router.
- **The repository** moves to
  [`theagentrouter/agent-router`](https://github.com/theagentrouter/agent-router). Old
  `envoyproxy/ai-gateway` links redirect.
- **The website** moves to [theagentrouter.ai](https://theagentrouter.ai). Every
  `aigateway.envoyproxy.io` link redirects to the same page.
- **Community chat** moves to the [Agent Router Discord](https://discord.gg/xuxtPq43gZ).
  The weekly community meeting continues as before.

## Where to go next

- [Get started](/docs/getting-started/) or try it with one command on your laptop:
  `OPENAI_API_KEY=sk-... aigw run`
- [Star the repository](https://github.com/theagentrouter/agent-router) and open issues and
  discussions there from now on.
- [Join the Discord](https://discord.gg/xuxtPq43gZ) and the
  [weekly community meeting](https://zoom-lfx.platform.linuxfoundation.org/meeting/91546415944?password=61fd5a5d-41e9-4b0c-86ea-b607c4513e37).

Thank you to everyone who built, adopted, reviewed, and championed Envoy AI Gateway over the
past two years. The name on the door is new. The people behind it, and the code, are the same.

**Agent Router configures. Envoy handles the traffic.**
