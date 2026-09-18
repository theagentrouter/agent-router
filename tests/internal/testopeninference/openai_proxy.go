// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopeninference

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/envoyproxy/ai-gateway/tests/internal/testopenai"
)

// startOpenAIProxy starts the OpenInference proxy container using Docker.
func startOpenAIProxy(ctx context.Context, logger *log.Logger, cassette testopenai.Cassette, openaiBaseURL, otlpEndpoint string) (url string, closer func(), err error) {
	env := map[string]string{
		"OTEL_SERVICE_NAME":           "openai-proxy-test",
		"OTEL_EXPORTER_OTLP_ENDPOINT": otlpEndpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_BSP_SCHEDULE_DELAY":     "100", // Reduce delay for faster tests.
		"PYTHONUNBUFFERED":            "1",   // Enable Python logging for debugging.
	}

	// For Azure cassettes, set Azure env vars instead of OpenAI ones.
	// The Python proxy will detect these and create an AzureOpenAI client.
	if strings.HasPrefix(cassette.String(), "azure-") {
		// Set fake Azure credentials - the cassette will provide the response
		env["AZURE_OPENAI_ENDPOINT"] = openaiBaseURL
		env["AZURE_OPENAI_API_KEY"] = "unused"
		env["OPENAI_API_VERSION"] = "2024-12-01-preview"
	} else {
		// Standard OpenAI - add /v1 to base URL
		env["OPENAI_BASE_URL"] = openaiBaseURL + "/v1"
		env["OPENAI_API_KEY"] = "unused"
	}

	req := testcontainers.ContainerRequest{
		Context: ".", Dockerfile: "Dockerfile.openai_proxy",
		Env:          env,
		ExposedPorts: []string{"8080/tcp"},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.ExtraHosts = []string{"localhost:host-gateway"}
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("8080/tcp").WithStartupTimeout(30 * time.Second),
	}

	proxyCtr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("failed to start proxy container: %w", err)
	}

	closer = func() {
		// Log container output for debugging.
		if logs, logErr := proxyCtr.Logs(ctx); logErr == nil {
			if logBytes, readErr := io.ReadAll(logs); readErr == nil && len(logBytes) > 0 {
				logger.Printf("Container logs:\n%s", string(logBytes))
			}
		}
		_ = proxyCtr.Terminate(ctx)
	}

	port, err := proxyCtr.MappedPort(ctx, "8080")
	if err != nil {
		closer()
		return "", nil, fmt.Errorf("failed to get mapped port: %w", err)
	}

	// Return base URL without /v1 prefix - buildPath in cassettes.go will add it.
	// For Azure cassettes, buildPath uses Azure-specific paths without /v1.
	url = fmt.Sprintf("http://localhost:%s", port.Port())
	return url, closer, nil
}
