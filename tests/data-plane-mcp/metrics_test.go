// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplanemcp

import (
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// TestMCPMetrics is a dedicated test for comprehensive metrics assertions.
// It exercises various MCP operations and verifies the expected metrics are
// recorded on the extproc /metrics endpoint.
func TestMCPMetrics(t *testing.T) {
	env := requireNewMCPEnv(t, false, 1200*time.Second, defaultMCPPath)

	// Run a set of operations to generate metrics.
	s := env.newSession(t)
	// tools/list
	_, err := s.session.ListTools(t.Context(), &mcp.ListToolsParams{})
	require.NoError(t, err)
	requireMCPSpan(t, env.collector.TakeSpan(), "ListTools", map[string]string{
		"mcp.method.name": "tools/list",
	})

	// tools/call (success)
	_, err = s.session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      defaultMCPBackendResourcePrefix + testmcp.ToolEcho.Tool.Name,
		Arguments: testmcp.ToolEchoArgs{Text: "metric test"},
	})
	require.NoError(t, err)
	_ = env.collector.TakeSpan()

	// tools/call (error tool)
	_, err = s.session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      defaultMCPBackendResourcePrefix + testmcp.ToolError.Tool.Name,
		Arguments: testmcp.ToolErrorArgs{Error: "metric error"},
	})
	require.NoError(t, err)
	_ = env.collector.TakeSpan()

	// resources/list
	_, err = s.session.ListResources(t.Context(), &mcp.ListResourcesParams{})
	require.NoError(t, err)
	_ = env.collector.TakeSpan()

	// resources/read
	_, err = s.session.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: defaultMCPBackendResourceURIPrefix + "file:///dummy.txt",
	})
	require.NoError(t, err)
	_ = env.collector.TakeSpan()

	// prompts/list
	_, err = s.session.ListPrompts(t.Context(), &mcp.ListPromptsParams{})
	require.NoError(t, err)
	_ = env.collector.TakeSpan()

	// --- Assert metrics ---

	t.Run("mcp_method_count_total/tools_list", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "tools/list",
		}, 0)
	})

	t.Run("mcp_method_count_total/tools_call", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "tools/call",
		}, 0)
	})

	t.Run("mcp_method_count_total/resources_list", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "resources/list",
		}, 0)
	})

	t.Run("mcp_method_count_total/resources_read", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "resources/read",
		}, 0)
	})

	t.Run("mcp_method_count_total/prompts_list", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "prompts/list",
		}, 0)
	})

	t.Run("mcp_method_count_total/initialize", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "initialize",
		}, 0)
	})

	t.Run("mcp_method_count_total/notifications_initialized", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "notifications/initialized",
		}, 0)
	})

	t.Run("mcp_capabilities_negotiated_total/server_tools", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_capabilities_negotiated_total", map[string]string{
			"capability_type": "tools",
			"capability_side": "server",
		}, 0)
	})

	t.Run("mcp_capabilities_negotiated_total/server_resources", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_capabilities_negotiated_total", map[string]string{
			"capability_type": "resources",
			"capability_side": "server",
		}, 0)
	})

	t.Run("mcp_capabilities_negotiated_total/server_prompts", func(t *testing.T) {
		requireMetricGreaterThan(t, env, "mcp_capabilities_negotiated_total", map[string]string{
			"capability_type": "prompts",
			"capability_side": "server",
		}, 0)
	})

	t.Run("mcp_request_duration/success", func(t *testing.T) {
		// The request duration histogram should have observations.
		// OTel prometheus exporter names this mcp_request_duration (no unit suffix).
		require.Eventually(t, func() bool {
			metrics, err := retrieveMetrics(env.extProcMetricsURL, retrieveMetricsTime)
			if err != nil {
				t.Log("failed to retrieve metrics:", err)
				return false
			}
			family := findHistogramFamily(metrics, "mcp_request_duration")
			if family == nil {
				t.Log("mcp_request_duration histogram not found")
				return false
			}
			for _, m := range family.Metric {
				if m.GetHistogram() != nil && m.GetHistogram().GetSampleCount() > 0 {
					return true
				}
			}
			return false
		}, retrieveMetricsTime, retrieveMetricsTick)
	})

	t.Run("mcp_initialization_duration", func(t *testing.T) {
		require.Eventually(t, func() bool {
			metrics, err := retrieveMetrics(env.extProcMetricsURL, retrieveMetricsTime)
			if err != nil {
				t.Log("failed to retrieve metrics:", err)
				return false
			}
			family := findHistogramFamily(metrics, "mcp_initialization_duration")
			if family == nil {
				t.Log("mcp_initialization_duration histogram not found")
				return false
			}
			for _, m := range family.Metric {
				if m.GetHistogram() != nil && m.GetHistogram().GetSampleCount() > 0 {
					return true
				}
			}
			return false
		}, retrieveMetricsTime, retrieveMetricsTick)
	})

	t.Run("mcp_method_count_total/per_backend", func(t *testing.T) {
		// Verify that per-backend metrics are recorded (the backend attribute is set).
		requireMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "tools/call",
			"mcp_backend":     "default-mcp-backend",
		}, 0)
	})
}

// TestModernMCPMetrics is a dedicated metrics test for the modern (2026-07-28) path.
func TestModernMCPMetrics(t *testing.T) {
	env := requireNewModernMCPEnv(t, 1200*time.Second, defaultMCPPath)
	cli := env.modernCli

	// Generate metrics via modern operations.
	cli.serverDiscover(t)
	cli.listTools(t)
	cli.callTool(t, defaultMCPBackendResourcePrefix+testmcp.ToolEcho.Tool.Name, testmcp.ToolEchoArgs{Text: "metric"})
	cli.listResources(t)
	cli.listPrompts(t)

	t.Run("mcp_method_count_total/server_discover", func(t *testing.T) {
		requireModernMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "server/discover",
		}, 0)
	})

	t.Run("mcp_method_count_total/tools_list", func(t *testing.T) {
		requireModernMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "tools/list",
		}, 0)
	})

	t.Run("mcp_method_count_total/tools_call", func(t *testing.T) {
		requireModernMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "tools/call",
		}, 0)
	})

	t.Run("mcp_method_count_total/resources_list", func(t *testing.T) {
		requireModernMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "resources/list",
		}, 0)
	})

	t.Run("mcp_method_count_total/prompts_list", func(t *testing.T) {
		requireModernMetricGreaterThan(t, env, "mcp_method_count_total", map[string]string{
			"mcp_method_name": "prompts/list",
		}, 0)
	})

	t.Run("mcp_request_duration/modern", func(t *testing.T) {
		require.Eventually(t, func() bool {
			metrics, err := retrieveMetrics(env.extProcMetricsURL, retrieveMetricsTime)
			if err != nil {
				t.Log("failed to retrieve metrics:", err)
				return false
			}
			family := findHistogramFamily(metrics, "mcp_request_duration")
			if family == nil {
				return false
			}
			for _, m := range family.Metric {
				if m.GetHistogram() != nil && m.GetHistogram().GetSampleCount() > 0 {
					return true
				}
			}
			return false
		}, retrieveMetricsTime, retrieveMetricsTick)
	})
}

// requireModernMetricGreaterThan is like requireMetricGreaterThan but for modernMCPEnv.
func requireModernMetricGreaterThan(t *testing.T, m *modernMCPEnv, metricName string, metricLabels map[string]string, prev float64) {
	t.Helper()
	require.Eventually(t, func() bool {
		current, err := getCounterMetricByNameLabels(m.extProcMetricsURL, retrieveMetricsTime, metricName, metricLabels)
		if err != nil {
			t.Log("failed to get metric:", err)
			return false
		}
		return current > prev
	}, retrieveMetricsTime, retrieveMetricsTick)
}

// findHistogramFamily finds a histogram metric family by exact name or common
// OTel prometheus suffixes (_seconds, _token, etc.).
func findHistogramFamily(metrics map[string]*dto.MetricFamily, prefix string) *dto.MetricFamily {
	if family, ok := metrics[prefix]; ok {
		return family
	}
	for name, family := range metrics {
		if strings.HasPrefix(name, prefix) && family.GetType() == dto.MetricType_HISTOGRAM {
			return family
		}
	}
	return nil
}
