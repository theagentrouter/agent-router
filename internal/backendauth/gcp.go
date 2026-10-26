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
	isPassThrough  bool
}

func newGCPHandler(ctx context.Context, gcpAuth *filterapi.GCPAuth) (filterapi.BackendAuthHandler, error) {
	if gcpAuth == nil {
		return nil, fmt.Errorf("GCP auth configuration cannot be nil")
	}

	handler := &gcpHandler{
		region:        gcpAuth.Region,
		projectName:   gcpAuth.ProjectName,
		isPassThrough: gcpAuth.IsPassThrough,
	}

	if handler.isPassThrough {
		return handler, nil
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
//  2. Add an "Authorization" header with the GCP access token, unless the handler is in
//     pass-through mode, in which case the client's own "Authorization" header is used as-is.
//
// The ":path" header is expected to contain the API-specific suffix, which is injected by translator.requestBody.
// The suffix is combined with the generated prefix to form the complete path for the GCP API call.
func (g *gcpHandler) Do(_ context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	// Build the GCP URL prefix using the configured region and project name.
	projectName := g.projectName
	region := g.region
	if g.isPassThrough {
		// In pass-through mode the configured project/region take precedence when set,
		// and the per-request headers are used as a fallback.
		if projectName == "" {
			var ok bool
			if projectName, ok = requestHeaders["gcp-project"]; !ok {
				return nil, fmt.Errorf("gcp-project header must be specified")
			}
		}

		if region == "" {
			var ok bool
			if region, ok = requestHeaders["gcp-region"]; !ok {
				return nil, fmt.Errorf("gcp-region header must be specified")
			}
		}
	}

	prefixPath := fmt.Sprintf("/v1/projects/%s/locations/%s", projectName, region)
	// Find and update the ":path" header by prepending the prefix.
	path := requestHeaders["gcp-path"]

	if path == "" {
		return nil, fmt.Errorf("missing 'gcp-path' header in the request")
	}

	newPath := fmt.Sprintf("%s/%s", prefixPath, path)
	resultHeaders := []internalapi.Header{{":path", newPath}}

	if g.isPassThrough {
		return resultHeaders, nil
	}

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

	resultHeaders = append(resultHeaders, internalapi.Header{"Authorization", fmt.Sprintf("Bearer %s", accessToken)})
	// Add the Authorization header with the GCP access token.
	return resultHeaders, nil
}
