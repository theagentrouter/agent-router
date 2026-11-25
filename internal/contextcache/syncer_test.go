// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package contextcache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSource adds a fixed set of entries, then returns err.
type fakeSource struct {
	gate    string
	entries map[string]Entry
	err     error
	calls   int
}

func (s *fakeSource) GateKey() string { return s.gate }

func (s *fakeSource) List(_ context.Context, add func(string, Entry)) error {
	s.calls++
	for k, e := range s.entries {
		add(k, e)
	}
	return s.err
}

// memStore is an in-memory Store for syncer tests. TTLs are ignored except that a gate,
// once taken, is never released, which is enough for a single test.
type memStore struct {
	mu      sync.Mutex
	entries map[string]Entry
	gates   map[string]bool
}

func newMemStore() *memStore {
	return &memStore{entries: map[string]Entry{}, gates: map[string]bool{}}
}

func (m *memStore) Get(_ context.Context, key string) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e, ok, nil
}

func (m *memStore) Set(_ context.Context, key string, e Entry, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = e
	return nil
}

func (m *memStore) SetNX(_ context.Context, key string, e Entry, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; ok {
		return false, nil
	}
	m.entries[key] = e
	return true, nil
}

func (m *memStore) AcquireGate(_ context.Context, key string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gates[key] {
		return false, nil
	}
	m.gates[key] = true
	return true, nil
}

func newTestSyncer(_ *testing.T, src *fakeSource) (*Syncer, Store) {
	store := newMemStore()
	return &Syncer{Store: store, Source: src, Interval: time.Minute}, store
}

func TestSyncer_WritesEntries(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	rc, store := newTestSyncer(t, &fakeSource{gate: "g", entries: map[string]Entry{
		"a": {Name: "c/a", ExpireTime: exp},
		"b": {Name: "c/b", ExpireTime: exp},
	}})

	stats, err := rc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Stats{Seen: 2, Written: 2}, stats)
	e, ok, err := store.Get(context.Background(), "a")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c/a", e.Name)
}

func TestSyncer_DoesNotOverwrite(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	rc, store := newTestSyncer(t, &fakeSource{gate: "g", entries: map[string]Entry{
		"a": {Name: "c/from-list", ExpireTime: exp},
	}})
	require.NoError(t, store.Set(context.Background(), "a", Entry{Name: "c/published", ExpireTime: exp}, time.Minute))

	stats, err := rc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Stats{Seen: 1, Skipped: 1}, stats)
	e, _, _ := store.Get(context.Background(), "a")
	assert.Equal(t, "c/published", e.Name)
}

func TestSyncer_SkipsNearExpiry(t *testing.T) {
	rc, store := newTestSyncer(t, &fakeSource{gate: "g", entries: map[string]Entry{
		"a": {Name: "c/a", ExpireTime: time.Now().Add(time.Second)},
	}})

	stats, err := rc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Stats{Seen: 1, Skipped: 1}, stats)
	_, ok, _ := store.Get(context.Background(), "a")
	assert.False(t, ok)
}

func TestSyncer_GateSkipsSecondRound(t *testing.T) {
	src := &fakeSource{gate: "g"}
	rc, _ := newTestSyncer(t, src)

	_, err := rc.RunOnce(context.Background())
	require.NoError(t, err)
	stats, err := rc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.True(t, stats.Gated)
	assert.Equal(t, 1, src.calls, "a gated round must not list")
}

func TestSyncer_ListErrorKeepsEarlierWrites(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	rc, store := newTestSyncer(t, &fakeSource{
		gate: "g", err: errors.New("boom"),
		entries: map[string]Entry{"a": {Name: "c/a", ExpireTime: exp}},
	})

	_, err := rc.RunOnce(context.Background())
	require.ErrorContains(t, err, "boom")
	_, ok, _ := store.Get(context.Background(), "a")
	assert.True(t, ok)
}

func TestSyncer_RunStopsOnCancel(t *testing.T) {
	rc, _ := newTestSyncer(t, &fakeSource{gate: "g", err: errors.New("boom")})
	rc.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rc.Run(ctx) // A failing round must not stop or panic the loop.
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return when its context is cancelled")
	}
}

func TestNoopStore_SyncerMethods(t *testing.T) {
	var s NoopStore
	wrote, err := s.SetNX(context.Background(), "k", Entry{Name: "n", ExpireTime: time.Now().Add(time.Hour)}, time.Minute)
	require.NoError(t, err)
	assert.False(t, wrote)
	won, err := s.AcquireGate(context.Background(), "g", time.Minute)
	require.NoError(t, err)
	assert.False(t, won)

	src := &fakeSource{gate: "g"}
	stats, err := (&Syncer{Store: s, Source: src, Interval: time.Minute}).RunOnce(context.Background())
	require.NoError(t, err)
	assert.True(t, stats.Gated)
	assert.Equal(t, 0, src.calls)
}
