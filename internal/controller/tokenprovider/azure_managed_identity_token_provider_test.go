// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/require"
)

// fakeAzureCredential implements [azcore.TokenCredential] for unit tests.
type fakeAzureCredential struct {
	token     string
	expiresOn time.Time
	err       error
}

func (f *fakeAzureCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: f.token, ExpiresOn: f.expiresOn}, f.err
}

func TestNewAzureManagedIdentityTokenProvider(t *testing.T) {
	t.Run("system-assigned managed identity", func(t *testing.T) {
		provider, err := NewAzureManagedIdentityTokenProvider(t.Context(), "", policy.TokenRequestOptions{})
		require.NoError(t, err)
		require.NotNil(t, provider)
	})

	t.Run("user-assigned managed identity", func(t *testing.T) {
		provider, err := NewAzureManagedIdentityTokenProvider(t.Context(), "some-client-id", policy.TokenRequestOptions{})
		require.NoError(t, err)
		require.NotNil(t, provider)
	})

	t.Run("user-assigned managed identity with workload identity env", func(t *testing.T) {
		tokenFile := filepath.Join(t.TempDir(), "token")
		t.Setenv("AZURE_FEDERATED_TOKEN_FILE", tokenFile)
		t.Setenv("AZURE_TENANT_ID", "some-tenant-id")
		provider, err := NewAzureManagedIdentityTokenProvider(t.Context(), "some-client-id", policy.TokenRequestOptions{})
		require.NoError(t, err)
		require.NotNil(t, provider)
	})
}

func TestNewAzureManagedIdentityTokenProvider_GetToken(t *testing.T) {
	expiresOn := time.Now().Add(time.Hour)

	t.Run("returns token and expiry from credential", func(t *testing.T) {
		provider := &azureManagedIdentityTokenProvider{
			credential:  &fakeAzureCredential{token: "some-token", expiresOn: expiresOn},
			tokenOption: policy.TokenRequestOptions{Scopes: []string{"some-azure-scope"}},
		}
		tokenExpiry, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, "some-token", tokenExpiry.Token)
		require.Equal(t, expiresOn, tokenExpiry.ExpiresAt)
	})

	t.Run("propagates credential error", func(t *testing.T) {
		provider := &azureManagedIdentityTokenProvider{
			credential:  &fakeAzureCredential{err: errors.New("some-azure-error")},
			tokenOption: policy.TokenRequestOptions{Scopes: []string{"some-azure-scope"}},
		}
		tokenExpiry, err := provider.GetToken(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "some-azure-error")
		require.Empty(t, tokenExpiry.Token)
		require.True(t, tokenExpiry.ExpiresAt.IsZero())
	})

	t.Run("azure proxy url", func(t *testing.T) {
		// Set environment variable for the test.
		mockProxyURL := "http://localhost:8888"
		t.Setenv("AI_GATEWAY_AZURE_PROXY_URL", mockProxyURL)

		opts := GetDefaultAzureCredentialOptions()

		require.NotNil(t, opts)
		require.NotNil(t, opts.Transport)

		// Assert that the transport has a proxy set.
		transport, ok := opts.Transport.(*http.Client)
		require.True(t, ok)
		require.NotNil(t, transport.Transport)

		// Check the proxy URL.
		innerTransport, ok := transport.Transport.(*http.Transport)
		require.True(t, ok)
		require.NotNil(t, innerTransport.Proxy)

		req, _ := http.NewRequest("GET", "http://example.com", nil)
		proxyFunc := innerTransport.Proxy
		proxyURL, err := proxyFunc(req)
		require.NoError(t, err)
		require.Equal(t, "http://localhost:8888", proxyURL.String())
	})
}
