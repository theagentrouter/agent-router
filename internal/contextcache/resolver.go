// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package contextcache

import (
	"context"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// ResolveResult holds the outcome of a successful cache resolution.
type ResolveResult struct {
	// CacheName is the provider's name for the resolved or created cache. For GCP Vertex
	// AI it is "projects/{project}/locations/{location}/cachedContents/{cache_id}".
	CacheName string
	// Messages is the non-cached remainder of the conversation (messages after the breakpoint).
	Messages []openai.ChatCompletionMessageParamUnion
	// Created is true when this call created a new cache entry (cache-write cost applies).
	Created bool
	// TokenCount is the number of tokens stored in the cache, from the provider's create
	// response. Only populated when Created is true.
	TokenCount int
	// ExpireTime is the cache expiration time reported by the provider.
	ExpireTime time.Time
}

// CacheResolver resolves or creates a provider cache for requests that carry
// Anthropic-style cache_control markers. It is all the request path needs; lifecycle
// methods live on CacheSyncer so request code cannot stop syncing for a shared resolver.
//
// Providers bind their credentials when the implementation is constructed.
type CacheResolver interface {
	// Resolve inspects openAIReq for cache_control markers, resolves or creates the
	// corresponding provider cache, and returns the result. It returns (nil, nil) when
	// the request has no markers.
	// The caller is responsible for injecting ResolveResult.CacheName into the provider
	// request and replacing the request messages with ResolveResult.Messages.
	Resolve(ctx context.Context, openAIReq *openai.ChatCompletionRequest) (*ResolveResult, error)
}
