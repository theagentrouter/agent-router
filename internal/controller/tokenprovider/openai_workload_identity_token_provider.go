// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	// tokenExchangeGrantType is the RFC 8693 grant type for token exchange requests.
	tokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101
	// jwtTokenType is the RFC 8693 token type of a JWT subject token.
	jwtTokenType = "urn:ietf:params:oauth:token-type:jwt" // #nosec G101
	// maxErrorBodyLength caps how much of an unrecognized error response is included in errors.
	maxErrorBodyLength = 512
	// OpenAIProxyEnvVar is the environment variable name for configuring the proxy URL of OpenAI token
	// exchange requests.
	OpenAIProxyEnvVar = "AI_GATEWAY_OPENAI_PROXY_URL"
)

// OpenAITokenExchangeURL is the OpenAI token exchange endpoint. It is not configurable through the API so that
// policies cannot direct the controller to send subject tokens to arbitrary URLs. It is a variable only so
// tests can point it at a local server.
var OpenAITokenExchangeURL = "https://auth.openai.com/oauth/token" // #nosec G101

// OpenAIWorkloadIdentityConfig holds the parameters of an OpenAI Workload Identity Federation token exchange.
type OpenAIWorkloadIdentityConfig struct {
	// TokenURL is the OpenAI token exchange endpoint.
	TokenURL string
	// IdentityProviderID is the OpenAI Workload Identity Provider ID.
	IdentityProviderID string
	// ServiceAccountID is the OpenAI service account ID.
	ServiceAccountID string
}

// openAIWorkloadIdentityTokenProvider is a provider implements TokenProvider interface for OpenAI
// Workload Identity Federation.
type openAIWorkloadIdentityTokenProvider struct {
	config        OpenAIWorkloadIdentityConfig
	subjectTokens TokenProvider
	httpClient    *http.Client
}

// openAITokenExchangeRequest is the JSON body of an OpenAI token exchange request.
type openAITokenExchangeRequest struct {
	GrantType          string `json:"grant_type"`
	SubjectToken       string `json:"subject_token"`
	SubjectTokenType   string `json:"subject_token_type"`
	IdentityProviderID string `json:"identity_provider_id"`
	ServiceAccountID   string `json:"service_account_id"`
}

// openAITokenExchangeResponse is the successful response of an OpenAI token exchange.
type openAITokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	// ExpiresAt is the expiration time of the access token in Unix seconds.
	ExpiresAt int64 `json:"expires_at"`
}

// openAIErrorResponse is the error response of the OpenAI API.
type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// NewOpenAIWorkloadIdentityTokenProvider creates a new TokenProvider that obtains a JWT subject token from
// subjectTokens and exchanges it for an OpenAI access token at config.TokenURL.
//
// https://developers.openai.com/api/reference/workload-identity-federation
func NewOpenAIWorkloadIdentityTokenProvider(config OpenAIWorkloadIdentityConfig, subjectTokens TokenProvider) (TokenProvider, error) {
	if config.TokenURL == "" {
		return nil, errors.New("token exchange URL must not be empty")
	}
	if config.IdentityProviderID == "" {
		return nil, errors.New("identity provider ID must not be empty")
	}
	if config.ServiceAccountID == "" {
		return nil, errors.New("service account ID must not be empty")
	}
	if subjectTokens == nil {
		return nil, errors.New("subject token provider must not be nil")
	}
	transport, err := newOpenAITransport()
	if err != nil {
		return nil, err
	}
	return &openAIWorkloadIdentityTokenProvider{
		config:        config,
		subjectTokens: subjectTokens,
		httpClient:    &http.Client{Timeout: time.Minute, Transport: transport},
	}, nil
}

// newOpenAITransport returns an http.RoundTripper that uses the proxy from AI_GATEWAY_OPENAI_PROXY_URL
// if set, and otherwise the default transport's proxy settings.
func newOpenAITransport() (http.RoundTripper, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURLStr := os.Getenv(OpenAIProxyEnvVar); proxyURLStr != "" {
		proxyURL, err := url.Parse(proxyURLStr)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", OpenAIProxyEnvVar, err)
		}
		t.Proxy = http.ProxyURL(proxyURL)
	}
	return t, nil
}

// GetToken implements TokenProvider.GetToken method to retrieve an OpenAI access token and its expiration time.
//
// The expiration time is taken from expires_at.
func (t *openAIWorkloadIdentityTokenProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	subject, err := t.subjectTokens.GetToken(ctx)
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to get subject token: %w", err)
	}

	reqBody, err := json.Marshal(openAITokenExchangeRequest{
		GrantType:          tokenExchangeGrantType,
		SubjectToken:       subject.Token,
		SubjectTokenType:   jwtTokenType,
		IdentityProviderID: t.config.IdentityProviderID,
		ServiceAccountID:   t.config.ServiceAccountID,
	})
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to encode token exchange request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.config.TokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to create token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to send token exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to read token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp openAIErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			return TokenExpiry{}, fmt.Errorf("token exchange failed with status %d: %s: %s",
				resp.StatusCode, errResp.Error.Code, errResp.Error.Message)
		}
		if len(body) > maxErrorBodyLength {
			body = body[:maxErrorBodyLength]
		}
		return TokenExpiry{}, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, body)
	}

	var tokenResp openAITokenExchangeResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to decode token exchange response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return TokenExpiry{}, errors.New("token exchange response has no access_token")
	}
	if tokenResp.ExpiresAt <= 0 {
		return TokenExpiry{}, errors.New("token exchange response has no expires_at")
	}
	return TokenExpiry{Token: tokenResp.AccessToken, ExpiresAt: time.Unix(tokenResp.ExpiresAt, 0)}, nil
}
