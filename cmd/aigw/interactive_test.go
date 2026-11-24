// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

const (
	keyDown  = "\x1b[B"
	keyEnter = "\r"
)

// providerIndex returns the position of the named provider in the checklist.
func providerIndex(t *testing.T, name string) int {
	for i := range interactiveProviders {
		if interactiveProviders[i].name == name {
			return i
		}
	}
	t.Fatalf("unknown provider %q", name)
	return -1
}

// selectKeys returns the keypresses that select the given providers and confirm.
func selectKeys(t *testing.T, names ...string) string {
	var b strings.Builder
	cursor := 0
	for _, name := range names {
		i := providerIndex(t, name)
		require.GreaterOrEqual(t, i, cursor, "providers must be given in checklist order")
		b.WriteString(strings.Repeat(keyDown, i-cursor) + " ")
		cursor = i
	}
	return b.String() + keyEnter
}

func newTestUI(input string) (*terminalUI, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &terminalUI{in: strings.NewReader(input), out: out}, out
}

func TestRunInteractiveSetup(t *testing.T) {
	t.Run("single provider gets every model", func(t *testing.T) {
		ui, out := newTestUI(selectKeys(t, "openai") + "sk-test" + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		save, env, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.False(t, save)
		require.Equal(t, []autoconfig.Provider{
			{Name: "openai", SchemaName: "OpenAI", ModelMatch: ".*", APIKeyEnv: "OPENAI_API_KEY"},
		}, data.Providers)
		require.Equal(t, map[string]string{"OPENAI_API_KEY": "sk-test"}, env)
		require.NotContains(t, out.String(), "export OPENAI_API_KEY", "nothing to export when not saved")
		require.NotContains(t, out.String(), "sk-test", "the API key must not be echoed")
		require.Contains(t, out.String(), "any model")
		require.Contains(t, out.String(), `"model": "gpt-5-mini"`)
	})

	t.Run("several providers are routed by model", func(t *testing.T) {
		ui, out := newTestUI(selectKeys(t, "openai", "anthropic", "tars") +
			"sk-openai" + keyEnter + "sk-ant" + keyEnter + "sk-tars" + keyEnter + keyEnter)
		var data autoconfig.ConfigData
		save, env, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.True(t, save)
		require.Equal(t, map[string]string{
			"OPENAI_API_KEY": "sk-openai", "ANTHROPIC_API_KEY": "sk-ant", "TARS_API_KEY": "sk-tars",
		}, env)
		for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TARS_API_KEY"} {
			require.Contains(t, out.String(), "export "+v+"=<API key>")
		}
		for _, key := range env {
			require.NotContains(t, out.String(), key, "API keys must not be printed")
		}
		require.Len(t, data.Providers, 3)
		require.Equal(t, "openai", data.Providers[0].Name)
		require.Equal(t, "claude-.*", data.Providers[1].ModelMatch)
		require.Equal(t, "tars", data.Providers[2].Name)
		require.Equal(t, ".*", data.Providers[2].ModelMatch)
		require.Contains(t, out.String(), "any other model")
		require.Contains(t, out.String(), "Configuration saved to /cfg/config.yaml")
		require.Contains(t, out.String(), "/v1/chat/completions")
		require.Contains(t, out.String(), "/anthropic/v1/messages")
	})

	t.Run("only the last catch-all provider gets the remaining models", func(t *testing.T) {
		ui, _ := newTestUI(selectKeys(t, "tars", "ollama") +
			"sk-tars" + keyEnter + keyEnter + keyEnter + // Ollama base URL and model defaults.
			"," + keyEnter + // Rejected: no model names.
			"gpt-5, gpt-5-mini" + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		_, env, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.Equal(t, map[string]string{"TARS_API_KEY": "sk-tars"}, env, "Ollama has no API key")
		require.Equal(t, "tars", data.Providers[0].Name)
		require.Equal(t, `gpt-5|gpt-5-mini`, data.Providers[0].ModelMatch)
		require.Equal(t, "ollama", data.Providers[1].Name)
		require.Equal(t, ".*", data.Providers[1].ModelMatch)
		require.Empty(t, data.Providers[1].APIKeyEnv)
	})

	t.Run("azure", func(t *testing.T) {
		ui, out := newTestUI(selectKeys(t, "azure-openai") +
			"example.com" + keyEnter + // Rejected: not a URL.
			"https://example.openai.azure.com" + keyEnter + keyEnter +
			keyEnter + // Rejected: a deployment is required.
			" , " + keyEnter + // Rejected: no deployment names.
			"my-gpt" + keyEnter +
			keyEnter + // Rejected: the key is required.
			"azure-key" + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		_, env, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.Equal(t, []autoconfig.Provider{{
			Name: "azure-openai", SchemaName: "AzureOpenAI", Version: "2024-12-01-preview",
			ModelMatch: ".*", APIKeyEnv: "AZURE_OPENAI_API_KEY",
		}}, data.Providers)
		require.Contains(t, out.String(), `"model": "my-gpt"`, "the example request uses a deployment")
		require.Equal(t, map[string]string{"AZURE_OPENAI_API_KEY": "azure-key"}, env)
		require.Equal(t, "example.openai.azure.com", data.Backends[0].Hostname)
	})

	t.Run("one provider receives the other models when all are model-specific", func(t *testing.T) {
		ui, _ := newTestUI(selectKeys(t, "openai", "anthropic") +
			"sk-openai" + keyEnter + "sk-ant" + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		_, _, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.Len(t, data.Providers, 2)
		require.Equal(t, "anthropic", data.Providers[0].Name)
		require.Equal(t, autoconfig.ClaudeModels, data.Providers[0].ModelMatch)
		require.Equal(t, "openai", data.Providers[1].Name)
		require.Equal(t, autoconfig.AllModels, data.Providers[1].ModelMatch, "OpenAI is the catch-all")
	})

	t.Run("first provider receives the other models when OpenAI is not selected", func(t *testing.T) {
		ui, _ := newTestUI(selectKeys(t, "anthropic", "azure-openai") +
			"sk-ant" + keyEnter + "https://example.openai.azure.com" + keyEnter + keyEnter +
			"my-gpt" + keyEnter + "azure-key" + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		_, _, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.Equal(t, "azure-openai", data.Providers[0].Name)
		require.Equal(t, "my-gpt", data.Providers[0].ModelMatch)
		require.Equal(t, "anthropic", data.Providers[1].Name)
		require.Equal(t, autoconfig.AllModels, data.Providers[1].ModelMatch)
	})

	t.Run("asks for a model of an OpenAI-compatible API", func(t *testing.T) {
		ui, out := newTestUI(selectKeys(t, "openai-compatible") +
			"https://llm.example.com/v1" + keyEnter +
			keyEnter + // Rejected: a model is required.
			"my-model" + keyEnter + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		_, env, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.Empty(t, env, "no API key was entered")
		require.Contains(t, out.String(), `"model": "my-model"`)
		require.NotContains(t, out.String(), "<model>")
	})

	t.Run("nothing selected", func(t *testing.T) {
		ui, out := newTestUI(keyEnter + " " + keyEnter + "sk" + keyEnter + "n" + keyEnter)
		var data autoconfig.ConfigData
		_, _, err := runInteractiveSetup(ui, &data, "/cfg/config.yaml")
		require.NoError(t, err)
		require.Contains(t, out.String(), "Select at least one provider")
		require.Len(t, data.Providers, 1)
	})

	t.Run("aborted", func(t *testing.T) {
		for _, input := range []string{"q", "\x03", " " + keyEnter + "\x03"} {
			ui, _ := newTestUI(input)
			_, _, err := runInteractiveSetup(ui, &autoconfig.ConfigData{}, "/cfg/config.yaml")
			require.ErrorIs(t, err, errSetupAborted)
		}
	})
}

func TestTerminalUIInput(t *testing.T) {
	ui, out := newTestUI("abcd\x7f\x7fx" + keyEnter)
	v, err := ui.input("Key", "", true)
	require.NoError(t, err)
	require.Equal(t, "abx", v)
	require.NotContains(t, out.String(), "abx")

	ui, _ = newTestUI(keyEnter)
	v, err = ui.input("URL", "http://default", false)
	require.NoError(t, err)
	require.Equal(t, "http://default", v)
}

func TestReadConfigInteractive(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	savePath := filepath.Join(t.TempDir(), "aigw", "config.yaml")
	setup := &interactiveSetup{}
	setup.prompt = func(data *autoconfig.ConfigData) error {
		setup.savePath = savePath
		t.Setenv("OPENAI_API_KEY", "sk-$ecret")
		return autoconfig.AddProvider(data, &autoconfig.Provider{
			Name: "openai", SchemaName: "OpenAI", ModelMatch: ".*", APIKeyEnv: "OPENAI_API_KEY",
		}, "https://api.openai.com/v1")
	}
	config, err := readConfig("", nil, false, setup)
	require.NoError(t, err)
	require.Contains(t, config, "apiKey: |-\n    sk-$ecret")

	// The saved config references the key, it does not contain it.
	saved, err := os.ReadFile(savePath)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(saved), savedConfigHeader))
	require.Contains(t, string(saved), "apiKey: |-\n    ${OPENAI_API_KEY}")
	require.NotContains(t, string(saved), "sk-$ecret")

	t.Run("reloaded with the key in the environment", func(t *testing.T) {
		reloaded, err := readConfig(savePath, nil, false, nil)
		require.NoError(t, err)
		require.Contains(t, reloaded, "apiKey: |-\n    sk-$ecret")
	})

	t.Run("reloaded without the key in the environment", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		_, err := readConfig(savePath, nil, false, nil)
		require.ErrorContains(t, err, "variable ${OPENAI_API_KEY} set but empty")
		require.ErrorContains(t, err, "delete the file to run the interactive setup again")
	})

	t.Run("still strict after edits above the header", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		edited := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(edited, append([]byte("# My notes\n\n"), saved...), 0o600))
		_, err := readConfig(edited, nil, false, nil)
		require.ErrorContains(t, err, "variable ${OPENAI_API_KEY} set but empty")
	})

	t.Run("other configs still allow unset variables", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other.yaml")
		require.NoError(t, os.WriteFile(other, []byte("value: ${AIGW_TEST_UNSET_VAR}\n"), 0o600))
		config, err := readConfig(other, nil, false, nil)
		require.NoError(t, err)
		require.Equal(t, "value: \n", config)
	})

	t.Run("not called when configured from the environment", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "sk-env")
		_, err := readConfig("", nil, false, &interactiveSetup{prompt: func(*autoconfig.ConfigData) error {
			t.Fatal("interactive setup must not run")
			return nil
		}})
		require.NoError(t, err)
	})
}
