// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"testing"

	"github.com/stretchr/testify/require"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// This file pins the MCP span attribute names emitted by mcp_otel.go against the
// versioned semconv package. Production code hardcodes the names so a dependency
// bump cannot silently rename what we emit and break users' dashboards; the
// literals below mirror those, so an upstream rename surfaces here as a failure
// that names the change. This mirrors otelgenai/semconv_vocabulary_test.go for
// the LLM-endpoint attributes.
//
// Only the OTel vocabulary is pinned here. The legacy vocabulary predates these
// conventions and is pinned by mcp_legacy_test.go against its own frozen names.

// TestMCPAttributeNames_matchSemconv pins every MCP attribute name we emit that
// has a semconv equivalent. The left column is the literal emitted by mcp_otel.go.
func TestMCPAttributeNames_matchSemconv(t *testing.T) {
	tests := []struct {
		ours     string
		expected string
	}{
		{ours: "mcp.method.name", expected: string(semconv.McpMethodNameKey)},
		{ours: "mcp.protocol.version", expected: string(semconv.McpProtocolVersionKey)},
		{ours: "mcp.session.id", expected: string(semconv.McpSessionIDKey)},
		{ours: "mcp.resource.uri", expected: string(semconv.McpResourceURIKey)},
		{ours: "jsonrpc.request.id", expected: string(semconv.JSONRPCRequestIDKey)},

		{ours: "gen_ai.operation.name", expected: string(semconv.GenAIOperationNameKey)},
		{ours: "gen_ai.tool.name", expected: string(semconv.GenAIToolNameKey)},
		{ours: "gen_ai.prompt.name", expected: string(semconv.GenAIPromptNameKey)},
		// Tool call content added for the MCP path. Both are spec attributes, so
		// they are pinned rather than invented.
		{ours: "gen_ai.tool.call.arguments", expected: string(semconv.GenAIToolCallArgumentsKey)},
		{ours: "gen_ai.tool.call.result", expected: string(semconv.GenAIToolCallResultKey)},

		{ours: "error.type", expected: string(semconv.ErrorTypeKey)},
		{ours: "rpc.response.status_code", expected: string(semconv.RPCResponseStatusCodeKey)},

		{ours: "network.transport", expected: string(semconv.NetworkTransportKey)},
		{ours: "network.protocol.name", expected: string(semconv.NetworkProtocolNameKey)},
		{ours: "network.protocol.version", expected: string(semconv.NetworkProtocolVersionKey)},
	}

	for _, tc := range tests {
		t.Run(tc.ours, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.ours)
		})
	}
}

// mcpCustomAttributes are the MCP span attributes we emit that have no semconv
// equivalent in the pinned version. They are enumerated here so the full emitted
// vocabulary is auditable in one place and a new custom key is a deliberate
// addition to this list rather than an unguarded string. The list count and
// resource_templates keys are the aggregated list sizes; the tool call
// arguments/result keys above are the only content attributes and are gated by
// the message-content opt-in.
var mcpCustomAttributes = []string{
	// Aggregated list result sizes (no semconv MCP count attribute exists).
	"mcp.tools.count",
	"mcp.resources.count",
	"mcp.resource_templates.count",
	"mcp.prompts.count",

	// Client handshake, logging, notifications and completion arguments.
	"mcp.client.name",
	"mcp.client.title",
	"mcp.client.version",
	"mcp.logging.level",
	"mcp.notifications.progress",
	"mcp.notifications.progress.token",
	"mcp.notifications.progress.message",
	"mcp.complete.argument.name",
	"mcp.complete.argument.value",

	// Gateway-specific "route to backend" event attributes.
	"mcp.backend.name",
	"mcp.session.new",
}

// TestMCPCustomAttributeNames documents the custom attributes and guards against
// a semconv version accidentally defining one of them, at which point we should
// switch to pinning it in TestMCPAttributeNames_matchSemconv instead.
func TestMCPCustomAttributeNames(t *testing.T) {
	semconvKeys := map[string]struct{}{
		string(semconv.McpMethodNameKey):          {},
		string(semconv.McpProtocolVersionKey):     {},
		string(semconv.McpSessionIDKey):           {},
		string(semconv.McpResourceURIKey):         {},
		string(semconv.GenAIToolCallArgumentsKey): {},
		string(semconv.GenAIToolCallResultKey):    {},
	}
	for _, name := range mcpCustomAttributes {
		t.Run(name, func(t *testing.T) {
			_, isSemconv := semconvKeys[name]
			require.False(t, isSemconv, "%s now has a semconv equivalent; pin it instead of listing it as custom", name)
		})
	}
}
