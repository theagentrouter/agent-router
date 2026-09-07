// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tetratelabs/func-e/api"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/version"
)

// TestRun verifies that the main run function starts up correctly without making any actual requests.
//
// The real e2e tests are in tests/e2e-aigw.
func TestRun(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	// Note: we do not make any real requests here!
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_API_KEY", "unused")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := testRunOpts(t, func(context.Context, []string, io.Writer) error { return nil })
	require.NoError(t, run(ctx, &cmdRun{Debug: true}, opts, os.Stdout, os.Stderr))
}

func TestRunExtprocStartFailure(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("OPENAI_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("OPENAI_API_KEY", "unused")

	ctx := t.Context()
	errChan := make(chan error)
	mockErr := errors.New("mock extproc error")
	go func() {
		opts := testRunOpts(t, func(context.Context, []string, io.Writer) error { return mockErr })
		errChan <- run(ctx, &cmdRun{}, opts, os.Stdout, io.Discard)
	}()

	select {
	case <-time.After(10 * time.Second):
		t.Fatal("expected extproc start to fail promptly")
	case err := <-errChan:
		require.ErrorIs(t, err, errExtProcRun)
		require.ErrorIs(t, err, mockErr)
	}
}

func TestRunCmdContext_writeEnvoyResourcesAndRunExtProc(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("OPENAI_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("OPENAI_API_KEY", "unused")

	runCtx := &runCmdContext{
		envoyGatewayResourcesOut: &bytes.Buffer{},
		stderrLogger:             slog.New(slog.DiscardHandler),
		stderr:                   io.Discard,
		tmpdir:                   t.TempDir(),
		extProcLauncher: func(ctx context.Context, _ []string, _ io.Writer) error {
			<-ctx.Done()
			return nil
		},
	}
	config := readFileFromProjectRoot(t, "examples/aigw/ollama.yaml")
	ctx, cancel := context.WithCancel(t.Context())
	_, done, _, err := runCtx.writeEnvoyResourcesAndRunExtProc(ctx, config)
	require.NoError(t, err)
	cancel()
	// Wait for the external processor to stop.
	require.NoError(t, <-done)
}

//go:embed testdata/gateway_no_listeners.yaml
var gatewayNoListenersConfig string

func TestRunCmdContext_writeEnvoyResourcesAndRunExtProc_noListeners(t *testing.T) {
	runCtx := &runCmdContext{
		envoyGatewayResourcesOut: &bytes.Buffer{},
		stderrLogger:             slog.New(slog.DiscardHandler),
		stderr:                   io.Discard,
		tmpdir:                   t.TempDir(),
		udsPath:                  filepath.Join("/tmp", "run-test.sock"),
	}

	_, _, _, err := runCtx.writeEnvoyResourcesAndRunExtProc(t.Context(), gatewayNoListenersConfig)
	require.EqualError(t, err, "gateway aigw-run has no listeners configured")
}

func Test_mustStartExtProc(t *testing.T) {
	mockErr := errors.New("mock extproc error")
	var capturedArgs []string
	runCtx := &runCmdContext{
		stderrLogger: slog.New(slog.DiscardHandler),
		stderr:       io.Discard,
		tmpdir:       t.TempDir(),
		adminPort:    1064,
		extProcLauncher: func(_ context.Context, args []string, _ io.Writer) error {
			capturedArgs = args
			return mockErr
		},
	}
	done := runCtx.mustStartExtProc(t.Context(), &filterapi.Config{Version: version.Parse()})
	require.ErrorIs(t, <-done, mockErr)

	bundlePath := findFlagValue(capturedArgs, "--configBundlePath")
	require.NotEmpty(t, bundlePath)
	indexRaw, err := os.ReadFile(filepath.Join(bundlePath, filterapi.ConfigBundleIndexFileName))
	require.NoError(t, err)
	index, err := filterapi.UnmarshalConfigBundleIndex(indexRaw)
	require.NoError(t, err)
	cfg, err := filterapi.ReassembleBundleConfig(index, func(part filterapi.ConfigBundlePart) ([]byte, error) {
		return os.ReadFile(filepath.Join(bundlePath, filepath.FromSlash(part.Path)))
	})
	require.NoError(t, err)
	require.Equal(t, version.Parse(), cfg.Version)
}

func Test_mustStartExtProc_defaultHeaderAttributes(t *testing.T) {
	var capturedArgs []string
	runCtx := &runCmdContext{
		stderrLogger: slog.New(slog.DiscardHandler),
		stderr:       io.Discard,
		tmpdir:       t.TempDir(),
		adminPort:    1064,
		extProcLauncher: func(_ context.Context, args []string, _ io.Writer) error {
			capturedArgs = args
			return errors.New("mock error")
		},
	}

	<-runCtx.mustStartExtProc(t.Context(), &filterapi.Config{Version: version.Parse()})

	require.NotContains(t, capturedArgs, "-requestHeaderAttributes")
	require.NotContains(t, capturedArgs, "-metricsRequestHeaderAttributes")
	require.NotContains(t, capturedArgs, "-spanRequestHeaderAttributes")
	require.NotContains(t, capturedArgs, "-logRequestHeaderAttributes")
}

func Test_mustStartExtProc_withHeaderAttributes(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("OTEL_AIGW_REQUEST_HEADER_ATTRIBUTES", "x-tenant-id:tenant.id")
	t.Setenv("OTEL_AIGW_SPAN_REQUEST_HEADER_ATTRIBUTES", "x-forwarded-proto:url.scheme")
	t.Setenv("OTEL_AIGW_METRICS_REQUEST_HEADER_ATTRIBUTES", "x-tenant-id:tenant.id")
	t.Setenv("OTEL_AIGW_LOG_REQUEST_HEADER_ATTRIBUTES", "x-forwarded-proto:url.scheme")

	var capturedArgs []string
	runCtx := &runCmdContext{
		stderrLogger: slog.New(slog.DiscardHandler),
		stderr:       io.Discard,
		tmpdir:       t.TempDir(),
		adminPort:    1064,
		extProcLauncher: func(_ context.Context, args []string, _ io.Writer) error {
			capturedArgs = args
			return errors.New("mock error") // Return error to stop execution
		},
	}

	done := runCtx.mustStartExtProc(t.Context(), &filterapi.Config{Version: version.Parse()})
	<-done // Wait for completion

	require.Contains(t, capturedArgs, "-requestHeaderAttributes")
	baseValue := findFlagValue(capturedArgs, "-requestHeaderAttributes")
	require.Equal(t, "x-tenant-id:tenant.id", baseValue)
	require.Contains(t, capturedArgs, "-spanRequestHeaderAttributes")
	spanValue := findFlagValue(capturedArgs, "-spanRequestHeaderAttributes")
	require.Equal(t, "x-forwarded-proto:url.scheme", spanValue)
	require.Contains(t, capturedArgs, "-metricsRequestHeaderAttributes")
	metricsValue := findFlagValue(capturedArgs, "-metricsRequestHeaderAttributes")
	require.Equal(t, "x-tenant-id:tenant.id", metricsValue)
	require.Contains(t, capturedArgs, "-logRequestHeaderAttributes")
	logValue := findFlagValue(capturedArgs, "-logRequestHeaderAttributes")
	require.Equal(t, "x-forwarded-proto:url.scheme", logValue)
}

func Test_mustStartExtProc_emptyHeaderAttributesClearsDefaults(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("OTEL_AIGW_REQUEST_HEADER_ATTRIBUTES", "x-tenant-id:tenant.id")
	t.Setenv("OTEL_AIGW_SPAN_REQUEST_HEADER_ATTRIBUTES", "")
	t.Setenv("OTEL_AIGW_METRICS_REQUEST_HEADER_ATTRIBUTES", "x-tenant-id:tenant.id")
	t.Setenv("OTEL_AIGW_LOG_REQUEST_HEADER_ATTRIBUTES", "")

	var capturedArgs []string
	runCtx := &runCmdContext{
		stderrLogger: slog.New(slog.DiscardHandler),
		stderr:       io.Discard,
		tmpdir:       t.TempDir(),
		adminPort:    1064,
		extProcLauncher: func(_ context.Context, args []string, _ io.Writer) error {
			capturedArgs = args
			return errors.New("mock error")
		},
	}

	<-runCtx.mustStartExtProc(t.Context(), &filterapi.Config{Version: version.Parse()})

	require.Contains(t, capturedArgs, "-requestHeaderAttributes")
	baseValue := findFlagValue(capturedArgs, "-requestHeaderAttributes")
	require.Equal(t, "x-tenant-id:tenant.id", baseValue)
	require.Contains(t, capturedArgs, "-spanRequestHeaderAttributes")
	spanValue := findFlagValue(capturedArgs, "-spanRequestHeaderAttributes")
	require.Empty(t, spanValue)
	require.Contains(t, capturedArgs, "-metricsRequestHeaderAttributes")
	metricsValue := findFlagValue(capturedArgs, "-metricsRequestHeaderAttributes")
	require.Equal(t, "x-tenant-id:tenant.id", metricsValue)
	require.Contains(t, capturedArgs, "-logRequestHeaderAttributes")
	logValue := findFlagValue(capturedArgs, "-logRequestHeaderAttributes")
	require.Empty(t, logValue)
}

func TestTryFindEnvoyListenerPort(t *testing.T) {
	gwWithListener := func(port gwapiv1.PortNumber) *gwapiv1.Gateway {
		return &gwapiv1.Gateway{
			Spec: gwapiv1.GatewaySpec{
				Listeners: []gwapiv1.Listener{
					{Port: port},
				},
			},
		}
	}

	tests := []struct {
		name     string
		gw       *gwapiv1.Gateway
		expected int
	}{
		{
			name:     "gateway with no listeners",
			gw:       &gwapiv1.Gateway{},
			expected: 0,
		},
		{
			name:     "gateway with listener on port 1975",
			gw:       gwWithListener(1975),
			expected: 1975,
		},
	}

	runCtx := &runCmdContext{
		stderrLogger: slog.New(slog.DiscardHandler),
		tmpdir:       t.TempDir(),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := runCtx.tryFindEnvoyListenerPort(tt.gw)
			require.Equal(t, tt.expected, port)
		})
	}
}

func Test_newEnvoyMiddleware(t *testing.T) {
	tests := []struct {
		name         string
		inputOptions []api.RunOption
	}{
		{
			name: "no input options",
		},
		{
			name:         "options appended",
			inputOptions: []api.RunOption{api.EnvoyVersion("1.2.3")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			start := time.Now()
			listenerPort := 1975

			dirs := newTempDirectories(t)
			middleware := newEnvoyRunMiddleware(dirs, "test-run", start, listenerPort, &stdout, &stderr)
			require.NotNil(t, middleware)

			err := middleware(func(ctx context.Context, args []string, options ...api.RunOption) error {
				require.Equal(t, t.Context(), ctx)
				require.Equal(t, []string{"test"}, args)

				// 8 = EnvoyOut, EnvoyErr, ConfigHome, DataHome, StateHome, RuntimeDir, RunID, StartupHook
				require.Len(t, options, 8+len(tt.inputOptions))
				return nil
			})(t.Context(), []string{"test"}, tt.inputOptions...)
			require.NoError(t, err)
		})
	}
}

func Test_newRunnerErrorHandler(t *testing.T) {
	tests := []struct {
		name           string
		runner         string
		expectCancel   bool
		expectInStderr bool
	}{
		{
			name:           "provider runner triggers graceful shutdown",
			runner:         "provider",
			expectCancel:   true,
			expectInStderr: true,
		},
		{
			name:           "infrastructure runner triggers graceful shutdown",
			runner:         "infrastructure",
			expectCancel:   true,
			expectInStderr: true,
		},
		{
			name:         "gateway-api runner does not trigger shutdown",
			runner:       "gateway-api",
			expectCancel: false,
		},
		{
			name:         "xds-translator runner does not trigger shutdown",
			runner:       "xds-translator",
			expectCancel: false,
		},
		{
			name:         "unknown runner does not trigger shutdown",
			runner:       "unknown",
			expectCancel: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var stderr bytes.Buffer
			handler := newRunnerErrorHandler(&stderr, cancel)

			mockErr := errors.New("something went wrong")
			handler(tt.runner, mockErr)

			if tt.expectCancel {
				require.Error(t, ctx.Err(), "context should be cancelled")
			} else {
				require.NoError(t, ctx.Err(), "context should not be cancelled")
			}
			if tt.expectInStderr {
				require.Contains(t, stderr.String(), tt.runner)
				require.Contains(t, stderr.String(), "something went wrong")
			} else {
				require.Empty(t, stderr.String())
			}
		})
	}
}

func findFlagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func readFileFromProjectRoot(t *testing.T, file string) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", file))
	require.NoError(t, err)
	return string(b)
}

// testRunOpts creates runOpts for testing.
// This ensures test isolation by using t.TempDir() for all XDG directories.
func testRunOpts(t *testing.T, extProcLauncher func(context.Context, []string, io.Writer) error) *runOpts {
	t.Helper()
	dirs := newTempDirectories(t)
	opts, err := newRunOpts(dirs, "test-run", "", extProcLauncher)
	require.NoError(t, err)
	return opts
}
