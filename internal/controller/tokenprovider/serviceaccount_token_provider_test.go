// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestNewServiceAccountTokenProvider(t *testing.T) {
	kube := fake.NewClientset()
	for _, tc := range []struct {
		name                        string
		namespace, saName, audience string
		expErr                      string
	}{
		{name: "empty namespace", saName: "sa", audience: "aud", expErr: "namespace and name must not be empty"},
		{name: "empty name", namespace: "ns", audience: "aud", expErr: "namespace and name must not be empty"},
		{name: "empty audience", namespace: "ns", saName: "sa", expErr: "audience must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewServiceAccountTokenProvider(kube, tc.namespace, tc.saName, tc.audience)
			require.ErrorContains(t, err, tc.expErr)
			require.Nil(t, provider)
		})
	}

	t.Run("nil client", func(t *testing.T) {
		provider, err := NewServiceAccountTokenProvider(nil, "ns", "sa", "aud")
		require.ErrorContains(t, err, "kubernetes client must not be nil")
		require.Nil(t, provider)
	})

	t.Run("valid", func(t *testing.T) {
		provider, err := NewServiceAccountTokenProvider(kube, "ns", "sa", "aud")
		require.NoError(t, err)
		require.IsType(t, &serviceAccountTokenProvider{}, provider)
	})
}

func TestServiceAccountTokenProvider_GetToken(t *testing.T) {
	t.Run("new token per call", func(t *testing.T) {
		expiresAt := time.Now().Add(10 * time.Minute).Truncate(time.Second)
		kube := fake.NewClientset()
		var requests []*authenticationv1.TokenRequest
		kube.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
			create := action.(k8stesting.CreateActionImpl)
			require.Equal(t, "token", create.GetSubresource())
			require.Equal(t, "ns", create.GetNamespace())
			require.Equal(t, "openai-federation", create.Name)
			req := create.GetObject().(*authenticationv1.TokenRequest)
			requests = append(requests, req)
			return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
				Token:               fmt.Sprintf("token-%d", len(requests)),
				ExpirationTimestamp: metav1.NewTime(expiresAt),
			}}, nil
		})
		provider, err := NewServiceAccountTokenProvider(kube, "ns", "openai-federation", "https://api.openai.com/v1")
		require.NoError(t, err)

		first, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, TokenExpiry{Token: "token-1", ExpiresAt: expiresAt}, first)
		second, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, "token-2", second.Token)

		require.Len(t, requests, 2)
		for _, req := range requests {
			require.Equal(t, []string{"https://api.openai.com/v1"}, req.Spec.Audiences)
			require.Equal(t, int64(serviceAccountTokenExpirationSeconds), *req.Spec.ExpirationSeconds)
			require.Nil(t, req.Spec.BoundObjectRef)
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		kube := fake.NewClientset()
		kube.PrependReactor("create", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, "openai-federation", fmt.Errorf("RBAC: access denied"))
		})
		provider, err := NewServiceAccountTokenProvider(kube, "ns", "openai-federation", "aud")
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to request token for ServiceAccount ns/openai-federation")
		require.True(t, apierrors.IsForbidden(err))
	})

	t.Run("empty token", func(t *testing.T) {
		kube := fake.NewClientset()
		kube.PrependReactor("create", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, &authenticationv1.TokenRequest{}, nil
		})
		provider, err := NewServiceAccountTokenProvider(kube, "ns", "openai-federation", "aud")
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.EqualError(t, err, "TokenRequest for ServiceAccount ns/openai-federation returned no token")
	})
}
