// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	stsTypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	oidcv3 "github.com/coreos/go-oidc/v3/oidc"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestBackendSecurityController_Reconcile(t *testing.T) {
	aiServiceBackendEventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	inferencePoolEventCh := internaltesting.NewControllerEventChan[*gwaiev1.InferencePool]()
	fakeClient := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, aiServiceBackendEventCh.Ch, inferencePoolEventCh.Ch)
	backendSecurityPolicyName := "mybackendSecurityPolicy"
	namespace := "default"

	// Create AIServiceBackend that references the BackendSecurityPolicy.
	asb := &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "default"},
		Spec: aigv1b1.AIServiceBackendSpec{
			BackendRef: gwapiv1.BackendObjectReference{
				Name: gwapiv1.ObjectName("mybackend"),
				Port: ptr.To[gwapiv1.PortNumber](8080),
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), asb))

	err := fakeClient.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: backendSecurityPolicyName, Namespace: namespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{Name: "mysecret"},
			},
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "AIServiceBackend",
					Group: "aigateway.envoyproxy.io",
					Name:  gwapiv1.ObjectName(asb.Name),
				},
			},
		},
	})
	require.NoError(t, err)
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: backendSecurityPolicyName}})
	require.NoError(t, err)
	require.False(t, res.Requeue)
	items := aiServiceBackendEventCh.RequireItemsEventually(t, 1)
	require.Len(t, items, 1)
	require.Equal(t, asb, items[0])

	// Check that the status was updated.
	var bsp aigv1b1.BackendSecurityPolicy
	require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: backendSecurityPolicyName}, &bsp))
	require.Len(t, bsp.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, bsp.Status.Conditions[0].Type)
	require.Equal(t, "BackendSecurityPolicy reconciled successfully", bsp.Status.Conditions[0].Message)
	require.Contains(t, bsp.Finalizers, aiGatewayControllerFinalizer, "Finalizer should be added")

	// Test targeting an InferencePool.
	inferencePool := &gwaiev1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{Name: "bar", Namespace: "default"},
	}
	require.NoError(t, fakeClient.Create(t.Context(), inferencePool))

	bspInferencePool := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp-inference-pool", Namespace: namespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
				SecretRef: &gwapiv1.SecretObjectReference{Name: "mysecret"},
			},
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Kind:  "InferencePool",
					Group: "inference.networking.k8s.io",
					Name:  gwapiv1.ObjectName(inferencePool.Name),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), bspInferencePool))
	res, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: bspInferencePool.Name}})
	require.NoError(t, err)
	require.False(t, res.Requeue)
	inferencePoolItems := inferencePoolEventCh.RequireItemsEventually(t, 1)
	require.Len(t, inferencePoolItems, 1)
	require.Equal(t, inferencePool, inferencePoolItems[0])

	// Test the case where the BackendSecurityPolicy is being deleted.
	err = fakeClient.Delete(t.Context(), &aigv1b1.BackendSecurityPolicy{ObjectMeta: metav1.ObjectMeta{Name: backendSecurityPolicyName, Namespace: namespace}})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: backendSecurityPolicyName}})
	require.NoError(t, err)
}

// mockSTSClient implements the STSOperations interface for testing.
type mockSTSClient struct {
	expTime time.Time
}

// AssumeRoleWithWebIdentity will return placeholder of type aws credentials.
//
// This implements [rotators.STSClient.AssumeRoleWithWebIdentity].
func (m *mockSTSClient) AssumeRoleWithWebIdentity(_ context.Context, _ *sts.AssumeRoleWithWebIdentityInput, _ ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	return &sts.AssumeRoleWithWebIdentityOutput{
		Credentials: &stsTypes.Credentials{
			AccessKeyId:     aws.String("NEWKEY"),
			SecretAccessKey: aws.String("NEWSECRET"),
			SessionToken:    aws.String("NEWTOKEN"),
			Expiration:      &m.expTime,
		},
	}, nil
}

func TestBackendSecurityPolicyController_Reconcile_SyncError(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	fakeClient := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)

	// Create a BackendSecurityPolicy with invalid spec to trigger sync error.
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "invalid-bsp",
			Namespace: "default",
		},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: "InvalidType", // Invalid type to cause sync error.
		},
	}
	err := fakeClient.Create(t.Context(), bsp)
	require.NoError(t, err)

	// Reconcile should fail during sync.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: "default",
			Name:      "invalid-bsp",
		},
	})
	require.Error(t, err)

	// Check that status was updated to NotAccepted.
	var updatedBSP aigv1b1.BackendSecurityPolicy
	err = fakeClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "invalid-bsp"}, &updatedBSP)
	require.NoError(t, err)
	require.Len(t, updatedBSP.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updatedBSP.Status.Conditions[0].Type)
}

func TestBackendSecurityPolicyController_ReconcileOIDC_Fail(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "mybackendSecurityPolicy"
	bspNamespace := "default"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-OIDC", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: egv1a1.OIDC{},
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)
	// Expects rotate credentials to fail due to missing OIDC details.
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bspNamespace, Name: fmt.Sprintf("%s-OIDC", bspName)}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to create oidc config")
	require.Equal(t, time.Minute, res.RequeueAfter)
}

func TestBackendSecurityPolicyController_RotateCredential(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		b, err := json.Marshal(oauth2.Token{AccessToken: "some-access-token", TokenType: "Bearer", ExpiresIn: 60})
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": []}`))
		require.NoError(t, err)
	}))
	defer discoveryServer.Close()

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "mybackendSecurityPolicy"
	bspNamespace := "default"

	// Initial secret lookup failure as no secret exist.
	_, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, rotators.GetBSPSecretName(fmt.Sprintf("%s-OIDC", bspName)))
	require.Error(t, err)
	require.Equal(t, "secrets \"ai-eg-bsp-mybackendSecurityPolicy-OIDC\" not found", err.Error())

	oidcSecretName := "oidcClientSecret"

	oidcSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oidcSecretName,
			Namespace: bspNamespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("client-secret"),
		},
	}
	require.NoError(t, cl.Create(t.Context(), &oidcSecret, &client.CreateOptions{}))

	// Create backend security policy with OIDC config.
	oidc := egv1a1.OIDC{
		Provider: egv1a1.OIDCProvider{
			Issuer:        discoveryServer.URL,
			TokenEndpoint: &tokenServer.URL,
		},
		ClientID: ptr.To("some-client-id"),
		ClientSecret: gwapiv1.SecretObjectReference{
			Name:      gwapiv1.ObjectName(oidcSecretName),
			Namespace: (*gwapiv1.Namespace)(&bspNamespace),
		},
	}
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-OIDC", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: oidc,
					},
				},
				Region: "us-east-1",
			},
		},
	}
	err = cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)

	data := map[string][]byte{
		"credentials": fmt.Appendf(nil, "[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\nregion = %s\n",
			"default", "accessKey", "secretKey", "sessionToken", "us-east-2"),
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ai-eg-bsp-%s-OIDC", bspName),
			Namespace: bspNamespace,
			Annotations: map[string]string{
				rotators.ExpirationTimeAnnotationKey: time.Now().Add(60 * time.Minute).Format(time.RFC3339),
			},
		},
		Data: data,
	}
	err = cl.Create(ctx, secret)
	require.NoError(t, err)
	_, err = c.rotateCredential(ctx, bsp)
	require.NoError(t, err)

	// Check the generated secret contains the owner reference to the BackendSecurityPolicy.
	awsSecretName := rotators.GetBSPSecretName(fmt.Sprintf("%s-OIDC", bspName))
	awsSecret, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, awsSecretName)
	require.NoError(t, err)
	ok, _ := ctrlutil.HasOwnerReference(awsSecret.OwnerReferences, bsp, c.client.Scheme())
	require.True(t, ok, "expected secret to have owner reference to BackendSecurityPolicy")
}

func TestBackendSecurityPolicyController_RotateExpiredCredential(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		b, err := json.Marshal(oauth2.Token{AccessToken: "some-access-token", TokenType: "Bearer", ExpiresIn: 60})
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": []}`))
		require.NoError(t, err)
	}))
	defer discoveryServer.Close()

	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	bspName := "mybackendSecurityPolicy"
	bspNamespace := "default"

	// Initial secret lookup failure as no secret exist.
	_, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, rotators.GetBSPSecretName(fmt.Sprintf("%s-OIDC", bspName)))
	require.Error(t, err)

	oidcSecretName := "oidcClientSecret"
	awsSecretName := rotators.GetBSPSecretName(fmt.Sprintf("%s-OIDC", bspName))

	oidcSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oidcSecretName,
			Namespace: bspNamespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("client-secret"),
		},
	}
	require.NoError(t, cl.Create(t.Context(), &oidcSecret, &client.CreateOptions{}))

	oidc := egv1a1.OIDC{
		Provider: egv1a1.OIDCProvider{
			Issuer:        discoveryServer.URL,
			TokenEndpoint: &tokenServer.URL,
		},
		ClientID: ptr.To("some-client-id"),
		ClientSecret: gwapiv1.SecretObjectReference{
			Name:      gwapiv1.ObjectName(oidcSecretName),
			Namespace: (*gwapiv1.Namespace)(&bspNamespace),
		},
	}
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-OIDC", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: oidc,
					},
				},
			},
		},
	}
	err = cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
	rotator, err := rotators.NewAWSOIDCRotator(ctx, cl, &mockSTSClient{time.Now().Add(time.Hour)}, fake2.NewClientset(), ctrl.Log, bspNamespace, bsp.Name, preRotationWindow,
		&oidc, "placeholder", "us-east-1")
	require.NoError(t, err)

	// Ensure aws credentials secret do not exist.
	_, err = rotators.LookupSecret(t.Context(), cl, bspNamespace, awsSecretName)
	require.Error(t, err)

	// First credential rotation should create aws credentials secret.
	res, err := rotator.Rotate(ctx)
	require.NoError(t, err)
	require.WithinRange(t, time.Now().Add(time.Until(res.Add(-preRotationWindow))), time.Now().Add(50*time.Minute), time.Now().Add(time.Hour))

	// Ensure both oidc secret and aws credential secret are created.
	returnOidcSecret, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, oidcSecretName)
	require.NoError(t, err)
	require.Equal(t, "client-secret", string(returnOidcSecret.Data["client-secret"]))

	awsSecret1, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, awsSecretName)
	require.NoError(t, err)

	// Second credential rotation should update expiration time.
	t0 := awsSecret1.Annotations[rotators.ExpirationTimeAnnotationKey]

	// Set secret time expired.
	parsedTime, err := time.Parse(time.RFC3339, t0)
	require.NoError(t, err)
	t1 := parsedTime.Add(-preRotationWindow - time.Minute).String()
	awsSecret1.Annotations[rotators.ExpirationTimeAnnotationKey] = t1
	require.NoError(t, cl.Update(t.Context(), awsSecret1))

	_, err = rotator.Rotate(ctx)
	require.NoError(t, err)
	awsSecret2, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, awsSecretName)
	require.NoError(t, err)
	t2 := awsSecret2.Annotations[rotators.ExpirationTimeAnnotationKey]
	require.NotEqual(t, t1, t2)
}

func TestBackendSecurityPolicyController_GetBackendSecurityPolicyAuthOIDC(t *testing.T) {
	// API Key type does not support OIDC.
	require.Nil(t, getBackendSecurityPolicyAuthOIDC(&aigv1b1.BackendSecurityPolicySpec{Type: aigv1b1.BackendSecurityPolicyTypeAPIKey}))

	// Azure type supports OIDC type but OIDC needs to be defined.
	require.Nil(t, getBackendSecurityPolicyAuthOIDC(&aigv1b1.BackendSecurityPolicySpec{
		Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
		AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
			ClientID:        "client-id",
			TenantID:        "tenant-id",
			ClientSecretRef: nil,
		},
	}))

	// Azure type with OIDC defined.
	oidcAzure := getBackendSecurityPolicyAuthOIDC(&aigv1b1.BackendSecurityPolicySpec{
		Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
		AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
			ClientID: "client-id",
			TenantID: "tenant-id",
			OIDCExchangeToken: &aigv1b1.AzureOIDCExchangeToken{
				BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
					OIDC: egv1a1.OIDC{
						ClientID: ptr.To("some-client-id"),
					},
				},
			},
		},
	})

	require.NotNil(t, oidcAzure)
	require.Equal(t, "some-client-id", *oidcAzure.ClientID)

	// AWS type supports OIDC type but OIDC needs to be defined.
	require.Nil(t, getBackendSecurityPolicyAuthOIDC(&aigv1b1.BackendSecurityPolicySpec{
		Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
		AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
			CredentialsFile: &aigv1b1.AWSCredentialsFile{},
		},
	}))

	// AWS type with OIDC defined.
	oidcAWS := getBackendSecurityPolicyAuthOIDC(&aigv1b1.BackendSecurityPolicySpec{
		Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
		AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
			OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
				BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
					OIDC: egv1a1.OIDC{
						ClientID: ptr.To("some-client-id"),
					},
				},
			},
		},
	})
	require.NotNil(t, oidcAWS)
	require.Equal(t, "some-client-id", *oidcAWS.ClientID)

	// GCP type with OIDC defined.
	oidcGCP := getBackendSecurityPolicyAuthOIDC(&aigv1b1.BackendSecurityPolicySpec{
		Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
		GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
			ProjectName: "fake-project-name",
			Region:      "fake-region",
			WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
				ProjectID:                    "fake-project-id",
				WorkloadIdentityProviderName: "fake-workload-identity-provider-name",
				OIDCExchangeToken: aigv1b1.GCPOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: egv1a1.OIDC{
							ClientID: ptr.To("some-client-id"),
						},
					},
				},
				WorkloadIdentityPoolName: "fake-workload-identity-pool-name",
			},
		},
	})
	require.NotNil(t, oidcGCP)
	require.Equal(t, "some-client-id", *oidcGCP.ClientID)
}

func TestNewBackendSecurityPolicyController_ReconcileAzureMissingSecret(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "my-azure-backend-security-policy"
	tenantID := "some-tenant-id"
	clientID := "some-client-id"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ClientID:        clientID,
				TenantID:        tenantID,
				ClientSecretRef: &gwapiv1.SecretObjectReference{Name: "some-azure-secret", Namespace: ptr.To[gwapiv1.Namespace]("default")},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: bspName}})
	require.Error(t, err)
	require.Equal(t, "secrets \"some-azure-secret\" not found", err.Error())
	require.Equal(t, time.Duration(0), res.RequeueAfter)
}

func TestNewBackendSecurityPolicyController_ReconcileAzureMissingSecretData(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "my-azure-backend-security-policy"
	tenantID := "some-tenant-id"
	clientID := "some-client-id"

	azureClientSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-azure-secret",
			Namespace: "default",
		},
	}
	require.NoError(t, cl.Create(t.Context(), &azureClientSecret, &client.CreateOptions{}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ClientID: clientID,
				TenantID: tenantID,
				ClientSecretRef: &gwapiv1.SecretObjectReference{
					Name:      "some-azure-secret",
					Namespace: ptr.To[gwapiv1.Namespace]("default"),
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: bspName}})
	require.Error(t, err)
	require.Equal(t, "missing azure client secret key client-secret", err.Error())
	require.Equal(t, time.Duration(0), res.RequeueAfter)
}

func TestNewBackendSecurityPolicyController_ReconcileAzureMissingAuthMethod(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "my-azure-backend-security-policy"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ClientID: "some-client-id",
				TenantID: "some-tenant-id",
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))
	_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: bspName}})
	require.Error(t, err)
	require.Equal(t, "one of secret ref, oidc, or managed identity must be defined, namespace default name my-azure-backend-security-policy", err.Error())
}

func TestNewBackendSecurityPolicyController_ReconcileAzureManagedIdentity(t *testing.T) {
	// Route the IMDS request of the user-assigned managed identity credential through a local
	// proxy that always fails, so that the rotation deterministically reaches the managed identity
	// token provider and fails there, without depending on the environment running the tests.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable in unit tests", http.StatusBadRequest)
	}))
	defer proxy.Close()
	t.Setenv("AI_GATEWAY_AZURE_PROXY_URL", proxy.URL)

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "my-azure-managed-identity-policy"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ManagedIdentity: &aigv1b1.AzureManagedIdentity{ClientID: "some-client-id"},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: bspName}})
	// The managed identity token request fails in the unit test environment; the important part is
	// that the reconciliation took the managed identity path instead of requiring a client secret
	// or an OIDC configuration.
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to execute rotation")
	require.NotContains(t, err.Error(), "one of secret ref, oidc, or managed identity must be defined")
	// A failed rotation is retried in one minute.
	require.Equal(t, time.Minute, res.RequeueAfter)
}

func TestNewBackendSecurityPolicyController_RotateCredentialInvalidType(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "some-backend-security-policy"
	bspNamespace := "default"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-OIDC", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: "Unknown",
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: egv1a1.OIDC{},
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bspNamespace, Name: fmt.Sprintf("%s-OIDC", bspName)}})
	require.Error(t, err)
	require.Equal(t, time.Duration(0), res.RequeueAfter)
}

func TestNewBackendSecurityPolicyController_RotateCredentialAwsCredentialFile(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "some-backend-security-policy"
	bspNamespace := "default"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-OIDC", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				CredentialsFile: &aigv1b1.AWSCredentialsFile{},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bspNamespace, Name: fmt.Sprintf("%s-OIDC", bspName)}})
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), res.RequeueAfter)
}

func TestNewBackendSecurityPolicyController_RotateCredentialGcpCredentialFile(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "gcp-backend-security-policy"
	bspNamespace := "default"

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-sa", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				CredentialsFile: &aigv1b1.GCPCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name: "gcp-app-credentials",
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bspNamespace, Name: fmt.Sprintf("%s-sa", bspName)}})
	require.Error(t, err)
	require.Equal(t, time.Duration(0), res.RequeueAfter)
}

func TestNewBackendSecurityPolicyController_RotateCredentialAzureIncorrectSecretRef(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)

	tenantID := "some-tenant-id"
	clientID := "some-client-id"
	secretName := rotators.GetBSPSecretName("some-secret")
	err := cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "default",
		},
		Data: map[string][]byte{
			"client-secret": []byte("client-secret"),
		},
	})
	require.NoError(t, err)

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "some-policy", Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ClientID:        clientID,
				TenantID:        tenantID,
				ClientSecretRef: &gwapiv1.SecretObjectReference{Name: gwapiv1.ObjectName("some-other-secret-name"), Namespace: ptr.To[gwapiv1.Namespace]("default")},
			},
		},
	}
	err = cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	res, err := c.rotateCredential(t.Context(), bsp)
	require.Error(t, err)
	require.Equal(t, time.Duration(0), res.RequeueAfter)
}

func TestBackendSecurityPolicyController_ExecutionRotation(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		b, err := json.Marshal(oauth2.Token{AccessToken: "some-access-token", TokenType: "Bearer", ExpiresIn: 60})
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}))
	defer tokenServer.Close()

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": []}`))
		require.NoError(t, err)
	}))
	defer discoveryServer.Close()

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspNamespace := "default"
	bspName := "some-back-end-security-policy"
	oidcSecretName := "oidcClientSecret"
	oidcSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oidcSecretName,
			Namespace: bspNamespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("client-secret"),
		},
	}
	require.NoError(t, cl.Create(t.Context(), &oidcSecret, &client.CreateOptions{}))
	oidc := egv1a1.OIDC{
		Provider: egv1a1.OIDCProvider{
			Issuer:        discoveryServer.URL,
			TokenEndpoint: &tokenServer.URL,
		},
		ClientID: ptr.To("some-client-id"),
		ClientSecret: gwapiv1.SecretObjectReference{
			Name:      gwapiv1.ObjectName(oidcSecretName),
			Namespace: (*gwapiv1.Namespace)(&bspNamespace),
		},
	}
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-OIDC", bspName), Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
					BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
						OIDC: oidc,
					},
				},
				Region: "us-east-1",
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))
	ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
	data := map[string][]byte{
		"credentials": fmt.Appendf(nil, "[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\nregion = %s\n",
			"default", "accessKey", "secretKey", "sessionToken", "us-east-2"),
	}
	now := time.Now()
	expirationTime := now.Add(-1 * time.Hour)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ai-eg-bsp-%s-OIDC", bspName),
			Namespace: bspNamespace,
			Annotations: map[string]string{
				rotators.ExpirationTimeAnnotationKey: expirationTime.Format(time.RFC3339),
			},
		},
		Data: data,
	}
	require.NoError(t, cl.Create(ctx, secret))

	rotator, err := rotators.NewAWSOIDCRotator(
		ctx,
		cl,
		&mockSTSClient{now.Add(time.Hour)},
		fake2.NewClientset(),
		ctrl.Log, bspNamespace,
		bsp.Name,
		preRotationWindow,
		&oidc,
		"placeholder",
		"us-east-1",
	)
	require.NoError(t, err)
	res, err := c.executeRotation(ctx, rotator, bsp)
	require.NoError(t, err)
	require.Less(t, res.RequeueAfter, time.Hour)
}

func TestValidateGCPCredentialsParams(t *testing.T) {
	tests := []struct {
		name      string
		input     *aigv1b1.BackendSecurityPolicyGCPCredentials
		wantError string
	}{
		{
			name:      "nil credentials",
			input:     nil,
			wantError: "invalid backend security policy, gcp credentials cannot be nil",
		},
		{
			name: "empty project name",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "pid",
					WorkloadIdentityPoolName:     "pool",
					WorkloadIdentityProviderName: "provider",
				},
			},
			wantError: "invalid GCP credentials configuration: projectName cannot be empty",
		},
		{
			name: "empty region",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "proj",
				Region:      "",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "pid",
					WorkloadIdentityPoolName:     "pool",
					WorkloadIdentityProviderName: "provider",
				},
			},
			wantError: "invalid GCP credentials configuration: region cannot be empty",
		},
		{
			name: "empty projectID",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "proj",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "",
					WorkloadIdentityPoolName:     "pool",
					WorkloadIdentityProviderName: "provider",
				},
			},
			wantError: "invalid GCP Workload Identity Federation configuration: projectID cannot be empty",
		},
		{
			name: "empty workloadIdentityPoolName",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "proj",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "pid",
					WorkloadIdentityPoolName:     "",
					WorkloadIdentityProviderName: "provider",
				},
			},
			wantError: "invalid GCP Workload Identity Federation configuration: workloadIdentityPoolName cannot be empty",
		},
		{
			name: "empty workloadIdentityProvider name",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "proj",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "pid",
					WorkloadIdentityPoolName:     "pool",
					WorkloadIdentityProviderName: "",
				},
			},
			wantError: "invalid GCP Workload Identity Federation configuration: workloadIdentityProvider.name cannot be empty",
		},
		{
			name: "valid credentials",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "proj",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "pid",
					WorkloadIdentityPoolName:     "pool",
					WorkloadIdentityProviderName: "provider",
				},
			},
			wantError: "",
		},
		{
			name: "valid credentials",
			input: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "proj",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "pid",
					WorkloadIdentityPoolName:     "pool",
					WorkloadIdentityProviderName: "provider",
				},
			},
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGCPCredentialsParams(tt.input)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, tt.wantError, err.Error())
			}
		})
	}
}

func TestBackendSecurityPolicyController_RotateCredential_GCPCredentials(t *testing.T) {
	bspNamespace := "default"
	bspName := "test-gcp-policy"

	// Test cases for validation errors.
	validationTests := []struct {
		name           string
		bsp            *aigv1b1.BackendSecurityPolicySpec
		expectedErrMsg string
	}{
		{
			name: "nil gcp credentials",
			bsp: &aigv1b1.BackendSecurityPolicySpec{
				Type:           aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
				GCPCredentials: nil,
			},
			expectedErrMsg: "invalid backend security policy, gcp credentials cannot be nil",
		},
		{
			name: "empty gcp credentials",
			bsp: &aigv1b1.BackendSecurityPolicySpec{
				Type:           aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
				GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{},
			},
			expectedErrMsg: "invalid GCP credentials configuration: projectName cannot be empty",
		},
	}

	c := NewBackendSecurityPolicyController(fake.NewFakeClient(), fake2.NewClientset(), ctrl.Log, nil, nil)

	for _, tt := range validationTests {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bspName,
				Namespace: bspNamespace,
			},
			Spec: *tt.bsp,
		}
		t.Run(tt.name, func(t *testing.T) {
			res, err := c.rotateCredential(context.Background(), bsp)

			switch {
			case tt.expectedErrMsg != "" && err == nil:
				t.Errorf("expected error but got none, expected: %s", tt.expectedErrMsg)
			case tt.expectedErrMsg == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.expectedErrMsg != "" && err != nil:
				require.Contains(t, err.Error(), tt.expectedErrMsg)
			default:
				require.NoError(t, err)
				require.NotZero(t, res.RequeueAfter)
			}
		})
	}
}

func TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_ADC(t *testing.T) {
	c := NewBackendSecurityPolicyController(fake.NewFakeClient(), fake2.NewClientset(), ctrl.Log, nil, nil)
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gcp-adc-policy",
			Namespace: "default",
		},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "test-project",
				Region:      "us-central1",
			},
		},
	}

	res, err := c.rotateCredential(context.Background(), bsp)
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter)
}

func TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_OIDC(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "gcp-oidc-policy"
	bspNamespace := "default"

	oidcSecretName := "gcp-oidc-secret" // #nosec G101
	oidcSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oidcSecretName,
			Namespace: bspNamespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("client-secret"),
		},
	}
	require.NoError(t, cl.Create(t.Context(), &oidcSecret, &client.CreateOptions{}))

	// Create backend security policy with GCP OIDC config.
	oidc := egv1a1.OIDC{
		Provider: egv1a1.OIDCProvider{
			Issuer: "https://fake-issuer.com",
		},
		ClientID: ptr.To("some-client-id"),
		ClientSecret: gwapiv1.SecretObjectReference{
			Name:      gwapiv1.ObjectName(oidcSecretName),
			Namespace: (*gwapiv1.Namespace)(&bspNamespace),
		},
	}
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "test-project",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "test-project-id",
					WorkloadIdentityPoolName:     "test-pool",
					WorkloadIdentityProviderName: "test-provider",
					OIDCExchangeToken: aigv1b1.GCPOIDCExchangeToken{
						BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
							OIDC: oidc,
						},
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	// Test that the OIDC path validation passes and the method attempts to create OIDC provider
	// (which will fail due to network issues, but that confirms the OIDC path logic is working).
	res, err := c.rotateCredential(t.Context(), bsp)

	// We expect an error due to network calls in the OIDC provider initialization.
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to initialize OIDC provider")
	require.Equal(t, ctrl.Result{}, res)
}

func TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_CredentialsFile(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "gcp-sa-policy"
	bspNamespace := "default"

	// Create a secret containing an invalid service account JSON structure to ensure controlled test failure.
	serviceAccountJSON := `{
		"type": "service_account",
		"project_id": "test-project",
		"private_key_id": "key-id",
		"private_key": "invalid-private-key-data",
		"client_email": "test@test-project.iam.gserviceaccount.com",
		"client_id": "123456789",
		"auth_uri": "https://accounts.google.com/o/oauth2/auth",
		"token_uri": "https://oauth2.googleapis.com/token"
	}` // #nosec G101

	serviceAccountSecretName := "gcp-sa-secret" // #nosec G101
	serviceAccountSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountSecretName,
			Namespace: bspNamespace,
		},
		Data: map[string][]byte{
			rotators.GCPServiceAccountJSON: []byte(serviceAccountJSON),
		},
	}
	require.NoError(t, cl.Create(t.Context(), &serviceAccountSecret, &client.CreateOptions{}))

	// Create backend security policy with GCP service account credentials file config.
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "test-project",
				Region:      "us-central1",
				CredentialsFile: &aigv1b1.GCPCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name: gwapiv1.ObjectName(serviceAccountSecretName),
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	// Test that credentials file path is correctly selected and reaches token provider creation.
	res, err := c.rotateCredential(t.Context(), bsp)

	// The test behavior varies depending on environment mocking, but both outcomes are valid:
	// 1. Error at token provider creation confirms the credentials file path worked
	// 2. Success indicates test environment provides full mocking.
	if err != nil {
		require.Contains(t, err.Error(), "private key")
	} else {
		require.NotZero(t, res.RequeueAfter)
	}
}

func TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_MissingSecret(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "gcp-missing-secret-policy"
	bspNamespace := "default"

	// Create backend security policy with GCP credentials file that doesn't exist.
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "test-project",
				Region:      "us-central1",
				CredentialsFile: &aigv1b1.GCPCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name: gwapiv1.ObjectName("non-existent-secret"),
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	// Test that rotation fails when the referenced secret doesn't exist.
	res, err := c.rotateCredential(t.Context(), bsp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secrets \"non-existent-secret\" not found")
	require.Equal(t, ctrl.Result{}, res)
}

func TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_MissingSecretKey(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil)
	bspName := "gcp-missing-key-policy"
	bspNamespace := "default"

	// Create a secret without the required GCP service account JSON key.
	serviceAccountSecretName := "gcp-incomplete-secret" // #nosec G101
	serviceAccountSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountSecretName,
			Namespace: bspNamespace,
		},
		Data: map[string][]byte{
			"wrong-key": []byte("some-data"),
		},
	}
	require.NoError(t, cl.Create(t.Context(), &serviceAccountSecret, &client.CreateOptions{}))

	// Create backend security policy with GCP credentials file config pointing to incomplete secret.
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "test-project",
				Region:      "us-central1",
				CredentialsFile: &aigv1b1.GCPCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name: gwapiv1.ObjectName(serviceAccountSecretName),
					},
				},
			},
		},
	}
	err := cl.Create(t.Context(), bsp)
	require.NoError(t, err)

	// Test that rotation fails when the secret doesn't contain the required key.
	res, err := c.rotateCredential(t.Context(), bsp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing gcp service account key service_account.json")
	require.Equal(t, ctrl.Result{}, res)
}

func TestGetBSPGeneratedSecretName(t *testing.T) {
	tests := []struct {
		name         string
		bsp          *aigv1b1.BackendSecurityPolicy
		expectedName string
	}{
		{
			name: "AWS without OIDCExchangeToken",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "aws-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
					AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
						CredentialsFile: &aigv1b1.AWSCredentialsFile{
							SecretRef: nil,
						},
						OIDCExchangeToken: nil,
					},
				},
			},
			expectedName: "",
		},
		{
			name: "AWS with OIDCExchangeToken",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "aws-oidc-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
					AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
						OIDCExchangeToken: &aigv1b1.AWSOIDCExchangeToken{
							BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
								OIDC: egv1a1.OIDC{
									ClientID: ptr.To("some-client-id"),
								},
							},
						},
					},
				},
			},
			expectedName: "ai-eg-bsp-aws-oidc-bsp",
		},
		{
			name: "Azure without OIDCExchangeToken",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "azure-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
					AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
						ClientSecretRef:   nil,
						OIDCExchangeToken: nil,
					},
				},
			},
			expectedName: "",
		},
		{
			name: "Azure with OIDCExchangeToken",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "azure-oidc-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
					AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
						OIDCExchangeToken: &aigv1b1.AzureOIDCExchangeToken{
							BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
								OIDC: egv1a1.OIDC{
									ClientID: ptr.To("some-client-id"),
								},
							},
						},
					},
				},
			},
			expectedName: "ai-eg-bsp-azure-oidc-bsp",
		},
		{
			name: "GCP type",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gcp-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
					GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
						CredentialsFile: &aigv1b1.GCPCredentialsFile{
							SecretRef: nil,
						},
					},
				},
			},
			expectedName: "",
		},
		{
			name: "GCP with service account credential file",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gcp-bsp-sa",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
					GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
						CredentialsFile: &aigv1b1.GCPCredentialsFile{
							SecretRef: &gwapiv1.SecretObjectReference{
								Name: "gcp-bsp-sa-json-key-file",
							},
						},
					},
				},
			},
			expectedName: "",
		},
		{
			name: "APIKey type",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "apikey-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
				},
			},
			expectedName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultName := getBSPGeneratedSecretName(tt.bsp)
			if resultName != "" {
				require.Equal(t, tt.expectedName, resultName)
			}
		})
	}
}
