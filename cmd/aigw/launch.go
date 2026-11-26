// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/xdg"
)

type (
	// cmdAgent corresponds to the `aigw claude` and `aigw codex` commands.
	cmdAgent struct {
		Setup bool     `help:"Choose again how the agent reaches its models."`
		Args  []string `arg:"" optional:"" passthrough:"all" help:"Arguments passed to the agent."`
	}
	// cmdStop corresponds to the `aigw stop` command.
	cmdStop struct{}
	// cmdStatus corresponds to the `aigw status` command.
	cmdStatus struct{}
)

// Variables so that tests can replace them.
var (
	// agentGatewayStartTimeout bounds the gateway startup. The first run downloads Envoy.
	agentGatewayStartTimeout = 5 * time.Minute
	// agentGatewayStopTimeout bounds the graceful shutdown of the gateway.
	agentGatewayStopTimeout = 15 * time.Second
	// agentGatewayAddr is the address the gateway listens on.
	agentGatewayAddr = "localhost:1975"
	// startAgentGateway starts the gateway process for the given configuration.
	startAgentGateway = spawnAgentGateway
)

// errAgentExit carries the exit code of the agent.
type errAgentExit struct{ code int }

func (e errAgentExit) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// agentState is the gateway started for the coding agents.
type agentState struct {
	// Pid of the "aigw run" process.
	Pid int `json:"pid"`
	// Started is when it was started.
	Started time.Time `json:"started"`
	// Agents are the settings of each agent the gateway routes.
	Agents map[string]agentSettings `json:"agents"`
}

// agentPaths are the files of the gateway started for the coding agents.
type agentPaths struct {
	dir, config, state, log string
}

func newAgentPaths(dirs *xdg.Directories) agentPaths {
	dir := filepath.Join(dirs.StateHome, "agents")
	return agentPaths{
		dir:    dir,
		config: filepath.Join(dir, "config.yaml"),
		state:  filepath.Join(dir, "gateway.json"),
		log:    filepath.Join(dir, "gateway.log"),
	}
}

// running returns the state of the gateway, or nil if it is not running.
func (p agentPaths) running() *agentState {
	raw, err := os.ReadFile(p.state)
	if err != nil {
		return nil
	}
	var s agentState
	if err = json.Unmarshal(raw, &s); err != nil || !processAlive(s.Pid) {
		return nil
	}
	return &s
}

func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// portInUse reports whether something listens on addr.
func portInUse(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// runAgent runs the coding agent through the gateway, starting it if needed.
func runAgent(ctx context.Context, a *agentSpec, c *cmdAgent, dirs *xdg.Directories, stderr io.Writer) error {
	binary, err := exec.LookPath(a.binary)
	if err != nil {
		return fmt.Errorf("%s is not installed (%q is not in PATH). Install it from %s", a.title, a.binary, a.install)
	}

	settingsPath := agentsConfigPath(dirs.ConfigHome)
	settings, err := loadAgentSettings(settingsPath)
	if err != nil {
		return err
	}
	ui := &terminalUI{in: os.Stdin, out: stderr, styled: os.Getenv("NO_COLOR") == ""}
	keys := map[string]string{}
	current, configured := settings[a.name]
	if !configured || c.Setup {
		var prev *agentSettings
		if configured {
			prev = &current
		}
		if current, keys, err = promptAgentSetup(ui, a, prev); err != nil {
			return err
		}
		settings[a.name] = current
		if err = saveAgentSettings(settingsPath, settings); err != nil {
			return err
		}
		printAgentSummary(ui, a, current, keys, settingsPath)
	}
	if missing := a.missingKey(current, keys); missing != "" {
		return fmt.Errorf("%s is not set: %s is configured to use %s.\nExport it, or run \"aigw %s --setup\" to choose again",
			missing, a.title, a.describe(current), a.name)
	}

	paths := newAgentPaths(dirs)
	state := paths.running()
	switch {
	case state != nil && state.Agents[a.name] == current:
		ui.printf("%s Agent Router running on %s · %s → %s\n", ui.cyan("✓"), agentGatewayURL, a.title, a.describe(current))
	default:
		if state == nil && portInUse(agentGatewayAddr) {
			return fmt.Errorf("%s is already in use, probably by \"aigw run\". Stop it first: "+
				"\"aigw %s\" starts its own Agent Router", agentGatewayAddr, a.name)
		}
		// Route every configured agent whose API key is available, so other open agents keep working.
		routed := map[string]agentSettings{a.name: current}
		var dropped []string
		for _, name := range slices.Sorted(maps.Keys(settings)) {
			other, ok := agents[name]
			if !ok || name == a.name {
				continue
			}
			if other.missingKey(settings[name], keys) == "" {
				routed[name] = settings[name]
			} else if state != nil && state.Agents[name] != (agentSettings{}) {
				dropped = append(dropped, other.title)
			}
		}
		if state != nil {
			ui.printf("%s\n", ui.dim("Restarting Agent Router to apply the "+a.title+" settings: other open agents pause for a few seconds."))
			if err = stopAgentGateway(paths, state); err != nil {
				return err
			}
		}
		for _, title := range dropped {
			ui.printf("%s\n", ui.sgr("33", title+" is no longer routed: its API key is not set in this terminal. Run it again from a terminal that has it."))
		}
		start := time.Now()
		ui.printf("%s", ui.dim("Starting Agent Router… "))
		if err = startAgentGateway(ctx, paths, dirs, routed, keys); err != nil {
			ui.printf("\n")
			return err
		}
		ui.printf("\r\x1b[K%s Agent Router started on %s (%v) · logs: %s\n", ui.cyan("✓"), agentGatewayURL,
			time.Since(start).Round(100*time.Millisecond), paths.log)
	}

	args, env, err := a.command(current, c.Args, os.Environ())
	if err != nil {
		return err
	}
	ui.printf("%s\n", ui.dim("Starting "+a.binary+"…"))
	code, err := runChild(ctx, binary, args, env)
	ui.printf("\n%s\n", ui.bold("Agent Router is still running at "+agentGatewayURL))
	ui.printf("  logs: %s\n", paths.log)
	ui.printf("  stop it with: %s\n", ui.cyan("aigw stop"))
	if err != nil {
		return err
	}
	if code != 0 {
		return errAgentExit{code: code}
	}
	return nil
}

// promptAgentSetup runs the agent setup in raw terminal mode.
func promptAgentSetup(ui *terminalUI, a *agentSpec, current *agentSettings) (agentSettings, map[string]string, error) {
	restore, ok, err := makeRaw(os.Stdin)
	if err != nil {
		return agentSettings{}, nil, err
	}
	defer restore()
	if !ok {
		return agentSettings{}, nil, fmt.Errorf("%s is not configured yet: run \"aigw %s\" from a terminal to set it up", a.title, a.name)
	}
	return runAgentSetup(ui, a, current)
}

// runChild runs the agent in the foreground, owning the terminal, and returns its exit code.
// Ctrl+C belongs to the agent: the terminal sends it to the agent as well, so it is ignored
// here. Termination signals sent only to this process are forwarded to the agent.
func runChild(ctx context.Context, binary string, args, env []string) (int, error) {
	cmd := exec.Command(binary, args...) //nolint:gosec // runs the agent the user asked for.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env

	// Take over the signals from the default handler, which exits on the second Ctrl+C.
	signal.Reset(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start %s: %w", binary, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode(), nil
			}
			return 0, err
		case sig := <-sigs:
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
				_ = cmd.Process.Signal(sig)
			}
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			ctx = context.Background() // Wait for the agent to exit.
		}
	}
}

// writeAgentGatewayConfig renders the configuration routing each agent to its provider.
func writeAgentGatewayConfig(path string, routed map[string]agentSettings) error {
	var data autoconfig.ConfigData
	for _, name := range slices.Sorted(maps.Keys(routed)) {
		if err := agents[name].addProvider(&data, routed[name]); err != nil {
			return err
		}
	}
	config, err := autoconfig.WriteConfig(&data)
	if err != nil {
		return err
	}
	header := "# Agent Router configuration for \"aigw claude\" and \"aigw codex\", generated from agents.yaml.\n" +
		"# It is regenerated on every start: run \"aigw <agent> --setup\" to change it.\n"
	return os.WriteFile(path, []byte(header+config), 0o600)
}

// spawnAgentGateway starts "aigw run" in the background, detached from the terminal so that
// it outlives the agent, and waits until it serves requests.
func spawnAgentGateway(ctx context.Context, paths agentPaths, dirs *xdg.Directories, routed map[string]agentSettings, keys map[string]string) error {
	if err := os.MkdirAll(paths.dir, 0o750); err != nil {
		return fmt.Errorf("failed to create %s: %w", paths.dir, err)
	}
	if err := writeAgentGatewayConfig(paths.config, routed); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find the aigw executable: %w", err)
	}
	logFile, err := os.Create(paths.log)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", paths.log, err)
	}
	defer logFile.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(self, "run", paths.config) //nolint:gosec // runs this same binary.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, logFile, logFile
	cmd.Env = os.Environ()
	for k, v := range map[string]string{
		"AIGW_CONFIG_HOME": dirs.ConfigHome, "AIGW_DATA_HOME": dirs.DataHome,
		"AIGW_STATE_HOME": dirs.StateHome, "AIGW_RUNTIME_DIR": dirs.RuntimeDir,
	} {
		cmd.Env = setEnv(cmd.Env, k, v)
	}
	for k, v := range keys {
		cmd.Env = setEnv(cmd.Env, k, v)
	}
	// A new session: Ctrl+C and closing the terminal never reach the gateway.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Agent Router: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	state, err := json.Marshal(agentState{Pid: cmd.Process.Pid, Started: time.Now(), Agents: routed})
	if err != nil {
		return err
	}
	if err = os.WriteFile(paths.state, state, 0o600); err != nil {
		return fmt.Errorf("failed to save %s: %w", paths.state, err)
	}

	ctx, cancel := context.WithTimeout(ctx, agentGatewayStartTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-exited:
			_ = os.Remove(paths.state)
			return fmt.Errorf("agent Router failed to start:\n%s", tail(paths.log, 20))
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = os.Remove(paths.state)
			return fmt.Errorf("agent Router did not start in %v, see %s", agentGatewayStartTimeout, paths.log)
		case <-ticker.C:
			if gatewayReady(ctx) {
				// The gateway runs on its own: stop waiting for it.
				_ = cmd.Process.Release()
				return nil
			}
		}
	}
}

// gatewayReady reports whether the gateway serves requests.
func gatewayReady(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+agentGatewayAddr+"/v1/models", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// tail returns the last n lines of a file.
func tail(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// stopAgentGateway stops the gateway gracefully, or kills it after agentGatewayStopTimeout.
func stopAgentGateway(paths agentPaths, state *agentState) error {
	if err := syscall.Kill(state.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("failed to stop Agent Router (pid %d): %w", state.Pid, err)
	}
	deadline := time.Now().Add(agentGatewayStopTimeout)
	for processAlive(state.Pid) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(state.Pid) {
		// Kill the whole session, including Envoy.
		_ = syscall.Kill(-state.Pid, syscall.SIGKILL)
	}
	// Wait for the ports to be released before starting again.
	for portInUse(agentGatewayAddr) && time.Now().Before(deadline.Add(5*time.Second)) {
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(paths.state)
	return nil
}

// stopAgents implements "aigw stop".
func stopAgents(dirs *xdg.Directories, stdout io.Writer) error {
	paths := newAgentPaths(dirs)
	state := paths.running()
	if state == nil {
		_ = os.Remove(paths.state)
		_, _ = fmt.Fprintln(stdout, "Agent Router is not running.")
		return nil
	}
	if err := stopAgentGateway(paths, state); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "✓ Agent Router stopped.")
	return nil
}

// agentsStatus implements "aigw status".
func agentsStatus(dirs *xdg.Directories, stdout io.Writer) error {
	paths := newAgentPaths(dirs)
	state := paths.running()
	if state == nil {
		_, _ = fmt.Fprintln(stdout, "Agent Router is not running. \"aigw claude\" or \"aigw codex\" start it.")
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "Agent Router is running at %s (pid %s, up %v)\n", agentGatewayURL,
		strconv.Itoa(state.Pid), time.Since(state.Started).Round(time.Second))
	for _, name := range slices.Sorted(maps.Keys(state.Agents)) {
		if a, ok := agents[name]; ok {
			_, _ = fmt.Fprintf(stdout, "  %-12s → %s\n", a.title, a.describe(state.Agents[name]))
		}
	}
	_, _ = fmt.Fprintf(stdout, "  logs: %s\n", paths.log)
	return nil
}
