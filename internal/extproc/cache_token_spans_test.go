// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

type cacheUsageChatCompletionSpan struct {
	mockChatCompletionSpan
	counts      [][2]uint32
	endedAtCall []int
}

func (s *cacheUsageChatCompletionSpan) RecordCacheTokenUsage(usage tracingapi.CacheTokenUsage) {
	fiveMinute, _ := usage.CacheCreation5mInputTokens()
	oneHour, _ := usage.CacheCreation1hInputTokens()
	s.counts = append(s.counts, [2]uint32{fiveMinute, oneHour})
	s.endedAtCall = append(s.endedAtCall, s.endedCount)
}

func TestProcessResponseBody_CacheTokenUsageSpan(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		name := "non-streaming"
		if streamed {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			span := &cacheUsageChatCompletionSpan{}
			body := &extprocv3.HttpBody{Body: []byte("response"), EndOfStream: !streamed}
			tr := &mockTranslator{t: t, expResponseBody: body}
			tr.retUsedToken.SetCacheCreation5mInputTokens(5)
			tr.retUsedToken.SetCacheCreation1hInputTokens(2)
			processor := &chatCompletionProcessorUpstreamFilter{
				translator:      tr,
				metrics:         &mockMetrics{},
				responseHeaders: map[string]string{":status": "200"},
				parent: &chatCompletionProcessorRouterFilter{
					stream: streamed,
					span:   span,
					config: &filterapi.RuntimeConfig{},
				},
			}
			_, err := processor.ProcessResponseBody(t.Context(), body)
			require.NoError(t, err)
			require.Equal(t, [][2]uint32{{5, 2}}, span.counts)
			require.Equal(t, []int{0}, span.endedAtCall)
			if streamed {
				body.EndOfStream = true
				tr.retUsedToken = metrics.TokenUsage{}
				tr.retUsedToken.SetOutputTokens(8)
				_, err = processor.ProcessResponseBody(t.Context(), body)
				require.NoError(t, err)
				require.Equal(t, [][2]uint32{{5, 2}, {5, 2}}, span.counts)
				require.Equal(t, []int{0, 0}, span.endedAtCall)
			}
			require.Equal(t, 1, span.endedCount)
		})
	}
}
