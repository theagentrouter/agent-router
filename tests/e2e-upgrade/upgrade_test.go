// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2eupgrade

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

const egSelector = "gateway.envoyproxy.io/owning-gateway-name=upgrade-test"

// testPhase represents the current phase of the upgrade test.
type testPhase int32

const (
	beforeUpgrade testPhase = iota
	duringUpgrade
	afterUpgrade
)

func (tp testPhase) String() string {
	switch tp {
	case beforeUpgrade:
		return "before upgrade"
	case duringUpgrade:
		return "during upgrade"
	case afterUpgrade:
		return "after upgrade"
	default:
		return "unknown phase"
	}
}

// TestUpgrade tests that the Envoy Gateway can be upgraded without dropping requests.
//
// There are two test cases:
//  1. Rolling out pods: This test case forces a rolling update of the Envoy pods by adding
//     a random annotation to the Envoy deployment. This simulates a scenario where the
//     Envoy pods are being updated while the control plane remains the same.
//  2. Control-plane upgrade: This test case upgrades the Envoy Gateway control plane
//     to the latest version. This simulates a scenario where both the control plane
//     and data plane are being updated.
//
// In both test cases, we continuously make requests to the Envoy Gateway and ensure
// that no requests fail during the upgrade process.
//
// Note that this utilizes MetalLB to make the situation more realistic as well as
// to avoid the complexity in maintaining non-recey port-forward assignment logic, etc.
// On Linux, this should work without any special setup. On Mac, this might
// require additional setup, e.g. https://waddles.org/2024/06/04/kind-with-metallb-on-macos/
// though it depends on the container runtime.
func TestUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name string
		// initFunc sets up the initial state of the cluster and returns the kind cluster name.
		initFunc func(context.Context) (clusterName string)
		// runningAfterUpgrade is the duration to wait after the upgrade before making requests.
		runningAfterUpgrade time.Duration
		// upgradeFunc performs the upgrade where we continue making requests during the upgrade.
		upgradeFunc func(context.Context)
	}{
		{
			name: "rolling out pods",
			initFunc: func(ctx context.Context) string {
				const kindClusterName = "envoy-ai-gateway-upgrade"
				require.NoError(t, e2elib.SetupAll(ctx, kindClusterName, e2elib.AIGatewayHelmOption{},
					false, false))
				return kindClusterName
			},
			runningAfterUpgrade: 30 * time.Second,
			upgradeFunc: func(ctx context.Context) {
				// Adding some random annotations to the Envoy deployments to force a rolling update.
				labelGetCmd := e2elib.Kubectl(ctx, "get", "deployment", "-n", e2elib.EnvoyGatewayNamespace,
					"-l", egSelector, "-o", "jsonpath={.items[0].metadata.name}")
				labelGetCmd.Stdout = nil
				labelGetCmd.Stderr = nil
				outputRaw, err := labelGetCmd.CombinedOutput()
				require.NoError(t, err, "failed to get deployment name: %s", string(outputRaw))
				deploymentName := string(outputRaw)
				t.Logf("Found deployment name: %s", deploymentName)
				cmd := e2elib.Kubectl(ctx, "patch", "deployment", "-n", e2elib.EnvoyGatewayNamespace,
					deploymentName, "--type=json", "-p",
					`[{"op":"add","path":"/spec/template/metadata/annotations/upgrade-timestamp","value":"`+uuid.NewString()+`"}]`)
				require.NoError(t, cmd.Run(), "failed to patch deployment")
			},
		},
		{
			name: "control-plane upgrade",
			initFunc: func(ctx context.Context) string {
				const previousEnvoyAIGatewayVersion = "v0.5.0"
				const kindClusterName = "envoy-ai-gateway-cp-upgrade"
				require.NoError(t, e2elib.SetupAll(ctx, kindClusterName, e2elib.AIGatewayHelmOption{
					ChartVersion: previousEnvoyAIGatewayVersion,
				}, false, false))
				return kindClusterName
			},
			runningAfterUpgrade: 2 * time.Minute, // Control plane pods take longer to restart and roll out new Envoy pods.
			upgradeFunc: func(ctx context.Context) {
				require.NoError(t, e2elib.InstallOrUpgradeAIGateway(ctx, e2elib.AIGatewayHelmOption{}))
				// Verify CRD storage version migration after upgrade
				verifyCRDStorageVersions(ctx, t)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NotNil(t, tc.upgradeFunc, "upgradeFunc must be set")
			clusterName := tc.initFunc(t.Context())
			defer func() {
				e2elib.CleanupKindCluster(t.Failed(), clusterName)
			}()

			phase := &phase{}
			monitorCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				if err := monitorPods(monitorCtx, egSelector, phase); err != nil && !errors.Is(err, context.Canceled) {
					log.Println("pod monitor error:", err) // This might print after the test ends, so not failing the test or using t.Log.
				}
			}()

			const manifest = "testdata/manifest.yaml"
			require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))

			e2elib.RequireWaitForGatewayPodReady(t, egSelector)
			ipAddress := e2elib.RequireGatewayListenerAddressViaMetalLB(t, "default", "upgrade-test")

			// Ensure that first request works.
			require.NoError(t, makeRequest(t, ipAddress, phase.String()))

			requestLoopCtx, cancelRequests := context.WithCancel(t.Context())
			defer cancelRequests()

			// Buffered channel prevents blocking when goroutines report errors.
			failChan := make(chan error, 100)
			defer close(failChan)
			var wg sync.WaitGroup
			wg.Add(100)
			for range 100 {
				go func() {
					defer wg.Done()
					for {
						select {
						case <-requestLoopCtx.Done():
							return
						default:
						}

						phase.requestCounts.Add(1)
						phaseStr := phase.String()
						if err := makeRequest(t, ipAddress, phaseStr); err != nil {
							// Non-blocking send: if channel is full, we've already captured enough failures.
							select {
							case failChan <- err:
							default:
								return
							}
						}
					}
				}()
			}

			t.Logf("Making sure multiple requests work before the upgrade")
			time.Sleep(30 * time.Second)
			if len(failChan) > 0 {
				t.Fatalf("request loop failed: %v", <-failChan)
			}
			t.Logf("Request count before upgrade: %d", phase.requestCounts.Load())
			phase.currentPhase.Store(int32(duringUpgrade))
			t.Log("Starting upgrade while making requests")
			tc.upgradeFunc(t.Context())
			t.Log("Upgrade completed")
			phase.currentPhase.Store(int32(afterUpgrade))

			t.Log("Making sure multiple requests work after the upgrade")
			time.Sleep(tc.runningAfterUpgrade)
			t.Logf("Request count after upgrade: %d", phase.requestCounts.Load())

			t.Log("Breaking filter config secret to simulate config change during control plane upgrade for the future versions")
			breakFilterConfig(t)
			time.Sleep(30 * time.Second)

			// Stop request goroutines and wait for clean shutdown before checking failures.
			cancelRequests()
			wg.Wait()

			// Collect and report all failures.
			var failures []error
			for i := len(failChan); i > 0; i-- {
				failures = append(failures, <-failChan)
			}
			if len(failures) > 0 {
				for _, err := range failures {
					t.Logf("request error: %v", err)
				}
				t.Fatalf("request loop had %d failures, first error: %v", len(failures), failures[0])
			}
		})
	}
}

// phase keeps track of the current test phase and the number of requests made.
type phase struct {
	// requestCounts keeps track of the number of requests made.
	requestCounts atomic.Int32
	// currentPhase indicates the current phase of the test.
	currentPhase atomic.Int32

	// currentPods keeps track of the current Envoy pods in the Envoy Gateway namespace.
	currentPods   string
	currentPodsMu sync.RWMutex
}

// String implements fmt.Stringer.
func (p *phase) String() string {
	phase := testPhase(p.currentPhase.Load())
	p.currentPodsMu.RLock()
	defer p.currentPodsMu.RUnlock()
	currentPods := p.currentPods
	return fmt.Sprintf("%s (requests made: %d, current pods: %s)", phase, p.requestCounts.Load(), currentPods)
}

// makeRequest makes a single request to the given IP address and returns an error if the request fails.
//
// The request is a simple POST request to the /v1/chat/completions endpoint with a streaming response.
// Since the testupstream server takes 200ms interval between each chunk, this ensures that the request
// lasts long enough to overlap with pod restarts during the upgrade.
func makeRequest(t *testing.T, ipAddress string, phase string) error {
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/v1/chat/completions", ipAddress),
		strings.NewReader(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"some-cool-model", "stream":true}`))
	require.NoError(t, err, "failed to create request")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(testupstreamlib.ResponseTypeKey, "sse")
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
		base64.StdEncoding.EncodeToString([]byte(`
		{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":"."},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" This"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" a"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":" test"},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[{"index":0,"delta":{"content":"."},"logprobs":null,"finish_reason":null}],"usage":null}
{"id":"chatcmpl-B8ZKlXBoEXZVTtv3YBmewxuCpNW7b","object":"chat.completion.chunk","created":1741382147,"model":"gpt-4o-mini-2024-07-18","service_tier":"default","system_fingerprint":"fp_06737a9306","choices":[],"usage":{"prompt_tokens":25,"completion_tokens":61,"total_tokens":86,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}}
		`)),
	)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("[%s] request failed: %w", phase, err)
	}
	defer resp.Body.Close()
	// Wait until we read the entire streaming response body.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("[%s] failed to read response body: %w", phase, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("[%s] unexpected status code %d: body=%s", phase, resp.StatusCode, string(body))
	}
	return nil
}

// breakFilterConfig modifies a filter config bundle part to contain an invalid configuration, which should be
// ignored by the running pods.
func breakFilterConfig(t *testing.T) {
	indexSecretName := controller.FilterConfigBundleIndexSecretName("upgrade-test", "default")
	getIndexCmd := e2elib.Kubectl(t.Context(), "get", "secret", indexSecretName,
		"-n", e2elib.EnvoyGatewayNamespace, "-o", "jsonpath={.data.index\\.yaml}")
	getIndexCmd.Stdout = nil
	getIndexCmd.Stderr = nil
	indexEncoded, err := getIndexCmd.Output()
	require.NoError(t, err, "failed to get filter config bundle index")
	indexRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(indexEncoded)))
	require.NoError(t, err, "failed to decode filter config bundle index")
	index, err := filterapi.UnmarshalConfigBundleIndex(indexRaw)
	require.NoError(t, err, "failed to unmarshal filter config bundle index")
	require.NotEmpty(t, index.Parts, "filter config bundle has no parts")

	newEncodedConfig := base64.StdEncoding.EncodeToString([]byte(`
version: some-nonexistent-version # The version mismatch should cause the filter to ignore this config.
foo: bar
`))

	// Patch the first part without changing the index checksum. The watcher must reject the corrupt bundle.
	patch := fmt.Sprintf(`{"data":{"chunk":"%s"}}`, newEncodedConfig)
	patchCmd := e2elib.Kubectl(t.Context(), "patch", "secret",
		index.Parts[0].Name,
		"-n", e2elib.EnvoyGatewayNamespace, "--type=merge", "-p", patch)
	patchCmd.Stdout = nil
	patchCmd.Stderr = nil
	require.NoError(t, patchCmd.Run(), "failed to patch filter config secret")
}

// monitorPods periodically checks the status of pods with the given label selector
// in the Envoy Gateway namespace and prints their status if it changes. It also updates
// the currentPods field in the given phase struct.
func monitorPods(ctx context.Context, labelSelector string, p *phase) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	type podInfo struct {
		name   string
		status string
	}

	var currentPods []podInfo
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			cmd := e2elib.Kubectl(ctx, "get", "pods", "-n", e2elib.EnvoyGatewayNamespace, "-l", labelSelector, "-o",
				"jsonpath={range .items[*]}{.metadata.name}:{.status.phase}{'\\n'}{end}")
			cmd.Stdout = nil
			cmd.Stderr = nil
			outputRaw, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("failed to get pods: %w: %s", err, string(outputRaw))
			}
			output := string(outputRaw)
			lines := strings.Split(strings.TrimSpace(output), "\n")
			var pods []podInfo
			for _, line := range lines {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) != 2 {
					continue
				}
				pods = append(pods, podInfo{name: parts[0], status: parts[1]})
			}
			sort.Slice(pods, func(i, j int) bool {
				return pods[i].name < pods[j].name
			})

			// Check for changes in pod status.
			changed := false
			if len(pods) != len(currentPods) {
				changed = true
			} else {
				for i, pod := range pods {
					if pod != currentPods[i] {
						changed = true
						break
					}
				}
			}
			if changed {
				currentPods = pods
				fmt.Printf("Current pods in namespace %q with selector %q:\n", e2elib.EnvoyGatewayNamespace, labelSelector)
				for _, pod := range currentPods {
					fmt.Printf(" - %s: %s\n", pod.name, pod.status)
				}

				var podStrs []string
				for _, pod := range currentPods {
					podStrs = append(podStrs, fmt.Sprintf("%s(%s)", pod.name, pod.status))
				}
				p.currentPodsMu.Lock()
				p.currentPods = strings.Join(podStrs, ", ")
				p.currentPodsMu.Unlock()
			}
		}
	}
}

// verifyCRDStorageVersions checks that CRDs have been properly migrated to v1beta1 storage version.
// It verifies that:
//  1. The stored versions include v1beta1
//  2. For CRDs that were migrated (AIGatewayRoute, AIServiceBackend, BackendSecurityPolicy, GatewayConfig),
//     both v1alpha1 and v1beta1 may be present during/after upgrade
//  3. The storage flag is set correctly for v1beta1
func verifyCRDStorageVersions(ctx context.Context, t *testing.T) {
	t.Log("Verifying CRD storage version migration")

	// CRDs that should have v1beta1 as storage version
	crdsToCheck := []string{
		"aigatewayroutes.aigateway.envoyproxy.io",
		"aiservicebackends.aigateway.envoyproxy.io",
		"backendsecuritypolicies.aigateway.envoyproxy.io",
		"gatewayconfigs.aigateway.envoyproxy.io",
		"mcproutes.aigateway.envoyproxy.io",
	}

	for _, crdName := range crdsToCheck {
		t.Run(crdName, func(t *testing.T) {
			// Check stored versions
			storedVersionsCmd := e2elib.Kubectl(ctx, "get", "crd", crdName,
				"-o", "jsonpath={.status.storedVersions}")
			storedVersionsCmd.Stdout = nil
			storedVersionsCmd.Stderr = nil
			output, err := storedVersionsCmd.CombinedOutput()
			require.NoError(t, err, "failed to get stored versions for %s: %s", crdName, string(output))

			storedVersions := strings.TrimSpace(string(output))
			t.Logf("CRD %s stored versions: %s", crdName, storedVersions)

			// Verify v1beta1 is in stored versions (it may have both v1alpha1 and v1beta1 during upgrade)
			require.Contains(t, storedVersions, "v1beta1", "CRD %s should have v1beta1 in stored versions", crdName)

			// Verify v1beta1 is marked as storage version
			storageVersionCmd := e2elib.Kubectl(ctx, "get", "crd", crdName,
				"-o", "jsonpath={.spec.versions[?(@.name==\"v1beta1\")].storage}")
			storageVersionCmd.Stdout = nil
			storageVersionCmd.Stderr = nil
			storageOutput, err := storageVersionCmd.CombinedOutput()
			require.NoError(t, err, "failed to get storage flag for v1beta1 in %s: %s", crdName, string(storageOutput))

			storageFlag := strings.TrimSpace(string(storageOutput))
			require.Equal(t, "true", storageFlag, "CRD %s should have v1beta1.storage=true", crdName)
			t.Logf("CRD %s has v1beta1 as storage version: %s", crdName, storageFlag)

			// Verify v1alpha1 is NOT marked as storage version
			v1alpha1StorageCmd := e2elib.Kubectl(ctx, "get", "crd", crdName,
				"-o", "jsonpath={.spec.versions[?(@.name==\"v1alpha1\")].storage}")
			v1alpha1StorageCmd.Stdout = nil
			v1alpha1StorageCmd.Stderr = nil
			v1alpha1Output, err := v1alpha1StorageCmd.CombinedOutput()
			require.NoError(t, err, "failed to get storage flag for v1alpha1 in %s: %s", crdName, string(v1alpha1Output))

			v1alpha1Storage := strings.TrimSpace(string(v1alpha1Output))
			require.Equal(t, "false", v1alpha1Storage, "CRD %s should have v1alpha1.storage=false", crdName)
		})
	}

	t.Log("CRD storage version migration verification completed successfully")
}

// TestCRDVersionUpgrade tests the upgrade path from v1alpha1 to v1beta1 API versions.
// It verifies that:
// 1. Resources created with v1alpha1 work correctly (inference calls succeed)
// 2. After upgrade, inference calls continue to work
// 3. Resources are successfully migrated to v1beta1 storage version
func TestCRDVersionUpgrade(t *testing.T) {
	ctx := t.Context()
	const kindClusterName = "envoy-ai-gateway-crd-version-upgrade"
	const previousEnvoyAIGatewayVersion = "v0.5.0"

	// Step 1: Setup cluster with v0.5.0 (v1alpha1 only)
	t.Log("Step 1: Setting up cluster with v0.5.0 (v1alpha1 CRDs)")
	require.NoError(t, e2elib.SetupAll(ctx, kindClusterName, e2elib.AIGatewayHelmOption{
		ChartVersion: previousEnvoyAIGatewayVersion,
	}, false, false))
	defer func() {
		e2elib.CleanupKindCluster(t.Failed(), kindClusterName)
	}()

	// Step 2: Apply test manifest (v1alpha1 resources) and wait for Gateway to be ready
	t.Log("Step 2: Applying test manifest with v1alpha1 resources")
	const manifest = "testdata/manifest.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(ctx, manifest))
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)
	ipAddress := e2elib.RequireGatewayListenerAddressViaMetalLB(t, "default", "upgrade-test")

	// Step 3: Verify inference calls work with v1alpha1
	t.Log("Step 3: Verifying inference calls work with v1alpha1 resources")
	require.NoError(t, makeRequest(t, ipAddress, "before upgrade"))
	t.Log("Inference calls successful with v1alpha1")

	// Step 4: Upgrade to latest version (with v1beta1 CRDs)
	t.Log("Step 4: Upgrading to latest version with v1beta1 CRDs")
	require.NoError(t, e2elib.InstallOrUpgradeAIGateway(ctx, e2elib.AIGatewayHelmOption{}))

	// Wait for controller to restart and reconcile
	time.Sleep(30 * time.Second)
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	// Step 5: Verify CRD storage versions are v1beta1
	t.Log("Step 5: Verifying CRD storage versions migrated to v1beta1")
	verifyCRDStorageVersions(ctx, t)

	// Step 6: Verify inference calls still work after upgrade
	t.Log("Step 6: Verifying inference calls work after upgrade to v1beta1")
	require.NoError(t, makeRequest(t, ipAddress, "after upgrade"))
	t.Log("Inference calls successful after upgrade")

	// Step 7: Verify resources are accessible via v1beta1 API
	t.Log("Step 7: Verifying resources are accessible via v1beta1 API")
	resources := []struct {
		kind string
		name string
	}{
		{"aigatewayroute", "upgrade-test"},
		{"aiservicebackend", "upgrade-test-cool-model-backend"},
		{"mcproute", "upgrade-test-mcp-route"},
	}

	for _, r := range resources {
		cmd := e2elib.Kubectl(ctx, "get", r.kind+".v1beta1.aigateway.envoyproxy.io", r.name, "-n", "default")
		cmd.Stdout = nil
		cmd.Stderr = nil
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed to get %s/%s via v1beta1: %s", r.kind, r.name, string(output))
		t.Logf("Resource %s/%s accessible via v1beta1 API", r.kind, r.name)
	}

	t.Log("CRD version upgrade test completed successfully!")
}
