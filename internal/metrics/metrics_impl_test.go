// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestNewProcessorMetrics(t *testing.T) {
	t.Parallel()
	var (
		mr    = metric.NewManualReader()
		meter = metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm    = NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)
	)

	assert.NotNil(t, pm)
	assert.False(t, pm.firstTokenSent)
}

func TestStartRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Helper()
		var (
			mr    = metric.NewManualReader()
			meter = metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
			pm    = NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)
		)

		before := time.Now()
		pm.StartRequest(nil)
		after := time.Now()

		assert.False(t, pm.firstTokenSent)
		assert.Equal(t, before, pm.requestStart)
		assert.Equal(t, after, pm.requestStart)
	})
}

func TestRecordTokenUsage(t *testing.T) {
	t.Parallel()
	var (
		mr    = metric.NewManualReader()
		meter = metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm    = NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs = []attribute.KeyValue{
			// gen_ai.operation.name - https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#common-attributes
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			// gen_ai.provider.name - https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#common-attributes
			attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
			// gen_ai.original.model - the original model from the request
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			// gen_ai.request.model - https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#common-attributes
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		}
		// gen_ai.token.type values - https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/#common-attributes
		inputAttrs              = attribute.NewSet(append(attrs, attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeInput))...)
		outputAttrs             = attribute.NewSet(append(attrs, attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeOutput))...)
		cachedInputAttrs        = attribute.NewSet(append(attrs, attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeCachedInput))...)
		cacheCreationInputAttrs = attribute.NewSet(append(attrs, attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeCacheCreationInput))...)
	)

	pm.SetOriginalModel("test-model")
	pm.SetRequestModel("test-model")
	pm.SetResponseModel("test-model")
	pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})
	pm.RecordTokenUsage(t.Context(), TokenUsage{
		inputTokens: 10, cachedInputTokens: 8, cacheCreationInputTokens: 2, outputTokens: 5,
		inputTokenSet: true, cachedInputTokenSet: true, cacheCreationInputTokenSet: true, outputTokenSet: true,
	}, nil)

	count, sum := testotel.GetHistogramValues(t, mr, genaiMetricClientTokenUsage, inputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 10.0, sum)

	count, sum = testotel.GetHistogramValues(t, mr, genaiMetricClientTokenUsage, cachedInputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 8.0, sum)

	count, sum = testotel.GetHistogramValues(t, mr, genaiMetricClientTokenUsage, cacheCreationInputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 2.0, sum)

	count, sum = testotel.GetHistogramValues(t, mr, genaiMetricClientTokenUsage, outputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 5.0, sum)
}

func TestRecordTokenLatency(t *testing.T) {
	synctest.Test(t, testRecordTokenLatency)
}

func testRecordTokenLatency(t *testing.T) {
	t.Helper()

	var (
		mr    = metric.NewManualReader()
		meter = metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm    = NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)
		attrs = attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderAWSBedrock),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)
	)

	pm.StartRequest(nil)
	pm.SetOriginalModel("test-model")
	pm.SetRequestModel("test-model")
	pm.SetResponseModel("test-model")
	pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}})

	time.Sleep(10 * time.Millisecond)
	pm.RecordTokenLatency(t.Context(), 1, false, nil)
	assert.True(t, pm.firstTokenSent)
	count, sum := testotel.GetHistogramValues(t, mr, genaiMetricServerTimeToFirstToken, attrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 10*time.Millisecond.Seconds(), sum)

	time.Sleep(10 * time.Millisecond)
	pm.RecordTokenLatency(t.Context(), 5, true, nil)
	count, sum = getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, (20*time.Millisecond-10*time.Millisecond).Seconds()/4, sum)

	time.Sleep(10 * time.Millisecond)
	pm.RecordTokenLatency(t.Context(), 6, false, nil)
	count, sum = getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, (20*time.Millisecond-10*time.Millisecond).Seconds()/4, sum)
}

func TestRecordRequestCompletion(t *testing.T) {
	synctest.Test(t, testRecordRequestCompletion)
}

func testRecordRequestCompletion(t *testing.T) {
	t.Helper()

	var (
		mr    = metric.NewManualReader()
		meter = metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm    = NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)
		attrs = []attribute.KeyValue{
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String("custom"),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String("custom"),
		}
		attrsSuccess = attribute.NewSet(attrs...)
		attrsFailure = attribute.NewSet(append(attrs, attribute.Key(genaiAttributeErrorType).String(genaiErrorTypeFallback))...)
	)

	pm.StartRequest(nil)
	pm.SetOriginalModel("test-model")
	pm.SetRequestModel("test-model")
	pm.SetResponseModel("test-model")
	pm.SetBackend(&filterapi.Backend{Name: "custom"})

	time.Sleep(10 * time.Millisecond)
	pm.RecordRequestCompletion(t.Context(), true, nil)
	count, sum := testotel.GetHistogramValues(t, mr, genaiMetricServerRequestDuration, attrsSuccess)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 10*time.Millisecond.Seconds(), sum)

	pm.RecordRequestCompletion(t.Context(), false, nil)
	pm.RecordRequestCompletion(t.Context(), false, nil)
	count, sum = testotel.GetHistogramValues(t, mr, genaiMetricServerRequestDuration, attrsFailure)
	assert.Equal(t, uint64(2), count)
	assert.Equal(t, 2*10*time.Millisecond.Seconds(), sum)
}

func TestGetTimeToFirstTokenMsAndGetInterTokenLatencyMs(t *testing.T) {
	t.Parallel()
	c := metricsImpl{timeToFirstToken: 1 * time.Second, interTokenLatencySec: 2}
	assert.Equal(t, 1000.0, c.GetTimeToFirstTokenMs())
	assert.Equal(t, 2000.0, c.GetInterTokenLatencyMs())
}

func TestHeaderLabelMapping(t *testing.T) {
	t.Parallel()
	var (
		mr    = metric.NewManualReader()
		meter = metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

		// Test header label mapping.
		headerMapping = map[string]string{
			"x-tenant-id": "tenant.id",
			"x-org-id":    "org_id",
		}

		pm = NewMetricsFactory(meter, headerMapping, GenAIOperationCompletion).NewMetrics().(*metricsImpl)
	)

	// Test with headers that should be mapped.
	requestHeaders := map[string]string{
		"x-tenant-id": "user123",
		"x-org-id":    "org456",
		"x-other":     "ignored", // This should be ignored as it's not in the mapping.
	}

	pm.SetOriginalModel("test-model")
	pm.SetRequestModel("test-model")
	pm.SetResponseModel("test-model")
	pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})
	pm.RecordTokenUsage(t.Context(), TokenUsage{
		inputTokens: 10, outputTokens: 8, totalTokens: 5,
		inputTokenSet: true, outputTokenSet: true, totalTokenSet: true,
	}, requestHeaders)

	// Verify that the header mapping is set correctly.
	assert.Equal(t, headerMapping, pm.requestHeaderAttributeMapping)

	// Verify that the metrics are recorded with the mapped header attributes.
	attrs := attribute.NewSet(
		attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
		attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
		attribute.Key(genaiAttributeOriginalModel).String("test-model"),
		attribute.Key(genaiAttributeRequestModel).String("test-model"),
		attribute.Key(genaiAttributeResponseModel).String("test-model"),
		attribute.Key(genaiAttributeBackend).String(""),
		attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeInput),
		attribute.Key("tenant.id").String("user123"),
		attribute.Key("org_id").String("org456"),
	)

	count, _ := testotel.GetHistogramValues(t, mr, genaiMetricClientTokenUsage, attrs)
	assert.Equal(t, uint64(1), count)
}

// TestModelNameHeaderKey tests that the model used in metrics is taken from
// the internalapi.ModelNameHeaderKey when present, which allows backend-specific
// model overrides to be tracked in metrics.
func TestModelNameHeaderKey(t *testing.T) {
	t.Parallel()
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
	pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

	// Simulate headers with model override
	headers := map[string]string{
		internalapi.ModelNameHeaderKeyDefault: "backend-specific-model",
	}

	pm.StartRequest(headers)
	pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}})

	// Simulate the original model from request body before override
	pm.SetOriginalModel("llama3-2-1b")
	// This simulates the processor setting the model from the header
	pm.SetRequestModel("backend-specific-model")
	// Response model is what the backend actually used
	pm.SetResponseModel("us.meta.llama3-2-1b-instruct-v1:0")

	pm.RecordTokenUsage(t.Context(), TokenUsage{
		inputTokens: 10, outputTokens: 8, totalTokens: 5,
		inputTokenSet: true, outputTokenSet: true, totalTokenSet: true,
	}, headers)

	// Verify metrics use the overridden request model and original model
	inputAttrs := attribute.NewSet(
		attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
		attribute.Key(genaiAttributeProviderName).String(genaiProviderAWSBedrock),
		attribute.Key(genaiAttributeOriginalModel).String("llama3-2-1b"),
		attribute.Key(genaiAttributeRequestModel).String("backend-specific-model"),
		attribute.Key(genaiAttributeResponseModel).String("us.meta.llama3-2-1b-instruct-v1:0"),
		attribute.Key(genaiAttributeBackend).String(""),
		attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeInput),
	)
	count, sum := getHistogramValues(t, mr, genaiMetricClientTokenUsage, inputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 10.0, sum)
}

func TestLabels_SetModel_RequestAndResponseDiffer(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
	pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

	pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})
	pm.SetOriginalModel("orig-model")
	pm.SetRequestModel("req-model")
	pm.SetResponseModel("res-model")
	pm.RecordTokenUsage(t.Context(), TokenUsage{
		inputTokens: 2, cachedInputTokens: 1, cacheCreationInputTokens: 6, outputTokens: 3,
		inputTokenSet: true, cachedInputTokenSet: true, cacheCreationInputTokenSet: true, outputTokenSet: true,
	}, nil)

	inputAttrs := attribute.NewSet(
		attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
		attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
		attribute.Key(genaiAttributeOriginalModel).String("orig-model"),
		attribute.Key(genaiAttributeRequestModel).String("req-model"),
		attribute.Key(genaiAttributeResponseModel).String("res-model"),
		attribute.Key(genaiAttributeBackend).String(""),
		attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeInput),
	)
	count, sum := getHistogramValues(t, mr, genaiMetricClientTokenUsage, inputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 2.0, sum)

	cachedInputAttrs := attribute.NewSet(
		attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
		attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
		attribute.Key(genaiAttributeOriginalModel).String("orig-model"),
		attribute.Key(genaiAttributeRequestModel).String("req-model"),
		attribute.Key(genaiAttributeResponseModel).String("res-model"),
		attribute.Key(genaiAttributeBackend).String(""),
		attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeCachedInput),
	)
	count, sum = getHistogramValues(t, mr, genaiMetricClientTokenUsage, cachedInputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 1.0, sum)

	cacheCreationInputAttrs := attribute.NewSet(
		attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
		attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
		attribute.Key(genaiAttributeOriginalModel).String("orig-model"),
		attribute.Key(genaiAttributeRequestModel).String("req-model"),
		attribute.Key(genaiAttributeResponseModel).String("res-model"),
		attribute.Key(genaiAttributeBackend).String(""),
		attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeCacheCreationInput),
	)
	count, sum = getHistogramValues(t, mr, genaiMetricClientTokenUsage, cacheCreationInputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 6.0, sum)

	outputAttrs := attribute.NewSet(
		attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
		attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
		attribute.Key(genaiAttributeOriginalModel).String("orig-model"),
		attribute.Key(genaiAttributeRequestModel).String("req-model"),
		attribute.Key(genaiAttributeResponseModel).String("res-model"),
		attribute.Key(genaiAttributeBackend).String(""),
		attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeOutput),
	)
	count, sum = getHistogramValues(t, mr, genaiMetricClientTokenUsage, outputAttrs)
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 3.0, sum)
}

// getHistogramValues returns the count and sum of a histogram metric with the given attributes.
func getHistogramValues(t *testing.T, reader metric.Reader, metric string, attrs attribute.Set) (uint64, float64) {
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &data))

	var datapoints []metricdata.HistogramDataPoint[float64]
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metric {
				continue
			}
			data := m.Data.(metricdata.Histogram[float64])
			for _, dp := range data.DataPoints {
				if dp.Attributes.Equals(&attrs) {
					datapoints = append(datapoints, dp)
				}
			}
		}
	}

	require.Len(t, datapoints, 1, "found %d datapoints for attributes: %v", len(datapoints), attrs)

	return datapoints[0].Count, datapoints[0].Sum
}

// TestRecordTokenLatency_MaxAcrossStream_EndHasNoUsage tests that we track the maximum token count
// across all streaming chunks, even when the final chunk has no usage data.
func TestRecordTokenLatency_MaxAcrossStream_EndHasNoUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := metric.NewManualReader()
		meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderAWSBedrock),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}})

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 1, false, nil)

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 5, false, nil)

		// A later event with a smaller number (simulate out-of-order/delta); should not reduce max.
		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 3, false, nil)

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 0, true, nil)

		count, sum := getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, (20*time.Millisecond-5*time.Millisecond).Seconds()/4, sum)
	})
}

// TestRecordTokenLatency_OnlyFinalUsage tests that time_per_output_token is calculated correctly
// when token usage is only provided in the final chunk (non-streaming responses).
func TestRecordTokenLatency_OnlyFinalUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := metric.NewManualReader()
		meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderAWSBedrock),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock}})

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 1, false, nil)

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 3, false, nil)

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 7, true, nil)

		count, sum := getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
		assert.Equal(t, uint64(1), count)
		expected := (10 * time.Millisecond).Seconds() / 6
		assert.Equal(t, expected, sum)
	})
}

// TestRecordTokenLatency_ZeroTokensFirst tests that time_to_first_token is recorded on the first chunk
// even when it has zero tokens (streaming responses without usage in initial chunks).
func TestRecordTokenLatency_ZeroTokensFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := metric.NewManualReader()
		meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})

		// First token (records TTFT).
		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 0, false, nil)
		count, sum := getHistogramValues(t, mr, genaiMetricServerTimeToFirstToken, attrs)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, 5*time.Millisecond.Seconds(), sum, "Should record TTFT at 5ms on first call")

		// Second chunk with 2 tokens.
		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 2, false, nil)
		count, sum = getHistogramValues(t, mr, genaiMetricServerTimeToFirstToken, attrs)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, 5*time.Millisecond.Seconds(), sum, "TTFT should remain at 5ms")

		// Final chunk with total of 5 tokens.
		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 5, true, nil)
		count, sum = getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, (10*time.Millisecond).Seconds()/4, sum)
	})
}

// TestRecordTokenLatency_IntegerTruncation tests that time_per_output_token
// does not truncate to zero due to integer division.
func TestRecordTokenLatency_IntegerTruncation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := metric.NewManualReader()
		meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})

		// First call records TTFT.
		time.Sleep(1 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 1, false, nil)

		// 50ns / (100-1) = 0.5ns TPOT; integer division would truncate to 0.
		time.Sleep(50 * time.Nanosecond)
		pm.RecordTokenLatency(t.Context(), 100, true, nil)

		count, sum := getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
		require.Equal(t, uint64(1), count)
		require.Greater(t, sum, 0.0)
	})
}

// TestRecordTokenLatency_SingleToken tests that time_per_output_token is NOT recorded
// when there's only one output token (division by zero case).
func TestRecordTokenLatency_SingleToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := metric.NewManualReader()
		meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})

		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 1, true, nil)

		count, sum := getHistogramValues(t, mr, genaiMetricServerTimeToFirstToken, attrs)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, 5*time.Millisecond.Seconds(), sum)

		var data2 metricdata.ResourceMetrics
		require.NoError(t, mr.Collect(t.Context(), &data2))
		hasTimePerToken := false
		for _, sm := range data2.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == genaiMetricServerTimePerOutputToken {
					hasTimePerToken = true
				}
			}
		}
		assert.False(t, hasTimePerToken, "Should not record time_per_output_token with only 1 token")
	})
}

// TestRecordTokenLatency_MultipleChunksFormula tests the OTEL spec formula:
// time_per_output_token = (request_duration - time_to_first_token) / (output_tokens - 1)
// with multiple streaming chunks to verify correct calculation.
func TestRecordTokenLatency_MultipleChunksFormula(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mr := metric.NewManualReader()
		meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(""),
		)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI}})

		// First chunk: 3 tokens at 5ms (records TTFT).
		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 3, false, nil)

		// Second chunk: 5 tokens total at 10ms.
		time.Sleep(5 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 5, false, nil)

		// Final chunk: 10 tokens total at 20ms.
		time.Sleep(10 * time.Millisecond)
		pm.RecordTokenLatency(t.Context(), 10, true, nil)

		count, sum := getHistogramValues(t, mr, genaiMetricServerTimeToFirstToken, attrs)
		assert.Equal(t, uint64(1), count)
		assert.Equal(t, 5*time.Millisecond.Seconds(), sum)

		count, sum = getHistogramValues(t, mr, genaiMetricServerTimePerOutputToken, attrs)
		assert.Equal(t, uint64(1), count)
		expected := (15 * time.Millisecond).Seconds() / 9
		assert.InDelta(t, expected, sum, 1e-9)
	})
}

func TestSetBackendProviderName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		schema           filterapi.APISchemaName
		backendName      string
		expectedProvider string
	}{
		{
			name:             "OpenAI schema",
			schema:           filterapi.APISchemaOpenAI,
			expectedProvider: "openai",
		},
		{
			name:             "Azure OpenAI schema",
			schema:           filterapi.APISchemaAzureOpenAI,
			expectedProvider: "azure.openai",
		},
		{
			name:             "AWS Bedrock schema",
			schema:           filterapi.APISchemaAWSBedrock,
			expectedProvider: "aws.bedrock",
		},
		{
			name:             "AWS Anthropic schema",
			schema:           filterapi.APISchemaAWSAnthropic,
			expectedProvider: "aws.anthropic",
		},
		{
			name:             "GCP Vertex AI schema",
			schema:           filterapi.APISchemaGCPVertexAI,
			expectedProvider: "gcp.vertex_ai",
		},
		{
			name:             "GCP Anthropic schema",
			schema:           filterapi.APISchemaGCPAnthropic,
			expectedProvider: "gcp.anthropic",
		},
		{
			name:             "Anthropic schema",
			schema:           filterapi.APISchemaAnthropic,
			expectedProvider: "anthropic",
		},
		{
			name:             "Cohere schema",
			schema:           filterapi.APISchemaCohere,
			expectedProvider: "cohere",
		},
		{
			name:             "Unknown schema falls back to backend name",
			schema:           "UnknownSchema",
			backendName:      "my-custom-backend",
			expectedProvider: "my-custom-backend",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pm := &metricsImpl{}
			backend := &filterapi.Backend{
				Name:   tc.backendName,
				Schema: filterapi.VersionedAPISchema{Name: tc.schema},
			}
			pm.SetBackend(backend)
			assert.Equal(t, tc.expectedProvider, pm.backend)
		})
	}
}

func TestAIServiceBackendAttribute(t *testing.T) {
	// Two backends sharing an API schema must stay distinguishable, which is what
	// gen_ai.provider.name alone cannot express. Both record into the same meter so
	// their data points would collapse into one if the attribute did not separate them.
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	backendNames := []string{
		internalapi.PerRouteRuleRefBackendName("default", "primary", "some-route", 0, 0),
		internalapi.PerRouteRuleRefBackendName("default", "fallback", "some-route", 0, 1),
	}
	for _, backendName := range backendNames {
		pm := NewMetricsFactory(meter, nil, GenAIOperationCompletion).NewMetrics().(*metricsImpl)

		pm.StartRequest(nil)
		pm.SetOriginalModel("test-model")
		pm.SetRequestModel("test-model")
		pm.SetResponseModel("test-model")
		pm.SetBackend(&filterapi.Backend{
			Name:   backendName,
			Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
		})
		pm.RecordTokenUsage(t.Context(), TokenUsage{inputTokens: 10, inputTokenSet: true}, nil)
	}

	for _, backendName := range backendNames {
		attrs := attribute.NewSet(
			attribute.Key(genaiAttributeOperationName).String(string(GenAIOperationCompletion)),
			attribute.Key(genaiAttributeProviderName).String(genaiProviderOpenAI),
			attribute.Key(genaiAttributeOriginalModel).String("test-model"),
			attribute.Key(genaiAttributeRequestModel).String("test-model"),
			attribute.Key(genaiAttributeResponseModel).String("test-model"),
			attribute.Key(genaiAttributeBackend).String(internalapi.AIServiceBackendName(backendName)),
			attribute.Key(genaiAttributeTokenType).String(genaiTokenTypeInput),
		)
		count, _ := testotel.GetHistogramValues(t, mr, genaiMetricClientTokenUsage, attrs)
		require.Equal(t, uint64(1), count)
	}
}
