// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"net/http"
	"net/url"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// azureTokenProvider is a provider implements TokenProvider interface for Azure access tokens.
type azureClientSecretTokenProvider struct {
	credential  *azidentity.ClientSecretCredential
	tokenOption policy.TokenRequestOptions
}

// NewAzureClientSecretTokenProvider creates a new TokenProvider with the given tenant ID, client ID, client secret, and token request options.
func NewAzureClientSecretTokenProvider(tenantID, clientID, clientSecret string, tokenOption policy.TokenRequestOptions) (TokenProvider, error) {
	clientOptions := GetClientSecretCredentialOptions()
	credential, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, clientOptions)
	if err != nil {
		return nil, err
	}
	return &azureClientSecretTokenProvider{credential: credential, tokenOption: tokenOption}, nil
}

// GetToken implements TokenProvider.GetToken method to retrieve an Azure access token and its expiration time.
func (a *azureClientSecretTokenProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	azureToken, err := a.credential.GetToken(ctx, a.tokenOption)
	if err != nil {
		return TokenExpiry{}, err
	}
	return TokenExpiry{Token: azureToken.Token, ExpiresAt: azureToken.ExpiresOn}, nil
}

func GetClientSecretCredentialOptions() *azidentity.ClientSecretCredentialOptions {
	if azureProxyURL := os.Getenv("AI_GATEWAY_AZURE_PROXY_URL"); azureProxyURL != "" {
		proxyURL, err := url.Parse(azureProxyURL)
		if err == nil {
			customTransport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
			customHTTPClient := &http.Client{Transport: customTransport}
			return &azidentity.ClientSecretCredentialOptions{
				Transport: customHTTPClient,
			}
		}
	}
	return nil
}
