// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

// TestNewMeterFromEnv_ConsoleExporter tests console/none exporter configuration.
// We use synctest here because console output relies on time.Sleep to wait for
// the periodic exporter, and synctest makes these sleeps instant in wall-clock time.
func TestNewMeterFromEnv_ConsoleExporter(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name                    string
		env                     map[string]string
		expectedConsoleContains string
		expectServiceName       string
		expectResource          bool
	}{
		{
			name: "console exporter outputs to stdout",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER": "console",
			},
			expectedConsoleContains: "test.console.metric",
			expectServiceName:       "ai-gateway",
			expectResource:          true,
		},
		{
			name: "console exporter with custom service name",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER": "console",
				"OTEL_SERVICE_NAME":     "my-custom-service",
			},
			expectedConsoleContains: "test.console.metric",
			expectServiceName:       "my-custom-service",
			expectResource:          true,
		},
		{
			name: "console with resource attributes overriding service name",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER":    "console",
				"OTEL_RESOURCE_ATTRIBUTES": "service.name=overridden-service",
			},
			expectedConsoleContains: "test.console.metric",
			expectServiceName:       "overridden-service",
			expectResource:          true,
		},
		{
			name: "no console output with prometheus exporter",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER": "prometheus",
			},
			expectedConsoleContains: "",
			expectServiceName:       "",
			expectResource:          false,
		},
		{
			name: "no console output when disabled",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER": "none",
			},
			expectedConsoleContains: "",
			expectServiceName:       "",
			expectResource:          false,
		},
		{
			name: "no console output when SDK disabled",
			env: map[string]string{
				"OTEL_SDK_DISABLED":     "true",
				"OTEL_METRICS_EXPORTER": "console",
			},
			expectedConsoleContains: "",
			expectServiceName:       "",
			expectResource:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Helper()
				t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "100")
				for k, v := range tt.env {
					t.Setenv(k, v)
				}

				var stdout bytes.Buffer
				manualReader := sdkmetric.NewManualReader()

				meter, shutdown, err := NewMeterFromEnv(t.Context(), &stdout, manualReader)
				require.NoError(t, err)
				require.NotNil(t, meter)
				require.NotNil(t, shutdown)
				defer func() {
					_ = shutdown(context.Background())
				}()

				// Create and record a metric
				counter, err := meter.Int64Counter("test.console.metric",
					metric.WithDescription("A test metric"),
					metric.WithUnit("1"))
				require.NoError(t, err)
				counter.Add(t.Context(), 42)

				// Collect metrics via Prometheus reader
				var rm metricdata.ResourceMetrics
				err = manualReader.Collect(t.Context(), &rm)
				require.NoError(t, err)
				require.NotEmpty(t, rm.ScopeMetrics, "Prometheus reader should collect metrics")

				// Verify resource attributes
				found := false
				var serviceName string
				for _, attr := range rm.Resource.Attributes() {
					if attr.Key == "service.name" {
						found = true
						serviceName = attr.Value.AsString()
						break
					}
				}
				if tt.expectResource {
					require.True(t, found, "service.name attribute should be present")
					require.Equal(t, tt.expectServiceName, serviceName)
				}

				// Check console output
				if tt.expectedConsoleContains != "" {
					time.Sleep(150 * time.Millisecond)
					synctest.Wait()
					output := stdout.String()
					// Single check for all expected content in console output
					expectedParts := []string{tt.expectedConsoleContains, "42"}
					if tt.expectServiceName != "" {
						expectedParts = append(expectedParts, tt.expectServiceName)
					}
					for _, part := range expectedParts {
						require.Contains(t, output, part)
					}
				} else {
					output := stdout.String()
					require.Empty(t, output, "no console output expected")
				}
			})
		})
	}
}

// TestNewMeterFromEnv_ConsoleExporter_NoMetrics tests that the console exporter
// does not output anything when no metrics are recorded.
func TestNewMeterFromEnv_ConsoleExporter_NoMetrics(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	synctest.Test(t, func(t *testing.T) {
		t.Helper()
		t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "100")
		t.Setenv("OTEL_METRICS_EXPORTER", "console")

		var stdout bytes.Buffer
		manualReader := sdkmetric.NewManualReader()

		meter, shutdown, err := NewMeterFromEnv(t.Context(), &stdout, manualReader)
		require.NoError(t, err)
		require.NotNil(t, meter)
		require.NotNil(t, shutdown)
		defer func() {
			_ = shutdown(context.Background())
		}()

		// Don't record any metrics, just wait for export interval
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()

		// Check that no output was generated
		output := stdout.String()
		require.Empty(t, output, "Expected no console output when no metrics are recorded")

		// Verify that Prometheus reader still works but has empty metrics
		var rm metricdata.ResourceMetrics
		err = manualReader.Collect(t.Context(), &rm)
		require.NoError(t, err)
		// The ScopeMetrics should be empty since no metrics were created
		require.Empty(t, rm.ScopeMetrics, "No metrics should be collected when none are recorded")
	})
}

// TestNewMeterFromEnv_NetworkExporters tests OTLP and other network-based exporters.
// We CANNOT use synctest here because it creates a "bubble" where goroutines are isolated
// and network operations that spawn goroutines outside the bubble cause a panic:
// "select on synctest channel from outside bubble"
// This happens because the HTTP client used by OTLP exporters uses net.Resolver which
// spawns goroutines for DNS resolution that escape the synctest bubble.
func TestNewMeterFromEnv_NetworkExporters(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	// Create a test server to avoid real network access
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tests := []struct {
		name              string
		env               map[string]string
		expectServiceName string
		expectResource    bool
	}{
		{
			name: "otlp exporter enabled with endpoint",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER":       "otlp",
				"OTEL_EXPORTER_OTLP_ENDPOINT": ts.URL,
			},
			expectServiceName: "ai-gateway",
			expectResource:    true,
		},
		{
			name: "default exporter (stdout) with otlp endpoint but no exporter set",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": ts.URL,
			},
			expectServiceName: "ai-gateway",
			expectResource:    true,
		},
		{
			name: "no additional exporter with prometheus and endpoint",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER":       "prometheus",
				"OTEL_EXPORTER_OTLP_ENDPOINT": ts.URL,
			},
			expectServiceName: "",
			expectResource:    false,
		},
		{
			name: "no additional exporter with none and endpoint",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER":       "none",
				"OTEL_EXPORTER_OTLP_ENDPOINT": ts.URL,
			},
			expectServiceName: "",
			expectResource:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			var stdout bytes.Buffer
			manualReader := sdkmetric.NewManualReader()

			meter, shutdown, err := NewMeterFromEnv(t.Context(), &stdout, manualReader)
			require.NoError(t, err)
			require.NotNil(t, meter)
			require.NotNil(t, shutdown)
			defer func() {
				_ = shutdown(context.Background())
			}()

			// Create and record a metric
			counter, err := meter.Int64Counter("test.network.metric",
				metric.WithDescription("A test metric"),
				metric.WithUnit("1"))
			require.NoError(t, err)
			counter.Add(t.Context(), 42)

			// Collect metrics via Prometheus reader
			var rm metricdata.ResourceMetrics
			err = manualReader.Collect(t.Context(), &rm)
			require.NoError(t, err)
			require.NotEmpty(t, rm.ScopeMetrics, "Prometheus reader should collect metrics")

			// Verify resource attributes
			found := false
			var serviceName string
			for _, attr := range rm.Resource.Attributes() {
				if attr.Key == "service.name" {
					found = true
					serviceName = attr.Value.AsString()
					break
				}
			}
			if tt.expectResource {
				require.True(t, found, "service.name attribute should be present")
				require.Equal(t, tt.expectServiceName, serviceName)
			}

			// No console output expected for network exporters
			output := stdout.String()
			require.Empty(t, output, "no console output expected for network exporters")
		})
	}
}

// TestNewMeterFromEnv_PrometheusReader tests that the prometheus reader
// is always included and functional.
func TestNewMeterFromEnv_PrometheusReader(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	// Create a test server to avoid real network access
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "prometheus reader with no OTEL",
			env:  map[string]string{},
		},
		{
			name: "prometheus reader with console exporter",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER": "console",
			},
		},
		{
			name: "prometheus reader with OTLP endpoint",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": ts.URL,
			},
		},
		{
			name: "prometheus reader when OTEL disabled",
			env: map[string]string{
				"OTEL_SDK_DISABLED": "true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			manualReader := sdkmetric.NewManualReader()
			meter, shutdown, err := NewMeterFromEnv(t.Context(), io.Discard, manualReader)
			require.NoError(t, err)
			require.NotNil(t, meter)
			require.NotNil(t, shutdown)
			defer func() {
				_ = shutdown(context.Background())
			}()

			// Create metrics
			counter, err := meter.Int64Counter("prometheus.test.counter")
			require.NoError(t, err)

			histogram, err := meter.Float64Histogram("prometheus.test.histogram")
			require.NoError(t, err)

			// Record values
			counter.Add(t.Context(), 1)
			counter.Add(t.Context(), 2)
			counter.Add(t.Context(), 3)
			histogram.Record(t.Context(), 1.5)
			histogram.Record(t.Context(), 2.5)

			// Collect via prometheus reader
			var rm metricdata.ResourceMetrics
			err = manualReader.Collect(t.Context(), &rm)
			require.NoError(t, err)

			// Verify metrics were collected
			require.NotEmpty(t, rm.ScopeMetrics)
			require.Len(t, rm.ScopeMetrics[0].Metrics, 2)

			// Verify counter sum and histogram count
			for _, m := range rm.ScopeMetrics[0].Metrics {
				switch m.Name {
				case "prometheus.test.counter":
					sum, ok := m.Data.(metricdata.Sum[int64])
					require.True(t, ok)
					require.Equal(t, int64(6), sum.DataPoints[0].Value)
				case "prometheus.test.histogram":
					hist, ok := m.Data.(metricdata.Histogram[float64])
					require.True(t, ok)
					require.Equal(t, uint64(2), hist.DataPoints[0].Count)
				}
			}
		})
	}
}

// TestNewMeterFromEnv_ErrorHandling verifies error handling for invalid configurations.
func TestNewMeterFromEnv_ErrorHandling(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	tests := []struct {
		name        string
		env         map[string]string
		expectError string
	}{
		{
			name: "invalid resource attributes",
			env: map[string]string{
				"OTEL_METRICS_EXPORTER":    "console",
				"OTEL_RESOURCE_ATTRIBUTES": "invalid",
			},
			expectError: "missing value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			manualReader := sdkmetric.NewManualReader()
			_, _, err := NewMeterFromEnv(t.Context(), io.Discard, manualReader)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectError)
		})
	}
}

// TestNewMeterFromEnv_OTLPHeaders tests that OTEL_EXPORTER_OTLP_HEADERS
// is properly handled by the autoexport package.
func TestNewMeterFromEnv_OTLPHeaders(t *testing.T) {
	expectedAuthorization := "ApiKey test-key-123"
	actualAuthorization := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualAuthorization <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	internaltesting.ClearTestEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization="+expectedAuthorization)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", ts.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")

	manualReader := sdkmetric.NewManualReader()
	meter, shutdown, err := NewMeterFromEnv(t.Context(), io.Discard, manualReader)
	require.NoError(t, err)
	defer func() {
		_ = shutdown(context.Background())
	}()

	// Create metric to trigger export
	counter, err := meter.Int64Counter("test.metric")
	require.NoError(t, err)
	counter.Add(t.Context(), 1)

	// Force flush
	err = shutdown(t.Context())
	require.NoError(t, err)

	require.Equal(t, expectedAuthorization, <-actualAuthorization)
}

func TestTokenUsage_IsZero(t *testing.T) {
	var u TokenUsage
	require.True(t, u.IsZero())

	u.SetOutputTokens(1)
	require.False(t, u.IsZero())
}
