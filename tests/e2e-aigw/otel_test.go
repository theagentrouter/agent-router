// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2emcp

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/tests/internal/testopenai"
)

type otelTestEnvironment struct {
	listenerPort int
	collector    *testotel.OTLPCollector
}

func setupOtelTestEnvironment(t *testing.T) *otelTestEnvironment {
	t.Helper()

	internaltesting.ClearTestEnv(t)

	collector := testotel.StartOTLPCollector()
	t.Cleanup(collector.Close)

	buffers := internaltesting.DumpLogsOnFail(t, "testopenai")

	openAIServer, err := testopenai.NewServer(buffers[0], 0)
	require.NoError(t, err)
	t.Cleanup(openAIServer.Close)

	env := append(collector.Env(),
		fmt.Sprintf("OPENAI_BASE_URL=http://127.0.0.1:%d/v1", openAIServer.Port()),
		"OPENAI_API_KEY=unused",
	)

	startAIGWCLI(t, aigwBin, env, "run")

	return &otelTestEnvironment{
		listenerPort: 1975,
		collector:    collector,
	}
}

func TestAIGWRun_OTLPChatCompletions(t *testing.T) {
	env := setupOtelTestEnvironment(t)
	listenerPort := env.listenerPort

	req, err := testopenai.NewRequest(t.Context(), fmt.Sprintf("http://127.0.0.1:%d", listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode, "Response body: %s", string(body))

	// Get the span to extract actual token counts and duration.
	span := env.collector.TakeSpan()
	require.NotNil(t, span)

	expectedCount := 2 // token usage + request duration
	allMetrics := env.collector.TakeMetrics(expectedCount)
	metrics := requireScopeMetrics(t, allMetrics)

	originalModel := getInvocationModel(span.Attributes, "llm.invocation_parameters")
	responseModel := getSpanAttributeString(span.Attributes, "llm.model_name")

	verifyTokenUsageMetrics(t, "chat", span, metrics, originalModel, responseModel)
	verifyRequestDurationMetrics(t, "chat", span, metrics, originalModel, responseModel)
	verifyAccessLog(t, env.collector, originalModel)
}

func verifyTokenUsageMetrics(t *testing.T, op string, span *tracev1.Span, metrics *metricsv1.ScopeMetrics, originalModel, responseModel string) {
	t.Helper()

	inputTokens := getSpanAttributeInt(span.Attributes, "llm.token_count.prompt")
	outputTokens := getSpanAttributeInt(span.Attributes, "llm.token_count.completion")

	require.Equal(t, inputTokens, getMetricValueByAttribute(metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "input"))
	require.Equal(t, outputTokens, getMetricValueByAttribute(metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "output"))

	// Verify attributes for each token type data point
	for _, metric := range metrics.Metrics {
		if metric.Name == "gen_ai.client.token.usage" {
			histogram := metric.GetHistogram()
			for _, dp := range histogram.DataPoints {
				attrs := getAttributeStringMap(dp.Attributes)
				tokenType := attrs["gen_ai.token.type"]
				if tokenType == "input" || tokenType == "output" {
					expected := map[string]string{
						"gen_ai.operation.name": op,
						"gen_ai.provider.name":  "openai",
						"gen_ai.backend":        "default/openai",
						"gen_ai.original.model": originalModel,
						"gen_ai.request.model":  originalModel,
						"gen_ai.response.model": responseModel,
						"gen_ai.token.type":     tokenType,
					}
					require.Equal(t, expected, attrs)
				}
			}
			break
		}
	}
}

func verifyRequestDurationMetrics(t *testing.T, op string, span *tracev1.Span, metrics *metricsv1.ScopeMetrics, originalModel, responseModel string) {
	t.Helper()

	spanDurationSec := float64(span.EndTimeUnixNano-span.StartTimeUnixNano) / 1e9
	metricDurationSec := getMetricHistogramSum(metrics, "gen_ai.server.request.duration")
	require.Greater(t, metricDurationSec, 0.0)
	require.InDelta(t, spanDurationSec, metricDurationSec, 0.3)

	expectedAttrs := map[string]string{
		"gen_ai.operation.name": op,
		"gen_ai.provider.name":  "openai",
		"gen_ai.backend":        "default/openai",
		"gen_ai.original.model": originalModel,
		"gen_ai.request.model":  originalModel,
		"gen_ai.response.model": responseModel,
	}
	verifyMetricAttributes(t, metrics, "gen_ai.server.request.duration", expectedAttrs)
}

func getSpanAttributeInt(attrs []*commonv1.KeyValue, key string) int64 {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetIntValue()
		}
	}
	return 0
}

type invocationParameters struct {
	Model string `json:"model"`
}

// getRequestModelFromSpan extracts the request model from llm.invocation_parameters JSON.
func getInvocationModel(attrs []*commonv1.KeyValue, key string) string {
	invocationParams := getSpanAttributeString(attrs, key)
	var params invocationParameters
	_ = json.Unmarshal([]byte(invocationParams), &params)
	return params.Model
}

func getSpanAttributeString(attrs []*commonv1.KeyValue, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	return ""
}

func requireScopeMetrics(t *testing.T, allMetrics []*metricsv1.ResourceMetrics) *metricsv1.ScopeMetrics {
	t.Helper()

	// Combine all metrics from multiple batches into a single ScopeMetrics.
	// Metrics may be sent in multiple batches (e.g., TTFT recorded separately from final metrics).
	combined := &metricsv1.ScopeMetrics{
		Scope: &commonv1.InstrumentationScope{Name: "envoyproxy/ai-gateway"},
	}

	for _, rm := range allMetrics {
		for _, sm := range rm.ScopeMetrics {
			combined.Metrics = append(combined.Metrics, sm.Metrics...)
		}
	}

	require.NotEmpty(t, combined.Metrics, "no metrics found")
	return combined
}

func getMetricValueByAttribute(metrics *metricsv1.ScopeMetrics, metricName string, attrKey string, attrValue string) int64 {
	for _, metric := range metrics.Metrics {
		if metric.Name == metricName {
			histogram := metric.GetHistogram()
			if histogram != nil {
				for _, dp := range histogram.DataPoints {
					for _, attr := range dp.Attributes {
						if attr.Key == attrKey && attr.Value.GetStringValue() == attrValue {
							return int64(dp.GetSum())
						}
					}
				}
			}
		}
	}
	return 0
}

func getMetricHistogramSum(metrics *metricsv1.ScopeMetrics, metricName string) float64 {
	for _, metric := range metrics.Metrics {
		if metric.Name == metricName {
			histogram := metric.GetHistogram()
			if histogram != nil && len(histogram.DataPoints) > 0 {
				return histogram.DataPoints[0].GetSum()
			}
		}
	}
	return 0
}

// verifyMetricAttributes verifies that a metric has exactly the expected string attributes.
func verifyMetricAttributes(t *testing.T, metrics *metricsv1.ScopeMetrics, metricName string, expectedAttrs map[string]string) {
	t.Helper()

	for _, metric := range metrics.Metrics {
		if metric.Name == metricName {
			histogram := metric.GetHistogram()
			require.NotNil(t, histogram)
			require.NotEmpty(t, histogram.DataPoints)

			for _, dp := range histogram.DataPoints {
				attrs := getAttributeStringMap(dp.Attributes)
				require.Equal(t, expectedAttrs, attrs)
			}
			return
		}
	}
	t.Fatalf("%s metric not found", metricName)
}

// getAttributeStringMap returns a map of only the string-valued attributes.
func getAttributeStringMap(attrs []*commonv1.KeyValue) map[string]string {
	m := make(map[string]string)
	for _, attr := range attrs {
		if sv := attr.Value.GetStringValue(); sv != "" {
			m[attr.Key] = sv
		}
	}
	return m
}

// verifyAccessLog verifies that OTLP access logs are received with expected LLM attributes.
func verifyAccessLog(t *testing.T, collector *testotel.OTLPCollector, expectedModel string) {
	t.Helper()

	resourceLogs := collector.TakeLog()
	require.NotNil(t, resourceLogs, "expected access log to be received")
	require.NotEmpty(t, resourceLogs.ScopeLogs, "expected scope logs")

	scopeLogs := resourceLogs.ScopeLogs[0]
	require.NotEmpty(t, scopeLogs.LogRecords, "expected log records")

	logRecord := scopeLogs.LogRecords[0]

	// Verify log body (text format) contains expected path.
	logBody := logRecord.Body.GetStringValue()
	require.Contains(t, logBody, "/v1/chat/completions", "log body should contain request path")
	require.Contains(t, logBody, "model="+expectedModel, "log body should contain model")

	// Verify log attributes (JSON fields) contain expected LLM fields.
	attrs := getLogAttributeMap(logRecord.Attributes)
	require.Equal(t, expectedModel, attrs["gen_ai.request.model"], "gen_ai.request.model attribute should match")
	require.NotEmpty(t, attrs["gen_ai.provider.name"], "gen_ai.provider.name should be present")
	require.NotEmpty(t, attrs["response_code"], "response_code should be present")
	require.Equal(t, "200", attrs["response_code"], "response_code should be 200")
}

// getLogAttributeMap returns a map of log attribute key-value pairs.
func getLogAttributeMap(attrs []*commonv1.KeyValue) map[string]string {
	m := make(map[string]string)
	for _, attr := range attrs {
		m[attr.Key] = attr.Value.GetStringValue()
	}
	return m
}
