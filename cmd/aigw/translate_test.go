// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a3 "sigs.k8s.io/gateway-api/apis/v1alpha3"
)

func Test_translate(t *testing.T) {
	for _, tc := range []struct {
		name, in, out string
	}{
		{
			name: "basic",
			in:   "testdata/translate_basic.in.yaml",
			out:  "testdata/translate_basic.out.yaml",
		},
		{
			name: "basic_v1alpha1",
			in:   "testdata/translate_basic_v1alpha1.in.yaml",
			out:  "testdata/translate_basic.out.yaml", // Output should be identical for both API versions
		},
		{
			name: "nonairesources",
			in:   "testdata/translate_nonairesources.yaml",
			// The result should be the same as the input.
			out: "testdata/translate_nonairesources.yaml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			// Multiple files should be supported and duplicated resources should be deduplicated.
			err := translate(t.Context(), []string{tc.in, tc.in}, buf, os.Stderr)
			require.NoError(t, err)
			outBuf, err := os.ReadFile(tc.out)
			require.NoError(t, err)

			outHTTPRoutes, outEnvoyExtensionPolicy, outHTTPRouteFilter,
				outConfigMaps, outSecrets, outDeployments, outServices,
				outBackends, outBackendTLSPolicy, outGatewayClass, outGateway, outEnvoyProxy, outBackendTrafficPolicy, outSecurityPolicy := requireCollectTranslatedObjects(t, buf.String())
			expHTTPRoutes, expEnvoyExtensionPolicy, expHTTPRouteFilter,
				expConfigMaps, expSecrets, expDeployments, expServices,
				expBackends, expBackendTLSPolicy, expGatewayClass, expGateway, expEnvoyProxy, expBackendTrafficPolicy, expSecurityPolicy := requireCollectTranslatedObjects(t, string(outBuf))
			assert.ElementsMatch(t, expHTTPRoutes, outHTTPRoutes)
			assert.ElementsMatch(t, expEnvoyExtensionPolicy, outEnvoyExtensionPolicy)
			assert.ElementsMatch(t, expHTTPRouteFilter, outHTTPRouteFilter)
			assert.ElementsMatch(t, expConfigMaps, outConfigMaps)
			assert.ElementsMatch(t, expSecrets, outSecrets)
			assert.ElementsMatch(t, expDeployments, outDeployments)
			assert.ElementsMatch(t, expServices, outServices)
			assert.ElementsMatch(t, expBackends, outBackends)
			assert.ElementsMatch(t, expBackendTLSPolicy, outBackendTLSPolicy)
			assert.ElementsMatch(t, expGatewayClass, outGatewayClass)
			assert.ElementsMatch(t, expGateway, outGateway)
			assert.ElementsMatch(t, expEnvoyProxy, outEnvoyProxy)
			assert.ElementsMatch(t, expBackendTrafficPolicy, outBackendTrafficPolicy)
			assert.ElementsMatch(t, expSecurityPolicy, outSecurityPolicy)
		})
	}
}

func Test_collectObjects_MCPRouteNamespaces(t *testing.T) {
	const route = `apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: mcp-route
  namespace: team-%[1]s
spec:
  parentRefs:
    - name: gateway-%[1]s
  backendRefs:
    - name: backend-%[1]s
      kind: Backend
      group: gateway.envoyproxy.io
      path: /mcp
`
	a, b := fmt.Sprintf(route, "a"), fmt.Sprintf(route, "b")
	for _, tc := range []struct {
		name  string
		input string
		want  []string
	}{
		{"A+B", a + "---\n" + b, []string{"team-a/mcp-route", "team-b/mcp-route"}},
		{"B+A", b + "---\n" + a, []string{"team-a/mcp-route", "team-b/mcp-route"}},
		{"A+A", a + "---\n" + a, []string{"team-a/mcp-route"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, routes, _, _, _, _, _, _, _, err := collectObjects(tc.input, io.Discard, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			var got []string
			for _, route := range routes {
				got = append(got, route.Namespace+"/"+route.Name)
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

func requireCollectTranslatedObjects(t *testing.T, yamlInput string) (
	outHTTPRoutes []gwapiv1.HTTPRoute,
	outEnvoyExtensionPolicy []egv1a1.EnvoyExtensionPolicy,
	outHTTPRouteFilter []egv1a1.HTTPRouteFilter,
	outConfigMaps []corev1.ConfigMap,
	outSecrets []corev1.Secret,
	outDeployments []appsv1.Deployment,
	outServices []corev1.Service,
	outBackends []egv1a1.Backend,
	outBackendTLSPolicy []gwapiv1a3.BackendTLSPolicy,
	outGatewayClasses []gwapiv1.GatewayClass,
	outGateway []gwapiv1.Gateway,
	outEnvoyProxy []egv1a1.EnvoyProxy,
	outBackendTrafficPolicy []egv1a1.BackendTrafficPolicy,
	outSecurityPolicy []egv1a1.SecurityPolicy,
) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(yamlInput)), 4096)
	for {
		var rawObj runtime.RawExtension
		err := decoder.Decode(&rawObj)
		if errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}

		if len(rawObj.Raw) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		_, _, err = unstructured.UnstructuredJSONScheme.Decode(rawObj.Raw, nil, obj)
		require.NoError(t, err)
		switch obj.GetKind() {
		case "HTTPRoute":
			mustExtractAndAppend(obj, &outHTTPRoutes)
		case "HTTPRouteFilter":
			mustExtractAndAppend(obj, &outHTTPRouteFilter)
		case "EnvoyExtensionPolicy":
			mustExtractAndAppend(obj, &outEnvoyExtensionPolicy)
		case "ConfigMap":
			mustExtractAndAppend(obj, &outConfigMaps)
		case "Secret":
			mustExtractAndAppend(obj, &outSecrets)
		case "Deployment":
			mustExtractAndAppend(obj, &outDeployments)
		case "Service":
			mustExtractAndAppend(obj, &outServices)
		case "Backend":
			mustExtractAndAppend(obj, &outBackends)
		case "BackendTLSPolicy":
			mustExtractAndAppend(obj, &outBackendTLSPolicy)
		case "GatewayClass":
			mustExtractAndAppend(obj, &outGatewayClasses)
		case "Gateway":
			mustExtractAndAppend(obj, &outGateway)
		case "EnvoyProxy":
			mustExtractAndAppend(obj, &outEnvoyProxy)
		case "BackendTrafficPolicy":
			mustExtractAndAppend(obj, &outBackendTrafficPolicy)
		case "SecurityPolicy":
			mustExtractAndAppend(obj, &outSecurityPolicy)
		default:
			t.Fatalf("Skipping unknown kind %q", obj.GetKind())
		}
	}
}

func Test_readYamlsAsString(t *testing.T) {
	tmpDir := t.TempDir()
	p1 := tmpDir + "/file1.yaml"
	err := os.WriteFile(p1, []byte("foo"), 0o600)
	require.NoError(t, err)
	p2 := tmpDir + "/file2.yaml"
	err = os.WriteFile(p2, []byte("bar"), 0o600)
	require.NoError(t, err)

	got, err := readYamlsAsString([]string{p1, p2})
	require.NoError(t, err)
	assert.Equal(t, `foo
---
bar
---
`, got)
}
