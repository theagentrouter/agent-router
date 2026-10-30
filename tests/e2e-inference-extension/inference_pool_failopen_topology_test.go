// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestInferencePoolFailOpenTopology tests that a FailOpen InferencePool works when its Pods are
// not laid out like the one in the other tests: in a namespace different from the Gateway's, and
// serving on more than one target port.
//
// For each topology it checks the headless fallback Service the controller creates for the pool,
// that requests are served with the endpoint picker up, and that, with the endpoint picker down,
// requests are served by every endpoint of the pool.
//
// The cases are not independent of the shared Gateway, so they run one after another.
func TestInferencePoolFailOpenTopology(t *testing.T) {
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/base.yaml"))
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), "../../examples/inference-pool/aigwroute.yaml"))
	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-aigwroute"
	e2elib.RequireWaitForGatewayPodReadyWithNamespace(t, inferenceGatewayNamespace, egSelector)

	for _, tc := range []struct {
		name string
		// manifest creates the pool, its Pods and endpoint picker, and a route to it.
		manifest string
		// namespace holds the pool, its Pods and endpoint picker, and so the fallback Service.
		namespace string
		pool      string
		model     string
		// ports are the target ports of the pool: every Pod serves each one.
		ports []string
	}{
		{
			name:      "pool_in_another_namespace",
			manifest:  "testdata/inference_pool_cross_namespace.yaml",
			namespace: "failopen-xns",
			pool:      "xns-pool",
			model:     "xns-model",
			ports:     []string{"8080"},
		},
		{
			name:      "multiple_target_ports",
			manifest:  "testdata/inference_pool_multi_port.yaml",
			namespace: inferenceGatewayNamespace,
			pool:      "ports-pool",
			model:     "ports-model",
			ports:     []string{"8080", "8081"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testFailOpenTopology(t, egSelector, tc.manifest, tc.namespace, tc.pool, tc.model, tc.ports)
		})
	}
}

func testFailOpenTopology(t *testing.T, egSelector, manifest, namespace, pool, model string, ports []string) {
	var (
		podSelector   = "app=" + pool + "-upstream"
		eppDeployment = pool + "-epp"
		fallback      = pool + "-epp-fallback"
	)
	t.Cleanup(func() {
		_ = e2elib.Kubectl(context.Background(), "delete", "-f", manifest, "--ignore-not-found", "--wait=true").Run()
	})
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	e2elib.RequireWaitForPodReady(t, namespace, podSelector)
	e2elib.RequireWaitForPodReady(t, namespace, "app="+eppDeployment)

	t.Run("fallback_service", func(t *testing.T) {
		require.Eventually(t, func() bool {
			_, err := kubectlOutput(t.Context(), "get", "service", fallback, "-n", namespace)
			return err == nil
		}, time.Minute, time.Second, "the fallback Service should be created in the pool's namespace")
		out, err := kubectlOutput(t.Context(), "get", "service", fallback, "-n", namespace,
			"-o", `jsonpath={.spec.clusterIP} {range .spec.ports[*]}{.targetPort} {end}`)
		require.NoError(t, err)
		require.Equal(t, "None "+strings.Join(ports, " "), strings.TrimSpace(out), "headless, with one port per target port")

		if namespace != inferenceGatewayNamespace {
			_, err := kubectlOutput(t.Context(), "get", "service", fallback, "-n", inferenceGatewayNamespace)
			require.Error(t, err, "there must be no fallback Service in the Gateway's namespace")
		}
	})

	// The Pods and what each one serves: the testupstream-id of an endpoint is "<pod>-<port>".
	podsByIP := requirePodsByIP(t, namespace, podSelector)
	var endpoints []string
	for _, pod := range podsByIP {
		for _, port := range ports {
			endpoints = append(endpoints, pod+"-"+port)
		}
	}
	require.Len(t, endpoints, 3*len(ports))

	fwd := e2elib.RequireNewHTTPPortForwarder(t, inferenceGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	t.Cleanup(fwd.Kill)
	// Every request has its own prompt: the endpoint picker prefers the endpoint that served a
	// prompt before, so identical requests would all go to one endpoint.
	var sent atomic.Int64
	post := func() (code int, endpoint string, err error) {
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Say this is test number %d"}]}`, model, sent.Add(1))
		return postChatCompletionFromUpstream(t, fwd, body, nil)
	}
	// waitServing waits until the route serves requests: the first ones can still run into the
	// configuration of before the pool existed, or of before the endpoint picker went down.
	waitServing := func(t *testing.T) {
		require.Eventually(t, func() bool {
			code, _, err := post()
			return err == nil && code == http.StatusOK
		}, 3*time.Minute, time.Second, "the route should serve requests")
	}
	// sendUntil sends requests, each of which must get a 200, until done says that the endpoints
	// that have answered so far are enough. It returns the endpoints in the order that they answered.
	sendUntil := func(t *testing.T, done func(answered []string) bool) (answered []string) {
		for range 600 {
			code, endpoint, err := post()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, code)
			answered = append(answered, endpoint)
			if done(answered) {
				return answered
			}
		}
		require.Failf(t, "not enough endpoints answered", "got %v", answered)
		return nil
	}

	t.Run("serves_the_endpoint_the_picker_chooses", func(t *testing.T) {
		waitServing(t)
		// Send until every target port has answered at least once, so that the check below
		// covers them all.
		answered := sendUntil(t, func(answered []string) bool {
			return !slices.ContainsFunc(ports, func(port string) bool {
				return !slices.ContainsFunc(answered, func(a string) bool { return strings.HasSuffix(a, "-"+port) })
			})
		})

		// The endpoint picker logs the "ip:port" it chose for each request, in the order of the
		// requests: each was answered by exactly that endpoint.
		chosen := requireChosenEndpoints(t, namespace, eppDeployment, len(answered))
		for i, c := range chosen {
			ip, port, ok := strings.Cut(c, ":")
			require.True(t, ok, "endpoint %q", c)
			require.Equal(t, podsByIP[ip]+"-"+port, answered[i], "request %d was sent to %s", i, c)
		}
	})

	t.Run("serves_every_endpoint_without_the_picker", func(t *testing.T) {
		require.NoError(t, e2elib.Kubectl(t.Context(), "scale", "deployment", eppDeployment,
			"-n", namespace, "--replicas=0").Run())
		require.NoError(t, e2elib.Kubectl(t.Context(), "wait", "--for=delete", "pod", "-l", "app="+eppDeployment,
			"-n", namespace, "--timeout=3m").Run())
		waitServing(t)
		sendUntil(t, func(answered []string) bool {
			return !slices.ContainsFunc(endpoints, func(e string) bool { return !slices.Contains(answered, e) })
		})
	})
}

// kubectlOutput runs kubectl and returns what it writes to stdout.
func kubectlOutput(ctx context.Context, args ...string) (string, error) {
	cmd := e2elib.Kubectl(ctx, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

// requirePodsByIP returns the names of the Pods matching selector, by Pod IP.
func requirePodsByIP(t *testing.T, namespace, selector string) map[string]string {
	out, err := kubectlOutput(t.Context(), "get", "pods", "-n", namespace, "-l", selector, "-o",
		`jsonpath={range .items[*]}{.status.podIP} {.metadata.name}{"\n"}{end}`)
	require.NoError(t, err)
	pods := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		ip, name, ok := strings.Cut(line, " ")
		require.True(t, ok, "unexpected Pod line %q", line)
		pods[ip] = name
	}
	return pods
}

// requireChosenEndpoints returns the "ip:port" the endpoint picker chose for each of the last n
// requests, oldest first, from its logs.
func requireChosenEndpoints(t *testing.T, namespace, eppDeployment string, n int) []string {
	out, err := kubectlOutput(t.Context(), "logs", "deployment/"+eppDeployment, "-n", namespace, "--tail=-1")
	require.NoError(t, err)
	var chosen []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var entry struct {
			Body     string `json:"body"`
			Endpoint string `json:"endpoint"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Body == "Request handled" && entry.Endpoint != "" {
			chosen = append(chosen, entry.Endpoint)
		}
	}
	require.GreaterOrEqual(t, len(chosen), n, "the endpoint picker should log the endpoint it chose for each request")
	return chosen[len(chosen)-n:]
}
