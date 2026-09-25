// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplaneenv

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/envoyproxy/ai-gateway/cmd/extproc/mainlib"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/pprof"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
)

// extprocBin holds the path to the compiled extproc binary.
var extprocBin string

func init() {
	var err error
	extprocBin, err = internaltesting.BuildGoBinaryOnDemand("EXTPROC_BIN", "extproc", "./cmd/extproc")
	if err != nil {
		panic(fmt.Sprintf("failed to build extproc binary: %v", err))
	}
}

// TestEnvironment holds all the services needed for tests.
type TestEnvironment struct {
	extprocConfig                                     string
	extprocEnv                                        []string
	extProcPort, extProcAdminPort, extProcMCPPort     int
	envoyConfig                                       string
	envoyListenerPort, envoyAdminPort                 int
	upstreamOut, extprocOut, envoyStdout, envoyStderr internaltesting.OutBuffer
	mcpWriteTimeout                                   time.Duration
	miscPortDefaults, miscPorts                       map[string]int
}

func (e *TestEnvironment) LogOutput(t testing.TB) {
	t.Logf("=== Envoy Stdout ===\n%s", e.envoyStdout.String())
	t.Logf("=== Envoy Stderr ===\n%s", e.envoyStderr.String())
	t.Logf("=== ExtProc Output (stdout + stderr) ===\n%s", e.extprocOut.String())
	t.Logf("=== Upstream Output ===\n%s", e.upstreamOut.String())
	// TODO: dump extproc and envoy metrics.
}

// EnvoyStdout returns the content of Envoy's stdout (e.g. the access log).
func (e *TestEnvironment) EnvoyStdout() string {
	return e.envoyStdout.String()
}

func (e *TestEnvironment) EnvoyListenerPort() int {
	return e.envoyListenerPort
}

func (e *TestEnvironment) ExtProcAdminPort() int {
	return e.extProcAdminPort
}

// StartTestEnvironment starts all required services and returns ports and a closer.
//
// If extProcInProcess is true, then this starts the extproc in-process by directly calling
// mainlib.Main instead of the built binary. This allows the benchmark test suite to directly do the profiling
// without the extroc.
func StartTestEnvironment(t testing.TB,
	requireNewUpstream func(t testing.TB, out io.Writer, miscPorts map[string]int), miscPortDefauls map[string]int,
	extprocConfig string, extprocEnv []string, envoyConfig string, okToDumpLogOnFailure, extProcInProcess bool,
	mcpWriteTimeout time.Duration, extprocArgs ...string,
) *TestEnvironment {
	// Get random ports for all services.
	const defaultPortCount = 5
	ports := internaltesting.RequireRandomPorts(t, defaultPortCount+len(miscPortDefauls))
	miscPorts := make(map[string]int)
	index := 0
	for key := range miscPortDefauls {
		miscPorts[key] = ports[defaultPortCount+index]
		index++
	}

	// Create log buffers that dump only on failure.
	labels := []string{
		"Upstream Output", "ExtProc Output (stdout + stderr)", "Envoy Stdout", "Envoy Stderr",
	}
	var buffers []internaltesting.OutBuffer
	if okToDumpLogOnFailure {
		buffers = internaltesting.DumpLogsOnFail(t, labels...)
	} else {
		buffers = internaltesting.CaptureOutput(labels...)
	}

	env := &TestEnvironment{
		extprocConfig:     extprocConfig,
		extprocEnv:        extprocEnv,
		extProcPort:       ports[0],
		extProcAdminPort:  ports[1],
		extProcMCPPort:    ports[2],
		envoyConfig:       envoyConfig,
		envoyListenerPort: ports[3],
		envoyAdminPort:    ports[4],
		upstreamOut:       buffers[0],
		extprocOut:        buffers[1],
		envoyStdout:       buffers[2],
		envoyStderr:       buffers[3],
		mcpWriteTimeout:   mcpWriteTimeout,
		miscPorts:         miscPorts,
		miscPortDefaults:  miscPortDefauls,
	}

	t.Logf("Starting test environment with ports: extproc=%d, envoyListener=%d, envoyAdmin=%d misc=%v",
		env.extProcPort, env.envoyListenerPort, env.envoyAdminPort, env.miscPorts)

	// The startup order is required: upstream, extProc, then envoy.
	requireNewUpstream(t, env.upstreamOut, env.miscPorts)

	// Replaces ports in extProcConfig.
	replacements := map[string]string{}
	for name, port := range env.miscPorts {
		defaultPort, ok := env.miscPortDefaults[name]
		require.True(t, ok)
		replacements[strconv.Itoa(defaultPort)] = strconv.Itoa(port)
	}
	processedExtProcConfig := replaceTokens(env.extprocConfig, replacements)
	env.extprocConfig = processedExtProcConfig

	// Start ExtProc.
	requireExtProc(t,
		env.extprocOut,
		env.extprocConfig,
		env.extprocEnv,
		extprocArgs,
		env.extProcPort,
		env.extProcAdminPort,
		env.extProcMCPPort,
		env.mcpWriteTimeout,
		extProcInProcess,
	)

	// Start Envoy mapping its testupstream port 8080 to the ephemeral one.
	requireEnvoy(t,
		env.envoyStdout,
		env.envoyStderr,
		env.envoyConfig,
		env.envoyListenerPort,
		env.envoyAdminPort,
		env.extProcPort,
		env.extProcMCPPort,
		env.miscPorts,
		env.miscPortDefaults,
	)

	// Note: Log dumping on failure is handled by DumpLogsOnFail if okToDumpLogOnFailure is true.

	// Sanity-check all connections to ensure everything is up.
	require.Eventually(t, func() bool {
		t.Logf("Checking connections to all services in the test environment")
		err := env.checkAllConnections()
		if err != nil {
			t.Logf("Error checking connections: %v", err)
			return false
		}
		t.Logf("All services are up and running")
		return true
	}, time.Second*60, time.Millisecond*500, "failed to connect to all services in the test environment")
	return env
}

func (e *TestEnvironment) checkAllConnections() error {
	errGroup := &errgroup.Group{}
	errGroup.Go(func() error {
		return e.checkConnection(e.extProcPort, "extProc")
	})
	errGroup.Go(func() error {
		return e.checkConnection(e.extProcAdminPort, "extProcAdmin")
	})
	errGroup.Go(func() error {
		return e.checkConnection(e.envoyListenerPort, "envoyListener")
	})
	errGroup.Go(func() error {
		return e.checkConnection(e.envoyAdminPort, "envoyAdmin")
	})
	for name, port := range e.miscPorts {
		errGroup.Go(func() error {
			return e.checkConnection(port, fmt.Sprintf("misc-%s", name))
		})
	}
	return errGroup.Wait()
}

func (e *TestEnvironment) checkConnection(port int, name string) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to %s on port %d: %w", name, port, err)
	}
	err = conn.Close()
	if err != nil {
		return fmt.Errorf("failed to close connection to %s on port %d: %w", name, port, err)
	}
	return nil
}

// requireEnvoy starts Envoy with the given configuration and ports.
func requireEnvoy(t testing.TB,
	stdout, stderr io.Writer,
	config string,
	listenerPort, adminPort, extProcPort, extProcMCPPort int,
	miscPorts, miscPortDefaults map[string]int,
) {
	// Use specific patterns to avoid breaking cluster names.
	replacements := map[string]string{
		"port_value: 1062": "port_value: " + strconv.Itoa(listenerPort),
		"port_value: 9901": "port_value: " + strconv.Itoa(adminPort),
		"port_value: 1063": "port_value: " + strconv.Itoa(extProcPort),
		"port_value: 9856": "port_value: " + strconv.Itoa(extProcMCPPort),
		// Handle any docker substitutions. These are ignored otherwise.
		"address: extproc":              "address: 127.0.0.1",
		"address: host.docker.internal": "address: 127.0.0.1",
	}
	for name, port := range miscPorts {
		defaultPort, ok := miscPortDefaults[name]
		require.True(t, ok, "missing default port for misc port %q", name)
		replacements["port_value: "+strconv.Itoa(defaultPort)] = "port_value: " + strconv.Itoa(port)
	}

	processedConfig := replaceTokens(config, replacements)

	envoyYamlPath := t.TempDir() + "/envoy.yaml"
	require.NoError(t, os.WriteFile(envoyYamlPath, []byte(processedConfig), 0o600))

	// Note: do not pass t.Context() to CommandContext, as it's canceled
	// *before* t.Cleanup functions are called.
	//
	// > Context returns a context that is canceled just before
	// > Cleanup-registered functions are called.
	//
	// That means the subprocess gets killed before we can send it an interrupt
	// signal for graceful shutdown, which results in orphaned subprocesses.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := testsinternal.GoToolCmdContext(ctx, "func-e", "run",
		"-c", envoyYamlPath,
		"--concurrency", strconv.Itoa(max(runtime.NumCPU(), 2)),
		// This allows multiple Envoy instances to run in parallel.
		"--base-id", strconv.Itoa(time.Now().Nanosecond()),
		"--component-log-level", "http:warn",
	)
	// Point func-e at the same data dir used by aigw so tests reuse the cached Envoy binary.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	cmd.Env = append(os.Environ(),
		"FUNC_E_DATA_HOME="+filepath.Join(home, ".local", "share", "aigw"),
	)
	// func-e will use the version specified in the project root's .envoy-version file.
	cmd.Dir = internaltesting.FindProjectRoot()
	version, err := os.ReadFile(filepath.Join(cmd.Dir, ".envoy-version"))
	require.NoError(t, err)
	t.Logf("Starting Envoy version %s", strings.TrimSpace(string(version)))
	cmd.WaitDelay = 3 * time.Second // auto-kill after 3 seconds.
	t.Cleanup(func() {
		defer cancel()
		// Graceful shutdown, should kill the Envoy subprocess, too.
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Logf("Failed to send interrupt to aigw process: %v", err)
		}
		// Wait for the process to exit gracefully, in worst case this is
		// killed in 3 seconds by WaitDelay above. In that case, you may
		// have a zombie Envoy process left behind!
		if _, err := cmd.Process.Wait(); err != nil {
			t.Logf("Failed to wait for aigw process to exit: %v", err)
		}
	})
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	require.NoError(t, cmd.Start())
}

// requireExtProc starts the external processor with the given configuration.
func requireExtProc(t testing.TB, out io.Writer, config string, env []string, extraArgs []string, port, adminPort, mcpPort int, mcpWriteTimeout time.Duration, inProcess bool) {
	configRaw := []byte(config)
	configBundlePath := filepath.Join(t.TempDir(), "extproc-config-bundle")
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

	args := []string{
		"-configBundlePath", configBundlePath,
		"-extProcAddr", fmt.Sprintf(":%d", port),
		"-adminPort", strconv.Itoa(adminPort),
		"-mcpAddr", ":" + strconv.Itoa(mcpPort),
		"-mcpWriteTimeout", mcpWriteTimeout.String(),
		"-logLevel", "info",
	}
	args = append(args, extraArgs...)
	// Disable pprof for tests to avoid port conflicts.
	env = append(env, fmt.Sprintf("%s=true", pprof.DisableEnvVarKey))
	t.Logf("Starting ExtProc with args: %v", args)
	if inProcess {
		go func() {
			for _, e := range env {
				parts := strings.Split(e, "=")
				if len(parts) != 2 {
					t.Logf("Skipping invalid environ: %s", e)
					continue
				}
				t.Setenv(parts[0], parts[1])
			}
			err := mainlib.Main(t.Context(), args, out)
			if err != nil {
				panic(err)
			}
		}()
	} else {
		cmd := exec.CommandContext(t.Context(), extprocBin)
		cmd.Args = append(cmd.Args, args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdout = out
		cmd.Stderr = out
		require.NoError(t, cmd.Start())
	}
}

// replaceTokens replaces all occurrences of tokens in content with their corresponding values.
func replaceTokens(content string, replacements map[string]string) string {
	result := content
	for token, value := range replacements {
		result = strings.ReplaceAll(result, token, value)
	}
	return result
}
