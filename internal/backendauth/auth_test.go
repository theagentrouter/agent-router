// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestNewHandler(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config *filterapi.BackendAuth
	}{
		{
			name: "AWSAuth",
			config: &filterapi.BackendAuth{AWSAuth: &filterapi.AWSAuth{
				Region: "us-west-2", CredentialFileLiteral: `
[default]
aws_access_key_id = test
aws_secret_access_key = test
`,
			}},
		},
		{
			name: "APIKey",
			config: &filterapi.BackendAuth{
				APIKey: &filterapi.APIKeyAuth{Key: "TEST"},
			},
		},
		{
			name: "AzureAuth",
			config: &filterapi.BackendAuth{
				AzureAuth: &filterapi.AzureAuth{AccessToken: "some-access-token"},
			},
		},
		{
			name: "GCPAuth",
			config: &filterapi.BackendAuth{
				GCPAuth: &filterapi.GCPAuth{
					AccessToken: "some-access-token",
					Region:      "some-region",
					ProjectName: "some-project",
				},
			},
		},
		{
			name: "AnthropicAPIKey",
			config: &filterapi.BackendAuth{
				AnthropicAPIKey: &filterapi.AnthropicAPIKeyAuth{Key: "TEST"},
			},
		},
		{
			name: "OpenAIAuth",
			config: &filterapi.BackendAuth{
				OpenAIAuth: &filterapi.OpenAIAuth{AccessToken: "some-access-token", Organization: "org-123"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHandler(t.Context(), tt.config)
			require.NoError(t, err)
		})
	}
}
