// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestOTELTracingWithConsoleExporter verifies that OTEL environment variables
// can be configured via Helm and are properly injected into extProc containers.
func TestOTELTracingWithConsoleExporter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	// Get the source directory relative to this test file.
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to get source file path")
	sourceDir := filepath.Dir(filename)

	helmChartPath := filepath.Join(sourceDir, "..", "..", "manifests", "charts", "ai-gateway-helm")
	manifest := filepath.Join(sourceDir, "testdata", "otel_tracing_console.yaml")

	// Upgrade existing AI Gateway installation with OTEL_TRACES_EXPORTER=console.
	t.Log("Upgrading AI Gateway with OTEL_TRACES_EXPORTER=console")

	// Upgrade the existing "ai-eg" release with new env vars.
	helm := testsinternal.GoToolCmdContext(ctx, "helm", "upgrade", "ai-eg", "--force",
		helmChartPath,
		"--set", "controller.spanRequestHeaderAttributes=x-tenant-id:"+tenantIDAttribute,
		"--set", "controller.metricsRequestHeaderAttributes=x-tenant-id:"+tenantIDAttribute,
		"--set", "controller.logRequestHeaderAttributes=x-tenant-id:"+tenantIDAttribute,
		"--set", "extProc.extraEnvVars[0].name=OTEL_TRACES_EXPORTER",
		"--set", "extProc.extraEnvVars[0].value=console",
		"--set", "extProc.extraEnvVars[1].name=OTEL_SERVICE_NAME",
		"--set", "extProc.extraEnvVars[1].value=ai-gateway-e2e-test",
		"-n", "envoy-ai-gateway-system")

	helm.Stdout = os.Stdout
	helm.Stderr = os.Stderr
	require.NoError(t, helm.Run(), "Failed to upgrade AI Gateway with OTEL env vars")

	// Setup cleanup to restore original configuration after test.
	t.Cleanup(func() {
		t.Log("Restoring original AI Gateway configuration")
		restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// Restore the release values from before the test, including suite-wide settings.
		output, err := testsinternal.RunGoToolContext(restoreCtx, "helm", "rollback", "ai-eg",
			"-n", "envoy-ai-gateway-system", "--wait", "--timeout", "90s")
		if err != nil {
			t.Errorf("Failed to restore the AI Gateway release: %v\n%s", err, output)
		}

		// Clean up the test manifest resources including namespace.
		_ = e2elib.KubectlDeleteManifest(restoreCtx, manifest)

		// Delete the test namespace to clean up completely.
		deleteNs := exec.CommandContext(restoreCtx, "kubectl", "delete", "namespace",
			"otel-test-namespace", "--ignore-not-found=true")
		_ = deleteNs.Run()
	})

	// Restart controller to pick up new configuration.
	restartCmd := exec.CommandContext(ctx, "kubectl", "rollout", "restart",
		"deployment/ai-gateway-controller", "-n", "envoy-ai-gateway-system")
	require.NoError(t, restartCmd.Run())

	// Wait for deployment to be ready.
	waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--timeout=2m",
		"-n", "envoy-ai-gateway-system", "deployment/ai-gateway-controller", "--for=condition=available")
	require.NoError(t, waitCmd.Run())

	// Apply the test manifest which will trigger pod creation.
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))

	// Get the pod with extProc container and verify env vars.
	t.Log("Checking extProc container for OTEL environment variables")

	// Get pod name from envoy-gateway-system namespace (where pods are created).
	require.Eventually(t, func() bool {
		const egSelector = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-otel-test"
		getPodsCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", // #nosec G204
			"-n", e2elib.EnvoyGatewayNamespace,
			"-l", egSelector,
			"-o", "jsonpath={.items[0].metadata.name}")

		var podNameBytes []byte
		podNameBytes, err := getPodsCmd.Output()
		if err != nil {
			t.Logf("Failed to get pod name: %v", err)
			return false // Retry if command fails.
		}
		podName := string(podNameBytes)
		if len(podName) == 0 {
			t.Log("No pods found with the specified selector, retrying...")
			return false // Retry if no pods found.
		}
		t.Logf("Found pod: %s", podName)

		// Get the pod description to check env vars.
		describeCmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName,
			"-n", e2elib.EnvoyGatewayNamespace,
			"-o", "jsonpath={.spec.initContainers[?(@.name=='ai-gateway-extproc')].env}")

		describeOutput := &bytes.Buffer{}
		describeCmd.Stdout = describeOutput
		describeCmd.Stderr = describeOutput

		err = describeCmd.Run()
		if err != nil {
			t.Logf("Failed to describe pod %s: %v", podName, err)
			return false // Retry if command fails.
		}

		envVars := describeOutput.String()
		t.Logf("Environment variables in extProc container: %s", envVars)

		// Get the container args to check header attributes configuration.
		argsCmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName,
			"-n", e2elib.EnvoyGatewayNamespace,
			"-o", "jsonpath={.spec.initContainers[?(@.name=='ai-gateway-extproc')].args}")

		argsOutput := &bytes.Buffer{}
		argsCmd.Stdout = argsOutput
		argsCmd.Stderr = argsOutput

		err = argsCmd.Run()
		if err != nil {
			t.Logf("Failed to get container args for pod %s: %v", podName, err)
			return false // Retry if command fails.
		}

		containerArgs := argsOutput.String()
		t.Logf("Container args in extProc container: %s", containerArgs)

		defer func() {
			// Deletes the pods to ensure they are recreated with the new configuration for the next iteration.
			deletePodsCmd := e2elib.Kubectl(ctx, "delete", "pod", podName,
				"-n", e2elib.EnvoyGatewayNamespace,
				"--ignore-not-found=true")
			err = deletePodsCmd.Run()
			if err != nil {
				t.Logf("Failed to delete pod %s: %v", podName, err)
			}
		}()

		// Verify that our OTEL env vars are present in the pod spec.
		if !strings.Contains(envVars, `"name":"OTEL_TRACES_EXPORTER","value":"console"`) {
			t.Log("Expected OTEL_TRACES_EXPORTER=console in extProc container spec")
			return false
		}
		if !strings.Contains(envVars, `"name":"OTEL_SERVICE_NAME","value":"ai-gateway-e2e-test"`) {
			t.Log("Expected OTEL_SERVICE_NAME=ai-gateway-e2e-test in extProc container spec")
			return false
		}

		// Verify that pre-upgrade header attribute args are present in the container args.
		if !strings.Contains(containerArgs, "-metricsRequestHeaderAttributes") || !strings.Contains(containerArgs, "x-tenant-id:"+tenantIDAttribute) {
			t.Log("Expected -metricsRequestHeaderAttributes x-tenant-id:" + tenantIDAttribute + " in extProc container args")
			return false
		}
		if !strings.Contains(containerArgs, "-spanRequestHeaderAttributes") || !strings.Contains(containerArgs, "x-tenant-id:"+tenantIDAttribute) {
			t.Log("Expected -spanRequestHeaderAttributes x-tenant-id:" + tenantIDAttribute + " in extProc container args")
			return false
		}
		if !strings.Contains(containerArgs, "-logRequestHeaderAttributes") || !strings.Contains(containerArgs, "x-tenant-id:"+tenantIDAttribute) {
			t.Log("Expected -logRequestHeaderAttributes x-tenant-id:" + tenantIDAttribute + " in extProc container args")
			return false
		}

		return true
	}, 2*time.Minute, 5*time.Second)

	t.Log("OTEL environment variables and header attribute args successfully verified in extProc container")
}

// TestOTELTracingWithGatewayConfig verifies that OTEL environment variables
// can be configured via GatewayConfig and are properly injected into extProc containers.
// This test uses the comprehensive example from examples/gateway-config/comprehensive.yaml.
func TestOTELTracingWithGatewayConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	const manifest = "../../examples/gateway-config/comprehensive.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	// Get the pod with extProc container and verify GatewayConfig env vars.
	t.Log("Checking extProc container for GatewayConfig OTEL environment variables")

	// Get pod name from envoy-gateway-system namespace (where pods are created).
	require.Eventually(t, func() bool {
		const egSelector = "gateway.envoyproxy.io/owning-gateway-name=production-ai-gateway"
		getPodsCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", // #nosec G204
			"-n", e2elib.EnvoyGatewayNamespace,
			"-l", egSelector,
			"-o", "jsonpath={.items[0].metadata.name}")

		var podNameBytes []byte
		podNameBytes, err := getPodsCmd.Output()
		if err != nil {
			t.Logf("Failed to get pod name: %v", err)
			return false // Retry if command fails.
		}
		podName := strings.TrimSpace(string(podNameBytes))
		if len(podName) == 0 {
			t.Log("No pods found with the specified selector, retrying...")
			return false // Retry if no pods found.
		}
		t.Logf("Found pod: %s", podName)

		// Get the pod description to check env vars.
		describeCmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName,
			"-n", e2elib.EnvoyGatewayNamespace,
			"-o", "jsonpath={.spec.initContainers[?(@.name=='ai-gateway-extproc')].env}")

		describeOutput := &bytes.Buffer{}
		describeCmd.Stdout = describeOutput
		describeCmd.Stderr = describeOutput

		err = describeCmd.Run()
		if err != nil {
			t.Logf("Failed to describe pod %s: %v", podName, err)
			return false // Retry if command fails.
		}

		envVars := describeOutput.String()
		t.Logf("Environment variables in extProc container: %s", envVars)

		// Get the container resources to verify GatewayConfig resources are applied.
		resourcesCmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName,
			"-n", e2elib.EnvoyGatewayNamespace,
			"-o", "jsonpath={.spec.initContainers[?(@.name=='ai-gateway-extproc')].resources}")

		resourcesOutput := &bytes.Buffer{}
		resourcesCmd.Stdout = resourcesOutput
		resourcesCmd.Stderr = resourcesOutput

		err = resourcesCmd.Run()
		if err != nil {
			t.Logf("Failed to get container resources for pod %s: %v", podName, err)
			return false // Retry if command fails.
		}

		resources := resourcesOutput.String()
		t.Logf("Resources in extProc container: %s", resources)

		defer func() {
			// Deletes the pods to ensure they are recreated with the new configuration for the next iteration.
			deletePodsCmd := e2elib.Kubectl(ctx, "delete", "pod", podName,
				"-n", e2elib.EnvoyGatewayNamespace,
				"--ignore-not-found=true")
			err = deletePodsCmd.Run()
			if err != nil {
				t.Logf("Failed to delete pod %s: %v", podName, err)
			}
		}()

		// Verify that GatewayConfig OTEL env vars are present in the pod spec.
		expectedEnvVars := []struct {
			name  string
			value string
		}{
			{"OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.monitoring:4317"},
			{"OTEL_EXPORTER_OTLP_HEADERS", "api-key=your-secret-key"},
			{"OTEL_SERVICE_NAME", "ai-gateway-production"},
			{"OTEL_TRACES_SAMPLER", "parentbased_traceidratio"},
			{"OTEL_TRACES_SAMPLER_ARG", "0.1"},
			{"LOG_LEVEL", "info"},
			{"ENABLE_DEBUG_METRICS", "false"},
		}

		for _, expected := range expectedEnvVars {
			expectedStr := `"name":"` + expected.name + `","value":"` + expected.value + `"`
			if !strings.Contains(envVars, expectedStr) {
				t.Logf("Expected %s=%s in extProc container spec", expected.name, expected.value)
				return false
			}
		}

		// Verify that GatewayConfig resources are applied.
		if !strings.Contains(resources, `"cpu":"100m"`) {
			t.Log("Expected CPU request 100m from GatewayConfig")
			return false
		}
		if !strings.Contains(resources, `"memory":"128Mi"`) {
			t.Log("Expected Memory request 128Mi from GatewayConfig")
			return false
		}
		if !strings.Contains(resources, `"cpu":"500m"`) {
			t.Log("Expected CPU limit 500m from GatewayConfig")
			return false
		}
		if !strings.Contains(resources, `"memory":"512Mi"`) {
			t.Log("Expected Memory limit 512Mi from GatewayConfig")
			return false
		}

		return true
	}, 2*time.Minute, 5*time.Second)

	t.Log("GatewayConfig OTEL environment variables and resources successfully verified in extProc container")
}
