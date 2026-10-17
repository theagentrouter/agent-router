---
id: aigwinstall
title: Installation
sidebar_position: 1
---

## Install with one command

The quickest way to get `aigw` is the install script. It downloads the latest release binary for your platform,
verifies its checksum, and installs it to `~/.local/bin`:

```shell
curl -fsSL https://raw.githubusercontent.com/theagentrouter/agent-router/main/install.sh | sh
```

The same script is also served from `https://theagentrouter.ai/install.sh`.

Then run the gateway locally with your provider credentials, for example for the [OpenAI provider](../getting-started/connect-providers/openai.md):

```shell
OPENAI_API_KEY=sk-... aigw run
```

The script honors the following environment variables:

| Environment Variable | Default            | Description                                                                |
| -------------------- | ------------------ | -------------------------------------------------------------------------- |
| `AIGW_VERSION`       | latest release     | Release to install, with or without the leading `v` (e.g. `v1.1.0`).       |
| `AIGW_INSTALL_DIR`   | `$HOME/.local/bin` | Directory to install the binary into. Created if missing, never uses sudo. |
| `GITHUB_TOKEN`       | unset              | Optional token for GitHub API calls, useful in CI to avoid rate limits.    |

For example, to pin a version and install it somewhere else:

```shell
curl -fsSL https://raw.githubusercontent.com/theagentrouter/agent-router/main/install.sh | AIGW_VERSION=v1.1.0 AIGW_INSTALL_DIR=/usr/local/bin sh
```

Re-running the script replaces the installed binary, which is how you upgrade.
The script only needs `curl` or `wget`, and uses `sha256sum` or `shasum` to verify the download when a checksum is
published for the release. If `~/.local/bin` is not on your `PATH`, the script prints the line to add to your shell profile.

### Supported platforms

Release binaries are published for the following platforms:

| Platform                    | Supported                                           |
| --------------------------- | --------------------------------------------------- |
| Linux amd64 (x86_64)        | Yes                                                 |
| Linux arm64                 | Yes                                                 |
| macOS Apple Silicon (arm64) | Yes                                                 |
| macOS Intel (amd64)         | No, use the [Docker image](#using-the-docker-image) |
| Windows                     | No, use the [Docker image](#using-the-docker-image) |

There is no macOS amd64 binary because Envoy, which `aigw` runs under the hood, is not published for that platform.
The install script fails with a clear message on Intel Macs and points to the Docker image instead.

## Official CLI binaries

Each release includes the binaries for the `aigw` CLI build for different platforms.<br/>
They can be downloaded directly from the corresponding release in the
[GitHub releases page](https://github.com/theagentrouter/agent-router/releases).
Newer releases also include a `checksums.txt` file with the SHA-256 checksum of every binary, which the install script verifies against.

## Using the Docker image

You can also use the official Docker images to run the CLI without installing it locally.
The CLI images are available at: https://hub.docker.com/r/envoyproxy/ai-gateway-cli/tags

To run the CLI using Docker, you only need to expose the port where the standalone `aigw` listens to
and configure the environment variables for the credentials. If you want to use a custom configuration file,
you can mount it as a volume.

The following example runs the AI Gateway with the default configuration for the [OpenAI provider](../getting-started/connect-providers/openai.md):

```shell
docker run --rm -p 1975:1975 -e OPENAI_API_KEY=OPENAI_API_KEY envoyproxy/ai-gateway-cli run
```

## Building the latest version

To use the latest version, you can use the following commands to clone the repo and build the CLI:

```shell
git clone https://github.com/theagentrouter/agent-router.git
cd agent-router
go install ./cmd/aigw
```

:::tip
`go install` command installs a binary in the `$(go env GOPATH)/bin` directory.
Make sure that the `$(go env GOPATH)/bin` directory is in your `PATH` environment variable.

For example, you can add the following line to your shell profile (e.g., `~/.bashrc`, `~/.zshrc`, etc.):

```sh
export PATH=$PATH:$(go env GOPATH)/bin
```

:::

Now, you can check if the installation was successful by running the following command:

```sh
aigw --help
```

This will display the help message for the `aigw` CLI like this:

```
Usage: aigw <command> [flags]

Envoy AI Gateway CLI

Flags:
  -h, --help    Show context-sensitive help.

Commands:
  version [flags]
    Show version.

  run [<path>] [flags]
    Run the AI Gateway locally for given configuration.

Run "aigw <command> --help" for more information on a command.
```

## Configuration

The [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html) defines standard locations for user-specific files:

- **Config files**: User-specific configuration (persistent, shared)
- **Data files**: Downloaded binaries (persistent, shared)
- **State files**: Logs and configs per run (persistent, debugging)
- **Runtime files**: Ephemeral files like sockets (deleted on reboot)

`aigw` adopts these conventions to separate configuration, downloaded Envoy binaries, logs, and ephemeral runtime files.

| Environment Variable | Default Path          | CLI Flag        |
| -------------------- | --------------------- | --------------- |
| `AIGW_CONFIG_HOME`   | `~/.config/aigw`      | `--config-home` |
| `AIGW_DATA_HOME`     | `~/.local/share/aigw` | `--data-home`   |
| `AIGW_STATE_HOME`    | `~/.local/state/aigw` | `--state-home`  |
| `AIGW_RUNTIME_DIR`   | `/tmp/aigw-${UID}`    | `--runtime-dir` |

**Priority**: CLI flags > Environment variables > Defaults

## What's next?

The following sections provide more information about each of the CLI commands:

- [aigw run](./run.md): Run the AI Gateway locally for a given configuration.
