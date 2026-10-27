// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
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

func testOpenAIWorkloadIdentityConfig(tokenURL string) OpenAIWorkloadIdentityConfig {
	return OpenAIWorkloadIdentityConfig{TokenURL: tokenURL, IdentityProviderID: "idp_123", ServiceAccountID: "sa_456"}
}

func TestNewOpenAIWorkloadIdentityTokenProvider(t *testing.T) {
	subject := NewMockTokenProvider("subject", time.Now(), nil)
	for _, tc := range []struct {
		name    string
		config  OpenAIWorkloadIdentityConfig
		subject TokenProvider
		expErr  string
	}{
		{
			name:    "valid",
			config:  testOpenAIWorkloadIdentityConfig("https://auth.openai.com/oauth/token"),
			subject: subject,
		},
		{
			name:    "empty token URL",
			config:  OpenAIWorkloadIdentityConfig{IdentityProviderID: "idp_123", ServiceAccountID: "sa_456"},
			subject: subject,
			expErr:  "token exchange URL must not be empty",
		},
		{
			name:    "empty identity provider ID",
			config:  OpenAIWorkloadIdentityConfig{TokenURL: "https://auth.openai.com/oauth/token", ServiceAccountID: "sa_456"},
			subject: subject,
			expErr:  "identity provider ID must not be empty",
		},
		{
			name:    "empty service account ID",
			config:  OpenAIWorkloadIdentityConfig{TokenURL: "https://auth.openai.com/oauth/token", IdentityProviderID: "idp_123"},
			subject: subject,
			expErr:  "service account ID must not be empty",
		},
		{
			name:   "nil subject token provider",
			config: testOpenAIWorkloadIdentityConfig("https://auth.openai.com/oauth/token"),
			expErr: "subject token provider must not be nil",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewOpenAIWorkloadIdentityTokenProvider(tc.config, tc.subject)
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

func TestNewOpenAIWorkloadIdentityTokenProvider_InvalidProxyURL(t *testing.T) {
	t.Setenv(OpenAIProxyEnvVar, "://bad")
	provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig("https://auth.openai.com/oauth/token"),
		NewMockTokenProvider("subject", time.Now(), nil))
	require.ErrorContains(t, err, "invalid AI_GATEWAY_OPENAI_PROXY_URL")
	require.Nil(t, provider)
}

func TestOpenAIWorkloadIdentityTokenProvider_GetToken_Proxy(t *testing.T) {
	var gotHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives the absolute request URL of the token endpoint.
		gotHost = r.URL.Host
		_, _ = fmt.Fprintf(w, `{"access_token":"exchanged","expires_in":600,"expires_at":%d}`, time.Now().Add(10*time.Minute).Unix())
	}))
	defer proxy.Close()
	t.Setenv(OpenAIProxyEnvVar, proxy.URL)

	provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig("http://auth.openai.invalid/oauth/token"),
		NewMockTokenProvider("subject", time.Now().Add(time.Hour), nil))
	require.NoError(t, err)
	token, err := provider.GetToken(t.Context())
	require.NoError(t, err)
	require.Equal(t, "exchanged", token.Token)
	require.Equal(t, "auth.openai.invalid", gotHost)
}

func TestOpenAIWorkloadIdentityTokenProvider_GetToken(t *testing.T) {
	subjectExpiry := time.Now().Add(time.Hour).Truncate(time.Second)

	t.Run("success", func(t *testing.T) {
		expiresAt := time.Now().Add(30 * time.Minute).Truncate(time.Second)
		var gotBody map[string]string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &gotBody))
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":"exchanged","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":600,"expires_at":%d}`, expiresAt.Unix())
		}))
		defer server.Close()

		provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig(server.URL),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)

		token, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, "exchanged", token.Token)
		require.True(t, expiresAt.Equal(token.ExpiresAt), "expected %v, got %v", expiresAt, token.ExpiresAt)
		require.Equal(t, map[string]string{
			"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
			"subject_token":        "subject",
			"subject_token_type":   "urn:ietf:params:oauth:token-type:jwt",
			"identity_provider_id": "idp_123",
			"service_account_id":   "sa_456",
		}, gotBody)
	})

	t.Run("subject token error", func(t *testing.T) {
		provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig("http://127.0.0.1:0"),
			NewMockTokenProvider("", time.Time{}, errors.New("workload API unavailable")))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.EqualError(t, err, "failed to get subject token: workload API unavailable")
	})

	for _, tc := range []struct {
		name   string
		status int
		body   string
		expErr string
	}{
		{
			name:   "openai error response",
			status: http.StatusUnauthorized,
			body:   `{"error":{"message":"No identity provider found for the given provider id.","type":"invalid_request_error","param":null,"code":"invalid_grant"}}`,
			expErr: "token exchange failed with status 401: invalid_grant: No identity provider found for the given provider id.",
		},
		{
			name:   "unrecognized error response",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_grant","error_description":"subject token expired"}`,
			expErr: `token exchange failed with status 400: {"error":"invalid_grant","error_description":"subject token expired"}`,
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
			body:   `{"expires_in":600,"expires_at":1793053844}`,
			expErr: "token exchange response has no access_token",
		},
		{
			name:   "missing expires_at",
			status: http.StatusOK,
			body:   `{"access_token":"exchanged","expires_in":600}`,
			expErr: "token exchange response has no expires_at",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig(server.URL),
				NewMockTokenProvider("subject", subjectExpiry, nil))
			require.NoError(t, err)
			_, err = provider.GetToken(t.Context())
			require.ErrorContains(t, err, tc.expErr)
		})
	}

	t.Run("long error response is truncated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBodyLength+100)))
		}))
		defer server.Close()

		provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig(server.URL),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.EqualError(t, err, "token exchange failed with status 502: "+strings.Repeat("x", maxErrorBodyLength))
	})

	t.Run("request error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()

		provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig(server.URL),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to send token exchange request")
	})

	t.Run("invalid token URL", func(t *testing.T) {
		provider, err := NewOpenAIWorkloadIdentityTokenProvider(testOpenAIWorkloadIdentityConfig("://bad"),
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to create token exchange request")
	})
}
