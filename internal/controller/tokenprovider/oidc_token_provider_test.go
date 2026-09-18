// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	oidcv3 "github.com/coreos/go-oidc/v3/oidc"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestOidcTokenProvider_GetToken(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	secret := &corev1.Secret{
		Name:      "clientSecret",
		Namespace: "default",
		Data: map[string][]byte{
			"client-secret": []byte("some-client-secret"),
		},
	}
	secretErr := client.Create(context.Background(), secret)
	require.NoError(t, secretErr)

	testCases := []struct {
		testName  string
		expErr    bool
		expErrMsg string
		oidcStr   string
	}{
		{
			"invalid oidc config",
			true,
			"failed to create oidc config",
			`{"issuer": "issuer", "token_endpoint": "token_endpoint", }`,
		},

		{
			"invalid issuer",
			true,
			"issuer is required in oidc provider config",
			`{"issuer": "", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": ["scope1", "scope2"]}`,
		},

		{
			"invalid claim scope",
			true,
			"failed to get scopes_supported field in claim",
			`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": ""}`,
		},

		{
			"invalid token endpoint",
			true,
			"token_endpoint is required in oidc provider config",
			`{"issuer": "issuer", "token_endpoint": "", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": ["scope1", "scope2"]}`,
		},

		{
			"valid claim scope endpoint",
			false,
			"",
			`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": ["scope3"]}`,
		},
	}

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		b, err := json.Marshal(oauth2.Token{AccessToken: "some-access-token", TokenType: "Bearer", Expiry: time.Now().Add(5 * time.Minute)})
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(tc.oidcStr))
				require.NoError(t, err)
			}))
			defer discoveryServer.Close()

			ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)

			oidcConfig := &egv1a1.OIDC{
				ClientID: new("clientID"),
				ClientSecret: gwapiv1.SecretObjectReference{
					Name:      "clientSecret",
					Namespace: ptr.To[gwapiv1.Namespace]("default"),
				},
				Provider: egv1a1.OIDCProvider{
					Issuer:        discoveryServer.URL,
					TokenEndpoint: &tokenServer.URL,
				},
				Scopes: []string{"scope1", "scope2"},
			}
			provider, err := NewOidcTokenProvider(ctx, client, oidcConfig)
			if tc.expErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expErrMsg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, provider)
			}
		})
	}
}

func TestOidcTokenProvider_GetToken_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	secret := &corev1.Secret{
		Name:      "clientSecret",
		Namespace: "default",
		Data: map[string][]byte{
			"client-secret": []byte("some-client-secret"),
		},
	}
	secretErr := client.Create(context.Background(), secret)
	require.NoError(t, secretErr)

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": []}`))
		require.NoError(t, err)
	}))
	defer discoveryServer.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		b, err := json.Marshal(oauth2.Token{AccessToken: "some-access-token", ExpiresIn: 60})
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}))

	t.Run("successfully get token", func(t *testing.T) {
		ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)

		oidcConfig := &egv1a1.OIDC{
			ClientID: new("clientID"),
			ClientSecret: gwapiv1.SecretObjectReference{
				Name:      "clientSecret",
				Namespace: ptr.To[gwapiv1.Namespace]("default"),
			},
			Provider: egv1a1.OIDCProvider{
				Issuer:        discoveryServer.URL,
				TokenEndpoint: &tokenServer.URL,
			},
			Scopes: []string{"scope1", "scope2"},
		}

		provider, err := NewOidcTokenProvider(ctx, client, oidcConfig)
		require.NoError(t, err)
		require.NotNil(t, provider)
		token, err := provider.GetToken(ctx)
		require.NoError(t, err)
		require.NotNil(t, token)
		require.Equal(t, "some-access-token", token.Token)
		require.WithinRange(t, token.ExpiresAt, time.Now().Add(-1*time.Minute), time.Now().Add(time.Minute))
	})
}
