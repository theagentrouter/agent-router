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
#                     Use "latest" for the latest stable release, or a release tag such as v1.3.0.
#   AIGW_INSTALL_DIR  Directory to install aigw into. Defaults to ~/.local/bin.

set -eu

REPO="theagentrouter/agent-router"
INSTALL_DIR="${AIGW_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${AIGW_VERSION:-main}"

fail() {
  echo "error: $*" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS: $(uname -s). aigw runs on Linux and macOS." ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ "$os" = "darwin" ] && [ "$arch" = "amd64" ]; then
  fail "aigw binaries are not published for Intel Macs."
fi

asset="aigw-${os}-${arch}"
case "$VERSION" in
  main) url="https://github.com/${REPO}/releases/download/latest-main/${asset}" ;;
  latest) url="https://github.com/${REPO}/releases/latest/download/${asset}" ;;
  *) url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

echo "Downloading ${asset} (${VERSION})..."
curl -fsSL --retry 3 --retry-delay 2 -o "$tmp" "$url" || fail "failed to download ${url}"
chmod +x "$tmp"

mkdir -p "$INSTALL_DIR"
mv "$tmp" "$INSTALL_DIR/aigw"
trap - EXIT
echo "Installed aigw to ${INSTALL_DIR}/aigw"

# Pre-download Envoy so the first "aigw run" starts quickly.
echo "Downloading Envoy..."
"$INSTALL_DIR/aigw" download-envoy >/dev/null || echo "warning: failed to pre-download Envoy; aigw run will retry" >&2

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo
    echo "${INSTALL_DIR} is not in your PATH. Add it with:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac

echo
echo "Run 'aigw run' to start Agent Router on http://localhost:1975"
