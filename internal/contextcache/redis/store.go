// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/envoyproxy/ai-gateway/internal/contextcache"
)

// redisStore is a contextcache.Store backed by Redis, shared across gateway replicas.
type redisStore struct {
	client goredis.UniversalClient
}

// NewStore returns a contextcache.Store backed by the Redis instance at url, which accepts
// either a bare "host:port" or a full "redis://" URL.
//
// No connection is established here; go-redis dials lazily. A Redis that is unreachable
// therefore surfaces as an error from Get/Set, which resolvers treat as a miss.
func NewStore(url string) (contextcache.Store, error) {
	opts, err := parseRedisURL(url)
	if err != nil {
		return nil, err
	}
	return &redisStore{client: goredis.NewClient(opts)}, nil
}

// parseRedisURL accepts both a scheme-qualified URL and a bare host:port.
func parseRedisURL(url string) (*goredis.Options, error) {
	if url == "" {
		return nil, errors.New("contextcache: redis url is empty")
	}
	if strings.Contains(url, "://") {
		opts, err := goredis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("contextcache: invalid redis url: %w", err)
		}
		return opts, nil
	}
	return &goredis.Options{Addr: url}, nil
}

// Get implements Store.
func (s *redisStore) Get(ctx context.Context, key string) (contextcache.Entry, bool, error) {
	v, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return contextcache.Entry{}, false, nil
		}
		return contextcache.Entry{}, false, fmt.Errorf("contextcache: redis get: %w", err)
	}
	e, err := decodeEntry(v)
	if err != nil {
		// A malformed value is treated as a miss rather than an error: it is not worth
		// failing a resolution over, and the next write will overwrite it.
		return contextcache.Entry{}, false, nil
	}
	return e, true, nil
}

// Set implements Store.
func (s *redisStore) Set(ctx context.Context, key string, e contextcache.Entry, ttl time.Duration) error {
	if err := s.client.Set(ctx, key, encodeEntry(e), ttl).Err(); err != nil {
		return fmt.Errorf("contextcache: redis set: %w", err)
	}
	return nil
}

// SetNX implements Store.
func (s *redisStore) SetNX(ctx context.Context, key string, e contextcache.Entry, ttl time.Duration) (bool, error) {
	wrote, err := s.client.SetNX(ctx, key, encodeEntry(e), ttl).Result()
	if err != nil {
		return false, fmt.Errorf("contextcache: redis setnx: %w", err)
	}
	return wrote, nil
}

// AcquireGate implements Store.
func (s *redisStore) AcquireGate(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	won, err := s.client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("contextcache: redis gate: %w", err)
	}
	return won, nil
}

var _ contextcache.Store = (*redisStore)(nil)

// encodeEntry serializes an entry as "<RFC3339 expiry>|<cache name>". The cache name is
// last because it is the only field that may itself contain the separator.
func encodeEntry(e contextcache.Entry) string {
	return e.ExpireTime.UTC().Format(time.RFC3339) + "|" + e.Name
}

func decodeEntry(s string) (contextcache.Entry, error) {
	expiry, name, ok := strings.Cut(s, "|")
	if !ok || name == "" {
		return contextcache.Entry{}, fmt.Errorf("contextcache: malformed cache entry %q", s)
	}
	t, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return contextcache.Entry{}, fmt.Errorf("contextcache: malformed cache entry expiry %q: %w", expiry, err)
	}
	return contextcache.Entry{Name: name, ExpireTime: t}, nil
}
