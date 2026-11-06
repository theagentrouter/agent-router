// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

const (
	defaultMCPPath         = "/mcp"
	mcpProxyBackendDummyIP = "192.0.2.42" // RFC 5737 TEST-NET-2, used as a dummy IP.
)

// MCPRouteController implements [reconcile.TypedReconciler].
//
// This handles the MCPRoute resource and creates the necessary resources for the external process.
//
// Exported for testing purposes.
type MCPRouteController struct {
	client client.Client
	kube   kubernetes.Interface
	logger logr.Logger
	// gatewayEventChan is a channel to send events to the gateway controller.
	gatewayEventChan chan event.GenericEvent
	// referenceGrantValidator authorizes cross-namespace apiKey.secretRef references via Gateway API
	// ReferenceGrant.
	referenceGrantValidator *referenceGrantValidator
}

// NewMCPRouteController creates a new reconcile.TypedReconciler[reconcile.Request] for the MCPRoute resource.
func NewMCPRouteController(
	client client.Client, kube kubernetes.Interface, logger logr.Logger,
	gatewayEventChan chan event.GenericEvent,
) *MCPRouteController {
	return &MCPRouteController{
		client:                  client,
		kube:                    kube,
		logger:                  logger,
		gatewayEventChan:        gatewayEventChan,
		referenceGrantValidator: newReferenceGrantValidator(client),
	}
}

// Reconcile implements [reconcile.TypedReconciler].
func (c *MCPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	c.logger.Info("Reconciling MCPRoute", "namespace", req.Namespace, "name", req.Name)

	var MCPRoute aigv1b1.MCPRoute
	if err := c.client.Get(ctx, req.NamespacedName, &MCPRoute); err != nil {
		if client.IgnoreNotFound(err) == nil {
			c.logger.Info("Deleting MCPRoute",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if err := c.syncMCPRoute(ctx, &MCPRoute); err != nil {
		c.logger.Error(err, "failed to sync MCPRoute")
		c.updateMCPRouteStatus(ctx, &MCPRoute, aigv1b1.ConditionTypeNotAccepted, err.Error())
		return ctrl.Result{}, err
	}
	c.updateMCPRouteStatus(ctx, &MCPRoute, aigv1b1.ConditionTypeAccepted, "MCP Gateway Route reconciled successfully")
	return reconcile.Result{}, nil
}

// syncMCPRoute is the main logic for reconciling the MCPRoute resource.
// This is decoupled from the Reconcile method to centralize the error handling and status updates.
func (c *MCPRouteController) syncMCPRoute(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	// On deletion, propagate to the referenced Gateways and clean up the shared Backend if this
	// is the last MCPRoute in the namespace.
	if handleFinalizer(ctx, c.client, c.logger, mcpRoute, c.onMCPRouteDeleted) {
		return nil
	}

	// A backend whose Secret reference is not permitted by a ReferenceGrant must not keep the credential
	// the controller copied for it while a grant existed. Converge those backends first, sync the rest of
	// the route as usual, and report the denial through the status at the end, the way
	// BackendSecurityPolicy handles a denied grant. The denial is reported next to any error of the later
	// steps: the denied backends' objects are already gone, and the status must say why.
	denied, err := c.revokeDeniedSecretRefs(ctx, mcpRoute)
	if err != nil {
		return errors.Join(err, denied.err)
	}
	return errors.Join(c.syncMCPRouteResources(ctx, mcpRoute, denied), denied.err)
}

// syncMCPRouteResources creates or updates the resources of the MCPRoute, skipping the backends in
// denied, which revokeDeniedSecretRefs has converged to the never-granted state.
func (c *MCPRouteController) syncMCPRouteResources(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, denied deniedSecretRefs) error {
	// Validate prefixMode=Never (whether set per-backend or inherited from the route-level
	// default): each effectively-Never backend must declare toolSelector.include, and tool
	// names must be unique across all effectively-Never backends on the route.
	if err := validatePerBackendPrefixMode(mcpRoute); err != nil {
		return err
	}

	// Ensure the shared per-namespace MCP proxy Backend exists before creating/updating the HTTPRoute.
	sharedBackendName, err := c.ensureMCPProxyBackend(ctx, mcpRoute.Namespace)
	if err != nil {
		return fmt.Errorf("failed to ensure MCP proxy Backend: %w", err)
	}

	// Clean up the old per-MCPRoute Backend left from before the shared-Backend refactor.
	c.deleteOldMCPRouteBackend(ctx, mcpRoute)

	c.logger.Info("Syncing MCPRoute", "namespace", mcpRoute.Namespace, "name", mcpRoute.Name)

	// First, we create or update the main HTTPRoute that routes to the MCP proxy.
	// The main HTTPRoutes will not be "moved" into the MCP Backend listener in the extension server.
	mainHTTPRouteName := internalapi.MCPMainHTTPRoutePrefix + mcpRoute.Name
	mainHTTPRoute, existing, err := c.getOrNewHTTPRouteRoute(ctx, mcpRoute, mainHTTPRouteName)
	if err != nil {
		return fmt.Errorf("failed to get or create HTTPRoute: %w", err)
	}
	if err = c.newMainHTTPRoute(mainHTTPRoute, mcpRoute, sharedBackendName); err != nil {
		return fmt.Errorf("failed to construct a new HTTPRoute: %w", err)
	}

	// Create or Update the main HTTPRoute.
	if err = c.createOrUpdateHTTPRoute(ctx, mainHTTPRoute, existing); err != nil {
		return fmt.Errorf("failed to create or update HTTPRoute: %w", err)
	}

	existingPerBackendRoutes, err := c.listExistingPerBackendHTTPRoutes(ctx, mcpRoute)
	if err != nil {
		return fmt.Errorf("failed to list existing per-backend HTTPRoutes: %w", err)
	}

	// Build HTTPRoute for each backend in the MCPRoute to avoid the hard limit of 16 Rules per HTTPRoute.
	// The route here will be moved to the backend listener in the extension server behind the MCP Proxy.
	//
	// Each backend will have its own rule that matches the internalapi.MCPBackendHeader set by the MCP proxy.
	// This allows the MCP proxy to route requests to the correct backend based on the header.
	for i := range mcpRoute.Spec.BackendRefs {
		ref := &mcpRoute.Spec.BackendRefs[i]
		name := mcpPerBackendRefHTTPRouteName(mcpRoute.Name, ref.Name)
		if denied.has(name) {
			// Already converged to the never-granted state by revokeDeniedSecretRefs; keep it out of the
			// orphan cleanup as well.
			delete(existingPerBackendRoutes, name)
			continue
		}
		httpRoute, existing := existingPerBackendRoutes[name]
		if !existing {
			httpRoute, err = c.newHTTPRoute(mcpRoute, name)
			if err != nil {
				return fmt.Errorf("failed to construct a new HTTPRoute for backend %s: %w", ref.Name, err)
			}
		}
		if err = c.newPerBackendRefHTTPRoute(ctx, httpRoute, mcpRoute, ref); err != nil {
			return fmt.Errorf("failed to construct a new HTTPRoute for backend %s: %w", ref.Name, err)
		}
		if err = c.createOrUpdateHTTPRoute(ctx, httpRoute, existing); err != nil {
			return fmt.Errorf("failed to create or update HTTPRoute for backend %s: %w", ref.Name, err)
		}
		delete(existingPerBackendRoutes, name)
	}

	if err = c.deleteOrphanedPerBackendResources(ctx, mcpRoute, existingPerBackendRoutes); err != nil {
		return fmt.Errorf("failed to delete orphaned per-backend resources: %w", err)
	}

	// Reconciles MCPRouteSecurityPolicy and creates/updates its associated envoy gateway resources.
	if err = c.syncMCPRouteSecurityPolicy(ctx, mcpRoute, mainHTTPRouteName); err != nil {
		return fmt.Errorf("failed to sync MCP route security policy: %w", err)
	}

	err = c.syncGateways(ctx, mcpRoute)
	if err != nil {
		return fmt.Errorf("failed to sync gw pods: %w", err)
	}
	return nil
}

// deniedSecretRefs is what revokeDeniedSecretRefs found: the per-backend HTTPRoute names of the backends
// whose Secret reference no ReferenceGrant permits, and their denials joined in spec order (nil when
// nothing is denied).
type deniedSecretRefs struct {
	httpRouteNames map[string]struct{}
	err            error
}

// has reports whether the backend with the given per-backend HTTPRoute name is denied.
func (d deniedSecretRefs) has(httpRouteName string) bool {
	_, ok := d.httpRouteNames[httpRouteName]
	return ok
}

// revokeDeniedSecretRefs converges every backend whose apiKey.secretRef points at a Secret in another
// namespace that no ReferenceGrant allows to the never-granted state: the per-backend HTTPRoute,
// HTTPRouteFilter and credential Secret generated while a grant existed are deleted, so a revoked or
// narrowed grant stops the data plane from injecting the copied credential. Any error other than a
// denial aborts the sync so the next reconcile retries; the denials recorded before it are returned
// with the error, because those backends' objects are already gone and the status must say why.
//
// This runs before the per-backend loop on purpose: that loop stops at its first error, so a backend
// with an unrelated persistent error earlier in the spec must not keep a later backend's revoked
// credential alive.
func (c *MCPRouteController) revokeDeniedSecretRefs(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) (deniedSecretRefs, error) {
	denied := deniedSecretRefs{httpRouteNames: make(map[string]struct{})}
	var denials []error
	omitted := 0
	for i := range mcpRoute.Spec.BackendRefs {
		ref := &mcpRoute.Spec.BackendRefs[i]
		if ref.SecurityPolicy == nil || ref.SecurityPolicy.APIKey == nil {
			continue
		}
		apiKey := ref.SecurityPolicy.APIKey
		if apiKey.SecretRef == nil || ptr.Deref(apiKey.Inline, "") != "" {
			continue // Inline keys never read a Secret, see readAPIKey.
		}
		secretNamespace := resolveAPIKeySecretNamespace(mcpRoute, apiKey.SecretRef)
		err := c.referenceGrantValidator.validateMCPRouteSecretReference(ctx, mcpRoute.Namespace, secretNamespace, string(apiKey.SecretRef.Name))
		if err == nil {
			continue
		}
		var notPermitted *ReferenceNotPermittedError
		if !errors.As(err, &notPermitted) {
			denied.err = joinDenials(denials, omitted)
			return denied, fmt.Errorf("failed to validate the Secret reference of backend %s: %w", ref.Name, err)
		}
		name := mcpPerBackendRefHTTPRouteName(mcpRoute.Name, ref.Name)
		if err = c.deletePerBackendResources(ctx, mcpRoute, name); err != nil {
			denied.err = joinDenials(denials, omitted)
			return denied, fmt.Errorf("failed to delete the resources of backend %s: %w", ref.Name, err)
		}
		denied.httpRouteNames[name] = struct{}{}
		if len(denials) <= maxReportedDenials {
			denials = append(denials, fmt.Errorf("backend %s: %w", ref.Name, notPermitted))
		} else {
			omitted++
		}
	}
	denied.err = joinDenials(denials, omitted)
	return denied, nil
}

// maxReportedDenials bounds the denials listed in the status: the CRD caps a condition message at 32768
// characters, one denial takes up to about 1 KiB with maximum-length names, and a route has up to 256
// backends.
const maxReportedDenials = 10

// joinDenials joins the recorded denials in spec order. The sweep records up to maxReportedDenials+1 of
// them and counts the rest as omitted; when any was omitted the status lists the first
// maxReportedDenials and counts the others, so the count line always stands for at least two backends
// and a route with exactly maxReportedDenials+1 denials lists them all.
func joinDenials(denials []error, omitted int) error {
	if omitted > 0 {
		hidden := len(denials) - maxReportedDenials + omitted
		denials = append(denials[:maxReportedDenials:maxReportedDenials],
			fmt.Errorf("%d more backends have a Secret reference that is not permitted", hidden))
	}
	return errors.Join(denials...)
}

func mcpPerBackendRefHTTPRouteName(mcpRouteName string, backendName gwapiv1.ObjectName) string {
	return fmt.Sprintf("%s%s-%s", internalapi.MCPPerBackendRefHTTPRoutePrefix, mcpRouteName, backendName)
}

func (c *MCPRouteController) createOrUpdateHTTPRoute(ctx context.Context, httpRoute *gwapiv1.HTTPRoute, update bool) error {
	if update {
		c.logger.Info("Updating HTTPRoute", "namespace", httpRoute.Namespace, "name", httpRoute.Name)
		if err := c.client.Update(ctx, httpRoute); err != nil {
			return fmt.Errorf("failed to update HTTPRoute: %w", err)
		}
	} else {
		c.logger.Info("Creating HTTPRoute", "namespace", httpRoute.Namespace, "name", httpRoute.Name)
		if err := c.client.Create(ctx, httpRoute); err != nil {
			return fmt.Errorf("failed to create HTTPRoute: %w", err)
		}
	}
	return nil
}

func (c *MCPRouteController) getOrNewHTTPRouteRoute(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, routeName string) (*gwapiv1.HTTPRoute, bool, error) {
	httpRoute := &gwapiv1.HTTPRoute{}
	err := c.client.Get(ctx, client.ObjectKey{Name: routeName, Namespace: mcpRoute.Namespace}, httpRoute)
	existing := err == nil
	if apierrors.IsNotFound(err) {
		// This means that this MCPRoute is a new one.
		httpRoute, err = c.newHTTPRoute(mcpRoute, routeName)
		if err != nil {
			return nil, false, err
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("failed to get HTTPRoute: %w", err)
	}
	return httpRoute, existing, nil
}

func (c *MCPRouteController) newHTTPRoute(mcpRoute *aigv1b1.MCPRoute, routeName string) (*gwapiv1.HTTPRoute, error) {
	httpRoute := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        routeName,
			Namespace:   mcpRoute.Namespace,
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		},
		Spec: gwapiv1.HTTPRouteSpec{},
	}
	// Copy labels from MCPRoute to HTTPRoute.
	for k, v := range mcpRoute.Labels {
		httpRoute.Labels[k] = v
	}
	// Copy non-controller annotations from MCPRoute to HTTPRoute.
	for k, v := range mcpRoute.Annotations {
		httpRoute.Annotations[k] = v
	}
	if err := ctrlutil.SetControllerReference(mcpRoute, httpRoute, c.client.Scheme()); err != nil {
		return nil, fmt.Errorf("failed to set controller reference for HTTPRoute: %w", err)
	}
	return httpRoute, nil
}

// listExistingPerBackendHTTPRoutes returns a map of name -> HTTPRoute for all per-backend
// HTTPRoutes currently owned by this MCPRoute.
func (c *MCPRouteController) listExistingPerBackendHTTPRoutes(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) (map[string]*gwapiv1.HTTPRoute, error) {
	var ownedRoutes gwapiv1.HTTPRouteList
	if err := c.client.List(ctx, &ownedRoutes,
		client.InNamespace(mcpRoute.Namespace),
		client.MatchingFields{k8sClientIndexMCPRouteToOwnedHTTPRoute: namespacedNameIndexKey(mcpRoute.Name, mcpRoute.Namespace)},
	); err != nil {
		return nil, fmt.Errorf("failed to list owned HTTPRoutes: %w", err)
	}
	existing := make(map[string]*gwapiv1.HTTPRoute, len(ownedRoutes.Items))
	for i := range ownedRoutes.Items {
		r := &ownedRoutes.Items[i]
		if strings.HasPrefix(r.Name, internalapi.MCPPerBackendRefHTTPRoutePrefix) {
			existing[r.Name] = r
		}
	}
	return existing, nil
}

// deleteOrphanedPerBackendResources deletes per-backend HTTPRoutes and their corresponding
// HTTPRouteFilters and credential Secrets that are no longer referenced by any backendRef in the MCPRoute spec.
func (c *MCPRouteController) deleteOrphanedPerBackendResources(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, orphaned map[string]*gwapiv1.HTTPRoute) error {
	for name := range orphaned {
		if err := c.deletePerBackendResources(ctx, mcpRoute, name); err != nil {
			return err
		}
	}
	return nil
}

// deletePerBackendResources deletes the per-backend HTTPRoute with the given name together with the
// HTTPRouteFilter and credential Secret whose names derive from it. An object that does not exist is
// not an error, so the call can be repeated and used for a backend that never had some of them; only
// deletions that happened are logged.
//
// Derived names are not unique across MCPRoutes: route "a" with backend "b-c" and route "a-b" with
// backend "c" share them. Each object is therefore read first and kept when another object controls
// it, and deleted with a UID precondition so that a replacement created in between survives. The
// controller always sets a controller reference on the objects it creates, so an object under these
// names without one is a manual copy or had its reference stripped; it is deleted too, because keeping
// it could keep a revoked credential alive (the write paths update such an object in place).
func (c *MCPRouteController) deletePerBackendResources(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, httpRouteName string) error {
	if err := c.deleteControlledPerBackendObject(ctx, mcpRoute, httpRouteName, &gwapiv1.HTTPRoute{}); err != nil {
		return err
	}
	filterName := strings.Replace(httpRouteName, internalapi.MCPPerBackendRefHTTPRoutePrefix, internalapi.MCPPerBackendHTTPRouteFilterPrefix, 1)
	if err := c.deleteControlledPerBackendObject(ctx, mcpRoute, filterName, &egv1a1.HTTPRouteFilter{}); err != nil {
		return err
	}

	// A credential Secret exists only for secretRef API keys and inline keys with injectionPolicy
	// IfNotPresent. Secrets go through the clientset, as everywhere else in this controller.
	credSecretName := internalapi.MCPPerBackendCredentialSecretPrefix + strings.TrimPrefix(httpRouteName, internalapi.MCPPerBackendRefHTTPRoutePrefix)
	secret, err := c.kube.CoreV1().Secrets(mcpRoute.Namespace).Get(ctx, credSecretName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("failed to get credential secret %s: %w", credSecretName, err)
	case controlledByAnotherOwner(secret, mcpRoute):
		c.logger.Info("Keeping credential secret controlled by another object", "namespace", mcpRoute.Namespace, "name", credSecretName)
		return nil
	}
	err = c.kube.CoreV1().Secrets(mcpRoute.Namespace).Delete(ctx, credSecretName,
		metav1.DeleteOptions{Preconditions: metav1.NewUIDPreconditions(string(secret.UID))})
	if err == nil {
		c.logger.Info("Deleted credential secret", "namespace", mcpRoute.Namespace, "name", credSecretName)
	} else if err = ignoreGoneOrReplaced(err); err != nil {
		return fmt.Errorf("failed to delete credential secret %s: %w", credSecretName, err)
	}
	return nil
}

// deleteControlledPerBackendObject deletes the named object in mcpRoute's namespace unless it does not
// exist or another object controls it. See deletePerBackendResources.
func (c *MCPRouteController) deleteControlledPerBackendObject(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, name string, obj client.Object) error {
	kind := perBackendObjectKind(obj)
	if err := c.client.Get(ctx, client.ObjectKey{Name: name, Namespace: mcpRoute.Namespace}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get %s %s: %w", kind, name, err)
	}
	if controlledByAnotherOwner(obj, mcpRoute) {
		c.logger.Info("Keeping per-backend object controlled by another object", "kind", kind, "namespace", mcpRoute.Namespace, "name", name)
		return nil
	}
	err := c.client.Delete(ctx, obj, client.Preconditions{UID: ptr.To(obj.GetUID())})
	if err == nil {
		c.logger.Info("Deleted per-backend object", "kind", kind, "namespace", mcpRoute.Namespace, "name", name)
	} else if err = ignoreGoneOrReplaced(err); err != nil {
		return fmt.Errorf("failed to delete %s %s: %w", kind, name, err)
	}
	return nil
}

// controlledByAnotherOwner reports whether obj has a controller reference to anything but mcpRoute:
// another kind, an MCPRoute of another API group, or an MCPRoute of another name. See
// controllingMCPRouteName for the identity, which the owned-HTTPRoute index shares.
func controlledByAnotherOwner(obj metav1.Object, mcpRoute *aigv1b1.MCPRoute) bool {
	if metav1.GetControllerOf(obj) == nil {
		return false
	}
	name, ok := controllingMCPRouteName(obj)
	return !ok || name != mcpRoute.Name
}

// perBackendObjectKind names the kind of a per-backend object for logs and errors.
func perBackendObjectKind(obj client.Object) string {
	switch obj.(type) {
	case *gwapiv1.HTTPRoute:
		return "HTTPRoute"
	case *egv1a1.HTTPRouteFilter:
		return "HTTPRouteFilter"
	default:
		return fmt.Sprintf("%T", obj)
	}
}

// ignoreGoneOrReplaced turns the delete errors that mean "nothing of ours left to delete" into nil:
// NotFound, and Conflict from the UID precondition when the object was replaced since it was read (the
// next reconcile judges the replacement).
func ignoreGoneOrReplaced(err error) error {
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// newMainHTTPRoute updates the main HTTPRoute with the MCPRoute.
func (c *MCPRouteController) newMainHTTPRoute(dst *gwapiv1.HTTPRoute, mcpRoute *aigv1b1.MCPRoute, backendName string) error {
	// This routes incoming MCP client requests to the MCP proxy in the ext proc.
	servingPath := ptr.Deref(mcpRoute.Spec.Path, defaultMCPPath)
	rules := []gwapiv1.HTTPRouteRule{{
		Matches: []gwapiv1.HTTPRouteMatch{
			{
				Path: &gwapiv1.HTTPPathMatch{
					Type:  ptr.To(gwapiv1.PathMatchExact),
					Value: ptr.To(servingPath),
				},
				Headers: mcpRoute.Spec.Headers,
			},
		},
		BackendRefs: []gwapiv1.HTTPBackendRef{
			{
				BackendRef: gwapiv1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Group:     ptr.To(gwapiv1.Group("gateway.envoyproxy.io")),
						Kind:      ptr.To(gwapiv1.Kind("Backend")),
						Name:      gwapiv1.ObjectName(backendName),
						Namespace: ptr.To(gwapiv1.Namespace(mcpRoute.Namespace)),
						Port:      ptr.To(gwapiv1.PortNumber(internalapi.MCPProxyPort)),
					},
				},
			},
		},
		Timeouts: &gwapiv1.HTTPRouteTimeouts{
			// TODO: make it configurable via MCPRoute.Spec?
			Request:        ptr.To(gwapiv1.Duration("30m")),
			BackendRequest: ptr.To(gwapiv1.Duration("30m")),
		},

		// Set the MCP route header to indicate which MCPRoute this request is for.
		// The MCP proxy uses this to initialize sessions with the backend MCP servers
		// attached to this MCPRoute.
		Filters: []gwapiv1.HTTPRouteFilter{
			{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{
					Set: []gwapiv1.HTTPHeader{
						{
							Name:  internalapi.MCPRouteHeader,
							Value: mcpRouteHeaderValue(mcpRoute),
						},
					},
				},
			},
		},
	}}

	// Add OAuth metadata endpoints if authentication is configured.
	if mcpRoute.Spec.SecurityPolicy != nil && mcpRoute.Spec.SecurityPolicy.OAuth != nil {
		// OAuth 2.0 Protected Resource Metadata (RFC 9728) - serve in both root and suffix paths because different clients
		// may expect either.
		// TODO: only one MCPRoute targeting the same listener can be configured with OAuth due to the fixed well-known path.
		httpRouteFilterName := oauthProtectedResourceMetadataName(mcpRoute.Name)

		// Suffix path: /.well-known/oauth-protected-resource{pathPrefix} (if pathPrefix exists).
		protectedResourceSuffixPath := fmt.Sprintf("/.well-known/oauth-protected-resource%s", strings.TrimSuffix(servingPath, "/"))
		protectedResourceSuffixRule := gwapiv1.HTTPRouteRule{
			Name: ptr.To(gwapiv1.SectionName("oauth-protected-resource-metadata")),
			Matches: []gwapiv1.HTTPRouteMatch{
				{
					Path: &gwapiv1.HTTPPathMatch{
						Type:  ptr.To(gwapiv1.PathMatchExact),
						Value: ptr.To(protectedResourceSuffixPath),
					},
					Headers: mcpRoute.Spec.Headers,
				},
			},
			Filters: []gwapiv1.HTTPRouteFilter{
				{
					Type: gwapiv1.HTTPRouteFilterExtensionRef,
					ExtensionRef: &gwapiv1.LocalObjectReference{
						Group: gwapiv1.Group("gateway.envoyproxy.io"),
						Kind:  gwapiv1.Kind("HTTPRouteFilter"),
						Name:  gwapiv1.ObjectName(httpRouteFilterName),
					},
				},
			},
		}
		rules = append(rules, protectedResourceSuffixRule)

		// Extract path component for RFC 8414 compliant well-known URI construction
		// RFC 8414: https://datatracker.ietf.org/doc/html/rfc8414#section-3.1
		// Pattern: "/.well-known/oauth-authorization-server" + issuer_path_component.
		authServerMeataFilterName := oauthAuthServerMetadataFilterName(mcpRoute.Name)

		// Suffix path: /.well-known/oauth-authorization-server{pathPrefix} (if pathPrefix exists).
		authServerSuffixPath := fmt.Sprintf("%s%s", oauthWellKnownAuthorizationServerMetadataPath, strings.TrimSuffix(servingPath, "/"))
		authServerSuffixRule := gwapiv1.HTTPRouteRule{
			Name: ptr.To(gwapiv1.SectionName("oauth-authorization-server-metadata")),
			Matches: []gwapiv1.HTTPRouteMatch{
				{
					Path: &gwapiv1.HTTPPathMatch{
						Type:  ptr.To(gwapiv1.PathMatchExact),
						Value: ptr.To(authServerSuffixPath),
					},
					Headers: mcpRoute.Spec.Headers,
				},
			},
			Filters: []gwapiv1.HTTPRouteFilter{
				{
					Type: gwapiv1.HTTPRouteFilterExtensionRef,
					ExtensionRef: &gwapiv1.LocalObjectReference{
						Group: gwapiv1.Group("gateway.envoyproxy.io"),
						Kind:  gwapiv1.Kind("HTTPRouteFilter"),
						Name:  gwapiv1.ObjectName(authServerMeataFilterName),
					},
				},
			},
		}
		// Suffix path: /.well-known/openid-configuration{pathPrefix} (if pathPrefix exists).
		authServerSuffixPathOIDC := fmt.Sprintf("%s%s", oidcWellKnownMetadataPath, servingPath)
		authServerSuffixRuleOIDC := gwapiv1.HTTPRouteRule{
			Name: ptr.To(gwapiv1.SectionName("oauth-authorization-server-metadata-oidc")),
			Matches: []gwapiv1.HTTPRouteMatch{
				{
					Path: &gwapiv1.HTTPPathMatch{
						Type:  ptr.To(gwapiv1.PathMatchExact),
						Value: ptr.To(authServerSuffixPathOIDC),
					},
					Headers: mcpRoute.Spec.Headers,
				},
			},
			Filters: []gwapiv1.HTTPRouteFilter{
				{
					Type: gwapiv1.HTTPRouteFilterExtensionRef,
					ExtensionRef: &gwapiv1.LocalObjectReference{
						Group: gwapiv1.Group("gateway.envoyproxy.io"),
						Kind:  gwapiv1.Kind("HTTPRouteFilter"),
						Name:  gwapiv1.ObjectName(authServerMeataFilterName),
					},
				},
			},
		}
		rules = append(rules, authServerSuffixRule, authServerSuffixRuleOIDC)
	}
	dst.Spec.Rules = rules

	copyMCPRouteMetadataToHTTPRoute(dst, mcpRoute)
	return nil
}

// copyMCPRouteMetadataToHTTPRoute copies the MCPRoute's labels, annotations, parent references, and hostnames
// onto the generated HTTPRoute. It is shared by newMainHTTPRoute and newPerBackendRefHTTPRoute.
func copyMCPRouteMetadataToHTTPRoute(dst *gwapiv1.HTTPRoute, mcpRoute *aigv1b1.MCPRoute) {
	// Initialize labels and annotations maps if they don't exist.
	if dst.Labels == nil {
		dst.Labels = make(map[string]string)
	}
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string)
	}

	// Copy labels from MCPRoute to HTTPRoute.
	for k, v := range mcpRoute.Labels {
		dst.Labels[k] = v
	}

	// Copy non-controller annotations from MCPRoute to HTTPRoute.
	for k, v := range mcpRoute.Annotations {
		dst.Annotations[k] = v
	}

	// Mark this HTTPRoute as generated by the MCP Gateway controller so that
	// this will invoke an extension server update.
	dst.Spec.ParentRefs = mcpRoute.Spec.ParentRefs
	// Carry the MCPRoute's hostnames onto the HTTPRoute so the route lands in the per-host
	// virtual host (mirrors AIGatewayRoute). Without this the MCP route is host-less and
	// stranded in the wildcard vhost, shadowed by host-scoped inference routes -> /mcp 404s.
	dst.Spec.Hostnames = mcpRoute.Spec.Hostnames
}

// newPerBackendRefHTTPRoute creates an HTTPRoute for each backend reference in the MCPRoute.
func (c *MCPRouteController) newPerBackendRefHTTPRoute(ctx context.Context, dst *gwapiv1.HTTPRoute, mcpRoute *aigv1b1.MCPRoute, ref *aigv1b1.MCPRouteBackendRef) error {
	if ns := ref.Namespace; ns != nil && *ns != gwapiv1.Namespace(mcpRoute.Namespace) {
		// TODO: do this in a CEL or webhook validation or start supporting cross-namespace references with ReferenceGrant.
		return fmt.Errorf("cross-namespace backend reference is not supported: backend %s/%s in MCPRoute %s/%s",
			*ns, ref.Name, mcpRoute.Namespace, mcpRoute.Name)
	}
	mcpBackendToHTTPRouteRule, err := c.mcpBackendRefToHTTPRouteRule(ctx, mcpRoute, ref)
	if err != nil {
		return fmt.Errorf("failed to convert MCPRouteRule to HTTPRouteRule: %w", err)
	}
	dst.Spec.Rules = []gwapiv1.HTTPRouteRule{mcpBackendToHTTPRouteRule}

	copyMCPRouteMetadataToHTTPRoute(dst, mcpRoute)
	return nil
}

// syncGateways synchronizes the gateways referenced by the MCPRoute by sending events to the gateway controller.
func (c *MCPRouteController) syncGateways(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	for _, p := range mcpRoute.Spec.ParentRefs {
		gwNamespace := mcpRoute.Namespace
		if p.Namespace != nil {
			gwNamespace = string(*p.Namespace)
		}
		if err := c.syncGateway(ctx, gwNamespace, string(p.Name)); err != nil {
			if mcpRoute.DeletionTimestamp != nil && apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
	}
	return nil
}

// syncGateway is a helper function for syncGateways that sends one GenericEvent to the gateway controller.
func (c *MCPRouteController) syncGateway(ctx context.Context, namespace, name string) error {
	var gw gwapiv1.Gateway
	if err := c.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("Gateway not found", "namespace", namespace, "name", name)
			return fmt.Errorf("gateway %s/%s not found: %w", namespace, name, err)
		}
		c.logger.Error(err, "failed to get Gateway", "namespace", namespace, "name", name)
		return fmt.Errorf("failed to get Gateway %s/%s: %w", namespace, name, err)
	}
	c.logger.Info("Syncing Gateway", "namespace", gw.Namespace, "name", gw.Name)
	c.gatewayEventChan <- event.GenericEvent{Object: &gw}
	return nil
}

// updateMCPRouteStatus updates the status of the MCPRoute.
func (c *MCPRouteController) updateMCPRouteStatus(ctx context.Context, route *aigv1b1.MCPRoute, conditionType string, message string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.client.Get(ctx, client.ObjectKey{Name: route.Name, Namespace: route.Namespace}, route); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		route.Status.Conditions = newConditions(conditionType, message)
		return c.client.Status().Update(ctx, route)
	})
	if err != nil {
		c.logger.Error(err, "failed to update MCPRoute status")
	}
}

// mcpProxySharedBackendName is the fixed name of the shared MCP proxy Backend. One Backend per
// namespace is shared by every MCPRoute in that namespace, across all the Gateways they attach to.
// The extension server rewrites the generated cluster to the in-pod proxy regardless of this
// Backend's endpoint (see modifyMCPGatewayGeneratedCluster), so a single placeholder suffices.
const mcpProxySharedBackendName = internalapi.MCPGeneratedResourceCommonPrefix + "proxy"

// ensureMCPProxyBackend ensures that the shared MCP proxy Backend exists in the given namespace.
// It is referenced by every main HTTPRoute in the namespace and lives in the same namespace as
// those HTTPRoutes, so the reference is valid for any number of parent Gateways (including
// cross-namespace ones). It is intentionally not owned by an MCPRoute or Gateway — a single
// MCPRoute/Gateway is not a correct owner when the Backend is shared; instead it is tagged with the
// managed-by label so cleanupSharedMCPProxyBackend can tell it apart from a user-provided Backend.
//
// Because the Backend has no owner reference it is not watched (the controller builder no longer
// Owns Backends); this call runs on every MCPRoute reconcile, so an out-of-band deletion self-heals
// on the next reconcile of any MCPRoute in the namespace. Returns the Backend's name.
func (c *MCPRouteController) ensureMCPProxyBackend(ctx context.Context, namespace string) (string, error) {
	name := mcpProxySharedBackendName
	var backend egv1a1.Backend
	err := c.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &backend)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get MCP proxy Backend: %w", err)
	}

	if apierrors.IsNotFound(err) {
		// Backend doesn't exist, create it.
		backend = egv1a1.Backend{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{managedByLabel: managedByValue},
			},
			Spec: egv1a1.BackendSpec{
				Endpoints: []egv1a1.BackendEndpoint{
					{
						IP: &egv1a1.IPEndpoint{
							Address: mcpProxyBackendDummyIP,
							Port:    int32(internalapi.MCPProxyPort),
						},
					},
				},
			},
		}
		c.logger.Info("Creating shared MCP proxy Backend", "namespace", namespace, "name", name)
		if err = c.client.Create(ctx, &backend); err != nil && !apierrors.IsAlreadyExists(err) {
			// AlreadyExists: a concurrent MCPRoute reconcile in the same namespace created it; fine.
			return "", fmt.Errorf("failed to create MCP proxy Backend: %w", err)
		}
	}

	return name, nil
}

// onMCPRouteDeleted runs from the finalizer when an MCPRoute is being deleted: it propagates the
// deletion to the referenced Gateways and, if this was the last live MCPRoute in the namespace,
// removes the shared MCP proxy Backend.
func (c *MCPRouteController) onMCPRouteDeleted(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	// Run the shared-Backend cleanup even if propagating to Gateways fails. handleFinalizer removes
	// the finalizer regardless of the returned error, so this is the only chance to avoid leaking
	// the Backend; skipping it on a transient syncGateways error would orphan it for good.
	syncErr := c.syncGateways(ctx, mcpRoute)
	c.cleanupSharedMCPProxyBackend(ctx, mcpRoute)
	return syncErr
}

// cleanupSharedMCPProxyBackend deletes the shared MCP proxy Backend once the MCPRoute being
// deleted is the last live one in its namespace. Best-effort: if a concurrent delete leaves the
// Backend behind it is harmless — it is a single placeholder and the next MCPRoute in the
// namespace reuses it via ensureMCPProxyBackend.
func (c *MCPRouteController) cleanupSharedMCPProxyBackend(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) {
	var routes aigv1b1.MCPRouteList
	if err := c.client.List(ctx, &routes, client.InNamespace(mcpRoute.Namespace)); err != nil {
		c.logger.Error(err, "failed to list MCPRoutes for shared Backend cleanup", "namespace", mcpRoute.Namespace)
		return
	}
	for i := range routes.Items {
		r := &routes.Items[i]
		// Keep the Backend if any other MCPRoute in the namespace is still live (not being deleted).
		// The list is namespace-scoped, so name uniquely identifies the route being deleted.
		if r.Name != mcpRoute.Name && r.GetDeletionTimestamp().IsZero() {
			return
		}
	}
	// Fetch first and only delete a Backend we created: a user may own a Backend that happens to
	// share the fixed name, and it lacks our managed-by label. Deleting it would not be ours to do.
	var backend egv1a1.Backend
	if err := c.client.Get(ctx, client.ObjectKey{Name: mcpProxySharedBackendName, Namespace: mcpRoute.Namespace}, &backend); err != nil {
		if !apierrors.IsNotFound(err) {
			c.logger.Error(err, "failed to get shared MCP proxy Backend for cleanup", "name", mcpProxySharedBackendName)
		}
		return
	}
	if backend.Labels[managedByLabel] != managedByValue {
		return // Not created by us — leave it alone.
	}
	// The owned main HTTPRoute that references this Backend is garbage-collected asynchronously after
	// the MCPRoute is gone, so there is a brief window where it dangles; harmless, as the whole route
	// is being torn down.
	c.logger.Info("Deleting shared MCP proxy Backend (last MCPRoute in namespace removed)",
		"namespace", mcpRoute.Namespace, "name", mcpProxySharedBackendName)
	if err := c.client.Delete(ctx, &backend); err != nil && !apierrors.IsNotFound(err) {
		c.logger.Error(err, "failed to delete shared MCP proxy Backend", "name", mcpProxySharedBackendName)
	}
}

// deleteOldMCPRouteBackend deletes the legacy per-MCPRoute Backend if it still exists and is
// owned by this MCPRoute. This is a one-time migration cleanup from the old naming scheme
// ({ns}-{routeName}-mcp-proxy) to the shared per-namespace Backend (ai-eg-mcp-proxy).
func (c *MCPRouteController) deleteOldMCPRouteBackend(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) {
	oldName := fmt.Sprintf("%s-%s-mcp-proxy", mcpRoute.Namespace, mcpRoute.Name)
	if oldName == mcpProxySharedBackendName {
		return // Degenerate name collision (e.g. ns "ai"/route "eg"); never touch the shared Backend here.
	}
	var backend egv1a1.Backend
	if err := c.client.Get(ctx, client.ObjectKey{Name: oldName, Namespace: mcpRoute.Namespace}, &backend); err != nil {
		return // Already gone or unreadable — nothing to do.
	}
	// Only delete if this MCPRoute is the controller owner; skip the shared (owner-less) Backend.
	owner := metav1.GetControllerOf(&backend)
	if owner == nil || owner.UID != mcpRoute.UID {
		return
	}
	c.logger.Info("Deleting legacy per-MCPRoute mcp-proxy Backend", "namespace", mcpRoute.Namespace, "name", oldName)
	if err := c.client.Delete(ctx, &backend); err != nil && !apierrors.IsNotFound(err) {
		c.logger.Error(err, "failed to delete legacy mcp-proxy Backend", "name", oldName)
	}
}

func mcpBackendRefFilterName(mcpRoute *aigv1b1.MCPRoute, backendName gwapiv1.ObjectName) string {
	return fmt.Sprintf("%s%s-%s", internalapi.MCPPerBackendHTTPRouteFilterPrefix, mcpRoute.Name, backendName)
}

func mcpCredentialSecretName(mcpRoute *aigv1b1.MCPRoute, backendName gwapiv1.ObjectName) string {
	return fmt.Sprintf("%s%s-%s", internalapi.MCPPerBackendCredentialSecretPrefix, mcpRoute.Name, backendName)
}

// mcpBackendRefToHTTPRouteRule creates a HTTPRouteRule for the given MCPRouteBackendRef.
// The rule routes requests to the specified backend using internalapi.MCPBackendHeader,
// which is set by the MCP proxy based on its routing logic.
// This route rule will eventually be moved to the backend listener in the extension server.
func (c *MCPRouteController) mcpBackendRefToHTTPRouteRule(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, ref *aigv1b1.MCPRouteBackendRef) (gwapiv1.HTTPRouteRule, error) {
	egFilterName := mcpBackendRefFilterName(mcpRoute, ref.Name)

	// Determine credential handling from the backend's security policy.
	// - inline API keys with injectionPolicy Always (default): use RequestHeaderModifier (no security
	//   benefit from a Secret since the plaintext is already in the MCPRoute CRD manifest).
	// - secretRef API keys, and inline keys with injectionPolicy IfNotPresent: use credentialInjection
	//   via a managed Secret. RequestHeaderModifier Set always replaces, so IfNotPresent needs
	//   HTTPCredentialInjectionFilter.overwrite=false to inject only when the header is absent.
	// - query param API keys: embed in the URL rewrite path (There is no route filter support for query params).
	var credentialSecretName string
	var credentialHeader *string
	credentialOverwrite := true
	var inlineHeaderFilter *gwapiv1.HTTPRouteFilter
	fullPathPtr := ptr.Deref(ref.Path, defaultMCPPath)

	if ref.SecurityPolicy != nil && ref.SecurityPolicy.APIKey != nil {
		apiKey := ref.SecurityPolicy.APIKey
		overwrite := ptr.Deref(apiKey.InjectionPolicy, aigv1b1.MCPBackendAPIKeyInjectionAlways) != aigv1b1.MCPBackendAPIKeyInjectionIfNotPresent

		switch {
		case apiKey.QueryParam != nil:
			// Query parameter injection cannot use Envoy Gateway's credentialInjection filter;
			// embed directly in the URL rewrite path. injectionPolicy does not apply here.
			// TODO: evaluate alternatives to avoid embedding the secret in the HTTPRoute manifest.
			apiKeyLiteral, err := c.readAPIKey(ctx, mcpRoute, apiKey)
			if err != nil {
				return gwapiv1.HTTPRouteRule{}, fmt.Errorf("failed to read API key for backend %s: %w", ref.Name, err)
			}
			fullPathPtr = fmt.Sprintf("%s?%s=%s", fullPathPtr, *apiKey.QueryParam, apiKeyLiteral)
		case apiKey.Inline != nil && overwrite:
			// Inline API key: inject via RequestHeaderModifier directly. The value is already
			// visible in the MCPRoute manifest, so a separate Secret adds no security benefit.
			header := ptr.Deref(apiKey.Header, "Authorization")
			value := *apiKey.Inline
			if header == "Authorization" {
				value = "Bearer " + value
			}
			inlineHeaderFilter = &gwapiv1.HTTPRouteFilter{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{
					Set: []gwapiv1.HTTPHeader{
						{Name: gwapiv1.HTTPHeaderName(header), Value: value},
					},
				},
			}
		case apiKey.SecretRef != nil || apiKey.Inline != nil:
			// Header injection that must honor injectionPolicy: create a managed credential Secret
			// and use HTTPRouteFilter credentialInjection. Inline IfNotPresent shares this
			// path because RequestHeaderModifier Set always replaces the header.
			credSecretName := mcpCredentialSecretName(mcpRoute, ref.Name)
			if err := c.ensureCredentialSecret(ctx, credSecretName, mcpRoute, apiKey); err != nil {
				return gwapiv1.HTTPRouteRule{}, fmt.Errorf("failed to ensure credential secret for backend %s: %w", ref.Name, err)
			}
			credentialSecretName = credSecretName
			credentialHeader = apiKey.Header
			credentialOverwrite = overwrite
		}
	}

	// Ensure the HTTPRouteFilter for this backend with URL rewrite and optional credential injection.
	if err := c.ensureMCPBackendRefHTTPFilter(ctx, egFilterName, mcpRoute, credentialSecretName, credentialHeader, credentialOverwrite); err != nil {
		return gwapiv1.HTTPRouteRule{}, fmt.Errorf("failed to ensure MCP backend HTTP filter: %w", err)
	}

	filters := []gwapiv1.HTTPRouteFilter{
		{
			Type: gwapiv1.HTTPRouteFilterExtensionRef,
			ExtensionRef: &gwapiv1.LocalObjectReference{
				Group: "gateway.envoyproxy.io",
				Kind:  "HTTPRouteFilter",
				Name:  gwapiv1.ObjectName(egFilterName),
			},
		},
	}
	if inlineHeaderFilter != nil {
		filters = append(filters, *inlineHeaderFilter)
	}
	filters = append(filters, gwapiv1.HTTPRouteFilter{
		Type: gwapiv1.HTTPRouteFilterURLRewrite,
		URLRewrite: &gwapiv1.HTTPURLRewriteFilter{
			Path: &gwapiv1.HTTPPathModifier{
				Type:            gwapiv1.FullPathHTTPPathModifier,
				ReplaceFullPath: ptr.To(fullPathPtr),
			},
		},
	})

	return gwapiv1.HTTPRouteRule{
		Matches: []gwapiv1.HTTPRouteMatch{
			{
				Path: &gwapiv1.HTTPPathMatch{Type: ptr.To(gwapiv1.PathMatchPathPrefix), Value: ptr.To("/")},
				Headers: []gwapiv1.HTTPHeaderMatch{
					{Name: internalapi.MCPBackendHeader, Value: string(ref.Name)},
					{Name: internalapi.MCPRouteHeader, Value: mcpRouteHeaderValue(mcpRoute)},
				},
			},
		},
		Filters: filters,
		BackendRefs: []gwapiv1.HTTPBackendRef{{
			BackendRef: gwapiv1.BackendRef{
				BackendObjectReference: gwapiv1.BackendObjectReference{
					Group:     ref.Group,
					Kind:      ref.Kind,
					Name:      ref.Name,
					Namespace: ref.Namespace,
					Port:      ref.Port,
				},
			},
		}},
		Timeouts: &gwapiv1.HTTPRouteTimeouts{
			Request:        ptr.To(gwapiv1.Duration("30m")),
			BackendRequest: ptr.To(gwapiv1.Duration("30m")),
		},
	}, nil
}

func mcpRouteHeaderValue(mcpRoute *aigv1b1.MCPRoute) string {
	return fmt.Sprintf("%s/%s", mcpRoute.Namespace, mcpRoute.Name)
}

// ensureMCPBackendRefHTTPFilter ensures that an HTTPRouteFilter exists for the given backend reference in the MCPRoute.
// When credentialSecretName is non-empty, the filter is configured with credential injection referencing
// the given secret (which must store the credential under the InjectedCredentialKey key). overwrite is
// forwarded to HTTPCredentialInjectionFilter so the API key is only injected when the header is
// missing if overwrite is false (injectionPolicy IfNotPresent). When credentialSecretName is empty,
// only URL hostname rewrite is configured.
func (c *MCPRouteController) ensureMCPBackendRefHTTPFilter(ctx context.Context, filterName string, mcpRoute *aigv1b1.MCPRoute,
	credentialSecretName string, credentialHeader *string, overwrite bool,
) error {
	filter := &egv1a1.HTTPRouteFilter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      filterName,
			Namespace: mcpRoute.Namespace,
		},
		Spec: egv1a1.HTTPRouteFilterSpec{
			// Rewrite the hostname to the backend service name.
			// This allows Envoy to route to public MCP services with SNI matching the service name.
			URLRewrite: &egv1a1.HTTPURLRewriteFilter{
				Hostname: &egv1a1.HTTPHostnameModifier{
					Type: egv1a1.BackendHTTPHostnameModifier,
				},
			},
		},
	}

	if credentialSecretName != "" {
		filter.Spec.CredentialInjection = &egv1a1.HTTPCredentialInjectionFilter{
			Overwrite: ptr.To(overwrite),
			Header:    credentialHeader,
			Credential: egv1a1.InjectedCredential{
				ValueRef: gwapiv1.SecretObjectReference{
					Name: gwapiv1.ObjectName(credentialSecretName),
				},
			},
		}
	}

	if err := ctrlutil.SetControllerReference(mcpRoute, filter, c.client.Scheme()); err != nil {
		return fmt.Errorf("failed to set controller reference for HTTPRouteFilter: %w", err)
	}
	// Create or Update the HTTPRouteFilter.
	var existingFilter egv1a1.HTTPRouteFilter
	err := c.client.Get(ctx, client.ObjectKey{Name: filterName, Namespace: mcpRoute.Namespace}, &existingFilter)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get HTTPRouteFilter: %w", err)
	}

	if apierrors.IsNotFound(err) {
		c.logger.Info("Creating HTTPRouteFilter", "namespace", filter.Namespace, "name", filter.Name)
		if err = c.client.Create(ctx, filter); err != nil {
			return fmt.Errorf("failed to create HTTPRouteFilter: %w", err)
		}
	} else {
		// Same collision as for the credential Secret: the filter of another MCPRoute's backend must not be
		// rewritten, and its credential Secret must not be deleted as "stale".
		if controlledByAnotherOwner(&existingFilter, mcpRoute) {
			return fmt.Errorf("HTTPRouteFilter %s is controlled by another MCPRoute whose backend derives the same object names; rename the backend", filterName)
		}
		previousCredentialSecretName := ""
		if existingFilter.Spec.CredentialInjection != nil {
			previousCredentialSecretName = string(existingFilter.Spec.CredentialInjection.Credential.ValueRef.Name)
		}
		// Delete only on credential-injection transitions to avoid per-reconcile Delete calls.
		if previousCredentialSecretName != "" && previousCredentialSecretName != credentialSecretName {
			deleteErr := c.kube.CoreV1().Secrets(mcpRoute.Namespace).Delete(ctx, previousCredentialSecretName, metav1.DeleteOptions{})
			if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return fmt.Errorf("failed to delete stale credential secret %s: %w", previousCredentialSecretName, deleteErr)
			}
		}

		// Update existing filter unconditionally to ensure it matches the desired state.
		existingFilter.Spec = filter.Spec
		c.logger.Info("Updating HTTPRouteFilter", "namespace", existingFilter.Namespace, "name", existingFilter.Name)
		if err = c.client.Update(ctx, &existingFilter); err != nil {
			return fmt.Errorf("failed to update HTTPRouteFilter: %w", err)
		}
	}
	return nil
}

// ensureCredentialSecret creates or updates a Kubernetes Secret that holds the formatted credential
// value under the InjectedCredentialKey key. This secret is referenced by the HTTPRouteFilter's credentialInjection,
// keeping the plaintext API key out of the HTTPRoute manifest.
func (c *MCPRouteController) ensureCredentialSecret(ctx context.Context, secretName string, mcpRoute *aigv1b1.MCPRoute, apiKey *aigv1b1.MCPBackendAPIKey) error {
	apiKeyLiteral, err := c.readAPIKey(ctx, mcpRoute, apiKey)
	if err != nil {
		return fmt.Errorf("failed to read API key: %w", err)
	}

	// Format the credential value. The credentialInjection filter injects
	// this verbatim into the target header.
	credentialValue := apiKeyLiteral
	header := ptr.Deref(apiKey.Header, "Authorization")
	if header == "Authorization" {
		credentialValue = "Bearer " + apiKeyLiteral
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: mcpRoute.Namespace,
		},
		Data: map[string][]byte{
			egv1a1.InjectedCredentialKey: []byte(credentialValue),
		},
	}
	setRefErr := ctrlutil.SetControllerReference(mcpRoute, desired, c.client.Scheme())
	if setRefErr != nil {
		return fmt.Errorf("failed to set controller reference for credential secret: %w", setRefErr)
	}

	existing, err := c.kube.CoreV1().Secrets(mcpRoute.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get credential secret: %w", err)
		}
		c.logger.Info("Creating credential secret", "namespace", mcpRoute.Namespace, "name", secretName)
		if _, err = c.kube.CoreV1().Secrets(mcpRoute.Namespace).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create credential secret: %w", err)
		}
		return nil
	}

	// Per-backend object names derive from the route and backend names and can collide across MCPRoutes
	// ("a"/"b-c" and "a-b"/"c"); never rewrite another route's credential.
	if controlledByAnotherOwner(existing, mcpRoute) {
		return fmt.Errorf("credential secret %s is controlled by another MCPRoute whose backend derives the same object names; rename the backend", secretName)
	}

	// Update if the credential value changed.
	if string(existing.Data[egv1a1.InjectedCredentialKey]) != credentialValue {
		existing.Data = desired.Data
		c.logger.Info("Updating credential secret", "namespace", mcpRoute.Namespace, "name", secretName)
		if _, err = c.kube.CoreV1().Secrets(mcpRoute.Namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update credential secret: %w", err)
		}
	}
	return nil
}

// readAPIKey returns the API key configured for a backend, reading the referenced Secret when the
// key is not inline. The Secret lives in secretRef.namespace when that is set, otherwise in the
// MCPRoute's namespace; a Secret in another namespace is only read when a ReferenceGrant in that
// namespace allows the MCPRoute to reference it.
func (c *MCPRouteController) readAPIKey(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, apiKey *aigv1b1.MCPBackendAPIKey) (string, error) {
	key := ptr.Deref(apiKey.Inline, "")
	if key == "" {
		secretRef := apiKey.SecretRef
		if secretRef == nil {
			// The CRD requires exactly one of inline and secretRef but admits an empty inline value.
			return "", errors.New("apiKey has neither a non-empty inline value nor a secretRef")
		}
		namespace := resolveAPIKeySecretNamespace(mcpRoute, secretRef)
		// The grant check must run before the Secret is read: the controller can read every Secret
		// in the cluster, so an ungranted reference must never reach the API server.
		if err := c.referenceGrantValidator.validateMCPRouteSecretReference(ctx, mcpRoute.Namespace, namespace, string(secretRef.Name)); err != nil {
			return "", err
		}
		secret, err := c.kube.CoreV1().Secrets(namespace).Get(ctx, string(secretRef.Name), metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to get secret for API key: %w", err)
		}
		if k, ok := secret.Data["apiKey"]; ok {
			key = string(k)
		} else if key, ok = secret.StringData["apiKey"]; !ok {
			return "", fmt.Errorf("secret %s/%s does not contain 'apiKey' key", namespace, secretRef.Name)
		}
	}
	return key, nil
}

// resolveAPIKeySecretNamespace returns the namespace of the Secret an apiKey.secretRef points at:
// secretRef.namespace when set, otherwise the MCPRoute's own namespace.
func resolveAPIKeySecretNamespace(mcpRoute *aigv1b1.MCPRoute, secretRef *gwapiv1.SecretObjectReference) string {
	if secretRef.Namespace != nil && *secretRef.Namespace != "" {
		return string(*secretRef.Namespace)
	}
	return mcpRoute.Namespace
}

// validatePerBackendPrefixMode checks that prefixMode=Never is used correctly, whether it is
// set on a specific backend or inherited from the route-level default: every backend whose
// *effective* PrefixMode is Never must declare toolSelector.include, and the declared tool
// names must be unique across all Never-mode backends on the route. Prompt names are validated
// the same way, but only for backends that opt in by declaring promptSelector.include — a
// Never-mode backend that leaves promptSelector unset simply keeps its prompts prefixed (see
// mcpproxy.mergePromptsList), so no declaration is required for backends without prompts.
//
// The effective-mode resolution here (per-backend override falling back to the route-level
// default, itself defaulting to Always) must stay in sync with effectivePrefixMode in
// internal/mcpproxy/config.go, which performs the same resolution at request time.
//
// The exact-list requirement (as opposed to allowing an unfiltered or regex toolSelector/
// promptSelector for Never mode) is intentional: it is what lets uniqueness of bare names be
// proven here, at admission time, before the route is ever accepted, rather than discovered as
// a runtime ambiguity between live backend catalogs that can change independently of this
// resource. Always-mode backends don't need this restriction because their prefixed names
// ("<backendName>__<tool>") can't collide with each other as long as backend names are unique,
// which is already enforced elsewhere.
func validatePerBackendPrefixMode(mcpRoute *aigv1b1.MCPRoute) error {
	routeMode := aigv1b1.MCPRoutePrefixModeAlways
	if mcpRoute.Spec.PrefixMode != nil {
		routeMode = *mcpRoute.Spec.PrefixMode
	}

	seenTools := make(map[string]string)   // tool name → first backend name that claimed it
	seenPrompts := make(map[string]string) // prompt name → first backend name that claimed it
	for _, ref := range mcpRoute.Spec.BackendRefs {
		effectiveMode := routeMode
		if ref.PrefixMode != nil {
			effectiveMode = *ref.PrefixMode
		}
		if effectiveMode != aigv1b1.MCPRoutePrefixModeNever {
			continue
		}
		backendName := string(ref.Name)
		if ref.ToolSelector == nil || len(ref.ToolSelector.Include) == 0 {
			return fmt.Errorf("prefixMode Never requires backend %q to explicitly enumerate its tools via toolSelector.include so names can be validated for uniqueness", backendName)
		}
		for _, tool := range ref.ToolSelector.Include {
			if first, collision := seenTools[tool]; collision {
				return fmt.Errorf("prefixMode Never requires tool names to be unique across backends, but %q is declared by both %q and %q", tool, first, backendName)
			}
			seenTools[tool] = backendName
		}
		if ref.PromptSelector == nil {
			continue // Opt-in: this backend's prompts stay prefixed under Never mode.
		}
		for _, prompt := range ref.PromptSelector.Include {
			if first, collision := seenPrompts[prompt]; collision {
				return fmt.Errorf("prefixMode Never requires prompt names to be unique across backends, but %q is declared by both %q and %q", prompt, first, backendName)
			}
			seenPrompts[prompt] = backendName
		}
	}
	return nil
}
