// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/xdg"
)

func testDirs(t *testing.T) *xdg.Directories {
	dir := t.TempDir()
	return &xdg.Directories{
		ConfigHome: filepath.Join(dir, "config"), DataHome: filepath.Join(dir, "data"),
		StateHome: filepath.Join(dir, "state"), RuntimeDir: filepath.Join(dir, "run"),
	}
}

// fakeAgent puts an executable named binary in PATH that exits with the given code.
func fakeAgent(t *testing.T, binary string, code int) {
	dir := t.TempDir()
	script := "#!/bin/sh\nexit " + strconv.Itoa(code) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, binary), []byte(script), 0o700)) //nolint:gosec // an executable script.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// useFreeAddr points the gateway address to a free port, so tests don't depend on 1975.
func useFreeAddr(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	prev := agentGatewayAddr
	agentGatewayAddr = addr
	t.Cleanup(func() { agentGatewayAddr = prev })
}

func fakeStart(t *testing.T, calls *[]map[string]agentSettings) {
	prev := startAgentGateway
	startAgentGateway = func(_ context.Context, paths agentPaths, _ *xdg.Directories, routed map[string]agentSettings, _ map[string]string) error {
		*calls = append(*calls, routed)
		require.NoError(t, os.MkdirAll(paths.dir, 0o750))
		// This process plays the gateway, so it is alive.
		state, err := json.Marshal(agentState{Pid: os.Getpid(), Started: time.Now(), Agents: routed})
		require.NoError(t, err)
		return os.WriteFile(paths.state, state, 0o600)
	}
	t.Cleanup(func() { startAgentGateway = prev })
}

func TestRunAgent(t *testing.T) {
	useFreeAddr(t)
	dirs := testDirs(t)
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv(tetrateKeyEnv, "")
	var calls []map[string]agentSettings
	fakeStart(t, &calls)

	t.Run("not installed", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		err := runAgent(t.Context(), &claudeAgent, &cmdAgent{}, dirs, &bytes.Buffer{})
		require.ErrorContains(t, err, "Claude Code is not installed")
	})

	fakeAgent(t, "claude", 0)
	fakeAgent(t, "codex", 3)

	t.Run("not configured without a terminal", func(t *testing.T) {
		err := runAgent(t.Context(), &claudeAgent, &cmdAgent{}, dirs, &bytes.Buffer{})
		require.ErrorContains(t, err, "not configured yet")
	})

	require.NoError(t, saveAgentSettings(agentsConfigPath(dirs.ConfigHome), map[string]agentSettings{
		"claude": {Mode: "subscription"},
		"codex":  {Mode: "openai-api-key"},
	}))

	t.Run("starts the gateway and leaves it running", func(t *testing.T) {
		out := &bytes.Buffer{}
		require.NoError(t, runAgent(t.Context(), &claudeAgent, &cmdAgent{}, dirs, out))
		require.Len(t, calls, 1)
		require.Equal(t, map[string]agentSettings{"claude": {Mode: "subscription"}, "codex": {Mode: "openai-api-key"}}, calls[0])
		require.Contains(t, out.String(), "Agent Router started on")
		require.Contains(t, out.String(), "Agent Router is still running at")
		require.Contains(t, out.String(), "aigw stop")
	})

	t.Run("joins the running gateway and returns the agent exit code", func(t *testing.T) {
		out := &bytes.Buffer{}
		err := runAgent(t.Context(), &codexAgent, &cmdAgent{}, dirs, out)
		require.Equal(t, errAgentExit{code: 3}, err)
		require.Len(t, calls, 1)
		require.Contains(t, out.String(), "Agent Router running on")
	})

	t.Run("missing key", func(t *testing.T) {
		require.NoError(t, saveAgentSettings(agentsConfigPath(dirs.ConfigHome), map[string]agentSettings{
			"claude": {Mode: "tetrate-managed"},
		}))
		err := runAgent(t.Context(), &claudeAgent, &cmdAgent{}, dirs, &bytes.Buffer{})
		require.ErrorContains(t, err, "TARS_API_KEY is not set")
	})

	t.Run("port in use by another gateway", func(t *testing.T) {
		require.NoError(t, saveAgentSettings(agentsConfigPath(dirs.ConfigHome), map[string]agentSettings{
			"claude": {Mode: "subscription"},
		}))
		require.NoError(t, os.Remove(newAgentPaths(dirs).state))
		l, err := net.Listen("tcp", agentGatewayAddr)
		require.NoError(t, err)
		defer l.Close()
		err = runAgent(t.Context(), &claudeAgent, &cmdAgent{}, dirs, &bytes.Buffer{})
		require.ErrorContains(t, err, "already in use")
	})
}

func TestStopAndStatus(t *testing.T) {
	useFreeAddr(t)
	dirs := testDirs(t)
	paths := newAgentPaths(dirs)

	out := &bytes.Buffer{}
	require.NoError(t, agentsStatus(dirs, out))
	require.Contains(t, out.String(), "not running")
	out.Reset()
	require.NoError(t, stopAgents(dirs, out))
	require.Contains(t, out.String(), "not running")

	// A stale state, whose process is gone, is not running.
	require.NoError(t, os.MkdirAll(paths.dir, 0o750))
	stale := exec.Command("true")
	require.NoError(t, stale.Run())
	state, err := json.Marshal(agentState{Pid: stale.Process.Pid, Agents: map[string]agentSettings{"claude": {Mode: "subscription"}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(paths.state, state, 0o600))
	require.Nil(t, paths.running())

	// A running gateway is reported, and stopped.
	gw := exec.Command("sleep", "60")
	require.NoError(t, gw.Start())
	go func() { _ = gw.Wait() }()
	state, err = json.Marshal(agentState{Pid: gw.Process.Pid, Started: time.Now(), Agents: map[string]agentSettings{"claude": {Mode: "subscription"}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(paths.state, state, 0o600))

	out.Reset()
	require.NoError(t, agentsStatus(dirs, out))
	require.Contains(t, out.String(), "Agent Router is running at")
	require.Contains(t, out.String(), "Claude Code  → Claude subscription (api.anthropic.com)")

	out.Reset()
	require.NoError(t, stopAgents(dirs, out))
	require.Contains(t, out.String(), "Agent Router stopped.")
	require.Eventually(t, func() bool { return !processAlive(gw.Process.Pid) }, 5*time.Second, 50*time.Millisecond)
	_, err = os.Stat(paths.state)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	require.NoError(t, os.WriteFile(path, []byte("a\nb\nc\n"), 0o600))
	require.Equal(t, "b\nc", tail(path, 2))
	require.Equal(t, "a\nb\nc", tail(path, 10))
	require.Empty(t, tail(filepath.Join(t.TempDir(), "missing"), 2))
}
