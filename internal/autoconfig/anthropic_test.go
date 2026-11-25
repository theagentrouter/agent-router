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

func TestPopulateAnthropicEnvConfig(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name          string
		envVars       map[string]string
		expected      ConfigData
		expectedError error
	}{
		{
			name: "default (Anthropic)",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test123",
				// ANTHROPIC_BASE_URL not set, defaults to https://api.anthropic.com/v1
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "anthropic",
						Hostname: "api.anthropic.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "anthropic", SchemaName: "Anthropic", ModelMatch: AllModels, APIKeyEnv: "ANTHROPIC_API_KEY"}},
			},
		},
		{
			name: "Anthropic with custom base URL",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY":  "sk-ant-test123",
				"ANTHROPIC_BASE_URL": "https://custom.anthropic.com/v2",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "anthropic",
						Hostname: "custom.anthropic.com",
						Port:     443,
						NeedsTLS: true,
					},
				},
				Providers: []Provider{{Name: "anthropic", SchemaName: "Anthropic", Version: "v2", ModelMatch: AllModels, APIKeyEnv: "ANTHROPIC_API_KEY"}},
			},
		},
		{
			name: "Anthropic with localhost",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY":  "sk-ant-test123",
				"ANTHROPIC_BASE_URL": "http://localhost:8080/v1",
			},
			expected: ConfigData{
				Backends: []Backend{
					{
						Name:     "anthropic",
						Hostname: "localhost",
						Port:     8080,
						NeedsTLS: false,
					},
				},
				Providers: []Provider{{Name: "anthropic", SchemaName: "Anthropic", ModelMatch: AllModels, APIKeyEnv: "ANTHROPIC_API_KEY"}},
			},
		},
		{
			name:          "missing required API key",
			envVars:       map[string]string{},
			expectedError: fmt.Errorf("ANTHROPIC_API_KEY environment variable is required"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			// Test PopulateAnthropicEnvConfig
			data := &ConfigData{}
			err := PopulateAnthropicEnvConfig(data, AllModels)

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
