// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testJWTTokenType = "urn:ietf:params:oauth:token-type:jwt"

func TestNewTokenExchangeProvider(t *testing.T) {
	subject := NewMockTokenProvider("subject", time.Now(), nil)
	for _, tc := range []struct {
		name    string
		config  TokenExchangeConfig
		subject TokenProvider
		expErr  string
	}{
		{
			name:    "valid",
			config:  TokenExchangeConfig{TokenURL: "https://auth.example.com/token", SubjectTokenType: testJWTTokenType},
			subject: subject,
		},
		{
			name:    "empty token URL",
			config:  TokenExchangeConfig{SubjectTokenType: testJWTTokenType},
			subject: subject,
			expErr:  "token exchange URL must not be empty",
		},
		{
			name:    "empty subject token type",
			config:  TokenExchangeConfig{TokenURL: "https://auth.example.com/token"},
			subject: subject,
			expErr:  "token exchange subject token type must not be empty",
		},
		{
			name:   "nil subject token provider",
			config: TokenExchangeConfig{TokenURL: "https://auth.example.com/token", SubjectTokenType: testJWTTokenType},
			expErr: "token exchange subject token provider must not be nil",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewTokenExchangeProvider(tc.config, tc.subject)
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

func TestTokenExchangeProvider_GetToken(t *testing.T) {
	subjectExpiry := time.Now().Add(time.Hour).Truncate(time.Second)

	t.Run("success", func(t *testing.T) {
		var gotForm url.Values
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
			require.NoError(t, r.ParseForm())
			gotForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"exchanged","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":600}`))
		}))
		defer server.Close()

		provider, err := NewTokenExchangeProvider(TokenExchangeConfig{
			TokenURL:         server.URL,
			SubjectTokenType: testJWTTokenType,
			Audience:         "https://api.openai.com",
			Scopes:           []string{"a", "b"},
		}, NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)

		before := time.Now()
		token, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, "exchanged", token.Token)
		require.WithinRange(t, token.ExpiresAt, before.Add(600*time.Second), time.Now().Add(600*time.Second))
		require.Equal(t, url.Values{
			"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"subject_token":      {"subject"},
			"subject_token_type": {testJWTTokenType},
			"audience":           {"https://api.openai.com"},
			"scope":              {"a b"},
		}, gotForm)
	})

	t.Run("optional parameters omitted", func(t *testing.T) {
		var gotForm url.Values
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, r.ParseForm())
			gotForm = r.PostForm
			_, _ = w.Write([]byte(`{"access_token":"exchanged","expires_in":600}`))
		}))
		defer server.Close()

		provider, err := NewTokenExchangeProvider(TokenExchangeConfig{TokenURL: server.URL, SubjectTokenType: testJWTTokenType},
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.NoError(t, err)
		require.NotContains(t, gotForm, "audience")
		require.NotContains(t, gotForm, "scope")
	})

	t.Run("no expires_in uses subject token expiry", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"access_token":"exchanged"}`))
		}))
		defer server.Close()

		provider, err := NewTokenExchangeProvider(TokenExchangeConfig{TokenURL: server.URL, SubjectTokenType: testJWTTokenType},
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		token, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, TokenExpiry{Token: "exchanged", ExpiresAt: subjectExpiry}, token)
	})

	t.Run("subject token error", func(t *testing.T) {
		provider, err := NewTokenExchangeProvider(TokenExchangeConfig{TokenURL: "http://127.0.0.1:0", SubjectTokenType: testJWTTokenType},
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
			name:   "oauth error response",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_grant","error_description":"subject token expired"}`,
			expErr: "token exchange failed with status 400: invalid_grant: subject token expired",
		},
		{
			name:   "non-json error response",
			status: http.StatusBadGateway,
			body:   `bad gateway`,
			expErr: "token exchange failed with status 502",
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
			body:   `{"expires_in":600}`,
			expErr: "token exchange response has no access_token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			provider, err := NewTokenExchangeProvider(TokenExchangeConfig{TokenURL: server.URL, SubjectTokenType: testJWTTokenType},
				NewMockTokenProvider("subject", subjectExpiry, nil))
			require.NoError(t, err)
			_, err = provider.GetToken(t.Context())
			require.ErrorContains(t, err, tc.expErr)
		})
	}

	t.Run("request error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()

		provider, err := NewTokenExchangeProvider(TokenExchangeConfig{TokenURL: server.URL, SubjectTokenType: testJWTTokenType},
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to send token exchange request")
	})

	t.Run("invalid token URL", func(t *testing.T) {
		provider, err := NewTokenExchangeProvider(TokenExchangeConfig{TokenURL: "://bad", SubjectTokenType: testJWTTokenType},
			NewMockTokenProvider("subject", subjectExpiry, nil))
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to create token exchange request")
	})
}
