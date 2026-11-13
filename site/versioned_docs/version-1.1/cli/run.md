---
id: aigwrun
title: aigw run
sidebar_position: 2
---

# `aigw run`

## Overview

This command runs the Agent Router locally as a standalone proxy with a given configuration file without any dependencies such as docker or Kubernetes.
Since the project is primarily focused on the Kubernetes environment, this command is useful for testing the configuration locally before deploying it to a Kubernetes cluster.
Not only does it help in testing the configuration, but it is also useful in a local development environment of the provider-agnostic AI applications.

:::warning
Currently, `aigw run` supports Linux and macOS.
:::

## Quick Start

The simplest way to get started is to have `aigw` generate a configuration for
you using environment variables. `aigw` supports auto-configuration using the same
environment variables as the OpenAI SDK, making it easy to integrate with existing
tooling. Here are some examples:

```bash
# OpenAI
OPENAI_API_KEY=sk-your-key aigw run
# Azure OpenAI
AZURE_OPENAI_ENDPOINT=https://example.openai.azure.com \
  AZURE_OPENAI_API_KEY=your-key \
  OPENAI_API_VERSION=2024-12-01-preview \
  aigw run
# Tetrate Agent Router Service (TARS)
OPENAI_BASE_URL=https://api.router.tetrate.ai/v1 OPENAI_API_KEY=sk-your-key aigw run
# Ollama running locally
OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=unused aigw run
```

Now, the AI Gateway is running locally with the default configuration serving at `localhost:1975`.

Then, open a new terminal and run the following curl commands to test the AI Gateway.
For example, use `qwen2.5:0.5b` model to route to Ollama, assuming Ollama is running locally and `ollama pull qwen2.5:0.5b` is executed to pull the model.

```shell
curl -H "Content-Type: application/json" -XPOST http://localhost:1975/v1/chat/completions \
  -d '{"model": "qwen2.5:0.5b","messages": [{"role": "user", "content": "Say this is a test!"}]}'
```

### Supported Environment Variables

The following environment variables are compatible with the OpenAI SDK:

**OpenAI / OpenAI-compatible backends:**

When `OPENAI_API_KEY` is set, the following environment variables are read:

| Variable          | Required | Example                     | Description                                          |
| ----------------- | -------- | --------------------------- | ---------------------------------------------------- |
| `OPENAI_API_KEY`  | Yes      | `sk-proj-...`               | API key for authentication (use "unused" for Ollama) |
| `OPENAI_BASE_URL` | No       | `https://api.openai.com/v1` | Base URL of your OpenAI-compatible backend           |

**Azure OpenAI:**

When `AZURE_OPENAI_API_KEY` is set, the following environment variables are read:

| Variable                | Required | Example                            | Description                                 |
| ----------------------- | -------- | ---------------------------------- | ------------------------------------------- |
| `AZURE_OPENAI_ENDPOINT` | Yes      | `https://example.openai.azure.com` | Your Azure endpoint, including the resource |
| `AZURE_OPENAI_API_KEY`  | Yes      | `abc123...`                        | API key for authentication                  |
| `OPENAI_API_VERSION`    | Yes      | `2024-12-01-preview`               | Azure OpenAI API version                    |

**Optional headers (both OpenAI and Azure OpenAI):**

| Variable            | Example    | Description                                                                                    |
| ------------------- | ---------- | ---------------------------------------------------------------------------------------------- |
| `OPENAI_ORG_ID`     | `org-...`  | Organization ID - adds `OpenAI-Organization` request header for billing and access control     |
| `OPENAI_PROJECT_ID` | `proj_...` | Project ID - adds `OpenAI-Project` request header for project-level billing and access control |

## Custom Configuration

To run the AI Gateway with a custom configuration, provide the path to the configuration file as an argument to the `aigw run` command.
For example, to run the AI Gateway with a custom configuration file named `config.yaml`, run the following command:

```shell
aigw run config.yaml
```

The configuration uses the same API as the Agent Router custom resources definitions. See [API Reference](../api/) for more information.

The best way to start customizing the configuration is to start with an [example configuration](https://github.com/theagentrouter/agent-router/tree/main/examples) and modify it as needed.

### Modify an Example Configuration

First, download an example configuration to use as a starting point:

```shell
curl -o ollama.yaml https://raw.githubusercontent.com/theagentrouter/agent-router/refs/heads/{vars.aigwGitRef}/examples/aigw/ollama.yaml
```

Next, let's say change the model matcher from `.*` (match all) to specifically match `deepseek-r1:1.5b` and save the configuration to `custom.yaml`:

```diff
--- ollama.yaml
+++ custom.yaml
@@ -88,7 +88,7 @@
         - headers:
             - type: RegularExpression
               name: x-ai-eg-model
-              value: .*
+              value: deepseek-r1:1.5b
```

You can also use environment variable substitution (`envsubst`) to allow small
changes without needing to copy a file. For example, you could use this syntax
instead, to default the model to the `CHAT_MODEL` variable.

```diff
--- ollama.yaml
+++ custom.yaml
@@ -88,7 +88,7 @@
         - headers:
             - type: RegularExpression
               name: x-ai-eg-model
-              value: .*
+              value: ${CHAT_MODEL:=deepseek-r1:1.5b}
```

### Run with the Custom Configuration

Now, run the AI Gateway with the custom configuration by running the following command:

```shell
aigw run custom.yaml
```

Now, the AI Gateway is running locally with the custom configuration serving at `localhost:1975`.

```shell
curl -H "Content-Type: application/json" -XPOST http://localhost:1975/v1/chat/completions \
  -d '{"model": "deepseek-r1:1.5b","messages": [{"role": "user", "content": "Say this is a test!"}]}'
```

## MCP Configuration

`aigw run` supports running as an [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) Gateway, allowing AI agents to connect to multiple MCP servers through a unified endpoint. The gateway aggregates tools from multiple backends, applies security policies, and provides observability for MCP traffic.

### Using MCP Servers Configuration File

Use the `--mcp-config` flag to load MCP server configuration from a JSON file. The format follows the canonical [MCP servers configuration](https://modelcontextprotocol.io/docs/getting-started/intro#configuring-mcp-servers) used by clients like Claude Desktop, Cursor, and VS Code:

```json
{
  "mcpServers": {
    "context7": {
      "type": "http",
      "url": "https://mcp.context7.com/mcp"
    },
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/readonly",
      "headers": {
        "Authorization": "Bearer ${GITHUB_ACCESS_TOKEN}"
      },
      "includeTools": ["issue_read", "list_issues"]
    }
  }
}
```

Start the MCP Gateway:

```shell
aigw run --mcp-config mcp-servers.json
```

The gateway will be available at `http://localhost:1975/mcp`. Configure your MCP client to connect to this URL.

### Using Inline JSON Configuration

For quick testing or when the configuration is provided programmatically, use the `--mcp-json` flag:

```shell
aigw run --mcp-json '{"mcpServers":{"context7":{"type":"http","url":"https://mcp.context7.com/mcp"}}}'
```

### MCP Servers Configuration Format

Each server configuration in the `mcpServers` object supports the following properties:

**`type`** (string, required)

The transport protocol for the MCP server. Supported values: `"http"`, `"streamable-http"`, `"streamable_http"`, or `"streamableHttp"`.

**`url`** (string, required)

The full endpoint URL for the MCP server, including protocol, hostname, and path.

**`headers`** (object, optional)

HTTP headers to send with requests to the MCP server. Authorization headers with Bearer tokens are automatically extracted and injected as API keys.

Example:

```json
"headers": {
  "Authorization": "Bearer ${GITHUB_TOKEN}",
  "X-Custom-Header": "value"
}
```

Header values support environment variable substitution using `${VAR}` syntax. For example, `${GITHUB_TOKEN}` will be replaced at runtime with the value of the `GITHUB_TOKEN` environment variable.

**`includeTools`** (array of strings, optional)

List of specific tool names to expose from this server. If not specified, all tools from the server are exposed.

This follows the same convention as [Gemini CLI's tool filtering](https://google-gemini.github.io/gemini-cli/docs/tools/mcp-server.html#optional).

Example:

```json
"includeTools": ["issue_read", "list_issues", "search_issues"]
```

### Testing the MCP Gateway

Use the [MCP Inspector](https://github.com/modelcontextprotocol/inspector) to test your gateway:

```shell
# List available tools
npx @modelcontextprotocol/inspector@0.16.8 --cli http://localhost:1975/mcp --method tools/list

# Call a tool
npx @modelcontextprotocol/inspector@0.16.8 \
  --cli http://localhost:1975/mcp \
  --method tools/call \
  --tool-name context7__resolve-library-id \
  --tool-arg libraryName="react"
```

:::note
Tool names are automatically prefixed with the server name (e.g., `github__issue_read`, `context7__resolve-library-id`) to route calls to the correct backend.
:::

### Advanced MCP Configuration

For production deployments with advanced features like OAuth authentication, fine-grained access control, and Kubernetes integration, use the [`MCPRoute` API](/docs/capabilities/mcp/). The same configuration can be used in both `aigw run` standalone mode and Kubernetes environments.

### Admin Endpoints

While running, `aigw` serves admin endpoints on port `1064` by default:

- `/metrics`: Prometheus metrics endpoint for [LLM/AI metrics](../capabilities/observability/metrics.md)
- `/health`: Health check endpoint that verifies the external processor is healthy

## OpenTelemetry

Agent Router's router joins and records distributed traces when supplied
with an [OpenTelemetry](https://opentelemetry.io/) collector endpoint.

Requests to the OpenAI Chat Completions and Embeddings endpoints are recorded
as Spans which include typical timing and request details. In addition, there
are GenAI attributes representing the LLM or Embeddings call including full
request and response details, defined by [OpenInference semantic conventions][openinference].

OpenInference attributes default to include full request and response data for
both chat completions and embeddings. This can be toggled with configuration,
but when enabled allows systems like [Arize Phoenix][phoenix] to perform
evaluations of production requests captured in OpenTelemetry spans.

For chat completions, this includes traditional LLM metrics such as correctness
and hallucination detection. For embeddings, it enables agentic RAG evaluations
focused on retrieval and semantic analysis.

### OpenTelemetry configuration

`aigw run` supports OpenTelemetry tracing via environment variables:

- **[OTEL SDK][otel-env]**: OTLP exporter configuration that controls span
  and metrics export such as:
  - `OTEL_EXPORTER_OTLP_ENDPOINT`: Collector endpoint (e.g., `http://phoenix:6006`)
  - `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`: Override traces endpoint separately
  - `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`: Override metrics endpoint separately
  - `OTEL_TRACES_EXPORTER`: Traces exporter type (e.g., `console`, `otlp`, `none`)
  - `OTEL_METRICS_EXPORTER`: Metrics exporter type (e.g., `console`, `otlp`, `none`)
  - `OTEL_BSP_SCHEDULE_DELAY`: Batch span processor delay (default: 5000ms)
  - `OTEL_METRIC_EXPORT_INTERVAL`: Metrics export interval (default: 60000ms)

- **Semantic Convention**: Select which span attributes are recorded. See
  [Tracing][tracing] for the attribute differences.
  - `AI_GATEWAY_TRACING_SEMCONV`: `openinference` (default) or `gen_ai` for the
    OpenTelemetry GenAI conventions. An unrecognized value fails startup.
  - `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`: Capture message content
    when `gen_ai` is selected (default: `false`). The GenAI conventions treat
    content as opt-in, unlike OpenInference.
  - This also selects the vocabulary for MCP spans: the default keeps the
    gateway-specific `mcp.*` attributes, `gen_ai` opts into the OpenTelemetry MCP
    conventions.

- **[OpenInference][openinference-config]**: Control sensitive data redaction,
  such as below. These apply only when `AI_GATEWAY_TRACING_SEMCONV` is
  `openinference`. There is [similar config][openinference-embeddings] for embeddings:
  - `OPENINFERENCE_HIDE_INPUTS`: Hide input messages/prompts (default: `false`)
  - `OPENINFERENCE_HIDE_OUTPUTS`: Hide output messages/completions (default: `false`)
  - `OPENINFERENCE_HIDE_EMBEDDINGS_TEXT`: Hide embeddings input (default: `false`)
  - `OPENINFERENCE_HIDE_EMBEDDINGS_VECTORS`: Hide embeddings output (default: `false`)

- **Header Mapping**: Map HTTP request headers to span attributes, metric labels, and access log attributes. See [Session Tracking][session-tracking] for more details.
  - `OTEL_AIGW_REQUEST_HEADER_ATTRIBUTES`: Base mapping applied to metrics, spans, and access logs. Example: `x-tenant-id:tenant.id`
  - `OTEL_AIGW_SPAN_REQUEST_HEADER_ATTRIBUTES`: Span-only mapping (merged with base). Default: `agent-session-id:session.id` when unset (set to empty to disable). Example: `x-forwarded-proto:url.scheme`
  - `OTEL_AIGW_METRICS_REQUEST_HEADER_ATTRIBUTES`: Metrics-only mapping (merged with base). Example: `x-tenant-id:tenant.id` (metrics never default to `session.id`)
  - `OTEL_AIGW_LOG_REQUEST_HEADER_ATTRIBUTES`: Access-log-only mapping (merged with base). Default: `agent-session-id:session.id` when unset (set to empty to disable). Example: `x-forwarded-proto:url.scheme`

See [docker-compose-otel.yaml][docker-compose-otel.yaml] for a complete example configuration.

## Configuration

### File Locations

`aigw run` uses the [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html) to organize files:

| Environment Variable | Default Path          | Purpose                     |
| -------------------- | --------------------- | --------------------------- |
| `AIGW_CONFIG_HOME`   | `~/.config/aigw`      | User configuration files    |
| `AIGW_DATA_HOME`     | `~/.local/share/aigw` | Downloaded Envoy binaries   |
| `AIGW_STATE_HOME`    | `~/.local/state/aigw` | Persistent logs and configs |
| `AIGW_RUNTIME_DIR`   | `/tmp/aigw-${UID}`    | Ephemeral runtime files     |

See [Installation - Configuration](./installation.md#configuration) for more details about XDG directories.

### File Mappings

Each invocation creates a unique run identifier (`runID`) in format `YYYYMMDD_HHMMSS_UUU` to isolate concurrent runs:

| File Type                 | Purpose                                  | Path                                                             | Type    |
| ------------------------- | ---------------------------------------- | ---------------------------------------------------------------- | ------- |
| Default Config            | Configuration file location              | `${AIGW_CONFIG_HOME}/config.yaml`                                | CONFIG  |
| Envoy Version Preference  | Selected Envoy version (via func-e)      | `${AIGW_CONFIG_HOME}/envoy-version`                              | CONFIG  |
| Envoy Binaries            | Downloaded executables (via func-e)      | `${AIGW_DATA_HOME}/envoy-versions/{version}/bin/envoy`           | DATA    |
| AIGW Logs                 | Gateway logs and stderr output           | `${AIGW_STATE_HOME}/runs/{runID}/aigw.log`                       | STATE   |
| Envoy Gateway Config      | Generated EG configuration               | `${AIGW_STATE_HOME}/runs/{runID}/envoy-gateway-config.yaml`      | STATE   |
| Envoy Gateway Resources   | Generated EG resources (Gateway, Routes) | `${AIGW_STATE_HOME}/runs/{runID}/envoy-ai-gateway-resources/...` | STATE   |
| External Processor Config | Generated extproc configuration          | `${AIGW_STATE_HOME}/runs/{runID}/extproc-config.yaml`            | STATE   |
| Envoy Run Logs (func-e)   | Envoy stdout/stderr (via func-e)         | `${AIGW_STATE_HOME}/envoy-runs/{runID}/stdout.log,stderr.log`    | STATE   |
| UDS Socket                | Unix domain socket for extproc           | `${AIGW_RUNTIME_DIR}/{runID}/uds.sock`                           | RUNTIME |
| Admin Address (func-e)    | Envoy admin endpoint (via func-e)        | `${AIGW_RUNTIME_DIR}/{runID}/admin-address.txt`                  | RUNTIME |

**File Categories:**

- **CONFIG**: User-specific configuration (persistent, shared across runs)
- **DATA**: Downloaded binaries (persistent, shared across runs)
- **STATE**: Per-run logs and configs (persistent for debugging)
- **RUNTIME**: Ephemeral files like sockets (cleaned on reboot)

### `runID`

By default, `aigw run` generates a timestamp-based `runID` for each invocation. You can customize this for predictable paths:

```shell
# Use run ID "0" for Docker/Kubernetes deployments
aigw run --run-id=0

# Or via environment variable
AIGW_RUN_ID=production aigw run
```

Custom run IDs:

- Enable predictable file paths in containers
- Allow correlation across multiple runs with the same ID
- Must not contain path separators (`/` or `\`)

---

[openinference]: https://github.com/Arize-ai/openinference/tree/main/spec
[phoenix]: https://docs.arize.com/phoenix
[otel-env]: https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/
[openinference-config]: https://github.com/Arize-ai/openinference/blob/main/spec/configuration.md
[openinference-embeddings]: https://github.com/Arize-ai/openinference/blob/main/spec/embedding_spans.md
[docker-compose-otel.yaml]: https://github.com/theagentrouter/agent-router/blob/main/cmd/aigw/docker-compose-otel.yaml
[session-tracking]: ../capabilities/observability/tracing.md#session-tracking
[tracing]: ../capabilities/observability/tracing.md#semantic-conventions
