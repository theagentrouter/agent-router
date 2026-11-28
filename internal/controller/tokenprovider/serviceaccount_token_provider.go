// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"context"
	"errors"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// serviceAccountTokenExpirationSeconds is the requested lifetime of a ServiceAccount token. The token is only
// used for a single token exchange, so the minimum lifetime accepted by the TokenRequest API is requested.
const serviceAccountTokenExpirationSeconds = 600

// serviceAccountTokenProvider is a provider implements TokenProvider interface for Kubernetes ServiceAccount
// tokens obtained from the TokenRequest API.
type serviceAccountTokenProvider struct {
	kube      kubernetes.Interface
	namespace string
	name      string
	audience  string
}

// NewServiceAccountTokenProvider creates a new TokenProvider that requests a token with the given audience for
// the ServiceAccount namespace/name from the Kubernetes TokenRequest API.
//
// Every GetToken call requests a new token, so each token exchange gets a token with its own "jti" claim.
func NewServiceAccountTokenProvider(kube kubernetes.Interface, namespace, name, audience string) (TokenProvider, error) {
	if kube == nil {
		return nil, errors.New("kubernetes client must not be nil")
	}
	if namespace == "" || name == "" {
		return nil, errors.New("ServiceAccount namespace and name must not be empty")
	}
	if audience == "" {
		return nil, errors.New("ServiceAccount token audience must not be empty")
	}
	return &serviceAccountTokenProvider{kube: kube, namespace: namespace, name: name, audience: audience}, nil
}

// GetToken implements TokenProvider.GetToken method to request a new ServiceAccount token and its expiration time.
func (s *serviceAccountTokenProvider) GetToken(ctx context.Context) (TokenExpiry, error) {
	resp, err := s.kube.CoreV1().ServiceAccounts(s.namespace).CreateToken(ctx, s.name, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{s.audience},
			ExpirationSeconds: ptr.To[int64](serviceAccountTokenExpirationSeconds),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("failed to request token for ServiceAccount %s/%s: %w", s.namespace, s.name, err)
	}
	if resp.Status.Token == "" {
		return TokenExpiry{}, fmt.Errorf("TokenRequest for ServiceAccount %s/%s returned no token", s.namespace, s.name)
	}
	return TokenExpiry{Token: resp.Status.Token, ExpiresAt: resp.Status.ExpirationTimestamp.Time}, nil
}
