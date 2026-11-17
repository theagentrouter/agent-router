// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// MCPServers is the structure of the MCP servers configuration file.
// This matches the format used by MCP client configuration files.
type MCPServers struct {
	McpServers map[string]MCPServer `json:"mcpServers"`
}

// MCPServer represents the configuration for a Model Context Protocol (MCP) server.
// This follows the canonical JSON format used by MCP clients like Claude Desktop, Cursor,
// VS Code, and other MCP ecosystem tools.
//
// The includeTools field follows the convention established by Gemini CLI:
// https://google-gemini.github.io/gemini-cli/docs/tools/mcp-server.html#optional
//
// Example canonical configuration:
//
//	{
//	  "mcpServers": {
//	    "github": {
//	      "type": "http",
//	      "url": "https://api.githubcopilot.com/mcp/",
//	      "headers": {"Authorization": "Bearer ghp_xxxxxxxxxxxx"},
//	      "includeTools": ["search_repositories", "get_file_contents", "list_issues"]
//	    }
//	  }
//	}
type MCPServer struct {
	// Type specifies the MCP server transport protocol.
	// Common values: "http", "streamable-http", "sse"
	Type string `json:"type"`

	// URL is the full endpoint URL for the MCP server.
	// For HTTP servers: "https://api.example.com/mcp"
	// For local servers: "http://localhost:3000/mcp"
	URL string `json:"url"`

	// Headers contains optional HTTP headers sent to the MCP server.
	// Commonly used for authentication: {"Authorization": "Bearer token"}
	Headers map[string]string `json:"headers,omitempty"`

	// IncludeTools specifies which tools will be available from the server.
	IncludeTools []string `json:"includeTools,omitempty"`

	// Command and Args are used for stdio MCP servers.
	// These values are only used during configuration parsing and are never used to render
	// the final configuration.
	// When stdio MCP servers are configured, we will run local Streamable HTTP proxies for
	// each command and update this MCP configuration to point to the local HTTP proxies.

	// Command is the executable to run.
	Command string `json:"command,omitempty"`
	// Args are the command-line arguments.
	Args []string `json:"args,omitempty"`
}

// AddMCPServers adds MCP server configurations to the ConfigData.
// It parses the MCPServers input and populates Backends and MCPBackendRefs fields.
// If the input is nil or empty, this function does nothing (safe to call).
//
// Headers are parsed intelligently:
//   - "Authorization: Bearer <token>" → extracted as APIKey (preserving ${VAR} envsubst syntax)
//   - Other headers → stored in Headers map for headerMutation
//
// Bearer tokens and other header values preserve envsubst syntax (e.g., "${VAR}") for
// runtime substitution by cmd/extproc.
func AddMCPServers(data *ConfigData, input *MCPServers) error {
	if data == nil {
		return fmt.Errorf("ConfigData cannot be nil")
	}
	if input == nil || len(input.McpServers) == 0 {
		// Nothing to add, skip silently
		return nil
	}

	// Collect all server names for consistent ordering
	names := make([]string, 0, len(input.McpServers))
	for name := range input.McpServers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		settings := input.McpServers[name]

		// Skip servers that are not streamable HTTP
		if !isSupportedMCPType(settings.URL, settings.Type) {
			continue
		}

		serverURL, err := url.Parse(settings.URL)
		if err != nil {
			return fmt.Errorf("failed to parse MCP server URL %s: %w", settings.URL, err)
		}

		// Determine port
		port := 0
		if serverURL.Port() != "" {
			port, _ = strconv.Atoi(serverURL.Port())
		}
		if port == 0 {
			switch serverURL.Scheme {
			case "https":
				port = 443
			case "http":
				port = 80
			}
		}

		// Extract path (default to "/" if empty)
		path := serverURL.Path
		if path == "" {
			path = "/"
		}

		// Parse headers intelligently
		var apiKey string
		headers := make(map[string]string)

		for headerKey, headerValue := range settings.Headers {
			if strings.EqualFold(headerKey, "Authorization") && strings.HasPrefix(headerValue, "Bearer ") {
				// Extract API key from Authorization header
				apiKey = strings.TrimPrefix(headerValue, "Bearer ")
			} else {
				// Keep other headers for headerMutation
				headers[headerKey] = headerValue
			}
		}

		// Create Backend for this MCP server
		hostname, ip := splitHost(serverURL.Hostname())
		backend := Backend{
			Name:     name,
			Hostname: hostname,
			IP:       ip,
			Port:     port,
			NeedsTLS: serverURL.Scheme == "https",
		}

		// Create MCPBackendRef referencing the backend
		backendRef := MCPBackendRef{
			BackendName:  name,
			Path:         path,
			IncludeTools: settings.IncludeTools,
			APIKey:       apiKey,
			Headers:      headers,
		}

		// Add to ConfigData
		data.Backends = append(data.Backends, backend)
		data.MCPBackendRefs = append(data.MCPBackendRefs, backendRef)
	}

	return nil
}

// isSupportedMCPType returns true if the given server type is supported.
func isSupportedMCPType(url, serverType string) bool {
	if url == "" {
		return false
	}
	// Be permissive with the type to maximize support for the different values used in agents.
	return serverType == "" || // If the URL is set and there is no type, assume Streamable HTTP.
		serverType == "http" ||
		serverType == "streamable-http" ||
		serverType == "streamable_http" ||
		serverType == "streamableHttp"
}
