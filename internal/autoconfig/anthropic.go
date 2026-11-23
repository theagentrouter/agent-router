// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	"fmt"
	"os"
)

// PopulateAnthropicEnvConfig adds an Anthropic provider to ConfigData, configured from
// the standard Anthropic SDK environment variables, serving the models matched by modelMatch.
//
// This errs if ANTHROPIC_API_KEY is not set.
//
// See https://docs.anthropic.com/en/api/client-sdks
func PopulateAnthropicEnvConfig(data *ConfigData, modelMatch string) error {
	if data == nil {
		return fmt.Errorf("ConfigData cannot be nil")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
	}

	// Get base URL, defaulting to the official Anthropic API endpoint
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	p := &Provider{ //nolint:gosec // APIKeyEnv is the name of the variable, not a credential.
		Name:       "anthropic",
		SchemaName: "Anthropic",
		ModelMatch: modelMatch,
		APIKeyEnv:  "ANTHROPIC_API_KEY",
	}
	return AddProvider(data, p, baseURL)
}
