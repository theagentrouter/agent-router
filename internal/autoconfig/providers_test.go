// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	_ "embed"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

//go:embed testdata/providers.yaml
var providersYAML string

func TestAddProvider(t *testing.T) {
	data := ConfigData{OTELLog: &otelLogConfig{Exporter: "console"}}
	require.NoError(t, AddProvider(&data, &Provider{
		Name: "openai", SchemaName: "OpenAI", ModelMatch: "(gpt-|o[0-9]).*", APIKey: "sk-openai",
	}, "https://api.openai.com/v1"))
	require.NoError(t, AddProvider(&data, &Provider{
		Name: "anthropic", SchemaName: "Anthropic", ModelMatch: "claude-.*", APIKey: "sk-ant-$secret",
	}, "https://api.anthropic.com/v1"))
	require.NoError(t, AddProvider(&data, &Provider{
		Name: "azure-openai", SchemaName: "AzureOpenAI", Version: "2024-12-01-preview", ModelMatch: "my-deployment", APIKeyEnv: "AZURE_OPENAI_API_KEY",
	}, "https://example.openai.azure.com"))
	require.NoError(t, AddProvider(&data, &Provider{
		Name: "ollama", SchemaName: "OpenAI", ModelMatch: ".*",
	}, "http://127.0.0.1:11434/v1"))

	got, err := WriteConfig(&data)
	require.NoError(t, err)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile("testdata/providers.yaml", []byte(got), 0o600))
		return
	}
	require.Equal(t, providersYAML, got) //nolint:testifylint // compare the exact output, including comments.

	require.Error(t, AddProvider(&data, &Provider{Name: "bad"}, "ftp://example.com"))
	require.Error(t, AddProvider(nil, &Provider{}, "https://example.com"))
}

// TestAPIKeyEnvIsAlwaysAString checks that API keys that look like other YAML types are
// still rendered as strings, as they are substituted into the generated configuration.
func TestAPIKeyEnvIsAlwaysAString(t *testing.T) {
	for _, key := range []string{"12345", "1e3", "true", "null", "~", "0x1F", `with"quote`, "with: colon", "#hash"} {
		t.Run(key, func(t *testing.T) {
			var data ConfigData
			require.NoError(t, AddProvider(&data, &Provider{
				Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "TEST_KEY",
			}, "https://api.openai.com/v1"))
			config, err := WriteConfig(&data)
			require.NoError(t, err)
			config = strings.ReplaceAll(config, "${TEST_KEY}", key)

			var found bool
			for _, doc := range strings.Split(config, "\n---") {
				var secret struct {
					Kind       string         `json:"kind"`
					StringData map[string]any `json:"stringData"`
				}
				require.NoError(t, yaml.Unmarshal([]byte(doc), &secret), doc)
				if secret.Kind == "Secret" {
					found = true
					require.Equal(t, key, secret.StringData["apiKey"])
				}
			}
			require.True(t, found)
		})
	}
}

//go:embed testdata/providers-header-match.yaml
var providersHeaderMatchYAML string

// TestAddProviderHeaderMatch checks providers routed by a request header, with removed headers
// and a custom request timeout, as used by "aigw claude" and "aigw codex".
func TestAddProviderHeaderMatch(t *testing.T) {
	data := ConfigData{OTELLog: &otelLogConfig{Exporter: "console"}}
	require.NoError(t, AddProvider(&data, &Provider{
		Name: "agent-claude", SchemaName: "Anthropic", ModelMatch: AllModels,
		HeaderMatch: &Header{Name: "x-aigw-agent", Value: "claude"}, RequestTimeout: "30m",
		Headers:       []Header{{Name: "x-tars-api-key", Value: "${TARS_API_KEY}"}},
		RemoveHeaders: []string{"x-aigw-agent"},
	}, "https://api.router.tetrate.ai/v1"))
	require.NoError(t, AddProvider(&data, &Provider{
		Name: "agent-codex", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY",
		HeaderMatch: &Header{Name: "x-aigw-agent", Value: "codex"}, RequestTimeout: "30m",
		RemoveHeaders: []string{"x-aigw-agent", "authorization", "x-api-key"},
	}, "https://api.openai.com/v1"))

	got, err := WriteConfig(&data)
	require.NoError(t, err)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile("testdata/providers-header-match.yaml", []byte(got), 0o600))
		return
	}
	require.Equal(t, providersHeaderMatchYAML, got) //nolint:testifylint // compare the exact output, including comments.
}
