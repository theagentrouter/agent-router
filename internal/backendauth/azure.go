// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/envoyproxy/ai-gateway/internal/azureauth"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

type azureHandler struct {
	azureAccessToken string                 // The Azure access token used for authentication (static token).
	credential       azcore.TokenCredential // Managed identity credential (caches and refreshes tokens).
}

func newAzureHandler(auth *filterapi.AzureAuth) (filterapi.BackendAuthHandler, error) {
	if auth.ManagedIdentity != nil {
		credential, err := azureauth.NewManagedIdentityCredential(auth.ManagedIdentity.ClientID)
		if err != nil {
			return nil, err
		}
		return &azureHandler{credential: credential}, nil
	}
	return &azureHandler{azureAccessToken: strings.TrimSpace(auth.AccessToken)}, nil
}

// Do implements [Handler.Do].
//
// Sets the Authorization header from the managed identity credential when configured, otherwise from the static access token.
func (a *azureHandler) Do(ctx context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	accessToken := a.azureAccessToken
	if a.credential != nil {
		token, err := a.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{azureauth.Scope}})
		if err != nil {
			return nil, fmt.Errorf("failed to get Azure access token: %w", err)
		}
		accessToken = token.Token
	}
	requestHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)
	return []internalapi.Header{{"Authorization", fmt.Sprintf("Bearer %s", accessToken)}}, nil
}
