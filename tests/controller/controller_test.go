// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package controller tests the internal/controller package using envtest.
// This is sort of the end-to-end test for the controller package, but without testing the
// actual interaction with the Envoy Gateway as well as the external process.
package controller

import (
	"cmp"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var defaultSchema = aigv1b1.VersionedAPISchema{Name: aigv1b1.APISchemaOpenAI, Prefix: new("v1")}

// TestStartControllers tests the [controller.StartControllers] function.
func TestStartControllers(t *testing.T) {
	c, cfg, _ := testsinternal.NewEnvTest(t)
	opts := controller.Options{
		ExtProcImage:           "envoyproxy/ai-gateway-extproc:foo",
		EnableLeaderElection:   false,
		DisableMutatingWebhook: true,
		RootPrefix:             "/root-prefix",
		ExtProcMaxRecvMsgSize:  512 * 1024 * 1024, // 512MB
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         controller.Scheme,
		LeaderElection: false,
	})
	require.NoError(t, err)

	ctx := t.Context()
	go func() {
		err := controller.StartControllers(ctx, mgr, cfg, defaultLogger(), &opts)
		require.NoError(t, err)
	}()

	t.Run("setup backends", func(t *testing.T) {
		for _, backend := range []string{"backend1", "backend2", "backend3", "backend4"} {
			err := c.Create(ctx, &aigv1b1.AIServiceBackend{
				Name: backend, Namespace: "default",
				Spec: aigv1b1.AIServiceBackendSpec{
					APISchema: defaultSchema,
					BackendRef: gwapiv1.BackendObjectReference{
						Name:  gwapiv1.ObjectName(backend),
						Kind:  ptr.To(gwapiv1.Kind("Backend")),
						Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
					},
				},
			})
			require.NoError(t, err)
		}
	})
	t.Run("setup routes", func(t *testing.T) {
		for _, route := range []string{"route1", "route2"} {
			parentRefs := []gwapiv1a2.ParentReference{{Name: "gtw"}}
			err := c.Create(ctx, &aigv1b1.AIGatewayRoute{
				Name: route, Namespace: "default",
				Spec: aigv1b1.AIGatewayRouteSpec{
					ParentRefs: parentRefs,
					Rules: []aigv1b1.AIGatewayRouteRule{
						{
							Matches: []aigv1b1.AIGatewayRouteRuleMatch{
								{
									Headers: []gwapiv1.HTTPHeaderMatch{
										{
											Name:  internalapi.ModelNameHeaderKeyDefault,
											Value: "foo",
										},
									},
								},
							},
							BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
								{Name: "backend1", Weight: ptr.To[int32](1)},
								{Name: "backend2", Weight: ptr.To[int32](1)},
							},
						},
					},
				},
			})
			require.NoError(t, err)
		}
	})

	for _, route := range []string{"route1", "route2"} {
		t.Run("verify ai gateway route "+route, func(t *testing.T) {
			require.Eventually(t, func() bool {
				var aiGatewayRoute aigv1b1.AIGatewayRoute
				err := c.Get(ctx, client.ObjectKey{Name: route, Namespace: "default"}, &aiGatewayRoute)
				if err != nil {
					t.Logf("failed to get route %s: %v", route, err)
					return false
				}

				require.Len(t, aiGatewayRoute.Spec.Rules, 1)
				require.Len(t, aiGatewayRoute.Spec.Rules[0].BackendRefs, 2)

				require.Equal(t, "backend1", aiGatewayRoute.Spec.Rules[0].BackendRefs[0].Name)
				require.Equal(t, "backend2", aiGatewayRoute.Spec.Rules[0].BackendRefs[1].Name)
				return true
			}, 30*time.Second, 200*time.Millisecond)
		})
	}

	for _, backend := range []string{"backend1", "backend2", "backend3", "backend4"} {
		t.Run("verify backend "+backend, func(t *testing.T) {
			require.Eventually(t, func() bool {
				var aiBackend aigv1b1.AIServiceBackend
				err := c.Get(ctx, client.ObjectKey{Name: backend, Namespace: "default"}, &aiBackend)
				if err != nil {
					t.Logf("failed to get backend %s: %v", backend, err)
					return false
				}
				require.Equal(t, "default", aiBackend.Namespace)
				require.Equal(t, backend, aiBackend.Name)
				return true
			}, 30*time.Second, 200*time.Millisecond)
		})
	}

	for _, route := range []string{"route1", "route2"} {
		t.Run("verify http route "+route, func(t *testing.T) {
			require.Eventually(t, func() bool {
				var httpRoute gwapiv1.HTTPRoute
				err := c.Get(ctx, client.ObjectKey{Name: route, Namespace: "default"}, &httpRoute)
				if err != nil {
					t.Logf("failed to get http route %s: %v", route, err)
					return false
				}
				require.Len(t, httpRoute.Spec.Rules, 2) // 1 for rule, 1 for the default rule.
				require.Len(t, httpRoute.Spec.Rules[0].Matches, 1)
				require.Len(t, httpRoute.Spec.Rules[0].Matches[0].Headers, 1)
				require.Equal(t, internalapi.ModelNameHeaderKeyDefault, string(httpRoute.Spec.Rules[0].Matches[0].Headers[0].Name))
				require.Equal(t, "foo", httpRoute.Spec.Rules[0].Matches[0].Headers[0].Value)

				// Check all rule has the host rewrite filter except for the last rule.
				for _, rule := range httpRoute.Spec.Rules[:len(httpRoute.Spec.Rules)-1] {
					require.Len(t, rule.Filters, 1)
					require.NotNil(t, rule.Filters[0].ExtensionRef)
					require.Equal(t, fmt.Sprintf("ai-eg-host-rewrite-%s", route), string(rule.Filters[0].ExtensionRef.Name))
				}
				// Check all rules have root-prefix as the path.
				for _, rule := range httpRoute.Spec.Rules {
					p := rule.Matches[0].Path
					require.NotNil(t, p)
					require.Equal(t, "/root-prefix", *p.Value)
				}
				return true
			}, 30*time.Second, 200*time.Millisecond)
		})
	}
}

func TestNamespaceScopedCache(t *testing.T) {
	_, cfg, _ := testsinternal.NewEnvTest(t)
	opts := controller.Options{EnableLeaderElection: false, DisableMutatingWebhook: true}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: controller.Scheme,
		Cache: cache.Options{
			// Only watch the "test" namespace, where the test will be creating resources.
			// Also initialize the cache with an unexisting namespace to verify it doesn't fail.
			DefaultNamespaces: map[string]cache.Config{"test": {}, "unexisting": {}},
			DefaultTransform:  cache.TransformStripManagedFields(),
		},
		Controller:     config.Controller{SkipNameValidation: new(true)},
		LeaderElection: false,
	})
	require.NoError(t, err)

	ctx := t.Context()
	go func() {
		startErr := controller.StartControllers(ctx, mgr, cfg, defaultLogger(), &opts)
		require.NoError(t, startErr)
	}()

	// Create one backend in the "default" namespace and one in the "test" namespace.
	// Only the one in the "test" namespace should be seen by the manager Client as the
	// other namespace is not watched.

	require.NoError(t, mgr.GetClient().Create(ctx, &corev1.Namespace{
		Name: "test",
	}))
	require.NoError(t, mgr.GetClient().Create(ctx, &aigv1b1.AIServiceBackend{
		Name: "backend1", Namespace: "default",
		Spec: aigv1b1.AIServiceBackendSpec{
			APISchema: defaultSchema,
			BackendRef: gwapiv1.BackendObjectReference{
				Name:  "backend1",
				Kind:  ptr.To(gwapiv1.Kind("Backend")),
				Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
			},
		},
	}))
	require.NoError(t, mgr.GetClient().Create(ctx, &aigv1b1.AIServiceBackend{
		Name: "backend2", Namespace: "test",
		Spec: aigv1b1.AIServiceBackendSpec{
			APISchema: defaultSchema,
			BackendRef: gwapiv1.BackendObjectReference{
				Name:  "backend2",
				Kind:  ptr.To(gwapiv1.Kind("Backend")),
				Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
			},
		},
	}))

	// Verify that the object in the cached namespace is seen.
	require.Eventually(t, func() bool {
		var aiBackend aigv1b1.AIServiceBackend
		err = mgr.GetClient().Get(ctx, client.ObjectKey{Name: "backend2", Namespace: "test"}, &aiBackend)
		if err != nil {
			t.Logf("failed to get backend: %v", err)
			return false
		}
		require.Equal(t, "test", aiBackend.Namespace)
		require.Equal(t, "backend2", aiBackend.Name)
		return true
	}, 30*time.Second, 200*time.Millisecond)

	// Verify that objects in uncached namespaces aren't seen or can't be retrieved due to the cache
	// failing on an unknown namespace.
	require.Never(t, func() bool {
		var backends aigv1b1.AIServiceBackendList
		err = mgr.GetClient().List(ctx, &backends, client.InNamespace("default"))
		return (err != nil && !strings.Contains(err.Error(), "unknown namespace")) || len(backends.Items) > 0
	}, 5*time.Second, 200*time.Millisecond)
}

func TestAIGatewayRouteController(t *testing.T) {
	c, cfg, k := testsinternal.NewEnvTest(t)

	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	rc := controller.NewAIGatewayRouteController(c, k, defaultLogger(), eventCh.Ch, "/foobar/")

	opt := ctrl.Options{Scheme: c.Scheme(), LeaderElection: false, Controller: config.Controller{SkipNameValidation: new(true)}}
	mgr, err := ctrl.NewManager(cfg, opt)
	require.NoError(t, err)

	err = controller.TypedControllerBuilderForCRD(mgr, &aigv1b1.AIGatewayRoute{}).Complete(rc)
	require.NoError(t, err)

	const gatewayName = "gtw"
	// Create the Gateway to be referenced by the AIGatewayRoute.
	err = c.Create(t.Context(), &gwapiv1.Gateway{
		Name: gatewayName, Namespace: "default",
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "gwclass",
			Listeners: []gwapiv1.Listener{
				{Name: "listener1", Port: 8080, Protocol: "http"},
			},
		},
	})
	require.NoError(t, err)

	origin := &aigv1b1.AIGatewayRoute{
		Name: "myroute", Namespace: "default",
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1a2.ParentReference{
				{
					Name:  gatewayName,
					Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
					Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{
					Matches: []aigv1b1.AIGatewayRouteRuleMatch{},
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "backend1", Weight: ptr.To[int32](1)},
						{Name: "backend2", Weight: ptr.To[int32](1)},
					},
				},
			},
		},
	}

	for _, b := range []string{"backend1", "backend2"} {
		err := c.Create(t.Context(), &aigv1b1.AIServiceBackend{
			Name: b, Namespace: "default",
			Spec: aigv1b1.AIServiceBackendSpec{
				APISchema: defaultSchema,
				BackendRef: gwapiv1.BackendObjectReference{
					Name:  gwapiv1.ObjectName(b),
					Kind:  ptr.To(gwapiv1.Kind("Backend")),
					Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				},
			},
		})
		require.NoError(t, err)
	}

	// Start the manager and wait for resources to be cached before trigger a reconciler.
	go func() { require.NoError(t, mgr.Start(t.Context())) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(t.Context()))

	t.Run("create route", func(t *testing.T) {
		err := c.Create(t.Context(), origin)
		require.NoError(t, err)

		var r aigv1b1.AIGatewayRoute
		err = c.Get(t.Context(), client.ObjectKey{Name: "myroute", Namespace: "default"}, &r)
		require.NoError(t, err)
		require.Equal(t, origin, &r)

		events := eventCh.RequireItemsEventually(t, 1)
		require.Equal(t, gatewayName, events[0].Name)
		hostRewriteKey := client.ObjectKey{
			Name:      "ai-eg-host-rewrite-myroute",
			Namespace: "default",
		}
		require.Eventually(t, func() bool {
			var f egv1a1.HTTPRouteFilter
			err = c.Get(t.Context(), hostRewriteKey, &f)
			if err != nil {
				t.Logf("expected to get hostRewriteFilter %s, but got error: %v", hostRewriteKey.Name, err)
				return false
			}
			ok, _ := ctrlutil.HasOwnerReference(f.OwnerReferences, origin, c.Scheme())
			require.True(t, ok, "expected hostRewriteFilter to have owner reference to AIGatewayRoute")
			return true
		}, 10*time.Second, 200*time.Millisecond)
		notFoundKey := client.ObjectKey{
			Name:      "ai-eg-route-not-found-response-myroute",
			Namespace: "default",
		}
		require.Eventually(t, func() bool {
			var f egv1a1.HTTPRouteFilter
			err = c.Get(t.Context(), notFoundKey, &f)
			if err != nil {
				t.Logf("expected to get notFoundFilter %s, but got error: %v", notFoundKey.Name, err)
				return false
			}
			ok, _ := ctrlutil.HasOwnerReference(f.OwnerReferences, origin, c.Scheme())
			require.True(t, ok, "expected notFoundFilter to have owner reference to AIGatewayRoute")
			return true
		}, 10*time.Second, 200*time.Millisecond)
	})

	t.Run("update", func(t *testing.T) {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var r aigv1b1.AIGatewayRoute
			if err := c.Get(t.Context(), types.NamespacedName{Name: "myroute", Namespace: "default"}, &r); err != nil {
				return err
			}
			// Update LLMRequestCosts to test route updates
			r.Spec.LLMRequestCosts = []aigv1b1.LLMRequestCost{
				{
					MetadataKey: "llm_input_token",
					Type:        aigv1b1.LLMRequestCostTypeInputToken,
				},
				{
					MetadataKey: "llm_output_token",
					Type:        aigv1b1.LLMRequestCostTypeOutputToken,
				},
			}
			return c.Update(t.Context(), &r)
		})
		require.NoError(t, err)
	})

	t.Run("check finalizer and status", func(t *testing.T) {
		require.Eventually(t, func() bool {
			var r aigv1b1.AIGatewayRoute
			err := c.Get(t.Context(), client.ObjectKey{Name: "myroute", Namespace: "default"}, &r)
			require.NoError(t, err)
			// Check if the finalizer is set.
			if len(r.Finalizers) == 0 || r.Finalizers[0] != "aigateway.envoyproxy.io/finalizer" {
				t.Logf("expected finalizer not found: %v", r.Finalizers)
				return false
			}
			if len(r.Status.Conditions) != 1 {
				return false
			}
			return r.Status.Conditions[0].Type == aigv1b1.ConditionTypeAccepted
		}, 30*time.Second, 200*time.Millisecond)
	})

	t.Run("delete route", func(t *testing.T) {
		err := c.Delete(t.Context(), origin)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var r aigv1b1.AIGatewayRoute
			err = c.Get(t.Context(), client.ObjectKey{Name: "myroute", Namespace: "default"}, &r)
			if err == nil || client.IgnoreNotFound(err) != nil {
				t.Logf("expected not found error, got: %v", err)
				return false
			}
			// On deletion, the event should be sent to the event channel to propagate the deletion to the Gateway.
			events := eventCh.RequireItemsEventually(t, 1)
			require.Equal(t, gatewayName, events[0].Name)
			return true
		}, 30*time.Second, 200*time.Millisecond)
	})
}

func TestBackendSecurityPolicyController(t *testing.T) {
	c, cfg, k := testsinternal.NewEnvTest(t)

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIServiceBackend]()
	eventChPool := internaltesting.NewControllerEventChan[*gwaiev1.InferencePool]()
	opt := ctrl.Options{Scheme: c.Scheme(), LeaderElection: false, Controller: config.Controller{SkipNameValidation: new(true)}}
	mgr, err := ctrl.NewManager(cfg, opt)
	require.NoError(t, err)
	require.NoError(t, controller.ApplyIndexing(t.Context(), mgr.GetFieldIndexer().IndexField))

	pc := controller.NewBackendSecurityPolicyController(mgr.GetClient(), k, defaultLogger(), eventCh.Ch, eventChPool.Ch)
	err = controller.TypedControllerBuilderForCRD(mgr, &aigv1b1.BackendSecurityPolicy{}).Complete(pc)
	require.NoError(t, err)

	const backendSecurityPolicyName, backendSecurityPolicyNamespace = "bsp", "default"

	originals := []*aigv1b1.AIServiceBackend{
		{
			Name: "backend1", Namespace: backendSecurityPolicyNamespace,
			Spec: aigv1b1.AIServiceBackendSpec{
				APISchema: defaultSchema,
				BackendRef: gwapiv1.BackendObjectReference{
					Name:  gwapiv1.ObjectName("mybackend"),
					Kind:  ptr.To(gwapiv1.Kind("Backend")),
					Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				},
			},
		},
		{
			Name: "backend2", Namespace: backendSecurityPolicyNamespace,
			Spec: aigv1b1.AIServiceBackendSpec{
				APISchema: defaultSchema,
				BackendRef: gwapiv1.BackendObjectReference{
					Name:  gwapiv1.ObjectName("mybackend"),
					Kind:  ptr.To(gwapiv1.Kind("Backend")),
					Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				},
			},
		},
	}
	for _, backend := range originals {
		require.NoError(t, c.Create(t.Context(), backend))
	}

	// Start the manager and wait for banckends to be cached before trigger a reconciler.
	go func() { require.NoError(t, mgr.Start(t.Context())) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(t.Context()))

	t.Run("create security policy", func(t *testing.T) {
		origin := &aigv1b1.BackendSecurityPolicy{
			Name:      backendSecurityPolicyName,
			Namespace: backendSecurityPolicyNamespace,
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name: "secret",
					},
				},
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{
						Name:  "backend1",
						Kind:  gwapiv1a2.Kind("AIServiceBackend"),
						Group: "aigateway.envoyproxy.io",
					},
					{
						Name:  "backend2",
						Kind:  gwapiv1a2.Kind("AIServiceBackend"),
						Group: "aigateway.envoyproxy.io",
					},
				},
			},
		}
		require.NoError(t, c.Create(t.Context(), origin))
		// Verify that they are the same.
		backends := eventCh.RequireItemsEventually(t, 2)
		slices.SortFunc(backends, func(a, b *aigv1b1.AIServiceBackend) int {
			a.TypeMeta = metav1.TypeMeta{}
			b.TypeMeta = metav1.TypeMeta{}
			return cmp.Compare(a.Name, b.Name)
		})
		require.Equal(t, originals, backends)
	})

	t.Run("update security policy", func(t *testing.T) {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			origin := aigv1b1.BackendSecurityPolicy{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: backendSecurityPolicyName, Namespace: backendSecurityPolicyNamespace}, &origin))
			origin.Spec.APIKey = nil
			origin.Spec.Type = aigv1b1.BackendSecurityPolicyTypeAWSCredentials

			origin.Spec.AWSCredentials = &aigv1b1.BackendSecurityPolicyAWSCredentials{
				Region: "us-east-1",
				CredentialsFile: &aigv1b1.AWSCredentialsFile{
					SecretRef: &gwapiv1.SecretObjectReference{
						Name:      "secret",
						Namespace: ptr.To[gwapiv1.Namespace](backendSecurityPolicyNamespace),
					},
				},
			}
			return c.Update(t.Context(), &origin)
		})
		require.NoError(t, err)

		// Verify that they are the same.
		backends := eventCh.RequireItemsEventually(t, 2)
		slices.SortFunc(backends, func(a, b *aigv1b1.AIServiceBackend) int {
			a.TypeMeta = metav1.TypeMeta{}
			b.TypeMeta = metav1.TypeMeta{}
			return cmp.Compare(a.Name, b.Name)
		})
		require.Equal(t, originals, backends)
	})

	t.Run("check finalizer and status", func(t *testing.T) {
		require.Eventually(t, func() bool {
			var r aigv1b1.BackendSecurityPolicy
			err = c.Get(t.Context(), client.ObjectKey{Name: backendSecurityPolicyName, Namespace: backendSecurityPolicyNamespace}, &r)
			require.NoError(t, err)
			// Check if the finalizer is set.
			if len(r.Finalizers) == 0 || r.Finalizers[0] != "aigateway.envoyproxy.io/finalizer" {
				t.Logf("expected finalizer not found: %v", r.Finalizers)
				return false
			}
			if len(r.Status.Conditions) != 1 {
				return false
			}
			return r.Status.Conditions[0].Type == aigv1b1.ConditionTypeAccepted
		}, 30*time.Second, 200*time.Millisecond)
	})

	t.Run("delete bsp", func(t *testing.T) {
		err = c.Delete(t.Context(), &aigv1b1.BackendSecurityPolicy{
			Name:      backendSecurityPolicyName,
			Namespace: backendSecurityPolicyNamespace,
		})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var bsp aigv1b1.BackendSecurityPolicy
			err = c.Get(t.Context(), client.ObjectKey{Name: backendSecurityPolicyName, Namespace: backendSecurityPolicyNamespace}, &bsp)
			if err == nil || client.IgnoreNotFound(err) != nil {
				t.Logf("expected not found error, got: %v", err)
				return false
			}
			// On deletion, the event should be sent to the event channel to propagate the deletion to the Gateway.
			backends := eventCh.RequireItemsEventually(t, 2)
			slices.SortFunc(backends, func(a, b *aigv1b1.AIServiceBackend) int {
				a.TypeMeta = metav1.TypeMeta{}
				b.TypeMeta = metav1.TypeMeta{}
				return cmp.Compare(a.Name, b.Name)
			})
			require.Equal(t, originals, backends)
			return true
		}, 30*time.Second, 200*time.Millisecond)
	})
}

func TestAIServiceBackendController(t *testing.T) {
	c, cfg, k := testsinternal.NewEnvTest(t)

	eventCh := internaltesting.NewControllerEventChan[*aigv1b1.AIGatewayRoute]()

	opt := ctrl.Options{Scheme: c.Scheme(), LeaderElection: false, Controller: config.Controller{SkipNameValidation: new(true)}}
	mgr, err := ctrl.NewManager(cfg, opt)
	require.NoError(t, err)
	require.NoError(t, controller.ApplyIndexing(t.Context(), mgr.GetFieldIndexer().IndexField))

	bc := controller.NewAIServiceBackendController(mgr.GetClient(), k, defaultLogger(), eventCh.Ch)
	err = controller.TypedControllerBuilderForCRD(mgr, &aigv1b1.AIServiceBackend{}).Complete(bc)
	require.NoError(t, err)

	const aiServiceBackendName, aiServiceBackendNamespace = "mybackend", "default"
	// Create an AIGatewayRoute to be referenced by the AIServiceBackend.
	originals := []*aigv1b1.AIGatewayRoute{
		{
			Name: "myroute", Namespace: aiServiceBackendNamespace,
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: []gwapiv1a2.ParentReference{
					{
						Name:  "gtw",
						Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
						Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
					},
				},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						Matches:     []aigv1b1.AIGatewayRouteRuleMatch{{}},
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: aiServiceBackendName}},
					},
				},
			},
		},
		{
			Name: "myroute2", Namespace: aiServiceBackendNamespace,
			Spec: aigv1b1.AIGatewayRouteSpec{
				ParentRefs: []gwapiv1a2.ParentReference{
					{
						Name:  "gtw",
						Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
						Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
					},
				},
				Rules: []aigv1b1.AIGatewayRouteRule{
					{
						Matches:     []aigv1b1.AIGatewayRouteRuleMatch{{}},
						BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: aiServiceBackendName}},
					},
				},
			},
		},
	}
	for _, route := range originals {
		require.NoError(t, c.Create(t.Context(), route))
	}

	// Start the manager and wait for routes to be cached before trigger a reconciler.
	go func() { require.NoError(t, mgr.Start(t.Context())) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(t.Context()))

	t.Run("create backend", func(t *testing.T) {
		origin := &aigv1b1.AIServiceBackend{
			Name: aiServiceBackendName, Namespace: aiServiceBackendNamespace,
			Spec: aigv1b1.AIServiceBackendSpec{
				APISchema: defaultSchema,
				BackendRef: gwapiv1.BackendObjectReference{
					Name:  gwapiv1.ObjectName("mybackend"),
					Kind:  ptr.To(gwapiv1.Kind("Backend")),
					Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
				},
			},
		}
		err = c.Create(t.Context(), origin)
		require.NoError(t, err)

		// Verify that they are the same.
		routes := eventCh.RequireItemsEventually(t, 2)
		slices.SortFunc(routes, func(a, b *aigv1b1.AIGatewayRoute) int {
			a.TypeMeta = metav1.TypeMeta{}
			b.TypeMeta = metav1.TypeMeta{}
			return cmp.Compare(a.Name, b.Name)
		})
		require.Equal(t, originals, routes)
	})

	t.Run("update backend", func(t *testing.T) {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var origin aigv1b1.AIServiceBackend
			err = c.Get(t.Context(), client.ObjectKey{Name: aiServiceBackendName, Namespace: aiServiceBackendNamespace}, &origin)
			require.NoError(t, err)
			origin.Spec.BackendRef.Name = "mybackend-updated"
			return c.Update(t.Context(), &origin)
		})
		require.NoError(t, err)
		// Verify that they are the same.
		routes := eventCh.RequireItemsEventually(t, 2)
		slices.SortFunc(routes, func(a, b *aigv1b1.AIGatewayRoute) int {
			a.TypeMeta = metav1.TypeMeta{}
			b.TypeMeta = metav1.TypeMeta{}
			return cmp.Compare(a.Name, b.Name)
		})
		require.Equal(t, originals, routes)
	})

	t.Run("check finalizer and status", func(t *testing.T) {
		require.Eventually(t, func() bool {
			var r aigv1b1.AIServiceBackend
			err = c.Get(t.Context(), client.ObjectKey{Name: aiServiceBackendName, Namespace: aiServiceBackendNamespace}, &r)
			require.NoError(t, err)
			// Check if the finalizer is set.
			if len(r.Finalizers) == 0 || r.Finalizers[0] != "aigateway.envoyproxy.io/finalizer" {
				t.Logf("expected finalizer not found: %v", r.Finalizers)
				return false
			}

			if len(r.Status.Conditions) != 1 {
				return false
			}
			return r.Status.Conditions[0].Type == aigv1b1.ConditionTypeAccepted
		}, 30*time.Second, 200*time.Millisecond)
	})

	t.Run("delete backend", func(t *testing.T) {
		err = c.Delete(t.Context(), &aigv1b1.AIServiceBackend{
			Name:      aiServiceBackendName,
			Namespace: aiServiceBackendNamespace,
		})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var r aigv1b1.AIServiceBackend
			err = c.Get(t.Context(), client.ObjectKey{Name: aiServiceBackendName, Namespace: aiServiceBackendNamespace}, &r)
			if err == nil || client.IgnoreNotFound(err) != nil {
				t.Logf("expected not found error, got: %v", err)
				return false
			}
			// On deletion, the event should be sent to the event channel to propagate the deletion to the Gateway.
			routes := eventCh.RequireItemsEventually(t, 2)
			slices.SortFunc(routes, func(a, b *aigv1b1.AIGatewayRoute) int {
				a.TypeMeta = metav1.TypeMeta{}
				b.TypeMeta = metav1.TypeMeta{}
				return cmp.Compare(a.Name, b.Name)
			})
			require.Equal(t, originals, routes)
			return true
		}, 30*time.Second, 200*time.Millisecond)
	})
}

func TestSecretController(t *testing.T) {
	c, cfg, k := testsinternal.NewEnvTest(t)

	opt := ctrl.Options{Scheme: c.Scheme(), LeaderElection: false, Controller: config.Controller{SkipNameValidation: new(true)}}
	mgr, err := ctrl.NewManager(cfg, opt)
	require.NoError(t, err)

	bspCh := internaltesting.NewControllerEventChan[*aigv1b1.BackendSecurityPolicy]()
	mcpRouteCh := internaltesting.NewControllerEventChan[*aigv1b1.MCPRoute]()
	sc := controller.NewSecretController(mgr.GetClient(), k, defaultLogger(), bspCh.Ch, mcpRouteCh.Ch)
	const secretName, secretNamespace = "mysecret", "default"

	err = ctrl.NewControllerManagedBy(mgr).For(&corev1.Secret{}).Complete(sc)
	require.NoError(t, err)
	require.NoError(t, controller.ApplyIndexing(t.Context(), mgr.GetFieldIndexer().IndexField))

	// Create a bsp that references the secret.
	originals := []*aigv1b1.BackendSecurityPolicy{
		{
			Name: "mybsp", Namespace: "default",
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type:   aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: secretName}},
			},
		},
		{
			Name: "mybsp2", Namespace: "default",
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
				AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{
					Region:          "us-west-2",
					CredentialsFile: &aigv1b1.AWSCredentialsFile{SecretRef: &gwapiv1.SecretObjectReference{Name: secretName}},
				},
			},
		},
	}
	for _, bsp := range originals {
		require.NoError(t, c.Create(t.Context(), bsp))
	}
	slices.SortFunc(originals, func(a, b *aigv1b1.BackendSecurityPolicy) int {
		return cmp.Compare(a.Name, b.Name)
	})

	// Start the manager and wait for bsps to be cached before trigger a reconciler.
	go func() { require.NoError(t, mgr.Start(t.Context())) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(t.Context()))

	t.Run("create secret", func(t *testing.T) {
		err = c.Create(t.Context(), &corev1.Secret{
			Name: secretName, Namespace: secretNamespace,
			StringData: map[string]string{"key": "value"},
		})
		require.NoError(t, err)

		// Verify that they are the same.
		bsps := bspCh.RequireItemsEventually(t, 2)
		slices.SortFunc(bsps, func(a, b *aigv1b1.BackendSecurityPolicy) int {
			a.TypeMeta = metav1.TypeMeta{}
			b.TypeMeta = metav1.TypeMeta{}
			return cmp.Compare(a.Name, b.Name)
		})
		require.Equal(t, originals, bsps)
	})

	t.Run("update secret", func(t *testing.T) {
		err = c.Update(t.Context(), &corev1.Secret{
			Name: "mysecret", Namespace: "default",
			StringData: map[string]string{"key": "value2"},
		})
		require.NoError(t, err)

		// Verify that they are the same.
		bsps := bspCh.RequireItemsEventually(t, 2)
		slices.SortFunc(bsps, func(a, b *aigv1b1.BackendSecurityPolicy) int {
			a.TypeMeta = metav1.TypeMeta{}
			b.TypeMeta = metav1.TypeMeta{}
			return cmp.Compare(a.Name, b.Name)
		})
		require.Equal(t, originals, bsps)
	})
}

func defaultLogger() logr.Logger {
	return logr.FromSlogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))
}
