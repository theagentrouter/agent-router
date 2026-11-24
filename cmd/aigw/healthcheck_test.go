// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	func_e "github.com/tetratelabs/func-e"
	"github.com/tetratelabs/func-e/api"
	"github.com/tetratelabs/func-e/experimental/admin"
)

func Test_healthcheck(t *testing.T) {
	pid := os.Getpid()

	t.Run("returns error when no envoy subprocess", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		err := doHealthcheck(ctx, pid, logger)
		require.EqualError(t, err, "timeout waiting for Envoy process: no Envoy process found")
		// Contains not Equal because there's a timestamp
		require.Contains(t, buf.String(), "Failed to find Envoy admin server")
	})

	t.Run("returns nil when ready", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var healthCheckErr error
		var log bytes.Buffer

		// Even though AdminClient.IsReady exists, we don't have it injected in
		// Docker. This intentionally ignores the parameter.
		startupHook := func(ctx context.Context, _ admin.AdminClient, _ string) error {
			logger := slog.New(slog.NewTextHandler(&log, nil))
			healthCheckErr = doHealthcheck(ctx, pid, logger)
			// Cancel immediately to stop Envoy and complete test quickly
			cancel()
			return nil
		}

		// Run with minimal Envoy config
		err := func_e.Run(ctx, []string{
			"--config-yaml",
			"admin: {address: {socket_address: {address: '127.0.0.1', port_value: 0}}}",
		}, api.EnvoyVersion(envoyVersion), api.Out(io.Discard), api.EnvoyOut(io.Discard), api.EnvoyErr(io.Discard), admin.WithStartupHook(startupHook))

		// Expect nil error since Run returns nil on context cancellation (documented behavior)
		require.NoError(t, err)

		require.NoError(t, healthCheckErr)
		require.Empty(t, log)
	})
}
