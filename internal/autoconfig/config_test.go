// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

var ollamaLocalYAML string

func init() {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("unable to get caller info")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../examples/aigw/ollama.yaml"))
	if err != nil {
		panic(err)
	}
	ollamaLocalYAML = string(b)
}

var (
	//go:embed testdata/openai.yaml
	openaiDefaultYAML string

	//go:embed testdata/tars.yaml
	tarsYAML string

	//go:embed testdata/openrouter.yaml
	openrouterYAML string

	//go:embed testdata/llamastack.yaml
	llamastackYAML string

	//go:embed testdata/azure-openai.yaml
	azureOpenAIYAML string

	//go:embed testdata/openai-with-org.yaml
	openaiWithOrgYAML string

	//go:embed testdata/openai-with-project.yaml
	openaiWithProjectYAML string

	//go:embed testdata/openai-with-org-and-project.yaml
	openaiWithOrgAndProjectYAML string

	//go:embed testdata/azure-openai-with-org-and-project.yaml
	azureOpenAIWithOrgAndProjectYAML string

	//go:embed testdata/debug.yaml
	debugYAML string

	//go:embed testdata/kiwi.yaml
	kiwiYAML string

	//go:embed testdata/openai-github.yaml
	openaiGithubYAML string

	//go:embed testdata/anthropic.yaml
	anthropicYAML string

	//go:embed testdata/anthropic-prefix.yaml
	anthropicPrefixYAML string

	//go:embed testdata/openai-otel.yaml
	openaiOTELYAML string

	//go:embed testdata/openai-otel-ip.yaml
	openaaiOTELIPYAML string
)

func TestWriteConfig(t *testing.T) {
	tests := []struct {
		name     string
		input    ConfigData
		expected string
	}{
		{
			name: "default (OpenAI)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: openaiDefaultYAML,
		},
		{
			name: "OpenAI with OTEL access logging",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
					{
						Name:        "otel",
						Hostname:    "otel-collector.monitoring",
						Port:        443,
						NeedsTLS:    true,
						IsTelemetry: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				OTELLog: &otelLogConfig{
					Exporter:    "otlp",
					BackendName: "otel",
					Headers: []otelHeader{
						{Name: "Authorization", Value: "ApiKey fake-key"},
					},
					Resources: []otelResourceAttr{
						{Key: "deployment.environment", Value: "production"},
						{Key: "service.name", Value: "ai-gateway-service"},
						{Key: "service.version", Value: "1.2.3"},
					},
				},
			},
			expected: openaiOTELYAML,
		},
		{
			name: "OpenAI and OTEL IPs",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						IP:       "127.0.0.1",
						Port:     11434,
						NeedsTLS: false,
					},
					{
						Name:        "otel",
						IP:          "127.0.0.1",
						Port:        4317,
						NeedsTLS:    false,
						IsTelemetry: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				OTELLog: &otelLogConfig{
					Exporter:    "otlp",
					BackendName: "otel",
				},
			},
			expected: openaaiOTELIPYAML,
		},
		{
			name: "Azure OpenAI",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "my-resource.openai.azure.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "AzureOpenAI",
					Version:     "2024-02-15-preview",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: azureOpenAIYAML,
		},
		{
			name: "Ollama (localhost with port)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "localhost",
						Port:     11434,
						NeedsTLS: false,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: ollamaLocalYAML,
		},
		{
			name: "Tetrate Agent Router Service (https host)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.router.tetrate.ai",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: tarsYAML,
		},
		{
			name: "OpenRouter (https path prefix)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "openrouter.ai",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "api/v1",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: openrouterYAML,
		},
		{
			name: "LlamaStack (localhost path prefix and port)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "localhost",
						Port:     8321,
						NeedsTLS: false,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "v1/openai/v1",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: llamastackYAML,
		},
		{
			name: "OpenAI with organization ID",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName:    "openai",
					SchemaName:     "OpenAI",
					Version:        "",
					OrganizationID: "org-test123",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: openaiWithOrgYAML,
		},
		{
			name: "OpenAI with project ID",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
					ProjectID:   "proj_test456",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: openaiWithProjectYAML,
		},
		{
			name: "OpenAI with both org and project ID",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName:    "openai",
					SchemaName:     "OpenAI",
					Version:        "",
					OrganizationID: "org-test123",
					ProjectID:      "proj_test456",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: openaiWithOrgAndProjectYAML,
		},
		{
			name: "Azure OpenAI with org and project ID",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "my-resource.openai.azure.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName:    "openai",
					SchemaName:     "AzureOpenAI",
					Version:        "2024-02-15-preview",
					OrganizationID: "org-test123",
					ProjectID:      "proj_test456",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: azureOpenAIWithOrgAndProjectYAML,
		},
		{
			name: "OpenAI with debug logging and custom Envoy version",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				Debug:        true,
				EnvoyVersion: "1.35.0",
				OTELLog:      &otelLogConfig{Exporter: "console"},
			},
			expected: debugYAML,
		},
		{
			name: "Kiwi MCP server (MCP-only, no OpenAI)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "kiwi",
						Hostname: "mcp.kiwi.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				MCPBackendRefs: []MCPBackendRef{
					{
						BackendName: "kiwi",
						Path:        "/",
					},
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: kiwiYAML,
		},
		{
			name: "OpenAI with GitHub MCP server",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
					{
						Name:     "github",
						Hostname: "api.githubcopilot.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "",
				},
				MCPBackendRefs: []MCPBackendRef{
					{
						BackendName:  "github",
						Path:         "/mcp/x/issues/readonly",
						APIKey:       "${GITHUB_MCP_TOKEN}",
						IncludeTools: []string{"issue_read", "list_issues"},
					},
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: openaiGithubYAML,
		},
		{
			name: "default (Anthropic)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "anthropic",
						Hostname: "api.anthropic.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Anthropic: &AnthropicConfig{
					BackendName: "anthropic",
					SchemaName:  "Anthropic",
					Version:     "",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: anthropicYAML,
		},
		{
			name: "Anthropic (path prefix)",
			input: ConfigData{
				Backends: []Backend{
					{
						Name:     "anthropic",
						Hostname: "api.anthropic.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Anthropic: &AnthropicConfig{
					BackendName: "anthropic",
					SchemaName:  "Anthropic",
					Version:     "api/v1",
				},
				OTELLog: &otelLogConfig{Exporter: "console"},
			},
			expected: anthropicPrefixYAML,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WriteConfig(&tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		name          string
		baseURL       string
		expected      parsedURL
		expectedError error
	}{
		{
			name:    "HTTPS with default port",
			baseURL: "https://api.openai.com/v1",
			expected: parsedURL{
				hostname: "api.openai.com",
				port:     443,
				version:  "", // v1 is omitted for cleaner output
				needsTLS: true,
			},
		},
		{
			name:    "custom path preserved",
			baseURL: "https://custom.ai/v1beta/openai",
			expected: parsedURL{
				hostname: "custom.ai",
				port:     443,
				version:  "v1beta/openai",
				needsTLS: true,
			},
		},
		{
			name:    "HTTP with default port 80",
			baseURL: "http://example.com/v1",
			expected: parsedURL{
				hostname: "example.com",
				port:     80,
				version:  "", // v1 is omitted
				needsTLS: false,
			},
		},
		{
			name:    "empty path treated as no version",
			baseURL: "https://api.example.com",
			expected: parsedURL{
				hostname: "api.example.com",
				port:     443,
				version:  "",
				needsTLS: true,
			},
		},
		{
			name:    "trailing slash ignored",
			baseURL: "https://api.example.com/",
			expected: parsedURL{
				hostname: "api.example.com",
				port:     443,
				version:  "",
				needsTLS: true,
			},
		},
		{
			name:          "invalid URL",
			baseURL:       ":::invalid",
			expectedError: fmt.Errorf("invalid base URL: parse \":::invalid\": missing protocol scheme"),
		},
		{
			name:          "missing hostname",
			baseURL:       "http:///path",
			expectedError: fmt.Errorf("invalid base URL: missing hostname"),
		},
		{
			name:          "unsupported scheme",
			baseURL:       "ftp://example.com",
			expectedError: fmt.Errorf("invalid base URL: unsupported scheme \"ftp\""),
		},
		{
			name:          "unsupported scheme with explicit port",
			baseURL:       "ftp://example.com:21",
			expectedError: fmt.Errorf("invalid base URL: unsupported scheme \"ftp\""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseURL(tt.baseURL)

			if tt.expectedError != nil {
				require.Error(t, err)
				require.Equal(t, tt.expectedError.Error(), err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, *got)
			}
		})
	}
}
