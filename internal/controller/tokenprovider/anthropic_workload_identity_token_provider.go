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
	// jwtBearerGrantType is the RFC 7523 grant type for JWT bearer assertions.
	jwtBearerGrantType = "urn:ietf:params:oauth:grant-type:jwt-bearer" // #nosec G101
	// AnthropicProxyEnvVar is the environment variable name for configuring the proxy URL of Anthropic token
	// exchange requests.
	AnthropicProxyEnvVar = "AI_GATEWAY_ANTHROPIC_PROXY_URL"
)

// AnthropicTokenExchangeURL is the Anthropic token exchange endpoint. It is not configurable through the API so
// that policies cannot direct the controller to send subject tokens to arbitrary URLs. It is a variable only so
// tests can point it at a local server.
var AnthropicTokenExchangeURL = "https://api.anthropic.com/v1/oauth/token" // #nosec G101

// AnthropicWorkloadIdentityConfig holds the parameters of an Anthropic Workload Identity Federation token exchange.
type AnthropicWorkloadIdentityConfig struct {
	// TokenURL is the Anthropic token exchange endpoint.
	TokenURL string
	// OrganizationID is the UUID of the Anthropic organization.
	OrganizationID string
	// ServiceAccountID is the Anthropic service account ID.
	ServiceAccountID string
	// FederationRuleID is the Anthropic federation rule ID.
	FederationRuleID string
	// WorkspaceID is the optional Anthropic workspace ID.
	WorkspaceID string
}

// anthropicWorkloadIdentityTokenProvider is a provider implements TokenProvider interface for Anthropic
// Workload Identity Federation.
type anthropicWorkloadIdentityTokenProvider struct {
	config        AnthropicWorkloadIdentityConfig
	subjectTokens TokenProvider
	httpClient    *http.Client
}

// anthropicTokenExchangeRequest is the JSON body of an Anthropic token exchange request.
type anthropicTokenExchangeRequest struct {
	GrantType        string `json:"grant_type"`
	Assertion        string `json:"assertion"`
	FederationRuleID string `json:"federation_rule_id"`
	OrganizationID   string `json:"organization_id"`
	ServiceAccountID string `json:"service_account_id"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
}

// anthropicTokenExchangeResponse is the successful response of an Anthropic token exchange.
type anthropicTokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	// ExpiresIn is the lifetime of the access token in seconds.
	ExpiresIn int64 `json:"expires_in"`
}

// anthropicErrorResponse is the error response of the Anthropic API.
type anthropicErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// NewAnthropicWorkloadIdentityTokenProvider creates a new TokenProvider that obtains a JWT subject token from
// subjectTokens and exchanges it for an Anthropic access token at config.TokenURL.
//
// Anthropic accepts a subject token with a "jti" claim only once, so every call to GetToken obtains a new
// subject token, and a failed exchange is not retried with the same subject token.
//
// https://platform.claude.com/docs/en/manage-claude/wif-reference#token-exchange-request
func NewAnthropicWorkloadIdentityTokenProvider(config *AnthropicWorkloadIdentityConfig, subjectTokens TokenProvider) (TokenProvider, error) {
	if config.TokenURL == "" {
		return nil, errors.New("token exchange URL must not be empty")
	}
	if config.OrganizationID == "" {
		return nil, errors.New("organization ID must not be empty")
	}
	if config.ServiceAccountID == "" {
		return nil, errors.New("service account ID must not be empty")
	}
	if config.FederationRuleID == "" {
		return nil, errors.New("federation rule ID must not be empty")
	}
	if subjectTokens == nil {
		return nil, errors.New("subject token provider must not be nil")
	}
	transport, err := newAnthropicTransport()
	if err != nil {
		return nil, err
	}
	return &anthropicWorkloadIdentityTokenProvider{
		config:        *config,
		subjectTokens: subjectTokens,
		httpClient:    &http.Client{Timeout: time.Minute, Transport: transport},
	}, nil
}

// newAnthropicTransport returns an http.RoundTripper that uses the proxy from AI_GATEWAY_ANTHROPIC_PROXY_URL
// if set, and otherwise the default transport's proxy settings.
func newAnthropicTransport() (http.RoundTripper, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURLStr := os.Getenv(AnthropicProxyEnvVar); proxyURLStr != "" {
		proxyURL, err := url.Parse(proxyURLStr)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", AnthropicProxyEnvVar, err)
		}
		t.Proxy = http.ProxyURL(proxyURL)
	}
	return t, nil
}

// GetToken implements TokenProvider.GetToken method to retrieve an Anthropic access token and its expiration time.
//
// The expiration time is computed from expires_in, relative to when the request was sent.
func (t *anthropicWorkloadIdentityTokenProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	subject, err := t.subjectTokens.GetToken(ctx)
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to get subject token: %w", err)
	}

	reqBody, err := json.Marshal(anthropicTokenExchangeRequest{
		GrantType:        jwtBearerGrantType,
		Assertion:        subject.Token,
		FederationRuleID: t.config.FederationRuleID,
		OrganizationID:   t.config.OrganizationID,
		ServiceAccountID: t.config.ServiceAccountID,
		WorkspaceID:      t.config.WorkspaceID,
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

	sentAt := time.Now()
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
		var errResp anthropicErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			err = fmt.Errorf("token exchange failed with status %d: %s: %s",
				resp.StatusCode, errResp.Error.Type, errResp.Error.Message)
			requestID := errResp.RequestID
			if requestID == "" {
				requestID = resp.Header.Get("request-id")
			}
			if requestID != "" {
				err = fmt.Errorf("%w (request-id: %s)", err, requestID)
			}
			return TokenExpiry{}, err
		}
		if len(body) > maxErrorBodyLength {
			body = body[:maxErrorBodyLength]
		}
		return TokenExpiry{}, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, body)
	}

	var tokenResp anthropicTokenExchangeResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to decode token exchange response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return TokenExpiry{}, errors.New("token exchange response has no access_token")
	}
	if tokenResp.ExpiresIn <= 0 {
		return TokenExpiry{}, errors.New("token exchange response has no expires_in")
	}
	return TokenExpiry{Token: tokenResp.AccessToken, ExpiresAt: sentAt.Add(time.Duration(tokenResp.ExpiresIn) * time.Second)}, nil
}
