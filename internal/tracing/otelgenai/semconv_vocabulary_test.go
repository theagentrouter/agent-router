// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// This file is the only place the versioned semconv package is referenced.
// Production code hardcodes attribute names so a dependency bump cannot
// silently rename what we emit and break users' dashboards. Pinning the names
// against semconv here instead means an upstream rename surfaces as a test
// failure that names the change, which is what we actually want to know.

// TestAttributeNames_matchSemconv pins every attribute name we emit to the
// spec. A mismatch means either a typo on our side or a spec change.
func TestAttributeNames_matchSemconv(t *testing.T) {
	tests := []struct {
		ours     string
		expected string
	}{
		{ours: OperationName, expected: string(semconv.GenAIOperationNameKey)},
		{ours: ProviderName, expected: string(semconv.GenAIProviderNameKey)},
		{ours: ConversationID, expected: string(semconv.GenAIConversationIDKey)},

		{ours: RequestModel, expected: string(semconv.GenAIRequestModelKey)},
		{ours: RequestMaxTokens, expected: string(semconv.GenAIRequestMaxTokensKey)},
		{ours: RequestTemperature, expected: string(semconv.GenAIRequestTemperatureKey)},
		{ours: RequestTopP, expected: string(semconv.GenAIRequestTopPKey)},
		{ours: RequestTopK, expected: string(semconv.GenAIRequestTopKKey)},
		{ours: RequestFrequencyPenalty, expected: string(semconv.GenAIRequestFrequencyPenaltyKey)},
		{ours: RequestPresencePenalty, expected: string(semconv.GenAIRequestPresencePenaltyKey)},
		{ours: RequestStopSequences, expected: string(semconv.GenAIRequestStopSequencesKey)},
		{ours: RequestSeed, expected: string(semconv.GenAIRequestSeedKey)},
		{ours: RequestChoiceCount, expected: string(semconv.GenAIRequestChoiceCountKey)},
		{ours: RequestEncodingFormats, expected: string(semconv.GenAIRequestEncodingFormatsKey)},

		{ours: ResponseID, expected: string(semconv.GenAIResponseIDKey)},
		{ours: ResponseModel, expected: string(semconv.GenAIResponseModelKey)},
		{ours: ResponseFinishReasons, expected: string(semconv.GenAIResponseFinishReasonsKey)},

		{ours: UsageInputTokens, expected: string(semconv.GenAIUsageInputTokensKey)},
		{ours: UsageOutputTokens, expected: string(semconv.GenAIUsageOutputTokensKey)},
		{ours: UsageCacheReadInputTokens, expected: string(semconv.GenAIUsageCacheReadInputTokensKey)},
		{ours: UsageCacheCreationInputTokens, expected: string(semconv.GenAIUsageCacheCreationInputTokensKey)},
		{ours: UsageReasoningOutputTokens, expected: string(semconv.GenAIUsageReasoningOutputTokensKey)},

		{ours: InputMessages, expected: string(semconv.GenAIInputMessagesKey)},
		{ours: OutputMessages, expected: string(semconv.GenAIOutputMessagesKey)},
		{ours: SystemInstructions, expected: string(semconv.GenAISystemInstructionsKey)},
		{ours: ToolDefinitions, expected: string(semconv.GenAIToolDefinitionsKey)},

		{ours: EmbeddingsDimensionCount, expected: string(semconv.GenAIEmbeddingsDimensionCountKey)},

		{ours: ErrorType, expected: string(semconv.ErrorTypeKey)},
	}

	for _, tc := range tests {
		t.Run(tc.ours, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.ours)
		})
	}
}

// TestOperations_matchSemconv pins the operations that have a registry value.
func TestOperations_matchSemconv(t *testing.T) {
	tests := []struct {
		ours     Operation
		expected string
	}{
		{ours: OperationChat, expected: semconv.GenAIOperationNameChat.Value.AsString()},
		{ours: OperationTextCompletion, expected: semconv.GenAIOperationNameTextCompletion.Value.AsString()},
		{ours: OperationEmbeddings, expected: semconv.GenAIOperationNameEmbeddings.Value.AsString()},
	}

	for _, tc := range tests {
		t.Run(string(tc.ours), func(t *testing.T) {
			require.Equal(t, tc.expected, string(tc.ours))
		})
	}
}
