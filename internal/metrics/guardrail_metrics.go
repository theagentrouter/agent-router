// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	guardrailEvaluationCount = "aigateway.guardrail.evaluation.count"
	guardrailAttributePhase  = "aigateway.guardrail.phase"
	guardrailAttributeResult = "aigateway.guardrail.result"
)

// GuardrailResult is the outcome of evaluating a payload against guardrails.
type GuardrailResult string

const (
	GuardrailResultAllowed   GuardrailResult = "allowed"
	GuardrailResultBlocked   GuardrailResult = "blocked"
	GuardrailResultError     GuardrailResult = "error"
	GuardrailResultMasked    GuardrailResult = "masked"
	GuardrailResultMonitored GuardrailResult = "monitored"
)

// GuardrailMetrics records guardrail evaluation outcomes.
type GuardrailMetrics interface {
	RecordEvaluation(ctx context.Context, phase string, result GuardrailResult)
}

type guardrailMetrics struct {
	evaluationCount metric.Float64Counter
}

// NewGuardrailMetrics creates guardrail metrics backed by the provided meter.
func NewGuardrailMetrics(meter metric.Meter) GuardrailMetrics {
	return &guardrailMetrics{
		evaluationCount: mustRegisterCounter(
			meter,
			guardrailEvaluationCount,
			metric.WithDescription("Total number of payloads evaluated by guardrails"),
		),
	}
}

func (m *guardrailMetrics) RecordEvaluation(ctx context.Context, phase string, result GuardrailResult) {
	m.evaluationCount.Add(ctx, 1, metric.WithAttributes(
		attribute.String(guardrailAttributePhase, phase),
		attribute.String(guardrailAttributeResult, string(result)),
	))
}
