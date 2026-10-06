// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"crypto/sha256"
	stdjson "encoding/json" //nolint: depguard // byte-stable hashing; sonic does not guarantee stable field order.
	"encoding/hex"
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

// resourceIntegrityDigestPayload mirrors internal/mcpproxy.resourceDigestPayload: the
// canonical, deterministically-ordered representation of a resource's content that a
// configured digest is computed over. Duplicated here (rather than importing the internal
// package, whose relevant function is unexported) so the manifest below can be built with a
// digest computed the exact same way the gateway computes it.
type resourceIntegrityDigestPayload struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}

// dummyResourceURI must match tests/internal/testmcp/resources.go's DummyResource exactly:
// the deployed backend pod runs that same handler, so its resources/read response for this
// URI is always the same content.
const dummyResourceURI = "file:///dummy.txt"

// requireLiveDummyResourceDigest starts a local, unproxied instance of the exact same test
// MCP server binary the e2e cluster deploys, reads DummyResource from it directly, and
// computes its content digest from the real wire-format response. A first attempt at this
// test hand-derived the expected digest from the DummyResourceHandler source instead of a
// live round trip, assuming MIMEType would be empty on the wire; it wasn't (the SDK
// populates ResourceContents.MIMEType from the resource's declared MIMEType when the handler
// doesn't set one), and the test failed against a real cluster. This is exactly the class of
// assumption a live probe exists to catch -- see the equivalent tool-digest helper in the
// sibling toolIntegrity PR for the same reasoning applied to tools/list.
func requireLiveDummyResourceDigest(t *testing.T) ([]byte, string) {
	t.Helper()
	port := internaltesting.RequireRandomPorts(t, 1)[0]
	srv, _ := testmcp.NewServer(&testmcp.Options{Port: port, DisableLog: true})
	t.Cleanup(func() { _ = srv.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "resource-digest-probe-client", Version: "0.1.0"}, nil)
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

	result, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: dummyResourceURI})
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	rc := result.Contents[0]

	payload := resourceIntegrityDigestPayload{URI: rc.URI, MIMEType: rc.MIMEType, Text: rc.Text, Blob: rc.Blob}
	data, err := stdjson.Marshal(payload)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return rc.Blob, hex.EncodeToString(sum[:])
}

// TestMCPRouteResourceIntegrity verifies opt-in content-digest verification of backend MCP
// resources at resources/read time: a resource with a matching configured digest is
// returned normally, and one with a mismatching digest is rejected with an error instead of
// its (drifted) content.
func TestMCPRouteResourceIntegrity(t *testing.T) {
	expectedBlob, correctDigest := requireLiveDummyResourceDigest(t)
	wrongDigest := strings.Repeat("0", 64)

	manifest := fmt.Sprintf(resourceIntegrityManifestTemplate, correctDigest, wrongDigest)
	require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifestStdin(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=mcp-gateway-resource-integrity"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	client := mcp.NewClient(&mcp.Implementation{Name: "resource-integrity-e2e-client", Version: "0.1.0"}, nil)
	// The gateway rewrites a non-ui:// resource URI to "<backend>+<uri>" for downstream
	// callers (see downstreamResourceURI); this is that rewrite applied by hand so the read
	// below targets the right backend without needing a resources/list round trip first.
	downstreamURI := "mcp-backend-resource-integrity+" + dummyResourceURI

	t.Run("matching digest: resource is returned normally", func(t *testing.T) {
		sess := requireConnectMCP(t.Context(), t, client, fwd.Address()+"/mcp-resource-integrity-match", nil)
		t.Cleanup(func() { _ = sess.Close() })

		result, err := sess.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: downstreamURI})
		require.NoError(t, err)
		require.Len(t, result.Contents, 1)
		require.Equal(t, expectedBlob, result.Contents[0].Blob)
	})

	t.Run("mismatching digest: read is rejected", func(t *testing.T) {
		sess := requireConnectMCP(t.Context(), t, client, fwd.Address()+"/mcp-resource-integrity-mismatch", nil)
		t.Cleanup(func() { _ = sess.Close() })

		_, err := sess.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: downstreamURI})
		require.Error(t, err, "a resource whose content digest doesn't match its configured expectation must be rejected")
	})
}

const resourceIntegrityManifestTemplate = `
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: mcp-gateway-class-resource-integrity
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mcp-gateway-resource-integrity
  namespace: default
spec:
  gatewayClassName: mcp-gateway-class-resource-integrity
  listeners:
    - name: http
      protocol: HTTP
      port: 80
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: envoy-mcp-gateway-resource-integrity
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: mcp-route-resource-integrity-match
  namespace: default
spec:
  path: "/mcp-resource-integrity-match"
  parentRefs:
    - name: mcp-gateway-resource-integrity
      kind: Gateway
      group: gateway.networking.k8s.io
  backendRefs:
    - name: mcp-backend-resource-integrity
      port: 1063
      resourceIntegrity:
        digests:
          "file:///dummy.txt": %q
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: mcp-route-resource-integrity-mismatch
  namespace: default
spec:
  path: "/mcp-resource-integrity-mismatch"
  parentRefs:
    - name: mcp-gateway-resource-integrity
      kind: Gateway
      group: gateway.networking.k8s.io
  backendRefs:
    - name: mcp-backend-resource-integrity
      port: 1063
      resourceIntegrity:
        digests:
          "file:///dummy.txt": %q
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mcp-backend-resource-integrity
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mcp-backend-resource-integrity
  template:
    metadata:
      labels:
        app: mcp-backend-resource-integrity
    spec:
      containers:
        - name: mcp-backend-resource-integrity
          image: docker.io/envoyproxy/ai-gateway-testmcpserver:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 1063
---
apiVersion: v1
kind: Service
metadata:
  name: mcp-backend-resource-integrity
  namespace: default
spec:
  selector:
    app: mcp-backend-resource-integrity
  ports:
    - protocol: TCP
      port: 1063
      targetPort: 1063
  type: ClusterIP
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: envoy-mcp-gateway-resource-integrity
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
