// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package rotators

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	oidcv3 "github.com/coreos/go-oidc/v3/oidc"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	clientSecretKey    = "client-secret"
	testClientSecret   = "test_client_secret"
	awsProfileName     = "default"
	awsRegion          = "us-east1"
	awsRoleArn         = "test-role"
	oldAwsAccessKey    = "old_aws_access_key"  // #nosec G101
	oldAwsSecretKey    = "old_aws_secret_key"  // #nosec G101
	oldAwsSessionToken = "old_aws_session_key" // #nosec G101
	newAwsAccessKey    = "new_aws_access_key"  // #nosec G101
	newAwsSecretKey    = "new_aws_secret_key"  // #nosec G101
	newAwsSessionToken = "new_aws_session_key" // #nosec G101
	newOidcToken       = "new_oidc_token"      // #nosec G101
	policyNameSpace    = "default"
	policyName         = "test-secret"
)

// createTestAwsSecret creates a test secret with given credentials.
func createTestAwsSecret(t *testing.T, client client.Client, bspName string, accessKey, secretKey, sessionToken string, profile string, awsRegion string) {
	if profile == "" {
		profile = awsProfileName
	}
	data := map[string][]byte{
		AwsCredentialsKey: fmt.Appendf(nil, "[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\nregion = %s\n",
			profile, accessKey, secretKey, sessionToken, awsRegion),
	}
	err := client.Create(t.Context(), &corev1.Secret{
		Name:      GetBSPSecretName(bspName),
		Namespace: policyNameSpace,
		Data:      data,
	})
	require.NoError(t, err)
}

// verifyAwsCredentialsSecret verifies the credentials in a secret.
func verifyAwsCredentialsSecret(t *testing.T, client client.Client, namespace, secretName, expectedKeyID, expectedSecret, expectedToken, profile, region string) {
	secret, err := LookupSecret(t.Context(), client, namespace, GetBSPSecretName(secretName))
	require.NoError(t, err)
	expectedSecretData := fmt.Sprintf("[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\nregion = %s\n", profile, expectedKeyID, expectedSecret, expectedToken, region)
	require.Equal(t, expectedSecretData, string(secret.Data[AwsCredentialsKey]))
}

// createOidcClientSecret creates the OIDC client secret.
func createOidcClientSecret(t *testing.T, client client.Client, name string) {
	data := map[string][]byte{
		clientSecretKey: []byte(testClientSecret),
	}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion,
		&corev1.Secret{},
	)
	err := client.Create(t.Context(), &corev1.Secret{
		Name:      name,
		Namespace: policyNameSpace,
		Data:      data,
	})
	require.NoError(t, err)
}

// MockSTSOperations implements the STSClient interface for testing.
type mockStsOperations struct {
	assumeRoleWithWebIdentityFunc func(ctx context.Context, params *sts.AssumeRoleWithWebIdentityInput, optFns ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error)
}

func (m *mockStsOperations) AssumeRoleWithWebIdentity(ctx context.Context, params *sts.AssumeRoleWithWebIdentityInput, optFns ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	if m.assumeRoleWithWebIdentityFunc != nil {
		return m.assumeRoleWithWebIdentityFunc(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("mock not implemented")
}

func TestAWS_OIDCRotator(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		b, err := json.Marshal(oauth2.Token{AccessToken: "newOidcToken", TokenType: "Bearer", ExpiresIn: 60})
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": ["scope3"]}`))
		require.NoError(t, err)
	}))
	defer discoveryServer.Close()

	oidc := egv1a1.OIDC{
		Provider: egv1a1.OIDCProvider{
			Issuer:        discoveryServer.URL,
			TokenEndpoint: &tokenServer.URL,
		},
		ClientID: new("some-client-id"),
		ClientSecret: gwapiv1.SecretObjectReference{
			Name:      gwapiv1.ObjectName(testClientSecret),
			Namespace: (*gwapiv1.Namespace)(new(policyNameSpace)),
		},
	}

	t.Run("basic rotation", func(t *testing.T) {
		startTime := time.Now()
		var mockSTS STSClient = &mockStsOperations{
			assumeRoleWithWebIdentityFunc: func(_ context.Context, _ *sts.AssumeRoleWithWebIdentityInput, _ ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
				return &sts.AssumeRoleWithWebIdentityOutput{
					Credentials: &types.Credentials{
						AccessKeyId:     aws.String(newAwsAccessKey),
						SecretAccessKey: aws.String(newAwsSecretKey),
						SessionToken:    aws.String(newAwsSessionToken),
						Expiration:      aws.Time(startTime.Add(1 * time.Hour)),
					},
				}, nil
			},
		}
		scheme := runtime.NewScheme()
		scheme.AddKnownTypes(corev1.SchemeGroupVersion,
			&corev1.Secret{},
		)
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		// Setup initial credentials and client secret.
		createTestAwsSecret(t, fakeClient, policyName, oldAwsAccessKey, oldAwsSecretKey, oldAwsSessionToken, awsProfileName, awsRegion)
		createOidcClientSecret(t, fakeClient, testClientSecret)

		awsOidcRotator := AWSOIDCRotator{
			client:                         fakeClient,
			stsClient:                      mockSTS,
			backendSecurityPolicyNamespace: policyNameSpace,
			backendSecurityPolicyName:      policyName,
			oidc:                           &oidc,
			region:                         awsRegion,
			roleArn:                        awsRoleArn,
		}

		ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
		expiration, err := awsOidcRotator.Rotate(ctx)
		require.NoError(t, err)
		require.NotNil(t, expiration)
		require.WithinRange(t, expiration, startTime, startTime.Add(1*time.Hour))
		verifyAwsCredentialsSecret(t, fakeClient, policyNameSpace, policyName, newAwsAccessKey, newAwsSecretKey, newAwsSessionToken, awsProfileName, awsRegion)
	})

	t.Run("error handling - STS assume role failure", func(t *testing.T) {
		scheme := runtime.NewScheme()
		scheme.AddKnownTypes(corev1.SchemeGroupVersion,
			&corev1.Secret{},
		)
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		createTestAwsSecret(t, fakeClient, policyName, oldAwsAccessKey, oldAwsSecretKey, oldAwsSessionToken, awsProfileName, awsRegion)
		createOidcClientSecret(t, fakeClient, testClientSecret)
		var mockSTS STSClient = &mockStsOperations{
			assumeRoleWithWebIdentityFunc: func(_ context.Context, _ *sts.AssumeRoleWithWebIdentityInput, _ ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
				return nil, fmt.Errorf("failed to assume role")
			},
		}
		awsOidcRotator := AWSOIDCRotator{
			client:                         fakeClient,
			stsClient:                      mockSTS,
			backendSecurityPolicyNamespace: policyNameSpace,
			backendSecurityPolicyName:      policyName,
			oidc:                           &oidc,
			region:                         awsRegion,
			roleArn:                        awsRoleArn,
		}

		ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
		expiration, err := awsOidcRotator.Rotate(ctx)
		require.Error(t, err)
		require.True(t, expiration.IsZero())
		assert.Contains(t, err.Error(), "failed to assume role")
	})

	t.Run("rotation - create when aws credential secret does not exist", func(t *testing.T) {
		startTime := time.Now()
		scheme := runtime.NewScheme()
		scheme.AddKnownTypes(corev1.SchemeGroupVersion,
			&corev1.Secret{},
		)
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		createOidcClientSecret(t, fakeClient, testClientSecret)
		var mockSTS STSClient = &mockStsOperations{
			assumeRoleWithWebIdentityFunc: func(_ context.Context, _ *sts.AssumeRoleWithWebIdentityInput, _ ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
				return &sts.AssumeRoleWithWebIdentityOutput{
					Credentials: &types.Credentials{
						AccessKeyId:     aws.String(newAwsAccessKey),
						SecretAccessKey: aws.String(newAwsSecretKey),
						SessionToken:    aws.String(newAwsSessionToken),
						Expiration:      aws.Time(startTime.Add(1 * time.Hour)),
					},
				}, nil
			},
		}
		rotator := AWSOIDCRotator{
			client:                         fakeClient,
			stsClient:                      mockSTS,
			backendSecurityPolicyNamespace: policyNameSpace,
			backendSecurityPolicyName:      policyName,
			oidc:                           &oidc,
			region:                         awsRegion,
			roleArn:                        awsRoleArn,
		}
		ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
		expiration, err := rotator.Rotate(ctx)
		require.NoError(t, err)
		require.NotNil(t, expiration)
		require.WithinRange(t, expiration, startTime, startTime.Add(1*time.Hour))
		verifyAwsCredentialsSecret(t, fakeClient, policyNameSpace, policyName, newAwsAccessKey, newAwsSecretKey, newAwsSessionToken, awsProfileName, awsRegion)
	})

	t.Run("rotation - update when aws credential secret exists", func(t *testing.T) {
		startTime := time.Now()
		scheme := runtime.NewScheme()
		scheme.AddKnownTypes(corev1.SchemeGroupVersion,
			&corev1.Secret{},
		)
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		createOidcClientSecret(t, fakeClient, testClientSecret)

		createTestAwsSecret(t, fakeClient, policyName, oldAwsAccessKey, oldAwsSecretKey, oldAwsSessionToken, awsProfileName, awsRegion)
		verifyAwsCredentialsSecret(t, fakeClient, policyNameSpace, policyName, oldAwsAccessKey, oldAwsSecretKey, oldAwsSessionToken, awsProfileName, awsRegion)

		var mockSTS STSClient = &mockStsOperations{
			assumeRoleWithWebIdentityFunc: func(_ context.Context, _ *sts.AssumeRoleWithWebIdentityInput, _ ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
				return &sts.AssumeRoleWithWebIdentityOutput{
					Credentials: &types.Credentials{
						AccessKeyId:     aws.String(newAwsAccessKey),
						SecretAccessKey: aws.String(newAwsSecretKey),
						SessionToken:    aws.String(newAwsSessionToken),
						Expiration:      aws.Time(startTime.Add(1 * time.Hour)),
					},
				}, nil
			},
		}
		rotator := AWSOIDCRotator{
			client:                         fakeClient,
			stsClient:                      mockSTS,
			backendSecurityPolicyNamespace: policyNameSpace,
			backendSecurityPolicyName:      policyName,
			oidc:                           &oidc,
			region:                         awsRegion,
			roleArn:                        awsRoleArn,
		}

		ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
		expiration, err := rotator.Rotate(ctx)
		require.NoError(t, err)
		require.NotNil(t, expiration)
		require.WithinRange(t, expiration, startTime, startTime.Add(1*time.Hour))
		verifyAwsCredentialsSecret(t, fakeClient, policyNameSpace, policyName, newAwsAccessKey, newAwsSecretKey, newAwsSessionToken, awsProfileName, awsRegion)
	})
}

func TestAWS_GetPreRotationTime(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion,
		&corev1.Secret{},
	)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	awsOidcRotator := AWSOIDCRotator{
		client:                         fakeClient,
		backendSecurityPolicyNamespace: policyNameSpace,
		backendSecurityPolicyName:      policyName,
	}

	preRotateTime, err := awsOidcRotator.GetPreRotationTime(t.Context())
	require.True(t, apierrors.IsNotFound(err))
	require.Equal(t, 0, preRotateTime.Minute())

	createTestAwsSecret(t, fakeClient, policyName, oldAwsAccessKey, oldAwsSecretKey, oldAwsSessionToken, awsProfileName, awsRegion)
	require.Equal(t, 0, preRotateTime.Minute())

	secret, err := LookupSecret(t.Context(), fakeClient, policyNameSpace, GetBSPSecretName(policyName))
	require.NoError(t, err)

	expiredTime := time.Now().Add(-1 * time.Hour)
	updateExpirationSecretAnnotation(secret, expiredTime)
	require.NoError(t, fakeClient.Update(t.Context(), secret))
	preRotateTime, _ = awsOidcRotator.GetPreRotationTime(t.Context())
	require.Equal(t, expiredTime.Format(time.RFC3339), preRotateTime.Format(time.RFC3339))
}

func TestAWS_IsExpired(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion,
		&corev1.Secret{},
	)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	awsOidcRotator := AWSOIDCRotator{
		client:                         fakeClient,
		backendSecurityPolicyNamespace: policyNameSpace,
		backendSecurityPolicyName:      policyName,
	}
	preRotateTime, _ := awsOidcRotator.GetPreRotationTime(t.Context())
	require.True(t, awsOidcRotator.IsExpired(preRotateTime))

	createTestAwsSecret(t, fakeClient, policyName, oldAwsAccessKey, oldAwsSecretKey, oldAwsSessionToken, awsProfileName, awsRegion)
	require.Equal(t, 0, preRotateTime.Minute())

	secret, err := LookupSecret(t.Context(), fakeClient, policyNameSpace, GetBSPSecretName(policyName))
	require.NoError(t, err)

	expiredTime := time.Now().Add(-1 * time.Hour)
	updateExpirationSecretAnnotation(secret, expiredTime)
	require.NoError(t, fakeClient.Update(t.Context(), secret))
	preRotateTime, _ = awsOidcRotator.GetPreRotationTime(t.Context())
	require.True(t, awsOidcRotator.IsExpired(preRotateTime))

	hourFromNowTime := time.Now().Add(1 * time.Hour)
	updateExpirationSecretAnnotation(secret, hourFromNowTime)
	require.NoError(t, fakeClient.Update(t.Context(), secret))
	preRotateTime, _ = awsOidcRotator.GetPreRotationTime(t.Context())
	require.False(t, awsOidcRotator.IsExpired(preRotateTime))
}
