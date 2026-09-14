// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// gatewayMutator implements [admission.CustomDefaulter].
type gatewayMutator struct {
	codec serializer.CodecFactory
	c     client.Client
	// noCacheReader bypasses the informer cache during admission to avoid
	// cache sync races that can cause extProc sidecar injection to be skipped.
	noCacheReader client.Reader
	kube          kubernetes.Interface
	logger        logr.Logger

	// extProcBuilder is the single source of truth for the injected extproc
	// container. It is shared with the gateway reconciler so that the desired
	// config hash written to workload templates matches webhook injection.
	*extProcBuilder
}

func newGatewayMutator(c client.Client, noCacheReader client.Reader, kube kubernetes.Interface, logger logr.Logger,
	builder *extProcBuilder,
) *gatewayMutator {
	return &gatewayMutator{
		c:              c,
		codec:          serializer.NewCodecFactory(Scheme),
		noCacheReader:  noCacheReader,
		kube:           kube,
		logger:         logger,
		extProcBuilder: builder,
	}
}

// Default implements [admission.CustomDefaulter].
func (g *gatewayMutator) Default(ctx context.Context, obj runtime.Object) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		panic(fmt.Sprintf("BUG: unexpected object type %T, expected *corev1.Pod", obj))
	}
	gatewayName := pod.Labels[egOwningGatewayNameLabel]
	gatewayNamespace := pod.Labels[egOwningGatewayNamespaceLabel]
	g.logger.Info("mutating gateway pod",
		"pod_name", pod.Name, "pod_namespace", pod.Namespace,
		"gateway_name", gatewayName, "gateway_namespace", gatewayNamespace,
	)
	if err := g.mutatePod(ctx, pod, gatewayName, gatewayNamespace); err != nil {
		g.logger.Error(err, "failed to mutate deployment", "name", pod.Name, "namespace", pod.Namespace)
		return err
	}
	return nil
}

const (
	mutationNamePrefix   = "ai-gateway-"
	extProcContainerName = mutationNamePrefix + "extproc"
)

// ParseExtraEnvVars parses semicolon-separated key=value pairs into a list of
// environment variables. The input delimiter is a semicolon (';') to allow
// values to contain commas without escaping.
// Example: "OTEL_SERVICE_NAME=ai-gateway;OTEL_TRACES_EXPORTER=otlp".
func ParseExtraEnvVars(s string) ([]corev1.EnvVar, error) {
	if s == "" {
		return nil, nil
	}

	pairs := strings.Split(s, ";")
	result := make([]corev1.EnvVar, 0, len(pairs))
	for i, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue // Skip empty pairs from trailing semicolons.
		}

		key, value, found := strings.Cut(pair, "=")
		if !found {
			return nil, fmt.Errorf("invalid env var pair at position %d: %q (expected format: KEY=value)", i+1, pair)
		}

		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("empty env var name at position %d: %q", i+1, pair)
		}
		result = append(result, corev1.EnvVar{Name: key, Value: value})
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

// ParseImagePullSecrets parses semicolon-separated secret names into a list of
// LocalObjectReference objects for image pull secrets.
// Example: "my-registry-secret;another-secret".
func ParseImagePullSecrets(s string) ([]corev1.LocalObjectReference, error) {
	if s == "" {
		return nil, nil
	}

	names := strings.Split(s, ";")
	result := make([]corev1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue // Skip empty names from trailing semicolons.
		}
		result = append(result, corev1.LocalObjectReference{Name: name})
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

func (g *gatewayMutator) listAIGatewayRoutesForGateway(ctx context.Context, gatewayName, gatewayNamespace string) (aigv1b1.AIGatewayRouteList, error) {
	return listAIGatewayRoutesForGateway(ctx, g.c, g.noCacheReader, gatewayName, gatewayNamespace)
}

func (g *gatewayMutator) listMCPRoutesForGateway(ctx context.Context, gatewayName, gatewayNamespace string) (aigv1b1.MCPRouteList, error) {
	return listMCPRoutesForGateway(ctx, g.c, g.noCacheReader, gatewayName, gatewayNamespace)
}

func listAIGatewayRoutesForGateway(ctx context.Context, cacheReader client.Reader, noCacheReader client.Reader, gatewayName, gatewayNamespace string) (aigv1b1.AIGatewayRouteList, error) {
	var routes aigv1b1.AIGatewayRouteList
	key := fmt.Sprintf("%s.%s", gatewayName, gatewayNamespace)
	cacheErr := cacheReader.List(ctx, &routes, client.MatchingFields{
		k8sClientIndexAIGatewayRouteToAttachedGateway: key,
	})
	if cacheErr == nil && len(routes.Items) > 0 {
		return routes, nil
	}
	if noCacheReader == nil {
		return routes, cacheErr
	}
	// noCacheReader doesn't have access to cache indexes, so list then filter.
	var all aigv1b1.AIGatewayRouteList
	if err := noCacheReader.List(ctx, &all); err != nil {
		return routes, fmt.Errorf("failed to list routes: %w", err)
	}
	routes.Items = filterAIGatewayRoutesForGateway(all.Items, gatewayName, gatewayNamespace)
	return routes, nil
}

func listMCPRoutesForGateway(ctx context.Context, cacheReader client.Reader, noCacheReader client.Reader, gatewayName, gatewayNamespace string) (aigv1b1.MCPRouteList, error) {
	var routes aigv1b1.MCPRouteList
	key := fmt.Sprintf("%s.%s", gatewayName, gatewayNamespace)
	cacheErr := cacheReader.List(ctx, &routes, client.MatchingFields{
		k8sClientIndexMCPRouteToAttachedGateway: key,
	})
	if cacheErr == nil && len(routes.Items) > 0 {
		return routes, nil
	}
	if noCacheReader == nil {
		return routes, cacheErr
	}
	// noCacheReader doesn't have access to cache indexes, so list then filter.
	var all aigv1b1.MCPRouteList
	if err := noCacheReader.List(ctx, &all); err != nil {
		return routes, fmt.Errorf("failed to list MCP routes: %w", err)
	}
	routes.Items = filterMCPRoutesForGateway(all.Items, gatewayName, gatewayNamespace)
	return routes, nil
}

func filterAIGatewayRoutesForGateway(routes []aigv1b1.AIGatewayRoute, gatewayName, gatewayNamespace string) []aigv1b1.AIGatewayRoute {
	var filtered []aigv1b1.AIGatewayRoute
	for i := range routes {
		if parentRefsMatchGateway(routes[i].Namespace, routes[i].Spec.ParentRefs, gatewayName, gatewayNamespace) {
			filtered = append(filtered, routes[i])
		}
	}
	return filtered
}

func filterMCPRoutesForGateway(routes []aigv1b1.MCPRoute, gatewayName, gatewayNamespace string) []aigv1b1.MCPRoute {
	var filtered []aigv1b1.MCPRoute
	for i := range routes {
		if parentRefsMatchGateway(routes[i].Namespace, routes[i].Spec.ParentRefs, gatewayName, gatewayNamespace) {
			filtered = append(filtered, routes[i])
		}
	}
	return filtered
}

// parentRefsMatchGateway replicates the namespace resolution logic used in
// aiGatewayRouteToAttachedGatewayIndexFunc and mcpRouteToAttachedGatewayIndexFunc.
// If the index key format or namespace resolution logic changes, this function
// must be updated to match.
func parentRefsMatchGateway(routeNamespace string, parentRefs []gwapiv1.ParentReference, gatewayName, gatewayNamespace string) bool {
	for _, ref := range parentRefs {
		namespace := routeNamespace
		if ref.Namespace != nil && *ref.Namespace != "" {
			namespace = string(*ref.Namespace)
		}
		if string(ref.Name) == gatewayName && namespace == gatewayNamespace {
			return true
		}
	}
	return false
}

func (g *gatewayMutator) mutatePod(ctx context.Context, pod *corev1.Pod, gatewayName, gatewayNamespace string) error {
	routes, err := g.listAIGatewayRoutesForGateway(ctx, gatewayName, gatewayNamespace)
	if err != nil {
		return fmt.Errorf("failed to list routes: %w", err)
	}

	mcpRoutes, err := g.listMCPRoutesForGateway(ctx, gatewayName, gatewayNamespace)
	if err != nil {
		return fmt.Errorf("failed to list MCP routes: %w", err)
	}
	if len(routes.Items) == 0 && len(mcpRoutes.Items) == 0 {
		g.logger.Info("no AIGatewayRoutes or MCPRoutes found for gateway", "name", gatewayName, "namespace", gatewayNamespace)
		return nil
	}
	g.logger.Info("found routes for gateway", "aigatewayroute_count", len(routes.Items), "mcpgatewayroute_count", len(mcpRoutes.Items))

	podspec := &pod.Spec

	// Resolve the config bundle.
	// If it does not exist, skip mutation to avoid blocking Envoy pod creation.
	// The controller will later trigger new pod mutations by updating pod/deployment annotations when the config secrets are created.
	bundleConfigIndexSecretName := FilterConfigBundleIndexSecretName(gatewayName, gatewayNamespace)

	bundleConfigIndexSecret, err := g.kube.CoreV1().Secrets(pod.Namespace).Get(ctx, bundleConfigIndexSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		g.logger.Info("no filter config secret found, skipping mutation",
			"gateway_name", gatewayName, "gateway_namespace", gatewayNamespace)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get bundled filter config secret %s: %w", bundleConfigIndexSecretName, err)
	}
	if bundleConfigIndexSecret == nil {
		g.logger.Info("no filter config secret found, skipping mutation",
			"gateway_name", gatewayName, "gateway_namespace", gatewayNamespace)
		return nil
	}

	gatewayConfig, err := g.fetchGatewayConfig(ctx, gatewayName, gatewayNamespace)
	if err != nil {
		return fmt.Errorf("failed to fetch GatewayConfig: %w", err)
	}

	// Now we construct the AI Gateway managed containers and volumes.
	filterConfigBundleVolumeName := filterConfigBundleVolumeName(gatewayName, gatewayNamespace)
	volumes := []corev1.Volume{
		{
			Name: extProcUDSVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
	projections := []corev1.VolumeProjection{
		{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: bundleConfigIndexSecretName},
				Items: []corev1.KeyToPath{
					{
						Key:  FilterConfigBundleIndexKey,
						Path: filterapi.ConfigBundleIndexFileName,
					},
				},
			},
		},
	}
	optional := true
	for i := range maxFilterConfigBundleSlots {
		projections = append(projections, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: filterConfigBundlePartSecretName(gatewayName, gatewayNamespace, i),
				},
				Optional: &optional,
				Items: []corev1.KeyToPath{
					{
						Key:  FilterConfigBundlePartKey,
						Path: filterapi.ConfigBundlePartPath(i),
					},
				},
			},
		})
	}
	volumes = append(volumes, corev1.Volume{
		Name: filterConfigBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{Sources: projections},
		},
	})
	podspec.Volumes = append(podspec.Volumes, volumes...)

	// Add imagePullSecrets for extProc if configured
	if len(g.imagePullSecrets) > 0 {
		podspec.ImagePullSecrets = append(podspec.ImagePullSecrets, g.imagePullSecrets...)
	}

	const filterConfigBundleMountPath = "/etc/filter-config-bundle"
	udsMountPath := filepath.Dir(g.udsPath)

	// Build the extproc container via the shared builder. The builder produces the
	// base container (image, env, base args, UDS mount, resources, securityContext,
	// GatewayConfig volumeMounts, readiness probe); the secret-presence-driven parts
	// (-configBundlePath and its volumeMount) are
	// added below, since they depend on which filter-config secrets exist at pod
	// creation time and must stay out of the drift hash.
	input := extProcContainerInput{gatewayConfig: gatewayConfig, needMCP: len(mcpRoutes.Items) > 0}
	container := g.buildExtProcContainer(input)

	container.Args = append([]string{"-configBundlePath", filterConfigBundleMountPath}, container.Args...)
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      filterConfigBundleVolumeName,
		MountPath: filterConfigBundleMountPath,
		ReadOnly:  true,
	})

	if g.extProcAsSideCar {
		// RestartPolicy is set by the builder for the sidecar case.
		podspec.InitContainers = append(podspec.InitContainers, container)
	} else {
		podspec.Containers = append(podspec.Containers, container)
	}

	// Lastly, we need to mount the Envoy container with the extproc socket.
	for i := range podspec.Containers {
		c := &podspec.Containers[i]
		if c.Name == "envoy" {
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
				Name:      extProcUDSVolumeName,
				MountPath: udsMountPath,
				ReadOnly:  false,
			})
		}
	}
	return nil
}

// fetchGatewayConfig returns the referenced GatewayConfig (if present) for the given Gateway.
// Returns (nil, nil) if: Gateway not found, no annotation, empty annotation, or GatewayConfig not found.
// Returns (nil, error) for transient failures (API errors) to trigger mutation retry.
func (g *gatewayMutator) fetchGatewayConfig(ctx context.Context, gatewayName, gatewayNamespace string) (*aigv1b1.GatewayConfig, error) {
	// Fetch the Gateway object.
	var gateway gwapiv1.Gateway
	if err := g.c.Get(ctx, client.ObjectKey{Name: gatewayName, Namespace: gatewayNamespace}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			g.logger.Info("Gateway not found, using global default configuration",
				"gateway_name", gatewayName, "gateway_namespace", gatewayNamespace)
			return nil, nil
		}
		// Return error for transient failures (e.g., API errors) to trigger retry.
		return nil, fmt.Errorf("failed to get Gateway: %w", err)
	}

	configName, ok := gateway.Annotations[GatewayConfigAnnotationKey]
	if !ok || configName == "" {
		return nil, nil
	}

	// Fetch the GatewayConfig (must be in same namespace as Gateway).
	var gatewayConfig aigv1b1.GatewayConfig
	if err := g.c.Get(ctx, client.ObjectKey{Name: configName, Namespace: gatewayNamespace}, &gatewayConfig); err != nil {
		if apierrors.IsNotFound(err) {
			g.logger.Info("GatewayConfig referenced by Gateway not found, using global defaults",
				"gateway_name", gatewayName, "gatewayconfig_name", configName)
			return nil, nil
		}
		// Return error for transient failures (e.g., API errors) to trigger retry.
		return nil, fmt.Errorf("failed to get GatewayConfig: %w", err)
	}

	g.logger.Info("found GatewayConfig for Gateway",
		"gateway_name", gatewayName, "gatewayconfig_name", configName)
	return &gatewayConfig, nil
}
