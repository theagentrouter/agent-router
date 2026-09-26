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

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// azureManagedIdentityTokenProvider is a provider that implements TokenProvider interface for Azure access
// tokens obtained with a managed identity available in the controller's environment, without a client secret.
type azureManagedIdentityTokenProvider struct {
	credential  azcore.TokenCredential
	tokenOption policy.TokenRequestOptions
}

// NewAzureManagedIdentityTokenProvider creates a new TokenProvider using an Azure managed identity.
//
// When clientID is empty, [azidentity.DefaultAzureCredential] is used, which supports:
//   - Environment variables (AZURE_CLIENT_ID, AZURE_TENANT_ID, AZURE_CLIENT_SECRET, AZURE_FEDERATED_TOKEN_FILE)
//   - Azure Workload Identity (federated service account tokens injected on AKS)
//   - System-assigned managed identity (IMDS)
//   - Azure CLI credentials (for local development)
//
// When clientID is set, the user-assigned managed identity with that client ID is used, preferring
// Azure Workload Identity when the federated service account token is available and falling back to IMDS.
func NewAzureManagedIdentityTokenProvider(_ context.Context, clientID string, tokenOption policy.TokenRequestOptions) (TokenProvider, error) {
	var credential azcore.TokenCredential
	var err error
	if clientID == "" {
		credential, err = azidentity.NewDefaultAzureCredential(GetDefaultAzureCredentialOptions())
	} else {
		credential, err = newUserAssignedManagedIdentityCredential(clientID)
	}
	if err != nil {
		return nil, err
	}
	return &azureManagedIdentityTokenProvider{credential: credential, tokenOption: tokenOption}, nil
}

// GetToken implements TokenProvider.GetToken method to retrieve an Azure access token and its expiration time.
func (a *azureManagedIdentityTokenProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	azureToken, err := a.credential.GetToken(ctx, a.tokenOption)
	if err != nil {
		return TokenExpiry{}, err
	}
	return TokenExpiry{Token: azureToken.Token, ExpiresAt: azureToken.ExpiresOn}, nil
}

// newUserAssignedManagedIdentityCredential builds a credential for a user-assigned managed identity
// selected by client ID. Azure Workload Identity is preferred when the federated service account
// token environment is present (e.g. on AKS clusters with workload identity enabled, whose webhook
// injects AZURE_FEDERATED_TOKEN_FILE and AZURE_TENANT_ID), and IMDS-based managed identity is used
// as a fallback (e.g. identities attached to the cluster's virtual machines).
func newUserAssignedManagedIdentityCredential(clientID string) (azcore.TokenCredential, error) {
	clientOptions := getAzureCoreClientOptions()
	var sources []azcore.TokenCredential
	// NewWorkloadIdentityCredential fails when the federated token environment is not injected,
	// in which case it is simply left out of the chain.
	if workloadIdentity, err := azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{
		ClientOptions: clientOptions,
		ClientID:      clientID,
	}); err == nil {
		sources = append(sources, workloadIdentity)
	}
	managedIdentity, err := azidentity.NewManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{
		ClientOptions: clientOptions,
		ID:            azidentity.ClientID(clientID),
	})
	if err != nil {
		return nil, err
	}
	sources = append(sources, managedIdentity)
	return azidentity.NewChainedTokenCredential(sources, nil)
}

// GetDefaultAzureCredentialOptions returns the client options for DefaultAzureCredential,
// including proxy configuration if set via the AI_GATEWAY_AZURE_PROXY_URL environment variable.
func GetDefaultAzureCredentialOptions() *azidentity.DefaultAzureCredentialOptions {
	return &azidentity.DefaultAzureCredentialOptions{ClientOptions: getAzureCoreClientOptions()}
}

// getAzureCoreClientOptions returns azcore client options with the proxy transport configured
// via the AI_GATEWAY_AZURE_PROXY_URL environment variable, if set.
func getAzureCoreClientOptions() (options azcore.ClientOptions) {
	if azureProxyURL := os.Getenv("AI_GATEWAY_AZURE_PROXY_URL"); azureProxyURL != "" {
		if proxyURL, err := url.Parse(azureProxyURL); err == nil {
			options.Transport = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		}
	}
	return
}
