// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json" //nolint: depguard // byte-stable hashing; sonic does not guarantee stable field order.
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// toolIntegrityDigestPayload mirrors internal/mcpproxy.toolDigestPayload: the canonical,
// deterministically-ordered representation of a tool definition that a configured digest
// is computed over. Duplicated here (rather than importing the internal package, which
// this external e2e test binary cannot) so the manifest below can be built with a digest
// computed the exact same way the gateway computes it.
type toolIntegrityDigestPayload struct {
	Name         string               `json:"name"`
	Description  string               `json:"description,omitempty"`
	InputSchema  any                  `json:"inputSchema,omitempty"`
	OutputSchema any                  `json:"outputSchema,omitempty"`
	Annotations  *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

func requireToolDigest(t *testing.T, tool *mcp.Tool) string {
	t.Helper()
	payload := toolIntegrityDigestPayload{
		Name:         tool.Name,
		Description:  tool.Description,
		InputSchema:  tool.InputSchema,
		OutputSchema: tool.OutputSchema,
		Annotations:  tool.Annotations,
	}
	data, err := stdjson.Marshal(payload)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// requireLiveEchoToolDigest starts a local, unproxied instance of the exact same test MCP
// server binary the e2e cluster deploys, and computes the "echo" tool's content digest from
// its real wire-format tools/list response. This is required (rather than hand-deriving the
// digest from the Go source literal in tests/internal/testmcp/tools.go) because the schema
// crosses the wire as arbitrary JSON and is decoded into map[string]any on the client side --
// the exact bytes matter for a content digest, and only a live round trip proves them.
func requireLiveEchoToolDigest(t *testing.T) string {
	t.Helper()
	port := internaltesting.RequireRandomPorts(t, 1)[0]
	srv, _ := testmcp.NewServer(&testmcp.Options{Port: port, DisableLog: true})
	t.Cleanup(func() { _ = srv.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "digest-probe-client", Version: "0.1.0"}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	var sess *mcp.ClientSession
	require.Eventually(t, func() bool {
		var err error
		sess, err = client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		}, nil)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "failed to connect to local probe MCP server")
	defer func() { _ = sess.Close() }()

	tools, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name == testmcp.ToolEcho.Tool.Name {
			return requireToolDigest(t, tool)
		}
	}
	t.Fatalf("echo tool not found in probe server's tools/list")
	return ""
}

// TestMCPRouteToolIntegrity verifies opt-in content-digest verification of backend tool
// definitions (#2736): a tool with a matching configured digest is exposed normally, a tool
// with a configured digest that does not match is dropped (onMismatch: Drop) or takes its
// whole backend down with it (onMismatch: Deny), and a tool with no configured digest passes
// through unaffected either way.
func TestMCPRouteToolIntegrity(t *testing.T) {
	correctEchoDigest := requireLiveEchoToolDigest(t)
	wrongDigest := strings.Repeat("0", 64)

	manifest := fmt.Sprintf(toolIntegrityManifestTemplate, correctEchoDigest, wrongDigest, wrongDigest)
	require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifestStdin(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway-tool-integrity"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	client := mcp.NewClient(&mcp.Implementation{Name: "tool-integrity-e2e-client", Version: "0.1.0"}, nil)

	t.Run("onMismatch=Drop: matching tool exposed, mismatched tool dropped, uncovered tool unaffected", func(t *testing.T) {
		sess := requireConnectMCP(t.Context(), t, client, fwd.Address()+"/mcp-tool-integrity-drop", nil)
		t.Cleanup(func() { _ = sess.Close() })

		tools, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)
		names := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			names[i] = tool.Name
		}
		require.Contains(t, names, "mcp-backend-tool-integrity__echo", "tool with a matching digest must be exposed")
		require.NotContains(t, names, "mcp-backend-tool-integrity__sum", "tool with a mismatched digest must be dropped")
		require.Contains(t, names, "mcp-backend-tool-integrity__countdown", "tool with no configured digest must pass through unaffected")
	})

	t.Run("onMismatch=Deny: any mismatch drops the entire backend's tools", func(t *testing.T) {
		sess := requireConnectMCP(t.Context(), t, client, fwd.Address()+"/mcp-tool-integrity-deny", nil)
		t.Cleanup(func() { _ = sess.Close() })

		tools, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)
		require.Empty(t, tools.Tools, "a single digest-covered mismatch must drop every tool from the backend")
	})
}

const toolIntegrityManifestTemplate = `
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: mcp-gateway-class-tool-integrity
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mcp-gateway-tool-integrity
  namespace: default
spec:
  gatewayClassName: mcp-gateway-class-tool-integrity
  listeners:
    - name: http
      protocol: HTTP
      port: 80
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: envoy-mcp-gateway-tool-integrity
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: mcp-route-tool-integrity-drop
  namespace: default
spec:
  path: "/mcp-tool-integrity-drop"
  parentRefs:
    - name: mcp-gateway-tool-integrity
      kind: Gateway
      group: gateway.networking.k8s.io
  backendRefs:
    - name: mcp-backend-tool-integrity
      port: 1063
      toolIntegrity:
        digests:
          echo: %q
          sum: %q
        onMismatch: Drop
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: mcp-route-tool-integrity-deny
  namespace: default
spec:
  path: "/mcp-tool-integrity-deny"
  parentRefs:
    - name: mcp-gateway-tool-integrity
      kind: Gateway
      group: gateway.networking.k8s.io
  backendRefs:
    - name: mcp-backend-tool-integrity
      port: 1063
      toolIntegrity:
        digests:
          sum: %q
        onMismatch: Deny
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mcp-backend-tool-integrity
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mcp-backend-tool-integrity
  template:
    metadata:
      labels:
        app: mcp-backend-tool-integrity
    spec:
      containers:
        - name: mcp-backend-tool-integrity
          image: docker.io/envoyproxy/ai-gateway-testmcpserver:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 1063
---
apiVersion: v1
kind: Service
metadata:
  name: mcp-backend-tool-integrity
  namespace: default
spec:
  selector:
    app: mcp-backend-tool-integrity
  ports:
    - protocol: TCP
      port: 1063
      targetPort: 1063
  type: ClusterIP
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: envoy-mcp-gateway-tool-integrity
  namespace: default
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        container:
          # Clear the default memory/cpu requirements for local tests.
          resources: {}
`
