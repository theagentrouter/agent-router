// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
)

// mockReceiver is a mock implementation of Receiver.
type mockReceiver struct {
	cfg *Config
	mux sync.Mutex
}

// LoadConfig implements ConfigReceiver.
func (m *mockReceiver) LoadConfig(_ context.Context, cfg *Config) error {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.cfg = cfg
	return nil
}

func (m *mockReceiver) getConfig() *Config {
	m.mux.Lock()
	defer m.mux.Unlock()
	return m.cfg
}

// newTestLoggerWithBuffer creates a new logger with a buffer for testing and asserting the output.
func newTestLoggerWithBuffer() (*slog.Logger, internaltesting.OutBuffer) {
	buf := internaltesting.CaptureOutput("test")[0]
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	return logger, buf
}

func TestStartConfigBundleWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tmpdir := t.TempDir()
		bundleDir := filepath.Join(tmpdir, "bundle")
		require.NoError(t, os.MkdirAll(filepath.Join(bundleDir, "parts"), 0o700))
		rcv := &mockReceiver{}
		const tickInterval = time.Millisecond

		writeBundle := func(cfg string) {
			raw := []byte(cfg)
			requireAtomicWriteFile(t, tickInterval, filepath.Join(bundleDir, "parts/000"), raw, 0o600)
			idx := &ConfigBundleIndex{
				Checksum: ConfigBundleChecksum(raw),
				Parts: []ConfigBundlePart{
					{Name: "part-000", Path: "parts/000"},
				},
			}
			idxRaw, err := MarshalConfigBundleIndex(idx)
			require.NoError(t, err)
			requireAtomicWriteFile(t, tickInterval, filepath.Join(bundleDir, ConfigBundleIndexFileName), idxRaw, 0o600)
		}

		writeBundle("version: dev\nbackends:\n- name: first\n")
		logger, buf := newTestLoggerWithBuffer()
		err := StartConfigBundleWatcher(t.Context(), bundleDir, rcv, logger, tickInterval)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			cfg := rcv.getConfig()
			return cfg != nil && len(cfg.Backends) == 1 && cfg.Backends[0].Name == "first"
		}, time.Second, tickInterval)

		writeBundle("version: dev\nbackends:\n- name: second\n")
		require.Eventually(t, func() bool {
			cfg := rcv.getConfig()
			return cfg != nil && len(cfg.Backends) == 1 && cfg.Backends[0].Name == "second"
		}, time.Second, tickInterval)
		secondCfg := rcv.getConfig()

		writeBundle("version: some-new-version\nbackends:\n- name: rejected\n")
		time.Sleep(2 * tickInterval)
		require.Equal(t, secondCfg, rcv.getConfig(), "config should not change after a version mismatch")
		require.Eventually(t, func() bool {
			return strings.Contains(buf.String(), "failed to update bundled config")
		}, time.Second, tickInterval, buf.String())
	})
}

// requireAtomicWriteFile creates a temporary file, writes the data to it, and then renames it to the final filename.
// This is an alternative to os.WriteFile but in a way that ensures the write is atomic.
func requireAtomicWriteFile(t *testing.T, tickInterval time.Duration, filename string, data []byte, perm os.FileMode) {
	// Sleep enough to ensure that the new file has a different modification time.
	// In practice, when the extproc is deployed, it will read from the k8s secret,
	// hence the file will have a different modification time (due to the delay caused by Kubernetes secret updates).
	time.Sleep(2 * tickInterval)

	tempFile, err := os.CreateTemp(t.TempDir(), filepath.Base(filename)+".tmp.*")
	require.NoError(t, err, "failed to create temporary file for atomic write")
	tempName := tempFile.Name()
	_, err = tempFile.Write(data)
	require.NoError(t, err, "failed to write data to temporary file")
	err = tempFile.Chmod(perm)
	require.NoError(t, err, "failed to set permissions on temporary file")
	err = tempFile.Close()
	require.NoError(t, err, "failed to close temporary file")
	err = os.Rename(tempName, filename)
	require.NoError(t, err, "failed to rename temporary file to final destination")
}
