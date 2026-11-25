// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openinference_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference/openai"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func TestCacheTokenUsageRecorder(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*metrics.TokenUsage)
		want []attribute.KeyValue
	}{
		{
			name: "reported split",
			set: func(u *metrics.TokenUsage) {
				u.SetCacheCreation5mInputTokens(5)
				u.SetCacheCreation1hInputTokens(1000)
			},
			want: []attribute.KeyValue{
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite5m, 5),
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite1h, 1000),
			},
		},
		{
			name: "explicit zero",
			set: func(u *metrics.TokenUsage) {
				u.SetCacheCreation5mInputTokens(0)
				u.SetCacheCreation1hInputTokens(0)
			},
			want: []attribute.KeyValue{
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite5m, 0),
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite1h, 0),
			},
		},
		{
			name: "aggregate does not imply split",
			set:  func(u *metrics.TokenUsage) { u.SetCacheCreationInputTokens(1005) },
		},
		{
			name: "independent provider values",
			set: func(u *metrics.TokenUsage) {
				u.SetCacheCreationInputTokens(9)
				u.SetCacheCreation5mInputTokens(5)
				u.SetCacheCreation1hInputTokens(1000)
			},
			want: []attribute.KeyValue{
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite5m, 5),
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite1h, 1000),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &metrics.TokenUsage{}
			tc.set(usage)
			span := testotel.RecordWithSpan(t, func(s trace.Span) bool {
				openinference.CacheTokenUsageRecorder{}.RecordCacheTokenUsage(s, usage)
				return false
			})
			require.Equal(t, tc.want, span.Attributes)
		})
	}
}

func TestCacheTokenUsageRecorder_PreservesCumulativeUpdates(t *testing.T) {
	span := testotel.RecordWithSpan(t, func(s trace.Span) bool {
		usage := &metrics.TokenUsage{}
		usage.SetCacheCreation5mInputTokens(5)
		usage.SetCacheCreation1hInputTokens(2)
		recorder := openinference.CacheTokenUsageRecorder{}
		recorder.RecordCacheTokenUsage(s, usage)
		var later metrics.TokenUsage
		later.SetOutputTokens(8)
		usage.Override(later)
		recorder.RecordCacheTokenUsage(s, usage)
		later.SetCacheCreation1hInputTokens(3)
		usage.Override(later)
		recorder.RecordCacheTokenUsage(s, usage)
		return false
	})
	require.Equal(t, []attribute.KeyValue{
		attribute.Int64(openinference.LLMTokenCountPromptCacheWrite5m, 5),
		attribute.Int64(openinference.LLMTokenCountPromptCacheWrite1h, 3),
	}, span.Attributes)
}

func TestCacheTokenUsageRecorder_EndpointRecorders(t *testing.T) {
	config := &openinference.TraceConfig{HideInputs: true, HideOutputs: true}
	for _, tc := range []struct {
		name     string
		recorder any
	}{
		{name: "native messages", recorder: anthropic.NewMessageRecorder(config)},
		{name: "translated chat completions", recorder: openai.NewChatCompletionRecorder(config)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, ok := tc.recorder.(tracingapi.CacheTokenUsageRecorder)
			require.True(t, ok)
			usage := &metrics.TokenUsage{}
			usage.SetCacheCreation1hInputTokens(2)
			span := testotel.RecordWithSpan(t, func(s trace.Span) bool {
				recorder.RecordCacheTokenUsage(s, usage)
				return false
			})
			require.Equal(t, []attribute.KeyValue{
				attribute.Int64(openinference.LLMTokenCountPromptCacheWrite1h, 2),
			}, span.Attributes)
		})
	}
}
