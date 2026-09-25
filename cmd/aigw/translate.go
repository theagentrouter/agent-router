// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	fake2 "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	kyaml "sigs.k8s.io/yaml"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller"
)

// translate reads the input files, collects the AI Gateway custom resources,
// translates them to Envoy Gateway and Kubernetes objects, and writes the translated objects to the output writer.
func translate(ctx context.Context, paths []string, output, stderr io.Writer) error {
	stderrLogger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{}))
	yaml, err := readYamlsAsString(paths)
	if err != nil {
		return err
	}
	aigwRoutes, mcpRoutes, aigwBackends, backendSecurityPolicies, backendTLSConfigs, originalGateways, originalSecrets, _, err := collectObjects(yaml, output, stderrLogger)
	if err != nil {
		return fmt.Errorf("error translating: %w", err)
	}

	_, _, httpRoutes, extensionPolicies, httpRouteFilter, backends, secrets, backendTrafficPolicies, securityPolicies, err := translateCustomResourceObjects(ctx, aigwRoutes, mcpRoutes, aigwBackends, backendSecurityPolicies, backendTLSConfigs, originalGateways, originalSecrets, stderrLogger)
	if err != nil {
		return fmt.Errorf("error emitting: %w", err)
	}

	// Emit the translated objects.
	for i := range httpRoutes.Items {
		httpRoute := &httpRoutes.Items[i]
		mustWriteObj(&httpRoute.TypeMeta, httpRoute, output)
	}
	for i := range extensionPolicies.Items {
		extensionPolicy := &extensionPolicies.Items[i]
		mustWriteObj(&extensionPolicy.TypeMeta, extensionPolicy, output)
	}
	for i := range backends.Items {
		backend := &backends.Items[i]
		mustWriteObj(&backend.TypeMeta, backend, output)
	}
	for i := range httpRouteFilter.Items {
		filter := &httpRouteFilter.Items[i]
		mustWriteObj(&filter.TypeMeta, filter, output)
	}
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		mustWriteObj(&secret.TypeMeta, secret, output)
	}
	for _, secret := range originalSecrets {
		mustWriteObj(&secret.TypeMeta, secret, output)
	}
	for _, gateway := range originalGateways {
		mustWriteObj(&gateway.TypeMeta, gateway, output)
	}
	for i := range backendTrafficPolicies.Items {
		btp := &backendTrafficPolicies.Items[i]
		mustWriteObj(&btp.TypeMeta, btp, output)
	}
	for i := range securityPolicies.Items {
		sp := &securityPolicies.Items[i]
		mustWriteObj(&sp.TypeMeta, sp, output)
	}
	return nil
}

// readYamlsAsString reads the files at the given paths and combines them into a single string.
func readYamlsAsString(paths []string) (string, error) {
	var buf strings.Builder
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("error reading file %s: %w", path, err)
		}
		buf.Write(content)
		buf.WriteString("\n---\n")
	}
	return buf.String(), nil
}

// collectObjects reads the YAML input and collects target resources. Currently, this will collect
// AIGatewayRoute, AIServiceBackend, BackendSecurityPolicy, and Secret resources. Other resources
// will be written back to the output writer.
//
// If the resource is not an AI Gateway custom resource, it will be written back to the output writer.
func collectObjects(yamlInput string, out io.Writer, logger *slog.Logger) (
	aigwRoutes []*aigv1b1.AIGatewayRoute,
	mcpRoutes []*aigv1b1.MCPRoute,
	aigwBackends []*aigv1b1.AIServiceBackend,
	backendSecurityPolicies []*aigv1b1.BackendSecurityPolicy,
	backendTLSConfigs []*gwapiv1.BackendTLSPolicy,
	gws []*gwapiv1.Gateway,
	secrets []*corev1.Secret,
	envoyProxies []*egv1a1.EnvoyProxy,
	err error,
) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(yamlInput)), 4096)
	exists := make(map[string]struct{})
	for {
		var rawObj runtime.RawExtension
		err = decoder.Decode(&rawObj)
		if errors.Is(err, io.EOF) {
			err = nil
			return
		} else if err != nil {
			err = fmt.Errorf("error decoding YAML: %w", err)
			return
		}

		if len(rawObj.Raw) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		_, _, err = unstructured.UnstructuredJSONScheme.Decode(rawObj.Raw, nil, obj)
		if err != nil {
			err = fmt.Errorf("error decoding unstructured object: %w", err)
			return
		}
		// Deduplicate objects, skipping if already seen.
		key := fmt.Sprintf("%s/%s", obj.GetKind(), obj.GetName())
		if _, ok := exists[key]; !ok {
			exists[key] = struct{}{}
		} else {
			logger.Info("Skipping duplicate object", "kind", obj.GetKind(), "name", obj.GetName())
			continue
		}

		switch obj.GetKind() {
		case "AIGatewayRoute":
			mustExtractAndAppend(obj, &aigwRoutes)
		case "MCPRoute":
			mustExtractAndAppend(obj, &mcpRoutes)
		case "AIServiceBackend":
			mustExtractAndAppend(obj, &aigwBackends)
		case "BackendSecurityPolicy":
			mustExtractAndAppend(obj, &backendSecurityPolicies)
		case "Secret":
			mustExtractAndAppend(obj, &secrets)
		case "Gateway":
			mustExtractAndAppend(obj, &gws)
		case "EnvoyProxy":
			mustExtractAndAppend(obj, &envoyProxies)
			mustWriteObj(nil, obj, out)
		case "BackendTLSPolicy":
			// We need to keep track of BackendTLSPolicy objects to load them in the fake client, but we don't
			// need to reconcile them; just create them as-is.
			mustExtractAndAppend(obj, &backendTLSConfigs)
			mustWriteObj(nil, obj, out)
		case "GatewayConfig":
			// GatewayConfig is gateway-scoped configuration for extproc containers.
			// Write it back as-is to the output.
			logger.Info("Writing GatewayConfig to output as-is", "name", obj.GetName())
			mustWriteObj(nil, obj, out)
		default:
			// Now you can inspect or manipulate the CRD.
			logger.Info("Writing back non-target object into the output as-is", "kind", obj.GetKind(), "name", obj.GetName())
			mustWriteObj(nil, obj, out)
		}
	}
}

// translateCustomResourceObjects translates the AI Gateway custom resources to Envoy Gateway and Kubernetes objects.
func translateCustomResourceObjects(
	ctx context.Context,
	aigwRoutes []*aigv1b1.AIGatewayRoute,
	mcpRoutes []*aigv1b1.MCPRoute,
	aigwBackends []*aigv1b1.AIServiceBackend,
	backendSecurityPolicies []*aigv1b1.BackendSecurityPolicy,
	backendTLSPolicies []*gwapiv1.BackendTLSPolicy,
	gws []*gwapiv1.Gateway,
	usedDefinedSecrets []*corev1.Secret,
	logger *slog.Logger,
) (
	fakeClient client.Client,
	fakeClientSet *fake2.Clientset,
	httpRoutes gwapiv1.HTTPRouteList,
	extensionPolicies egv1a1.EnvoyExtensionPolicyList,
	httpRouteFilters egv1a1.HTTPRouteFilterList,
	backends egv1a1.BackendList,
	secrets *corev1.SecretList,
	backendTrafficPolicies egv1a1.BackendTrafficPolicyList,
	securityPolicies egv1a1.SecurityPolicyList,
	err error,
) {
	builder := fake.NewClientBuilder().
		WithScheme(controller.Scheme).
		WithStatusSubresource(&aigv1b1.AIGatewayRoute{}).
		WithStatusSubresource(&aigv1b1.MCPRoute{}).
		WithStatusSubresource(&aigv1b1.AIServiceBackend{}).
		WithStatusSubresource(&aigv1b1.BackendSecurityPolicy{})
	_ = controller.ApplyIndexing(ctx, func(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
		builder = builder.WithIndex(obj, field, extractValue)
		return nil
	}) // Error should never happen.
	fakeClient = builder.Build()
	fakeClientSet = fake2.NewClientset()

	// Store the user-defined secrets in the fake client set so that Gateway controller can read them.
	userDefinedSecretKeys := map[string]struct{}{}
	for _, s := range usedDefinedSecrets {
		if _, err = fakeClientSet.CoreV1().Secrets(s.Namespace).Create(ctx, s, metav1.CreateOptions{}); err != nil {
			err = fmt.Errorf("error creating secret: %w", err)
			return
		}
		userDefinedSecretKeys[fmt.Sprintf("%s/%s", s.Namespace, s.Name)] = struct{}{}
	}

	// Use buffered event channels so that controllers can push gateway sync events without blocking,
	// This mirrors the production code at controller/controller.go:128
	const eventChanBuffer = 100
	bspC := controller.NewBackendSecurityPolicyController(fakeClient, fakeClientSet, logr.FromSlogHandler(logger.Handler()),
		make(chan event.GenericEvent, eventChanBuffer), make(chan event.GenericEvent, eventChanBuffer))
	aisbC := controller.NewAIServiceBackendController(fakeClient, fakeClientSet, logr.FromSlogHandler(logger.Handler()),
		make(chan event.GenericEvent, eventChanBuffer))
	airC := controller.NewAIGatewayRouteController(fakeClient, fakeClientSet, logr.FromSlogHandler(logger.Handler()),
		make(chan event.GenericEvent, eventChanBuffer), "/",
	)
	mcpC := controller.NewMCPRouteController(fakeClient, fakeClientSet, logr.FromSlogHandler(logger.Handler()),
		make(chan event.GenericEvent, eventChanBuffer),
	)
	gwC := controller.NewGatewayController(fakeClient, fakeClientSet, logr.FromSlogHandler(logger.Handler()), "envoy-gateway-system",
		true, func() string {
			return "aigw-translate"
		}, &controller.Options{
			ExtProcImage:    "docker.io/envoyproxy/ai-gateway-extproc:latest",
			ExtProcLogLevel: "debug",
		}, false,
	)
	// Pre-create Gateways (without reconciling) before reconciling resources so that
	// syncGateways can resolve the parent Gateway via the fake client.
	// Otherwise the reconcile would fail with "gateway not found" because the
	// Gateway is not yet present in the cache at this point.
	for _, gw := range gws {
		mustCreate(ctx, fakeClient, gw, logger)
	}
	// Create and reconcile the custom resources to store the translated objects.
	// Note that the order of creation is important as some objects depend on others.
	for _, btp := range backendTLSPolicies {
		mustCreate(ctx, fakeClient, btp, logger)
	}
	for _, bsp := range backendSecurityPolicies {
		mustCreateAndReconcile(ctx, fakeClient, bsp, bspC, logger)
	}
	for _, backend := range aigwBackends {
		mustCreateAndReconcile(ctx, fakeClient, backend, aisbC, logger)
	}
	for _, route := range aigwRoutes {
		mustCreateAndReconcile(ctx, fakeClient, route, airC, logger)
	}
	for _, mcpRoute := range mcpRoutes {
		mustCreateAndReconcile(ctx, fakeClient, mcpRoute, mcpC, logger)
	}
	for _, gw := range gws {
		mustReconcile(ctx, gw, gwC, logger)
	}

	// Now you can retrieve the translated objects from the fake client.
	err = fakeClient.List(ctx, &httpRoutes)
	if err != nil {
		err = fmt.Errorf("error listing HTTPRoutes: %w", err)
		return
	}
	err = fakeClient.List(ctx, &extensionPolicies)
	if err != nil {
		err = fmt.Errorf("error listing EnvoyExtensionPolicies: %w", err)
		return
	}
	err = fakeClient.List(ctx, &backends)
	if err != nil {
		err = fmt.Errorf("error listing Backends: %w", err)
		return
	}
	err = fakeClient.List(ctx, &httpRouteFilters)
	if err != nil {
		err = fmt.Errorf("error listing HTTPRouteFilters: %w", err)
		return
	}
	secrets, err = fakeClientSet.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		err = fmt.Errorf("error listing Secrets: %w", err)
		return
	}
	err = fakeClient.List(ctx, &backendTrafficPolicies)
	if err != nil {
		err = fmt.Errorf("error listing BackendTrafficPolicies: %w", err)
		return
	}
	err = fakeClient.List(ctx, &securityPolicies)
	if err != nil {
		err = fmt.Errorf("error listing SecurityPolicies: %w", err)
		return
	}
	// We only want to return the secrets that are not user-defined, but created by the controller.
	for i := len(secrets.Items) - 1; i >= 0; i-- {
		secret := &secrets.Items[i]
		if _, ok := userDefinedSecretKeys[fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)]; ok {
			secrets.Items = append(secrets.Items[:i], secrets.Items[i+1:]...)
		}
	}
	return
}

// mustExtractAndAppend extracts the object from the unstructured object and appends it to the slice.
func mustExtractAndAppend[T any](obj *unstructured.Unstructured, slice *[]T) {
	var item T
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), &item)
	if err != nil {
		panic(err)
	}
	*slice = append(*slice, item)
}

// mustCreateAndReconcile creates the object in the fake client.
func mustCreate(ctx context.Context, fakeClient client.Client, obj client.Object, logger *slog.Logger) {
	logger.Info("Fake creating", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName())
	err := fakeClient.Create(ctx, obj)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info("Skipping already existing object", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName())
			return
		}
		panic(err)
	}
}

// mustCreateAndReconcile creates the object in the fake client and reconciles it.
func mustCreateAndReconcile(
	ctx context.Context,
	fakeClient client.Client, obj client.Object,
	c reconcile.TypedReconciler[reconcile.Request],
	logger *slog.Logger,
) {
	mustCreate(ctx, fakeClient, obj, logger)
	mustReconcile(ctx, obj, c, logger)
}

// mustReconcile reconciles the object using the provided reconciler.
func mustReconcile(
	ctx context.Context,
	obj client.Object,
	c reconcile.TypedReconciler[reconcile.Request],
	logger *slog.Logger,
) {
	logger.Info("Fake reconciling", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName())
	_, err := c.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}})
	if err != nil {
		panic(err)
	}
}

func mustSetGroupVersionKind(typedMeta *metav1.TypeMeta, obj client.Object) {
	// https://github.com/kubernetes-sigs/controller-runtime/issues/1517#issuecomment-844703142
	gvks, unversioned, err := controller.Scheme.ObjectKinds(obj)
	if err != nil {
		panic(err)
	}
	if !unversioned && len(gvks) != 1 {
		panic(fmt.Errorf("expected exactly one GVK, got %d", len(gvks)))
	}
	typedMeta.SetGroupVersionKind(gvks[0])
}

// mustWriteObj writes the object to the writer, panicking on error.
//
// This sets the kind and API version of the object to the values in the TypeMeta as it is not set from the fake client.
func mustWriteObj(typedMeta *metav1.TypeMeta, obj client.Object, w io.Writer) {
	_, _ = w.Write([]byte("---\n"))
	if typedMeta != nil {
		mustSetGroupVersionKind(typedMeta, obj)
	}
	// Ignore ManagedFields as they are not relevant to the user.
	obj.SetManagedFields(nil)
	// Ignore ResourceVersion as it is not relevant to the user.
	obj.SetResourceVersion("")
	marshaled, err := kyaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	_, _ = w.Write(marshaled)
}
