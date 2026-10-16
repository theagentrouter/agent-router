// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/contextcache"
)

// newTestRedisStore starts an in-process Redis and returns a store pointed at it.
func newTestRedisStore(t *testing.T) (*redisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	s, err := NewStore(mr.Addr())
	require.NoError(t, err)
	return s.(*redisStore), mr
}

func TestParseRedisURL(t *testing.T) {
	t.Run("bare host:port", func(t *testing.T) {
		opts, err := parseRedisURL("localhost:6379")
		require.NoError(t, err)
		assert.Equal(t, "localhost:6379", opts.Addr)
	})
	t.Run("scheme-qualified", func(t *testing.T) {
		opts, err := parseRedisURL("redis://user:pw@localhost:6380/2")
		require.NoError(t, err)
		assert.Equal(t, "localhost:6380", opts.Addr)
		assert.Equal(t, 2, opts.DB)
	})
	t.Run("empty", func(t *testing.T) {
		_, err := parseRedisURL("")
		require.Error(t, err)
	})
	t.Run("malformed", func(t *testing.T) {
		_, err := parseRedisURL("http://localhost:6379")
		require.Error(t, err)
	})
}

func TestRedisStore_SetGetRoundTrip(t *testing.T) {
	s, _ := newTestRedisStore(t)
	ctx := context.Background()
	expire := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)

	require.NoError(t, s.Set(ctx, "k", contextcache.Entry{Name: "projects/p/locations/r/cachedContents/x", ExpireTime: expire}, time.Minute))

	got, ok, err := s.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "projects/p/locations/r/cachedContents/x", got.Name)
	assert.True(t, expire.Equal(got.ExpireTime), "want %s got %s", expire, got.ExpireTime)
}

func TestRedisStore_GetMiss(t *testing.T) {
	s, _ := newTestRedisStore(t)
	_, ok, err := s.Get(context.Background(), "absent")
	require.NoError(t, err)
	assert.False(t, ok)
}

// A value that does not decode is treated as a miss rather than an error: it is not worth
// failing a resolution over, and the next write overwrites it.
func TestRedisStore_MalformedValueIsAMiss(t *testing.T) {
	s, mr := newTestRedisStore(t)
	require.NoError(t, mr.Set("k", "not-an-entry"))

	_, ok, err := s.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEncodeDecodeEntry(t *testing.T) {
	expire := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	// The cache name is encoded last precisely because it may contain the separator.
	e := contextcache.Entry{Name: "projects/p|weird/cachedContents/x", ExpireTime: expire}

	got, err := decodeEntry(encodeEntry(e))
	require.NoError(t, err)
	assert.Equal(t, e.Name, got.Name)
	assert.True(t, expire.Equal(got.ExpireTime))

	for _, bad := range []string{"", "no-separator", "not-a-time|name", "2020-01-01T00:00:00Z|"} {
		_, err := decodeEntry(bad)
		assert.Error(t, err, "input %q", bad)
	}
}
