// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// scriptedGuardrailEvaluator rejects payloads longer than limit as too large, returns the queued
// errors first, and otherwise evaluates the payload with result.
type scriptedGuardrailEvaluator struct {
	limit  int
	errs   []error
	calls  []string
	result func(body []byte) filterapi.GuardrailEvaluationResult
}

func (e *scriptedGuardrailEvaluator) Evaluate(_ context.Context, body []byte, _ filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	e.calls = append(e.calls, string(body))
	if len(e.errs) > 0 {
		err := e.errs[0]
		e.errs = e.errs[1:]
		return filterapi.GuardrailEvaluationResult{}, err
	}
	if e.limit > 0 && len(body) > e.limit {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("provider returned HTTP 413: %w", filterapi.ErrGuardrailPayloadTooLarge)
	}
	if e.result == nil {
		return filterapi.GuardrailEvaluationResult{}, nil
	}
	return e.result(body), nil
}

func recordGuardrailRetrySleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var delays []time.Duration
	original := guardrailRetrySleep
	guardrailRetrySleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}
	t.Cleanup(func() { guardrailRetrySleep = original })
	return &delays
}

func TestGuardrailSplitPoint(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{text: "aaaa bbbb cccc", want: 10},
		{text: "aa bbbbbbbb cc", want: 12},
		{text: "nowhitespace", want: -1},
		{text: "trailing ", want: -1},
		{text: " leading", want: 1},
		{text: "", want: -1},
		{text: "ñañá ñañá", want: len("ñañá ")},
	} {
		t.Run(tc.text, func(t *testing.T) {
			require.Equal(t, tc.want, guardrailSplitPoint([]byte(tc.text)))
		})
	}
}

func TestBisectGuardrailFragments(t *testing.T) {
	t.Run("several fragments split the list", func(t *testing.T) {
		fragments := []guardrailFragment{{content: 0, text: []byte("a")}, {content: 1, text: []byte("b")}, {content: 2, text: []byte("c")}}
		left, right, ok := bisectGuardrailFragments(fragments)
		require.True(t, ok)
		require.Equal(t, fragments[:1], left)
		require.Equal(t, fragments[1:], right)
	})
	t.Run("single fragment splits at whitespace and keeps its content", func(t *testing.T) {
		left, right, ok := bisectGuardrailFragments([]guardrailFragment{{content: 7, text: []byte("hello big world")}})
		require.True(t, ok)
		require.Equal(t, []guardrailFragment{{content: 7, text: []byte("hello ")}}, left)
		require.Equal(t, []guardrailFragment{{content: 7, text: []byte("big world")}}, right)
	})
	t.Run("no valid split", func(t *testing.T) {
		_, _, ok := bisectGuardrailFragments([]guardrailFragment{{text: []byte("unsplittable")}})
		require.False(t, ok)
		_, _, ok = bisectGuardrailFragments(nil)
		require.False(t, ok)
	})
}

func TestEvaluateGuardrailWithRetry(t *testing.T) {
	throttled := fmt.Errorf("provider returned HTTP 429: %w", filterapi.ErrGuardrailThrottled)

	t.Run("retries throttling with exponential backoff", func(t *testing.T) {
		delays := recordGuardrailRetrySleeps(t)
		evaluator := &scriptedGuardrailEvaluator{errs: []error{throttled, throttled}}
		_, err := evaluateGuardrailWithRetry(t.Context(), evaluator, []byte("x"), filterapi.GuardrailPhaseRequest)
		require.NoError(t, err)
		require.Len(t, evaluator.calls, 3)
		require.Equal(t, []time.Duration{500 * time.Millisecond, time.Second}, *delays)
	})

	t.Run("gives up after three retries", func(t *testing.T) {
		delays := recordGuardrailRetrySleeps(t)
		evaluator := &scriptedGuardrailEvaluator{errs: []error{throttled, throttled, throttled, throttled, throttled}}
		_, err := evaluateGuardrailWithRetry(t.Context(), evaluator, []byte("x"), filterapi.GuardrailPhaseRequest)
		require.ErrorIs(t, err, filterapi.ErrGuardrailThrottled)
		require.Len(t, evaluator.calls, 4)
		require.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}, *delays)
	})

	t.Run("does not retry size or unrecoverable errors", func(t *testing.T) {
		delays := recordGuardrailRetrySleeps(t)
		for _, err := range []error{filterapi.ErrGuardrailPayloadTooLarge, errors.New("bad credentials")} {
			evaluator := &scriptedGuardrailEvaluator{errs: []error{err}}
			_, got := evaluateGuardrailWithRetry(t.Context(), evaluator, []byte("x"), filterapi.GuardrailPhaseRequest)
			require.ErrorIs(t, got, err)
			require.Len(t, evaluator.calls, 1)
		}
		require.Empty(t, *delays)
	})

	t.Run("stops when the context is done", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		evaluator := &scriptedGuardrailEvaluator{errs: []error{throttled, throttled}}
		_, err := evaluateGuardrailWithRetry(ctx, evaluator, []byte("x"), filterapi.GuardrailPhaseRequest)
		require.ErrorIs(t, err, filterapi.ErrGuardrailThrottled)
		require.ErrorIs(t, err, context.Canceled)
		require.Len(t, evaluator.calls, 1)
	})
}

func TestExternalGuardrailBisection(t *testing.T) {
	guardrail := func(action filterapi.GuardrailAction, evaluator filterapi.GuardrailEvaluator) []filterapi.RuntimeGuardrail {
		return []filterapi.RuntimeGuardrail{{
			Name: "bedrock", Phase: filterapi.GuardrailPhaseRequest,
			Provider:  filterapi.GuardrailProvider{Type: filterapi.GuardrailProviderTypeBedrockGuardrails, Action: action},
			Evaluator: evaluator,
		}}
	}
	matchBad := func(b []byte) filterapi.GuardrailEvaluationResult {
		if !bytes.Contains(b, []byte("bad")) {
			return filterapi.GuardrailEvaluationResult{}
		}
		return filterapi.GuardrailEvaluationResult{Matched: true, Replacement: bytes.ReplaceAll(b, []byte("bad"), []byte("***"))}
	}

	t.Run("block splits fields and stops at the first match", func(t *testing.T) {
		body := []byte(`{"messages":[{"content":"first bad field"},{"content":"second field"},{"content":"third field"},{"content":"fourth field"}]}`)
		evaluator := &scriptedGuardrailEvaluator{limit: 40, result: matchBad}
		outcome, err := evaluateRequestGuardrails(t.Context(), guardrail(filterapi.GuardrailActionBlock, evaluator), body)
		require.NoError(t, err)
		require.NotNil(t, outcome.Violation)
		// Whole payload rejected, then the left half matched; the right half is never sent.
		require.Len(t, evaluator.calls, 2)
		require.NotContains(t, evaluator.calls[1], "third field")
	})

	t.Run("block allows when every partition is clean", func(t *testing.T) {
		body := []byte(`{"messages":[{"content":"` + strings.Repeat("clean words ", 20) + `"}]}`)
		evaluator := &scriptedGuardrailEvaluator{limit: 50, result: matchBad}
		outcome, err := evaluateRequestGuardrails(t.Context(), guardrail(filterapi.GuardrailActionBlock, evaluator), body)
		require.NoError(t, err)
		require.Nil(t, outcome.Violation)
		for _, call := range evaluator.calls[1:] {
			require.NotEmpty(t, call)
		}
	})

	t.Run("mask reassembles split text into its field", func(t *testing.T) {
		long := strings.Repeat("ok ", 10) + "bad " + strings.Repeat("ok ", 10) + "bad end"
		body := []byte(`{"messages":[{"content":"short bad"},{"content":"` + long + `"}]}`)
		evaluator := &scriptedGuardrailEvaluator{limit: 30, result: matchBad}
		outcome, err := evaluateRequestGuardrails(t.Context(), guardrail(filterapi.GuardrailActionMask, evaluator), body)
		require.NoError(t, err)
		require.True(t, outcome.Masked)
		expLong := strings.ReplaceAll(long, "bad", "***")
		require.JSONEq(t, `{"messages":[{"content":"short ***"},{"content":"`+expLong+`"}]}`, string(outcome.Body))
	})

	t.Run("propagates the original error when no split is possible", func(t *testing.T) {
		body := []byte(`{"messages":[{"content":"` + strings.Repeat("x", 64) + `"}]}`)
		evaluator := &scriptedGuardrailEvaluator{limit: 16}
		_, err := evaluateRequestGuardrails(t.Context(), guardrail(filterapi.GuardrailActionBlock, evaluator), body)
		require.ErrorIs(t, err, filterapi.ErrGuardrailPayloadTooLarge)
		require.False(t, isGuardrailFailOpenError(err))
		require.Len(t, evaluator.calls, 1)
	})

	t.Run("throttled partitions are retried", func(t *testing.T) {
		delays := recordGuardrailRetrySleeps(t)
		body := []byte(`{"messages":[{"content":"clean field one"},{"content":"clean field two"}]}`)
		evaluator := &scriptedGuardrailEvaluator{
			limit: 20,
			errs:  []error{fmt.Errorf("provider returned HTTP 429: %w", filterapi.ErrGuardrailThrottled)},
		}
		outcome, err := evaluateRequestGuardrails(t.Context(), guardrail(filterapi.GuardrailActionBlock, evaluator), body)
		require.NoError(t, err)
		require.Nil(t, outcome.Violation)
		require.Equal(t, []time.Duration{500 * time.Millisecond}, *delays)
	})
}
