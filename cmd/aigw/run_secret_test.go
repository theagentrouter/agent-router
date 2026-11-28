// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

//go:embed testdata/security_policy_api_key.yaml
var securityPolicyAPIKeyConfig string

func TestRunCmdContext_writeEnvoyResourcesAndRunExtProc_userSecrets(t *testing.T) {
	t.Setenv("AIGW_TEST_CLIENT_KEY", "env-key")
	keyFile := filepath.Join(t.TempDir(), "client-key")
	require.NoError(t, os.WriteFile(keyFile, []byte("file-key"), 0o600))

	for _, tc := range []struct {
		name        string
		data        map[string][]byte
		stringData  map[string]string
		annotations map[string]string
		want        map[string][]byte
	}{
		{
			name: "data",
			data: map[string][]byte{"client1": []byte("encoded-key")},
			want: map[string][]byte{"client1": []byte("encoded-key")},
		},
		{
			name:       "stringData",
			stringData: map[string]string{"client1": "plain-key"},
			want:       map[string][]byte{"client1": []byte("plain-key")},
		},
		{
			name:       "stringData overrides data",
			data:       map[string][]byte{"client1": []byte("old-key"), "other": []byte("untouched-key")},
			stringData: map[string]string{"client1": "replacement"},
			want:       map[string][]byte{"client1": []byte("replacement"), "other": []byte("untouched-key")},
		},
		{
			name:        "environment substitution",
			stringData:  map[string]string{"client1": "placeholder"},
			annotations: map[string]string{"substitution.aigw.run/env/client1": "AIGW_TEST_CLIENT_KEY"},
			want:        map[string][]byte{"client1": []byte("env-key")},
		},
		{
			name:        "file substitution",
			stringData:  map[string]string{"client1": "placeholder"},
			annotations: map[string]string{"substitution.aigw.run/file/client1": keyFile},
			want:        map[string][]byte{"client1": []byte("file-key")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := &corev1.Secret{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{
					Name: "client-keys", Namespace: "default", Annotations: tc.annotations,
				},
				Type: corev1.SecretTypeOpaque, Data: tc.data, StringData: tc.stringData,
			}
			secretYAML, err := yaml.Marshal(secret)
			require.NoError(t, err)
			var output bytes.Buffer
			runCtx := &runCmdContext{
				envoyGatewayResourcesOut: &output,
				stderrLogger:             slog.New(slog.DiscardHandler),
				stderr:                   io.Discard,
				tmpdir:                   t.TempDir(),
				extProcLauncher: func(ctx context.Context, _ []string, _ io.Writer) error {
					<-ctx.Done()
					return nil
				},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, done, _, err := runCtx.writeEnvoyResourcesAndRunExtProc(ctx,
				securityPolicyAPIKeyConfig+"\n---\n"+string(secretYAML))
			require.NoError(t, err)
			cancel()
			require.NoError(t, <-done)

			_, _, _, _, outputSecrets, _, _, _, _, _, _, _, _, policies := requireCollectTranslatedObjects(t, output.String())
			require.Len(t, policies, 1)
			require.Equal(t, "client-keys", string(policies[0].Spec.APIKeyAuth.CredentialRefs[0].Name))
			var credentials []corev1.Secret
			for _, s := range outputSecrets {
				if s.Name == "client-keys" && s.Namespace == "default" {
					credentials = append(credentials, s)
				}
			}
			require.Len(t, credentials, 1, "Envoy Gateway must receive the Secret referenced by SecurityPolicy exactly once")
			require.Equal(t, corev1.SecretTypeOpaque, credentials[0].Type)
			require.Equal(t, tc.want, credentials[0].Data)
			require.Empty(t, credentials[0].StringData)
		})
	}
}
