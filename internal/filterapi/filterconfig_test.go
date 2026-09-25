// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi_test

import (
	"log/slog"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestUnmarshalConfigYaml(t *testing.T) {
	configPath := path.Join(t.TempDir(), "config.yaml")
	config := `
schema:
  name: OpenAI
llmRequestCosts:
- metadataKey: token_usage_key
  routeName: ns/my-route
  type: OutputToken
`
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))
	cfg, err := filterapi.UnmarshalConfigYaml(configPath)
	require.NoError(t, err)

	expectedCfg := &filterapi.Config{
		LLMRequestCosts: []filterapi.LLMRequestCost{
			{
				MetadataKey: "token_usage_key",
				RouteName:   "ns/my-route",
				Type:        filterapi.LLMRequestCostTypeOutputToken,
			},
		},
	}

	require.Equal(t, expectedCfg, cfg)

	t.Run("not found", func(t *testing.T) {
		_, err := filterapi.UnmarshalConfigYaml("not-found.yaml")
		require.Error(t, err)
		require.True(t, os.IsNotExist(err))
	})
	t.Run("invalid", func(t *testing.T) {
		const invalidConfig = `{wefaf3q20,9u,f02`
		require.NoError(t, os.WriteFile(configPath, []byte(invalidConfig), 0o600))
		_, err := filterapi.UnmarshalConfigYaml(configPath)
		require.Error(t, err)
	})
}

func TestVersionedAPISchemaAnthropicPrefix(t *testing.T) {
	require.Equal(t, "v1", filterapi.VersionedAPISchema{Name: filterapi.APISchemaAnthropic}.AnthropicPrefix())
	require.Equal(t, "gateway/v1", filterapi.VersionedAPISchema{
		Name:   filterapi.APISchemaAnthropic,
		Prefix: "gateway/v1",
	}.AnthropicPrefix())
}

func TestVersionedAPISchemaOpenAIPrefix(t *testing.T) {
	require.Empty(t, filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}.OpenAIPrefix())
	require.Equal(t, "openai/v1", filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSOpenAI}.OpenAIPrefix())
	require.Equal(t, "custom/v1", filterapi.VersionedAPISchema{
		Name:   filterapi.APISchemaAWSOpenAI,
		Prefix: "custom/v1",
	}.OpenAIPrefix())
}

// logAttrs extracts the key→value map from a slog.KindGroup Value.
func logAttrs(v slog.Value) map[string]string {
	result := make(map[string]string)
	for _, a := range v.Group() {
		result[a.Key] = a.Value.String()
	}
	return result
}

func TestAWSAuthLogValue(t *testing.T) {
	a := filterapi.AWSAuth{CredentialFileLiteral: "secret-creds", Region: "us-east-1"}
	attrs := logAttrs(a.LogValue())
	require.Equal(t, "[REDACTED]", attrs["credentialFileLiteral"])
	require.Equal(t, "us-east-1", attrs["region"])
}

func TestAPIKeyAuthLogValue(t *testing.T) {
	a := filterapi.APIKeyAuth{Key: "my-api-key"}
	attrs := logAttrs(a.LogValue())
	require.Equal(t, "[REDACTED]", attrs["key"])
	require.NotContains(t, attrs["key"], "my-api-key")
}

func TestAzureAPIKeyAuthLogValue(t *testing.T) {
	a := filterapi.AzureAPIKeyAuth{Key: "azure-secret-key"}
	attrs := logAttrs(a.LogValue())
	require.Equal(t, "[REDACTED]", attrs["key"])
}

func TestAnthropicAPIKeyAuthLogValue(t *testing.T) {
	a := filterapi.AnthropicAPIKeyAuth{Key: "anthropic-secret-key"}
	attrs := logAttrs(a.LogValue())
	require.Equal(t, "[REDACTED]", attrs["key"])
}

func TestAzureAuthLogValue(t *testing.T) {
	a := filterapi.AzureAuth{AccessToken: "my-access-token"}
	attrs := logAttrs(a.LogValue())
	require.Equal(t, "[REDACTED]", attrs["accessToken"])
}

func TestGCPAuthLogValue(t *testing.T) {
	g := filterapi.GCPAuth{AccessToken: "gcp-token", Region: "us-central1", ProjectName: "my-project"}
	attrs := logAttrs(g.LogValue())
	require.Equal(t, "[REDACTED]", attrs["accessToken"])
	require.Equal(t, "us-central1", attrs["region"])
	require.Equal(t, "my-project", attrs["projectName"])
}

func TestHTTPHeaderLogValue(t *testing.T) {
	h := filterapi.HTTPHeader{Name: "authorization", Value: "Bearer secret-token"}
	attrs := logAttrs(h.LogValue())
	require.Equal(t, "authorization", attrs["name"])
	require.Equal(t, "[REDACTED]", attrs["value"])
}
