// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcpcache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/contextcache"
	"github.com/envoyproxy/ai-gateway/internal/contextcache/redis"
)

var syncAuth = &fakeGCPAuth{token: "tok", region: "us-central1", project: "p"}

// newTestRedisStore starts an in-process Redis and returns a store pointed at it.
func newTestRedisStore(t *testing.T) (contextcache.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	s, err := redis.NewStore(mr.Addr())
	require.NoError(t, err)
	return s, mr
}

// syncOnce runs one round for auth against r's store and returns its stats and the
// number of list pages read.
func syncOnce(t *testing.T, r *Resolver, auth *fakeGCPAuth) (contextcache.Stats, int, error) {
	t.Helper()
	rc := r.newSyncer(auth, time.Minute)
	stats, err := rc.RunOnce(context.Background())
	return stats, rc.Source.(*listSource).pages, err
}

func testKey(n int) string { return fmt.Sprintf("%064x", n) }

func itemJSON(key, name string, expire time.Time) string {
	return fmt.Sprintf(`{"name":%q,"displayName":%q,"expireTime":%q}`, name, key, expire.UTC().Format(time.RFC3339))
}

// newListServer serves pages keyed by pageToken ("" is the first page). It fails the test
// on any non-GET, and records every query it saw.
func newListServer(t *testing.T, pages map[string]string) (*httptest.Server, *atomic.Int64, *[]string) {
	t.Helper()
	var calls atomic.Int64
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("syncer issued %s; it must only list", r.Method)
		}
		calls.Add(1)
		queries = append(queries, r.URL.RawQuery)
		body, ok := pages[r.URL.Query().Get("pageToken")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &queries
}

func TestSync_MultiPageWalkWritesEveryEntry(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	srv, calls, queries := newListServer(t, map[string]string{
		"":   `{"cachedContents":[` + itemJSON(testKey(1), "c/1", exp) + `],"nextPageToken":"p2"}`,
		"p2": `{"cachedContents":[` + itemJSON(testKey(2), "c/2", exp) + `]}`,
	})
	store, _ := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	stats, pages, err := syncOnce(t, r, syncAuth)
	require.NoError(t, err)
	assert.Equal(t, 2, pages)
	assert.Equal(t, 2, stats.Written)
	assert.Equal(t, int64(2), calls.Load())
	for _, q := range *queries {
		assert.Contains(t, q, "pageSize=1000")
	}
	for n, name := range map[int]string{1: "c/1", 2: "c/2"} {
		e, ok, err := store.Get(context.Background(), testKey(n))
		require.NoError(t, err)
		require.True(t, ok, "key %d", n)
		assert.Equal(t, name, e.Name)
	}
}

func TestSync_DoesNotOverwriteExistingEntry(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	srv, _, _ := newListServer(t, map[string]string{
		"": `{"cachedContents":[` + itemJSON(testKey(1), "c/from-list", exp) + `]}`,
	})
	store, _ := newTestRedisStore(t)
	require.NoError(t, store.Set(context.Background(), testKey(1), contextcache.Entry{Name: "c/published", ExpireTime: exp}, time.Minute))
	r := resolverWithServer(srv.URL, store)

	stats, _, err := syncOnce(t, r, syncAuth)
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Written)
	e, _, _ := store.Get(context.Background(), testKey(1))
	assert.Equal(t, "c/published", e.Name, "syncer must not clobber a request's write")
}

func TestSync_GateSkipsSecondReplica(t *testing.T) {
	srv, calls, _ := newListServer(t, map[string]string{"": `{"cachedContents":[]}`})
	store, mr := newTestRedisStore(t)
	second, err := redis.NewStore(mr.Addr())
	require.NoError(t, err)
	a := resolverWithServer(srv.URL, store)
	b := resolverWithServer(srv.URL, second)

	statsA, _, err := syncOnce(t, a, syncAuth)
	require.NoError(t, err)
	assert.False(t, statsA.Gated)
	statsB, _, err := syncOnce(t, b, syncAuth)
	require.NoError(t, err)
	assert.True(t, statsB.Gated)
	assert.Equal(t, int64(1), calls.Load())

	// Once the interval passes, a replica may sync again.
	mr.FastForward(time.Minute + time.Second)
	statsB, _, err = syncOnce(t, b, syncAuth)
	require.NoError(t, err)
	assert.False(t, statsB.Gated)
}

func TestSync_GateIsPerProjectRegion(t *testing.T) {
	srv, calls, _ := newListServer(t, map[string]string{"": `{"cachedContents":[]}`})
	store, _ := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)
	other := &fakeGCPAuth{token: "tok", region: "europe-west1", project: "p"}

	_, _, err := syncOnce(t, r, syncAuth)
	require.NoError(t, err)
	stats, _, err := syncOnce(t, r, other)
	require.NoError(t, err)
	assert.False(t, stats.Gated, "one region's round must not suppress another's")
	assert.Equal(t, int64(2), calls.Load())
}

func TestSync_SkipsForeignExpiredAndUnparseable(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	items := []string{
		itemJSON("my-own-cache", "c/foreign", exp),                         // not a cache key
		itemJSON(testKey(1), "c/near-expiry", time.Now().Add(time.Second)), // within staleThreshold
		fmt.Sprintf(`{"name":"c/bad","displayName":%q,"expireTime":"not-a-time"}`, testKey(2)),
		itemJSON(testKey(3), "c/good", exp),
	}
	srv, _, _ := newListServer(t, map[string]string{"": `{"cachedContents":[` + strings.Join(items, ",") + `]}`})
	store, mr := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	stats, _, err := syncOnce(t, r, syncAuth)
	require.NoError(t, err)
	// Foreign and unparseable items are dropped by the source; the near-expiry one is
	// seen but not written.
	assert.Equal(t, 2, stats.Seen)
	assert.Equal(t, 1, stats.Written)
	assert.Equal(t, 1, stats.Skipped)
	assert.False(t, mr.Exists("my-own-cache"))
	assert.False(t, mr.Exists(testKey(1)))
	assert.False(t, mr.Exists(testKey(2)))
	assert.True(t, mr.Exists(testKey(3)))
}

func TestSync_EntryTTLMatchesExpiry(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	srv, _, _ := newListServer(t, map[string]string{"": `{"cachedContents":[` + itemJSON(testKey(1), "c/1", exp) + `]}`})
	store, mr := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	_, _, err := syncOnce(t, r, syncAuth)
	require.NoError(t, err)
	assert.InDelta(t, (5 * time.Minute).Seconds(), mr.TTL(testKey(1)).Seconds(), 5)
}

func TestSync_ListFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	store, _ := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	_, _, err := syncOnce(t, r, syncAuth)
	require.ErrorContains(t, err, "HTTP 500")
}

// A no-op store never wins the gate, so its rounds never list the provider.
func TestSync_NoopStoreIsGatedAndNeverLists(t *testing.T) {
	srv, calls, _ := newListServer(t, map[string]string{"": `{"cachedContents":[]}`})
	r := resolverWithServer(srv.URL)

	stats, _, err := syncOnce(t, r, syncAuth)
	require.NoError(t, err)
	assert.True(t, stats.Gated)
	assert.Equal(t, int64(0), calls.Load())
}

func TestIsCacheKey(t *testing.T) {
	assert.True(t, isCacheKey(testKey(7)))
	assert.False(t, isCacheKey(""))
	assert.False(t, isCacheKey(strings.Repeat("z", 64)))
	assert.False(t, isCacheKey(testKey(7)[:63]))
}

// -----------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------

func TestResolver_StartRunsSyncerAndCloseStopsIt(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	srv, calls, _ := newListServer(t, map[string]string{
		"": `{"cachedContents":[` + itemJSON(testKey(1), "c/1", exp) + `]}`,
	})
	store, mr := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	r.Start()
	require.Eventually(t, func() bool { return mr.Exists(testKey(1)) }, 2*time.Second, 10*time.Millisecond,
		"Start must run a sync round")

	require.NoError(t, r.Close())
	select {
	case <-r.done:
	default:
		t.Fatal("Close must wait for the syncer goroutine to exit")
	}
	assert.Equal(t, int64(1), calls.Load())
}

func TestResolver_StartIsIdempotent(t *testing.T) {
	srv, _, _ := newListServer(t, map[string]string{"": `{"cachedContents":[]}`})
	store, _ := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	r.Start()
	first := r.done
	r.Start()
	assert.Equal(t, first, r.done, "a second Start must not launch another syncer")
	require.NoError(t, r.Close())
}

func TestResolver_CloseIsIdempotentAndBlocksLaterStart(t *testing.T) {
	srv, calls, _ := newListServer(t, map[string]string{"": `{"cachedContents":[]}`})
	store, _ := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)

	require.NoError(t, r.Close(), "Close before Start must be safe")
	require.NoError(t, r.Close())
	r.Start()
	assert.Nil(t, r.cancel, "Start after Close must do nothing")
	assert.Equal(t, int64(0), calls.Load())
}

func TestResolver_StartWithNoopStoreClosesCleanly(t *testing.T) {
	r := New(nil, nil, syncAuth, nil)
	r.Start()
	require.NoError(t, r.Close())
	select {
	case <-r.done:
	default:
		t.Fatal("Close must wait for the syncer goroutine to exit")
	}
}

// The syncer reads the resolver's current credentials each round, so SetAuth after a
// config reload moves the next round to the new project and region.
func TestSync_FollowsSetAuth(t *testing.T) {
	srv, _, queries := newListServer(t, map[string]string{"": `{"cachedContents":[]}`})
	store, _ := newTestRedisStore(t)
	r := resolverWithServer(srv.URL, store)
	rc := r.newSyncer(nil, time.Minute)

	assert.Equal(t, syncGatePrefix+"p/us-central1", rc.Source.GateKey())
	r.SetAuth(&fakeGCPAuth{token: "tok2", region: "europe-west1", project: "q"})
	assert.Equal(t, syncGatePrefix+"q/europe-west1", rc.Source.GateKey())

	_, err := rc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, *queries, 1)
}

// Resolve uses the credentials set most recently, so a rotated token reaches GCP.
func TestResolver_SetAuthRotatesToken(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		_, _ = fmt.Fprintf(w, `{"name":"c/x","expireTime":%q}`, time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339))
	}))
	t.Cleanup(srv.Close)
	r := resolverWithServer(srv.URL)
	r.SetAuth(&fakeGCPAuth{token: "rotated", region: "us-central1", project: "p"})

	_, err := r.Resolve(context.Background(), crossReplicaRequest())
	require.NoError(t, err)
	assert.Equal(t, "Bearer rotated", gotAuth.Load())
}
