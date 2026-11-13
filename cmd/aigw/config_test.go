// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/autoconfig"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

var testMcpServers = &autoconfig.MCPServers{
	McpServers: map[string]autoconfig.MCPServer{
		"dreamtap": {
			Type: "http",
			URL:  "https://dreamtap.xyz/mcp",
		},
	},
}

// TestReadConfig is mainly for coverage as the autoconfig package is tested more thoroughly.
func TestReadConfig(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name            string
		path            string
		mcpServers      *autoconfig.MCPServers
		envVars         map[string]string
		expectHostnames []string
		expectPort      string
		expectError     string
	}{
		{
			name:            "generates config for MCP",
			mcpServers:      testMcpServers,
			expectHostnames: []string{"dreamtap.xyz"},
			expectPort:      "443",
		},
		{
			name: "generates config for MCP and OpenAI",
			envVars: map[string]string{
				"OPENAI_API_KEY":  "test-key",
				"OPENAI_BASE_URL": "http://localhost:11434/v1",
			},
			mcpServers:      testMcpServers,
			expectHostnames: []string{"localhost", "dreamtap.xyz"},
			expectPort:      "11434",
		},
		{
			name: "generates config from OpenAI env vars for custom host",
			envVars: map[string]string{
				"OPENAI_API_KEY":  "test-key",
				"OPENAI_BASE_URL": "http://myservice:8080/v1",
			},
			expectHostnames: []string{"myservice"},
			expectPort:      "8080",
		},
		{
			name: "defaults to OpenAI when only API key is set",
			envVars: map[string]string{
				"OPENAI_API_KEY": "test-key",
			},
			expectHostnames: []string{"api.openai.com"},
			expectPort:      "443",
		},
		{
			name: "generates config from Anthropic env vars",
			envVars: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test123",
			},
			expectHostnames: []string{"api.anthropic.com"},
			expectPort:      "443",
		},
		{
			name: "OpenAI takes precedence when both are set",
			envVars: map[string]string{
				"OPENAI_API_KEY":    "test-key",
				"ANTHROPIC_API_KEY": "sk-ant-test123",
			},
			expectHostnames: []string{"api.openai.com"},
			expectPort:      "443",
		},
		{
			name: "configures OTEL access logs when OTLP endpoint is set",
			envVars: map[string]string{
				"OPENAI_API_KEY":              "test-key",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel-collector:4317",
			},
			expectHostnames: []string{"api.openai.com", "otel-collector"},
			expectPort:      "4317",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			config, err := readConfig(tt.path, tt.mcpServers, false)
			if tt.expectError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectError)
			} else {
				require.NoError(t, err)
				for _, expectHostname := range tt.expectHostnames {
					require.Contains(t, config, "hostname: "+expectHostname)
				}
				require.Contains(t, config, "port: "+tt.expectPort)
			}
		})
	}

	t.Run("error when file and no OPENAI_API_KEY", func(t *testing.T) {
		_, err := readConfig("", nil, false)
		require.Error(t, err)
		require.EqualError(t, err, "you must supply at least OPENAI_API_KEY, AZURE_OPENAI_API_KEY, ANTHROPIC_API_KEY, or a config file path")
	})

	t.Run("includes ENVOY_VERSION in inline MCP autoconfig", func(t *testing.T) {
		t.Setenv("ENVOY_VERSION", "1.2.3")

		config, err := readConfig("", testMcpServers, false)
		require.NoError(t, err)
		require.Contains(t, config, `envoyVersion: "1.2.3"`)
	})

	t.Run("error when file does not exist", func(t *testing.T) {
		_, err := readConfig("/non/existent/file.yaml", nil, false)
		require.Error(t, err)
		require.EqualError(t, err, "error reading config: open /non/existent/file.yaml: no such file or directory")
	})
}

func TestExpandPath(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name     string
		path     string
		envVars  map[string]string
		expected string
	}{
		{
			name:     "empty path returns empty",
			path:     "",
			expected: "",
		},
		{
			name:     "tilde path",
			path:     "~/test/file.txt",
			expected: filepath.Join(homeDir, "test/file.txt"),
		},
		{
			name:     "tilde slash returns HOME",
			path:     "~/",
			expected: homeDir,
		},
		{
			name:     "absolute path unchanged",
			path:     "/absolute/path/file.txt",
			expected: "/absolute/path/file.txt",
		},
		{
			name:     "env var expansion",
			path:     "${HOME}/test",
			expected: filepath.Join(homeDir, "test"),
		},
		{
			name:     "custom env var",
			path:     "${CUSTOM_DIR}/file.txt",
			envVars:  map[string]string{"CUSTOM_DIR": "/custom"},
			expected: "/custom/file.txt",
		},
		{
			name:     "tilde with env var",
			path:     "~/test/${USER}",
			envVars:  map[string]string{"USER": "testuser"},
			expected: filepath.Join(homeDir, "test/testuser"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			actual := expandPath(tt.path)
			require.Equal(t, tt.expected, actual)
		})
	}
	t.Run("relative/path", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		expected := filepath.Join(cwd, "relative/path")
		actual := expandPath("relative/path")
		require.Equal(t, expected, actual)
	})
}

func TestRecreateDir(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(t *testing.T, path string)
	}{
		{
			name: "creates new directory",
			setupFunc: func(*testing.T, string) {
			},
		},
		{
			name: "recreates existing directory",
			setupFunc: func(t *testing.T, path string) {
				require.NoError(t, os.MkdirAll(path, 0o755))
				testFile := filepath.Join(path, "test.txt")
				require.NoError(t, os.WriteFile(testFile, []byte("test"), 0o600))
			},
		},
		{
			name: "recreates existing directory with subdirs",
			setupFunc: func(t *testing.T, path string) {
				subdir := filepath.Join(path, "subdir")
				require.NoError(t, os.MkdirAll(subdir, 0o755))
				testFile := filepath.Join(subdir, "test.txt")
				require.NoError(t, os.WriteFile(testFile, []byte("test"), 0o600))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			tt.setupFunc(t, tmpDir)

			err := recreateDir(tmpDir)
			require.NoError(t, err)
			// Verify directory exists and is empty.
			info, err := os.Stat(tmpDir)
			require.NoError(t, err)
			require.True(t, info.IsDir())

			// Check directory is empty.
			entries, err := os.ReadDir(tmpDir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestMaybeResolveHome(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name   string
		path   string
		expect string
	}{
		{
			name:   "tilde path",
			path:   "~/test/file.txt",
			expect: filepath.Join(homeDir, "test/file.txt"),
		},
		{
			name:   "absolute path",
			path:   "/absolute/path/file.txt",
			expect: "/absolute/path/file.txt",
		},
		{
			name:   "relative path",
			path:   "relative/path/file.txt",
			expect: "relative/path/file.txt",
		},
		{
			name:   "tilde only",
			path:   "~",
			expect: "~",
		},
		{
			name:   "tilde in middle",
			path:   "/path/~/file.txt",
			expect: "/path/~/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := maybeResolveHome(tt.path)
			require.Equal(t, tt.expect, home)
		})
	}
}
