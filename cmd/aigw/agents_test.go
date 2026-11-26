// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
)

func TestRunAgentSetup(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(tetrateKeyEnv, "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "")

	tests := []struct {
		name    string
		agent   *agentSpec
		current *agentSettings
		input   string
		env     map[string]string
		exp     agentSettings
		expKeys map[string]string
		expOut  []string
	}{
		{
			name: "claude subscription is the default", agent: &claudeAgent, input: "\r",
			exp: agentSettings{Mode: "subscription"}, expKeys: map[string]string{},
			expOut: []string{"First run: choose how Claude Code reaches Claude", "(•) Claude subscription"},
		},
		{
			name: "anthropic api key", agent: &claudeAgent, input: "j\rsk-ant\r",
			exp: agentSettings{Mode: "anthropic-api-key"}, expKeys: map[string]string{"ANTHROPIC_API_KEY": "sk-ant"},
			expOut: []string{"console.anthropic.com", "(6 characters)"},
		},
		{
			name: "anthropic api key from the environment", agent: &claudeAgent, input: "j\r",
			env: map[string]string{"ANTHROPIC_API_KEY": "sk-env"},
			exp: agentSettings{Mode: "anthropic-api-key"}, expKeys: map[string]string{},
			expOut: []string{"Using ANTHROPIC_API_KEY from the environment."},
		},
		{
			name: "tetrate passthrough with a custom url", agent: &claudeAgent,
			input: keyDown + keyDown + "\rhttps://router.example.com/\r\rtars\r",
			exp:   agentSettings{Mode: "tetrate-passthrough", URL: "https://router.example.com"}, expKeys: map[string]string{tetrateKeyEnv: "tars"},
			expOut: []string{"How should Claude Code sign in to Claude?", "(•) My Claude subscription"},
		},
		{
			name: "tetrate managed with the default url", agent: &claudeAgent,
			input: "jj\r\rj\rtars\r",
			exp:   agentSettings{Mode: "tetrate-managed"}, expKeys: map[string]string{tetrateKeyEnv: "tars"},
			expOut: []string{"(•) Tetrate Agent Router's Anthropic key  Managed"},
		},
		{
			name: "setup again preselects the current mode", agent: &claudeAgent,
			current: &agentSettings{Mode: "tetrate-managed", URL: "https://router.example.com"},
			input:   "\r\r\r",
			env:     map[string]string{tetrateKeyEnv: "tars"},
			exp:     agentSettings{Mode: "tetrate-managed", URL: "https://router.example.com"}, expKeys: map[string]string{},
			expOut: []string{"Currently: Tetrate Agent Router, managed (router.example.com)"},
		},
		{
			name: "codex openai", agent: &codexAgent, input: "\rsk-openai\r",
			exp: agentSettings{Mode: "openai-api-key"}, expKeys: map[string]string{"OPENAI_API_KEY": "sk-openai"},
		},
		{
			name: "codex compatible api without key", agent: &codexAgent, input: "jj\rhttp://localhost:11434/v1\r\r",
			exp: agentSettings{Mode: "openai-compatible", URL: "http://localhost:11434/v1", NoKey: true}, expKeys: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			ui, out := newTestUI(tt.input)
			got, keys, err := runAgentSetup(ui, tt.agent, tt.current)
			require.NoError(t, err)
			require.Equal(t, tt.exp, got)
			require.Equal(t, tt.expKeys, keys)
			for _, s := range tt.expOut {
				require.Contains(t, out.String(), s)
			}
		})
	}

	t.Run("abort", func(t *testing.T) {
		ui, _ := newTestUI("q")
		_, _, err := runAgentSetup(ui, &claudeAgent, nil)
		require.ErrorIs(t, err, errSetupAborted)
	})
}

func TestAgentSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agents.yaml")
	got, err := loadAgentSettings(path)
	require.NoError(t, err)
	require.Empty(t, got)

	want := map[string]agentSettings{
		"claude": {Mode: "tetrate-passthrough", URL: "https://router.example.com"},
		"codex":  {Mode: "openai-compatible", URL: "http://localhost:11434/v1", NoKey: true},
	}
	require.NoError(t, saveAgentSettings(path, want))
	got, err = loadAgentSettings(path)
	require.NoError(t, err)
	require.Equal(t, want, got)

	require.NoError(t, os.WriteFile(path, []byte("claude: ["), 0o600))
	_, err = loadAgentSettings(path)
	require.Error(t, err)
}

// providerOf renders the configuration of one agent and returns its provider.
func providerOf(t *testing.T, a *agentSpec, s agentSettings) (autoconfig.Provider, autoconfig.Backend) {
	var data autoconfig.ConfigData
	require.NoError(t, a.addProvider(&data, s))
	require.Len(t, data.Providers, 1)
	return data.Providers[0], data.Backends[0]
}

func TestAgentAddProvider(t *testing.T) {
	header := &autoconfig.Header{Name: agentHeader, Value: "claude"}

	p, b := providerOf(t, &claudeAgent, agentSettings{Mode: "subscription"})
	require.Equal(t, "Anthropic", p.SchemaName)
	require.Equal(t, header, p.HeaderMatch)
	require.Empty(t, p.APIKeyEnv)
	require.Empty(t, p.Headers)
	// The user's login is forwarded.
	require.Equal(t, []string{agentHeader}, p.RemoveHeaders)
	require.Equal(t, "api.anthropic.com", b.Hostname)

	p, _ = providerOf(t, &claudeAgent, agentSettings{Mode: "anthropic-api-key"})
	require.Equal(t, "ANTHROPIC_API_KEY", p.APIKeyEnv)
	require.Equal(t, []string{agentHeader, "authorization", "x-api-key", tetratePassthroughHeader}, p.RemoveHeaders)

	p, b = providerOf(t, &claudeAgent, agentSettings{Mode: "tetrate-passthrough", URL: "https://router.example.com"})
	require.Empty(t, p.APIKeyEnv)
	require.Equal(t, []autoconfig.Header{{Name: tetratePassthroughHeader, Value: "${" + tetrateKeyEnv + "}"}}, p.Headers)
	require.Equal(t, []string{agentHeader}, p.RemoveHeaders)
	require.Equal(t, "router.example.com", b.Hostname)

	p, b = providerOf(t, &claudeAgent, agentSettings{Mode: "tetrate-managed"})
	require.Equal(t, tetrateKeyEnv, p.APIKeyEnv)
	require.Equal(t, "api.router.tetrate.ai", b.Hostname)

	p, b = providerOf(t, &codexAgent, agentSettings{Mode: "openai-compatible", URL: "http://localhost:11434/v1", NoKey: true})
	require.Equal(t, "OpenAI", p.SchemaName)
	require.Empty(t, p.APIKeyEnv)
	require.Equal(t, &autoconfig.Header{Name: agentHeader, Value: "codex"}, p.HeaderMatch)
	require.Equal(t, 11434, b.Port)

	var data autoconfig.ConfigData
	require.Error(t, claudeAgent.addProvider(&data, agentSettings{Mode: "nope"}))
}

func TestWriteAgentGatewayConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, writeAgentGatewayConfig(path, map[string]agentSettings{
		"claude": {Mode: "tetrate-passthrough"},
		"codex":  {Mode: "openai-api-key"},
	}))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	config := string(raw)

	// Every document is valid YAML, and each agent is routed by its header.
	for _, doc := range strings.Split(config, "\n---") {
		var obj map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(doc), &obj), doc)
	}
	require.Contains(t, config, `name: "x-aigw-agent"
              value: "claude"`)
	require.Contains(t, config, `name: "x-aigw-agent"
              value: "codex"`)
	require.Contains(t, config, "request: 30m")
	require.Contains(t, config, `value: "${TARS_API_KEY}"`)
	require.Contains(t, config, "apiKey: |-\n    ${OPENAI_API_KEY}")
}

func TestAgentCommand(t *testing.T) {
	base := []string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-real", "ANTHROPIC_CUSTOM_HEADERS=x-team: a"}

	args, env, err := claudeAgent.command(agentSettings{Mode: "subscription"}, []string{"-p", "hi"}, base)
	require.NoError(t, err)
	require.Equal(t, "--settings", args[0])
	require.JSONEq(t, `{"env":{"ANTHROPIC_BASE_URL":"http://localhost:1975/anthropic","ANTHROPIC_CUSTOM_HEADERS":"x-team: a\nx-aigw-agent: claude"}}`, args[1])
	require.Equal(t, []string{"-p", "hi"}, args[2:])
	require.Empty(t, lookupEnv(env, "ANTHROPIC_API_KEY"))
	require.Empty(t, lookupEnv(env, "ANTHROPIC_AUTH_TOKEN"))
	require.Equal(t, "http://localhost:1975/anthropic", lookupEnv(env, "ANTHROPIC_BASE_URL"))
	require.Equal(t, "/bin", lookupEnv(env, "PATH"))

	args, env, err = claudeAgent.command(agentSettings{Mode: "anthropic-api-key"}, nil, base)
	require.NoError(t, err)
	require.Contains(t, args[1], `"ANTHROPIC_AUTH_TOKEN":"aigw"`)
	require.Equal(t, "aigw", lookupEnv(env, "ANTHROPIC_AUTH_TOKEN"))

	args, env, err = codexAgent.command(agentSettings{Mode: "openai-api-key"}, []string{"exec", "hi"}, base)
	require.NoError(t, err)
	require.Contains(t, args, `model_providers.aigw.base_url="http://localhost:1975/v1"`)
	require.Contains(t, args, `model_providers.aigw.http_headers={"x-aigw-agent"="codex"}`)
	require.Equal(t, []string{"exec", "hi"}, args[len(args)-2:])
	require.Equal(t, "aigw", lookupEnv(env, "AIGW_CODEX_API_KEY"))

	_, _, err = claudeAgent.command(agentSettings{Mode: "nope"}, nil, base)
	require.Error(t, err)
}

func TestAgentMissingKey(t *testing.T) {
	t.Setenv(tetrateKeyEnv, "")
	s := agentSettings{Mode: "tetrate-managed"}
	require.Equal(t, tetrateKeyEnv, claudeAgent.missingKey(s, nil))
	require.Empty(t, claudeAgent.missingKey(s, map[string]string{tetrateKeyEnv: "k"}))
	require.Empty(t, claudeAgent.missingKey(agentSettings{Mode: "subscription"}, nil))
	require.Empty(t, codexAgent.missingKey(agentSettings{Mode: "openai-compatible", NoKey: true}, nil))
	t.Setenv(tetrateKeyEnv, "k")
	require.Empty(t, claudeAgent.missingKey(s, nil))
}

func TestAgentDescribe(t *testing.T) {
	require.Equal(t, "Claude subscription (api.anthropic.com)", claudeAgent.describe(agentSettings{Mode: "subscription"}))
	require.Equal(t, "Tetrate Agent Router, passthrough (router.example.com)",
		claudeAgent.describe(agentSettings{Mode: "tetrate-passthrough", URL: "https://router.example.com"}))
	require.Equal(t, "nope", claudeAgent.describe(agentSettings{Mode: "nope"}))
}

func TestWrap(t *testing.T) {
	require.Equal(t, []string{"aa bb", "cc"}, wrap("aa bb cc", 5))
	require.Equal(t, []string{""}, wrap("", 5))
}
