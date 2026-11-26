// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	"fmt"
	"os"
)

// PopulateOpenAIEnvConfig adds an OpenAI provider to ConfigData, configured from the
// standard OpenAI SDK environment variables, serving the models matched by modelMatch.
//
// This errs if neither OPENAI_API_KEY nor AZURE_OPENAI_API_KEY is set.
// Prioritizes AZURE_OPENAI_API_KEY over OPENAI_API_KEY when both are set.
//
// For Azure OpenAI, requires AZURE_OPENAI_ENDPOINT and OPENAI_API_VERSION.
//
// See https://github.com/openai/openai-python/blob/main/src/openai/_client.py
func PopulateOpenAIEnvConfig(data *ConfigData, modelMatch string) error {
	if data == nil {
		return fmt.Errorf("ConfigData cannot be nil")
	}

	p := Provider{Name: "openai", ModelMatch: modelMatch}
	var baseURL string
	switch {
	case os.Getenv("AZURE_OPENAI_API_KEY") != "":
		// Azure OpenAI mode
		baseURL = os.Getenv("AZURE_OPENAI_ENDPOINT")
		if baseURL == "" {
			return fmt.Errorf("AZURE_OPENAI_ENDPOINT environment variable is required when AZURE_OPENAI_API_KEY is set")
		}
		p.Version = os.Getenv("OPENAI_API_VERSION")
		if p.Version == "" {
			return fmt.Errorf("OPENAI_API_VERSION environment variable is required when AZURE_OPENAI_API_KEY is set")
		}
		p.SchemaName = "AzureOpenAI"
		p.APIKeyEnv = "AZURE_OPENAI_API_KEY"
	case os.Getenv("OPENAI_API_KEY") != "":
		// Standard OpenAI mode
		baseURL = os.Getenv("OPENAI_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		p.SchemaName = "OpenAI"
		p.APIKeyEnv = "OPENAI_API_KEY"
	default:
		return fmt.Errorf("either OPENAI_API_KEY or AZURE_OPENAI_API_KEY environment variable is required")
	}

	if org := os.Getenv("OPENAI_ORG_ID"); org != "" {
		p.Headers = append(p.Headers, Header{Name: "OpenAI-Organization", Value: org})
	}
	if project := os.Getenv("OPENAI_PROJECT_ID"); project != "" {
		p.Headers = append(p.Headers, Header{Name: "OpenAI-Project", Value: project})
	}
	return AddProvider(data, &p, baseURL)
}
