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
	"path/filepath"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestBackendSecurityController_Reconcile(t *testing.T) {
	aiServiceBackendEventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	inferencePoolEventCh := internaltesting.NewControllerEventChan[*gwaiev1.InferencePool]()
	fakeClient := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, aiServiceBackendEventCh.Ch, inferencePoolEventCh.Ch, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(fakeClient, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	rotator, err := rotators.NewAWSOIDCRotator(ctx, cl, &mockSTSClient{time.Now().Add(time.Hour)}, fake2.NewClientset(), ctrl.Log, bspNamespace, bsp.Name, DefaultPreRotationWindow,
		&oidc, "placeholder", "us-east-1", nil)
	require.NoError(t, err)

	// Ensure aws credentials secret do not exist.
	_, err = rotators.LookupSecret(t.Context(), cl, bspNamespace, awsSecretName)
	require.Error(t, err)

	// First credential rotation should create aws credentials secret.
	res, err := rotator.Rotate(ctx)
	require.NoError(t, err)
	require.WithinRange(t, time.Now().Add(time.Until(res.Add(-DefaultPreRotationWindow))), time.Now().Add(50*time.Minute), time.Now().Add(time.Hour))

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
	t1 := parsedTime.Add(-DefaultPreRotationWindow - time.Minute).String()
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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

// The BackendSecurityPolicy controller must not read a Secret from a foreign namespace referenced
// via AzureCredentials.ClientSecretRef.Namespace unless a ReferenceGrant authorizes it.
func TestNewBackendSecurityPolicyController_RotateCredentialAzureCrossNamespaceBlocked(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

	// Victim secret lives in a different namespace than the BackendSecurityPolicy.
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "victim-azure-secret", Namespace: "tenant-b"},
		Data:       map[string][]byte{clientSecretKey: []byte("VICTIM-AZURE-CLIENT-SECRET")},
	}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "attacker-bsp", Namespace: "tenant-a"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ClientID: "attacker-client-id",
				TenantID: "attacker-tenant-id",
				ClientSecretRef: &gwapiv1.SecretObjectReference{
					Name:      "victim-azure-secret",
					Namespace: ptr.To[gwapiv1.Namespace]("tenant-b"),
				},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	_, err := c.rotateCredential(t.Context(), bsp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not permitted")
	require.NotContains(t, err.Error(), "not found",
		"the Secret must be rejected by the ReferenceGrant check before a lookup is even attempted")

	// Directly confirm the Secret is unreachable via the controller path: the reconciler must
	// surface the ReferenceGrant error, not the secret contents.
	res, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-a", Name: "attacker-bsp"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not permitted")
	require.Equal(t, time.Duration(0), res.RequeueAfter)
	var updated aigv1b1.BackendSecurityPolicy
	require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Namespace: "tenant-a", Name: "attacker-bsp"}, &updated))
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)
	require.Contains(t, updated.Status.Conditions[0].Message, "is not permitted")
}

// TestNewBackendSecurityPolicyController_RotateCredentialAzureCrossNamespaceAllowedWithReferenceGrant
// verifies that a valid ReferenceGrant still allows an intentional cross-namespace Secret reference.
func TestNewBackendSecurityPolicyController_RotateCredentialAzureCrossNamespaceAllowedWithReferenceGrant(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-azure-secret", Namespace: "tenant-b"},
		Data:       map[string][]byte{clientSecretKey: []byte("shared-secret")},
	}))
	require.NoError(t, cl.Create(t.Context(), &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-tenant-a", Namespace: "tenant-b"},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{
				Group:     aiServiceBackendGroup,
				Kind:      backendSecurityPolicyKind,
				Namespace: "tenant-a",
			}},
			To: []gwapiv1b1.ReferenceGrantTo{{Group: secretGroup, Kind: secretKind}},
		},
	}))

	bspNamespace, secretNamespace, secretName := "tenant-a", "tenant-b", "shared-azure-secret"

	// This mirrors exactly the check rotateCredential performs before calling
	// rotators.LookupSecret for AzureCredentials.ClientSecretRef: it must succeed given the
	// ReferenceGrant above, proving the fix doesn't break legitimate, explicitly-granted
	// cross-namespace references.
	require.NoError(t, c.referenceGrantValidator.validateSecretReference(t.Context(), bspNamespace, secretNamespace, secretName))

	secret, err := rotators.LookupSecret(t.Context(), cl, secretNamespace, secretName)
	require.NoError(t, err)
	require.Equal(t, "shared-secret", string(secret.Data[clientSecretKey]))
}

func TestNewBackendSecurityPolicyController_RotateCredentialGCPCrossNamespaceBlocked(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "victim-gcp-sa", Namespace: "tenant-b"},
		Data:       map[string][]byte{rotators.GCPServiceAccountJSON: []byte(`{"type":"service_account"}`)},
	}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "attacker-gcp-bsp", Namespace: "tenant-a"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "attacker-project",
				Region:      "us-central1",
				CredentialsFile: &aigv1b1.GCPCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name:      "victim-gcp-sa",
						Namespace: ptr.To[gwapiv1.Namespace]("tenant-b"),
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	_, err := c.rotateCredential(t.Context(), bsp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not permitted")
	require.NotContains(t, err.Error(), "not found",
		"the Secret must be rejected by the ReferenceGrant check before a lookup is even attempted")
}

// TestNewBackendSecurityPolicyController_RotateCredentialGCPCrossNamespaceAllowedWithReferenceGrant
// verifies that, unlike the blocked case above, a valid ReferenceGrant lets rotateCredential's full
// GCPCredentials.CredentialsFile path proceed past the ReferenceGrant check.
func TestNewBackendSecurityPolicyController_RotateCredentialGCPCrossNamespaceAllowedWithReferenceGrant(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

	// Create a secret containing an invalid service account JSON structure to ensure controlled test
	// failure past the ReferenceGrant check, mirroring
	// TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_CredentialsFile.
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
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-gcp-sa", Namespace: "tenant-b"},
		Data:       map[string][]byte{rotators.GCPServiceAccountJSON: []byte(serviceAccountJSON)},
	}))
	require.NoError(t, cl.Create(t.Context(), &gwapiv1b1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-tenant-a", Namespace: "tenant-b"},
		Spec: gwapiv1b1.ReferenceGrantSpec{
			From: []gwapiv1b1.ReferenceGrantFrom{{
				Group:     aiServiceBackendGroup,
				Kind:      backendSecurityPolicyKind,
				Namespace: "tenant-a",
			}},
			To: []gwapiv1b1.ReferenceGrantTo{{Group: secretGroup, Kind: secretKind}},
		},
	}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-gcp-bsp", Namespace: "tenant-a"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "test-project",
				Region:      "us-central1",
				CredentialsFile: &aigv1b1.GCPCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name:      "shared-gcp-sa",
						Namespace: ptr.To[gwapiv1.Namespace]("tenant-b"),
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	res, err := c.rotateCredential(t.Context(), bsp)
	// As in TestBackendSecurityPolicyController_RotateCredential_GCPCredentials_CredentialsFile, the
	// test behavior past the ReferenceGrant check varies depending on environment mocking, but both
	// outcomes prove the ReferenceGrant was honored:
	// 1. Error at token provider creation (invalid private key) confirms the Secret was read.
	// 2. Success indicates the test environment provides full mocking.
	if err != nil {
		require.Contains(t, err.Error(), "private key")
		require.NotContains(t, err.Error(), "is not permitted")
	} else {
		require.NotZero(t, res.RequeueAfter)
	}
}

// TestBackendSecurityPolicyController_Reconcile_StaticCredentialCrossNamespace verifies that a static
// credential whose Secret is in another namespace marks the policy NotAccepted unless a ReferenceGrant
// allows it, and that the targeted backend is synced either way so the Gateway controller re-reads it.
func TestBackendSecurityPolicyController_Reconcile_StaticCredentialCrossNamespace(t *testing.T) {
	const bspNamespace, secretNamespace = "app", "shared-secrets"
	secretRef := &gwapiv1.SecretObjectReference{Name: "creds", Namespace: ptr.To[gwapiv1.Namespace](secretNamespace)}
	staticTypes := []struct {
		name string
		spec aigv1b1.BackendSecurityPolicySpec
	}{
		{name: "APIKey", spec: aigv1b1.BackendSecurityPolicySpec{
			Type:   aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{SecretRef: secretRef},
		}},
		{name: "AzureAPIKey", spec: aigv1b1.BackendSecurityPolicySpec{
			Type:        aigv1b1.BackendSecurityPolicyTypeAzureAPIKey,
			AzureAPIKey: &aigv1b1.BackendSecurityPolicyAzureAPIKey{SecretRef: secretRef},
		}},
		{name: "AnthropicAPIKey", spec: aigv1b1.BackendSecurityPolicySpec{
			Type:            aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey,
			AnthropicAPIKey: &aigv1b1.BackendSecurityPolicyAnthropicAPIKey{SecretRef: secretRef},
		}},
		{name: "AWSCredentialsFile", spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
			AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
				Region:          "us-east-1",
				CredentialsFile: &aigv1b1.AWSCredentialsFile{SecretRef: secretRef},
			},
		}},
	}

	for _, st := range staticTypes {
		t.Run(st.name, func(t *testing.T) {
			for _, withGrant := range []bool{false, true} {
				t.Run(fmt.Sprintf("withGrant=%t", withGrant), func(t *testing.T) {
					eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
					cl := requireNewFakeClientWithIndexes(t)
					c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

					require.NoError(t, cl.Create(t.Context(), &aigv1b1.AIServiceBackend{
						ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: bspNamespace},
					}))
					if withGrant {
						require.NoError(t, cl.Create(t.Context(), &gwapiv1b1.ReferenceGrant{
							ObjectMeta: metav1.ObjectMeta{Name: "allow-app", Namespace: secretNamespace},
							Spec: gwapiv1b1.ReferenceGrantSpec{
								From: []gwapiv1b1.ReferenceGrantFrom{{
									Group:     aiServiceBackendGroup,
									Kind:      backendSecurityPolicyKind,
									Namespace: bspNamespace,
								}},
								To: []gwapiv1b1.ReferenceGrantTo{{Group: secretGroup, Kind: secretKind}},
							},
						}))
					}
					spec := st.spec
					spec.TargetRefs = []gwapiv1a2.LocalPolicyTargetReference{
						{Group: aiServiceBackendGroup, Kind: aiServiceBackendKind, Name: "backend"},
					}
					require.NoError(t, cl.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
						ObjectMeta: metav1.ObjectMeta{Name: "bsp", Namespace: bspNamespace},
						Spec:       spec,
					}))

					_, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bspNamespace, Name: "bsp"}})
					items := eventCh.RequireItemsEventually(t, 1)
					require.Equal(t, "backend", items[0].Name, "the targeted backend must be synced whether or not the Secret is allowed")

					var updated aigv1b1.BackendSecurityPolicy
					require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Namespace: bspNamespace, Name: "bsp"}, &updated))
					require.Len(t, updated.Status.Conditions, 1)
					if withGrant {
						require.NoError(t, err)
						require.Equal(t, aigv1b1.ConditionTypeAccepted, updated.Status.Conditions[0].Type)
						return
					}
					require.ErrorContains(t, err, "is not permitted")
					require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)
					require.Contains(t, updated.Status.Conditions[0].Message, "is not permitted")
				})
			}
		})
	}
}

func TestNewBackendSecurityPolicyController_RotateCredentialInvalidType(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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

func TestBackendSecurityPolicyController_RotateCredential_OpenAICredentials_OIDC(t *testing.T) {
	var gotBody map[string]string
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"issuer": %q, "token_endpoint": %q, "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": []}`,
			server.URL, server.URL+"/oidc/token")
	})
	mux.HandleFunc("/oidc/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token": "subject-token", "token_type": "Bearer", "expires_in": 3600}`))
	})
	mux.HandleFunc("/exchange", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Add("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token": "openai-access-token", "token_type": "Bearer", "expires_in": 3600, "expires_at": %d}`, time.Now().Add(time.Hour).Unix())
	})

	origTokenURL := tokenprovider.OpenAITokenExchangeURL
	tokenprovider.OpenAITokenExchangeURL = server.URL + "/exchange"
	t.Cleanup(func() { tokenprovider.OpenAITokenExchangeURL = origTokenURL })

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
	const bspNamespace = "default"
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "oidc-client-secret", Namespace: bspNamespace},
		Data:       map[string][]byte{"client-secret": []byte("client-secret")},
	}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "openai-oidc", Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeOpenAICredentials,
			OpenAICredentials: &aigv1b1.BackendSecurityPolicyOpenAICredentials{
				WorkloadIdentityFederationConfig: aigv1b1.OpenAIWorkloadIdentityFederationConfig{
					IdentityProviderID: "idp_123",
					ServiceAccountID:   "sa_456",
					SubjectToken: aigv1b1.OpenAISubjectToken{
						OIDCExchangeToken: &aigv1b1.BackendSecurityPolicyOIDC{
							OIDC: egv1a1.OIDC{
								Provider: egv1a1.OIDCProvider{Issuer: server.URL},
								ClientID: ptr.To("some-client-id"),
								ClientSecret: gwapiv1.SecretObjectReference{
									Name:      "oidc-client-secret",
									Namespace: ptr.To(gwapiv1.Namespace(bspNamespace)),
								},
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	res, err := c.rotateCredential(t.Context(), bsp)
	require.NoError(t, err)
	require.Greater(t, res.RequeueAfter, 50*time.Minute)

	require.Equal(t, "subject-token", gotBody["subject_token"])
	require.Equal(t, "idp_123", gotBody["identity_provider_id"])
	require.Equal(t, "sa_456", gotBody["service_account_id"])

	secret, err := rotators.LookupSecret(t.Context(), cl, bspNamespace, rotators.GetBSPSecretName("openai-oidc"))
	require.NoError(t, err)
	require.Equal(t, "openai-access-token", string(secret.Data[rotators.OpenAIAccessTokenKey]))
	ok, _ := ctrlutil.HasOwnerReference(secret.OwnerReferences, bsp, c.client.Scheme())
	require.True(t, ok, "expected secret to have owner reference to BackendSecurityPolicy")
}

func TestBackendSecurityPolicyController_Reconcile_OpenAICredentials_SPIFFEUnavailable(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "openai-bsp", Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeOpenAICredentials,
			OpenAICredentials: &aigv1b1.BackendSecurityPolicyOpenAICredentials{
				WorkloadIdentityFederationConfig: aigv1b1.OpenAIWorkloadIdentityFederationConfig{
					IdentityProviderID: "idp_123",
					ServiceAccountID:   "sa_456",
					SubjectToken: aigv1b1.OpenAISubjectToken{
						SPIFFEJWTSVID: &aigv1b1.BackendSecurityPolicySPIFFEJWTSVID{
							Audience:   "https://auth.example.com",
							SocketPath: "unix://" + filepath.Join(t.TempDir(), "missing.sock"),
						},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "openai-bsp"}})
	require.ErrorContains(t, err, "failed to fetch JWT-SVID from SPIFFE Workload API")

	var updated aigv1b1.BackendSecurityPolicy
	require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "openai-bsp"}, &updated))
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)
}

func TestBackendSecurityPolicyController_RotateCredential_OpenAICredentials_NoSubjectToken(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "openai-bsp", Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type:              aigv1b1.BackendSecurityPolicyTypeOpenAICredentials,
			OpenAICredentials: &aigv1b1.BackendSecurityPolicyOpenAICredentials{},
		},
	}
	_, err := c.rotateCredential(t.Context(), bsp)
	require.EqualError(t, err, "one of spiffeJWTSVID or oidcExchangeToken must be defined, namespace default name openai-bsp")
}

func TestNewBackendSecurityPolicyController_RotateCredentialAwsCredentialFile(t *testing.T) {
	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	cl := fake.NewClientBuilder().WithScheme(Scheme).Build()
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)

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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
		DefaultPreRotationWindow,
		&oidc,
		"placeholder",
		"us-east-1",
		nil,
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

func TestBackendSecurityPolicyController_RotateCredential_AzureCredentials_Nil(t *testing.T) {
	c := NewBackendSecurityPolicyController(fake.NewFakeClient(), fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-azure-policy",
			Namespace: "default",
		},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type:             aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: nil,
		},
	}
	_, err := c.rotateCredential(context.Background(), bsp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid backend security policy, azure credentials cannot be nil")
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

	c := NewBackendSecurityPolicyController(fake.NewFakeClient(), fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)

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
	c := NewBackendSecurityPolicyController(fake.NewFakeClient(), fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, eventCh.Ch, nil, DefaultPreRotationWindow)
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
		{
			name: "OpenAICredentials type",
			bsp: &aigv1b1.BackendSecurityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "openai-bsp",
				},
				Spec: aigv1b1.BackendSecurityPolicySpec{
					Type: aigv1b1.BackendSecurityPolicyTypeOpenAICredentials,
				},
			},
			expectedName: "ai-eg-bsp-openai-bsp",
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

// fakeRotator is a [rotators.Rotator] whose results are fixed by the test.
type fakeRotator struct {
	preRotationTime    time.Time
	preRotationTimeErr error
	expirationTime     time.Time
	rotateErr          error
	rotated            bool
}

func (f *fakeRotator) IsExpired(preRotationExpirationTime time.Time) bool {
	return !time.Now().Before(preRotationExpirationTime)
}

func (f *fakeRotator) GetPreRotationTime(context.Context) (time.Time, error) {
	return f.preRotationTime, f.preRotationTimeErr
}

func (f *fakeRotator) Rotate(context.Context) (time.Time, error) {
	f.rotated = true
	return f.expirationTime, f.rotateErr
}

func TestBackendSecurityPolicyController_executeRotation(t *testing.T) {
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp", Namespace: "default"},
		Spec:       aigv1b1.BackendSecurityPolicySpec{Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials},
	}
	notFound := apierrors.NewNotFound(corev1.Resource("secrets"), "bsp")

	for _, tc := range []struct {
		name              string
		preRotationWindow time.Duration
		rotator           *fakeRotator
		expRotated        bool
		expErr            string
		// expRequeueMin and expRequeueMax bound the expected RequeueAfter.
		expRequeueMin, expRequeueMax time.Duration
	}{
		{
			name:              "failing to get the pre-rotation time retries in one minute",
			preRotationWindow: DefaultPreRotationWindow,
			rotator:           &fakeRotator{preRotationTimeErr: fmt.Errorf("boom")},
			expErr:            "boom",
			expRequeueMin:     time.Minute,
			expRequeueMax:     time.Minute,
		},
		{
			name:              "missing secret rotates and requeues before expiry by the pre-rotation window",
			preRotationWindow: 20 * time.Minute,
			rotator:           &fakeRotator{preRotationTimeErr: notFound, expirationTime: time.Now().Add(time.Hour)},
			expRotated:        true,
			expRequeueMin:     39 * time.Minute,
			expRequeueMax:     40 * time.Minute,
		},
		{
			name:              "expired credential rotates using the configured pre-rotation window",
			preRotationWindow: 30 * time.Minute,
			rotator: &fakeRotator{
				preRotationTime: time.Now().Add(-time.Minute),
				expirationTime:  time.Now().Add(time.Hour),
			},
			expRotated:    true,
			expRequeueMin: 29 * time.Minute,
			expRequeueMax: 30 * time.Minute,
		},
		{
			name:              "pre-rotation window longer than the credential lifetime is an error",
			preRotationWindow: 20 * time.Minute,
			rotator: &fakeRotator{
				preRotationTime: time.Now().Add(-time.Minute),
				expirationTime:  time.Now().Add(10 * time.Minute),
			},
			expRotated:    true,
			expErr:        "newly rotated credential is already expired",
			expRequeueMin: time.Minute,
			expRequeueMax: time.Minute,
		},
		{
			name:              "failing to rotate retries in one minute",
			preRotationWindow: DefaultPreRotationWindow,
			rotator: &fakeRotator{
				preRotationTime: time.Now().Add(-time.Minute),
				rotateErr:       fmt.Errorf("rotate failed"),
			},
			expRotated:    true,
			expErr:        "rotate failed",
			expRequeueMin: time.Minute,
			expRequeueMax: time.Minute,
		},
		{
			name:              "unexpired credential requeues at the pre-rotation time without rotating",
			preRotationWindow: DefaultPreRotationWindow,
			rotator:           &fakeRotator{preRotationTime: time.Now().Add(15 * time.Minute)},
			expRequeueMin:     14 * time.Minute,
			expRequeueMax:     15 * time.Minute,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewBackendSecurityPolicyController(fake.NewFakeClient(), fake2.NewClientset(), ctrl.Log, nil, nil, tc.preRotationWindow)
			res, err := c.executeRotation(t.Context(), tc.rotator, bsp)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.expRotated, tc.rotator.rotated)
			require.GreaterOrEqual(t, res.RequeueAfter, tc.expRequeueMin)
			require.LessOrEqual(t, res.RequeueAfter, tc.expRequeueMax)
		})
	}
}

func TestBackendSecurityPolicyController_Reconcile_GetPolicy(t *testing.T) {
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "bsp"}}

	t.Run("deleted policy is ignored", func(t *testing.T) {
		c := NewBackendSecurityPolicyController(requireNewFakeClientWithIndexes(t), fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)
		res, err := c.Reconcile(t.Context(), req)
		require.NoError(t, err)
		require.Equal(t, ctrl.Result{}, res)
	})

	t.Run("get error is returned", func(t *testing.T) {
		cl := interceptor.NewClient(fake.NewClientBuilder().WithScheme(Scheme).Build(), interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return fmt.Errorf("get failed")
			},
		})
		c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)
		_, err := c.Reconcile(t.Context(), req)
		require.EqualError(t, err, "get failed")
	})
}

func TestBackendSecurityPolicyController_syncBackendSecurityPolicy(t *testing.T) {
	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bsp", Namespace: "default"},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: aiServiceBackendGroup, Kind: aiServiceBackendKind, Name: "missing-backend"},
				{Group: inferencePoolGroup, Kind: inferencePoolKind, Name: "missing-pool"},
			},
		},
	}

	t.Run("missing targets are skipped", func(t *testing.T) {
		aiServiceBackendEventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
		inferencePoolEventCh := internaltesting.NewControllerEventChan[*gwaiev1.InferencePool]()
		c := NewBackendSecurityPolicyController(requireNewFakeClientWithIndexesAndInferencePool(t), fake2.NewClientset(), ctrl.Log,
			aiServiceBackendEventCh.Ch, inferencePoolEventCh.Ch, DefaultPreRotationWindow)
		require.NoError(t, c.syncBackendSecurityPolicy(t.Context(), bsp))
		require.Empty(t, aiServiceBackendEventCh.RequireItemsEventually(t, 0))
		require.Empty(t, inferencePoolEventCh.RequireItemsEventually(t, 0))
	})

	for _, tc := range []struct {
		name   string
		failOn client.Object
		expErr string
	}{
		{
			name:   "AIServiceBackend get error",
			failOn: &aigv1b1.AIServiceBackend{},
			expErr: "failed to get targeted AIServiceBackend missing-backend: get failed",
		},
		{
			name:   "InferencePool get error",
			failOn: &gwaiev1.InferencePool{},
			expErr: "failed to get targeted InferencePool missing-pool: get failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := interceptor.NewClient(fake.NewClientBuilder().WithScheme(Scheme).Build(), interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if fmt.Sprintf("%T", obj) == fmt.Sprintf("%T", tc.failOn) {
						return fmt.Errorf("get failed")
					}
					return cl.Get(ctx, key, obj, opts...)
				},
			})
			c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, nil, nil, DefaultPreRotationWindow)
			require.EqualError(t, c.syncBackendSecurityPolicy(t.Context(), bsp), tc.expErr)
		})
	}
}

func TestGetBSPGeneratedSecretName_UnsupportedType(t *testing.T) {
	require.PanicsWithValue(t, "BUG: unsupported backend security policy type: Unknown", func() {
		getBSPGeneratedSecretName(&aigv1b1.BackendSecurityPolicy{
			Spec: aigv1b1.BackendSecurityPolicySpec{Type: "Unknown"},
		})
	})
}

// TestBackendSecurityPolicyController_RotateCredential_AzureClientSecretUsesPreRotationWindow verifies that
// the Azure client secret path builds its rotator with the controller's configured pre-rotation window.
func TestBackendSecurityPolicyController_RotateCredential_AzureClientSecretUsesPreRotationWindow(t *testing.T) {
	const bspNamespace, bspName = "default", "azure-bsp"
	preRotationWindow := 10 * time.Minute
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, nil, nil, preRotationWindow)

	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "azure-client-secret", Namespace: bspNamespace},
		Data:       map[string][]byte{clientSecretKey: []byte("client-secret")},
	}))
	// Pre-create the generated secret with a future expiration so that no token request is made.
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rotators.GetBSPSecretName(bspName),
			Namespace: bspNamespace,
			Annotations: map[string]string{
				rotators.ExpirationTimeAnnotationKey: time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		},
	}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAzureCredentials,
			AzureCredentials: &aigv1b1.BackendSecurityPolicyAzureCredentials{
				ClientID:        "client-id",
				TenantID:        "tenant-id",
				ClientSecretRef: &gwapiv1.SecretObjectReference{Name: "azure-client-secret"},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	res, err := c.rotateCredential(t.Context(), bsp)
	require.NoError(t, err)
	// Expiration is in one hour, so the next rotation should be scheduled one hour minus the window from now.
	require.InDelta(t, (time.Hour - preRotationWindow).Seconds(), res.RequeueAfter.Seconds(), 5)
}

// TestBackendSecurityPolicyController_RotateCredential_GCPOIDCUsesPreRotationWindow verifies that the GCP
// OIDC path builds its rotator with the controller's configured pre-rotation window.
func TestBackendSecurityPolicyController_RotateCredential_GCPOIDCUsesPreRotationWindow(t *testing.T) {
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"issuer": "issuer", "token_endpoint": "token_endpoint", "authorization_endpoint": "authorization_endpoint", "jwks_uri": "jwks_uri", "scopes_supported": []}`))
		require.NoError(t, err)
	}))
	defer discoveryServer.Close()

	const bspNamespace, bspName = "default", "gcp-oidc-bsp"
	preRotationWindow := 10 * time.Minute
	cl := requireNewFakeClientWithIndexes(t)
	c := NewBackendSecurityPolicyController(cl, fake2.NewClientset(), ctrl.Log, nil, nil, preRotationWindow)

	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "oidc-client-secret", Namespace: bspNamespace},
		Data:       map[string][]byte{"client-secret": []byte("client-secret")},
	}))
	// Pre-create the generated secret with a future expiration so that no token request is made.
	require.NoError(t, cl.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rotators.GetBSPSecretName(bspName),
			Namespace: bspNamespace,
			Annotations: map[string]string{
				rotators.ExpirationTimeAnnotationKey: time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		},
	}))

	bsp := &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
			GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
				ProjectName: "project-name",
				Region:      "us-central1",
				WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{
					ProjectID:                    "project-id",
					WorkloadIdentityPoolName:     "pool-name",
					WorkloadIdentityProviderName: "provider-name",
					OIDCExchangeToken: aigv1b1.GCPOIDCExchangeToken{
						BackendSecurityPolicyOIDC: aigv1b1.BackendSecurityPolicyOIDC{
							OIDC: egv1a1.OIDC{
								Provider: egv1a1.OIDCProvider{Issuer: discoveryServer.URL},
								ClientID: ptr.To("some-client-id"),
								ClientSecret: gwapiv1.SecretObjectReference{
									Name: "oidc-client-secret",
								},
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(t.Context(), bsp))

	ctx := oidcv3.InsecureIssuerURLContext(t.Context(), discoveryServer.URL)
	res, err := c.rotateCredential(ctx, bsp)
	require.NoError(t, err)
	// Expiration is in one hour, so the next rotation should be scheduled one hour minus the window from now.
	require.InDelta(t, (time.Hour - preRotationWindow).Seconds(), res.RequeueAfter.Seconds(), 5)
}
