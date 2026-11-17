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

func TestNewOpenAIHandler(t *testing.T) {
	handler, err := newOpenAIHandler(&filterapi.OpenAIAuth{AccessToken: " some-access-token \n"})
	require.NoError(t, err)
	require.Equal(t, "some-access-token", handler.(*openAIHandler).accessToken)
}

func TestOpenAIHandler_Do(t *testing.T) {
	for _, tc := range []struct {
		name       string
		auth       filterapi.OpenAIAuth
		expHeaders []internalapi.Header
	}{
		{
			name:       "token only",
			auth:       filterapi.OpenAIAuth{AccessToken: "some-access-token"},
			expHeaders: []internalapi.Header{{"Authorization", "Bearer some-access-token"}},
		},
		{
			name: "organization and project",
			auth: filterapi.OpenAIAuth{AccessToken: "some-access-token", Organization: "org-123", Project: "proj_456"},
			expHeaders: []internalapi.Header{
				{"Authorization", "Bearer some-access-token"},
				{"OpenAI-Organization", "org-123"},
				{"OpenAI-Project", "proj_456"},
			},
		},
		{
			name: "project only",
			auth: filterapi.OpenAIAuth{AccessToken: "some-access-token", Project: "proj_456"},
			expHeaders: []internalapi.Header{
				{"Authorization", "Bearer some-access-token"},
				{"OpenAI-Project", "proj_456"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, err := newOpenAIHandler(&tc.auth)
			require.NoError(t, err)

			requestHeaders := map[string]string{":method": "POST", ":path": "/v1/chat/completions"}
			headers, err := handler.Do(t.Context(), requestHeaders, nil)
			require.NoError(t, err)
			require.Equal(t, tc.expHeaders, headers)
			for _, h := range tc.expHeaders {
				require.Equal(t, h[1], requestHeaders[h[0]])
			}
		})
	}
}
