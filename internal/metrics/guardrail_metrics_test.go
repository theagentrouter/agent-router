// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestGuardrailMetricsRecordEvaluation(t *testing.T) {
	reader := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(reader)).Meter("test")
	guardrails := NewGuardrailMetrics(meter)

	guardrails.RecordEvaluation(t.Context(), "Request", GuardrailResultBlocked)
	guardrails.RecordEvaluation(t.Context(), "Request", GuardrailResultBlocked)

	value := testotel.GetCounterValue(t, reader, guardrailEvaluationCount, attribute.NewSet(
		attribute.String(guardrailAttributePhase, "Request"),
		attribute.String(guardrailAttributeResult, string(GuardrailResultBlocked)),
	))
	require.Equal(t, float64(2), value)
}
