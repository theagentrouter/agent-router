// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestChatRequestAttrs(t *testing.T) {
	tests := []struct {
		name     string
		req      *openai.ChatCompletionRequest
		expected []attribute.KeyValue
	}{
		{
			name:     "no parameters records nothing",
			req:      &openai.ChatCompletionRequest{},
			expected: nil,
		},
		{
			name: "all parameters",
			req: &openai.ChatCompletionRequest{
				Temperature:      ptr(0.7),
				TopP:             ptr(0.9),
				FrequencyPenalty: ptr(float32(0.5)),
				PresencePenalty:  ptr(float32(0.25)),
				Seed:             ptr(42),
				N:                ptr(2),
				MaxTokens:        ptr(int64(256)),
			},
			expected: []attribute.KeyValue{
				attribute.Float64(RequestTemperature, 0.7),
				attribute.Float64(RequestTopP, 0.9),
				attribute.Float64(RequestFrequencyPenalty, 0.5),
				attribute.Float64(RequestPresencePenalty, 0.25),
				attribute.Int(RequestSeed, 42),
				attribute.Int(RequestChoiceCount, 2),
				attribute.Int64(RequestMaxTokens, 256),
			},
		},
		{
			// Zero is a meaningful temperature, so it must be recorded rather
			// than treated as absent.
			name: "zero temperature is recorded",
			req:  &openai.ChatCompletionRequest{Temperature: ptr(0.0)},
			expected: []attribute.KeyValue{
				attribute.Float64(RequestTemperature, 0),
			},
		},
		{
			name: "max_completion_tokens supersedes max_tokens",
			req: &openai.ChatCompletionRequest{
				MaxTokens:           ptr(int64(100)),
				MaxCompletionTokens: ptr(int64(200)),
			},
			expected: []attribute.KeyValue{
				attribute.Int64(RequestMaxTokens, 200),
			},
		},
		{
			name: "max_tokens used when max_completion_tokens absent",
			req:  &openai.ChatCompletionRequest{MaxTokens: ptr(int64(100))},
			expected: []attribute.KeyValue{
				attribute.Int64(RequestMaxTokens, 100),
			},
		},
		{
			name: "single stop string becomes a list",
			req: &openai.ChatCompletionRequest{
				Stop: openaigo.ChatCompletionNewParamsStopUnion{OfString: param.NewOpt("END")},
			},
			expected: []attribute.KeyValue{
				attribute.StringSlice(RequestStopSequences, []string{"END"}),
			},
		},
		{
			name: "stop array",
			req: &openai.ChatCompletionRequest{
				Stop: openaigo.ChatCompletionNewParamsStopUnion{OfStringArray: []string{"A", "B"}},
			},
			expected: []attribute.KeyValue{
				attribute.StringSlice(RequestStopSequences, []string{"A", "B"}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, chatRequestAttrs(tc.req))
		})
	}
}

func TestCompletionRequestAttrs(t *testing.T) {
	tests := []struct {
		name     string
		req      *openai.CompletionRequest
		expected []attribute.KeyValue
	}{
		{name: "no parameters", req: &openai.CompletionRequest{}, expected: nil},
		{
			name: "parameters",
			req: &openai.CompletionRequest{
				Temperature: ptr(0.3),
				MaxTokens:   ptr(64),
				Seed:        ptr(int64(7)),
			},
			expected: []attribute.KeyValue{
				attribute.Float64(RequestTemperature, 0.3),
				attribute.Int64(RequestSeed, 7),
				attribute.Int(RequestMaxTokens, 64),
			},
		},
		{
			name: "stop as plain string",
			req:  &openai.CompletionRequest{Stop: "END"},
			expected: []attribute.KeyValue{
				attribute.StringSlice(RequestStopSequences, []string{"END"}),
			},
		},
		{
			name: "stop as any slice",
			req:  &openai.CompletionRequest{Stop: []any{"A", "B"}},
			expected: []attribute.KeyValue{
				attribute.StringSlice(RequestStopSequences, []string{"A", "B"}),
			},
		},
		{
			name: "stop as string slice",
			req:  &openai.CompletionRequest{Stop: []string{"A", "B"}},
			expected: []attribute.KeyValue{
				attribute.StringSlice(RequestStopSequences, []string{"A", "B"}),
			},
		},
		{
			name:     "empty stop string is omitted",
			req:      &openai.CompletionRequest{Stop: ""},
			expected: nil,
		},
		{
			name:     "unsupported stop type is omitted",
			req:      &openai.CompletionRequest{Stop: 42},
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, completionRequestAttrs(tc.req))
		})
	}
}

func TestUsageDetailAttrs(t *testing.T) {
	tests := []struct {
		name                                string
		cacheRead, cacheCreation, reasoning int
		expected                            []attribute.KeyValue
	}{
		{name: "all absent", expected: nil},
		{
			name: "all present", cacheRead: 10, cacheCreation: 5, reasoning: 3,
			expected: []attribute.KeyValue{
				attribute.Int(UsageCacheReadInputTokens, 10),
				attribute.Int(UsageCacheCreationInputTokens, 5),
				attribute.Int(UsageReasoningOutputTokens, 3),
			},
		},
		{
			name: "zero counts are omitted", cacheRead: 0, cacheCreation: 5, reasoning: 0,
			expected: []attribute.KeyValue{
				attribute.Int(UsageCacheCreationInputTokens, 5),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, usageDetailAttrs(tc.cacheRead, tc.cacheCreation, tc.reasoning))
		})
	}
}

// TestChatCompletionRecorder_paramsRecordedWithoutContent pins that sampling
// parameters are metadata: they appear even when content capture is off.
func TestChatCompletionRecorder_paramsRecordedWithoutContent(t *testing.T) {
	r := NewChatCompletionRecorder(NewConfig())

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, &openai.ChatCompletionRequest{
			Model:       "gpt-5-nano",
			Temperature: ptr(0.7),
			Messages:    []openai.ChatCompletionMessageParamUnion{userMessage("secret")},
		}, nil)
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.String(OperationName, "chat"),
		attribute.String(RequestModel, "gpt-5-nano"),
		attribute.Float64(RequestTemperature, 0.7),
	}, span.Attributes)
}

// TestChatCompletionRecorder_usageDetails pins the cache and reasoning
// breakdowns end to end through the recorder.
func TestChatCompletionRecorder_usageDetails(t *testing.T) {
	r := NewChatCompletionRecorder(NewConfig())

	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordResponse(span, &openai.ChatCompletionResponse{
			Usage: openai.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				PromptTokensDetails: &openai.PromptTokensDetails{
					CachedTokens:        80,
					CacheCreationTokens: 20,
				},
				CompletionTokensDetails: &openai.CompletionTokensDetails{
					ReasoningTokens: 30,
				},
			},
		})
		return false
	})

	testotel.RequireAttributesEqual(t, []attribute.KeyValue{
		attribute.Int(UsageInputTokens, 100),
		attribute.Int(UsageOutputTokens, 50),
		attribute.Int(UsageCacheReadInputTokens, 80),
		attribute.Int(UsageCacheCreationInputTokens, 20),
		attribute.Int(UsageReasoningOutputTokens, 30),
	}, span.Attributes)
}
