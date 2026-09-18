// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type recordingGuardrailMetrics struct {
	phase  string
	result metrics.GuardrailResult
	count  int
}

type failingGuardrailEvaluator struct{}

func (*failingGuardrailEvaluator) Evaluate(context.Context, []byte, filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	return filterapi.GuardrailEvaluationResult{}, errors.New("provider unavailable")
}

type maskingGuardrailEvaluator struct{}

func (*maskingGuardrailEvaluator) Evaluate(_ context.Context, body []byte, _ filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	return filterapi.GuardrailEvaluationResult{Matched: true, Replacement: []byte("masked:" + string(body))}, nil
}

func (m *recordingGuardrailMetrics) RecordEvaluation(_ context.Context, phase string, result metrics.GuardrailResult) {
	m.phase = phase
	m.result = result
	m.count++
}

func TestEvaluateGuardrailsForPhase(t *testing.T) {
	t.Run("request guardrail matches and returns violation", func(t *testing.T) {
		guardrails := []filterapi.RuntimeGuardrail{{
			Name:  "deny-pii",
			Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{
				Type:    filterapi.GuardrailProviderTypeRegex,
				Message: "PII detected in request",
			},
			Matcher: regexp.MustCompile(`\bSSN\b`),
		}}

		outcome, err := evaluateRequestGuardrails(t.Context(), guardrails, []byte("customer SSN is present"))
		require.NoError(t, err)
		require.NotNil(t, outcome.Violation)
		require.Equal(t, "deny-pii", outcome.Violation.Name)
		require.Equal(t, "PII detected in request", outcome.Violation.Message)
	})

	t.Run("response guardrail ignores different phase", func(t *testing.T) {
		guardrails := []filterapi.RuntimeGuardrail{{
			Name:  "block-sensitive-response",
			Phase: filterapi.GuardrailPhaseResponse,
			Provider: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypeRegex,
			},
			Matcher: regexp.MustCompile(`forbidden`),
		}}

		outcome, err := evaluateRequestGuardrails(t.Context(), guardrails, []byte("forbidden"))
		require.NoError(t, err)
		require.Nil(t, outcome.Violation)
	})

	t.Run("regex guardrail without compiled matcher returns error on matching phase", func(t *testing.T) {
		guardrails := []filterapi.RuntimeGuardrail{{
			Name:  "missing-matcher",
			Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypeRegex,
			},
		}}

		outcome, err := evaluateRequestGuardrails(t.Context(), guardrails, []byte("forbidden"))
		require.Error(t, err)
		require.Nil(t, outcome.Violation)
		require.Contains(t, err.Error(), "uses regex provider without a compiled matcher")
	})
}

func TestRequestGuardrailBlockRecordsMetric(t *testing.T) {
	recorder := &recordingGuardrailMetrics{}
	config := &filterapi.RuntimeConfig{
		Guardrails: []filterapi.RuntimeGuardrail{{
			Name:  "deny-pii",
			Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypeRegex,
			},
			Matcher: regexp.MustCompile(`SSN`),
		}},
	}
	factory := NewFactory(nil, recorder, tracingapi.NoopChatCompletionTracer{}, endpointspec.ChatCompletionsEndpointSpec{})
	processor, err := factory(config, map[string]string{
		"content-type": "application/json",
		":path":        "/v1/chat/completions",
	}, slog.Default(), false, false)
	require.NoError(t, err)

	response, err := processor.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{
		Body: []byte(`{"model":"test","messages":[{"role":"user","content":"customer SSN"}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, response.GetImmediateResponse())
	require.Equal(t, 1, recorder.count)
	require.Equal(t, string(filterapi.GuardrailPhaseRequest), recorder.phase)
	require.Equal(t, metrics.GuardrailResultBlocked, recorder.result)
}

func TestRequestGuardrailMaskMutatesBody(t *testing.T) {
	recorder := &recordingGuardrailMetrics{}
	config := &filterapi.RuntimeConfig{Guardrails: []filterapi.RuntimeGuardrail{{
		Name: "mask-email", Phase: filterapi.GuardrailPhaseRequest,
		Provider: filterapi.GuardrailProvider{
			Type: filterapi.GuardrailProviderTypeRegex, Action: filterapi.GuardrailActionMask,
			MaskReplacement: "[EMAIL]",
		},
		Matcher: regexp.MustCompile(`alice@example\.com`),
	}}}
	factory := NewFactory(nil, recorder, tracingapi.NoopChatCompletionTracer{}, endpointspec.ChatCompletionsEndpointSpec{})
	processor, err := factory(config, map[string]string{
		"content-type": "application/json",
		":path":        "/v1/chat/completions",
	}, slog.Default(), false, false)
	require.NoError(t, err)

	response, err := processor.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{
		Body: []byte(`{"model":"test","messages":[{"role":"user","content":"email alice@example.com"}]}`),
	})
	require.NoError(t, err)
	require.Nil(t, response.GetImmediateResponse())
	require.Nil(t, response.GetRequestBody().Response.BodyMutation)
	processorImpl := processor.(*chatCompletionProcessorRouterFilter)
	require.JSONEq(t, `{"model":"test","messages":[{"role":"user","content":"email [EMAIL]"}]}`, string(processorImpl.originalRequestBodyRaw))
	require.Equal(t, metrics.GuardrailResultMasked, recorder.result)
}

func TestRecordGuardrailEvaluationIgnoresUnconfiguredPhase(t *testing.T) {
	recorder := &recordingGuardrailMetrics{}
	processor := &chatCompletionProcessorRouterFilter{
		config: &filterapi.RuntimeConfig{Guardrails: []filterapi.RuntimeGuardrail{{
			Phase: filterapi.GuardrailPhaseResponse,
		}}},
		guardrailMetrics: recorder,
	}

	processor.recordGuardrailEvaluation(t.Context(), filterapi.GuardrailPhaseRequest, metrics.GuardrailResultAllowed, "", true)
	require.Zero(t, recorder.count)
}

func TestBackendScopedGuardrail(t *testing.T) {
	guardrails := []filterapi.RuntimeGuardrail{{
		Name: "backend-only", Phase: filterapi.GuardrailPhaseRequest,
		Backends: []string{"selected-backend"},
		Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex},
		Matcher:  regexp.MustCompile("blocked"),
	}}

	outcome, err := evaluateBackendRequestGuardrails(t.Context(), guardrails, []byte("blocked"), "other-backend")
	require.NoError(t, err)
	require.Nil(t, outcome.Violation)

	outcome, err = evaluateBackendRequestGuardrails(t.Context(), guardrails, []byte("blocked"), "selected-backend")
	require.NoError(t, err)
	require.NotNil(t, outcome.Violation)
}

func TestGuardrailFailureModes(t *testing.T) {
	guardrail := filterapi.RuntimeGuardrail{
		Name: "external", Phase: filterapi.GuardrailPhaseRequest,
		Provider:  filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypePresidio},
		Evaluator: &failingGuardrailEvaluator{},
	}

	_, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{guardrail}, []byte("payload"))
	require.Error(t, err)
	require.False(t, isGuardrailFailOpenError(err))

	guardrail.Provider.FailureMode = filterapi.GuardrailFailureModeFailOpen
	outcome, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{guardrail}, []byte("payload"))
	require.Nil(t, outcome.Violation)
	require.Error(t, err)
	require.True(t, isGuardrailFailOpenError(err))
}

func TestGuardrailMonitorAndMask(t *testing.T) {
	body := []byte(`{"model":"safe-model","messages":[{"role":"user","content":"secret"}]}`)

	t.Run("monitor detects without changing body", func(t *testing.T) {
		outcome, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
			Name: "monitor", Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex, Action: filterapi.GuardrailActionMonitor},
			Matcher:  regexp.MustCompile("secret"),
		}}, body)
		require.NoError(t, err)
		require.True(t, outcome.Monitored)
		require.False(t, outcome.Masked)
		require.Nil(t, outcome.Violation)
		require.Equal(t, body, outcome.Body)
	})

	t.Run("mask replaces extracted content only", func(t *testing.T) {
		outcome, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
			Name: "mask", Phase: filterapi.GuardrailPhaseRequest,
			Provider:  filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypePresidio, Action: filterapi.GuardrailActionMask},
			Evaluator: &maskingGuardrailEvaluator{},
		}}, body)
		require.NoError(t, err)
		require.True(t, outcome.Masked)
		require.JSONEq(t, `{"model":"safe-model","messages":[{"role":"user","content":"masked:secret"}]}`, string(outcome.Body))
		require.NotContains(t, string(outcome.Body), "masked:safe-model")
	})
}

func TestGuardrailPayloadLimit(t *testing.T) {
	guardrail := filterapi.RuntimeGuardrail{
		Name: "small", Phase: filterapi.GuardrailPhaseRequest, MaxPayloadBytes: 8,
		Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex},
		Matcher:  regexp.MustCompile("secret"),
	}
	_, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{guardrail}, []byte(`{"content":"secret"}`))
	require.ErrorContains(t, err, "exceeding the 8-byte limit")

	guardrail.Provider.FailureMode = filterapi.GuardrailFailureModeFailOpen
	_, err = evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{guardrail}, []byte(`{"content":"secret"}`))
	require.True(t, isGuardrailFailOpenError(err))
}
