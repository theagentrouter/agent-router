#!/bin/sh
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Installs the Agent Router CLI (aigw).
#
#   curl -sSL https://theagentrouter.ai/install.sh | sh
#
# Environment variables:
#   AIGW_VERSION      Version to install. Defaults to "main", the latest build of the main branch.
#                     Use "latest" for the latest stable release, or a release such as v1.3.0 (or 1.3.0).
#   AIGW_INSTALL_DIR  Directory to install aigw into. Defaults to ~/.local/bin.
#   GITHUB_TOKEN      Optional. Authenticates the GitHub API call used to verify releases that
#                     predate checksums.txt, to avoid the low unauthenticated rate limit.
#
# The script needs curl or wget. The binary is verified against the checksums.txt published
# with it (or, for older releases, the digest GitHub reports for the asset) using sha256sum or
# shasum. It never uses sudo: pick a writable AIGW_INSTALL_DIR if the default does not suit you.

set -eu

REPO="theagentrouter/agent-router"
INSTALL_DIR="${AIGW_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${AIGW_VERSION:-main}"
DOCKER_CMD="docker run --rm -p 1975:1975 -e OPENAI_API_KEY=... envoyproxy/ai-gateway-cli run"

fail() {
  echo "error: $*" >&2
  exit 1
}

warn() {
  echo "warning: $*" >&2
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS: $(uname -s). aigw runs on Linux and macOS. Elsewhere, use the Docker image:
  ${DOCKER_CMD}" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m). Use the Docker image instead:
  ${DOCKER_CMD}" ;;
esac

if [ "$os" = "darwin" ] && [ "$arch" = "amd64" ]; then
  fail "aigw binaries are not published for Intel Macs: Envoy, which aigw runs, is not available for them.
Use the Docker image instead:
  ${DOCKER_CMD}"
fi

asset="aigw-${os}-${arch}"
case "$VERSION" in
  main) base="https://github.com/${REPO}/releases/download/latest-main" ;;
  latest) base="https://github.com/${REPO}/releases/latest/download" ;;
  v*) base="https://github.com/${REPO}/releases/download/${VERSION}" ;;
  *)
    VERSION="v${VERSION}"
    base="https://github.com/${REPO}/releases/download/${VERSION}"
    ;;
esac
url="${base}/${asset}"

# Everything below downloads through http_get and http_download, so curl and wget behave the same.
if command -v curl >/dev/null 2>&1; then
  http_client=curl
elif command -v wget >/dev/null 2>&1; then
  http_client=wget
else
  fail "curl or wget is required"
fi

# http_get URL [HEADER]: print the response body to stdout. Fails on HTTP errors.
http_get() {
  if [ -n "${2:-}" ]; then
    case "$http_client" in
      curl) curl -fsSL --retry 3 --retry-delay 2 -H "$2" "$1" ;;
      wget) wget -qO- --header="$2" "$1" ;;
    esac
    return
  fi
  case "$http_client" in
    curl) curl -fsSL --retry 3 --retry-delay 2 "$1" ;;
    wget) wget -qO- "$1" ;;
  esac
}

# http_download URL DEST: download URL to DEST. Fails on HTTP errors.
http_download() {
  case "$http_client" in
    curl) curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1" ;;
    wget) wget -qO "$2" "$1" ;;
  esac
}

# Check the install directory before downloading, so a bad AIGW_INSTALL_DIR fails fast
# instead of after a ~300 MB download.
mkdir -p "$INSTALL_DIR" 2>/dev/null || fail "cannot create ${INSTALL_DIR}. Set AIGW_INSTALL_DIR to a writable directory."
[ -w "$INSTALL_DIR" ] || fail "${INSTALL_DIR} is not writable. Set AIGW_INSTALL_DIR to a writable directory."

# Download next to the destination: the final rename is then atomic, and a small tmpfs /tmp
# cannot run out of space. The EXIT trap removes the partial file on any failure, and the
# signal traps route Ctrl-C and termination through it.
tmp="$(mktemp "${INSTALL_DIR}/.aigw.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Downloading ${asset} (${VERSION})..."
http_download "$url" "$tmp" || fail "failed to download ${url}"

# Verify the download. Prefer the checksums.txt published with the binaries. Releases cut before
# checksums.txt existed fall back to the digest the GitHub API reports for every release asset.
expected="$(http_get "${base}/checksums.txt" 2>/dev/null |
  awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }')" || expected=""
if [ -z "$expected" ] && [ "$VERSION" != "main" ]; then
  if [ "$VERSION" = "latest" ]; then
    release_api="https://api.github.com/repos/${REPO}/releases/latest"
  else
    release_api="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
  fi
  # Split the JSON on commas so each field is on its own line whether or not it is pretty-printed,
  # then take the digest that follows the asset's name.
  expected="$(http_get "$release_api" "${GITHUB_TOKEN:+Authorization: Bearer ${GITHUB_TOKEN}}" 2>/dev/null |
    tr ',' '\n' |
    awk -v asset="$asset" '
      /"name": *"/ { found = ($0 ~ "\"name\": *\"" asset "\"") }
      found && /"digest": *"sha256:/ { sub(/.*"sha256:/, ""); sub(/".*/, ""); print; exit }
    ')" || expected=""
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  sha256="shasum -a 256"
else
  sha256=""
fi
if [ -z "$expected" ]; then
  warn "no checksum is published for ${asset} (${VERSION}); skipping verification"
elif [ -z "$sha256" ]; then
  warn "neither sha256sum nor shasum is installed; skipping checksum verification"
else
  actual="$($sha256 "$tmp" | awk '{ print $1 }')"
  [ "$actual" = "$expected" ] || fail "checksum mismatch for ${asset} (${VERSION}): expected ${expected}, got ${actual}.
The download may be corrupted or tampered with, or the release was being updated: try again in a minute.
Nothing was installed."
  echo "Checksum verified."
fi

# mktemp creates the file as 0600, so set the mode explicitly rather than just adding +x.
chmod 755 "$tmp"
# Rename rather than copy: it is atomic on the same filesystem and works while the old binary is running.
mv -f "$tmp" "$INSTALL_DIR/aigw"
trap - EXIT INT TERM
echo "Installed aigw to ${INSTALL_DIR}/aigw"

# Pre-download Envoy so the first "aigw run" starts quickly.
echo "Downloading Envoy..."
"$INSTALL_DIR/aigw" download-envoy >/dev/null || warn "failed to pre-download Envoy; aigw run will retry"

case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    # The install directory is on PATH, but another aigw earlier on PATH (a Go build in ~/go/bin, an
    # old copy in /usr/local/bin, ...) would still win. Say so, or "aigw" silently runs the wrong one.
    resolved="$(command -v aigw 2>/dev/null || true)"
    if [ -n "$resolved" ] && [ "$resolved" != "${INSTALL_DIR}/aigw" ]; then
      warn "${resolved} comes before ${INSTALL_DIR} on your PATH and will shadow the new aigw.
Remove it, or run the new one by its full path: ${INSTALL_DIR}/aigw"
    fi
    ;;
  *)
    path_line="export PATH=\"${INSTALL_DIR}:\$PATH\""
    shell_name="${SHELL:-sh}"
    # shellcheck disable=SC2088 # The profile is shown to the user, not expanded.
    case "${shell_name##*/}" in
      fish)
        profile="~/.config/fish/config.fish"
        path_line="fish_add_path ${INSTALL_DIR}"
        ;;
      zsh) profile="~/.zshrc" ;;
      bash) if [ "$os" = darwin ]; then profile="~/.bash_profile"; else profile="~/.bashrc"; fi ;;
      *) profile="your shell profile" ;;
    esac
    echo
    echo "${INSTALL_DIR} is not in your PATH. Add it with the line below, and to ${profile} to keep it:"
    echo "  ${path_line}"
    ;;
esac

echo
echo "Run 'aigw run' to start Agent Router on http://localhost:1975"
