// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func Test_doMain(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name         string
		args         []string
		env          map[string]string
		rf           runFn
		hf           healthcheckFn
		df           downloadEnvoyFn
		vf           validateFn
		expOut       string
		expPanicCode *int
	}{
		{
			name: "help",
			args: []string{"--help"},
			expOut: `Usage: aigw <command> [flags]

Envoy AI Gateway CLI

Flags:
  -h, --help                  Show context-sensitive help.
      --config-home=STRING    Configuration files directory. Defaults to
                              ~/.config/aigw ($AIGW_CONFIG_HOME)
      --data-home=STRING      Downloaded Envoy binaries directory. Defaults to
                              ~/.local/share/aigw ($AIGW_DATA_HOME)
      --state-home=STRING     Persistent state and logs directory. Defaults to
                              ~/.local/state/aigw ($AIGW_STATE_HOME)
      --runtime-dir=STRING    Ephemeral runtime files directory. Defaults to
                              /tmp/aigw-$UID ($AIGW_RUNTIME_DIR)

Commands:
  version [flags]
    Show version.

  run [<path>] [flags]
    Run the AI Gateway locally for given configuration.

  healthcheck [flags]
    Docker HEALTHCHECK command.

  download-envoy [flags]
    Download Envoy binary for the Envoy Gateway default version.

  validate [<paths> ...] [flags]
    Validate AI Gateway config YAML file(s) against the CRD schemas.

Run "aigw <command> --help" for more information on a command.
`,
			expPanicCode: ptr.To(0),
		},
		{
			name:   "version",
			args:   []string{"version"},
			expOut: "Envoy AI Gateway CLI: dev\n",
		},
		{
			name:         "version help",
			args:         []string{"version", "--help"},
			expPanicCode: ptr.To(0),
			expOut: `Usage: aigw version [flags]

Show version.

Flags:
  -h, --help                  Show context-sensitive help.
      --config-home=STRING    Configuration files directory. Defaults to
                              ~/.config/aigw ($AIGW_CONFIG_HOME)
      --data-home=STRING      Downloaded Envoy binaries directory. Defaults to
                              ~/.local/share/aigw ($AIGW_DATA_HOME)
      --state-home=STRING     Persistent state and logs directory. Defaults to
                              ~/.local/state/aigw ($AIGW_STATE_HOME)
      --runtime-dir=STRING    Ephemeral runtime files directory. Defaults to
                              /tmp/aigw-$UID ($AIGW_RUNTIME_DIR)
`,
		},
		{
			name:         "run no arg",
			args:         []string{"run"},
			rf:           func(context.Context, *cmdRun, *runOpts, io.Writer, io.Writer) error { return nil },
			expPanicCode: ptr.To(80),
		},
		{
			name: "run with OpenAI env",
			args: []string{"run"},
			env:  map[string]string{"OPENAI_API_KEY": "dummy-key"},
			rf:   func(context.Context, *cmdRun, *runOpts, io.Writer, io.Writer) error { return nil },
		},
		{
			name: "run with Anthropic env",
			args: []string{"run"},
			env:  map[string]string{"ANTHROPIC_API_KEY": "dummy-key"},
			rf:   func(context.Context, *cmdRun, *runOpts, io.Writer, io.Writer) error { return nil },
		},
		{
			name: "run help",
			args: []string{"run", "--help"},
			rf:   func(context.Context, *cmdRun, *runOpts, io.Writer, io.Writer) error { return nil },
			expOut: `Usage: aigw run [<path>] [flags]

Run the AI Gateway locally for given configuration.

Arguments:
  [<path>]    Path to the AI Gateway configuration yaml file. Defaults to
              $AIGW_CONFIG_HOME/config.yaml if exists, otherwise optional when
              at least OPENAI_API_KEY, AZURE_OPENAI_API_KEY or ANTHROPIC_API_KEY
              is set.

Flags:
  -h, --help                  Show context-sensitive help.
      --config-home=STRING    Configuration files directory. Defaults to
                              ~/.config/aigw ($AIGW_CONFIG_HOME)
      --data-home=STRING      Downloaded Envoy binaries directory. Defaults to
                              ~/.local/share/aigw ($AIGW_DATA_HOME)
      --state-home=STRING     Persistent state and logs directory. Defaults to
                              ~/.local/state/aigw ($AIGW_STATE_HOME)
      --runtime-dir=STRING    Ephemeral runtime files directory. Defaults to
                              /tmp/aigw-$UID ($AIGW_RUNTIME_DIR)

      --debug                 Enable debug logging emitted to stderr
                              ($AIGW_DEBUG).
      --admin-port=1064       HTTP port for the admin server (serves /metrics
                              and /health endpoints).
      --mcp-config=STRING     Path to MCP servers configuration file.
      --mcp-json=STRING       JSON string of MCP servers configuration.
      --run-id=STRING         Run identifier for this invocation. Defaults to
                              timestamp-based ID or $AIGW_RUN_ID. Use '0' for
                              Docker/Kubernetes ($AIGW_RUN_ID).
      --mcp-session-encryption-iterations=100000
                              Number of iterations for MCP session encryption
                              key derivation.
`,
			expPanicCode: ptr.To(0),
		},
		{
			name: "run with path",
			args: []string{"run", "./path"},
			rf: func(_ context.Context, c *cmdRun, _ *runOpts, _, _ io.Writer) error {
				abs, err := filepath.Abs("./path")
				require.NoError(t, err)
				require.Equal(t, abs, c.Path)
				return nil
			},
		},
		{
			name: "download-envoy",
			args: []string{"download-envoy"},
			df: func(_ context.Context, c *cmdDownloadEnvoy, _, _ io.Writer) error {
				require.NotNil(t, c)
				require.NotEmpty(t, c.dataHome)
				return nil
			},
		},
		{
			name:         "download-envoy help",
			args:         []string{"download-envoy", "--help"},
			expPanicCode: ptr.To(0),
			expOut: `Usage: aigw download-envoy [flags]

Download Envoy binary for the Envoy Gateway default version.

Flags:
  -h, --help                  Show context-sensitive help.
      --config-home=STRING    Configuration files directory. Defaults to
                              ~/.config/aigw ($AIGW_CONFIG_HOME)
      --data-home=STRING      Downloaded Envoy binaries directory. Defaults to
                              ~/.local/share/aigw ($AIGW_DATA_HOME)
      --state-home=STRING     Persistent state and logs directory. Defaults to
                              ~/.local/state/aigw ($AIGW_STATE_HOME)
      --runtime-dir=STRING    Ephemeral runtime files directory. Defaults to
                              /tmp/aigw-$UID ($AIGW_RUNTIME_DIR)
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			out := &bytes.Buffer{}
			if tt.expPanicCode != nil {
				require.PanicsWithValue(t, *tt.expPanicCode, func() {
					doMain(t.Context(), out, os.Stderr, tt.args, func(code int) { panic(code) }, tt.rf, tt.hf, tt.df, tt.vf)
				})
			} else {
				doMain(t.Context(), out, os.Stderr, tt.args, nil, tt.rf, tt.hf, tt.df, tt.vf)
			}
			fmt.Println(out.String())
			require.Equal(t, tt.expOut, out.String())
		})
	}
}

func TestCmd_BeforeApply(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name            string
		configHome      string
		dataHome        string
		stateHome       string
		runtimeDir      string
		envVars         map[string]string
		expectedConfig  string
		expectedData    string
		expectedState   string
		expectedRuntime string
	}{
		{
			name:            "sets defaults when all empty",
			configHome:      "",
			dataHome:        "",
			stateHome:       "",
			runtimeDir:      "",
			envVars:         map[string]string{"HOME": "/home/test", "UID": "1000"},
			expectedConfig:  "/home/test/.config/aigw",
			expectedData:    "/home/test/.local/share/aigw",
			expectedState:   "/home/test/.local/state/aigw",
			expectedRuntime: "/tmp/aigw-1000",
		},
		{
			name:            "preserves explicit values",
			configHome:      "/custom/config",
			dataHome:        "/custom/data",
			stateHome:       "/custom/state",
			runtimeDir:      "/custom/runtime",
			expectedConfig:  "/custom/config",
			expectedData:    "/custom/data",
			expectedState:   "/custom/state",
			expectedRuntime: "/custom/runtime",
		},
		{
			name:            "mixes defaults and explicit values",
			configHome:      "/custom/config",
			dataHome:        "",
			stateHome:       "/custom/state",
			runtimeDir:      "",
			envVars:         map[string]string{"HOME": "/home/test", "UID": "1000"},
			expectedConfig:  "/custom/config",
			expectedData:    "/home/test/.local/share/aigw",
			expectedState:   "/custom/state",
			expectedRuntime: "/tmp/aigw-1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			c := cmd{
				ConfigHome: tt.configHome,
				DataHome:   tt.dataHome,
				StateHome:  tt.stateHome,
				RuntimeDir: tt.runtimeDir,
			}

			err := c.BeforeApply(nil)
			require.NoError(t, err)

			require.Equal(t, tt.expectedConfig, c.ConfigHome)
			require.Equal(t, tt.expectedData, c.DataHome)
			require.Equal(t, tt.expectedState, c.StateHome)
			require.Equal(t, tt.expectedRuntime, c.RuntimeDir)

			// Verify Run.dirs is populated
			require.NotNil(t, c.Run.dirs)
			require.Equal(t, tt.expectedConfig, c.Run.dirs.ConfigHome)
			require.Equal(t, tt.expectedData, c.Run.dirs.DataHome)
			require.Equal(t, tt.expectedState, c.Run.dirs.StateHome)
			require.Equal(t, tt.expectedRuntime, c.Run.dirs.RuntimeDir)
		})
	}
}

func TestCmdRun_BeforeApply(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		runID        string
		setupDirs    func(t *testing.T, configHome string)
		expectedPath string
		expectedID   string // empty means check it's generated
	}{
		{
			name:         "generates runID when empty",
			path:         "",
			runID:        "",
			expectedPath: "",
			expectedID:   "", // will verify it's non-empty
		},
		{
			name:         "preserves explicit runID",
			path:         "",
			runID:        "my-custom-id",
			expectedPath: "",
			expectedID:   "my-custom-id",
		},
		{
			name:         "preserves explicit path",
			path:         "/explicit/config.yaml",
			runID:        "",
			expectedPath: "/explicit/config.yaml",
			expectedID:   "",
		},
		{
			name:  "sets path to default when config.yaml exists",
			path:  "",
			runID: "",
			setupDirs: func(t *testing.T, configHome string) {
				err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("test"), 0o600)
				require.NoError(t, err)
			},
			expectedPath: "", // will be {configHome}/config.yaml
			expectedID:   "",
		},
		{
			name:         "leaves path empty when config.yaml does not exist",
			path:         "",
			runID:        "",
			expectedPath: "",
			expectedID:   "",
		},
		{
			name:  "preserves explicit path even when config.yaml exists",
			path:  "/explicit/config.yaml",
			runID: "",
			setupDirs: func(t *testing.T, configHome string) {
				err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("test"), 0o600)
				require.NoError(t, err)
			},
			expectedPath: "/explicit/config.yaml",
			expectedID:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configHome := t.TempDir()

			if tt.setupDirs != nil {
				tt.setupDirs(t, configHome)
			}

			dirs := newTempDirectories(t)
			dirs.ConfigHome = configHome

			c := cmdRun{
				Path:  tt.path,
				RunID: tt.runID,
				dirs:  dirs,
			}

			err := c.BeforeApply(nil)
			require.NoError(t, err)

			// Check Path
			if tt.expectedPath == "" && tt.path == "" && tt.setupDirs != nil {
				// Special case: should be set to default
				expected := filepath.Join(configHome, "config.yaml")
				require.Equal(t, expected, c.Path)
			} else {
				require.Equal(t, tt.expectedPath, c.Path)
			}

			// Check RunID
			if tt.expectedID == "" && tt.runID == "" {
				// Should be generated
				require.NotEmpty(t, c.RunID)
				// Verify format: YYYYMMDD_HHMMSS_UUU
				require.Regexp(t, `^\d{8}_\d{6}_\d{3}$`, c.RunID)
			} else {
				require.Equal(t, tt.expectedID, c.RunID)
			}
		})
	}
}

func TestCmdRun_Validate(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name          string
		path          string
		envVars       map[string]string
		expectedError string
	}{
		{
			name:          "no config and no env vars",
			path:          "",
			envVars:       map[string]string{},
			expectedError: "you must supply at least OPENAI_API_KEY, AZURE_OPENAI_API_KEY, ANTHROPIC_API_KEY, or a config file path",
		},
		{
			name:    "config path provided",
			path:    "/path/to/config.yaml",
			envVars: map[string]string{},
		},
		{
			name: "OPENAI_API_KEY set",
			path: "",
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test",
			},
		},
		{
			name: "AZURE_OPENAI_API_KEY set",
			path: "",
			envVars: map[string]string{
				"AZURE_OPENAI_API_KEY": "azure-key",
			},
		},
		{
			name: "both API keys set",
			path: "",
			envVars: map[string]string{
				"OPENAI_API_KEY":       "sk-test",
				"AZURE_OPENAI_API_KEY": "azure-key",
			},
		},
		{
			name: "ANTHROPIC_API_KEY set",
			path: "",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
			},
		},
		{
			name: "config path and OPENAI_API_KEY both set",
			path: "/path/to/config.yaml",
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test",
			},
		},
		{
			name: "config path and AZURE_OPENAI_API_KEY both set",
			path: "/path/to/config.yaml",
			envVars: map[string]string{
				"AZURE_OPENAI_API_KEY": "azure-key",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			cmd := cmdRun{
				Path:  tt.path,
				RunID: "test-run-id",
				dirs:  newTempDirectories(t),
			}

			err := cmd.Validate()

			if tt.expectedError != "" {
				require.EqualError(t, err, tt.expectedError)
				require.Nil(t, cmd.runOpts)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cmd.runOpts)
				require.Equal(t, tt.path, cmd.runOpts.configPath)
				require.Equal(t, "test-run-id", cmd.runOpts.runID)
			}
		})
	}
}
