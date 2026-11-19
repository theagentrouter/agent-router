// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package forwardproxy resolves the Secrets referenced by a GatewayConfig forward proxy
// configuration. It is shared by the controller (validation and change propagation) and the
// extension server (Envoy configuration generation).
package forwardproxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
)

const (
	// CACertKey is the Secret key holding the PEM CA bundle used to verify the proxy.
	CACertKey = "ca.crt"
	// UsernameKey is the Secret key holding the Basic auth username.
	UsernameKey = "username"
	// PasswordKey is the Secret key holding the Basic auth password.
	PasswordKey = "password"
	// HeaderValueKey is the Secret key holding the verbatim Proxy-Authorization header value.
	HeaderValueKey = "value"
)

// Credentials holds the resolved secret material for a forward proxy.
type Credentials struct {
	// CACert is the PEM CA bundle used to verify the proxy, or nil to use the system trust store.
	CACert []byte
	// ClientCert and ClientKey are the PEM client certificate and key for mTLS to the proxy, or
	// nil when no client certificate is configured.
	ClientCert, ClientKey []byte
	// ProxyAuthorization is the Proxy-Authorization header value, or "" when no auth is set.
	ProxyAuthorization string
}

// SecretRefs returns the Secrets referenced by fp, resolved in the GatewayConfig namespace.
func SecretRefs(namespace string, fp *aigv1b1.GatewayConfigForwardProxy) []client.ObjectKey {
	if fp == nil {
		return nil
	}
	var refs []client.ObjectKey
	add := func(ref *gwapiv1.SecretObjectReference) {
		if ref != nil {
			refs = append(refs, secretKey(namespace, ref))
		}
	}
	if fp.TLS != nil {
		add(fp.TLS.CACertificateRef)
		add(fp.TLS.ClientCertificateRef)
	}
	if fp.Auth != nil {
		if fp.Auth.Basic != nil {
			add(&fp.Auth.Basic.SecretRef)
		}
		add(fp.Auth.HeaderValueRef)
	}
	return refs
}

// LoadCredentials reads and validates the Secrets referenced by fp from the GatewayConfig
// namespace. It returns an error if a referenced Secret or one of its required keys is missing,
// or if a reference points at another namespace.
func LoadCredentials(ctx context.Context, c client.Reader, namespace string, fp *aigv1b1.GatewayConfigForwardProxy) (*Credentials, error) {
	creds := &Credentials{}
	if fp == nil {
		return creds, nil
	}
	if fp.TLS != nil {
		if ref := fp.TLS.CACertificateRef; ref != nil {
			data, err := readSecret(ctx, c, namespace, ref, CACertKey)
			if err != nil {
				return nil, fmt.Errorf("forward proxy CA certificate: %w", err)
			}
			creds.CACert = data[CACertKey]
		}
		if ref := fp.TLS.ClientCertificateRef; ref != nil {
			data, err := readSecret(ctx, c, namespace, ref, corev1.TLSCertKey, corev1.TLSPrivateKeyKey)
			if err != nil {
				return nil, fmt.Errorf("forward proxy client certificate: %w", err)
			}
			creds.ClientCert, creds.ClientKey = data[corev1.TLSCertKey], data[corev1.TLSPrivateKeyKey]
		}
	}
	if fp.Auth != nil {
		switch {
		case fp.Auth.Basic != nil:
			data, err := readSecret(ctx, c, namespace, &fp.Auth.Basic.SecretRef, UsernameKey, PasswordKey)
			if err != nil {
				return nil, fmt.Errorf("forward proxy basic auth: %w", err)
			}
			username, password := string(data[UsernameKey]), string(data[PasswordKey])
			if strings.Contains(username, ":") {
				return nil, fmt.Errorf("forward proxy basic auth: username must not contain a colon")
			}
			creds.ProxyAuthorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		case fp.Auth.HeaderValueRef != nil:
			data, err := readSecret(ctx, c, namespace, fp.Auth.HeaderValueRef, HeaderValueKey)
			if err != nil {
				return nil, fmt.Errorf("forward proxy auth header value: %w", err)
			}
			creds.ProxyAuthorization = strings.TrimSpace(string(data[HeaderValueKey]))
		}
	}
	return creds, nil
}

func secretKey(namespace string, ref *gwapiv1.SecretObjectReference) client.ObjectKey {
	ns := namespace
	if ref.Namespace != nil && *ref.Namespace != "" {
		ns = string(*ref.Namespace)
	}
	return client.ObjectKey{Namespace: ns, Name: string(ref.Name)}
}

// readSecret reads the referenced Secret and checks that every key is present and non-empty.
func readSecret(ctx context.Context, c client.Reader, namespace string, ref *gwapiv1.SecretObjectReference, keys ...string) (map[string][]byte, error) {
	key := secretKey(namespace, ref)
	if key.Namespace != namespace {
		return nil, fmt.Errorf("secret %s must be in the GatewayConfig namespace %q", key, namespace)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", key, err)
	}
	for _, k := range keys {
		if len(secret.Data[k]) == 0 {
			return nil, fmt.Errorf("secret %s is missing the %q key", key, k)
		}
	}
	return secret.Data, nil
}
