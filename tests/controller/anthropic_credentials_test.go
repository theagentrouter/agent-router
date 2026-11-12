// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
)

// TestBackendSecurityPolicyController_AnthropicCredentialsSPIFFE runs the BackendSecurityPolicy controller
// against a fake SPIFFE Workload API and a fake Anthropic token exchange endpoint, and checks that the exchanged
// access token ends up in the generated secret.
func TestBackendSecurityPolicyController_AnthropicCredentialsSPIFFE(t *testing.T) {
	c, cfg, k := testsinternal.NewEnvTest(t)

	workloadAPI := &fakeWorkloadAPI{t: t}
	socketAddr := startFakeWorkloadAPI(t, workloadAPI)

	var (
		mu      sync.Mutex
		gotBody map[string]string
	)
	exchangeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-access-token","token_type":"Bearer","expires_in":600,"scope":"workspace:inference"}`))
	}))
	t.Cleanup(exchangeServer.Close)
	origTokenURL := tokenprovider.AnthropicTokenExchangeURL
	tokenprovider.AnthropicTokenExchangeURL = exchangeServer.URL
	t.Cleanup(func() { tokenprovider.AnthropicTokenExchangeURL = origTokenURL })

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	eventChPool := internaltesting.NewControllerEventChan[*gwaiev1.InferencePool]()
	opt := ctrl.Options{Scheme: c.Scheme(), LeaderElection: false, Controller: config.Controller{SkipNameValidation: ptr.To(true)}}
	mgr, err := ctrl.NewManager(cfg, opt)
	require.NoError(t, err)
	require.NoError(t, controller.ApplyIndexing(t.Context(), mgr.GetFieldIndexer().IndexField))
	pc := controller.NewBackendSecurityPolicyController(mgr.GetClient(), k, defaultLogger(), eventCh.Ch, eventChPool.Ch, controller.DefaultPreRotationWindow)
	require.NoError(t, controller.TypedControllerBuilderForCRD(mgr, &aigv1b1.BackendSecurityPolicy{}).Complete(pc))
	go func() { require.NoError(t, mgr.Start(t.Context())) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(t.Context()))

	const bspName, bspNamespace = "anthropic-spiffe", "default"
	require.NoError(t, c.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeAnthropicCredentials,
			AnthropicCredentials: &aigv1b1.BackendSecurityPolicyAnthropicCredentials{
				WorkloadIdentityFederationConfig: aigv1b1.AnthropicWorkloadIdentityFederationConfig{
					OrganizationID:   "00000000-0000-0000-0000-000000000000",
					ServiceAccountID: "svac_123",
					FederationRuleID: "fdrl_456",
					WorkspaceID:      "wrkspc_789",
					SubjectToken: aigv1b1.OpenAISubjectToken{
						SPIFFEJWTSVID: &aigv1b1.BackendSecurityPolicySPIFFEJWTSVID{
							Audience:   "https://api.anthropic.com",
							SocketPath: socketAddr,
						},
					},
				},
			},
		},
	}))

	var secret corev1.Secret
	require.Eventually(t, func() bool {
		return c.Get(t.Context(), client.ObjectKey{Name: rotators.GetBSPSecretName(bspName), Namespace: bspNamespace}, &secret) == nil
	}, 30*time.Second, 200*time.Millisecond)
	require.Equal(t, "sk-ant-oat01-access-token", string(secret.Data[rotators.AnthropicAccessTokenKey]))
	expiresAt, err := rotators.GetExpirationSecretAnnotation(&secret)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(10*time.Minute), expiresAt, time.Minute)

	require.Eventually(t, func() bool {
		var bsp aigv1b1.BackendSecurityPolicy
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: bspName, Namespace: bspNamespace}, &bsp))
		return len(bsp.Status.Conditions) == 1 && bsp.Status.Conditions[0].Type == aigv1b1.ConditionTypeAccepted
	}, 30*time.Second, 200*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", gotBody["grant_type"])
	require.Equal(t, "00000000-0000-0000-0000-000000000000", gotBody["organization_id"])
	require.Equal(t, "svac_123", gotBody["service_account_id"])
	require.Equal(t, "fdrl_456", gotBody["federation_rule_id"])
	require.Equal(t, "wrkspc_789", gotBody["workspace_id"])
	claims := jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(gotBody["assertion"], &claims, func(*jwt.Token) (any, error) { return &workloadAPI.key().PublicKey, nil })
	require.NoError(t, err)
	require.Equal(t, testControllerSPIFFEID, claims.Subject)
	require.Equal(t, jwt.ClaimStrings{"https://api.anthropic.com"}, claims.Audience)
}
