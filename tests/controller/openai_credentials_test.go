// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spiffe/go-spiffe/v2/proto/spiffe/workload"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
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
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
)

const testControllerSPIFFEID = "spiffe://example.org/ns/envoy-ai-gateway-system/sa/ai-gateway-controller"

// TestBackendSecurityPolicyController_OpenAICredentialsSPIFFE runs the BackendSecurityPolicy controller
// against a fake SPIFFE Workload API and a fake token exchange endpoint, and checks that the exchanged
// access token ends up in the generated secret.
func TestBackendSecurityPolicyController_OpenAICredentialsSPIFFE(t *testing.T) {
	c, cfg, k := testsinternal.NewEnvTest(t)

	workloadAPI := &fakeWorkloadAPI{t: t}
	socketAddr := startFakeWorkloadAPI(t, workloadAPI)

	var (
		mu                sync.Mutex
		gotSubjectToken   string
		gotSubjectTokType string
	)
	exchangeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotSubjectToken = r.PostForm.Get("subject_token")
		gotSubjectTokType = r.PostForm.Get("subject_token_type")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"openai-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(exchangeServer.Close)

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	eventChPool := internaltesting.NewControllerEventChan[*gwaiev1.InferencePool]()
	opt := ctrl.Options{Scheme: c.Scheme(), LeaderElection: false, Controller: config.Controller{SkipNameValidation: ptr.To(true)}}
	mgr, err := ctrl.NewManager(cfg, opt)
	require.NoError(t, err)
	require.NoError(t, controller.ApplyIndexing(t.Context(), mgr.GetFieldIndexer().IndexField))
	pc := controller.NewBackendSecurityPolicyController(mgr.GetClient(), k, defaultLogger(), eventCh.Ch, eventChPool.Ch)
	require.NoError(t, controller.TypedControllerBuilderForCRD(mgr, &aigv1b1.BackendSecurityPolicy{}).Complete(pc))
	go func() { require.NoError(t, mgr.Start(t.Context())) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(t.Context()))

	const bspName, bspNamespace = "openai-spiffe", "default"
	require.NoError(t, c.Create(t.Context(), &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: bspName, Namespace: bspNamespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type: aigv1b1.BackendSecurityPolicyTypeOpenAICredentials,
			OpenAICredentials: &aigv1b1.BackendSecurityPolicyOpenAICredentials{
				Organization: "org-123",
				TokenExchange: aigv1b1.BackendSecurityPolicyTokenExchange{
					TokenURL: exchangeServer.URL,
					SubjectToken: aigv1b1.BackendSecurityPolicySubjectToken{
						SPIFFEJWTSVID: &aigv1b1.BackendSecurityPolicySPIFFEJWTSVID{
							Audience:   "https://auth.example.com",
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
	require.Equal(t, "openai-access-token", string(secret.Data[rotators.OpenAIAccessTokenKey]))
	expiresAt, err := rotators.GetExpirationSecretAnnotation(&secret)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Hour), expiresAt, time.Minute)

	require.Eventually(t, func() bool {
		var bsp aigv1b1.BackendSecurityPolicy
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: bspName, Namespace: bspNamespace}, &bsp))
		return len(bsp.Status.Conditions) == 1 && bsp.Status.Conditions[0].Type == aigv1b1.ConditionTypeAccepted
	}, 30*time.Second, 200*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "urn:ietf:params:oauth:token-type:jwt", gotSubjectTokType)
	claims := jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(gotSubjectToken, &claims, func(*jwt.Token) (any, error) { return &workloadAPI.key().PublicKey, nil })
	require.NoError(t, err)
	require.Equal(t, testControllerSPIFFEID, claims.Subject)
	require.Equal(t, jwt.ClaimStrings{"https://auth.example.com"}, claims.Audience)
}

// fakeWorkloadAPI issues JWT-SVIDs for testControllerSPIFFEID.
type fakeWorkloadAPI struct {
	workload.UnimplementedSpiffeWorkloadAPIServer
	t       *testing.T
	once    sync.Once
	signKey *ecdsa.PrivateKey
}

func (f *fakeWorkloadAPI) key() *ecdsa.PrivateKey {
	f.once.Do(func() {
		var err error
		f.signKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(f.t, err)
	})
	return f.signKey
}

func (f *fakeWorkloadAPI) FetchJWTSVID(_ context.Context, req *workload.JWTSVIDRequest) (*workload.JWTSVIDResponse, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		Subject:   testControllerSPIFFEID,
		Audience:  req.Audience,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
	})
	signed, err := token.SignedString(f.key())
	if err != nil {
		return nil, err
	}
	return &workload.JWTSVIDResponse{Svids: []*workload.JWTSVID{{SpiffeId: testControllerSPIFFEID, Svid: signed}}}, nil
}

// startFakeWorkloadAPI serves the given server on a unix socket and returns its address.
func startFakeWorkloadAPI(t *testing.T, server workload.SpiffeWorkloadAPIServer) string {
	t.Helper()
	// Use a short directory since unix socket paths are limited to ~104 bytes on macOS.
	dir, err := os.MkdirTemp("", "spiffe")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "agent.sock")

	lis, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	s := grpc.NewServer()
	workload.RegisterSpiffeWorkloadAPIServer(s, server)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return "unix://" + socketPath
}
