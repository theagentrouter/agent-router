// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/guardrails"
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
	span := &mockGuardrailChatCompletionSpan{}
	processor := &chatCompletionProcessorRouterFilter{
		config: &filterapi.RuntimeConfig{Guardrails: []filterapi.RuntimeGuardrail{{
			Phase: filterapi.GuardrailPhaseResponse,
		}}},
		guardrailMetrics: recorder,
		span:             span,
	}

	processor.recordGuardrailEvaluation(t.Context(), "", filterapi.GuardrailPhaseRequest, metrics.GuardrailResultAllowed, "", true)
	require.Zero(t, recorder.count)
	require.Empty(t, span.guardrailEvents)

	processor.recordGuardrailEvaluation(t.Context(), "deny-pii", filterapi.GuardrailPhaseResponse, metrics.GuardrailResultBlocked, "", true)
	require.Equal(t, 1, recorder.count)
	require.Equal(t, []string{"deny-pii/Response/blocked"}, span.guardrailEvents)
}

func TestRecordGuardrailEvaluationWithoutGuardrails(t *testing.T) {
	span := &mockGuardrailChatCompletionSpan{}
	processor := &chatCompletionProcessorRouterFilter{config: &filterapi.RuntimeConfig{}, span: span}

	processor.recordGuardrailEvaluation(t.Context(), "", filterapi.GuardrailPhaseRequest, metrics.GuardrailResultAllowed, "", true)
	processor.recordGuardrailEvaluation(t.Context(), "", filterapi.GuardrailPhaseResponse, metrics.GuardrailResultAllowed, "backend", true)
	require.Empty(t, span.guardrailEvents)
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

func TestGuardrailHTTPProviderActions(t *testing.T) {
	body := []byte(`{"model":"safe-model","messages":[{"role":"user","content":"call me at 555-0100"}]}`)
	for _, tc := range []struct {
		name         string
		response     string
		action       filterapi.GuardrailAction
		expViolation bool
		expMonitored bool
		expMasked    bool
		expBody      string
		expErr       string
	}{
		{name: "allow passes through", response: `{"action":"allow"}`, expBody: string(body)},
		{name: "block blocks", response: `{"action":"block"}`, expViolation: true, expBody: string(body)},
		{name: "block with monitor records", response: `{"action":"block"}`, action: filterapi.GuardrailActionMonitor, expMonitored: true, expBody: string(body)},
		{
			name:      "findings masked",
			response:  `{"action":"modify","findings":[{"type":"PHONE","start":11,"end":19,"score":0.9}]}`,
			action:    filterapi.GuardrailActionMask,
			expMasked: true,
			expBody:   `{"model":"safe-model","messages":[{"role":"user","content":"call me at [REDACTED]"}]}`,
		},
		{
			name:      "replacement applied",
			response:  `{"action":"modify","replacement":"call me later"}`,
			action:    filterapi.GuardrailActionMask,
			expMasked: true,
			expBody:   `{"model":"safe-model","messages":[{"role":"user","content":"call me later"}]}`,
		},
		{name: "block without findings cannot mask", response: `{"action":"block"}`, action: filterapi.GuardrailActionMask, expErr: "returned no masked content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.response))
			}))
			t.Cleanup(server.Close)
			provider := filterapi.GuardrailProvider{
				Type:   filterapi.GuardrailProviderTypeHTTP,
				Action: tc.action,
				HTTP:   &filterapi.HTTPGuardrailProvider{Endpoint: server.URL},
			}
			evaluator, err := guardrails.NewEvaluator(t.Context(), &provider)
			require.NoError(t, err)

			outcome, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
				Name: "custom", Phase: filterapi.GuardrailPhaseRequest, Provider: provider, Evaluator: evaluator,
			}}, body)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expViolation, outcome.Violation != nil)
			require.Equal(t, tc.expMonitored, outcome.Monitored)
			require.Equal(t, tc.expMasked, outcome.Masked)
			require.JSONEq(t, tc.expBody, string(outcome.Body))
		})
	}
}

// guardrailProcessorCase describes one guardrail outcome exercised through a processor.
type guardrailProcessorCase struct {
	name       string
	guardrail  filterapi.RuntimeGuardrail
	wantErr    string
	wantBlock  bool
	wantResult metrics.GuardrailResult
}

func guardrailProcessorCases(phase filterapi.GuardrailPhase, backends []string) []guardrailProcessorCase {
	regex := func(name string, action filterapi.GuardrailAction) filterapi.RuntimeGuardrail {
		return filterapi.RuntimeGuardrail{
			Name: name, Phase: phase, Backends: backends,
			Provider: filterapi.GuardrailProvider{
				Type: filterapi.GuardrailProviderTypeRegex, Action: action, MaskReplacement: "[MASKED]", Message: "blocked",
			},
			Matcher: regexp.MustCompile("secret"),
		}
	}
	failing := func(mode filterapi.GuardrailFailureMode) filterapi.RuntimeGuardrail {
		return filterapi.RuntimeGuardrail{
			Name: "provider", Phase: phase, Backends: backends,
			Provider:  filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypePresidio, FailureMode: mode},
			Evaluator: &failingGuardrailEvaluator{},
		}
	}
	return []guardrailProcessorCase{
		{name: "block", guardrail: regex("deny", filterapi.GuardrailActionBlock), wantBlock: true, wantResult: metrics.GuardrailResultBlocked},
		{name: "monitor", guardrail: regex("monitor", filterapi.GuardrailActionMonitor), wantResult: metrics.GuardrailResultMonitored},
		{name: "mask", guardrail: regex("mask", filterapi.GuardrailActionMask), wantResult: metrics.GuardrailResultMasked},
		{name: "fail open", guardrail: failing(filterapi.GuardrailFailureModeFailOpen), wantResult: metrics.GuardrailResultError},
		{name: "fail closed", guardrail: failing(filterapi.GuardrailFailureModeFailClosed), wantErr: "guardrails", wantResult: metrics.GuardrailResultError},
	}
}

func TestRouterRequestGuardrailOutcomes(t *testing.T) {
	for _, test := range guardrailProcessorCases(filterapi.GuardrailPhaseRequest, nil) {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingGuardrailMetrics{}
			span := &mockGuardrailChatCompletionSpan{}
			factory := NewFactory(nil, recorder, &mockTracer{returnedSpan: span}, endpointspec.ChatCompletionsEndpointSpec{})
			processor, err := factory(&filterapi.RuntimeConfig{Guardrails: []filterapi.RuntimeGuardrail{test.guardrail}}, map[string]string{
				"content-type": "application/json",
				":path":        "/v1/chat/completions",
			}, slog.Default(), false, false)
			require.NoError(t, err)

			response, err := processor.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{
				Body: []byte(`{"model":"test","messages":[{"role":"user","content":"my secret"}]}`),
			})
			require.Equal(t, test.wantResult, recorder.result)
			require.Len(t, span.guardrailEvents, 1)
			if test.wantErr != "" {
				require.ErrorContains(t, err, "failed to evaluate request guardrails")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantBlock, response.GetImmediateResponse() != nil)
			if test.wantBlock {
				require.Equal(t, 1, span.endedOnErrorCount)
				require.Equal(t, http.StatusForbidden, span.errorStatusCode)
				return
			}
			if test.wantResult == metrics.GuardrailResultMasked {
				require.Contains(t, string(processor.(*chatCompletionProcessorRouterFilter).originalRequestBodyRaw), "my [MASKED]")
			}
		})
	}
}

func TestUpstreamRequestGuardrailOutcomes(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"my secret"}]}`)
	for _, test := range guardrailProcessorCases(filterapi.GuardrailPhaseRequest, []string{"backend-a"}) {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingGuardrailMetrics{}
			span := &mockGuardrailChatCompletionSpan{}
			parent := &chatCompletionProcessorRouterFilter{
				config:                 &filterapi.RuntimeConfig{Guardrails: []filterapi.RuntimeGuardrail{test.guardrail}},
				logger:                 slog.Default(),
				originalRequestBodyRaw: body,
				originalModel:          "test",
				guardrailMetrics:       recorder,
				span:                   span,
			}
			p := &chatCompletionProcessorUpstreamFilter{
				parent:         parent,
				logger:         slog.Default(),
				requestHeaders: map[string]string{":path": "/v1/chat/completions"},
				metrics:        &mockMetrics{},
				backendName:    "backend-a",
				// Stop right after the guardrail evaluation, before translation.
				unsupportedBackendErr: errors.New("stop after guardrails"),
			}

			response, err := p.ProcessRequestHeaders(t.Context(), nil)
			require.Equal(t, test.wantResult, recorder.result)
			require.Len(t, span.guardrailEvents, 1)
			if test.wantErr != "" {
				require.ErrorContains(t, err, "failed to evaluate backend request guardrails")
				return
			}
			require.NoError(t, err)
			if test.wantBlock {
				require.Contains(t, string(response.GetImmediateResponse().GetBody()), "GuardrailViolation")
				require.Equal(t, 1, span.endedOnErrorCount)
				return
			}
			require.Contains(t, string(response.GetImmediateResponse().GetBody()), "stop after guardrails")
			if test.wantResult == metrics.GuardrailResultMasked {
				require.Contains(t, string(parent.originalRequestBodyRaw), "my [MASKED]")
				require.True(t, parent.forceBodyMutation)
			}
		})
	}
}

func TestUpstreamResponseGuardrailOutcomes(t *testing.T) {
	responseBody := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"my secret"}}]}`)
	for _, test := range guardrailProcessorCases(filterapi.GuardrailPhaseResponse, []string{"backend-a"}) {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingGuardrailMetrics{}
			span := &mockGuardrailChatCompletionSpan{}
			p := &chatCompletionProcessorUpstreamFilter{
				parent: &chatCompletionProcessorRouterFilter{
					config:           &filterapi.RuntimeConfig{Guardrails: []filterapi.RuntimeGuardrail{test.guardrail}},
					logger:           slog.Default(),
					guardrailMetrics: recorder,
					span:             span,
				},
				logger:          slog.Default(),
				translator:      &mockTranslator{t: t, retBodyMutation: responseBody},
				metrics:         &mockMetrics{},
				backendName:     "backend-a",
				responseHeaders: map[string]string{":status": "200"},
			}

			response, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: responseBody, EndOfStream: true})
			require.Equal(t, test.wantResult, recorder.result)
			require.Len(t, span.guardrailEvents, 1)
			if test.wantErr != "" {
				require.ErrorContains(t, err, "failed to evaluate response guardrails")
				return
			}
			require.NoError(t, err)
			if test.wantBlock {
				require.Contains(t, string(response.GetImmediateResponse().GetBody()), "GuardrailViolation")
				return
			}
			returnedBody := response.GetResponseBody().GetResponse().GetBodyMutation().GetBody()
			if test.wantResult == metrics.GuardrailResultMasked {
				require.Contains(t, string(returnedBody), "my [MASKED]")
				return
			}
			require.Equal(t, responseBody, returnedBody)
		})
	}
}

func TestEvaluateGuardrailsForPhaseEdgeCases(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"my secret"}]}`)

	t.Run("regex block without match", func(t *testing.T) {
		outcome, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
			Name: "deny", Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex},
			Matcher:  regexp.MustCompile("forbidden"),
		}}, body)
		require.NoError(t, err)
		require.Nil(t, outcome.Violation)
		require.False(t, outcome.Monitored)
	})

	t.Run("regex mask uses default replacement", func(t *testing.T) {
		outcome, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
			Name: "mask", Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex, Action: filterapi.GuardrailActionMask},
			Matcher:  regexp.MustCompile("secret"),
		}}, body)
		require.NoError(t, err)
		require.True(t, outcome.Masked)
		require.Contains(t, string(outcome.Body), "my [REDACTED]")
	})

	t.Run("regex mask without compiled matcher", func(t *testing.T) {
		_, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
			Name: "mask", Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeRegex, Action: filterapi.GuardrailActionMask},
		}}, body)
		require.ErrorContains(t, err, `guardrail "mask" uses regex provider without a compiled matcher`)
	})

	t.Run("external provider without evaluator", func(t *testing.T) {
		_, err := evaluateRequestGuardrails(t.Context(), []filterapi.RuntimeGuardrail{{
			Name: "presidio", Phase: filterapi.GuardrailPhaseRequest,
			Provider: filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypePresidio},
		}}, body)
		require.ErrorContains(t, err, `guardrail "presidio" uses provider "Presidio" without an evaluator`)
	})
}

func TestReplaceGuardrailContentWholeBody(t *testing.T) {
	replaced, err := replaceGuardrailContent([]byte("plain text"), guardrailContent{}, []byte("masked"))
	require.NoError(t, err)
	require.Equal(t, "masked", string(replaced))
}
