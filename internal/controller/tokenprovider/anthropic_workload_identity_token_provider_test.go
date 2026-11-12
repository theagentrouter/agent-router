// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

func testAnthropicWorkloadIdentityConfig(tokenURL string) *AnthropicWorkloadIdentityConfig {
	return &AnthropicWorkloadIdentityConfig{
		TokenURL:         tokenURL,
		OrganizationID:   "00000000-0000-0000-0000-000000000000",
		ServiceAccountID: "svac_123",
		FederationRuleID: "fdrl_456",
	}
}

// sequentialTokenProvider returns a new subject token on every call, like a source that mints a new "jti".
type sequentialTokenProvider struct{ calls int }

func (s *sequentialTokenProvider) GetToken(context.Context) (TokenExpiry, error) {
	s.calls++
	return TokenExpiry{Token: fmt.Sprintf("subject-%d", s.calls), ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func TestNewAnthropicWorkloadIdentityTokenProvider(t *testing.T) {
	subject := NewMockTokenProvider("subject", time.Now(), nil)
	const tokenURL = "https://api.anthropic.com/v1/oauth/token"
	for _, tc := range []struct {
		name    string
		config  *AnthropicWorkloadIdentityConfig
		subject TokenProvider
		expErr  string
	}{
		{
			name:    "valid",
			config:  testAnthropicWorkloadIdentityConfig(tokenURL),
			subject: subject,
		},
		{
			name:    "empty token URL",
			config:  testAnthropicWorkloadIdentityConfig(""),
			subject: subject,
			expErr:  "token exchange URL must not be empty",
		},
		{
			name:    "empty organization ID",
			config:  &AnthropicWorkloadIdentityConfig{TokenURL: tokenURL, ServiceAccountID: "svac_123", FederationRuleID: "fdrl_456"},
			subject: subject,
			expErr:  "organization ID must not be empty",
		},
		{
			name:    "empty service account ID",
			config:  &AnthropicWorkloadIdentityConfig{TokenURL: tokenURL, OrganizationID: "org", FederationRuleID: "fdrl_456"},
			subject: subject,
			expErr:  "service account ID must not be empty",
		},
		{
			name:    "empty federation rule ID",
			config:  &AnthropicWorkloadIdentityConfig{TokenURL: tokenURL, OrganizationID: "org", ServiceAccountID: "svac_123"},
			subject: subject,
			expErr:  "federation rule ID must not be empty",
		},
		{
			name:   "nil subject token provider",
			config: testAnthropicWorkloadIdentityConfig(tokenURL),
			expErr: "subject token provider must not be nil",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewAnthropicWorkloadIdentityTokenProvider(tc.config, tc.subject)
			if tc.expErr != "" {
				require.EqualError(t, err, tc.expErr)
				require.Nil(t, provider)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, provider)
		})
	}
}

func TestNewAnthropicWorkloadIdentityTokenProvider_InvalidProxyURL(t *testing.T) {
	t.Setenv(AnthropicProxyEnvVar, "://bad")
	provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig("https://api.anthropic.com/v1/oauth/token"),
		NewMockTokenProvider("subject", time.Now(), nil))
	require.ErrorContains(t, err, "invalid AI_GATEWAY_ANTHROPIC_PROXY_URL")
	require.Nil(t, provider)
}

func TestAnthropicWorkloadIdentityTokenProvider_GetToken_Proxy(t *testing.T) {
	var gotHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives the absolute request URL of the token endpoint.
		gotHost = r.URL.Host
		_, _ = w.Write([]byte(`{"access_token":"exchanged","token_type":"Bearer","expires_in":600}`))
	}))
	defer proxy.Close()
	t.Setenv(AnthropicProxyEnvVar, proxy.URL)

	provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig("http://api.anthropic.invalid/v1/oauth/token"),
		NewMockTokenProvider("subject", time.Now().Add(time.Hour), nil))
	require.NoError(t, err)
	token, err := provider.GetToken(t.Context())
	require.NoError(t, err)
	require.Equal(t, "exchanged", token.Token)
	require.Equal(t, "api.anthropic.invalid", gotHost)
}

func TestAnthropicWorkloadIdentityTokenProvider_GetToken(t *testing.T) {
	subjectExpiry := time.Now().Add(time.Hour).Truncate(time.Second)

	for _, tc := range []struct {
		name        string
		workspaceID string
		expBody     map[string]string
	}{
		{
			name: "without workspace",
			expBody: map[string]string{
				"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
				"assertion":          "subject",
				"federation_rule_id": "fdrl_456",
				"organization_id":    "00000000-0000-0000-0000-000000000000",
				"service_account_id": "svac_123",
			},
		},
		{
			name:        "with workspace",
			workspaceID: "wrkspc_789",
			expBody: map[string]string{
				"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
				"assertion":          "subject",
				"federation_rule_id": "fdrl_456",
				"organization_id":    "00000000-0000-0000-0000-000000000000",
				"service_account_id": "svac_123",
				"workspace_id":       "wrkspc_789",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "application/json", r.Header.Get("Content-Type"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &gotBody))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-exchanged","token_type":"Bearer","expires_in":600,"scope":"workspace:developer"}`))
			}))
			defer server.Close()

			config := testAnthropicWorkloadIdentityConfig(server.URL)
			config.WorkspaceID = tc.workspaceID
			provider, err := NewAnthropicWorkloadIdentityTokenProvider(config, NewMockTokenProvider("subject", subjectExpiry, nil))
			require.NoError(t, err)

			before := time.Now()
			token, err := provider.GetToken(t.Context())
			require.NoError(t, err)
			require.Equal(t, "sk-ant-oat01-exchanged", token.Token)
			require.WithinRange(t, token.ExpiresAt, before.Add(600*time.Second), time.Now().Add(600*time.Second))
			require.Equal(t, tc.expBody, gotBody)
		})
	}

	t.Run("every exchange uses a new subject token", func(t *testing.T) {
		var assertions []string
		fail := true
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assertions = append(assertions, body["assertion"])
			if fail {
				fail = false
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"exchanged","token_type":"Bearer","expires_in":600}`))
		}))
		defer server.Close()

		subject := &sequentialTokenProvider{}
		provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig(server.URL), subject)
		require.NoError(t, err)

		_, err = provider.GetToken(t.Context())
		require.Error(t, err)
		_, err = provider.GetToken(t.Context())
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, []string{"subject-1", "subject-2", "subject-3"}, assertions)
	})

	t.Run("subject token error", func(t *testing.T) {
		provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig("http://127.0.0.1:0"),
			NewMockTokenProvider("", time.Time{}, errors.New("workload API unavailable")))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.EqualError(t, err, "failed to get subject token: workload API unavailable")
	})

	for _, tc := range []struct {
		name      string
		status    int
		requestID string
		body      string
		expErr    string
	}{
		{
			name:   "anthropic error response",
			status: http.StatusUnauthorized,
			body:   `{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"},"request_id":"req_011"}`,
			expErr: "token exchange failed with status 401: authentication_error: Authentication failed (request-id: req_011)",
		},
		{
			name:      "anthropic error response with request id header",
			status:    http.StatusBadRequest,
			requestID: "req_022",
			body:      `{"type":"error","error":{"type":"invalid_request_error","message":"federation_rule_id: invalid"}}`,
			expErr:    "token exchange failed with status 400: invalid_request_error: federation_rule_id: invalid (request-id: req_022)",
		},
		{
			name:   "anthropic error response without request id",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"invalid_request_error","message":"federation_rule_id: invalid"}}`,
			expErr: "token exchange failed with status 400: invalid_request_error: federation_rule_id: invalid",
		},
		{
			name:   "unrecognized error response",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_grant","error_description":"assertion expired"}`,
			expErr: `token exchange failed with status 400: {"error":"invalid_grant","error_description":"assertion expired"}`,
		},
		{
			name:   "non-json error response",
			status: http.StatusBadGateway,
			body:   `bad gateway`,
			expErr: "token exchange failed with status 502: bad gateway",
		},
		{
			name:   "invalid json",
			status: http.StatusOK,
			body:   `{`,
			expErr: "failed to decode token exchange response",
		},
		{
			name:   "missing access token",
			status: http.StatusOK,
			body:   `{"token_type":"Bearer","expires_in":600}`,
			expErr: "token exchange response has no access_token",
		},
		{
			name:   "missing expires_in",
			status: http.StatusOK,
			body:   `{"access_token":"exchanged","token_type":"Bearer"}`,
			expErr: "token exchange response has no expires_in",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.requestID != "" {
					w.Header().Set("request-id", tc.requestID)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig(server.URL),
				NewMockTokenProvider("subject", subjectExpiry, nil))
			require.NoError(t, err)
			_, err = provider.GetToken(t.Context())
			if tc.status == http.StatusOK {
				require.ErrorContains(t, err, tc.expErr)
			} else {
				require.EqualError(t, err, tc.expErr)
			}
		})
	}

	t.Run("long error response is truncated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBodyLength+100)))
		}))
		defer server.Close()

		provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig(server.URL),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.EqualError(t, err, "token exchange failed with status 502: "+strings.Repeat("x", maxErrorBodyLength))
	})

	t.Run("request error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()

		provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig(server.URL),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to send token exchange request")
	})

	t.Run("invalid token URL", func(t *testing.T) {
		provider, err := NewAnthropicWorkloadIdentityTokenProvider(testAnthropicWorkloadIdentityConfig("://bad"),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to create token exchange request")
	})
}
