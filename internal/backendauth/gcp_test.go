// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestNewGCPHandler(t *testing.T) {
	type wantError struct {
		check bool
		msg   string
	}
	testCases := []struct {
		name        string
		gcpAuth     *filterapi.GCPAuth
		wantHandler *gcpHandler
		wantError   wantError
	}{
		{
			name: "valid config with token",
			gcpAuth: &filterapi.GCPAuth{
				AccessToken: "test-token",
				Region:      "us-central1",
				ProjectName: "test-project",
			},
			wantHandler: &gcpHandler{
				gcpAccessToken: "test-token",
				region:         "us-central1",
				projectName:    "test-project",
			},
		},
		{
			name: "empty token uses ADC",
			gcpAuth: &filterapi.GCPAuth{
				Region:      "us-central1",
				ProjectName: "test-project",
			},
			wantError: wantError{check: true, msg: "failed to find GCP default credentials"},
		},
		{
			name:      "nil config",
			gcpAuth:   nil,
			wantError: wantError{check: true, msg: "GCP auth configuration cannot be nil"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			handler, err := newGCPHandler(ctx, tc.gcpAuth)
			if err != nil {
				require.True(t, tc.wantError.check, "unexpected error: %v", err)
				require.ErrorContains(t, err, tc.wantError.msg)
			} else {
				require.NotNil(t, handler)
				if tc.wantHandler != nil {
					if d := cmp.Diff(tc.wantHandler, handler, cmp.AllowUnexported(gcpHandler{})); d != "" {
						t.Errorf("Handler mismatch (-want +got):\n%s", d)
					}
				}
			}
		})
	}
}

func TestGCPHandler_Do(t *testing.T) {
	handler := &gcpHandler{
		gcpAccessToken: "test-token",
		region:         "us-central1",
		projectName:    "test-project",
	}
	testCases := []struct {
		name             string
		handler          *gcpHandler
		requestHeaders   map[string]string
		wantPathValue    string
		wantPathRawValue []byte
	}{
		{
			name:    "basic headers update",
			handler: handler,
			requestHeaders: map[string]string{
				":path": "publishers/google/models/gemini-pro:generateContent",
			},
			wantPathValue: "/v1/projects/test-project/locations/us-central1/publishers/google/models/gemini-pro:generateContent",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			hdrs, err := tc.handler.Do(ctx, tc.requestHeaders, nil)
			require.NoError(t, err)

			expectedAuthHeader := fmt.Sprintf("Bearer %s", tc.handler.gcpAccessToken)

			hdrsMap := stringPairsToMap(hdrs)
			authValue, ok := hdrsMap["Authorization"]
			require.True(t, ok, "Authorization header not found in returned headers")
			require.Equal(t, expectedAuthHeader, authValue, "Authorization header value mismatch")

			pathValue, ok := hdrsMap[":path"]
			require.True(t, ok, ":path header not found in returned headers")
			require.Equal(t, tc.wantPathValue, pathValue, ":path header value mismatch")
		})
	}
}

type mockTokenSource struct {
	token string
	err   error
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &oauth2.Token{
		AccessToken: m.token,
		Expiry:      time.Now().Add(time.Hour),
	}, nil
}

func TestGCPHandler_Do_WithTokenSource(t *testing.T) {
	handler := &gcpHandler{
		tokenSource: &mockTokenSource{token: "adc-token"},
		region:      "us-central1",
		projectName: "test-project",
	}

	requestHeaders := map[string]string{
		":path": "publishers/google/models/gemini-pro:generateContent",
	}

	hdrs, err := handler.Do(context.Background(), requestHeaders, nil)
	require.NoError(t, err)

	hdrsMap := stringPairsToMap(hdrs)
	require.Equal(t, "Bearer adc-token", hdrsMap["Authorization"])
	require.Equal(t, "/v1/projects/test-project/locations/us-central1/publishers/google/models/gemini-pro:generateContent", hdrsMap[":path"])
}

func TestGCPHandler_Do_TokenSourceError(t *testing.T) {
	handler := &gcpHandler{
		tokenSource: &mockTokenSource{err: fmt.Errorf("token refresh failed")},
		region:      "us-central1",
		projectName: "test-project",
	}

	requestHeaders := map[string]string{
		":path": "publishers/google/models/gemini-pro:generateContent",
	}

	_, err := handler.Do(context.Background(), requestHeaders, nil)
	require.ErrorContains(t, err, "failed to get GCP access token")
}

// The handler is built while a configuration loads, and the watcher cancels
// that context the moment the load returns (filterapi.bundleConfigWatcher).
// The ADC token source must not inherit the cancellation: external_account
// credentials exchange tokens over HTTP under the context they were created
// with, so every later exchange would fail with "context canceled".
func TestNewGCPHandler_ADCTokenSourceOutlivesLoadContext(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"exchanged-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(sts.Close)

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("projected-token"), 0o600))
	credFile := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(credFile, fmt.Appendf(nil, `{
  "type": "external_account",
  "audience": "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/pool/providers/oidc",
  "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
  "token_url": %q,
  "credential_source": {"file": %q}
}`, sts.URL+"/v1/token", tokenFile), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)

	loadCtx, cancelLoad := context.WithCancel(context.Background())
	handler, err := newGCPHandler(loadCtx, &filterapi.GCPAuth{Region: "us-central1", ProjectName: "test-project"})
	require.NoError(t, err)
	cancelLoad()

	hdrs, err := handler.Do(context.Background(), map[string]string{":path": "publishers/google/models/gemini-pro:generateContent"}, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer exchanged-token", stringPairsToMap(hdrs)["Authorization"])
}
