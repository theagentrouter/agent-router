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
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/dataplaneenv"
)

func TestGeminiNativeGeneration(t *testing.T) {
	const requestBody = `{ "contents": [{"parts":[{"thoughtSignature":"opaque*signature"}]}], "cachedContent":"cachedContents/123" }`
	const responseBody = `{"candidates":[{"content":{"parts":[{"thoughtSignature":"opaque*signature","text":"hello"}]}}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":5,"thoughtsTokenCount":3,"totalTokenCount":18}}`
	const errorBody = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Invalid contents"}}`
	const streamError = `{"error":{"code":503,"status":"UNAVAILABLE","message":"Service unavailable"}}`
	const partialResponse = `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`
	cases := []struct {
		name, action, query, format, response string
		status                                int
		streamFailure                         bool
	}{
		{"unary", "generateContent", "", "application/json", responseBody, http.StatusOK, false},
		{"SSE", "streamGenerateContent", "alt=sse", "text/event-stream", "data: " + responseBody + "\n\n", http.StatusOK, false},
		{"JSON array", "streamGenerateContent", "", "application/json", "[" + responseBody + "]", http.StatusOK, false},
		{"provider error", "generateContent", "", "application/json", errorBody, http.StatusBadRequest, false},
		{"SSE error", "streamGenerateContent", "alt=sse", "text/event-stream", "data: " + partialResponse + "\n\ndata: " + streamError + "\n\n", http.StatusOK, true},
		{"JSON array error", "streamGenerateContent", "", "application/json", "[" + partialResponse + "," + streamError + "]", http.StatusOK, true},
		{"retry", "generateContent", "", "application/json", responseBody, http.StatusOK, false},
	}
	backend := func(name, model, key string, fallbackToConfigured bool) filterapi.Backend {
		return filterapi.Backend{
			Name: name, ModelNameOverride: model, Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaGoogleAIStudio},
			Auth: &filterapi.BackendAuth{
				GoogleAPIKey: &filterapi.GoogleAPIKeyAuth{Key: key},
				CredentialOverride: &filterapi.CredentialOverride{
					HeaderName:           "authorization",
					FallbackToConfigured: fallbackToConfigured,
					InputHeadersToRemove: []string{"authorization"},
				},
			},
			HeaderMutation: &filterapi.HTTPHeaderMutation{Remove: []string{"authorization"}},
		}
	}
	config, err := yaml.Marshal(&filterapi.Config{
		Version: version.Parse(),
		Backends: []filterapi.Backend{
			testUpstreamModelNameOverride,
			backend("testupstream-openai-5xx", "primary-gemini", "primary-configured-key", true),
			backend("testupstream-openai", "fallback-gemini", "fallback-configured-key", false),
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
	streamPrefixRead := make(chan struct{}, 1)
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
				response := tc.response
				if tc.streamFailure {
					// Deliver the error object before the final framing byte.
					_, _ = io.WriteString(w, response[:len(response)-1])
					_ = http.NewResponseController(w).Flush()
					select {
					case <-streamPrefixRead:
					case <-r.Context().Done():
						return
					}
					response = response[len(response)-1:]
				}
				_, _ = io.WriteString(w, response)
				return
			}
			http.Error(w, "unknown case", http.StatusInternalServerError)
		})}
		t.Cleanup(func() { _ = server.Close() })
		go func() { _ = server.Serve(lis) }()
	}
	collector := testotel.StartOTLPCollector()
	t.Cleanup(collector.Close)
	collector.SetEnv(t.Setenv)
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("AI_GATEWAY_TRACING_SEMCONV", "openinference")
	env := dataplaneenv.StartTestEnvironment(t, startUpstream, map[string]int{"upstream": 8080},
		string(config), nil, envoyConfig, true, true, 120*time.Second,
		"-metricsRequestHeaderAttributes", "x-test-case:request.case")
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
			const overrideKey = "test-upstream-override"
			if tc.name == "retry" {
				req.Header.Set("Authorization", overrideKey)
			}
			req.Header.Set("x-goog-api-key", "client-header-key")
			req.Header.Set("x-goog-user-project", "client-project")
			req.Header.Set("x-ai-eg-model", "spoofed-model")
			req.Header.Set("x-test-case", tc.name)
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			var body []byte
			if tc.streamFailure {
				body = make([]byte, len(tc.response)-1)
				_, err = io.ReadFull(resp.Body, body)
				require.NoError(t, err)
				streamPrefixRead <- struct{}{}
			}
			tail, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			body = append(body, tail...)
			require.Equal(t, tc.status, resp.StatusCode, string(body))
			require.Equal(t, tc.format, resp.Header.Get("Content-Type"))
			require.Equal(t, tc.response, string(body))
			if tc.name == "retry" {
				assertAttempt(t, "primary-gemini", tc.action, tc.query, overrideKey)
				assertAttempt(t, "fallback-gemini", tc.action, tc.query, overrideKey)
			} else {
				assertAttempt(t, "primary-gemini", tc.action, tc.query, "primary-configured-key")
			}
			require.Empty(t, attempts)
			span := collector.TakeSpan()
			require.NotNil(t, span)
			if tc.streamFailure {
				require.Equal(t, tracev1.Status_STATUS_CODE_ERROR, span.GetStatus().GetCode())
				require.Contains(t, span.GetStatus().GetMessage(), "503")
			}
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
		streamCompletions := map[string]map[bool]float64{}
		for line := range strings.SplitSeq(string(body), "\n") {
			name, _, _ := strings.Cut(line, "{")
			if !strings.Contains(line, `gen_ai_operation_name="generate_content"`) ||
				!strings.Contains(line, `gen_ai_provider_name="gcp.gemini"`) {
				continue
			}
			if name == "gen_ai_server_request_duration_seconds_count" {
				_, label, _ := strings.Cut(line, `request_case="`)
				caseName, _, _ := strings.Cut(label, `"`)
				_, value, _ := strings.Cut(line, "} ")
				count, err := strconv.ParseFloat(value, 64)
				require.NoError(t, err)
				if streamCompletions[caseName] == nil {
					streamCompletions[caseName] = map[bool]float64{}
				}
				streamCompletions[caseName][strings.Contains(line, `error_type="`)] += count
				continue
			}
			if !strings.HasPrefix(name, "gen_ai_client_token_usage") || !strings.HasSuffix(name, "_sum") {
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
		for _, tc := range cases {
			if tc.streamFailure {
				require.Equal(t, map[bool]float64{true: 1}, streamCompletions[tc.name], tc.name)
			}
		}
	})
}
