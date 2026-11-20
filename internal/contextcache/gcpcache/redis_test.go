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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/contextcache"
	"github.com/envoyproxy/ai-gateway/internal/contextcache/redis"
)

// -----------------------------------------------------------------------
// Cross-replica behavior
// -----------------------------------------------------------------------

// Two resolvers stand in for two gateway replicas: separate processes, one shared Redis.
// Once one replica has published a cache name, the other reuses it without calling Google.
func TestResolver_CrossReplica_SecondResolveHitsStore(t *testing.T) {
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	createResp := `{"name":"projects/p/locations/us-central1/cachedContents/new","expireTime":"` + expireISO + `","usageMetadata":{"totalTokenCount":512}}`
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, createResp, http.StatusOK)

	mr := miniredis.RunT(t)
	newReplica := func() *Resolver {
		store, err := redis.NewStore(mr.Addr())
		require.NoError(t, err)
		return resolverWithServer(fake.srv.URL, store)
	}
	a, b := newReplica(), newReplica()

	resA, err := a.Resolve(context.Background(), crossReplicaRequest())
	require.NoError(t, err)
	require.NotNil(t, resA)
	assert.True(t, resA.Created)
	assert.Equal(t, 512, resA.TokenCount)

	resB, err := b.Resolve(context.Background(), crossReplicaRequest())
	require.NoError(t, err)
	require.NotNil(t, resB)
	assert.Equal(t, resA.CacheName, resB.CacheName)
	assert.False(t, resB.Created, "a store hit must not report a create")
	assert.Equal(t, 0, resB.TokenCount)

	assert.Equal(t, 1, fake.creates(), "the second replica must reuse the published cache")
	assert.Equal(t, 0, fake.lists())
}

// Replicas that miss the same cold prefix at the same moment each create a cache. This is
// the accepted cost of not coordinating: each replica keeps the cache it created, and each
// reports the create so the duplicate write is visible in cache-write token metrics.
func TestResolver_CrossReplica_ColdPrefixRaceMayDuplicate(t *testing.T) {
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)

	// Hold every create until both replicas have issued one, so both miss the store.
	const replicas = 2
	var creates atomic.Int64
	arrived := make(chan struct{}, replicas)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s: the request path must only create", r.Method)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		n := creates.Add(1)
		arrived <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"name":"projects/p/locations/us-central1/cachedContents/new-%d","expireTime":"%s","usageMetadata":{"totalTokenCount":512}}`, n, expireISO)
	}))
	t.Cleanup(srv.Close)

	mr := miniredis.RunT(t)

	var done sync.WaitGroup
	results := make([]*contextcache.ResolveResult, replicas)
	errs := make([]error, replicas)
	for i := range replicas {
		store, err := redis.NewStore(mr.Addr())
		require.NoError(t, err)
		r := resolverWithServer(srv.URL, store)
		done.Add(1)
		go func() {
			defer done.Done()
			results[i], errs[i] = r.Resolve(context.Background(), crossReplicaRequest())
		}()
	}
	for range replicas {
		<-arrived
	}
	close(release)
	done.Wait()

	assert.Equal(t, int64(replicas), creates.Load())
	names := map[string]bool{}
	for i, res := range results {
		require.NoError(t, errs[i], "replica %d", i)
		require.NotNil(t, res, "replica %d", i)
		assert.True(t, res.Created, "replica %d performed a create and must report it", i)
		assert.Equal(t, 512, res.TokenCount, "replica %d", i)
		names[res.CacheName] = true
	}
	assert.Len(t, names, replicas, "each replica must keep the cache it created")
}

func crossReplicaRequest() *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("You are helpful.", ephemeralFields()),
			userMsg("Hello"),
		},
	}
}

// The failure policy: an unreachable store degrades caching, it does not fail requests.
// The resolver falls back to Google and the request is served normally.
func TestResolver_StoreUnreachable_FailsOpen(t *testing.T) {
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	createResp := `{"name":"projects/p/locations/us-central1/cachedContents/new","expireTime":"` + expireISO + `","usageMetadata":{"totalTokenCount":512}}`
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, createResp, http.StatusOK)

	mr := miniredis.RunT(t)
	store, err := redis.NewStore(mr.Addr())
	require.NoError(t, err)
	mr.Close() // Every operation now fails to dial.

	r := resolverWithServer(fake.srv.URL, store)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("You are helpful.", ephemeralFields()),
			userMsg("Hello"),
		},
	}

	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err, "a dead store must not fail the request")
	require.NotNil(t, res)
	assert.Equal(t, "projects/p/locations/us-central1/cachedContents/new", res.CacheName)
	assert.Equal(t, 1, fake.creates(), "the resolver falls through to Google")
}
