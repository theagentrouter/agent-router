// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package celvalidation

import (
	"embed"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/yaml"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
)

//go:embed testdata
var testdata embed.FS

func TestAIGatewayRoutes(t *testing.T) {
	c, _, _ := testsinternal.NewEnvTest(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name   string
		expErr string
	}{
		{name: "basic.yaml"},
		{name: "rule_name.yaml"},
		{
			name:   "duplicate_rule_names.yaml",
			expErr: "spec.rules: Invalid value: \"array\": rule name must be unique within the route",
		},
		{
			name:   "reserved_rule_name.yaml",
			expErr: "spec.rules[0]: Invalid value: \"object\": rule name route-not-found is reserved",
		},
		{name: "llmcosts.yaml"},
		{name: "parent_refs.yaml"},
		{name: "parent_refs_default_kind.yaml"},
		{
			name:   "parent_refs_invalid_kind.yaml",
			expErr: `spec.parentRefs: Invalid value: "array": only Gateway is supported`,
		},
		{name: "inference_pool_valid.yaml"},
		{
			name:   "inference_pool_mixed_backends.yaml",
			expErr: "spec.rules[0]: Invalid value: \"object\": cannot mix InferencePool and AIServiceBackend references in the same rule",
		},
		{
			name:   "inference_pool_multiple.yaml",
			expErr: "spec.rules[0]: Invalid value: \"object\": only one InferencePool backend is allowed per rule",
		},
		{
			name:   "inference_pool_partial_ref.yaml",
			expErr: "spec.rules[0].backendRefs[0]: Invalid value: \"object\": group and kind must be specified together",
		},
		{
			name:   "inference_pool_unsupported_group.yaml",
			expErr: "spec.rules[0].backendRefs[0]: Invalid value: \"object\": only InferencePool from inference.networking.k8s.io group is supported",
		},
		{
			name:   "too_many_rules.yaml",
			expErr: "spec.rules: Too many: 16: must have at most 15 items",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := testdata.ReadFile(path.Join("testdata/aigatewayroutes", tc.name))
			require.NoError(t, err)

			aiGatewayRoute := &aigv1b1.AIGatewayRoute{}
			err = yaml.UnmarshalStrict(data, aiGatewayRoute)
			require.NoError(t, err)

			if tc.expErr != "" {
				require.ErrorContains(t, c.Create(ctx, aiGatewayRoute), tc.expErr)
			} else {
				require.NoError(t, c.Create(ctx, aiGatewayRoute))
				require.NoError(t, c.Delete(ctx, aiGatewayRoute))
			}
		})
	}
}

func TestAIServiceBackends(t *testing.T) {
	c, _, _ := testsinternal.NewEnvTest(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name   string
		expErr string
	}{
		{name: "basic.yaml"},
		{name: "anthropic-schema.yaml"},
		{name: "basic-eg-backend-aws.yaml"},
		{name: "aws-openai-schema.yaml"},
		{name: "basic-eg-backend-azure.yaml"},
		{
			name:   "unknown_schema.yaml",
			expErr: "spec.schema.name: Unsupported value: \"SomeRandomVendor\": supported values: \"OpenAI\", \"Cohere\", \"AWSBedrock\", \"AzureOpenAI\", \"GCPVertexAI\", \"GCPAnthropic\", \"Anthropic\"",
		},
		{name: "k8s-svc.yaml", expErr: "BackendRef must be a Backend resource of Envoy Gateway"},
		{name: "header-value-filters-gcpanthropic.yaml"},
		{name: "header-value-filters-awsanthropic.yaml"},
		{
			name:   "header-value-filters-unsupported-schema.yaml",
			expErr: "headerValueFilters is only honored by GCPAnthropic and AWSAnthropic backends",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := testdata.ReadFile(path.Join("testdata/aiservicebackends", tc.name))
			require.NoError(t, err)

			aiBackend := &aigv1b1.AIServiceBackend{}
			err = yaml.UnmarshalStrict(data, aiBackend)
			require.NoError(t, err)

			if tc.expErr != "" {
				require.ErrorContains(t, c.Create(ctx, aiBackend), tc.expErr)
			} else {
				require.NoError(t, c.Create(ctx, aiBackend))
				require.NoError(t, c.Delete(ctx, aiBackend))
			}
		})
	}
}

func TestBackendSecurityPolicies(t *testing.T) {
	c, _, _ := testsinternal.NewEnvTest(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name   string
		expErr string
	}{
		{name: "basic.yaml"},
		{
			name:   "unknown_provider.yaml",
			expErr: "spec.type: Unsupported value: \"UnknownType\": supported values: \"APIKey\", \"AWSCredentials\", \"AzureAPIKey\", \"AzureCredentials\"",
		},
		{
			name:   "missing_type.yaml",
			expErr: "spec.type: Unsupported value: \"\": supported values: \"APIKey\", \"AWSCredentials\", \"AzureAPIKey\", \"AzureCredentials\"",
		},
		{
			name:   "multiple_security_policies.yaml",
			expErr: "When type is APIKey, only apiKey field should be set",
		},
		{
			name:   "azure_credentials_missing_client_id.yaml",
			expErr: "spec.azureCredentials.clientID in body should be at least 1 chars long",
		},
		{
			name:   "azure_credentials_missing_tenant_id.yaml",
			expErr: "spec.azureCredentials.tenantID in body should be at least 1 chars long",
		},
		{
			name:   "azure_missing_auth.yaml",
			expErr: "Exactly one of clientSecretRef or oidcExchangeToken must be specified",
		},
		{
			name:   "azure_multiple_auth.yaml",
			expErr: "Exactly one of clientSecretRef or oidcExchangeToken must be specified",
		},
		// CEL validation test cases - these should fail due to type mismatch.
		{
			name:   "apikey_with_aws_credentials.yaml",
			expErr: "When type is APIKey, only apiKey field should be set",
		},
		{
			name:   "apikey_with_azure_credentials.yaml",
			expErr: "When type is APIKey, only apiKey field should be set",
		},
		{
			name:   "apikey_with_gcp_credentials.yaml",
			expErr: "When type is APIKey, only apiKey field should be set",
		},
		{
			name:   "apikey_with_nil_configuration.yaml",
			expErr: "When type is APIKey, only apiKey field should be set",
		},
		{
			name:   "aws_with_azure_credentials.yaml",
			expErr: "When type is AWSCredentials, only awsCredentials field should be set",
		},
		{
			name:   "azure_with_gcp_credentials.yaml",
			expErr: "When type is AzureCredentials, only azureCredentials field should be set",
		},
		{
			name:   "gcp_with_apikey.yaml",
			expErr: "When type is GCPCredentials, only gcpCredentials field should be set",
		},
		{name: "azure_oidc.yaml"},
		{name: "azure_valid_credentials.yaml"},
		{name: "aws_credential_file.yaml"},
		{name: "aws_oidc.yaml"},
		// AWSCredentials used to be rejected outright by a CEL rule, because SigV4 needs three
		// inputs and the override plumbing carried one string.
		{name: "aws_credential_override.yaml"},
		{name: "gcp_oidc.yaml"},
		{name: "anthropic-apikey.yaml"},
		{name: "targetrefs_basic.yaml"},
		{name: "targetrefs_multiple.yaml"},
		{name: "targetrefs_inferencepool.yaml"},
		{name: "targetrefs_mixed.yaml"},
		{
			name:   "targetrefs_invalid_kind.yaml",
			expErr: "targetRefs must reference AIServiceBackend or InferencePool resources",
		},
		{
			name:   "targetrefs_invalid_group.yaml",
			expErr: "targetRefs must reference AIServiceBackend or InferencePool resources",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := testdata.ReadFile(path.Join("testdata/backendsecuritypolicies", tc.name))
			require.NoError(t, err)

			backendSecurityPolicy := &aigv1b1.BackendSecurityPolicy{}
			err = yaml.UnmarshalStrict(data, backendSecurityPolicy)
			require.NoError(t, err)

			if tc.expErr != "" {
				require.ErrorContains(t, c.Create(ctx, backendSecurityPolicy), tc.expErr)
			} else {
				require.NoError(t, c.Create(ctx, backendSecurityPolicy))
				require.NoError(t, c.Delete(ctx, backendSecurityPolicy))
			}
		})
	}
}

func TestMCPRoutes(t *testing.T) {
	c, _, _ := testsinternal.NewEnvTest(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name   string
		expErr string
	}{
		{name: "basic.yaml"},
		{
			name:   "same_backend_names.yaml",
			expErr: `MCPRoute.aigateway.envoyproxy.io "same-backend-names" is invalid: spec.backendRefs: Invalid value: "array": all backendRefs names must be unique`,
		},
		{
			name:   "parent_refs_invalid_kind.yaml",
			expErr: `spec.parentRefs: Invalid value: "array": only Gateway is supported`,
		},
		{
			name:   "tool_selector_missing.yaml",
			expErr: "spec.backendRefs[0].toolSelector: Invalid value: \"object\": at least one of include, includeRegex, exclude, or excludeRegex must be specified",
		},
		{
			name:   "tool_selector_both.yaml",
			expErr: "spec.backendRefs[0].toolSelector: Invalid value: \"object\": include and includeRegex are mutually exclusive",
		},
		{name: "tool_selector_exclude.yaml"},
		{name: "tool_selector_exclude_regex.yaml"},
		{name: "tool_selector_include_and_exclude.yaml"},
		{
			name:   "tool_selector_exclude_both.yaml",
			expErr: "spec.backendRefs[0].toolSelector: Invalid value: \"object\": exclude and excludeRegex are mutually exclusive",
		},
		{
			name:   "backend_api_key_inline_and_secret.yaml",
			expErr: "spec.backendRefs[0].securityPolicy.apiKey: Invalid value: \"object\": exactly one of secretRef or inline must be set",
		},
		{
			name:   "backend_api_key_missing.yaml",
			expErr: "spec.backendRefs[0].securityPolicy.apiKey: Invalid value: \"object\": exactly one of secretRef or inline must be set",
		},
		{
			name:   "backend_api_key_both_header_and_query.yaml",
			expErr: "only one of header or queryParam can be set",
		},
		{
			name:   "jwks_missing.yaml",
			expErr: "spec.securityPolicy.oauth.jwks: Invalid value: \"object\": either remoteJWKS or localJWKS must be specified.",
		},
		{
			name:   "jwks_both.yaml",
			expErr: "spec.securityPolicy.oauth.jwks: Invalid value: \"object\": remoteJWKS and localJWKS cannot both be specified.",
		},
		{
			name:   "authorization_with_jwt_without_oauth.yaml",
			expErr: "spec.securityPolicy: Invalid value: \"object\": oauth must be configured when any authorization rule uses a jwt source",
		},
		{
			name:   "authorization_claim_scope_reserved.yaml",
			expErr: "spec.securityPolicy.authorization.rules[0].source.jwt.claims: Invalid value: \"array\": 'scope' claim name is reserved for OAuth scopes",
		},
		{
			name:   "authorization_jwt_missing_scopes_and_claims.yaml",
			expErr: "spec.securityPolicy.authorization.rules[0].source.jwt: Invalid value: \"object\": either scopes or claims must be specified",
		},
		{name: "authorization_without_jwt_source.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := testdata.ReadFile(path.Join("testdata/mcpgatewayroutes", tc.name))
			require.NoError(t, err)

			mcpRoute := &aigv1b1.MCPRoute{}
			err = yaml.UnmarshalStrict(data, mcpRoute)
			require.NoError(t, err)

			if tc.expErr != "" {
				require.ErrorContains(t, c.Create(ctx, mcpRoute), tc.expErr)
			} else {
				require.NoError(t, c.Create(ctx, mcpRoute))
				require.NoError(t, c.Delete(ctx, mcpRoute))
			}
		})
	}
}
