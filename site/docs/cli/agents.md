---
id: aigwagents
title: Claude Code and Codex
sidebar_position: 3
---

# Run Claude Code and Codex through Agent Router

`aigw claude` and `aigw codex` run the coding agents through a local Agent
Router with one command. There is no configuration file to write and no
environment variable to export:

```shell
aigw claude
aigw codex
```

Any arguments are passed to the agent, for example `aigw claude --continue` or
`aigw codex exec "fix the tests"`. The agent runs in your terminal as usual.

## First run

The first time, `aigw claude` asks how Claude Code reaches Claude:

| Choice                            | Who pays                 | What it needs                              |
| --------------------------------- | ------------------------ | ------------------------------------------ |
| Claude subscription (default)     | Your Claude Pro/Max plan | Your Claude Code login                     |
| Anthropic API key                 | Your Anthropic Console   | `ANTHROPIC_API_KEY`                        |
| Tetrate Agent Router, passthrough | Your Claude Pro/Max plan | Your Claude Code login, and `TARS_API_KEY` |
| Tetrate Agent Router, managed     | Tetrate Agent Router     | `TARS_API_KEY`                             |

For Tetrate Agent Router (Service or Enterprise), it also asks for its URL,
which defaults to `https://api.router.tetrate.ai`. In passthrough mode, your
Claude login goes through to Anthropic unchanged, and Agent Router adds your
Agent Router API key in the `x-tars-api-key` header for routing and
observability.

`aigw codex` asks for OpenAI (`OPENAI_API_KEY`), Tetrate Agent Router
(`TARS_API_KEY`) or any other API serving the OpenAI Responses API.

The choice is saved to `~/.config/aigw/agents.yaml` (`$AIGW_CONFIG_HOME`), so
the next runs start right away. Run `aigw claude --setup` or
`aigw codex --setup` to choose again. API keys are never saved: if you enter one
during the setup, export it in your shell profile for the next runs.

The agent always uses its provider, whatever model it asks for: there are no
model names to configure. Your Claude Code and Codex configuration files are not
changed.

## The background Agent Router

`aigw claude` and `aigw codex` start Agent Router in the background on
`http://localhost:1975`, and share it: Claude Code and Codex can run at the same
time, in any number of terminals, and each one's requests go to the provider
chosen for it. It only accepts requests from this machine, and the agents never
get the API keys: Agent Router adds them to the requests. Its logs are written
to `~/.local/state/aigw/agents/gateway.log`.

Agent Router keeps running when the agent exits, so the next `aigw claude` or
`aigw codex` starts at once:

```shell
aigw status # Show whether it runs, and where each agent goes
aigw stop   # Stop it
```

It uses the same ports as `aigw run`: stop one before starting the other.
