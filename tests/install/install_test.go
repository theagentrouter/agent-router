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

// curlStub records the requested URL and writes an aigw stub to the -o path. The aigw stub
// records how it was called, so the test can check that Envoy is pre-downloaded.
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
cat > "$out" <<'EOF'
#!/bin/sh
echo "aigw $*" >> "$FAKE_LOG"
[ "$FAKE_ENVOY_FAIL" = "1" ] && exit 1
exit 0
EOF
`

// systemTools are the external commands the script needs besides uname and curl.
var systemTools = []string{"mktemp", "chmod", "mkdir", "mv", "rm", "cat"}

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
	dirInPath  bool // Add the install dir to the PATH.
	curlFails  bool
	envoyFails bool
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
	for _, tool := range systemTools {
		p, lookErr := exec.LookPath(tool)
		require.NoError(t, lookErr)
		require.NoError(t, os.Symlink(p, filepath.Join(bin, tool)))
	}

	installDir := filepath.Join(t.TempDir(), "bin")
	path := bin
	if o.dirInPath {
		path = installDir + ":" + path
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
	}{
		{name: "main by default", os: "Linux", arch: "x86_64", url: releasesURL + "download/latest-main/aigw-linux-amd64"},
		{name: "explicit main", os: "Linux", arch: "x86_64", version: "main", url: releasesURL + "download/latest-main/aigw-linux-amd64"},
		{name: "latest release", os: "Linux", arch: "x86_64", version: "latest", url: releasesURL + "latest/download/aigw-linux-amd64"},
		{name: "pinned release", os: "Linux", arch: "x86_64", version: "v1.3.0", url: releasesURL + "download/v1.3.0/aigw-linux-amd64"},
		{name: "linux aarch64", os: "Linux", arch: "aarch64", url: releasesURL + "download/latest-main/aigw-linux-arm64"},
		{name: "linux arm64", os: "Linux", arch: "arm64", url: releasesURL + "download/latest-main/aigw-linux-arm64"},
		{name: "linux amd64", os: "Linux", arch: "amd64", url: releasesURL + "download/latest-main/aigw-linux-amd64"},
		{name: "apple silicon", os: "Darwin", arch: "arm64", url: releasesURL + "download/latest-main/aigw-darwin-arm64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env []string
			if tt.version != "" {
				env = append(env, "AIGW_VERSION="+tt.version)
			}
			r := runInstall(t, options{os: tt.os, arch: tt.arch, env: env})
			require.NoError(t, r.err, "stdout: %s\nstderr: %s", r.stdout, r.stderr)
			require.Equal(t, []string{"curl " + tt.url, "aigw download-envoy"}, strings.Split(strings.TrimSpace(r.log), "\n"))

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
		require.Contains(t, r.stderr, "curl is required")
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
