// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestInferencePoolFailOpen tests that an InferencePool whose endpoint picker has failureMode
// FailOpen keeps serving requests from the pool's endpoints while the endpoint picker is down,
// that a client cannot choose the upstream while it is down, and that FailClose is unchanged.
//
// The subtests are ordered steps on one shared cluster (the endpoint picker is scaled down
// part-way through), so they cannot be run individually with -run.
func TestInferencePoolFailOpen(t *testing.T) {
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/base.yaml"))
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/aigwroute.yaml"))

	const (
		pool            = "mistral"
		eppDeployment   = "mistral-epp"
		eppSelector     = "app=mistral-epp"
		fallbackService = "mistral-epp-fallback"
		egSelector      = "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-aigwroute"
		// An address outside the pool (TEST-NET-1) that nothing serves: a request that reaches it
		// can only be the result of the destination header being honoured.
		spoofedEndpoint = "192.0.2.1:8080"
		body            = `{"model":"mistral:latest","messages":[{"role":"user","content":"Say this is a test"}]}`
	)
	e2elib.RequireWaitForGatewayPodReadyWithNamespace(t, inferenceGatewayNamespace, egSelector)

	setFailureMode := func(mode string) {
		patch := `{"spec":{"endpointPickerRef":{"failureMode":"` + mode + `"}}}`
		require.NoError(t, e2elib.Kubectl(t.Context(), "patch", "inferencepool", pool,
			"-n", inferenceGatewayNamespace, "--type=merge", "-p", patch).Run())
	}
	scaleEPP := func(ctx context.Context, replicas string) error {
		return e2elib.Kubectl(ctx, "scale", "deployment", eppDeployment,
			"-n", inferenceGatewayNamespace, "--replicas="+replicas).Run()
	}
	fallbackServiceExists := func() bool {
		return e2elib.Kubectl(t.Context(), "get", "service", fallbackService, "-n", inferenceGatewayNamespace).Run() == nil
	}

	fwd := e2elib.RequireNewHTTPPortForwarder(t, inferenceGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	t.Cleanup(fwd.Kill)
	post := func(headers map[string]string) (int, error) {
		return postChatCompletion(t, fwd, body, headers)
	}
	requireEventuallyStatus := func(t *testing.T, headers map[string]string, accept func(code int) bool, msg string) {
		require.Eventually(t, func() bool {
			code, err := post(headers)
			if err != nil {
				t.Logf("request failed: %v", err)
				return false
			}
			t.Logf("status %d", code)
			return accept(code)
		}, 2*time.Minute, 2*time.Second, msg)
	}
	isOK := func(code int) bool { return code == http.StatusOK }

	// Leave the shared cluster as other tests expect it: EPP running, pool FailClose.
	t.Cleanup(func() {
		ctx := context.Background()
		_ = e2elib.Kubectl(ctx, "patch", "inferencepool", pool, "-n", inferenceGatewayNamespace,
			"--type=merge", "-p", `{"spec":{"endpointPickerRef":{"failureMode":"FailClose"}}}`).Run()
		_ = scaleEPP(ctx, "1")
		// t.Context() is already canceled here, so don't use the helpers that rely on it.
		_ = e2elib.Kubectl(ctx, "rollout", "status", "deployment/"+eppDeployment,
			"-n", inferenceGatewayNamespace, "--timeout=3m").Run()
	})

	setFailureMode("FailOpen")

	t.Run("fallback_service_is_created", func(t *testing.T) {
		require.Eventually(t, fallbackServiceExists, time.Minute, time.Second, "fallback Service should be created for a FailOpen pool")
	})

	t.Run("serves_with_endpoint_picker_up", func(t *testing.T) {
		requireEventuallyStatus(t, nil, isOK, "FailOpen pool should serve requests while the endpoint picker is up")
	})

	// Take the endpoint picker down for the next two subtests.
	require.NoError(t, scaleEPP(t.Context(), "0"))
	require.NoError(t, e2elib.Kubectl(t.Context(), "wait", "--for=delete", "pod", "-l", eppSelector,
		"-n", inferenceGatewayNamespace, "--timeout=3m").Run())

	t.Run("serves_without_endpoint_picker", func(t *testing.T) {
		requireEventuallyStatus(t, nil, isOK, "FailOpen pool should serve requests from its endpoints while the endpoint picker is down")
		for i := range 10 {
			code, err := post(nil)
			require.NoError(t, err, "request %d", i)
			require.Equal(t, http.StatusOK, code, "request %d", i)
		}
	})

	t.Run("client_destination_header_is_ignored", func(t *testing.T) {
		for i := range 5 {
			code, err := post(map[string]string{"x-gateway-destination-endpoint": spoofedEndpoint})
			require.NoError(t, err, "request %d", i)
			require.Equal(t, http.StatusOK, code, "request %d: the client must not be able to choose the upstream", i)
		}
	})

	t.Run("fail_close_still_fails_without_endpoint_picker", func(t *testing.T) {
		setFailureMode("FailClose")
		requireEventuallyStatus(t, nil, func(code int) bool { return code >= http.StatusInternalServerError },
			"FailClose pool should fail requests while the endpoint picker is down")
		require.Eventually(t, func() bool { return !fallbackServiceExists() }, time.Minute, time.Second,
			"fallback Service should be deleted when the pool is FailClose")
	})
}
