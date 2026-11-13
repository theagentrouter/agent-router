// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// For InferencePool integration tests, we change to use Envoy Gateway GNM(gateway namespace mode).
// The namespace of the Gateway resources(e.g. Deployment, Service) changed from "envoy-gateway-system" to "default" namespace.
const inferenceGatewayNamespace = "default"

// TestInferencePoolIntegration tests the InferencePool integration with AI Gateway.
func TestInferencePoolIntegration(t *testing.T) {
	// Apply the base test manifest.
	const baseManifest = "../../examples/inference-pool/base.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), baseManifest))
	// Test inferencePool with AIGatewayRoute.
	const aiGWRouteManifest = "../../examples/inference-pool/aigwroute.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), aiGWRouteManifest))

	egSelector := "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-aigwroute"
	e2elib.RequireWaitForGatewayPodReadyWithNamespace(t, inferenceGatewayNamespace, egSelector)

	// Verify InferencePool status is correctly set for the Gateway.
	t.Run("verify_inference_pool_status", func(t *testing.T) {
		// Verify that the mistral InferencePool has correct status for the Gateway.
		requireInferencePoolStatusValid(t, inferenceGatewayNamespace, "mistral", "inference-pool-with-aigwroute")

		// Verify that the vllm-llama3-8b-instruct InferencePool has correct status for the Gateway.
		// Note: This InferencePool is referenced in the AIGatewayRoute but may not exist in base.yaml.
		// We'll check if it exists first.
		status, err := getInferencePoolStatus(t.Context(), inferenceGatewayNamespace, "vllm-llama3-8b-instruct")
		if err == nil && status != nil {
			requireInferencePoolStatusValid(t, inferenceGatewayNamespace, "vllm-llama3-8b-instruct", "inference-pool-with-aigwroute")
		} else {
			t.Logf("InferencePool vllm-llama3-8b-instruct not found, skipping status validation: %v", err)
		}
	})

	// Test connectivity to inferencePool + header match + inference pods with valid metrics, should return 200.
	t.Run("endpointpicker_with_aigwroute_matched_header", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", map[string]string{"Authorization": "sk-abcdefghijklmnopqrstuvwxyz"}, http.StatusOK)
	})

	// Test connectivity to inferencePool + header match + inference pods with valid metrics, should return 200.
	t.Run("endpointpicker_with_aigwroute_matched_header", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", map[string]string{"Authorization": "sk-zyxwvutsrqponmlkjihgfedcba"}, http.StatusOK)
	})

	// Test connectivity to inferencePool + unmatched route + inference pods with valid metrics, should return 404 directly.
	t.Run("endpointpicker_with_aigwroute_unmatched", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", nil, http.StatusNotFound)
	})

	// Test connectivity to inferencePool + inference pods with invalid metrics, should fallback to a random pick.
	t.Run("endpointpicker_with_aigwroute_invalid_pod_metrics", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "mistral:latest", nil, http.StatusOK)
	})

	// Test connectivity to aiservicebackend within the same aigatewayroute with inferencePool.
	t.Run("endpointpicker_with_aigwroute_aiservicebackend", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "some-cool-self-hosted-model", nil, http.StatusOK)
	})

	// Test connectivity to inferencePool + inference pods with compressed and uncompressed JSON body.
	t.Run("endpointpicker_with_compressed_json_body", func(t *testing.T) {
		testInferenceGatewayConnectivity(t, egSelector, `{"model":"meta-llama/Llama-3.1-8B-Instruct","messages":[{"role":"user","content":"Say this is a test"}]}`, map[string]string{"Authorization": "sk-abcdefghijklmnopqrstuvwxyz"}, http.StatusOK)
	})

	// Test connectivity to inferencePool + inference pods with compressed and uncompressed JSON body which will be compressed by the EPP.
	t.Run("endpointpicker_with_uncompressed_json_body", func(t *testing.T) {
		testInferenceGatewayConnectivity(t, egSelector, `
{
	"model": "meta-llama/Llama-3.1-8B-Instruct",
	"messages": [{
		"role": "user",
		"content": "Say this is a test"
	}]
}`, map[string]string{"Authorization": "sk-abcdefghijklmnopqrstuvwxyz"}, http.StatusOK)
	})

	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), aiGWRouteManifest)
	})

	// Test inferencePool with HTTPRoute.
	const httpRouteManifest = "../../examples/inference-pool/httproute.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), httpRouteManifest))

	egSelector = "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-httproute"
	e2elib.RequireWaitForPodReady(t, inferenceGatewayNamespace, egSelector)

	// Verify InferencePool status is correctly set for the HTTPRoute Gateway.
	t.Run("verify_inference_pool_status_httproute", func(t *testing.T) {
		// For HTTPRoute, the referenced InferencePool is "vllm-llama3-8b-instruct".
		// The HTTPRoute Gateway name should be "inference-pool-with-httproute".
		requireInferencePoolStatusValid(t, inferenceGatewayNamespace, "vllm-llama3-8b-instruct", "inference-pool-with-httproute")
	})

	// Test connectivity to inferencePool + inference pods with valid metrics.
	t.Run("endpointpicker_with_httproute_valid_pod_metrics", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", nil, http.StatusOK)
	})

	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), httpRouteManifest)
	})

	// Test an InferencePool without an endpointPickerRef: its share of a rule's traffic fails with
	// a 500, as an invalid backendRef's does, while the rule's other backends keep their share.
	// Every other backend here has ready endpoints; otherwise Envoy would answer the failed share
	// with the route-wide 503 instead.
	noEPPCases := []struct {
		name, testCase string
		wantOK         bool
	}{
		{"httproute_service_and_inference_pool_without_epp", "service-and-no-epp-pool", true},
		{"httproute_inference_pools_with_and_without_epp", "epp-pool-and-no-epp-pool", true},
		{"httproute_only_inference_pool_without_epp", "only-no-epp-pool", false},
	}
	// The manifest has rules with two InferencePools. Older Envoy Gateway releases fail the whole
	// xDS translation on those, which would break every route on the Gateway, so don't apply it.
	supported := e2elib.EnvoyGatewaySupportsMultipleInferencePoolsPerRule()
	if supported {
		const noEPPManifest = "testdata/inference-pool-without-epp.yaml"
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), noEPPManifest))
		t.Cleanup(func() {
			_ = e2elib.KubectlDeleteManifest(context.Background(), noEPPManifest)
		})
	}

	for _, tc := range noEPPCases {
		t.Run(tc.name, func(t *testing.T) {
			if !supported {
				t.Skipf("needs Envoy Gateway v1.11.0-rc.1 or later, have %s", e2elib.EnvoyGatewayVersion())
			}
			requireInferenceGatewayStatusSplit(t, egSelector, tc.testCase, tc.wantOK)
		})
	}
}

// requireInferenceGatewayStatusSplit sends requests matching the x-test-case rule of
// testdata/inference-pool-without-epp.yaml until their statuses show the share of the InferencePool
// without an endpoint picker failing with a 500. With wantOK, the rule's other backend must keep
// its share, answering 200; otherwise every request must fail with a 500.
func requireInferenceGatewayStatusSplit(t *testing.T, egSelector, testCase string, wantOK bool) {
	const requestsPerAttempt = 20
	require.Eventually(t, func() bool {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, inferenceGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
		defer fwd.Kill()

		statuses := make(map[int]int)
		for range requestsPerAttempt {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, fwd.Address()+"/v1/chat/completions",
				strings.NewReader(`{"model":"meta-llama/Llama-3.1-8B-Instruct","messages":[{"role":"user","content":"Say this is a test"}]}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-test-case", testCase)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				cancel()
				t.Logf("request failed: %v", err)
				return false
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			cancel()
			statuses[resp.StatusCode]++
		}
		t.Logf("%s: response statuses %v", testCase, statuses)

		// Until the route is programmed, requests fall through to the catch-all rule with a 200.
		if statuses[http.StatusInternalServerError] == 0 {
			return false
		}
		if !wantOK {
			return statuses[http.StatusInternalServerError] == requestsPerAttempt
		}
		// Weights 1:1, so 20 requests all landing on one side is a ~1e-6 chance.
		return statuses[http.StatusOK] > 0 &&
			statuses[http.StatusOK]+statuses[http.StatusInternalServerError] == requestsPerAttempt
	}, 2*time.Minute, 5*time.Second, "unexpected response statuses for %s", testCase)
}

// testInferenceGatewayConnectivityByModel tests that the Gateway is accessible and returns a 200 status code.
// for a valid request to the InferencePool backend for a specific model.
func testInferenceGatewayConnectivityByModel(t *testing.T, egSelector, model string, additionalHeaders map[string]string, expectedStatusCode int) {
	testInferenceGatewayConnectivity(t, egSelector,
		fmt.Sprintf(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`, model), additionalHeaders, expectedStatusCode)
}

// testInferenceGatewayConnectivity tests that the InferenceGateway is working as expected and returns a expected status code.
func testInferenceGatewayConnectivity(t *testing.T, egSelector, body string, additionalHeaders map[string]string, expectedStatusCode int) {
	require.Eventually(t, func() bool {
		fwd := e2elib.RequireNewHTTPPortForwarder(t, inferenceGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
		defer fwd.Kill()

		// Set timeout context.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		// Create a request to the InferencePool backend with the correct model header.
		requestBody := body
		t.Logf("Request body: %s", requestBody)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(requestBody))
		require.NoError(t, err)
		// Set required headers for InferencePool routing.
		req.Header.Set("Content-Type", "application/json")
		for key, value := range additionalHeaders {
			req.Header.Set(key, value)
		}

		// Make the request.
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("request failed: %v", err)
			return false
		}
		defer func() { _ = resp.Body.Close() }()

		// Read response body for debugging.
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err, "failed to read response body")
		t.Logf("Response status: %d, body: %s", resp.StatusCode, string(body))

		// Check for successful response (200 OK).
		if resp.StatusCode != expectedStatusCode {
			t.Logf("unexpected status code: %d (expected %d), body: %s", resp.StatusCode, expectedStatusCode, string(body))
			return false
		}

		// Verify we got a valid response body (should contain some content).
		require.NotEmpty(t, body, "response body should not be empty")
		t.Logf("Gateway connectivity test passed: status=%d", resp.StatusCode)
		return true
	}, 2*time.Minute, 5*time.Second, "Gateway should return expected status code", expectedStatusCode)
}

// getInferencePoolStatus retrieves the status of an InferencePool resource.
func getInferencePoolStatus(ctx context.Context, namespace, name string) (*gwaiev1.InferencePoolStatus, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "inferencepool", name, "-n", namespace, "-o", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get InferencePool %s/%s: %w", namespace, name, err)
	}

	var inferencePool gwaiev1.InferencePool
	if err := json.Unmarshal(out, &inferencePool); err != nil {
		return nil, fmt.Errorf("failed to unmarshal InferencePool: %w", err)
	}

	return &inferencePool.Status, nil
}

// requireInferencePoolStatusValid validates that the InferencePool status is correctly set.
func requireInferencePoolStatusValid(t *testing.T, namespace, inferencePoolName, expectedGatewayName string) {
	require.Eventually(t, func() bool {
		status, err := getInferencePoolStatus(t.Context(), namespace, inferencePoolName)
		if err != nil {
			t.Logf("Failed to get InferencePool status: %v", err)
			return false
		}

		// Check that we have at least one parent status.
		if len(status.Parents) == 0 {
			t.Logf("InferencePool %s has no parent status", inferencePoolName)
			return false
		}

		// Find the parent status for the expected Gateway.
		var foundParent *gwaiev1.ParentStatus
		for i := range status.Parents {
			parent := &status.Parents[i]
			if string(parent.ParentRef.Name) == expectedGatewayName {
				foundParent = parent
				break
			}
		}

		if foundParent == nil {
			t.Logf("InferencePool %s does not have parent status for Gateway %s", inferencePoolName, expectedGatewayName)
			return false
		}

		// Validate the GatewayRef fields.
		if foundParent.ParentRef.Group == nil || string(*foundParent.ParentRef.Group) != "gateway.networking.k8s.io" {
			t.Logf("InferencePool %s parent GatewayRef has incorrect group: %v", inferencePoolName, foundParent.ParentRef.Group)
			return false
		}

		if string(foundParent.ParentRef.Kind) != "Gateway" {
			t.Logf("InferencePool %s parent GatewayRef has incorrect kind: %v", inferencePoolName, foundParent.ParentRef.Kind)
			return false
		}

		if string(foundParent.ParentRef.Name) != expectedGatewayName {
			t.Logf("InferencePool %s parent GatewayRef has incorrect name: %s (expected %s)", inferencePoolName, foundParent.ParentRef.Name, expectedGatewayName)
			return false
		}

		if string(foundParent.ParentRef.Namespace) != namespace {
			t.Logf("InferencePool %s parent GatewayRef has incorrect namespace: %v (expected %s)", inferencePoolName, foundParent.ParentRef.Namespace, namespace)
			return false
		}

		// Validate the conditions.
		if len(foundParent.Conditions) == 0 {
			t.Logf("InferencePool %s parent has no conditions", inferencePoolName)
			return false
		}

		// Find the "Accepted" condition.
		var acceptedCondition *metav1.Condition
		for i := range foundParent.Conditions {
			condition := &foundParent.Conditions[i]
			if condition.Type == "Accepted" {
				acceptedCondition = condition
				break
			}
		}

		if acceptedCondition == nil {
			t.Logf("InferencePool %s parent does not have 'Accepted' condition", inferencePoolName)
			return false
		}

		if acceptedCondition.Status != metav1.ConditionTrue {
			t.Logf("InferencePool %s 'Accepted' condition status is not True: %s", inferencePoolName, acceptedCondition.Status)
			return false
		}

		if acceptedCondition.Reason != "Accepted" {
			t.Logf("InferencePool %s 'Accepted' condition reason is not 'Accepted': %s", inferencePoolName, acceptedCondition.Reason)
			return false
		}

		t.Logf("InferencePool %s status validation passed: Gateway=%s, Condition=%s", inferencePoolName, expectedGatewayName, acceptedCondition.Status)
		return true
	}, 2*time.Minute, 5*time.Second, "InferencePool status should be correctly set")
}
