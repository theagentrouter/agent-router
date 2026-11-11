// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package vcr

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/testopenai"
)

// TestPrometheusMetrics verifies that metrics are properly exported via Prometheus
// when processing a chat completion request. This test uses the default configuration
// which exposes metrics on the /metrics endpoint. This doesn't independently test
// embeddings as that's redundant to the otel metrics tests.
func TestPrometheusMetrics(t *testing.T) {
	env := startTestEnvironment(t, nil)
	listenerPort := env.EnvoyListenerPort()
	adminPort := env.ExtProcAdminPort()

	// Send the chat request.
	req, err := testopenai.NewRequest(t.Context(), fmt.Sprintf("http://localhost:%d", listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Always read the content.
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Wait for metrics to be available with exponential backoff.
	// Parse the Prometheus metrics.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	var metricFamilies map[string]*dto.MetricFamily
	require.Eventually(t, func() bool {
		metricsReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://localhost:%d/metrics", adminPort), nil)
		require.NoError(t, err)

		metricsResp, err := http.DefaultClient.Do(metricsReq)
		require.NoError(t, err)
		defer func() { _ = metricsResp.Body.Close() }()

		metricsBody, err := io.ReadAll(metricsResp.Body)
		require.NoError(t, err)

		metricFamilies, err = parser.TextToMetricFamilies(strings.NewReader(string(metricsBody)))
		require.NoError(t, err)

		// Just check if we have the metrics we need
		return metricFamilies["gen_ai_server_request_duration_seconds"] != nil &&
			metricFamilies["gen_ai_client_token_usage"] != nil
	}, 3*time.Second, 100*time.Millisecond)

	requestModel := "gpt-5-nano"
	verifyPrometheusRequestDuration(t, metricFamilies["gen_ai_server_request_duration_seconds"], requestModel)
	verifyPrometheusTokenUsage(t, metricFamilies["gen_ai_client_token_usage"], requestModel)
}

// verifyPrometheusRequestDuration verifies the request duration metric has the expected labels and values.
func verifyPrometheusRequestDuration(t *testing.T, metric *dto.MetricFamily, expectedRequestModel string) {
	t.Helper()
	require.NotNil(t, metric)
	require.Len(t, metric.Metric, 1)

	m := metric.Metric[0]

	// Convert labels to map
	labels := make(map[string]string)
	for _, label := range m.Label {
		labels[*label.Name] = *label.Value
	}

	expectedLabels := map[string]string{
		"gen_ai_operation_name": "chat",
		"gen_ai_provider_name":  "openai",
		"gen_ai_backend":        "openai",
		"gen_ai_original_model": expectedRequestModel, // For non-override cases, original equals request
		"gen_ai_request_model":  expectedRequestModel,
		"gen_ai_response_model": "gpt-5-nano-2025-08-07",
		"otel_scope_name":       "envoyproxy/ai-gateway",
		"otel_scope_schema_url": "",
		"otel_scope_version":    "",
	}
	require.Equal(t, expectedLabels, labels)

	// Verify it's a histogram with data
	require.NotNil(t, m.Histogram)
	require.Equal(t, uint64(1), *m.Histogram.SampleCount)
	require.Greater(t, *m.Histogram.SampleSum, float64(0))
}

// verifyPrometheusTokenUsage verifies both input and output token usage metrics exist with correct labels.
func verifyPrometheusTokenUsage(t *testing.T, metric *dto.MetricFamily, expectedModel string) {
	t.Helper()
	require.NotNil(t, metric)
	require.Len(t, metric.Metric, 5)
	var inputMetric, cachedInputMetric, cacheCreationInputMetric, outputMetric, reasoningMetric *dto.Metric
	for _, m := range metric.Metric {
		for _, label := range m.Label {
			if *label.Name == "gen_ai_token_type" {
				switch *label.Value {
				case "input":
					inputMetric = m
				case "cached_input":
					cachedInputMetric = m
				case "cache_creation_input":
					cacheCreationInputMetric = m
				case "output":
					outputMetric = m
				case "reasoning":
					reasoningMetric = m
				}
				break
			}
		}
	}
	require.NotNil(t, inputMetric, "Input metric not found")
	require.NotNil(t, cachedInputMetric, "Cached Input metric not found")
	require.NotNil(t, cacheCreationInputMetric, "Cache Creation Input metric not found")
	require.NotNil(t, outputMetric, "Output metric not found")
	require.NotNil(t, reasoningMetric, "Reasoning metric not found")

	type testCase struct {
		metric      *dto.Metric
		tokenType   string
		expectedSum float64
	}

	cases := []testCase{
		{inputMetric, "input", 8},
		{cachedInputMetric, "cached_input", 0},
		{cacheCreationInputMetric, "cache_creation_input", 0},
		{outputMetric, "output", 377},
		{reasoningMetric, "reasoning", 320},
	}

	for _, tc := range cases {
		// Convert labels to map
		labels := make(map[string]string)
		for _, label := range tc.metric.Label {
			labels[*label.Name] = *label.Value
		}

		expectedLabels := map[string]string{
			"gen_ai_operation_name": "chat",
			"gen_ai_provider_name":  "openai",
			"gen_ai_backend":        "openai",
			"gen_ai_original_model": expectedModel, // For non-override cases, original equals request
			"gen_ai_request_model":  expectedModel,
			"gen_ai_response_model": "gpt-5-nano-2025-08-07",
			"gen_ai_token_type":     tc.tokenType,
			"otel_scope_name":       "envoyproxy/ai-gateway",
			"otel_scope_schema_url": "",
			"otel_scope_version":    "",
		}
		require.Equal(t, expectedLabels, labels)

		// Verify histogram
		require.NotNil(t, tc.metric.Histogram)
		require.Equal(t, uint64(1), *tc.metric.Histogram.SampleCount)
		require.Equal(t, tc.expectedSum, *tc.metric.Histogram.SampleSum)
	}
}
