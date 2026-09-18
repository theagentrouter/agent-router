// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func setupOAuthTestServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]interface{}{
			"issuer":                 r.URL.Scheme + "://" + r.Host,
			"authorization_endpoint": r.URL.Scheme + "://" + r.Host + "/auth",
			"token_endpoint":         r.URL.Scheme + "://" + r.Host + "/token",
			// Use HTTP to force the backend TLS Policy discovery.
			"jwks_uri": "https://" + r.Host + "/.well-known/jwks.json",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metadata)
	})

	return httptest.NewServer(mux)
}

func TestMCPRouteController_syncMCPRouteSecurityPolicy(t *testing.T) {
	server := setupOAuthTestServer()
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	tests := []struct {
		name           string
		mcpRoute       *aigv1b1.MCPRoute
		extraObjs      []client.Object
		wantSecPol     bool
		wantJWT        bool
		wantAPIKeyAuth *egv1a1.APIKeyAuth
		wantExtAuth    *egv1a1.ExtAuth
		wantBTP        bool
		wantFilter     bool
		wantIssuer     string
		wantJWKS       *egv1a1.RemoteJWKS
		wantMergeType  *egv1a1.MergeType
		wantBTPMerge   *egv1a1.MergeType
		wantErr        bool
	}{
		{
			name: "no authentication configured",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{},
				},
			},
			wantSecPol: false,
			wantJWT:    false,
			wantBTP:    false,
			wantFilter: false,
			wantErr:    false,
		},
		{
			name: "authentication configured",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						OAuth: &aigv1b1.MCPRouteOAuth{
							Issuer:    server.URL,
							Audiences: []string{"test-audience"},
							JWKS: &aigv1b1.JWKS{
								RemoteJWKS: &egv1a1.RemoteJWKS{
									URI: server.URL + "/.well-known/jwks.json",
								},
							},
							ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
								Resource:                          "https://api.example.com/mcp",
								ScopesSupported:                   []string{"read", "write"},
								ResourceName:                      ptr.To("my cool mcp tools"),
								ResourceSigningAlgValuesSupported: []string{"RS256", "ES256"},
								ResourceDocumentation:             ptr.To("https://api.example.com/docs"),
								ResourcePolicyURI:                 ptr.To("https://api.example.com/policy"),
							},
						},
					},
				},
			},
			wantSecPol: true,
			wantJWT:    true,
			wantBTP:    true,
			wantFilter: true,
			wantIssuer: server.URL,
			// For HTTP JWKS we don't need a cluster with TLS config.
			wantJWKS: &egv1a1.RemoteJWKS{URI: server.URL + "/.well-known/jwks.json"},
			wantErr:  false,
		},
		{
			name: "authentication configured without jwks - auto discovery",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						OAuth: &aigv1b1.MCPRouteOAuth{
							Issuer:    server.URL,
							Audiences: []string{"test-audience"},
							ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
								Resource:        "https://api.example.com/mcp",
								ScopesSupported: []string{"read", "write"},
							},
						},
					},
				},
			},
			extraObjs: []client.Object{
				&gwapiv1.BackendTLSPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "non-matching-backend-tls", Namespace: "default"},
					Spec: gwapiv1.BackendTLSPolicySpec{
						Validation: gwapiv1.BackendTLSPolicyValidation{Hostname: gwapiv1.PreciseHostname("example.com")},
						TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{
							{
								LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
									Group: "gateway.envoyproxy.io/v1alpha1",
									Kind:  "Backend",
									Name:  "non-matching-backend",
								},
							},
						},
					},
				},
				&gwapiv1.BackendTLSPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "jwks-backend-tls", Namespace: "default"},
					Spec: gwapiv1.BackendTLSPolicySpec{
						Validation: gwapiv1.BackendTLSPolicyValidation{Hostname: gwapiv1.PreciseHostname(serverURL.Hostname())},
						TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{
							{
								LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
									Group: "gateway.envoyproxy.io/v1alpha1",
									Kind:  "Backend",
									Name:  "jwks-backend",
								},
							},
						},
					},
				},
			},
			wantSecPol: true, // JWKS discovery should work with test server.
			wantJWT:    true,
			wantBTP:    true,
			wantFilter: true,
			wantIssuer: server.URL,
			// For HTTPS JWKS we need a cluster with TLS config.
			wantJWKS: &egv1a1.RemoteJWKS{
				URI: fmt.Sprintf("https://%s/.well-known/jwks.json", serverURL.Host),
				BackendCluster: egv1a1.BackendCluster{
					BackendRefs: []egv1a1.BackendRef{
						{
							BackendObjectReference: gwapiv1.BackendObjectReference{
								Group: ptr.To(gwapiv1.Group("gateway.envoyproxy.io/v1alpha1")),
								Kind:  ptr.To(gwapiv1.Kind("Backend")),
								Name:  "jwks-backend",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "api key authentication configured",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						APIKeyAuth: &egv1a1.APIKeyAuth{
							CredentialRefs: []gwapiv1.SecretObjectReference{
								{Name: "client-keys"},
							},
							ExtractFrom: []*egv1a1.ExtractFrom{
								{Headers: []string{"x-api-key"}},
							},
							ForwardClientIDHeader: ptr.To("x-client-id"),
							Sanitize:              ptr.To(true),
						},
					},
				},
			},
			wantSecPol: true,
			wantJWT:    false,
			wantAPIKeyAuth: &egv1a1.APIKeyAuth{ // expected spec
				CredentialRefs: []gwapiv1.SecretObjectReference{
					{Name: "client-keys"},
				},
				ExtractFrom: []*egv1a1.ExtractFrom{
					{Headers: []string{"x-api-key"}},
				},
				ForwardClientIDHeader: ptr.To("x-client-id"),
				Sanitize:              ptr.To(true),
			},
			wantBTP:    false,
			wantFilter: false,
			wantJWKS:   nil,
			wantErr:    false,
		},
		{
			name: "ext auth configured",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						ExtAuth: &egv1a1.ExtAuth{
							GRPC: &egv1a1.GRPCExtAuthService{
								BackendCluster: egv1a1.BackendCluster{
									BackendRefs: []egv1a1.BackendRef{
										{
											BackendObjectReference: gwapiv1.BackendObjectReference{
												Name: "grpc-service",
												Port: ptr.To(gwapiv1.PortNumber(1073)),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantSecPol: true,
			wantJWT:    false,
			wantExtAuth: &egv1a1.ExtAuth{
				GRPC: &egv1a1.GRPCExtAuthService{
					BackendCluster: egv1a1.BackendCluster{
						BackendRefs: []egv1a1.BackendRef{
							{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Name: "grpc-service",
									Port: ptr.To(gwapiv1.PortNumber(1073)),
								},
							},
						},
					},
				},
			},
			wantBTP:    false,
			wantFilter: false,
			wantJWKS:   nil,
			wantErr:    false,
		},
		{
			name: "api key authentication and ext auth configured",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						APIKeyAuth: &egv1a1.APIKeyAuth{
							CredentialRefs: []gwapiv1.SecretObjectReference{
								{Name: "client-keys"},
							},
							ExtractFrom: []*egv1a1.ExtractFrom{
								{Headers: []string{"x-api-key"}},
							},
							ForwardClientIDHeader: ptr.To("x-client-id"),
							Sanitize:              ptr.To(true),
						},
						ExtAuth: &egv1a1.ExtAuth{
							GRPC: &egv1a1.GRPCExtAuthService{
								BackendCluster: egv1a1.BackendCluster{
									BackendRefs: []egv1a1.BackendRef{
										{
											BackendObjectReference: gwapiv1.BackendObjectReference{
												Name: "grpc-service",
												Port: ptr.To(gwapiv1.PortNumber(1073)),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantSecPol: true,
			wantJWT:    false,
			wantAPIKeyAuth: &egv1a1.APIKeyAuth{ // expected spec
				CredentialRefs: []gwapiv1.SecretObjectReference{
					{Name: "client-keys"},
				},
				ExtractFrom: []*egv1a1.ExtractFrom{
					{Headers: []string{"x-api-key"}},
				},
				ForwardClientIDHeader: ptr.To("x-client-id"),
				Sanitize:              ptr.To(true),
			},
			wantExtAuth: &egv1a1.ExtAuth{
				GRPC: &egv1a1.GRPCExtAuthService{
					BackendCluster: egv1a1.BackendCluster{
						BackendRefs: []egv1a1.BackendRef{
							{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Name: "grpc-service",
									Port: ptr.To(gwapiv1.PortNumber(1073)),
								},
							},
						},
					},
				},
			},
			wantBTP:    false,
			wantFilter: false,
			wantJWKS:   nil,
			wantErr:    false,
		},
		{
			name: "oauth configured with mergeType on SecurityPolicy and BackendTrafficPolicy",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						MergeType: ptr.To(egv1a1.StrategicMerge),
						OAuth: &aigv1b1.MCPRouteOAuth{
							Issuer:    server.URL,
							Audiences: []string{"test-audience"},
							JWKS: &aigv1b1.JWKS{
								RemoteJWKS: &egv1a1.RemoteJWKS{
									URI: server.URL + "/.well-known/jwks.json",
								},
							},
							ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
								Resource:        "https://api.example.com/mcp",
								ScopesSupported: []string{"read", "write"},
							},
						},
					},
					BackendTrafficPolicy: &aigv1b1.MCPRouteBackendTrafficPolicy{
						MergeType: ptr.To(egv1a1.StrategicMerge),
					},
				},
			},
			wantSecPol:    true,
			wantJWT:       true,
			wantBTP:       true,
			wantFilter:    true,
			wantIssuer:    server.URL,
			wantJWKS:      &egv1a1.RemoteJWKS{URI: server.URL + "/.well-known/jwks.json"},
			wantMergeType: ptr.To(egv1a1.StrategicMerge),
			wantBTPMerge:  ptr.To(egv1a1.StrategicMerge),
			wantErr:       false,
		},
		{
			name: "api key authentication configured with mergeType on SecurityPolicy only",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
						MergeType: ptr.To(egv1a1.StrategicMerge),
						APIKeyAuth: &egv1a1.APIKeyAuth{
							CredentialRefs: []gwapiv1.SecretObjectReference{
								{Name: "client-keys"},
							},
							ExtractFrom: []*egv1a1.ExtractFrom{
								{Headers: []string{"x-api-key"}},
							},
						},
					},
				},
			},
			wantSecPol: true,
			wantJWT:    false,
			wantAPIKeyAuth: &egv1a1.APIKeyAuth{
				CredentialRefs: []gwapiv1.SecretObjectReference{
					{Name: "client-keys"},
				},
				ExtractFrom: []*egv1a1.ExtractFrom{
					{Headers: []string{"x-api-key"}},
				},
			},
			wantBTP:       false,
			wantFilter:    false,
			wantMergeType: ptr.To(egv1a1.StrategicMerge),
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := requireNewFakeClientWithIndexesForMCP(t)
			eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
			c := NewMCPRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch)

			err := fakeClient.Create(t.Context(), tt.mcpRoute)
			require.NoError(t, err)
			for _, obj := range tt.extraObjs {
				err = fakeClient.Create(t.Context(), obj)
				require.NoError(t, err)
			}

			httpRouteName := "test-http-route"
			err = c.syncMCPRouteSecurityPolicy(t.Context(), tt.mcpRoute, httpRouteName)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			securityPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + tt.mcpRoute.Name
			var securityPolicy egv1a1.SecurityPolicy
			secPolErr := fakeClient.Get(t.Context(), client.ObjectKey{Name: securityPolicyName, Namespace: tt.mcpRoute.Namespace}, &securityPolicy)

			if tt.wantSecPol {
				require.NoError(t, secPolErr, "SecurityPolicy should exist")
				if tt.wantJWT {
					require.NotNil(t, securityPolicy.Spec.JWT)
					require.NotEmpty(t, securityPolicy.Spec.JWT.Providers)
					require.Equal(t, tt.wantIssuer, securityPolicy.Spec.JWT.Providers[0].Issuer)
					if tt.wantJWKS != nil {
						require.Equal(t, tt.wantJWKS, securityPolicy.Spec.JWT.Providers[0].RemoteJWKS)
					}
				} else {
					require.Nil(t, securityPolicy.Spec.JWT)
				}

				if tt.wantAPIKeyAuth != nil {
					require.NotNil(t, securityPolicy.Spec.APIKeyAuth)
					require.Equal(t, tt.wantAPIKeyAuth, securityPolicy.Spec.APIKeyAuth)
				} else {
					require.Nil(t, securityPolicy.Spec.APIKeyAuth)
				}

				if tt.wantExtAuth != nil {
					require.NotNil(t, securityPolicy.Spec.ExtAuth)
					require.Equal(t, tt.wantExtAuth, securityPolicy.Spec.ExtAuth)
				} else {
					require.Nil(t, securityPolicy.Spec.ExtAuth)
				}

				// The SecurityPolicy should only apply to the HTTPRoute MCP proxy rule.
				// However, since HTTPRouteRule name is experimental in Gateway API, and some vendors (e.g. GKE Gateway) do not
				// support it yet, we currently do not set the sectionName to avoid compatibility issues.
				// The authn filters will be removed from backend routes in the extension server.
				// TODO: use sectionName to target the MCP proxy rule only when the HTTPRouteRule name is in stable channel.
				require.Nil(t, securityPolicy.Spec.TargetRefs[0].SectionName)

				require.Equal(t, tt.wantMergeType, securityPolicy.Spec.MergeType)

			} else {
				require.Error(t, secPolErr, "SecurityPolicy should not exist")
			}

			backendTrafficPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + tt.mcpRoute.Name + oauthProtectedResourceMetadataSuffix
			var backendTrafficPolicy egv1a1.BackendTrafficPolicy
			btpErr := fakeClient.Get(t.Context(), client.ObjectKey{Name: backendTrafficPolicyName, Namespace: tt.mcpRoute.Namespace}, &backendTrafficPolicy)

			if tt.wantBTP {
				require.NoError(t, btpErr, "BackendTrafficPolicy should exist")
				require.Equal(t, tt.wantBTPMerge, backendTrafficPolicy.Spec.MergeType)
			} else {
				require.Error(t, btpErr, "BackendTrafficPolicy should not exist")
			}

			httpRouteFilterName := internalapi.MCPGeneratedResourceCommonPrefix + tt.mcpRoute.Name + oauthProtectedResourceMetadataSuffix
			var httpRouteFilter egv1a1.HTTPRouteFilter
			filterErr := fakeClient.Get(t.Context(), client.ObjectKey{Name: httpRouteFilterName, Namespace: tt.mcpRoute.Namespace}, &httpRouteFilter)

			if tt.wantFilter {
				require.NoError(t, filterErr, "HTTPRouteFilter should exist")
			} else {
				require.Error(t, filterErr, "HTTPRouteFilter should not exist")
			}
		})
	}
}

func TestMCPRouteControllerCleanupSecurityPolicyResources(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch)

	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
				OAuth: &aigv1b1.MCPRouteOAuth{
					Issuer:    "https://auth.example.com",
					Audiences: []string{"test-audience"},
					JWKS: &aigv1b1.JWKS{
						RemoteJWKS: &egv1a1.RemoteJWKS{
							URI: "https://auth.example.com/.well-known/jwks.json",
						},
					},
					ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
						Resource:        "https://api.example.com/mcp",
						ScopesSupported: []string{"read", "write"},
					},
				},
			},
		},
	}

	err := fakeClient.Create(t.Context(), mcpRoute)
	require.NoError(t, err)

	httpRouteName := "test-http-route"

	err = c.syncMCPRouteSecurityPolicy(t.Context(), mcpRoute, httpRouteName)
	require.NoError(t, err)

	securityPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name
	backendTrafficPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name + oauthProtectedResourceMetadataSuffix
	protecedResourceMetadataFilterName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name + oauthProtectedResourceMetadataSuffix
	authServerMetadataFilterName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name + oauthAuthServerMetadataSuffix

	var securityPolicy egv1a1.SecurityPolicy
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &securityPolicy)
	require.NoError(t, err, "SecurityPolicy should exist before cleanup")

	var backendTrafficPolicy egv1a1.BackendTrafficPolicy
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: backendTrafficPolicyName, Namespace: mcpRoute.Namespace}, &backendTrafficPolicy)
	require.NoError(t, err, "BackendTrafficPolicy should exist before cleanup")

	var protecedResourceMetadataFilter egv1a1.HTTPRouteFilter
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: protecedResourceMetadataFilterName, Namespace: mcpRoute.Namespace}, &protecedResourceMetadataFilter)
	require.NoError(t, err, "Protected Resource Metadata HTTPRouteFilter should exist before cleanup")

	var authServerMetadataFilter egv1a1.HTTPRouteFilter
	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: authServerMetadataFilterName, Namespace: mcpRoute.Namespace}, &authServerMetadataFilter)
	require.NoError(t, err, "Authorization Server Metadata HTTPRouteFilter should exist before cleanup")

	mcpRouteWithoutSecurityPolicy := mcpRoute.DeepCopy()
	mcpRouteWithoutSecurityPolicy.Spec.SecurityPolicy = nil

	err = c.syncMCPRouteSecurityPolicy(t.Context(), mcpRouteWithoutSecurityPolicy, httpRouteName)
	require.NoError(t, err)

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &securityPolicy)
	require.Error(t, err, "SecurityPolicy should be deleted after cleanup")
	require.True(t, apierrors.IsNotFound(err), "SecurityPolicy should not be found after cleanup")

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: backendTrafficPolicyName, Namespace: mcpRoute.Namespace}, &backendTrafficPolicy)
	require.Error(t, err, "BackendTrafficPolicy should be deleted after cleanup")
	require.True(t, apierrors.IsNotFound(err), "BackendTrafficPolicy should not be found after cleanup")

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: protecedResourceMetadataFilterName, Namespace: mcpRoute.Namespace}, &protecedResourceMetadataFilter)
	require.Error(t, err, "Protected Resource Metadata HTTPRouteFilter should be deleted after cleanup")
	require.True(t, apierrors.IsNotFound(err), "Protected Resource Metadata HTTPRouteFilter should not be found after cleanup")

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: authServerMetadataFilterName, Namespace: mcpRoute.Namespace}, &authServerMetadataFilter)
	require.Error(t, err, "Authorization Server Metadata HTTPRouteFilter should be deleted after cleanup")
	require.True(t, apierrors.IsNotFound(err), "Authorization Server Metadata HTTPRouteFilter should not be found after cleanup")
}

func TestMCPRouteController_syncMCPRouteSecurityPolicy_DisableOAuthKeepsAPIKey(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch)

	securityPolicy := &aigv1b1.MCPRouteSecurityPolicy{
		OAuth: &aigv1b1.MCPRouteOAuth{
			Issuer: "https://auth.example.com",
			JWKS: &aigv1b1.JWKS{
				RemoteJWKS: &egv1a1.RemoteJWKS{
					URI: "https://auth.example.com/.well-known/jwks.json",
				},
			},
			ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{Resource: "https://api.example.com/mcp"},
		},
		APIKeyAuth: &egv1a1.APIKeyAuth{
			CredentialRefs: []gwapiv1.SecretObjectReference{{Name: "client-keys"}},
			ExtractFrom:    []*egv1a1.ExtractFrom{{Headers: []string{"x-api-key"}}},
		},
	}

	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       aigv1b1.MCPRouteSpec{SecurityPolicy: securityPolicy.DeepCopy()},
	}

	require.NoError(t, fakeClient.Create(t.Context(), mcpRoute))

	httpRouteName := "test-http-route"
	require.NoError(t, c.syncMCPRouteSecurityPolicy(t.Context(), mcpRoute, httpRouteName))

	securityPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name
	backendTrafficPolicyName := oauthProtectedResourceMetadataName(mcpRoute.Name)
	protectedResourceMetadataFilterName := oauthProtectedResourceMetadataName(mcpRoute.Name)
	authServerMetadataFilterName := oauthAuthServerMetadataFilterName(mcpRoute.Name)

	// Ensure OAuth resources exist after initial reconciliation.
	var sp egv1a1.SecurityPolicy
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &sp))
	require.NotNil(t, sp.Spec.JWT)
	require.NotNil(t, sp.Spec.APIKeyAuth)

	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: backendTrafficPolicyName, Namespace: mcpRoute.Namespace}, &egv1a1.BackendTrafficPolicy{}))
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: protectedResourceMetadataFilterName, Namespace: mcpRoute.Namespace}, &egv1a1.HTTPRouteFilter{}))
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: authServerMetadataFilterName, Namespace: mcpRoute.Namespace}, &egv1a1.HTTPRouteFilter{}))

	// Remove OAuth configuration and reconcile again.
	mcpRoute.Spec.SecurityPolicy.OAuth = nil
	require.NoError(t, fakeClient.Update(t.Context(), mcpRoute))
	require.NoError(t, c.syncMCPRouteSecurityPolicy(t.Context(), mcpRoute, httpRouteName))

	// SecurityPolicy should remain with API key config only.
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &sp))
	require.Nil(t, sp.Spec.JWT)
	require.NotNil(t, sp.Spec.APIKeyAuth)

	// OAuth-specific resources should be removed.
	err := fakeClient.Get(t.Context(), client.ObjectKey{Name: backendTrafficPolicyName, Namespace: mcpRoute.Namespace}, &egv1a1.BackendTrafficPolicy{})
	require.Error(t, err)
	require.True(t, apierrors.IsNotFound(err))

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: protectedResourceMetadataFilterName, Namespace: mcpRoute.Namespace}, &egv1a1.HTTPRouteFilter{})
	require.Error(t, err)
	require.True(t, apierrors.IsNotFound(err))

	err = fakeClient.Get(t.Context(), client.ObjectKey{Name: authServerMetadataFilterName, Namespace: mcpRoute.Namespace}, &egv1a1.HTTPRouteFilter{})
	require.Error(t, err)
	require.True(t, apierrors.IsNotFound(err))
}

func TestMCPRouteController_syncMCPRouteSecurityPolicy_ClaimToHeaders(t *testing.T) {
	// Test that ClaimToHeaders from MCPRoute OAuth config are correctly configured
	// in the SecurityPolicy's JWTProvider.
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch)

	mcpRoute := &aigv1b1.MCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: aigv1b1.MCPRouteSpec{
			SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
				OAuth: &aigv1b1.MCPRouteOAuth{
					Issuer:    "https://auth.example.com",
					Audiences: []string{"test-audience"},
					JWKS: &aigv1b1.JWKS{
						RemoteJWKS: &egv1a1.RemoteJWKS{
							URI: "https://auth.example.com/.well-known/jwks.json",
						},
					},
					ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
						Resource: "https://api.example.com/mcp",
					},
					ClaimToHeaders: []egv1a1.ClaimToHeader{
						{Claim: "sub", Header: "X-User-Id"},
						{Claim: "email", Header: "X-User-Email"},
						{Claim: "realm_access.roles", Header: "X-User-Roles"},
					},
				},
			},
		},
	}

	require.NoError(t, fakeClient.Create(t.Context(), mcpRoute))

	httpRouteName := "test-http-route"
	require.NoError(t, c.syncMCPRouteSecurityPolicy(t.Context(), mcpRoute, httpRouteName))

	// Verify SecurityPolicy was created with ClaimToHeaders.
	securityPolicyName := internalapi.MCPGeneratedResourceCommonPrefix + mcpRoute.Name
	var sp egv1a1.SecurityPolicy
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKey{Name: securityPolicyName, Namespace: mcpRoute.Namespace}, &sp))

	require.NotNil(t, sp.Spec.JWT)
	require.Len(t, sp.Spec.JWT.Providers, 1)

	provider := sp.Spec.JWT.Providers[0]
	require.Len(t, provider.ClaimToHeaders, 4)
	// The gateway always projects the verified "sub" claim into the trusted subject header first,
	// so the MCP proxy can read the subject without re-parsing the client-controlled token.
	require.Equal(t, egv1a1.ClaimToHeader{Claim: "sub", Header: internalapi.MCPSubjectHeader}, provider.ClaimToHeaders[0])
	require.Equal(t, egv1a1.ClaimToHeader{Claim: "sub", Header: "X-User-Id"}, provider.ClaimToHeaders[1])
	require.Equal(t, egv1a1.ClaimToHeader{Claim: "email", Header: "X-User-Email"}, provider.ClaimToHeaders[2])
	require.Equal(t, egv1a1.ClaimToHeader{Claim: "realm_access.roles", Header: "X-User-Roles"}, provider.ClaimToHeaders[3])
}

func Test_buildOAuthProtectedResourceMetadataJSON(t *testing.T) {
	auth := &aigv1b1.MCPRouteOAuth{
		Issuer: "https://auth.example.com",
		ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
			Resource:        "https://api.example.com/mcp",
			ScopesSupported: []string{"read", "write", "admin"},
		},
	}

	result := buildOAuthProtectedResourceMetadataJSON(auth, "https://api.example.com/mcp")

	var jsonResponse map[string]interface{}
	err := json.Unmarshal([]byte(result), &jsonResponse)
	require.NoError(t, err)

	require.Equal(t, "https://api.example.com/mcp", jsonResponse["resource"])
	require.Equal(t, []interface{}{"https://auth.example.com"}, jsonResponse["authorization_servers"])
	require.Equal(t, []interface{}{"header"}, jsonResponse["bearer_methods_supported"])
	require.Equal(t, []interface{}{"read", "write", "admin"}, jsonResponse["scopes_supported"])
}

func Test_buildWWWAuthenticateHeaderValue(t *testing.T) {
	tests := []struct {
		name            string
		resourceURL     string
		scopesSupported []string
		expected        string
	}{
		{
			name:        "https URL with path",
			resourceURL: "https://api.example.com/mcp/v1",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp/v1"`,
		},
		{
			name:        "https URL without path",
			resourceURL: "https://api.example.com",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource"`,
		},
		{
			name:        "https URL with trailing slash",
			resourceURL: "https://api.example.com/mcp/",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp"`,
		},
		{
			name:        "http URL with path",
			resourceURL: "http://api.example.com/mcp/v1",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="http://api.example.com/.well-known/oauth-protected-resource/mcp/v1"`,
		},
		{
			name:        "http URL without path",
			resourceURL: "http://api.example.com",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="http://api.example.com/.well-known/oauth-protected-resource"`,
		},
		{
			name:        "http URL with trailing slash",
			resourceURL: "http://api.example.com/mcp/",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="http://api.example.com/.well-known/oauth-protected-resource/mcp"`,
		},
		{
			name:        "URL with port number https",
			resourceURL: "https://api.example.com:8080/mcp",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com:8080/.well-known/oauth-protected-resource/mcp"`,
		},
		{
			name:        "URL with port number http",
			resourceURL: "http://api.example.com:8080/mcp",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="http://api.example.com:8080/.well-known/oauth-protected-resource/mcp"`,
		},
		{
			name:        "complex path with multiple segments",
			resourceURL: "https://api.example.com/v1/mcp/endpoint",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/v1/mcp/endpoint"`,
		},
		{
			name:        "with empty scopes supported",
			resourceURL: "https://api.example.com/mcp",
			expected:    `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp"`,
		},
		{
			name:            "with single scope supported",
			resourceURL:     "https://api.example.com/mcp",
			scopesSupported: []string{"read"},
			expected:        `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp", scope="read"`,
		},
		{
			name:            "with multiple scopes supported",
			resourceURL:     "https://api.example.com/mcp",
			scopesSupported: []string{"read", "write"},
			expected:        `Bearer error="invalid_token", error_description="The access token is missing or invalid", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp", scope="read write"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildWWWAuthenticateHeaderValue(tt.resourceURL, tt.scopesSupported)
			require.Equal(t, tt.expected, result)
		})
	}
}

func Test_fetchOAuthServerMetadata(t *testing.T) {
	tests := []struct {
		name           string
		issuerPath     string
		authSeverURL   string
		forcedFailures int
		wantStatusCode int
	}{
		{
			name:           "root path empty",
			issuerPath:     "",
			authSeverURL:   "/.well-known/oauth-authorization-server",
			forcedFailures: 0,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "root path trailing slash",
			issuerPath:     "/",
			authSeverURL:   "/.well-known/oauth-authorization-server",
			forcedFailures: 0,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "well-known at the end",
			issuerPath:     "/some/path",
			authSeverURL:   "/some/path/.well-known/oauth-authorization-server",
			forcedFailures: 0,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "well-known after issuer",
			issuerPath:     "/some/path",
			authSeverURL:   "/.well-known/oauth-authorization-server/some/path",
			forcedFailures: 0,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "oidc well-known at the end",
			issuerPath:     "/some/path",
			authSeverURL:   "/some/path/.well-known/openid-configuration",
			forcedFailures: 0,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "oidc well-known after issuer",
			issuerPath:     "/some/path",
			authSeverURL:   "/.well-known/openid-configuration/some/path",
			forcedFailures: 0,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "unknown failure",
			issuerPath:     "/",
			authSeverURL:   "/.well-known/oauth-authorization-server",
			forcedFailures: 1, // Allow to self-heal before the backoff retries are exhausted.
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "unknown failure",
			issuerPath:     "/",
			authSeverURL:   "/.well-known/oauth-authorization-server",
			forcedFailures: 20, // Do not allow to self-heal before the backoff retries are exhausted.
			wantStatusCode: http.StatusInternalServerError,
		},
		{
			name:           "no valid URL found",
			issuerPath:     "/",
			authSeverURL:   "/not-a-well-known",
			forcedFailures: 0,
			wantStatusCode: http.StatusNotFound,
		},
	}

	handler := func(failCount int) http.HandlerFunc {
		failures := 0
		return func(w http.ResponseWriter, r *http.Request) {
			if failures < failCount {
				w.WriteHeader(http.StatusInternalServerError)
				failures++
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")
			metadata := map[string]interface{}{
				"issuer":                 "http://" + r.Host,
				"authorization_endpoint": "http://" + r.Host + "/auth",
				"token_endpoint":         "http://" + r.Host + "/token",
				// Use HTTP to force the backend TLS Policy discovery.
				"jwks_uri": "https://" + r.Host + "/.well-known/jwks.json",
			}
			_ = json.NewEncoder(w).Encode(metadata)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(tt.authSeverURL, handler(tt.forcedFailures))

			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			addr := server.Listener.Addr().String()

			// Use a small backoff timeout that allows the test to configure a number of attempts
			// to force failures or self-healing.
			metadata, err := fetchOAuthAuthServerMetadata(server.URL+tt.issuerPath, 1*time.Second)

			if tt.wantStatusCode != http.StatusOK {
				var httpError *httpError
				require.ErrorAs(t, err, &httpError)
				require.Equal(t, tt.wantStatusCode, httpError.statusCode)
				require.Nil(t, metadata)
			} else {
				require.NoError(t, err)
				require.Equal(t, "http://"+addr, metadata.Issuer)
				require.Equal(t, "http://"+addr+"/auth", metadata.AuthorizationEndpoint)
				require.Equal(t, "http://"+addr+"/token", metadata.TokenEndpoint)
				require.Equal(t, "https://"+addr+"/.well-known/jwks.json", metadata.JwksURI)
			}
		})
	}
}

// Test_fetchOAuthServerMetadata_unusableDocument covers authorization servers that answer 200 at a
// well-known path they do not actually implement, returning an empty or incomplete document.
// Those must count as a miss so that the remaining URL variants are still tried.
func Test_fetchOAuthServerMetadata_unusableDocument(t *testing.T) {
	const issuerPath = "/some/path"

	// The URL variants are tried in this order.
	var (
		firstVariant = "/.well-known/oauth-authorization-server" + issuerPath
		lastVariant  = issuerPath + "/.well-known/openid-configuration"
	)

	writeJSON := func(body map[string]interface{}) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(body)
		}
	}

	completeDocument := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(map[string]interface{}{
			"issuer":                 "http://" + r.Host + issuerPath,
			"authorization_endpoint": "http://" + r.Host + "/auth",
			"token_endpoint":         "http://" + r.Host + "/token",
		})(w, r)
	}

	t.Run("falls through an empty document to a later variant", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc(firstVariant, writeJSON(map[string]interface{}{}))
		mux.HandleFunc(lastVariant, completeDocument)

		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		addr := server.Listener.Addr().String()

		metadata, err := fetchOAuthAuthServerMetadata(server.URL+issuerPath, 1*time.Second)
		require.NoError(t, err)
		require.Equal(t, "http://"+addr+issuerPath, metadata.Issuer)
		require.Equal(t, "http://"+addr+"/auth", metadata.AuthorizationEndpoint)
		require.Equal(t, "http://"+addr+"/token", metadata.TokenEndpoint)
	})

	t.Run("does not leak fields from an incomplete document", func(t *testing.T) {
		mux := http.NewServeMux()
		// Valid JSON, but without the members needed to drive an authorization flow.
		mux.HandleFunc(firstVariant, writeJSON(map[string]interface{}{
			"jwks_uri": "https://leaked.example.com/keys",
		}))
		mux.HandleFunc(lastVariant, completeDocument)

		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		metadata, err := fetchOAuthAuthServerMetadata(server.URL+issuerPath, 1*time.Second)
		require.NoError(t, err)
		require.Empty(t, metadata.JwksURI, "jwks_uri from a rejected variant must not survive")
	})

	t.Run("fails when no variant yields a usable document", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc(lastVariant, writeJSON(map[string]interface{}{}))

		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		metadata, err := fetchOAuthAuthServerMetadata(server.URL+issuerPath, 1*time.Second)
		require.Nil(t, metadata)
		var invalidErr *invalidMetadataError
		require.ErrorAs(t, err, &invalidErr)
		require.ErrorContains(t, err, "missing issuer")
	})

	// discoverJWKSURI only needs jwks_uri, so a document without the authorization flow endpoints
	// must still reach that caller rather than being rejected by the fetcher.
	t.Run("accepts a document without the authorization flow endpoints", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc(firstVariant, writeJSON(map[string]interface{}{}))
		mux.HandleFunc(lastVariant, writeJSON(map[string]interface{}{
			"issuer":   "https://idp.example.com",
			"jwks_uri": "https://idp.example.com/keys",
		}))

		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		metadata, err := fetchOAuthAuthServerMetadata(server.URL+issuerPath, 1*time.Second)
		require.NoError(t, err)
		require.Equal(t, "https://idp.example.com/keys", metadata.JwksURI)
	})

	t.Run("falls through a non-JSON body to a later variant", func(t *testing.T) {
		mux := http.NewServeMux()
		// A catch-all route that serves HTML with a 200 for every unknown path.
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html><body>not found</body></html>"))
		})
		mux.HandleFunc(lastVariant, completeDocument)

		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		addr := server.Listener.Addr().String()

		metadata, err := fetchOAuthAuthServerMetadata(server.URL+issuerPath, 1*time.Second)
		require.NoError(t, err)
		require.Equal(t, "http://"+addr+issuerPath, metadata.Issuer)
	})
}

func Test_resolveDeterministicHostname(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	ctx := t.Context()

	// Seed parent Gateway objects for tests.
	gwSingleListener := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-single", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gw-single.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gwSingleListener))

	gwDeduplicatedListeners := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-dedup", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "http",
					Protocol: gwapiv1.HTTPProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gw-shared.example.com")),
				},
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gw-shared.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gwDeduplicatedListeners))

	gwMultipleListeners := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-multi", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "listener-a",
					Protocol: gwapiv1.HTTPSProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("a.example.com")),
				},
				{
					Name:     "listener-b",
					Protocol: gwapiv1.HTTPSProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("b.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gwMultipleListeners))

	gwWildcardListener := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-wildcard", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("*.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gwWildcardListener))

	gwNoHostnames := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-no-host", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gwNoHostnames))

	tests := []struct {
		name        string
		mcpRoute    *aigv1b1.MCPRoute
		k8sClient   client.Client
		expected    string
		expectedErr string
	}{
		{
			name: "route specifies multiple hostnames",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					Hostnames: []gwapiv1.Hostname{"a.example.com", "b.example.com"},
				},
			},
			expectedErr: "cannot derive OAuth protectedResourceMetadata.resource: route specifies multiple hostnames",
		},
		{
			name: "route specifies wildcard hostname",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					Hostnames: []gwapiv1.Hostname{"*.example.com"},
				},
			},
			expectedErr: "contains wildcard",
		},
		{
			name: "route specifies empty hostname",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r3", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					Hostnames: []gwapiv1.Hostname{""},
				},
			},
			expectedErr: "route hostname is empty",
		},
		{
			name: "route specifies single valid hostname",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r4", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					Hostnames: []gwapiv1.Hostname{"api.example.com"},
				},
			},
			expected: "api.example.com",
		},
		{
			name: "route has no hostnames and no parentRefs",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r5", Namespace: "default"},
			},
			expectedErr: "route must reference exactly one parent gateway",
		},
		{
			name: "route has multiple parentRefs",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r6", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw1"},
						{Name: "gw2"},
					},
				},
			},
			expectedErr: "route must reference exactly one parent gateway",
		},
		{
			name: "client is nil when resolving via parentRefs",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r7", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-single"},
					},
				},
			},
			k8sClient:   nil,
			expectedErr: "parent Gateway cannot be inspected without Kubernetes client",
		},
		{
			name: "parent Gateway not found",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r9", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "non-existent-gw"},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "failed to get parent Gateway default/non-existent-gw",
		},
		{
			name: "parent Gateway sectionName found",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r10", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-multi", SectionName: ptr.To(gwapiv1.SectionName("listener-a"))},
					},
				},
			},
			k8sClient: fakeClient,
			expected:  "a.example.com",
		},
		{
			name: "parent Gateway sectionName not found",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r11", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-multi", SectionName: ptr.To(gwapiv1.SectionName("unknown-listener"))},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "has no listener named \"unknown-listener\"",
		},
		{
			name: "parent Gateway sectionName listener has wildcard",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r12", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-wildcard", SectionName: ptr.To(gwapiv1.SectionName("https"))},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "listener \"https\" hostname \"*.example.com\" contains wildcard",
		},
		{
			name: "parent Gateway sectionName listener has no hostname",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r13", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-no-host", SectionName: ptr.To(gwapiv1.SectionName("https"))},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "listener \"https\" on parent Gateway default/gw-no-host has no hostname configured",
		},
		{
			name: "parent Gateway single listener",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r14", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-single"},
					},
				},
			},
			k8sClient: fakeClient,
			expected:  "gw-single.example.com",
		},
		{
			name: "parent Gateway deduplicated listener hostnames",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r15", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-dedup"},
					},
				},
			},
			k8sClient: fakeClient,
			expected:  "gw-shared.example.com",
		},
		{
			name: "parent Gateway multiple listener hostnames",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r16", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-multi"},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "parent Gateway default/gw-multi has multiple listener hostnames",
		},
		{
			name: "parent Gateway listener with wildcard",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r17", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-wildcard"},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "parent Gateway default/gw-wildcard listener hostname \"*.example.com\" contains wildcard",
		},
		{
			name: "parent Gateway no listeners with hostname",
			mcpRoute: &aigv1b1.MCPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "r18", Namespace: "default"},
				Spec: aigv1b1.MCPRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{
						{Name: "gw-no-host"},
					},
				},
			},
			k8sClient:   fakeClient,
			expectedErr: "parent Gateway default/gw-no-host has no HTTP/HTTPS listeners with configured hostnames",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveDeterministicHostname(ctx, tt.k8sClient, tt.mcpRoute)
			if tt.expectedErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

func Test_resolveOAuthResourceURL(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	ctx := t.Context()

	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
					Port:     443,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gateway.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gw))

	httpGw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "http-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "http",
					Protocol: gwapiv1.HTTPProtocolType,
					Port:     80,
					Hostname: (*gwapiv1.Hostname)(ptr.To("http-gateway.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, httpGw))

	customPortGw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-port-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "https-8443",
					Protocol: gwapiv1.HTTPSProtocolType,
					Port:     8443,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gateway.example.com")),
				},
				{
					Name:     "http-8080",
					Protocol: gwapiv1.HTTPProtocolType,
					Port:     8080,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gateway.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, customPortGw))

	dualGw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "dual-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "http",
					Protocol: gwapiv1.HTTPProtocolType,
					Port:     80,
					Hostname: (*gwapiv1.Hostname)(ptr.To("dual.example.com")),
				},
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
					Port:     443,
					Hostname: (*gwapiv1.Hostname)(ptr.To("dual.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, dualGw))

	tcpGw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "tcp-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "tcp-listener",
					Protocol: gwapiv1.TCPProtocolType,
					Port:     9000,
					Hostname: (*gwapiv1.Hostname)(ptr.To("tcp.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, tcpGw))

	t.Run("explicit resource takes precedence", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Hostnames: []gwapiv1.Hostname{"ignored.example.com"},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{
						ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
							Resource: "https://explicit.example.com/mcp/",
						},
					},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "https://explicit.example.com/mcp", url)
	})

	t.Run("omitted resource derives from route hostname with default path", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Hostnames: []gwapiv1.Hostname{"route.example.com"},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "https://route.example.com/mcp", url)
	})

	t.Run("omitted resource derives from route hostname with custom path", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r3", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Path:      ptr.To("/v1/custom/endpoint/"),
				Hostnames: []gwapiv1.Hostname{"route.example.com"},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "https://route.example.com/v1/custom/endpoint", url)
	})

	t.Run("omitted resource derives from parent gateway listener hostname", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r4", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{Name: "my-gw"},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "https://gateway.example.com/mcp", url)
	})

	t.Run("omitted resource fails derivation when hostname is ambiguous", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r5", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Hostnames: []gwapiv1.Hostname{"a.com", "b.com"},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		_, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.Error(t, err)
		require.Contains(t, err.Error(), "route specifies multiple hostnames")
	})

	t.Run("omitted resource derives http scheme when listener is HTTP:80", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r-http", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{Name: "http-gw"},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "http://http-gateway.example.com/mcp", url)
	})

	t.Run("omitted resource derives https scheme with non-standard port 8443 via sectionName", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r-https-8443", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{
						Name:        "custom-port-gw",
						SectionName: (*gwapiv1.SectionName)(ptr.To("https-8443")),
					},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "https://gateway.example.com:8443/mcp", url)
	})

	t.Run("omitted resource derives http scheme with non-standard port 8080 via sectionName", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r-http-8080", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{
						Name:        "custom-port-gw",
						SectionName: (*gwapiv1.SectionName)(ptr.To("http-8080")),
					},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "http://gateway.example.com:8080/mcp", url)
	})

	t.Run("omitted resource prefers https:443 when both http:80 and https:443 listeners exist", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r-dual", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{Name: "dual-gw"},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "https://dual.example.com/mcp", url)
	})

	t.Run("omitted resource rejects non-HTTP/HTTPS listener referenced by sectionName", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r-tcp", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{
						Name:        "tcp-gw",
						SectionName: (*gwapiv1.SectionName)(ptr.To("tcp-listener")),
					},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		_, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.Error(t, err)
		require.Contains(t, err.Error(), `protocol "TCP" is not HTTP or HTTPS`)
	})

	t.Run("route hostname derives scheme and port from parent gateway listener if attached", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r-route-attached", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Hostnames: []gwapiv1.Hostname{"gateway.example.com"},
				ParentRefs: []gwapiv1.ParentReference{
					{
						Name:        "custom-port-gw",
						SectionName: (*gwapiv1.SectionName)(ptr.To("http-8080")),
					},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{},
				},
			},
		}
		url, err := resolveOAuthResourceURL(ctx, fakeClient, mcpRoute)
		require.NoError(t, err)
		require.Equal(t, "http://gateway.example.com:8080/mcp", url)
	})
}

func TestMCPRouteController_syncMCPRouteSecurityPolicy_AutoDeriveResource(t *testing.T) {
	fakeClient := requireNewFakeClientWithIndexesForMCP(t)
	eventCh := internaltesting.NewControllerEventChan[*gwapiv1.Gateway]()
	c := NewMCPRouteController(fakeClient, nil, logr.Discard(), eventCh.Ch)
	ctx := t.Context()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jwks_uri": "https://auth.example.com/.well-known/jwks.json",
		})
	}))
	t.Cleanup(server.Close)

	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{
				{
					Name:     "https",
					Protocol: gwapiv1.HTTPSProtocolType,
					Hostname: (*gwapiv1.Hostname)(ptr.To("gw-host.example.com")),
				},
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, gw))

	jwks := &aigv1b1.JWKS{
		RemoteJWKS: &egv1a1.RemoteJWKS{
			URI: "https://auth.example.com/.well-known/jwks.json",
		},
	}

	t.Run("auto-derives from route hostname", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-auto-hostname", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Hostnames: []gwapiv1.Hostname{"mcp.example.com"},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{
						Issuer: "https://auth.example.com",
						JWKS:   jwks,
						ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
							ScopesSupported: []string{"tools"},
						},
					},
				},
			},
		}
		require.NoError(t, fakeClient.Create(ctx, mcpRoute))
		require.NoError(t, c.syncMCPRouteSecurityPolicy(ctx, mcpRoute, "main-route"))

		// Check BackendTrafficPolicy WWW-Authenticate value
		var btp egv1a1.BackendTrafficPolicy
		btpName := oauthProtectedResourceMetadataName(mcpRoute.Name)
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: btpName, Namespace: mcpRoute.Namespace}, &btp))
		require.Len(t, btp.Spec.ResponseOverride, 1)
		headers := btp.Spec.ResponseOverride[0].Response.Header.Set
		var wwwAuth string
		for _, h := range headers {
			if string(h.Name) == "WWW-Authenticate" {
				wwwAuth = h.Value
				break
			}
		}
		require.NotEmpty(t, wwwAuth)
		require.Contains(t, wwwAuth, `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`)

		// Check HTTPRouteFilter metadata JSON
		var hrf egv1a1.HTTPRouteFilter
		hrfName := oauthProtectedResourceMetadataName(mcpRoute.Name)
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: hrfName, Namespace: mcpRoute.Namespace}, &hrf))
		require.NotNil(t, hrf.Spec.DirectResponse)
		require.NotNil(t, hrf.Spec.DirectResponse.Body.Inline)
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(*hrf.Spec.DirectResponse.Body.Inline), &body))
		require.Equal(t, "https://mcp.example.com/mcp", body["resource"])
	})

	t.Run("auto-derives from parent gateway listener hostname", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-auto-gateway", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{Name: "test-gw"},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{
						Issuer: "https://auth.example.com",
						JWKS:   jwks,
						ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
							ScopesSupported: []string{"tools"},
						},
					},
				},
			},
		}
		require.NoError(t, fakeClient.Create(ctx, mcpRoute))
		require.NoError(t, c.syncMCPRouteSecurityPolicy(ctx, mcpRoute, "main-route"))

		var hrf egv1a1.HTTPRouteFilter
		hrfName := oauthProtectedResourceMetadataName(mcpRoute.Name)
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: hrfName, Namespace: mcpRoute.Namespace}, &hrf))
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(*hrf.Spec.DirectResponse.Body.Inline), &body))
		require.Equal(t, "https://gw-host.example.com/mcp", body["resource"])
	})

	t.Run("fails when hostname is ambiguous", func(t *testing.T) {
		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-ambiguous", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				Hostnames: []gwapiv1.Hostname{"a.example.com", "b.example.com"},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{
						Issuer: "https://auth.example.com",
						JWKS:   jwks,
					},
				},
			},
		}
		require.NoError(t, fakeClient.Create(ctx, mcpRoute))
		err := c.syncMCPRouteSecurityPolicy(ctx, mcpRoute, "main-route")
		require.Error(t, err)
		require.Contains(t, err.Error(), "route specifies multiple hostnames")
	})

	t.Run("updates HRF and BTP when gateway listener hostname changes", func(t *testing.T) {
		gwChanging := &gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-changing", Namespace: "default"},
			Spec: gwapiv1.GatewaySpec{
				Listeners: []gwapiv1.Listener{
					{
						Name:     "https",
						Protocol: gwapiv1.HTTPSProtocolType,
						Hostname: (*gwapiv1.Hostname)(ptr.To("initial.example.com")),
					},
				},
			},
		}
		require.NoError(t, fakeClient.Create(ctx, gwChanging))

		mcpRoute := &aigv1b1.MCPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-gw-changing", Namespace: "default"},
			Spec: aigv1b1.MCPRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{
					{Name: "gw-changing"},
				},
				SecurityPolicy: &aigv1b1.MCPRouteSecurityPolicy{
					OAuth: &aigv1b1.MCPRouteOAuth{
						Issuer: "https://auth.example.com",
						JWKS:   jwks,
						ProtectedResourceMetadata: aigv1b1.ProtectedResourceMetadata{
							ScopesSupported: []string{"tools"},
						},
					},
				},
			},
		}
		require.NoError(t, fakeClient.Create(ctx, mcpRoute))
		require.NoError(t, c.syncMCPRouteSecurityPolicy(ctx, mcpRoute, "main-route"))

		// Check initial BTP and HRF
		var btp egv1a1.BackendTrafficPolicy
		btpName := oauthProtectedResourceMetadataName(mcpRoute.Name)
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: btpName, Namespace: mcpRoute.Namespace}, &btp))
		require.Len(t, btp.Spec.ResponseOverride, 1)
		var wwwAuth string
		for _, h := range btp.Spec.ResponseOverride[0].Response.Header.Set {
			if string(h.Name) == "WWW-Authenticate" {
				wwwAuth = h.Value
				break
			}
		}
		require.Contains(t, wwwAuth, `resource_metadata="https://initial.example.com/.well-known/oauth-protected-resource/mcp"`)

		var hrf egv1a1.HTTPRouteFilter
		hrfName := oauthProtectedResourceMetadataName(mcpRoute.Name)
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: hrfName, Namespace: mcpRoute.Namespace}, &hrf))
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(*hrf.Spec.DirectResponse.Body.Inline), &body))
		require.Equal(t, "https://initial.example.com/mcp", body["resource"])

		// Gateway listener hostname changes
		gwChanging.Spec.Listeners[0].Hostname = (*gwapiv1.Hostname)(ptr.To("updated.example.com"))
		require.NoError(t, fakeClient.Update(ctx, gwChanging))

		// Gateway watcher enqueues the MCPRoute
		reqs := c.gatewayEventHandler(ctx, gwChanging)
		require.Len(t, reqs, 1)
		require.Equal(t, "default/route-gw-changing", reqs[0].String())

		// Re-sync after Gateway event
		require.NoError(t, c.syncMCPRouteSecurityPolicy(ctx, mcpRoute, "main-route"))

		// Check updated BTP and HRF have the new hostname
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: btpName, Namespace: mcpRoute.Namespace}, &btp))
		wwwAuth = ""
		for _, h := range btp.Spec.ResponseOverride[0].Response.Header.Set {
			if string(h.Name) == "WWW-Authenticate" {
				wwwAuth = h.Value
				break
			}
		}
		require.Contains(t, wwwAuth, `resource_metadata="https://updated.example.com/.well-known/oauth-protected-resource/mcp"`)

		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: hrfName, Namespace: mcpRoute.Namespace}, &hrf))
		require.NoError(t, json.Unmarshal([]byte(*hrf.Spec.DirectResponse.Body.Inline), &body))
		require.Equal(t, "https://updated.example.com/mcp", body["resource"])
	})
}
