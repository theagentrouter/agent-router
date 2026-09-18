// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestPresidioEvaluatorHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/analyze", req.URL.Path)
		require.Equal(t, "Bearer secret", req.Header.Get("Authorization"))
		var body struct {
			Text           string  `json:"text"`
			Language       string  `json:"language"`
			ScoreThreshold float64 `json:"score_threshold"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, "customer SSN", body.Text)
		require.Equal(t, "es", body.Language)
		require.Equal(t, 0.75, body.ScoreThreshold)
		_, _ = w.Write([]byte(`[{"entity_type":"US_SSN","start":9,"end":12,"score":0.98}]`))
	}))
	t.Cleanup(server.Close)

	evaluator, err := newPresidioEvaluator(&filterapi.PresidioGuardrailProvider{
		Endpoint: server.URL, Language: "es", ScoreThresholdPercent: 75, APIKey: "secret",
	}, "[REDACTED]", server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("customer SSN"), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
	require.Equal(t, "customer [REDACTED]", string(evaluation.Replacement))
}

func TestAzureContentSafetyEvaluatorHTTP(t *testing.T) {
	severityThreshold := int32(4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/contentsafety/text:analyze", req.URL.Path)
		require.Equal(t, "2024-09-01", req.URL.Query().Get("api-version"))
		require.Equal(t, "azure-secret", req.Header.Get("Ocp-Apim-Subscription-Key"))
		_, _ = w.Write([]byte(`{"categoriesAnalysis":[{"category":"Violence","severity":6}]}`))
	}))
	t.Cleanup(server.Close)

	evaluator, err := newAzureContentSafetyEvaluator(&filterapi.AzureContentSafetyGuardrailProvider{
		Endpoint: server.URL, APIKey: "azure-secret", SeverityThreshold: &severityThreshold,
	}, server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("unsafe response"), filterapi.GuardrailPhaseResponse)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
}

func TestBedrockEvaluatorHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/guardrail/guardrail-id/version/1/apply", req.URL.Path)
		require.Contains(t, req.Header.Get("Authorization"), "Credential=AKIDEXAMPLE/")
		require.NotEmpty(t, req.Header.Get("X-Amz-Date"))
		var body struct {
			Source  string `json:"source"`
			Content []struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"content"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, "OUTPUT", body.Source)
		require.Equal(t, "unsafe response", body.Content[0].Text.Text)
		_, _ = w.Write([]byte(`{"action":"GUARDRAIL_INTERVENED","outputs":[{"text":"safe response"}]}`))
	}))
	t.Cleanup(server.Close)

	evaluator, err := newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{
		Endpoint: server.URL, Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
		CredentialFileLiteral: strings.TrimSpace(`
[default]
aws_access_key_id = AKIDEXAMPLE
aws_secret_access_key = secret
`),
	}, server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("unsafe response"), filterapi.GuardrailPhaseResponse)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
	require.Equal(t, "safe response", string(evaluation.Replacement))
}

func TestEvaluatorProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	evaluator, err := newPresidioEvaluator(&filterapi.PresidioGuardrailProvider{Endpoint: server.URL}, "", server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("payload"), filterapi.GuardrailPhaseRequest)
	require.False(t, evaluation.Matched)
	require.ErrorContains(t, err, "HTTP 503")
	require.ErrorContains(t, err, "provider unavailable")
}

func TestNewEvaluatorConfiguresTimeout(t *testing.T) {
	evaluator, err := NewEvaluator(t.Context(), &filterapi.GuardrailProvider{
		Type:           filterapi.GuardrailProviderTypePresidio,
		TimeoutSeconds: 3,
		Presidio:       &filterapi.PresidioGuardrailProvider{Endpoint: "https://presidio.example.com"},
	})
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, evaluator.(*presidioEvaluator).client.Timeout)
}
