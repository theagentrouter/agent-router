// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestInferencePoolFallbackService tests how the controller manages the headless Service that
// backs a FailOpen pool's cluster: it is recreated when deleted, follows the pool's target port,
// and a Service of the same name that the pool doesn't own is left alone.
func TestInferencePoolFallbackService(t *testing.T) {
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/base.yaml"))
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/aigwroute.yaml"))

	const (
		pool            = "mistral"
		fallbackService = "mistral-epp-fallback"
	)
	ns := inferenceGatewayNamespace

	// output runs kubectl and returns its stdout; failures return an empty string.
	output := func(args ...string) string {
		var out bytes.Buffer
		cmd := exec.CommandContext(t.Context(), "kubectl", args...)
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return ""
		}
		return strings.TrimSpace(out.String())
	}
	serviceUID := func() string {
		return output("get", "service", fallbackService, "-n", ns, "-o", "jsonpath={.metadata.uid}")
	}
	servicePorts := func() string {
		return output("get", "service", fallbackService, "-n", ns, "-o", "jsonpath={.spec.ports[*].port}")
	}
	patchPool := func(patch string) {
		require.NoError(t, e2elib.Kubectl(t.Context(), "patch", "inferencepool", pool,
			"-n", ns, "--type=merge", "-p", patch).Run())
	}
	setFailureMode := func(mode string) {
		patchPool(`{"spec":{"endpointPickerRef":{"failureMode":"` + mode + `"}}}`)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_ = e2elib.Kubectl(ctx, "patch", "inferencepool", pool, "-n", ns, "--type=merge",
			"-p", `{"spec":{"targetPorts":[{"number":8080}],"endpointPickerRef":{"failureMode":"FailClose"}}}`).Run()
		_ = e2elib.Kubectl(ctx, "delete", "service", fallbackService, "-n", ns, "--ignore-not-found").Run()
	})

	setFailureMode("FailOpen")
	require.Eventually(t, func() bool { return serviceUID() != "" }, time.Minute, time.Second)
	require.Equal(t, "8080", servicePorts())
	require.Equal(t, "mistral-upstream", output("get", "service", fallbackService, "-n", ns, "-o", "jsonpath={.spec.selector.app}"))
	require.Equal(t, "None", output("get", "service", fallbackService, "-n", ns, "-o", "jsonpath={.spec.clusterIP}"))

	t.Run("deleted_service_is_recreated", func(t *testing.T) {
		before := serviceUID()
		require.NoError(t, e2elib.Kubectl(t.Context(), "delete", "service", fallbackService, "-n", ns).Run())
		require.Eventually(t, func() bool {
			uid := serviceUID()
			return uid != "" && uid != before
		}, time.Minute, time.Second, "the controller should recreate the deleted fallback Service")
	})

	t.Run("target_port_change_is_applied", func(t *testing.T) {
		patchPool(`{"spec":{"targetPorts":[{"number":8081}]}}`)
		require.Eventually(t, func() bool { return servicePorts() == "8081" }, time.Minute, time.Second)
		patchPool(`{"spec":{"targetPorts":[{"number":8080}]}}`)
		require.Eventually(t, func() bool { return servicePorts() == "8080" }, time.Minute, time.Second)
	})

	t.Run("foreign_service_is_not_modified", func(t *testing.T) {
		setFailureMode("FailClose")
		require.Eventually(t, func() bool { return serviceUID() == "" }, time.Minute, time.Second,
			"FailClose deletes the fallback Service the pool owns")

		foreign := `
apiVersion: v1
kind: Service
metadata:
  name: ` + fallbackService + `
  namespace: ` + ns + `
spec:
  selector:
    app: someone-else
  ports:
    - port: 9999
`
		require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), foreign))
		foreignUID := serviceUID()
		require.NotEmpty(t, foreignUID)

		// A FailClose pool never deletes a Service it doesn't own.
		patchPool(`{"spec":{"appProtocol":"kubernetes.io/h2c"}}`)
		require.Never(t, func() bool { return serviceUID() != foreignUID }, 5*time.Second, time.Second)
		patchPool(`{"spec":{"appProtocol":null}}`)

		// FailOpen must not take it over; the pool says why it is not accepted.
		setFailureMode("FailOpen")
		require.Eventually(t, func() bool {
			return strings.Contains(output("get", "inferencepool", pool, "-n", ns,
				"-o", "jsonpath={.status.parents[*].conditions[*].message}"), "is not owned by InferencePool")
		}, time.Minute, time.Second, "the pool should report that the fallback Service isn't its own")
		require.Equal(t, foreignUID, serviceUID())
		require.Equal(t, "9999", servicePorts())
		require.Equal(t, "someone-else", output("get", "service", fallbackService, "-n", ns, "-o", "jsonpath={.spec.selector.app}"))

		// Once the conflicting Service is gone, the pool recovers.
		require.NoError(t, e2elib.Kubectl(t.Context(), "delete", "service", fallbackService, "-n", ns).Run())
		require.Eventually(t, func() bool { return servicePorts() == "8080" }, time.Minute, time.Second)
	})
}
