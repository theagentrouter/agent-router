// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"reflect"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// newTestBuilder returns an extProcBuilder with the standard test defaults. It is
// the single source of truth for the config the webhook injects and the
// reconciler hashes, so the tests below exercise the actual drift signal.
func newTestBuilder() *extProcBuilder {
	opts := &Options{
		ExtProcImage:                           "docker.io/envoyproxy/ai-gateway-extproc:latest",
		ExtProcLogLevel:                        "info",
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

// TestExtProcContainerHash_Deterministic pins the property the whole drift
// mechanism relies on: the workload template hash must be stable for identical
// extproc config. Because both webhook injection and reconciler hashing use the
// same builder, this guards against a second, parallel hash implementation.
func TestExtProcContainerHash_Deterministic(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))
	b := newTestBuilder()

	for _, tc := range []struct {
		name  string
		input extProcContainerInput
	}{
		{name: "no gateway config, no MCP", input: extProcContainerInput{}},
		{name: "need MCP", input: extProcContainerInput{needMCP: true}},
		{name: "with gateway config", input: extProcContainerInput{gatewayConfig: testGatewayConfig}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h1 := b.extProcContainerHash(tc.input)
			require.NotEmpty(t, h1)
			for range 20 {
				require.Equal(t, h1, b.extProcContainerHash(tc.input), "hash must be stable across recomputes")
			}
		})
	}
}

// TestExtProcContainerHash_Drift asserts that every config field the issue
// lists as "silently half-honored" actually moves the hash, so a controller
// restart with a changed flag is detected and triggers a rollout. A change to
// any of these must produce a different hash; otherwise the drift signal is
// broken and the bug persists.
func TestExtProcContainerHash_Drift(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))
	base := newTestBuilder()
	// input has no GatewayConfig and no MCP, so the *global* builder fields
	// (image, mcpSessionEncryptionSeed, …) are the ones exercised. The
	// GatewayConfig-driven fields are exercised separately below.
	input := extProcContainerInput{}
	baseHash := base.extProcContainerHash(input)
	require.NotEmpty(t, baseHash)
	require.Equal(t, 20, reflect.TypeOf(extProcBuilder{}).NumField(),
		"update drift coverage when extProcBuilder fields change")

	// Helper: mutate a copy of the builder, recompute, expect a different hash.
	assertDrifts := func(name string, mutated *extProcBuilder) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, baseHash, mutated.extProcContainerHash(input),
				"%s change must alter the config hash so drift is detected", name)
		})
	}

	assertDrifts("extraEnvVars", func() *extProcBuilder {
		b := newTestBuilder()
		b.extraEnvVars = []corev1.EnvVar{{Name: "OTEL_SERVICE_NAME", Value: "ai-gateway"}}
		return b
	}())
	assertDrifts("imagePullSecrets", func() *extProcBuilder {
		b := newTestBuilder()
		b.imagePullSecrets = []corev1.LocalObjectReference{{Name: "reg-cred"}}
		return b
	}())
	assertDrifts("imagePullPolicy", func() *extProcBuilder {
		b := newTestBuilder()
		b.imagePullPolicy = corev1.PullAlways
		return b
	}())
	assertDrifts("udsPath", func() *extProcBuilder {
		b := newTestBuilder()
		b.udsPath = "/var/run/extproc.sock"
		return b
	}())
	assertDrifts("endpointPrefixes", func() *extProcBuilder {
		b := newTestBuilder()
		b.endpointPrefixes = "openai:/,cohere:/cohere"
		return b
	}())
	assertDrifts("enableRedaction", func() *extProcBuilder {
		b := newTestBuilder()
		b.enableRedaction = true
		return b
	}())
	assertDrifts("maxRecvMsgSize", func() *extProcBuilder {
		b := newTestBuilder()
		b.maxRecvMsgSize = 42
		return b
	}())
	assertDrifts("image", func() *extProcBuilder {
		b := newTestBuilder()
		b.image = "docker.io/envoyproxy/ai-gateway-extproc:v2"
		return b
	}())
	assertDrifts("logLevel", func() *extProcBuilder {
		b := newTestBuilder()
		b.logLevel = "debug"
		return b
	}())
	assertDrifts("logFormat", func() *extProcBuilder {
		b := newTestBuilder()
		b.logFormat = internalapi.LogFormatJSON
		return b
	}())
	assertDrifts("requestHeaderAttributes", func() *extProcBuilder {
		b := newTestBuilder()
		s := "x-tenant-id:tenant.id"
		b.requestHeaderAttributes = &s
		return b
	}())
	assertDrifts("spanRequestHeaderAttributes", func() *extProcBuilder {
		b := newTestBuilder()
		s := "x-trace-id:trace.id"
		b.spanRequestHeaderAttributes = &s
		return b
	}())
	assertDrifts("metricsRequestHeaderAttributes", func() *extProcBuilder {
		b := newTestBuilder()
		s := "x-metric-id:metric.id"
		b.metricsRequestHeaderAttributes = &s
		return b
	}())
	assertDrifts("logRequestHeaderAttributes", func() *extProcBuilder {
		b := newTestBuilder()
		s := "x-log-id:log.id"
		b.logRequestHeaderAttributes = &s
		return b
	}())
	assertDrifts("rootPrefix", func() *extProcBuilder {
		b := newTestBuilder()
		b.rootPrefix = "/custom"
		return b
	}())
	assertDrifts("extProcAsSideCar", func() *extProcBuilder {
		b := newTestBuilder()
		b.extProcAsSideCar = true
		return b
	}())
	// mcpSessionEncryptionSeed only enters the container args when needMCP is
	// true, so verify drift under that input.
	assertDriftsMCP := func(name string, mutated *extProcBuilder) {
		t.Helper()
		mcpInput := extProcContainerInput{needMCP: true}
		require.NotEqual(t, base.extProcContainerHash(mcpInput), mutated.extProcContainerHash(mcpInput),
			"%s change must alter the config hash when MCP is enabled", name)
	}
	assertDriftsMCP("mcpSessionEncryptionSeed", func() *extProcBuilder {
		b := newTestBuilder()
		b.mcpSessionEncryptionSeed = "different-seed"
		return b
	}())
	assertDriftsMCP("mcpSessionEncryptionIterations", func() *extProcBuilder {
		b := newTestBuilder()
		b.mcpSessionEncryptionIterations = 101
		return b
	}())
	assertDriftsMCP("mcpFallbackSessionEncryptionSeed", func() *extProcBuilder {
		b := newTestBuilder()
		b.mcpFallbackSessionEncryptionSeed = "different-fallback"
		return b
	}())
	assertDriftsMCP("mcpFallbackSessionEncryptionIterations", func() *extProcBuilder {
		b := newTestBuilder()
		b.mcpFallbackSessionEncryptionIterations = 201
		return b
	}())

	// GatewayConfig-driven fields move the hash too (resources / securityContext
	// / volumeMounts / env override / image override), proving the reconciler
	// detects edits to GatewayConfig.spec.extProc.kubernetes.*.
	gcInput := extProcContainerInput{gatewayConfig: testGatewayConfig}
	gcBaseHash := base.extProcContainerHash(gcInput)
	t.Run("gatewayConfig resources change", func(t *testing.T) {
		gc := testGatewayConfig.DeepCopy()
		gc.Spec.ExtProc.Kubernetes.Resources = &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("999m")},
		}
		require.NotEqual(t, gcBaseHash, base.extProcContainerHash(extProcContainerInput{gatewayConfig: gc}))
	})
	t.Run("gatewayConfig env override changes", func(t *testing.T) {
		gc := testGatewayConfig.DeepCopy()
		gc.Spec.ExtProc.Kubernetes.Env = []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "error"}}
		require.NotEqual(t, gcBaseHash, base.extProcContainerHash(extProcContainerInput{gatewayConfig: gc}))
	})
	t.Run("gatewayConfig image override changes", func(t *testing.T) {
		gc := testGatewayConfig.DeepCopy()
		gc.Spec.ExtProc.Kubernetes.Image = ptr.To("gcr.io/custom/extproc:v9")
		require.NotEqual(t, gcBaseHash, base.extProcContainerHash(extProcContainerInput{gatewayConfig: gc}))
	})

	// needMCP toggling the MCP args must move the hash.
	require.NotEqual(t, baseHash, base.extProcContainerHash(extProcContainerInput{needMCP: true}),
		"needMCP must alter the config hash")
}

// TestExtProcContainerHash_ExcludesConfigRoutingArgs ensures the secret-
// presence-driven -configBundlePath args are NOT part of the hash
// (they are added by the webhook after buildExtProcContainer returns). The base
// container's Args must contain neither flag, so secret-existence transitions
// do not spuriously trigger rollouts.
func TestExtProcContainerHash_ExcludesConfigRoutingArgs(t *testing.T) {
	b := newTestBuilder()
	input := extProcContainerInput{}
	baseHash := b.extProcContainerHash(input)
	require.NotEmpty(t, baseHash)

	container := b.buildExtProcContainer(input)
	for _, a := range container.Args {
		require.NotEqual(t, "-configBundlePath", a)
	}
	require.Equal(t, baseHash, b.extProcContainerHash(input),
		"secret-presence-driven config routing must not affect the workload template hash")
}

// TestBuildExtProcBaseArgs_LogFormat asserts that -logFormat reaches the extproc container only
// when it differs from the extproc default, so existing deployments keep the args they already have.
func TestBuildExtProcBaseArgs_LogFormat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		logFormat string
	}{
		{name: "unset", logFormat: ""},
		{name: "explicit default", logFormat: internalapi.LogFormatText},
	} {
		t.Run("not passed when "+tc.name, func(t *testing.T) {
			b := newTestBuilder()
			b.logFormat = tc.logFormat
			require.NotContains(t, b.buildExtProcBaseArgs(false), "-logFormat")
		})
	}

	t.Run("passed when json", func(t *testing.T) {
		b := newTestBuilder()
		b.logFormat = internalapi.LogFormatJSON
		args := b.buildExtProcBaseArgs(false)
		require.Equal(t, []string{"-logFormat", internalapi.LogFormatJSON}, args[len(args)-2:])
	})
}

// TestNewExtProcBuilder_MalformedInput covers the defensive parse-error log
// branches in newExtProcBuilder. In production main.go validates these flags
// before StartControllers, but the builder must still degrade gracefully (log +
// skip) rather than panic on malformed input.
func TestNewExtProcBuilder_MalformedInput(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))
	// "badpair" has no '=', so ParseExtraEnvVars errors.
	b := newExtProcBuilder(&Options{
		ExtProcExtraEnvVars:   "badpair",
		ExtProcImage:          "img",
		ExtProcLogLevel:       "info",
		UDSPath:               "/tmp/extproc.sock",
		RootPrefix:            "/",
		ExtProcMaxRecvMsgSize: 512 * 1024 * 1024,
	}, false, ctrl.Log)
	require.Empty(t, b.extraEnvVars, "malformed env var must be skipped, not stored")
}

// TestBuildExtProcContainer_GatewayConfigOverrides covers the GatewayConfig-driven
// container fields that the drift hash must include: a SecurityContext override,
// extra VolumeMounts, Resources, and the resolveExtProcImage default branch
// (Kubernetes set with neither Image nor ImageRepository — falls back to the
// builder's global image).
func TestBuildExtProcContainer_GatewayConfigOverrides(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))
	b := newTestBuilder()
	gc := &aigv1b1.GatewayConfig{
		Spec: aigv1b1.GatewayConfigSpec{
			ExtProc: &aigv1b1.GatewayConfigExtProc{
				Kubernetes: &egv1a1.KubernetesContainerSpec{
					// No Image / ImageRepository → resolveExtProcImage returns the
					// builder's global image (default branch).
					Resources: &corev1.ResourceRequirements{
						Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
					},
					SecurityContext: &corev1.SecurityContext{
						RunAsUser: ptr.To(int64(1000)),
					},
					VolumeMounts: []corev1.VolumeMount{{Name: "extra-mount", MountPath: "/extra"}},
				},
			},
		},
	}
	container := b.buildExtProcContainer(extProcContainerInput{gatewayConfig: gc})
	require.Equal(t, b.image, container.Image, "default branch returns the global image")
	require.Equal(t, int64(1000), *container.SecurityContext.RunAsUser, "SecurityContext override applied")
	require.NotEmpty(t, container.Resources.Limits, "Resources applied")
	var foundExtra bool
	for _, vm := range container.VolumeMounts {
		if vm.Name == "extra-mount" {
			foundExtra = true
		}
	}
	require.True(t, foundExtra, "GatewayConfig VolumeMounts appended")

	// The override fields move the hash relative to no-GatewayConfig.
	baseHash := b.extProcContainerHash(extProcContainerInput{})
	overrideHash := b.extProcContainerHash(extProcContainerInput{gatewayConfig: gc})
	require.NotEqual(t, baseHash, overrideHash)
}

func TestMergeImageWithRepository(t *testing.T) {
	tests := []struct {
		name       string
		baseImage  string
		repository string
		expected   string
	}{
		{name: "empty repository returns base", baseImage: "img:v1", repository: "", expected: "img:v1"},
		{name: "base with no tag returns repository only", baseImage: "img", repository: "gcr.io/custom", expected: "gcr.io/custom"},
		{name: "base with tag reuses tag", baseImage: "img:v1", repository: "gcr.io/custom", expected: "gcr.io/custom:v1"},
		{name: "base with digest reuses digest", baseImage: "img@sha256:abc", repository: "gcr.io/custom", expected: "gcr.io/custom@sha256:abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, mergeImageWithRepository(tt.baseImage, tt.repository))
		})
	}
}

func TestImageTagOrDigest(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		expected string
	}{
		{name: "empty", image: "", expected: ""},
		{name: "digest", image: "img@sha256:abc", expected: "@sha256:abc"},
		{name: "tag after last slash", image: "registry.io/foo:v1", expected: ":v1"},
		{name: "no tag and no digest", image: "registry.io/foo", expected: ""},
		{name: "colon before slash is not a tag", image: "localhost:5000/foo", expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, imageTagOrDigest(tt.image))
		})
	}
}
