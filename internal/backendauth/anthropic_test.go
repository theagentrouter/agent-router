// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestNewAnthropicHandler(t *testing.T) {
	handler, err := newAnthropicHandler(&filterapi.AnthropicAuth{AccessToken: " sk-ant-oat01-token \n"})
	require.NoError(t, err)
	require.Equal(t, "sk-ant-oat01-token", handler.(*anthropicHandler).accessToken)
}

func TestAnthropicHandler_Do(t *testing.T) {
	handler, err := newAnthropicHandler(&filterapi.AnthropicAuth{AccessToken: "sk-ant-oat01-token"})
	require.NoError(t, err)

	requestHeaders := map[string]string{":method": "POST", ":path": "/v1/messages"}
	headers, err := handler.Do(t.Context(), requestHeaders, nil)
	require.NoError(t, err)
	require.Equal(t, []internalapi.Header{{"Authorization", "Bearer sk-ant-oat01-token"}}, headers)
	require.Equal(t, "Bearer sk-ant-oat01-token", requestHeaders["Authorization"])
	require.NotContains(t, requestHeaders, "x-api-key")
}
