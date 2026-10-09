// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// testGatewayConfig is a GatewayConfig used for testing.
var testGatewayConfig = &aigv1b1.GatewayConfig{
	ObjectMeta: metav1.ObjectMeta{
		Name: "test-gateway-config",
	},
	Spec: aigv1b1.GatewayConfigSpec{
		ExtProc: &aigv1b1.GatewayConfigExtProc{
			Kubernetes: &egv1a1.KubernetesContainerSpec{
				Image: ptr.To("gcr.io/custom/extproc:v2"),
				Env: []corev1.EnvVar{
					{Name: "LOG_LEVEL", Value: "debug"}, // Overrides global
					{Name: "CONFIG_VAR", Value: "config-value"},
				},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
		},
	},
}

func TestGatewayMutator_Default(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	fakeKube := fake2.NewClientset()
	g := newTestGatewayMutator(fakeClient, fakeKube, nil, nil, nil, nil, "", "", "", false)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-namespace"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "envoy"}},
		},
	}
	err := fakeClient.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gateway", Namespace: "test-namespace"},
		Spec:       aigv1b1.AIGatewayRouteSpec{},
	})
	require.NoError(t, err)
	err = g.Default(t.Context(), pod)
	require.NoError(t, err)
}

func TestGatewayMutator_mutatePod(t *testing.T) {
	tests := []struct {
		name                           string
		requestHeaderAttributes        *string
		spanRequestHeaderAttributes    *string
		metricsRequestHeaderAttributes *string
		logRequestHeaderAttributes     *string
		endpointPrefixes               string
		extProcExtraEnvVars            string
		extProcImagePullSecrets        string
		extprocTest                    func(t *testing.T, container corev1.Container)
		podTest                        func(t *testing.T, pod corev1.Pod)
		needMCP                        bool
		gatewayConfig                  *aigv1b1.GatewayConfig
	}{
		{
			name: "basic extproc container",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Empty(t, container.Env)
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
				var foundBundle bool
				for i := range pod.Spec.Volumes {
					v := pod.Spec.Volumes[i]
					if !strings.HasSuffix(v.Name, "-bundle") {
						continue
					}
					foundBundle = true
					require.NotNil(t, v.Projected)
					require.Len(t, v.Projected.Sources, maxFilterConfigBundleSlots+1) // index + fixed slots
					for j, src := range v.Projected.Sources {
						if j == 0 {
							require.Nil(t, src.Secret.Optional) // index secret is not optional
							continue
						}
						require.NotNil(t, src.Secret.Optional) // parital secrets are optional
						require.True(t, *src.Secret.Optional)
					}
				}
				require.True(t, foundBundle)
			},
		},
		{
			name:    "basic extproc container with MCPRoute",
			needMCP: true,
			extprocTest: func(t *testing.T, container corev1.Container) {
				var foundMCPAddr, foundMCPSeed, foundMCPSIterations, foundFallbackSeed, foundFallbackIterations bool
				for i, arg := range container.Args {
					switch arg {
					case "-mcpAddr":
						foundMCPAddr = true
						require.Equal(t, ":"+strconv.Itoa(internalapi.MCPProxyPort), container.Args[i+1])
					case "-mcpSessionEncryptionSeed":
						foundMCPSeed = true
						require.Equal(t, "seed", container.Args[i+1])
					case "-mcpSessionEncryptionIterations":
						foundMCPSIterations = true
						require.Equal(t, "100", container.Args[i+1])
					case "-mcpFallbackSessionEncryptionSeed":
						foundFallbackSeed = true
						require.Equal(t, "fallback", container.Args[i+1])
					case "-mcpFallbackSessionEncryptionIterations":
						foundFallbackIterations = true
						require.Equal(t, "200", container.Args[i+1])
					}
				}
				require.True(t, foundMCPAddr)
				require.True(t, foundMCPSeed)
				require.True(t, foundMCPSIterations)
				require.True(t, foundFallbackSeed)
				require.True(t, foundFallbackIterations)
			},
		},
		{
			name:             "with endpoint prefixes",
			endpointPrefixes: "openai:/v1,cohere:/cohere/v2,anthropic:/anthropic/v1",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Contains(t, container.Args, "-endpointPrefixes")
				require.Contains(t, container.Args, "openai:/v1,cohere:/cohere/v2,anthropic:/anthropic/v1")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                "with extra env vars",
			extProcExtraEnvVars: "OTEL_SERVICE_NAME=ai-gateway-extproc;OTEL_TRACES_EXPORTER=otlp",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Equal(t, []corev1.EnvVar{
					{Name: "OTEL_SERVICE_NAME", Value: "ai-gateway-extproc"},
					{Name: "OTEL_TRACES_EXPORTER", Value: "otlp"},
				}, container.Env)
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                           "with metrics request header labels",
			metricsRequestHeaderAttributes: strPtr("x-tenant-id:tenant.id,x-tenant-id:tenant.id"),
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Empty(t, container.Env)
				require.Contains(t, container.Args, "-metricsRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-tenant-id:tenant.id,x-tenant-id:tenant.id")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                    "with base request header attributes",
			requestHeaderAttributes: strPtr("x-tenant-id:tenant.id"),
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Contains(t, container.Args, "-requestHeaderAttributes")
				require.Contains(t, container.Args, "x-tenant-id:tenant.id")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                           "with both metrics and env vars",
			metricsRequestHeaderAttributes: strPtr("x-tenant-id:tenant.id"),
			extProcExtraEnvVars:            "OTEL_SERVICE_NAME=custom-service",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Equal(t, []corev1.EnvVar{
					{Name: "OTEL_SERVICE_NAME", Value: "custom-service"},
				}, container.Env)
				require.Contains(t, container.Args, "-metricsRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-tenant-id:tenant.id")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                        "with tracing request header attributes",
			spanRequestHeaderAttributes: strPtr("x-forwarded-proto:url.scheme"),
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Empty(t, container.Env)
				require.Contains(t, container.Args, "-spanRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-forwarded-proto:url.scheme")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                        "with explicit empty span and log header attributes",
			spanRequestHeaderAttributes: strPtr(""),
			logRequestHeaderAttributes:  strPtr(""),
			extprocTest: func(t *testing.T, container corev1.Container) {
				var spanValue, logValue *string
				for i, arg := range container.Args {
					if arg == "-spanRequestHeaderAttributes" && i+1 < len(container.Args) {
						value := container.Args[i+1]
						spanValue = &value
					}
					if arg == "-logRequestHeaderAttributes" && i+1 < len(container.Args) {
						value := container.Args[i+1]
						logValue = &value
					}
				}
				require.NotNil(t, spanValue)
				require.NotNil(t, logValue)
				require.Empty(t, *spanValue)
				require.Empty(t, *logValue)
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                       "with access log request header attributes",
			logRequestHeaderAttributes: strPtr("x-forwarded-proto:url.scheme"),
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Empty(t, container.Env)
				require.Contains(t, container.Args, "-logRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-forwarded-proto:url.scheme")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                           "with metrics, tracing, and env vars",
			spanRequestHeaderAttributes:    strPtr("x-forwarded-proto:url.scheme"),
			metricsRequestHeaderAttributes: strPtr("x-tenant-id:tenant.id"),
			logRequestHeaderAttributes:     strPtr("x-forwarded-proto:url.scheme"),
			extProcExtraEnvVars:            "OTEL_SERVICE_NAME=test-service",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Equal(t, []corev1.EnvVar{
					{Name: "OTEL_SERVICE_NAME", Value: "test-service"},
				}, container.Env)
				require.Contains(t, container.Args, "-metricsRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-tenant-id:tenant.id")
				require.Contains(t, container.Args, "-spanRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-forwarded-proto:url.scheme")
				require.Contains(t, container.Args, "-logRequestHeaderAttributes")
				require.Contains(t, container.Args, "x-forwarded-proto:url.scheme")
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				require.Empty(t, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                    "with image pull secrets",
			extProcImagePullSecrets: "my-registry-secret;backup-secret",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Empty(t, container.Env)
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				expectedSecrets := []corev1.LocalObjectReference{
					{Name: "my-registry-secret"},
					{Name: "backup-secret"},
				}
				require.Equal(t, expectedSecrets, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                    "with image pull secrets and env vars",
			extProcExtraEnvVars:     "OTEL_SERVICE_NAME=test-service",
			extProcImagePullSecrets: "my-registry-secret",
			extprocTest: func(t *testing.T, container corev1.Container) {
				require.Equal(t, []corev1.EnvVar{
					{Name: "OTEL_SERVICE_NAME", Value: "test-service"},
				}, container.Env)
			},
			podTest: func(t *testing.T, pod corev1.Pod) {
				expectedSecrets := []corev1.LocalObjectReference{
					{Name: "my-registry-secret"},
				}
				require.Equal(t, expectedSecrets, pod.Spec.ImagePullSecrets)
			},
		},
		{
			name:                "with GatewayConfig",
			extProcExtraEnvVars: "GLOBAL_VAR=global-value;LOG_LEVEL=info",
			gatewayConfig:       testGatewayConfig,
			extprocTest: func(t *testing.T, container corev1.Container) {
				// GatewayConfig env vars override global env vars
				require.Equal(t, []corev1.EnvVar{
					{Name: "GLOBAL_VAR", Value: "global-value"},
					{Name: "LOG_LEVEL", Value: "debug"}, // GatewayConfig overrides global
					{Name: "CONFIG_VAR", Value: "config-value"},
				}, container.Env)
				// GatewayConfig image override
				require.Equal(t, "gcr.io/custom/extproc:v2", container.Image)
				// GatewayConfig resources
				require.NotNil(t, container.Resources)
				require.NotNil(t, container.Resources.Requests)
				cpuReq := container.Resources.Requests[corev1.ResourceCPU]
				memReq := container.Resources.Requests[corev1.ResourceMemory]
				require.Equal(t, resource.MustParse("200m"), cpuReq)
				require.Equal(t, resource.MustParse("256Mi"), memReq)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, sidecar := range []bool{true, false} {
				t.Run(fmt.Sprintf("sidecar=%v", sidecar), func(t *testing.T) {
					fakeClient := requireNewFakeClientWithIndexes(t)
					fakeKube := fake2.NewClientset()
					g := newTestGatewayMutator(fakeClient, fakeKube, tt.requestHeaderAttributes, tt.spanRequestHeaderAttributes, tt.metricsRequestHeaderAttributes, tt.logRequestHeaderAttributes, tt.endpointPrefixes, tt.extProcExtraEnvVars, tt.extProcImagePullSecrets, sidecar)

					const gwName, gwNamespace = "test-gateway", "test-namespace"
					err := fakeClient.Create(t.Context(), &aigv1b1.AIGatewayRoute{
						ObjectMeta: metav1.ObjectMeta{Name: gwName, Namespace: gwNamespace},
						Spec: aigv1b1.AIGatewayRouteSpec{
							ParentRefs: []gwapiv1a2.ParentReference{
								{
									Name:  gwName,
									Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
									Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
								},
							},
							Rules: []aigv1b1.AIGatewayRouteRule{
								{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}}},
							},
						},
					})
					require.NoError(t, err)

					if tt.needMCP {
						err = fakeClient.Create(t.Context(), &aigv1b1.MCPRoute{
							ObjectMeta: metav1.ObjectMeta{Name: "test-mcp", Namespace: gwNamespace},
							Spec: aigv1b1.MCPRouteSpec{
								ParentRefs: []gwapiv1.ParentReference{
									{
										Name:  gwapiv1.ObjectName(gwName),
										Kind:  ptr.To(gwapiv1.Kind("Gateway")),
										Group: ptr.To(gwapiv1.Group("gateway.networking.k8s.io")),
									},
								},
							},
						})
						require.NoError(t, err)
					}

					// Create Gateway and GatewayConfig if configured for this test case
					if tt.gatewayConfig != nil {
						// Create GatewayConfig
						gatewayConfig := tt.gatewayConfig.DeepCopy()
						gatewayConfig.Namespace = gwNamespace
						err = fakeClient.Create(t.Context(), gatewayConfig)
						require.NoError(t, err)

						// Create Gateway with GatewayConfig annotation
						err = fakeClient.Create(t.Context(), &gwapiv1.Gateway{
							ObjectMeta: metav1.ObjectMeta{
								Name:      gwName,
								Namespace: gwNamespace,
								Annotations: map[string]string{
									GatewayConfigAnnotationKey: tt.gatewayConfig.Name,
								},
							},
							Spec: gwapiv1.GatewaySpec{
								GatewayClassName: "test-class",
							},
						})
						require.NoError(t, err)
					}

					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-namespace"},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "envoy"}},
						},
					}

					// At this point, the config secret does not exist, so the pod should not be mutated.
					err = g.mutatePod(t.Context(), pod, gwName, gwNamespace)
					require.NoError(t, err)
					require.Len(t, pod.Spec.Containers, 1)

					// Create the config secret and mutate the pod again.
					indexRaw, idxErr := filterapi.MarshalConfigBundleIndex(&filterapi.ConfigBundleIndex{
						Checksum: "abc",
						Parts: []filterapi.ConfigBundlePart{
							{
								Name: FilterConfigBundleIndexSecretName(gwName, gwNamespace) + "-part-000",
								Path: "parts/000",
							},
						},
					})
					require.NoError(t, idxErr)
					_, err = g.kube.CoreV1().Secrets("test-namespace").Create(t.Context(),
						&corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{Name: FilterConfigBundleIndexSecretName(
								gwName, gwNamespace,
							), Namespace: "test-namespace"},
							Data: map[string][]byte{
								FilterConfigBundleIndexKey: indexRaw,
							},
						}, metav1.CreateOptions{})
					require.NoError(t, err)
					_, err = g.kube.CoreV1().Secrets("test-namespace").Create(t.Context(),
						&corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{
								Name:      FilterConfigBundleIndexSecretName(gwName, gwNamespace) + "-part-000",
								Namespace: "test-namespace",
							},
							Data: map[string][]byte{FilterConfigBundlePartKey: []byte("version: dev\n")},
						}, metav1.CreateOptions{})
					require.NoError(t, err)
					err = g.mutatePod(t.Context(), pod, gwName, gwNamespace)
					require.NoError(t, err)

					var extProcContainer corev1.Container
					if sidecar {
						require.Len(t, pod.Spec.Containers, 1)
						require.Len(t, pod.Spec.InitContainers, 1)
						extProcContainer = pod.Spec.InitContainers[0]
						require.NotNil(t, extProcContainer.RestartPolicy)
						require.Equal(t, corev1.ContainerRestartPolicyAlways, *extProcContainer.RestartPolicy)
					} else {
						require.Len(t, pod.Spec.Containers, 2)
						extProcContainer = pod.Spec.Containers[1]
					}

					require.Equal(t, "ai-gateway-extproc", extProcContainer.Name)
					require.Contains(t, extProcContainer.Args, "-configBundlePath")
					tt.extprocTest(t, extProcContainer)
					if tt.podTest != nil {
						tt.podTest(t, *pod)
					}
				})
			}
		})
	}
}

func TestGatewayMutator_mutatePod_BundleOnly(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexes(t)
	fakeKube := fake2.NewClientset()
	g := newTestGatewayMutator(fakeClient, fakeKube, nil, nil, nil, nil, "", "", "", false)

	const gwName, gwNamespace = "test-gateway", "test-namespace"
	err := fakeClient.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: gwName, Namespace: gwNamespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1a2.ParentReference{
				{
					Name:  gwName,
					Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
					Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "apple"}}}},
		},
	})
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: gwNamespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
	}

	indexRaw, idxErr := filterapi.MarshalConfigBundleIndex(&filterapi.ConfigBundleIndex{
		Checksum: "abc",
		Parts: []filterapi.ConfigBundlePart{
			{Name: FilterConfigBundleIndexSecretName(gwName, gwNamespace) + "-part-000", Path: "parts/000"},
		},
	})
	require.NoError(t, idxErr)
	_, err = g.kube.CoreV1().Secrets(gwNamespace).Create(t.Context(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: FilterConfigBundleIndexSecretName(gwName, gwNamespace), Namespace: gwNamespace},
			Data:       map[string][]byte{FilterConfigBundleIndexKey: indexRaw},
		}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = g.kube.CoreV1().Secrets(gwNamespace).Create(t.Context(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: FilterConfigBundleIndexSecretName(gwName, gwNamespace) + "-part-000", Namespace: gwNamespace},
			Data:       map[string][]byte{FilterConfigBundlePartKey: []byte("version: dev\n")},
		}, metav1.CreateOptions{})
	require.NoError(t, err)

	err = g.mutatePod(t.Context(), pod, gwName, gwNamespace)
	require.NoError(t, err)
	require.Len(t, pod.Spec.Containers, 2)

	extProcContainer := pod.Spec.Containers[1]
	require.Contains(t, extProcContainer.Args, "-configBundlePath")
}

func strPtr(value string) *string {
	return &value
}

func newTestGatewayMutator(fakeClient client.Client, fakeKube *fake2.Clientset, requestHeaderAttributes, spanRequestHeaderAttributes, metricsRequestHeaderAttributes, logRequestHeaderAttributes *string, endpointPrefixes, extProcExtraEnvVars, extProcImagePullSecrets string, sidecar bool) *gatewayMutator {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true, Level: zapcore.DebugLevel})))
	opts := &Options{
		ExtProcImage:                           "docker.io/envoyproxy/ai-gateway-extproc:latest",
		ExtProcImagePullPolicy:                 corev1.PullIfNotPresent,
		ExtProcLogLevel:                        "info",
		ExtProcEnableRedaction:                 false,
		UDSPath:                                "/tmp/extproc.sock",
		RequestHeaderAttributes:                requestHeaderAttributes,
		TracingRequestHeaderAttributes:         spanRequestHeaderAttributes,
		MetricsRequestHeaderAttributes:         metricsRequestHeaderAttributes,
		LogRequestHeaderAttributes:             logRequestHeaderAttributes,
		RootPrefix:                             "/v1",
		EndpointPrefixes:                       endpointPrefixes,
		ExtProcExtraEnvVars:                    extProcExtraEnvVars,
		ExtProcImagePullSecrets:                extProcImagePullSecrets,
		ExtProcMaxRecvMsgSize:                  512 * 1024 * 1024,
		MCPSessionEncryptionSeed:               "seed",
		MCPSessionEncryptionIterations:         100,
		MCPFallbackSessionEncryptionSeed:       "fallback",
		MCPFallbackSessionEncryptionIterations: 200,
	}
	return newGatewayMutator(fakeClient, fakeClient, fakeKube, ctrl.Log, newExtProcBuilder(opts, sidecar, ctrl.Log))
}

// defaultTestExtProcBuilder returns an extProcBuilder with the standard test
// defaults used by the no-cache-reader mutator tests (which need a distinct
// noCacheReader and therefore call newGatewayMutator directly).
func defaultTestExtProcBuilder() *extProcBuilder {
	opts := &Options{
		ExtProcImage:                           "docker.io/envoyproxy/ai-gateway-extproc:latest",
		ExtProcImagePullPolicy:                 corev1.PullIfNotPresent,
		ExtProcLogLevel:                        "info",
		ExtProcEnableRedaction:                 false,
		UDSPath:                                "/tmp/extproc.sock",
		RootPrefix:                             "/v1",
		ExtProcMaxRecvMsgSize:                  512 * 1024 * 1024,
		MCPSessionEncryptionSeed:               "seed",
		MCPSessionEncryptionIterations:         100,
		MCPFallbackSessionEncryptionSeed:       "fallback",
		MCPFallbackSessionEncryptionIterations: 200,
	}
	return newExtProcBuilder(opts, false, ctrl.Log)
}

func TestParseExtraEnvVars(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      []corev1.EnvVar
		wantError string
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "single env var",
			input: "OTEL_SERVICE_NAME=ai-gateway",
			want: []corev1.EnvVar{
				{Name: "OTEL_SERVICE_NAME", Value: "ai-gateway"},
			},
		},
		{
			name:  "multiple env vars",
			input: "OTEL_SERVICE_NAME=ai-gateway;OTEL_TRACES_EXPORTER=otlp",
			want: []corev1.EnvVar{
				{Name: "OTEL_SERVICE_NAME", Value: "ai-gateway"},
				{Name: "OTEL_TRACES_EXPORTER", Value: "otlp"},
			},
		},
		{
			name:  "env var with comma in value",
			input: "OTEL_RESOURCE_ATTRIBUTES=service.name=gateway,service.version=1.0",
			want: []corev1.EnvVar{
				{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: "service.name=gateway,service.version=1.0"},
			},
		},
		{
			name:  "multiple env vars with commas",
			input: "OTEL_RESOURCE_ATTRIBUTES=service.name=gateway,service.version=1.0;OTEL_TRACES_EXPORTER=otlp",
			want: []corev1.EnvVar{
				{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: "service.name=gateway,service.version=1.0"},
				{Name: "OTEL_TRACES_EXPORTER", Value: "otlp"},
			},
		},
		{
			name:  "env var with equals in value",
			input: "CONFIG=key1=value1",
			want: []corev1.EnvVar{
				{Name: "CONFIG", Value: "key1=value1"},
			},
		},
		{
			name:  "trailing semicolon",
			input: "OTEL_SERVICE_NAME=ai-gateway;",
			want: []corev1.EnvVar{
				{Name: "OTEL_SERVICE_NAME", Value: "ai-gateway"},
			},
		},
		{
			name:  "spaces around values",
			input: " OTEL_SERVICE_NAME = ai-gateway ; OTEL_TRACES_EXPORTER = otlp ",
			want: []corev1.EnvVar{
				{Name: "OTEL_SERVICE_NAME", Value: " ai-gateway"},
				{Name: "OTEL_TRACES_EXPORTER", Value: " otlp"},
			},
		},
		{
			name:  "only semicolons",
			input: ";;;",
			want:  nil,
		},
		{
			name:      "missing equals",
			input:     "OTEL_SERVICE_NAME",
			wantError: "invalid env var pair at position 1: \"OTEL_SERVICE_NAME\" (expected format: KEY=value)",
		},
		{
			name:      "empty key",
			input:     "=value",
			wantError: "empty env var name at position 1: \"=value\"",
		},
		{
			name:      "mixed valid and invalid",
			input:     "VALID=value;INVALID;ANOTHER=value",
			wantError: "invalid env var pair at position 2: \"INVALID\" (expected format: KEY=value)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExtraEnvVars(tt.input)
			if tt.wantError != "" {
				require.Error(t, err)
				require.Equal(t, tt.wantError, err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestParseImagePullSecrets(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      []corev1.LocalObjectReference
		wantError string
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "single secret",
			input: "my-registry-secret",
			want:  []corev1.LocalObjectReference{{Name: "my-registry-secret"}},
		},
		{
			name:  "multiple secrets",
			input: "my-registry-secret;backup-secret;third-secret",
			want: []corev1.LocalObjectReference{
				{Name: "my-registry-secret"},
				{Name: "backup-secret"},
				{Name: "third-secret"},
			},
		},
		{
			name:  "secrets with spaces",
			input: " my-registry-secret ; backup-secret ",
			want: []corev1.LocalObjectReference{
				{Name: "my-registry-secret"},
				{Name: "backup-secret"},
			},
		},
		{
			name:  "trailing semicolon",
			input: "my-registry-secret;backup-secret;",
			want: []corev1.LocalObjectReference{
				{Name: "my-registry-secret"},
				{Name: "backup-secret"},
			},
		},
		{
			name:  "only semicolons",
			input: ";;;",
			want:  nil,
		},
		{
			name:  "empty secret names",
			input: "my-secret;;backup-secret",
			want: []corev1.LocalObjectReference{
				{Name: "my-secret"},
				{Name: "backup-secret"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseImagePullSecrets(tt.input)
			if tt.wantError != "" {
				require.Error(t, err)
				require.Equal(t, tt.wantError, err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestGatewayMutator_mergeEnvVars(t *testing.T) {
	tests := []struct {
		name          string
		globalEnvVars string
		gatewayConfig *aigv1b1.GatewayConfig
		expectedEnvs  map[string]string
	}{
		{
			name:          "global env vars only",
			globalEnvVars: "GLOBAL_VAR=global-value;LOG_LEVEL=info",
			gatewayConfig: nil,
			expectedEnvs: map[string]string{
				"GLOBAL_VAR": "global-value",
				"LOG_LEVEL":  "info",
			},
		},
		{
			name:          "GatewayConfig env vars only",
			globalEnvVars: "",
			gatewayConfig: &aigv1b1.GatewayConfig{
				Spec: aigv1b1.GatewayConfigSpec{
					ExtProc: &aigv1b1.GatewayConfigExtProc{
						Kubernetes: &egv1a1.KubernetesContainerSpec{
							Env: []corev1.EnvVar{
								{Name: "CONFIG_VAR", Value: "config-value"},
								{Name: "LOG_LEVEL", Value: "debug"},
							},
						},
					},
				},
			},
			expectedEnvs: map[string]string{
				"CONFIG_VAR": "config-value",
				"LOG_LEVEL":  "debug",
			},
		},
		{
			name:          "GatewayConfig overrides global",
			globalEnvVars: "LOG_LEVEL=info;GLOBAL_ONLY=global",
			gatewayConfig: &aigv1b1.GatewayConfig{
				Spec: aigv1b1.GatewayConfigSpec{
					ExtProc: &aigv1b1.GatewayConfigExtProc{
						Kubernetes: &egv1a1.KubernetesContainerSpec{
							Env: []corev1.EnvVar{
								{Name: "LOG_LEVEL", Value: "debug"},
								{Name: "CONFIG_ONLY", Value: "config"},
							},
						},
					},
				},
			},
			expectedEnvs: map[string]string{
				"LOG_LEVEL":   "debug",  // GatewayConfig overrides global
				"GLOBAL_ONLY": "global", // global only
				"CONFIG_ONLY": "config", // config only
			},
		},
		{
			name:          "GatewayConfig with nil ExtProc",
			globalEnvVars: "GLOBAL_VAR=global-value",
			gatewayConfig: &aigv1b1.GatewayConfig{
				Spec: aigv1b1.GatewayConfigSpec{
					ExtProc: nil,
				},
			},
			expectedEnvs: map[string]string{
				"GLOBAL_VAR": "global-value",
			},
		},
		{
			name:          "empty both",
			globalEnvVars: "",
			gatewayConfig: nil,
			expectedEnvs:  map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexes(t)
			fakeKube := fake2.NewClientset()
			g := newTestGatewayMutator(fakeClient, fakeKube, nil, nil, nil, nil, "", tt.globalEnvVars, "", false)

			result := g.mergeEnvVars(tt.gatewayConfig)

			// Convert result to map for easier comparison.
			resultMap := make(map[string]string)
			for _, env := range result {
				resultMap[env.Name] = env.Value
			}

			require.Equal(t, tt.expectedEnvs, resultMap)
		})
	}
}

func TestGatewayMutator_resolveExtProcImage(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		extProc  *aigv1b1.GatewayConfigExtProc
		expected string
	}{
		{
			name:     "nil spec uses base image",
			base:     "docker.io/envoyproxy/ai-gateway-extproc:latest",
			extProc:  nil,
			expected: "docker.io/envoyproxy/ai-gateway-extproc:latest",
		},
		{
			name: "explicit image override",
			base: "docker.io/envoyproxy/ai-gateway-extproc:latest",
			extProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					Image: ptr.To("gcr.io/custom/extproc:v2"),
				},
			},
			expected: "gcr.io/custom/extproc:v2",
		},
		{
			name: "repository override reuses tag",
			base: "docker.io/envoyproxy/ai-gateway-extproc:latest",
			extProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					ImageRepository: ptr.To("gcr.io/custom/extproc"),
				},
			},
			expected: "gcr.io/custom/extproc:latest",
		},
		{
			name: "repository override keeps digest",
			base: "docker.io/envoyproxy/ai-gateway-extproc@sha256:deadbeef",
			extProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					ImageRepository: ptr.To("gcr.io/custom/extproc"),
				},
			},
			expected: "gcr.io/custom/extproc@sha256:deadbeef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &gatewayMutator{extProcBuilder: &extProcBuilder{image: tt.base}}
			require.Equal(t, tt.expected, g.resolveExtProcImage(tt.extProc))
		})
	}
}

func TestGatewayMutator_mutatePod_UsesNoCacheReader(t *testing.T) {
	cacheClient := requireNewFakeClientWithIndexes(t)
	noCacheReader := requireNewFakeClientWithIndexes(t)
	fakeKube := fake2.NewClientset()
	g := newGatewayMutator(cacheClient, noCacheReader, fakeKube, ctrl.Log, defaultTestExtProcBuilder())

	const gwName, gwNamespace = "test-gateway", "test-namespace"
	// Route only in noCacheReader, not in cacheClient — simulates cache not yet synced.
	err := noCacheReader.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: gwNamespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1a2.ParentReference{
				{
					Name:  gwapiv1a2.ObjectName(gwName),
					Kind:  ptr.To(gwapiv1a2.Kind("Gateway")),
					Group: ptr.To(gwapiv1a2.Group("gateway.networking.k8s.io")),
				},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}}},
			},
		},
	})
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: gwNamespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "envoy"}}},
	}

	_, err = g.kube.CoreV1().Secrets(gwNamespace).Create(t.Context(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      FilterConfigBundleIndexSecretName(gwName, gwNamespace),
				Namespace: gwNamespace,
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err)

	err = g.mutatePod(t.Context(), pod, gwName, gwNamespace)
	require.NoError(t, err)
	require.Len(t, pod.Spec.Containers, 2)
	require.Equal(t, extProcContainerName, pod.Spec.Containers[1].Name)
}

func TestGatewayMutator_listAIGatewayRoutesForGateway_NoCacheReaderFallback(t *testing.T) {
	cacheClient := requireNewFakeClientWithIndexes(t)
	noCacheReader := requireNewFakeClientWithIndexes(t)
	fakeKube := fake2.NewClientset()
	g := newGatewayMutator(cacheClient, noCacheReader, fakeKube, ctrl.Log, defaultTestExtProcBuilder())

	const gwName, gwNamespace = "test-gateway", "test-namespace"

	// Matching route in noCacheReader only.
	err := noCacheReader.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-matching", Namespace: gwNamespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{Name: gwapiv1.ObjectName(gwName)},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}}}},
		},
	})
	require.NoError(t, err)

	// Non-matching route — different namespace in parentRef.
	otherNamespace := gwapiv1.Namespace("other")
	err = noCacheReader.Create(t.Context(), &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route-non-matching", Namespace: gwNamespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{Name: gwapiv1.ObjectName(gwName), Namespace: &otherNamespace},
			},
			Rules: []aigv1b1.AIGatewayRouteRule{{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: "backend"}}}},
		},
	})
	require.NoError(t, err)

	routes, err := g.listAIGatewayRoutesForGateway(t.Context(), gwName, gwNamespace)
	require.NoError(t, err)
	require.Len(t, routes.Items, 1)
	require.Equal(t, "route-matching", routes.Items[0].Name)
}

func TestGatewayMutator_listMCPRoutesForGateway_NoCacheReaderFallback(t *testing.T) {
	cacheClient := requireNewFakeClientWithIndexes(t)
	noCacheReader := requireNewFakeClientWithIndexes(t)
	fakeKube := fake2.NewClientset()
	g := newGatewayMutator(cacheClient, noCacheReader, fakeKube, ctrl.Log, defaultTestExtProcBuilder())

	const gwName, gwNamespace = "test-gateway", "test-namespace"

	// Matching MCP route in noCacheReader only.
	err := noCacheReader.Create(t.Context(), &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-matching", Namespace: gwNamespace},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{Name: gwapiv1.ObjectName(gwName)},
			},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				{BackendObjectReference: gwapiv1.BackendObjectReference{Name: gwapiv1.ObjectName("server")}},
			},
		},
	})
	require.NoError(t, err)

	// Non-matching MCP route — different gateway name.
	err = noCacheReader.Create(t.Context(), &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-non-matching", Namespace: gwNamespace},
		Spec: aigv1b1.MCPRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{Name: gwapiv1.ObjectName("other-gw")},
			},
			BackendRefs: []aigv1b1.MCPRouteBackendRef{
				{BackendObjectReference: gwapiv1.BackendObjectReference{Name: gwapiv1.ObjectName("other")}},
			},
		},
	})
	require.NoError(t, err)

	routes, err := g.listMCPRoutesForGateway(t.Context(), gwName, gwNamespace)
	require.NoError(t, err)
	require.Len(t, routes.Items, 1)
	require.Equal(t, "mcp-matching", routes.Items[0].Name)
}
