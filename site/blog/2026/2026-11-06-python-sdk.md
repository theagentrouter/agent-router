---
slug: python-sdk
date: 2026-11-06
title: "Keep LiteLLM's API, put Envoy behind it: introducing the Agent Router Python SDK"
authors: [nacx, arif]
tags: [announcements, features]
og_title: Introducing the Agent Router Python SDK
og_subtitle: Keep LiteLLM's API, put Envoy behind it.
og_logo: python
og_code: from theagentrouter.compat import litellm
image: /img/og/blog/python-sdk.png
description: >-
  The Agent Router Python SDK keeps LiteLLM's API and sends every call through Agent Router, the
  open-source AI gateway built on Envoy. Fallbacks, retries, request limits and provider keys move out
  of your Python process and into the gateway. Moving over is usually one changed import.
---

![Introducing the Agent Router Python SDK](/img/og/blog/python-sdk.png)

If you build agents in Python, there's a good chance LiteLLM is somewhere in your dependency tree.
It gave the ecosystem something it badly needed: one `completion()` call for every model provider.
Google ADK and the OpenAI Agents SDK both lean on it, and so do countless internal tools.

The API isn't the problem. The problem is where all the hard work happens. With a library,
fallbacks, retries, rate limits and provider keys all live inside your Python process, on every
laptop, CI runner and container that runs your agent. Production systems have a well-understood
answer to that pattern, and it's a gateway.

Today we're introducing the
**[Agent Router Python SDK](https://github.com/theagentrouter/agent-router-python)**. It keeps
LiteLLM's API (same functions, same arguments, same response objects, same error names) and sends
every call through Agent Router, the open-source AI gateway built on Envoy. Moving over is usually
one changed import:

```python
from theagentrouter.compat import litellm   # was: import litellm
```

<!-- truncate -->

## Why a gateway, not a library

The SDK's design comes down to one rule: **the SDK never calls a provider itself.** Every request
goes to an Agent Router gateway, either one the SDK runs on your machine or one your team operates.
That split buys you a few things a library can't:

- **Resilience runs in the gateway, not your process.** Fallbacks, retries and request limits are
  defined once and enforced by Envoy, the same proxy that already carries production traffic at a
  large share of the internet's infrastructure. Every program on the machine, or every service
  pointed at the shared gateway, gets the same behavior.
- **Provider keys stay on the gateway.** Point the SDK at a shared gateway with two environment
  variables, and laptops and CI jobs hold a client key, not your OpenAI, Anthropic or Bedrock
  credentials.
- **A much smaller footprint.** `theagentrouter-core` has exactly one runtime dependency: `httpx`.
  There are no provider SDKs, and the model catalog ships with each release instead of being
  downloaded on import. After this year's supply-chain incidents across the Python ecosystem,
  including the [compromised LiteLLM releases in March](https://docs.litellm.ai/blog/security-update-march-2026),
  a small dependency surface and keys that never touch the client are worth more than ever.
- **One observability story.** Instead of per-library logging integrations, the gateway emits
  OpenTelemetry traces and metrics for every call.

## Your first call

```shell
pip install theagentrouter
export ANTHROPIC_API_KEY=...
```

```python
import theagentrouter

resp = theagentrouter.completion(
    model="anthropic/claude-sonnet-5-5",
    messages=[{"role": "user", "content": "Summarise yesterday's refunds"}],
)
print(resp.choices[0].message.content)
```

There's no server to start. On the first call, the SDK starts a local Agent Router gateway, writes
its config from the provider keys in your environment, and routes the call through it. The gateway
keeps running in the background and is shared by every program on the machine that uses the SDK.

The SDK covers the APIs agents actually use:

- `completion` / `acompletion`, with streaming, tools, structured output and thinking
- OpenAI's Responses API via `theagentrouter.responses()`
- Anthropic's Messages API via `theagentrouter.anthropic.messages.create()`, in Anthropic's native
  format, so prompt caching and thinking work as you'd expect
- `embedding`, `token_counter`, cost and model-info helpers

OpenAI, Anthropic, AWS Bedrock, Azure OpenAI, Gemini, Groq, Mistral, DeepSeek and Together are
supported today. Claude calls go to the gateway in the Messages format; everything else in OpenAI's
format.

## Coming from LiteLLM: change one import

For your own code, swap the import and leave the rest alone. `completion` arguments,
`ModelResponse` objects, streaming chunks, `RateLimitError` and `ContextWindowExceededError`,
`success_callback`, `mock_response`, `completion_cost`, `token_counter` and even `Router` all
behave the way your code expects.

The SDK is deliberately honest about the edges, too. LiteLLM arguments that would do nothing here,
such as a per-call `fallbacks=` or `caching=`, don't silently disappear; each one warns the first
time it's used, and the docs tell you what to do instead. Functions outside the agent use case
(image generation, speech, `text_completion`) aren't included, so code that relies on them fails
loudly with `AttributeError` rather than misbehaving. The full table lives in
[Coming from LiteLLM](https://github.com/theagentrouter/agent-router-python/blob/main/docs/coming-from-litellm.md).

If you'd rather hand the migration to your coding agent, the repo ships a
[migrate-from-litellm](https://github.com/theagentrouter/agent-router-python/tree/main/skills/migrate-from-litellm)
skill. Drop it into your agent's skills folder (for Claude Code, `.claude/skills/`) and ask it to
migrate the project.

## Fallbacks, retries and limits, defined once

A **route** is a model name you choose, with real models behind it in order:

```python
import theagentrouter as ar

ar.route(
    "chat",
    primary="openai/gpt-5.4",
    fallbacks=["anthropic/claude-sonnet-5-5"],
    retry=ar.Retry(attempts=2),
    limit=ar.Limit(60, per="minute"),
)

ar.completion(model="chat", messages=[{"role": "user", "content": "hi"}])
```

In this example, a call to `chat` goes to `gpt-5.4`. On a connection failure or a 429, 500, 502,
503 or 504, the gateway retries up to twice more, then moves to Claude. A 401 doesn't trigger a
retry. Requests beyond 60 a minute get a `RateLimitError`. Give `primary` a list and the route
spreads traffic across several models.

The gateway also deals with the cross-provider details that usually break hand-rolled fallbacks.
For example, when a request falls back from Claude to a newer OpenAI model, `max_tokens` becomes
`max_completion_tokens`, and parameters a newer Claude model refuses are stripped before the
request reaches it.

Routes are saved to `~/.agentrouter/routes.json`, so every program on the machine shares them. If
your code already uses LiteLLM's `Router` with `fallbacks` and `num_retries`, its model groups are
turned into gateway routes automatically.

## Agent frameworks, no LiteLLM required

Google ADK and the OpenAI Agents SDK both import `litellm` internally. One call makes that import
resolve to Agent Router instead, so neither framework needs LiteLLM installed:

```python
from theagentrouter.compat import install_as_litellm
install_as_litellm()  # before importing the framework

from agents import Agent, Runner
from agents.extensions.models.litellm_model import LitellmModel

agent = Agent(
    name="refunds",
    instructions="Answer questions about refunds.",
    model=LitellmModel(model="anthropic/claude-sonnet-5-5"),
)
print(Runner.run_sync(agent, "How much was refunded yesterday?").final_output)
```

ADK works the same way through its `LiteLlm` model class.

## From laptop to production without changing code

The local gateway is for development. For staging and production, run Agent Router as a shared
gateway and point your code at it:

```shell
export AGENTROUTER_BASE_URL=https://gateway.acme.example/v1
export AGENTROUTER_API_KEY=...
```

With these set, the local gateway never starts and calls go to the shared one. The SDK sends the
same requests to both, so code that works on your laptop works in production. In servers,
containers, serverless functions and CI, you can install just `theagentrouter-core` and skip the
local gateway entirely.

To get to a shared gateway quickly, export the setup you've been using locally:

```python
from pathlib import Path
from theagentrouter import local_gateway

Path("config.yaml").write_text(local_gateway.export_config(port=1975))
```

The exported configuration can be used to run a gateway with the
[Agent Router standalone mode](/docs/cli/aigwrun). It checks one shared client key: pick any
value, set it as `AGENTROUTER_API_KEY` on the server, and give your clients the same value.

## Locked down by default, even on your laptop

A developer gateway holding real API keys should be treated like production, and the local gateway
is configured that way:

- It answers only callers on the same machine (`127.0.0.1` and `::1`), matched on the connection's
  source address, so a forged header can't get around it.
- It checks the `Host` header, so a malicious web page can't reach it through DNS rebinding.
- Its config file contains no key values. Each key is written to its own file with narrow
  permissions, and is removed when the gateway stops.

## What's not there yet

The SDK is in alpha (`0.1.0a12`), and at the time of writing these are the known limitations:

- The bundled local gateway runs on macOS 13+ on Apple Silicon and on Linux with glibc 2.36+
  (x86-64 and Arm). On Windows, Intel Macs and older Linux, the SDK installs without it and needs a
  remote gateway.
- Google Vertex AI isn't wired into the local gateway yet. Use `gemini/...` for the Gemini API.
- Image generation, speech, transcription, files and batches are not yet supported.

## Try it out

```shell
pip install theagentrouter
```

- Check out the code, docs and more than 20 runnable examples:
  [github.com/theagentrouter/agent-router-python](https://github.com/theagentrouter/agent-router-python)
- Learn about the gateway:
  [github.com/theagentrouter/agent-router](https://github.com/theagentrouter/agent-router)
- Questions and feedback: the [Agent Router Discord](https://discord.gg/xuxtPq43gZ)

If you try it on a real LiteLLM codebase, we would love to hear your feedback! Open an issue, or
tell us in Discord.
