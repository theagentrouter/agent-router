// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"cmp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestSecretController_Reconcile(t *testing.T) {
	bspCh := internaltesting.NewControllerEventChan[*aigv1b1.BackendSecurityPolicy]()
	mcpRouteCh := internaltesting.NewControllerEventChan[*aigv1b1.MCPRoute]()
	gatewayConfigCh := internaltesting.NewControllerEventChan[*aigv1b1.GatewayConfig]()
	fakeClient := requireNewFakeClientWithIndexes(t)
	c := NewSecretController(fakeClient, fake2.NewClientset(), ctrl.Log, bspCh.Ch, mcpRouteCh.Ch, gatewayConfigCh.Ch)

	err := fakeClient.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mysecret", Namespace: "default"},
		StringData: map[string]string{"key": "value"},
	})
	require.NoError(t, err)

	// Create a bsp that references the secret.
	bsps := []*aigv1b1.BackendSecurityPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type:   aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "mysecret"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar", Namespace: "default"},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
				AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
					Region:          "us-west-2",
					CredentialsFile: &aigv1b1.AWSCredentialsFile{SecretRef: &gwapiv1.SecretObjectReference{Name: "mysecret"}},
				},
			},
		},
	}
	for _, bsp := range bsps {
		require.NoError(t, fakeClient.Create(t.Context(), bsp))
	}

	// Create a MCPRoute that references the secret via API Key secret ref.
	mcp := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			BackendRefs: []aigv1b1.MCPRouteBackendRef{{
				SecurityPolicy: &aigv1b1.MCPBackendSecurityPolicy{APIKey: &aigv1b1.MCPBackendAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{Name: "mysecret", Namespace: ptr.To[gwapiv1.Namespace]("default")},
				}},
			}},
		},
	}
	require.NoError(t, fakeClient.Create(t.Context(), mcp))

	// Create a GatewayConfig whose forward proxy references the secret, and one that does not.
	gatewayConfig := &aigv1b1.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "gwconfig", Namespace: "default"},
		Spec: aigv1b1.GatewayConfigSpec{ForwardProxy: &aigv1b1.GatewayConfigForwardProxy{
			Address: "proxy.corp:3128",
			Auth: &aigv1b1.GatewayConfigForwardProxyAuth{
				Basic: &aigv1b1.GatewayConfigForwardProxyBasicAuth{SecretRef: gwapiv1.SecretObjectReference{Name: "mysecret"}},
			},
		}},
	}
	require.NoError(t, fakeClient.Create(t.Context(), gatewayConfig))
	require.NoError(t, fakeClient.Create(t.Context(), &aigv1b1.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec:       aigv1b1.GatewayConfigSpec{ForwardProxy: &aigv1b1.GatewayConfigForwardProxy{Address: "proxy.corp:3128"}},
	}))

	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: "default", Name: "mysecret",
	}})
	require.NoError(t, err)

	// Verify that both BSP and MCPRoute events are triggered.
	actual := bspCh.RequireItemsEventually(t, len(bsps))
	slices.SortFunc(actual, func(a, b *aigv1b1.BackendSecurityPolicy) int {
		return cmp.Compare(a.Name, b.Name)
	})
	slices.SortFunc(bsps, func(a, b *aigv1b1.BackendSecurityPolicy) int {
		return cmp.Compare(a.Name, b.Name)
	})
	require.Equal(t, bsps, actual)

	mcpActual := mcpRouteCh.RequireItemsEventually(t, 1)
	require.Equal(t, mcp, mcpActual[0])

	gatewayConfigActual := gatewayConfigCh.RequireItemsEventually(t, 1)
	require.Equal(t, gatewayConfig.Name, gatewayConfigActual[0].Name)

	// Test the case where the Secret is being deleted.
	err = fakeClient.Delete(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mysecret", Namespace: "default"},
	})
	require.NoError(t, err)
	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: "default", Name: "mysecret",
	}})
	require.NoError(t, err)

	// The deletion still notifies the GatewayConfig referencing the secret.
	gatewayConfigActual = gatewayConfigCh.RequireItemsEventually(t, 1)
	require.Equal(t, gatewayConfig.Name, gatewayConfigActual[0].Name)
}
