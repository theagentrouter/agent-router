// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/azureauth"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestNewAzureHandler(t *testing.T) {
	auth := filterapi.AzureAuth{AccessToken: " some-access-token \n"}
	handler, err := newAzureHandler(&auth)
	require.NoError(t, err)
	require.NotNil(t, handler)

	require.Equal(t, "some-access-token", handler.(*azureHandler).azureAccessToken)
}

func TestNewAzureHandler_Do(t *testing.T) {
	auth := filterapi.AzureAuth{AccessToken: "some-access-token"}
	handler, err := newAzureHandler(&auth)
	require.NoError(t, err)
	require.NotNil(t, handler)

	requestHeaders := map[string]string{":method": "POST", ":path": "/model/some-random-model/chat/completion"}
	headers, err := handler.Do(t.Context(), requestHeaders, []byte(`{"messages": [{"role": "user", "content": [{"text": "Say this is a test!"}]}]}`))
	require.NoError(t, err)

	bearerToken, ok := requestHeaders["Authorization"]
	require.True(t, ok)
	require.Equal(t, "Bearer some-access-token", bearerToken)

	require.Len(t, headers, 1)
	require.Equal(t, "Authorization", headers[0][0])
	require.Equal(t, "Bearer some-access-token", headers[0][1])
}

// fakeAzureCredential implements [azcore.TokenCredential] for unit tests.
type fakeAzureCredential struct {
	token  string
	err    error
	scopes []string
}

func (f *fakeAzureCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.scopes = options.Scopes
	return azcore.AccessToken{Token: f.token}, f.err
}

func TestNewAzureHandler_ManagedIdentity(t *testing.T) {
	auth := filterapi.AzureAuth{ManagedIdentity: &filterapi.AzureManagedIdentityAuth{ClientID: "some-client-id"}}
	handler, err := newAzureHandler(&auth)
	require.NoError(t, err)
	require.NotNil(t, handler.(*azureHandler).credential)
	require.Empty(t, handler.(*azureHandler).azureAccessToken)
}

func TestNewAzureHandler_ManagedIdentityDo(t *testing.T) {
	credential := &fakeAzureCredential{token: "managed-identity-token"}
	handler := &azureHandler{credential: credential}

	requestHeaders := map[string]string{":method": "POST"}
	headers, err := handler.Do(t.Context(), requestHeaders, nil)
	require.NoError(t, err)
	require.Equal(t, []string{azureauth.Scope}, credential.scopes)
	require.Equal(t, "Bearer managed-identity-token", requestHeaders["Authorization"])
	require.Equal(t, []internalapi.Header{{"Authorization", "Bearer managed-identity-token"}}, headers)

	handler.credential = &fakeAzureCredential{err: errors.New("imds unavailable")}
	_, err = handler.Do(t.Context(), map[string]string{}, nil)
	require.ErrorContains(t, err, "failed to get Azure access token: imds unavailable")
}
