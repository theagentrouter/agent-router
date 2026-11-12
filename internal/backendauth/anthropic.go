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

type anthropicHandler struct {
	accessToken string
}

func newAnthropicHandler(auth *filterapi.AnthropicAuth) (filterapi.BackendAuthHandler, error) {
	return &anthropicHandler{accessToken: strings.TrimSpace(auth.AccessToken)}, nil
}

// Do implements [Handler.Do].
//
// Sets the Anthropic access token obtained via workload identity federation as a bearer token. Unlike an
// Anthropic API key, it is not sent in the "x-api-key" header.
//
// https://platform.claude.com/docs/en/manage-claude/wif-reference#token-exchange-response
func (a *anthropicHandler) Do(_ context.Context, requestHeaders map[string]string, _ []byte) ([]internalapi.Header, error) {
	return applyBearerCredential(requestHeaders, a.accessToken)
}
