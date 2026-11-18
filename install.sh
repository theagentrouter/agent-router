#!/bin/sh
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Installs the Agent Router CLI (aigw, formerly Envoy AI Gateway) from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/theagentrouter/agent-router/main/install.sh | sh
#
# Environment variables:
#   AIGW_VERSION      Release to install, e.g. "v1.1.0" or "1.1.0". Defaults to the latest release.
#   AIGW_INSTALL_DIR  Directory to install the binary into. Defaults to "$HOME/.local/bin".
#   GITHUB_TOKEN      Optional. Used to authenticate GitHub API calls (avoids rate limits in CI).
#   AIGW_OS, AIGW_ARCH
#                     Testing only. Override the detected platform, e.g. AIGW_OS=darwin AIGW_ARCH=amd64.
#
# Supported platforms: linux-amd64, linux-arm64, darwin-arm64.
# There is no darwin-amd64 (Intel Mac) binary: Envoy is not published for macOS amd64 and
# aigw depends on it. Use the Docker image on Intel Macs instead:
#   docker run --rm -p 1975:1975 -e OPENAI_API_KEY=... envoyproxy/ai-gateway-cli run
#
# The script only needs curl or wget, plus sha256sum or shasum for checksum verification.
# It never uses sudo: pick a writable AIGW_INSTALL_DIR if the default does not suit you.

set -eu

REPO="theagentrouter/agent-router"
BINARY="aigw"
DOCKER_CMD="docker run --rm -p 1975:1975 -e OPENAI_API_KEY=... envoyproxy/ai-gateway-cli run"

info() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

# Pick an HTTP client. Everything else in the script goes through http_get/http_download so
# curl and wget behave the same.
if command -v curl >/dev/null 2>&1; then
  HTTP_CLIENT=curl
elif command -v wget >/dev/null 2>&1; then
  HTTP_CLIENT=wget
else
  fail "curl or wget is required to download ${BINARY}."
fi

# http_get URL: print the response body to stdout. Fails on HTTP errors.
http_get() {
  case "$HTTP_CLIENT" in
    curl) curl -fsSL "$1" ;;
    wget) wget -qO- "$1" ;;
  esac
}

# api_get URL: like http_get, but for the GitHub API. Sends GITHUB_TOKEN when set so CI and other
# shared egress IPs are not tripped up by the low unauthenticated rate limit.
api_get() {
  if [ -z "${GITHUB_TOKEN:-}" ]; then
    http_get "$1"
    return
  fi
  case "$HTTP_CLIENT" in
    curl) curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" "$1" ;;
    wget) wget -qO- --header="Authorization: Bearer ${GITHUB_TOKEN}" "$1" ;;
  esac
}

# http_download URL DEST: download with a progress indicator when attached to a terminal.
http_download() {
  case "$HTTP_CLIENT" in
    curl)
      if [ -t 2 ]; then
        curl -fL --progress-bar -o "$2" "$1"
      else
        curl -fsSL -o "$2" "$1"
      fi
      ;;
    wget)
      if [ -t 2 ]; then
        wget -O "$2" "$1"
      else
        wget -qO "$2" "$1"
      fi
      ;;
  esac
}

# http_redirect_target URL: print the final URL after following redirects, without downloading
# the body. Used as a fallback to resolve the latest release tag without the GitHub API.
http_redirect_target() {
  case "$HTTP_CLIENT" in
    curl) curl -fsSLI -o /dev/null -w '%{url_effective}' "$1" ;;
    wget)
      # wget prints the redirect chain to stderr, with "Location: <url>" for each hop.
      wget --spider -S "$1" 2>&1 | sed -n 's/^ *Location: *\([^ ]*\).*/\1/p' | tail -n 1
      ;;
  esac
}

# ---- Platform detection -------------------------------------------------------------------------

OS="${AIGW_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
ARCH="${AIGW_ARCH:-$(uname -m)}"

case "$ARCH" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
esac

case "${OS}-${ARCH}" in
  linux-amd64 | linux-arm64 | darwin-arm64) ;;
  darwin-amd64)
    fail "no ${BINARY} binary is published for macOS on Intel (darwin-amd64).
Envoy, which ${BINARY} depends on, is not available for macOS amd64, so the release skips it on purpose.
Use the Docker image instead:

  ${DOCKER_CMD}"
    ;;
  *)
    fail "unsupported platform ${OS}-${ARCH}.
Supported platforms: linux-amd64, linux-arm64, darwin-arm64.
On other platforms, use the Docker image:

  ${DOCKER_CMD}"
    ;;
esac

ASSET="${BINARY}-${OS}-${ARCH}"

# ---- Version resolution -------------------------------------------------------------------------

if [ -n "${AIGW_VERSION:-}" ]; then
  case "$AIGW_VERSION" in
    v*) TAG="$AIGW_VERSION" ;;
    *) TAG="v${AIGW_VERSION}" ;;
  esac
else
  info "Resolving the latest ${BINARY} release..."
  # The GitHub API is the source of truth. Fall back to the redirect on the human-facing
  # releases/latest page, which does not count against the (low) unauthenticated API rate limit.
  TAG=$(api_get "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1) || true
  if [ -z "$TAG" ]; then
    TAG=$(http_redirect_target "https://github.com/${REPO}/releases/latest" 2>/dev/null |
      sed -n 's#.*/releases/tag/\([^/?[:space:]]*\).*#\1#p') || true
  fi
  [ -n "$TAG" ] || fail "could not determine the latest release. Set AIGW_VERSION to install a specific version."
fi

DOWNLOAD_BASE="https://github.com/${REPO}/releases/download/${TAG}"

# ---- Install directory --------------------------------------------------------------------------

# Validate the destination before downloading so a bad AIGW_INSTALL_DIR fails fast instead of after
# pulling a ~300 MB binary.
INSTALL_DIR="${AIGW_INSTALL_DIR:-${HOME}/.local/bin}"
mkdir -p "$INSTALL_DIR" || fail "cannot create ${INSTALL_DIR}. Set AIGW_INSTALL_DIR to a writable directory."
[ -w "$INSTALL_DIR" ] || fail "${INSTALL_DIR} is not writable. Set AIGW_INSTALL_DIR to a writable directory."

# ---- Download -----------------------------------------------------------------------------------

# Download next to the destination: the final rename is then atomic, and a small tmpfs /tmp cannot
# run out of space on a ~300 MB file. The EXIT trap removes the partial file on any failure, and the
# signal traps route Ctrl-C and termination through it.
TMP_FILE=$(mktemp "${INSTALL_DIR}/.${BINARY}.XXXXXX")
trap 'rm -f "$TMP_FILE"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

info "Downloading ${ASSET} ${TAG} (this is a ~300 MB binary, it can take a minute)..."
http_download "${DOWNLOAD_BASE}/${ASSET}" "$TMP_FILE" ||
  fail "failed to download ${DOWNLOAD_BASE}/${ASSET}
Check that release ${TAG} exists and publishes a ${ASSET} asset: https://github.com/${REPO}/releases"

# ---- Checksum verification ----------------------------------------------------------------------

if command -v sha256sum >/dev/null 2>&1; then
  SHA256_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  SHA256_CMD="shasum -a 256"
else
  SHA256_CMD=""
fi

EXPECTED_SHA256=""
# Preferred: a checksums.txt asset published alongside the binaries.
EXPECTED_SHA256=$(http_get "${DOWNLOAD_BASE}/checksums.txt" 2>/dev/null |
  awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { print $1; exit }') || true
if [ -z "$EXPECTED_SHA256" ]; then
  # Fallback: the per-asset digest the GitHub API reports for every release asset.
  # Split the JSON on commas so each field is on its own line whether or not it is pretty-printed,
  # then take the digest that follows our asset's name.
  EXPECTED_SHA256=$(api_get "https://api.github.com/repos/${REPO}/releases/tags/${TAG}" 2>/dev/null |
    tr ',' '\n' |
    awk -v asset="$ASSET" '
      /"name": *"/ { found = ($0 ~ "\"name\": *\"" asset "\"") }
      found && /"digest": *"sha256:/ { sub(/.*"sha256:/, ""); sub(/".*/, ""); print; exit }
    ') || true
fi

if [ -z "$EXPECTED_SHA256" ]; then
  warn "no checksum is available for ${ASSET} ${TAG}; skipping verification."
elif [ -z "$SHA256_CMD" ]; then
  warn "neither sha256sum nor shasum is installed; skipping checksum verification."
else
  ACTUAL_SHA256=$($SHA256_CMD "$TMP_FILE" | awk '{ print $1 }')
  if [ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]; then
    fail "checksum mismatch for ${ASSET} ${TAG}
  expected: ${EXPECTED_SHA256}
  actual:   ${ACTUAL_SHA256}
The download may be corrupted or tampered with. Nothing was installed."
  fi
  info "Checksum verified."
fi

# ---- Install ------------------------------------------------------------------------------------

if [ -e "${INSTALL_DIR}/${BINARY}" ]; then
  PREVIOUS=$("${INSTALL_DIR}/${BINARY}" version 2>/dev/null || true)
  info "Replacing the existing ${INSTALL_DIR}/${BINARY}${PREVIOUS:+ (${PREVIOUS})}."
fi

# mktemp creates the file as 0600, so set the mode explicitly rather than just adding +x.
chmod 755 "$TMP_FILE"
# Rename rather than copy: it is atomic on the same filesystem and works while the old binary is running.
mv -f "$TMP_FILE" "${INSTALL_DIR}/${BINARY}"

info "Installed ${INSTALL_DIR}/${BINARY}: $("${INSTALL_DIR}/${BINARY}" version)"

# ---- PATH hint ----------------------------------------------------------------------------------

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*)
    # The install directory is on PATH, but another aigw earlier on PATH (a Go build in ~/go/bin, an
    # old copy in /usr/local/bin, ...) would still win. Say so, or "aigw" silently runs the wrong thing.
    RESOLVED=$(command -v "$BINARY" 2>/dev/null || true)
    if [ -n "$RESOLVED" ] && [ "$RESOLVED" != "${INSTALL_DIR}/${BINARY}" ]; then
      RESOLVED_VERSION=$("$RESOLVED" version 2>/dev/null || true)
      warn "${RESOLVED}${RESOLVED_VERSION:+ (${RESOLVED_VERSION})} comes before ${INSTALL_DIR} on your PATH and will shadow the new ${BINARY}.
Remove it, or run the new binary by its full path: ${INSTALL_DIR}/${BINARY}"
    fi
    ;;
  *)
    SHELL_NAME=$(basename "${SHELL:-sh}")
    PATH_LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""
    case "$SHELL_NAME" in
      fish)
        PROFILE=".config/fish/config.fish"
        PATH_LINE="fish_add_path ${INSTALL_DIR}"
        ;;
      zsh) PROFILE=".zshrc" ;;
      bash) if [ "$OS" = darwin ]; then PROFILE=".bash_profile"; else PROFILE=".bashrc"; fi ;;
      *) PROFILE="" ;;
    esac
    info ""
    info "${INSTALL_DIR} is not on your PATH. Run the line below, and add it to ${PROFILE:+~/}${PROFILE:-your shell profile}:"
    info ""
    info "  ${PATH_LINE}"
    ;;
esac

info ""
info "Get started: OPENAI_API_KEY=sk-... ${BINARY} run"
