// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

// cmdValidate is the struct for the `aigw validate` command.
type cmdValidate struct {
	Paths []string `arg:"" name:"paths" help:"Path(s) to AI Gateway config YAML file(s) to validate." type:"path" optional:""`
}

// Validate is called by Kong after parsing to enforce that at least one path was given.
func (c *cmdValidate) Validate() error {
	if len(c.Paths) == 0 {
		return fmt.Errorf("at least one path is required")
	}
	return nil
}

type validateFn func(ctx context.Context, c *cmdValidate, stdout, stderr io.Writer) error

// validateCmd checks AI Gateway config YAML files for structural and cross-reference errors
// without requiring a running Kubernetes cluster.
func validateCmd(_ context.Context, c *cmdValidate, stdout, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{}))
	yamlContent, err := readYamlsAsString(c.Paths)
	if err != nil {
		return err
	}

	aigwRoutes, _, aigwBackends, backendSecurityPolicies, _, _, _, _, err := collectObjects(yamlContent, io.Discard, logger)
	if err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	total := len(aigwRoutes) + len(aigwBackends) + len(backendSecurityPolicies)
	fmt.Fprintf(stdout, "Validating %d resource(s) from %d file(s)...\n\n", total, len(c.Paths))

	results := validateResources(aigwRoutes, aigwBackends, backendSecurityPolicies)

	errorCount := 0
	for _, r := range results {
		if len(r.issues) > 0 {
			errorCount += len(r.issues)
			fmt.Fprintf(stdout, "ERROR %s %q:\n", r.kind, resourceID(r.namespace, r.name))
			for _, iss := range r.issues {
				fmt.Fprintf(stdout, "  [%s] %s\n", iss.field, iss.message)
			}
			fmt.Fprintln(stdout)
		} else {
			fmt.Fprintf(stdout, "OK    %s %q\n", r.kind, resourceID(r.namespace, r.name))
		}
	}

	fmt.Fprintln(stdout)
	if errorCount > 0 {
		return fmt.Errorf("validation failed: %d error(s)", errorCount)
	}
	_, _ = fmt.Fprintln(stdout, "All resources are valid.")
	return nil
}

func resourceID(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

type validationIssue struct {
	field   string
	message string
}

type validationResult struct {
	kind      string
	namespace string
	name      string
	issues    []validationIssue
}

// validateResources runs all structural and cross-reference validation checks.
func validateResources(
	aigwRoutes []*aigv1b1.AIGatewayRoute,
	backends []*aigv1b1.AIServiceBackend,
	bsps []*aigv1b1.BackendSecurityPolicy,
) []validationResult {
	backendSet := make(map[string]bool, len(backends))
	for _, b := range backends {
		ns := b.Namespace
		if ns == "" {
			ns = "default"
		}
		backendSet[ns+"/"+b.Name] = true
	}

	var results []validationResult

	for _, route := range aigwRoutes {
		results = append(results, validationResult{
			kind:      "AIGatewayRoute",
			namespace: route.Namespace,
			name:      route.Name,
			issues:    checkAIGatewayRoute(route, backendSet),
		})
	}
	for _, b := range backends {
		results = append(results, validationResult{
			kind:      "AIServiceBackend",
			namespace: b.Namespace,
			name:      b.Name,
			issues:    checkAIServiceBackend(b),
		})
	}
	for _, bsp := range bsps {
		results = append(results, validationResult{
			kind:      "BackendSecurityPolicy",
			namespace: bsp.Namespace,
			name:      bsp.Name,
			issues:    checkBackendSecurityPolicy(bsp, backendSet),
		})
	}

	return results
}

func checkAIGatewayRoute(route *aigv1b1.AIGatewayRoute, backendSet map[string]bool) []validationIssue {
	var issues []validationIssue

	if len(route.Spec.Rules) == 0 {
		return append(issues, validationIssue{field: "spec.rules", message: "at least one rule is required"})
	}

	for i, rule := range route.Spec.Rules {
		hasPool, hasBackend := false, false
		for _, ref := range rule.BackendRefs {
			if ref.Group != nil || ref.Kind != nil {
				hasPool = true
			} else {
				hasBackend = true
			}
		}
		if hasPool && hasBackend {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.rules[%d].backendRefs", i),
				message: "cannot mix InferencePool and AIServiceBackend references in the same rule",
			})
		}

		poolCount := 0
		for _, ref := range rule.BackendRefs {
			if ref.Group != nil || ref.Kind != nil {
				poolCount++
			}
		}
		if poolCount > 1 {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.rules[%d].backendRefs", i),
				message: "only one InferencePool backend is allowed per rule",
			})
		}

		for j, ref := range rule.BackendRefs {
			if ref.Group != nil || ref.Kind != nil {
				g := derefStr(ref.Group)
				k := derefStr(ref.Kind)
				if g != "inference.networking.k8s.io" || k != "InferencePool" {
					issues = append(issues, validationIssue{
						field:   fmt.Sprintf("spec.rules[%d].backendRefs[%d]", i, j),
						message: fmt.Sprintf(`only InferencePool from "inference.networking.k8s.io" is supported, got group=%q kind=%q`, g, k),
					})
				}
			} else {
				ns := route.Namespace
				if ns == "" {
					ns = "default"
				}
				if ref.Namespace != nil && string(*ref.Namespace) != "" {
					ns = string(*ref.Namespace)
				}
				key := ns + "/" + ref.Name
				if !backendSet[key] {
					issues = append(issues, validationIssue{
						field:   fmt.Sprintf("spec.rules[%d].backendRefs[%d].name", i, j),
						message: fmt.Sprintf("references AIServiceBackend %q which is not defined in the provided config", key),
					})
				}
			}
		}
	}

	for i, ref := range route.Spec.ParentRefs {
		if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.parentRefs[%d].kind", i),
				message: fmt.Sprintf(`only "Gateway" is supported, got %q`, string(*ref.Kind)),
			})
		}
	}

	seenCostKeys := make(map[string]bool)
	for i, cost := range route.Spec.LLMRequestCosts {
		if seenCostKeys[cost.MetadataKey] {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.llmRequestCosts[%d].metadataKey", i),
				message: fmt.Sprintf("duplicate metadata key %q", cost.MetadataKey),
			})
		}
		seenCostKeys[cost.MetadataKey] = true
		if cost.Type == aigv1b1.LLMRequestCostTypeCEL && cost.CEL == nil {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.llmRequestCosts[%d].cel", i),
				message: "cel expression is required when type is CEL",
			})
		}
		if cost.Type != aigv1b1.LLMRequestCostTypeCEL && cost.CEL != nil {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.llmRequestCosts[%d].cel", i),
				message: "cel expression must only be set when type is CEL",
			})
		}
	}

	return issues
}

func checkAIServiceBackend(b *aigv1b1.AIServiceBackend) []validationIssue {
	var issues []validationIssue

	validSchemas := map[aigv1b1.APISchema]bool{
		aigv1b1.APISchemaOpenAI:       true,
		aigv1b1.APISchemaCohere:       true,
		aigv1b1.APISchemaAWSBedrock:   true,
		aigv1b1.APISchemaAzureOpenAI:  true,
		aigv1b1.APISchemaGCPVertexAI:  true,
		aigv1b1.APISchemaGCPAnthropic: true,
		aigv1b1.APISchemaAnthropic:    true,
		aigv1b1.APISchemaAWSAnthropic: true,
	}
	if !validSchemas[b.Spec.APISchema.Name] {
		issues = append(issues, validationIssue{
			field:   "spec.schema.name",
			message: fmt.Sprintf("invalid value %q, must be one of: OpenAI, Cohere, AWSBedrock, AzureOpenAI, GCPVertexAI, GCPAnthropic, Anthropic, AWSAnthropic", b.Spec.APISchema.Name),
		})
	}

	ref := b.Spec.BackendRef
	if ref.Kind == nil || string(*ref.Kind) != "Backend" {
		issues = append(issues, validationIssue{
			field:   "spec.backendRef.kind",
			message: `must be "Backend" (Envoy Gateway Backend resource)`,
		})
	}
	if ref.Group == nil || string(*ref.Group) != "gateway.envoyproxy.io" {
		issues = append(issues, validationIssue{
			field:   "spec.backendRef.group",
			message: `must be "gateway.envoyproxy.io"`,
		})
	}
	if string(ref.Name) == "" {
		issues = append(issues, validationIssue{
			field:   "spec.backendRef.name",
			message: "name is required",
		})
	}

	return issues
}

func checkBackendSecurityPolicy(bsp *aigv1b1.BackendSecurityPolicy, backendSet map[string]bool) []validationIssue {
	var issues []validationIssue
	spec := bsp.Spec

	validTypes := map[aigv1b1.BackendSecurityPolicyType]bool{
		aigv1b1.BackendSecurityPolicyTypeAPIKey:           true,
		aigv1b1.BackendSecurityPolicyTypeAWSCredentials:   true,
		aigv1b1.BackendSecurityPolicyTypeAzureAPIKey:      true,
		aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey:  true,
		aigv1b1.BackendSecurityPolicyTypeAzureCredentials: true,
		aigv1b1.BackendSecurityPolicyTypeGCPCredentials:   true,
	}
	if !validTypes[spec.Type] {
		return append(issues, validationIssue{
			field:   "spec.type",
			message: fmt.Sprintf("invalid value %q, must be one of: APIKey, AWSCredentials, AzureAPIKey, AzureCredentials, GCPCredentials, AnthropicAPIKey", spec.Type),
		})
	}

	switch spec.Type {
	case aigv1b1.BackendSecurityPolicyTypeAPIKey:
		if spec.APIKey == nil {
			issues = append(issues, validationIssue{field: "spec.apiKey", message: "required when type is APIKey"})
		}
		if spec.AWSCredentials != nil || spec.AzureAPIKey != nil || spec.AzureCredentials != nil || spec.GCPCredentials != nil || spec.AnthropicAPIKey != nil {
			issues = append(issues, validationIssue{field: "spec", message: "only apiKey field may be set when type is APIKey"})
		}
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		if spec.AWSCredentials == nil {
			issues = append(issues, validationIssue{field: "spec.awsCredentials", message: "required when type is AWSCredentials"})
		} else if spec.AWSCredentials.Region == "" {
			issues = append(issues, validationIssue{field: "spec.awsCredentials.region", message: "required"})
		}
		if spec.APIKey != nil || spec.AzureAPIKey != nil || spec.AzureCredentials != nil || spec.GCPCredentials != nil || spec.AnthropicAPIKey != nil {
			issues = append(issues, validationIssue{field: "spec", message: "only awsCredentials field may be set when type is AWSCredentials"})
		}
	case aigv1b1.BackendSecurityPolicyTypeAzureAPIKey:
		if spec.AzureAPIKey == nil {
			issues = append(issues, validationIssue{field: "spec.azureAPIKey", message: "required when type is AzureAPIKey"})
		}
		if spec.APIKey != nil || spec.AWSCredentials != nil || spec.AzureCredentials != nil || spec.GCPCredentials != nil || spec.AnthropicAPIKey != nil {
			issues = append(issues, validationIssue{field: "spec", message: "only azureAPIKey field may be set when type is AzureAPIKey"})
		}
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		if spec.AzureCredentials == nil {
			issues = append(issues, validationIssue{field: "spec.azureCredentials", message: "required when type is AzureCredentials"})
		} else {
			if spec.AzureCredentials.ClientID == "" {
				issues = append(issues, validationIssue{field: "spec.azureCredentials.clientID", message: "required"})
			}
			if spec.AzureCredentials.TenantID == "" {
				issues = append(issues, validationIssue{field: "spec.azureCredentials.tenantID", message: "required"})
			}
		}
		if spec.APIKey != nil || spec.AWSCredentials != nil || spec.AzureAPIKey != nil || spec.GCPCredentials != nil || spec.AnthropicAPIKey != nil {
			issues = append(issues, validationIssue{field: "spec", message: "only azureCredentials field may be set when type is AzureCredentials"})
		}
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		if spec.GCPCredentials == nil {
			issues = append(issues, validationIssue{field: "spec.gcpCredentials", message: "required when type is GCPCredentials"})
		} else {
			if spec.GCPCredentials.ProjectName == "" {
				issues = append(issues, validationIssue{field: "spec.gcpCredentials.projectName", message: "required"})
			}
			if spec.GCPCredentials.Region == "" {
				issues = append(issues, validationIssue{field: "spec.gcpCredentials.region", message: "required"})
			}
			if spec.GCPCredentials.CredentialsFile == nil && spec.GCPCredentials.WorkloadIdentityFederationConfig == nil {
				issues = append(issues, validationIssue{
					field:   "spec.gcpCredentials",
					message: "exactly one of credentialsFile or workloadIdentityFederationConfig must be specified",
				})
			} else if spec.GCPCredentials.CredentialsFile != nil && spec.GCPCredentials.WorkloadIdentityFederationConfig != nil {
				issues = append(issues, validationIssue{
					field:   "spec.gcpCredentials",
					message: "only one of credentialsFile or workloadIdentityFederationConfig may be set",
				})
			}
		}
		if spec.APIKey != nil || spec.AWSCredentials != nil || spec.AzureAPIKey != nil || spec.AzureCredentials != nil || spec.AnthropicAPIKey != nil {
			issues = append(issues, validationIssue{field: "spec", message: "only gcpCredentials field may be set when type is GCPCredentials"})
		}
	case aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey:
		if spec.AnthropicAPIKey == nil {
			issues = append(issues, validationIssue{field: "spec.anthropicAPIKey", message: "required when type is AnthropicAPIKey"})
		}
		if spec.APIKey != nil || spec.AWSCredentials != nil || spec.AzureAPIKey != nil || spec.AzureCredentials != nil || spec.GCPCredentials != nil {
			issues = append(issues, validationIssue{field: "spec", message: "only anthropicAPIKey field may be set when type is AnthropicAPIKey"})
		}
	}

	ns := bsp.Namespace
	if ns == "" {
		ns = "default"
	}
	for i, ref := range spec.TargetRefs {
		g, k := string(ref.Group), string(ref.Kind)
		isAIServiceBackend := g == "aigateway.envoyproxy.io" && k == "AIServiceBackend"
		isInferencePool := g == "inference.networking.k8s.io" && k == "InferencePool"
		if !isAIServiceBackend && !isInferencePool {
			issues = append(issues, validationIssue{
				field:   fmt.Sprintf("spec.targetRefs[%d]", i),
				message: fmt.Sprintf("must reference AIServiceBackend or InferencePool, got group=%q kind=%q", g, k),
			})
			continue
		}
		if isAIServiceBackend {
			key := ns + "/" + string(ref.Name)
			if !backendSet[key] {
				issues = append(issues, validationIssue{
					field:   fmt.Sprintf("spec.targetRefs[%d]", i),
					message: fmt.Sprintf("references AIServiceBackend %q which is not defined in the provided config", key),
				})
			}
		}
	}

	return issues
}

// derefStr safely dereferences a *string, returning "" if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
