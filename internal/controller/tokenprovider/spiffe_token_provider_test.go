// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spiffe/go-spiffe/v2/proto/spiffe/workload"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testSPIFFEID = "spiffe://example.org/ns/envoy-ai-gateway-system/sa/ai-gateway-controller"

func TestNewSPIFFETokenProvider(t *testing.T) {
	t.Run("empty audience", func(t *testing.T) {
		provider, err := NewSPIFFETokenProvider("unix:///tmp/agent.sock", "")
		require.ErrorContains(t, err, "audience must not be empty")
		require.Nil(t, provider)
	})

	t.Run("valid", func(t *testing.T) {
		provider, err := NewSPIFFETokenProvider("unix:///tmp/agent.sock", "openai")
		require.NoError(t, err)
		require.IsType(t, &spiffeTokenProvider{}, provider)
	})

	t.Run("empty socket address", func(t *testing.T) {
		provider, err := NewSPIFFETokenProvider("", "openai")
		require.NoError(t, err)
		require.IsType(t, &spiffeTokenProvider{}, provider)
	})
}

func TestSPIFFETokenProvider_GetToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		expiresAt := time.Now().Add(5 * time.Minute)
		var gotAudience string
		provider := &spiffeTokenProvider{
			audience: "openai",
			fetch: func(_ context.Context, audience string) (TokenExpiry, error) {
				gotAudience = audience
				return TokenExpiry{Token: "svid", ExpiresAt: expiresAt}, nil
			},
		}
		token, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, "openai", gotAudience)
		require.Equal(t, TokenExpiry{Token: "svid", ExpiresAt: expiresAt}, token)
	})

	t.Run("fetch error", func(t *testing.T) {
		provider := &spiffeTokenProvider{
			audience: "openai",
			fetch: func(context.Context, string) (TokenExpiry, error) {
				return TokenExpiry{}, errors.New("connection refused")
			},
		}
		_, err := provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to fetch JWT-SVID from SPIFFE Workload API: connection refused")
	})
}

func TestSPIFFETokenProvider_GetToken_WorkloadAPI(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	expiresAt := time.Now().Add(5 * time.Minute).Truncate(time.Second).UTC()

	server := &fakeWorkloadAPIServer{key: key, expiresAt: expiresAt}
	addr := startFakeWorkloadAPI(t, server)

	t.Run("explicit socket address", func(t *testing.T) {
		provider, err := NewSPIFFETokenProvider(addr, "openai")
		require.NoError(t, err)
		token, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.Equal(t, expiresAt, token.ExpiresAt)
		require.Equal(t, []string{"openai"}, server.lastAudience)

		claims := jwt.RegisteredClaims{}
		_, err = jwt.ParseWithClaims(token.Token, &claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
		require.NoError(t, err)
		require.Equal(t, testSPIFFEID, claims.Subject)
		require.Equal(t, jwt.ClaimStrings{"openai"}, claims.Audience)
	})

	t.Run("socket address from SPIFFE_ENDPOINT_SOCKET", func(t *testing.T) {
		t.Setenv("SPIFFE_ENDPOINT_SOCKET", addr)
		provider, err := NewSPIFFETokenProvider("", "other-audience")
		require.NoError(t, err)
		token, err := provider.GetToken(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, token.Token)
		require.Equal(t, []string{"other-audience"}, server.lastAudience)
	})

	t.Run("workload API error", func(t *testing.T) {
		provider, err := NewSPIFFETokenProvider(addr, "denied")
		require.NoError(t, err)
		_, err = provider.GetToken(t.Context())
		require.ErrorContains(t, err, "failed to fetch JWT-SVID from SPIFFE Workload API")
		require.ErrorContains(t, err, "no identity issued")
	})
}

// fakeWorkloadAPIServer issues JWT-SVIDs signed by key for testSPIFFEID.
type fakeWorkloadAPIServer struct {
	workload.UnimplementedSpiffeWorkloadAPIServer
	key          *ecdsa.PrivateKey
	expiresAt    time.Time
	lastAudience []string
}

func (f *fakeWorkloadAPIServer) FetchJWTSVID(_ context.Context, req *workload.JWTSVIDRequest) (*workload.JWTSVIDResponse, error) {
	f.lastAudience = req.Audience
	if len(req.Audience) == 1 && req.Audience[0] == "denied" {
		return nil, status.Error(codes.PermissionDenied, "no identity issued")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		Subject:   testSPIFFEID,
		Audience:  req.Audience,
		ExpiresAt: jwt.NewNumericDate(f.expiresAt),
	})
	signed, err := token.SignedString(f.key)
	if err != nil {
		return nil, err
	}
	return &workload.JWTSVIDResponse{Svids: []*workload.JWTSVID{{SpiffeId: testSPIFFEID, Svid: signed}}}, nil
}

// startFakeWorkloadAPI serves the given server on a unix socket and returns its address.
func startFakeWorkloadAPI(t *testing.T, server workload.SpiffeWorkloadAPIServer) string {
	t.Helper()
	// Use a short directory since unix socket paths are limited to ~104 bytes on macOS.
	dir, err := os.MkdirTemp("", "spiffe")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "agent.sock")

	lis, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	s := grpc.NewServer()
	workload.RegisterSpiffeWorkloadAPIServer(s, server)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return "unix://" + socketPath
}
