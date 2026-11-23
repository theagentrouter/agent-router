// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	_ "embed"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
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
