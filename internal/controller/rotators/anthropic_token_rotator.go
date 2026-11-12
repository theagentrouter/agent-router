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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
)

const (
	// AnthropicAccessTokenKey is the key used to store Anthropic access token in Kubernetes secrets.
	AnthropicAccessTokenKey = "anthropicAccessToken" // #nosec G101
)

// anthropicTokenRotator implements Rotator interface for Anthropic access token exchange.
type anthropicTokenRotator struct {
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
	// tokenProvider specifies provider to fetch Anthropic access token.
	tokenProvider tokenprovider.TokenProvider
}

// NewAnthropicTokenRotator creates a new anthropicTokenRotator with the given parameters.
func NewAnthropicTokenRotator(
	client client.Client,
	kube kubernetes.Interface,
	logger logr.Logger,
	backendSecurityPolicyNamespace string,
	backendSecurityPolicyName string,
	preRotationWindow time.Duration,
	tokenProvider tokenprovider.TokenProvider,
) (Rotator, error) {
	return &anthropicTokenRotator{
		client:                         client,
		kube:                           kube,
		logger:                         logger.WithName("anthropic-token-rotator"),
		backendSecurityPolicyNamespace: backendSecurityPolicyNamespace,
		backendSecurityPolicyName:      backendSecurityPolicyName,
		preRotationWindow:              preRotationWindow,
		tokenProvider:                  tokenProvider,
	}, nil
}

// IsExpired implements Rotator.IsExpired method to check if the preRotation time is before the current time.
func (r *anthropicTokenRotator) IsExpired(preRotationExpirationTime time.Time) bool {
	return IsBufferedTimeExpired(0, preRotationExpirationTime)
}

// GetPreRotationTime implements Rotator.GetPreRotationTime method to retrieve the pre-rotation time for Anthropic token.
func (r *anthropicTokenRotator) GetPreRotationTime(ctx context.Context) (time.Time, error) {
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

// Rotate implements Rotator.Rotate method to rotate Anthropic access token and updates the Kubernetes secret.
func (r *anthropicTokenRotator) Rotate(ctx context.Context) (time.Time, error) {
	bspNamespace := r.backendSecurityPolicyNamespace
	bspName := r.backendSecurityPolicyName
	secretName := GetBSPSecretName(bspName)

	r.logger.Info("start rotating Anthropic access token", "namespace", bspNamespace, "name", bspName)

	anthropicToken, err := r.tokenProvider.GetToken(ctx)
	if err != nil {
		r.logger.Error(err, "failed to get access token via token exchange")
		return time.Time{}, err
	}
	secret, err := LookupSecret(ctx, r.client, bspNamespace, secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.Info("creating a new Anthropic access token into secret", "namespace", bspNamespace, "name", bspName)
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: bspNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: make(map[string][]byte),
			}
			populateAnthropicAccessToken(secret, &anthropicToken)
			err = r.client.Create(ctx, secret)
			if err != nil {
				r.logger.Error(err, "failed to create Anthropic access token", "namespace", bspNamespace, "name", bspName)
				return time.Time{}, err
			}
			return anthropicToken.ExpiresAt, nil
		}
		r.logger.Error(err, "failed to lookup Anthropic access token secret", "namespace", bspNamespace, "name", bspName)
		return time.Time{}, err
	}
	r.logger.Info("updating Anthropic access token secret", "namespace", bspNamespace, "name", bspName)

	populateAnthropicAccessToken(secret, &anthropicToken)
	err = r.client.Update(ctx, secret)
	if err != nil {
		r.logger.Error(err, "failed to update Anthropic access token", "namespace", bspNamespace, "name", bspName)
		return time.Time{}, err
	}
	return anthropicToken.ExpiresAt, nil
}

// populateAnthropicAccessToken updates the secret with the Anthropic access token.
func populateAnthropicAccessToken(secret *corev1.Secret, token *tokenprovider.TokenExpiry) {
	updateExpirationSecretAnnotation(secret, token.ExpiresAt)

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data[AnthropicAccessTokenKey] = []byte(token.Token)
}
