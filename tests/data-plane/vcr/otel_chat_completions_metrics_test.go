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

	"github.com/stretchr/testify/require"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/tests/internal/testopenai"
)

// otelChatMetricsTestCase defines the expected behavior for each cassette.
type otelChatMetricsTestCase struct {
	cassette    testopenai.Cassette
	isStreaming bool // whether this is a streaming response.
	isError     bool // whether this is an error response.
}

// buildOtelChatMetricsTestCases returns all test cases with their expected behaviors.
func buildOtelChatMetricsTestCases() []otelChatMetricsTestCase {
	var cases []otelChatMetricsTestCase
	for _, cassette := range testopenai.ChatCassettes() {
		if strings.HasPrefix(cassette.String(), "azure-") {
			continue // Skip Azure as they are tested separately.
		}
		tc := otelChatMetricsTestCase{cassette: cassette}
		switch cassette {
		case testopenai.CassetteChatBadRequest, testopenai.CassetteChatUnknownModel, testopenai.CassetteChatNoMessages:
			tc.isError = true
		case testopenai.CassetteChatStreaming, testopenai.CassetteChatStreamingWebSearch, testopenai.CassetteChatStreamingDetailedUsage:
			tc.isStreaming = true
		}
		cases = append(cases, tc)
	}
	return cases
}

// TestOtelOpenAIChatCompletions_metrics tests that metrics are properly exported via OTLP for chat completion requests.
func TestOtelOpenAIChatCompletions_metrics(t *testing.T) {
	env := setupOtelTestEnvironment(t)
	listenerPort := env.listenerPort
	was5xx := false

	for _, tc := range buildOtelChatMetricsTestCases() {
		if was5xx {
			return // rather than also failing subsequent tests, which confuses root cause.
		}

		t.Run(tc.cassette.String(), func(t *testing.T) {
			// Send request.
			req, err := testopenai.NewRequest(t.Context(), fmt.Sprintf("http://localhost:%d", listenerPort), tc.cassette)
			require.NoError(t, err)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			failIf5xx(t, resp, &was5xx)

			// Always read the content.
			_, err = io.ReadAll(resp.Body)
			require.NoError(t, err)

			// Get the span to extract actual token counts and duration.
			span := env.collector.TakeSpan()
			require.NotNil(t, span)

			expectedCount := 2 // token usage + request duration
			if tc.isStreaming && !tc.isError {
				expectedCount = 4 // 2 + time to first token + time per output token
			}
			allMetrics := env.collector.TakeMetrics(expectedCount)
			metrics := requireScopeMetrics(t, allMetrics)

			// Get expected model names from span
			originalModel := getInvocationModel(span.Attributes, "llm.invocation_parameters")
			requestModel := originalModel // in non-override cases, these are the same
			responseModel := getSpanAttributeString(span.Attributes, "llm.model_name")

			verifyTokenUsageMetricsWithOriginal(t, "chat", metrics, span, originalModel, requestModel, responseModel, tc.isError)
			verifyRequestDurationMetricsWithOriginal(t, "chat", metrics, span, originalModel, requestModel, responseModel, tc.isError)
			if tc.isStreaming && !tc.isError {
				verifyTimeToFirstTokenMetrics(t, metrics, originalModel, requestModel, responseModel)
				verifyTimePerOutputTokenMetricsWithOriginal(t, metrics, span, originalModel, requestModel, responseModel)
			}
		})
	}
}

func TestOtelOpenAIChatCompletions_metrics_modelNameOverride(t *testing.T) {
	env := setupOtelTestEnvironment(t)
	listenerPort := env.listenerPort

	req, err := testopenai.NewRequest(t.Context(), fmt.Sprintf("http://localhost:%d", listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)
	// Set the x-test-backend which envoy.yaml routes to the openai-chat-override
	// backend in extproc.yaml. This backend overrides the model to gpt-5-nano.
	req.Header.Set("x-test-backend", "openai-chat-override")
	originalModel := "gpt-5"
	replaceRequestModel(t, req, originalModel)

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

	// Get expected model names from span
	// TODO: Until trace attribute recording is moved to the upstream filter,
	// llm.invocation_parameters is the original model, not the override.
	requestModel := "gpt-5-nano" // overridden model
	responseModel := getSpanAttributeString(span.Attributes, "llm.model_name")

	verifyTokenUsageMetricsWithProvider(t, "chat", "openai", "openai-chat-override", metrics, span, originalModel, requestModel, responseModel, false)
	verifyRequestDurationMetricsWithProvider(t, "chat", "openai", "openai-chat-override", metrics, span, originalModel, requestModel, responseModel, false)
}

// verifyTimeToFirstTokenMetrics verifies the gen_ai.server.time_to_first_token metric including original model attribute.
func verifyTimeToFirstTokenMetrics(t *testing.T, metrics *metricsv1.ScopeMetrics, originalModel, requestModel, responseModel string) {
	t.Helper()

	ttft := getMetricHistogramSum(metrics, "gen_ai.server.time_to_first_token")
	metricDurationSec := getMetricHistogramSum(metrics, "gen_ai.server.request.duration")
	require.Greater(t, ttft, 0.0)
	require.Less(t, ttft, metricDurationSec)

	expectedAttrs := map[string]string{
		"gen_ai.operation.name": "chat",
		"gen_ai.provider.name":  "openai",
		"gen_ai.backend":        "openai",
		"gen_ai.original.model": originalModel,
		"gen_ai.request.model":  requestModel,
		"gen_ai.response.model": responseModel,
	}
	verifyMetricAttributes(t, metrics, "gen_ai.server.time_to_first_token", expectedAttrs)
}

// verifyTimePerOutputTokenMetricsWithOriginal verifies the gen_ai.server.time_per_output_token metric including original model attribute.
func verifyTimePerOutputTokenMetricsWithOriginal(t *testing.T, metrics *metricsv1.ScopeMetrics, span *tracev1.Span, originalModel, requestModel, responseModel string) {
	t.Helper()

	outputTokens := getSpanAttributeInt(span.Attributes, "llm.token_count.completion")
	if outputTokens <= 0 {
		return // Skip if no output tokens.
	}

	tpot := getMetricHistogramSum(metrics, "gen_ai.server.time_per_output_token")
	metricDurationSec := getMetricHistogramSum(metrics, "gen_ai.server.request.duration")
	require.Greater(t, tpot, 0.0)
	require.Less(t, tpot, metricDurationSec)

	expectedAttrs := map[string]string{
		"gen_ai.operation.name": "chat",
		"gen_ai.provider.name":  "openai",
		"gen_ai.backend":        "openai",
		"gen_ai.original.model": originalModel,
		"gen_ai.request.model":  requestModel,
		"gen_ai.response.model": responseModel,
	}
	verifyMetricAttributes(t, metrics, "gen_ai.server.time_per_output_token", expectedAttrs)
}
