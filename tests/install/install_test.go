// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package install tests the aigw install script (site/static/install.sh) without network
// access: "uname" and "curl" are replaced by stubs on a PATH that contains nothing else
// but the few system tools the script needs.
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const releasesURL = "https://github.com/theagentrouter/agent-router/releases/"

// unameStub prints the OS and architecture of the platform being simulated.
const unameStub = `#!/bin/sh
case "$1" in
  -s) echo "$FAKE_OS" ;;
  -m) echo "$FAKE_ARCH" ;;
esac
`

// aigwStub records how it was called, so the test can check that Envoy is pre-downloaded.
const aigwStub = `#!/bin/sh
echo "aigw $*" >> "$FAKE_LOG"
[ "$FAKE_ENVOY_FAIL" = "1" ] && exit 1
exit 0
`

// curlStub records the requested URL. It writes the aigw stub to the -o path, and prints
// the fake responses for checksums.txt and the GitHub API.
const curlStub = `#!/bin/sh
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
echo "curl $url" >> "$FAKE_LOG"
[ "$FAKE_CURL_FAIL" = "1" ] && exit 22
` + fakeResponses

// wgetStub is the wget equivalent of curlStub.
const wgetStub = `#!/bin/sh
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -qO-) out="-" ;;
    -qO) out="$2"; shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
echo "wget $url" >> "$FAKE_LOG"
[ "$FAKE_CURL_FAIL" = "1" ] && exit 8
` + fakeResponses

// fakeResponses serves $url: checksums.txt from $FAKE_CHECKSUMS, the GitHub API from $FAKE_API
// (both failing like a missing page when empty), and the aigw stub otherwise.
const fakeResponses = `case "$url" in
  */checksums.txt)
    [ -n "$FAKE_CHECKSUMS" ] || exit 22
    printf '%s' "$FAKE_CHECKSUMS"
    ;;
  https://api.github.com/*)
    [ -n "$FAKE_API" ] || exit 22
    printf '%s' "$FAKE_API"
    ;;
  *) cat "$FAKE_AIGW" > "$out" ;;
esac
`

// systemTools are the external commands the script needs besides uname and curl.
var systemTools = []string{"mktemp", "chmod", "mkdir", "mv", "rm", "cat", "awk", "tr"}

// sha256Tools are the checksum utilities the script can use. Only the first one found is
// needed: Linux has sha256sum, macOS only has shasum.
var sha256Tools = []string{"sha256sum", "shasum"}

type result struct {
	err        error
	stdout     string
	stderr     string
	log        string // Calls recorded by the curl and aigw stubs.
	installDir string
}

type options struct {
	os, arch   string
	env        []string
	noCurl     bool // Leave curl out of the PATH.
	wget       bool // Add wget to the PATH.
	otherAigw  bool // Put another aigw on the PATH, before the install dir.
	dirInPath  bool // Add the install dir to the PATH.
	curlFails  bool
	envoyFails bool
	// checksums is the content of checksums.txt. Empty means a valid one for every asset.
	checksums string
	// noChecksums simulates a release without checksums.txt.
	noChecksums bool
	// api is the GitHub API response for the release.
	api string
}

// validChecksums returns a checksums.txt matching the aigw stub for every published asset.
func validChecksums() string {
	sum := sha256.Sum256([]byte(aigwStub))
	h := hex.EncodeToString(sum[:])
	return fmt.Sprintf("%s  aigw-darwin-arm64\n%s  aigw-linux-amd64\n%s  aigw-linux-arm64\n", h, h, h)
}

func runInstall(t *testing.T, o options) result {
	t.Helper()
	script, err := filepath.Abs("../../site/static/install.sh")
	require.NoError(t, err)
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)

	// The PATH only has the stubs and symlinks to the system tools the script needs,
	// so the real curl is never reachable.
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "uname"), unameStub)
	if !o.noCurl {
		writeExecutable(t, filepath.Join(bin, "curl"), curlStub)
	}
	if o.wget {
		writeExecutable(t, filepath.Join(bin, "wget"), wgetStub)
	}
	for _, tool := range systemTools {
		p, lookErr := exec.LookPath(tool)
		require.NoError(t, lookErr)
		require.NoError(t, os.Symlink(p, filepath.Join(bin, tool)))
	}
	foundSHA256 := false
	for _, tool := range sha256Tools {
		if p, lookErr := exec.LookPath(tool); lookErr == nil {
			require.NoError(t, os.Symlink(p, filepath.Join(bin, tool)))
			foundSHA256 = true
			break
		}
	}
	require.True(t, foundSHA256, "one of %v is required", sha256Tools)

	aigw := filepath.Join(t.TempDir(), "aigw")
	writeExecutable(t, aigw, aigwStub)
	checksums := o.checksums
	if checksums == "" {
		checksums = validChecksums()
	}
	if o.noChecksums {
		checksums = ""
	}

	installDir := filepath.Join(t.TempDir(), "bin")
	path := bin
	if o.dirInPath {
		path = installDir + ":" + path
	}
	if o.otherAigw {
		other := t.TempDir()
		writeExecutable(t, filepath.Join(other, "aigw"), aigwStub)
		path = other + ":" + path
	}
	logFile := filepath.Join(t.TempDir(), "calls.log")
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}

	cmd := exec.CommandContext(t.Context(), sh, script) //nolint:gosec // the script path is fixed.
	cmd.Env = append([]string{
		"PATH=" + path,
		"HOME=" + t.TempDir(),
		"AIGW_INSTALL_DIR=" + installDir,
		"FAKE_OS=" + o.os,
		"FAKE_ARCH=" + o.arch,
		"FAKE_LOG=" + logFile,
		"FAKE_AIGW=" + aigw,
		"FAKE_CHECKSUMS=" + checksums,
		"FAKE_API=" + o.api,
		"FAKE_CURL_FAIL=" + flag(o.curlFails),
		"FAKE_ENVOY_FAIL=" + flag(o.envoyFails),
	}, o.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	log, _ := os.ReadFile(logFile)
	return result{err: runErr, stdout: stdout.String(), stderr: stderr.String(), log: string(log), installDir: installDir}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755)) //nolint:gosec // test stubs must be executable.
}

func TestInstall_DownloadURL(t *testing.T) {
	tests := []struct {
		name     string
		os, arch string
		version  string
		url      string
		sums     string
	}{
		{name: "main by default", os: "Linux", arch: "x86_64", url: releasesURL + "download/latest-main/aigw-linux-amd64", sums: releasesURL + "download/latest-main/checksums.txt"},
		{name: "explicit main", os: "Linux", arch: "x86_64", version: "main", url: releasesURL + "download/latest-main/aigw-linux-amd64", sums: releasesURL + "download/latest-main/checksums.txt"},
		{name: "latest release", os: "Linux", arch: "x86_64", version: "latest", url: releasesURL + "latest/download/aigw-linux-amd64", sums: releasesURL + "latest/download/checksums.txt"},
		{name: "pinned release without v", os: "Linux", arch: "x86_64", version: "1.3.0", url: releasesURL + "download/v1.3.0/aigw-linux-amd64", sums: releasesURL + "download/v1.3.0/checksums.txt"},
		{name: "pinned release", os: "Linux", arch: "x86_64", version: "v1.3.0", url: releasesURL + "download/v1.3.0/aigw-linux-amd64", sums: releasesURL + "download/v1.3.0/checksums.txt"},
		{name: "linux aarch64", os: "Linux", arch: "aarch64", url: releasesURL + "download/latest-main/aigw-linux-arm64", sums: releasesURL + "download/latest-main/checksums.txt"},
		{name: "linux arm64", os: "Linux", arch: "arm64", url: releasesURL + "download/latest-main/aigw-linux-arm64", sums: releasesURL + "download/latest-main/checksums.txt"},
		{name: "linux amd64", os: "Linux", arch: "amd64", url: releasesURL + "download/latest-main/aigw-linux-amd64", sums: releasesURL + "download/latest-main/checksums.txt"},
		{name: "apple silicon", os: "Darwin", arch: "arm64", url: releasesURL + "download/latest-main/aigw-darwin-arm64", sums: releasesURL + "download/latest-main/checksums.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env []string
			if tt.version != "" {
				env = append(env, "AIGW_VERSION="+tt.version)
			}
			r := runInstall(t, options{os: tt.os, arch: tt.arch, env: env})
			require.NoError(t, r.err, "stdout: %s\nstderr: %s", r.stdout, r.stderr)
			require.Equal(t, []string{"curl " + tt.url, "curl " + tt.sums, "aigw download-envoy"}, strings.Split(strings.TrimSpace(r.log), "\n"))
			require.Contains(t, r.stdout, "Checksum verified.")

			info, err := os.Stat(filepath.Join(r.installDir, "aigw"))
			require.NoError(t, err)
			require.NotZero(t, info.Mode().Perm()&0o100, "aigw must be executable")
			require.Contains(t, r.stdout, "Installed aigw to "+filepath.Join(r.installDir, "aigw"))
		})
	}
}

func TestInstall_Unsupported(t *testing.T) {
	tests := []struct {
		name     string
		os, arch string
		err      string
	}{
		{name: "intel mac", os: "Darwin", arch: "x86_64", err: "not published for Intel Macs"},
		{name: "windows", os: "MINGW64_NT-10.0", arch: "x86_64", err: "unsupported OS: MINGW64_NT-10.0"},
		{name: "32-bit arm", os: "Linux", arch: "armv7l", err: "unsupported architecture: armv7l"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runInstall(t, options{os: tt.os, arch: tt.arch})
			require.Error(t, r.err)
			require.Contains(t, r.stderr, tt.err)
			require.Empty(t, r.log, "nothing must be downloaded")
			require.NoFileExists(t, filepath.Join(r.installDir, "aigw"))
		})
	}
}

func TestInstall_Failures(t *testing.T) {
	t.Run("curl missing", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", noCurl: true})
		require.Error(t, r.err)
		require.Contains(t, r.stderr, "curl or wget is required")
	})

	t.Run("download fails", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", curlFails: true})
		require.Error(t, r.err)
		require.Contains(t, r.stderr, "failed to download "+releasesURL+"download/latest-main/aigw-linux-amd64")
		require.NoFileExists(t, filepath.Join(r.installDir, "aigw"))
	})

	t.Run("envoy download fails", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", envoyFails: true})
		require.NoError(t, r.err, "the install must succeed anyway")
		require.Contains(t, r.stderr, "warning: failed to pre-download Envoy")
		require.FileExists(t, filepath.Join(r.installDir, "aigw"))
	})
}

func TestInstall_PathHint(t *testing.T) {
	r := runInstall(t, options{os: "Linux", arch: "x86_64"})
	require.NoError(t, r.err)
	require.Contains(t, r.stdout, r.installDir+" is not in your PATH")

	r = runInstall(t, options{os: "Linux", arch: "x86_64", dirInPath: true})
	require.NoError(t, r.err)
	require.NotContains(t, r.stdout, "is not in your PATH")
}

func TestInstall_Checksums(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", checksums: strings.Repeat("0", 64) + "  aigw-linux-amd64\n"})
		require.Error(t, r.err)
		require.Contains(t, r.stderr, "checksum mismatch for aigw-linux-amd64")
		require.NotContains(t, r.log, "aigw download-envoy")
		requireNothingInstalled(t, r.installDir)
	})

	t.Run("asset missing from checksums.txt", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", checksums: strings.Repeat("0", 64) + "  aigw-darwin-arm64\n"})
		require.NoError(t, r.err, "stderr: %s", r.stderr)
		require.Contains(t, r.stderr, "warning: no checksum is published for aigw-linux-amd64")
		require.FileExists(t, filepath.Join(r.installDir, "aigw"))
	})

	t.Run("checksums.txt not published", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", noChecksums: true, env: []string{"AIGW_VERSION=v1.3.0"}})
		require.NoError(t, r.err, "stderr: %s", r.stderr)
		require.Contains(t, r.stderr, "warning: no checksum is published for aigw-linux-amd64 (v1.3.0)")
		require.NotContains(t, r.stdout, "Checksum verified.")
		info, err := os.Stat(filepath.Join(r.installDir, "aigw"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	})

	t.Run("binary-mode marker in checksums.txt", func(t *testing.T) {
		sum := sha256.Sum256([]byte(aigwStub))
		r := runInstall(t, options{os: "Linux", arch: "x86_64", checksums: hex.EncodeToString(sum[:]) + " *aigw-linux-amd64\n"})
		require.NoError(t, r.err, "stderr: %s", r.stderr)
		require.Contains(t, r.stdout, "Checksum verified.")
	})
}

func TestInstall_InstallDir(t *testing.T) {
	t.Run("not creatable", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		dir := filepath.Join(file, "bin")
		r := runInstall(t, options{os: "Linux", arch: "x86_64", env: []string{"AIGW_INSTALL_DIR=" + dir}})
		require.Error(t, r.err)
		require.Contains(t, r.stderr, "cannot create "+dir)
		require.Empty(t, r.log, "nothing must be downloaded")
	})

	t.Run("failed download leaves no partial file", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", curlFails: true})
		require.Error(t, r.err)
		requireNothingInstalled(t, r.installDir)
	})
}

// requireNothingInstalled checks that neither aigw nor a partial download is left in dir.
func requireNothingInstalled(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Empty(t, names)
}

func TestInstall_GitHubDigestFallback(t *testing.T) {
	sum := sha256.Sum256([]byte(aigwStub))
	digest := hex.EncodeToString(sum[:])
	// The API lists other assets too: the digest must be the one of the requested asset.
	api := `{"tag_name":"v1.3.0","assets":[` +
		`{"name":"aigw-darwin-arm64","digest":"sha256:` + strings.Repeat("1", 64) + `"},` +
		`{"name":"aigw-linux-amd64","digest":"sha256:` + digest + `"},` +
		`{"name":"aigw-linux-arm64","digest":"sha256:` + strings.Repeat("2", 64) + `"}]}`

	t.Run("pinned release", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", noChecksums: true, api: api, env: []string{"AIGW_VERSION=v1.3.0"}})
		require.NoError(t, r.err, "stderr: %s", r.stderr)
		require.Contains(t, r.stdout, "Checksum verified.")
		require.Contains(t, r.log, "curl https://api.github.com/repos/theagentrouter/agent-router/releases/tags/v1.3.0")
	})

	t.Run("latest release", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", noChecksums: true, api: api, env: []string{"AIGW_VERSION=latest"}})
		require.NoError(t, r.err, "stderr: %s", r.stderr)
		require.Contains(t, r.stdout, "Checksum verified.")
		require.Contains(t, r.log, "curl https://api.github.com/repos/theagentrouter/agent-router/releases/latest")
	})

	t.Run("mismatch", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "aarch64", noChecksums: true, api: api, env: []string{"AIGW_VERSION=v1.3.0"}})
		require.Error(t, r.err)
		require.Contains(t, r.stderr, "checksum mismatch for aigw-linux-arm64")
		requireNothingInstalled(t, r.installDir)
	})

	t.Run("not used for main", func(t *testing.T) {
		r := runInstall(t, options{os: "Linux", arch: "x86_64", noChecksums: true, api: api})
		require.NoError(t, r.err, "stderr: %s", r.stderr)
		require.Contains(t, r.stderr, "warning: no checksum is published for aigw-linux-amd64 (main)")
		require.NotContains(t, r.log, "api.github.com")
	})
}

func TestInstall_Wget(t *testing.T) {
	r := runInstall(t, options{os: "Linux", arch: "x86_64", noCurl: true, wget: true})
	require.NoError(t, r.err, "stderr: %s", r.stderr)
	require.Equal(t, []string{
		"wget " + releasesURL + "download/latest-main/aigw-linux-amd64",
		"wget " + releasesURL + "download/latest-main/checksums.txt",
		"aigw download-envoy",
	}, strings.Split(strings.TrimSpace(r.log), "\n"))
	require.Contains(t, r.stdout, "Checksum verified.")
	require.FileExists(t, filepath.Join(r.installDir, "aigw"))
}

func TestInstall_UnsupportedSuggestsDocker(t *testing.T) {
	r := runInstall(t, options{os: "Darwin", arch: "x86_64"})
	require.Error(t, r.err)
	require.Contains(t, r.stderr, "docker run --rm -p 1975:1975")
}

func TestInstall_ShadowedBinary(t *testing.T) {
	r := runInstall(t, options{os: "Linux", arch: "x86_64", dirInPath: true, otherAigw: true})
	require.NoError(t, r.err, "stderr: %s", r.stderr)
	require.Contains(t, r.stderr, "will shadow the new aigw")

	r = runInstall(t, options{os: "Linux", arch: "x86_64", dirInPath: true})
	require.NoError(t, r.err, "stderr: %s", r.stderr)
	require.NotContains(t, r.stderr, "shadow")
}

func TestInstall_PathHintPerShell(t *testing.T) {
	tests := []struct {
		shell, os, profile, line string
	}{
		{shell: "/bin/zsh", os: "Darwin", profile: "~/.zshrc", line: "export PATH="},
		{shell: "/bin/bash", os: "Linux", profile: "~/.bashrc", line: "export PATH="},
		{shell: "/bin/bash", os: "Darwin", profile: "~/.bash_profile", line: "export PATH="},
		{shell: "/usr/bin/fish", os: "Linux", profile: "~/.config/fish/config.fish", line: "fish_add_path "},
		{shell: "/bin/ksh", os: "Linux", profile: "your shell profile", line: "export PATH="},
	}
	for _, tt := range tests {
		t.Run(tt.shell+" on "+tt.os, func(t *testing.T) {
			r := runInstall(t, options{os: tt.os, arch: "arm64", env: []string{"SHELL=" + tt.shell}})
			require.NoError(t, r.err, "stderr: %s", r.stderr)
			require.Contains(t, r.stdout, "and to "+tt.profile+" to keep it")
			require.Contains(t, r.stdout, "  "+tt.line)
		})
	}
}
