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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// TestModelsHeaderScopeJWT reproduces envoyproxy/ai-gateway#2452 end-to-end:
// JWT sub is projected to x-jwt-sub via Envoy Gateway SecurityPolicy (claimToHeaders +
// recomputeRoute), and GET /v1/models returns only models for the matching tenant.
func TestModelsHeaderScopeJWT(t *testing.T) {
	const manifest = "testdata/models_header_scope_jwt.yaml"
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
	})

	const egSelector = "gateway.envoyproxy.io/owning-gateway-name=models-header-scope-jwt"
	e2elib.RequireWaitForGatewayPodReady(t, egSelector)

	fwd := e2elib.RequireNewHTTPPortForwarder(t, e2elib.EnvoyGatewayNamespace, egSelector, e2elib.EnvoyGatewayDefaultServicePort)
	defer fwd.Kill()

	t.Run("client-a models only", func(t *testing.T) {
		internaltesting.RequireEventuallyNoError(t, func() error {
			got, err := listModelIDs(t, fwd, makeTenantJWT(t, "client-a"))
			if err != nil {
				return err
			}
			if !sameStringSet(got, []string{"model-a1", "model-a2"}) {
				return fmt.Errorf("unexpected models for client-a: %v", got)
			}
			return nil
		}, 60*time.Second, 3*time.Second)
	})

	t.Run("client-b models only", func(t *testing.T) {
		internaltesting.RequireEventuallyNoError(t, func() error {
			got, err := listModelIDs(t, fwd, makeTenantJWT(t, "client-b"))
			if err != nil {
				return err
			}
			if !sameStringSet(got, []string{"model-b1", "model-b2"}) {
				return fmt.Errorf("unexpected models for client-b: %v", got)
			}
			return nil
		}, 60*time.Second, 3*time.Second)
	})

	t.Run("missing jwt is denied", func(t *testing.T) {
		internaltesting.RequireEventuallyNoError(t, func() error {
			status, err := modelsListStatus(t, fwd, "")
			if err != nil {
				return err
			}
			if status != http.StatusUnauthorized {
				return fmt.Errorf("expected 401 without JWT, got %d", status)
			}
			return nil
		}, 60*time.Second, 3*time.Second)
	})
}

func makeTenantJWT(t *testing.T, sub string) string {
	t.Helper()
	now := time.Now()
	return makeSignedJWTWithClaims(t, jwt.MapClaims{
		"iss": "https://auth-server.example.com",
		"sub": sub,
		"iat": now.Unix(),
		"exp": now.Add(30 * time.Minute).Unix(),
	})
}

func listModelIDs(t *testing.T, fwd e2elib.PortForwarder, token string) ([]string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fwd.Address()+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/models returned %d: %s", resp.StatusCode, string(body))
	}

	var models openai.ModelList
	if err := json.Unmarshal(body, &models); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(models.Data))
	for _, model := range models.Data {
		ids = append(ids, model.ID)
	}
	return ids, nil
}

func modelsListStatus(t *testing.T, fwd e2elib.PortForwarder, token string) (int, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fwd.Address()+"/v1/models", nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(got))
	for _, id := range got {
		seen[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}
