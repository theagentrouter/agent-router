// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package azureauth provides shared Azure authentication utilities.
package azureauth

import (
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

const (
	// ProxyEnvVar is the environment variable name for configuring the Azure auth proxy URL.
	ProxyEnvVar = "AI_GATEWAY_AZURE_PROXY_URL"
	// Scope is the scope of the access tokens used to call Azure OpenAI and other Azure AI services.
	Scope = "https://cognitiveservices.azure.com/.default"

	// federatedTokenFileEnvVar is set by Azure Workload Identity on the pods using it.
	federatedTokenFileEnvVar = "AZURE_FEDERATED_TOKEN_FILE"
)

// ClientOptions returns azcore client options with the proxy transport configured
// via the AI_GATEWAY_AZURE_PROXY_URL environment variable, if set.
func ClientOptions() (options azcore.ClientOptions) {
	if azureProxyURL := os.Getenv(ProxyEnvVar); azureProxyURL != "" {
		if proxyURL, err := url.Parse(azureProxyURL); err == nil {
			options.Transport = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		}
	}
	return
}

// NewManagedIdentityCredential returns a credential for the Azure managed identity available to the
// current pod.
//
// When clientID is empty, [azidentity.DefaultAzureCredential] is used, which supports:
//   - Environment variables (AZURE_CLIENT_ID, AZURE_TENANT_ID, AZURE_CLIENT_SECRET, AZURE_FEDERATED_TOKEN_FILE)
//   - Azure Workload Identity (federated service account tokens injected on AKS)
//   - System-assigned managed identity (IMDS)
//   - Azure CLI credentials (for local development)
//
// When clientID is set, the user-assigned managed identity with that client ID is used, through
// Azure Workload Identity when the pod has it and through IMDS otherwise.
func NewManagedIdentityCredential(clientID string) (azcore.TokenCredential, error) {
	clientOptions := ClientOptions()
	if clientID == "" {
		return azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{ClientOptions: clientOptions})
	}
	var sources []azcore.TokenCredential
	// Workload Identity is only skipped when its token isn't injected, so that a misconfiguration
	// such as a missing AZURE_TENANT_ID isn't hidden behind an IMDS error.
	if os.Getenv(federatedTokenFileEnvVar) != "" {
		workloadIdentity, err := azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{
			ClientOptions: clientOptions,
			ClientID:      clientID,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure workload identity credential: %w", err)
		}
		sources = append(sources, workloadIdentity)
	}
	managedIdentity, err := azidentity.NewManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{
		ClientOptions: clientOptions,
		ID:            azidentity.ClientID(clientID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure managed identity credential: %w", err)
	}
	sources = append(sources, managedIdentity)
	return azidentity.NewChainedTokenCredential(sources, nil)
}
