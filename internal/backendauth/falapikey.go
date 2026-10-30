// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

type falAPIKeyHandler struct {
	apiKey string
}

func newFalAPIKeyHandler(auth *filterapi.FalAPIKeyAuth) (filterapi.BackendAuthHandler, error) {
	return &falAPIKeyHandler{apiKey: strings.TrimSpace(auth.Key)}, nil
}

// Do sets the Authorization header for fal API requests, which use the "Key" scheme instead of "Bearer".
//
// https://docs.fal.ai/model-apis/authentication
func (a *falAPIKeyHandler) Do(_ context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	return applyFalCredential(requestHeaders, a.apiKey)
}
