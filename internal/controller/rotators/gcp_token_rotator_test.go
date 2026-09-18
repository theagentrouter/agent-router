// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package rotators

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
)

func TestGCPTokenRotator_Rotate(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	t.Run("failed to get gcp token", func(t *testing.T) {
		now := time.Now()
		oneHourBeforeNow := now.Add(-1 * time.Hour)
		twoHourAfterNow := now.Add(2 * time.Hour)
		mockProvider := tokenprovider.NewMockTokenProvider("fake-token", twoHourAfterNow, fmt.Errorf("failed to get gcp access token"))

		secret := &corev1.Secret{
			Name:      GetBSPSecretName("test-policy"),
			Namespace: "default",
			Annotations: map[string]string{
				ExpirationTimeAnnotationKey: oneHourBeforeNow.Format(time.RFC3339),
			},
			Data: map[string][]byte{
				GCPAccessTokenKey: []byte("some-gcp-access-token"),
			},
		}
		err := client.Create(context.Background(), secret)
		require.NoError(t, err)

		rotator := &gcpTokenRotator{
			client:                         client,
			backendSecurityPolicyNamespace: "default",
			backendSecurityPolicyName:      "test-policy",
			preRotationWindow:              5 * time.Minute,
			tokenProvider:                  mockProvider,
		}

		_, err = rotator.Rotate(context.Background())
		require.Error(t, err)
		err = client.Delete(context.Background(), secret)
		require.NoError(t, err)
	})

	t.Run("secret does not exist", func(t *testing.T) {
		now := time.Now()
		twoHourAfterNow := now.Add(2 * time.Hour)
		mockProvider := tokenprovider.NewMockTokenProvider("fake-token", twoHourAfterNow, nil)

		rotator := &gcpTokenRotator{
			client: client,

			backendSecurityPolicyNamespace: "default",
			backendSecurityPolicyName:      "test-policy",
			preRotationWindow:              5 * time.Minute,
			tokenProvider:                  mockProvider,
		}
		expiration, err := rotator.Rotate(context.Background())
		require.NoError(t, err)
		secret, err := LookupSecret(context.Background(), client, "default", GetBSPSecretName("test-policy"))
		require.NoError(t, err)
		err = client.Delete(context.Background(), secret)
		require.NoError(t, err)
		require.Equal(t, twoHourAfterNow, expiration)
	})

	t.Run("secret exist", func(t *testing.T) {
		now := time.Now()
		twoHourAfterNow := now.Add(2 * time.Hour)
		oneHourBeforeNow := now.Add(-1 * time.Hour)
		mockProvider := tokenprovider.NewMockTokenProvider("fake-token", twoHourAfterNow, nil)

		secret := &corev1.Secret{
			Name:      GetBSPSecretName("test-policy"),
			Namespace: "default",
			Annotations: map[string]string{
				ExpirationTimeAnnotationKey: oneHourBeforeNow.Format(time.RFC3339),
			},
			Data: map[string][]byte{
				GCPAccessTokenKey: []byte("some-gcp-access-token"),
			},
		}
		err := client.Create(context.Background(), secret)
		require.NoError(t, err)

		rotator := &gcpTokenRotator{
			client:                         client,
			tokenProvider:                  mockProvider,
			backendSecurityPolicyNamespace: "default",
			backendSecurityPolicyName:      "test-policy",
			preRotationWindow:              5 * time.Minute,
		}

		expiration, err := rotator.Rotate(context.Background())
		require.NoError(t, err)
		require.Equal(t, twoHourAfterNow, expiration)

		err = client.Delete(context.Background(), secret)
		require.NoError(t, err)
	})
}

func TestGCPTokenRotator_GetPreRotationTime(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	rotator := &gcpTokenRotator{
		client:                         client,
		preRotationWindow:              5 * time.Minute,
		backendSecurityPolicyNamespace: "default",
		backendSecurityPolicyName:      "test-policy",
	}

	now := time.Now()

	tests := []struct {
		name          string
		secret        *corev1.Secret
		expectedTime  time.Time
		expectedError bool
	}{
		{
			name: "secret annotation missing",
			secret: &corev1.Secret{
				Name:      GetBSPSecretName("test-policy"),
				Namespace: "default",
				Data: map[string][]byte{
					GCPAccessTokenKey: []byte("some-gcp-access-token"),
				},
			},
			expectedTime:  time.Time{},
			expectedError: true,
		},
		{
			name: "rotation time before expiration time",
			secret: &corev1.Secret{
				Name:      GetBSPSecretName("test-policy"),
				Namespace: "default",
				Annotations: map[string]string{
					ExpirationTimeAnnotationKey: now.Add(2 * time.Hour).Format(time.RFC3339),
				},
				Data: map[string][]byte{
					GCPAccessTokenKey: []byte("some-gcp-access-token"),
				},
			},
			expectedTime:  now.Add(2 * time.Hour),
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.Create(context.Background(), tt.secret)
			require.NoError(t, err)

			got, err := rotator.GetPreRotationTime(context.Background())
			if (err != nil) != tt.expectedError {
				t.Errorf("GCPTokenRotator.GetPreRotationTime() error = %v, expectedError %v", err, tt.expectedError)
				return
			}
			if !tt.expectedTime.IsZero() && got.Compare(tt.expectedTime) >= 0 {
				t.Errorf("GCPTokenRotator.GetPreRotationTime() = %v, expected %v", got, tt.expectedTime)
			}
			err = client.Delete(context.Background(), tt.secret)
			require.NoError(t, err)
		})
	}
}

func TestGCPTokenRotator_IsExpired(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	rotator := &gcpTokenRotator{
		client: client,
	}
	tests := []struct {
		name       string
		expiration time.Time
		expect     bool
	}{
		{
			name:       "not expired",
			expiration: time.Now().Add(1 * time.Hour),
			expect:     false,
		},
		{
			name:       "expired",
			expiration: time.Now().Add(-1 * time.Hour),
			expect:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rotator.IsExpired(tt.expiration); got != tt.expect {
				t.Errorf("GCPTokenRotator.IsExpired() = %v, expect %v", got, tt.expect)
			}
		})
	}
}

func TestPopulateGCPAccessToken(t *testing.T) {
	secret := &corev1.Secret{}
	expiration := time.Now()

	gcpAccessToken := tokenprovider.TokenExpiry{Token: "some-gcp-token", ExpiresAt: expiration}
	populateGCPAccessToken(secret, &gcpAccessToken)

	annotation, ok := secret.Annotations[ExpirationTimeAnnotationKey]
	require.True(t, ok)
	require.Equal(t, expiration.Format(time.RFC3339), annotation)

	require.Len(t, secret.Data, 1)
	val, ok := secret.Data[GCPAccessTokenKey]
	require.True(t, ok)
	require.Equal(t, "some-gcp-token", string(val))
}
