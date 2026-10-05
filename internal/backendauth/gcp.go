// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/gcpauth"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// gcpHTTPClient is used for GCP ADC token operations, with proxy support if configured.
var gcpHTTPClient = &http.Client{
	Transport: gcpauth.MustNewTransport(),
	Timeout:   10 * time.Second,
}

type gcpHandler struct {
	gcpAccessToken string             // The GCP access token used for authentication (static token).
	tokenSource    oauth2.TokenSource // Token source for ADC (auto-refreshing).
	region         string             // The GCP region to use for requests.
	projectName    string             // The GCP project to use for requests.
}

func newGCPHandler(ctx context.Context, gcpAuth *filterapi.GCPAuth) (filterapi.BackendAuthHandler, error) {
	if gcpAuth == nil {
		return nil, fmt.Errorf("GCP auth configuration cannot be nil")
	}

	handler := &gcpHandler{
		region:      gcpAuth.Region,
		projectName: gcpAuth.ProjectName,
	}

	if gcpAuth.AccessToken != "" {
		// Use provided static token
		handler.gcpAccessToken = gcpAuth.AccessToken
	} else {
		// Use ADC for GKE Workload Identity. TokenSource auto-refreshes in Do().
		// Inject HTTP client with proxy support into context for token operations.
		ctx = context.WithValue(ctx, oauth2.HTTPClient, gcpHTTPClient)
		creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("failed to find GCP default credentials: %w", err)
		}
		handler.tokenSource = creds.TokenSource
	}

	return handler, nil
}

// Do implements [Handler.Do].
//
// This method updates the request headers to:
//  1. Prepend the GCP API prefix to the ":path" header, constructing the full endpoint URL.
//  2. Add an "Authorization" header with the GCP access token.
//
// The ":path" header is expected to contain the API-specific suffix, which is injected by translator.requestBody.
// The suffix is combined with the generated prefix to form the complete path for the GCP API call.
func (g *gcpHandler) Do(_ context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	// Build the GCP URL prefix using the configured region and project name.
	prefixPath := fmt.Sprintf("/v1/projects/%s/locations/%s", g.projectName, g.region)
	// Find and update the ":path" header by prepending the prefix.
	path := requestHeaders[":path"]

	if path == "" {
		return nil, fmt.Errorf("missing ':path' header in the request")
	}

	newPath := fmt.Sprintf("%s/%s", prefixPath, path)

	// Get the access token
	var accessToken string
	if g.tokenSource != nil {
		token, err := g.tokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("failed to get GCP access token: %w", err)
		}
		accessToken = token.AccessToken
	} else {
		accessToken = g.gcpAccessToken
	}

	// Add the Authorization header with the GCP access token.
	requestHeaders[":path"] = newPath
	requestHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)
	return []internalapi.Header{{":path", newPath}, {"Authorization", fmt.Sprintf("Bearer %s", accessToken)}}, nil
}

// GCPTokenSource implements [filterapi.GCPAuthHandler].
// It returns the OAuth2 token source used to authenticate to GCP APIs.
// When a static access token is configured, it wraps it in a StaticTokenSource.
func (g *gcpHandler) GCPTokenSource() oauth2.TokenSource {
	if g.tokenSource != nil {
		return g.tokenSource
	}
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: g.gcpAccessToken})
}

// GCPRegion implements [filterapi.GCPAuthHandler].
func (g *gcpHandler) GCPRegion() string { return g.region }

// GCPProject implements [filterapi.GCPAuthHandler].
func (g *gcpHandler) GCPProject() string { return g.projectName }
