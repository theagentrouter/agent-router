// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// jwtSVIDFetcher fetches a JWT-SVID for the given audience from the SPIFFE Workload API.
type jwtSVIDFetcher func(ctx context.Context, audience string) (TokenExpiry, error)

// spiffeTokenProvider is a provider implements TokenProvider interface for SPIFFE JWT-SVIDs.
type spiffeTokenProvider struct {
	audience string
	fetch    jwtSVIDFetcher
}

// NewSPIFFETokenProvider creates a new TokenProvider that fetches JWT-SVIDs with the given audience
// from the SPIFFE Workload API.
//
// socketAddr is the Workload API address, e.g. "unix:///run/spire/sockets/agent.sock". When empty,
// the SPIFFE_ENDPOINT_SOCKET environment variable is used.
func NewSPIFFETokenProvider(socketAddr, audience string) (TokenProvider, error) {
	if audience == "" {
		return nil, errors.New("SPIFFE JWT-SVID audience must not be empty")
	}
	var opts []workloadapi.ClientOption
	if socketAddr != "" {
		opts = append(opts, workloadapi.WithAddr(socketAddr))
	}
	return &spiffeTokenProvider{
		audience: audience,
		fetch: func(ctx context.Context, audience string) (TokenExpiry, error) {
			svid, err := workloadapi.FetchJWTSVID(ctx, jwtsvid.Params{Audience: audience}, opts...)
			if err != nil {
				return TokenExpiry{}, err
			}
			return TokenExpiry{Token: svid.Marshal(), ExpiresAt: svid.Expiry}, nil
		},
	}, nil
}

// GetToken implements TokenProvider.GetToken method to retrieve a JWT-SVID and its expiration time.
func (s *spiffeTokenProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	token, err := s.fetch(ctx, s.audience)
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to fetch JWT-SVID from SPIFFE Workload API: %w", err)
	}
	return token, nil
}
