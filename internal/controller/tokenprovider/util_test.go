// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenprovider

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetClientSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(corev1.SchemeGroupVersion,
		&corev1.Secret{},
	)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	secretName, secretNamespace := "secret", "secret-ns"

	secret, err := GetClientSecret(t.Context(), cl, &corev1.SecretReference{
		Name:      secretName,
		Namespace: secretNamespace,
	})
	require.Error(t, err)
	require.Empty(t, secret)

	err = cl.Create(t.Context(), &corev1.Secret{
		Name:      secretName,
		Namespace: secretNamespace,
		Immutable: nil,
		Data: map[string][]byte{
			"client-secret": []byte("client-secret"),
		},
		StringData: nil,
		Type:       "",
	})
	require.NoError(t, err)

	secret, err = GetClientSecret(t.Context(), cl, &corev1.SecretReference{
		Name:      secretName,
		Namespace: secretNamespace,
	})
	require.NoError(t, err)
	require.Equal(t, "client-secret", secret)
}
