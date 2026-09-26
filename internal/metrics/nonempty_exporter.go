// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// nonEmptyExporter wraps a stdout metric exporter to suppress empty metrics exports.
type nonEmptyExporter struct {
	delegate metric.Exporter
}

// temporalityExporter wraps nonEmptyExporter to support configurable temporality.
type temporalityExporter struct {
	nonEmptyExporter
	temporality metricdata.Temporality
}

// newNonEmptyConsoleExporter creates a console exporter that only exports when there are metrics
// and respects the OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE environment variable.
// Supported values are "cumulative" (default) and "delta".
func newNonEmptyConsoleExporter(writer io.Writer) (metric.Exporter, error) {
	delegate, err := stdoutmetric.New(
		stdoutmetric.WithWriter(writer),
	)
	if err != nil {
		return nil, err
	}

	// Parse temporality preference from environment
	temporality, err := parseTemporalityPreference()
	if err != nil {
		return nil, err
	}

	return &temporalityExporter{
		delegate:    delegate,
		temporality: temporality,
	}, nil
}

// parseTemporalityPreference reads OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE
// and returns the corresponding temporality. Defaults to cumulative.
func parseTemporalityPreference() (metricdata.Temporality, error) {
	pref := strings.ToLower(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"))

	switch pref {
	case "", "cumulative":
		return metricdata.CumulativeTemporality, nil
	case "delta":
		return metricdata.DeltaTemporality, nil
	default:
		return metricdata.CumulativeTemporality, fmt.Errorf("unsupported OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE value: %q (supported values: cumulative, delta)", pref)
	}
}

// newNonEmptyExporter creates a new exporter that only exports when there are metrics.
// This is kept for backward compatibility and testing.
func newNonEmptyExporter(writer io.Writer) (metric.Exporter, error) {
	delegate, err := stdoutmetric.New(
		stdoutmetric.WithWriter(writer),
	)
	if err != nil {
		return nil, err
	}
	return &nonEmptyExporter{delegate: delegate}, nil
}

// Export only delegates to the underlying exporter if there are actual metrics to export.
func (e *nonEmptyExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	if rm == nil {
		return nil
	}
	for _, sm := range rm.ScopeMetrics {
		if len(sm.Metrics) > 0 {
			return e.delegate.Export(ctx, rm)
		}
	}
	return nil
}

// Temporality returns the temporality of the underlying exporter.
func (e *nonEmptyExporter) Temporality(kind metric.InstrumentKind) metricdata.Temporality {
	return e.delegate.Temporality(kind)
}

// Aggregation returns the aggregation of the underlying exporter.
func (e *nonEmptyExporter) Aggregation(kind metric.InstrumentKind) metric.Aggregation {
	return e.delegate.Aggregation(kind)
}

// Shutdown shuts down the underlying exporter.
func (e *nonEmptyExporter) Shutdown(ctx context.Context) error {
	return e.delegate.Shutdown(ctx)
}

// ForceFlush flushes the underlying exporter.
func (e *nonEmptyExporter) ForceFlush(ctx context.Context) error {
	return e.delegate.ForceFlush(ctx)
}

// Temporality returns the configured temporality for all instrument kinds.
func (e *temporalityExporter) Temporality(metric.InstrumentKind) metricdata.Temporality {
	return e.temporality
}
