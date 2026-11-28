// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestPopulateOpenAIEnvConfig(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name          string
		envVars       map[string]string
		expected      ConfigData
		expectedError error
	}{
		{
			name: "default (OpenAI)",
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test123",
				// OPENAI_BASE_URL not set, defaults to https://api.openai.com/v1
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY"}},
			},
		},
		{
			name: "Scaleway Generative APIs",
			envVars: map[string]string{
				"OPENAI_API_KEY":  "test-scaleway-key",
				"OPENAI_BASE_URL": "https://api.scaleway.ai/11111111-2222-3333-4444-555555555555/v1",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.scaleway.ai",
						Port:     443,
						NeedsTLS: true,
					},
				},
				OpenAI: &OpenAIConfig{
					BackendName: "openai",
					SchemaName:  "OpenAI",
					Version:     "11111111-2222-3333-4444-555555555555/v1",
				},
			},
		},
		{
			name: "Azure OpenAI",
			envVars: map[string]string{
				"AZURE_OPENAI_API_KEY":  "azure-key-123",
				"AZURE_OPENAI_ENDPOINT": "https://my-resource.openai.azure.com",
				"OPENAI_API_VERSION":    "2024-02-15-preview",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "my-resource.openai.azure.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "AzureOpenAI", Version: "2024-02-15-preview", ModelMatch: AllModels, APIKeyEnv: "AZURE_OPENAI_API_KEY"}},
			},
		},
		{
			name: "Azure OpenAI prioritized over standard OpenAI",
			envVars: map[string]string{
				"AZURE_OPENAI_API_KEY":  "azure-key-123",
				"AZURE_OPENAI_ENDPOINT": "https://my-resource.openai.azure.com",
				"OPENAI_API_VERSION":    "2024-02-15-preview",
				"OPENAI_API_KEY":        "sk-test123",
				"OPENAI_BASE_URL":       "https://api.openai.com/v1",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "my-resource.openai.azure.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "AzureOpenAI", Version: "2024-02-15-preview", ModelMatch: AllModels, APIKeyEnv: "AZURE_OPENAI_API_KEY"}},
			},
		},
		{
			name: "Ollama (localhost with port)",
			envVars: map[string]string{
				"OPENAI_API_KEY":  "ignored",
				"OPENAI_BASE_URL": "http://localhost:11434/v1",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "localhost",
						Port:     11434,
						NeedsTLS: false,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY"}},
			},
		},
		{
			name: "OpenAI with IP base URL",
			envVars: map[string]string{
				"OPENAI_API_KEY":  "sk-test123",
				"OPENAI_BASE_URL": "http://127.0.0.1:11434/v1",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						IP:       "127.0.0.1",
						Port:     11434,
						NeedsTLS: false,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY"}},
			},
		},
		{
			name: "OpenAI with organization ID",
			envVars: map[string]string{
				"OPENAI_API_KEY": "sk-test123",
				"OPENAI_ORG_ID":  "org-test123",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY", Headers: []Header{{Name: "OpenAI-Organization", Value: "org-test123"}}}},
			},
		},
		{
			name: "OpenAI with project ID",
			envVars: map[string]string{
				"OPENAI_API_KEY":    "sk-test123",
				"OPENAI_PROJECT_ID": "proj_test456",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY", Headers: []Header{{Name: "OpenAI-Project", Value: "proj_test456"}}}},
			},
		},
		{
			name: "OpenAI with both org and project ID",
			envVars: map[string]string{
				"OPENAI_API_KEY":    "sk-test123",
				"OPENAI_ORG_ID":     "org-test123",
				"OPENAI_PROJECT_ID": "proj_test456",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "openai",
						Hostname: "api.openai.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "openai", SchemaName: "OpenAI", ModelMatch: AllModels, APIKeyEnv: "OPENAI_API_KEY", Headers: []Header{{Name: "OpenAI-Organization", Value: "org-test123"}, {Name: "OpenAI-Project", Value: "proj_test456"}}}},
			},
		},
		{
			name:          "missing required API key",
			envVars:       map[string]string{},
			expectedError: fmt.Errorf("either OPENAI_API_KEY or AZURE_OPENAI_API_KEY environment variable is required"),
		},
		{
			name: "Azure missing AZURE_OPENAI_ENDPOINT",
			envVars: map[string]string{
				"AZURE_OPENAI_API_KEY": "azure-key-123",
				"OPENAI_API_VERSION":   "2024-02-15-preview",
			},
			expectedError: fmt.Errorf("AZURE_OPENAI_ENDPOINT environment variable is required when AZURE_OPENAI_API_KEY is set"),
		},
		{
			name: "Azure missing OPENAI_API_VERSION",
			envVars: map[string]string{
				"AZURE_OPENAI_API_KEY":  "azure-key-123",
				"AZURE_OPENAI_ENDPOINT": "https://my-resource.openai.azure.com",
			},
			expectedError: fmt.Errorf("OPENAI_API_VERSION environment variable is required when AZURE_OPENAI_API_KEY is set"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			// Test PopulateOpenAIEnvConfig
			data := &ConfigData{}
			err := PopulateOpenAIEnvConfig(data, AllModels)

			// Check result
			if tt.expectedError != nil {
				require.Error(t, err)
				require.Equal(t, tt.expectedError.Error(), err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, *data)
			}
		})
	}
}
