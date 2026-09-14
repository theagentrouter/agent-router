// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	oauthWellKnownProtectedResourceMetadataPath   = "/.well-known/oauth-protected-resource"
	oauthWellKnownAuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"
	oidcWellKnownMetadataPath                     = "/.well-known/openid-configuration"

	oauthProtectedResourceMetadataSuffix = "-oauth-protected-resource-metadata"
	oauthAuthServerMetadataSuffix        = "-oauth-authorization-server-metadata"

	httpClientTimeout   = 5 * time.Second
	maxRetryElapsedTime = 10 * time.Second
)

// syncMCPRouteSecurityPolicy reconciles MCPRouteSecurityPolicy and creates/updates its associated envoy gateway resources.
func (c *MCPRouteController) syncMCPRouteSecurityPolicy(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, httpRouteName string) error {
	securityPolicy := mcpRoute.Spec.SecurityPolicy
	hasOAuth := securityPolicy != nil && securityPolicy.OAuth != nil
	hasAPIKeyAuth := securityPolicy != nil && securityPolicy.APIKeyAuth != nil
	hasExtAuth := securityPolicy != nil && securityPolicy.ExtAuth != nil
	securityPolicyConfigured := hasOAuth || hasAPIKeyAuth || hasExtAuth

	if securityPolicyConfigured {
		// Create and manage SecurityPolicy to enforce client authentication on the MCP proxy rule.
		if secErr := c.ensureSecurityPolicy(ctx, mcpRoute, httpRouteName); secErr != nil {
			return fmt.Errorf("failed to ensure SecurityPolicy: %w", secErr)
		}

		// Create OAuth-related resources if OAuth is configured.
		if hasOAuth {
			if err := c.ensureOAuthResources(ctx, mcpRoute, httpRouteName); err != nil {
				return fmt.Errorf("failed to ensure OAuth resources: %w", err)
			}
		} else {
			// Clean up any OAuth-specific resources if OAuth is no longer configured.
			if err := c.cleanupOAuthResources(ctx, mcpRoute); err != nil {
				return fmt.Errorf("failed to cleanup OAuth resources: %w", err)
			}
		}
	} else {
		// Clean up existing SecurityPolicy resources if no authentication is configured.
		if err := c.cleanupSecurityPolicyResources(ctx, mcpRoute); err != nil {
			return fmt.Errorf("failed to cleanup SecurityPolicy resources: %w", err)
		}

		// Clean up any existing OAuth-specific resources if no authentication is configured.
		if err := c.cleanupOAuthResources(ctx, mcpRoute); err != nil {
			return fmt.Errorf("failed to cleanup OAuth resources: %w", err)
		}
	}

	return nil
}

// ensureSecurityPolicy ensures that the SecurityPolicy resource exists with the configured authentication methods.
func (c *MCPRouteController) ensureSecurityPolicy(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, httpRouteName string) error {
	var securityPolicy egv1a1.SecurityPolicy
	securityPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name
	err := c.client.Get(ctx, client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &securityPolicy)
	existingPolicy := err == nil

	if apierrors.IsNotFound(err) {
		// SecurityPolicy doesn't exist, create it.
		securityPolicy = egv1a1.SecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      securityPolicyName,
				Namespace: mcpRoute.Namespace,
			},
		}
		// Set owner reference to mcpRoute for garbage collection.
		if err = ctrlutil.SetControllerReference(mcpRoute, &securityPolicy, c.client.Scheme()); err != nil {
			return fmt.Errorf("failed to set controller reference for SecurityPolicy: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get SecurityPolicy: %w", err)
	}

	securityPolicySpec := egv1a1.SecurityPolicySpec{}

	// Configure JWT authentication to validate access tokens if OAuth is enabled.
	if oauth := mcpRoute.Spec.SecurityPolicy.OAuth; oauth != nil {
		c.logger.Info("Configuring OAuth in SecurityPolicy")

		name := "mcp-jwt-provider"
		jwtProvider := egv1a1.JWTProvider{
			Name:      name,
			Audiences: oauth.Audiences,
		}

		// Configure JWKS source: use explicit config if provided, otherwise auto-discover from authorization server metadata.
		if oauth.JWKS != nil {
			jwtProvider.RemoteJWKS = oauth.JWKS.RemoteJWKS
			jwtProvider.LocalJWKS = oauth.JWKS.LocalJWKS
		} else {
			// Auto-discover JWKS URI from authorization server metadata.
			c.logger.Info("Auto-discovering JWKS URI from authorization server metadata", "issuer", oauth.Issuer)
			jwksURI, discoveryErr := c.discoverJWKSURI(oauth.Issuer)
			if discoveryErr != nil {
				return fmt.Errorf("failed to auto-discover JWKS URI: %w", discoveryErr)
			}
			c.logger.Info("Found JWKS URI from authorization server metadata", "issuer", oauth.Issuer, "jwks_uri", jwksURI)

			jwtProvider.RemoteJWKS = &egv1a1.RemoteJWKS{
				URI: jwksURI,
			}

			// If the JWKS URI is an HTTPS backend, try to discover the backend service for Envoy to fetch JWKS.
			if strings.HasPrefix(jwksURI, "https://") {
				c.logger.Info("Auto-discovering backends with TLS config to fetch JWKS URI", "jwks_uri", jwksURI)
				jwtProvider.RemoteJWKS.BackendRefs, err = c.tryGetBackendsForJWKS(ctx, jwksURI)
				if err != nil {
					// log the error but continue, hoping the JWKS can be fetched without explicitly setting the backend cluster.
					c.logger.Error(err, "could not find a backend with TLS config to fetch the remote JWKS", "jwks_uri", jwksURI)
				}
			}
		}

		// Project the verified "sub" claim into a trusted, gateway-set header so the MCP proxy can
		// read the authenticated subject without re-parsing the client-controlled bearer token.
		// Envoy's JWT filter overwrites this header from the verified token, so any client-supplied
		// value is discarded here.
		jwtProvider.ClaimToHeaders = append(jwtProvider.ClaimToHeaders, egv1a1.ClaimToHeader{
			Claim:  "sub",
			Header: internalapi.MCPSubjectHeader,
		})

		// Add ClaimToHeaders to extract JWT claims and set them as HTTP headers.
		// Envoy's JWT filter will extract these claims and add them to the request headers,
		// which can then be forwarded to backend MCP servers.
		jwtProvider.ClaimToHeaders = append(jwtProvider.ClaimToHeaders, oauth.ClaimToHeaders...)

		securityPolicySpec.JWT = &egv1a1.JWT{
			Providers: []egv1a1.JWTProvider{jwtProvider},
		}
	}

	// Configure API Key authentication if enabled.
	if apiKeyAuth := mcpRoute.Spec.SecurityPolicy.APIKeyAuth; apiKeyAuth != nil {
		c.logger.Info("Configuring API Key in SecurityPolicy")
		securityPolicySpec.APIKeyAuth = apiKeyAuth.DeepCopy()
	}
	// Configure External Auth if set up.
	if extAuth := mcpRoute.Spec.SecurityPolicy.ExtAuth; extAuth != nil {
		c.logger.Info("Configuring Ext Auth in SecurityPolicy")
		securityPolicySpec.ExtAuth = extAuth.DeepCopy()
	}

	// Propagate MergeType so the auto-generated SecurityPolicy can merge with (instead of
	// overriding) a parent Gateway/Listener-level SecurityPolicy, e.g. an IP allowlist.
	securityPolicySpec.MergeType = mcpRoute.Spec.SecurityPolicy.MergeType

	// The SecurityPolicy should only apply to the HTTPRoute MCP proxy rule.
	// However, since HTTPRouteRule name is experimental in Gateway API, and some vendors (e.g. GKE Gateway) do not
	// support it yet, we currently do not set the sectionName to avoid compatibility issues.
	// The jwt and API key auth filter will be removed from backend routes in the extension server.
	// TODO: use sectionName to target the MCP proxy rule only when the HTTPRouteRule name is in stable channel.
	securityPolicySpec.TargetRefs = []gwapiv1.LocalPolicyTargetReferenceWithSectionName{
		{
			LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  gwapiv1.ObjectName(httpRouteName),
			},
		},
	}

	securityPolicy.Spec = securityPolicySpec

	if existingPolicy {
		c.logger.Info("Updating SecurityPolicy", "namespace", securityPolicy.Namespace, "name", securityPolicy.Name)
		if err = c.client.Update(ctx, &securityPolicy); err != nil {
			return fmt.Errorf("failed to update SecurityPolicy: %w", err)
		}
	} else {
		c.logger.Info("Creating SecurityPolicy", "namespace", securityPolicy.Namespace, "name", securityPolicy.Name)
		if err = c.client.Create(ctx, &securityPolicy); err != nil {
			return fmt.Errorf("failed to create SecurityPolicy: %w", err)
		}
	}

	return nil
}

// ensureOAuthProtectedResourceMetadataBTP ensures that the BackendTrafficPolicy resource exists with response override for WWW-Authenticate header.
func (c *MCPRouteController) ensureOAuthProtectedResourceMetadataBTP(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, httpRouteName string) error {
	var backendTrafficPolicy egv1a1.BackendTrafficPolicy
	backendTrafficPolicyName := oauthProtectedResourceMetadataName(mcpRoute.Name)
	err := c.client.Get(ctx, client.ObjectKey{Name: backendTrafficPolicyName, Namespace: mcpRoute.Namespace}, &backendTrafficPolicy)
	existingPolicy := err == nil

	if apierrors.IsNotFound(err) {
		// BackendTrafficPolicy doesn't exist, create it.
		backendTrafficPolicy = egv1a1.BackendTrafficPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendTrafficPolicyName,
				Namespace: mcpRoute.Namespace,
			},
		}
		// Set owner reference to mcpRoute for garbage collection.
		if err = ctrlutil.SetControllerReference(mcpRoute, &backendTrafficPolicy, c.client.Scheme()); err != nil {
			return fmt.Errorf("failed to set controller reference for BackendTrafficPolicy: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get BackendTrafficPolicy: %w", err)
	}

	// Build WWW-Authenticate header value based on RFC 9728 and MCP spec.
	auth := mcpRoute.Spec.SecurityPolicy.OAuth
	wwwAuthenticateValue := buildWWWAuthenticateHeaderValue(&auth.ProtectedResourceMetadata)

	// Configure response override for 401 responses.
	backendTrafficPolicy.Spec = egv1a1.BackendTrafficPolicySpec{
		ResponseOverride: []*egv1a1.ResponseOverride{
			{
				Match: egv1a1.CustomResponseMatch{
					StatusCodes: []egv1a1.StatusCodeMatch{
						{
							Type:  ptr.To(egv1a1.StatusCodeValueTypeValue),
							Value: ptr.To(http.StatusUnauthorized),
						},
					},
				},
				Response: &egv1a1.CustomResponse{
					StatusCode: ptr.To(http.StatusUnauthorized),
					Header: &gwapiv1.HTTPHeaderFilter{
						Set: []gwapiv1.HTTPHeader{
							{
								Name:  "WWW-Authenticate",
								Value: wwwAuthenticateValue,
							},
						},
					},
				},
			},
		},
	}

	ensureCORSHeaders(backendTrafficPolicy.Spec.ResponseOverride[0].Response.Header)

	// Propagate MergeType so the auto-generated OAuth BackendTrafficPolicy can merge with
	// (instead of overriding) an operator-defined BackendTrafficPolicy targeting the same
	// route, e.g. one configuring rate limiting.
	if btp := mcpRoute.Spec.BackendTrafficPolicy; btp != nil {
		backendTrafficPolicy.Spec.MergeType = btp.MergeType
	}

	// Target the HTTPRoute MCP proxy rule only.
	backendTrafficPolicy.Spec.TargetRefs = []gwapiv1.LocalPolicyTargetReferenceWithSectionName{
		{
			LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  gwapiv1.ObjectName(httpRouteName),
			},
			// TODO: this filter should be applied to the MCP proxy rule only, enable sectionName when supported in Envoy Gateway.
			// SectionName: ptr.To(gwapiv1a2.SectionName(mcpProxyRuleName)).
		},
	}

	if existingPolicy {
		c.logger.Info("Updating BackendTrafficPolicy", "namespace", backendTrafficPolicy.Namespace, "name", backendTrafficPolicy.Name)
		if err = c.client.Update(ctx, &backendTrafficPolicy); err != nil {
			return fmt.Errorf("failed to update BackendTrafficPolicy: %w", err)
		}
	} else {
		c.logger.Info("Creating BackendTrafficPolicy", "namespace", backendTrafficPolicy.Namespace, "name", backendTrafficPolicy.Name)
		if err = c.client.Create(ctx, &backendTrafficPolicy); err != nil {
			return fmt.Errorf("failed to create BackendTrafficPolicy: %w", err)
		}
	}

	return nil
}

// buildResourceMetadataURL constructs the OAuth protected resource metadata URL using the resource identifier.
// References:
// * https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization#authorization-server-location
// * https://datatracker.ietf.org/doc/html/rfc9728#name-www-authenticate-response
func buildResourceMetadataURL(metadata *aigv1b1.ProtectedResourceMetadata) string {
	resourceURL := strings.TrimSuffix(metadata.Resource, "/")

	var (
		baseURL       string
		prefixLen     int
		pathComponent string
	)
	switch {
	case strings.HasPrefix(resourceURL, "https://"):
		prefixLen = 8
	case strings.HasPrefix(resourceURL, "http://"):
		prefixLen = 7
	default: // should not happen as CEL validation should have caught it.
		prefixLen = 0
	}

	if idx := strings.Index(resourceURL[prefixLen:], "/"); idx != -1 {
		baseURL = resourceURL[:prefixLen+idx]
		pathComponent = resourceURL[prefixLen+idx:]
	} else {
		baseURL = resourceURL
	}

	// Some agents do not expect the path component to be included in the resource_metadata URL, but according to the
	// spec https://mcp.mintlify.app/specification/2025-11-25/basic/authorization#protected-resource-metadata-discovery-requirements
	// they should honor hte value returned here.
	// We can't expose these resource at the root, because there may be multiple MCP routes with different OAuth settings, so we need
	// to rely on clients properly implementing the spec and using this value returned in the header.
	return fmt.Sprintf("%s%s%s", baseURL, oauthWellKnownProtectedResourceMetadataPath, pathComponent)
}

// buildWWWAuthenticateHeaderValue constructs the WWW-Authenticate header value according to RFC 9728.
// References:
// * https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization#protected-resource-metadata-discovery-requirements
// * https://datatracker.ietf.org/doc/html/rfc9728#name-www-authenticate-response
func buildWWWAuthenticateHeaderValue(metadata *aigv1b1.ProtectedResourceMetadata) string {
	resourceMetadataURL := buildResourceMetadataURL(metadata)
	headerValue := `Bearer error="invalid_token", error_description="The access token is missing or invalid"`

	// Add resource_metadata as per RFC 9728 Section 5.1.
	headerValue = fmt.Sprintf(`%s, resource_metadata="%s"`, headerValue, resourceMetadataURL)

	if len(metadata.ScopesSupported) > 0 {
		// Add scope as per RFC 6750 Section 3.
		headerValue = fmt.Sprintf(`%s, scope="%s"`, headerValue, strings.Join(metadata.ScopesSupported, " "))
	}

	return headerValue
}

// ensureOAuthProtectedResourceMetadataHRF ensures that the HTTPRouteFilter resource exists with direct response for OAuth metadata.
func (c *MCPRouteController) ensureOAuthProtectedResourceMetadataHRF(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	if mcpRoute.Spec.SecurityPolicy == nil || mcpRoute.Spec.SecurityPolicy.OAuth == nil {
		return nil
	}

	var httpRouteFilter egv1a1.HTTPRouteFilter
	httpRouteFilterName := oauthProtectedResourceMetadataName(mcpRoute.Name)
	err := c.client.Get(ctx, client.ObjectKey{Name: httpRouteFilterName, Namespace: mcpRoute.Namespace}, &httpRouteFilter)
	existingFilter := err == nil

	if apierrors.IsNotFound(err) {
		// HTTPRouteFilter doesn't exist, create it.
		httpRouteFilter = egv1a1.HTTPRouteFilter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      httpRouteFilterName,
				Namespace: mcpRoute.Namespace,
			},
		}
		// Set owner reference to mcpRoute for garbage collection.
		if err = ctrlutil.SetControllerReference(mcpRoute, &httpRouteFilter, c.client.Scheme()); err != nil {
			return fmt.Errorf("failed to set controller reference for HTTPRouteFilter: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get HTTPRouteFilter: %w", err)
	}

	// Build OAuth protected resource metadata JSON response.
	metadataJSON := buildOAuthProtectedResourceMetadataJSON(mcpRoute.Spec.SecurityPolicy.OAuth)

	// Configure direct response with OAuth metadata.
	httpRouteFilter.Spec = egv1a1.HTTPRouteFilterSpec{
		DirectResponse: &egv1a1.HTTPDirectResponseFilter{
			ContentType: ptr.To("application/json"),
			StatusCode:  ptr.To(http.StatusOK),
			Body: &egv1a1.CustomResponseBody{
				Type:   ptr.To(egv1a1.ResponseValueTypeInline),
				Inline: ptr.To(metadataJSON),
			},
			Header: &gwapiv1.HTTPHeaderFilter{},
		},
	}
	ensureCORSHeaders(httpRouteFilter.Spec.DirectResponse.Header)

	if existingFilter {
		c.logger.Info("Updating HTTPRouteFilter", "namespace", httpRouteFilter.Namespace, "name", httpRouteFilter.Name)
		if err = c.client.Update(ctx, &httpRouteFilter); err != nil {
			return fmt.Errorf("failed to update HTTPRouteFilter: %w", err)
		}
	} else {
		c.logger.Info("Creating HTTPRouteFilter", "namespace", httpRouteFilter.Namespace, "name", httpRouteFilter.Name)
		if err = c.client.Create(ctx, &httpRouteFilter); err != nil {
			return fmt.Errorf("failed to create HTTPRouteFilter: %w", err)
		}
	}

	return nil
}

// ensureCORSHeaders ensures that the HTTPHeaderFilter resource exists with CORS headers.
// This is required by the MCP inspector, which is running in the browser on a different origin.
func ensureCORSHeaders(httpHeaderFilter *gwapiv1.HTTPHeaderFilter) {
	// Add CORS headers.
	httpHeaderFilter.Set = append(httpHeaderFilter.Set, gwapiv1.HTTPHeader{
		Name:  "Access-Control-Allow-Origin",
		Value: "*",
	}, gwapiv1.HTTPHeader{
		Name:  "Access-Control-Allow-Methods",
		Value: "GET",
	}, gwapiv1.HTTPHeader{
		Name:  "Access-Control-Allow-Headers",
		Value: "mcp-protocol-version",
	})
}

// ensureOAuthAuthServerMetadataHRF ensures that the HTTPRouteFilter resource exists with direct response for OAuth authorization server metadata.
func (c *MCPRouteController) ensureOAuthAuthServerMetadataHRF(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	var httpRouteFilter egv1a1.HTTPRouteFilter
	authServerFilterName := oauthAuthServerMetadataFilterName(mcpRoute.Name)
	err := c.client.Get(ctx, client.ObjectKey{Name: authServerFilterName, Namespace: mcpRoute.Namespace}, &httpRouteFilter)
	existingFilter := err == nil

	if apierrors.IsNotFound(err) {
		// HTTPRouteFilter doesn't exist, create it.
		httpRouteFilter = egv1a1.HTTPRouteFilter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authServerFilterName,
				Namespace: mcpRoute.Namespace,
			},
		}
		// Set owner reference to mcpRoute for garbage collection.
		if err = ctrlutil.SetControllerReference(mcpRoute, &httpRouteFilter, c.client.Scheme()); err != nil {
			return fmt.Errorf("failed to set controller reference for HTTPRouteFilter: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get HTTPRouteFilter: %w", err)
	}

	// Build OAuth authorization server metadata JSON response.
	metadataJSON := c.buildOAuthAuthServerMetadataJSON(mcpRoute.Spec.SecurityPolicy.OAuth)

	// Configure direct response with OAuth authorization server metadata.
	httpRouteFilter.Spec = egv1a1.HTTPRouteFilterSpec{
		DirectResponse: &egv1a1.HTTPDirectResponseFilter{
			ContentType: ptr.To("application/json"),
			StatusCode:  ptr.To(http.StatusOK),
			Body: &egv1a1.CustomResponseBody{
				Type:   ptr.To(egv1a1.ResponseValueTypeInline),
				Inline: ptr.To(metadataJSON),
			},
			Header: &gwapiv1.HTTPHeaderFilter{},
		},
	}
	ensureCORSHeaders(httpRouteFilter.Spec.DirectResponse.Header)

	if existingFilter {
		c.logger.Info("Updating AuthServer HTTPRouteFilter", "namespace", httpRouteFilter.Namespace, "name", httpRouteFilter.Name)
		if err = c.client.Update(ctx, &httpRouteFilter); err != nil {
			return fmt.Errorf("failed to update HTTPRouteFilter: %w", err)
		}
	} else {
		c.logger.Info("Creating AuthServer HTTPRouteFilter", "namespace", httpRouteFilter.Namespace, "name", httpRouteFilter.Name)
		if err = c.client.Create(ctx, &httpRouteFilter); err != nil {
			return fmt.Errorf("failed to create HTTPRouteFilter: %w", err)
		}
	}

	return nil
}

// buildOAuthProtectedResourceMetadataJSON constructs the OAuth protected resource metadata JSON response.
// References:
// * https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization#authorization-server-location
// * https://datatracker.ietf.org/doc/html/rfc9728#name-protected-resource-metadata
func buildOAuthProtectedResourceMetadataJSON(auth *aigv1b1.MCPRouteOAuth) string {
	response := map[string]interface{}{
		"resource":                 auth.ProtectedResourceMetadata.Resource,
		"authorization_servers":    []string{auth.Issuer},
		"bearer_methods_supported": []string{"header"},
	}
	if auth.ProtectedResourceMetadata.ResourceName != nil && *auth.ProtectedResourceMetadata.ResourceName != "" {
		response["resource_name"] = auth.ProtectedResourceMetadata.ResourceName
	}
	if len(auth.ProtectedResourceMetadata.ScopesSupported) != 0 {
		response["scopes_supported"] = auth.ProtectedResourceMetadata.ScopesSupported
	}
	if auth.ProtectedResourceMetadata.ResourceName != nil && *auth.ProtectedResourceMetadata.ResourceName != "" {
		response["resource_name"] = auth.ProtectedResourceMetadata.ResourceName
	}
	if len(auth.ProtectedResourceMetadata.ResourceSigningAlgValuesSupported) > 0 {
		response["resource_signing_alg_values_supported"] = auth.ProtectedResourceMetadata.ResourceSigningAlgValuesSupported
	}
	if auth.ProtectedResourceMetadata.ResourceDocumentation != nil && *auth.ProtectedResourceMetadata.ResourceDocumentation != "" {
		response["resource_documentation"] = auth.ProtectedResourceMetadata.ResourceDocumentation
	}
	if auth.ProtectedResourceMetadata.ResourcePolicyURI != nil && *auth.ProtectedResourceMetadata.ResourcePolicyURI != "" {
		response["resource_policy_uri"] = auth.ProtectedResourceMetadata.ResourcePolicyURI
	}

	// Convert to JSON string.
	jsonBytes, _ := json.Marshal(response)
	return string(jsonBytes)
}

// buildOAuthAuthServerMetadataJSON constructs the OAuth authorization server metadata JSON response.
// It first attempts to fetch metadata from the authorization server's well-known endpoint,
// and falls back to hardcoded values if the fetch fails.
// References:
// * https://modelcontextprotocol.io/specification/2025-03-26/basic/authorization#authorization-server-location
// * https://datatracker.ietf.org/doc/html/rfc8414#section-3.2
func (c *MCPRouteController) buildOAuthAuthServerMetadataJSON(oauth *aigv1b1.MCPRouteOAuth) string {
	// For 2025-03-26 compatibility, we return the authorization server metadata.

	// The authorization server's issuer identifier, which is a URL that uses the "https" scheme and has no query or
	// fragment components.  Authorization server metadata is published at a location that is ".well-known" according
	// to RFC 5785 derived from this issuer identifier, as described in Section 3.
	// https://datatracker.ietf.org/doc/html/rfc8414#section-2
	authServer := strings.TrimSuffix(oauth.Issuer, "/")

	// Try to fetch metadata from the well-known endpoint first.
	if authServer != "" {
		fetchedMetadata, err := fetchOAuthAuthServerMetadata(authServer, maxRetryElapsedTime)
		if err == nil && fetchedMetadata != nil {
			// Convert to JSON string and return.
			jsonBytes, _ := json.Marshal(fetchedMetadata)
			return string(jsonBytes)
		}
		// If there was an error fetching metadata, log it.
		if err != nil {
			c.logger.Error(err, "failed to fetch OAuth authorization server metadata from well-known endpoint", "authServer", authServer)
		}
	}

	c.logger.Info("Falling back to hardcoded OAuth authorization server metadata", "authServer", authServer)

	// Fallback to hardcoded values if fetching fails or authServer is empty
	// These fields are currently hardcoded for keycloak compatibility.
	// We don't fail the reconciliation if fetching metadata fails,
	// as the authorization server metadata is just for back-compatibility with the MCP spec 2025-03-26.
	response := map[string]interface{}{
		"issuer":                                authServer,
		"authorization_endpoint":                authServer + "/protocol/openid-connect/auth",
		"token_endpoint":                        authServer + "/protocol/openid-connect/token",
		"jwks_uri":                              authServer + "/protocol/openid-connect/certs",
		"scopes_supported":                      oauth.ProtectedResourceMetadata.ScopesSupported,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
	}

	// Convert to JSON string.
	jsonBytes, _ := json.Marshal(response)
	return string(jsonBytes)
}

// cleanupSecurityPolicyResources deletes existing SecurityPolicy-related resources when SecurityPolicy is nil.
func (c *MCPRouteController) cleanupSecurityPolicyResources(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	// Delete SecurityPolicy.
	securityPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name
	var securityPolicy egv1a1.SecurityPolicy
	err := c.client.Get(ctx, client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &securityPolicy)
	if err == nil {
		c.logger.Info("Deleting SecurityPolicy", "namespace", securityPolicy.Namespace, "name", securityPolicy.Name)
		if err = c.client.Delete(ctx, &securityPolicy); err != nil {
			return fmt.Errorf("failed to delete SecurityPolicy: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get SecurityPolicy for deletion: %w", err)
	}
	return nil
}

func (c *MCPRouteController) ensureOAuthResources(ctx context.Context, mcpRoute *aigv1b1.MCPRoute, httpRouteName string) error {
	// Create BackendTrafficPolicy for WWW-Authenticate header with OAuth resource metadata in 401 responses.
	if btpErr := c.ensureOAuthProtectedResourceMetadataBTP(ctx, mcpRoute, httpRouteName); btpErr != nil {
		return fmt.Errorf("failed to ensure BackendTrafficPolicy: %w", btpErr)
	}

	// Create HTTPRouteFilter for OAuth protected resource metadata endpoint.
	if hrfErr := c.ensureOAuthProtectedResourceMetadataHRF(ctx, mcpRoute); hrfErr != nil {
		return fmt.Errorf("failed to ensure HTTPRouteFilter: %w", hrfErr)
	}

	// Create HTTPRouteFilter for OAuth authorization server metadata endpoint.
	if hrfErr := c.ensureOAuthAuthServerMetadataHRF(ctx, mcpRoute); hrfErr != nil {
		return fmt.Errorf("failed to ensure AuthServer HTTPRouteFilter: %w", hrfErr)
	}
	return nil
}

func (c *MCPRouteController) cleanupOAuthResources(ctx context.Context, mcpRoute *aigv1b1.MCPRoute) error {
	// Delete BackendTrafficPolicy.
	backendTrafficPolicyName := oauthProtectedResourceMetadataName(mcpRoute.Name)
	var backendTrafficPolicy egv1a1.BackendTrafficPolicy
	err := c.client.Get(ctx, client.ObjectKey{Name: backendTrafficPolicyName, Namespace: mcpRoute.Namespace}, &backendTrafficPolicy)
	if err == nil {
		c.logger.Info("Deleting BackendTrafficPolicy", "namespace", backendTrafficPolicy.Namespace, "name", backendTrafficPolicy.Name)
		if err = c.client.Delete(ctx, &backendTrafficPolicy); err != nil {
			return fmt.Errorf("failed to delete BackendTrafficPolicy: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get BackendTrafficPolicy for deletion: %w", err)
	}

	// Delete OAuth protected resource HTTPRouteFilter.
	httpRouteFilterName := oauthProtectedResourceMetadataName(mcpRoute.Name)
	var httpRouteFilter egv1a1.HTTPRouteFilter
	err = c.client.Get(ctx, client.ObjectKey{Name: httpRouteFilterName, Namespace: mcpRoute.Namespace}, &httpRouteFilter)
	if err == nil {
		c.logger.Info("Deleting HTTPRouteFilter", "namespace", httpRouteFilter.Namespace, "name", httpRouteFilter.Name)
		if err = c.client.Delete(ctx, &httpRouteFilter); err != nil {
			return fmt.Errorf("failed to delete HTTPRouteFilter: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get HTTPRouteFilter for deletion: %w", err)
	}

	// Delete OAuth authorization server HTTPRouteFilter.
	authServerFilterName := oauthAuthServerMetadataFilterName(mcpRoute.Name)
	var authServerFilter egv1a1.HTTPRouteFilter
	err = c.client.Get(ctx, client.ObjectKey{Name: authServerFilterName, Namespace: mcpRoute.Namespace}, &authServerFilter)
	if err == nil {
		c.logger.Info("Deleting AuthServer HTTPRouteFilter", "namespace", authServerFilter.Namespace, "name", authServerFilter.Name)
		if err = c.client.Delete(ctx, &authServerFilter); err != nil {
			return fmt.Errorf("failed to delete AuthServer HTTPRouteFilter: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get AuthServer HTTPRouteFilter for deletion: %w", err)
	}

	return nil
}

// OAuthAuthServerMetadata represents the OAuth authorization server metadata response
// from the /.well-known/oauth-authorization-server endpoint as defined in RFC 8414.
// https://datatracker.ietf.org/doc/html/rfc8414#section-2
type OAuthAuthServerMetadata struct {
	Issuer                                             string   `json:"issuer"`
	AuthorizationEndpoint                              string   `json:"authorization_endpoint"`
	TokenEndpoint                                      string   `json:"token_endpoint"`
	JwksURI                                            string   `json:"jwks_uri"`
	RegistrationEndpoint                               string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                                    []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported                             []string `json:"response_types_supported,omitempty"`
	ResponseModesSupported                             []string `json:"response_modes_supported,omitempty"`
	GrantTypesSupported                                []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported                  []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	TokenEndpointAuthSigningAlgValuesSupported         []string `json:"token_endpoint_auth_signing_alg_values_supported,omitempty"`
	ServiceDocumentation                               string   `json:"service_documentation,omitempty"`
	UILocalesSupported                                 []string `json:"ui_locales_supported,omitempty"`
	OPPolicyURI                                        string   `json:"op_policy_uri,omitempty"`
	OPTosURI                                           string   `json:"op_tos_uri,omitempty"`
	RevocationEndpoint                                 string   `json:"revocation_endpoint,omitempty"`
	RevocationEndpointAuthMethodsSupported             []string `json:"revocation_endpoint_auth_methods_supported,omitempty"`
	RevocationEndpointAuthSigningAlgValuesSupported    []string `json:"revocation_endpoint_auth_signing_alg_values_supported,omitempty"`
	IntrospectionEndpoint                              string   `json:"introspection_endpoint,omitempty"`
	IntrospectionEndpointAuthMethodsSupported          []string `json:"introspection_endpoint_auth_methods_supported,omitempty"`
	IntrospectionEndpointAuthSigningAlgValuesSupported []string `json:"introspection_endpoint_auth_signing_alg_values_supported,omitempty"`
	CodeChallengeMethodsSupported                      []string `json:"code_challenge_methods_supported,omitempty"`
}

// httpError represents an HTTP error with status code and status message.
type httpError struct {
	statusCode int
	status     string
}

func (h *httpError) Error() string { return fmt.Sprintf("HTTP %d %s", h.statusCode, h.status) }

// invalidMetadataError represents a well-known endpoint that answered with a document we cannot use.
// Some authorization servers return 200 with an empty JSON object at a well-known path they do not
// actually implement, which must be treated as a miss rather than a successful discovery.
type invalidMetadataError struct {
	url string
	err error
}

func (e *invalidMetadataError) Error() string {
	return fmt.Sprintf("unusable authorization server metadata at %s: %v", e.url, e.err)
}

func (e *invalidMetadataError) Unwrap() error { return e.err }

// fetchOAuthAuthServerMetadata fetches OAuth authorization server metadata from the well-known endpoint
// with exponential backoff retry logic. It returns the fetched metadata or an error if all attempts fail.
func fetchOAuthAuthServerMetadata(authServer string, maxRetryElapsedTime time.Duration) (*OAuthAuthServerMetadata, error) {
	httpClient := &http.Client{Timeout: httpClientTimeout}

	operation := func(wellKnownURL string, metadata *OAuthAuthServerMetadata) error {
		resp, err := httpClient.Get(wellKnownURL)
		if err != nil {
			urlError, dnsError := &url.Error{}, &net.DNSError{}
			if errors.As(err, &urlError) || errors.As(err, &dnsError) {
				// These errors are highly likely configuration issues, don't retry.
				//
				// ***NOTE***: Do not delete this handling, otherwise all the test case that hitting this
				// will keep retrying until timeout, which slows the tests significantly.
				return backoff.Permanent(fmt.Errorf("failed to fetch OAuth authorization server metadata: %w", err))
			}
			return err
		}
		defer resp.Body.Close()

		// Check for successful response.
		if resp.StatusCode != http.StatusOK {
			// Retry on 5xx server errors, but not on 4xx client errors.
			if resp.StatusCode >= 500 && resp.StatusCode < 600 {
				return &httpError{statusCode: resp.StatusCode, status: resp.Status}
			}
			// 4xx errors are permanent, don't retry.
			return backoff.Permanent(&httpError{statusCode: resp.StatusCode, status: resp.Status})
		}

		// Read and parse the response body.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			// I/O errors during reading might be transient.
			return err
		}

		var fetched OAuthAuthServerMetadata
		if err := json.Unmarshal(body, &fetched); err != nil {
			// A body that is not a metadata document at all, such as the HTML a catch-all route
			// serves. Retrying this URL will not change the response, so move to the next variant.
			return backoff.Permanent(&invalidMetadataError{url: wellKnownURL, err: fmt.Errorf("failed to parse JSON: %w", err)})
		}
		// RFC 8414 Section 2 makes issuer REQUIRED, so a document without one is not a metadata
		// document, however well-formed. This is the case for authorization servers that answer 200
		// with an empty JSON object at a well-known path they do not actually implement.
		// Anything beyond this is caller-specific: discoverJWKSURI requires jwks_uri, while the
		// served metadata passes the fetched document through as-is.
		// https://datatracker.ietf.org/doc/html/rfc8414#section-2
		if fetched.Issuer == "" {
			return backoff.Permanent(&invalidMetadataError{url: wellKnownURL, err: errors.New("missing issuer")})
		}
		*metadata = fetched
		return nil
	}

	authServerURL, err := url.Parse(authServer)
	if err != nil {
		return nil, fmt.Errorf("invalid authorization server URL: %w", err)
	}

	// Build the well-known URL according to the spec: https://datatracker.ietf.org/doc/html/rfc8414#section-3
	// Some providers like Descope do not honor the spec and put the well-known endpoint
	// after the issuer path, so we try a set of variants to maximize compatibility.
	// See: https://modelcontextprotocol.io/specification/draft/basic/authorization#authorization-server-metadata-discovery
	wellKnownURLVariants := []string{
		fmt.Sprintf("%s://%s%s%s",
			authServerURL.Scheme,
			authServerURL.Host,
			oauthWellKnownAuthorizationServerMetadataPath,
			strings.TrimSuffix(authServerURL.Path, "/"),
		),
		fmt.Sprintf("%s://%s%s%s",
			authServerURL.Scheme,
			authServerURL.Host,
			oidcWellKnownMetadataPath,
			strings.TrimSuffix(authServerURL.Path, "/"),
		),
		fmt.Sprintf("%s://%s%s%s",
			authServerURL.Scheme,
			authServerURL.Host,
			strings.TrimSuffix(authServerURL.Path, "/"),
			oauthWellKnownAuthorizationServerMetadataPath,
		),
		fmt.Sprintf("%s://%s%s%s",
			authServerURL.Scheme,
			authServerURL.Host,
			strings.TrimSuffix(authServerURL.Path, "/"),
			oidcWellKnownMetadataPath,
		),
	}

	var lastErr error
	for _, wellKnownURL := range wellKnownURLVariants {
		var metadata OAuthAuthServerMetadata
		b := backoff.NewExponentialBackOff()
		b.MaxElapsedTime = maxRetryElapsedTime
		err = backoff.Retry(func() error {
			return operation(wellKnownURL, &metadata)
		}, b)
		if err == nil { // Success.
			return &metadata, nil
		}
		lastErr = err

		var httpErr *httpError
		var invalidErr *invalidMetadataError
		switch {
		case errors.As(err, &httpErr) && httpErr.statusCode >= 400 && httpErr.statusCode < 500:
			// If it is a 4xx error, try the next URL variant instead of retrying or failing
			continue
		case errors.As(err, &invalidErr):
			// The endpoint answered but the document is unusable, try the next URL variant.
			continue
		default:
			// Other errors, return immediately as the backoff time is exhausted.
			return nil, err
		}
	}

	// We can only get here if every variant was a 4xx or returned an unusable document.
	// Return the last failure.
	return nil, fmt.Errorf("no usable authorization server metadata found for %q: %w", authServer, lastErr)
}

func oauthProtectedResourceMetadataName(mcpRouteName string) string {
	return fmt.Sprintf("%s%s%s", internalapi.MCPGeneratedResourceCommonPrefix, mcpRouteName, oauthProtectedResourceMetadataSuffix)
}

func oauthAuthServerMetadataFilterName(mcpRouteName string) string {
	return fmt.Sprintf("%s%s%s", internalapi.MCPGeneratedResourceCommonPrefix, mcpRouteName, oauthAuthServerMetadataSuffix)
}

// discoverJWKSURI attempts to discover the JWKS URI from the OAuth authorization server metadata.
// It fetches the well-known metadata endpoint and extracts the jwks_uri field.
func (c *MCPRouteController) discoverJWKSURI(issuer string) (string, error) {
	// Fetch OAuth authorization server metadata.
	metadata, err := fetchOAuthAuthServerMetadata(issuer, maxRetryElapsedTime)
	switch {
	case err != nil:
		return "", fmt.Errorf("failed to fetch authorization server metadata: %w", err)
	case metadata.JwksURI == "":
		return "", fmt.Errorf("jwks_uri not found in authorization server metadata")
	default:
		return metadata.JwksURI, nil
	}
}

// tryGetBackendsForJWKS attempts to find a BackendCluster that can reach the given JWKS URL.
func (c *MCPRouteController) tryGetBackendsForJWKS(ctx context.Context, jwksURL string) ([]egv1a1.BackendRef, error) {
	var backendTLSPolicies gwapiv1.BackendTLSPolicyList
	if err := c.client.List(ctx, &backendTLSPolicies); err != nil {
		return nil, fmt.Errorf("failed to list BackendTLSPolicy: %w", err)
	}

	u, err := url.Parse(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWKS URL: %w", err)
	}
	hostname := u.Hostname()

	var backendRefs []egv1a1.BackendRef
	for i := range backendTLSPolicies.Items {
		btp := &backendTLSPolicies.Items[i]
		if string(btp.Spec.Validation.Hostname) == hostname {
			for _, ref := range btp.Spec.TargetRefs {
				backendRefs = append(backendRefs, egv1a1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Group: &ref.Group,
						Kind:  &ref.Kind,
						Name:  ref.Name,
					},
				})
			}
		}
	}

	return backendRefs, nil
}
