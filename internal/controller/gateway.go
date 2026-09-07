// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
	"github.com/envoyproxy/ai-gateway/internal/version"
)

const (
	// defaultOwnedBy is the default value for the ModelsOwnedBy field in the filter config.
	defaultOwnedBy = "Envoy AI Gateway"
)

// NewGatewayController creates a new reconcile.TypedReconciler for gwapiv1.Gateway.
//
// The extproc builder is derived from options + extProcAsSideCar via the same
// newExtProcBuilder the mutating webhook uses (StartControllers passes the same
// options to both), so the workload template hash matches webhook injection.
func NewGatewayController(
	client client.Client, kube kubernetes.Interface, logger logr.Logger, envoyGatewayNamespace string,
	standAlone bool, uuidFn func() string, options *Options, extProcAsSideCar bool,
) *GatewayController {
	uf := uuidFn
	if uf == nil {
		uf = uuid.NewString
	}
	return &GatewayController{
		client:                client,
		kube:                  kube,
		logger:                logger,
		envoyGatewayNamespace: envoyGatewayNamespace,
		standAlone:            standAlone,
		uuidFn:                uf,
		extProcBuilder:        newExtProcBuilder(options, extProcAsSideCar, logger),
	}
}

// GatewayController implements reconcile.TypedReconciler for gwapiv1.Gateway.
type GatewayController struct {
	client                client.Client
	kube                  kubernetes.Interface
	logger                logr.Logger
	envoyGatewayNamespace string // The namespace where Envoy Gateway is deployed.
	// standAlone indicates whether the controller is running in standalone mode.
	standAlone bool
	uuidFn     func() string // Function to generate a new UUID for the filter config.
	// extProcBuilder is shared with the mutating webhook so the template hash
	// computed here matches the extproc container injected by the webhook.
	*extProcBuilder
}

// Reconcile implements the reconcile.Reconciler for gwapiv1.Gateway.
func (c *GatewayController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	gw := &gwapiv1.Gateway{}
	if err := c.client.Get(ctx, req.NamespacedName, gw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	aiRoutes, err := listAIGatewayRoutesForGateway(ctx, c.client, nil, req.Name, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	mcpRoutes, err := listMCPRoutesForGateway(ctx, c.client, nil, req.Name, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Sort MCPRoutes by CreationTimestamp (earliest first) for deterministic prioritization.
	sort.Slice(mcpRoutes.Items, func(i, j int) bool {
		return mcpRoutes.Items[i].CreationTimestamp.Before(&mcpRoutes.Items[j].CreationTimestamp)
	})

	namespace, pods, deployments, daemonSets, err := c.getObjectsForGateway(ctx, gw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get objects for gateway %s: %w", gw.Name, err)
	}
	if len(pods) == 0 && len(deployments) == 0 && len(daemonSets) == 0 && !c.standAlone {
		// This means that the gateway is not running any pods, deployments or daemonsets and just after the gateway is created.
		// Wait for EG to create the pods, deployments or daemonsets to be able to reconcile the filter config. Until that happens,
		// we are yet to know which namespace the Gateway's pods, deployments, and daemonsets are running in.
		//
		// On standalone mode, we won't have these resources and code assume that the filter config Secret is created in the "empty" namespace,
		// so we don't need to enter this branch.
		const requeueAfter = 5 * time.Second
		c.logger.Info("No pods, deployments or daemonsets found for the Gateway.", "namespace", gw.Namespace, "name", gw.Name, "requeueAfter", requeueAfter.String())
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	uid := c.uuidFn()

	// Fetch GatewayConfig to get global LLM request cost defaults.
	gwConfig, err := c.fetchGatewayConfig(ctx, gw)
	if err != nil {
		return ctrl.Result{}, err
	}
	var defaultLLMCosts []aigv1b1.LLMRequestCost
	if gwConfig != nil {
		defaultLLMCosts = gwConfig.Spec.GlobalLLMRequestCosts
	}

	// We need to create the filter config in Envoy Gateway system namespace because the sidecar extproc need
	// to access it.
	var hasEffectiveRoutes bool // indicates whether the filter config is effective (i.e., there is at least one active route).
	hasEffectiveRoutes, err = c.reconcileFilterConfigSecret(ctx, gw.Name, gw.Namespace, namespace, aiRoutes.Items, mcpRoutes.Items, uid, defaultLLMCosts)
	if err != nil {
		return ctrl.Result{}, err
	}

	input := extProcContainerInput{gatewayConfig: gwConfig, needMCP: len(mcpRoutes.Items) > 0}
	result, err := c.annotateGatewayPods(ctx, pods, deployments, daemonSets, uid,
		hasEffectiveRoutes, input.needMCP, c.extProcImage(input), c.extProcContainerHash(input))
	if err != nil {
		c.logger.Error(err, "Failed to annotate gateway pods", "namespace", gw.Namespace, "name", gw.Name)
		return ctrl.Result{}, err
	}
	return result, nil
}

// schemaToFilterAPI converts an aigv1b1.VersionedAPISchema to filterapi.VersionedAPISchema.
func schemaToFilterAPI(schema aigv1b1.VersionedAPISchema) filterapi.VersionedAPISchema {
	ret := filterapi.VersionedAPISchema{}
	ret.Name = filterapi.APISchemaName(schema.Name)
	if schema.Name == aigv1b1.APISchemaOpenAI || schema.Name == aigv1b1.APISchemaAnthropic {
		ret.Prefix = cmp.Or(ptr.Deref(schema.Prefix, ""), "v1")
	} else {
		ret.Version = ptr.Deref(schema.Version, "")
	}
	return ret
}

// headerMutationToFilterAPI converts an aigv1b1.HTTPHeaderMutation to filterapi.HTTPHeaderMutation.
func headerMutationToFilterAPI(m *aigv1b1.HTTPHeaderMutation) *filterapi.HTTPHeaderMutation {
	if m == nil {
		return nil
	}
	ret := &filterapi.HTTPHeaderMutation{}
	ret.Remove = make([]string, 0, len(m.Remove))
	for _, h := range m.Remove {
		ret.Remove = append(ret.Remove, strings.ToLower(h))
	}
	for _, h := range m.Set {
		ret.Set = append(ret.Set, filterapi.HTTPHeader{Name: strings.ToLower(string(h.Name)), Value: h.Value})
	}
	return ret
}

// headerValueFiltersToFilterAPI converts aigv1b1.HTTPHeaderValueFilter to filterapi.HTTPHeaderValueFilter.
func headerValueFiltersToFilterAPI(filters []aigv1b1.HTTPHeaderValueFilter) []filterapi.HTTPHeaderValueFilter {
	if len(filters) == 0 {
		return nil
	}
	ret := make([]filterapi.HTTPHeaderValueFilter, 0, len(filters))
	for _, f := range filters {
		mode := f.Mode
		if mode == "" {
			// The CRD defaults this, but objects persisted before the default was added still reach us
			// without it. Fall back to the same value rather than silently disabling the filter.
			mode = aigv1b1.HTTPHeaderValueFilterModeDenylist
		}
		ret = append(ret, filterapi.HTTPHeaderValueFilter{
			Name:   strings.ToLower(string(f.Name)),
			Mode:   string(mode),
			Values: f.Values,
		})
	}
	return ret
}

// bodyMutationToFilterAPI converts an aigv1b1.HTTPBodyMutation to filterapi.HTTPBodyMutation.
func bodyMutationToFilterAPI(m *aigv1b1.HTTPBodyMutation) *filterapi.HTTPBodyMutation {
	if m == nil {
		return nil
	}
	ret := &filterapi.HTTPBodyMutation{}
	ret.Remove = make([]string, 0, len(m.Remove))
	ret.Remove = append(ret.Remove, m.Remove...)
	for _, field := range m.Set {
		ret.Set = append(ret.Set, filterapi.HTTPBodyField{Path: field.Path, Value: field.Value})
	}
	return ret
}

// validateCELExpression validates and returns a CEL expression for cost calculation.
func validateCELExpression(cost aigv1b1.LLMRequestCost) (string, error) {
	if cost.CEL == nil {
		return "", fmt.Errorf("missing CEL expression")
	}
	expr := *cost.CEL
	if _, err := llmcostcel.NewProgram(expr); err != nil {
		return "", fmt.Errorf("invalid CEL expression: %w", err)
	}
	return expr, nil
}

// aigwLLMRequestCostToFilterAPI converts an API LLMRequestCost to filter API form for the given
// AIGatewayRoute (routeName is "namespace/name").
func aigwGlobalLLMRequestCostToFilterAPI(cost aigv1b1.LLMRequestCost) (filterapi.GlobalLLMRequestCost, error) {
	out := filterapi.GlobalLLMRequestCost{
		MetadataKey: cost.MetadataKey,
		Type:        filterapi.LLMRequestCostType(cost.Type),
	}
	if cost.Type == aigv1b1.LLMRequestCostTypeCEL {
		celExpr, err := validateCELExpression(cost)
		if err != nil {
			return filterapi.GlobalLLMRequestCost{}, err
		}
		out.CEL = celExpr
	}
	return out, nil
}

func aigwLLMRequestCostToFilterAPI(cost aigv1b1.LLMRequestCost, routeName string) (filterapi.LLMRequestCost, error) {
	out := filterapi.LLMRequestCost{
		MetadataKey: cost.MetadataKey,
		RouteName:   routeName,
		Type:        filterapi.LLMRequestCostType(cost.Type),
	}
	if cost.Type == aigv1b1.LLMRequestCostTypeCEL {
		celExpr, err := validateCELExpression(cost)
		if err != nil {
			return filterapi.LLMRequestCost{}, err
		}
		out.CEL = celExpr
	}
	return out, nil
}

// mergeBodyMutations merges route-level and backend-level BodyMutation with route-level taking precedence.
// Returns the merged BodyMutation where route-level operations override backend-level operations for conflicting body fields.
func mergeBodyMutations(routeLevel, backendLevel *aigv1b1.HTTPBodyMutation) *aigv1b1.HTTPBodyMutation {
	if routeLevel == nil {
		return backendLevel
	}
	if backendLevel == nil {
		return routeLevel
	}

	result := &aigv1b1.HTTPBodyMutation{}

	// Merge Set operations (route-level wins conflicts)
	fieldMap := make(map[string]aigv1b1.HTTPBodyField)

	// Add backend-level fields first
	for _, f := range backendLevel.Set {
		fieldMap[f.Path] = f
	}

	// Override with route-level fields (route-level wins)
	for _, f := range routeLevel.Set {
		fieldMap[f.Path] = f
	}

	// Convert back to slice
	for _, f := range fieldMap {
		result.Set = append(result.Set, f)
	}

	// Merge Remove operations (combine and deduplicate)
	removeMap := make(map[string]struct{})

	for _, f := range backendLevel.Remove {
		removeMap[f] = struct{}{}
	}
	for _, f := range routeLevel.Remove {
		removeMap[f] = struct{}{}
	}

	for f := range removeMap {
		result.Remove = append(result.Remove, f)
	}

	return result
}

// mergeHeaderMutations merges route-level and backend-level HeaderMutation with route-level taking precedence.
// Returns the merged HeaderMutation where route-level operations override backend-level operations for conflicting headers.
func mergeHeaderMutations(routeLevel, backendLevel *aigv1b1.HTTPHeaderMutation) *aigv1b1.HTTPHeaderMutation {
	if routeLevel == nil {
		return backendLevel
	}
	if backendLevel == nil {
		return routeLevel
	}

	result := &aigv1b1.HTTPHeaderMutation{}

	// Merge Set operations (route-level wins conflicts)
	headerMap := make(map[string]gwapiv1.HTTPHeader)

	// Add backend-level headers first
	for _, h := range backendLevel.Set {
		headerMap[strings.ToLower(string(h.Name))] = h
	}

	// Override with route-level headers (route-level wins)
	for _, h := range routeLevel.Set {
		headerMap[strings.ToLower(string(h.Name))] = h
	}

	// Convert back to slice
	for _, h := range headerMap {
		result.Set = append(result.Set, h)
	}

	// Merge Remove operations (combine and deduplicate)
	removeMap := make(map[string]struct{})

	for _, h := range backendLevel.Remove {
		removeMap[strings.ToLower(h)] = struct{}{}
	}
	for _, h := range routeLevel.Remove {
		removeMap[strings.ToLower(h)] = struct{}{}
	}

	for h := range removeMap {
		result.Remove = append(result.Remove, h)
	}

	return result
}

// isCtxErr reports whether err is the reconcile's context being cancelled or timing out, rather than
// a problem with the object being read.
//
// The distinction matters everywhere reconcileFilterConfigSecret skips a backend on error. Skipping
// is right when the backend or its policy cannot be resolved — a missing object, a bad ref — because
// the resulting config reflects the cluster. It is wrong when the read was interrupted, because then
// the reconcile carries on and publishes a config in which the backend is missing, or present without
// its credentials, until something triggers another reconcile. Returning instead lets
// controller-runtime requeue with a fresh context.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// reconcileFilterConfigSecret updates the filter config secret for the external processor.
func (c *GatewayController) reconcileFilterConfigSecret(
	ctx context.Context,
	gatewayName,
	gatewayNamespace,
	configSecretNamespace string,
	aiGatewayRoutes []aigv1b1.AIGatewayRoute,
	mcpRoutes []aigv1b1.MCPRoute,
	uuid string,
	defaultLLMCosts []aigv1b1.LLMRequestCost,
) (hasEffectiveRoute bool, _ error) {
	// Precondition: aiGatewayRoutes is not empty as we early return if it is empty.
	ec := &filterapi.Config{UUID: uuid, Version: version.Parse()}
	var err error

	// Process global LLM request costs from GatewayConfig.
	// These have no RouteName and serve as defaults.
	// Note: The CRD enforces uniqueness via +listType=map and +listMapKey=metadataKey,
	// so we don't need to deduplicate here.
	for _, cost := range defaultLLMCosts {
		fc, convErr := aigwGlobalLLMRequestCostToFilterAPI(cost)
		if convErr != nil {
			return false, fmt.Errorf("failed to convert global LLMRequestCosts: %w", convErr)
		}
		ec.GlobalLLMRequestCosts = append(ec.GlobalLLMRequestCosts, fc)
	}

	// Models contributed by routes with no Spec.Hostnames. We only promote these to
	// ec.UnscopedModels (and merge them into ec.ModelsByHost) when at least one route
	// IS hostname-scoped; otherwise the existing ec.Models list already covers them.
	var unscopedModels []filterapi.Model

	for i := range aiGatewayRoutes {
		aiGatewayRoute := &aiGatewayRoutes[i]
		if !aiGatewayRoute.GetDeletionTimestamp().IsZero() {
			c.logger.Info("AIGatewayRoute is being deleted, skipping extproc secret update", "namespace", aiGatewayRoutes[i].Namespace, "name", aiGatewayRoutes[i].Name)
			continue
		}
		hasEffectiveRoute = true
		routeName := fmt.Sprintf("%s/%s", aiGatewayRoute.Namespace, aiGatewayRoute.Name)
		hostnames := aiGatewayRoute.Spec.Hostnames
		spec := aiGatewayRoute.Spec
		routeBackendNamesSet := map[string]struct{}{}
		routeBackendNames := []string{}
		injectedQuotaCosts := make(map[string]struct{})
		for ruleIndex := range spec.Rules {
			rule := &spec.Rules[ruleIndex]
			for _, m := range rule.Matches {
				for _, h := range m.Headers {
					// If explicitly set to something that is not an exact match, skip.
					// If not set, we assume it's an exact match.
					//
					// Also, we only care about the AIModel header to declare models.
					if (h.Type != nil && *h.Type != gwapiv1.HeaderMatchExact) || string(h.Name) != internalapi.ModelNameHeaderKeyDefault {
						continue
					}
					model := filterapi.Model{
						Name:      h.Value,
						CreatedAt: ptr.Deref[metav1.Time](rule.ModelsCreatedAt, aiGatewayRoute.CreationTimestamp).UTC(),
						OwnedBy:   ptr.Deref(rule.ModelsOwnedBy, defaultOwnedBy),
					}
					ec.Models = append(ec.Models, model)
					if len(hostnames) > 0 {
						if ec.ModelsByHost == nil {
							ec.ModelsByHost = make(map[string][]filterapi.Model)
						}
						for _, hn := range hostnames {
							ec.ModelsByHost[string(hn)] = append(ec.ModelsByHost[string(hn)], model)
						}
					} else {
						// Routes without hostnames are "unscoped": they apply to every host.
						// Tracked in unscopedModels for now; only promoted to ec.UnscopedModels
						// after the loop if at least one scoped route is also present.
						unscopedModels = append(unscopedModels, model)
					}
				}
			}
			for backendRefIndex := range rule.BackendRefs {
				backendRef := &rule.BackendRefs[backendRefIndex]
				b := filterapi.Backend{}
				b.Name = internalapi.PerRouteRuleRefBackendName(aiGatewayRoute.Namespace, backendRef.Name, aiGatewayRoute.Name, ruleIndex, backendRefIndex)
				b.ModelNameOverride = backendRef.ModelNameOverride

				var bsp *aigv1b1.BackendSecurityPolicy
				backendNamespace := backendRef.GetNamespace(aiGatewayRoute.Namespace)

				if backendRef.IsInferencePool() {
					// We assume that InferencePools are all OpenAI schema.
					b.Schema = filterapi.VersionedAPISchema{
						Name: filterapi.APISchemaOpenAI,
						// This is for backward compatibility. TODO: Remove the 'version' field usage after v0.5.0 release.
						Version: "v1", Prefix: "v1",
					}

					bsp, err = c.getBSPForInferencePool(ctx, backendNamespace, backendRef.Name)
					if err != nil {
						if isCtxErr(err) {
							return false, fmt.Errorf("reconcile interrupted while reading backend security policy for inference pool %s: %w", backendRef.Name, err)
						}
						c.logger.Error(err, "failed to get backend security policy for inference pool",
							"backend_name", backendRef.Name, "aigatewayroute", aiGatewayRoute.Name,
							"namespace", backendNamespace)
						continue
					}
				} else {
					var backendObj *aigv1b1.AIServiceBackend
					backendObj, bsp, err = c.backendWithMaybeBSP(ctx, backendNamespace, backendRef.Name)
					if err != nil {
						if isCtxErr(err) {
							return false, fmt.Errorf("reconcile interrupted while reading backend %s: %w", backendRef.Name, err)
						}
						c.logger.Error(err, "failed to get backend or backend security policy. Skipping this backend.",
							"backend_name", backendRef.Name, "aigatewayroute", aiGatewayRoute.Name,
							"namespace", backendNamespace)
						continue
					}

					// Extract HeaderMutation from both route and backend levels
					routeHeaderMutation := backendRef.HeaderMutation
					backendHeaderMutation := backendObj.Spec.HeaderMutation

					// Merge with route-level taking precedence over backend-level
					mergedHeaderMutation := mergeHeaderMutations(routeHeaderMutation, backendHeaderMutation)

					// Convert to FilterAPI format
					b.HeaderMutation = headerMutationToFilterAPI(mergedHeaderMutation)

					routeBodyMutation := backendRef.BodyMutation
					backendBodyMutation := backendObj.Spec.BodyMutation
					// Merge with route-level taking precedence over backend-level
					mergedBodyMutation := mergeBodyMutations(routeBodyMutation, backendBodyMutation)
					b.BodyMutation = bodyMutationToFilterAPI(mergedBodyMutation)

					b.Schema = schemaToFilterAPI(backendObj.Spec.APISchema)
					b.HeaderValueFilters = headerValueFiltersToFilterAPI(backendObj.Spec.HeaderValueFilters)
				}

				if bsp != nil {
					b.Auth, err = c.bspToFilterAPIBackendAuth(ctx, bsp)
					if err != nil {
						if isCtxErr(err) {
							return false, fmt.Errorf("reconcile interrupted while reading auth for backend security policy %s: %w", bsp.Name, err)
						}
						c.logger.Error(err, "failed to get backend auth from backend security policy. Skipping this backend.",
							"backend_name", backendRef.Name, "backend_security_policy", bsp.Name,
							"aigatewayroute", aiGatewayRoute.Name, "namespace", aiGatewayRoute.Namespace)
						continue
					}
					stripCredentialOverrideInputHeaders(&b)
				}

				ec.Backends = append(ec.Backends, b)
				if _, exists := routeBackendNamesSet[b.Name]; !exists {
					routeBackendNamesSet[b.Name] = struct{}{}
					routeBackendNames = append(routeBackendNames, b.Name)
				}
			}
		}
		if len(routeBackendNames) > 0 {
			// Dedup per (metadataKey, routeName): last definition wins.
			dedup := map[string]filterapi.LLMRequestCost{}
			for _, cost := range aiGatewayRoute.Spec.LLMRequestCosts {
				fc, convErr := aigwLLMRequestCostToFilterAPI(cost, routeName)
				if convErr != nil {
					return false, fmt.Errorf("failed to convert LLMRequestCosts for route %s: %w", aiGatewayRoute.Name, convErr)
				}
				key := fc.MetadataKey
				dedup[key] = fc
			}
			// Inject QuotaPolicy cost expressions as LLMRequestCost entries so ext_proc
			// computes and stores them in metadata for the HitsAddend to read.
			c.injectQuotaPolicyCostExpressions(ctx, aiGatewayRoute, ec, injectedQuotaCosts, routeName)

			for _, fc := range dedup {
				ec.LLMRequestCosts = append(ec.LLMRequestCosts, fc)
			}
		}
	}

	// If at least one route is hostname-scoped, promote the unscoped models to ec.UnscopedModels
	// so the runtime can fall back to them on unmatched hosts, and merge them into every per-host
	// list so a host-matched request still sees the models from routes that didn't declare hostnames.
	// When no route uses hostname scoping, ec.Models is the sole source of truth and we skip both
	// steps to avoid serializing a redundant UnscopedModels duplicate of Models.
	if len(ec.ModelsByHost) > 0 && len(unscopedModels) > 0 {
		ec.UnscopedModels = unscopedModels
		for hn := range ec.ModelsByHost {
			ec.ModelsByHost[hn] = append(ec.ModelsByHost[hn], unscopedModels...)
		}
	}

	// Configuration for MCP processor.
	var effectiveMCPRoute bool
	ec.MCPConfig, effectiveMCPRoute = mcpConfig(mcpRoutes)
	hasEffectiveRoute = hasEffectiveRoute || effectiveMCPRoute

	marshaled, err := yaml.Marshal(ec)
	if err != nil {
		return false, fmt.Errorf("failed to marshal extproc config: %w", err)
	}
	if err = c.writeFilterConfigBundle(ctx, gatewayName, gatewayNamespace, configSecretNamespace, marshaled, uuid); err != nil {
		return false, err
	}
	return hasEffectiveRoute, nil
}

// reconcileFilterConfigSecretForMCPGateway updates the filter config secret for the external processor.
func mcpConfig(mcpRoutes []aigv1b1.MCPRoute) (_ *filterapi.MCPConfig, hasEffectiveRoute bool) {
	if len(mcpRoutes) == 0 {
		return nil, false
	}

	mc := &filterapi.MCPConfig{
		BackendListenerAddr: fmt.Sprintf("http://127.0.0.1:%d", internalapi.MCPBackendListenerPort),
	}
	for i := range mcpRoutes {
		route := &mcpRoutes[i]
		if !route.GetDeletionTimestamp().IsZero() {
			continue
		}
		hasEffectiveRoute = true
		mcpRoute := filterapi.MCPRoute{
			Name:     fmt.Sprintf("%s/%s", route.Namespace, route.Name),
			Backends: []filterapi.MCPBackend{},
		}
		for _, b := range route.Spec.BackendRefs {
			mcpBackend := filterapi.MCPBackend{
				// MCPRoute doesn't support cross-namespace backend reference so just use the name.
				Name: filterapi.MCPBackendName(b.Name),
			}
			if b.ToolSelector != nil {
				mcpBackend.ToolSelector = &filterapi.MCPToolSelector{
					Include:      b.ToolSelector.Include,
					IncludeRegex: b.ToolSelector.IncludeRegex,
					Exclude:      b.ToolSelector.Exclude,
					ExcludeRegex: b.ToolSelector.ExcludeRegex,
				}
			}
			for _, fh := range b.ForwardHeaders {
				hf := filterapi.MCPHeaderForward{Name: fh.Name}
				if fh.BackendHeader != nil {
					hf.BackendHeader = *fh.BackendHeader
				}
				mcpBackend.ForwardHeaders = append(mcpBackend.ForwardHeaders, hf)
			}
			mcpRoute.Backends = append(
				mcpRoute.Backends, mcpBackend)
		}
		// Add authorization configuration for the route.
		if route.Spec.SecurityPolicy != nil && route.Spec.SecurityPolicy.Authorization != nil {
			authorization := route.Spec.SecurityPolicy.Authorization
			mcpRoute.Authorization = &filterapi.MCPRouteAuthorization{}

			if route.Spec.SecurityPolicy.OAuth != nil {
				mcpRoute.Authorization.ResourceMetadataURL = buildResourceMetadataURL(&route.Spec.SecurityPolicy.OAuth.ProtectedResourceMetadata)
			}

			defaultAction := ptr.Deref(authorization.DefaultAction, egv1a1.AuthorizationActionDeny)
			mcpRoute.Authorization.DefaultAction = filterapi.AuthorizationAction(defaultAction)

			for _, rule := range authorization.Rules {
				action := ptr.Deref(rule.Action, egv1a1.AuthorizationActionAllow)
				if mcpRoute.Authorization.Rules == nil {
					mcpRoute.Authorization.Rules = []filterapi.MCPRouteAuthorizationRule{}
				}

				mcpRule := filterapi.MCPRouteAuthorizationRule{
					Action: filterapi.AuthorizationAction(action),
					CEL:    rule.CEL,
				}

				if rule.Source != nil {
					scopes := make([]string, len(rule.Source.JWT.Scopes))
					for i, scope := range rule.Source.JWT.Scopes {
						scopes[i] = string(scope)
					}
					claims := make([]filterapi.JWTClaim, len(rule.Source.JWT.Claims))
					for i, claim := range rule.Source.JWT.Claims {
						claims[i] = filterapi.JWTClaim{
							Name:      claim.Name,
							ValueType: filterapi.JWTClaimValueType(ptr.Deref(claim.ValueType, egv1a1.JWTClaimValueTypeString)),
							Values:    append([]string(nil), claim.Values...),
						}
					}
					mcpRule.Source = &filterapi.MCPAuthorizationSource{
						JWT: filterapi.JWTSource{
							Scopes: scopes,
							Claims: claims,
						},
					}
				}

				if rule.Target != nil {
					tools := make([]filterapi.ToolCall, len(rule.Target.Tools))
					for i, tool := range rule.Target.Tools {
						tools[i] = filterapi.ToolCall{
							Backend: tool.Backend,
							Tool:    tool.Tool,
						}
					}
					mcpRule.Target = &filterapi.MCPAuthorizationTarget{
						Tools: tools,
					}
				}

				mcpRoute.Authorization.Rules = append(mcpRoute.Authorization.Rules, mcpRule)
			}
		}
		// BackendSelector reuses the same filterapi.MCPRouteAuthorization struct and CEL
		// engine as Authorization above, evaluated once per candidate backend before a
		// session is opened. Source/Target aren't set here since MCPBackendSelectorRule
		// only has cel and action.
		if route.Spec.BackendSelector != nil {
			selector := route.Spec.BackendSelector
			mcpRoute.BackendSelector = &filterapi.MCPRouteAuthorization{
				DefaultAction: filterapi.AuthorizationAction(ptr.Deref(selector.DefaultAction, egv1a1.AuthorizationActionDeny)),
			}

			for _, rule := range selector.Rules {
				action := ptr.Deref(rule.Action, egv1a1.AuthorizationActionAllow)
				mcpRoute.BackendSelector.Rules = append(mcpRoute.BackendSelector.Rules, filterapi.MCPRouteAuthorizationRule{
					Action: filterapi.AuthorizationAction(action),
					CEL:    rule.CEL,
				})
			}
		}
		// Forward OAuth claim-to-header mappings to all backends in this route.
		if route.Spec.SecurityPolicy != nil && route.Spec.SecurityPolicy.OAuth != nil {
			for _, ctoh := range route.Spec.SecurityPolicy.OAuth.ClaimToHeaders {
				if !slices.Contains(mcpRoute.ForwardHeaders, ctoh.Header) {
					mcpRoute.ForwardHeaders = append(mcpRoute.ForwardHeaders, ctoh.Header)
				}
			}
		}
		// Forward the api-key-auth client-identity header to all backends in this
		// route, mirroring the OAuth claim-to-header bridge above. Without this the
		// caller id injected by apiKeyAuth.forwardClientIDHeader has no path to the
		// MCP proxy request, so it never reaches the backends nor the MCP
		// request-attribute plumbing (metrics/spans/logs). See #2422.
		//
		// Dedup against ForwardHeaders: OAuth and API-key auth can both be configured on
		// the same route, and pointing both at the same header name is how you get a
		// single unified caller-attribution label regardless of which method authenticated.
		if route.Spec.SecurityPolicy != nil && route.Spec.SecurityPolicy.APIKeyAuth != nil {
			if h := route.Spec.SecurityPolicy.APIKeyAuth.ForwardClientIDHeader; h != nil && *h != "" {
				if !slices.Contains(mcpRoute.ForwardHeaders, *h) {
					mcpRoute.ForwardHeaders = append(mcpRoute.ForwardHeaders, *h)
				}
			}
		}
		mc.Routes = append(mc.Routes, mcpRoute)
	}
	return mc, hasEffectiveRoute
}

// stripCredentialOverrideInputHeaders puts the credential input header(s) on the backend's remove
// list so Envoy drops them before the backend sees them. HeaderMutator.Mutate keeps them in the
// extproc's local map, so the handler can still read them in Do().
//
// This is what keeps the credential off the wire. Deleting it from the requestHeaders map would
// not: only headers a handler returns become Envoy mutations. AWS has three, others one. No-op for
// the metadata source, where the credential never touches the request.
func stripCredentialOverrideInputHeaders(b *filterapi.Backend) {
	if b.Auth == nil || b.Auth.CredentialOverride == nil || len(b.Auth.CredentialOverride.InputHeadersToRemove) == 0 {
		return
	}
	if b.HeaderMutation == nil {
		b.HeaderMutation = &filterapi.HTTPHeaderMutation{}
	}
	b.HeaderMutation.Remove = append(b.HeaderMutation.Remove, b.Auth.CredentialOverride.InputHeadersToRemove...)
}

// defaultOverrideHeaderName returns the default x-aigw-* header name for the given auth type.
// These headers carry the per-request credential injected by a trusted ingress filter.
//
// For AWSCredentials this is a prefix, not a full header name: three names are derived from it.
// See internalapi.AWSCredentialOverrideHeaderNames.
func defaultOverrideHeaderName(t aigv1b1.BackendSecurityPolicyType) string {
	switch t {
	case aigv1b1.BackendSecurityPolicyTypeAPIKey:
		return "x-aigw-api-key"
	case aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey:
		return "x-aigw-anthropic-api-key"
	case aigv1b1.BackendSecurityPolicyTypeAzureAPIKey:
		return "x-aigw-azure-api-key"
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		return "x-aigw-azure-access-token"
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		return "x-aigw-gcp-access-token"
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		return internalapi.AWSCredentialOverrideHeaderPrefix
	default:
		return ""
	}
}

// defaultOverrideMetadataKey returns the default metadata key for the given auth type. Same as the
// header name except for AWSCredentials, whose metadata value is one struct holding all three inputs.
func defaultOverrideMetadataKey(t aigv1b1.BackendSecurityPolicyType) string {
	if t == aigv1b1.BackendSecurityPolicyTypeAWSCredentials {
		return internalapi.AWSCredentialOverrideMetadataKey
	}
	return defaultOverrideHeaderName(t)
}

// overrideInputHeaders returns the headers a trusted filter injects for this auth type, which are
// also the ones to strip. Three for AWS, one for the rest.
func overrideInputHeaders(t aigv1b1.BackendSecurityPolicyType, headerName string) []string {
	if t == aigv1b1.BackendSecurityPolicyTypeAWSCredentials {
		accessKeyID, secretAccessKey, sessionToken := internalapi.AWSCredentialOverrideHeaderNames(headerName)
		return []string{accessKeyID, secretAccessKey, sessionToken}
	}
	return []string{headerName}
}

// resolveCredentialOverride converts the API-level CredentialOverride to the filterapi type,
// resolving default header/key names and validating the fallback configuration.
func resolveCredentialOverride(bspType aigv1b1.BackendSecurityPolicyType, override *aigv1b1.BackendSecurityPolicyCredentialOverride, hasStaticCredential bool) (*filterapi.CredentialOverride, error) {
	if override == nil {
		return nil, nil
	}

	result := &filterapi.CredentialOverride{}

	switch {
	case override.FromRequestHeaders != nil:
		src := override.FromRequestHeaders
		headerName := src.Header
		if headerName == "" {
			headerName = defaultOverrideHeaderName(bspType)
		}
		headerName = strings.ToLower(headerName)
		result.HeaderName = headerName
		result.InputHeadersToRemove = overrideInputHeaders(bspType, headerName)
		result.FallbackToConfigured = src.FallbackToConfigured == nil || *src.FallbackToConfigured

	case override.FromDynamicMetadata != nil:
		src := override.FromDynamicMetadata
		key := src.Key
		if key == "" {
			key = defaultOverrideMetadataKey(bspType)
		}
		result.DynamicMetadataNamespace = src.Namespace
		result.DynamicMetadataKey = key
		result.FallbackToConfigured = src.FallbackToConfigured == nil || *src.FallbackToConfigured
	}

	if result.FallbackToConfigured && !hasStaticCredential {
		return nil, fmt.Errorf("credentialOverride with fallbackToConfigured=true requires a static credential to be configured")
	}

	return result, nil
}

func (c *GatewayController) bspToFilterAPIBackendAuth(ctx context.Context, backendSecurityPolicy *aigv1b1.BackendSecurityPolicy) (*filterapi.BackendAuth, error) {
	namespace := backendSecurityPolicy.Namespace
	spec := &backendSecurityPolicy.Spec
	var (
		auth          *filterapi.BackendAuth
		hasStaticCred bool
		err           error
	)

	switch spec.Type {
	case aigv1b1.BackendSecurityPolicyTypeAPIKey:
		secretName := string(spec.APIKey.SecretRef.Name)
		apiKey, getErr := c.getSecretData(ctx, namespace, secretName, apiKeyInSecret)
		if getErr != nil {
			return nil, getErr
		}
		auth = &filterapi.BackendAuth{APIKey: &filterapi.APIKeyAuth{Key: apiKey}}
		hasStaticCred = true
	case aigv1b1.BackendSecurityPolicyTypeAzureAPIKey:
		secretName := string(spec.AzureAPIKey.SecretRef.Name)
		apiKey, getErr := c.getSecretData(ctx, namespace, secretName, apiKeyInSecret)
		if getErr != nil {
			return nil, getErr
		}
		auth = &filterapi.BackendAuth{AzureAPIKey: &filterapi.AzureAPIKeyAuth{Key: apiKey}}
		hasStaticCred = true
	case aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey:
		secretName := string(spec.AnthropicAPIKey.SecretRef.Name)
		apiKey, getErr := c.getSecretData(ctx, namespace, secretName, apiKeyInSecret)
		if getErr != nil {
			return nil, getErr
		}
		auth = &filterapi.BackendAuth{AnthropicAPIKey: &filterapi.AnthropicAPIKeyAuth{Key: apiKey}}
		hasStaticCred = true
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		awsCred := spec.AWSCredentials

		if awsCred.CredentialsFile == nil && awsCred.OIDCExchangeToken == nil {
			// If no credentials file or OIDC token is configured, use default credential chain
			// This allows IRSA/Pod Identity to work automatically
			auth = &filterapi.BackendAuth{AWSAuth: &filterapi.AWSAuth{Region: awsCred.Region}}
		} else {
			// Otherwise, fetch credentials from secret
			var secretName string
			if awsCred.CredentialsFile != nil {
				secretName = string(awsCred.CredentialsFile.SecretRef.Name)
			} else {
				secretName = rotators.GetBSPSecretName(backendSecurityPolicy.Name)
			}
			credentialsLiteral, getErr := c.getSecretData(ctx, namespace, secretName, rotators.AwsCredentialsKey)
			if getErr != nil {
				return nil, getErr
			}
			auth = &filterapi.BackendAuth{AWSAuth: &filterapi.AWSAuth{
				CredentialFileLiteral: credentialsLiteral,
				Region:                awsCred.Region,
			}}
		}
		// True for both branches: the handler can sign without the per-request source. The default
		// chain may still fail at request time, but rejecting fallbackToConfigured here would break
		// IRSA, where there is deliberately no secret for the controller to see.
		hasStaticCred = true
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		secretName := rotators.GetBSPSecretName(backendSecurityPolicy.Name)
		azureAccessToken, getErr := c.getSecretData(ctx, namespace, secretName, rotators.AzureAccessTokenKey)
		if getErr != nil {
			return nil, getErr
		}
		auth = &filterapi.BackendAuth{AzureAuth: &filterapi.AzureAuth{AccessToken: azureAccessToken}}
		hasStaticCred = true
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		gcpCreds := spec.GCPCredentials

		// If no credentials file or WIF is configured, use ADC (handled by extproc)
		if gcpCreds.CredentialsFile == nil && gcpCreds.WorkloadIdentityFederationConfig == nil {
			auth = &filterapi.BackendAuth{
				GCPAuth: &filterapi.GCPAuth{
					Region:      gcpCreds.Region,
					ProjectName: gcpCreds.ProjectName,
				},
			}
			// No static access token, but ADC is used — the per-request override replaces the token only.
			hasStaticCred = false
		} else {
			// Otherwise, fetch token from rotated secret
			secretName := rotators.GetBSPSecretName(backendSecurityPolicy.Name)
			gcpAccessToken, getErr := c.getSecretData(ctx, namespace, secretName, rotators.GCPAccessTokenKey)
			if getErr != nil {
				return nil, getErr
			}
			auth = &filterapi.BackendAuth{
				GCPAuth: &filterapi.GCPAuth{
					AccessToken: gcpAccessToken,
					Region:      gcpCreds.Region,
					ProjectName: gcpCreds.ProjectName,
				},
			}
			hasStaticCred = true
		}
	default:
		return nil, fmt.Errorf("invalid backend security type %s for policy %s", spec.Type, backendSecurityPolicy.Name)
	}

	// Project CredentialOverride when configured.
	if spec.CredentialOverride != nil {
		auth.CredentialOverride, err = resolveCredentialOverride(spec.Type, spec.CredentialOverride, hasStaticCred)
		if err != nil {
			return nil, fmt.Errorf("invalid credentialOverride for policy %s: %w", backendSecurityPolicy.Name, err)
		}
	}

	return auth, nil
}

func (c *GatewayController) getSecretData(ctx context.Context, namespace, name, dataKey string) (string, error) {
	secret, err := c.kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s: %w", name, err)
	}
	if secret.Data != nil {
		if value, ok := secret.Data[dataKey]; ok {
			return string(value), nil
		}
	}
	if secret.StringData != nil {
		if value, ok := secret.StringData[dataKey]; ok {
			return value, nil
		}
	}
	return "", fmt.Errorf("secret %s does not contain key %s", name, dataKey)
}

// injectQuotaPolicyCostExpressions looks up QuotaPolicies targeting the backends
// on this route and injects their CostExpression as LLMRequestCost entries into
// the ext_proc config. This allows ext_proc to compute and store quota costs in
// dynamic metadata for the rate limit filter's HitsAddend to read.
func (c *GatewayController) injectQuotaPolicyCostExpressions(
	ctx context.Context,
	route *aigv1b1.AIGatewayRoute,
	ec *filterapi.Config,
	injectedQuotaCosts map[string]struct{},
	routeName string,
) {
	var quotaPolicies aigv1a1.QuotaPolicyList
	if err := c.client.List(ctx, &quotaPolicies, client.InNamespace(route.Namespace)); err != nil {
		c.logger.Error(err, "failed to list QuotaPolicies for cost expression injection")
		return
	}

	// Collect backend names and model name overrides on this route.
	routeBackends := make(map[string]bool)
	routeModels := make(map[string]bool)
	for _, rule := range route.Spec.Rules {
		for _, br := range rule.BackendRefs {
			routeBackends[br.Name] = true
			if br.ModelNameOverride != "" {
				routeModels[br.ModelNameOverride] = true
			}
		}
	}

	for i := range quotaPolicies.Items {
		qp := &quotaPolicies.Items[i]
		// Check if this policy targets any backend on this route.
		targetsRoute := false
		for _, ref := range qp.Spec.TargetRefs {
			if routeBackends[string(ref.Name)] {
				targetsRoute = true
				break
			}
		}
		if !targetsRoute {
			continue
		}

		for _, pmq := range qp.Spec.PerModelQuotas {
			if pmq.ModelName == nil {
				continue
			}
			// Skip this PerModelQuota if the model is not served by this route.
			if len(routeModels) > 0 && !routeModels[*pmq.ModelName] {
				continue
			}
			expr := "total_tokens"
			if pmq.Quota.CostExpression != nil {
				expr = *pmq.Quota.CostExpression
			}
			if _, err := llmcostcel.NewProgram(expr); err != nil {
				c.logger.Error(err, "invalid QuotaPolicy cost expression, skipping",
					"policy", qp.Name, "model", *pmq.ModelName, "expression", expr)
				continue
			}
			// One LLMRequestCost per target backend with the Backend and Model filters.
			// ext_proc only evaluates the entry matching the serving backend and model,
			// storing the result under the shared metadata key.
			for _, ref := range qp.Spec.TargetRefs {
				backendKey := route.Namespace + "/" + string(ref.Name)
				dedupeKey := QuotaCostMetadataKey + "\x00" + *pmq.ModelName + "\x00" + backendKey
				if _, exists := injectedQuotaCosts[dedupeKey]; exists {
					continue
				}
				ec.LLMRequestCosts = append(ec.LLMRequestCosts, filterapi.LLMRequestCost{
					Type:        filterapi.LLMRequestCostTypeCEL,
					MetadataKey: QuotaCostMetadataKey,
					CEL:         expr,
					Backend:     backendKey,
					RouteName:   routeName,
					Model:       *pmq.ModelName,
				})
				injectedQuotaCosts[dedupeKey] = struct{}{}
			}
		}
	}
}

// QuotaCostMetadataKey is the dynamic metadata key used to store a
// QuotaPolicy's computed cost. A single key suffices because only one model
// is active per request, and ext_proc filters cost entries by Model before
// writing to this key.
const QuotaCostMetadataKey = "quota_cost"

// backendWithMaybeBSP retrieves the AIServiceBackend and its associated BackendSecurityPolicy if it exists.
func (c *GatewayController) backendWithMaybeBSP(ctx context.Context, namespace, name string) (backend *aigv1b1.AIServiceBackend, bsp *aigv1b1.BackendSecurityPolicy, err error) {
	backend = &aigv1b1.AIServiceBackend{}
	if err = c.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, backend); err != nil {
		return
	}

	var backendSecurityPolicyList aigv1b1.BackendSecurityPolicyList
	key := fmt.Sprintf("%s.%s", name, namespace)
	if err := c.client.List(ctx, &backendSecurityPolicyList, client.InNamespace(namespace),
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingBackendSecurityPolicy: key}); err != nil {
		return nil, nil, fmt.Errorf("failed to list BackendSecurityPolicies for backend %s: %w", name, err)
	}

	var matchingBSPs []*aigv1b1.BackendSecurityPolicy
	for i := range backendSecurityPolicyList.Items {
		policy := &backendSecurityPolicyList.Items[i]
		for _, target := range policy.Spec.TargetRefs {
			if string(target.Name) == name &&
				target.Group == aiServiceBackendGroup &&
				target.Kind == aiServiceBackendKind {
				matchingBSPs = append(matchingBSPs, policy)
			}
		}
	}

	switch len(matchingBSPs) {
	case 0:
	case 1:
		bsp = matchingBSPs[0]
	default:
		// We reject the case of multiple BackendSecurityPolicies for the same backend since that could be potentially
		// a security issue. API is clearly documented to allow only one BackendSecurityPolicy per backend.
		//
		// Same validation happens in the AIServiceBackend controller, but it might be the case that a new BackendSecurityPolicy
		// is created after the AIServiceBackend's reconciliation.
		c.logger.Info("multiple BackendSecurityPolicies found for backend", "backend_name", name, "backend_namespace", namespace,
			"count", len(matchingBSPs))
		return nil, nil, fmt.Errorf("multiple BackendSecurityPolicies found for backend %s", name)
	}
	return
}

// getBSPForInferencePool retrieves the BackendSecurityPolicy for a given InferencePool if it exists.
func (c *GatewayController) getBSPForInferencePool(ctx context.Context, namespace, name string) (*aigv1b1.BackendSecurityPolicy, error) {
	var bspList aigv1b1.BackendSecurityPolicyList
	key := fmt.Sprintf("%s.%s", name, namespace)
	if err := c.client.List(ctx, &bspList, client.InNamespace(namespace),
		client.MatchingFields{k8sClientIndexAIServiceBackendToTargetingBackendSecurityPolicy: key}); err != nil {
		return nil, fmt.Errorf("failed to list BackendSecurityPolicies for inference pool %s: %w", name, err)
	}

	var matchingBSPs []*aigv1b1.BackendSecurityPolicy
	for i := range bspList.Items {
		bsp := &bspList.Items[i]
		for _, target := range bsp.Spec.TargetRefs {
			if string(target.Name) == name &&
				target.Group == inferencePoolGroup &&
				target.Kind == inferencePoolKind {
				matchingBSPs = append(matchingBSPs, bsp)
			}
		}
	}

	if len(matchingBSPs) == 0 {
		return nil, nil
	}
	if len(matchingBSPs) > 1 {
		return nil, fmt.Errorf("multiple BackendSecurityPolicies found for inference pool %s in namespace %s", name, namespace)
	}
	return matchingBSPs[0], nil
}

// checkPodHasSideCar returns whether the extproc container is current.
func (c *GatewayController) checkPodHasSideCar(pod *corev1.Pod, needMCP bool, desiredImage string) (hasSideCar bool) {
	podSpec := pod.Spec

	if c.extProcAsSideCar {
		for i := range podSpec.InitContainers {
			if podSpec.InitContainers[i].Name == extProcContainerName && podSpec.InitContainers[i].Image == desiredImage {
				hasSideCar = true
				hasMCPAddr := false
				for j := range podSpec.InitContainers[i].Args {
					if j > 0 && podSpec.InitContainers[i].Args[j-1] == "-logLevel" && podSpec.InitContainers[i].Args[j] != c.logLevel {
						hasSideCar = false
						break
					}
					if j > 0 && podSpec.InitContainers[i].Args[j-1] == "-mcpAddr" {
						hasMCPAddr = true
					}
				}
				if needMCP && !hasMCPAddr {
					c.logger.Info("MCPRoutes exist but sidecar is missing -mcpAddr argument, triggering rollout",
						"pod", pod.Name, "namespace", pod.Namespace)
					hasSideCar = false
				}
				break
			}
		}
	} else {
		for i := range podSpec.Containers {
			if podSpec.Containers[i].Name == extProcContainerName && podSpec.Containers[i].Image == desiredImage {
				hasSideCar = true
				hasMCPAddr := false
				for j := range podSpec.Containers[i].Args {
					if j > 0 && podSpec.Containers[i].Args[j-1] == "-logLevel" && podSpec.Containers[i].Args[j] != c.logLevel {
						hasSideCar = false
						break
					}
					if j > 0 && podSpec.Containers[i].Args[j-1] == "-mcpAddr" {
						hasMCPAddr = true
					}
				}
				if needMCP && !hasMCPAddr {
					c.logger.Info("MCPRoutes exist but sidecar is missing -mcpAddr argument, triggering rollout",
						"pod", pod.Name, "namespace", pod.Namespace)
					hasSideCar = false
				}
				break
			}
		}
	}

	return hasSideCar
}

// isRolloutInProgress checks whether any Deployment or DaemonSet is currently rolling out.
func isRolloutInProgress(deployments []appsv1.Deployment, daemonSets []appsv1.DaemonSet) bool {
	for i := range deployments {
		dep := &deployments[i]
		if dep.Status.ObservedGeneration < dep.Generation {
			return true
		}
		// Rollout is still converging while total pods exceed updated pods, which
		// indicates at least one old-template pod is still present.
		if dep.Status.Replicas > dep.Status.UpdatedReplicas {
			return true
		}
	}
	for i := range daemonSets {
		ds := &daemonSets[i]
		// Ignore status-based checks until the controller observed this generation.
		if ds.Status.ObservedGeneration == 0 {
			continue
		}
		if ds.Status.ObservedGeneration < ds.Generation {
			return true
		}
		// Rollout is still converging while total pods exceed updated pods, which
		// indicates at least one old-template pod is still present.
		if ds.Status.CurrentNumberScheduled > ds.Status.UpdatedNumberScheduled {
			return true
		}
	}
	return false
}

// annotateGatewayPods annotates pods to speed filter config Secret refresh and
// rolls workload templates only when pod template state must change.
// See https://neonmirrors.net/post/2022-12/reducing-pod-volume-update-times/ for explanation.
func (c *GatewayController) annotateGatewayPods(ctx context.Context,
	pods []corev1.Pod,
	deployments []appsv1.Deployment,
	daemonSets []appsv1.DaemonSet,
	uuid string,
	hasEffectiveRoute bool,
	needMCP bool,
	desiredImage string,
	desiredHash string,
) (ctrl.Result, error) {
	if isRolloutInProgress(deployments, daemonSets) {
		const requeueAfter = 5 * time.Second
		c.logger.Info("rollout in progress - requeueing", "requeueAfter", requeueAfter.String())
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	seenWithSidecar := false
	seenWithoutSidecar := false
	for i := range pods {
		if !pods[i].GetDeletionTimestamp().IsZero() {
			continue
		}
		hasSideCar := c.checkPodHasSideCar(&pods[i], needMCP, desiredImage)
		if hasSideCar {
			seenWithSidecar = true
		} else {
			seenWithoutSidecar = true
		}
		if seenWithSidecar && seenWithoutSidecar {
			break
		}
	}
	forceRollout := seenWithSidecar && seenWithoutSidecar
	if forceRollout {
		c.logger.Info("forcing rollout",
			"podsWithSidecarSeen", seenWithSidecar, "podsWithoutSidecarSeen", seenWithoutSidecar)
	}
	hasSideCar := seenWithSidecar && !seenWithoutSidecar

	for i := range pods {
		pod := &pods[i]
		if !pod.GetDeletionTimestamp().IsZero() {
			c.logger.Info("skipping terminating pod", "namespace", pod.Namespace, "name", pod.Name)
			continue
		}
		c.logger.Info("annotating pod", "namespace", pod.Namespace, "name", pod.Name)
		_, err := c.kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType,
			fmt.Appendf(nil,
				`{"metadata":{"annotations":{"%s":"%s"}}}`, aigatewayUUIDAnnotationKey, uuid),
			metav1.PatchOptions{})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch pod %s: %w", pod.Name, err)
		}
	}

	// For sidecar state changes, annotate the workloads under three scenarios:
	// 1. If there's an effective route but no sidecar container, we need to add the sidecar container.
	// 2. If there's no effective route but has sidecar container,
	//    we need to roll out the deployment to trigger the mutation webhook to remove the sidecar container.
	// 3. If pods are inconsistent even when rollout isn't in progress, force rollout to self-heal.
	needsStateRollout := hasEffectiveRoute != hasSideCar || forceRollout
	for i := range deployments {
		dep := &deployments[i]
		// Hash changes are written to the pod template so Kubernetes owns the rollout.
		needsHashRollout := hasEffectiveRoute && desiredHash != "" && dep.Spec.Template.Annotations[extProcConfigHashAnnotationKey] != desiredHash
		if !needsStateRollout && !needsHashRollout {
			continue
		}
		c.logger.Info("rolling out deployment", "namespace", dep.Namespace, "name", dep.Name, "desiredHash", desiredHash)
		_, err := c.kube.AppsV1().Deployments(dep.Namespace).Patch(ctx, dep.Name, types.MergePatchType,
			workloadTemplateAnnotationPatch(uuid, desiredHash, needsStateRollout, needsHashRollout),
			metav1.PatchOptions{})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch deployment %s: %w", dep.Name, err)
		}
	}

	for i := range daemonSets {
		daemonSet := &daemonSets[i]
		// Hash changes are written to the pod template so Kubernetes owns the rollout.
		needsHashRollout := hasEffectiveRoute && desiredHash != "" && daemonSet.Spec.Template.Annotations[extProcConfigHashAnnotationKey] != desiredHash
		if !needsStateRollout && !needsHashRollout {
			continue
		}
		c.logger.Info("rolling out daemonSet", "namespace", daemonSet.Namespace, "name", daemonSet.Name, "desiredHash", desiredHash)
		_, err := c.kube.AppsV1().DaemonSets(daemonSet.Namespace).Patch(ctx, daemonSet.Name, types.MergePatchType,
			workloadTemplateAnnotationPatch(uuid, desiredHash, needsStateRollout, needsHashRollout),
			metav1.PatchOptions{})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch daemonset %s: %w", daemonSet.Name, err)
		}
	}
	return ctrl.Result{}, nil
}

func workloadTemplateAnnotationPatch(uuid, desiredHash string, includeUUID, includeHash bool) []byte {
	annotations := make([]string, 0, 2)
	if includeUUID {
		annotations = append(annotations, fmt.Sprintf(`"%s":"%s"`, aigatewayUUIDAnnotationKey, uuid))
	}
	if includeHash {
		annotations = append(annotations, fmt.Sprintf(`"%s":"%s"`, extProcConfigHashAnnotationKey, desiredHash))
	}
	return []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%s}}}}}`, strings.Join(annotations, ",")))
}

// getObjectsForGateway retrieves the pods, deployments, and daemonsets for a given Gateway.
// They are all created and managed by the Envoy Gateway controller. Depending on the deployment strategy of Envoy Gateway,
// the namespace is either the same as the Gateway's namespace or the Envoy Gateway system namespace.
// This returns the **unique** namespace where the Gateway's pods, deployments, and daemonsets are running.
func (c *GatewayController) getObjectsForGateway(ctx context.Context, gw *gwapiv1.Gateway) (
	namespace string,
	pods []corev1.Pod,
	deployments []appsv1.Deployment,
	daemonSets []appsv1.DaemonSet,
	err error,
) {
	listOption := metav1.ListOptions{LabelSelector: fmt.Sprintf(
		"%s=%s,%s=%s", egOwningGatewayNameLabel, gw.Name, egOwningGatewayNamespaceLabel, gw.Namespace,
	)}

	candidateNamespaces := make([]string, 1, 2)
	candidateNamespaces[0] = gw.Namespace
	if c.envoyGatewayNamespace != gw.Namespace {
		candidateNamespaces = append(candidateNamespaces, c.envoyGatewayNamespace)
	}

	var distinctNamespaces []string
	for _, ns := range candidateNamespaces {
		var ps *corev1.PodList
		ps, err = c.kube.CoreV1().Pods(ns).List(ctx, listOption)
		if err != nil {
			err = fmt.Errorf("failed to list pods in namespace %s: %w", ns, err)
			return
		}
		pods = append(pods, ps.Items...)

		var ds *appsv1.DeploymentList
		ds, err = c.kube.AppsV1().Deployments(ns).List(ctx, listOption)
		if err != nil {
			err = fmt.Errorf("failed to list deployments in namespace %s: %w", ns, err)
			return
		}
		deployments = append(deployments, ds.Items...)

		var dss *appsv1.DaemonSetList
		dss, err = c.kube.AppsV1().DaemonSets(ns).List(ctx, listOption)
		if err != nil {
			err = fmt.Errorf("failed to list daemonsets in namespace %s: %w", ns, err)
			return
		}
		daemonSets = append(daemonSets, dss.Items...)

		if len(ps.Items) > 0 || len(ds.Items) > 0 || len(dss.Items) > 0 {
			distinctNamespaces = append(distinctNamespaces, ns)
		}
	}

	// All pods, deployments, and daemonsets should be in the same namespace.
	// Otherwise, it would be a bug in the EG or the disruptive configuration change of EG.
	if len(distinctNamespaces) > 1 {
		err = fmt.Errorf("found gateway-labeled objects in multiple namespaces: %v", distinctNamespaces)
		return
	}

	if len(pods) > 0 {
		namespace = pods[0].Namespace
	}
	if len(deployments) > 0 {
		namespace = deployments[0].Namespace
	}
	if len(daemonSets) > 0 {
		namespace = daemonSets[0].Namespace
	}
	return
}

// fetchGatewayConfig returns the referenced GatewayConfig (if present) for the given Gateway.
// Returns nil if no GatewayConfig is referenced or if it cannot be found.
// fetchGatewayConfig returns the referenced GatewayConfig (if present) for the given Gateway.
// Returns (nil, nil) if: no annotation, empty annotation, or GatewayConfig not found.
// Returns (nil, error) for transient failures (API errors) to trigger reconciliation retry.
func (c *GatewayController) fetchGatewayConfig(ctx context.Context, gw *gwapiv1.Gateway) (*aigv1b1.GatewayConfig, error) {
	configName, ok := gw.Annotations[GatewayConfigAnnotationKey]
	if !ok || configName == "" {
		return nil, nil
	}

	// Fetch the GatewayConfig (must be in same namespace as Gateway).
	var gatewayConfig aigv1b1.GatewayConfig
	if err := c.client.Get(ctx, client.ObjectKey{Name: configName, Namespace: gw.Namespace}, &gatewayConfig); err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("GatewayConfig referenced by Gateway not found, using defaults",
				"gateway_name", gw.Name, "gateway_namespace", gw.Namespace, "gatewayconfig_name", configName)
			return nil, nil
		}
		// Return error for transient failures (e.g., API errors) to trigger retry.
		return nil, fmt.Errorf("failed to get GatewayConfig: %w", err)
	}

	c.logger.Info("found GatewayConfig for Gateway",
		"gateway_name", gw.Name, "gateway_namespace", gw.Namespace, "gatewayconfig_name", configName)
	return &gatewayConfig, nil
}
