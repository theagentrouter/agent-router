// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplane

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// TestExtProcHeaderMutationsNotRejected is a regression test for #2826: the external
// processor must not emit mutations that Envoy's default ext_proc mutation_rules reject.
// Setting the reserved x-envoy-original-path header was dropped silently by Envoy and
// counted in ext_proc.rejected_header_mutations on every request. This drives a real
// chat completion through Envoy and asserts the counter stays at zero.
func TestExtProcHeaderMutationsNotRejected(t *testing.T) {
	config := &filterapi.Config{
		Version:  version.Parse(),
		Backends: []filterapi.Backend{testUpstreamOpenAIBackend},
	}
	configBytes, err := yaml.Marshal(config)
	require.NoError(t, err)
	env := startTestEnvironment(t, string(configBytes), true, false)

	listenerAddr := fmt.Sprintf("http://127.0.0.1:%d", env.EnvoyListenerPort())
	adminAddr := fmt.Sprintf("http://127.0.0.1:%d", env.EnvoyAdminPort())

	// Drive a few successful chat completions through the full extproc request path.
	for i := 0; i < 3; i++ {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			listenerAddr+"/v1/chat/completions",
			strings.NewReader(`{"model":"something","messages":[{"role":"system","content":"You are a chatbot."}]}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-test-backend", "openai")
		req.Header.Set(testupstreamlib.ResponseStatusKey, "200")
		req.Header.Set(testupstreamlib.ExpectedPathHeaderKey,
			base64.StdEncoding.EncodeToString([]byte("/v1/chat/completions")))
		req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
			base64.StdEncoding.EncodeToString([]byte(`{"choices":[{"message":{"content":"This is a test."}}]}`)))

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status, body: %s", string(body))
	}

	// Every ext_proc.rejected_header_mutations counter must read zero. Before the fix,
	// each request incremented it by one for the rejected x-envoy-original-path mutation.
	resp, err := http.Get(adminAddr + "/stats?filter=ext_proc.rejected_header_mutations")
	require.NoError(t, err)
	statsBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()

	for _, line := range strings.Split(strings.TrimSpace(string(statsBody)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		require.True(t, ok, "unexpected stats line: %q", line)
		count, err := strconv.Atoi(strings.TrimSpace(value))
		require.NoError(t, err, "unexpected stats line: %q", line)
		require.Zero(t, count, "ext_proc rejected a header mutation: %s", strings.TrimSpace(name))
	}
}
