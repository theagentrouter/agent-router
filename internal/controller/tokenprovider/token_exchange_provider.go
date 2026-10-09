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
	"net/url"
	"strings"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// tokenExchangeGrantType is the RFC 8693 grant type for token exchange requests.
const tokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101

// TokenExchangeConfig holds the parameters of an RFC 8693 token exchange request.
type TokenExchangeConfig struct {
	// TokenURL is the token exchange endpoint.
	TokenURL string
	// SubjectTokenType is the subject_token_type parameter.
	SubjectTokenType string
	// Audience is the optional audience parameter.
	Audience string
	// Scopes are joined with spaces into the optional scope parameter.
	Scopes []string
}

// tokenExchangeProvider is a provider implements TokenProvider interface for OAuth 2.0 Token Exchange.
type tokenExchangeProvider struct {
	config        TokenExchangeConfig
	subjectTokens TokenProvider
	httpClient    *http.Client
}

// tokenExchangeResponse is the successful response of RFC 8693 section 2.2.1.
type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// tokenExchangeErrorResponse is the error response of RFC 6749 section 5.2.
type tokenExchangeErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// NewTokenExchangeProvider creates a new TokenProvider that obtains a subject token from subjectTokens
// and exchanges it for an access token at config.TokenURL, following RFC 8693.
//
// https://datatracker.ietf.org/doc/html/rfc8693
func NewTokenExchangeProvider(config TokenExchangeConfig, subjectTokens TokenProvider) (TokenProvider, error) {
	if config.TokenURL == "" {
		return nil, errors.New("token exchange URL must not be empty")
	}
	if config.SubjectTokenType == "" {
		return nil, errors.New("token exchange subject token type must not be empty")
	}
	if subjectTokens == nil {
		return nil, errors.New("token exchange subject token provider must not be nil")
	}
	return &tokenExchangeProvider{
		config:        config,
		subjectTokens: subjectTokens,
		httpClient:    &http.Client{Timeout: time.Minute},
	}, nil
}

// GetToken implements TokenProvider.GetToken method to retrieve an exchanged access token and its expiration time.
//
// When the response has no expires_in, the access token is assumed to expire with the subject token.
func (t *tokenExchangeProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	subject, err := t.subjectTokens.GetToken(ctx)
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to get subject token: %w", err)
	}

	form := url.Values{
		"grant_type":         {tokenExchangeGrantType},
		"subject_token":      {subject.Token},
		"subject_token_type": {t.config.SubjectTokenType},
	}
	if t.config.Audience != "" {
		form.Set("audience", t.config.Audience)
	}
	if len(t.config.Scopes) > 0 {
		form.Set("scope", strings.Join(t.config.Scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to create token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	issuedAt := time.Now()
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
		var errResp tokenExchangeErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return TokenExpiry{}, fmt.Errorf("token exchange failed with status %d: %s: %s",
				resp.StatusCode, errResp.Error, errResp.ErrorDescription)
		}
		return TokenExpiry{}, fmt.Errorf("token exchange failed with status %d", resp.StatusCode)
	}

	var tokenResp tokenExchangeResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to decode token exchange response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return TokenExpiry{}, errors.New("token exchange response has no access_token")
	}
	expiresAt := subject.ExpiresAt
	if tokenResp.ExpiresIn > 0 {
		expiresAt = issuedAt.Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return TokenExpiry{Token: tokenResp.AccessToken, ExpiresAt: expiresAt}, nil
}
