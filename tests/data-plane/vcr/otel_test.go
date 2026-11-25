// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package vcr

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// otelTestEnvironment holds all the services needed for OTEL tests.
type otelTestEnvironment struct {
	listenerPort int
	collector    *testotel.OTLPCollector
}

// setupOtelTestEnvironment starts all required services and returns ports and a closer.
func setupOtelTestEnvironment(t *testing.T, extraExtProcEnv ...string) *otelTestEnvironment {
	internaltesting.ClearTestEnv(t)

	collector := testotel.StartOTLPCollector()
	t.Cleanup(collector.Close)

	extprocEnv := append(collector.Env(), extraExtProcEnv...)

	port := startTestEnvironment(t, extprocEnv)

	return &otelTestEnvironment{
		listenerPort: port.EnvoyListenerPort(),
		collector:    collector,
	}
}

// failIf5xx because 5xx errors are likely a sign of a broken ExtProc or Envoy.
func failIf5xx(t *testing.T, resp *http.Response, was5xx *bool) {
	if resp.StatusCode >= 500 && resp.StatusCode < 600 {
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		*was5xx = true
		t.Fatalf("received %d response with body: %s", resp.StatusCode, string(body))
	}
}

// verifyTokenUsageMetricsWithOriginal verifies token usage metrics including original model attribute
func verifyTokenUsageMetricsWithOriginal(t *testing.T, op string, metrics *metricsv1.ScopeMetrics, span *tracev1.Span, originalModel, requestModel, responseModel string, isError bool) {
	verifyTokenUsageMetricsWithProvider(t, op, "openai", "openai", metrics, span, originalModel, requestModel, responseModel, isError)
}

// verifyTokenUsageMetricsWithProvider verifies token usage metrics including original model attribute and provider name
func verifyTokenUsageMetricsWithProvider(t *testing.T, op string, provider, backend string, metrics *metricsv1.ScopeMetrics, span *tracev1.Span, originalModel, requestModel, responseModel string, isError bool) {
	t.Helper()
	if isError {
		return // Token usage metrics are not verified for error cases.
	}

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
						"gen_ai.provider.name":  provider,
						"gen_ai.backend":        backend,
						"gen_ai.original.model": originalModel,
						"gen_ai.request.model":  requestModel,
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

// verifyRequestDurationMetricsWithOriginal verifies request duration metrics including original model attribute
func verifyRequestDurationMetricsWithOriginal(t *testing.T, op string, metrics *metricsv1.ScopeMetrics, span *tracev1.Span, originalModel, requestModel, responseModel string, isError bool) {
	verifyRequestDurationMetricsWithProvider(t, op, "openai", "openai", metrics, span, originalModel, requestModel, responseModel, isError)
}

// verifyRequestDurationMetricsWithProvider verifies request duration metrics including original model attribute and provider name
func verifyRequestDurationMetricsWithProvider(t *testing.T, op string, provider, backend string, metrics *metricsv1.ScopeMetrics, span *tracev1.Span, originalModel, requestModel, responseModel string, isError bool) {
	t.Helper()

	spanDurationSec := float64(span.EndTimeUnixNano-span.StartTimeUnixNano) / 1e9
	metricDurationSec := getMetricHistogramSum(metrics, "gen_ai.server.request.duration")
	require.Greater(t, metricDurationSec, 0.0)
	require.InDelta(t, spanDurationSec, metricDurationSec, 0.3)

	// For error cases, don't validate response model since we don't get one from the backend
	if isError {
		// Just verify the error type is present
		for _, metric := range metrics.Metrics {
			if metric.Name == "gen_ai.server.request.duration" {
				histogram := metric.GetHistogram()
				require.NotNil(t, histogram)
				require.NotEmpty(t, histogram.DataPoints)
				for _, dp := range histogram.DataPoints {
					attrs := getAttributeStringMap(dp.Attributes)
					expected := map[string]string{
						"error.type":            "_OTHER", // we don't set specific error types yet
						"gen_ai.operation.name": op,
						"gen_ai.provider.name":  provider,
						"gen_ai.backend":        backend,
						"gen_ai.request.model":  requestModel,
						"gen_ai.original.model": originalModel,
						"gen_ai.response.model": attrs["gen_ai.response.model"],
					}
					require.Equal(t, expected, attrs)
				}
				return
			}
		}
		t.Fatalf("gen_ai.server.request.duration metric not found")
		return
	}

	expectedAttrs := map[string]string{
		"gen_ai.operation.name": op,
		"gen_ai.provider.name":  provider,
		"gen_ai.backend":        backend,
		"gen_ai.original.model": originalModel,
		"gen_ai.request.model":  requestModel,
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

func replaceRequestModel(t *testing.T, req *http.Request, requestModel string) {
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	modifiedBody, err := sjson.SetBytes(body, "model", requestModel)
	require.NoError(t, err)

	req.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	req.ContentLength = int64(len(modifiedBody))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBody)))
}
