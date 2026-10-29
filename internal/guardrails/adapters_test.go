// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/gcpauth"
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

// newModelArmorTestCredentials returns a service account key JSON whose token_uri points to a fake
// token server, so tests exercise the real OAuth2 JWT flow without contacting Google.
func newModelArmorTestCredentials(t *testing.T) string {
	t.Helper()
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.NoError(t, req.ParseForm())
		require.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", req.Form.Get("grant_type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gcp-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	credentials, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "project-id",
		"private_key_id": "key-id",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "guardrails@project-id.iam.gserviceaccount.com",
		"token_uri":      tokenServer.URL,
	})
	require.NoError(t, err)
	return string(credentials)
}

func TestModelArmorEvaluatorHTTP(t *testing.T) {
	credentials := newModelArmorTestCredentials(t)
	tests := []struct {
		name            string
		phase           filterapi.GuardrailPhase
		wantPath        string
		wantBody        string
		response        string
		wantMatched     bool
		wantReplacement string
	}{
		{
			name:     "request no match",
			phase:    filterapi.GuardrailPhaseRequest,
			wantPath: "/v1/projects/project-id/locations/us-central1/templates/template-id:sanitizeUserPrompt",
			wantBody: `{"userPromptData":{"text":"hello"}}`,
			response: `{"sanitizationResult":{"filterMatchState":"NO_MATCH_FOUND","invocationResult":"SUCCESS"}}`,
		},
		{
			name:     "request jailbreak match",
			phase:    filterapi.GuardrailPhaseRequest,
			wantPath: "/v1/projects/project-id/locations/us-central1/templates/template-id:sanitizeUserPrompt",
			wantBody: `{"userPromptData":{"text":"hello"}}`,
			response: `{"sanitizationResult":{"filterMatchState":"MATCH_FOUND","invocationResult":"SUCCESS","filterResults":{
				"pi_and_jailbreak":{"piAndJailbreakFilterResult":{"executionState":"EXECUTION_SUCCESS","matchState":"MATCH_FOUND"}}}}}`,
			wantMatched: true,
		},
		{
			name:     "response sdp de-identify match",
			phase:    filterapi.GuardrailPhaseResponse,
			wantPath: "/v1/projects/project-id/locations/us-central1/templates/template-id:sanitizeModelResponse",
			wantBody: `{"modelResponseData":{"text":"hello"}}`,
			response: `{"sanitizationResult":{"filterMatchState":"MATCH_FOUND","invocationResult":"SUCCESS","filterResults":{
				"rai":{"raiFilterResult":{"matchState":"NO_MATCH_FOUND"}},
				"sdp":{"sdpFilterResult":{"deidentifyResult":{"matchState":"MATCH_FOUND","data":{"text":"[EMAIL_ADDRESS]"}}}}}}}`,
			wantMatched:     true,
			wantReplacement: "[EMAIL_ADDRESS]",
		},
		{
			name:     "sdp de-identify with another filter match",
			phase:    filterapi.GuardrailPhaseResponse,
			wantPath: "/v1/projects/project-id/locations/us-central1/templates/template-id:sanitizeModelResponse",
			wantBody: `{"modelResponseData":{"text":"hello"}}`,
			response: `{"sanitizationResult":{"filterMatchState":"MATCH_FOUND","invocationResult":"SUCCESS","filterResults":{
				"rai":{"raiFilterResult":{"matchState":"MATCH_FOUND"}},
				"sdp":{"sdpFilterResult":{"deidentifyResult":{"matchState":"MATCH_FOUND","data":{"text":"[EMAIL_ADDRESS]"}}}}}}}`,
			wantMatched: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, test.wantPath, req.URL.Path)
				require.Equal(t, "Bearer gcp-token", req.Header.Get("Authorization"))
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.JSONEq(t, test.wantBody, string(body))
				_, _ = w.Write([]byte(test.response))
			}))
			t.Cleanup(server.Close)

			evaluator, err := newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{
				Endpoint: server.URL, Project: "project-id", Location: "us-central1", Template: "template-id",
				CredentialsJSON: credentials,
			}, server.Client())
			require.NoError(t, err)
			evaluation, err := evaluator.Evaluate(t.Context(), []byte("hello"), test.phase)
			require.NoError(t, err)
			require.Equal(t, test.wantMatched, evaluation.Matched)
			require.Equal(t, test.wantReplacement, string(evaluation.Replacement))
		})
	}
}

func TestModelArmorEvaluatorErrors(t *testing.T) {
	_, err := newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{Project: "project-id"}, http.DefaultClient)
	require.ErrorContains(t, err, "project, location, and template are required")
	_, err = newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{
		Project: "project-id", Location: "us-central1", Template: "template-id", CredentialsJSON: `{"type":"authorized_user"}`,
	}, http.DefaultClient)
	require.ErrorContains(t, err, "cannot load GCP credentials")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sanitizationResult":{"filterMatchState":"NO_MATCH_FOUND","invocationResult":"FAILURE"}}`))
	}))
	t.Cleanup(server.Close)
	evaluator, err := newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{
		Endpoint: server.URL, Project: "project-id", Location: "us-central1", Template: "template-id",
		CredentialsJSON: newModelArmorTestCredentials(t),
	}, server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("hello"), filterapi.GuardrailPhaseRequest)
	require.False(t, evaluation.Matched)
	require.ErrorContains(t, err, "sanitizeUserPrompt invocation failed")
}

// isolateAWSEnvironment keeps AWS credential resolution hermetic: no ambient credentials and no IMDS calls.
func isolateAWSEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
}

func TestNewEvaluatorProviders(t *testing.T) {
	isolateAWSEnvironment(t)
	severity := int32(2)
	for _, provider := range []*filterapi.GuardrailProvider{
		{Type: filterapi.GuardrailProviderTypeBedrockGuardrails, Bedrock: &filterapi.BedrockGuardrailProvider{
			Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
		}},
		{Type: filterapi.GuardrailProviderTypeAzureContentSafety, AzureContentSafety: &filterapi.AzureContentSafetyGuardrailProvider{
			Endpoint: "https://content-safety.example.com", APIKey: "key", SeverityThreshold: &severity,
		}},
		{Type: filterapi.GuardrailProviderTypeHTTP, HTTP: &filterapi.HTTPGuardrailProvider{Endpoint: "https://guardrail.example.com"}},
		{Type: filterapi.GuardrailProviderTypeModelArmor, ModelArmor: &filterapi.ModelArmorGuardrailProvider{
			Project: "project-id", Location: "us-central1", Template: "template-id", CredentialsJSON: newModelArmorTestCredentials(t),
		}},
	} {
		t.Run(string(provider.Type), func(t *testing.T) {
			evaluator, err := NewEvaluator(t.Context(), provider)
			require.NoError(t, err)
			require.NotNil(t, evaluator)
		})
	}

	_, err := NewEvaluator(t.Context(), &filterapi.GuardrailProvider{Type: "Unknown"})
	require.ErrorContains(t, err, `unsupported external guardrail provider "Unknown"`)
}

func TestTimeoutSecondsDefault(t *testing.T) {
	require.Equal(t, defaultTimeoutSeconds, timeoutSeconds(0))
	require.Equal(t, int32(5), timeoutSeconds(5))
}

func TestDoJSONErrors(t *testing.T) {
	err := doJSON(t.Context(), http.DefaultClient, http.MethodPost, "https://example.com", make(chan int), nil, nil)
	require.ErrorContains(t, err, "unsupported type")

	err = doJSON(t.Context(), http.DefaultClient, http.MethodPost, "http://[::1", struct{}{}, nil, nil)
	require.ErrorContains(t, err, "missing ']' in host")

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	err = doJSON(t.Context(), http.DefaultClient, http.MethodPost, server.URL, struct{}{}, nil, nil)
	require.ErrorContains(t, err, "connection refused")

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(malformed.Close)
	var response struct{}
	err = doJSON(t.Context(), malformed.Client(), http.MethodPost, malformed.URL, struct{}{}, nil, &response)
	require.ErrorContains(t, err, "cannot decode provider response")
}

func TestPresidioEvaluatorConfigurationAndNoFindings(t *testing.T) {
	_, err := newPresidioEvaluator(&filterapi.PresidioGuardrailProvider{}, "", http.DefaultClient)
	require.ErrorContains(t, err, "presidio endpoint is required")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	evaluator, err := newPresidioEvaluator(&filterapi.PresidioGuardrailProvider{Endpoint: server.URL}, "", server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("nothing sensitive"), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.False(t, evaluation.Matched)
}

func TestAzureContentSafetyEvaluatorConfiguration(t *testing.T) {
	_, err := newAzureContentSafetyEvaluator(&filterapi.AzureContentSafetyGuardrailProvider{APIKey: "key"}, http.DefaultClient)
	require.ErrorContains(t, err, "azure Content Safety endpoint is required")
	_, err = newAzureContentSafetyEvaluator(&filterapi.AzureContentSafetyGuardrailProvider{Endpoint: "https://content-safety.example.com"}, http.DefaultClient)
	require.ErrorContains(t, err, "azure Content Safety API key is required")

	evaluator, err := newAzureContentSafetyEvaluator(&filterapi.AzureContentSafetyGuardrailProvider{
		Endpoint: "https://content-safety.example.com", APIKey: "key",
	}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, int32(4), *evaluator.(*azureContentSafetyEvaluator).config.SeverityThreshold)
}

func TestAzureContentSafetyEvaluatorBelowThresholdAndError(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"categoriesAnalysis":[{"category":"Violence","severity":2}]}`))
	}))
	t.Cleanup(server.Close)
	evaluator, err := newAzureContentSafetyEvaluator(&filterapi.AzureContentSafetyGuardrailProvider{
		Endpoint: server.URL, APIKey: "key",
	}, server.Client())
	require.NoError(t, err)

	evaluation, err := evaluator.Evaluate(t.Context(), []byte("mild"), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.False(t, evaluation.Matched)

	status = http.StatusTooManyRequests
	_, err = evaluator.Evaluate(t.Context(), []byte("mild"), filterapi.GuardrailPhaseRequest)
	require.ErrorContains(t, err, "azure Content Safety analyze request failed: provider returned HTTP 429")
}

func TestBedrockEvaluatorConfiguration(t *testing.T) {
	isolateAWSEnvironment(t)
	_, err := newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{Region: "us-east-1"}, http.DefaultClient)
	require.ErrorContains(t, err, "bedrock region, guardrailIdentifier, and guardrailVersion are required")

	_, err = newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{
		Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
		CredentialFileLiteral: "[other]\naws_access_key_id = AKIDEXAMPLE\naws_secret_access_key = secret\n",
	}, http.DefaultClient)
	require.ErrorContains(t, err, "cannot load AWS credentials")

	t.Setenv("AWS_PROFILE", "missing")
	_, err = newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{
		Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
	}, http.DefaultClient)
	require.ErrorContains(t, err, "cannot load AWS config")
}

func TestBedrockEvaluatorErrors(t *testing.T) {
	isolateAWSEnvironment(t)
	// Without a credentials Secret, the default chain is used and only fails when credentials are retrieved.
	evaluator, err := newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{
		Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
	}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", evaluator.(*bedrockEvaluator).config.Endpoint)
	_, err = evaluator.Evaluate(t.Context(), []byte("payload"), filterapi.GuardrailPhaseRequest)
	require.ErrorContains(t, err, "cannot retrieve AWS credentials")

	credentials := "[default]\naws_access_key_id = AKIDEXAMPLE\naws_secret_access_key = secret\n"
	evaluator, err = newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{
		Endpoint: "http://[::1", Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
		CredentialFileLiteral: credentials,
	}, http.DefaultClient)
	require.NoError(t, err)
	_, err = evaluator.Evaluate(t.Context(), []byte("payload"), filterapi.GuardrailPhaseRequest)
	require.ErrorContains(t, err, "missing ']' in host")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "throttled", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	evaluator, err = newBedrockEvaluator(t.Context(), &filterapi.BedrockGuardrailProvider{
		Endpoint: server.URL, Region: "us-east-1", GuardrailIdentifier: "guardrail-id", GuardrailVersion: "1",
		CredentialFileLiteral: credentials,
	}, server.Client())
	require.NoError(t, err)
	_, err = evaluator.Evaluate(t.Context(), []byte("payload"), filterapi.GuardrailPhaseRequest)
	require.ErrorContains(t, err, "bedrock ApplyGuardrail request failed: provider returned HTTP 429")
}

func TestModelArmorEvaluatorDefaults(t *testing.T) {
	credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
	require.NoError(t, os.WriteFile(credentialsPath, []byte(newModelArmorTestCredentials(t)), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentialsPath)

	evaluator, err := newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{
		Project: "project-id", Location: "europe-west4", Template: "template-id",
	}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "https://modelarmor.europe-west4.rep.googleapis.com/v1/projects/project-id/locations/europe-west4/templates/template-id",
		evaluator.(*modelArmorEvaluator).templateURL)

	t.Setenv(gcpauth.ProxyEnvVar, "://invalid")
	_, err = newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{
		Project: "project-id", Location: "europe-west4", Template: "template-id",
	}, http.DefaultClient)
	require.ErrorContains(t, err, "invalid "+gcpauth.ProxyEnvVar)
}

func TestModelArmorEvaluatorRequestErrors(t *testing.T) {
	tokenStatus := http.StatusOK
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(tokenStatus)
		_, _ = w.Write([]byte(`{"access_token":"gcp-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	var credentials map[string]string
	require.NoError(t, json.Unmarshal([]byte(newModelArmorTestCredentials(t)), &credentials))
	credentials["token_uri"] = tokenServer.URL
	credentialsJSON, err := json.Marshal(credentials)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	newEvaluator := func() filterapi.GuardrailEvaluator {
		evaluator, newErr := newModelArmorEvaluator(t.Context(), &filterapi.ModelArmorGuardrailProvider{
			Endpoint: server.URL, Project: "project-id", Location: "us-central1", Template: "template-id",
			CredentialsJSON: string(credentialsJSON),
		}, server.Client())
		require.NoError(t, newErr)
		return evaluator
	}

	_, err = newEvaluator().Evaluate(t.Context(), []byte("hello"), filterapi.GuardrailPhaseResponse)
	require.ErrorContains(t, err, "model Armor sanitizeModelResponse request failed: provider returned HTTP 503")

	tokenStatus = http.StatusUnauthorized
	_, err = newEvaluator().Evaluate(t.Context(), []byte("hello"), filterapi.GuardrailPhaseRequest)
	require.ErrorContains(t, err, "cannot retrieve GCP access token")
}

func TestModelArmorDeidentifiedTextWithSDPInspectMatch(t *testing.T) {
	var results map[string]modelArmorFilterResult
	require.NoError(t, json.Unmarshal([]byte(`{"sdp":{"sdpFilterResult":{"inspectResult":{"matchState":"MATCH_FOUND"}}}}`), &results))
	require.Nil(t, modelArmorDeidentifiedText(results))
}
