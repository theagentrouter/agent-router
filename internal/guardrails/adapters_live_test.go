// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestLivePresidio(t *testing.T) {
	endpoint := os.Getenv("TEST_PRESIDIO_ENDPOINT")
	blockedText := os.Getenv("TEST_PRESIDIO_BLOCKED_TEXT")
	if endpoint == "" || blockedText == "" {
		t.Skip("TEST_PRESIDIO_ENDPOINT and TEST_PRESIDIO_BLOCKED_TEXT are not set")
	}
	provider := &filterapi.GuardrailProvider{
		Type: filterapi.GuardrailProviderTypePresidio,
		Presidio: &filterapi.PresidioGuardrailProvider{
			Endpoint: endpoint,
			APIKey:   os.Getenv("TEST_PRESIDIO_API_KEY"),
		},
	}
	evaluator, err := NewEvaluator(t.Context(), provider)
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte(blockedText), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
}

func TestLiveAzureContentSafety(t *testing.T) {
	endpoint, apiKey := os.Getenv("TEST_AZURE_CONTENT_SAFETY_ENDPOINT"), os.Getenv("TEST_AZURE_CONTENT_SAFETY_API_KEY")
	blockedText := os.Getenv("TEST_AZURE_CONTENT_SAFETY_BLOCKED_TEXT")
	if endpoint == "" || apiKey == "" || blockedText == "" {
		t.Skip("TEST_AZURE_CONTENT_SAFETY_ENDPOINT, TEST_AZURE_CONTENT_SAFETY_API_KEY, and TEST_AZURE_CONTENT_SAFETY_BLOCKED_TEXT are not set")
	}
	threshold := int32(4)
	provider := &filterapi.GuardrailProvider{
		Type: filterapi.GuardrailProviderTypeAzureContentSafety,
		AzureContentSafety: &filterapi.AzureContentSafetyGuardrailProvider{
			Endpoint: endpoint, APIKey: apiKey, SeverityThreshold: &threshold,
		},
	}
	evaluator, err := NewEvaluator(t.Context(), provider)
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte(blockedText), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
}

func TestLiveBedrockGuardrail(t *testing.T) {
	region := os.Getenv("TEST_AWS_BEDROCK_GUARDRAIL_REGION")
	identifier := os.Getenv("TEST_AWS_BEDROCK_GUARDRAIL_ID")
	version := os.Getenv("TEST_AWS_BEDROCK_GUARDRAIL_VERSION")
	blockedText := os.Getenv("TEST_AWS_BEDROCK_GUARDRAIL_BLOCKED_TEXT")
	if region == "" || identifier == "" || version == "" || blockedText == "" {
		t.Skip("Bedrock guardrail configuration and TEST_AWS_BEDROCK_GUARDRAIL_BLOCKED_TEXT are not set")
	}
	provider := &filterapi.GuardrailProvider{
		Type: filterapi.GuardrailProviderTypeBedrockGuardrails,
		Bedrock: &filterapi.BedrockGuardrailProvider{
			Region: region, GuardrailIdentifier: identifier, GuardrailVersion: version,
		},
	}
	evaluator, err := NewEvaluator(t.Context(), provider)
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte(blockedText), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
}
