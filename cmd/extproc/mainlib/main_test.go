// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mainlib

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func Test_newLogHandler(t *testing.T) {
	t.Run("json emits parseable records", func(t *testing.T) {
		var buf bytes.Buffer
		slog.New(newLogHandler(&buf, slog.LevelInfo, internalapi.LogFormatJSON)).
			Info("starting external processor", slog.String("address", ":1063"))

		var record map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
		require.Equal(t, "starting external processor", record["msg"])
		require.Equal(t, ":1063", record["address"])
		require.Equal(t, "INFO", record["level"])
	})

	t.Run("text is unchanged and is the fallback", func(t *testing.T) {
		for _, format := range []string{internalapi.LogFormatText, ""} {
			var buf bytes.Buffer
			slog.New(newLogHandler(&buf, slog.LevelInfo, format)).
				Info("starting external processor", slog.String("address", ":1063"))

			require.Contains(t, buf.String(), `msg="starting external processor" address=:1063`)
			var record map[string]any
			require.Error(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record), "text output must not be JSON")
		}
	})

	t.Run("level is honored", func(t *testing.T) {
		var buf bytes.Buffer
		slog.New(newLogHandler(&buf, slog.LevelWarn, internalapi.LogFormatJSON)).Info("filtered out")
		require.Empty(t, buf.String())
	})
}

func Test_parseAndValidateFlags(t *testing.T) {
	t.Run("ok extProcFlags", func(t *testing.T) {
		for _, tc := range []struct {
			name             string
			args             []string
			configBundlePath string
			addr             string
			rootPrefix       string
			logLevel         slog.Level
			logFormat        string
			enableRedaction  bool
		}{
			{
				name:             "minimal extProcFlags",
				args:             []string{"-configBundlePath", "/path/to/config-bundle"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelInfo,
				logFormat:        "text",
				enableRedaction:  false,
			},
			{
				name:             "log format json",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-logFormat", "json"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelInfo,
				logFormat:        "json",
				enableRedaction:  false,
			},
			{
				name:             "custom addr",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-extProcAddr", "unix:///tmp/ext_proc.sock"},
				configBundlePath: "/path/to/config-bundle",
				addr:             "unix:///tmp/ext_proc.sock",
				rootPrefix:       "/",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
			{
				name:             "log level debug",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-logLevel", "debug"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelDebug,
				enableRedaction:  false,
			},
			{
				name:             "log level debug with redaction enabled",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-logLevel", "debug", "-enableRedaction"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelDebug,
				enableRedaction:  true,
			},
			{
				name:             "log level warn",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-logLevel", "warn"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelWarn,
				enableRedaction:  false,
			},
			{
				name:             "log level error",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-logLevel", "error"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelError,
				enableRedaction:  false,
			},
			{
				name: "all extProcFlags",
				args: []string{
					"-configBundlePath", "/path/to/config-bundle",
					"-extProcAddr", "unix:///tmp/ext_proc.sock",
					"-logLevel", "debug",
					"-rootPrefix", "/foo/bar/",
				},
				configBundlePath: "/path/to/config-bundle",
				addr:             "unix:///tmp/ext_proc.sock",
				rootPrefix:       "/foo/bar/",
				logLevel:         slog.LevelDebug,
				enableRedaction:  false,
			},
			{
				name:             "with endpoint prefixes",
				args:             []string{"-configBundlePath", "/path/to/config-bundle", "-endpointPrefixes", "openai:/,cohere:/cohere,anthropic:/anthropic"},
				configBundlePath: "/path/to/config-bundle",
				addr:             ":1063",
				rootPrefix:       "/",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
			{
				name: "with metrics header mapping",
				args: []string{
					"-configBundlePath", "/path/to/config-bundle",
					"-metricsRequestHeaderAttributes", "x-tenant-id:tenant.id,x-tenant-id:tenant.id",
				},
				configBundlePath: "/path/to/config-bundle",
				rootPrefix:       "/",
				addr:             ":1063",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
			{
				name: "with base header mapping",
				args: []string{
					"-configBundlePath", "/path/to/config-bundle",
					"-metricsRequestHeaderAttributes", "x-team-id:team.id,x-user-id:user.id",
				},
				configBundlePath: "/path/to/config-bundle",
				rootPrefix:       "/",
				addr:             ":1063",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
			{
				name: "with tracing header attributes",
				args: []string{
					"-configBundlePath", "/path/to/config-bundle",
					"-spanRequestHeaderAttributes", "x-session-id:session.id,x-user-id:user.id",
				},
				configBundlePath: "/path/to/config-bundle",
				rootPrefix:       "/",
				addr:             ":1063",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
			{
				name: "with both metrics and tracing headers",
				args: []string{
					"-configBundlePath", "/path/to/config-bundle",
					"-metricsRequestHeaderAttributes", "x-user-id:user.id",
					"-spanRequestHeaderAttributes", "x-session-id:session.id",
				},
				configBundlePath: "/path/to/config-bundle",
				rootPrefix:       "/",
				addr:             ":1063",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
			{
				name:             "bundle path only",
				args:             []string{"-configBundlePath", "/path/to/config-bundle"},
				configBundlePath: "/path/to/config-bundle",
				rootPrefix:       "/",
				addr:             ":1063",
				logLevel:         slog.LevelInfo,
				enableRedaction:  false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				flags, err := parseAndValidateFlags(tc.args)
				require.NoError(t, err)
				require.Equal(t, tc.configBundlePath, flags.configBundlePath)
				require.Equal(t, tc.addr, flags.extProcAddr)
				require.Equal(t, tc.logLevel, flags.logLevel)
				// Cases that do not care about the format expect the default.
				wantLogFormat := tc.logFormat
				if wantLogFormat == "" {
					wantLogFormat = "text"
				}
				require.Equal(t, wantLogFormat, flags.logFormat)
				require.Equal(t, tc.enableRedaction, flags.enableRedaction)
				require.Equal(t, tc.rootPrefix, flags.rootPrefix)
			})
		}
	})

	t.Run("invalid extProcFlags", func(t *testing.T) {
		tests := []struct {
			name          string
			args          []string
			expectedError string
		}{
			{
				name:          "invalid log level",
				args:          []string{"-logLevel", "invalid"},
				expectedError: "configBundlePath must be provided\nfailed to unmarshal log level: slog: level string \"invalid\": unknown name",
			},
			{
				name:          "invalid log format",
				args:          []string{"-configBundlePath", "/path/to/config-bundle", "-logFormat", "yaml"},
				expectedError: `invalid log format: "yaml", must be "text" or "json"`,
			},
			{
				name:          "invalid endpoint prefixes - unknown key",
				args:          []string{"-configBundlePath", "/path/to/config-bundle", "-endpointPrefixes", "foo:/x"},
				expectedError: "failed to parse endpoint prefixes: unknown endpointPrefixes key \"foo\" at position 1 (allowed: openai, cohere, anthropic)",
			},
			{
				name:          "invalid endpoint prefixes - missing colon",
				args:          []string{"-configBundlePath", "/path/to/config-bundle", "-endpointPrefixes", "openai"},
				expectedError: "failed to parse endpoint prefixes: invalid endpointPrefixes pair at position 1: \"openai\" (expected format: key:value)",
			},
			{
				name:          "invalid tracing header attributes - missing colon",
				args:          []string{"-configBundlePath", "/path/to/config-bundle", "-spanRequestHeaderAttributes", "x-session-id"},
				expectedError: "failed to parse tracing header mapping: invalid header-attribute pair at position 1: \"x-session-id\" (expected format: header:attribute)",
			},
			{
				name:          "invalid tracing header attributes - empty header",
				args:          []string{"-configBundlePath", "/path/to/config-bundle", "-spanRequestHeaderAttributes", ":session.id"},
				expectedError: "failed to parse tracing header mapping: empty header or attribute at position 1: \":session.id\"",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := parseAndValidateFlags(tt.args)
				require.EqualError(t, err, tt.expectedError)
			})
		}
	})

	t.Run("legacy config path is rejected", func(t *testing.T) {
		_, err := parseAndValidateFlags([]string{"-configPath", "/path/to/config.yaml"})
		require.EqualError(t, err, "failed to parse extProcFlags: flag provided but not defined: -configPath")
	})
}

func TestListenAddress(t *testing.T) {
	unixPath := t.TempDir() + "/extproc.sock"
	// Create a stale file to ensure that removing the file works correctly.
	require.NoError(t, os.WriteFile(unixPath, []byte("stale socket"), 0o600))

	lis, err := listen(t.Context(), t.Name(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer lis.Close() //nolint:errcheck

	tests := []struct {
		addr        string
		wantNetwork string
		wantAddress string
	}{
		{lis.Addr().String(), "tcp", lis.Addr().String()},
		{"unix://" + unixPath, "unix", unixPath},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			network, address := listenAddress(tt.addr)
			require.Equal(t, tt.wantNetwork, network)
			require.Equal(t, tt.wantAddress, address)
		})
	}
	_, err = os.Stat(unixPath)
	require.ErrorIs(t, err, os.ErrNotExist, "expected the stale socket file to be removed")
}

// TestExtProcStartupMessage ensures other programs can rely on the startup message to STDERR.
func TestExtProcStartupMessage(t *testing.T) {
	// Create a temporary config bundle.
	tmpDir := t.TempDir()
	configRaw := []byte(`
version: dev
backends:
- name: openai
  schema:
    name: OpenAI
    version: v1
`)
	configBundlePath := filepath.Join(tmpDir, "config-bundle")
	part := filterapi.ConfigBundlePart{Name: "config", Path: filterapi.ConfigBundlePartPath(0), SizeBytes: len(configRaw)}
	partPath := filepath.Join(configBundlePath, filepath.FromSlash(part.Path))
	require.NoError(t, os.MkdirAll(filepath.Dir(partPath), 0o700))
	require.NoError(t, os.WriteFile(partPath, configRaw, 0o600))
	indexRaw, err := filterapi.MarshalConfigBundleIndex(&filterapi.ConfigBundleIndex{
		Checksum: filterapi.ConfigBundleChecksum(configRaw),
		Parts:    []filterapi.ConfigBundlePart{part},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configBundlePath, filterapi.ConfigBundleIndexFileName), indexRaw, 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Create a pipe for stderr.
	stderrR, stderrW := io.Pipe()

	// Start a goroutine to scan stderr until it reaches "AI Gateway External Processor is ready" written by envoy.
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "AI Gateway External Processor is ready") {
				cancel() // interrupts extproc.
				return
			}
		}
	}()

	// UNIX doesn't like the long socket paths, so create a temp directory for the socket instead of t.TempDir.
	socketTempDir := "/tmp/" + uuid.NewString()
	t.Cleanup(func() { _ = os.RemoveAll(socketTempDir) })
	require.NoError(t, os.MkdirAll(socketTempDir, 0o700))
	socketPath := filepath.Join(socketTempDir, "mcp.sock")

	// Run ExtProc in a goroutine on ephemeral ports.
	errCh := make(chan error, 1)
	go func() {
		args := []string{
			"-configBundlePath", configBundlePath,
			"-extProcAddr", ":0",
			"-adminPort", "0",
			"-mcpAddr", "unix://" + socketPath,
			"-logLevel", "info",
		}
		errCh <- Main(ctx, args, stderrW)
	}()

	timeout, cancelTimeout := context.WithTimeout(t.Context(), time.Second*3)
	defer cancelTimeout()
	select {
	case <-ctx.Done():
	case <-timeout.Done():
		t.Fatal("timeout waiting for startup message")
	case err := <-errCh:
		require.NoError(t, err, "extproc exited with error before startup message")
	}
}
