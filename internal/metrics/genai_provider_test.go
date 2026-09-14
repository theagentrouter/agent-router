// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/tracing/otelgenai"
)

// TestGenAIProviders_matchTracing pins the provider values emitted by metrics
// against the ones the OTel GenAI tracing convention records, so spans and
// metrics stay joinable on gen_ai.provider.name. It lives here rather than in
// otelgenai because the metrics constants are unexported.
func TestGenAIProviders_matchTracing(t *testing.T) {
	for _, tc := range []struct {
		metric string
		span   otelgenai.Provider
	}{
		{genaiProviderOpenAI, otelgenai.ProviderOpenAI},
		{genaiProviderAzureOpenAI, otelgenai.ProviderAzureOpenAI},
		{genaiProviderAWSBedrock, otelgenai.ProviderAWSBedrock},
		{genaiProviderAWSAnthropic, otelgenai.ProviderAWSAnthropic},
		{genaiProviderGCPVertexAI, otelgenai.ProviderGCPVertexAI},
		{genaiProviderGCPAnthropic, otelgenai.ProviderGCPAnthropic},
		{genaiProviderAnthropic, otelgenai.ProviderAnthropic},
		{genaiProviderCohere, otelgenai.ProviderCohere},
	} {
		t.Run(tc.metric, func(t *testing.T) {
			require.Equal(t, tc.metric, string(tc.span))
		})
	}
}

// TestGenAIAttributeProviderName_matchTracing ensures both signals key the
// provider on the same attribute name.
func TestGenAIAttributeProviderName_matchTracing(t *testing.T) {
	require.Equal(t, genaiAttributeProviderName, otelgenai.ProviderName)
}
