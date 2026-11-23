// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	"fmt"
	"strings"
)

// Provider is an LLM provider in a multi-provider configuration, where requests
// are routed to each provider based on the requested model name.
type Provider struct {
	Name       string   // Resource name of the provider's Backend, AIServiceBackend, Secret, etc.
	SchemaName string   // Schema name: "OpenAI", "AzureOpenAI" or "Anthropic"
	Version    string   // API version (path prefix, or the Azure API version)
	ModelMatch string   // Regular expression matched against the requested model name
	APIKey     string   // API key, stored inline in the generated Secret. Empty means no authentication.
	APIKeyEnv  string   // Environment variable holding the API key, referenced from the Secret instead of APIKey.
	Headers    []Header // Headers added to every request sent to the provider.
}

// Header is an HTTP header name and value.
type Header struct {
	Name  string
	Value string
}

const (
	// AllModels matches every model name.
	AllModels = ".*"
	// ClaudeModels matches the Anthropic Claude models.
	ClaudeModels = "claude-.*"
)

// AddProvider adds a provider reachable at baseURL to the configuration. Providers are
// routed in the order they are added, so catch-all providers must be added last.
func AddProvider(data *ConfigData, provider *Provider, baseURL string) error {
	if data == nil {
		return fmt.Errorf("ConfigData cannot be nil")
	}
	parsed, err := parseURL(baseURL)
	if err != nil {
		return err
	}
	p := *provider
	if p.Version == "" && p.SchemaName != "AzureOpenAI" {
		p.Version = parsed.version
	}
	// Escape "$" so that envsubst, which runs on the generated configuration, leaves the key unchanged.
	p.APIKey = strings.ReplaceAll(p.APIKey, "$", "$$")
	data.Backends = append(data.Backends, Backend{
		Name:     p.Name,
		Hostname: parsed.hostname,
		IP:       parsed.ip,
		Port:     parsed.port,
		NeedsTLS: parsed.needsTLS,
	})
	data.Providers = append(data.Providers, p)
	return nil
}
