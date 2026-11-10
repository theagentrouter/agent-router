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

// reloadReceiver is a [ReloadableConfigReceiver] whose loads a test can hold, to see how many run at once.
type reloadReceiver struct {
	mu        sync.Mutex
	reload    func(context.Context) error
	cfg       *Config
	loads     int
	running   int
	maxAtOnce int
	loadErr   error // the error of the load's context when the load finished
	entered   chan struct{}
	hold      chan struct{}
}

// SetConfigReloader implements [ReloadableConfigReceiver.SetConfigReloader].
func (r *reloadReceiver) SetConfigReloader(reload func(context.Context) error) { r.reload = reload }

// LoadConfig implements [ConfigReceiver.LoadConfig].
func (r *reloadReceiver) LoadConfig(ctx context.Context, cfg *Config) error {
	r.mu.Lock()
	r.loads++
	r.running++
	r.maxAtOnce = max(r.maxAtOnce, r.running)
	entered, hold := r.entered, r.hold
	r.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running--
	r.cfg = cfg
	r.loadErr = ctx.Err()
	return nil
}

// holdLoads makes the next loads signal entered and then wait until hold is closed.
func (r *reloadReceiver) holdLoads(entered, hold chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered, r.hold = entered, hold
}

func (r *reloadReceiver) state() (backend string, loads, maxAtOnce int, loadErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg != nil && len(r.cfg.Backends) > 0 {
		backend = r.cfg.Backends[0].Name
	}
	return backend, r.loads, r.maxAtOnce, r.loadErr
}

// writeReloadBundle writes a one-part bundle with one backend into dir, and sets the modification time of its files.
func writeReloadBundle(t *testing.T, dir, backend string, mtime time.Time) {
	t.Helper()
	raw := []byte("version: dev\nbackends:\n- name: " + backend + "\n")
	part := filepath.Join(dir, "parts", "000")
	require.NoError(t, os.MkdirAll(filepath.Dir(part), 0o700))
	require.NoError(t, os.WriteFile(part, raw, 0o600))
	idx, err := MarshalConfigBundleIndex(&ConfigBundleIndex{
		Checksum: ConfigBundleChecksum(raw),
		Parts:    []ConfigBundlePart{{Name: "part-000", Path: "parts/000"}},
	})
	require.NoError(t, err)
	index := filepath.Join(dir, ConfigBundleIndexFileName)
	require.NoError(t, os.WriteFile(index, idx, 0o600))
	for _, f := range []string{part, index} {
		require.NoError(t, os.Chtimes(f, mtime, mtime))
	}
}

// startReloadWatcher starts a watcher on a bundle with backend "first". It ticks once an hour, so in these tests
// only reload loads a newer bundle.
func startReloadWatcher(t *testing.T) (dir string, rcv *reloadReceiver) {
	t.Helper()
	dir = t.TempDir()
	writeReloadBundle(t, dir, "first", time.Now())
	rcv = &reloadReceiver{}
	logger, _ := newTestLoggerWithBuffer()
	require.NoError(t, StartConfigBundleWatcher(t.Context(), dir, rcv, logger, time.Hour))
	require.NotNil(t, rcv.reload, "the watcher did not give its reload function to the receiver")
	backend, loads, _, _ := rcv.state()
	require.Equal(t, "first", backend)
	require.Equal(t, 1, loads)
	return dir, rcv
}

func TestStartConfigBundleWatcher_reload(t *testing.T) {
	dir, rcv := startReloadWatcher(t)

	require.NoError(t, rcv.reload(t.Context()))
	backend, loads, _, _ := rcv.state()
	require.Equal(t, "first", backend)
	require.Equal(t, 1, loads, "the bundle did not change, so there is nothing to load")

	writeReloadBundle(t, dir, "second", time.Now().Add(time.Minute))
	require.NoError(t, rcv.reload(t.Context()))
	backend, loads, _, _ = rcv.state()
	require.Equal(t, "second", backend)
	require.Equal(t, 2, loads)
}

func TestStartConfigBundleWatcher_concurrentReloadsLoadOnce(t *testing.T) {
	dir, rcv := startReloadWatcher(t)
	entered, hold := make(chan struct{}, 8), make(chan struct{})
	rcv.holdLoads(entered, hold)
	writeReloadBundle(t, dir, "second", time.Now().Add(time.Minute))

	const callers = 8
	errs := make(chan error, callers)
	for range callers {
		go func() { errs <- rcv.reload(t.Context()) }()
	}
	<-entered
	time.Sleep(50 * time.Millisecond) // Gives the other callers time to start a second load, if they could.
	close(hold)
	for range callers {
		require.NoError(t, <-errs)
	}
	backend, loads, maxAtOnce, _ := rcv.state()
	require.Equal(t, "second", backend)
	require.Equal(t, 2, loads, "the initial load and one reload")
	require.Equal(t, 1, maxAtOnce)
}

func TestStartConfigBundleWatcher_reloadOutlivesCallerContext(t *testing.T) {
	dir, rcv := startReloadWatcher(t)
	entered, hold := make(chan struct{}, 1), make(chan struct{})
	rcv.holdLoads(entered, hold)
	writeReloadBundle(t, dir, "second", time.Now().Add(time.Minute))

	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- rcv.reload(ctx) }()
	<-entered
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled, "reload must return when the caller's context ends")

	close(hold)
	require.Eventually(t, func() bool {
		backend, _, _, _ := rcv.state()
		return backend == "second"
	}, time.Second, time.Millisecond)
	_, _, _, loadErr := rcv.state()
	require.NoError(t, loadErr, "the load must not run under the caller's context")
}
