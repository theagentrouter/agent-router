// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// Test_CredentialOverrideFromDynamicMetadata checks the whole fromDynamicMetadata chain on a real
// cluster: ext_authz returns a per-tenant API key as dynamic metadata and the upstream must see
// that key, not the static one. The GatewayConfig is applied mid-test, so the declaration
// reaching the running gateway is covered too.
func Test_CredentialOverrideFromDynamicMetadata(t *testing.T) {
	const manifest = "testdata/credential_override_metadata.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-credential-override"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)
	e2elib.RequireWaitForPodReady(t, "default", "app=ext-auth-server-credential-override")
	e2elib.RequireWaitForPodReady(t, "default", "app=envoy-ai-gateway-credential-override-testupstream")

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	// doRequest asserts which Authorization header the upstream received. The testupstream answers
	// 400 on a mismatch, so callers poll until it holds.
	doRequest := func(t *testing.T, tenantID, wantAuthorization string) (int, string, error) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, fwd.Address()+"/v1/chat/completions", strings.NewReader(
			`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"credential-override-model"}`))
		require.NoError(t, err)
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(
			`{"choices":[{"message":{"content":"This is a test."}}]}`)))
		req.Header.Set(testupstreamlib.ExpectedHeadersKey, base64.StdEncoding.EncodeToString([]byte(
			"Authorization:"+wantAuthorization)))
		if tenantID != "" {
			req.Header.Set("x-tenant-id", tenantID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, "", err
		}
		return resp.StatusCode, string(body), nil
	}

	requireEventuallyAuthorized := func(t *testing.T, tenantID, wantAuthorization string) {
		t.Helper()
		require.Eventually(t, func() bool {
			status, body, err := doRequest(t, tenantID, wantAuthorization)
			if err != nil {
				t.Logf("request error: %v", err)
				return false
			}
			if status != http.StatusOK {
				t.Logf("status %d: %s", status, body)
				return false
			}
			return true
		}, 3*time.Minute, 2*time.Second)
	}

	// The namespace is not declared yet, so nothing is forwarded and the static credential is used.
	requireEventuallyAuthorized(t, "tenant-a", "Bearer static-configured-key")

	// Declaring the namespace must reach the running gateway on its own.
	const gwConfigManifest = "testdata/credential_override_metadata_gwconfig.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), gwConfigManifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), gwConfigManifest)
	})
	requireEventuallyAuthorized(t, "tenant-a", "Bearer tenant-a-per-request-key")

	// A tenant the auth server has no entry for falls back to the static credential.
	requireEventuallyAuthorized(t, "tenant-unknown", "Bearer static-configured-key")

	// So does a request with no tenant header at all.
	requireEventuallyAuthorized(t, "", "Bearer static-configured-key")
}
