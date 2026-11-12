// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package contextcache holds the provider-neutral parts of context caching: the shared
// store that maps a deterministic cache key to a provider cache name, and the background
// syncer that repairs drift between that store and the provider.
//
// Provider packages (such as gcpcache) own everything that talks to a provider API:
// computing cache keys, creating caches, and listing them for the syncer.
//
// The shared store deduplicates callers separated in time: once any replica publishes a
// cache name, every replica reuses it. Replicas that miss the same cold prefix at the
// same moment each create their own cache; this is tolerated, and each create is billed
// and reported.
package contextcache

import (
	"context"
	"time"
)

// Entry is a resolved provider cache: its name and when it expires.
type Entry struct {
	// Name identifies the cache to the provider. For GCP Vertex AI it is the full
	// cachedContents resource name.
	Name string
	// ExpireTime is when the provider cache expires.
	ExpireTime time.Time
}

// Store maps a deterministic cache key to the provider cache it resolved to.
//
// The store is shared across gateway replicas, so a cache name published by one
// replica is reused by all of them. Replicas that miss the
// same cold prefix at the same moment may each create a provider cache, and resolvers
// tolerate that. Implementations must be safe for concurrent use.
//
// Store errors are never fatal to a request: resolvers treat them as a cache miss and
// proceed to the provider.
type Store interface {
	// Get returns the entry for key. The bool reports whether a usable entry was
	// found; a miss returns (Entry{}, false, nil).
	Get(ctx context.Context, key string) (Entry, bool, error)
	// Set records e under key, expiring it after ttl.
	Set(ctx context.Context, key string, e Entry, ttl time.Duration) error
	// SetNX records e under key only if key is absent. It reports whether it wrote.
	SetNX(ctx context.Context, key string, e Entry, ttl time.Duration) (bool, error)
	// AcquireGate claims key for ttl. It reports whether the caller won.
	AcquireGate(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// NoopStore is the Store used when no shared store is configured. Every lookup misses
// and every write is discarded, so context caching becomes inert: markers are still
// parsed, but nothing is resolved or created.
//
// This is what makes "store unconfigured" behave identically to "store unreachable"
// without a nil check at each call site.
//
// AcquireGate never wins, so a Syncer over a NoopStore reports every round as gated
// and never lists the provider.
type NoopStore struct{}

// Get implements Store.
func (NoopStore) Get(context.Context, string) (Entry, bool, error) { return Entry{}, false, nil }

// Set implements Store.
func (NoopStore) Set(context.Context, string, Entry, time.Duration) error { return nil }

// SetNX implements Store.
func (NoopStore) SetNX(context.Context, string, Entry, time.Duration) (bool, error) {
	return false, nil
}

// AcquireGate implements Store.
func (NoopStore) AcquireGate(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

var _ Store = NoopStore{}
