// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package rotators

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
)

const (
	// AzureAccessTokenKey is the key used to store Azure access token in Kubernetes secrets.
	AzureAccessTokenKey = "azureAccessToken"
)

// azureTokenRotator implements Rotator interface for Azure access token exchange.
type azureTokenRotator struct {
	// client is used for Kubernetes API operations.
	client client.Client
	// kube provides additional API capabilities.
	kube kubernetes.Interface
	// logger is used for structured logging.
	logger logr.Logger
	// backendSecurityPolicyName provides name of backend security policy.
	backendSecurityPolicyName string
	// backendSecurityPolicyNamespace provides namespace of backend security policy.
	backendSecurityPolicyNamespace string
	// preRotationWindow specifies how long before expiry to rotate.
	preRotationWindow time.Duration
	// tokenProvider specifies provider to fetch Azure access token.
	tokenProvider tokenprovider.TokenProvider
}

// NewAzureTokenRotator creates a new azureTokenRotator with the given parameters.
func NewAzureTokenRotator(
	client client.Client,
	kube kubernetes.Interface,
	logger logr.Logger,
	backendSecurityPolicyNamespace string,
	backendSecurityPolicyName string,
	preRotationWindow time.Duration,
	tokenProvider tokenprovider.TokenProvider,
) (Rotator, error) {
	return &azureTokenRotator{
		client:                         client,
		kube:                           kube,
		logger:                         logger.WithName("azure-token-rotator"),
		backendSecurityPolicyNamespace: backendSecurityPolicyNamespace,
		backendSecurityPolicyName:      backendSecurityPolicyName,
		preRotationWindow:              preRotationWindow,
		tokenProvider:                  tokenProvider,
	}, nil
}

// IsExpired implements Rotator.IsExpired method to check if the preRotation time is before the current time.
func (r *azureTokenRotator) IsExpired(preRotationExpirationTime time.Time) bool {
	return IsBufferedTimeExpired(0, preRotationExpirationTime)
}

// GetPreRotationTime implements Rotator.GetPreRotationTime method to retrieve the pre-rotation time for Azure token.
func (r *azureTokenRotator) GetPreRotationTime(ctx context.Context) (time.Time, error) {
	secret, err := LookupSecret(ctx, r.client, r.backendSecurityPolicyNamespace, GetBSPSecretName(r.backendSecurityPolicyName))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	expirationTime, err := GetExpirationSecretAnnotation(secret)
	if err != nil {
		return time.Time{}, err
	}
	preRotationTime := expirationTime.Add(-r.preRotationWindow)
	return preRotationTime, nil
}

// Rotate implements Rotator.Rotate method to rotate Azure access token and updates the Kubernetes secret.
func (r *azureTokenRotator) Rotate(ctx context.Context) (time.Time, error) {
	bspNamespace := r.backendSecurityPolicyNamespace
	bspName := r.backendSecurityPolicyName
	secretName := GetBSPSecretName(bspName)

	r.logger.Info("start rotating azure access token", "namespace", bspNamespace, "name", bspName)

	azureToken, err := r.tokenProvider.GetToken(ctx)
	if err != nil {
		r.logger.Error(err, "failed to get access token via azure client")
		return time.Time{}, err
	}
	secret, err := LookupSecret(ctx, r.client, bspNamespace, secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.Info("creating a new azure access token into secret", "namespace", bspNamespace, "name", bspName)
			secret = &corev1.Secret{
				Name:      secretName,
				Namespace: bspNamespace,
				Type:      corev1.SecretTypeOpaque,
				Data:      make(map[string][]byte),
			}
			populateAzureAccessToken(secret, &azureToken)
			err = r.client.Create(ctx, secret)
			if err != nil {
				r.logger.Error(err, "failed to create azure access token", "namespace", bspNamespace, "name", bspName)
				return time.Time{}, err
			}
			return azureToken.ExpiresAt, nil
		}
		r.logger.Error(err, "failed to lookup azure access token secret", "namespace", bspNamespace, "name", bspName)
		return time.Time{}, err
	}
	r.logger.Info("updating azure access token secret", "namespace", bspNamespace, "name", bspName)

	populateAzureAccessToken(secret, &azureToken)
	err = r.client.Update(ctx, secret)
	if err != nil {
		r.logger.Error(err, "failed to update azure access token", "namespace", bspNamespace, "name", bspName)
		return time.Time{}, err
	}
	return azureToken.ExpiresAt, nil
}

// populateAzureAccessToken updates the secret with the Azure access token.
func populateAzureAccessToken(secret *corev1.Secret, token *tokenprovider.TokenExpiry) {
	updateExpirationSecretAnnotation(secret, token.ExpiresAt)

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data[AzureAccessTokenKey] = []byte(token.Token)
}
