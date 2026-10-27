// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package runner

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("default port", func(t *testing.T) {
		r := New(logr.Discard(), 0)
		require.Equal(t, DefaultPort, r.port)
	})
	t.Run("custom port", func(t *testing.T) {
		r := New(logr.Discard(), 9999)
		require.Equal(t, 9999, r.port)
	})
}

func TestUpdateConfigs(t *testing.T) {
	t.Run("error when cache not initialized", func(t *testing.T) {
		r := New(logr.Discard(), 0)
		// cache is nil before Start is called.
		err := r.UpdateConfigs(t.Context(), nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "snapshot cache not initialized")
	})

	t.Run("empty configs", func(t *testing.T) {
		r := newRunnerWithCache(t)
		require.NoError(t, r.UpdateConfigs(t.Context(), nil))
		require.Equal(t, int64(1), r.snapshotVersion)
	})

	t.Run("nil configs are filtered", func(t *testing.T) {
		r := newRunnerWithCache(t)
		configs := []*rlsconfv3.RateLimitConfig{nil, nil}
		require.NoError(t, r.UpdateConfigs(t.Context(), configs))
		require.Equal(t, int64(1), r.snapshotVersion)

		snap, err := r.cache.GetSnapshot(NodeID)
		require.NoError(t, err)
		require.Empty(t, snap.GetResources(resourcev3.RateLimitConfigType))
	})

	t.Run("valid configs are stored in snapshot", func(t *testing.T) {
		r := newRunnerWithCache(t)
		configs := []*rlsconfv3.RateLimitConfig{
			{
				Name:   "config1",
				Domain: "test-domain",
			},
			{
				Name:   "config2",
				Domain: "test-domain-2",
			},
		}
		require.NoError(t, r.UpdateConfigs(t.Context(), configs))
		require.Equal(t, int64(1), r.snapshotVersion)

		snap, err := r.cache.GetSnapshot(NodeID)
		require.NoError(t, err)
		resources := snap.GetResources(resourcev3.RateLimitConfigType)
		require.Len(t, resources, 2)
	})

	t.Run("subsequent updates increment version", func(t *testing.T) {
		r := newRunnerWithCache(t)
		cfg := []*rlsconfv3.RateLimitConfig{{Name: "c1", Domain: "d1"}}

		require.NoError(t, r.UpdateConfigs(t.Context(), cfg))
		require.Equal(t, int64(1), r.snapshotVersion)

		require.NoError(t, r.UpdateConfigs(t.Context(), cfg))
		require.Equal(t, int64(2), r.snapshotVersion)

		require.NoError(t, r.UpdateConfigs(t.Context(), cfg))
		require.Equal(t, int64(3), r.snapshotVersion)
	})

	t.Run("version wraps around at MaxInt64", func(t *testing.T) {
		r := newRunnerWithCache(t)
		r.snapshotVersion = math.MaxInt64

		cfg := []*rlsconfv3.RateLimitConfig{{Name: "c1", Domain: "d1"}}
		require.NoError(t, r.UpdateConfigs(t.Context(), cfg))
		require.Equal(t, int64(1), r.snapshotVersion)
	})

	t.Run("mixed nil and valid configs", func(t *testing.T) {
		r := newRunnerWithCache(t)
		configs := []*rlsconfv3.RateLimitConfig{
			nil,
			{Name: "config1", Domain: "test-domain"},
			nil,
			{Name: "config2", Domain: "test-domain-2"},
			nil,
		}
		require.NoError(t, r.UpdateConfigs(t.Context(), configs))

		snap, err := r.cache.GetSnapshot(NodeID)
		require.NoError(t, err)
		resources := snap.GetResources(resourcev3.RateLimitConfigType)
		require.Len(t, resources, 2)
	})

	t.Run("update replaces previous snapshot", func(t *testing.T) {
		r := newRunnerWithCache(t)

		// First update with 2 configs.
		configs1 := []*rlsconfv3.RateLimitConfig{
			{Name: "config1", Domain: "d1"},
			{Name: "config2", Domain: "d2"},
		}
		require.NoError(t, r.UpdateConfigs(t.Context(), configs1))

		snap, err := r.cache.GetSnapshot(NodeID)
		require.NoError(t, err)
		require.Len(t, snap.GetResources(resourcev3.RateLimitConfigType), 2)

		// Second update with 1 config replaces the previous snapshot.
		configs2 := []*rlsconfv3.RateLimitConfig{
			{Name: "config3", Domain: "d3"},
		}
		require.NoError(t, r.UpdateConfigs(t.Context(), configs2))

		snap, err = r.cache.GetSnapshot(NodeID)
		require.NoError(t, err)
		resources := snap.GetResources(resourcev3.RateLimitConfigType)
		require.Len(t, resources, 1)
	})
}

func TestStart(t *testing.T) {
	t.Run("starts and stops with context cancellation", func(t *testing.T) {
		r := New(logr.Discard(), 0)
		ctx, cancel := context.WithCancel(t.Context())

		errCh := make(chan error, 1)
		go func() { errCh <- r.Start(ctx) }()

		// Wait until the cache is initialized (Start has begun).
		require.Eventually(t, func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.cache != nil
		}, 5*time.Second, 50*time.Millisecond)

		// Verify UpdateConfigs works after Start.
		cfg := []*rlsconfv3.RateLimitConfig{{Name: "c1", Domain: "d1"}}
		require.NoError(t, r.UpdateConfigs(t.Context(), cfg))

		cancel()
		require.NoError(t, <-errCh)
	})

	t.Run("no error when stopped before the server starts serving", func(t *testing.T) {
		// Left to chance Serve wins the race every time, so hold Start at the log line
		// sitting between the go statement and Serve to let the stop land first.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		logger := funcr.New(func(_, args string) {
			if strings.Contains(args, "starting rate limit xDS config server") {
				time.Sleep(100 * time.Millisecond)
			}
		}, funcr.Options{})

		require.NoError(t, New(logger, 0).Start(ctx))
	})
}

// newRunnerWithCache creates a Runner with an initialized snapshot cache
// (simulating the state after Start has set up the cache).
func newRunnerWithCache(t *testing.T) *Runner {
	t.Helper()
	r := New(logr.Discard(), 0)
	r.cache = cachev3.NewSnapshotCache(false, cachev3.IDHash{}, nil)
	return r
}
