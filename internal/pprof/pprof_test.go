// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package pprof

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

func TestRun_disabled(t *testing.T) {
	t.Setenv(DisableEnvVarKey, "anything")
	ctx := t.Context()
	Run(ctx)
	// Try accessing the pprof server here if needed.
	response, err := http.Get("http://localhost:6060/debug/pprof/") //nolint:bodyclose
	require.Error(t, err)
	require.Nil(t, response)
}

func TestRun_enabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := internaltesting.CaptureOutput("")[0]
	l := log.New(buf, "", 0)
	run(ctx, l)
	// Use eventually to avoid flake when the server is not yet started by the time we access it.
	internaltesting.RequireEventuallyNoError(t, func() error {
		resp, err := http.Get("http://localhost:6060/debug/pprof/cmdline")
		if err != nil {
			return err
		}
		defer func() {
			_ = resp.Body.Close()
		}()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		// Test binary name should be present in the cmdline output.
		if !strings.Contains(string(body), "pprof.test") {
			return fmt.Errorf("unexpected body: %s", string(body))
		}
		return nil
	}, 3*time.Second, 100*time.Millisecond)
	cancel()
	internaltesting.RequireEventuallyNoError(t, func() error {
		logs := buf.String()
		if !strings.Contains(logs, "pprof server shut down gracefully") {
			return fmt.Errorf("pprof server did not shut down gracefully, logs: %s", logs)
		}
		return nil
	}, 3*time.Second, 100*time.Millisecond)
}
