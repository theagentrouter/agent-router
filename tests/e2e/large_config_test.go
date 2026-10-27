// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

func TestLargeConfigIsSharded(t *testing.T) {
	const (
		namespace         = "default"
		gatewayClassName  = "mcp-large-config-class"
		gatewayName       = "mcp-large-config-gateway"
		envoyProxyName    = "mcp-large-config-envoyproxy"
		egSelector        = "gateway.envoyproxy.io/owning-gateway-name=" + gatewayName
		testBackendName   = "mcp-large-config-backend"
		testBackendAPIKey = "test-api-key"
		testBackendPort   = 1063
		routeCount        = 50
	)

	baseManifest := buildLargeMCPBaseManifest(namespace, gatewayClassName, gatewayName, envoyProxyName, testBackendName, testBackendAPIKey, testBackendPort)
	require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), baseManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifestStdin(context.Background(), baseManifest)
	})

	mcpRouteManifest := buildLargeMCPRouteManifest(namespace, gatewayName, testBackendName, testBackendAPIKey, testBackendPort, routeCount)
	require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), mcpRouteManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifestStdin(context.Background(), mcpRouteManifest)
	})

	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	indexSecretName := controller.FilterConfigBundleIndexSecretName(gatewayName, namespace)
	require.Eventually(t, func() bool {
		partCount, err := readIndexPartCount(t.Context(), indexSecretName)
		if err != nil {
			t.Logf("failed reading index secret: %v", err)
			return false
		}
		if partCount <= 1 {
			t.Logf("partCount=%d, expecting > 1", partCount)
			return false
		}
		t.Logf("partCount=%d", partCount)
		for i := range partCount {
			if err = requireSecretExists(t.Context(), fmt.Sprintf("%s-part-%03d", indexSecretName, i)); err != nil {
				t.Logf("part-%03d missing: %v", i, err)
				return false
			}
		}
		t.Logf("config bundle successfully split into %d secrets", partCount)
		return true
	}, 90*time.Second, 2*time.Second, "bundle did not split into multiple secrets")

	client := mcp.NewClient(&mcp.Implementation{Name: "large-config-mcp-client", Version: "0.1.0"}, nil)
	testMCPRouteTools(
		t.Context(),
		t,
		client,
		fwd.Address(),
		"/mcp/large/000",
		testMCPServerAllToolNames(testBackendName+"__"),
		nil,
		true,
		true,
	)
	t.Logf("successfully tested route /mcp/large/000")
	testMCPRouteTools(
		t.Context(),
		t,
		client,
		fwd.Address(),
		fmt.Sprintf("/mcp/large/%03d", routeCount-1),
		testMCPServerAllToolNames(testBackendName+"__"),
		nil,
		true,
		true,
	)
	t.Logf("successfully tested route /mcp/large/%03d", routeCount-1)
}

func buildLargeMCPBaseManifest(namespace, gatewayClassName, gatewayName, envoyProxyName, backendName, backendAPIKey string, backendPort int) string {
	return strings.TrimSpace(fmt.Sprintf(`
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: %s
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: %s
  namespace: %s
spec:
  gatewayClassName: %s
  listeners:
    - name: http
      protocol: HTTP
      port: 80
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: %s
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: %s
  namespace: %s
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        container:
          resources: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
        - name: mcp-backend
          image: docker.io/envoyproxy/ai-gateway-testmcpserver:latest
          imagePullPolicy: IfNotPresent
          env:
            - name: TEST_API_KEY
              value: "%s"
          ports:
            - containerPort: %d
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: %s
  ports:
    - protocol: TCP
      port: %d
      targetPort: %d
  type: ClusterIP
`, gatewayClassName, gatewayName, namespace, gatewayClassName, envoyProxyName, envoyProxyName, namespace, backendName, namespace, backendName, backendName, backendAPIKey, backendPort, backendName, namespace, backendName, backendPort, backendPort))
}

func buildLargeMCPRouteManifest(namespace, gatewayName, backendName, backendAPIKey string, backendPort, routeCount int) string {
	const (
		authRulesPerRoute = 32
		toolsPerRule      = 16
	)

	var b strings.Builder
	for r := 0; r < routeCount; r++ {
		fmt.Fprintf(&b, `---
apiVersion: aigateway.envoyproxy.io/v1alpha1
kind: MCPRoute
metadata:
  name: large-config-mcp-route-%03d
  namespace: %s
spec:
  path: /mcp/large/%03d
  parentRefs:
    - name: %s
      kind: Gateway
      group: gateway.networking.k8s.io
  backendRefs:
    - name: %s
      port: %d
      securityPolicy:
        apiKey:
          inline: "%s"
  securityPolicy:
    authorization:
      defaultAction: Allow
      rules:
`, r, namespace, r, gatewayName, backendName, backendPort, backendAPIKey)
		for i := 0; i < authRulesPerRoute; i++ {
			fmt.Fprintf(&b, `        - cel: request.mcp.params.name == "tool-r%03d-i%03d" && request.headers["x-debug-id"] == "large-config-r%03d-i%03d-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
          action: Deny
          target:
            tools:
`, r, i, r, i)
			for j := 0; j < toolsPerRule; j++ {
				fmt.Fprintf(&b, `              - backend: %s
                tool: "tool-r%03d-i%03d-t%02d"
`, backendName, r, i, j)
			}
		}
	}

	return strings.TrimSpace(b.String())
}

func readIndexPartCount(ctx context.Context, secretName string) (int, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "secret", "-n", e2elib.EnvoyGatewayNamespace, secretName, "-o", "jsonpath={.data.index\\.yaml}") // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	var idx struct {
		Parts []struct {
			Name string `yaml:"name"`
		} `yaml:"parts"`
	}
	if err = yaml.Unmarshal(decoded, &idx); err != nil {
		return 0, err
	}
	return len(idx.Parts), nil
}

func requireSecretExists(ctx context.Context, secretName string) error {
	cmd := e2elib.Kubectl(ctx, "get", "secret", "-n", e2elib.EnvoyGatewayNamespace, secretName)
	return cmd.Run()
}
