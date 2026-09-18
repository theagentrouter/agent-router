// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

// requireNewFakeClientForGatewayConfig creates a fake client for GatewayConfig tests.
func requireNewFakeClientForGatewayConfig(t *testing.T) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(Scheme).
		WithStatusSubresource(&aigv1b1.GatewayConfig{}).
		WithIndex(&gwapiv1.Gateway{}, k8sClientIndexGatewayToGatewayConfig, gatewayToGatewayConfigIndexFunc)
	return builder.Build()
}

type errorListClient struct {
	client.Client
	listErr error
}

func (c *errorListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.listErr != nil {
		return c.listErr
	}
	return c.Client.List(ctx, list, opts...)
}

func TestGatewayConfigController_Reconcile(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewGatewayConfigController(fakeClient, ctrl.Log, eventCh.Ch)

	// Create a GatewayConfig.
	gatewayConfig := &aigv1b1.GatewayConfig{
		Name:      "test-config",
		Namespace: "default",
		Spec: aigv1b1.GatewayConfigSpec{
			ExtProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					Env: []corev1.EnvVar{
						{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector:4317"},
					},
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}
	err := fakeClient.Create(t.Context(), gatewayConfig)
	require.NoError(t, err)

	// Reconcile - should succeed with no referencing Gateways.
	result, err := c.Reconcile(t.Context(), reconcile.Request{
		Name: "test-config", Namespace: "default",
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	// Verify status was updated to Accepted.
	var updated aigv1b1.GatewayConfig
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "test-config", Namespace: "default"}, &updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeAccepted, updated.Status.Conditions[0].Type)
}

func TestGatewayConfigController_NotifyGateways(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewGatewayConfigController(fakeClient, ctrl.Log, eventCh.Ch)

	// Create a GatewayConfig.
	gatewayConfig := &aigv1b1.GatewayConfig{
		Name:      "test-config",
		Namespace: "default",
		Spec: aigv1b1.GatewayConfigSpec{
			ExtProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					Env: []corev1.EnvVar{
						{Name: "LOG_LEVEL", Value: "debug"},
					},
				},
			},
		},
	}
	err := fakeClient.Create(t.Context(), gatewayConfig)
	require.NoError(t, err)

	// Reconcile without any referencing Gateway - should not add finalizer.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		Name: "test-config", Namespace: "default",
	})
	require.NoError(t, err)

	var updatedConfig aigv1b1.GatewayConfig
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "test-config", Namespace: "default"}, &updatedConfig)
	require.NoError(t, err)
	require.Empty(t, updatedConfig.Finalizers)

	// Create a Gateway that references the GatewayConfig.
	gateway := &gwapiv1.Gateway{
		Name:      "test-gateway",
		Namespace: "default",
		Annotations: map[string]string{
			GatewayConfigAnnotationKey: "test-config",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
			Listeners: []gwapiv1.Listener{
				{
					Name:     "http",
					Port:     8080,
					Protocol: gwapiv1.HTTPProtocolType,
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), gateway)
	require.NoError(t, err)

	// Reconcile again - should notify the Gateway and still not add any finalizer.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		Name: "test-config", Namespace: "default",
	})
	require.NoError(t, err)

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "test-config", Namespace: "default"}, &updatedConfig)
	require.NoError(t, err)
	require.Empty(t, updatedConfig.Finalizers)

	// Gateway event should be sent.
	events := eventCh.RequireItemsEventually(t, 1)
	require.Len(t, events, 1)
}

func TestGatewayConfigController_MultipleGatewaysReferencing(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewGatewayConfigController(fakeClient, ctrl.Log, eventCh.Ch)

	// Create a GatewayConfig.
	gatewayConfig := &aigv1b1.GatewayConfig{
		Name:      "shared-config",
		Namespace: "default",
		Spec: aigv1b1.GatewayConfigSpec{
			ExtProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					Env: []corev1.EnvVar{
						{Name: "SHARED_VAR", Value: "shared-value"},
					},
				},
			},
		},
	}
	err := fakeClient.Create(t.Context(), gatewayConfig)
	require.NoError(t, err)

	// Create two Gateways that reference the same GatewayConfig.
	for _, name := range []string{"gateway-1", "gateway-2"} {
		gateway := &gwapiv1.Gateway{
			Name:      name,
			Namespace: "default",
			Annotations: map[string]string{
				GatewayConfigAnnotationKey: "shared-config",
			},
			Spec: gwapiv1.GatewaySpec{
				GatewayClassName: "test-class",
				Listeners: []gwapiv1.Listener{
					{
						Name:     "http",
						Port:     8080,
						Protocol: gwapiv1.HTTPProtocolType,
					},
				},
			},
		}
		err = fakeClient.Create(t.Context(), gateway)
		require.NoError(t, err)
	}

	// Reconcile - should notify both gateways.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		Name: "shared-config", Namespace: "default",
	})
	require.NoError(t, err)

	var updatedConfig aigv1b1.GatewayConfig
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "shared-config", Namespace: "default"}, &updatedConfig)
	require.NoError(t, err)
	require.Empty(t, updatedConfig.Finalizers)

	// Both Gateways should have been notified.
	events := eventCh.RequireItemsEventually(t, 2)
	require.Len(t, events, 2)
}

func TestGatewayConfigController_DeletionDoesNotBlock(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewGatewayConfigController(fakeClient, ctrl.Log, eventCh.Ch)

	deletionTime := metav1.NewTime(time.Now())

	// Create a GatewayConfig marked for deletion.
	gatewayConfig := &aigv1b1.GatewayConfig{
		Name:              "test-config",
		Namespace:         "default",
		DeletionTimestamp: &deletionTime,
		Spec:              aigv1b1.GatewayConfigSpec{},
	}
	err := fakeClient.Create(t.Context(), gatewayConfig)
	require.NoError(t, err)

	// Create a Gateway that references the GatewayConfig.
	gateway := &gwapiv1.Gateway{
		Name:      "test-gateway",
		Namespace: "default",
		Annotations: map[string]string{
			GatewayConfigAnnotationKey: "test-config",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
			Listeners: []gwapiv1.Listener{
				{
					Name:     "http",
					Port:     8080,
					Protocol: gwapiv1.HTTPProtocolType,
				},
			},
		},
	}
	err = fakeClient.Create(t.Context(), gateway)
	require.NoError(t, err)

	// Reconcile should not block deletion and should notify the referencing Gateway.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		Name: "test-config", Namespace: "default",
	})
	require.NoError(t, err)

	events := eventCh.RequireItemsEventually(t, 1)
	require.Len(t, events, 1)
}

func TestGatewayConfigController_ReconcileNotFound(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewGatewayConfigController(fakeClient, ctrl.Log, eventCh.Ch)

	result, err := c.Reconcile(t.Context(), reconcile.Request{
		Name: "missing-config", Namespace: "default",
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
	require.Empty(t, eventCh.RequireItemsEventually(t, 0))
}

func TestGatewayConfigController_ListErrorSetsNotAcceptedStatus(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	errClient := &errorListClient{
		Client:  fakeClient,
		listErr: errors.New("list failure"),
	}
	c := NewGatewayConfigController(errClient, ctrl.Log, eventCh.Ch)

	gatewayConfig := &aigv1b1.GatewayConfig{
		Name:      "list-error-config",
		Namespace: "default",
		Spec:      aigv1b1.GatewayConfigSpec{},
	}
	err := fakeClient.Create(t.Context(), gatewayConfig)
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcile.Request{
		Name: "list-error-config", Namespace: "default",
	})
	require.Error(t, err)

	var updated aigv1b1.GatewayConfig
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: "list-error-config", Namespace: "default"}, &updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, updated.Status.Conditions[0].Type)
	require.Equal(t, metav1.ConditionFalse, updated.Status.Conditions[0].Status)
	require.Contains(t, updated.Status.Conditions[0].Message, "failed to find referencing Gateways")
}

func TestGatewayConfigController_GatewayReferencesNonExistingConfig(t *testing.T) {
	fakeClient := requireNewFakeClientForGatewayConfig(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewGatewayConfigController(fakeClient, ctrl.Log, eventCh.Ch)

	// Create a Gateway that references a GatewayConfig that doesn't exist (e.g., user made a typo).
	gateway := &gwapiv1.Gateway{
		Name:      "test-gateway",
		Namespace: "default",
		Annotations: map[string]string{
			GatewayConfigAnnotationKey: "typo-config", // This config will never be created
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "test-class",
			Listeners: []gwapiv1.Listener{
				{
					Name:     "http",
					Port:     8080,
					Protocol: gwapiv1.HTTPProtocolType,
				},
			},
		},
	}
	err := fakeClient.Create(t.Context(), gateway)
	require.NoError(t, err)

	// Try to reconcile the non-existing GatewayConfig.
	// This should return nil (no error) since the resource doesn't exist.
	_, err = c.Reconcile(t.Context(), reconcile.Request{
		Name: "typo-config", Namespace: "default",
	})
	require.NoError(t, err)

	// No events should be sent since the GatewayConfig doesn't exist.
	require.Empty(t, eventCh.RequireItemsEventually(t, 0))
}

func TestGatewayConfigConditionsNotAccepted(t *testing.T) {
	conds := gatewayConfigConditions(aigv1b1.ConditionTypeNotAccepted, "nope")
	require.Len(t, conds, 1)
	require.Equal(t, aigv1b1.ConditionTypeNotAccepted, conds[0].Type)
	require.Equal(t, metav1.ConditionFalse, conds[0].Status)
	require.Equal(t, "nope", conds[0].Message)
}
