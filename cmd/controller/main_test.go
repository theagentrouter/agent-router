// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func Test_parseAndValidateFlags(t *testing.T) {
	t.Run("no flags", func(t *testing.T) {
		f, err := parseAndValidateFlags([]string{})
		require.Equal(t, "envoy-gateway-system", f.envoyGatewayNamespace)
		require.Equal(t, "info", f.extProcLogLevel)
		require.Equal(t, "text", f.extProcLogFormat)
		require.Equal(t, "text", f.logFormat)
		require.False(t, f.extProcEnableRedaction)
		require.Equal(t, "docker.io/envoyproxy/ai-gateway-extproc:latest", f.extProcImage)
		require.Equal(t, corev1.PullIfNotPresent, f.extProcImagePullPolicy)
		require.True(t, f.enableLeaderElection)
		require.Equal(t, "info", f.logLevel.String())
		require.Equal(t, ":1063", f.extensionServerPort)
		require.Equal(t, "/certs", f.tlsCertDir)
		require.Equal(t, "tls.crt", f.tlsCertName)
		require.Equal(t, "tls.key", f.tlsKeyName)
		require.Equal(t, 9443, f.webhookPort)
		require.Equal(t, 4*1024*1024, f.maxRecvMsgSize)
		require.Nil(t, f.spanRequestHeaderAttributes)
		require.Nil(t, f.logRequestHeaderAttributes)
		require.NoError(t, err)
	})
	t.Run("all flags", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			dash string
		}{
			{"single dash", "-"},
			{"double dash", "--"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				args := []string{
					tc.dash + "envoyGatewayNamespace=eg-system",
					tc.dash + "extProcLogLevel=debug",
					tc.dash + "extProcLogFormat=json",
					tc.dash + "logFormat=json",
					tc.dash + "extProcEnableRedaction=true",
					tc.dash + "extProcImage=example.com/extproc:latest",
					tc.dash + "extProcImagePullPolicy=Always",
					tc.dash + "enableLeaderElection=false",
					tc.dash + "logLevel=debug",
					tc.dash + "port=:8080",
					tc.dash + "webhookPort=19443",
					tc.dash + "extProcExtraEnvVars=OTEL_SERVICE_NAME=test;OTEL_TRACES_EXPORTER=console",
					tc.dash + "requestHeaderAttributes=x-tenant-id:tenant.id",
					tc.dash + "spanRequestHeaderAttributes=x-forwarded-proto:url.scheme",
					tc.dash + "logRequestHeaderAttributes=x-forwarded-proto:url.scheme",
					tc.dash + "endpointPrefixes=openai:/v1,cohere:/cohere/v2,anthropic:/anthropic/v1",
					tc.dash + "maxRecvMsgSize=33554432",
					tc.dash + "watchNamespaces=default,envoy-ai-gateway-system",
					tc.dash + "cacheSyncTimeout=5m",
					tc.dash + "mcpSessionEncryptionSeed=my-seed",
					tc.dash + "mcpSessionEncryptionIterations=100",
					tc.dash + "mcpFallbackSessionEncryptionSeed=my-fallback-seed",
					tc.dash + "mcpFallbackSessionEncryptionIterations=200",
				}
				f, err := parseAndValidateFlags(args)
				require.Equal(t, "eg-system", f.envoyGatewayNamespace)
				require.Equal(t, "debug", f.extProcLogLevel)
				require.Equal(t, "json", f.extProcLogFormat)
				require.Equal(t, "json", f.logFormat)
				require.True(t, f.extProcEnableRedaction)
				require.Equal(t, "example.com/extproc:latest", f.extProcImage)
				require.Equal(t, corev1.PullAlways, f.extProcImagePullPolicy)
				require.False(t, f.enableLeaderElection)
				require.Equal(t, "debug", f.logLevel.String())
				require.Equal(t, ":8080", f.extensionServerPort)
				require.Equal(t, 19443, f.webhookPort)
				require.Equal(t, "OTEL_SERVICE_NAME=test;OTEL_TRACES_EXPORTER=console", f.extProcExtraEnvVars)
				require.NotNil(t, f.requestHeaderAttributes)
				require.Equal(t, "x-tenant-id:tenant.id", *f.requestHeaderAttributes)
				require.NotNil(t, f.spanRequestHeaderAttributes)
				require.Equal(t, "x-forwarded-proto:url.scheme", *f.spanRequestHeaderAttributes)
				require.NotNil(t, f.logRequestHeaderAttributes)
				require.Equal(t, "x-forwarded-proto:url.scheme", *f.logRequestHeaderAttributes)
				require.Equal(t, "openai:/v1,cohere:/cohere/v2,anthropic:/anthropic/v1", f.endpointPrefixes)
				require.Equal(t, 32*1024*1024, f.maxRecvMsgSize)
				require.Equal(t, []string{"default", "envoy-ai-gateway-system"}, f.watchNamespaces)
				require.Equal(t, 5*time.Minute, f.cacheSyncTimeout)
				require.Equal(t, "my-seed", f.mcpSessionEncryptionSeed)
				require.Equal(t, 100, f.mcpSessionEncryptionIterations)
				require.Equal(t, "my-fallback-seed", f.mcpFallbackSessionEncryptionSeed)
				require.Equal(t, 200, f.mcpFallbackSessionEncryptionIterations)
				require.NoError(t, err)
			})
		}
	})

	t.Run("invalid flags", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			flags  []string
			expErr string
		}{
			{
				name:   "invalid extProcLogLevel",
				flags:  []string{"--extProcLogLevel=invalid"},
				expErr: "invalid external processor log level: \"invalid\"",
			},
			{
				name:   "invalid logLevel",
				flags:  []string{"--logLevel=invalid"},
				expErr: "invalid log level: \"invalid\"",
			},
			{
				name:   "invalid extProcImagePullPolicy",
				flags:  []string{"--extProcImagePullPolicy=invalid"},
				expErr: "invalid external processor pull policy: \"invalid\"",
			},
			{
				name:   "invalid extProcExtraEnvVars - missing value",
				flags:  []string{"--extProcExtraEnvVars=OTEL_SERVICE_NAME"},
				expErr: "invalid extProc extra env vars",
			},
			{
				name:   "invalid extProcExtraEnvVars - empty key",
				flags:  []string{"--extProcExtraEnvVars==value"},
				expErr: "invalid extProc extra env vars",
			},
			{
				name:   "invalid spanRequestHeaderAttributes - missing colon",
				flags:  []string{"--spanRequestHeaderAttributes=agent-session-id"},
				expErr: "invalid tracing header attributes",
			},
			{
				name:   "invalid logRequestHeaderAttributes - missing colon",
				flags:  []string{"--logRequestHeaderAttributes=agent-session-id"},
				expErr: "invalid access log header attributes",
			},
			{
				name:   "invalid requestHeaderAttributes - missing colon",
				flags:  []string{"--requestHeaderAttributes=agent-session-id"},
				expErr: "invalid request header attributes",
			},
			{
				name:   "invalid spanRequestHeaderAttributes - empty header",
				flags:  []string{"--spanRequestHeaderAttributes=:session.id"},
				expErr: "invalid tracing header attributes",
			},
			{
				name:   "invalid endpointPrefixes - unknown key",
				flags:  []string{"--endpointPrefixes=foo:/x"},
				expErr: "invalid endpoint prefixes",
			},
			{
				name:   "invalid endpointPrefixes - missing colon",
				flags:  []string{"--endpointPrefixes=openai"},
				expErr: "invalid endpoint prefixes",
			},
			{
				name:   "invalid log format",
				flags:  []string{"--logFormat=yaml"},
				expErr: `invalid log format: "yaml", must be "text" or "json"`,
			},
			{
				name:   "invalid extproc log format",
				flags:  []string{"--extProcLogFormat=yaml"},
				expErr: `external processor: invalid log format: "yaml", must be "text" or "json"`,
			},
			{
				name:   "invalid mcp session encryption iterations",
				flags:  []string{"--mcpSessionEncryptionIterations=invalid"},
				expErr: `invalid value "invalid" for flag -mcpSessionEncryptionIterations: parse error`,
			},
			{
				name:   "negative mcp session encryption iterations",
				flags:  []string{"--mcpSessionEncryptionIterations=-1"},
				expErr: "mcp session encryption iterations must be positive: -1",
			},
			{
				name:   "invalid mcp fallback session encryption iterations",
				flags:  []string{"--mcpFallbackSessionEncryptionSeed=fallback", "--mcpFallbackSessionEncryptionIterations=invalid"},
				expErr: `invalid value "invalid" for flag -mcpFallbackSessionEncryptionIterations: parse error`,
			},
			{
				name:   "negative mcp fallback session encryption iterations",
				flags:  []string{"--mcpFallbackSessionEncryptionSeed=fallback", "--mcpFallbackSessionEncryptionIterations=-1"},
				expErr: "mcp fallback session encryption iterations must be positive: -1",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := parseAndValidateFlags(tc.flags)
				require.ErrorContains(t, err, tc.expErr)
			})
		}
	})
}

func Test_newZapOpts(t *testing.T) {
	logTo := func(t *testing.T, logFormat string) string {
		t.Helper()
		var buf bytes.Buffer
		opts := append(newZapOpts(logFormat, zapcore.InfoLevel), zap.WriteTo(&buf))
		zap.New(opts...).Info("starting controller", "address", ":1063")
		return buf.String()
	}

	t.Run("json emits parseable records", func(t *testing.T) {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(logTo(t, internalapi.LogFormatJSON)), &record))
		require.Equal(t, "starting controller", record["msg"])
		require.Equal(t, ":1063", record["address"])
		require.Equal(t, "info", record["level"])
	})

	t.Run("json timestamps stay RFC3339", func(t *testing.T) {
		// zap.JSONEncoder builds the encoder eagerly, which drops controller-runtime's default time
		// encoder unless it is passed explicitly. Without that the ts field is an epoch float, which
		// disagrees with what the console format prints.
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(logTo(t, internalapi.LogFormatJSON)), &record))
		ts, ok := record["ts"].(string)
		require.True(t, ok, "ts must be a string, got %T", record["ts"])
		_, err := time.Parse(time.RFC3339, ts)
		require.NoError(t, err)
	})

	t.Run("text stays console and is the fallback", func(t *testing.T) {
		for _, format := range []string{internalapi.LogFormatText, ""} {
			out := logTo(t, format)
			require.Contains(t, out, "starting controller")
			var record map[string]any
			require.Error(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &record), "console output must not be JSON")
		}
	})
}

func Test_maybePatchAdmissionWebhook(t *testing.T) {
	const ns = "envoy-ai-gateway-system"
	t.Setenv("POD_NAMESPACE", ns)
	c := fake.NewClientBuilder().Build()

	err := maybePatchAdmissionWebhook(t.Context(), c, "")
	require.ErrorContains(t, err, `"envoy-ai-gateway-gateway-pod-mutator.envoy-ai-gateway-system" not found`)

	w := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: mutatingWebhookConfigurationName + "." + ns,
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{},
	}
	err = c.Create(t.Context(), w, &client.CreateOptions{})
	require.NoError(t, err)

	err = maybePatchAdmissionWebhook(t.Context(), c, "")
	require.ErrorContains(t, err, `expected 1 webhook in envoy-ai-gateway-gateway-pod-mutator.envoy-ai-gateway-system, got 0`)

	w.Webhooks = append(w.Webhooks, admissionregistrationv1.MutatingWebhook{
		ClientConfig: admissionregistrationv1.WebhookClientConfig{},
	})
	err = c.Update(t.Context(), w, &client.UpdateOptions{})
	require.NoError(t, err)

	err = maybePatchAdmissionWebhook(t.Context(), c, "/path/to/invalid/bundle")
	require.ErrorContains(t, err, `failed to read CA bundle: open /path/to/invalid/bundle: no such file or directory`)

	p := t.TempDir() + "/bundle"
	err = os.WriteFile(p, []byte("somebundle"), 0o600)
	require.NoError(t, err)
	err = maybePatchAdmissionWebhook(t.Context(), c, p)
	require.NoError(t, err)

	updated := &admissionregistrationv1.MutatingWebhookConfiguration{}
	err = c.Get(t.Context(), client.ObjectKey{Name: w.Name}, updated)
	require.NoError(t, err)
	require.Equal(t, updated.Webhooks[0].ClientConfig.CABundle, []byte("somebundle"))
}

func Test_parseAndValidateFlags_extProcImagePullSecrets(t *testing.T) {
	tests := []struct {
		name     string
		flags    []string
		expected string
		wantErr  bool
	}{
		{
			name:     "no image pull secrets",
			flags:    []string{},
			expected: "",
			wantErr:  false,
		},
		{
			name:     "single image pull secret",
			flags:    []string{"--extProcImagePullSecrets=my-registry-secret"},
			expected: "my-registry-secret",
			wantErr:  false,
		},
		{
			name:     "multiple image pull secrets",
			flags:    []string{"--extProcImagePullSecrets=my-registry-secret;backup-secret;third-secret"},
			expected: "my-registry-secret;backup-secret;third-secret",
			wantErr:  false,
		},
		{
			name:     "image pull secrets with spaces",
			flags:    []string{"--extProcImagePullSecrets= my-registry-secret ; backup-secret "},
			expected: " my-registry-secret ; backup-secret ",
			wantErr:  false,
		},
		{
			name:     "empty string",
			flags:    []string{"--extProcImagePullSecrets="},
			expected: "",
			wantErr:  false,
		},
		{
			name:     "empty secret names (valid - filtered out during parsing)",
			flags:    []string{"--extProcImagePullSecrets=my-secret;;backup-secret"},
			expected: "my-secret;;backup-secret",
			wantErr:  false,
		},
		{
			name:     "only semicolons (valid - results in empty list during parsing)",
			flags:    []string{"--extProcImagePullSecrets=;;;"},
			expected: ";;;",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseAndValidateFlags(tt.flags)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, f.extProcImagePullSecrets)
			}
		})
	}
}

func Test_parseAndValidateFlags_watchNamespaces(t *testing.T) {
	tests := []struct {
		name     string
		flags    []string
		expected []string
	}{
		{"no watch namespaces", []string{}, nil},
		{"single watch namespace", []string{"--watchNamespaces=default"}, []string{"default"}},
		{"multiple watch namespaces", []string{"--watchNamespaces=default,envoy-ai-gateway-system"}, []string{"default", "envoy-ai-gateway-system"}},
		{"watch namespaces with spaces", []string{"--watchNamespaces= default , envoy-ai-gateway-system "}, []string{"default", "envoy-ai-gateway-system"}},
		{"empty string", []string{"--watchNamespaces="}, nil},
		{"empty namespace names", []string{"--watchNamespaces=default,,envoy-ai-gateway-system"}, []string{"default", "envoy-ai-gateway-system"}},
		{"only commas", []string{"--watchNamespaces=,,,"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseAndValidateFlags(tt.flags)
			require.NoError(t, err)
			require.Equal(t, tt.expected, f.watchNamespaces)
		})
	}
}

func TestSetupCache(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		c := setupCache(&flags{})

		require.NotNil(t, c.DefaultTransform)
		require.Nil(t, c.DefaultNamespaces)
	})

	t.Run("empty watch namespaces", func(t *testing.T) {
		c := setupCache(&flags{watchNamespaces: []string{}})

		require.NotNil(t, c.DefaultTransform)
		require.Nil(t, c.DefaultNamespaces)
	})

	t.Run("watch namespaces", func(t *testing.T) {
		c := setupCache(&flags{watchNamespaces: []string{"default", "envoy-ai-gateway-system"}})

		require.NotNil(t, c.DefaultTransform)
		require.Equal(t, map[string]cache.Config{
			"default":                 {},
			"envoy-ai-gateway-system": {},
		}, c.DefaultNamespaces)
	})
}

func Test_handleVersion(t *testing.T) {
	t.Run("version subcommand", func(t *testing.T) {
		out := &bytes.Buffer{}
		require.True(t, handleVersion([]string{"version"}, out))
		require.Equal(t, "Envoy AI Gateway Controller: dev\n", out.String())
	})

	t.Run("no args", func(t *testing.T) {
		out := &bytes.Buffer{}
		require.False(t, handleVersion(nil, out))
		require.Empty(t, out.String())
	})

	t.Run("regular flags", func(t *testing.T) {
		out := &bytes.Buffer{}
		require.False(t, handleVersion([]string{"--logLevel", "debug"}, out))
		require.Empty(t, out.String())
	})
}
