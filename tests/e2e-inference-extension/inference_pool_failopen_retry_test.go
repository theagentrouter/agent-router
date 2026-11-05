// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestInferencePoolFailOpenRetry tests that a retry to an InferencePool backend leaves the
// endpoint that failed (https://github.com/envoyproxy/ai-gateway/issues/2757).
//
// One member of the mistral pool is Ready but refuses every connection on the pool's target port,
// so any request that is sent to it fails with a connect failure. With a retry policy on the route
// and failureMode FailOpen, a client must never see that failure: the retry goes to another
// endpoint, both while the endpoint picker is up (it keeps choosing the broken endpoint for some
// requests) and while it is down. With FailClose the retry goes back to the endpoint the picker
// chose, which documents the behavior FailOpen changes.
func TestInferencePoolFailOpenRetry(t *testing.T) {
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/base.yaml"))
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/aigwroute.yaml"))

	const (
		pool          = "mistral"
		eppDeployment = "mistral-epp"
		eppSelector   = "app=mistral-epp"
		egSelector    = "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-aigwroute"
		manifest      = "testdata/inference_pool_retry.yaml"
		bodyTemplate  = `{"model":"mistral:latest","messages":[{"role":"user","content":"Say this is test number %d"}]}`
		requests      = 150
		concurrency   = 5
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

	// Leave the shared cluster as other tests expect it.
	t.Cleanup(func() {
		ctx := context.Background()
		_ = e2elib.Kubectl(ctx, "patch", "inferencepool", pool, "-n", inferenceGatewayNamespace,
			"--type=merge", "-p", `{"spec":{"endpointPickerRef":{"failureMode":"FailClose"}}}`).Run()
		_ = e2elib.Kubectl(ctx, "delete", "-f", manifest, "--ignore-not-found", "--wait=true").Run()
		_ = scaleEPP(ctx, "1")
		_ = e2elib.Kubectl(ctx, "rollout", "status", "deployment/"+eppDeployment,
			"-n", inferenceGatewayNamespace, "--timeout=3m").Run()
	})

	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	e2elib.RequireWaitForPodReady(t, inferenceGatewayNamespace, "app=mistral-upstream,broken=true")

	fwd := e2elib.RequireNewHTTPPortForwarder(t, inferenceGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	t.Cleanup(fwd.Kill)
	// Every request has its own prompt: the endpoint picker prefers the endpoint that served a
	// prompt before, so identical requests would all go to one endpoint, broken or not.
	var sent atomic.Int64
	post := func() (int, error) {
		return postChatCompletion(t, fwd, fmt.Sprintf(bodyTemplate, sent.Add(1)), nil)
	}
	// burst sends the requests and returns how many did not get a 200.
	burst := func(t *testing.T) int64 {
		var wg sync.WaitGroup
		var next, failed atomic.Int64
		for range concurrency {
			wg.Go(func() {
				for next.Add(1) <= requests {
					if code, err := post(); err != nil || code != http.StatusOK {
						t.Logf("request failed: status=%d err=%v", code, err)
						failed.Add(1)
					}
				}
			})
		}
		wg.Wait()
		return failed.Load()
	}
	// steadyLoad waits until the configuration has propagated (the route serves) and then
	// sends a burst of requests, returning how many did not get a 200.
	steadyLoad := func(t *testing.T, warmup int) (failures int64) {
		// The previous configuration can still be in effect for a moment after a change: wait
		// for a run of consecutive successes, which the previous configuration (where the broken
		// member fails some requests) is very unlikely to produce.
		consecutive := 0
		require.Eventually(t, func() bool {
			if code, err := post(); err == nil && code == http.StatusOK {
				consecutive++
			} else {
				consecutive = 0
			}
			return consecutive >= warmup
		}, 3*time.Minute, 200*time.Millisecond, "the route should serve requests")
		return burst(t)
	}

	t.Run("fail_open_endpoint_picker_up", func(t *testing.T) {
		setFailureMode("FailOpen")
		require.Zero(t, steadyLoad(t, 30), "clients must not see the failure of one pool member")
	})

	t.Run("fail_open_endpoint_picker_down", func(t *testing.T) {
		setFailureMode("FailOpen")
		require.NoError(t, scaleEPP(t.Context(), "0"))
		require.NoError(t, e2elib.Kubectl(t.Context(), "wait", "--for=delete", "pod", "-l", eppSelector,
			"-n", inferenceGatewayNamespace, "--timeout=3m").Run())
		require.Zero(t, steadyLoad(t, 30), "clients must not see the failure of one pool member")

		require.NoError(t, scaleEPP(t.Context(), "1"))
		require.NoError(t, e2elib.Kubectl(t.Context(), "rollout", "status", "deployment/"+eppDeployment,
			"-n", inferenceGatewayNamespace, "--timeout=3m").Run())
	})

	// Control: it shows the load above does exercise the failing member. The endpoint picker
	// chooses the broken member for some requests, and with FailClose the retry is sent to the
	// same endpoint again.
	t.Run("fail_close_retries_the_failed_endpoint", func(t *testing.T) {
		setFailureMode("FailClose")
		// The FailOpen configuration is still in effect until the change has propagated, and then
		// every request succeeds, so keep sending until the first failure shows the change.
		require.Eventually(t, func() bool {
			failures := burst(t)
			t.Logf("FailClose: %d of %d requests failed", failures, requests)
			return failures > 0
		}, 2*time.Minute, time.Second, "with FailClose the retry goes back to the endpoint that failed")
	})
}
