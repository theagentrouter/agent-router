// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func newHTTPGuardrailTestServer(t *testing.T, response string, check func(*http.Request, httpGuardrailRequest)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		var body httpGuardrailRequest
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		if check != nil {
			check(req, body)
		}
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestHTTPEvaluatorRequestContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		path      string
		apiKey    string
		phase     filterapi.GuardrailPhase
		expPath   string
		expStage  string
		expHeader string
	}{
		{name: "default path request", phase: filterapi.GuardrailPhaseRequest, expPath: "/analyze", expStage: "input"},
		{name: "custom path response with api key", path: "/v1/check", apiKey: "secret", phase: filterapi.GuardrailPhaseResponse, expPath: "/v1/check", expStage: "output", expHeader: "Bearer secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newHTTPGuardrailTestServer(t, `{"action":"allow"}`, func(req *http.Request, body httpGuardrailRequest) {
				require.Equal(t, tc.expPath, req.URL.Path)
				require.Equal(t, tc.expHeader, req.Header.Get("Authorization"))
				require.Equal(t, "some user input", body.Text)
				require.Equal(t, tc.expStage, body.Context.Stage)
			})
			evaluator, err := newHTTPEvaluator(&filterapi.HTTPGuardrailProvider{
				Endpoint: server.URL + "/", Path: tc.path, APIKey: tc.apiKey,
			}, "", server.Client())
			require.NoError(t, err)
			evaluation, err := evaluator.Evaluate(t.Context(), []byte("some user input"), tc.phase)
			require.NoError(t, err)
			require.False(t, evaluation.Matched)
			require.Nil(t, evaluation.Replacement)
		})
	}
}

func TestHTTPEvaluatorResponseContract(t *testing.T) {
	const text = "my email is john@example.com ok"
	for _, tc := range []struct {
		name           string
		response       string
		expMatched     bool
		expReplacement string
		expErr         string
	}{
		{
			name:     "allow ignores findings",
			response: `{"action":"allow","findings":[{"type":"PII","start":12,"end":28,"score":0.92}]}`,
		},
		{
			name:       "block without findings",
			response:   `{"action":"block"}`,
			expMatched: true,
		},
		{
			name:           "block with findings provides mask",
			response:       `{"action":"block","findings":[{"type":"EMAIL","start":12,"end":28,"score":0.92}]}`,
			expMatched:     true,
			expReplacement: "my email is [MASKED] ok",
		},
		{
			name:           "modify with replacement",
			response:       `{"action":"modify","replacement":"my email is <email> ok"}`,
			expMatched:     true,
			expReplacement: "my email is <email> ok",
		},
		{
			name:           "modify with findings only",
			response:       `{"action":"modify","findings":[{"type":"EMAIL","start":12,"end":28}]}`,
			expMatched:     true,
			expReplacement: "my email is [MASKED] ok",
		},
		{
			name:           "action is case insensitive",
			response:       `{"action":"MODIFY","findings":[{"start":0,"end":2}]}`,
			expMatched:     true,
			expReplacement: "[MASKED] email is john@example.com ok",
		},
		{
			name:     "modify without replacement or findings",
			response: `{"action":"modify"}`,
			expErr:   "modify without replacement or findings",
		},
		{
			name:     "unknown action",
			response: `{"action":"quarantine"}`,
			expErr:   `unsupported action "quarantine"`,
		},
		{
			name:     "missing action",
			response: `{"findings":[]}`,
			expErr:   `unsupported action ""`,
		},
		{
			name:     "malformed response",
			response: `[1,2,3]`,
			expErr:   "cannot decode provider response",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newHTTPGuardrailTestServer(t, tc.response, nil)
			evaluator, err := newHTTPEvaluator(&filterapi.HTTPGuardrailProvider{Endpoint: server.URL}, "[MASKED]", server.Client())
			require.NoError(t, err)
			evaluation, err := evaluator.Evaluate(t.Context(), []byte(text), filterapi.GuardrailPhaseRequest)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				require.False(t, evaluation.Matched)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expMatched, evaluation.Matched)
			require.Equal(t, tc.expReplacement, string(evaluation.Replacement))
		})
	}
}

func TestHTTPEvaluatorProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "guardrail unavailable", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	evaluator, err := newHTTPEvaluator(&filterapi.HTTPGuardrailProvider{Endpoint: server.URL}, "", server.Client())
	require.NoError(t, err)
	evaluation, err := evaluator.Evaluate(t.Context(), []byte("payload"), filterapi.GuardrailPhaseRequest)
	require.False(t, evaluation.Matched)
	require.ErrorContains(t, err, "HTTP 502")
	require.ErrorContains(t, err, "guardrail unavailable")
}

func TestNewHTTPEvaluatorValidation(t *testing.T) {
	_, err := newHTTPEvaluator(nil, "", http.DefaultClient)
	require.ErrorContains(t, err, "endpoint is required")
	_, err = newHTTPEvaluator(&filterapi.HTTPGuardrailProvider{Endpoint: "https://guardrail.example.com", Path: "analyze"}, "", http.DefaultClient)
	require.ErrorContains(t, err, "path must start with /")
}

func TestNewEvaluatorHTTPProvider(t *testing.T) {
	evaluator, err := NewEvaluator(t.Context(), &filterapi.GuardrailProvider{
		Type:           filterapi.GuardrailProviderTypeHTTP,
		TimeoutSeconds: 2,
		HTTP:           &filterapi.HTTPGuardrailProvider{Endpoint: "https://guardrail.example.com/", Path: "/v1/analyze"},
	})
	require.NoError(t, err)
	httpEval := evaluator.(*httpEvaluator)
	require.Equal(t, "https://guardrail.example.com/v1/analyze", httpEval.url)
	require.Equal(t, "[REDACTED]", httpEval.maskReplacement)
	require.Equal(t, 2*time.Second, httpEval.client.Timeout)
}

func TestMaskSpans(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		spans []textSpan
		exp   string
	}{
		{name: "single", body: "abcdef", spans: []textSpan{{Start: 1, End: 3}}, exp: "a#def"},
		{name: "unsorted", body: "abcdef", spans: []textSpan{{Start: 4, End: 5}, {Start: 0, End: 1}}, exp: "#bcd#f"},
		{name: "overlapping merged", body: "abcdef", spans: []textSpan{{Start: 1, End: 4}, {Start: 2, End: 5}}, exp: "a#f"},
		{name: "adjacent merged", body: "abcdef", spans: []textSpan{{Start: 1, End: 2}, {Start: 2, End: 3}}, exp: "a#def"},
		{name: "unicode offsets", body: "héllo wörld", spans: []textSpan{{Start: 6, End: 11}}, exp: "héllo #"},
		{name: "invalid spans ignored", body: "abcdef", spans: []textSpan{{Start: -1, End: 2}, {Start: 2, End: 99}, {Start: 3, End: 3}, {Start: 0, End: 1}}, exp: "#bcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.exp, string(maskSpans([]byte(tc.body), tc.spans, "#")))
		})
	}
	require.Nil(t, maskSpans([]byte("abc"), []textSpan{{Start: 5, End: 6}}, "#"))
	require.Nil(t, maskSpans([]byte("abc"), nil, "#"))
}
