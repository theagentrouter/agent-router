// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/alecthomas/kong"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/envoyproxy/ai-gateway/cmd/extproc/mainlib"
	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/internal/xdg"
)

type (
	// cmd corresponds to the top-level `aigw` command.
	cmd struct {
		// Global XDG flags
		ConfigHome string `name:"config-home" env:"AIGW_CONFIG_HOME" help:"Configuration files directory. Defaults to ~/.config/aigw" type:"path"`
		DataHome   string `name:"data-home" env:"AIGW_DATA_HOME" help:"Downloaded Envoy binaries directory. Defaults to ~/.local/share/aigw" type:"path"`
		StateHome  string `name:"state-home" env:"AIGW_STATE_HOME" help:"Persistent state and logs directory. Defaults to ~/.local/state/aigw" type:"path"`
		RuntimeDir string `name:"runtime-dir" env:"AIGW_RUNTIME_DIR" help:"Ephemeral runtime files directory. Defaults to /tmp/aigw-$UID" type:"path"`

		// Version is the sub-command to show the version.
		Version struct{} `cmd:"" help:"Show version."`
		// Run is the sub-command parsed by the `cmdRun` struct.
		Run cmdRun `cmd:"" help:"Run the AI Gateway locally for given configuration."`
		// Healthcheck is the sub-command to check if the aigw server is healthy.
		Healthcheck cmdHealthcheck `cmd:"" help:"Docker HEALTHCHECK command."`
		// DownloadEnvoy downloads the Envoy binary used by Envoy Gateway.
		DownloadEnvoy cmdDownloadEnvoy `cmd:"" help:"Download Envoy binary for the Envoy Gateway default version."`
		// Validate is the sub-command to validate AI Gateway config YAML files.
		Validate cmdValidate `cmd:"" help:"Validate AI Gateway config YAML file(s) against the CRD schemas."`
	}
	// cmdRun corresponds to `aigw run` command.
	cmdRun struct {
		Debug     bool   `env:"AIGW_DEBUG" help:"Enable debug logging emitted to stderr."`
		Path      string `arg:"" name:"path" optional:"" help:"Path to the AI Gateway configuration yaml file. Defaults to $AIGW_CONFIG_HOME/config.yaml if exists, otherwise optional when at least OPENAI_API_KEY, AZURE_OPENAI_API_KEY or ANTHROPIC_API_KEY is set." type:"path"`
		AdminPort int    `help:"HTTP port for the admin server (serves /metrics and /health endpoints)." default:"1064"`
		McpConfig string `name:"mcp-config" help:"Path to MCP servers configuration file." type:"path"`
		McpJSON   string `name:"mcp-json" help:"JSON string of MCP servers configuration."`
		RunID     string `name:"run-id" env:"AIGW_RUN_ID" help:"Run identifier for this invocation. Defaults to timestamp-based ID or $AIGW_RUN_ID. Use '0' for Docker/Kubernetes."`

		MCPSessionEncryptionIterations int `name:"mcp-session-encryption-iterations" help:"Number of iterations for MCP session encryption key derivation." default:"100000"`

		mcpConfig *autoconfig.MCPServers `kong:"-"` // Internal field: normalized MCP JSON data
		dirs      *xdg.Directories       `kong:"-"` // Internal field: XDG directories, set by BeforeApply
		runOpts   *runOpts               `kong:"-"` // Internal field: run options, set by Validate
	}
	// cmdHealthcheck corresponds to `aigw healthcheck` command.
	cmdHealthcheck struct{}
	// cmdDownloadEnvoy corresponds to `aigw download-envoy` command.
	cmdDownloadEnvoy struct {
		dataHome string `kong:"-"`
	}
)

// BeforeApply is called by Kong before applying defaults to set XDG directory defaults.
func (c *cmd) BeforeApply(_ *kong.Context) error {
	// Expand paths unconditionally (handles ~/, env vars, and converts to absolute)
	// Set defaults only if not set (empty string)
	if c.ConfigHome == "" {
		c.ConfigHome = "~/.config/aigw"
	}
	c.ConfigHome = expandPath(c.ConfigHome)

	if c.DataHome == "" {
		c.DataHome = "~/.local/share/aigw"
	}
	c.DataHome = expandPath(c.DataHome)

	if c.StateHome == "" {
		c.StateHome = "~/.local/state/aigw"
	}
	c.StateHome = expandPath(c.StateHome)

	if c.RuntimeDir == "" {
		c.RuntimeDir = "/tmp/aigw-${UID}"
	}
	c.RuntimeDir = expandPath(c.RuntimeDir)

	// Populate Run.dirs with expanded XDG directories for use in Run.BeforeApply
	c.Run.dirs = &xdg.Directories{
		ConfigHome: c.ConfigHome,
		DataHome:   c.DataHome,
		StateHome:  c.StateHome,
		RuntimeDir: c.RuntimeDir,
	}
	// Populate DownloadEnvoy dataHome with expanded data directory.
	c.DownloadEnvoy.dataHome = c.DataHome

	return nil
}

// BeforeApply is called by Kong before applying defaults to set computed default values.
func (c *cmdRun) BeforeApply(_ *kong.Context) error {
	// Set RunID default if not provided
	if c.RunID == "" {
		c.RunID = generateRunID(time.Now())
	}

	// Set Path to default config.yaml if it exists and Path not provided
	if c.Path == "" && c.dirs != nil {
		defaultPath := c.dirs.ConfigHome + "/config.yaml"
		if _, err := os.Stat(defaultPath); err == nil {
			c.Path = defaultPath
		}
	}
	// Expand Path (handles ~/, env vars, and converts to absolute)
	c.Path = expandPath(c.Path)

	return nil
}

// Validate is called by Kong after parsing to validate the cmdRun arguments.
func (c *cmdRun) Validate() error {
	if c.McpConfig != "" && c.McpJSON != "" {
		return fmt.Errorf("mcp-config and mcp-json are mutually exclusive")
	}
	if c.Path == "" && os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("AZURE_OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" && c.McpConfig == "" && c.McpJSON == "" {
		return fmt.Errorf("you must supply at least OPENAI_API_KEY, AZURE_OPENAI_API_KEY, ANTHROPIC_API_KEY, or a config file path")
	}

	c.McpConfig = expandPath(c.McpConfig)

	var mcpJSON string
	if c.McpConfig != "" {
		raw, err := os.ReadFile(c.McpConfig)
		if err != nil {
			return fmt.Errorf("failed to read MCP config file: %w", err)
		}
		mcpJSON = string(raw)
	} else {
		mcpJSON = c.McpJSON
	}

	if mcpJSON != "" {
		var mcpConfig autoconfig.MCPServers
		if err := json.Unmarshal([]byte(mcpJSON), &mcpConfig); err != nil {
			return fmt.Errorf("failed to unmarshal MCP config: %w", err)
		}
		c.mcpConfig = &mcpConfig
	}

	opts, err := newRunOpts(c.dirs, c.RunID, c.Path, mainlib.Main)
	if err != nil {
		return fmt.Errorf("failed to create run options: %w", err)
	}
	c.runOpts = opts

	return nil
}

type (
	runFn           func(context.Context, *cmdRun, *runOpts, io.Writer, io.Writer) error
	healthcheckFn   func(context.Context, io.Writer, io.Writer) error
	downloadEnvoyFn func(context.Context, *cmdDownloadEnvoy, io.Writer, io.Writer) error
)

func main() {
	doMain(ctrl.SetupSignalHandler(), os.Stdout, os.Stderr, os.Args[1:], os.Exit, run, healthcheck, downloadEnvoyCmd, validateCmd)
}

// doMain is the main entry point for the CLI. It parses the command line arguments and executes the appropriate command.
//
//   - stdout is the writer to use for standard output. Mainly for testing.
//   - stderr is the writer to use for standard error. Mainly for testing.
//   - `args` are the command line arguments without the program name.
//   - exitFn is the function to call to exit the program during the parsing of the command line arguments. Mainly for testing.
//   - rf is the function to call to run the AI Gateway locally. Mainly for testing.
func doMain(ctx context.Context, stdout, stderr io.Writer, args []string, exitFn func(int),
	rf runFn,
	hf healthcheckFn,
	df downloadEnvoyFn,
	vf validateFn,
) {
	var c cmd
	parser, err := kong.New(&c,
		kong.Name("aigw"),
		kong.Description("Envoy AI Gateway CLI"),
		kong.Writers(stdout, stderr),
		kong.Exit(exitFn),
	)
	if err != nil {
		log.Fatalf("Error creating parser: %v", err)
	}
	parsed, err := parser.Parse(args)
	parser.FatalIfErrorf(err)

	switch parsed.Command() {
	case "version":
		_, _ = fmt.Fprintf(stdout, "Envoy AI Gateway CLI: %s\n", version.Parse())
	case "run", "run <path>":
		err = rf(ctx, &c.Run, c.Run.runOpts, stdout, stderr)
		if err != nil {
			log.Fatalf("Error running: %v", err)
		}
	case "healthcheck":
		err = hf(ctx, stdout, stderr)
		if err != nil {
			log.Fatalf("Health check failed: %v", err)
		}
	case "download-envoy":
		err = df(ctx, &c.DownloadEnvoy, stdout, stderr)
		if err != nil {
			log.Fatalf("Download Envoy failed: %v", err)
		}
	case "validate", "validate <paths>":
		err = vf(ctx, &c.Validate, stdout, stderr)
		if err != nil {
			log.Fatalf("Validation failed: %v", err)
		}
	default:
		panic("unreachable")
	}
}

// generateRunID generates a unique run identifier based on the current time.
// Defaults to the same convention as func-e: "YYYYMMDD_HHMMSS_UUU" format.
// Last 3 digits of microseconds to allow concurrent runs.
func generateRunID(now time.Time) string {
	micro := now.Nanosecond() / 1000 % 1000
	return fmt.Sprintf("%s_%03d", now.Format("20060102_150405"), micro)
}
