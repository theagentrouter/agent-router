// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"
	"strconv"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	gwaiev1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a3 "sigs.k8s.io/gateway-api/apis/v1alpha3"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/runner"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(Scheme))
	utilruntime.Must(aigv1a1.AddToScheme(Scheme))
	utilruntime.Must(aigv1b1.AddToScheme(Scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(Scheme))
	utilruntime.Must(egv1a1.AddToScheme(Scheme))
	utilruntime.Must(gwapiv1.Install(Scheme))
	utilruntime.Must(gwapiv1a3.Install(Scheme))
	utilruntime.Must(gwapiv1b1.Install(Scheme))
	utilruntime.Must(gwaiev1.Install(Scheme))
}

// Scheme contains the necessary schemes for the AI Gateway.
//
// This is exported for testing purposes.
var Scheme = runtime.NewScheme()

// Options defines the program configurable options that may be passed on the command line.
type Options struct {
	// ExtProcLogLevel is the log level for the external processor, e.g., debug, info, warn, or error.
	ExtProcLogLevel string
	// ExtProcLogFormat is the log output format for the external processor, "text" or "json".
	// Empty means the extproc default, which is text.
	ExtProcLogFormat string
	// ExtProcEnableRedaction enables redaction of sensitive information in debug logs for the external processor.
	ExtProcEnableRedaction bool
	// ExtProcImage is the image for the external processor set on Deployment.
	ExtProcImage string
	// ExtProcImagePullPolicy is the image pull policy for the external processor set on Deployment.
	ExtProcImagePullPolicy corev1.PullPolicy
	// EnableLeaderElection enables leader election for the controller manager.
	// Enabling this ensures there is only one active controller manager.
	EnableLeaderElection bool
	// UDSPath is the path to the UDS socket for the external processor.
	UDSPath string
	// DisableMutatingWebhook disables the mutating webhook for the Gateway for testing purposes.
	DisableMutatingWebhook bool
	// RequestHeaderAttributes is the comma-separated key-value pairs for mapping HTTP request headers to Otel attributes shared across metrics, spans, and access logs.
	RequestHeaderAttributes *string
	// TracingRequestHeaderAttributes is the comma-separated key-value pairs for mapping HTTP request headers to otel span attributes.
	TracingRequestHeaderAttributes *string
	// MetricsRequestHeaderAttributes is the comma-separated key-value pairs for mapping HTTP request headers to Otel metric attributes.
	MetricsRequestHeaderAttributes *string
	// LogRequestHeaderAttributes is the comma-separated key-value pairs for mapping HTTP request headers to access log attributes.
	LogRequestHeaderAttributes *string
	// RootPrefix is the root prefix for all the routes handled by the AI Gateway.
	RootPrefix string
	// ExtProcExtraEnvVars is the semicolon-separated key=value pairs for extra environment variables in extProc container.
	ExtProcExtraEnvVars string
	// ExtProcImagePullSecrets is the semicolon-separated list of image pull secret names for extProc container.
	ExtProcImagePullSecrets string
	// ExtProcMaxRecvMsgSize is the maximum message size in bytes that the gRPC server can receive for extProc.
	ExtProcMaxRecvMsgSize int
	// MCPSessionEncryptionSeed is the seed used to derive the encryption key for MCP session encryption.
	MCPSessionEncryptionSeed string
	// MCPSessionEncryptionIterations is the number of iterations to use for PBKDF2 key derivation for MCP session encryption.
	MCPSessionEncryptionIterations int
	// MCPFallbackSessionEncryptionSeed is the optional fallback seed used for MCP session key rotation.
	MCPFallbackSessionEncryptionSeed string
	// MCPFallbackSessionEncryptionIterations is the number of iterations used in the fallback PBKDF2 key derivation for MCP session encryption.
	MCPFallbackSessionEncryptionIterations int
	// EndpointPrefixes is the comma-separated key-value pairs for endpoint prefixes.
	EndpointPrefixes string
	// RateLimitRunner is the xDS runner that serves rate limit configs to the rate limit service.
	RateLimitRunner *runner.Runner
	// EnvoyGatewayNamespace is the namespace where Envoy Gateway is deployed.
	EnvoyGatewayNamespace string
}

// StartControllers starts the controllers for the AI Gateway.
// This blocks until the manager is stopped.
//
// Note: this is tested with envtest, hence the test exists outside of this package. See /tests/controller_test.go.
func StartControllers(ctx context.Context, mgr manager.Manager, config *rest.Config, logger logr.Logger, options *Options) (err error) {
	c := mgr.GetClient()
	indexer := mgr.GetFieldIndexer()
	if err = ApplyIndexing(ctx, indexer.IndexField); err != nil {
		return fmt.Errorf("failed to apply indexing: %w", err)
	}
	var versionInfo *version.Info
	kube := kubernetes.NewForConfigOrDie(config)
	versionInfo, err = kube.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("failed to get server version: %w", err)
	}

	gatewayEventChan := make(chan event.GenericEvent, 100)
	// The extproc builder is shared by the mutating webhook and the gateway
	// reconciler. The webhook uses this builder instance to inject the container;
	// the reconciler builds an identical one from the same options to write the
	// desired config hash to workload pod templates.
	extProcBuilder := newExtProcBuilder(options, isKubernetes133OrLater(versionInfo, logger), logger)
	gatewayC := NewGatewayController(c, kubernetes.NewForConfigOrDie(config),
		logger.WithName("gateway"), options.EnvoyGatewayNamespace,
		false, uuid.NewString, options, isKubernetes133OrLater(versionInfo, logger))
	if err = TypedControllerBuilderForCRD(mgr, &gwapiv1.Gateway{}).
		WatchesRawSource(source.Channel(
			gatewayEventChan,
			&handler.EnqueueRequestForObject{},
		)).
		Complete(gatewayC); err != nil {
		return fmt.Errorf("failed to create controller for Gateway: %w", err)
	}

	aiGatewayRouteEventChan := make(chan event.GenericEvent, 100)
	routeC := NewAIGatewayRouteController(c, kubernetes.NewForConfigOrDie(config), logger.WithName("ai-gateway-route"),
		gatewayEventChan, options.RootPrefix,
	)
	if err = TypedControllerBuilderForCRD(mgr, &aigv1b1.AIGatewayRoute{}).
		Owns(&gwapiv1.HTTPRoute{}).
		Owns(&egv1a1.HTTPRouteFilter{}).
		WatchesRawSource(source.Channel(
			aiGatewayRouteEventChan,
			&handler.EnqueueRequestForObject{},
		)).
		Complete(routeC); err != nil {
		return fmt.Errorf("failed to create controller for AIGatewayRoute: %w", err)
	}

	aiServiceBackendEventChan := make(chan event.GenericEvent, 100)
	backendC := NewAIServiceBackendController(c, kubernetes.NewForConfigOrDie(config), logger.
		WithName("ai-service-backend"), aiGatewayRouteEventChan)
	aiServiceBackendBuilder := TypedControllerBuilderForCRD(mgr, &aigv1b1.AIServiceBackend{}).
		WatchesRawSource(source.Channel(
			aiServiceBackendEventChan,
			&handler.EnqueueRequestForObject{},
		))

	backendSecurityPolicyEventChan := make(chan event.GenericEvent, 100)
	inferencePoolEventChan := make(chan event.GenericEvent, 100)
	backendSecurityPolicyC := NewBackendSecurityPolicyController(c, kubernetes.NewForConfigOrDie(config), logger.
		WithName("backend-security-policy"), aiServiceBackendEventChan, inferencePoolEventChan)
	if err = TypedControllerBuilderForCRD(mgr, &aigv1b1.BackendSecurityPolicy{}).
		WatchesRawSource(source.Channel(
			backendSecurityPolicyEventChan,
			&handler.EnqueueRequestForObject{},
		)).
		Owns(&corev1.Secret{}).
		Complete(backendSecurityPolicyC); err != nil {
		return fmt.Errorf("failed to create controller for BackendSecurityPolicy: %w", err)
	}

	// Check if InferencePool CRD exists before creating the controller.
	crdClient, err := apiextensionsclientset.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create CRD client for inference extension: %w", err)
	}
	const inferencePoolCRD = "inferencepools.inference.networking.k8s.io"
	if _, crdErr := crdClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, inferencePoolCRD, metav1.GetOptions{}); crdErr != nil {
		if apierrors.IsNotFound(crdErr) {
			logger.Info("InferencePool CRD not found, skipping InferencePool controller. " +
				"If you need it, please install the Gateway API Inference Extension CRDs.")
		} else {
			return fmt.Errorf("failed to query InferencePool CRD: %w", crdErr)
		}
	} else {
		// CRD exists, create the controller.
		inferencePoolC := NewInferencePoolController(c, kubernetes.NewForConfigOrDie(config), logger.
			WithName("inference-pool"), inferencePoolEventChan)
		if err = TypedControllerBuilderForCRD(mgr, &gwaiev1.InferencePool{}).
			Watches(&gwapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(inferencePoolC.gatewayEventHandler)).
			Watches(&aigv1b1.AIGatewayRoute{}, handler.EnqueueRequestsFromMapFunc(inferencePoolC.aiGatewayRouteEventHandler)).
			Watches(&gwapiv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(inferencePoolC.httpRouteEventHandler)).
			WatchesRawSource(source.Channel(
				inferencePoolEventChan,
				&handler.EnqueueRequestForObject{},
			)).
			Complete(inferencePoolC); err != nil {
			return fmt.Errorf("failed to create controller for InferencePool: %w", err)
		}
		// When the InferencePool CRD is present, watch InferencePool events so that
		// AIServiceBackend objects whose backendRef targets an InferencePool are re-reconciled
		// whenever the pool changes (e.g., existence check needs to be re-evaluated).
		aiServiceBackendBuilder = aiServiceBackendBuilder.
			Watches(&gwaiev1.InferencePool{}, handler.EnqueueRequestsFromMapFunc(backendC.inferencePoolEventHandler))
	}
	if err = aiServiceBackendBuilder.Complete(backendC); err != nil {
		return fmt.Errorf("failed to create controller for AIServiceBackend: %w", err)
	}
	mcpRouteEventChan := make(chan event.GenericEvent, 100)
	secretC := NewSecretController(c, kubernetes.NewForConfigOrDie(config), logger.
		WithName("secret"), backendSecurityPolicyEventChan, mcpRouteEventChan)
	// Do not use TypedControllerBuilderForCRD for secret, as changing a secret content doesn't change the generation.
	if err = ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		Complete(secretC); err != nil {
		return fmt.Errorf("failed to create controller for Secret: %w", err)
	}

	mcpRouteC := NewMCPRouteController(c, kubernetes.NewForConfigOrDie(config), logger.WithName("ai-gateway-mcp-route"),
		gatewayEventChan,
	)
	if err = TypedControllerBuilderForCRD(mgr, &aigv1b1.MCPRoute{}).
		Owns(&gwapiv1.HTTPRoute{}).
		WatchesRawSource(source.Channel(
			mcpRouteEventChan,
			&handler.EnqueueRequestForObject{},
		)).
		Complete(mcpRouteC); err != nil {
		return fmt.Errorf("failed to create controller for MCPRoute: %w", err)
	}

	// GatewayConfig controller for gateway-scoped configuration.
	gatewayConfigC := NewGatewayConfigController(c, logger.WithName("gateway-config"), gatewayEventChan)
	if err = TypedControllerBuilderForCRD(mgr, &aigv1b1.GatewayConfig{}).
		Complete(gatewayConfigC); err != nil {
		return fmt.Errorf("failed to create controller for GatewayConfig: %w", err)
	}

	// QuotaPolicy controller for backend quota rate limiting.
	if options.RateLimitRunner != nil {
		quotaPolicyC := NewQuotaPolicyController(c, kube, logger.WithName("quota-policy"), options.RateLimitRunner, aiGatewayRouteEventChan)
		if err = TypedControllerBuilderForCRD(mgr, &aigv1a1.QuotaPolicy{}).
			Watches(&aigv1b1.AIServiceBackend{}, handler.EnqueueRequestsFromMapFunc(quotaPolicyC.BackendToQuotaPolicy)).
			Complete(quotaPolicyC); err != nil {
			return fmt.Errorf("failed to create controller for QuotaPolicy: %w", err)
		}
	}

	// ReferenceGrant controller for cross-namespace access validation
	referenceGrantC := NewReferenceGrantController(c, logger.WithName("reference-grant"), aiGatewayRouteEventChan)
	if err = TypedControllerBuilderForCRD(mgr, &gwapiv1b1.ReferenceGrant{}).
		Complete(referenceGrantC); err != nil {
		return fmt.Errorf("failed to create controller for ReferenceGrant: %w", err)
	}

	if !options.DisableMutatingWebhook {
		h := admission.WithCustomDefaulter(Scheme, &corev1.Pod{}, newGatewayMutator(c, mgr.GetAPIReader(), kube,
			logger.WithName("gateway-mutator"),
			extProcBuilder,
		))
		mgr.GetWebhookServer().Register("/mutate", &webhook.Admission{Handler: h})
	}

	if err = mgr.Start(ctx); err != nil { // This blocks until the manager is stopped.
		return fmt.Errorf("failed to start controller manager: %w", err)
	}
	return nil
}

// TypedControllerBuilderForCRD returns a new controller builder for the given CRD object type.
//
// This is to share the common logic for setting up a controller for a given object type.
//
// Exported for testing purposes in tests/controller_test.go.
func TypedControllerBuilderForCRD(mgr ctrl.Manager, obj client.Object) *ctrl.Builder {
	return ctrl.NewControllerManagedBy(mgr).
		For(obj).
		// We do not need to watch for changes in the status subresource.
		WithEventFilter(predicate.GenerationChangedPredicate{})
}

const (
	// Indexes for AI Gateway
	//
	// k8sClientIndexAIGatewayRouteToAttachedGateway is the index name that maps from a Gateway to the
	// AIGatewayRoute that attaches to it.
	k8sClientIndexAIGatewayRouteToAttachedGateway = "GWAPIGatewayToReferencingAIGatewayRoute"
	// k8sClientIndexSecretToReferencingBackendSecurityPolicy is the index name that maps
	// from a Secret to the BackendSecurityPolicy that references it.
	k8sClientIndexSecretToReferencingBackendSecurityPolicy = "SecretToReferencingBackendSecurityPolicy"
	// k8sClientIndexSecretToReferencingMCPRoute is the index name that maps
	// from a Secret to the MCPRoute that references it.
	k8sClientIndexSecretToReferencingMCPRoute = "SecretToReferencingMCPRoute"
	// k8sClientIndexBackendToReferencingAIGatewayRoute is the index name that maps from a Backend to the
	// AIGatewayRoute that references it.
	k8sClientIndexBackendToReferencingAIGatewayRoute = "BackendToReferencingAIGatewayRoute"
	// k8sClientIndexAIServiceBackendToTargetingBackendSecurityPolicy is the index name that maps from an AIServiceBackend
	// to the BackendSecurityPolicy whose targetRefs contains the AIServiceBackend.
	k8sClientIndexAIServiceBackendToTargetingBackendSecurityPolicy = "AIServiceBackendToTargetingBackendSecurityPolicy"
	// k8sClientIndexAIServiceBackendToTargetingQuotaPolicy is the index name that maps from an AIServiceBackend
	// to the QuotaPolicy whose targetRefs contains the AIServiceBackend.
	k8sClientIndexAIServiceBackendToTargetingQuotaPolicy = "AIServiceBackendToTargetingQuotaPolicy"
	// k8sClientIndexGatewayToGatewayConfig maps from a GatewayConfig name to Gateways referencing it.
	k8sClientIndexGatewayToGatewayConfig = "GatewayToGatewayConfig"

	// k8sClientIndexReferenceGrantToTargetKind is the index name that maps from namespace/kind to ReferenceGrants, enabling efficient lookup of grants
	// allowing access to specific resource types in specific namespaces.
	k8sClientIndexReferenceGrantToTargetKind = "ReferenceGrantToTargetKind"

	// Indexes for MCP Gateway
	//
	// k8sClientIndexMCPRouteToAttachedGateway is the index name that maps from a Gateway to the
	// MCPRoute that attaches to it.
	k8sClientIndexMCPRouteToAttachedGateway = "GWAPIGatewayToReferencingMCPRoute"

	// Indexes for MCPRoute
	//
	// k8sClientIndexMCPRouteToOwnedHTTPRoute is the index name that maps from an MCPRoute to the
	// HTTPRoutes it owns, enabling efficient lookup of child HTTPRoutes for orphan cleanup.
	k8sClientIndexMCPRouteToOwnedHTTPRoute = "MCPRouteToOwnedHTTPRoute"
)

// ApplyIndexing applies indexing to the given indexer. This is exported for testing purposes.
func ApplyIndexing(ctx context.Context, indexer func(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error) error {
	err := indexer(ctx, &aigv1b1.AIGatewayRoute{},
		k8sClientIndexBackendToReferencingAIGatewayRoute, aiGatewayRouteIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from Backends to AIGatewayRoute: %w", err)
	}
	err = indexer(ctx, &aigv1b1.AIGatewayRoute{},
		k8sClientIndexAIGatewayRouteToAttachedGateway, aiGatewayRouteToAttachedGatewayIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from Gateway to AIGatewayRoute: %w", err)
	}
	err = indexer(ctx, &aigv1b1.BackendSecurityPolicy{},
		k8sClientIndexSecretToReferencingBackendSecurityPolicy, backendSecurityPolicyIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from Secret to BackendSecurityPolicy: %w", err)
	}
	err = indexer(ctx, &aigv1b1.BackendSecurityPolicy{},
		k8sClientIndexAIServiceBackendToTargetingBackendSecurityPolicy, backendSecurityPolicyTargetRefsIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to index field for BackendSecurityPolicy targetRefs: %w", err)
	}

	err = indexer(ctx, &aigv1a1.QuotaPolicy{},
		k8sClientIndexAIServiceBackendToTargetingQuotaPolicy, quotaPolicyTargetRefsIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to index field for QuotaPolicy targetRefs: %w", err)
	}

	err = indexer(ctx, &gwapiv1.Gateway{},
		k8sClientIndexGatewayToGatewayConfig, gatewayToGatewayConfigIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from GatewayConfig to Gateway: %w", err)
	}

	err = indexer(ctx, &gwapiv1b1.ReferenceGrant{},
		k8sClientIndexReferenceGrantToTargetKind, referenceGrantToTargetKindIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from target kind to ReferenceGrant: %w", err)
	}

	err = indexer(ctx, &aigv1b1.MCPRoute{},
		k8sClientIndexMCPRouteToAttachedGateway, mcpRouteToAttachedGatewayIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from Gateway to MCPRoute: %w", err)
	}
	err = indexer(ctx, &aigv1b1.MCPRoute{},
		k8sClientIndexSecretToReferencingMCPRoute, mcpRouteToReferencedSecret)
	if err != nil {
		return fmt.Errorf("failed to create index from Gateway to MCPRoute: %w", err)
	}
	err = indexer(ctx, &gwapiv1.HTTPRoute{},
		k8sClientIndexMCPRouteToOwnedHTTPRoute, httpRouteToOwnerMCPRouteIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to create index from MCPRoute to owned HTTPRoutes: %w", err)
	}
	return nil
}

func mcpRouteToAttachedGatewayIndexFunc(o client.Object) []string {
	mcpRoute := o.(*aigv1b1.MCPRoute)
	var ret []string
	for _, ref := range mcpRoute.Spec.ParentRefs {
		// Use the namespace from parentRef if specified, otherwise use the route's namespace.
		namespace := mcpRoute.Namespace
		if ref.Namespace != nil && *ref.Namespace != "" {
			namespace = string(*ref.Namespace)
		}
		ret = append(ret, fmt.Sprintf("%s.%s", ref.Name, namespace))
	}
	return ret
}

func mcpRouteToReferencedSecret(o client.Object) []string {
	mcpRoute := o.(*aigv1b1.MCPRoute)
	var ret []string
	for _, ref := range mcpRoute.Spec.BackendRefs {
		if ref.SecurityPolicy == nil || ref.SecurityPolicy.APIKey == nil || ref.SecurityPolicy.APIKey.SecretRef == nil {
			continue
		}
		apiKeyRef := ref.SecurityPolicy.APIKey.SecretRef
		// Use the namespace from parentRef if specified, otherwise use the route's namespace.
		namespace := mcpRoute.Namespace
		if apiKeyRef.Namespace != nil && *apiKeyRef.Namespace != "" {
			namespace = string(*apiKeyRef.Namespace)
		}
		ret = append(ret, fmt.Sprintf("%s.%s", apiKeyRef.Name, namespace))
	}
	return ret
}

func httpRouteToOwnerMCPRouteIndexFunc(o client.Object) []string {
	owner := metav1.GetControllerOf(o)
	if owner == nil || owner.Kind != "MCPRoute" {
		return nil
	}
	return []string{fmt.Sprintf("%s.%s", owner.Name, o.GetNamespace())}
}

func gatewayToGatewayConfigIndexFunc(o client.Object) []string {
	gateway := o.(*gwapiv1.Gateway)

	configName, ok := gateway.Annotations[GatewayConfigAnnotationKey]
	if !ok || configName == "" {
		return nil
	}
	return []string{configName}
}

func aiGatewayRouteToAttachedGatewayIndexFunc(o client.Object) []string {
	aiGatewayRoute := o.(*aigv1b1.AIGatewayRoute)
	var ret []string
	for _, ref := range aiGatewayRoute.Spec.ParentRefs {
		// Use the namespace from parentRef if specified, otherwise use the route's namespace.
		namespace := aiGatewayRoute.Namespace
		if ref.Namespace != nil && *ref.Namespace != "" {
			namespace = string(*ref.Namespace)
		}
		ret = append(ret, fmt.Sprintf("%s.%s", ref.Name, namespace))
	}
	return ret
}

func aiGatewayRouteIndexFunc(o client.Object) []string {
	aiGatewayRoute := o.(*aigv1b1.AIGatewayRoute)
	var ret []string
	for _, rule := range aiGatewayRoute.Spec.Rules {
		for _, backend := range rule.BackendRefs {
			// Use the namespace from the backend reference, or default to the route's namespace
			backendNamespace := backend.GetNamespace(aiGatewayRoute.Namespace)
			key := fmt.Sprintf("%s.%s", backend.Name, backendNamespace)
			ret = append(ret, key)
		}
	}
	return ret
}

func backendSecurityPolicyIndexFunc(o client.Object) []string {
	backendSecurityPolicy := o.(*aigv1b1.BackendSecurityPolicy)
	var key string
	switch backendSecurityPolicy.Spec.Type {
	case aigv1b1.BackendSecurityPolicyTypeAPIKey:
		apiKey := backendSecurityPolicy.Spec.APIKey
		key = getSecretNameAndNamespace(apiKey.SecretRef, backendSecurityPolicy.Namespace)
	case aigv1b1.BackendSecurityPolicyTypeAWSCredentials:
		awsCreds := backendSecurityPolicy.Spec.AWSCredentials
		if awsCreds.CredentialsFile != nil {
			key = getSecretNameAndNamespace(awsCreds.CredentialsFile.SecretRef, backendSecurityPolicy.Namespace)
		} else if awsCreds.OIDCExchangeToken != nil {
			key = backendSecurityPolicyKey(backendSecurityPolicy.Namespace, backendSecurityPolicy.Name)
		}
	case aigv1b1.BackendSecurityPolicyTypeGCPCredentials:
		gcpCreds := backendSecurityPolicy.Spec.GCPCredentials
		if gcpCreds.CredentialsFile != nil {
			key = getSecretNameAndNamespace(gcpCreds.CredentialsFile.SecretRef, backendSecurityPolicy.Namespace)
		}
	case aigv1b1.BackendSecurityPolicyTypeAzureAPIKey:
		apiKey := backendSecurityPolicy.Spec.AzureAPIKey
		key = getSecretNameAndNamespace(apiKey.SecretRef, backendSecurityPolicy.Namespace)
	case aigv1b1.BackendSecurityPolicyTypeAnthropicAPIKey:
		apiKey := backendSecurityPolicy.Spec.AnthropicAPIKey
		key = getSecretNameAndNamespace(apiKey.SecretRef, backendSecurityPolicy.Namespace)
	case aigv1b1.BackendSecurityPolicyTypeAzureCredentials:
		azureCreds := backendSecurityPolicy.Spec.AzureCredentials
		if azureCreds.ClientSecretRef != nil {
			key = getSecretNameAndNamespace(azureCreds.ClientSecretRef, backendSecurityPolicy.Namespace)
		} else if azureCreds.OIDCExchangeToken != nil {
			key = backendSecurityPolicyKey(backendSecurityPolicy.Namespace, backendSecurityPolicy.Name)
		}
	}
	return []string{key}
}

func backendSecurityPolicyTargetRefsIndexFunc(o client.Object) []string {
	backendSecurityPolicy := o.(*aigv1b1.BackendSecurityPolicy)
	var ret []string
	for _, targetRef := range backendSecurityPolicy.Spec.TargetRefs {
		ret = append(ret, fmt.Sprintf("%s.%s", targetRef.Name, backendSecurityPolicy.Namespace))
	}
	return ret
}

func quotaPolicyTargetRefsIndexFunc(o client.Object) []string {
	quotaPolicy := o.(*aigv1a1.QuotaPolicy)
	var ret []string
	for _, targetRef := range quotaPolicy.Spec.TargetRefs {
		ret = append(ret, fmt.Sprintf("%s.%s", targetRef.Name, quotaPolicy.Namespace))
	}
	return ret
}

func getSecretNameAndNamespace(secretRef *gwapiv1.SecretObjectReference, namespace string) string {
	if secretRef.Namespace != nil {
		return fmt.Sprintf("%s.%s", secretRef.Name, *secretRef.Namespace)
	}
	return fmt.Sprintf("%s.%s", secretRef.Name, namespace)
}

func getReferenceGrantIndexKey(namespace, kind string) string {
	return fmt.Sprintf("%s.%s", namespace, kind)
}

func referenceGrantToTargetKindIndexFunc(o client.Object) []string {
	referenceGrant := o.(*gwapiv1b1.ReferenceGrant)
	var keys []string
	for _, to := range referenceGrant.Spec.To {
		key := getReferenceGrantIndexKey(referenceGrant.Namespace, string(to.Kind))
		keys = append(keys, key)
	}
	return keys
}

// newConditions creates a new condition with the given type and message.
//
// Currently, we only set one condition at a time either "Accepted" or "NotAccepted".
// In the future, if we can have multiple conditions like multiple errors, we can make changes here.
func newConditions(conditionType, message string) []metav1.Condition {
	condition := metav1.Condition{Message: message, LastTransitionTime: metav1.Now()}
	// Note: we use the fixed reason for now since the message is enough to describe the error and
	// reason doesn't fit the entire message.
	switch conditionType {
	case aigv1b1.ConditionTypeAccepted:
		condition.Type = aigv1b1.ConditionTypeAccepted
		condition.Status = metav1.ConditionTrue
		condition.Reason = "ReconciliationSucceeded"
	case aigv1b1.ConditionTypeNotAccepted:
		condition.Type = aigv1b1.ConditionTypeNotAccepted
		condition.Status = metav1.ConditionFalse
		condition.Reason = "ReconciliationFailed"
	}
	return []metav1.Condition{condition}
}

// aiGatewayControllerFinalizer is the name of the finalizer added to various AI Gateway resources.
const aiGatewayControllerFinalizer = "aigateway.envoyproxy.io/finalizer"

// handleFinalizer checks if the object has a deletion timestamp. If it does, it removes the finalizer and
// calls the onDeletionFn if provided. Otherwise, it adds the finalizer to the object and updates it
// so that the finalizer is persisted.
//
// onDeletionFn can be nil, in which case it will not be called. The function can return an error but should not
// be a recoverable error. For example, onDeletionFn only propagates the deletion of the object to other resources.
// See the call sites of this function for examples.
func handleFinalizer[objType client.Object](
	ctx context.Context, client client.Client,
	logger logr.Logger,
	o objType,
	onDeletionFn func(ctx context.Context, o objType) error,
) (onDelete bool) {
	if o.GetDeletionTimestamp().IsZero() {
		if !ctrlutil.ContainsFinalizer(o, aiGatewayControllerFinalizer) {
			ctrlutil.AddFinalizer(o, aiGatewayControllerFinalizer)
			if err := client.Update(ctx, o); err != nil {
				// This shouldn't happen in normal operation, but if it does, we log the error.
				logger.Error(err, "Failed to add finalizer to object",
					"namespace", o.GetNamespace(), "name", o.GetName())
			}
		}
		return false
	}
	if ctrlutil.ContainsFinalizer(o, aiGatewayControllerFinalizer) {
		ctrlutil.RemoveFinalizer(o, aiGatewayControllerFinalizer)
		if onDeletionFn != nil {
			if err := onDeletionFn(ctx, o); err != nil {
				// onDeletionFn can return an error, but it should not be a recoverable error.
				logger.Error(err, "Failed to handle finalizer deletion",
					"namespace", o.GetNamespace(), "name", o.GetName())
			}
		}
		if err := client.Update(ctx, o); err != nil {
			// This shouldn't happen in normal operation, but if it does, we log the error.
			logger.Error(err, "Failed to remove finalizer from object",
				"namespace", o.GetNamespace(), "name", o.GetName())
		}
	}
	return true
}

// isKubernetes133OrLater returns true if the Kubernetes version is 1.33 or later.
func isKubernetes133OrLater(versionInfo *version.Info, logger logr.Logger) bool {
	major, minor := versionInfo.Major, versionInfo.Minor
	majorInt, err := strconv.Atoi(major)
	if err != nil {
		logger.Error(err, "failed to parse major version", "major", major)
		return false
	}
	minorInt, err := strconv.Atoi(minor)
	if err != nil {
		logger.Error(err, "failed to parse minor version", "minor", minor)
		return false
	}
	return majorInt >= 1 && minorInt >= 33
}
