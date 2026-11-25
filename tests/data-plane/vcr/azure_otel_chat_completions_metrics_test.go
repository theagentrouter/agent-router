// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package vcr

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/testopenai"
)

func TestOtelAzureOpenAIChatCompletions_metrics(t *testing.T) {
	env := setupOtelTestEnvironment(t)
	listenerPort := env.listenerPort

	// Use standard OpenAI path but set X-Cassette-Name header to azure-chat-basic
	req, err := testopenai.NewRequest(t.Context(), fmt.Sprintf("http://localhost:%d", listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)
	req.Header.Set(testopenai.CassetteNameHeader, testopenai.CassetteAzureChatBasic.String())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode, "Response body: %s", string(body))

	span := env.collector.TakeSpan()
	require.NotNil(t, span)

	expectedCount := 2 // token usage + request duration
	allMetrics := env.collector.TakeMetrics(expectedCount)
	metrics := requireScopeMetrics(t, allMetrics)

	originalModel := getInvocationModel(span.Attributes, "llm.invocation_parameters")
	requestModel := originalModel
	responseModel := getSpanAttributeString(span.Attributes, "llm.model_name")

	verifyTokenUsageMetricsWithProvider(t, "chat", "azure.openai", "azure-openai", metrics, span, originalModel, requestModel, responseModel, false)
	verifyRequestDurationMetricsWithProvider(t, "chat", "azure.openai", "azure-openai", metrics, span, originalModel, requestModel, responseModel, false)
}
