// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplane

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/dataplaneenv"
)

func TestGeminiNativeGeneration(t *testing.T) {
	const requestBody = `{ "contents": [{"parts":[{"thoughtSignature":"opaque*signature"}]}], "cachedContent":"cachedContents/123" }`
	const responseBody = `{"candidates":[{"content":{"parts":[{"thoughtSignature":"opaque*signature","text":"hello"}]}}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":5,"thoughtsTokenCount":3,"totalTokenCount":18}}`
	const errorBody = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Invalid contents"}}`
	cases := []struct {
		name, action, query, format, response string
		status                                int
	}{
		{"unary", "generateContent", "", "application/json", responseBody, http.StatusOK},
		{"SSE", "streamGenerateContent", "alt=sse", "text/event-stream", "data: " + responseBody + "\n\n", http.StatusOK},
		{"JSON array", "streamGenerateContent", "", "application/json", "[" + responseBody + "]", http.StatusOK},
		{"provider error", "generateContent", "", "application/json", errorBody, http.StatusBadRequest},
		{"retry", "generateContent", "", "application/json", responseBody, http.StatusOK},
	}
	backend := func(name, model, key string) filterapi.Backend {
		return filterapi.Backend{
			Name: name, ModelNameOverride: model, Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaGoogleAIStudio},
			Auth: &filterapi.BackendAuth{GoogleAPIKey: &filterapi.GoogleAPIKeyAuth{Key: key}},
		}
	}
	config, err := yaml.Marshal(&filterapi.Config{
		Version: version.Parse(),
		Backends: []filterapi.Backend{
			testUpstreamModelNameOverride,
			backend("testupstream-openai-5xx", "primary-gemini", "primary-configured-key"),
			backend("testupstream-openai", "fallback-gemini", "fallback-configured-key"),
		},
	})
	require.NoError(t, err)
	type attempt struct {
		path, query string
		body        []byte
		headers     http.Header
		err         error
	}
	attempts := make(chan attempt, len(cases)+1)
	startUpstream := func(t testing.TB, _ io.Writer, ports map[string]int) {
		lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports["upstream"])) //nolint:gosec
		require.NoError(t, err)
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			attempts <- attempt{r.URL.Path, r.URL.RawQuery, body, r.Header.Clone(), err}
			if r.Header.Get("x-test-case") == "openai" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"model":"override-model","choices":[]}`)
				return
			}
			for _, tc := range cases {
				if r.Header.Get("x-test-case") != tc.name {
					continue
				}
				w.Header().Set("Content-Type", tc.format)
				if tc.name == "retry" && strings.Contains(r.URL.Path, "/primary-gemini:") {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
				return
			}
			http.Error(w, "unknown case", http.StatusInternalServerError)
		})}
		t.Cleanup(func() { _ = server.Close() })
		go func() { _ = server.Serve(lis) }()
	}
	env := dataplaneenv.StartTestEnvironment(t, startUpstream, map[string]int{"upstream": 8080},
		string(config), nil, envoyConfig, true, true, 120*time.Second)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/gemini/v1beta/models/native-gemini:", env.EnvoyListenerPort())
	client := &http.Client{Timeout: eventuallyTimeout}
	assertAttempt := func(t *testing.T, model, action, query, key string) {
		t.Helper()
		select {
		case a := <-attempts:
			require.NoError(t, a.err)
			require.Equal(t, "/v1beta/models/"+model+":"+action, a.path)
			require.Equal(t, query, a.query)
			require.Equal(t, []byte(requestBody), a.body) //nolint:testifylint // Passthrough preserves the exact request bytes.
			require.Equal(t, []string{key}, a.headers.Values("x-goog-api-key"))
			require.Empty(t, a.headers.Get("Authorization"))
			require.Empty(t, a.headers.Get("x-goog-user-project"))
			require.NotContains(t, a.headers.Get("x-ai-eg-original-path"), "client-query-key")
			require.NotContains(t, a.headers.Get("x-envoy-original-path"), "client-query-key")
		default:
			t.Fatal("request did not reach the upstream")
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := baseURL + tc.action + "?key=client-query-key&%24fields=candidates"
			if tc.query != "" {
				url += "&" + tc.query
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(requestBody))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer client-token")
			req.Header.Set("x-goog-api-key", "client-header-key")
			req.Header.Set("x-goog-user-project", "client-project")
			req.Header.Set("x-ai-eg-model", "spoofed-model")
			req.Header.Set("x-test-case", tc.name)
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode, string(body))
			require.Equal(t, tc.format, resp.Header.Get("Content-Type"))
			require.Equal(t, tc.response, string(body))
			assertAttempt(t, "primary-gemini", tc.action, tc.query, "primary-configured-key")
			if tc.name == "retry" {
				assertAttempt(t, "fallback-gemini", tc.action, tc.query, "fallback-configured-key")
			}
			require.Empty(t, attempts)
		})
	}
	t.Run("same model via OpenAI endpoint", func(t *testing.T) {
		url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", env.EnvoyListenerPort())
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url,
			strings.NewReader(`{"model":"native-gemini","messages":[{"role":"user","content":"Hello"}]}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-test-case", "openai")
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		select {
		case a := <-attempts:
			require.NoError(t, a.err)
			require.Equal(t, "/v1/chat/completions", a.path)
			require.JSONEq(t, `{"model":"override-model","messages":[{"role":"user","content":"Hello"}]}`, string(a.body))
			require.Empty(t, a.headers.Get("x-goog-api-key"))
		default:
			t.Fatal("request did not reach the upstream")
		}
	})
	t.Run("unsupported operation does not dispatch", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"countTokens?key=client-query-key", strings.NewReader(requestBody))
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.NotContains(t, string(body), "client-query-key")
		require.Empty(t, attempts)
	})
	t.Run("usage", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/metrics", env.ExtProcAdminPort()), nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		sums := map[string]float64{}
		for line := range strings.SplitSeq(string(body), "\n") {
			name, _, _ := strings.Cut(line, "{")
			if !strings.HasPrefix(name, "gen_ai_client_token_usage") || !strings.HasSuffix(name, "_sum") ||
				!strings.Contains(line, `gen_ai_operation_name="generate_content"`) ||
				!strings.Contains(line, `gen_ai_provider_name="gcp.gemini"`) {
				continue
			}
			_, label, _ := strings.Cut(line, `gen_ai_token_type="`)
			tokenType, _, _ := strings.Cut(label, `"`)
			_, value, _ := strings.Cut(line, "} ")
			count, err := strconv.ParseFloat(value, 64)
			require.NoError(t, err)
			sums[tokenType] += count
		}
		require.Equal(t, map[string]float64{"input": 40, "output": 32, "cached_input": 16, "reasoning": 12}, sums)
	})
}
