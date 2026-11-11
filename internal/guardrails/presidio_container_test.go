// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

const presidioAnalyzerImage = "ghcr.io/data-privacy-stack/presidio-analyzer:2.2.364"

func TestPresidioEvaluatorContainer(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := t.Context()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        presidioAnalyzerImage,
			ExposedPorts: []string{"3000/tcp"},
			WaitingFor: wait.ForHTTP("/health").
				WithPort("3000/tcp").
				WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.WithoutCancel(ctx)))
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3000/tcp")
	require.NoError(t, err)

	evaluator, err := newPresidioEvaluator(&filterapi.PresidioGuardrailProvider{
		Endpoint:              fmt.Sprintf("http://%s", net.JoinHostPort(host, port.Port())),
		Language:              "en",
		ScoreThresholdPercent: 80,
	}, "[REDACTED]", &http.Client{Timeout: 10 * time.Second})
	require.NoError(t, err)

	evaluation, err := evaluator.Evaluate(ctx, []byte("Contact me at alice@example.com"), filterapi.GuardrailPhaseRequest)
	require.NoError(t, err)
	require.True(t, evaluation.Matched)
}
