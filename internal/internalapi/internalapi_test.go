// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internalapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEndpointPrefixes_Success(t *testing.T) {
	in := "openai:/foo,cohere:/1/2/3,anthropic:/cat"
	ep, err := ParseEndpointPrefixes(in)
	require.NoError(t, err)
	require.Equal(t, "/foo", ep.OpenAI)
	require.Equal(t, "/1/2/3", ep.Cohere)
	require.Equal(t, "/cat", ep.Anthropic)
}

func TestParseEndpointPrefixes_EmptyInput(t *testing.T) {
	ep, err := ParseEndpointPrefixes("")
	require.NoError(t, err)
	require.Equal(t, "/", ep.OpenAI)
	require.Equal(t, "/cohere", ep.Cohere)
	require.Equal(t, "/anthropic", ep.Anthropic)
}

func TestParseEndpointPrefixes_UnknownKey(t *testing.T) {
	_, err := ParseEndpointPrefixes("unknown:/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown endpointPrefixes key")
}

func TestParseEndpointPrefixes_EmptyValue(t *testing.T) {
	ep, err := ParseEndpointPrefixes("openai:")
	require.NoError(t, err)
	require.Empty(t, ep.OpenAI)
}

func TestParseEndpointPrefixes_MissingColon(t *testing.T) {
	_, err := ParseEndpointPrefixes("openai")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected format: key:value")
}

func TestParseEndpointPrefixes_EmptyPair(t *testing.T) {
	_, err := ParseEndpointPrefixes("openai:/,,cohere:/cohere")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty endpointPrefixes pair at position 2")
}

func TestPerRouteRuleRefBackendName(t *testing.T) {
	tests := []struct {
		name           string
		namespace      string
		backendName    string
		routeName      string
		routeRuleIndex int
		refIndex       int
		expected       string
	}{
		{
			name:           "basic case",
			namespace:      "default",
			backendName:    "backend1",
			routeName:      "route1",
			routeRuleIndex: 0,
			refIndex:       0,
			expected:       "default/backend1/route/route1/rule/0/ref/0",
		},
		{
			name:           "different namespace",
			namespace:      "test-ns",
			backendName:    "my-backend",
			routeName:      "my-route",
			routeRuleIndex: 2,
			refIndex:       1,
			expected:       "test-ns/my-backend/route/my-route/rule/2/ref/1",
		},
		{
			name:           "with special characters",
			namespace:      "ns-with-dash",
			backendName:    "backend_with_underscore",
			routeName:      "route-with-dash",
			routeRuleIndex: 10,
			refIndex:       5,
			expected:       "ns-with-dash/backend_with_underscore/route/route-with-dash/rule/10/ref/5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PerRouteRuleRefBackendName(tt.namespace, tt.backendName, tt.routeName, tt.routeRuleIndex, tt.refIndex)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestConstants(t *testing.T) {
	// Test that constants have expected values
	require.Equal(t, "aigateway.envoy.io", InternalEndpointMetadataNamespace)
	require.Equal(t, "per_route_rule_backend_name", InternalMetadataBackendNameKey)
	require.Equal(t, "aigw_route_name", InternalMetadataRouteNameKey)
	require.Equal(t, "x-gateway-destination-endpoint", EndpointPickerHeaderKey)
	require.Equal(t, "xds.route_metadata.filter_metadata['aigateway.envoy.io']['aigw_route_name']", XDSRouteMetadataRouteNamePath)
}

func TestParseRequestHeaderAttributeMapping(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  map[string]string
		expectErr bool
	}{
		{
			name:      "empty string",
			input:     "",
			expected:  nil,
			expectErr: false,
		},
		{
			name:      "single valid pair",
			input:     "agent-session-id:session.id",
			expected:  map[string]string{"agent-session-id": "session.id"},
			expectErr: false,
		},
		{
			name:      "multiple valid pairs",
			input:     "agent-session-id:session.id,x-tenant-id:tenant.id",
			expected:  map[string]string{"agent-session-id": "session.id", "x-tenant-id": "tenant.id"},
			expectErr: false,
		},
		{
			name:      "with whitespace",
			input:     " agent-session-id : session.id , x-tenant-id : tenant.id ",
			expected:  map[string]string{"agent-session-id": "session.id", "x-tenant-id": "tenant.id"},
			expectErr: false,
		},
		{
			name:      "invalid format - missing colon",
			input:     "agent-session-id",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "invalid format - empty header",
			input:     ":session.id",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "invalid format - empty attribute",
			input:     "agent-session-id:",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "multiple colons - takes first colon",
			input:     "agent-session-id:session.id:extra",
			expected:  map[string]string{"agent-session-id": "session.id:extra"},
			expectErr: false,
		},
		{
			name:      "trailing comma - should fail",
			input:     "agent-session-id:session.id,",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "double comma - should fail",
			input:     "agent-session-id:session.id,,x-tenant-id:tenant.id",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "comma with spaces - should fail",
			input:     "agent-session-id : session.id , , x-tenant-id : tenant.id",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "leading comma - should fail",
			input:     ",agent-session-id:session.id",
			expected:  nil,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseRequestHeaderAttributeMapping(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestMergeRequestHeaderAttributeMappings(t *testing.T) {
	tests := []struct {
		name     string
		base     map[string]string
		override map[string]string
		expected map[string]string
	}{
		{
			name:     "both empty",
			base:     nil,
			override: nil,
			expected: nil,
		},
		{
			name:     "base only",
			base:     map[string]string{"x-tenant-id": "tenant.id"},
			override: nil,
			expected: map[string]string{"x-tenant-id": "tenant.id"},
		},
		{
			name:     "override only",
			base:     nil,
			override: map[string]string{"agent-session-id": "session.id"},
			expected: map[string]string{"agent-session-id": "session.id"},
		},
		{
			name:     "override wins",
			base:     map[string]string{"x-tenant-id": "tenant.id", "agent-session-id": "old.session.id"},
			override: map[string]string{"agent-session-id": "session.id"},
			expected: map[string]string{"x-tenant-id": "tenant.id", "agent-session-id": "session.id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := MergeRequestHeaderAttributeMappings(tt.base, tt.override)
			require.Equal(t, tt.expected, actual)
		})
	}
}

func TestFormatRequestHeaderAttributeMapping(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected string
	}{
		{name: "nil", input: nil, expected: ""},
		{name: "empty", input: map[string]string{}, expected: ""},
		{
			name:     "sorted output",
			input:    map[string]string{"x-tenant-id": "tenant.id", "agent-session-id": "session.id"},
			expected: "agent-session-id:session.id,x-tenant-id:tenant.id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, FormatRequestHeaderAttributeMapping(tt.input))
		})
	}
}

func TestAIServiceBackendName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "per-route rule ref backend name",
			input:    PerRouteRuleRefBackendName("default", "some-backend", "some-route", 0, 1),
			expected: "default/some-backend",
		},
		{
			name:     "namespace and name only",
			input:    "default/some-backend",
			expected: "default/some-backend",
		},
		{
			name:     "no separator",
			input:    "some-backend",
			expected: "some-backend",
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, AIServiceBackendName(tt.input))
		})
	}
}

// Recognized forms, all case-sensitive lowercase:
//   - Public: bedrock-runtime.<region>.amazonaws.com
//   - Public FIPS: bedrock-runtime-fips.<region>.amazonaws.com
//   - PrivateLink (VPCE): <vpce-id>.bedrock-runtime.<region>.vpce.amazonaws.com
//   - PrivateLink FIPS: <vpce-id>.bedrock-runtime-fips.<region>.vpce.amazonaws.com
//   - Newer API domain: bedrock-runtime.<region>.api.aws
func TestAWSBedrockRegionFromHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		want string
	}{
		{"public bedrock host", "bedrock-runtime.us-east-1.amazonaws.com", "us-east-1"},
		{"vpce host", "vpce-123.bedrock-runtime.us-west-2.vpce.amazonaws.com", "us-west-2"},
		{"prefixed bedrock host", "aaa.bedrock-runtime.eu-central-1.amazonaws.com", "eu-central-1"},
		{"fips public bedrock host", "bedrock-runtime-fips.us-east-1.amazonaws.com", "us-east-1"},
		{"fips vpce host", "vpce-123.bedrock-runtime-fips.us-west-2.vpce.amazonaws.com", "us-west-2"},
		{"api.aws domain host", "bedrock-runtime.ap-southeast-2.api.aws", "ap-southeast-2"},
		{"non-bedrock host", "api.openai.com", ""},
		{"custom internal host has no derivable region", "bedrock.corp.internal", ""},
		{"spoofed suffix is rejected", "bedrock-runtime.us-east-1.amazonaws.com.evil.com", ""},
		{"spoofed api.aws suffix is rejected", "bedrock-runtime.us-east-1.api.aws.evil.com", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AWSBedrockRegionFromHost(tc.host))
		})
	}
}
