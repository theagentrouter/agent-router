// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

type googleAPIKeyHandler struct {
	apiKey string
}

// Do implements [filterapi.BackendAuthHandler].
func (h *googleAPIKeyHandler) Do(_ context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	return applyGoogleAPIKeyCredential(requestHeaders, h.apiKey)
}

func applyGoogleAPIKeyCredential(requestHeaders map[string]string, credential string) ([]internalapi.Header, error) {
	requestHeaders[internalapi.GoogleAPIKeyHeader] = credential
	return []internalapi.Header{{internalapi.GoogleAPIKeyHeader, credential}}, nil
}
