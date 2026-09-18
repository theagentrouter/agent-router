// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package rotators

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"google.golang.org/api/sts/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/gcpauth"
)

const (
	// GCPAccessTokenKey is the key used to store GCP access token in Kubernetes secrets.
	GCPAccessTokenKey     = "gcpAccessToken"
	GCPServiceAccountJSON = "service_account.json"
	GCPProjectNameKey     = "projectName"
	GCPRegionKey          = "region"
	// grantTypeTokenExchange is the OAuth 2.0 grant type for token exchange.
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" //nolint:gosec
	// gcpIAMScope is the OAuth scope for IAM operations in GCP.
	gcpIAMScope = "https://www.googleapis.com/auth/iam" //nolint:gosec
	// tokenTypeAccessToken indicates the requested token type is an access token.
	tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token" //nolint:gosec
	// tokenTypeJWT indicates the subject token type is a JWT.
	tokenTypeJWT = "urn:ietf:params:oauth:token-type:jwt" //nolint:gosec
	// stsTokenScope is the OAuth scope for GCP cloud platform operations.
	stsTokenScope = "https://www.googleapis.com/auth/cloud-platform" //nolint:gosec
)

// serviceAccountTokenGenerator defines a function type for generating a GCP service account access token
// using an STS token and impersonation configuration.
type serviceAccountTokenGenerator func(
	ctx context.Context,
	stsToken string,
	saConfig aigv1b1.GCPServiceAccountImpersonationConfig,
	projectName string,
	opts ...option.ClientOption,
) (*tokenprovider.TokenExpiry, error)

// stsTokenGenerator defines a function type for exchanging a JWT token for a GCP STS token
// using Workload Identity Federation configuration.
type stsTokenGenerator func(
	ctx context.Context,
	jwtToken string,
	wifConfig *aigv1b1.GCPWorkloadIdentityFederationConfig,
	opts ...option.ClientOption,
) (*tokenprovider.TokenExpiry, error)

// gcpOIDCTokenRotator implements Rotator interface for GCP access token exchange.
// It handles the complete authentication flow for GCP Workload Identity Federation:
//  1. Obtaining an OIDC token from the configured provider
//  2. Exchanging the OIDC token for a GCP STS token
//  3. Using the STS token to impersonate a GCP service account
//  4. Storing the resulting access token in a Kubernetes secret
type gcpOIDCTokenRotator struct {
	client client.Client // Kubernetes client for interacting with the cluster.
	logger logr.Logger   // Logger for recording rotator activities.
	// GCP Credentials configuration from BackendSecurityPolicy.
	gcpCredentials aigv1b1.BackendSecurityPolicyGCPCredentials
	// backendSecurityPolicyName provides name of backend security policy.
	backendSecurityPolicyName string
	// backendSecurityPolicyNamespace provides namespace of backend security policy.
	backendSecurityPolicyNamespace string
	// preRotationWindow is the duration before token expiry when rotation should occur.
	preRotationWindow time.Duration
	// oidcProvider provides the OIDC token needed for GCP Workload Identity Federation.
	oidcProvider tokenprovider.TokenProvider

	saTokenFunc  serviceAccountTokenGenerator
	stsTokenFunc stsTokenGenerator
}

// sharedGCPTransport is a shared HTTP transport used for GCP API calls.
// It is initialized with the GCP proxy URL if provided in the environment variable.
var (
	sharedGCPTransport     http.RoundTripper
	sharedGCPTransportOnce sync.Once
	sharedGCPTransportErr  error
)

func initSharedGCPTransport() error {
	sharedGCPTransportOnce.Do(func() {
		sharedGCPTransport, sharedGCPTransportErr = gcpauth.NewTransport()
	})
	return sharedGCPTransportErr
}

// NewGCPOIDCTokenRotator creates a new gcpOIDCTokenRotator with the given parameters.
func NewGCPOIDCTokenRotator(
	client client.Client,
	logger logr.Logger,
	bsp *aigv1b1.BackendSecurityPolicy,
	preRotationWindow time.Duration,
	tokenProvider tokenprovider.TokenProvider,
) (Rotator, error) {
	logger = logger.WithName("gcp-token-rotator")

	if err := initSharedGCPTransport(); err != nil {
		return nil, fmt.Errorf("error initializing GCP transport: %w", err)
	}

	if bsp.Spec.GCPCredentials == nil {
		return nil, fmt.Errorf("GCP credentials are not configured in BackendSecurityPolicy %s/%s", bsp.Namespace, bsp.Name)
	}

	return &gcpOIDCTokenRotator{
		client:                         client,
		logger:                         logger,
		gcpCredentials:                 *bsp.Spec.GCPCredentials,
		backendSecurityPolicyName:      bsp.Name,
		backendSecurityPolicyNamespace: bsp.Namespace,
		preRotationWindow:              preRotationWindow,
		oidcProvider:                   tokenProvider,
		saTokenFunc:                    impersonateServiceAccount,
		stsTokenFunc:                   exchangeJWTForSTSToken,
	}, nil
}

// IsExpired implements [Rotator.IsExpired].
// IsExpired checks if the preRotation time is before the current time.
func (r *gcpOIDCTokenRotator) IsExpired(preRotationExpirationTime time.Time) bool {
	// Use the common IsBufferedTimeExpired helper to determine if the token has expired.
	// A buffer of 0 means we check exactly at the pre-rotation time.
	return IsBufferedTimeExpired(0, preRotationExpirationTime)
}

// GetPreRotationTime implements [Rotator.GetPreRotationTime].
// GetPreRotationTime retrieves the pre-rotation time for GCP token.
func (r *gcpOIDCTokenRotator) GetPreRotationTime(ctx context.Context) (time.Time, error) {
	// Look up the secret containing the current token.
	secret, err := LookupSecret(ctx, r.client, r.backendSecurityPolicyNamespace, GetBSPSecretName(r.backendSecurityPolicyName))
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the secret doesn't exist, return zero time to indicate immediate rotation is needed.
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("failed to lookup secret: %w", err)
	}
	// Extract the token expiration time from the secret's annotations.
	expirationTime, err := GetExpirationSecretAnnotation(secret)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get expiration time from secret: %w", err)
	}

	// Calculate the pre-rotation time by subtracting the pre-rotation window from the expiration time
	// This ensures tokens are rotated before they expire.
	preRotationTime := expirationTime.Add(-r.preRotationWindow)
	return preRotationTime, nil
}

// Rotate implements [Rotator.Rotate].
// Rotate fetches new GCP access token and updates the Kubernetes secret.
// The token rotation process follows these steps:
// 1. Obtain an OIDC token from the configured provider
// 2. Exchange the OIDC token for a GCP STS token
// 3. (If configured) Use the STS token to impersonate the specified GCP service account
// 4. Store the resulting access token in a Kubernetes secret
// Returns the expiration time of the new token and any error encountered during rotation.
func (r *gcpOIDCTokenRotator) Rotate(ctx context.Context) (time.Time, error) {
	secretName := GetBSPSecretName(r.backendSecurityPolicyName)

	r.logger.Info("start rotating gcp access token", "namespace", r.backendSecurityPolicyNamespace, "name", r.backendSecurityPolicyName)

	// 1. Get OIDCProvider Token.
	// This is the initial token from the configured OIDC provider (e.g., Kubernetes service account token).
	oidcTokenExpiry, err := r.oidcProvider.GetToken(ctx)
	if err != nil {
		r.logger.Error(err, "failed to get token from oidc provider", "oidcIssuer", r.gcpCredentials.WorkloadIdentityFederationConfig.WorkloadIdentityProviderName)
		return time.Time{}, fmt.Errorf("failed to obtain OIDC token: %w", err)
	}

	// 2. Exchange the JWT for an STS token.
	// The OIDC JWT token is exchanged for a Google Cloud STS token.
	stsToken, err := r.stsTokenFunc(ctx, oidcTokenExpiry.Token, r.gcpCredentials.WorkloadIdentityFederationConfig)
	if err != nil {
		wifConfig := r.gcpCredentials.WorkloadIdentityFederationConfig
		r.logger.Error(err, "failed to exchange JWT for STS token",
			"projectID", wifConfig.ProjectID,
			"workloadIdentityPool", wifConfig.WorkloadIdentityPoolName,
			"workloadIdentityProvider", wifConfig.WorkloadIdentityProviderName)
		return time.Time{}, fmt.Errorf("failed to exchange JWT for STS token (project: %s, pool: %s): %w",
			wifConfig.ProjectID, wifConfig.WorkloadIdentityPoolName, err)
	}

	// 3. Exchange the STS token for a GCP service account access token.
	// The STS token is used to impersonate a GCP service account.
	var gcpAccessToken *tokenprovider.TokenExpiry
	if r.gcpCredentials.WorkloadIdentityFederationConfig.ServiceAccountImpersonation != nil {
		gcpAccessToken, err = r.saTokenFunc(ctx, stsToken.Token, *r.gcpCredentials.WorkloadIdentityFederationConfig.ServiceAccountImpersonation, r.gcpCredentials.ProjectName)
		if err != nil {
			saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com",
				r.gcpCredentials.WorkloadIdentityFederationConfig.ServiceAccountImpersonation.ServiceAccountName,
				r.gcpCredentials.ProjectName)
			r.logger.Error(err, "failed to impersonate GCP service account",
				"serviceAccount", saEmail,
				"serviceAccountProject", r.gcpCredentials.ProjectName)
			return time.Time{}, fmt.Errorf("failed to impersonate service account %s: %w", saEmail, err)
		}
	} else {
		// If no service account impersonation is configured, use the STS token directly.
		gcpAccessToken = stsToken
	}

	secret, err := LookupSecret(ctx, r.client, r.backendSecurityPolicyNamespace, secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.Info("creating a new gcp access token into secret", "namespace", r.backendSecurityPolicyNamespace, "name", r.backendSecurityPolicyName)
			secret = &corev1.Secret{
				Name:      secretName,
				Namespace: r.backendSecurityPolicyNamespace,
				Type:      corev1.SecretTypeOpaque,
				Data:      make(map[string][]byte),
			}
			populateInSecret(secret, filterapi.GCPAuth{
				AccessToken: gcpAccessToken.Token,
				Region:      r.gcpCredentials.Region,
				ProjectName: r.gcpCredentials.ProjectName,
			}, gcpAccessToken.ExpiresAt)
			err = r.client.Create(ctx, secret)
			if err != nil {
				r.logger.Error(err, "failed to create gcp access token", "namespace", r.backendSecurityPolicyNamespace, "name", r.backendSecurityPolicyName)
				return time.Time{}, err
			}
			return gcpAccessToken.ExpiresAt, nil
		}
		r.logger.Error(err, "failed to lookup gcp access token secret", "namespace", r.backendSecurityPolicyNamespace, "name", r.backendSecurityPolicyName)
		return time.Time{}, err
	}
	r.logger.Info("updating gcp access token secret", "namespace", r.backendSecurityPolicyNamespace, "name", r.backendSecurityPolicyName)

	populateInSecret(secret, filterapi.GCPAuth{
		AccessToken: gcpAccessToken.Token,
		Region:      r.gcpCredentials.Region,
		ProjectName: r.gcpCredentials.ProjectName,
	}, gcpAccessToken.ExpiresAt)
	err = r.client.Update(ctx, secret)
	if err != nil {
		r.logger.Error(err, "failed to update gcp access token", "namespace", r.backendSecurityPolicyNamespace, "name", r.backendSecurityPolicyName)
		return time.Time{}, err
	}
	return gcpAccessToken.ExpiresAt, nil
}

var _ stsTokenGenerator = exchangeJWTForSTSToken

// exchangeJWTForSTSToken implements [stsTokenGenerator]
// exchangeJWTForSTSToken exchanges a JWT token for a GCP STS (Security Token Service) token.
func exchangeJWTForSTSToken(ctx context.Context, jwtToken string, wifConfig *aigv1b1.GCPWorkloadIdentityFederationConfig, opts ...option.ClientOption) (*tokenprovider.TokenExpiry, error) {
	// This step does not pass the token via the auth header.
	// The empty string implies that the auth header will be skipped.
	roundTripper, err := newBearerAuthRoundTripper("")
	if err != nil {
		return nil, fmt.Errorf("error creating HTTP transport for STS token exchange: %w", err)
	}

	// Create an HTTP client with a custom RoundTripper that adds the Bearer token Authorization header.
	httpClient := &http.Client{
		Transport: roundTripper,
	}
	// Prepend the HTTP client option so test options can override it.
	opts = append([]option.ClientOption{option.WithHTTPClient(httpClient)}, opts...)

	// Create an STS client.
	stsService, err := sts.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("error creating GCP STS service client: %w", err)
	}
	// Construct the STS request.
	// Build the audience string in the format required by GCP Workload Identity Federation.
	stsAudience := fmt.Sprintf("//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		wifConfig.ProjectID,
		wifConfig.WorkloadIdentityPoolName,
		wifConfig.WorkloadIdentityProviderName)

	// Create the token exchange request with the appropriate parameters.
	req := &sts.GoogleIdentityStsV1ExchangeTokenRequest{
		GrantType:          grantTypeTokenExchange,
		Audience:           stsAudience,
		Scope:              gcpIAMScope,
		RequestedTokenType: tokenTypeAccessToken,
		SubjectToken:       jwtToken,
		SubjectTokenType:   tokenTypeJWT,
	}

	// Call the STS API.
	resp, err := stsService.V1.Token(req).Do()
	if err != nil {
		return nil, fmt.Errorf("error calling GCP STS Token API with audience %s: %w", stsAudience, err)
	}

	return &tokenprovider.TokenExpiry{
		Token:     resp.AccessToken,
		ExpiresAt: time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}, nil
}

// bearerAuthRoundTripper implements [http.RoundTripper].
var _ http.RoundTripper = &bearerAuthRoundTripper{}

// bearerAuthRoundTripper is an HTTP RoundTripper that adds a Bearer token to the Authorization header.
type bearerAuthRoundTripper struct {
	base  http.RoundTripper
	token string
}

func newBearerAuthRoundTripper(token string) (http.RoundTripper, error) {
	return &bearerAuthRoundTripper{
		base:  sharedGCPTransport,
		token: token,
	}, nil
}

// RoundTrip implements [RoundTripper.RoundTrip].
func (rt *bearerAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	if rt.token != "" {
		req2.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return rt.base.RoundTrip(req2)
}

var _ serviceAccountTokenGenerator = impersonateServiceAccount

// impersonateServiceAccount returns a GCP service account access token or an error if impersonation fails.
// It takes an STS token and uses it to impersonate a GCP service account,
// generating a new access token with the permissions of that service account.
//
// The service account email is constructed from serviceAccountName and serviceAccountProjectName
// in the format: <serviceAccountName>@<serviceAccountProjectName>.iam.gserviceaccount.com
//
// The resulting token will have the cloud-platform scope.
func impersonateServiceAccount(ctx context.Context, stsToken string, saConfig aigv1b1.GCPServiceAccountImpersonationConfig, projectName string, opts ...option.ClientOption) (*tokenprovider.TokenExpiry, error) {
	// Construct the service account email from the configured parameters.
	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saConfig.ServiceAccountName, projectName)

	// Configure the impersonation parameters.
	// Define which service account to impersonate and what scopes the token should have.
	config := impersonate.CredentialsConfig{
		TargetPrincipal: saEmail,                 // The service account to impersonate.
		Scopes:          []string{stsTokenScope}, // The desired scopes for the access token.
	}

	// Use the STS token as the source token for impersonation.
	// Create an HTTP client with a custom RoundTripper that adds the Bearer token Authorization header.
	roundTripper, err := newBearerAuthRoundTripper(stsToken)
	if err != nil {
		return nil, fmt.Errorf("error creating BearerAuthRoundTripper: %w", err)
	}
	httpClient := &http.Client{
		Transport: roundTripper,
	}
	// Prepend the HTTP client option so test options can override it.
	opts = append([]option.ClientOption{option.WithHTTPClient(httpClient)}, opts...)

	// Create a token source that will provide tokens with the permissions of the impersonated service account.
	ts, err := impersonate.CredentialsTokenSource(ctx, config, opts...)
	if err != nil {
		return nil, fmt.Errorf("error creating impersonated credentials for service account %s: %w", saEmail, err)
	}

	// Get the token.
	token, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("error getting access token for service account %s: %w", saEmail, err)
	}
	return &tokenprovider.TokenExpiry{
		Token:     token.AccessToken,
		ExpiresAt: token.Expiry,
	}, nil
}

// populateInSecret updates the secret with the GCP access token.
func populateInSecret(secret *corev1.Secret, gcpAuth filterapi.GCPAuth, expiryTime time.Time) {
	updateExpirationSecretAnnotation(secret, expiryTime)
	secret.Data = map[string][]byte{
		GCPAccessTokenKey: []byte(gcpAuth.AccessToken),
		GCPProjectNameKey: []byte(gcpAuth.ProjectName),
		GCPRegionKey:      []byte(gcpAuth.Region),
	}
}
