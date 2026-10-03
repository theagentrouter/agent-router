// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// Test_validateCmd tests the full pipeline using real YAML files.
func Test_validateCmd(t *testing.T) {
	tests := []struct {
		name          string
		paths         []string
		expectErr     string
		expectOutputs []string
	}{
		{
			name:  "valid config passes",
			paths: []string{"testdata/validate_valid.yaml"},
			expectOutputs: []string{
				`OK    AIGatewayRoute "default/test-route"`,
				`OK    AIServiceBackend "default/test-openai"`,
				`OK    AIServiceBackend "default/test-anthropic"`,
				`OK    BackendSecurityPolicy "default/test-openai-apikey"`,
				`OK    BackendSecurityPolicy "default/test-anthropic-apikey"`,
				"All resources are valid.",
			},
		},
		{
			name:      "invalid config reports all errors",
			paths:     []string{"testdata/validate_invalid.yaml"},
			expectErr: "validation failed:",
			expectOutputs: []string{
				`ERROR AIGatewayRoute "default/bad-route":`,
				`ERROR AIServiceBackend "default/bad-backend":`,
				`ERROR BackendSecurityPolicy "default/bad-policy":`,
			},
		},
		{
			name:  "duplicate resources across files are deduplicated",
			paths: []string{"testdata/validate_valid.yaml", "testdata/validate_valid.yaml"},
			expectOutputs: []string{
				"Validating 5 resource(s) from 2 file(s)",
				"All resources are valid.",
			},
		},
		{
			name:      "nonexistent file returns error before validation",
			paths:     []string{"/nonexistent/config.yaml"},
			expectErr: "error reading file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			err := validateCmd(t.Context(), &cmdValidate{Paths: tt.paths}, stdout, os.Stderr)

			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				require.NoError(t, err)
			}

			out := stdout.String()
			for _, want := range tt.expectOutputs {
				assert.Contains(t, out, want, "output should contain %q\nfull output:\n%s", want, out)
			}
		})
	}
}

// Test_doMain_validate tests the validate subcommand wiring through doMain.
func Test_doMain_validate(t *testing.T) {
	t.Run("no paths exits with code 80", func(t *testing.T) {
		out := &bytes.Buffer{}
		require.PanicsWithValue(t, 80, func() {
			doMain(t.Context(), out, os.Stderr, []string{"validate"},
				func(code int) { panic(code) }, nil, nil, nil, validateCmd)
		})
	})

	t.Run("valid file succeeds", func(t *testing.T) {
		out := &bytes.Buffer{}
		doMain(t.Context(), out, os.Stderr, []string{"validate", "testdata/validate_valid.yaml"},
			nil, nil, nil, nil, validateCmd)
		assert.Contains(t, out.String(), "All resources are valid.")
	})
}

// Test_checkAIGatewayRoute tests AIGatewayRoute validation rules.
func Test_checkAIGatewayRoute(t *testing.T) {
	backendSet := map[string]bool{"default/my-backend": true}

	t.Run("no rules", func(t *testing.T) {
		route := &aigv1b1.AIGatewayRoute{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"}}
		issues := checkAIGatewayRoute(route, nil)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.rules", issues[0].field)
	})

	t.Run("valid route passes", func(t *testing.T) {
		route := helperRoute("r", "default", "my-backend")
		assert.Empty(t, checkAIGatewayRoute(route, backendSet))
	})

	t.Run("wrong parentRef kind", func(t *testing.T) {
		route := helperRoute("r", "default", "my-backend")
		wrong := gwapiv1.Kind("HTTPRoute")
		route.Spec.ParentRefs = []gwapiv1.ParentReference{{Kind: &wrong, Name: "gw"}}
		issues := checkAIGatewayRoute(route, backendSet)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].field, "parentRefs[0]")
		assert.Contains(t, issues[0].message, "HTTPRoute")
	})

	t.Run("dangling backend reference", func(t *testing.T) {
		route := helperRoute("r", "default", "ghost")
		issues := checkAIGatewayRoute(route, backendSet)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, "ghost")
	})

	t.Run("mixed InferencePool and AIServiceBackend in same rule", func(t *testing.T) {
		g := "inference.networking.k8s.io"
		k := "InferencePool"
		route := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "my-backend"},
						{Name: "my-pool", Group: &g, Kind: &k},
					},
				}},
			},
		}
		issues := checkAIGatewayRoute(route, backendSet)
		hasMixedErr := false
		for _, iss := range issues {
			if strings.Contains(iss.message, "cannot mix") {
				hasMixedErr = true
			}
		}
		assert.True(t, hasMixedErr)
	})

	t.Run("more than one InferencePool per rule", func(t *testing.T) {
		g := "inference.networking.k8s.io"
		k := "InferencePool"
		route := &aigv1b1.AIGatewayRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
			Spec: aigv1b1.AIGatewayRouteSpec{
				Rules: []aigv1b1.AIGatewayRouteRule{{
					BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{
						{Name: "pool-a", Group: &g, Kind: &k},
						{Name: "pool-b", Group: &g, Kind: &k},
					},
				}},
			},
		}
		issues := checkAIGatewayRoute(route, nil)
		hasOnePoolErr := false
		for _, iss := range issues {
			if strings.Contains(iss.message, "only one InferencePool") {
				hasOnePoolErr = true
			}
		}
		assert.True(t, hasOnePoolErr)
	})

	t.Run("duplicate LLMRequestCost metadata key", func(t *testing.T) {
		route := helperRoute("r", "default", "my-backend")
		route.Spec.LLMRequestCosts = []aigv1b1.LLMRequestCost{
			{MetadataKey: "tokens", Type: aigv1b1.LLMRequestCostTypeInputToken},
			{MetadataKey: "tokens", Type: aigv1b1.LLMRequestCostTypeOutputToken},
		}
		issues := checkAIGatewayRoute(route, backendSet)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, `duplicate metadata key "tokens"`)
	})

	t.Run("CEL type requires cel expression", func(t *testing.T) {
		route := helperRoute("r", "default", "my-backend")
		route.Spec.LLMRequestCosts = []aigv1b1.LLMRequestCost{
			{MetadataKey: "cost", Type: aigv1b1.LLMRequestCostTypeCEL},
		}
		issues := checkAIGatewayRoute(route, backendSet)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, "cel expression is required")
	})

	t.Run("non-CEL type must not have cel expression", func(t *testing.T) {
		route := helperRoute("r", "default", "my-backend")
		expr := "input_tokens"
		route.Spec.LLMRequestCosts = []aigv1b1.LLMRequestCost{
			{MetadataKey: "cost", Type: aigv1b1.LLMRequestCostTypeInputToken, CEL: &expr},
		}
		issues := checkAIGatewayRoute(route, backendSet)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, "must only be set when type is CEL")
	})
}

// Test_checkAIServiceBackend tests AIServiceBackend validation rules.
func Test_checkAIServiceBackend(t *testing.T) {
	t.Run("valid backend passes", func(t *testing.T) {
		assert.Empty(t, checkAIServiceBackend(helperBackend("b", "default", aigv1b1.APISchemaOpenAI)))
	})

	t.Run("all valid schema names pass", func(t *testing.T) {
		for _, schema := range []aigv1b1.APISchema{
			aigv1b1.APISchemaOpenAI, aigv1b1.APISchemaCohere, aigv1b1.APISchemaAWSBedrock,
			aigv1b1.APISchemaAzureOpenAI, aigv1b1.APISchemaGCPVertexAI, aigv1b1.APISchemaGCPAnthropic,
			aigv1b1.APISchemaAnthropic, aigv1b1.APISchemaAWSAnthropic,
		} {
			assert.Empty(t, checkAIServiceBackend(helperBackend("b", "default", schema)), "schema %q should be valid", schema)
		}
	})

	t.Run("invalid schema name", func(t *testing.T) {
		b := helperBackend("b", "default", "BadProvider")
		issues := checkAIServiceBackend(b)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.schema.name", issues[0].field)
		assert.Contains(t, issues[0].message, "BadProvider")
	})

	t.Run("wrong backendRef kind", func(t *testing.T) {
		b := helperBackend("b", "default", aigv1b1.APISchemaOpenAI)
		svc := gwapiv1.Kind("Service")
		b.Spec.BackendRef.Kind = &svc
		issues := checkAIServiceBackend(b)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.backendRef.kind", issues[0].field)
	})

	t.Run("wrong backendRef group", func(t *testing.T) {
		b := helperBackend("b", "default", aigv1b1.APISchemaOpenAI)
		wrong := gwapiv1.Group("wrong.group.io")
		b.Spec.BackendRef.Group = &wrong
		issues := checkAIServiceBackend(b)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.backendRef.group", issues[0].field)
	})
}

// Test_checkBackendSecurityPolicy tests BackendSecurityPolicy validation rules.
func Test_checkBackendSecurityPolicy(t *testing.T) {
	t.Run("valid APIKey policy passes", func(t *testing.T) {
		bsp := helperAPIKeyPolicy("p", "default", "my-backend")
		assert.Empty(t, checkBackendSecurityPolicy(bsp, map[string]bool{"default/my-backend": true}))
	})

	t.Run("invalid type", func(t *testing.T) {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Spec:       aigv1b1.BackendSecurityPolicySpec{Type: "BadType"},
		}
		issues := checkBackendSecurityPolicy(bsp, nil)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.type", issues[0].field)
		assert.Contains(t, issues[0].message, "BadType")
	})

	t.Run("APIKey type without apiKey field", func(t *testing.T) {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Spec:       aigv1b1.BackendSecurityPolicySpec{Type: aigv1b1.BackendSecurityPolicyTypeAPIKey},
		}
		issues := checkBackendSecurityPolicy(bsp, nil)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.apiKey", issues[0].field)
		assert.Contains(t, issues[0].message, "required when type is APIKey")
	})

	t.Run("AWSCredentials missing region", func(t *testing.T) {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type:           aigv1b1.BackendSecurityPolicyTypeAWSCredentials,
				AWSCredentials: &aigv1b1.BackendSecurityPolicyAWSCredentials{},
			},
		}
		issues := checkBackendSecurityPolicy(bsp, nil)
		require.Len(t, issues, 1)
		assert.Equal(t, "spec.awsCredentials.region", issues[0].field)
	})

	t.Run("GCPCredentials with both credentialsFile and workloadIdentity", func(t *testing.T) {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type: aigv1b1.BackendSecurityPolicyTypeGCPCredentials,
				GCPCredentials: &aigv1b1.BackendSecurityPolicyGCPCredentials{
					ProjectName:                      "proj",
					Region:                           "us-central1",
					CredentialsFile:                  &aigv1b1.GCPCredentialsFile{},
					WorkloadIdentityFederationConfig: &aigv1b1.GCPWorkloadIdentityFederationConfig{},
				},
			},
		}
		issues := checkBackendSecurityPolicy(bsp, nil)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, "only one of")
	})

	t.Run("targetRef to nonexistent AIServiceBackend", func(t *testing.T) {
		bsp := helperAPIKeyPolicy("p", "default", "ghost")
		issues := checkBackendSecurityPolicy(bsp, map[string]bool{"default/other": true})
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, "ghost")
	})

	t.Run("targetRef with wrong group/kind", func(t *testing.T) {
		bsp := &aigv1b1.BackendSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Spec: aigv1b1.BackendSecurityPolicySpec{
				Type:   aigv1b1.BackendSecurityPolicyTypeAPIKey,
				APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{},
				TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
					{Group: "wrong.group", Kind: "Foo", Name: "bar"},
				},
			},
		}
		issues := checkBackendSecurityPolicy(bsp, nil)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].message, "must reference AIServiceBackend or InferencePool")
	})
}

// --- helpers ---

func helperRoute(name, namespace, backendName string) *aigv1b1.AIGatewayRoute {
	gwKind := gwapiv1.Kind("Gateway")
	return &aigv1b1.AIGatewayRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: aigv1b1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Kind: &gwKind, Name: "test-gw"}},
			Rules: []aigv1b1.AIGatewayRouteRule{
				{BackendRefs: []aigv1b1.AIGatewayRouteRuleBackendRef{{Name: backendName}}},
			},
		},
	}
}

func helperBackend(name, namespace string, schema aigv1b1.APISchema) *aigv1b1.AIServiceBackend {
	kind := gwapiv1.Kind("Backend")
	group := gwapiv1.Group("gateway.envoyproxy.io")
	return &aigv1b1.AIServiceBackend{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: aigv1b1.AIServiceBackendSpec{
			APISchema: aigv1b1.VersionedAPISchema{Name: schema},
			BackendRef: gwapiv1.BackendObjectReference{
				Name:  gwapiv1.ObjectName(name + "-backend"),
				Kind:  &kind,
				Group: &group,
			},
		},
	}
}

func helperAPIKeyPolicy(name, namespace, backendName string) *aigv1b1.BackendSecurityPolicy {
	return &aigv1b1.BackendSecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: aigv1b1.BackendSecurityPolicySpec{
			Type:   aigv1b1.BackendSecurityPolicyTypeAPIKey,
			APIKey: &aigv1b1.BackendSecurityPolicyAPIKey{SecretRef: &gwapiv1.SecretObjectReference{Name: "secret"}},
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend", Name: gwapiv1a2.ObjectName(backendName)},
			},
		},
	}
}
