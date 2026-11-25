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
#   AIGW_SKIP_CHECKSUM  Set to 1 to install without verifying the checksum.
#
# The script needs curl or wget. The binary is verified against the checksums.txt published
# with it (or, for older releases, the digest GitHub reports for the asset) using sha256sum or
# shasum. If the checksum cannot be fetched, nothing is installed: only a release that has no
# checksum at all is installed without verification, with a warning.
# It never uses sudo: pick a writable AIGW_INSTALL_DIR if the default does not suit you.

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

# Everything below downloads through http_download and http_fetch, so curl and wget behave the same.
if command -v curl >/dev/null 2>&1; then
  http_client=curl
elif command -v wget >/dev/null 2>&1; then
  http_client=wget
else
  fail "curl or wget is required"
fi

# http_fetch URL DEST [HEADER]: download URL to DEST, telling a page that does not exist from a
# request that failed. Returns 0 on success, 1 if the server answers 404, and 2 for anything else
# (network errors, rate limits, server errors, responses cut short). What happened is left in
# http_detail, for error messages.
http_fetch() {
  http_status=""
  http_detail="no response"
  case "$http_client" in
    curl)
      # No -f: the status is needed, and curl must not turn a 404 into a generic failure.
      curl_status=0
      if [ -n "${3:-}" ]; then
        http_status="$(curl -sSL --retry 3 --retry-delay 2 -o "$2" -w '%{http_code}' -H "$3" "$1" 2>/dev/null)" || curl_status=$?
      else
        http_status="$(curl -sSL --retry 3 --retry-delay 2 -o "$2" -w '%{http_code}' "$1" 2>/dev/null)" || curl_status=$?
      fi
      if [ "$curl_status" -ne 0 ]; then
        # A 200 from a curl that failed is a response cut short: the body is incomplete.
        if [ "$http_status" = "200" ]; then http_detail="incomplete response"; else http_detail="no response"; fi
        return 2
      fi
      ;;
    wget)
      # -S prints the response headers to stderr. The last status line is the one of the final
      # response, after redirects. Without any status line, the request itself failed.
      wget_status=0
      if [ -n "${3:-}" ]; then
        wget_output="$(wget -qS -O "$2" --header="$3" "$1" 2>&1)" || wget_status=$?
      else
        wget_output="$(wget -qS -O "$2" "$1" 2>&1)" || wget_status=$?
      fi
      http_status="$(printf '%s\n' "$wget_output" | awk '$1 ~ /^HTTP\// { code = $2 } END { print code }')"
      # A 200 from a wget that failed is a response cut short: the body is incomplete.
      if [ "$wget_status" -ne 0 ] && [ "$http_status" = "200" ]; then
        http_detail="incomplete response"
        return 2
      fi
      ;;
  esac
  http_detail="${http_status:+HTTP }${http_status:-no response}"
  case "$http_status" in
    200) return 0 ;;
    404) return 1 ;;
    *) return 2 ;;
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
meta="${tmp}.meta" # Holds checksums.txt or the GitHub API response while verifying.
trap 'rm -f "$tmp" "$meta"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Downloading ${asset} (${VERSION})..."
http_download "$url" "$tmp" || fail "failed to download ${url}"

# Verify the download. Prefer the checksums.txt published with the binaries. Releases cut before
# checksums.txt existed fall back to the digest the GitHub API reports for every release asset.
#
# A checksum that does not exist and a checksum that could not be fetched are different things:
# only the first one, which can only happen for old releases, is installed without verification.
# Anything else stops the install, so a network error or a rate limit never skips the check.
retry_hint="Try again in a minute, or set AIGW_SKIP_CHECKSUM=1 to install without verifying it.
Nothing was installed."
expected=""
verify=1
if [ "${AIGW_SKIP_CHECKSUM:-}" = "1" ]; then
  verify=0
  warn "AIGW_SKIP_CHECKSUM=1: installing ${asset} (${VERSION}) without verifying its checksum"
else
  status=0
  http_fetch "${base}/checksums.txt" "$meta" || status=$?
  case "$status" in
    0)
      expected="$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }' "$meta")"
      [ -n "$expected" ] || fail "checksums.txt of ${VERSION} does not list ${asset}, so it cannot be verified.
${retry_hint}"
      ;;
    1)
      # The latest-main release always has a checksums.txt. It is only missing for the few seconds
      # the release is being updated, so this is not a release without checksums.
      [ "$VERSION" != "main" ] || fail "checksums.txt is missing from the latest build of main, which is probably being updated.
${retry_hint}"
      if [ "$VERSION" = "latest" ]; then
        release_api="https://api.github.com/repos/${REPO}/releases/latest"
      else
        release_api="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
      fi
      status=0
      http_fetch "$release_api" "$meta" "${GITHUB_TOKEN:+Authorization: Bearer ${GITHUB_TOKEN}}" || status=$?
      [ "$status" -eq 0 ] || fail "could not fetch the checksum of ${asset} (${VERSION}) from the GitHub API (${http_detail}).
The API limits unauthenticated requests: setting GITHUB_TOKEN avoids that.
${retry_hint}"
      # Split the JSON on commas so each field is on its own line whether or not it is pretty-printed,
      # then take the digest that follows the asset's name. Assets uploaded before GitHub computed
      # digests have a null one: that is the one case where there is no checksum to verify against.
      # It must be stated by the response: an asset or a digest that is not there at all means the
      # response is incomplete or unexpected, not that there is no checksum.
      expected="$(tr ',' '\n' <"$meta" |
        awk -v asset="$asset" '
          /"name": *"/ { found = ($0 ~ "\"name\": *\"" asset "\"") }
          found && /"digest": *"sha256:/ { sub(/.*"sha256:/, ""); sub(/".*/, ""); print; exit }
          found && /"digest": *null/ { print "none"; exit }
        ')"
      case "$expected" in
        "")
          fail "could not read the checksum of ${asset} (${VERSION}) from the GitHub API response.
${retry_hint}"
          ;;
        none)
          expected=""
          verify=0
          warn "no checksum is published for ${asset} (${VERSION}); skipping verification"
          ;;
      esac
      ;;
    *)
      fail "could not fetch the checksum of ${asset} (${VERSION}) from ${base}/checksums.txt (${http_detail}).
${retry_hint}"
      ;;
  esac
fi
rm -f "$meta"
if command -v sha256sum >/dev/null 2>&1; then
  sha256="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  sha256="shasum -a 256"
else
  sha256=""
fi
if [ "$verify" -eq 1 ]; then
  if [ -z "$sha256" ]; then
    warn "neither sha256sum nor shasum is installed; skipping checksum verification"
  else
    actual="$($sha256 "$tmp" | awk '{ print $1 }')"
    [ "$actual" = "$expected" ] || fail "checksum mismatch for ${asset} (${VERSION}): expected ${expected}, got ${actual}.
The download may be corrupted or tampered with, or the release was being updated: try again in a minute.
Nothing was installed."
    echo "Checksum verified."
  fi
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
