// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import "go.opentelemetry.io/otel/metric"

// mustRegisterCounter registers a Counter with the meter and panics if it fails.
func mustRegisterCounter(meter metric.Meter, name string, options ...metric.Float64CounterOption) metric.Float64Counter {
	h, err := meter.Float64Counter(name, options...)
	if err != nil {
		panic(err)
	}
	return h
}

// mustRegisterUpDownCounter registers an up/down counter with the meter and panics if it fails.
func mustRegisterUpDownCounter(meter metric.Meter, name string, options ...metric.Float64UpDownCounterOption) metric.Float64UpDownCounter {
	h, err := meter.Float64UpDownCounter(name, options...)
	if err != nil {
		panic(err)
	}
	return h
}

// mustRegisterHistogram registers a histogram with the meter and panics if it fails.
func mustRegisterHistogram(meter metric.Meter, name string, options ...metric.Float64HistogramOption) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name, options...)
	if err != nil {
		panic(err)
	}
	return h
}
