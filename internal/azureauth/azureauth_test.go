// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package azureauth

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientOptions(t *testing.T) {
	t.Run("no proxy", func(t *testing.T) {
		t.Setenv(ProxyEnvVar, "")
		require.Nil(t, ClientOptions().Transport)
	})

	t.Run("proxy", func(t *testing.T) {
		t.Setenv(ProxyEnvVar, "http://localhost:8888")
		opts := ClientOptions()
		client, ok := opts.Transport.(*http.Client)
		require.True(t, ok)
		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
		require.NoError(t, err)
		proxyURL, err := transport.Proxy(req)
		require.NoError(t, err)
		require.Equal(t, "http://localhost:8888", proxyURL.String())
	})

	t.Run("invalid proxy is ignored", func(t *testing.T) {
		t.Setenv(ProxyEnvVar, "://invalid")
		require.Nil(t, ClientOptions().Transport)
	})
}

func TestNewManagedIdentityCredential(t *testing.T) {
	t.Run("default credential chain", func(t *testing.T) {
		credential, err := NewManagedIdentityCredential("")
		require.NoError(t, err)
		require.NotNil(t, credential)
	})

	t.Run("user-assigned managed identity", func(t *testing.T) {
		t.Setenv(federatedTokenFileEnvVar, "")
		credential, err := NewManagedIdentityCredential("some-client-id")
		require.NoError(t, err)
		require.NotNil(t, credential)
	})

	t.Run("user-assigned managed identity with workload identity", func(t *testing.T) {
		t.Setenv(federatedTokenFileEnvVar, filepath.Join(t.TempDir(), "token"))
		t.Setenv("AZURE_TENANT_ID", "some-tenant-id")
		credential, err := NewManagedIdentityCredential("some-client-id")
		require.NoError(t, err)
		require.NotNil(t, credential)
	})

	t.Run("workload identity without tenant ID", func(t *testing.T) {
		t.Setenv(federatedTokenFileEnvVar, filepath.Join(t.TempDir(), "token"))
		t.Setenv("AZURE_TENANT_ID", "")
		_, err := NewManagedIdentityCredential("some-client-id")
		require.ErrorContains(t, err, "failed to create Azure workload identity credential")
	})
}
